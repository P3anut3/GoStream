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

// testDatabases creates disposable databases on the server GOSTREAM_TEST_MYSQL_DSN points to and
// drops them when the test ends; it skips the test without that variable.
type testDatabases struct {
	t      *testing.T
	ctx    context.Context
	dsn    string
	admin  *sql.DB
	schema string
}

func newTestDatabases(t *testing.T, ctx context.Context) *testDatabases {
	t.Helper()
	dsn := os.Getenv("GOSTREAM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set GOSTREAM_TEST_MYSQL_DSN to a disposable MySQL server")
	}
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
	return &testDatabases{t: t, ctx: ctx, dsn: dsn, admin: admin, schema: string(schema)}
}

// create makes a database with the tables of schema.sql; withNotifications false leaves the
// notifications table out so that the real migrations can create it.
func (d *testDatabases) create(label string, withNotifications bool) *sql.DB {
	t := d.t
	t.Helper()
	name := fmt.Sprintf("gostream_%s_%d", label, time.Now().UnixNano())
	if _, err := d.admin.ExecContext(d.ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := d.admin.ExecContext(cleanup, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	})
	config, err := mysql.ParseDSN(d.dsn)
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
	for _, statement := range strings.Split(d.schema, ";") {
		statement = strings.TrimSpace(statement)
		if !strings.HasPrefix(statement, "CREATE TABLE ") || (!withNotifications && strings.Contains(statement, "notifications")) {
			continue
		}
		if _, err := pool.ExecContext(d.ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

// fixtures inserts rows into a test database and returns their IDs.
type fixtures struct {
	t    *testing.T
	ctx  context.Context
	pool *sql.DB
}

func (f fixtures) exec(query string, args ...any) uint64 {
	f.t.Helper()
	result, err := f.pool.ExecContext(f.ctx, query, args...)
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		f.t.Fatal(err)
	}
	return uint64(id)
}

func (f fixtures) user(name string) uint64 {
	return f.exec("INSERT INTO users (username, password_hash) VALUES (?, 'x')", name)
}

func (f fixtures) video(author uint64, title string) uint64 {
	return f.exec("INSERT INTO videos (author_id, title, play_url, cover_url) VALUES (?, ?, '/media/videos/n.mp4', '/cover.svg')", author, title)
}

func likeOf(id, actor, recipient, video uint64) outbox.Event {
	return outbox.Event{ID: id, Type: outbox.TypeLikeCreated, CreatedAt: eventTime,
		Payload: fmt.Appendf(nil, `{"actor_id":%d,"author_id":%d,"video_id":%d}`, actor, recipient, video)}
}

func commentOf(id, actor, recipient, video, comment uint64) outbox.Event {
	return outbox.Event{ID: id, Type: outbox.TypeCommentCreated, CreatedAt: eventTime,
		Payload: fmt.Appendf(nil, `{"actor_id":%d,"author_id":%d,"video_id":%d,"comment_id":%d}`, actor, recipient, video, comment)}
}

// TestNotificationMySQLIntegration runs the service and repository against a real MySQL: a like
// and a comment become notifications carrying the event time, an event delivered again (even
// eight times at once) leaves one row, an event for a deleted video leaves none, deleting a video
// removes its notifications, deleting a comment keeps them, and an event naming a missing user is
// invalid. It also checks that the 008 and 009 migrations create the same table as
// scripts/schema.sql.
func TestNotificationMySQLIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	databases := newTestDatabases(t, ctx)
	pool := databases.create("notifications", true)
	migrated := databases.create("notifications_migration", false)
	for _, file := range []string{"008_notifications.sql", "009_notifications_unread_index.sql"} {
		migration, err := os.ReadFile("../../scripts/migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := migrated.ExecContext(ctx, string(migration)); err != nil {
			t.Fatalf("migration %s failed: %v", file, err)
		}
	}
	showCreate := func(db *sql.DB) string {
		t.Helper()
		var table, definition string
		if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE notifications").Scan(&table, &definition); err != nil {
			t.Fatal(err)
		}
		return definition
	}
	if fromSchema, fromMigrations := showCreate(pool), showCreate(migrated); fromSchema != fromMigrations {
		t.Fatalf("schema.sql and the 008 and 009 migrations must create the same table\nschema.sql:\n%s\nmigrations:\n%s", fromSchema, fromMigrations)
	}

	f := fixtures{t: t, ctx: ctx, pool: pool}
	author, fan := f.user("notification_author"), f.user("notification_fan")
	video := f.video(author, "n")
	comment := f.exec("INSERT INTO video_comments (video_id, user_id, content) VALUES (?, ?, 'nice')", video, fan)
	service := NewService(&GORMRepository{DB: database.WrapSQL(pool)})

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
	if err := service.HandleEvent(ctx, likeOf(101, fan, author, video)); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleEvent(ctx, commentOf(102, fan, author, video, comment)); err != nil {
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
	if err := service.HandleEvent(ctx, likeOf(101, fan, author, video)); err != nil || count(101) != 1 {
		t.Fatalf("a repeated event must be accepted without a second row: %v, %d rows", err, count(101))
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- service.HandleEvent(ctx, likeOf(103, fan, author, video)) })
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
	f.exec("DELETE FROM video_comments WHERE id = ?", comment)
	if _, ok := rowFor(102); !ok {
		t.Fatal("deleting a comment must not remove its notification")
	}

	// An event for a video that is already gone has nothing to notify; deleting a video later
	// removes its notifications with it.
	gone := f.video(author, "gone")
	f.exec("DELETE FROM videos WHERE id = ?", gone)
	if err := service.HandleEvent(ctx, likeOf(104, fan, author, gone)); err != nil || count(104) != 0 {
		t.Fatalf("an event for a deleted video must be accepted without a row: %v, %d rows", err, count(104))
	}
	later := f.video(author, "later")
	if err := service.HandleEvent(ctx, likeOf(105, fan, author, later)); err != nil || count(105) != 1 {
		t.Fatalf("like on a new video: %v, %d rows", err, count(105))
	}
	f.exec("DELETE FROM videos WHERE id = ?", later)
	if count(105) != 0 {
		t.Fatal("deleting a video must remove its notifications")
	}

	// A user that does not exist makes the event invalid instead of failing forever.
	const missing uint64 = 999999
	for _, event := range []outbox.Event{likeOf(106, missing, author, video), likeOf(107, fan, missing, video)} {
		if err := service.HandleEvent(ctx, event); !errors.Is(err, ErrInvalidEvent) || count(event.ID) != 0 {
			t.Fatalf("event %d names a missing user: %v, %d rows", event.ID, err, count(event.ID))
		}
	}

	// An unavailable database is a temporary problem, not an invalid event.
	closed, err := database.OpenDB(databases.dsn)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	err = NewService(&GORMRepository{DB: database.WrapSQL(closed)}).HandleEvent(ctx, likeOf(108, fan, author, video))
	if err == nil || errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("an unavailable database must be reported as retryable, got %v", err)
	}
}

