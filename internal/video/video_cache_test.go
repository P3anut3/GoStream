package video

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestVideoCache(t *testing.T) (*RedisVideoCache, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { client.Close() })
	return &RedisVideoCache{Client: client, TTL: 10 * time.Minute, Jitter: time.Minute}, server
}

func TestRedisVideoCacheRoundTripAndExpiry(t *testing.T) {
	cache, server := newTestVideoCache(t)
	ctx := context.Background()
	if _, hit, err := cache.Get(ctx, 42); err != nil || hit {
		t.Fatalf("a missing key must be a miss, not an error: hit %v, err %v", hit, err)
	}
	video := Video{ID: 42, AuthorID: 7, Title: "缓存 <测试>", PlayURL: "/media/videos/a.mp4", CoverURL: "/cover.svg", CreatedAt: time.Date(2026, 11, 3, 10, 0, 0, 123000000, time.UTC)}
	if err := cache.Set(ctx, video); err != nil {
		t.Fatal(err)
	}
	key := "gostream:video:v1:42"
	if !server.Exists(key) {
		t.Fatalf("expected key %s", key)
	}
	if ttl := server.TTL(key); ttl < 10*time.Minute || ttl >= 11*time.Minute {
		t.Fatalf("TTL %v must be within [10m, 11m)", ttl)
	}
	got, hit, err := cache.Get(ctx, 42)
	if err != nil || !hit || got.ID != 42 || got.AuthorID != 7 || got.Title != video.Title || got.PlayURL != video.PlayURL || got.CoverURL != video.CoverURL || !got.CreatedAt.Equal(video.CreatedAt) {
		t.Fatalf("round trip changed the video: %+v, hit %v, err %v", got, hit, err)
	}
	if err := cache.Delete(ctx, 42); err != nil || server.Exists(key) {
		t.Fatalf("delete must remove the key: %v", err)
	}
	if err := cache.Delete(ctx, 42); err != nil {
		t.Fatalf("deleting a missing key must succeed: %v", err)
	}
}

func TestRedisVideoCacheJitterSpreadsExpiry(t *testing.T) {
	cache, server := newTestVideoCache(t)
	ttls := map[time.Duration]bool{}
	for id := uint64(1); id <= 20; id++ {
		if err := cache.Set(context.Background(), Video{ID: id, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		ttls[server.TTL(fmt.Sprintf("gostream:video:v1:%d", id))] = true
	}
	if len(ttls) < 2 {
		t.Fatal("jitter must spread expirations so entries do not all expire together")
	}
}

func TestRedisVideoCacheErrors(t *testing.T) {
	cache, server := newTestVideoCache(t)
	ctx := context.Background()
	// A corrupted entry is reported as an error so the service falls back to MySQL.
	if err := server.Set("gostream:video:v1:7", "not-json"); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := cache.Get(ctx, 7); err == nil || hit {
		t.Fatal("a corrupted entry must be an error, not a hit")
	}
	// When Redis is down every operation returns an error instead of pretending to succeed.
	server.Close()
	if _, hit, err := cache.Get(ctx, 7); err == nil || hit {
		t.Fatal("Get must fail while Redis is down")
	}
	if err := cache.Set(ctx, Video{ID: 7}); err == nil {
		t.Fatal("Set must fail while Redis is down")
	}
	if err := cache.Delete(ctx, 7); err == nil {
		t.Fatal("Delete must fail while Redis is down")
	}
}
