-- New databases: latest schema. Existing databases use scripts/migrations/.
CREATE DATABASE IF NOT EXISTS gostream CHARACTER SET utf8mb4;
USE gostream;

CREATE TABLE IF NOT EXISTS users (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    username VARCHAR(32) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uk_users_username (username)
);

CREATE TABLE IF NOT EXISTS videos (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    author_id BIGINT UNSIGNED NOT NULL,
    title VARCHAR(100) NOT NULL,
    play_url VARCHAR(1024) NOT NULL,
    cover_url VARCHAR(1024) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_videos_author_id (author_id),
    CONSTRAINT fk_videos_author FOREIGN KEY (author_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS uploads (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BIGINT UNSIGNED NOT NULL,
    saved_name VARCHAR(64) NOT NULL,
    file_type VARCHAR(16) NOT NULL DEFAULT 'video',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    PRIMARY KEY (id),
    UNIQUE KEY uk_uploads_saved_name (saved_name),
    KEY idx_uploads_user_id (user_id),
    KEY idx_uploads_status_created_at (status, created_at),
    CONSTRAINT fk_uploads_user FOREIGN KEY (user_id) REFERENCES users(id)
);

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
