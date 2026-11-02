package follow

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"gostream/internal/database"
)

func TestGORMRepositoryFollow(t *testing.T) {
	writeFailure := errors.New("database unavailable")
	resultFailure := errors.New("affected rows unavailable")
	checkViolation := &mysql.MySQLError{Number: 3819, Message: "Check constraint 'chk_follows_not_self' is violated."}
	for _, tc := range []struct {
		name                 string
		result               driver.Result
		execError, wantError error
	}{
		{name: "first_follow", result: fakeResult{affected: 1}},
		{name: "duplicate_follow", execError: &mysql.MySQLError{Number: 1062}, wantError: ErrAlreadyFollowing},
		{name: "wrapped_duplicate_follow", execError: fmt.Errorf("driver: %w", &mysql.MySQLError{Number: 1062}), wantError: ErrAlreadyFollowing},
		{name: "target_not_found", result: fakeResult{affected: 0}, wantError: ErrUserNotFound},
		{name: "check_violation_is_not_duplicate", execError: checkViolation, wantError: checkViolation},
		{name: "write_failure_preserves_cause", execError: writeFailure, wantError: writeFailure},
		{name: "affected_rows_failure_preserves_cause", result: fakeResult{err: resultFailure}, wantError: resultFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := newFakeRepository(t, fakeConnector{exec: func(got context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
				calls++
				if got != ctx {
					t.Error("request context must reach the database")
				}
				// The insert must be conditional on the target user existing.
				if normalizeSQL(query) != "INSERT INTO follows (follower_id, followee_id) SELECT ?, id FROM users WHERE id = ?" {
					t.Errorf("unexpected follow INSERT: %s", query)
				}
				expectIDs(t, args)
				return tc.result, tc.execError
			}})
			if err := repo.Follow(ctx, 12, 34); !errors.Is(err, tc.wantError) {
				t.Fatalf("expected error %v, got %v", tc.wantError, err)
			}
			if calls != 1 {
				t.Errorf("expected one database call, got %d", calls)
			}
		})
	}
}

func TestGORMRepositoryUnfollow(t *testing.T) {
	writeFailure := errors.New("database unavailable")
	for _, tc := range []struct {
		name                 string
		result               driver.Result
		execError, wantError error
	}{
		{name: "deleted_existing_relation", result: fakeResult{affected: 1}},
		// Unfollowing a relation that does not exist is still a success.
		{name: "nothing_to_delete_is_success", result: fakeResult{affected: 0}},
		{name: "write_failure_preserves_cause", execError: writeFailure, wantError: writeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := newFakeRepository(t, fakeConnector{exec: func(got context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
				calls++
				if got != ctx {
					t.Error("request context must reach the database")
				}
				q := strings.ToLower(normalizeSQL(query))
				if !strings.HasPrefix(q, "delete from") || !strings.Contains(q, "follows") ||
					!strings.Contains(q, "follower_id = ?") || !strings.Contains(q, "followee_id = ?") {
					t.Errorf("unexpected unfollow DELETE: %s", query)
				}
				expectIDs(t, args)
				return tc.result, tc.execError
			}})
			if err := repo.Unfollow(ctx, 12, 34); !errors.Is(err, tc.wantError) {
				t.Fatalf("expected error %v, got %v", tc.wantError, err)
			}
			if calls != 1 {
				t.Errorf("expected one database call, got %d", calls)
			}
		})
	}
}

func TestGORMRepositoryIsFollowing(t *testing.T) {
	queryFailure := errors.New("database unavailable")
	for _, tc := range []struct {
		name       string
		rows       []driver.Value
		queryError error
		want       bool
		wantError  error
	}{
		{name: "following", rows: []driver.Value{int64(1)}, want: true},
		{name: "not_following", rows: []driver.Value{int64(0)}},
		// No row from users means the target is missing, which the handler reports as 404.
		{name: "target_not_found", wantError: ErrUserNotFound},
		{name: "query_failure_preserves_cause", queryError: queryFailure, wantError: queryFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := newFakeRepository(t, fakeConnector{query: func(got context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
				calls++
				if got != ctx {
					t.Error("request context must reach the database")
				}
				if normalizeSQL(query) != "SELECT EXISTS(SELECT 1 FROM follows WHERE follower_id = ? AND followee_id = u.id) FROM users AS u WHERE u.id = ?" {
					t.Errorf("unexpected status query: %s", query)
				}
				expectIDs(t, args)
				if tc.queryError != nil {
					return nil, tc.queryError
				}
				return &fakeRows{values: tc.rows}, nil
			}})
			got, err := repo.IsFollowing(ctx, 12, 34)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("expected error %v, got %v", tc.wantError, err)
			}
			if got != tc.want {
				t.Errorf("expected following=%v, got %v", tc.want, got)
			}
			if calls != 1 {
				t.Errorf("expected one database call, got %d", calls)
			}
		})
	}
}

// The fake driver intercepts SQL at the database/sql layer without a real database.
type fakeConnector struct {
	exec  func(context.Context, string, []driver.NamedValue) (driver.Result, error)
	query func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
}

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return fakeConn{connector: c}, nil
}
func (fakeConnector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type fakeConn struct{ connector fakeConnector }

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected Prepare") }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected Begin") }
func (fakeConn) Close() error                        { return nil }
func (c fakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.connector.exec == nil {
		return nil, errors.New("unexpected Exec")
	}
	return c.connector.exec(ctx, query, args)
}
func (c fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.connector.query == nil {
		return nil, errors.New("unexpected Query")
	}
	return c.connector.query(ctx, query, args)
}

type fakeResult struct {
	affected int64
	err      error
}

func (fakeResult) LastInsertId() (int64, error)   { return 0, errors.New("unexpected LastInsertId") }
func (r fakeResult) RowsAffected() (int64, error) { return r.affected, r.err }

// fakeRows returns one single-column row per value; no values means no rows.
type fakeRows struct{ values []driver.Value }

func (*fakeRows) Columns() []string { return []string{"following"} }
func (*fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	dest[0], r.values = r.values[0], r.values[1:]
	return nil
}

func newFakeRepository(t *testing.T, connector fakeConnector) *GORMRepository {
	t.Helper()
	db := sql.OpenDB(connector)
	t.Cleanup(func() { db.Close() })
	return &GORMRepository{DB: database.WrapSQL(db)}
}

func normalizeSQL(query string) string { return strings.Join(strings.Fields(query), " ") }

// expectIDs checks that the follower (12) comes before the followee (34).
func expectIDs(t *testing.T, args []driver.NamedValue) {
	t.Helper()
	if len(args) != 2 || args[0].Value != int64(12) || args[1].Value != int64(34) {
		t.Errorf("SQL arguments must be follower 12 then followee 34, got %v", args)
	}
}
