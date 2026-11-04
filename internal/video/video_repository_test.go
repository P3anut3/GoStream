package video

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"gostream/internal/database"
)

func TestGORMVideoRepositoryFindByID(t *testing.T) {
	stamp := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name     string
		rows     [][]driver.Value
		queryErr error
		wantErr  error
	}{
		{name: "found", rows: [][]driver.Value{{int64(42), int64(7), "标题", "/media/videos/a.mp4", "/cover.svg", stamp}}},
		{name: "missing_is_not_found", wantErr: ErrVideoNotFound},
		// A failing database must surface as an error, never as "not found".
		{name: "database_error_is_not_not_found", queryErr: failure, wantErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repo := newFakeVideoRepositoryDB(t, videoRepoConnector{query: func(query string, args []driver.NamedValue) (driver.Rows, error) {
				calls++
				compact := compactVideoSQL(query)
				if !strings.HasPrefix(compact, "select") || !strings.Contains(compact, "fromvideoswhereid=?") {
					t.Errorf("unexpected detail query: %s", query)
				}
				if len(args) == 0 || args[0].Value != int64(42) {
					t.Errorf("video ID 42 must be the first argument, got %v", args)
				}
				if tc.queryErr != nil {
					return nil, tc.queryErr
				}
				return &videoRepoRows{values: tc.rows}, nil
			}})
			video, err := repo.FindByID(context.Background(), 42)
			if !errors.Is(err, tc.wantErr) || (tc.queryErr != nil && errors.Is(err, ErrVideoNotFound)) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if calls != 1 {
				t.Errorf("expected one query, got %d", calls)
			}
			if tc.wantErr == nil && (video.ID != 42 || video.AuthorID != 7 || video.Title != "标题" || !video.CreatedAt.Equal(stamp)) {
				t.Fatalf("incorrect row mapping: %+v", video)
			}
		})
	}
}

func TestGORMVideoRepositoryDeleteOwned(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name     string
		affected int64
		execErr  error
		wantErr  error
	}{
		{name: "deleted", affected: 1},
		{name: "missing_or_not_owned_is_not_found", affected: 0, wantErr: ErrVideoNotFound},
		// A failing database must surface as an error, never as "not found".
		{name: "database_error_is_not_not_found", execErr: failure, wantErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repo := newFakeVideoRepositoryDB(t, videoRepoConnector{exec: func(query string, args []driver.NamedValue) (driver.Result, error) {
				calls++
				// Only the author's own row may match the delete.
				if compactVideoSQL(query) != "deletefromvideoswhereid=?andauthor_id=?" {
					t.Errorf("unexpected delete: %s", query)
				}
				if len(args) != 2 || args[0].Value != int64(42) || args[1].Value != int64(7) {
					t.Errorf("arguments must be video 42 then author 7, got %v", args)
				}
				if tc.execErr != nil {
					return nil, tc.execErr
				}
				return driver.RowsAffected(tc.affected), nil
			}})
			err := repo.DeleteOwned(context.Background(), 42, 7)
			if !errors.Is(err, tc.wantErr) || (tc.execErr != nil && errors.Is(err, ErrVideoNotFound)) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if calls != 1 {
				t.Errorf("expected one statement, got %d", calls)
			}
		})
	}
}

func newFakeVideoRepositoryDB(t *testing.T, connector videoRepoConnector) *GORMVideoRepository {
	t.Helper()
	db := sql.OpenDB(connector)
	t.Cleanup(func() { db.Close() })
	return &GORMVideoRepository{DB: database.WrapSQL(db)}
}

func compactVideoSQL(query string) string {
	return strings.ToLower(strings.NewReplacer("`", "", " ", "", "\n", "", "\t", "").Replace(query))
}

// The fake driver intercepts SQL at the database/sql layer without a real database.
type videoRepoConnector struct {
	exec  func(string, []driver.NamedValue) (driver.Result, error)
	query func(string, []driver.NamedValue) (driver.Rows, error)
}

func (c videoRepoConnector) Connect(context.Context) (driver.Conn, error) {
	return videoRepoConn{connector: c}, nil
}
func (videoRepoConnector) Driver() driver.Driver { return videoRepoDriver{} }

type videoRepoDriver struct{}

func (videoRepoDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type videoRepoConn struct{ connector videoRepoConnector }

func (videoRepoConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (videoRepoConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected Begin") }
func (videoRepoConn) Close() error              { return nil }
func (c videoRepoConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.connector.exec == nil {
		return nil, errors.New("unexpected Exec")
	}
	return c.connector.exec(query, args)
}
func (c videoRepoConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.connector.query == nil {
		return nil, errors.New("unexpected Query")
	}
	return c.connector.query(query, args)
}

type videoRepoRows struct{ values [][]driver.Value }

func (*videoRepoRows) Columns() []string {
	return []string{"id", "author_id", "title", "play_url", "cover_url", "created_at"}
}
func (*videoRepoRows) Close() error { return nil }
func (r *videoRepoRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}
