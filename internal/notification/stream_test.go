package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
)

// streamWriter stands in for the browser's connection: it keeps apart what was written and what
// was flushed (only flushed bytes reach the browser), records every write deadline, and can be
// made to fail like a connection the browser dropped.
type streamWriter struct {
	mu        sync.Mutex
	header    http.Header
	status    int
	pending   bytes.Buffer
	flushed   bytes.Buffer
	deadlines []time.Time
	broken    bool
}

func (w *streamWriter) Header() http.Header { return w.header }

func (w *streamWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.broken {
		return 0, errors.New("connection reset by peer")
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.pending.Write(p)
}

func (w *streamWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending.WriteTo(&w.flushed)
}

func (w *streamWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

// sent returns what has reached the browser since the last call.
func (w *streamWriter) sent() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	text := w.flushed.String()
	w.flushed.Reset()
	return text
}

func (w *streamWriter) lastDeadline() (time.Time, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.deadlines) == 0 {
		return time.Time{}, 0
	}
	return w.deadlines[len(w.deadlines)-1], len(w.deadlines)
}

func (w *streamWriter) breakConnection() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.broken = true
}

// fakeSessions answers Exists from a set of live sessions, or fails like a Redis that is down.
type fakeSessions struct {
	mu    sync.Mutex
	live  map[string]bool
	err   error
	asked []string
}

func (f *fakeSessions) Exists(_ context.Context, sessionID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, sessionID)
	return f.live[sessionID], f.err
}

func (f *fakeSessions) set(sessionID string, live bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live[sessionID], f.err = live, err
}

func (f *fakeSessions) questions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// stream is one running Stream handler.
type stream struct {
	w      *streamWriter
	leave  context.CancelFunc // the browser closes the connection
	done   chan struct{}      // closed when the handler returns
	values map[string]any
}

func (s *stream) ended() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// login is what JWTAuth puts into the context for user 7's session s1, whose token expires in
// 15 minutes.
func login() map[string]any {
	return map[string]any{"userID": uint64(7), "sessionID": "s1", "tokenExpiresAt": time.Now().Add(15 * time.Minute)}
}

// open runs the handler on a fake connection with the given context values and returns at once.
func open(h *StreamHandler, values map[string]any) *stream {
	w := &streamWriter{header: http.Header{}}
	c, _ := gin.CreateTestContext(w)
	ctx, leave := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodGet, "/notifications/stream", nil).WithContext(ctx)
	for key, value := range values {
		c.Set(key, value)
	}
	s := &stream{w: w, leave: leave, done: make(chan struct{}), values: values}
	go func() {
		defer close(s.done)
		h.Stream(c)
	}()
	return s
}

const (
	connected = ": connected\n\n"
	hint      = "event: notification\ndata: {}\n\n"
	ping      = ": ping\n\n"
)

