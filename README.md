# GoStream

一个用于学习 Go 后端开发的短视频项目，逐个模块实现和验证。当前完成用户认证、视频元数据管理、视频及封面本地上传、文件归属校验与公开文件访问，并提供 Vue 3 + TypeScript + Pinia + Vue Router 工作台。

开发与学习的统一入口：[项目主线与阶段计划](docs/learning/00_project_roadmap.md)、[模块设计与数据流](docs/learning/04_module_design.md)。M0 核心流程见 [验收记录](docs/learning/07_m0_acceptance.md)。配置管理、健康检查、[API 镜像](docs/learning/10_container_basics.md)、[Compose 持久化](docs/learning/12_compose_environment.md)、[Vue 迁移与真实双账号浏览器验收](docs/learning/13_vue_migration.md)已完成。M1 阶段提交已推送到 GitHub，基础 CI 首次运行通过。

## 当前功能

- Gin HTTP 服务，单进程运行。
- GORM + MySQL 数据访问，用户名唯一约束。
- bcrypt 密码哈希与校验，不存储明文密码。
- HS256 JWT，包含用户 ID、签发时间和两小时有效期。
- Bearer token 鉴权中间件。
- 公开健康检查：`GET /healthz` 不查数据库；`GET /readyz` 对数据库连接池执行最长 1 秒的 PingContext。成功返回 200、`{"status":"ok"}`；就绪失败返回 503、`{"status":"not_ready"}`，不返回底层错误，详见 [健康检查](docs/learning/09_health_checks.md)。
- 密码、JWT 和鉴权中间件的基础单元测试。
- 视频元数据存储、公开查询、分页查询和仅作者可删除的权限控制。
- 显式开启的 MySQL 上传、发布、个人列表与删除权限集成测试。
- JPEG/PNG 封面上传（最大 5 MiB）、预览、默认封面与归属校验。
- MP4 上传：非空、最大 100 MiB、后缀与文件头初步识别、随机文件名和本地保存。
- 本地前端：注册登录、当前用户、上传并发布、本人已上传地址发布、公共及我的视频列表、详情播放与作者删除。
- 关注关系 API：关注、取消关注和关注状态查询；关系有向、重复操作幂等、不能关注自己。视频详情页已有作者关注按钮；关注流 `GET /feed/following` 与视频库“关注”标签页只展示已关注作者的作品。
- 视频详情缓存：`GET /videos/:id` 先查进程内缓存（go-cache，5 秒），再查 Redis（Cache-Aside，可选，未配置 Redis 时直接读 MySQL）；未命中时用 singleflight 合并同一视频的并发回源，不存在的 ID 缓存 30 秒“不存在”标记；删除作品后让缓存失效，发布后清除新 ID 的标记，Redis 故障时降级到 MySQL。

当前已迁移到 Gin 1.11.0 + GORM 1.31.1（MySQL 驱动 1.6.0）。阅读变化见 [迁移指南](docs/learning/02_gin_gorm_migration.md)。视频详情已通过 go-redis 接入 Redis 缓存；RabbitMQ 尚未接入；当前仍为单进程模块化应用。

## Compose 快速启动

Docker Desktop 运行后，在项目根目录执行：

```powershell
./scripts/init-compose-env.ps1
docker compose --env-file .env.compose up -d --build --wait --wait-timeout 180
```

访问 `http://127.0.0.1:19080/`；RabbitMQ 管理页面为 `http://127.0.0.1:15674/`，账号 `gostream`、密码在本机忽略文件 `.env.compose`。初始化脚本不会覆盖已有文件。

这套环境使用独立数据库和上传卷，不会自动导入 Windows MySQL 的旧账号、作品和本机 uploads 文件。普通 `docker compose --env-file .env.compose down` 保留数据；`down -v` 会删除卷。端口、服务名、初始化规则和测试方法见 [Compose 学习说明](docs/learning/12_compose_environment.md)。下面的“环境与启动”是直接在本机运行 Go 的另一种方式。

## 手动清理过期上传

已有数据库先执行一次 `scripts/migrations/004_upload_status.sql`；新数据库使用最新 `scripts/schema.sql`。发布时通过事务锁定视频及可选封面的上传记录，创建视频并将上传状态更新为 `published`。清理只领取已过期、未被视频引用的 `pending` 记录，将其标记为 `deleting` 后删除文件，再删除上传记录。失败保留 `deleting` 以便下次重试。

在项目根目录执行，默认读取 `.env`，只预览，不改变数据库状态或删除文件：

```powershell
go run ./cmd/cleanup-uploads
go run ./cmd/cleanup-uploads --limit=20
```

明确传入 `--execute` 才会真正处理一轮任务：

```powershell
go run ./cmd/cleanup-uploads --execute --limit=100 --timeout=30s
```

保留期限固定为 24 小时。清理命令与 API 共用 `internal/config/config.yaml` 及环境变量覆盖规则，读取同一 DSN 和媒体目录；相对目录按各自工作目录解析，因此均应从项目根目录运行。默认目录为 `uploads/videos`、`uploads/covers`，可通过 YAML 或环境变量修改。`limit` 范围为 1～1000，分别限制重试任务和新增过期候选，一轮最多处理 `2 × limit` 条。优先重试 `deleting`，再处理过期 `pending`。预览不取得清理权，实际执行时仍重新锁定记录、检查状态与引用。用户上传的封面与视频都受保护，系统默认 `/cover.svg` 不参与清理。

单条失败会继续处理其他记录，输出成功、跳过、失败数量及错误；正常结束退出码为 0，运行失败（含部分失败）为 1，参数错误为 2。Ctrl+C 或超时停止后续处理，已经完成的文件删除和事务提交不会撤销。当前没有自动定时任务；已发布文件及删除视频后的文件回收不属于此命令的范围。

