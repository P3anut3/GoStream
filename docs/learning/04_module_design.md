# GoStream 模块设计与数据流

这是与 00_project_roadmap.md 配套的设计说明。已有接口来自当前源码；标明“拟定”的接口/表尚未实现。
不把逻辑模块数量等同于进程或微服务数量，不在本轮搬迁现有文件。

## 1. 通用分层与数据归属

```text
HTTP Handler：绑定参数，取得认证身份，映射状态码和 JSON
    ↓
Service：业务校验、用例编排、事务边界和调用依赖
    ↓
Repository：参数绑定、查询/写入、映射存储错误
    ↓
MySQL
```

Handler 不决定客户端传入的 author_id 是否可信，当前用户身份必须来自 JWT 中间件。
路径 id 表示操作目标，不表示操作者身份。
跨多次写入的事务由用例统一组织，事务内 Repository 必须共享同一个事务连接。
新模块按此划分；视频 Handler 的历史 SQL 在相关用例演进时提取，不为统一外观集中重写。
当前部分 Repository 直接返回展示字段是已有实践；模块变复杂后再引入专门内部类型，不为了分层重复所有结构体。

## 2. 用户与认证

业务目标：用户能安全注册、登录，后续写操作识别真实操作者。

- 现有接口：POST /users、POST /login、GET /me。
- 持久数据：users；username 唯一，password_hash 保存 bcrypt 结果。
- 依赖：MySQL、JWT 签名工具；不要求注册依赖 Redis/MQ。
- 流程：绑定凭据 → Service 校验 → Repository 查询/保存 → 校验密码 → Handler 签发 JWT。
- 关键失败：重复用户名、错误凭据、token 过期、用户已不存在、数据库故障。
- 当前限制：页面退出清除本页 token；不等于服务端撤销已签发 token。刷新 token/强制下线不是当前已有功能。
- 后续：输入边界、登录限流，若实现撤销则明确 Redis 会话与 JWT 有效期策略。
- 验收：错误密码不成功，响应不带哈希，非法身份不能写操作；登录限流故障策略可解释。

## 3. 上传与媒体访问

业务目标：用户上传文件，系统提供自己的播放/封面地址。

- 现有接口：POST /uploads/videos、POST /uploads/covers；GET/HEAD /media/videos/:filename、/media/covers/:filename。
- 数据：uploads；磁盘 uploads/videos、uploads/covers。
- 核心流程：JWT 身份 → 大小/格式检查 → 随机文件名 → 保存文件 → 写 uploads pending → 返回地址。
- 数据库写失败时尝试删除刚写入的文件；文件系统与数据库没有共享事务，删除失败必须记录并支持后续恢复。
- MP4 文件头检查是初步类型识别，不宣称完整转码或内容有效性审核。
- 媒体读取当前公开，不因为上传接口鉴权就认为播放地址私有。未来私有视频必须同时设计文件访问授权。
- Range 请求用于分段读取；与分片上传是两种不同能力。
- 清理命令只处理符合条件的过期 pending/deleting 上传；当前不会自动回收所有已删作品的文件。
- 后续分片接口拟定：POST /uploads/sessions、PUT /uploads/sessions/:id/parts/:part、GET /uploads/sessions/:id、POST /uploads/sessions/:id/complete。
- 分片状态绑定用户、总大小、分片范围和过期时间；合并成功接回已有普通发布链路。

## 4. 视频作品与发布

业务目标：把上传资源变成可浏览的作品，并管理本人作品。

- 现有接口：POST /videos、GET /videos/:id、GET /videos、GET /me/videos、DELETE /videos/:id。
- 数据：videos；引用 uploads 中属于作者的视频/封面资源。
- 发布流程：校验标题/地址 → 开启事务 → 锁上传记录并检查归属/状态 → 创建作品 → 更新 uploads 为 published → 提交。
- 清理与发布通过相同上传记录的行锁协调；不能在锁释放后凭之前读到的 pending 状态直接删除文件。
- 当前作品写入 videos 就表示发布，没有独立审核队列；uploads.status 不作为作品可见性字段。
- 删除作品按 id + author_id 条件执行；点赞、评论依赖外键级联删除，物理媒体仍保留。
- 详情缓存（M3 已实现）：GET /videos/:id 经 VideoService 读取，依次为进程内 go-cache（5 秒）、Redis（key gostream:video:v1:{id}，只缓存公开字段，TTL 10 分钟加 0～60 秒随机值）、经 singleflight 合并的 MySQL 回源；两层缓存都实现 VideoCache 接口，本地缓存包在 Redis 外面。MySQL 确认不存在时写 30 秒“不存在”标记，发布事务提交后清除；删除先删库再删缓存，删缓存失败时旧数据最多保留到过期（≤11 分钟），其他实例的本地缓存最多再陈旧 5 秒；Redis 读写失败只记日志并降级到 MySQL。
- 最新流时间线（M3 已实现）：feed.Service 先问 RedisTimeline（有序集合 gostream:feed:latest:v1:ids 存补零 ID 和毫秒发布时间，哈希 gostream:feed:latest:v1:videos 存公开字段，底部哨兵 end/more），回答不了再查 MySQL；重建读 Window()+1 行并用 singleflight 合并。video 包用自己定义的 LatestFeed 接口在发布提交后、删除成功后让时间线失效，不 import feed 包，由 main.go 把 feed.Service 传给视频 Handler。
- 后续发布事务扩展：同一事务写出 video.published 事件的 outbox 行，提交后由 Worker 转发。
- 验收：A 不能发布 B 的资源，不能删 B 的作品；事务失败无半条作品；清理竞争不破坏已发布作品。

