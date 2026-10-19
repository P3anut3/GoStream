package video

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
)

var (
	ErrAlreadyLiked      = errors.New("已经点赞")
	ErrLikeVideoNotFound = errors.New("视频不存在")
)

type SQLLikeRepository struct {
	DB *sql.DB
}

func (r *SQLLikeRepository) LikeVideo(ctx context.Context, userID, videoID uint64) error {
	// 视频存在是才插入点赞记录
	//ExecContext 插入 QueryContext 查询 QueryRowContext 读取一行
	result, err := r.DB.ExecContext(ctx,
		`INSERT INTO video_likes (user_id, video_id)
		 SELECT ?, id FROM videos WHERE id = ?`,
		userID, videoID,
	)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return ErrAlreadyLiked
		}
		return fmt.Errorf("插入点赞记录失败：%w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取点赞结果失败: %w", err)
	}
	if affected == 0 {
		return ErrLikeVideoNotFound
	}
	return nil
}
func (r *SQLLikeRepository) UnLikeVideo(ctx context.Context, userID, videoID uint64) error {
	_, err := r.DB.ExecContext(ctx,
		`DELETE FROM video_likes
		 WHERE user_id = ? AND video_id = ?`,
		userID, videoID,
	)
	if err != nil {
		return fmt.Errorf("删除点赞记录失败: %w", err)
	}

	return nil
}
func (r *SQLLikeRepository) GetLikeStatus(ctx context.Context, userID, videoID uint64) (LikeStatus, error) {
	var status LikeStatus
	err := r.DB.QueryRowContext(ctx,
		`SELECT
		    (SELECT COUNT(*)
		     FROM video_likes
		     WHERE video_id = v.id),
		    EXISTS(
		        SELECT 1
		        FROM video_likes
		        WHERE video_id = v.id AND user_id = ?
		    )
		 FROM videos AS v
		 WHERE v.id = ?`,
		userID, videoID,
	).Scan(&status.LikeCount, &status.Liked)

	if errors.Is(err, sql.ErrNoRows) {
		return LikeStatus{}, ErrLikeVideoNotFound
	}
	if err != nil {
		return LikeStatus{}, fmt.Errorf("查询点赞状态失败: %w", err)
	}
	return status, nil
}
