# GoStream：Gin + GORM 迁移阅读指南

本轮将 HTTP 框架从 Hertz 迁移到 Gin，将业务数据访问迁移到 GORM + MySQL。用户、视频、点赞、评论和上传清理继续沿用现有业务规则。Redis、RabbitMQ、Worker、SSE 和 Vue 是后续阶段，本轮没有引入。

## 先看哪些文件

按下面顺序阅读，不需要一次读完所有测试。

| 顺序 | 文件 | 先弄懂的内容 |
| --- | --- | --- |
| 1 | `cmd/server/main.go` | `OpenGORM`、`newRouter`、依赖装配与路由 |
| 2 | `internal/auth/middleware.go` | Gin 中间件、`Set`、`Next`、`Abort` |
| 3 | `internal/user/handler.go` | `ShouldBindJSON`、请求 context、调用 Service 和输出响应 |
| 3a | `internal/user/service.go`、`repository.go` | 注册校验、密码哈希、登录认证与 GORM 创建和查询 |
| 4 | `internal/database/gorm.go` | `*gorm.DB` 与底层 `*sql.DB` 连接池的关系 |
| 5 | `internal/video/comment_repository.go` | Service 接口没有改变，Repository 换用 GORM |
| 6 | `internal/video/handler.go` 的 `Publish` | GORM 显式事务、行锁、状态更新、提交与回滚 |
| 7 | `internal/video/upload_repository.go` | 清理事务仍与发布锁定同一行记录 |

## 1. HTTP 入口怎么变了

原来的 Handler 接收两个参数：

```go
func (h *Handler) List(ctx context.Context, c *app.RequestContext)
```

现在接收 Gin 的请求上下文：

```go
func (h *Handler) List(c *gin.Context) {
    ctx := c.Request.Context()
    // 将 ctx 继续传给 Service 或 GORM。
}
```

`*gin.Context` 负责读取参数、保存本次请求的用户身份、输出响应；`c.Request.Context()` 是标准库 `context.Context`，负责取消和超时传播。两者用途不同。

JSON 绑定使用 `ShouldBindJSON`，由 Handler 自己决定错误响应。Gin 的 `BindJSON` 会自动中断并写入 400，容易与原有自定义 JSON 错误响应冲突。

JWT 中间件继续通过 `c.Set("userID", userID)` 保存验证后的身份。`c.Next(ctx)` 变成 `c.Next()`；Service 仍接收普通 Go 参数，不依赖 Gin。

`newRouter` 注册完整应用路由；HTTP 服务由标准库 `http.Server` 运行，并在 Ctrl+C 时尝试优雅关闭。静态工作台仍由同一个服务提供。

## 2. 数据库对象怎么变了

视频 Handler 和关系数据库 Repository 的 `DB` 字段现在是 `*gorm.DB`。用户 Handler 已完成分层，只持有 `Service` 和 `JWTSecret`，不再持有数据库连接：

```go
db, err := database.OpenGORM(dsn)
repository := &video.SQLLikeRepository{DB: db}
service := video.NewLikeService(repository)
```

`*gorm.DB` 提供 ORM 操作，底层仍使用 `database/sql` 的连接池。`OpenDB` 保留为创建、Ping 底层连接池的方法；`OpenGORM` 在这个连接池上初始化 GORM。`WrapSQL` 也供测试包装假的 SQL 驱动，测试不必连接真实数据库。

原来的 `SQLCommentRepository`、`SQLLikeRepository`、`SQLUploadRepository` 名称保留，表示它们是基于关系数据库的存储实现，字段已经使用 GORM。

## 3. 常见操作如何对应

| 原来的写法 | 现在的写法 |
| --- | --- |
| `ExecContext` 插入用户，再读取 `LastInsertId` | `db.WithContext(ctx).Create(&user)`，主键回填到 `user.ID` |
| `QueryRowContext(...).Scan(...)` 查询实体 | `Where(...).Take(&user)`，结果填入结构体 |
| `QueryContext`、`rows.Next`、`rows.Scan` | `Where(...).Order(...).Limit(...).Offset(...).Find(&items)` |
| `sql.ErrNoRows`（实体不存在） | `gorm.ErrRecordNotFound` |
| 删除后判断 `RowsAffected()` | `result.RowsAffected` |
| `BeginTx`、`Commit`、`Rollback` | `db.WithContext(ctx).Begin()`、`tx.Commit().Error`、`tx.Rollback()` |

`Take` 没有匹配项时返回 `gorm.ErrRecordNotFound`；`Find` 查询不到列表数据时正常返回。列表仍从非 nil 空切片开始，JSON 保持 `[]`。

每次 GORM 操作需要检查 `.Error`。不能因为方法调用完成就认为操作成功。

## 4. 为什么仍然能看到 SQL

