# GoStream

一个用于学习 Go 后端开发的短视频后端项目，逐个模块实现和验证。当前完成用户认证、视频元数据管理、视频及封面本地上传、文件归属校验与公开文件访问，并提供原生 HTML/CSS/JavaScript 工作台。

## 当前功能

- Hertz HTTP 服务，单进程运行。
- MySQL 用户存储，用户名唯一约束。
- bcrypt 密码哈希与校验，不存储明文密码。
- HS256 JWT，包含用户 ID、签发时间和两小时有效期。
- Bearer token 鉴权中间件。
- 密码、JWT 和鉴权中间件的基础单元测试。
- 视频元数据存储、公开查询、分页查询和仅作者可删除的权限控制。
- 显式开启的 MySQL 上传、发布、个人列表与删除权限集成测试。
- JPEG/PNG 封面上传（最大 5 MiB）、预览、默认封面与归属校验。
- MP4 上传：非空、最大 100 MiB、后缀与文件头初步识别、随机文件名和本地保存。
- 本地前端：注册登录、当前用户、上传并发布、本人已上传地址发布、公共及我的视频列表、详情播放与作者删除。

当前未引入 Redis、消息队列或微服务。

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

脚本创建 `gostream` 数据库和 `users`、`videos`、`uploads` 表，不删除已有数据。`IF NOT EXISTS` 不会更新已有表的结构；它是初始建表脚本，不是数据库迁移系统。应用本身不会自动建表。视频作者外键关联 `users.id`，有视频记录的用户不能直接删除。

### 2. 配置本地环境

在仓库根目录执行一次：

```powershell
Copy-Item .env.example .env
```

如果已有 `.env`，不要重复复制覆盖。编辑 `.env`：

```dotenv
GOSTREAM_DSN="root:YOUR_PASSWORD@tcp(127.0.0.1:13306)/gostream?charset=utf8mb4&parseTime=true&loc=Local"
JWT_SECRET=REPLACE_WITH_A_RANDOM_SECRET_AT_LEAST_32_BYTES
```

将数据库密码和 JWT 密钥占位符替换成自己的配置。JWT 密钥应使用密码学随机源生成，例如在 PowerShell 中执行以下命令，将输出粘贴到 `JWT_SECRET`：

```powershell
$jwtKeyBytes = New-Object byte[] 32
$jwtRng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$jwtRng.GetBytes($jwtKeyBytes)
$jwtRng.Dispose()
[BitConverter]::ToString($jwtKeyBytes).Replace("-", "")
```

`.env` 已通过 `.gitignore` 排除，不提交真实密码、密钥或登录 token。应用当前要求 `.env` 文件存在；`godotenv.Load()` 默认不覆盖进程中已有的同名环境变量。

### 3. 下载依赖并运行

```powershell
go mod download
go run ./cmd/server
```

从仓库根目录启动，以便读取 `.env`。服务监听 `127.0.0.1:8080`，按 Ctrl+C 停止。`OpenDB` 会检查数据库连接，但连接成功不代表表已存在。

### 4. 打开前端工作台

浏览器访问 `http://127.0.0.1:8080/`，无需 Node.js、npm 或前端构建。新增前端静态路由后需先重启 Go 服务；页面资源更新后可用 Ctrl+F5 强制刷新。

- 访客可浏览分页列表、按 ID 查看详情、播放可用的视频。
- 点击“登录 / 注册”创建账号或登录；注册成功后尝试自动登录。token 仅在当前页面内存中保存，刷新或退出后需要重新登录。“退出”不代表服务端撤销已有 token。
- 点击“发布视频”，选择小于等于 100 MiB 的 MP4，填写标题。页面先上传文件，再发布元数据；发布失败时可在同一登录会话中重试，复用已上传地址。
- 也可以切换到“使用已上传地址”，填写本人上传接口返回的 `/media/videos/…` 地址。可以选择不超过 5 MiB 的 JPEG/PNG 封面，预览或移除；不选则使用 `/cover.svg`。自动抽帧尚未实现。
- 详情中仅向当前作者显示删除按钮，删除前二次确认；最终权限仍由后端判断。删除只移除数据库记录，当前不删除文件。上传后取消发布也可能留下文件。
- 公共视频列表、详情和媒体文件是公开访问的；“我的视频”列表需要登录，未发布文件只要知道地址也可访问。页面不显示或持久化 token。

