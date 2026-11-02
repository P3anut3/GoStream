# M1：存活与就绪检查

状态（2026-11-02）：GET /healthz、GET /readyz 已由学习者实现并注册；导师完成路由、故障边界及真实 MySQL 验证。

## 接口约定

| 路径 | 判断内容 | 成功 | 失败 |
| --- | --- | --- | --- |
| GET /healthz | HTTP 进程可响应，不访问数据库 | 200，{"status":"ok"} | 无额外依赖检查 |
| GET /readyz | 当前 MySQL 连接可用 | 200，{"status":"ok"} | 503，{"status":"not_ready"} |

两者不要求登录。就绪响应使用 Cache-Control: no-store，不把数据库地址、账号或底层错误返回调用者。
当前只实现检查信号，没有外部组件自动轮询、重启进程或切换流量；部署工具需在后续明确接入。

## 实际用途和边界

进程已运行后，MySQL 短暂不可用时，存活检查仍可成功，就绪检查应失败。这能让部署工具区分「进程不响应」和「必要依赖不可用」，按不同策略处理。

当前 main 中 database.OpenGORM 会在 HTTP 监听之前检查数据库连接。因此 MySQL 在启动阶段就不可用时，main 会退出，并不存在还能访问的 /healthz。本轮不改变该启动策略。

PingContext 检查数据库连接是否可用，不证明表结构正确、磁盘文件存在、所有业务都正常，也不修改业务记录。当前项目只把 MySQL 连通性纳入就绪条件，未来依赖应按是否为服务必需项决定是否纳入。

## 实现要点

internal/health/handler.go 的 Ready(db *gorm.DB) gin.HandlerFunc 在 newRouter 通过 router.GET("/readyz", health.Ready(db)) 注册。

1. Ready(db) 在路由装配时接收数据库依赖，返回处理函数；检查操作发生在每次请求中。
2. db 为 nil 或 db.DB() 失败时返回 503，避免测试环境或无效连接引发 panic。
3. db.DB() 获取已经存在的底层 *sql.DB 连接池，不是新建另一套连接池；不要在处理函数中 Close。
4. 从请求 context 派生 1 秒超时，defer cancel()，调用 pool.PingContext(ctx)。请求取消或超时应结束这次探测。
5. Ping 失败返回通用 503 响应；成功返回 200。不在探测中修复表、执行迁移或重启数据库。

## 已执行的验证

cmd/server/health_test.go 的 TestHealthzWithoutDatabaseOrLogin 使用真实 newRouter，数据库参数为 nil；匿名和携带无效 token 的两种请求均返回 200、JSON 且只有 status=ok。证明路由已挂载、探测无需认证且不依赖数据库。

cmd/server/readiness_test.go 使用标准 database/sql 驱动替身，只允许 Ping，不支持业务查询或事务，验证：

- 路由注册时没有探测；每次就绪请求只 Ping 一次；探测不要求认证。
- 同一个路由/连接池成功 → 失败 → 恢复，响应为 200 → 503 → 200，失败时 /healthz 仍为 200。
- 缺少 GORM 对象、无法取得 SQL 池、连接池已关闭都返回通用 503，无 panic，不回显底层错误。
- 就绪响应为 JSON，只有 status 字段，Cache-Control 为 no-store。
- 请求 context 中的值与取消信号传到探测；无上游截止时间时约 1 秒触发 context.DeadlineExceeded，主动取消则得到 context.Canceled。
- 探测结束后共享池仍可使用，没有被 Handler 关闭。

cmd/server/health_integration_test.go 的 TestHealthRoutesMySQLIntegration 显式开启后，使用真实 MySQL 与临时 httptest HTTP 服务验证：连接正常时两接口均为 200；关闭本测试独占的连接池后，就绪为 503、存活仍为 200。本轮已执行并通过，无数据库写入，没有停止 MySQL，也没有影响正式服务连接池。

边界：探测超时和主动取消使用驱动替身验证；TestHealthRoutesMySQLIntegration 验证真实连接与测试池关闭后的行为。后续 TestContainerAPIIntegration 已补独立 MySQL 容器停机/恢复验证：同一个 API 进程中 /readyz 从 200 变 503 再变 200，/healthz 持续可用；没有停止本机 MySQL，也没有进行真实网络超时演练。详见 10_container_basics.md。

go test ./...、go vet ./... 和显式 TestHealthRoutesMySQLIntegration 均通过，退出码为 0。复现真实连接测试：在项目根目录 PowerShell 中，设置本次进程的 GOSTREAM_INTEGRATION_TEST=1，再执行 go test ./cmd/server -run '^TestHealthRoutesMySQLIntegration$' -count=1 -v。

## 下一阶段：Compose

配置与两个探测端点就绪。Docker 29.8.2、linux/x86_64 引擎和 Compose v5.6.0 已验证可用。API 镜像、默认 SIGTERM、隔离容器端到端测试及四服务 Compose 重建持久化测试均已通过。Compose API 使用 /readyz，启动时等待 MySQL 的健康检查；临时测试资源已清理。见 [API 镜像](10_container_basics.md)与 [Compose 环境](12_compose_environment.md)。
