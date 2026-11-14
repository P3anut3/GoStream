package notification

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"slices"
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
			repo := newFakeRepository(t, fakeConnector{exec: func(got context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
				calls++
				if got != ctx {
					t.Error("the caller's context must reach the database")
				}
				// The insert is conditional on the video still existing.
				want := "INSERT INTO notifications (recipient_id, actor_id, event_id, event_type, video_id, comment_id, created_at) SELECT ?, ?, ?, ?, id, ?, ? FROM videos WHERE id = ?"
				if normalize(query) != want {
					t.Errorf("unexpected INSERT: %s", query)
				}
				expectArgs(t, args, nil)
				return tc.result, tc.execError
			}})
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
	repo := newFakeRepository(t, fakeConnector{exec: func(_ context.Context, _ string, args []driver.NamedValue) (driver.Result, error) {
		expectArgs(t, args, int64(9))
		return driver.RowsAffected(1), nil
	}})
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

// listSQL is the query List must send, without and with a cursor.
const listSQL = "SELECT n.id, n.event_type, n.actor_id, u.username AS actor_name, n.video_id, v.title AS video_title, " +
	"n.comment_id, c.content AS comment_content, n.created_at, n.read_at IS NOT NULL AS is_read " +
	"FROM notifications n JOIN users u ON u.id = n.actor_id JOIN videos v ON v.id = n.video_id " +
	"LEFT JOIN video_comments c ON c.id = n.comment_id WHERE n.recipient_id = ?"

var listColumns = []string{"id", "event_type", "actor_id", "actor_name", "video_id", "video_title", "comment_id", "comment_content", "created_at", "is_read"}

func TestGORMRepositoryList(t *testing.T) {
	for _, tc := range []struct {
		name     string
		before   uint64
		wantSQL  string
		wantArgs []driver.Value
	}{
		{name: "first_page", wantSQL: listSQL + " ORDER BY n.id DESC LIMIT ?", wantArgs: []driver.Value{int64(3), int64(21)}},
		{name: "after_a_cursor", before: 8, wantSQL: listSQL + " AND n.id < ? ORDER BY n.id DESC LIMIT ?", wantArgs: []driver.Value{int64(3), int64(8), int64(21)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			repo := newFakeRepository(t, fakeConnector{query: func(got context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
				if got != ctx {
					t.Error("the caller's context must reach the database")
				}
				if normalize(query) != tc.wantSQL {
					t.Errorf("unexpected query:\n%s\nwant\n%s", normalize(query), tc.wantSQL)
				}
				if !slices.Equal(values(args), tc.wantArgs) {
					t.Errorf("arguments %v, want %v", values(args), tc.wantArgs)
				}
				return &fakeRows{columns: listColumns, rows: [][]driver.Value{
					{int64(9), "comment.created", int64(7), "bob", int64(42), "第二个视频", int64(5), "拍得真好", eventTime, int64(0)},
					{int64(8), "comment.created", int64(7), "bob", int64(42), "第二个视频", int64(4), nil, eventTime, int64(1)},
					{int64(7), "like.created", int64(7), "bob", int64(42), "第二个视频", nil, nil, eventTime, int64(1)},
				}}, nil
			}})
			items, err := repo.List(ctx, 3, tc.before, 21)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 3 {
				t.Fatalf("expected three items, got %+v", items)
			}
			first, second, third := items[0], items[1], items[2]
			if first.ID != 9 || first.Type != "comment.created" || first.ActorID != 7 || first.ActorName != "bob" || first.VideoID != 42 ||
				first.VideoTitle != "第二个视频" || first.CommentID == nil || *first.CommentID != 5 || first.CommentContent == nil ||
				*first.CommentContent != "拍得真好" || !first.CreatedAt.Equal(eventTime) || first.Read {
				t.Fatalf("unread comment: %+v", first)
			}
			// A deleted comment keeps its ID but has no content; a like has neither.
			if second.CommentID == nil || *second.CommentID != 4 || second.CommentContent != nil || !second.Read {
				t.Fatalf("read notification about a deleted comment: %+v", second)
			}
			if third.Type != "like.created" || third.CommentID != nil || third.CommentContent != nil || !third.Read {
				t.Fatalf("like: %+v", third)
			}
		})
	}
	down := errors.New("database unavailable")
	repo := newFakeRepository(t, fakeConnector{query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) { return nil, down }})
	if _, err := repo.List(context.Background(), 3, 0, 21); !errors.Is(err, down) {
		t.Fatalf("a database error must be returned, got %v", err)
	}
}

func TestGORMRepositoryCountUnread(t *testing.T) {
	repo := newFakeRepository(t, fakeConnector{query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
		if normalize(query) != "SELECT COUNT(*) FROM notifications WHERE recipient_id = ? AND read_at IS NULL" {
			t.Errorf("unexpected query: %s", query)
		}
		if !slices.Equal(values(args), []driver.Value{int64(3)}) {
			t.Errorf("arguments %v", values(args))
		}
		return &fakeRows{columns: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(4)}}}, nil
	}})
	if count, err := repo.CountUnread(context.Background(), 3); err != nil || count != 4 {
		t.Fatalf("count %d, %v", count, err)
	}
	down := errors.New("database unavailable")
	failing := newFakeRepository(t, fakeConnector{query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) { return nil, down }})
	if _, err := failing.CountUnread(context.Background(), 3); !errors.Is(err, down) {
		t.Fatalf("a database error must be returned, got %v", err)
	}
}

