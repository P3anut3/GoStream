package feed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gostream/internal/auth"
)

func TestHandlerInvalidQuery(t *testing.T) {
	for _, tc := range []struct{ name, query, code string }{
		{"non_integer_limit", "limit=abc", "INVALID_ARGUMENT"},
		{"overflow_limit", "limit=99999999999999999999999", "INVALID_ARGUMENT"},
		{"negative_limit", "limit=-1", "INVALID_ARGUMENT"},
		{"invalid_cursor", "cursor=invalid", "INVALID_CURSOR"},
		{"oversized_cursor", "cursor=" + strings.Repeat("x", 1025), "INVALID_CURSOR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := feedRepositoryFunc(func(context.Context, *Position, int) ([]FeedItem, error) {
				t.Fatal("bad query must not reach repository")
				return nil, nil
			})
			router := gin.New()
			router.GET("/feed", NewHandler(NewService(repo, testCursorCodec(t), nil)).ListLatest)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/feed?"+tc.query, nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.Code)
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 2 || body["code"] != tc.code || body["error"] == "" {
				t.Fatalf("unexpected error response: %s", response.Body.String())
			}
		})
	}
}

func TestHandlerFeedPagination(t *testing.T) {
	codec := testCursorCodec(t)
	stamp := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	ctx := context.WithValue(context.Background(), feedContextKey{}, "http-request")
	calls := 0
	repo := feedRepositoryFunc(func(got context.Context, position *Position, limit int) ([]FeedItem, error) {
		calls++
		if got != ctx || limit != 3 {
			t.Fatal("request context and lookahead limit must reach repository")
		}
		if calls == 1 {
			if position != nil {
				t.Fatal("first page has unexpected cursor")
			}
			return []FeedItem{{ID: 30, CreatedAt: stamp}, {ID: 29, CreatedAt: stamp}, {ID: 28, CreatedAt: stamp}}, nil
		}
		if position == nil || position.ID != 29 || !position.CreatedAt.Equal(stamp) {
			t.Fatal("HTTP cursor must decode to returned item 29")
		}
		return []FeedItem{{ID: 28, CreatedAt: stamp}}, nil
	})
	router := gin.New()
	router.GET("/feed", NewHandler(NewService(repo, codec, nil)).ListLatest)
	get := func(path string) ListResponse {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
		if response.Code != 200 || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
		}
		var body ListResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 3 {
			t.Fatal("unexpected response fields")
		}
		return body
	}
	first := get("/feed?limit=2") // Public endpoint: no JWT required.
	if len(first.Videos) != 2 || first.Videos[0].ID != 30 || first.Videos[1].ID != 29 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("incorrect first page: %+v", first)
	}
	second := get("/feed?limit=2&cursor=" + url.QueryEscape(first.NextCursor))
	if len(second.Videos) != 1 || second.Videos[0].ID != 28 || second.HasMore || second.NextCursor != "" {
		t.Fatalf("incorrect last page: %+v", second)
	}
}

func TestHandlerEmptyFeedAndInternalFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		repo := feedRepositoryFunc(func(_ context.Context, _ *Position, limit int) ([]FeedItem, error) {
			if limit != 11 {
				t.Error("omitted limit must default to ten plus one lookahead row")
			}
			if fail {
				return nil, errors.New("private-database-details")
			}
			return nil, nil
		})
		router := gin.New()
		router.GET("/feed", NewHandler(NewService(repo, testCursorCodec(t), nil)).ListLatest)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/feed", nil))
		if !fail {
			if response.Code != 200 || response.Body.String() != `{"videos":[],"next_cursor":"","has_more":false}` {
				t.Fatalf("incorrect empty feed: %d %s", response.Code, response.Body.String())
			}
			continue
		}
		var body map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != 500 || len(body) != 2 || body["code"] != "INTERNAL_ERROR" || body["error"] != "视频流查询失败" {
			t.Fatalf("incorrect failure response: %d %s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "private-database-details") {
			t.Fatal("internal error leaked to client")
		}
	}
}

func TestHandlerFollowing(t *testing.T) {
	secret := []byte("feed-test-only-jwt-secret-at-least-32-bytes")
	token, err := auth.GenerateToken(7, "", secret)
	if err != nil {
		t.Fatal(err)
	}
	codec := testCursorCodec(t)
	latest, err := codec.Encode(KindLatest, Position{CreatedAt: time.Now(), ID: 24})
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, path, token string
		withJWT           bool
		failure           error
		wantStatus        int
		wantCode          string
		wantQuery         bool
	}{
		{name: "viewer_feed", path: "/feed/following?limit=2", token: token, withJWT: true, wantStatus: 200, wantQuery: true},
		{name: "missing_token_is_401", path: "/feed/following", withJWT: true, wantStatus: 401},
		// A route mounted without JWT must fail closed instead of serving an anonymous feed.
		{name: "missing_middleware_fails_closed", path: "/feed/following", wantStatus: 500, wantCode: "INTERNAL_ERROR"},
		{name: "latest_cursor_is_rejected", path: "/feed/following?cursor=" + url.QueryEscape(latest), token: token, withJWT: true, wantStatus: 400, wantCode: "INVALID_CURSOR"},
		{name: "negative_limit_is_rejected", path: "/feed/following?limit=-1", token: token, withJWT: true, wantStatus: 400, wantCode: "INVALID_ARGUMENT"},
		{name: "database_error_is_safe", path: "/feed/following", token: token, withJWT: true, failure: errors.New("private-database-details"), wantStatus: 500, wantCode: "INTERNAL_ERROR", wantQuery: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queried := false
			repo := followingRepository{list: func(_ context.Context, viewerID uint64, position *Position, limit int) ([]FeedItem, error) {
				queried = true
				if viewerID != 7 || position != nil {
					t.Errorf("unexpected viewer %d or position %v", viewerID, position)
				}
				if tc.failure != nil {
					return nil, tc.failure
				}
				return []FeedItem{{ID: 30, AuthorID: 2, CreatedAt: stamp}}, nil
			}}
			handler := NewHandler(NewService(repo, codec, nil))
			router := gin.New()
			if tc.withJWT {
				router.GET("/feed/following", auth.JWTAuth(secret, nil), handler.ListFollowing)
			} else {
				router.GET("/feed/following", handler.ListFollowing)
			}
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.wantStatus || queried != tc.wantQuery {
				t.Fatalf("status %d (queried %v), want %d (queried %v): %s", response.Code, queried, tc.wantStatus, tc.wantQuery, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private-database-details") {
				t.Fatal("internal error leaked to client")
			}
			if tc.wantCode != "" {
				var body map[string]string
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["code"] != tc.wantCode {
					t.Fatalf("expected code %s, got %s", tc.wantCode, response.Body.String())
				}
			}
			if tc.wantStatus == http.StatusOK {
				if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Vary") != "Authorization" {
					t.Error("a personal feed must not be stored by shared caches")
				}
				var body ListResponse
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Videos) != 1 || body.Videos[0].ID != 30 || body.HasMore {
					t.Fatalf("unexpected body: %s", response.Body.String())
				}
			}
		})
	}
}
