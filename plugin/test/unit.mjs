// 无需真脑的 base 单元测试。用 esbuild 加载与生产相同的 TypeScript 源码。
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
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
  acknowledgeRuntimeReloadResult,
  armRuntimeReload,
  CONTENT_MESSAGE,
  Connection,
  ContentSensor,
  DEFAULTS,
  Dispatcher,
  ErrorCode,
  ERROR_CODE_META,
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
  navigationTracker,
  normalizeLocalWsUrl,
  NotReadyReason,
  PageKind,
  Primitive,
  PROTO_VERSION,
  RECONNECT_STABLE_MS,
  Retryable,
  ResultStatus,
  SensorBridge,
  ZHILIAN_UNREAD_BADGE_SELECTOR,
  inspectZhilianSendSurfaceDiagnostic,
  readZhilianUnreadTotal,
  readZhilianList,
  readZhilianThread,
  readZhilianCurrentCandidate,
  readZhilianGreetingOutcome,
  refreshPagesAfterRuntimeReload,
  sendZhilianGreeting,
  sendZhilianMessage,
  normalizeZhilianMessageText,
  register,
  utf8ByteLength,
  validatePrimitiveResult,
  WitnessStore,
  WitnessStoreError,
  WitnessUnavailableReason,
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

function memoryWitnessStorage(initial = {}, hooks = {}) {
  const state = structuredClone(initial)
  const writes = []
  return {
    state,
    writes,
    async get(keys = null) {
      if (hooks.beforeGet) await hooks.beforeGet(keys)
      if (keys === null || keys === undefined) return structuredClone(state)
      const names = Array.isArray(keys) ? keys : [keys]
      return Object.fromEntries(names.filter((key) => Object.hasOwn(state, key)).map((key) => [key, structuredClone(state[key])]))
    },
    async set(items) {
      if (hooks.beforeSet) await hooks.beforeSet(items)
      writes.push({ kind: 'set', items: structuredClone(items) })
      Object.assign(state, structuredClone(items))
    },
    async remove(keys) {
      if (hooks.beforeRemove) await hooks.beforeRemove(keys)
      const names = Array.isArray(keys) ? keys : [keys]
      writes.push({ kind: 'remove', keys: [...names] })
      for (const key of names) delete state[key]
    },
  }
}

function sendMessageCommand(ref, idemKey, overrides = {}) {
  return {
    name: Primitive.ChatSendMessage,
    ver: 1,
    context: {
      platform: 'zhilian',
      accountRef: 'account-fixture',
      expectedPrincipalFingerprint: 'a'.repeat(64),
    },
    args: { conversationRef: 'conversation-fixture', text: '你好' },
    guards: { expectedTail: [{ direction: 'in', contentHash: 'b'.repeat(64) }] },
    idemKey,
    deadline: Date.now() + 10_000,
    execBudgetMs: 5_000,
    leaseMs: 30_000,
    ...overrides,
  }
}

