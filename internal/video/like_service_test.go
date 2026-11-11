package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"gostream/internal/auth"

	"github.com/gin-gonic/gin"
	ut "gostream/internal/testutil"
)

type likeRepositoryFunc func(context.Context, uint64, uint64) error

func (f likeRepositoryFunc) LikeVideo(ctx context.Context, userID, videoID uint64) error {
	return f(ctx, userID, videoID)
}

func (f likeRepositoryFunc) UnLikeVideo(context.Context, uint64, uint64) error {
	return errors.New("like test must not call UnLikeVideo")
}

func (f likeRepositoryFunc) GetLikeStatus(context.Context, uint64, uint64) (LikeStatus, error) {
	return LikeStatus{}, errors.New("like test must not query status")
}

func TestLikeServiceLikeVideo(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name                       string
		repositoryError, wantError error
	}{
		{name: "first_like_succeeds"},
		{name: "duplicate_is_success", repositoryError: ErrAlreadyLiked},
		{name: "wrapped_duplicate_is_success", repositoryError: fmt.Errorf("repository: %w", ErrAlreadyLiked)},
		{name: "missing_video_preserved", repositoryError: ErrLikeVideoNotFound, wantError: ErrLikeVideoNotFound},
		{name: "database_error_preserved", repositoryError: failure, wantError: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := likeRepositoryFunc(func(gotContext context.Context, userID, videoID uint64) error {
				calls++
				if gotContext != ctx || userID != 12 || videoID != 34 {
					t.Error("context and identity must reach repository unchanged")
				}
				return tc.repositoryError
			})
			if err := NewLikeService(repo).LikeVideo(ctx, 12, 34); !errors.Is(err, tc.wantError) {
				t.Fatalf("expected %v, got %v", tc.wantError, err)
			}
			if calls != 1 {
				t.Errorf("expected one repository call, got %d", calls)
			}
		})
	}
}

func TestLikeServiceHTTPResponse(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(12, "", secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name            string
		repositoryError error
		wantStatus      int
		wantMessage     string
	}{
		{name: "first_like", wantStatus: 204},
		{name: "duplicate_like", repositoryError: ErrAlreadyLiked, wantStatus: 204},
		{name: "missing_video", repositoryError: ErrLikeVideoNotFound, wantStatus: 404, wantMessage: "视频不存在"},
		{name: "database_error_is_safe", repositoryError: errors.New("private database details"), wantStatus: 500, wantMessage: "点赞失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repo := likeRepositoryFunc(func(_ context.Context, userID, videoID uint64) error {
				calls++
				if userID != 12 || videoID != 34 {
					t.Error("expected JWT identity 12 and path video ID 34")
				}
				return tc.repositoryError
			})
			handler := &Handler{LikeService: NewLikeService(repo)}
			h := gin.New()
			h.PUT("/videos/:id/like", auth.JWTAuth(secret, nil), handler.Like)
			resp := ut.PerformRequest(h, http.MethodPut, "/videos/34/like", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token}).Result()
			if resp.StatusCode() != tc.wantStatus {
				t.Fatalf("expected %d, got %d", tc.wantStatus, resp.StatusCode())
			}
			if calls != 1 {
				t.Errorf("expected one repository call, got %d", calls)
			}
			if tc.wantStatus == 204 {
				if len(resp.Body()) != 0 {
					t.Error("204 must have no body")
				}
				return
			}
			var failure struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(resp.Body(), &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Error != tc.wantMessage {
				t.Errorf("expected %q, got %q", tc.wantMessage, failure.Error)
			}
		})
	}
}
