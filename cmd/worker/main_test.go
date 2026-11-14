package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	amqp "github.com/rabbitmq/amqp091-go"
	"gostream/internal/config"
	"gostream/internal/database"
	"gostream/internal/mq"
	"gostream/internal/notification"
	"gostream/internal/outbox"
)

func TestRunReportsAnUnreachableDatabase(t *testing.T) {
	cfg := config.WorkerConfig{DSN: "user:pass@tcp(127.0.0.1:1)/gostream?timeout=1s", AMQPURL: "amqp://u:p@127.0.0.1:1/"}
	if err := run(context.Background(), cfg, nil); err == nil {
		t.Fatal("the worker must not start without its database")
	}
}

// stubRepository answers every Create with err; the worker never reads notifications.
type stubRepository struct{ err error }

func (r stubRepository) Create(context.Context, notification.Notification) error { return r.err }
func (stubRepository) List(context.Context, uint64, uint64, int) ([]notification.Item, error) {
	return nil, errors.New("unexpected List")
}
func (stubRepository) CountUnread(context.Context, uint64) (int64, error) {
	return 0, errors.New("unexpected CountUnread")
}
func (stubRepository) MarkRead(context.Context, uint64, uint64) error {
	return errors.New("unexpected MarkRead")
}
func (stubRepository) MarkAllRead(context.Context, uint64, uint64) error {
	return errors.New("unexpected MarkAllRead")
}

// TestHandleEvent: an invalid event is rejected into the dead-letter queue, a database error is
// retried, and a stored notification is acknowledged.
func TestHandleEvent(t *testing.T) {
	valid := outbox.Event{ID: 1, Type: outbox.TypeLikeCreated, Payload: []byte(`{"actor_id":2,"author_id":3,"video_id":4}`), CreatedAt: time.Now()}
	unknown := outbox.Event{ID: 1, Type: "video.deleted", Payload: []byte(`{}`), CreatedAt: time.Now()}
	down := errors.New("database unavailable")
	missingUser := fmt.Errorf("%w: user 2 does not exist", notification.ErrInvalidEvent)
	for _, tc := range []struct {
		name       string
		event      outbox.Event
		repoErr    error
		wantErr    error
		wantReject bool
	}{
		{name: "stored", event: valid},
		{name: "invalid_event_is_rejected", event: unknown, wantErr: notification.ErrInvalidEvent, wantReject: true},
		{name: "missing_user_is_rejected", event: valid, repoErr: missingUser, wantErr: notification.ErrInvalidEvent, wantReject: true},
		{name: "database_error_is_retried", event: valid, repoErr: down, wantErr: down},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := handleEvent(notification.NewService(stubRepository{err: tc.repoErr}))(context.Background(), tc.event)
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
			if errors.Is(err, mq.ErrReject) != tc.wantReject {
				t.Fatalf("reject: got %v, want %v (%v)", errors.Is(err, mq.ErrReject), tc.wantReject, err)
			}
		})
	}
}

