package video

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gostream/internal/auth"
)

func TestSQLLikeRepositoryUnLikeVideo(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name     string
		affected int64
		err      error
	}{
		{name: "existing_like_deleted", affected: 1},
		{name: "absent_like_is_success"},
		{name: "database_error_preserved", err: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			db := sql.OpenDB(likeRepositoryConnector{exec: func(got context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
				calls++
				if got != ctx {
					t.Error("request context must reach database")
				}
				if strings.Join(strings.Fields(query), " ") != "DELETE FROM video_likes WHERE user_id = ? AND video_id = ?" {
					t.Errorf("unexpected DELETE: %s", query)
				}
				if len(args) != 2 || args[0].Value != int64(12) || args[1].Value != int64(34) {
					t.Error("DELETE must match both user ID 12 and video ID 34")
				}
				return driver.RowsAffected(tc.affected), tc.err
			}})
			t.Cleanup(func() { db.Close() })
			if err := (&SQLLikeRepository{DB: db}).UnLikeVideo(ctx, 12, 34); !errors.Is(err, tc.err) {
				t.Fatalf("expected cause %v, got %v", tc.err, err)
			}
			if calls != 1 {
				t.Errorf("expected one SQL call, got %d", calls)
			}
		})
	}
}

type unlikeRepositoryFunc func(context.Context, uint64, uint64) error

func (f unlikeRepositoryFunc) GetLikeStatus(context.Context, uint64, uint64) (LikeStatus, error) {
	return LikeStatus{}, errors.New("unlike test must not query status")
}

func (f unlikeRepositoryFunc) LikeVideo(context.Context, uint64, uint64) error {
	return errors.New("unlike test must not call LikeVideo")
}
func (f unlikeRepositoryFunc) UnLikeVideo(ctx context.Context, userID, videoID uint64) error {
	return f(ctx, userID, videoID)
}

func TestLikeServiceUnLikeVideo(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "success"}, {name: "database_error_preserved", err: errors.New("database unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := unlikeRepositoryFunc(func(got context.Context, userID, videoID uint64) error {
				calls++
				if got != ctx || userID != 12 || videoID != 34 {
					t.Error("service must forward context and both IDs")
				}
				return tc.err
			})
			if err := NewLikeService(repo).UnLikeVideo(ctx, 12, 34); !errors.Is(err, tc.err) {
				t.Fatalf("expected %v, got %v", tc.err, err)
			}
			if calls != 1 {
				t.Errorf("expected one repository call, got %d", calls)
			}
		})
	}
}

func TestUnLikeServiceHTTPResponse(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(12, secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "repeated_cancel_is_success", want: 204},
		{name: "database_error_is_safe", err: errors.New("private database details"), want: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repo := unlikeRepositoryFunc(func(_ context.Context, userID, videoID uint64) error {
				calls++
				if userID != 12 || videoID != 34 {
					t.Error("expected JWT identity 12 and path video ID 34")
				}
				return tc.err
			})
			h := server.New()
			handler := &Handler{LikeService: NewLikeService(repo)}
			h.DELETE("/videos/:id/like", auth.JWTAuth(secret), handler.UnLike)
			for i := 0; i < 2; i++ {
				resp := ut.PerformRequest(h.Engine, http.MethodDelete, "/videos/34/like", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token}).Result()
				if resp.StatusCode() != tc.want {
					t.Fatalf("expected %d, got %d", tc.want, resp.StatusCode())
				}
				if tc.want == 204 {
					if len(resp.Body()) != 0 {
						t.Error("204 must have no body")
					}
				} else {
					var failure struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(resp.Body(), &failure); err != nil || failure.Error != "取消点赞失败" {
						t.Fatal("expected safe unlike error message")
					}
				}
			}
			if calls != 2 {
				t.Errorf("expected two repository calls, got %d", calls)
			}
		})
	}
}
