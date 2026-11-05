package feed

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakeTimeline is a goroutine-safe stand-in for RedisTimeline. Every page is missing until it is
// rebuilt; after that it answers with state, serving rows (by default the rows it was rebuilt with).
type fakeTimeline struct {
	mu                                 sync.Mutex
	window                             int
	built                              bool
	stayMissing                        bool      // rebuilding does not help: pages stay missing
	state                              PageState // the answer once built
	rows                               []FeedItem
	pageErr, rebuildErr, invalidateErr error
	pages, rebuilds, invalidations     int
	rebuiltWith                        []FeedItem
}

func (f *fakeTimeline) Page(_ context.Context, _ *Position, n int) ([]FeedItem, PageState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages++
	if f.pageErr != nil {
		return nil, PageMissing, f.pageErr
	}
	if !f.built {
		return nil, PageMissing, nil
	}
	if f.state != PageHit {
		return nil, f.state, nil
	}
	items := slices.Clone(f.rows)
	if len(items) > n {
		items = items[:n]
	}
	return items, PageHit, nil
}

func (f *fakeTimeline) Rebuild(_ context.Context, newest []FeedItem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rebuilds++
	f.rebuiltWith = slices.Clone(newest)
	if f.rebuildErr != nil {
		return f.rebuildErr
	}
	if f.rows == nil {
		f.rows = slices.Clone(newest)
	}
	f.built = !f.stayMissing
	return nil
}

func (f *fakeTimeline) Invalidate(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidations++
	return f.invalidateErr
}

func (f *fakeTimeline) Window() int { return f.window }

func (f *fakeTimeline) counts() (pages, rebuilds, invalidations int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pages, f.rebuilds, f.invalidations
}

type latestQuery struct {
	ctx      context.Context
	position *Position
	limit    int
}

// latestRepository records latest-feed queries. A query with limit failLimit fails, and while
// release is set every query waits for it, like a slow database that honours cancellation.
type latestRepository struct {
	mu        sync.Mutex
	rows      []FeedItem
	failLimit int
	release   chan struct{}
	calls     []latestQuery
}