命令分支和失败行为通过 `go test ./cmd/cleanup-uploads` 使用替身测试；文件删除测试使用临时目录。MySQL 查询及行锁测试按原有 `GOSTREAM_INTEGRATION_TEST=1` 开关显式执行，不运行真实上传目录的批量删除。

端到端验收测试编译真实命令，分别覆盖 YAML 自定义相对目录（无 `.env`）、环境变量覆盖 YAML/`.env` 的绝对目录、默认目录三种场景。每种场景新建独立的临时 MySQL 数据库和媒体目录，运行预览、`--execute` 和重复执行，验证过期视频及封面被清理，未过期、已发布和被引用的文件保留，`deleting` 任务可恢复（包括文件已不存在），旧目录及被覆盖目录的同名文件不变。它需要本地数据库账号拥有创建、删除测试数据库的权限；结束后删除本次生成的测试数据库，不对 `.env` 指向的项目数据库运行清理命令。

```powershell
$env:GOSTREAM_INTEGRATION_TEST = '1'
try {
    go test ./cmd/cleanup-uploads -run '^TestCleanupCommandE2E$' -count=1 -v
} finally {
    Remove-Item Env:GOSTREAM_INTEGRATION_TEST
}
```

当前版本每轮按 ID 查询前 `limit` 条重试任务，持续失败的记录可能占满该批次；尚未实现重试退避、游标推进和失败任务隔离，也未启用定时调度。

## 环境与启动

当前开发环境为 Windows、Go 1.27.1、MySQL 8.4.9。`go.mod` 声明 Go 1.27.1；尚未验证其他环境组合。MySQL 地址和端口可通过配置修改，本地学习实例使用 `127.0.0.1:13306`。

### 1. 初始化数据库

在仓库根目录打开 PowerShell，登录 MySQL：

```powershell
mysql -h 127.0.0.1 -P 13306 -u root -p
```

进入 MySQL 客户端后执行，绝对路径请根据自己的项目位置调整：

```sql
SOURCE D:/code/Golang/GoStream/scripts/schema.sql;
```

脚本创建 `gostream` 数据库和 `users`、`videos`、`uploads`、`video_likes`、`video_comments` 表，不删除已有数据。`IF NOT EXISTS` 不会更新已有表的结构；它是初始建表脚本，不是数据库迁移系统。应用本身不会自动建表。视频作者外键关联 `users.id`，有视频记录的用户不能直接删除。

### 2. 配置本地环境

在仓库根目录执行一次：

```powershell
Copy-Item .env.example .env
```

如果已有 `.env`，不要重复复制覆盖。编辑 `.env`：

```dotenv
GOSTREAM_DSN="root:YOUR_PASSWORD@tcp(127.0.0.1:13306)/gostream?charset=utf8mb4&parseTime=true&loc=Local"
JWT_SECRET=REPLACE_WITH_A_RANDOM_SECRET_AT_LEAST_32_BYTES
GOSTREAM_FEED_CURSOR_SECRET=REPLACE_WITH_A_DIFFERENT_RANDOM_SECRET_AT_LEAST_32_BYTES
```

可选：设置 `GOSTREAM_REDIS_ADDR`（例如 `127.0.0.1:16379`）和 `GOSTREAM_REDIS_PASSWORD` 启用视频详情缓存；不设置时直接读写 MySQL。Compose 中的 API 已配置为 `redis:6379`，密码取自 `.env.compose`。

将数据库密码和 JWT 密钥占位符替换成自己的配置。JWT 密钥应使用密码学随机源生成，例如在 PowerShell 中执行以下命令，将输出粘贴到 `JWT_SECRET`：

```powershell
$jwtKeyBytes = New-Object byte[] 32
$jwtRng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$jwtRng.GetBytes($jwtKeyBytes)
$jwtRng.Dispose()
[BitConverter]::ToString($jwtKeyBytes).Replace("-", "")
```

`.env` 已通过 `.gitignore` 排除，不提交真实密码、密钥或登录 token。API 和清理命令都允许没有 `.env`，此时需要在进程环境中提供必要配置；文件存在但读取/解析失败仍会报错。`godotenv.Load()` 默认不覆盖进程中已有的同名环境变量。当前共用完整应用配置校验，清理命令也要求两项签名密钥有效，虽然清理业务本身不用 JWT。

两个入口均会读取 `internal/config/config.yaml` 中的非敏感默认配置：

```yaml
http:
  addr: "127.0.0.1:8080"
storage:
  video_dir: "uploads/videos"
  cover_dir: "uploads/covers"
```

`GOSTREAM_HTTP_ADDR`、`GOSTREAM_VIDEO_DIR`、`GOSTREAM_COVER_DIR` 可覆盖上述对应项，也可写入本地 `.env`。最终配置在创建数据库连接前校验；密码和签名密钥只从环境变量读取。更换媒体目录不会自动迁移已有文件，HTTP 访问前缀仍为 `/media/videos`、`/media/covers`。

### 3. 下载依赖并运行

```powershell
go mod download
npm --prefix frontend ci
npm --prefix frontend run build
go run ./cmd/server
```

从仓库根目录启动，以便读取 YAML 和可选的 `.env`。服务默认监听 `127.0.0.1:8080`，实际地址由配置决定，按 Ctrl+C 停止。`OpenGORM` 会检查数据库连接，但连接成功不代表表已存在。

### 4. 打开前端工作台

浏览器访问 `http://127.0.0.1:8080/`。本机首次需要 Node.js 24 执行前端构建，Gin 提供 web/dist 中的静态产物；运行时不需要 Node。Compose 使用 Dockerfile 自动构建。前端开发热更新、测试和目录说明见 [frontend/README.md](frontend/README.md)。