## 5. Feed 与内容组装

业务目标：浏览者得到有序的一批作品与继续加载的位置。

- 现有接口：GET /feed?limit=10&cursor=...；独立 internal/feed。
- 当前实现：按 created_at DESC,id DESC，组合游标、limit+1；只读 videos 的公开字段。
- 返回：videos、next_cursor、has_more；前端保存 next_cursor，不自行解析或修改。
- 当前接口不是推荐模型，也不记录实际观看进度。
- M0：接通页面；计划索引 idx_videos_created_at_id(created_at,id)；批量查询作者、点赞数和用户点赞状态。
- 个人点赞状态读取需要明确的可选认证：无 token 按游客处理；提供了无效 token 时返回 401，不能悄悄认为已登录。
- 批量组装保持原始排序，不能因数据库 IN 查询顺序不确定而打乱列表。
- 公共缓存保存公开视频实体/公共计数，个性化状态按当前请求补充。
- 关注流已实现：GET /feed/following（需登录）；推荐流将来可独立 GET /feed/recommended，不共用 latest 的时间过滤。
- 推荐接口边界：输入用户/会话与续读标记，返回有序候选 ID 和下一标记；Feed 批量补全及检查可展示性。
- 推荐会话快照、去重和行为事件均是后续方案，当前不创建空的推荐微服务。
- 验收：同时间边界、删除边界、新插入、空批次、失效游标、刷新与追加、请求竞态都可验证。

## 6. 点赞与评论

业务目标：用户对公开作品互动，作者/本人权限边界清楚。

- 现有点赞接口：PUT /videos/:id/like、DELETE /videos/:id/like、GET /videos/:id/like。
- 数据：video_likes；(user_id,video_id) 唯一；重复点赞/取消幂等。
- 现有评论接口：POST/GET /videos/:id/comments、DELETE /comments/:id。
- 数据：video_comments；同一用户可发多条评论，不对 (user_id,video_id) 加唯一约束。
- 评论校验去掉首尾空白、1～500 Unicode 字符；只允许删除本人评论。
- 当前同步 MySQL 写入是成功依据；重复请求、唯一键错误与真实数据库错误要区分。
- 后续事件：只对真正新增的点赞/评论产生通知事件，重复点赞不应再次创建通知。
- 评论重复发送是否防重需要单独产品约定，不能把重复内容一律当成重复请求。
- 验收：两个用户点赞数一致、个人状态不同；重复点赞不增加记录；删除他人评论失败；不存在视频不能新增互动。

## 7. 关注关系与关注流（已实现）

业务目标：A 关注 B，关注流只展示 B 等已关注作者的作品。

- 已实现模块 internal/follow：Handler → Service → Repository。目录名采用 follow，不再使用早期规划的 social/。
- 表 follows(follower_id,followee_id,created_at)：联合主键防重复；idx_follows_followee_id 用于粉丝方向查询；两个外键指向 users(id)，不级联删除（当前没有删除用户的功能）；CHECK (follower_id <> followee_id) 兜底禁止自关注。迁移 006 与 schema.sql 建出的结构一致。
- 操作者 follower_id 只来自 JWT，目标 followee_id 来自路径；请求体中的身份字段被忽略。
- 接口约定：PUT /users/:id/follow 首次与重复均为 204，自关注 400，目标不存在 404；DELETE /users/:id/follow 一律 204（关系不存在即已达成目标）；GET /users/:id/follow 返回 {"following": bool}，目标不存在 404，禁止共享缓存。
- Repository 用 INSERT … SELECT 在一条语句中检查目标存在并插入，用主键冲突 1062 识别重复；Service 拒绝自关注，并把重复关注视为成功。Repository 仍区分"新建"和"已存在"，M4 只对首次关注产生通知事件。
- 关注流：GET /feed/following 需登录，JOIN follows 只取当前用户关注的作者，沿用 created_at、id 稳定排序、limit+1 与批量元数据。Service 的 list 由两种流共用，fetch 决定数据来源。游标在签名内记录 latest/following，不能混用，也不携带身份。续读没有快照，翻页期间关系变化会影响后续页。
- 不把关注关系和缓存同时当作两份事实；MySQL 是事实，Redis 是后续加速层。
- 页面：详情页作者信息下方的关注按钮；游客先登录，作者本人不显示，状态以后端 GET 结果为准。
- 验收：关系 API 已由单元、HTTP、真实 MySQL 集成和端到端测试覆盖（有向、幂等、自关注、目标不存在、未登录、非法 ID、跨账号隔离）；关注按钮、关注流与关注标签页已通过 Chromium 夹具与隔离 Compose 真实浏览器验收；MySQL 集成和端到端测试验证多账号筛选、同一时间边界不越界、取关后不可见和游标不能混用。

