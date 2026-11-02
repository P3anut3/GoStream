package video

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"gostream/internal/database"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gostream/internal/auth"
	ut "gostream/internal/testutil"
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
	h := gin.New()
	handler := &Handler{DB: database.WrapSQL(db), CommentService: NewCommentService(&SQLCommentRepository{DB: database.WrapSQL(db)})}
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
			resp := ut.PerformRequest(h, http.MethodPost, "/videos/"+tc.id+"/comments",
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
	h := gin.New()
	handler := &Handler{DB: database.WrapSQL(db), CommentService: NewCommentService(&SQLCommentRepository{DB: database.WrapSQL(db)})}
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
			resp := ut.PerformRequest(h, http.MethodGet, tc.path, nil).Result()
			if resp.StatusCode() != tc.want {
				t.Errorf("expected %d, got %d", tc.want, resp.StatusCode())
			}
			if tc.name == "database_failure" {
				var failure struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(resp.Body(), &failure); err != nil || failure.Error != "检查视频失败" {
					t.Fatal("valid pagination must reach the repository video lookup")
				}
			}
		})
	}
}

type listCommentRepositoryFunc func(context.Context, uint64, int, int) ([]Comment, error)

func (f listCommentRepositoryFunc) CreateComment(context.Context, uint64, uint64, string) (uint64, error) {
	return 0, errors.New("list test must not create comments")
}

func (f listCommentRepositoryFunc) DeleteComment(context.Context, uint64, uint64) (bool, error) {
	return false, errors.New("list test must not delete comments")
}

func (f listCommentRepositoryFunc) ListComments(ctx context.Context, videoID uint64, limit, offset int) ([]Comment, error) {
	return f(ctx, videoID, limit, offset)
}

func TestListCommentsServiceResponse(t *testing.T) {
	for _, tc := range []struct {
		name        string
		comments    []Comment
		err         error
		wantStatus  int
		wantMessage string
	}{
		{name: "public_paginated_results", comments: []Comment{{ID: 9, VideoID: 34, UserID: 12, Content: "hello"}}, wantStatus: 200},
		{name: "empty_is_array", comments: make([]Comment, 0), wantStatus: 200},
		{name: "missing_video", err: ErrCommentVideoNotFound, wantStatus: 404, wantMessage: ErrCommentVideoNotFound.Error()},
		{name: "storage_error_safe_message", err: &commentStorageError{message: "读取评论失败", cause: errors.New("private database details")}, wantStatus: 500, wantMessage: "读取评论失败"},
		{name: "unexpected_error_safe_message", err: errors.New("private database details"), wantStatus: 500, wantMessage: "评论列表查询失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repo := listCommentRepositoryFunc(func(ctx context.Context, videoID uint64, limit, offset int) ([]Comment, error) {
				calls++
				if ctx == nil || videoID != 34 || limit != 5 || offset != 10 {
					t.Error("expected video 34, limit 5 and offset 10")
				}
				return tc.comments, tc.err
			})
			h := gin.New()
			handler := &Handler{CommentService: NewCommentService(repo)}
			h.GET("/videos/:id/comments", handler.ListComments)
			resp := ut.PerformRequest(h, http.MethodGet, "/videos/34/comments?page=3&page_size=5", nil).Result()
			if resp.StatusCode() != tc.wantStatus {
				t.Fatalf("expected %d, got %d", tc.wantStatus, resp.StatusCode())
			}
			if calls != 1 {
				t.Errorf("expected one repository call, got %d", calls)
			}
			if tc.wantStatus != 200 {
				var failure struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(resp.Body(), &failure); err != nil {
					t.Fatal(err)
				}
				if failure.Error != tc.wantMessage {
					t.Errorf("expected %q, got %q", tc.wantMessage, failure.Error)
				}
				return
			}
			var result struct {
				VideoID  uint64    `json:"video_id"`
				Page     int       `json:"page"`
				PageSize int       `json:"page_size"`
				Comments []Comment `json:"comments"`
			}
			if err := json.Unmarshal(resp.Body(), &result); err != nil {
				t.Fatal(err)
			}
			if result.VideoID != 34 || result.Page != 3 || result.PageSize != 5 || result.Comments == nil {
				t.Fatal("incorrect pagination or null comments")
			}
			if len(result.Comments) != len(tc.comments) {
				t.Fatal("incorrect comment count")
			}
			for i := range result.Comments {
				if result.Comments[i] != tc.comments[i] {
					t.Error("comment fields must be preserved")
				}
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
	h := gin.New()
	handler := &Handler{DB: database.WrapSQL(db), CommentService: NewCommentService(&SQLCommentRepository{DB: database.WrapSQL(db)})}
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
			resp := ut.PerformRequest(h, http.MethodDelete, "/comments/"+tc.id, nil, headers...).Result()
			if resp.StatusCode() != tc.want {
				t.Errorf("expected %d, got %d", tc.want, resp.StatusCode())
			}
			if tc.name == "database_failure" {
				var failure struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(resp.Body(), &failure); err != nil || failure.Error != "删除评论失败" {
					t.Fatal("valid request must reach the repository and return the database failure response")
				}
			}
		})
	}
}

func TestDeleteCommentServiceResponse(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(123, secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		deleted     bool
		wantStatus  int
		wantMessage string
	}{
		{name: "success_has_no_body", deleted: true, wantStatus: http.StatusNoContent},
		{name: "missing_or_not_owned_is_404", wantStatus: http.StatusNotFound, wantMessage: ErrCommentNotFound.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repository := deleteCommentRepositoryFunc(func(_ context.Context, userID, commentID uint64) (bool, error) {
				calls++
				if userID != 123 || commentID != 456 {
					t.Error("handler must pass JWT identity and path comment ID")
				}
				return tc.deleted, nil
			})
			handler := &Handler{CommentService: NewCommentService(repository)}
			h := gin.New()
			h.DELETE("/comments/:id", auth.JWTAuth(secret), handler.DeleteComment)
			resp := ut.PerformRequest(h, http.MethodDelete, "/comments/456", nil,
				ut.Header{Key: "Authorization", Value: "Bearer " + token}).Result()
			if resp.StatusCode() != tc.wantStatus {
				t.Fatalf("expected %d, got %d", tc.wantStatus, resp.StatusCode())
			}
			if calls != 1 {
				t.Errorf("expected one repository call, got %d", calls)
			}
			if tc.wantStatus == http.StatusNoContent {
				if len(resp.Body()) != 0 {
					t.Error("204 response must have no body")
				}
				return
			}
			var failure struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(resp.Body(), &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Error != tc.wantMessage {
				t.Errorf("expected message %q, got %q", tc.wantMessage, failure.Error)
			}
		})
	}
}
