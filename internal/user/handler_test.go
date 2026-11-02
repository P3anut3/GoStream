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
)

func TestRegisterHandler(t *testing.T) {
	for _, tc := range []struct {
		name, body            string
		repositoryError       error
		noService             bool
		wantStatus, wantCalls int
		wantMessage           string
	}{
		{name: "success", body: `{"username":"learner","password":"password123"}`, wantStatus: 201, wantCalls: 1},
		{name: "duplicate", body: `{"username":"learner","password":"password123"}`, repositoryError: ErrUsernameExists, wantStatus: 409, wantCalls: 1, wantMessage: ErrUsernameExists.Error()},
		{name: "storage_failure", body: `{"username":"learner","password":"password123"}`, repositoryError: errors.New("private storage details"), wantStatus: 500, wantCalls: 1, wantMessage: "注册失败"},
		{name: "empty_username", body: `{"password":"password123"}`, wantStatus: 400, wantMessage: ErrEmptyUsername.Error()},
		{name: "empty_password", body: `{"username":"learner"}`, wantStatus: 400, wantMessage: ErrEmptyPassword.Error()},
		{name: "short_password", body: `{"username":"learner","password":"123"}`, wantStatus: 400, wantMessage: ErrInvalidPasswordLength.Error()},
		{name: "malformed_json", body: `{"username":`, wantStatus: 400, wantMessage: "请求体不是有效的用户JSON"},
		{name: "wrong_field_type", body: `{"username":123}`, wantStatus: 400, wantMessage: "请求体不是有效的用户JSON"},
		{name: "missing_service", body: `{"username":"learner","password":"password123"}`, noService: true, wantStatus: 500, wantMessage: "用户服务未初始化"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repo := userRepositoryFunc(func(_ context.Context, user *User) error {
				calls++
				if user.Username != "learner" {
					t.Error("handler must forward request username")
				}
				user.ID = 77
				return tc.repositoryError
			})
			handler := &UserHandler{}
			if !tc.noService {
				handler.Service = NewUserService(repo)
			}
			router := gin.New()
			router.POST("/users", handler.CreateUser)
			req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != tc.wantStatus {
				t.Errorf("expected HTTP %d, got %d", tc.wantStatus, resp.Code)
			}
			if calls != tc.wantCalls {
				t.Errorf("expected %d repository calls, got %d", tc.wantCalls, calls)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
				t.Fatalf("handler must return exactly one JSON response: %v; body=%s", err, resp.Body)
			}
			if tc.wantStatus != 201 {
				var message string
				if json.Unmarshal(result["error"], &message) != nil || message != tc.wantMessage || len(result) != 1 {
					t.Fatal("incorrect or unsafe error response")
				}
				return
			}
			var id uint64
			var username string
			if len(result) != 2 || json.Unmarshal(result["id"], &id) != nil || json.Unmarshal(result["username"], &username) != nil || id != 77 || username != "learner" {
				t.Fatal("success must contain only public ID and username")
			}
		})
	}
}