- 访客可浏览分页列表、按 ID 查看详情、播放可用的视频。
- 点击“登录 / 注册”创建账号或登录；注册成功后尝试自动登录。token 仅在当前页面内存中保存，刷新或退出后需要重新登录。“退出”不代表服务端撤销已有 token。
- 点击“发布视频”，选择小于等于 100 MiB 的 MP4，填写标题。页面先上传文件，再发布元数据；发布失败时可在同一登录会话中重试，复用已上传地址。
- 也可以切换到“使用已上传地址”，填写本人上传接口返回的 `/media/videos/…` 地址。可以选择不超过 5 MiB 的 JPEG/PNG 封面，预览或移除；不选则使用 `/cover.svg`。自动抽帧尚未实现。
- 详情中仅向当前作者显示删除按钮，删除前二次确认；最终权限仍由后端判断。删除只移除数据库记录，当前不删除文件。上传后取消发布也可能留下文件。
- 公共视频列表、详情和媒体文件是公开访问的；“我的视频”列表需要登录，未发布文件只要知道地址也可访问。页面不显示或持久化 token。

浏览器与 API 使用同一来源。前端源码在 `frontend/src`，通过 Vite 构建到 `web/dist`，由 `httpx.RegisterWeb` 提供。页面使用 Hash 路由，例如 `/#/mine`、`/#/publish`、`/#/videos/1`；后端 JSON API 仍为 `/me/videos`、`/videos`、`/videos/1`。旧 web 页面保留作学习对照，不再由当前路由或镜像提供。没有部署到云端。

## HTTP 接口

基础地址：`http://127.0.0.1:8080`。除两个文件上传接口使用 `multipart/form-data` 外，POST 请求使用 `Content-Type: application/json`，字段放在 JSON 请求体中，不放在 URL 的 Params 中。

| 方法 | 路径 | 身份验证 | 成功结果 |
| --- | --- | --- | --- |
| POST | `/users` | 无 | 201，用户 ID 和用户名 |
| POST | `/login` | 用户名和密码 | 200，用户 ID、用户名和 token |
| GET | `/me` | Bearer token | 200，当前用户 ID 和用户名 |
| POST | `/videos` | Bearer token | 201，视频 ID、作者 ID 和标题 |
| POST | `/videos/:id/comments` | Bearer token | 201，新评论 ID、视频 ID、评论者 ID 和内容 |
| GET | `/videos/:id/comments` | 无 | 200，视频 ID、分页参数和评论数组 |
| DELETE | `/comments/:id` | Bearer token | 204，无响应体，仅评论作者可删除 |
| GET | `/videos/:id` | 无 | 200，视频元数据 |
| GET | `/videos` | 无 | 200，分页参数及视频数组 |
| GET | `/feed` | 可选 Bearer token；提供时须有效 | 200，最新视频及作者/点赞信息、字符串游标和 `has_more` |
| GET | `/feed/following` | Bearer token | 200，仅含已关注作者的作品，带作者/点赞信息、关注流游标和 `has_more` |
| GET | `/me/videos` | Bearer token | 200，仅当前用户的视频及分页参数 |
| DELETE | `/videos/:id` | Bearer token | 204，无响应体，仅作者可删除 |
| GET | `/videos/:id/like` | Bearer token | 200，`like_count` 和当前用户的 `liked` |
| PUT | `/videos/:id/like` | Bearer token | 204，无响应体；重复点赞仍成功 |
| DELETE | `/videos/:id/like` | Bearer token | 204，无响应体；重复取消仍成功 |
| GET | `/users/:id/follow` | Bearer token | 200，`following`：当前用户是否关注目标用户 |
| PUT | `/users/:id/follow` | Bearer token | 204，无响应体；重复关注仍成功，不能关注自己 |
| DELETE | `/users/:id/follow` | Bearer token | 204，无响应体；重复取消仍成功 |
| POST | `/uploads/videos` | Bearer token | 200，原文件名、随机文件名、大小及 `play_url` |
| POST | `/uploads/covers` | Bearer token | 200，原文件名、随机文件名、大小及 `cover_url` |
| GET / HEAD | `/media/covers/:filename` | 不需要 | 封面图片内容或头信息 |
| GET/HEAD | `/media/videos/:filename` | 无 | 文件内容或元数据，支持 Range 分段读取 |

### 注册

```json
{"username":"alice","password":"example-password-123"}
```

用户名不能为空，密码长度为 8～72 字节。数据库用户名列长度为 32 个字符，但当前 handler 尚未主动检查用户名长度；超过表约束可能返回 500。用户名是否重复由 MySQL 唯一约束判断。成功示例（ID 由数据库分配）：

```json
{"id":1,"username":"alice"}
```

参数错误返回 400；重复用户名返回 409 和 `{"error":"用户名已经存在"}`；其他存储或内部处理错误返回 500。

### 登录

发送与注册相同形状的 JSON，使用已注册的账号密码。成功响应：

```json
{"id":1,"username":"alice","token":"<server-issued-token>"}
```

JSON 格式错误或缺少用户名/密码返回 400。用户不存在或密码校验失败返回 401 和 `{"error":"用户名或密码错误"}`。数据库查询或 token 签发失败返回 500。

### 当前用户

请求头：

```http
Authorization: Bearer <server-issued-token>
```

不需要请求体，也不接受客户端提供的用户 ID 来决定查询谁。中间件验证 token 后取出用户 ID，handler 根据该 ID 查询 MySQL。

Postman 中选择 **Authorization → Bearer Token**，只粘贴 token，不添加双引号或 `Bearer ` 前缀。不要选择需要填写 Secret 的 **JWT Bearer**，那是由 Postman 生成 JWT 的功能。

token 缺失、格式不正确、签名校验失败或过期返回 401；数据库中的当前用户已不存在也返回 401；查询失败或请求内部身份信息异常返回 500。成功只返回 ID 和用户名，不返回密码或哈希。

### 视频元数据

发布使用 JSON：

```json
{"title":"示例视频","play_url":"/media/videos/<上传返回的文件名>.mp4","cover_url":"/cover.svg"}
```

