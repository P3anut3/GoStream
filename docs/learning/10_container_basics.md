# M1：API 镜像与 Compose 的第一步

状态（2026-11-02）：学习者已添加 Dockerfile 和 SIGTERM，导师完成实际构建和隔离容器验证，镜像 gostream-api:dev 保留在本机。Docker 29.8.2、linux/x86_64；Compose v5.6.0。默认停止退出码为 0；四服务 Compose 和跨容器重建的持久化已验收，见 [Compose 开发环境](12_compose_environment.md)。临时测试资源已清理。

## 本次的用户目标

把 GoStream 的 API 和运行资源打包为镜像。后续在容器内运行同一份应用，再由 Compose 组织 MySQL、Redis 和 RabbitMQ。环境里启动 Redis/MQ 不等于业务已经使用它们。

Dockerfile 是构建步骤，镜像是步骤执行后产生的运行模板，容器是基于镜像启动的进程及运行环境。
Docker Compose 将多个容器的启动参数、网络和卷写成配置；它与 Dockerfile 职责不同。

## 已添加的 Dockerfile

在项目根目录创建 Dockerfile（没有扩展名）：

```dockerfile
FROM node:24-bookworm-slim AS frontend
WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM golang:1.27.1-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/gostream ./cmd/server

FROM builder AS checks
RUN go vet ./... && go test -race ./...

FROM debian:bookworm-slim

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates tzdata curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /out/gostream ./gostream
COPY --from=frontend /web/dist/ ./web/dist/
COPY internal/config/config.yaml ./internal/config/config.yaml

RUN mkdir -p uploads/videos uploads/covers \
    && chown -R 10001:10001 /app

ENV GIN_MODE=release \
    TZ=Asia/Shanghai \
    GOSTREAM_HTTP_ADDR=0.0.0.0:8080

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/app/gostream"]
```

构建镜像 golang:1.27.1-bookworm 和运行镜像 debian:bookworm-slim 均已通过注册表 manifest 查询确认支持 linux/amd64；没有为引入 Docker 升级 go.mod 的版本。首次查询 manifest 只验证镜像存在；本轮另执行了真正的镜像构建和容器启动。

## 关键指令与项目的对应关系

- builder 阶段提供 Go 编译工具，把源码编译为 Linux 可执行文件；CGO_ENABLED=0 使用不依赖 C 工具链的构建方式，实际兼容性仍需后续构建和启动验证。
- 先复制 go.mod/go.sum 并下载依赖，再复制源码：源码变化但依赖声明不变时，可以复用前面的构建缓存。
- 第二个 FROM 开始运行阶段，仅复制编译结果、web、YAML；最终镜像不保留 builder 中的 Go 工具链和整份源码。
- ca-certificates 用于 TLS 根证书；tzdata 配合 TZ 让 Local 时区与当前学习环境一致；curl 为后续容器探测准备。
- WORKDIR /app 与 main 当前相对路径一致：可找到 /app/internal/config/config.yaml 和 /app/web。
- USER 10001:10001 以普通 UID/GID 运行；提前创建并授予上传目录写权限。后续挂载的卷仍需按实际权限验证。
- 监听 0.0.0.0:8080 让容器网络能访问 API；容器内的 127.0.0.1 只指向容器自身。
- EXPOSE 是端口说明，不会自动把端口发布到 Windows 主机；后续运行参数/Compose 才设置主机映射。
- ENTRYPOINT 直接启动编译产物；容器运行时不再 go run。

数据库 DSN、JWT 和游标签名密钥在容器启动时注入，不复制 .env，不写进 Dockerfile。当前根 .env 的数据库 127.0.0.1 是 Windows 本机地址，不能原样当成 API 容器中的 MySQL 地址；Compose 中将使用 MySQL 服务名。

## 已执行的构建与验收

