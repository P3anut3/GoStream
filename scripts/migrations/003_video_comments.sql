-- Existing databases: keep the manually created table if it already exists.
CREATE TABLE IF NOT EXISTS video_comments (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    video_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    content VARCHAR(500) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_video_comments_video_id_id (video_id, id),
    CONSTRAINT fk_video_comments_video FOREIGN KEY (video_id) REFERENCES videos(id)
        ON DELETE CASCADE,
    CONSTRAINT fk_video_comments_user FOREIGN KEY (user_id) REFERENCES users(id)
);
