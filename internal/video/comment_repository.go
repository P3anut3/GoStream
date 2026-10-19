package video

import (
	"context"
	"database/sql"
)

type SQLCommentRepository struct {
	DB *sql.DB
}

// Keep the existing safe error messages while preserving the cause for logging.
type commentStorageError struct {
	message string
	cause   error
}

func (e *commentStorageError) Error() string { return e.message + ": " + e.cause.Error() }
func (e *commentStorageError) Unwrap() error { return e.cause }

func (r *SQLCommentRepository) CreateComment(ctx context.Context, userID, videoID uint64, content string) (uint64, error) {
	result, err := r.DB.ExecContext(ctx,
		`INSERT INTO video_comments(video_id, user_id, content)
		SELECT id,?,? FROM videos WHERE id = ?`,
		userID, content, videoID,
	)
	if err != nil {
		return 0, &commentStorageError{message: "保存评论失败", cause: err}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, &commentStorageError{message: "获取评论保存结果失败", cause: err}
	}
	if affected == 0 {
		return 0, ErrCommentVideoNotFound
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, &commentStorageError{message: "获取评论 ID 失败", cause: err}
	}
	return uint64(id), nil
}
