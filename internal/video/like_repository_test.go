package video

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"gostream/internal/database"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestSQLLikeRepositoryLikeVideo checks that a like and its outbox event are written in one
// transaction: every statement runs on the transaction's connection, success commits, and
// any failure rolls the like back together with the event.
func TestSQLLikeRepositoryLikeVideo(t *testing.T) {
	writeFailure := errors.New("database unavailable")
	resultFailure := errors.New("affected rows unavailable")
	lookupFailure := errors.New("author lookup failed")
	outboxFailure := errors.New("outbox unavailable")
	otherMySQLError := &mysql.MySQLError{Number: 1452, Message: "foreign key constraint"}
	likeOnly := []string{likeInsertSQL}
	likeAndAuthor := []string{likeInsertSQL, authorQuerySQL}
	everything := []string{likeInsertSQL, authorQuerySQL, outboxInsertSQL}
	for _, tc := range []struct {
		name                     string
		likeResult               driver.Result
		likeError                error
		authorID                 int64
		authorError, outboxError error
		wantError                error
		wantStatements           []string
		wantEvent                string
		wantCommit               bool
	}{
		{name: "like_writes_an_event_for_the_author", likeResult: driver.RowsAffected(1), authorID: 99, wantStatements: everything,
			wantEvent: `{"actor_id":12,"author_id":99,"video_id":34}`, wantCommit: true},
		{name: "own_video_writes_no_event", likeResult: driver.RowsAffected(1), authorID: 12, wantStatements: likeAndAuthor, wantCommit: true},
		{name: "duplicate_like", likeError: &mysql.MySQLError{Number: 1062}, wantError: ErrAlreadyLiked, wantStatements: likeOnly},
		{name: "wrapped_duplicate_like", likeError: fmt.Errorf("driver: %w", &mysql.MySQLError{Number: 1062}), wantError: ErrAlreadyLiked, wantStatements: likeOnly},
		{name: "video_not_found", likeResult: driver.RowsAffected(0), wantError: ErrLikeVideoNotFound, wantStatements: likeOnly},
		{name: "write_failure_preserves_cause", likeError: writeFailure, wantError: writeFailure, wantStatements: likeOnly},
		{name: "other_mysql_error_is_not_duplicate", likeError: otherMySQLError, wantError: otherMySQLError, wantStatements: likeOnly},
		{name: "affected_rows_failure_preserves_cause", likeResult: likeRepositoryResult{err: resultFailure}, wantError: resultFailure, wantStatements: likeOnly},
		{name: "author_lookup_failure_rolls_back", likeResult: driver.RowsAffected(1), authorError: lookupFailure, wantError: lookupFailure, wantStatements: likeAndAuthor},
		{name: "outbox_failure_rolls_back_the_like", likeResult: driver.RowsAffected(1), authorID: 99, outboxError: outboxFailure, wantError: outboxFailure,
			wantStatements: everything, wantEvent: `{"actor_id":12,"author_id":99,"video_id":34}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var event string
			db := &txTestDB{ctx: ctx,
				exec: func(query string, args []driver.NamedValue) (driver.Result, error) {
					switch query {
					case likeInsertSQL:
						// The insert must be conditional on the target video existing.
						if len(args) != 2 || args[0].Value != int64(12) || args[1].Value != int64(34) {
							t.Error("like arguments must be user ID 12 then video ID 34")
						}
						return tc.likeResult, tc.likeError
					case outboxInsertSQL:
						if len(args) != 2 || args[0].Value != "like.created" {
							t.Errorf("the event type must be like.created, got %v", args)
						}
						event, _ = args[1].Value.(string)
						return driver.RowsAffected(1), tc.outboxError
					}
					return nil, errors.New("unexpected statement")
				},
				query: func(query string, args []driver.NamedValue) (driver.Rows, error) {
					if len(args) != 1 || args[0].Value != int64(34) {
						t.Error("the author lookup must use video ID 34")
					}
					if tc.authorError != nil {
						return nil, tc.authorError
					}
					return authorRows(tc.authorID), nil
				}}
			pool := sql.OpenDB(db)
			t.Cleanup(func() { pool.Close() })
			err := (&SQLLikeRepository{DB: database.WrapSQL(pool)}).LikeVideo(ctx, 12, 34)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("expected error %v, got %v", tc.wantError, err)
			}
			db.expect(t, tc.wantStatements, tc.wantCommit)
			if event != tc.wantEvent {
				t.Errorf("event payload %s, want %s", event, tc.wantEvent)
			}
		})
	}
}

// TestSQLLikeRepositoryTransactionFailures: failing to begin or commit is reported, never
// swallowed, because the like would then silently not exist.
func TestSQLLikeRepositoryTransactionFailures(t *testing.T) {
	beginFailure, commitFailure := errors.New("cannot begin"), errors.New("cannot commit")
	for _, tc := range []struct {
		name string
		db   *txTestDB
		want error
	}{
		{name: "begin", db: &txTestDB{beginErr: beginFailure}, want: beginFailure},
		{name: "commit", db: &txTestDB{commitErr: commitFailure}, want: commitFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.db.exec = func(string, []driver.NamedValue) (driver.Result, error) { return driver.RowsAffected(1), nil }
			tc.db.query = func(string, []driver.NamedValue) (driver.Rows, error) { return authorRows(99), nil }
			pool := sql.OpenDB(tc.db)
			t.Cleanup(func() { pool.Close() })
			if err := (&SQLLikeRepository{DB: database.WrapSQL(pool)}).LikeVideo(context.Background(), 12, 34); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

type likeRepositoryConnector struct {
	exec func(context.Context, string, []driver.NamedValue) (driver.Result, error)
}

func (c likeRepositoryConnector) Connect(context.Context) (driver.Conn, error) {
	return likeRepositoryConn{connector: c}, nil
}
func (likeRepositoryConnector) Driver() driver.Driver { return likeRepositoryDriver{} }

type likeRepositoryDriver struct{}

func (likeRepositoryDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type likeRepositoryConn struct{ connector likeRepositoryConnector }

func (likeRepositoryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (likeRepositoryConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected Begin") }
func (likeRepositoryConn) Close() error              { return nil }
func (c likeRepositoryConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.connector.exec(ctx, query, args)
}

type likeRepositoryResult struct{ err error }

func (likeRepositoryResult) LastInsertId() (int64, error) {
	return 0, errors.New("unexpected LastInsertId")
}
func (r likeRepositoryResult) RowsAffected() (int64, error) { return 0, r.err }
