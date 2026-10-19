-- 执行前暂停应用；本迁移只执行一次。
ALTER TABLE uploads
    ADD COLUMN status VARCHAR(16) NOT NULL DEFAULT 'pending',
    ADD INDEX idx_uploads_status_created_at (status, created_at);

-- 将已有视频引用的上传文件标记为 published，
-- 避免老数据被误当成待清理文件。
UPDATE uploads AS u
SET u.status = 'published'
WHERE
    (
        u.file_type = 'video'
            AND EXISTS (
            SELECT 1
            FROM videos AS v
            WHERE v.play_url =
                  CONCAT('/media/videos/', u.saved_name)
        )
        )
   OR
    (
        u.file_type = 'cover'
            AND EXISTS (
            SELECT 1
            FROM videos AS v
            WHERE v.cover_url =
                  CONCAT('/media/covers/', u.saved_name)
        )
        );