func TestGORMRepositoryMarkRead(t *testing.T) {
	const updateSQL = "UPDATE notifications SET read_at = NOW() WHERE id = ? AND recipient_id = ? AND read_at IS NULL"
	const existsSQL = "SELECT EXISTS(SELECT 1 FROM notifications WHERE id = ? AND recipient_id = ?)"
	down := errors.New("database unavailable")
	for _, tc := range []struct {
		name      string
		changed   int64
		execErr   error
		exists    []driver.Value // the EXISTS answer; nil means the query is not expected
		queryErr  error
		wantErr   error
		wantQuery bool
	}{
		// The update changed the row: it was unread, now it is read.
		{name: "marked", changed: 1},
		// Nothing changed: either it was read before (still a success) or it is not theirs.
		{name: "already_read", exists: []driver.Value{int64(1)}, wantQuery: true},
		{name: "someone_elses_or_missing", exists: []driver.Value{int64(0)}, wantQuery: true, wantErr: ErrNotFound},
		{name: "update_fails", execErr: down, wantErr: down},
		{name: "check_fails", queryErr: down, wantQuery: true, wantErr: down},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queried := false
			repo := newFakeRepository(t, fakeConnector{
				exec: func(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
					if normalize(query) != updateSQL || !slices.Equal(values(args), []driver.Value{int64(7), int64(3)}) {
						t.Errorf("unexpected update %s %v", query, values(args))
					}
					return driver.RowsAffected(tc.changed), tc.execErr
				},
				query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
					queried = true
					if normalize(query) != existsSQL || !slices.Equal(values(args), []driver.Value{int64(7), int64(3)}) {
						t.Errorf("unexpected check %s %v", query, values(args))
					}
					if tc.queryErr != nil {
						return nil, tc.queryErr
					}
					return &fakeRows{columns: []string{"exists"}, rows: [][]driver.Value{tc.exists}}, nil
				},
			})
			err := repo.MarkRead(context.Background(), 3, 7)
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
			if tc.wantErr == down && errors.Is(err, ErrNotFound) {
				t.Fatal("a database error must not look like a missing notification")
			}
			if queried != tc.wantQuery {
				t.Fatalf("checked whether it exists: %v, want %v", queried, tc.wantQuery)
			}
		})
	}
}

func TestGORMRepositoryMarkAllRead(t *testing.T) {
	repo := newFakeRepository(t, fakeConnector{exec: func(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
		if normalize(query) != "UPDATE notifications SET read_at = NOW() WHERE recipient_id = ? AND id <= ? AND read_at IS NULL" {
			t.Errorf("unexpected update: %s", query)
		}
		if !slices.Equal(values(args), []driver.Value{int64(3), int64(9)}) {
			t.Errorf("arguments %v", values(args))
		}
		return driver.RowsAffected(2), nil
	}})
	if err := repo.MarkAllRead(context.Background(), 3, 9); err != nil {
		t.Fatal(err)
	}
	down := errors.New("database unavailable")
	failing := newFakeRepository(t, fakeConnector{exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) { return nil, down }})
	if err := failing.MarkAllRead(context.Background(), 3, 9); !errors.Is(err, down) {
		t.Fatalf("a database error must be returned, got %v", err)
	}
}

func normalize(query string) string { return strings.Join(strings.Fields(query), " ") }

func values(args []driver.NamedValue) []driver.Value {
	all := make([]driver.Value, len(args))
	for i, arg := range args {
		all[i] = arg.Value
	}
	return all
}

type (
	execFunc  func(context.Context, string, []driver.NamedValue) (driver.Result, error)
	queryFunc func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
)

// fakeConnector answers Exec with exec and Query with query, and refuses everything else.
type fakeConnector struct {
	exec  execFunc
	query queryFunc
}

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return fakeConn(c), nil }
func (fakeConnector) Driver() driver.Driver                          { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type fakeConn struct {
	exec  execFunc
	query queryFunc
}

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected Prepare") }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected Begin") }
func (fakeConn) Close() error                        { return nil }
func (c fakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.exec == nil {
		return nil, errors.New("unexpected Exec")
	}
	return c.exec(ctx, query, args)
}
func (c fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.query == nil {
		return nil, errors.New("unexpected Query")
	}
	return c.query(ctx, query, args)
}

// fakeRows returns rows of values under columns.
type fakeRows struct {
	columns []string
	rows    [][]driver.Value
}

func (r *fakeRows) Columns() []string { return r.columns }
func (*fakeRows) Close() error        { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(dest, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}

func newFakeRepository(t *testing.T, connector fakeConnector) *GORMRepository {
	t.Helper()
	db := sql.OpenDB(connector)
	t.Cleanup(func() { db.Close() })
	return &GORMRepository{DB: database.WrapSQL(db)}
}
