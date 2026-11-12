package outbox

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gostream/internal/database"
)

// The JSON field names are the contract with the consumer that turns events into
// notifications, so renaming a field must fail here first.
func TestEventPayloads(t *testing.T) {
	for _, tc := range []struct {
		event any
		want  string
	}{
		{LikeCreated{ActorID: 1, AuthorID: 2, VideoID: 3}, `{"actor_id":1,"author_id":2,"video_id":3}`},
		{CommentCreated{ActorID: 1, AuthorID: 2, VideoID: 3, CommentID: 4}, `{"actor_id":1,"author_id":2,"video_id":3,"comment_id":4}`},
	} {
		data, err := json.Marshal(tc.event)
		if err != nil || string(data) != tc.want {
			t.Errorf("payload %s (%v), want %s", data, err, tc.want)
		}
	}
	if TypeLikeCreated != "like.created" || TypeCommentCreated != "comment.created" {
		t.Error("event types are routing keys and must not change")
	}
}

func TestAddWritesOneRow(t *testing.T) {
	var calls []string
	var args []driver.NamedValue
	db := sql.OpenDB(execConnector(func(query string, got []driver.NamedValue) (driver.Result, error) {
		calls = append(calls, strings.Join(strings.Fields(query), " "))
		args = got
		return driver.RowsAffected(1), nil
	}))
	t.Cleanup(func() { db.Close() })
	if err := Add(database.WrapSQL(db), TypeLikeCreated, LikeCreated{ActorID: 7, AuthorID: 3, VideoID: 42}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "INSERT INTO outbox_events (event_type, payload) VALUES (?, ?)" {
		t.Fatalf("statements %q", calls)
	}
	// The payload goes in as a string: with interpolateParams a []byte would become a binary
	// string, which a MySQL JSON column refuses (error 3144).
	if len(args) != 2 || args[0].Value != "like.created" || args[1].Value != `{"actor_id":7,"author_id":3,"video_id":42}` {
		t.Fatalf("arguments %v", args)
	}
}

func TestAddReportsFailures(t *testing.T) {
	calls := 0
	failure := errors.New("table is missing")
	db := sql.OpenDB(execConnector(func(string, []driver.NamedValue) (driver.Result, error) {
		calls++
		return nil, failure
	}))
	t.Cleanup(func() { db.Close() })
	// A payload that cannot become JSON never reaches the database.
	if err := Add(database.WrapSQL(db), TypeLikeCreated, make(chan int)); err == nil || calls != 0 {
		t.Fatalf("an unencodable payload must fail before any SQL, got %v after %d calls", err, calls)
	}
	// A failed insert is returned with its cause, so the caller's transaction rolls back.
	if err := Add(database.WrapSQL(db), TypeLikeCreated, LikeCreated{}); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("want the insert failure, got %v after %d calls", err, calls)
	}
}

// execConnector is a database/sql driver that sends every Exec to one function.
type execConnector func(string, []driver.NamedValue) (driver.Result, error)

func (c execConnector) Connect(context.Context) (driver.Conn, error) { return execConn{c}, nil }
func (execConnector) Driver() driver.Driver                          { return execDriver{} }

type execDriver struct{}

func (execDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type execConn struct{ exec execConnector }

func (execConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected Prepare") }
func (execConn) Close() error                        { return nil }
func (execConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected Begin") }
func (c execConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.exec(query, args)
}
