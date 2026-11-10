# GoStream 开发与教学交接（给 Claude）

交接日期：2026-11-02。请先阅读本文件，再核对实际源码。本文件区分已实现、已验证与后续设计，不要把计划当成现状。

## 1. 先明确项目和协作方式

实际项目根目录：`D:\code\Golang\GoStream`。

聊天工具可能默认打开 `D:\code\Golang\Titok`，但后续开发应进入 GoStream，不要在 Titok 新建一份项目。

用户目标是准备 Go 后端开发实习面试，正在边学习边开发短视频社区。已经会一些 Go 基础和 Gin，但对接口、依赖注入、分层、JWT、事务和工程组件仍需要具体调用链讲解。

必须延续的协作约定：

- 后端业务默认由用户练习实现。你给出真实文件位置、一小段任务、必要代码和讲解，再检查他的修改；不要未经要求把整个后端模块写完。
- 前端、测试、测试修复、构建、部署和通用工具由导师直接完成并执行。不要把测试脚本的问题交给用户排查。
- 用户明确说“你直接实现/替我修改”时，可以接手对应业务范围；这不等于后续全部业务都交给导师。
- 每次推进一个有实际流程的功能，解释输入、输出、权限、数据流和失败行为。不要反复只改目录、重复分层，或一次加入全部组件。
- 当前已经是正式项目，不要重新起一个“正式版”、推倒重写或复制整个参考工程。
- 用中文，适合初学者，重要决策说明动机和取舍；保留用户自主理解和编写代码的空间。

用户最后要求：生成交接文档，转由 Claude 继续。上一轮前端迁移已完成；下一项业务是 M2 关注关系与关注流。

## 2. 项目主线与参考基线

最早参考过 TikGok，但用户后来明确希望技术栈对齐：

https://github.com/LeoninCS/feedsystem_video_go

已核对的参考提交：`7c1c27d179bf22246931d249267a57df8b5cca4b`。若继续比较新版本，应重新查阅源码并标明版本，不从 README 宣传推断实现。

用户重视技术栈实际使用，功能不要求完全一样。目标覆盖 Gin、GORM、MySQL、JWT/bcrypt、YAML/env、Redis/go-redis、go-cache/singleflight、RabbitMQ/amqp091-go、API/Worker、Outbox、SSE、Vue/TS/Pinia/Router/Vite、分片上传/spark-md5、Compose、CI 和 pprof。

当前保持模块化单体。未来 API 与 Worker 可以是独立进程，不等于拆成业务微服务。不要重新引入最初 TikGok 的 Hertz、Kitex、注册发现等，除非用户另有要求。对象存储和转码集群也不属于当前主线。

## 3. 接手先读这些文件

按顺序阅读，不需要先逐行扫描全部业务：

1. `docs/learning/00_project_roadmap.md`：统一里程碑和教学分工。
2. `docs/learning/11_reference_gap_review.md`：参考栈差距与真实边界。
3. `docs/learning/13_vue_migration.md`：最新前端、部署和验收记录。
4. `README.md`、`frontend/README.md`：启动与复现命令。
5. `cmd/server/main.go`：配置、依赖装配、实际路由和退出信号。
6. `scripts/schema.sql`、`scripts/migrations/`：六张现有表及 001～006 迁移。
7. 改 Feed 或关注流前读 `internal/feed/{entity,cursor,service,repository,handler}.go`。

其他记录：07 为 M0 验收，08 配置，09 健康检查，10 Docker，12 Compose。较早文档记录的是当时实现；最新现状以源码和 00/11/13 为准。

## 4. 已完成的模块

