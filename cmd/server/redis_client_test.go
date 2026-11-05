package main

import (
	"context"
	"net"
	"testing"
	"time"
)

// While Redis is down a cache call must fail at once, so the request falls back to MySQL
// without waiting. go-redis retries a failed dial 5 times, 100ms apart, unless told otherwise.
func TestRedisClientFailsFastWhenRedisIsDown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close() // nothing listens on addr any more, so every dial is refused
	client := newRedisClient(addr, "")
	defer client.Close()
	start := time.Now()
	if err := client.Get(context.Background(), "gostream:video:v1:1").Err(); err == nil {
		t.Fatal("expected an error while Redis is down")
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("a refused connection must fail at once, took %v", elapsed)
	}
}
