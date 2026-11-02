package video

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

var (
	ErrAlreadyLiked      = errors.New("已经点赞")
	ErrLikeVideoNotFound = errors.New("视频不存在")
)

type SQLLikeRepository struct{ DB *gorm.DB }

func (r *SQLLikeRepository) LikeVideo(ctx context.Context, userID, videoID uint64) error {
	info := gorm.WithResult()
	result := r.DB.WithContext(ctx).Clauses(info).Exec(`INSERT INTO video_likes (user_id, video_id) SELECT ?, id FROM videos WHERE id = ?`, userID, videoID)
	if result.Error != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(result.Error, &mysqlErr) && mysqlErr.Number == 1062 {
			return ErrAlreadyLiked
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
	return nil
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
