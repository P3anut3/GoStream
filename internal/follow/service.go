package follow

import (
	"context"
	"errors"
)

// Repository 是 Service 需要的数据访问能力；GORMRepository 是它的 MySQL 实现。
type Repository interface {
	Follow(ctx context.Context, followerID, followeeID uint64) error
	Unfollow(ctx context.Context, followerID, followeeID uint64) error
	IsFollowing(ctx context.Context, followerID, followeeID uint64) (bool, error)
}

// 编译期检查：GORMRepository 必须实现 Repository，方法签名对不上时这里直接编译失败。
var _ Repository = (*GORMRepository)(nil)

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

// Follow 拒绝关注自己，重复关注视为成功，保证 PUT 可以安全重试
func (s *Service) Follow(ctx context.Context, followerID, followeeID uint64) error {
	if followerID == followeeID {
		return ErrCannotFollowSelf
	}
	err := s.repository.Follow(ctx, followerID, followeeID)
	// 业务规则：已经关注也算成功
	if errors.Is(err, ErrAlreadyFollowing) {
		return nil
	}
	return err
}

// Unfollow 本来没关注，目标不存在都算成功，结果都是"不再关注"。
func (s *Service) Unfollow(ctx context.Context, followerID, followeeID uint64) error {
	return s.repository.Unfollow(ctx, followerID, followeeID)
}

// IsFollowing 目标用户不存在时返回 ErrUserNotFound
func (s *Service) IsFollowing(ctx context.Context, followerID, followeeID uint64) (bool, error) {
	return s.repository.IsFollowing(ctx, followerID, followeeID)
}
