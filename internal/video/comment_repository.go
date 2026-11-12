package video

import (
	"context"
	"errors"
	"gostream/internal/outbox"

	"gorm.io/gorm"
)

type SQLCommentRepository struct{ DB *gorm.DB }
type commentStorageError struct {
	message string
	cause   error
}

func (e *commentStorageError) Error() string { return e.message + ": " + e.cause.Error() }
func (e *commentStorageError) Unwrap() error { return e.cause }

// CreateComment 在同一个事务里写评论和发件箱事件，做法和 LikeVideo 相同。
func (r *SQLCommentRepository) CreateComment(ctx context.Context, userID, videoID uint64, content string) (uint64, error) {
	var commentID uint64
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// WithResult captures the INSERT result directly; no separate connection or
		// SELECT LAST_INSERT_ID is needed. INSERT SELECT remains one atomic statement.
		info := gorm.WithResult()
		result := tx.Clauses(info).Exec(`INSERT INTO video_comments(video_id,user_id,content) SELECT id,?,? FROM videos WHERE id = ?`, userID, content, videoID)
		if result.Error != nil {
			return &commentStorageError{"保存评论失败", result.Error}
		}
		affected, err := info.Result.RowsAffected()
		if err != nil {
			return &commentStorageError{"获取评论保存结果失败", err}
		}
		if affected == 0 {
			return ErrCommentVideoNotFound
		}
		id, err := info.Result.LastInsertId()
		if err != nil {
			return &commentStorageError{"获取评论 ID 失败", err}
		}
		commentID = uint64(id)
		var authorID uint64
		if err := tx.Raw(`SELECT author_id FROM videos WHERE id = ?`, videoID).Row().Scan(&authorID); err != nil {
			return &commentStorageError{"查询视频作者失败", err}
		}
		if authorID == userID {
			return nil // 评论自己的视频：不通知自己
		}
		event := outbox.CommentCreated{ActorID: userID, AuthorID: authorID, VideoID: videoID, CommentID: commentID}
		if err := outbox.Add(tx, outbox.TypeCommentCreated, event); err != nil {
			return &commentStorageError{"保存评论失败", err}
		}
		return nil
	})
	if err != nil {
		// 开启或提交事务失败时，Handler 也会显示“保存评论失败”。
		return 0, err
	}
	return commentID, nil
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
