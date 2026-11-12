-- 发件箱：点赞、评论产生的事件和业务数据在同一个事务里写入，投递员（cmd/worker）随后发到 RabbitMQ。
-- published_at 为 NULL 表示还没发送；id 就是事件 ID，消费者用它去重。
-- 已有数据库执行一次；新库由 schema.sql 创建。
CREATE TABLE IF NOT EXISTS outbox_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    event_type VARCHAR(64) NOT NULL,
    payload JSON NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at TIMESTAMP NULL DEFAULT NULL,
    PRIMARY KEY (id),
    KEY idx_outbox_events_pending (published_at, id)
);
