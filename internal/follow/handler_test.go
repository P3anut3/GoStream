package follow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gostream/internal/auth"
)

var handlerTestSecret = []byte("test-only-secret-key-at-least-32-bytes")

func TestHandlerFollow(t *testing.T) {
	for _, tc := range []struct {
		name            string
		path, body      string
		repositoryError error
		wantCalls       int
		wantStatus      int
		wantMessage     string
	}{
		{name: "first_follow", path: "/users/34/follow", wantCalls: 1, wantStatus: 204},
		{name: "duplicate_follow_is_204", path: "/users/34/follow", repositoryError: ErrAlreadyFollowing, wantCalls: 1, wantStatus: 204},
		// The operator always comes from the token; a forged body is ignored.
		{name: "forged_body_is_ignored", path: "/users/34/follow", body: `{"follower_id": 99}`, wantCalls: 1, wantStatus: 204},
		{name: "self_follow_is_400", path: "/users/12/follow", wantStatus: 400, wantMessage: "不能关注自己"},
		{name: "missing_target_is_404", path: "/users/34/follow", repositoryError: ErrUserNotFound, wantCalls: 1, wantStatus: 404, wantMessage: "用户不存在"},
		{name: "database_error_is_safe_500", path: "/users/34/follow", repositoryError: errors.New("private database details"), wantCalls: 1, wantStatus: 500, wantMessage: "关注失败"},
		{name: "non_numeric_id_is_400", path: "/users/abc/follow", wantStatus: 400, wantMessage: "用户 ID 必须是正整数"},
		{name: "zero_id_is_400", path: "/users/0/follow", wantStatus: 400, wantMessage: "用户 ID 必须是正整数"},
		{name: "negative_id_is_400", path: "/users/-1/follow", wantStatus: 400, wantMessage: "用户 ID 必须是正整数"},
		{name: "overflow_id_is_400", path: "/users/18446744073709551616/follow", wantStatus: 400, wantMessage: "用户 ID 必须是正整数"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			router := newFollowRouter(fakeRepository{follow: func(_ context.Context, followerID, followeeID uint64) error {
				calls++
				if followerID != 12 || followeeID != 34 {
					t.Errorf("expected JWT user 12 and path user 34, got %d and %d", followerID, followeeID)
				}
				return tc.repositoryError
			}}, true)
			recorder := serveFollow(t, router, http.MethodPut, tc.path, 12, tc.body)
			expectResponse(t, recorder, tc.wantStatus, tc.wantMessage)
			if calls != tc.wantCalls {
				t.Errorf("expected %d repository calls, got %d", tc.wantCalls, calls)
			}
		})
	}
}

func TestHandlerUnfollow(t *testing.T) {
	for _, tc := range []struct {
		name            string
		path            string
		wantFollowee    uint64
		repositoryError error
		wantCalls       int
		wantStatus      int
		wantMessage     string
	}{
		{name: "unfollow", path: "/users/34/follow", wantFollowee: 34, wantCalls: 1, wantStatus: 204},
		{name: "self_unfollow_is_204", path: "/users/12/follow", wantFollowee: 12, wantCalls: 1, wantStatus: 204},
		{name: "database_error_is_safe_500", path: "/users/34/follow", wantFollowee: 34, repositoryError: errors.New("private database details"), wantCalls: 1, wantStatus: 500, wantMessage: "取消关注失败"},
		{name: "invalid_id_is_400", path: "/users/abc/follow", wantStatus: 400, wantMessage: "用户 ID 必须是正整数"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			router := newFollowRouter(fakeRepository{unfollow: func(_ context.Context, followerID, followeeID uint64) error {
				calls++
				if followerID != 12 || followeeID != tc.wantFollowee {
					t.Errorf("expected JWT user 12 and path user %d, got %d and %d", tc.wantFollowee, followerID, followeeID)
				}
				return tc.repositoryError
			}}, true)
			recorder := serveFollow(t, router, http.MethodDelete, tc.path, 12, "")
			expectResponse(t, recorder, tc.wantStatus, tc.wantMessage)
			if calls != tc.wantCalls {
				t.Errorf("expected %d repository calls, got %d", tc.wantCalls, calls)
			}
		})
	}
}

