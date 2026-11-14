-- 通知：worker 的消费者把通知队列里的点赞、评论事件写成通知（M4 第三步）。
-- 同一接收者、同一事件只有一条（唯一键），消息被重复投递时不会多出一条。
-- 视频删除时，它的通知跟着删除；评论删除时通知保留，所以 comment_id 不加外键。
-- created_at 是事件发生的时间；read_at 为 NULL 表示未读（第四步使用）。
-- 已有数据库执行一次；新库由 schema.sql 创建。
CREATE TABLE IF NOT EXISTS notifications (
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
    CONSTRAINT fk_notifications_recipient FOREIGN KEY (recipient_id) REFERENCES users(id),
    CONSTRAINT fk_notifications_actor FOREIGN KEY (actor_id) REFERENCES users(id),
    CONSTRAINT fk_notifications_video FOREIGN KEY (video_id) REFERENCES videos(id)
        ON DELETE CASCADE
);
