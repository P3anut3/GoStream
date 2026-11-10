package ratelimit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

var loginRule = Rule{Name: "login", Limit: 3, Window: time.Minute}

func newTestRedisLimiter(t *testing.T) (*RedisLimiter, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	// Like production: no command retries and a single dial attempt, so failures return at once.
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1, DialerRetries: 1})
	t.Cleanup(func() { client.Close() })
	return &RedisLimiter{Client: client}, server
}

func allow(t *testing.T, limiter Limiter, rule Rule, key string) Decision {
	t.Helper()
	decision, err := limiter.Allow(context.Background(), rule, key)
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func TestRedisLimiterFixedWindow(t *testing.T) {
	limiter, server := newTestRedisLimiter(t)
	const counter = "gostream:ratelimit:v1:login:ip:192.0.2.1"
	for i := 1; i <= 3; i++ {
		if !allow(t, limiter, loginRule, "ip:192.0.2.1").Allowed {
			t.Fatalf("request %d must be allowed", i)
		}
	}
	if ttl := server.TTL(counter); ttl != time.Minute {
		t.Fatalf("the window starts at the first request: TTL %v", ttl)
	}
	// Later requests in the same window must not push its end back.
	server.FastForward(20 * time.Second)
	decision := allow(t, limiter, loginRule, "ip:192.0.2.1")
	if decision.Allowed || decision.RetryAfter <= 39*time.Second || decision.RetryAfter > 40*time.Second {
		t.Fatalf("the 4th request must wait for the rest of the window (40s), got %+v", decision)
	}
	if ttl := server.TTL(counter); ttl > 40*time.Second {
		t.Fatalf("rejected requests must not extend the window: TTL %v", ttl)
	}
	// When the window ends the counter expires and counting starts again from 1.
	server.FastForward(41 * time.Second)
	if !allow(t, limiter, loginRule, "ip:192.0.2.1").Allowed {
		t.Fatal("a new window must allow requests again")
	}
	if value, err := server.Get(counter); err != nil || value != "1" {
		t.Fatalf("the new window must count from 1, got %q (%v)", value, err)
	}
}

func TestRedisLimiterKeepsRulesAndIdentitiesApart(t *testing.T) {
	limiter, _ := newTestRedisLimiter(t)
	for range 3 {
		allow(t, limiter, loginRule, "ip:192.0.2.1")
	}
	if allow(t, limiter, loginRule, "ip:192.0.2.1").Allowed {
		t.Fatal("the 4th login from the same IP must be rejected")
	}
	write := Rule{Name: "write", Limit: 3, Window: time.Minute}
	if !allow(t, limiter, loginRule, "ip:192.0.2.2").Allowed || !allow(t, limiter, write, "ip:192.0.2.1").Allowed {
		t.Fatal("other identities and other rules must have their own counters")
	}
}

func TestRedisLimiterHealsCounterWithoutTTL(t *testing.T) {
	limiter, server := newTestRedisLimiter(t)
	const counter = "gostream:ratelimit:v1:login:ip:192.0.2.1"
	// A counter left without an expiry (say by an INCR whose EXPIRE never ran) would block forever.
	server.Set(counter, "99")
	if allow(t, limiter, loginRule, "ip:192.0.2.1").Allowed {
		t.Fatal("the counter is over the limit")
	}
	if ttl := server.TTL(counter); ttl != time.Minute {
		t.Fatalf("EXPIRE NX must give such a counter a TTL, got %v", ttl)
	}
}

func TestRedisLimiterReportsRedisFailure(t *testing.T) {
	limiter, server := newTestRedisLimiter(t)
	server.Close()
	if _, err := limiter.Allow(context.Background(), loginRule, "ip:192.0.2.1"); err == nil {
		t.Fatal("Allow must fail while Redis is down")
	}
}

func TestLocalLimiterFixedWindow(t *testing.T) {
	limiter := NewLocalLimiter()
	rule := Rule{Name: "login", Limit: 3, Window: 400 * time.Millisecond}
	start := time.Now()
	for i := 1; i <= 3; i++ {
		if !allow(t, limiter, rule, "ip:192.0.2.1").Allowed {
			t.Fatalf("request %d must be allowed", i)
		}
	}
	decision := allow(t, limiter, rule, "ip:192.0.2.1")
	if decision.Allowed || decision.RetryAfter <= 0 || decision.RetryAfter > rule.Window {
		t.Fatalf("the 4th request must wait for the rest of the window, got %+v", decision)
	}
	if !allow(t, limiter, rule, "ip:192.0.2.2").Allowed || !allow(t, limiter, Rule{Name: "write", Limit: 3, Window: time.Minute}, "ip:192.0.2.1").Allowed {
		t.Fatal("other identities and other rules must have their own counters")
	}
	// A rejected request in the middle of the window must not push its end back.
	time.Sleep(time.Until(start.Add(250 * time.Millisecond)))
	if allow(t, limiter, rule, "ip:192.0.2.1").Allowed {
		t.Fatal("still inside the first window")
	}
	time.Sleep(time.Until(start.Add(500 * time.Millisecond)))
	if !allow(t, limiter, rule, "ip:192.0.2.1").Allowed {
		t.Fatal("a new window must start when the first one ends")
	}
}

func TestLocalLimiterCountsConcurrentRequestsExactly(t *testing.T) {
	limiter := NewLocalLimiter()
	rule := Rule{Name: "write", Limit: 100, Window: time.Minute}
	var allowed atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 100 {
		wg.Go(func() {
			<-start // release every goroutine at once so the counts really collide
			for range 20 {
				if decision, _ := limiter.Allow(context.Background(), rule, "user:7"); decision.Allowed {
					allowed.Add(1)
				}
			}
		})
	}
	close(start)
	wg.Wait()
	// Reading, adding one and writing back must happen as one step, or increments get lost.
	if allowed.Load() != 100 {
		t.Fatalf("exactly 100 of 2000 concurrent requests may pass, got %d", allowed.Load())
	}
}

// fakeLimiter returns a fixed answer and counts how often it was asked.
type fakeLimiter struct {
	calls    atomic.Int64
	decision Decision
	err      error
}

func (f *fakeLimiter) Allow(context.Context, Rule, string) (Decision, error) {
	f.calls.Add(1)
	return f.decision, f.err
}

func TestFailoverLimiterUsesTheFallbackOnlyWhenRedisFails(t *testing.T) {
	fallback := &fakeLimiter{decision: Decision{RetryAfter: time.Second}}
	failover := &FailoverLimiter{Primary: &fakeLimiter{decision: Decision{Allowed: true}}, Fallback: fallback}
	if !allow(t, failover, loginRule, "ip:192.0.2.1").Allowed || fallback.calls.Load() != 0 {
		t.Fatal("while Redis works its answer is used and the fallback is not touched")
	}
	failover.Primary = &fakeLimiter{err: errors.New("redis down")}
	if decision := allow(t, failover, loginRule, "ip:192.0.2.1"); decision.Allowed || decision.RetryAfter != time.Second || fallback.calls.Load() != 1 {
		t.Fatalf("when Redis fails the fallback must decide, got %+v", decision)
	}
}

func TestFailoverLimiterKeepsLimitingWhileRedisIsDown(t *testing.T) {
	redisLimiter, server := newTestRedisLimiter(t)
	server.Close()
	failover := &FailoverLimiter{Primary: redisLimiter, Fallback: NewLocalLimiter()}
	for i := 1; i <= 3; i++ {
		if !allow(t, failover, loginRule, "ip:192.0.2.1").Allowed {
			t.Fatalf("request %d must be allowed by the local count", i)
		}
	}
	if allow(t, failover, loginRule, "ip:192.0.2.1").Allowed {
		t.Fatal("the local count must still reject the 4th request")
	}
}

func TestDecide(t *testing.T) {
	if !decide(loginRule, 3, 0).Allowed {
		t.Fatal("the last request within the limit is allowed")
	}
	// PTTL reports -1 or -2 when it has no expiry to show: wait for a whole window then.
	if decision := decide(loginRule, 4, -1); decision.Allowed || decision.RetryAfter != time.Minute {
		t.Fatalf("got %+v", decision)
	}
}
