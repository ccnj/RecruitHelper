// 无需真脑的 base 单元测试。用 esbuild 加载与生产相同的 TypeScript 源码。
import assert from 'node:assert/strict'
import * as esbuild from 'esbuild'
import { mkdirSync, readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['test/unitentry.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/unit-bundle.mjs',
  logLevel: 'error',
})

const unitBundleURL = pathToFileURL(process.cwd() + '/test/dist/unit-bundle.mjs').href
const {
  AckStatus,
  CONTENT_MESSAGE,
  Connection,
  ContentSensor,
  DEFAULTS,
  Dispatcher,
  ErrorCode,
  EventName,
  Feature,
  GENERATED_CONTRACT_VALIDATION_EXECUTED,
  handleInfrastructureMessage,
  heartbeatDelayMs,
  Kind,
  LoginState,
  MANUAL_EMIT_MIN_MS,
  ManualInteractionKind,
  NavigationTracker,
  normalizeLocalWsUrl,
  NotReadyReason,
  PageKind,
  Primitive,
  PROTO_VERSION,
  RECONNECT_STABLE_MS,
  ResultStatus,
  SensorBridge,
  ZHILIAN_UNREAD_BADGE_SELECTOR,
  readZhilianUnreadTotal,
  readZhilianList,
  readZhilianThread,
  register,
  utf8ByteLength,
  zhilianTestHooks,
  ZhilianPlatformError,
} = await import(unitBundleURL + `?t=${Date.now()}`)

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
async function eventually(predicate, message, timeoutMs = 500) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (predicate()) return
    await sleep(2)
  }
  assert.fail(message)
}

function command(name, args, overrides = {}) {
  return {
    name,
    ver: 1,
    args,
    deadline: Date.now() + 1_000,
    execBudgetMs: 500,
    ...(name === Primitive.DebugSlowEcho ? { idemKey: `ik1:debug:test:${name}:-:${Math.random()}` } : {}),
    ...overrides,
  }
}

function recorder() {
  const frames = []
  return {
    frames,
    send(kind, session, body) {
      frames.push({ kind, session, body })
      return 'sent'
    },
  }
}

function results(frames, ref) {
  return frames.filter((frame) => frame.kind === Kind.Result && frame.body.ref === ref)
}

function pingOk(echo = null) {
  return { status: 'ok', data: { echo, swStartedAt: 0 } }
}

function contentSensorHarness() {
  const messages = []
  const timers = new Map()
  let timerID = 0
  const state = {
    now: 0,
    url: 'https://rd6.zhaopin.com/app/im',
    unread: 0,
    unreadReads: [],
    login: LoginState.In,
    loginReads: [],
  }
  const env = {
    clearTimer(handle) { timers.delete(handle) },
    currentURL() { return state.url },
    emit(message) { messages.push(message) },
    now() { return state.now },
    pageKind() {
      return new URL(state.url).pathname.startsWith('/app/im') ? PageKind.Im : PageKind.Other
    },
    readLoginState() { return state.loginReads.length > 0 ? state.loginReads.shift() : state.login },
    readUnreadTotal() { return state.unreadReads.length > 0 ? state.unreadReads.shift() : state.unread },
    setTimer(callback, delayMs) {
      const id = ++timerID
      timers.set(id, { callback, delayMs })
      return id
    },
  }
  return {
    env,
    messages,
    state,
    timers,
    runTimers() {
      const batch = [...timers.entries()]
      for (const [id, timer] of batch) {
        if (!timers.has(id)) continue
        timers.delete(id)
        timer.callback()
      }
    },
  }
}

class FakeSensorConnection {
  constructor() {
    this.config = {
      badgeDebounceMs: 800,
      badgeMinEmitIntervalMs: 5_000,
      navSettleMs: 500,
      manualQuietMs: 45_000,
    }
    this.context = undefined
    this.events = []
    this.contextHealth = []
    this.snapshots = []
    this.commandListeners = []
    this.configListeners = []
  }
  currentCommandContext(platform) {
    return this.context?.platform === platform ? this.context : undefined
  }
  emitPlatformSensorEvent(name, platform, data, observedAt) {
    this.events.push({ name, platform, data, observedAt, accountRef: this.context?.accountRef })
    return 'sent'
  }
  onCommandContext(listener) { this.commandListeners.push(listener); return () => {} }
  onSensorConfig(listener) { this.configListeners.push(listener); return () => {} }
  sensorConfig() { return this.config }
  setContextHealth(contexts) { this.contextHealth = contexts }
  setSensorSnapshot(snapshot) { this.snapshots.push(snapshot) }
  setContext(context) {
    this.context = context
    for (const listener of this.commandListeners) listener(context)
  }
}

function chromeEvent() {
  const listeners = []
  return {
    listeners,
    addListener(listener) { listeners.push(listener) },
  }
}

const tests = []
function test(name, fn) { tests.push({ name, fn }) }

test('本地 handId 并发只生成一次且模块重载后稳定', async () => {
  const storage = { infra: { wsUrl: 'ws://unit.invalid/v1/channel', obsolete: true } }
  let reads = 0
  let writes = 0
  globalThis.chrome = {
    storage: {
      local: {
        async get(key) {
          reads++
          await sleep(2)
          return key in storage ? { [key]: storage[key] } : {}
        },
        async set(value) {
          writes++
          Object.assign(storage, value)
        },
      },
    },
  }

  const firstModule = await import(unitBundleURL + `?hand-first=${Date.now()}`)
  const generated = await Promise.all(Array.from({ length: 24 }, () => firstModule.getHandId()))
  assert.equal(new Set(generated).size, 1, '并发首读生成了多个 handId')
  assert.match(generated[0], /^hand-[0-9a-f]{24}$/)
  assert.equal(reads, 1, '并发首读应共享一个存储读')
  assert.equal(writes, 1, '并发首读应只落盘一次')
  assert.deepEqual(storage.infra, { wsUrl: 'ws://unit.invalid/v1/channel', handId: generated[0] },
    '基础设施写回应只保留当前字段')

  const reloadedModule = await import(unitBundleURL + `?hand-reload=${Date.now()}`)
  const reloaded = await reloadedModule.getHandId()
  assert.equal(reloaded, generated[0], '模块重载后未从本地恢复稳定 handId')
})

test('options 配置写经后台栅栏保住首次 handId，并拒绝远端 WebSocket', async () => {
  const originalChrome = globalThis.chrome
  const storage = {}
  let releaseFirstRead
  const firstReadGate = new Promise((resolve) => { releaseFirstRead = resolve })
  let reads = 0
  let writes = 0
  try {
    globalThis.chrome = {
      storage: {
        local: {
          async get(key) {
            reads++
            if (reads === 1) await firstReadGate
            return key in storage ? { [key]: { ...storage[key] } } : {}
          },
          async set(value) {
            writes++
            Object.assign(storage, value)
          },
        },
      },
    }
    const isolated = await import(unitBundleURL + `?options-race=${Date.now()}`)
    const handIdPromise = isolated.getHandId()
    const responsePromise = new Promise((resolve) => {
      assert.equal(isolated.handleInfrastructureMessage({
        type: 'setWsUrl', wsUrl: 'ws://LOCALHOST:18888/v1/channel',
      }, resolve), true)
    })
    releaseFirstRead()
    const [handId, response] = await Promise.all([handIdPromise, responsePromise])
    assert.deepEqual(response, { ok: true, wsUrl: 'ws://localhost:18888/v1/channel' })
    assert.deepEqual(storage.infra, { handId, wsUrl: 'ws://localhost:18888/v1/channel' },
      'options 保存覆盖了刚出生的 handId')
    assert.equal(reads, 1, '配置保存必须共享首次 handId 的同一存储读')
    assert.equal(writes, 2, '应先落 handId，再原子写回 handId+wsUrl')

    const rejected = await new Promise((resolve) => {
      assert.equal(isolated.handleInfrastructureMessage({
        type: 'setWsUrl', wsUrl: 'ws://remote.example/v1/channel',
      }, resolve), true)
    })
    assert.equal(rejected.ok, false)
    assert.match(rejected.error, /只允许/)
    assert.deepEqual(storage.infra, { handId, wsUrl: 'ws://localhost:18888/v1/channel' },
      '非法远端地址不得改写配置')
    assert.equal(isolated.handleInfrastructureMessage({ type: 'unrelated' }, () => {}), undefined)
    storage.infra.wsUrl = 'ws://remote.example/v1/channel'
    await assert.rejects(isolated.getWsUrl(), /只允许/, '历史或外部写入的远端地址也不得进入拨号')
  } finally {
    globalThis.chrome = originalChrome
  }

  assert.equal(normalizeLocalWsUrl('ws://127.0.0.1:17872/v1/channel'), 'ws://127.0.0.1:17872/v1/channel')
  assert.equal(normalizeLocalWsUrl('ws://LOCALHOST:17873/v1/channel'), 'ws://localhost:17873/v1/channel')
  for (const invalid of [
    'wss://127.0.0.1:17872/v1/channel',
    'ws://127.0.0.1:17872/other',
    'ws://user@127.0.0.1:17872/v1/channel',
    'ws://localhost:17872/v1/channel?remote=1',
  ]) {
    assert.throws(() => normalizeLocalWsUrl(invalid), /只允许/)
  }

  const optionsSource = readFileSync('src/options/options.js', 'utf8')
  assert.doesNotMatch(optionsSource, /storage\.local\.set/, 'options 不得再直接写 infra')
  assert.match(optionsSource, /runtime\.sendMessage\(\{ type: 'setWsUrl'/,
    'options 必须经 background 的结构化消息入口保存')
})

test('generated contract validation.test.ts 在 Node 门禁中真正执行', async () => {
  assert.equal(GENERATED_CONTRACT_VALIDATION_EXECUTED, true)
})

test('全局严格 FIFO，queueDepth 与 inFlight 分开上报', async () => {
  let releaseFirst
  const firstGate = new Promise((resolve) => { releaseFirst = resolve })
  const starts = []
  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler(args) {
      starts.push(args.id)
      if (args.id === 1) await firstGate
      return pingOk({ id: args.id })
    },
  })
  const out = recorder()
  const dispatcher = new Dispatcher(out.send)
  dispatcher.handleCmd('fifo-1', 's', 's', command(Primitive.DebugPing, { id: 1 }))
  dispatcher.handleCmd('fifo-2', 's', 's', command(Primitive.DebugPing, { id: 2 }))
  dispatcher.handleCmd('fifo-3', 's', 's', command(Primitive.DebugPing, { id: 3 }))
  assert.deepEqual(starts, [1])
  assert.deepEqual(dispatcher.snapshot(), { queueDepth: 2, inFlight: 'fifo-1' })
  releaseFirst()
  await eventually(() => results(out.frames, 'fifo-3').length === 1, 'FIFO 未执行完')
  assert.deepEqual(starts, [1, 2, 3])
  assert.deepEqual(dispatcher.snapshot(), { queueDepth: 0, inFlight: null })
})

