package video

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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

// fakeVideoCache is a goroutine-safe in-memory cache; a nil entry is a "missing" marker.
type fakeVideoCache struct {
	mu                                    sync.Mutex
	items                                 map[uint64]*Video
	getErr, setErr, missingErr, deleteErr error
	gets, sets, missings, deletes         int
}

func newFakeVideoCache() *fakeVideoCache { return &fakeVideoCache{items: map[uint64]*Video{}} }

func (c *fakeVideoCache) Get(_ context.Context, id uint64) (*Video, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	video, ok := c.items[id]
	if !ok || video == nil {
		return nil, ok, nil
	}
	copied := *video
	return &copied, true, nil
}

func (c *fakeVideoCache) Set(_ context.Context, video Video) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sets++
	if c.setErr != nil {
		return c.setErr
	}
	c.items[video.ID] = &video
	return nil
}

func (c *fakeVideoCache) SetMissing(_ context.Context, id uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.missings++
	if c.missingErr != nil {
		return c.missingErr
	}
	c.items[id] = nil
	return nil
}

func (c *fakeVideoCache) Delete(_ context.Context, id uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletes++
	if c.deleteErr != nil {
		return c.deleteErr
	}
	delete(c.items, id)
	return nil
}

func (c *fakeVideoCache) counts() (gets, sets, missings, deletes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets, c.sets, c.missings, c.deletes
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
		// The shared load keeps the caller's values but not its cancellation.
		if got.Value(videoServiceContextKey{}) != "detail" || id != 42 {
			t.Error("context values and video ID must reach the repository")
		}
		return stored, nil
	}}
	cache := newFakeVideoCache()
	service := NewVideoService(repo, cache)
	// A miss loads MySQL once and fills the cache.
	got, err := service.GetVideo(ctx, 42)
	if _, sets, _, _ := cache.counts(); err != nil || got != stored || finds != 1 || sets != 1 {
		t.Fatalf("first read: video %+v, err %v, finds %d, sets %d", got, err, finds, sets)
	}
	// A hit is served from the cache without touching MySQL.
	got, err = service.GetVideo(ctx, 42)
	if err != nil || got != stored || finds != 1 {
		t.Fatalf("a cache hit must not query MySQL: finds %d", finds)
	}
}

func TestVideoServiceGetVideoFailures(t *testing.T) {
	stored := storedTestVideo()
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name                         string
		findErr                      error
		getErr, setErr, missingErr   error
		nilCache                     bool
		wantErr                      error
		wantFinds, wantSets, wantMis int
	}{
		{name: "missing_video_sets_a_marker", findErr: ErrVideoNotFound, wantErr: ErrVideoNotFound, wantFinds: 1, wantMis: 1},
		{name: "database_error_is_not_cached", findErr: failure, wantErr: failure, wantFinds: 1},
		// Redis is only an accelerator: a failing read falls back to MySQL.
		{name: "cache_read_failure_falls_back", getErr: errors.New("redis down"), wantFinds: 1, wantSets: 1},
		{name: "cache_write_failure_still_returns", setErr: errors.New("redis down"), wantFinds: 1, wantSets: 1},
		{name: "marker_write_failure_still_404", findErr: ErrVideoNotFound, missingErr: errors.New("redis down"), wantErr: ErrVideoNotFound, wantFinds: 1, wantMis: 1},
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
			cache.getErr, cache.setErr, cache.missingErr = tc.getErr, tc.setErr, tc.missingErr
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
			_, sets, missings, _ := cache.counts()
			if finds != tc.wantFinds || sets != tc.wantSets || missings != tc.wantMis {
				t.Fatalf("finds %d/%d, sets %d/%d, missing markers %d/%d", finds, tc.wantFinds, sets, tc.wantSets, missings, tc.wantMis)
			}
		})
	}
}

func TestVideoServiceMissingMarkerStopsRepeatedLookups(t *testing.T) {
	finds := 0
	repo := &fakeVideoRepository{find: func(context.Context, uint64) (Video, error) {
		finds++
		return Video{}, ErrVideoNotFound
	}}
	service := NewVideoService(repo, newFakeVideoCache())
	for i := range 3 {
		if _, err := service.GetVideo(context.Background(), 42); !errors.Is(err, ErrVideoNotFound) {
			t.Fatalf("read %d: want ErrVideoNotFound, got %v", i, err)
		}
	}
	if finds != 1 {
		t.Fatalf("the missing marker must stop repeated database lookups, got %d queries", finds)
	}
}

// The concurrency tests run in a synctest bubble: synctest.Wait returns once every other
// goroutine is blocked, so the tests know when all callers are waiting without sleeping.

