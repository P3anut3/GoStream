# M1-1：配置统一入口（API/清理命令已接入并回归）

## 2026-11-02 实施进度

- 学习者已实现 Config、Load、applyEnv、Validate；当前校验文件名为 vaildate.go，Go 按方法名识别，不影响编译。
- 当前 YAML 实际保存于 internal/config/config.yaml；尚未迁到 configs 目录。下一步从项目根目录运行，main 应传入这个实际路径，不应引用尚不存在的文件。
- 导师已添加并执行 load_test.go、validate_test.go。YAML 映射与拒绝错误字段、环境覆盖、未设置与显式空值、18 组配置校验、7 组最终环境值校验通过；go test ./... 通过。
- 这些测试不连接数据库，也没有启动服务；普通测试命令不表示启用了依赖外部服务的集成测试。
- main 已使用 Config：Load 的结果用于 HTTP 监听地址、DSN、JWT 与 Feed 游标签名密钥；.env 缺失已允许由进程环境提供配置。当前 go test ./... 通过，但未启动服务验证实际监听。
- 学习者已完成媒体目录传递：main → newRouter 的 StorageConfig → Handler 的 UploadDir/CoverDir → 文件保存及媒体读取。媒体 URL 前缀保持 /media/videos 和 /media/covers。
- 导师已调整所有路由测试调用，包括显式开启的 MySQL 和浏览器手工环境；测试目录通过 t.TempDir 隔离。媒体回归验证自定义目录的视频/封面 GET、HEAD、Range，旧默认目录同名文件不会被读取，只有旧目录存在的文件返回 404。
- 本次 go test ./...、go vet ./... 均通过；另显式开启 TestGinGORMHTTPIntegration 和 TestFeedHTTPMySQLIntegration，真实 HTTP/MySQL 两项均通过，上传文件落在自定义临时目录，原有发布/个人列表/互动/权限流程通过，测试夹具已清理。本轮没有启动用户的正式服务，也未重新做浏览器验收。
- 学习者已将 cmd/cleanup-uploads/main.go 的 openRuntime 接入 config.Load，使用相同 DSN 与目录，.env 缺失处理也与 API 一致。
- 导师补充 8 组真实 openRuntime 配置失败测试，以及真实清理命令的 3 种配置 × 3 个阶段回归：YAML 自定义相对目录且无 .env、环境变量绝对目录覆盖 YAML/.env、默认目录；每种验证预览、执行、重复执行。过期文件及记录正确删除；未过期/已发布/被引用的文件保留；旧默认目录和被覆盖目录的同名文件不变。
- 已显式执行 TestCleanupCommandE2E，通过并清理 3 个随机临时 MySQL 测试库和临时文件；没有对项目业务库执行清理。go test ./...、go vet ./... 也通过。后续存活/就绪检查现已完成，见 09_health_checks.md，下一步进入 Compose/Vue。
- .env 是本地便利文件；接入时只忽略文件不存在，其他读取错误仍报错。已有进程环境配齐时应允许没有 .env，不生成临时密钥。

## 目标和改造前的问题

改造前 cmd/server/main.go 分别读取 GOSTREAM_DSN、JWT_SECRET、GOSTREAM_FEED_CURSOR_SECRET，监听地址固定为 127.0.0.1:8080，newRouter 中的媒体目录也是固定字符串；目前 API 已通过 Config 装配这些值。
本阶段把「运行环境的差异」集中成配置，业务 Handler/Service 无需知道配置来自文件还是环境变量。

目标启动链路：

```text
YAML 文件中的非敏感默认配置 → 环境变量覆盖 → 校验 → Config
                                                   ↓
                                  main 装配数据库、认证、路由、HTTP 服务
```

保留本地 .env 的便利：godotenv.Load 仅负责把未设置的变量读入当前进程；外部进程环境优先于 .env。它不是 YAML 解析器，也不负责配置合法性校验。

## 约定：哪些值放在哪里

| 配置 | 来源 | 原因 |
| --- | --- | --- |
| HTTP 监听地址 | YAML 默认，GOSTREAM_HTTP_ADDR 覆盖 | 本地与容器需要不同的监听地址 |
| 视频、封面目录 | YAML 默认，环境变量覆盖 | 本地目录与容器挂载路径可能不同 |
| MySQL DSN | GOSTREAM_DSN，允许来自本地 .env | 包含密码，不写进受版本控制的 YAML |
| JWT 密钥 | JWT_SECRET，允许来自本地 .env | 用于身份凭证签名与校验 |
| Feed 游标密钥 | GOSTREAM_FEED_CURSOR_SECRET，允许来自本地 .env | 用于游标签名，与 JWT 密钥独立配置 |

