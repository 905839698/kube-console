import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'

export default defineConfig({
  plugins: [vue(), Components({ resolvers: [ElementPlusResolver()], dts: false })],
  server: {
    port: 5173,
    proxy: {
      // 开发环境将 /api 代理到后端服务（含 WebSocket 升级）
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
  build: {
    chunkSizeWarningLimit: 1500,
  },
})