// TestStreamSendsHintsAndHeartbeats follows one stream through virtual time: the headers and the
// first comment arrive at once, a hint for the user arrives as an event while hints for others do
// not, and every 25 seconds a heartbeat follows a check that the session still exists. Every write
// is flushed and gets a 10-second deadline.
func TestStreamSendsHintsAndHeartbeats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewHub()
		sessions := &fakeSessions{live: map[string]bool{"s1": true}}
		s := open(NewStreamHandler(hub, sessions), login())
		synctest.Wait()
		if s.ended() {
			t.Fatal("the stream must stay open")
		}
		if s.w.status != http.StatusOK {
			t.Fatalf("status %d, want 200", s.w.status)
		}
		for name, want := range map[string]string{
			"Content-Type": "text/event-stream", "Cache-Control": "private, no-store", "Vary": "Authorization", "X-Accel-Buffering": "no",
		} {
			if got := s.w.header.Get(name); got != want {
				t.Errorf("%s: got %q, want %q", name, got, want)
			}
		}
		if got := s.w.sent(); got != connected {
			t.Fatalf("the browser must get the opening comment at once, got %q", got)
		}
		expectDeadline := func(writes int) {
			t.Helper()
			deadline, count := s.w.lastDeadline()
			if count != writes || !deadline.Equal(time.Now().Add(10*time.Second)) {
				t.Fatalf("each write needs a 10-second deadline: %d deadlines for %d writes, last %v", count, writes, deadline)
			}
		}
		expectDeadline(1)

		hub.Publish(8) // someone else's notification
		synctest.Wait()
		if got := s.w.sent(); got != "" {
			t.Fatalf("another user's hint must not reach this stream, got %q", got)
		}
		hub.Publish(7)
		synctest.Wait()
		if got := s.w.sent(); got != hint {
			t.Fatalf("a hint for user 7 must arrive as one event, got %q", got)
		}
		expectDeadline(2)

		time.Sleep(25*time.Second - time.Nanosecond)
		synctest.Wait()
		if got := s.w.sent(); got != "" || len(sessions.questions()) != 0 {
			t.Fatalf("nothing is due before 25 seconds, got %q and %d session checks", got, len(sessions.questions()))
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := s.w.sent(); got != ping {
			t.Fatalf("a heartbeat comment is due every 25 seconds, got %q", got)
		}
		if asked := sessions.questions(); len(asked) != 1 || asked[0] != "s1" {
			t.Fatalf("the heartbeat must check the token's session s1, checked %v", asked)
		}
		expectDeadline(3)
		time.Sleep(25 * time.Second)
		synctest.Wait()
		if got := s.w.sent(); got != ping || len(sessions.questions()) != 2 {
			t.Fatalf("the next heartbeat follows 25 seconds later, got %q", got)
		}
		if len(hub.streams[7]) != 1 {
			t.Fatal("an open stream must stay registered")
		}
		s.leave()
		synctest.Wait()
	})
}

// TestStreamEnds: the stream ends, and leaves the hub, when the browser leaves, the token
// expires, the session is deleted or the hub closes for shutdown.
func TestStreamEnds(t *testing.T) {
	writer := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(writer) })
	for _, tc := range []struct {
		name string
		// end makes the stream end, after it has been open for a while.
		end   func(t *testing.T, hub *Hub, sessions *fakeSessions, s *stream)
		after time.Duration // how long it may take, in virtual time
	}{
		{name: "browser_leaves", end: func(_ *testing.T, _ *Hub, _ *fakeSessions, s *stream) { s.leave() }},
		{name: "hub_closes", end: func(_ *testing.T, hub *Hub, _ *fakeSessions, _ *stream) { hub.Close() }},
		{name: "hub_closes_with_a_hint_waiting", end: func(_ *testing.T, hub *Hub, _ *fakeSessions, _ *stream) {
			hub.Publish(7)
			hub.Close()
		}},
		{name: "token_expires", end: func(t *testing.T, _ *Hub, _ *fakeSessions, s *stream) {
			left := time.Until(s.values["tokenExpiresAt"].(time.Time))
			time.Sleep(left - time.Nanosecond)
			synctest.Wait()
			if s.ended() {
				t.Fatal("the stream must last until the token expires")
			}
		}, after: time.Nanosecond},
		{name: "session_deleted", end: func(_ *testing.T, _ *Hub, sessions *fakeSessions, _ *stream) {
			sessions.set("s1", false, nil)
		}, after: 25 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hub := NewHub()
				sessions := &fakeSessions{live: map[string]bool{"s1": true}}
				s := open(NewStreamHandler(hub, sessions), login())
				time.Sleep(time.Minute) // two heartbeats pass first
				synctest.Wait()
				if s.ended() {
					t.Fatal("the stream must stay open while nothing happens")
				}
				s.w.sent()
				tc.end(t, hub, sessions, s)
				time.Sleep(tc.after)
				synctest.Wait()
				if !s.ended() {
					t.Fatalf("the stream must end %v after this", tc.after)
				}
				if tc.name == "session_deleted" && strings.Contains(s.w.sent(), ping) {
					t.Fatal("a stream whose session is gone must end without another heartbeat")
				}
				if _, registered := hub.streams[7]; registered {
					t.Fatal("an ended stream must leave the hub")
				}
				s.leave()
			})
		})
	}
}

