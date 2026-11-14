package mq

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"gostream/internal/outbox"
)

// messageTime lies in the past, like every real event time.
var messageTime = time.Date(2025, 11, 12, 20, 30, 0, 0, time.UTC)

// answers implements amqp.Acknowledger: it records how the consumer answered each delivery, next
// to the handler calls written by the tests, and fails every answer once fail is set.
type answers struct {
	mu   sync.Mutex
	log  []string
	fail error
}

func (a *answers) add(entry string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.log = append(a.log, entry)
}

func (a *answers) answer(entry string) error {
	a.add(entry)
	return a.fail
}

func (a *answers) entries() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.log)
}

func (a *answers) Ack(tag uint64, multiple bool) error {
	return a.answer(fmt.Sprintf("ack %d multiple=%v", tag, multiple))
}
func (a *answers) Nack(tag uint64, multiple, requeue bool) error {
	return a.answer(fmt.Sprintf("nack %d requeue=%v", tag, requeue))
}
func (a *answers) Reject(tag uint64, requeue bool) error {
	return a.answer(fmt.Sprintf("reject %d requeue=%v", tag, requeue))
}

func message(a *answers, id string) amqp.Delivery {
	return amqp.Delivery{Acknowledger: a, DeliveryTag: 7, MessageId: id, Type: outbox.TypeLikeCreated,
		Body: []byte(`{"actor_id":1}`), Timestamp: messageTime}
}

// discardLogs keeps the consumer's progress logs out of the test output.
func discardLogs(t *testing.T) {
	writer := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(writer) })
}

func TestHandleAcksAfterTheHandlerSucceeds(t *testing.T) {
	discardLogs(t)
	a := &answers{}
	var got []outbox.Event
	c := &Consumer{Handler: func(_ context.Context, e outbox.Event) error {
		got = append(got, e)
		a.add("handled")
		return nil
	}}
	if err := c.handle(context.Background(), message(a, "41")); err != nil {
		t.Fatal(err)
	}
	// The ack comes only after the handler is done: a crash in between means a redelivery.
	if log := a.entries(); !slices.Equal(log, []string{"handled", "ack 7 multiple=false"}) {
		t.Fatalf("answers: %v", log)
	}
	want := outbox.Event{ID: 41, Type: outbox.TypeLikeCreated, Payload: []byte(`{"actor_id":1}`), CreatedAt: messageTime}
	if len(got) != 1 || got[0].ID != want.ID || got[0].Type != want.Type || string(got[0].Payload) != string(want.Payload) || !got[0].CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("the handler got %+v, want %+v", got, want)
	}
}

func TestHandleRejectsWhatCannotBeHandled(t *testing.T) {
	discardLogs(t)
	t.Run("handler_says_reject", func(t *testing.T) {
		a := &answers{}
		c := &Consumer{Handler: func(context.Context, outbox.Event) error {
			return fmt.Errorf("wrapped: %w", fmt.Errorf("%w: unknown type", ErrReject))
		}}
		if err := c.handle(context.Background(), message(a, "41")); err != nil {
			t.Fatal(err)
		}
		if log := a.entries(); !slices.Equal(log, []string{"reject 7 requeue=false"}) {
			t.Fatalf("a rejected event must go to the dead-letter queue, answers: %v", log)
		}
	})
	// A message whose message_id is not an event ID cannot be handled at all.
	for _, id := range []string{"", "abc", "0", "-1", "4.5", "18446744073709551616"} {
		t.Run("message_id_"+id, func(t *testing.T) {
			a := &answers{}
			c := &Consumer{Handler: func(context.Context, outbox.Event) error {
				t.Error("the handler must not see a message without an event ID")
				return nil
			}}
			if err := c.handle(context.Background(), message(a, id)); err != nil {
				t.Fatal(err)
			}
			if log := a.entries(); !slices.Equal(log, []string{"reject 7 requeue=false"}) {
				t.Fatalf("answers: %v", log)
			}
		})
	}
}

