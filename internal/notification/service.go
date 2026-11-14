package notification

import (
	"context"
	"strconv"

	"gostream/internal/outbox"
)

// Repository 是 Service 需要的数据访问能力；GORMRepository 是它的 MySQL 实现。
type Repository interface {
	Create(ctx context.Context, n Notification) error
	List(ctx context.Context, recipientID, before uint64, limit int) ([]Item, error)
	CountUnread(ctx context.Context, recipientID uint64) (int64, error)
	MarkRead(ctx context.Context, recipientID, id uint64) error
	MarkAllRead(ctx context.Context, recipientID, upTo uint64) error
}

// 编译期检查：GORMRepository 必须实现 Repository，方法签名对不上时这里直接编译失败。
var _ Repository = (*GORMRepository)(nil)

// 每页条数：不传时 20 条，最多 50 条。
const (
	defaultLimit = 20
	maxLimit     = 50
)

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

// List 返回 recipientID 的一页通知，新的在前。limit 为 0 时用默认值；
// cursor 是上一页返回的 next_cursor，第一页传空字符串。
func (s *Service) List(ctx context.Context, recipientID uint64, limit int, cursor string) (Page, error) {
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 0 || limit > maxLimit {
		return Page{}, ErrInvalidLimit
	}
	var before uint64
	if cursor != "" {
		id, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil || id == 0 {
			return Page{}, ErrInvalidCursor
		}
		before = id
	}
	// 多查一条：查到了，说明后面还有下一页
	items, err := s.repository.List(ctx, recipientID, before, limit+1)
	if err != nil {
		return Page{}, err
	}
	page := Page{Notifications: items}
	if len(items) > limit {
		page.Notifications = items[:limit]
		page.HasMore = true
		page.NextCursor = strconv.FormatUint(items[limit-1].ID, 10)
	}
	// 没有通知时返回空数组而不是 null，前端不用额外判断。
	if page.Notifications == nil {
		page.Notifications = []Item{}
	}
	return page, nil
}

// UnreadCount 返回 recipientID 的未读通知数。
func (s *Service) UnreadCount(ctx context.Context, recipientID uint64) (int64, error) {
	return s.repository.CountUnread(ctx, recipientID)
}

// MarkRead 把一条通知标成已读；通知不存在或不是 recipientID 的，返回 ErrNotFound。
func (s *Service) MarkRead(ctx context.Context, recipientID, id uint64) error {
	return s.repository.MarkRead(ctx, recipientID, id)
}

// MarkAllRead 把 recipientID 的、id 不大于 upTo 的未读通知都标成已读。
func (s *Service) MarkAllRead(ctx context.Context, recipientID, upTo uint64) error {
	return s.repository.MarkAllRead(ctx, recipientID, upTo)
}