func (r *latestRepository) ListLatest(ctx context.Context, position *Position, limit int) ([]FeedItem, error) {
	r.mu.Lock()
	r.calls = append(r.calls, latestQuery{ctx: ctx, position: position, limit: limit})
	r.mu.Unlock()
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if limit == r.failLimit {
		return nil, errors.New("database unavailable")
	}
	rows := slices.Clone(r.rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (r *latestRepository) ListFollowing(context.Context, uint64, *Position, int) ([]FeedItem, error) {
	return nil, errors.New("the latest feed must not query the following feed")
}

func (r *latestRepository) LoadMetadata(context.Context, []uint64, []uint64, uint64) (Metadata, error) {
	return Metadata{}, nil
}

func (r *latestRepository) queries() []latestQuery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// timelineRows returns videos already in feed order, one second apart.
func timelineRows(ids ...uint64) []FeedItem {
	base := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	items := make([]FeedItem, len(ids))
	for i, id := range ids {
		items[i] = FeedItem{ID: id, AuthorID: 1, CreatedAt: base.Add(-time.Duration(i) * time.Second)}
	}
	return items
}

func TestLatestFeedServedFromTimeline(t *testing.T) {
	repo := &latestRepository{}
	timeline := &fakeTimeline{window: 500, built: true, state: PageHit, rows: timelineRows(30, 29, 28)}
	response, err := NewService(repo, testCursorCodec(t), timeline).ListLatest(context.Background(), 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := feedIDs(response.Videos); !slices.Equal(got, []uint64{30, 29}) || !response.HasMore || response.NextCursor == "" {
		t.Fatalf("unexpected page %v, has more %v", got, response.HasMore)
	}
	if calls := repo.queries(); len(calls) != 0 {
		t.Fatalf("a timeline hit must not query MySQL for videos: %v", calls)
	}
	if _, rebuilds, _ := timeline.counts(); rebuilds != 0 {
		t.Fatal("a hit must not rebuild the timeline")
	}
}

func TestLatestFeedRebuildsMissingTimeline(t *testing.T) {
	repo := &latestRepository{rows: timelineRows(30, 29, 28)}
	timeline := &fakeTimeline{window: 2, state: PageHit}
	response, err := NewService(repo, testCursorCodec(t), timeline).ListLatest(context.Background(), 1, "")
	if err != nil {
		t.Fatal(err)
	}
	// The rebuild reads Window()+1 newest rows from the top: the extra row tells whether older ones exist.
	calls := repo.queries()
	if len(calls) != 1 || calls[0].position != nil || calls[0].limit != 3 {
		t.Fatalf("expected one rebuild query for the newest 3 rows, got %+v", calls)
	}
	if _, rebuilds, _ := timeline.counts(); rebuilds != 1 || !slices.Equal(feedIDs(timeline.rebuiltWith), []uint64{30, 29, 28}) {
		t.Fatalf("the timeline must be rebuilt once from those rows: %v", feedIDs(timeline.rebuiltWith))
	}
	if got := feedIDs(response.Videos); !slices.Equal(got, []uint64{30}) || !response.HasMore {
		t.Fatalf("the page must come from the rebuilt timeline: %v", got)
	}
}

func TestLatestFeedFallsBackToMySQL(t *testing.T) {
	failure := errors.New("redis down")
	for _, tc := range []struct {
		name         string
		timeline     *fakeTimeline
		failLimit    int
		wantRebuilds int
		wantQueries  int // MySQL feed queries in total: a rebuild attempt plus the page itself
	}{
		{name: "page_beyond_the_window", timeline: &fakeTimeline{window: 500, built: true, state: PageBeyond}, wantQueries: 1},
		{name: "redis_read_fails", timeline: &fakeTimeline{window: 500, pageErr: failure}, wantQueries: 1},
		{name: "rebuild_query_fails", timeline: &fakeTimeline{window: 500}, failLimit: 501, wantQueries: 2},
		{name: "rebuild_write_fails", timeline: &fakeTimeline{window: 500, rebuildErr: failure}, wantRebuilds: 1, wantQueries: 2},
		{name: "still_missing_after_rebuild", timeline: &fakeTimeline{window: 500, stayMissing: true}, wantRebuilds: 1, wantQueries: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &latestRepository{rows: timelineRows(30, 29), failLimit: tc.failLimit}
			response, err := NewService(repo, testCursorCodec(t), tc.timeline).ListLatest(context.Background(), 10, "")
			if err != nil || !slices.Equal(feedIDs(response.Videos), []uint64{30, 29}) {
				t.Fatalf("MySQL must still answer: %v, %v", feedIDs(response.Videos), err)
			}
			calls := repo.queries()
			if _, rebuilds, _ := tc.timeline.counts(); rebuilds != tc.wantRebuilds || len(calls) != tc.wantQueries {
				t.Fatalf("rebuilds %d (want %d), MySQL queries %+v (want %d)", rebuilds, tc.wantRebuilds, calls, tc.wantQueries)
			}
			// The fallback is the page itself (limit+1 rows), never another window-sized read.
			if last := calls[len(calls)-1]; last.limit != 11 {
				t.Fatalf("the fallback must query the requested page: %+v", last)
			}
		})
	}
}

func TestFollowingFeedIgnoresTheLatestTimeline(t *testing.T) {
	repo := followingRepository{list: func(context.Context, uint64, *Position, int) ([]FeedItem, error) {
		return timelineRows(5), nil
	}}
	timeline := &fakeTimeline{window: 500, built: true, state: PageHit, rows: timelineRows(30)}
	response, err := NewService(repo, testCursorCodec(t), timeline).ListFollowing(context.Background(), 10, "", 7)
	if err != nil || !slices.Equal(feedIDs(response.Videos), []uint64{5}) {
		t.Fatalf("the following feed must come from its own query: %v, %v", feedIDs(response.Videos), err)
	}
	if pages, rebuilds, _ := timeline.counts(); pages != 0 || rebuilds != 0 {
		t.Fatal("the following feed must not touch the latest timeline")
	}
}

func TestInvalidateLatest(t *testing.T) {
	timeline := &fakeTimeline{window: 500}
	service := NewService(&latestRepository{}, testCursorCodec(t), timeline)
	service.InvalidateLatest(context.Background())
	if _, _, invalidations := timeline.counts(); invalidations != 1 {
		t.Fatalf("expected one invalidation, got %d", invalidations)
	}
	// Failures are only logged: the publish or delete that triggered them already succeeded.
	timeline.invalidateErr = errors.New("redis down")
	service.InvalidateLatest(context.Background())
	NewService(&latestRepository{}, testCursorCodec(t), nil).InvalidateLatest(context.Background())
}

// The concurrency tests run in a synctest bubble: synctest.Wait returns once every other
// goroutine is blocked, so the tests know when all callers are waiting without sleeping.

func TestLatestFeedCoalescesConcurrentRebuilds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		repo := &latestRepository{rows: timelineRows(30, 29, 28), release: release}
		timeline := &fakeTimeline{window: 500, state: PageHit}
		service := NewService(repo, testCursorCodec(t), timeline)
		const callers = 20
		var wg sync.WaitGroup
		pages, errs := make([][]uint64, callers), make([]error, callers)
		for i := range callers {
			wg.Go(func() {
				response, err := service.ListLatest(context.Background(), 2, "")
				pages[i], errs[i] = feedIDs(response.Videos), err
			})
		}
		synctest.Wait() // every caller found the timeline missing and waits for the one rebuild
		close(release)
		wg.Wait()
		if calls := repo.queries(); len(calls) != 1 {
			t.Fatalf("%d concurrent misses must share one rebuild query, got %d", callers, len(calls))
		}
		if _, rebuilds, _ := timeline.counts(); rebuilds != 1 {
			t.Fatalf("expected one rebuild, got %d", rebuilds)
		}
		for i := range callers {
			if errs[i] != nil || !slices.Equal(pages[i], []uint64{30, 29}) {
				t.Fatalf("caller %d got %v, %v", i, pages[i], errs[i])
			}
		}
	})
}

