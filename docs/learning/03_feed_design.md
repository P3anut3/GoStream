# GoStream 最新视频 Feed 设计

状态：最新流、页面接入、组合索引及作者/点赞批量补全已实现并通过对应测试。用户已执行索引迁移并提供计划输出。实际浏览器整体验收待完成。原始调研日期：2026-10-20；下文是最初设计，当前补全接口与验证详见 06_feed_metadata.md。

## 目标与范围

Feed 提供连续浏览的视频信息和播放地址；视频文件仍通过现有静态资源路由取得。
本阶段实现公共最新视频流，保留现有 GET /videos 和 GET /me/videos 的行为。
参考项目的独立 feed 模块，采用 Handler → Service → Repository。
最新、关注、热榜具有不同排序与一致性需求，不强行共用一种分页算法。

## 调研依据与取舍

| 来源 | 可借鉴的做法 | 本项目的选择 |
| --- | --- | --- |
| Slack 分页实践 | cursor + limit；next_cursor；客户端不解释游标内部内容 | 对外使用字符串游标，前端只回传 |
| Google AIP-158 | 分页令牌与底层实现解耦；令牌不能作为授权依据 | 版本化游标；查询条件和权限始终由服务端决定 |
| MySQL LIMIT 文档 | 排序字段有重复值时，应添加唯一字段以确定顺序 | created_at DESC, id DESC |
| Elasticsearch 分页文档 | search_after 与唯一排序字段；严格快照需要额外机制 | 使用键集分页，但不宣称跨请求快照一致性 |

这些来源说明成熟系统的设计原则，不表示 GoStream 已达到它们的生产规模。
本方案是结合当前表结构做出的工程选择，不是行业唯一规范。

## API 约定

请求：GET /feed?limit=10；下一批 GET /feed?limit=10&cursor=<URL编码后的游标>。

- 游客可访问。第一版不返回个性化 is_liked，不处理可选登录。
- limit 不传或为 0：使用 10；超过 50：按 50 处理。
- limit 为负数、非整数或溢出：400 INVALID_ARGUMENT。
- cursor 不传或为空：第一批；不再将数字 0 作为游标约定。
- cursor 长度上限 1024 字节；格式、签名、版本、流类型或字段不合法：400 INVALID_CURSOR。
- 后续请求允许调整 limit；未来新增筛选条件时，必须与游标绑定，不能跨条件复用。

成功响应示意（videos 结构沿用现有公开视频字段）：

```json
{
  "videos": [],
  "next_cursor": "",
  "has_more": false
}
```

- videos 永远是数组，没有内容时为 []。
- has_more 为 false 时，next_cursor 必须为空字符串。
- has_more 为 true 时，next_cursor 必须非空。
- 不提供总条数；连续浏览不需要每次执行 COUNT。
- 数据库失败返回 500：{"code":"INTERNAL_ERROR","error":"视频流查询失败"}，详细错误仅记录在服务端。
- 参数错误同样使用 code、error 两个字段；旧接口错误结构暂不迁移。
- 前端刷新时清除 cursor，从第一批开始；旧的异步请求结果不得混入新一轮列表。

## 稳定排序与数据库查询

排序键为 (created_at, id)，两列在发布后保持不变；暂以现有 created_at 表示发布记录创建时间。
未来如果引入审核或预约发布，应先定义 published_at，不能直接把上传时间当发布时间。

第一批：

```sql
SELECT id, author_id, title, play_url, cover_url, created_at
FROM videos
ORDER BY created_at DESC, id DESC
LIMIT ?;
```

后续批次：

```sql
SELECT id, author_id, title, play_url, cover_url, created_at
FROM videos
WHERE created_at < ?
   OR (created_at = ? AND id < ?)
ORDER BY created_at DESC, id DESC
LIMIT ?;
```

占位符使用参数绑定，不拼接客户端输入。LIMIT 使用有效 limit + 1。
同一时间的视频按 ID 倒序，所以分页不会因为时间相同而跳过剩余记录。
新视频位于已读位置之前时，不会把后续页面的位置推移；刷新后可见这些新视频。

计划新增索引 idx_videos_created_at_id(created_at, id)。现有 schema.sql 尚无此索引。
实现时提供迁移文件，通过真实 MySQL 的 EXPLAIN 检查执行计划；不预先宣称性能提升。
现有秒级 TIMESTAMP 可以使用组合游标，无需为了分页立刻修改字段精度。
游标时间以数据库实际读回值编码，统一 UTC；若未来增加精度，编码不得截断。

## 多查一条与游标生成

1. Service 校验业务输入，要求 Repository 查询有效 limit + 1 条。
2. 超过 limit 时，has_more=true，只保留前 limit 条。
3. 用实际返回的最后一条记录生成 next_cursor，不能使用被截掉的额外记录。
4. 没超过 limit 时，has_more=false、next_cursor=""。

