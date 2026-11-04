package video

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

var ErrVideoNotFound = errors.New("视频不存在")

// GORMVideoRepository 只负责视频作品记录在 MySQL 中的读取和删除
type GORMVideoRepository struct {
	DB *gorm.DB
}

// FindByID 按 ID 读取一条视频；不存在时返回 ErrVideoNotFound
func (r *GORMVideoRepository) FindByID(ctx context.Context, id uint64) (Video, error) {
	var video Video
	err := r.DB.WithContext(ctx).Where("id = ?", id).Take(&video).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Video{}, ErrVideoNotFound
	}
	if err != nil {
		return Video{}, fmt.Errorf("查询视频失败：%w", err)
	}
	return video, nil
}

// DeleteOwned 只删除 authorID 自己的视频；视频不存在或不属于他时返回 ErrVideoNotFound
func (r *GORMVideoRepository) DeleteOwned(ctx context.Context, id, authorID uint64) error {
	result := r.DB.WithContext(ctx).Where("id = ? AND author_id = ?", id, authorID).Delete(&Video{})
	// 先看 Error：执行失败时 RowsAffected 也是 0，不能把数据库故障当成"不存在"
	if result.Error != nil {
		return fmt.Errorf("删除视频失败：%w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrVideoNotFound
	}
	return nil
}