| 模块 | 当前能力 | 源码及边界 |
| --- | --- | --- |
| 用户与认证 | 注册、登录、当前用户、bcrypt、HS256 JWT、Bearer 中间件 | `internal/user`、`internal/auth`；用户模块已分层；token 有效期两小时，无 refresh token 和服务端撤销 |
| 上传 | MP4 最大 100 MiB；JPEG/PNG 封面最大 5 MiB；校验、随机名、本地保存与上传记录 | `internal/video/handler.go`、`cover.go`、`storage.go`；普通上传，不是分片或对象存储 |
| 发布 | 校验视频/封面归属，事务与行锁，创建作品并更新上传状态 | `internal/video/handler.go`、`upload_repository.go`；当前没有管理员审核 |
| 作品管理 | 公共列表、详情、我的视频、仅作者删除 | 部分仍直接在 Handler 使用 GORM，不宣称全项目已统一分层 |
| 最新 Feed | created_at DESC、id DESC；签名组合游标；limit+1；批量作者/点赞信息；最新流先读 Redis 时间线 | `internal/feed`；时间线缓存最新 500 条（ZSET 存顺序、HASH 存公开字段），发布/删除后整条失效、读时重建，超出窗口或故障回源 MySQL；关注流直接查 MySQL；无推荐模型 |
| 点赞 | 点赞/取消幂等、当前用户状态与总数 | `internal/video/like*`；Handler → Service → Repository，同步 MySQL 写入 |
| 评论 | 创建、分页、仅评论作者删除、Unicode 内容校验 | `internal/video/comment*`；已分层，同步 MySQL 写入 |
| 关注关系 | 关注/取消/状态；有向、幂等、禁止自关注、目标不存在 404 | `internal/follow`、`006_follows.sql`；已分层；关注按钮与关注流（GET /feed/following、关注标签页）已完成 |
| 视频详情缓存 | GET /videos/:id 两级缓存：进程内 go-cache 5 秒 + Redis Cache-Aside；未命中经 singleflight 合并回源；不存在的 ID 写 30 秒标记；删除后失效、发布后清标记；Redis 故障降级 MySQL | `internal/video/video_{repository,cache,local_cache,service}.go`；Redis TTL 10 分钟加抖动；singleflight 只合并单进程内的回源；多实例的本地缓存最多陈旧 5 秒 |
| 限流 | 登录、注册按客户端 IP，写操作按登录用户，固定窗口计数；超限返回 429 + Retry-After；Redis 故障或未配置时改用单机计数 | `internal/ratelimit`；登录每分钟 10 次、注册每小时 10 个、发布/上传/评论/点赞/关注/删除共用每分钟 60 次；读接口未限流，没有按账号限流 |
| 上传清理 | pending/published/deleting 生命周期；24 小时未发布上传的手工预览/执行清理 | `cmd/cleanup-uploads`、`upload_cleanup*`；行锁协调发布与清理、失败重试，无自动定时调度 |
| 配置与健康 | YAML+env 覆盖与校验；healthz/readyz；SIGINT/SIGTERM 退出 | `internal/config`、`internal/health`；readyz 对 DB Ping 最长 1 秒 |
| 前端 | Vue/TS/Pinia/Router/Vite；注册登录、发布播放、Feed、我的作品、点赞评论、作者删除 | `frontend/src`；迁移及真实浏览器验收已完成 |
| 容器 | 多阶段前后端构建、非 root、四服务 Compose、命名卷、健康依赖 | `Dockerfile`、`compose.yaml`；环境和持久化已验收 |
| CI | Go 格式/vet/race/构建，前端测试/类型检查/构建/Chromium 夹具测试 | `.github/workflows/ci.yml`；2026-11-02 GitHub Actions 首次运行（#1，cdf4a69）通过 |

现有表只有：`users`、`videos`、`uploads`、`video_likes`、`video_comments`、`follows`。

重要边界：

- Redis 已通过 go-redis v9 用于视频详情缓存（Cache-Aside、删除后失效、故障降级），前面还有 go-cache 进程内缓存，回源经 singleflight 合并；最新流有 Redis 时间线；限流计数也放在 Redis（故障时降级为单机计数）；关注流还没有缓存。RabbitMQ 没有 Go Client 或调用，不能说异步业务已接入。
- 没有 notifications、outbox 表，没有独立消息 Worker、SSE 或分片上传。
- 删除作品会删除作品记录，当前不会立即删除物理视频文件；不要把这个既定行为当成前端迁移遗漏。
- 已发布文件不属于“过期未发布上传”的清理范围；pending 是上传生命周期，不是审核状态。
- 媒体公开可读；未发布文件知道地址也能访问。前端隐藏按钮不等于后端授权。
- 当前退出只清除前端内存凭证，不能撤销已签发 JWT。
- 没有压测或生产规模数据，不要杜撰 QPS、性能提升百分比或上线成绩。

## 5. 当前目录与关键装配

