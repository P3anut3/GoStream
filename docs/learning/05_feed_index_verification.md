# M0-2：Feed 索引验证记录

验证对象：用户编写的 scripts/migrations/005_feed_index.sql 和 scripts/schema.sql。
验证方法：读取本地连接配置，创建随机命名的独立测试数据库；新库 schema 建表、检查索引字段顺序；仅在该库模拟旧表，再执行真实 005 迁移。测试结束删除该测试库。
未对配置中的业务数据库执行 ALTER、插入或删除。

测试文件：internal/feed/index_integration_test.go。

命令：设置 GOSTREAM_INTEGRATION_TEST=1 后运行 go test ./internal/feed -run '^TestFeedIndexMigrationIntegration$' -count=1 -v。
测试数据为 2000 条视频，每秒多个视频。未执行耗时基准测试。

| 场景 | type | key | rows（估计） | Extra |
| --- | --- | --- | --- | --- |
| 索引前，第一批 | ALL | NULL | 2000 | Using filesort |
| 索引前，带游标 | ALL | NULL | 2000 | Using where; Using filesort |
| 索引后，第一批 | index | idx_videos_created_at_id | 11 | Backward index scan |
| 索引后，带游标 | range | idx_videos_created_at_id | 1995 | Using index condition; Backward index scan |

结论：在该测试数据和环境中，优化器选择了组合索引，未显示额外 filesort。迁移前后两种分页查询的结果完全一致，新库脚本也建立了 (created_at,id) 索引。
边界：rows 是估计值，不是实际读取行数；带游标场景的 1995 不能解释为返回了 1995 条。LIMIT 11 限制返回条数。Backward index scan 表示反向扫描，符合两个排序字段均为 DESC 的查询。
不从此结果推导业务库一定选择同一计划，也不宣称 QPS 或耗时改善百分比。

下一步由学习者操作自己的学习数据库：

1. USE gostream; SELECT DATABASE(); SHOW INDEX FROM videos;
2. 确认没有同名索引后，SOURCE D:/code/Golang/GoStream/scripts/migrations/005_feed_index.sql;
3. 再 SHOW INDEX，并对 Feed 实际字段与排序执行 EXPLAIN。
4. 同名索引存在时不要重复迁移；比较字段顺序，必要时先澄清当前状态。

生产环境的大表建索引还需评估 DDL 资源、锁和发布窗口；本记录仅验证隔离的学习环境。
