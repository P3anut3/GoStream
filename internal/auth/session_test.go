package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisSessions(t *testing.T) (*RedisSessionStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	// Like production: no command retries and a single dial attempt, so failures return at once.
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1, DialerRetries: 1})
	t.Cleanup(func() { client.Close() })
	return &RedisSessionStore{Client: client}, server
}

// eachStore runs the same behaviour against both implementations.
func eachStore(t *testing.T, test func(t *testing.T, store SessionStore)) {
	t.Run("redis", func(t *testing.T) {
		store, _ := newTestRedisSessions(t)
		test(t, store)
	})
	t.Run("memory", func(t *testing.T) { test(t, NewMemorySessionStore()) })
}

func TestSessionLifecycle(t *testing.T) {
	eachStore(t, func(t *testing.T, store SessionStore) {
		ctx := context.Background()
		sessionID, refresh, err := store.Create(ctx, 42)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessionID) != 32 || !strings.HasPrefix(refresh, sessionID+".") {
			t.Fatalf("refresh token %q must be <32-char session ID>.<secret>", refresh)
		}
		if exists, err := store.Exists(ctx, sessionID); err != nil || !exists {
			t.Fatalf("a new session must exist: %v, %v", exists, err)
		}
		// Each refresh returns the user and a fresh token for the same session.
		userID, rotatedID, next, err := store.Rotate(ctx, refresh)
		if err != nil || userID != 42 || rotatedID != sessionID || next == refresh || !strings.HasPrefix(next, sessionID+".") {
			t.Fatalf("rotate: user %d, session %q, token %q, err %v", userID, rotatedID, next, err)
		}
		_, _, latest, err := store.Rotate(ctx, next)
		if err != nil || latest == next {
			t.Fatalf("the new token must refresh again: %q, %v", latest, err)
		}
		// Logging out deletes the session, so none of its tokens can refresh any more.
		if err := store.Delete(ctx, sessionID); err != nil {
			t.Fatal(err)
		}
		if exists, err := store.Exists(ctx, sessionID); err != nil || exists {
			t.Fatalf("a deleted session must be gone: %v, %v", exists, err)
		}
		if _, _, _, err := store.Rotate(ctx, latest); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("refreshing a deleted session must fail with ErrSessionNotFound, got %v", err)
		}
		if err := store.Delete(ctx, sessionID); err != nil {
			t.Fatalf("deleting twice must succeed: %v", err)
		}
	})
}

func TestSessionReuseRevokesTheSession(t *testing.T) {
	eachStore(t, func(t *testing.T, store SessionStore) {
		ctx := context.Background()
		sessionID, stolen, err := store.Create(ctx, 42)
		if err != nil {
			t.Fatal(err)
		}
		// The real user refreshes first; the thief's copy is now the previous token.
		_, _, current, err := store.Rotate(ctx, stolen)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := store.Rotate(ctx, stolen); !errors.Is(err, ErrRefreshTokenReused) {
			t.Fatalf("replaying a rotated token must be detected, got %v", err)
		}
		if exists, _ := store.Exists(ctx, sessionID); exists {
			t.Fatal("reuse must delete the whole session")
		}
		if _, _, _, err := store.Rotate(ctx, current); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("after reuse even the newest token is dead, got %v", err)
		}
	})
}

func TestSessionRejectsInvalidTokens(t *testing.T) {
	eachStore(t, func(t *testing.T, store SessionStore) {
		ctx := context.Background()
		sessionID, refresh, err := store.Create(ctx, 42)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			token string
			want  error
		}{
			{token: "", want: ErrInvalidRefreshToken},
			{token: "no-dot-at-all", want: ErrInvalidRefreshToken},
			{token: "short.secret", want: ErrInvalidRefreshToken},
			{token: strings.Repeat("z", 32) + ".secret", want: ErrInvalidRefreshToken},   // not hex
			{token: "abcd.secret", want: ErrInvalidRefreshToken},                         // hex, but too short
			{token: strings.Repeat("a", 4096) + ".secret", want: ErrInvalidRefreshToken}, // never becomes a huge Redis key
			{token: sessionID + ".", want: ErrInvalidRefreshToken},
			{token: strings.Repeat("a", 32) + ".secret", want: ErrSessionNotFound},
			{token: sessionID + ".a-guessed-secret", want: ErrInvalidRefreshToken},
		} {
			if _, _, _, err := store.Rotate(ctx, tc.token); !errors.Is(err, tc.want) {
				t.Fatalf("token %q: want %v, got %v", tc.token, tc.want, err)
			}
		}
		// Wrong guesses must neither revoke the session nor change its token.
		if _, _, _, err := store.Rotate(ctx, refresh); err != nil {
			t.Fatalf("the real token must still work after wrong guesses: %v", err)
		}
	})
}

