package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestUploadCleanupRunOnceOrderAndPartialFailures(t *testing.T) {
	ctx := context.Background()
	before := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	videoDir, coverDir := t.TempDir(), t.TempDir()
	recordFailure, claimFailure := errors.New("record deletion unavailable"), errors.New("claim unavailable")
	var events []string
	upload := func(id uint64) Upload {
		return Upload{ID: id, Status: "deleting", FileType: "video", SavedName: fmt.Sprintf("%032x.mp4", id)}
	}
	for _, id := range []uint64{1, 3, 6} {
		if err := os.WriteFile(filepath.Join(videoDir, upload(id).SavedName), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	repo := cleanupRepositoryStub{
		listDeleting: func(got context.Context, limit int) ([]Upload, error) {
			if got != ctx || limit != 3 {
				t.Error("incorrect retry query arguments")
			}
			events = append(events, "list_retry")
			return []Upload{{ID: 1}, {ID: 2}, {ID: 3}}, nil
		},
		listExpired: func(got context.Context, cutoff time.Time, limit int) ([]Upload, error) {
			if got != ctx || !cutoff.Equal(before) || limit != 3 {
				t.Error("incorrect candidate query arguments")
			}
			events = append(events, "list_pending")
			return []Upload{{ID: 4}, {ID: 5}, {ID: 6}}, nil
		},
		get: func(got context.Context, id uint64) (Upload, bool, error) {
			if got != ctx {
				t.Error("incorrect retry context")
			}
			events = append(events, fmt.Sprintf("get_%d", id))
			return upload(id), id != 2, nil
		},
		claim: func(got context.Context, id uint64, cutoff time.Time) (Upload, bool, error) {
			if got != ctx || !cutoff.Equal(before) {
				t.Error("incorrect claim arguments")
			}
			events = append(events, fmt.Sprintf("claim_%d", id))
			if id == 4 {
				return Upload{}, false, claimFailure
			}
			return upload(id), id != 5, nil
		},
		delete: func(got context.Context, id uint64) error {
			if got != ctx {
				t.Error("incorrect deletion context")
			}
			events = append(events, fmt.Sprintf("delete_%d", id))
			if _, err := os.Stat(filepath.Join(videoDir, upload(id).SavedName)); !os.IsNotExist(err) {
				t.Error("file must be deleted before record")
			}
			if id == 1 {
				return recordFailure
			}
			return nil
		},
	}
	report, err := NewUploadCleanupService(repo, videoDir, coverDir).RunOnce(ctx, before, 3)
	if report != (CleanupReport{Succeeded: 2, Skipped: 2, Failed: 2}) {
		t.Errorf("incorrect report: %+v", report)
	}
	if !errors.Is(err, recordFailure) || !errors.Is(err, claimFailure) {
		t.Error("both failure causes must be preserved")
	}
	want := []string{"list_retry", "get_1", "delete_1", "get_2", "get_3", "delete_3", "list_pending", "claim_4", "claim_5", "claim_6", "delete_6"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("incorrect execution order: %v", events)
	}
}

func TestUploadCleanupRunOnceValidationAndEmpty(t *testing.T) {
	for _, limit := range []int{-1, 0, 1001} {
		report, err := NewUploadCleanupService(nil, t.TempDir(), t.TempDir()).RunOnce(context.Background(), time.Now(), limit)
		if err == nil || report != (CleanupReport{}) {
			t.Errorf("limit %d must fail before repository access", limit)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if report, err := NewUploadCleanupService(nil, t.TempDir(), t.TempDir()).RunOnce(ctx, time.Now(), 1); !errors.Is(err, context.Canceled) || report != (CleanupReport{}) {
		t.Fatal("pre-canceled run must not access repository")
	}
	repo := cleanupRepositoryStub{
		listDeleting: func(context.Context, int) ([]Upload, error) { return []Upload{}, nil },
		listExpired:  func(context.Context, time.Time, int) ([]Upload, error) { return []Upload{}, nil },
	}
	if report, err := NewUploadCleanupService(repo, t.TempDir(), t.TempDir()).RunOnce(context.Background(), time.Now(), 100); err != nil || report != (CleanupReport{}) {
		t.Fatal("empty run must succeed with zero counts")
	}
}

func TestUploadCleanupRunOnceListFailures(t *testing.T) {
	first, second := errors.New("first query failed"), errors.New("second query failed")
	t.Run("retry_list_failure_stops_run", func(t *testing.T) {
		repo := cleanupRepositoryStub{listDeleting: func(context.Context, int) ([]Upload, error) { return nil, first }, listExpired: func(context.Context, time.Time, int) ([]Upload, error) {
			t.Error("must stop after retry list failure")
			return nil, nil
		}}
		if report, err := NewUploadCleanupService(repo, t.TempDir(), t.TempDir()).RunOnce(context.Background(), time.Now(), 1); !errors.Is(err, first) || report != (CleanupReport{}) {
			t.Fatal("must propagate retry query failure")
		}
	})
	t.Run("candidate_query_preserves_prior_failure", func(t *testing.T) {
		repo := cleanupRepositoryStub{
			listDeleting: func(context.Context, int) ([]Upload, error) { return []Upload{{ID: 1}}, nil },
			get:          func(context.Context, uint64) (Upload, bool, error) { return Upload{}, false, first },
			listExpired:  func(context.Context, time.Time, int) ([]Upload, error) { return nil, second },
		}
		report, err := NewUploadCleanupService(repo, t.TempDir(), t.TempDir()).RunOnce(context.Background(), time.Now(), 1)
		if report != (CleanupReport{Failed: 1}) || !errors.Is(err, first) || !errors.Is(err, second) {
			t.Fatal("must preserve partial report and both causes")
		}
	})
}

func TestUploadCleanupRunOnceCancellation(t *testing.T) {
	for _, phase := range []string{"retry", "pending"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := cleanupRepositoryStub{
				listDeleting: func(context.Context, int) ([]Upload, error) {
					if phase == "retry" {
						return []Upload{{ID: 1}, {ID: 2}}, nil
					}
					return nil, nil
				},
				get: func(context.Context, uint64) (Upload, bool, error) { calls++; cancel(); return Upload{}, false, nil },
				listExpired: func(context.Context, time.Time, int) ([]Upload, error) {
					if phase == "retry" {
						t.Error("cancellation must prevent the next list query")
					}
					return []Upload{{ID: 3}, {ID: 4}}, nil
				},
				claim: func(context.Context, uint64, time.Time) (Upload, bool, error) {
					calls++
					cancel()
					return Upload{}, false, nil
				},
			}
			report, err := NewUploadCleanupService(repo, t.TempDir(), t.TempDir()).RunOnce(ctx, time.Now(), 2)
			if calls != 1 || report != (CleanupReport{Skipped: 1}) || !errors.Is(err, context.Canceled) {
				t.Errorf("cancellation did not stop next item: calls=%d report=%+v err=%v", calls, report, err)
			}
		})
	}
}
