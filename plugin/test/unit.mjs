// 无需真脑的 base 单元测试。用 esbuild 加载与生产相同的 TypeScript 源码。
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mock } from 'node:test'
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
  capabilities,
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
  parsedKeywordSections,
  NotReadyReason,
  PageKind,
  Primitive,
  PROTO_VERSION,
  RECONNECT_STABLE_MS,
  Retryable,
  ResultStatus,
  SensorBridge,
  ZHILIAN_UNREAD_BADGE_SELECTOR,
  applyZhilianSourcingFilters,
  acceptZhilianWechatRequest,
  canonicalZhilianTab,
  ensureZhilianIM,
  identifyZhilianCurrentConversation,
  inspectZhilianSendSurfaceDiagnostic,
  openZhilianConversation,
  readZhilianUnreadTotal,
  readZhilianList,
  readZhilianThread,
  readZhilianCurrentCandidate,
  readZhilianSourcingResume,
  readZhilianSourcingTargetResume,
  readZhilianSourcingWindow,
  readZhilianGreetingOutcome,
  readZhilianWechatExchangeOutcome,
  refreshPagesAfterRuntimeReload,
  registerM3Primitives,
  registerM6Primitives,
  sendZhilianGreeting,
  sendZhilianInviteCard,
  sendZhilianMessage,
  sendZhilianWechatInvite,
  normalizeZhilianMessageText,
  lookup,
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

test('关键词分组词库解析:空分组当未就绪继续轮询，optional 计数整键省略', async () => {
  // 读不出分组 ≠ 词库为空。弹层是异步渲染的，此时收工会把"还没画完"当成
  // "这个职位没有关键词可选"，进而干净失败转人工——所以这几种都必须返回
  // null 让调用方接着轮询。
  assert.equal(parsedKeywordSections('不是 JSON'), null)
  assert.equal(parsedKeywordSections('{"sections":[]}'), null)
  assert.equal(parsedKeywordSections('{"sections":[{"title":"","words":[]}]}'), null)
  assert.equal(parsedKeywordSections('{"sections":[{"title":"行业经验"}]}'), null)

  // 不带配额的组件变体读不到 (已选/上限)。契约里 limit/selected 是 optional，
  // 必须整键省略——显式赋 undefined 会被校验判成 null 而整条命令失败。
  const plain = parsedKeywordSections(JSON.stringify({
    sections: [{ title: '财务管理方向', words: ['成本管理', '税务筹划'] }],
  }))
  assert.deepEqual(plain, { sections: [{ title: '财务管理方向', words: ['成本管理', '税务筹划'] }] })
  assert.equal('limit' in plain.sections[0], false)
  assert.equal('selected' in plain.sections[0], false)
  assert.equal('totalQuota' in plain, false)

  // 带配额的变体:标题里的 (已选/上限) 与底部总配额都要如实带回，模型靠它们
  // 才知道每组还能塞几个。
  const limited = parsedKeywordSections(JSON.stringify({
    sections: [{ title: '您还有哪些招聘要求？ (0/3)', limit: 3, selected: 0, words: [] }],
    totalQuota: 11,
  }))
  assert.equal(limited.sections[0].limit, 3)
  assert.equal(limited.sections[0].selected, 0)
  assert.equal(limited.totalQuota, 11)

  // 词条数组里混进非字符串只丢那一项，不掀翻整次读取。
  const dirty = parsedKeywordSections(JSON.stringify({
    sections: [{ title: '证书', words: ['CPA', null, 123, 'ACCA'] }],
  }))
  assert.deepEqual(dirty.sections[0].words, ['CPA', 'ACCA'])
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
  assert.equal(Object.hasOwn(storage.state, 'journal:idem-witness-1'), false,
    'ack 必须同批收割对应 committed journal，否则真实吞吐下 journal 只进不出打满容量')
  const ackRemove = storage.writes.filter((entry) => entry.kind === 'remove').at(-1)
  assert.deepEqual([...ackRemove.keys].sort(),
    ['journal:idem-witness-1', 'outbox:result-envelope-1'],
    'outbox 与 journal 必须在同一次 remove 调用中收割')
  assert.equal(witness.advertisement().outboxPending, 0)
  assert.equal(storage.state['witness:meta'].outboxCount, 0)
  assert.equal(storage.state['witness:meta'].journalCount, 0)
  const afterAckRestart = new WitnessStore(storage, () => now, () => 'unused-after-ack')
  await afterAckRestart.initialize()
  assert.equal(await afterAckRestart.findJournalByIdemKey('idem-witness-1'), null,
    'ack 收割后同 storeId 不残留 journal；脑已终局的命令不会再被 query')
})

test('witness ack 收割:无 journal 的前置失败终局只删 outbox', async () => {
  const now = 1_700_000_000_000
  const storage = memoryWitnessStorage()
  const witness = new WitnessStore(storage, () => now, () => 'witness-ack-prefail')
  await witness.initialize()
  await witness.markAttempting('cmd-live-1', 'idem-live-1')
  await witness.enqueueResult({
    proto: 1, kind: 'result', msgId: 'envelope-prefail-1', session: 's1', ts: now, attempt: 1,
    body: {
      ref: 'cmd-prefail-1', status: 'failed', replayed: false, execMs: 0,
      error: { code: ErrorCode.CtxNotReady, retryable: Retryable.AfterRecovery, sideEffect: 'none' },
    },
  })
  await witness.acknowledgeResult('envelope-prefail-1')
  assert.equal(Object.hasOwn(storage.state, 'outbox:envelope-prefail-1'), false)
  assert.equal(storage.state['witness:meta'].outboxCount, 0)
  assert.equal(storage.state['witness:meta'].journalCount, 1,
    'attempting 写点前失败的终局没有 journal，ack 不得误删无关 journal')
  assert.equal((await witness.findJournalByIdemKey('idem-live-1')).ref, 'cmd-live-1')
})

