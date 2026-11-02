import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 开发时由 Vite 转发请求；构建后由 Gin 提供页面及 API，仍然同源。
const proxy = Object.fromEntries(
  ['/users', '/login', '/me', '/videos', '/uploads', '/comments', '/feed', '/media', '/healthz', '/readyz']
    .map(path => [path, { target: process.env.GOSTREAM_DEV_PROXY || 'http://127.0.0.1:19080', changeOrigin: true }]),
)

export default defineConfig({
  plugins: [vue()],
  server: { host: '127.0.0.1', port: 5173, strictPort: true, proxy },
  build: { outDir: '../web/dist', emptyOutDir: true },
})
