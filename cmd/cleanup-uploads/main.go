package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"gostream/internal/database"
	"gostream/internal/video"

	"github.com/joho/godotenv"
)

type previewRepository interface {
	ListDeleting(context.Context, int) ([]video.Upload, error)
	ListExpiredPending(context.Context, time.Time, int) ([]video.Upload, error)
}

type cleanupRunner interface {
	RunOnce(context.Context, time.Time, int) (video.CleanupReport, error)
}

type cleanupRuntime struct {
	repository previewRepository
	runner     cleanupRunner
	close      func()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, openRuntime)
	stop()
	os.Exit(code)
}

// run parses arguments before opening MySQL. The factory allows command tests
// to verify preview/execute selection without touching application data.
func run(parent context.Context, args []string, out, errOut io.Writer, open func() (cleanupRuntime, error)) int {
	flags := flag.NewFlagSet("cleanup-uploads", flag.ContinueOnError)
	flags.SetOutput(errOut)
	execute := flags.Bool("execute", false, "实际执行一轮清理；默认仅预览")
	limit := flags.Int("limit", 100, "每类记录上限（1～1000），一轮最多处理两倍此数量")
	timeout := flags.Duration("timeout", 30*time.Second, "本轮查询和清理的超时时间，例如 30s、2m")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *limit < 1 || *limit > 1000 || *timeout <= 0 {
		fmt.Fprintln(errOut, "参数错误：不接受位置参数，limit 必须为 1～1000，timeout 必须大于 0")
		return 2
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	runtime, err := open()
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	defer runtime.close()
	before := time.Now().Add(-24 * time.Hour)
	fmt.Fprintf(out, "过期截止时间：%s；每类上限：%d\n", before.Format(time.RFC3339), *limit)

	if *execute {
		fmt.Fprintln(out, "执行模式：先重试 deleting，再清理过期 pending。")
		report, err := runtime.runner.RunOnce(ctx, before, *limit)
		fmt.Fprintf(out, "清理结果：成功=%d 跳过=%d 失败=%d\n", report.Succeeded, report.Skipped, report.Failed)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	}

	fmt.Fprintln(out, "预览模式：不修改状态、不删除文件。实际执行需显式传入 --execute。")
	retries, err := runtime.repository.ListDeleting(ctx, *limit)
	if err != nil {
		fmt.Fprintf(errOut, "查询待重试记录失败: %v\n", err)
		return 1
	}
	candidates, err := runtime.repository.ListExpiredPending(ctx, before, *limit)
	if err != nil {
		fmt.Fprintf(errOut, "查询过期候选失败: %v\n", err)
		return 1
	}
	printUploads(out, "待重试记录", retries)
	printUploads(out, "过期上传候选", candidates)
	return 0
}

func printUploads(out io.Writer, title string, uploads []video.Upload) {
	fmt.Fprintf(out, "\n%s：%d 条\n", title, len(uploads))
	for _, upload := range uploads {
		fmt.Fprintf(out, "ID=%d 类型=%s 状态=%s 文件=%s 上传时间=%s\n", upload.ID,
			upload.FileType, upload.Status, upload.SavedName, upload.CreatedAt.Format(time.RFC3339))
	}
}

func openRuntime() (cleanupRuntime, error) {
	if err := godotenv.Load(); err != nil {
		return cleanupRuntime{}, errors.New("加载 .env 失败，请从项目根目录运行")
	}
	dsn := os.Getenv("GOSTREAM_DSN")
	if dsn == "" {
		return cleanupRuntime{}, errors.New("缺少 GOSTREAM_DSN 配置")
	}
	videoDir, err := filepath.Abs("uploads/videos")
	if err != nil {
		return cleanupRuntime{}, errors.New("无法解析视频上传目录")
	}
	coverDir, err := filepath.Abs("uploads/covers")
	if err != nil {
		return cleanupRuntime{}, errors.New("无法解析封面上传目录")
	}
	db, err := database.OpenDB(dsn)
	if err != nil {
		return cleanupRuntime{}, errors.New("连接数据库失败，请检查本地配置和 MySQL 服务")
	}
	repository := &video.SQLUploadRepository{DB: db}
	return cleanupRuntime{
		repository: repository,
		runner:     video.NewUploadCleanupService(repository, videoDir, coverDir),
		close:      func() { db.Close() },
	}, nil
}
