package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"gostream/internal/database"
	"gostream/internal/user"
)

// TestAuthSessionHTTPIntegration signs in through the real router against MySQL and checks the
// session rules end to end, with sessions kept in Redis (miniredis) and in process memory:
// refreshing rotates the cookie, replaying an old refresh token ends the session, logging out
// makes the access token fail at once, and a Redis outage keeps signed-in requests working while
// login and refresh answer 503. GOSTREAM_TEST_MYSQL_DSN must point to a disposable server.
func TestAuthSessionHTTPIntegration(t *testing.T) {
	dsn := os.Getenv("GOSTREAM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set GOSTREAM_TEST_MYSQL_DSN to a disposable MySQL server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := database.OpenDB(dsn)
	if err != nil {
		t.Fatal("cannot connect to the disposable MySQL server")
	}
	defer admin.Close()
	name := fmt.Sprintf("gostream_auth_session_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.ExecContext(cleanup, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.DBName = name
	config.ParseTime = true
	pool, err := database.OpenDB(config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schema, err := os.ReadFile("../../scripts/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range strings.Split(string(schema), ";") {
		if statement = strings.TrimSpace(statement); strings.HasPrefix(statement, "CREATE TABLE ") {
			if _, err := pool.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	db := database.WrapSQL(pool)
	secret := []byte("test-only-jwt-secret-at-least-32-bytes")

	for _, tc := range []struct {
		name  string
		redis bool
	}{{name: "redis", redis: true}, {name: "in_process"}} {
		t.Run(tc.name, func(t *testing.T) {
			var redisServer *miniredis.Miniredis
			var redisClient *redis.Client
			if tc.redis {
				redisServer = miniredis.RunT(t)
				redisClient = newRedisClient(redisServer.Addr(), "")
				defer redisClient.Close()
			}
			server := httptest.NewServer(newRouter(db, secret, newTestFeedCursor(t), newTestStorage(t), redisClient, false))
			defer server.Close()
			client := &http.Client{Timeout: 5 * time.Second}
			// send returns the status, the body and the refresh cookie the response set ("" if none).
			send := func(method, path, token, cookie string, payload any) (int, []byte, *http.Cookie) {
				t.Helper()
				var body io.Reader
				if payload != nil {
					data, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					body = bytes.NewReader(data)
				}
				request, err := http.NewRequestWithContext(ctx, method, server.URL+path, body)
				if err != nil {
					t.Fatal(err)
				}
				if payload != nil {
					request.Header.Set("Content-Type", "application/json")
				}
				if token != "" {
					request.Header.Set("Authorization", "Bearer "+token)
				}
				if cookie != "" {
					request.AddCookie(&http.Cookie{Name: user.RefreshCookieName, Value: cookie})
				}
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				data, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				for _, set := range response.Cookies() {
					if set.Name == user.RefreshCookieName {
						return response.StatusCode, data, set
					}
				}
				return response.StatusCode, data, nil
			}
			credentials := map[string]string{"username": "auth_" + tc.name, "password": "auth-session-test-password"}
			if status, data, _ := send(http.MethodPost, "/users", "", "", credentials); status != http.StatusCreated {
				t.Fatalf("register: %d %s", status, data)
			}
			login := func() (string, string) {
				t.Helper()
				status, data, cookie := send(http.MethodPost, "/login", "", "", credentials)
				var body struct {
					Token string `json:"token"`
				}
				if status != http.StatusOK || json.Unmarshal(data, &body) != nil || body.Token == "" || cookie == nil || !cookie.HttpOnly || cookie.Path != "/auth" {
					t.Fatalf("login must return an access token and set the refresh cookie: %d %s %+v", status, data, cookie)
				}
				return body.Token, cookie.Value
			}
			me := func(token string) int {
				t.Helper()
				status, _, _ := send(http.MethodGet, "/me", token, "", nil)
				return status
			}
			refresh := func(cookie string) (int, string, string) {
				t.Helper()
				status, data, set := send(http.MethodPost, "/auth/refresh", "", cookie, nil)
				var body struct {
					Token string `json:"token"`
				}
				_ = json.Unmarshal(data, &body)
				next := ""
				if set != nil {
					next = set.Value
				}
				return status, body.Token, next
			}

			// 1. A refresh hands out a new access token and a new refresh token for the same session.
			loginToken, firstCookie := login()
			if status := me(loginToken); status != http.StatusOK {
				t.Fatalf("a fresh login must reach /me, got %d", status)
			}
			status, refreshedToken, secondCookie := refresh(firstCookie)
			if status != http.StatusOK || refreshedToken == "" || secondCookie == "" || secondCookie == firstCookie {
				t.Fatalf("refresh must rotate the cookie: %d, token %q, cookie %q", status, refreshedToken, secondCookie)
			}
			if me(refreshedToken) != http.StatusOK || me(loginToken) != http.StatusOK {
				t.Fatal("both access tokens of a live session must keep working")
			}
			// 2. Replaying the first refresh token ends the whole session: every token of it fails now.
			if status, _, cleared := refresh(firstCookie); status != http.StatusUnauthorized || cleared != "" {
				t.Fatalf("a replayed refresh token must be refused and cleared: %d, cookie %q", status, cleared)
			}
			if me(refreshedToken) != http.StatusUnauthorized || me(loginToken) != http.StatusUnauthorized {
				t.Fatal("after reuse every access token of that session must be rejected at once")
			}
			if status, _, _ := refresh(secondCookie); status != http.StatusUnauthorized {
				t.Fatalf("after reuse even the newest refresh token is dead, got %d", status)
			}
			// 3. Logging out makes the access token fail immediately, not when it expires.
			token, cookie := login()
			if status, _, cleared := send(http.MethodPost, "/auth/logout", "", cookie, nil); status != http.StatusNoContent || cleared == nil || cleared.MaxAge >= 0 {
				t.Fatalf("logout must answer 204 and clear the cookie: %d %+v", status, cleared)
			}
			if me(token) != http.StatusUnauthorized {
				t.Fatal("an access token must stop working as soon as its session logs out")
			}
			// The feed lets guests in, but a logged out token is refused, not treated as signed in.
			if status, _, _ := send(http.MethodGet, "/feed", token, "", nil); status != http.StatusUnauthorized {
				t.Fatalf("the feed must refuse a logged out token, got %d", status)
			}
			if status, _, _ := refresh(cookie); status != http.StatusUnauthorized {
				t.Fatalf("a logged out refresh token cannot refresh, got %d", status)
			}
			if !tc.redis {
				return
			}
			// 4. While Redis is down, signed-in requests still pass on the signature (for at most
			//    15 minutes), but no new session can be created or refreshed.
			token, cookie = login()
			redisServer.Close()
			if me(token) != http.StatusOK {
				t.Fatal("an outage must not sign out a valid access token")
			}
			if status, _, _ := refresh(cookie); status != http.StatusServiceUnavailable {
				t.Fatalf("refresh during an outage must be 503, got %d", status)
			}
			if status, _, _ := send(http.MethodPost, "/login", "", "", credentials); status != http.StatusServiceUnavailable {
				t.Fatalf("login during an outage must be 503, got %d", status)
			}
			// 5. Redis keeps its data across the restart, so the same cookie works again afterwards.
			if err := redisServer.Restart(); err != nil {
				t.Fatal(err)
			}
			if status, _, _ := refresh(cookie); status != http.StatusOK {
				t.Fatalf("after Redis recovers the session must refresh again, got %d", status)
			}
		})
	}
}