## 8. 事件、Worker 与通知（拟定）

业务目标：互动成功后可靠产生作者提醒；服务故障后可以补处理。

- 事件字段：event_id、event_type、version、actor_id、target_id、occurred_at，以及必要业务载荷；不携带密码或 token。
- 拟定表 outbox_events：事件 ID 唯一、载荷、状态/重试、下次尝试时间和创建时间；具体投递领取方案在 M4 明确。
- 拟定表 notifications：id、recipient_id、event_id、类型、关联作品、已读状态、创建时间；(recipient_id,event_id) 唯一。
- RabbitMQ：确认发布、持久化配置、失败重试与隔离；消息投递按可能重复处理，不宣称恰好一次。
- 业务事务提交但发布失败：outbox 保留待发；发布成功但标记失败：允许重发，由消费者去重。
- Worker：发布器与消费者可作为 cmd/worker 的任务运行，支持 context 取消、断线恢复和明确退出。
- 拟定接口：GET /notifications、PATCH /notifications/:id/read、GET /notifications/stream。
- 通知只给接收者读取；SSE 通道不能信任客户端传来的 recipient_id。
- SSE 鉴权在浏览器客户端实现阶段选定：可用带 Authorization 的 fetch 读取流；不把长期 JWT 放在 URL 中。
- SSE 是提示通道，持久化通知是事实；重连时按位置补读，避免把推送成功当成用户已读。
- 消息处理不直接依赖用户当前是否在线；去重、自己给自己互动不通知等规则在设计评审中明确。

## 9. 工程基础与依赖故障策略（目标）

| 组件 | 承担什么 | 故障时的目标行为 |
| --- | --- | --- |
| MySQL | 账号、作品、关系、互动、通知等事实 | 依赖写入不能假成功；就绪检查体现不可用 |
| Redis | 读缓存、限流、短期会话 | Feed 缓存可受控回源；分片会话不能假定丢失后仍可续传；限流明确失败策略 |
| RabbitMQ | 异步事件传输 | 有 outbox 时交互写入仍可提交，事件延迟，恢复后补发 |
| 本地文件系统 | 视频/封面/分片 | 上传失败不能发布空地址；回收必须先判断归属与生命周期 |
| SSE | 在线通知提示 | 断线重连、补读；不回滚已经完成的互动 |

存活检查拟定 /healthz：进程可处理请求；就绪检查拟定 /readyz：关键依赖可服务。
Redis/MQ 是否导致不就绪取决于接口的降级策略，不能把所有可选依赖一概设为强依赖。
YAML/env 配置在启动时校验；统一管理 MySQL/Redis/MQ 连接和超时，不在每个请求里重新建立客户端。
日志记录请求 ID、操作和必要错误，不记录密码、密钥、完整 token 或上传文件内容。
pprof 受控开放；Compose 与 CI 使用隔离数据和测试账号，不自动对本地业务库执行清理。

## 10. 目录演进：只在需要时创建

```text
cmd/
  server/             当前 API 入口
  cleanup-uploads/    已有手动清理命令
  worker/             M4 再创建
internal/
  user/ auth/         已有用户与认证
  video/              已有作品、上传、点赞、评论
  feed/               已有最新流
  follow/             M2 关注关系（关注流位于 feed/）
  notification/       M4 通知
  config/             M1 配置
  database/ httpx/    已有基础设施
  event/              M4 可靠消息抽象，届时按实际需求收敛
frontend/             M1 Vue 客户端；验收后再替换原 web 发布方式
scripts/migrations/   版本化数据库变更
docs/learning/        路线、模块设计、阶段说明
```

点赞/评论是否从 video 移出属于后续维护选择，不作为当前流程跑通的前置条件。
测试继续邻近对应包；跨进程/环境验收可单独归类，不集中搬走所有 *_test.go。

## 11. 设计变更规则

每个未来模块真正开工前，只细化当前模块的请求/响应、表、索引、权限和失败场景。
本文件中拟定的接口用于稳定方向，不要求现在一次性实现，也不伪装为已发布 API。
改变已实现接口时记录兼容策略和回归范围，不因为参考项目路径不同就批量改名。
性能提升必须有实测；微服务拆分必须有部署、团队或独立扩缩容需求，不能仅由模块多推导。