test('单槽外最多排队 16 条，第 18 条 QUEUE_FULL 且不进 dedup', async () => {
  let releaseFirst
  const firstGate = new Promise((resolve) => { releaseFirst = resolve })
  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler(args) {
      if (args.id === 0) await firstGate
      return pingOk(args)
    },
  })
  const out = recorder()
  const dispatcher = new Dispatcher(out.send)
  for (let id = 0; id < 18; id++) {
    dispatcher.handleCmd(`full-${id}`, 's', 's', command(Primitive.DebugPing, { id }))
  }
  assert.deepEqual(dispatcher.snapshot(), { queueDepth: 16, inFlight: 'full-0' })
  const rejected = out.frames.find((frame) => frame.kind === Kind.Ack && frame.body.ref === 'full-17')
  assert.equal(rejected?.body.status, AckStatus.Rejected)
  assert.equal(rejected?.body.error.code, ErrorCode.QueueFull)

  dispatcher.handleCmd('full-expired', 's', 's', command(Primitive.DebugPing, { id: 99 }, {
    deadline: Date.now() - 1,
  }))
  const expiredAck = out.frames.find((frame) => frame.kind === Kind.Ack && frame.body.ref === 'full-expired')
  assert.equal(expiredAck?.body.status, AckStatus.Accepted)
  assert.equal(results(out.frames, 'full-expired')[0]?.body.status, ResultStatus.Expired)
  assert.deepEqual(dispatcher.snapshot(), { queueDepth: 16, inFlight: 'full-0' })

  // rejected 不进 dedup：腾出位置后同 msgId 可重新受理。
  dispatcher.handleCancel('cancel-full-1', 's', 's', { ref: 'full-1', reason: 'operator' })
  dispatcher.handleCmd('full-17', 's', 's', command(Primitive.DebugPing, { id: 17 }))
  const accepted = out.frames.filter((frame) => frame.kind === Kind.Ack && frame.body.ref === 'full-17')
  assert.equal(accepted.at(-1)?.body.status, AckStatus.Accepted)
  releaseFirst()
  await eventually(() => results(out.frames, 'full-17').length === 1, '背压释放后未收束')
})

test('accepted 后过期返回 expired，绝不进入 handler', async () => {
  let calls = 0
  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler() { calls++; return pingOk() },
  })
  const out = recorder()
  const dispatcher = new Dispatcher(out.send)
  dispatcher.handleCmd('expired-1', 's', 's', command(Primitive.DebugPing, {}, { deadline: Date.now() - 1 }))
  await eventually(() => results(out.frames, 'expired-1').length === 1, 'expired 未返回')
  const ack = out.frames.find((frame) => frame.kind === Kind.Ack && frame.body.ref === 'expired-1')
  assert.equal(ack?.body.status, AckStatus.Accepted)
  assert.equal(results(out.frames, 'expired-1')[0].body.status, ResultStatus.Expired)
  assert.equal(calls, 0)
})

test('execBudget 到期响亮失败，并隔离忽略信号的僵尸 handler', async () => {
  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler() {
      await sleep(35)
      return pingOk()
    },
  })
  const out = recorder()
  const dispatcher = new Dispatcher(out.send)
  dispatcher.handleCmd('budget-1', 's', 's', command(Primitive.DebugPing, {}, { execBudgetMs: 5 }))
  await eventually(() => results(out.frames, 'budget-1').length === 1, '预算终局未返回')
  assert.equal(results(out.frames, 'budget-1')[0].body.status, ResultStatus.Failed)
  assert.equal(results(out.frames, 'budget-1')[0].body.error.code, ErrorCode.ExecTimeoutHand)
  assert.equal(dispatcher.snapshot().inFlight, 'budget-1', '僵尸 handler 退出前不得释放执行槽')
  await eventually(() => dispatcher.snapshot().inFlight === null, '僵尸 handler 未收敛')
  assert.equal(results(out.frames, 'budget-1').length, 1, '晚到 ok 不得覆盖预算终局')
})

test('handler progress 为 QoS0 帧，终局后不再上报', async () => {
  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler(_args, context) {
      const hooks = context
      hooks.progress('page 1', 25)
      return pingOk()
    },
  })
  const out = recorder()
  const dispatcher = new Dispatcher(out.send)
  dispatcher.handleCmd('progress-1', 's', 's', command(Primitive.DebugPing, {}))
  await eventually(() => results(out.frames, 'progress-1').length === 1, 'progress 用例未收束')
  const progress = out.frames.find((frame) => frame.kind === Kind.Progress)
  assert.deepEqual(progress?.body, { ref: 'progress-1', stage: 'page 1', pct: 25 })
})

test('generated CmdContext 原样只读暴露给 program handler', async () => {
  let receivedContext
  let observedContext
  register({
    name: Primitive.ChatReadList,
    class: 'intrusive',
    async handler(_args, context) {
      receivedContext = context.commandContext
      return { status: 'ok', data: { complete: true, sessions: [] } }
    },
  })
  const out = recorder()
  const dispatcher = new Dispatcher(out.send, (value) => { observedContext = value })
  const context = {
    platform: 'zhilian',
    accountRef: 'acc-1',
    expectedPrincipalFingerprint: 'principal-opaque-1',
  }
  dispatcher.handleCmd('context-1', 's', 's', {
    name: Primitive.ChatReadList,
    ver: 1,
    context,
    args: { filter: 'all', maxSessions: 10 },
    deadline: Date.now() + 1_000,
    execBudgetMs: 500,
    leaseMs: 60_000,
  })
  await eventually(() => results(out.frames, 'context-1').length === 1, 'context 用例未收束')
  assert.deepEqual(receivedContext, context)
  assert.deepEqual(observedContext, context, 'accepted 命令上下文必须同步进入 SW 内存接缝')
  assert.equal(Object.isFrozen(observedContext), true)
  assert.equal(results(out.frames, 'context-1')[0].body.status, ResultStatus.Ok)
})

test('generated validator 在 ack 前拦截坏 args/version/cancel reason', async () => {
  let calls = 0
  register({
    name: Primitive.DebugSlowEcho,
    class: 'effectful',
    async handler() { calls++; return { status: 'ok', data: { echoedAfterMs: 0 } } },
  })
  const out = recorder()
  const dispatcher = new Dispatcher(out.send)
  dispatcher.handleCmd('bad-args-1', 's', 's', command(Primitive.DebugSlowEcho, {}))
  const badArgs = out.frames.find((frame) => frame.kind === Kind.Ack && frame.body.ref === 'bad-args-1')
  assert.equal(badArgs?.body.status, AckStatus.Rejected)
  assert.equal(badArgs?.body.error.code, ErrorCode.ProtoBadArgs)
  assert.equal(calls, 0)

  dispatcher.handleCmd('bad-ver-1', 's', 's', command(Primitive.DebugPing, {}, { ver: 99 }))
  const badVersion = out.frames.find((frame) => frame.kind === Kind.Ack && frame.body.ref === 'bad-ver-1')
  assert.equal(badVersion?.body.status, AckStatus.Rejected)
  assert.equal(badVersion?.body.error.code, ErrorCode.ProtoUnsupportedCmd)

  let badClassCalls = 0
  register({
    name: Primitive.DebugPing,
    class: 'effectful',
    async handler() { badClassCalls++; return pingOk() },
  })
  dispatcher.handleCmd('bad-class-1', 's', 's', command(Primitive.DebugPing, {}))
  const badClass = out.frames.find((frame) => frame.kind === Kind.Ack && frame.body.ref === 'bad-class-1')
  assert.equal(badClass?.body.status, AckStatus.Rejected)
  assert.equal(badClass?.body.error.code, ErrorCode.ProtoUnsupportedCmd)
  assert.equal(badClassCalls, 0)

  dispatcher.handleCancel('bad-cancel-1', 's', 's', { ref: 'missing', reason: 'free-text-is-forbidden' })
  const badCancel = out.frames.find((frame) => frame.kind === Kind.Ack && frame.body.ref === 'bad-cancel-1')
  assert.equal(badCancel?.body.status, AckStatus.Rejected)
  assert.equal(badCancel?.body.error.code, ErrorCode.ProtoBadArgs)
})

test('cancel queued：移出队列并返回 canceled，handler 零调用', async () => {
  let releaseFirst
  const firstGate = new Promise((resolve) => { releaseFirst = resolve })
  const starts = []
  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler(args) {
      starts.push(args.id)
      if (args.id === 1) await firstGate
      return pingOk()
    },
  })
  const out = recorder()
  const dispatcher = new Dispatcher(out.send)
  dispatcher.handleCmd('cq-1', 's', 's', command(Primitive.DebugPing, { id: 1 }))
  dispatcher.handleCmd('cq-2', 's', 's', command(Primitive.DebugPing, { id: 2 }))
  dispatcher.handleCancel('cancel-cq-2', 's', 's', { ref: 'cq-2', reason: 'operator' })
  assert.equal(results(out.frames, 'cq-2')[0]?.body.status, ResultStatus.Canceled)
  assert.deepEqual(starts, [1])
  releaseFirst()
  await eventually(() => results(out.frames, 'cq-1').length === 1, '首命令未结束')
  assert.deepEqual(starts, [1])
})

