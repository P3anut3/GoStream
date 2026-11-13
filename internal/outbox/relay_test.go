package outbox

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"gostream/internal/database"
)

// The statements RelayOnce and DeletePublished send, whitespace-normalized.
const (
	claimSQL   = "SELECT id, event_type, payload, created_at FROM outbox_events WHERE published_at IS NULL ORDER BY id LIMIT ? FOR UPDATE SKIP LOCKED"
	markPrefix = "UPDATE outbox_events SET published_at = NOW() WHERE id IN ("
	cleanupSQL = "DELETE FROM outbox_events WHERE published_at < NOW() - INTERVAL ? SECOND LIMIT 1000"
)

// relayTestDB stands in for the outbox table behind database/sql: SELECT returns pending events
// up to its LIMIT, UPDATE marks the listed IDs as published, DELETE reports the next value of
// deleted. It records every statement, whether it ran inside a transaction, the isolation level
// of each transaction and how each one ended.
type relayTestDB struct {
	mu                           sync.Mutex
	pending                      []Event
	claimErr, markErr, deleteErr error
	deleted                      []int64

	statements                   []relayStatement
	marked                       [][]uint64
	limits                       []int64
	isolations                   []driver.IsolationLevel
	began, committed, rolledBack int
}

type relayStatement struct {
	sql  string
	inTx bool
	at   time.Time
}

func (d *relayTestDB) Connect(context.Context) (driver.Conn, error) {
	return &relayTestConn{db: d}, nil
}
func (d *relayTestDB) Driver() driver.Driver { return relayTestDriver{} }

func (d *relayTestDB) record(sql string, inTx bool) {
	d.statements = append(d.statements, relayStatement{sql: strings.Join(strings.Fields(sql), " "), inTx: inTx, at: time.Now()})
}

// sqlOf returns the recorded statements; claimTimes the moments a batch was claimed.
func (d *relayTestDB) sqlOf() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var all []string
	for _, s := range d.statements {
		all = append(all, s.sql)
	}
	return all
}

func (d *relayTestDB) claimTimes(start time.Time) []time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	var times []time.Duration
	for _, s := range d.statements {
		if s.sql == claimSQL {
			times = append(times, s.at.Sub(start))
		}
	}
	return times
}

type relayTestDriver struct{}

