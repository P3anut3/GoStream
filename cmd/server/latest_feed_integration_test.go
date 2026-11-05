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
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gostream/internal/database"
)

// TestLatestFeedTimelineHTTPIntegration reads GET /feed through two routers over the same MySQL
// data: one with the Redis timeline (miniredis) and one without. Every page must be identical,
// cached pages must not query the videos table, personal like state must not leak through the
// shared cache, and publishing or deleting must show up on the next read. GOSTREAM_TEST_MYSQL_DSN
// must point to a disposable server.
func TestLatestFeedTimelineHTTPIntegration(t *testing.T) {
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
	name := fmt.Sprintf("gostream_latest_feed_%d", time.Now().UnixNano())
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
	// Counts reads of the videos table: feed pages and timeline rebuilds. Author names and likes
	// come from other tables and are always read from MySQL.
	var videoQueries atomic.Int64
	if err := db.Callback().Query().After("gorm:query").Register("test:count-videos-table", func(tx *gorm.DB) {
		if tx.Statement.Table == "videos" {
			videoQueries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	redisServer := miniredis.RunT(t)
	redisClient := newRedisClient(redisServer.Addr(), "")
	defer redisClient.Close()
	secret := []byte("test-only-jwt-secret-at-least-32-bytes")
	storage := newTestStorage(t)
	cached := httptest.NewServer(newRouter(db, secret, newTestFeedCursor(t), storage, redisClient))
	defer cached.Close()
	direct := httptest.NewServer(newRouter(db, secret, newTestFeedCursor(t), storage, nil)) // MySQL only
	defer direct.Close()
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
	type account struct {
		ID    uint64 `json:"id"`
		Token string `json:"token"`
	}
	signUp := func(username string) account {
		t.Helper()
		credentials, _ := json.Marshal(map[string]string{"username": username, "password": "latest-feed-test-password"})
		if status, data := send(direct, http.MethodPost, "/users", "", "application/json", bytes.NewReader(credentials)); status != http.StatusCreated {
			t.Fatalf("register %s: %d %s", username, status, data)
		}
		status, data := send(direct, http.MethodPost, "/login", "", "application/json", bytes.NewReader(credentials))
		var login account
		if status != http.StatusOK || json.Unmarshal(data, &login) != nil || login.Token == "" {
			t.Fatalf("login %s: %d %s", username, status, data)
		}
		return login
	}
	author, viewer := signUp("timeline_author"), signUp("timeline_viewer")
	// Several videos share a second, and 9, 10 and 11 share one: "9" > "10" as text, so only
	// zero-padded members keep Redis in the same order as MySQL.
	base := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	for id, offset := range map[uint64]int{1: 0, 2: 1, 3: 1, 4: 2, 9: 3, 10: 3, 11: 3, 5: 4, 6: 5} {
		if _, err := pool.ExecContext(ctx, "INSERT INTO videos (id, author_id, title, play_url, cover_url, created_at) VALUES (?, ?, ?, '/media/videos/t.mp4', '/cover.svg', ?)",
			id, author.ID, fmt.Sprintf("timeline video %d", id), base.Add(time.Duration(offset)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for _, like := range [][2]uint64{{viewer.ID, 10}, {author.ID, 10}, {author.ID, 5}} {
		if _, err := pool.ExecContext(ctx, "INSERT INTO video_likes (user_id, video_id) VALUES (?, ?)", like[0], like[1]); err != nil {
			t.Fatal(err)
		}
	}
	const timelineKey = "gostream:feed:latest:v1:ids"

	// walk follows next_cursor with two videos per page and returns every page body and the
	// number of videos-table queries it caused.
	walk := func(server *httptest.Server, token string) ([]string, int64) {
		t.Helper()
		before := videoQueries.Load()
		var pages []string
		cursor := ""
		for range 20 {
			path := "/feed?limit=2"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			status, data := send(server, http.MethodGet, path, token, "", nil)
			if status != http.StatusOK {
				t.Fatalf("GET %s: %d %s", path, status, data)
			}
			pages = append(pages, string(data))
			var page struct {
				NextCursor string `json:"next_cursor"`
				HasMore    bool   `json:"has_more"`
			}
			if err := json.Unmarshal(data, &page); err != nil {
				t.Fatal(err)
			}
			if !page.HasMore {
				return pages, videoQueries.Load() - before
			}
			cursor = page.NextCursor
		}
		t.Fatal("feed pagination did not finish")
		return nil, 0
	}
	videoIDs := func(pages []string) []uint64 {
		t.Helper()
		var ids []uint64
		for _, body := range pages {
			var page struct {
				Videos []struct {
					ID uint64 `json:"id"`
				} `json:"videos"`
			}
			if err := json.Unmarshal([]byte(body), &page); err != nil {
				t.Fatal(err)
			}
			for _, video := range page.Videos {
				ids = append(ids, video.ID)
			}
		}
		return ids
	}
	compare := func(step string, cachedPages, directPages []string) {
		t.Helper()
		if !slices.Equal(cachedPages, directPages) {
			t.Fatalf("%s: the timeline must return exactly what MySQL returns\ncached: %v\ndirect: %v", step, cachedPages, directPages)
		}
	}

	// 1. The first read builds the timeline with one query; every later page comes from Redis.
	cachedPages, used := walk(cached, viewer.Token)
	if used != 1 {
		t.Fatalf("a full walk through the timeline must query the videos table once (the rebuild), got %d", used)
	}
	directPages, used := walk(direct, viewer.Token)
	if used != int64(len(directPages)) {
		t.Fatalf("without Redis every page queries MySQL: %d queries for %d pages", used, len(directPages))
	}
	compare("viewer", cachedPages, directPages)
	if ids := videoIDs(cachedPages); !slices.Equal(ids, []uint64{6, 5, 11, 10, 9, 4, 3, 2, 1}) {
		t.Fatalf("pages must follow created_at DESC, id DESC across ties: %v", ids)
	}
	if ttl := redisServer.TTL(timelineKey); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("the timeline must expire within a minute, TTL %v", ttl)
	}
	// 2. A guest sees the same cached pages without the viewer's likes: personal state stays out of Redis.
	guestPages, used := walk(cached, "")
	if used != 0 {
		t.Fatalf("a warm timeline must answer every page from Redis, got %d videos queries", used)
	}
	directGuest, _ := walk(direct, "")
	compare("guest", guestPages, directGuest)
	if slices.Equal(guestPages, cachedPages) {
		t.Fatal("the viewer's liked state must differ from the guest's")
	}

	// 3. Publishing invalidates the timeline; the next read rebuilds it with the new video on top.
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	part, err := writer.CreateFormFile("file", "timeline.mp4")
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
	status, data := send(cached, http.MethodPost, "/uploads/videos", author.Token, writer.FormDataContentType(), &form)
	var upload struct {
		PlayURL string `json:"play_url"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &upload) != nil || upload.PlayURL == "" {
		t.Fatalf("upload: %d %s", status, data)
	}
	payload, _ := json.Marshal(map[string]string{"title": "fresh on top", "play_url": upload.PlayURL, "cover_url": "/cover.svg"})
	status, data = send(cached, http.MethodPost, "/videos", author.Token, "application/json", bytes.NewReader(payload))
	var published struct {
		ID uint64 `json:"id"`
	}
	if status != http.StatusCreated || json.Unmarshal(data, &published) != nil || published.ID == 0 {
		t.Fatalf("publish: %d %s", status, data)
	}
	if redisServer.Exists(timelineKey) {
		t.Fatal("publishing must invalidate the timeline")
	}
	firstPage := func(step string, wantFirst uint64) {
		t.Helper()
		before := videoQueries.Load()
		_, cachedFirst := send(cached, http.MethodGet, "/feed?limit=2", "", "", nil)
		if used := videoQueries.Load() - before; used != 1 {
			t.Fatalf("%s: the next read must rebuild the timeline with one query, got %d", step, used)
		}
		_, directFirst := send(direct, http.MethodGet, "/feed?limit=2", "", "", nil)
		compare(step, []string{string(cachedFirst)}, []string{string(directFirst)})
		if ids := videoIDs([]string{string(cachedFirst)}); len(ids) == 0 || ids[0] != wantFirst {
			t.Fatalf("%s: first video %v, want %d", step, ids, wantFirst)
		}
	}
	firstPage("after publish", published.ID)

	// 4. Deleting invalidates it again, so the deleted video is gone from the next read.
	if status, data := send(cached, http.MethodDelete, fmt.Sprintf("/videos/%d", published.ID), author.Token, "", nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, data)
	}
	if redisServer.Exists(timelineKey) {
		t.Fatal("deleting must invalidate the timeline")
	}
	firstPage("after delete", 6)

	// 5. While Redis is down every page falls back to MySQL and still matches.
	redisServer.Close()
	outagePages, used := walk(cached, viewer.Token)
	if used != int64(len(outagePages)) {
		t.Fatalf("during the outage every page must fall back to MySQL: %d queries for %d pages", used, len(outagePages))
	}
	compare("redis outage", outagePages, directPages)
}
