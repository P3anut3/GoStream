package video

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"

	"gostream/internal/auth"
	"gostream/internal/database"
	"gostream/internal/user"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/joho/godotenv"
)

// TestDeleteIntegration uses the configured MySQL database only when explicitly enabled.
// Requests run in process without starting a listener. All fixtures are cleaned up.
func TestDeleteIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_INTEGRATION_TEST") != "1" {
		t.Skip("set GOSTREAM_INTEGRATION_TEST=1 to run against local MySQL")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read project .env")
	}
	db, err := database.OpenDB(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("cannot connect to configured MySQL")
	}
	t.Cleanup(func() { db.Close() })
	secret := []byte(config["JWT_SECRET"])
	if len(secret) < 32 {
		t.Fatal("JWT_SECRET must contain at least 32 bytes")
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("cannot generate fixture identifier")
	}
	prefix := "deltest_" + hex.EncodeToString(random[:])
	h := server.New()
	users := &user.UserHandler{DB: db, JWTSecret: secret}
	videos := &Handler{DB: db}
	h.POST("/users", users.CreateUser)
	h.POST("/login", users.Login)
	h.POST("/videos", auth.JWTAuth(secret), videos.Publish)
	h.GET("/videos/:id", videos.GetByID)
	h.DELETE("/videos/:id", auth.JWTAuth(secret), videos.Delete)

	request := func(method, path, token string, payload interface{}) (int, []byte) {
		t.Helper()
		var body *ut.Body
		headers := []ut.Header{}
		if payload != nil {
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatal("cannot encode test request")
			}
			body = &ut.Body{Body: bytes.NewReader(data), Len: len(data)}
			headers = append(headers, ut.Header{Key: "Content-Type", Value: "application/json"})
		}
		if token != "" {
			headers = append(headers, ut.Header{Key: "Authorization", Value: "Bearer " + token})
		}
		resp := ut.PerformRequest(h.Engine, method, path, body, headers...).Result()
		return resp.StatusCode(), append([]byte(nil), resp.Body()...)
	}
	createAccount := func(suffix string) (uint64, string) {
		t.Helper()
		name := prefix + suffix
		payload := map[string]string{"username": name, "password": "integration-test-password"}
		status, data := request(http.MethodPost, "/users", "", payload)
		if status != http.StatusCreated {
			t.Fatalf("fixture registration: expected 201, got %d", status)
		}
		// Cleanup matches this run's exact unique username; videos go first for the FK.
		t.Cleanup(func() {
			if _, err := db.Exec("DELETE FROM videos WHERE author_id IN (SELECT id FROM users WHERE username = ?)", name); err != nil {
				t.Error("failed to clean fixture videos")
			}
			if _, err := db.Exec("DELETE FROM users WHERE username = ?", name); err != nil {
				t.Error("failed to clean fixture account")
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", name).Scan(&count); err != nil || count != 0 {
				t.Error("fixture cleanup verification failed")
			}
		})
		var account struct {
			ID uint64 `json:"id"`
		}
		if err := json.Unmarshal(data, &account); err != nil || account.ID == 0 {
			t.Fatal("registration did not return a valid user ID")
		}
		status, data = request(http.MethodPost, "/login", "", payload)
		if status != http.StatusOK {
			t.Fatalf("fixture login: expected 200, got %d", status)
		}
		var login struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(data, &login); err != nil || login.Token == "" {
			t.Fatal("login did not return a token")
		}
		return account.ID, login.Token
	}
	ownerID, ownerToken := createAccount("a")
	_, otherToken := createAccount("b")
	status, data := request(http.MethodPost, "/videos", ownerToken, map[string]string{
		"title": prefix, "play_url": "https://example.com/test.mp4", "cover_url": "https://example.com/test.jpg",
	})
	if status != http.StatusCreated {
		t.Fatalf("fixture video publication: expected 201, got %d", status)
	}
	var published struct {
		ID       uint64 `json:"id"`
		AuthorID uint64 `json:"author_id"`
	}
	if err := json.Unmarshal(data, &published); err != nil || published.ID == 0 || published.AuthorID != ownerID {
		t.Fatal("publication did not return expected ownership")
	}
	path := "/videos/" + strconv.FormatUint(published.ID, 10)
	cases := []struct {
		name, method, path, token string
		want                      int
	}{
		{"without_token", http.MethodDelete, path, "", 401},
		{"invalid_token", http.MethodDelete, path, "invalid", 401},
		{"invalid_id", http.MethodDelete, "/videos/abc", ownerToken, 400},
		{"zero_id", http.MethodDelete, "/videos/0", ownerToken, 400},
		{"other_user_cannot_delete", http.MethodDelete, path, otherToken, 404},
		{"video_survives_unauthorized_attempts", http.MethodGet, path, "", 200},
		{"owner_can_delete", http.MethodDelete, path, ownerToken, 204},
		{"video_is_gone", http.MethodGet, path, "", 404},
		{"repeat_delete", http.MethodDelete, path, ownerToken, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, body := request(tc.method, tc.path, tc.token, nil)
			if got != tc.want {
				t.Errorf("expected status %d, got %d", tc.want, got)
			}
			if tc.want == http.StatusNoContent && len(body) != 0 {
				t.Error("204 response must have an empty body")
			}
		})
	}
}
