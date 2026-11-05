package video

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLocalVideoCacheServesRepeatedReadsLocally(t *testing.T) {
	next := newFakeVideoCache()
	stored := storedTestVideo()
	next.items[42] = &stored
	local := NewLocalVideoCache(next, time.Minute)
	for i := 0; i < 3; i++ {
		video, hit, err := local.Get(context.Background(), 42)
		if err != nil || !hit || video == nil || *video != stored {
			t.Fatalf("read %d: %v, hit %v, err %v", i, video, hit, err)
		}
	}
	if gets, _, _, _ := next.counts(); gets != 1 {
		t.Fatalf("repeated reads must be served locally, the next layer was read %d times", gets)
	}
}

func TestLocalVideoCacheRemembersMissingMarker(t *testing.T) {
	next := newFakeVideoCache()
	local := NewLocalVideoCache(next, time.Minute)
	if err := local.SetMissing(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	video, hit, err := local.Get(context.Background(), 42)
	if gets, _, missings, _ := next.counts(); err != nil || !hit || video != nil || gets != 0 || missings != 1 {
		t.Fatalf("the marker must be answered locally and stored below: %v, %v, %v, gets %d, markers %d", video, hit, err, gets, missings)
	}
	// A marker found in the next layer (written by another instance) is remembered locally too.
	next.items[7] = nil
	for range 2 {
		if video, hit, err := local.Get(context.Background(), 7); err != nil || !hit || video != nil {
			t.Fatalf("a marker from the next layer must read as (nil, true, nil): %v, %v, %v", video, hit, err)
		}
	}
	if gets, _, _, _ := next.counts(); gets != 1 {
		t.Fatalf("the marker from the next layer must be kept locally, next layer reads %d", gets)
	}
}

func TestLocalVideoCacheDoesNotRememberPlainMissesOrErrors(t *testing.T) {
	next := newFakeVideoCache()
	local := NewLocalVideoCache(next, time.Minute)
	// A plain miss is not a marker: the next read must ask the next layer again.
	if _, hit, err := local.Get(context.Background(), 42); err != nil || hit {
		t.Fatalf("expected a miss: hit %v, err %v", hit, err)
	}
	next.getErr = errors.New("redis down")
	if _, _, err := local.Get(context.Background(), 42); err == nil {
		t.Fatal("a next-layer error must be returned")
	}
	next.getErr = nil
	local.Get(context.Background(), 42)
	if gets, _, _, _ := next.counts(); gets != 3 {
		t.Fatalf("misses and errors must not be cached locally, next layer reads %d", gets)
	}
}

func TestLocalVideoCacheExpires(t *testing.T) {
	next := newFakeVideoCache()
	stored := storedTestVideo()
	next.items[42] = &stored
	local := NewLocalVideoCache(next, 50*time.Millisecond)
	local.Get(context.Background(), 42)
	time.Sleep(120 * time.Millisecond)
	local.Get(context.Background(), 42)
	if gets, _, _, _ := next.counts(); gets != 2 {
		t.Fatalf("an expired local entry must be reloaded from the next layer, reads %d", gets)
	}
}

func TestLocalVideoCacheKeepsLocalCopyWhenNextFails(t *testing.T) {
	next := newFakeVideoCache()
	next.setErr = errors.New("redis down")
	local := NewLocalVideoCache(next, time.Minute)
	stored := storedTestVideo()
	if err := local.Set(context.Background(), stored); err == nil {
		t.Fatal("the next-layer failure must be reported to the caller")
	}
	video, hit, err := local.Get(context.Background(), 42)
	if gets, _, _, _ := next.counts(); err != nil || !hit || video == nil || *video != stored || gets != 0 {
		t.Fatalf("while Redis is down the local copy must still answer: %v, %v, %v, gets %d", video, hit, err, gets)
	}
}

// Local caches belong to one process: deleting through one instance cannot clear another
// instance's copy, which may be served until it expires. This is the consistency window
// that the 5-second local TTL in main.go accepts.
func TestLocalVideoCacheIsPerInstance(t *testing.T) {
	shared := newFakeVideoCache() // plays Redis, shared by both instances
	stored := storedTestVideo()
	shared.items[42] = &stored
	first := NewLocalVideoCache(shared, time.Minute)
	second := NewLocalVideoCache(shared, 200*time.Millisecond)
	first.Get(context.Background(), 42)
	second.Get(context.Background(), 42)
	if err := first.Delete(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if video, hit, err := second.Get(context.Background(), 42); err != nil || !hit || video == nil {
		t.Fatal("the other instance keeps its local copy until it expires")
	}
	time.Sleep(300 * time.Millisecond)
	if _, hit, err := second.Get(context.Background(), 42); err != nil || hit {
		t.Fatal("after its local TTL the other instance must see the deletion")
	}
}

func TestLocalVideoCacheDeleteClearsBothLayers(t *testing.T) {
	next := newFakeVideoCache()
	local := NewLocalVideoCache(next, time.Minute)
	if err := local.Set(context.Background(), storedTestVideo()); err != nil {
		t.Fatal(err)
	}
	if err := local.Delete(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	video, hit, err := local.Get(context.Background(), 42)
	if gets, _, _, deletes := next.counts(); err != nil || hit || video != nil || gets != 1 || deletes != 1 {
		t.Fatalf("delete must clear both layers: %v, %v, %v, gets %d, deletes %d", video, hit, err, gets, deletes)
	}
}