func TestHandlerGetStatus(t *testing.T) {
	for _, tc := range []struct {
		name            string
		path            string
		following       bool
		repositoryError error
		wantCalls       int
		wantStatus      int
		wantMessage     string
	}{
		{name: "following", path: "/users/34/follow", following: true, wantCalls: 1, wantStatus: 200},
		{name: "not_following", path: "/users/34/follow", wantCalls: 1, wantStatus: 200},
		{name: "missing_target_is_404", path: "/users/34/follow", repositoryError: ErrUserNotFound, wantCalls: 1, wantStatus: 404, wantMessage: "用户不存在"},
		{name: "database_error_is_safe_500", path: "/users/34/follow", repositoryError: errors.New("private database details"), wantCalls: 1, wantStatus: 500, wantMessage: "查询关注状态失败"},
		{name: "invalid_id_is_400", path: "/users/0/follow", wantStatus: 400, wantMessage: "用户 ID 必须是正整数"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			router := newFollowRouter(fakeRepository{isFollowing: func(_ context.Context, followerID, followeeID uint64) (bool, error) {
				calls++
				if followerID != 12 || followeeID != 34 {
					t.Errorf("expected JWT user 12 and path user 34, got %d and %d", followerID, followeeID)
				}
				return tc.following, tc.repositoryError
			}}, true)
			recorder := serveFollow(t, router, http.MethodGet, tc.path, 12, "")
			if calls != tc.wantCalls {
				t.Errorf("expected %d repository calls, got %d", tc.wantCalls, calls)
			}
			if tc.wantStatus != http.StatusOK {
				expectResponse(t, recorder, tc.wantStatus, tc.wantMessage)
				return
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d (%s)", recorder.Code, recorder.Body.String())
			}
			// The answer depends on the token, so shared caches must not reuse it.
			if recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Vary") != "Authorization" {
				t.Error("personal follow status must not be stored by shared caches")
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var following bool
			if len(result) != 1 || json.Unmarshal(result["following"], &following) != nil || following != tc.following {
				t.Fatalf("expected exactly {\"following\": %v}, got %s", tc.following, recorder.Body.String())
			}
		})
	}
}

func TestHandlerRequiresIdentity(t *testing.T) {
	unexpected := fakeRepository{} // every repository method fails if it is reached
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodGet} {
		t.Run(method+"_without_token_is_401", func(t *testing.T) {
			recorder := serveFollow(t, newFollowRouter(unexpected, true), method, "/users/34/follow", 0, "")
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d (%s)", recorder.Code, recorder.Body.String())
			}
		})
		// A route mounted without JWT must fail closed instead of acting anonymously.
		t.Run(method+"_without_middleware_is_500", func(t *testing.T) {
			recorder := serveFollow(t, newFollowRouter(unexpected, false), method, "/users/34/follow", 0, "")
			expectResponse(t, recorder, http.StatusInternalServerError, "请求中缺少有效的用户身份")
		})
	}
}

// newFollowRouter mounts the routes the way main.go does, optionally without the JWT middleware.
func newFollowRouter(repo Repository, withJWT bool) *gin.Engine {
	handler := NewHandler(NewService(repo))
	router := gin.New()
	chain := func(final gin.HandlerFunc) []gin.HandlerFunc {
		if withJWT {
			return []gin.HandlerFunc{auth.JWTAuth(handlerTestSecret), final}
		}
		return []gin.HandlerFunc{final}
	}
	router.PUT("/users/:id/follow", chain(handler.Follow)...)
	router.DELETE("/users/:id/follow", chain(handler.Unfollow)...)
	router.GET("/users/:id/follow", chain(handler.GetStatus)...)
	return router
}

// serveFollow sends one request as userID; 0 means no Authorization header.
func serveFollow(t *testing.T, router http.Handler, method, path string, userID uint64, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if userID != 0 {
		token, err := auth.GenerateToken(userID, handlerTestSecret)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// expectResponse checks a 204 without body, or an error status with a safe JSON message.
func expectResponse(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantMessage string) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("expected %d, got %d (%s)", wantStatus, recorder.Code, recorder.Body.String())
	}
	if wantStatus == http.StatusNoContent {
		if recorder.Body.Len() != 0 {
			t.Error("204 must have no body")
		}
		return
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil || failure.Error != wantMessage {
		t.Errorf("expected error %q, got %s", wantMessage, recorder.Body.String())
	}
}
