package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthzWithoutDatabaseOrLogin(t *testing.T) {
	// A nil database proves liveness does not depend on database availability.
	router := newRouter(nil, []byte("health-test-only-jwt-secret-at-least-32-bytes"), newTestFeedCursor(t), newTestStorage(t), nil, false)
	for _, authorization := range []string{"", "Bearer invalid-token"} {
		name := "anonymous"
		if authorization != "" {
			name = "invalid_credentials_do_not_block_probe"
		}
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			if authorization != "" {
				request.Header.Set("Authorization", authorization)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("expected 200 without a database or login, got %d", response.Code)
			}
			if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
				t.Fatal("liveness must return JSON")
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body["status"] != "ok" {
				t.Fatalf("unexpected liveness response: %s", response.Body.String())
			}
		})
	}
}
