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
      '/v1': {
        target: 'http://127.0.0.1:7200',
        changeOrigin: true
      }
    }
  }
})