// TestNotificationListMySQLIntegration reads and marks notifications in a real MySQL: pages come
// newest first with the actor's name, the video title and the comment (gone once the comment is
// deleted), each user sees and counts only their own, marking one keeps the time it was first
// read, someone else's or a missing notification is not found, marking all stops at up_to, and
// the unread count reads only the 009 index.
func TestNotificationListMySQLIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newTestDatabases(t, ctx).create("notification_list", true)
	f := fixtures{t: t, ctx: ctx, pool: pool}
	author, fan, other := f.user("list_author"), f.user("list_fan"), f.user("list_other")
	first, second := f.video(author, "第一个视频"), f.video(author, "第二个视频")
	elsewhere := f.video(other, "别人的视频")
	kept := f.exec("INSERT INTO video_comments (video_id, user_id, content) VALUES (?, ?, '拍得真好')", first, fan)
	deleted := f.exec("INSERT INTO video_comments (video_id, user_id, content) VALUES (?, ?, '删掉的评论')", second, fan)
	repo := &GORMRepository{DB: database.WrapSQL(pool)}
	service := NewService(repo)
	for _, event := range []outbox.Event{
		likeOf(1, fan, author, first),
		commentOf(2, fan, author, first, kept),
		likeOf(3, fan, author, second),
		commentOf(4, fan, author, second, deleted),
		likeOf(5, author, other, elsewhere), // the other user's only notification
	} {
		if err := service.HandleEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	f.exec("DELETE FROM video_comments WHERE id = ?", deleted)
	idOf := func(event uint64) uint64 {
		t.Helper()
		var id uint64
		if err := pool.QueryRowContext(ctx, "SELECT id FROM notifications WHERE event_id = ?", event).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	page, err := service.List(ctx, author, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Notifications) != 3 || !page.HasMore || page.NextCursor != fmt.Sprint(idOf(2)) {
		t.Fatalf("first page: %+v", page)
	}
	deletedComment, likeOfSecond, keptComment := page.Notifications[0], page.Notifications[1], page.Notifications[2]
	if deletedComment.ID != idOf(4) || deletedComment.Type != outbox.TypeCommentCreated || deletedComment.ActorName != "list_fan" ||
		deletedComment.VideoTitle != "第二个视频" || deletedComment.CommentID == nil || *deletedComment.CommentID != deleted ||
		deletedComment.CommentContent != nil || deletedComment.Read || !deletedComment.CreatedAt.Equal(eventTime) {
		t.Fatalf("comment that was deleted later: %+v", deletedComment)
	}
	if likeOfSecond.ID != idOf(3) || likeOfSecond.Type != outbox.TypeLikeCreated || likeOfSecond.CommentID != nil || likeOfSecond.CommentContent != nil {
		t.Fatalf("like: %+v", likeOfSecond)
	}
	if keptComment.ID != idOf(2) || keptComment.CommentContent == nil || *keptComment.CommentContent != "拍得真好" || keptComment.VideoTitle != "第一个视频" {
		t.Fatalf("comment: %+v", keptComment)
	}
	rest, err := service.List(ctx, author, 3, page.NextCursor)
	if err != nil || len(rest.Notifications) != 1 || rest.Notifications[0].ID != idOf(1) || rest.HasMore || rest.NextCursor != "" {
		t.Fatalf("second page: %+v, %v", rest, err)
	}
	theirs, err := service.List(ctx, other, 0, "")
	if err != nil || len(theirs.Notifications) != 1 || theirs.Notifications[0].ID != idOf(5) {
		t.Fatalf("the other user sees only their own: %+v, %v", theirs, err)
	}
	unread := func(user uint64) int64 {
		t.Helper()
		count, err := service.UnreadCount(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		return count
	}
	if unread(author) != 4 || unread(other) != 1 || unread(fan) != 0 {
		t.Fatalf("unread: author %d, other %d, fan %d", unread(author), unread(other), unread(fan))
	}

	// Marking one is idempotent and keeps the first read time.
	readAt := func(id uint64) sql.NullTime {
		t.Helper()
		var at sql.NullTime
		if err := pool.QueryRowContext(ctx, "SELECT read_at FROM notifications WHERE id = ?", id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	if err := service.MarkRead(ctx, author, idOf(1)); err != nil {
		t.Fatal(err)
	}
	// Move the read time an hour back, so that a second NOW() would be visibly different.
	f.exec("UPDATE notifications SET read_at = read_at - INTERVAL 1 HOUR WHERE id = ?", idOf(1))
	firstRead := readAt(idOf(1))
	if err := service.MarkRead(ctx, author, idOf(1)); err != nil {
		t.Fatalf("marking again must succeed: %v", err)
	}
	if again := readAt(idOf(1)); !firstRead.Valid || !again.Time.Equal(firstRead.Time) {
		t.Fatalf("marking again must keep the first read time: %v then %v", firstRead, again)
	}
	// Someone else's notification and a missing one are both not found, and stay as they were.
	if err := service.MarkRead(ctx, author, idOf(5)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's notification: %v", err)
	}
	if err := service.MarkRead(ctx, author, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing notification: %v", err)
	}
	if unread(other) != 1 || unread(author) != 3 {
		t.Fatalf("unread after marking: author %d, other %d", unread(author), unread(other))
	}

	// Marking all stops at up_to: the newest stays unread, and other users are not touched.
	if err := service.MarkAllRead(ctx, author, idOf(3)); err != nil {
		t.Fatal(err)
	}
	if readAt(idOf(4)).Valid || !readAt(idOf(3)).Valid || !readAt(idOf(2)).Valid || unread(author) != 1 || unread(other) != 1 {
		t.Fatalf("mark all up to %d: author %d unread, other %d unread", idOf(3), unread(author), unread(other))
	}
	// An up_to past someone else's notification (5) still marks only the author's own.
	if err := service.MarkAllRead(ctx, author, idOf(5)); err != nil || unread(author) != 0 || unread(other) != 1 {
		t.Fatalf("mark all up to %d: %v, author %d unread, other %d unread", idOf(5), err, unread(author), unread(other))
	}

	// With enough rows for the statistics to matter, counting unread notifications reads only the
	// 009 index (Using index: no row lookups).
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO notifications (recipient_id, actor_id, event_id, event_type, video_id, read_at)
		VALUES (?, ?, ?, 'like.created', ?, IF(? % 3 = 0, NULL, NOW()))`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 600; i++ {
		recipient, actor, video := author, fan, first
		if i%2 == 0 {
			recipient, actor, video = other, author, elsewhere
		}
		if _, err := statement.ExecContext(ctx, recipient, actor, 1000+i, video, i); err != nil {
			t.Fatal(err)
		}
	}
	statement.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, "ANALYZE TABLE notifications"); err != nil {
		t.Fatal(err)
	}
	var id, selectType, table, partitions, accessType, possibleKeys, key, keyLength, ref, rows, filtered, extra sql.NullString
	if err := pool.QueryRowContext(ctx, "EXPLAIN SELECT COUNT(*) FROM notifications WHERE recipient_id = ? AND read_at IS NULL", author).
		Scan(&id, &selectType, &table, &partitions, &accessType, &possibleKeys, &key, &keyLength, &ref, &rows, &filtered, &extra); err != nil {
		t.Fatal(err)
	}
	t.Logf("EXPLAIN unread count: type=%s key=%s rows=%s Extra=%s", accessType.String, key.String, rows.String, extra.String)
	if key.String != "idx_notifications_unread" || !strings.Contains(extra.String, "Using index") {
		t.Fatalf("the unread count must read only idx_notifications_unread, got key=%s Extra=%s", key.String, extra.String)
	}
}
