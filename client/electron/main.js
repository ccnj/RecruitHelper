// Electron 主进程(壳):启动脑服务 → 等就绪 → 开窗加载 UI → 显式退出时停服务。
// 三层职责硬边界:壳只管窗口与进程;逻辑中枢在 Go 服务;UI 只展示与人工回填。
'use strict'
const { app, BrowserWindow, Menu, Tray, dialog, nativeImage } = require('electron')
const crypto = require('node:crypto')
const fs = require('node:fs')
const path = require('node:path')
const { BrainService } = require('./service')
const { resolveLayout, resolveDataDir } = require('./layout')
const { pluginInstallDir, ensurePluginInstalled } = require('./pluginSeed')
const { TRAY_ICON_PNG_BASE64 } = require('./trayIcon')

const PORT = Number(process.env.BRAIN_PORT || 17872)
const ADMIN_BASE = `http://127.0.0.1:${PORT}`
// 仅开发态有意义:打包后主进程在 asar 内,一切资源改由 resources 提供。
const REPO_ROOT = path.resolve(__dirname, '..', '..')
// 单代轮转的阈值。只留一份 .old,不做多代归档 —— 这是诊断用的运行日志,
// 不是需要长期保全的业务事实。
const LOG_ROTATE_BYTES = 32 * 1024 * 1024

let service = null
let win = null
let tray = null
let quitting = false
let logStream = null

// 脑的 stdout/stderr 经 BrainService 汇到一处。打包后是 GUI 进程、没有控制台,
// console.log 直接进虚空,所以这里落一份到磁盘,否则现场排查无从下手。
// 内容边界不变:AGENTS.md 对普通日志的约束照旧(不得出现 key、聊天正文、简历
// 正文、完整 prompt 或候选人明文身份)——落盘只是把易失变成持久,不放宽任何一条。
function openLogStream() {
  const logDir = path.join(app.getPath('userData'), 'logs')
  const logPath = path.join(logDir, 'brain.log')
  try {
    fs.mkdirSync(logDir, { recursive: true })
    if (fs.statSync(logPath).size > LOG_ROTATE_BYTES) {
      fs.renameSync(logPath, `${logPath}.old`)
    }
  } catch {
    // 首次运行没有该文件是正常的;目录真不可写时下面开流会失败并降级。
  }
  try {
    const stream = fs.createWriteStream(logPath, { flags: 'a' })
    stream.write(`\n=== 启动 ${new Date().toISOString()} ===\n`)
    return stream
  } catch (error) {
    // 日志写不了不是拦停客户端的理由,退回只有控制台。
    console.error('[main] 日志文件打开失败,仅输出到控制台', error)
    return null
  }
}

function writeLog(line) {
  console.log(line)
  if (logStream) logStream.write(`${line}\n`)
}

async function boot() {
  logStream = openLogStream()
  const layout = resolveLayout({
    packaged: app.isPackaged,
    resourcesPath: process.resourcesPath,
    repoRoot: REPO_ROOT,
  })
  const dataDir = resolveDataDir({
    packaged: app.isPackaged,
    userDataDir: app.getPath('userData'),
  })
  // 只有打包态才安置插件:开发期插件由开发者自己从 plugin/dist 加载。
  // 放在起脑之前是因为这一刻还没有任何批次在跑,替换天然安全;失败只降级。
  if (app.isPackaged) {
    ensurePluginInstalled({
      sourceDir: layout.pluginDir,
      targetDir: pluginInstallDir({ userDataDir: app.getPath('userData') }),
      log: writeLog,
    })
  }
  // 管理 token 每次进程启动重新生成，只在主进程环境与隔离 preload 内存中流转。
  const adminToken = crypto.randomBytes(32).toString('base64url')
  service = new BrainService({
    bin: layout.brainBin,
    args: [...layout.brainArgs, '-port', String(PORT), '-data', dataDir],
    cwd: layout.brainCwd,
    env: { ...process.env, RECRUITHELPER_ADMIN_TOKEN: adminToken },
    onLog: writeLog,
  })
  service.start()
  const healthy = await service.waitHealthy(ADMIN_BASE)
  if (!healthy) writeLog('[main] 脑服务未在期限内就绪')

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
    // 必须是 PNG。此前这里用 SVG data URL,而 nativeImage 只认 PNG/JPEG —— 它对
    // 解析不了的 data URL 不抛错、返回一个空 image,于是 Tray 构造成功、托盘项出现、
    // 图标却一片空白,连下面的 catch 都不会触发。这个失败模式在 macOS 开发机上看不见
    // (那里跑不起 GUI),装到 Windows 才暴露。图标由 scripts/generate-tray-icon.mjs
    // 生成,不 resize:交给系统按当前 DPI 缩放,比先压到固定像素更清晰。
    const icon = nativeImage.createFromDataURL(
      `data:image/png;base64,${TRAY_ICON_PNG_BASE64}`,
    )
    if (icon.isEmpty()) {
      // 只记一笔,照样建托盘。图标解码失败不等于托盘不可用——托盘仍然能点、能退出,
      // 只是没图标。判据是"托盘能不能用",不是"图标好不好看";拿后者去触发下面的
      // 降级,等于用"关窗即停业务"去换一个美观问题,不划算。托盘真建不起来会自己
      // 抛错走 catch。
      writeLog('[main] 托盘图标解码为空,托盘将无图标 —— 检查 trayIcon.js 是否是合法 PNG')
    }
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
  if (logStream) {
    // 退出前把缓冲刷干净,否则最后几行(往往正是崩溃现场)会丢。
    logStream.end()
    logStream = null
  }
})
