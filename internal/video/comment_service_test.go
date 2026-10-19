package video

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A function can implement the small repository contract in a test.
type commentRepositoryFunc func(context.Context, uint64, uint64, string) (uint64, error)

func (f commentRepositoryFunc) CreateComment(ctx context.Context, userID, videoID uint64, content string) (uint64, error) {
	return f(ctx, userID, videoID, content)
}

func TestCommentServiceCreateComment(t *testing.T) {
	databaseFailure := errors.New("test repository unavailable")
	for _, tc := range []struct {
		name, input, stored        string
		repositoryError, wantError error
		wantCalls                  int
	}{
		{name: "empty_rejected_before_storage", input: "", wantError: ErrInvalidCommentContent},
		{name: "whitespace_rejected_before_storage", input: " \t\n　", wantError: ErrInvalidCommentContent},
		{name: "501_unicode_characters_rejected", input: strings.Repeat("评", 501), wantError: ErrInvalidCommentContent},
		{name: "trim_before_storage_and_response", input: "  好视频！　", stored: "好视频！", wantCalls: 1},
		{name: "500_chinese_characters_allowed", input: strings.Repeat("评", 500), stored: strings.Repeat("评", 500), wantCalls: 1},
		{name: "500_emoji_allowed", input: strings.Repeat("😀", 500), stored: strings.Repeat("😀", 500), wantCalls: 1},
		{name: "missing_video_error_propagated", input: "hello", stored: "hello", repositoryError: ErrCommentVideoNotFound, wantError: ErrCommentVideoNotFound, wantCalls: 1},
		{name: "storage_error_propagated", input: "hello", stored: "hello", repositoryError: databaseFailure, wantError: databaseFailure, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repository := commentRepositoryFunc(func(gotContext context.Context, userID, videoID uint64, content string) (uint64, error) {
				calls++
				if gotContext != ctx || userID != 12 || videoID != 34 || content != tc.stored {
					t.Error("service must pass context, IDs and normalized content to repository")
				}
				return 56, tc.repositoryError
			})
			service := NewCommentService(repository)
			comment, err := service.CreateComment(ctx, 12, 34, tc.input)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("expected error %v, got %v", tc.wantError, err)
			}
			if calls != tc.wantCalls {
				t.Errorf("expected %d storage calls, got %d", tc.wantCalls, calls)
			}
			if err == nil && (comment.ID != 56 || comment.UserID != 12 || comment.VideoID != 34 || comment.Content != tc.stored) {
				t.Error("service result must contain the persisted ID, identities and normalized content")
			}
			if err != nil && comment != (CreatedComment{}) {
				t.Error("failure must not return a successful comment result")
			}
		})
	}
}