作者 ID 从已验证的 token 获取。标题和地址去除首尾空白后不能为空，标题最多 100 个 Unicode 字符，两项地址分别最多 1024 个字符。视频地址必须使用本站上传接口返回的 `/media/videos/<文件名>` 路径。后端查询 `uploads`，同时匹配文件名、当前用户 ID 和 `file_type = video`；上传记录不存在、属于其他人或类型不匹配时返回 403。外部地址和异常路径返回 400，无有效 token 返回 401，数据库异常返回 500。文件内容是否完整可播放仍不由该校验保证。

详情示例：`GET /videos/1`。ID 非法或为 0 返回 400，不存在返回 404，数据库错误返回 500。成功返回 `id`、`author_id`、`title`、`play_url`、`cover_url`、`created_at`。

最新视频流：`GET /feed?limit=10`；继续加载时将响应的 `next_cursor` 原样作为 `cursor` 查询参数传回（通过 URL 参数工具编码）。按 `created_at DESC, id DESC` 排序；limit 不传或为 0 时默认 10，大于 50 时按 50 处理，负数或格式错误返回 400。响应包含 `videos`、`next_cursor`、`has_more`，没有更多数据时游标为空，空列表为 `[]`。非法游标返回 400 和 `INVALID_CURSOR`；数据库错误返回 500 和 `INTERNAL_ERROR`。游客可访问。刷新时清除游标；分页位置不代表用户实际观看进度。

Feed 游标签名使用独立的 `GOSTREAM_FEED_CURSOR_SECRET`，至少 32 字节，采用密码学随机源生成，与 JWT 密钥分开；部署多个实例时保持一致。密钥变化会使已有游标失效。公共页面已接入 Feed，“加载更多”追加结果，“刷新”和更改每批条数重新加载第一批；发布成功回到公共流刷新。“我的视频”仍使用独立页码分页。失败保留已有公共视频，游标失效时提示刷新，旧请求结果不能覆盖新列表。组合索引迁移和隔离执行计划验证见 [索引记录](docs/learning/05_feed_index_verification.md)。

Feed 每条视频新增 `author_name`、`like_count`、`liked`，分别表示作者名、总点赞数和当前用户是否点赞。游客可查看作者和总数，`liked=false`；携带 token 时必须验证，错误凭证返回 401。非空批次共 3 次数据库读取：视频、批量作者、批量点赞聚合；空批次仅查询视频。个人状态不允许进入共享 HTTP 缓存。前端登录/退出时刷新列表，卡片展示作者、计数及已点赞标记。实现和测试证据见 [批量补全说明](docs/learning/06_feed_metadata.md)。

`node scripts/web_feed_test.cjs` 使用模拟 DOM/API 验证游标传递、刷新、追加、失败重试、模式切换、退出登录和请求竞态；`node scripts/web_publish_test.cjs` 同时验证发布后刷新公共流。它们不替代实际浏览器的布局与播放验收。

列表示例：`GET /videos?page=1&page_size=5`。默认页码 1、每页 10 条；页码范围 1～10000，每页范围 1～100，非法参数返回 400。偏移量为 `(page-1)*page_size`。响应包含 `page`、`page_size`、`videos`；没有记录时 `videos` 为 `[]`。列表按 `id DESC` 排序；并发新增或删除时，OFFSET 分页仍可能发生偏移。

删除示例：`DELETE /videos/1`。SQL 同时匹配视频 ID 和当前用户 ID。ID 非法返回 400，无有效 token 返回 401，没有匹配到属于自己的记录返回 404，删除成功返回 204 且无响应体，数据库错误返回 500。当前只删除元数据，不涉及文件清理。

### 上传与文件访问

`POST /uploads/videos` 使用 multipart/form-data，文件字段名必须是 `file`。缺少文件/空文件返回 400，文件超过 100 MiB 返回 413，非 MP4 后缀或文件头未识别为 `video/mp4` 返回 415，无有效登录返回 401，存储失败返回 500。文件头识别不保证视频完整或可播放。服务器另将整个请求体限制为 101 MiB，超限请求可能在 handler 之前被框架拒绝。

成功响应包含 `filename`、`saved_name`、`size`（字节）和 `play_url`（`/media/videos/<随机文件名>.mp4`）。磁盘目录为 `uploads/videos/`，不纳入 Git。文件保存使用随机文件名并拒绝覆盖已有文件，保存失败时尝试清理不完整文件。保存成功后，写入 `uploads(user_id, saved_name, file_type)`，视频类型为 `video`；上传记录写入失败时返回 500，并尝试删除本次保存的文件。

媒体路由显式剥离 `/media/videos` 两段路径后访问该目录，不生成目录索引。普通 GET 返回完整文件，HEAD 返回头信息；有效字节范围请求返回 206，不存在文件返回 404。文件访问不要求登录。

## 测试与验证范围

```powershell
go test -v ./...
```

默认自动测试覆盖：正确/错误密码、正确/错误 JWT 密钥、过期 token、无 token 时拦截后续处理、有效 token 时放行并传递用户 ID。默认跳过数据库集成测试，不启动监听端口，不需要 `.env` 或 MySQL。

上传与保存测试使用自动清理的临时目录，覆盖 14 个上传场景、落盘字节一致性、随机命名、公开 GET/HEAD、Range 及不存在文件。识别测试使用最小 MP4 文件头样本，不等同于视频解码测试。可单独运行 `go test -v ./internal/video -run '^(TestUpload|TestSaveVideo)$'`。

注册、重复注册、登录、`/me` 及视频发布、详情、列表已通过本地 Postman 手动验证；未穷尽 HTTP 参数和 token 的全部边界情况。

删除权限集成测试需要 `.env` 和已建表的 MySQL，在 PowerShell 中显式开启：

