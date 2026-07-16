// Node 集成 harness:注入浏览器全局(chrome/WebSocket),跑真实插件 base 代码,连真脑服务。
// 验证插件的连接/握手/派发/去重逻辑与脑端在真 WS 上端到端互通(不覆盖 SW 生死,那是人端尾巴)。
//
// 用法:node test/run.mjs <ws-url> <admin-base>
import { WebSocket } from 'ws'
import * as esbuild from 'esbuild'
import { pathToFileURL } from 'node:url'
import { writeFileSync, mkdirSync } from 'node:fs'

const wsUrl = process.argv[2] || 'ws://127.0.0.1:17872/v1/channel'
const adminBase = process.argv[3] || 'http://127.0.0.1:17872'

// ---- 注入浏览器全局(mock chrome + WebSocket) ----
// 浏览器 SW 的 WebSocket 会自动带扩展 Origin;Node 'ws' 不带,故子类注入 chrome-extension:// Origin,
// 让脑的 Origin 前缀校验通过(等价真实扩展)。插件代码仍是无参 new WebSocket(url)。
class HarnessWebSocket extends WebSocket {
  constructor(url) {
    super(url, [], { origin: 'chrome-extension://harnesstestaaaaaaaaaaaaaaaaaaaa' })
  }
}
globalThis.WebSocket = HarnessWebSocket

const storage = {}
let manifestVersion = '0.1.0'
globalThis.chrome = {
  storage: {
    local: {
      async get(key) {
        return key in storage ? { [key]: storage[key] } : {}
      },
      async set(o) {
        Object.assign(storage, o)
      },
    },
  },
  runtime: {
    getManifest: () => ({ version: manifestVersion }),
    onStartup: { addListener() {} },
    onInstalled: { addListener() {} },
    onMessage: { addListener() {} },
  },
  alarms: { create() {}, onAlarm: { addListener() {} } },
  windows: { async getCurrent() { return { id: 1 } } },
  tabs: {
    async query() {
      return [
        { id: 10, index: 0, active: true, windowId: 1 },
        { id: 11, index: 1, active: false, windowId: 1 },
      ]
    },
    async update(id, _props) {
      switchedTabId = id
      return { id }
    },
  },
}
let switchedTabId = null

// ---- 打包插件 base(与生产同源)供 Node 导入 ----
mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['test/nodeentry.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/bundle.mjs',
  logLevel: 'error',
})
const { Connection, registerDebugPrimitives } = await import(pathToFileURL(process.cwd() + '/test/dist/bundle.mjs').href)

// ---- admin 辅助 ----
const admin = {
  async post(path, body) {
    const r = await fetch(adminBase + path, { method: 'POST', body: body ? JSON.stringify(body) : undefined })
    return r.json().catch(() => ({}))
  },
  async get(path) {
    const r = await fetch(adminBase + path)
    return r.json()
  },
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
let failures = 0
function check(cond, msg) {
  if (cond) console.log('  PASS', msg)
  else { console.log('  FAIL', msg); failures++ }
}

// ---- 剧本 ----
console.log('storage 无工牌 → 手端应走配对')
storage.infra = { wsUrl }
registerDebugPrimitives()
const conn = new Connection()
await admin.post('/admin/pairing/open')
conn.ensureConnected()

// 等手端 null hello 到达 → 待配对
let pending = null
for (let i = 0; i < 30; i++) {
  await sleep(200)
  const r = await admin.get('/admin/pairing/pending')
  if (r.pending && r.pending.length) { pending = r.pending[0]; break }
}
check(!!pending, '手端发出 null hello,脑侧出现待配对项')
if (!pending) { finish(); }

// 确认配对 → 手端应收 welcome{issued} 并存工牌
const bootId = pending?.bootId
const confirmed = await admin.post('/admin/pairing/confirm', { origin: pending?.origin, bootId })
check(!!confirmed.handId, `脑签发工牌 ${confirmed.handId}`)
await sleep(500)
check(storage.infra?.creds?.handId === confirmed.handId, '手端收 welcome{issued} 并存工牌')

// 在线状态
const health = await admin.get('/admin/hands/health')
const online = (health.hands || []).find((h) => h.handId === confirmed.handId && h.online)
check(!!online, '脑侧 hands/health 显示手在线')
check((online?.caps || []).includes('debug.switchWindow@1'), '能力集含 debug.switchWindow@1')

// 派发三类命令,账本应全 ok
async function dispatchAndWait(name, args) {
  const { msgId } = await admin.post('/admin/cmd', { handId: confirmed.handId, name, args })
  for (let i = 0; i < 25; i++) {
    await sleep(150)
    const led = await admin.get('/admin/ledger')
    const rec = (led.ledger || []).find((r) => r.msgId === msgId)
    if (rec && rec.status === 'ok') return true
    if (rec && rec.status !== 'sent' && rec.status !== 'accepted' && rec.status !== 'queued') return rec.status
  }
  return 'timeout'
}
check((await dispatchAndWait('debug.ping', { echo: 'hi' })) === true, 'debug.ping 账本走到 ok')
check((await dispatchAndWait('debug.switchWindow', {})) === true, 'debug.switchWindow 账本走到 ok')
check(switchedTabId === 11, 'switchWindow 真的激活了下一个标签页(mock tab 11)')
check((await dispatchAndWait('debug.slowEcho', { ms: 50, outcome: 'ok' })) === true, 'debug.slowEcho(ok) 账本走到 ok')

function finish() {
  console.log(failures === 0 ? '\nALL PASS' : `\n${failures} FAIL`)
  process.exit(failures === 0 ? 0 : 1)
}
finish()
