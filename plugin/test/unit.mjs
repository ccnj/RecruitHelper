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

test('智联 MAIN 发送只信 route+sender 因果绑定，列表在线 is-active 不作为目标证据', async () => {
  const original = {
    window: globalThis.window,
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
    HTMLElement: globalThis.HTMLElement,
    HTMLTextAreaElement: globalThis.HTMLTextAreaElement,
    InputEvent: globalThis.InputEvent,
    Event: globalThis.Event,
  }
  const conversationRef = 'conversation-send-main'
  const inboundText = '候选人尾消息'
  const outboundText = ' 你好  '
  const mixedTextFirst = '合成已发文本一'
  const mixedSystemText = '合成系统提示'
  const mixedCardRequest = 'synthetic-card-request'
  const mixedTextFourth = '合成已发文本四'
  const mixedTextFifth = '合成已发文本五'
  const digest = (value) => createHash('sha256').update(normalizeZhilianMessageText(value)).digest('hex')
  const sourceKey = (idServer) => createHash('sha256').update(`source-v1|${idServer}`).digest('hex')
  const timelineRows = []
  // 脱敏 real-shape：真实失败现场的尾部类别顺序为 out text / system /
  // out card / out text / out text。DOM fixture 故意不尝试表达卡片语义。
  const liveTimelineRows = [
    {
      idServer: 'server-mixed-text-1', time: 1, status: 'success', type: 'text',
      from: 'staff-fixture', text: mixedTextFirst,
    },
    {
      idServer: 'server-mixed-system-2', time: 2, status: '', type: 999,
      from: '', content: JSON.stringify({ title: mixedSystemText }),
    },
    {
      idServer: 'server-mixed-card-3', time: 3, status: 'success', type: 105,
      from: 'staff-fixture',
      content: JSON.stringify({ content: JSON.stringify({
        staffContent: '合成交换卡片', requestId: mixedCardRequest,
      }) }),
    },
    {
      idServer: 'server-mixed-text-4', time: 4, status: 'success', type: 'text',
      from: 'staff-fixture', text: mixedTextFourth,
    },
    {
      idServer: 'server-mixed-text-5', time: 5, status: 'success', type: 'text',
      from: 'staff-fixture', text: mixedTextFifth,
    },
  ]
  let nextServerSequence = 6
  const currentBaselineServerSourceKeys = () => {
    const ordered = [...liveTimelineRows]
      .map((row, sourceIndex) => ({ ...row, sourceIndex }))
      .sort((left, right) => Number(left.time) - Number(right.time) || left.sourceIndex - right.sourceIndex)
    const seen = new Set()
    const deduped = ordered.filter((row) => {
      if (seen.has(row.idServer)) return false
      seen.add(row.idServer)
      return true
    })
    return deduped.slice(-64).map((row) => sourceKey(row.idServer))
  }
  let clicks = 0
  let mutateBindingOnInput = false
  let mutateHandlerOnInput = false
  let mutateRouteOnInput = false
  let normalizeDraftOnInput = false
  let throwOnInput = false
  let mutateScalarBindingOnInput = false
  let replaceScalarContainerOnInput = false
  let addScalarProjectionOnInput = false
  let duplicateVisibleDetail = false
  class FixtureEvent {
    constructor(type, options = {}) { this.type = type; Object.assign(this, options) }
  }
  class FixtureTextArea {
    constructor() { this._value = ''; this.isConnected = true }
    get value() { return this._value }
    set value(value) { this._value = String(value) }
    getClientRects() { return [{}] }
    contains(node) { return node === this }
    closest(selector) {
      if (selector === '.im-sender__input-wrapper') return wrapper
      if (selector === '.im-session-detail') return detail
      return null
    }
    dispatchEvent(event) {
      if (event.type === 'input') {
        button.disabled = false
        if (mutateBindingOnInput) senderOwner.currentSession.sessionId = 'conversation-input-race'
        if (mutateScalarBindingOnInput) senderOwner.$data.activeSessionId = 'conversation-input-race'
        if (replaceScalarContainerOnInput) senderOwner.$data = { activeSessionId: conversationRef }
        if (addScalarProjectionOnInput) senderOwner.$data.currentSessionId = conversationRef
        if (mutateHandlerOnInput) {
          buttonOwner._events.click = [alternateSenderHandler]
          buttonOwner.$vnode.componentOptions.listeners.click = alternateSenderHandler
        }
        if (event.inputType === 'insertText' && mutateRouteOnInput) {
          globalThis.location.href = 'https://rd6.zhaopin.com/app/im?sessionId=conversation-input-route-race'
        }
        if (event.inputType === 'insertText' && normalizeDraftOnInput) this._value = normalizeZhilianMessageText(this._value)
        if (event.inputType === 'insertText' && throwOnInput) throw new Error('fixture-input-throw')
      }
      return true
    }
  }
  const composer = new FixtureTextArea()
  const textNode = (textContent) => ({ textContent })
  const messageRow = (direction, textContent) => ({
    getClientRects() { return [{}] },
    classList: {
      contains(name) {
        if (name === 'im-message__toast') return direction === 'system'
        if (name === 'im-message__bubble') return direction !== 'system'
        if (name === 'im-message__bubble--me') return direction === 'out'
        return false
      },
    },
    querySelector(selector) {
      if (selector === '.im-message__text' && direction !== 'system') return textNode(textContent)
      if (selector === '.im-message__toast-inner' && direction === 'system') return textNode(textContent)
      return null
    },
  })
  timelineRows.push(messageRow('out', mixedTextFifth))
  const unwrapFixtureListener = (value) => {
    let current = value
    for (let depth = 0; depth < 12; depth += 1) {
      if (Array.isArray(current)) {
        if (current.length !== 1) throw new Error('fixture listener ambiguous')
        current = current[0]
        continue
      }
      if (typeof current !== 'function') throw new Error('fixture listener missing')
      if (current.fns !== undefined) {
        current = current.fns
        continue
      }
      return current
    }
    throw new Error('fixture listener wrapper overflow')
  }
  class FixtureHTMLElement {
    click() {
      clicks += 1
      const handler = unwrapFixtureListener(buttonOwner._vnode.data.on.click)
      handler.call(buttonOwner)
    }
  }
  let buttonTypeAttribute = null
  const button = {
    textContent: '发送', disabled: true,
    isConnected: true, form: null,
    parentElement: null,
    getClientRects() { return [{}] },
    getAttribute(name) { return name === 'type' ? buttonTypeAttribute : null },
    closest(selector) {
      if (selector === '.im-session-detail') return detail
      if (selector === '.im-sender__input-wrapper') return wrapper
      return null
    },
    click() {
      clicks += 1
      const handler = unwrapFixtureListener(buttonOwner._vnode.data.on.click)
      handler.call(buttonOwner)
    },
  }
  const wrapper = {
    parentElement: null,
    isConnected: true,
    closest(selector) {
      if (selector === '.im-session-detail') return detail
      if (selector === '.im-sender') return senderRoot
      return null
    },
    querySelectorAll(selector) { return selector === 'button' ? [button] : [] },
    contains(node) { return node === wrapper || node === composer || node === button },
  }
  const senderRoot = {
    parentElement: null,
    isConnected: true,
    contains(node) {
      return node === senderRoot || node === wrapper || node === inputRoot || node === composer || node === button
    },
  }
  const inputRoot = {
    isConnected: true,
    contains(node) { return node === inputRoot || node === composer },
  }
  const detail = {
    parentElement: null,
    isConnected: true,
    getClientRects() { return [{}] },
    contains(node) {
      return node === detail || node === senderRoot || node === wrapper || node === inputRoot ||
        node === composer || node === button
    },
    querySelectorAll(selector) {
      if (selector.includes('.im-message__toast')) return timelineRows
      return []
    },
  }
  const duplicateDetail = { getClientRects() { return [{}] } }
  const sessionItem = {
    className: 'im-session-item km-list__item',
    __vue__: { _props: { source: { sessionId: 'conversation-unrelated-online-row' } } },
    getClientRects() { return [{}] },
  }
  const onlineStatus = {
    className: 'im-session-item__status is-active',
    parentElement: sessionItem,
    getClientRects() { return [{}] },
  }
  composer.parentElement = wrapper
  wrapper.parentElement = senderRoot
  senderRoot.parentElement = detail
  button.parentElement = wrapper
  globalThis.HTMLTextAreaElement = FixtureTextArea
  globalThis.HTMLElement = FixtureHTMLElement
  globalThis.InputEvent = FixtureEvent
  globalThis.Event = FixtureEvent
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.location = { href: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}` }
  globalThis.document = {
    scripts: [],
    querySelectorAll(selector) {
      if (selector === '.im-session-detail, .im-timeline__wrapper, .im-timeline, .im-sender') {
        return [detail, senderRoot]
      }
      if (selector === '.im-session-item.km-list__item.is-active') return []
      if (selector === '.im-session-item.km-list__item') return [sessionItem]
      if (selector === '.im-session-item__status.is-active') return [onlineStatus]
      if (selector === '.im-session-detail') return duplicateVisibleDetail ? [detail, duplicateDetail] : [detail]
      if (selector.startsWith('textarea.')) return [composer]
      if (selector === '.im-sender__input-wrapper button') return [button]
      return []
    },
  }
  const staffId = 'staff-fixture'
  const orgId = 'org-fixture'
  const loginPoint = 'login-point-fixture'
  const principalCanonical = ['zhilian-principal-v2', staffId, orgId, loginPoint]
    .map((piece) => `${new TextEncoder().encode(piece).length}:${piece}`).join('|')
  const principalFingerprint = digest(principalCanonical)
  const sessionFixture = {
    sessionId: conversationRef,
    peerPartnerId: 'candidate-fixture',
    sortTime: 123,
    modifiedTime: 456,
    lastSentence: '{"text":"候选人尾消息"}',
  }
  const sessionVersionToken = digest(JSON.stringify([
    sessionFixture.sessionId, sessionFixture.peerPartnerId,
    sessionFixture.sortTime, sessionFixture.modifiedTime, sessionFixture.lastSentence,
  ]))
  const targetBindingToken = digest(JSON.stringify([conversationRef, sessionFixture.peerPartnerId]))
  const rawMainSendMessageOnce = zhilianTestHooks.mainSendMessageOnce
  const invokeMainSendMessageOnce = (
    targetConversationRef,
    text,
    textHash,
    expectedTail,
    expectedVersionToken,
    expectedFingerprint,
    irreversibleNotAfterMs,
    phase = 'commit',
  ) => rawMainSendMessageOnce(
    targetConversationRef,
    text,
    textHash,
    expectedTail,
    expectedVersionToken,
    expectedFingerprint,
    irreversibleNotAfterMs,
    currentBaselineServerSourceKeys(),
    targetBindingToken,
    phase,
  )
  const nuxtOwner = {
    $children: [],
    $store: { state: { im: { timelineMap: { [conversationRef]: { timeline: liveTimelineRows } } } } },
  }
  nuxtOwner.$root = nuxtOwner
  const detailOwner = {
    $el: detail,
    currentSession: { sessionId: conversationRef, peerPartnerId: 'candidate-fixture' },
    $parent: nuxtOwner,
    $children: [],
  }
  const senderOwner = {
    $el: senderRoot,
    $root: nuxtOwner,
    currentSession: { sessionId: conversationRef, peerPartnerId: 'candidate-fixture' },
    $parent: detailOwner,
    $children: [],
  }
  const deliverFixtureOutbound = () => {
    timelineRows.push(messageRow('out', outboundText))
    liveTimelineRows.push({
      idServer: `server-outbound-${nextServerSequence}`,
      time: nextServerSequence,
      status: 'success',
      type: 'text',
      from: staffId,
      text: outboundText,
    })
    nextServerSequence += 1
    composer._value = ''
  }
  function sendMessage() {
    deliverFixtureOutbound()
    return this.currentSession
  }
  function alternateSendMessage() { return this.currentSession }
  senderOwner.sendMessage = sendMessage
  senderOwner.$options = { methods: { sendMessage } }
  const alternateSenderHandler = alternateSendMessage
  function emitClick() { this.$emit('click') }
  const buttonOwner = {
    $el: button,
    $parent: senderOwner,
    $children: [],
    emitClick,
    $emit(name) {
      if (name !== 'click') return
      const handler = unwrapFixtureListener(this._events.click)
      handler.call(senderOwner)
    },
    $options: { methods: { emitClick } },
    _vnode: { elm: button, data: { on: { click: emitClick } } },
    _events: { click: [sendMessage] },
    $vnode: { componentOptions: { listeners: { click: sendMessage } } },
  }
  const inputOwner = {
    $el: inputRoot,
    $parent: senderOwner,
    $children: [],
  }
  nuxtOwner.$children = [detailOwner]
  detailOwner.$children = [senderOwner]
  senderOwner.$children = [buttonOwner, inputOwner]
  globalThis.window = {
    $session: {
      isLoggedIn: true,
      staff: { staffId, defaultLoginPoint: loginPoint },
      org: { orgId },
    },
    $nuxt: nuxtOwner,
    imEngine: { sessions: [sessionFixture] },
  }
  try {
    const expectedTail = [
      { direction: 'out', contentHash: digest(mixedTextFirst) },
      { direction: 'system', contentHash: digest(mixedSystemText) },
      { direction: 'out', contentHash: digest(`card\x1fwechatExchange\x1f${mixedCardRequest}`) },
      { direction: 'out', contentHash: digest(mixedTextFourth) },
      { direction: 'out', contentHash: digest(mixedTextFifth) },
    ]
    const inspected = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(inspected.selected, true)
    assert.equal(inspected.composerBindingResolved, true)
    assert.equal(inspected.composerBindingMatched, true)
    assert.equal(inspected.diagnosticStage, 'ok')
    const mixedTailPreflight = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText), expectedTail,
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
      'preflight',
    )
    assert.deepEqual(mixedTailPreflight, { status: 'ready' },
      'text/system/card 混合 live Vuex 尾部必须通过字面同一 evaluator 的只读预检')
    assert.equal(clicks, 0, '预检不得 click')
    assert.equal(composer.value, '', '预检不得写入草稿')

    button.form = {}
    const unsafeFormInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(unsafeFormInspection.diagnosticStage, 'button_form_unsafe')
    const unsafeFormFinal = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText), expectedTail,
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(unsafeFormFinal.reason, 'composer_binding_unresolved')
    assert.equal(clicks, 0, '关联 form 且未显式 type=button 时最终路径绝不能 click')

    buttonTypeAttribute = 'button'
    const explicitButtonTypeInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(explicitButtonTypeInspection.diagnosticStage, 'ok')
    const explicitButtonTypeFinal = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'in', contentHash: digest('刻意错误的 form 安全尾锚') }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(explicitButtonTypeFinal.reason, 'guard_changed',
      '关联 form 但显式 type=button 时应通过结构绑定再由独立尾锚闭锁')
    assert.equal(clicks, 0)
    button.form = null
    buttonTypeAttribute = null

    const liveButtonVNode = buttonOwner._vnode
    delete buttonOwner._vnode
    const missingButtonVNodeInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(missingButtonVNodeInspection.diagnosticStage, 'button_vnode_missing')
    buttonOwner._vnode = liveButtonVNode

    buttonOwner._vnode.data.on.click = [emitClick, emitClick]
    const ambiguousButtonListenerInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(ambiguousButtonListenerInspection.diagnosticStage, 'button_dom_listener_ambiguous')

    function unregisteredDomHandler() { return /source-is-not-an-authorization-input/u.test('x') }
    buttonOwner._vnode.data.on.click = unregisteredDomHandler
    const unregisteredDomInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(unregisteredDomInspection.diagnosticStage, 'ok',
      '未登记到 $options.methods 的 live DOM handler 仍由结构链授权')

    function unregisteredSenderHandler() { return 'trusted Sender runtime handler' }
    const domWrapper = function domWrapper() {}
    domWrapper.fns = unregisteredDomHandler
    const eventWrapper = function eventWrapper() {}
    eventWrapper.fns = unregisteredSenderHandler
    const vnodeWrapper = function vnodeWrapper() {}
    vnodeWrapper.fns = unregisteredSenderHandler
    buttonOwner._vnode.data.on.click = domWrapper
    buttonOwner._events.click = [eventWrapper]
    buttonOwner.$vnode.componentOptions.listeners.click = vnodeWrapper
    const wrappedHandlersInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(wrappedHandlersInspection.diagnosticStage, 'ok',
      '两侧 wrapper 解开到同一 live handler 时无需解析或登记 method')
    const wrappedHandlersFinal = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'in', contentHash: digest('刻意错误的 wrapper 尾锚') }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(wrappedHandlersFinal.reason, 'guard_changed',
      '最终路径必须接受同一结构 handler 后继续由独立尾锚闭锁')
    assert.equal(clicks, 0)

    buttonOwner._events.click = [eventWrapper, eventWrapper]
    const ambiguousComponentListenerInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(ambiguousComponentListenerInspection.diagnosticStage, 'sender_listener_ambiguous')
    buttonOwner._events.click = [eventWrapper]
    const mismatchedComponentWrapper = function mismatchedComponentWrapper() {}
    mismatchedComponentWrapper.fns = alternateSenderHandler
    buttonOwner.$vnode.componentOptions.listeners.click = mismatchedComponentWrapper
    const mismatchedComponentListenerInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(mismatchedComponentListenerInspection.diagnosticStage, 'sender_listener_mismatch')

    buttonOwner._vnode.data.on.click = emitClick
    buttonOwner._events.click = [sendMessage]
    buttonOwner.$vnode.componentOptions.listeners.click = sendMessage


    globalThis.location.href = 'https://rd6.zhaopin.com/app/im?sessionId=conversation-other-route'
    const wrongRoute = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(wrongRoute.selected, false)
    assert.equal(wrongRoute.composerBindingResolved, true, '阶段码不得改变原有只读事实字段')
    assert.equal(wrongRoute.diagnosticStage, 'route_target_missing')
    globalThis.location.href = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`
    duplicateVisibleDetail = true
    const ambiguousDetail = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(ambiguousDetail.composerBindingResolved, false, '两个可见 detail 时 preflight 必须 fail-closed')
    assert.equal(ambiguousDetail.diagnosticStage, 'detail_ambiguous')
    duplicateVisibleDetail = false
    const action = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText), expectedTail,
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.deepEqual(action, { status: 'clicked' })
    assert.equal(clicks, 1)

    const stableInstanceClick = button.click
    const domRowsBeforeIntrinsic = timelineRows.length
    const liveRowsBeforeIntrinsic = liveTimelineRows.length
    let replacedInstanceClickCalls = 0
    button.click = () => { replacedInstanceClickCalls += 1; deliverFixtureOutbound(); deliverFixtureOutbound() }
    const intrinsicClickAction = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.deepEqual(intrinsicClickAction, { status: 'clicked' })
    assert.equal(replacedInstanceClickCalls, 0, '实例 click 被替换时不得采用页面覆写路径')
    assert.equal(clicks, 2, '冻结的 HTMLElement 原型 click 只允许产生一次 dispatch')
    button.click = stableInstanceClick
    timelineRows.splice(domRowsBeforeIntrinsic)
    liveTimelineRows.splice(liveRowsBeforeIntrinsic)
    clicks = 1

    const baselineBeforeBarrierRace = currentBaselineServerSourceKeys()
    liveTimelineRows.push({
      idServer: `server-concurrent-${nextServerSequence}`,
      time: nextServerSequence,
      status: 'success',
      type: 'text',
      from: staffId,
      text: outboundText,
    })
    nextServerSequence += 1
    const sourceChangedAfterBarrier = rawMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
      baselineBeforeBarrierRace, targetBindingToken, 'commit',
    )
    assert.deepEqual(sourceChangedAfterBarrier, { status: 'failed', reason: 'guard_changed' })
    assert.equal(clicks, 1, 'barrier 后同文 server success 到达但 DOM tail 未变时必须零 click')

    const mutableLiveTail = liveTimelineRows[liveTimelineRows.length - 1]
    const stableLiveTail = { ...mutableLiveTail }
    const stableLiveKeys = currentBaselineServerSourceKeys()
    for (const [field, value] of [
      ['text', 'barrier 后同 ID 正文被改写'],
      ['from', 'candidate-fixture'],
      ['status', 'failed'],
    ]) {
      Object.assign(mutableLiveTail, stableLiveTail, { [field]: value })
      const rewrittenSameServerID = rawMainSendMessageOnce(
        conversationRef, outboundText, digest(outboundText),
        [{ direction: 'out', contentHash: digest(outboundText) }],
        sessionVersionToken, principalFingerprint, Date.now() + 10_000,
        stableLiveKeys, targetBindingToken, 'commit',
      )
      assert.deepEqual(rewrittenSameServerID, { status: 'failed', reason: 'guard_changed' },
        `barrier 后同 idServer 的 ${field} 被原地改写时必须由 live tail 闭锁`)
      assert.equal(clicks, 1, `同 ID ${field} 改写且 DOM 未变时必须零 click`)
    }
    Object.assign(mutableLiveTail, stableLiveTail)

    globalThis.location.href = `https://rd6.zhaopin.com/app/other?sessionId=${conversationRef}`
    const wrongPathSameSession = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.deepEqual(wrongPathSameSession, { status: 'failed', reason: 'route_changed' })
    assert.equal(clicks, 1, 'sessionId 相同但 pathname 不是 /app/im 时必须零 click')
    globalThis.location.href = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`

    buttonOwner._events.click = [alternateSenderHandler]
    buttonOwner.$vnode.componentOptions.listeners.click = sendMessage
    const closureToOtherSender = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(closureToOtherSender.reason, 'composer_binding_unresolved')
    assert.equal(composer.value, '', 'component listener 两侧不一致时必须零输入')
    assert.equal(clicks, 1, 'component listener 两侧不一致时必须零 click')
    buttonOwner._events.click = [sendMessage]
    buttonOwner.$vnode.componentOptions.listeners.click = sendMessage

    senderOwner._isDestroyed = true
    const deadOwnerInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(deadOwnerInspection.composerBindingResolved, false)
    assert.equal(deadOwnerInspection.diagnosticStage, 'sender_owner_inactive')
    const deadOwner = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(deadOwner.reason, 'composer_binding_unresolved')
    assert.equal(composer.value, '')
    assert.equal(clicks, 1, 'destroyed Sender owner 绝不能 click')
    senderOwner._isDestroyed = false

    const overflowComponents = Array.from({ length: 4092 }, () => ({ $children: [] }))
    nuxtOwner.$children = [detailOwner, ...overflowComponents]
    const overflowInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(overflowInspection.composerBindingResolved, false, '组件树 >4096 必须 unresolved')
    assert.equal(overflowInspection.diagnosticStage, 'component_tree_overflow')
    const overflow = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(overflow.reason, 'composer_binding_unresolved')
    assert.equal(composer.value, '')
    assert.equal(clicks, 1, '组件树超上界时绝不能 click')
    nuxtOwner.$children = [detailOwner]

    const mountedNuxt = globalThis.window.$nuxt
    delete globalThis.window.$nuxt
    const missingTreeRootInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(missingTreeRootInspection.composerBindingResolved, false)
    assert.equal(missingTreeRootInspection.diagnosticStage, 'component_tree_root_missing')

    nuxtOwner.$root = nuxtOwner
    senderOwner.$root = nuxtOwner
    senderRoot.__vue__ = senderOwner
    const domOwnedTreeInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(domOwnedTreeInspection.composerBindingResolved, true,
      '无 window.$nuxt 时只允许从发送区 DOM owner 的唯一自反 $root 恢复完整树')
    assert.equal(domOwnedTreeInspection.diagnosticStage, 'ok')
    const domOwnedFinalCheck = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest('刻意不匹配的尾锚') }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(domOwnedFinalCheck.reason, 'guard_changed',
      '最终不可逆 MAIN 路径必须使用同一 DOM owner 根发现规则，再由尾锚独立 fail-closed')
    assert.equal(composer.value, '')
    assert.equal(clicks, 1, 'DOM owner 根兼容回归不得产生额外 click')
    delete senderRoot.__vue__
    senderOwner.$root = nuxtOwner
    nuxtOwner.$root = nuxtOwner
    globalThis.window.$nuxt = mountedNuxt

    nuxtOwner.$children = [null]
    const malformedTreeInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(malformedTreeInspection.composerBindingResolved, false)
    assert.equal(malformedTreeInspection.diagnosticStage, 'component_tree_malformed')
    nuxtOwner.$children = [detailOwner]

    senderOwner.currentSession.sessionId = 'conversation-old-detail'
    const staleDetailInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(staleDetailInspection.composerBindingResolved, false)
    assert.equal(staleDetailInspection.diagnosticStage, 'model_target_mismatch')
    const staleDetail = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(staleDetail.reason, 'composer_binding_unresolved')
    assert.equal(clicks, 1, 'URL/左侧/engine 均为目标但右侧 detail 属于旧会话时必须零输入零 click')
    senderOwner.currentSession.sessionId = conversationRef

    const boundSession = senderOwner.currentSession
    delete senderOwner.currentSession
    const missingDirectBindingInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(missingDirectBindingInspection.composerBindingResolved, false)
    assert.equal(missingDirectBindingInspection.diagnosticStage, 'model_scalar_absent')
    const missingDirectBinding = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(missingDirectBinding.reason, 'composer_binding_unresolved')
    assert.equal(clicks, 1, '仅有 URL/左侧/engine/DetailShell 证据不得代替 Sender 直绑')

    senderOwner.$store = { state: { im: { activeSessionId: conversationRef } } }
    const storeScalarInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(storeScalarInspection.diagnosticStage, 'model_scalar_store',
      'scalar-only 至少需要两个独立容器/路径，单一 store 标量不能授权')
    senderOwner.$data = { activeSessionId: conversationRef }

    function unregisteredScalarHandler() { return 'source is outside trust boundary' }
    const scalarEventWrapper = function scalarEventWrapper() {}
    scalarEventWrapper.fns = unregisteredScalarHandler
    const scalarVNodeWrapper = function scalarVNodeWrapper() {}
    scalarVNodeWrapper.fns = unregisteredScalarHandler
    buttonOwner._events.click = [scalarEventWrapper]
    buttonOwner.$vnode.componentOptions.listeners.click = scalarVNodeWrapper
    const unregisteredScalarInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(unregisteredScalarInspection.diagnosticStage, 'ok',
      '标量投影完整且 wrapper 解开到同一运行时 handler 时，不要求 method 登记或源码证明')
    const unregisteredScalarFinal = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'in', contentHash: digest('刻意错误的未登记 handler 尾锚') }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(unregisteredScalarFinal.reason, 'guard_changed')
    assert.equal(clicks, 1, '未登记 handler 的结构授权不得绕过独立尾锚')

    function sendMessageFromScalar() {
      deliverFixtureOutbound()
      return this.$data.activeSessionId
    }
    senderOwner.sendMessage = sendMessageFromScalar
    senderOwner.$options.methods.sendMessage = sendMessageFromScalar
    buttonOwner._events.click = [sendMessageFromScalar]
    buttonOwner.$vnode.componentOptions.listeners.click = sendMessageFromScalar
    const multipleScalarInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(multipleScalarInspection.composerBindingResolved, true,
      '多个显式标量全部精确指向当前会话且结构 listener 链一致时可构成绑定')
    assert.equal(multipleScalarInspection.diagnosticStage, 'ok')

    senderOwner.$data.propsSessionId = { sessionId: 'conversation-hidden-conflict' }
    const malformedScalarInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(malformedScalarInspection.diagnosticStage, 'model_scalar_conflict',
      '闭集字段存在但类型不可规范化时不能静默当作 absent')
    const malformedScalarFinal = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(malformedScalarFinal.reason, 'composer_binding_unresolved')
    assert.equal(clicks, 1, '畸形标量槽绝不能 click')
    delete senderOwner.$data.propsSessionId

    senderOwner.currentSession = true
    const malformedDirectInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(malformedDirectInspection.diagnosticStage, 'model_target_mismatch',
      'direct 对象槽存在但不是对象时必须闭锁')
    const malformedDirectFinal = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(malformedDirectFinal.reason, 'composer_binding_unresolved')
    assert.equal(clicks, 1, '畸形 direct 对象槽绝不能 click')
    delete senderOwner.currentSession

    const stableDataContainer = senderOwner.$data
    senderOwner.$data = []
    const malformedContainerInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(malformedContainerInspection.diagnosticStage, 'model_candidate_conflict',
      '闭集中间容器非空但不是普通对象时不得静默当作 absent')
    const malformedContainerFinal = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(malformedContainerFinal.reason, 'composer_binding_unresolved')
    assert.equal(clicks, 1, '畸形中间容器绝不能 click')
    senderOwner.$data = stableDataContainer

    const multipleScalarFinalCheck = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest('标量绑定下刻意错误的尾锚') }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(multipleScalarFinalCheck.reason, 'guard_changed',
      '最终不可逆路径必须接受同一标量绑定后继续由独立尾锚闭锁')
    assert.equal(composer.value, '')
    assert.equal(clicks, 1, '标量绑定回归不得产生额外 click')

    const scalarRowsBeforeSuccess = timelineRows.length
    const scalarSuccess = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.deepEqual(scalarSuccess, { status: 'clicked' })
    assert.equal(clicks, 2, '合法标量绑定必须恰好产生一次 click')
    timelineRows.splice(scalarRowsBeforeSuccess)
    clicks = 1

    mutateScalarBindingOnInput = true
    const scalarInputRace = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(scalarInputRace.reason, 'composer_binding_unresolved')
    assert.equal(composer.value, '', 'input 同步事件修改显式标量时必须清理自动草稿')
    assert.equal(clicks, 1, '标量值在 click 前变化时绝不能 click')
    mutateScalarBindingOnInput = false
    senderOwner.$data.activeSessionId = conversationRef

    replaceScalarContainerOnInput = true
    const scalarContainerRace = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(scalarContainerRace.reason, 'composer_binding_changed')
    assert.equal(composer.value, '', 'input 同步替换为同值容器也必须清理自动草稿')
    assert.equal(clicks, 1, '同值标量容器身份变化时绝不能 click')
    replaceScalarContainerOnInput = false

    addScalarProjectionOnInput = true
    const scalarProjectionRace = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(scalarProjectionRace.reason, 'composer_binding_changed')
    assert.equal(composer.value, '', 'input 同步新增同值标量投影也必须清理自动草稿')
    assert.equal(clicks, 1, '标量投影集合变化时绝不能 click')
    addScalarProjectionOnInput = false
    delete senderOwner.$data.currentSessionId

    const stableScalarData = senderOwner.$data
    let scalarDataGetterReads = 0
    Object.defineProperty(senderOwner, '$data', {
      configurable: true,
      get() {
        scalarDataGetterReads += 1
        return scalarDataGetterReads === 1
          ? stableScalarData
          : { activeSessionId: 'conversation-getter-race' }
      },
    })
    const getterScalarInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(getterScalarInspection.diagnosticStage, 'ok')
    assert.equal(scalarDataGetterReads, 1, '单轮绑定必须只取得一次标量容器')
    scalarDataGetterReads = 0
    const getterScalarFinal = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(getterScalarFinal.reason, 'composer_binding_unresolved')
    assert.equal(composer.value, '')
    assert.equal(clicks, 1, '跨 surface 返回不同容器/值的 getter 必须在输入前闭锁')
    Object.defineProperty(senderOwner, '$data', {
      configurable: true,
      writable: true,
      value: stableScalarData,
    })

    senderOwner.currentSession = boundSession
    senderOwner.$data.activeSessionId = 'conversation-conflict'
    function sendMessageFromMixedModels() {
      return this.currentSession && this.$data.activeSessionId
    }
    senderOwner.sendMessage = sendMessageFromMixedModels
    senderOwner.$options.methods.sendMessage = sendMessageFromMixedModels
    buttonOwner._events.click = [sendMessageFromMixedModels]
    buttonOwner.$vnode.componentOptions.listeners.click = sendMessageFromMixedModels
    const conflictingScalarInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(conflictingScalarInspection.diagnosticStage, 'model_scalar_conflict')
    const conflictingScalarFinalCheck = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(conflictingScalarFinalCheck.reason, 'composer_binding_unresolved')
    assert.equal(composer.value, '')
    assert.equal(clicks, 1, 'direct 对象正确也不能掩盖任一显式标量冲突')
    delete senderOwner.currentSession
    delete senderOwner.$store
    delete senderOwner.$data
    senderOwner.sendMessage = sendMessage
    senderOwner.$options.methods.sendMessage = sendMessage
    buttonOwner._events.click = [sendMessage]
    buttonOwner.$vnode.componentOptions.listeners.click = sendMessage

    senderOwner._props = { currentSession: { ...boundSession } }
    const propsCandidateInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(propsCandidateInspection.diagnosticStage, 'model_candidate_props')
    assert.equal(propsCandidateInspection.composerBindingResolved, false,
      '候选位置诊断只观察，不得直接授权发送绑定')

    senderOwner.$data = { currentSession: { ...boundSession } }
    const multipleCandidateInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(multipleCandidateInspection.diagnosticStage, 'model_candidate_multiple')
    senderOwner.$data.currentSession.peerPartnerId = 'candidate-conflict'
    const conflictingCandidateInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(conflictingCandidateInspection.diagnosticStage, 'model_candidate_conflict')
    delete senderOwner._props
    delete senderOwner.$data

    senderOwner.currentSession = boundSession
    senderOwner.$data = {
      currentSession: { sessionId: 'conversation-conflict', peerPartnerId: 'candidate-conflict' },
    }
    const directWithCandidateConflict = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(directWithCandidateConflict.diagnosticStage, 'model_candidate_conflict',
      'direct 对象正确时也必须扫描并拒绝替代对象冲突')
    const directWithCandidateConflictFinal = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(directWithCandidateConflictFinal.reason, 'composer_binding_unresolved')
    assert.equal(clicks, 1, '替代对象冲突时最终路径绝不能 click')
    delete senderOwner.currentSession
    delete senderOwner.$data

    senderOwner.$store = { state: { im: { currentSession: { ...boundSession } } } }
    const storeCandidateInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(storeCandidateInspection.diagnosticStage, 'model_candidate_store')
    delete senderOwner.$store
    senderOwner.currentSession = boundSession

    senderOwner.propsSession = { ...boundSession }
    const clonedDirectBindingInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(clonedDirectBindingInspection.composerBindingResolved, true,
      '两个直接槽可为同一目标投影的不同对象')
    assert.equal(clonedDirectBindingInspection.diagnosticStage, 'ok')
    const clonedDirectFinalCheck = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest('另一条刻意错误尾锚') }],
      sessionVersionToken, principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(clonedDirectFinalCheck.reason, 'guard_changed')
    assert.equal(composer.value, '')
    assert.equal(clicks, 1, '同目标 clone 槽兼容不得绕过独立尾锚')

    senderOwner.propsSession.peerPartnerId = 'candidate-conflict'
    const conflictingDirectBindingInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(conflictingDirectBindingInspection.composerBindingResolved, false)
    assert.equal(conflictingDirectBindingInspection.diagnosticStage, 'model_slot_ambiguous')
    delete senderOwner.propsSession

    buttonOwner.$vnode.componentOptions.listeners.click = alternateSenderHandler
    const mismatchedListenerInspection = await zhilianTestHooks.mainInspectSendSurface(conversationRef)
    assert.equal(mismatchedListenerInspection.composerBindingResolved, false)
    assert.equal(mismatchedListenerInspection.diagnosticStage, 'sender_listener_mismatch')
    buttonOwner.$vnode.componentOptions.listeners.click = sendMessage

    const diagnosticStages = [
      inspected.diagnosticStage,
      unsafeFormInspection.diagnosticStage,
      explicitButtonTypeInspection.diagnosticStage,
      missingButtonVNodeInspection.diagnosticStage,
      ambiguousButtonListenerInspection.diagnosticStage,
      unregisteredDomInspection.diagnosticStage,
      wrappedHandlersInspection.diagnosticStage,
      ambiguousComponentListenerInspection.diagnosticStage,
      mismatchedComponentListenerInspection.diagnosticStage,
      wrongRoute.diagnosticStage,
      ambiguousDetail.diagnosticStage,
      deadOwnerInspection.diagnosticStage,
      overflowInspection.diagnosticStage,
      missingTreeRootInspection.diagnosticStage,
      domOwnedTreeInspection.diagnosticStage,
      malformedTreeInspection.diagnosticStage,
      staleDetailInspection.diagnosticStage,
      missingDirectBindingInspection.diagnosticStage,
      storeScalarInspection.diagnosticStage,
      multipleScalarInspection.diagnosticStage,
      conflictingScalarInspection.diagnosticStage,
      propsCandidateInspection.diagnosticStage,
      multipleCandidateInspection.diagnosticStage,
      conflictingCandidateInspection.diagnosticStage,
      storeCandidateInspection.diagnosticStage,
      clonedDirectBindingInspection.diagnosticStage,
      conflictingDirectBindingInspection.diagnosticStage,
      mismatchedListenerInspection.diagnosticStage,
    ]
    for (const stage of diagnosticStages) {
      assert.match(stage, /^[a-z]+(?:_[a-z]+)*$/u)
      assert.equal(stage.includes(conversationRef), false, '阶段码不得携带会话标识')
      assert.equal(stage.includes(inboundText), false, '阶段码不得携带消息正文')
      assert.equal(stage.includes('.im-'), false, '阶段码不得携带 selector')
    }

    mutateBindingOnInput = true
    const inputRace = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(inputRace.reason, 'composer_binding_unresolved')
    assert.equal(composer.value, '', '绑定竞态失败时必须清理本次自动写入')
    assert.equal(clicks, 1, 'input 同步事件切换 owner 后绝不能 click')
    mutateBindingOnInput = false
    senderOwner.currentSession.sessionId = conversationRef

    mutateHandlerOnInput = true
    const handlerRace = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(handlerRace.reason, 'composer_binding_changed')
    assert.equal(composer.value, '', 'input 事件替换 sender handler 时必须清理草稿')
    assert.equal(clicks, 1, 'input 前后 handler 不恒等时绝不能 click')
    mutateHandlerOnInput = false
    buttonOwner._events.click = [sendMessage]
    buttonOwner.$vnode.componentOptions.listeners.click = sendMessage

    mutateRouteOnInput = true
    const routeRaceAfterInput = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(routeRaceAfterInput.reason, 'route_changed')
    assert.equal(composer.value, '', 'input 同步事件切换 route 时必须清理本次自动草稿')
    assert.equal(clicks, 1, 'input 后 route 改变绝不能 click')
    mutateRouteOnInput = false
    globalThis.location.href = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`

    normalizeDraftOnInput = true
    const normalizedDraft = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'out', contentHash: digest(outboundText) }],
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(normalizedDraft.reason, 'input_rejected')
    assert.equal(composer.value, '', '页面同步规范化自动正文时也必须强制清空草稿')
    assert.equal(clicks, 1)
    normalizeDraftOnInput = false

    throwOnInput = true
    assert.throws(
      () => invokeMainSendMessageOnce(
        conversationRef, outboundText, digest(outboundText),
        [{ direction: 'out', contentHash: digest(outboundText) }],
        sessionVersionToken,
        principalFingerprint, Date.now() + 10_000,
      ),
      /fixture-input-throw/u,
    )
    assert.equal(composer.value, '', 'input 事件抛错后也不得留下自动草稿')
    assert.equal(clicks, 1)
    throwOnInput = false

    const changed = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText),
      [{ direction: 'in', contentHash: 'f'.repeat(64) }],
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(changed.status, 'failed')
    assert.equal(changed.reason, 'guard_changed')
    assert.equal(clicks, 1, '尾锚失败后绝不能产生第二次 click')

    const preBarrierTail = [{ direction: 'out', contentHash: digest(outboundText) }]
    const racedInbound = 'attempting 后插入的新消息'
    liveTimelineRows.push({
      idServer: `server-raced-inbound-${nextServerSequence}`,
      time: nextServerSequence,
      status: 'success',
      type: 'text',
      from: 'candidate-fixture',
      text: racedInbound,
    })
    nextServerSequence += 1
    const racedTail = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText), preBarrierTail,
      sessionVersionToken,
      principalFingerprint, Date.now() + 10_000,
    )
    assert.equal(racedTail.reason, 'guard_changed')
    assert.equal(clicks, 1, 'attempting 后尾消息变化必须在同步 MAIN task 内拦截')

    const currentTail = [{ direction: 'in', contentHash: digest(racedInbound) }]
    const wrongIdentity = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText), currentTail,
      sessionVersionToken,
      'f'.repeat(64), Date.now() + 10_000,
    )
    assert.equal(wrongIdentity.reason, 'identity_changed')
    assert.equal(clicks, 1, '主体指纹不匹配绝不能 click')

    const elapsed = invokeMainSendMessageOnce(
      conversationRef, outboundText, digest(outboundText), currentTail,
      sessionVersionToken,
      principalFingerprint, Date.now() - 1,
    )
    assert.equal(elapsed.reason, 'action_window_elapsed')
    assert.equal(clicks, 1, '不可逆动作窗口过期绝不能 click')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('诊断、发送 baseline 与后置观察共用 live Vuex 双采样且始终零 history', async () => {
  const originalCryptoDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto')
  const original = {
    chrome: globalThis.chrome,
    window: globalThis.window,
    document: globalThis.document,
    location: globalThis.location,
    setTimeout: globalThis.setTimeout,
  }
  const conversationRef = 'conversation-live-send-lifecycle'
  const staffId = 'staff-live-send-lifecycle'
  const target = 'candidate-live-send-lifecycle'
  const text = '你好'
  const textHash = createHash('sha256').update(text).digest('hex')
  const sourceKey = (id) => createHash('sha256').update(`source-v1|${id}`).digest('hex')
  const old = {
    idServer: 'server-old', time: 1, type: 'text', from: target, text: '旧消息',
  }
  const liveEntry = { timeline: [old] }
  const root = { $store: { state: { im: { timelineMap: { [conversationRef]: liveEntry } } } } }
  root.$root = root
  const session = {
    sessionId: conversationRef, peerPartnerId: target,
    sortTime: 1, modifiedTime: 2, lastSentence: '{"text":"旧消息"}',
  }
  let historyCalls = 0
  let timerCalls = 0
  let mutateDuringDigest = null
  globalThis.location = { href: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}` }
  globalThis.document = { scripts: [], querySelectorAll() { return [] } }
  globalThis.window = {
    $nuxt: root,
    $session: { staff: { staffId } },
    imEngine: {
      sessions: [session],
      async getHistoryMsgs() {
        historyCalls += 1
        throw new Error('live-only 发送链不得调用 history')
      },
    },
  }
  globalThis.setTimeout = (callback, ms) => {
    assert.equal(ms, 250, 'live baseline 两份同步样本之间只允许固定 250ms 窗口')
    timerCalls += 1
    queueMicrotask(callback)
    return timerCalls
  }
  const realDigest = globalThis.crypto.subtle.digest.bind(globalThis.crypto.subtle)
  Object.defineProperty(globalThis, 'crypto', { configurable: true, writable: true, value: {
    subtle: {
      async digest(...args) {
        const result = await realDigest(...args)
        const mutation = mutateDuringDigest
        mutateDuringDigest = null
        if (mutation) mutation()
        return result
      },
    },
  } })

  const tab = {
    id: 17,
    active: true,
    status: 'complete',
    url: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`,
  }
  let tabs = [tab]
  let snapshots = []
  let executeCalls = 0
  let mutationCalls = 0
  let mainCalls = []
  let useActualBaseline = true
  const privateSourceKey = 'a'.repeat(64)
  const privateSessionToken = 'b'.repeat(64)
  const privateTargetToken = 'c'.repeat(64)
  let baselineResult = {
    status: 'ready', stage: 'ready',
    serverSourceKeys: [privateSourceKey], sessionVersionToken: privateSessionToken,
    targetBindingToken: privateTargetToken,
  }
  const snapshot = (overrides = {}) => ({
    selected: true,
    composerBindingResolved: true,
    composerBindingMatched: true,
    composerCount: 1,
    composerValue: '',
    sendButtonCount: 1,
    diagnosticStage: 'ok',
    ...overrides,
  })
  const forbiddenMutation = async () => { mutationCalls += 1; return {} }
  globalThis.chrome = {
    tabs: {
      async query() { return tabs },
      async sendMessage() { return { ok: true } },
      create: forbiddenMutation,
      update: forbiddenMutation,
      reload: forbiddenMutation,
    },
    scripting: {
      async executeScript({ func, args }) {
        executeCalls += 1
        mainCalls.push(func.name)
        let next
        if (func.name === 'mainInspectSendSurface') next = snapshots.shift()
        else if (func.name === 'mainCaptureSendBaseline') {
          assert.equal(args[0], conversationRef)
          assert.deepEqual(args[1], [])
          next = useActualBaseline ? await func(...args) : baselineResult
        } else {
          throw new Error(`readonly diagnostic called unexpected MAIN function ${func.name}`)
        }
        if (next instanceof Error) throw next
        return [{ result: structuredClone(next) }]
      },
    },
    storage: { local: { get: forbiddenMutation, set: forbiddenMutation, remove: forbiddenMutation } },
  }
  try {
    tabs = []
    assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), {
      ready: false, stage: 'page_absent',
    })
    assert.equal(executeCalls, 0)

    tabs = [{ ...tab, url: 'https://rd6.zhaopin.com/app/im' }]
    assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), {
      ready: false, stage: 'route_missing',
    })
    assert.equal(executeCalls, 0)

    tabs = [tab]
    snapshots = [snapshot(), snapshot()]
    mainCalls = []
    const ready = await inspectZhilianSendSurfaceDiagnostic()
    assert.deepEqual(ready, { ready: true, stage: 'ready' })
    assert.deepEqual(mainCalls, [
      'mainInspectSendSurface', 'mainInspectSendSurface',
      'mainCaptureSendBaseline',
    ], 'readonly debug 必须直接执行生产 live baseline')
    assert.deepEqual(Object.keys(ready).sort(), ['ready', 'stage'])
    assert.equal(historyCalls, 0, 'debug live baseline 不得调用 history')

    snapshots = [snapshot(), snapshot()]
    assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), { ready: true, stage: 'ready' },
      '同一 live root 生命周期内重复诊断必须保持可执行')
    assert.equal(historyCalls, 0)

    const oldTail = [{ direction: 'in', contentHash: createHash('sha256').update(old.text).digest('hex') }]
    const productionBaseline = await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, oldTail)
    assert.equal(productionBaseline.status, 'ready')
    assert.deepEqual(productionBaseline.serverSourceKeys, [sourceKey(old.idServer)])
    assert.match(productionBaseline.targetBindingToken, /^[0-9a-f]{64}$/)
    assert.equal(historyCalls, 0, '生产 capture 与 debug 必须共用零 history 路径')

    const baseline = [sourceKey(old.idServer)]
    liveEntry.timeline = [
      old,
      { sendMessageId: 'client-optimistic-only', time: 2, type: 'text', from: staffId, text },
    ]
    const optimistic = await zhilianTestHooks.mainObserveStableOutbound(
      conversationRef, textHash, baseline, productionBaseline.targetBindingToken,
    )
    assert.equal(optimistic.matchingNewServerMessages, 0,
      '仅 client sendMessageId 的乐观行不能完成 effectful 原语')

    liveEntry.timeline = [
      old,
      { idServer: 'server-failed', time: 2, status: 'failed', type: 'text', from: staffId, text },
      { idServer: 'server-confirmed', time: 3, status: 'success', type: 'text', from: staffId, text },
    ]
    const failedThenConfirmed = await zhilianTestHooks.mainObserveStableOutbound(
      conversationRef, textHash, baseline, productionBaseline.targetBindingToken,
    )
    assert.equal(failedThenConfirmed.matchingNewServerMessages, 0,
      'baseline 后 failed+success 共追加两行必须按严格连续性返回阴性')

    liveEntry.timeline = [
      old,
      { idServer: 'server-confirmed', time: 3, status: 'success', type: 'text', from: staffId, text },
    ]
    const confirmed = await zhilianTestHooks.mainObserveStableOutbound(
      conversationRef, textHash, baseline, productionBaseline.targetBindingToken,
    )
    assert.equal(confirmed.matchingNewServerMessages, 1,
      'baseline 后恰好追加一个唯一 server success 才能观察到正匹配')

    liveEntry.timeline.push({
      idServer: 'server-confirmed-duplicate', time: 4,
      status: 'success', type: 'text', from: staffId, text,
    })
    const ambiguous = await zhilianTestHooks.mainObserveStableOutbound(
      conversationRef, textHash, baseline, productionBaseline.targetBindingToken,
    )
    assert.equal(ambiguous.matchingNewServerMessages, 0,
      '两个同文新 server success 违反严格单追加连续性，必须保持阴性')

    liveEntry.timeline = [
      old,
      { idServer: 'server-rebound-check', time: 3, status: 'success', type: 'text', from: staffId, text },
    ]
    mutateDuringDigest = () => { session.peerPartnerId = 'candidate-rebound-during-digest' }
    const rebound = await zhilianTestHooks.mainObserveStableOutbound(
      conversationRef, textHash, baseline, productionBaseline.targetBindingToken,
    )
    assert.equal(rebound.matchingNewServerMessages, 0,
      'observer 多次 digest 期间 session peer 换绑必须在返回前复核为零匹配')
    session.peerPartnerId = target

    liveEntry.timeline = [
      old,
      { idServer: 'server-rebound-between-rounds', time: 3, status: 'success', type: 'text', from: staffId, text },
    ]
    session.peerPartnerId = 'candidate-rebound-before-observer'
    const reboundBeforeObserver = await zhilianTestHooks.mainObserveStableOutbound(
      conversationRef, textHash, baseline, productionBaseline.targetBindingToken,
    )
    assert.equal(reboundBeforeObserver.matchingNewServerMessages, 0,
      'observer 新一轮开始前 peer 已换绑时不得把当轮 peer 重新信任为基线')
    session.peerPartnerId = target

    const outsideWindowMatch = {
      idServer: 'server-outside-last64', time: 1,
      status: 'success', type: 'text', from: staffId, text,
    }
    const fillers = Array.from({ length: 64 }, (_, index) => ({
      idServer: `server-window-filler-${index}`, time: index + 2,
      type: 'text', from: target, text: `填充消息${index}`,
    }))
    const baselineWindow = fillers.map((row) => sourceKey(row.idServer))
    liveEntry.timeline = [
      outsideWindowMatch,
      ...fillers,
      {
        idServer: 'server-window-appended', time: 66,
        status: 'success', type: 'text', from: staffId, text: '本轮不同正文',
      },
    ]
    const windowed = await zhilianTestHooks.mainObserveStableOutbound(
      conversationRef, textHash, baselineWindow, productionBaseline.targetBindingToken,
    )
    assert.equal(windowed.matchingNewServerMessages, 0,
      '严格连续窗成立时，last64 之外旧同文也不得计入 post 匹配')
    assert.equal(historyCalls, 0, 'post observer 重复执行也必须始终零 history')

    useActualBaseline = false
    const baselineStageMappings = [
      ['engine_unavailable', 'baseline_engine_unavailable'],
      ['route_changed', 'baseline_route_changed'],
      ['session_unavailable', 'baseline_session_unavailable'],
      ['history_first_unavailable', 'baseline_history_unavailable'],
      ['history_second_unavailable', 'baseline_history_unavailable'],
      ['history_unstable', 'baseline_history_unstable'],
      ['guard_snapshot_uncovered', 'baseline_guard_uncovered'],
      ['session_changed', 'baseline_session_changed'],
      ['hash_unavailable', 'baseline_hash_unavailable'],
      ['unexpected', 'baseline_unexpected'],
    ]
    for (const [internalStage, publicStage] of baselineStageMappings) {
      baselineResult = { status: 'failed', stage: internalStage }
      snapshots = [snapshot(), snapshot()]
      assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), {
        ready: false, stage: publicStage,
      })
    }
    baselineResult = {
      status: 'ready', stage: 'ready',
      serverSourceKeys: ['raw-platform-id'], sessionVersionToken: privateSessionToken,
      targetBindingToken: privateTargetToken,
    }
    snapshots = [snapshot(), snapshot()]
    assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), {
      ready: false, stage: 'baseline_unexpected',
    }, 'malformed ready baseline 必须闭锁为公开 unexpected')
    baselineResult = {
      status: 'ready', stage: 'ready',
      serverSourceKeys: [privateSourceKey], sessionVersionToken: privateSessionToken,
      targetBindingToken: privateTargetToken,
    }

    snapshots = [snapshot({ diagnosticStage: 'model_target_mismatch' }),
      snapshot({ diagnosticStage: 'model_target_mismatch' })]
    assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), {
      ready: false, stage: 'direct_model_target_mismatch',
    })

    const buttonStageMappings = [
      ['button_form_unsafe', 'button_form_unsafe'],
      ['button_vnode_missing', 'button_vnode_missing'],
      ['button_dom_listener_ambiguous', 'button_dom_listener_ambiguous'],
    ]
    for (const [internalStage, publicStage] of buttonStageMappings) {
      snapshots = [snapshot({ diagnosticStage: internalStage }), snapshot({ diagnosticStage: internalStage })]
      assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), {
        ready: false,
        stage: publicStage,
      })
    }

    snapshots = [snapshot({ diagnosticStage: 'model_slot_ambiguous' }),
      snapshot({ diagnosticStage: 'model_slot_ambiguous' })]
    assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), {
      ready: false, stage: 'direct_model_target_mismatch',
    })

    const privateDraft = '不得出现在诊断结果中的人工草稿'
    snapshots = [snapshot({ composerValue: privateDraft }), snapshot({ composerValue: privateDraft })]
    const draft = await inspectZhilianSendSurfaceDiagnostic()
    assert.deepEqual(draft, { ready: false, stage: 'draft_present' })
    assert.equal(JSON.stringify(draft).includes(privateDraft), false)
    assert.equal(JSON.stringify(draft).includes(conversationRef), false)

    snapshots = [snapshot(), snapshot({ sendButtonCount: 2, diagnosticStage: 'button_count' })]
    assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), {
      ready: false, stage: 'unstable',
    })

    snapshots = [snapshot({ diagnosticStage: 'not_allowlisted' }),
      snapshot({ diagnosticStage: 'not_allowlisted' })]
    assert.deepEqual(await inspectZhilianSendSurfaceDiagnostic(), {
      ready: false, stage: 'diagnostic_unavailable',
    })

    snapshots = [new Error('原始页面异常不得出现在结果')]
    const unavailable = await inspectZhilianSendSurfaceDiagnostic()
    assert.deepEqual(unavailable, { ready: false, stage: 'diagnostic_unavailable' })
    assert.equal(JSON.stringify(unavailable).includes('原始页面异常'), false)
    assert.equal(mutationCalls, 0, '只读诊断不得导航、写 storage 或调用 tabs mutation')
    assert.equal(historyCalls, 0, '整个 debug/capture/post 共享生命周期不得触碰 history')
    assert.ok(timerCalls >= 3, '重复 debug 与生产 capture 必须真正经过 live 双采样窗口')
  } finally {
    Object.assign(globalThis, original)
    if (originalCryptoDescriptor) Object.defineProperty(globalThis, 'crypto', originalCryptoDescriptor)
    else delete globalThis.crypto
  }
})

