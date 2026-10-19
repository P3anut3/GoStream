package video

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestSQLLikeRepositoryLikeVideo(t *testing.T) {
	writeFailure := errors.New("database unavailable")
	resultFailure := errors.New("affected rows unavailable")
	otherMySQLError := &mysql.MySQLError{Number: 1452, Message: "foreign key constraint"}
	for _, tc := range []struct {
		name                 string
		result               driver.Result
		execError, wantError error
	}{
		{name: "first_like", result: driver.RowsAffected(1)},
		{name: "duplicate_like", execError: &mysql.MySQLError{Number: 1062}, wantError: ErrAlreadyLiked},
		{name: "wrapped_duplicate_like", execError: fmt.Errorf("driver: %w", &mysql.MySQLError{Number: 1062}), wantError: ErrAlreadyLiked},
		{name: "video_not_found", result: driver.RowsAffected(0), wantError: ErrLikeVideoNotFound},
		{name: "write_failure_preserves_cause", execError: writeFailure, wantError: writeFailure},
		{name: "other_mysql_error_is_not_duplicate", execError: otherMySQLError, wantError: otherMySQLError},
		{name: "affected_rows_failure_preserves_cause", result: likeRepositoryResult{err: resultFailure}, wantError: resultFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			connector := likeRepositoryConnector{exec: func(gotContext context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
				calls++
				if gotContext != ctx {
					t.Error("request context must reach the database")
				}
				// The insert must be conditional on the target video existing.
				if strings.Join(strings.Fields(query), " ") != "INSERT INTO video_likes (user_id, video_id) SELECT ?, id FROM videos WHERE id = ?" {
					t.Errorf("unexpected like INSERT: %s", query)
				}
				if len(args) != 2 || args[0].Value != int64(12) || args[1].Value != int64(34) {
					t.Error("SQL arguments must be user ID 12 then video ID 34")
				}
				return tc.result, tc.execError
			}}
			db := sql.OpenDB(connector)
			t.Cleanup(func() { db.Close() })
			err := (&SQLLikeRepository{DB: db}).LikeVideo(ctx, 12, 34)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("expected error %v, got %v", tc.wantError, err)
			}
			if calls != 1 {
				t.Errorf("expected one database call, got %d", calls)
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
