package video

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type UploadCleanupRepository interface {
	ClaimExpired(ctx context.Context, uploadID uint64, before time.Time) (Upload, bool, error)
	DeleteClaimed(ctx context.Context, uploadID uint64) error
	GetDeleting(ctx context.Context, uploadID uint64) (Upload, bool, error)
	ListDeleting(ctx context.Context, limit int) ([]Upload, error)
	ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]Upload, error)
}

type CleanupReport struct {
	Succeeded int
	Skipped   int
	Failed    int
}

type UploadCleanupService struct {
	repository UploadCleanupRepository
	videoDir   string
	coverDir   string
}

func NewUploadCleanupService(repository UploadCleanupRepository, videoDir, coverDir string) *UploadCleanupService {
	return &UploadCleanupService{
		repository: repository,
		videoDir:   videoDir,
		coverDir:   coverDir,
	}
}

// CleanupOne 尝试清理一条过期的 pending 上传记录。
// 返回 true 表示文件和上传记录都已清理完成。
func (s *UploadCleanupService) CleanupOne(ctx context.Context, uploadID uint64, before time.Time) (bool, error) {
	upload, claimed, err := s.repository.ClaimExpired(
		ctx, uploadID, before,
	)
	if err != nil {
		return false, fmt.Errorf("取得清理权失败: %w", err)
	}
	if !claimed {
		return false, nil
	}

	if err := s.finishCleanup(ctx, upload); err != nil {
		return false, err
	}

	return true, nil
}

func (s *UploadCleanupService) finishCleanup(ctx context.Context, upload Upload) error {
	if err := removeUploadedFile(
		upload, s.videoDir, s.coverDir,
	); err != nil {
		return fmt.Errorf("清理上传文件失败: %w", err)
	}

	if err := s.repository.DeleteClaimed(ctx, upload.ID); err != nil {
		return fmt.Errorf("清理上传记录失败: %w", err)
	}

	return nil
}
func (s *UploadCleanupService) RetryOne(ctx context.Context, uploadID uint64) (bool, error) {
	upload, found, err := s.repository.GetDeleting(ctx, uploadID)
	if err != nil {
		return false, fmt.Errorf("读取清理重试记录失败: %w", err)
	}
	if !found {
		return false, nil
	}

	if err := s.finishCleanup(ctx, upload); err != nil {
		return false, err
	}

	return true, nil
}

// RunOnce retries unfinished work before claiming new expired uploads.
// limit applies separately to each category, so a run handles at most 2*limit records.
// Per-record errors are collected; cancellation and list errors stop the run.
func (s *UploadCleanupService) RunOnce(ctx context.Context, before time.Time, limit int) (CleanupReport, error) {
	var report CleanupReport
	var failures []error
	if limit < 1 || limit > 1000 {
		return report, fmt.Errorf("处理数量必须为 1～1000")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}

	process := func(uploads []Upload, action string, cleanup func(uint64) (bool, error)) error {
		for _, upload := range uploads {
			if err := ctx.Err(); err != nil {
				return err
			}
			done, err := cleanup(upload.ID)
			switch {
			case err != nil:
				report.Failed++
				failures = append(failures, fmt.Errorf("%s上传记录 %d 失败: %w", action, upload.ID, err))
			case done:
				report.Succeeded++
			default:
				report.Skipped++
			}
		}
		return ctx.Err()
	}

	retries, err := s.repository.ListDeleting(ctx, limit)
	if err != nil {
		return report, fmt.Errorf("查询清理重试任务失败: %w", err)
	}
	if err := process(retries, "重试", func(id uint64) (bool, error) {
		return s.RetryOne(ctx, id)
	}); err != nil {
		return report, errors.Join(append(failures, err)...)
	}

	candidates, err := s.repository.ListExpiredPending(ctx, before, limit)
	if err != nil {
		failures = append(failures, fmt.Errorf("查询过期候选失败: %w", err))
		return report, errors.Join(failures...)
	}
	if err := process(candidates, "清理", func(id uint64) (bool, error) {
		return s.CleanupOne(ctx, id, before)
	}); err != nil {
		return report, errors.Join(append(failures, err)...)
	}
	return report, errors.Join(failures...)
}