func (relayTestDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type relayTestConn struct {
	db   *relayTestDB
	inTx bool
}

func (*relayTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (*relayTestConn) Close() error              { return nil }
func (*relayTestConn) Begin() (driver.Tx, error) { return nil, errors.New("use BeginTx") }

func (c *relayTestConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.db.began++
	c.db.isolations = append(c.db.isolations, opts.Isolation)
	c.inTx = true
	return relayTestTx{c}, nil
}

func (c *relayTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	d := c.db
	d.mu.Lock()
	defer d.mu.Unlock()
	d.record(query, c.inTx)
	if d.claimErr != nil {
		return nil, d.claimErr
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("the claim must pass its LIMIT as the only argument, got %d", len(args))
	}
	limit, _ := args[0].Value.(int64)
	d.limits = append(d.limits, limit)
	rows := &relayTestRows{}
	for _, event := range d.pending {
		if int64(len(rows.values)) == limit {
			break
		}
		rows.values = append(rows.values, []driver.Value{int64(event.ID), event.Type, event.Payload, event.CreatedAt})
	}
	return rows, nil
}

func (c *relayTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	d := c.db
	d.mu.Lock()
	defer d.mu.Unlock()
	d.record(query, c.inTx)
	if strings.HasPrefix(strings.Join(strings.Fields(query), " "), "DELETE") {
		if d.deleteErr != nil {
			return nil, d.deleteErr
		}
		var n int64
		if len(d.deleted) > 0 {
			n, d.deleted = d.deleted[0], d.deleted[1:]
		}
		return driver.RowsAffected(n), nil
	}
	if d.markErr != nil {
		return nil, d.markErr
	}
	var ids []uint64
	for _, arg := range args {
		id, _ := arg.Value.(int64)
		ids = append(ids, uint64(id))
	}
	d.marked = append(d.marked, ids)
	d.pending = slices.DeleteFunc(d.pending, func(e Event) bool { return slices.Contains(ids, e.ID) })
	return driver.RowsAffected(len(ids)), nil
}

type relayTestTx struct{ conn *relayTestConn }

func (tx relayTestTx) Commit() error {
	tx.conn.db.mu.Lock()
	defer tx.conn.db.mu.Unlock()
	tx.conn.inTx = false
	tx.conn.db.committed++
	return nil
}

func (tx relayTestTx) Rollback() error {
	tx.conn.db.mu.Lock()
	defer tx.conn.db.mu.Unlock()
	tx.conn.inTx = false
	tx.conn.db.rolledBack++
	return nil
}

type relayTestRows struct{ values [][]driver.Value }

func (*relayTestRows) Columns() []string {
	return []string{"id", "event_type", "payload", "created_at"}
}
func (*relayTestRows) Close() error { return nil }
func (r *relayTestRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

// fakePublisher records every batch; decide answers each call (nil confirms everything).
type fakePublisher struct {
	mu     sync.Mutex
	calls  [][]Event
	decide func(call int, events []Event) ([]uint64, error)
}

func (p *fakePublisher) Publish(_ context.Context, events []Event) ([]uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, events)
	if p.decide != nil {
		return p.decide(len(p.calls), events)
	}
	var ids []uint64
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	return ids, nil
}

func pendingEvents(ids ...uint64) []Event {
	var events []Event
	for _, id := range ids {
		events = append(events, Event{ID: id, Type: TypeLikeCreated, Payload: []byte(`{"actor_id":1}`), CreatedAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)})
	}
	return events
}

func newTestRelay(db *relayTestDB, publisher Publisher) (*Relay, *sql.DB) {
	pool := sql.OpenDB(db)
	relay := NewRelay(database.WrapSQL(pool), publisher)
	return relay, pool
}

func TestNewRelayDefaults(t *testing.T) {
	r := NewRelay(nil, nil)
	if r.BatchSize != 100 || r.PollInterval != time.Second || r.MaxBackoff != 30*time.Second || r.Retention != 7*24*time.Hour {
		t.Fatalf("unexpected defaults %+v", r)
	}
}

// TestRelayOnce checks one round: claim in a read-committed transaction, publish, mark only the
// confirmed events and commit; failures before marking leave every event pending.
func TestRelayOnce(t *testing.T) {
	publishFailure := errors.New("broker unavailable")
	markFailure := errors.New("cannot mark")
	claimFailure := errors.New("cannot claim")
	for _, tc := range []struct {
		name          string
		pending       []Event
		claimErr      error
		markErr       error
		decide        func(int, []Event) ([]uint64, error)
		wantSent      int
		wantErr       error
		wantPublished int // events handed to the publisher
		wantMarked    []uint64
		wantSQL       []string
		wantCommit    bool
	}{
		{name: "everything_confirmed", pending: pendingEvents(1, 2, 3), wantSent: 3, wantPublished: 3, wantMarked: []uint64{1, 2, 3},
			wantSQL: []string{claimSQL, markPrefix + "?,?,?)"}, wantCommit: true},
		{name: "only_confirmed_events_are_marked", pending: pendingEvents(1, 2, 3),
			decide:   func(int, []Event) ([]uint64, error) { return []uint64{1, 3}, publishFailure },
			wantSent: 2, wantErr: publishFailure, wantPublished: 3, wantMarked: []uint64{1, 3},
			wantSQL: []string{claimSQL, markPrefix + "?,?)"}, wantCommit: true},
		{name: "nothing_confirmed_marks_nothing", pending: pendingEvents(1, 2),
			decide:  func(int, []Event) ([]uint64, error) { return nil, publishFailure },
			wantErr: publishFailure, wantPublished: 2, wantSQL: []string{claimSQL}, wantCommit: true},
		{name: "empty_outbox_publishes_nothing", wantSQL: []string{claimSQL}, wantCommit: true},
		{name: "claim_failure", pending: pendingEvents(1), claimErr: claimFailure, wantErr: claimFailure, wantSQL: []string{claimSQL}},
		{name: "mark_failure_rolls_back", pending: pendingEvents(1, 2), markErr: markFailure, wantErr: markFailure, wantPublished: 2,
			wantSQL: []string{claimSQL, markPrefix + "?,?)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &relayTestDB{pending: tc.pending, claimErr: tc.claimErr, markErr: tc.markErr}
			publisher := &fakePublisher{decide: tc.decide}
			relay, pool := newTestRelay(db, publisher)
			t.Cleanup(func() { pool.Close() })
			sent, err := relay.RelayOnce(context.Background())
			if sent != tc.wantSent || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("got %d, %v; want %d, %v", sent, err, tc.wantSent, tc.wantErr)
			}
			if got := db.sqlOf(); !slices.Equal(got, tc.wantSQL) {
				t.Errorf("statements %q, want %q", got, tc.wantSQL)
			}
			for _, s := range db.statements {
				if !s.inTx {
					t.Errorf("%q ran outside the transaction", s.sql)
				}
			}
			// Read committed takes no gap locks, so new likes can insert events meanwhile.
			if len(db.isolations) != 1 || db.isolations[0] != driver.IsolationLevel(sql.LevelReadCommitted) {
				t.Errorf("the claim must run in one read-committed transaction, got %v", db.isolations)
			}
			wantCommit, wantRollback := 1, 0
			if !tc.wantCommit {
				wantCommit, wantRollback = 0, 1
			}
			if db.committed != wantCommit || db.rolledBack != wantRollback {
				t.Errorf("committed %d, rolled back %d; want %d, %d", db.committed, db.rolledBack, wantCommit, wantRollback)
			}
			published := 0
			for _, batch := range publisher.calls {
				published += len(batch)
			}
			if published != tc.wantPublished {
				t.Errorf("published %d events, want %d", published, tc.wantPublished)
			}
			var marked []uint64
			for _, ids := range db.marked {
				marked = append(marked, ids...)
			}
			if tc.markErr == nil && !slices.Equal(marked, tc.wantMarked) {
				t.Errorf("marked %v, want %v", marked, tc.wantMarked)
			}
			if tc.claimErr == nil && (len(db.limits) != 1 || db.limits[0] != 100) {
				t.Errorf("the claim must be limited to the batch size, got %v", db.limits)
			}
		})
	}
}

