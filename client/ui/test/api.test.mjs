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
let forceSendConflict = false
let forceSendRejected = false
let forceGreetingConflict = false
let forceGreetingRejected = false
let forceCandidateReadError = false
let forceCandidateSelectError = false
globalThis.window = {
  recruitHelper: { adminBase: 'http://127.0.0.1:18888', adminToken: 'test-memory-token' },
  setTimeout: globalThis.setTimeout,
  clearTimeout: globalThis.clearTimeout,
}
globalThis.localStorage = { getItem: () => 'http://127.0.0.1:19999' }
globalThis.fetch = async (url, init = {}) => {
  requests.push({ url: String(url), headers: new Headers(init.headers), body: init.body })
  if (String(url).includes('/admin/messages/send')) {
    if (forceSendRejected && init.method === 'POST') {
      return new Response(JSON.stringify({ error: '账号身份未在当前手会话验证' }), { status: 409 })
    }
    if (forceSendConflict && init.method === 'POST') {
      return new Response(JSON.stringify({
        error: '发送账本已更新',
        current: {
          intentId: 'intent-current', logicalDispatchId: 'logical-current', msgId: 'msg-current',
          status: 'ok', commandStatus: 'ok', verificationAttempts: 0,
        },
      }), { status: 409 })
    }
    return new Response(JSON.stringify({
      intentId: 'intent-stable', logicalDispatchId: 'logical-stable', msgId: 'msg-stable',
      status: 'queued', commandStatus: 'sent', verificationAttempts: 0,
    }), { status: init.method === 'POST' ? 202 : 200 })
  }
  if (String(url).includes('/admin/candidates/greeting/send')) {
    if (forceGreetingRejected && init.method === 'POST') {
      return new Response(JSON.stringify({ error: '候选人档案当前不能发送招呼' }), { status: 409 })
    }
    if (forceGreetingConflict && init.method === 'POST') {
      return new Response(JSON.stringify({
        error: '招呼账本已更新',
        current: {
          intentId: 'greeting-current', logicalDispatchId: 'logical-greeting-current', msgId: 'msg-greeting-current',
          status: 'queued', commandStatus: 'sent', verificationAttempts: 0,
        },
      }), { status: 409 })
    }
    return new Response(JSON.stringify({
      intentId: 'greeting-stable', logicalDispatchId: 'logical-greeting-stable', msgId: 'msg-greeting-stable',
      status: 'queued', commandStatus: 'sent', verificationAttempts: 0,
    }), { status: init.method === 'POST' ? 202 : 200 })
  }
  if (String(url).includes('/admin/candidates/current/read')) {
    if (forceCandidateReadError) {
      return new Response(JSON.stringify({ error: 'raw-user-secret-from-brain' }), { status: 500 })
    }
    return new Response(JSON.stringify({
      selectionRef: 'selection-safe', displayName: '候选人甲', positionTitle: '前端工程师',
      contactState: 'unestablished', platformUserRef: 'raw-user-must-not-escape', positionRef: 'raw-job-must-not-escape',
    }), { status: 200 })
  }
  if (String(url).includes('/admin/candidates/current/select')) {
    if (forceCandidateSelectError) {
      return new Response(JSON.stringify({ error: 'raw-selection-diagnostic' }), { status: 409 })
    }
    return new Response(JSON.stringify({
      profileId: 'profile-safe', status: 'selected', created: true, platformUserRef: 'raw-user-must-not-escape',
    }), { status: 200 })
  }
  if (String(url).includes('/admin/messages')) return new Response('{"messages":[]}', { status: 200 })
  if (String(url).includes('/admin/hands/reload')) return new Response(JSON.stringify({
    ready: true, handId: 'hand-test', msgId: 'reload-msg', previousBootId: 'boot-old',
    bootId: 'boot-new', contractHash: 'sha256:new', extensionVersion: '0.1.0',
  }), { status: 200 })
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
const {
  api: authApi, ADMIN_BASE: authBase, CANDIDATE_READ_ERROR, CANDIDATE_SELECT_ERROR,
  SendIntentConflictError, SendIntentRejectedError,
} = await import(authModuleUrl)
await authApi.health()
await authApi.messages('zhi&lian', 'account ref', 'conversation/ref')
await authApi.bindAccount('platform-from-user', 'hand-test', 'account-test')
const reloadReady = await authApi.reloadHand('hand-test')
const candidatePreview = await authApi.readCurrentCandidate({ platform: 'zhilian', accountRef: 'account-test' })
const candidateProfile = await authApi.selectCurrentCandidate(candidatePreview.selectionRef)
const greetingCreated = await authApi.sendGreeting('greeting-stable', 'greeting-before', 'profile-safe', '你好')
const greetingStatus = await authApi.greetingStatus('greeting-stable')
const greetingLatest = await authApi.latestGreetingIntent('profile-safe')
forceGreetingConflict = true
let greetingConflictCurrent = null
try {
  await authApi.sendGreeting('greeting-racing', 'greeting-before', 'profile-safe', '你好')
} catch (reason) {
  if (reason instanceof SendIntentConflictError) greetingConflictCurrent = reason.current
}
forceGreetingConflict = false
forceGreetingRejected = true
let greetingRejectedBeforeCreate = false
try {
  await authApi.sendGreeting('greeting-rejected', 'greeting-before', 'profile-safe', '你好')
} catch (reason) {
  greetingRejectedBeforeCreate = reason instanceof SendIntentRejectedError
}
forceGreetingRejected = false
const sendCreated = await authApi.sendMessage('intent-stable', 'intent-before', 'zhilian', 'account-test', 'conversation-test', '你好')
const sendStatus = await authApi.sendStatus('intent-stable')
const sendLatest = await authApi.latestSendIntent('zhilian', 'account-test', 'conversation-test')
forceSendConflict = true
let conflictCurrent = null
try {
  await authApi.sendMessage('intent-racing', 'intent-before', 'zhilian', 'account-test', 'conversation-test', '第二条')
} catch (reason) {
  if (reason instanceof SendIntentConflictError) conflictCurrent = reason.current
}
forceSendConflict = false
forceSendRejected = true
let rejectedBeforeCreate = false
try {
  await authApi.sendMessage('intent-rejected', 'intent-before', 'zhilian', 'account-test', 'conversation-test', '你好')
} catch (reason) {
  rejectedBeforeCreate = reason instanceof SendIntentRejectedError
}
forceSendRejected = false
forceCandidateReadError = true
let candidateReadError = ''
try { await authApi.readCurrentCandidate({ platform: 'zhilian', accountRef: 'account-test' }) } catch (reason) {
  candidateReadError = reason instanceof Error ? reason.message : String(reason)
}
forceCandidateReadError = false
forceCandidateSelectError = true
let candidateSelectError = ''
try { await authApi.selectCurrentCandidate('selection-safe') } catch (reason) {
  candidateSelectError = reason instanceof Error ? reason.message : String(reason)
}
forceCandidateSelectError = false
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
const reloadRequest = requests.find((request) => request.url.includes('/admin/hands/reload'))
check(JSON.parse(String(reloadRequest?.body || '{}')).handId === 'hand-test' && reloadReady.bootId === 'boot-new', '一键重载携带目标手并返回新 boot 就绪证词')
const candidateReadRequest = requests.find((request) => request.url.includes('/admin/candidates/current/read'))
const candidateReadBody = JSON.parse(String(candidateReadRequest?.body || '{}'))
check(Object.keys(candidateReadBody).sort().join(',') === 'accountRef,platform' && candidateReadBody.accountRef === 'account-test', '候选人读取只发送账号上下文')
check(Object.keys(candidatePreview).sort().join(',') === 'contactState,displayName,positionTitle,selectionRef' && !JSON.stringify(candidatePreview).includes('raw-user'), '候选人预览归一化为安全最小 DTO，原始平台引用不进入 UI')
const candidateSelectRequest = requests.find((request) => request.url.includes('/admin/candidates/current/select'))
const candidateSelectBody = JSON.parse(String(candidateSelectRequest?.body || '{}'))
check(Object.keys(candidateSelectBody).join(',') === 'selectionRef' && candidateSelectBody.selectionRef === 'selection-safe', '人工确认只回传本次 selectionRef')
check(Object.keys(candidateProfile).sort().join(',') === 'created,profileId,status' && !JSON.stringify(candidateProfile).includes('raw-user'), '建档响应归一化为安全档案视图')
check(candidateReadError === CANDIDATE_READ_ERROR && !candidateReadError.includes('raw-user-secret'), '候选人读取失败使用固定安全文案')
check(candidateSelectError === CANDIDATE_SELECT_ERROR && !candidateSelectError.includes('raw-selection'), '候选人确认失败使用固定安全文案')
const greetingRequest = requests.find((request) => request.url.endsWith('/admin/candidates/greeting/send') && request.body)
const greetingBody = JSON.parse(String(greetingRequest?.body || '{}'))
check(
  Object.keys(greetingBody).sort().join(',') === 'intentId,previousIntentId,profileId,text'
    && greetingBody.intentId === 'greeting-stable'
    && greetingBody.previousIntentId === 'greeting-before'
    && greetingBody.profileId === 'profile-safe'
    && greetingBody.text === '你好',
  '招呼请求只携带稳定意图、前序 CAS、脑内 profileId 与明确正文，不接触平台原始引用',
)
check(greetingCreated.intentId === 'greeting-stable' && greetingStatus.commandStatus === 'sent' && greetingLatest?.intentId === 'greeting-stable', '招呼创建、按 ID 查询与按档案 current 恢复沿用同一意图视图')
check(requests.some((request) => request.url.includes('/admin/candidates/greeting/send?intentId=greeting-stable')), '招呼状态按 intentId 查询且不携带正文')
check(requests.some((request) => request.url.includes('/admin/candidates/greeting/send?profileId=profile-safe')), '招呼前可按 profileId 收编 current')
check(greetingConflictCurrent?.intentId === 'greeting-current', '招呼 CAS 冲突保留脑返回的 current 供 UI 收编')
check(greetingRejectedBeforeCreate, '招呼无 current 的安全拒绝被标记为确定性未创建')
const sendRequest = requests.find((request) => request.url.endsWith('/admin/messages/send') && request.body)
const sendBody = JSON.parse(String(sendRequest?.body || '{}'))
check(sendBody.intentId === 'intent-stable' && sendBody.previousIntentId === 'intent-before' && sendBody.text === '你好' && sendBody.conversationRef === 'conversation-test', '发送请求携带稳定 intentId、前序 CAS 与明确会话，不由 API 层重铸意图')
check(sendCreated.intentId === 'intent-stable' && sendStatus.commandStatus === 'sent' && sendLatest?.intentId === 'intent-stable', '发送创建、按 ID 查询与按会话恢复使用同一意图视图')
check(requests.some((request) => request.url.includes('/admin/messages/send?intentId=intent-stable')), '发送状态查询对 intentId 做 URL 编码并且不携带正文')
check(requests.some((request) => request.url.includes('/admin/messages/send?platform=zhilian') && request.url.includes('conversationRef=conversation-test')), '页面恢复按账号与会话查询脑侧 latest')
check(conflictCurrent?.intentId === 'intent-current', 'CAS 409 保留脑返回的 current，UI 可立即收编并锁定')
check(rejectedBeforeCreate, '无 current 的安全拒绝被标记为确定性未创建，UI 可解除当前内存 pending')
check(receivedFrames.length === 1 && receivedFrames[0].seq === 7, 'fetch SSE 解析协议帧且无需 EventSource')
globalThis.fetch = realFetch
delete globalThis.window
delete globalThis.localStorage

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
brain.kill('SIGTERM')
await new Promise((resolveWait) => brain.once('exit', resolveWait))
rmSync(isolatedDir, { recursive: true, force: true })
process.exit(fail === 0 ? 0 : 1)
