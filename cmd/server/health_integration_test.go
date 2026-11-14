package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"gostream/internal/database"
)

func TestHealthRoutesMySQLIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_INTEGRATION_TEST") != "1" {
		t.Skip("set GOSTREAM_INTEGRATION_TEST=1 for local MySQL")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read local database configuration")
	}
	db, err := database.OpenGORM(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("cannot connect to configured MySQL")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal("cannot obtain test connection pool")
	}
	t.Cleanup(func() { pool.Close() })
	router := newRouter(db, []byte("health-integration-test-jwt-secret-at-least-32-bytes"), newTestFeedCursor(t), newTestStorage(t), nil, false, nil)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 3 * time.Second}
	probe := func(path string, wantCode int, status string) {
		t.Helper()
		response, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal("health HTTP request failed")
		}
		assertProbeResponse(t, response, wantCode, status, path == "/readyz")
	}
	probe("/healthz", 200, "ok")
	probe("/readyz", 200, "ok")
	// Close only this test's pool; do not stop MySQL or alter business data.
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	probe("/readyz", 503, "not_ready")
	probe("/healthz", 200, "ok")
}
