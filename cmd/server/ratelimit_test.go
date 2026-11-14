package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gostream/internal/auth"
)

// TestRouterRateLimits checks which routes are limited and by how much, with Redis (counters
// shared by every instance) and without it (counting in this process). The bodies are invalid
// on purpose: an allowed request stops inside its handler (400 for the body, 401 for a refresh
// without a cookie), so no database is needed, and a rejected one gets 429 before the handler runs.
func TestRouterRateLimits(t *testing.T) {
	secret := []byte("test-only-jwt-secret-at-least-32-bytes")
	redisServer := miniredis.RunT(t)
	redisClient := newRedisClient(redisServer.Addr(), "")
	defer redisClient.Close()
	// tokenFor signs a user in through a session in Redis, which the router checks on every request.
	sessions := &auth.RedisSessionStore{Client: redisClient}
	tokenFor := func(userID uint64) string {
		t.Helper()
		sessionID, _, err := sessions.Create(context.Background(), userID)
		if err != nil {
			t.Fatal(err)
		}
		token, err := auth.GenerateToken(userID, sessionID, secret)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	for _, tc := range []struct {
		name  string
		redis *redis.Client
	}{{name: "redis", redis: redisClient}, {name: "in_process", redis: nil}} {
		t.Run(tc.name, func(t *testing.T) {
			router := newRouter(nil, secret, newTestFeedCursor(t), newTestStorage(t), tc.redis, false, nil)
			do := func(method, path, token string) *httptest.ResponseRecorder {
				t.Helper()
				request := httptest.NewRequest(method, path, strings.NewReader("not json"))
				request.Header.Set("Content-Type", "application/json")
				if token != "" {
					request.Header.Set("Authorization", "Bearer "+token)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				return response
			}
			// expectLimit sends limit requests that reach the handler (allowed), then one that must be stopped.
			expectLimit := func(name string, limit int, method, path, token string, allowed, retryMax int) {
				t.Helper()
				for i := 1; i <= limit; i++ {
					if response := do(method, path, token); response.Code != allowed {
						t.Fatalf("%s request %d must reach the handler, got %d %s", name, i, response.Code, response.Body.String())
					}
				}
				response := do(method, path, token)
				if response.Code != http.StatusTooManyRequests {
					t.Fatalf("%s request %d must be rejected, got %d", name, limit+1, response.Code)
				}
				if retry, err := strconv.Atoi(response.Header().Get("Retry-After")); err != nil || retry < 1 || retry > retryMax {
					t.Fatalf("%s: Retry-After %q must be 1 to %d seconds", name, response.Header().Get("Retry-After"), retryMax)
				}
			}
			expectLimit("login", 10, http.MethodPost, "/login", "", http.StatusBadRequest, 60)
			expectLimit("register", 10, http.MethodPost, "/users", "", http.StatusBadRequest, 3600)
			expectLimit("refresh", 30, http.MethodPost, "/auth/refresh", "", http.StatusUnauthorized, 60)
			// Writes need a signed-in user, and only a session in Redis is reachable from the test; the
			// in-process write limit is covered by the ratelimit package itself.
			if tc.redis != nil {
				userA, userB := tokenFor(7), tokenFor(8)
				expectLimit("write", 60, http.MethodPost, "/videos", userA, http.StatusBadRequest, 60)
				// Every write shares one allowance per user, and other users keep their own.
				if response := do(http.MethodPut, "/videos/1/like", userA); response.Code != http.StatusTooManyRequests {
					t.Fatalf("likes share the write allowance, got %d", response.Code)
				}
				for _, path := range []string{"/notifications/1/read", "/notifications/read?up_to=1"} {
					if response := do(http.MethodPut, path, userA); response.Code != http.StatusTooManyRequests {
						t.Fatalf("marking notifications read shares the write allowance, got %d for %s", response.Code, path)
					}
				}
				if response := do(http.MethodPost, "/videos", userB); response.Code != http.StatusBadRequest {
					t.Fatalf("another user has their own allowance, got %d", response.Code)
				}
			}
			// Reads are not limited in this step.
			for range 100 {
				if response := do(http.MethodGet, "/feed?limit=abc", ""); response.Code != http.StatusBadRequest {
					t.Fatalf("reading the feed must not be limited, got %d", response.Code)
				}
			}
			if tc.redis != nil {
				for key, window := range map[string]time.Duration{
					"gostream:ratelimit:v1:login:ip:192.0.2.1":    time.Minute,
					"gostream:ratelimit:v1:register:ip:192.0.2.1": time.Hour,
					"gostream:ratelimit:v1:refresh:ip:192.0.2.1":  time.Minute,
					"gostream:ratelimit:v1:write:user:7":          time.Minute,
				} {
					if ttl := redisServer.TTL(key); ttl <= 0 || ttl > window {
						t.Fatalf("%s must be a counter that expires within %v, TTL %v", key, window, ttl)
					}
				}
			}
		})
	}
}

// TestRouterRateLimitsWhileRedisIsDown shows the failover: with Redis gone the limits still hold,
// counted in this process.
func TestRouterRateLimitsWhileRedisIsDown(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := newRedisClient(redisServer.Addr(), "")
	defer redisClient.Close()
	router := newRouter(nil, []byte("test-only-jwt-secret-at-least-32-bytes"), newTestFeedCursor(t), newTestStorage(t), redisClient, false, nil)
	redisServer.Close()
	for i := 1; i <= 11; i++ {
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("not json"))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		want := http.StatusBadRequest
		if i == 11 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("login %d while Redis is down: want %d, got %d", i, want, response.Code)
		}
	}
}
