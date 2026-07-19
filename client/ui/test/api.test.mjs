// UI 数据层集成测试:打包 api.ts,对隔离真脑跑关键端点,确认 UI 依赖的契约与认证成立。
import * as esbuild from 'esbuild'
import { spawn, spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..')
const isolatedDir = mkdtempSync(join(tmpdir(), 'recruithelper-ui-test-'))
const brainBin = join(isolatedDir, 'braind')
const port = String(20000 + (process.pid % 1000))
const adminBase = `http://127.0.0.1:${port}`
const built = spawnSync('go', ['build', '-o', brainBin, './client/service'], {
  cwd: repoRoot,
  env: { ...process.env, GOCACHE: process.env.GOCACHE || '/private/tmp/recruithelper-gocache' },
  stdio: 'inherit',
})
if (built.status !== 0) {
  rmSync(isolatedDir, { recursive: true, force: true })
  process.exit(built.status || 1)
}
const brain = spawn(brainBin, ['-port', port, '-data', join(isolatedDir, 'data')], {
  cwd: repoRoot,
  stdio: 'ignore',
})

async function waitHealthy() {
  const deadline = Date.now() + 10_000
  while (Date.now() < deadline) {
    try {
      const response = await fetch(adminBase + '/admin/health')
      if (response.ok && (await response.json()).ok === true) return
    } catch {
      // 服务仍在启动。
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 100))
  }
  throw new Error('隔离脑服务未在期限内就绪')
}

await waitHealthy()

globalThis.window = {
  recruitHelper: { adminBase, adminToken: '' },
  setTimeout: globalThis.setTimeout,
  clearTimeout: globalThis.clearTimeout,
}

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/api.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/api.mjs',
  logLevel: 'error',
})
const apiModuleUrl = pathToFileURL(process.cwd() + '/test/dist/api.mjs').href
const { api } = await import(apiModuleUrl)

let fail = 0
const check = (c, m) => { console.log(c ? '  PASS' : '  FAIL', m); if (!c) fail++ }

const h = await api.health()
check(h.ok === true && typeof h.proto === 'number' && typeof h.contract === 'string', 'health 返回 ok/proto/contract')

const hh = await api.handsHealth()
check(Array.isArray(hh.hands), 'handsHealth 返回 hands 数组')

const led = await api.ledger()
check(Array.isArray(led.ledger), 'ledger 返回数组')

const sus = await api.suspects()
check(Array.isArray(sus.suspects), 'suspects 返回数组')

const acc = await api.accounts()
check(Array.isArray(acc.accounts), 'accounts 返回账号数组')

if (acc.accounts.length > 0) {
  const account = acc.accounts[0]
  const conv = await api.conversations(account.platform, account.accountRef)
  check(Array.isArray(conv.conversations), 'conversations 返回会话数组')

  const aud = await api.audits(account.platform, account.accountRef)
  check(Array.isArray(aud.audits), 'audits 返回审计数组')

  if (conv.conversations.length > 0) {
    const msg = await api.messages(account.platform, account.accountRef, conv.conversations[0].conversationRef)
    check(Array.isArray(msg.messages), 'messages 返回消息数组')
  }
} else {
  console.log('  SKIP conversations/messages/audits（尚无已绑定账号）')
}

// Electron preload 配置与鉴权头不依赖真脑，单独用 fetch 替身验证。
const realFetch = globalThis.fetch
const requests = []
globalThis.window = {
  recruitHelper: { adminBase: 'http://127.0.0.1:18888', adminToken: 'test-memory-token' },
  setTimeout: globalThis.setTimeout,
  clearTimeout: globalThis.clearTimeout,
}
globalThis.localStorage = { getItem: () => 'http://127.0.0.1:19999' }
globalThis.fetch = async (url, init = {}) => {
  requests.push({ url: String(url), headers: new Headers(init.headers), body: init.body })
  if (String(url).includes('/admin/messages')) return new Response('{"messages":[]}', { status: 200 })
  if (String(url).includes('/admin/accounts/bind')) return new Response('{"ok":true}', { status: 200 })
  if (String(url).includes('/admin/frames')) {
    return new Response('data: {"seq":7,"dir":"in","kind":"result","handId":"hand-test","msgId":"msg-test","ts":1}\n\n', {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream' },
    })
  }
  return new Response('{"ok":true,"proto":1,"contract":"test","activeHands":[]}', { status: 200 })
}
const authModuleUrl = apiModuleUrl + `?auth=${Date.now()}`
const { api: authApi, ADMIN_BASE: authBase } = await import(authModuleUrl)
await authApi.health()
await authApi.messages('zhi&lian', 'account ref', 'conversation/ref')
await authApi.bindAccount('platform-from-user', 'hand-test', 'account-test')
const receivedFrames = []
const stopFrames = authApi.subscribeFrames((frame) => receivedFrames.push(frame))
await new Promise((resolve) => setTimeout(resolve, 20))
stopFrames()
check(authBase === 'http://127.0.0.1:18888', 'preload adminBase 优先于 localStorage')
check(requests.every((request) => request.headers.get('Authorization') === 'Bearer test-memory-token'), '所有 fetch 携带 preload Bearer token')
check(requests.every((request) => !request.url.includes('test-memory-token')), 'token 不进入 URL')
check(requests[1].url.includes('platform=zhi%26lian') && requests[1].url.includes('accountRef=account+ref'), 'M2 查询参数经过 URL 编码')
const bindRequest = requests.find((request) => request.url.includes('/admin/accounts/bind'))
const bindBody = JSON.parse(String(bindRequest?.body || '{}'))
check(bindBody.platform === 'platform-from-user' && bindBody.handId === 'hand-test' && bindBody.accountRef === 'account-test', '绑定平台由调用方传入且原样进入账号上下文')
check(receivedFrames.length === 1 && receivedFrames[0].seq === 7, 'fetch SSE 解析协议帧且无需 EventSource')
globalThis.fetch = realFetch
delete globalThis.window
delete globalThis.localStorage

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
brain.kill('SIGTERM')
await new Promise((resolveWait) => brain.once('exit', resolveWait))
rmSync(isolatedDir, { recursive: true, force: true })
process.exit(fail === 0 ? 0 : 1)
