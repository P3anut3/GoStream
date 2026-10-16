# GoStream

一个用于学习 Go 后端开发的短视频后端项目，逐个模块实现和验证。当前阶段完成用户注册、登录、JWT 鉴权和当前用户查询，视频模块尚未实现。

## 当前功能

- Hertz HTTP 服务，单进程运行。
- MySQL 用户存储，用户名唯一约束。
- bcrypt 密码哈希与校验，不存储明文密码。
- HS256 JWT，包含用户 ID、签发时间和两小时有效期。
- Bearer token 鉴权中间件。
- 密码、JWT 和鉴权中间件的基础单元测试。

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

脚本创建 `gostream` 数据库和 `users` 表，不删除已有数据。`IF NOT EXISTS` 不会更新已有表的结构；它是初始建表脚本，不是数据库迁移系统。应用本身不会自动建表。

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

## HTTP 接口

基础地址：`http://127.0.0.1:8080`。POST 请求使用 `Content-Type: application/json`，字段放在 JSON 请求体中，不放在 URL 的 Params 中。

| 方法 | 路径 | 身份验证 | 成功结果 |
| --- | --- | --- | --- |
| POST | `/users` | 无 | 201，用户 ID 和用户名 |
| POST | `/login` | 用户名和密码 | 200，用户 ID、用户名和 token |
| GET | `/me` | Bearer token | 200，当前用户 ID 和用户名 |

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

## 测试与验证范围

```powershell
go test -v ./...
```

当前自动测试覆盖：正确/错误密码、正确/错误 JWT 密钥、过期 token、无 token 时拦截后续处理、有效 token 时放行并传递用户 ID。测试不启动监听端口，不需要 `.env` 或 MySQL。

注册、重复注册、登录和携带 token 查询 `/me` 已由开发者在本地通过 Postman 手动验证。当前尚无自动化数据库集成测试，也未穷尽 HTTP 参数和 token 的全部边界情况。

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
| `scripts/schema.sql` | 数据库初始建表脚本 |

目录按业务和职责分包，仍是一个进程。启动入口依赖 `database`、`auth`、`user`，`user` 依赖 `auth`；鉴权包不依赖用户业务包。`internal` 限制这些包供本项目内部使用。当前保留 handler 中的 SQL，暂不增加独立的 service/repository 层。开始实现视频功能时再创建 `internal/video`。

## 下一阶段

先理解和整理现有用户模块，再进入视频模块。当前没有实现 token 刷新、主动撤销、登录限流等功能，不将此学习版本视为完整的生产认证系统。
