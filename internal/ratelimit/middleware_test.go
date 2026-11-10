package ratelimit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// newLimitedRouter puts the middleware in front of a handler that only counts how often it runs.
func newLimitedRouter(limiter Limiter, rule Rule, keyOf KeyFunc, before ...gin.HandlerFunc) (*gin.Engine, *atomic.Int64) {
	router := gin.New()
	router.SetTrustedProxies(nil) // the same setting as the real router
	var reached atomic.Int64
	handlers := append(before, Middleware(limiter, rule, keyOf), func(c *gin.Context) {
		reached.Add(1)
		c.Status(http.StatusNoContent)
	})
	router.POST("/limited", handlers...)
	return router, &reached
}

func send(router http.Handler, remoteAddr string, header http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/limited", nil)
	request.RemoteAddr = remoteAddr
	for name, values := range header {
		request.Header[name] = values
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestMiddlewareRejectsOverTheLimitWith429(t *testing.T) {
	router, reached := newLimitedRouter(NewLocalLimiter(), Rule{Name: "login", Limit: 2, Window: time.Minute}, ByIP)
	for i := 1; i <= 2; i++ {
		if response := send(router, "192.0.2.1:1234", nil); response.Code != http.StatusNoContent {
			t.Fatalf("request %d: got %d", i, response.Code)
		}
	}
	response := send(router, "192.0.2.1:5678", nil) // the same IP from another port
	if response.Code != http.StatusTooManyRequests || reached.Load() != 2 {
		t.Fatalf("the 3rd request must be rejected before the handler: %d, handler ran %d times", response.Code, reached.Load())
	}
	retry, err := strconv.Atoi(response.Header().Get("Retry-After"))
	if err != nil || retry < 59 || retry > 60 {
		t.Fatalf("Retry-After must be the whole seconds left in the window, got %q", response.Header().Get("Retry-After"))
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["error"] != fmt.Sprintf("请求太频繁，请 %d 秒后再试", retry) {
		t.Fatalf("unexpected body %s", response.Body.String())
	}
	if response := send(router, "192.0.2.2:1234", nil); response.Code != http.StatusNoContent {
		t.Fatalf("another IP has its own allowance, got %d", response.Code)
	}
}

func TestMiddlewareRoundsRetryAfterUp(t *testing.T) {
	// 300ms left must not become "retry after 0 seconds", which would invite an immediate retry.
	router, _ := newLimitedRouter(&fakeLimiter{decision: Decision{RetryAfter: 300 * time.Millisecond}}, loginRule, ByIP)
	response := send(router, "192.0.2.1:1234", nil)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("got %d with Retry-After %q, want 429 with 1", response.Code, response.Header().Get("Retry-After"))
	}
}

func TestByIPIgnoresForwardedHeaders(t *testing.T) {
	router, _ := newLimitedRouter(NewLocalLimiter(), Rule{Name: "login", Limit: 1, Window: time.Minute}, ByIP)
	send(router, "192.0.2.1:1234", nil)
	// No proxy is trusted, so a client cannot dodge the limit by claiming another address.
	spoofed := http.Header{"X-Forwarded-For": {"198.51.100.7"}, "X-Real-Ip": {"198.51.100.7"}}
	if response := send(router, "192.0.2.1:1234", spoofed); response.Code != http.StatusTooManyRequests {
		t.Fatalf("a forged X-Forwarded-For must not open a new allowance, got %d", response.Code)
	}
}

func TestByUserCountsPerAuthenticatedUser(t *testing.T) {
	// Stands in for the JWT middleware, which puts the verified user ID into the context.
	setUser := func(c *gin.Context) {
		if id, err := strconv.ParseUint(c.GetHeader("X-Test-User"), 10, 64); err == nil {
			c.Set("userID", id)
		}
	}
	router, reached := newLimitedRouter(NewLocalLimiter(), Rule{Name: "write", Limit: 1, Window: time.Minute}, ByUser, setUser)
	user := func(id string) http.Header { return http.Header{"X-Test-User": {id}} }
	if first, second := send(router, "192.0.2.1:1", user("7")).Code, send(router, "192.0.2.1:1", user("7")).Code; first != http.StatusNoContent || second != http.StatusTooManyRequests {
		t.Fatalf("user 7: got %d then %d", first, second)
	}
	if response := send(router, "192.0.2.1:1", user("8")); response.Code != http.StatusNoContent {
		t.Fatalf("users behind the same IP have separate allowances, got %d", response.Code)
	}
	// Without an identity the route is wired wrongly: refuse instead of skipping the limit.
	before := reached.Load()
	if response := send(router, "192.0.2.1:1", nil); response.Code != http.StatusInternalServerError || reached.Load() != before {
		t.Fatalf("a request without a user must not reach the handler, got %d", response.Code)
	}
}

func TestMiddlewareLetsRequestsThroughWhenLimitingFails(t *testing.T) {
	router, reached := newLimitedRouter(&fakeLimiter{err: errors.New("redis down")}, loginRule, ByIP)
	if response := send(router, "192.0.2.1:1234", nil); response.Code != http.StatusNoContent || reached.Load() != 1 {
		t.Fatalf("a broken limiter must not take the site down, got %d", response.Code)
	}
}

func TestMiddlewareRejectsInvalidRulesAtStartup(t *testing.T) {
	valid := Rule{Name: "login", Limit: 1, Window: time.Minute}
	for _, tc := range []struct {
		name    string
		limiter Limiter
		rule    Rule
		keyOf   KeyFunc
	}{
		{name: "no_name", limiter: NewLocalLimiter(), rule: Rule{Limit: 1, Window: time.Minute}, keyOf: ByIP},
		{name: "no_limit", limiter: NewLocalLimiter(), rule: Rule{Name: "login", Window: time.Minute}, keyOf: ByIP},
		{name: "no_window", limiter: NewLocalLimiter(), rule: Rule{Name: "login", Limit: 1}, keyOf: ByIP},
		{name: "no_limiter", rule: valid, keyOf: ByIP},
		{name: "no_key", limiter: NewLocalLimiter(), rule: valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("an invalid rule must fail when the routes are built")
				}
			}()
			Middleware(tc.limiter, tc.rule, tc.keyOf)
		})
	}
}