导师从项目根目录执行 docker build --progress=plain -t gostream-api:dev .，成功；末尾的点表示项目根目录为构建上下文。构建上下文约 436 kB；首次下载基础镜像、Go 模块和运行包。镜像标识为 sha256:290c34df965f9e2a778384030d40a4f618a1eea822d8628c2d7599c4d679d365，docker image inspect 报告大小为 241506857 字节。该标识和大小仅代表本次实际构建，不是后续构建的保证值或性能指标。

cmd/server/container_integration_test.go 的 TestContainerAPIIntegration 已显式执行并 PASS（约 24 秒，镜像下载与构建时间不在该测试耗时内）：

- 使用随机命名的私有测试网络，运行 API 和 mysql:8.4.9 两个临时容器。MySQL 不发布主机端口；API 只绑定主机 127.0.0.1 的随机端口。使用临时随机凭证，没有读取项目 .env 的密码或签名密钥。
- 项目 schema 以只读文件挂载到测试 MySQL 的初始化目录，仅创建测试容器中的业务表。保留本机 MySQL 和原有 redis 停止容器。
- 实际 UID 为 10001，工作目录 /app；二进制、web、YAML、证书及时区存在，时区为 +0800；运行镜像没有 /app/.env、用户 Service 源码和 Go 编译器。
- 注册、登录、视频/PNG 封面上传至 /app/test-media 自定义目录、发布和详情通过；文件字节与上传内容一致，Range 返回 206。MP4 是类型识别测试字节，不用于证明浏览器解码播放。
- 停止测试 MySQL 后，API 保持存活，/readyz 为 503、/healthz 为 200；重启 MySQL 并等待就绪后，/readyz 恢复 200，原作品仍可读取。
- 首轮使用显式 SIGINT；补入 SIGTERM 并重建后，测试改为默认 docker stop，退出码为 0（约 25 秒完成整组测试）。尚未覆盖耗时请求排空。
- 使用测试清理函数移除本轮容器、私有网络和 MySQL 匿名卷，退出后再次确认没有带 gostream.smoke 标签的测试网络或容器。镜像和构建缓存保留，原 redis 容器仍为原来的停止状态。

普通 go test ./...、go vet ./... 同时通过。容器测试默认跳过；已准备镜像后，从项目根目录 PowerShell 复现：

```powershell
$env:GOSTREAM_DOCKER_TEST = '1'
try {
    go test ./cmd/server -run '^TestContainerAPIIntegration$' -count=1 -v -timeout=10m
} finally {
    Remove-Item Env:GOSTREAM_DOCKER_TEST
}
```

首次需先构建 gostream-api:dev 并拉取 mysql:8.4.9；测试使用 --pull=never，避免悄悄更换镜像版本。可选 GOSTREAM_DOCKER_IMAGE 指向另一份已在本机构建的待测 API 镜像。

## 默认 SIGTERM 与 Compose 的后续验收

main 当前捕获 os.Interrupt 和 syscall.SIGTERM。SIGTERM 验收时镜像标识为 sha256:c43f8f1174695ec8d610cf5e5f295b78c9177667ac759e5d8273192f4c0b72ce；前面的 290c34… 是首轮构建记录。之后 Vue 迁移再次重建，不能用这些历史标识代表最新镜像。默认 docker stop 已通过正常退出验证，没有覆盖耗时请求排空。

Compose 初始化、四个服务的健康状态、命名卷与 down/up 后数据保留已由独立测试验收。Vue 迁移、镜像内构建和真实浏览器验收也已完成，见 [Compose 环境](12_compose_environment.md)与 [Vue 迁移](13_vue_migration.md)。可选 checks 目标已实际通过 Linux vet/race 测试，不进入运行镜像。基础 CI 已在 GitHub Actions 首次运行通过，Redis 已用于视频详情缓存，MQ 业务接入仍在后续阶段。

参考：[Docker 多阶段构建](https://docs.docker.com/build/building/multi-stage/)、[构建上下文与 dockerignore](https://docs.docker.com/build/concepts/context/)、[Docker 的 Go 指南](https://docs.docker.com/guides/golang/)。