```powershell
$env:GOSTREAM_INTEGRATION_TEST = '1'
try {
    go test -v ./internal/video -run '^TestDeleteIntegration$' -count=1
} finally {
    Remove-Item Env:GOSTREAM_INTEGRATION_TEST -ErrorAction SilentlyContinue
}
```

测试在进程内构造 HTTP 请求，连接真实 MySQL，创建两个临时账号和测试视频，结束时按本次生成的唯一用户名清理。它会改变自增计数，不是完全只读的测试。联调场景覆盖评论删除及评论作者/视频作者区分、评论分页、空数组及视频隔离、发表评论、字符边界、身份防伪和评论级联清理、取消点赞及双账号、双视频隔离、点赞及联合主键去重、删除视频级联清理点赞、双账号个人列表隔离、公共列表、封面上传及类型记录、默认及本人封面发布、封面归属和地址校验、发布权限、本站路径校验、文件类型限制，以及缺少/无效 token、非法/零 ID、他人不能删除、越权尝试后记录保留、作者删除、删除后查询和重复删除。本人上传及发布也在准备测试数据时验证；数据库查询失败通过独立测试模拟。

关注关系、关注流与视频详情缓存的 MySQL 测试不读取 `.env`（缓存测试使用进程内的 miniredis，不需要真实 Redis），只连接 `GOSTREAM_TEST_MYSQL_DSN` 指向的一次性 MySQL，在自建的随机库中运行并在结束时删除该库。例如先用 Docker 启动临时库，等待约 10～20 秒就绪后执行：

```powershell
docker run -d --rm --name gostream-test-mysql -p 127.0.0.1:13399:3306 -e MYSQL_ALLOW_EMPTY_PASSWORD=yes mysql:8.4.9
$env:GOSTREAM_TEST_MYSQL_DSN = 'root@tcp(127.0.0.1:13399)/'
try {
    go test -v ./internal/follow ./internal/feed ./cmd/server -run '^(TestGORMRepositoryMySQLIntegration|TestFollowHTTPMySQLIntegration|TestListFollowingMySQLIntegration|TestFollowingFeedHTTPMySQLIntegration|TestVideoDetailCacheHTTPIntegration)$' -count=1
} finally {
    Remove-Item Env:GOSTREAM_TEST_MYSQL_DSN -ErrorAction SilentlyContinue
    docker rm -f gostream-test-mysql
}
```

当前环境可能打印 Sonic 不支持该 Go 版本并回退到 `encoding/json` 的警告；已观察到现有测试可通过，不表示已完成依赖兼容性验证。

## 文件导航

| 文件 | 用途 |
| --- | --- |
| `cmd/server/main.go` | 加载配置、装配数据库和 handler、注册路由、启动 Gin |
| `internal/user/handler.go` | 用户接口参数解析、HTTP 响应与登录凭证签发 |
| `internal/user/service.go`、`repository.go`、`errors.go` | 用户业务规则、GORM 存储及可识别错误 |
| `internal/user/model.go` | 用户模型 |
| `internal/user/password.go` | bcrypt 哈希生成和校验 |
| `internal/database/mysql.go`、`gorm.go` | 创建 MySQL 连接池、检查连接并初始化 GORM |
| `internal/httpx/http.go` | 完整请求体限制、multipart 临时文件清理和公开媒体访问 |
| `internal/auth/jwt.go` | JWT 签发和验证 |
| `internal/auth/middleware.go` | Bearer token 鉴权中间件 |
| `internal/auth/*_test.go`、`internal/user/*_test.go` | 基础单元测试 |
| `internal/video/handler.go`、`model.go` | 视频元数据接口和模型 |
| `internal/video/delete_integration_test.go` | 连接 MySQL 的删除权限集成测试 |
| `internal/video/storage.go`、`storage_test.go` | 文件保存和测试 |
| `internal/video/upload_test.go` | 上传与公开文件读取测试 |
| `internal/follow/` | 关注关系的 Handler、Service、Repository 及测试 |
| `internal/video/video_repository.go`、`video_cache.go`、`video_local_cache.go`、`video_service.go` | 视频详情与删除的 MySQL 访问、Redis 缓存、进程内缓存以及 Cache-Aside 与 singleflight 规则 |
| `frontend/` | Vue/TS 源码、状态、路由、Vite 构建和前端测试 |
| `web/dist/` | 可重建的前端产物，Git 忽略；旧 web 文件仅作迁移对照 |
| `internal/httpx/web.go` | 只提供构建产物，未知 API 保持 404 |
| `.github/workflows/ci.yml` | Go 检查/race、前端检查/构建和浏览器夹具回归 |
| `scripts/schema.sql` | 数据库初始建表脚本 |

目录按业务和职责分包，HTTP 服务仍是一个进程，另有独立的手动清理命令。启动入口依赖 `database`、`auth`、`user`、`video`、`feed`、`follow`，`user` 依赖 `auth`；鉴权包不依赖用户业务包。`internal` 限制这些包供本项目内部使用。评论、点赞和上传清理使用 service/repository 分层；用户注册、登录和当前用户查询、关注关系也已完成 Handler → Service → Repository 分层；视频元数据与发布等部分 GORM 数据访问仍在 Handler 中。

## 下一阶段

M2 关注关系与关注流已完成。M3 进行中：视频详情的 Cache-Aside、本地缓存、singleflight 与空值缓存已完成，接下来是时间线缓存、限流和认证增强，随后是 MQ/Worker 通知。当前没有 token 刷新、主动撤销、登录限流、分片续传；按主线分阶段实现，见项目计划。基础 CI 在每次 push/PR 时由 GitHub Actions 运行（首次运行已通过）；它不包含需要真实 MySQL 或 Docker 的集成测试。

### 上传表迁移

新环境使用 `scripts/schema.sql` 创建 `uploads` 表。已有 `video_uploads` 表的环境，在暂停服务后，对当前项目数据库执行一次 `scripts/migrations/001_generalize_uploads.sql`，随后启动新版服务。不要在已经迁移的数据库重复执行该迁移，也不要用初始建表脚本代替迁移。

