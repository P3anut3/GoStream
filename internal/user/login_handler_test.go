package user

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gostream/internal/auth"
)

func TestLoginHandler(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	hash, err := HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body            string
		repositoryError       error
		noService             bool
		wantStatus, wantCalls int
		wantMessage           string
	}{
		{name: "success", body: `{"username":"learner","password":"password123"}`, wantStatus: 200, wantCalls: 1},
		{name: "wrong_password", body: `{"username":"learner","password":"wrong"}`, wantStatus: 401, wantCalls: 1, wantMessage: ErrInvalidCredentials.Error()},
		{name: "missing_user", body: `{"username":"learner","password":"password123"}`, repositoryError: ErrUserNotFound, wantStatus: 401, wantCalls: 1, wantMessage: ErrInvalidCredentials.Error()},
		{name: "database_failure", body: `{"username":"learner","password":"password123"}`, repositoryError: errors.New("private database details"), wantStatus: 500, wantCalls: 1, wantMessage: "查询用户失败"},
		{name: "empty_credentials", body: `{}`, wantStatus: 400, wantMessage: ErrEmptyCredentials.Error()},
		{name: "malformed_json", body: `{"username":`, wantStatus: 400, wantMessage: "请求体格式错误"},
		{name: "wrong_field_type", body: `{"username":123}`, wantStatus: 400, wantMessage: "请求体格式错误"},
		{name: "missing_service", body: `{"username":"learner","password":"password123"}`, noService: true, wantStatus: 500, wantMessage: "用户服务未初始化"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := loginRepositoryFunc(func(got context.Context, username string) (User, error) {
				calls++
				if got != ctx || username != "learner" {
					t.Error("request context and username must reach repository")
				}
				return User{ID: 77, Username: username, PasswordHash: hash}, tc.repositoryError
			})
			handler := &UserHandler{JWTSecret: secret}
			if !tc.noService {
				handler.Service = NewUserService(repo)
			}
			router := gin.New()
			router.POST("/login", handler.Login)
			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(tc.body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != tc.wantStatus || calls != tc.wantCalls {
				t.Fatalf("expected status=%d calls=%d, got status=%d calls=%d", tc.wantStatus, tc.wantCalls, resp.Code, calls)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
				t.Fatalf("must return exactly one JSON response: %v", err)
			}
			if tc.wantStatus != 200 {
				var message string
				if len(result) != 1 || json.Unmarshal(result["error"], &message) != nil || message != tc.wantMessage {
					t.Fatal("failed login must return only safe error, without a token")
				}
				return
			}
			var id uint64
			var username, token string
			if len(result) != 3 || json.Unmarshal(result["id"], &id) != nil || json.Unmarshal(result["username"], &username) != nil || json.Unmarshal(result["token"], &token) != nil || id != 77 || username != "learner" {
				t.Fatal("success must contain only ID, username and token")
			}
			if userID, err := auth.ParseToken(token, secret); err != nil || userID != 77 {
				t.Fatal("token must authenticate the validated user")
			}
			if _, err := auth.ParseToken(token, []byte("another-test-secret-key-at-least-32-bytes")); err == nil {
				t.Fatal("different secret must reject login token")
			}
		})
	}
}
