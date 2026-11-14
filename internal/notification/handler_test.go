package notification

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// serve runs one request through a router that registers route like main.go does, with userID
// put into the context the way the JWT middleware does (nil leaves it out).
func serve(repo *fakeRepository, method, route, path string, userID any, handle func(*Handler) gin.HandlerFunc) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Handle(method, route, func(c *gin.Context) {
		if userID != nil {
			c.Set("userID", userID)
		}
	}, handle(NewHandler(NewService(repo))))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	return response
}

func list(h *Handler) gin.HandlerFunc        { return h.List }
func unreadCount(h *Handler) gin.HandlerFunc { return h.UnreadCount }
func markRead(h *Handler) gin.HandlerFunc    { return h.MarkRead }
func markAllRead(h *Handler) gin.HandlerFunc { return h.MarkAllRead }

func expectPrivate(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Vary") != "Authorization" {
		t.Fatalf("a per-user response must not be cached by others, headers %v", response.Header())
	}
}

func TestHandlerListReturnsTheCurrentUsersPage(t *testing.T) {
	comment := uint64(5)
	content := "拍得真好"
	found := []Item{
		{ID: 9, Type: "comment.created", ActorID: 7, ActorName: "bob", VideoID: 42, VideoTitle: "第二个视频", CommentID: &comment, CommentContent: &content, CreatedAt: eventTime},
		{ID: 8, Type: "comment.created", ActorID: 7, ActorName: "bob", VideoID: 42, VideoTitle: "第二个视频", CommentID: &comment, CreatedAt: eventTime, Read: true},
		{ID: 7, Type: "like.created", ActorID: 7, ActorName: "bob", VideoID: 42, VideoTitle: "第二个视频", CreatedAt: eventTime},
	}
	repo := &fakeRepository{items: found}
	// The recipient comes only from the context: a user ID in the query is ignored.
	response := serve(repo, "GET", "/notifications", "/notifications?limit=2&cursor=10&user_id=99", uint64(3), list)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", response.Code, response.Body)
	}
	expectPrivate(t, response)
	if !slices.Equal(repo.calls, []string{"List recipient=3 before=10 limit=3"}) {
		t.Fatalf("calls %v", repo.calls)
	}
	want := `{"notifications":[` +
		`{"id":9,"type":"comment.created","actor_id":7,"actor_name":"bob","video_id":42,"video_title":"第二个视频","comment_id":5,"comment_content":"拍得真好","created_at":"2025-11-12T20:30:00Z","read":false},` +
		`{"id":8,"type":"comment.created","actor_id":7,"actor_name":"bob","video_id":42,"video_title":"第二个视频","comment_id":5,"comment_content":null,"created_at":"2025-11-12T20:30:00Z","read":true}` +
		`],"next_cursor":"8","has_more":true}`
	if response.Body.String() != want {
		t.Fatalf("body\n%s\nwant\n%s", response.Body, want)
	}
}

func TestHandlerListWithoutNotifications(t *testing.T) {
	response := serve(&fakeRepository{}, "GET", "/notifications", "/notifications", uint64(3), list)
	if response.Code != http.StatusOK || response.Body.String() != `{"notifications":[],"next_cursor":"","has_more":false}` {
		t.Fatalf("got %d %s", response.Code, response.Body)
	}
}

func TestHandlerListRejectsBadParameters(t *testing.T) {
	for path, wantError := range map[string]string{
		"/notifications?limit=abc":  "查询参数格式错误",
		"/notifications?limit=51":   ErrInvalidLimit.Error(),
		"/notifications?limit=-1":   ErrInvalidLimit.Error(),
		"/notifications?cursor=abc": ErrInvalidCursor.Error(),
		"/notifications?cursor=0":   ErrInvalidCursor.Error(),
	} {
		t.Run(path, func(t *testing.T) {
			repo := &fakeRepository{}
			response := serve(repo, "GET", "/notifications", path, uint64(3), list)
			var body map[string]string
			if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &body) != nil || body["error"] != wantError {
				t.Fatalf("expected 400 %q, got %d %s", wantError, response.Code, response.Body)
			}
			if len(repo.calls) != 0 {
				t.Fatalf("bad parameters must not reach the database, got %v", repo.calls)
			}
		})
	}
}

func TestHandlerUnreadCount(t *testing.T) {
	repo := &fakeRepository{unread: 5}
	response := serve(repo, "GET", "/notifications/unread-count", "/notifications/unread-count", uint64(3), unreadCount)
	if response.Code != http.StatusOK || response.Body.String() != `{"count":5}` {
		t.Fatalf("got %d %s", response.Code, response.Body)
	}
	expectPrivate(t, response)
	if !slices.Equal(repo.calls, []string{"CountUnread recipient=3"}) {
		t.Fatalf("calls %v", repo.calls)
	}
}

