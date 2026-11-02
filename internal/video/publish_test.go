package video

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"gostream/internal/database"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"gostream/internal/auth"

	"github.com/gin-gonic/gin"
	ut "gostream/internal/testutil"
)

// A closed database simulates failure to begin a transaction without stopping MySQL.
func TestPublishDatabaseFailure(t *testing.T) {
	dir := t.TempDir()
	name := "test.mp4"
	content := []byte("owner's file must survive a publication failure")
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(&uploadTestStore{dir: dir})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(123, secret)
	if err != nil {
		t.Fatal(err)
	}
	h := gin.New()
	handler := &Handler{DB: database.WrapSQL(db), UploadDir: dir}
	h.POST("/videos", auth.JWTAuth(secret), handler.Publish)
	data, err := json.Marshal(map[string]string{
		"title": "test", "play_url": "/media/videos/" + name, "cover_url": "/cover.svg",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := ut.PerformRequest(h, http.MethodPost, "/videos",
		&ut.Body{Body: bytes.NewReader(data), Len: len(data)},
		ut.Header{Key: "Content-Type", Value: "application/json"},
		ut.Header{Key: "Authorization", Value: "Bearer " + token}).Result()
	if resp.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode())
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(resp.Body(), &failure); err != nil || failure.Error != "发布视频失败" {
		t.Fatal("expected a safe transaction-start failure response")
	}
	saved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(saved, content) {
		t.Fatal("publication failure must preserve the file")
	}
}