GORM 支持 ORM 与原生 SQL 混用。本次有意保留：

- 点赞、评论的 `INSERT ... SELECT`：保证视频存在时才插入，保持原来的原子操作。
- 点赞总数与个人状态查询：使用一条聚合 SQL。
- 上传候选的关联子查询：检查视频和封面是否仍被引用。

这些语句通过 `WithContext(ctx).Exec(...)` 或 `Raw(...)` 执行。条件插入使用 GORM `WithResult()` 取得同一次 SQL 的执行结果，因此不会去其他连接读取 `LAST_INSERT_ID()`。

MySQL 的重复键错误仍在 Repository 识别，Service 继续决定重复点赞算成功。Service 的接口和业务规则没有变。

## 5. 原有数据库与文件规则

- 本轮不调用 `AutoMigrate`，不增加或删除数据库字段，已有 `.env`、表结构和上传目录继续使用。
- `Comment.TableName()` 明确对应 `video_comments`，避免 GORM 默认推断成 `comments`。
- `CreatedAt` 使用 `gorm:"->"`，读取时间由数据库维护，不让 ORM 用 Go 的零值或本地时钟覆盖原有默认值。
- 上传创建只写用户 ID、文件名、类型；`pending` 和创建时间继续使用数据库默认值。
- 发布与清理使用 `clause.Locking{Strength: "UPDATE"}`，对应 `FOR UPDATE`。
- GORM 的自动单语句事务关闭，发布与清理的多步操作保留显式事务。
- 用户视频最大 100 MiB、封面最大 5 MiB、完整 HTTP 请求最大 101 MiB。
- 大 multipart 请求可临时落盘，请求结束后清理框架解析产生的临时文件。
- `internal/httpx.RegisterMedia` 使用 `http.ServeContent`，保持 GET、HEAD、Range 播放，不提供目录列表、不缓存文件句柄。

## 6. 如何运行与验证

从项目根目录运行，MySQL 服务需已启动：

```powershell
go run ./cmd/server
```

页面仍为 `http://127.0.0.1:8080/`。现有原生前端没有更换，接口方法、URL 和正常响应字段沿用旧版。

普通测试无需真实数据库：

```powershell
go test ./...
go vet ./...
```

显式开启本地 MySQL 测试：

```powershell
$env:GOSTREAM_INTEGRATION_TEST = '1'
try {
    go test ./... -count=1
} finally {
    Remove-Item Env:GOSTREAM_INTEGRATION_TEST
}
```

测试会创建并清理独立账号、视频及上传记录，自增计数会变化。清理命令端到端测试额外创建临时数据库，需要 CREATE/DROP DATABASE 权限；它只删除临时目录的文件。

迁移后增加了生产路由的静态文件、鉴权、请求大小限制测试；`TestGinGORMHTTPIntegration` 使用 `httptest.NewServer` 对真实 Gin 路由发 HTTP 请求，覆盖注册、重复注册、登录、`/me`、上传、发布、查询、点赞、评论和删除。原有发布回滚、行锁竞争和清理命令端到端测试继续保留。

已知限制：符号链接文件保护测试在 Windows 未获创建符号链接权限时跳过；真实浏览器的布局和视频解码仍需人工查看。API 自动测试中的 MP4 仅包含识别用文件头，不是完整的可播放视频。

## 建议你先回答的三个问题

1. 为什么 Handler 使用 `*gin.Context`，Service 仍使用 `context.Context`？
2. `Create(&user)` 执行后，`user.ID` 是怎么得到的？
3. 为什么 ORM 迁移后，发布与清理仍需要同一行记录上的锁？

参考：[Gin JSON 绑定](https://gin-gonic.com/en/docs/examples/binding-and-validation/)、[GORM 数据库连接](https://gorm.io/docs/connecting_to_the_database.html)、[GORM 事务](https://gorm.io/docs/transactions.html)。

## 用户模块分层收尾

注册、登录、当前用户查询均已接入 `UserService` 和 `GORMRepository`。启动入口按 Repository → Service → Handler 装配对象。

- `Register` 校验输入、生成 bcrypt 哈希并保存用户。重复用户名转换为 `ErrUsernameExists`，Handler 返回 409。
- `Login` 查询用户并校验密码；用户不存在与密码错误统一为 `ErrInvalidCredentials`，Handler 返回 401。通过认证后由 Handler 使用服务端密钥签发 JWT。
- `GetCurrentUser` 按 JWT 中已验证的用户 ID 查询公开信息。查询不读取密码哈希；用户被删除时 Handler 返回 401。URL 参数不能改变当前用户身份。
- Handler 的所有错误分支都结束请求，防止写入错误 JSON 后继续写入成功响应。
- `UserHandler.DB` 已移除。现阶段 JWT 签发仍在 Handler，业务 Service 不依赖 Gin 或 HTTP 状态码。
