package video

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Upload struct {
	ID        uint64    `json:"id"`
	UserID    uint64    `json:"user_id"`
	SavedName string    `json:"saved_name"`
	FileType  string    `json:"file_type"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type SQLUploadRepository struct {
	DB *sql.DB
}

func (r *SQLUploadRepository) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]Upload, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("查询数量必须为 1～1000")
	}
	// 找出“超过保留时间、还没发布、也没有被视频使用”的上传记录。
	rows, err := r.DB.QueryContext(ctx,
		`SELECT
		    u.id, u.user_id, u.saved_name,
		    u.file_type, u.status, u.created_at
		 FROM uploads AS u
		 WHERE u.status = 'pending'
		   AND u.created_at < ?
		   AND (
		       (
		           u.file_type = 'video'
		           AND NOT EXISTS (
		               SELECT 1 FROM videos AS v
		               WHERE v.play_url =
		                   CONCAT('/media/videos/', u.saved_name)
		           )
		       )
		       OR
		       (
		           u.file_type = 'cover'
		           AND NOT EXISTS (
		               SELECT 1 FROM videos AS v
		               WHERE v.cover_url =
		                   CONCAT('/media/covers/', u.saved_name)
		           )
		       )
		   )
		 ORDER BY u.created_at ASC, u.id ASC
		 LIMIT ?`,
		before, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("查询过期上传记录失败: %w", err)
	}
	defer rows.Close()

	uploads := make([]Upload, 0)

	for rows.Next() {
		var upload Upload
		if err := rows.Scan(
			&upload.ID,
			&upload.UserID,
			&upload.SavedName,
			&upload.FileType,
			&upload.Status,
			&upload.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("读取上传记录失败: %w", err)
		}

		uploads = append(uploads, upload)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历上传记录失败: %w", err)
	}
	return uploads, nil
}

func (r *SQLUploadRepository) ClaimExpired(ctx context.Context, uploadID uint64, before time.Time) (Upload, bool, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return Upload{}, false, err
	}
	defer tx.Rollback()
	// 1. 锁定本次准备处理的上传记录
	var upload Upload
	err = tx.QueryRowContext(ctx,
		`SELECT id, user_id, saved_name, file_type, status, created_at
		 FROM uploads
		 WHERE id = ?
		 FOR UPDATE`,
		uploadID,
	).Scan(
		&upload.ID,
		&upload.UserID,
		&upload.SavedName,
		&upload.FileType,
		&upload.Status,
		&upload.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, false, nil
	}
	if err != nil {
		return Upload{}, false, fmt.Errorf("锁定上传记录失败: %w", err)
	}
	// 2、重新确认状态和过期时间(前面先上锁,然后在读取状态)
	if upload.Status != "pending" || !upload.CreatedAt.Before(before) {
		return Upload{}, false, nil
	}
	// 3、根据文件类型，转杯引用检查
	var query, mediaURL string
	switch upload.FileType {
	case "video":
		query = `SELECT EXISTS(SELECT 1 FROM videos WHERE play_url=?)`
		mediaURL = "/media/videos/" + upload.SavedName
	case "cover":
		query = `SELECT EXISTS(SELECT 1 FROM videos WHERE cover_url = ?)`
		mediaURL = "/media/covers/" + upload.SavedName
	default:
		// 不认识的文件类型不参与处理
		return Upload{}, false, nil
	}
	var referenced bool
	if err := tx.QueryRowContext(ctx, query, mediaURL).Scan(&referenced); err != nil {
		return Upload{}, false, fmt.Errorf("检查上传文件引用失败: %w", err)
	}
	if referenced {
		return Upload{}, false, nil
	}
	// 4、标记为正在清理
	_, err = tx.ExecContext(ctx,
		`UPDATE uploads SET status = 'deleting' WHERE id = ?`,
		upload.ID,
	)
	if err != nil {
		return Upload{}, false, fmt.Errorf("标记上传文件清理状态失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Upload{}, false, fmt.Errorf("提交清理事务失败: %w", err)
	}
	upload.Status = "deleting"
	return upload, true, nil
}

func (r *SQLUploadRepository) DeleteClaimed(ctx context.Context, uploadID uint64) error {
	_, err := r.DB.ExecContext(ctx,
		`DELETE FROM uploads
		 WHERE id = ? AND status = 'deleting'`,
		uploadID,
	)
	if err != nil {
		return fmt.Errorf("删除上传记录失败: %w", err)
	}

	return nil
}

func (r *SQLUploadRepository) GetDeleting(ctx context.Context, uploadID uint64) (Upload, bool, error) {
	var upload Upload

	err := r.DB.QueryRowContext(ctx,
		`SELECT id, user_id, saved_name, file_type, status, created_at
		 FROM uploads
		 WHERE id = ? AND status = 'deleting'`,
		uploadID,
	).Scan(
		&upload.ID,
		&upload.UserID,
		&upload.SavedName,
		&upload.FileType,
		&upload.Status,
		&upload.CreatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, false, nil
	}
	if err != nil {
		return Upload{}, false, fmt.Errorf("查询待重试上传记录失败: %w", err)
	}

	return upload, true, nil
}

func (r *SQLUploadRepository) ListDeleting(ctx context.Context, limit int) ([]Upload, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("查询数量必须为 1～1000")
	}

	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, user_id, saved_name, file_type, status, created_at
		 FROM uploads
		 WHERE status = 'deleting'
		 ORDER BY id ASC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("查询待重试记录失败: %w", err)
	}
	defer rows.Close()

	uploads := make([]Upload, 0)
	for rows.Next() {
		var upload Upload
		if err := rows.Scan(
			&upload.ID,
			&upload.UserID,
			&upload.SavedName,
			&upload.FileType,
			&upload.Status,
			&upload.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("读取待重试记录失败: %w", err)
		}
		uploads = append(uploads, upload)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历待重试记录失败: %w", err)
	}

	return uploads, nil
}
