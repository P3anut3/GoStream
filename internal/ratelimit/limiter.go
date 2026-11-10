package ratelimit

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/redis/go-redis/v9"
)

// Rule 是一条限流规则：同一个身份在 Window 时间内最多请求 Limit 次。
type Rule struct {
	Name   string        // 写进计数器的 key 里，区分不同规则，例如 "login"
	Limit  int           // 一个窗口里最多允许多少次
	Window time.Duration // 窗口长度；Redis 按秒计时，至少 1 秒
}

// Decision 是一次限流判断的结果。
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration // 被拒绝时还要等多久：等到这个窗口结束，计数清零
}

// Limiter 判断某个身份在当前窗口里还能不能再请求一次；每次调用都会计数。
type Limiter interface {
	Allow(ctx context.Context, rule Rule, key string) (Decision, error)
}

// decide 根据"这是窗口里的第几次请求"和"窗口还剩多久"给出结果，几种限流器共用。
func decide(rule Rule, count int64, remaining time.Duration) Decision {
	if count <= int64(rule.Limit) {
		return Decision{Allowed: true}
	}
	if remaining <= 0 {
		remaining = rule.Window // 读不到剩余时间时按整个窗口算
	}
	return Decision{RetryAfter: remaining}
}

// RedisLimiter 用 Redis 计数：所有 API 实例共用同一份计数。
type RedisLimiter struct {
	Client *redis.Client
}

// Allow 是固定窗口计数：第一次请求时开始计时，窗口结束时计数器自动过期。
func (l *RedisLimiter) Allow(ctx context.Context, rule Rule, key string) (Decision, error) {
	counter := "gostream:ratelimit:v1:" + rule.Name + ":" + key
	var count *redis.IntCmd
	var ttl *redis.DurationCmd
	// 三条命令放进同一个 MULTI：不会出现"计数加了、过期时间却没设上"的 key，那样这个身份会被永久封禁。
	_, err := l.Client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		count = pipe.Incr(ctx, counter)
		// NX：只在还没有过期时间时设置，窗口从第一次请求开始，后面的请求不会把它往后推。
		pipe.ExpireNX(ctx, counter, rule.Window)
		ttl = pipe.PTTL(ctx, counter)
		return nil
	})
	if err != nil {
		return Decision{}, fmt.Errorf("限流计数失败: %w", err)
	}
	return decide(rule, count.Val(), ttl.Val()), nil
}

// LocalLimiter 在本进程的内存里计数（go-cache），在 Redis 不可用或没有配置 Redis 时使用。
// 每个进程各数各的：部署了 N 个实例时，总上限会放大成 N 倍。
type LocalLimiter struct {
	mu     sync.Mutex
	counts *gocache.Cache
}

func NewLocalLimiter() *LocalLimiter {
	// 每个计数自己带过期时间（窗口结束的时刻）；后台每分钟清理一次已经过期的计数。
	return &LocalLimiter{counts: gocache.New(gocache.NoExpiration, time.Minute)}
}

func (l *LocalLimiter) Allow(_ context.Context, rule Rule, key string) (Decision, error) {
	counter := rule.Name + ":" + key
	// 加锁让"读出计数、加一、写回"成为一步，并发请求不会数漏。
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	count, resetAt := int64(1), now.Add(rule.Window) // 没有计数（或上一个窗口已结束）：开始新窗口
	if value, expiresAt, found := l.counts.GetWithExpiration(counter); found && expiresAt.After(now) {
		count, resetAt = value.(int64)+1, expiresAt // 还在同一个窗口：计数加一，结束时刻不变
	}
	remaining := resetAt.Sub(now)
	l.counts.Set(counter, count, remaining)
	return decide(rule, count, remaining), nil
}

// FailoverLimiter 平时用 Primary（Redis）；Primary 出错时改用 Fallback（单机计数）。
// 这样 Redis 挂了既不会全部放行（失去保护），也不会全部拒绝（把整个网站拖垮）。
type FailoverLimiter struct {
	Primary  Limiter
	Fallback Limiter
}

func (l *FailoverLimiter) Allow(ctx context.Context, rule Rule, key string) (Decision, error) {
	decision, err := l.Primary.Allow(ctx, rule, key)
	if err == nil {
		return decision, nil
	}
	log.Printf("Redis 限流不可用，改用单机计数: %v", err)
	return l.Fallback.Allow(ctx, rule, key)
}