func TestHandlerMarkRead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		repoErr  error
		want     int
		wantCall string
	}{
		{name: "marked", path: "/notifications/7/read", want: http.StatusNoContent, wantCall: "MarkRead recipient=3 id=7"},
		{name: "not_mine_or_missing", path: "/notifications/7/read", repoErr: ErrNotFound, want: http.StatusNotFound, wantCall: "MarkRead recipient=3 id=7"},
		{name: "text_id", path: "/notifications/abc/read", want: http.StatusBadRequest},
		{name: "zero_id", path: "/notifications/0/read", want: http.StatusBadRequest},
		{name: "negative_id", path: "/notifications/-7/read", want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepository{err: tc.repoErr}
			response := serve(repo, "PUT", "/notifications/:id/read", tc.path, uint64(3), markRead)
			if response.Code != tc.want {
				t.Fatalf("expected %d, got %d %s", tc.want, response.Code, response.Body)
			}
			if tc.want == http.StatusNotFound && !strings.Contains(response.Body.String(), ErrNotFound.Error()) {
				t.Fatalf("a missing notification must say so, got %s", response.Body)
			}
			if want := []string{tc.wantCall}; tc.wantCall == "" && len(repo.calls) != 0 || tc.wantCall != "" && !slices.Equal(repo.calls, want) {
				t.Fatalf("calls %v, want %q", repo.calls, tc.wantCall)
			}
		})
	}
}

func TestHandlerMarkAllRead(t *testing.T) {
	for _, tc := range []struct {
		path     string
		want     int
		wantCall string
	}{
		{path: "/notifications/read?up_to=9", want: http.StatusNoContent, wantCall: "MarkAllRead recipient=3 upTo=9"},
		{path: "/notifications/read", want: http.StatusBadRequest},
		{path: "/notifications/read?up_to=abc", want: http.StatusBadRequest},
		{path: "/notifications/read?up_to=0", want: http.StatusBadRequest},
	} {
		t.Run(tc.path, func(t *testing.T) {
			repo := &fakeRepository{}
			response := serve(repo, "PUT", "/notifications/read", tc.path, uint64(3), markAllRead)
			if response.Code != tc.want {
				t.Fatalf("expected %d, got %d %s", tc.want, response.Code, response.Body)
			}
			if tc.wantCall == "" && len(repo.calls) != 0 || tc.wantCall != "" && !slices.Equal(repo.calls, []string{tc.wantCall}) {
				t.Fatalf("calls %v, want %q", repo.calls, tc.wantCall)
			}
		})
	}
}

// TestHandlerDatabaseErrors: a failing database answers 500 without its details.
func TestHandlerDatabaseErrors(t *testing.T) {
	down := errors.New("dial tcp 10.0.0.5:3306: secret detail")
	for name, tc := range map[string]struct {
		method, route, path string
		handle              func(*Handler) gin.HandlerFunc
	}{
		"list":          {"GET", "/notifications", "/notifications", list},
		"unread_count":  {"GET", "/notifications/unread-count", "/notifications/unread-count", unreadCount},
		"mark_read":     {"PUT", "/notifications/:id/read", "/notifications/7/read", markRead},
		"mark_all_read": {"PUT", "/notifications/read", "/notifications/read?up_to=9", markAllRead},
	} {
		t.Run(name, func(t *testing.T) {
			response := serve(&fakeRepository{err: down}, tc.method, tc.route, tc.path, uint64(3), tc.handle)
			if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("expected a plain 500, got %d %s", response.Code, response.Body)
			}
		})
	}
}

// TestHandlerRequiresAnIdentity: without a user from the JWT middleware every endpoint refuses to
// guess one; that can only happen when a route lacks the middleware, so it is a server error.
func TestHandlerRequiresAnIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		method, route, path string
		handle              func(*Handler) gin.HandlerFunc
	}{
		"list":          {"GET", "/notifications", "/notifications", list},
		"unread_count":  {"GET", "/notifications/unread-count", "/notifications/unread-count", unreadCount},
		"mark_read":     {"PUT", "/notifications/:id/read", "/notifications/7/read", markRead},
		"mark_all_read": {"PUT", "/notifications/read", "/notifications/read?up_to=9", markAllRead},
	} {
		for _, userID := range []any{nil, uint64(0), "3", 3} {
			repo := &fakeRepository{}
			response := serve(repo, tc.method, tc.route, tc.path, userID, tc.handle)
			if response.Code != http.StatusInternalServerError || len(repo.calls) != 0 {
				t.Fatalf("%s with user %#v: expected 500 and no database call, got %d %v", name, userID, response.Code, repo.calls)
			}
		}
	}
}
