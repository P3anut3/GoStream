package auth

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestOptionalJWTAuth(t *testing.T) {
	secret := []byte("test-only-optional-secret-at-least-32-bytes")
	token, err := GenerateToken(7, "", secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, header string
		supplied     bool
		status       int
		id           uint64
	}{
		{"guest", "", false, 200, 0}, {"valid", "Bearer " + token, true, 200, 7}, {"empty", "", true, 401, 0}, {"malformed", "bad", true, 401, 0}, {"invalid", "Bearer bad", true, 401, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			r := gin.New()
			r.GET("/feed", OptionalJWTAuth(secret, nil), func(c *gin.Context) {
				called = true
				v, exists := c.Get("userID")
				if tc.id == 0 && exists {
					t.Fatal("guest has identity")
				}
				if tc.id > 0 && v != tc.id {
					t.Fatal("incorrect identity")
				}
				c.Status(200)
			})
			req := httptest.NewRequest("GET", "/feed?userID=999", nil)
			if tc.supplied {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status || called != (tc.status == 200) {
				t.Fatalf("status=%d called=%v", w.Code, called)
			}
		})
	}
}