func TestRelayOnceHandsEventsOverIntact(t *testing.T) {
	created := time.Date(2026, 11, 12, 10, 30, 0, 0, time.UTC)
	db := &relayTestDB{pending: []Event{{ID: 7, Type: TypeCommentCreated, Payload: []byte(`{"comment_id":9}`), CreatedAt: created}}}
	publisher := &fakePublisher{}
	relay, pool := newTestRelay(db, publisher)
	t.Cleanup(func() { pool.Close() })
	if _, err := relay.RelayOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := publisher.calls[0][0]
	if got.ID != 7 || got.Type != TypeCommentCreated || string(got.Payload) != `{"comment_id":9}` || !got.CreatedAt.Equal(created) {
		t.Fatalf("the publisher must receive the row as stored, got %+v", got)
	}
}

// quietLogs keeps the relay's progress logs out of the test output.
func quietLogs(t *testing.T) {
	writer := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(writer) })
}

// TestRunBacksOffAndRecovers: failures wait 1, 2, 4… seconds up to MaxBackoff; a success returns
// to polling every PollInterval and starts the next run of failures from 1 second again.
// synctest runs the loop on a fake clock.
func TestRunBacksOffAndRecovers(t *testing.T) {
	quietLogs(t)
	synctest.Test(t, func(t *testing.T) {
		db := &relayTestDB{pending: pendingEvents(1)}
		publisher := &fakePublisher{decide: func(call int, events []Event) ([]uint64, error) {
			if call <= 5 || call == 7 {
				return nil, errors.New("broker unavailable")
			}
			return []uint64{events[0].ID}, nil
		}}
		relay, pool := newTestRelay(db, publisher)
		defer pool.Close()
		relay.MaxBackoff = 5 * time.Second
		ctx, cancel := context.WithCancel(context.Background())
		start := time.Now()
		done := make(chan struct{})
		go func() { relay.Run(ctx); close(done) }()
		// A new event arrives after the outage is over; its first try fails once more.
		time.Sleep(19*time.Second + 500*time.Millisecond)
		db.mu.Lock()
		db.pending = pendingEvents(2)
		db.mu.Unlock()
		time.Sleep(3 * time.Second)
		cancel()
		<-done
		// Five failures wait 1, 2, 4, then 5 and 5 (the cap); the sixth try succeeds at 17s and
		// polling goes back to once a second. The failure at 20s waits only 1 second again.
		want := []time.Duration{0, 1 * time.Second, 3 * time.Second, 7 * time.Second, 12 * time.Second, 17 * time.Second,
			18 * time.Second, 19 * time.Second, 20 * time.Second, 21 * time.Second, 22 * time.Second}
		if got := db.claimTimes(start); !slices.Equal(got, want) {
			t.Fatalf("claims at %v, want %v", got, want)
		}
	})
}

