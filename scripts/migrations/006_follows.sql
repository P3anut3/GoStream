-- 关注关系：follower_id 关注了 followee_id（有向）。已有数据库执行一次；新库由 schema.sql 创建。
CREATE TABLE IF NOT EXISTS follows (
    follower_id BIGINT UNSIGNED NOT NULL,
    followee_id BIGINT UNSIGNED NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (follower_id, followee_id),
    KEY idx_follows_followee_id (followee_id),
    CONSTRAINT fk_follows_follower FOREIGN KEY (follower_id) REFERENCES users(id),
    CONSTRAINT fk_follows_followee FOREIGN KEY (followee_id) REFERENCES users(id),
    CONSTRAINT chk_follows_not_self CHECK (follower_id <> followee_id)
    );