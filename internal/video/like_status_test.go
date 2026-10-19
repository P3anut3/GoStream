package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gostream/internal/auth"
)

type likeStatusRepositoryFunc func(context.Context, uint64, uint64) (LikeStatus, error)

func (f likeStatusRepositoryFunc) LikeVideo(context.Context, uint64, uint64) error {
	return errors.New("query must not insert likes")
}
func (f likeStatusRepositoryFunc) UnLikeVideo(context.Context, uint64, uint64) error {
	return errors.New("query must not delete likes")
}
func (f likeStatusRepositoryFunc) GetLikeStatus(ctx context.Context, userID, videoID uint64) (LikeStatus, error) {
	return f(ctx, userID, videoID)
}

func TestLikeStatusServiceAndHTTP(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(12, secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		status      LikeStatus
		err         error
		wantCode    int
		wantMessage string
	}{
		{name: "liked", status: LikeStatus{LikeCount: 7, Liked: true}, wantCode: 200},
		{name: "not_liked_with_same_total", status: LikeStatus{LikeCount: 7}, wantCode: 200},
		{name: "zero_likes", wantCode: 200},
		{name: "missing_video", err: ErrLikeVideoNotFound, wantCode: 404, wantMessage: "视频不存在"},
		{name: "database_error", err: errors.New("private database details"), wantCode: 500, wantMessage: "查询点赞状态失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := likeStatusRepositoryFunc(func(got context.Context, userID, videoID uint64) (LikeStatus, error) {
				calls++
				if calls == 1 && got != ctx {
					t.Error("service must forward context")
				}
				if userID != 12 || videoID != 34 {
					t.Error("must forward JWT user ID and path video ID")
				}
				return tc.status, tc.err
			})
			service := NewLikeService(repo)
			status, err := service.GetLikeStatus(ctx, 12, 34)
			if !errors.Is(err, tc.err) || status != tc.status {
				t.Fatal("service must preserve result and error")
			}
			h := server.New()
			handler := &Handler{LikeService: service}
			h.GET("/videos/:id/like", auth.JWTAuth(secret), handler.GetLikeStatus)
			resp := ut.PerformRequest(h.Engine, http.MethodGet, "/videos/34/like", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token}).Result()
			if resp.StatusCode() != tc.wantCode {
				t.Fatalf("expected %d, got %d", tc.wantCode, resp.StatusCode())
			}
			if calls != 2 {
				t.Errorf("expected one direct and one HTTP query, got %d", calls)
			}
			if tc.wantCode != 200 {
				var failure struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(resp.Body(), &failure); err != nil || failure.Error != tc.wantMessage {
					t.Fatal("incorrect or unsafe error response")
				}
				return
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(resp.Body(), &result); err != nil {
				t.Fatal(err)
			}
			var count uint64
			var liked bool
			if len(result) != 2 || json.Unmarshal(result["like_count"], &count) != nil || json.Unmarshal(result["liked"], &liked) != nil {
				t.Fatal("expected numeric like_count and boolean liked")
			}
			if count != tc.status.LikeCount || liked != tc.status.Liked {
				t.Fatal("response must preserve count and personal status")
			}
		})
	}
}