function sendGreetingCommand(ref, idemKey, overrides = {}) {
  return {
    name: Primitive.ChatSendGreeting,
    ver: 1,
    context: {
      platform: 'zhilian',
      accountRef: 'account-fixture',
      expectedPrincipalFingerprint: 'a'.repeat(64),
    },
    args: {
      platformUserRef: 'fixture-user-greeting-orchestration',
      positionRef: 'fixture-job-greeting-orchestration',
      text: '你好',
    },
    guards: { expectUnestablished: true },
    idemKey,
    deadline: Date.now() + 10_000,
    execBudgetMs: 8_000,
    leaseMs: 30_000,
    ...overrides,
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

test('自重载 marker 只在同 ref result ACK 后触发一次，并由新 SW 一次性刷新平台页', async () => {
  const originalChrome = globalThis.chrome
  const storage = {}
  const runtimeReloads = []
  const pageReloads = []
  try {
    globalThis.chrome = {
      storage: {
        local: {
          async get(key) { return Object.hasOwn(storage, key) ? { [key]: structuredClone(storage[key]) } : {} },
          async set(values) { Object.assign(storage, structuredClone(values)) },
          async remove(key) { delete storage[key] },
        },
      },
      runtime: { reload() { runtimeReloads.push('reload') } },
      tabs: {
        async query(query) {
          assert.equal(query.url, 'https://rd6.zhaopin.com/*')
          return [{ id: 11 }, { id: 12 }, {}]
        },
        async reload(tabId) { pageReloads.push(tabId) },
      },
    }

    await armRuntimeReload('cmd-reload-1')
    assert.equal(acknowledgeRuntimeReloadResult('another-command'), false)
    assert.equal(runtimeReloads.length, 0)
    assert.equal(acknowledgeRuntimeReloadResult('cmd-reload-1'), true)
    assert.equal(acknowledgeRuntimeReloadResult('cmd-reload-1'), false)
    assert.equal(runtimeReloads.length, 1, 'accepted/duplicate ACK 只能触发一次 runtime.reload')

    assert.equal(await refreshPagesAfterRuntimeReload(), 2)
    assert.deepEqual(pageReloads, [11, 12])
    assert.equal(await refreshPagesAfterRuntimeReload(), 0, 'marker 被消费后不得重复刷新页面')
  } finally {
    globalThis.chrome = originalChrome
  }
})

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

test('witness journal/outbox 持久相关性、跨会话补投与 ack 删除', async () => {
  let now = 1_700_000_000_000
  const storage = memoryWitnessStorage()
  const witness = new WitnessStore(storage, () => now, () => 'witness-fixture-1')
  await witness.initialize()
  assert.deepEqual(witness.advertisement(), {
    witnessStoreId: 'witness-fixture-1', outboxPending: 0, journalOpen: 0,
  })

  const attempting = await witness.markAttempting('cmd-witness-1', 'idem-witness-1')
  assert.equal(attempting.state, 'attempting')
  assert.equal(storage.state['journal:idem-witness-1'].ref, 'cmd-witness-1')
  const result = {
    ref: 'cmd-witness-1', status: 'ok', replayed: false, execMs: 12,
    data: { conversationRef: 'c', contentHash: 'a'.repeat(64), observedAt: now },
    evidence: [{ type: 'outboundMessageObserved' }],
  }
  now += 10
  const envelope = {
    proto: 1, kind: 'result', msgId: 'result-envelope-1', session: 'session-old', ts: now,
    attempt: 1, body: result,
  }
  await witness.commitAndEnqueue('idem-witness-1', envelope)
  assert.equal(storage.state['journal:idem-witness-1'].result.ref, 'cmd-witness-1')
  assert.equal(witness.advertisement().outboxPending, 1)
  now += 10
  const replay = await witness.nextOutboxAttempt('result-envelope-1', 'session-current')
  assert.equal(replay.session, 'session-current')
  assert.equal(replay.attempt, 2)
  assert.equal(replay.msgId, envelope.msgId)
  assert.deepEqual(replay.body, envelope.body)
  assert.equal(storage.state['outbox:result-envelope-1'].message.session, 'session-current',
    '补投 session 必须先持久化再发送')
  await witness.acknowledgeResult('result-envelope-1')
  assert.equal(Object.hasOwn(storage.state, 'outbox:result-envelope-1'), false)
  assert.equal(witness.advertisement().outboxPending, 0)
  const afterAckRestart = new WitnessStore(storage, () => now, () => 'unused-after-ack')
  await afterAckRestart.initialize()
  assert.equal((await afterAckRestart.findJournalByIdemKey('idem-witness-1')).state, 'committed',
    'ack 后 committed journal 无 outbox 是合法稳定态')
})

test('witness 对 same idem/different ref 与 committed result.ref 错配硬失败', async () => {
  const firstStorage = memoryWitnessStorage()
  const first = new WitnessStore(firstStorage, () => 1_700_000_000_000, () => 'witness-correlation-1')
  await first.initialize()
  await first.markAttempting('cmd-original', 'idem-correlation')
  await assert.rejects(
    first.markAttempting('cmd-other', 'idem-correlation'),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
  )

  const secondStorage = memoryWitnessStorage()
  const second = new WitnessStore(secondStorage, () => 1_700_000_000_000, () => 'witness-correlation-2')
  await second.initialize()
  await second.markAttempting('cmd-original', 'idem-correlation')
  await assert.rejects(
    second.commitAndEnqueue('idem-correlation', {
      proto: 1, kind: 'result', msgId: 'wrong-correlation-result', session: 's', ts: Date.now(), attempt: 1,
      body: {
        ref: 'cmd-other', status: 'failed', replayed: false, execMs: 0,
        error: { code: ErrorCode.InternalHand, retryable: Retryable.ManualOnly, sideEffect: 'possible' },
      },
    }),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
  )

  const injected = memoryWitnessStorage({
    'witness:meta': {
      storeId: 'witness-injected', createdAt: 1, schemaVersion: 1,
      journalCount: 1, outboxCount: 0,
    },
    'journal:idem-injected': {
      ref: 'cmd-original', idemKey: 'idem-injected', state: 'committed', startedAt: 1,
      committedAt: 2, expiresAt: 9_999_999_999_999,
      result: { ref: 'cmd-other', status: 'failed', replayed: false, execMs: 0,
        error: { code: ErrorCode.InternalHand, retryable: Retryable.ManualOnly, sideEffect: 'possible' } },
    },
  })
  const loaded = new WitnessStore(injected)
  await assert.rejects(
    loaded.initialize(),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
    '加载时也必须复核 committed result.ref，不能只靠写路径',
  )
})

test('witness required count 阻断 same-store 单 journal 丢失，SW 重生也不能伪造 unknown', async () => {
  const storage = memoryWitnessStorage()
  const first = new WitnessStore(storage, () => 1_700_000_000_000, () => 'witness-continuity')
  await first.initialize()
  await first.markAttempting('cmd-continuity', 'idem-continuity')
  assert.equal(storage.state['witness:meta'].journalCount, 1)

  delete storage.state['journal:idem-continuity']
  const afterRestart = new WitnessStore(storage, () => 1_700_000_000_100, () => 'unused-store-id')
  await assert.rejects(
    afterRestart.initialize(),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
    '同 storeId 下 meta.count=1 但 key=0 必须熔断，不能返回 unknown',
  )
})

test('witness 拒绝 meta+成功 outbox 已落但 journal 仍 attempting 的 partial write', async () => {
  const now = 1_700_000_000_000
  const body = {
    ref: 'cmd-partial-write', status: 'ok', replayed: false, execMs: 10,
    data: { conversationRef: 'conversation-partial', contentHash: 'a'.repeat(64), observedAt: now },
    evidence: [{ type: 'outboundMessageObserved' }],
  }
  const storage = memoryWitnessStorage({
    'witness:meta': {
      storeId: 'witness-partial-write', createdAt: now, schemaVersion: 1,
      journalCount: 1, outboxCount: 1,
    },
    'journal:idem-partial-write': {
      ref: body.ref, idemKey: 'idem-partial-write', state: 'attempting',
      startedAt: now, expiresAt: now + DEFAULTS.journalTtlDays * 24 * 60 * 60 * 1000,
    },
    'outbox:result-partial-write': {
      message: {
        proto: 1, kind: 'result', msgId: 'result-partial-write', session: 'session-partial',
        ts: now + 1, attempt: 1, body,
      },
      createdAt: now + 1,
      expiresAt: now + 1 + DEFAULTS.outboxTtlDays * 24 * 60 * 60 * 1000,
    },
  })
  const witness = new WitnessStore(storage, () => now + 2, () => 'unused-partial-write')
  await assert.rejects(
    witness.initialize(),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
  )
})

test('witness 同一 SW 生命周期检测 key 集缩小，即使 count 被同步篡改也硬失败', async () => {
  const storage = memoryWitnessStorage()
  const witness = new WitnessStore(storage, () => 1_700_000_000_000, () => 'witness-live-continuity')
  await witness.initialize()
  await witness.markAttempting('cmd-live-continuity', 'idem-live-continuity')
  delete storage.state['journal:idem-live-continuity']
  storage.state['witness:meta'].journalCount = 0
  await assert.rejects(
    witness.findJournalByIdemKey('idem-live-continuity'),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
  )
})

test('expired attempting 先持久换 witnessStoreId 再删并更新 count，不在同库降成 unknown', async () => {
  let now = 1_700_000_000_000
  const storage = memoryWitnessStorage()
  const first = new WitnessStore(storage, () => now, () => 'witness-before-expiry')
  await first.initialize()
  await first.markAttempting('cmd-expired-attempting', 'idem-expired-attempting')
  now += DEFAULTS.journalTtlDays * 24 * 60 * 60 * 1000 + 1

  const afterRestart = new WitnessStore(storage, () => now, () => 'witness-after-expiry')
  await afterRestart.initialize()
  assert.deepEqual(afterRestart.advertisement(), {
    witnessStoreId: 'witness-after-expiry', outboxPending: 0, journalOpen: 0,
  })
  assert.equal(storage.state['witness:meta'].storeId, 'witness-after-expiry')
  assert.equal(storage.state['witness:meta'].journalCount, 0)
  assert.equal(Object.hasOwn(storage.state, 'journal:idem-expired-attempting'), false)
  assert.equal(await afterRestart.findJournalByIdemKey('idem-expired-attempting'), null,
    'unknown 只允许出现在已换代的新 storeId')
})

test('TTL 删除后 count 更新失败会熔断，重生按 count mismatch 继续拒绝 unknown', async () => {
  let now = 1_700_000_000_000
  let failCountUpdate = false
  const storage = memoryWitnessStorage({}, {
    beforeSet(items) {
      if (failCountUpdate && items['witness:meta']?.journalCount === 0) {
        throw new Error('fixture crash after ttl remove')
      }
    },
  })
  const first = new WitnessStore(storage, () => now, () => 'witness-ttl-crash-old')
  await first.initialize()
  await first.markAttempting('cmd-ttl-crash', 'idem-ttl-crash')
  now += DEFAULTS.journalTtlDays * 24 * 60 * 60 * 1000 + 1
  failCountUpdate = true
  const pruning = new WitnessStore(storage, () => now, () => 'witness-ttl-crash-new')
  await assert.rejects(
    pruning.initialize(),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
  )
  failCountUpdate = false
  const afterCrash = new WitnessStore(storage, () => now, () => 'unused-after-crash')
  await assert.rejects(
    afterCrash.initialize(),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
  )
})

test('真实 SX 顺序固定为 attempting 落盘后 click，committed 与 outbox 后才 WS', async () => {
  const order = []
  const storage = memoryWitnessStorage({}, {
    beforeSet(items) {
      const entries = Object.entries(items)
      if (entries.some(([key, value]) => key.startsWith('journal:') && value.state === 'attempting')) {
        order.push('attempting')
      }
      if (entries.some(([key, value]) => key.startsWith('journal:') && value.state === 'committed') &&
          entries.some(([key]) => key.startsWith('outbox:'))) {
        order.push('atomic-committed-outbox')
      }
    },
  })
  const witness = new WitnessStore(storage, Date.now, () => 'witness-order')
  await witness.initialize()
  order.length = 0
  let resultID = 0
  const out = recorder()
  const durable = async (session, body, commitIdemKey) => {
    const message = {
      proto: 1, kind: 'result', msgId: `durable-${++resultID}`, session, ts: Date.now(), attempt: 1, body,
    }
    if (commitIdemKey) await witness.commitAndEnqueue(commitIdemKey, message)
    else await witness.enqueueResult(message)
    order.push('ws')
    out.send(Kind.Result, session, body)
    return 'sent'
  }
  register({
    name: Primitive.ChatSendMessage,
    class: 'effectful',
    async handler(_args, context) {
      await context.beforeSideEffect()
      order.push('click')
      return {
        status: 'ok',
        data: { conversationRef: 'conversation-fixture', contentHash: 'c'.repeat(64), observedAt: Date.now() },
        evidence: [{ type: 'outboundMessageObserved' }],
      }
    },
  })
  const dispatcher = new Dispatcher(out.send, undefined, witness, durable)
  await dispatcher.handleCmd('sx-order-1', 's', 's', sendMessageCommand('sx-order-1', 'idem-order-1'))
  await eventually(() => results(out.frames, 'sx-order-1').length === 1, '真实 SX 未收束')
  assert.deepEqual(order, ['attempting', 'click', 'atomic-committed-outbox', 'ws'])
  const atomicWrite = storage.writes.find((write) => write.kind === 'set' &&
    Object.hasOwn(write.items, 'journal:idem-order-1') &&
    Object.keys(write.items).some((key) => key.startsWith('outbox:')))
  assert.ok(atomicWrite, 'committed journal 与 outbox 必须由同一次 storage.set 写入')
  const journal = storage.state['journal:idem-order-1']
  assert.equal(journal.state, 'committed')
  assert.equal(journal.result.ref, 'sx-order-1')

  await dispatcher.handleQuery('sx-order-1', 's')
  const report = out.frames.find((frame) => frame.kind === Kind.Report)
  assert.equal(report.body.state, 'done')
  assert.equal(report.body.result.ref, 'sx-order-1')
  assert.equal(report.body.witnessStoreId, 'witness-order')
})

test('真实 SX 越过 barrier 后任意失败终局均 atomic committed，query 只返回 done', async () => {
  const cases = [
    {
      label: 'guard-none',
      expectedCode: ErrorCode.GuardFailed,
      expectedSideEffect: 'none',
      async run(context) {
        await context.beforeSideEffect()
        return {
          status: 'failed',
          error: {
            code: ErrorCode.GuardFailed,
            retryable: Retryable.ManualOnly,
            sideEffect: 'none',
          },
        }
      },
    },
    {
      label: 'postcondition-possible',
      expectedCode: ErrorCode.PostconditionUnconfirmed,
      expectedSideEffect: 'possible',
      async run(context) {
        await context.beforeSideEffect()
        return {
          status: 'failed',
          error: {
            code: ErrorCode.PostconditionUnconfirmed,
            retryable: Retryable.ManualOnly,
            sideEffect: 'possible',
          },
        }
      },
    },
    {
      label: 'timeout',
      expectedCode: ErrorCode.ExecTimeoutHand,
      expectedSideEffect: 'possible',
      commandOverrides: { execBudgetMs: 30 },
      async run(context) {
        await context.beforeSideEffect()
        await sleep(80)
        return {
          status: 'failed',
          error: {
            code: ErrorCode.PostconditionUnconfirmed,
            retryable: Retryable.ManualOnly,
            sideEffect: 'possible',
          },
        }
      },
    },
  ]

  for (const fixture of cases) {
    const ref = `sx-terminal-${fixture.label}`
    const idemKey = `idem-terminal-${fixture.label}`
    const storage = memoryWitnessStorage()
    const witness = new WitnessStore(storage, Date.now, () => `witness-terminal-${fixture.label}`)
    await witness.initialize()
    const out = recorder()
    const commitKeys = []
    let resultID = 0
    const durable = async (session, body, commitIdemKey) => {
      commitKeys.push(commitIdemKey)
      const envelope = {
        proto: 1,
        kind: 'result',
        msgId: `terminal-${fixture.label}-${++resultID}`,
        session,
        ts: Date.now(),
        attempt: 1,
        body,
      }
      if (commitIdemKey) await witness.commitAndEnqueue(commitIdemKey, envelope)
      else await witness.enqueueResult(envelope)
      out.send(Kind.Result, session, body)
      return 'sent'
    }
    register({
      name: Primitive.ChatSendMessage,
      class: 'effectful',
      async handler(_args, context) { return fixture.run(context) },
    })
    const dispatcher = new Dispatcher(out.send, undefined, witness, durable)
    await dispatcher.handleCmd(
      ref,
      's',
      's',
      sendMessageCommand(ref, idemKey, fixture.commandOverrides),
    )
    await eventually(() => results(out.frames, ref).length === 1, `${fixture.label}: 终局未发送`)

    const terminal = results(out.frames, ref)[0].body
    assert.equal(terminal.status, ResultStatus.Failed, `${fixture.label}: 非 failed 终局`)
    assert.equal(terminal.error.code, fixture.expectedCode, `${fixture.label}: 错误码被改写`)
    assert.equal(terminal.error.sideEffect, fixture.expectedSideEffect, `${fixture.label}: sideEffect 被改写`)
    assert.deepEqual(commitKeys, [idemKey], `${fixture.label}: barrier 后未携带 idemKey 原子提交`)

    const journal = storage.state[`journal:${idemKey}`]
    assert.equal(journal.state, 'committed', `${fixture.label}: journal 永久滞留 attempting`)
    assert.deepEqual(journal.result, terminal, `${fixture.label}: journal 未保存同一完整 ResultBody`)
    const atomicWrite = storage.writes.find((write) => write.kind === 'set' &&
      Object.hasOwn(write.items, `journal:${idemKey}`) &&
      Object.keys(write.items).some((key) => key.startsWith('outbox:')))
    assert.ok(atomicWrite, `${fixture.label}: journal/outbox 没有同一次 storage.set 双写`)
    assert.equal(witness.advertisement().journalOpen, 0, `${fixture.label}: attempting 诊断计数未归零`)

    await dispatcher.handleQuery(ref, 's')
    const report = out.frames.find((frame) => frame.kind === Kind.Report && frame.body.ref === ref)
    assert.equal(report.body.state, 'done', `${fixture.label}: query 未返回 done`)
    assert.equal(report.body.journal.state, 'committed', `${fixture.label}: report journal 非 committed`)
    assert.deepEqual(report.body.result, terminal, `${fixture.label}: report 与发送终局矛盾`)
    await eventually(() => dispatcher.snapshot().inFlight === null, `${fixture.label}: handler 未收敛`)
  }
})

test('真实 SX barrier 前 guard 失败只入 outbox，不创建 journal', async () => {
  const ref = 'sx-pre-barrier-guard'
  const idemKey = 'idem-pre-barrier-guard'
  const storage = memoryWitnessStorage()
  const witness = new WitnessStore(storage, Date.now, () => 'witness-pre-barrier-guard')
  await witness.initialize()
  const out = recorder()
  const commitKeys = []
  register({
    name: Primitive.ChatSendMessage,
    class: 'effectful',
    async handler() {
      return {
        status: 'failed',
        error: {
          code: ErrorCode.GuardFailed,
          retryable: Retryable.ManualOnly,
          sideEffect: 'none',
        },
      }
    },
  })
  const dispatcher = new Dispatcher(out.send, undefined, witness, async (session, body, commitIdemKey) => {
    commitKeys.push(commitIdemKey)
    await witness.enqueueResult({
      proto: 1,
      kind: 'result',
      msgId: 'pre-barrier-guard-result',
      session,
      ts: Date.now(),
      attempt: 1,
      body,
    })
    out.send(Kind.Result, session, body)
    return 'sent'
  })
  await dispatcher.handleCmd(ref, 's', 's', sendMessageCommand(ref, idemKey))
  await eventually(() => results(out.frames, ref).length === 1, 'barrier 前 guard 失败未发送终局')
  assert.deepEqual(commitKeys, [undefined], 'barrier 前失败不得请求 committed 双写')
  assert.equal(Object.hasOwn(storage.state, `journal:${idemKey}`), false)
  assert.equal(Object.hasOwn(storage.state, 'outbox:pre-barrier-guard-result'), true)

  await dispatcher.handleQuery(ref, 's')
  const report = out.frames.find((frame) => frame.kind === Kind.Report && frame.body.ref === ref)
  assert.equal(report.body.state, 'unknown', '无 journal 的零副作用终局不得伪报 committed')
  assert.equal(report.body.result, null)
  assert.equal(report.body.journal, null)
})

test('barrier 后失败终局 atomic 双写失败时保持 attempting 并熔断后续 SX', async () => {
  const storage = memoryWitnessStorage({}, {
    beforeSet(items) {
      const keys = Object.keys(items)
      if (keys.some((key) => key.startsWith('journal:')) &&
          keys.some((key) => key.startsWith('outbox:'))) {
        throw new Error('fixture failed-terminal atomic write failed')
      }
    },
  })
  const witness = new WitnessStore(storage, Date.now, () => 'witness-failed-terminal-fuse')
  await witness.initialize()
  const out = recorder()
  let handlerCalls = 0
  let durableAttempts = 0
  register({
    name: Primitive.ChatSendMessage,
    class: 'effectful',
    async handler(_args, context) {
      handlerCalls += 1
      await context.beforeSideEffect()
      return {
        status: 'failed',
        error: {
          code: ErrorCode.PostconditionUnconfirmed,
          retryable: Retryable.ManualOnly,
          sideEffect: 'possible',
        },
      }
    },
  })
  const dispatcher = new Dispatcher(out.send, undefined, witness, async (session, body, commitIdemKey) => {
    durableAttempts += 1
    try {
      const envelope = {
        proto: 1,
        kind: 'result',
        msgId: `failed-terminal-fuse-${durableAttempts}`,
        session,
        ts: Date.now(),
        attempt: 1,
        body,
      }
      if (commitIdemKey) await witness.commitAndEnqueue(commitIdemKey, envelope)
      else await witness.enqueueResult(envelope)
    } catch {
      return 'dropped'
    }
    assert.fail('atomic 双写失败后不得发送 result')
  })
  const firstRef = 'sx-failed-terminal-fuse'
  await dispatcher.handleCmd(
    firstRef,
    'session-fuse',
    'session-fuse',
    sendMessageCommand(firstRef, 'idem-failed-terminal-fuse'),
  )
  await eventually(
    () => durableAttempts === 1 && dispatcher.snapshot().inFlight === null,
    'barrier 后失败终局未进入 atomic 双写或未收敛',
  )
  assert.equal(handlerCalls, 1)
  assert.equal(results(out.frames, firstRef).length, 0, '持久屏障失败不得发送易失终局')
  assert.equal(storage.state['journal:idem-failed-terminal-fuse'].state, 'attempting')
  assert.equal(Object.keys(storage.state).some((key) => key.startsWith('outbox:')), false)

  await dispatcher.handleQuery(firstRef, 'session-fuse')
  const report = out.frames.find((frame) => frame.kind === Kind.Report && frame.body.ref === firstRef)
  assert.equal(report.body.state, 'attempting')
  const secondRef = 'sx-after-failed-terminal-fuse'
  await dispatcher.handleCmd(
    secondRef,
    'session-fuse',
    'session-fuse',
    sendMessageCommand(secondRef, 'idem-after-failed-terminal-fuse'),
  )
  const rejected = out.frames.find((frame) => frame.kind === Kind.Ack && frame.body.ref === secondRef)
  assert.equal(rejected.body.status, AckStatus.Rejected)
  assert.equal(rejected.body.error.code, ErrorCode.QueueFull)
  assert.equal(handlerCalls, 1, 'durable 失败熔断后不应执行下一条 SX')
})

test('committed+outbox 原子 set 失败时两边内存视图都不推进且不发送 WS', async () => {
  const storage = memoryWitnessStorage({}, {
    beforeSet(items) {
      const keys = Object.keys(items)
      if (keys.some((key) => key.startsWith('journal:')) && keys.some((key) => key.startsWith('outbox:'))) {
        throw new Error('fixture atomic set failed')
      }
    },
  })
  const witness = new WitnessStore(storage, Date.now, () => 'witness-atomic-fail')
  await witness.initialize()
  await witness.markAttempting('sx-atomic-fail', 'idem-atomic-fail')
  let wsWrites = 0
  const envelope = {
    proto: 1, kind: 'result', msgId: 'atomic-fail-result', session: 's', ts: Date.now(), attempt: 1,
    body: {
      ref: 'sx-atomic-fail', status: 'ok', replayed: false, execMs: 1,
      data: { conversationRef: 'c', contentHash: 'a'.repeat(64), observedAt: Date.now() },
      evidence: [{ type: 'outboundMessageObserved' }],
    },
  }
  try {
    await witness.commitAndEnqueue('idem-atomic-fail', envelope)
    wsWrites += 1
  } catch (error) {
    assert.ok(error instanceof WitnessStoreError)
    assert.equal(error.reason, WitnessUnavailableReason.WriteFailed)
  }
  assert.equal(wsWrites, 0)
  assert.equal(storage.state['journal:idem-atomic-fail'].state, 'attempting')
  assert.equal(Object.hasOwn(storage.state, 'outbox:atomic-fail-result'), false)
  assert.deepEqual(witness.advertisement(), {
    witnessStoreId: 'witness-atomic-fail', outboxPending: 0, journalOpen: 1,
  })
})

test('postcondition ok 但 atomic commit 失败后，同 SW 重复 cmd 不得从内存 success 造 outbox', async () => {
  const storage = memoryWitnessStorage({}, {
    beforeSet(items) {
      const keys = Object.keys(items)
      if (keys.some((key) => key.startsWith('journal:')) && keys.some((key) => key.startsWith('outbox:'))) {
        throw new Error('fixture commit unavailable after confirmed postcondition')
      }
    },
  })
  const witness = new WitnessStore(storage, Date.now, () => 'witness-confirmed-commit-fail')
  await witness.initialize()
  const out = recorder()
  let handlerCalls = 0
  let durableAttempts = 0
  register({
    name: Primitive.ChatSendMessage,
    class: 'effectful',
    async handler(_args, context) {
      handlerCalls += 1
      await context.beforeSideEffect()
      return {
        status: 'ok',
        data: { conversationRef: 'conversation-fixture', contentHash: 'a'.repeat(64), observedAt: Date.now() },
        evidence: [{ type: 'outboundMessageObserved' }],
      }
    },
  })
  const durable = async (session, body, commitIdemKey) => {
    durableAttempts += 1
    const envelope = {
      proto: 1, kind: 'result', msgId: `confirmed-commit-fail-${durableAttempts}`,
      session, ts: Date.now(), attempt: 1, body,
    }
    try {
      if (commitIdemKey) await witness.commitAndEnqueue(commitIdemKey, envelope)
      else await witness.enqueueResult(envelope)
    } catch {
      return 'dropped'
    }
    out.send(Kind.Result, session, body)
    return 'sent'
  }
  const dispatcher = new Dispatcher(out.send, undefined, witness, durable)
  const commandBody = sendMessageCommand('sx-confirmed-commit-fail', 'idem-confirmed-commit-fail')
  await dispatcher.handleCmd('sx-confirmed-commit-fail', 's', 's', commandBody)
  await eventually(() => durableAttempts === 1, 'confirmed 后 atomic commit 未尝试')
  assert.equal(handlerCalls, 1)
  assert.equal(results(out.frames, 'sx-confirmed-commit-fail').length, 0,
    'commit 失败不能 QoS0 发送 confirmed')
  assert.equal(storage.state['journal:idem-confirmed-commit-fail'].state, 'attempting')
  assert.equal(Object.keys(storage.state).some((key) => key.startsWith('outbox:')), false)

  await dispatcher.handleCmd('sx-confirmed-commit-fail', 's', 's', commandBody)
  await sleep(10)
  assert.equal(handlerCalls, 1, '同 SW 重复 cmd 不能再次执行')
  assert.equal(durableAttempts, 1, '同 SW 重复 cmd 不能重放未持久化的内存 success')
  assert.equal(results(out.frames, 'sx-confirmed-commit-fail').length, 0)
  const duplicateAcks = out.frames.filter((frame) =>
    frame.kind === Kind.Ack && frame.body.ref === 'sx-confirmed-commit-fail' && frame.body.status === AckStatus.Duplicate)
  assert.equal(duplicateAcks.length, 1)

  await dispatcher.handleQuery('sx-confirmed-commit-fail', 's')
  const report = out.frames.find((frame) => frame.kind === Kind.Report && frame.body.ref === 'sx-confirmed-commit-fail')
  assert.equal(report.body.state, 'attempting')
  assert.equal(report.body.result, null)
})

test('witnessed durable dropped/tooLarge 熔断多条 SX，只逐条执行隔离 msgId 的新信封', async () => {
  for (const failedOutcome of ['dropped', 'tooLarge']) {
    const storage = memoryWitnessStorage()
    const witness = new WitnessStore(
      storage,
      Date.now,
      () => `witness-fuse-${failedOutcome}`,
    )
    await witness.initialize()
    const out = recorder()
    const handlerCalls = []
    let releaseFirst
    let firstPastBarrierResolve
    const firstPastBarrier = new Promise((resolve) => { firstPastBarrierResolve = resolve })
    const firstOutcomeGate = new Promise((resolve) => { releaseFirst = resolve })
    register({
      name: Primitive.ChatSendMessage,
      class: 'effectful',
      async handler(_args, context) {
        handlerCalls.push(context.cmdMsgId)
        await context.beforeSideEffect()
        if (context.cmdMsgId === `sx-fuse-first-${failedOutcome}`) {
          firstPastBarrierResolve()
          await firstOutcomeGate
        }
        return {
          status: 'ok',
          data: {
            conversationRef: 'conversation-fixture',
            contentHash: 'a'.repeat(64),
            observedAt: Date.now(),
          },
          evidence: [{ type: 'outboundMessageObserved' }],
        }
      },
    })
    register({
      name: Primitive.DebugPing,
      class: 'readonly',
      async handler(args) { return pingOk(args) },
    })
    const durableAttempts = []
    let resultID = 0
    const firstRef = `sx-fuse-first-${failedOutcome}`
    const secondRef = `sx-fuse-second-${failedOutcome}`
    const thirdRef = `sx-fuse-third-${failedOutcome}`
    const durable = async (session, body, commitIdemKey) => {
      durableAttempts.push(body.ref)
      if (body.ref === firstRef) return failedOutcome
      const envelope = {
        proto: 1,
        kind: 'result',
        msgId: `fuse-${failedOutcome}-result-${++resultID}`,
        session,
        ts: Date.now(),
        attempt: 1,
        body,
      }
      if (commitIdemKey) await witness.commitAndEnqueue(commitIdemKey, envelope)
      else await witness.enqueueResult(envelope)
      out.send(Kind.Result, session, body)
      return 'sent'
    }
    const dispatcher = new Dispatcher(out.send, undefined, witness, durable)
    const firstBody = sendMessageCommand(firstRef, `idem-fuse-first-${failedOutcome}`)
    const secondBody = sendMessageCommand(secondRef, `idem-fuse-second-${failedOutcome}`)
    const thirdBody = sendMessageCommand(thirdRef, `idem-fuse-third-${failedOutcome}`)

    await dispatcher.handleCmd(firstRef, 'session-old', 'session-old', firstBody)
    await firstPastBarrier
    await dispatcher.handleCmd(secondRef, 'session-old', 'session-old', secondBody)
    await dispatcher.handleCmd(thirdRef, 'session-old', 'session-old', thirdBody)
    assert.deepEqual(dispatcher.snapshot(), { queueDepth: 2, inFlight: firstRef })
    releaseFirst()
    await eventually(
      () => durableAttempts.length === 1 && dispatcher.snapshot().inFlight === null,
      `${failedOutcome}: 首个 durable 失败未收束`,
    )
    assert.deepEqual(handlerCalls, [firstRef], `${failedOutcome}: 队列中后续 SX 被旧副本执行`)
    assert.deepEqual(dispatcher.snapshot(), { queueDepth: 0, inFlight: null },
      `${failedOutcome}: 未启动 SX 必须从 FIFO 永久隔离`)

    // 熔断不能堵死配套验证读（用无副作用原语代表同一
    // Dispatcher 槽位）。
    await dispatcher.handleCmd(
      `fuse-read-${failedOutcome}`,
      'session-old',
      'session-old',
      command(Primitive.DebugPing, { id: failedOutcome }),
    )
    await eventually(
      () => results(out.frames, `fuse-read-${failedOutcome}`).length === 1,
      `${failedOutcome}: 熔断误堵非 SX 验证读`,
    )

    // 新 session 的 query/report 覆盖所有 fused ref；旧队列副本已不在
    // dedup/FIFO，因此第二条必须诚实回 unknown。
    await dispatcher.handleQuery(firstRef, 'session-new')
    await dispatcher.handleQuery(secondRef, 'session-new')
    await dispatcher.handleQuery(thirdRef, 'session-new')
    const firstReport = out.frames.find((frame) =>
      frame.kind === Kind.Report && frame.session === 'session-new' && frame.body.ref === firstRef)
    const secondReport = out.frames.find((frame) =>
      frame.kind === Kind.Report && frame.session === 'session-new' && frame.body.ref === secondRef)
    const thirdReport = out.frames.find((frame) =>
      frame.kind === Kind.Report && frame.session === 'session-new' && frame.body.ref === thirdRef)
    assert.equal(firstReport.body.state, 'attempting')
    assert.equal(secondReport.body.state, 'unknown')
    assert.equal(thirdReport.body.state, 'unknown')

    // 只要还有 quarantine，即使所有 query/report 都已齐备，
    // 也不能用任意新 msgId 绕过屏障。
    const wrongBeforeRef = `sx-fuse-wrong-before-${failedOutcome}`
    await dispatcher.handleCmd(
      wrongBeforeRef,
      'session-new',
      'session-new',
      sendMessageCommand(wrongBeforeRef, `idem-fuse-wrong-before-${failedOutcome}`),
    )
    const wrongBeforeAck = out.frames.find((frame) =>
      frame.kind === Kind.Ack && frame.body.ref === wrongBeforeRef)
    assert.equal(wrongBeforeAck?.body.status, AckStatus.Rejected)
    assert.equal(wrongBeforeAck?.body.error.code, ErrorCode.QueueFull)
    assert.deepEqual(handlerCalls, [firstRef], `${failedOutcome}: 错误新 msgId 执行了 handler`)

    // 脑只能以新信封逐条重投原 msgId；quarantine 的旧
    // QueueItem 绝不复活。收束第二条后，第三条仍是唯一可执行 SX。
    await dispatcher.handleCmd(secondRef, 'session-new', 'session-new', secondBody)
    await eventually(
      () => results(out.frames, secondRef).length === 1 && dispatcher.snapshot().inFlight === null,
      `${failedOutcome}: 安全重投新信封未收束`,
    )
    assert.deepEqual(handlerCalls, [firstRef, secondRef],
      `${failedOutcome}: 同 msgId 重投不是恰好一次新执行`)

    const wrongMiddleRef = `sx-fuse-wrong-middle-${failedOutcome}`
    await dispatcher.handleCmd(
      wrongMiddleRef,
      'session-new',
      'session-new',
      sendMessageCommand(wrongMiddleRef, `idem-fuse-wrong-middle-${failedOutcome}`),
    )
    const wrongMiddleAck = out.frames.find((frame) =>
      frame.kind === Kind.Ack && frame.body.ref === wrongMiddleRef)
    assert.equal(wrongMiddleAck?.body.status, AckStatus.Rejected)
    assert.equal(wrongMiddleAck?.body.error.code, ErrorCode.QueueFull)
    assert.deepEqual(handlerCalls, [firstRef, secondRef],
      `${failedOutcome}: 仍有 quarantine 时错误新 msgId 执行了 handler`)

    await dispatcher.handleCmd(thirdRef, 'session-new', 'session-new', thirdBody)
    await eventually(
      () => results(out.frames, thirdRef).length === 1 && dispatcher.snapshot().inFlight === null,
      `${failedOutcome}: 第三条安全重投未收束`,
    )
    assert.deepEqual(handlerCalls, [firstRef, secondRef, thirdRef])

    // quarantine 全部收束后，脑仍只能在同一恢复 session
    // 给出新 SX。这条新信封是剩余 attempting 已由脑侧收束的屏障证词。
    const barrierRef = `sx-fuse-barrier-${failedOutcome}`
    await dispatcher.handleCmd(
      barrierRef,
      'session-new',
      'session-new',
      sendMessageCommand(barrierRef, `idem-fuse-barrier-${failedOutcome}`),
    )
    await eventually(
      () => results(out.frames, barrierRef).length === 1 && dispatcher.snapshot().inFlight === null,
      `${failedOutcome}: quarantine 清空后未解熔`,
    )
    assert.deepEqual(handlerCalls, [firstRef, secondRef, thirdRef, barrierRef])

    await dispatcher.handleCmd(secondRef, 'session-new', 'session-new', secondBody)
    await dispatcher.handleCmd(thirdRef, 'session-new', 'session-new', thirdBody)
    await sleep(10)
    assert.deepEqual(handlerCalls, [firstRef, secondRef, thirdRef, barrierRef],
      `${failedOutcome}: 重投终局后重复 cmd 再次执行了 handler`)
  }
})

test('attempting 写失败返回 WITNESS_UNAVAILABLE/none 且 handler 不会点击', async () => {
  const storage = memoryWitnessStorage({}, {
    beforeSet(items) {
      if (Object.entries(items).some(([key, value]) => key.startsWith('journal:') && value.state === 'attempting')) {
        throw new Error('fixture journal write failed')
      }
    },
  })
  const witness = new WitnessStore(storage, Date.now, () => 'witness-write-fail')
  await witness.initialize()
  const out = recorder()
  let clicks = 0
  let resultID = 0
  const durable = async (session, body) => {
    await witness.enqueueResult({
      proto: 1, kind: 'result', msgId: `write-fail-result-${++resultID}`,
      session, ts: Date.now(), attempt: 1, body,
    })
    out.send(Kind.Result, session, body)
    return 'sent'
  }
  register({
    name: Primitive.ChatSendMessage,
    class: 'effectful',
    async handler(_args, context) {
      await context.beforeSideEffect()
      clicks += 1
      return { status: 'ok', data: {}, evidence: [{ type: 'outboundMessageObserved' }] }
    },
  })
  const dispatcher = new Dispatcher(out.send, undefined, witness, durable)
  await dispatcher.handleCmd('sx-write-fail', 's', 's', sendMessageCommand('sx-write-fail', 'idem-write-fail'))
  await eventually(() => results(out.frames, 'sx-write-fail').length === 1, '证词写失败未返回终局')
  const body = results(out.frames, 'sx-write-fail')[0].body
  assert.equal(clicks, 0)
  assert.equal(body.status, ResultStatus.Failed)
  assert.equal(body.error.code, ErrorCode.WitnessUnavailable)
  assert.equal(body.error.sideEffect, 'none')
  assert.equal(body.error.data.reason, WitnessUnavailableReason.WriteFailed)
})

test('outbox 满载在 attempting 前拒绝真实 SX，零 click 且失败终局不降级 QoS0', async () => {
  const now = 1_700_000_000_000
  const initial = {
    'witness:meta': {
      storeId: 'witness-outbox-full', createdAt: now, schemaVersion: 1,
      journalCount: 0, outboxCount: DEFAULTS.witnessCapacity,
    },
  }
  for (let index = 0; index < DEFAULTS.witnessCapacity; index += 1) {
    const msgId = `full-outbox-${index}`
    initial[`outbox:${msgId}`] = {
      message: {
        proto: 1, kind: 'result', msgId, session: 'session-full', ts: now, attempt: 1,
        body: {
          ref: `old-command-${index}`, status: 'failed', replayed: false, execMs: 0,
          error: {
            code: ErrorCode.WitnessUnavailable,
            data: { reason: WitnessUnavailableReason.CapacityExceeded },
            retryable: Retryable.ManualOnly,
            sideEffect: 'none',
          },
        },
      },
      createdAt: now,
      expiresAt: now + DEFAULTS.outboxTtlDays * 24 * 60 * 60 * 1000,
    }
  }
  const storage = memoryWitnessStorage(initial)
  const witness = new WitnessStore(storage, () => now + 1, () => 'unused-outbox-full')
  await witness.initialize()
  const out = recorder()
  let clicks = 0
  const durableBodies = []
  register({
    name: Primitive.ChatSendMessage,
    class: 'effectful',
    async handler(_args, context) {
      await context.beforeSideEffect()
      clicks += 1
      return {
        status: 'ok',
        data: { conversationRef: 'never', contentHash: 'a'.repeat(64), observedAt: now },
        evidence: [{ type: 'outboundMessageObserved' }],
      }
    },
  })
  const durable = async (_session, body) => {
    durableBodies.push(body)
    try {
      await witness.enqueueResult({
        proto: 1, kind: 'result', msgId: 'full-capacity-terminal', session: 'session-full',
        ts: now + 1, attempt: 1, body,
      })
    } catch {
      return 'dropped'
    }
    assert.fail('满 outbox 不应能持久化新终局')
  }
  const dispatcher = new Dispatcher(out.send, undefined, witness, durable)
  await dispatcher.handleCmd(
    'sx-outbox-full', 'session-full', 'session-full',
    sendMessageCommand('sx-outbox-full', 'idem-outbox-full'),
  )
  await eventually(() => durableBodies.length === 1, '容量失败终局未进入 durable 尝试')
  assert.equal(clicks, 0)
  assert.equal(durableBodies[0].error.code, ErrorCode.WitnessUnavailable)
  assert.equal(durableBodies[0].error.sideEffect, 'none')
  assert.equal(durableBodies[0].error.data.reason, WitnessUnavailableReason.CapacityExceeded)
  assert.equal(out.frames.some((frame) => frame.kind === Kind.Result), false,
    'durable 入箱失败必须保持静默并由连接层断链，不能 QoS0 提前显示失败')
  assert.equal(await witness.findJournalByIdemKey('idem-outbox-full'), null)
})

test('Dispatcher pre-read 拒绝 same idem/different ref，不执行且不回放旧 result', async () => {
  const storage = memoryWitnessStorage()
  const witness = new WitnessStore(storage, Date.now, () => 'witness-pre-read')
  await witness.initialize()
  await witness.markAttempting('sx-old-ref', 'idem-pre-read')
  const out = recorder()
  let calls = 0
  register({
    name: Primitive.ChatSendMessage,
    class: 'effectful',
    async handler() { calls += 1; return { status: 'failed', error: {
      code: ErrorCode.InternalHand, retryable: Retryable.ManualOnly, sideEffect: 'possible',
    } } },
  })
  const dispatcher = new Dispatcher(out.send, undefined, witness, async (session, body) => {
    out.send(Kind.Result, session, body)
    return 'sent'
  })
  await dispatcher.handleCmd('sx-new-ref', 's', 's', sendMessageCommand('sx-new-ref', 'idem-pre-read'))
  assert.equal(calls, 0)
  const ack = out.frames.find((frame) => frame.kind === Kind.Ack && frame.body.ref === 'sx-new-ref')
  assert.equal(ack.body.status, AckStatus.Accepted)
  const body = results(out.frames, 'sx-new-ref')[0].body
  assert.equal(body.error.code, ErrorCode.WitnessUnavailable)
  assert.equal(body.error.data.reason, WitnessUnavailableReason.StoreCorrupt)
  assert.equal(out.frames.some((frame) => frame.kind === Kind.Result && frame.body.ref === 'sx-old-ref'), false)
})

test('真实 SX ok 缺 evidence 被降为 INTERNAL_HAND，barrier 后仍持久 committed 失败终局', async () => {
  const storage = memoryWitnessStorage()
  const witness = new WitnessStore(storage, Date.now, () => 'witness-evidence')
  await witness.initialize()
  const out = recorder()
  register({
    name: Primitive.ChatSendMessage,
    class: 'effectful',
    async handler(_args, context) {
      await context.beforeSideEffect()
      return {
        status: 'ok',
        data: { conversationRef: 'conversation-fixture', contentHash: 'd'.repeat(64), observedAt: Date.now() },
      }
    },
  })
  const dispatcher = new Dispatcher(out.send, undefined, witness, async (session, body, commitIdemKey) => {
    const envelope = {
      proto: 1, kind: 'result', msgId: 'evidence-result', session, ts: Date.now(), attempt: 1, body,
    }
    if (commitIdemKey) await witness.commitAndEnqueue(commitIdemKey, envelope)
    else await witness.enqueueResult(envelope)
    out.send(Kind.Result, session, body)
    return 'sent'
  })
  await dispatcher.handleCmd('sx-evidence', 's', 's', sendMessageCommand('sx-evidence', 'idem-evidence'))
  await eventually(() => results(out.frames, 'sx-evidence').length === 1, 'evidence 门禁未收束')
  const body = results(out.frames, 'sx-evidence')[0].body
  assert.equal(body.status, ResultStatus.Failed)
  assert.equal(body.error.code, ErrorCode.InternalHand)
  assert.equal(body.error.sideEffect, 'possible')
  assert.equal(storage.state['journal:idem-evidence'].state, 'committed')
  assert.deepEqual(storage.state['journal:idem-evidence'].result, body)
})

test('effectful 超限降级使用允许 possible 的 INTERNAL_HAND 形态', async () => {
  const compact = {
    ref: 'sx-too-large', status: ResultStatus.Failed, replayed: false, execMs: 0,
    error: {
      code: ErrorCode.InternalHand,
      message: 'result 完整信封超过 maxMsgBytes',
      retryable: Retryable.ManualOnly,
      sideEffect: 'possible',
    },
  }
  assert.deepEqual(validatePrimitiveResult(Primitive.ChatSendMessage, 1, compact), [])
  assert.equal(ERROR_CODE_META[ErrorCode.InternalHand].sideEffect.includes('possible'), true)
  assert.equal(ERROR_CODE_META[ErrorCode.ProtoMsgTooLarge].sideEffect.includes('possible'), false)
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

function installM4CurrentCandidateFixture() {
  const original = {
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
  }
  const refs = {
    resume: 'fixture-resume-private-current',
    otherResume: 'fixture-resume-private-other',
    user: 'fixture-user-stable-current',
    otherUser: 'fixture-user-stable-other',
    job: 'fixture-job-stable-current',
  }
  const element = (textContent = '') => ({
    textContent,
    disabled: false,
    getClientRects() { return [{}] },
    click() { state.clicks += 1 },
  })
  const name = element(' 合成 候选人 ')
  const title = element(' 合成 职位 ')
  const button = element('打招呼')
  const detail = element()
  const root = { _route: { query: { jobNumber: refs.job } } }
  const store = { state: { talent: { activeJob: { jobNumber: refs.job, jobTitle: '合成 职位' } } } }
  const matchedOwner = {
    _props: { source: { resumeNumber: refs.resume, userMasterId: refs.user } },
    $root: root,
    $store: store,
  }
  const otherOwner = {
    _props: { source: { resumeNumber: refs.otherResume, userMasterId: refs.otherUser } },
    $root: root,
    $store: store,
  }
  const items = [{ __vue__: matchedOwner }, { __vue__: otherOwner }]
  const state = {
    details: [detail],
    items,
    names: [name],
    titles: [title],
    buttons: [button],
    clicks: 0,
  }
  detail.querySelectorAll = (selector) => {
    if (selector === '.resume-basic-new__name') return state.names
    if (selector === 'button[type="button"]') return state.buttons
    return []
  }
  globalThis.location = {
    href: `https://rd6.zhaopin.com/app/recommend?resumeNumber=${encodeURIComponent(refs.resume)}` +
      `&jobNumber=${encodeURIComponent(refs.job)}`,
  }
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.document = {
    querySelectorAll(selector) {
      if (selector === '.new-shortcut-resume__modal') return state.details
      if (selector === '[role="listitem"]') return state.items
      if (selector === '.job-pane__item--active .job-pane__item-job-title') return state.titles
      return []
    },
  }
  return {
    refs,
    state,
    matchedOwner,
    otherOwner,
    restore() {
      globalThis.document = original.document
      globalThis.location = original.location
      globalThis.getComputedStyle = original.getComputedStyle
    },
  }
}

test('candidate.readCurrent MAIN 只读唯一详情并以瞬时 resume join 返回稳定身份', () => {
  const fixture = installM4CurrentCandidateFixture()
  try {
    const result = zhilianTestHooks.mainReadCurrentCandidate()
    assert.deepEqual(result, {
      status: 'ready',
      data: {
        platformUserRef: fixture.refs.user,
        displayName: '合成 候选人',
        positionRef: fixture.refs.job,
        positionTitle: '合成 职位',
        contactState: 'unestablished',
      },
    })
    assert.equal(JSON.stringify(result).includes(fixture.refs.resume), false,
      'resumeNumber 只能留在同次 MAIN 的瞬时 join 中')
    assert.equal(fixture.state.clicks, 0, 'readonly evaluator 不得调用页面动作')

    fixture.state.buttons = []
    const unknown = zhilianTestHooks.mainReadCurrentCandidate()
    assert.equal(unknown.status, 'ready')
    assert.equal(unknown.data.contactState, 'unknown', '关系正证不足时不得猜 established')
  } finally {
    fixture.restore()
  }
})

test('candidate.readCurrent MAIN 对身份、绑定与职位歧义逐项失败关闭', () => {
  const cases = [
    ['没有打开详情', (fixture) => { fixture.state.details = [] }, 'detail_absent'],
    ['同时出现多个详情', (fixture) => {
      fixture.state.details = [fixture.state.details[0], { ...fixture.state.details[0] }]
    }, 'detail_cardinality'],
    ['来源私有通道缺失', (fixture) => { delete fixture.state.items[0].__vue__ }, 'list_source_unavailable'],
    ['resume 瞬时连接重复', (fixture) => {
      fixture.otherOwner._props.source.resumeNumber = fixture.refs.resume
    }, 'detail_binding_ambiguous'],
    ['userMasterId 同窗重复', (fixture) => {
      fixture.otherOwner._props.source.userMasterId = fixture.refs.user
    }, 'candidate_identity_duplicated'],
    ['职位三值不等', (fixture) => {
      fixture.matchedOwner.$root._route.query.jobNumber = 'fixture-job-route-drift'
    }, 'position_identity_mismatch'],
    ['职位标题冲突', (fixture) => {
      fixture.matchedOwner.$store.state.talent.activeJob.jobTitle = '另一职位'
    }, 'position_title_mismatch'],
  ]
  for (const [name, mutate, expectedReason] of cases) {
    const fixture = installM4CurrentCandidateFixture()
    try {
      mutate(fixture)
      assert.deepEqual(zhilianTestHooks.mainReadCurrentCandidate(), {
        status: 'failed',
        reason: expectedReason,
      }, name)
    } finally {
      fixture.restore()
    }
  }
})

test('candidate.readCurrent 只用跨窗口唯一推荐页详情且账号前后复核', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = 'a'.repeat(64)
  const tab = {
    id: 401,
    active: true,
    status: 'complete',
    url: 'https://rd6.zhaopin.com/app/recommend?resumeNumber=private&jobNumber=private',
  }
  const emptyBackgroundTab = { ...tab, id: 402 }
  const mainCalls = []
  const queryCalls = []
  const progress = []
  globalThis.chrome = {
    tabs: {
      async query(query) {
        queryCalls.push(structuredClone(query))
        return [{ ...emptyBackgroundTab }, { ...tab }]
      },
      async sendMessage(id, message) {
        assert.ok(id === tab.id || id === emptyBackgroundTab.id)
        assert.equal(message.type, 'recruithelper.content.probe')
        return { ok: true }
      },
    },
    scripting: {
      async executeScript({ target, func }) {
        assert.ok(target.tabId === tab.id || target.tabId === emptyBackgroundTab.id)
        mainCalls.push([target.tabId, func.name])
        if (func.name === 'mainProbeZhilian') return [{ result: {
          pageKind: 'recommend', loginState: 'in', principalFingerprint: fingerprint,
          imListVisible: false,
        } }]
        if (target.tabId === emptyBackgroundTab.id) return [{ result: {
          status: 'failed', reason: 'detail_absent',
        } }]
        if (func.name === 'mainReadCurrentCandidate') return [{ result: {
          status: 'ready',
          data: {
            platformUserRef: 'fixture-user', displayName: '合成候选人',
            positionRef: 'fixture-job', positionTitle: '合成职位', contactState: 'unestablished',
          },
        } }]
        throw new Error(`unexpected MAIN ${func.name}`)
      },
    },
  }
  try {
    const data = await readZhilianCurrentCandidate({
      signal: new AbortController().signal,
      cmdMsgId: 'read-current-fixture',
      deadlineMs: Date.now() + 10_000,
      irreversibleNotAfterMs: Date.now() + 10_000,
      commandContext: undefined,
      guards: undefined,
      checkpoint() {},
      async beforeSideEffect() { throw new Error('readonly 不得进入动作栅栏') },
      async progress(label, percent) { progress.push([label, percent]) },
    }, fingerprint)
    assert.equal(data.platformUserRef, 'fixture-user')
    assert.deepEqual(queryCalls, [
      { url: 'https://rd6.zhaopin.com/*' },
      { url: 'https://rd6.zhaopin.com/*' },
    ])
    assert.deepEqual(mainCalls, [
      [emptyBackgroundTab.id, 'mainProbeZhilian'],
      [emptyBackgroundTab.id, 'mainReadCurrentCandidate'],
      [tab.id, 'mainProbeZhilian'],
      [tab.id, 'mainReadCurrentCandidate'],
      [emptyBackgroundTab.id, 'mainProbeZhilian'],
      [emptyBackgroundTab.id, 'mainReadCurrentCandidate'],
      [tab.id, 'mainProbeZhilian'],
      [tab.id, 'mainReadCurrentCandidate'],
    ])
    assert.equal(progress.at(-1)[1], 100)
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('candidate.readCurrent 切页或 MAIN 阴性只返回固定脱敏失败', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = 'b'.repeat(64)
  const privateResume = 'private-resume-must-not-leak'
  let queryCount = 0
  let snapshot = { status: 'failed', reason: 'detail_binding_ambiguous', privateResume }
  const firstTab = {
    id: 501, active: true, status: 'complete',
    url: `https://rd6.zhaopin.com/app/recommend?resumeNumber=${privateResume}&jobNumber=private`,
  }
  globalThis.chrome = {
    tabs: {
      async query() { queryCount += 1; return [{ ...firstTab }] },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func }) {
        if (func.name === 'mainProbeZhilian') return [{ result: {
          pageKind: 'recommend', loginState: 'in', principalFingerprint: fingerprint,
          imListVisible: false,
        } }]
        return [{ result: snapshot }]
      },
    },
  }
  const context = {
    signal: new AbortController().signal,
    cmdMsgId: 'read-current-negative',
    deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined,
    guards: undefined,
    checkpoint() {}, async beforeSideEffect() {}, async progress() {},
  }
  try {
    await assert.rejects(readZhilianCurrentCandidate(context, fingerprint), (error) => {
      assert.ok(error instanceof ZhilianPlatformError)
      assert.equal(error.code, 'ELEMENT_UNRESOLVED')
      assert.equal(error.message.includes(privateResume), false)
      return true
    })

    queryCount = 0
    snapshot = {
      status: 'ready',
      data: {
        platformUserRef: 'fixture-user', displayName: null,
        positionRef: 'fixture-job', positionTitle: null, contactState: 'unknown',
      },
    }
    globalThis.chrome.tabs.query = async () => {
      queryCount += 1
      return [{ ...firstTab, id: queryCount === 1 ? firstTab.id : firstTab.id + 1 }]
    }
    await assert.rejects(readZhilianCurrentCandidate(context, fingerprint), (error) => {
      assert.ok(error instanceof ZhilianPlatformError)
      assert.equal(error.code, 'CTX_LOST_DURING_EXEC')
      assert.equal(error.message.includes(privateResume), false)
      return true
    })
  } finally {
    globalThis.chrome = originalChrome
  }
})

function installM4GreetingFixture(options = {}) {
  const original = {
    window: globalThis.window,
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
    HTMLElement: globalThis.HTMLElement,
    HTMLButtonElement: globalThis.HTMLButtonElement,
    HTMLTextAreaElement: globalThis.HTMLTextAreaElement,
    InputEvent: globalThis.InputEvent,
    Event: globalThis.Event,
  }
  const refs = {
    resume: 'fixture-resume-greeting',
    user: 'fixture-user-greeting',
    job: 'fixture-job-greeting',
  }
  const text = '你好'
  const state = {
    modalVisible: options.existingModal === true,
    customSelected: options.existingModal === true || options.customInitiallySelected === true,
    textareaVisible: options.existingModal === true,
    editIconStyleHidden: options.editIconStyleHidden === true,
    defaultChecked: options.defaultChecked === true,
    directUnsafe: options.directUnsafe === true,
    detailCount: options.detailCount ?? 1,
    listItemCount: options.listItemCount ?? 1,
    greetingButtonCount: options.greetingButtonCount ?? 1,
    openClicks: 0,
    optionClicks: 0,
    editClicks: 0,
    finalClicks: 0,
    instanceClicks: 0,
    candidateVisibleActions: 0,
    checkboxClicks: 0,
    textareaEvents: [],
    throwOnReadAfterFinal: false,
  }

  class FixtureEvent {
    constructor(type, init = {}) { this.type = type; Object.assign(this, init) }
  }
  class FixtureHTMLElement {
    constructor(textContent = '') {
      this.textContent = textContent
      this.isConnected = true
      this.form = null
      this.type = 'button'
      this._classes = new Set()
      this.classList = { contains: (name) => this._classes.has(name) }
      this._onIntrinsicClick = null
    }
    getClientRects() { return [{}] }
    getAttribute() { return null }
    querySelector() { return null }
    querySelectorAll() { return [] }
    click() {
      if (typeof this._onIntrinsicClick === 'function') this._onIntrinsicClick()
    }
  }
  class FixtureTextArea extends FixtureHTMLElement {
    constructor(value) {
      super()
      this._value = value
    }
    get value() { return this._value }
    set value(value) { this._value = String(value) }
    dispatchEvent(event) {
      state.textareaEvents.push(event.type)
      return true
    }
  }
  globalThis.HTMLElement = FixtureHTMLElement
  globalThis.HTMLButtonElement = FixtureHTMLElement
  globalThis.HTMLTextAreaElement = FixtureTextArea
  globalThis.InputEvent = FixtureEvent
  globalThis.Event = FixtureEvent

  const opener = new FixtureHTMLElement('打招呼')
  opener._onIntrinsicClick = () => {
    state.openClicks += 1
    if (state.directUnsafe) {
      state.candidateVisibleActions += 1
      return
    }
    state.modalVisible = true
  }
  if (state.directUnsafe) {
    // 代表当前公开动作表面不再是批次 0 已证实的纯两步按钮；若误点就会产生外部动作。
    opener.form = {}
    opener.type = 'submit'
  }
  const secondOpener = new FixtureHTMLElement('打招呼')
  const detail = new FixtureHTMLElement()
  detail.querySelectorAll = (selector) => selector === 'button[type="button"]'
    ? [opener, secondOpener].slice(0, state.greetingButtonCount)
    : []
  const secondDetail = new FixtureHTMLElement()

  const aiOption = new FixtureHTMLElement('AI 招呼')
  aiOption.querySelector = (selector) => selector === '.ai-greeting-modal__ai-icon' ? {} : null
  const customOption = new FixtureHTMLElement('统一招呼')
  customOption.classList = { contains: (name) => name === 'is-selected' && state.customSelected }
  customOption._onIntrinsicClick = () => {
    state.optionClicks += 1
    state.customSelected = true
  }
  const textarea = new FixtureTextArea(options.existingDraft ?? '平台原始招呼')
  const editIcon = new FixtureHTMLElement()
  editIcon._onIntrinsicClick = () => {
    state.editClicks += 1
    state.textareaVisible = true
  }
  customOption.querySelector = () => null
  customOption.querySelectorAll = (selector) => {
    if (selector === '.ai-greeting-modal__edit-area textarea') {
      return state.textareaVisible ? [textarea] : []
    }
    if (selector === '.ai-greeting-modal__edit-icon') return state.textareaVisible ? [] : [editIcon]
    return []
  }

  const checkboxInput = new FixtureHTMLElement()
  Object.defineProperty(checkboxInput, 'checked', { get: () => state.defaultChecked })
  checkboxInput._onIntrinsicClick = () => { state.checkboxClicks += 1 }
  const defaultControl = new FixtureHTMLElement('设置为默认')
  defaultControl.querySelectorAll = (selector) => selector === 'input[type="checkbox"]' ? [checkboxInput] : []
  defaultControl.querySelector = () => null

  const sendButton = new FixtureHTMLElement('发送')
  sendButton._onIntrinsicClick = () => { state.finalClicks += 1 }
  sendButton.click = () => { state.instanceClicks += 1 }
  const footer = new FixtureHTMLElement()
  footer.querySelectorAll = (selector) => selector === 'button[type="button"]' ? [sendButton] : []
  const modal = new FixtureHTMLElement()
  modal.querySelectorAll = (selector) => {
    if (selector === '.ai-greeting-modal__option') return [aiOption, customOption]
    if (selector === '.km-checkbox') return [defaultControl]
    if (selector === '.ai-greeting-modal__footer') return [footer]
    return []
  }

  const owner = {
    _props: { source: { resumeNumber: refs.resume, userMasterId: refs.user } },
    $root: { _route: { query: { jobNumber: refs.job } } },
    $store: { state: { talent: { activeJob: { jobNumber: refs.job } } } },
  }
  const listItem = new FixtureHTMLElement()
  listItem.__vue__ = owner
  const staffId = 'staff-m4-greeting'
  const orgId = 'org-m4-greeting'
  const loginPoint = 'login-m4-greeting'
  const principal = ['zhilian-principal-v2', staffId, orgId, loginPoint]
    .map((piece) => `${new TextEncoder().encode(piece).length}:${piece}`).join('|')
  globalThis.window = {
    $session: {
      isLoggedIn: true,
      staff: { staffId, defaultLoginPoint: loginPoint },
      org: { orgId },
    },
  }
  globalThis.location = {
    href: `https://rd6.zhaopin.com/app/recommend?resumeNumber=${refs.resume}&jobNumber=${refs.job}`,
  }
  globalThis.getComputedStyle = (element) => ({
    display: 'block',
    visibility: element === editIcon && state.editIconStyleHidden ? 'hidden' : 'visible',
  })
  globalThis.document = {
    scripts: [],
    querySelectorAll(selector) {
      if (state.throwOnReadAfterFinal && state.finalClicks > 0) {
        throw new Error('最终 click 后不得继续读页面')
      }
      if (selector === '.new-shortcut-resume__modal') {
        return [detail, secondDetail].slice(0, state.detailCount)
      }
      if (selector === '[role="listitem"]') return state.listItemCount === 0 ? [] : [listItem]
      if (selector === '.ai-greeting-modal') return state.modalVisible ? [modal] : []
      return []
    },
  }
  const fingerprint = createHash('sha256').update(principal).digest('hex')
  const invoke = (phase, expectedOwnedDraft = phase === 'prepare' ? '' : text) =>
    zhilianTestHooks.mainSendGreetingOnce(
      refs.user,
      refs.job,
      text,
      fingerprint,
      Date.now() + 10_000,
      expectedOwnedDraft,
      phase,
    )
  return {
    detail,
    invoke,
    listItem,
    owner,
    refs,
    restore() { Object.assign(globalThis, original) },
    sendButton,
    state,
    text,
    textarea,
  }
}

function installM4GreetingOrchestrationFixture(options = {}) {
  const original = {
    chrome: globalThis.chrome,
    setTimeout: globalThis.setTimeout,
  }
  const fingerprint = 'a'.repeat(64)
  const refs = {
    user: 'fixture-user-greeting-orchestration',
    job: 'fixture-job-greeting-orchestration',
    conversation: 'fixture-conversation-greeting-orchestration',
  }
  const text = '你好'
  const contentHash = createHash('sha256').update(text).digest('hex')
  const state = {
    phases: [],
    proofCalls: 0,
    finalClicks: 0,
    barriers: 0,
  }
  const tabCount = options.tabCount ?? 1
  const tabs = Array.from({ length: tabCount }, (_, index) => ({
    id: 701 + index,
    active: index === 0,
    status: 'complete',
    url: `https://rd6.zhaopin.com/app/recommend?resumeNumber=private-${index}&jobNumber=private`,
  }))
  const currentData = (tabId) => ({
    platformUserRef: options.currentUser ?? refs.user,
    displayName: '合成候选人',
    positionRef: options.currentJob ?? refs.job,
    positionTitle: '合成职位',
    contactState: options.contactState ?? 'unestablished',
    ...(options.currentDataByTab?.(tabId) ?? {}),
  })
  const phaseResult = (phase) => {
    const configured = options[`${phase}Result`]
    if (configured === 'throw') throw new Error(`fixture-${phase}-death`)
    if (configured !== undefined) return structuredClone(configured)
    if (phase === 'prepare') return { status: 'prepared' }
    if (phase === 'preflight') return { status: 'ready' }
    return { status: 'clicked' }
  }

  // 压缩 production observer 的等待，不触碰 Dispatcher 的秒级 deadline/execBudget timer。
  globalThis.setTimeout = (callback, delay, ...args) => original.setTimeout(
    callback,
    delay === 250 || delay === 120 ? 0 : delay,
    ...args,
  )
  globalThis.chrome = {
    tabs: {
      async query() { return tabs.map((tab) => ({ ...tab })) },
      async get(id) {
        const tab = tabs.find((candidate) => candidate.id === id)
        if (!tab) throw new Error('fixture-tab-absent')
        return { ...tab }
      },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ target, func, args }) {
        if (func.name === 'mainProbeZhilian') return [{ result: {
          pageKind: 'recommend', loginState: 'in', principalFingerprint: fingerprint,
          imListVisible: false,
        } }]
        if (func.name === 'mainReadCurrentCandidate') return [{ result: {
          status: 'ready', data: currentData(target.tabId),
        } }]
        if (func.name === 'mainSendGreetingOnce') {
          const phase = args.at(-1)
          state.phases.push(phase)
          const result = phaseResult(phase)
          if (phase === 'commit' && result.status === 'clicked') state.finalClicks += 1
          return [{ result }]
        }
        if (func.name === 'mainReadGreetingProof') {
          state.proofCalls += 1
          if (options.proofMode === 'throw') throw new Error('fixture-observer-death')
          if (options.proofMode === 'negative') return [{ result: { confirmed: false } }]
          if (options.proofMode === 'unstable') return [{ result: {
            confirmed: true,
            conversationRef: refs.conversation,
            contentHash,
            proofToken: (state.proofCalls % 2 === 0 ? 'b' : 'c').repeat(64),
          } }]
          return [{ result: {
            confirmed: true,
            conversationRef: refs.conversation,
            contentHash,
            proofToken: 'b'.repeat(64),
          } }]
        }
        throw new Error(`unexpected MAIN ${func.name}`)
      },
    },
  }

  return {
    contentHash,
    fingerprint,
    refs,
    state,
    text,
    context() {
      return {
        signal: new AbortController().signal,
        cmdMsgId: 'send-greeting-matrix',
        deadlineMs: Date.now() + 60_000,
        irreversibleNotAfterMs: Date.now() + 60_000,
        commandContext: undefined,
        guards: undefined,
        checkpoint() {},
        async beforeSideEffect() {
          state.barriers += 1
          if (options.barrierThrows) throw new Error('fixture-attempting-write-death')
        },
        async progress() {},
      }
    },
    restore() { Object.assign(globalThis, original) },
  }
}

test('M4 招呼 prepare 完成全部编辑，attempting 后同一 evaluator 只做最终 intrinsic click', async () => {
  const fixture = installM4GreetingFixture()
  try {
    assert.deepEqual(await fixture.invoke('prepare'), { status: 'prepared' })
    assert.equal(fixture.textarea.value, fixture.text)
    assert.deepEqual(fixture.state.textareaEvents, ['input', 'change'])
    assert.deepEqual(await fixture.invoke('preflight'), { status: 'ready' })
    fixture.state.throwOnReadAfterFinal = true
    assert.deepEqual(await fixture.invoke('commit'), { status: 'clicked' })
    assert.equal(fixture.state.openClicks, 1)
    assert.equal(fixture.state.optionClicks, 1)
    assert.equal(fixture.state.editClicks, 1)
    assert.equal(fixture.state.finalClicks, 1)
    assert.equal(fixture.state.instanceClicks, 0, '最终动作必须绕过页面替换过的 instance click')
    assert.equal(fixture.state.checkboxClicks, 0)
    assert.deepEqual(fixture.state.textareaEvents, ['input', 'change'],
      'preflight/commit 不得再次写入或恢复 textarea')
  } finally {
    fixture.restore()
  }
})

test('M4 招呼 prepare 可点击 DOM 内样式隐藏的唯一编辑图标', async () => {
  const fixture = installM4GreetingFixture({
    customInitiallySelected: true,
    editIconStyleHidden: true,
  })
  try {
    assert.deepEqual(await fixture.invoke('prepare'), { status: 'prepared' })
    assert.equal(fixture.state.optionClicks, 0, '已选自定义项不得再 click')
    assert.equal(fixture.state.editClicks, 1)
    assert.equal(fixture.textarea.value, fixture.text)
    assert.deepEqual(fixture.state.textareaEvents, ['input', 'change'])
    assert.equal(fixture.state.finalClicks, 0, 'prepare 不得触碰最终发送')
  } finally {
    fixture.restore()
  }
})

test('M4 招呼不接管既有编辑器，不改默认项，公开两步拓扑不成立时零动作', async () => {
  const existing = installM4GreetingFixture({ existingModal: true, existingDraft: '人工草稿' })
  try {
    assert.deepEqual(await existing.invoke('prepare'), { status: 'failed', reason: 'existing_editor' })
    assert.equal(existing.textarea.value, '人工草稿')
    assert.equal(existing.state.openClicks, 0)
    assert.deepEqual(existing.state.textareaEvents, [])
  } finally {
    existing.restore()
  }

  const checked = installM4GreetingFixture({ defaultChecked: true })
  try {
    assert.deepEqual(await checked.invoke('prepare'), {
      status: 'failed', reason: 'default_setting_selected',
    })
    assert.equal(checked.state.finalClicks, 0)
    assert.equal(checked.state.checkboxClicks, 0, '不得替真人取消平台默认招呼设置')
    assert.equal(checked.textarea.value, '平台原始招呼', '默认项被选中时不得写正文')
  } finally {
    checked.restore()
  }

  const direct = installM4GreetingFixture({ directUnsafe: true })
  try {
    assert.deepEqual(await direct.invoke('prepare'), {
      status: 'failed', reason: 'two_step_surface_unavailable',
    })
    assert.equal(direct.state.openClicks, 0)
    assert.equal(direct.state.candidateVisibleActions, 0,
      '第一击可能直接发送的公开拓扑不得试点动作')
  } finally {
    direct.restore()
  }
})

test('M4 招呼 preflight 后世界变化时 commit 零最终动作', async () => {
  const fixture = installM4GreetingFixture()
  try {
    assert.deepEqual(await fixture.invoke('prepare'), { status: 'prepared' })
    assert.deepEqual(await fixture.invoke('preflight'), { status: 'ready' })
    fixture.textarea.value = '真人改写'
    assert.deepEqual(await fixture.invoke('commit'), { status: 'failed', reason: 'editor_changed' })
    assert.equal(fixture.state.finalClicks, 0)
    assert.equal(fixture.textarea.value, '真人改写', 'commit 不得覆盖或恢复真人的新输入')
  } finally {
    fixture.restore()
  }
})

test('M4 招呼动作前对目标、职位、关系和零/多表面变化全部失败关闭', async () => {
  const beforePrepare = [
    {
      label: '零详情目标',
      options: { detailCount: 0 },
      expectedReason: 'two_step_surface_unavailable',
    },
    {
      label: '多个详情目标',
      options: { detailCount: 2 },
      expectedReason: 'two_step_surface_unavailable',
    },
    {
      label: '已有关系或关系无法确证',
      options: { greetingButtonCount: 0 },
      expectedReason: 'two_step_surface_unavailable',
    },
    {
      label: '多个招呼入口',
      options: { greetingButtonCount: 2 },
      expectedReason: 'two_step_surface_unavailable',
    },
    {
      label: '候选人绑定变化',
      mutate(fixture) { fixture.owner._props.source.userMasterId = 'fixture-other-user' },
      expectedReason: 'two_step_surface_unavailable',
    },
    {
      label: '职位绑定变化',
      mutate(fixture) { fixture.owner.$store.state.talent.activeJob.jobNumber = 'fixture-other-job' },
      expectedReason: 'two_step_surface_unavailable',
    },
  ]
  for (const scenario of beforePrepare) {
    const fixture = installM4GreetingFixture(scenario.options)
    try {
      scenario.mutate?.(fixture)
      assert.deepEqual(await fixture.invoke('prepare'), {
        status: 'failed', reason: scenario.expectedReason,
      }, scenario.label)
      assert.equal(fixture.state.openClicks, 0, `${scenario.label}: 不得打开编辑器`)
      assert.equal(fixture.state.finalClicks, 0, `${scenario.label}: 不得调用最终发送`)
      assert.ok(fixture.state.finalClicks + fixture.state.candidateVisibleActions <= 1,
        `${scenario.label}: 候选人可见动作不得超过一次`)
    } finally {
      fixture.restore()
    }
  }

  for (const scenario of [
    {
      label: 'prepare 后候选人变化',
      mutate(fixture) { fixture.owner._props.source.userMasterId = 'fixture-other-user' },
    },
    {
      label: 'prepare 后职位变化',
      mutate(fixture) { fixture.owner.$root._route.query.jobNumber = 'fixture-other-job' },
    },
    {
      label: 'prepare 后关系入口消失',
      mutate(fixture) { fixture.state.greetingButtonCount = 0 },
    },
  ]) {
    const fixture = installM4GreetingFixture()
    try {
      assert.deepEqual(await fixture.invoke('prepare'), { status: 'prepared' }, scenario.label)
      scenario.mutate(fixture)
      assert.deepEqual(await fixture.invoke('preflight'), {
        status: 'failed', reason: 'relationship_changed',
      }, scenario.label)
      assert.equal(fixture.state.finalClicks, 0, `${scenario.label}: attempting 前必须零最终动作`)
      assert.ok(fixture.state.finalClicks + fixture.state.candidateVisibleActions <= 1,
        `${scenario.label}: 候选人可见动作不得超过一次`)
    } finally {
      fixture.restore()
    }
  }
})

test('M4 招呼验证读只接受候选人职位唯一会话中的唯一服务端我方招呼', async () => {
  const original = { window: globalThis.window, document: globalThis.document }
  const refs = {
    user: 'fixture-user-greeting-proof', job: 'fixture-job-greeting-proof',
    conversation: 'fixture-conversation-greeting-proof', staff: 'fixture-staff-greeting-proof',
  }
  const contentHash = createHash('sha256').update('你好').digest('hex')
  const rows = [{
    idServer: 'fixture-server-greeting-proof', status: 'success', type: 'custom', from: refs.staff,
    content: JSON.stringify({ type: 131, content: JSON.stringify({ greetingText: '你好' }) }),
  }]
  const sessions = [{
    sessionId: refs.conversation,
    jobNumber: refs.job,
    userId: refs.user,
    typeUserId: refs.user,
    peerPartnerId: refs.user,
  }]
  let pageCalls = 0
  globalThis.document = { scripts: [] }
  globalThis.window = {
    $session: { staff: { staffId: refs.staff } },
    imEngine: {
      async getSessions({ pageNo, pageSize }) {
        pageCalls += 1
        assert.equal(pageNo, 1)
        assert.equal(pageSize, 8)
        return { curSessions: sessions, hasMoreSession: false }
      },
      async getHistoryMsgs({ to }) {
        assert.equal(to, refs.user)
        return rows
      },
    },
  }
  try {
    const first = await zhilianTestHooks.mainReadGreetingProof(refs.user, refs.job, contentHash)
    const second = await zhilianTestHooks.mainReadGreetingProof(refs.user, refs.job, contentHash)
    assert.equal(first.confirmed, true)
    assert.equal(first.conversationRef, refs.conversation)
    assert.equal(first.contentHash, contentHash)
    assert.match(first.proofToken, /^[0-9a-f]{64}$/)
    assert.deepEqual(second, first, '同一服务端 id 的两次正采样必须稳定')
    assert.equal(pageCalls, 2)

    const onlySession = structuredClone(sessions[0])
    sessions.splice(0)
    assert.deepEqual(
      await zhilianTestHooks.mainReadGreetingProof(refs.user, refs.job, contentHash),
      { confirmed: false },
      '零会话不得构成正证',
    )
    sessions.push(onlySession, { ...onlySession, sessionId: 'fixture-conversation-greeting-other' })
    assert.deepEqual(
      await zhilianTestHooks.mainReadGreetingProof(refs.user, refs.job, contentHash),
      { confirmed: false },
      '多个候选人职位会话不得任取一个认领',
    )
    sessions.splice(1)

    const onlyRow = structuredClone(rows[0])
    rows.splice(0)
    assert.deepEqual(
      await zhilianTestHooks.mainReadGreetingProof(refs.user, refs.job, contentHash),
      { confirmed: false },
      '唯一会话中零条同 hash 服务端招呼不得构成正证',
    )
    rows.push(onlyRow)
    rows.push({ ...rows[0], idServer: 'fixture-server-greeting-duplicate' })
    assert.deepEqual(
      await zhilianTestHooks.mainReadGreetingProof(refs.user, refs.job, contentHash),
      { confirmed: false },
      '两条同文服务端招呼不得被认作唯一正证',
    )
    rows.splice(1)
    sessions[0].jobNumber = 'fixture-job-other'
    assert.deepEqual(
      await zhilianTestHooks.mainReadGreetingProof(refs.user, refs.job, contentHash),
      { confirmed: false },
      '职位不一致不得认领新会话',
    )
  } finally {
    Object.assign(globalThis, original)
  }
})

test('sendZhilianGreeting 在零/多目标、意图目标变化和已有关系时停在 attempting 前', async () => {
  const scenarios = [
    { label: '零目标', options: { tabCount: 0 }, code: ErrorCode.CtxNotReady },
    { label: '多个目标', options: { tabCount: 2 }, code: ErrorCode.ElementUnresolved },
    {
      label: '候选人变化',
      options: { currentUser: 'fixture-other-user' },
      code: ErrorCode.GuardFailed,
    },
    {
      label: '职位变化',
      options: { currentJob: 'fixture-other-job' },
      code: ErrorCode.GuardFailed,
    },
    {
      label: '已有关系',
      options: { contactState: 'established' },
      code: ErrorCode.GuardFailed,
    },
    {
      label: '关系无法确证',
      options: { contactState: 'unknown' },
      code: ErrorCode.GuardFailed,
    },
  ]
  for (const scenario of scenarios) {
    const fixture = installM4GreetingOrchestrationFixture(scenario.options)
    try {
      await assert.rejects(
        sendZhilianGreeting(
          { platformUserRef: fixture.refs.user, positionRef: fixture.refs.job, text: fixture.text },
          { expectUnestablished: true },
          fixture.context(),
          fixture.fingerprint,
        ),
        (error) => error instanceof ZhilianPlatformError && error.code === scenario.code,
        scenario.label,
      )
      assert.equal(fixture.state.barriers, 0, `${scenario.label}: 不得进入 attempting`)
      assert.deepEqual(fixture.state.phases, [], `${scenario.label}: 不得调用 greeting evaluator`)
      assert.equal(fixture.state.finalClicks, 0, `${scenario.label}: 不得调用候选人可见发送`)
    } finally {
      fixture.restore()
    }
  }
})

test('chat.readGreetingOutcome 的 false 与稳定正证都只读且绝不补招呼动作', async () => {
  for (const scenario of [
    { label: '正证不足', proofMode: 'negative', confirmed: false, proofCalls: 1 },
    { label: '稳定正证', proofMode: 'positive', confirmed: true, proofCalls: 2 },
  ]) {
    const fixture = installM4GreetingOrchestrationFixture({ proofMode: scenario.proofMode })
    try {
      const result = await readZhilianGreetingOutcome(
        {
          platformUserRef: fixture.refs.user,
          positionRef: fixture.refs.job,
          contentHash: fixture.contentHash,
        },
        fixture.context(),
        fixture.fingerprint,
      )
      assert.equal(result.confirmed, scenario.confirmed, scenario.label)
      assert.equal(fixture.state.proofCalls, scenario.proofCalls, scenario.label)
      assert.equal(fixture.state.finalClicks, 0, `${scenario.label}: 验证读不得触发招呼动作`)
      assert.deepEqual(fixture.state.phases, [], `${scenario.label}: 验证读不得调用动作 evaluator`)
    } finally {
      fixture.restore()
    }
  }
})

test('M4 招呼 attempting 前后与动作后故障均由原 witness 内核收束且不补动作', async () => {
  const scenarios = [
    {
      key: 'pre-attempting',
      label: 'attempting 前死亡',
      options: { preflightResult: 'throw' },
      expectedCode: ErrorCode.InternalHand,
      expectedClicks: 0,
      witnessed: false,
    },
    {
      key: 'post-attempting-pre-action',
      label: 'attempting 后动作前死亡',
      options: { commitResult: 'throw' },
      expectedCode: ErrorCode.InternalHand,
      expectedClicks: 0,
      witnessed: true,
    },
    {
      key: 'post-action-pre-observer',
      label: '动作后 observer 前死亡',
      options: { proofMode: 'throw' },
      expectedCode: ErrorCode.PostconditionUnconfirmed,
      expectedClicks: 1,
      witnessed: true,
    },
    {
      key: 'negative-proof',
      label: '动作后正证 false',
      options: { proofMode: 'negative' },
      expectedCode: ErrorCode.PostconditionUnconfirmed,
      expectedClicks: 1,
      witnessed: true,
    },
  ]

  for (const scenario of scenarios) {
    const fixture = installM4GreetingOrchestrationFixture(scenario.options)
    const ref = `sx-m4-${scenario.key}`
    const idemKey = `ik1:zhilian:account-fixture:chat.sendGreeting:profile-fixture:${ref}`
    const storage = memoryWitnessStorage()
    const witness = new WitnessStore(storage, Date.now, () => `witness-${ref}`)
    await witness.initialize()
    const out = recorder()
    const commitKeys = []
    let resultID = 0
    register({
      name: Primitive.ChatSendGreeting,
      class: 'effectful',
      async handler(args, context) {
        try {
          const data = await sendZhilianGreeting(
            args,
            context.guards,
            context,
            context.commandContext?.expectedPrincipalFingerprint,
          )
          return {
            status: 'ok', data,
            evidence: [{ type: 'outboundGreetingObserved' }],
          }
        } catch (error) {
          if (!(error instanceof ZhilianPlatformError)) throw error
          return {
            status: 'failed',
            error: {
              code: error.code,
              message: error.message,
              retryable: error.retryable,
              sideEffect: error.sideEffect,
              ...(error.reason ? { data: { reason: error.reason } } : {}),
            },
          }
        }
      },
    })
    const durable = async (session, body, commitIdemKey) => {
      commitKeys.push(commitIdemKey)
      const envelope = {
        proto: 1,
        kind: 'result',
        msgId: `m4-matrix-result-${++resultID}`,
        session,
        ts: Date.now(),
        attempt: 1,
        body,
      }
      if (commitIdemKey) await witness.commitAndEnqueue(commitIdemKey, envelope)
      else await witness.enqueueResult(envelope)
      out.send(Kind.Result, session, body)
      return 'sent'
    }
    try {
      const dispatcher = new Dispatcher(out.send, undefined, witness, durable)
      await dispatcher.handleCmd(ref, 's', 's', sendGreetingCommand(ref, idemKey))
      await eventually(() => results(out.frames, ref).length === 1, `${scenario.label}: 未返回唯一终局`)
      const terminal = results(out.frames, ref)[0].body
      assert.equal(terminal.status, ResultStatus.Failed, scenario.label)
      assert.equal(terminal.error.code, scenario.expectedCode, scenario.label)
      assert.equal(fixture.state.finalClicks, scenario.expectedClicks, scenario.label)
      assert.ok(fixture.state.finalClicks <= 1, `${scenario.label}: 候选人可见发送不得超过一次`)
      assert.deepEqual(
        fixture.state.phases,
        scenario.key === 'pre-attempting'
          ? ['prepare', 'preflight']
          : ['prepare', 'preflight', 'commit'],
        `${scenario.label}: 故障注入点未命中预期 evaluator phase`,
      )
      assert.deepEqual(commitKeys, [scenario.witnessed ? idemKey : undefined], scenario.label)
      const journal = storage.state[`journal:${idemKey}`]
      if (scenario.witnessed) {
        assert.equal(journal.state, 'committed', `${scenario.label}: attempting 必须终局化`)
        assert.deepEqual(journal.result, terminal, `${scenario.label}: journal 必须保存同一终局`)
      } else {
        assert.equal(journal, undefined, `${scenario.label}: barrier 前不得创建 journal`)
      }
      if (scenario.options.proofMode) {
        assert.ok(fixture.state.proofCalls > 0, `${scenario.label}: 必须真的进入验证读`)
        assert.equal(fixture.state.finalClicks, 1,
          `${scenario.label}: 阴性验证不得补第二次候选人可见动作`)
      }
    } finally {
      fixture.restore()
    }
  }
})

test('sendZhilianGreeting 的 prepare/preflight 在证词前，commit 在唯一 barrier 后且阴性不补动作', async () => {
  const original = { chrome: globalThis.chrome, setTimeout: globalThis.setTimeout }
  const fingerprint = 'a'.repeat(64)
  const refs = {
    user: 'fixture-user-greeting-orchestration', job: 'fixture-job-greeting-orchestration',
    conversation: 'fixture-conversation-greeting-orchestration',
  }
  const text = '你好'
  const contentHash = createHash('sha256').update(text).digest('hex')
  const targetTab = {
    id: 601, active: true, status: 'complete',
    url: 'https://rd6.zhaopin.com/app/recommend?resumeNumber=private&jobNumber=private',
  }
  const phases = []
  const functions = []
  let barriers = 0
  let proofCalls = 0
  globalThis.setTimeout = (callback) => { queueMicrotask(callback); return 1 }
  globalThis.chrome = {
    tabs: {
      async query() { return [{ ...targetTab }] },
      async get(id) { assert.equal(id, targetTab.id); return { ...targetTab } },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ target, func, args }) {
        assert.equal(target.tabId, targetTab.id)
        if (func.name === 'mainProbeZhilian') return [{ result: {
          pageKind: 'recommend', loginState: 'in', principalFingerprint: fingerprint,
          imListVisible: false,
        } }]
        if (func.name === 'mainReadCurrentCandidate') return [{ result: {
          status: 'ready',
          data: {
            platformUserRef: refs.user, displayName: '合成候选人',
            positionRef: refs.job, positionTitle: '合成职位', contactState: 'unestablished',
          },
        } }]
        if (func.name === 'mainSendGreetingOnce') {
          functions.push(func)
          const phase = args.at(-1)
          phases.push(phase)
          if (phase === 'prepare') {
            assert.equal(barriers, 0)
            assert.equal(args[5], '')
            return [{ result: { status: 'prepared' } }]
          }
          if (phase === 'preflight') {
            assert.equal(barriers, 0)
            assert.equal(args[5], text)
            return [{ result: { status: 'ready' } }]
          }
          assert.equal(phase, 'commit')
          assert.equal(barriers, 1)
          assert.equal(args[5], text)
          return [{ result: { status: 'clicked' } }]
        }
        if (func.name === 'mainReadGreetingProof') {
          proofCalls += 1
          assert.equal(barriers, 1)
          return [{ result: {
            confirmed: true,
            conversationRef: refs.conversation,
            contentHash,
            proofToken: 'b'.repeat(64),
          } }]
        }
        throw new Error(`unexpected MAIN ${func.name}`)
      },
    },
  }
  const context = {
    signal: new AbortController().signal,
    cmdMsgId: 'send-greeting-orchestration',
    deadlineMs: Date.now() + 60_000,
    irreversibleNotAfterMs: Date.now() + 60_000,
    commandContext: undefined,
    guards: undefined,
    checkpoint() {},
    async beforeSideEffect() { barriers += 1 },
    async progress() {},
  }
  try {
    const result = await sendZhilianGreeting(
      { platformUserRef: refs.user, positionRef: refs.job, text },
      { expectUnestablished: true },
      context,
      fingerprint,
    )
    assert.equal(result.conversationRef, refs.conversation)
    assert.equal(result.contentHash, contentHash)
    assert.deepEqual(phases, ['prepare', 'preflight', 'commit'])
    assert.equal(new Set(functions).size, 1,
      'prepare/preflight/commit 必须注入字面同一份 evaluator 函数')
    assert.equal(barriers, 1)
    assert.equal(proofCalls, 2, '成功必须是两次稳定正采样，验证读不得触发第二个动作')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('智联 MAIN 线程解析：方向不猜、未知类型归 system、数字卡片状态保持 unknown', async () => {
  const rows = [
    { idServer: 'm-text-out', status: 'success', time: 1, type: 'text', from: 'staff', text: '  招聘方  消息 ' },
    { idServer: 'm-text-in', time: 2, type: 'text', from: 'candidate', text: '候选人消息' },
    {
      idServer: 'm-card', status: 'success', time: 3, type: 105, from: 'staff',
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
  const unresolvedDirection = await zhilianTestHooks.mainReadThreadPage('conversation-1', 8, null)
  assert.match(unresolvedDirection.__recruitHelperMainError, /message_direction_unresolved/u)

  window.imEngine.getHistoryMsgs = async () => [
    { sendMessageId: 'client-only-out', status: 'success', time: 2, type: 'text', from: 'staff', text: '乐观同文' },
  ]
  const missingServerIdentity = await zhilianTestHooks.mainReadThreadPage('conversation-1', 8, null)
  assert.match(missingServerIdentity.__recruitHelperMainError, /message_identity_missing/u,
    'ambiguity verifier 复用的 readThread 不得结构化 client-only 乐观行')

  window.imEngine.getHistoryMsgs = async () => [
    { idServer: 'server-pending-out', status: 'pending', time: 3, type: 'text', from: 'staff', text: '未确认同文' },
  ]
  const unconfirmedOutbound = await zhilianTestHooks.mainReadThreadPage('conversation-1', 8, null)
  assert.match(unconfirmedOutbound.__recruitHelperMainError, /outbound_delivery_unconfirmed/u,
    '带 idServer 但非 success 的 out 行也不得成为 verifier 正证据')
})

test('智联会话选择拆成只滚动 finder 与同步 click-once', async () => {
  const original = {
    window: globalThis.window,
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
  }
  const targetRef = 'conversation-target-exact'
  let clickCalls = 0
  const scrollElement = {
    scrollTop: 0,
    scrollHeight: 1_000,
    clientHeight: 400,
    parentElement: null,
    querySelectorAll() { return [] },
    dispatchEvent() {},
  }
  const clickTarget = {
    isConnected: true,
    getClientRects() { return [{}] },
    click() {
      clickCalls += 1
      globalThis.location.href = `https://rd6.zhaopin.com/app/im?sessionId=${targetRef}`
    },
  }
  const row = (sessionId, clickNode) => {
    const result = {
    __vue__: { _props: { source: { sessionId } } },
    isConnected: true,
    parentElement: null,
    getClientRects() { return [{}] },
    querySelector(selector) {
      if (selector === '.im-session-item__box, .im-session-item') return clickNode
      if (selector === '.im-session-item') return clickNode
      return null
    },
    querySelectorAll() { return [] },
    contains(node) { return node === result || node === clickNode },
    scrollIntoView() {},
    }
    return result
  }
  const firstClickNode = { isConnected: true, getClientRects() { return [{}] } }
  const firstRow = row('conversation-first-window', firstClickNode)
  const targetRow = row(targetRef, clickTarget)
  globalThis.location = { href: 'https://rd6.zhaopin.com/app/im' }
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  const staffId = 'staff-select-fixture'
  const orgId = 'org-select-fixture'
  const loginPoint = 'login-select-fixture'
  globalThis.window = {
    $session: {
      isLoggedIn: true,
      staff: { staffId, defaultLoginPoint: loginPoint },
      org: { orgId },
    },
  }
  globalThis.document = {
    scripts: [],
    querySelector(selector) {
      return selector === '.im-session-list .im-session-list__virtual' ? scrollElement : null
    },
    querySelectorAll(selector) {
      if (selector.startsWith('textarea.')) return []
      return scrollElement.scrollTop < 300 ? [firstRow] : [targetRow]
    },
  }
  try {
    const found = await zhilianTestHooks.mainFindConversation(targetRef)
    assert.deepEqual(found, { status: 'found' })
    assert.equal(clickCalls, 0, 'finder 无论耗时多久都不得 click')
    assert.equal(globalThis.location.href, 'https://rd6.zhaopin.com/app/im')

    const principal = ['zhilian-principal-v2', staffId, orgId, loginPoint]
      .map((piece) => `${new TextEncoder().encode(piece).length}:${piece}`).join('|')
    const fingerprint = createHash('sha256').update(principal).digest('hex')
    const clicked = zhilianTestHooks.mainClickConversationOnce(targetRef, '', fingerprint, Date.now() + 1_000)
    assert.deepEqual(clicked, { status: 'clicked' })
    assert.equal(clickCalls, 1)
    assert.match(globalThis.location.href, /conversation-target-exact/u)

    globalThis.location.href = 'https://rd6.zhaopin.com/app/im'
    const expired = zhilianTestHooks.mainClickConversationOnce(targetRef, '', fingerprint, Date.now() - 1)
    assert.deepEqual(expired, { status: 'failed', reason: 'action_window_elapsed' })
    assert.equal(clickCalls, 1, '排队到期限外的同步 task 必须零 click')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('智联会话 click-once 对冲突绑定、人工草稿与账号变化一律零 click', () => {
  const original = {
    window: globalThis.window,
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
  }
  const targetRef = 'conversation-target-guarded'
  let clicks = 0
  const child = { __vue__: { _props: { source: { sessionId: 'conversation-conflict' } } } }
  const clickTarget = { isConnected: true, getClientRects() { return [{}] }, click() { clicks += 1 } }
  const row = {
    __vue__: { _props: { source: { sessionId: targetRef } } },
    isConnected: true,
    getClientRects() { return [{}] },
    querySelector(selector) {
      if (selector === '.im-session-item__box, .im-session-item' || selector === '.im-session-item') return clickTarget
      return null
    },
    querySelectorAll() { return [child] },
    contains(node) { return node === row || node === child || node === clickTarget },
  }
  const composer = {
    value: '人工草稿',
    getClientRects() { return [{}] },
    closest(selector) { return selector === '.im-sender__input-wrapper' ? {} : null },
  }
  const staffId = 'staff-click-guard'
  const orgId = 'org-click-guard'
  const loginPoint = 'login-click-guard'
  globalThis.window = {
    $session: {
      isLoggedIn: true,
      staff: { staffId, defaultLoginPoint: loginPoint },
      org: { orgId },
    },
  }
  const principal = ['zhilian-principal-v2', staffId, orgId, loginPoint]
    .map((piece) => `${new TextEncoder().encode(piece).length}:${piece}`).join('|')
  const fingerprint = createHash('sha256').update(principal).digest('hex')
  globalThis.location = { href: 'https://rd6.zhaopin.com/app/im' }
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.document = {
    scripts: [],
    querySelectorAll(selector) {
      if (selector.startsWith('textarea.')) return [composer]
      return [row]
    },
  }
  try {
    const draft = zhilianTestHooks.mainClickConversationOnce(targetRef, '', fingerprint, Date.now() + 1_000)
    assert.deepEqual(draft, { status: 'failed', reason: 'composer_nonempty' })
    assert.equal(clicks, 0)

    composer.value = ''
    const conflict = zhilianTestHooks.mainClickConversationOnce(targetRef, '', fingerprint, Date.now() + 1_000)
    assert.deepEqual(conflict, { status: 'failed', reason: 'list_binding_unresolved' })
    assert.equal(clicks, 0, '同一行出现两个 sessionId 必须拒绝')

    child.__vue__._props.source.sessionId = targetRef
    const identity = zhilianTestHooks.mainClickConversationOnce(targetRef, '', 'f'.repeat(64), Date.now() + 1_000)
    assert.deepEqual(identity, { status: 'failed', reason: 'identity_changed' })
    assert.equal(clicks, 0)

    globalThis.location.href = 'https://rd6.zhaopin.com/app/im?sessionId=manual-switch'
    const routeRace = zhilianTestHooks.mainClickConversationOnce(targetRef, '', fingerprint, Date.now() + 1_000)
    assert.deepEqual(routeRace, { status: 'failed', reason: 'route_changed' })
    assert.equal(clicks, 0, 'finder 后人工切换会话必须在同步 click task 内再次拦截')
  } finally {
    Object.assign(globalThis, original)
  }
})

// M3_SEND_GUARD_RESTRUCTURE_TESTS
const m3Hash = (value) => createHash('sha256').update(value).digest('hex')

function installM3SendFixture() {
  const original = {
    window: globalThis.window,
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
    HTMLElement: globalThis.HTMLElement,
    HTMLButtonElement: globalThis.HTMLButtonElement,
    HTMLTextAreaElement: globalThis.HTMLTextAreaElement,
    InputEvent: globalThis.InputEvent,
    Event: globalThis.Event,
    chrome: globalThis.chrome,
  }
  const conversationRef = 'conversation-m3-public-boundary'
  const peerRef = 'candidate-m3-public-boundary'
  const staffId = 'staff-m3-public-boundary'
  const orgId = 'org-m3-public-boundary'
  const loginPoint = 'login-m3-public-boundary'
  const text = '你好'
  const tailText = '候选人尾消息'
  const rows = [{
    idServer: 'server-m3-baseline-1',
    time: 1,
    status: 'success',
    type: 'text',
    from: peerRef,
    text: tailText,
  }]
  const state = {
    rows,
    details: [],
    composers: [],
    buttons: [],
    intrinsicClicks: 0,
    instanceClicks: 0,
    valueAtClick: null,
    inputEvents: [],
    rewriteInsertedText: false,
    ariaDisabled: null,
    throwOnReadAfterClick: false,
  }

  class FixtureEvent {
    constructor(type, options = {}) { this.type = type; Object.assign(this, options) }
  }
  let composer
  class FixtureHTMLElement {
    constructor() { this.isConnected = true }
    getClientRects() { return [{}] }
    click() {
      state.valueAtClick = composer.value
      state.intrinsicClicks += 1
    }
  }
  class FixtureTextArea {
    constructor() {
      this._value = ''
      this.isConnected = true
      this.parentElement = null
    }
    get value() {
      if (state.throwOnReadAfterClick && state.intrinsicClicks > 0) {
        throw new Error('click 后不得再读取 composer')
      }
      return this._value
    }
    set value(value) { this._value = String(value) }
    getClientRects() { return [{}] }
    closest(selector) {
      if (selector === '.im-sender__input-wrapper') return wrapper
      if (selector === '.im-session-detail') return detail
      return null
    }
    dispatchEvent(event) {
      state.inputEvents.push({ type: event.type, inputType: event.inputType ?? null, data: event.data ?? null })
      if (state.rewriteInsertedText && event.type === 'input' && event.inputType === 'insertText') {
        this._value = '页面改写后的正文'
      }
      return true
    }
  }
  globalThis.HTMLElement = FixtureHTMLElement
  globalThis.HTMLButtonElement = FixtureHTMLElement
  globalThis.HTMLTextAreaElement = FixtureTextArea
  globalThis.InputEvent = FixtureEvent
  globalThis.Event = FixtureEvent

  const detail = new FixtureHTMLElement()
  const wrapper = new FixtureHTMLElement()
  const timeline = new FixtureHTMLElement()
  composer = new FixtureTextArea()
  const button = new FixtureHTMLElement()
  button.textContent = '发送'
  button.form = null
  button.type = 'submit'
  button.disabled = false
  button.getAttribute = (name) => name === 'aria-disabled' ? state.ariaDisabled : null
  button.click = () => { state.instanceClicks += 1 }

  detail.contains = (node) => [detail, wrapper, timeline, composer, button].includes(node)
  detail.querySelectorAll = (selector) => {
    if (selector.includes('textarea')) return state.composers
    if (selector.includes('button')) return state.buttons
    return []
  }
  wrapper.parentElement = detail
  wrapper.contains = (node) => [wrapper, composer, button].includes(node)
  wrapper.closest = (selector) => selector === '.im-session-detail' ? detail : null
  timeline.parentElement = detail
  timeline.closest = (selector) => selector === '.im-session-detail' ? detail : null
  composer.parentElement = wrapper
  button.parentElement = wrapper
  button.closest = (selector) => {
    if (selector === '.im-sender__input-wrapper') return wrapper
    if (selector === '.im-session-detail') return detail
    return null
  }
  state.details = [detail]
  state.composers = [composer]
  state.buttons = [button]

  const root = {
    $store: { state: { im: { timelineMap: { [conversationRef]: { timeline: rows } } } } },
    $children: [],
  }
  root.$root = root
  const session = {
    sessionId: conversationRef,
    peerPartnerId: peerRef,
    sortTime: 1,
    modifiedTime: 2,
    lastSentence: tailText,
  }
  const runtimeSession = {
    isLoggedIn: true,
    staff: { staffId, defaultLoginPoint: loginPoint },
    org: { orgId },
  }
  const principal = ['zhilian-principal-v2', staffId, orgId, loginPoint]
    .map((piece) => `${new TextEncoder().encode(piece).length}:${piece}`).join('|')
  const expectedTail = [{ direction: 'in', contentHash: m3Hash(tailText) }]

  globalThis.location = { href: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}` }
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.document = {
    scripts: [],
    querySelectorAll(selector) {
      if (state.throwOnReadAfterClick && state.intrinsicClicks > 0) {
        throw new Error('click 后不得再查询页面')
      }
      if (selector === '.im-session-detail') return state.details
      if (selector === 'textarea.km-input__original.is-normal.is-textarea.is-autoresize') {
        return state.composers
      }
      if (selector === '.im-sender__input-wrapper button') return state.buttons
      if (selector === '.im-timeline__wrapper') return [timeline]
      return []
    },
  }
  globalThis.window = {
    $nuxt: root,
    $session: runtimeSession,
    imEngine: { sessions: [session] },
  }

  const capture = (tail = expectedTail) =>
    zhilianTestHooks.mainCaptureSendBaseline(conversationRef, tail)
  const invoke = (baseline, phase = 'preflight', overrides = {}) =>
    zhilianTestHooks.mainSendMessageOnce(
      overrides.conversationRef ?? conversationRef,
      overrides.text ?? text,
      overrides.expectedTail ?? expectedTail,
      overrides.fingerprint ?? m3Hash(principal),
      overrides.deadline ?? Date.now() + 10_000,
      overrides.baselineKeys ?? baseline.serverSourceKeys,
      overrides.targetToken ?? baseline.targetBindingToken,
      phase,
    )
  const appendOutbound = (body = text, id = `server-m3-out-${rows.length + 1}`) => {
    rows.push({
      idServer: id,
      time: rows.length + 1,
      status: 'success',
      type: 'text',
      from: staffId,
      text: body,
    })
  }
  const restore = () => { Object.assign(globalThis, original) }
  return {
    appendOutbound,
    button,
    capture,
    composer,
    conversationRef,
    detail,
    expectedTail,
    invoke,
    peerRef,
    principal,
    restore,
    root,
    rows,
    session,
    state,
    text,
    timeline,
    wrapper,
  }
}

test('M3 baseline 单次取样且只冻结 server source keys 与目标绑定 token', async () => {
  const fixture = installM3SendFixture()
  const originalSetTimeout = globalThis.setTimeout
  let timerCalls = 0
  globalThis.setTimeout = () => {
    timerCalls += 1
    throw new Error('baseline 不得建立固定等待窗')
  }
  try {
    const baseline = await fixture.capture()
    assert.deepEqual(baseline, {
      status: 'ready',
      stage: 'ready',
      serverSourceKeys: [m3Hash('source-v1|server-m3-baseline-1')],
      targetBindingToken: m3Hash(JSON.stringify([fixture.conversationRef, fixture.peerRef])),
    })
    assert.equal(timerCalls, 0)
    assert.deepEqual(Object.keys(baseline).sort(), [
      'serverSourceKeys', 'stage', 'status', 'targetBindingToken',
    ])

    const wrongTail = await fixture.capture([{ direction: 'in', contentHash: 'f'.repeat(64) }])
    assert.deepEqual(wrongTail, { status: 'failed', stage: 'guard_snapshot_uncovered' })

    globalThis.location.href = 'https://rd6.zhaopin.com/app/im?sessionId=other'
    assert.deepEqual(await fixture.capture(), { status: 'failed', stage: 'route_changed' })
    globalThis.location.href = `https://rd6.zhaopin.com/app/im?sessionId=${fixture.conversationRef}`
    globalThis.window.imEngine.sessions = []
    assert.deepEqual(await fixture.capture(), { status: 'failed', stage: 'session_unavailable' })
  } finally {
    globalThis.setTimeout = originalSetTimeout
    fixture.restore()
  }
})

test('M3 evaluator 只守世界状态、目标 token 与公开 DOM 语义', async () => {
  const fixture = installM3SendFixture()
  try {
    const baseline = await fixture.capture()
    assert.equal(baseline.status, 'ready')

    // 旧实现依赖的 Vue owner/model/VNode/listener 任意冲突都不得再影响授权。
    fixture.root.$children = null
    fixture.detail.__vue__ = { $root: { privateRoot: true }, currentSession: { sessionId: 'wrong' } }
    fixture.button.__vue__ = { _events: { click: [() => {}] }, $vnode: null }
    fixture.button._events = { click: [() => {}, () => {}] }
    fixture.button.$vnode = { componentOptions: { listeners: { click: () => {} } } }
    fixture.session.sortTime = 99
    fixture.session.modifiedTime = 100
    fixture.session.lastSentence = '展示字段变化'
    fixture.button.disabled = true
    fixture.state.ariaDisabled = 'true'
    assert.deepEqual(fixture.invoke(baseline), { status: 'ready' },
      '私有对象变化与 disabled/aria-disabled 不得阻断 evaluator')

    fixture.button.form = {}
    for (const type of ['', 'submit', 'reset']) {
      fixture.button.type = type
      const rejected = fixture.invoke(baseline)
      assert.equal(rejected.status, 'failed', `关联 form 的 ${type || '缺省'} type 必须拒绝`)
      assert.equal(fixture.state.intrinsicClicks, 0)
    }
    fixture.button.type = 'button'
    assert.deepEqual(fixture.invoke(baseline), { status: 'ready' })
    fixture.button.form = null

    const cases = [
      ['route', () => { globalThis.location.href = 'https://rd6.zhaopin.com/app/im?sessionId=other' }, {}, 'route_changed'],
      ['identity', () => {}, { fingerprint: 'f'.repeat(64) }, 'identity_changed'],
      ['source keys', () => {}, { baselineKeys: ['e'.repeat(64)] }, 'baseline_changed'],
      ['expected tail', () => {}, {
        expectedTail: [{ direction: 'in', contentHash: 'd'.repeat(64) }],
      }, 'baseline_changed'],
      ['target token', () => {}, { targetToken: 'c'.repeat(64) }, 'target_changed'],
      ['deadline', () => {}, { deadline: Date.now() - 1 }, 'action_window_elapsed'],
    ]
    for (const [name, mutate, overrides, reason] of cases) {
      const currentHref = globalThis.location.href
      mutate()
      const result = fixture.invoke(baseline, 'preflight', overrides)
      assert.deepEqual(result, { status: 'failed', reason }, `${name} 必须闭锁`)
      globalThis.location.href = currentHref
    }

    const extraDetail = { getClientRects() { return [{}] } }
    fixture.state.details = [fixture.detail, extraDetail]
    assert.deepEqual(fixture.invoke(baseline), { status: 'failed', reason: 'composer_missing' })
    fixture.state.details = [fixture.detail]

    const extraComposer = Object.create(Object.getPrototypeOf(fixture.composer))
    extraComposer._value = ''
    extraComposer.isConnected = true
    extraComposer.closest = fixture.composer.closest.bind(fixture.composer)
    extraComposer.getClientRects = () => [{}]
    fixture.state.composers = [fixture.composer, extraComposer]
    assert.deepEqual(fixture.invoke(baseline), { status: 'failed', reason: 'composer_missing' })
    fixture.state.composers = [fixture.composer]

    const extraButton = Object.assign(Object.create(Object.getPrototypeOf(fixture.button)), {
      textContent: '发送', form: null, type: 'button', isConnected: true,
      getClientRects() { return [{}] },
      closest: fixture.button.closest,
    })
    fixture.state.buttons = [fixture.button, extraButton]
    assert.deepEqual(fixture.invoke(baseline), { status: 'failed', reason: 'composer_missing' })
    fixture.state.buttons = [fixture.button]

    const originalButtonClosest = fixture.button.closest
    fixture.button.closest = (selector) => selector === '.im-session-detail' ? fixture.detail : null
    assert.deepEqual(fixture.invoke(baseline), { status: 'failed', reason: 'composer_missing' })
    fixture.button.closest = originalButtonClosest

    fixture.composer.value = '人工草稿'
    assert.deepEqual(fixture.invoke(baseline), { status: 'failed', reason: 'composer_nonempty' })
    fixture.composer.value = ''
    assert.equal(fixture.state.intrinsicClicks, 0)
  } finally {
    fixture.restore()
  }
})

test('M3 preflight 与 commit 复用 evaluator，最终绿色后只走一次 intrinsic click', async () => {
  const fixture = installM3SendFixture()
  try {
    const baseline = await fixture.capture()
    assert.deepEqual(fixture.invoke(baseline, 'preflight'), { status: 'ready' })
    assert.equal(fixture.composer.value, '')
    assert.equal(fixture.state.intrinsicClicks, 0)

    // 两轮之间私有实现换壳不构成世界变化；公开边界保持不变即可继续。
    fixture.detail.__vue__ = { $root: { replaced: true } }
    fixture.button._vnode = { data: { on: { click: [() => {}, () => {}] } } }
    fixture.button._events = { click: null }
    fixture.button.$vnode = null
    fixture.button.disabled = true
    fixture.state.ariaDisabled = 'true'
    fixture.button.form = {}
    fixture.button.type = 'button'
    fixture.state.throwOnReadAfterClick = true

    assert.deepEqual(fixture.invoke(baseline, 'commit'), { status: 'clicked' })
    assert.equal(fixture.state.intrinsicClicks, 1)
    assert.equal(fixture.state.instanceClicks, 0, '不得调用页面替换过的 instance click')
    assert.equal(fixture.state.valueAtClick, fixture.text)
    assert.deepEqual(fixture.state.inputEvents.map(({ type }) => type), ['input', 'change'])
  } finally {
    fixture.restore()
  }
})

test('M3 写后正文被页面改写时清空草稿且零 click', async () => {
  const fixture = installM3SendFixture()
  try {
    const baseline = await fixture.capture()
    fixture.state.rewriteInsertedText = true
    const result = fixture.invoke(baseline, 'commit')
    assert.deepEqual(result, { status: 'failed', reason: 'input_rejected' })
    assert.equal(fixture.composer.value, '')
    assert.equal(fixture.state.intrinsicClicks, 0)
    assert.deepEqual(fixture.state.inputEvents.map(({ type }) => type), [
      'input', 'change', 'input', 'change',
    ])
  } finally {
    fixture.restore()
  }
})

test('M3 post 只接受 baseline 后严格追加一条 server success，同文阴性不补 click', async () => {
  const fixture = installM3SendFixture()
  try {
    const baseline = await fixture.capture()
    const observe = () => zhilianTestHooks.mainObserveStableOutbound(
      fixture.conversationRef,
      m3Hash(fixture.text),
      baseline.serverSourceKeys,
      baseline.targetBindingToken,
    )
    assert.deepEqual(await observe(), { selected: true, matchingNewServerMessages: 0 })
    fixture.appendOutbound()
    assert.deepEqual(await observe(), { selected: true, matchingNewServerMessages: 1 })
    fixture.appendOutbound(fixture.text, 'server-m3-out-extra')
    assert.deepEqual(await observe(), { selected: true, matchingNewServerMessages: 0 },
      '严格 +2 即使同文也必须保持阴性')
    assert.equal(fixture.state.intrinsicClicks, 0, 'observer 永远不 click')

    fixture.rows.splice(1)
    fixture.session.peerPartnerId = 'candidate-rebound'
    assert.deepEqual(await observe(), { selected: true, matchingNewServerMessages: 0 },
      'target token 失配不得认领后置条件')
    fixture.session.peerPartnerId = fixture.peerRef
    globalThis.location.href = 'https://rd6.zhaopin.com/app/im?sessionId=other'
    assert.deepEqual(await observe(), { selected: false, matchingNewServerMessages: 0 })
  } finally {
    fixture.restore()
  }
})

test('M3 post 的 64 行滑窗严格左移一格，新增行四类语义不符均阴性', async () => {
  const fixture = installM3SendFixture()
  try {
    fixture.rows.splice(0, fixture.rows.length, ...Array.from({ length: 64 }, (_, index) => ({
      idServer: `server-m3-window-${index}`,
      time: index + 1,
      status: 'success',
      type: 'text',
      from: fixture.peerRef,
      text: `窗口消息${index}`,
    })))
    const baselineRows = structuredClone(fixture.rows)
    const baseline = await fixture.capture([])
    assert.equal(baseline.status, 'ready')
    assert.equal(baseline.serverSourceKeys.length, 64)
    const observe = () => zhilianTestHooks.mainObserveStableOutbound(
      fixture.conversationRef,
      m3Hash(fixture.text),
      baseline.serverSourceKeys,
      baseline.targetBindingToken,
    )
    const reset = () => fixture.rows.splice(0, fixture.rows.length, ...structuredClone(baselineRows))
    const append = (overrides) => fixture.rows.push({
      idServer: `server-m3-window-new-${overrides.id}`,
      time: 65,
      status: 'success',
      type: 'text',
      from: globalThis.window.$session.staff.staffId,
      text: fixture.text,
      ...overrides,
    })

    append({ id: 'ok' })
    assert.deepEqual(await observe(), { selected: true, matchingNewServerMessages: 1 },
      'baseline=64 时只允许窗口左移一格并追加唯一成功文本')

    for (const [name, overrides] of [
      ['status failed', { id: 'failed', status: 'failed' }],
      ['direction in', { id: 'inbound', from: fixture.peerRef }],
      ['type 非 text', { id: 'system', type: 999 }],
      ['正文 hash 错', { id: 'wrong-hash', text: '不是本次正文' }],
    ]) {
      reset()
      append(overrides)
      assert.deepEqual(await observe(), { selected: true, matchingNewServerMessages: 0 },
        `${name} 不得形成发送成功证词`)
    }
    assert.equal(fixture.state.intrinsicClicks, 0)
  } finally {
    fixture.restore()
  }
})

test('debug.inspectSendSurface 只读单次公开 surface+timeline 且不发射私有阶段', async () => {
  const fixture = installM3SendFixture()
  const calls = []
  globalThis.chrome = {
    tabs: {
      async query() {
        return [{
          id: 71,
          active: true,
          status: 'complete',
          url: globalThis.location.href,
        }]
      },
    },
    scripting: {
      async executeScript({ func, args }) {
        calls.push(func.name)
        return [{ result: await func(...args) }]
      },
    },
  }
  try {
    fixture.detail.__vue__ = { $root: null }
    fixture.button._events = { click: [() => {}, () => {}] }
    fixture.button.$vnode = { componentOptions: { listeners: { click: null } } }
    globalThis.window.imEngine = null
    assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), { ready: true, stage: 'ready' })
    assert.deepEqual(calls, ['mainInspectSendSurface', 'mainInspectSendTimeline'],
      'debug 只允许单次公开 surface snapshot 与单次纯 timeline 投影')

    calls.length = 0
    fixture.root.$store.state.im.timelineMap[fixture.conversationRef].timeline = null
    assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), {
      ready: false,
      stage: 'thread_unavailable',
    })
    assert.deepEqual(calls, ['mainInspectSendSurface', 'mainInspectSendTimeline'])
    fixture.root.$store.state.im.timelineMap[fixture.conversationRef].timeline = fixture.rows

    calls.length = 0
    fixture.button.form = {}
    fixture.button.type = 'submit'
    const unsafe = await inspectZhilianSendSurfaceDiagnostic()
    assert.deepEqual(unsafe, { ready: false, stage: 'button_form_unsafe' })
    assert.deepEqual(calls, ['mainInspectSendSurface'])
    assert.equal(/vue|vnode|listener|component|owner|engine/iu.test(JSON.stringify(unsafe)), false)
  } finally {
    fixture.restore()
  }
})

test('sendZhilianMessage 后置条件阴性只读轮询，绝不重试 click', async () => {
  const originalSetTimeout = globalThis.setTimeout
  const fingerprint = 'e'.repeat(64)
  const conversationRef = 'conversation-send-orchestration'
  const tailHash = 'a'.repeat(64)
  const expectedTail = [{ direction: 'in', contentHash: tailHash }]
  let mainSendCalls = 0
  let preflightCalls = 0
  let commitCalls = 0
  let evaluatorFunction = null
  let baselineCalls = 0
  let mainReadThreadCalls = 0
  let selectCalls = 0
  let updateCalls = 0
  const targetTabId = 91
  let currentURL = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`
  let observePositive = true
  let sendBaselineResult = {
    status: 'ready', stage: 'ready',
    serverSourceKeys: ['d'.repeat(64)],
    targetBindingToken: 'b'.repeat(64),
  }
  globalThis.setTimeout = (callback) => {
    queueMicrotask(callback)
    return 1
  }
  globalThis.chrome = {
    tabs: {
      async query() {
        return [
          {
            id: 90,
            url: 'https://rd6.zhaopin.com/app/im?sessionId=other-conversation',
            status: 'complete', active: true, lastAccessed: Date.now() + 1_000,
          },
          {
            id: targetTabId,
            url: currentURL,
            status: 'complete', active: false, lastAccessed: Date.now(),
          },
        ]
      },
      async get(id) {
        assert.equal(id, targetTabId, '发送必须精确选中人工打开目标 conversationRef 的 tab')
        return {
          id: targetTabId,
          url: currentURL,
          status: 'complete', active: false,
        }
      },
      async update() { updateCalls += 1; throw new Error('不得直接拼 URL 导航') },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ target, func, args }) {
        assert.equal(target.tabId, targetTabId,
          '所有 baseline/evaluator/post 注入必须落到精确匹配 conversationRef 的 tab')
        if (func.name === 'mainProbeZhilian') return [{ result: {
          pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
        } }]
        if (func.name === 'mainFindConversation') {
          selectCalls += 1
          return [{ result: { status: 'found' } }]
        }
        if (func.name === 'mainClickConversationOnce') {
          currentURL = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`
          return [{ result: { status: 'clicked' } }]
        }
        if (func.name === 'mainReadThreadPage') {
          mainReadThreadCalls += 1
          throw new Error('send preflight 不得再调用带 DOM/SSR 回退的 mainReadThreadPage')
        }
        if (func.name === 'mainCaptureSendBaseline') {
          baselineCalls += 1
          assert.deepEqual(args, [conversationRef, expectedTail])
          return [{ result: structuredClone(sendBaselineResult) }]
        }
        if (func.name === 'mainSendMessageOnce') {
          mainSendCalls += 1
          if (evaluatorFunction === null) evaluatorFunction = func
          else assert.strictEqual(func, evaluatorFunction,
            'preflight 与 commit 必须注入字面同一份 evaluator 函数')
          const phase = args.at(-1)
          assert.equal(args.length, 8)
          assert.deepEqual(args.slice(5), [
            sendBaselineResult.serverSourceKeys,
            sendBaselineResult.targetBindingToken,
            phase,
          ], '同步 action 必须携带有序 baseline keys 与稳定目标绑定 token')
          if (phase === 'preflight') {
            preflightCalls += 1
            return [{ result: { status: 'ready' } }]
          }
          assert.equal(phase, 'commit')
          commitCalls += 1
          return [{ result: { status: 'clicked' } }]
        }
        if (func.name === 'mainObserveStableOutbound') {
          assert.deepEqual(args.slice(2), [
            sendBaselineResult.serverSourceKeys,
            sendBaselineResult.targetBindingToken,
          ], '每轮 observer 必须复用 baseline 目标绑定 token')
          return [{ result: {
            selected: true, matchingNewServerMessages: observePositive ? 1 : 0,
          } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = () => {
    const state = { barriers: 0 }
    return {
      state,
      value: {
        cmdMsgId: 'send-orchestration', deadlineMs: Date.now() + 60_000,
        irreversibleNotAfterMs: Date.now() + 60_000,
        commandContext: undefined, guards: undefined,
        signal: new AbortController().signal,
        async progress() {}, checkpoint() {}, async beforeSideEffect() { state.barriers += 1 },
      },
    }
  }
  try {
    const first = context()
    const firstBaselineStart = baselineCalls
    const result = await sendZhilianMessage(
      { conversationRef, text: '你好' },
      { expectedTail },
      first.value,
      fingerprint,
    )
    assert.equal(result.conversationRef, conversationRef)
    assert.equal(first.state.barriers, 1)
    assert.equal(mainSendCalls, 2, '预检与 commit 必须调用字面同一 MAIN evaluator')
    assert.equal(preflightCalls, 1)
    assert.equal(commitCalls, 1)
    assert.equal(baselineCalls - firstBaselineStart, 1,
      '完整 send preflight 必须恰好调用一次 baseline capture')
    assert.equal(selectCalls, 0, 'chat.sendMessage 只允许已打开的目标会话，不应内部导航')
    assert.equal(updateCalls, 0, '发送不得再依赖已知不可靠的 tabs.update 深链')

    observePositive = false
    const second = context()
    const secondBaselineStart = baselineCalls
    await assert.rejects(
      sendZhilianMessage(
        { conversationRef, text: '你好' },
        { expectedTail },
        second.value,
        fingerprint,
      ),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === ErrorCode.PostconditionUnconfirmed && error.sideEffect === 'possible',
    )
    assert.equal(second.state.barriers, 1)
    assert.equal(mainSendCalls, 4, '两条命令各预检一次、commit 一次；阴性观察不得重试 evaluator')
    assert.equal(preflightCalls, 2)
    assert.equal(commitCalls, 2, '两条命令各只允许一个 commit')
    assert.equal(baselineCalls - secondBaselineStart, 1,
      '后置条件阴性也不能改变 preflight 的唯一 capture')

    for (const stage of [
      'engine_unavailable',
      'session_unavailable',
      'history_first_unavailable',
      'hash_unavailable',
      'unexpected',
    ]) {
      sendBaselineResult = { status: 'failed', stage }
      const failedBaseline = context()
      const failedBaselineStart = baselineCalls
      await assert.rejects(
        sendZhilianMessage(
          { conversationRef, text: '你好' },
          { expectedTail },
          failedBaseline.value,
          fingerprint,
        ),
        (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.CtxNotReady,
        `${stage} 应映射为 CTX_NOT_READY`,
      )
      assert.equal(failedBaseline.state.barriers, 0, `${stage} 必须停在 witness barrier 之前`)
      assert.equal(mainSendCalls, 4, `${stage} 不得调用同步 MAIN evaluator`)
      assert.equal(baselineCalls - failedBaselineStart, 1, `${stage} 不得触发第二次 capture`)
    }

    for (const stage of [
      'route_changed',
      'guard_snapshot_uncovered',
    ]) {
      sendBaselineResult = { status: 'failed', stage }
      const failedBaseline = context()
      const failedBaselineStart = baselineCalls
      await assert.rejects(
        sendZhilianMessage(
          { conversationRef, text: '你好' },
          { expectedTail },
          failedBaseline.value,
          fingerprint,
        ),
        (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.GuardFailed,
        `${stage} 应映射为 GUARD_FAILED`,
      )
      assert.equal(failedBaseline.state.barriers, 0, `${stage} 必须停在 witness barrier 之前`)
      assert.equal(mainSendCalls, 4, `${stage} 不得调用同步 MAIN evaluator`)
      assert.equal(baselineCalls - failedBaselineStart, 1, `${stage} 不得触发第二次 capture`)
    }

    sendBaselineResult = {
      status: 'ready', stage: 'ready',
      serverSourceKeys: ['raw-platform-id'],
      targetBindingToken: 'b'.repeat(64),
    }
    const malformedBaseline = context()
    await assert.rejects(
      sendZhilianMessage(
        { conversationRef, text: '你好' },
        { expectedTail },
        malformedBaseline.value,
        fingerprint,
      ),
      (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.CtxNotReady,
    )
    assert.equal(malformedBaseline.state.barriers, 0, 'malformed ready baseline 必须停在 witness barrier 之前')
    assert.equal(mainSendCalls, 4, 'malformed ready baseline 不得进入 MAIN evaluator')
    assert.equal(mainReadThreadCalls, 0, 'send preflight 不得再复用可回退 DOM/SSR 的 mainReadThreadPage')
  } finally {
    globalThis.setTimeout = originalSetTimeout
  }
})

test('会话切换等待异常时 commandNavigation 必经 finally 清理', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = '9'.repeat(64)
  const conversationRef = 'conversation-navigation-finally'
  let getCalls = 0
  let barrierPassed = false
  globalThis.chrome = {
    tabs: {
      async get() {
        getCalls += 1
        if (getCalls > 1) throw new Error('tabs-get-after-click')
        return { id: 93, url: 'https://rd6.zhaopin.com/app/im', status: 'complete', active: true }
      },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func }) {
        if (func.name === 'mainFindConversation') return [{ result: { status: 'found' } }]
        if (func.name === 'mainProbeZhilian') return [{ result: {
          pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
        } }]
        if (func.name === 'mainClickConversationOnce') {
          assert.equal(barrierPassed, true, '导航 click task 必须排在 dispatcher cancellation barrier 之后')
          return [{ result: { status: 'clicked' } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = {
    cmdMsgId: 'navigation-finally', deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined, guards: undefined,
    signal: new AbortController().signal,
    async progress() {}, checkpoint() {}, async beforeSideEffect() { barrierPassed = true },
  }
  try {
    await assert.rejects(
      zhilianTestHooks.ensureThreadRoute(
        { id: 93, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' },
        conversationRef,
        fingerprint,
        context,
      ),
      /tabs-get-after-click/u,
    )
    assert.equal(
      navigationTracker.noteChromeNavigation(
        93,
        `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`,
        Date.now(),
      ),
      'unknown',
      '异常路径若泄漏 command window，这里会被误判为 command',
    )
  } finally {
    navigationTracker.removeTab(93)
    globalThis.chrome = originalChrome
  }
})

test('会话 finder 在取消后才返回也不能进入同步 click task', async () => {
  const originalChrome = globalThis.chrome
  const conversationRef = 'conversation-delayed-finder'
  let releaseFinder
  const finderGate = new Promise((resolve) => { releaseFinder = resolve })
  let clickCalls = 0
  let canceled = false
  globalThis.chrome = {
    scripting: {
      async executeScript({ func }) {
        if (func.name === 'mainFindConversation') {
          await finderGate
          return [{ result: { status: 'found' } }]
        }
        if (func.name === 'mainClickConversationOnce') {
          clickCalls += 1
          return [{ result: { status: 'clicked' } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = {
    cmdMsgId: 'delayed-finder', deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined, guards: undefined,
    signal: new AbortController().signal,
    async progress() {},
    checkpoint() { if (canceled) throw new Error('finder-canceled') },
    async beforeSideEffect() {},
  }
  try {
    const pending = zhilianTestHooks.ensureThreadRoute(
      { id: 94, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' },
      conversationRef,
      '8'.repeat(64),
      context,
    )
    canceled = true
    releaseFinder()
    await assert.rejects(pending, /finder-canceled/u)
    assert.equal(clickCalls, 0, 'finder 本身无 click，释放后 checkpoint 必须挡住 click task')
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('发送 baseline Promise 卡死跨过 timer，释放后也不能进入 attempting 或迟到 click', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = 'd'.repeat(64)
  const conversationRef = 'conversation-hanging-history'
  const tailHash = 'b'.repeat(64)
  const expectedTail = [{ direction: 'in', contentHash: tailHash }]
  let releaseHistory
  const historyGate = new Promise((resolve) => { releaseHistory = resolve })
  let mainSendCalls = 0
  let baselineCalls = 0
  let baselineSettled = false
  let mainReadThreadCalls = 0
  globalThis.chrome = {
    tabs: {
      async query() {
        return [{ id: 92, url: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`,
          status: 'complete', active: true }]
      },
      async get() {
        return { id: 92, url: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`,
          status: 'complete', active: true }
      },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func, args }) {
        if (func.name === 'mainProbeZhilian') return [{ result: {
          pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
        } }]
        if (func.name === 'mainInspectSendSurface') return [{ result: {
          selected: true,
          composerBindingResolved: true, composerBindingMatched: true,
          composerCount: 1, composerValue: '', sendButtonCount: 1,
        } }]
        if (func.name === 'mainReadThreadPage') {
          mainReadThreadCalls += 1
          throw new Error('send preflight 不得再调用 mainReadThreadPage')
        }
        if (func.name === 'mainCaptureSendBaseline') {
          baselineCalls += 1
          assert.deepEqual(args, [conversationRef, expectedTail])
          await historyGate
          baselineSettled = true
          return [{ result: {
            status: 'ready', stage: 'ready',
            serverSourceKeys: ['d'.repeat(64)],
            targetBindingToken: 'b'.repeat(64),
          } }]
        }
        if (func.name === 'mainSendMessageOnce') {
          mainSendCalls += 1
          return [{ result: { status: 'clicked' } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const storage = memoryWitnessStorage()
  const witness = new WitnessStore(storage, Date.now, () => 'witness-hanging-history')
  await witness.initialize()
  const out = recorder()
  let resultID = 0
  const durable = async (session, body, commitIdemKey) => {
    const envelope = {
      proto: 1, kind: 'result', msgId: `hanging-history-result-${++resultID}`,
      session, ts: Date.now(), attempt: 1, body,
    }
    if (commitIdemKey) await witness.commitAndEnqueue(commitIdemKey, envelope)
    else await witness.enqueueResult(envelope)
    out.send(Kind.Result, session, body)
    return 'sent'
  }
  register({
    name: Primitive.ChatSendMessage,
    class: 'effectful',
    async handler(_args, context) {
      const data = await sendZhilianMessage(
        { conversationRef, text: '你好' },
        { expectedTail },
        context,
        fingerprint,
      )
      return { status: 'ok', data, evidence: [{ type: 'outboundMessageObserved' }] }
    },
  })
  try {
    const dispatcher = new Dispatcher(out.send, undefined, witness, durable)
    const body = sendMessageCommand('sx-hanging-history', 'idem-hanging-history', {
      context: { platform: 'zhilian', accountRef: 'account-fixture', expectedPrincipalFingerprint: fingerprint },
      args: { conversationRef, text: '你好' },
      guards: { expectedTail },
      deadline: Date.now() + 1_000,
      execBudgetMs: 250,
    })
    await dispatcher.handleCmd('sx-hanging-history', 'session-hanging', 'session-hanging', body)
    await eventually(() => results(out.frames, 'sx-hanging-history').length === 1,
      'history 卡死后 timer 未产生唯一终局')
    const terminal = results(out.frames, 'sx-hanging-history')[0].body
    assert.equal(terminal.status, ResultStatus.Failed)
    assert.equal(terminal.error.code, ErrorCode.ExecTimeoutHand)
    assert.equal(terminal.error.sideEffect, 'none')
    assert.equal(baselineCalls, 1, 'timer 必须在唯一 baseline capture 挂起期间获胜')

    releaseHistory()
    await eventually(() => baselineSettled, '释放 history 后 baseline Promise 未完成')
    await sleep(20)
    assert.equal(mainSendCalls, 0, 'terminal 后释放迟到 history 绝不能进入同步 MAIN click task')
    assert.equal(await witness.findJournalByIdemKey('idem-hanging-history'), null,
      'timer 在 barrier 前获胜时不得写 attempting')
    assert.equal(results(out.frames, 'sx-hanging-history').length, 1, '迟到 handler 不能覆盖唯一终局')
    assert.equal(mainReadThreadCalls, 0, '挂起路径也不得调用 mainReadThreadPage')
  } finally {
    globalThis.chrome = originalChrome
  }
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
          { idServer: 'initial-out', status: 'success', time: 1, type: 'text', from: 'staff-from-initial', text: '招聘方' },
          { idServer: 'initial-in', time: 2, type: 'text', from: 'candidate-initial', text: '候选人' },
        ]
      },
    },
  }

  const page = await zhilianTestHooks.mainReadThreadPage(conversationRef, 8, null)
  assert.deepEqual(page.messages.map((message) => message.direction), ['out', 'in'])
  assert.equal(scriptsReadCount, 1, '会话与 staff 回退必须复用同一份 initial state 解析结果')
})

test('智联线程不依赖未验证 getSessionsByIds，分页命中后必须二次稳定采样', async () => {
  const conversationRef = 'conversation-stable-page'
  let byIdsCalls = 0
  let pageCalls = 0
  const engine = {
    sessions: [],
    async getSessionsByIds() {
      byIdsCalls += 1
      throw new Error('不得调用未经验证的按 ID API')
    },
    async getSessions() {
      pageCalls += 1
      return {
        curSessions: [{ sessionId: conversationRef, peerPartnerId: 'peer-stable', name: '脱敏候选人' }],
        hasMoreSession: false,
      }
    },
    async getHistoryMsgs() {
      return [{
        idServer: 'stable-page-message', status: 'success', time: 1_700_000_000_000,
        type: 'text', from: 'peer-stable', text: '合成消息',
      }]
    },
  }
  globalThis.location = { href: 'https://rd6.zhaopin.com/app/im' }
  globalThis.document = { scripts: [] }
  globalThis.window = { $session: { staff: { staffId: 'staff-mutated' } }, imEngine: engine }

  const page = await zhilianTestHooks.mainReadThreadPage(conversationRef, 8, null)
  assert.equal(page.messages.length, 1)
  assert.equal(page.messages[0].direction, 'in')
  assert.equal(byIdsCalls, 0)
  assert.equal(pageCalls, 2)
})

test('智联线程按已验证 getSessions 分页精确回退且不依赖目标已在首屏', async () => {
  const conversationRef = 'conversation-from-page-two'
  let byIdsCalls = 0
  const requestedPages = []
  let historyCalls = 0
  const engine = {
    sessions: [],
    async getSessionsByIds() {
      byIdsCalls += 1
      throw new Error('此构建不接受该参数形态')
    },
    async getSessions({ pageNo }) {
      requestedPages.push(pageNo)
      if (pageNo === 1) {
        return {
          curSessions: [{ sessionId: 'another-conversation', peerPartnerId: 'another-peer' }],
          hasMoreSession: true,
        }
      }
      return {
        curSessions: [{ sessionId: conversationRef, peerPartnerId: 'peer-page-two', name: '脱敏候选人' }],
        hasMoreSession: false,
      }
    },
    async getHistoryMsgs() {
      historyCalls += 1
      return [{
        idServer: 'page-two-message', status: 'success', time: 1_700_000_000_000,
        type: 'text', from: 'peer-page-two', text: '合成消息',
      }]
    },
  }
  globalThis.location = { href: 'https://rd6.zhaopin.com/app/im' }
  globalThis.document = { scripts: [] }
  globalThis.window = { $session: { staff: { staffId: 'staff-page-two' } }, imEngine: engine }

  const page = await zhilianTestHooks.mainReadThreadPage(conversationRef, 8, null)
  assert.equal(page.messages.length, 1)
  assert.equal(page.messages[0].direction, 'in')
  assert.equal(page.peer.platformUserRef, 'peer-page-two')
  assert.equal(byIdsCalls, 0)
  assert.deepEqual(requestedPages, [1, 2, 2])
  assert.equal(historyCalls, 1)
})

test('智联线程 history 拒绝在列表页响亮分阶段失败，不伪装成 DOM 路由问题', async () => {
  const conversationRef = 'conversation-history-rejected'
  let historyCalls = 0
  let domQueries = 0
  globalThis.location = { href: 'https://rd6.zhaopin.com/app/im' }
  globalThis.document = {
    scripts: [],
    querySelector() { domQueries += 1; return null },
  }
  globalThis.window = {
    $session: { staff: { staffId: 'staff-history' } },
    imEngine: {
      sessions: [{ sessionId: conversationRef, peerPartnerId: 'peer-history', name: '脱敏候选人' }],
      async getHistoryMsgs() {
        historyCalls += 1
        throw new Error('原始错误不得穿过脱敏信封')
      },
    },
  }

  const failure = await zhilianTestHooks.mainReadThreadPage(conversationRef, 8, null)
  assert.match(failure.__recruitHelperMainError,
    /read_history_dom_fallback:history_api_rejected_on_base_route/u)
  assert.equal(JSON.stringify(failure).includes('原始错误'), false)
  assert.equal(historyCalls, 1)
  assert.equal(domQueries, 0)
})

test('智联 MAIN 线程内部异常以脱敏哨兵穿过 Chrome InjectionResult', async () => {
  globalThis.location = { href: 'https://rd6.zhaopin.com/app/im' }
  globalThis.document = { scripts: [] }
  globalThis.window = {
    $session: { staff: { staffId: 'staff' } },
    imEngine: {
      sessions: [],
      async getSessions() { return { curSessions: null, hasMoreSession: false } },
    },
  }
  const sentinel = await zhilianTestHooks.mainReadThreadPage('missing-conversation', 8, null)
  assert.match(sentinel.__recruitHelperMainError, /resolve_session_scan:session_lookup_response_invalid/u)

  globalThis.chrome = { scripting: { async executeScript() { return [{ result: sentinel }] } } }
  await assert.rejects(
    zhilianTestHooks.runMain(7, async () => ({ ok: true }), []),
    /read_thread_main_failed:resolve_session_scan:session_lookup_response_invalid/u,
  )
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
  const row = {
    idServer: 'dom-message-1', status: 'success', time: 1_700_000_000_000,
    type: 'text', from: 'staff', text: 'DOM 消息',
  }
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
            { idServer: 'initial-dom-out', status: 'success', time: 1_700_000_000_000, type: 'text', from: 'staff-initial', text: '招聘方消息' },
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
  let updateCalls = 0
  let mainReadCalls = 0
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
      async update(_id, update) {
        updateCalls += 1
        currentThreadURL = update.url
        return { id: 7, url: currentThreadURL, status: 'complete' }
      },
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
        if (func.name === 'mainReadThreadPage') {
          mainReadCalls += 1
          return [{ result: await threadBehavior(...args) }]
        }
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
  assert.equal(updateCalls, 0, 'API 线程读取不得为不可见会话主动切换真人页面')
  assert.equal(mainReadCalls, 1, '首个 API 窗口只允许执行一次完整 MAIN 读取')

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
  const readsBeforeLost = mainReadCalls
  const lostContext = context()
  await assert.rejects(
    readZhilianThread(args, lostContext.value, fingerprint),
    (error) => error instanceof ZhilianPlatformError &&
      error.code === ErrorCode.CtxLostDuringExec && error.sideEffect === 'possible',
  )
  assert.equal(lostContext.state.beforeCalls, 1)
  assert.equal(updateCalls, 0, 'API 读取失败后不得偷偷导航再读一次')
  assert.equal(mainReadCalls, readsBeforeLost + 1, '上下文丢失时不得重跑完整 MAIN 读取')
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
  const commandWindow = navigation.beginCommandNavigation(1, now + 1_000)
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

test('导航归因：命令窗口内出现新鲜真人意图时真人优先且窗口立即失效', () => {
  let now = 10_000
  const navigation = new NavigationTracker(() => now)
  const commandWindow = navigation.beginCommandNavigation(7, now + 1_000)
  navigation.noteTrustedNavigationIntent(7, now)
  assert.equal(
    navigation.noteChromeNavigation(7, 'https://rd6.zhaopin.com/app/im?sessionId=manual', now + 1),
    'manual',
  )
  now += 2
  assert.equal(
    navigation.noteChromeNavigation(7, 'https://rd6.zhaopin.com/app/im?sessionId=next', now),
    'unknown',
    '真人抢占后旧 command window 不得继续吞下一次导航',
  )
  commandWindow.end()
})

test('导航归因：乱序旧事件不消费命令窗口，窗口严格服从动作截止时间', () => {
  let now = 20_000
  const navigation = new NavigationTracker(() => now)
  navigation.noteTrustedNavigationIntent(9, now - 5)
  const commandWindow = navigation.beginCommandNavigation(9, now + 50)

  assert.equal(
    navigation.noteChromeNavigation(9, 'https://rd6.zhaopin.com/app/im?sessionId=old', now - 1),
    'unknown',
    '早于窗口创建时间的迟到旧事件不得归因为本命令或借旧真人意图撤销窗口',
  )
  assert.equal(
    navigation.noteChromeNavigation(9, 'https://rd6.zhaopin.com/app/im?sessionId=command', now + 1),
    'command',
    '旧事件不得消费仍有效的命令窗口',
  )
  commandWindow.end()

  now = 30_000
  navigation.beginCommandNavigation(9, now + 10)
  assert.equal(
    navigation.noteChromeNavigation(9, 'https://rd6.zhaopin.com/app/im?sessionId=late', now + 11),
    'unknown',
    '动作截止时间之后的事件不得沿用默认 intrusive 总预算伪装成命令导航',
  )
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
    const stableStorage = { infra: { wsUrl: 'ws://127.0.0.1:18001/v1/channel' } }
    globalThis.chrome = {
      storage: {
        local: {
          async get(key = null) {
            if (key === null) return structuredClone(stableStorage)
            return Object.hasOwn(stableStorage, key) ? { [key]: structuredClone(stableStorage[key]) } : {}
          },
          async set(value) { Object.assign(stableStorage, structuredClone(value)) },
          async remove(keys) {
            for (const key of Array.isArray(keys) ? keys : [keys]) delete stableStorage[key]
          },
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
        async get(key = null) {
          if (key === null) return structuredClone(storage)
          return key in storage ? { [key]: structuredClone(storage[key]) } : {}
        },
        async set(value) { Object.assign(storage, value) },
        async remove(keys) {
          for (const key of Array.isArray(keys) ? keys : [keys]) delete storage[key]
        },
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
  assert.deepEqual(hello.body.features, [Feature.Progress1, Feature.Lease1, Feature.Cancel1, Feature.Witness1])
  assert.match(hello.body.witnessStoreId, /^witness-[0-9a-f]{24}$/)
  assert.equal(hello.body.outboxPending, 0)
  assert.equal(hello.body.journalOpen, 0)
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
