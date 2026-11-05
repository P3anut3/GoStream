package video

import (
	"context"
	"strconv"
	"time"

	gocache "github.com/patrickmn/go-cache"
)

// LocalVideoCache 在另一层缓存（通常是 Redis）前面加一层几秒钟的进程内缓存，挡住热点视频的重复读取。
// 多个 API 实例的本地缓存互不通知：某个实例删除视频后，其他实例最多再展示 TTL 这么久。
type LocalVideoCache struct {
	local *gocache.Cache
	next  VideoCache
}

func NewLocalVideoCache(next VideoCache, ttl time.Duration) *LocalVideoCache {
	return &LocalVideoCache{local: gocache.New(ttl, 2*ttl), next: next}
}

func localKey(id uint64) string { return strconv.FormatUint(id, 10) }

// Get 先查进程内缓存，没有再查下一层；下一层命中的结果（包括"不存在"标记）也记到本地。
// 返回的指针只读：调用方需要修改时先复制一份。
func (c *LocalVideoCache) Get(ctx context.Context, id uint64) (*Video, bool, error) {
	if value, ok := c.local.Get(localKey(id)); ok {
		return value.(*Video), true, nil
	}
	video, hit, err := c.next.Get(ctx, id)
	if err == nil && hit {
		c.local.SetDefault(localKey(id), video)
	}
	return video, hit, err
}

// Set 先写本地再写下一层；下一层（Redis）失败时，本地仍能在几秒内挡住重复回源。
func (c *LocalVideoCache) Set(ctx context.Context, video Video) error {
	c.local.SetDefault(localKey(video.ID), &video)
	return c.next.Set(ctx, video)
}

// SetMissing 在两层都记住"不存在"；本地用 nil 指针表示这个标记。
func (c *LocalVideoCache) SetMissing(ctx context.Context, id uint64) error {
	c.local.SetDefault(localKey(id), (*Video)(nil))
	return c.next.SetMissing(ctx, id)
}

// Delete 两层都删除；只能清掉本实例的本地缓存。
func (c *LocalVideoCache) Delete(ctx context.Context, id uint64) error {
	c.local.Delete(localKey(id))
	return c.next.Delete(ctx, id)
}
