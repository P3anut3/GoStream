package notification

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

type contextKey struct{}

// fakeRepository records every call as text, with the context it got, and answers with err.
// List returns items (all of them, whatever the limit) and CountUnread returns unread.
type fakeRepository struct {
	got    []Notification
	calls  []string
	ctxs   []context.Context
	err    error
	items  []Item
	unread int64
}

func (r *fakeRepository) record(ctx context.Context, format string, args ...any) {
	r.calls = append(r.calls, fmt.Sprintf(format, args...))
	r.ctxs = append(r.ctxs, ctx)
}

func (r *fakeRepository) Create(ctx context.Context, n Notification) error {
	r.got = append(r.got, n)
	r.record(ctx, "Create %d", n.EventID)
	return r.err
}

func (r *fakeRepository) List(ctx context.Context, recipientID, before uint64, limit int) ([]Item, error) {
	r.record(ctx, "List recipient=%d before=%d limit=%d", recipientID, before, limit)
	return r.items, r.err
}

func (r *fakeRepository) CountUnread(ctx context.Context, recipientID uint64) (int64, error) {
	r.record(ctx, "CountUnread recipient=%d", recipientID)
	return r.unread, r.err
}

func (r *fakeRepository) MarkRead(ctx context.Context, recipientID, id uint64) error {
	r.record(ctx, "MarkRead recipient=%d id=%d", recipientID, id)
	return r.err
}

func (r *fakeRepository) MarkAllRead(ctx context.Context, recipientID, upTo uint64) error {
	r.record(ctx, "MarkAllRead recipient=%d upTo=%d", recipientID, upTo)
	return r.err
}

func items(ids ...uint64) []Item {
	all := make([]Item, len(ids))
	for i, id := range ids {
		all[i] = Item{ID: id, Type: "like.created", ActorID: 7, ActorName: "fan", VideoID: 42, VideoTitle: "video", CreatedAt: eventTime}
	}
	return all
}

func ids(page Page) []uint64 {
	all := make([]uint64, len(page.Notifications))
	for i, item := range page.Notifications {
		all[i] = item.ID
	}
	return all
}

func TestServiceHandleEvent(t *testing.T) {
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	like := likeEvent(`{"actor_id":7,"author_id":3,"video_id":42}`)

	t.Run("stores_the_notification", func(t *testing.T) {
		repo := &fakeRepository{}
		if err := NewService(repo).HandleEvent(ctx, like); err != nil {
			t.Fatal(err)
		}
		want := Notification{RecipientID: 3, ActorID: 7, EventID: 41, EventType: like.Type, VideoID: 42, CreatedAt: eventTime}
		if len(repo.got) != 1 || !sameNotification(repo.got[0], want) {
			t.Fatalf("stored %+v, want one %+v", repo.got, want)
		}
		if repo.ctxs[0].Value(contextKey{}) != "request" {
			t.Error("the caller's context must reach the repository")
		}
	})

	t.Run("invalid_events_never_reach_the_database", func(t *testing.T) {
		repo := &fakeRepository{}
		err := NewService(repo).HandleEvent(ctx, likeEvent(`{"actor_id":7}`))
		if !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("expected ErrInvalidEvent, got %v", err)
		}
		if len(repo.got) != 0 {
			t.Fatalf("an invalid event must not be stored, got %+v", repo.got)
		}
	})

	t.Run("database_errors_can_be_retried", func(t *testing.T) {
		down := errors.New("database unavailable")
		err := NewService(&fakeRepository{err: down}).HandleEvent(ctx, like)
		if !errors.Is(err, down) || errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("a database error must be returned as is, got %v", err)
		}
	})
}

