// Electron 主进程(壳):启动脑服务 → 等就绪 → 开窗加载 UI → 显式退出时停服务。
// 三层职责硬边界:壳只管窗口与进程;逻辑中枢在 Go 服务;UI 只展示与人工回填。
'use strict'
const { app, BrowserWindow, Menu, Tray, dialog, nativeImage } = require('electron')
const crypto = require('node:crypto')
const path = require('node:path')
const { BrainService } = require('./service')
const { resolveLayout, resolveDataDir } = require('./layout')

const PORT = Number(process.env.BRAIN_PORT || 17872)
const ADMIN_BASE = `http://127.0.0.1:${PORT}`
// 仅开发态有意义:打包后主进程在 asar 内,一切资源改由 resources 提供。
const REPO_ROOT = path.resolve(__dirname, '..', '..')

let service = null
let win = null
let tray = null
let quitting = false

async function boot() {
  const layout = resolveLayout({
    packaged: app.isPackaged,
    resourcesPath: process.resourcesPath,
    repoRoot: REPO_ROOT,
  })
  const dataDir = resolveDataDir({
    packaged: app.isPackaged,
    userDataDir: app.getPath('userData'),
  })
  // 管理 token 每次进程启动重新生成，只在主进程环境与隔离 preload 内存中流转。
  const adminToken = crypto.randomBytes(32).toString('base64url')
  service = new BrainService({
    bin: layout.brainBin,
    args: [...layout.brainArgs, '-port', String(PORT), '-data', dataDir],
    cwd: layout.brainCwd,
    env: { ...process.env, RECRUITHELPER_ADMIN_TOKEN: adminToken },
    onLog: (l) => console.log(l),
  })
  service.start()
  const healthy = await service.waitHealthy(ADMIN_BASE)
  if (!healthy) console.error('[main] 脑服务未在期限内就绪')

  createWindow(adminToken, layout.uiEntry)
  createTray()
}

function createWindow(adminToken, uiEntry) {
  win = new BrowserWindow({
    width: 1200,
    height: 840,
    title: '招聘助手 · 客户端',
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      additionalArguments: [
        `--recruit-helper-admin-base=${ADMIN_BASE}`,
        `--recruit-helper-admin-token=${adminToken}`,
      ],
    },
  })
  // 开发期用 vite dev(UI_URL);打包后加载随包 UI 构建产物。
  const devUrl = app.isPackaged ? '' : process.env.UI_URL
  if (devUrl) win.loadURL(devUrl)
  else win.loadFile(uiEntry)
  win.on('close', (event) => {
    // 普通用户关闭窗口只是在收起 UI。脑仍在同一 Electron 进程中运行，
    // 完整流程、巡检和暂停事实都不由 renderer 的生命周期决定。
    if (!quitting && tray) {
      event.preventDefault()
      win.hide()
    }
  })
  win.on('closed', () => {
    win = null
  })
}

function createTray() {
  try {
    const svg = [
      '<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 32 32">',
      '<rect width="32" height="32" rx="8" fill="#3568e8"/>',
      '<path d="M9 11h14v12H9z" fill="none" stroke="white" stroke-width="2"/>',
      '<path d="M12 11V8h8v3M12 16h8M12 20h5" fill="none" stroke="white" stroke-width="2" stroke-linecap="round"/>',
      '</svg>',
    ].join('')
    const icon = nativeImage.createFromDataURL(
      `data:image/svg+xml;base64,${Buffer.from(svg).toString('base64')}`,
    ).resize({ width: 18, height: 18 })
    tray = new Tray(icon)
    tray.setToolTip('招聘助手')
    tray.setContextMenu(Menu.buildFromTemplate([
      { label: '打开招聘助手', click: showWindow },
      { type: 'separator' },
      {
        label: '退出',
        click: () => {
          quitting = true
          app.quit()
        },
      },
    ]))
    tray.on('click', showWindow)
  } catch (error) {
    // 托盘是 UI 生命周期便利层，不是业务正确性前提。创建失败时保留传统
    // 关闭即退出行为，避免产生无法重新打开的后台进程。
    tray = null
    console.error('[main] 系统托盘创建失败，窗口关闭将退出应用', error)
  }
}

function showWindow() {
  if (!win) return
  if (win.isMinimized()) win.restore()
  win.show()
  win.focus()
}

// 第二个壳不能另铸一枚管理 token 后误连首实例的脑。只保留唯一实例，
// 重复启动退化为把已经运行的普通窗口带回前台。
const primaryInstance = app.requestSingleInstanceLock()
if (!primaryInstance) {
  quitting = true
  app.quit()
} else {
  app.on('second-instance', showWindow)
  app.whenReady().then(boot).catch((error) => {
    // 安装包不完整等启动前置失败必须当场可见,不留一个开了窗却没有脑的壳。
    const detail = String(error?.message || error)
    console.error('[main] 启动失败', detail)
    dialog.showErrorBox('招聘助手无法启动', detail)
    quitting = true
    app.quit()
  })
}

app.on('activate', showWindow)

app.on('window-all-closed', () => {
  if (!tray) {
    quitting = true
    app.quit()
  }
})

app.on('before-quit', () => {
  quitting = true
  if (service) service.stop()
})
