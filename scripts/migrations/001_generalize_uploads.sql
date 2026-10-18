-- Existing databases only. Run once, with the server stopped.
-- MySQL DDL commits implicitly; this ALTER preserves existing upload rows.
-- The default marks all existing records as videos.
ALTER TABLE video_uploads
    ADD COLUMN file_type VARCHAR(16) NOT NULL DEFAULT 'video' AFTER saved_name,
    RENAME TO uploads;
