package video

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
)

// txTestDB is a database/sql driver for repository tests that need transactions and no MySQL.
// Every statement goes to exec or query with its SQL whitespace-normalized. The driver records
// the statements in order, whether each one ran on the connection that holds the transaction,
// and how the transaction ended.
type txTestDB struct {
	ctx       context.Context // every call must carry this context
	exec      func(query string, args []driver.NamedValue) (driver.Result, error)
	query     func(query string, args []driver.NamedValue) (driver.Rows, error)
	beginErr  error
	commitErr error

	mu                           sync.Mutex
	statements                   []txTestStatement
	began, committed, rolledBack int
	wrongContext                 bool
}

type txTestStatement struct {
	sql  string
	inTx bool
}

func normalizeSQL(query string) string { return strings.Join(strings.Fields(query), " ") }

func (d *txTestDB) Connect(context.Context) (driver.Conn, error) { return &txTestConn{db: d}, nil }
func (d *txTestDB) Driver() driver.Driver                        { return txTestDriver{} }

// expect checks the statements, that all of them ran inside the one transaction, and whether
// it was committed (true) or rolled back (false).
func (d *txTestDB) expect(t *testing.T, statements []string, committed bool) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.wrongContext {
		t.Error("the request context must reach every database call")
	}
	var got []string
	for _, statement := range d.statements {
		got = append(got, statement.sql)
		if !statement.inTx {
			t.Errorf("%q ran outside the transaction", statement.sql)
		}
	}
	if !slices.Equal(got, statements) {
		t.Errorf("statements %q, want %q", got, statements)
	}
	wantCommit, wantRollback := 0, 1
	if committed {
		wantCommit, wantRollback = 1, 0
	}
	if d.began != 1 || d.committed != wantCommit || d.rolledBack != wantRollback {
		t.Errorf("transactions began %d, committed %d, rolled back %d; want 1, %d, %d", d.began, d.committed, d.rolledBack, wantCommit, wantRollback)
	}
}

type txTestDriver struct{}

func (txTestDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

// txTestConn is one pooled connection; inTx is true while it holds the transaction.
type txTestConn struct {
	db   *txTestDB
	inTx bool
}

func (*txTestConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected Prepare") }
func (*txTestConn) Close() error                        { return nil }
func (*txTestConn) Begin() (driver.Tx, error)           { return nil, errors.New("use BeginTx") }

func (c *txTestConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d := c.db
	d.mu.Lock()
	defer d.mu.Unlock()
	d.checkContext(ctx)
	d.began++
	if d.beginErr != nil {
		return nil, d.beginErr
	}
	c.inTx = true
	return txTestTx{c}, nil
}

func (c *txTestConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	query = c.record(ctx, query)
	if c.db.exec == nil {
		return nil, errors.New("unexpected Exec: " + query)
	}
	return c.db.exec(query, args)
}

func (c *txTestConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	query = c.record(ctx, query)
	if c.db.query == nil {
		return nil, errors.New("unexpected Query: " + query)
	}
	return c.db.query(query, args)
}

func (c *txTestConn) record(ctx context.Context, query string) string {
	d := c.db
	d.mu.Lock()
	defer d.mu.Unlock()
	d.checkContext(ctx)
	query = normalizeSQL(query)
	d.statements = append(d.statements, txTestStatement{sql: query, inTx: c.inTx})
	return query
}

func (d *txTestDB) checkContext(ctx context.Context) {
	if d.ctx != nil && ctx != d.ctx {
		d.wrongContext = true
	}
}

type txTestTx struct{ conn *txTestConn }

func (tx txTestTx) Commit() error {
	d := tx.conn.db
	d.mu.Lock()
	defer d.mu.Unlock()
	tx.conn.inTx = false
	d.committed++
	return d.commitErr
}

func (tx txTestTx) Rollback() error {
	d := tx.conn.db
	d.mu.Lock()
	defer d.mu.Unlock()
	tx.conn.inTx = false
	d.rolledBack++
	return nil
}

// txTestRows answers a query with fixed rows.
type txTestRows struct {
	columns []string
	values  [][]driver.Value
}

func (r *txTestRows) Columns() []string { return r.columns }
func (r *txTestRows) Close() error      { return nil }
func (r *txTestRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

// authorRows is the answer to SELECT author_id FROM videos WHERE id = ?.
func authorRows(authorID int64) driver.Rows {
	return &txTestRows{columns: []string{"author_id"}, values: [][]driver.Value{{authorID}}}
}

// insertResult is the result of an INSERT with an auto-increment ID.
type insertResult struct {
	id, rows       int64
	idErr, rowsErr error
}

func (r insertResult) LastInsertId() (int64, error) { return r.id, r.idErr }
func (r insertResult) RowsAffected() (int64, error) { return r.rows, r.rowsErr }

// The statements LikeVideo and CreateComment send, whitespace-normalized.
const (
	likeInsertSQL    = "INSERT INTO video_likes (user_id, video_id) SELECT ?, id FROM videos WHERE id = ?"
	commentInsertSQL = "INSERT INTO video_comments(video_id,user_id,content) SELECT id,?,? FROM videos WHERE id = ?"
	authorQuerySQL   = "SELECT author_id FROM videos WHERE id = ?"
	outboxInsertSQL  = "INSERT INTO outbox_events (event_type, payload) VALUES (?, ?)"
)
