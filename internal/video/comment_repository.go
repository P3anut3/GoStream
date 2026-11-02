package video

import (
	"context"
	"errors"
	"gorm.io/gorm"
)

type SQLCommentRepository struct{ DB *gorm.DB }
type commentStorageError struct {
	message string
	cause   error
}

func (e *commentStorageError) Error() string { return e.message + ": " + e.cause.Error() }
func (e *commentStorageError) Unwrap() error { return e.cause }

func (r *SQLCommentRepository) CreateComment(ctx context.Context, userID, videoID uint64, content string) (uint64, error) {
	// WithResult captures the INSERT result directly; no separate connection or
	// SELECT LAST_INSERT_ID is needed. INSERT SELECT remains one atomic statement.
	info := gorm.WithResult()
	result := r.DB.WithContext(ctx).Clauses(info).Exec(`INSERT INTO video_comments(video_id,user_id,content) SELECT id,?,? FROM videos WHERE id = ?`, userID, content, videoID)
	if result.Error != nil {
		return 0, &commentStorageError{"保存评论失败", result.Error}
	}
	affected, err := info.Result.RowsAffected()
	if err != nil {
		return 0, &commentStorageError{"获取评论保存结果失败", err}
	}
	if affected == 0 {
		return 0, ErrCommentVideoNotFound
	}
	id, err := info.Result.LastInsertId()
	if err != nil {
		return 0, &commentStorageError{"获取评论 ID 失败", err}
	}
	return uint64(id), nil
}
func (r *SQLCommentRepository) DeleteComment(ctx context.Context, userID, commentID uint64) (bool, error) {
	result := r.DB.WithContext(ctx).Where("id = ? AND user_id = ?", commentID, userID).Delete(&Comment{})
	if result.Error != nil {
		return false, &commentStorageError{"删除评论失败", result.Error}
	}
	return result.RowsAffected > 0, nil
}
func (r *SQLCommentRepository) ListComments(ctx context.Context, videoID uint64, limit, offset int) ([]Comment, error) {
	var target Video
	err := r.DB.WithContext(ctx).Select("id").Where("id = ?", videoID).Take(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrCommentVideoNotFound
	}
	if err != nil {
		return nil, &commentStorageError{"检查视频失败", err}
	}
	comments := make([]Comment, 0)
	err = r.DB.WithContext(ctx).Where("video_id = ?", videoID).Order("id DESC").Limit(limit).Offset(offset).Find(&comments).Error
	if err != nil {
		return nil, &commentStorageError{"评论列表查询失败", err}
	}
	return comments, nil
}
