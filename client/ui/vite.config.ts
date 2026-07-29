import { readFileSync } from 'node:fs'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 版本号只留一个源头。此前 UI 里有六处硬编码的 '0.1.0'，升版本时必然漏掉，
// 结果是安装包已经是新版、客户端左下角还显示旧版本——而版本号恰恰是排查时
// 第一个要问的东西，显示错了比不显示更糟。
const { version } = JSON.parse(
  readFileSync(new URL('./package.json', import.meta.url), 'utf8'),
) as { version: string }

export default defineConfig({
  plugins: [react()],
  base: './', // 相对路径:Electron 经 file:// 加载构建产物时资源才能解析
  define: { __APP_VERSION__: JSON.stringify(version) },
  server: { port: 5273 },
})
