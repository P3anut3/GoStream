package user

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type userRepositoryFunc func(context.Context, *User) error

func (f userRepositoryFunc) FindByID(context.Context, uint64) (User, error) {
	return User{}, errors.New("registration must not query current user")
}

func (f userRepositoryFunc) CreateUser(ctx context.Context, user *User) error { return f(ctx, user) }

func (f userRepositoryFunc) FindByUsername(context.Context, string) (User, error) {
	return User{}, errors.New("registration must not query login credentials")
}

func TestUserServiceRegister(t *testing.T) {
	storageFailure := errors.New("repository unavailable")
	for _, tc := range []struct {
		name, username, password   string
		repositoryError, wantError error
		wantCalls                  int
	}{
		{name: "empty_username", password: "password123", wantError: ErrEmptyUsername},
		{name: "empty_password", username: "learner", wantError: ErrEmptyPassword},
		{name: "short_password", username: "learner", password: "1234567", wantError: ErrInvalidPasswordLength},
		{name: "long_password", username: "learner", password: strings.Repeat("a", 73), wantError: ErrInvalidPasswordLength},
		{name: "unicode_byte_limit", username: "learner", password: strings.Repeat("密", 25), wantError: ErrInvalidPasswordLength},
		{name: "minimum_length", username: "learner", password: "12345678", wantCalls: 1},
		{name: "maximum_length", username: "learner", password: strings.Repeat("a", 72), wantCalls: 1},
		{name: "duplicate_username_propagated", username: "learner", password: "password123", repositoryError: ErrUsernameExists, wantError: ErrUsernameExists, wantCalls: 1},
		{name: "storage_failure_propagated", username: "learner", password: "password123", repositoryError: storageFailure, wantError: storageFailure, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := userRepositoryFunc(func(got context.Context, user *User) error {
				calls++
				if got != ctx || user.Username != tc.username {
					t.Error("context and username must reach repository unchanged")
				}
				if user.PasswordHash == tc.password || user.PasswordHash == "" {
					t.Error("must persist a hash rather than plaintext")
				}
				if err := CheckPassword(user.PasswordHash, tc.password); err != nil {
					t.Error("stored hash must match submitted password")
				}
				if err := CheckPassword(user.PasswordHash, "wrong-password"); err == nil {
					t.Error("stored hash must reject incorrect password")
				}
				user.ID = 77
				return tc.repositoryError
			})
			user, err := NewUserService(repo).Register(ctx, tc.username, tc.password)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("expected %v, got %v", tc.wantError, err)
			}
			if calls != tc.wantCalls {
				t.Errorf("expected %d storage calls, got %d", tc.wantCalls, calls)
			}
			if err != nil && user != (User{}) {
				t.Error("failure must not return a successful user")
			}
			if err == nil && (user.ID != 77 || user.Username != tc.username || user.PasswordHash == "") {
				t.Error("result must preserve saved ID, username and hash")
			}
		})
	}
}
