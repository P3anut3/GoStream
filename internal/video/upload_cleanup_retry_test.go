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

func TestUploadCleanupServiceRetryAfterRecordFailure(t *testing.T) {
	ctx := context.Background()
	videoDir, coverDir := t.TempDir(), t.TempDir()
	name := strings.Repeat("b", 32) + ".mp4"
	path := filepath.Join(videoDir, name)
	if err := os.WriteFile(path, []byte("test media"), 0600); err != nil {
		t.Fatal(err)
	}
	upload := Upload{ID: 45, FileType: "video", SavedName: name, Status: "deleting"}
	failure := errors.New("temporary database failure")
	claimCalls, getCalls, deleteCalls := 0, 0, 0
	recordExists := true
	repo := cleanupRepositoryStub{
		claim: func(context.Context, uint64, time.Time) (Upload, bool, error) { claimCalls++; return upload, true, nil },
		get: func(got context.Context, id uint64) (Upload, bool, error) {
			getCalls++
			if got != ctx || id != 45 {
				t.Error("retry must forward context and upload ID")
			}
			return upload, recordExists, nil
		},
		delete: func(context.Context, uint64) error {
			deleteCalls++
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Error("file must be absent before deleting record")
			}
			if deleteCalls == 1 {
				return failure
			}
			recordExists = false
			return nil
		},
	}
	service := NewUploadCleanupService(repo, videoDir, coverDir)
	if done, err := service.CleanupOne(ctx, 45, time.Now()); done || !errors.Is(err, failure) {
		t.Fatal("first attempt must preserve record on database failure")
	}
	if !recordExists {
		t.Fatal("retry information must survive")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("first attempt should already have removed file")
	}
	if done, err := service.RetryOne(ctx, 45); !done || err != nil {
		t.Fatalf("retry should complete despite missing file: %v", err)
	}
	if recordExists {
		t.Fatal("successful retry must remove record")
	}
	if done, err := service.RetryOne(ctx, 45); done || err != nil {
		t.Fatal("already completed cleanup must safely skip")
	}
	if claimCalls != 1 || getCalls != 2 || deleteCalls != 2 {
		t.Errorf("unexpected calls: claim=%d get=%d delete=%d", claimCalls, getCalls, deleteCalls)
	}
}

func TestUploadCleanupServiceRetryGuards(t *testing.T) {
	failure := errors.New("lookup unavailable")
	for _, tc := range []struct {
		name        string
		found       bool
		lookupError error
		invalid     bool
	}{
		{name: "missing_record"},
		{name: "lookup_failure", lookupError: failure},
		{name: "invalid_file_preserves_record", found: true, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			videoDir, coverDir := t.TempDir(), t.TempDir()
			name := strings.Repeat("c", 32) + ".mp4"
			path := filepath.Join(videoDir, name)
			if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			upload := Upload{ID: 45, Status: "deleting", FileType: "video", SavedName: name}
			if tc.invalid {
				upload.SavedName = "../" + name
			}
			repo := cleanupRepositoryStub{
				claim: func(context.Context, uint64, time.Time) (Upload, bool, error) {
					t.Error("retry must not reclaim pending files")
					return Upload{}, false, nil
				},
				get: func(context.Context, uint64) (Upload, bool, error) { return upload, tc.found, tc.lookupError },
				delete: func(context.Context, uint64) error {
					t.Error("failed or skipped retry must preserve record")
					return nil
				},
			}
			done, err := NewUploadCleanupService(repo, videoDir, coverDir).RetryOne(context.Background(), 45)
			if done || (err != nil) != (tc.lookupError != nil || tc.invalid) {
				t.Fatalf("unexpected result done=%v err=%v", done, err)
			}
			if tc.lookupError != nil && !errors.Is(err, tc.lookupError) {
				t.Error("lookup error cause must be preserved")
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
				t.Fatal("file must remain unchanged")
			}
		})
	}
}