test('witness ack 收割 remove 后 meta 更新失败即熔断，重启按 required count 判 corrupt', async () => {
  const now = 1_700_000_000_000
  let failMetaWrite = false
  const storage = memoryWitnessStorage({}, {
    beforeSet: async (items) => {
      if (failMetaWrite && Object.hasOwn(items, 'witness:meta')) throw new Error('storage quota')
    },
  })
  const witness = new WitnessStore(storage, () => now, () => 'witness-ack-crashseam')
  await witness.initialize()
  await witness.markAttempting('cmd-seam-1', 'idem-seam-1')
  const body = {
    ref: 'cmd-seam-1', status: 'ok', replayed: false, execMs: 5,
    data: { conversationRef: 'c', contentHash: 'a'.repeat(64), observedAt: now },
    evidence: [{ type: 'outboundMessageObserved' }],
  }
  await witness.commitAndEnqueue('idem-seam-1', {
    proto: 1, kind: 'result', msgId: 'envelope-seam-1', session: 's1', ts: now, attempt: 1, body,
  })
  failMetaWrite = true
  await assert.rejects(
    () => witness.acknowledgeResult('envelope-seam-1'),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
    'remove 已成功而 meta 更新失败时不得继续沿用旧计数运行')
  failMetaWrite = false
  await assert.rejects(
    () => witness.acknowledgeResult('envelope-seam-1'),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
    '熔断后的实例必须保持 corrupt，不得复活')
  const restarted = new WitnessStore(storage, () => now, () => 'witness-ack-crashseam-2')
  await assert.rejects(
    () => restarted.initialize(),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
    '崩溃缝留下的计数与 key 不一致必须在下次读取判 corrupt，不能伪造同世代 unknown')
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

test('最大执行预算准点或晚触发都保留 EXEC_TIMEOUT_HAND，诊断时长封顶', async () => {
  const realDateNow = Date.now
  for (const observedElapsed of [
    DEFAULTS.execBudgetDefaultMs.capMs,
    DEFAULTS.execBudgetDefaultMs.capMs + 11,
  ]) {
    let now = 1_700_000_000_000
    let releaseHandler = () => {}
    Date.now = () => now
    mock.timers.enable({ apis: ['setTimeout'] })
    try {
      const handlerGate = new Promise((resolve) => { releaseHandler = resolve })
      register({
        name: Primitive.DebugPing,
        class: 'readonly',
        async handler() {
          await handlerGate
          return pingOk()
        },
      })
      const out = recorder()
      const dispatcher = new Dispatcher(out.send)
      const ref = `budget-cap-${observedElapsed}`
      await dispatcher.handleCmd(ref, 's', 's', command(Primitive.DebugPing, {}, {
        deadline: now + DEFAULTS.execBudgetDefaultMs.capMs + 60_000,
        execBudgetMs: DEFAULTS.execBudgetDefaultMs.capMs,
      }))

      now += observedElapsed
      mock.timers.tick(DEFAULTS.execBudgetDefaultMs.capMs)
      for (let round = 0; round < 20 && results(out.frames, ref).length === 0; round++) {
        await Promise.resolve()
      }
      const terminal = results(out.frames, ref)[0]?.body
      assert.equal(terminal?.status, ResultStatus.Failed)
      assert.equal(terminal?.error.code, ErrorCode.ExecTimeoutHand)
      assert.equal(terminal?.error.retryable, Retryable.Yes)
      assert.equal(terminal?.error.sideEffect, 'none')
      assert.equal(terminal?.execMs, DEFAULTS.execBudgetDefaultMs.capMs)

      releaseHandler()
      for (let round = 0; round < 20 && dispatcher.snapshot().inFlight !== null; round++) {
        await Promise.resolve()
      }
      assert.equal(results(out.frames, ref).length, 1)
    } finally {
      releaseHandler()
      mock.timers.reset()
      Date.now = realDateNow
    }
  }
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

test('带租约命令在执行与排队期间按 ref 发送活性心跳，终局后停止', async () => {
  let releaseFirst = () => {}
  mock.timers.enable({ apis: ['setTimeout', 'setInterval', 'Date'] })
  try {
    const firstGate = new Promise((resolve) => { releaseFirst = resolve })
    const starts = []
    register({
      name: Primitive.ChatReadList,
      class: 'intrusive',
      async handler(args) {
        starts.push(args.filter)
        if (args.filter === 'all') await firstGate
        return { status: 'ok', data: { complete: true, sessions: [] } }
      },
    })
    const out = recorder()
    const dispatcher = new Dispatcher(out.send)
    const leasedCommand = (filter) => ({
      name: Primitive.ChatReadList,
      ver: 1,
      context: {
        platform: 'zhilian',
        accountRef: 'account-lease-pulse',
        expectedPrincipalFingerprint: 'principal-lease-pulse',
      },
      args: { filter, move: 'reset' },
      deadline: Date.now() + 100_000,
      execBudgetMs: 60_000,
      leaseMs: 30_000,
    })

    await dispatcher.handleCmd('lease-running', 's', 's', leasedCommand('all'))
    await dispatcher.handleCmd('lease-queued', 's', 's', leasedCommand('unread'))
    await dispatcher.handleCmd('lease-canceled', 's', 's', leasedCommand('unread'))
    assert.deepEqual(starts, ['all'])
    assert.deepEqual(dispatcher.snapshot(), { queueDepth: 2, inFlight: 'lease-running' })

    mock.timers.tick(10_000)
    await Promise.resolve()
    const runningPulse = out.frames.find((frame) =>
      frame.kind === Kind.Progress && frame.body.ref === 'lease-running')
    const queuedPulse = out.frames.find((frame) =>
      frame.kind === Kind.Progress && frame.body.ref === 'lease-queued')
    const canceledPulse = out.frames.find((frame) =>
      frame.kind === Kind.Progress && frame.body.ref === 'lease-canceled')
    assert.deepEqual(runningPulse?.body, { ref: 'lease-running', stage: '命令执行中' })
    assert.deepEqual(queuedPulse?.body, { ref: 'lease-queued', stage: '命令排队中' })
    assert.deepEqual(canceledPulse?.body, { ref: 'lease-canceled', stage: '命令排队中' })

    dispatcher.handleCancel(
      'cancel-lease-canceled',
      's',
      's',
      { ref: 'lease-canceled', reason: 'operator' },
    )
    for (let round = 0; round < 20 && results(out.frames, 'lease-canceled').length === 0; round++) {
      await Promise.resolve()
    }
    assert.equal(results(out.frames, 'lease-canceled')[0]?.body.status, ResultStatus.Canceled)
    assert.deepEqual(dispatcher.snapshot(), { queueDepth: 1, inFlight: 'lease-running' })

    releaseFirst()
    for (let round = 0; round < 20 && results(out.frames, 'lease-queued').length === 0; round++) {
      await Promise.resolve()
    }
    assert.deepEqual(starts, ['all', 'unread'])
    assert.equal(results(out.frames, 'lease-running')[0]?.body.status, ResultStatus.Ok)
    assert.equal(results(out.frames, 'lease-queued')[0]?.body.status, ResultStatus.Ok)

    const pulseCount = out.frames.filter((frame) => frame.kind === Kind.Progress).length
    mock.timers.tick(30_000)
    await Promise.resolve()
    assert.equal(
      out.frames.filter((frame) => frame.kind === Kind.Progress).length,
      pulseCount,
      '终局后不得残留租约心跳',
    )
  } finally {
    releaseFirst()
    mock.timers.reset()
  }
})

test('租约心跳不延长 execBudget，预算终局后立即停止', async () => {
  let releaseHandler = () => {}
  mock.timers.enable({ apis: ['setTimeout', 'setInterval', 'Date'] })
  try {
    const handlerGate = new Promise((resolve) => { releaseHandler = resolve })
    register({
      name: Primitive.ChatReadList,
      class: 'intrusive',
      async handler() {
        await handlerGate
        return { status: 'ok', data: { complete: true, sessions: [] } }
      },
    })
    const out = recorder()
    const dispatcher = new Dispatcher(out.send)
    await dispatcher.handleCmd('lease-budget', 's', 's', {
      name: Primitive.ChatReadList,
      ver: 1,
      context: {
        platform: 'zhilian',
        accountRef: 'account-lease-budget',
        expectedPrincipalFingerprint: 'principal-lease-budget',
      },
      args: { filter: 'all', move: 'reset' },
      deadline: Date.now() + 100_000,
      execBudgetMs: 25_000,
      leaseMs: 9_000,
    })

    mock.timers.tick(24_000)
    await Promise.resolve()
    assert.equal(results(out.frames, 'lease-budget').length, 0)
    assert.ok(
      out.frames.filter((frame) =>
        frame.kind === Kind.Progress && frame.body.ref === 'lease-budget').length >= 2,
      '预算内长执行必须持续发送租约心跳',
    )

    mock.timers.tick(1_000)
    for (let round = 0; round < 20 && results(out.frames, 'lease-budget').length === 0; round++) {
      await Promise.resolve()
    }
    const terminal = results(out.frames, 'lease-budget')[0]?.body
    assert.equal(terminal?.status, ResultStatus.Failed)
    assert.equal(terminal?.error.code, ErrorCode.ExecTimeoutHand)

    const pulseCount = out.frames.filter((frame) =>
      frame.kind === Kind.Progress && frame.body.ref === 'lease-budget').length
    mock.timers.tick(30_000)
    await Promise.resolve()
    assert.equal(
      out.frames.filter((frame) =>
        frame.kind === Kind.Progress && frame.body.ref === 'lease-budget').length,
      pulseCount,
      'execBudget 终局后不得继续续租',
    )

    releaseHandler()
    for (let round = 0; round < 20 && dispatcher.snapshot().inFlight !== null; round++) {
      await Promise.resolve()
    }
    assert.equal(dispatcher.snapshot().inFlight, null)
    assert.equal(results(out.frames, 'lease-budget').length, 1, '晚到 handler 结果不得覆盖预算终局')
  } finally {
    releaseHandler()
    mock.timers.reset()
  }
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
    args: { filter: 'all', move: 'reset' },
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
    scripts: [],
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

    fixture.state.buttons = [{
      textContent: '继续沟通',
      disabled: false,
      getClientRects() { return [{}] },
    }]
    const established = zhilianTestHooks.mainReadCurrentCandidate()
    assert.equal(established.status, 'ready')
    assert.equal(established.data.contactState, 'established',
      '同一详情的唯一“继续沟通”是可见关系已建立正证')
    assert.equal(fixture.state.clicks, 0, '关系正证读取不得调用页面动作')
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

function installM5ResumeFixture(options = {}) {
  const original = {
    document: globalThis.document,
    location: globalThis.location,
    window: globalThis.window,
    getComputedStyle: globalThis.getComputedStyle,
    setTimeout: globalThis.setTimeout,
    dateNow: Date.now,
    random: Math.random,
  }
  let now = 1_700_000_000_000
  const timerDelays = []
  let timerHook = () => {}
  globalThis.setTimeout = (callback, delay = 0, ...args) => {
    const delayMs = Math.max(0, Number(delay) || 0)
    timerDelays.push(delayMs)
    now += delayMs
    timerHook(delayMs)
    queueMicrotask(() => callback(...args))
    return 1
  }
  Date.now = () => now
  Math.random = () => 0.5
  const conversationRef = 'fixture-conversation-m5'
  const platformUserRef = 'fixture-user-m5'
  const node = (text = '') => ({
    textContent: text,
    innerText: text,
    getClientRects: () => [{}],
    query: new Map(),
    querySelectorAll(selector) { return this.query.get(selector) ?? [] },
    closest(selector) { return selector === '.im-session-detail' ? this.detail ?? null : null },
    click() {},
  })
  const detail = node()
  detail.detail = detail
  const entry = node('查看详情')
  entry.detail = detail
  const modal = node()
  const root = node()
  const state = {
    modals: [],
    clicks: 0,
    closeClicks: 0,
    staleCloseClicks: 0,
    commandStartedAt: now,
    openedAt: 0,
    closedAt: 0,
    timerDelays,
    get now() { return now },
  }
  entry.click = () => {
    state.clicks += 1
    state.openedAt = Date.now()
    state.modals = [modal]
  }
  detail.query.set('.hover-resume-footer__button, button, a, [role="button"]', [entry])
  const close = node('关闭')
  close.click = () => {
    state.closeClicks += 1
    state.closedAt = Date.now()
    if (options.closeStuck !== true) state.modals = []
  }
  modal.query.set('.new-shortcut-resume__close', options.closeUnavailable === true ? [] : [close])
  const staleModal = node()
  const staleClose = node('关闭其他弹窗')
  staleClose.click = () => {
    state.staleCloseClicks += 1
    state.modals = []
  }
  staleModal.query.set('.new-shortcut-resume__close', [staleClose])
  if (options.staleModalBeforeOpen === true) {
    timerHook = (delayMs) => {
      if (delayMs >= 1_000 && state.modals.length === 0) {
        state.modals = [staleModal]
        timerHook = () => {}
      }
    }
  } else if (options.replaceModalDuringHold === true) {
    timerHook = (delayMs) => {
      if (delayMs >= 2_000 && state.modals.length === 1 && state.modals[0] === modal) {
        state.modals = [staleModal]
        timerHook = () => {}
      }
    }
  }
  modal.query.set('.resume-detail', [root])
  root.query.set('.resume-basic-new__name', [node('合成候选人')])
  root.query.set('.resume-basic-new__meta-item', [
    node('30岁（1996年）'), node('8年工作经验'), node('本科'), node('在职-看看机会'), node('现居：合成城市'),
  ])
  const purpose = node()
  purpose.query.set('.new-resume-purposes__item-city', [node('合成城市')])
  purpose.query.set('.new-resume-purposes__item-type', [node('合成职位')])
  purpose.query.set('.new-resume-purposes__item-salary', [node('合成薪资')])
  root.query.set('.new-resume-purposes__item', [purpose])
  const work = node()
  work.query.set('.new-work-experiences__item', [node('合成公司\n合成职责')])
  root.query.set('.new-work-experiences', [work])
  const education = node()
  education.query.set('.new-education-experiences__item', [node('合成学校\n本科')])
  root.query.set('.new-education-experiences', [education])
  root.query.set('.resume-section-self-evaluation, .new-self-evaluation, .new-resume-self-evaluation', [])
  root.query.set('h1, h2, h3, h4, h5, b, .resume-section-new__title', [])
  globalThis.location = { href: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}` }
  globalThis.window = { imEngine: { sessions: [{ sessionId: conversationRef, peerPartnerId: platformUserRef }] } }
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.document = {
    querySelectorAll(selector) {
      if (selector === '.im-session-detail') return [detail]
      if (selector === '.new-shortcut-resume__modal') return state.modals
      return []
    },
  }
  return {
    conversationRef,
    platformUserRef,
    root,
    state,
    modal,
    useInitialBinding() {
      globalThis.window = {}
      globalThis.document.scripts = [{
        textContent: `__INITIAL_STATE__=${JSON.stringify({
          im: { sessions: [{ sessionId: conversationRef, peerPartnerId: platformUserRef }] },
        })};`,
      }]
    },
    useExpandedBasicAndSelf() {
      root.query.set('.resume-basic-new__meta-item', [
        node('30岁（1996年）'), node('8年'), node('本科'), node('离职-正在找工作'),
        node('现居：合成城市'), node('户口：合成城市'), node('合成附加信息'),
      ])
      root.query.set('.resume-section-self-evaluation, .new-self-evaluation, .new-resume-self-evaluation', [
        node('合成自评第一行\n合成自评第二行'),
      ])
    },
    restore() {
      globalThis.document = original.document
      globalThis.location = original.location
      globalThis.window = original.window
      globalThis.getComputedStyle = original.getComputedStyle
      globalThis.setTimeout = original.setTimeout
      Date.now = original.dateNow
      Math.random = original.random
    },
  }
}

test('candidate.readResume MAIN 单次打开、停留后关闭并返回完整五分区', async () => {
  for (const source of ['runtime', 'initial']) {
    const fixture = installM5ResumeFixture()
    try {
      if (source === 'initial') {
        fixture.useInitialBinding()
        fixture.useExpandedBasicAndSelf()
      }
      const args = [fixture.conversationRef, fixture.platformUserRef]
      const result = await zhilianTestHooks.mainReadCurrentResume(...args)
      assert.equal(result.status, 'ready', source)
      assert.equal(fixture.state.clicks, 1, source)
      assert.equal(fixture.state.closeClicks, 1, source)
      assert.equal(fixture.state.modals.length, 0, source)
      const openDelayMs = fixture.state.openedAt - fixture.state.commandStartedAt
      assert.ok(openDelayMs >= 1_000 && openDelayMs <= 1_500,
        `${source}: 打开前等待必须在 1000-1500ms`)
      const closeDelayMs = fixture.state.closedAt - fixture.state.openedAt
      assert.ok(closeDelayMs >= 2_000 && closeDelayMs <= 2_500,
        `${source}: 打开后关闭等待必须在 2000-2500ms`)
      assert.equal(result.data.conversationRef, fixture.conversationRef)
      assert.equal(result.data.platformUserRef, fixture.platformUserRef)
      assert.deepEqual(result.data.expectations.map(({ label }) => label),
        ['期望地点', '期望职位', '期望薪资'])
      if (source === 'initial') {
        assert.deepEqual(result.data.basic.map(({ label }) => label), [
          '姓名', '年龄', '工作经验', '最高学历', '求职状态', '现居地', '户口地', '其他信息1',
        ])
        assert.equal(result.data.selfEvaluation, '合成自评第一行\n合成自评第二行')
      } else {
        assert.deepEqual(result.data.basic.map(({ label }) => label),
          ['姓名', '年龄', '工作经验', '最高学历', '求职状态', '现居地'])
        assert.equal(result.data.selfEvaluation, '', '结构和标题同时不存在才表示明确空自评')
      }
      assert.ok(result.data.education && result.data.workExperiences)
    } finally {
      fixture.restore()
    }
  }
})

test('candidate.readResume MAIN 关闭后仍可见时响亮失败', async () => {
  const fixture = installM5ResumeFixture({ closeStuck: true })
  try {
    const result = await zhilianTestHooks.mainReadCurrentResume(
      fixture.conversationRef, fixture.platformUserRef)
    assert.deepEqual(result, { status: 'failed', reason: 'close_unavailable' })
    assert.equal(fixture.state.clicks, 1)
    assert.equal(fixture.state.closeClicks, 1)
    assert.equal(fixture.state.modals.length, 1)
    assert.equal(fixture.state.now - fixture.state.closedAt, 10_000,
      '关闭后最多条件等待 10 秒确认消失')
  } finally {
    fixture.restore()
  }
})

test('candidate.readResume MAIN 不接管停留期间替换进来的弹窗', async () => {
  const fixture = installM5ResumeFixture({ replaceModalDuringHold: true })
  try {
    const result = await zhilianTestHooks.mainReadCurrentResume(
      fixture.conversationRef, fixture.platformUserRef)
    assert.deepEqual(result, { status: 'failed', reason: 'stale_modal' })
    assert.equal(fixture.state.clicks, 1)
    assert.equal(fixture.state.closeClicks, 0)
    assert.equal(fixture.state.staleCloseClicks, 0)
    assert.equal(fixture.state.modals.length, 1, '替换进来的弹窗必须保留给真人处理')
  } finally {
    fixture.restore()
  }
})

test('candidate.readResume MAIN 不接管打开前等待期间出现的旧弹窗', async () => {
  const fixture = installM5ResumeFixture({ staleModalBeforeOpen: true })
  try {
    const result = await zhilianTestHooks.mainReadCurrentResume(
      fixture.conversationRef, fixture.platformUserRef)
    assert.deepEqual(result, { status: 'failed', reason: 'stale_modal' })
    assert.equal(fixture.state.clicks, 0)
    assert.equal(fixture.state.closeClicks, 0)
    assert.equal(fixture.state.staleCloseClicks, 0)
    assert.equal(fixture.state.modals.length, 1, '等待期间出现的旧弹窗必须保留给真人处理')
  } finally {
    fixture.restore()
  }
})

test('candidate.readResume MAIN 对旧弹窗、换绑与缺区整体失败并安全清理', async () => {
  for (const [name, mutate, reason] of [
    ['旧弹窗', (fixture) => { fixture.state.modals = [fixture.modal] }, 'stale_modal'],
    ['目标换绑', (fixture) => { globalThis.window.imEngine.sessions[0].peerPartnerId = 'other-user' }, 'target_changed'],
    ['教育缺区', (fixture) => { fixture.root.query.set('.new-education-experiences', []) }, 'education_unresolved'],
  ]) {
    const fixture = installM5ResumeFixture()
    try {
      mutate(fixture)
      const result = await zhilianTestHooks.mainReadCurrentResume(
        fixture.conversationRef, fixture.platformUserRef)
      assert.deepEqual(result, { status: 'failed', reason }, name)
      assert.equal(fixture.state.clicks, name === '教育缺区' ? 1 : 0)
      assert.equal(fixture.state.closeClicks, name === '教育缺区' ? 1 : 0)
      assert.equal(fixture.state.modals.length, name === '旧弹窗' ? 1 : 0,
        name === '旧弹窗' ? '不得接管调用前已经存在的弹窗' : '自有弹窗必须尽力关闭')
      assert.equal(result.data, undefined)
    } finally {
      fixture.restore()
    }
  }
})

function installM6SourcingFixture(options = {}) {
  const original = {
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
    setTimeout: globalThis.setTimeout,
  }
  if (options.realTimers !== true) {
    globalThis.setTimeout = (callback, _delay, ...args) => {
      queueMicrotask(() => callback(...args))
      return 1
    }
  }
  const refs = {
    job: 'fixture-job-sourcing',
    firstUser: 'fixture-user-sourcing-1',
    secondUser: 'fixture-user-sourcing-2',
    firstResume: 'fixture-resume-sourcing-1',
    secondResume: 'fixture-resume-sourcing-2',
  }
  const node = (text = '') => ({
    textContent: text,
    innerText: text,
    disabled: false,
    getClientRects: () => [{}],
    query: new Map(),
    querySelectorAll(selector) { return this.query.get(selector) ?? [] },
    click() {},
  })
  const root = { _route: { query: { jobNumber: refs.job } } }
  const store = { state: { talent: { activeJob: { jobNumber: refs.job, jobTitle: '合成采集职位' } } } }
  const modal = node()
  const detailReadyAfterEvaluations = Number.isInteger(options.detailReadyAfterEvaluations) &&
    options.detailReadyAfterEvaluations > 0 ? options.detailReadyAfterEvaluations : 1
  let detailEvaluations = 0
  const modalQuerySelectorAll = modal.querySelectorAll.bind(modal)
  modal.querySelectorAll = (selector) => {
    if (selector === '.new-shortcut-resume__close') return modalQuerySelectorAll(selector)
    if (selector === '.resume-basic-new__name') detailEvaluations += 1
    if (detailEvaluations < detailReadyAfterEvaluations) return []
    return modalQuerySelectorAll(selector)
  }
  const close = node('关闭')
  close.click = () => {
    state.closedAt = Date.now()
    state.modals = []
    globalThis.location.href = `https://rd6.zhaopin.com/app/recommend?jobNumber=${refs.job}`
  }
  modal.query.set('.new-shortcut-resume__close', options.closeUnavailable === true ? [] : [close])
  const name = node('合成采集候选人一')
  modal.query.set('.resume-basic-new__name', [name])
  modal.query.set('.resume-basic-new__meta-item', [
    node('28岁'), node('5年工作经验'), node('本科'), node('在职-看看机会'), node('现居：合成城市'),
  ])
  modal.query.set('.resume-section-purposes', [node('求职期望\n合成城市 合成岗位')])
  modal.query.set('.new-work-experiences', [node('工作经历\n合成公司\n合成职责')])
  modal.query.set('.new-education-experiences', [node('教育经历\n合成学校\n本科')])
  modal.query.set(
    '.resume-section-self-evaluation, .new-self-evaluation, .new-resume-self-evaluation',
    [],
  )
  const state = {
    modals: [], clicks: [], routeResumeOverride: options.routeResumeOverride ?? null,
    openedAt: 0, closedAt: 0,
    get detailEvaluations() { return detailEvaluations },
  }
  const makeCandidate = (platformUserRef, resumeNumber, displayName, established = false) => {
    const item = node(established ? `${displayName}\n同事聊过` : `${displayName}\n打招呼`)
    const owner = {
      _props: { source: { userMasterId: platformUserRef, resumeNumber } },
      $root: root,
      $store: store,
    }
    item.__vue__ = owner
    const button = node('打招呼')
    const entry = node(displayName)
    entry.click = () => {
      state.clicks.push(platformUserRef)
      state.openedAt = Date.now()
      name.textContent = displayName
      name.innerText = displayName
      state.modals = [modal]
      const boundResume = state.routeResumeOverride ?? resumeNumber
      globalThis.location.href = `https://rd6.zhaopin.com/app/recommend?jobNumber=${refs.job}` +
        `&resumeNumber=${boundResume}`
    }
    item.query.set('button[type="button"]', [button])
    item.query.set('.resume-item__content', [entry])
    return { item, owner, button, entry }
  }
  const first = makeCandidate(refs.firstUser, refs.firstResume, '合成采集候选人一',
    options.established === true)
  const second = makeCandidate(refs.secondUser, refs.secondResume, '合成采集候选人二')
  const items = [first.item, second.item]
  globalThis.location = {
    href: `https://rd6.zhaopin.com/app/recommend?jobNumber=${refs.job}`,
  }
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.document = {
    querySelectorAll(selector) {
      if (selector === '.recommend-list__left div[role="listitem"]') return items
      if (selector === '.new-shortcut-resume__modal') return state.modals
      if (selector === '.job-pane__item--active .job-pane__item-job-title') {
        return [node('合成采集职位')]
      }
      return []
    },
  }
  return {
    refs,
    first,
    second,
    modal,
    state,
    removeIdentity() { delete first.owner._props.source.userMasterId },
    removeSection(selector) { modal.query.set(selector, []) },
    restore() {
      globalThis.document = original.document
      globalThis.location = original.location
      globalThis.getComputedStyle = original.getComputedStyle
      globalThis.setTimeout = original.setTimeout
    },
  }
}

function installM6SourcingWindowFixture(options = {}) {
  const original = {
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
    setTimeout: globalThis.setTimeout,
    dateNow: Date.now,
  }
  if (options.virtualTime === true) {
    let virtualNow = 1_780_000_000_000
    Date.now = () => virtualNow
    globalThis.setTimeout = (callback, delay = 0, ...args) => {
      virtualNow += Math.max(0, Number(delay) || 0)
      queueMicrotask(() => callback(...args))
      return 1
    }
  }
  const staticDomCount = Number.isInteger(options.staticDomCount) && options.staticDomCount > 0
    ? options.staticDomCount
    : 4
  const staticRefs = Array.from(
    { length: staticDomCount },
    (_, index) => `fixture-window-user-${index + 1}`,
  )
  const refs = {
    job: 'fixture-job-window',
    firstWindow: staticRefs.slice(0, 2),
    secondWindow: staticRefs.slice(2, 4),
    all: staticRefs,
  }
  const node = (text = '') => ({
    textContent: text,
    innerText: text,
    parentElement: null,
    getBoundingClientRect: () => ({ top: 10, bottom: 90, height: 80 }),
    getClientRects: () => [{}],
    querySelectorAll() { return [] },
  })
  const body = node()
  const root = { _route: { query: { jobNumber: refs.job } } }
  const store = {
    state: { talent: { activeJob: { jobNumber: refs.job, jobTitle: '合成窗口职位' } } },
  }
  const state = {
    index: options.startAt === 'first' ? 0 : 1,
    visibleTitles: ['合成窗口职位'],
    transientReadsRemaining: 0,
    windowReads: 0,
  }
  const scroller = {
    ...node(),
    parentElement: body,
    scrollTop: state.index * 100,
    clientHeight: 100,
    scrollHeight: options.staticDom === true ? Math.max(staticDomCount * 50, 100) : 200,
    scrollTo({ top }) {
      this.scrollTop = Number(top)
      state.index = Math.max(Math.floor(this.scrollTop / 100), 0)
      state.transientReadsRemaining = Number.isInteger(options.transientAfterScrollReads)
        ? Math.max(options.transientAfterScrollReads, 0)
        : 0
    },
    dispatchEvent() {},
  }
  const makeItem = (platformUserRef, index) => {
    const item = node(`绝不返回姓名${index}`)
    item.parentElement = options.documentRoot === true ? body : scroller
    if (options.staticDom === true) {
      item.getBoundingClientRect = () => {
        const top = (index - 1) * 50 - scroller.scrollTop
        return { top, bottom: top + 50, height: 50 }
      }
      item.getClientRects = () => [item.getBoundingClientRect()]
    }
    item.__vue__ = {
      _props: {
        source: {
          userMasterId: platformUserRef,
          resumeNumber: `绝不返回-resumeNumber-${index}`,
        },
      },
      $root: root,
      $store: store,
    }
    return item
  }
  const windows = options.staticDom === true
    ? [staticRefs.map((platformUserRef, index) => makeItem(platformUserRef, index + 1))]
    : [
        refs.firstWindow.map((platformUserRef, index) => makeItem(platformUserRef, index + 1)),
        refs.secondWindow.map((platformUserRef, index) => makeItem(platformUserRef, index + 3)),
      ]
  scroller.getBoundingClientRect = () => ({ top: 0, bottom: 100, height: 100 })
  globalThis.location = {
    href: `https://rd6.zhaopin.com/app/recommend?jobNumber=${refs.job}`,
  }
  globalThis.getComputedStyle = (element) => ({
    display: 'block',
    visibility: 'visible',
    overflow: element === scroller && options.documentRoot !== true ? 'auto' : 'visible',
    overflowY: element === scroller && options.documentRoot !== true ? 'auto' : 'visible',
  })
  globalThis.document = {
    body,
    scrollingElement: options.documentRoot === true ? scroller : null,
    querySelectorAll(selector) {
      if (selector === '.recommend-list__left div[role="listitem"]') {
        state.windowReads += 1
        if (state.transientReadsRemaining > 0) {
          state.transientReadsRemaining -= 1
          return []
        }
        if (options.currentSwitchAfterReads === true) {
          return state.windowReads <= 2 ? windows[0] : windows[1]
        }
        if (options.unstableCurrent === true) {
          return windows[state.windowReads % 2]
        }
        return options.staticDom === true ? windows.flat() : windows[state.index]
      }
      if (selector === '.job-pane__item--active .job-pane__item-job-title') {
        return state.visibleTitles.map((title) => node(title))
      }
      return []
    },
  }
  return {
    refs,
    state,
    scroller,
    windows,
    store,
    root,
    removeFirstIdentity() { delete windows[state.index][0].__vue__._props.source.userMasterId },
    duplicateIdentity() {
      windows[state.index][1].__vue__._props.source.userMasterId =
        windows[state.index][0].__vue__._props.source.userMasterId
    },
    restore() {
      globalThis.document = original.document
      globalThis.location = original.location
      globalThis.getComputedStyle = original.getComputedStyle
      globalThis.setTimeout = original.setTimeout
      Date.now = original.dateNow
    },
  }
}

function installM6PositionSelectorFixture(options = {}) {
  const original = {
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
  }
  const refs = {
    oldJob: 'fixture-old-job',
    targetJob: 'fixture-target-job',
    targetTitle: '目标 职位',
  }
  const state = {
    drawerOpen: false,
    currentJob: options.alreadySelected === true ? refs.targetJob : refs.oldJob,
    currentTitle: options.alreadySelected === true ? refs.targetTitle : '旧职位',
    interactions: [],
    itemReads: 0,
  }
  const classList = (...initial) => {
    const values = new Set(initial)
    return {
      contains(value) { return values.has(value) },
      add(value) { values.add(value) },
      remove(value) { values.delete(value) },
    }
  }
  const node = (text = '') => ({
    textContent: text,
    innerText: text,
    classList: classList(),
    getClientRects: () => [{}],
    querySelectorAll() { return [] },
    querySelector(selector) { return this.querySelectorAll(selector)[0] ?? null },
    cloneNode() { return node(this.textContent) },
    closest() { return null },
    click() {},
    scrollIntoView() {},
  })
  const titleNode = (title, withStatusTag = false) => {
    const base = node(withStatusTag ? `未上线协作${title}` : title)
    const decorations = withStatusTag
      ? [node('未上线'), node('协作'), node('')]
      : []
    base.cloneNode = withStatusTag ? undefined : base.cloneNode
    base.querySelectorAll = (selector) =>
      selector === '.job-tag-withdrawn, .job-tag-coordination, .icon-eye'
        ? decorations
        : []
    return base
  }
  const makeJobItem = (title, jobRef, active = false, withStatusTag = false) => {
    const item = node(title)
    item.classList = classList(...(active ? ['is-active'] : []))
    const titleElement = titleNode(title, withStatusTag)
    item.querySelectorAll = (selector) =>
      selector === '.job-side-selector__title' ? [titleElement] : []
    item.scrollIntoView = () => {
      state.interactions.push(['scroll-target', Date.now()])
    }
    item.click = () => {
      state.interactions.push(['click-target', Date.now()])
      for (const candidate of jobItems) candidate.classList.remove('is-active')
      item.classList.add('is-active')
      state.currentJob = jobRef
      state.currentTitle = title
      globalThis.location.href =
        `https://rd6.zhaopin.com/app/recommend?jobNumber=${encodeURIComponent(jobRef)}`
    }
    return item
  }
  const oldItem = makeJobItem(
    '旧职位',
    refs.oldJob,
    options.alreadySelected !== true,
  )
  const targetItem = makeJobItem(
    refs.targetTitle,
    refs.targetJob,
    options.alreadySelected === true,
    true,
  )
  const duplicateTarget = makeJobItem(refs.targetTitle, 'fixture-duplicate-job')
  const jobItems = options.omitTarget === true
    ? [oldItem]
    : options.duplicateTarget === true
      ? [oldItem, targetItem, duplicateTarget]
      : [oldItem, targetItem]
  const closeButton = node('关闭')
  closeButton.click = () => {
    state.interactions.push(['close-drawer', Date.now()])
    state.drawerOpen = false
  }
  const drawer = node()
  drawer.closest = (selector) =>
    selector === '.km-modal__wrapper--right.job-side-selector' ? drawer : null
  drawer.querySelectorAll = (selector) => {
    if (selector === '.job-side-selector__item') {
      state.itemReads += 1
      if (state.itemReads <= (options.delayedItemReads ?? 0)) return []
      return jobItems
    }
    if (selector === '.km-modal__close-btn') return [closeButton]
    return []
  }
  const trigger = node('选择职位')
  trigger.click = () => {
    state.interactions.push(['open-drawer', Date.now()])
    state.drawerOpen = true
  }
  globalThis.location = {
    href: `https://rd6.zhaopin.com/app/recommend?jobNumber=${state.currentJob}`,
  }
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.document = {
    querySelectorAll(selector) {
      if (selector === 'a[zp-stat-id="talent_more_jobs"]') return [trigger]
      if (selector === '.job-side-selector') return state.drawerOpen ? [drawer] : []
      if (selector === '.job-pane__item--active .job-pane__item-job-title') {
        return [node(state.currentTitle)]
      }
      return []
    },
  }
  return {
    refs,
    state,
    targetItem,
    restore() {
      globalThis.document = original.document
      globalThis.location = original.location
      globalThis.getComputedStyle = original.getComputedStyle
    },
  }
}

test('candidate.selectSourcingPosition MAIN 精确唯一匹配并遵守交互间隔后确认稳定职位', async () => {
  const fixture = installM6PositionSelectorFixture({ delayedItemReads: 3 })
  try {
    const result = await zhilianTestHooks.mainSelectSourcingPosition('  目标\u00a0 职位  ')
    assert.equal(result.status, 'ready')
    assert.equal(result.data.positionRef, fixture.refs.targetJob)
    assert.equal(result.data.positionTitle, fixture.refs.targetTitle)
    assert.deepEqual(fixture.state.interactions.map(([name]) => name), [
      'open-drawer', 'scroll-target', 'click-target', 'close-drawer',
    ])
    for (let index = 1; index < fixture.state.interactions.length; index += 1) {
      const elapsed = fixture.state.interactions[index][1] - fixture.state.interactions[index - 1][1]
      assert.ok(elapsed >= 990, `第 ${index + 1} 个页面动作与前一动作须至少间隔一秒`)
    }
    assert.equal(fixture.state.drawerOpen, false)
    assert.ok(fixture.state.itemReads >= 4, '职位项异步出现前不得把瞬时空源判成永久失败')
  } finally {
    fixture.restore()
  }
})

test('candidate.selectSourcingPosition MAIN 已在目标职位时不重复点击职位项', async () => {
  const fixture = installM6PositionSelectorFixture({ alreadySelected: true })
  try {
    const result = await zhilianTestHooks.mainSelectSourcingPosition(fixture.refs.targetTitle)
    assert.equal(result.status, 'ready')
    assert.deepEqual(fixture.state.interactions.map(([name]) => name), [
      'open-drawer', 'close-drawer',
    ])
  } finally {
    fixture.restore()
  }
})

test('candidate.selectSourcingPosition MAIN 目标零匹配或多匹配时均不选择', async () => {
  for (const [options, reason] of [
    [{ omitTarget: true }, 'target_absent'],
    [{ duplicateTarget: true }, 'target_ambiguous'],
  ]) {
    const fixture = installM6PositionSelectorFixture(options)
    try {
      const result = await zhilianTestHooks.mainSelectSourcingPosition(fixture.refs.targetTitle)
      assert.deepEqual(result, { status: 'failed', reason })
      assert.deepEqual(fixture.state.interactions.map(([name]) => name), [
        'open-drawer', 'close-drawer',
      ])
      assert.equal(fixture.state.drawerOpen, false)
    } finally {
      fixture.restore()
    }
  }
})

test('candidate.selectSourcingPosition MAIN 推荐路由变化时不执行页面动作', async () => {
  const fixture = installM6PositionSelectorFixture()
  try {
    globalThis.location.href = 'https://rd6.zhaopin.com/app/im'
    assert.deepEqual(
      await zhilianTestHooks.mainSelectSourcingPosition(fixture.refs.targetTitle),
      { status: 'failed', reason: 'route_changed' },
    )
    assert.deepEqual(fixture.state.interactions, [])
  } finally {
    fixture.restore()
  }
})

test('candidate.selectSourcingPosition outer 使用唯一推荐页并在动作前后复核账号', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = '9'.repeat(64)
  const tab = {
    id: 600,
    active: true,
    status: 'complete',
    url: 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-old-job',
  }
  const mainCalls = []
  const actionArgs = []
  let probeCalls = 0
  let barrierCalls = 0
  globalThis.chrome = {
    tabs: {
      async query() { return [{ ...tab }] },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ target, func, args }) {
        assert.equal(target.tabId, tab.id)
        mainCalls.push(func.name)
        if (func.name === 'mainProbeZhilian') {
          probeCalls += 1
          return [{ result: {
            pageKind: 'recommend',
            loginState: 'in',
            principalFingerprint: fingerprint,
            imListVisible: false,
          } }]
        }
        assert.equal(func.name, 'mainSelectSourcingPosition')
        actionArgs.push(structuredClone(args))
        return [{ result: {
          status: 'ready',
          data: {
            positionRef: 'fixture-target-job',
            positionTitle: '目标 职位',
            observedAt: Date.now(),
          },
        } }]
      },
    },
  }
  const context = {
    signal: new AbortController().signal,
    cmdMsgId: 'select-sourcing-position-fixture',
    deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined,
    guards: undefined,
    checkpoint() {},
    async beforeSideEffect() { barrierCalls += 1 },
    async progress() {},
  }
  try {
    const data = await zhilianTestHooks.selectZhilianSourcingPosition(
      { positionTitle: '  目标\u00a0职位 ' },
      context,
      fingerprint,
    )
    assert.equal(data.positionRef, 'fixture-target-job')
    assert.equal(data.positionTitle, '目标 职位')
    assert.equal(barrierCalls, 1)
    assert.equal(probeCalls, 3)
    assert.deepEqual(mainCalls, [
      'mainProbeZhilian',
      'mainProbeZhilian',
      'mainSelectSourcingPosition',
      'mainProbeZhilian',
    ])
    assert.deepEqual(actionArgs, [['目标 职位']])
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('candidate.selectSourcingPosition outer 无推荐页时复用既有智联标签并在同一命令继续', async () => {
  const originalChrome = globalThis.chrome
  const originalSetTimeout = globalThis.setTimeout
  const originalRandom = Math.random
  const fingerprint = '8'.repeat(64)
  const tab = {
    id: 601,
    active: true,
    status: 'complete',
    url: 'https://rd6.zhaopin.com/app/im',
  }
  const created = []
  const updated = []
  const delays = []
  const events = []
  let barrierCalls = 0
  let actionCalls = 0
  globalThis.setTimeout = (callback, delay = 0, ...args) => {
    delays.push(delay)
    callback(...args)
    return 1
  }
  Math.random = () => 0
  globalThis.chrome = {
    tabs: {
      async query() { return [{ ...tab }] },
      async create(options) {
        created.push(options)
        throw new Error('已有智联标签时不应新建')
      },
      async update(id, options) {
        assert.equal(id, tab.id)
        events.push('update')
        updated.push({ id, options })
        tab.url = options.url
        return { ...tab }
      },
      async get(id) {
        assert.equal(id, tab.id)
        return { ...tab }
      },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ target, func, args }) {
        assert.equal(target.tabId, tab.id)
        if (func.name === 'mainProbeZhilian') {
          const pageKind = tab.url.includes('/app/recommend') ? 'recommend' : 'im'
          events.push(`probe:${pageKind}`)
          return [{ result: {
            pageKind,
            loginState: 'in',
            principalFingerprint: fingerprint,
            imListVisible: pageKind === 'im',
          } }]
        }
        assert.equal(func.name, 'mainSelectSourcingPosition')
        events.push('select')
        actionCalls += 1
        assert.deepEqual(args, ['目标职位'])
        return [{ result: {
          status: 'ready',
          data: {
            positionRef: 'fixture-target-job',
            positionTitle: '目标职位',
            observedAt: Date.now(),
          },
        } }]
      },
    },
  }
  const context = {
    signal: new AbortController().signal,
    cmdMsgId: 'select-sourcing-position-reuse-tab',
    deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined,
    guards: undefined,
    checkpoint() {},
    async beforeSideEffect() { barrierCalls += 1 },
    async progress() {},
  }
  try {
    const data = await zhilianTestHooks.selectZhilianSourcingPosition(
      { positionTitle: '目标职位' },
      context,
      fingerprint,
    )
    assert.equal(data.positionRef, 'fixture-target-job')
    assert.deepEqual(created, [])
    assert.deepEqual(updated, [{
      id: tab.id,
      options: { url: 'https://rd6.zhaopin.com/app/recommend' },
    }])
    assert.equal(delays[0], 1_000)
    assert.equal(actionCalls, 1)
    assert.equal(barrierCalls, 1)
    assert.deepEqual(events.slice(0, 2), ['probe:im', 'update'])
  } finally {
    globalThis.chrome = originalChrome
    globalThis.setTimeout = originalSetTimeout
    Math.random = originalRandom
  }
})

test('candidate.selectSourcingPosition outer 没有任何智联标签时交回既有 nav 恢复通道', async () => {
  const originalChrome = globalThis.chrome
  const created = []
  const updated = []
  globalThis.chrome = {
    tabs: {
      async query() { return [] },
      async create(options) {
        created.push(options)
        throw new Error('candidate 原语不得越过 nav.ensureSurface 自建页面')
      },
      async update(id, options) {
        updated.push({ id, options })
        throw new Error('没有智联标签时不应调用 update')
      },
    },
  }
  const context = {
    signal: new AbortController().signal,
    cmdMsgId: 'select-sourcing-position-create-tab',
    deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined,
    guards: undefined,
    checkpoint() {},
    async beforeSideEffect() {},
    async progress() {},
  }
  try {
    await assert.rejects(
      zhilianTestHooks.selectZhilianSourcingPosition(
        { positionTitle: '目标职位' },
        context,
        '7'.repeat(64),
      ),
      (error) => error?.code === 'CTX_NOT_READY' && error?.reason === 'pageAbsent',
    )
    assert.deepEqual(created, [])
    assert.deepEqual(updated, [])
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('candidate.selectSourcingPosition outer 复用其他智联页前账号不符则零导航', async () => {
  const originalChrome = globalThis.chrome
  const tab = {
    id: 602,
    active: true,
    status: 'complete',
    url: 'https://rd6.zhaopin.com/app/im',
  }
  let updateCalls = 0
  let barrierCalls = 0
  globalThis.chrome = {
    tabs: {
      async query() { return [{ ...tab }] },
      async update() {
        updateCalls += 1
        throw new Error('账号不符时不应导航')
      },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func }) {
        assert.equal(func.name, 'mainProbeZhilian')
        return [{ result: {
          pageKind: 'im',
          loginState: 'in',
          principalFingerprint: '5'.repeat(64),
          imListVisible: true,
        } }]
      },
    },
  }
  const context = {
    signal: new AbortController().signal,
    cmdMsgId: 'select-sourcing-position-account-mismatch',
    deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined,
    guards: undefined,
    checkpoint() {},
    async beforeSideEffect() { barrierCalls += 1 },
    async progress() {},
  }
  try {
    await assert.rejects(
      zhilianTestHooks.selectZhilianSourcingPosition(
        { positionTitle: '目标职位' },
        context,
        '4'.repeat(64),
      ),
      (error) => error?.code === 'ACCOUNT_MISMATCH',
    )
    assert.equal(updateCalls, 0)
    assert.equal(barrierCalls, 0)
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('candidate.selectSourcingPosition outer 推荐页超时未就绪时不执行职位动作', async () => {
  const originalChrome = globalThis.chrome
  const originalSetTimeout = globalThis.setTimeout
  const originalRandom = Math.random
  const tab = {
    id: 603,
    active: true,
    status: 'complete',
    url: 'https://rd6.zhaopin.com/app/im',
  }
  let barrierCalls = 0
  let scriptCalls = 0
  globalThis.setTimeout = (callback, _delay = 0, ...args) => {
    callback(...args)
    return 1
  }
  Math.random = () => 0
  globalThis.chrome = {
    tabs: {
      async query() { return [{ ...tab }] },
      async update(_id, options) {
        tab.url = options.url
        tab.status = 'loading'
        return { ...tab }
      },
      async get() { return { ...tab } },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func }) {
        scriptCalls += 1
        assert.equal(func.name, 'mainProbeZhilian')
        return [{ result: {
          pageKind: 'im',
          loginState: 'in',
          principalFingerprint: '6'.repeat(64),
          imListVisible: true,
        } }]
      },
    },
  }
  const context = {
    signal: new AbortController().signal,
    cmdMsgId: 'select-sourcing-position-page-timeout',
    deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined,
    guards: undefined,
    checkpoint() {},
    async beforeSideEffect() { barrierCalls += 1 },
    async progress() {},
  }
  try {
    await assert.rejects(
      zhilianTestHooks.selectZhilianSourcingPosition(
        { positionTitle: '目标职位' },
        context,
        '6'.repeat(64),
      ),
      (error) => error?.code === 'CTX_NOT_READY' && error?.reason === 'pageBroken',
    )
    assert.equal(barrierCalls, 0)
    assert.equal(scriptCalls, 1, '只允许导航前账号复核，不执行职位动作')
  } finally {
    globalThis.chrome = originalChrome
    globalThis.setTimeout = originalSetTimeout
    Math.random = originalRandom
  }
})

const m6SourcingFilterTarget = {
  age: { mode: 'range', minAge: 25, maxAge: 45 },
  activeWindow: 'days3',
  careerStatuses: [],
  educations: ['associate', 'bachelor', 'master', 'mbaEmba', 'doctorate'],
  gender: 'any',
  excludeViewed: true,
  excludeCoworkerContacted: false,
}

function installM6SourcingFilterFixture(options = {}) {
  const original = {
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
    setTimeout: globalThis.setTimeout,
    dateNow: Date.now,
    random: Math.random,
  }
  let now = 1_780_000_000_000
  globalThis.setTimeout = (callback, delay = 0, ...args) => {
    now += Math.max(0, Number(delay) || 0)
    queueMicrotask(() => callback(...args))
    return 1
  }
  Date.now = () => now
  Math.random = () => 0.5

  const refs = {
    job: 'fixture-filter-job',
    title: '合成筛选职位',
  }
  const state = {
    drawerOpen: false,
    popover: null,
    openCount: 0,
    confirms: 0,
    cancels: 0,
    listVersion: 0,
    listReads: 0,
    postConfirmListReads: 0,
    styleVersion: 0,
    interactions: [],
    groupReads: new Map(),
  }
  const classList = (...initial) => {
    const values = new Set(initial)
    return {
      contains(value) { return values.has(value) },
      add(value) { values.add(value) },
      remove(value) { values.delete(value) },
    }
  }
  const node = (text = '') => ({
    textContent: text,
    innerText: text,
    outerHTML: `<div>${text}</div>`,
    classList: classList(),
    disabled: false,
    value: '',
    query: new Map(),
    attrs: new Map(),
    getClientRects: () => [{}],
    querySelectorAll(selector) { return this.query.get(selector) ?? [] },
    querySelector(selector) { return this.querySelectorAll(selector)[0] ?? null },
    getAttribute(name) { return this.attrs.get(name) ?? null },
    hasAttribute(name) { return this.attrs.has(name) },
    click() {},
  })
  const interact = (name) => {
    state.interactions.push([name, Date.now()])
  }

  const groupSpecs = {
    age: {
      selector: '.filter-item-age',
      title: '年龄要求',
      labels: ['不限', '20-25', '25-30', '30-35', '35-40', '40以上', '自定义'],
      control: 'checkbox',
    },
    activeTime: {
      selector: '.filter-item-activeTime',
      title: '活跃日期',
      labels: ['不限', '今日活跃', '3天内活跃', '7天内活跃', '30天内活跃'],
      control: 'radio',
    },
    careerStatuses: {
      selector: '.filter-item-careerStatuses',
      title: '求职状态可多选',
      labels: ['不限', '在职-正在找工作', '离职-正在找工作', '在职-看看机会', '在职-暂不找工作'],
      control: 'checkbox',
    },
    educations: {
      selector: '.filter-item-educations',
      title: '学历要求可多选',
      labels: ['不限', '初中及以下', '高中', '中专/中技', '大专', '本科', '硕士', 'MBA/EMBA', '博士'],
      control: 'checkbox',
    },
    gender: {
      selector: '.filter-item-gender',
      title: '性别要求',
      labels: ['不限', '男', '女'],
      control: 'radio',
    },
    filterTypes: {
      selector: '.filter-item-filterTypes',
      title: '人才范围可多选',
      labels: ['不限', '过滤我已看过', '过滤同事已聊'],
      control: 'checkbox',
    },
  }
  const initialSelections = options.initialTarget === true
    ? {
        age: ['自定义'],
        activeTime: ['3天内活跃'],
        careerStatuses: ['不限'],
        educations: ['大专', '本科', '硕士', 'MBA/EMBA', '博士'],
        gender: ['不限'],
        filterTypes: ['过滤我已看过'],
      }
    : {
        age: ['不限'],
        activeTime: ['不限'],
        careerStatuses: ['不限'],
        educations: ['不限'],
        gender: ['不限'],
        filterTypes: ['不限'],
      }

  const groups = {}
  const rangeValues = {
    start: options.initialTarget === true ? '25岁' : '不限',
    end: options.initialTarget === true ? '45岁' : '及以上',
  }
  const rangeDisplay = (kind) => {
    const display = node(rangeValues[kind])
    Object.defineProperty(display, 'textContent', {
      get() { return rangeValues[kind] },
      set(value) { rangeValues[kind] = String(value) },
    })
    return display
  }
  const rangeSelect = (kind) => {
    const select = node()
    const display = rangeDisplay(kind)
    select.query.set('.km-input__custom, .km-input__inner', [display])
    select.click = () => {
      interact(`open-range-${kind}`)
      const values = kind === 'start'
        ? ['不限', ...Array.from({ length: 35 }, (_, index) => String(index + 16)), '55', '60', '65']
        : ['及以上', ...Array.from({ length: 35 }, (_, index) => String(index + 16)), '55', '60', '65']
      const optionsNodes = values.map((value) => {
        const option = node(value === '及以上' ? value : `${value}岁`)
        option.click = () => {
          interact(`choose-range-${kind}-${value}`)
          rangeValues[kind] = value === '及以上' ? value : `${value}岁`
          state.popover = null
        }
        return option
      })
      const popover = node()
      popover.query.set('.km-option', optionsNodes)
      state.popover = popover
    }
    return select
  }
  const startSelect = rangeSelect('start')
  const endSelect = rangeSelect('end')
  const selector = node()
  selector.query.set('.filter-select-two__start .km-select', [startSelect])
  selector.query.set('.filter-select-two__end .km-select', [endSelect])

  for (const [key, spec] of Object.entries(groupSpecs)) {
    const group = node()
    const title = node(spec.title)
    const optionNodes = spec.labels.map((label) => {
      const option = node(label)
      const labelNode = node(label)
      option.query.set('span', [labelNode])
      if (spec.control === 'radio') option.query.set('.km-radio__label', [labelNode])
      const setSelected = (selected) => {
        if (spec.control === 'radio') {
          if (selected) option.classList.add('km-radio--checked')
          else option.classList.remove('km-radio--checked')
        } else {
          option.classList.remove(selected
            ? 'recommend-checkbox-group__inactive'
            : 'recommend-checkbox-group__active')
          option.classList.add(selected
            ? 'recommend-checkbox-group__active'
            : 'recommend-checkbox-group__inactive')
        }
      }
      option.setSelected = setSelected
      setSelected(initialSelections[key].includes(label))
      option.click = () => {
        interact(`click-${key}-${label}`)
        if (spec.control === 'radio' || key === 'age') {
          for (const candidate of optionNodes) candidate.setSelected(candidate === option)
          return
        }
        const unlimited = optionNodes.find((candidate) => candidate.textContent === '不限')
        if (label === '不限') {
          for (const candidate of optionNodes) candidate.setSelected(candidate === option)
          return
        }
        unlimited.setSelected(false)
        option.setSelected(!option.classList.contains('recommend-checkbox-group__active'))
        const selectedSpecifics = optionNodes.filter((candidate) =>
          candidate.textContent !== '不限' &&
          candidate.classList.contains('recommend-checkbox-group__active'))
        if (selectedSpecifics.length === 0) unlimited.setSelected(true)
      }
      return option
    })
    group.optionNodes = optionNodes
    group.querySelectorAll = (query) => {
      if (query === '.tr-talent-filter-item__title, .filter-group-major__title, .filter-item__title') {
        return [title]
      }
      if (query === (spec.control === 'radio'
        ? '.km-radio'
        : '.recommend-checkbox-group__item')) {
        return options.driftOptionSet === key ? optionNodes.slice(0, -1) : optionNodes
      }
      if (query === '.recommend-checkbox-group__selector') {
        const custom = optionNodes.find((candidate) => candidate.textContent === '自定义')
        return key === 'age' &&
          custom.classList.contains('recommend-checkbox-group__active') ? [selector] : []
      }
      return []
    }
    groups[key] = group
  }

  const cancel = node('取消')
  cancel.click = () => {
    interact('cancel')
    state.cancels += 1
    if (options.styleChangeOnCancel === true) state.styleVersion += 1
    state.drawerOpen = false
  }
  const confirm = node('确定')
  confirm.click = () => {
    interact('confirm')
    state.confirms += 1
    state.drawerOpen = false
    if (options.delayedListChangeReads === undefined && options.neverListChange !== true) {
      state.listVersion += 1
    }
  }
  const drawer = node()
  Object.defineProperty(drawer, 'isConnected', { get() { return state.drawerOpen } })
  drawer.querySelectorAll = (query) => {
    for (const [key, spec] of Object.entries(groupSpecs)) {
      if (query === spec.selector) {
        state.groupReads.set(key, (state.groupReads.get(key) ?? 0) + 1)
        if (options.missingGroup === key) return []
        if (options.duplicateGroup === key) return [groups[key], groups[key]]
        return [groups[key]]
      }
    }
    if (query === 'button[zp-stat-id="rsmlist-confirm"]') return [confirm]
    if (query === 'button') return [cancel, confirm]
    return []
  }
  const trigger = node('筛选')
  trigger.click = () => {
    interact('open-filter')
    state.openCount += 1
    state.drawerOpen = true
    if (options.driftOnReopen === true && state.openCount >= 2) {
      for (const option of groups.activeTime.optionNodes) {
        option.setSelected(option.textContent === '不限')
      }
    }
  }
  const title = node(refs.title)
  const listIdentity = () => {
    state.listReads += 1
    if (state.confirms > 0 && options.delayedListChangeReads !== undefined) {
      state.postConfirmListReads += 1
      if (state.postConfirmListReads <= options.delayedListChangeReads) {
        return `fixture-user-pending-${state.postConfirmListReads}`
      }
      if (state.listVersion === 0) state.listVersion += 1
    }
    if (options.missingListIdentity === true) return ''
    const unstableSuffix = options.unstableList === true ? `-${state.listReads}` : ''
    const cancelSuffix =
      options.identityChangeOnCancel === true && state.cancels > 0 ? '-after-cancel' : ''
    return `fixture-user-${state.listVersion}${unstableSuffix}${cancelSuffix}`
  }
  const makeListItem = (dataIndex) => {
    const listItem = node()
    listItem.attrs.set('data-index', String(dataIndex))
    const source = {}
    Object.defineProperty(source, 'userMasterId', { get: listIdentity })
    listItem.__vue__ = { _props: { source } }
    Object.defineProperty(listItem, 'outerHTML', {
      get() {
        return `<div data-index="${dataIndex}" style="top:${state.styleVersion}px">same card</div>`
      },
    })
    return listItem
  }
  const listItems = [makeListItem(0)]
  if (options.duplicateListIdentity === true) listItems.push(makeListItem(1))

  globalThis.location = {
    href: `https://rd6.zhaopin.com/app/recommend?jobNumber=${refs.job}`,
  }
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.document = {
    querySelectorAll(query) {
      if (query === '.km-modal.km-modal--open.km-modal--right') {
        return state.drawerOpen ? [drawer] : []
      }
      if (query === 'a[zp-stat-id="talent-recommend-filter-click"]') return [trigger]
      if (query === '.job-pane__item--active .job-pane__item-job-title') return [title]
      if (query === '.recommend-list__left div[role="listitem"]') return listItems
      if (query === '.km-popover.filter-select-two__popover') {
        return state.popover ? [state.popover] : []
      }
      return []
    },
  }

  return {
    refs,
    state,
    groups,
    restore() {
      globalThis.document = original.document
      globalThis.location = original.location
      globalThis.getComputedStyle = original.getComputedStyle
      globalThis.setTimeout = original.setTimeout
      Date.now = original.dateNow
      Math.random = original.random
    },
  }
}

test('candidate.applySourcingFilters MAIN 只点差异、年龄精确覆盖并二次回读后取消', async () => {
  const fixture = installM6SourcingFilterFixture()
  try {
    const result = await zhilianTestHooks.mainApplySourcingFilters(
      fixture.refs.job,
      fixture.refs.title,
      structuredClone(m6SourcingFilterTarget),
    )
    assert.equal(result.status, 'ready')
    assert.deepEqual(result.data.filters, m6SourcingFilterTarget)
    assert.equal(result.data.positionRef, fixture.refs.job)
    assert.equal(result.data.positionTitle, fixture.refs.title)
    assert.equal(fixture.state.confirms, 1, '筛选命令只能点击一次确定')
    assert.equal(fixture.state.cancels, 1, '二次回读只以取消收口')
    assert.equal(fixture.state.drawerOpen, false)
    assert.equal(fixture.state.openCount, 2)
    for (const key of Object.keys(fixture.groups)) {
      assert.equal(fixture.state.groupReads.get(key), 3,
        `${key} 必须由字面同一 readFilters 完成初读、提交前回读和二次回读`)
    }
    const names = fixture.state.interactions.map(([name]) => name)
    assert.equal(names.filter((name) => name === 'confirm').length, 1)
    assert.equal(names.includes('click-careerStatuses-不限'), false, '相同多选不得冗余点击')
    assert.equal(names.includes('click-gender-不限'), false, '相同单选不得冗余点击')
    assert.ok(names.includes('click-age-自定义'))
    assert.ok(names.includes('choose-range-start-25'))
    assert.ok(names.includes('choose-range-end-45'))
    for (let index = 1; index < fixture.state.interactions.length; index += 1) {
      const elapsed =
        fixture.state.interactions[index][1] - fixture.state.interactions[index - 1][1]
      assert.ok(elapsed >= 1_000, `第 ${index + 1} 个页面动作与前一动作须至少间隔一秒`)
    }
  } finally {
    fixture.restore()
  }
})

test('candidate.applySourcingFilters MAIN 已完全一致仍完整提交回读且不点筛选项', async () => {
  const fixture = installM6SourcingFilterFixture({ initialTarget: true })
  try {
    const result = await zhilianTestHooks.mainApplySourcingFilters(
      fixture.refs.job,
      fixture.refs.title,
      structuredClone(m6SourcingFilterTarget),
    )
    assert.equal(result.status, 'ready')
    assert.deepEqual(fixture.state.interactions.map(([name]) => name), [
      'open-filter', 'confirm', 'open-filter', 'cancel',
    ])
    assert.equal(fixture.state.confirms, 1)
    assert.equal(fixture.state.cancels, 1)
  } finally {
    fixture.restore()
  }
})

test('candidate.applySourcingFilters MAIN 等待推荐身份延迟稳定后再二次回读', async () => {
  const fixture = installM6SourcingFilterFixture({ delayedListChangeReads: 3 })
  try {
    const result = await zhilianTestHooks.mainApplySourcingFilters(
      fixture.refs.job,
      fixture.refs.title,
      structuredClone(m6SourcingFilterTarget),
    )
    assert.equal(result.status, 'ready')
    assert.ok(fixture.state.postConfirmListReads >= 5,
      '必须先跨过若干次不稳定身份，再对稳定身份完成间隔一秒的双采样')
    assert.equal(fixture.state.confirms, 1)
    assert.equal(fixture.state.cancels, 1)
  } finally {
    fixture.restore()
  }
})

test('candidate.applySourcingFilters MAIN 条件已生效时允许列表签名保持稳定', async () => {
  const fixture = installM6SourcingFilterFixture({ neverListChange: true })
  try {
    const result = await zhilianTestHooks.mainApplySourcingFilters(
      fixture.refs.job,
      fixture.refs.title,
      structuredClone(m6SourcingFilterTarget),
    )
    assert.equal(result.status, 'ready')
    assert.deepEqual(result.data.filters, m6SourcingFilterTarget)
    assert.equal(fixture.state.confirms, 1)
    assert.equal(fixture.state.openCount, 2, '稳定后仍须二次打开并回读筛选条件')
    assert.equal(fixture.state.cancels, 1)
  } finally {
    fixture.restore()
  }
})

test('candidate.applySourcingFilters MAIN 取消后仅页面样式变化不误判推荐流', async () => {
  const fixture = installM6SourcingFilterFixture({ styleChangeOnCancel: true })
  try {
    const result = await zhilianTestHooks.mainApplySourcingFilters(
      fixture.refs.job,
      fixture.refs.title,
      structuredClone(m6SourcingFilterTarget),
    )
    assert.equal(result.status, 'ready')
    assert.equal(fixture.state.confirms, 1)
    assert.equal(fixture.state.cancels, 1)
    assert.equal(fixture.state.styleVersion, 1, 'fixture 必须真实模拟取消后的无关 style 漂移')
  } finally {
    fixture.restore()
  }
})

test('candidate.applySourcingFilters MAIN 取消后候选身份变化交由后续窗口独立回读', async () => {
  const fixture = installM6SourcingFilterFixture({ identityChangeOnCancel: true })
  try {
    const result = await zhilianTestHooks.mainApplySourcingFilters(
      fixture.refs.job,
      fixture.refs.title,
      structuredClone(m6SourcingFilterTarget),
    )
    assert.equal(result.status, 'ready')
    assert.deepEqual(result.data.filters, m6SourcingFilterTarget)
    assert.equal(fixture.state.confirms, 1)
    assert.equal(fixture.state.cancels, 1)
  } finally {
    fixture.restore()
  }
})

test('candidate.applySourcingFilters MAIN 稳定身份缺失或重复时不提交', async () => {
  for (const options of [
    { missingListIdentity: true },
    { duplicateListIdentity: true },
  ]) {
    const fixture = installM6SourcingFilterFixture(options)
    try {
      const result = await zhilianTestHooks.mainApplySourcingFilters(
        fixture.refs.job,
        fixture.refs.title,
        structuredClone(m6SourcingFilterTarget),
      )
      assert.deepEqual(result, { status: 'failed', reason: 'list_unavailable' })
      assert.equal(fixture.state.confirms, 0)
      assert.equal(fixture.state.cancels, 1, '提交前身份不可用时仍须关闭自有筛选面')
    } finally {
      fixture.restore()
    }
  }
})

test('candidate.applySourcingFilters MAIN 六组缺失、重复或选项漂移均不提交', async () => {
  for (const [options, reason] of [
    [{ missingGroup: 'gender' }, 'group_cardinality'],
    [{ duplicateGroup: 'age' }, 'group_cardinality'],
    [{ driftOptionSet: 'educations' }, 'option_set_mismatch'],
  ]) {
    const fixture = installM6SourcingFilterFixture(options)
    try {
      const result = await zhilianTestHooks.mainApplySourcingFilters(
        fixture.refs.job,
        fixture.refs.title,
        structuredClone(m6SourcingFilterTarget),
      )
      assert.deepEqual(result, { status: 'failed', reason })
      assert.equal(fixture.state.confirms, 0)
      assert.equal(fixture.state.cancels, 1, '自有筛选面失败时须以取消尽力收口')
    } finally {
      fixture.restore()
    }
  }
})

test('candidate.applySourcingFilters MAIN 合法但平台无精确年龄选项时响亮失败且不近似', async () => {
  const fixture = installM6SourcingFilterFixture()
  try {
    const target = structuredClone(m6SourcingFilterTarget)
    target.age = { mode: 'range', minAge: 51, maxAge: 54 }
    const result = await zhilianTestHooks.mainApplySourcingFilters(
      fixture.refs.job,
      fixture.refs.title,
      target,
    )
    assert.deepEqual(result, { status: 'failed', reason: 'range_option_unavailable' })
    assert.equal(fixture.state.confirms, 0)
    assert.equal(
      fixture.state.interactions.some(([name]) => name === 'choose-range-start-50' ||
        name === 'choose-range-start-55'),
      false,
      '不得把 51 岁近似成相邻平台选项',
    )
  } finally {
    fixture.restore()
  }
})

test('candidate.applySourcingFilters MAIN 列表不稳定或二次回读漂移时整体失败', async () => {
  for (const [options, reason, expectedConfirms] of [
    [{ unstableList: true }, 'list_unstable', 1],
    [{ driftOnReopen: true }, 'filter_mismatch', 1],
  ]) {
    const fixture = installM6SourcingFilterFixture(options)
    try {
      const result = await zhilianTestHooks.mainApplySourcingFilters(
        fixture.refs.job,
        fixture.refs.title,
        structuredClone(m6SourcingFilterTarget),
      )
      assert.deepEqual(result, { status: 'failed', reason })
      assert.equal(fixture.state.confirms, expectedConfirms)
      assert.equal(result.data, undefined, '失败不得返回部分筛选 data')
    } finally {
      fixture.restore()
    }
  }
})

test('candidate.applySourcingFilters outer 使用唯一推荐页并在动作前后复核账号', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = '8'.repeat(64)
  const tab = {
    id: 601,
    active: true,
    status: 'complete',
    url: 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-filter-job',
  }
  const mainCalls = []
  const actionArgs = []
  let probeCalls = 0
  let barrierCalls = 0
  globalThis.chrome = {
    tabs: {
      async query() { return [{ ...tab }] },
    },
    scripting: {
      async executeScript({ target, func, args }) {
        assert.equal(target.tabId, tab.id)
        mainCalls.push(func.name)
        if (func.name === 'mainProbeZhilian') {
          probeCalls += 1
          return [{ result: {
            pageKind: 'recommend',
            loginState: 'in',
            principalFingerprint: fingerprint,
            imListVisible: false,
          } }]
        }
        assert.equal(func.name, 'mainApplySourcingFilters')
        actionArgs.push(structuredClone(args))
        return [{ result: {
          status: 'ready',
          data: {
            positionRef: 'fixture-filter-job',
            positionTitle: '合成筛选职位',
            filters: structuredClone(m6SourcingFilterTarget),
            observedAt: Date.now(),
          },
        } }]
      },
    },
  }
  const context = {
    signal: new AbortController().signal,
    cmdMsgId: 'apply-sourcing-filter-fixture',
    deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined,
    guards: undefined,
    checkpoint() {},
    async beforeSideEffect() { barrierCalls += 1 },
    async progress() {},
  }
  try {
    const data = await applyZhilianSourcingFilters({
      positionRef: 'fixture-filter-job',
      positionTitle: '  合成筛选职位 ',
      filters: structuredClone(m6SourcingFilterTarget),
    }, context, fingerprint)
    assert.deepEqual(data.filters, m6SourcingFilterTarget)
    assert.equal(barrierCalls, 1)
    assert.equal(probeCalls, 3)
    assert.deepEqual(mainCalls, [
      'mainProbeZhilian',
      'mainProbeZhilian',
      'mainApplySourcingFilters',
      'mainProbeZhilian',
    ])
    assert.deepEqual(actionArgs, [[
      'fixture-filter-job',
      '合成筛选职位',
      m6SourcingFilterTarget,
    ]])

    registerM6Primitives()
    assert.ok(capabilities().includes(`${Primitive.CandidateApplySourcingFilters}@1`))
    assert.ok(lookup(Primitive.CandidateApplySourcingFilters),
      'candidate.applySourcingFilters 必须注册生产 handler')
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('candidate.readSourcingWindow MAIN current/reset/next 只返回稳定身份并推进至多一窗', async () => {
  const fixture = installM6SourcingWindowFixture()
  try {
    const current = await zhilianTestHooks.mainReadSourcingWindow('current')
    assert.equal(current.status, 'ready')
    assert.equal(current.data.positionRef, fixture.refs.job)
    assert.equal(current.data.positionTitle, '合成窗口职位')
    assert.deepEqual(current.data.platformUserRefs, fixture.refs.secondWindow)
    assert.equal(current.data.moved, false)

    const reset = await zhilianTestHooks.mainReadSourcingWindow('reset')
    assert.equal(reset.status, 'ready')
    assert.deepEqual(reset.data.platformUserRefs, fixture.refs.firstWindow)
    assert.equal(reset.data.moved, true)

    const next = await zhilianTestHooks.mainReadSourcingWindow('next')
    assert.equal(next.status, 'ready')
    assert.deepEqual(next.data.platformUserRefs, fixture.refs.secondWindow)
    assert.equal(next.data.moved, true)
    assert.equal(Object.hasOwn(next.data, 'exhausted'), false, 'moved 不承载耗尽语义')
    const serialized = JSON.stringify([current, reset, next])
    assert.equal(serialized.includes('绝不返回姓名'), false)
    assert.equal(serialized.includes('resumeNumber'), false)
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingWindow MAIN current 等待连续稳定窗口而非返回首次瞬时读', async () => {
  const fixture = installM6SourcingWindowFixture({
    startAt: 'first',
    currentSwitchAfterReads: true,
  })
  try {
    const current = await zhilianTestHooks.mainReadSourcingWindow('current')
    assert.equal(current.status, 'ready')
    assert.deepEqual(current.data.platformUserRefs, fixture.refs.secondWindow)
    assert.equal(current.data.moved, false, 'current 只读稳定现状，不把渲染变化声明为主动移动')
    assert.ok(fixture.state.windowReads >= 5, 'current 必须完成首次读后的连续稳定采样')
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingWindow MAIN current 持续抖动到十秒后失败而非冒充稳定', async () => {
  const fixture = installM6SourcingWindowFixture({
    startAt: 'first',
    unstableCurrent: true,
    virtualTime: true,
  })
  try {
    assert.deepEqual(
      await zhilianTestHooks.mainReadSourcingWindow('current'),
      { status: 'failed', reason: 'page_unstable' },
    )
    assert.ok(fixture.state.windowReads > 5)
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingWindow MAIN 滚动后暂时空窗会等待稳定而非立即失败', async () => {
  const fixture = installM6SourcingWindowFixture({ transientAfterScrollReads: 2 })
  try {
    const reset = await zhilianTestHooks.mainReadSourcingWindow('reset')
    assert.equal(reset.status, 'ready')
    assert.deepEqual(reset.data.platformUserRefs, fixture.refs.firstWindow)
    assert.equal(reset.data.moved, true)
    assert.equal(fixture.state.transientReadsRemaining, 0)
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingWindow MAIN 接受无 overflow 标记但可滚动的 document root', async () => {
  const fixture = installM6SourcingWindowFixture({ startAt: 'first', documentRoot: true })
  try {
    const next = await zhilianTestHooks.mainReadSourcingWindow('next')
    assert.equal(next.status, 'ready')
    assert.deepEqual(next.data.platformUserRefs, fixture.refs.secondWindow)
    assert.equal(next.data.moved, true)
    assert.equal(fixture.scroller.scrollTop, fixture.scroller.clientHeight,
      'next 只推进一个 document viewport')
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingWindow MAIN 从常驻 DOM 中只返回当前视口候选人', async () => {
  const fixture = installM6SourcingWindowFixture({ startAt: 'first', staticDom: true })
  try {
    assert.equal(globalThis.document.querySelectorAll(
      '.recommend-list__left div[role="listitem"]',
    ).length, 4, '四张卡始终常驻 DOM')
    const current = await zhilianTestHooks.mainReadSourcingWindow('current')
    assert.equal(current.status, 'ready')
    assert.deepEqual(current.data.platformUserRefs, fixture.refs.firstWindow)

    const next = await zhilianTestHooks.mainReadSourcingWindow('next')
    assert.equal(next.status, 'ready')
    assert.deepEqual(next.data.platformUserRefs, fixture.refs.secondWindow)
    assert.equal(next.data.moved, true)
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingWindow MAIN 可遍历 document root 上超过 32 张常驻卡片并在尾部停止', async () => {
  const fixture = installM6SourcingWindowFixture({
    startAt: 'first', staticDom: true, staticDomCount: 34, documentRoot: true,
    virtualTime: true,
  })
  try {
    delete fixture.windows[0][33].__vue__._props.source.userMasterId
    const seen = new Set()
    let window = await zhilianTestHooks.mainReadSourcingWindow('reset')
    assert.equal(window.status, 'ready')
    for (const ref of window.data.platformUserRefs) seen.add(ref)
    assert.ok(window.data.platformUserRefs.length <= 32)

    for (let index = 0; index < 20; index += 1) {
      window = await zhilianTestHooks.mainReadSourcingWindow('next')
      if (window.status === 'failed') {
        assert.equal(seen.size, 32, '仅含坏身份的当前尾窗整体失败，前 32 张均应已遍历')
        assert.equal(window.reason, 'candidate_identity_unavailable')
        break
      }
      assert.ok(window.data.platformUserRefs.length <= 32)
      for (const ref of window.data.platformUserRefs) seen.add(ref)
    }
    assert.equal(seen.size, 32)

    fixture.windows[0][33].__vue__._props.source.userMasterId = fixture.refs.all[33]
    window = await zhilianTestHooks.mainReadSourcingWindow('current')
    assert.equal(window.status, 'ready')
    for (const ref of window.data.platformUserRefs) seen.add(ref)
    assert.equal(seen.size, 34)

    const tail = await zhilianTestHooks.mainReadSourcingWindow('next')
    assert.equal(tail.status, 'ready')
    assert.equal(tail.data.moved, false)
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingWindow MAIN 对候选人身份与职位不唯一响亮失败', async () => {
  for (const [name, mutate, reason] of [
    ['身份缺失', (fixture) => fixture.removeFirstIdentity(), 'candidate_identity_unavailable'],
    ['身份重复', (fixture) => fixture.duplicateIdentity(), 'candidate_identity_duplicated'],
    ['职位错绑', (fixture) => { fixture.store.state.talent.activeJob.jobNumber = 'other-job' },
      'position_identity_mismatch'],
    ['职位标题多解', (fixture) => { fixture.state.visibleTitles = ['职位一', '职位二'] },
      'position_title_ambiguous'],
  ]) {
    const fixture = installM6SourcingWindowFixture({ startAt: 'first' })
    try {
      mutate(fixture)
      assert.deepEqual(await zhilianTestHooks.mainReadSourcingWindow('current'), {
        status: 'failed', reason,
      }, name)
    } finally {
      fixture.restore()
    }
  }
})

test('candidate.readSourcingTargetResume MAIN 只打开当前窗唯一目标并复核职位后关闭', async () => {
  const fixture = installM6SourcingFixture()
  try {
    const result = await zhilianTestHooks.mainReadSourcingResume([], {
      platformUserRef: fixture.refs.secondUser,
      positionRef: fixture.refs.job,
    })
    assert.equal(result.status, 'ready')
    assert.equal(result.data.platformUserRef, fixture.refs.secondUser)
    assert.equal(result.data.positionRef, fixture.refs.job)
    assert.deepEqual(fixture.state.clicks, [fixture.refs.secondUser])
    assert.equal(fixture.state.modals.length, 0, '定点读取成功也必须关闭详情')
    assert.equal(globalThis.location.href.includes('resumeNumber='), false)
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingTargetResume MAIN 等待异步详情连续稳定后再收编', async () => {
  const fixture = installM6SourcingFixture({ detailReadyAfterEvaluations: 2 })
  try {
    const result = await zhilianTestHooks.mainReadSourcingResume([], {
      platformUserRef: fixture.refs.secondUser,
      positionRef: fixture.refs.job,
    })
    assert.equal(result.status, 'ready')
    assert.equal(result.data.platformUserRef, fixture.refs.secondUser)
    assert.ok(fixture.state.detailEvaluations >= 3,
      '一次未就绪读后必须取得连续两次完整一致投影')
    assert.deepEqual(fixture.state.clicks, [fixture.refs.secondUser], '等待期间不得重复打开详情')
    assert.equal(fixture.state.modals.length, 0)
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingTargetResume MAIN 对目标零匹配、多匹配与职位错绑均不点击', async () => {
  for (const [name, target, mutate, reason] of [
    ['零匹配', (fixture) => ({ platformUserRef: 'missing-user', positionRef: fixture.refs.job }),
      () => {}, 'no_candidate'],
    ['多匹配', (fixture) => ({ platformUserRef: fixture.refs.firstUser, positionRef: fixture.refs.job }),
      (fixture) => { fixture.second.owner._props.source.userMasterId = fixture.refs.firstUser },
      'candidate_identity_duplicated'],
    ['职位错绑', (fixture) => ({ platformUserRef: fixture.refs.secondUser, positionRef: 'other-job' }),
      () => {}, 'position_identity_mismatch'],
  ]) {
    const fixture = installM6SourcingFixture()
    try {
      mutate(fixture)
      assert.deepEqual(await zhilianTestHooks.mainReadSourcingResume([], target(fixture)), {
        status: 'failed', reason,
      }, name)
      assert.deepEqual(fixture.state.clicks, [], `${name} 不得打开详情`)
    } finally {
      fixture.restore()
    }
  }
})

test('candidate.readSourcingTargetResume MAIN 关闭无法确认时整体失败且保留现场', async () => {
  const fixture = installM6SourcingFixture({ closeUnavailable: true })
  try {
    const result = await zhilianTestHooks.mainReadSourcingResume([], {
      platformUserRef: fixture.refs.secondUser,
      positionRef: fixture.refs.job,
    })
    assert.deepEqual(result, { status: 'failed', reason: 'close_unavailable' })
    assert.equal(result.data, undefined)
    assert.deepEqual(fixture.state.clicks, [fixture.refs.secondUser])
    assert.equal(fixture.state.modals.length, 1)
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingResume MAIN 打开首个未排除候选人并完整绑定五分区', async () => {
  for (const excludedFirst of [false, true]) {
    const fixture = installM6SourcingFixture()
    try {
      const exclusions = excludedFirst ? [fixture.refs.firstUser] : []
      const result = await zhilianTestHooks.mainReadSourcingResume(exclusions)
      assert.equal(result.status, 'ready')
      const expectedUser = excludedFirst ? fixture.refs.secondUser : fixture.refs.firstUser
      assert.equal(result.data.platformUserRef, expectedUser)
      assert.equal(result.data.positionRef, fixture.refs.job)
      assert.equal(result.data.contactState, 'unestablished')
      assert.deepEqual(result.data.expectations.map(({ label }) => label), ['求职期望'])
      assert.ok(result.data.workExperiences && result.data.education)
      assert.equal(fixture.state.modals.length, 0, '成功采集后立即关闭详情')
      assert.deepEqual(fixture.state.clicks, [expectedUser])
      assert.equal(JSON.stringify(result).includes('fixture-resume-sourcing'), false,
        'resumeNumber 只能留在同次 MAIN 的瞬时 join 中')
    } finally {
      fixture.restore()
    }
  }
})

test('candidate.readSourcingResume MAIN 打开详情后至少停留两秒再关闭', async () => {
  const fixture = installM6SourcingFixture({ realTimers: true })
  try {
    const result = await zhilianTestHooks.mainReadSourcingResume([])
    assert.equal(result.status, 'ready')
    assert.ok(fixture.state.openedAt > 0 && fixture.state.closedAt > 0)
    assert.ok(fixture.state.closedAt - fixture.state.openedAt >= 2_000,
      `详情停留不足两秒: ${fixture.state.closedAt - fixture.state.openedAt}ms`)
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingResume MAIN 将同事聊过判为 established', async () => {
  const fixture = installM6SourcingFixture({ established: true })
  try {
    const result = await zhilianTestHooks.mainReadSourcingResume([])
    assert.equal(result.status, 'ready')
    assert.equal(result.data.contactState, 'established')
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingResume MAIN 无法关闭详情时不返回部分简历', async () => {
  const fixture = installM6SourcingFixture({ closeUnavailable: true })
  try {
    const result = await zhilianTestHooks.mainReadSourcingResume([])
    assert.deepEqual(result, { status: 'failed', reason: 'close_unavailable' })
    assert.equal(result.data, undefined)
    assert.equal(fixture.state.modals.length, 1, '关闭失败必须如实保留现场')
  } finally {
    fixture.restore()
  }
})

test('candidate.readSourcingResume MAIN 对身份、详情绑定与必需分区失败关闭', async () => {
  for (const [name, options, mutate, expectedReason] of [
    ['resumeNumber 不匹配', { routeResumeOverride: 'wrong-resume' }, () => {}, 'detail_binding_ambiguous'],
    ['稳定身份缺失', {}, (fixture) => fixture.removeIdentity(), 'candidate_identity_unavailable'],
    ['工作经历缺失', {}, (fixture) => fixture.removeSection('.new-work-experiences'), 'work_unresolved'],
    ['教育经历缺失', {}, (fixture) => fixture.removeSection('.new-education-experiences'), 'education_unresolved'],
  ]) {
    const fixture = installM6SourcingFixture(options)
    try {
      mutate(fixture)
      const result = await zhilianTestHooks.mainReadSourcingResume([])
      const expected = (expectedReason === 'work_unresolved' || expectedReason === 'education_unresolved')
        ? { status: 'failed', reason: expectedReason, failedPlatformUserRef: fixture.refs.firstUser }
        : { status: 'failed', reason: expectedReason }
      assert.deepEqual(result, expected, name)
      assert.equal(result.data, undefined, `${name} 不得返回部分 data`)
      assert.equal(fixture.state.modals.length, 0, `${name} 不得遗留已打开详情`)
    } finally {
      fixture.restore()
    }
  }
})

test('candidate.readSourcingResume outer 只使用全浏览器唯一推荐页并前后复核账号', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = 'c'.repeat(64)
  const tab = {
    id: 601,
    active: true,
    status: 'complete',
    url: 'https://rd6.zhaopin.com/app/recommend?jobNumber=private',
  }
  const queryCalls = []
  const mainCalls = []
  const progress = []
  let barrierCalls = 0
  let probeCalls = 0
  globalThis.chrome = {
    tabs: {
      async query(query) {
        queryCalls.push(structuredClone(query))
        return [{ ...tab }]
      },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ target, func }) {
        assert.equal(target.tabId, tab.id)
        mainCalls.push(func.name)
        if (func.name === 'mainProbeZhilian') {
          probeCalls += 1
          return [{ result: {
            pageKind: 'recommend', loginState: 'in', principalFingerprint: fingerprint,
            imListVisible: false,
          } }]
        }
        if (func.name === 'mainReadSourcingResume') return [{ result: {
          status: 'ready',
          data: {
            platformUserRef: 'fixture-user', displayName: '合成候选人',
            positionRef: 'fixture-job', positionTitle: '合成职位', contactState: 'unestablished',
            observedAt: Date.now(), basic: [{ label: '姓名', value: '合成候选人' }],
            expectations: [{ label: '求职期望', value: '合成职位' }], selfEvaluation: '',
            education: '合成教育', workExperiences: '合成经历',
          },
        } }]
        throw new Error(`unexpected MAIN ${func.name}`)
      },
    },
  }
  const context = {
    signal: new AbortController().signal,
    cmdMsgId: 'read-sourcing-fixture',
    deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined,
    guards: undefined,
    checkpoint() {},
    async beforeSideEffect() { barrierCalls += 1 },
    async progress(label, percent) { progress.push([label, percent]) },
  }
  try {
    const data = await readZhilianSourcingResume({ excludePlatformUserRefs: [] }, context, fingerprint)
    assert.equal(data.platformUserRef, 'fixture-user')
    assert.equal(barrierCalls, 1)
    assert.equal(probeCalls, 3)
    assert.deepEqual(queryCalls, [
      { url: 'https://rd6.zhaopin.com/*' },
      { url: 'https://rd6.zhaopin.com/*' },
      { url: 'https://rd6.zhaopin.com/*' },
    ])
    assert.deepEqual(mainCalls, [
      'mainProbeZhilian', 'mainProbeZhilian', 'mainReadSourcingResume', 'mainProbeZhilian',
    ])
    assert.equal(progress.at(-1)[1], 100)
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('candidate.readSourcingWindow/TargetResume outer 共用唯一推荐页、账号三次复核与取消屏障', async () => {
  const fingerprint = 'e'.repeat(64)
  const tab = {
    id: 603,
    active: true,
    status: 'complete',
    url: 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-job',
  }
  const resumeData = {
    platformUserRef: 'fixture-user', displayName: '合成候选人',
    positionRef: 'fixture-job', positionTitle: '合成职位', contactState: 'unestablished',
    observedAt: Date.now(), basic: [{ label: '姓名', value: '合成候选人' }],
    expectations: [{ label: '求职期望', value: '合成职位' }], selfEvaluation: '',
    education: '合成教育', workExperiences: '合成经历',
  }
  for (const scenario of [
    {
      name: 'window',
      mainName: 'mainReadSourcingWindow',
      args: { move: 'current' },
      invoke: (context) => readZhilianSourcingWindow({ move: 'current' }, context, fingerprint),
      mainResult: {
        status: 'ready',
        data: {
          positionRef: 'fixture-job', positionTitle: '合成职位',
          platformUserRefs: ['fixture-user'], moved: false, observedAt: Date.now(),
        },
      },
    },
    {
      name: 'target',
      mainName: 'mainReadSourcingResume',
      args: [[], { platformUserRef: 'fixture-user', positionRef: 'fixture-job' }],
      invoke: (context) => readZhilianSourcingTargetResume({
        platformUserRef: 'fixture-user', positionRef: 'fixture-job',
      }, context, fingerprint),
      mainResult: { status: 'ready', data: resumeData },
    },
  ]) {
    const originalChrome = globalThis.chrome
    const queryCalls = []
    const mainCalls = []
    const actionArgs = []
    let probeCalls = 0
    let barrierCalls = 0
    globalThis.chrome = {
      tabs: {
        async query(query) {
          queryCalls.push(structuredClone(query))
          return [{ ...tab }]
        },
        async sendMessage() { return { ok: true } },
      },
      scripting: {
        async executeScript({ target, func, args }) {
          assert.equal(target.tabId, tab.id)
          mainCalls.push(func.name)
          if (func.name === 'mainProbeZhilian') {
            probeCalls += 1
            return [{ result: {
              pageKind: 'recommend', loginState: 'in', principalFingerprint: fingerprint,
              imListVisible: false,
            } }]
          }
          assert.equal(func.name, scenario.mainName)
          actionArgs.push(structuredClone(args))
          return [{ result: structuredClone(scenario.mainResult) }]
        },
      },
    }
    const context = {
      signal: new AbortController().signal,
      cmdMsgId: `read-sourcing-${scenario.name}-fixture`,
      deadlineMs: Date.now() + 10_000,
      irreversibleNotAfterMs: Date.now() + 10_000,
      commandContext: undefined,
      guards: undefined,
      checkpoint() {},
      async beforeSideEffect() { barrierCalls += 1 },
      async progress() {},
    }
    try {
      const data = await scenario.invoke(context)
      assert.equal(data.positionRef, 'fixture-job')
      assert.equal(barrierCalls, 1)
      assert.equal(probeCalls, 3)
      assert.equal(queryCalls.length, 3)
      assert.deepEqual(mainCalls, [
        'mainProbeZhilian', 'mainProbeZhilian', scenario.mainName, 'mainProbeZhilian',
      ])
      assert.deepEqual(actionArgs, [scenario.name === 'window' ? ['current'] : scenario.args])
    } finally {
      globalThis.chrome = originalChrome
    }
  }
})

test('candidate.readSourcingWindow outer 拒绝多推荐页与动作后账号换绑', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = 'f'.repeat(64)
  const tab = {
    id: 604,
    active: true,
    status: 'complete',
    url: 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-job',
  }
  const context = {
    signal: new AbortController().signal,
    cmdMsgId: 'read-sourcing-window-guards-fixture',
    deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined,
    guards: undefined,
    checkpoint() {},
    async beforeSideEffect() {},
    async progress() {},
  }
  try {
    globalThis.chrome = {
      tabs: {
        async query() { return [{ ...tab }, { ...tab, id: 605 }] },
        async sendMessage() { return { ok: true } },
      },
      scripting: { async executeScript() { throw new Error('多推荐页不得执行 MAIN') } },
    }
    await assert.rejects(
      () => readZhilianSourcingWindow({ move: 'current' }, context, fingerprint),
      (error) => error instanceof ZhilianPlatformError && error.code === 'ELEMENT_UNRESOLVED',
    )

    let probeCalls = 0
    globalThis.chrome = {
      tabs: {
        async query() { return [{ ...tab }] },
        async sendMessage() { return { ok: true } },
      },
      scripting: {
        async executeScript({ func }) {
          if (func.name === 'mainProbeZhilian') {
            probeCalls += 1
            return [{ result: {
              pageKind: 'recommend', loginState: 'in',
              principalFingerprint: probeCalls === 3 ? '0'.repeat(64) : fingerprint,
              imListVisible: false,
            } }]
          }
          if (func.name === 'mainReadSourcingWindow') return [{ result: {
            status: 'ready', data: {
              positionRef: 'fixture-job', positionTitle: null,
              platformUserRefs: ['fixture-user'], moved: false, observedAt: Date.now(),
            },
          } }]
          throw new Error(`unexpected MAIN ${func.name}`)
        },
      },
    }
    await assert.rejects(
      () => readZhilianSourcingWindow({ move: 'current' }, context, fingerprint),
      (error) => error instanceof ZhilianPlatformError && error.code === 'ACCOUNT_MISMATCH',
    )
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('candidate.readSourcingResume outer 同命令最多跳过五个内容不完整候选人并读取第六位', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = 'd'.repeat(64)
  const tab = {
    id: 602,
    active: true,
    status: 'complete',
    url: 'https://rd6.zhaopin.com/app/recommend?jobNumber=private',
  }
  const mainExclusions = []
  let sourcingCalls = 0
  globalThis.chrome = {
    tabs: {
      async query() { return [{ ...tab }] },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func, args }) {
        if (func.name === 'mainProbeZhilian') return [{ result: {
          pageKind: 'recommend', loginState: 'in', principalFingerprint: fingerprint,
          imListVisible: false,
        } }]
        if (func.name === 'mainReadSourcingResume') {
          sourcingCalls += 1
          mainExclusions.push(structuredClone(args[0]))
          if (sourcingCalls <= 5) return [{ result: {
            status: 'failed', reason: 'work_unresolved',
            failedPlatformUserRef: `fixture-bad-user-${sourcingCalls}`,
          } }]
          return [{ result: {
            status: 'ready',
            data: {
              platformUserRef: 'fixture-good-user', displayName: '合成候选人',
              positionRef: 'fixture-job', positionTitle: '合成职位', contactState: 'unestablished',
              observedAt: Date.now(), basic: [{ label: '姓名', value: '合成候选人' }],
              expectations: [{ label: '求职期望', value: '合成职位' }], selfEvaluation: '',
              education: '合成教育', workExperiences: '合成经历',
            },
          } }]
        }
        throw new Error(`unexpected MAIN ${func.name}`)
      },
    },
  }
  const context = {
    signal: new AbortController().signal,
    cmdMsgId: 'read-sourcing-skip-fixture',
    deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined,
    guards: undefined,
    checkpoint() {},
    async beforeSideEffect() {},
    async progress() {},
  }
  try {
    const data = await readZhilianSourcingResume(
      { excludePlatformUserRefs: ['fixture-existing-user'] }, context, fingerprint)
    assert.equal(data.platformUserRef, 'fixture-good-user')
    assert.equal(sourcingCalls, 6)
    assert.deepEqual(mainExclusions, [
      ['fixture-existing-user'],
      ['fixture-existing-user', 'fixture-bad-user-1'],
      ['fixture-existing-user', 'fixture-bad-user-1', 'fixture-bad-user-2'],
      ['fixture-existing-user', 'fixture-bad-user-1', 'fixture-bad-user-2', 'fixture-bad-user-3'],
      [
        'fixture-existing-user', 'fixture-bad-user-1', 'fixture-bad-user-2',
        'fixture-bad-user-3', 'fixture-bad-user-4',
      ],
      [
        'fixture-existing-user', 'fixture-bad-user-1', 'fixture-bad-user-2',
        'fixture-bad-user-3', 'fixture-bad-user-4', 'fixture-bad-user-5',
      ],
    ])
  } finally {
    globalThis.chrome = originalChrome
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
    setTimeout: globalThis.setTimeout,
  }
  let delayedModalPending = false
  let delayedCustomSelectionPending = false
  let timerCallbacks = 0
  if (options.realTimers !== true) {
    globalThis.setTimeout = (callback, _delay, ...args) => {
      timerCallbacks += 1
      if (delayedModalPending && timerCallbacks >= (options.modalOpenAfterTimerCalls ?? Infinity)) {
        state.modalVisible = true
        delayedModalPending = false
      }
      if (delayedCustomSelectionPending &&
          timerCallbacks >= (options.customSelectedAfterTimerCalls ?? Infinity)) {
        state.customSelected = true
        delayedCustomSelectionPending = false
      }
      queueMicrotask(() => callback(...args))
      return 1
    }
  }
  const refs = {
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
    detailCount: options.detailCount ?? 0,
    listItemCount: options.listItemCount ?? 1,
    greetingButtonCount: options.greetingButtonCount ?? 1,
    continueButtonCount: options.continueButtonCount ?? 0,
    openClicks: 0,
    optionClicks: 0,
    editClicks: 0,
    finalClicks: 0,
    instanceClicks: 0,
    candidateVisibleActions: 0,
    checkboxClicks: 0,
    textareaSets: 0,
    textareaEvents: [],
    interactions: [],
    throwOnReadAfterFinal: false,
  }

  const documentListeners = new Map()
  const emitDocumentEvent = (event) => {
    for (const listener of documentListeners.get(event.type) ?? []) listener(event)
  }
  const documentListenerCount = () => Array.from(documentListeners.values())
    .reduce((total, listeners) => total + listeners.size, 0)

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
    set value(value) {
      state.textareaSets += 1
      state.interactions.push({ kind: 'input', at: Date.now() })
      this._value = String(value)
    }
    dispatchEvent(event) {
      state.textareaEvents.push(event.type)
      emitDocumentEvent(event)
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
    state.interactions.push({ kind: 'open', at: Date.now() })
    if (state.directUnsafe) {
      state.candidateVisibleActions += 1
      return
    }
    if (Number.isInteger(options.modalOpenAfterTimerCalls)) delayedModalPending = true
    else state.modalVisible = true
  }
  if (state.directUnsafe) {
    // 代表当前公开动作表面不再是批次 0 已证实的纯两步按钮；若误点就会产生外部动作。
    opener.form = {}
    opener.type = 'submit'
  }
  const secondOpener = new FixtureHTMLElement('打招呼')
  const continueButton = new FixtureHTMLElement('继续沟通')
  const secondContinueButton = new FixtureHTMLElement('继续沟通')
  const detail = new FixtureHTMLElement()
  const secondDetail = new FixtureHTMLElement()

  const aiOption = new FixtureHTMLElement('AI 招呼')
  aiOption.querySelector = (selector) => selector === '.ai-greeting-modal__ai-icon' ? {} : null
  const customOption = new FixtureHTMLElement('统一招呼')
  customOption.classList = { contains: (name) => name === 'is-selected' && state.customSelected }
  customOption._onIntrinsicClick = () => {
    state.optionClicks += 1
    state.interactions.push({ kind: 'option', at: Date.now() })
    if (Number.isInteger(options.customSelectedAfterTimerCalls)) {
      delayedCustomSelectionPending = true
    } else {
      state.customSelected = true
    }
    if (options.trustedInputDuringOptionWait === true) {
      setTimeout(() => {
        state.textareaVisible = true
        textarea._value = options.trustedDraft ?? '真人草稿'
        emitDocumentEvent({ type: 'input', isTrusted: true, target: textarea })
      }, 0)
    }
  }
  const textarea = new FixtureTextArea(options.existingDraft ?? '平台原始招呼')
  const editIcon = new FixtureHTMLElement()
  editIcon._onIntrinsicClick = () => {
    state.editClicks += 1
    state.interactions.push({ kind: 'edit', at: Date.now() })
    if (options.trustedInputDuringEditWait === true) {
      setTimeout(() => {
        state.textareaVisible = true
        textarea._value = options.trustedDraft ?? '真人草稿'
        emitDocumentEvent({ type: 'input', isTrusted: true, target: textarea })
      }, 0)
      return
    }
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
  sendButton._onIntrinsicClick = () => {
    state.finalClicks += 1
    state.interactions.push({ kind: 'send', at: Date.now() })
  }
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
    _props: { source: { userMasterId: refs.user } },
  }
  const listItem = new FixtureHTMLElement()
  listItem.__vue__ = owner
  listItem.querySelectorAll = (selector) => selector === 'button[type="button"]'
    ? [
      ...[opener, secondOpener].slice(0, state.greetingButtonCount),
      ...[continueButton, secondContinueButton].slice(0, state.continueButtonCount),
    ]
    : []
  const secondOwner = { _props: { source: { userMasterId: refs.user } } }
  const secondListItem = new FixtureHTMLElement()
  secondListItem.__vue__ = secondOwner
  secondListItem.querySelectorAll = (selector) => selector === 'button[type="button"]' ? [secondOpener] : []
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
    href: `https://rd6.zhaopin.com/app/recommend?jobNumber=${refs.job}`,
  }
  globalThis.getComputedStyle = (element) => ({
    display: 'block',
    visibility: element === editIcon && state.editIconStyleHidden ? 'hidden' : 'visible',
  })
  globalThis.document = {
    scripts: [],
    addEventListener(type, listener) {
      const listeners = documentListeners.get(type) ?? new Set()
      listeners.add(listener)
      documentListeners.set(type, listeners)
    },
    removeEventListener(type, listener) {
      const listeners = documentListeners.get(type)
      listeners?.delete(listener)
      if (listeners?.size === 0) documentListeners.delete(type)
    },
    querySelectorAll(selector) {
      if (state.throwOnReadAfterFinal && state.finalClicks > 0) {
        throw new Error('最终 click 后不得继续读页面')
      }
      if (selector === '.new-shortcut-resume__modal') {
        return [detail, secondDetail].slice(0, state.detailCount)
      }
      if (selector === '.recommend-list__left div[role="listitem"]') {
        return [listItem, secondListItem].slice(0, state.listItemCount)
      }
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
    documentListenerCount,
    invoke,
    listItem,
    invokeRead() {
      return zhilianTestHooks.mainReadGreetingListTarget(refs.user, refs.job)
    },
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
    proofTabKinds: [],
    finalClicks: 0,
    barriers: 0,
    createdIMTabs: 0,
    removedIMTabs: 0,
    listTargetReads: 0,
    interactionPaceWaits: 0,
  }
  const tabCount = options.tabCount ?? 1
  const tabs = Array.from({ length: tabCount }, (_, index) => ({
    id: 701 + index,
    active: index === 0,
    status: 'complete',
    url: `https://rd6.zhaopin.com/app/recommend?jobNumber=${options.currentJob ?? refs.job}`,
  }))
  if (options.existingIM) tabs.push({
    id: 780, active: false, status: 'complete', url: 'https://rd6.zhaopin.com/app/im',
  })
  const currentContactState = (tabId) =>
    options.currentDataByTab?.(tabId)?.contactState ?? options.contactState ?? 'unestablished'
  const phaseResult = (phase) => {
    const configured = options[`${phase}Result`]
    if (configured === 'throw') throw new Error(`fixture-${phase}-death`)
    if (configured !== undefined) return structuredClone(configured)
    if (phase === 'prepare') return { status: 'prepared' }
    if (phase === 'preflight') return { status: 'ready' }
    return { status: 'clicked' }
  }

  // 压缩 production observer 的等待，不触碰 Dispatcher 的秒级 deadline/execBudget timer。
  globalThis.setTimeout = (callback, delay, ...args) => {
    const isInteractionPaceWait = delay >= 1_000 && delay <= 1_500
    if (isInteractionPaceWait) state.interactionPaceWaits += 1
    return original.setTimeout(
      callback,
      delay === 250 || delay === 120 || isInteractionPaceWait ? 0 : delay,
      ...args,
    )
  }
  globalThis.chrome = {
    tabs: {
      async query() { return tabs.map((tab) => ({ ...tab })) },
      async get(id) {
        const tab = tabs.find((candidate) => candidate.id === id)
        if (!tab) throw new Error('fixture-tab-absent')
        return { ...tab }
      },
      async create({ url, active }) {
        const tab = { id: 799 + state.createdIMTabs, active, status: 'complete', url }
        tabs.push(tab)
        state.createdIMTabs += 1
        return { ...tab }
      },
      async remove(id) {
        const index = tabs.findIndex((candidate) => candidate.id === id)
        if (index < 0) throw new Error('fixture-tab-absent')
        tabs.splice(index, 1)
        state.removedIMTabs += 1
      },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ target, func, args }) {
        if (func.name === 'mainProbeZhilian') {
          const tab = tabs.find((candidate) => candidate.id === target.tabId)
          const im = tab?.url.includes('/app/im') === true
          return [{ result: {
            pageKind: im ? 'im' : 'recommend', loginState: 'in', principalFingerprint: fingerprint,
            imListVisible: im,
          } }]
        }
        if (func.name === 'mainReadGreetingListTarget') {
          assert.deepEqual(args, [refs.user, refs.job])
          state.listTargetReads += 1
          const tab = tabs.find((candidate) => candidate.id === target.tabId)
          if (options.currentReadThrows) throw new Error('fixture-current-read-death')
          if ((options.currentUser ?? refs.user) !== refs.user) {
            return [{ result: { status: 'failed', reason: 'target_absent' } }]
          }
          if (state.finalClicks > 0) {
            state.proofCalls += 1
            state.proofTabKinds.push(tab?.url.includes('/app/recommend') ? 'recommend' : 'other')
            if (options.proofMode === 'throw') throw new Error('fixture-observer-death')
            const contactState = options.proofMode === 'negative' ? 'unestablished' : 'established'
            return [{ result: { status: 'ready', data: { contactState } } }]
          }
          return [{ result: {
            status: 'ready', data: { contactState: currentContactState(target.tabId) },
          } }]
        }
        if (func.name === 'mainSendGreetingOnce') {
          const phase = args.at(-1)
          state.phases.push(phase)
          const result = phaseResult(phase)
          if (phase === 'commit' && result.status === 'clicked') state.finalClicks += 1
          return [{ result }]
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

test('M6 列表招呼 prepare 完成全部编辑，attempting 后同一 evaluator 只做最终 intrinsic click', async () => {
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

test('M6 列表招呼允许编辑弹窗在十秒窗口内延迟就绪', async () => {
  const fixture = installM4GreetingFixture({ modalOpenAfterTimerCalls: 199 })
  try {
    assert.deepEqual(await fixture.invoke('prepare'), { status: 'prepared' })
    assert.equal(fixture.state.openClicks, 1)
    assert.equal(fixture.state.finalClicks, 0)
  } finally {
    fixture.restore()
  }
})

test('M6 列表招呼等待统一招呼选项在十秒窗口内真正选中', async () => {
  const fixture = installM4GreetingFixture({ customSelectedAfterTimerCalls: 199 })
  try {
    assert.deepEqual(await fixture.invoke('prepare'), { status: 'prepared' })
    assert.equal(fixture.state.optionClicks, 1)
    assert.equal(fixture.state.finalClicks, 0)
  } finally {
    fixture.restore()
  }
})

test('M6 列表招呼 prepare 的相邻点击与输入至少间隔一秒', async () => {
  const fixture = installM4GreetingFixture({ realTimers: true })
  try {
    assert.deepEqual(await fixture.invoke('prepare'), { status: 'prepared' })
    assert.deepEqual(fixture.state.interactions.map(({ kind }) => kind), [
      'open', 'option', 'edit', 'input',
    ])
    for (let index = 1; index < fixture.state.interactions.length; index += 1) {
      const previous = fixture.state.interactions[index - 1]
      const current = fixture.state.interactions[index]
      assert.ok(current.at - previous.at >= 1_000,
        `${previous.kind} → ${current.kind} 仅间隔 ${current.at - previous.at}ms`)
    }
  } finally {
    fixture.restore()
  }
})

test('M6 列表招呼 prepare 可点击 DOM 内样式隐藏的唯一编辑图标', async () => {
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

test('M6 列表招呼不接管既有编辑器，不改默认项，公开两步拓扑不成立时零动作', async () => {
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

test('M6 列表招呼 prepare 异步窗口出现真人输入时不覆盖且移除临时监听', async () => {
  for (const scenario of [
    {
      label: '选择自定义项等待期间',
      options: { trustedInputDuringOptionWait: true, trustedDraft: '选项等待期间真人草稿' },
    },
    {
      label: '展开编辑区等待期间',
      options: {
        customInitiallySelected: true,
        trustedInputDuringEditWait: true,
        trustedDraft: '编辑区等待期间真人草稿',
      },
    },
  ]) {
    const fixture = installM4GreetingFixture(scenario.options)
    try {
      assert.deepEqual(await fixture.invoke('prepare'), {
        status: 'failed', reason: 'existing_editor',
      }, scenario.label)
      assert.equal(fixture.textarea.value, scenario.options.trustedDraft,
        `${scenario.label}: 不得覆盖或恢复真人草稿`)
      assert.equal(fixture.state.textareaSets, 0,
        `${scenario.label}: 不得调用 textarea setter`)
      assert.equal(fixture.state.finalClicks, 0,
        `${scenario.label}: 不得触发最终发送`)
      assert.equal(fixture.documentListenerCount(), 0,
        `${scenario.label}: prepare 终局后必须移除临时监听`)
    } finally {
      fixture.restore()
    }
  }
})

test('M6 列表招呼 preflight 后世界变化时 commit 零最终动作', async () => {
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

test('M6 列表招呼动作前对目标、职位、关系和零/多表面变化全部失败关闭', async () => {
  const beforePrepare = [
    {
      label: '目标卡缺失',
      options: { listItemCount: 0 },
      expectedReason: 'two_step_surface_unavailable',
    },
    {
      label: '目标卡重复',
      options: { listItemCount: 2 },
      expectedReason: 'two_step_surface_unavailable',
    },
    {
      label: '详情仍打开',
      options: { detailCount: 1 },
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
      mutate() {
        globalThis.location.href = 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-other-job'
      },
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
      mutate() {
        globalThis.location.href = 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-other-job'
      },
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

test('M6 列表关系投影只按公开职位和稳定身份读取同一卡片', () => {
  const unestablished = installM4GreetingFixture()
  try {
    assert.deepEqual(unestablished.invokeRead(), {
      status: 'ready', data: { contactState: 'unestablished' },
    })
  } finally {
    unestablished.restore()
  }

  const established = installM4GreetingFixture({ greetingButtonCount: 0, continueButtonCount: 1 })
  try {
    assert.deepEqual(established.invokeRead(), {
      status: 'ready', data: { contactState: 'established' },
    })
  } finally {
    established.restore()
  }

  for (const scenario of [
    {
      label: '错职位',
      mutate() {
        globalThis.location.href = 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-other-job'
      },
      reason: 'route_mismatch',
    },
    {
      label: '目标缺失',
      mutate(fixture) { fixture.owner._props.source.userMasterId = 'fixture-other-user' },
      reason: 'target_absent',
    },
    {
      label: '目标重复',
      options: { listItemCount: 2 },
      reason: 'candidate_identity_duplicated',
    },
    {
      label: '详情打开',
      options: { detailCount: 1 },
      reason: 'detail_present',
    },
  ]) {
    const fixture = installM4GreetingFixture(scenario.options)
    try {
      scenario.mutate?.(fixture)
      assert.deepEqual(fixture.invokeRead(), { status: 'failed', reason: scenario.reason }, scenario.label)
    } finally {
      fixture.restore()
    }
  }
})

test('sendZhilianGreeting 在零/多目标、意图目标变化和已有关系时停在 attempting 前', async () => {
  const scenarios = [
    { label: '零目标', options: { tabCount: 0 }, code: ErrorCode.CtxNotReady },
    { label: '多个目标', options: { tabCount: 2 }, code: ErrorCode.ElementUnresolved },
    {
      label: '候选人变化',
      options: { currentUser: 'fixture-other-user' },
      code: ErrorCode.ElementUnresolved,
    },
    {
      label: '职位变化',
      options: { currentJob: 'fixture-other-job' },
      code: ErrorCode.ElementUnresolved,
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

test('chat.readGreetingOutcome 只读同一推荐页目标的可见关系状态', async () => {
  for (const scenario of [
    { label: '关系仍未建立', contactState: 'unestablished', confirmed: false, expectedReads: 1 },
    {
      label: '关系已建立', contactState: 'established', confirmed: true,
      existingIM: true, expectedReads: 1,
    },
    { label: '关系无法确证', contactState: 'unknown', confirmed: false, expectedReads: 1 },
    {
      label: '候选人不匹配', contactState: 'established', currentUser: 'fixture-other-user',
      confirmed: false, expectedReads: 1,
    },
    {
      label: '职位不匹配', contactState: 'established', currentJob: 'fixture-other-job',
      confirmed: false, expectedReads: 0,
    },
    {
      label: '跨页目标重复', contactState: 'established', tabCount: 2,
      confirmed: false, expectedReads: 2,
    },
    {
      label: '读取异常', contactState: 'established', currentReadThrows: true,
      confirmed: false, expectedReads: 1,
    },
  ]) {
    const fixture = installM4GreetingOrchestrationFixture({
      contactState: scenario.contactState,
      existingIM: scenario.existingIM,
      currentReadThrows: scenario.currentReadThrows,
      currentUser: scenario.currentUser,
      currentJob: scenario.currentJob,
      tabCount: scenario.tabCount,
    })
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
      assert.equal(result.contentHash, scenario.confirmed ? fixture.contentHash : undefined,
        `${scenario.label}: 只有正证回显原命令正文 hash`)
      assert.equal(result.conversationRef, undefined,
        `${scenario.label}: 推荐页正证不猜会话引用`)
      assert.equal(fixture.state.listTargetReads, scenario.expectedReads,
        `${scenario.label}: 只能读取与精确目标有关的推荐页`)
      assert.equal(fixture.state.createdIMTabs, 0, `${scenario.label}: 不得新建 IM 页`)
      assert.equal(fixture.state.removedIMTabs, 0, `${scenario.label}: 不得关闭任何 IM 页`)
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
        assert.ok(fixture.state.proofTabKinds.every((kind) => kind === 'recommend'),
          `${scenario.label}: 验证只能读原推荐页`)
        assert.equal(fixture.state.createdIMTabs, 0, `${scenario.label}: 不得新建 IM 页`)
        assert.equal(fixture.state.removedIMTabs, 0, `${scenario.label}: 不得关闭 IM 页`)
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
    url: `https://rd6.zhaopin.com/app/recommend?jobNumber=${refs.job}`,
  }
  const phases = []
  const functions = []
  let barriers = 0
  let proofCalls = 0
  let postProofSettleWaits = 0
  let interactionPaceWaits = 0
  let finalClicked = false
  globalThis.setTimeout = (callback, delay) => {
    if (delay === 250 && finalClicked && proofCalls > 0) postProofSettleWaits += 1
    if (delay >= 1_000 && delay <= 1_500) interactionPaceWaits += 1
    queueMicrotask(callback)
    return 1
  }
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
        if (func.name === 'mainReadGreetingListTarget') {
          assert.deepEqual(args, [refs.user, refs.job])
          if (finalClicked) proofCalls += 1
          return [{ result: {
            status: 'ready',
            data: { contactState: finalClicked ? 'established' : 'unestablished' },
          } }]
        }
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
          finalClicked = true
          return [{ result: { status: 'clicked' } }]
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
    assert.equal(result.conversationRef, undefined)
    assert.equal(result.contentHash, contentHash)
    assert.deepEqual(phases, ['prepare', 'preflight', 'commit'])
    assert.equal(new Set(functions).size, 1,
      'prepare/preflight/commit 必须注入字面同一份 evaluator 函数')
    assert.equal(barriers, 1)
    assert.equal(interactionPaceWaits, 1, '填入招呼正文后必须等待 1～1.5 秒再进入最终发送链')
    assert.equal(proofCalls, 1, '同一目标“继续沟通”一次明确读取即构成正证')
    assert.equal(postProofSettleWaits, 1, '正证后只等待一次页面重渲染，不增加读取或动作')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('智联 MAIN 线程解析：方向不猜、105 按已证实发起方形状映射换微信请求', async () => {
  const rows = [
    { idServer: 'm-text-out', status: 'success', time: 1, type: 'text', from: 'staff', text: '  招聘方  消息 ' },
    { idServer: 'm-text-in', time: 2, type: 'text', from: 'candidate', text: '候选人消息' },
    {
      idServer: 'm-card', status: 'success', time: 3, type: 'custom', from: 'candidate',
      content: JSON.stringify({
        type: '105',
        content: JSON.stringify({ originType: '2', requestId: 'request-1', userContent: '交换微信' }),
      }),
    },
    {
      idServer: 'm-staff-105', status: 'success', time: 4, type: 'custom', from: 'staff',
      content: JSON.stringify({
        type: '105',
        content: JSON.stringify({ originType: '1', staffContent: '交换微信' }),
      }),
    },
    {
      idServer: 'm-unknown', time: 5, type: 99, from: 'candidate',
      content: JSON.stringify({ type: 2, msgb: '已拒绝' }),
    },
  ]
  globalThis.window = {
    $session: { staff: { staffId: 'staff' } },
    imEngine: {
      sessions: [{ sessionId: 'conversation-1', peerPartnerId: 'candidate', name: '候选人' }],
      async getHistoryMsgs() { return rows },
    },
  }
  const page = await zhilianTestHooks.mainReadThreadPage('conversation-1', 8, null)
  assert.equal(page.messages[0].direction, 'out')
  assert.equal(page.messages[0].text, '招聘方 消息')
  assert.equal(page.messages[1].direction, 'in')
  assert.equal(page.messages[2].kind, 'card')
  assert.equal(page.messages[2].cardState, 'pending')
  assert.equal(page.messages[3].direction, 'out')
  assert.equal(page.messages[3].kind, 'card')
  assert.equal(page.messages[3].cardType, 'wechatExchange')
  assert.equal(page.messages[3].cardState, 'pending')
  assert.equal(page.messages[4].direction, 'system')
  assert.equal(page.messages[4].kind, 'system')

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

test('智联 148 拒绝模板在读取、发送基线与最终 evaluator 中严格同义', async () => {
  const fixture = installM3SendFixture()
  const rejectionText = '很抱歉，我暂时不考虑这个机会，感谢您的认可~'
  const staffID = globalThis.window.$session.staff.staffId
  const variants = [
    {
      name: '候选人 staffText', customType: 148, from: fixture.peerRef,
      idServer: '  server-type-148-raw-identity  ',
      details: { staffText: `  ${rejectionText}  ` },
      expected: { direction: 'in', kind: 'text', text: rejectionText },
    },
    {
      name: '招聘方发送者', customType: 148, from: staffID,
      details: { staffText: rejectionText },
      expected: { direction: 'system', kind: 'system', text: rejectionText },
    },
    {
      name: '发送者缺失', customType: 148, from: '',
      details: { staffText: rejectionText },
      expected: { direction: 'system', kind: 'system', text: rejectionText },
    },
    {
      name: 'staffText 缺失', customType: 148, from: fixture.peerRef,
      details: { staffText: '   ', userText: '保守系统正文' },
      expected: { direction: 'system', kind: 'system', text: '[系统消息:148]' },
    },
    {
      name: '非 success 状态', customType: 148, from: fixture.peerRef, status: 'failed',
      details: { staffText: rejectionText },
      expected: { direction: 'system', kind: 'system', text: rejectionText },
    },
    {
      name: '未验证的相邻类型', customType: 149, from: fixture.peerRef,
      details: { staffText: rejectionText },
      expected: { direction: 'system', kind: 'system', text: rejectionText },
    },
    {
      name: '未见的数字外层类型', customType: 148, rawType: 148, from: fixture.peerRef,
      details: { staffText: rejectionText },
      expected: { direction: 'system', kind: 'system', text: rejectionText },
    },
  ]
  try {
    globalThis.window.imEngine.getHistoryMsgs = async () => fixture.rows
    for (const [index, variant] of variants.entries()) {
      const idServer = variant.idServer ?? `server-type-148-${index}`
      fixture.rows.splice(0, fixture.rows.length, {
        idServer,
        time: index + 1,
        status: variant.status ?? 'success',
        type: variant.rawType ?? 'custom',
        from: variant.from,
        content: JSON.stringify({ type: variant.customType, content: JSON.stringify(variant.details) }),
      })

      const page = await zhilianTestHooks.mainReadThreadPage(fixture.conversationRef, 8, null)
      assert.equal(page.messages.length, 1, `${variant.name}: readThread 应保留一行`)
      const [message] = page.messages
      assert.deepEqual(
        { direction: message.direction, kind: message.kind, text: message.text },
        variant.expected,
        `${variant.name}: readThread 映射错误`,
      )
      assert.equal(message.contentHash, m3Hash(variant.expected.text),
        `${variant.name}: readThread contentHash 应按映射后的正文计算`)
      assert.equal(message.sourceKey, m3Hash(`source-v1|${idServer}`),
        `${variant.name}: sourceKey 必须使用冻结的 source-v1 全量 SHA-256 配方`)
      assert.match(message.sourceKey, /^[0-9a-f]{64}$/u)

      const expectedTail = [{
        direction: variant.expected.direction,
        contentHash: message.contentHash,
      }]
      const baseline = await fixture.capture(expectedTail)
      assert.equal(baseline.status, 'ready', `${variant.name}: baseline 投影必须与 readThread 一致`)
      assert.deepEqual(fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
        `${variant.name}: final evaluator 投影必须与 readThread/baseline 一致`)
    }
  } finally {
    fixture.restore()
  }
})

test('智联 313 在线简历只在真机严格形状成立时三路提升为简历卡', async () => {
  const fixture = installM3SendFixture()
  const template = '对方向您发送了在线简历'
  const staffID = globalThis.window.$session.staff.staffId
  const variants = [
    {
      name: '真机严格形状', rawType: 'custom', envelopeType: '313', from: fixture.peerRef,
      status: 'success', nested: true, staffText: `  ${template}  `, card: true,
      idServer: '  server-type-313-raw-identity  ',
    },
    {
      name: '招聘方发送者', rawType: 'custom', envelopeType: '313', from: staffID,
      status: 'success', nested: true, staffText: template, card: false,
    },
    {
      name: '第三方发送者', rawType: 'custom', envelopeType: '313', from: 'third-party',
      status: 'success', nested: true, staffText: template, card: false,
    },
    {
      name: '发送者缺失', rawType: 'custom', envelopeType: '313', from: '',
      status: 'success', nested: true, staffText: template, card: false,
    },
    {
      name: '非 success', rawType: 'custom', envelopeType: '313', from: fixture.peerRef,
      status: 'failed', nested: true, staffText: template, card: false,
    },
    {
      name: '固定模板不匹配', rawType: 'custom', envelopeType: '313', from: fixture.peerRef,
      status: 'success', nested: true, staffText: '在线简历提示发生变化', card: false,
    },
    {
      name: '只有展示 fallback 文本', rawType: 'custom', envelopeType: '313', from: fixture.peerRef,
      status: 'success', nested: true, staffText: '', rowText: template, card: false,
    },
    {
      name: '缺少内层 content 对象', rawType: 'custom', envelopeType: '313', from: fixture.peerRef,
      status: 'success', nested: false, staffText: template, card: false,
    },
    {
      name: '外层 content 不是序列化字符串', rawType: 'custom', envelopeType: '313', from: fixture.peerRef,
      status: 'success', nested: true, staffText: template, contentObject: true, card: false,
    },
    {
      name: '内层 type 是数字', rawType: 'custom', envelopeType: 313, from: fixture.peerRef,
      status: 'success', nested: true, staffText: template, card: false,
    },
    {
      name: '相邻类型', rawType: 'custom', envelopeType: '314', from: fixture.peerRef,
      status: 'success', nested: true, staffText: template, card: false,
    },
    {
      name: '数字顶层类型', rawType: 313, envelopeType: '313', from: fixture.peerRef,
      status: 'success', nested: true, staffText: template, card: false,
    },
  ]
  try {
    globalThis.window.imEngine.getHistoryMsgs = async () => fixture.rows
    for (const [index, variant] of variants.entries()) {
      const idServer = variant.idServer ?? `server-type-313-${index}`
      const details = { staffText: variant.staffText }
      const envelope = variant.nested
        ? { type: variant.envelopeType, content: JSON.stringify(details) }
        : { type: variant.envelopeType, ...details }
      fixture.rows.splice(0, fixture.rows.length, {
        idServer,
        time: index + 1,
        status: variant.status,
        type: variant.rawType,
        from: variant.from,
        text: variant.rowText ?? '',
        content: variant.contentObject ? envelope : JSON.stringify(envelope),
      })

      const page = await zhilianTestHooks.mainReadThreadPage(fixture.conversationRef, 8, null)
      assert.equal(page.messages.length, 1, `${variant.name}: readThread 应保留一行`)
      const [message] = page.messages
      assert.equal(message.direction, variant.card ? 'in' : 'system', `${variant.name}: direction`)
      assert.equal(message.kind, variant.card ? 'card' : 'system', `${variant.name}: kind`)
      assert.equal(message.cardType, variant.card ? 'resumeAttachment' : null, `${variant.name}: cardType`)
      assert.equal(message.cardState, variant.card ? 'unknown' : null, `${variant.name}: cardState`)
      const expectedText = variant.staffText.trim() || '[系统消息:313]'
      assert.equal(message.text, variant.card ? template : expectedText, `${variant.name}: text`)
      const expectedHash = variant.card
        ? m3Hash(`card\x1fresumeAttachment\x1f${idServer.trim()}`)
        : m3Hash(expectedText)
      assert.equal(message.contentHash, expectedHash, `${variant.name}: contentHash`)
      assert.equal(message.sourceKey, m3Hash(`source-v1|${idServer}`), `${variant.name}: sourceKey`)

      const expectedTail = [{ direction: message.direction, contentHash: message.contentHash }]
      const baseline = await fixture.capture(expectedTail)
      assert.equal(baseline.status, 'ready', `${variant.name}: baseline 与 readThread 必须同义`)
      assert.deepEqual(fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
        `${variant.name}: final evaluator 与 readThread/baseline 必须同义`)
    }

    fixture.rows.splice(0, fixture.rows.length, {
      time: 99,
      status: 'success',
      type: 'custom',
      from: fixture.peerRef,
      content: JSON.stringify({ type: '313', content: JSON.stringify({ staffText: template }) }),
    })
    const missingIdentity = await zhilianTestHooks.mainReadThreadPage(fixture.conversationRef, 8, null)
    assert.match(missingIdentity.__recruitHelperMainError, /message_identity_missing/u,
      '313 缺少 idServer 时 readThread 必须响亮失败')
    const missingBaseline = await fixture.capture([])
    assert.equal(missingBaseline.status, 'failed', '313 缺少 idServer 时不得建立发送基线')
    assert.equal(fixture.invoke({
      status: 'ready',
      stage: 'ready',
      serverSourceKeys: [],
      targetBindingToken: m3Hash(JSON.stringify([fixture.conversationRef, fixture.peerRef])),
    }, 'preflight', { expectedTail: [] }).status, 'failed',
    '313 缺少 idServer 时最终 evaluator 必须停止')
  } finally {
    fixture.restore()
  }
})

test('智联 177 附件简历按窄类型归一化在四路提升为同一简历卡', async () => {
  const fixture = installM3SendFixture()
  installM5BCardActionSurface(fixture)
  const canonicalText = '您好，这是我的附件简历，请查收'
  const fallbackText = '这是我的附件简历，请查收'
  const staffID = globalThis.window.$session.staff.staffId
  const variants = [
    {
      name: '初始时间线数字类型', rawType: 'custom', envelopeType: 177, from: fixture.peerRef,
      status: 'success', contentString: true, card: true,
      idServer: 'server-type-177-equivalent',
    },
    {
      name: '历史 API 规范字符串类型', rawType: 'custom', envelopeType: '177',
      from: fixture.peerRef, status: 'success', innerContent: canonicalText,
      omitFallback: true, contentString: true, card: true,
      idServer: 'server-type-177-equivalent',
    },
    {
      name: '字符串类型配时间线 fallback', rawType: 'custom', envelopeType: '177',
      from: fixture.peerRef, status: 'success', contentString: true, card: true,
    },
    {
      name: '数字类型配历史正文', rawType: 'custom', envelopeType: 177,
      from: fixture.peerRef, status: 'success', innerContent: fallbackText,
      omitFallback: true, contentString: true, card: true,
    },
    {
      name: 'style 与展示文案变化不参与授权', rawType: 'custom', envelopeType: 177,
      from: fixture.peerRef, status: 'success', receiverStyle: 'unexpected',
      senderStyle: 99, receiverText: '展示文案变化', senderText: '',
      contentString: true, card: true,
    },
    {
      name: '无 fallback 与规范正文仍由枚举表达语义', rawType: 'custom',
      envelopeType: '177', from: fixture.peerRef, status: 'success',
      omitFallback: true, contentString: true, card: true,
    },
    {
      name: '对象 content 与字符串 content 同义', rawType: 'custom',
      envelopeType: 177, from: fixture.peerRef, status: 'success',
      contentString: false, card: true,
    },
    {
      name: '招聘方发送者', rawType: 'custom', envelopeType: 177, from: staffID,
      status: 'success', contentString: true, card: false,
    },
    {
      name: '第三方发送者', rawType: 'custom', envelopeType: 177, from: 'third-party',
      status: 'success', contentString: true, card: false,
    },
    {
      name: '发送者缺失', rawType: 'custom', envelopeType: 177, from: '',
      status: 'success', contentString: true, card: false,
    },
    {
      name: '非 success', rawType: 'custom', envelopeType: 177, from: fixture.peerRef,
      status: 'failed', contentString: true, card: false,
    },
    {
      name: '字符串带空白不是规范类型', rawType: 'custom', envelopeType: ' 177 ',
      from: fixture.peerRef, status: 'success', contentString: true, card: false,
    },
    {
      name: '字符串带前导零不是规范类型', rawType: 'custom', envelopeType: '0177',
      from: fixture.peerRef, status: 'success', contentString: true, card: false,
    },
    {
      name: '字符串小数不是规范类型', rawType: 'custom', envelopeType: '177.0',
      from: fixture.peerRef, status: 'success', contentString: true, card: false,
    },
    {
      name: '非整数数字不是规范类型', rawType: 'custom', envelopeType: 177.5,
      from: fixture.peerRef, status: 'success', contentString: true, card: false,
    },
    {
      name: '相邻类型', rawType: 'custom', envelopeType: 178, from: fixture.peerRef,
      status: 'success', contentString: true, card: false,
    },
    {
      name: '数字顶层类型', rawType: 177, envelopeType: 177, from: fixture.peerRef,
      status: 'success', contentString: true, card: false,
    },
  ]
  try {
    globalThis.window.imEngine.getHistoryMsgs = async () => fixture.rows
    for (const [index, variant] of variants.entries()) {
      const idServer = variant.idServer ?? `server-type-177-${index}`
      const envelope = {
        type: variant.envelopeType,
        ...(variant.omitFallback ? {} : {
          fallbackText: {
            receiverStyle: variant.receiverStyle ?? 1,
            receiverText: variant.receiverText ?? fallbackText,
            senderStyle: variant.senderStyle ?? 1,
            senderText: variant.senderText ?? fallbackText,
          },
        }),
        content: JSON.stringify(variant.innerContent === undefined
          ? {}
          : { content: variant.innerContent }),
      }
      fixture.rows.splice(0, fixture.rows.length, {
        idServer,
        time: index + 1,
        status: variant.status,
        type: variant.rawType,
        from: variant.from,
        text: variant.rowText ?? '',
        content: variant.contentString ? JSON.stringify(envelope) : envelope,
      })

      const page = await zhilianTestHooks.mainReadThreadPage(fixture.conversationRef, 8, null)
      assert.equal(page.messages.length, 1, `${variant.name}: readThread 应保留一行`)
      const [message] = page.messages
      assert.equal(message.direction, variant.card ? 'in' : 'system', `${variant.name}: direction`)
      assert.equal(message.kind, variant.card ? 'card' : 'system', `${variant.name}: kind`)
      assert.equal(message.cardType, variant.card ? 'resumeAttachment' : null, `${variant.name}: cardType`)
      assert.equal(message.cardState, variant.card ? 'unknown' : null, `${variant.name}: cardState`)
      const expectedText = '{}'
      assert.equal(message.text, variant.card ? canonicalText : expectedText, `${variant.name}: text`)
      const expectedHash = variant.card
        ? m3Hash(`card\x1fresumeAttachment\x1f${idServer.trim()}`)
        : m3Hash(expectedText)
      assert.equal(message.contentHash, expectedHash, `${variant.name}: contentHash`)
      assert.equal(message.sourceKey, m3Hash(`source-v1|${idServer}`), `${variant.name}: sourceKey`)

      const expectedTail = [{ direction: message.direction, contentHash: message.contentHash }]
      const baseline = await fixture.capture(expectedTail)
      assert.equal(baseline.status, 'ready', `${variant.name}: baseline 与 readThread 必须同义`)
      assert.deepEqual(fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
        `${variant.name}: 正文 evaluator 与 readThread/baseline 必须同义`)
      assert.deepEqual(
        zhilianTestHooks.mainSendCardOnce(
          fixture.conversationRef,
          'wechatInvite',
          null,
          null,
          expectedTail,
          m3Hash(fixture.principal),
          Date.now() + 10_000,
          baseline.serverSourceKeys,
          baseline.targetBindingToken,
          'preflight',
        ),
        { status: 'ready' },
        `${variant.name}: 卡片 evaluator 与 readThread/baseline 必须同义`,
      )
    }

    fixture.rows.splice(0, fixture.rows.length, {
      time: 99,
      status: 'success',
      type: 'custom',
      from: fixture.peerRef,
      text: '',
      content: JSON.stringify({
        type: 177,
        fallbackText: {
          receiverStyle: 1,
          receiverText: fallbackText,
          senderStyle: 1,
          senderText: fallbackText,
        },
        content: JSON.stringify({}),
      }),
    })
    const missingIdentity = await zhilianTestHooks.mainReadThreadPage(fixture.conversationRef, 8, null)
    assert.match(missingIdentity.__recruitHelperMainError, /message_identity_missing/u,
      '177 缺少 idServer 时 readThread 必须响亮失败')
    const missingBaseline = await fixture.capture([])
    assert.equal(missingBaseline.status, 'failed', '177 缺少 idServer 时不得建立发送基线')
    const nominalBaseline = {
      status: 'ready',
      stage: 'ready',
      serverSourceKeys: [],
      targetBindingToken: m3Hash(JSON.stringify([fixture.conversationRef, fixture.peerRef])),
    }
    assert.equal(
      fixture.invoke(nominalBaseline, 'preflight', { expectedTail: [] }).status,
      'failed',
      '177 缺少 idServer 时正文 evaluator 必须停止',
    )
    assert.equal(
      zhilianTestHooks.mainSendCardOnce(
        fixture.conversationRef,
        'wechatInvite',
        null,
        null,
        [],
        m3Hash(fixture.principal),
        Date.now() + 10_000,
        [],
        nominalBaseline.targetBindingToken,
        'preflight',
      ).status,
      'failed',
      '177 缺少 idServer 时卡片 evaluator 必须停止',
    )
  } finally {
    fixture.restore()
  }
})

test('智联 105 只在当前真机发起方形状成立时三路提升为请求卡', async () => {
  const fixture = installM3SendFixture()
  const staffID = globalThis.window.$session.staff.staffId
  let stateEndpointCalls = 0
  const variants = [
    {
      name: '候选人数字 originType=2', rawType: 'custom', envelopeType: '105',
      from: fixture.peerRef, originType: 2, status: 'success',
      expected: { direction: 'in', kind: 'card', state: 'pending' },
    },
    {
      name: '候选人字符串 originType=2', rawType: 'custom', envelopeType: 105,
      from: fixture.peerRef, originType: ' 2 ', status: 'success',
      expected: { direction: 'in', kind: 'card', state: 'pending' },
    },
    {
      name: '招聘方 originType=1', rawType: 'custom', envelopeType: '105',
      from: staffID, originType: 1, status: 'success',
      expected: { direction: 'out', kind: 'card', state: 'pending' },
    },
    {
      name: '招聘方 originType=2', rawType: 'custom', envelopeType: '105',
      from: staffID, originType: 2, status: 'success',
      expected: { direction: 'system', kind: 'system', state: null },
    },
    {
      name: '候选人 originType=1', rawType: 'custom', envelopeType: '105',
      from: fixture.peerRef, originType: 1, status: 'success',
      expected: { direction: 'system', kind: 'system', state: null },
    },
    {
      name: '第三方发送者', rawType: 'custom', envelopeType: '105',
      from: 'third-party', originType: 2, status: 'success',
      expected: { direction: 'system', kind: 'system', state: null },
    },
    {
      name: '缺 originType', rawType: 'custom', envelopeType: '105',
      from: fixture.peerRef, status: 'success',
      expected: { direction: 'system', kind: 'system', state: null },
    },
    {
      name: '非法 originType', rawType: 'custom', envelopeType: '105',
      from: fixture.peerRef, originType: 'candidate', status: 'success',
      expected: { direction: 'system', kind: 'system', state: null },
    },
    {
      name: '非 success', rawType: 'custom', envelopeType: '105',
      from: fixture.peerRef, originType: 2, status: 'failed',
      expected: { direction: 'system', kind: 'system', state: null },
    },
    {
      name: '未证实的数字顶层类型', rawType: 105, envelopeType: '105',
      from: fixture.peerRef, originType: 2, status: 'success',
      expected: { direction: 'system', kind: 'system', state: null },
    },
    {
      name: '相邻类型', rawType: 'custom', envelopeType: '106',
      from: fixture.peerRef, originType: 2, status: 'success',
      expected: { direction: 'system', kind: 'system', state: null },
    },
  ]
  try {
    globalThis.window.fetch = async () => {
      stateEndpointCalls += 1
      return { ok: true, async json() { return { data: 'ACCEPTED' } } }
    }
    globalThis.window.imEngine.getHistoryMsgs = async () => fixture.rows
    for (const [index, variant] of variants.entries()) {
      const idServer = `server-type-105-${index}`
      const details = {
        requestId: `request-${index}`,
        userContent: '候选人请求换微信',
        staffContent: '招聘方请求换微信',
      }
      if (Object.prototype.hasOwnProperty.call(variant, 'originType')) details.originType = variant.originType
      fixture.rows.splice(0, fixture.rows.length, {
        idServer,
        time: index + 1,
        status: variant.status,
        type: variant.rawType,
        from: variant.from,
        content: JSON.stringify({ type: variant.envelopeType, content: JSON.stringify(details) }),
      })

      const page = await zhilianTestHooks.mainReadThreadPage(fixture.conversationRef, 8, null)
      assert.equal(page.messages.length, 1, `${variant.name}: readThread 应保留一行`)
      const [message] = page.messages
      assert.equal(message.direction, variant.expected.direction, `${variant.name}: direction`)
      assert.equal(message.kind, variant.expected.kind, `${variant.name}: kind`)
      assert.equal(message.cardType, variant.expected.kind === 'card' ? 'wechatExchange' : null,
        `${variant.name}: cardType`)
      assert.equal(message.cardState, variant.expected.state, `${variant.name}: cardState`)
      assert.equal(message.sourceKey, m3Hash(`source-v1|${idServer}`), `${variant.name}: sourceKey`)
      if (variant.expected.kind === 'card') {
        assert.equal(
          message.contentHash,
          m3Hash('card\x1fwechatExchange'),
          `${variant.name}: contentHash 不得混入卡片状态、微信号或平台身份`,
        )
      }

      const expectedTail = [{ direction: message.direction, contentHash: message.contentHash }]
      const baseline = await fixture.capture(expectedTail)
      assert.equal(baseline.status, 'ready', `${variant.name}: baseline 与 readThread 必须同义`)
      assert.deepEqual(fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
        `${variant.name}: final evaluator 与 readThread/baseline 必须同义`)
    }
    assert.equal(stateEndpointCalls, 0, '未验证的状态接口不得覆盖候选人请求的 pending 语义')
  } finally {
    fixture.restore()
  }
})

test('智联 259 只在当前真机交换结果形状成立时三路提升为已换号卡', async () => {
  const fixture = installM3SendFixture()
  const staffID = globalThis.window.$session.staff.staffId
  const variants = [
    {
      name: '真机接受结果', rawType: 'custom', envelopeType: '259',
      from: fixture.peerRef, originType: 1, status: 'success',
      userWeChat: 'candidate-wechat-fixture', staffWeChat: 'staff-wechat-fixture', card: true,
    },
    {
      name: '字符串 originType=1', rawType: 'custom', envelopeType: 259,
      from: fixture.peerRef, originType: ' 1 ', status: 'success',
      userWeChat: 'candidate-wechat-fixture', staffWeChat: 'staff-wechat-fixture', card: true,
    },
    {
      name: '缺候选人微信', rawType: 'custom', envelopeType: '259',
      from: fixture.peerRef, originType: 1, status: 'success',
      userWeChat: '', staffWeChat: 'staff-wechat-fixture', card: false,
    },
    {
      name: '缺招聘方微信', rawType: 'custom', envelopeType: '259',
      from: fixture.peerRef, originType: 1, status: 'success',
      userWeChat: 'candidate-wechat-fixture', staffWeChat: '', card: false,
    },
    {
      name: '错误 originType', rawType: 'custom', envelopeType: '259',
      from: fixture.peerRef, originType: 2, status: 'success',
      userWeChat: 'candidate-wechat-fixture', staffWeChat: 'staff-wechat-fixture', card: false,
    },
    {
      name: '招聘方发送者', rawType: 'custom', envelopeType: '259',
      from: staffID, originType: 1, status: 'success',
      userWeChat: 'candidate-wechat-fixture', staffWeChat: 'staff-wechat-fixture', card: false,
    },
    {
      name: '非 success', rawType: 'custom', envelopeType: '259',
      from: fixture.peerRef, originType: 1, status: 'failed',
      userWeChat: 'candidate-wechat-fixture', staffWeChat: 'staff-wechat-fixture', card: false,
    },
    {
      name: '未证实的数字顶层类型', rawType: 259, envelopeType: '259',
      from: fixture.peerRef, originType: 1, status: 'success',
      userWeChat: 'candidate-wechat-fixture', staffWeChat: 'staff-wechat-fixture', card: false,
    },
  ]
  try {
    globalThis.window.imEngine.getHistoryMsgs = async () => fixture.rows
    for (const [index, variant] of variants.entries()) {
      const idServer = `server-type-259-${index}`
      fixture.rows.splice(0, fixture.rows.length, {
        idServer,
        time: index + 1,
        status: variant.status,
        type: variant.rawType,
        from: variant.from,
        content: JSON.stringify({
          type: variant.envelopeType,
          content: JSON.stringify({
            originType: variant.originType,
            receiverTitle: '交换微信结果',
            receiverText: '平台固定展示',
            userWeChat: variant.userWeChat,
            staffWeChat: variant.staffWeChat,
          }),
        }),
      })

      const page = await zhilianTestHooks.mainReadThreadPage(fixture.conversationRef, 8, null)
      assert.equal(page.messages.length, 1, `${variant.name}: readThread 应保留一行`)
      const [message] = page.messages
      assert.equal(message.direction, variant.card ? 'in' : 'system', `${variant.name}: direction`)
      assert.equal(message.kind, variant.card ? 'card' : 'system', `${variant.name}: kind`)
      assert.equal(message.cardType, variant.card ? 'wechatExchange' : null, `${variant.name}: cardType`)
      assert.equal(message.cardState, variant.card ? 'accepted' : null, `${variant.name}: cardState`)
      assert.equal(message.text, variant.card ? '[微信交换成功]' : '[系统消息:259]', `${variant.name}: text`)
      assert.equal(message.sourceKey, m3Hash(`source-v1|${idServer}`), `${variant.name}: sourceKey`)
      if (variant.card) {
        assert.equal(
          message.contentHash,
          m3Hash('card\x1fwechatExchange'),
          `${variant.name}: 接受态不得改变换微信卡不可变身份 hash`,
        )
      }

      const expectedTail = [{ direction: message.direction, contentHash: message.contentHash }]
      const baseline = await fixture.capture(expectedTail)
      assert.equal(baseline.status, 'ready', `${variant.name}: baseline 与 readThread 必须同义`)
      assert.deepEqual(fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
        `${variant.name}: final evaluator 与 readThread/baseline 必须同义`)
    }
  } finally {
    fixture.restore()
  }
})

test('智联 355 只在当前真机新版邀面形状成立时三路提升为状态未知的邀面卡', async () => {
  const fixture = installM3SendFixture()
  const staffID = globalThis.window.$session.staff.staffId
  const complete = {
    interviewId: 'interview-fixture',
    startTime: 1_800_000_000_000,
    endTime: 1_800_001_800_000,
    interviewType: 2,
    interviewPlatform: 4,
    state: 0,
    staffTitle: '线上面试邀请',
  }
  const variants = [
    {
      name: '数字枚举 2/4 保留既有容忍映射', rawType: 'custom', envelopeType: '355',
      from: staffID, status: 'success', details: complete, card: true,
      interview: {
        startsAt: complete.startTime,
        endsAt: complete.endTime,
        method: 'wechatVideo',
      },
    },
    {
      name: '真机字符串枚举 VIDEO/WECHAT_VIDEO 映射微信视频（2026-07-27）', rawType: 'custom', envelopeType: '355',
      from: staffID, status: 'success',
      details: { ...complete, interviewType: 'VIDEO', interviewPlatform: 'WECHAT_VIDEO' },
      card: true,
      interview: {
        startsAt: complete.startTime,
        endsAt: complete.endTime,
        method: 'wechatVideo',
      },
    },
    {
      name: '字符串平台 TENCENT 不猜映射', rawType: 'custom', envelopeType: '355',
      from: staffID, status: 'success',
      details: { ...complete, interviewType: 'VIDEO', interviewPlatform: 'TENCENT' },
      card: true,
    },
    {
      name: '未知数字方式不猜映射', rawType: 'custom', envelopeType: '355',
      from: staffID, status: 'success',
      details: { ...complete, interviewPlatform: 99 },
      card: true,
    },
    {
      name: '结束不晚于开始不生成 interview', rawType: 'custom', envelopeType: '355',
      from: staffID, status: 'success',
      details: { ...complete, endTime: complete.startTime },
      card: true,
    },
    { name: '候选人发送者', rawType: 'custom', envelopeType: '355', from: fixture.peerRef, status: 'success', details: complete, card: false },
    { name: '缺 interviewId', rawType: 'custom', envelopeType: '355', from: staffID, status: 'success', details: { ...complete, interviewId: '' }, card: false },
    { name: '无效开始时间', rawType: 'custom', envelopeType: '355', from: staffID, status: 'success', details: { ...complete, startTime: 0 }, card: false },
    { name: '无效结束时间', rawType: 'custom', envelopeType: '355', from: staffID, status: 'success', details: { ...complete, endTime: 'invalid' }, card: false },
    { name: '缺平台', rawType: 'custom', envelopeType: '355', from: staffID, status: 'success', details: { ...complete, interviewPlatform: '' }, card: false },
    { name: '缺 state 字段', rawType: 'custom', envelopeType: '355', from: staffID, status: 'success', details: (() => { const { state, ...rest } = complete; return rest })(), card: false },
    { name: '非 success', rawType: 'custom', envelopeType: '355', from: staffID, status: 'failed', details: complete, card: false },
    { name: '未证实的数字顶层类型', rawType: 355, envelopeType: '355', from: staffID, status: 'success', details: complete, card: false },
  ]
  try {
    globalThis.window.imEngine.getHistoryMsgs = async () => fixture.rows
    for (const [index, variant] of variants.entries()) {
      const idServer = `server-type-355-${index}`
      fixture.rows.splice(0, fixture.rows.length, {
        idServer,
        time: index + 1,
        status: variant.status,
        type: variant.rawType,
        from: variant.from,
        content: JSON.stringify({
          type: variant.envelopeType,
          content: JSON.stringify(variant.details),
        }),
      })

      const page = await zhilianTestHooks.mainReadThreadPage(fixture.conversationRef, 8, null)
      assert.equal(page.messages.length, 1, `${variant.name}: readThread 应保留一行`)
      const [message] = page.messages
      assert.equal(message.direction, variant.card ? 'out' : 'system', `${variant.name}: direction`)
      assert.equal(message.kind, variant.card ? 'card' : 'system', `${variant.name}: kind`)
      assert.equal(message.cardType, variant.card ? 'interviewInvite' : null, `${variant.name}: cardType`)
      assert.equal(message.cardState, variant.card ? 'unknown' : null, `${variant.name}: cardState`)
      assert.equal(message.text, variant.card ? '[面试邀请]' : '[系统消息:355]', `${variant.name}: text`)
      assert.equal(message.sourceKey, m3Hash(`source-v1|${idServer}`), `${variant.name}: sourceKey`)
      assert.deepEqual(message.interview, variant.interview, `${variant.name}: interview`)
      if (variant.interview) {
        assert.equal(
          message.contentHash,
          m3Hash([
            'card',
            'interviewInvite',
            String(variant.interview.startsAt),
            String(variant.interview.endsAt),
            variant.interview.method,
          ].join('\x1f')),
          `${variant.name}: contentHash 只覆盖平台无关邀面身份投影`,
        )
      }

      const expectedTail = [{ direction: message.direction, contentHash: message.contentHash }]
      const baseline = await fixture.capture(expectedTail)
      assert.equal(baseline.status, 'ready', `${variant.name}: baseline 与 readThread 必须同义`)
      assert.deepEqual(fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
        `${variant.name}: final evaluator 与 readThread/baseline 必须同义`)
    }
  } finally {
    fixture.restore()
  }
})

test('智联面试接受固定回执三路归一化为接受卡事件而不是普通对话', async () => {
  const fixture = installM3SendFixture()
  const staffID = globalThis.window.$session.staff.staffId
  const acceptedText = '我已接受贵司的面试邀请，将准时参加面试'
  const variants = [
    { name: '候选人固定回执', from: fixture.peerRef, text: `  ${acceptedText}  `, card: true, direction: 'in' },
    { name: '候选人近似文本', from: fixture.peerRef, text: `${acceptedText}。`, card: false, direction: 'in' },
    { name: '招聘方同文', from: staffID, text: acceptedText, card: false, direction: 'out' },
  ]
  try {
    globalThis.window.imEngine.getHistoryMsgs = async () => fixture.rows
    for (const [index, variant] of variants.entries()) {
      const idServer = `server-interview-accepted-${index}`
      fixture.rows.splice(0, fixture.rows.length, {
        idServer,
        time: index + 1,
        status: 'success',
        type: 'text',
        from: variant.from,
        text: variant.text,
      })

      const page = await zhilianTestHooks.mainReadThreadPage(fixture.conversationRef, 8, null)
      assert.equal(page.messages.length, 1, `${variant.name}: readThread 应保留一行`)
      const [message] = page.messages
      assert.equal(message.direction, variant.direction, `${variant.name}: direction`)
      assert.equal(message.kind, variant.card ? 'card' : 'text', `${variant.name}: kind`)
      assert.equal(message.cardType, variant.card ? 'interviewInvite' : null, `${variant.name}: cardType`)
      assert.equal(message.cardState, variant.card ? 'accepted' : null, `${variant.name}: cardState`)
      assert.equal(message.sourceKey, m3Hash(`source-v1|${idServer}`), `${variant.name}: sourceKey`)

      const expectedTail = [{ direction: message.direction, contentHash: message.contentHash }]
      const baseline = await fixture.capture(expectedTail)
      assert.equal(baseline.status, 'ready', `${variant.name}: baseline 与 readThread 必须同义`)
      assert.deepEqual(fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
        `${variant.name}: final evaluator 与 readThread/baseline 必须同义`)
    }
  } finally {
    fixture.restore()
  }
})

test('智联未知消息类型日志只含去重后的类型码且不改变消息身份', async () => {
  const originalInfo = console.info
  const original = { window: globalThis.window, document: globalThis.document, location: globalThis.location }
  const logs = []
  const sentinelText = 'PII-SENTINEL-NEVER-IN-LOG'
  try {
    console.info = (...args) => { logs.push(args) }
    globalThis.document = { scripts: [] }
    globalThis.window = {
      $session: { staff: { staffId: 'staff-log-fixture' } },
      imEngine: {
        sessions: [{ sessionId: 'conversation-log-fixture', peerPartnerId: 'candidate-log-fixture', name: '脱敏候选人' }],
        async getHistoryMsgs() {
          return [
            { idServer: 'unknown-343-a', time: 1, type: 343, from: 'candidate-log-fixture', text: sentinelText },
            { idServer: 'unknown-343-b', time: 2, type: 343, from: 'candidate-log-fixture', text: sentinelText },
            { idServer: 'unknown-shape', time: 3, type: 'future-shape', from: 'candidate-log-fixture', text: sentinelText },
          ]
        },
      },
    }
    const page = await zhilianTestHooks.mainReadThreadPage('conversation-log-fixture', 8, null)
    assert.equal(page.messages.length, 3)
    assert.equal(page.messages[0].sourceKey, m3Hash('source-v1|unknown-343-a'))
    assert.equal(page.messages[0].contentHash, m3Hash(sentinelText))
    assert.deepEqual(logs, [
      ['[RecruitHelper] zhilian_unrecognized_message_type', '343'],
      ['[RecruitHelper] zhilian_unrecognized_message_type', 'unknown'],
    ])
    assert.equal(JSON.stringify(logs).includes(sentinelText), false)
    assert.equal(JSON.stringify(logs).includes('candidate-log-fixture'), false)
    assert.equal(JSON.stringify(logs).includes('unknown-343-a'), false)
  } finally {
    console.info = originalInfo
    Object.assign(globalThis, original)
  }
})

test('智联会话 finder 只检查当前窗口且不改变滚动位置，click-once 仍独立执行', async () => {
  const original = {
    window: globalThis.window,
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
  }
  const targetRef = 'conversation-target-exact'
  let clickCalls = 0
  let scrollIntoViewCalls = 0
  const scrollElement = {
    scrollTop: 321,
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
    scrollIntoView() { scrollIntoViewCalls += 1 },
    }
    return result
  }
  const firstClickNode = { isConnected: true, getClientRects() { return [{}] } }
  const firstRow = row('conversation-first-window', firstClickNode)
  const targetRow = row(targetRef, clickTarget)
  let windowRows = [targetRow]
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
      return windowRows
    },
  }
  try {
    const found = await zhilianTestHooks.mainFindConversation(targetRef)
    assert.deepEqual(found, { status: 'found' })
    assert.equal(clickCalls, 0, 'finder 无论耗时多久都不得 click')
    assert.equal(scrollElement.scrollTop, 321)
    assert.equal(scrollIntoViewCalls, 0)
    assert.equal(globalThis.location.href, 'https://rd6.zhaopin.com/app/im')

    windowRows = [firstRow]
    const absent = await zhilianTestHooks.mainFindConversation(targetRef)
    assert.deepEqual(absent, { status: 'failed', reason: 'target_not_found' })
    assert.equal(scrollElement.scrollTop, 321, '目标只在下一窗时 finder 不得滚动寻找')
    windowRows = [targetRow]

    const principal = ['zhilian-principal-v2', staffId, orgId, loginPoint]
      .map((piece) => `${new TextEncoder().encode(piece).length}:${piece}`).join('|')
    const fingerprint = createHash('sha256').update(principal).digest('hex')
    const clicked = zhilianTestHooks.mainClickConversationOnce(targetRef, '', fingerprint, Date.now() + 1_000)
    assert.deepEqual(clicked, { status: 'clicked' })
    assert.equal(clickCalls, 1)
    assert.match(globalThis.location.href, /conversation-target-exact/u)

    windowRows = [firstRow]
    const leftUnreadList = await zhilianTestHooks.mainFindConversation(targetRef, true)
    assert.deepEqual(leftUnreadList, { status: 'failed', reason: 'target_not_found' },
      '路由已经命中目标时，未读后置观察仍必须继续检查行是否离开')
    windowRows = [targetRow]

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
    staffId,
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

function appendM3CardRow(fixture, {
  idServer,
  cardKind,
  time = fixture.rows.length + 1,
  status = 'success',
  from = globalThis.window.$session.staff.staffId,
  rawType = 'custom',
  overrides = {},
} = {}) {
  const details = cardKind === 'wechatInvite'
    ? { originType: 1, staffContent: '招聘方请求换微信', ...overrides }
    : {
        interviewId: `interview-${idServer}`,
        startTime: 1_800_000_000_000,
        endTime: 1_800_001_800_000,
        interviewType: 2,
        interviewPlatform: 4,
        state: 0,
        staffTitle: '线上面试邀请',
        ...overrides,
      }
  fixture.rows.push({
    idServer,
    time,
    status,
    type: rawType,
    from,
    content: JSON.stringify({
      type: cardKind === 'wechatInvite' ? '105' : '355',
      content: JSON.stringify(details),
    }),
  })
}

function installM5BCardActionSurface(fixture) {
  const state = {
    launchers: [],
    modals: [],
    dateValue: '2027-01-15',
    timeValue: '16:00',
    durationValue: '30分钟',
    methodSelected: true,
    onlineSelected: true,
  }
  const node = ({
    text = '',
    value = '',
    placeholder = '',
    active = false,
  } = {}) => {
    const element = new globalThis.HTMLElement()
    const input = { value, placeholder, checked: active }
    element.textContent = text
    element.children = []
    element.form = null
    element.type = 'button'
    element.matches = () => false
    element.querySelector = (selector) => selector === 'input' ? input : null
    element.querySelectorAll = () => []
    element.classList = {
      contains(name) {
        return (name === 'is-active' || name === 'is-checked') && active
      },
    }
    element.getAttribute = (name) => name === 'aria-checked' && active ? 'true' : null
    return { element, input }
  }

  const launcher = node({ text: '换微信' }).element
  const dateLabel = node({ text: state.dateValue }).element
  const timeInput = node({ value: state.timeValue, placeholder: '请选择时间' }).element
  timeInput.matches = (selector) => selector === 'input'
  const durationInput = node({ value: state.durationValue, placeholder: '面试时长' }).element
  durationInput.matches = (selector) => selector === 'input'
  const method = node({ text: '微信视频', active: true }).element
  const online = node({ text: '线上面试' }).element
  // 2026-07-27 真机：标题为"邀请{候选人姓名}参加 线上面试"，不存在字面恰好
  // "参加 线上面试"的节点；夹具用虚构姓名钉住包含匹配语义。
  const title = node({ text: '邀请 测试候选人 参加 线上面试' }).element
  const send = node({ text: '发送' }).element
  const modal = node().element
  modal.querySelector = (selector) => selector === '.interview-form' ? {} : null
  modal.querySelectorAll = (selector) => {
    if (selector === '.km-date-picker__label') return [dateLabel]
    if (selector === 'input[placeholder="请选择时间"]') return [timeInput]
    if (selector === 'input[placeholder="面试时长"]') return [durationInput]
    if (selector === '.interview-platform__btn.is-checked') {
      return state.methodSelected ? [method] : []
    }
    if (selector === '.interview-form-way-list-item') return [online]
    if (selector === '*') return [title]
    if (selector === 'button[type="button"]') return [send]
    return []
  }
  state.launchers = [launcher]
  state.modals = [modal]

  const originalDetailQuery = fixture.detail.querySelectorAll.bind(fixture.detail)
  fixture.detail.querySelectorAll = (selector) => {
    if (selector === 'a[zp-stat-id="im_ask_for_wx_open"][type="button"]') return state.launchers
    return originalDetailQuery(selector)
  }
  const originalDocumentQuery = globalThis.document.querySelectorAll.bind(globalThis.document)
  globalThis.document.querySelectorAll = (selector) => {
    if (selector === '.km-modal__wrapper.interview-modal') return state.modals
    return originalDocumentQuery(selector)
  }

  const syncInputs = () => {
    dateLabel.textContent = state.dateValue
    timeInput.value = state.timeValue
    durationInput.value = state.durationValue
    method.querySelector('input').checked = state.methodSelected
    method.classList = {
      contains(name) {
        return state.methodSelected && (name === 'is-active' || name === 'is-checked')
      },
    }
    method.getAttribute = (name) =>
      name === 'aria-checked' && state.methodSelected ? 'true' : null
    title.textContent = state.onlineSelected
      ? '邀请 测试候选人 参加 线上面试'
      : '邀请 测试候选人 参加 现场面试'
  }
  syncInputs()
  return {
    state,
    syncInputs,
  }
}

function appendM5BWechatRow(fixture, {
  idServer,
  type = 105,
  originType = 2,
  from = fixture.peerRef,
  status = 'success',
  userWeChat,
  staffWeChat,
  time = fixture.rows.length + 1,
} = {}) {
  fixture.rows.push({
    idServer,
    time,
    status,
    type: 'custom',
    from,
    content: JSON.stringify({
      type: String(type),
      content: JSON.stringify({
        originType,
        ...(type === 105 ? { userContent: '交换微信请求' } : {}),
        ...(userWeChat === undefined ? {} : { userWeChat }),
        ...(staffWeChat === undefined ? {} : { staffWeChat }),
      }),
    }),
  })
}

function installM5BWechatAcceptSurface(fixture) {
  // byClass = .imc-wx-request__actions-success 命中的控件；byText = 卡内
  // button/a。evaluator 2026-07-29 起类名优先、缺失才按可见文本兜底。
  const state = { done: false, cards: [], byClass: [], byText: [] }
  const node = (text) => {
    const element = new globalThis.HTMLElement()
    element.textContent = text
    element.classList = { contains: () => false }
    element.querySelector = () => null
    element.querySelectorAll = () => []
    return element
  }
  const action = node('同意')
  state.byClass = [action]
  state.byText = [action]
  const card = new globalThis.HTMLElement()
  card.textContent = '交换微信 同意'
  card.classList = { contains: (name) => name === 'is-wx-done' && state.done }
  card.querySelector = (selector) => selector === '.is-wx-done' && state.done ? {} : null
  card.querySelectorAll = (selector) => {
    if (selector === '.imc-wx-request__actions-success') return state.byClass
    if (selector === 'button, a') return state.byText
    return []
  }
  state.cards = [card]
  const originalQuery = fixture.detail.querySelectorAll.bind(fixture.detail)
  fixture.detail.querySelectorAll = (selector) =>
    selector === '.imc-wx-request' ? state.cards : originalQuery(selector)
  return { action, card, state, node }
}

test('M5-B 卡片 evaluator 以同一冻结输入做 preflight/commit，且最终只 click 一次', async () => {
  const fixture = installM3SendFixture()
  const surface = installM5BCardActionSurface(fixture)
  const interview = {
    startsAt: 1_800_000_000_000,
    endsAt: 1_800_001_800_000,
    method: 'wechatVideo',
  }
  try {
    const baseline = await fixture.capture()
    assert.equal(baseline.status, 'ready')
    const invoke = (cardKind, interviewValue, phase = 'preflight', overrides = {}) =>
      zhilianTestHooks.mainSendCardOnce(
        overrides.conversationRef ?? fixture.conversationRef,
        cardKind,
        interviewValue,
        null,
        overrides.expectedTail ?? fixture.expectedTail,
        overrides.fingerprint ?? m3Hash(fixture.principal),
        overrides.deadline ?? Date.now() + 10_000,
        overrides.baselineKeys ?? baseline.serverSourceKeys,
        overrides.targetToken ?? baseline.targetBindingToken,
        phase,
      )

    assert.deepEqual(invoke('wechatInvite', null, 'preflight'), { status: 'ready' })
    assert.equal(fixture.state.intrinsicClicks, 0)
    assert.deepEqual(invoke('wechatInvite', null, 'commit'), { status: 'clicked' })
    assert.equal(fixture.state.intrinsicClicks, 1)

    assert.deepEqual(invoke('interviewInvite', interview, 'preflight'), { status: 'ready' })
    assert.equal(fixture.state.intrinsicClicks, 1)
    assert.deepEqual(invoke('interviewInvite', interview, 'commit'), { status: 'clicked' })
    assert.equal(fixture.state.intrinsicClicks, 2, '两条独立命令各只允许一次标准 click')

    const clicksBeforeGuards = fixture.state.intrinsicClicks
    fixture.composer.value = '人工草稿'
    assert.deepEqual(invoke('wechatInvite', null), {
      status: 'failed',
      reason: 'composer_nonempty',
    })
    fixture.composer.value = ''

    const originalHref = globalThis.location.href
    globalThis.location.href = 'https://rd6.zhaopin.com/app/im?sessionId=other'
    assert.deepEqual(invoke('wechatInvite', null), { status: 'failed', reason: 'route_changed' })
    globalThis.location.href = originalHref

    assert.deepEqual(invoke('wechatInvite', null, 'preflight', {
      fingerprint: '0'.repeat(64),
    }), { status: 'failed', reason: 'identity_changed' })
    assert.deepEqual(invoke('wechatInvite', null, 'preflight', {
      baselineKeys: ['1'.repeat(64)],
    }), { status: 'failed', reason: 'baseline_changed' })
    assert.deepEqual(invoke('wechatInvite', null, 'preflight', {
      targetToken: '2'.repeat(64),
    }), { status: 'failed', reason: 'target_changed' })

    surface.state.launchers.push(new globalThis.HTMLElement())
    surface.state.launchers[1].textContent = '换微信'
    assert.deepEqual(invoke('wechatInvite', null), {
      status: 'failed',
      reason: 'surface_unavailable',
    })
    surface.state.launchers.pop()

    for (const [name, mutate] of [
      ['日期不精确', () => { surface.state.dateValue = '2027-01-16' }],
      ['时间不精确', () => { surface.state.timeValue = '16:01' }],
      ['时长不精确', () => { surface.state.durationValue = '60分钟' }],
      ['方式未选中', () => { surface.state.methodSelected = false }],
      ['线上面试未选中', () => { surface.state.onlineSelected = false }],
    ]) {
      surface.state.dateValue = '2027-01-15'
      surface.state.timeValue = '16:00'
      surface.state.durationValue = '30分钟'
      surface.state.methodSelected = true
      surface.state.onlineSelected = true
      mutate()
      surface.syncInputs()
      assert.deepEqual(
        invoke('interviewInvite', interview),
        { status: 'failed', reason: 'input_rejected' },
        `${name}必须在最终动作前阻断`,
      )
    }
    assert.equal(
      fixture.state.intrinsicClicks,
      clicksBeforeGuards,
      '所有 guard/表单阴性都不得产生额外 click',
    )
  } finally {
    fixture.restore()
  }
})

test('M5-B 微信接受复用同一 evaluator 精确锚定 pending 请求且只 click 一次', async () => {
  const fixture = installM3SendFixture()
  const surface = installM5BWechatAcceptSurface(fixture)
  appendM5BWechatRow(fixture, { idServer: 'wechat-request-candidate' })
  const requestSourceKey = m3Hash('source-v1|wechat-request-candidate')
  try {
    const baseline = await fixture.capture([])
    assert.equal(baseline.status, 'ready')
    const invoke = (phase = 'preflight', overrides = {}) =>
      zhilianTestHooks.mainSendCardOnce(
        fixture.conversationRef,
        'wechatAccept',
        null,
        overrides.requestSourceKey ?? requestSourceKey,
        [],
        m3Hash(fixture.principal),
        Date.now() + 10_000,
        overrides.baselineKeys ?? baseline.serverSourceKeys,
        baseline.targetBindingToken,
        phase,
      )

    assert.deepEqual(invoke(), { status: 'ready', wechatCopyCards: 0 })
    assert.equal(fixture.state.intrinsicClicks, 0)
    assert.deepEqual(invoke('commit'), { status: 'clicked', wechatCopyCards: 0 })
    assert.equal(fixture.state.intrinsicClicks, 1)

    const clicksAfterSuccess = fixture.state.intrinsicClicks
    surface.state.done = true
    assert.deepEqual(invoke(), {
      status: 'failed',
      reason: 'surface_unavailable',
      detail: 'wxaccept:all_done cards=1',
    })
    surface.state.done = false
    assert.deepEqual(invoke('preflight', { baselineKeys: ['f'.repeat(64)] }), {
      status: 'failed',
      reason: 'baseline_changed',
    })
    assert.deepEqual(invoke('preflight', { requestSourceKey: 'e'.repeat(64) }), {
      status: 'failed',
      reason: 'input_rejected',
    })
    // 类名优先：类名命中时不再看文本，图标按钮也能定位（旧项目生产同款）。
    surface.state.byClass = [surface.node('')]
    surface.state.byText = []
    assert.deepEqual(invoke(), { status: 'ready', wechatCopyCards: 0 },
      '.imc-wx-request__actions-success 命中即可定位，不要求文本')
    // 类名缺失才按可见文本兜底，且判据是"包含同意"而非全等。
    surface.state.byClass = []
    surface.state.byText = [surface.node('同意 (1)')]
    assert.deepEqual(invoke(), { status: 'ready', wechatCopyCards: 0 },
      '类名缺失时文本包含"同意"即可兜底')
    // "不同意"字面包含"同意"：放宽为包含匹配后必须排除否定式，否则唯一
    // 命中的正是拒绝控件——那是真实的错误副作用。
    surface.state.byText = [surface.node('不同意')]
    assert.deepEqual(invoke(), {
      status: 'failed',
      reason: 'surface_unavailable',
      detail: 'wxaccept:no_action byClass=0 byText=0',
    }, '否定式控件绝不可被当作同意动作')
    surface.state.byText = [surface.node('同意'), surface.node('我同意')]
    assert.deepEqual(invoke(), {
      status: 'failed',
      reason: 'surface_unavailable',
      detail: 'wxaccept:multi_action byClass=0 byText=2',
    }, '候选控件不唯一必须停在 click 前')
    surface.state.cards = []
    assert.deepEqual(invoke(), { status: 'failed', reason: 'surface_unavailable', detail: 'wxaccept:no_card' })
    surface.state.cards = [surface.card, surface.card]
    assert.deepEqual(invoke(), {
      status: 'failed',
      reason: 'surface_unavailable',
      detail: 'wxaccept:multi_card cards=2 pending=2',
    })
    surface.state.cards = [surface.card]
    surface.state.byClass = [surface.action]
    surface.state.byText = [surface.action]
    assert.equal(fixture.state.intrinsicClicks, clicksAfterSuccess,
      '以上全部定位失败分支都不得点击')

    // 候选人主动发起(originType=2)的交换结果归我方(out)，故已存在的 259
    // 必须按 staffId 认；按 target 认会永远漏判而重复派发。
    appendM5BWechatRow(fixture, {
      idServer: 'wechat-result-already-done',
      type: 259,
      from: fixture.staffId,
      userWeChat: 'peer_fixture',
      staffWeChat: 'staff_fixture',
    })
    const afterOutcomeBaseline = await fixture.capture([])
    assert.equal(afterOutcomeBaseline.status, 'ready')
    assert.deepEqual(
      zhilianTestHooks.mainSendCardOnce(
        fixture.conversationRef,
        'wechatAccept',
        null,
        requestSourceKey,
        [],
        m3Hash(fixture.principal),
        Date.now() + 10_000,
        afterOutcomeBaseline.serverSourceKeys,
        afterOutcomeBaseline.targetBindingToken,
        'preflight',
      ),
      { status: 'failed', reason: 'surface_unavailable', detail: 'wxaccept:already_exchanged n=1' },
      '已经存在交换结果时重复执行必须停在 click 前',
    )
    assert.equal(fixture.state.intrinsicClicks, clicksAfterSuccess)
  } finally {
    fixture.restore()
  }
})

test('M5-B 微信结果只读面区分两种 105→259 origin，并在歧义或缺字段时保持阴性', async () => {
  const fixture = installM3SendFixture()
  try {
    const reset = () => fixture.rows.splice(0, fixture.rows.length)
    const read = (requestID) => zhilianTestHooks.mainReadWechatExchangeOutcome(
      fixture.conversationRef,
      m3Hash(`source-v1|${requestID}`),
      null,
      null,
    )

    // 形态 A（候选人主动发起，originType=2）：由我方点同意，259 归我方(out)。
    // 2026-07-28 生产页面直读；旧锚写成 in 是本形态全线不通的直接原因。
    reset()
    appendM5BWechatRow(fixture, { idServer: 'candidate-request' })
    appendM5BWechatRow(fixture, {
      idServer: 'candidate-result',
      type: 259,
      from: fixture.staffId,
      userWeChat: 'peer_candidate_fixture',
      staffWeChat: 'staff_fixture',
    })
    assert.deepEqual(await read('candidate-request'), {
      confirmed: true,
      exchangeSourceKey: m3Hash('source-v1|candidate-result'),
      peerWechat: 'peer_candidate_fixture',
    })

    reset()
    appendM5BWechatRow(fixture, { idServer: 'candidate-request-wrong-side' })
    appendM5BWechatRow(fixture, {
      idServer: 'candidate-result-wrong-side',
      type: 259,
      userWeChat: 'peer_candidate_fixture',
      staffWeChat: 'staff_fixture',
    })
    assert.deepEqual(await read('candidate-request-wrong-side'), { confirmed: false },
      'originType=2 的 259 若归对方(in)则方向不符，必须保持阴性')

    reset()
    appendM5BWechatRow(fixture, {
      idServer: 'staff-request',
      originType: 1,
      from: globalThis.window.$session.staff.staffId,
    })
    appendM5BWechatRow(fixture, {
      idServer: 'staff-result',
      type: 259,
      originType: 1,
      userWeChat: 'peer_staff_fixture',
      staffWeChat: 'staff_fixture',
    })
    assert.deepEqual(await read('staff-request'), {
      confirmed: true,
      exchangeSourceKey: m3Hash('source-v1|staff-result'),
      peerWechat: 'peer_staff_fixture',
    })

    reset()
    appendM5BWechatRow(fixture, { idServer: 'missing-field-request' })
    appendM5BWechatRow(fixture, {
      idServer: 'missing-field-result',
      type: 259,
      from: fixture.staffId,
      userWeChat: 'peer_fixture',
    })
    assert.deepEqual(await read('missing-field-request'), { confirmed: false })

    reset()
    appendM5BWechatRow(fixture, { idServer: 'ambiguous-request' })
    appendM5BWechatRow(fixture, {
      idServer: 'ambiguous-result-1',
      type: 259,
      from: fixture.staffId,
      userWeChat: 'peer_fixture_1',
      staffWeChat: 'staff_fixture',
    })
    appendM5BWechatRow(fixture, {
      idServer: 'ambiguous-result-2',
      type: 259,
      from: fixture.staffId,
      userWeChat: 'peer_fixture_2',
      staffWeChat: 'staff_fixture',
    })
    assert.deepEqual(await read('ambiguous-request'), { confirmed: false })

    reset()
    appendM5BWechatRow(fixture, { idServer: 'bounded-request' })
    appendM5BWechatRow(fixture, { idServer: 'next-request' })
    appendM5BWechatRow(fixture, {
      idServer: 'late-result',
      type: 259,
      from: fixture.staffId,
      userWeChat: 'peer_late_fixture',
      staffWeChat: 'staff_fixture',
    })
    assert.deepEqual(await read('bounded-request'), { confirmed: false })
  } finally {
    fixture.restore()
  }
})

test('M5-B 卡片 observer 只接受 baseline 后严格 +1，并返回规范 hash 与 sourceKey', async () => {
  const fixture = installM3SendFixture()
  try {
    const baselineRows = structuredClone(fixture.rows)
    const baseline = await fixture.capture()
    assert.equal(baseline.status, 'ready')
    const observeWechat = () => zhilianTestHooks.mainObserveStableOutboundCard(
      fixture.conversationRef,
      'wechatInvite',
      null,
      baseline.serverSourceKeys,
      baseline.targetBindingToken,
    )
    const interview = {
      startsAt: 1_800_000_000_000,
      endsAt: 1_800_001_800_000,
      method: 'wechatVideo',
    }
    const observeInterview = () => zhilianTestHooks.mainObserveStableOutboundCard(
      fixture.conversationRef,
      'interviewInvite',
      interview,
      baseline.serverSourceKeys,
      baseline.targetBindingToken,
    )
    const reset = () => fixture.rows.splice(
      0,
      fixture.rows.length,
      ...structuredClone(baselineRows),
    )

    assert.deepEqual(await observeWechat(), {
      selected: true,
      matchingNewServerMessages: 0,
    })
    appendM3CardRow(fixture, {
      idServer: 'server-m5b-wechat-1',
      cardKind: 'wechatInvite',
    })
    const wechat = await observeWechat()
    assert.equal(wechat.selected, true)
    assert.equal(wechat.matchingNewServerMessages, 1)
    assert.equal(
      wechat.sourceKey,
      m3Hash('source-v1|server-m5b-wechat-1'),
      '换微信卡必须返回稳定服务端消息身份的 sourceKey',
    )
    assert.equal(
      wechat.contentHash,
      m3Hash('card\x1fwechatExchange'),
      '换微信卡 observer 必须返回 readThread 同口径 contentHash',
    )
    assert.equal(wechat.interview, undefined)

    appendM3CardRow(fixture, {
      idServer: 'server-m5b-wechat-2',
      cardKind: 'wechatInvite',
    })
    const ambiguousWechat = await observeWechat()
    assert.equal(ambiguousWechat.selected, true)
    assert.equal(ambiguousWechat.matchingNewServerMessages, 0, '严格 +2 不得形成换微信正证')
    assert.equal(ambiguousWechat.sourceKey, undefined)
    assert.equal(ambiguousWechat.contentHash, undefined)

    reset()
    appendM3CardRow(fixture, {
      idServer: 'server-m5b-interview-1',
      cardKind: 'interviewInvite',
    })
    const invite = await observeInterview()
    assert.equal(invite.selected, true)
    assert.equal(invite.matchingNewServerMessages, 1)
    assert.deepEqual(invite.interview, interview)
    assert.equal(
      invite.sourceKey,
      m3Hash('source-v1|server-m5b-interview-1'),
      '邀面卡必须返回稳定服务端消息身份的 sourceKey',
    )
    assert.equal(
      invite.contentHash,
      m3Hash([
        'card',
        'interviewInvite',
        String(interview.startsAt),
        String(interview.endsAt),
        interview.method,
      ].join('\x1f')),
      '邀面卡 contentHash 不得混入平台消息身份或私有卡片 ID',
    )

    appendM3CardRow(fixture, {
      idServer: 'server-m5b-interview-2',
      cardKind: 'interviewInvite',
    })
    assert.equal(
      (await observeInterview()).matchingNewServerMessages,
      0,
      '严格 +2 不得形成邀面正证',
    )
  } finally {
    fixture.restore()
  }
})

test('M5-B 卡片 observer 对错形态、缺服务端 id、错误时间与目标变化保持阴性', async () => {
  const fixture = installM3SendFixture()
  try {
    const baselineRows = structuredClone(fixture.rows)
    const baseline = await fixture.capture()
    assert.equal(baseline.status, 'ready')
    const interview = {
      startsAt: 1_800_000_000_000,
      endsAt: 1_800_001_800_000,
      method: 'wechatVideo',
    }
    const observe = (cardKind, expectedInterview = null) =>
      zhilianTestHooks.mainObserveStableOutboundCard(
        fixture.conversationRef,
        cardKind,
        expectedInterview,
        baseline.serverSourceKeys,
        baseline.targetBindingToken,
      )
    const reset = () => fixture.rows.splice(
      0,
      fixture.rows.length,
      ...structuredClone(baselineRows),
    )

    for (const [name, row] of [
      ['105 非 success', {
        idServer: 'server-m5b-bad-wechat-status',
        cardKind: 'wechatInvite',
        status: 'failed',
      }],
      ['105 不是招聘方发起', {
        idServer: 'server-m5b-bad-wechat-origin',
        cardKind: 'wechatInvite',
        overrides: { originType: 2 },
      }],
      ['105 非 custom 顶层', {
        idServer: 'server-m5b-bad-wechat-type',
        cardKind: 'wechatInvite',
        rawType: 105,
      }],
    ]) {
      reset()
      appendM3CardRow(fixture, row)
      const result = await observe('wechatInvite')
      assert.equal(result.selected, true, `${name}: 仍在目标会话`)
      assert.equal(result.matchingNewServerMessages, 0, `${name}: 不得形成正证`)
    }

    reset()
    appendM3CardRow(fixture, {
      idServer: '',
      cardKind: 'wechatInvite',
    })
    assert.equal(
      (await observe('wechatInvite')).matchingNewServerMessages,
      0,
      '缺稳定服务端 id 不得形成正证',
    )

    reset()
    appendM3CardRow(fixture, {
      idServer: 'server-m5b-wrong-time',
      cardKind: 'interviewInvite',
      overrides: { endTime: interview.endsAt + 60_000 },
    })
    assert.equal(
      (await observe('interviewInvite', interview)).matchingNewServerMessages,
      0,
      '邀面时间必须与命令精确一致',
    )

    reset()
    appendM3CardRow(fixture, {
      idServer: 'server-m5b-wrong-method',
      cardKind: 'interviewInvite',
      overrides: { interviewPlatform: 99 },
    })
    assert.equal(
      (await observe('interviewInvite', interview)).matchingNewServerMessages,
      0,
      '无法映射规范 method 的卡片不得形成邀面正证',
    )

    fixture.session.peerPartnerId = 'candidate-rebound'
    assert.equal(
      (await observe('interviewInvite', interview)).matchingNewServerMessages,
      0,
      '目标换绑不得认领卡片后置条件',
    )
    fixture.session.peerPartnerId = fixture.peerRef
    globalThis.location.href = 'https://rd6.zhaopin.com/app/im?sessionId=other'
    assert.deepEqual(await observe('interviewInvite', interview), {
      selected: false,
      matchingNewServerMessages: 0,
    })
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
        if (func.name === 'mainReadListDOMWindow') {
          selectCalls += 1
          return [{ result: {
            sessions: [{ conversationRef }],
            atBottom: false, moved: true, scrollHeight: 1_000, scrollTop: 0, unstable: false,
          } }]
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

test('M5-B 两类卡片外层流程各只过一次 barrier、一次 commit，阴性观察绝不补动作', async () => {
  const originalChrome = globalThis.chrome
  const originalSetTimeout = globalThis.setTimeout
  const fingerprint = '8'.repeat(64)
  const conversationRef = 'conversation-card-orchestration'
  const expectedTail = [{ direction: 'in', contentHash: '7'.repeat(64) }]
  const interview = {
    startsAt: 1_800_000_000_000,
    endsAt: 1_800_001_800_000,
    method: 'wechatVideo',
  }
  const baseline = {
    status: 'ready',
    stage: 'ready',
    serverSourceKeys: ['6'.repeat(64)],
    targetBindingToken: '5'.repeat(64),
  }
  const targetTabId = 191
  let observePositive = true
  let evaluatorFunction = null
  let preflightCalls = 0
  let commitCalls = 0
  let prepareCalls = 0
  let baselineCalls = 0
  let observerCalls = 0
  let closeModalCalls = 0
  const evaluatorArgs = []

  globalThis.setTimeout = (callback) => {
    queueMicrotask(callback)
    return 1
  }
  globalThis.chrome = {
    tabs: {
      async query() {
        return [{
          id: targetTabId,
          url: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`,
          status: 'complete',
          active: true,
          lastAccessed: Date.now(),
        }]
      },
      async get(id) {
        assert.equal(id, targetTabId)
        return {
          id: targetTabId,
          url: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`,
          status: 'complete',
          active: true,
        }
      },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ target, func, args }) {
        assert.equal(target.tabId, targetTabId)
        if (func.name === 'mainProbeZhilian') {
          return [{ result: {
            pageKind: 'im',
            loginState: 'in',
            principalFingerprint: fingerprint,
            imListVisible: true,
          } }]
        }
        if (func.name === 'mainCaptureSendBaseline') {
          baselineCalls += 1
          assert.deepEqual(args, [conversationRef, expectedTail])
          return [{ result: structuredClone(baseline) }]
        }
        if (func.name === 'mainPrepareInterviewEditor') {
          prepareCalls += 1
          assert.equal(args[0], conversationRef)
          assert.deepEqual(args[1], interview)
          assert.equal(args[2], fingerprint)
          return [{ result: {
            status: 'ready',
            prepared: {
              ...interview,
              dateValue: '2027-01-15',
              timeValue: '16:00',
              durationValue: '30分钟',
              methodValue: '微信视频',
            },
          } }]
        }
        if (func.name === 'mainSendCardOnce') {
          if (evaluatorFunction === null) evaluatorFunction = func
          else assert.strictEqual(
            func,
            evaluatorFunction,
            '两类卡片的 preflight/commit 必须注入字面同一份 evaluator',
          )
          evaluatorArgs.push(structuredClone(args))
          const phase = args.at(-1)
          assert.deepEqual(args.slice(7), [
            baseline.serverSourceKeys,
            baseline.targetBindingToken,
            phase,
          ])
          if (phase === 'preflight') {
            preflightCalls += 1
            return [{ result: { status: 'ready' } }]
          }
          assert.equal(phase, 'commit')
          commitCalls += 1
          return [{ result: { status: 'clicked' } }]
        }
        if (func.name === 'mainCloseInterviewSuccessModal') {
          closeModalCalls += 1
          assert.ok(commitCalls > 0, '成功弹窗清理只允许出现在 commit 之后')
          assert.deepEqual(args, [])
          return [{ result: { found: true, closed: true } }]
        }
        if (func.name === 'mainObserveStableOutboundCard') {
          observerCalls += 1
          const [observedConversation, cardKind, expectedInterview, baselineKeys, targetToken] = args
          assert.equal(observedConversation, conversationRef)
          assert.ok(['wechatInvite', 'interviewInvite'].includes(cardKind))
          assert.deepEqual(
            expectedInterview,
            cardKind === 'interviewInvite' ? interview : null,
          )
          assert.deepEqual(baselineKeys, baseline.serverSourceKeys)
          assert.equal(targetToken, baseline.targetBindingToken)
          if (!observePositive) {
            return [{ result: { selected: true, matchingNewServerMessages: 0 } }]
          }
          return [{ result: {
            selected: true,
            matchingNewServerMessages: 1,
            contentHash: cardKind === 'interviewInvite'
              ? m3Hash([
                  'card',
                  'interviewInvite',
                  String(interview.startsAt),
                  String(interview.endsAt),
                  interview.method,
                ].join('\x1f'))
              : m3Hash('card\x1fwechatExchange'),
            sourceKey: m3Hash(`source-v1|server-m5b-orchestration-${cardKind}`),
            ...(cardKind === 'interviewInvite' ? { interview } : {}),
          } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }

  const context = (suffix) => {
    const state = { barriers: 0 }
    return {
      state,
      value: {
        cmdMsgId: `card-orchestration-${suffix}`,
        deadlineMs: Date.now() + 60_000,
        irreversibleNotAfterMs: Date.now() + 60_000,
        commandContext: undefined,
        guards: undefined,
        signal: new AbortController().signal,
        async progress() {},
        checkpoint() {},
        async beforeSideEffect() { state.barriers += 1 },
      },
    }
  }
  try {
    const wechatContext = context('wechat-positive')
    const wechatBaselineStart = baselineCalls
    const wechat = await sendZhilianWechatInvite(
      { conversationRef },
      { expectedTail },
      wechatContext.value,
      fingerprint,
    )
    assert.equal(wechat.conversationRef, conversationRef)
    assert.match(wechat.contentHash, /^[0-9a-f]{64}$/u)
    assert.match(wechat.sourceKey, /^[0-9a-f]{64}$/u)
    assert.equal(wechatContext.state.barriers, 1)
    assert.equal(baselineCalls - wechatBaselineStart, 1)
    assert.equal(prepareCalls, 0, '换微信邀请不得打开邀面编辑器')
    assert.equal(closeModalCalls, 0, '换微信邀请不触碰邀面成功弹窗')

    const inviteContext = context('invite-positive')
    const inviteBaselineStart = baselineCalls
    const invite = await sendZhilianInviteCard(
      { conversationRef, interview },
      { expectedTail },
      inviteContext.value,
      fingerprint,
    )
    assert.equal(invite.conversationRef, conversationRef)
    assert.deepEqual(invite.interview, interview)
    assert.match(invite.contentHash, /^[0-9a-f]{64}$/u)
    assert.match(invite.sourceKey, /^[0-9a-f]{64}$/u)
    assert.equal(inviteContext.state.barriers, 1)
    assert.equal(baselineCalls - inviteBaselineStart, 1)
    assert.equal(prepareCalls, 1, '邀面编辑器只允许准备一次')
    assert.equal(closeModalCalls, 1, '邀面卡确认成功后必须尝试关闭成功弹窗一次')
    assert.equal(preflightCalls, 2)
    assert.equal(commitCalls, 2, '两条成功命令各只有一次最终动作')
    assert.equal(evaluatorArgs[0][1], 'wechatInvite')
    assert.equal(evaluatorArgs[1][1], 'wechatInvite')
    assert.equal(evaluatorArgs[2][1], 'interviewInvite')
    assert.equal(evaluatorArgs[3][1], 'interviewInvite')

    registerM3Primitives()
    const m3Capabilities = capabilities()
    assert.ok(m3Capabilities.includes(`${Primitive.ChatSendWechatInvite}@1`))
    assert.ok(m3Capabilities.includes(`${Primitive.ChatSendInviteCard}@1`))
    assert.ok(m3Capabilities.includes(`${Primitive.ChatAcceptWechat}@1`))
    assert.ok(m3Capabilities.includes(`${Primitive.ChatReadWechatExchangeOutcome}@1`))

    for (const [name, args, evidenceType] of [
      [
        Primitive.ChatSendWechatInvite,
        { conversationRef },
        'outboundWechatInviteObserved',
      ],
      [
        Primitive.ChatSendInviteCard,
        { conversationRef, interview },
        'outboundInterviewInviteObserved',
      ],
    ]) {
      const primitive = lookup(name)
      assert.ok(primitive, `${name} 必须注册生产 handler`)
      const handlerContext = context(`${name}-handler`)
      handlerContext.value.commandContext = {
        platform: 'zhilian',
        accountRef: 'account-card-orchestration',
        expectedPrincipalFingerprint: fingerprint,
      }
      handlerContext.value.guards = { expectedTail }
      const outcome = await primitive.handler(args, handlerContext.value)
      assert.equal(outcome.status, 'ok')
      assert.deepEqual(outcome.evidence, [{ type: evidenceType }])
      assert.deepEqual(validatePrimitiveResult(name, 1, {
        status: 'ok',
        data: outcome.data,
        evidence: outcome.evidence,
        ref: `validate-${name}`,
        execMs: 0,
        replayed: false,
      }), [])
      assert.equal(handlerContext.state.barriers, 1)
    }
    assert.equal(closeModalCalls, 2, '生产 handler 的邀面成功路径同样清理成功弹窗')

    observePositive = false
    const negativeContext = context('wechat-negative')
    const commitsBeforeNegative = commitCalls
    const observersBeforeNegative = observerCalls
    await assert.rejects(
      sendZhilianWechatInvite(
        { conversationRef },
        { expectedTail },
        negativeContext.value,
        fingerprint,
      ),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === ErrorCode.PostconditionUnconfirmed &&
        error.sideEffect === 'possible',
    )
    assert.equal(negativeContext.state.barriers, 1)
    assert.equal(commitCalls - commitsBeforeNegative, 1,
      '后置条件阴性也只允许一次 commit，observer 轮询不得补动作')
    assert.ok(observerCalls > observersBeforeNegative, '阴性路径必须实际执行验证读')
  } finally {
    globalThis.chrome = originalChrome
    globalThis.setTimeout = originalSetTimeout
  }
})

test('M5-B 微信接受外层只过一次 barrier、同一 evaluator 一次 commit，结果读保持 readonly', async () => {
  const originalChrome = globalThis.chrome
  const originalSetTimeout = globalThis.setTimeout
  const fingerprint = '4'.repeat(64)
  const conversationRef = 'conversation-wechat-accept-orchestration'
  const requestSourceKey = '3'.repeat(64)
  const exchangeSourceKey = '2'.repeat(64)
  const expectedTail = [{ direction: 'in', contentHash: '1'.repeat(64) }]
  const baseline = {
    status: 'ready',
    stage: 'ready',
    serverSourceKeys: [requestSourceKey],
    targetBindingToken: '0'.repeat(64),
  }
  const targetTabId = 192
  const delays = []
  let outcomePositive = true
  let surfaceAccepted = true
  let evaluatorFunction = null
  let preflightCalls = 0
  let commitCalls = 0
  let outcomeReads = 0

  globalThis.setTimeout = (callback, delay = 0) => {
    delays.push(delay)
    queueMicrotask(callback)
    return 1
  }
  globalThis.chrome = {
    tabs: {
      async query() {
        return [{
          id: targetTabId,
          url: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`,
          status: 'complete',
          active: true,
          lastAccessed: Date.now(),
        }]
      },
      async get(id) {
        assert.equal(id, targetTabId)
        return {
          id: targetTabId,
          url: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`,
          status: 'complete',
          active: true,
        }
      },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ target, func, args }) {
        assert.equal(target.tabId, targetTabId)
        if (func.name === 'mainProbeZhilian') {
          return [{ result: {
            pageKind: 'im',
            loginState: 'in',
            principalFingerprint: fingerprint,
            imListVisible: true,
          } }]
        }
        if (func.name === 'mainCaptureSendBaseline') {
          assert.deepEqual(args, [conversationRef, expectedTail])
          return [{ result: structuredClone(baseline) }]
        }
        if (func.name === 'mainSendCardOnce') {
          if (evaluatorFunction === null) evaluatorFunction = func
          else assert.strictEqual(func, evaluatorFunction,
            '微信接受的 preflight/commit 必须是字面同一 evaluator')
          assert.deepEqual(args.slice(0, 7), [
            conversationRef,
            'wechatAccept',
            null,
            requestSourceKey,
            expectedTail,
            fingerprint,
            args[6],
          ])
          assert.deepEqual(args.slice(7, 9), [
            baseline.serverSourceKeys,
            baseline.targetBindingToken,
          ])
          if (args[9] === 'preflight') {
            preflightCalls += 1
            return [{ result: { status: 'ready' } }]
          }
          assert.equal(args[9], 'commit')
          commitCalls += 1
          return [{ result: { status: 'clicked' } }]
        }
        if (func.name === 'mainReadWechatExchangeOutcome') {
          outcomeReads += 1
          assert.equal(args[0], conversationRef)
          assert.equal(args[1], requestSourceKey)
          if (args[2] === null) {
            assert.equal(args[3], null, 'readonly 结果读不得伪造发送基线')
          } else {
            assert.deepEqual(args.slice(2), [
              baseline.serverSourceKeys,
              baseline.targetBindingToken,
            ])
          }
          // surface 是成功正证（可见后置状态），confirmed/号只是可选加成。
          const surface = args[2] === null
            ? undefined
            : { surface: { pendingRequestCards: surfaceAccepted ? 0 : 1, copyWechatCards: surfaceAccepted ? 1 : 0 } }
          return [{ result: outcomePositive
            ? {
                confirmed: true,
                exchangeSourceKey,
                peerWechat: 'peer_wechat_fixture',
                ...surface,
              }
            : { confirmed: false, ...surface } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = (suffix) => {
    const state = { barriers: 0 }
    return {
      state,
      value: {
        cmdMsgId: `wechat-accept-${suffix}`,
        deadlineMs: Date.now() + 60_000,
        irreversibleNotAfterMs: Date.now() + 60_000,
        commandContext: undefined,
        guards: undefined,
        signal: new AbortController().signal,
        async progress() {},
        checkpoint() {},
        async beforeSideEffect() { state.barriers += 1 },
      },
    }
  }
  try {
    const acceptedContext = context('positive')
    const accepted = await acceptZhilianWechatRequest(
      { conversationRef, requestSourceKey },
      { expectedTail },
      acceptedContext.value,
      fingerprint,
    )
    assert.deepEqual(accepted, {
      conversationRef,
      requestSourceKey,
      exchangeSourceKey,
      peerWechat: 'peer_wechat_fixture',
      observedAt: accepted.observedAt,
    })
    assert.equal(acceptedContext.state.barriers, 1)
    assert.equal(preflightCalls, 1)
    assert.equal(commitCalls, 1)
    assert.ok(delays[0] >= 1_000 && delays[0] <= 1_500,
      '候选人可见接受动作前必须随机等待至少一秒')

    const readonlyContext = context('readonly')
    const readonly = await readZhilianWechatExchangeOutcome(
      { conversationRef, requestSourceKey },
      readonlyContext.value,
      fingerprint,
    )
    assert.equal(readonly.confirmed, true)
    assert.equal(readonly.exchangeSourceKey, exchangeSourceKey)
    assert.equal(readonly.peerWechat, 'peer_wechat_fixture')
    assert.equal(readonlyContext.state.barriers, 0, '专门结果读不得越过副作用 barrier')

    registerM3Primitives()
    const primitive = lookup(Primitive.ChatAcceptWechat)
    assert.ok(primitive)
    const handlerContext = context('handler')
    handlerContext.value.commandContext = {
      platform: 'zhilian',
      accountRef: 'account-wechat-accept',
      expectedPrincipalFingerprint: fingerprint,
    }
    handlerContext.value.guards = { expectedTail }
    const outcome = await primitive.handler(
      { conversationRef, requestSourceKey },
      handlerContext.value,
    )
    assert.equal(outcome.status, 'ok')
    assert.deepEqual(outcome.evidence, [{
      type: 'candidateWechatRequestAcceptedObserved',
    }])
    assert.deepEqual(validatePrimitiveResult(Primitive.ChatAcceptWechat, 1, {
      status: 'ok',
      data: outcome.data,
      evidence: outcome.evidence,
      ref: 'validate-wechat-accept',
      execMs: 0,
      replayed: false,
    }), [])

    // 可见后置状态成立但 259 未到：仍是成功，只是无号，号留给延迟收编。
    outcomePositive = false
    const noNumberContext = context('no-number')
    const commitsBeforeNoNumber = commitCalls
    const readsBeforeNoNumber = outcomeReads
    const noNumber = await acceptZhilianWechatRequest(
      { conversationRef, requestSourceKey },
      { expectedTail },
      noNumberContext.value,
      fingerprint,
    )
    assert.deepEqual(noNumber, {
      conversationRef,
      requestSourceKey,
      observedAt: noNumber.observedAt,
    }, '259 未到不推翻可见后置状态正证，data 里两个取号字段必须一并缺席')
    assert.deepEqual(validatePrimitiveResult(Primitive.ChatAcceptWechat, 1, {
      status: 'ok',
      data: noNumber,
      evidence: [{ type: 'candidateWechatRequestAcceptedObserved' }],
      ref: 'validate-wechat-accept-no-number',
      execMs: 0,
      replayed: false,
    }), [], '无号 data 必须符合契约')
    assert.equal(commitCalls - commitsBeforeNoNumber, 1, '无号成功也只允许一次 commit')
    assert.equal(outcomeReads - readsBeforeNoNumber, 21,
      '正证 1 次 + 取号加成 20 次，取号预算不扩大到第二次 click')

    // 可见后置状态始终不成立：未确认转人工，绝不补第二次同意。
    surfaceAccepted = false
    const negativeContext = context('negative')
    const commitsBefore = commitCalls
    const readsBefore = outcomeReads
    await assert.rejects(
      acceptZhilianWechatRequest(
        { conversationRef, requestSourceKey },
        { expectedTail },
        negativeContext.value,
        fingerprint,
      ),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === ErrorCode.PostconditionUnconfirmed &&
        error.sideEffect === 'possible',
    )
    assert.equal(commitCalls - commitsBefore, 1,
      '阴性观察后不得补第二次接受动作')
    assert.equal(outcomeReads - readsBefore, 40,
      '接受动作后置观察总预算固定为 40×250ms，不扩大到第二次 click')
  } finally {
    globalThis.chrome = originalChrome
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
        if (func.name === 'mainReadListDOMWindow') return [{ result: {
          sessions: [{ conversationRef }],
          atBottom: false, moved: true, scrollHeight: 1_000, scrollTop: 0, unstable: false,
        } }]
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

test('页面列表目标在 readThread 前离开窗口时只报无副作用 TARGET_NOT_FOUND', async () => {
  const originalChrome = globalThis.chrome
  const conversationRef = 'conversation-stale-page-window'
  let finderReason = 'target_not_found'
  let clickCalls = 0
  globalThis.chrome = {
    scripting: {
      async executeScript({ func }) {
        if (func.name === 'mainReadListDOMWindow') {
          if (finderReason === 'list_binding_unresolved') {
            throw new Error('list_binding_unresolved')
          }
          return [{ result: {
            sessions: [],
            atBottom: false, moved: true, scrollHeight: 1_000, scrollTop: 0, unstable: false,
          } }]
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
    cmdMsgId: 'stale-page-window', deadlineMs: Date.now() + 10_000,
    irreversibleNotAfterMs: Date.now() + 10_000,
    commandContext: undefined, guards: undefined,
    signal: new AbortController().signal,
    async progress() {}, checkpoint() {}, async beforeSideEffect() {
      throw new Error('陈旧页面目标不得越过副作用 barrier')
    },
  }
  try {
    await assert.rejects(
      zhilianTestHooks.ensureThreadRoute(
        { id: 95, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' },
        conversationRef,
        '7'.repeat(64),
        context,
      ),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === ErrorCode.TargetNotFound &&
        error.retryable === 'no' &&
        error.sideEffect === 'none',
    )
    assert.equal(clickCalls, 0)

    finderReason = 'list_binding_unresolved'
    await assert.rejects(
      zhilianTestHooks.ensureThreadRoute(
        { id: 95, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' },
        conversationRef,
        '7'.repeat(64),
        context,
      ),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === ErrorCode.ElementUnresolved &&
        error.retryable === 'manualOnly',
      '只有精确 target_not_found 可以降级，列表绑定异常仍须响亮停止',
    )
    assert.equal(clickCalls, 0)
  } finally {
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
        if (func.name === 'mainReadListDOMWindow') {
          await finderGate
          return [{ result: {
            sessions: [{ conversationRef }],
            atBottom: false, moved: true, scrollHeight: 1_000, scrollTop: 0, unstable: false,
          } }]
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
  globalThis.location = {
    href: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`,
  }
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

test('智联线程从当前 runtime 窗口读取且不调用 getSessions 全局扫描', async () => {
  const conversationRef = 'conversation-stable-page'
  let byIdsCalls = 0
  let pageCalls = 0
  const engine = {
    sessions: [{ sessionId: conversationRef, peerPartnerId: 'peer-stable', name: '脱敏候选人' }],
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
  assert.equal(pageCalls, 0)
})

test('智联线程目标只存在 getSessions 后续页时立即失败且不扫描', async () => {
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
  assert.match(page.__recruitHelperMainError, /resolve_session_initial_state:conversation_not_found/u)
  assert.equal(byIdsCalls, 0)
  assert.deepEqual(requestedPages, [])
  assert.equal(historyCalls, 0)
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
    },
  }
  const sentinel = await zhilianTestHooks.mainReadThreadPage('missing-conversation', 8, null)
  assert.match(sentinel.__recruitHelperMainError, /resolve_session_initial_state:conversation_not_found/u)

  globalThis.chrome = { scripting: { async executeScript() { return [{ result: sentinel }] } } }
  await assert.rejects(
    zhilianTestHooks.runMain(7, async () => ({ ok: true }), []),
    /read_thread_main_failed:resolve_session_initial_state:conversation_not_found/u,
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

test('智联线程页面 API 不响应时由 MAIN 本地截止响亮释放', async () => {
  const conversationRef = 'conversation-history-timeout'
  globalThis.location = { href: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}` }
  globalThis.document = { scripts: [] }
  globalThis.window = {
    $session: { staff: { staffId: 'staff-timeout' } },
    imEngine: {
      sessions: [{ sessionId: conversationRef, peerPartnerId: 'peer-timeout', name: '脱敏候选人' }],
      getHistoryMsgs() { return new Promise(() => {}) },
    },
  }
  const failure = await zhilianTestHooks.mainReadThreadPage(conversationRef, 8, null, 5)
  assert.match(failure.__recruitHelperMainError, /read_history_api:history_api_timeout/u)
})

test('readList 只交付当前可定位 DOM 窗口且不制造列表游标', async () => {
  const fingerprint = '9'.repeat(64)
  let domCalls = 0
  const rows = Array.from({ length: 8 }, (_unused, index) => ({
    conversationRef: `conversation-window-${index}`,
    peer: { displayName: `候选人${index}`, platformUserRef: `peer-${index}` },
    unreadCount: index,
    lastMessage: { direction: 'in', kind: 'text', textPreview: '新消息' },
    lastActivityTs: Date.now() - index,
  }))
  globalThis.chrome = {
    tabs: {
      async query() { return [{ id: 18, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' }] },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func }) {
        if (func.name === 'mainProbeZhilian') {
          return [{ result: {
            pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
          } }]
        }
        if (func.name === 'mainEnsureChatListFilter') {
          return [{ result: { status: 'ready', changed: false } }]
        }
        if (func.name === 'mainReadListDOMWindow') {
          domCalls += 1
          return [{ result: {
            sessions: rows,
            atBottom: false,
            moved: true,
            scrollHeight: 2_000,
            scrollTop: 0,
            unstable: false,
          } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = {
    cmdMsgId: 'list-window', deadlineMs: Date.now() + 10_000, commandContext: undefined,
    signal: new AbortController().signal,
    async progress() {}, checkpoint() {}, beforeSideEffect() {},
  }
  const page = await readZhilianList(
    { filter: 'all', move: 'reset', stopOlderThanDays: 8 },
    context,
    fingerprint,
  )
  assert.equal(domCalls, 1)
  assert.equal(page.sessions.length, 8)
  assert.equal(page.complete, false)
  assert.deepEqual(Object.keys(page).sort(), ['complete', 'sessions'])
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
    sourceKey: m3Hash(`source-v1|${key}`),
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

test('readThread 只去重 sourceKey 完全同义行，跨页语义冲突立即转人工', async () => {
  const hash = 'a'.repeat(64)
  const duplicateRef = 'conversation-source-key-duplicate'
  const duplicateHarness = installThreadReadHarness(duplicateRef, async () => ({
    messages: [
      threadFixtureMessage('same-platform-identity', hash, 'SAME'),
      threadFixtureMessage('same-platform-identity', hash, 'SAME'),
    ],
    reachedTop: true,
    cursor: null,
    peer: { displayName: '脱敏候选人' },
  }))
  const duplicate = await readZhilianThread({
    conversationRef: duplicateRef,
    window: { maxMessages: 2, anchorTail: [], deep: false },
  }, duplicateHarness.context().value, duplicateHarness.fingerprint)
  assert.equal(duplicate.messages.length, 1, '同 key+方向+hash 的重复观察只保留一行')

  const conflictRef = 'conversation-source-key-conflict'
  const cursor = { endTime: 100, lastMsgId: 'older-conflicting-observation' }
  const conflictKey = 'conflicting-platform-identity'
  const conflictHarness = installThreadReadHarness(conflictRef, async (_conversation, _limit, pageCursor) => {
    const message = threadFixtureMessage(conflictKey, hash, 'SAME')
    if (pageCursor === null) {
      return {
        messages: [message], reachedTop: false, cursor,
        peer: { displayName: '脱敏候选人' },
      }
    }
    return {
      messages: [{ ...message, direction: 'out' }], reachedTop: true, cursor: null,
      peer: { displayName: '脱敏候选人' },
    }
  })
  const opaqueKey = m3Hash(`source-v1|${conflictKey}`)
  await assert.rejects(
    readZhilianThread({
      conversationRef: conflictRef,
      window: { maxMessages: 2, anchorTail: [], deep: true },
    }, conflictHarness.context().value, conflictHarness.fingerprint),
    (error) => {
      assert.ok(error instanceof ZhilianPlatformError)
      assert.equal(error.code, 'ELEMENT_UNRESOLVED')
      assert.equal(error.retryable, 'manualOnly')
      assert.equal(error.sideEffect, 'possible', '冲突在 intrusive 读已发生后发现，不伪称零读回执')
      assert.equal(error.message.includes(opaqueKey), false, '错误不得泄露 sourceKey 值')
      return true
    },
  )
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
  assert.deepEqual(result.messages.map((message) => message.sourceKey), [
    m3Hash('source-v1|adoption-older'),
    m3Hash('source-v1|adoption-newer'),
  ], 'chat.readThread 不得再剥掉手内已计算的 sourceKey')
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

function installThreadRouteHarness(conversationRef, {
  selected = false,
  readBehavior,
  routeAfterRead,
  routeReadyAfterClick = true,
} = {}) {
  const fingerprint = '7'.repeat(64)
  let currentURL = `https://rd6.zhaopin.com/app/im${selected ? `?sessionId=${conversationRef}` : ''}`
  const state = { barriers: 0, clicks: 0, finds: 0, reads: 0, events: [] }
  const page = {
    messages: [threadFixtureMessage('route-message', 'a'.repeat(64), '合成消息')],
    reachedTop: true,
    cursor: null,
    peer: { displayName: '脱敏候选人' },
  }
  globalThis.chrome = {
    tabs: {
      async query() { return [{ id: 71, url: currentURL, status: 'complete' }] },
      async get() { return { id: 71, url: currentURL, status: 'complete' } },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func, args }) {
        if (func.name === 'mainProbeZhilian') {
          return [{ result: {
            pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
          } }]
        }
        if (func.name === 'mainEnsureChatListFilter') {
          return [{ result: { status: 'ready', changed: false } }]
        }
        if (func.name === 'mainReadListDOMWindow') {
          state.finds += 1
          state.events.push('find')
          return [{ result: {
            sessions: [{ conversationRef }],
            atBottom: false, moved: true, scrollHeight: 1_000, scrollTop: 0, unstable: false,
          } }]
        }
        if (func.name === 'mainClickConversationOnce') {
          state.clicks += 1
          state.events.push('click')
          if (routeReadyAfterClick) {
            currentURL = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`
          }
          return [{ result: { status: 'clicked' } }]
        }
        if (func.name === 'mainReadThreadPage') {
          state.reads += 1
          state.events.push('read')
          const result = readBehavior ? await readBehavior(...args) : page
          if (routeAfterRead) currentURL = routeAfterRead
          return [{ result }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  return {
    fingerprint,
    state,
    context: {
      cmdMsgId: 'thread-route',
      deadlineMs: Date.now() + 10_000,
      irreversibleNotAfterMs: Date.now() + 10_000,
      commandContext: undefined,
      signal: new AbortController().signal,
      async progress() {},
      checkpoint() {},
      async beforeSideEffect() {
        state.barriers += 1
        state.events.push('barrier')
      },
    },
  }
}

test('ensureZhilianIM 复用 canonical 推荐页并在同一标签导航到 IM', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = 'f'.repeat(64)
  const tab = {
    id: 70,
    url: 'https://rd6.zhaopin.com/app/recommend',
    status: 'complete',
    active: true,
  }
  const created = []
  const updated = []
  try {
    globalThis.chrome = {
      tabs: {
        async query() { return [{ ...tab }] },
        async create(options) {
          created.push(options)
          throw new Error('已有 canonical 智联标签时不得新建')
        },
        async update(id, options) {
          assert.equal(id, tab.id)
          updated.push({ id, options })
          tab.url = options.url
          return { ...tab }
        },
        async get(id) {
          assert.equal(id, tab.id)
          return { ...tab }
        },
        async sendMessage() { return { ok: true } },
      },
      scripting: {
        async executeScript({ target }) {
          assert.equal(target.tabId, tab.id)
          return [{
            result: {
              pageKind: 'im',
              loginState: 'in',
              principalFingerprint: fingerprint,
              imListVisible: true,
            },
          }]
        },
      },
    }
    const result = await ensureZhilianIM({
      deadlineMs: Date.now() + 10_000,
      irreversibleNotAfterMs: Date.now() + 10_000,
      signal: new AbortController().signal,
      async progress() {},
      checkpoint() {},
    }, fingerprint)

    assert.equal(result.ready, true)
    assert.equal(result.createdTab, false)
    assert.deepEqual(created, [])
    assert.deepEqual(updated, [{
      id: tab.id,
      options: { url: 'https://rd6.zhaopin.com/app/im', active: true },
    }])
    assert.equal(tab.url, 'https://rd6.zhaopin.com/app/im')
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('ensureZhilianIM 双页现场优先把唯一推荐页交接到 IM', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = 'e'.repeat(64)
  const recommendTab = {
    id: 72,
    url: 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-job',
    status: 'complete',
    active: false,
    lastAccessed: 100,
    windowId: 1,
  }
  const existingIMTab = {
    id: 73,
    url: 'https://rd6.zhaopin.com/app/im',
    status: 'complete',
    active: true,
    lastAccessed: 200,
    windowId: 1,
  }
  const updated = []
  const probed = []
  try {
    globalThis.chrome = {
      tabs: {
        async query() {
          return [{ ...existingIMTab }, { ...recommendTab }]
        },
        async create() {
          throw new Error('双页现场不得新建第三张智联标签')
        },
        async update(id, options) {
          assert.equal(id, recommendTab.id, '应接管刚完成漏斗的推荐页，而不是复用旧 IM 页')
          updated.push({ id, options })
          recommendTab.url = options.url
          if (options.active) {
            recommendTab.active = true
            existingIMTab.active = false
          }
          return { ...recommendTab }
        },
        async get(id) {
          assert.equal(id, recommendTab.id)
          return { ...recommendTab }
        },
        async sendMessage(id) {
          assert.ok(id === recommendTab.id || id === existingIMTab.id)
          return { ok: true }
        },
      },
      scripting: {
        async executeScript({ target }) {
          assert.equal(target.tabId, recommendTab.id)
          probed.push(target.tabId)
          return [{
            result: {
              pageKind: 'im',
              loginState: 'in',
              principalFingerprint: fingerprint,
              imListVisible: true,
            },
          }]
        },
      },
    }

    const result = await ensureZhilianIM({
      deadlineMs: Date.now() + 10_000,
      irreversibleNotAfterMs: Date.now() + 10_000,
      signal: new AbortController().signal,
      async progress() {},
      checkpoint() {},
    }, fingerprint)

    assert.deepEqual(result, { ready: true, loginState: 'in', createdTab: false })
    assert.deepEqual(updated, [{
      id: recommendTab.id,
      options: { url: 'https://rd6.zhaopin.com/app/im', active: true },
    }])
    assert.deepEqual(probed, [recommendTab.id])
    assert.equal((await canonicalZhilianTab())?.id, recommendTab.id,
      '交接完成后后续 canonical 读取仍应选中本轮工作页')
    assert.equal(existingIMTab.url, 'https://rd6.zhaopin.com/app/im',
      '既有 IM 页不是本轮工作页，不应被导航或关闭')
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('identifyCurrentConversation 只读唯一 IM URL，首页无会话或多 IM 均失败', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = 'f'.repeat(64)
  const session = 'a'.repeat(32)
  let tabs = [{ id: 70, url: `https://rd6.zhaopin.com/app/im?sessionId=${session}`, status: 'complete' }]
  let probes = 0
  try {
    globalThis.chrome = {
      tabs: {
        async query() { return tabs },
        async get(id) { return { ...tabs.find((tab) => tab.id === id) } },
        async sendMessage() { return { ok: true } },
      },
      scripting: {
        async executeScript({ func }) {
          assert.equal(func.name, 'mainProbeZhilian')
          probes += 1
          return [{ result: {
            pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
          } }]
        },
      },
    }
    const found = await identifyZhilianCurrentConversation(fingerprint)
    assert.equal(found.conversationRef, session)
    assert.ok(Number.isSafeInteger(found.observedAt))

    tabs = [{ id: 70, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' }]
    await assert.rejects(
      identifyZhilianCurrentConversation(fingerprint),
      (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.ElementUnresolved,
    )

    tabs = [
      { id: 70, url: `https://rd6.zhaopin.com/app/im?sessionId=${session}`, status: 'complete' },
      { id: 71, url: `https://rd6.zhaopin.com/app/im?sessionId=${'b'.repeat(32)}`, status: 'complete' },
    ]
    await assert.rejects(
      identifyZhilianCurrentConversation(fingerprint),
      (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.ElementUnresolved,
    )
    assert.equal(probes, 3, '多 IM 应在读取任何页面内部状态前直接拒绝')

    tabs = [{ id: 70, url: `https://rd6.zhaopin.com/app/im?sessionId=${session}`, status: 'complete' }]
    let getCount = 0
    globalThis.chrome.tabs.get = async (id) => {
      getCount += 1
      const tab = { ...tabs.find((item) => item.id === id) }
      if (getCount >= 2) {
        tab.url = `https://rd6.zhaopin.com/app/im?sessionId=${'c'.repeat(32)}`
      }
      return tab
    }
    await assert.rejects(
      identifyZhilianCurrentConversation(fingerprint),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === ErrorCode.UserActive &&
        error.sideEffect === 'none',
      '只读识别期间路由漂移不得虚报可能产生读回执',
    )
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('readThread 从基础路由唯一切到目标后只消费一次 barrier 再读取一次', async () => {
  const conversationRef = 'conversation-route-open'
  const harness = installThreadRouteHarness(conversationRef)
  const result = await readZhilianThread({
    conversationRef,
    window: { maxMessages: 1, anchorTail: [], deep: false },
  }, harness.context, harness.fingerprint)

  assert.equal(result.messages.length, 1)
  assert.equal(harness.state.barriers, 1)
  assert.equal(harness.state.finds, 1)
  assert.equal(harness.state.clicks, 1)
  assert.equal(harness.state.reads, 1)
  assert.deepEqual(harness.state.events, ['find', 'barrier', 'click', 'read'])
})

test('readThread requireCurrent 不定位会话并在读取后路由漂移时失败', async () => {
  const conversationRef = 'conversation-current-only'
  const ready = installThreadRouteHarness(conversationRef, { selected: true })
  await readZhilianThread({
    conversationRef,
    requireCurrent: true,
    window: { maxMessages: 1, anchorTail: [], deep: false },
  }, ready.context, ready.fingerprint)
  assert.deepEqual(ready.state.events, ['barrier', 'read'])
  assert.equal(ready.state.finds, 0)
  assert.equal(ready.state.clicks, 0)

  const drifted = installThreadRouteHarness(conversationRef, {
    selected: true,
    routeAfterRead: 'https://rd6.zhaopin.com/app/im?sessionId=another-conversation',
  })
  await assert.rejects(
    readZhilianThread({
      conversationRef,
      requireCurrent: true,
      window: { maxMessages: 1, anchorTail: [], deep: false },
    }, drifted.context, drifted.fingerprint),
    (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.UserActive,
  )
  assert.deepEqual(drifted.state.events, ['barrier', 'read'])
  assert.equal(drifted.state.finds, 0)
  assert.equal(drifted.state.clicks, 0)
})

test('readThread 已在目标路由时不定位不点击并在 history 前消费唯一 barrier', async () => {
  const conversationRef = 'conversation-route-ready'
  const harness = installThreadRouteHarness(conversationRef, { selected: true })
  await readZhilianThread({
    conversationRef,
    window: { maxMessages: 1, anchorTail: [], deep: false },
  }, harness.context, harness.fingerprint)

  assert.equal(harness.state.barriers, 1)
  assert.equal(harness.state.finds, 0)
  assert.equal(harness.state.clicks, 0)
  assert.equal(harness.state.reads, 1)
  assert.deepEqual(harness.state.events, ['barrier', 'read'])
})

test('readThread 非法 opaque cursor 在定位、barrier、点击与读取前拒绝', async () => {
  const conversationRef = 'conversation-route-invalid-cursor'
  const harness = installThreadRouteHarness(conversationRef)
  const cursor = zhilianTestHooks.encodeCursor({
    v: 1,
    kind: 'thread',
    mode: 'api',
    binding: 'b'.repeat(64),
    endTime: 100,
    lastMsgId: 'message-1',
  })
  await assert.rejects(
    readZhilianThread({
      conversationRef,
      cursor,
      window: { maxMessages: 1, anchorTail: [], deep: false },
    }, harness.context, harness.fingerprint),
    (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.CursorInvalid,
  )

  assert.deepEqual(harness.state, { barriers: 0, clicks: 0, finds: 0, reads: 0, events: [] })
})

test('readThread 切到目标后 history 失败不二次 barrier、不重切也不重读', async () => {
  const conversationRef = 'conversation-route-read-failed'
  const harness = installThreadRouteHarness(conversationRef, {
    async readBehavior() { throw new Error('page execution lost') },
  })
  await assert.rejects(
    readZhilianThread({
      conversationRef,
      window: { maxMessages: 1, anchorTail: [], deep: false },
    }, harness.context, harness.fingerprint),
    (error) => error instanceof ZhilianPlatformError &&
      error.code === ErrorCode.CtxLostDuringExec && error.sideEffect === 'possible',
  )

  assert.equal(harness.state.barriers, 1)
  assert.equal(harness.state.finds, 1)
  assert.equal(harness.state.clicks, 1)
  assert.equal(harness.state.reads, 1)
  assert.deepEqual(harness.state.events, ['find', 'barrier', 'click', 'read'])
})

test('readThread 已点击但目标 route 未就绪时如实返回 possible 并禁止自动重派', async () => {
  const originalSetTimeout = globalThis.setTimeout
  globalThis.setTimeout = (callback) => {
    callback()
    return 0
  }
  const conversationRef = 'conversation-route-timeout'
  const harness = installThreadRouteHarness(conversationRef, { routeReadyAfterClick: false })
  try {
    await assert.rejects(
      readZhilianThread({
        conversationRef,
        window: { maxMessages: 1, anchorTail: [], deep: false },
      }, harness.context, harness.fingerprint),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === ErrorCode.CtxLostDuringExec &&
        error.retryable === Retryable.ManualOnly &&
        error.sideEffect === 'possible',
    )
  } finally {
    globalThis.setTimeout = originalSetTimeout
  }

  assert.equal(harness.state.barriers, 1)
  assert.equal(harness.state.finds, 1)
  assert.equal(harness.state.clicks, 1)
  assert.equal(harness.state.reads, 0)
  assert.deepEqual(harness.state.events, ['find', 'barrier', 'click'])
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
  let currentThreadURL = 'https://rd6.zhaopin.com/app/im?sessionId=conversation-1'
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
  assert.equal(updateCalls, 0, '已在目标会话时不得导航或切换真人页面')
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

test('readList 走无游标 DOM 窗口，reset 回顶且 next 原样交付跨窗重复项', async () => {
  const fingerprint = 'e'.repeat(64)
  const now = Date.now()
  const first = {
    conversationRef: 'window-a',
    peer: { displayName: '候选人甲', platformUserRef: 'peer-a' },
    unreadCount: 1,
    lastMessage: { direction: 'in', kind: 'text', textPreview: '新消息' },
    lastActivityTs: now,
  }
  const repeated = {
    conversationRef: 'window-b',
    peer: { displayName: '候选人乙', platformUserRef: 'peer-b' },
    unreadCount: 0,
    lastMessage: { direction: 'out', kind: 'text', textPreview: '旧消息' },
    lastActivityTs: now - 1_000,
  }
  const last = {
    conversationRef: 'window-c',
    peer: { displayName: '候选人丙', platformUserRef: 'peer-c' },
    unreadCount: 0,
    lastMessage: { direction: 'in', kind: 'text', textPreview: '再问一下' },
    lastActivityTs: now - 2_000,
  }
  const resetPage = {
    sessions: [first, repeated],
    atBottom: false,
    moved: true,
    scrollHeight: 2_000,
    scrollTop: 0,
    unstable: false,
  }
  const nextPage = {
    ...resetPage,
    sessions: [repeated, last],
    atBottom: true,
    scrollTop: 700,
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
        if (func.name === 'mainEnsureChatListFilter') {
          return [{ result: { status: 'ready', changed: false } }]
        }
        if (func.name === 'mainReadListDOMWindow') {
          domCalls.push(args)
          return [{ result: args[0] ? nextPage : resetPage }]
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
  const reset = await readZhilianList(
    { filter: 'all', move: 'reset', stopOlderThanDays: 8 },
    context,
    fingerprint,
  )
  assert.equal(reset.complete, false)
  assert.deepEqual(reset.sessions.map((item) => item.conversationRef), ['window-a', 'window-b'])
  assert.deepEqual(domCalls.at(-1), [false, true], 'reset 必须回到顶部后读取')

  const next = await readZhilianList(
    { filter: 'all', move: 'next', stopOlderThanDays: 8 },
    context,
    fingerprint,
  )
  assert.equal(next.complete, true)
  assert.deepEqual(next.sessions.map((item) => item.conversationRef), ['window-b', 'window-c'],
    '跨窗重复由脑按指纹跳过，手必须原样交付')
  assert.deepEqual(domCalls.at(-1), [true, false], 'next 只向下移动，不复位顶部')
})

test('chat.readList 真实覆盖全部职位与未读开关，未读轮不套年龄截止', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = '6'.repeat(64)
  const now = Date.now()
  let activeFilter = 'all'
  let barriers = 0
  const filterCalls = []
  const oldUnread = {
    conversationRef: 'conversation-old-unread',
    peer: { displayName: '候选人甲', platformUserRef: 'peer-old-unread' },
    unreadCount: 1,
    lastMessage: { direction: 'in', kind: 'text', textPreview: '旧未读' },
    lastActivityTs: now - 60 * 86_400_000,
  }
  globalThis.chrome = {
    tabs: {
      async query() { return [{ id: 108, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' }] },
      async get() { return { id: 108, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' } },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func, args }) {
        if (func.name === 'mainProbeZhilian') {
          return [{ result: {
            pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
          } }]
        }
        if (func.name === 'mainEnsureChatListFilter') {
          filterCalls.push(args)
          const target = args[0] ? 'unread' : 'all'
          if (activeFilter === target) return [{ result: { status: 'ready', changed: false } }]
          if (!args[1]) return [{ result: { status: 'needs_action' } }]
          activeFilter = target
          return [{ result: { status: 'ready', changed: true } }]
        }
        if (func.name === 'mainReadListDOMWindow') {
          return [{ result: {
            sessions: [oldUnread],
            atBottom: true,
            moved: true,
            scrollHeight: 1_000,
            scrollTop: 0,
            unstable: false,
          } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = {
    cmdMsgId: 'list-real-filter', deadlineMs: now + 60_000,
    irreversibleNotAfterMs: now + 60_000,
    commandContext: undefined,
    signal: new AbortController().signal,
    async progress() {}, checkpoint() {}, async beforeSideEffect() { barriers += 1 },
  }
  try {
    const unread = await readZhilianList({ filter: 'unread', move: 'reset' }, context, fingerprint)
    assert.deepEqual(unread.sessions.map((item) => item.conversationRef), ['conversation-old-unread'],
      '未读轮不得把 60 天前会话套进普通 8 天截止')
    assert.equal(unread.complete, true)
    assert.deepEqual(filterCalls.slice(0, 2), [[true, false], [true, true]])
    assert.equal(barriers, 1)

    const callsBeforeMismatchedNext = filterCalls.length
    await assert.rejects(
      readZhilianList(
        { filter: 'all', move: 'next', stopOlderThanDays: 8 },
        context,
        fingerprint,
      ),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === ErrorCode.ElementUnresolved,
      'next 遇到筛选变化必须要求脑以 reset 重建窗口，不能在手内切换筛选',
    )
    assert.deepEqual(filterCalls.slice(callsBeforeMismatchedNext), [[false, false]])
    assert.equal(barriers, 1, '被拒绝的 next 不得跨越页面动作栅栏')

    const allFilterStart = filterCalls.length
    const all = await readZhilianList(
      { filter: 'all', move: 'reset', stopOlderThanDays: 8 },
      context,
      fingerprint,
    )
    assert.deepEqual(all.sessions, [], '普通轮仍按 8 天截止')
    assert.equal(all.complete, true)
    assert.deepEqual(filterCalls.slice(allFilterStart), [[false, false], [false, true]])
    assert.equal(barriers, 2)

    await assert.rejects(
      readZhilianList({ filter: 'unread', move: 'reset', stopOlderThanDays: 8 }, context, fingerprint),
      (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.GuardFailed,
      'unread 携带年龄截止必须在任何页面交互前拒绝',
    )

    activeFilter = 'unread'
    const badUnread = { ...oldUnread, unreadCount: 0 }
    globalThis.chrome.scripting.executeScript = async ({ func }) => {
      if (func.name === 'mainProbeZhilian') return [{ result: {
        pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
      } }]
      if (func.name === 'mainEnsureChatListFilter') {
        return [{ result: { status: 'ready', changed: false } }]
      }
      if (func.name === 'mainReadListDOMWindow') return [{ result: {
        sessions: [badUnread], atBottom: true, moved: true,
        scrollHeight: 1_000, scrollTop: 0, unstable: false,
      } }]
      throw new Error(`unexpected MAIN function ${func.name}`)
    }
    const mixedUnread = await readZhilianList(
      { filter: 'unread', move: 'reset' },
      context,
      fingerprint,
    )
    assert.equal(mixedUnread.complete, true)
    assert.equal(mixedUnread.sessions.length, 1)
    assert.equal(mixedUnread.sessions[0].conversationRef, badUnread.conversationRef)
    assert.equal(mixedUnread.sessions[0].unreadCount, 0,
      '未读视图里的瞬时零标记必须如实交给脑，不能整页失败或在手内静默过滤')
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('IM 列表筛选 evaluator 只点差异并用标准 checked 回读', async () => {
  const original = {
    document: globalThis.document,
    location: globalThis.location,
    getComputedStyle: globalThis.getComputedStyle,
    setTimeout: globalThis.setTimeout,
  }
  let popupOpen = false
  let triggerClicks = 0
  let optionClicks = 0
  let inputClicks = 0
  const scheduled = []
  const interactionOrder = []
  const label = { textContent: '具体职位', getClientRects() { return [{}] } }
  const input = {
    type: 'checkbox',
    disabled: false,
    checked: false,
    click() { inputClicks += 1; interactionOrder.push('input'); this.checked = !this.checked },
  }
  const wrapper = {
    textContent: '未读',
    getClientRects() { return [{}] },
    querySelectorAll(selector) { return selector === 'input[type="checkbox"]' ? [input] : [] },
  }
  const trigger = {
    getClientRects() { return [{}] },
    click() { triggerClicks += 1; interactionOrder.push('trigger'); popupOpen = true },
  }
  const option = {
    textContent: '全部职位',
    getClientRects() { return [{}] },
    click() {
      optionClicks += 1
      interactionOrder.push('option')
      label.textContent = '全部职位'
      popupOpen = false
    },
  }
  try {
    globalThis.location = { href: 'https://rd6.zhaopin.com/app/im' }
    globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
    globalThis.setTimeout = (callback, delay) => {
      scheduled.push(delay)
      if (delay >= 1_000) interactionOrder.push('wait')
      queueMicrotask(callback)
      return 1
    }
    globalThis.document = {
      querySelectorAll(selector) {
        if (selector === '.app-job-selector') return [trigger]
        if (selector === '.app-job-selector .im-job-filter__label') return [label]
        if (selector === '.side-panel-header__checkbox') return [wrapper]
        if (selector.includes('.app-job-selector-item')) return popupOpen ? [option] : []
        return []
      },
    }
    const preflight = await zhilianTestHooks.mainEnsureChatListFilter(true, false)
    assert.deepEqual(preflight, { status: 'needs_action' })
    assert.equal(triggerClicks + optionClicks + inputClicks, 0)

    const applied = await zhilianTestHooks.mainEnsureChatListFilter(true, true)
    assert.deepEqual(applied, { status: 'ready', changed: true })
    assert.deepEqual([triggerClicks, optionClicks, inputClicks], [1, 1, 1])
    assert.equal(input.checked, true)
    assert.deepEqual(interactionOrder, [
      'wait', 'trigger', 'wait', 'option', 'wait', 'input', 'wait',
    ], '首个 click 前及每个相邻可见交互之间都必须有 1s+抖动')
    assert.ok(scheduled.filter((delay) => delay >= 1_000 && delay <= 1_400).length >= 4)

    interactionOrder.length = 0
    const closed = await zhilianTestHooks.mainEnsureChatListFilter(false, true)
    assert.deepEqual(closed, { status: 'ready', changed: true })
    assert.deepEqual(interactionOrder, ['wait', 'input', 'wait'],
      '已经是全部职位、只需切换未读时，首个 checkbox click 前同样必须等待')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('chat.openConversation 只点 fresh 未读目标一次并以路由和行离开双读收束', async () => {
  const originalChrome = globalThis.chrome
  const originalSetTimeout = globalThis.setTimeout
  const fingerprint = '5'.repeat(64)
  const conversationRef = 'conversation-unread-open'
  let currentURL = 'https://rd6.zhaopin.com/app/im?sessionId=previous-conversation'
  let clickCalls = 0
  let findCalls = 0
  let barriers = 0
  globalThis.setTimeout = (callback) => {
    queueMicrotask(callback)
    return 1
  }
  globalThis.chrome = {
    tabs: {
      async query() {
        return [{ id: 109, url: currentURL, status: 'complete', active: true }]
      },
      async get() {
        return { id: 109, url: currentURL, status: 'complete', active: true }
      },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func }) {
        if (func.name === 'mainProbeZhilian') return [{ result: {
          pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
        } }]
        if (func.name === 'mainEnsureChatListFilter') {
          return [{ result: { status: 'ready', changed: false } }]
        }
        if (func.name === 'mainReadListDOMWindow') return [{ result: {
          sessions: [
            {
              conversationRef: 'conversation-transient-zero',
              peer: { displayName: '候选人乙', platformUserRef: 'peer-zero' },
              unreadCount: 0,
              lastMessage: { direction: 'out', kind: 'text', textPreview: '已读消息' },
              lastActivityTs: Date.now(),
            },
            {
              conversationRef,
              peer: { displayName: '候选人甲', platformUserRef: 'peer-open' },
              unreadCount: 0,
              lastMessage: { direction: 'in', kind: 'text', textPreview: '未读消息' },
              lastActivityTs: Date.now(),
            },
          ],
          atBottom: false, moved: true, scrollHeight: 1_000, scrollTop: 0, unstable: false,
        } }]
        if (func.name === 'mainClickConversationOnce') {
          clickCalls += 1
          assert.equal(barriers, 1, '唯一 click 必须在取消安全点之后')
          currentURL = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`
          return [{ result: { status: 'clicked' } }]
        }
        if (func.name === 'mainFindConversation') {
          findCalls += 1
          return [{ result: { status: 'failed', reason: 'target_not_found' } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = {
    cmdMsgId: 'open-unread', deadlineMs: Date.now() + 60_000,
    irreversibleNotAfterMs: Date.now() + 60_000,
    commandContext: undefined,
    signal: new AbortController().signal,
    async progress() {}, checkpoint() {}, async beforeSideEffect() { barriers += 1 },
  }
  try {
    const result = await openZhilianConversation(
      { conversationRef },
      context,
      fingerprint,
    )
    assert.equal(result.conversationRef, conversationRef)
    assert.ok(result.observedAt > 0)
    assert.equal(clickCalls, 1,
      '公开未读筛选已确认且目标唯一时，低保真行级零值不得阻断唯一打开动作')
    assert.equal(barriers, 1)
    assert.equal(findCalls, 2, '行离开必须连续双读，不能用单个瞬时空窗宣告成功')
  } finally {
    globalThis.chrome = originalChrome
    globalThis.setTimeout = originalSetTimeout
  }
})

test('chat.openConversation 筛选未就绪时零 click，点击后未读不收敛只报 possible', async () => {
  const originalChrome = globalThis.chrome
  const originalSetTimeout = globalThis.setTimeout
  const fingerprint = '4'.repeat(64)
  const conversationRef = 'conversation-unread-pending'
  let currentURL = 'https://rd6.zhaopin.com/app/im?sessionId=previous-conversation'
  let filterReady = false
  let clickCalls = 0
  globalThis.setTimeout = (callback) => {
    queueMicrotask(callback)
    return 1
  }
  globalThis.chrome = {
    tabs: {
      async query() { return [{ id: 110, url: currentURL, status: 'complete' }] },
      async get() { return { id: 110, url: currentURL, status: 'complete' } },
      async sendMessage() { return { ok: true } },
    },
    scripting: {
      async executeScript({ func }) {
        if (func.name === 'mainProbeZhilian') return [{ result: {
          pageKind: 'im', loginState: 'in', principalFingerprint: fingerprint, imListVisible: true,
        } }]
        if (func.name === 'mainEnsureChatListFilter') {
          return [{ result: filterReady
            ? { status: 'ready', changed: false }
            : { status: 'needs_action' } }]
        }
        if (func.name === 'mainReadListDOMWindow') return [{ result: {
          sessions: [{
            conversationRef,
            peer: { displayName: '候选人乙', platformUserRef: 'peer-pending' },
            unreadCount: 1,
            lastMessage: { direction: 'in', kind: 'text', textPreview: '未读消息' },
            lastActivityTs: Date.now(),
          }],
          atBottom: false, moved: true, scrollHeight: 1_000, scrollTop: 0, unstable: false,
        } }]
        if (func.name === 'mainClickConversationOnce') {
          clickCalls += 1
          currentURL = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`
          return [{ result: { status: 'clicked' } }]
        }
        if (func.name === 'mainFindConversation') {
          return [{ result: { status: 'found', unreadMarkerCleared: false } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = {
    cmdMsgId: 'open-unread-pending', deadlineMs: Date.now() + 60_000,
    irreversibleNotAfterMs: Date.now() + 60_000,
    commandContext: undefined,
    signal: new AbortController().signal,
    async progress() {}, checkpoint() {}, async beforeSideEffect() {},
  }
  try {
    await assert.rejects(
      openZhilianConversation({ conversationRef }, context, fingerprint),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === ErrorCode.GuardFailed && error.sideEffect === 'none',
    )
    assert.equal(clickCalls, 0)

    filterReady = true
    currentURL = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`
    await assert.rejects(
      openZhilianConversation({ conversationRef }, context, fingerprint),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === ErrorCode.TargetNotFound &&
        error.retryable === 'no' &&
        error.sideEffect === 'none',
      '已经打开的目标不能伪造成 ok，也不能升级为账号级人工停机',
    )
    assert.equal(clickCalls, 0)

    currentURL = 'https://rd6.zhaopin.com/app/im?sessionId=previous-conversation'
    await assert.rejects(
      openZhilianConversation({ conversationRef }, context, fingerprint),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === ErrorCode.PostconditionUnconfirmed &&
        error.sideEffect === 'possible',
    )
    assert.equal(clickCalls, 1, '未读结果阴性也不得补第二次 click')
  } finally {
    globalThis.chrome = originalChrome
    globalThis.setTimeout = originalSetTimeout
  }
})

test('readList MAIN 内部异常保留脱敏阶段且不退化为空结果', async () => {
  const original = {
    chrome: globalThis.chrome,
    document: globalThis.document,
  }
  try {
    globalThis.document = {
      querySelector() { return null },
    }
    const sentinel = await zhilianTestHooks.mainReadListDOMWindow(false, false)
    assert.match(
      sentinel.__recruitHelperMainError,
      /^read_list_main_failed:resolve_surface:dom_list_virtual_missing$/u,
    )

    globalThis.chrome = {
      scripting: {
        async executeScript({ func, args }) {
          return [{ result: await func(...args) }]
        },
      },
    }
    await assert.rejects(
      zhilianTestHooks.runMain(
        8,
        zhilianTestHooks.mainReadListDOMWindow,
        [false, false],
      ),
      (error) => {
        assert.match(error.message, /read_list_main_failed:resolve_surface:dom_list_virtual_missing/u)
        assert.doesNotMatch(error.message, /页面脚本未返回结果/u)
        return true
      },
    )
  } finally {
    Object.assign(globalThis, original)
  }
})

test('readList MAIN 从同一行 Nuxt 组件读取稳定身份且私有时间倒挂不阻断页面顺序', async () => {
  const original = {
    document: globalThis.document,
    window: globalThis.window,
    getComputedStyle: globalThis.getComputedStyle,
    setTimeout: globalThis.setTimeout,
  }
  const marker = {}
  const makeRow = () => ({
    getClientRects: () => [{}],
    contains: (element) => element === marker,
    querySelector(selector) {
      if (selector === '.im-session-item__box, .im-session-item') return marker
      return null
    },
    querySelectorAll() { return [] },
  })
  const rows = [makeRow(), makeRow()]
  const sources = [
    {
      sessionId: 'session-nuxt-a',
      peerPartnerId: 'peer-nuxt-a',
      unreadCount: 1,
      name: '候选人甲',
      jobTitle: '销售',
      sortTime: 2_000,
      lastSentence: { senderType: 'USER', text: '你好', sendTime: 2_000 },
    },
    {
      sessionId: 'session-nuxt-b',
      peerPartnerId: 'peer-nuxt-b',
      unreadCount: 0,
      name: '候选人乙',
      jobTitle: '销售',
      // 真机可见列表顺序与私有 sortTime 可能短暂倒挂；页面顺序是读取事实，
      // 私有字段不取得整窗授权权力。
      sortTime: 3_000,
      lastSentence: { senderType: 'STAFF', text: '稍后联系', sendTime: 3_000 },
    },
  ]
  const virtual = {
    scrollTop: 0,
    scrollHeight: 600,
    clientHeight: 300,
    parentElement: null,
    querySelectorAll() { return [] },
    dispatchEvent() {},
  }
  let timerCalls = 0
  try {
    globalThis.setTimeout = (callback) => {
      timerCalls += 1
      sources[0].unreadCount = timerCalls
      callback()
      return 0
    }
    globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
    globalThis.document = {
      querySelector(selector) {
        return selector === '.im-session-list .im-session-list__virtual' ? virtual : null
      },
      querySelectorAll(selector) {
        return selector.includes('div[role="listitem"]') ? rows : []
      },
    }
    globalThis.window = {
      $nuxt: {
        $children: rows.map((row, index) => ({
          $el: row,
          _props: { source: sources[index] },
          $children: [],
        })),
      },
    }
    const result = await zhilianTestHooks.mainReadListDOMWindow(false, false)
    assert.equal(result.__recruitHelperMainError, undefined)
    assert.deepEqual(
      result.sessions.map((item) => [
        item.conversationRef,
        item.peer.platformUserRef,
        item.lastMessage.direction,
      ]),
      [
        ['session-nuxt-a', 'peer-nuxt-a', 'in'],
        ['session-nuxt-b', 'peer-nuxt-b', 'out'],
      ],
    )
    assert.ok(timerCalls >= 3, '测试必须覆盖稳定等待期间业务字段持续变化')
    const advanced = await zhilianTestHooks.mainReadListDOMWindow(true, false)
    assert.equal(advanced.__recruitHelperMainError, undefined)
    assert.equal(advanced.scrollTop, 210, 'next 每次只移动当前视口高度的 70%，保留跨窗重叠')
    assert.equal(advanced.moved, true)
  } finally {
    Object.assign(globalThis, original)
  }
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
  harness.state.unreadReads = [0, 2, 3]
  sensor.onDOMMutation()
  harness.runTimers()
  assert.equal(unreadCount(), beforeMismatch, '两次读数不一致不得上报')

  harness.state.now = 1_000
  harness.state.unreadReads = [3]
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

  const stableSnapshotBefore = unreadCount()
  const businessEventsBefore = harness.messages.filter((message) =>
    message.type === CONTENT_MESSAGE.UnreadStable && message.emitEvent).length
  harness.state.unreadReads = [4, 4]
  sensor.configure({
    badgeDebounceMs: 800,
    badgeMinEmitIntervalMs: 5_000,
    navSettleMs: 500,
    manualQuietMs: 45_000,
  }, true)
  harness.runTimers()
  assert.equal(unreadCount(), stableSnapshotBefore + 1,
    '新 SW 请求快照时，同一个稳定值也必须重新交付')
  const repeatedSnapshot = harness.messages
    .filter((message) => message.type === CONTENT_MESSAGE.UnreadStable)
    .at(-1)
  assert.deepEqual(repeatedSnapshot, {
    type: CONTENT_MESSAGE.UnreadStable,
    emitEvent: false,
    observedAt: 6_000,
    prev: 4,
    value: 4,
  })
  assert.equal(harness.messages.filter((message) =>
    message.type === CONTENT_MESSAGE.UnreadStable && message.emitEvent).length, businessEventsBefore,
  '强制快照只能回补 SW 现货，不能制造重复业务事件')

  harness.state.unreadReads = [5, 5]
  sensor.configure({
    badgeDebounceMs: 800,
    badgeMinEmitIntervalMs: 5_000,
    navSettleMs: 500,
    manualQuietMs: 45_000,
  })
  const unreadReadsBeforeMutations = harness.state.unreadReads.length
  for (let index = 0; index < 20; index += 1) sensor.onDOMMutation()
  assert.equal(harness.state.unreadReads.length, unreadReadsBeforeMutations,
    '采样窗已经在路上时，高频 DOM mutation 不得重启双读首样本')
  harness.runTimers()
  assert.equal(
    harness.messages.filter((message) =>
      message.type === CONTENT_MESSAGE.UnreadStable && message.value === 5).length,
    1,
    '持续渲染期间既有采样窗仍必须按期完成',
  )

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

test('SensorBridge 只把推荐页 committed reload 上报为换代并压住随后重复 manual', async () => {
  const originalChrome = globalThis.chrome
  const runtimeMessage = chromeEvent()
  const tabActivated = chromeEvent()
  const tabUpdated = chromeEvent()
  const tabRemoved = chromeEvent()
  const windowFocused = chromeEvent()
  const committed = chromeEvent()
  const historyUpdated = chromeEvent()
  const fragmentUpdated = chromeEvent()
  let now = 10_000
  try {
    globalThis.chrome = {
      runtime: { onMessage: runtimeMessage },
      tabs: {
        onActivated: tabActivated,
        onUpdated: tabUpdated,
        onRemoved: tabRemoved,
        async query() { return [] },
        async sendMessage() { return { ok: true } },
      },
      windows: { WINDOW_ID_NONE: -1, onFocusChanged: windowFocused },
      webNavigation: {
        onCommitted: committed,
        onHistoryStateUpdated: historyUpdated,
        onReferenceFragmentUpdated: fragmentUpdated,
      },
    }
    const connection = new FakeSensorConnection()
    const bridge = new SensorBridge(connection, new NavigationTracker(() => now), () => now)
    bridge.start()
    const listener = tabUpdated.listeners[0]
    const recommendURL = 'https://rd6.zhaopin.com/app/recommend'

    committed.listeners[0]({
      tabId: 7, frameId: 0, url: recommendURL, timeStamp: now, transitionType: 'reload',
    })
    assert.equal(connection.events.length, 0, '无 CmdContext 时推荐页 reload 不得上报')

    connection.setContext({ platform: 'zhilian', accountRef: 'account-1', expectedPrincipalFingerprint: 'fp' })
    bridge.acceptContentMessage({
      type: CONTENT_MESSAGE.ManualInteraction,
      at: now,
      kind: ManualInteractionKind.Pointer,
      pageKind: PageKind.Recommend,
    }, { tabId: 7, active: true, url: recommendURL, windowId: 1 })
    assert.equal(connection.events.length, 1)

    now += 1_000
    listener(7, { status: 'loading' }, { url: recommendURL })
    assert.equal(connection.events.length, 1, '简历详情也会令 tab loading，不能据此判定推荐流换代')
    committed.listeners[0]({
      tabId: 7, frameId: 0, url: recommendURL, timeStamp: now, transitionType: 'reload',
    })
    assert.equal(connection.events.length, 2, '公开确认的整页 reload 不得被普通 5s manual 节流吞掉')
    assert.deepEqual(connection.events.at(-1), {
      name: EventName.ManualInteraction,
      platform: 'zhilian',
      accountRef: 'account-1',
      observedAt: now,
      data: {
        at: now,
        kind: ManualInteractionKind.Navigation,
        pageKind: PageKind.Recommend,
      },
    })

    now += 1
    bridge.acceptContentMessage({
      type: CONTENT_MESSAGE.ManualInteraction,
      at: now,
      kind: ManualInteractionKind.Pointer,
      pageKind: PageKind.Recommend,
    }, { tabId: 7, active: true, url: recommendURL, windowId: 1 })
    assert.equal(connection.events.length, 2, 'reload 必须更新 manual 节流时刻，压住紧随其后的重复上报')

    now += MANUAL_EMIT_MIN_MS
    listener(7, { status: 'complete' }, { url: recommendURL })
    listener(7, { status: 'loading' }, { url: 'https://rd6.zhaopin.com/app/im' })
    listener(7, { status: 'loading' }, { url: 'https://example.com/app/recommend' })
    committed.listeners[0]({
      tabId: 7, frameId: 0, url: recommendURL, timeStamp: now, transitionType: 'link',
    })
    committed.listeners[0]({
      tabId: 7, frameId: 0, url: 'https://rd6.zhaopin.com/app/im', timeStamp: now,
      transitionType: 'reload',
    })
    committed.listeners[0]({
      tabId: 7, frameId: 1, url: recommendURL, timeStamp: now, transitionType: 'reload',
    })
    assert.equal(connection.events.length, 2, '非 reload、非推荐页或非主框架均不得上报换代')
  } finally {
    globalThis.chrome = originalChrome
  }
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
