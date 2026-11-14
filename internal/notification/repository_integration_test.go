package notification

import (
	"context"
	"database/sql"
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

// TestNotificationMySQLIntegration runs the service and repository against a real MySQL: a like
// and a comment become notifications carrying the event time, an event delivered again (even
// eight times at once) leaves one row, an event for a deleted video leaves none, deleting a video
// removes its notifications, deleting a comment keeps them, and an event naming a missing user is
// invalid. It also checks that the 008 migration creates the same table as scripts/schema.sql.
// GOSTREAM_TEST_MYSQL_DSN must point to a disposable server; the test creates and drops its own
// databases.
func TestNotificationMySQLIntegration(t *testing.T) {
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
	// newDatabase creates a disposable database with the tables of schema.sql; withNotifications
	// false leaves the notifications table out so that the real migration can create it.
	newDatabase := func(label string, withNotifications bool) *sql.DB {
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
			if !strings.HasPrefix(statement, "CREATE TABLE ") || (!withNotifications && strings.Contains(statement, "notifications")) {
				continue
			}
			if _, err := pool.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		return pool
	}
	pool := newDatabase("notifications", true)
	migrated := newDatabase("notifications_migration", false)
	migration, err := os.ReadFile("../../scripts/migrations/008_notifications.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("008 migration failed: %v", err)
	}
	showCreate := func(db *sql.DB) string {
		t.Helper()
		var table, definition string
		if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE notifications").Scan(&table, &definition); err != nil {
			t.Fatal(err)
		}
		return definition
	}
	if fromSchema, fromMigration := showCreate(pool), showCreate(migrated); fromSchema != fromMigration {
		t.Fatalf("schema.sql and the 008 migration must create the same table\nschema.sql:\n%s\n008:\n%s", fromSchema, fromMigration)
	}

	exec := func(query string, args ...any) uint64 {
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
		return exec("INSERT INTO users (username, password_hash) VALUES (?, 'x')", name)
	}
	newVideo := func(author uint64) uint64 {
		return exec("INSERT INTO videos (author_id, title, play_url, cover_url) VALUES (?, 'n', '/media/videos/n.mp4', '/cover.svg')", author)
	}
	author, fan := newUser("notification_author"), newUser("notification_fan")
	video := newVideo(author)
	comment := exec("INSERT INTO video_comments (video_id, user_id, content) VALUES (?, ?, 'nice')", video, fan)
	service := NewService(&GORMRepository{DB: database.WrapSQL(pool)})
	like := func(id, actor, recipient, video uint64) outbox.Event {
		return outbox.Event{ID: id, Type: outbox.TypeLikeCreated, CreatedAt: eventTime,
			Payload: fmt.Appendf(nil, `{"actor_id":%d,"author_id":%d,"video_id":%d}`, actor, recipient, video)}
	}

	type row struct {
		Recipient, Actor, Video uint64
		Type                    string
		Comment                 sql.NullInt64
		Created                 time.Time
		Read                    sql.NullTime
	}
	rowFor := func(event uint64) (row, bool) {
		t.Helper()
		var r row
		err := pool.QueryRowContext(ctx, `SELECT recipient_id, actor_id, video_id, event_type, comment_id, created_at, read_at
			FROM notifications WHERE event_id = ?`, event).Scan(&r.Recipient, &r.Actor, &r.Video, &r.Type, &r.Comment, &r.Created, &r.Read)
		if errors.Is(err, sql.ErrNoRows) {
			return row{}, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return r, true
	}
	count := func(event uint64) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE event_id = ?", event).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// A like and a comment become unread notifications for the author, with the event time.
	if err := service.HandleEvent(ctx, like(101, fan, author, video)); err != nil {
		t.Fatal(err)
	}
	commented := outbox.Event{ID: 102, Type: outbox.TypeCommentCreated, CreatedAt: eventTime,
		Payload: fmt.Appendf(nil, `{"actor_id":%d,"author_id":%d,"video_id":%d,"comment_id":%d}`, fan, author, video, comment)}
	if err := service.HandleEvent(ctx, commented); err != nil {
		t.Fatal(err)
	}
	if r, ok := rowFor(101); !ok || r.Recipient != author || r.Actor != fan || r.Video != video || r.Type != outbox.TypeLikeCreated ||
		r.Comment.Valid || !r.Created.Equal(eventTime) || r.Read.Valid {
		t.Fatalf("like notification: %+v, %v", r, ok)
	}
	if r, ok := rowFor(102); !ok || r.Type != outbox.TypeCommentCreated || !r.Comment.Valid || uint64(r.Comment.Int64) != comment ||
		!r.Created.Equal(eventTime) || r.Read.Valid {
		t.Fatalf("comment notification: %+v, %v", r, ok)
	}

	// The same event delivered again leaves one row, also when the copies arrive at once.
	if err := service.HandleEvent(ctx, like(101, fan, author, video)); err != nil || count(101) != 1 {
		t.Fatalf("a repeated event must be accepted without a second row: %v, %d rows", err, count(101))
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- service.HandleEvent(ctx, like(103, fan, author, video)) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("every concurrent copy must succeed: %v", err)
		}
	}
	if n := count(103); n != 1 {
		t.Fatalf("eight concurrent copies must leave one notification, got %d", n)
	}

	// A comment deleted later keeps its notification.
	exec("DELETE FROM video_comments WHERE id = ?", comment)
	if _, ok := rowFor(102); !ok {
		t.Fatal("deleting a comment must not remove its notification")
	}

	// An event for a video that is already gone has nothing to notify; deleting a video later
	// removes its notifications with it.
	gone := newVideo(author)
	exec("DELETE FROM videos WHERE id = ?", gone)
	if err := service.HandleEvent(ctx, like(104, fan, author, gone)); err != nil || count(104) != 0 {
		t.Fatalf("an event for a deleted video must be accepted without a row: %v, %d rows", err, count(104))
	}
	later := newVideo(author)
	if err := service.HandleEvent(ctx, like(105, fan, author, later)); err != nil || count(105) != 1 {
		t.Fatalf("like on a new video: %v, %d rows", err, count(105))
	}
	exec("DELETE FROM videos WHERE id = ?", later)
	if count(105) != 0 {
		t.Fatal("deleting a video must remove its notifications")
	}

	// A user that does not exist makes the event invalid instead of failing forever.
	const missing uint64 = 999999
	for _, event := range []outbox.Event{like(106, missing, author, video), like(107, fan, missing, video)} {
		if err := service.HandleEvent(ctx, event); !errors.Is(err, ErrInvalidEvent) || count(event.ID) != 0 {
			t.Fatalf("event %d names a missing user: %v, %d rows", event.ID, err, count(event.ID))
		}
	}

	// An unavailable database is a temporary problem, not an invalid event.
	closed, err := database.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	err = NewService(&GORMRepository{DB: database.WrapSQL(closed)}).HandleEvent(ctx, like(108, fan, author, video))
	if err == nil || errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("an unavailable database must be reported as retryable, got %v", err)
	}
}
