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
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gostream/internal/database"
)

// TestVideoDetailCacheHTTPIntegration drives GET/DELETE /videos/:id through the real
// router with MySQL and an in-process Redis (miniredis). It proves that a cache hit
// skips MySQL, that a Redis outage falls back to MySQL, and that deleting a video
// invalidates its cache entry. GOSTREAM_TEST_MYSQL_DSN must point to a disposable server.
func TestVideoDetailCacheHTTPIntegration(t *testing.T) {
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
	name := fmt.Sprintf("gostream_video_cache_%d", time.Now().UnixNano())
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
	// users and videos mirror scripts/schema.sql.
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
			CONSTRAINT fk_videos_author FOREIGN KEY (author_id) REFERENCES users(id)
		)`,
	} {
		if _, err := pool.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	db := database.WrapSQL(pool)
	var queries atomic.Int64
	if err := db.Callback().Query().After("gorm:query").Register("test:count-video-query", func(*gorm.DB) { queries.Add(1) }); err != nil {
		t.Fatal(err)
	}
	cache := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: cache.Addr(), DialTimeout: time.Second, ReadTimeout: 300 * time.Millisecond, WriteTimeout: 300 * time.Millisecond, MaxRetries: -1})
	defer redisClient.Close()
	secret := []byte("test-only-jwt-secret-at-least-32-bytes")
	server := httptest.NewServer(newRouter(db, secret, newTestFeedCursor(t), newTestStorage(t), redisClient))
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
	credentials := map[string]string{"username": "cache_author", "password": "video-cache-test-password"}
	if status, _ := do(http.MethodPost, "/users", "", credentials); status != http.StatusCreated {
		t.Fatalf("register: got %d", status)
	}
	status, data := do(http.MethodPost, "/login", "", credentials)
	var author struct {
		ID    uint64 `json:"id"`
		Token string `json:"token"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &author) != nil || author.Token == "" {
		t.Fatalf("login: got %d %s", status, data)
	}
	if _, err := pool.ExecContext(ctx, "INSERT INTO videos (id, author_id, title, play_url, cover_url, created_at) VALUES (301, ?, 'cached video', '/media/videos/c.mp4', '/cover.svg', ?)", author.ID, time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	const key = "gostream:video:v1:301"
	// readDetail returns the response and how many MySQL queries it caused.
	readDetail := func(wantStatus int) (map[string]any, int64) {
		t.Helper()
		before := queries.Load()
		status, data := do(http.MethodGet, "/videos/301", "", nil)
		if status != wantStatus {
			t.Fatalf("GET /videos/301: want %d, got %d %s", wantStatus, status, data)
		}
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		return body, queries.Load() - before
	}

	// 1. A miss reads MySQL once and fills Redis with a jittered TTL.
	first, used := readDetail(http.StatusOK)
	if used != 1 || first["title"] != "cached video" || !cache.Exists(key) {
		t.Fatalf("miss must read MySQL once and fill the cache: queries %d, body %v", used, first)
	}
	if ttl := cache.TTL(key); ttl < 10*time.Minute || ttl >= 11*time.Minute {
		t.Fatalf("TTL %v must be within [10m, 11m)", ttl)
	}
	// 2. A hit returns the same body without any MySQL query.
	second, used := readDetail(http.StatusOK)
	if used != 0 || fmt.Sprint(second) != fmt.Sprint(first) {
		t.Fatalf("hit must skip MySQL and return the same body: queries %d, body %v", used, second)
	}
	// 3. While Redis is down the detail page still works from MySQL.
	cache.Close()
	if _, used := readDetail(http.StatusOK); used != 1 {
		t.Fatalf("Redis outage must fall back to MySQL: queries %d", used)
	}
	// 4. Once Redis is back the retained entry is served again.
	if err := cache.Restart(); err != nil {
		t.Fatal(err)
	}
	if _, used := readDetail(http.StatusOK); used != 0 {
		t.Fatalf("after Redis recovers the cache must be used again: queries %d", used)
	}
	// 5. Deleting the video invalidates the entry, so it is not served from a stale cache.
	if status, data := do(http.MethodDelete, "/videos/301", author.Token, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE: got %d %s", status, data)
	}
	if cache.Exists(key) {
		t.Fatal("deleting the video must invalidate its cache entry")
	}
	if body, used := readDetail(http.StatusNotFound); used != 1 || body["error"] != "视频不存在" {
		t.Fatalf("deleted video must be 404 from MySQL: queries %d, body %v", used, body)
	}
}
