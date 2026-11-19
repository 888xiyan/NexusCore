import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath } from 'node:url'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  server: {
    port: 5173,
    host: '0.0.0.0',
    proxy: {
      '/v1/chat/rooms': {
        target: 'http://127.0.0.1:7300',
        changeOrigin: true
      },
      // 工具正文与授权在 plugin_tool_center，不进 Hub
      '/v1/tools': {
        target: 'http://127.0.0.1:7400',
        changeOrigin: true,
        rewrite: (path) => {
          const rest = path.replace(/^\/v1\/tools/, '')
          if (rest.startsWith('/named-bots/')) {
            return '/api/tools/' + rest.slice('/named-bots/'.length) + '/named-bots'
          }
          if (
            rest.startsWith('/skills') ||
            rest.startsWith('/grants') ||
            rest.startsWith('/mcp') ||
            rest.startsWith('/traces')
          ) {
            return '/api' + rest
          }
          return '/api/tools' + rest
        }
      },
      '/v1': {
        target: 'http://127.0.0.1:7200',
        changeOrigin: true
      }
    }
  }
})