test('cancel executing 合作式生效；handler 正常完成时 result wins', async () => {
  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler(_args, context) {
      const signal = context.signal
      await new Promise((_resolve, reject) => {
        signal.addEventListener('abort', () => reject(signal.reason), { once: true })
      })
      return pingOk()
    },
  })
  let out = recorder()
  let dispatcher = new Dispatcher(out.send)
  dispatcher.handleCmd('ce-1', 's', 's', command(Primitive.DebugPing, {}))
  dispatcher.handleCancel('cancel-ce-1', 's', 's', { ref: 'ce-1', reason: 'operator' })
  await eventually(() => results(out.frames, 'ce-1').length === 1, '合作取消未收束')
  assert.equal(results(out.frames, 'ce-1')[0].body.status, ResultStatus.Canceled)

  let release
  const gate = new Promise((resolve) => { release = resolve })
  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler() { await gate; return pingOk() },
  })
  out = recorder()
  dispatcher = new Dispatcher(out.send)
  dispatcher.handleCmd('rw-1', 's', 's', command(Primitive.DebugPing, {}))
  dispatcher.handleCancel('cancel-rw-1', 's', 's', { ref: 'rw-1', reason: 'operator' })
  release()
  await eventually(() => results(out.frames, 'rw-1').length === 1, 'result-wins 未收束')
  assert.equal(results(out.frames, 'rw-1')[0].body.status, ResultStatus.Ok)
})

test('越过不可逆动作安全点后 cancel 不打断，原 result 获胜', async () => {
  let release
  const gate = new Promise((resolve) => { release = resolve })
  let signal
  register({
    name: Primitive.DebugSlowEcho,
    class: 'effectful',
    async handler(_args, context) {
      context.beforeSideEffect()
      signal = context.signal
      await gate
      return { status: 'ok', data: { echoedAfterMs: 0 }, evidence: [{ type: 'postcondition', text: 'done' }] }
    },
  })
  const out = recorder()
  const dispatcher = new Dispatcher(out.send)
  dispatcher.handleCmd('safe-point-1', 's', 's', command(Primitive.DebugSlowEcho, { ms: 0, outcome: 'ok' }))
  dispatcher.handleCancel('cancel-safe-point-1', 's', 's', { ref: 'safe-point-1', reason: 'operator' })
  assert.equal(signal.aborted, false, '越过安全点后不应 abort handler')
  release()
  await eventually(() => results(out.frames, 'safe-point-1').length === 1, '安全点 result 未收束')
  assert.equal(results(out.frames, 'safe-point-1')[0].body.status, ResultStatus.Ok)
})

test('result 超完整信封硬上限时改为小型 PROTO_MSG_TOO_LARGE 终局', async () => {
  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler() { return pingOk('x'.repeat(1_000)) },
  })
  const frames = []
  const dispatcher = new Dispatcher((kind, session, body) => {
    frames.push({ kind, session, body })
    if (kind === Kind.Result && body.status === ResultStatus.Ok) return 'tooLarge'
    return 'sent'
  })
  dispatcher.handleCmd('large-result-1', 's', 's', command(Primitive.DebugPing, {}))
  await eventually(() => results(frames, 'large-result-1').length === 2, '大 result 未降级')
  const compact = results(frames, 'large-result-1').at(-1).body
  assert.equal(compact.status, ResultStatus.Failed)
  assert.equal(compact.error.code, ErrorCode.ProtoMsgTooLarge)
  assert.equal(compact.error.sideEffect, 'none')
  assert.equal(compact.data, undefined)
})

test('完成命令重复投递：duplicate ack + replayed result，不二次执行', async () => {
  let calls = 0
  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler() { calls++; return pingOk() },
  })
  const out = recorder()
  const dispatcher = new Dispatcher(out.send)
  const body = command(Primitive.DebugPing, {})
  dispatcher.handleCmd('dup-1', 's', 's', body)
  await eventually(() => results(out.frames, 'dup-1').length === 1, '首投未结束')
  dispatcher.handleCmd('dup-1', 'old-session', 's', body)
  assert.equal(calls, 1)
  const acks = out.frames.filter((frame) => frame.kind === Kind.Ack && frame.body.ref === 'dup-1')
  assert.equal(acks.at(-1).body.status, AckStatus.Duplicate)
  const replay = results(out.frames, 'dup-1').at(-1)
  assert.equal(replay.body.replayed, true)
  assert.equal(replay.session, 's', '跨会话重放必须使用当前会话')
})

test('完整帧按 UTF-8 字节计量，不按 JS 字符数', async () => {
  assert.equal('招聘'.length, 2)
  assert.equal(utf8ByteLength('招聘'), 6)
  assert.equal(utf8ByteLength('a'.repeat(32)), 32)
})

test('session 心跳使用 welcome 间隔的 ±20% 抖动', async () => {
  assert.equal(heartbeatDelayMs(20_000, () => 0), 16_000)
  assert.equal(heartbeatDelayMs(20_000, () => 0.5), 20_000)
  assert.equal(heartbeatDelayMs(20_000, () => 1), 24_000)
})

test('智联生产身份探针优先 session orgId、旧公司字段仅兜底且不外泄原始 ID', async () => {
  const probe = async ({ orgId, rootCompanyId }) => {
    const initial = {
      session: {
        session: {
          isLoggedIn: true,
          staff: { staffId: 'staff-private-42', defaultLoginPoint: 'login-private-7' },
          ...(orgId === undefined ? {} : { org: { orgId } }),
        },
      },
      personal: {
        imUserInfo: rootCompanyId === undefined ? {} : { rootCompanyId },
      },
    }
    globalThis.window = {}
    globalThis.location = { pathname: '/app/im' }
    globalThis.document = {
      scripts: [{ textContent: `globalThis.__INITIAL_STATE__=${JSON.stringify(initial)};` }],
      querySelector(selector) { return selector === '.im-session-list' ? {} : null },
    }
    return zhilianTestHooks.mainProbeZhilian()
  }

  const primary = await probe({ orgId: 'org-private-primary', rootCompanyId: 'legacy-private-a' })
  const samePrimary = await probe({ orgId: 'org-private-primary', rootCompanyId: 'legacy-private-b' })
  assert.equal(primary.loginState, 'in')
  assert.equal(primary.pageKind, 'im')
  assert.equal(primary.imListVisible, true)
  assert.match(primary.principalFingerprint, /^[0-9a-f]{64}$/)
  assert.equal(samePrimary.principalFingerprint, primary.principalFingerprint,
    'session orgId 存在时不得受旧 rootCompanyId 变化影响')
  const serialized = JSON.stringify(primary)
  for (const raw of ['staff-private-42', 'login-private-7', 'org-private-primary', 'legacy-private-a']) {
    assert.equal(serialized.includes(raw), false, `探针结果泄露原始身份字段: ${raw}`)
  }

  const fallback = await probe({ orgId: undefined, rootCompanyId: 'legacy-private-a' })
  const changedFallback = await probe({ orgId: undefined, rootCompanyId: 'legacy-private-b' })
  assert.match(fallback.principalFingerprint, /^[0-9a-f]{64}$/)
  assert.notEqual(changedFallback.principalFingerprint, fallback.principalFingerprint,
    '缺少 session orgId 时旧 rootCompanyId 兜底未参与指纹')

  const missingOrganization = await probe({ orgId: undefined, rootCompanyId: undefined })
  assert.equal(missingOrganization.loginState, 'in')
  assert.equal(missingOrganization.principalFingerprint, null,
    '无法确证组织身份时必须返回 null，让脑暂停绑定')
})

test('智联 MAIN 线程解析：方向不猜、未知类型归 system、数字卡片状态保持 unknown', async () => {
  const rows = [
    { idServer: 'm-text-out', time: 1, type: 'text', from: 'staff', text: '  招聘方  消息 ' },
    { idServer: 'm-text-in', time: 2, type: 'text', from: 'candidate', text: '候选人消息' },
    {
      idServer: 'm-card', time: 3, type: 105, from: 'staff',
      content: JSON.stringify({ content: JSON.stringify({ requestId: 'request-1', staffContent: '交换微信' }) }),
    },
    {
      idServer: 'm-unknown', time: 4, type: 99, from: 'candidate',
      content: JSON.stringify({ type: 2, msgb: '已拒绝' }),
    },
  ]
  globalThis.window = {
    $session: { staff: { staffId: 'staff' } },
    imEngine: {
      sessions: [{ sessionId: 'conversation-1', peerPartnerId: 'candidate', name: '候选人' }],
      async getHistoryMsgs() { return rows },
    },
    async fetch() {
      return { ok: true, status: 200, async json() { return { data: 2 } } }
    },
  }
  const page = await zhilianTestHooks.mainReadThreadPage('conversation-1', 8, null)
  assert.equal(page.messages[0].direction, 'out')
  assert.equal(page.messages[0].text, '招聘方 消息')
  assert.equal(page.messages[1].direction, 'in')
  assert.equal(page.messages[2].kind, 'card')
  assert.equal(page.messages[2].cardState, 'unknown')
  assert.equal(page.messages[3].direction, 'system')
  assert.equal(page.messages[3].kind, 'system')

  window.imEngine.getHistoryMsgs = async () => [
    { idServer: 'missing-from', time: 1, type: 'text', text: '不能猜方向' },
  ]
  await assert.rejects(
    zhilianTestHooks.mainReadThreadPage('conversation-1', 8, null),
    /message_direction_unresolved/,
  )
})

test('智联 MAIN 线程解析：runtime $session 缺失时复用 initial state 确定消息方向', async () => {
  const conversationRef = 'conversation-initial-staff'
  const initial = {
    im: {
      sessions: [{ sessionId: conversationRef, peerPartnerId: 'candidate-initial', name: '脱敏候选人' }],
    },
    session: { session: { staff: { staffId: 'staff-from-initial' } } },
  }
  let scriptsReadCount = 0
  globalThis.document = {
    get scripts() {
      scriptsReadCount += 1
      return [{ textContent: `globalThis.__INITIAL_STATE__=${JSON.stringify(initial)};` }]
    },
  }
  globalThis.window = {
    $session: null,
    imEngine: {
      sessions: [],
      async getHistoryMsgs() {
        return [
          { idServer: 'initial-out', time: 1, type: 'text', from: 'staff-from-initial', text: '招聘方' },
          { idServer: 'initial-in', time: 2, type: 'text', from: 'candidate-initial', text: '候选人' },
        ]
      },
    },
  }

  const page = await zhilianTestHooks.mainReadThreadPage(conversationRef, 8, null)
  assert.deepEqual(page.messages.map((message) => message.direction), ['out', 'in'])
  assert.equal(scriptsReadCount, 1, '会话与 staff 回退必须复用同一份 initial state 解析结果')
})

