package user

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

// sessionUsers looks users up by ID for Refresh; nil means the database is down.
type sessionUsers map[uint64]string

func (u sessionUsers) FindByID(_ context.Context, id uint64) (User, error) {
	if u == nil {
		return User{}, errors.New("private database details")
	}
	name, ok := u[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return User{ID: id, Username: name}, nil
}

func (sessionUsers) FindByUsername(context.Context, string) (User, error) {
	return User{}, errors.New("unexpected FindByUsername")
}

func (sessionUsers) CreateUser(context.Context, *User) error {
	return errors.New("unexpected CreateUser")
}

var sessionTestSecret = []byte("test-only-session-handler-secret-32-bytes")

func newSessionRouter(sessions auth.SessionStore, users sessionUsers) *gin.Engine {
	handler := &UserHandler{JWTSecret: sessionTestSecret, Service: NewUserService(users), Sessions: sessions}
	router := gin.New()
	router.POST("/auth/refresh", handler.Refresh)
	router.POST("/auth/logout", handler.Logout)
	return router
}

func postWithCookie(router http.Handler, path, cookie string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, nil)
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: RefreshCookieName, Value: cookie})
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

// expectCleared checks that the response tells the browser to drop the refresh cookie.
func expectCleared(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == RefreshCookieName && cookie.MaxAge < 0 && cookie.Path == "/auth" {
			return
		}
	}
	t.Fatalf("the refresh cookie must be cleared, got %v", response.Result().Cookies())
}

