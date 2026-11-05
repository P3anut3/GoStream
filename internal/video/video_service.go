package video

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"

	"golang.org/x/sync/singleflight"
)

// VideoRepository 是 VideoService 需要的数据访问能力；GORMVideoRepository 是它的 MySQL 实现。
type VideoRepository interface {
	FindByID(ctx context.Context, id uint64) (Video, error)
	DeleteOwned(ctx context.Context, id, authorID uint64) error
}

// VideoCache 是可选的缓存；RedisVideoCache 和 LocalVideoCache 都实现它。
type VideoCache interface {
	// Get 有三种结果：未命中 (nil, false, nil)；命中视频 (&video, true, nil)；命中"不存在"标记 (nil, true, nil)。
	Get(ctx context.Context, id uint64) (*Video, bool, error)
	Set(ctx context.Context, video Video) error
	SetMissing(ctx context.Context, id uint64) error
	Delete(ctx context.Context, id uint64) error
}

// loadTimeout 是合并后那一次数据库查询的上限；它不跟随某一个调用方取消。
const loadTimeout = 3 * time.Second

type VideoService struct {
	repository VideoRepository
	cache      VideoCache         // nil 表示没有启用缓存
	loads      singleflight.Group // 合并同一个 ID 的并发回源（只在本进程内有效）
}

func NewVideoService(repository VideoRepository, cache VideoCache) *VideoService {
	return &VideoService{repository: repository, cache: cache}
}

// GetVideo 先查缓存（可能命中"不存在"标记）；未命中时同一个 ID 只回源一次，其余并发请求共享结果。
func (s *VideoService) GetVideo(ctx context.Context, id uint64) (Video, error) {
	if s.cache != nil {
		video, hit, err := s.cache.Get(ctx, id)
		if err != nil {
			log.Printf("读取视频缓存失败，改为查询数据库: %v", err)
		} else if hit {
			if video == nil {
				return Video{}, ErrVideoNotFound // 命中"不存在"标记，不再查库
			}
			return *video, nil
		}
	}
	result := s.loads.DoChan(strconv.FormatUint(id, 10), func() (any, error) {
		// 这次查询由所有等待者共享：不能因为第一个调用方断开，就让其他人一起失败。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()
		return s.load(loadCtx, id)
	})
	select {
	case r := <-result:
		if r.Err != nil {
			return Video{}, r.Err
		}
		return r.Val.(Video), nil
	case <-ctx.Done():
		return Video{}, ctx.Err() // 只有当前调用方放弃等待，共享查询继续进行
	}
}

// load 查询 MySQL 并写回缓存；"不存在"也写一个短期标记，防止缓存穿透。
func (s *VideoService) load(ctx context.Context, id uint64) (Video, error) {
	video, err := s.repository.FindByID(ctx, id)
	if s.cache != nil {
		switch {
		case err == nil:
			// 回写失败不影响本次返回，下次请求会再回源。
			if err := s.cache.Set(ctx, video); err != nil {
				log.Printf("写入视频缓存失败: %v", err)
			}
		case errors.Is(err, ErrVideoNotFound):
			if err := s.cache.SetMissing(ctx, id); err != nil {
				log.Printf("写入不存在标记失败: %v", err)
			}
		}
	}
	return video, err
}

// DeleteVideo 先删数据库记录，成功后再让缓存失效；缓存删除失败时，旧数据最多保留到过期。
func (s *VideoService) DeleteVideo(ctx context.Context, id, authorID uint64) error {
	if err := s.repository.DeleteOwned(ctx, id, authorID); err != nil {
		return err
	}
	if s.cache != nil {
		if err := s.cache.Delete(ctx, id); err != nil {
			log.Printf("删除视频缓存失败，旧缓存会在过期后消失: %v", err)
		}
	}
	return nil
}

// Forget 清掉某个 ID 的缓存（包括"不存在"标记）；发布新视频后调用，让它立刻可见。
func (s *VideoService) Forget(ctx context.Context, id uint64) {
	if s.cache == nil {
		return
	}
	if err := s.cache.Delete(ctx, id); err != nil {
		log.Printf("清除视频缓存失败，过期后会自动恢复: %v", err)
	}
}
