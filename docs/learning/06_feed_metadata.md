# M0-3：Feed 的作者与点赞信息

本次按用户明确要求代实现。后续后端继续采用指导学习者实现、导师负责测试的分工。

## 接口变化

GET /feed 保留原字段、游标、排序和分页规则，向每条视频新增：

```json
{
  "author_name": "发布者",
  "like_count": 2,
  "liked": false
}
```

author_id 继续保留，不改旧视频详情和我的视频接口。字段均明确输出，不用 omitempty 隐藏零值。
游客不带 Authorization：可以访问，liked 为 false；可以查看总点赞数。
登录用户携带 Bearer token：校验成功后根据 JWT 身份查询 liked。
提供空的、错误或过期凭证：401，不静默当成游客；前端清除失效登录后重新加载游客 Feed。
URL 的 userID 参数不决定个人状态。响应标记 private, no-store，避免 HTTP 共享缓存混淆不同用户。

## 实际调用链

```text
GET /feed
 → auth.OptionalJWTAuth（缺少凭证允许继续；提供凭证则调用 JWTAuth 校验）
 → feed.Handler.ListLatest（取得经验证的 userID）
 → Service.ListLatestForViewer
    1. 解码游标，查询 limit+1 条
    2. 裁剪到实际返回的 limit 条
    3. 收集视频 ID，去重作者 ID
    4. Repository.LoadMetadata：批量查作者、批量聚合点赞
    5. 按原顺序补充字段，生成下一游标
 → JSON
```

Service.ListLatest 保留为游客调用入口，委托给 ListLatestForViewer(...,0)。
Repository 接口新增 LoadMetadata；Metadata 以 ID 为键存放作者名、点赞摘要，查询返回顺序不影响视频顺序。

## 查询次数

非空、有效批次固定 3 次读取：

1. videos：按时间+ID 排序查询。
2. users：id IN (...)，只取 id 和 username，不查询密码哈希。
3. video_likes：限定本批 video_id 后 GROUP BY，COUNT(*) 统计总数；MAX(CASE WHEN user_id = 当前用户 THEN 1 ELSE 0 END) 计算是否点赞。

多查出来但不返回的那一条不会参与元信息补全；同一作者只加入一次作者 ID 集合。
没有点赞记录的视频通过 map 零值得到 like_count=0、liked=false。
空批次只有第一次视频查询，不执行其他读取。
数据量增加仍可能增加聚合查询成本；3 次查询不等于常量耗时，更不是性能基准结论。
三次读取没有跨请求快照保证，并发删除或修改可能造成短时变化。作者名缺失时前端退回创作者 ID 展示，不暴露用户私密数据。

## 前端行为

- 公共列表登录时自动携带 token；游客请求不发送该请求头。
- 卡片显示 author_name、总点赞数和当前用户的已点赞标记。
- 登录、退出和切换账号都重新获取列表，清除旧用户状态；旧响应按请求序号与 token 隔离。
- 详情点赞状态重新查询成功后，同步更新当前卡片显示。
- 作者名用 textContent 输出，不当作 HTML 解析。

## 代码阅读顺序

1. internal/feed/entity.go：FeedItem、LikeSummary、Metadata。
2. internal/feed/service.go：ListLatestForViewer，重点看先裁剪再批量补全。
3. internal/feed/repository.go：LoadMetadata，两次批量数据库读取。
4. internal/auth/middleware.go：OptionalJWTAuth 如何复用 JWTAuth。
5. internal/feed/handler.go、cmd/server/main.go：身份如何到达 Service。
6. web/app.js：optionalAuth、卡片展示和会话切换。

## 验证结果

- go test ./...、go vet ./... 通过。
- Feed/Server 的 MySQL 集成测试通过：临时表隔离，游客、A、B 总数一致而个人状态不同；URL userID 不能伪造身份。
- 集成测试实际统计 GORM 读取次数：两条与多条视频均为 3 次，空批次为 1 次。
- 单元测试覆盖作者去重、保持顺序、零点赞、补全失败、空列表跳过，以及非法 token 不执行后续处理。
- 现有发布、点赞、评论与 Feed DOM/API 模拟测试通过；新增作者文本、已点赞展示、登录失效和会话切换检查。
- 本轮没有修改业务数据库表结构和业务记录，没有在真实浏览器重新验收；HTTP/MySQL 使用测试服务器与隔离测试数据。

下一步：M0-4，通过两个真实账号验收发布、浏览、播放、点赞/评论与权限；此前 M0-1 的浏览器验收也在这一轮补齐。