本轮先管理已有依赖；Redis/MQ 配置等到实际接入时再加。（M3，2026-11-04：已加入可选的 Redis 配置。YAML `redis.addr` 默认为空，表示不启用缓存；环境变量 `GOSTREAM_REDIS_ADDR` 覆盖地址，密码只来自 `GOSTREAM_REDIS_PASSWORD`，写进 YAML 会因未知字段被拒绝；填写地址时必须为 host:port。）配置文件缺失、地址为空、必要密钥不足 32 字节应明确返回错误；错误及日志不打印配置全集、DSN 或密钥。不要自动生成临时密钥，否则重启后已有凭证会失效。

## 为什么清理命令也要复用配置

API 与 cleanup-uploads 是两个独立进程，分别创建数据库连接和各自的配置对象。复用 Config/Load 表示复用配置结构及解析规则，不是共用内存中的那个 cfg，也不是让清理命令请求 HTTP API。

实际故障例子：API 根据配置保存到 D:/gostream-data/videos，清理命令仍拼接 uploads/videos。uploads 表只记录 saved_name 和 file_type，不保存每条记录的绝对目录，因此清理必须用正确目录拼接文件名。
现有 storage.go 的 removeUploadedFile 把文件不存在视为已清理，以便重试；upload_cleanup_service.go 的 finishCleanup 随后删除上传记录。因此路径配错时，可能表现为命令成功、记录删除，但真正的文件还留在新目录，无法靠原上传记录再次定位。这是复用目录配置的具体用途。

同样，两个入口必须指向预期的同一个业务数据库，否则清理依据的记录也会不同。使用同一份 YAML 和相同的相关环境变量，能让装配规则一致；不同进程若注入不同覆盖值，仍会得到不同配置，不能把复用 Load 当作自动同步。修改配置后各进程需重新启动或重新运行；当前没有热加载。

本轮练习先让 openRuntime 调用现有 config.Load，再用 cfg.Database.DSN 连接数据库、对 cfg.Storage.VideoDir/CoverDir 求绝对路径。两个入口都从项目根目录运行，保证相对目录基准一致。更换目录不会迁移历史文件。

已知取舍：当前 Load 会校验完整应用配置，因此清理命令即使不使用 JWT，也需要两个签名密钥通过校验。先沿用现有整份配置；若要独立部署清理进程，可把共享解析与 API/清理的按需校验拆开，避免复制 YAML/env 解析逻辑。openRuntime 已由学习者接入；真实清理已在隔离测试库和临时目录中验证。

## 你的第一步：只定义配置结构

创建 internal/config/config.go，暂不改 main、不安装新依赖、不连接数据库。

```go
package config

type Config struct {
	HTTP     HTTPConfig     `yaml:"http"`
	Storage  StorageConfig  `yaml:"storage"`
	Database DatabaseConfig `yaml:"-"`
	Auth     AuthConfig     `yaml:"-"`
}

type HTTPConfig struct {
	Addr string `yaml:"addr"`
}

type StorageConfig struct {
	VideoDir string `yaml:"video_dir"`
	CoverDir string `yaml:"cover_dir"`
}

type DatabaseConfig struct {
	DSN string
}

type AuthConfig struct {
	JWTSecret        string
	FeedCursorSecret string
}
```

Config 就是普通的 Go 结构体，使用 cfg.HTTP.Addr 取得监听地址。它既不连接数据库，也不替你读取文件。
yaml 标签只告诉后续解析库如何对应字段；yaml:"-" 表示数据库与认证配置不从 YAML 映射。
不要为所有字段加 JSON 标签或把整个 Config 返回前端。是否为导出字段、是否有 JSON 标签，都不能代替保密边界。

## 后续按顺序完成

1. 定义 Load(path string) (Config, error)，先解析非敏感 YAML，再覆盖环境变量，最后校验。
2. 导师补充配置测试：覆盖优先级、必要项缺失、空值/无效值、错误不泄漏密钥；测试不依赖 MySQL。
3. main 只调用一次 Load，用返回值装配现有组件；暂不创建全局 Config 变量。
4. 覆盖服务与媒体目录的硬编码，更新启动说明，再做 HTTP 回归（已完成）。
5. 清理命令复用同一份 DSN 和目录配置，覆盖自定义目录场景（已完成）；避免清理路径与 API 保存路径不一致。
6. 增加存活/就绪检查，随后进入 Compose 与 Vue 迁移。

验收：换端口和媒体目录不改 Go 源码；现有环境变量继续可用；无效配置在启动时失败；原有业务测试仍通过。

思考：为什么 Load 只返回 DSN 字符串，而把连接 MySQL 的动作留给 main 装配的 database 包？先用「配置数据」与「实际资源」的区别解释即可。
