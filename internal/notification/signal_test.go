package notification

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// waitForSignal waits, in real time, until the stream's slot holds a signal.
func waitForSignal(t *testing.T, s *Subscription, what string) {
	t.Helper()
	select {
	case _, open := <-s.C():
		if !open {
			t.Fatalf("%s: the slot was closed", what)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no signal arrived", what)
	}
}

// waitForSubscribers waits until the signal channel has n subscribers.
func waitForSubscribers(t *testing.T, server *miniredis.Miniredis, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); server.PubSubNumSub(SignalChannel)[SignalChannel] != n; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the signal channel must get %d subscribers", n)
		}
	}
}

// TestRedisSignals runs a publisher and a listener against miniredis, with the API's client
// settings (300 ms read timeout, no retries): a signal reaches only its recipient's streams,
// malformed messages are dropped, the subscription survives idle time, and after Redis restarts
// the listener subscribes again and signals every stream, since signals sent meanwhile were lost.
func TestRedisSignals(t *testing.T) {
	writer := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(writer) })
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), DialTimeout: time.Second, ReadTimeout: 300 * time.Millisecond,
		WriteTimeout: 300 * time.Millisecond, MaxRetries: -1, DialerRetries: 1})
	t.Cleanup(func() { client.Close() })
	signals := &RedisSignals{Client: client}
	hub := NewHub()
	author, fan := subscribe(t, hub, 1), subscribe(t, hub, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		signals.Listen(ctx, hub)
	}()
	// Subscribing signals every stream: nothing sent before could be heard.
	waitForSignal(t, author, "subscribed")
	waitForSignal(t, fan, "subscribed")
	waitForSubscribers(t, server, 1)

	time.Sleep(time.Second) // idle for longer than the client's read timeout
	if err := signals.Publish(ctx, 1); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, author, "the author's signal")
	if received(fan) {
		t.Fatal("the fan must not hear about the author's notification")
	}
	// Messages are handled in order: the fan's signal arriving after the malformed ones proves
	// those were dropped, not just slow.
	for _, payload := range []string{"not json", `{"recipient_id":0}`, `{"recipient_id":"1"}`, `{}`} {
		if err := client.Publish(ctx, SignalChannel, payload).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if err := signals.Publish(ctx, 2); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, fan, "the fan's signal")
	if received(author) {
		t.Fatal("malformed signals must reach nobody")
	}

	// Redis restarts: whatever was sent while it was down is gone, so the listener signals every
	// stream once it has subscribed again.
	server.Close()
	if err := signals.Publish(ctx, 1); err == nil {
		t.Fatal("publishing while Redis is down must fail")
	}
	if err := server.Restart(); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, author, "subscribed again")
	waitForSignal(t, fan, "subscribed again")
	waitForSubscribers(t, server, 1)
	if err := signals.Publish(ctx, 1); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, author, "after the restart")

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Listen must return once its context ends")
	}
	waitForSubscribers(t, server, 0)
}
