package notification

import (
	"errors"
	"sync"
	"testing"
)

// received takes the signal waiting in a stream's slot, without waiting for one.
func received(s *Subscription) bool {
	select {
	case _, open := <-s.C():
		return open
	default:
		return false
	}
}

// isClosed empties a stream's slot and reports whether the hub has closed it.
func isClosed(s *Subscription) bool {
	for {
		select {
		case _, open := <-s.C():
			if !open {
				return true
			}
		default:
			return false
		}
	}
}

func subscribe(t *testing.T, hub *Hub, userID uint64) *Subscription {
	t.Helper()
	s, err := hub.Subscribe(userID)
	if err != nil {
		t.Fatalf("subscribe user %d: %v", userID, err)
	}
	return s
}

func TestHubSignalsOnlyTheUsersStreams(t *testing.T) {
	hub := NewHub()
	firstTab, secondTab, other := subscribe(t, hub, 1), subscribe(t, hub, 1), subscribe(t, hub, 2)
	hub.Publish(1)
	if !received(firstTab) || !received(secondTab) {
		t.Fatal("every stream of user 1 must hear about user 1's new notification")
	}
	if received(other) {
		t.Fatal("user 2 must not hear about user 1's notifications")
	}
	hub.Publish(3) // nobody of user 3 is connected: nothing happens
	if received(firstTab) || received(secondTab) || received(other) {
		t.Fatal("a signal for a user without streams must reach nobody")
	}
}

func TestHubCoalescesSignalsIntoOneSlot(t *testing.T) {
	hub := NewHub()
	s := subscribe(t, hub, 1)
	// Nobody reads the slot: publishing must neither block nor queue.
	for range 10 {
		hub.Publish(1)
	}
	if !received(s) {
		t.Fatal("the stream must hear about the new notifications")
	}
	if received(s) {
		t.Fatal("ten signals that arrive while the stream is busy must wait as one")
	}
	hub.Publish(1)
	if !received(s) {
		t.Fatal("once the slot is empty, the next signal must arrive")
	}
}

func TestHubPublishAllSignalsEveryStream(t *testing.T) {
	hub := NewHub()
	streams := []*Subscription{subscribe(t, hub, 1), subscribe(t, hub, 1), subscribe(t, hub, 2)}
	hub.PublishAll()
	for i, s := range streams {
		if !received(s) {
			t.Fatalf("stream %d must hear the signal sent to everyone", i)
		}
	}
}

func TestHubLimitsStreamsPerUser(t *testing.T) {
	if MaxStreamsPerUser != 5 {
		t.Fatalf("a user may keep 5 streams, got %d", MaxStreamsPerUser)
	}
	hub := NewHub()
	var streams []*Subscription
	for range MaxStreamsPerUser {
		streams = append(streams, subscribe(t, hub, 1))
	}
	if s, err := hub.Subscribe(1); !errors.Is(err, ErrTooManyStreams) || s != nil {
		t.Fatalf("the sixth stream must be refused with ErrTooManyStreams, got %v", err)
	}
	subscribe(t, hub, 2) // the limit is per user
	streams[0].Close()
	subscribe(t, hub, 1) // a closed stream frees its place
	if _, err := hub.Subscribe(1); !errors.Is(err, ErrTooManyStreams) {
		t.Fatalf("the place may be taken only once, got %v", err)
	}
}

func TestSubscriptionClose(t *testing.T) {
	hub := NewHub()
	closed, open := subscribe(t, hub, 1), subscribe(t, hub, 1)
	closed.Close()
	closed.Close() // closing twice is harmless
	hub.Publish(1)
	if received(closed) {
		t.Fatal("a closed stream must not be signaled")
	}
	if !received(open) {
		t.Fatal("the user's other stream must still be signaled")
	}
	open.Close()
	// Every user that ever connected would otherwise stay in the map.
	if len(hub.streams) != 0 {
		t.Fatalf("a user without streams must be forgotten, %d users remain", len(hub.streams))
	}
}

func TestHubClose(t *testing.T) {
	hub := NewHub()
	waiting, idle := subscribe(t, hub, 1), subscribe(t, hub, 2)
	hub.Publish(1) // a signal still in the slot does not keep it open
	hub.Close()
	hub.Close() // closing twice is harmless
	if !isClosed(waiting) || !isClosed(idle) {
		t.Fatal("closing the hub must close every stream's slot")
	}
	if s, err := hub.Subscribe(1); !errors.Is(err, ErrHubClosed) || s != nil {
		t.Fatalf("a closed hub must refuse new streams with ErrHubClosed, got %v", err)
	}
	// Streams ending after the hub closed, and late signals, must not panic.
	waiting.Close()
	idle.Close()
	hub.Publish(1)
	hub.PublishAll()
}

// TestHubConcurrentUse is meant for -race: streams come and go while signals are published and
// the hub closes.
func TestHubConcurrentUse(t *testing.T) {
	hub := NewHub()
	var wg sync.WaitGroup
	for user := uint64(1); user <= 8; user++ {
		wg.Go(func() {
			for range 300 {
				s, err := hub.Subscribe(user)
				if err != nil {
					t.Error(err)
					return
				}
				hub.Publish(user)
				received(s)
				s.Close()
			}
		})
		wg.Go(func() {
			for range 300 {
				hub.Publish(user)
				hub.PublishAll()
			}
		})
	}
	wg.Wait()
	if len(hub.streams) != 0 {
		t.Fatalf("every stream was closed, yet %d users remain", len(hub.streams))
	}
	// Closing while streams keep coming: whoever is refused gets ErrHubClosed, nobody panics.
	for user := uint64(1); user <= 8; user++ {
		wg.Go(func() {
			for range 300 {
				s, err := hub.Subscribe(user)
				if errors.Is(err, ErrHubClosed) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				hub.Publish(user)
				s.Close()
			}
		})
	}
	hub.Close()
	wg.Wait()
}