// TestHandleRetriesInPlace: a temporary failure keeps the message and tries the same event again
// after 1, 2, 4… seconds up to MaxBackoff, without returning it to the queue in between.
func TestHandleRetriesInPlace(t *testing.T) {
	discardLogs(t)
	synctest.Test(t, func(t *testing.T) {
		a := &answers{}
		start := time.Now()
		var tries []time.Duration
		c := &Consumer{MaxBackoff: 5 * time.Second, Handler: func(_ context.Context, e outbox.Event) error {
			tries = append(tries, time.Since(start))
			if len(tries) <= 6 {
				return errors.New("database unavailable")
			}
			return nil
		}}
		if err := c.handle(context.Background(), message(a, "41")); err != nil {
			t.Fatal(err)
		}
		want := []time.Duration{0, 1 * time.Second, 3 * time.Second, 7 * time.Second, 12 * time.Second, 17 * time.Second, 22 * time.Second}
		if !slices.Equal(tries, want) {
			t.Fatalf("tries at %v, want %v", tries, want)
		}
		if log := a.entries(); !slices.Equal(log, []string{"ack 7 multiple=false"}) {
			t.Fatalf("the message must be acknowledged once and never returned, answers: %v", log)
		}
	})
}

// TestHandleStopsWithoutAnswering: on shutdown the message is neither acknowledged nor rejected,
// so it goes back to the queue when the connection closes and is handled after the restart.
func TestHandleStopsWithoutAnswering(t *testing.T) {
	discardLogs(t)
	t.Run("while_waiting_to_retry", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			a := &answers{}
			c := &Consumer{Handler: func(context.Context, outbox.Event) error { return errors.New("database unavailable") }}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error)
			go func() { done <- c.handle(ctx, message(a, "41")) }()
			time.Sleep(2500 * time.Millisecond)
			start := time.Now()
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if waited := time.Since(start); waited != 0 {
				t.Fatalf("stopping must not wait for the next try, waited %v", waited)
			}
			if log := a.entries(); len(log) != 0 {
				t.Fatalf("answers: %v", log)
			}
		})
	})
	t.Run("while_handling", func(t *testing.T) {
		a := &answers{}
		ctx, cancel := context.WithCancel(context.Background())
		c := &Consumer{Handler: func(ctx context.Context, _ outbox.Event) error {
			cancel()
			return ctx.Err()
		}}
		if err := c.handle(ctx, message(a, "41")); err != nil {
			t.Fatal(err)
		}
		if log := a.entries(); len(log) != 0 {
			t.Fatalf("answers: %v", log)
		}
	})
}

// TestHandleReportsALostChannel: when the answer cannot be sent the consumer must reconnect.
func TestHandleReportsALostChannel(t *testing.T) {
	discardLogs(t)
	lost := errors.New("channel/connection is not open")
	for name, handler := range map[string]Handler{
		"ack":    func(context.Context, outbox.Event) error { return nil },
		"reject": func(context.Context, outbox.Event) error { return ErrReject },
	} {
		t.Run(name, func(t *testing.T) {
			c := &Consumer{Handler: handler}
			if err := c.handle(context.Background(), message(&answers{fail: lost}, "41")); !errors.Is(err, lost) {
				t.Fatalf("expected the channel error, got %v", err)
			}
		})
	}
	t.Run("bad_message", func(t *testing.T) {
		c := &Consumer{Handler: func(context.Context, outbox.Event) error { return nil }}
		if err := c.handle(context.Background(), message(&answers{fail: lost}, "")); !errors.Is(err, lost) {
			t.Fatalf("expected the channel error, got %v", err)
		}
	})
}

func TestToEventTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		for _, tc := range []struct {
			name      string
			timestamp time.Time
			want      time.Time
		}{
			{name: "event_time_is_kept", timestamp: messageTime.AddDate(-30, 0, 0), want: messageTime.AddDate(-30, 0, 0)},
			{name: "missing_time_is_now", timestamp: time.Time{}, want: now},
			{name: "epoch_is_now", timestamp: time.Unix(0, 0), want: now},
			// Clocks of two machines differ by a few seconds; only a clearly wrong time is replaced.
			{name: "slightly_ahead_is_kept", timestamp: now.Add(time.Minute), want: now.Add(time.Minute)},
			{name: "far_future_is_now", timestamp: now.Add(2 * time.Hour), want: now},
		} {
			e, err := toEvent(amqp.Delivery{MessageId: "41", Timestamp: tc.timestamp})
			if err != nil {
				t.Fatal(err)
			}
			if !e.CreatedAt.Equal(tc.want) {
				t.Errorf("%s: got %v, want %v", tc.name, e.CreatedAt, tc.want)
			}
		}
	})
}

