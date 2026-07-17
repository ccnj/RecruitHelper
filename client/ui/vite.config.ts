import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  base: './', // 相对路径:Electron 经 file:// 加载构建产物时资源才能解析
  server: { port: 5273 },
})