func TestSessionRotationIsAtomic(t *testing.T) {
	eachStore(t, func(t *testing.T, store SessionStore) {
		ctx := context.Background()
		_, refresh, err := store.Create(ctx, 42)
		if err != nil {
			t.Fatal(err)
		}
		var rotated atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 20 {
			wg.Go(func() {
				<-start // release every refresh at once
				_, _, _, err := store.Rotate(ctx, refresh)
				switch {
				case err == nil:
					rotated.Add(1)
				case errors.Is(err, ErrRefreshTokenReused), errors.Is(err, ErrSessionNotFound):
				default:
					t.Errorf("unexpected error %v", err)
				}
			})
		}
		close(start)
		wg.Wait()
		// "Compare, then replace" happens as one step: only one refresh can win the token.
		if rotated.Load() != 1 {
			t.Fatalf("exactly one of 20 concurrent refreshes may succeed, got %d", rotated.Load())
		}
	})
}

func TestRedisSessionStoresOnlyHashesAndSlidesItsTTL(t *testing.T) {
	store, server := newTestRedisSessions(t)
	ctx := context.Background()
	sessionID, refresh, err := store.Create(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	key := "gostream:auth:v1:session:" + sessionID
	secret := strings.TrimPrefix(refresh, sessionID+".")
	if stored := server.HGet(key, "refresh"); stored != hashSecret(secret) || strings.Contains(stored, secret) {
		t.Fatalf("Redis must keep only the SHA-256 of the secret, got %q", stored)
	}
	if user := server.HGet(key, "user"); user != "42" {
		t.Fatalf("the session must remember its user, got %q", user)
	}
	if ttl := server.TTL(key); ttl != SessionTTL {
		t.Fatalf("a new session lasts %v, TTL %v", SessionTTL, ttl)
	}
	// Every refresh restarts the idle timer.
	server.FastForward(3 * 24 * time.Hour)
	if _, _, _, err := store.Rotate(ctx, refresh); err != nil {
		t.Fatal(err)
	}
	if ttl := server.TTL(key); ttl != SessionTTL {
		t.Fatalf("refreshing must restart the %v window, TTL %v", SessionTTL, ttl)
	}
	if previous := server.HGet(key, "previous"); previous != hashSecret(secret) {
		t.Fatalf("the replaced hash must be kept to detect reuse, got %q", previous)
	}
	// An idle session simply expires.
	server.FastForward(SessionTTL + time.Second)
	if exists, err := store.Exists(ctx, sessionID); err != nil || exists {
		t.Fatalf("an idle session must expire: %v, %v", exists, err)
	}
}

func TestRedisSessionReportsRedisFailures(t *testing.T) {
	store, server := newTestRedisSessions(t)
	ctx := context.Background()
	sessionID, refresh, err := store.Create(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	sessionErrors := []error{ErrSessionNotFound, ErrInvalidRefreshToken, ErrRefreshTokenReused}
	isSessionError := func(err error) bool {
		for _, target := range sessionErrors {
			if errors.Is(err, target) {
				return true
			}
		}
		return false
	}
	// A broken store is not "logged out": callers must be able to tell the difference.
	if _, _, err := store.Create(ctx, 42); err == nil {
		t.Fatal("Create must fail while Redis is down")
	}
	if _, _, _, err := store.Rotate(ctx, refresh); err == nil || isSessionError(err) {
		t.Fatalf("Rotate must report the outage, not a session error: %v", err)
	}
	if _, err := store.Exists(ctx, sessionID); err == nil {
		t.Fatal("Exists must fail while Redis is down")
	}
	if err := store.Delete(ctx, sessionID); err == nil {
		t.Fatal("Delete must fail while Redis is down")
	}
}
