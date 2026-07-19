// Node 端到端验收：启动隔离的真 Go 脑，注入最小 chrome 边界后运行生产
// Connection、Dispatcher 与 M2 program。M2 业务命令只能由绑定/账号 actor 产生；
// /admin/cmd 仅保留里程碑 1 的 debug 回归，不能用于任何 M2 原语。
//
// 默认：node test/run.mjs（自建脑进程、临时 SQLite、随机端口）
// 外部：node test/run.mjs <ws-url> <admin-base> [admin-token]
import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdtempSync, mkdirSync, rmSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { WebSocket } from 'ws'
import * as esbuild from 'esbuild'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(scriptDir, '../..')
const pluginRoot = resolve(scriptDir, '..')
const externalWsURL = process.argv[2]
const externalAdminBase = process.argv[3]
const externalAdminToken = process.argv[4] ?? process.env.RECRUITHELPER_ADMIN_TOKEN ?? ''

const sleep = (ms) => new Promise((done) => setTimeout(done, ms))

async function unusedPort() {
  const server = createServer()
  await new Promise((resolveListen, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolveListen)
  })
  const address = server.address()
  assert.ok(address && typeof address === 'object')
  const port = address.port
  await new Promise((resolveClose, reject) => server.close((error) => error ? reject(error) : resolveClose()))
  return port
}

function appendBounded(current, chunk) {
  const next = current + String(chunk)
  return next.length <= 24_000 ? next : next.slice(next.length - 24_000)
}

async function startBrain() {
  if (externalWsURL || externalAdminBase) {
    assert.ok(externalWsURL && externalAdminBase, '外部模式必须同时给出 ws-url 与 admin-base')
    return {
      wsURL: externalWsURL,
      adminBase: externalAdminBase,
      adminToken: externalAdminToken,
      logs: () => '',
      async stop() {},
    }
  }

  const workDir = mkdtempSync(join(tmpdir(), 'recruithelper-plugin-e2e-'))
  const binary = join(workDir, 'braind')
  const dataDir = join(workDir, 'data')
  const build = spawnSync('go', ['build', '-o', binary, './client/service'], {
    cwd: repoRoot,
    encoding: 'utf8',
    env: {
      ...process.env,
      GOCACHE: process.env.GOCACHE || join(tmpdir(), 'recruithelper-plugin-e2e-gocache'),
    },
  })
  if (build.status !== 0) {
    rmSync(workDir, { recursive: true, force: true })
    throw new Error(`构建脑服务失败\n${build.stdout}\n${build.stderr}`)
  }

  const port = await unusedPort()
  const token = `node-e2e-${process.pid}-${Date.now()}`
  let output = ''
  const child = spawn(binary, [
    '-port', String(port),
    '-data', dataDir,
    '-admin-token', token,
  ], { cwd: repoRoot, stdio: ['ignore', 'pipe', 'pipe'] })
  child.stdout.on('data', (chunk) => { output = appendBounded(output, chunk) })
  child.stderr.on('data', (chunk) => { output = appendBounded(output, chunk) })
  let exited = false
  child.once('exit', () => { exited = true })

  return {
    wsURL: `ws://127.0.0.1:${port}/v1/channel`,
    adminBase: `http://127.0.0.1:${port}`,
    adminToken: token,
    logs: () => output,
    async stop() {
      if (!exited) {
        child.kill('SIGTERM')
        await Promise.race([
          new Promise((resolveExit) => child.once('exit', resolveExit)),
          sleep(6_000).then(() => {
            if (!exited) child.kill('SIGKILL')
          }),
        ])
      }
      rmSync(workDir, { recursive: true, force: true })
    },
  }
}

const brain = await startBrain()
let postPaths = []
let switchedTabId = null
let conn = null

