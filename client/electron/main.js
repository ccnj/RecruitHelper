// Electron 主进程(壳):启动脑服务 → 等就绪 → 开窗加载 UI → 显式退出时停服务。
// 三层职责硬边界:壳只管窗口与进程;逻辑中枢在 Go 服务;UI 只展示与人工回填。
'use strict'
const { app, BrowserWindow, Menu, Tray, dialog, nativeImage, ipcMain } = require('electron')
const crypto = require('node:crypto')
const { spawn } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')
const { BrainService } = require('./service')
const { resolveLayout, resolveDataDir } = require('./layout')
const { pluginInstallDir, updateStageDir, ensurePluginInstalled } = require('./pluginSeed')
const { TRAY_ICON_PNG_BASE64, APP_ICON_PNG_BASE64 } = require('./icons')
const { RotatingLog } = require('./logRotate')

const PORT = Number(process.env.BRAIN_PORT || 17872)
const ADMIN_BASE = `http://127.0.0.1:${PORT}`
// 仅开发态有意义:打包后主进程在 asar 内,一切资源改由 resources 提供。
const REPO_ROOT = path.resolve(__dirname, '..', '..')
// 轮转参数见 logRotate.js:运行期按累计写入触发,保留 5 代 × 16MB。
// 原来是"只在启动时检查一次 + 只留一份 .old",两个毛病都会在最需要日志的时候
// 反咬一口(2026-07-31 甲方裁决改为方案 A)。

let service = null
let win = null
let tray = null
let quitting = false
let logStream = null

// 脑的 stdout/stderr 经 BrainService 汇到一处。打包后是 GUI 进程、没有控制台,
// console.log 直接进虚空,所以这里落一份到磁盘,否则现场排查无从下手。
// 内容边界不变:AGENTS.md 对普通日志的约束照旧(不得出现 key、聊天正文、简历
// 正文、完整 prompt 或候选人明文身份)——落盘只是把易失变成持久,不放宽任何一条。
// 日志目录只在这里算一次:脑要按名字取 brain.log 打进诊断包(现场数据上报,
// 2026-07-31 裁决),两处各写一遍迟早漂移成"传上来的包里永远没有日志"。
function logDirPath() {
  return path.join(app.getPath('userData'), 'logs')
}

function openLogStream() {
  const log = new RotatingLog({ dir: logDirPath() }).open()
  log.write(`\n=== 启动 ${new Date().toISOString()} ===`)
  return log
}

function writeLog(line) {
  console.log(line)
  if (logStream) logStream.write(line)
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
  // 放在起脑之前是因为这一刻还没有任何批次在跑,替换天然安全;失败只降级 ——
  // 也因为这个顺序,脑一启动就能把固定目录里的 manifest 当作"磁盘上是哪一版"
  // 的真相。
  const pluginDir = app.isPackaged
    ? pluginInstallDir({ userDataDir: app.getPath('userData') })
    : ''
  if (pluginDir) {
    ensurePluginInstalled({
      sourceDir: layout.pluginDir,
      targetDir: pluginDir,
      log: writeLog,
    })
  }
  // 管理 token 每次进程启动重新生成，只在主进程环境与隔离 preload 内存中流转。
  const adminToken = crypto.randomBytes(32).toString('base64url')
  const brainEnv = {
    ...process.env,
    RECRUITHELPER_ADMIN_TOKEN: adminToken,
    // 脑自己的数据目录里没有日志 —— 日志是本进程写的,路径只有这里知道。
    RECRUITHELPER_LOG_DIR: logDirPath(),
  }
  // 开发期不传:那时固定目录要么不存在,要么是上次装包留下的陈旧副本,而
  // Chrome 里加载的是开发者自己的 plugin/dist —— 拿它当基准只会误判。
  if (pluginDir) brainEnv.RECRUITHELPER_PLUGIN_DIR = pluginDir
  // 自更新检查同样只在打包态启用:开发期没有"当前版本"这回事(跑的是工作树),
  // 拿 package.json 的版本去跟更新源比,只会把开发机也卷进下载。
  if (app.isPackaged) {
    brainEnv.RECRUITHELPER_UPDATE_DIR = updateStageDir({
      userDataDir: app.getPath('userData'),
    })
    brainEnv.RECRUITHELPER_APP_VERSION = app.getVersion()
  }
  service = new BrainService({
    bin: layout.brainBin,
    args: [...layout.brainArgs, '-port', String(PORT), '-data', dataDir],
    cwd: layout.brainCwd,
    env: brainEnv,
    onLog: writeLog,
  })
  service.start()
  const healthy = await service.waitHealthy(ADMIN_BASE)
  if (!healthy) writeLog('[main] 脑服务未在期限内就绪')

  // 只认本次进程的 token:renderer 不传凭据,主进程用自己手上那份去问脑。
  ipcMain.handle('recruit-helper:install-update', () => runUpdateInstall(adminToken))

  createWindow(adminToken, layout.uiEntry)
  createTray()
}