test('MAIN 注入空结果与 Chrome error 字段均响亮归类 CTX_NOT_READY', async () => {
  const fixtureMain = async () => ({ ok: true })
  for (const injection of [{ result: null }, { result: undefined }]) {
    globalThis.chrome = { scripting: { async executeScript() { return [injection] } } }
    await assert.rejects(
      zhilianTestHooks.runMain(7, fixtureMain, []),
      (error) => {
        assert.ok(error instanceof ZhilianPlatformError)
        assert.equal(error.code, 'CTX_NOT_READY')
        assert.notEqual(error.code, 'INTERNAL_HAND')
        assert.equal(error.reason, 'contentScriptDead')
        return true
      },
    )
  }

  globalThis.chrome = {
    scripting: {
      async executeScript() { return [{ error: { message: '页面上下文已销毁' } }] },
    },
  }
  await assert.rejects(
    zhilianTestHooks.runMain(7, fixtureMain, []),
    (error) => {
      assert.ok(error instanceof ZhilianPlatformError)
      assert.equal(error.code, 'CTX_NOT_READY')
      assert.match(error.message, /页面上下文已销毁/u)
      return true
    },
  )
})

test('智联列表 API 响应缺少真实 hasMore 时响亮失败', async () => {
  globalThis.window = {
    imEngine: {
      async getSessions() { return { curSessions: [] } },
    },
  }
  await assert.rejects(
    zhilianTestHooks.mainReadListPage(1, 8, 'all'),
    /hasMore missing/,
  )
})

test('智联线程 API 不可用时只接受目标路由上的稳定 Vue 时间线与明确 90 天边界', async () => {
  const row = { idServer: 'dom-message-1', time: 1_700_000_000_000, type: 'text', from: 'staff', text: 'DOM 消息' }
  const parent = {
    textContent: '仅展示近 90 天消息',
    scrollHeight: 100,
    clientHeight: 100,
    scrollTop: 0,
    querySelectorAll() { return [] },
  }
  const timeline = {
    __vue__: { _props: { data: [row] } },
    parentElement: parent,
  }
  globalThis.location = { href: 'https://rd6.zhaopin.com/app/im?sessionId=conversation-dom' }
  globalThis.document = {
    scripts: [{ textContent: `__INITIAL_STATE__=${JSON.stringify({
      im: { sessions: [{ sessionId: 'conversation-dom', peerPartnerId: 'peer-dom', name: '脱敏候选人' }] },
    })};` }],
    querySelector(selector) {
      return selector === '.im-timeline__wrapper .km-list' ? timeline : null
    },
  }
  globalThis.window = {
    $session: { staff: { staffId: 'staff' } },
  }
  const page = await zhilianTestHooks.mainReadThreadPage('conversation-dom', 8, null)
  assert.equal(page.reachedTop, true)
  assert.equal(page.cursor, null)
  assert.equal(page.messages.length, 1)
  assert.equal(page.messages[0].direction, 'out')
})

