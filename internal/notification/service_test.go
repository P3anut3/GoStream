package notification

import (
	"context"
	"errors"
	"testing"
)

type contextKey struct{}

// fakeRepository records what the service stores and answers with err.
type fakeRepository struct {
	got  []Notification
	ctxs []context.Context
	err  error
}

func (r *fakeRepository) Create(ctx context.Context, n Notification) error {
	r.got = append(r.got, n)
	r.ctxs = append(r.ctxs, ctx)
	return r.err
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