迁移将 `video_uploads` 改名为 `uploads`，增加 `file_type VARCHAR(16) NOT NULL DEFAULT 'video'`；已有上传记录标记为 `video`，原有视频记录和磁盘文件不变。业务代码明确写入类型，封面上传写入 `cover`。MySQL DDL 隐式提交，不能通过普通事务回滚。已提供封面上传接口及发布时的封面归属校验，前端已支持选择、预览、移除封面，并在发布前上传图片。

### 封面上传

`POST /uploads/covers` 要求 Bearer token，使用 multipart/form-data，字段名为 `file`。支持 `.jpg`、`.jpeg`、`.png`（后缀不区分大小写），最大 5 MiB。JPEG 统一保存为 `.jpg`。通过 `image.DecodeConfig` 检查图片头格式和正尺寸，并要求与后缀匹配；不完整解码图片，也不保证所有像素数据完整或可显示。

缺少文件或空文件返回 400；超限返回 413；不支持的后缀、无效图片头或内容与后缀不匹配返回 415；无有效登录返回 401；文件保存或上传记录写入失败返回 500。数据库写入失败时尝试删除本次封面文件。

成功返回 `filename`、`saved_name`、`size` 和 `cover_url`（`/media/covers/<随机文件名>.jpg` 或 `.png`）。封面保存到 `uploads/covers/`，公开访问，不生成目录列表；数据库记录到 `uploads`，类型为 `cover`。前端发布窗口已接入封面选择及预览；发布时依次上传视频、上传可选封面、提交视频元数据。同一登录会话内失败重试会复用已上传结果；更换封面清除其缓存，切换登录清除两类上传缓存。发布时仅允许系统默认封面 `/cover.svg`，或 `/media/covers/<文件名>`：上传封面必须在 `uploads` 中同时匹配文件名、当前用户及 `file_type = cover`。没有匹配记录返回 403；外部地址、子目录、编码路径及查询或片段后缀返回 400；归属查询异常返回 500。拒绝发布不会删除文件和上传记录，已发布视频的查询和播放保持原样。

`go test -v ./internal/video -run '^TestUploadCover$'` 使用测试数据库替身，覆盖 17 个上传场景及静态图片访问、原始字节保存、失败清理；真实 MySQL 的封面上传记录验证包含在 `TestDeleteIntegration` 中。

当前 Vue 发布测试见 `frontend/tests`，通过 `npm --prefix frontend test` 和构建后的 `npm --prefix frontend run test:e2e` 运行。原 `scripts/web_publish_test.cjs` 仅用于迁移前页面，不能证明 Vue 正确。

### 本阶段页面验收

Vue 迁移已完成 Chromium 响应夹具回归和隔离 Compose 中的真实双账号验收，覆盖上传及封面、发布、MP4 解码播放、Range、Feed/个人分页、点赞评论和跨账号权限。详见 [迁移与验收记录](docs/learning/13_vue_migration.md)，不宣称所有浏览器兼容或所有编码格式可播放。

### 点赞

`PUT /videos/:id/like` 无需请求体，用户 ID 从 JWT 上下文获取。首次或重复点赞均返回 204；非法或为零的 ID 返回 400，视频不存在返回 404，无有效 token 返回 401，数据库异常返回 500。`video_likes` 使用 `(user_id, video_id)` 联合主键防止重复，删除视频时通过外键级联清理点赞。新环境使用初始建表脚本；旧环境可执行 `scripts/migrations/002_video_likes.sql`。点赞、取消及查询已接入详情窗口。

`DELETE /videos/:id/like` 只删除当前用户对指定视频的点赞，不需要请求体。首次和重复取消均返回 204；格式正确但不存在的视频 ID 同样返回 204；非法、零或负数 ID 返回 400；无有效 token 返回 401；数据库异常返回 500。双账号、双视频的真实 MySQL 测试确认，取消不会影响其他用户或当前用户对其他视频的点赞。

### 点赞状态与实际 HTTP 验证

`GET /videos/:id/like` 查询而不修改点赞：返回 `like_count`（这条视频的总点赞数）和 `liked`（当前登录用户是否点赞）。两个账号的总数相同，个人状态可以不同。视频不存在返回 404，非法或零 ID 返回 400，无有效 token 返回 401，数据库异常返回 500。GET 必须绑定 `GetLikeStatus`，不要绑定会写入记录的 `Like`。

已启动本地后端后，可在 PowerShell 设置 `$env:GOSTREAM_HTTP_INTEGRATION_TEST = '1'`，执行 `go test -v ./internal/video -run '^TestLikeHTTPIntegration$' -count=1`，最后移除该环境变量。该测试读取 `.env`，针对 `http://127.0.0.1:8080` 发出真实 HTTP 请求，创建两个临时账号及视频并生成测试 token，不走登录流程，结束时清理；会改变自增计数。9 个场景验证查询无副作用、两个账号的总数与个人状态、重复点赞、取消及重复取消、鉴权和非法/不存在 ID。默认 `go test ./...` 跳过此测试。

### 前端点赞交互

打开视频详情后，登录用户会查询 `GET /videos/:id/like` 并显示数量和个人状态。未点赞时点击发送 PUT，已点赞时发送 DELETE，成功后重新 GET，以后端结果更新显示。查询失败提供重试，操作失败要求重新查询后再操作，不直接在前端增减数量。请求处理中禁用重复操作，关闭详情、切换视频或账号后忽略旧响应。访客显示登录提示，当前接口要求鉴权，因此访客不查询点赞数量。状态不会自动轮询，可点击“刷新点赞”获取其他用户操作后的结果。

运行 `node scripts/web_likes_test.cjs` 可验证模拟 DOM/API 下的完整交互、重复点击、失败重试、登录失效及旧响应隔离；不包含真实浏览器渲染验收。