// TestRunDrainsFullBatchesAtOnce: a full batch means more may be waiting, so the next claim
// follows immediately instead of after PollInterval.
func TestRunDrainsFullBatchesAtOnce(t *testing.T) {
	quietLogs(t)
	synctest.Test(t, func(t *testing.T) {
		db := &relayTestDB{pending: pendingEvents(1, 2, 3, 4, 5)}
		relay, pool := newTestRelay(db, &fakePublisher{})
		defer pool.Close()
		relay.BatchSize = 2
		ctx, cancel := context.WithCancel(context.Background())
		start := time.Now()
		done := make(chan struct{})
		go func() { relay.Run(ctx); close(done) }()
		time.Sleep(1500 * time.Millisecond)
		cancel()
		<-done
		if got, want := db.claimTimes(start), []time.Duration{0, 0, 0, time.Second}; !slices.Equal(got, want) {
			t.Fatalf("claims at %v, want %v", got, want)
		}
		if len(db.pending) != 0 {
			t.Fatalf("every event must be sent, %d left", len(db.pending))
		}
	})
}

// TestRunCleansUpHourly: old published events are removed when the relay starts and then once
// an hour, and a failing cleanup does not stop delivery.
func TestRunCleansUpHourly(t *testing.T) {
	quietLogs(t)
	synctest.Test(t, func(t *testing.T) {
		db := &relayTestDB{deleteErr: errors.New("cleanup failed")}
		relay, pool := newTestRelay(db, &fakePublisher{})
		defer pool.Close()
		relay.PollInterval = time.Minute
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { relay.Run(ctx); close(done) }()
		time.Sleep(2*time.Hour + 30*time.Minute)
		cancel()
		<-done
		deletes, claims := 0, 0
		for _, s := range db.sqlOf() {
			switch s {
			case cleanupSQL:
				deletes++
			case claimSQL:
				claims++
			}
		}
		if deletes != 3 {
			t.Fatalf("cleanup must run at start and then hourly: 3 times in 2.5 hours, got %d", deletes)
		}
		if claims < 150 {
			t.Fatalf("a failing cleanup must not stop delivery, only %d claims", claims)
		}
	})
}

func TestRunStopsWhenCanceled(t *testing.T) {
	quietLogs(t)
	synctest.Test(t, func(t *testing.T) {
		db := &relayTestDB{}
		relay, pool := newTestRelay(db, &fakePublisher{})
		defer pool.Close()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { relay.Run(ctx); close(done) }()
		time.Sleep(500 * time.Millisecond)
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("Run must return as soon as it is canceled")
		}
	})
}

// TestDeletePublished removes old published events 1000 rows at a time until fewer remain.
func TestDeletePublished(t *testing.T) {
	db := &relayTestDB{deleted: []int64{1000, 1000, 500}}
	relay, pool := newTestRelay(db, &fakePublisher{})
	t.Cleanup(func() { pool.Close() })
	relay.Retention = 48 * time.Hour
	deleted, err := relay.DeletePublished(context.Background())
	if err != nil || deleted != 2500 {
		t.Fatalf("got %d, %v; want 2500 in three rounds", deleted, err)
	}
	if got := db.sqlOf(); len(got) != 3 || got[0] != cleanupSQL {
		t.Fatalf("statements %q", got)
	}
	failure := errors.New("cleanup failed")
	db = &relayTestDB{deleteErr: failure}
	relay, pool = newTestRelay(db, &fakePublisher{})
	t.Cleanup(func() { pool.Close() })
	if _, err := relay.DeletePublished(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("a failed cleanup must be reported, got %v", err)
	}
}
