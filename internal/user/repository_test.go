package user

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

func TestGORMRepositoryCreate(t *testing.T) {
	failure := errors.New("storage unavailable")
	for _, tc := range []struct {
		name                 string
		execError, wantError error
	}{
		{name: "generated_id_returned"},
		{name: "duplicate_username", execError: &mysql.MySQLError{Number: 1062}, wantError: ErrUsernameExists},
		{name: "wrapped_duplicate", execError: fmt.Errorf("driver: %w", &mysql.MySQLError{Number: 1062}), wantError: ErrUsernameExists},
		{name: "database_failure_preserved", execError: failure, wantError: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			pool := sql.OpenDB(userRepositoryConnector{exec: func(got context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
				calls++
				if got != ctx {
					t.Error("request context must reach database")
				}
				query = strings.ReplaceAll(strings.Join(strings.Fields(query), " "), "`", "")
				if query != "INSERT INTO users (username,password_hash) VALUES (?,?)" {
					t.Errorf("unexpected insert: %s", query)
				}
				if len(args) != 2 || args[0].Value != "learner" || args[1].Value != "test-hash" {
					t.Error("repository must persist username and already-hashed password")
				}
				return userRepositoryResult{}, tc.execError
			}})
			defer pool.Close()
			user := User{Username: "learner", PasswordHash: "test-hash"}
			err := (&GORMRepository{DB: database.WrapSQL(pool)}).CreateUser(ctx, &user)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("expected %v, got %v", tc.wantError, err)
			}
			if calls != 1 {
				t.Errorf("expected one insert, got %d", calls)
			}
			if err == nil && user.ID != 77 {
				t.Errorf("expected generated ID 77, got %d", user.ID)
			}
		})
	}
}

type userRepositoryConnector struct {
	exec  func(context.Context, string, []driver.NamedValue) (driver.Result, error)
	query func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
}

func (c userRepositoryConnector) Connect(context.Context) (driver.Conn, error) {
	return userRepositoryConn{c}, nil
}
func (userRepositoryConnector) Driver() driver.Driver { return userRepositoryDriver{} }

type userRepositoryDriver struct{}

func (userRepositoryDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type userRepositoryConn struct{ connector userRepositoryConnector }

func (userRepositoryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (userRepositoryConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected Begin") }
func (userRepositoryConn) Close() error              { return nil }
func (c userRepositoryConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.connector.exec(ctx, query, args)
}

func (c userRepositoryConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.connector.query == nil {
		return nil, errors.New("unexpected user query")
	}
	return c.connector.query(ctx, query, args)
}

func TestGORMRepositoryFindByUsername(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name, username        string
		found                 bool
		queryError, wantError error
	}{
		{name: "found", username: "learner", found: true},
		{name: "not_found", username: "missing", wantError: ErrUserNotFound},
		{name: "database_error_preserved", username: "learner", queryError: failure, wantError: failure},
		{name: "username_is_bound_parameter", username: "' OR 1=1 --", found: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			pool := sql.OpenDB(userRepositoryConnector{query: func(got context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
				calls++
				if got != ctx {
					t.Error("context must reach query")
				}
				query = strings.ReplaceAll(strings.Join(strings.Fields(query), " "), "`", "")
				if query != "SELECT id,username,password_hash FROM users WHERE username = ? LIMIT ?" {
					t.Errorf("unexpected user lookup: %s", query)
				}
				if len(args) != 2 || args[0].Value != tc.username || args[1].Value != int64(1) {
					t.Error("username must be bound as a parameter")
				}
				if tc.queryError != nil {
					return nil, tc.queryError
				}
				rows := &userRepositoryRows{}
				if tc.found {
					rows.values = []driver.Value{int64(77), tc.username, "test-hash"}
				}
				return rows, nil
			}})
			defer pool.Close()
			user, err := (&GORMRepository{DB: database.WrapSQL(pool)}).FindByUsername(ctx, tc.username)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("expected %v, got %v", tc.wantError, err)
			}
			if calls != 1 {
				t.Error("expected one user query")
			}
			if err == nil && (user.ID != 77 || user.Username != tc.username || user.PasswordHash != "test-hash") {
				t.Error("incorrect scanned user")
			}
			if err != nil && user != (User{}) {
				t.Error("failed lookup must not return successful user data")
			}
		})
	}
}

type userRepositoryRows struct {
	values  []driver.Value
	columns []string
	done    bool
}

func (r *userRepositoryRows) Columns() []string {
	if r.columns != nil {
		return r.columns
	}
	return []string{"id", "username", "password_hash"}
}
func (*userRepositoryRows) Close() error { return nil }
func (r *userRepositoryRows) Next(dest []driver.Value) error {
	if r.done || r.values == nil {
		return io.EOF
	}
	copy(dest, r.values)
	r.done = true
	return nil
}

type userRepositoryResult struct{}

func (userRepositoryResult) LastInsertId() (int64, error) { return 77, nil }
func (userRepositoryResult) RowsAffected() (int64, error) { return 1, nil }