### 发表评论

`POST /videos/:id/comments` 要求 Bearer token 和 JSON 请求体，如 `{"content":"拍得不错！"}`。用户 ID 来自中间件，视频 ID 来自 URL。内容去除首尾空白后必须为 1～500 个 Unicode 字符；非法 ID、JSON 或内容返回 400，未登录返回 401，视频不存在返回 404，数据库异常返回 500。成功返回 201，包含新评论的 `id`、`video_id`、`user_id`、`content`。相同用户可重复评论，每次生成独立 ID；删除视频时评论通过外键级联清理。

初始建表脚本包含评论表；已有环境可执行 `scripts/migrations/003_video_comments.sql`。已有表不会被重复创建。默认测试检查参数、JSON、鉴权及模拟数据库异常；真实 MySQL 联调检查存储内容、独立评论 ID、500/501 中文字符边界、忽略请求体伪造身份及视频删除级联。评论列表及作者删除已实现；前端详情窗口已接入评论分页、发表和作者删除。

`GET /videos/:id/comments?page=1&page_size=10` 允许访客访问。默认第 1 页、每页 10 条，page 范围 1～10000，page_size 范围 1～100，按评论 ID 降序排列并仅查询指定视频。成功返回 `video_id`、`page`、`page_size`、`comments`；视频存在但没有评论或页码超过最后一页时返回 200 和 `[]`，视频不存在返回 404，非法参数返回 400，数据库异常返回 500。真实 MySQL 测试覆盖分页无重叠、末页不足、跨视频隔离、内容与时间字段和访客访问。

`DELETE /comments/:id` 中的 ID 是评论 ID。仅允许评论作者删除，通过 SQL 同时匹配评论 ID 和 JWT 用户 ID。成功返回 204，无响应体；不存在、不属于自己或重复删除返回 404；非法、零、负数 ID 返回 400；无有效 token 返回 401；数据库异常返回 500。视频作者不能以视频所有者身份单独删除其他用户的评论；删除整个视频时评论仍会级联清理。真实 MySQL 测试使用两个账号互相评论对方的视频，确认越权请求保留评论、作者可删除、其他评论不受影响。

### 前端评论区

详情页提供每页 5 条的公开评论列表、上一页/下一页和刷新。登录后可提交 1～500 个 Unicode 字符的评论，成功后回到第一页；失败保留输入。网络异常时先刷新确认结果再手动重试，当前没有评论提交的请求幂等键。仅对当前评论作者显示删除按钮，确认后调用后端权限校验；删掉末页唯一评论时回到上一页。评论通过 Vue 文本插值输出，不作为 HTML 解析。切换视频、离开详情或切换账号会使旧请求失效。

评论与点赞的当前前端回归位于 `frontend/tests/e2e`；真实浏览器测试由隔离 Compose 入口执行，见迁移文档。旧 `scripts/web_comments_test.cjs` 等保留为历史测试，不作为 Vue 验收证据。

### 评论与点赞的分层示例

发表评论、评论列表、删除评论，以及点赞、取消点赞、点赞状态查询均已分层。文件仍处于 `internal/video` 包，不引入额外框架。

| 文件 | 职责 |
| --- | --- |
| `comment.go` | 评论接口的身份、URL、JSON 与分页解析，将业务结果映射成 HTTP 响应 |
| `comment_service.go` | 校验评论内容，将删除未命中转为业务错误，通过存储接口创建、查询和删除评论 |
| `comment_repository.go` | 评论 SQL，视频存在性检查、分页、扫描结果、受影响行数及自增 ID |
| `like.go` | 点赞接口的身份和视频 ID 解析，以及 HTTP 响应 |
| `like_service.go` | 将重复点赞视为成功，通过存储接口执行点赞、取消和状态查询 |
| `like_repository.go` | 点赞 SQL，将 MySQL 唯一键冲突转换为业务可识别的错误 |

请求流向为 Handler → CommentService → CommentRepository → MySQL。Service 只依赖一个小的 Go interface，不依赖 Gin 或数据库连接。`cmd/server/main.go` 依次创建 SQLCommentRepository、CommentService、Handler，并把前一个对象交给后一个对象；这是依赖传递，不是发起业务请求。Handler 暂时仍持有 DB，供尚未分层的其他处理函数使用。

Service 的错误不包含 HTTP 状态码；Handler 将内容无效映射为 400、视频不存在映射为 404、存储异常映射为 500。原有请求、状态码、成功响应字段和安全错误提示保持不变。测试中的 Handler 也必须明确配置 CommentService。

`go test -v ./internal/video -run '^TestCommentServiceCreateComment$'` 不启动 HTTP、不访问 MySQL，8 个子场景通过仓储替身验证非法内容不触发存储、内容规范化、中文及 emoji 边界、错误及 context 传递。其他 Service 测试验证删除、点赞及查询行为，真实 MySQL 联调继续验证整条 HTTP → Service → Repository 链路。

### 关注关系

`internal/follow` 按 Handler → Service → Repository 分层。操作人只来自 JWT，目标用户来自路径 `:id`；请求体中的身份字段一律忽略。

- `PUT /users/:id/follow`：关注。首次和重复关注都返回 204；关注自己返回 400，目标用户不存在返回 404。
- `DELETE /users/:id/follow`：取消关注。本来没关注、目标不存在或目标是自己都返回 204，表示当前已不再关注。
- `GET /users/:id/follow`：返回 `{"following": true|false}`，带 `Cache-Control: private, no-store` 和 `Vary: Authorization`；目标用户不存在返回 404。
- 三个接口都要求 Bearer token，缺失或无效返回 401；非法、零、负数或溢出的 ID 返回 400；数据库异常返回 500，不回显内部错误。

