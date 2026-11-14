package notification

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gostream/internal/database"
)

func TestGORMRepositoryCreate(t *testing.T) {
	down := errors.New("database unavailable")
	missingTable := &mysql.MySQLError{Number: 1146, Message: "Table 'gostream.notifications' doesn't exist"}
	for _, tc := range []struct {
		name         string
		result       driver.Result
		execError    error
		wantError    error // nil means success
		wantRetrying bool  // the error must not be ErrInvalidEvent
	}{
		{name: "inserted", result: driver.RowsAffected(1)},
		// The SELECT found no video: it was deleted, so there is nothing to notify.
		{name: "deleted_video_is_done", result: driver.RowsAffected(0)},
		{name: "duplicate_is_done", execError: &mysql.MySQLError{Number: 1062}},
		{name: "wrapped_duplicate_is_done", execError: fmt.Errorf("driver: %w", &mysql.MySQLError{Number: 1062})},
		{name: "missing_user_is_invalid", execError: &mysql.MySQLError{Number: 1452}, wantError: ErrInvalidEvent},
		{name: "missing_table_can_be_retried", execError: missingTable, wantError: missingTable, wantRetrying: true},
		{name: "unavailable_database_can_be_retried", execError: down, wantError: down, wantRetrying: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := newFakeRepository(t, func(got context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
				calls++
				if got != ctx {
					t.Error("the caller's context must reach the database")
				}
				// The insert is conditional on the video still existing.
				want := "INSERT INTO notifications (recipient_id, actor_id, event_id, event_type, video_id, comment_id, created_at) SELECT ?, ?, ?, ?, id, ?, ? FROM videos WHERE id = ?"
				if strings.Join(strings.Fields(query), " ") != want {
					t.Errorf("unexpected INSERT: %s", query)
				}
				expectArgs(t, args, nil)
				return tc.result, tc.execError
			})
			err := repo.Create(ctx, Notification{RecipientID: 3, ActorID: 7, EventID: 41, EventType: "like.created", VideoID: 42, CreatedAt: eventTime})
			switch {
			case tc.wantError == nil && err != nil:
				t.Fatalf("expected success, got %v", err)
			case tc.wantError != nil && !errors.Is(err, tc.wantError):
				t.Fatalf("expected %v, got %v", tc.wantError, err)
			case tc.wantRetrying && errors.Is(err, ErrInvalidEvent):
				t.Fatalf("a database problem must not make the event invalid: %v", err)
			}
			if calls != 1 {
				t.Errorf("expected one database call, got %d", calls)
			}
		})
	}
}

func TestGORMRepositoryCreateStoresTheCommentID(t *testing.T) {
	comment := uint64(9)
	repo := newFakeRepository(t, func(_ context.Context, _ string, args []driver.NamedValue) (driver.Result, error) {
		expectArgs(t, args, int64(9))
		return driver.RowsAffected(1), nil
	})
	if err := repo.Create(context.Background(), Notification{RecipientID: 3, ActorID: 7, EventID: 41, EventType: "like.created", VideoID: 42, CommentID: &comment, CreatedAt: eventTime}); err != nil {
		t.Fatal(err)
	}
}

// expectArgs checks the arguments in the order of the INSERT … SELECT: recipient, actor, event,
// type, comment (NULL for likes), time, and the video the SELECT looks up.
func expectArgs(t *testing.T, args []driver.NamedValue, comment driver.Value) {
	t.Helper()
	want := []driver.Value{int64(3), int64(7), int64(41), "like.created", comment, eventTime, int64(42)}
	if len(args) != len(want) {
		t.Fatalf("expected %d arguments, got %v", len(want), args)
	}
	for i := range want {
		if when, ok := args[i].Value.(time.Time); ok {
			if !when.Equal(eventTime) || i != 5 {
				t.Errorf("argument %d: got %v, want %v", i, args[i].Value, want[i])
			}
			continue
		}
		if args[i].Value != want[i] {
			t.Errorf("argument %d: got %#v, want %#v", i, args[i].Value, want[i])
		}
	}
}

type execFunc func(context.Context, string, []driver.NamedValue) (driver.Result, error)

// fakeConnector answers every Exec with exec and refuses everything else.
type fakeConnector struct{ exec execFunc }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return fakeConn(c), nil }
func (fakeConnector) Driver() driver.Driver                          { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type fakeConn struct{ exec execFunc }

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected Prepare") }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected Begin") }
func (fakeConn) Close() error                        { return nil }
func (c fakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.exec(ctx, query, args)
}

func newFakeRepository(t *testing.T, exec execFunc) *GORMRepository {
	t.Helper()
	db := sql.OpenDB(fakeConnector{exec: exec})
	t.Cleanup(func() { db.Close() })
	return &GORMRepository{DB: database.WrapSQL(db)}
}
