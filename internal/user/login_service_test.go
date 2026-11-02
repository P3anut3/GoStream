package user

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type loginRepositoryFunc func(context.Context, string) (User, error)

func (f loginRepositoryFunc) FindByID(context.Context, uint64) (User, error) {
	return User{}, errors.New("login must not query current user by ID")
}

func (f loginRepositoryFunc) FindByUsername(ctx context.Context, username string) (User, error) {
	return f(ctx, username)
}
func (f loginRepositoryFunc) CreateUser(context.Context, *User) error {
	return errors.New("login must not create users")
}

func TestUserServiceLogin(t *testing.T) {
	hash, err := HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name, username, password   string
		repositoryError, wantError error
		wantCalls                  int
	}{
		{name: "empty_username", password: "password123", wantError: ErrEmptyCredentials},
		{name: "empty_password", username: "learner", wantError: ErrEmptyCredentials},
		{name: "correct_password", username: "learner", password: "password123", wantCalls: 1},
		{name: "wrong_password", username: "learner", password: "wrong", wantCalls: 1, wantError: ErrInvalidCredentials},
		{name: "missing_user", username: "missing", password: "password123", repositoryError: ErrUserNotFound, wantError: ErrInvalidCredentials, wantCalls: 1},
		{name: "wrapped_missing_user", username: "missing", password: "password123", repositoryError: fmt.Errorf("lookup: %w", ErrUserNotFound), wantError: ErrInvalidCredentials, wantCalls: 1},
		{name: "database_error", username: "learner", password: "password123", repositoryError: failure, wantError: failure, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := loginRepositoryFunc(func(got context.Context, username string) (User, error) {
				calls++
				if got != ctx || username != tc.username {
					t.Error("must forward request context and username")
				}
				return User{ID: 77, Username: username, PasswordHash: hash}, tc.repositoryError
			})
			user, err := NewUserService(repo).Login(ctx, tc.username, tc.password)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("expected %v, got %v", tc.wantError, err)
			}
			if calls != tc.wantCalls {
				t.Errorf("expected %d queries, got %d", tc.wantCalls, calls)
			}
			if err != nil && user != (User{}) {
				t.Error("failed authentication must not return authenticated user")
			}
			if err == nil && (user.ID != 77 || user.Username != tc.username) {
				t.Error("incorrect authenticated user")
			}
		})
	}
}