test('发送 baseline 只用 live Vuex 双采样并按 time 排序、首 ID 去重与 last64 建基线', async () => {
  const original = {
    window: globalThis.window,
    document: globalThis.document,
    location: globalThis.location,
    setTimeout: globalThis.setTimeout,
  }
  const conversationRef = 'conversation-baseline-coverage'
  const target = 'candidate-baseline-coverage'
  const staffId = 'staff-baseline-coverage'
  const sourceKey = (id) => createHash('sha256').update(`source-v1|${id}`).digest('hex')
  const hashContent = (value) => createHash('sha256').update(value).digest('hex')
  const old = {
    idServer: 'server-old-same-text', time: 10, type: 'text',
    from: target, text: '旧同文',
  }
  const other = {
    idServer: 'server-other', time: 5, type: 'text',
    from: target, text: '其他消息',
  }
  const oldTail = [{ direction: 'in', contentHash: hashContent(old.text) }]
  const liveEntry = { timeline: [old] }
  const timelineMap = { [conversationRef]: liveEntry }
  const root = { $store: { state: { im: { timelineMap } } } }
  root.$root = root
  let historyCalls = 0
  let timerCalls = 0
  let betweenSamples = null
  const session = {
    sessionId: conversationRef, peerPartnerId: target,
    sortTime: 1, modifiedTime: 2, lastSentence: '{"text":"旧同文"}',
  }
  const resetSamples = (mutation = null) => {
    betweenSamples = mutation
    timerCalls = 0
  }
  globalThis.location = { href: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}` }
  globalThis.document = {
    scripts: [{ textContent: `__INITIAL_STATE__=${JSON.stringify({
      im: { timelineMap: { [conversationRef]: { timeline: [old] } } },
    })};` }],
    querySelectorAll() { return [] },
  }
  globalThis.window = {
    $nuxt: root,
    $session: { staff: { staffId } },
    imEngine: {
      sessions: [session],
      async getHistoryMsgs() {
        historyCalls += 1
        throw new Error('live-only baseline 不得调用 history')
      },
    },
  }
  globalThis.setTimeout = (callback, ms) => {
    assert.equal(ms, 250)
    timerCalls += 1
    const mutation = betweenSamples
    betweenSamples = null
    if (mutation) mutation()
    queueMicrotask(callback)
    return timerCalls
  }
  try {
    delete timelineMap[conversationRef]
    resetSamples()
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, oldTail),
      { status: 'failed', stage: 'history_first_unavailable' },
      'live exact slot 缺失时不得回退有效 startup',
    )
    assert.equal(timerCalls, 0)

    timelineMap[conversationRef] = liveEntry
    liveEntry.timeline = { malformed: true }
    resetSamples()
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, oldTail),
      { status: 'failed', stage: 'history_first_unavailable' },
      '第一份 live shape 错误必须在等待前闭锁',
    )
    assert.equal(timerCalls, 0)

    liveEntry.timeline = [{ sendMessageId: 'live-optimistic', time: 1, type: 'text', from: staffId, text: '乐观行' }]
    resetSamples()
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, oldTail),
      { status: 'failed', stage: 'history_first_unavailable' },
      '第一份 live 任一行缺 idServer 时必须闭锁',
    )
    assert.equal(timerCalls, 0)

    liveEntry.timeline = [old]
    resetSamples()
    const stable = await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, oldTail)
    assert.equal(stable.status, 'ready')
    assert.deepEqual(stable.serverSourceKeys, [sourceKey(old.idServer)])
    assert.match(stable.sessionVersionToken, /^[0-9a-f]{64}$/)
    assert.match(stable.targetBindingToken, /^[0-9a-f]{64}$/)
    assert.equal(timerCalls, 1, 'ready 必须恰好采两份相隔 250ms 的同步 live 样本')

    liveEntry.timeline = [old]
    resetSamples(() => { liveEntry.timeline = { malformed: true } })
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, oldTail),
      { status: 'failed', stage: 'history_second_unavailable' },
      '第二份 live shape 错误必须收束为 second unavailable',
    )
    assert.equal(timerCalls, 1)

    liveEntry.timeline = [old]
    resetSamples(() => {
      liveEntry.timeline = [{ sendMessageId: 'second-optimistic', time: 11, type: 'text', from: staffId, text: '乐观行' }]
    })
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, oldTail),
      { status: 'failed', stage: 'history_second_unavailable' },
      '第二份 live 任一行缺 idServer 时不得过滤',
    )

    liveEntry.timeline = [other, old]
    resetSamples(() => { liveEntry.timeline = [old] })
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, oldTail),
      { status: 'failed', stage: 'guard_snapshot_uncovered' },
      '第一集合多个 ID 被第二份缺失一个时必须判 guard uncovered',
    )

    liveEntry.timeline = [old]
    resetSamples(() => { liveEntry.timeline = [other, old] })
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, oldTail),
      { status: 'failed', stage: 'history_unstable' },
      '第二份增加额外 ID 时必须判 history unstable',
    )

    const orderedFirst = { idServer: 'server-order-first', time: 1, type: 'text', from: target, text: '顺序一' }
    const orderedSecond = { idServer: 'server-order-second', time: 2, type: 'text', from: target, text: '顺序二' }
    liveEntry.timeline = [orderedFirst, orderedSecond]
    resetSamples(() => {
      orderedFirst.time = 3
      orderedSecond.time = 1
    })
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, [{
        direction: 'in', contentHash: hashContent(orderedSecond.text),
      }]),
      { status: 'failed', stage: 'history_unstable' },
      '两份样本 ID 集相同但时间顺序变化时不得 ready',
    )

    liveEntry.timeline = [old]
    resetSamples()
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, [{
        direction: 'in', contentHash: hashContent('错误尾锚'),
      }]),
      { status: 'failed', stage: 'guard_snapshot_uncovered' },
      '稳定双样本与 expectedTail 不匹配时仍须封闭',
    )
    assert.equal(timerCalls, 1)

    liveEntry.timeline = [old]
    resetSamples(() => {
      globalThis.location.href = 'https://rd6.zhaopin.com/app/im?sessionId=conversation-other'
    })
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, oldTail),
      { status: 'failed', stage: 'route_changed' },
    )
    globalThis.location.href = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`

    liveEntry.timeline = [old]
    resetSamples(() => {
      session.modifiedTime = 3
      session.lastSentence = '{"text":"双采样期间到达的新消息"}'
    })
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, oldTail),
      { status: 'failed', stage: 'session_changed' },
    )
    session.modifiedTime = 2
    session.lastSentence = '{"text":"旧同文"}'

    const duplicateLate = {
      idServer: 'server-duplicate', time: 30, type: 'text', from: target, text: '晚到重复行',
    }
    const duplicateEarly = {
      idServer: 'server-duplicate', time: 10, type: 'text', from: target, text: '排序后首行',
    }
    const finalRow = {
      idServer: 'server-final', time: 20, type: 'text', from: target, text: '排序后尾行',
    }
    const sortedDedupTail = [
      { direction: 'in', contentHash: hashContent(duplicateEarly.text) },
      { direction: 'in', contentHash: hashContent(finalRow.text) },
    ]
    liveEntry.timeline = [duplicateLate, finalRow, duplicateEarly]
    resetSamples()
    const sortedDedup = await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, sortedDedupTail)
    assert.equal(sortedDedup.status, 'ready',
      '必须先按 Number(time) 升序，再对同 idServer 保留排序后首行，并支持多 anchor')
    assert.deepEqual(sortedDedup.serverSourceKeys,
      [sourceKey(duplicateEarly.idServer), sourceKey(finalRow.idServer)],
      'baseline source keys 必须保持排序去重后的时间顺序')

    const textRow = {
      idServer: 'server-tail-text', time: 1, type: 'text', from: target,
      text: '  你\u00a0好 \n 世界  ',
    }
    const cardRow = {
      idServer: 'server-tail-card', time: 2, status: 'success', type: 105, from: staffId,
      content: JSON.stringify({ content: JSON.stringify({
        staffContent: ' 加微信 ', requestId: 'request-tail-card',
      }) }),
    }
    const systemRow = {
      idServer: 'server-tail-system', time: 3, type: 999, from: '',
      content: JSON.stringify({ title: '  系统\u00a0通知  ' }),
    }
    const mixedTail = [
      { direction: 'in', contentHash: hashContent('你 好 世界') },
      { direction: 'out', contentHash: hashContent('card\x1fwechatExchange\x1frequest-tail-card') },
      { direction: 'system', contentHash: hashContent('系统 通知') },
    ]
    liveEntry.timeline = [systemRow, textRow, cardRow]
    resetSamples()
    const normalizedTail = await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, mixedTail)
    assert.equal(normalizedTail.status, 'ready', 'text/card/system 多尾锚必须按 time 排序并统一归一化')

    const longRows = Array.from({ length: 66 }, (_, index) => ({
      idServer: `server-long-${index}`, time: index + 1,
      type: 'text', from: target, text: `长时间线消息${index}`,
    }))
    liveEntry.timeline = [...longRows].reverse()
    resetSamples()
    const longTail = longRows.slice(-2).map((row) => ({
      direction: 'in', contentHash: hashContent(row.text),
    }))
    const windowed = await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, longTail)
    assert.equal(windowed.status, 'ready')
    assert.equal(windowed.serverSourceKeys.length, 64, '65+ 行只允许排序去重后的 last64 进入 baseline')
    assert.deepEqual(new Set(windowed.serverSourceKeys),
      new Set(longRows.slice(2).map((row) => sourceKey(row.idServer))))

    liveEntry.timeline = [
      { sendMessageId: 'invalid-outside-window', time: -1, type: 'text', from: staffId, text: '窗口外乐观行' },
      ...longRows,
    ]
    resetSamples()
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, longTail),
      { status: 'failed', stage: 'history_first_unavailable' },
      '全源必须先验 ID，不能因坏行位于 last64 外而静默裁掉',
    )
    assert.equal(timerCalls, 0)

    const mutableCardRow = {
      idServer: 'server-live-before-wait', time: 1, status: 'success', type: 105, from: staffId,
      content: { content: { staffContent: '原始交换微信卡', requestId: 'request-before-wait' } },
    }
    liveEntry.timeline = [mutableCardRow]
    resetSamples(() => {
      mutableCardRow.idServer = 'server-live-after-wait'
      mutableCardRow.content.content.requestId = 'request-after-wait'
    })
    assert.deepEqual(
      await zhilianTestHooks.mainCaptureSendBaseline(conversationRef, [{
        direction: 'out',
        contentHash: hashContent('card\x1fwechatExchange\x1frequest-before-wait'),
      }]),
      { status: 'failed', stage: 'guard_snapshot_uncovered' },
      '250ms 内 SDK 原地改写 live row/content 不能回写第一份深值快照',
    )

    assert.equal(historyCalls, 0, '所有 baseline 分支都不得触碰 history API')
  } finally {
    Object.assign(globalThis, original)
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
  let baselineCalls = 0
  let mainReadThreadCalls = 0
  let selectCalls = 0
  let updateCalls = 0
  let currentURL = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`
  let observePositive = true
  let sendBaselineResult = {
    status: 'ready', stage: 'ready',
    serverSourceKeys: ['d'.repeat(64)], sessionVersionToken: 'c'.repeat(64),
    targetBindingToken: 'b'.repeat(64),
  }
  globalThis.setTimeout = (callback) => {
    queueMicrotask(callback)
    return 1
  }
  globalThis.chrome = {
    tabs: {
      async query() {
        return [{
          id: 91,
          url: currentURL,
          status: 'complete', active: true,
        }]
      },
      async get() {
        return {
          id: 91,
          url: currentURL,
          status: 'complete', active: true,
        }
      },
      async update() { updateCalls += 1; throw new Error('不得直接拼 URL 导航') },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func, args }) {
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
          const phase = args.at(-1)
          assert.deepEqual(args.slice(7), [
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
      '完整 send preflight 必须恰好调用一次自含双样本的 capture')
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
      'history_second_unavailable',
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
      'history_unstable',
      'guard_snapshot_uncovered',
      'session_changed',
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
      serverSourceKeys: ['raw-platform-id'], sessionVersionToken: 'c'.repeat(64),
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
            serverSourceKeys: ['d'.repeat(64)], sessionVersionToken: 'c'.repeat(64),
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
