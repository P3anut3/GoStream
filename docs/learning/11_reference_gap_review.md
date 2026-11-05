# GoStream 与 feedsystem_video_go：模块盘点和后续重点

核对日期：2026-11-02。参考项目为 LeoninCS/feedsystem_video_go，读取 main 对应提交 `7c1c27d179bf22246931d249267a57df8b5cca4b`。本地以当时源码（现为提交 cdf4a69）为准，验收记录见 07～13。初次盘点之后已补 Compose、Vue 迁移和基础 CI 配置；2026-11-02 阶段提交推送后，GitHub CI 首次运行通过。

结论：GoStream 已形成「注册登录 → 上传 → 发布 → 最新流 → 播放 → 点赞评论 → 我的作品管理」的基础闭环。主要差距集中在缓存、异步事件与通知、关注关系、大文件上传，以及完整的部署和前端工程。不能把这些阶段用一个缺乏依据的完成百分比表示。

## 1. 已有模块和当前边界

| 模块 | 已实现并有验证的能力 | 现有源码 | 当前边界 |
| --- | --- | --- | --- |
| 用户与认证 | 注册、登录、当前用户、bcrypt、JWT 签名/校验、写接口身份来源 | internal/user、internal/auth | 没有 Refresh Token、服务端撤销、资料编辑；页面退出只清除内存 token |
| 上传与媒体 | MP4、JPEG/PNG 上传，大小和类型检查、随机名、本地保存、公开媒体与 Range | internal/video/handler.go、cover.go、storage.go；internal/httpx | 普通上传；没有分片和断点续传，不是对象存储 |
| 发布与作品管理 | 校验资源归属，事务和行锁，发布更新上传状态，详情、个人列表、作者删除 | internal/video/handler.go、model.go | 发布/查询等部分仍直接在 Handler 使用 GORM；删作品不会立即删除物理文件 |
| 最新 Feed | created_at+id 稳定排序、签名游标、limit+1、组合索引、批量作者/点赞信息、游客/登录状态 | internal/feed；scripts/migrations/005_feed_index.sql | MySQL 最新流与关注流（GET /feed/following）；最新流已有 Redis 时间线（最新 500 条，发布/删除后整条重建）；没有热榜或推荐模型 |
| 点赞与评论 | 重复点赞/取消的幂等语义、联合唯一约束、评论校验/分页/本人删除、跨账号权限 | internal/video/like*、comment* | 同步 MySQL 写入；没有通知、已赞列表或 @ 提及 |
| 上传生命周期与清理 | pending/published/deleting，发布与清理协调、过期领取、重试、预览/执行、文件和记录清理 | internal/video/upload*；cmd/cleanup-uploads | 手工运行，没有定时调度；不是所有物理文件的通用回收系统 |
| 页面 | 登录注册、上传发布、视频库、我的视频、播放、点赞评论 | frontend/src、frontend/tests | 已迁移 Vue/TS/Pinia/Router/Vite；登录状态只在本页内存；自动浏览器验收为 Chromium |
| 配置与健康 | YAML+env、启动校验、API/清理共用目录和 DSN；healthz/readyz、超时与取消 | internal/config、internal/health；cmd/server/main.go | YAML 路径仍固定；清理加载完整配置，也要求签名密钥有效；没有热加载 |
| 容器与测试 | 多阶段前后端构建、非 root、数据库停机/恢复、SIGTERM、四服务 Compose、持久化、真实浏览器 | Dockerfile；compose.yaml；cmd/server/*test.go；frontend/tests；.github/workflows/ci.yml | 基础 CI 已在 GitHub 运行通过；Redis 已用于视频详情缓存，MQ 尚无业务调用；没有吞吐量结论 |

现有数据库有 users、videos、uploads、video_likes、video_comments、follows 六张业务表（follows 由 006 迁移加入）。没有 notifications、outbox_events 等表。

分层应按实际源码描述：用户、Feed、点赞、评论已有 Handler → Service → Repository；部分视频与上传操作仍在 Handler，不能宣称已经全项目统一。后续接入视频缓存、发布事件时再沿业务边界下沉这些操作，保留已经实现的归属和事务规则。

## 2. 技术栈逐项差距

| 能力/组件 | 参考项目的实际用途 | GoStream 状态 | 接下来放在哪一步 |
| --- | --- | --- | --- |
| Gin / GORM / MySQL / JWT / bcrypt | HTTP、ORM、业务持久化、认证 | 已使用；Gin/GORM 等核心组件已对齐 | 持续使用，不为匹配旧补丁版本降级 |
| YAML / dotenv | 配置及环境覆盖 | 已有同类能力；当前 goccy/go-yaml，参考直接使用 gopkg.in/yaml.v3 | 若要求具体解析库也一致，M1 收尾时小范围迁移并保留严格解析测试 |
| go-redis / Redis | Token 状态、实体缓存、时间线、热榜、限流、分片会话 | 视频详情 Cache-Aside 与最新流时间线已接入（go-redis v9）；热榜、限流、Token 状态未做 | M3 继续，分片会话在 M5 |
| go-cache / singleflight | 本地短缓存、进程内合并重复回源 | 视频详情已使用：go-cache 5 秒本地缓存挡在 Redis 前面，x/sync singleflight 合并单进程内同一 ID 的回源；最新流时间线的重建也用 singleflight 合并；没有跨实例合并 | 后续缓存按需复用 |
| amqp091-go / RabbitMQ | 互动、热度、关注和时间线等事件传输 | Compose 环境和持久消息测试通过；Go AMQP Client 未接入 | M4 |
| Worker / Outbox | 独立消费入口，发布事件从本地消息表补发；API 内也运行部分任务 | 没有消息 Worker 或 Outbox；清理命令不是消息消费者 | M4 |
| SSE / 通知 | 通知持久化、列表/未读/已读与实时流 | 未实现 | M4，先有通知数据与鉴权再推送 |
| Vue 3 / TS / Pinia / Router / Vite | 页面组件、类型约束、状态与路由、前端构建；参考 vite 指向 rolldown-vite 别名 | 已迁移，采用原生 Rolldown 的 Vite 8；构建及浏览器验收通过 | M1 已完成此项；不为复制旧别名降级 |
| spark-md5 / 分片上传 | 浏览器文件摘要、Redis 会话、上传进度与分片合并 | 未实现 | M5 |
| Docker Compose | MySQL/Redis/RabbitMQ/API/Worker/前端、网络、卷和健康依赖 | 四服务已验收；Vue 构建产物由 API 镜像提供；Worker 待实现 | M1 环境就绪，独立 Worker 在 M4 |
| CI / miniredis / pprof | 自动 Go 检查与前端构建、Redis 测试替身、性能诊断 | 基础 CI 已在 GitHub Actions 运行通过；miniredis 已用于缓存测试；无 pprof | 诊断与性能在 M6 |

核对来源：[后端依赖](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/go.mod)、[前端依赖](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/frontend/package.json)、[Feed 实现](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/internal/feed/service.go)、[路由和装配](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/internal/http/router.go)、[Compose](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/docker-compose.yml)、[CI](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/.github/workflows/ci.yml)。

## 3. 参考项目还多了哪些业务

| 业务差距 | 参考实现 | 我们的优先级 |
| --- | --- | --- |
| 关注/取关、关注与粉丝列表/计数、关注流 | social 和 feed.ListByFollowing | 关注/取关/状态与关注流已实现；粉丝列表/计数待做 |
| 通知列表、未读、已读、SSE | worker/notificationworker.go、ssehub.go | 高：异步主线的用户可见结果 |
| 分片、进度查询、合并、恢复上传 | video/chunk_handler.go | 高：完整技术栈对齐的一部分，但在 Redis 基础后做 |
| Refresh Token、服务端登出/撤销、限流 | account/service.go、middleware/jwt、ratelimit | 中高：放在 Redis 阶段，明确过期/刷新/撤销语义 |
| 点赞榜、热榜、话题流 | feed 的排行/快照/ZSET 和 ListByTag | 可在缓存/事件基础完成后扩展，热度统计与 MQ 结合 |
| 头像、简介、改名改密、主页统计、已赞列表 | account、video/like | 后续补齐普通业务，不阻塞核心中间件主线 |
| 私信、评论 @ 提及 | message/handler.go、comment_service.go | 后置，可选功能；用户目标允许功能不全部复刻 |

来源：[账号 Service](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/internal/account/service.go)、[关注 Service](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/internal/social/service.go)、[分片处理](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/internal/video/chunk_handler.go)、[通知与 SSE](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/internal/worker/ssehub.go)、[私信](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/internal/message/handler.go)。

## 4. 需要明确的设计取舍

### 同步业务写入与异步任务

参考 LikeService、CommentService 在 MQ 可用时尝试异步落库，投递失败时有同步 MySQL 回退。参考视频发布会在事务中同时写 Video 与 OutboxMsg，再由轮询器投递时间线事件；不能据此说所有事件都已经使用事务 Outbox。

GoStream 目前以同步 MySQL 成功作为点赞/评论成功依据。后续先保留这一点，在同一事务写待投递事件，由 Worker 将事件交给 RabbitMQ，再完成通知/热度等异步工作。这样仍覆盖 RabbitMQ、Worker、Outbox、重试和幂等；如果以后改为异步业务落库，需要另行说明接口成功语义、读写延迟和故障恢复。

这是未来方案，当前还没有事件表、队列或消费者。参考源码：[点赞 Service](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/internal/video/like_service.go)、[评论 Service](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/internal/video/comment_service.go)、[发布事务](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/internal/video/video_service.go)、[Outbox 轮询器](https://github.com/LeoninCS/feedsystem_video_go/blob/7c1c27d179bf22246931d249267a57df8b5cca4b/backend/internal/worker/outboxworker.go)。

### 架构和推荐能力

参考有独立 API、Worker 入口，API 装配中也启动部分 Outbox、时间线和通知任务。它提供了模块化应用加后台任务的参考，不能仅因有两个进程就称为完整的业务微服务架构。

当前可核实的 Feed 策略是最新、关注、点赞榜、热榜和话题流；本轮没有发现独立的个性化推荐模型/训练/在线推理链路。后续继续保留推荐候选与 Feed 组装的扩展边界，暂不开发模型。

本轮所读上传实现与 Compose 使用本地文件和卷。对象存储、转码集群、服务注册发现不列为这份参考栈必须对齐的项目；没有因为名称类似就把最初参考项目的组件混入本计划。

参考中存在某项实现，不等于该项已经经过我们的运行验证，更不代表可以直接承诺生产规模、正确性或吞吐量。本轮对参考项目进行了源码核对，没有执行它的部署或测试。

## 5. 接下来固定按这个顺序推进

| 阶段 | 重点任务 | 必须留下的验收结果 |
| --- | --- | --- |
| M1 收尾：环境与展示（完成） | 重建验证 SIGTERM；Compose API/MySQL/Redis/RabbitMQ，上传文件持久卷；Vue/TS/Pinia/Router/Vite 迁移；整理 Git 阶段提交/配置远端/基础 CI | 干净环境可启动；API 重建后作品与文件保留；默认停止正常退出；前端构建/类型检查与现有业务通过；CI 实际运行 |
| M2：关注与关注流（完成） | 关注关系表、幂等操作、防自关注、关注/粉丝查询、关注流、页面入口 | A 关注 B 后出现 B 作品，取消后刷新不出现；不能伪造操作者 |
| M3：缓存与认证增强 | Cache-Aside → Redis 时间线 → 本地缓存/singleflight；限流；刷新/撤销策略 | 命中/未命中、失效、回源、Redis 故障都有可复现测试；个人状态不污染公共缓存；失效凭证不能通过 |
| M4：可靠事件与通知 | 业务事务+Outbox、RabbitMQ 发布与消费、独立 Worker、重试/幂等、通知数据与 SSE；有余力再接热度榜 | MQ 故障后补发；重复消息不重复产生结果；作者能收到自己的通知，重连可补读，不能订阅他人通知 |
| M5：分片续传 | 用户绑定的上传会话、Redis 状态、分片校验/幂等、合并、spark-md5、过期清理 | 中断后只补缺失分片，跨账号不能接管；合并后仍走 uploads → Publish 原有归属与生命周期规则 |
| M6：工程证据 | 扩展 CI、pprof、请求日志/指标、故障演练、对比测试与文档 | 测试和构建可重复；报告真实环境/数据量/并发/延迟/查询次数；保留诊断和修复证据 |

M1 中把 Redis/MQ 容器启动起来只记为「依赖环境可用」；M3/M4 有实际读写和故障链路后，才能记为已接入对应组件。

M1 和 M6 的分工：M1 先建立基础自动检查，不等到功能全部完成；M6 再扩展集成、性能和故障验证。Redis 的 miniredis 测试在 M3 接入，不能只添加依赖名称。

前端迁移、测试和工具由导师承担；业务实现继续由学习者编写、导师按文件和调用链指导。阶段完成后更新记录和提交，避免反复改目录而业务主线不前进。

## 6. 当前最短行动清单

1. 学习者已添加 os.Interrupt + syscall.SIGTERM；导师已重建镜像，并用默认 docker stop 验证正常退出。
2. Compose 网络、服务名、初始化、健康依赖、命名卷与端口已完成；隔离测试验证 down/up 后账号、作品、上传文件、Redis 值和 RabbitMQ 消息保留。见 [Compose 环境](12_compose_environment.md)，不复用本机业务库做初始化或清理。
3. Vue 技术栈和前后端真实双账号闭环验收已完成，见 [迁移记录](13_vue_migration.md)。
4. 2026-11-02 已创建阶段提交 cdf4a69、关联 GitHub 远端 origin 并推送；GitHub Actions 首次运行（GoStream checks #1）的 backend、frontend 均通过。
5. M1 已完成，下一步进入关注模块，再进入 Redis 与异步事件主线。

## 7. 当前已经能够解释的项目内容

JWT 身份与目标资源 ID 的区别、权限落在实际写条件中；发布和清理的行锁协调；上传文件与数据库的补偿边界；幂等点赞和联合唯一约束；组合游标和联合索引；批量组装避免逐条查询；健康检查与依赖就绪；容器内服务地址、普通用户权限和 MySQL 停机恢复。

这些已有源码和验收记录支撑。Redis 缓存命中率、MQ 消费吞吐、SSE 连接规模、分片续传、生产 QPS 等还没有实现或测量，不能写成已完成成果。
