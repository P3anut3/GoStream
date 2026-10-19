package video

import (
	"context"
	"database/sql"
	"errors"
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

func (r *SQLCommentRepository) DeleteComment(ctx context.Context, userID uint64, commentID uint64) (bool, error) {
	result, err := r.DB.ExecContext(ctx,
		`DELETE FROM video_comments WHERE id = ? AND user_id = ?`,
		commentID, userID,
	)
	if err != nil {
		return false, &commentStorageError{message: "删除评论失败", cause: err}
	}
	affected, err := result.RowsAffected()
	if err != nil { // 数据库执行失败
		return false, &commentStorageError{message: "获取删除结果失败", cause: err}
	}
	if affected == 0 { //数据库正常执行，但是没有找到对应的记录
		return false, nil
	}
	return true, nil
}

func (r *SQLCommentRepository) ListComments(ctx context.Context, videoID uint64, limit, offset int) ([]Comment, error) {
	// 1、确认目标视频存在
	var foundID uint64
	err := r.DB.QueryRowContext(ctx,
		`SELECT id FROM videos WHERE id = ?`,
		videoID,
	).Scan(&foundID)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCommentVideoNotFound
	}
	if err != nil {
		return nil, &commentStorageError{message: "检查视频失败", cause: err}
	}
	//2、分页查询该视频的评论
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, video_id, user_id, content, created_at
		 FROM video_comments
		 WHERE video_id = ?
		 ORDER BY id DESC
		 LIMIT ? OFFSET ?`,
		videoID, limit, offset,
	)
	if err != nil {
		return nil, &commentStorageError{
			message: "评论列表查询失败",
			cause:   err,
		}
	}
	defer rows.Close()

	// 3、将每一行转化为 Comment
	comments := make([]Comment, 0)
	for rows.Next() {
		var comment Comment
		if err := rows.Scan(&comment.ID, &comment.VideoID, &comment.UserID, &comment.Content, &comment.CreatedAt); err != nil {
			return nil, &commentStorageError{message: "读取评论失败", cause: err}
		}
		comments = append(comments, comment)
	}

	// 4、检查便利过程是否发生错误
	if err := rows.Err(); err != nil {
		return nil, &commentStorageError{message: "查询评论列表失败", cause: err}
	}
	return comments, nil
}