```text
GoStream/
├── cmd/server/                 # Gin 入口、路由、HTTP/容器验收测试
├── cmd/cleanup-uploads/        # 手工清理命令及测试
├── internal/
│   ├── auth/                   # JWT、必选/可选身份中间件
│   ├── config/                 # config.yaml、Load、Validate
│   ├── database/               # MySQL 连接池、GORM
│   ├── user/                   # 用户 Handler/Service/Repository
│   ├── feed/                   # 最新流、游标、批量元数据
│   ├── video/                  # 发布、上传、互动、清理
│   ├── health/                 # Live、Ready
│   └── httpx/                  # 请求体限制、媒体、Vue 产物路由
├── frontend/                   # 当前 Vue 源码、锁文件和测试
├── web/dist/                   # Vite 生成产物，Git 忽略
├── scripts/                    # 初始 schema、001～005 迁移、环境生成
├── docs/learning/              # 学习与验收文档
├── compose.yaml
├── Dockerfile
└── .github/workflows/ci.yml
```

`main()` 读取可选 `.env` → `config.Load("internal/config/config.yaml")` → 创建游标 Codec 和 GORM → `newRouter` 装配 → HTTP Server → 捕获 os.Interrupt/syscall.SIGTERM。

用户、Feed、点赞、评论通过具体 Repository 实现接口注入 Service，再注入 Handler。视频发布等仍有 Handler 使用 DB 的代码。后续按业务演进提取，不要再开启一轮全项目统一分层。

Feed 的 `Service.ListLatestForViewer` 调 Repository 查询 limit+1 条，裁剪结果并批量 `LoadMetadata`，返回 ListResponse。非空正常链路批量查询，不逐条查作者/点赞。005 迁移已增加 `(created_at,id)` 索引；小表 EXPLAIN 可能选择全表扫描，这不等于索引无效。

## 6. 当前 HTTP 约定

| 方法 | 路径 | 身份 |
| --- | --- | --- |
| POST | /users、/login | 无 token；登录用用户名密码 |
| GET | /me、/me/videos | JWT |
| GET | /feed | 可选 JWT，提供时必须有效 |
| GET | /feed/following | JWT；只含已关注作者；游标与最新流互不通用 |
| GET | /videos、/videos/:id | 公开 |
| POST | /videos | JWT，发布元数据 |
| DELETE | /videos/:id | JWT，仅作者 |
| POST | /uploads/videos、/uploads/covers | JWT，multipart 字段名 file |
| GET/PUT/DELETE | /videos/:id/like | JWT |
| GET | /videos/:id/comments | 公开 |
| POST | /videos/:id/comments | JWT |
| DELETE | /comments/:id | JWT，仅评论作者 |
| GET/PUT/DELETE | /users/:id/follow | JWT；PUT 自关注 400、目标不存在 404；DELETE 一律 204 |
| GET/HEAD | /media/videos/:filename、/media/covers/:filename | 公开，支持 Range |
| GET | /healthz、/readyz | 公开 |

JSON 错误通常为 `{ "error": "安全提示" }`；Feed 另有 code，如 INVALID_CURSOR。存储内部错误进服务端日志，不原样返回浏览器。204 没有 JSON 响应体。

Feed 响应含 videos、next_cursor、has_more；前端原样回传 cursor。游标内部含版本、kind=latest、created_at、id，以独立密钥 HMAC 签名；它不代表用户身份或观看进度。刷新清空游标，退出页面不自动保存观看进度。

## 7. 前端交接：已经迁完，不要重写旧页面

- Vue 3.5.44、Pinia 3.0.4、Vue Router 4.6.4、TypeScript 5.9.3、Vite 8.3.5；精确版本与锁文件见 frontend。
- 参考项目用 rolldown-vite 别名；当前 Vite 8 原生使用 Rolldown，不为复制旧别名降级。
- `frontend/src/api/client.ts` 处理 fetch、JSON/FormData、204 和错误。
- `stores/session.ts` 保存登录身份、会话版本和登录窗口；`library.ts` 分开管理公共游标/个人页码；`publish.ts` 管草稿、上传缓存与发布顺序。
- 页面在 views，登录窗口、视频卡片和评论区在 components。
- token 与草稿只在页面内存，不写 localStorage/sessionStorage。刷新需要重新登录，前端不能持有 JWTSecret。
- 页面路由为 `/#/`、`/#/mine`、`/#/publish`、`/#/account`、`/#/videos/:id`。Hash 避免与后端 API 路径冲突，未知 API 保留真实 404。
- 上传顺序：视频 → 可选封面 → POST /videos。同次会话发布失败重试复用已成功上传结果；换文件清除对应缓存，换账号清空草稿。
- 会话版本和请求序号防止旧响应污染新账号/页面。改动时保留这些保障。
- `npm run build` 先类型检查，输出 `web/dist`。`httpx.RegisterWeb` 只提供 dist 首页、assets、默认封面。
- 旧 `web/index.html/app.js/style.css` 和 `scripts/web_*_test.cjs` 仅保留学习对照，不由当前路由/镜像使用。新功能改 frontend/src，新测试放 frontend/tests，不要改旧 web/app.js 或手改 dist。