/**
 * 执行一次自动更新:先问脑能不能装,拿到路径后启动安装器并退出。
 *
 * 裁决全在脑那边(包重新校验、结束运行中的工作流、等在途命令收敛),这里只做
 * 它做不了的两件事 —— 脑杀不掉自己,而安装器要覆盖的正是这两个进程。
 *
 * @returns {Promise<{ok:boolean, error?:string}>}
 */
async function runUpdateInstall(adminToken) {
  let packagePath = ''
  try {
    const response = await fetch(`${ADMIN_BASE}/app/update/install`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${adminToken}` },
    })
    const body = await response.json().catch(() => ({}))
    if (!response.ok) return { ok: false, error: body.error || `脑返回 ${response.status}` }
    packagePath = String(body.packagePath || '')
  } catch (error) {
    return { ok: false, error: `无法联系脑服务:${String(error?.message || error)}` }
  }
  if (!packagePath) return { ok: false, error: '脑未给出安装包路径' }

  try {
    // detached 是硬要求:安装器上来就 taskkill 本进程,非 detached 的子进程会被
    // 一起带走,于是"更新"变成"客户端凭空消失"。unref 让本进程不等它。
    const child = spawn(packagePath, ['/S'], { detached: true, stdio: 'ignore' })
    child.unref()
    writeLog(`[update] 已交出安装器,即将退出:${packagePath}`)
  } catch (error) {
    return { ok: false, error: `启动安装器失败:${String(error?.message || error)}` }
  }

  // 留一点时间让安装器真正起来再退。它随后会 taskkill 我们,这里主动退只是
  // 为了让脑走正常的关闭路径,少留一份要靠恢复轨收敛的中断。
  setTimeout(() => {
    quitting = true
    app.quit()
  }, 1000)
  return { ok: true }
}

function createWindow(adminToken, uiEntry) {
  win = new BrowserWindow({
    width: 1200,
    height: 840,
    title: 'AI增员助手 · 客户端',
    // 标题栏与任务栏图标。给 256 的大图让 Windows 自己按场景降采样,比预先压到
    // 某个尺寸清楚。不设的话这两处会一直是 Electron 的默认原子图标。
    icon: nativeImage.createFromDataURL(`data:image/png;base64,${APP_ICON_PNG_BASE64}`),
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
    // (那里跑不起 GUI),装到 Windows 才暴露。图标由 scripts/generate-icons.mjs
    // 生成,不 resize:交给系统按当前 DPI 缩放,比先压到固定像素更清晰。
    const icon = nativeImage.createFromDataURL(
      `data:image/png;base64,${TRAY_ICON_PNG_BASE64}`,
    )
    if (icon.isEmpty()) {
      // 只记一笔,照样建托盘。图标解码失败不等于托盘不可用——托盘仍然能点、能退出,
      // 只是没图标。判据是"托盘能不能用",不是"图标好不好看";拿后者去触发下面的
      // 降级,等于用"关窗即停业务"去换一个美观问题,不划算。托盘真建不起来会自己
      // 抛错走 catch。
      writeLog('[main] 托盘图标解码为空,托盘将无图标 —— 检查 icons.js 是否是合法 PNG')
    }
    tray = new Tray(icon)
    tray.setToolTip('AI增员助手')
    tray.setContextMenu(Menu.buildFromTemplate([
      { label: '打开AI增员助手', click: showWindow },
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
    dialog.showErrorBox('AI增员助手无法启动', detail)
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
    logStream.close()
    logStream = null
  }
})
