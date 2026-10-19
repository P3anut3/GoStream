package video

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gostream/internal/auth"
)

func TestCreateCommentValidation(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(123, secret)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(&uploadTestStore{})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	h := server.New()
	handler := &Handler{DB: db}
	h.POST("/videos/:id/comments", auth.JWTAuth(secret), handler.CreateComment)
	for _, tc := range []struct {
		name, id, token, body string
		want                  int
	}{
		{"no_token", "1", "", `{"content":"hello"}`, 401},
		{"invalid_token", "1", "invalid", `{"content":"hello"}`, 401},
		{"invalid_id", "abc", token, `{"content":"hello"}`, 400},
		{"zero_id", "0", token, `{"content":"hello"}`, 400},
		{"negative_id", "-1", token, `{"content":"hello"}`, 400},
		{"malformed_json", "1", token, `{"content":`, 400},
		{"wrong_content_type", "1", token, `{"content":123}`, 400},
		{"missing_content", "1", token, `{}`, 400},
		{"whitespace", "1", token, `{"content":" \t\n　"}`, 400},
		{"too_long", "1", token, `{"content":"` + strings.Repeat("评", 501) + `"}`, 400},
		{"database_failure", "1", token, `{"content":"hello"}`, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := []ut.Header{{Key: "Content-Type", Value: "application/json"}}
			if tc.token != "" {
				headers = append(headers, ut.Header{Key: "Authorization", Value: "Bearer " + tc.token})
			}
			resp := ut.PerformRequest(h.Engine, http.MethodPost, "/videos/"+tc.id+"/comments",
				&ut.Body{Body: bytes.NewBufferString(tc.body), Len: len(tc.body)}, headers...).Result()
			if resp.StatusCode() != tc.want {
				t.Errorf("expected %d, got %d", tc.want, resp.StatusCode())
			}
			var failure struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(resp.Body(), &failure); err != nil || failure.Error == "" {
				t.Fatal("expected JSON error")
			}
			if tc.name == "database_failure" && failure.Error != "保存评论失败" {
				t.Error("valid identity and input must reach the database operation")
			}
		})
	}
}

func TestListCommentsValidation(t *testing.T) {
	db := sql.OpenDB(&uploadTestStore{})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	h := server.New()
	handler := &Handler{DB: db}
	h.GET("/videos/:id/comments", handler.ListComments)
	for _, tc := range []struct {
		name, path string
		want       int
	}{
		{"invalid_id", "/videos/abc/comments", 400},
		{"zero_id", "/videos/0/comments", 400},
		{"negative_id", "/videos/-1/comments", 400},
		{"invalid_page", "/videos/1/comments?page=abc", 400},
		{"zero_page", "/videos/1/comments?page=0", 400},
		{"page_too_large", "/videos/1/comments?page=10001", 400},
		{"invalid_page_size", "/videos/1/comments?page_size=abc", 400},
		{"zero_page_size", "/videos/1/comments?page_size=0", 400},
		{"page_size_too_large", "/videos/1/comments?page_size=101", 400},
		{"database_failure", "/videos/1/comments", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := ut.PerformRequest(h.Engine, http.MethodGet, tc.path, nil).Result()
			if resp.StatusCode() != tc.want {
				t.Errorf("expected %d, got %d", tc.want, resp.StatusCode())
			}
		})
	}
}

func TestDeleteCommentValidation(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(123, secret)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(&uploadTestStore{})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	h := server.New()
	handler := &Handler{DB: db}
	h.DELETE("/comments/:id", auth.JWTAuth(secret), handler.DeleteComment)
	for _, tc := range []struct {
		name, id, token string
		want            int
	}{
		{"no_token", "1", "", 401},
		{"invalid_token", "1", "invalid", 401},
		{"invalid_id", "abc", token, 400},
		{"zero_id", "0", token, 400},
		{"negative_id", "-1", token, 400},
		{"database_failure", "1", token, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := []ut.Header{}
			if tc.token != "" {
				headers = append(headers, ut.Header{Key: "Authorization", Value: "Bearer " + tc.token})
			}
			resp := ut.PerformRequest(h.Engine, http.MethodDelete, "/comments/"+tc.id, nil, headers...).Result()
			if resp.StatusCode() != tc.want {
				t.Errorf("expected %d, got %d", tc.want, resp.StatusCode())
			}
		})
	}
}