## 8. 环境与启动（不要把秘密写入交接或日志）

Windows PowerShell，已使用 Go 1.27.1、Node 24.18.0/npm 11、MySQL 8.4.9、Docker Desktop/Compose。接手时重新检查工具是否可用，不自动升级工具链。

### 推荐：Compose

在项目根目录执行：

```powershell
Set-Location D:\code\Golang\GoStream
docker info
./scripts/init-compose-env.ps1
docker compose --env-file .env.compose up -d --build --wait --wait-timeout 180
docker compose --env-file .env.compose ps
```

`.env.compose` 已存在且 Git 忽略，脚本存在即保留，不覆盖密码。公开模板为 `.env.compose.example`。最终交接核查时四服务都是 running/healthy；这是当时状态，不保证以后 Docker 一直运行。

| 用途 | Windows 地址 | 容器网络内地址 |
| --- | --- | --- |
| 页面/API | http://127.0.0.1:19080/ | api:8080 |
| MySQL | 127.0.0.1:13307 | mysql:3306 |
| Redis | 127.0.0.1:16379 | redis:6379 |
| RabbitMQ AMQP | 127.0.0.1:15673 | rabbitmq:5672 |
| RabbitMQ 管理页 | http://127.0.0.1:15674/ | rabbitmq:15672 |

容器应用数据库账号和 RabbitMQ 默认账号为 gostream，真实密码只在本机配置。不要打印 `.env`、`.env.compose`、完整 DSN 或解析后的 `docker compose config`；校验用 `config --quiet`。

四个命名卷保存数据库、媒体、Redis、RabbitMQ。普通 `down` 保留，`down -v` 会删除数据，不当成日常重启。不要清理原停止状态的 redis 容器、redis-data 卷或 Windows MySQL。

Compose 与 Windows 原 MySQL 是两套数据：不会自动导入旧账号/作品，也不复用根目录 uploads。MySQL 初始化 schema 只在空卷首次运行；改 schema 后重启不能迁移旧库。密码环境变量也不会自动修改已有卷中的账号密码。

### 本机 Go + Vite

本机 Go 使用已有 `.env`，数据库地址为 Windows 的 127.0.0.1:13306。不要把这一 DSN 放入 API 容器；容器使用 mysql:3306。

```powershell
npm --prefix frontend ci
npm --prefix frontend run build
go run ./cmd/server
```

默认本机页面/API 为 http://127.0.0.1:8080/，实际取决于本机配置。开发热更新在 frontend 执行 npm run dev，访问 5173；默认代理 API 到 19080。本机 API 为 8080 时，执行 dev 前设置 `GOSTREAM_DEV_PROXY=http://127.0.0.1:8080`。

配置非敏感默认项在 internal/config/config.yaml；GOSTREAM_DSN、JWT_SECRET、GOSTREAM_FEED_CURSOR_SECRET 来自环境；HTTP/媒体目录也可由环境覆盖。Redis 可选：YAML `redis.addr` 默认为空，`GOSTREAM_REDIS_ADDR`、`GOSTREAM_REDIS_PASSWORD` 来自环境，Compose 中 API 使用 `redis:6379`。JWT 与游标签名使用不同密钥。清理命令目前共用完整配置校验，也要求这两项密钥，虽清理本身不用 JWT。

## 9. 已验证结果及复现

以下是交接前上一轮实际结果，本次生成文档没有重新运行全部测试：