// TestStreamOutlivesASessionStoreOutage: when the session cannot be checked, the stream goes on,
// as requests do, and ends at the latest when its token expires.
func TestStreamOutlivesASessionStoreOutage(t *testing.T) {
	writer := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(writer) })
	synctest.Test(t, func(t *testing.T) {
		sessions := &fakeSessions{live: map[string]bool{}, err: errors.New("redis down")}
		s := open(NewStreamHandler(NewHub(), sessions), login())
		time.Sleep(25 * time.Second)
		synctest.Wait()
		if s.ended() || s.w.sent() != connected+ping {
			t.Fatal("while sessions cannot be checked, heartbeats must go on")
		}
		time.Sleep(15 * time.Minute)
		synctest.Wait()
		if !s.ended() {
			t.Fatal("the stream must still end when the token expires")
		}
	})
}

// TestStreamWithoutSessionChecks: with no session store the stream only ends for the other reasons.
func TestStreamWithoutSessionChecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := open(NewStreamHandler(NewHub(), nil), login())
		time.Sleep(time.Minute)
		synctest.Wait()
		if s.ended() || s.w.sent() != connected+ping+ping {
			t.Fatal("without a session store, heartbeats go on")
		}
		s.leave()
		synctest.Wait()
	})
}

// TestStreamStopsOnABrokenConnection: once a write fails, the handler returns instead of writing
// into a dead connection forever.
func TestStreamStopsOnABrokenConnection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(hub *Hub)
	}{
		{name: "hint", write: func(hub *Hub) { hub.Publish(7) }},
		{name: "heartbeat", write: func(*Hub) { time.Sleep(25 * time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hub := NewHub()
				s := open(NewStreamHandler(hub, nil), login())
				synctest.Wait()
				s.w.breakConnection()
				tc.write(hub)
				synctest.Wait()
				if !s.ended() {
					t.Fatal("a failed write must end the stream")
				}
				if _, registered := hub.streams[7]; registered {
					t.Fatal("an ended stream must leave the hub")
				}
				s.leave()
			})
		})
	}
	synctest.Test(t, func(t *testing.T) {
		hub := NewHub()
		w := &streamWriter{header: http.Header{}, broken: true}
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/notifications/stream", nil)
		for key, value := range login() {
			c.Set(key, value)
		}
		started := time.Now()
		NewStreamHandler(hub, nil).Stream(c)
		if waited := time.Since(started); waited != 0 {
			t.Fatalf("when even the first write fails the stream must end at once, it waited %v", waited)
		}
		if _, registered := hub.streams[7]; registered {
			t.Fatal("a stream that could not start must leave the hub")
		}
	})
}

// TestStreamRefusals: refused streams get a JSON error and no stream.
func TestStreamRefusals(t *testing.T) {
	full := NewHub()
	for range MaxStreamsPerUser {
		if _, err := full.Subscribe(7); err != nil {
			t.Fatal(err)
		}
	}
	closed := NewHub()
	closed.Close()
	without := func(key string) map[string]any {
		values := login()
		delete(values, key)
		return values
	}
	for _, tc := range []struct {
		name   string
		hub    *Hub
		values map[string]any
		want   int
	}{
		{name: "too_many_streams", hub: full, values: login(), want: http.StatusTooManyRequests},
		{name: "shutting_down", hub: closed, values: login(), want: http.StatusServiceUnavailable},
		// Both come only from JWTAuth: without them the route lacks the middleware, a server fault.
		{name: "no_user", hub: NewHub(), values: without("userID"), want: http.StatusInternalServerError},
		{name: "no_token_expiry", hub: NewHub(), values: without("tokenExpiresAt"), want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := open(NewStreamHandler(tc.hub, nil), tc.values)
				synctest.Wait()
				if !s.ended() {
					t.Fatal("a refused stream must end at once")
				}
				var body struct {
					Error string `json:"error"`
				}
				sent := s.w.pending.String() + s.w.sent()
				if s.w.status != tc.want || json.Unmarshal([]byte(sent), &body) != nil || body.Error == "" {
					t.Fatalf("got %d %q, want %d with a JSON error", s.w.status, sent, tc.want)
				}
				if strings.HasPrefix(s.w.header.Get("Content-Type"), "text/event-stream") {
					t.Fatal("a refusal is not a stream")
				}
				if tc.name == "too_many_streams" && len(tc.hub.streams[7]) != MaxStreamsPerUser {
					t.Fatal("a refused stream must not take a place")
				}
				s.leave()
			})
		})
	}
}
