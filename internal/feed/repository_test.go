package feed

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"gostream/internal/database"
)

func TestRepositoryListLatest(t *testing.T) {
	stamp := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name     string
		position *Position
		empty    bool
		failure  error
	}{
		{name: "first_batch"},
		{name: "continue_with_time_and_id", position: &Position{CreatedAt: stamp, ID: 24}},
		{name: "empty_array", empty: true},
		{name: "database_error_preserved", failure: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), feedContextKey{}, "request")
			calls := 0
			pool := sql.OpenDB(feedConnector{query: func(got context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
				calls++
				if got != ctx {
					t.Error("request context did not reach database")
				}
				normalized := strings.ToLower(strings.ReplaceAll(strings.Join(strings.Fields(query), " "), "`", ""))
				if !strings.HasPrefix(strings.ReplaceAll(normalized, " ", ""), "selectid,author_id,title,play_url,cover_url,created_atfromvideos") {
					t.Errorf("查询字段应与 videos 表一致，不包含 titel 或 updated_at；实际 SQL: %s", query)
				}
				if !strings.Contains(normalized, "order by created_at desc,id desc") || strings.Contains(normalized, "offset") {
					t.Errorf("expected deterministic time/ID ordering without offset: %s", query)
				}
				want := []any{int64(11)}
				if tc.position != nil {
					compact := strings.ReplaceAll(normalized, " ", "")
					if !strings.Contains(compact, "(created_at<?)or(created_at=?andid<?)") {
						t.Errorf("missing composite boundary: %s", query)
					}
					want = []any{stamp, stamp, int64(24), int64(11)}
				} else if strings.Contains(normalized, "where") {
					t.Errorf("first page must not filter by a cursor: %s", query)
				}
				values := make([]any, len(args))
				for i := range args {
					values[i] = args[i].Value
				}
				if !reflect.DeepEqual(values, want) {
					t.Errorf("bound arguments = %v, want %v", values, want)
				}
				if tc.failure != nil {
					return nil, tc.failure
				}
				rows := &feedRows{}
				if !tc.empty {
					rows.values = [][]driver.Value{{int64(23), int64(2), "fixture", "/media/videos/a.mp4", "/cover.svg", stamp}}
				}
				return rows, nil
			}})
			defer pool.Close()
			items, err := (&GORMRepository{DB: database.WrapSQL(pool)}).ListLatest(ctx, tc.position, 11)
			if !errors.Is(err, tc.failure) {
				t.Fatalf("error = %v, want %v", err, tc.failure)
			}
			if calls != 1 {
				t.Errorf("database calls = %d, want 1", calls)
			}
			if err != nil {
				return
			}
			if items == nil {
				t.Fatal("successful empty result must be a non-nil slice")
			}
			if tc.empty {
				if len(items) != 0 {
					t.Error("expected empty result")
				}
				return
			}
			if len(items) != 1 || items[0].ID != 23 || items[0].Title != "fixture" || !items[0].CreatedAt.Equal(stamp) {
				t.Errorf("incorrect row mapping: %+v", items)
			}
		})
	}
}

func TestRepositoryCanceledContext(t *testing.T) {
	pool := sql.OpenDB(feedConnector{query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
		t.Error("canceled request must not execute query")
		return &feedRows{}, nil
	}})
	defer pool.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&GORMRepository{DB: database.WrapSQL(pool)}).ListLatest(ctx, nil, 11)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

type feedContextKey struct{}
type feedConnector struct {
	query func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
}

func (c feedConnector) Connect(context.Context) (driver.Conn, error) { return feedConn{c}, nil }
func (feedConnector) Driver() driver.Driver                          { return feedDriver{} }

type feedDriver struct{}

func (feedDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type feedConn struct{ connector feedConnector }

func (feedConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected Prepare") }
func (feedConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected transaction") }
func (feedConn) Close() error                        { return nil }
func (c feedConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.connector.query(ctx, q, args)
}

type feedRows struct {
	values  [][]driver.Value
	columns []string
}

func (r *feedRows) Columns() []string {
	if r.columns != nil {
		return r.columns
	}
	return []string{"id", "author_id", "title", "play_url", "cover_url", "created_at"}
}
func (*feedRows) Close() error { return nil }
func (r *feedRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

func TestRepositoryListFollowing(t *testing.T) {
	stamp := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name     string
		position *Position
		failure  error
	}{
		{name: "first_batch"},
		{name: "continue_with_time_and_id", position: &Position{CreatedAt: stamp, ID: 24}},
		{name: "database_error_preserved", failure: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), feedContextKey{}, "following")
			calls := 0
			pool := sql.OpenDB(feedConnector{query: func(got context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
				calls++
				if got != ctx {
					t.Error("request context did not reach database")
				}
				compact := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(query, "`", ""), " ", ""))
				for _, part := range []string{"fromvideosasv", "joinfollowsasfonf.followee_id=v.author_id", "f.follower_id=?", "orderbyv.created_atdesc,v.iddesc"} {
					if !strings.Contains(compact, part) {
						t.Errorf("following query is missing %q: %s", part, query)
					}
				}
				want := []any{int64(7), int64(11)}
				if tc.position != nil {
					// The outer parentheses keep the cursor's OR inside the follower filter.
					if !strings.Contains(compact, "((v.created_at<?)or(v.created_at=?andv.id<?))") {
						t.Errorf("missing parenthesized composite boundary: %s", query)
					}
					want = []any{int64(7), stamp, stamp, int64(24), int64(11)}
				}
				values := make([]any, len(args))
				for i := range args {
					values[i] = args[i].Value
				}
				if !reflect.DeepEqual(values, want) {
					t.Errorf("bound arguments = %v, want %v", values, want)
				}
				if tc.failure != nil {
					return nil, tc.failure
				}
				return &feedRows{values: [][]driver.Value{{int64(23), int64(2), "fixture", "/media/videos/a.mp4", "/cover.svg", stamp}}}, nil
			}})
			defer pool.Close()
			items, err := (&GORMRepository{DB: database.WrapSQL(pool)}).ListFollowing(ctx, 7, tc.position, 11)
			if !errors.Is(err, tc.failure) {
				t.Fatalf("error = %v, want %v", err, tc.failure)
			}
			if calls != 1 {
				t.Errorf("database calls = %d, want 1", calls)
			}
			if err == nil && (len(items) != 1 || items[0].ID != 23 || items[0].AuthorID != 2) {
				t.Errorf("incorrect row mapping: %+v", items)
			}
		})
	}
}