- Windows go test ./...、go vet ./...、两个入口构建与 gofmt 检查通过。
- Docker checks 目标在 Linux 实际通过 vet 和 go test -race ./...。Windows 当前 CGO_ENABLED=0，普通 Windows 测试不能声称 race 通过。
- Vitest 12 项通过；Chromium 6 项 API 响应夹具页面测试通过。
- 隔离 Compose 项目中真实 Chromium 双账号验收通过：注册自动登录、视频/封面上传、发布、实际 MP4 解码与播放时间推进、Range 206、点赞评论、本人评论删除、越权拒绝、公共和个人 6+1 分页、作者删除。
- API 容器测试通过：非 root、自定义媒体目录、上传发布、MySQL 停机 readyz 200→503→200、healthz 保持 200、数据保留、默认 SIGTERM 退出码 0。
- Compose 全部容器 down/up 后账号/JWT、作品、上传文件、Redis 值和 RabbitMQ 持久消息保留；随机测试项目资源最终清理。
- 最后开发环境检查首页与构建 JS 返回 200、readyz=ok。不能宣称全浏览器兼容或生产压测；GitHub CI 首次运行结果见第 10 节。

普通检查：

```powershell
go test ./...
go vet ./...
go build ./cmd/server ./cmd/cleanup-uploads
npm --prefix frontend test
npm --prefix frontend run build
Push-Location frontend
npx playwright install chromium
Pop-Location
npm --prefix frontend run test:e2e
```

需要 Linux race 验证时：`docker build --target checks -t gostream-checks:dev .`。

真实 Compose + 浏览器测试（先准备当前 API 镜像、其余三个镜像、npm 依赖和 Playwright Chromium）：

```powershell
docker build -t gostream-api:dev .
$env:GOSTREAM_COMPOSE_TEST='1'
$env:GOSTREAM_COMPOSE_BROWSER_TEST='1'
try {
    go test ./cmd/server -run '^TestComposeEnvironmentIntegration$' -count=1 -v -timeout=10m
} finally {
    Remove-Item Env:GOSTREAM_COMPOSE_TEST,Env:GOSTREAM_COMPOSE_BROWSER_TEST
}
```

容器停机恢复专项：设置 GOSTREAM_DOCKER_TEST=1，执行 `go test ./cmd/server -run '^TestContainerAPIIntegration$' -count=1 -v -timeout=10m`，随后清除开关。

普通测试默认跳过外部依赖。MySQL 集成测试另由 GOSTREAM_INTEGRATION_TEST=1 显式启用，部分读取根 .env；不要不检查连接目标就运行写入测试。Compose/容器测试使用随机临时项目、凭证、端口/网络和数据，不读本机业务凭证。

默认前端 Playwright 使用响应夹具，不证明真实 MySQL 成功。真实 live.spec.ts 由 Compose 测试传入临时地址和隔离标志，不应手工指向开发库跑造数。

测试视频为 frontend/tests/fixtures/playable.mp4，自行生成的移动色块，不含用户媒体。截图和 trace 在 frontend/test-results，已忽略。只覆盖 Chromium，不宣称所有格式/编码可播放。

## 10. Git 与远端（2026-11-02 已收尾）

交接之后已完成 M1 的工程收尾：

- 阶段提交 `cdf4a69 feat: migrate to Gin/GORM, add latest feed, config and Vue frontend` 收入了交接时全部已暂存、未暂存和未跟踪的成果（143 个文件）。提交前用本机 `.env`、`.env.compose` 的真实密钥值比对过全部入库文件，无命中。
- 远端 origin 是 GitHub 仓库 `P3anut3/GoStream`（当前 Private），main 已推送并跟踪 origin/main。
- GitHub Actions 首次运行（GoStream checks #1，提交 cdf4a69）的 backend、frontend 两个 job 均通过。CI 不包含需要真实 MySQL 或 Docker 的集成测试。

本机全局 git 配置了代理 `127.0.0.1:7890`，代理没开时访问 GitHub 会失败，可用 `git -c "http.proxy=" -c "https.proxy=" push` 临时直连。本机没有 gh CLI，仓库为 Private 时匿名 API 读不到 CI，结果需要用户在网页查看。

以后仍不能用 reset --hard、checkout --、clean 等丢弃用户未提交的成果，也不要把真实配置和上传媒体加入 Git。

本机 .env/.env.compose、node_modules、web/dist、上传目录和测试证据已忽略。不要因为忽略文件不在 Git，就随意重建或覆盖本机凭证。

## 11. M2 关注关系与关注流（已完成）

