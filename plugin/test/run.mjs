// Node 端到端验收：启动隔离的真 Go 脑，注入最小 chrome 边界后运行生产
// Connection、Dispatcher 与生产 program。M2 业务命令只由绑定/账号 actor 产生，
// M3/M4 真实 SX 只由正式产品入口产生；/admin/cmd 仅保留里程碑 1 的
// debug 回归，不能用于任何 M2/M3/M4 原语。
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

// 生产脑坚持“本地 08:00 后由真人开启”的日闸。E2E 只调整隔离子进程的
// IANA 时区，使运行时落在稳定日间；不改生产时钟，也不增加测试分支。
function daytimeTestZone(now = new Date()) {
  const zones = ['Etc/GMT+12', 'Etc/GMT+8', 'Etc/GMT', 'Etc/GMT-8', 'Etc/GMT-12']
  for (const zone of zones) {
    const hour = Number(new Intl.DateTimeFormat('en-US', {
      timeZone: zone,
      hour: '2-digit',
      hourCycle: 'h23',
    }).format(now))
    if (hour >= 8 && hour <= 22) return zone
  }
  throw new Error('无法为隔离脑选择稳定日间时区')
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
  ], {
    cwd: repoRoot,
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, TZ: daytimeTestZone() },
  })
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
  const fixtureSendText = '合成端到端问候'
  const fixtureCandidateRef = 'fixture-candidate-m4-001'
  const fixturePositionRef = 'fixture-position-m4-001'
  const fixtureGreetingConversationRef = 'fixture-greeting-conversation-001'
  const fixtureGreetingText = '合成主动建联正文'
  const hashText = (text) => createHash('sha256').update(text.normalize('NFC').trim().replace(/\s+/gu, ' ')).digest('hex')
  const fixtureTargetBindingToken = createHash('sha256')
    .update(JSON.stringify([fixtureConversationRef, fixturePeerRef]))
    .digest('hex')
  const fixtureMessages = [
    {
      sourceKey: hashText('fixture-source-001'), direction: 'in', kind: 'text',
      text: '合成历史消息一', blobRef: null, contentHash: hashText('合成历史消息一'),
      cardType: null, cardState: null, tsApprox: Date.now() - 2_000,
    },
    {
      sourceKey: hashText('fixture-source-002'), direction: 'out', kind: 'text',
      text: '合成历史消息二', blobRef: null, contentHash: hashText('合成历史消息二'),
      cardType: null, cardState: null, tsApprox: Date.now() - 1_000,
    },
  ]
  const fixtureGreetingMessages = [{
    sourceKey: hashText('fixture-source-greeting-001'), direction: 'out', kind: 'text',
    text: fixtureGreetingText, blobRef: null, contentHash: hashText(fixtureGreetingText),
    cardType: null, cardState: null, tsApprox: Date.now(),
  }]
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
    sendClickCount: 0,
    sendEvaluationPhases: [],
    directThreadRouteUpdates: 0,
    sendServerMessageCreated: false,
    sendBaselineSourceKeys: [],
    greetingActionPhases: [],
    greetingClickCount: 0,
    greetingServerMessageCreated: false,
    greetingRelationshipReads: 0,
    resumeReadCount: 0,
  }

  const harnessSockets = []
  const resultAckFault = {
    armed: false,
    dropped: false,
    targetResultMsgId: null,
    resultEnvelopes: [],
    resultAcks: [],
  }
  const witnessTrace = { outboxWritten: false }
  class HarnessWebSocket extends WebSocket {
    constructor(url) {
      super(url, [], { origin: 'chrome-extension://harnesstestaaaaaaaaaaaaaaaaaaaa' })
      harnessSockets.push(this)
    }

    send(data, ...args) {
      if (resultAckFault.armed) {
        try {
          const envelope = JSON.parse(String(data))
          const isTargetSuccess = envelope.kind === 'result' && envelope.body?.status === 'ok' &&
            envelope.body?.data?.conversationRef === fixtureConversationRef &&
            envelope.body?.data?.contentHash === hashText(fixtureSendText) &&
            envelope.body?.evidence?.some((item) => item?.type === 'outboundMessageObserved')
          if (isTargetSuccess) {
            if (resultAckFault.targetResultMsgId === null) {
              resultAckFault.targetResultMsgId = envelope.msgId
            }
            if (envelope.msgId === resultAckFault.targetResultMsgId) {
              resultAckFault.resultEnvelopes.push(structuredClone(envelope))
            }
          }
        } catch {
          // 非协议帧仍由真实 ws 实现处理。
        }
      }
      return super.send(data, ...args)
    }

    emit(event, ...args) {
      if (event === 'message' && resultAckFault.armed && resultAckFault.targetResultMsgId !== null) {
        try {
          const envelope = JSON.parse(String(args[0]))
          if (envelope.kind === 'ack' && envelope.body?.ref === resultAckFault.targetResultMsgId) {
            resultAckFault.resultAcks.push(structuredClone(envelope))
            if (!resultAckFault.dropped) {
              resultAckFault.dropped = true
              queueMicrotask(() => this.terminate())
              return false
            }
          }
        } catch {
          // 非协议帧继续透传。
        }
      }
      return super.emit(event, ...args)
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
        async set(value) {
          if (Object.keys(value).some((key) => key.startsWith('outbox:'))) {
            witnessTrace.outboxWritten = true
          }
          Object.assign(storage, value)
        },
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
          if (props.url) {
            if (String(props.url).includes('sessionId=')) platform.directThreadRouteUpdates += 1
            platform.tab.url = props.url
          }
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
      async executeScript({ target, world, func, args }) {
        assert.equal(target.tabId, platform.tab.id)
        assert.equal(world, 'MAIN')
        if (!platform.tab.exists) throw new Error('tab not found')
        const name = func.name
        platform.mainCalls.push(name)
        if (name === 'mainProbeZhilian') {
          const pageKind = platform.tab.url.includes('/app/recommend') ? 'recommend' : 'im'
          return [{ result: {
            pageKind, loginState: 'in', principalFingerprint,
            imListVisible: pageKind === 'im',
          } }]
        }
        if (name === 'mainReadCurrentCandidate') {
          if (platform.greetingServerMessageCreated) platform.greetingRelationshipReads += 1
          return [{ result: {
            status: 'ready',
            data: {
              platformUserRef: fixtureCandidateRef,
              displayName: '合成建联候选人',
              positionRef: fixturePositionRef,
              positionTitle: '合成建联职位',
              contactState: platform.greetingServerMessageCreated ? 'established' : 'unestablished',
            },
          } }]
        }
        if (name === 'mainReadCurrentResume') {
          assert.deepEqual(args, [fixtureGreetingConversationRef, fixtureCandidateRef])
          platform.resumeReadCount += 1
          return [{ result: {
            status: 'ready',
            data: {
              conversationRef: fixtureGreetingConversationRef,
              platformUserRef: fixtureCandidateRef,
              observedAt: Date.now(),
              basic: [{ label: '合成基本项', value: '合成基本值' }],
              expectations: [{ label: '合成期望项', value: '合成期望值' }],
              selfEvaluation: '',
              education: '合成教育经历',
              workExperiences: '合成工作经历',
            },
          } }]
        }
        if (name === 'mainSendGreetingOnce') {
          assert.equal(args.length, 7)
          const [
            platformUserRef,
            positionRef,
            text,
            expectedPrincipalFingerprint,
            irreversibleNotAfterMs,
            expectedOwnedDraft,
            phase,
          ] = args
          assert.equal(platformUserRef, fixtureCandidateRef)
          assert.equal(positionRef, fixturePositionRef)
          assert.equal(text, fixtureGreetingText)
          assert.equal(expectedPrincipalFingerprint, principalFingerprint)
          assert.ok(Number.isFinite(irreversibleNotAfterMs) && irreversibleNotAfterMs >= Date.now())
          platform.greetingActionPhases.push(phase)
          if (phase === 'prepare') {
            assert.equal(expectedOwnedDraft, '')
            return [{ result: { status: 'prepared' } }]
          }
          assert.equal(expectedOwnedDraft, fixtureGreetingText)
          if (phase === 'preflight') return [{ result: { status: 'ready' } }]
          assert.equal(phase, 'commit')
          platform.greetingClickCount += 1
          platform.greetingServerMessageCreated = true
          return [{ result: { status: 'clicked' } }]
        }
        if (name === 'mainReadListPage') {
          const [pageNo] = args
          assert.equal(pageNo, 1)
          const sessions = [{
            conversationRef: fixtureConversationRef,
            peer: { displayName: '合成候选人', platformUserRef: fixturePeerRef },
            unreadCount: 1,
            lastMessage: { direction: 'in', kind: 'text', textPreview: '合成列表摘要' },
            lastActivityTs: Date.now() - 500,
          }]
          if (platform.greetingServerMessageCreated) {
            sessions.push({
              conversationRef: fixtureGreetingConversationRef,
              peer: { displayName: '合成建联候选人', platformUserRef: fixtureCandidateRef },
              unreadCount: 0,
              lastMessage: { direction: 'out', kind: 'text', textPreview: fixtureGreetingText },
              lastActivityTs: Date.now(),
            })
          }
          return [{ result: {
            sessions,
            hasMore: false,
            unstable: false,
          } }]
        }
        if (name === 'mainFindConversation') {
          assert.equal(args.length, 1)
          assert.ok([fixtureConversationRef, fixtureGreetingConversationRef].includes(args[0]))
          return [{ result: { status: 'found' } }]
        }
        if (name === 'mainClickConversationOnce') {
          const [
            conversationRef,
            expectedCurrentConversationRef,
            expectedPrincipalFingerprint,
            notAfterMs,
          ] = args
          assert.ok([fixtureConversationRef, fixtureGreetingConversationRef].includes(conversationRef))
          const currentConversationRef = new URL(platform.tab.url).searchParams.get('sessionId') ?? ''
          assert.equal(expectedCurrentConversationRef, currentConversationRef)
          assert.equal(expectedPrincipalFingerprint, principalFingerprint)
          assert.ok(Number.isFinite(notAfterMs) && notAfterMs >= Date.now())
          platform.tab.url = `https://rd6.zhaopin.com/app/im?sessionId=${encodeURIComponent(conversationRef)}`
          platform.tab.status = 'complete'
          return [{ result: { status: 'clicked' } }]
        }
        if (name === 'mainReadThreadPage') {
          const [conversationRef, , cursor] = args
          assert.ok([fixtureConversationRef, fixtureGreetingConversationRef].includes(conversationRef))
          assert.equal(cursor, null)
          const greetingConversation = conversationRef === fixtureGreetingConversationRef
          return [{ result: {
            messages: greetingConversation ? fixtureGreetingMessages : fixtureMessages,
            reachedTop: true,
            cursor: null,
            peer: greetingConversation
              ? { displayName: '合成建联候选人', platformUserRef: fixtureCandidateRef }
              : { displayName: '合成候选人', platformUserRef: fixturePeerRef },
          } }]
        }
        if (name === 'mainInspectSendSurface') {
          const [conversationRef] = args
          assert.equal(conversationRef, fixtureConversationRef)
          return [{ result: {
            selected: true,
            composerBindingResolved: true,
            composerBindingMatched: true,
            composerCount: 1,
            composerValue: '',
            sendButtonCount: 1,
            diagnosticStage: 'ok',
          } }]
        }
        if (name === 'mainInspectSendTimeline') {
          const [conversationRef] = args
          assert.equal(conversationRef, fixtureConversationRef)
          return [{ result: true }]
        }
        if (name === 'mainCaptureSendBaseline') {
          const [conversationRef, expectedTail] = args
          assert.equal(conversationRef, fixtureConversationRef)
          assert.deepEqual(expectedTail,
            fixtureMessages.map(({ direction, contentHash }) => ({ direction, contentHash })))
          platform.sendBaselineSourceKeys = fixtureMessages.map((message) => message.sourceKey)
          return [{ result: {
            status: 'ready',
            stage: 'ready',
            serverSourceKeys: [...platform.sendBaselineSourceKeys],
            targetBindingToken: fixtureTargetBindingToken,
          } }]
        }
        if (name === 'mainSendMessageOnce') {
          assert.equal(args.length, 8)
          const [
            conversationRef,
            text,
            expectedTail,
            expectedPrincipalFingerprint,
            irreversibleNotAfterMs,
            expectedBaselineServerSourceKeys,
            expectedTargetBindingToken,
            phase,
          ] = args
          assert.equal(conversationRef, fixtureConversationRef)
          assert.equal(text, fixtureSendText)
          assert.deepEqual(expectedTail, fixtureMessages.map(({ direction, contentHash }) => ({ direction, contentHash })))
          assert.equal(expectedPrincipalFingerprint, principalFingerprint)
          assert.ok(Number.isFinite(irreversibleNotAfterMs) && irreversibleNotAfterMs >= Date.now())
          assert.deepEqual(expectedBaselineServerSourceKeys, platform.sendBaselineSourceKeys)
          assert.equal(expectedTargetBindingToken, fixtureTargetBindingToken)
          platform.sendEvaluationPhases.push(phase)
          if (phase === 'preflight') return [{ result: { status: 'ready' } }]
          assert.equal(phase, 'commit')
          platform.sendClickCount += 1
          if (!platform.sendServerMessageCreated) {
            platform.sendServerMessageCreated = true
            fixtureMessages.push({
              sourceKey: hashText('fixture-source-send-001'), direction: 'out', kind: 'text',
              text: fixtureSendText, blobRef: null, contentHash: hashText(fixtureSendText),
              cardType: null, cardState: null, tsApprox: Date.now(),
            })
          }
          return [{ result: { status: 'clicked' } }]
        }
        if (name === 'mainObserveStableOutbound') {
          assert.equal(args.length, 4)
          const [conversationRef, textHash, baselineServerSourceKeys, expectedTargetBindingToken] = args
          assert.equal(conversationRef, fixtureConversationRef)
          assert.deepEqual(baselineServerSourceKeys, platform.sendBaselineSourceKeys)
          assert.equal(expectedTargetBindingToken, fixtureTargetBindingToken)
          const currentSourceKeys = fixtureMessages.map((message) => message.sourceKey)
          const strictlyAppended = currentSourceKeys.length === baselineServerSourceKeys.length + 1 &&
            baselineServerSourceKeys.every((key, index) => currentSourceKeys[index] === key) &&
            !baselineServerSourceKeys.includes(currentSourceKeys.at(-1))
          const appendedMessage = strictlyAppended ? fixtureMessages.at(-1) : null
          return [{ result: {
            selected: true,
            matchingNewServerMessages: platform.sendServerMessageCreated && strictlyAppended &&
              appendedMessage?.direction === 'out' && appendedMessage?.kind === 'text' &&
              appendedMessage?.contentHash === textHash && textHash === hashText(fixtureSendText) ? 1 : 0,
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
  const {
    Connection,
    registerDebugPrimitives,
    registerM2Primitives,
    registerM3Primitives,
    registerM4Primitives,
    registerM5Primitives,
    registerM6Primitives,
  } = await import(`${bundleURL}?t=${Date.now()}`)

  storage.infra = { wsUrl: brain.wsURL }
  registerDebugPrimitives()
  registerM2Primitives()
  registerM3Primitives()
  registerM4Primitives()
  registerM5Primitives()
  registerM6Primitives()
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
    'debug.inspectSendSurface@1',
    'probe.platform@1', 'nav.ensureSurface@1', 'chat.readList@1', 'chat.readThread@1',
    'candidate.readCurrent@1',
    'candidate.readResume@1',
    'candidate.readSourcingResume@1',
    'chat.readGreetingOutcome@1',
    'chat.sendMessage@1',
    'chat.sendGreeting@1',
  ]) {
    assert.ok(online.caps.includes(capability), `hello 能力集缺少 ${capability}`)
  }
  console.log('  PASS 稳定 handId、自动登记、M2/M3/M4/M5 能力集及在线会话')

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
  for (const expectedCall of [
    'mainProbeZhilian', 'mainReadListPage', 'mainFindConversation',
    'mainClickConversationOnce', 'mainReadThreadPage',
  ]) {
    assert.ok(platform.mainCalls.includes(expectedCall), `生产平台接缝没有执行 ${expectedCall}`)
  }
  console.log('  PASS 四条 M2 原语均由 actor/绑定流经生产 dispatcher 与真 WS')

  console.log('正式产品发送入口、持久 witness/outbox 与重连不双发')
  const baselineLedgerMessages = messages.messages.length
  const intentId = 'fixture-intent-send-001'
  const sessionBeforeSend = conn.status().session
  const socketsBeforeSend = harnessSockets.length
  const routeFindCallsBeforeSend = platform.mainCalls.filter(
    (name) => name === 'mainFindConversation',
  ).length
  const routeClickCallsBeforeSend = platform.mainCalls.filter(
    (name) => name === 'mainClickConversationOnce',
  ).length
  // 模拟真人已在 Chrome 中打开目标会话。生产发送不得自行切换会话。
  platform.tab.url = `https://rd6.zhaopin.com/app/im?sessionId=${encodeURIComponent(fixtureConversationRef)}`
  resultAckFault.armed = true
  const createdSend = await admin.post('/admin/messages/send', {
    intentId,
    previousIntentId: '',
    platform: 'zhilian',
    accountRef,
    conversationRef: fixtureConversationRef,
    text: fixtureSendText,
  })
  assert.equal(createdSend.intentId, intentId)
  const completedSend = await eventually(
    () => admin.get(`/admin/messages/send?intentId=${encodeURIComponent(intentId)}`),
    (view) => view.status === 'ok' && view.commandStatus === 'ok',
    '正式 M3 发送没有到达 ok',
    15_000,
  )
  assert.equal(completedSend.intentId, intentId)
  assert.equal(platform.sendClickCount, 1, '正式发送命令必须只越过一次不可逆点击')
  assert.deepEqual(platform.sendEvaluationPhases, ['preflight', 'commit'],
    '生产预检与 commit 必须使用字面同一 MAIN evaluator，且各执行一次')
  assert.equal(platform.directThreadRouteUpdates, 0, '正式发送不得回退到已知不可靠的 tabs.update 深链')
  await eventually(
    () => ({
      dropped: resultAckFault.dropped,
      targetResultMsgId: resultAckFault.targetResultMsgId,
      resultCount: resultAckFault.resultEnvelopes.length,
    }),
    ({ dropped, targetResultMsgId, resultCount }) => dropped && targetResultMsgId !== null && resultCount >= 1,
    '测试传输层没有捕获目标 M3 成功 result 并丢弃首次 ack',
  )
  assert.equal(resultAckFault.dropped, true, '测试传输层没有丢弃首次 result ack')
  assert.equal(witnessTrace.outboxWritten, true, '真实 SX result 未先写入持久 outbox')
  const journalKeys = Object.keys(storage).filter((key) => key.startsWith('journal:'))
  assert.equal(journalKeys.length, 1)
  assert.equal(storage[journalKeys[0]].state, 'committed')
  assert.equal(journalKeys.filter((key) => storage[key].state === 'attempting').length, 0)
  assert.equal(storage['witness:meta'].journalCount, 1)

  await eventually(
    () => ({
      status: conn.status(),
      sockets: harnessSockets.length,
      resultCount: resultAckFault.resultEnvelopes.length,
      ackCount: resultAckFault.resultAcks.length,
      keys: Object.keys(storage),
    }),
    ({ status, sockets, resultCount, ackCount, keys }) => status.phase === 'session' && status.session !== null &&
      status.session !== sessionBeforeSend && sockets > socketsBeforeSend && resultCount >= 2 && ackCount >= 2 &&
      status.pendingResults === 0 && !keys.some((key) => key.startsWith('outbox:')),
    '首次 result ack 丢失后没有重连补投并清理 outbox',
    15_000,
  )
  const [firstResult, replayedResult] = resultAckFault.resultEnvelopes
  assert.equal(replayedResult.msgId, firstResult.msgId)
  assert.deepEqual(replayedResult.body, firstResult.body)
  assert.equal(replayedResult.attempt, firstResult.attempt + 1)
  assert.notEqual(replayedResult.session, firstResult.session)
  assert.equal(resultAckFault.resultAcks[0].body.status, 'accepted')
  assert.equal(resultAckFault.resultAcks[1].body.status, 'duplicate')
  assert.equal(storage['witness:meta'].outboxCount, 0)
  assert.equal(storage[journalKeys[0]].state, 'committed')

  const sentMessages = await admin.get(
    `/admin/messages?platform=zhilian&accountRef=${encodeURIComponent(accountRef)}` +
    `&conversationRef=${encodeURIComponent(fixtureConversationRef)}`,
  )
  assert.equal(sentMessages.messages.length, baselineLedgerMessages + 1)
  assert.equal(sentMessages.messages.filter((message) => message.origin === 'self').length, 1)
  assert.equal(sentMessages.messages.at(-1).text, fixtureSendText)
  const sentLedger = await admin.get('/admin/ledger')
  assert.equal(sentLedger.ledger.filter((record) => record.name === 'chat.sendMessage').length, 1)
  assert.ok(sentLedger.ledger.some((record) =>
    record.name === 'chat.sendMessage' && record.status === 'ok' && record.attempt === 1))
  for (const expectedCall of [
    'mainCaptureSendBaseline', 'mainSendMessageOnce', 'mainObserveStableOutbound',
  ]) {
    assert.ok(platform.mainCalls.includes(expectedCall), `M3 生产平台接缝没有执行 ${expectedCall}`)
  }
  assert.equal(platform.mainCalls.includes('mainInspectSendSurface'), false,
    '生产发送不得再由另一套 DOM preflight 逻辑授权')
  assert.equal(platform.mainCalls.filter((name) => name === 'mainFindConversation').length,
    routeFindCallsBeforeSend, 'M3 发送不得搜索或切换会话')
  assert.equal(platform.mainCalls.filter((name) => name === 'mainClickConversationOnce').length,
    routeClickCallsBeforeSend, 'M3 发送不得点击会话行')

  await sleep(500)
  const recoveredSend = await admin.get(`/admin/messages/send?intentId=${encodeURIComponent(intentId)}`)
  const recoveredMessages = await admin.get(
    `/admin/messages?platform=zhilian&accountRef=${encodeURIComponent(accountRef)}` +
    `&conversationRef=${encodeURIComponent(fixtureConversationRef)}`,
  )
  const recoveredLedger = await admin.get('/admin/ledger')
  assert.equal(recoveredSend.status, 'ok')
  assert.equal(platform.sendClickCount, 1, '终局发送在重连后不得再次点击')
  assert.equal(recoveredMessages.messages.filter((message) => message.origin === 'self').length, 1)
  assert.equal(recoveredLedger.ledger.filter((record) => record.name === 'chat.sendMessage').length, 1)
  assert.equal(Object.keys(storage).some((key) => key.startsWith('outbox:')), false)
  assert.equal(storage[journalKeys[0]].state, 'committed')
  resultAckFault.armed = false
  assert.equal(resultAckFault.armed, false)
  console.log('  PASS /admin/messages/send 经真脑/真 WS/生产 M3 到达 ok，ack 清 outbox，重连零增生')

  console.log('M4 当前候选人建档、主动招呼与重连后重复请求零增生')
  // M3 故障注入已经建立了新 WS session；正式候选人入口必须基于当前
  // session/boot 的 fresh 账号绑定，而不能沿用旧 session 的身份事实。
  const rebound = await admin.post('/admin/accounts/bind', {
    platform: 'zhilian',
    handId,
    accountRef,
  })
  assert.equal(rebound.account.accountRef, accountRef)
  assert.equal(rebound.account.identityState, 'verified')

  // 模拟真人把同一已登录标签页切到唯一推荐详情。此后的 read/select/send
  // 都经正式管理入口、真 Dispatcher 和真 WS，不使用 /admin/cmd。
  platform.tab.url = 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-position'
  const preview = await admin.post('/admin/candidates/current/read', {
    platform: 'zhilian', accountRef,
  })
  assert.match(preview.selectionRef, /^m-/)
  assert.equal(preview.contactState, 'unestablished')
  const selected = await admin.post('/admin/candidates/current/select', {
    selectionRef: preview.selectionRef,
  })
  assert.match(selected.profileId, /^p-/)
  assert.equal(selected.status, 'selected')
  assert.equal(selected.created, true)

  const greetingIntentId = 'fixture-intent-greeting-001'
  const greetingBody = {
    intentId: greetingIntentId,
    previousIntentId: '',
    profileId: selected.profileId,
    text: fixtureGreetingText,
  }
  const createdGreeting = await admin.post('/admin/candidates/greeting/send', greetingBody)
  assert.equal(createdGreeting.intentId, greetingIntentId)
  const completedGreeting = await eventually(
    () => admin.get(`/admin/candidates/greeting/send?intentId=${encodeURIComponent(greetingIntentId)}`),
    (view) => view.status === 'ok' && view.commandStatus === 'ok',
    '正式 M4 主动招呼没有到达 ok',
    15_000,
  )
  assert.equal(completedGreeting.intentId, greetingIntentId)
  assert.equal(platform.greetingClickCount, 1, '主动招呼必须只越过一次候选人可见最终动作')
  assert.deepEqual(platform.greetingActionPhases, ['prepare', 'preflight', 'commit'])
  assert.ok(platform.greetingRelationshipReads >= 1,
    '主动招呼必须在原推荐页确认同一目标关系已建立')

  const greetingConversations = await admin.get(
    `/admin/conversations?platform=zhilian&accountRef=${encodeURIComponent(accountRef)}`,
  )
  const greetingConversation = greetingConversations.conversations.find(
    (item) => item.conversationRef === fixtureGreetingConversationRef,
  )
  assert.equal(greetingConversation, undefined,
    '推荐页关系正证不得伪造尚未感知的会话引用')
  const greetingMessages = await admin.get(
    `/admin/messages?platform=zhilian&accountRef=${encodeURIComponent(accountRef)}` +
    `&conversationRef=${encodeURIComponent(fixtureGreetingConversationRef)}`,
  )
  assert.equal(greetingMessages.messages.length, 0,
    '推荐页关系正证不得伪造首条消息')
  const greetingLedger = await admin.get('/admin/ledger')
  assert.equal(greetingLedger.ledger.filter((record) => record.name === 'candidate.readCurrent').length, 1)
  assert.equal(greetingLedger.ledger.filter((record) => record.name === 'chat.sendGreeting').length, 1)
  assert.ok(greetingLedger.ledger.some((record) =>
    record.name === 'chat.sendGreeting' && record.status === 'ok' && record.attempt === 1))

  await eventually(
    () => ({ status: conn.status(), keys: Object.keys(storage) }),
    ({ status, keys }) => status.phase === 'session' && status.pendingResults === 0 &&
      !keys.some((key) => key.startsWith('outbox:')),
    '主动招呼 result 没有完成 ack/outbox 收束',
  )
  const greetingSessionBeforeReconnect = conn.status().session
  const socketsBeforeGreetingReconnect = harnessSockets.length
  harnessSockets.at(-1).terminate()
  await eventually(
    () => ({ status: conn.status(), sockets: harnessSockets.length }),
    ({ status, sockets }) => status.phase === 'session' && status.session !== null &&
      status.session !== greetingSessionBeforeReconnect && sockets > socketsBeforeGreetingReconnect,
    '主动招呼终局后手没有完成真实 WS 重连',
    15_000,
  )

  const repeatedGreeting = await admin.post('/admin/candidates/greeting/send', greetingBody)
  assert.equal(repeatedGreeting.intentId, greetingIntentId)
  assert.equal(repeatedGreeting.status, 'ok')
  assert.equal(repeatedGreeting.commandStatus, 'ok')
  const recoveredGreeting = await admin.get(
    `/admin/candidates/greeting/send?profileId=${encodeURIComponent(selected.profileId)}`,
  )
  assert.equal(recoveredGreeting.intentId, greetingIntentId)
  assert.equal(recoveredGreeting.status, 'ok')
  const recoveredGreetingConversations = await admin.get(
    `/admin/conversations?platform=zhilian&accountRef=${encodeURIComponent(accountRef)}`,
  )
  const recoveredGreetingMessages = await admin.get(
    `/admin/messages?platform=zhilian&accountRef=${encodeURIComponent(accountRef)}` +
    `&conversationRef=${encodeURIComponent(fixtureGreetingConversationRef)}`,
  )
  const recoveredGreetingLedger = await admin.get('/admin/ledger')
  assert.equal(platform.greetingClickCount, 1, '重复 POST/重连后不得再次执行候选人可见动作')
  assert.equal(recoveredGreetingConversations.conversations.some(
    (item) => item.conversationRef === fixtureGreetingConversationRef,
  ), false, '重复 POST/重连后不得补造会话事实')
  assert.equal(recoveredGreetingMessages.messages.length, 0,
    '重复 POST/重连后不得补造招呼消息事实')
  assert.equal(recoveredGreetingLedger.ledger.filter((record) => record.name === 'chat.sendGreeting').length, 1,
    '重复 POST/重连后招呼命令账本不得增生')
  console.log('  PASS candidate.readCurrent→建档→sendGreeting 经真脑/真 WS 收束，重复 POST/重连零增生且零伪造会话')

  // 新建会话必须等后续真实 IM 巡检按平台身份绑定到该档案；
  // 绑定能力落地前，不得在 E2E 夹具中伪造 conversationRef 接回 M5。

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
  await dispatchDebugAndWait('debug.inspectSendSurface', {})
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
