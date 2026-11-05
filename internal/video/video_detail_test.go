package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gostream/internal/auth"
)

func TestVideoDetailHandler(t *testing.T) {
	stored := storedTestVideo()
	for _, tc := range []struct {
		name, path  string
		findErr     error
		marked      bool // the cache already remembers video 42 as missing
		wantStatus  int
		wantMessage string
		wantFind    bool
	}{
		{name: "found", path: "/videos/42", wantStatus: 200, wantFind: true},
		{name: "missing_is_404", path: "/videos/42", findErr: ErrVideoNotFound, wantStatus: 404, wantMessage: "视频不存在", wantFind: true},
		{name: "cached_missing_is_404_without_query", path: "/videos/42", marked: true, wantStatus: 404, wantMessage: "视频不存在"},
		{name: "database_error_is_safe_500", path: "/videos/42", findErr: errors.New("private database details"), wantStatus: 500, wantMessage: "视频查询失败", wantFind: true},
		{name: "invalid_id_is_400", path: "/videos/abc", wantStatus: 400, wantMessage: "视频ID必须是正整数"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := false
			repo := &fakeVideoRepository{find: func(_ context.Context, id uint64) (Video, error) {
				found = true
				if id != 42 {
					t.Errorf("unexpected video ID %d", id)
				}
				return stored, tc.findErr
			}}
			cache := newFakeVideoCache()
			if tc.marked {
				cache.items[42] = nil
			}
			router := gin.New()
			router.GET("/videos/:id", (&Handler{VideoService: NewVideoService(repo, cache)}).GetByID)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if response.Code != tc.wantStatus || found != tc.wantFind {
				t.Fatalf("status %d (queried %v), want %d (queried %v): %s", response.Code, found, tc.wantStatus, tc.wantFind, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private database details") {
				t.Fatal("internal error leaked to the client")
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.wantStatus != http.StatusOK {
				if body["error"] != tc.wantMessage {
					t.Fatalf("expected error %q, got %v", tc.wantMessage, body)
				}
				return
			}
			// The public response keeps exactly the six fields it had before caching.
			if len(body) != 6 || body["id"] != float64(42) || body["author_id"] != float64(7) || body["title"] != stored.Title || body["created_at"] != "2026-11-03T10:00:00Z" {
				t.Fatalf("unexpected detail response: %v", body)
			}
		})
	}
}

func TestVideoDeleteHandler(t *testing.T) {
	secret := []byte("video-test-only-jwt-secret-at-least-32-bytes")
	token, err := auth.GenerateToken(7, secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, token string
		deleteErr         error
		wantStatus        int
		wantMessage       string
		wantDelete        bool
	}{
		{name: "author_deletes", path: "/videos/42", token: token, wantStatus: 204, wantDelete: true},
		{name: "not_owned_is_404", path: "/videos/42", token: token, deleteErr: ErrVideoNotFound, wantStatus: 404, wantMessage: "视频不存在或不属于当前用户", wantDelete: true},
		{name: "database_error_is_safe_500", path: "/videos/42", token: token, deleteErr: errors.New("private database details"), wantStatus: 500, wantMessage: "删除视频失败", wantDelete: true},
		{name: "invalid_id_is_400", path: "/videos/0", token: token, wantStatus: 400, wantMessage: "视频ID必须是正整数"},
		{name: "missing_token_is_401", path: "/videos/42", wantStatus: 401, wantMessage: "请提供 Bearer token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deleted := false
			cache := newFakeVideoCache()
			cached := storedTestVideo()
			cache.items[42] = &cached
			repo := &fakeVideoRepository{deleteOwned: func(_ context.Context, id, authorID uint64) error {
				deleted = true
				if id != 42 || authorID != 7 {
					t.Errorf("expected video 42 by JWT user 7, got %d by %d", id, authorID)
				}
				return tc.deleteErr
			}}
			router := gin.New()
			router.DELETE("/videos/:id", auth.JWTAuth(secret), (&Handler{VideoService: NewVideoService(repo, cache)}).Delete)
			request := httptest.NewRequest(http.MethodDelete, tc.path, nil)
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.wantStatus || deleted != tc.wantDelete {
				t.Fatalf("status %d (deleted %v), want %d (deleted %v): %s", response.Code, deleted, tc.wantStatus, tc.wantDelete, response.Body.String())
			}
			_, _, _, deletes := cache.counts()
			if tc.wantStatus == http.StatusNoContent {
				if response.Body.Len() != 0 || deletes != 1 {
					t.Fatalf("204 must have no body and must invalidate the cache (deletes %d)", deletes)
				}
				return
			}
			if deletes != 0 {
				t.Fatal("a failed delete must not invalidate the cache")
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["error"] != tc.wantMessage {
				t.Fatalf("expected error %q, got %s", tc.wantMessage, response.Body.String())
			}
		})
	}
}
