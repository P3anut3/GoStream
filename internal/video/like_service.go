package video

import (
	"context"
	"errors"
)

type LikeRepository interface {
	LikeVideo(ctx context.Context, userID, videoID uint64) error
	UnLikeVideo(ctx context.Context, userID, videoID uint64) error
	GetLikeStatus(ctx context.Context, userID, videoID uint64) (LikeStatus, error)
}

type LikeService struct {
	repository LikeRepository
}
type LikeStatus struct { //查询点赞数量和当前用户的点赞状态
	LikeCount uint64
	Liked     bool
}

func NewLikeService(repository LikeRepository) *LikeService {
	return &LikeService{
		repository: repository,
	}
}

func (s *LikeService) LikeVideo(ctx context.Context, userID, videoID uint64) error {
	err := s.repository.LikeVideo(ctx, userID, videoID)
	// 业务规则：重复点赞也视为成功
	if errors.Is(err, ErrAlreadyLiked) {
		return nil
	}
	return err
}
func (s *LikeService) UnLikeVideo(ctx context.Context, userID, videoID uint64) error {
	return s.repository.UnLikeVideo(ctx, userID, videoID)
}
func (s *LikeService) GetLikeStatus(ctx context.Context, userID, videoID uint64) (LikeStatus, error) {
	return s.repository.GetLikeStatus(ctx, userID, videoID)
}
