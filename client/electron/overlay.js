// 屏幕顶层状态栏窗(2026-09-08 甲方裁决,出口 docs/boss/状态栏出口-2026-09-08.md)。
//
// 一个透明、置顶、不可聚焦、鼠标穿透的小窗,贴在主屏工作区顶部居中,主窗最小化
// 或收进托盘时照样在。它只是一张纸:画什么由 client/ui 的 overlay 入口决定,
// 这里只负责把纸贴上去,而且**只贴一次**——BOSS 的打字是 OS 注入进 Chrome 的
// 焦点窗口,小窗在运行期任何一次 show/focus/置顶重设都可能把键序抢走。所以:
//   - focusable:false + showInactive:从生到死不拿焦点;
//   - 尺寸固定为开发版的高度,客户版只画顶部一条、其余透明:两种模式切换不 resize;
//   - 建窗之后不再调用任何窗口级 API。
//
// 纯函数(几何、选项)与 Electron 调用分开,前者能在 Node 下单测。
'use strict'

const OVERLAY_WIDTH = 640
const OVERLAY_HEIGHT = 220

/**
 * 小窗在主屏工作区里的位置:顶部居中,比工作区还宽时贴左并截到工作区宽。
 *
 * @param {{x:number,y:number,width:number,height:number}} workArea
 * @param {{width:number,height:number}} [size]
 */
function overlayBounds(workArea, size = { width: OVERLAY_WIDTH, height: OVERLAY_HEIGHT }) {
  const width = Math.max(1, Math.min(size.width, workArea.width))
  const height = Math.max(1, Math.min(size.height, workArea.height))
  const x = Math.round(workArea.x + (workArea.width - width) / 2)
  return { x, y: workArea.y, width, height }
}

/**
 * BrowserWindow 选项。Windows 上 `type:'toolbar'` 让它不进 Alt+Tab;
 * macOS/Linux 没有这个类型,不设。
 *
 * @param {{
 *   bounds: {x:number,y:number,width:number,height:number},
 *   preload: string,
 *   rendererArguments: string[],
 *   platform: string,
 * }} opts
 */
function overlayWindowOptions({ bounds, preload, rendererArguments, platform }) {
  const options = {
    ...bounds,
    title: 'AI增员助手状态栏',
    transparent: true,
    frame: false,
    alwaysOnTop: true,
    skipTaskbar: true,
    focusable: false,
    resizable: false,
    movable: false,
    minimizable: false,
    maximizable: false,
    fullscreenable: false,
    hasShadow: false,
    // 先不显示:等 ready-to-show 再 showInactive,免得先闪一块空白再画内容。
    show: false,
    webPreferences: {
      preload,
      contextIsolation: true,
      nodeIntegration: false,
      additionalArguments: rendererArguments,
    },
  }
  if (platform === 'win32') options.type = 'toolbar'
  return options
}

/** 开发态 vite dev 的状态栏页面地址。 */
function overlayDevUrl(devUrl) {
  return `${String(devUrl).replace(/\/+$/, '')}/overlay.html`
}

/**
 * 建窗。除 BrowserWindow/screen 之外全部是数据,方便用假对象单测调用序列。
 *
 * @param {{
 *   BrowserWindow: any,
 *   screen: { getPrimaryDisplay(): { workArea: {x:number,y:number,width:number,height:number} } },
 *   preload: string,
 *   rendererArguments: string[],
 *   entry: string,
 *   devUrl?: string,
 *   platform?: string,
 * }} opts
 */
function createOverlayWindow({
  BrowserWindow, screen, preload, rendererArguments, entry, devUrl = '', platform = process.platform,
}) {
  const { workArea } = screen.getPrimaryDisplay()
  const win = new BrowserWindow(overlayWindowOptions({
    bounds: overlayBounds(workArea), preload, rendererArguments, platform,
  }))
  // 穿透是硬要求:真人与 OS 注入的点击都得从它身上过去。不带 forward:
  // 我们不需要它转发悬停事件,只需要它在命中测试里不存在。
  win.setIgnoreMouseEvents(true)
  // 'screen-saver' 是最高的常用层级;默认的 'floating' 在 macOS 上会被全屏
  // 应用盖住。Windows 只有一个"置顶",层级参数无效但无害。
  win.setAlwaysOnTop(true, 'screen-saver')
  // macOS:跟着所有桌面空间走,Chrome 全屏时也可见。Windows 上是空操作。
  if (typeof win.setVisibleOnAllWorkspaces === 'function') {
    win.setVisibleOnAllWorkspaces(true, { visibleOnFullScreen: true })
  }
  win.once('ready-to-show', () => {
    if (!win.isDestroyed()) win.showInactive()
  })
  if (devUrl) win.loadURL(overlayDevUrl(devUrl))
  else win.loadFile(entry)
  return win
}

module.exports = {
  OVERLAY_WIDTH,
  OVERLAY_HEIGHT,
  overlayBounds,
  overlayWindowOptions,
  overlayDevUrl,
  createOverlayWindow,
}