浏览器与 API 使用同一来源。前端文件为 `web/index.html`、`web/style.css`、`web/app.js`、`web/cover.svg`，由 Hertz 的固定文件路由提供。没有额外部署到云端。

## HTTP 接口

基础地址：`http://127.0.0.1:8080`。除两个文件上传接口使用 `multipart/form-data` 外，POST 请求使用 `Content-Type: application/json`，字段放在 JSON 请求体中，不放在 URL 的 Params 中。

| 方法 | 路径 | 身份验证 | 成功结果 |
| --- | --- | --- | --- |
| POST | `/users` | 无 | 201，用户 ID 和用户名 |
| POST | `/login` | 用户名和密码 | 200，用户 ID、用户名和 token |
| GET | `/me` | Bearer token | 200，当前用户 ID 和用户名 |
| POST | `/videos` | Bearer token | 201，视频 ID、作者 ID 和标题 |
| GET | `/videos/:id` | 无 | 200，视频元数据 |
| GET | `/videos` | 无 | 200，分页参数及视频数组 |
| GET | `/me/videos` | Bearer token | 200，仅当前用户的视频及分页参数 |
| DELETE | `/videos/:id` | Bearer token | 204，无响应体，仅作者可删除 |
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

测试在进程内构造 HTTP 请求，连接真实 MySQL，创建两个临时账号和测试视频，结束时按本次生成的唯一用户名清理。它会改变自增计数，不是完全只读的测试。34 个子场景覆盖双账号个人列表隔离、公共列表、封面上传及类型记录、默认及本人封面发布、封面归属和地址校验、发布权限、本站路径校验、文件类型限制，以及缺少/无效 token、非法/零 ID、他人不能删除、越权尝试后记录保留、作者删除、删除后查询和重复删除。本人上传及发布也在准备测试数据时验证；数据库查询失败通过独立测试模拟。

当前环境可能打印 Sonic 不支持该 Go 版本并回退到 `encoding/json` 的警告；已观察到现有测试可通过，不表示已完成依赖兼容性验证。

## 文件导航

| 文件 | 用途 |
| --- | --- |
| `cmd/server/main.go` | 加载配置、装配数据库和 handler、注册路由、启动 Hertz |
| `internal/user/handler.go` | 注册、登录和当前用户查询，以及用户接口的请求类型 |
| `internal/user/model.go` | 用户模型 |
| `internal/user/password.go` | bcrypt 哈希生成和校验 |
| `internal/database/mysql.go` | 创建 MySQL 连接池并检查连接 |
| `internal/auth/jwt.go` | JWT 签发和验证 |
| `internal/auth/middleware.go` | Bearer token 鉴权中间件 |
| `internal/auth/*_test.go`、`internal/user/*_test.go` | 基础单元测试 |
| `internal/video/handler.go`、`model.go` | 视频元数据接口和模型 |
| `internal/video/delete_integration_test.go` | 连接 MySQL 的删除权限集成测试 |
| `internal/video/storage.go`、`storage_test.go` | 文件保存和测试 |
| `internal/video/upload_test.go` | 上传与公开文件读取测试 |
| `web/` | 原生前端页面、样式、交互和默认封面 |
| `scripts/schema.sql` | 数据库初始建表脚本 |