try {
  const admin = {
    async request(method, path, body) {
      const response = await fetch(brain.adminBase + path, {
        method,
        headers: {
          ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
          ...(brain.adminToken ? { Authorization: `Bearer ${brain.adminToken}` } : {}),
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        signal: AbortSignal.timeout(10_000),
      })
      const data = await response.json().catch(() => ({}))
      if (!response.ok) {
        throw new Error(`${method} ${path} -> ${response.status}: ${data.error ?? JSON.stringify(data)}`)
      }
      return data
    },
    get(path) { return this.request('GET', path) },
    post(path, body = {}) {
      postPaths.push(path)
      return this.request('POST', path, body)
    },
  }

  async function eventually(load, accept, message, timeoutMs = 10_000, intervalMs = 100) {
    const deadline = Date.now() + timeoutMs
    let latest
    let latestError
    while (Date.now() < deadline) {
      try {
        latest = await load()
        if (accept(latest)) return latest
      } catch (error) {
        latestError = error
      }
      await sleep(intervalMs)
    }
    const detail = latestError ? latestError.message : JSON.stringify(latest)
    throw new Error(`${message}；最后观测=${detail}`)
  }

  await eventually(
    () => admin.get('/admin/health'),
    (health) => health.ok === true,
    '隔离脑服务未就绪',
    15_000,
  )

  // 浏览器边界 fixture。所有标识、姓名与正文均为合成值，不含真实账号或候选人信息。
  const principalFingerprint = 'ab'.repeat(32)
  const fixtureConversationRef = 'fixture-conversation-001'
  const fixturePeerRef = 'fixture-peer-001'
  const hashText = (text) => createHash('sha256').update(text.normalize('NFC').trim().replace(/\s+/gu, ' ')).digest('hex')
  const fixtureMessages = [
    {
      sourceKey: 'fixture-source-001', direction: 'in', kind: 'text',
      text: '合成历史消息一', blobRef: null, contentHash: hashText('合成历史消息一'),
      cardType: null, cardState: null, tsApprox: Date.now() - 2_000,
    },
    {
      sourceKey: 'fixture-source-002', direction: 'out', kind: 'text',
      text: '合成历史消息二', blobRef: null, contentHash: hashText('合成历史消息二'),
      cardType: null, cardState: null, tsApprox: Date.now() - 1_000,
    },
  ]
  const platform = {
    tab: {
      exists: true,
      id: 10,
      url: 'https://rd6.zhaopin.com/app/im',
      status: 'complete',
      active: true,
      windowId: 1,
      index: 0,
    },
    mainCalls: [],
  }

  class HarnessWebSocket extends WebSocket {
    constructor(url) {
      super(url, [], { origin: 'chrome-extension://harnesstestaaaaaaaaaaaaaaaaaaaa' })
    }
  }
  globalThis.WebSocket = HarnessWebSocket

  const storage = {}
  globalThis.chrome = {
    storage: {
      local: {
        async get(key) {
          if (typeof key === 'string') return key in storage ? { [key]: storage[key] } : {}
          return { ...storage }
        },
        async set(value) { Object.assign(storage, value) },
        async remove(key) {
          for (const item of Array.isArray(key) ? key : [key]) delete storage[item]
        },
      },
    },
    runtime: {
      getManifest: () => ({ version: '0.1.0' }),
      onStartup: { addListener() {} },
      onInstalled: { addListener() {} },
      onMessage: { addListener() {} },
    },
    alarms: { create() {}, onAlarm: { addListener() {} } },
    windows: { async getCurrent() { return { id: 1 } } },
    tabs: {
      async query(queryInfo = {}) {
        if (queryInfo.url) return platform.tab.exists ? [{ ...platform.tab }] : []
        return [
          ...(platform.tab.exists ? [{ ...platform.tab }] : []),
          {
            id: 11,
            url: 'https://example.invalid/',
            status: 'complete',
            active: !platform.tab.active,
            windowId: 1,
            index: 1,
          },
        ]
      },
      async get(id) {
        if (!platform.tab.exists || id !== platform.tab.id) throw new Error('tab not found')
        return { ...platform.tab }
      },
      async create(props) {
        platform.tab.exists = true
        platform.tab.url = props.url
        platform.tab.status = 'complete'
        platform.tab.active = props.active === true
        return { ...platform.tab }
      },
      async update(id, props) {
        if (id === platform.tab.id) {
          if (props.url) platform.tab.url = props.url
          if (props.active !== undefined) {
            platform.tab.active = props.active
            if (props.active) switchedTabId = id
          }
          platform.tab.status = 'complete'
          return { ...platform.tab }
        }
        if (props.active) switchedTabId = id
        return { id, ...props }
      },
      async reload(id) {
        if (id !== platform.tab.id) throw new Error('tab not found')
        platform.tab.status = 'complete'
      },
      async sendMessage(id, message) {
        if (!platform.tab.exists || id !== platform.tab.id) throw new Error('content script absent')
        if (message?.type === 'recruithelper.content.probe') return { ok: true }
        return undefined
      },
    },
    scripting: {
      async executeScript({ target, func, args }) {
        assert.equal(target.tabId, platform.tab.id)
        if (!platform.tab.exists) throw new Error('tab not found')
        const name = func.name
        platform.mainCalls.push(name)
        if (name === 'mainProbeZhilian') {
          return [{ result: {
            pageKind: 'im', loginState: 'in', principalFingerprint,
            imListVisible: true,
          } }]
        }
        if (name === 'mainReadListPage') {
          const [pageNo] = args
          assert.equal(pageNo, 1)
          return [{ result: {
            sessions: [{
              conversationRef: fixtureConversationRef,
              peer: { displayName: '合成候选人', platformUserRef: fixturePeerRef },
              unreadCount: 1,
              lastMessage: { direction: 'in', kind: 'text', textPreview: '合成列表摘要' },
              lastActivityTs: Date.now() - 500,
            }],
            hasMore: false,
            unstable: false,
          } }]
        }
        if (name === 'mainReadThreadPage') {
          const [conversationRef, , cursor] = args
          assert.equal(conversationRef, fixtureConversationRef)
          assert.equal(cursor, null)
          return [{ result: {
            messages: fixtureMessages,
            reachedTop: true,
            cursor: null,
            peer: { displayName: '合成候选人', platformUserRef: fixturePeerRef },
          } }]
        }
        throw new Error(`未实现的 MAIN fixture: ${name}`)
      },
    },
  }

  mkdirSync(join(pluginRoot, 'test/dist'), { recursive: true })
  await esbuild.build({
    entryPoints: [join(pluginRoot, 'test/nodeentry.ts')],
    bundle: true,
    format: 'esm',
    platform: 'neutral',
    outfile: join(pluginRoot, 'test/dist/bundle.mjs'),
    logLevel: 'error',
  })
  const bundleURL = pathToFileURL(join(pluginRoot, 'test/dist/bundle.mjs')).href
  const { Connection, registerDebugPrimitives, registerM2Primitives } = await import(`${bundleURL}?t=${Date.now()}`)

  storage.infra = { wsUrl: brain.wsURL }
  registerDebugPrimitives()
  registerM2Primitives()
  conn = new Connection()

  console.log('本地稳定 handId 与生产 Connection 自动握手')
  conn.ensureConnected()
  const registered = await eventually(
    async () => ({ handId: storage.infra?.handId, health: await admin.get('/admin/hands/health') }),
    ({ handId, health }) => typeof handId === 'string' &&
      health.hands?.some((hand) => hand.handId === handId && hand.online),
    '本地 handId 没有自动登记为在线手',
  )
  const handId = registered.handId
  assert.match(handId, /^hand-[0-9a-f]{24}$/)
  assert.equal(postPaths.length, 0, '自动登记握手不得依赖任何管理端写接口')
  const health = await admin.get('/admin/hands/health')
  const online = health.hands.find((hand) => hand.handId === handId)
  for (const capability of [
    'probe.platform@1', 'nav.ensureSurface@1', 'chat.readList@1', 'chat.readThread@1',
  ]) {
    assert.ok(online.caps.includes(capability), `hello 能力集缺少 ${capability}`)
  }
  console.log('  PASS 稳定 handId、自动登记、M2 能力冻结与在线会话')

  console.log('正式绑定探测与 actor 页面恢复/列表索引')
  const bound = await admin.post('/admin/accounts/bind', {
    platform: 'zhilian',
    handId,
  })
  const accountRef = bound.account.accountRef
  assert.equal(bound.account.identityState, 'verified')

  // 绑定完成后让 canonical 页暂时消失。actor 的第一次 readList 必须失败为 pageAbsent，
  // 随即经同一生产 dispatcher 执行 ensureSurface、probe 与重发 readList。
  platform.tab.exists = false
  await admin.post('/admin/accounts/enable', { platform: 'zhilian', accountRef })
  await admin.post('/admin/accounts/run', { platform: 'zhilian', accountRef })
  const indexed = await eventually(
    async () => {
      const accounts = await admin.get('/admin/accounts')
      const conversations = await admin.get(`/admin/conversations?platform=zhilian&accountRef=${encodeURIComponent(accountRef)}`)
      return {
        account: accounts.accounts.find((item) => item.accountRef === accountRef),
        conversations: conversations.conversations,
      }
    },
    ({ account, conversations }) => account?.latestRound?.status === 'ok' &&
      conversations.some((item) => item.conversationRef === fixtureConversationRef),
    'actor 没有完成恢复与会话索引',
    20_000,
  )
  const firstRoundID = indexed.account.latestRound.roundId
  const indexedConversation = indexed.conversations.find((item) => item.conversationRef === fixtureConversationRef)
  assert.equal(indexedConversation.trackingState, 'untracked')
  console.log('  PASS bind probe 与 actor 的 ensureSurface/probe/readList/索引链路')

  console.log('正式 track 意图、actor readThread 与首次收编')
  const tracking = await admin.post('/admin/conversations/track', {
    platform: 'zhilian', accountRef, conversationRef: fixtureConversationRef,
  })
  assert.equal(tracking.trackingState, 'pending')
  await admin.post('/admin/accounts/run', { platform: 'zhilian', accountRef })

  // 生产最小轮间隔为 60 秒。这里等待真实 actor 调度，不改时钟、不写 SQLite、
  // 不增设测试配置，从而保持测试和生产是同一条构造路径。
  const adopted = await eventually(
    async () => {
      const accounts = await admin.get('/admin/accounts')
      const conversations = await admin.get(`/admin/conversations?platform=zhilian&accountRef=${encodeURIComponent(accountRef)}`)
      return {
        account: accounts.accounts.find((item) => item.accountRef === accountRef),
        conversation: conversations.conversations.find((item) => item.conversationRef === fixtureConversationRef),
      }
    },
    ({ account, conversation }) => account?.latestRound?.roundId !== firstRoundID &&
      account?.latestRound?.status === 'ok' && conversation?.trackingState === 'adopted',
    '最小轮间隔后没有完成首次收编',
    75_000,
    250,
  )
  assert.equal(adopted.conversation.adoptedBoundarySeq, fixtureMessages.length)
  assert.equal(adopted.conversation.lastMessageSeq, fixtureMessages.length)

  const messages = await admin.get(
    `/admin/messages?platform=zhilian&accountRef=${encodeURIComponent(accountRef)}` +
    `&conversationRef=${encodeURIComponent(fixtureConversationRef)}`,
  )
  assert.equal(messages.messages.length, fixtureMessages.length)
  assert.deepEqual(messages.messages.map((message) => message.text), fixtureMessages.map((message) => message.text))
  assert.equal(adopted.account.latestRound.newMessageCount, 0, '首次收编历史不得投影成新消息')

  const audits = await admin.get(`/admin/audits?platform=zhilian&accountRef=${encodeURIComponent(accountRef)}`)
  assert.ok(audits.audits.some((entry) => entry.category === 'conversation_adopted'))
  console.log('  PASS readThread 快照入账、adopted 边界与零历史投影')

  const ledger = await admin.get('/admin/ledger')
  const m2Names = ['probe.platform', 'nav.ensureSurface', 'chat.readList', 'chat.readThread']
  for (const name of m2Names) {
    assert.ok(ledger.ledger.some((record) => record.name === name && record.status === 'ok'),
      `${name} 没有经生产 dispatcher 到达 ok`)
  }
  assert.ok(ledger.ledger.some((record) =>
    record.name === 'chat.readList' && record.status === 'failed' && record.errorCode === 'CTX_NOT_READY'),
  '页面缺失没有留下响亮失败账本')
  assert.equal(postPaths.filter((path) => path === '/admin/cmd').length, 0,
    'M2 链路不得使用 /admin/cmd 旁路')
  for (const expectedCall of ['mainProbeZhilian', 'mainReadListPage', 'mainReadThreadPage']) {
    assert.ok(platform.mainCalls.includes(expectedCall), `生产平台接缝没有执行 ${expectedCall}`)
  }
  console.log('  PASS 四条 M2 原语均由 actor/绑定流经生产 dispatcher 与真 WS')

  // 保留原有里程碑 1 debug E2E。只有以下回归使用 /admin/cmd。
  async function dispatchDebugAndWait(name, args) {
    const { msgId } = await admin.post('/admin/cmd', { handId, name, args })
    const state = await eventually(
      () => admin.get('/admin/ledger'),
      (view) => view.ledger.some((record) => record.msgId === msgId && record.status === 'ok'),
      `${name} 没有到达 ok`,
    )
    return state.ledger.find((record) => record.msgId === msgId)
  }
  await dispatchDebugAndWait('debug.ping', { echo: 'node-e2e' })
  await dispatchDebugAndWait('debug.switchWindow', {})
  assert.equal(switchedTabId, platform.tab.id)
  await dispatchDebugAndWait('debug.slowEcho', { ms: 25, outcome: 'ok' })
  console.log('  PASS 原有 debug 真 WS 回归')

  console.log('\nALL PASS')
} catch (error) {
  console.error('\nNODE E2E FAILED')
  console.error(error?.stack ?? error)
  const logs = brain.logs()
  if (logs) console.error(`\n脑服务末尾日志：\n${logs}`)
  process.exitCode = 1
} finally {
  await brain.stop()
}

// Connection 有生产心跳/重连定时器；进程级测试在完成脑服务清理后显式退出。
process.exit(process.exitCode ?? 0)