// TestWorkerTurnsEventsIntoNotifications runs the whole worker against a real MySQL and RabbitMQ:
// while RabbitMQ cannot be reached the events stay in the outbox; once it can, they are sent and
// marked, the like and the comment become notifications that carry the time of the event, the
// event with a broken payload moves to the dead-letter queue, and canceling the context stops the
// worker cleanly. GOSTREAM_TEST_MYSQL_DSN and GOSTREAM_TEST_AMQP_URL must point to disposable
// servers; the test creates and drops its own database and empties the queues.
func TestWorkerTurnsEventsIntoNotifications(t *testing.T) {
	dsn, amqpURL := os.Getenv("GOSTREAM_TEST_MYSQL_DSN"), os.Getenv("GOSTREAM_TEST_AMQP_URL")
	if dsn == "" || amqpURL == "" {
		t.Skip("set GOSTREAM_TEST_MYSQL_DSN and GOSTREAM_TEST_AMQP_URL to disposable servers")
	}
	writer := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(writer) })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := database.OpenDB(dsn)
	if err != nil {
		t.Fatal("cannot connect to the disposable MySQL server")
	}
	t.Cleanup(func() { admin.Close() })
	name := fmt.Sprintf("gostream_worker_%d", time.Now().UnixNano())
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
	dbConfig, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	dbConfig.DBName = name
	dbConfig.ParseTime = true
	workerDSN := dbConfig.FormatDSN()
	pool, err := database.OpenDB(workerDSN)
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
	author := exec("INSERT INTO users (username, password_hash) VALUES ('worker_author', 'x')")
	fan := exec("INSERT INTO users (username, password_hash) VALUES ('worker_fan', 'x')")
	video := exec("INSERT INTO videos (author_id, title, play_url, cover_url) VALUES (?, 'w', '/media/videos/w.mp4', '/cover.svg')", author)
	comment := exec("INSERT INTO video_comments (video_id, user_id, content) VALUES (?, ?, 'nice')", video, fan)
	// Event 1 is a like, 2 a comment, and 3 a like whose payload lacks the author and the video.
	likeID := exec("INSERT INTO outbox_events (event_type, payload) VALUES (?, ?)", outbox.TypeLikeCreated,
		fmt.Sprintf(`{"actor_id":%d,"author_id":%d,"video_id":%d}`, fan, author, video))
	commentID := exec("INSERT INTO outbox_events (event_type, payload) VALUES (?, ?)", outbox.TypeCommentCreated,
		fmt.Sprintf(`{"actor_id":%d,"author_id":%d,"video_id":%d,"comment_id":%d}`, fan, author, video, comment))
	brokenID := exec("INSERT INTO outbox_events (event_type, payload) VALUES (?, ?)", outbox.TypeLikeCreated, fmt.Sprintf(`{"actor_id":%d}`, fan))
	count := func(query string) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	pending := func() int { return count("SELECT COUNT(*) FROM outbox_events WHERE published_at IS NULL") }
	notifications := func() int { return count("SELECT COUNT(*) FROM notifications") }

	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatal("cannot connect to the disposable RabbitMQ")
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	if err := mq.DeclareTopology(ch); err != nil {
		t.Fatal(err)
	}
	for _, queue := range []string{mq.NotificationsQueue, mq.DeadLetterQueue} {
		if _, err := ch.QueuePurge(queue, false); err != nil {
			t.Fatal(err)
		}
	}
	// Short waits keep the test quick.
	quick := func(relay *outbox.Relay, consumer *mq.Consumer) {
		relay.PollInterval = 100 * time.Millisecond
		relay.MaxBackoff = 200 * time.Millisecond
		consumer.MaxBackoff = 200 * time.Millisecond
	}
	start := func(cfg config.WorkerConfig) (context.CancelFunc, chan error) {
		workerCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- run(workerCtx, cfg, quick) }()
		return stop, done
	}
	stopWithin := func(stop context.CancelFunc, done chan error) {
		t.Helper()
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("the worker must stop cleanly, got %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the worker did not stop after its context was canceled")
		}
	}

	// RabbitMQ unreachable: the worker keeps retrying and the events stay in the outbox.
	stop, done := start(config.WorkerConfig{DSN: workerDSN, AMQPURL: "amqp://gostream:wrong@127.0.0.1:1/"})
	time.Sleep(1500 * time.Millisecond)
	stopWithin(stop, done)
	if n := pending(); n != 3 || notifications() != 0 {
		t.Fatalf("with RabbitMQ down every event must stay in the outbox, %d pending, %d notifications", n, notifications())
	}

	// RabbitMQ back: everything is sent, the valid events become notifications and the broken one
	// moves to the dead-letter queue.
	stop, done = start(config.WorkerConfig{DSN: workerDSN, AMQPURL: amqpURL})
	var dead amqp.Delivery
	deadFound := false
	for deadline := time.Now().Add(20 * time.Second); pending() > 0 || notifications() < 2 || !deadFound; time.Sleep(100 * time.Millisecond) {
		if !deadFound {
			message, ok, err := ch.Get(mq.DeadLetterQueue, true)
			if err != nil {
				t.Fatal(err)
			}
			dead, deadFound = message, ok
		}
		if time.Now().After(deadline) {
			t.Fatalf("events were not handled: %d pending, %d notifications, dead letter %v", pending(), notifications(), deadFound)
		}
	}
	stopWithin(stop, done)
	if dead.MessageId != fmt.Sprint(brokenID) {
		t.Fatalf("the broken event %d must be dead-lettered, got %q", brokenID, dead.MessageId)
	}
	if message, ok, err := ch.Get(mq.NotificationsQueue, true); err != nil || ok {
		t.Fatalf("every message must be acknowledged, %q is still queued (%v)", message.MessageId, err)
	}
	// Each notification carries the time its event was written to the outbox.
	for _, tc := range []struct {
		event   uint64
		kind    string
		comment sql.NullInt64
	}{
		{event: likeID, kind: outbox.TypeLikeCreated},
		{event: commentID, kind: outbox.TypeCommentCreated, comment: sql.NullInt64{Int64: int64(comment), Valid: true}},
	} {
		var recipient, actor, videoID uint64
		var kind string
		var commentRef sql.NullInt64
		var notified, happened time.Time
		err := pool.QueryRowContext(ctx, `SELECT n.recipient_id, n.actor_id, n.video_id, n.event_type, n.comment_id, n.created_at, o.created_at
			FROM notifications n JOIN outbox_events o ON o.id = n.event_id WHERE n.event_id = ?`, tc.event).
			Scan(&recipient, &actor, &videoID, &kind, &commentRef, &notified, &happened)
		if err != nil {
			t.Fatalf("event %d: %v", tc.event, err)
		}
		if recipient != author || actor != fan || videoID != video || kind != tc.kind || commentRef != tc.comment || !notified.Equal(happened) {
			t.Fatalf("event %d became %d %d %d %s %v %v (event at %v)", tc.event, recipient, actor, videoID, kind, commentRef, notified, happened)
		}
	}
}
