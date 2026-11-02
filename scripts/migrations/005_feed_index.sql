-- 在目标数据库执行一次；执行前确认同名索引尚不存在。
ALTER TABLE videos
    ADD INDEX idx_videos_created_at_id (created_at, id);