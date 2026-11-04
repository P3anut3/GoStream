package video

import (
	"context"
	"errors"
	"testing"
	"time"
)

type videoServiceContextKey struct{}

// fakeVideoRepository fails the test through an error if an unset method is reached.
type fakeVideoRepository struct {
	find        func(ctx context.Context, id uint64) (Video, error)
	deleteOwned func(ctx context.Context, id, authorID uint64) error
}

func (r *fakeVideoRepository) FindByID(ctx context.Context, id uint64) (Video, error) {
	if r.find == nil {
		return Video{}, errors.New("unexpected FindByID")
	}
	return r.find(ctx, id)
}

func (r *fakeVideoRepository) DeleteOwned(ctx context.Context, id, authorID uint64) error {
	if r.deleteOwned == nil {
		return errors.New("unexpected DeleteOwned")
	}
	return r.deleteOwned(ctx, id, authorID)
}

// fakeVideoCache is an in-memory cache whose operations can be made to fail.
type fakeVideoCache struct {
	items                     map[uint64]Video
	getErr, setErr, deleteErr error
	gets, sets, deletes       int
}

func newFakeVideoCache() *fakeVideoCache { return &fakeVideoCache{items: map[uint64]Video{}} }

func (c *fakeVideoCache) Get(_ context.Context, id uint64) (Video, bool, error) {
	c.gets++
	if c.getErr != nil {
		return Video{}, false, c.getErr
	}
	video, ok := c.items[id]
	return video, ok, nil
}

func (c *fakeVideoCache) Set(_ context.Context, video Video) error {
	c.sets++
	if c.setErr != nil {
		return c.setErr
	}
	c.items[video.ID] = video
	return nil
}

func (c *fakeVideoCache) Delete(_ context.Context, id uint64) error {
	c.deletes++
	if c.deleteErr != nil {
		return c.deleteErr
	}
	delete(c.items, id)
	return nil
}

func storedTestVideo() Video {
	return Video{ID: 42, AuthorID: 7, Title: "缓存测试", PlayURL: "/media/videos/a.mp4", CoverURL: "/cover.svg", CreatedAt: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)}
}

func TestVideoServiceGetVideoCacheAside(t *testing.T) {
	stored := storedTestVideo()
	ctx := context.WithValue(context.Background(), videoServiceContextKey{}, "detail")
	finds := 0
	repo := &fakeVideoRepository{find: func(got context.Context, id uint64) (Video, error) {
		finds++
		if got != ctx || id != 42 {
			t.Error("context and video ID must reach the repository unchanged")
		}
		return stored, nil
	}}
	cache := newFakeVideoCache()
	service := NewVideoService(repo, cache)
	// A miss loads MySQL once and fills the cache.
	got, err := service.GetVideo(ctx, 42)
	if err != nil || got != stored || finds != 1 || cache.sets != 1 {
		t.Fatalf("first read: video %+v, err %v, finds %d, sets %d", got, err, finds, cache.sets)
	}
	// A hit is served from the cache without touching MySQL.
	got, err = service.GetVideo(ctx, 42)
	if err != nil || got != stored || finds != 1 || cache.sets != 1 {
		t.Fatalf("a cache hit must not query MySQL: finds %d, sets %d", finds, cache.sets)
	}
}

func TestVideoServiceGetVideoFailures(t *testing.T) {
	stored := storedTestVideo()
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name               string
		findErr            error
		getErr, setErr     error
		nilCache           bool
		wantErr            error
		wantFinds, wantSet int
	}{
		{name: "missing_video_is_not_cached", findErr: ErrVideoNotFound, wantErr: ErrVideoNotFound, wantFinds: 1},
		{name: "database_error_is_not_cached", findErr: failure, wantErr: failure, wantFinds: 1},
		// Redis is only an accelerator: a failing read falls back to MySQL.
		{name: "cache_read_failure_falls_back", getErr: errors.New("redis down"), wantFinds: 1, wantSet: 1},
		{name: "cache_write_failure_still_returns", setErr: errors.New("redis down"), wantFinds: 1, wantSet: 1},
		{name: "no_cache_reads_mysql", nilCache: true, wantFinds: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finds := 0
			repo := &fakeVideoRepository{find: func(context.Context, uint64) (Video, error) {
				finds++
				if tc.findErr != nil {
					return Video{}, tc.findErr
				}
				return stored, nil
			}}
			cache := newFakeVideoCache()
			cache.getErr, cache.setErr = tc.getErr, tc.setErr
			service := NewVideoService(repo, cache)
			if tc.nilCache {
				service = NewVideoService(repo, nil)
			}
			got, err := service.GetVideo(context.Background(), 42)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && got != stored {
				t.Fatalf("unexpected video %+v", got)
			}
			if finds != tc.wantFinds || cache.sets != tc.wantSet {
				t.Fatalf("finds %d (want %d), sets %d (want %d)", finds, tc.wantFinds, cache.sets, tc.wantSet)
			}
		})
	}
}

func TestVideoServiceDeleteVideoInvalidatesAfterDatabase(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name        string
		deleteErr   error
		cacheErr    error
		nilCache    bool
		wantErr     error
		wantDeletes int
	}{
		{name: "deleted_then_invalidated", wantDeletes: 1},
		{name: "not_owned_keeps_cache", deleteErr: ErrVideoNotFound, wantErr: ErrVideoNotFound},
		{name: "database_error_keeps_cache", deleteErr: failure, wantErr: failure},
		// The record is gone; a failed invalidation only leaves a stale entry until it expires.
		{name: "invalidation_failure_is_still_success", cacheErr: errors.New("redis down"), wantDeletes: 1},
		{name: "no_cache", nilCache: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := newFakeVideoCache()
			cache.items[42] = storedTestVideo()
			cache.deleteErr = tc.cacheErr
			calls := 0
			repo := &fakeVideoRepository{deleteOwned: func(_ context.Context, id, authorID uint64) error {
				calls++
				if id != 42 || authorID != 7 {
					t.Error("video ID and authenticated author must reach the repository")
				}
				// MySQL must be changed before the cache is invalidated.
				if cache.deletes != 0 {
					t.Error("cache was invalidated before the database delete")
				}
				return tc.deleteErr
			}}
			service := NewVideoService(repo, cache)
			if tc.nilCache {
				service = NewVideoService(repo, nil)
			}
			if err := service.DeleteVideo(context.Background(), 42, 7); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if calls != 1 || cache.deletes != tc.wantDeletes {
				t.Fatalf("repository calls %d, cache deletes %d (want %d)", calls, cache.deletes, tc.wantDeletes)
			}
		})
	}
}
