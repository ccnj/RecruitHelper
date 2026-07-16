// Electron 主进程(壳):启动脑服务 → 等就绪 → 开窗加载 UI → 退出时停服务。
// 三层职责硬边界:壳只管窗口与进程;逻辑中枢在 Go 服务;UI 只展示与人工回填。
'use strict'
const { app, BrowserWindow } = require('electron')
const path = require('node:path')
const { BrainService } = require('./service')

const PORT = Number(process.env.BRAIN_PORT || 17872)
const ADMIN_BASE = `http://127.0.0.1:${PORT}`
const REPO_ROOT = path.resolve(__dirname, '..', '..')

let service = null
let win = null

// 脑服务二进制:优先环境变量指定的预编译二进制;否则开发期用 `go run`。
function serviceSpec(dataDir) {
  const bin = process.env.BRAIND_BIN
  const args = ['-port', String(PORT), '-data', dataDir]
  if (bin) return { bin, args, cwd: REPO_ROOT }
  return { bin: 'go', args: ['run', './client/service', ...args], cwd: REPO_ROOT }
}

async function boot() {
  const dataDir = path.join(app.getPath('userData'), 'data')
  const spec = serviceSpec(dataDir)
  service = new BrainService({ ...spec, onLog: (l) => console.log(l) })
  service.start()
  const healthy = await service.waitHealthy(ADMIN_BASE)
  if (!healthy) console.error('[main] 脑服务未在期限内就绪')

  win = new BrowserWindow({
    width: 1200,
    height: 840,
    title: '招聘助手 · 客户端',
    webPreferences: { preload: path.join(__dirname, 'preload.js') },
  })
  // 开发期用 vite dev(UI_URL);打包后加载 UI 构建产物。
  const devUrl = process.env.UI_URL
  if (devUrl) win.loadURL(devUrl)
  else win.loadFile(path.join(REPO_ROOT, 'client', 'ui', 'dist', 'index.html'))
}

app.whenReady().then(boot)

app.on('window-all-closed', () => {
  if (service) service.stop()
  app.quit()
})

app.on('before-quit', () => {
  if (service) service.stop()
})