func TestRefreshRotatesTheTokenAndCatchesReuse(t *testing.T) {
	sessions := auth.NewMemorySessionStore()
	router := newSessionRouter(sessions, sessionUsers{42: "learner"})
	sessionID, first, err := sessions.Create(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	response := postWithCookie(router, "/auth/refresh", first)
	var body struct {
		ID       uint64 `json:"id"`
		Username string `json:"username"`
		Token    string `json:"token"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.ID != 42 || body.Username != "learner" {
		t.Fatalf("refresh: %d %s", response.Code, response.Body.String())
	}
	if userID, tokenSession, err := auth.ParseToken(body.Token, sessionTestSecret); err != nil || userID != 42 || tokenSession != sessionID {
		t.Fatalf("the new access token must belong to the same session: %d, %q, %v", userID, tokenSession, err)
	}
	second := refreshCookie(t, response).Value
	if second == first || !strings.HasPrefix(second, sessionID+".") {
		t.Fatalf("refreshing must hand out a new refresh token, got %q", second)
	}
	// Someone replays the first token: the whole session is revoked, including the newest token.
	response = postWithCookie(router, "/auth/refresh", first)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("a reused refresh token must be rejected, got %d", response.Code)
	}
	expectCleared(t, response)
	if exists, _ := sessions.Exists(context.Background(), sessionID); exists {
		t.Fatal("reuse must delete the session")
	}
	if response := postWithCookie(router, "/auth/refresh", second); response.Code != http.StatusUnauthorized {
		t.Fatalf("after reuse the newest token is dead too, got %d", response.Code)
	}
}

func TestRefreshRejectsMissingOrInvalidCookies(t *testing.T) {
	router := newSessionRouter(auth.NewMemorySessionStore(), sessionUsers{42: "learner"})
	if response := postWithCookie(router, "/auth/refresh", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: want 401, got %d", response.Code)
	}
	for _, cookie := range []string{"garbage", strings.Repeat("a", 32) + ".unknown-session"} {
		response := postWithCookie(router, "/auth/refresh", cookie)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("cookie %q: want 401, got %d", cookie, response.Code)
		}
		expectCleared(t, response)
	}
}

func TestRefreshWhileTheStoreIsDownKeepsTheCookie(t *testing.T) {
	router := newSessionRouter(brokenSessions{}, sessionUsers{42: "learner"})
	response := postWithCookie(router, "/auth/refresh", strings.Repeat("a", 32)+".secret")
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "redis") {
		t.Fatalf("an unavailable store must be a safe 503, got %d %s", response.Code, response.Body.String())
	}
	// The token may still be valid once Redis is back, so the browser must keep it.
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("an outage must not clear the refresh cookie")
	}
}

func TestRefreshForADeletedUserEndsTheSession(t *testing.T) {
	sessions := auth.NewMemorySessionStore()
	router := newSessionRouter(sessions, sessionUsers{})
	sessionID, refresh, _ := sessions.Create(context.Background(), 99)
	response := postWithCookie(router, "/auth/refresh", refresh)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "当前用户已经不存在") {
		t.Fatalf("want 401 for a deleted user, got %d %s", response.Code, response.Body.String())
	}
	expectCleared(t, response)
	if exists, _ := sessions.Exists(context.Background(), sessionID); exists {
		t.Fatal("the session of a deleted user must be removed")
	}
}

func TestRefreshHandsOverTheNewCookieEvenWhenTheUserLookupFails(t *testing.T) {
	sessions := auth.NewMemorySessionStore()
	router := newSessionRouter(sessions, nil) // the database is down
	_, first, _ := sessions.Create(context.Background(), 42)
	response := postWithCookie(router, "/auth/refresh", first)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "private database details") {
		t.Fatalf("want a safe 500, got %d %s", response.Code, response.Body.String())
	}
	// The old token was already replaced, so the browser must get the new one to retry with.
	second := refreshCookie(t, response).Value
	router = newSessionRouter(sessions, sessionUsers{42: "learner"})
	if response := postWithCookie(router, "/auth/refresh", second); response.Code != http.StatusOK {
		t.Fatalf("retrying with the new cookie must work, got %d", response.Code)
	}
}

func TestLogoutDeletesTheSession(t *testing.T) {
	sessions := auth.NewMemorySessionStore()
	router := newSessionRouter(sessions, sessionUsers{42: "learner"})
	sessionID, refresh, _ := sessions.Create(context.Background(), 42)
	response := postWithCookie(router, "/auth/logout", refresh)
	if response.Code != http.StatusNoContent {
		t.Fatalf("logout: want 204, got %d", response.Code)
	}
	expectCleared(t, response)
	if exists, _ := sessions.Exists(context.Background(), sessionID); exists {
		t.Fatal("logout must delete the session, so its access tokens stop working at once")
	}
	if response := postWithCookie(router, "/auth/refresh", refresh); response.Code != http.StatusUnauthorized {
		t.Fatalf("a logged out session cannot refresh, got %d", response.Code)
	}
	// Logging out without a cookie, or while the store is down, still logs this browser out.
	for _, router := range []*gin.Engine{router, newSessionRouter(brokenSessions{}, nil)} {
		for _, cookie := range []string{"", refresh} {
			response := postWithCookie(router, "/auth/logout", cookie)
			if response.Code != http.StatusNoContent {
				t.Fatalf("logout must always succeed for this browser, got %d", response.Code)
			}
			expectCleared(t, response)
		}
	}
}

// The Secure flag follows the configuration: true behind HTTPS, false for plain-HTTP local development.
func TestRefreshCookieFollowsTheSecureSetting(t *testing.T) {
	for _, secure := range []bool{false, true} {
		sessions := auth.NewMemorySessionStore()
		handler := &UserHandler{JWTSecret: sessionTestSecret, Service: NewUserService(sessionUsers{42: "learner"}), Sessions: sessions, SecureCookie: secure}
		router := gin.New()
		router.POST("/auth/refresh", handler.Refresh)
		router.POST("/auth/logout", handler.Logout)
		_, refresh, _ := sessions.Create(context.Background(), 42)
		// Refreshing sets the cookie and logging out clears it; both must carry the flag.
		for _, response := range []*httptest.ResponseRecorder{postWithCookie(router, "/auth/refresh", refresh), postWithCookie(router, "/auth/logout", "")} {
			if cookies := response.Result().Cookies(); len(cookies) != 1 || cookies[0].Secure != secure {
				t.Fatalf("SecureCookie=%v: want one cookie with Secure=%v, got %+v", secure, secure, cookies)
			}
		}
	}
}

func TestSessionEndpointsWithoutAStore(t *testing.T) {
	router := newSessionRouter(nil, sessionUsers{})
	for _, path := range []string{"/auth/refresh", "/auth/logout"} {
		if response := postWithCookie(router, path, "x"); response.Code != http.StatusInternalServerError {
			t.Fatalf("%s without a session store must be a wiring error, got %d", path, response.Code)
		}
	}
}
