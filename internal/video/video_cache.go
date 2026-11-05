package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"
)

//RedisVideoCache 把视频的公开字段以 JSON 存进 Redis； 他只是加速层，数据以 MySQL 为准

// missingValue 是数据库里面确认没有这个视频的 标记；正常缓存值是JSON对象，不会和它混淆
const missingValue = "missing"

type RedisVideoCache struct {
	Client     *redis.Client
	TTL        time.Duration // 基础过期时间
	Jitter     time.Duration // 额外随机过期时间上限，避免大量 key 同时过期 (缓存雪崩)
	MissingTTL time.Duration // "不存在"标记的过期时间: 要短，既要防穿透，又不长期挡住新发布的视频
}

// videoKey 带上项目前缀和格式版本；以后缓存的字段变了，换成 v2 就不会读取旧格式
func videoKey(id uint64) string {
	return fmt.Sprintf("gostream:video:v1:%d", id)
}

// Get 有三种结果：未命中(nil,false,nil)；命中视频(&video, true, nil);命中"不存在"标记(nil,true,nil)。
func (c *RedisVideoCache) Get(ctx context.Context, id uint64) (*Video, bool, error) {
	data, err := c.Client.Get(ctx, videoKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("读取视频缓存失败: %w", err)
	}
	if string(data) == missingValue {
		return nil, true, nil
	}
	var video Video
	if err := json.Unmarshal(data, &video); err != nil {
		return nil, false, fmt.Errorf("解析视频缓存失败: %w", err)
	}
	return &video, true, nil
}

// Set 写入缓存，过期时间为 TTL 再加上 [0, Jitter) 之间的随机值。
func (c *RedisVideoCache) Set(ctx context.Context, video Video) error {
	data, err := json.Marshal(video)
	if err != nil {
		return fmt.Errorf("编码视频缓存失败: %w", err)
	}
	ttl := c.TTL
	if c.Jitter > 0 {
		ttl += rand.N(c.Jitter)
	}
	if err := c.Client.Set(ctx, videoKey(video.ID), data, ttl).Err(); err != nil {
		return fmt.Errorf("写入视频缓存失败: %w", err)
	}
	return nil
}

// SetMissing 记住"这个 ID 不存在"，在MissingTTL 内挡住对同一个不存在ID 的重复查库(防缓存穿透)
func (c *RedisVideoCache) SetMissing(ctx context.Context, id uint64) error {
	// 过期时间为 0 在 Redis里表示永不过期：这样的标记会挡住以后用这个 ID 发布的视频，所以干脆不写
	if c.MissingTTL <= 0 {
		return nil
	}
	if err := c.Client.Set(ctx, videoKey(id), missingValue, c.MissingTTL).Err(); err != nil {
		return fmt.Errorf("写入不存在标记失败: %w", err)
	}
	return nil
}

// Delete 让缓存失效；key 本来就不存在也算成功
func (c *RedisVideoCache) Delete(ctx context.Context, id uint64) error {
	if err := c.Client.Del(ctx, videoKey(id)).Err(); err != nil {
		return fmt.Errorf("删除视频缓存失败: %w", err)
	}
	return nil
}
