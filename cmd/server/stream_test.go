package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"gostream/internal/auth"
	"gostream/internal/notification"
)

// TestNotificationStreamRoute: the stream needs a login, and without a hub, which main creates
// only with Redis, there is no stream at all, so the page keeps polling.
func TestNotificationStreamRoute(t *testing.T) {
	secret := []byte("test-only-jwt-secret-at-least-32-bytes")
	for _, tc := range []struct {
		name string
		hub  *notification.Hub
		want int
	}{
		{name: "without_redis_there_is_no_stream", want: http.StatusNotFound},
		{name: "the_stream_needs_a_login", hub: notification.NewHub(), want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newRouter(nil, secret, newTestFeedCursor(t), newTestStorage(t), nil, false, tc.hub)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/notifications/stream", nil))
			if response.Code != tc.want {
				t.Fatalf("got %d, want %d", response.Code, tc.want)
			}
		})
	}
}

// sseBlocks sends each block of a stream (its lines up to a blank line) as it arrives, and closes
// the channel when the stream ends.
func sseBlocks(body io.Reader) <-chan string {
	blocks := make(chan string, 16)
	go func() {
		defer close(blocks)
		scanner := bufio.NewScanner(body)
		var lines []string
		for scanner.Scan() {
			if line := scanner.Text(); line != "" {
				lines = append(lines, line)
				continue
			}
			blocks <- strings.Join(lines, "\n")
			lines = nil
		}
	}()
	return blocks
}

// TestNotificationStreamThroughRedis runs the real router and server over real connections, with
// miniredis and the API's Redis client: a signal published to Redis reaches the recipient's
// stream and nobody else's, also after the subscription sat idle for longer than the client's
// read timeout; and Shutdown completes at once although streams are open, ending them.
func TestNotificationStreamThroughRedis(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := newRedisClient(redisServer.Addr(), "")
	t.Cleanup(func() { redisClient.Close() })
	secret := []byte("stream-test-only-jwt-secret-at-least-32-bytes")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if newHub(ctx, nil) != nil {
		t.Fatal("without Redis there must be no hub, so no stream")
	}
	hub := newHub(ctx, redisClient)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newServer(listener.Addr().String(), newRouter(nil, secret, newTestFeedCursor(t), newTestStorage(t), redisClient, false, hub), hub)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() { server.Close() })
	subscribers := func() int { return redisServer.PubSubNumSub(notification.SignalChannel)[notification.SignalChannel] }
	for deadline := time.Now().Add(5 * time.Second); subscribers() != 1; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the hub must listen to the signal channel")
		}
	}
	signals := &notification.RedisSignals{Client: redisClient}

	next := func(blocks <-chan string, what string) string {
		t.Helper()
		select {
		case block, open := <-blocks:
			if !open {
				t.Fatalf("%s: the stream ended", what)
			}
			return block
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: nothing arrived", what)
			return ""
		}
	}
	sessions := &auth.RedisSessionStore{Client: redisClient}
	client := &http.Client{} // no timeout: the response never ends by itself
	open := func(userID uint64) <-chan string {
		t.Helper()
		sessionID, _, err := sessions.Create(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		token, err := auth.GenerateToken(userID, sessionID, secret)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listener.Addr().String()+"/notifications/stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { response.Body.Close() })
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatalf("user %d: got %d %q, want a 200 event stream", userID, response.StatusCode, response.Header.Get("Content-Type"))
		}
		blocks := sseBlocks(response.Body)
		if first := next(blocks, "opening comment"); first != ": connected" {
			t.Fatalf("the stream must open with a comment, got %q", first)
		}
		return blocks
	}
	author, fan := open(1), open(2)

	time.Sleep(time.Second) // idle for longer than the Redis client's 300 ms read timeout
	if err := signals.Publish(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got := next(author, "the author's hint"); got != "event: notification\ndata: {}" {
		t.Fatalf("the author must get the hint as an event, got %q", got)
	}
	select {
	case block := <-fan:
		t.Fatalf("the fan must not hear about the author's notification, got %q", block)
	case <-time.After(300 * time.Millisecond):
	}

	started := time.Now()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := server.Shutdown(shutdown); err != nil {
		t.Fatalf("Shutdown must complete while streams are open, got %v", err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Fatalf("Shutdown took %v: the streams kept it waiting", took)
	}
	for name, blocks := range map[string]<-chan string{"author": author, "fan": fan} {
		for ended := false; !ended; {
			select {
			case _, open := <-blocks:
				ended = !open
			case <-time.After(5 * time.Second):
				t.Fatalf("the %s's stream must end on shutdown", name)
			}
		}
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("the server must stop with ErrServerClosed, got %v", err)
	}
	// The hub listens only as long as its context: the process is stopping.
	cancel()
	for deadline := time.Now().Add(5 * time.Second); subscribers() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the hub must stop listening when its context ends")
		}
	}
}
