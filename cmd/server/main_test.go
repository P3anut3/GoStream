package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/joho/godotenv"
	"gostream/internal/auth"
	"gostream/internal/database"
)

func TestRouterStaticFilesAndAuthentication(t *testing.T) {
	root := t.TempDir()
	storage := newTestStorage(t)
	for _, dir := range []string{filepath.Join(root, "web/dist/assets"), filepath.Join(root, "uploads/videos"), filepath.Join(root, "uploads/covers"), storage.VideoDir, storage.CoverDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"index.html", "assets/app-fixture.js", "assets/style-fixture.css", "cover.svg"} {
		if err := os.WriteFile(filepath.Join(root, "web/dist", name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	content := []byte("0123456789abcdef")
	for dir, name := range map[string]string{storage.VideoDir: "demo.mp4", storage.CoverDir: "demo.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Decoys prove media is served from the injected directories, not old defaults.
	for dir, name := range map[string]string{"uploads/videos": "demo.mp4", "uploads/covers": "demo.png"} {
		for _, filename := range []string{name, "legacy-only.bin"} {
			if err := os.WriteFile(filepath.Join(root, dir, filename), []byte("wrong default directory"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Chdir(root)
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	router := newRouter(nil, secret, newTestFeedCursor(t), storage, nil)
	for _, tc := range []struct {
		method, path, rangeHeader string
		want                      int
	}{
		{"GET", "/", "", 200}, {"HEAD", "/", "", 200}, {"GET", "/assets/app-fixture.js", "", 200}, {"GET", "/assets/style-fixture.css", "", 200}, {"GET", "/cover.svg", "", 200},
		{"GET", "/assets/", "", 404}, {"GET", "/assets/missing.js", "", 404}, {"GET", "/frontend/src/main.ts", "", 404}, {"GET", "/unknown-api", "", 404},
		{"GET", "/media/videos/demo.mp4", "", 200}, {"HEAD", "/media/videos/demo.mp4", "", 200},
		{"GET", "/media/videos/demo.mp4", "bytes=0-3", 206}, {"GET", "/media/videos/", "", 404},
		{"GET", "/media/covers/demo.png", "", 200}, {"HEAD", "/media/covers/demo.png", "", 200},
		{"GET", "/media/covers/demo.png", "bytes=0-3", 206},
		{"GET", "/media/videos/legacy-only.bin", "", 404}, {"GET", "/media/covers/legacy-only.bin", "", 404},
		{"GET", "/media/videos/missing.mp4", "", 404}, {"GET", "/media/covers/", "", 404},
		{"GET", "/me", "", 401}, {"GET", "/me/videos", "", 401}, {"POST", "/videos", "", 401},
		{"POST", "/uploads/videos", "", 401}, {"POST", "/uploads/covers", "", 401},
		{"GET", "/videos/abc", "", 400}, {"GET", "/videos/abc/comments", "", 400},
	} {
		t.Run(tc.method+tc.path+tc.rangeHeader, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.rangeHeader != "" {
				req.Header.Set("Range", tc.rangeHeader)
			}
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, resp.Code, resp.Body)
			}
			if tc.method == "HEAD" && resp.Body.Len() != 0 {
				t.Error("HEAD must not return a body")
			}
			if tc.want == 206 && resp.Body.String() != "0123" {
				t.Error("incorrect byte range")
			}
			if tc.want == 200 && tc.method == "GET" && strings.HasPrefix(tc.path, "/media/") && !bytes.Equal(resp.Body.Bytes(), content) {
				t.Error("media must come from the configured storage directory")
			}
			if tc.want == 206 && resp.Header().Get("Content-Range") != "bytes 0-3/16" {
				t.Error("incorrect Content-Range")
			}
		})
	}
	t.Run("request_limit_before_handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/uploads/videos", strings.NewReader("tiny"))
		req.ContentLength = 101*1024*1024 + 1
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != 413 {
			t.Fatalf("expected 413, got %d", resp.Code)
		}
	})
}

// Exercises the actual production Gin router over HTTP using GORM and MySQL.
// Only exact generated user fixtures and temporary media directories are used.
func TestGinGORMHTTPIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_INTEGRATION_TEST") != "1" {
		t.Skip("set GOSTREAM_INTEGRATION_TEST=1 for local MySQL")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read local config")
	}
	db, err := database.OpenDB(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("cannot connect to configured database")
	}
	defer db.Close()
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := "gin_" + hex.EncodeToString(random[:])
	otherName := name + "_b"
	defer func() {
		for _, fixtureName := range []string{otherName, name} {
			for _, query := range []string{
				"DELETE FROM videos WHERE author_id IN (SELECT id FROM users WHERE username=?)",
				"DELETE FROM uploads WHERE user_id IN (SELECT id FROM users WHERE username=?)",
				"DELETE FROM video_likes WHERE user_id IN (SELECT id FROM users WHERE username=?)",
				"DELETE FROM video_comments WHERE user_id IN (SELECT id FROM users WHERE username=?)",
				"DELETE FROM users WHERE username=?",
			} {
				if _, err := db.Exec(query, fixtureName); err != nil {
					t.Error(err)
				}
			}
		}
	}()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web/dist"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "web/dist/index.html"), []byte("GoStream"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	storage := newTestStorage(t)
	server := httptest.NewServer(newRouter(database.WrapSQL(db), []byte(config["JWT_SECRET"]), newTestFeedCursor(t), storage, nil))
	defer server.Close()
	client := server.Client()
	request := func(t *testing.T, method, path, token string, payload any) (int, []byte) {
		t.Helper()
		var body io.Reader
		if payload != nil {
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(data)
		}
		req, err := http.NewRequest(method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	credentials := map[string]string{"username": name, "password": "integration-test-password"}
	if status, _ := request(t, "POST", "/users", "", credentials); status != 201 {
		t.Fatalf("registration expected 201, got %d", status)
	}
	if status, _ := request(t, "POST", "/users", "", credentials); status != 409 {
		t.Fatalf("duplicate registration expected 409, got %d", status)
	}
	status, data := request(t, "POST", "/login", "", credentials)
	var login struct {
		ID    uint64 `json:"id"`
		Token string `json:"token"`
	}
	if status != 200 || json.Unmarshal(data, &login) != nil || login.ID == 0 || login.Token == "" {
		t.Fatal("login failed")
	}
	if status, data := request(t, "GET", "/me", login.Token, nil); status != 200 || !strings.Contains(string(data), name) {
		t.Fatal("authenticated user lookup failed")
	}
	if _, err := auth.ParseToken(login.Token, []byte(config["JWT_SECRET"])); err != nil {
		t.Fatal("issued token invalid")
	}
	otherCredentials := map[string]string{"username": otherName, "password": "integration-test-password"}
	if status, _ := request(t, "POST", "/users", "", otherCredentials); status != 201 {
		t.Fatal("second account registration failed")
	}
	status, data = request(t, "POST", "/login", "", otherCredentials)
	var other struct {
		ID    uint64 `json:"id"`
		Token string `json:"token"`
	}
	if status != 200 || json.Unmarshal(data, &other) != nil || other.ID == 0 || other.ID == login.ID || other.Token == "" {
		t.Fatal("second account login failed")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "demo.mp4")
	if err != nil {
		t.Fatal(err)
	}
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2', 'i', 's', 'o', 'm'}
	if _, err := part.Write(mp4); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", server.URL+"/uploads/videos", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var upload struct {
		PlayURL   string `json:"play_url"`
		SavedName string `json:"saved_name"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(data, &upload) != nil || upload.PlayURL == "" {
		t.Fatalf("upload failed: %d %s", resp.StatusCode, data)
	}
	saved, err := os.ReadFile(filepath.Join(storage.VideoDir, upload.SavedName))
	if err != nil || !bytes.Equal(saved, mp4) {
		t.Fatal("upload must be saved in the configured video directory")
	}
	if status, _ := request(t, "POST", "/videos", other.Token, map[string]string{"title": "forged publication", "play_url": upload.PlayURL, "cover_url": "/cover.svg"}); status != 403 {
		t.Fatal("B must not publish A's uploaded file")
	}
	status, data = request(t, "POST", "/videos", login.Token, map[string]string{"title": "Gin GORM integration", "play_url": upload.PlayURL, "cover_url": "/cover.svg"})
	var published struct {
		ID uint64 `json:"id"`
	}
	if status != 201 || json.Unmarshal(data, &published) != nil || published.ID == 0 {
		t.Fatalf("publish failed: %d %s", status, data)
	}
	var state string
	if err := db.QueryRow("SELECT status FROM uploads WHERE saved_name=? AND user_id=?", upload.SavedName, login.ID).Scan(&state); err != nil || state != "published" {
		t.Fatal("publish must commit upload state")
	}
	path := "/videos/" + strconv.FormatUint(published.ID, 10)
	if status, _ := request(t, "DELETE", path, other.Token, nil); status != 404 {
		t.Fatal("B must not delete A's work")
	}
	if status, _ := request(t, "DELETE", path, "", nil); status != 401 {
		t.Fatal("guest must not delete work")
	}
	if status, _ := request(t, "GET", path, "", nil); status != 200 {
		t.Fatal("video lookup failed")
	}
	if status, _ := request(t, "GET", "/me/videos", login.Token, nil); status != 200 {
		t.Fatal("personal list failed")
	}
	if status, data := request(t, "GET", upload.PlayURL, "", nil); status != 200 || !bytes.Equal(data, mp4) {
		t.Fatal("uploaded media bytes changed")
	}
	rangeRequest, err := http.NewRequest("GET", server.URL+upload.PlayURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rangeRequest.Header.Set("Range", "bytes=0-7")
	rangeResponse, err := client.Do(rangeRequest)
	if err != nil {
		t.Fatal(err)
	}
	rangeData, err := io.ReadAll(rangeResponse.Body)
	rangeResponse.Body.Close()
	if err != nil || rangeResponse.StatusCode != 206 || !bytes.Equal(rangeData, mp4[:8]) {
		t.Fatal("media Range access failed")
	}
	if status, data := request(t, "GET", "/me/videos", other.Token, nil); status != 200 || strings.Contains(string(data), upload.PlayURL) {
		t.Fatal("B's personal list contains A's work")
	}
	if status, _ := request(t, "PUT", path+"/like", login.Token, nil); status != 204 {
		t.Fatal("like failed")
	}
	if status, data := request(t, "GET", path+"/like", login.Token, nil); status != 200 || !strings.Contains(string(data), `"liked":true`) {
		t.Fatal("like status failed")
	}
	if status, _ := request(t, "DELETE", path+"/like", login.Token, nil); status != 204 {
		t.Fatal("unlike failed")
	}
	status, data = request(t, "POST", path+"/comments", login.Token, map[string]string{"content": "测试评论"})
	var comment struct {
		ID uint64 `json:"id"`
	}
	if status != 201 || json.Unmarshal(data, &comment) != nil || comment.ID == 0 {
		t.Fatalf("create comment failed: %d %s", status, data)
	}
	if status, _ := request(t, "DELETE", "/comments/"+strconv.FormatUint(comment.ID, 10), other.Token, nil); status != 404 {
		t.Fatal("B must not delete A's comment")
	}
	if status, _ := request(t, "PUT", path+"/like", other.Token, nil); status != 204 {
		t.Fatal("B like failed")
	}
	if status, _ := request(t, "PUT", path+"/like", other.Token, nil); status != 204 {
		t.Fatal("repeat B like must be idempotent")
	}
	if status, data := request(t, "GET", path+"/like", login.Token, nil); status != 200 || !strings.Contains(string(data), `"like_count":1`) || !strings.Contains(string(data), `"liked":false`) {
		t.Fatal("A must see B's like count without being personally liked")
	}
	if status, data := request(t, "GET", path+"/like", other.Token, nil); status != 200 || !strings.Contains(string(data), `"like_count":1`) || !strings.Contains(string(data), `"liked":true`) {
		t.Fatal("B must see its own like")
	}
	status, data = request(t, "POST", path+"/comments", other.Token, map[string]any{"content": "B 的评论", "user_id": login.ID})
	var otherComment struct {
		ID     uint64 `json:"id"`
		UserID uint64 `json:"user_id"`
	}
	if status != 201 || json.Unmarshal(data, &otherComment) != nil || otherComment.UserID != other.ID {
		t.Fatal("comment identity must come from B's JWT")
	}
	if status, _ := request(t, "DELETE", "/comments/"+strconv.FormatUint(otherComment.ID, 10), login.Token, nil); status != 404 {
		t.Fatal("video author A must not delete B's comment under current policy")
	}
	if status, data := request(t, "GET", path+"/comments", "", nil); status != 200 || !strings.Contains(string(data), "测试评论") {
		t.Fatal("comment query failed")
	}
	if status, _ := request(t, "DELETE", "/comments/"+strconv.FormatUint(comment.ID, 10), login.Token, nil); status != 204 {
		t.Fatal("comment deletion failed")
	}
	if status, _ := request(t, "DELETE", path, login.Token, nil); status != 204 {
		t.Fatal("video deletion failed")
	}
	if status, _ := request(t, "GET", path, "", nil); status != 404 {
		t.Fatal("deleted video should be missing")
	}
	for _, table := range []string{"video_likes", "video_comments"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE video_id=?", published.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal("video deletion must cascade fixture interactions")
		}
	}
	t.Log("two-account HTTP flow verified: registration/login → A upload/publish → public detail/Range → private lists → B like/comment → denied cross-user writes → A delete/cascade")
}
