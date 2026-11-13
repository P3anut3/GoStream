package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gostream/internal/database"
)

// blockingPublisher confirms every event, but only after release is closed; started is closed
// as soon as the first batch arrives, while the relay still holds its rows.
type blockingPublisher struct {
	started, release chan struct{}
	got              []uint64
}

func (p *blockingPublisher) Publish(ctx context.Context, events []Event) ([]uint64, error) {
	close(p.started)
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	for _, e := range events {
		p.got = append(p.got, e.ID)
	}
	return p.got, nil
}

// TestRelayMySQLIntegration runs the relay against a real MySQL with the 007 table: batches go
// out in ID order and are marked, only confirmed events are marked, a second relay skips rows the
// first one holds, new events can be inserted while a relay holds its batch, and cleanup removes
// only old published events in rounds of 1000. GOSTREAM_TEST_MYSQL_DSN must point to a disposable
// server; the test creates and drops its own database.
func TestRelayMySQLIntegration(t *testing.T) {
	dsn := os.Getenv("GOSTREAM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set GOSTREAM_TEST_MYSQL_DSN to a disposable MySQL server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := database.OpenDB(dsn)
	if err != nil {
		t.Fatal("cannot connect to the disposable MySQL server")
	}
	t.Cleanup(func() { admin.Close() })
	name := fmt.Sprintf("gostream_relay_%d", time.Now().UnixNano())
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
	migration, err := os.ReadFile("../../scripts/migrations/007_outbox_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	db := database.WrapSQL(pool)

	reset := func() {
		t.Helper()
		if _, err := pool.ExecContext(ctx, "TRUNCATE TABLE outbox_events"); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(n int) {
		t.Helper()
		for i := 1; i <= n; i++ {
			if _, err := pool.ExecContext(ctx, "INSERT INTO outbox_events (event_type, payload) VALUES (?, ?)",
				TypeLikeCreated, fmt.Sprintf(`{"actor_id": %d, "author_id": 1, "video_id": 1}`, i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	pending := func() []uint64 {
		t.Helper()
		rows, err := pool.QueryContext(ctx, "SELECT id FROM outbox_events WHERE published_at IS NULL ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []uint64
		for rows.Next() {
			var id uint64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		return ids
	}

	// 1. A batch goes out in ID order with its stored type, payload and time, and is marked.
	insert(3)
	recorder := &fakePublisher{}
	relay := NewRelay(db, recorder)
	if sent, err := relay.RelayOnce(ctx); sent != 3 || err != nil {
		t.Fatalf("first round: %d, %v", sent, err)
	}
	batch := recorder.calls[0]
	if ids := []uint64{batch[0].ID, batch[1].ID, batch[2].ID}; !slices.Equal(ids, []uint64{1, 2, 3}) {
		t.Fatalf("events must go out in ID order, got %v", ids)
	}
	if batch[0].Type != TypeLikeCreated || !strings.Contains(string(batch[0].Payload), `"actor_id": 1`) || time.Since(batch[0].CreatedAt) > time.Hour {
		t.Fatalf("the stored event must reach the publisher, got %+v", batch[0])
	}
	if ids := pending(); len(ids) != 0 {
		t.Fatalf("confirmed events must be marked, still pending %v", ids)
	}
	if sent, err := relay.RelayOnce(ctx); sent != 0 || err != nil || len(recorder.calls) != 1 {
		t.Fatalf("an empty outbox must publish nothing: %d, %v", sent, err)
	}

	// 2. Only confirmed events are marked; the rest go out in the next round.
	reset()
	insert(3)
	partial := &fakePublisher{decide: func(call int, events []Event) ([]uint64, error) {
		if call == 1 {
			return []uint64{1, 3}, fmt.Errorf("event 2 was returned")
		}
		return []uint64{events[0].ID}, nil
	}}
	relay = NewRelay(db, partial)
	if sent, err := relay.RelayOnce(ctx); sent != 2 || err == nil {
		t.Fatalf("a partly confirmed batch: %d, %v", sent, err)
	}
	if ids := pending(); !slices.Equal(ids, []uint64{2}) {
		t.Fatalf("only the unconfirmed event may stay pending, got %v", ids)
	}
	if sent, err := relay.RelayOnce(ctx); sent != 1 || err != nil || len(pending()) != 0 {
		t.Fatalf("the unconfirmed event must go out next time: %d, %v", sent, err)
	}

	// holdBatch starts a relay whose publisher blocks with its batch claimed; the returned
	// function releases it and waits for the round to finish.
	holdBatch := func(batchSize int) (*blockingPublisher, func() int) {
		t.Helper()
		publisher := &blockingPublisher{started: make(chan struct{}), release: make(chan struct{})}
		holder := NewRelay(db, publisher)
		holder.BatchSize = batchSize
		done := make(chan int, 1)
		go func() {
			sent, err := holder.RelayOnce(ctx)
			if err != nil {
				t.Errorf("held round: %v", err)
			}
			done <- sent
		}()
		select {
		case <-publisher.started:
		case <-time.After(10 * time.Second):
			t.Fatal("the held relay never claimed its batch")
		}
		return publisher, func() int { close(publisher.release); return <-done }
	}

	// 3. SKIP LOCKED: while one relay publishes events 1 and 2, another one takes 3 and 4
	// instead of waiting for the locks or sending the same events again.
	reset()
	insert(4)
	_, finish := holdBatch(2)
	second := &fakePublisher{}
	other := NewRelay(db, second)
	other.BatchSize = 2
	quick, stop := context.WithTimeout(ctx, 3*time.Second)
	sent, err := other.RelayOnce(quick)
	stop()
	if err != nil || sent != 2 || len(second.calls) != 1 || second.calls[0][0].ID != 3 || second.calls[0][1].ID != 4 {
		t.Fatalf("the second relay must skip the held rows and take 3 and 4: %d, %v, %v", sent, err, second.calls)
	}
	if sent := finish(); sent != 2 || len(pending()) != 0 {
		t.Fatalf("the held batch must still be marked: %d, pending %v", sent, pending())
	}

	// 4. Read committed: a relay that has claimed the end of the pending range holds no gap lock,
	// so a like or comment can still insert its event right after it.
	reset()
	insert(2)
	_, finish = holdBatch(100)
	quick, stop = context.WithTimeout(ctx, 3*time.Second)
	tx, err := pool.BeginTx(quick, &sql.TxOptions{}) // the default isolation, like a business transaction
	if err == nil {
		_, err = tx.ExecContext(quick, "INSERT INTO outbox_events (event_type, payload) VALUES (?, '{}')", TypeCommentCreated)
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	stop()
	if err != nil {
		t.Fatalf("a new event must not wait for the relay's transaction: %v", err)
	}
	if sent := finish(); sent != 2 {
		t.Fatalf("the held batch: %d", sent)
	}
	if ids := pending(); !slices.Equal(ids, []uint64{3}) {
		t.Fatalf("the event inserted meanwhile must still be pending, got %v", ids)
	}

	// 5. Cleanup deletes only events published before the retention window, 1000 rows a round.
	reset()
	old := make([]string, 2500)
	for i := range old {
		old[i] = "('like.created', '{}', NOW() - INTERVAL 8 DAY)"
	}
	if _, err := pool.ExecContext(ctx, "INSERT INTO outbox_events (event_type, payload, published_at) VALUES "+strings.Join(old, ",")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO outbox_events (event_type, payload, published_at) VALUES
		('like.created', '{}', NOW() - INTERVAL 1 DAY), ('like.created', '{}', NOW() - INTERVAL 1 DAY), ('like.created', '{}', NULL)`); err != nil {
		t.Fatal(err)
	}
	deleted, err := NewRelay(db, nil).DeletePublished(ctx)
	if err != nil || deleted != 2500 {
		t.Fatalf("cleanup: %d, %v; want the 2500 old events", deleted, err)
	}
	var left, stillPending int
	if err := pool.QueryRowContext(ctx, "SELECT COUNT(*), COUNT(*) - COUNT(published_at) FROM outbox_events").Scan(&left, &stillPending); err != nil {
		t.Fatal(err)
	}
	if left != 3 || stillPending != 1 {
		t.Fatalf("recent and pending events must stay: %d left, %d pending", left, stillPending)
	}
}