func TestVideoServiceCoalescesConcurrentMisses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stored := storedTestVideo()
		var finds atomic.Int64
		release := make(chan struct{})
		repo := &fakeVideoRepository{find: func(context.Context, uint64) (Video, error) {
			finds.Add(1)
			<-release
			return stored, nil
		}}
		cache := newFakeVideoCache()
		service := NewVideoService(repo, cache)
		const callers = 20
		var wg sync.WaitGroup
		results, errs := make([]Video, callers), make([]error, callers)
		for i := range callers {
			wg.Go(func() { results[i], errs[i] = service.GetVideo(context.Background(), 42) })
		}
		// Every caller has missed the cache and is waiting for the one in-flight load.
		synctest.Wait()
		close(release)
		wg.Wait()
		if finds.Load() != 1 {
			t.Fatalf("%d concurrent misses must share one database query, got %d", callers, finds.Load())
		}
		for i := range results {
			if errs[i] != nil || results[i] != stored {
				t.Fatalf("caller %d got %+v, %v", i, results[i], errs[i])
			}
		}
		if _, sets, _, _ := cache.counts(); sets != 1 {
			t.Fatalf("the shared load must fill the cache once, got %d writes", sets)
		}
	})
}

func TestVideoServiceSharedLoadSurvivesLeaderCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stored := storedTestVideo()
		var finds atomic.Int64
		entered := make(chan context.Context, 1)
		release := make(chan struct{})
		// Deferred so that a failed check still lets the blocked goroutines finish.
		releaseLoad := sync.OnceFunc(func() { close(release) })
		defer releaseLoad()
		repo := &fakeVideoRepository{find: func(ctx context.Context, _ uint64) (Video, error) {
			finds.Add(1)
			entered <- ctx
			<-release
			return stored, nil
		}}
		service := NewVideoService(repo, newFakeVideoCache())
		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()
		leaderDone := make(chan error, 1)
		go func() {
			_, err := service.GetVideo(leaderCtx, 42)
			leaderDone <- err
		}()
		loadCtx := <-entered
		type outcome struct {
			video Video
			err   error
		}
		followerDone := make(chan outcome, 1)
		go func() {
			video, err := service.GetVideo(context.Background(), 42)
			followerDone <- outcome{video, err}
		}()
		synctest.Wait() // the follower has joined the in-flight load
		// The first caller disconnects: it stops waiting, but the shared load keeps going.
		cancelLeader()
		if err := <-leaderDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("the canceled caller must see context.Canceled, got %v", err)
		}
		if loadCtx.Err() != nil {
			t.Fatal("the shared load must not be canceled by one caller")
		}
		if deadline, ok := loadCtx.Deadline(); !ok || time.Until(deadline) > loadTimeout {
			t.Fatal("the shared load must have its own timeout")
		}
		releaseLoad()
		if got := <-followerDone; got.err != nil || got.video != stored {
			t.Fatalf("the waiting caller must still get the video: %+v, %v", got.video, got.err)
		}
		if finds.Load() != 1 {
			t.Fatalf("expected one shared database query, got %d", finds.Load())
		}
	})
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
			stored := storedTestVideo()
			cache.items[42] = &stored
			cache.deleteErr = tc.cacheErr
			calls := 0
			repo := &fakeVideoRepository{deleteOwned: func(_ context.Context, id, authorID uint64) error {
				calls++
				if id != 42 || authorID != 7 {
					t.Error("video ID and authenticated author must reach the repository")
				}
				// MySQL must be changed before the cache is invalidated.
				if _, _, _, deletes := cache.counts(); deletes != 0 {
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
			if _, _, _, deletes := cache.counts(); calls != 1 || deletes != tc.wantDeletes {
				t.Fatalf("repository calls %d, cache deletes %d (want %d)", calls, deletes, tc.wantDeletes)
			}
		})
	}
}

func TestVideoServiceForgetClearsMissingMarker(t *testing.T) {
	stored := storedTestVideo()
	finds := 0
	repo := &fakeVideoRepository{find: func(context.Context, uint64) (Video, error) {
		finds++
		return stored, nil
	}}
	cache := newFakeVideoCache()
	cache.items[42] = nil // probed before it was published
	service := NewVideoService(repo, cache)
	if _, err := service.GetVideo(context.Background(), 42); !errors.Is(err, ErrVideoNotFound) || finds != 0 {
		t.Fatalf("the marker must answer before publishing: %v, finds %d", err, finds)
	}
	service.Forget(context.Background(), 42)
	if got, err := service.GetVideo(context.Background(), 42); err != nil || got != stored || finds != 1 {
		t.Fatalf("after Forget the published video must be visible: %+v, %v, finds %d", got, err, finds)
	}
	// Forget never panics without a cache or when the cache is down.
	NewVideoService(repo, nil).Forget(context.Background(), 42)
	failing := newFakeVideoCache()
	failing.deleteErr = errors.New("redis down")
	NewVideoService(repo, failing).Forget(context.Background(), 42)
}
