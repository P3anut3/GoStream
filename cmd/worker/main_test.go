package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"
	"gostream/internal/config"
	"gostream/internal/database"
	"gostream/internal/mq"
	"gostream/internal/outbox"
)

func TestRunReportsAnUnreachableDatabase(t *testing.T) {
	cfg := config.WorkerConfig{DSN: "user:pass@tcp(127.0.0.1:1)/gostream?timeout=1s", AMQPURL: "amqp://u:p@127.0.0.1:1/"}
	if err := run(context.Background(), cfg, outbox.NewRelay); err == nil {
		t.Fatal("the worker must not start without its database")
	}
}

// TestWorkerDeliversOutboxEvents runs the whole worker against a real MySQL and RabbitMQ: while
// RabbitMQ cannot be reached the events stay in the outbox, once it can they arrive in the
// notifications queue and are marked, and canceling the context stops the worker cleanly.
// GOSTREAM_TEST_MYSQL_DSN and GOSTREAM_TEST_AMQP_URL must point to disposable servers.
func TestWorkerDeliversOutboxEvents(t *testing.T) {
	dsn, amqpURL := os.Getenv("GOSTREAM_TEST_MYSQL_DSN"), os.Getenv("GOSTREAM_TEST_AMQP_URL")
	if dsn == "" || amqpURL == "" {
		t.Skip("set GOSTREAM_TEST_MYSQL_DSN and GOSTREAM_TEST_AMQP_URL to disposable servers")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	migration, err := os.ReadFile("../../scripts/migrations/007_outbox_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	for i, eventType := range []string{outbox.TypeLikeCreated, outbox.TypeCommentCreated, outbox.TypeLikeCreated} {
		if _, err := pool.ExecContext(ctx, "INSERT INTO outbox_events (event_type, payload) VALUES (?, ?)", eventType, fmt.Sprintf(`{"actor_id": %d}`, i+1)); err != nil {
			t.Fatal(err)
		}
	}
	pending := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox_events WHERE published_at IS NULL").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
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
	if _, err := ch.QueuePurge(mq.NotificationsQueue, false); err != nil {
		t.Fatal(err)
	}
	// A short poll interval and backoff keep the test quick.
	quick := func(db *gorm.DB, publisher outbox.Publisher) *outbox.Relay {
		relay := outbox.NewRelay(db, publisher)
		relay.PollInterval = 100 * time.Millisecond
		relay.MaxBackoff = 200 * time.Millisecond
		return relay
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
	if n := pending(); n != 3 {
		t.Fatalf("with RabbitMQ down every event must stay in the outbox, %d pending", n)
	}

	// RabbitMQ back: everything is delivered and marked.
	stop, done = start(config.WorkerConfig{DSN: workerDSN, AMQPURL: amqpURL})
	for deadline := time.Now().Add(15 * time.Second); pending() > 0; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("events were not delivered, %d pending", pending())
		}
	}
	stopWithin(stop, done)
	seen := map[string]bool{}
	for range 3 {
		message, ok, err := ch.Get(mq.NotificationsQueue, true)
		if err != nil || !ok {
			t.Fatalf("a delivered event is missing from the queue: %v", err)
		}
		seen[message.MessageId] = true
	}
	for id := 1; id <= 3; id++ {
		if !seen[strconv.Itoa(id)] {
			t.Fatalf("event %d did not arrive, got %v", id, seen)
		}
	}
}
