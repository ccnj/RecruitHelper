// 状态栏窗:几何、选项与建窗调用序列。不需 Electron/显示——BrowserWindow 与
// screen 都是假的,盯的是"不拿焦点、穿透、只 showInactive"这几条硬约束。
import { createRequire } from 'node:module'
const require = createRequire(import.meta.url)
const {
  overlayBounds, overlayWindowOptions, overlayDevUrl, createOverlayWindow,
  OVERLAY_WIDTH, OVERLAY_HEIGHT,
} = require('../overlay.js')

let fail = 0
const check = (c, m) => { console.log(c ? '  PASS' : '  FAIL', m); if (!c) fail++ }

// —— 几何 ——
const mac = overlayBounds({ x: 0, y: 25, width: 1440, height: 875 })
check(mac.x === 400 && mac.y === 25 && mac.width === OVERLAY_WIDTH && mac.height === OVERLAY_HEIGHT, '主屏顶部居中,贴工作区顶边(菜单栏之下)')
const offset = overlayBounds({ x: 1920, y: 0, width: 1920, height: 1040 })
check(offset.x === 1920 + 640 && offset.y === 0, '工作区不从 0 起也居中')
const narrow = overlayBounds({ x: 0, y: 0, width: 500, height: 300 })
check(narrow.x === 0 && narrow.width === 500 && narrow.height === OVERLAY_HEIGHT, '比工作区宽时截到工作区宽并贴左')

// —— 选项:硬约束 ——
const opts = overlayWindowOptions({
  bounds: mac, preload: '/p/preload.js', rendererArguments: ['--a=1'], platform: 'darwin',
})
check(opts.focusable === false, '不可聚焦')
check(opts.transparent === true && opts.frame === false && opts.hasShadow === false, '透明无框无影')
check(opts.alwaysOnTop === true && opts.skipTaskbar === true, '置顶且不进任务栏')
check(opts.show === false, '建时不显示,等 ready-to-show')
check(opts.resizable === false && opts.movable === false, '不可拖不可缩')
check(opts.webPreferences.contextIsolation === true && opts.webPreferences.nodeIntegration === false, '渲染器隔离')
check(opts.webPreferences.additionalArguments[0] === '--a=1' && opts.webPreferences.preload === '/p/preload.js', '带管理连接参数与 preload')
check(!('type' in opts), 'macOS 不设 type')
const winOpts = overlayWindowOptions({ bounds: mac, preload: '', rendererArguments: [], platform: 'win32' })
check(winOpts.type === 'toolbar', 'Windows 用 toolbar 类型,不进 Alt+Tab')

// —— dev 地址 ——
check(overlayDevUrl('http://127.0.0.1:5273/') === 'http://127.0.0.1:5273/overlay.html', 'dev 地址去尾斜杠拼 overlay.html')

// —— 建窗调用序列 ——
function fakeWindowClass(calls) {
  return class FakeWindow {
    constructor(options) { this.options = options; this.handlers = {}; calls.push(['new', options]) }
    setIgnoreMouseEvents(...a) { calls.push(['setIgnoreMouseEvents', ...a]) }
    setAlwaysOnTop(...a) { calls.push(['setAlwaysOnTop', ...a]) }
    setVisibleOnAllWorkspaces(...a) { calls.push(['setVisibleOnAllWorkspaces', ...a]) }
    once(event, fn) { this.handlers[event] = fn }
    on() {}
    isDestroyed() { return false }
    showInactive() { calls.push(['showInactive']) }
    show() { calls.push(['show']) }
    focus() { calls.push(['focus']) }
    loadFile(p) { calls.push(['loadFile', p]) }
    loadURL(u) { calls.push(['loadURL', u]) }
  }
}
const screen = { getPrimaryDisplay: () => ({ workArea: { x: 0, y: 25, width: 1440, height: 875 } }) }

const calls = []
const win = createOverlayWindow({
  BrowserWindow: fakeWindowClass(calls), screen, preload: '/p', rendererArguments: [], entry: '/ui/overlay.html', platform: 'darwin',
})
win.handlers['ready-to-show']()
const names = calls.map((c) => c[0])
check(names.includes('setIgnoreMouseEvents') && calls.find((c) => c[0] === 'setIgnoreMouseEvents')[1] === true, '建窗后立即鼠标穿透')
check(calls.some((c) => c[0] === 'setAlwaysOnTop' && c[1] === true && c[2] === 'screen-saver'), '置顶层级 screen-saver')
check(calls.some((c) => c[0] === 'loadFile' && c[1] === '/ui/overlay.html'), '无 devUrl 时加载随包 overlay.html')
check(names.includes('showInactive') && !names.includes('show') && !names.includes('focus'), 'ready-to-show 只 showInactive,从不 show/focus')

const devCalls = []
createOverlayWindow({
  BrowserWindow: fakeWindowClass(devCalls), screen, preload: '/p', rendererArguments: [], entry: '/ui/overlay.html', devUrl: 'http://localhost:5273', platform: 'win32',
})
check(devCalls.some((c) => c[0] === 'loadURL' && c[1] === 'http://localhost:5273/overlay.html'), '有 devUrl 时走 vite dev 的 overlay.html')
check(devCalls[0][1].type === 'toolbar', 'Windows 建窗带 toolbar 类型')

if (fail) { console.error(`\n${fail} 项失败`); process.exit(1) }
console.log('\n状态栏窗测试通过')
