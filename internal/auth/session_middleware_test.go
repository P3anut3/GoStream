package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// fakeSessions answers Exists from a fixed set, or fails like a store whose Redis is down.
type fakeSessions struct {
	live  map[string]bool
	err   error
	calls int
}

func (f *fakeSessions) Exists(_ context.Context, sessionID string) (bool, error) {
	f.calls++
	return f.live[sessionID], f.err
}

func TestJWTAuthChecksTheSession(t *testing.T) {
	secret := []byte("test-only-session-secret-at-least-32-bytes")
	sign := func(sessionID string) string {
		t.Helper()
		token, err := GenerateToken(7, sessionID, secret)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	live, ended, legacy := sign("live-session"), sign("ended-session"), sign("")
	for _, tc := range []struct {
		name      string
		token     string
		storeErr  error
		want      int
		wantCalls int
	}{
		{name: "live_session", token: live, want: http.StatusOK, wantCalls: 1},
		{name: "logged_out_session", token: ended, want: http.StatusUnauthorized, wantCalls: 1},
		// Tokens issued before sessions existed carry no session and cannot be revoked.
		{name: "token_without_session", token: legacy, want: http.StatusUnauthorized},
		// If the store is down, the signature and expiry still decide: at worst for 15 minutes.
		{name: "store_down_trusts_the_signature", token: ended, storeErr: errors.New("redis down"), want: http.StatusOK, wantCalls: 1},
		// A token that can never be revoked is refused even then.
		{name: "store_down_still_refuses_tokens_without_session", token: legacy, storeErr: errors.New("redis down"), want: http.StatusUnauthorized},
	} {
		for _, optional := range []bool{false, true} {
			sessions := &fakeSessions{live: map[string]bool{"live-session": true}, err: tc.storeErr}
			middleware := JWTAuth(secret, sessions)
			if optional {
				middleware = OptionalJWTAuth(secret, sessions)
			}
			router := gin.New()
			router.GET("/me", middleware, func(c *gin.Context) {
				if c.GetUint64("userID") != 7 {
					t.Error("the verified user must be in the context")
				}
				c.Status(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodGet, "/me", nil)
			request.Header.Set("Authorization", "Bearer "+tc.token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.want || sessions.calls != tc.wantCalls {
				t.Fatalf("%s (optional %v): status %d, session checks %d; want %d, %d", tc.name, optional, response.Code, sessions.calls, tc.want, tc.wantCalls)
			}
		}
	}
}

func TestTokenCarriesTheSessionAndExpiresQuickly(t *testing.T) {
	secret := []byte("test-only-session-secret-at-least-32-bytes")
	token, err := GenerateToken(9, "session-9", secret)
	if err != nil {
		t.Fatal(err)
	}
	userID, sessionID, err := ParseToken(token, secret)
	if err != nil || userID != 9 || sessionID != "session-9" {
		t.Fatalf("ParseToken: user %d, session %q, err %v", userID, sessionID, err)
	}
	claims := &Claims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		t.Fatal(err)
	}
	if lifetime := claims.ExpiresAt.Sub(claims.IssuedAt.Time); lifetime != AccessTokenTTL || AccessTokenTTL != 15*time.Minute {
		t.Fatalf("access tokens must live 15 minutes, got %v", lifetime)
	}
}
