package video

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type Upload struct {
	ID        uint64    `json:"id"`
	UserID    uint64    `json:"user_id"`
	SavedName string    `json:"saved_name"`
	FileType  string    `json:"file_type"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at" gorm:"->"`
}
type SQLUploadRepository struct{ DB *gorm.DB }

func (r *SQLUploadRepository) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]Upload, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("查询数量必须为 1～1000")
	}
	uploads := make([]Upload, 0)
	// Retain the correlated reference checks as SQL while GORM manages scanning.
	err := r.DB.WithContext(ctx).Raw(`SELECT u.id,u.user_id,u.saved_name,u.file_type,u.status,u.created_at
 FROM uploads AS u WHERE u.status='pending' AND u.created_at < ? AND (
 (u.file_type='video' AND NOT EXISTS(SELECT 1 FROM videos v WHERE v.play_url=CONCAT('/media/videos/',u.saved_name)))
 OR (u.file_type='cover' AND NOT EXISTS(SELECT 1 FROM videos v WHERE v.cover_url=CONCAT('/media/covers/',u.saved_name))))
 ORDER BY u.created_at ASC,u.id ASC LIMIT ?`, before, limit).Scan(&uploads).Error
	if err != nil {
		return nil, fmt.Errorf("查询过期上传记录失败: %w", err)
	}
	return uploads, nil
}
func (r *SQLUploadRepository) ClaimExpired(ctx context.Context, uploadID uint64, before time.Time) (Upload, bool, error) {
	tx := r.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return Upload{}, false, fmt.Errorf("开启清理事务失败: %w", tx.Error)
	}
	defer tx.Rollback()
	var upload Upload
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", uploadID).Take(&upload).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Upload{}, false, nil
	}
	if err != nil {
		return Upload{}, false, fmt.Errorf("锁定上传记录失败: %w", err)
	}
	if upload.Status != "pending" || !upload.CreatedAt.Before(before) {
		return Upload{}, false, nil
	}
	var query, url string
	switch upload.FileType {
	case "video":
		query = `SELECT EXISTS(SELECT 1 FROM videos WHERE play_url=?)`
		url = "/media/videos/" + upload.SavedName
	case "cover":
		query = `SELECT EXISTS(SELECT 1 FROM videos WHERE cover_url=?)`
		url = "/media/covers/" + upload.SavedName
	default:
		return Upload{}, false, nil
	}
	var referenced bool
	if err := tx.Raw(query, url).Row().Scan(&referenced); err != nil {
		return Upload{}, false, fmt.Errorf("检查上传文件引用失败: %w", err)
	}
	if referenced {
		return Upload{}, false, nil
	}
	if err := tx.Model(&Upload{}).Where("id = ?", upload.ID).Update("status", "deleting").Error; err != nil {
		return Upload{}, false, fmt.Errorf("标记上传文件清理状态失败: %w", err)
	}
	if err := tx.Commit().Error; err != nil {
		return Upload{}, false, fmt.Errorf("提交清理事务失败: %w", err)
	}
	upload.Status = "deleting"
	return upload, true, nil
}
func (r *SQLUploadRepository) DeleteClaimed(ctx context.Context, uploadID uint64) error {
	err := r.DB.WithContext(ctx).Where("id = ? AND status = ?", uploadID, "deleting").Delete(&Upload{}).Error
	if err != nil {
		return fmt.Errorf("删除上传记录失败: %w", err)
	}
	return nil
}
func (r *SQLUploadRepository) GetDeleting(ctx context.Context, uploadID uint64) (Upload, bool, error) {
	var upload Upload
	err := r.DB.WithContext(ctx).Where("id = ? AND status = ?", uploadID, "deleting").Take(&upload).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
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
	uploads := make([]Upload, 0)
	err := r.DB.WithContext(ctx).Where("status = ?", "deleting").Order("id ASC").Limit(limit).Find(&uploads).Error
	if err != nil {
		return nil, fmt.Errorf("查询待重试记录失败: %w", err)
	}
	return uploads, nil
}
