# GoStream Vue 工作台

Vue 3 + TypeScript + Pinia + Vue Router + Vite。API 继续使用项目根目录的 Gin 服务，不改变后端接口。需要 Node.js 24。

```powershell
npm ci
npm run dev
```

开发页面为 http://127.0.0.1:5173/，默认代理 API 到 http://127.0.0.1:19080/。后端直接在 8080 运行时，在执行 dev 的 PowerShell 设置 `$env:GOSTREAM_DEV_PROXY='http://127.0.0.1:8080'`。它只是本机开发配置，不包含任何数据库密码或 JWT 签名密钥。

```powershell
npm test
npm run build
npx playwright install chromium
npm run test:e2e
```

build 先执行 vue-tsc 类型检查，再生成 `../web/dist`。从仓库根目录启动 Go 后即可访问已构建页面，不需要同时运行 Vite。Dockerfile 会自动完成前端构建。

单元测试验证请求、会话隔离、Feed 分页和发布复用；默认 Playwright 测试使用 API 响应夹具，不代表真实后端成功。真实双账号浏览器验收通过根目录的隔离 Compose 测试运行：

```powershell
$env:GOSTREAM_COMPOSE_TEST='1'
$env:GOSTREAM_COMPOSE_BROWSER_TEST='1'
try {
    go test ./cmd/server -run '^TestComposeEnvironmentIntegration$' -count=1 -v -timeout=10m
} finally {
    Remove-Item Env:GOSTREAM_COMPOSE_TEST,Env:GOSTREAM_COMPOSE_BROWSER_TEST
}
```

这段命令应在仓库根目录运行，要求四个 Docker 镜像已准备、当前 API 镜像已重建，且 frontend 的 npm 依赖和 Playwright Chromium 已安装。它创建随机 Compose 项目、凭证、端口和卷，最终清理；不对开发库或原 Windows MySQL 造测试数据。

`tests/fixtures/playable.mp4` 是脚本 `scripts/create-test-video.mjs` 用浏览器录制的移动色块，供真实上传、MP4 解码、播放和 Range 验收使用，不来自用户媒体。默认 API 响应夹具和真实浏览器测试是两类证据。截图、失败 trace 输出到已忽略的 `test-results/`。

当前 token 和草稿只在页面内存，刷新后需要重新登录。页面不持有 JWTSecret。Hash 路由避免与 /videos 等后端 API 路径冲突。
