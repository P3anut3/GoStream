package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gostream/internal/database"
)

// Only Ping is supported: readiness must not query or modify business tables.
type probeConnector struct{ ping func(context.Context) error }

func (c probeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return probeConnection{ping: c.ping}, nil
}
func (probeConnector) Driver() driver.Driver { return probeDriver{} }

type probeDriver struct{}

func (probeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type probeConnection struct{ ping func(context.Context) error }

func (c probeConnection) Ping(ctx context.Context) error { return c.ping(ctx) }
func (probeConnection) Close() error                     { return nil }
func (probeConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected query")
}
func (probeConnection) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }

func newProbePool(t *testing.T, ping func(context.Context) error) *sql.DB {
	t.Helper()
	pool := sql.OpenDB(probeConnector{ping: ping})
	t.Cleanup(func() { pool.Close() })
	return pool
}

func assertProbeResponse(t *testing.T, response *http.Response, wantCode int, wantStatus string, noStore bool) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != wantCode {
		t.Fatalf("expected status %d, got %d", wantCode, response.StatusCode)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatal("probe must return JSON")
	}
	if noStore && response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("readiness response must not be cached")
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal("invalid probe response JSON")
	}
	if len(body) != 1 || body["status"] != wantStatus {
		t.Fatal("probe must return only the documented status, without internal error details")
	}
}

func requestProbe(t *testing.T, router http.Handler, path string, wantCode int, status string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	// Public probes must not depend on parsing login credentials.
	request.Header.Set("Authorization", "Bearer invalid-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertProbeResponse(t, response.Result(), wantCode, status, path == "/readyz")
}

func TestReadinessFailureAndRecovery(t *testing.T) {
	var calls atomic.Int64
	var unavailable atomic.Bool
	pool := newProbePool(t, func(context.Context) error {
		calls.Add(1)
		if unavailable.Load() {
			return errors.New("private-database-error-and-credential-marker")
		}
		return nil
	})
	router := newRouter(database.WrapSQL(pool), []byte("health-test-only-jwt-secret-at-least-32-bytes"), newTestFeedCursor(t), newTestStorage(t), nil, false, nil)
	if calls.Load() != 0 {
		t.Fatal("route registration must not ping the database")
	}
	requestProbe(t, router, "/healthz", 200, "ok")
	if calls.Load() != 0 {
		t.Fatal("liveness must not ping the database")
	}
	requestProbe(t, router, "/readyz", 200, "ok")
	unavailable.Store(true)
	requestProbe(t, router, "/readyz", 503, "not_ready")
	requestProbe(t, router, "/healthz", 200, "ok")
	unavailable.Store(false)
	requestProbe(t, router, "/readyz", 200, "ok")
	if calls.Load() != 3 {
		t.Fatalf("expected exactly one ping per readiness request, got %d", calls.Load())
	}
	if err := pool.PingContext(context.Background()); err != nil {
		t.Fatal("readiness must leave the shared connection pool usable")
	}
}

func TestReadinessUnavailablePool(t *testing.T) {
	closed := newProbePool(t, func(context.Context) error { t.Error("closed pool must not call the driver"); return nil })
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		db   *gorm.DB
	}{
		{"nil_database", nil},
		{"no_sql_pool", &gorm.DB{Config: &gorm.Config{}}},
		{"closed_sql_pool", database.WrapSQL(closed)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newRouter(tc.db, []byte("health-test-only-jwt-secret-at-least-32-bytes"), newTestFeedCursor(t), newTestStorage(t), nil, false, nil)
			requestProbe(t, router, "/readyz", 503, "not_ready")
			requestProbe(t, router, "/healthz", 200, "ok")
		})
	}
}

type probeContextKey struct{}

func TestReadinessTimeout(t *testing.T) {
	var cause error
	pool := newProbePool(t, func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 1200*time.Millisecond || ctx.Value(probeContextKey{}) != "request-marker" {
			cause = errors.New("probe must derive a one-second deadline from the request context")
			return cause
		}
		select {
		case <-ctx.Done():
			cause = ctx.Err()
		case <-time.After(3 * time.Second):
			cause = errors.New("probe did not time out")
		}
		return cause
	})
	router := newRouter(database.WrapSQL(pool), []byte("health-test-only-jwt-secret-at-least-32-bytes"), newTestFeedCursor(t), newTestStorage(t), nil, false, nil)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	request = request.WithContext(context.WithValue(request.Context(), probeContextKey{}, "request-marker"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertProbeResponse(t, response.Result(), 503, "not_ready", true)
	if !errors.Is(cause, context.DeadlineExceeded) {
		t.Fatalf("expected database probe deadline, got %v", cause)
	}
}

func TestReadinessRequestCancellation(t *testing.T) {
	started, done := make(chan struct{}), make(chan struct{})
	causes := make(chan error, 1)
	pool := newProbePool(t, func(ctx context.Context) error {
		close(started)
		var err error
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(3 * time.Second):
			err = errors.New("request cancellation did not reach database probe")
		}
		causes <- err
		return err
	})
	router := newRouter(database.WrapSQL(pool), []byte("health-test-only-jwt-secret-at-least-32-bytes"), newTestFeedCursor(t), newTestStorage(t), nil, false, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	go func() { router.ServeHTTP(response, request); close(done) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("probe never reached the database")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("canceled readiness request did not finish")
	}
	assertProbeResponse(t, response.Result(), 503, "not_ready", true)
	if cause := <-causes; !errors.Is(cause, context.Canceled) {
		t.Fatalf("expected request cancellation, got %v", cause)
	}
}
