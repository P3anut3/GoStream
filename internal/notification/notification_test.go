package notification

import (
	"errors"
	"testing"
	"time"

	"gostream/internal/outbox"
)

var eventTime = time.Date(2025, 11, 12, 20, 30, 0, 0, time.UTC)

func likeEvent(payload string) outbox.Event {
	return outbox.Event{ID: 41, Type: outbox.TypeLikeCreated, Payload: []byte(payload), CreatedAt: eventTime}
}

func commentEvent(payload string) outbox.Event {
	return outbox.Event{ID: 43, Type: outbox.TypeCommentCreated, Payload: []byte(payload), CreatedAt: eventTime}
}

// sameNotification compares the comment ID by value and the time by instant.
func sameNotification(a, b Notification) bool {
	sameComment := a.CommentID == nil && b.CommentID == nil ||
		a.CommentID != nil && b.CommentID != nil && *a.CommentID == *b.CommentID
	return a.RecipientID == b.RecipientID && a.ActorID == b.ActorID && a.EventID == b.EventID &&
		a.EventType == b.EventType && a.VideoID == b.VideoID && sameComment && a.CreatedAt.Equal(b.CreatedAt)
}

func TestFromEvent(t *testing.T) {
	comment := uint64(9)
	like := Notification{RecipientID: 3, ActorID: 7, EventID: 41, EventType: outbox.TypeLikeCreated, VideoID: 42, CreatedAt: eventTime}
	for _, tc := range []struct {
		name  string
		event outbox.Event
		want  Notification
	}{
		{name: "like_notifies_the_author", event: likeEvent(`{"actor_id":7,"author_id":3,"video_id":42}`), want: like},
		{name: "comment_keeps_the_comment", event: commentEvent(`{"actor_id":7,"author_id":3,"video_id":42,"comment_id":9}`),
			want: Notification{RecipientID: 3, ActorID: 7, EventID: 43, EventType: outbox.TypeCommentCreated, VideoID: 42, CommentID: &comment, CreatedAt: eventTime}},
		// Fields added to a payload later must not break this consumer.
		{name: "unknown_fields_are_ignored", event: likeEvent(`{"actor_id":7,"author_id":3,"video_id":42,"source":"feed"}`), want: like},
		// A like never carries a comment, whatever its payload says.
		{name: "likes_have_no_comment", event: likeEvent(`{"actor_id":7,"author_id":3,"video_id":42,"comment_id":9}`), want: like},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FromEvent(tc.event)
			if err != nil {
				t.Fatal(err)
			}
			if !sameNotification(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFromEventRejectsInvalidEvents(t *testing.T) {
	const valid = `{"actor_id":7,"author_id":3,"video_id":42}`
	for _, tc := range []struct {
		name  string
		event outbox.Event
	}{
		{name: "unknown_type", event: outbox.Event{ID: 41, Type: "video.deleted", Payload: []byte(valid), CreatedAt: eventTime}},
		{name: "no_type", event: outbox.Event{ID: 41, Payload: []byte(valid), CreatedAt: eventTime}},
		{name: "like_not_json", event: likeEvent(`not json`)},
		{name: "like_with_text_id", event: likeEvent(`{"actor_id":"7","author_id":3,"video_id":42}`)},
		{name: "like_with_negative_id", event: likeEvent(`{"actor_id":-7,"author_id":3,"video_id":42}`)},
		{name: "comment_not_json", event: commentEvent(`[`)},
		{name: "like_without_actor", event: likeEvent(`{"author_id":3,"video_id":42}`)},
		{name: "like_without_author", event: likeEvent(`{"actor_id":7,"video_id":42}`)},
		{name: "like_without_video", event: likeEvent(`{"actor_id":7,"author_id":3}`)},
		{name: "comment_without_comment", event: commentEvent(valid)},
		{name: "comment_without_actor", event: commentEvent(`{"author_id":3,"video_id":42,"comment_id":9}`)},
		{name: "comment_without_author", event: commentEvent(`{"actor_id":7,"video_id":42,"comment_id":9}`)},
		{name: "comment_without_video", event: commentEvent(`{"actor_id":7,"author_id":3,"comment_id":9}`)},
		{name: "no_event_id", event: outbox.Event{Type: outbox.TypeLikeCreated, Payload: []byte(valid), CreatedAt: eventTime}},
		{name: "no_time", event: outbox.Event{ID: 41, Type: outbox.TypeLikeCreated, Payload: []byte(valid)}},
		{name: "own_like", event: likeEvent(`{"actor_id":3,"author_id":3,"video_id":42}`)},
		{name: "own_comment", event: commentEvent(`{"actor_id":3,"author_id":3,"video_id":42,"comment_id":9}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := FromEvent(tc.event); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("expected ErrInvalidEvent, got %v", err)
			}
		})
	}
}
