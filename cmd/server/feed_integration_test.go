package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gostream/internal/auth"
	"gostream/internal/database"
	"gostream/internal/feed"
)

func TestFeedHTTPMySQLIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_INTEGRATION_TEST") != "1" {
		t.Skip("set GOSTREAM_INTEGRATION_TEST=1 for local MySQL")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read local database config")
	}
	pool, err := database.OpenDB(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("cannot connect to configured MySQL")
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, ddl := range []string{
		"CREATE TEMPORARY TABLE users(id BIGINT UNSIGNED PRIMARY KEY, username VARCHAR(32), password_hash VARCHAR(255))",
		"CREATE TEMPORARY TABLE video_likes(user_id BIGINT UNSIGNED, video_id BIGINT UNSIGNED, PRIMARY KEY(user_id,video_id))",
	} {
		if _, err := conn.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		for _, table := range []string{"video_likes", "users"} {
			if _, err := conn.ExecContext(cleanup, "DROP TEMPORARY TABLE IF EXISTS "+table); err != nil {
				t.Error(err)
			}
		}
	}()
	if _, err := conn.ExecContext(ctx, "INSERT INTO users VALUES (1,'发布者','private-hash'),(7,'A','private-hash'),(8,'B','private-hash')"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO video_likes VALUES (7,304),(8,304),(7,303)"); err != nil {
		t.Fatal(err)
	}
	// Temporary table is private to this connection; real videos stay untouched.
	_, err = conn.ExecContext(ctx, `CREATE TEMPORARY TABLE videos (
		id BIGINT UNSIGNED PRIMARY KEY, author_id BIGINT UNSIGNED NOT NULL,
		title VARCHAR(100) NOT NULL, play_url VARCHAR(1024) NOT NULL,
		cover_url VARCHAR(1024) NOT NULL, created_at TIMESTAMP NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if _, err := conn.ExecContext(cleanupCtx, "DROP TEMPORARY TABLE IF EXISTS videos"); err != nil {
			t.Error(err)
		}
	}()
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}), &gorm.Config{
		DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	var queryCount atomic.Int64
	db.Callback().Query().After("gorm:query").Register("test:count-query", func(*gorm.DB) { queryCount.Add(1) })
	db.Callback().Row().After("gorm:row").Register("test:count-row", func(*gorm.DB) { queryCount.Add(1) })
	stamp := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	insert := func(id int, at time.Time) {
		t.Helper()
		_, err := conn.ExecContext(ctx, `INSERT INTO videos VALUES (?,1,?,?,?,?)`, id, "HTTP feed fixture", "/media/videos/fixture.mp4", "/cover.svg", at)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int{301, 302, 303} {
		insert(id, stamp)
	}
	insert(304, stamp.Add(time.Minute))
	secret := []byte("test-only-jwt-secret-at-least-32-bytes")
	server := httptest.NewServer(newRouter(db, secret, newTestFeedCursor(t), newTestStorage(t), nil, false, nil))
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	get := func(query string, ids []uint64, more bool, viewer uint64) feed.ListResponse {
		t.Helper()
		beforeQueries := queryCount.Load()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/feed?"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		if viewer > 0 {
			token, err := auth.GenerateToken(viewer, "", secret)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("expected public feed 200, got %d", response.StatusCode)
		}
		if response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("personal state must not be shared in HTTP caches")
		}
		var body feed.ListResponse
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		got := make([]uint64, len(body.Videos))
		for i := range body.Videos {
			got[i] = body.Videos[i].ID
			item := body.Videos[i]
			wantCount := int64(0)
			wantLiked := false
			if item.ID == 304 {
				wantCount = 2
				wantLiked = viewer == 7 || viewer == 8
			}
			if item.ID == 303 {
				wantCount = 1
				wantLiked = viewer == 7
			}
			if item.AuthorName != "发布者" || item.LikeCount != wantCount || item.Liked != wantLiked {
				t.Fatalf("incorrect viewer %d metadata: %+v", viewer, item)
			}
		}
		if !reflect.DeepEqual(got, ids) || body.HasMore != more || (body.NextCursor != "") != more {
			t.Fatalf("unexpected feed IDs %v, has_more %v", got, body.HasMore)
		}
		wantQueries := int64(3)
		if len(ids) == 0 {
			wantQueries = 1
		}
		if queryCount.Load()-beforeQueries != wantQueries {
			t.Fatalf("expected %d batch reads, got %d", wantQueries, queryCount.Load()-beforeQueries)
		}
		return body
	}
	first := get("limit=2&userID=7", []uint64{304, 303}, true, 0)
	get("limit=2&userID=8", []uint64{304, 303}, true, 7)
	get("limit=2", []uint64{304, 303}, true, 8)
	insert(305, stamp.Add(2*time.Minute))
	get("limit=2&cursor="+url.QueryEscape(first.NextCursor), []uint64{302, 301}, false, 0)
	get("limit=2", []uint64{305, 304}, true, 0)
	get("limit=50", []uint64{305, 304, 303, 302, 301}, false, 7)
	older, err := newTestFeedCursor(t).Encode(feed.KindLatest, feed.Position{CreatedAt: stamp.Add(-time.Minute), ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	get("limit=10&cursor="+url.QueryEscape(older), []uint64{}, false, 0)
}
