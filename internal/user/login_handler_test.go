package user

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gostream/internal/auth"
)

func TestLoginHandler(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	hash, err := HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body            string
		repositoryError       error
		noService, noSessions bool
		sessionsDown          bool
		wantStatus, wantCalls int
		wantMessage           string
	}{
		{name: "session_store_down", body: `{"username":"learner","password":"password123"}`, sessionsDown: true, wantStatus: 503, wantCalls: 1, wantMessage: "登录服务暂时不可用，请稍后再试"},
		{name: "missing_session_store", body: `{"username":"learner","password":"password123"}`, noSessions: true, wantStatus: 500, wantMessage: "用户服务未初始化"},
		{name: "success", body: `{"username":"learner","password":"password123"}`, wantStatus: 200, wantCalls: 1},
		{name: "wrong_password", body: `{"username":"learner","password":"wrong"}`, wantStatus: 401, wantCalls: 1, wantMessage: ErrInvalidCredentials.Error()},
		{name: "missing_user", body: `{"username":"learner","password":"password123"}`, repositoryError: ErrUserNotFound, wantStatus: 401, wantCalls: 1, wantMessage: ErrInvalidCredentials.Error()},
		{name: "database_failure", body: `{"username":"learner","password":"password123"}`, repositoryError: errors.New("private database details"), wantStatus: 500, wantCalls: 1, wantMessage: "查询用户失败"},
		{name: "empty_credentials", body: `{}`, wantStatus: 400, wantMessage: ErrEmptyCredentials.Error()},
		{name: "malformed_json", body: `{"username":`, wantStatus: 400, wantMessage: "请求体格式错误"},
		{name: "wrong_field_type", body: `{"username":123}`, wantStatus: 400, wantMessage: "请求体格式错误"},
		{name: "missing_service", body: `{"username":"learner","password":"password123"}`, noService: true, wantStatus: 500, wantMessage: "用户服务未初始化"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := loginRepositoryFunc(func(got context.Context, username string) (User, error) {
				calls++
				if got != ctx || username != "learner" {
					t.Error("request context and username must reach repository")
				}
				return User{ID: 77, Username: username, PasswordHash: hash}, tc.repositoryError
			})
			sessions := auth.NewMemorySessionStore()
			handler := &UserHandler{JWTSecret: secret, Sessions: sessions}
			if !tc.noService {
				handler.Service = NewUserService(repo)
			}
			if tc.noSessions {
				handler.Sessions = nil
			}
			if tc.sessionsDown {
				handler.Sessions = brokenSessions{}
			}
			router := gin.New()
			router.POST("/login", handler.Login)
			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(tc.body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != tc.wantStatus || calls != tc.wantCalls {
				t.Fatalf("expected status=%d calls=%d, got status=%d calls=%d", tc.wantStatus, tc.wantCalls, resp.Code, calls)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
				t.Fatalf("must return exactly one JSON response: %v", err)
			}
			if tc.wantStatus != 200 {
				var message string
				if len(result) != 1 || json.Unmarshal(result["error"], &message) != nil || message != tc.wantMessage {
					t.Fatal("failed login must return only safe error, without a token")
				}
				if len(resp.Result().Cookies()) != 0 {
					t.Fatal("a failed login must not set a refresh cookie")
				}
				return
			}
			var id uint64
			var username, token string
			if len(result) != 3 || json.Unmarshal(result["id"], &id) != nil || json.Unmarshal(result["username"], &username) != nil || json.Unmarshal(result["token"], &token) != nil || id != 77 || username != "learner" {
				t.Fatal("success must contain only ID, username and token")
			}
			userID, sessionID, err := auth.ParseToken(token, secret)
			if err != nil || userID != 77 {
				t.Fatal("token must authenticate the validated user")
			}
			if _, _, err := auth.ParseToken(token, []byte("another-test-secret-key-at-least-32-bytes")); err == nil {
				t.Fatal("different secret must reject login token")
			}
			// Every login opens a session; the access token names it and the cookie carries its refresh token.
			if exists, _ := sessions.Exists(ctx, sessionID); sessionID == "" || !exists {
				t.Fatalf("login must create the session named in the token, got %q", sessionID)
			}
			cookie := refreshCookie(t, resp)
			if !strings.HasPrefix(cookie.Value, sessionID+".") {
				t.Fatalf("the refresh cookie must belong to the same session: %q", cookie.Value)
			}
		})
	}
}

// refreshCookie returns the refresh cookie a response set and checks its protections.
func refreshCookie(t *testing.T, resp *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range resp.Result().Cookies() {
		if cookie.Name != RefreshCookieName {
			continue
		}
		if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/auth" || cookie.MaxAge != int(auth.SessionTTL/time.Second) {
			t.Fatalf("the refresh cookie must be HttpOnly, SameSite=Strict, limited to /auth and kept as long as the session: %+v", cookie)
		}
		return cookie
	}
	t.Fatal("no refresh cookie was set")
	return nil
}

// brokenSessions fails every call, like a session store whose Redis is down.
type brokenSessions struct{}

func (brokenSessions) Create(context.Context, uint64) (string, string, error) {
	return "", "", errors.New("redis down")
}

func (brokenSessions) Rotate(context.Context, string) (uint64, string, string, error) {
	return 0, "", "", errors.New("redis down")
}

func (brokenSessions) Exists(context.Context, string) (bool, error) {
	return false, errors.New("redis down")
}

func (brokenSessions) Delete(context.Context, string) error { return errors.New("redis down") }
