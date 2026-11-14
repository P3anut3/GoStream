-- 未读数：数某个人 read_at IS NULL 的通知时，只读这个索引就够，不用回表（覆盖索引）；
-- “全部已读”的 UPDATE 也靠它找到要改的行（M4 第四步）。
-- 已有数据库执行一次；新库由 schema.sql 创建。
ALTER TABLE notifications ADD KEY idx_notifications_unread (recipient_id, read_at);
