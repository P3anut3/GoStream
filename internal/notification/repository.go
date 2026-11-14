package notification

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// GORMRepository 只负责写 notifications 表，并把数据库错误翻译成业务结果。
type GORMRepository struct {
	DB *gorm.DB
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
