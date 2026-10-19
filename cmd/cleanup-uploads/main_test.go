package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gostream/internal/video"
)

type commandRepository struct {
	deleting func(context.Context, int) ([]video.Upload, error)
	expired  func(context.Context, time.Time, int) ([]video.Upload, error)
}

func (r commandRepository) ListDeleting(ctx context.Context, limit int) ([]video.Upload, error) {
	return r.deleting(ctx, limit)
}
func (r commandRepository) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]video.Upload, error) {
	return r.expired(ctx, before, limit)
}

type commandRunner func(context.Context, time.Time, int) (video.CleanupReport, error)

func (f commandRunner) RunOnce(ctx context.Context, before time.Time, limit int) (video.CleanupReport, error) {
	return f(ctx, before, limit)
}

func TestPreviewNeverExecutesCleanup(t *testing.T) {
	for _, args := range [][]string{nil, {"--limit=20", "--timeout=2s"}, {"--execute=false"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			wantLimit := 100
			if len(args) > 0 && args[0] == "--limit=20" {
				wantLimit = 20
			}
			calls, closed := 0, false
			var cutoff time.Time
			factory := func() (cleanupRuntime, error) {
				return cleanupRuntime{
					repository: commandRepository{
						deleting: func(ctx context.Context, limit int) ([]video.Upload, error) {
							calls++
							if limit != wantLimit {
								t.Error("incorrect retry limit")
							}
							if _, ok := ctx.Deadline(); !ok {
								t.Error("command needs timeout")
							}
							return []video.Upload{{ID: 1, Status: "deleting", FileType: "video", SavedName: "fixture.mp4"}}, nil
						},
						expired: func(_ context.Context, before time.Time, limit int) ([]video.Upload, error) {
							calls++
							cutoff = before
							if limit != wantLimit {
								t.Error("incorrect candidate limit")
							}
							return []video.Upload{}, nil
						},
					},
					runner: commandRunner(func(context.Context, time.Time, int) (video.CleanupReport, error) {
						t.Fatal("preview must not call RunOnce")
						return video.CleanupReport{}, nil
					}),
					close: func() { closed = true },
				}, nil
			}
			var out, errOut bytes.Buffer
			if code := run(context.Background(), args, &out, &errOut, factory); code != 0 {
				t.Fatalf("code=%d err=%s", code, &errOut)
			}
			if calls != 2 || !closed || time.Since(cutoff) < 24*time.Hour || time.Since(cutoff) > 24*time.Hour+time.Minute {
				t.Fatal("incorrect preview queries, retention cutoff or resource cleanup")
			}
			if !strings.Contains(out.String(), "预览模式") || !strings.Contains(out.String(), "ID=1") || !strings.Contains(out.String(), "过期上传候选：0 条") {
				t.Fatal("preview must describe mode and results")
			}
		})
	}
}

func TestExecuteReportsSuccessAndPartialFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "partial_failure"
		}
		t.Run(name, func(t *testing.T) {
			calls, closed := 0, false
			factory := func() (cleanupRuntime, error) {
				return cleanupRuntime{
					runner: commandRunner(func(ctx context.Context, before time.Time, limit int) (video.CleanupReport, error) {
						calls++
						if limit != 7 || time.Since(before) < 24*time.Hour {
							t.Error("incorrect execute parameters")
						}
						if _, ok := ctx.Deadline(); !ok {
							t.Error("missing timeout")
						}
						report := video.CleanupReport{Succeeded: 2, Skipped: 1}
						if fail {
							report.Failed = 1
							return report, errors.New("cleanup failed")
						}
						return report, nil
					}), close: func() { closed = true },
				}, nil
			}
			var out, errOut bytes.Buffer
			code := run(context.Background(), []string{"--execute", "--limit=7"}, &out, &errOut, factory)
			wantCode := 0
			if fail {
				wantCode = 1
			}
			if code != wantCode || calls != 1 || !closed {
				t.Fatal("incorrect execution branch or resource cleanup")
			}
			if !strings.Contains(out.String(), "成功=2 跳过=1") || !strings.Contains(out.String(), "执行模式") {
				t.Fatal("must display partial result even when cleanup returns error")
			}
			if fail && !strings.Contains(errOut.String(), "cleanup failed") {
				t.Fatal("failure must reach stderr")
			}
		})
	}
}

func TestInvalidArgumentsNeverOpenDatabase(t *testing.T) {
	for _, args := range [][]string{{"--limit=0"}, {"--limit=1001"}, {"--timeout=0s"}, {"--timeout=-1s"}, {"--unknown"}, {"extra"}, {"--execute=bad"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(context.Background(), args, &out, &errOut, func() (cleanupRuntime, error) {
				t.Fatal("invalid arguments must not open database")
				return cleanupRuntime{}, nil
			})
			if code != 2 {
				t.Fatalf("expected usage exit code 2, got %d", code)
			}
		})
	}
}

func TestHelpAndCanceledContextNeverOpenDatabase(t *testing.T) {
	factory := func() (cleanupRuntime, error) { t.Fatal("must not initialize runtime"); return cleanupRuntime{}, nil }
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &out, &errOut, factory); code != 0 {
		t.Fatal("help must succeed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := run(ctx, nil, &out, &errOut, factory); code != 1 {
		t.Fatal("canceled command must fail without database access")
	}
}

func TestPreviewQueryErrorsAndTimeout(t *testing.T) {
	for _, phase := range []string{"retry", "pending", "timeout"} {
		t.Run(phase, func(t *testing.T) {
			closed := false
			factory := func() (cleanupRuntime, error) {
				return cleanupRuntime{
					repository: commandRepository{
						deleting: func(ctx context.Context, _ int) ([]video.Upload, error) {
							if phase == "retry" {
								return nil, errors.New("retry query failed")
							}
							if phase == "timeout" {
								<-ctx.Done()
								return nil, ctx.Err()
							}
							return nil, nil
						},
						expired: func(context.Context, time.Time, int) ([]video.Upload, error) {
							if phase != "pending" {
								t.Error("should not query candidates after retry query failure")
							}
							return nil, errors.New("candidate query failed")
						},
					}, close: func() { closed = true },
				}, nil
			}
			var out, errOut bytes.Buffer
			if code := run(context.Background(), []string{"--timeout=10ms"}, &out, &errOut, factory); code != 1 || !closed || errOut.Len() == 0 {
				t.Fatal("query failure must close runtime and return failure")
			}
		})
	}
}

func TestConfigurationFailure(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), nil, &out, &errOut, func() (cleanupRuntime, error) { return cleanupRuntime{}, errors.New("configuration unavailable") }); code != 1 || !strings.Contains(errOut.String(), "configuration unavailable") {
		t.Fatal("configuration errors must fail the command")
	}
}
