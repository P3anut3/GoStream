package video

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"gostream/internal/outbox"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

var (
	ErrAlreadyLiked      = errors.New("已经点赞")
	ErrLikeVideoNotFound = errors.New("视频不存在")
)

type SQLLikeRepository struct{ DB *gorm.DB }

// LikeVideo 在同一个事务里写点赞和发件箱事件：要么都写进去，要么都不写。
// 函数里每一条 SQL 都要用 tx；用 r.DB 会走另一个连接，不在这个事务里。
func (r *SQLLikeRepository) LikeVideo(ctx context.Context, userID, videoID uint64) error {
	// 回调返回 nil 就提交，返回 error 就回滚，这个 error 会原样返回给调用方。
	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		info := gorm.WithResult()
		result := tx.Clauses(info).Exec(`INSERT INTO video_likes (user_id, video_id) SELECT ?, id FROM videos WHERE id = ?`, userID, videoID)
		if result.Error != nil {
			var mysqlErr *mysql.MySQLError
			if errors.As(result.Error, &mysqlErr) && mysqlErr.Number == 1062 {
				return ErrAlreadyLiked // 重复点赞：回滚，不会产生第二个事件
			}
			return fmt.Errorf("插入点赞记录失败: %w", result.Error)
		}
		affected, err := info.Result.RowsAffected()
		if err != nil {
			return fmt.Errorf("获取点赞结果失败: %w", err)
		}
		if affected == 0 {
			return ErrLikeVideoNotFound
		}
		// 插入点赞时，外键检查已经锁住了这条视频，提交前它不会被删除，所以一定查得到作者。
		var authorID uint64
		if err := tx.Raw(`SELECT author_id FROM videos WHERE id = ?`, videoID).Row().Scan(&authorID); err != nil {
			return fmt.Errorf("查询视频作者失败: %w", err)
		}
		if authorID == userID {
			return nil // 给自己点赞：点赞照常保存，不通知自己
		}
		return outbox.Add(tx, outbox.TypeLikeCreated, outbox.LikeCreated{ActorID: userID, AuthorID: authorID, VideoID: videoID})
	})
}
func (r *SQLLikeRepository) UnLikeVideo(ctx context.Context, userID, videoID uint64) error {
	result := r.DB.WithContext(ctx).Table("video_likes").Where("user_id = ? AND video_id = ?", userID, videoID).Delete(nil)
	if result.Error != nil {
		return fmt.Errorf("删除点赞记录失败: %w", result.Error)
	}
	return nil
}
func (r *SQLLikeRepository) GetLikeStatus(ctx context.Context, userID, videoID uint64) (LikeStatus, error) {
	var status LikeStatus
	err := r.DB.WithContext(ctx).Raw(`SELECT
 (SELECT COUNT(*) FROM video_likes WHERE video_id = v.id),
 EXISTS(SELECT 1 FROM video_likes WHERE video_id = v.id AND user_id = ?)
 FROM videos AS v WHERE v.id = ?`, userID, videoID).Row().Scan(&status.LikeCount, &status.Liked)
	if errors.Is(err, sql.ErrNoRows) {
		return LikeStatus{}, ErrLikeVideoNotFound
	}
	if err != nil {
		return LikeStatus{}, fmt.Errorf("查询点赞状态失败: %w", err)
	}
	return status, nil
}