has_more 仅表示本次查询时还能看到后续记录。下次请求前记录可能被删除，因此不是永久保证。
无需为了多查一条开启事务，也不跨 HTTP 请求持有数据库事务或行锁。
并发事务晚提交、历史数据补录、删除以及排序字段变化都意味着列表不是固定快照。
本版本接受普通时间线的实时一致性边界，不承诺遍历期间所有记录恰好出现一次。

## 游标格式与安全边界

客户端将 cursor 当作不可自行构造、解析的续读标记；这不等于加密或隐藏数据。
建议采用版本化载荷的 Base64URL 编码，加 HMAC-SHA256 签名。
载荷字段为版本、流类型 latest、最后一条记录的 UTC 时间和 ID。
HMAC 只能检测修改，不能保密；Base64URL 也不是加密。
这不是对 Google 不透明令牌要求的完整复刻，而是本项目可演进的客户端约定。
游标中不存密码、JWT、权限和数据库连接信息。

使用独立配置 GOSTREAM_FEED_CURSOR_SECRET，不能直接复用 JWTSecret；所有实例保持一致。
密钥不进入 Git。第一版不设置游标过期时间；密钥轮换或不支持的版本会使旧游标失效，客户端重新加载第一批。
检查解码长度、签名、版本和合法字段后才查询数据库。
第一版是公共流，不绑定用户；未来关注流需要绑定用户或查询上下文，并在每次请求重新鉴权。（M2 已实现：关注流游标记录 following 类型，每次请求由 JWT 确定用户。）
签名不是通用分页的必需条件，这里选择它用于明确拒绝被修改的游标。

## 模块分工

```text
internal/feed/
  entity.go       FeedItem、响应结构和内部位置类型
  handler.go      HTTP 参数、状态码和 JSON
  service.go      条数规则、多查一条、结果裁剪和下一游标
  repository.go   查询接口及 GORM 实现
  cursor.go       游标编码、校验和版本处理
```

Repository 返回数据，不依赖 Gin，不解释对外字符串游标。
Service 依赖 Repository 接口；启动入口装配具体 GORM 实现。
请求的 context 沿 Handler → Service → Repository 传递。
新增接口第一阶段直接返回公开的视频字段。作者信息、点赞状态下一阶段批量补全，避免每条视频单独查询。

## 技术栈演进顺序

1. MySQL 正确性：组合游标、索引、完整测试和前端加载下一批。
2. Redis：用于最新时间线索引与公共视频实体缓存；缓存异常时明确回源策略。
3. RabbitMQ：发布事件异步更新缓存，配套事务 Outbox、消费者幂等和重试；落库与缓存之间是最终一致。
4. 热榜：可变分数会改变排名，单独评估有期限的榜单快照与分页方式。

Redis 实现也必须保留相同的排序和续读语义，不能用时间分数减 1 来跳过同一时刻的全部视频。
个性化点赞状态不能写入所有用户共享的公共缓存。
缓存键、容量、TTL、超时、回源限流和快照过期行为在对应阶段单独设计与实测。
对齐技术栈通过这些实际问题逐步完成；本轮没有部署 Redis、MQ 或创建新服务。

## 实现时必须覆盖的测试

- 空列表、少于 limit、刚好 limit、多于 limit；最多返回 limit 条。
- 多条 created_at 相同，跨页无重复且不遗漏现有稳定记录。
- 游标取返回末项，额外查询的一条在下一批可见。
- 刷新获得新视频；正常新插入头部记录不推移后续批次。
- 上一批末项被删除后，仍可用游标中的值继续查询，不要求该行仍存在。
- 非法/过长/被修改/未知版本/错误流类型游标在查库前被拒绝。
- limit 的缺省、零、上限、超限、负数、非数字和溢出。
- 数据库错误与 context 取消传播；API 不泄漏内部错误和私密字段。
- MySQL 集成测试验证真实排序、时间精度和索引计划；前端验证结束状态和刷新。

## 来源

- [Slack 官方分页文档](https://docs.slack.dev/apis/web-api/pagination/)
- [Slack 工程实践：分页演进](https://slack.engineering/evolving-api-pagination-at-slack/)
- [Google AIP-158](https://google.aip.dev/158)
- [MySQL 8.4 LIMIT 与确定性排序](https://dev.mysql.com/doc/refman/8.4/en/limit-optimization.html)
- [Elasticsearch search_after 与快照分页](https://www.elastic.co/docs/reference/elasticsearch/rest-apis/paginate-search-results)
- [参考项目 Feed 模块](https://github.com/LeoninCS/feedsystem_video_go/tree/main/backend/internal/feed)
