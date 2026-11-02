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
			router.GET("/feed", NewHandler(NewService(repo, testCursorCodec(t))).ListLatest)
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
	router.GET("/feed", NewHandler(NewService(repo, codec)).ListLatest)
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
		router.GET("/feed", NewHandler(NewService(repo, testCursorCodec(t))).ListLatest)
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
