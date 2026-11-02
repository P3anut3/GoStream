package follow

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// fakeRepository keeps Service tests away from the database; an unset method fails if called.
type fakeRepository struct {
	follow      func(context.Context, uint64, uint64) error
	unfollow    func(context.Context, uint64, uint64) error
	isFollowing func(context.Context, uint64, uint64) (bool, error)
}

func (f fakeRepository) Follow(ctx context.Context, followerID, followeeID uint64) error {
	if f.follow == nil {
		return errors.New("unexpected Follow")
	}
	return f.follow(ctx, followerID, followeeID)
}

func (f fakeRepository) Unfollow(ctx context.Context, followerID, followeeID uint64) error {
	if f.unfollow == nil {
		return errors.New("unexpected Unfollow")
	}
	return f.unfollow(ctx, followerID, followeeID)
}

func (f fakeRepository) IsFollowing(ctx context.Context, followerID, followeeID uint64) (bool, error) {
	if f.isFollowing == nil {
		return false, errors.New("unexpected IsFollowing")
	}
	return f.isFollowing(ctx, followerID, followeeID)
}

func TestServiceFollow(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name                       string
		repositoryError, wantError error
	}{
		{name: "first_follow"},
		{name: "duplicate_is_success", repositoryError: ErrAlreadyFollowing},
		{name: "wrapped_duplicate_is_success", repositoryError: fmt.Errorf("repository: %w", ErrAlreadyFollowing)},
		{name: "missing_target_preserved", repositoryError: ErrUserNotFound, wantError: ErrUserNotFound},
		{name: "database_error_preserved", repositoryError: failure, wantError: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			service := NewService(fakeRepository{follow: func(got context.Context, followerID, followeeID uint64) error {
				calls++
				if got != ctx || followerID != 12 || followeeID != 34 {
					t.Error("context and IDs must reach the repository unchanged")
				}
				return tc.repositoryError
			}})
			if err := service.Follow(ctx, 12, 34); !errors.Is(err, tc.wantError) {
				t.Fatalf("expected %v, got %v", tc.wantError, err)
			}
			if calls != 1 {
				t.Errorf("expected one repository call, got %d", calls)
			}
		})
	}
}

func TestServiceFollowRejectsSelfWithoutDatabase(t *testing.T) {
	service := NewService(fakeRepository{follow: func(context.Context, uint64, uint64) error {
		t.Error("self-follow must be rejected before reaching the repository")
		return nil
	}})
	if err := service.Follow(context.Background(), 12, 12); !errors.Is(err, ErrCannotFollowSelf) {
		t.Fatalf("expected ErrCannotFollowSelf, got %v", err)
	}
}

func TestServiceUnfollow(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name                       string
		followeeID                 uint64
		repositoryError, wantError error
	}{
		{name: "unfollow", followeeID: 34},
		// Unfollow has no self rule: that relation can never exist, so removing it is a no-op.
		{name: "self_unfollow_is_forwarded", followeeID: 12},
		{name: "database_error_preserved", followeeID: 34, repositoryError: failure, wantError: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			service := NewService(fakeRepository{unfollow: func(got context.Context, followerID, followeeID uint64) error {
				calls++
				if got != ctx || followerID != 12 || followeeID != tc.followeeID {
					t.Error("context and IDs must reach the repository unchanged")
				}
				return tc.repositoryError
			}})
			if err := service.Unfollow(ctx, 12, tc.followeeID); !errors.Is(err, tc.wantError) {
				t.Fatalf("expected %v, got %v", tc.wantError, err)
			}
			if calls != 1 {
				t.Errorf("expected one repository call, got %d", calls)
			}
		})
	}
}

func TestServiceIsFollowing(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, tc := range []struct {
		name            string
		following       bool
		repositoryError error
	}{
		{name: "following", following: true},
		{name: "not_following"},
		{name: "missing_target_preserved", repositoryError: ErrUserNotFound},
		{name: "database_error_preserved", repositoryError: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			service := NewService(fakeRepository{isFollowing: func(got context.Context, followerID, followeeID uint64) (bool, error) {
				calls++
				if got != ctx || followerID != 12 || followeeID != 34 {
					t.Error("context and IDs must reach the repository unchanged")
				}
				return tc.following, tc.repositoryError
			}})
			following, err := service.IsFollowing(ctx, 12, 34)
			if !errors.Is(err, tc.repositoryError) || following != tc.following {
				t.Fatalf("expected (%v, %v), got (%v, %v)", tc.following, tc.repositoryError, following, err)
			}
			if calls != 1 {
				t.Errorf("expected one repository call, got %d", calls)
			}
		})
	}
}