进度（2026-11-03）：M2 已完成并提交。follows 表（006 迁移）、关注/取消/状态 API（PUT 204/400/404，DELETE 一律 204，GET 200/404）、详情页关注按钮、关注流 GET /feed/following（游标在签名内绑定流类型）和视频库关注标签页均已实现；单元、HTTP、真实 MySQL 集成、端到端、Vitest 14 项、Chromium 夹具 9 项和隔离 Compose 真实浏览器验收通过。用户的 Windows 库和 gostream-dev 数据卷还没有执行 006，须用户确认后再执行；gostream-api:dev 已按新代码重建，dev 栈下次重建 API 容器时会用到它。下一项是 M3。

M3 进度（2026-11-04）：第一步视频详情 Cache-Aside 已完成并提交（go-redis v9.23.0、miniredis v2.40.0）；gostream-dev 已执行 006，API 容器已换新镜像并连上 Redis。用户决定以后只用 Compose 开发环境，不再使用 Windows 本机 MySQL。

M3 第二步（2026-11-05）：视频详情两级缓存已完成。go-cache v2.1.0 进程内缓存 5 秒挡在 Redis 前面；singleflight（x/sync v0.24.0）用 DoChan 合并同一 ID 的并发回源，共享查询不随第一个调用方取消，另有 3 秒超时；MySQL 确认不存在时写 30 秒 `missing` 标记，发布事务提交后清除；Redis 客户端设 `DialerRetries: 1`（go-redis 默认建连重试 5 次）。三个业务文件由用户按参考实现编写。Windows 单元与一次性 MySQL 集成测试、Linux vet/race、gostream-dev 真实 Redis（本地命中不发 Redis GET，标记 TTL 29 秒）和隔离 Compose 真实浏览器验收通过。下一步：时间线缓存。

M3 第三步（2026-11-06）：最新流时间线已完成。`internal/feed/timeline.go` 的 RedisTimeline 用有序集合 `gostream:feed:latest:v1:ids`（成员补零到 20 位，分数为毫秒发布时间，底部哨兵 `end`/`more`）加哈希 `gostream:feed:latest:v1:videos`（公开字段 JSON）缓存最新 500 条，1 分钟过期；`feed.Service` 先问时间线，缺失时 singleflight 重建（读 MySQL 501 行），超出窗口或 Redis 故障回源 MySQL，作者名与点赞信息仍每次查 MySQL。`video` 包定义 `LatestFeed` 接口，在发布提交后、删除成功后调用 `InvalidateLatest` 让时间线整条失效，由 `main.go` 装配。没有做发布时增量 ZADD（`Video.CreatedAt` 由 MySQL 默认值填写），留作优化。`timeline.go`、`service.go` 由用户按参考实现编写。Windows 单元与一次性 MySQL 集成测试（带/不带 Redis 两个路由逐页 JSON 一致）、Linux vet/race、gostream-dev 真实 Redis（换镜像前后首页逐字节一致，游标页走 Redis 且日志无降级）和隔离 Compose 真实浏览器验收通过。下一步：限流。

M3 第四步（2026-11-10）：限流已完成。`internal/ratelimit` 提供 `Rule`、`RedisLimiter`（MULTI 里 INCR + EXPIRE NX + PTTL 的固定窗口，key 为 `gostream:ratelimit:v1:{规则}:{ip:… 或 user:…}`）、`LocalLimiter`（go-cache 加锁计数）、`FailoverLimiter`（Redis 出错改用单机计数）和 Gin 中间件（`ByIP`、`ByUser`，超限 429 + 向上取整的 Retry-After，规则无效启动即 panic，缺少用户 ID 返回 500，限流器失败时放行）。`main.go` 给登录（每个 IP 每分钟 10 次）、注册（每个 IP 每小时 10 个）和全部写操作（排在 JWT 之后，每个用户每分钟共 60 次）挂上限流，读接口不限；没有 Redis 时只做单机计数。用户这一步让我直接写入参考实现。Windows 单元与一次性 MySQL 集成测试、Linux vet/race、gostream-dev 真实 Redis（第 11 次登录 429，计数器 `…:login:ip:172.18.0.1` 的 TTL 59 秒，验证后已删除恢复额度）和隔离 Compose 真实浏览器验收通过。已知局限：固定窗口交界处可能放过两倍请求；只按 IP 限登录；Redis 不可用时每个请求仍先尝试 Redis，未做熔断。下一步：认证刷新与撤销。