func TestConsumerDefaults(t *testing.T) {
	c := &Consumer{}
	if c.prefetch() != 10 || c.maxBackoff() != 30*time.Second || c.timeout() != 5*time.Second || c.firstBackoff() != time.Second {
		t.Fatalf("defaults: prefetch %d, max backoff %v, timeout %v, first backoff %v", c.prefetch(), c.maxBackoff(), c.timeout(), c.firstBackoff())
	}
	c = &Consumer{Prefetch: 3, MaxBackoff: 200 * time.Millisecond, Timeout: time.Second}
	if c.prefetch() != 3 || c.maxBackoff() != 200*time.Millisecond || c.timeout() != time.Second || c.firstBackoff() != 200*time.Millisecond {
		t.Fatalf("settings: prefetch %d, max backoff %v, timeout %v, first backoff %v", c.prefetch(), c.maxBackoff(), c.timeout(), c.firstBackoff())
	}
}

// TestRunKeepsReconnecting: a broker that hangs up on every connection is retried until shutdown,
// and the logs never show the password in the URL.
func TestRunKeepsReconnecting(t *testing.T) {
	var logs bytes.Buffer
	writer := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(writer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	c := &Consumer{URL: "amqp://gostream:s3cret@" + listener.Addr().String() + "/", Timeout: time.Second, MaxBackoff: 50 * time.Millisecond,
		Handler: func(context.Context, outbox.Event) error { return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	started := time.Now()
	c.Run(ctx)
	if time.Since(started) > 2*time.Second {
		t.Fatal("Run must return soon after the context is canceled")
	}
	if n := strings.Count(logs.String(), "后重连"); n < 2 {
		t.Fatalf("expected repeated reconnect attempts, got %d:\n%s", n, logs.String())
	}
	if strings.Contains(logs.String(), "s3cret") {
		t.Fatal("logs must not contain the password")
	}
}

// TestConsumerRabbitMQIntegration consumes from a real RabbitMQ: handled messages are
// acknowledged, rejected ones move to the dead-letter queue, a temporary failure is retried in
// place, the consumer resubscribes after its queue disappears, prefetch limits the messages it
// holds, and a message whose consumer keeps stopping before answering moves to the dead-letter
// queue after six deliveries. GOSTREAM_TEST_AMQP_URL must point to a disposable broker; the test
// empties its queues.
func TestConsumerRabbitMQIntegration(t *testing.T) {
	url := os.Getenv("GOSTREAM_TEST_AMQP_URL")
	if url == "" {
		t.Skip("set GOSTREAM_TEST_AMQP_URL to a disposable RabbitMQ")
	}
	discardLogs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal("cannot connect to the disposable RabbitMQ")
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	if err := DeclareTopology(ch); err != nil {
		t.Fatal(err)
	}
	purge := func() {
		t.Helper()
		for _, queue := range []string{NotificationsQueue, DeadLetterQueue} {
			if _, err := ch.QueuePurge(queue, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	purge()
	get := func(queue string) (amqp.Delivery, bool) {
		t.Helper()
		// Dead-lettering is asynchronous, so poll for a moment before giving up.
		for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			message, ok, err := ch.Get(queue, true)
			if err != nil {
				t.Fatal(err)
			}
			if ok || time.Now().After(deadline) {
				return message, ok
			}
		}
	}
	ready := func() int {
		t.Helper()
		queue, err := ch.QueueDeclarePassive(NotificationsQueue, true, false, false, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		return queue.Messages
	}
	waitUntil := func(what string, done func() bool) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); !done(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting until %s", what)
			}
		}
	}
	deathReason := func(d amqp.Delivery) string {
		deaths, _ := d.Headers["x-death"].([]any)
		if len(deaths) == 0 {
			return ""
		}
		death, _ := deaths[0].(amqp.Table)
		reason, _ := death["reason"].(string)
		return reason
	}
	p := &Publisher{URL: url, Timeout: 5 * time.Second}
	defer p.Close()
	publish := func(ids ...uint64) {
		t.Helper()
		events := make([]outbox.Event, len(ids))
		for i, id := range ids {
			events[i] = outbox.Event{ID: id, Type: outbox.TypeLikeCreated, Payload: fmt.Appendf(nil, `{"n":%d}`, id), CreatedAt: messageTime}
		}
		if confirmed, err := p.Publish(ctx, events); err != nil || !slices.Equal(confirmed, ids) {
			t.Fatalf("publishing %v: %v, %v", ids, confirmed, err)
		}
	}
	start := func(c *Consumer) func() {
		t.Helper()
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { c.Run(runCtx); close(done) }()
		return func() {
			t.Helper()
			stop()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the consumer did not stop after its context was canceled")
			}
		}
	}

	// Event 1 and 4 are handled, 2 is rejected, 3 fails twice before it is handled.
	var mu sync.Mutex
	tries := map[uint64]int{}
	handled := map[uint64]outbox.Event{}
	consumer := &Consumer{URL: url, MaxBackoff: 50 * time.Millisecond, Handler: func(_ context.Context, e outbox.Event) error {
		mu.Lock()
		defer mu.Unlock()
		tries[e.ID]++
		switch {
		case e.ID == 2:
			return fmt.Errorf("%w: test event", ErrReject)
		case e.ID == 3 && tries[e.ID] <= 2:
			return errors.New("database unavailable")
		}
		handled[e.ID] = e
		return nil
	}}
	isHandled := func(ids ...uint64) func() bool {
		return func() bool {
			mu.Lock()
			defer mu.Unlock()
			for _, id := range ids {
				if _, ok := handled[id]; !ok {
					return false
				}
			}
			return true
		}
	}
	stop := start(consumer)
	publish(1, 2, 3, 4)
	waitUntil("events 1, 3 and 4 are handled", isHandled(1, 3, 4))
	mu.Lock()
	if tries[1] != 1 || tries[2] != 1 || tries[3] != 3 || tries[4] != 1 {
		t.Fatalf("tries per event: %v", tries)
	}
	if e := handled[3]; e.Type != outbox.TypeLikeCreated || string(e.Payload) != `{"n":3}` || !e.CreatedAt.Equal(messageTime) {
		t.Fatalf("event 3 arrived as %+v", e)
	}
	mu.Unlock()
	dead, ok := get(DeadLetterQueue)
	if !ok || dead.MessageId != "2" || deathReason(dead) != "rejected" {
		t.Fatalf("event 2 must be dead-lettered as rejected, got %+v", dead)
	}
	if message, ok := get(NotificationsQueue); ok {
		t.Fatalf("handled events must be acknowledged, %s is still queued", message.MessageId)
	}

	// The queue disappears under the running consumer: it notices, reconnects, declares the queue
	// again and goes on consuming.
	if _, err := ch.QueueDelete(NotificationsQueue, false, false, false); err != nil {
		t.Fatal(err)
	}
	// Nothing else declares the queue meanwhile: the consumer has to declare and bind it again when
	// it reconnects. It subscribes only after the bindings, so a consumer on the queue means the
	// queue is ready. A passive declare of a missing queue closes its channel, so every check uses
	// a new one.
	waitUntil("the consumer declares the queue again and subscribes", func() bool {
		probe, err := conn.Channel()
		if err != nil {
			t.Fatal(err)
		}
		defer probe.Close()
		queue, err := probe.QueueDeclarePassive(NotificationsQueue, true, false, false, false, nil)
		return err == nil && queue.Consumers == 1
	})
	publish(5)
	waitUntil("the consumer resubscribes and handles event 5", isHandled(5))
	stop()

	// Prefetch 2: the consumer holds two unacknowledged messages, the other three stay ready.
	purge()
	holding := &Consumer{URL: url, Prefetch: 2, MaxBackoff: 50 * time.Millisecond, Handler: func(ctx context.Context, _ outbox.Event) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	stop = start(holding)
	publish(6, 7, 8, 9, 10)
	waitUntil("three messages are left ready", func() bool { return ready() == 3 })
	time.Sleep(300 * time.Millisecond)
	if n := ready(); n != 3 {
		t.Fatalf("prefetch 2 must leave three of five messages ready, %d are", n)
	}
	stop()
	waitUntil("the held messages are back in the queue", func() bool { return ready() == 5 })
	purge()

	// A consumer that stops before answering gives the message back, and each return counts as a
	// delivery: after six deliveries the message moves to the dead-letter queue instead of coming
	// back forever.
	publish(11)
	for round := 1; round <= 6; round++ {
		received := make(chan uint64, 1)
		crashing := &Consumer{URL: url, MaxBackoff: 50 * time.Millisecond, Handler: func(ctx context.Context, e outbox.Event) error {
			received <- e.ID
			<-ctx.Done()
			return ctx.Err()
		}}
		stop = start(crashing)
		select {
		case id := <-received:
			if id != 11 {
				t.Fatalf("round %d received event %d", round, id)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: event 11 was not delivered", round)
		}
		stop()
	}
	dead, ok = get(DeadLetterQueue)
	if !ok || dead.MessageId != "11" || deathReason(dead) != "delivery_limit" {
		t.Fatalf("event 11 must be dead-lettered after six deliveries, got %+v", dead)
	}
	if message, ok := get(NotificationsQueue); ok {
		t.Fatalf("event 11 must have left the queue, got %s", message.MessageId)
	}
}
