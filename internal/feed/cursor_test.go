package feed

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func testCursorCodec(t *testing.T) *CursorCodec {
	t.Helper()
	codec, err := NewCursorCodec([]byte("feed-test-only-secret-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func TestCursorRoundTrip(t *testing.T) {
	codec := testCursorCodec(t)
	want := Position{CreatedAt: time.Date(2026, 10, 20, 15, 30, 0, 123456789, time.FixedZone("UTC+8", 8*3600)), ID: ^uint64(0)}
	token, err := codec.Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) > 1024 || strings.ContainsAny(token, "+/= \n") {
		t.Fatal("token must be URL safe and bounded")
	}
	got, err := codec.Decode(token)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || !got.CreatedAt.Equal(want.CreatedAt) || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("position changed during round trip: %+v", got)
	}
	if got, err := codec.Decode(""); err != nil || got != nil {
		t.Fatal("empty cursor must indicate first batch")
	}
}

func TestCursorRejectsModifiedTokens(t *testing.T) {
	codec := testCursorCodec(t)
	token, err := codec.Encode(Position{CreatedAt: time.Now(), ID: 24})
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewCursorCodec([]byte("different-test-only-secret-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Decode(token); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("wrong signing key must fail")
	}
	for _, token := range []string{"0", "abc", ".abc", "abc.", token + ".extra", "x" + token, strings.Repeat("x", 1025)} {
		if got, err := codec.Decode(token); !errors.Is(err, ErrInvalidCursor) || got != nil {
			t.Error("malformed or modified token must fail")
		}
	}
}

func TestCursorValidSignatureStillRequiresValidPayload(t *testing.T) {
	codec := testCursorCodec(t)
	for _, payload := range []string{
		`{"v":2,"kind":"latest","created_at":"2026-10-20T10:00:00Z","id":24}`,
		`{"v":1,"kind":"recommended","created_at":"2026-10-20T10:00:00Z","id":24}`,
		`{"v":1,"kind":"latest","created_at":"2026-10-20T10:00:00Z","id":0}`,
		`{"v":1,"kind":"latest","id":24}`,
		`{"v":1,"kind":"latest","created_at":"invalid","id":24}`,
		`{"v":1,"kind":"latest","created_at":"2026-10-20T10:00:00Z","id":-1}`,
		`{"v":1,"kind":"latest","created_at":"2026-10-20T10:00:00Z","id":24,"extra":true}`,
		`{"v":1,"kind":"latest","created_at":"2026-10-20T10:00:00Z","id":24} {}`,
		`null`, `not-json`,
	} {
		t.Run(payload, func(t *testing.T) {
			encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
			token := encoded + "." + base64.RawURLEncoding.EncodeToString(codec.sign(encoded))
			if _, err := codec.Decode(token); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("invalid payload accepted: %v", err)
			}
		})
	}
}

func TestCursorInvalidPositionAndKey(t *testing.T) {
	for _, key := range [][]byte{nil, []byte("too-short")} {
		if _, err := NewCursorCodec(key); err == nil {
			t.Fatal("short key must fail")
		}
	}
	codec := testCursorCodec(t)
	for _, position := range []Position{{}, {ID: 24}, {CreatedAt: time.Now()}} {
		if _, err := codec.Encode(position); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("incomplete position must fail")
		}
	}
	key := []byte("feed-test-only-secret-at-least-32-bytes")
	codec, err := NewCursorCodec(key)
	if err != nil {
		t.Fatal(err)
	}
	position := Position{CreatedAt: time.Now(), ID: 24}
	before, err := codec.Encode(position)
	if err != nil {
		t.Fatal(err)
	}
	key[0] ^= 1
	after, err := codec.Encode(position)
	if err != nil || before != after {
		t.Fatal("caller mutation must not change the codec's key")
	}
}
