package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gostream/internal/database"
)

// TestFollowingFeedHTTPMySQLIntegration drives GET /feed/following through the real
// router: accounts and relations are created over HTTP, videos are inserted directly,
// and the feed is read page by page. GOSTREAM_TEST_MYSQL_DSN must point to a
// disposable server; the test works in its own random database and drops it.
func TestFollowingFeedHTTPMySQLIntegration(t *testing.T) {
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
	name := fmt.Sprintf("gostream_following_http_%d", time.Now().UnixNano())
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
	config.ParseTime = true
	pool, err := database.OpenDB(config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// users, videos and video_likes mirror scripts/schema.sql; follows comes from the real migration.
	for _, ddl := range []string{
		`CREATE TABLE users (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
			username VARCHAR(32) NOT NULL,
			password_hash VARCHAR(255) NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (id),
			UNIQUE KEY uk_users_username (username)
		)`,
		`CREATE TABLE videos (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
			author_id BIGINT UNSIGNED NOT NULL,
			title VARCHAR(100) NOT NULL,
			play_url VARCHAR(1024) NOT NULL,
			cover_url VARCHAR(1024) NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (id),
			KEY idx_videos_author_id (author_id),
			KEY idx_videos_created_at_id (created_at, id),
			CONSTRAINT fk_videos_author FOREIGN KEY (author_id) REFERENCES users(id)
		)`,
		`CREATE TABLE video_likes (
			user_id BIGINT UNSIGNED NOT NULL,
			video_id BIGINT UNSIGNED NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, video_id),
			KEY idx_video_likes_video_id (video_id)
		)`,
	} {
		if _, err := pool.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
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
	do := func(method, path, token string, payload any) (int, []byte) {
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
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	type account struct {
		id       uint64
		token    string
		username string
	}
	signUp := func(username string) account {
		t.Helper()
		credentials := map[string]string{"username": username, "password": "following-test-password"}
		if status, _ := do(http.MethodPost, "/users", "", credentials); status != http.StatusCreated {
			t.Fatalf("register %s: got %d", username, status)
		}
		status, data := do(http.MethodPost, "/login", "", credentials)
		var login struct {
			ID    uint64 `json:"id"`
			Token string `json:"token"`
		}
		if status != http.StatusOK || json.Unmarshal(data, &login) != nil || login.ID == 0 || login.Token == "" {
			t.Fatalf("login %s: got %d %s", username, status, data)
		}
		return account{id: login.ID, token: login.Token, username: username}
	}
	a, b, c := signUp("feed_a"), signUp("feed_b"), signUp("feed_c")
	stamp := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	for _, v := range []struct {
		id     uint64
		author account
		at     time.Time
	}{{201, b, stamp}, {202, b, stamp.Add(time.Minute)}, {203, c, stamp.Add(2 * time.Minute)}} {
		if _, err := pool.ExecContext(ctx, "INSERT INTO videos (id, author_id, title, play_url, cover_url, created_at) VALUES (?,?,?,?,?,?)",
			v.id, v.author.id, "following fixture", "/media/videos/f.mp4", "/cover.svg", v.at); err != nil {
			t.Fatal(err)
		}
	}
	if status, _ := do(http.MethodPut, fmt.Sprintf("/users/%d/follow", b.id), a.token, nil); status != http.StatusNoContent {
		t.Fatalf("A follows B: got %d", status)
	}

	type page struct {
		Videos []struct {
			ID         uint64 `json:"id"`
			AuthorID   uint64 `json:"author_id"`
			AuthorName string `json:"author_name"`
		} `json:"videos"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	}
	read := func(path, token string, wantIDs []uint64, wantMore bool) page {
		t.Helper()
		status, data := do(http.MethodGet, path, token, nil)
		var body page
		if status != http.StatusOK || json.Unmarshal(data, &body) != nil {
			t.Fatalf("GET %s: got %d %s", path, status, data)
		}
		ids := make([]uint64, len(body.Videos))
		for i, video := range body.Videos {
			ids[i] = video.ID
		}
		if !reflect.DeepEqual(ids, wantIDs) || body.HasMore != wantMore || (body.NextCursor != "") != wantMore {
			t.Fatalf("GET %s: got %v (has_more %v), want %v (has_more %v)", path, ids, body.HasMore, wantIDs, wantMore)
		}
		return body
	}
	expectInvalidCursor := func(path, token string) {
		t.Helper()
		status, data := do(http.MethodGet, path, token, nil)
		var body map[string]string
		if status != http.StatusBadRequest || json.Unmarshal(data, &body) != nil || body["code"] != "INVALID_CURSOR" {
			t.Fatalf("GET %s: want 400 INVALID_CURSOR, got %d %s", path, status, data)
		}
	}

	// A sees only B's videos, one per page, with the author's name filled in.
	first := read("/feed/following?limit=1", a.token, []uint64{202}, true)
	if first.Videos[0].AuthorID != b.id || first.Videos[0].AuthorName != b.username {
		t.Fatalf("author metadata missing: %+v", first.Videos[0])
	}
	read("/feed/following?limit=1&cursor="+url.QueryEscape(first.NextCursor), a.token, []uint64{201}, false)
	// The public latest feed still lists every video and issues its own cursor.
	latest := read("/feed?limit=1", "", []uint64{203}, true)
	// Cursors cannot cross feeds in either direction.
	expectInvalidCursor("/feed/following?cursor="+url.QueryEscape(latest.NextCursor), a.token)
	expectInvalidCursor("/feed?cursor="+url.QueryEscape(first.NextCursor), "")
	// The viewer comes only from the token; relations are directed.
	if status, _ := do(http.MethodGet, "/feed/following", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous following feed: want 401, got %d", status)
	}
	read("/feed/following", b.token, []uint64{}, false)
	// After A unfollows B, a new query no longer shows B's videos.
	if status, _ := do(http.MethodDelete, fmt.Sprintf("/users/%d/follow", b.id), a.token, nil); status != http.StatusNoContent {
		t.Fatalf("A unfollows B: got %d", status)
	}
	read("/feed/following", a.token, []uint64{}, false)
}
