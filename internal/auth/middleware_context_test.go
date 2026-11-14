package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// TestJWTAuthPassesTheSessionAndExpiry: a long-lived response such as the notification stream is
// checked only when it starts, so besides the user it needs the token's session, to check it
// again later, and the token's expiry, not to outlive it.
func TestJWTAuthPassesTheSessionAndExpiry(t *testing.T) {
	secret := []byte("test-only-session-secret-at-least-32-bytes")
	token, err := GenerateToken(7, "live-session", secret)
	if err != nil {
		t.Fatal(err)
	}
	claims := &Claims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		t.Fatal(err)
	}
	if until := time.Until(claims.ExpiresAt.Time); until <= 14*time.Minute || until > AccessTokenTTL {
		t.Fatalf("a fresh token must expire in about 15 minutes, got %v", until)
	}
	for _, tc := range []struct {
		name     string
		sessions SessionChecker
	}{
		{name: "signature_only"},
		{name: "with_sessions", sessions: &fakeSessions{live: map[string]bool{"live-session": true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			router := gin.New()
			router.GET("/stream", JWTAuth(secret, tc.sessions), func(c *gin.Context) {
				called = true
				if got := c.GetUint64("userID"); got != 7 {
					t.Errorf("userID: got %d, want 7", got)
				}
				if got := c.GetString("sessionID"); got != "live-session" {
					t.Errorf("sessionID: got %q, want the token's session", got)
				}
				if got := c.GetTime("tokenExpiresAt"); !got.Equal(claims.ExpiresAt.Time) {
					t.Errorf("tokenExpiresAt: got %v, want the token's expiry %v", got, claims.ExpiresAt.Time)
				}
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequest(http.MethodGet, "/stream", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent || !called {
				t.Fatalf("a valid token must reach the handler, got %d", response.Code)
			}
		})
	}
}