以下为原设计，已按此实现。后续里程碑仍按小步骤指导，不要一次写完 M3～M6。

### 第一节建议

先用 A 关注 B 的场景解释：操作人 A 来自 JWT，目标 B 来自 URL。关系是有向的，A 关注 B 不代表 B 关注 A。让用户解释为什么不能从请求体指定操作人，然后一起确认表字段和唯一约束。

建议新模块 internal/follow，沿用 Handler → Service → Repository。表建议含 follower_id、followee_id、created_at，联合唯一/主键防重复；是否补反向索引、外键等根据实际查询与删除策略决定。迁移当前到 005，新迁移预计 006，创建前重新检查目录，并同步新库 schema。不要自动对用户原数据库执行 DDL。

可以采用以下候选接口，先确定约定，再实现：

- PUT /users/:id/follow：关注；重复关注幂等。
- DELETE /users/:id/follow：取消关注；重复取消幂等。
- GET /users/:id/follow：查询当前用户与目标的关注状态。
- GET /feed/following：已登录用户的关注流。

状态码与“目标不存在时取消”的具体语义尚未定稿，应说明取舍后统一 Handler、Service 和测试。禁止关注自己；Service 校验，数据库唯一键保证并发防重；关注者身份仍由后端验证。

先完成关注/取消/状态的 API 和测试，再加作者关注按钮，最后实现关注流。第一版不必做复杂个人主页、推荐算法、粉丝通知或 MQ。

### 关注流的特别注意

- 只查询当前用户关注作者的作品，不能信任客户端提供的 viewer_id。
- 保留 created_at、id 的稳定排序与 limit+1，复用批量作者/点赞元数据，不增加逐条查询。
- 当前 CursorCodec 只接受 kind=latest，不能直接把它当通用 following 游标。扩展时明确流类型与调用上下文，保持最新流兼容并补测试；身份授权不能由游标签名替代。
- 说明关注关系变更时的读取语义；不要宣称跨页快照或永久无重复/无遗漏，而源码并无快照机制。
- 前端关注流与公共流分别管理分页位置，切换账号或取关后刷新，防止旧响应污染。

验收至少包括：A 关注 B 后能在关注流读到 B 作品；取消后新查询不可见；A/B 关系有向隔离；重复操作幂等；自关注拒绝；未登录和非法 ID 被拒绝；跨账号不能替别人建立关系。

## 12. 后续里程碑（不要提前同时做）

| 阶段 | 主线 |
| --- | --- |
| M1 | 已完成：Vue/容器与本地验收、阶段提交 cdf4a69、GitHub 远端与首次 CI |
| M2 | 已完成：关注关系与关注流 |
| M3 | 进行中：视频详情 Cache-Aside、本地缓存/singleflight、空值缓存、最新流时间线、限流已完成；余下认证刷新/撤销策略 |
| M4 | 同步业务事实 + 同事务 Outbox、RabbitMQ、独立 Worker、幂等/ACK/重试、通知与鉴权 SSE |
| M5 | Redis 上传会话、分片/进度/合并、spark-md5、续传、过期清理 |
| M6 | 扩展 CI/集成/故障验证、日志与指标、受控 pprof、真实测量和面试证据 |

已确定的异步方向：第一阶段保留点赞/评论的同步 MySQL 成功语义；事务中写待投递事件，再异步通知。不要为了“使用 MQ”直接把核心写入改成未知结果的异步落库。

## 13. 给 Claude 的开场执行清单

1. 确认工作目录是 GoStream，读本文件和 00/13 文档，查看 Git 工作区。
2. 用几句话向用户确认已完成的闭环与当前边界，不要求重新搭建已有模块。
3. 如要验证基线，先普通测试和前端构建；真实写入验收使用隔离环境。
4. 从 M2 的关系表和权限模型开始教，一次给出一个明确文件的小任务。
5. 用户完成业务后，由你补测试并实际执行，再完成对应页面接入。
6. 保持文档状态、源码和验证结论一致。失败先修本轮范围，不让主线再次陷入无限重构。

用户可以直接对你说：“请先读 HANDOFF_CLAUDE.md，再按原来的教学分工，从 M2 关注关系的表设计和权限来源开始带我做。前端和测试你负责，后端业务指导我自己写。”
