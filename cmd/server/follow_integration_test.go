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
	"reflect"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gostream/internal/database"
)

// TestFollowHTTPMySQLIntegration drives the real router end to end: accounts are
// registered and logged in over HTTP, then follow, status and unfollow run against
// MySQL. GOSTREAM_TEST_MYSQL_DSN must point to a disposable server; the test works
// in its own random database, drops it afterwards and never reads .env.
func TestFollowHTTPMySQLIntegration(t *testing.T) {
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
	name := fmt.Sprintf("gostream_follow_http_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
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
	pool, err := database.OpenDB(config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// users mirrors scripts/schema.sql; follows comes from the real migration file.
	if _, err := pool.ExecContext(ctx, `CREATE TABLE users (
		id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		username VARCHAR(32) NOT NULL,
		password_hash VARCHAR(255) NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_users_username (username)
	)`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../scripts/migrations/006_follows.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("006 migration failed: %v", err)
	}

	secret := []byte("test-only-jwt-secret-at-least-32-bytes")
	server := httptest.NewServer(newRouter(database.WrapSQL(pool), secret, newTestFeedCursor(t), newTestStorage(t)))
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	do := func(method, path, token string, payload any) (int, map[string]any) {
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
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var result map[string]any
		if response.StatusCode != http.StatusNoContent {
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatalf("%s %s: expected a JSON body: %v", method, path, err)
			}
		}
		return response.StatusCode, result
	}

	type account struct {
		id    uint64
		token string
	}
	signUp := func(username string) account {
		t.Helper()
		credentials := map[string]string{"username": username, "password": "follow-test-password"}
		if status, _ := do(http.MethodPost, "/users", "", credentials); status != http.StatusCreated {
			t.Fatalf("register %s: got %d", username, status)
		}
		status, result := do(http.MethodPost, "/login", "", credentials)
		id, _ := result["id"].(float64)
		token, _ := result["token"].(string)
		if status != http.StatusOK || id == 0 || token == "" {
			t.Fatalf("login %s: got %d %v", username, status, result)
		}
		return account{id: uint64(id), token: token}
	}
	a, b, c := signUp("follow_a"), signUp("follow_b"), signUp("follow_c")
	followPath := func(target uint64) string { return fmt.Sprintf("/users/%d/follow", target) }
	expect := func(want int, method string, who account, path string, payload any) {
		t.Helper()
		if status, result := do(method, path, who.token, payload); status != want {
			t.Fatalf("%s %s as user %d: want %d, got %d %v", method, path, who.id, want, status, result)
		}
	}
	following := func(who account, target uint64) bool {
		t.Helper()
		status, result := do(http.MethodGet, followPath(target), who.token, nil)
		value, ok := result["following"].(bool)
		if status != http.StatusOK || !ok || len(result) != 1 {
			t.Fatalf("status of %d -> %d: got %d %v", who.id, target, status, result)
		}
		return value
	}

	// A follows B; the relation is directed.
	expect(http.StatusNoContent, http.MethodPut, a, followPath(b.id), nil)
	if !following(a, b.id) || following(b, a.id) {
		t.Fatal("A -> B must exist without implying B -> A")
	}
	// Repeating the follow is idempotent.
	expect(http.StatusNoContent, http.MethodPut, a, followPath(b.id), nil)
	// Rejected inputs never create relations.
	expect(http.StatusBadRequest, http.MethodPut, a, followPath(a.id), nil)
	expect(http.StatusNotFound, http.MethodPut, a, followPath(999999), nil)
	expect(http.StatusNotFound, http.MethodGet, a, followPath(999999), nil)
	expect(http.StatusBadRequest, http.MethodPut, a, "/users/abc/follow", nil)
	expect(http.StatusUnauthorized, http.MethodPut, account{}, followPath(b.id), nil)
	// C acts only as C: a forged follower_id is ignored and C cannot remove A's relation.
	expect(http.StatusNoContent, http.MethodPut, c, followPath(b.id), map[string]uint64{"follower_id": a.id})
	expect(http.StatusNoContent, http.MethodDelete, c, followPath(b.id), map[string]uint64{"follower_id": a.id})
	if !following(a, b.id) {
		t.Fatal("C must not be able to remove A -> B")
	}
	expect(http.StatusNoContent, http.MethodPut, c, followPath(b.id), nil)
	// A unfollows B, and repeating it is still a success.
	expect(http.StatusNoContent, http.MethodDelete, a, followPath(b.id), nil)
	if following(a, b.id) {
		t.Fatal("A -> B must be gone after unfollow")
	}
	expect(http.StatusNoContent, http.MethodDelete, a, followPath(b.id), nil)

	rows, err := pool.QueryContext(ctx, "SELECT follower_id, followee_id FROM follows ORDER BY follower_id, followee_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var relations [][2]uint64
	for rows.Next() {
		var pair [2]uint64
		if err := rows.Scan(&pair[0], &pair[1]); err != nil {
			t.Fatal(err)
		}
		relations = append(relations, pair)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := [][2]uint64{{c.id, b.id}}; !reflect.DeepEqual(relations, want) {
		t.Fatalf("expected only C -> B to remain, got %v", relations)
	}
}
