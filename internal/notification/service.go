package notification

import (
	"context"

	"gostream/internal/outbox"
)

// Repository 是 Service 需要的数据访问能力；GORMRepository 是它的 MySQL 实现。
type Repository interface {
	Create(ctx context.Context, n Notification) error
}

// 编译期检查：GORMRepository 必须实现 Repository，方法签名对不上时这里直接编译失败。
var _ Repository = (*GORMRepository)(nil)

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

// HandleEvent 处理消息队列送来的一个事件：转换成通知，保存下来。
// 返回 nil 表示处理完了（包括重复投递、视频已经删除）；
// 错误包含 ErrInvalidEvent 表示事件本身有问题，不要重试；
// 其他错误（比如数据库连不上）可以稍后重试同一个事件。
func (s *Service) HandleEvent(ctx context.Context, e outbox.Event) error {
	n, err := FromEvent(e)
	if err != nil {
		return err
	}
	return s.repository.Create(ctx, n)
}
