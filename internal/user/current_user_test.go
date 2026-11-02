package user

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gostream/internal/auth"
	"gostream/internal/database"
)

func TestGORMRepositoryFindByID(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name                  string
		found                 bool
		queryError, wantError error
	}{
		{name: "found", found: true},
		{name: "missing", wantError: ErrUserNotFound},
		{name: "database_failure", queryError: failure, wantError: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pool := sql.OpenDB(userRepositoryConnector{query: func(got context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
				if got != ctx {
					t.Error("repository must preserve context")
				}
				query = strings.ReplaceAll(strings.Join(strings.Fields(query), " "), "`", "")
				if query != "SELECT id,username FROM users WHERE id = ? LIMIT ?" {
					t.Errorf("current user lookup should only read public fields: %s", query)
				}
				if len(args) != 2 || args[0].Value != int64(77) || args[1].Value != int64(1) {
					t.Error("expected bound user ID 77")
				}
				if tc.queryError != nil {
					return nil, tc.queryError
				}
				rows := &userRepositoryRows{columns: []string{"id", "username"}}
				if tc.found {
					rows.values = []driver.Value{int64(77), "learner"}
				}
				return rows, nil
			}})
			defer pool.Close()
			user, err := (&GORMRepository{DB: database.WrapSQL(pool)}).FindByID(ctx, 77)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("expected %v, got %v", tc.wantError, err)
			}
			if err == nil && (user.ID != 77 || user.Username != "learner" || user.PasswordHash != "") {
				t.Error("incorrect public user fields")
			}
			if err != nil && user != (User{}) {
				t.Error("failed lookup must return empty user")
			}
		})
	}
}

type currentUserRepositoryFunc func(context.Context, uint64) (User, error)

func (f currentUserRepositoryFunc) FindByID(ctx context.Context, id uint64) (User, error) {
	return f(ctx, id)
}
func (f currentUserRepositoryFunc) CreateUser(context.Context, *User) error {
	return errors.New("profile must not create users")
}
func (f currentUserRepositoryFunc) FindByUsername(context.Context, string) (User, error) {
	return User{}, errors.New("profile must use verified ID")
}

func TestUserServiceGetCurrentUser(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, wantError := range []error{nil, ErrUserNotFound, failure} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		repo := currentUserRepositoryFunc(func(got context.Context, id uint64) (User, error) {
			calls++
			if got != ctx || id != 77 {
				t.Error("service must forward context and verified ID")
			}
			if wantError != nil {
				return User{}, wantError
			}
			return User{ID: 77, Username: "learner"}, nil
		})
		user, err := NewUserService(repo).GetCurrentUser(ctx, 77)
		cancel()
		if !errors.Is(err, wantError) || calls != 1 {
			t.Fatal("service must preserve lookup result and error")
		}
		if err == nil && user.ID != 77 {
			t.Error("incorrect current user")
		}
	}
}

func TestCurrentUserHandler(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(77, secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, token           string
		repositoryError       error
		noService             bool
		wantStatus, wantCalls int
		wantMessage           string
	}{
		{name: "verified_identity_ignores_query_id", token: token, wantStatus: 200, wantCalls: 1},
		{name: "no_token", wantStatus: 401, wantMessage: "请提供 Bearer token"},
		{name: "invalid_token", token: "invalid", wantStatus: 401, wantMessage: "登录凭证无效或已过期"},
		{name: "deleted_user", token: token, repositoryError: ErrUserNotFound, wantStatus: 401, wantCalls: 1, wantMessage: "当前用户已经不存在"},
		{name: "database_failure", token: token, repositoryError: errors.New("private storage details"), wantStatus: 500, wantCalls: 1, wantMessage: "查询用户失败"},
		{name: "missing_service", token: token, noService: true, wantStatus: 500, wantMessage: "用户服务未初始化"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := currentUserRepositoryFunc(func(got context.Context, id uint64) (User, error) {
				calls++
				if got != ctx || id != 77 {
					t.Error("must use JWT identity rather than URL userID=999")
				}
				return User{ID: 77, Username: "learner", PasswordHash: "must-not-be-exposed"}, tc.repositoryError
			})
			handler := &UserHandler{}
			if !tc.noService {
				handler.Service = NewUserService(repo)
			}
			router := gin.New()
			router.GET("/me", auth.JWTAuth(secret), handler.Me)
			req := httptest.NewRequest(http.MethodGet, "/me?userID=999", nil).WithContext(ctx)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != tc.wantStatus || calls != tc.wantCalls {
				t.Fatalf("expected status=%d calls=%d, got %d %d", tc.wantStatus, tc.wantCalls, resp.Code, calls)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
				t.Fatal("must return exactly one JSON response")
			}
			if tc.wantStatus != 200 {
				var message string
				if len(result) != 1 || json.Unmarshal(result["error"], &message) != nil || message != tc.wantMessage {
					t.Fatal("incorrect or unsafe error response")
				}
				return
			}
			var id uint64
			var username string
			if len(result) != 2 || json.Unmarshal(result["id"], &id) != nil || json.Unmarshal(result["username"], &username) != nil || id != 77 || username != "learner" {
				t.Fatal("profile must only expose public ID and username")
			}
		})
	}
}

func TestCurrentUserHandlerRejectsInvalidContextIdentity(t *testing.T) {
	for _, value := range []any{nil, "77", uint64(0)} {
		router := gin.New()
		handler := &UserHandler{}
		router.GET("/me", func(c *gin.Context) {
			if value != nil {
				c.Set("userID", value)
			}
			c.Next()
		}, handler.Me)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/me", nil))
		if resp.Code != 500 || !strings.Contains(resp.Body.String(), "请求中缺少有效的用户身份") {
			t.Error("missing or malformed context identity must be rejected before accessing service")
		}
	}
}
