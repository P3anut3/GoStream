package video

import (
	"context"
	"log"
)

// VideoRepository 是 VideoService 需要的数据访问能力； GORMVideoRepository 是它的 MySQL 实现

type VideoRepository interface {
	FindByID(ctx context.Context, id uint64) (Video, error)
	DeleteOwned(ctx context.Context, id, authorID uint64) error
}

// VideoCache 是可选的缓存；RedisVideoCache 是它的 Redis 实现
type VideoCache interface {
	Get(ctx context.Context, id uint64) (Video, bool, error)
	Set(ctx context.Context, v Video) error
	Delete(ctx context.Context, id uint64) error
}

type VideoService struct {
	repository VideoRepository
	cache      VideoCache // nil 表示没有启动缓存
}

func NewVideoService(repository VideoRepository, cache VideoCache) *VideoService {
	return &VideoService{repository: repository, cache: cache}
}

// GetVideo 按 Cache-Aside 读取；先查缓存，未命中再查 MySQL 并写会；缓存出错时降级为 直接查 MySQL
func (s *VideoService) GetVideo(ctx context.Context, id uint64) (Video, error) {
	if s.cache != nil {
		video, hit, err := s.cache.Get(ctx, id)
		if err != nil {
			log.Printf("读取视频缓存失败，改为查询数据库：%v", err)
		} else if hit {
			return video, nil
		}
	}
	video, err := s.repository.FindByID(ctx, id)
	if err != nil {
		return Video{}, err
	}
	if s.cache != nil {
		//回写失败不影响本次返回，下次请求会再回源。
		if err := s.cache.Set(ctx, video); err != nil {
			log.Printf("写入视频缓存失败：%v", err)
		}
	}
	return video, nil
}

// DeleteVideo 先删除数据库记录，成功后再让缓存失效；缓存删除失败时，旧数据最多保留到到期
func (s *VideoService) DeleteVideo(ctx context.Context, id, authorID uint64) error {
	if err := s.repository.DeleteOwned(ctx, id, authorID); err != nil {
		return err
	}
	if s.cache != nil {
		if err := s.cache.Delete(ctx, id); err != nil {
			log.Printf("删除视频缓存失败，旧缓存会在过期后消失：%v", err)
		}
	}
	return nil
}
