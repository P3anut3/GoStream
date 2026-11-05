package feed

import (
	"cmp"
	"context"
	"encoding/json"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestTimeline(t *testing.T, size int) (*RedisTimeline, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { client.Close() })
	return &RedisTimeline{Client: client, Size: size, TTL: time.Minute}, server
}

// newestFirst orders videos the way the MySQL feed does: created_at DESC, id DESC.
func newestFirst(items []FeedItem) []FeedItem {
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b FeedItem) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	return sorted
}

func feedIDs(items []FeedItem) []uint64 {
	ids := make([]uint64, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}

// readTimeline pages through the timeline the way Service.list does (limit+1 rows per page).
func readTimeline(t *testing.T, timeline *RedisTimeline, limit int) []uint64 {
	t.Helper()
	var ids []uint64
	var position *Position
	for range 100 {
		items, state, err := timeline.Page(context.Background(), position, limit+1)
		if err != nil || state != PageHit {
			t.Fatalf("page after %+v: state %v, err %v", position, state, err)
		}
		more := len(items) > limit
		if more {
			items = items[:limit]
		}
		ids = append(ids, feedIDs(items)...)
		if !more {
			return ids
		}
		last := items[len(items)-1]
		position = &Position{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	t.Fatal("pagination did not finish")
	return nil
}

func TestRedisTimelinePagesMatchMySQLOrder(t *testing.T) {
	base := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	random := rand.New(rand.NewPCG(1, 2))
	var videos []FeedItem
	for id := uint64(1); id <= 40; id++ {
		// Only four distinct seconds, so many videos share a publish time.
		createdAt := base.Add(time.Duration(random.IntN(4)) * time.Second)
		if id == 9 || id == 10 {
			createdAt = base.Add(10 * time.Second) // as text "9" > "10": only zero padding keeps 10 first
		}
		videos = append(videos, FeedItem{ID: id, AuthorID: id % 3, Title: "video", CreatedAt: createdAt})
	}
	want := feedIDs(newestFirst(videos))
	timeline, _ := newTestTimeline(t, 100)
	if err := timeline.Rebuild(context.Background(), newestFirst(videos)); err != nil {
		t.Fatal(err)
	}
	// Every page size must give the MySQL order with nothing skipped or repeated across pages.
	for _, limit := range []int{1, 2, 3, 7, 50} {
		if got := readTimeline(t, timeline, limit); !slices.Equal(got, want) {
			t.Fatalf("limit %d: timeline order %v, want MySQL order %v", limit, got, want)
		}
	}
}

func TestRedisTimelineStoresPaddedIDsPublicFieldsAndTTL(t *testing.T) {
	timeline, server := newTestTimeline(t, 10)
	video := FeedItem{ID: 9, AuthorID: 7, Title: "缓存 <测试>", PlayURL: "/media/videos/a.mp4", CoverURL: "/cover.svg",
		CreatedAt: time.Date(2026, 11, 5, 12, 0, 1, 0, time.UTC), AuthorName: "private", LikeCount: 5, Liked: true}
	if err := timeline.Rebuild(context.Background(), []FeedItem{video}); err != nil {
		t.Fatal(err)
	}
	if members, err := server.ZMembers(timelineIDsKey); err != nil || !slices.Equal(members, []string{sentinelEnd, "00000000000000000009"}) {
		t.Fatalf("members %v (%v): IDs must be zero padded with the end marker at the bottom", members, err)
	}
	if score, err := server.ZScore(timelineIDsKey, "00000000000000000009"); err != nil || score != float64(video.CreatedAt.UnixMilli()) {
		t.Fatalf("score %v (%v) must be the publish time in milliseconds", score, err)
	}
	var fields map[string]any
	if raw := server.HGet(timelineVideosKey, "00000000000000000009"); json.Unmarshal([]byte(raw), &fields) != nil || len(fields) != 6 {
		t.Fatalf("a cached row must hold only the six public fields: %s", raw)
	}
	for _, key := range []string{timelineIDsKey, timelineVideosKey} {
		if ttl := server.TTL(key); ttl != time.Minute {
			t.Fatalf("%s TTL %v, want 1m", key, ttl)
		}
	}
	items, state, err := timeline.Page(context.Background(), nil, 2)
	if err != nil || state != PageHit || len(items) != 1 {
		t.Fatalf("page: %v, %v, %v", items, state, err)
	}
	got := items[0]
	if got.ID != 9 || got.AuthorID != 7 || got.Title != video.Title || got.PlayURL != video.PlayURL || got.CoverURL != video.CoverURL ||
		!got.CreatedAt.Equal(video.CreatedAt) || got.AuthorName != "" || got.LikeCount != 0 || got.Liked {
		t.Fatalf("the round trip must keep the public fields and drop the personal ones: %+v", got)
	}
}

func TestRedisTimelineWindowFallsBackBelowItsBottom(t *testing.T) {
	base := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	var newest []FeedItem
	for i := range 4 {
		newest = append(newest, FeedItem{ID: uint64(10 - i), CreatedAt: base.Add(-time.Duration(i) * time.Second)})
	}
	// Window 3 plus one extra row: keep 3 videos and mark that older ones exist only in MySQL.
	timeline, server := newTestTimeline(t, 3)
	if err := timeline.Rebuild(context.Background(), newest); err != nil {
		t.Fatal(err)
	}
	if members, _ := server.ZMembers(timelineIDsKey); len(members) != 4 || members[0] != sentinelMore {
		t.Fatalf("expected three videos above the more marker, got %v", members)
	}
	after := func(i int) *Position { return &Position{CreatedAt: newest[i].CreatedAt, ID: newest[i].ID} }
	for _, tc := range []struct {
		name     string
		position *Position
		n        int
		want     PageState
		ids      []uint64
	}{
		{name: "page_inside_the_window", n: 3, want: PageHit, ids: []uint64{10, 9, 8}},
		{name: "page_reaching_past_the_window", n: 4, want: PageBeyond},
		{name: "next_page_crossing_the_bottom", position: after(1), n: 2, want: PageBeyond},
		{name: "cursor_below_the_window", position: after(2), n: 2, want: PageBeyond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, state, err := timeline.Page(context.Background(), tc.position, tc.n)
			if err != nil || state != tc.want || !slices.Equal(feedIDs(items), tc.ids) {
				t.Fatalf("got %v %v (%v), want %v %v", feedIDs(items), state, err, tc.ids, tc.want)
			}
		})
	}
	// Without the extra row everything fits, so reading past the last video is simply the end.
	if err := timeline.Rebuild(context.Background(), newest[:3]); err != nil {
		t.Fatal(err)
	}
	if items, state, err := timeline.Page(context.Background(), nil, 4); err != nil || state != PageHit || !slices.Equal(feedIDs(items), []uint64{10, 9, 8}) {
		t.Fatalf("a complete timeline must answer the last page: %v %v %v", feedIDs(items), state, err)
	}
}

func TestRedisTimelineDetectsMissingOrIncompleteData(t *testing.T) {
	video := FeedItem{ID: 1, CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	cursor := &Position{CreatedAt: video.CreatedAt.Add(time.Second), ID: 2}
	timeline, server := newTestTimeline(t, 10)
	expect := func(name string, want PageState) {
		t.Helper()
		for _, position := range []*Position{nil, cursor} {
			if _, state, err := timeline.Page(context.Background(), position, 3); err != nil || state != want {
				t.Fatalf("%s (cursor page %v): state %v, err %v, want %v", name, position != nil, state, err, want)
			}
		}
	}
	rebuild := func() {
		t.Helper()
		if err := timeline.Rebuild(context.Background(), []FeedItem{video}); err != nil {
			t.Fatal(err)
		}
	}
	expect("never built", PageMissing)
	rebuild()
	expect("rebuilt", PageHit)
	if err := timeline.Invalidate(context.Background()); err != nil || server.Exists(timelineIDsKey) || server.Exists(timelineVideosKey) {
		t.Fatalf("Invalidate must delete both keys: %v", err)
	}
	expect("invalidated", PageMissing)
	rebuild()
	server.Del(timelineVideosKey) // the rows expired a moment before the order
	expect("rows missing", PageMissing)
	rebuild()
	server.Del(timelineIDsKey) // the order expired a moment before the rows
	expect("order missing", PageMissing)
}

func TestRedisTimelineEmptyFeedIsAnAnswer(t *testing.T) {
	timeline, server := newTestTimeline(t, 10)
	if err := timeline.Rebuild(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	items, state, err := timeline.Page(context.Background(), nil, 11)
	if err != nil || state != PageHit || items == nil || len(items) != 0 {
		t.Fatalf("an empty feed must be a cached empty page: %v %v %v", items, state, err)
	}
	if server.Exists(timelineVideosKey) {
		t.Fatal("no rows are stored for an empty feed")
	}
}

func TestRedisTimelineRejectsMissingSizeOrTTL(t *testing.T) {
	for _, tc := range []struct {
		size int
		ttl  time.Duration
	}{{size: 0, ttl: time.Minute}, {size: 10, ttl: 0}} {
		timeline, server := newTestTimeline(t, tc.size)
		timeline.TTL = tc.ttl
		video := FeedItem{ID: 1, CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
		if err := timeline.Rebuild(context.Background(), []FeedItem{video}); err == nil || server.Exists(timelineIDsKey) {
			t.Fatalf("size %d, ttl %v: a timeline that holds nothing or never expires must not be written", tc.size, tc.ttl)
		}
	}
}

func TestRedisTimelineReportsRedisFailures(t *testing.T) {
	timeline, server := newTestTimeline(t, 10)
	video := FeedItem{ID: 1, CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	if err := timeline.Rebuild(context.Background(), []FeedItem{video}); err != nil {
		t.Fatal(err)
	}
	// A corrupted row is an error, so the service falls back to MySQL instead of rebuilding on every request.
	server.HSet(timelineVideosKey, timelineMember(1), "not-json")
	if _, _, err := timeline.Page(context.Background(), nil, 2); err == nil {
		t.Fatal("a corrupted row must be reported")
	}
	server.Close()
	if _, _, err := timeline.Page(context.Background(), nil, 2); err == nil {
		t.Fatal("Page must fail while Redis is down")
	}
	if _, _, err := timeline.Page(context.Background(), &Position{CreatedAt: video.CreatedAt, ID: 1}, 2); err == nil {
		t.Fatal("cursor pages must fail while Redis is down")
	}
	if err := timeline.Rebuild(context.Background(), []FeedItem{video}); err == nil {
		t.Fatal("Rebuild must fail while Redis is down")
	}
	if err := timeline.Invalidate(context.Background()); err == nil {
		t.Fatal("Invalidate must fail while Redis is down")
	}
}
