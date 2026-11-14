package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	appconfig "gostream/internal/config"
	"gostream/internal/feed"
)

func newTestStorage(t *testing.T) appconfig.StorageConfig {
	t.Helper()
	root := t.TempDir()
	return appconfig.StorageConfig{
		VideoDir: filepath.Join(root, "video-files"),
		CoverDir: filepath.Join(root, "cover-files"),
	}
}

func newTestFeedCursor(t *testing.T) *feed.CursorCodec {
	t.Helper()
	codec, err := feed.NewCursorCodec([]byte("server-test-only-feed-secret-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func TestFeedRouteRejectsInvalidSuppliedToken(t *testing.T) {
	router := newRouter(nil, []byte("test-only-jwt-secret-at-least-32-bytes"), newTestFeedCursor(t), newTestStorage(t), nil, false, nil)
	request := httptest.NewRequest(http.MethodGet, "/feed?userID=1", nil)
	request.Header.Set("Authorization", "Bearer bad")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("expected 401 before database access, got %d", response.Code)
	}
}

func TestFeedRouteRejectsInvalidInputWithoutLogin(t *testing.T) {
	// Nil database also proves invalid input is rejected before querying storage.
	router := newRouter(nil, []byte("test-only-jwt-secret-at-least-32-bytes"), newTestFeedCursor(t), newTestStorage(t), nil, false, nil)
	for _, tc := range []struct{ path, code string }{
		{"/feed?limit=abc", "INVALID_ARGUMENT"},
		{"/feed?limit=-1", "INVALID_ARGUMENT"},
		{"/feed?cursor=bad", "INVALID_CURSOR"},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tc.path, nil))
		var body map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != 400 || body["code"] != tc.code {
			t.Fatalf("%s: got %d %s", tc.path, response.Code, response.Body.String())
		}
	}
}
