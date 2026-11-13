package mq

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"gostream/internal/outbox"
)

// Every event type the outbox writes needs a binding, or its messages would be returned and
// stay in the outbox forever.
func TestNotificationQueueReceivesEveryOutboxEventType(t *testing.T) {
	for _, eventType := range []string{outbox.TypeLikeCreated, outbox.TypeCommentCreated} {
		if !slices.Contains(NotificationEventTypes, eventType) {
			t.Errorf("%s has no binding to %s", eventType, NotificationsQueue)
		}
	}
}

func TestPublisherReportsAnUnreachableBroker(t *testing.T) {
	p := &Publisher{URL: "amqp://gostream:wrong@127.0.0.1:1/", Timeout: time.Second}
	defer p.Close()
	started := time.Now()
	confirmed, err := p.Publish(context.Background(), []outbox.Event{{ID: 1, Type: outbox.TypeLikeCreated, Payload: []byte(`{}`)}})
	if err == nil || len(confirmed) != 0 {
		t.Fatalf("an unreachable broker must confirm nothing, got %v, %v", confirmed, err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("errors must not contain the password")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("connecting must give up within the timeout")
	}
}

// TestPublisherRabbitMQIntegration publishes to a real RabbitMQ: confirmed events wait in the
// notifications queue as persistent messages carrying the event ID, type and payload; a message
// no queue receives is returned and not reported; a lost connection is restored on the next call;
// and a rejected message moves to the dead-letter queue that step 3 relies on.
// GOSTREAM_TEST_AMQP_URL must point to a disposable broker; the test empties its queues.
func TestPublisherRabbitMQIntegration(t *testing.T) {
	url := os.Getenv("GOSTREAM_TEST_AMQP_URL")
	if url == "" {
		t.Skip("set GOSTREAM_TEST_AMQP_URL to a disposable RabbitMQ")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	// Declaring twice is harmless: the worker declares on every connection.
	if err := DeclareTopology(ch); err != nil {
		t.Fatalf("declaring the same topology again must succeed: %v", err)
	}
	for _, queue := range []string{NotificationsQueue, DeadLetterQueue} {
		if _, err := ch.QueuePurge(queue, false); err != nil {
			t.Fatal(err)
		}
	}
	get := func(queue string) (amqp.Delivery, bool) {
		t.Helper()
		// Dead-lettering is asynchronous, so poll for a moment before giving up.
		for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			message, ok, err := ch.Get(queue, false)
			if err != nil {
				t.Fatal(err)
			}
			if ok || time.Now().After(deadline) {
				return message, ok
			}
		}
	}

	p := &Publisher{URL: url, Timeout: 5 * time.Second}
	defer p.Close()
	created := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	events := []outbox.Event{
		{ID: 1, Type: outbox.TypeLikeCreated, Payload: []byte(`{"actor_id":7,"author_id":3,"video_id":42}`), CreatedAt: created},
		{ID: 2, Type: outbox.TypeCommentCreated, Payload: []byte(`{"actor_id":7,"author_id":3,"video_id":42,"comment_id":9}`), CreatedAt: created},
	}
	confirmed, err := p.Publish(ctx, events)
	if err != nil || !slices.Equal(confirmed, []uint64{1, 2}) {
		t.Fatalf("both events must be confirmed: %v, %v", confirmed, err)
	}
	for _, event := range events {
		message, ok := get(NotificationsQueue)
		if !ok {
			t.Fatalf("event %d is missing from %s", event.ID, NotificationsQueue)
		}
		if message.MessageId != map[uint64]string{1: "1", 2: "2"}[event.ID] || message.Type != event.Type || message.RoutingKey != event.Type ||
			message.ContentType != "application/json" || message.DeliveryMode != amqp.Persistent ||
			string(message.Body) != string(event.Payload) || !message.Timestamp.Equal(created) {
			t.Fatalf("event %d arrived as %+v", event.ID, message)
		}
		if err := message.Ack(false); err != nil {
			t.Fatal(err)
		}
	}

	// A message that no queue receives is returned by RabbitMQ: it must not count as sent, while
	// the rest of the batch does.
	confirmed, err = p.Publish(ctx, []outbox.Event{
		{ID: 3, Type: outbox.TypeLikeCreated, Payload: []byte(`{}`)},
		{ID: 4, Type: "video.unbound", Payload: []byte(`{}`)},
		{ID: 5, Type: outbox.TypeCommentCreated, Payload: []byte(`{}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "4") || !slices.Equal(confirmed, []uint64{3, 5}) {
		t.Fatalf("the returned event must be left out: %v, %v", confirmed, err)
	}
	for range 2 {
		if message, ok := get(NotificationsQueue); !ok {
			t.Fatal("the routed events must arrive")
		} else if err := message.Ack(false); err != nil {
			t.Fatal(err)
		}
	}

	// After a failed batch the publisher drops its connection and starts over with a new one.
	if confirmed, err := p.Publish(ctx, []outbox.Event{{ID: 6, Type: outbox.TypeLikeCreated, Payload: []byte(`{}`)}}); err != nil || !slices.Equal(confirmed, []uint64{6}) {
		t.Fatalf("the publisher must start over after a failed batch: %v, %v", confirmed, err)
	}
	// A connection lost behind its back (a broker restart) is repaired on the next publish too.
	p.mu.Lock()
	lost := p.conn
	p.mu.Unlock()
	if lost == nil {
		t.Fatal("a successful publish must leave its connection open")
	}
	lost.Close()
	if confirmed, err := p.Publish(ctx, []outbox.Event{{ID: 7, Type: outbox.TypeLikeCreated, Payload: []byte(`{}`)}}); err != nil || !slices.Equal(confirmed, []uint64{7}) {
		t.Fatalf("the publisher must reconnect by itself: %v, %v", confirmed, err)
	}
	if message, ok := get(NotificationsQueue); !ok || message.MessageId != "6" {
		t.Fatalf("event 6 must be waiting, got %+v", message)
	} else if err := message.Ack(false); err != nil {
		t.Fatal(err)
	}

	// Step 3 relies on the dead-letter wiring: a message the consumer rejects moves to the
	// dead-letter queue instead of disappearing.
	message, ok := get(NotificationsQueue)
	if !ok || message.MessageId != "7" {
		t.Fatalf("event 7 must be waiting, got %+v", message)
	}
	if err := message.Reject(false); err != nil {
		t.Fatal(err)
	}
	dead, ok := get(DeadLetterQueue)
	if !ok || dead.MessageId != "7" || string(dead.Body) != "{}" {
		t.Fatalf("a rejected message must move to %s, got %+v", DeadLetterQueue, dead)
	}
	if err := dead.Ack(false); err != nil {
		t.Fatal(err)
	}
}
