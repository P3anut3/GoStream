package video

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cleanupRepositoryStub struct {
	claim        func(context.Context, uint64, time.Time) (Upload, bool, error)
	delete       func(context.Context, uint64) error
	get          func(context.Context, uint64) (Upload, bool, error)
	listDeleting func(context.Context, int) ([]Upload, error)
	listExpired  func(context.Context, time.Time, int) ([]Upload, error)
}

func (r cleanupRepositoryStub) ListDeleting(ctx context.Context, limit int) ([]Upload, error) {
	if r.listDeleting == nil {
		return nil, errors.New("unexpected retry list query")
	}
	return r.listDeleting(ctx, limit)
}

func (r cleanupRepositoryStub) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]Upload, error) {
	if r.listExpired == nil {
		return nil, errors.New("unexpected candidate list query")
	}
	return r.listExpired(ctx, before, limit)
}

func (r cleanupRepositoryStub) GetDeleting(ctx context.Context, id uint64) (Upload, bool, error) {
	if r.get == nil {
		return Upload{}, false, errors.New("first cleanup must not query retry records")
	}
	return r.get(ctx, id)
}

func (r cleanupRepositoryStub) ClaimExpired(ctx context.Context, id uint64, before time.Time) (Upload, bool, error) {
	return r.claim(ctx, id, before)
}
func (r cleanupRepositoryStub) DeleteClaimed(ctx context.Context, id uint64) error {
	return r.delete(ctx, id)
}

func TestUploadCleanupServiceCleanupOne(t *testing.T) {
	failure := errors.New("test database failure")
	for _, tc := range []struct {
		name                             string
		claimed, cover, missing, invalid bool
		claimError, deleteError          error
		wantDone, wantFile, wantError    bool
		wantDeleteCalls                  int
	}{
		{name: "video_cleanup", claimed: true, wantDone: true, wantDeleteCalls: 1},
		{name: "cover_cleanup", claimed: true, cover: true, wantDone: true, wantDeleteCalls: 1},
		{name: "missing_file_still_removes_record", claimed: true, missing: true, wantDone: true, wantDeleteCalls: 1},
		{name: "not_claimed_preserves_file_and_record", wantFile: true},
		{name: "claim_error_preserves_file_and_record", claimError: failure, wantFile: true, wantError: true},
		{name: "file_failure_preserves_record", claimed: true, invalid: true, wantFile: true, wantError: true},
		{name: "record_failure_leaves_file_deleted", claimed: true, deleteError: failure, wantError: true, wantDeleteCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			before := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			videoDir, coverDir := t.TempDir(), t.TempDir()
			kind, ext, dir := "video", ".mp4", videoDir
			if tc.cover {
				kind, ext, dir = "cover", ".png", coverDir
			}
			name := strings.Repeat("a", 32) + ext
			path := filepath.Join(dir, name)
			if !tc.missing {
				if err := os.WriteFile(path, []byte("test media"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			upload := Upload{ID: 45, Status: "deleting", FileType: kind, SavedName: name}
			if tc.invalid {
				upload.SavedName = "../" + name
			}
			claimCalls, deleteCalls := 0, 0
			repo := cleanupRepositoryStub{
				claim: func(got context.Context, id uint64, cutoff time.Time) (Upload, bool, error) {
					claimCalls++
					if got != ctx || id != 45 || !cutoff.Equal(before) {
						t.Error("must forward context, ID and cutoff")
					}
					if !tc.missing {
						if _, err := os.Stat(path); err != nil {
							t.Error("file must remain until cleanup rights are acquired")
						}
					}
					return upload, tc.claimed, tc.claimError
				},
				delete: func(got context.Context, id uint64) error {
					deleteCalls++
					if got != ctx || id != 45 {
						t.Error("must delete the claimed upload record")
					}
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Error("file must be removed before deleting database record")
					}
					return tc.deleteError
				},
			}
			done, err := NewUploadCleanupService(repo, videoDir, coverDir).CleanupOne(ctx, 45, before)
			if done != tc.wantDone || (err != nil) != tc.wantError {
				t.Fatalf("expected done=%v error=%v, got done=%v err=%v", tc.wantDone, tc.wantError, done, err)
			}
			if tc.claimError != nil && !errors.Is(err, tc.claimError) {
				t.Error("claim error cause must be preserved")
			}
			if tc.deleteError != nil && !errors.Is(err, tc.deleteError) {
				t.Error("delete error cause must be preserved")
			}
			if claimCalls != 1 || deleteCalls != tc.wantDeleteCalls {
				t.Errorf("unexpected calls: claim=%d delete=%d", claimCalls, deleteCalls)
			}
			_, statErr := os.Stat(path)
			if tc.wantFile {
				if statErr != nil {
					t.Error("file must be preserved")
				}
			} else if !os.IsNotExist(statErr) {
				t.Error("file must be absent")
			}
		})
	}
}
