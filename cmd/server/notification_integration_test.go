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

	"github.com/go-sql-driver/mysql"
	"gostream/internal/database"
	"gostream/internal/notification"
	"gostream/internal/outbox"
)

// TestNotificationHTTPMySQLIntegration drives the notification endpoints through the real router
// and MySQL: a fan likes and comments over HTTP, the outbox events become notifications the way
// the worker would turn them, and the author lists, pages, counts and marks them, while the fan
// can neither see nor mark them. GOSTREAM_TEST_MYSQL_DSN must point to a disposable server; the
// test works in its own random database and drops it afterwards.
func TestNotificationHTTPMySQLIntegration(t *testing.T) {
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
	t.Cleanup(func() { admin.Close() })
	name := fmt.Sprintf("gostream_notification_http_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.ExecContext(cleanup, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	})
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
	t.Cleanup(func() { pool.Close() })
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
	server := httptest.NewServer(newRouter(db, secret, newTestFeedCursor(t), newTestStorage(t), nil, false, nil))
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	do := func(method, path, token string, payload any) (int, http.Header, []byte) {
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
		return response.StatusCode, response.Header, data
	}
	type account struct {
		id    uint64
		name  string
		token string
	}
	signUp := func(username string) account {
		t.Helper()
		credentials := map[string]string{"username": username, "password": "notification-test-password"}
		if status, _, body := do(http.MethodPost, "/users", "", credentials); status != http.StatusCreated {
			t.Fatalf("register %s: got %d %s", username, status, body)
		}
		status, _, body := do(http.MethodPost, "/login", "", credentials)
		var login struct {
			ID    uint64 `json:"id"`
			Token string `json:"token"`
		}
		if status != http.StatusOK || json.Unmarshal(body, &login) != nil || login.ID == 0 || login.Token == "" {
			t.Fatalf("login %s: got %d %s", username, status, body)
		}
		return account{id: login.ID, name: username, token: login.Token}
	}
	author, fan := signUp("notify_author"), signUp("notify_fan")
	result, err := pool.ExecContext(ctx, "INSERT INTO videos (author_id, title, play_url, cover_url) VALUES (?, '通知测试视频', '/media/videos/n.mp4', '/cover.svg')", author.id)
	if err != nil {
		t.Fatal(err)
	}
	videoID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	expect := func(want int, method string, who account, path string, payload any) []byte {
		t.Helper()
		status, _, body := do(method, path, who.token, payload)
		if status != want {
			t.Fatalf("%s %s as %s: want %d, got %d %s", method, path, who.name, want, status, body)
		}
		return body
	}
	expect(http.StatusNoContent, http.MethodPut, fan, fmt.Sprintf("/videos/%d/like", videoID), nil)
	expect(http.StatusCreated, http.MethodPost, fan, fmt.Sprintf("/videos/%d/comments", videoID), map[string]string{"content": "拍得真好"})
	expect(http.StatusNoContent, http.MethodDelete, fan, fmt.Sprintf("/videos/%d/like", videoID), nil)
	expect(http.StatusNoContent, http.MethodPut, fan, fmt.Sprintf("/videos/%d/like", videoID), nil)

	// Stand in for the worker: every outbox event goes through the same service it uses.
	rows, err := pool.QueryContext(ctx, "SELECT id, event_type, payload, created_at FROM outbox_events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var events []outbox.Event
	for rows.Next() {
		var e outbox.Event
		if err := rows.Scan(&e.ID, &e.Type, &e.Payload, &e.CreatedAt); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	rows.Close()
	if len(events) != 3 {
		t.Fatalf("a like, a comment and a second like must leave three events, got %d", len(events))
	}
	service := notification.NewService(&notification.GORMRepository{DB: db})
	for _, e := range events {
		if err := service.HandleEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	type page struct {
		Notifications []struct {
			ID             uint64  `json:"id"`
			Type           string  `json:"type"`
			ActorID        uint64  `json:"actor_id"`
			ActorName      string  `json:"actor_name"`
			VideoID        uint64  `json:"video_id"`
			VideoTitle     string  `json:"video_title"`
			CommentID      *uint64 `json:"comment_id"`
			CommentContent *string `json:"comment_content"`
			CreatedAt      string  `json:"created_at"`
			Read           bool    `json:"read"`
		} `json:"notifications"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	}
	listAs := func(who account, query string) page {
		t.Helper()
		status, header, body := do(http.MethodGet, "/notifications"+query, who.token, nil)
		var p page
		if status != http.StatusOK || json.Unmarshal(body, &p) != nil || p.Notifications == nil {
			t.Fatalf("list as %s: got %d %s", who.name, status, body)
		}
		if header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("a personal list must not be cached, got %q", header.Get("Cache-Control"))
		}
		return p
	}
	unread := func(who account) int {
		t.Helper()
		var result struct {
			Count *int `json:"count"`
		}
		if err := json.Unmarshal(expect(http.StatusOK, http.MethodGet, who, "/notifications/unread-count", nil), &result); err != nil || result.Count == nil {
			t.Fatalf("unread count as %s: %v", who.name, err)
		}
		return *result.Count
	}

	// The author pages through three notifications, newest first: like, comment, like.
	first := listAs(author, "?limit=2")
	if len(first.Notifications) != 2 || !first.HasMore || first.NextCursor != fmt.Sprint(first.Notifications[1].ID) {
		t.Fatalf("first page: %+v", first)
	}
	second := listAs(author, "?limit=2&cursor="+first.NextCursor)
	if len(second.Notifications) != 1 || second.HasMore || second.NextCursor != "" {
		t.Fatalf("second page: %+v", second)
	}
	all := append(first.Notifications, second.Notifications...)
	wantTypes := []string{outbox.TypeLikeCreated, outbox.TypeCommentCreated, outbox.TypeLikeCreated}
	for i, n := range all {
		if n.Type != wantTypes[i] || n.ActorID != fan.id || n.ActorName != fan.name || n.VideoID != uint64(videoID) ||
			n.VideoTitle != "通知测试视频" || n.Read || n.CreatedAt == "" {
			t.Fatalf("notification %d: %+v", i, n)
		}
		if i > 0 && n.ID >= all[i-1].ID {
			t.Fatalf("notifications must come newest first, got %d after %d", n.ID, all[i-1].ID)
		}
	}
	if comment := all[1]; comment.CommentID == nil || comment.CommentContent == nil || *comment.CommentContent != "拍得真好" {
		t.Fatalf("the comment notification must carry the comment: %+v", comment)
	}
	if like := all[0]; like.CommentID != nil || like.CommentContent != nil {
		t.Fatalf("a like has no comment: %+v", like)
	}
	if unread(author) != 3 {
		t.Fatal("the author must have three unread notifications")
	}

	// The fan has none, cannot read the author's, and gets 404 for the author's IDs: exactly what a
	// missing ID gets, so no one can probe which IDs exist.
	if p := listAs(fan, ""); len(p.Notifications) != 0 || p.HasMore || unread(fan) != 0 {
		t.Fatalf("the fan must have no notifications: %+v", p)
	}
	newest, comment, oldest := all[0].ID, all[1].ID, all[2].ID
	expect(http.StatusNotFound, http.MethodPut, fan, fmt.Sprintf("/notifications/%d/read", comment), nil)
	expect(http.StatusNotFound, http.MethodPut, author, "/notifications/999999/read", nil)
	expect(http.StatusNoContent, http.MethodPut, fan, fmt.Sprintf("/notifications/read?up_to=%d", newest), nil)
	if unread(author) != 3 {
		t.Fatal("nothing the fan does may change the author's notifications")
	}

	// Marking one read is idempotent.
	expect(http.StatusNoContent, http.MethodPut, author, fmt.Sprintf("/notifications/%d/read", comment), nil)
	expect(http.StatusNoContent, http.MethodPut, author, fmt.Sprintf("/notifications/%d/read", comment), nil)
	if unread(author) != 2 {
		t.Fatal("one notification read must leave two unread")
	}
	// Mark all read up to the comment: the newer like stays unread.
	expect(http.StatusNoContent, http.MethodPut, author, fmt.Sprintf("/notifications/read?up_to=%d", comment), nil)
	readState := map[uint64]bool{}
	for _, n := range listAs(author, "").Notifications {
		readState[n.ID] = n.Read
	}
	if readState[newest] || !readState[comment] || !readState[oldest] || unread(author) != 1 {
		t.Fatalf("up_to must leave newer notifications unread: %v", readState)
	}
	expect(http.StatusNoContent, http.MethodPut, author, fmt.Sprintf("/notifications/read?up_to=%d", newest), nil)
	if unread(author) != 0 {
		t.Fatal("everything must be read now")
	}
	// Bad input is rejected before the database.
	expect(http.StatusBadRequest, http.MethodGet, author, "/notifications?cursor=abc", nil)
	expect(http.StatusBadRequest, http.MethodGet, author, "/notifications?limit=51", nil)
	expect(http.StatusBadRequest, http.MethodPut, author, "/notifications/abc/read", nil)
	expect(http.StatusBadRequest, http.MethodPut, author, "/notifications/read", nil)
}