func TestLatestFeedRebuildSurvivesLeaderCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		// Deferred so that a failed check still lets the blocked goroutines finish.
		releaseRebuild := sync.OnceFunc(func() { close(release) })
		defer releaseRebuild()
		repo := &latestRepository{rows: timelineRows(30, 29), release: release}
		timeline := &fakeTimeline{window: 500, state: PageHit}
		service := NewService(repo, testCursorCodec(t), timeline)
		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()
		leaderDone := make(chan error, 1)
		go func() {
			_, err := service.ListLatest(leaderCtx, 1, "")
			leaderDone <- err
		}()
		synctest.Wait() // the leader started the rebuild and waits inside its query
		followerDone := make(chan []uint64, 1)
		go func() {
			response, err := service.ListLatest(context.Background(), 1, "")
			if err != nil {
				t.Errorf("the waiting caller must not fail: %v", err)
			}
			followerDone <- feedIDs(response.Videos)
		}()
		synctest.Wait() // the follower joined the same rebuild
		// The first caller disconnects: only it stops waiting, the shared rebuild keeps going.
		cancelLeader()
		if err := <-leaderDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("the canceled caller must see context.Canceled, got %v", err)
		}
		rebuild := repo.queries()[0]
		if rebuild.limit != 501 || rebuild.ctx.Err() != nil {
			t.Fatalf("the shared rebuild must not be canceled by one caller: %+v", rebuild)
		}
		if deadline, ok := rebuild.ctx.Deadline(); !ok || time.Until(deadline) > rebuildTimeout {
			t.Fatal("the shared rebuild must have its own timeout")
		}
		releaseRebuild()
		if got := <-followerDone; !slices.Equal(got, []uint64{30}) {
			t.Fatalf("the waiting caller must get the page from the rebuilt timeline: %v", got)
		}
		if _, rebuilds, _ := timeline.counts(); rebuilds != 1 {
			t.Fatalf("expected one rebuild, got %d", rebuilds)
		}
	})
}
