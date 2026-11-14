package notification

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// GORMRepository 只负责读写 notifications 表，并把数据库错误翻译成业务结果。
type GORMRepository struct {
	DB *gorm.DB
}

// listQuery 一次查出通知和展示要用的用户名、视频标题、评论内容（JOIN），不用一条条再查（避免 N+1）。
// 评论用 LEFT JOIN：评论被删除后通知还在，comment_content 是 NULL。
// read_at 不为 NULL 就是已读；read 是 MySQL 的保留字，所以列名叫 is_read。
const listQuery = `SELECT n.id, n.event_type, n.actor_id, u.username AS actor_name,
	n.video_id, v.title AS video_title, n.comment_id, c.content AS comment_content,
	n.created_at, n.read_at IS NOT NULL AS is_read
	FROM notifications n
	JOIN users u ON u.id = n.actor_id
	JOIN videos v ON v.id = n.video_id
	LEFT JOIN video_comments c ON c.id = n.comment_id
	WHERE n.recipient_id = ?`

// List 返回 recipientID 的通知， 按 id 从新到旧， 最多 limit 条
// before 是游标：大于 0 时 只需要 id 比它小的（更早的）；为 0 时从最新的开始。
func (r *GORMRepository) List(ctx context.Context, recipientID, before uint64, limit int) ([]Item, error) {
	query, args := listQuery, []any{recipientID}
	if before > 0 {
		query += ` AND n.id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY n.id DESC LIMIT ?`
	args = append(args, limit)
	var items []Item
	if err := r.DB.WithContext(ctx).Raw(query, args...).Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("查询通知失败: %w", err)
	}
	return items, nil
}

// CountUnread 数 recipientID 的未读通知。009 迁移加了 (recipient_id,read_at) 索引，只读索引就能数完
func (r *GORMRepository) CountUnread(ctx context.Context, recipientID uint64) (int64, error) {
	var count int64
	err := r.DB.WithContext(ctx).Raw(`SELECT COUNT(*) FROM notifications
		WHERE recipient_id = ? AND read_at IS NULL`, recipientID).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("查询未读数失败: %w", err)
	}
	return count, nil
}

// MarkRead 把一条通知标成已读。已经读过的不再改（保留第一次读的时间），也算成功。
// 通知不存在、或者不是 recipientID 的，都返回 ErrNotFound：故意不区分，别人没法试探哪些 ID 存在。
func (r *GORMRepository) MarkRead(ctx context.Context, recipientID, id uint64) error {
	result := r.DB.WithContext(ctx).Exec(`UPDATE notifications SET read_at = NOW()
		WHERE id = ? AND recipient_id = ? AND read_at IS NULL`, id, recipientID)
	if result.Error != nil {
		return fmt.Errorf("标记已读失败: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return nil
	}
	// 一行都没改到：要么已经读过，要么不是这个人的通知，再查一次分清楚。
	var exists bool
	err := r.DB.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM notifications
		WHERE id = ? AND recipient_id = ?)`, id, recipientID).Scan(&exists).Error
	if err != nil {
		return fmt.Errorf("查询通知失败: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// MarkAllRead 把 recipientID 的、id 不大于 upTo 的未读通知都标成已读。
// upTo 是页面上最新那条通知的 id：之后才到的通知用户还没看到，不能一起标掉。
func (r *GORMRepository) MarkAllRead(ctx context.Context, recipientID, upTo uint64) error {
	err := r.DB.WithContext(ctx).Exec(`UPDATE notifications SET read_at = NOW()
		WHERE recipient_id = ? AND id <= ? AND read_at IS NULL`, recipientID, upTo).Error
	if err != nil {
		return fmt.Errorf("全部标记已读失败: %w", err)
	}
	return nil
}

// Create 保存一条通知。下面两种情况什么都不写，也算处理完了，返回 nil：
//   - 视频已经删除：INSERT … SELECT 查不到视频，插入 0 行，没有要通知的；
//   - 这条通知已经有了：同一条消息被投递了两次，第二次撞上唯一键（错误 1062）。
//
// 接收者或点赞的人不存在（外键检查失败，错误 1452）说明事件本身有问题，返回 ErrInvalidEvent。
// 其他错误（比如数据库连不上）原样返回，调用方可以稍后重试。
func (r *GORMRepository) Create(ctx context.Context, n Notification) error {
	err := r.DB.WithContext(ctx).Exec(`INSERT INTO notifications
		(recipient_id, actor_id, event_id, event_type, video_id, comment_id, created_at)
		SELECT ?, ?, ?, ?, id, ?, ? FROM videos WHERE id = ?`,
		n.RecipientID, n.ActorID, n.EventID, n.EventType, n.CommentID, n.CreatedAt, n.VideoID).Error
	if err == nil {
		return nil
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1062: // 唯一键冲突：这条通知已经写过了
			return nil
		case 1452: // 外键检查失败：接收者或点赞的人不存在
			return fmt.Errorf("%w: 事件 %d 引用的用户不存在", ErrInvalidEvent, n.EventID)
		}
	}
	return fmt.Errorf("写入通知失败: %w", err)
}
