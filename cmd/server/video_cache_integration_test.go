package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gostream/internal/database"
)

// TestVideoDetailCacheHTTPIntegration drives the video detail cache through two real routers,
// playing two API instances that share MySQL and an in-process Redis (miniredis). It proves
// the local and Redis layers, the short "missing" marker, the Redis outage fallback, and that
// delete and publish clear stale entries. GOSTREAM_TEST_MYSQL_DSN must point to a disposable server.
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
	// Publishing needs the real uploads table, so the whole schema comes from scripts/schema.sql.
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
	var queries atomic.Int64
	if err := db.Callback().Query().After("gorm:query").Register("test:count-video-query", func(*gorm.DB) { queries.Add(1) }); err != nil {
		t.Fatal(err)
	}
	redisServer := miniredis.RunT(t)
	redisClient := newRedisClient(redisServer.Addr(), "") // the same client options as production
	defer redisClient.Close()
	secret := []byte("test-only-jwt-secret-at-least-32-bytes")
	storage := newTestStorage(t)
	// Each router has its own local cache; both share MySQL and Redis, like two API instances.
	first := httptest.NewServer(newRouter(db, secret, newTestFeedCursor(t), storage, redisClient, false, nil))
	defer first.Close()
	second := httptest.NewServer(newRouter(db, secret, newTestFeedCursor(t), storage, redisClient, false, nil))
	defer second.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	send := func(server *httptest.Server, method, path, token, contentType string, body io.Reader) (int, []byte) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
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
	sendJSON := func(method, path, token string, payload any) (int, []byte) {
		t.Helper()
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return send(first, method, path, token, "application/json", bytes.NewReader(data))
	}
	credentials := map[string]string{"username": "cache_author", "password": "video-cache-test-password"}
	if status, _ := sendJSON(http.MethodPost, "/users", "", credentials); status != http.StatusCreated {
		t.Fatalf("register: got %d", status)
	}
	status, data := sendJSON(http.MethodPost, "/login", "", credentials)
	var author struct {
		ID    uint64 `json:"id"`
		Token string `json:"token"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &author) != nil || author.Token == "" {
		t.Fatalf("login: got %d %s", status, data)
	}
	for _, id := range []uint64{301, 302} {
		if _, err := pool.ExecContext(ctx, "INSERT INTO videos (id, author_id, title, play_url, cover_url, created_at) VALUES (?, ?, ?, '/media/videos/c.mp4', '/cover.svg', ?)", id, author.ID, fmt.Sprintf("cached video %d", id), time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	key := func(id uint64) string { return fmt.Sprintf("gostream:video:v1:%d", id) }
	// readDetail returns the response and how many MySQL queries it caused.
	readDetail := func(server *httptest.Server, id uint64, wantStatus int) (map[string]any, int64) {
		t.Helper()
		before := queries.Load()
		status, data := send(server, http.MethodGet, fmt.Sprintf("/videos/%d", id), "", "", nil)
		if status != wantStatus {
			t.Fatalf("GET /videos/%d: want %d, got %d %s", id, wantStatus, status, data)
		}
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		return body, queries.Load() - before
	}
	// requireMarker checks that Redis remembers id as missing, for at most 30 seconds.
	requireMarker := func(id uint64) {
		t.Helper()
		if value, err := redisServer.Get(key(id)); err != nil || value != "missing" {
			t.Fatalf("video %d: expected a missing marker in Redis, got %q (%v)", id, value, err)
		}
		if ttl := redisServer.TTL(key(id)); ttl <= 0 || ttl > 30*time.Second {
			t.Fatalf("video %d: the missing marker must expire within 30s, TTL %v", id, ttl)
		}
	}

	// 1. A miss reads MySQL once and fills Redis with a jittered TTL.
	original, used := readDetail(first, 301, http.StatusOK)
	if used != 1 || original["title"] != "cached video 301" || !redisServer.Exists(key(301)) {
		t.Fatalf("miss must read MySQL once and fill Redis: queries %d, body %v", used, original)
	}
	if ttl := redisServer.TTL(key(301)); ttl < 10*time.Minute || ttl >= 11*time.Minute {
		t.Fatalf("TTL %v must be within [10m, 11m)", ttl)
	}
	// 2. A repeat on the same instance is answered by its local cache: no MySQL, no Redis.
	commands := redisServer.CommandCount()
	repeat, used := readDetail(first, 301, http.StatusOK)
	if sent := redisServer.CommandCount() - commands; used != 0 || sent != 0 || fmt.Sprint(repeat) != fmt.Sprint(original) {
		t.Fatalf("local hit must skip MySQL and Redis: queries %d, Redis commands %d, body %v", used, sent, repeat)
	}
	// 3. The other instance starts with an empty local cache, so Redis answers it without MySQL.
	commands = redisServer.CommandCount()
	other, used := readDetail(second, 301, http.StatusOK)
	if sent := redisServer.CommandCount() - commands; used != 0 || sent == 0 || fmt.Sprint(other) != fmt.Sprint(original) {
		t.Fatalf("Redis hit must skip MySQL: queries %d, Redis commands %d, body %v", used, sent, other)
	}

	// 4. Deleting clears this instance's local copy and Redis; MySQL then answers 404 once and
	//    the short marker absorbs the repeats. (The second instance may keep showing its local
	//    copy for up to 5 seconds; that window is pinned by TestLocalVideoCacheIsPerInstance.)
	if status, data := send(first, http.MethodDelete, "/videos/301", author.Token, "", nil); status != http.StatusNoContent {
		t.Fatalf("DELETE: got %d %s", status, data)
	}
	if redisServer.Exists(key(301)) {
		t.Fatal("deleting the video must invalidate its Redis entry")
	}
	if body, used := readDetail(first, 301, http.StatusNotFound); used != 1 || body["error"] != "视频不存在" {
		t.Fatalf("deleted video must be 404 from MySQL: queries %d, body %v", used, body)
	}
	requireMarker(301)
	if _, used := readDetail(first, 301, http.StatusNotFound); used != 0 {
		t.Fatalf("the missing marker must answer repeated reads: queries %d", used)
	}
	// 5. Probing an ID that never existed costs one MySQL query; the marker in Redis then
	//    answers the other instance too (cache penetration protection).
	if _, used := readDetail(first, 999, http.StatusNotFound); used != 1 {
		t.Fatalf("the first probe must ask MySQL once: queries %d", used)
	}
	requireMarker(999)
	if _, used := readDetail(second, 999, http.StatusNotFound); used != 0 {
		t.Fatalf("the shared marker must answer the other instance: queries %d", used)
	}

	// 6. While Redis is down the page still works from MySQL, and the local copy absorbs the
	//    repeats even though writing Redis failed.
	redisServer.Close()
	if body, used := readDetail(first, 302, http.StatusOK); used != 1 || body["title"] != "cached video 302" {
		t.Fatalf("Redis outage must fall back to MySQL: queries %d, body %v", used, body)
	}
	if _, used := readDetail(first, 302, http.StatusOK); used != 0 {
		t.Fatalf("the local copy must answer repeats during the outage: queries %d", used)
	}
	// 7. Once Redis is back, a miss on the other instance fills it again.
	if err := redisServer.Restart(); err != nil {
		t.Fatal(err)
	}
	if _, used := readDetail(second, 302, http.StatusOK); used != 1 || !redisServer.Exists(key(302)) {
		t.Fatalf("after Redis recovers a miss must refill it: queries %d", used)
	}

	// 8. A crawler probes the next ID before it exists. Publishing must clear that marker, or the
	//    new video would be 404 for up to 30 seconds.
	const nextID = 303 // the explicit inserts above moved AUTO_INCREMENT past 302
	if _, used := readDetail(first, nextID, http.StatusNotFound); used != 1 {
		t.Fatalf("the probe must ask MySQL once: queries %d", used)
	}
	requireMarker(nextID)
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	part, err := writer.CreateFormFile("file", "probe.mp4")
	if err != nil {
		t.Fatal(err)
	}
	// MIME identification bytes, not a claim of playable video decoding.
	if _, err := part.Write([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2', 'i', 's', 'o', 'm'}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	status, data = send(first, http.MethodPost, "/uploads/videos", author.Token, writer.FormDataContentType(), &form)
	var upload struct {
		PlayURL string `json:"play_url"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &upload) != nil || upload.PlayURL == "" {
		t.Fatalf("upload: got %d %s", status, data)
	}
	status, data = sendJSON(http.MethodPost, "/videos", author.Token, map[string]string{"title": "published after probe", "play_url": upload.PlayURL, "cover_url": "/cover.svg"})
	var published struct {
		ID uint64 `json:"id"`
	}
	if status != http.StatusCreated || json.Unmarshal(data, &published) != nil || published.ID != nextID {
		t.Fatalf("publish: want video %d, got %d %s", nextID, status, data)
	}
	if redisServer.Exists(key(nextID)) {
		t.Fatal("publishing must clear the missing marker in Redis")
	}
	if body, used := readDetail(first, nextID, http.StatusOK); used != 1 || body["title"] != "published after probe" {
		t.Fatalf("the new video must be visible at once: queries %d, body %v", used, body)
	}
	if body, used := readDetail(second, nextID, http.StatusOK); used != 0 || body["title"] != "published after probe" {
		t.Fatalf("the other instance must get the new video from Redis: queries %d, body %v", used, body)
	}
}
