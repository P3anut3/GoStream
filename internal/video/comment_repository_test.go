package video

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"gostream/internal/database"
)

// TestSQLCommentRepositoryCreateComment checks that a comment and its outbox event are written
// in one transaction, with safe messages for every failure.
func TestSQLCommentRepositoryCreateComment(t *testing.T) {
	insertFailure := errors.New("database unavailable")
	rowsFailure := errors.New("affected rows unavailable")
	idFailure := errors.New("insert ID unavailable")
	lookupFailure := errors.New("author lookup failed")
	outboxFailure := errors.New("outbox unavailable")
	commentOnly := []string{commentInsertSQL}
	commentAndAuthor := []string{commentInsertSQL, authorQuerySQL}
	everything := []string{commentInsertSQL, authorQuerySQL, outboxInsertSQL}
	const event = `{"actor_id":12,"author_id":99,"video_id":34,"comment_id":77}`
	for _, tc := range []struct {
		name                     string
		insert                   insertResult
		insertError              error
		authorID                 int64
		authorError, outboxError error
		wantID                   uint64
		wantError                error
		wantMessage              string
		wantStatements           []string
		wantEvent                string
		wantCommit               bool
	}{
		{name: "comment_writes_an_event_for_the_author", insert: insertResult{id: 77, rows: 1}, authorID: 99, wantID: 77,
			wantStatements: everything, wantEvent: event, wantCommit: true},
		{name: "own_video_writes_no_event", insert: insertResult{id: 77, rows: 1}, authorID: 12, wantID: 77, wantStatements: commentAndAuthor, wantCommit: true},
		{name: "video_not_found", insert: insertResult{rows: 0}, wantError: ErrCommentVideoNotFound, wantStatements: commentOnly},
		{name: "insert_failure", insertError: insertFailure, wantError: insertFailure, wantMessage: "保存评论失败", wantStatements: commentOnly},
		{name: "rows_failure", insert: insertResult{rowsErr: rowsFailure}, wantError: rowsFailure, wantMessage: "获取评论保存结果失败", wantStatements: commentOnly},
		{name: "id_failure", insert: insertResult{rows: 1, idErr: idFailure}, wantError: idFailure, wantMessage: "获取评论 ID 失败", wantStatements: commentOnly},
		{name: "author_lookup_failure_rolls_back", insert: insertResult{id: 77, rows: 1}, authorError: lookupFailure, wantError: lookupFailure,
			wantMessage: "查询视频作者失败", wantStatements: commentAndAuthor},
		{name: "outbox_failure_rolls_back_the_comment", insert: insertResult{id: 77, rows: 1}, authorID: 99, outboxError: outboxFailure,
			wantError: outboxFailure, wantMessage: "保存评论失败", wantStatements: everything, wantEvent: event},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var gotEvent string
			db := &txTestDB{ctx: ctx,
				exec: func(query string, args []driver.NamedValue) (driver.Result, error) {
					switch query {
					case commentInsertSQL:
						if len(args) != 3 || args[0].Value != int64(12) || args[1].Value != "hello" || args[2].Value != int64(34) {
							t.Errorf("comment arguments must be user 12, the content, then video 34, got %v", args)
						}
						if tc.insertError != nil {
							return nil, tc.insertError
						}
						return tc.insert, nil
					case outboxInsertSQL:
						if len(args) != 2 || args[0].Value != "comment.created" {
							t.Errorf("the event type must be comment.created, got %v", args)
						}
						gotEvent, _ = args[1].Value.(string)
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
			id, err := (&SQLCommentRepository{DB: database.WrapSQL(pool)}).CreateComment(ctx, 12, 34, "hello")
			if !errors.Is(err, tc.wantError) || id != tc.wantID {
				t.Fatalf("got ID %d and error %v, want ID %d and error %v", id, err, tc.wantID, tc.wantError)
			}
			if tc.wantMessage != "" {
				var storageErr *commentStorageError
				if !errors.As(err, &storageErr) || storageErr.message != tc.wantMessage {
					t.Errorf("the handler would show %v, want %q", err, tc.wantMessage)
				}
			}
			db.expect(t, tc.wantStatements, tc.wantCommit)
			if gotEvent != tc.wantEvent {
				t.Errorf("event payload %s, want %s", gotEvent, tc.wantEvent)
			}
		})
	}
}

// TestSQLCommentRepositoryTransactionFailures: failing to begin or commit is reported with ID 0.
func TestSQLCommentRepositoryTransactionFailures(t *testing.T) {
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
			tc.db.exec = func(string, []driver.NamedValue) (driver.Result, error) { return insertResult{id: 77, rows: 1}, nil }
			tc.db.query = func(string, []driver.NamedValue) (driver.Rows, error) { return authorRows(99), nil }
			pool := sql.OpenDB(tc.db)
			t.Cleanup(func() { pool.Close() })
			id, err := (&SQLCommentRepository{DB: database.WrapSQL(pool)}).CreateComment(context.Background(), 12, 34, "hello")
			if !errors.Is(err, tc.want) || id != 0 {
				t.Fatalf("got ID %d and error %v, want ID 0 and %v", id, err, tc.want)
			}
		})
	}
}
