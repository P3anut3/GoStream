package feed

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

type GORMRepository struct {
	DB *gorm.DB
}

func (r *GORMRepository) ListLatest(ctx context.Context, position *Position, limit int) ([]FeedItem, error) {
	items := make([]FeedItem, 0)
	query := r.DB.WithContext(ctx).Table("videos").
		Select("id,author_id,title,play_url,cover_url,created_at").
		Order("created_at desc").
		Order("id desc").
		Limit(limit)

	if position != nil {
		query = query.Where("(created_at <?) OR (created_at = ? AND id < ?)", position.CreatedAt, position.CreatedAt, position.ID)
	}

	if err := query.Find(&items).Error; err != nil {
		return nil, fmt.Errorf("查询最新视频失败: %w", err)
	}
	return items, nil
}

// LoadMetadata 用两次批量查询补全整批作品，避免逐条查询产生 N+1。
func (r *GORMRepository) LoadMetadata(ctx context.Context, videoIDs, authorIDs []uint64, viewerID uint64) (Metadata, error) {
	result := Metadata{Authors: make(map[uint64]string), Likes: make(map[uint64]LikeSummary)}
	if len(videoIDs) == 0 {
		return result, nil
	}
	var authors []struct {
		ID       uint64
		Username string
	}
	if len(authorIDs) > 0 {
		if err := r.DB.WithContext(ctx).Table("users").Select("id,username").Where("id IN ?", authorIDs).Find(&authors).Error; err != nil {
			return Metadata{}, fmt.Errorf("批量查询作者失败: %w", err)
		}
	}
	for _, author := range authors {
		result.Authors[author.ID] = author.Username
	}
	var summaries []LikeSummary
	err := r.DB.WithContext(ctx).Raw(`SELECT video_id, COUNT(*) AS like_count,
		MAX(CASE WHEN user_id = ? THEN 1 ELSE 0 END) AS liked
		FROM video_likes WHERE video_id IN ? GROUP BY video_id`, viewerID, videoIDs).Scan(&summaries).Error
	if err != nil {
		return Metadata{}, fmt.Errorf("批量查询点赞失败: %w", err)
	}
	for _, summary := range summaries {
		result.Likes[summary.VideoID] = summary
	}
	return result, nil
}
