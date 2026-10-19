package video

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"gostream/internal/auth"
	"gostream/internal/database"
)

// TestLikeHTTPIntegration targets the running local server, including main's routing.
// Temporary database fixtures and directly signed tokens isolate it from real accounts.
func TestLikeHTTPIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_HTTP_INTEGRATION_TEST") != "1" {
		t.Skip("set GOSTREAM_HTTP_INTEGRATION_TEST=1 with server running on 127.0.0.1:8080")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read project config")
	}
	db, err := database.OpenDB(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("cannot connect to configured database")
	}
	t.Cleanup(func() { db.Close() })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "liketest_" + hex.EncodeToString(random[:])
	var userIDs []uint64
	var videoID uint64
	t.Cleanup(func() {
		if videoID != 0 {
			if _, err := db.Exec("DELETE FROM videos WHERE id = ? AND title = ?", videoID, prefix); err != nil {
				t.Error("cannot clean test video")
			}
		}
		for _, uid := range userIDs {
			if _, err := db.Exec("DELETE FROM video_likes WHERE user_id = ?", uid); err != nil {
				t.Error("cannot clean test likes")
			}
			if _, err := db.Exec("DELETE FROM users WHERE id = ? AND username IN (?, ?)", uid, prefix+"a", prefix+"b"); err != nil {
				t.Error("cannot clean test account")
			}
		}
	})
	tokens := make([]string, 2)
	for i, suffix := range []string{"a", "b"} {
		result, err := db.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", prefix+suffix, "test-only-not-used-for-login")
		if err != nil {
			t.Fatal("cannot create test account")
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		userIDs = append(userIDs, uint64(id))
		tokens[i], err = auth.GenerateToken(uint64(id), []byte(config["JWT_SECRET"]))
		if err != nil {
			t.Fatal("cannot generate fixture token")
		}
	}
	result, err := db.Exec("INSERT INTO videos (author_id, title, play_url, cover_url) VALUES (?, ?, ?, ?)", userIDs[0], prefix, "/media/videos/test-only.mp4", "/cover.svg")
	if err != nil {
		t.Fatal("cannot create test video")
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	videoID = uint64(id)
	path := "/videos/" + strconv.FormatUint(videoID, 10) + "/like"
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(t *testing.T, method, target, token string, want int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, "http://127.0.0.1:8080"+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("cannot reach the running local server")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s: expected %d, got %d", method, target, want, response.StatusCode)
		}
		if want == 204 && len(data) != 0 {
			t.Fatal("204 must have no body")
		}
		return data
	}
	status := func(t *testing.T, token string, count uint64, liked bool) {
		t.Helper()
		data := request(t, http.MethodGet, path, token, 200)
		var got struct {
			Count *uint64 `json:"like_count"`
			Liked *bool   `json:"liked"`
		}
		if err := json.Unmarshal(data, &got); err != nil || got.Count == nil || got.Liked == nil {
			t.Fatal("expected like_count and liked JSON fields")
		}
		if *got.Count != count || *got.Liked != liked {
			t.Fatalf("expected count=%d liked=%v, got count=%d liked=%v", count, liked, *got.Count, *got.Liked)
		}
		var actual uint64
		if err := db.QueryRow("SELECT COUNT(*) FROM video_likes WHERE video_id = ?", videoID).Scan(&actual); err != nil || actual != count {
			t.Fatal("GET response or side effects disagree with stored count")
		}
	}
	t.Run("query_is_read_only", func(t *testing.T) {
		status(t, tokens[0], 0, false)
		status(t, tokens[0], 0, false)
		status(t, tokens[1], 0, false)
	})
	t.Run("one_like_different_personal_status", func(t *testing.T) {
		request(t, http.MethodPut, path, tokens[0], 204)
		status(t, tokens[0], 1, true)
		status(t, tokens[1], 1, false)
	})
	t.Run("repeat_like_does_not_increase_count", func(t *testing.T) { request(t, http.MethodPut, path, tokens[0], 204); status(t, tokens[0], 1, true) })
	t.Run("both_users_liked", func(t *testing.T) {
		request(t, http.MethodPut, path, tokens[1], 204)
		status(t, tokens[0], 2, true)
		status(t, tokens[1], 2, true)
	})
	t.Run("cancel_changes_only_current_user", func(t *testing.T) {
		request(t, http.MethodDelete, path, tokens[0], 204)
		status(t, tokens[0], 1, false)
		status(t, tokens[1], 1, true)
	})
	t.Run("repeat_cancel_keeps_status", func(t *testing.T) {
		request(t, http.MethodDelete, path, tokens[0], 204)
		status(t, tokens[0], 1, false)
	})
	t.Run("query_requires_token", func(t *testing.T) { request(t, http.MethodGet, path, "", 401) })
	t.Run("query_invalid_id", func(t *testing.T) {
		request(t, http.MethodGet, "/videos/abc/like", tokens[0], 400)
		request(t, http.MethodGet, "/videos/0/like", tokens[0], 400)
	})
	t.Run("query_missing_video", func(t *testing.T) { request(t, http.MethodGet, "/videos/18446744073709551615/like", tokens[0], 404) })
}
