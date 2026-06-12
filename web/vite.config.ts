import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Console SPA（设计 §5.6）。开发态代理到本地 core；生产构建为纯静态产物
// （部署走镜像/R2+CDN，绝不 bind mount——禁止移植黑名单的部署事故类）。
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/v1': 'http://localhost:8080',
    },
  },
  build: {
    sourcemap: false,
  },
})
