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
    KEY idx_videos_created_at_id (created_at, id),
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

CREATE TABLE IF NOT EXISTS follows ( -- 关注表
    follower_id BIGINT UNSIGNED NOT NULL,
    followee_id BIGINT UNSIGNED NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (follower_id, followee_id),
    KEY idx_follows_followee_id (followee_id),
    CONSTRAINT fk_follows_follower FOREIGN KEY (follower_id) REFERENCES users(id),
    CONSTRAINT fk_follows_followee FOREIGN KEY (followee_id) REFERENCES users(id),
    CONSTRAINT chk_follows_not_self CHECK (follower_id <> followee_id)
);

CREATE TABLE IF NOT EXISTS outbox_events ( -- 发件箱：事件和业务数据同一个事务写入，published_at 为 NULL 表示待发送
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    event_type VARCHAR(64) NOT NULL,
    payload JSON NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at TIMESTAMP NULL DEFAULT NULL,
    PRIMARY KEY (id),
    KEY idx_outbox_events_pending (published_at, id)
);

CREATE TABLE IF NOT EXISTS notifications ( -- 通知：消费者按事件写入，同一接收者同一事件只有一条；视频删除时一起删除
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    recipient_id BIGINT UNSIGNED NOT NULL,
    actor_id BIGINT UNSIGNED NOT NULL,
    event_id BIGINT UNSIGNED NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    video_id BIGINT UNSIGNED NOT NULL,
    comment_id BIGINT UNSIGNED NULL DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    read_at TIMESTAMP NULL DEFAULT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_notifications_recipient_event (recipient_id, event_id),
    KEY idx_notifications_recipient_id (recipient_id, id),
    KEY idx_notifications_actor_id (actor_id),
    KEY idx_notifications_video_id (video_id),
    KEY idx_notifications_unread (recipient_id, read_at),
    CONSTRAINT fk_notifications_recipient FOREIGN KEY (recipient_id) REFERENCES users(id),
    CONSTRAINT fk_notifications_actor FOREIGN KEY (actor_id) REFERENCES users(id),
    CONSTRAINT fk_notifications_video FOREIGN KEY (video_id) REFERENCES videos(id)
        ON DELETE CASCADE
);