test('智联线程 DOM 回退：无 Vue 且 runtime session 为空时复用 initial timeline 与真实 90 天边界', async () => {
  const conversationRef = 'conversation-initial-timeline'
  const initial = {
    im: {
      sessions: [{ sessionId: conversationRef, peerPartnerId: 'peer-initial', name: '脱敏候选人' }],
      timelineMap: {
        [conversationRef]: {
          timeline: [
            { idServer: 'initial-dom-out', time: 1_700_000_000_000, type: 'text', from: 'staff-initial', text: '招聘方消息' },
            { idServer: 'initial-dom-in', time: 1_700_000_001_000, type: 'text', from: 'peer-initial', text: '候选人消息' },
          ],
        },
      },
    },
    session: { session: { staff: { staffId: 'staff-initial' } } },
  }
  const parent = {
    textContent: '',
    scrollHeight: 100,
    clientHeight: 100,
    scrollTop: 0,
    querySelectorAll() { return [] },
  }
  const timeline = { parentElement: parent }
  globalThis.location = { href: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}` }
  globalThis.document = {
    scripts: [{ textContent: `__INITIAL_STATE__=${JSON.stringify(initial)};` }],
    querySelector(selector) {
      if (selector === '.im-timeline__wrapper .im-timeline') return timeline
      if (selector === '.im-timeline-ending') return { textContent: '以下是90天内的聊天消息' }
      return null
    },
  }
  globalThis.window = { $session: null }

  const page = await zhilianTestHooks.mainReadThreadPage(conversationRef, 8, null)
  assert.equal(page.reachedTop, true)
  assert.equal(page.cursor, null)
  assert.deepEqual(page.messages.map((message) => message.direction), ['out', 'in'])
})

function threadFixtureMessage(key, hash, text = key, tsApprox = 100) {
  return {
    sourceKey: `source-${key}`,
    direction: 'in',
    kind: 'text',
    text,
    blobRef: null,
    contentHash: hash,
    cardType: null,
    cardState: null,
    tsApprox,
  }
}

function installThreadReadHarness(conversationRef, initialBehavior) {
  const fingerprint = '9'.repeat(64)
  let threadBehavior = initialBehavior
  let currentThreadURL = `https://rd6.zhaopin.com/app/im?sessionId=${encodeURIComponent(conversationRef)}`
  globalThis.chrome = {
    tabs: {
      async query() { return [{ id: 27, url: currentThreadURL, status: 'complete' }] },
      async update(_id, update) {
        currentThreadURL = update.url
        return { id: 27, url: currentThreadURL, status: 'complete' }
      },
      async get() { return { id: 27, url: currentThreadURL, status: 'complete' } },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func, args }) {
        if (func.name === 'mainProbeZhilian') {
          return [{ result: {
            pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
          } }]
        }
        if (func.name === 'mainReadThreadPage') return [{ result: await threadBehavior(...args) }]
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  return {
    fingerprint,
    setBehavior(next) { threadBehavior = next },
    context() {
      const state = { beforeCalls: 0 }
      return {
        state,
        value: {
          cmdMsgId: 'thread-fixture', deadlineMs: Date.now() + 10_000, commandContext: undefined,
          signal: new AbortController().signal,
          async progress() {}, checkpoint() {}, beforeSideEffect() { state.beforeCalls++ },
        },
      }
    },
  }
}

test('readThread 唯一锚尾裁掉前文并在裁剪后执行正文与总载荷门禁', async () => {
  const hashes = {
    older: 'd'.repeat(64),
    a: 'a'.repeat(64),
    b: 'b'.repeat(64),
    newer: 'c'.repeat(64),
  }
  const conversationRef = 'conversation-anchor-trim'
  const harness = installThreadReadHarness(conversationRef, async () => ({
    messages: [
      threadFixtureMessage('older-huge', hashes.older, '旧'.repeat(70 * 1024), 100),
      threadFixtureMessage('a', hashes.a, 'A', 100),
      threadFixtureMessage('b', hashes.b, 'B', 100),
      threadFixtureMessage('newer', hashes.newer, 'NEW', 100),
    ],
    reachedTop: true,
    cursor: null,
    peer: { displayName: '脱敏候选人' },
  }))
  const context = harness.context()
  const result = await readZhilianThread({
    conversationRef,
    window: {
      maxMessages: 4,
      anchorTail: [
        { direction: 'in', contentHash: hashes.a },
        { direction: 'in', contentHash: hashes.b },
      ],
      deep: false,
    },
  }, context.value, harness.fingerprint)

  assert.deepEqual(result.messages.map((message) => message.text), ['A', 'B', 'NEW'])
  assert.deepEqual(result.messages.map((message) => message.idx), [0, 1, 2])
  assert.equal(result.anchorMatched, true)
  assert.equal(result.complete, true)
  assert.equal(result.reachedTop, true)
  assert.equal(result.nextCursor, null)
})

test('readThread 平台分页按整页前插且同毫秒锚尾跨页仍保持真实顺序', async () => {
  const hashes = {
    older: 'd'.repeat(64),
    a: 'a'.repeat(64),
    b: 'b'.repeat(64),
    newer: 'c'.repeat(64),
  }
  const conversationRef = 'conversation-platform-boundary'
  const platformCursor = { endTime: 100, lastMsgId: 'platform-older' }
  const harness = installThreadReadHarness(conversationRef, async (_conversation, _limit, cursor) => cursor === null
    ? {
        messages: [
          threadFixtureMessage('platform-b', hashes.b, 'B', 100),
          threadFixtureMessage('platform-newer', hashes.newer, 'NEW', 100),
        ],
        reachedTop: false,
        cursor: platformCursor,
        peer: { displayName: '脱敏候选人' },
      }
    : {
        messages: [
          threadFixtureMessage('platform-older', hashes.older, 'OLDER', 100),
          threadFixtureMessage('platform-a', hashes.a, 'A', 100),
        ],
        reachedTop: true,
        cursor: null,
        peer: { displayName: '脱敏候选人' },
      })
  const result = await readZhilianThread({
    conversationRef,
    window: {
      maxMessages: 4,
      anchorTail: [
        { direction: 'in', contentHash: hashes.a },
        { direction: 'in', contentHash: hashes.b },
      ],
      deep: false,
    },
  }, harness.context().value, harness.fingerprint)

  assert.deepEqual(result.messages.map((message) => message.text), ['A', 'B', 'NEW'])
  assert.deepEqual(result.messages.map((message) => message.idx), [0, 1, 2])
  assert.equal(result.anchorMatched, true)
})

test('readThread opaque 游标携带跨 protocol 页锚点边界并严格校验位图', async () => {
  const hashes = {
    older: 'd'.repeat(64),
    a: 'a'.repeat(64),
    b: 'b'.repeat(64),
    newer: 'c'.repeat(64),
  }
  const conversationRef = 'conversation-protocol-boundary'
  const platformCursor = { endTime: 100, lastMsgId: 'protocol-older' }
  const harness = installThreadReadHarness(conversationRef, async (_conversation, _limit, cursor) => cursor === null
    ? {
        messages: [
          threadFixtureMessage('protocol-b', hashes.b, 'B', 100),
          threadFixtureMessage('protocol-newer', hashes.newer, 'NEW', 100),
        ],
        reachedTop: false,
        cursor: platformCursor,
        peer: { displayName: '脱敏候选人' },
      }
    : {
        messages: [
          threadFixtureMessage('protocol-older', hashes.older, 'OLDER', 100),
          threadFixtureMessage('protocol-a', hashes.a, 'A', 100),
        ],
        reachedTop: true,
        cursor: null,
        peer: { displayName: '脱敏候选人' },
      })
  const args = {
    conversationRef,
    window: {
      maxMessages: 2,
      anchorTail: [
        { direction: 'in', contentHash: hashes.a },
        { direction: 'in', contentHash: hashes.b },
      ],
      deep: false,
    },
  }
  const first = await readZhilianThread(args, harness.context().value, harness.fingerprint)
  assert.equal(first.complete, false)
  assert.ok(first.nextCursor)
  assert.deepEqual(first.messages.map((message) => message.text), ['B', 'NEW'])
  const decoded = zhilianTestHooks.decodeCursor(first.nextCursor)
  assert.equal(decoded.ap, 2, 'bit 1 表示较新聚合前缀匹配 anchorTail[1:]')

  const second = await readZhilianThread({ ...args, cursor: first.nextCursor }, harness.context().value, harness.fingerprint)
  assert.deepEqual(second.messages.map((message) => message.text), ['A'])
  assert.deepEqual(
    [...second.messages, ...first.messages].map((message) => message.text),
    ['A', 'B', 'NEW'],
    '脑端按协议前插后必须以完整账本锚尾开头，同毫秒也不得反序',
  )
  assert.equal(second.anchorMatched, true)
  assert.equal(second.complete, true)

  for (const invalidAP of [1, 2 ** 32, Number.MAX_SAFE_INTEGER + 1]) {
    const tampered = zhilianTestHooks.encodeCursor({ ...decoded, ap: invalidAP })
    const context = harness.context()
    await assert.rejects(
      readZhilianThread({ ...args, cursor: tampered }, context.value, harness.fingerprint),
      (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.CursorInvalid,
    )
    assert.equal(context.state.beforeCalls, 0, '非法 ap 必须在平台读取安全点之前拒绝')
  }
})

test('readThread 对同页与跨 protocol 页的重复完整锚尾保留全部候选交给脑', async () => {
  const hashA = 'a'.repeat(64)
  const hashB = 'b'.repeat(64)
  const hashX = 'e'.repeat(64)
  const samePageRef = 'conversation-anchor-duplicate'
  const samePageHarness = installThreadReadHarness(samePageRef, async () => ({
    messages: [
      threadFixtureMessage('duplicate-a-1', hashA, 'A1'),
      threadFixtureMessage('duplicate-b-1', hashB, 'B1'),
      threadFixtureMessage('duplicate-x', hashX, 'X'),
      threadFixtureMessage('duplicate-a-2', hashA, 'A2'),
      threadFixtureMessage('duplicate-b-2', hashB, 'B2'),
    ],
    reachedTop: true,
    cursor: null,
    peer: { displayName: '脱敏候选人' },
  }))
  const samePageArgs = {
    conversationRef: samePageRef,
    window: {
      maxMessages: 5,
      anchorTail: [
        { direction: 'in', contentHash: hashA },
        { direction: 'in', contentHash: hashB },
      ],
      deep: true,
    },
  }
  const samePage = await readZhilianThread(
    samePageArgs,
    samePageHarness.context().value,
    samePageHarness.fingerprint,
  )
  assert.equal(samePage.complete, true)
  assert.equal(samePage.anchorMatched, true)
  assert.equal(samePage.nextCursor, null)
  assert.deepEqual(
    samePage.messages.map((message) => message.text),
    ['A1', 'B1', 'X', 'A2', 'B2'],
    '手不得选一个锚尾后裁剪；两个候选都在，脑才能选最晚起点并审计',
  )

  const crossPageRef = 'conversation-anchor-cross-duplicate'
  const crossCursor = { endTime: 100, lastMsgId: 'cross-older' }
  const crossHarness = installThreadReadHarness(crossPageRef, async (_conversation, _limit, cursor) => cursor === null
    ? {
        messages: [
          threadFixtureMessage('cross-newer-a', hashA, 'A-newer'),
          threadFixtureMessage('cross-newer-x', hashX, 'X-newer'),
        ],
        reachedTop: false,
        cursor: crossCursor,
        peer: { displayName: '脱敏候选人' },
      }
    : {
        messages: [
          threadFixtureMessage('cross-older-a-1', hashA, 'A-old-1'),
          threadFixtureMessage('cross-older-a-2', hashA, 'A-old-2'),
        ],
        reachedTop: true,
        cursor: null,
        peer: { displayName: '脱敏候选人' },
      })
  const crossArgs = {
    conversationRef: crossPageRef,
    window: {
      maxMessages: 2,
      anchorTail: [
        { direction: 'in', contentHash: hashA },
        { direction: 'in', contentHash: hashA },
      ],
      deep: false,
    },
  }
  const crossFirst = await readZhilianThread(
    crossArgs,
    crossHarness.context().value,
    crossHarness.fingerprint,
  )
  assert.equal(zhilianTestHooks.decodeCursor(crossFirst.nextCursor).ap, 2)
  const crossSecond = await readZhilianThread(
    { ...crossArgs, cursor: crossFirst.nextCursor },
    crossHarness.context().value,
    crossHarness.fingerprint,
  )
  assert.equal(crossSecond.complete, true)
  assert.equal(crossSecond.anchorMatched, true)
  assert.equal(crossSecond.nextCursor, null)
  assert.deepEqual(crossSecond.messages.map((message) => message.text), ['A-old-1', 'A-old-2'])
  assert.deepEqual(
    [...crossSecond.messages, ...crossFirst.messages].map((message) => message.text),
    ['A-old-1', 'A-old-2', 'A-newer', 'X-newer'],
    '跨页前插后两个 [A,A] 候选均可见；脑取最晚起点时只投影 X-newer',
  )
})

test('readThread 首次收编没有 anchorTail 时保持完整正序快照', async () => {
  const conversationRef = 'conversation-first-adoption'
  const harness = installThreadReadHarness(conversationRef, async () => ({
    messages: [
      threadFixtureMessage('adoption-older', 'd'.repeat(64), 'OLDER', 100),
      threadFixtureMessage('adoption-newer', 'c'.repeat(64), 'NEW', 100),
    ],
    reachedTop: true,
    cursor: null,
    peer: { displayName: '脱敏候选人' },
  }))
  const result = await readZhilianThread({
    conversationRef,
    window: { maxMessages: 2, anchorTail: [], deep: false },
  }, harness.context().value, harness.fingerprint)

  assert.deepEqual(result.messages.map((message) => message.text), ['OLDER', 'NEW'])
  assert.deepEqual(result.messages.map((message) => message.idx), [0, 1])
  assert.equal(result.anchorMatched, false)
  assert.equal(result.reachedTop, true)
  assert.equal(result.complete, true)
})

test('readThread 不把 MAIN 注入 null 结果降级为 INTERNAL_HAND', async () => {
  const conversationRef = 'conversation-null-injection'
  const harness = installThreadReadHarness(conversationRef, async () => null)
  await assert.rejects(
    readZhilianThread({
      conversationRef,
      window: { maxMessages: 2, anchorTail: [], deep: false },
    }, harness.context().value, harness.fingerprint),
    (error) => {
      assert.ok(error instanceof ZhilianPlatformError)
      assert.equal(error.code, 'CTX_LOST_DURING_EXEC')
      assert.notEqual(error.code, 'INTERNAL_HAND')
      assert.equal(error.sideEffect, 'possible')
      assert.match(error.message, /页面脚本未返回结果/u)
      return true
    },
  )
})

test('readThread 游标绑定参数、设置读取安全点并拒绝原地游标', async () => {
  const fingerprint = 'f'.repeat(64)
  let threadBehavior = async () => ({
    messages: [{
      sourceKey: 'source-1',
      direction: 'in',
      kind: 'text',
      text: 'fixture',
      blobRef: null,
      contentHash: 'a'.repeat(64),
      cardType: null,
      cardState: null,
      tsApprox: 100,
    }],
    reachedTop: false,
    cursor: { endTime: 100, lastMsgId: 'message-1' },
    peer: { displayName: '脱敏候选人', platformUserRef: 'peer-1' },
  })
  let currentThreadURL = 'https://rd6.zhaopin.com/app/im'
  globalThis.chrome = {
    tabs: {
      async query() { return [{ id: 7, url: currentThreadURL, status: 'complete' }] },
      async update(_id, update) { currentThreadURL = update.url; return { id: 7, url: currentThreadURL, status: 'complete' } },
      async get() { return { id: 7, url: currentThreadURL, status: 'complete' } },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func, args }) {
        if (func.name === 'mainProbeZhilian') {
          return [{ result: {
            pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
          } }]
        }
        if (func.name === 'mainReadThreadPage') return [{ result: await threadBehavior(...args) }]
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = () => {
    const state = { beforeCalls: 0 }
    return {
      state,
      value: {
        cmdMsgId: 'cmd', deadlineMs: Date.now() + 1_000, commandContext: undefined,
        signal: new AbortController().signal,
        async progress() {},
        checkpoint() {},
        beforeSideEffect() { state.beforeCalls++ },
      },
    }
  }
  const args = {
    conversationRef: 'conversation-1',
    window: { maxMessages: 1, anchorTail: [], deep: false },
  }
  const firstContext = context()
  const first = await readZhilianThread(args, firstContext.value, fingerprint)
  assert.equal(first.complete, false)
  assert.ok(first.nextCursor)
  assert.equal(firstContext.state.beforeCalls, 1)

  const mismatchContext = context()
  await assert.rejects(
    readZhilianThread({ ...args, conversationRef: 'conversation-2', cursor: first.nextCursor }, mismatchContext.value, fingerprint),
    (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.CursorInvalid,
  )
  assert.equal(mismatchContext.state.beforeCalls, 0, '游标绑定失败不得开始平台读取')

  const stuckContext = context()
  await assert.rejects(
    readZhilianThread({ ...args, cursor: first.nextCursor }, stuckContext.value, fingerprint),
    (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.CursorInvalid,
  )
  assert.equal(stuckContext.state.beforeCalls, 1)

  threadBehavior = async () => { throw new Error('page execution lost') }
  const lostContext = context()
  await assert.rejects(
    readZhilianThread(args, lostContext.value, fingerprint),
    (error) => error instanceof ZhilianPlatformError &&
      error.code === ErrorCode.CtxLostDuringExec && error.sideEffect === 'possible',
  )
  assert.equal(lostContext.state.beforeCalls, 1)
})

test('readList API 失效时走 Vue DOM 虚拟列表，只有跨过 cutoff 才宣告 complete', async () => {
  const fingerprint = 'e'.repeat(64)
  const now = Date.now()
  let domPage = {
    sessions: [
      {
        conversationRef: 'newer',
        peer: { displayName: '候选人甲', platformUserRef: 'peer-a' },
        unreadCount: 1,
        lastMessage: { direction: 'in', kind: 'text', textPreview: '新消息' },
        lastActivityTs: now,
      },
      {
        conversationRef: 'older',
        peer: { displayName: '候选人乙', platformUserRef: 'peer-b' },
        unreadCount: 0,
        lastMessage: { direction: 'out', kind: 'text', textPreview: '旧消息' },
        lastActivityTs: now - 9 * 86_400_000,
      },
    ],
    atBottom: false,
    moved: true,
    scrollHeight: 2_000,
    scrollTop: 500,
    unstable: false,
  }
  const domCalls = []
  globalThis.chrome = {
    tabs: {
      async query() { return [{ id: 8, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' }] },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func, args }) {
        if (func.name === 'mainProbeZhilian') {
          return [{ result: {
            pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
          } }]
        }
        if (func.name === 'mainReadListPage') throw new Error('imEngine unavailable')
        if (func.name === 'mainReadListDOMWindow') {
          domCalls.push(args)
          return [{ result: domPage }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = {
    cmdMsgId: 'list', deadlineMs: now + 10_000, commandContext: undefined,
    signal: new AbortController().signal,
    async progress() {}, checkpoint() {}, beforeSideEffect() {},
  }
  const result = await readZhilianList({ filter: 'all', stopOlderThanDays: 8 }, context, fingerprint)
  assert.equal(result.complete, true)
  assert.equal(result.nextCursor, null)
  assert.deepEqual(result.sessions.map((item) => item.conversationRef), ['newer'])
  assert.deepEqual(domCalls[0], [false, true], '首次 DOM 兜底必须先复位列表顶部')

  domPage = {
    ...domPage,
    sessions: [domPage.sessions[0]],
    atBottom: true,
  }
  await assert.rejects(
    readZhilianList({ filter: 'all', stopOlderThanDays: 8 }, context, fingerprint),
    (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.ElementUnresolved,
  )

  const stableWindow = [
    { ...domPage.sessions[0], conversationRef: 'cursor-a', lastActivityTs: now },
    {
      ...domPage.sessions[0],
      conversationRef: 'cursor-b',
      peer: { displayName: '候选人丙', platformUserRef: 'peer-c' },
      lastActivityTs: now - 1_000,
    },
  ]
  domPage = { ...domPage, sessions: stableWindow, atBottom: false, scrollTop: 500 }
  const firstWindow = await readZhilianList({
    filter: 'all', stopOlderThanDays: 8, maxSessions: 1,
  }, context, fingerprint)
  assert.equal(firstWindow.complete, false)
  assert.ok(firstWindow.nextCursor)
  assert.deepEqual(domCalls.at(-1), [false, true])

  // 继续读时不自作主张复位；当前窗的会话重排/人工滚动必须使 digest 失配并响亮拒绝。
  domPage = { ...domPage, sessions: [...stableWindow].reverse(), scrollTop: 600 }
  await assert.rejects(
    readZhilianList({
      filter: 'all', stopOlderThanDays: 8, maxSessions: 1, cursor: firstWindow.nextCursor,
    }, context, fingerprint),
    (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.CursorInvalid,
  )
  assert.deepEqual(domCalls.at(-1), [false, false])
})

test('content 传感器：精确双读、5s 节流、isTrusted 与动态参数', async () => {
  const selectors = []
  assert.equal(readZhilianUnreadTotal({ querySelector(selector) { selectors.push(selector); return null } }), null,
    '徽章缺失不能猜成零')
  assert.deepEqual(selectors, [ZHILIAN_UNREAD_BADGE_SELECTOR])
  assert.equal(readZhilianUnreadTotal({ querySelector() { return { textContent: ' 12 ' } } }), 12)
  assert.equal(readZhilianUnreadTotal({ querySelector() { return { textContent: '消息 12' } } }), null,
    '禁止模糊扫描全页数字')

  const harness = contentSensorHarness()
  const sensor = new ContentSensor(harness.env)
  sensor.start()
  assert.equal(harness.messages[0].type, CONTENT_MESSAGE.Ready)
  assert.equal(harness.timers.size, 0, 'welcome 参数到达前不得自带传感节奏')

  sensor.configure({
    badgeDebounceMs: 800,
    badgeMinEmitIntervalMs: 5_000,
    navSettleMs: 500,
    manualQuietMs: 45_000,
  })
  assert.deepEqual([...harness.timers.values()].map((timer) => timer.delayMs).sort((a, b) => a - b), [500, 800, 800])
  harness.runTimers()
  const firstUnread = harness.messages.find((message) => message.type === CONTENT_MESSAGE.UnreadStable)
  assert.deepEqual(firstUnread, {
    type: CONTENT_MESSAGE.UnreadStable,
    emitEvent: true,
    observedAt: 0,
    prev: null,
    value: 0,
  })

  const unreadCount = () => harness.messages.filter((message) => message.type === CONTENT_MESSAGE.UnreadStable).length
  const beforeMismatch = unreadCount()
  harness.state.unreadReads = [0, 2]
  sensor.onDOMMutation()
  harness.runTimers()
  assert.equal(unreadCount(), beforeMismatch, '两次读数不一致不得上报')

  harness.state.now = 1_000
  harness.state.unreadReads = [3, 3]
  sensor.onDOMMutation()
  harness.runTimers()
  const throttled = harness.messages.filter((message) => message.type === CONTENT_MESSAGE.UnreadStable).at(-1)
  assert.equal(throttled.value, 3)
  assert.equal(throttled.emitEvent, false, '5s 内变化只更新 ping 现货，不发 event')

  harness.state.now = 6_000
  harness.state.unreadReads = [4, 4]
  sensor.onDOMMutation()
  harness.runTimers()
  const emitted = harness.messages.filter((message) => message.type === CONTENT_MESSAGE.UnreadStable).at(-1)
  assert.equal(emitted.emitEvent, true)
  assert.equal(emitted.prev, 0)

  const manualBefore = harness.messages.filter((message) => message.type === CONTENT_MESSAGE.ManualInteraction).length
  sensor.onTrustedPointer(false)
  assert.equal(harness.messages.filter((message) => message.type === CONTENT_MESSAGE.ManualInteraction).length, manualBefore)
  sensor.onTrustedPointer(true)
  harness.state.now += MANUAL_EMIT_MIN_MS - 1
  sensor.onTrustedKeyboard(true)
  assert.equal(harness.messages.filter((message) => message.type === CONTENT_MESSAGE.ManualInteraction).length, manualBefore + 1)
  harness.state.now += 1
  sensor.onTrustedKeyboard(true)
  assert.equal(harness.messages.filter((message) => message.type === CONTENT_MESSAGE.ManualInteraction).length, manualBefore + 2)
  sensor.onTrustedNavigationIntent(false)
  sensor.onTrustedNavigationIntent(true)
  assert.equal(harness.messages.filter((message) => message.type === CONTENT_MESSAGE.TrustedNavigationIntent).length, 1)
})

test('SW 页面桥：无 CmdContext 不造 accountRef，canonical 静音且命令导航不伪装人工', async () => {
  let now = 0
  const connection = new FakeSensorConnection()
  const navigation = new NavigationTracker(() => now)
  const bridge = new SensorBridge(connection, navigation, () => now)
  const tab1 = { tabId: 1, active: true, url: 'https://rd6.zhaopin.com/app/im', windowId: 1 }
  const tab2 = { tabId: 2, active: true, url: 'https://rd6.zhaopin.com/app/recommend', windowId: 1 }

  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.Ready, at: 0, pageKind: PageKind.Im, url: tab1.url }, tab1)
  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.LoginStable, observedAt: 0, state: LoginState.In }, tab1)
  bridge.acceptContentMessage({
    type: CONTENT_MESSAGE.UnreadStable,
    emitEvent: true,
    observedAt: 0,
    prev: null,
    value: 1,
  }, tab1)
  assert.equal(connection.events.length, 0, '未学到脑侧 accountRef 时必须静默')
  assert.equal(connection.snapshots.at(-1).unreadTotal.value, 1)

  connection.setContext({ platform: 'zhilian', accountRef: 'account-1', expectedPrincipalFingerprint: 'fp' })
  bridge.refreshCachedState()
  assert.deepEqual(connection.contextHealth, [{ platform: 'zhilian', accountRef: 'account-1', ready: true }])

  now = 6_000
  bridge.acceptContentMessage({
    type: CONTENT_MESSAGE.UnreadStable,
    emitEvent: true,
    observedAt: now,
    prev: 1,
    value: 2,
  }, tab1)
  assert.equal(connection.events.at(-1).name, EventName.UnreadBadge)
  assert.equal(connection.events.at(-1).accountRef, 'account-1')

  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.Ready, at: now, pageKind: PageKind.Recommend, url: tab2.url }, tab2)
  bridge.acceptContentMessage({
    type: CONTENT_MESSAGE.UnreadStable,
    emitEvent: true,
    observedAt: now,
    prev: null,
    value: 99,
  }, tab2)
  assert.equal(connection.snapshots.at(-1).unreadTotal.value, 2, '非 canonical 读数不得喂 ping')

  now = 12_000
  bridge.acceptContentMessage({
    type: CONTENT_MESSAGE.ManualInteraction,
    at: now,
    kind: ManualInteractionKind.Pointer,
    pageKind: PageKind.Recommend,
  }, tab2)
  assert.equal(connection.events.at(-1).name, EventName.ManualInteraction, 'manual 必须跨 canonical 上报')

  now = 18_000
  const commandWindow = navigation.beginCommandNavigation(1)
  const commandURL = 'https://rd6.zhaopin.com/app/im?sessionId=command'
  bridge.noteChromeNavigation(1, commandURL, now)
  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.PageNavigated, at: now + 500, pageKind: PageKind.Im, url: commandURL }, {
    ...tab1,
    url: commandURL,
  })
  commandWindow.end()
  assert.equal(connection.events.at(-1).name, EventName.PageNavigated)
  const manualAfterCommand = connection.events.filter((event) => event.name === EventName.ManualInteraction).length

  now = 24_000
  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.TrustedNavigationIntent, at: now }, { ...tab1, url: commandURL })
  const manualURL = 'https://rd6.zhaopin.com/app/im?sessionId=manual'
  bridge.noteChromeNavigation(1, manualURL, now + 1)
  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.PageNavigated, at: now + 500, pageKind: PageKind.Im, url: manualURL }, {
    ...tab1,
    url: manualURL,
  })
  const manualEvents = connection.events.filter((event) => event.name === EventName.ManualInteraction)
  assert.equal(manualEvents.length, manualAfterCommand + 1)
  assert.equal(manualEvents.at(-1).data.kind, ManualInteractionKind.Navigation)

  now = 30_000
  bridge.acceptContentMessage({
    type: CONTENT_MESSAGE.LoginStable,
    observedAt: now,
    state: LoginState.Out,
  }, { ...tab1, url: manualURL })
  assert.equal(connection.events.at(-1).name, EventName.LoginStateChanged)
  assert.equal(connection.events.at(-1).data.state, LoginState.Out)
  assert.equal(connection.contextHealth[0].reason, NotReadyReason.LoginRequired)
})

test('SensorBridge 只在 base 注册 Chrome 监听，并动态下发 welcome sensors', async () => {
  const runtimeMessage = chromeEvent()
  const tabActivated = chromeEvent()
  const tabUpdated = chromeEvent()
  const tabRemoved = chromeEvent()
  const windowFocused = chromeEvent()
  const committed = chromeEvent()
  const historyUpdated = chromeEvent()
  const fragmentUpdated = chromeEvent()
  const sent = []
  globalThis.chrome = {
    runtime: { onMessage: runtimeMessage },
    tabs: {
      onActivated: tabActivated,
      onUpdated: tabUpdated,
      onRemoved: tabRemoved,
      async query() { return [{ id: 7, url: 'https://rd6.zhaopin.com/app/im', active: true, windowId: 1 }] },
      async sendMessage(tabId, message) { sent.push({ tabId, message }); return { ok: true } },
    },
    windows: { WINDOW_ID_NONE: -1, onFocusChanged: windowFocused },
    webNavigation: {
      onCommitted: committed,
      onHistoryStateUpdated: historyUpdated,
      onReferenceFragmentUpdated: fragmentUpdated,
    },
  }
  const connection = new FakeSensorConnection()
  const bridge = new SensorBridge(connection)
  bridge.start()
  assert.deepEqual([
    runtimeMessage.listeners.length,
    tabActivated.listeners.length,
    tabUpdated.listeners.length,
    tabRemoved.listeners.length,
    windowFocused.listeners.length,
    committed.listeners.length,
    historyUpdated.listeners.length,
    fragmentUpdated.listeners.length,
  ], [1, 1, 1, 1, 1, 1, 1, 1])
  await eventually(() => sent.length > 0, '启动时未向已有 rd6 content 下发传感参数')
  assert.deepEqual(sent.at(-1).message.sensors, connection.config)
  connection.config = { ...connection.config, badgeDebounceMs: 1_200 }
  for (const listener of connection.configListeners) listener(connection.config)
  await eventually(() => sent.some((item) => item.message.sensors?.badgeDebounceMs === 1_200),
    'welcome 更新后未动态推送 content')
})

test('连接退避只在同一 session 稳定 60s 后归零，陈旧 timer 不能污染新连接', async () => {
  const originalSetTimeout = globalThis.setTimeout
  const originalClearTimeout = globalThis.clearTimeout
  const originalRandom = Math.random
  const originalWebSocket = globalThis.WebSocket
  const originalChrome = globalThis.chrome
  const clock = (() => {
    let now = 0
    let nextID = 1
    const active = new Map()
    const archive = new Map()
    return {
      setTimeout(callback, delay = 0, ...args) {
        const id = nextID++
        const record = { id, due: now + Math.max(0, Number(delay) || 0), callback, args }
        active.set(id, record)
        archive.set(id, record)
        return id
      },
      clearTimeout(id) { active.delete(id) },
      advance(ms) {
        const target = now + ms
        while (true) {
          const due = [...active.values()]
            .filter((record) => record.due <= target)
            .sort((left, right) => left.due - right.due || left.id - right.id)[0]
          if (!due) break
          active.delete(due.id)
          now = due.due
          due.callback(...due.args)
        }
        now = target
      },
      invokeCleared(id) {
        const record = archive.get(id)
        assert.ok(record, `timer ${id} 不存在`)
        assert.equal(active.has(id), false, `timer ${id} 尚未清除`)
        record.callback(...record.args)
      },
      pendingDelays() {
        return [...active.values()]
          .map((record) => record.due - now)
          .sort((left, right) => left - right)
      },
    }
  })()
  const sockets = []
  class StableWindowWebSocket {
    static OPEN = 1
    constructor(url) {
      this.url = url
      this.readyState = 0
      this.sent = []
      sockets.push(this)
    }
    open() {
      this.readyState = StableWindowWebSocket.OPEN
    }
    send(raw) { this.sent.push(raw) }
    close() {
      if (this.readyState === 3) return
      this.readyState = 3
      this.onclose?.()
    }
    receive(raw) { this.onmessage?.({ data: raw }) }
  }
  const flushMicrotasks = async () => {
    for (let index = 0; index < 12; index++) await Promise.resolve()
  }
  const welcomeBody = (session) => ({
    session,
    proto: PROTO_VERSION,
    hb: { intervalMs: 120_000, graceMs: 240_000 },
    limits: { maxMsgBytes: DEFAULTS.maxMsgBytes, inlineBytes: DEFAULTS.inlineBytes },
    contractMatch: true,
    now: Date.now(),
  })

  try {
    globalThis.setTimeout = clock.setTimeout
    globalThis.clearTimeout = clock.clearTimeout
    Math.random = () => 0.5
    globalThis.WebSocket = StableWindowWebSocket
    globalThis.chrome = {
      storage: {
        local: {
          async get() { return { infra: { wsUrl: 'ws://127.0.0.1:18001/v1/channel' } } },
          async set() {},
        },
      },
      runtime: { getManifest: () => ({ version: 'test' }) },
    }

    const connection = new Connection()
    const establish = async (socketIndex) => {
      await flushMicrotasks()
      const socket = sockets[socketIndex]
      assert.ok(socket, `第 ${socketIndex + 1} 个 socket 未创建`)
      socket.open()
      connection.onWelcome(welcomeBody('same-session-id'), socket)
      assert.equal(connection.status().phase, 'session')
      return socket
    }

    connection.ensureConnected()
    const first = await establish(0)
    const staleStableTimer = connection.reconnectStableTimer
    assert.ok(staleStableTimer)
    first.close()
    assert.equal(connection.reconnectDelay, DEFAULTS.reconnect.baseMs * DEFAULTS.reconnect.factor)
    assert.deepEqual(clock.pendingDelays(), [DEFAULTS.reconnect.baseMs])

    clock.advance(DEFAULTS.reconnect.baseMs)
    const second = await establish(1)
    const currentStableTimer = connection.reconnectStableTimer
    assert.notEqual(currentStableTimer, staleStableTimer)
    clock.invokeCleared(staleStableTimer)
    assert.equal(
      connection.reconnectDelay,
      DEFAULTS.reconnect.baseMs * DEFAULTS.reconnect.factor,
      '上一 socket 的已排队 timer 不得重置当前退避',
    )
    assert.equal(connection.reconnectStableTimer, currentStableTimer, '陈旧回调不得清掉当前稳定窗口')

    second.close()
    assert.equal(connection.reconnectDelay, DEFAULTS.reconnect.baseMs * DEFAULTS.reconnect.factor ** 2)
    assert.deepEqual(clock.pendingDelays(), [DEFAULTS.reconnect.baseMs * DEFAULTS.reconnect.factor])

    clock.advance(DEFAULTS.reconnect.baseMs * DEFAULTS.reconnect.factor)
    const third = await establish(2)
    clock.advance(RECONNECT_STABLE_MS - 1)
    assert.equal(
      connection.reconnectDelay,
      DEFAULTS.reconnect.baseMs * DEFAULTS.reconnect.factor ** 2,
      '稳定窗口未满不得提前归零',
    )
    clock.advance(1)
    assert.equal(connection.reconnectDelay, DEFAULTS.reconnect.baseMs, '稳定窗口满 60s 后应归零')
    assert.equal(connection.reconnectStableTimer, null)

    third.close()
    assert.deepEqual(clock.pendingDelays(), [DEFAULTS.reconnect.baseMs], '稳定后断线应从 base 重新退避')
  } finally {
    globalThis.setTimeout = originalSetTimeout
    globalThis.clearTimeout = originalClearTimeout
    Math.random = originalRandom
    globalThis.WebSocket = originalWebSocket
    globalThis.chrome = originalChrome
  }
})

test('连接地址首次读取失败会恢复 closed 并退避，后续成功可重新拨号', async () => {
  const originalSetTimeout = globalThis.setTimeout
  const originalClearTimeout = globalThis.clearTimeout
  const originalRandom = Math.random
  const originalWebSocket = globalThis.WebSocket
  const originalChrome = globalThis.chrome
  const timers = new Map()
  let nextTimerID = 1
  let storageReads = 0
  const sockets = []
  class StorageRecoveryWebSocket {
    static OPEN = 1
    constructor(url) {
      this.url = url
      this.readyState = 0
      sockets.push(this)
    }
    close() { this.readyState = 3 }
    send() {}
  }
  const flushMicrotasks = async () => {
    for (let index = 0; index < 12; index++) await Promise.resolve()
  }

  try {
    globalThis.setTimeout = (callback, delay = 0, ...args) => {
      const id = nextTimerID++
      timers.set(id, { callback, delay, args })
      return id
    }
    globalThis.clearTimeout = (id) => { timers.delete(id) }
    Math.random = () => 0.5
    globalThis.WebSocket = StorageRecoveryWebSocket
    globalThis.chrome = {
      storage: {
        local: {
          async get() {
            storageReads += 1
            if (storageReads === 1) throw new Error('fixture storage unavailable')
            return { infra: { wsUrl: 'ws://127.0.0.1:18002/v1/channel' } }
          },
          async set() {},
        },
      },
      runtime: { getManifest: () => ({ version: 'test' }) },
    }

    const connection = new Connection()
    connection.ensureConnected()
    await flushMicrotasks()
    assert.equal(connection.status().phase, 'closed', 'storage rejection 后不得楔在 connecting')
    assert.equal(sockets.length, 0)
    assert.equal(timers.size, 1)
    const [timerID, reconnect] = [...timers.entries()][0]
    assert.equal(reconnect.delay, DEFAULTS.reconnect.baseMs)

    timers.delete(timerID)
    reconnect.callback(...reconnect.args)
    await flushMicrotasks()
    assert.equal(storageReads, 2)
    assert.equal(sockets.length, 1, '退避后未重新创建 WebSocket')
    assert.equal(sockets[0].url, 'ws://127.0.0.1:18002/v1/channel')
    assert.equal(connection.status().phase, 'connecting')
  } finally {
    globalThis.setTimeout = originalSetTimeout
    globalThis.clearTimeout = originalClearTimeout
    Math.random = originalRandom
    globalThis.WebSocket = originalWebSocket
    globalThis.chrome = originalChrome
  }
})

test('连接层协商 feature、发送 QoS0 event，并在完整 UTF-8 信封硬边界关链', async () => {
  const sockets = []
  class FakeWebSocket {
    static OPEN = 1
    constructor(url) {
      this.url = url
      this.readyState = 0
      this.sent = []
      this.closeCalls = []
      sockets.push(this)
      queueMicrotask(() => {
        this.readyState = FakeWebSocket.OPEN
        this.onopen?.()
      })
    }
    send(text) { this.sent.push(text) }
    close(code, reason) {
      this.closeCalls.push({ code, reason })
      this.readyState = 3
      // 单元测试只观察传输边界，不触发真实重连定时器。
    }
    receive(text) { this.onmessage?.({ data: text }) }
  }

  const storage = { infra: { wsUrl: 'ws://127.0.0.1:18003/v1/channel' } }
  globalThis.WebSocket = FakeWebSocket
  globalThis.chrome = {
    storage: {
      local: {
        async get(key) { return key in storage ? { [key]: storage[key] } : {} },
        async set(value) { Object.assign(storage, value) },
      },
    },
    runtime: { getManifest: () => ({ version: 'test' }) },
  }

  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler(args) { return pingOk(args) },
  })
  const connection = new Connection()
  connection.ensureConnected()
  await eventually(() => sockets.length === 1 && sockets[0].sent.length > 0, 'hello 未发出')
  const socket = sockets[0]
  const hello = JSON.parse(socket.sent[0])
  assert.deepEqual(hello.body.features, [Feature.Progress1, Feature.Lease1, Feature.Cancel1])
  assert.match(hello.body.handId, /^hand-[0-9a-f]{24}$/)
  assert.equal(storage.infra.handId, hello.body.handId, 'hello 未使用已落盘的稳定 handId')
  assert.equal(Object.hasOwn(hello.body, 'auth'), false, 'hello 不得再携带 auth 字段')
  assert.equal(connection.hbTimer, null, 'welcome 前不得启动心跳')

  const envelope = (kind, msgId, session, body) => JSON.stringify({
    proto: PROTO_VERSION,
    kind,
    msgId,
    session,
    ts: Date.now(),
    attempt: 1,
    body,
  })
  const welcomeBody = {
    session: 's',
    proto: PROTO_VERSION,
    hb: { intervalMs: 12_345, graceMs: 50_000 },
    limits: { maxMsgBytes: 2_048, inlineBytes: 1_024 },
    contractMatch: true,
    now: Date.now(),
    sensors: {
      badgeDebounceMs: 1,
      badgeMinEmitIntervalMs: 5_000,
      navSettleMs: 1,
      manualQuietMs: 45_000,
    },
  }
  socket.receive(envelope(Kind.Welcome, 'welcome-1', null, welcomeBody))
  await eventually(() => connection.status().phase === 'session', 'welcome 未建立 session')
  assert.equal(connection.sensorConfig().badgeDebounceMs, 1)
  assert.equal(connection.status().heartbeatIntervalMs, 12_345, 'session 心跳必须服从 welcome.hb')

  const beforeEvent = socket.sent.length
  assert.equal(connection.emitSensorEvent(EventName.UnreadBadge, {
    platform: 'zhilian', accountRef: 'acc',
  }, { scope: 'total', value: 1, prev: 0, stable: true }), 'sent')
  assert.equal(socket.sent.length, beforeEvent + 1)
  const event = JSON.parse(socket.sent.at(-1))
  assert.equal(event.kind, Kind.Event)
  assert.equal(event.body.name, EventName.UnreadBadge)

  socket.receive(envelope(Kind.Cmd, 'wire-cmd-1', 's', command(Primitive.DebugPing, { via: 'wire' })))
  await eventually(() => socket.sent.some((raw) => {
    const frame = JSON.parse(raw)
    return frame.kind === Kind.Result && frame.body.ref === 'wire-cmd-1'
  }), '连接层命令未返回 result')
  const resultFrame = socket.sent.map(JSON.parse).find((frame) => frame.kind === Kind.Result && frame.body.ref === 'wire-cmd-1')
  assert.equal(connection.status().pendingResults, 1)

  // 同 bootId 跨会话补投：msgId/body 不变，attempt 递增，信封 session 更新为当前会话。
  socket.receive(envelope(Kind.Welcome, 'welcome-2', null, { ...welcomeBody, session: 's2' }))
  await eventually(() => connection.status().session === 's2', '第二会话未建立')
  await eventually(() => socket.sent.filter((raw) => JSON.parse(raw).msgId === resultFrame.msgId).length === 2,
    '未补投上一会话未 ack 的 result')
  const resentResult = socket.sent.map(JSON.parse).filter((frame) => frame.msgId === resultFrame.msgId).at(-1)
  assert.equal(resentResult.session, 's2')
  assert.equal(resentResult.attempt, 2)
  assert.deepEqual(resentResult.body, resultFrame.body)
  socket.receive(envelope(Kind.Ack, 'brain-ack-1', 's2', { ref: resultFrame.msgId, status: AckStatus.Accepted }))
  await eventually(() => connection.status().pendingResults === 0, 'result ack 未释放内存待投')

  // body 合法但完整信封超过本会话硬上限时，必须回小型失败终局，不能静默丢结果。
  register({
    name: Primitive.DebugPing,
    class: 'readonly',
    async handler() { return pingOk('中'.repeat(1_000)) },
  })
  socket.receive(envelope(Kind.Cmd, 'wire-large-result', 's2', command(Primitive.DebugPing, { compact: true })))
  await eventually(() => socket.sent.some((raw) => {
    const frame = JSON.parse(raw)
    return frame.kind === Kind.Result && frame.body.ref === 'wire-large-result'
  }), '超限 result 未返回小型失败终局')
  const compactResultFrame = socket.sent.map(JSON.parse).find((frame) => (
    frame.kind === Kind.Result && frame.body.ref === 'wire-large-result'
  ))
  assert.equal(compactResultFrame.body.status, ResultStatus.Failed)
  assert.equal(compactResultFrame.body.error.code, ErrorCode.ProtoMsgTooLarge)
  socket.receive(envelope(Kind.Ack, 'brain-ack-large', 's2', {
    ref: compactResultFrame.msgId,
    status: AckStatus.Accepted,
  }))
  await eventually(() => connection.status().pendingResults === 0, '小型失败终局 ack 未释放内存待投')

  function exactFrame(bytes, msgId) {
    const value = {
      proto: PROTO_VERSION,
      kind: Kind.Pong,
      msgId,
      session: 's',
      ts: Date.now(),
      attempt: 1,
      body: { now: Date.now(), padding: '' },
    }
    const base = JSON.stringify(value)
    const padding = bytes - utf8ByteLength(base)
    assert.ok(padding >= 0)
    value.body.padding = 'a'.repeat(padding)
    const raw = JSON.stringify(value)
    assert.equal(utf8ByteLength(raw), bytes)
    return raw
  }

  socket.receive(exactFrame(2_047, 'limit-minus-1'))
  await sleep(5)
  assert.equal(socket.closeCalls.length, 0, 'limit-1 不应关链')
  socket.receive(exactFrame(2_049, 'limit-plus-1'))
  await eventually(() => socket.closeCalls.some((call) => call.code === 1009), 'limit+1 未以 1009 关链')

  // 发送侧也按完整信封硬拒，event 为 QoS0，不能持久化或重试。
  const sentBeforeOversize = socket.sent.length
  const outcome = connection.emitSensorEvent(EventName.PageNavigated, {
    platform: 'zhilian', accountRef: 'acc',
  }, { at: Date.now(), pageKind: 'other', text: '中'.repeat(1_000) })
  assert.equal(outcome, 'tooLarge')
  assert.equal(socket.sent.length, sentBeforeOversize)
  assert.equal(DEFAULTS.maxMsgBytes, 262144)

  // 关闭真实 timer，只手动推进同一生产 heartbeatTick，验证连续两次 pong 缺失会关链。
  connection.stopHeartbeat()
  connection.scheduleHeartbeatTick = () => {}
  socket.readyState = FakeWebSocket.OPEN
  socket.closeCalls = []
  connection.heartbeatTick()
  connection.heartbeatTick()
  assert.equal(socket.closeCalls.length, 0)
  connection.heartbeatTick()
  assert.ok(socket.closeCalls.length > 0, '连续两次 pong 缺失未主动关链')
})

let failures = 0
for (const { name, fn } of tests) {
  try {
    await fn()
    console.log('PASS', name)
  } catch (error) {
    failures++
    console.error('FAIL', name)
    console.error(error)
  }
}

if (failures > 0) process.exit(1)
console.log(`ALL PASS (${tests.length})`)
process.exit(0)