目录按业务和职责分包，仍是一个进程。启动入口依赖 `database`、`auth`、`user`、`video`，`user` 依赖 `auth`；鉴权包不依赖用户业务包。`internal` 限制这些包供本项目内部使用。当前保留 handler 中的 SQL，暂不增加独立的 service/repository 层。

## 下一阶段

视频及封面归属校验、封面上传、列表按 ID 倒序已实现。后续可完善遗留文件清理、图片完整性检查及自动封面处理。当前没有实现 token 刷新、主动撤销、登录限流等功能，不将此学习版本视为完整的生产认证系统。

### 上传表迁移

新环境使用 `scripts/schema.sql` 创建 `uploads` 表。已有 `video_uploads` 表的环境，在暂停服务后，对当前项目数据库执行一次 `scripts/migrations/001_generalize_uploads.sql`，随后启动新版服务。不要在已经迁移的数据库重复执行该迁移，也不要用初始建表脚本代替迁移。

迁移将 `video_uploads` 改名为 `uploads`，增加 `file_type VARCHAR(16) NOT NULL DEFAULT 'video'`；已有上传记录标记为 `video`，原有视频记录和磁盘文件不变。业务代码明确写入类型，封面上传写入 `cover`。MySQL DDL 隐式提交，不能通过普通事务回滚。已提供封面上传接口及发布时的封面归属校验，前端已支持选择、预览、移除封面，并在发布前上传图片。

### 封面上传

`POST /uploads/covers` 要求 Bearer token，使用 multipart/form-data，字段名为 `file`。支持 `.jpg`、`.jpeg`、`.png`（后缀不区分大小写），最大 5 MiB。JPEG 统一保存为 `.jpg`。通过 `image.DecodeConfig` 检查图片头格式和正尺寸，并要求与后缀匹配；不完整解码图片，也不保证所有像素数据完整或可显示。

缺少文件或空文件返回 400；超限返回 413；不支持的后缀、无效图片头或内容与后缀不匹配返回 415；无有效登录返回 401；文件保存或上传记录写入失败返回 500。数据库写入失败时尝试删除本次封面文件。

成功返回 `filename`、`saved_name`、`size` 和 `cover_url`（`/media/covers/<随机文件名>.jpg` 或 `.png`）。封面保存到 `uploads/covers/`，公开访问，不生成目录列表；数据库记录到 `uploads`，类型为 `cover`。前端发布窗口已接入封面选择及预览；发布时依次上传视频、上传可选封面、提交视频元数据。同一登录会话内失败重试会复用已上传结果；更换封面清除其缓存，切换登录清除两类上传缓存。发布时仅允许系统默认封面 `/cover.svg`，或 `/media/covers/<文件名>`：上传封面必须在 `uploads` 中同时匹配文件名、当前用户及 `file_type = cover`。没有匹配记录返回 403；外部地址、子目录、编码路径及查询或片段后缀返回 400；归属查询异常返回 500。拒绝发布不会删除文件和上传记录，已发布视频的查询和播放保持原样。

`go test -v ./internal/video -run '^TestUploadCover$'` 使用测试数据库替身，覆盖 17 个上传场景及静态图片访问、原始字节保存、失败清理；真实 MySQL 的封面上传记录验证包含在 `TestDeleteIntegration` 中。

前端发布逻辑可运行 `node scripts/web_publish_test.cjs` 验证。测试模拟 DOM 和 API 响应，覆盖默认封面、上传顺序、失败重试、更换及移除图片、大小类型校验和登录失效；不代表真实浏览器显示、图片解码或实际网络联调已验证。

### 本阶段页面验收

自动测试覆盖接口权限、真实数据库记录、静态资源字节以及模拟 DOM 中的发布交互。页面实际效果仍需在真实浏览器确认：选择视频和封面发布后显示图片且可播放；不选封面使用默认图；两个账号的“我的视频”互不混入；删除仅允许作者。此前已人工确认视频上传和播放，新增封面 UI 的实际渲染未自动验证。