// TestServiceList: the service asks for one item more than the page holds; finding it means
// there is a next page, which starts after the last item shown.
func TestServiceList(t *testing.T) {
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	for _, tc := range []struct {
		name       string
		limit      int
		cursor     string
		found      []Item
		wantCall   string
		wantIDs    []uint64
		wantCursor string
		wantMore   bool
	}{
		{name: "default_limit", found: items(9, 8, 7), wantCall: "List recipient=3 before=0 limit=21", wantIDs: []uint64{9, 8, 7}},
		{name: "more_than_a_page", limit: 2, found: items(9, 8, 7), wantCall: "List recipient=3 before=0 limit=3", wantIDs: []uint64{9, 8}, wantCursor: "8", wantMore: true},
		{name: "exactly_a_page", limit: 2, found: items(9, 8), wantCall: "List recipient=3 before=0 limit=3", wantIDs: []uint64{9, 8}},
		{name: "next_page_from_the_cursor", limit: 2, cursor: "8", found: items(7, 6, 5), wantCall: "List recipient=3 before=8 limit=3", wantIDs: []uint64{7, 6}, wantCursor: "6", wantMore: true},
		{name: "largest_page", limit: 50, found: items(1), wantCall: "List recipient=3 before=0 limit=51", wantIDs: []uint64{1}},
		{name: "smallest_page", limit: 1, found: items(5, 4), wantCall: "List recipient=3 before=0 limit=2", wantIDs: []uint64{5}, wantCursor: "5", wantMore: true},
		{name: "nothing_yet", wantCall: "List recipient=3 before=0 limit=21", wantIDs: []uint64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepository{items: tc.found}
			page, err := NewService(repo).List(ctx, 3, tc.limit, tc.cursor)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(repo.calls, []string{tc.wantCall}) {
				t.Fatalf("calls %v, want %q", repo.calls, tc.wantCall)
			}
			if repo.ctxs[0].Value(contextKey{}) != "request" {
				t.Error("the caller's context must reach the repository")
			}
			if !slices.Equal(ids(page), tc.wantIDs) || page.NextCursor != tc.wantCursor || page.HasMore != tc.wantMore {
				t.Fatalf("page %v cursor %q more %v, want %v %q %v", ids(page), page.NextCursor, page.HasMore, tc.wantIDs, tc.wantCursor, tc.wantMore)
			}
			// An empty page is an empty list, not nil, so the JSON is [] rather than null.
			if page.Notifications == nil {
				t.Fatal("the notifications must never be nil")
			}
		})
	}
}

func TestServiceListRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		limit  int
		cursor string
		want   error
	}{
		{limit: -1, want: ErrInvalidLimit},
		{limit: 51, want: ErrInvalidLimit},
		{cursor: "abc", want: ErrInvalidCursor},
		{cursor: "0", want: ErrInvalidCursor},
		{cursor: "-8", want: ErrInvalidCursor},
		{cursor: "8.5", want: ErrInvalidCursor},
		{cursor: "18446744073709551616", want: ErrInvalidCursor},
	} {
		t.Run(fmt.Sprintf("limit_%d_cursor_%s", tc.limit, tc.cursor), func(t *testing.T) {
			repo := &fakeRepository{}
			if _, err := NewService(repo).List(context.Background(), 3, tc.limit, tc.cursor); !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
			if len(repo.calls) != 0 {
				t.Fatalf("bad input must not reach the database, got %v", repo.calls)
			}
		})
	}
	down := errors.New("database unavailable")
	if _, err := NewService(&fakeRepository{err: down}).List(context.Background(), 3, 0, ""); !errors.Is(err, down) {
		t.Fatalf("a database error must be returned, got %v", err)
	}
}

func TestServiceReadState(t *testing.T) {
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	repo := &fakeRepository{unread: 4}
	s := NewService(repo)
	if count, err := s.UnreadCount(ctx, 3); err != nil || count != 4 {
		t.Fatalf("unread count: %d, %v", count, err)
	}
	if err := s.MarkRead(ctx, 3, 7); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAllRead(ctx, 3, 9); err != nil {
		t.Fatal(err)
	}
	want := []string{"CountUnread recipient=3", "MarkRead recipient=3 id=7", "MarkAllRead recipient=3 upTo=9"}
	if !slices.Equal(repo.calls, want) {
		t.Fatalf("calls %v, want %v", repo.calls, want)
	}
	for _, got := range repo.ctxs {
		if got.Value(contextKey{}) != "request" {
			t.Fatal("the caller's context must reach the repository")
		}
	}
	// Errors, ErrNotFound included, pass through unchanged.
	failing := NewService(&fakeRepository{err: ErrNotFound})
	if _, err := failing.UnreadCount(ctx, 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unread count error: %v", err)
	}
	if err := failing.MarkRead(ctx, 3, 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mark read error: %v", err)
	}
	if err := failing.MarkAllRead(ctx, 3, 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mark all read error: %v", err)
	}
}
