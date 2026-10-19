package video

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gostream/internal/auth"
)

func TestPublishTransaction(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(123, secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, videoStatus, coverStatus, fail string
		cover                                bool
		want                                 int
	}{
		{name: "default_cover_commits", videoStatus: "pending", want: 201},
		{name: "uploaded_cover_commits_both", videoStatus: "pending", coverStatus: "pending", cover: true, want: 201},
		{name: "published_file_reuse_allowed", videoStatus: "published", want: 201},
		{name: "deleting_video_rejected", videoStatus: "deleting", want: 409},
		{name: "deleting_cover_rejected", videoStatus: "pending", coverStatus: "deleting", cover: true, want: 409},
		{name: "video_lookup_error_is_500", fail: "video_query", want: 500},
		{name: "cover_lookup_error_is_500", videoStatus: "pending", cover: true, fail: "cover_query", want: 500},
		{name: "insert_failure_rolls_back", videoStatus: "pending", fail: "insert", want: 500},
		{name: "video_status_failure_rolls_back_insert", videoStatus: "pending", fail: "video_update", want: 500},
		{name: "cover_status_failure_rolls_back_video_status_and_insert", videoStatus: "pending", coverStatus: "pending", cover: true, fail: "cover_update", want: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &publishTransactionStore{t: t, videoStatus: tc.videoStatus, coverStatus: tc.coverStatus, fail: tc.fail}
			db := sql.OpenDB(store)
			t.Cleanup(func() { db.Close() })
			handler := &Handler{DB: db}
			h := server.New()
			h.POST("/videos", auth.JWTAuth(secret), handler.Publish)
			coverURL := "/cover.svg"
			if tc.cover {
				coverURL = "/media/covers/cover.png"
			}
			body, err := json.Marshal(map[string]string{"title": "transaction test", "play_url": "/media/videos/video.mp4", "cover_url": coverURL})
			if err != nil {
				t.Fatal(err)
			}
			resp := ut.PerformRequest(h.Engine, http.MethodPost, "/videos", &ut.Body{Body: bytes.NewReader(body), Len: len(body)},
				ut.Header{Key: "Content-Type", Value: "application/json"}, ut.Header{Key: "Authorization", Value: "Bearer " + token}).Result()
			if resp.StatusCode() != tc.want {
				t.Errorf("expected HTTP %d, got %d: %s", tc.want, resp.StatusCode(), resp.Body())
			}
			if tc.want == 201 {
				if !store.committed || store.rolledBack || !store.inserted || !store.videoUpdated || store.coverUpdated != tc.cover {
					t.Error("success must commit the video and all supplied upload statuses")
				}
			} else {
				if store.committed || !store.rolledBack {
					t.Error("failure must rollback without committing partial changes")
				}
			}
			if strings.Contains(string(resp.Body()), "private storage failure") {
				t.Error("response must not expose internal database errors")
			}
		})
	}
}

// A transactional driver injects failures at each stage and observes Commit/Rollback.
// Actual MySQL persistence is checked separately by TestDeleteIntegration.
type publishTransactionStore struct {
	t                                                                   *testing.T
	videoStatus, coverStatus, fail                                      string
	active, inserted, videoUpdated, coverUpdated, committed, rolledBack bool
}

func (s *publishTransactionStore) Connect(context.Context) (driver.Conn, error) {
	return &publishTransactionConn{s: s}, nil
}
func (*publishTransactionStore) Driver() driver.Driver { return likeRepositoryDriver{} }

type publishTransactionConn struct{ s *publishTransactionStore }

func (*publishTransactionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (*publishTransactionConn) Close() error                { return nil }
func (c *publishTransactionConn) Begin() (driver.Tx, error) { c.s.active = true; return c, nil }
func (c *publishTransactionConn) Commit() error             { c.s.committed = true; c.s.active = false; return nil }
func (c *publishTransactionConn) Rollback() error {
	c.s.rolledBack = true
	c.s.active = false
	return nil
}
func (c *publishTransactionConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	if !s.active {
		s.t.Error("lookup must execute inside transaction")
	}
	q := strings.Join(strings.Fields(query), " ")
	if q != "SELECT id, status FROM uploads WHERE saved_name = ? AND user_id = ? AND file_type = ? FOR UPDATE" {
		s.t.Errorf("expected ownership lookup with row lock, got %s", q)
	}
	if len(args) != 3 || args[1].Value != int64(123) {
		return nil, errors.New("incorrect identity arguments")
	}
	cover := args[2].Value == "cover"
	id, status, name, failure := int64(11), s.videoStatus, "video.mp4", "video_query"
	if cover {
		id, status, name, failure = 22, s.coverStatus, "cover.png", "cover_query"
	}
	if args[0].Value != name {
		s.t.Error("lookup must use supplied media filename")
	}
	if s.fail == failure {
		return nil, errors.New("private storage failure")
	}
	return &publishTransactionRows{values: []driver.Value{id, status}}, nil
}
func (c *publishTransactionConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	if !s.active {
		s.t.Error("write must execute inside transaction")
	}
	q := strings.Join(strings.Fields(query), " ")
	if strings.HasPrefix(q, "INSERT INTO videos") {
		if s.fail == "insert" {
			return nil, errors.New("private storage failure")
		}
		s.inserted = true
		return publishTransactionResult{}, nil
	}
	if q != "UPDATE uploads SET status = 'published' WHERE id = ?" || len(args) != 1 {
		return nil, errors.New("unexpected upload status update")
	}
	if !s.inserted {
		s.t.Error("test expects video insertion before status updates")
	}
	switch args[0].Value {
	case int64(11):
		if s.fail == "video_update" {
			return nil, errors.New("private storage failure")
		}
		s.videoUpdated = true
	case int64(22):
		if s.fail == "cover_update" {
			return nil, errors.New("private storage failure")
		}
		s.coverUpdated = true
	default:
		return nil, errors.New("incorrect upload ID")
	}
	return driver.RowsAffected(1), nil
}

type publishTransactionResult struct{}

func (publishTransactionResult) LastInsertId() (int64, error) { return 99, nil }
func (publishTransactionResult) RowsAffected() (int64, error) { return 1, nil }

type publishTransactionRows struct {
	values []driver.Value
	done   bool
}

func (*publishTransactionRows) Columns() []string { return []string{"id", "status"} }
func (*publishTransactionRows) Close() error      { return nil }
func (r *publishTransactionRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	copy(dest, r.values)
	r.done = true
	return nil
}