`follows(follower_id, followee_id, created_at)` 以 `(follower_id, followee_id)` 为主键防止重复关注，`idx_follows_followee_id` 支持反向查询，两个外键指向 `users(id)`，`CHECK (follower_id <> followee_id)` 在数据库层兜底禁止自关注。新库由 `scripts/schema.sql` 创建；已有库执行一次 `scripts/migrations/006_follows.sql`。

默认测试用假驱动和仓储替身覆盖 SQL、参数顺序、错误翻译、业务规则和 25 个 HTTP 场景。设置 `GOSTREAM_TEST_MYSQL_DSN` 后，另运行 Repository 集成测试和端到端 HTTP 测试（注册登录、关注、幂等、拒绝情况、跨账号隔离、取消），运行方式见“测试与验证范围”。

详情页在作者信息下方提供关注按钮。游客点击先打开登录窗口，不发送请求；登录后用 GET 读取状态，点击发送 PUT 或 DELETE，成功后重新 GET，以后端结果更新按钮；作者本人看不到按钮；读取失败时禁用按钮并提供“重新读取关注状态”。切换视频或账号后忽略旧响应。Chromium 夹具测试覆盖这些交互和请求顺序；隔离 Compose 的真实浏览器测试验证双账号关注、返回页面后状态保持、取消关注和作者本人不显示按钮。

`GET /feed/following` 要求 Bearer token，只返回当前用户关注的作者的作品；排序、limit+1 和批量作者/点赞补全与最新流相同。游标在签名内记录流类型，最新流游标不能续读关注流，反之亦然，误用返回 400 `INVALID_CURSOR`；游标不含身份，关注者始终来自 JWT。续读按 (created_at, id) 位置进行，没有快照：翻页期间新关注的作者的旧作品可能出现在后续页，已取关作者的剩余作品不再出现。视频库的“关注”标签页需要登录，使用独立的游标，切换标签或账号时重新加载。Go 单元测试覆盖游标类型绑定、分页与错误映射；设置 `GOSTREAM_TEST_MYSQL_DSN` 后，集成和端到端测试验证筛选、同一时间边界不越界、有向关系、取关后不可见和游标不能混用；隔离 Compose 真实浏览器测试验证关注页分页、自己的作品不进入关注流以及取关后关注页为空。

### 视频详情缓存

`GET /videos/:id` 由 `VideoService` 按 Cache-Aside 读取：先查 Redis 键 `gostream:video:v1:{id}`，命中直接返回；未命中再查 MySQL，并把公开字段（id、作者、标题、播放和封面地址、发布时间）写回 Redis，过期时间为 10 分钟加 0～60 秒随机值，避免大量键同时过期。点赞数和“我是否点赞”等个人或易变数据不进入缓存。`DELETE /videos/:id` 先删数据库记录，成功后再删除缓存；删除缓存失败时只记日志，旧数据最多保留到过期（不超过 11 分钟），这是明确的失效窗口，不是强一致。Redis 读写失败一律记日志并降级到 MySQL，详情页不因缓存故障不可用；Redis 客户端使用短超时，命令和建连都不重试，就绪检查不包含 Redis。go-redis 默认建连失败会再试 5 次、每次间隔 100ms，而 `DialerRetries` 小于等于 0 都按默认处理，所以必须显式设为 1：实测 Redis 拒绝连接时，默认配置下一次缓存读取要约 400ms 才失败，设为 1 后不到 10ms。

Redis 前面还有一层进程内缓存 `LocalVideoCache`（go-cache，5 秒）。它同样实现 `VideoCache` 接口并包在 Redis 外面，Service 不知道缓存有两层；本地命中时不访问 Redis。代价是每个进程各有一份本地缓存，互不通知：某个实例删除或发布作品后，其他实例最多 5 秒内仍可能返回旧结果。

两层都未命中时，`singleflight` 按视频 ID 合并同一进程内的并发回源（防缓存击穿）：同一时刻只有一次 MySQL 查询，其余请求等待共享结果。共享查询用 `context.WithoutCancel` 保留请求上下文里的值，但不随第一个调用方取消，另有 3 秒超时；每个调用方仍可自行放弃等待。合并只在单个进程内有效，N 个实例最多同时查 N 次。

MySQL 确认视频不存在时，两层缓存都记一个“不存在”标记（Redis 中的值为 `missing`，30 秒；本地为 nil 指针），重复访问同一个不存在的 ID 不再查库（防缓存穿透）；数据库报错时不写标记。`MissingTTL` 未配置时不写标记，因为 Redis 中过期时间为 0 表示永不过期。发布作品的事务提交后会清除新 ID 的缓存，避免发布前被访问过的 ID 在发布后仍返回 404；清除必须在提交之后，否则并发读取可能把标记写回。

已知窗口：读请求刚从 MySQL 读到旧数据，随后作品被删除并清除缓存，读请求再把旧数据写回时，旧数据最多保留到 Redis 过期；某次访问查完 MySQL、尚未写标记时恰好发布了这个 ID，标记最多多挡 30 秒。两者概率都很低，由 TTL 兜底。

响应字段和状态码与接入缓存前一致。默认测试覆盖 Cache-Aside 命中与未命中、缓存读写失败降级、先删库后删缓存、Repository 的 SQL 与错误翻译（数据库故障不会被说成“不存在”）、基于 miniredis 的键、过期时间、抖动与“不存在”标记、本地缓存的命中、过期与跨实例陈旧、发布在事务提交后才清标记，以及 Redis 拒绝连接时客户端立即失败。并发测试使用 `testing/synctest`：20 个并发未命中只查一次库，第一个调用方取消后其余调用方仍拿到结果。`TestVideoDetailCacheHTTPIntegration` 用两个路由模拟两个 API 实例，共享 MySQL 和 miniredis，验证本地命中既不查库也不访问 Redis、另一个实例从 Redis 命中、删除后失效并写入“不存在”标记、Redis 停机降级与恢复后回填、发布后新作品立即可见。没有进行压测，不给出命中率或性能提升数值。
