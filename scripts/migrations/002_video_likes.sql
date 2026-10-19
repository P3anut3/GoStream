-- Existing databases: the user-created table is left in place if it exists.
CREATE TABLE IF NOT EXISTS video_likes (
    user_id BIGINT UNSIGNED NOT NULL,
    video_id BIGINT UNSIGNED NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, video_id),
    KEY idx_video_likes_video_id (video_id),
    CONSTRAINT fk_video_likes_user FOREIGN KEY (user_id) REFERENCES users(id),
    CONSTRAINT fk_video_likes_video FOREIGN KEY (video_id) REFERENCES videos(id)
        ON DELETE CASCADE
);
