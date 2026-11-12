package video

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gostream/internal/database"
	"gostream/internal/outbox"
)

// TestOutboxMySQLIntegration runs likes and comments against a real MySQL and checks the
// events they leave in the outbox: one per new like or comment on someone else's video, none
// for duplicates, own videos or missing videos, and none without the like or comment itself.
// It also checks that the 007 migration creates the same table as scripts/schema.sql.
// GOSTREAM_TEST_MYSQL_DSN must point to a disposable server; the test creates and drops its
// own databases.
func TestOutboxMySQLIntegration(t *testing.T) {
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
	// Registered first, so it runs after the cleanups below have dropped their databases.
	t.Cleanup(func() { admin.Close() })
	schema, err := os.ReadFile("../../scripts/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	// newDatabase creates a disposable database with the tables of schema.sql; withOutbox false
	// leaves out the outbox so that the real migration can create it.
	newDatabase := func(label string, withOutbox bool) *sql.DB {
		t.Helper()
		name := fmt.Sprintf("gostream_%s_%d", label, time.Now().UnixNano())
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
		for _, statement := range strings.Split(string(schema), ";") {
			statement = strings.TrimSpace(statement)
			if !strings.HasPrefix(statement, "CREATE TABLE ") || (!withOutbox && strings.Contains(statement, "outbox_events")) {
				continue
			}
			if _, err := pool.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		return pool
	}
	pool := newDatabase("outbox", true)
	migrated := newDatabase("outbox_migration", false)
	migration, err := os.ReadFile("../../scripts/migrations/007_outbox_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("007 migration failed: %v", err)
	}
	showCreate := func(db *sql.DB) string {
		t.Helper()
		var table, definition string
		if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE outbox_events").Scan(&table, &definition); err != nil {
			t.Fatal(err)
		}
		return definition
	}
	if fromSchema, fromMigration := showCreate(pool), showCreate(migrated); fromSchema != fromMigration {
		t.Fatalf("schema.sql and the 007 migration must create the same table\nschema.sql:\n%s\n007:\n%s", fromSchema, fromMigration)
	}

	insert := func(query string, args ...any) uint64 {
		t.Helper()
		result, err := pool.ExecContext(ctx, query, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return uint64(id)
	}
	newUser := func(name string) uint64 {
		return insert("INSERT INTO users (username, password_hash) VALUES (?, 'x')", name)
	}
	author, fan, late := newUser("outbox_author"), newUser("outbox_fan"), newUser("outbox_late")
	video := insert("INSERT INTO videos (author_id, title, play_url, cover_url) VALUES (?, 'outbox', '/media/videos/o.mp4', '/cover.svg')", author)
	const missingVideo uint64 = 999999
	db := database.WrapSQL(pool)
	likes := NewLikeService(&SQLLikeRepository{DB: db})
	comments := NewCommentService(&SQLCommentRepository{DB: db})

	type event struct {
		Type    string
		Payload map[string]uint64
	}
	events := func() []event {
		t.Helper()
		rows, err := pool.QueryContext(ctx, "SELECT event_type, payload, published_at IS NULL FROM outbox_events ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var all []event
		for rows.Next() {
			var e event
			var payload []byte
			var pending bool
			if err := rows.Scan(&e.Type, &payload, &pending); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(payload, &e.Payload); err != nil {
				t.Fatal(err)
			}
			if !pending {
				t.Error("a new event must wait for the relay: published_at must be NULL")
			}
			all = append(all, e)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return all
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	likeEvent := func(actor uint64) event {
		return event{outbox.TypeLikeCreated, map[string]uint64{"actor_id": actor, "author_id": author, "video_id": video}}
	}
	expectEvents := func(step string, want ...event) {
		t.Helper()
		got := events()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s: events %v, want %v", step, got, want)
		}
	}

	// A like on someone else's video leaves exactly one event for the author.
	if err := likes.LikeVideo(ctx, fan, video); err != nil {
		t.Fatal(err)
	}
	expectEvents("first like", likeEvent(fan))
	// Liking again still succeeds for the client, but the duplicate rolls back without an event.
	if err := likes.LikeVideo(ctx, fan, video); err != nil {
		t.Fatalf("a repeated like must still succeed, got %v", err)
	}
	// Liking your own video is kept, but nobody needs to be told.
	if err := likes.LikeVideo(ctx, author, video); err != nil {
		t.Fatal(err)
	}
	if err := likes.LikeVideo(ctx, fan, missingVideo); !errors.Is(err, ErrLikeVideoNotFound) {
		t.Fatalf("liking a missing video must be ErrLikeVideoNotFound, got %v", err)
	}
	if n := count("SELECT COUNT(*) FROM video_likes WHERE video_id = ?", video); n != 2 {
		t.Fatalf("likes from the fan and the author must be stored, got %d", n)
	}
	expectEvents("duplicate, own and missing likes", likeEvent(fan))
	// Unliking leaves no event; liking again is a new like and a new event.
	if err := likes.UnLikeVideo(ctx, fan, video); err != nil {
		t.Fatal(err)
	}
	expectEvents("unlike", likeEvent(fan))
	if err := likes.LikeVideo(ctx, fan, video); err != nil {
		t.Fatal(err)
	}
	expectEvents("like after unlike", likeEvent(fan), likeEvent(fan))

	// Comments follow the same rules and carry the comment ID.
	created, err := comments.CreateComment(ctx, fan, video, "拍得真好")
	if err != nil {
		t.Fatal(err)
	}
	commentEvent := event{outbox.TypeCommentCreated, map[string]uint64{"actor_id": fan, "author_id": author, "video_id": video, "comment_id": created.ID}}
	if _, err := comments.CreateComment(ctx, author, video, "谢谢"); err != nil {
		t.Fatal(err)
	}
	if _, err := comments.CreateComment(ctx, fan, missingVideo, "hello"); !errors.Is(err, ErrCommentVideoNotFound) {
		t.Fatalf("commenting on a missing video must be ErrCommentVideoNotFound, got %v", err)
	}
	expectEvents("comments", likeEvent(fan), likeEvent(fan), commentEvent)

	// Concurrent duplicate likes: one wins, the rest are duplicates, and there is one event.
	rush := newUser("outbox_rush")
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := likes.LikeVideo(ctx, rush, video); err != nil {
				failures <- err
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent repeated likes must all succeed, got %v", err)
	}
	if n := count("SELECT COUNT(*) FROM outbox_events WHERE JSON_EXTRACT(payload, '$.actor_id') = ?", rush); n != 1 {
		t.Fatalf("concurrent repeated likes must leave one event, got %d", n)
	}

	// The event shares the transaction: when it cannot be written, the like and the comment
	// are not saved either.
	if _, err := pool.ExecContext(ctx, "RENAME TABLE outbox_events TO outbox_events_gone"); err != nil {
		t.Fatal(err)
	}
	if err := likes.LikeVideo(ctx, late, video); err == nil || errors.Is(err, ErrLikeVideoNotFound) {
		t.Fatalf("a like whose event cannot be written must fail, got %v", err)
	}
	if _, err := comments.CreateComment(ctx, late, video, "看不到我"); err == nil {
		t.Fatal("a comment whose event cannot be written must fail")
	}
	if n := count("SELECT COUNT(*) FROM video_likes WHERE user_id = ?", late); n != 0 {
		t.Fatalf("the like must roll back with its event, found %d", n)
	}
	if n := count("SELECT COUNT(*) FROM video_comments WHERE user_id = ?", late); n != 0 {
		t.Fatalf("the comment must roll back with its event, found %d", n)
	}
}
