// 无需真脑的 base 单元测试。用 esbuild 加载与生产相同的 TypeScript 源码。
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mock } from 'node:test'
import * as esbuild from 'esbuild'
import { mkdirSync, readFileSync, readdirSync } from 'node:fs'
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
  telemetryAppend,
  bodyText,
  deepFind,
  recordUpload,
  bossTelemetrySite,
  telemetrySites,
  telemetryReadAll,
  telemetryClear,
  telemetryParseLedger,
  shouldRecord,
  siteOfHost,
  hostOf,
  stripHeaders,
  sensitiveHeader,
  describeBody,
  captureActive,
  CaptureSession,
  CAPTURE_DURATION_MS,
  STRIPPED_VALUE,
  BODY_KEEP_CHARS,
  HEADER_VALUE_KEEP_CHARS,
  TELEMETRY_KIND_REQUEST,
  TELEMETRY_MAX_REQUEST_CHUNKS,
  netCaptureSites,
  tabHost,
  noteTabUrl,
  forgetTabHost,
  resetTabHostsForTest,
  BOSS_INPUT_COUNTERS,
  BOSS_REPORT_EVERY,
  classifyBossEntry,
  bossCodeLabel,
  bossCodeMeaning,
  bossKnownCodes,
  bossSevereHits,
  TELEMETRY_CHUNK,
  TELEMETRY_KIND_CLICK,
  TELEMETRY_KIND_UPLOAD,
  TELEMETRY_MAX_UPLOAD_CHUNKS,
  TELEMETRY_MAX_CLICK_CHUNKS,
  capabilities,
  DEFAULTS,
  Dispatcher,
  ErrorCode,
  ERROR_CODE_META,
  EventName,
  Feature,
  GENERATED_CONTRACT_VALIDATION_EXECUTED,
  handleInfrastructureMessage,
  HandLogCode,
  heartbeatDelayMs,
  installHandLogSink,
  netGuardStats,
  noteCommandDispatched,
  registerNetGuard,
  resetNetGuardForTest,
  Kind,
  LoginState,
  MAIN_ERROR_SENTINEL,
  ManualInteractionKind,
  runInPage,
  normalizeLocalWsUrl,
  parsedKeywordSections,
  NotReadyReason,
  PageKind,
  PlatformError,
  Primitive,
  PROTO_VERSION,
  RECONNECT_STABLE_MS,
  Retryable,
  ResultStatus,
  SensorBridge,
  allSites,
  bossAdapter,
  bossSite,
  bossTestHooks,
  BOSS_DISMISS_WHITELIST,
  identityCacheUsable,
  resetBossIdentityCacheForTest,
  tabNavigationGeneration,
  noteMainFrameNavigation,
  forgetTab,
  resetTabGenerationsForTest,
  registerTabGenerationTracking,
  requireCapability,
  resetSitesForTest,
  setSitesForTest,
  zhilianSite,
  ZHILIAN_UNREAD_BADGE_SELECTOR,
  parseZhilianUnreadBadgeText,
  applyZhilianSourcingFilters,
  acceptZhilianWechatRequest,
  canonicalZhilianTab,
  ensureZhilianIM,
  zhilianInterviewDetails,
  identifyZhilianCurrentConversation,
  inspectZhilianSendSurfaceDiagnostic,
  openZhilianConversation,
  readZhilianList,
  readZhilianThread,
  readZhilianCurrentCandidate,
  readZhilianSourcingResume,
  readZhilianSourcingTargetResume,
  readZhilianSourcingWindow,
  readZhilianGreetingOutcome,
  readZhilianWechatExchangeOutcome,
  refreshPagesAfterRuntimeReload,
  registerM2Primitives,
  registerM3Primitives,
  registerM4Primitives,
  registerM5Primitives,
  registerM6Primitives,
  registerM7Primitives,
  registerJobPublishPrimitives,
  registerAccountPrimitives,
  registerDebugPrimitives,
  capabilitiesByPlatform,
  hasCapability,
  registerPlatform,
  registeredPlatforms,
  resetPlatformsForTest,
  zhilianAdapter,
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
  planMove,
  planType,
  PUNCT_KEY,
  tokenize,
  keyFor,
  stripNewlines,
  clickAimPoint,
  mulberry32,
  SPREAD_FRACTIONS,
  refuseBeforeMoving,
  runOsProbe,
  refuseWhenNotInFront,
  describeFront,
  osProbeContractData,
  composeClearKeys,
  composeScrollBurst,
  composeScrollPause,
  SCROLL_BURST,
  runOsScroll,
  osScrollContractData,
  osClickContractData,
  DEFAULT_MAX_DWELL_MS,
  OSENGINE_SOURCE,
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
    readLoginState() { return state.loginReads.length > 0 ? state.loginReads.shift() : state.login },
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
      navSettleMs: 500,
      manualQuietMs: 45_000,
    }
    // 按平台各存一份 —— 生产的 currentCommandContext(platform) 就是这个形状。
    this.contexts = new Map()
    this.events = []
    this.contextHealth = []
    this.commandListeners = []
    this.configListeners = []
    this.heartbeatListeners = []
  }
  currentCommandContext(platform) {
    return this.contexts.get(platform)
  }
  emitPlatformSensorEvent(name, platform, data, observedAt) {
    this.events.push({
      name, platform, data, observedAt,
      accountRef: this.contexts.get(platform)?.accountRef,
    })
    return 'sent'
  }
  onCommandContext(listener) { this.commandListeners.push(listener); return () => {} }
  onSensorConfig(listener) { this.configListeners.push(listener); return () => {} }
  onHeartbeat(listener) { this.heartbeatListeners.push(listener); return () => {} }
  sensorConfig() { return this.config }
  setContextHealth(contexts) { this.contextHealth = contexts }
  setContext(context) {
    this.contexts.set(context.platform, context)
    for (const listener of this.commandListeners) listener(context)
  }
  // 驱动一次心跳节奏，供重新同步用例使用
  tickHeartbeat() {
    for (const listener of this.heartbeatListeners) listener()
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
  const queriedURLs = []
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
        // 每个已登记站点各查一次:换代后的内容脚本只有靠刷新才进得去已开的页面,
        // 所以"新加的平台有没有被刷到"正是本用例要钉住的东西。
        async query(query) {
          queriedURLs.push(query.url)
          const base = 10 * (queriedURLs.length)
          return [{ id: base + 1 }, { id: base + 2 }, {}]
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

    const siteMatches = allSites().map((site) => site.match)
    assert.equal(await refreshPagesAfterRuntimeReload(), 2 * siteMatches.length)
    assert.deepEqual(queriedURLs.slice().sort(), siteMatches.slice().sort(),
      '每个已登记站点都必须被刷一次 —— 漏掉的那个平台,已开的页面上永远是旧内容脚本')
    assert.equal(pageReloads.length, 2 * siteMatches.length)
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

test('witness ack 收割 remove 后 meta 更新失败改为换代继续，重启仍不得在旧 storeId 下答 unknown', async () => {
  const now = 1_700_000_000_000
  let failMetaWrite = false
  const storage = memoryWitnessStorage({}, {
    beforeSet: async (items) => {
      // 换代写的也是 witness:meta，但它必须能落盘——否则无从安全继续，只能升 A 档。
      // 这里只让"计数更新"那一次失败，模拟真实的删除/计数崩溃缝。
      if (failMetaWrite && items['witness:meta']?.storeId === 'witness-ack-crashseam-1') {
        throw new Error('storage quota')
      }
    },
  })
  let seq = 0
  const witness = new WitnessStore(storage, () => now, () => `witness-ack-crashseam-${++seq}`)
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
  // remove 已成功而计数没落盘。B 档:换代后继续服务,不熔断整库(§9.5 B 档第 5 条)。
  await witness.acknowledgeResult('envelope-seam-1')
  assert.notEqual(storage.state['witness:meta'].storeId, 'witness-ack-crashseam-1',
    'remove 已成功而 meta 更新失败时必须换代，不得继续沿用旧 storeId')
  const rotated = storage.state['witness:meta'].storeId
  failMetaWrite = false
  // 换代后库仍然可用——这正是本次裁决要的:一条崩溃缝不该让整机停发。
  await witness.markAttempting('cmd-seam-2', 'idem-seam-2')
  assert.equal(witness.advertisement().witnessStoreId, rotated)
  // 换代与计数修正写在同一次 set 里,所以重启时已无差额、无须再换一次代。
  // "记录真的丢了"那条路径由「单 journal 丢失换代后继续服务」单独覆盖。
  const restarted = new WitnessStore(storage, () => now, () => 'witness-ack-restarted')
  await restarted.initialize()
  assert.equal(storage.state['witness:meta'].storeId, rotated,
    '换代已同时修正计数，重启不应再产生一次多余换代')
  assert.equal(storage.state['witness:meta'].journalCount, 1, '换代必须带着修正后的计数落盘')
  assert.equal(storage.state['witness:meta'].outboxCount, 0)
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

test('witness 单 journal 丢失换代后继续服务，绝不在旧 storeId 下伪造 unknown', async () => {
  const storage = memoryWitnessStorage()
  const first = new WitnessStore(storage, () => 1_700_000_000_000, () => 'witness-continuity')
  await first.initialize()
  await first.markAttempting('cmd-continuity', 'idem-continuity')
  assert.equal(storage.state['witness:meta'].journalCount, 1)

  delete storage.state['journal:idem-continuity']
  const afterRestart = new WitnessStore(storage, () => 1_700_000_000_100, () => 'witness-rotated')
  // 账面有、实物没有 = 可能有记录悄悄消失。B 档:不熔断,但必须换代——脑比较
  // WitnessStoreIDAtDispatch 后走验证/人工,绝不会把 report=unknown 当成
  // 零副作用证明并安全重投(§9.5 B 档第 3 条)。
  await afterRestart.initialize()
  assert.equal(storage.state['witness:meta'].storeId, 'witness-rotated',
    '账面 count=1 而 key=0 时必须换代，不得在旧 storeId 下继续')
  assert.equal(afterRestart.advertisement().witnessStoreId, 'witness-rotated',
    '换代必须对外宣告，否则脑仍会按旧世代授权安全重投')
  assert.equal(storage.state['witness:meta'].journalCount, 0, '计数须按实际 key 集修正')
})

test('witness 对 meta+成功 outbox 已落但 journal 仍 attempting 的 partial write 换代后继续', async () => {
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
  const witness = new WitnessStore(storage, () => now + 2, () => 'witness-partial-rotated')
  // 终局 outbox 找不到对应的 committed journal,最可能的成因就是那条 journal
  // 已经不在了。B 档:换代后继续服务,不整库熔断(§9.5 B 档第 4 条)。
  await witness.initialize()
  assert.equal(storage.state['witness:meta'].storeId, 'witness-partial-rotated',
    'outbox 终局与 journal 关联破裂时必须换代，不得在旧 storeId 下继续')
  assert.equal(witness.advertisement().witnessStoreId, 'witness-partial-rotated')
})

test('witness B 档闩锁按触发身份记账：持久触发只换一次代，新触发各换一次', async () => {
  const now = 1_700_000_000_000
  let seq = 0
  const storage = memoryWitnessStorage({
    'witness:meta': {
      storeId: 'witness-latch-base', createdAt: now, schemaVersion: 1,
      journalCount: 1, outboxCount: 0,
    },
    // state=committed 却缺 committedAt/result：违反 schema，读不懂。它会一直
    // 留在 storage 里，因此每次 loadValidated 都会再次命中同一个 B 档触发。
    'journal:idem-latch': { ref: 'cmd-latch', idemKey: 'idem-latch', state: 'committed', startedAt: now },
  })
  const witness = new WitnessStore(storage, () => now, () => `witness-latch-${++seq}`)
  await witness.initialize()
  const first = storage.state['witness:meta'].storeId
  assert.equal(first, 'witness-latch-1', '首次读到读不懂的条目必须换代')

  // 反复触发 reload。若闩锁失效而每读一换，脑侧 WitnessStoreIDAtDispatch 会在
  // 每条命令的 report 回来前就失效，全部 effectful 转验证/人工——人工介入率被
  // 顶满，比原来的熔断更难诊断。
  await witness.findJournalByIdemKey('idem-other')
  await witness.findJournalByRef('cmd-other')
  await witness.markAttempting('cmd-latch-2', 'idem-latch-2')
  assert.equal(storage.state['witness:meta'].storeId, first, '同一持久触发只换一次代')
  assert.ok(storage.state['journal:idem-latch'], '隔离不等于删除：读不懂的条目必须留在 storage')

  // 新触发必须各自再换一次代，闩锁绝不能退化成“我已降级故不再换代”——那样
  // 此后每一次真实的记录丢失都会停在旧 storeId 下，接上脑的安全重投闸。
  storage.state['journal:idem-latch-3'] = {
    ref: 'cmd-latch-3', idemKey: 'idem-latch-3', state: 'committed', startedAt: now,
  }
  storage.state['witness:meta'].journalCount = 3
  await witness.findJournalByIdemKey('idem-other')
  assert.equal(storage.state['witness:meta'].storeId, 'witness-latch-2', '新出现的触发必须再换一次代')
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

test('TTL 删除后 count 更新失败改为再换一次代继续，换代自身写不进去才升 A 档', async () => {
  let now = 1_700_000_000_000
  let failCountUpdate = false
  const storage = memoryWitnessStorage({}, {
    beforeSet(items) {
      // 只拦"本轮清理世代下的计数更新"这一次。换代写的是下一个 storeId，
      // 必须放行——否则测的就成了"storage 整体不可写"，那是 A 档。
      if (failCountUpdate && items['witness:meta']?.storeId === 'witness-ttl-crash-new-1' &&
          items['witness:meta']?.journalCount === 0) {
        throw new Error('fixture crash after ttl remove')
      }
    },
  })
  const first = new WitnessStore(storage, () => now, () => 'witness-ttl-crash-old')
  await first.initialize()
  await first.markAttempting('cmd-ttl-crash', 'idem-ttl-crash')
  now += DEFAULTS.journalTtlDays * 24 * 60 * 60 * 1000 + 1
  failCountUpdate = true
  // TTL 清理开头已换过一次代;删除生效而计数没落盘是本次新出现的事实,
  // 按新触发再换一次代(§9.5 处置纪律第 9 条),不因"本世代已换过"而豁免。
  let ttlSeq = 0
  const pruning = new WitnessStore(storage, () => now, () => `witness-ttl-crash-new-${++ttlSeq}`)
  await pruning.initialize()
  assert.equal(storage.state['witness:meta'].storeId, 'witness-ttl-crash-new-2',
    'TTL 删除已生效而计数未落盘时必须再次换代，不得停留在本轮清理的世代上')
  failCountUpdate = false
  // 换代与计数修正同在一次 set 里落盘,重启已无差额,不该再多换一次代。
  const afterCrash = new WitnessStore(storage, () => now, () => 'witness-after-crash')
  await afterCrash.initialize()
  assert.equal(storage.state['witness:meta'].storeId, 'witness-ttl-crash-new-2',
    '换代已带着修正后的计数落盘，重启不应再产生一次多余换代')
  assert.equal(storage.state['witness:meta'].journalCount, 0)

  // 换代自身也写不进去时没有安全的继续方式,只能升 A 档(§9.5 处置纪律第 2 条)。
  let hardSeq = 0
  const hardStorage = memoryWitnessStorage({}, {
    beforeSet(items) {
      if (items['witness:meta']?.journalCount === 0 && items['witness:meta']?.storeId !== 'witness-hard-0') {
        throw new Error('fixture storage unwritable')
      }
    },
  })
  const hardFirst = new WitnessStore(hardStorage, () => now, () => `witness-hard-${hardSeq++}`)
  await hardFirst.initialize()
  await hardFirst.markAttempting('cmd-ttl-hard', 'idem-ttl-hard')
  const hardNow = now + DEFAULTS.journalTtlDays * 24 * 60 * 60 * 1000 + 1
  const hardPruning = new WitnessStore(hardStorage, () => hardNow, () => `witness-hard-${hardSeq++}`)
  await assert.rejects(
    hardPruning.initialize(),
    (error) => error instanceof WitnessStoreError && error.reason === WitnessUnavailableReason.StoreCorrupt,
    '换代写不进去时不得在旧 storeId 下继续服务',
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
    assert.equal(fixture.state.now - fixture.state.closedAt, 20_000,
      '关闭后最多条件等待 20 秒确认消失')
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
    titleReads: new Map(),
    staleClicks: 0,
    // 年龄「自定义」的区间下拉是否已弹出;初态即目标时下拉本来就在。
    ageCustomOpen: options.initialTarget === true,
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
    isConnected: true,
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
          // 7-24 事实记录 §3.3:选完下限,「自定义」才成为唯一年龄选中项。
          if (kind === 'start') {
            for (const candidate of currentAgeGroup().optionNodes) {
              candidate.setSelected(candidate.textContent === '自定义')
            }
          }
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

  // 模拟"点击被平台吞掉":click() 照常派发、页面照常记一次交互,但选中态纹丝不动。
  // 2026-09-03/09-04 客户机年龄格现场即为此形态,肉眼就是"点了没反应"。
  const swallowedClicks = new Map()
  const swallowClick = (key, label) => {
    const spec = options.swallowClicks
    if (spec === undefined || spec.key !== key || spec.label !== label) return false
    const seen = swallowedClicks.get(`${key}/${label}`) ?? 0
    if (seen >= spec.times) return false
    swallowedClicks.set(`${key}/${label}`, seen + 1)
    return true
  }

  const buildGroup = (key, spec) => {
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
        if (swallowClick(key, label)) return
        if (key === 'age' && label === '自定义') {
          // 真实平台(7-24 事实记录 §3.3):点「自定义」只让区间下拉出现,格子此刻
          // 不亮,要到选完下限才成为唯一选中项。2026-09-05 3.27.0 首日零采集,就是
          // 夹具此前把它建模成"点哪个哪个立刻亮",让错的确认信号在测试里全绿。
          // 再点一下按最坏情况建模——下拉收回(真机未验;代码不该走到这一步)。
          state.ageCustomOpen = !state.ageCustomOpen
          return
        }
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
        // 只有 readFilters 查标题;点击前的当场定位不查,所以这个计数专门用来数
        // readFilters 跑了几轮,不受定位次数干扰。年龄组重建后两个对象共用同一 key。
        state.titleReads.set(key, (state.titleReads.get(key) ?? 0) + 1)
        return [title]
      }
      if (query === (spec.control === 'radio'
        ? '.km-radio'
        : '.recommend-checkbox-group__item')) {
        return options.driftOptionSet === key ? optionNodes.slice(0, -1) : optionNodes
      }
      if (query === '.recommend-checkbox-group__selector') {
        return key === 'age' && state.ageCustomOpen ? [selector] : []
      }
      return []
    }
    return group
  }
  for (const [key, spec] of Object.entries(groupSpecs)) {
    groups[key] = buildGroup(key, spec)
  }

  // 模拟真实平台上的年龄组重建:初读之后 Vue 换掉整组 DOM,旧节点脱离文档 ——
  // 对它 click() 既不抛异常也不生效。取组必须当场重取才点得中。
  let ageReplacement = null
  const currentAgeGroup = () => {
    if (options.replaceAgeGroupAfterRead !== true) return groups.age
    if ((state.groupReads.get('age') ?? 0) < 2) return groups.age
    if (ageReplacement === null) {
      ageReplacement = buildGroup('age', groupSpecs.age)
      groups.age.isConnected = false
      for (const option of groups.age.optionNodes) {
        option.click = () => { state.staleClicks += 1 }
      }
    }
    return ageReplacement
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
        return [key === 'age' ? currentAgeGroup() : groups[key]]
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
      assert.equal(fixture.state.titleReads.get(key), 3,
        `${key} 必须由字面同一 readFilters 完成初读、提交前回读和二次回读`)
    }
    const names = fixture.state.interactions.map(([name]) => name)
    assert.equal(names.filter((name) => name === 'confirm').length, 1)
    assert.equal(names.includes('click-careerStatuses-不限'), false, '相同多选不得冗余点击')
    assert.equal(names.includes('click-gender-不限'), false, '相同单选不得冗余点击')
    // 2026-09-05 事故回归:点「自定义」后下拉出现、格子未亮,这就是达标,不得再点第二下。
    assert.equal(names.filter((name) => name === 'click-age-自定义').length, 1,
      '「自定义」只点一次:下拉已出现即达标,格子亮不亮不是此刻的判据')
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

// 2026-08-07 客户机现场:年龄格肉眼没反应、其余五组正常选中,20 秒后
// custom_selector_unavailable。成因是取节点与 click 之间隔着一秒的平台节奏等待,
// Vue 在这一秒里重建了年龄组,click() 打在脱离文档的旧节点上——不报错也不生效。
test('candidate.applySourcingFilters MAIN 年龄组初读后被重建仍点得中并完成自定义', async () => {
  const fixture = installM6SourcingFilterFixture({ replaceAgeGroupAfterRead: true })
  try {
    const result = await zhilianTestHooks.mainApplySourcingFilters(
      fixture.refs.job,
      fixture.refs.title,
      structuredClone(m6SourcingFilterTarget),
    )
    assert.equal(result.status, 'ready', '组被重建不该让筛选失败')
    assert.equal(fixture.state.staleClicks, 0, '不得再对脱离文档的旧节点派点击')
    const names = fixture.state.interactions.map(([name]) => name)
    assert.equal(names.filter((name) => name === 'click-age-自定义').length, 1,
      '必须点中重建后的自定义,且只点一次')
    assert.ok(names.includes('choose-range-start-25'))
    assert.ok(names.includes('choose-range-end-45'))
    assert.equal(fixture.state.confirms, 1, '筛选命令仍只点一次确定')
  } finally {
    fixture.restore()
  }
})

// 2026-09-03 与 09-04 客户机现场:点「自定义」那一下被吞,页面上年龄格仍是「不限」,
// 旧实现只看"我点了"、照旧返回 true,直到后面找自定义区间下拉才以
// custom_selector_unavailable 报错——09-04 那次废掉当日剩余 3 个职位 53 个名额。
test('candidate.applySourcingFilters MAIN 首次点击被吞时回读发现并补点', async () => {
  const fixture = installM6SourcingFilterFixture({
    swallowClicks: { key: 'age', label: '自定义', times: 1 },
  })
  try {
    const result = await zhilianTestHooks.mainApplySourcingFilters(
      fixture.refs.job,
      fixture.refs.title,
      structuredClone(m6SourcingFilterTarget),
    )
    assert.equal(result.status, 'ready', '点击被吞必须由回读发现并补点,不能拖到下拉才失败')
    const names = fixture.state.interactions.map(([name]) => name)
    assert.equal(names.filter((name) => name === 'click-age-自定义').length, 2,
      '恰好补点一次:回读已达目标态就不再点,不能把 checkbox 又 toggle 回去')
    assert.ok(names.includes('choose-range-start-25'))
    assert.ok(names.includes('choose-range-end-45'))
    assert.equal(fixture.state.confirms, 1, '筛选命令仍只点一次确定')
    for (let index = 1; index < fixture.state.interactions.length; index += 1) {
      const elapsed =
        fixture.state.interactions[index][1] - fixture.state.interactions[index - 1][1]
      assert.ok(elapsed >= 1_000, `补点也须守平台节奏下限(第 ${index + 1} 个动作)`)
    }
  } finally {
    fixture.restore()
  }
})

test('candidate.applySourcingFilters MAIN 点击持续被吞时按选项失配收场且补点有界', async () => {
  const fixture = installM6SourcingFilterFixture({
    swallowClicks: { key: 'age', label: '自定义', times: 99 },
  })
  try {
    const result = await zhilianTestHooks.mainApplySourcingFilters(
      fixture.refs.job,
      fixture.refs.title,
      structuredClone(m6SourcingFilterTarget),
    )
    assert.equal(result.status, 'failed')
    assert.equal(result.reason, 'option_click_exhausted',
      '点不中就是点不中,要在这一步响亮失败,不许带着「不限」往下走;'
      + '且要与「平台改版、选项集不认识」的 option_set_mismatch 分开')
    assert.ok(String(result.scene).includes('age/自定义'), '失败要带上是哪一格没点动')
    assert.ok(String(result.scene).includes('选中态=false,下拉=无'),
      '失败要带上最后一次回读到的实际值(09-05 现场少了这一句只能猜)')
    const names = fixture.state.interactions.map(([name]) => name)
    assert.equal(names.filter((name) => name === 'click-age-自定义').length, 3, '补点至多三次')
    assert.equal(fixture.state.confirms, 0, '没点中不得提交')
    assert.equal(fixture.state.drawerOpen, false, '失败也要把抽屉收干净')
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

// 分档决定脑侧敢不敢再跑一趟:CTX_NOT_READY + afterRecovery 才进
// patrol.transientPageNotReady 的重试链,manualOnly 一律停工。2026-09-04 起
// "点不动/下拉没出来"归瞬时档,"平台选项集不认识"仍归人工档。
test('candidate.applySourcingFilters outer 按成因分档:点不动是瞬时,选项集不认识才要人工', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = '8'.repeat(64)
  const cases = [
    ['option_click_exhausted', 'CTX_NOT_READY', 'afterRecovery'],
    ['custom_selector_unavailable', 'CTX_NOT_READY', 'afterRecovery'],
    ['option_set_mismatch', 'ELEMENT_UNRESOLVED', 'manualOnly'],
    ['selection_unreadable', 'ELEMENT_UNRESOLVED', 'manualOnly'],
  ]
  try {
    for (const [reason, expectedCode, expectedRetryable] of cases) {
      globalThis.chrome = {
        tabs: {
          async query() {
            return [{
              id: 601,
              active: true,
              status: 'complete',
              url: 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-filter-job',
            }]
          },
        },
        scripting: {
          async executeScript({ func }) {
            if (func.name === 'mainProbeZhilian') {
              return [{ result: {
                pageKind: 'recommend',
                loginState: 'in',
                principalFingerprint: fingerprint,
                imListVisible: false,
              } }]
            }
            return [{ result: { status: 'failed', reason, scene: 'age/自定义=选中' } }]
          },
        },
      }
      const context = {
        signal: new AbortController().signal,
        cmdMsgId: 'apply-sourcing-filter-class-fixture',
        deadlineMs: Date.now() + 10_000,
        irreversibleNotAfterMs: Date.now() + 10_000,
        commandContext: undefined,
        guards: undefined,
        checkpoint() {},
        async beforeSideEffect() {},
        async progress() {},
      }
      await assert.rejects(
        applyZhilianSourcingFilters({
          positionRef: 'fixture-filter-job',
          positionTitle: '合成筛选职位',
          filters: structuredClone(m6SourcingFilterTarget),
        }, context, fingerprint),
        (error) => {
          assert.equal(error.code, expectedCode, `${reason} 应判 ${expectedCode}`)
          assert.equal(error.retryable, expectedRetryable, `${reason} 应判 ${expectedRetryable}`)
          assert.ok(String(error.message).includes(reason), '判定现场必须随消息带出')
          return true
        },
      )
    }
  } finally {
    globalThis.chrome = originalChrome
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

test('candidate.readSourcingWindow MAIN 列表短到无处可滚时 next/reset 如实报 moved=false 而非失败', async () => {
  // 2026-09-05~07 俞炳冬01 真机:推荐流只剩 1~3 人,列表不溢出、document root 也不可滚,
  // 此前 next 报 scroll_unavailable → 脑记 windowReadFailed → 整日计划终止。
  const fixture = installM6SourcingWindowFixture({
    startAt: 'first', staticDom: true, staticDomCount: 2, virtualTime: true,
  })
  try {
    assert.equal(fixture.scroller.scrollHeight, fixture.scroller.clientHeight, '夹具:列表不溢出')
    const current = await zhilianTestHooks.mainReadSourcingWindow('current')
    assert.equal(current.status, 'ready')
    assert.deepEqual(current.data.platformUserRefs, fixture.refs.all)
    assert.equal(current.data.moved, false)

    const next = await zhilianTestHooks.mainReadSourcingWindow('next')
    assert.equal(next.status, 'ready', 'next 无处可滚不是故障')
    assert.deepEqual(next.data.platformUserRefs, fixture.refs.all)
    assert.equal(next.data.moved, false)
    assert.equal(Object.hasOwn(next.data, 'exhausted'), false, 'moved 不承载耗尽语义')

    const reset = await zhilianTestHooks.mainReadSourcingWindow('reset')
    assert.equal(reset.status, 'ready')
    assert.deepEqual(reset.data.platformUserRefs, fixture.refs.all)
    assert.equal(reset.data.moved, false)
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
  // modalKind: 'legacy' 是旧版 .ai-greeting-modal;'km' 是新版 .chat-set-greet
  // (2026-08-24 真机形态:km-radio 选项组、常驻输入框、原生禁用发送键)。
  const modalKind = options.modalKind === 'km' ? 'km' : 'legacy'
  const state = {
    modalVisible: options.existingModal === true,
    customSelected: options.existingModal === true || options.customInitiallySelected === true,
    textareaVisible: options.existingModal === true,
    sendDisabled: modalKind === 'km' && String(options.existingDraft ?? '').trim() === '',
    invalidText: options.invalidText ?? '',
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
      if (modalKind === 'km' && options.sendStaysDisabled !== true) {
        state.sendDisabled = this._value.trim() === ''
      }
    }
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
  }
  const textarea = new FixtureTextArea(options.existingDraft ?? (modalKind === 'km' ? '' : '平台原始招呼'))
  const editIcon = new FixtureHTMLElement()
  editIcon._onIntrinsicClick = () => {
    state.editClicks += 1
    state.interactions.push({ kind: 'edit', at: Date.now() })
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

  // 新版 .chat-set-greet 弹窗形态:预设 km-radio 两项 + 自带输入框的自定义项,
  // 发送键在正文为空时原生 disabled,页脚另有「选择其他招呼语」按钮。
  const kmRadioLabel = new FixtureHTMLElement()
  kmRadioLabel.classList = { contains: (name) => name === 'km-radio--checked' && state.customSelected }
  kmRadioLabel._onIntrinsicClick = () => {
    state.optionClicks += 1
    state.interactions.push({ kind: 'option', at: Date.now() })
    if (Number.isInteger(options.customSelectedAfterTimerCalls)) {
      delayedCustomSelectionPending = true
    } else {
      state.customSelected = true
    }
  }
  const kmCustomItem = new FixtureHTMLElement()
  kmCustomItem.classList = { contains: (name) => name === 'chat-set-greet__content-textarea' }
  // 校验槽常驻 DOM,无错时是纯空白;有错时平台写入拒绝原话并把发送键置回禁用。
  const kmInvalidNode = new FixtureHTMLElement()
  Object.defineProperty(kmInvalidNode, 'innerText', { get: () => state.invalidText })
  kmCustomItem.querySelectorAll = (selector) => {
    if (selector === '.km-radio') return [kmRadioLabel]
    if (selector === 'textarea') return [textarea]
    if (selector === '.km-form-item__invalid') return [kmInvalidNode]
    return []
  }
  const kmPresetItemA = new FixtureHTMLElement('你好，请问现在还在看机会吗？')
  const kmPresetItemB = new FixtureHTMLElement('Hi，方便聊一聊吗？')
  const kmOtherButton = new FixtureHTMLElement('选择其他招呼语')
  const kmSendButton = new FixtureHTMLElement('发送')
  Object.defineProperty(kmSendButton, 'disabled', { get: () => state.sendDisabled })
  kmSendButton._onIntrinsicClick = () => {
    state.finalClicks += 1
    state.interactions.push({ kind: 'send', at: Date.now() })
  }
  kmSendButton.click = () => { state.instanceClicks += 1 }
  const kmFooter = new FixtureHTMLElement()
  kmFooter.querySelectorAll = (selector) =>
    selector === 'button[type="button"]' ? [kmOtherButton, kmSendButton] : []
  const kmModal = new FixtureHTMLElement()
  kmModal.querySelectorAll = (selector) => {
    if (selector === '.chat-set-greet__content-form-item') return [kmPresetItemA, kmPresetItemB, kmCustomItem]
    if (selector === '.km-checkbox') return [defaultControl]
    if (selector === '.km-modal__footer') return [kmFooter]
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
      if (selector === '.ai-greeting-modal') {
        return modalKind === 'legacy' && state.modalVisible ? [modal] : []
      }
      if (selector === '.chat-set-greet .km-modal') {
        return modalKind === 'km' && state.modalVisible ? [kmModal] : []
      }
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
    sceneReads: 0,
    sceneBaselines: [],
    modalCloseCalls: 0,
    successDismissCalls: 0,
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
        if (func.name === 'mainReadGreetingScene') {
          state.sceneReads += 1
          state.sceneBaselines.push(structuredClone(args[0]))
          if (options.sceneMode === 'throw') throw new Error('fixture-scene-death')
          return [{ result: structuredClone(
            options.sceneResult ?? { status: 'ready', modalCount: 0, newTexts: [], editError: '', successModalCount: 0 },
          ) }]
        }
        if (func.name === 'mainCloseGreetingModal') {
          state.modalCloseCalls += 1
          if (options.closeMode === 'throw') throw new Error('fixture-close-death')
          return [{ result: { closed: options.closeMode !== 'stuck' } }]
        }
        if (func.name === 'mainDismissGreetingSuccessModals') {
          state.successDismissCalls += 1
          if (options.dismissMode === 'throw') throw new Error('fixture-dismiss-death')
          return [{ result: { found: true, closed: true, remaining: 0, scene: '' } }]
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
    assert.deepEqual(await fixture.invoke('commit'), { status: 'clicked', modalKind: 'legacy' })
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

test('M6 列表招呼新版弹窗:选中自带输入框的 km-radio 项、直写正文,commit 只做最终 intrinsic click', async () => {
  const fixture = installM4GreetingFixture({ modalKind: 'km' })
  try {
    assert.deepEqual(await fixture.invoke('prepare'), { status: 'prepared' })
    assert.equal(fixture.textarea.value, fixture.text)
    assert.deepEqual(fixture.state.textareaEvents, ['input', 'change'])
    assert.equal(fixture.state.optionClicks, 1, '未选中时点唯一 km-radio 标签选中自定义项')
    assert.equal(fixture.state.editClicks, 0, '新版输入框常驻,不存在编辑图标步骤')
    assert.deepEqual(await fixture.invoke('preflight'), { status: 'ready' })
    fixture.state.throwOnReadAfterFinal = true
    assert.deepEqual(await fixture.invoke('commit'), { status: 'clicked', modalKind: 'kmGreet' })
    assert.equal(fixture.state.openClicks, 1)
    assert.equal(fixture.state.finalClicks, 1)
    assert.equal(fixture.state.instanceClicks, 0, '最终动作必须绕过页面替换过的 instance click')
    assert.equal(fixture.state.checkboxClicks, 0, '「设置为默认发送」复选框一次都不得触碰')
  } finally {
    fixture.restore()
  }
})

test('M6 列表招呼新版弹窗:已选中的自定义项不再点选,发送键解禁跟随正文', async () => {
  const fixture = installM4GreetingFixture({ modalKind: 'km', customInitiallySelected: true })
  try {
    assert.equal(fixture.state.sendDisabled, true, '正文为空时发送键原生禁用')
    assert.deepEqual(await fixture.invoke('prepare'), { status: 'prepared' })
    assert.equal(fixture.state.optionClicks, 0, '已选中的自定义项不得再 click')
    assert.equal(fixture.state.sendDisabled, false)
    assert.deepEqual(await fixture.invoke('preflight'), { status: 'ready' })
  } finally {
    fixture.restore()
  }
})

test('M6 列表招呼新版弹窗:默认勾选被勾、发送键未解禁或既有弹窗时零最终动作', async () => {
  const checked = installM4GreetingFixture({ modalKind: 'km', defaultChecked: true })
  try {
    assert.deepEqual(await checked.invoke('prepare'), {
      status: 'failed', reason: 'default_setting_selected',
    })
    assert.equal(checked.state.checkboxClicks, 0, '不得替真人取消平台默认招呼设置')
    assert.equal(checked.textarea.value, '', '默认项被勾选时不得写正文')
    assert.deepEqual(checked.state.textareaEvents, [])
  } finally {
    checked.restore()
  }

  const disabled = installM4GreetingFixture({ modalKind: 'km', sendStaysDisabled: true })
  try {
    assert.deepEqual(await disabled.invoke('prepare'), { status: 'prepared' })
    assert.deepEqual(await disabled.invoke('preflight'), {
      status: 'failed', reason: 'send_surface_unavailable',
    })
    assert.deepEqual(await disabled.invoke('commit'), {
      status: 'failed', reason: 'send_surface_unavailable',
    })
    assert.equal(disabled.state.finalClicks, 0, '原生禁用的发送键绝不点击')
    assert.equal(disabled.state.instanceClicks, 0)
  } finally {
    disabled.restore()
  }

  const existing = installM4GreetingFixture({ modalKind: 'km', existingModal: true })
  try {
    assert.deepEqual(await existing.invoke('prepare'), { status: 'failed', reason: 'existing_editor' })
    assert.equal(existing.state.openClicks, 0, '既有新版弹窗同样挡住接管')
  } finally {
    existing.restore()
  }
})

test('M6 列表招呼新版弹窗:发送前平台校验拒绝即 content_rejected,空白校验槽不算拒绝', async () => {
  const rejected = installM4GreetingFixture({ modalKind: 'km' })
  try {
    assert.deepEqual(await rejected.invoke('prepare'), { status: 'prepared' })
    // 平台异步校验命中敏感词:校验槽出话,发送键回到禁用(2026-08-24 真机 DOM)。
    rejected.state.invalidText = '内容中涉及联系方式等敏感词，请修改后保存'
    rejected.state.sendDisabled = true
    assert.deepEqual(await rejected.invoke('preflight'), {
      status: 'failed', reason: 'content_rejected',
      detail: '内容中涉及联系方式等敏感词，请修改后保存',
    })
    assert.deepEqual(await rejected.invoke('commit'), {
      status: 'failed', reason: 'content_rejected',
      detail: '内容中涉及联系方式等敏感词，请修改后保存',
    })
    assert.equal(rejected.state.finalClicks, 0, '平台拒绝后绝不点击发送')
    assert.equal(rejected.state.instanceClicks, 0)
  } finally {
    rejected.restore()
  }

  const blank = installM4GreetingFixture({ modalKind: 'km', invalidText: '\n          \n        ' })
  try {
    assert.deepEqual(await blank.invoke('prepare'), { status: 'prepared' })
    assert.deepEqual(await blank.invoke('preflight'), { status: 'ready' })
  } finally {
    blank.restore()
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

test('M4 招呼阴性验证的 suspect 取证:浮层原话与未关弹窗进错误 message,基线内文本不报', async () => {
  const fixture = installM4GreetingOrchestrationFixture({
    proofMode: 'negative',
    sceneResult: { status: 'ready', modalCount: 1, newTexts: ['今日沟通人数已达上限'], editError: '', successModalCount: 0 },
  })
  const context = fixture.context()
  try {
    await assert.rejects(
      sendZhilianGreeting(
        { platformUserRef: fixture.refs.user, positionRef: fixture.refs.job, text: fixture.text },
        { expectUnestablished: true },
        context,
        fixture.fingerprint,
      ),
      (error) => {
        assert.ok(error instanceof ZhilianPlatformError)
        assert.equal(error.code, 'POSTCONDITION_UNCONFIRMED')
        assert.equal(error.sideEffect, 'possible')
        assert.ok(error.message.includes('今日沟通人数已达上限'), '浮层原话必须进错误 message')
        assert.ok(error.message.includes('招呼弹窗仍未关闭'), '弹窗未关必须进错误 message')
        assert.ok(error.message.length <= 500, 'ErrorBody.message 上限 500 字符')
        return true
      },
    )
    assert.equal(fixture.state.finalClicks, 1, '取证不得改变唯一发送')
    assert.ok(fixture.state.sceneReads >= 2, '基线一次+轮询至少一次')
    assert.deepEqual(fixture.state.sceneBaselines[0], [], '基线读取用空基线拿全量')
  } finally {
    fixture.restore()
  }
})

test('M4 招呼 suspect 取证读挂掉时判定与 message 不变', async () => {
  const fixture = installM4GreetingOrchestrationFixture({
    proofMode: 'negative',
    sceneMode: 'throw',
  })
  const context = fixture.context()
  try {
    await assert.rejects(
      sendZhilianGreeting(
        { platformUserRef: fixture.refs.user, positionRef: fixture.refs.job, text: fixture.text },
        { expectUnestablished: true },
        context,
        fixture.fingerprint,
      ),
      (error) => {
        assert.ok(error instanceof ZhilianPlatformError)
        assert.equal(error.code, 'POSTCONDITION_UNCONFIRMED')
        assert.equal(
          error.message,
          '最终发送只调用一次，但未确认同一候选人的关系状态变为已建立',
          '取证失败必须空手:message 保持原文',
        )
        return true
      },
    )
    assert.equal(fixture.state.finalClicks, 1)
  } finally {
    fixture.restore()
  }
})

test('M4 招呼:弹窗内错误位有话即判 GREETING_REJECTED,关弹窗后不再补动作', async () => {
  const fixture = installM4GreetingOrchestrationFixture({
    proofMode: 'negative',
    sceneResult: {
      status: 'ready', modalCount: 1, newTexts: [],
      editError: '内容中涉及敏感词，请修改', successModalCount: 0,
    },
  })
  const context = fixture.context()
  try {
    await assert.rejects(
      sendZhilianGreeting(
        { platformUserRef: fixture.refs.user, positionRef: fixture.refs.job, text: fixture.text },
        { expectUnestablished: true },
        context,
        fixture.fingerprint,
      ),
      (error) => {
        assert.ok(error instanceof ZhilianPlatformError)
        assert.equal(error.code, 'GREETING_REJECTED', '平台明确拒绝必须判 GREETING_REJECTED')
        assert.equal(error.sideEffect, 'none', '契约要求零候选人可见副作用')
        assert.equal(error.retryable, 'no')
        assert.ok(error.message.includes('内容中涉及敏感词'), '平台原话必须随行,供客户端显示')
        return true
      },
    )
    assert.equal(fixture.state.finalClicks, 1, '拒绝判定不得补第二次发送')
    assert.equal(fixture.state.modalCloseCalls, 1, '拒绝后必须关掉不会自己关的弹窗')
    assert.ok(fixture.state.proofCalls <= 2, '读到拒绝必须立即收场,不空转满 20 轮')
  } finally {
    fixture.restore()
  }
})

test('M4 招呼:新版弹窗发送前平台拒绝即 GREETING_REJECTED,零点击并关弹窗', async () => {
  const preflightRejected = installM4GreetingOrchestrationFixture({
    preflightResult: {
      status: 'failed', reason: 'content_rejected',
      detail: '内容中涉及联系方式等敏感词，请修改后保存',
    },
  })
  const context = preflightRejected.context()
  try {
    await assert.rejects(
      sendZhilianGreeting(
        {
          platformUserRef: preflightRejected.refs.user,
          positionRef: preflightRejected.refs.job,
          text: preflightRejected.text,
        },
        { expectUnestablished: true },
        context,
        preflightRejected.fingerprint,
      ),
      (error) => {
        assert.ok(error instanceof ZhilianPlatformError)
        assert.equal(error.code, 'GREETING_REJECTED', '发送前的平台拒绝必须判 GREETING_REJECTED')
        assert.equal(error.sideEffect, 'none', '未发生任何点击,副作用确定为无')
        assert.equal(error.retryable, 'no')
        assert.ok(error.message.includes('内容中涉及联系方式等敏感词'), '平台原话必须随行')
        return true
      },
    )
    assert.deepEqual(preflightRejected.state.phases, ['prepare', 'preflight'], '拒绝后不得进入 commit')
    assert.equal(preflightRejected.state.finalClicks, 0)
    assert.equal(preflightRejected.state.barriers, 0, '证词 barrier 之前就收场')
    assert.equal(preflightRejected.state.modalCloseCalls, 1, '发前拒绝同样要关掉不会自己关的弹窗')
  } finally {
    preflightRejected.restore()
  }

  const commitRejected = installM4GreetingOrchestrationFixture({
    commitResult: {
      status: 'failed', reason: 'content_rejected',
      detail: '内容中涉及联系方式等敏感词，请修改后保存',
    },
  })
  const commitContext = commitRejected.context()
  try {
    await assert.rejects(
      sendZhilianGreeting(
        {
          platformUserRef: commitRejected.refs.user,
          positionRef: commitRejected.refs.job,
          text: commitRejected.text,
        },
        { expectUnestablished: true },
        commitContext,
        commitRejected.fingerprint,
      ),
      (error) => {
        assert.equal(error.code, 'GREETING_REJECTED')
        assert.equal(error.sideEffect, 'none', 'commit 阶段的发前拒绝同样零点击')
        return true
      },
    )
    assert.equal(commitRejected.state.finalClicks, 0, '拒绝判定不得补发送')
    assert.equal(commitRejected.state.modalCloseCalls, 1)
  } finally {
    commitRejected.restore()
  }
})

test('M4 招呼:新版发送成功后等到「招呼语已发送」弹窗并清一趟,失败或缺席都不动摇成功', async () => {
  const args = (fixture) => [
    { platformUserRef: fixture.refs.user, positionRef: fixture.refs.job, text: fixture.text },
    { expectUnestablished: true },
    fixture.context(),
    fixture.fingerprint,
  ]

  // 弹窗在场:成功出场前探测到并清一趟。
  const swept = installM4GreetingOrchestrationFixture({
    commitResult: { status: 'clicked', modalKind: 'kmGreet' },
    sceneResult: { status: 'ready', modalCount: 0, newTexts: [], editError: '', successModalCount: 1 },
  })
  try {
    const data = await sendZhilianGreeting(...args(swept))
    assert.equal(data.contentHash, swept.contentHash, '清场不改变成功返回')
    assert.equal(swept.state.successDismissCalls, 1, '成功出场前必须清一趟成功弹窗')
  } finally {
    swept.restore()
  }

  // 弹窗一直不出现:有界等待后照常成功,零清场动作。
  const absent = installM4GreetingOrchestrationFixture({
    commitResult: { status: 'clicked', modalKind: 'kmGreet' },
    sceneResult: { status: 'ready', modalCount: 0, newTexts: [], editError: '', successModalCount: 0 },
  })
  try {
    const data = await sendZhilianGreeting(...args(absent))
    assert.equal(data.contentHash, absent.contentHash, '弹窗缺席不影响成功收场')
    assert.equal(absent.state.successDismissCalls, 0)
  } finally {
    absent.restore()
  }

  // 清场自身炸掉:成功判定不变。
  const broken = installM4GreetingOrchestrationFixture({
    commitResult: { status: 'clicked', modalKind: 'kmGreet' },
    sceneResult: { status: 'ready', modalCount: 0, newTexts: [], editError: '', successModalCount: 1 },
    dismissMode: 'throw',
  })
  try {
    const data = await sendZhilianGreeting(...args(broken))
    assert.equal(data.contentHash, broken.contentHash, '清场失败只留日志,不改变成功判定')
    assert.equal(broken.state.successDismissCalls, 1)
  } finally {
    broken.restore()
  }

  // 旧版形态:零探测零清场,成功路径一次页面扫描都不多做。
  const legacy = installM4GreetingOrchestrationFixture({})
  try {
    const data = await sendZhilianGreeting(...args(legacy))
    assert.equal(data.contentHash, legacy.contentHash)
    assert.equal(legacy.state.successDismissCalls, 0, '旧版没有成功弹窗,不得清场')
    assert.equal(legacy.state.sceneReads, 1,
      '旧版只保留既有的发送前取证基线读,成功路径零新增扫描与等待')
  } finally {
    legacy.restore()
  }
})

test('M4 招呼:新版阴性轮询里见到成功弹窗只清一趟,suspect 判定照旧', async () => {
  const fixture = installM4GreetingOrchestrationFixture({
    proofMode: 'negative',
    commitResult: { status: 'clicked', modalKind: 'kmGreet' },
    sceneResult: { status: 'ready', modalCount: 0, newTexts: [], editError: '', successModalCount: 1 },
  })
  const context = fixture.context()
  try {
    await assert.rejects(
      sendZhilianGreeting(
        { platformUserRef: fixture.refs.user, positionRef: fixture.refs.job, text: fixture.text },
        { expectUnestablished: true },
        context,
        fixture.fingerprint,
      ),
      (error) => {
        assert.ok(error instanceof ZhilianPlatformError)
        assert.equal(error.code, 'POSTCONDITION_UNCONFIRMED', '弹窗不是成功判据,阴性仍走 suspect')
        return true
      },
    )
    assert.equal(fixture.state.successDismissCalls, 1, '二十轮阴性轮询里至多清一趟')
    assert.equal(fixture.state.finalClicks, 1, '清场不补发送')
  } finally {
    fixture.restore()
  }
})

test('M4 招呼:关弹窗失败不推翻拒绝判定', async () => {
  const fixture = installM4GreetingOrchestrationFixture({
    proofMode: 'negative',
    sceneResult: { status: 'ready', modalCount: 1, newTexts: [], editError: '内容中涉及敏感词，请修改', successModalCount: 0 },
    closeMode: 'throw',
  })
  const context = fixture.context()
  try {
    await assert.rejects(
      sendZhilianGreeting(
        { platformUserRef: fixture.refs.user, positionRef: fixture.refs.job, text: fixture.text },
        { expectUnestablished: true },
        context,
        fixture.fingerprint,
      ),
      (error) => {
        assert.equal(error.code, 'GREETING_REJECTED')
        assert.equal(error.sideEffect, 'none')
        return true
      },
    )
    assert.equal(fixture.state.finalClicks, 1)
  } finally {
    fixture.restore()
  }
})

test('M4 招呼:错误位空白不得被当成拒绝,仍走原 suspect 轨', async () => {
  const fixture = installM4GreetingOrchestrationFixture({
    proofMode: 'negative',
    sceneResult: { status: 'ready', modalCount: 1, newTexts: [], editError: '', successModalCount: 0 },
  })
  const context = fixture.context()
  try {
    await assert.rejects(
      sendZhilianGreeting(
        { platformUserRef: fixture.refs.user, positionRef: fixture.refs.job, text: fixture.text },
        { expectUnestablished: true },
        context,
        fixture.fingerprint,
      ),
      (error) => {
        assert.equal(error.code, 'POSTCONDITION_UNCONFIRMED', '空错误位必须保持不确认,不得判失败')
        assert.equal(error.sideEffect, 'possible')
        return true
      },
    )
    assert.equal(fixture.state.modalCloseCalls, 0, '未判拒绝就不该动弹窗')
  } finally {
    fixture.restore()
  }
})

test('mainReadGreetingScene 读出弹窗内错误位,空白节点不算拒绝(2026-08-07 真机结构)', () => {
  const original = {
    document: globalThis.document,
    getComputedStyle: globalThis.getComputedStyle,
    HTMLElement: globalThis.HTMLElement,
  }
  class FakeElement {
    constructor(props = {}) {
      this.className = props.className ?? ''
      this.innerText = props.innerText ?? ''
      this.children = props.children ?? []
      this.visible = props.visible !== false
      this.position = props.position ?? 'static'
      this.descendants = props.descendants ?? []
    }
    getClientRects() { return this.visible ? [{}] : [] }
    closest() { return null }
    querySelector() { return null }
    querySelectorAll(selector) {
      return selector === '.ai-greeting-modal__edit-error'
        ? this.descendants.filter((d) => String(d.className).includes('edit-error'))
        : []
    }
  }
  const run = (errorNode) => {
    const modal = new FakeElement({
      className: 'ai-greeting-modal', position: 'fixed', descendants: errorNode ? [errorNode] : [],
    })
    globalThis.HTMLElement = FakeElement
    globalThis.getComputedStyle = (node) => ({
      display: 'block', visibility: 'visible', position: node.position ?? 'static',
    })
    globalThis.document = {
      body: { children: [] },
      querySelectorAll(selector) {
        if (selector === '.ai-greeting-modal') return [modal]
        return []
      },
    }
    return zhilianTestHooks.mainReadGreetingScene([])
  }
  try {
    // 真机实测:无错时节点仍在,innerText 是 "\n          \n        " 这样的纯空白。
    const blank = run(new FakeElement({
      className: 'ai-greeting-modal__edit-error', innerText: '\n          \n        ',
    }))
    assert.equal(blank.status, 'ready')
    assert.equal(blank.editError, '', '常驻空白节点必须读成空串,否则成功会被误判成拒绝')
    assert.equal(blank.modalCount, 1)

    const errored = run(new FakeElement({
      className: 'ai-greeting-modal__edit-error', innerText: '内容中涉及敏感词，请修改',
    }))
    assert.equal(errored.editError, '内容中涉及敏感词，请修改')

    const hidden = run(new FakeElement({
      className: 'ai-greeting-modal__edit-error', innerText: '内容中涉及敏感词，请修改', visible: false,
    }))
    assert.equal(hidden.editError, '', '不可见的错误位不算拒绝')

    assert.equal(run(null).editError, '', '没有错误位节点时为空串')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('mainReadGreetingScene 只报基线外的可见浮层并统计招呼弹窗', () => {
  const original = {
    document: globalThis.document,
    getComputedStyle: globalThis.getComputedStyle,
    HTMLElement: globalThis.HTMLElement,
  }
  class FakeElement {
    constructor(props = {}) {
      this.className = props.className ?? ''
      this.innerText = props.innerText ?? ''
      this.children = props.children ?? []
      this.visible = props.visible !== false
      this.position = props.position ?? 'static'
      this.insideModal = props.insideModal === true
      this.containsModal = props.containsModal === true
    }
    getClientRects() { return this.visible ? [{}] : [] }
    closest(selector) {
      return selector === '.ai-greeting-modal' && this.insideModal ? this : null
    }
    querySelector(selector) {
      return selector === '.ai-greeting-modal' && this.containsModal ? this : null
    }
    // 本例的弹窗里没有错误位:浮层扫描与 editError 判据互不干扰。
    querySelectorAll() { return [] }
  }
  const kmOld = new FakeElement({ className: 'km-message km-message--success', innerText: '消息已发送' })
  const kmNew = new FakeElement({ className: 'km-message km-message--error', innerText: '今日沟通人数已达上限' })
  const kmHidden = new FakeElement({ className: 'km-message', innerText: '隐藏提示', visible: false })
  const fixedPortal = new FakeElement({ position: 'fixed', innerText: '操作频繁，请稍后再试' })
  const staticRoot = new FakeElement({ position: 'static', innerText: '应用根很长的内容'.repeat(40), children: [fixedPortal] })
  const modal = new FakeElement({ className: 'ai-greeting-modal', position: 'fixed', innerText: '打招呼弹窗正文', containsModal: true })
  globalThis.HTMLElement = FakeElement
  globalThis.getComputedStyle = (node) => ({
    display: 'block', visibility: 'visible', position: node.position ?? 'static',
  })
  globalThis.document = {
    body: { children: [staticRoot, modal] },
    querySelectorAll(selector) {
      if (selector === '[class*="km-message"], [class*="km-toast"]') return [kmOld, kmNew, kmHidden]
      if (selector === '.ai-greeting-modal') return [modal]
      return []
    },
  }
  try {
    const result = zhilianTestHooks.mainReadGreetingScene(['消息已发送'])
    assert.equal(result.status, 'ready')
    assert.equal(result.modalCount, 1, '可见招呼弹窗必须计数')
    assert.deepEqual(
      result.newTexts,
      ['今日沟通人数已达上限', '操作频繁，请稍后再试'],
      '基线内文本与不可见提示不报;弹窗自身不算浮层;fixed portal 由兜底捕获',
    )
    const baseline = zhilianTestHooks.mainReadGreetingScene([])
    assert.ok(baseline.newTexts.includes('消息已发送'), '空基线返回全量作为基线')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('mainReadGreetingScene 统计新版 chat-set-greet 弹窗且不把它当浮层,editError 不从新版取', () => {
  const original = {
    document: globalThis.document,
    getComputedStyle: globalThis.getComputedStyle,
    HTMLElement: globalThis.HTMLElement,
  }
  class FakeElement {
    constructor(props = {}) {
      this.className = props.className ?? ''
      this.innerText = props.innerText ?? ''
      this.children = props.children ?? []
      this.visible = props.visible !== false
      this.position = props.position ?? 'static'
      this.closestKm = null
    }
    getClientRects() { return this.visible ? [{}] : [] }
    closest(selector) {
      return selector === '.chat-set-greet' ? this.closestKm : null
    }
    querySelector() { return null }
    querySelectorAll() { return [] }
  }
  const kmModal = new FakeElement({ className: 'km-modal', position: 'fixed', innerText: '选择打招呼语' })
  // 校验槽常驻:无错时纯空白,有错时是平台拒绝原话(2026-08-24 真机 DOM)。
  const invalidSlot = new FakeElement({
    className: 'km-form-item__invalid', innerText: '\n          \n        ',
  })
  kmModal.querySelectorAll = (selector) => selector === '.km-form-item__invalid' ? [invalidSlot] : []
  const kmWrapper = new FakeElement({
    className: 'km-modal__wrapper chat-set-greet', position: 'fixed',
    innerText: '选择打招呼语', children: [kmModal],
  })
  kmWrapper.closestKm = kmWrapper
  kmModal.closestKm = kmWrapper
  const toast = new FakeElement({ position: 'fixed', innerText: '操作频繁，请稍后再试' })
  const successModal = new FakeElement({ className: 'km-modal', position: 'fixed', innerText: '招呼语已发送' })
  globalThis.HTMLElement = FakeElement
  globalThis.getComputedStyle = (node) => ({
    display: 'block', visibility: 'visible', position: node.position ?? 'static',
  })
  globalThis.document = {
    body: { children: [kmWrapper, toast] },
    querySelectorAll(selector) {
      if (selector === '.chat-set-greet .km-modal') return [kmModal]
      if (selector === '.set-greet-success-modal .km-modal') return [successModal]
      return []
    },
  }
  try {
    const blank = zhilianTestHooks.mainReadGreetingScene([])
    assert.equal(blank.status, 'ready')
    assert.equal(blank.modalCount, 1, '可见的新版招呼弹窗必须计数')
    assert.equal(blank.successModalCount, 1, '「招呼语已发送」成功弹窗单独计数')
    assert.equal(blank.editError, '', '常驻空白校验槽必须读成空串,否则成功会被误判成拒绝')
    assert.deepEqual(blank.newTexts, ['操作频繁，请稍后再试'],
      '新版弹窗自身不算浮层;fixed portal 照常由兜底捕获')

    invalidSlot.innerText = '内容中涉及联系方式等敏感词，请修改后保存'
    const errored = zhilianTestHooks.mainReadGreetingScene([])
    assert.equal(errored.editError, '内容中涉及联系方式等敏感词，请修改后保存',
      '新版校验槽有话即平台明确业务拒绝')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('mainCloseGreetingModal 对新版 chat-set-greet 弹窗同样只点标准关闭键', () => {
  const original = {
    document: globalThis.document,
    getComputedStyle: globalThis.getComputedStyle,
    HTMLElement: globalThis.HTMLElement,
  }
  let closeClicks = 0
  class FakeElement {
    constructor() {
      this.form = null
      this.type = 'button'
    }
    getClientRects() { return [{}] }
    click() { closeClicks += 1 }
    querySelectorAll() { return [] }
  }
  const closeButton = new FakeElement()
  const kmModal = new FakeElement()
  kmModal.querySelectorAll = (selector) => selector === '.km-modal__close-btn' ? [closeButton] : []
  globalThis.HTMLElement = FakeElement
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.document = {
    querySelectorAll(selector) {
      if (selector === '.chat-set-greet .km-modal') return [kmModal]
      return []
    },
  }
  try {
    assert.deepEqual(zhilianTestHooks.mainCloseGreetingModal(), { closed: true })
    assert.equal(closeClicks, 1, '只点弹窗自身的关闭控件一次')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('mainDismissGreetingSuccessModals 一趟全关:首选「我知道了」,缺了才点 X,别的不碰', async () => {
  const original = {
    document: globalThis.document,
    getComputedStyle: globalThis.getComputedStyle,
    HTMLElement: globalThis.HTMLElement,
    setTimeout: globalThis.setTimeout,
  }
  globalThis.setTimeout = (callback, _delay, ...args) => {
    queueMicrotask(() => callback(...args))
    return 1
  }
  class FakeElement {
    constructor(text = '', props = {}) {
      this.textContent = text
      this.isConnected = true
      this.form = null
      this.type = 'button'
      this.disabled = props.disabled === true
      this.visible = props.visible !== false
      this.clicks = 0
      this.tagName = props.tagName ?? 'BUTTON'
      this._onIntrinsicClick = null
    }
    getAttribute() { return '' }
    getClientRects() { return this.visible ? [{}] : [] }
    click() {
      this.clicks += 1
      if (typeof this._onIntrinsicClick === 'function') this._onIntrinsicClick()
    }
    querySelectorAll() { return [] }
  }
  const makeModal = ({ ack, close, extra }) => {
    const modal = new FakeElement('', { tagName: 'DIV' })
    modal.querySelectorAll = (selector) => {
      if (selector === 'button[type="button"]') return [ack, extra].filter(Boolean)
      if (selector === '.km-modal__close-btn') return close ? [close] : []
      return []
    }
    // 任一控件被点即视为该弹窗已关闭(从 roots 复查中消失)。
    for (const node of [ack, close]) {
      if (node) node._onIntrinsicClick = () => { modal.visible = false }
    }
    return modal
  }
  const ackA = new FakeElement('我知道了')
  const closeA = new FakeElement('')
  const extraA = new FakeElement('查看设置')
  const modalA = makeModal({ ack: ackA, close: closeA, extra: extraA })
  const closeB = new FakeElement('')
  const modalB = makeModal({ ack: null, close: closeB })
  globalThis.HTMLElement = FakeElement
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.document = {
    querySelectorAll(selector) {
      if (selector === '.set-greet-success-modal .km-modal') {
        return [modalA, modalB].filter((modal) => modal.visible)
      }
      return []
    },
  }
  try {
    const outcome = await zhilianTestHooks.mainDismissGreetingSuccessModals()
    assert.equal(outcome.found, true)
    assert.equal(outcome.closed, true, '两个弹窗都点掉后 closed 为真')
    assert.equal(outcome.remaining, 0)
    assert.equal(ackA.clicks, 1, '有唯一「我知道了」时点它')
    assert.equal(closeA.clicks, 0, '「我知道了」在场时不再碰 X')
    assert.equal(closeB.clicks, 1, '没有「我知道了」的弹窗点它自己的 X')
    assert.equal(extraA.clicks, 0, '其它按钮一个不碰')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('mainDismissGreetingSuccessModals:禁用的「我知道了」退到 X,零弹窗零动作,关不掉如实报', async () => {
  const original = {
    document: globalThis.document,
    getComputedStyle: globalThis.getComputedStyle,
    HTMLElement: globalThis.HTMLElement,
    setTimeout: globalThis.setTimeout,
  }
  globalThis.setTimeout = (callback, _delay, ...args) => {
    queueMicrotask(() => callback(...args))
    return 1
  }
  class FakeElement {
    constructor(text = '', props = {}) {
      this.textContent = text
      this.isConnected = true
      this.form = null
      this.type = 'button'
      this.disabled = props.disabled === true
      this.visible = props.visible !== false
      this.clicks = 0
      this.tagName = props.tagName ?? 'BUTTON'
    }
    getAttribute() { return '' }
    getClientRects() { return this.visible ? [{}] : [] }
    click() { this.clicks += 1 }
    querySelectorAll() { return [] }
  }
  globalThis.HTMLElement = FakeElement
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })

  const disabledAck = new FakeElement('我知道了', { disabled: true })
  const closeBtn = new FakeElement('')
  const stuckModal = new FakeElement('', { tagName: 'DIV' })
  stuckModal.querySelectorAll = (selector) => {
    if (selector === 'button[type="button"]') return [disabledAck]
    if (selector === '.km-modal__close-btn') return [closeBtn]
    return []
  }
  globalThis.document = {
    querySelectorAll(selector) {
      return selector === '.set-greet-success-modal .km-modal' ? [stuckModal] : []
    },
  }
  try {
    // 点了 X 但弹窗纹丝不动:如实报 remaining,绝不重复点。
    const stuck = await zhilianTestHooks.mainDismissGreetingSuccessModals()
    assert.equal(disabledAck.clicks, 0, '禁用的「我知道了」不点')
    assert.equal(closeBtn.clicks, 1, '退到弹窗自己的 X')
    assert.equal(stuck.found, true)
    assert.equal(stuck.closed, false, '关不掉必须如实报,不装成功')
    assert.equal(stuck.remaining, 1)

    globalThis.document = { querySelectorAll() { return [] } }
    assert.deepEqual(await zhilianTestHooks.mainDismissGreetingSuccessModals(), {
      found: false, closed: false, remaining: 0, scene: '',
    }, '零弹窗零动作')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('mainDismissGlobalSceneModals 两族叠弹全关:标准 X 优先,优惠券点自己的 __close 图标,其余不碰', async () => {
  const original = {
    document: globalThis.document,
    getComputedStyle: globalThis.getComputedStyle,
    HTMLElement: globalThis.HTMLElement,
    setTimeout: globalThis.setTimeout,
  }
  globalThis.setTimeout = (callback, _delay, ...args) => {
    queueMicrotask(() => callback(...args))
    return 1
  }
  class FakeElement {
    constructor(text = '', props = {}) {
      this.textContent = text
      this.isConnected = true
      this.form = null
      this.type = 'button'
      this.disabled = props.disabled === true
      this.visible = props.visible !== false
      this.clicks = 0
      this.tagName = props.tagName ?? 'BUTTON'
      this.className = props.className ?? ''
      this._onIntrinsicClick = null
    }
    getAttribute(name) { return name === 'class' ? this.className : '' }
    getClientRects() { return this.visible ? [{}] : [] }
    click() {
      this.clicks += 1
      if (typeof this._onIntrinsicClick === 'function') this._onIntrinsicClick()
    }
    querySelectorAll() { return [] }
  }
  // 图片广告形态:头部有唯一标准 X。
  const stdClose = new FakeElement('', { className: 'km-modal__close-btn km-button' })
  const imageModal = new FakeElement('', {
    tagName: 'DIV', className: 'km-modal__wrapper image-popup-modal',
  })
  imageModal.querySelectorAll = (selector) => {
    if (selector === 'button.km-modal__close-btn') return [stdClose]
    // 真实 DOM 里 [class*="__close"] 也会捞到 X 自己,endsWith 过滤要能扛住。
    if (selector === '[class*="__close"]') return [stdClose]
    return []
  }
  // 优惠券形态:没有标准 X,关闭控件是它自己的 *__close 图标;「去使用」在场但
  // 不属于任何关闭选择器;同名前缀的 __close-tip 装饰节点必须被 endsWith 排掉。
  const useButton = new FakeElement('去使用')
  const closeIcon = new FakeElement('', {
    tagName: 'I', className: 'km-icon sati sati-times-circle coupons-auto-modal__close',
  })
  const closeTip = new FakeElement('', {
    tagName: 'DIV', className: 'coupons-auto-modal__close-tip',
  })
  const couponModal = new FakeElement('', {
    tagName: 'DIV', className: 'km-modal__wrapper coupons-auto-modal',
  })
  couponModal.querySelectorAll = (selector) => {
    if (selector === 'button.km-modal__close-btn') return []
    if (selector === '[class*="__close"]') return [closeTip, closeIcon]
    return []
  }
  for (const [modal, control] of [[imageModal, stdClose], [couponModal, closeIcon]]) {
    control._onIntrinsicClick = () => { modal.visible = false }
  }
  globalThis.HTMLElement = FakeElement
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.document = {
    querySelectorAll(selector) {
      // 选择器就是判据:必须带 body 直挂与 scene="GLOBAL",别的写法一律查不到。
      if (selector === 'body > .km-modal__wrapper[scene="GLOBAL"]') {
        return [imageModal, couponModal].filter((modal) => modal.visible)
      }
      return []
    },
  }
  try {
    const outcome = await zhilianTestHooks.mainDismissGlobalSceneModals()
    assert.equal(outcome.found, true)
    assert.equal(outcome.closed, true, '两族弹窗都关掉后 closed 为真')
    assert.equal(outcome.remaining, 0)
    assert.equal(stdClose.clicks, 1, '图片广告点它自己的标准 X')
    assert.equal(closeIcon.clicks, 1, '优惠券点它自己的 __close 图标')
    assert.equal(closeTip.clicks, 0, '__close-tip 装饰节点不碰')
    assert.equal(useButton.clicks, 0, '「去使用」一次都不碰')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('mainDismissGlobalSceneModals:关闭控件不唯一就不动并如实报,零弹窗零动作', async () => {
  const original = {
    document: globalThis.document,
    getComputedStyle: globalThis.getComputedStyle,
    HTMLElement: globalThis.HTMLElement,
    setTimeout: globalThis.setTimeout,
  }
  globalThis.setTimeout = (callback, _delay, ...args) => {
    queueMicrotask(() => callback(...args))
    return 1
  }
  class FakeElement {
    constructor(text = '', props = {}) {
      this.textContent = text
      this.isConnected = true
      this.form = null
      this.type = 'button'
      this.disabled = props.disabled === true
      this.visible = props.visible !== false
      this.clicks = 0
      this.tagName = props.tagName ?? 'BUTTON'
      this.className = props.className ?? ''
    }
    getAttribute(name) { return name === 'class' ? this.className : '' }
    getClientRects() { return this.visible ? [{}] : [] }
    click() { this.clicks += 1 }
    querySelectorAll() { return [] }
  }
  globalThis.HTMLElement = FakeElement
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  const closeOne = new FakeElement('', { className: 'km-modal__close-btn' })
  const closeTwo = new FakeElement('', { className: 'km-modal__close-btn' })
  const oddModal = new FakeElement('', {
    tagName: 'DIV', className: 'km-modal__wrapper mystery-global-modal',
  })
  oddModal.querySelectorAll = (selector) => {
    if (selector === 'button.km-modal__close-btn') return [closeOne, closeTwo]
    if (selector === '[class*="__close"]') return [closeOne, closeTwo]
    return []
  }
  globalThis.document = {
    querySelectorAll(selector) {
      return selector === 'body > .km-modal__wrapper[scene="GLOBAL"]' ? [oddModal] : []
    },
  }
  try {
    const stuck = await zhilianTestHooks.mainDismissGlobalSceneModals()
    assert.equal(closeOne.clicks + closeTwo.clicks, 0, '关闭控件不唯一时一个都不点')
    assert.equal(stuck.found, true)
    assert.equal(stuck.closed, false, '没关掉必须如实报')
    assert.equal(stuck.remaining, 1)
    assert.ok(stuck.scene.includes('noDismissBtn'), 'scene 记下没找到关闭控件')

    globalThis.document = { querySelectorAll() { return [] } }
    assert.deepEqual(await zhilianTestHooks.mainDismissGlobalSceneModals(), {
      found: false, closed: false, remaining: 0, scene: '',
    }, '零弹窗零动作')
  } finally {
    Object.assign(globalThis, original)
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
      assert.deepEqual(await fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
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
      assert.deepEqual(await fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
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
    assert.equal((await fixture.invoke({
      status: 'ready',
      stage: 'ready',
      serverSourceKeys: [],
      targetBindingToken: m3Hash(JSON.stringify([fixture.conversationRef, fixture.peerRef])),
    }, 'preflight', { expectedTail: [] })).status, 'failed',
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
      assert.deepEqual(await fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
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
      (await fixture.invoke(nominalBaseline, 'preflight', { expectedTail: [] })).status,
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
      assert.deepEqual(await fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
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
      assert.deepEqual(await fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
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
      assert.deepEqual(await fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
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
      assert.deepEqual(await fixture.invoke(baseline, 'preflight', { expectedTail }), { status: 'ready' },
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
    // 草稿非空不再挡切会话(2026-09-03 撤销 composer.empty);下面这次因 sessionId 歧义被拒,
    // 与草稿无关——草稿留在框里原样不动。
    composer.value = '人工草稿'
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
    KeyboardEvent: globalThis.KeyboardEvent,
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
  globalThis.KeyboardEvent = FixtureEvent

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
    // 页面数据优先探测(probe_page_source)会查复合选择器;本 fixture 不建
    // 页面时间线通道,统一返回"查无"让读取走平台接口兜底。
    querySelector(selector) {
      const matches = this.querySelectorAll(selector)
      return matches.length > 0 ? matches[0] : null
    },
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

    // 2026-08-04 甲方裁决：消息基线降为观测模式。尾部对不上不再拒绝发送，
    // 而是照常算出基线并带回漂移事实（只有方向，不含正文）供日志留痕。
    // 路由变化、会话不可用等"读不到/错靶"方向仍然照旧硬拒。
    const wrongTail = await fixture.capture([{ direction: 'in', contentHash: 'f'.repeat(64) }])
    assert.equal(wrongTail.status, 'ready')
    assert.equal(wrongTail.tailDrifted, true)
    assert.ok(Array.isArray(wrongTail.tailDirections))
    assert.deepEqual(
      wrongTail.serverSourceKeys,
      [m3Hash('source-v1|server-m3-baseline-1')],
      '漂移不得影响发后正证要用的基线 source keys',
    )

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
    assert.deepEqual(await fixture.invoke(baseline), { status: 'ready' },
      '私有对象变化与 disabled/aria-disabled 不得阻断 evaluator')

    fixture.button.form = {}
    for (const type of ['', 'submit', 'reset']) {
      fixture.button.type = type
      const rejected = await fixture.invoke(baseline)
      assert.equal(rejected.status, 'failed', `关联 form 的 ${type || '缺省'} type 必须拒绝`)
      assert.equal(fixture.state.intrinsicClicks, 0)
    }
    fixture.button.type = 'button'
    assert.deepEqual(await fixture.invoke(baseline), { status: 'ready' })
    fixture.button.form = null

    const cases = [
      ['route', () => { globalThis.location.href = 'https://rd6.zhaopin.com/app/im?sessionId=other' }, {}, 'route_changed'],
      ['identity', () => {}, { fingerprint: 'f'.repeat(64) }, 'identity_changed'],
      ['target token', () => {}, { targetToken: 'c'.repeat(64) }, 'target_changed'],
      ['deadline', () => {}, { deadline: Date.now() - 1 }, 'action_window_elapsed'],
    ]
    for (const [name, mutate, overrides, reason] of cases) {
      const currentHref = globalThis.location.href
      mutate()
      const result = await fixture.invoke(baseline, 'preflight', overrides)
      assert.deepEqual(result, { status: 'failed', reason }, `${name} 必须闭锁`)
      globalThis.location.href = currentHref
    }

    // 2026-08-04 甲方裁决：消息基线降为观测模式。source keys 与 expectedTail
    // 漂移只记日志、照常放行；"读不到"方向（guard_unresolved）仍照旧硬拒。
    for (const [name, overrides] of [
      ['source keys 漂移', { baselineKeys: ['e'.repeat(64)] }],
      ['expected tail 漂移', { expectedTail: [{ direction: 'in', contentHash: 'd'.repeat(64) }] }],
    ]) {
      assert.deepEqual(await fixture.invoke(baseline, 'preflight', overrides), { status: 'ready' },
        `${name} 按观测模式放行`)
      assert.equal(fixture.state.intrinsicClicks, 0)
    }

    const extraDetail = { getClientRects() { return [{}] } }
    fixture.state.details = [fixture.detail, extraDetail]
    assert.deepEqual(await fixture.invoke(baseline), { status: 'failed', reason: 'composer_missing' })
    fixture.state.details = [fixture.detail]

    const extraComposer = Object.create(Object.getPrototypeOf(fixture.composer))
    extraComposer._value = ''
    extraComposer.isConnected = true
    extraComposer.closest = fixture.composer.closest.bind(fixture.composer)
    extraComposer.getClientRects = () => [{}]
    fixture.state.composers = [fixture.composer, extraComposer]
    assert.deepEqual(await fixture.invoke(baseline), { status: 'failed', reason: 'composer_missing' })
    fixture.state.composers = [fixture.composer]

    const extraButton = Object.assign(Object.create(Object.getPrototypeOf(fixture.button)), {
      textContent: '发送', form: null, type: 'button', isConnected: true,
      getClientRects() { return [{}] },
      closest: fixture.button.closest,
    })
    fixture.state.buttons = [fixture.button, extraButton]
    assert.deepEqual(await fixture.invoke(baseline), { status: 'failed', reason: 'composer_missing' })
    fixture.state.buttons = [fixture.button]

    const originalButtonClosest = fixture.button.closest
    fixture.button.closest = (selector) => selector === '.im-session-detail' ? fixture.detail : null
    assert.deepEqual(await fixture.invoke(baseline), { status: 'failed', reason: 'composer_missing' })
    fixture.button.closest = originalButtonClosest

    // 草稿非空不再拒:preflight 就绪,由写入整段覆盖(2026-09-03 撤销 composer.empty)。
    fixture.composer.value = '人工草稿'
    assert.deepEqual(await fixture.invoke(baseline), { status: 'ready' })
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
    assert.deepEqual(await fixture.invoke(baseline, 'preflight'), { status: 'ready' })
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

    assert.deepEqual(await fixture.invoke(baseline, 'commit'), { status: 'clicked' })
    assert.equal(fixture.state.intrinsicClicks, 1)
    assert.equal(fixture.state.instanceClicks, 0, '不得调用页面替换过的 instance click')
    assert.equal(fixture.state.valueAtClick, fixture.text)
    // 970d443 起写入事件序列对齐旧产品：beforeinput/input 配对，keyup 收尾。
    assert.deepEqual(fixture.state.inputEvents.map(({ type }) => type), [
      'beforeinput', 'input', 'change', 'keyup',
    ])
  } finally {
    fixture.restore()
  }
})

test('M3 写后正文被页面改写时清空草稿且零 click', async () => {
  const fixture = installM3SendFixture()
  try {
    const baseline = await fixture.capture()
    fixture.state.rewriteInsertedText = true
    const result = await fixture.invoke(baseline, 'commit')
    assert.deepEqual(result, { status: 'failed', reason: 'input_rejected' })
    assert.equal(fixture.composer.value, '')
    assert.equal(fixture.state.intrinsicClicks, 0)
    // 前四项是写入序列（970d443 对齐旧产品），后两项是 restoreDraft 清草稿。
    assert.deepEqual(fixture.state.inputEvents.map(({ type }) => type), [
      'beforeinput', 'input', 'change', 'keyup', 'input', 'change',
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
    // matchedTimeMs = 夹具行 time(秒值 2)按生产同一换算放大为毫秒。
    // matchedSourceKey = 命中行 idServer 按冻结 source-v1 配方派生(S1 回执补 ID)。
    assert.deepEqual(await observe(), {
      selected: true,
      matchingNewServerMessages: 1,
      matchedSourceKey: m3Hash('source-v1|server-m3-out-2'),
      matchedTimeMs: 2000,
    })
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
    assert.deepEqual(await observe(), {
      selected: true,
      matchingNewServerMessages: 1,
      matchedSourceKey: m3Hash('source-v1|server-m3-window-new-ok'),
      matchedTimeMs: 65000,
    }, 'baseline=64 时只允许窗口左移一格并追加唯一成功文本')

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
    // 草稿非空不再拒(2026-09-03 撤销 composer.empty):preflight 就绪,不产生点击。
    fixture.composer.value = '人工草稿'
    assert.deepEqual(invoke('wechatInvite', null, 'preflight'), { status: 'ready' })
    fixture.composer.value = ''

    const originalHref = globalThis.location.href
    globalThis.location.href = 'https://rd6.zhaopin.com/app/im?sessionId=other'
    assert.deepEqual(invoke('wechatInvite', null), { status: 'failed', reason: 'route_changed' })
    globalThis.location.href = originalHref

    assert.deepEqual(invoke('wechatInvite', null, 'preflight', {
      fingerprint: '0'.repeat(64),
    }), { status: 'failed', reason: 'identity_changed' })
    // 2026-08-04 甲方裁决:消息基线漂移只记不停手,统一适用于全部复用该
    // guards 的发送原语,卡片路径同样放行;身份/目标漂移仍硬拒。
    assert.deepEqual(invoke('wechatInvite', null, 'preflight', {
      baselineKeys: ['1'.repeat(64)],
    }), { status: 'ready' })
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
    // 2026-08-04 甲方裁决:消息基线漂移只记不停手,微信接受路径同样放行。
    assert.deepEqual(invoke('preflight', { baselineKeys: ['f'.repeat(64)] }), {
      status: 'ready',
      wechatCopyCards: 0,
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

// 2026-08-06 甲方裁决:收号改认结果消息自身。真机故障(客户机 2026-08-06)是我方
// 那张请求卡早已滚出平台会话首屏窗口,数组里只剩带号的结果消息,旧实现锚不到卡
// 即阴性,收号从延迟恶化为永久失败。带锚形态(acceptWechat 配套验证读)必须逐字
// 照旧,故每段都同时回归带锚行为。
test('M5-B 微信结果只读面无锚形态:请求卡不在窗口内仍收号,带锚形态照旧要求锚', async () => {
  const fixture = installM3SendFixture()
  try {
    const reset = () => fixture.rows.splice(0, fixture.rows.length)
    const readNoAnchor = () => zhilianTestHooks.mainReadWechatExchangeOutcome(
      fixture.conversationRef,
      null,
      null,
      null,
    )
    const readAnchored = (requestID) => zhilianTestHooks.mainReadWechatExchangeOutcome(
      fixture.conversationRef,
      m3Hash(`source-v1|${requestID}`),
      null,
      null,
    )

    // 我方发起(originType=1):候选人点同意,259 归对方(in)。数组里没有请求卡。
    reset()
    appendM5BWechatRow(fixture, {
      idServer: 'origin-one-result',
      type: 259,
      originType: 1,
      from: fixture.peerRef,
      userWeChat: 'peer_no_anchor',
      staffWeChat: 'staff_no_anchor',
    })
    assert.deepEqual(await readNoAnchor(), {
      confirmed: true,
      exchangeSourceKey: m3Hash('source-v1|origin-one-result'),
      peerWechat: 'peer_no_anchor',
    })
    // 同一份数据带锚时照旧阴性——带锚路径没有被放宽。
    assert.deepEqual(await readAnchored('absent-request'), { confirmed: false })

    // 形态 A(候选人发起 originType=2,259 归我方 out)无锚同样认。
    reset()
    appendM5BWechatRow(fixture, {
      idServer: 'origin-two-result',
      type: 259,
      originType: 2,
      from: fixture.staffId,
      userWeChat: 'peer_form_a',
      staffWeChat: 'staff_form_a',
    })
    assert.deepEqual(await readNoAnchor(), {
      confirmed: true,
      exchangeSourceKey: m3Hash('source-v1|origin-two-result'),
      peerWechat: 'peer_form_a',
    })

    // 多条结果消息:带锚形态判歧义阴性,无锚形态按"满足条件的最新一条即本次"取末条。
    reset()
    appendM5BWechatRow(fixture, {
      idServer: 'older-result',
      type: 259,
      originType: 1,
      from: fixture.peerRef,
      userWeChat: 'peer_older',
      staffWeChat: 'staff_older',
    })
    appendM5BWechatRow(fixture, {
      idServer: 'newer-result',
      type: 259,
      originType: 1,
      from: fixture.peerRef,
      userWeChat: 'peer_newer',
      staffWeChat: 'staff_newer',
    })
    assert.deepEqual(await readNoAnchor(), {
      confirmed: true,
      exchangeSourceKey: m3Hash('source-v1|newer-result'),
      peerWechat: 'peer_newer',
    })

    // 判据本身一条都不放宽:方向与 originType 配不上即不是结果消息。
    reset()
    appendM5BWechatRow(fixture, {
      idServer: 'mismatched-pair',
      type: 259,
      originType: 1,
      from: fixture.staffId,
      userWeChat: 'peer_mismatch',
      staffWeChat: 'staff_mismatch',
    })
    assert.deepEqual(await readNoAnchor(), { confirmed: false })

    // 双微信字段必须齐全。
    reset()
    appendM5BWechatRow(fixture, {
      idServer: 'missing-staff-field',
      type: 259,
      originType: 1,
      from: fixture.peerRef,
      userWeChat: 'peer_only',
    })
    assert.deepEqual(await readNoAnchor(), { confirmed: false })

    // 只有请求卡、没有结果消息:无号可收,阴性。
    reset()
    appendM5BWechatRow(fixture, { idServer: 'only-a-request' })
    assert.deepEqual(await readNoAnchor(), { confirmed: false })
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

test('M5-B 卡片 observer 接受现场面试形态并把缺席 endsAt 投影为空串', async () => {
  const fixture = installM3SendFixture()
  try {
    const baselineRows = structuredClone(fixture.rows)
    const baseline = await fixture.capture()
    assert.equal(baseline.status, 'ready')
    // 现场面试：平台 interviewType="ATTENDANCE"，不下发 interviewPlatform，
    // endTime 恒为 "0"（2026-07-31 真机）；命令侧 endsAt 必须缺席。
    const onsite = { startsAt: 1_800_000_000_000, method: 'onsite' }
    const observeOnsite = () => zhilianTestHooks.mainObserveStableOutboundCard(
      fixture.conversationRef,
      'interviewInvite',
      onsite,
      baseline.serverSourceKeys,
      baseline.targetBindingToken,
    )
    appendM3CardRow(fixture, {
      idServer: 'server-m5b-onsite-1',
      cardKind: 'interviewInvite',
      overrides: {
        interviewType: 'ATTENDANCE',
        interviewPlatform: undefined,
        endTime: '0',
        staffTitle: '现场面试邀请',
      },
    })
    const invite = await observeOnsite()
    assert.equal(invite.selected, true)
    assert.equal(invite.matchingNewServerMessages, 1)
    assert.deepEqual(invite.interview, { startsAt: onsite.startsAt, method: 'onsite' })
    assert.equal(
      invite.sourceKey,
      m3Hash('source-v1|server-m5b-onsite-1'),
      '现场面试卡同样必须返回稳定服务端消息身份的 sourceKey',
    )
    assert.equal(
      invite.contentHash,
      m3Hash(['card', 'interviewInvite', String(onsite.startsAt), '', 'onsite'].join('\x1f')),
      '缺席 endsAt 必须投影为空串,分隔符位数不变,method 取实际归一化值',
    )

    // 形态错配不得成立：命令要现场，页面来的却是线上卡。
    fixture.rows.splice(0, fixture.rows.length, ...structuredClone(baselineRows))
    appendM3CardRow(fixture, {
      idServer: 'server-m5b-onsite-2',
      cardKind: 'interviewInvite',
    })
    assert.equal(
      (await observeOnsite()).matchingNewServerMessages,
      0,
      '命令为 onsite 时线上形态的 355 不得形成正证',
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
            ...(observePositive ? { matchedSourceKey: m3Hash('source-v1|server-send-orchestration') } : {}),
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
    assert.equal(result.sourceKey, m3Hash('source-v1|server-send-orchestration'),
      '成功回执必须携带命中行按 source-v1 配方派生的 sourceKey(S1 回执补 ID)')
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
      // 2026-08-04 消息基线降观测后 capture 不再产出该阶段(改为 ready +
      // tailDrifted);若从陈旧 MAIN world 传来,按"无法建立可信基线"兜底。
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
        (error) => error instanceof ZhilianPlatformError && error.code === ErrorCode.CtxNotReady,
        `${stage} 应映射为 CTX_NOT_READY`,
      )
      assert.equal(failedBaseline.state.barriers, 0, `${stage} 必须停在 witness barrier 之前`)
      assert.equal(mainSendCalls, 4, `${stage} 不得调用同步 MAIN evaluator`)
      assert.equal(baselineCalls - failedBaselineStart, 1, `${stage} 不得触发第二次 capture`)
    }

    for (const stage of [
      'route_changed',
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
    // 邀面卡抓两次基线：首抓给 evaluator，编辑器准备完之后再抓一次作为观测
    // 基准。准备现场面试编辑器真机要十几秒，平台可能在这期间自行插入引导行，
    // 沿用首抓基线会让"相对基线恰好新增一条"落空。抓基线是纯只读，多抓一次
    // 不产生任何副作用；真正约束副作用的是下面的 barriers/commitCalls。
    assert.equal(baselineCalls - inviteBaselineStart, 2)
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
    assert.equal(outcomeReads - readsBefore, 80,
      '接受动作后置观察总预算固定为 80×250ms，不扩大到第二次 click')
  } finally {
    globalThis.chrome = originalChrome
    globalThis.setTimeout = originalSetTimeout
  }
})

test('会话切换的 click task 排在 cancellation barrier 之后，等待异常照实抛出', async () => {
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
  } finally {
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
        error.retryable === 'afterRecovery' &&
        error.message.includes('list_binding_unresolved'),
      '列表绑定异常按渲染暂态处理:本轮跳过下轮重试且原始信息随行(2026-08-26 甲方裁决),点击照旧为零',
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
    querySelector: () => null,
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
        // 注入机制已上移到 platform/inject.ts,抛的是平台无关的 PlatformError。
        // 判据(已知平台失败 + CTX_NOT_READY + contentScriptDead)一个没动;
        // ZhilianPlatformError 是它的子类,原语层捕获 PlatformError 两者通吃。
        assert.ok(error instanceof PlatformError)
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
      assert.ok(error instanceof PlatformError)
      assert.equal(error.code, 'CTX_NOT_READY')
      // 平台名仍进消息,好让日志一眼看出是哪个平台的页面没就绪。
      assert.match(error.message, /智联/u)
      assert.match(error.message, /页面上下文已销毁/u)
      return true
    },
  )
})

test('智联线程页面 API 不响应时由 MAIN 本地截止响亮释放', async () => {
  const conversationRef = 'conversation-history-timeout'
  globalThis.location = { href: `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}` }
  globalThis.document = { scripts: [], querySelector: () => null }
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

test('智联线程 DOM 回退：无 Vue 数据时不回退 initial timeline，响亮报通道失效', async () => {
  // 2026-08-03 起 initial timeline(SSR 静态快照)不再作为消息回退:它不随
  // 新消息更新,用作发后验证读会系统性误判。两级页面通道(timeline props 与
  // Vuex getter)都取不到时,必须以 thread_page_source_unavailable 响亮失败
  // 交脑侧处理,不得把静态快照或"取不到"当成可信读数。
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
    querySelectorAll: () => [],
  }
  globalThis.window = { $session: null }

  const page = await zhilianTestHooks.mainReadThreadPage(conversationRef, 8, null)
  assert.match(page.__recruitHelperMainError, /thread_page_source_unavailable/u,
    '页面通道取不到数据必须响亮失败,不得回退 SSR 静态快照')
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
        if (func.name === 'mainProbeZhilianBlockedDialog') {
          return [{ result: { status: 'absent' } }]
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

test('ensureZhilianIM 双页现场:已有健康沟通页就直接复用,真人另开的推荐页一动不动', async () => {
  // 2026-09-08 甲方裁决翻转了旧优先级(旧:只要有推荐页就优先把它导航到 IM)。回复巡检轮
  // 每轮起手都派 ensureSurface,稳态下必须是空操作;否则真人在同一个 Chrome 里开一张
  // 推荐页,下一轮就被劫持。
  const originalChrome = globalThis.chrome
  const fingerprint = 'e'.repeat(64)
  const recommendTab = {
    id: 72,
    url: 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-job',
    status: 'complete',
    active: true,
    lastAccessed: 300,
    windowId: 1,
  }
  const existingIMTab = {
    id: 73,
    url: 'https://rd6.zhaopin.com/app/im',
    status: 'complete',
    active: false,
    lastAccessed: 200,
    windowId: 1,
  }
  const updated = []
  const probed = []
  try {
    globalThis.chrome = {
      tabs: {
        async query() {
          return [{ ...recommendTab }, { ...existingIMTab }]
        },
        async create() {
          throw new Error('双页现场不得新建第三张智联标签')
        },
        async update(id, options) {
          updated.push({ id, options })
          throw new Error(`已有健康沟通页时不得导航任何标签(试图动 ${id})`)
        },
        async reload(id) {
          throw new Error(`健康的沟通页不该被 reload(${id})`)
        },
        async get(id) {
          assert.equal(id, existingIMTab.id, '后续读取只该盯着复用的那张沟通页')
          return { ...existingIMTab }
        },
        async sendMessage(id) {
          assert.ok(id === recommendTab.id || id === existingIMTab.id)
          return { ok: true }
        },
      },
      scripting: {
        async executeScript({ target }) {
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
    assert.deepEqual(updated, [], '有健康沟通页就是空操作,不导航、不激活')
    assert.ok(probed.length >= 2 && probed.every((id) => id === existingIMTab.id),
      `探测与清场都该落在复用的沟通页上: ${JSON.stringify(probed)}`)
    assert.equal(recommendTab.url, 'https://rd6.zhaopin.com/app/recommend?jobNumber=fixture-job',
      '真人开着的推荐页不得被导航')
    assert.equal(recommendTab.active, true, '真人开着的推荐页不得被切走焦点')
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('ensureZhilianIM 没有沟通页、却有两张推荐页:挑正在前台的那张交接,不转人工', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = 'd'.repeat(64)
  const background = {
    id: 80,
    url: 'https://rd6.zhaopin.com/app/recommend?jobNumber=old',
    status: 'complete',
    active: false,
    lastAccessed: 900,
    windowId: 1,
  }
  const foreground = {
    id: 81,
    url: 'https://rd6.zhaopin.com/app/recommend?jobNumber=work',
    status: 'complete',
    active: true,
    lastAccessed: 100,
    windowId: 1,
  }
  const updated = []
  try {
    globalThis.chrome = {
      tabs: {
        async query() {
          return [{ ...background }, { ...foreground }]
        },
        async create() {
          throw new Error('已有推荐页时不得新建标签')
        },
        async update(id, options) {
          assert.equal(id, foreground.id, '两张推荐页应挑正在前台的那张,不因歧义转人工')
          updated.push({ id, options })
          foreground.url = options.url
          return { ...foreground }
        },
        async get(id) {
          assert.equal(id, foreground.id)
          return { ...foreground }
        },
        async sendMessage() { return { ok: true } },
      },
      scripting: {
        async executeScript({ target }) {
          assert.equal(target.tabId, foreground.id)
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
      id: foreground.id,
      options: { url: 'https://rd6.zhaopin.com/app/im', active: true },
    }])
    assert.equal(background.url, 'https://rd6.zhaopin.com/app/recommend?jobNumber=old',
      '没被挑中的那张原样不动')
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

test('readThread 已点击但目标 route 未就绪时如实返回 possible、按暂态下轮重试并留最后路由观察', async () => {
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
        error.retryable === Retryable.AfterRecovery &&
        error.sideEffect === 'possible' &&
        error.message.includes('last='),
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

test('readList 年龄截止只在整窗过线时命中，置顶陈旧行不得截断遍历', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = 'f'.repeat(64)
  const now = Date.now()
  const pinnedStale = {
    conversationRef: 'pinned-stale',
    peer: { displayName: '候选人甲', platformUserRef: 'peer-pinned' },
    unreadCount: 0,
    lastMessage: { direction: 'out', kind: 'text', textPreview: '九天前' },
    lastActivityTs: now - 9 * 86_400_000,
  }
  const freshTop = {
    conversationRef: 'fresh-top',
    peer: { displayName: '候选人乙', platformUserRef: 'peer-fresh-top' },
    unreadCount: 0,
    lastMessage: { direction: 'in', kind: 'text', textPreview: '今天' },
    lastActivityTs: now,
  }
  const noTs = {
    conversationRef: 'no-activity-ts',
    peer: { displayName: '候选人丙', platformUserRef: 'peer-no-ts' },
    unreadCount: 0,
    lastMessage: { direction: 'in', kind: 'text', textPreview: '时间缺失' },
    lastActivityTs: null,
  }
  const staleA = {
    ...pinnedStale,
    conversationRef: 'stale-a',
    peer: { displayName: '候选人丁', platformUserRef: 'peer-stale-a' },
  }
  const staleB = {
    ...pinnedStale,
    conversationRef: 'stale-b',
    peer: { displayName: '候选人戊', platformUserRef: 'peer-stale-b' },
    lastActivityTs: now - 10 * 86_400_000,
  }
  const basePage = { atBottom: false, moved: true, scrollHeight: 4_000, scrollTop: 0, unstable: false }
  let domWindow = { ...basePage, sessions: [pinnedStale, freshTop, noTs] }
  globalThis.chrome = {
    tabs: {
      async query() { return [{ id: 9, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' }] },
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
          return [{ result: domWindow }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = {
    cmdMsgId: 'list-pinned', deadlineMs: now + 10_000, commandContext: undefined,
    signal: new AbortController().signal,
    async progress() {}, checkpoint() {}, beforeSideEffect() {},
  }
  try {
    const mixed = await readZhilianList(
      { filter: 'all', move: 'reset', stopOlderThanDays: 8 },
      context,
      fingerprint,
    )
    assert.equal(mixed.complete, false,
      '置顶陈旧行不能证明已扫到陈旧区，窗内仍有未过线行时禁止判 complete')
    assert.deepEqual(mixed.sessions.map((item) => item.conversationRef), ['fresh-top', 'no-activity-ts'],
      '过线行滤出返回集，时间缺失行不算过线、照常交付')

    domWindow = { ...basePage, sessions: [staleA, staleB], scrollTop: 700 }
    const terminal = await readZhilianList(
      { filter: 'all', move: 'next', stopOlderThanDays: 8 },
      context,
      fingerprint,
    )
    assert.equal(terminal.complete, true, '整窗全部行过线才命中年龄截止')
    assert.deepEqual(terminal.sessions, [])
  } finally {
    globalThis.chrome = originalChrome
  }
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

test('列表筛选确认超时带最后一轮读值 detail(错误收敛必须留痕,2026-08-26)', async () => {
  const original = {
    location: globalThis.location, document: globalThis.document,
    getComputedStyle: globalThis.getComputedStyle, setTimeout: globalThis.setTimeout,
  }
  const realDateNow = Date.now
  const label = { textContent: '全部职位', getClientRects() { return [{}] } }
  const input = {
    type: 'checkbox', disabled: false, checked: false,
    click() { /* 卡死形态:点击不翻转 */ },
  }
  const wrapper = {
    textContent: '未读',
    getClientRects() { return [{}] },
    querySelectorAll(selector) { return selector === 'input[type="checkbox"]' ? [input] : [] },
  }
  const trigger = { getClientRects() { return [{}] }, click() {} }
  try {
    globalThis.location = { href: 'https://rd6.zhaopin.com/app/im' }
    globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
    // 虚拟时钟:每次 setTimeout 立即推进等长虚拟时间,20 秒轮询瞬时走完。
    let clock = realDateNow()
    Date.now = () => clock
    globalThis.setTimeout = (callback, delay) => {
      clock += Math.max(1, delay ?? 0)
      queueMicrotask(callback)
      return 1
    }
    globalThis.document = {
      querySelectorAll(selector) {
        if (selector === '.app-job-selector') return [trigger]
        if (selector === '.app-job-selector .im-job-filter__label') return [label]
        if (selector === '.side-panel-header__checkbox') return [wrapper]
        return []
      },
    }
    const result = await zhilianTestHooks.mainEnsureChatListFilter(true, true)
    assert.deepEqual(result, {
      status: 'failed',
      reason: 'unread_selection_unconfirmed',
      detail: 'last=ready allJobs=true unread=false want=true',
    }, '超时收场必须带最后一轮各判据的实际读值')
  } finally {
    Date.now = realDateNow
    Object.assign(globalThis, original)
  }
})

test('readList 筛选失败降为 afterRecovery 且 detail 进入错误文案(2026-08-26 甲方裁决)', async () => {
  const originalChrome = globalThis.chrome
  const fingerprint = '6'.repeat(64)
  let filterCalls = 0
  globalThis.chrome = {
    tabs: {
      async query() { return [{ id: 9, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' }] },
      async get() { return { id: 9, url: 'https://rd6.zhaopin.com/app/im', status: 'complete' } },
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
          filterCalls += 1
          if (filterCalls === 1) return [{ result: { status: 'needs_action' } }]
          return [{ result: {
            status: 'failed', reason: 'unread_selection_unconfirmed',
            detail: 'last=ready allJobs=true unread=false want=true',
          } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  const context = {
    cmdMsgId: 'list-demote', deadlineMs: Date.now() + 10_000, commandContext: undefined,
    signal: new AbortController().signal,
    async progress() {}, checkpoint() {}, beforeSideEffect() {},
  }
  try {
    await assert.rejects(
      readZhilianList({ filter: 'unread', move: 'reset' }, context, fingerprint),
      (error) => error instanceof ZhilianPlatformError &&
        error.code === 'ELEMENT_UNRESOLVED' &&
        error.retryable === 'afterRecovery' &&
        error.message.includes('unread_selection_unconfirmed') &&
        error.message.includes('last=ready allJobs=true unread=false want=true'),
      '筛选确认失败必须是 afterRecovery(本轮失败下轮重试,不停账号)且留痕随行',
    )
    assert.equal(filterCalls, 2, '预检 needs_action 后才进入 apply 路径')
  } finally {
    globalThis.chrome = originalChrome
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
        if (func.name === 'mainProbeZhilianBlockedDialog') {
          return [{ result: { status: 'absent' } }]
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

test('chat.openConversation 筛选未就绪时零 click，未读不收敛按是否点击如实报 possible/none', async () => {
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
        if (func.name === 'mainProbeZhilianBlockedDialog') {
          return [{ result: { status: 'absent' } }]
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
        error.code === ErrorCode.PostconditionUnconfirmed &&
        error.sideEffect === 'none',
      '目标已是当前路由但未读未收敛：零点击直进后置核验，不得伪造成 ok，sideEffect 如实为 none',
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

test('chat.openConversation 目标态已达成时零点击直进后置核验并按幂等判成功', async () => {
  const originalChrome = globalThis.chrome
  const originalSetTimeout = globalThis.setTimeout
  const fingerprint = '6'.repeat(64)
  const conversationRef = 'conversation-already-open'
  let barriers = 0
  let findCalls = 0
  let windowReads = 0
  let currentURL = ''
  globalThis.setTimeout = (callback) => {
    queueMicrotask(callback)
    return 1
  }
  const makeContext = (label) => ({
    cmdMsgId: label, deadlineMs: Date.now() + 60_000,
    irreversibleNotAfterMs: Date.now() + 60_000,
    commandContext: undefined,
    signal: new AbortController().signal,
    async progress() {}, checkpoint() {}, async beforeSideEffect() { barriers += 1 },
  })
  globalThis.chrome = {
    tabs: {
      async query() { return [{ id: 111, url: currentURL, status: 'complete', active: true }] },
      async get() { return { id: 111, url: currentURL, status: 'complete', active: true } },
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
        if (func.name === 'mainReadListDOMWindow') {
          windowReads += 1
          const result = {
            sessions: [{
              conversationRef,
              peer: { displayName: '候选人丙', platformUserRef: 'peer-already-open' },
              unreadCount: 1,
              lastMessage: { direction: 'in', kind: 'text', textPreview: '未读消息' },
              lastActivityTs: Date.now(),
            }],
            atBottom: false, moved: true, scrollHeight: 1_000, scrollTop: 0, unstable: false,
          }
          // 第二次窗口读取发生在 ensureThreadRoute 内、点击前：模拟真人此刻
          // 抢先点开了目标会话。
          if (windowReads === 2) {
            currentURL = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`
          }
          return [{ result }]
        }
        if (func.name === 'mainClickConversationOnce') {
          throw new Error('目标态已达成时不得产生任何点击')
        }
        if (func.name === 'mainFindConversation') {
          findCalls += 1
          return [{ result: { status: 'failed', reason: 'target_not_found' } }]
        }
        throw new Error(`unexpected MAIN function ${func.name}`)
      },
    },
  }
  try {
    // 场景一：命令抵达时目标已是当前路由（真人正停在该会话）。
    currentURL = `https://rd6.zhaopin.com/app/im?sessionId=${conversationRef}`
    const already = await openZhilianConversation(
      { conversationRef }, makeContext('open-already-current'), fingerprint,
    )
    assert.equal(already.conversationRef, conversationRef)
    assert.ok(already.observedAt > 0)
    assert.equal(barriers, 0, '零点击路径不得消费取消安全点')
    assert.equal(windowReads, 0, '已是当前路由时不再要求 fresh 窗口定位')
    assert.ok(findCalls >= 2, '零点击路径仍必须连续双读正证')

    // 场景二：fresh 定位后、点击前真人抢先打开目标（ensureThreadRoute 返回
    // !changed），同样零点击、直进后置核验并判成功。
    findCalls = 0
    windowReads = 0
    currentURL = 'https://rd6.zhaopin.com/app/im?sessionId=previous-conversation'
    const helped = await openZhilianConversation(
      { conversationRef }, makeContext('open-human-helped'), fingerprint,
    )
    assert.equal(helped.conversationRef, conversationRef)
    assert.equal(barriers, 0, '点击前让位的路径同样不得消费取消安全点')
    assert.equal(windowReads, 2, '定位读照常发生，仅点击被跳过')
    assert.ok(findCalls >= 2)
  } finally {
    globalThis.chrome = originalChrome
    globalThis.setTimeout = originalSetTimeout
  }
})

test('readList MAIN 内部异常保留脱敏阶段且不退化为空结果', async () => {
  const original = {
    chrome: globalThis.chrome,
    document: globalThis.document,
    setTimeout: globalThis.setTimeout,
    dateNow: Date.now,
  }
  try {
    // 虚拟列表缺失时 resolve_surface 会按条件轮询等满上限。这条用例是纯阴性,
    // 用虚拟时钟推进,免得真等两轮 10 秒。
    let virtualNow = 1_780_000_000_000
    Date.now = () => virtualNow
    globalThis.setTimeout = (callback, delay = 0, ...args) => {
      virtualNow += Math.max(0, Number(delay) || 0)
      queueMicrotask(() => callback(...args))
      return 1
    }
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
    Date.now = original.dateNow
  }
})

test('readList MAIN 在虚拟列表重建空窗内按条件轮询等它回来', async () => {
  const original = {
    document: globalThis.document,
    setTimeout: globalThis.setTimeout,
    dateNow: Date.now,
  }
  try {
    let virtualNow = 1_780_000_000_000
    Date.now = () => virtualNow
    const delays = []
    globalThis.setTimeout = (callback, delay = 0, ...args) => {
      delays.push(delay)
      virtualNow += Math.max(0, Number(delay) || 0)
      queueMicrotask(() => callback(...args))
      return 1
    }
    // 切换会话列表筛选时智联会整体卸载重建虚拟列表,真机实测空窗 0.3~0.9 秒。
    // 外层 .im-session-list 全程都在,imListVisible 探针看不见这段空窗,所以
    // 这里必须自己等:前三次查不到,第四次回来就得接着往下读。
    let lookups = 0
    const virtual = {
      querySelectorAll: () => [],
      parentElement: null,
      scrollHeight: 0,
      clientHeight: 0,
    }
    globalThis.document = {
      querySelector(selector) {
        if (!String(selector).includes('__virtual')) return null
        lookups += 1
        return lookups < 4 ? null : virtual
      },
    }

    const sentinel = await zhilianTestHooks.mainReadListDOMWindow(false, false)
    assert.equal(lookups, 4, '虚拟列表未就绪时必须继续轮询,不能查一次就判缺失')
    assert.doesNotMatch(
      sentinel.__recruitHelperMainError,
      /dom_list_virtual_missing/u,
      '列表已经回来就不得再判缺失',
    )
    assert.match(sentinel.__recruitHelperMainError, /dom_list_scroll_surface_missing/u)
    assert.deepEqual(delays, [100, 100, 100], '轮询间隔固定 100ms,不做退避')
  } finally {
    Object.assign(globalThis, original)
    Date.now = original.dateNow
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

// 真机 2026-08-03：平台偶发给某一行 lastSentence 字符串 "null"（该会话摘要
// 未被填充，打开会话后才补上，纯等待 18 秒读 12 次一动不动）。此前整窗抛错，
// 一个坏行让 32 行全读不出来，还借 readThread 升成 manualOnly 隔离了两个无辜
// 候选人。现在跳过坏行、整窗照常返回，并把跳过的身份留在 skippedRefs 里。
test('readList MAIN 单行摘要坏数据只跳过该行，整窗照常返回且跳过留痕', async () => {
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
      return selector === '.im-session-item__box, .im-session-item' ? marker : null
    },
    querySelectorAll() { return [] },
  })
  const rows = [makeRow(), makeRow(), makeRow(), makeRow()]
  const sources = [
    {
      sessionId: 'session-ok-head', peerPartnerId: 'peer-ok-head', unreadCount: 0,
      name: '候选人甲', sortTime: 4_000,
      lastSentence: { senderType: 'USER', text: '你好', sendTime: 4_000 },
    },
    {
      // 真机原样形态：JSON 序列化的 null 落成字符串
      sessionId: 'session-null-sentence', peerPartnerId: 'peer-null-sentence', unreadCount: 0,
      name: '候选人乙', sortTime: 3_000, lastSentence: 'null',
    },
    {
      // 非法 JSON 同样只跳过该行，不炸整窗
      sessionId: 'session-broken-json', peerPartnerId: 'peer-broken-json', unreadCount: 0,
      name: '候选人丙', sortTime: 2_000, lastSentence: '{"senderType"',
    },
    {
      sessionId: 'session-ok-tail', peerPartnerId: 'peer-ok-tail', unreadCount: 2,
      name: '候选人丁', sortTime: 1_000,
      lastSentence: { senderType: 'STAFF', text: '稍后联系', sendTime: 1_000 },
    },
  ]
  const virtual = {
    scrollTop: 0, scrollHeight: 600, clientHeight: 300,
    parentElement: null, querySelectorAll() { return [] }, dispatchEvent() {},
  }
  try {
    globalThis.setTimeout = (callback) => { callback(); return 0 }
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
        $children: rows.map((row, index) => ({ $el: row, _props: { source: sources[index] }, $children: [] })),
      },
    }
    const result = await zhilianTestHooks.mainReadListDOMWindow(false, false)
    assert.equal(result.__recruitHelperMainError, undefined,
      '单行摘要坏数据不得让整窗读取失败')
    assert.deepEqual(
      result.sessions.map((item) => item.conversationRef),
      ['session-ok-head', 'session-ok-tail'],
      '坏行被跳过，其余行必须原样保留且保持页面顺序',
    )
    assert.deepEqual(
      result.skippedRefs,
      ['session-null-sentence', 'session-broken-json'],
      '跳过必须留痕：不得由手静默过滤',
    )
    assert.equal(result.unstable, false, '坏行占位参与稳定判定，不得误判为窗口抖动')
    const advanced = await zhilianTestHooks.mainReadListDOMWindow(true, false)
    assert.equal(advanced.__recruitHelperMainError, undefined)
    assert.equal(advanced.moved, true, '坏行不得影响 next 的推进判定')
  } finally {
    Object.assign(globalThis, original)
  }
})

test('智联未读角标：解析口径逐字未变(2026-08-03 事故判据)', () => {
  // 2026-08-03 真机订正：角标节点常驻聊天菜单项，未读清零只摘掉
  // `app-im-unread` 类并清空文本。旧断言"徽章缺失不能猜成零"把这个正式的
  // 零形态一起判成缺失，快照塌成 null，未读子轮清完未读后回读不到收尾数，
  // 基线永久写不进——插队因此长期失效。**干得越干净越判定为没跑完。**
  // 三态改由文本承担：节点缺失才是读不到，空文本是零，非空非数字向多算。
  //
  // 2026-08-26：被动传感链删除后，读取节点那一步随之消失（角标改由
  // chat.readUnreadTotal 现场读，见 zhilian.ts 的 mainReadZhilianUnreadBadge），
  // 但**解析口径一个字节没改**，仍在主动路径上服役。本用例因此从"读 DOM"
  // 改为直接断言解析函数；判据一条不减。
  assert.equal(ZHILIAN_UNREAD_BADGE_SELECTOR, '.app-menu-item__im-unread',
    '只认常驻单类；带 app-im-unread 的双类选择器在零未读时不命中')
  assert.equal(parseZhilianUnreadBadgeText(''), 0,
    '空文本是页面表达无未读的正式形态，必须读成零')
  assert.equal(parseZhilianUnreadBadgeText('   '), 0,
    '全空白同样是零，不得判成读不到')
  assert.equal(parseZhilianUnreadBadgeText(' 12 '), 12)
  assert.equal(parseZhilianUnreadBadgeText('99+'), 99,
    '截断展示取前导数字，绝不能落回零')
  assert.equal(parseZhilianUnreadBadgeText('消息 12'), 1,
    '节点在且非空即至少一条；仍禁止模糊扫描全页数字')
  // "节点整体缺失=读不到" 的语义现在由调用方承担：mainReadZhilianUnreadBadge
  // 返回 found=false 时，readZhilianUnreadTotalNow 直接给 total=null，不进解析。
})

test('content 传感器：精确双读、强制快照与采样窗不被高频渲染重启', async () => {
  // 2026-08-26：本用例原以未读角标承载这三条机制,角标被动传感已随裁决删除
  // (现改由 chat.readUnreadTotal 现场读)。三条机制在登录态采样上逐字相同,
  // 故改用登录态承载——覆盖一条不减。
  const config = { badgeDebounceMs: 800, navSettleMs: 500, manualQuietMs: 45_000 }
  const harness = contentSensorHarness()
  const sensor = new ContentSensor(harness.env)
  sensor.start()
  assert.equal(harness.messages[0].type, CONTENT_MESSAGE.Ready)
  assert.equal(harness.messages[0].pageKind, undefined,
    'Ready 不再携带 pageKind——SW 侧一律自行按 URL 重算')
  assert.equal(harness.timers.size, 0, 'welcome 参数到达前不得自带传感节奏')

  sensor.configure(config)
  assert.deepEqual([...harness.timers.values()].map((timer) => timer.delayMs).sort((a, b) => a - b), [500, 800])
  harness.runTimers()
  const loginMessages = () => harness.messages.filter((message) => message.type === CONTENT_MESSAGE.LoginStable)
  assert.equal(loginMessages().length, 1, '首个稳定登录态必须上报')
  assert.equal(loginMessages().at(-1).state, LoginState.In)

  // 双读不一致不产生事实。
  const beforeMismatch = loginMessages().length
  harness.state.loginReads = [LoginState.Out, LoginState.In]
  sensor.onDOMMutation()
  harness.runTimers()
  assert.equal(loginMessages().length, beforeMismatch, '两次读数不一致不得上报')

  // 值未变化时不重复上报。
  harness.state.now = 1_000
  sensor.onDOMMutation()
  harness.runTimers()
  assert.equal(loginMessages().length, beforeMismatch, '稳定值未变化不得重复上报')

  // 强制快照:新 SW 索要时,同一个稳定值也必须重新交付一次。
  sensor.configure(config, true)
  harness.runTimers()
  assert.equal(loginMessages().length, beforeMismatch + 1,
    '新 SW 请求快照时，同一个稳定值也必须重新交付')
  assert.deepEqual(loginMessages().at(-1), {
    type: CONTENT_MESSAGE.LoginStable,
    observedAt: 1_000,
    state: LoginState.In,
  })

  // 注:"采样窗已在路上时高频 mutation 不得重启双读首样本"这条性质是
  // armBadge 特有的(它见到在途计时器就早退),armLogin 每次 mutation 都清掉
  // 重建。角标采样器随被动未读传感删除,该性质一并消失,不搬到登录态上——
  // 硬搬会断言一个从来不成立的行为。
  harness.state.loginReads = [LoginState.Out, LoginState.Out]
  sensor.configure(config)
  harness.runTimers()
  assert.equal(loginMessages().at(-1).state, LoginState.Out, '登录态变化必须如实上报')

  // 真人 DOM 输入不再上报(2026-08-11 甲方裁决):ContentSensor 不再有
  // onTrustedPointer/onTrustedKeyboard/onTrustedNavigationIntent,
  // manualInteraction 只剩 SW 侧的推荐页 reload 换代信号。
  assert.equal(typeof sensor.onTrustedPointer, 'undefined')
  assert.equal(typeof sensor.onTrustedKeyboard, 'undefined')
  assert.equal(typeof sensor.onTrustedNavigationIntent, 'undefined')
})

test('SW 页面桥：无 CmdContext 不造 accountRef，canonical 静音且页面导航只报 pageNavigated', async () => {
  let now = 0
  const connection = new FakeSensorConnection()
  const bridge = new SensorBridge(connection, () => now)
  const tab1 = { tabId: 1, active: true, url: 'https://rd6.zhaopin.com/app/im', windowId: 1 }
  const tab2 = { tabId: 2, active: true, url: 'https://rd6.zhaopin.com/app/recommend', windowId: 1 }

  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.Ready, at: 0, url: tab1.url }, tab1)
  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.LoginStable, observedAt: 0, state: LoginState.In }, tab1)
  assert.equal(connection.events.length, 0, '未学到脑侧 accountRef 时必须静默')

  connection.setContext({ platform: 'zhilian', accountRef: 'account-1', expectedPrincipalFingerprint: 'fp' })
  bridge.refreshCachedState()
  assert.deepEqual(connection.contextHealth, [{ platform: 'zhilian', accountRef: 'account-1', ready: true }])

  now = 6_000
  // 登录态变化事件带上脑侧 accountRef——它是掉登录即时停机通道的入口。
  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.LoginStable, observedAt: now, state: LoginState.Out }, tab1)
  assert.equal(connection.events.at(-1).name, EventName.LoginStateChanged)
  assert.equal(connection.events.at(-1).accountRef, 'account-1')
  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.LoginStable, observedAt: now, state: LoginState.In }, tab1)

  // 非 canonical 标签页的传感一律静音。
  const eventsBeforeNonCanonical = connection.events.length
  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.Ready, at: now, url: tab2.url }, tab2)
  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.LoginStable, observedAt: now, state: LoginState.Out }, tab2)
  assert.equal(connection.events.length, eventsBeforeNonCanonical, '非 canonical 传感不得发事件')

  now = 18_000
  const commandURL = 'https://rd6.zhaopin.com/app/im?sessionId=command'
  bridge.noteChromeNavigation(1, commandURL)
  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.PageNavigated, at: now + 500, pageKind: PageKind.Im, url: commandURL }, {
    ...tab1,
    url: commandURL,
  })
  assert.equal(connection.events.at(-1).name, EventName.PageNavigated)

  now = 24_000
  const manualURL = 'https://rd6.zhaopin.com/app/im?sessionId=manual'
  bridge.noteChromeNavigation(1, manualURL)
  bridge.acceptContentMessage({ type: CONTENT_MESSAGE.PageNavigated, at: now + 500, pageKind: PageKind.Im, url: manualURL }, {
    ...tab1,
    url: manualURL,
  })
  assert.equal(connection.events.at(-1).name, EventName.PageNavigated,
    '真人导航与命令导航一样，只报 pageNavigated')
  assert.equal(connection.events.filter((event) => event.name === EventName.ManualInteraction).length, 0,
    '真人 DOM 输入不再上报：页面桥只在推荐页 reload 换代时产生 manualInteraction')

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

// 真机 2026-08-03：SW 未重启、只有一个平台标签页，SW 侧传感缓存仍然空着，而
// 页面侧"值没变就不报"于是永不补发。未读靠一条新消息才解锁，登录态几乎从不
// 变化，pageHealth 一直停在 degraded。增量上报的接收方必须自带重新同步路径。
test('SensorBridge 心跳自查：传感缓存缺失时索要重新同步，齐全后不再打扰', async () => {
  const runtimeMessage = chromeEvent()
  const sent = []
  globalThis.chrome = {
    runtime: { onMessage: runtimeMessage },
    tabs: {
      onActivated: chromeEvent(), onUpdated: chromeEvent(), onRemoved: chromeEvent(),
      async query() { return [] },
      async sendMessage(tabId, message) { sent.push({ tabId, message }); return { ok: true } },
    },
    windows: { WINDOW_ID_NONE: -1, onFocusChanged: chromeEvent() },
    webNavigation: {
      onCommitted: chromeEvent(), onHistoryStateUpdated: chromeEvent(),
      onReferenceFragmentUpdated: chromeEvent(),
    },
  }
  const connection = new FakeSensorConnection()
  const bridge = new SensorBridge(connection)
  bridge.start()
  const tab = { tabId: 7, active: true, url: 'https://rd6.zhaopin.com/app/im', windowId: 1 }

  // 还没有任何 content 状态：心跳不得凭空向不存在的页面索要
  connection.tickHeartbeat()
  await eventually(() => true, '')
  assert.equal(sent.length, 0, '没有 canonical 页面时心跳不得发起重新同步')

  // Ready 建起缓存：unread 为 null、登录态为 Unknown，正是失联形态
  bridge.acceptContentMessage(
    { type: CONTENT_MESSAGE.Ready, at: 0, pageKind: PageKind.Im, url: tab.url }, tab,
  )
  await eventually(() => sent.length > 0, 'Ready 握手未下发配置')
  const 握手后 = sent.length

  connection.tickHeartbeat()
  await eventually(() => sent.length > 握手后, '传感缓存缺失时心跳未索要重新同步')
  assert.equal(sent.at(-1).message.requestSnapshot, true,
    '重新同步必须带 requestSnapshot，否则页面侧仍会因值未变而沉默')

  // 补齐登录态。2026-08-26 未读那一半判据随被动传感删除,登录态这一半必须留:
  // 它是 lastCanonicalLogin 拿到第一个非 unknown 采样的唯一路径。
  bridge.acceptContentMessage(
    { type: CONTENT_MESSAGE.LoginStable, observedAt: 1, state: LoginState.In }, tab,
  )
  const 齐全后 = sent.length
  connection.tickHeartbeat()
  connection.tickHeartbeat()
  await eventually(() => true, '')
  assert.equal(sent.length, 齐全后, '传感齐全后心跳必须零打扰，不得每拍骚扰页面')
})

test('SensorBridge 只把推荐页 committed reload 上报为换代', async () => {
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
    const bridge = new SensorBridge(connection, () => now)
    bridge.start()
    const listener = tabUpdated.listeners[0]
    const recommendURL = 'https://rd6.zhaopin.com/app/recommend'

    committed.listeners[0]({
      tabId: 7, frameId: 0, url: recommendURL, timeStamp: now, transitionType: 'reload',
    })
    assert.equal(connection.events.length, 0, '无 CmdContext 时推荐页 reload 不得上报')

    connection.setContext({ platform: 'zhilian', accountRef: 'account-1', expectedPrincipalFingerprint: 'fp' })

    now += 1_000
    listener(7, { status: 'loading' }, { url: recommendURL })
    assert.equal(connection.events.length, 0, '简历详情也会令 tab loading，不能据此判定推荐流换代')
    committed.listeners[0]({
      tabId: 7, frameId: 0, url: recommendURL, timeStamp: now, transitionType: 'reload',
    })
    assert.equal(connection.events.length, 1, '公开确认的整页 reload 必须上报换代')
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

    now += 1_000
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
    assert.equal(connection.events.length, 1, '非 reload、非推荐页或非主框架均不得上报换代')
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
  // 按平台声明能力(2026-09-02 甲方裁决):platforms 无条件发,每张表 ⊆ caps 并集。
  assert.deepEqual(hello.body.platforms.map((p) => p.id), registeredPlatforms().map((a) => a.id),
    'hello.platforms 必须逐一对应已注册平台')
  for (const platform of hello.body.platforms) {
    for (const capability of platform.caps) {
      assert.ok(hello.body.caps.includes(capability), `${platform.id} 表里的 ${capability} 不在并集里`)
    }
  }

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
      navSettleMs: 1,
      manualQuietMs: 45_000,
    },
  }
  socket.receive(envelope(Kind.Welcome, 'welcome-1', null, welcomeBody))
  await eventually(() => connection.status().phase === 'session', 'welcome 未建立 session')
  assert.equal(connection.sensorConfig().badgeDebounceMs, 1)
  assert.equal(connection.status().heartbeatIntervalMs, 12_345, 'session 心跳必须服从 welcome.hb')

  const beforeEvent = socket.sent.length
  assert.equal(connection.emitSensorEvent(EventName.PageNavigated, {
    platform: 'zhilian', accountRef: 'acc',
  }, { at: 1 }), 'sent')
  assert.equal(socket.sent.length, beforeEvent + 1)
  const event = JSON.parse(socket.sent.at(-1))
  assert.equal(event.kind, Kind.Event)
  assert.equal(event.body.name, EventName.PageNavigated)

  // handLog 不带 context:契约允许省略,但信封里不能留一个值为 undefined 的 context 键——
  // 校验器按「键在不在」判缺失,键在、值 undefined 会被当成坏 context 校验失败、整帧静默
  // dropped。2026-09-03 实证:两个 BOSS 账本的 processed_msgs 零条 event,手侧日志从没到过脑。
  const beforeHandLog = socket.sent.length
  assert.equal(connection.emitHandLog({ level: 'warn', code: 'probe', message: 'x', at: Date.now() }), 'sent',
    '不带 context 的 handLog 必须发得出去')
  assert.equal(socket.sent.length, beforeHandLog + 1)
  const handLogFrame = JSON.parse(socket.sent.at(-1))
  assert.equal(handLogFrame.body.name, EventName.HandLog)
  assert.equal('context' in handLogFrame.body, false, '省略的 context 不能以 undefined 键的形式出现在信封里')

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

// 平台阻塞弹窗。最危险的一条路是 body 里堆着的隐藏残留:真机实测同一页面 7 个,
// 每个都还带着 disabled=false 的「删除对话」。全局 querySelector 会命中最老那个,
// 点下去删的是别人的会话——下面的用例把这条路钉死。
function installBlockedDialogFixture(specs) {
  const original = {
    document: globalThis.document,
    getComputedStyle: globalThis.getComputedStyle,
    window: globalThis.window,
  }
  const wrappers = specs.map((spec) => {
    const buttons = (spec.buttons ?? ['知道了', '删除对话']).map((text) => {
      const button = { innerText: text, disabled: spec.disabled === text, clicks: 0 }
      button.click = () => { button.clicks += 1 }
      return button
    })
    // 真机事实:关闭后的残留只是 display:none，modal 上的 km-modal--open 被摘掉。
    const modal = { classList: { contains: (name) => spec.open === true && name === 'km-modal--open' } }
    const title = spec.title === undefined ? null : { innerText: spec.title }
    return {
      buttons,
      hidden: spec.hidden === true,
      getBoundingClientRect: () => ({ height: spec.hidden === true ? 0 : 200 }),
      querySelector(selector) {
        if (selector === '.km-modal') return modal
        if (selector === '.km-modal__title-inner') return title
        return null
      },
      querySelectorAll(selector) {
        return selector === '.km-modal__footer button' ? buttons : []
      },
    }
  })
  const computeStyle = (node) => ({
    display: node.hidden ? 'none' : 'flex',
    visibility: 'visible',
  })
  globalThis.getComputedStyle = computeStyle
  // 生产代码按本文件惯例走 window.getComputedStyle，Node 里没有 window。
  globalThis.window = { getComputedStyle: computeStyle }
  globalThis.document = {
    querySelectorAll(selector) {
      return selector === 'body > .km-modal__wrapper' ? wrappers : []
    },
  }
  return {
    wrappers,
    live: wrappers[wrappers.length - 1],
    restore() {
      globalThis.document = original.document
      globalThis.getComputedStyle = original.getComputedStyle
      globalThis.window = original.window
    },
  }
}

const BLOCKED_DIALOG_STALE = Object.freeze({
  hidden: true, open: false, title: '求职者存在违规行为，系统已为您自动屏蔽',
})

test('平台阻塞弹窗只认当前可见的 open 弹层，7 个隐藏残留一个都不碰', () => {
  const fixture = installBlockedDialogFixture([
    ...Array.from({ length: 7 }, () => BLOCKED_DIALOG_STALE),
    { open: true, title: '\n      求职者存在违规行为，系统已为您自动屏蔽\n    ' },
  ])
  try {
    const probe = zhilianTestHooks.mainProbeZhilianBlockedDialog()
    assert.equal(probe.status, 'present')
    assert.equal(probe.title, '求职者存在违规行为，系统已为您自动屏蔽', '标题需去掉平台的换行缩进')
    assert.equal(probe.known, true)
    assert.equal(probe.dismissible, true)

    const step = zhilianTestHooks.mainClickZhilianBlockedDialogButton('删除对话')
    assert.deepEqual(step, { status: 'ok', detail: '删除对话' })
    assert.equal(fixture.live.buttons.find((b) => b.innerText === '删除对话').clicks, 1)
    assert.equal(fixture.live.buttons.find((b) => b.innerText === '知道了').clicks, 0)
    for (const stale of fixture.wrappers.slice(0, 7)) {
      for (const button of stale.buttons) {
        assert.equal(button.clicks, 0, '隐藏残留弹层的按钮一次都不得被点击')
      }
    }
  } finally {
    fixture.restore()
  }
})

test('平台阻塞弹窗两种文案同构，只有白名单命中才算已知', () => {
  const cases = [
    ['求职者存在违规行为，系统已为您自动屏蔽', true],
    ['对方已关闭求职，无法继续进行沟通', true],
    ['确定要清空聊天记录吗', false],
  ]
  for (const [title, known] of cases) {
    const fixture = installBlockedDialogFixture([{ open: true, title }])
    try {
      const probe = zhilianTestHooks.mainProbeZhilianBlockedDialog()
      assert.equal(probe.status, 'present')
      assert.equal(probe.known, known, `文案「${title}」的白名单判定`)
    } finally {
      fixture.restore()
    }
  }
})

test('平台阻塞弹窗:只有残留、非本类弹层或多个候选时都不动手', () => {
  const onlyStale = installBlockedDialogFixture([BLOCKED_DIALOG_STALE, BLOCKED_DIALOG_STALE])
  try {
    assert.deepEqual(zhilianTestHooks.mainProbeZhilianBlockedDialog(), { status: 'absent' })
    assert.deepEqual(zhilianTestHooks.mainClickZhilianBlockedDialogButton('删除对话'),
      { status: 'failed', reason: 'dialog_absent' })
  } finally {
    onlyStale.restore()
  }

  // 可见 open 弹层，但不带「删除对话」:巡检期间撞上的任何别的确认框都走这里。
  const otherModal = installBlockedDialogFixture([
    { open: true, title: '确认发送', buttons: ['取消', '确定'] },
  ])
  try {
    assert.deepEqual(zhilianTestHooks.mainProbeZhilianBlockedDialog(), { status: 'absent' })
    assert.equal(otherModal.live.buttons.every((b) => b.clicks === 0), true)
  } finally {
    otherModal.restore()
  }

  const twoLive = installBlockedDialogFixture([
    { open: true, title: '求职者存在违规行为，系统已为您自动屏蔽' },
    { open: true, title: '对方已关闭求职，无法继续进行沟通' },
  ])
  try {
    assert.deepEqual(zhilianTestHooks.mainProbeZhilianBlockedDialog(), { status: 'ambiguous', count: 2 })
    assert.deepEqual(zhilianTestHooks.mainClickZhilianBlockedDialogButton('删除对话'),
      { status: 'failed', reason: 'dialog_ambiguous' })
    for (const wrapper of twoLive.wrappers) {
      assert.equal(wrapper.buttons.every((b) => b.clicks === 0), true, '歧义时一个都不许点')
    }
  } finally {
    twoLive.restore()
  }
})

test('平台阻塞弹窗:按钮缺失或禁用时诚实失败，不退而求其次点别的', () => {
  const noDismiss = installBlockedDialogFixture([
    { open: true, title: '求职者存在违规行为，系统已为您自动屏蔽', buttons: ['删除对话'] },
  ])
  try {
    assert.equal(zhilianTestHooks.mainProbeZhilianBlockedDialog().dismissible, false)
    assert.deepEqual(zhilianTestHooks.mainClickZhilianBlockedDialogButton('知道了'),
      { status: 'failed', reason: 'button_absent' })
    assert.equal(noDismiss.live.buttons[0].clicks, 0, '找不到目标按钮时不得改点「删除对话」')
  } finally {
    noDismiss.restore()
  }

  const disabled = installBlockedDialogFixture([
    { open: true, title: '对方已关闭求职，无法继续进行沟通', disabled: '删除对话' },
  ])
  try {
    assert.equal(zhilianTestHooks.mainProbeZhilianBlockedDialog().known, true)
    assert.deepEqual(zhilianTestHooks.mainClickZhilianBlockedDialogButton('删除对话'),
      { status: 'failed', reason: 'button_disabled' })
    assert.equal(disabled.live.buttons.find((b) => b.innerText === '删除对话').clicks, 0)
  } finally {
    disabled.restore()
  }
})

// chat.readPeerPhone MAIN:四形态真机踩点回归(2026-08-06 三形态 + 08-07 遮挡)。
// 真实号=复制按钮带 data-clipboard-text;无号=电话容器整段空缺;虚拟号=有显示
// 文本但无复制按钮;遮挡=「查看电话」按钮在场(masked=true,完整号不在 DOM)。
// 除真实号外都必须落 phone=null,显示文本永远不作数据源。
function installPeerPhonePanelFixture(variant) {
  const original = { document: globalThis.document }
  const copyNode = {
    getAttribute: (name) => (name === 'data-clipboard-text' ? '13801995730' : null),
  }
  const nameNode = { textContent: ' 洪建辉 ' }
  const clicks = []
  const revealButton = { disabled: false, click: () => clicks.push('reveal') }
  const panelRoot = {
    querySelector(selector) {
      if (selector === '.new-resume-basic__name-wrapper') return nameNode
      if (selector === '.new-resume-basic__contact--phone--box .im-resume-basic__phone--copy') {
        return variant === 'real' ? copyNode : null
      }
      if (selector === '.new-resume-basic__contact--phone--box .resume-button.get-phone') {
        return variant === 'masked' ? {} : null
      }
      return null
    },
  }
  globalThis.document = {
    querySelector(selector) {
      if (variant === 'noPanel') return null
      if (selector === '.new-resume-basic') return panelRoot
      if (
        selector ===
        '.new-resume-basic .new-resume-basic__contact--phone--box .resume-button.get-phone button'
      ) {
        return variant === 'masked' ? revealButton : null
      }
      return null
    },
  }
  return {
    clicks,
    restore() {
      globalThis.document = original.document
    },
  }
}

test('chat.readPeerPhone MAIN 只认复制按钮 data 属性,遮挡形态只报 masked', () => {
  for (const [variant, want] of [
    ['real', { phone: '13801995730', panelName: '洪建辉', masked: false }],
    // 无号(容器空)与虚拟号(有显示文本无复制按钮)在选择器层同构:都查不到复制按钮。
    ['absentOrVirtual', { phone: null, panelName: '洪建辉', masked: false }],
    ['masked', { phone: null, panelName: '洪建辉', masked: true }],
    ['noPanel', { phone: null, panelName: null, masked: false }],
  ]) {
    const fixture = installPeerPhonePanelFixture(variant)
    try {
      assert.deepEqual(zhilianTestHooks.mainReadPeerPhone(), want)
    } finally {
      fixture.restore()
    }
  }
})

test('chat.revealPeerPhone MAIN 点击步只在查看按钮在场时点一次', () => {
  const masked = installPeerPhonePanelFixture('masked')
  try {
    assert.deepEqual(zhilianTestHooks.mainClickRevealPeerPhone(), { clicked: true })
    assert.equal(masked.clicks.length, 1)
  } finally {
    masked.restore()
  }
  // 真实号形态没有查看按钮:绝不点击,由扩展侧改走直接读回。
  const real = installPeerPhonePanelFixture('real')
  try {
    assert.deepEqual(zhilianTestHooks.mainClickRevealPeerPhone(), { clicked: false })
    assert.equal(real.clicks.length, 0)
  } finally {
    real.restore()
  }
})

// 邀面成功弹窗清理。2026-08-07 甲方现场:连发两次后页面上叠着两个没关掉的弹窗,
// 「关注服务号 / 转给同事」——这两个按钮一个都不许碰,点错了就是我方擅自外发。
// 下面的 fixture 只造出生产代码真正用到的那点 DOM 能力,不引 jsdom(与本文件其余
// fixture 同一路子)。
function installInterviewSuccessModalFixture(specs) {
  const original = {
    document: globalThis.document,
    getComputedStyle: globalThis.getComputedStyle,
    window: globalThis.window,
    HTMLElement: globalThis.HTMLElement,
    KeyboardEvent: globalThis.KeyboardEvent,
    NodeFilter: globalThis.NodeFilter,
    setTimeout: globalThis.setTimeout,
    dateNow: Date.now,
  }
  // 虚拟时钟:生产代码的节奏等待是真的 1 秒起步、等弹窗出现是真的 10 秒轮询。
  // 每次 setTimeout 把时钟推进它请求的那么多毫秒,再立刻执行 —— 生产逻辑一行不改,
  // 测试不空转。
  let clock = original.dateNow()
  Date.now = () => clock
  const el = (tag, attrs, children = []) => {
    const node = {
      tagName: tag.toUpperCase(),
      attrs,
      childNodes: [],
      parentElement: null,
      isConnected: true,
      clicks: 0,
      style: attrs.style ?? {},
      getAttribute(name) { return this.attrs[name] ?? null },
      get textContent() {
        return this.childNodes
          .map((child) => (child.nodeType === 3 ? child.nodeValue : child.textContent))
          .join('')
      },
      descendants() {
        const out = []
        for (const child of this.childNodes) {
          if (child.nodeType === 3) continue
          out.push(child, ...child.descendants())
        }
        return out
      },
      querySelectorAll(selector) {
        const attr = (node2, name) => node2.getAttribute(name) ?? ''
        const match = {
          '.km-modal__close-btn': (n) => attr(n, 'class').split(/\s+/).includes('km-modal__close-btn'),
          '[class*="close" i]': (n) => /close/i.test(attr(n, 'class')),
          '[aria-label*="关闭"]': (n) => attr(n, 'aria-label').includes('关闭'),
          '[aria-label*="close" i]': (n) => /close/i.test(attr(n, 'aria-label')),
          '[title*="关闭"]': (n) => attr(n, 'title').includes('关闭'),
        }[selector]
        if (match === undefined) throw new Error(`fixture 未覆盖选择器 ${selector}`)
        return this.descendants().filter(match)
      },
    }
    for (const child of children) {
      if (typeof child === 'string') node.childNodes.push({ nodeType: 3, nodeValue: child, parentElement: node })
      else { child.parentElement = node; node.childNodes.push(child) }
    }
    return node
  }
  const wrappers = specs.map((spec) => {
    const card = el('DIV', { class: 'interview-service-account-modal' }, [
      ...(spec.closeAttrs === undefined ? [] : [el('IMG', spec.closeAttrs)]),
      el('DIV', { class: 'title' }, ['面试邀请已发出']),
      el('DIV', { class: 'sub' }, ['关注服务号接收实时通知']),
      el('DIV', { class: 'btns' }, [
        el('BUTTON', { class: 'follow-btn' }, ['关注服务号']),
        el('BUTTON', { class: 'forward-btn' }, ['转给同事']),
      ]),
    ])
    return el('DIV', {
      class: 'km-modal__wrapper',
      style: { position: 'fixed', zIndex: '2000' },
    }, [card])
  })
  const body = el('BODY', {}, wrappers)
  body.parentElement = null
  const computeStyle = (node) => ({
    display: node.removed === true ? 'none' : 'block',
    visibility: 'visible',
    position: node.style?.position ?? 'static',
    zIndex: node.style?.zIndex ?? 'auto',
  })
  for (const node of [body, ...body.descendants()]) {
    node.getClientRects = () => (node.removed === true ? [] : [{ width: 10, height: 10 }])
  }
  globalThis.getComputedStyle = computeStyle
  globalThis.window = { getComputedStyle: computeStyle }
  globalThis.NodeFilter = { SHOW_TEXT: 4 }
  globalThis.HTMLElement = { prototype: { click() { this.clicks += 1; this.onClick?.() } } }
  globalThis.KeyboardEvent = class { constructor(type, init) { Object.assign(this, init, { type }) } }
  globalThis.setTimeout = (fn, delay) => {
    clock += typeof delay === 'number' ? delay : 0
    return original.setTimeout(fn, 0)
  }
  const escapes = []
  globalThis.document = {
    body,
    dispatchEvent(event) { escapes.push(event.key); return true },
    createTreeWalker(root) {
      const texts = []
      const walk = (node) => {
        for (const child of node.childNodes) {
          if (child.nodeType === 3) texts.push(child)
          else walk(child)
        }
      }
      walk(root)
      let index = -1
      return {
        get currentNode() { return texts[index] },
        nextNode() { index += 1; return index < texts.length ? texts[index] : null },
      }
    },
  }
  return {
    wrappers,
    escapes,
    buttons: wrappers.flatMap((wrapper) => wrapper.descendants().filter((n) => n.tagName === 'BUTTON')),
    closeIcons: wrappers.flatMap((wrapper) => wrapper.descendants().filter((n) => n.tagName === 'IMG')),
    restore() {
      globalThis.document = original.document
      globalThis.getComputedStyle = original.getComputedStyle
      globalThis.window = original.window
      globalThis.HTMLElement = original.HTMLElement
      globalThis.KeyboardEvent = original.KeyboardEvent
      globalThis.NodeFilter = original.NodeFilter
      globalThis.setTimeout = original.setTimeout
      Date.now = original.dateNow
    },
  }
}

test('邀面成功弹窗:两个堆叠的都关掉,「关注服务号」「转给同事」一次都不碰', async () => {
  // 关闭钮按旧项目考古的类名造:img.interview-service-account-modal__top-close-icon。
  const closeAttrs = { class: 'interview-service-account-modal__top-close-icon' }
  const fixture = installInterviewSuccessModalFixture([{ closeAttrs }, { closeAttrs }])
  try {
    for (const icon of fixture.closeIcons) {
      // 平台真实行为:点关闭后弹窗整体消失。
      icon.onClick = () => { icon.parentElement.parentElement.removed = true }
    }
    const outcome = await zhilianTestHooks.mainCloseInterviewSuccessModal()
    assert.equal(outcome.found, true)
    assert.equal(outcome.closed, true, '两个弹窗都该被关掉')
    assert.equal(outcome.remaining, 0)
    assert.equal(fixture.closeIcons.every((icon) => icon.clicks === 1), true, '每个弹窗的关闭钮各点一次')
    assert.equal(fixture.buttons.every((button) => button.clicks === 0), true,
      '关注服务号/转给同事一个都不许点')
    assert.deepEqual(fixture.escapes, [], '关闭钮生效时不必再补 Escape')
  } finally {
    fixture.restore()
  }
})

test('邀面成功弹窗:没有关闭钮时补 Escape,仍然一个业务按钮都不点', async () => {
  const fixture = installInterviewSuccessModalFixture([{}])
  try {
    const outcome = await zhilianTestHooks.mainCloseInterviewSuccessModal()
    assert.equal(outcome.found, true)
    assert.equal(outcome.closed, false, '关不掉就得如实说关不掉')
    assert.equal(outcome.remaining, 1)
    assert.deepEqual(fixture.escapes, ['Escape', 'Escape'], 'keydown + keyup 各一次')
    assert.equal(fixture.buttons.every((button) => button.clicks === 0), true)
    // scene 供 handLog 带回结构:只有标签名与类名,没有任何页面文本。
    assert.match(outcome.scene, /^DIV\.km-modal__wrapper>noCloseBtn$/)
    assert.equal(/面试邀请|关注|同事/u.test(outcome.scene), false, 'scene 不得带页面文本')
  } finally {
    fixture.restore()
  }
})

test('邀面成功弹窗:页面上没有弹窗时不空等、不误报', async () => {
  const fixture = installInterviewSuccessModalFixture([])
  try {
    const outcome = await zhilianTestHooks.mainCloseInterviewSuccessModal()
    assert.deepEqual(outcome, { found: false, closed: false, remaining: 0, scene: '' })
    assert.deepEqual(fixture.escapes, [])
  } finally {
    fixture.restore()
  }
})

// ---- 埋点上报拦截规则的在线自检 ----
// 拦的是智联 environment-check 脚本本身,不是它的上报端点:拦端点时请求照常发起并
// 失败,而平台会把这次失败连同 staffId 与路径报进自己的异常通道,比不拦更糟。
// 判据单向且不依赖旁证 —— 规则若生效该脚本永不执行,它留在页面上的 window 标记就
// 永不存在;标记存在即规则失效。
function installNetGuardFixture({
  hasFeedback = true,
  enabledRulesets = ['zhilian_env_report'],
  rulesetsThrow = false,
  tabs = [{ id: 7 }],
  markerPresent = false,
  probeThrows = false,
} = {}) {
  const originalChrome = globalThis.chrome
  const matchListeners = []
  const executed = []
  const logs = []
  resetNetGuardForTest()
  installHandLogSink((data) => logs.push(data))
  const dnr = {
    async getEnabledRulesets() {
      if (rulesetsThrow) throw new Error('api unavailable')
      return enabledRulesets
    },
  }
  if (hasFeedback) {
    dnr.onRuleMatchedDebug = { addListener(cb) { matchListeners.push(cb) } }
  }
  globalThis.chrome = {
    declarativeNetRequest: dnr,
    tabs: { async query() { return tabs } },
    scripting: {
      async executeScript(args) {
        executed.push(args)
        if (probeThrows) throw new Error('injection refused')
        return [{ result: markerPresent }]
      },
    },
  }
  const pump = async () => {
    for (let i = 0; i < 20; i += 1) noteCommandDispatched()
    await sleep(10)
  }
  return {
    matchListeners,
    executed,
    logs,
    pump,
    staleLog: () => logs.find((d) => d.code === HandLogCode.EnvReportGuardStale),
    offLog: () => logs.find((d) => d.code === HandLogCode.EnvReportGuardOff),
    blindLog: () => logs.find((d) => d.code === HandLogCode.EnvReportGuardBlind),
    restore() {
      globalThis.chrome = originalChrome
      installHandLogSink(null)
      resetNetGuardForTest()
    },
  }
}

test('埋点上报自检:命中数不可观测时说明原因,但自检照常工作', async () => {
  const fixture = installNetGuardFixture({ hasFeedback: false, markerPresent: true })
  try {
    registerNetGuard()
    const blind = fixture.blindLog()
    assert.ok(blind, '应说明命中数失去观测')
    assert.equal(blind.level, 'warn')
    await fixture.pump()
    // 关键:判据是"脚本有没有执行",与能否观测命中无关,所以照样查得出失效
    assert.equal(fixture.executed.length, 1)
    assert.ok(fixture.staleLog(), '观测不到命中不等于自检失效')
  } finally {
    fixture.restore()
  }
})

test('埋点上报自检:规则集已启用时不报警', async () => {
  const fixture = installNetGuardFixture()
  try {
    registerNetGuard()
    await sleep(10)
    assert.equal(fixture.offLog(), undefined)
  } finally {
    fixture.restore()
  }
})

test('埋点上报自检:规则集没被启用时当场报出,不等探测', async () => {
  const fixture = installNetGuardFixture({ enabledRulesets: [] })
  try {
    registerNetGuard()
    await sleep(10)
    const off = fixture.offLog()
    assert.ok(off, '规则集未启用必须当场报 envReportGuardOff')
    assert.equal(off.level, 'error')
    assert.match(off.message, /未启用/)
  } finally {
    fixture.restore()
  }
})

test('埋点上报自检:查不到规则集状态时只降级报 warn,不当作未启用', async () => {
  const fixture = installNetGuardFixture({ rulesetsThrow: true })
  try {
    registerNetGuard()
    await sleep(10)
    const off = fixture.offLog()
    assert.ok(off)
    assert.equal(off.level, 'warn', '问不到不等于没启用,不能报成 error')
  } finally {
    fixture.restore()
  }
})

test('埋点上报自检:检测脚本仍在执行即判失效并报给脑', async () => {
  const fixture = installNetGuardFixture({ markerPresent: true })
  try {
    registerNetGuard()
    await fixture.pump()
    assert.equal(fixture.executed.length, 1, '攒够派发数才探一次')
    assert.equal(fixture.executed[0].world, 'MAIN')
    const stale = fixture.staleLog()
    assert.ok(stale, '脚本在执行说明没拦住,必须报')
    assert.equal(stale.level, 'error')
    // handLog 纪律:不得携带任何候选人明文。detail 只允许是计数与时间戳。
    assert.match(stale.detail, /^blocked=\d+ lastBlockedAt=\d+$/)
  } finally {
    fixture.restore()
  }
})

test('埋点上报自检:检测脚本未执行属正常,不判失效', async () => {
  const fixture = installNetGuardFixture({ markerPresent: false })
  try {
    registerNetGuard()
    await fixture.pump()
    assert.equal(fixture.executed.length, 1)
    assert.equal(fixture.staleLog(), undefined, '标记不存在正是规则生效的样子')
  } finally {
    fixture.restore()
  }
})

test('埋点上报自检:命中计数只作观测,盖不住脚本已执行的事实', async () => {
  const fixture = installNetGuardFixture({ markerPresent: true })
  try {
    registerNetGuard()
    fixture.matchListeners[0]()
    fixture.matchListeners[0]()
    assert.equal(netGuardStats().blockedCount, 2)
    await fixture.pump()
    assert.ok(fixture.staleLog(), '拦下过若干次不代表现在还拦得住')
  } finally {
    fixture.restore()
  }
})

test('埋点上报自检:没有平台页时不下结论', async () => {
  const fixture = installNetGuardFixture({ tabs: [], markerPresent: true })
  try {
    registerNetGuard()
    await fixture.pump()
    assert.equal(fixture.executed.length, 0, '没有平台页就无从探测')
    assert.equal(fixture.staleLog(), undefined)
  } finally {
    fixture.restore()
  }
})

test('埋点上报自检:探测注入失败不判失效、不抛异常', async () => {
  const fixture = installNetGuardFixture({ probeThrows: true, markerPresent: true })
  try {
    registerNetGuard()
    await fixture.pump()
    assert.equal(fixture.executed.length, 1)
    assert.equal(fixture.staleLog(), undefined, '注入失败只是没问到,不构成失效结论')
  } finally {
    fixture.restore()
  }
})

// 职位性质 + 代招公司(2026-08-14 甲方裁决:一律选「代招岗位」,并在弹窗里
// 选最后一家代招公司)。DOM 形态照抄甲方当日从真实发布页取的三段快照:
// 未选态、弹窗、已选态。
function partnerNode({ className = '', innerText = '', attrs = {}, sel = {}, selAll = {}, clicks, id }) {
  return {
    className,
    innerText,
    getAttribute: (name) => (name in attrs ? attrs[name] : null),
    getBoundingClientRect: () => ({ height: 40 }),
    querySelector: (selector) => sel[selector] ?? null,
    querySelectorAll: (selector) => selAll[selector] ?? [],
    click: () => clicks.push(id),
  }
}

const LABEL_SELECTOR = ':scope > label, :scope > [class*="label"]'

// variant: 'absent'(没有职位性质这一组) | 'present'(三个选项齐全) | 'valueDrift'(文案在、value 变了)
function installJobNatureFixture(variant) {
  const original = { document: globalThis.document }
  const clicks = []
  const natureButtons = [
    ['代招岗位', variant === 'valueDrift' ? 'AGENT_RECRUIT' : 'AGENT_RECRUITMENT'],
    ['劳务派遣', 'LABOR_DISPATCH'],
    ['自招职位', 'NORMAL'],
  ].map(([text, value]) => partnerNode({
    innerText: text, attrs: { value }, clicks, id: `nature:${text}`,
  }))
  const natureItem = partnerNode({
    className: 'publish-form__jobNature km-form-item',
    sel: { [LABEL_SELECTOR]: { innerText: '职位性质' } },
    selAll: { button: natureButtons },
    clicks,
    id: 'natureItem',
  })
  const employmentItem = partnerNode({
    className: 'publish-form__employment km-form-item',
    sel: { [LABEL_SELECTOR]: { innerText: '工作性质' } },
    selAll: { button: [] },
    clicks,
    id: 'employmentItem',
  })
  globalThis.document = {
    querySelectorAll: (selector) => {
      if (selector === '.km-form-item') {
        return variant === 'absent' ? [employmentItem] : [natureItem, employmentItem]
      }
      return []
    },
  }
  return {
    clicks,
    restore() {
      globalThis.document = original.document
    },
  }
}

test('职位性质 MAIN:文案与 value 双核对后点「代招岗位」,字段缺席是正常路径', () => {
  const present = installJobNatureFixture('present')
  try {
    assert.deepEqual(zhilianTestHooks.mainPickZhilianJobNature(), { status: 'ok', detail: 'picked' })
    // 只点代招岗位那一颗;劳务派遣与自招职位一次都不许碰。
    assert.deepEqual(present.clicks, ['nature:代招岗位'])
  } finally {
    present.restore()
  }

  const absent = installJobNatureFixture('absent')
  try {
    assert.deepEqual(zhilianTestHooks.mainPickZhilianJobNature(), { status: 'ok', detail: 'absent' })
    assert.deepEqual(absent.clicks, [])
  } finally {
    absent.restore()
  }

  // 文案还在但 value 对不上:宁可停下转人工,也不能把职位挂成别的性质发出去。
  const drift = installJobNatureFixture('valueDrift')
  try {
    assert.deepEqual(zhilianTestHooks.mainPickZhilianJobNature(),
      { status: 'failed', reason: 'job_nature_option_absent' })
    assert.deepEqual(drift.clicks, [])
  } finally {
    drift.restore()
  }
})

// rows: 每家公司的名字;'' 表示这一行读不出名字。modal=false 表示弹窗没开。
// picked 是「代招公司」框里已经显示的名字(未选时为空串)。
function installPartnerPickerFixture({ rows = [], modal = true, formRow = true, picked = '' } = {}) {
  const original = { document: globalThis.document, getComputedStyle: globalThis.window?.getComputedStyle }
  const clicks = []
  const items = rows.map((name, index) => {
    const info = partnerNode({
      className: 'partnership-organization-list__item-info',
      sel: { '.info-name': name === '' ? null : { innerText: ` ${name} ` } },
      clicks,
      id: `info:${index}`,
    })
    return partnerNode({
      className: 'partnership-organization-list__item',
      sel: {
        '.partnership-organization-list__item-info': info,
        // 编辑与删除就贴在同一行里:测试要能证明它们从没被点过。
        'a.action-edit': partnerNode({ clicks, id: `edit:${index}` }),
        'a.action-delete': partnerNode({ clicks, id: `delete:${index}` }),
      },
      clicks,
      id: `row:${index}`,
    })
  })
  const listModal = partnerNode({
    className: 'km-modal__wrapper partnership-organization-list',
    selAll: { '.partnership-organization-list__item': items },
    clicks,
    id: 'listModal',
  })
  // 真实页面里同时挂着一个 display:none 的「创建合作公司」弹层,可见性过滤必须挡住它。
  const hiddenModal = {
    className: 'km-modal__wrapper hr-recruitment-modal',
    getBoundingClientRect: () => ({ height: 0 }),
    querySelector: () => null,
    querySelectorAll: () => [],
    click: () => clicks.push('hiddenModal'),
  }
  const entry = partnerNode({
    className: 'partnership-organization-input__content',
    clicks,
    id: 'entry',
  })
  const partnerItem = partnerNode({
    className: 'km-form-item',
    sel: {
      [LABEL_SELECTOR]: { innerText: '代招公司' },
      '.partnership-organization-input__content': entry,
      '.partnership-organization-input__name': { innerText: picked },
    },
    clicks,
    id: 'partnerItem',
  })
  globalThis.window = globalThis.window ?? {}
  globalThis.window.getComputedStyle = (node) => (
    node === hiddenModal ? { display: 'none', visibility: 'visible' }
      : { display: 'block', visibility: 'visible' })
  globalThis.document = {
    querySelectorAll: (selector) => {
      if (selector === '.km-modal__wrapper') return modal ? [listModal, hiddenModal] : [hiddenModal]
      if (selector === '.km-form-item') return formRow ? [partnerItem] : []
      return []
    },
  }
  return {
    clicks,
    restore() {
      globalThis.document = original.document
      globalThis.window.getComputedStyle = original.getComputedStyle
    },
  }
}

test('代招公司选择器 MAIN:已开就不再点,未开点内容区,行没渲染出来时诚实失败', () => {
  const open = installPartnerPickerFixture({ rows: ['阿狸与桃子（上海）信息咨询有限公司'] })
  try {
    assert.deepEqual(zhilianTestHooks.mainOpenZhilianPartnerPicker(), { status: 'ok', detail: 'open' })
    // 弹窗已开还去点框,会把它 toggle 关掉。
    assert.deepEqual(open.clicks, [])
  } finally {
    open.restore()
  }

  const closed = installPartnerPickerFixture({ modal: false })
  try {
    assert.deepEqual(zhilianTestHooks.mainOpenZhilianPartnerPicker(), { status: 'ok', detail: 'clicked' })
    assert.deepEqual(closed.clicks, ['entry'])
  } finally {
    closed.restore()
  }

  // 点完职位性质到这一行渲染出来有间隙:失败让轮询继续等,不是终局。
  const pending = installPartnerPickerFixture({ modal: false, formRow: false })
  try {
    assert.deepEqual(zhilianTestHooks.mainOpenZhilianPartnerPicker(),
      { status: 'failed', reason: 'partner_company_group_absent' })
    assert.deepEqual(pending.clicks, [])
  } finally {
    pending.restore()
  }
})

// detail 是 JSON {outcome, name?, names}:outcome=picked 才真点了;absent / ambiguous /
// unconfigured 是明确不中、由调用方以失败收场;names 是弹窗里实际的公司全称清单。
function partnerPicked(name, names) {
  return { status: 'ok', detail: JSON.stringify({ outcome: 'picked', name, names }) }
}
function partnerMiss(outcome, names) {
  return { status: 'ok', detail: JSON.stringify({ outcome, names }) }
}

// 2026-08-23 甲方裁决(收回 08-22「提示不是闸」):只认去首尾空白后逐字相等且恰好
// 一行;零行/多行/未配置一律不点、不猜、不取最后一家。起因是真机弹窗证实同一账号
// 挂着 5 家代招公司、目标排第 3,旧口径会把职位挂到最后一家(别家)名下发出去。
test('代招公司选择 MAIN:逐字相等恰好一行才点 info 区,编辑与删除绝不触碰', () => {
  const rows = ['青岛厚优企业信息咨询有限公司', '上海卓曜引智商务咨询有限公司', '上海云砚禾信息咨询有限公司',
    '湖北临朔创科技有限公司', '阿狸与桃子（上海）信息咨询有限公司']

  // 逐字相等:选中排第 3 的那家,不是最后一家;首尾空白不影响。
  const exact = installPartnerPickerFixture({ rows })
  try {
    assert.deepEqual(zhilianTestHooks.mainPickZhilianPartnerCompany(' 上海云砚禾信息咨询有限公司 '),
      partnerPicked('上海云砚禾信息咨询有限公司', rows))
    // 落点必须是 info 区:同一行紧挨着的 action-delete 点一下就把客户的代招公司删了。
    assert.deepEqual(exact.clicks, ['info:2'])
  } finally {
    exact.restore()
  }

  // 子串不算命中:运营只填了短名,不点、不猜,把清单带回去让人改配置。
  const contains = installPartnerPickerFixture({ rows })
  try {
    assert.deepEqual(zhilianTestHooks.mainPickZhilianPartnerCompany('上海云砚禾'),
      partnerMiss('absent', rows))
    assert.deepEqual(contains.clicks, [])
  } finally {
    contains.restore()
  }

  // 完全没命中:同样不点,绝不回落最后一家。
  const missing = installPartnerPickerFixture({ rows })
  try {
    assert.deepEqual(zhilianTestHooks.mainPickZhilianPartnerCompany('不存在的公司'),
      partnerMiss('absent', rows))
    assert.deepEqual(missing.clicks, [])
  } finally {
    missing.restore()
  }

  // 同名多行:名字分不出是哪家,按歧义不点。
  const dupRows = ['桃子科技有限公司分公司', '桃子科技有限公司', '桃子科技有限公司']
  const dup = installPartnerPickerFixture({ rows: dupRows })
  try {
    assert.deepEqual(zhilianTestHooks.mainPickZhilianPartnerCompany('桃子科技有限公司'),
      partnerMiss('ambiguous', dupRows))
    assert.deepEqual(dup.clicks, [])
  } finally {
    dup.restore()
  }

  // 未配置(空串/未传):不点,把清单带回去;与旧版"取最后一家"彻底分手。
  for (const wanted of ['', '   ', undefined]) {
    const unconfigured = installPartnerPickerFixture({ rows })
    try {
      assert.deepEqual(zhilianTestHooks.mainPickZhilianPartnerCompany(wanted),
        partnerMiss('unconfigured', rows))
      assert.deepEqual(unconfigured.clicks, [])
    } finally {
      unconfigured.restore()
    }
  }

  // 别的行名字读不到,但配置名逐字命中了有名字的那行:照常选中;清单只含读得出的名字。
  const namelessTail = installPartnerPickerFixture({ rows: ['有名字的公司', ''] })
  try {
    assert.deepEqual(zhilianTestHooks.mainPickZhilianPartnerCompany('有名字的公司'),
      partnerPicked('有名字的公司', ['有名字的公司']))
    assert.deepEqual(namelessTail.clicks, ['info:0'])
  } finally {
    namelessTail.restore()
  }

  // 一行名字都读不出 => 整张列表不是预期结构,是结构失配(交给轮询/转人工),不是"没命中"。
  const nameless = installPartnerPickerFixture({ rows: ['', ''] })
  try {
    assert.deepEqual(zhilianTestHooks.mainPickZhilianPartnerCompany('有名字的公司'),
      { status: 'failed', reason: 'partner_company_name_unresolved' })
    assert.deepEqual(nameless.clicks, [])
  } finally {
    nameless.restore()
  }

  // 列表还没渲染出来 / 弹窗没开:未就绪,交给轮询继续等。
  const empty = installPartnerPickerFixture({ rows: [] })
  try {
    assert.deepEqual(zhilianTestHooks.mainPickZhilianPartnerCompany('上海云砚禾信息咨询有限公司'),
      { status: 'failed', reason: 'partner_company_option_absent' })
    assert.deepEqual(empty.clicks, [])
  } finally {
    empty.restore()
  }
  const noModal = installPartnerPickerFixture({ modal: false })
  try {
    assert.deepEqual(zhilianTestHooks.mainPickZhilianPartnerCompany('上海云砚禾信息咨询有限公司'),
      { status: 'failed', reason: 'partner_company_picker_absent' })
    assert.deepEqual(noModal.clicks, [])
  } finally {
    noModal.restore()
  }
})

// 发布后的 message-box 弹窗(2026-08-23 甲方裁决)。DOM 形态照抄甲方当日从自动发布
// 现场取的真机快照:.km-modal__wrapper > .km-modal--message-box,标题「提示」、正文
// 一个 span、唯一按钮「我知道了」。boxes 每项是 {body, visible?, messageBox?}。
function installPublishMessageBoxFixture(boxes) {
  const original = { document: globalThis.document, getComputedStyle: globalThis.window?.getComputedStyle }
  const clicks = []
  const hidden = new Set()
  const wrappers = boxes.map((box, index) => {
    const body = partnerNode({ className: 'km-modal__body', innerText: `\n  ${box.body}\n`, clicks, id: `body:${index}` })
    const modal = partnerNode({
      className: box.messageBox === false
        ? 'km-modal km-modal--open km-modal--normal'
        : 'km-modal km-modal--open km-modal--v-centered km-modal--message-box km-modal--normal',
      clicks,
      id: `modal:${index}`,
    })
    const wrapper = partnerNode({
      className: 'km-modal__wrapper',
      innerText: `提示\n${box.body}\n我知道了`,
      sel: {
        '.km-modal--message-box': box.messageBox === false ? null : modal,
        '.km-modal__body': body,
      },
      clicks,
      id: `wrapper:${index}`,
    })
    if (box.visible === false) {
      hidden.add(wrapper)
      wrapper.getBoundingClientRect = () => ({ height: 0 })
    }
    return wrapper
  })
  globalThis.window = globalThis.window ?? {}
  globalThis.window.getComputedStyle = (node) => (
    hidden.has(node) ? { display: 'none', visibility: 'visible' } : { display: 'block', visibility: 'visible' })
  globalThis.document = {
    querySelectorAll: (selector) => (selector === '.km-modal__wrapper' ? wrappers : []),
  }
  return {
    clicks,
    restore() {
      globalThis.document = original.document
      globalThis.window.getComputedStyle = original.getComputedStyle
    },
  }
}

test('发布后弹窗 MAIN:只认「职位类别+不匹配」族为拒绝,其它 message-box 只记原话,全程不点任何按钮', () => {
  const reject = '所选职位类别与职位描述不匹配，请重新选择职位类别'

  // 已知族:known=true,正文取 __body、去首尾空白。
  const hit = installPublishMessageBoxFixture([{ body: reject }])
  try {
    assert.deepEqual(zhilianTestHooks.mainReadZhilianPublishMessageBox(),
      { status: 'ok', detail: JSON.stringify({ known: true, texts: [reject] }) })
    assert.deepEqual(hit.clicks, [])
  } finally {
    hit.restore()
  }

  // 同库「我知道了」但不是失败的弹层(热门职位可免费发布):known=false,只记原话。
  const other = installPublishMessageBoxFixture([{ body: '该职位为热门职位，可免费发布' }])
  try {
    assert.deepEqual(zhilianTestHooks.mainReadZhilianPublishMessageBox(),
      { status: 'ok', detail: JSON.stringify({ known: false, texts: ['该职位为热门职位，可免费发布'] }) })
    assert.deepEqual(other.clicks, [])
  } finally {
    other.restore()
  }

  // 两个弹层并存、其中一个命中:known=true,两段原话都带上。
  const both = installPublishMessageBoxFixture([{ body: '该职位为热门职位，可免费发布' }, { body: reject }])
  try {
    assert.deepEqual(zhilianTestHooks.mainReadZhilianPublishMessageBox(),
      { status: 'ok', detail: JSON.stringify({ known: true, texts: ['该职位为热门职位，可免费发布', reject] }) })
  } finally {
    both.restore()
  }

  // 隐藏的 / 不是 message-box 的 km-modal(如敏感词确认层、弹窗已关)一律不算。
  const none = installPublishMessageBoxFixture([
    { body: reject, visible: false },
    { body: '发布信息中包含敏感词', messageBox: false },
  ])
  try {
    assert.deepEqual(zhilianTestHooks.mainReadZhilianPublishMessageBox(),
      { status: 'failed', reason: 'publish_message_box_absent' })
  } finally {
    none.restore()
  }
})

test('代招公司回读 MAIN:框里有名字才算数,空着交给轮询继续等', () => {
  const filled = installPartnerPickerFixture({ picked: ' 阿狸与桃子（上海）信息咨询有限公司 ' })
  try {
    assert.deepEqual(zhilianTestHooks.mainReadZhilianPartnerCompany(),
      { status: 'ok', detail: '阿狸与桃子（上海）信息咨询有限公司' })
  } finally {
    filled.restore()
  }

  const blank = installPartnerPickerFixture({ picked: '' })
  try {
    assert.deepEqual(zhilianTestHooks.mainReadZhilianPartnerCompany(), { status: 'ok', detail: '' })
  } finally {
    blank.restore()
  }

  const gone = installPartnerPickerFixture({ formRow: false })
  try {
    assert.deepEqual(zhilianTestHooks.mainReadZhilianPartnerCompany(),
      { status: 'failed', reason: 'partner_company_group_absent' })
  } finally {
    gone.restore()
  }
})

// ---------------------------------------------------------------------------
// 平台适配层的接缝验收。
//
// 这三条是「多平台抽象层」这一批的**交付判据本身**:如果加一个平台还得改
// base、改分发器或改契约,那抽象就没成立。用假平台验,不实现任何真实第二平台
// (BOSS 的页面事实还没做真机考古,按「平台枚举面事实门」不得凭空实现)。

/** 造一个只实现给定能力的假平台。没给的能力就是「这个平台没有」。 */
function fakePlatform(id, capabilities = {}) {
  return {
    id,
    hostMatch: `https://${id}.example.com/*`,
    // 故意与智联不同:第二平台只能用 ISOLATED、只能走 OS 级注入,
    // 声明面必须容得下这两种取值,否则抽象对它无效。
    world: 'ISOLATED',
    input: 'os',
    ...capabilities,
  }
}

/** 换一套平台跑,跑完把原来的装回去(测试之间共用同一张注册表)。 */
async function withPlatforms(adapters, run) {
  const saved = registeredPlatforms()
  resetPlatformsForTest()
  for (const adapter of adapters) registerPlatform(adapter)
  try {
    return await run()
  } finally {
    resetPlatformsForTest()
    for (const adapter of saved) registerPlatform(adapter)
  }
}

function identifyCommand(ref, platform) {
  return command(Primitive.ChatIdentifyCurrentConversation, {}, {
    context: { platform, accountRef: 'account-seam-fixture' },
  })
}

test('加一个平台只需注册适配器:base、命令分发器与契约一律不动', async () => {
  const seen = []
  const fake = fakePlatform('fake-platform', {
    identifyCurrentConversation({ args, ctx, fingerprint }) {
      seen.push({ args, hasCtx: typeof ctx?.checkpoint === 'function', fingerprint })
      return Promise.resolve({ conversationRef: 'fake-conversation', observedAt: 1_700_000_000_000 })
    },
  })
  await withPlatforms([fake], async () => {
    registerM2Primitives()
    const out = recorder()
    // 生产分发器,原封不动——本批一行都没改它。
    const dispatcher = new Dispatcher(out.send)
    await dispatcher.handleCmd('seam-1', 's', 's', identifyCommand('seam-1', 'fake-platform'))
    await eventually(() => results(out.frames, 'seam-1').length === 1, '假平台命令未收束')

    const [result] = results(out.frames, 'seam-1')
    assert.equal(result.body.status, 'ok')
    assert.equal(result.body.data.conversationRef, 'fake-conversation')
    assert.equal(seen.length, 1, '必须恰好路由到假平台一次')
    assert.equal(seen[0].hasCtx, true, '适配器必须拿到真实 PrimitiveContext(合作式钩子)')
  })
})

test('平台没注册、或平台没实现这条能力,都显式拒绝,绝不默认回成功', async () => {
  const bare = fakePlatform('bare-platform')
  await withPlatforms([bare], async () => {
    registerM2Primitives()
    const out = recorder()
    const dispatcher = new Dispatcher(out.send)

    // 一、平台压根没注册:CTX_NOT_READY,且 retryable=no ——
    // 「本手没有这个平台」不是等一等就会变的事。
    await dispatcher.handleCmd('seam-2', 's', 's', identifyCommand('seam-2', 'never-registered'))
    await eventually(() => results(out.frames, 'seam-2').length === 1, '未注册平台的命令未收束')
    const [missing] = results(out.frames, 'seam-2')
    assert.equal(missing.body.status, 'failed')
    assert.equal(missing.body.error.code, ErrorCode.CtxNotReady)
    assert.equal(missing.body.error.retryable, Retryable.No)
    assert.equal(missing.body.error.sideEffect, 'none')

    // 二、平台注册了但没实现这条能力:显式拒绝(反模式 18)。
    await dispatcher.handleCmd('seam-3', 's', 's', identifyCommand('seam-3', 'bare-platform'))
    await eventually(() => results(out.frames, 'seam-3').length === 1, '缺能力的命令未收束')
    const [unsupported] = results(out.frames, 'seam-3')
    assert.equal(unsupported.body.status, 'failed')
    assert.equal(unsupported.body.error.code, ErrorCode.ProtoUnsupportedCmd)
    assert.equal(unsupported.body.error.retryable, Retryable.No)
  })
})

test('两个平台并存时命令按 context.platform 各走各的,不串线', async () => {
  const hits = []
  const makeAdapter = (id) => fakePlatform(id, {
    identifyCurrentConversation() {
      hits.push(id)
      return Promise.resolve({ conversationRef: `${id}-conversation`, observedAt: 1_700_000_000_000 })
    },
  })
  await withPlatforms([makeAdapter('platform-a'), makeAdapter('platform-b')], async () => {
    registerM2Primitives()
    const out = recorder()
    const dispatcher = new Dispatcher(out.send)

    await dispatcher.handleCmd('seam-4', 's', 's', identifyCommand('seam-4', 'platform-b'))
    await eventually(() => results(out.frames, 'seam-4').length === 1, 'b 平台命令未收束')
    await dispatcher.handleCmd('seam-5', 's', 's', identifyCommand('seam-5', 'platform-a'))
    await eventually(() => results(out.frames, 'seam-5').length === 1, 'a 平台命令未收束')

    assert.deepEqual(hits, ['platform-b', 'platform-a'], '命令必须落到各自声明的平台上')
    assert.equal(results(out.frames, 'seam-4')[0].body.data.conversationRef, 'platform-b-conversation')
    assert.equal(results(out.frames, 'seam-5')[0].body.data.conversationRef, 'platform-a-conversation')
  })
})

test('平台之间相交而互不包含:各有对方没有的能力,各自照跑、越界即拒', async () => {
  // 这条是抽象层的核心假设本身。若按「谁是谁的子集」建模,能力多的那个平台
  // 会被迫替另一个实现它根本没有的东西;反过来也一样。
  const onlyIdentify = fakePlatform('platform-x', {
    identifyCurrentConversation: () =>
      Promise.resolve({ conversationRef: 'x-conversation', observedAt: 1_700_000_000_000 }),
  })
  const onlyUnread = fakePlatform('platform-y', {
    readUnreadTotal: () => Promise.resolve({ total: 7, observedAt: 1_700_000_000_000 }),
  })
  const unreadCommand = (platform) => command(Primitive.ChatReadUnreadTotal, {}, {
    context: { platform, accountRef: 'account-seam-fixture' },
  })

  await withPlatforms([onlyIdentify, onlyUnread], async () => {
    registerM2Primitives()
    const out = recorder()
    const dispatcher = new Dispatcher(out.send)

    // 各自有的那条:照跑。
    await dispatcher.handleCmd('seam-6', 's', 's', identifyCommand('seam-6', 'platform-x'))
    await eventually(() => results(out.frames, 'seam-6').length === 1, 'x 的自有能力未收束')
    assert.equal(results(out.frames, 'seam-6')[0].body.status, 'ok')

    await dispatcher.handleCmd('seam-7', 's', 's', unreadCommand('platform-y'))
    await eventually(() => results(out.frames, 'seam-7').length === 1, 'y 的自有能力未收束')
    assert.equal(results(out.frames, 'seam-7')[0].body.status, 'ok')
    assert.equal(results(out.frames, 'seam-7')[0].body.data.total, 7)

    // 交叉那条:显式拒绝,不借道另一个平台的实现。
    await dispatcher.handleCmd('seam-8', 's', 's', unreadCommand('platform-x'))
    await eventually(() => results(out.frames, 'seam-8').length === 1, 'x 的越界命令未收束')
    assert.equal(results(out.frames, 'seam-8')[0].body.status, 'failed')
    assert.equal(results(out.frames, 'seam-8')[0].body.error.code, ErrorCode.ProtoUnsupportedCmd)

    await dispatcher.handleCmd('seam-9', 's', 's', identifyCommand('seam-9', 'platform-y'))
    await eventually(() => results(out.frames, 'seam-9').length === 1, 'y 的越界命令未收束')
    assert.equal(results(out.frames, 'seam-9')[0].body.status, 'failed')
    assert.equal(results(out.frames, 'seam-9')[0].body.error.code, ErrorCode.ProtoUnsupportedCmd)
  })
})

// 节奏登记表之外**另有**节奏来源的页面变更函数。每一条都要注明归谁管——
// 空着不许进这张表。
//
// 为什么需要这张表:节奏闸按函数引用比对,而登记表在文件里离那些函数六千多行远;
// 「新增一个可见动作、忘了登记」是它唯一的漏网口子,而漏了之后**没有任何症状**
// (不报错、不打日志、测试不红),页面上就是机器速度连点。下面那条用例把这个
// 口子堵在构建期:含点击/派发事件的 main* 函数,不在登记表就必须在这张表里,
// 两张表都没有 = 门禁红。
const KNOWN_OTHER_PACING = {
  // —— 已核实:各自路径里有行内 setTimeout(1_000 + 抖动),点击前都会等 ——
  mainClickZhilianJobSection: '职位管理页分区切换,调用点前有行内节奏',
  mainClickZhilianJobOffline: '下线入口,调用点前有行内节奏',
  mainConfirmZhilianJobOffline: '下线二次确认,调用点前有行内节奏',
  mainCancelZhilianOfflineDialog: '下线取消,调用点前有行内节奏',
  mainClickRevealPeerPhone: '查看电话,调用点前有行内节奏',
  mainSendGreetingOnce: '招呼发送,三个调用点前均有行内节奏',
  mainActivateZhilianNoticeTab: '个人中心「通知」页签切换,导航后有行内节奏(ensureZhilianPersonalTab),点击后再等一秒才读列表',

  // —— 未确认:2026-08-26 静态核查时,调用点前 45 行内没找到节奏构造。
  //    这**不等于**没有节奏(可能在更上层、或在调用者里),只是本次没能从源码
  //    确认。它们全在发布链路之外,是本批之前就有的既有状态,不是本批引入;
  //    按记录级登记,不在本批顺手改。真要收口需要一次独立审计。 ——
  mainApplySourcingFilters: '采集筛选;节奏来源未确认',
  mainSelectSourcingPosition: '采集选职位;节奏来源未确认',
  mainReadSourcingResume: '采集读简历(内含开关弹窗的点击);节奏来源未确认',
  mainReadCurrentResume: 'IM 读简历(内含开关弹窗的点击);节奏来源未确认',
  mainResumeCaptureStep: '简历截图分步;节奏来源未确认',
  mainClickConversationOnce: '点开会话;节奏来源未确认',
  mainEnsureChatListFilter: '会话列表筛选;节奏来源未确认',
  mainClickZhilianBlockedDialogButton: '平台阻塞弹窗处置;节奏来源未确认',
  mainSendMessageOnce: '消息发送;节奏来源未确认(命令间节奏另由脑侧保证)',
  mainPrepareInterviewEditor: '邀面编辑器填充;节奏来源未确认',
  mainCloseInterviewSuccessModal: '邀面成功弹窗清场;节奏来源未确认',
  mainReadListDOMWindow: '会话列表窗口读取(含滚动派发);节奏来源未确认',
  mainReadThreadPage: '会话历史分页(含滚动派发);节奏来源未确认',
}

test('每一次页面点击都有节奏机制认领:新增可见动作忘了登记,门禁当场红', () => {
  const source = readFileSync('src/program/platform/zhilian.ts', 'utf8')
  const sourceLines = source.split('\n')

  const registryBlock = /const zhilianPublishInteractions = new Set<unknown>\(\[([\s\S]*?)\]\)/u.exec(source)
  assert.ok(registryBlock, '节奏登记表不见了——它是发布链路唯一的节奏判据')
  const registered = new Set(
    registryBlock[1].split('\n')
      .map((line) => line.trim().replace(/,$/u, ''))
      .filter((line) => line.length > 0 && !line.startsWith('//')),
  )

  // 每个顶层函数的起始行,用来把「哪一行有点击」归给「哪个函数」。
  const starts = []
  sourceLines.forEach((line, index) => {
    const declared = /^(?:export )?(?:async )?function (\w+)/u.exec(line)
    if (declared) starts.push({ line: index + 1, name: declared[1] })
  })
  const ownerOf = (lineNumber) => {
    let owner = null
    for (const entry of starts) {
      if (entry.line <= lineNumber && (!owner || entry.line > owner.line)) owner = entry
    }
    return owner?.name ?? null
  }

  // 会在页面上产生可见变化的两种调用:点击,以及往输入框派发事件(打字)。
  const mutating = new Set()
  sourceLines.forEach((line, index) => {
    if (line.includes('.click()') || line.includes('.dispatchEvent(')) {
      const owner = ownerOf(index + 1)
      if (owner && owner.startsWith('main')) mutating.add(owner)
    }
  })
  assert.ok(mutating.size > 30, `页面变更函数只扫出 ${mutating.size} 个,扫描口径多半失效了`)

  const unclaimed = [...mutating].filter(
    (name) => !registered.has(name) && !Object.hasOwn(KNOWN_OTHER_PACING, name),
  ).sort()
  assert.deepEqual(unclaimed, [],
    `这些函数会动页面,却既不在节奏登记表、也没在 KNOWN_OTHER_PACING 里认领:` +
    `${unclaimed.join('、')}。要么登记进节奏闸,要么写明归哪套节奏管——` +
    `不许两处都不写,那等于把它变成机器速度连点且无人知晓。`)

  // 反向:登记表里的名字必须在源码里真存在。改名后表里留下化石,闸对那个函数
  // 就永远不生效,而且同样没有症状。
  const defined = new Set(starts.map((entry) => entry.name))
  const fossils = [...registered].filter((name) => !defined.has(name)).sort()
  assert.deepEqual(fossils, [], `节奏登记表里有源码中已不存在的名字: ${fossils.join('、')}`)

  // KNOWN_OTHER_PACING 同样不许留化石,且不许与登记表重叠(两处都写=谁管的说不清)。
  const staleKnown = Object.keys(KNOWN_OTHER_PACING).filter((name) => !defined.has(name)).sort()
  assert.deepEqual(staleKnown, [], `KNOWN_OTHER_PACING 里有源码中已不存在的名字: ${staleKnown.join('、')}`)
  const both = Object.keys(KNOWN_OTHER_PACING).filter((name) => registered.has(name)).sort()
  assert.deepEqual(both, [], `这些函数同时出现在两张表里,归谁管说不清: ${both.join('、')}`)
  for (const [name, note] of Object.entries(KNOWN_OTHER_PACING)) {
    assert.ok(note.trim().length > 0, `${name} 必须注明节奏归谁管`)
  }
})

test('注入接缝:执行世界由适配器声明,不在调用点写死', async () => {
  const originalChrome = globalThis.chrome
  const calls = []
  try {
    globalThis.chrome = {
      scripting: {
        async executeScript(options) {
          calls.push(options)
          return [{ result: { ok: true } }]
        },
      },
    }

    // 只能用 ISOLATED 的平台:声明什么就注入到什么世界。
    const value = await runInPage({ world: 'ISOLATED', label: '某平台' }, 11, () => ({ ok: true }), [])
    assert.deepEqual(value, { ok: true })
    assert.equal(calls[0].world, 'ISOLATED')
    assert.equal(calls[0].target.tabId, 11)

    // 智联仍是 MAIN——它读消息数组要页面 Vue 实例,换 ISOLATED 会瞎。
    await zhilianTestHooks.runMain(12, async () => ({ ok: true }), [])
    assert.equal(calls[1].world, 'MAIN')
    assert.equal(zhilianAdapter.world, 'MAIN')
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('注入接缝:页面内抛出的异常经哨兵还原成真异常,不被当成正常返回值', async () => {
  const originalChrome = globalThis.chrome
  try {
    globalThis.chrome = {
      scripting: {
        async executeScript() {
          return [{ result: { [MAIN_ERROR_SENTINEL]: '页面里炸了' } }]
        },
      },
    }
    await assert.rejects(
      runInPage({ world: 'ISOLATED', label: '某平台' }, 13, () => ({}), []),
      (error) => {
        // 刻意是裸 Error 不是 PlatformError:页面代码自己出错 ≠ 页面没就绪,
        // 两者的处置完全不同,不能混成同一个失败码。
        assert.ok(error instanceof Error)
        assert.ok(!(error instanceof PlatformError))
        assert.match(error.message, /页面里炸了/u)
        return true
      },
    )
  } finally {
    globalThis.chrome = originalChrome
  }
})

test('智联适配器把 36 条能力实现齐,并如实声明自己的执行世界与输入通道', () => {
  // 少一条能力,对应原语在真机上会以 PROTO_UNSUPPORTED_CMD 静默退化;
  // 这条用例让它在门禁上就红。
  const required = [
    'probePlatform', 'ensureSurface', 'readWechatSetting', 'readNotices',
    'readList', 'readThread', 'readUnreadTotal', 'identifyCurrentConversation', 'openConversation',
    'readCurrentCandidate', 'readResume',
    'selectSourcingPosition', 'applySourcingFilters', 'readSourcingWindow',
    'readSourcingResume', 'readSourcingTargetResume',
    'sendGreeting', 'sendMessage', 'sendWechatInvite', 'acceptWechat', 'sendInviteCard',
    'readGreetingOutcome', 'readWechatExchangeOutcome',
    'captureThreadScreenshot', 'captureResumeScreenshot', 'readPeerPhone', 'revealPeerPhone',
    'readPublishedJobs', 'readJobClassCandidates', 'readJobKeywordVocabulary',
    'prepareJobDraft', 'publishJobDraft', 'takeJobOffline',
    'inspectSendSurface', 'probeInterviewEditor', 'capturePageSnapshot',
  ]
  assert.equal(required.length, 36)
  for (const name of required) {
    assert.equal(typeof zhilianAdapter[name], 'function', `智联适配器缺能力 ${name}`)
  }
  assert.equal(zhilianAdapter.id, 'zhilian')
  // MAIN 是感知逼出来的(消息数组只能经页面 Vue 实例拿),不是动作需要。
  assert.equal(zhilianAdapter.world, 'MAIN')
  assert.equal(zhilianAdapter.input, 'intrinsic')
})

// ——— 批 B:base 层平台知识搬迁 ———

test('站点登记表与 manifest 必须对得上:漏一条,那个平台的 content script 永远不注入且无任何症状', () => {
  const manifest = JSON.parse(readFileSync('manifest.json', 'utf8'))
  const contentMatches = new Set((manifest.content_scripts ?? []).flatMap((entry) => entry.matches ?? []))
  const hostPermissions = new Set(manifest.host_permissions ?? [])
  const rulesetIds = new Set((manifest.declarative_net_request?.rule_resources ?? []).map((r) => r.id))
  const sites = allSites()
  assert.ok(sites.length > 0, '站点登记表不能是空的')

  for (const site of sites) {
    // 这条才是本用例存在的理由:manifest 少一行,Chrome 不会报错、测试不会红、
    // 日志里什么都没有 —— 页面上就是"传感器永远不上线",查起来毫无线索。
    assert.ok(contentMatches.has(site.match),
      `manifest content_scripts.matches 缺 ${site.match},${site.id} 的 content script 永不注入`)
    assert.ok(hostPermissions.has(site.match) || hostPermissions.has('<all_urls>'),
      `manifest host_permissions 覆盖不到 ${site.match},注入与 tabs.query 都会被拒`)
  }
  for (const match of contentMatches) {
    assert.ok(sites.some((site) => site.match === match),
      `manifest 注入了 ${match},但站点表不认它 —— 那里的 content script 认不出自己在哪,白装一个`)
  }
  for (const adapter of registeredPlatforms()) {
    const guard = adapter.envReportGuard
    if (!guard) continue
    assert.ok(rulesetIds.has(guard.rulesetId),
      `${adapter.id} 声明了埋点守卫 ${guard.rulesetId},但 manifest 没静态声明这个规则集`)
  }
})

test('base 不许引入任何具体平台的模块:平台知识只经 sites/registry/types 三张平台无关的表进来', () => {
  // 组合根豁免 —— 「加一个平台 = background.ts 多一行」正是这套抽象的出口。
  const COMPOSITION_ROOT = 'background.ts'
  const PLATFORM_AGNOSTIC = new Set(['sites', 'registry', 'types', 'inject'])
  const files = readdirSync('src/base').filter((name) => name.endsWith('.ts'))
  assert.ok(files.includes(COMPOSITION_ROOT), 'base 目录形状变了,本门禁需要复核')

  const offenders = []
  for (const file of files) {
    if (file === COMPOSITION_ROOT) continue
    const source = readFileSync(`src/base/${file}`, 'utf8')
    for (const match of source.matchAll(/from '\.\.\/program\/platform\/([\w./-]+)'/gu)) {
      if (!PLATFORM_AGNOSTIC.has(match[1])) offenders.push(`${file} -> ${match[1]}`)
    }
  }
  assert.deepEqual(offenders, [],
    `base 里出现了具体平台的依赖:${offenders.join('、')}。平台事实要么进适配器,要么进站点表`)
})

test('传感桥按平台分区:canonical、登录基线与上下文健康各算各的,一个平台的掉登录不得记到另一个头上', () => {
  // 刻意不叫 boss —— BOSS 的页面事实尚未考古,这里只验分区的形状。
  const other = {
    id: 'demo2',
    origin: 'https://demo2.example.com',
    match: 'https://demo2.example.com/*',
    matches: (url) => typeof url === 'string' && url.startsWith('https://demo2.example.com/'),
    pageKind: (url) => (url.includes('/im') ? PageKind.Im : PageKind.Other),
    // 拿不到就 unknown:某个平台无法被动感知登录态是允许的,方向仍是"不确认"。
    readLoginState: () => LoginState.Unknown,
  }
  setSitesForTest([zhilianSite, other])
  try {
    const connection = new FakeSensorConnection()
    const bridge = new SensorBridge(connection, () => 1_000)
    connection.setContext({ platform: 'zhilian', accountRef: 'account-z', expectedPrincipalFingerprint: 'fp-z' })
    connection.setContext({ platform: 'demo2', accountRef: 'account-d', expectedPrincipalFingerprint: 'fp-d' })

    const zTab = { tabId: 1, active: true, url: 'https://rd6.zhaopin.com/app/im', windowId: 1 }
    const dTab = { tabId: 2, active: true, url: 'https://demo2.example.com/im', windowId: 1 }
    for (const tab of [zTab, dTab]) {
      bridge.acceptContentMessage({ type: CONTENT_MESSAGE.Ready, at: 0, url: tab.url }, tab)
      bridge.acceptContentMessage({ type: CONTENT_MESSAGE.LoginStable, observedAt: 0, state: LoginState.In }, tab)
    }

    const byPlatform = () => Object.fromEntries(connection.contextHealth.map((c) => [c.platform, c]))
    assert.deepEqual(Object.keys(byPlatform()).sort(), ['demo2', 'zhilian'], '两个平台各报一条')
    assert.equal(byPlatform().zhilian.ready, true)
    assert.equal(byPlatform().demo2.ready, true)

    // demo2 掉登录:事件必须姓 demo2、带 demo2 的 accountRef。
    // 分区没做对时,这一条会以 zhilian/account-z 的名义发出去 —— 脑随即停掉一个
    // 根本没掉线的账号,真掉线的那个继续跑。这是错靶,不是显示问题。
    bridge.acceptContentMessage({ type: CONTENT_MESSAGE.LoginStable, observedAt: 2_000, state: LoginState.Out }, dTab)
    const dropped = connection.events.at(-1)
    assert.equal(dropped.name, EventName.LoginStateChanged)
    assert.equal(dropped.platform, 'demo2')
    assert.equal(dropped.accountRef, 'account-d')
    assert.equal(byPlatform().zhilian.ready, true, '别人掉登录不得污染智联的健康')
    assert.equal(byPlatform().demo2.reason, NotReadyReason.LoginRequired)

    // 「有页但脚本死了」也只能看自己平台的标签页:demo2 有页无传感,
    // 智联连页都没有,两者的 reason 必须不同。
    bridge.removeTab(1)
    bridge.removeTab(2)
    bridge.noteChromeNavigation(9, 'https://demo2.example.com/im')
    bridge.refreshCachedState()
    assert.equal(byPlatform().demo2.reason, NotReadyReason.ContentScriptDead)
    assert.equal(byPlatform().zhilian.reason, NotReadyReason.PageAbsent,
      '别的平台开着页,不能替智联作证「页在、只是脚本死了」')
  } finally {
    resetSitesForTest()
  }
})

test('埋点上报自检由适配器声明驱动:没声明守卫的平台不探测,也不伪造一条结论', async () => {
  const fixture = installNetGuardFixture({ markerPresent: true })
  try {
    await withPlatforms([fakePlatform('no-guard')], async () => {
      registerNetGuard()
      await fixture.pump()
      assert.equal(fixture.executed.length, 0, '没声明守卫就不该去页面上探测')
      assert.equal(fixture.staleLog(), undefined, '没有守卫可谈失效')
      assert.equal(fixture.offLog(), undefined, '不得对着一个不存在的规则集报未启用')
      assert.equal(fixture.blindLog(), undefined, '连自检都不做,不必解释命中数观测不到')
    })
  } finally {
    fixture.restore()
  }
})

// ---------------------------------------------------------------------------
// osengine:从 hiBoss 搬入的鼠标轨迹引擎
//
// 这一组测的不是"轨迹像不像人"——那要判别器,是研究设备,永远留在 hiBoss。
// 这里测的是**我们这一侧的胶水没有把上游的东西改掉**:随机数派生、参数传递、
// 取整口径、抽 pressMs 的时机。任何一处偏了,同种子就生成不出同一条轨迹,
// 那边的离线跑分与我们真机播的就不是一个东西,而且不会有任何报错。
// ---------------------------------------------------------------------------

const OSENGINE_FIXTURE = JSON.parse(
  readFileSync('test/fixtures/osengine-hiboss-60e3880.json', 'utf8'),
)

test('osengine 与 hiBoss 原件逐点一致(基准由上游原始文件生成)', () => {
  const { from, to, targetW, maxDwellMs, cases } = OSENGINE_FIXTURE
  assert.equal(maxDwellMs, DEFAULT_MAX_DWELL_MS, '基准的截断值必须与我们的缺省一致')
  assert.equal(OSENGINE_SOURCE.commit, '60e3880', '版本钉子与基准文件名必须同步')

  for (const [seedText, expected] of Object.entries(cases)) {
    const plan = planMove({ from, to, targetW, maxDwellMs, seed: Number(seedText) })
    assert.deepEqual(
      plan.points.map((p) => [p.x, p.y, p.t]),
      expected.points.map((p) => [p.x, p.y, p.t]),
      `种子 ${seedText} 的轨迹与上游不一致`,
    )
    assert.equal(plan.pressMs, expected.pressMs, `种子 ${seedText} 的 pressMs 与上游不一致`)
  }
})

test('osengine 末点落在目标上,时刻单调不减', () => {
  const { from, to, targetW, maxDwellMs, cases } = OSENGINE_FIXTURE
  for (const seedText of Object.keys(cases)) {
    const { points } = planMove({ from, to, targetW, maxDwellMs, seed: Number(seedText) })
    const last = points.at(-1)
    // 末段刻意落在目标上(引擎最后一条腿的终点就是 x1,y1),取整到三位后允许千分位误差。
    assert.ok(Math.abs(last.x - to.x) <= 0.01 && Math.abs(last.y - to.y) <= 0.01,
      `种子 ${seedText} 末点 ${last.x},${last.y} 未落在目标 ${to.x},${to.y}`)
    for (let i = 1; i < points.length; i++) {
      assert.ok(points[i].t >= points[i - 1].t,
        `种子 ${seedText} 第 ${i} 帧时刻倒退:${points[i - 1].t} -> ${points[i].t}`)
    }
  }
})

test('osengine 同输入必然同输出(确定性,标定与复现的前提)', () => {
  const { from, to, targetW, maxDwellMs } = OSENGINE_FIXTURE
  const a = planMove({ from, to, targetW, maxDwellMs, seed: 5 })
  const b = planMove({ from, to, targetW, maxDwellMs, seed: 5 })
  assert.deepEqual(a, b)
  const c = planMove({ from, to, targetW, maxDwellMs, seed: 6 })
  assert.notDeepEqual(a.points, c.points, '换种子必须换轨迹')
})

test('osengine 的 pressMs 来自实测池而不是常数', () => {
  const { from, to, targetW, maxDwellMs } = OSENGINE_FIXTURE
  const drawn = new Set()
  for (let seed = 1; seed <= 40; seed++) {
    const { pressMs } = planMove({ from, to, targetW, maxDwellMs, seed })
    assert.ok(Number.isFinite(pressMs) && pressMs > 0, 'pressMs 必须是正数')
    drawn.add(pressMs)
  }
  // 常数会给出一串一模一样的数字,那是零误伤的机器签名。真人 464 条按压里
  // 中位 96ms、四分位 86~110,所以 40 次抽样出现多个不同取值是必然的。
  assert.ok(drawn.size >= 10, `40 次抽样只出现 ${drawn.size} 个不同的 pressMs,疑似退化成常数`)
})

// ---------------------------------------------------------------------------
// osengine/compose:从 hiBoss 搬入的打字排版器
//
// 与鼠标那组同理,测的不是"打字像不像人"——那要判别器,永远留在 hiBoss。
// 这里测的是**我们这一侧没有把上游改掉**。打字线尤其经不起移植:上游的 LCG
// `s*1103515245+12345` 在 s 接近 2^31 时乘积超过 2^53,**JS 的精度丢失是结果的
// 一部分**;换个语言、换个 RNG,生成的分布就悄悄偏了,而且不会有任何报错。
// ---------------------------------------------------------------------------

const COMPOSE_FIXTURE = JSON.parse(
  readFileSync('test/fixtures/osengine-compose-hiboss-60e3880.json', 'utf8'),
)

/** 上游 injector.go 的 shiftGuard:Shift 必须在下一个键按下前至少这么久松开。 */
const SHIFT_GUARD_MS = 40

/** 把一份计划里的全部按键(含上屏键)按 down 排成一列。 */
function flattenPlanKeys(plan) {
  const keys = []
  for (const w of plan.words) {
    for (const k of w.keys) keys.push(k)
    if (w.commit) keys.push(w.commit)
  }
  return keys.sort((a, b) => a.down - b.down)
}

test('osengine/compose 与 hiBoss 原件逐字段一致(基准由上游原始文件生成)', async () => {
  assert.equal(OSENGINE_SOURCE.commit, '60e3880', '版本钉子与基准文件名必须同步')
  assert.ok(OSENGINE_SOURCE.files.includes('compose'), '版本钉子要覆盖排版器的来源')

  for (const [name, { text, bySeed }] of Object.entries(COMPOSE_FIXTURE.cases)) {
    for (const [seedText, expected] of Object.entries(bySeed)) {
      const got = await planType(text, Number(seedText))
      assert.equal(got.ok, expected.ok, `${name}/${seedText} 的成败与上游不一致`)
      if (!expected.ok) continue
      assert.equal(got.seed, expected.seed, `${name}/${seedText} 命中的种子与上游不一致`)
      assert.equal(got.tries, expected.tries, `${name}/${seedText} 的重采次数与上游不一致`)
      assert.deepEqual(got.plan, expected.plan, `${name}/${seedText} 的计划与上游不一致`)
    }
  }
})

test('osengine/compose 同步上游 60e3880:「」【】『』·–￥ 有键位了——「」走 BracketLeft/Right 不带 Shift,『』同键带 Shift,– 与 — 同键;第四趟真机就是「」把两条招呼正文拦在打字前', async () => {
  const r = await planType('看到您做过「获客号」运营，在『上海』，薪资20–35K', 1)
  assert.ok(r.ok, `应当排得出:${(r.reasons ?? []).join(';')}`)
  const direct = r.plan.words.filter((w) => w.direct)
  // 带 Shift 的直接段键序是 [ShiftLeft, 主键]:看主键,不看修饰键。
  const byText = Object.fromEntries(direct.map((w) => [w.text, w.keys.find((k) => k.code !== 'ShiftLeft')]))
  assert.equal(byText['「'].code, 'BracketLeft'); assert.ok(!byText['「'].shift, '「 不带 Shift')
  assert.equal(byText['」'].code, 'BracketRight')
  assert.equal(byText['『'].code, 'BracketLeft'); assert.equal(byText['『'].shift, true, '『 带 Shift,与「」配套')
  assert.equal(byText['–'].code, 'Minus'); assert.equal(byText['–'].shift, true, 'en dash 键位跟 — 同')
  for (const ch of ['「', '」', '『', '』', '【', '】', '·', '–', '￥', '¥']) {
    const k = keyFor(tokenize(ch)[0]); assert.ok(k && k.passthrough === false, `${ch} 走组字上屏,不透传`)
  }
  // 全角 ASCII 变体故意不收:主流输入法中文模式下这些键仍出半角
  assert.equal(keyFor(tokenize('％')[0]), null)
})

test('osengine/compose 打不出的字元要显式失败,不许编一个假键序', async () => {
  // 安全性质在于**宁可失败也不编**。2026-08-31 上游修过一个静默 bug:
  // pinyin-pro 给「嗯」的 `ng` 是词典注音,真人打的是 `en`,于是排出了
  // KeyN,KeyG,KeyN,KeyG —— 一个没有真人会敲的键序,而排版器照样返回 ok:true。
  // 自研 TIP 走 commit(word) 直接上屏、不查拼音串,屏幕上完全看不出问题;
  // **一旦回落到系统输入法(我们的 macOS 开发环境就是),ng 上不了屏**。
  //
  // 「呣」的注音是 m,不是完整音节,IME 会当声母等韵母,真人也打不出——所以上游
  // 刻意不给它编输入音,而是显式失败。
  //
  // 失败的**形状**自上游 2026-09-01 起是返回值,不再是 throw:「有打不出的字元」与
  // 「重采 N 次都没过预检」合成一种 `{ok:false, reasons}`。理由是同一件事两种形状
  // 会让漏掉 catch 的那一套把**一条**文案的问题变成**整批**停机。
  // 抛出去的只剩排版器自己坏了那一类——那种每条文案都会撞上,该停下来让人看见。
  const r = await planType('呣', 1)
  assert.equal(r.ok, false, '「呣」必须排不出来')
  assert.equal(r.plan, null)
  assert.match(r.reasons.join(';'), /打不出/, '原因要说清是打不出,不是排不出')
  assert.match(r.reasons.join(';'), /呣/, '原因要指名是哪个字元')
  // 对照:同样极短、同样曾经排不出来的「嗯」,修好之后必须能排出来。
  const ok = await planType('嗯', 1)
  assert.ok(ok.ok, '「嗯」修好后应当排得出来')
  const codes = ok.plan.words.flatMap((w) => w.keys.map((k) => k.code))
  assert.deepEqual(codes, ['KeyE', 'KeyN'], '「嗯」要按真人打的 en,不是词典注音 ng')
})

test('osengine/compose 的 Shift 必须在下一个键按下前松开(「薪资」→「Xin子」那个 bug)', () => {
  // 2026-08-21 上游真机:Slash 的 dwell 采到 125ms,而到下一个键只有 86ms,
  // Shift 压到了下一个字母上,输入法收到大写 X 当成英文。down→down 的间隔模型
  // 不管上一个键何时松手,所以时序职责在排版器——这条用例就是钉住它没退化。
  for (const [name, { bySeed }] of Object.entries(COMPOSE_FIXTURE.cases)) {
    for (const [seedText, c] of Object.entries(bySeed)) {
      if (!c.ok) continue
      const keys = flattenPlanKeys(c.plan)
      for (const m of keys.filter((k) => k.modifier)) {
        for (const k of keys) {
          if (k === m) continue
          if (k.down > m.down && k.down < m.up) {
            assert.ok(k.shift,
              `${name}/${seedText}:${k.code} 落在 Shift 按住的窗口里却没标 shift`)
          } else if (k.down >= m.up) {
            assert.ok(k.down - m.up >= SHIFT_GUARD_MS,
              `${name}/${seedText}:Shift 松手于 ${m.up},而 ${k.code} 在 ${k.down} 按下,`
              + `间隔 ${k.down - m.up}ms 不足 ${SHIFT_GUARD_MS}ms`)
            break
          }
        }
      }
    }
  }
})

test('osengine/compose 能发出的键位全集钉死——变了 Go 侧键码表就得补', () => {
  // Go 键码表(keymap_darwin.go / keymap_windows.go)按这个集合建,期望在
  // keymap_expect_test.go。**上游再长出新键位,这条先红**——否则我们一片绿,直到真机上
  // 遇到那个字元,注入层报「键码表里没有」,或者更坏:某天有人给它加了兜底。
  //
  // 集合不再只看 PUNCT_KEY(全角,走组字):上游 2026-09-01 加了 ASCII_KEY(半角,透传),
  // 它没有 export,所以这里**走 keyFor 逐字元问**,那才是排版器真正用的路。
  const halfWidth = '`-=[]\\;\',./~!@#$%^&*()_+{}:"<>?'
  const probe = [...Object.keys(PUNCT_KEY), ...halfWidth, ' ', '　', '\n', '0', '５']
  const codes = new Set()
  for (const ch of probe) {
    const k = keyFor(tokenize(ch)[0])
    assert.ok(k, `${JSON.stringify(ch)} 应当有键位`)
    codes.add(k.code)
    assert.equal(typeof k.passthrough, 'boolean', `${JSON.stringify(ch)} 的 passthrough 必须显式给出`)
  }
  const nonDigit = [...codes].filter((c) => !c.startsWith('Digit')).sort()
  assert.deepEqual(nonDigit, [
    'Backquote', 'Backslash', 'BracketLeft', 'BracketRight', 'Comma', 'Enter', 'Equal',
    'Minus', 'Period', 'Quote', 'Semicolon', 'Slash', 'Space',
  ], '上游能发出的键位变了:同步 keymap_expect_test.go 的 punctKeysFromUpstream')
  // Enter 在这里、**不在** Go 表里——那是刻意的(refusedOnPurpose):裸 Enter 是发送。
  // 但它不是前门:boss.osType 在排版前就把换行删掉了(stripNewlines,甲方 2026-09-02
  // 裁决"不给上层找麻烦"),Go 这道拒绝只是后手,正常路径碰不到。
  assert.ok(codes.has('Enter'), '换行段的 Enter 是排版器真能发出的,Go 侧的拒绝必须是显式的')
})

test('osengine/compose 的英文段走 composition,键序全小写、大小写由上屏词带出', async () => {
  // 2026-09-01 上游的设计:自研 TIP 决定上屏内容,所以英文不需要"切输入法模式"——
  // 它就是一个普通的 ime 段,键序 b,a,s,e + 一个上屏键,上屏的文本由词表指定。
  // TIP 一行没改,改的全在排版器。
  //
  // 这条钉住四个刻意的取舍。它们不是实现细节,每一条都有理由:
  const r = await planType('岗位是Java后端，Base深圳', 1)
  assert.ok(r.ok, '中英混排应当排得出来')
  const latin = r.plan.words.filter((w) => /^[A-Za-z]+$/.test(w.text))
  assert.deepEqual(latin.map((w) => w.text), ['Java', 'Base'], '两个英文词都要成段')

  for (const w of latin) {
    // 一、走 composition,不是直接键入。direct 的段没有 commit 键。
    assert.ok(!w.direct, `${w.text} 应当走 composition`)
    assert.ok(w.commit, `${w.text} 应当有上屏键`)

    // 二、**键序全小写,不排 Shift**。大写由上屏的词带出来——真人在中英混输里
    // 从候选选首字母大写的那一项,键序也是全小写。这样不必给"打大写字母的 Shift"
    // 编时序:现有 shift 参数取自打全角标点的样本(句读位置的动作),套到词中间
    // 会把 B→a 顶到几百毫秒。
    const codes = w.keys.map((k) => k.code)
    assert.deepEqual(codes, [...w.text.toLowerCase()].map((c) => 'Key' + c.toUpperCase()),
      `${w.text} 的键序应当是全小写字母`)
    assert.ok(w.keys.every((k) => !k.shift && !k.modifier),
      `${w.text} 不该出现 Shift —— 大小写由上屏的词带,不由按键带`)

    // 三、**上屏文本保留大小写**。「按什么键」与「出什么字」是分开的。
    assert.match(w.text, /^[A-Z]/, `${w.text} 的首字母大写要保住`)

    // 四、**没有音节分隔**。真输入法打英文时也不画那个撇号。
    assert.deepEqual(w.splits ?? [], [], `${w.text} 不该有音节边界`)
  }

  // 上屏键必须在 Go 侧键码表里 —— 英文段用的是与中文段同一张 commitKeys
  // (Space + Digit2/3/4),不是另立一套。
  for (const w of latin) {
    assert.match(w.commit.code, /^(Space|Digit[234])$/,
      `${w.text} 的上屏键 ${w.commit.code} 不在中文段那张 commitKeys 里`)
  }
})

test('osengine/compose 半角标点走透传:排得出、标 passthrough,只有竖线仍拒绝', async () => {
  // 上游 2026-09-01 放行。关键不是"能打了",是**信息流向修正了**:「这个直接段走不走
  // 组字」排版器一直知道(synth 靠它展开事件流),却从没告诉过 TIP;TIP 手上只有键码,
  // 同一个 Comma 键可能是「，」也可能是 ",",它没法分辨,于是把数字/空格/OEM 键
  // 一律当上屏键吃掉。现在排版器随键位给出 passthrough,一路送到 TIP。
  //
  // 判据是「这个键在美式布局上按下去,出来的就是这个字元吗」——是键位表的属性,
  // 不是 Unicode 区间的属性。上游第一版用 containsChinese 判,`— …` 六个全角标点
  // 在 U+2000 段、会被判成透传,真机上 Minus+Shift 打出来的是 `_`。
  const r = await planType('年薪20-30万，前端/后端', 1)
  assert.ok(r.ok, `半角连字符与斜杠应当排得出来:${r.ok ? '' : r.reasons}`)
  const byText = Object.fromEntries(r.plan.words.filter((w) => w.direct).map((w) => [w.text, w]))
  for (const ch of ['2', '0', '-', '3', '/']) {
    assert.ok(byText[ch], `${ch} 应当是一个直接段`)
    assert.equal(byText[ch].passthrough, true, `${ch} 是键盘布局直接打出来的,必须标透传`)
    assert.ok(!byText[ch].commit, `${ch} 透传段没有上屏键`)
  }
  // 全角「，」相反:Comma 键打出来的是 ",",「，」只能由 TIP 上屏——走组字。
  assert.equal(byText['，'].passthrough, false, '全角逗号必须走组字,不能透传')

  // 只有 `|` 仍拒:它是 Go→TIP 词表协议的字段分隔符,装不进去。这是真实边界,
  // 不是当初"要切输入法模式"那种过期的理由。
  const bar = await planType('a|b', 1)
  assert.equal(bar.ok, false, '竖线必须仍被拒绝')
  assert.match(bar.reasons.join(';'), /竖线|分隔符/, '原因要说清是协议分隔符')
})

test('boss.osType 打字前把换行删掉——删掉不是拒绝,删了多少要报出去', () => {
  // 甲方 2026-09-02 裁决:一个换行不是大问题,不该让整条命令失败、把麻烦推给上层。
  // 两条路(放行 Enter / 删掉换行)里选删掉:放行赌的是「Shift 松早了半截话发出去」
  // 那条红线,而 TIP 里还没有对应的闸;删掉只是少一个换行,方向是少做。
  assert.deepEqual(stripNewlines('你好\n方便聊聊吗'), { text: '你好方便聊聊吗', removed: 1 })
  // Windows 剪贴板来的 \r\n 算一个换行,不是两个字元各删各的
  assert.deepEqual(stripNewlines('第一行\r\n第二行'), { text: '第一行第二行', removed: 2 })
  assert.deepEqual(stripNewlines('a\rb'), { text: 'ab', removed: 1 }, '单独的 \\r 也去掉,排版器本来就打不出它')
  assert.deepEqual(stripNewlines('没有换行'), { text: '没有换行', removed: 0 })
  assert.deepEqual(stripNewlines('\n\n'), { text: '', removed: 2 }, '全是换行时文案为空,由调用方判 planFailed')
})

test('osengine/compose 同输入必然同输出(复现与门禁的前提)', async () => {
  const a = await planType('你好，方便加个微信聊聊吗', 1)
  const b = await planType('你好，方便加个微信聊聊吗', 1)
  assert.deepEqual(a, b)
  const c = await planType('你好，方便加个微信聊聊吗', 99)
  assert.ok(a.ok && c.ok)
  assert.notDeepEqual(a.plan, c.plan, '换种子必须换计划')
})

test('osengine/compose 的上屏键不是清一色 Space——数字选词是真人的形状', async () => {
  // 上游 params.mjs:约 14% 的段落用 Digit2/3/4 选第 N 个候选。全是 Space 意味着
  // 我们把上游的分布改掉了,而那是零方差的机器签名。
  const commits = new Set()
  for (const { bySeed } of Object.values(COMPOSE_FIXTURE.cases)) {
    for (const c of Object.values(bySeed)) {
      if (!c.ok) continue
      for (const w of c.plan.words) if (w.commit) commits.add(w.commit.code)
    }
  }
  assert.ok(commits.has('Space'), '上屏键里应当有 Space')
  assert.ok(commits.size >= 2,
    `基准里的上屏键只有 ${[...commits].join(',')} 一种,数字选词的分支没被覆盖或已被改掉`)
})

// ---------------------------------------------------------------------------
// 真机第一跑的回归:平台失败逃出映射
//
// `debug.osProbe` 第一版没接 platformFailure,于是「智联 IM 页面不存在」这条本该是
// CTX_NOT_READY / pageAbsent / sideEffect=none 的失败,回到脑那边成了
// INTERNAL_HAND / sideEffect=possible ——一次什么都没做的失败被记成
// 「副作用可能发生了」,方向正好反了。
//
// 判据盯的是**原语的返回**,不是 platformFailure 这个函数本身:漏掉的是接线,
// 只测函数照样绿。
// ---------------------------------------------------------------------------

test('osProbe 把平台失败如实映射,不逃成 INTERNAL_HAND', async () => {
  resetPlatformsForTest()
  try {
    registerPlatform({
      id: 'fakeplat', hostMatch: 'https://example.invalid/*', world: 'ISOLATED', input: 'os',
      osProbe: () => {
        throw new PlatformError('CTX_NOT_READY', '假平台页面不存在', 'afterRecovery', 'pageAbsent')
      },
    })
    const prim = lookup('debug.osProbe')
    assert.ok(prim, 'debug.osProbe 必须已注册')
    assert.equal(prim.class, 'intrusive')
    const out = await prim.handler({ target: 'viewportSpread' }, {
      cmdMsgId: 'm-test', deadlineMs: Date.now() + 60_000, irreversibleNotAfterMs: Date.now() + 60_000,
      commandContext: { platform: 'fakeplat' }, guards: undefined,
      signal: new AbortController().signal,
      progress: () => {}, checkpoint: () => {}, beforeSideEffect: async () => {},
    })
    assert.equal(out.status, 'failed')
    assert.equal(out.error.code, 'CTX_NOT_READY', '错误码必须原样带出,不得退化成 INTERNAL_HAND')
    assert.equal(out.error.sideEffect, 'none',
      'intrusive 原语没有资格用 possible —— 什么都没做的失败不能报成"副作用可能发生了"')
    assert.equal(out.error.data?.reason, 'pageAbsent', 'notReady 原因必须随行,否则排障只剩一句英文原语名')
  } finally {
    resetPlatformsForTest()
    registerPlatform(zhilianAdapter)
  }
})

// 移动之前的拒绝判据。**这一组的每一条都对应一次真实的鼠标失控风险。**
// 副屏**必须放行**。这条是反着写的:2026-08-28 曾因副屏上光标飞了 34 秒而加过一道
// "副屏一律拒绝",两点实测之后证明根因是种子公式抄错了平台(macOS 不该乘 dpr),
// 与在哪块屏无关。那道闸拿一个可修的 bug 换了一条永久产品约束,已撤。
// 留这条用例是为了不让它被"顺手"加回来。
test('osProbe 不因窗口在副屏而拒绝', () => {
  const view = { innerW: 1470, innerH: 662, screenX: 2560, screenY: 517, dpr: 2, availLeft: 2560, availTop: 517 }
  assert.equal(refuseBeforeMoving(view), null, '副屏必须放行——根因在种子,不在屏')
  assert.equal(refuseBeforeMoving({ ...view, screenX: 100, availLeft: 0, availTop: 38 }), null, '主屏当然放行')
  assert.ok(refuseBeforeMoving({ ...view, innerW: 0 }), '视口读不出来时必须拒绝')
})

// 契约里没有浮点类型,而手服务算出来的滞后是浮点。真机第一次成功跑完 34.6 秒之后,
// result 就是被 `$.data.lagMaxUs: 需要整数` 拦在回程上,那一趟的数据全丢了。
test('osProbe 的契约 data 全是整数', () => {
  const data = osProbeContractData('viewportSpread', {
    outcome: 'landed', attempts: 2, landingDriftPx: 1.2, calibStatus: '就绪',
    unreachableFrames: 0, planMs: 903.7, elapsedMs: 34627.4, lagMaxUs: 22641.83,
  }, 1756000000000)
  for (const k of ['attempts', 'landingDriftPx', 'unreachableFrames', 'planMs', 'elapsedMs', 'lagMaxUs', 'observedAt']) {
    assert.ok(Number.isInteger(data[k]), `${k} 必须是整数,实际 ${data[k]}`)
  }
  assert.equal(data.landingDriftPx, 2, '偏差向上取整——宁可报大不报小')
})

test('观测分片环:追加读回一致,键带前缀,且不重写已满的片', async () => {
  const store = memoryWitnessStorage()
  await telemetryAppend(store, TELEMETRY_KIND_UPLOAD, [{ i: 0 }], TELEMETRY_MAX_UPLOAD_CHUNKS)
  assert.deepEqual(await telemetryReadAll(store, TELEMETRY_KIND_UPLOAD), [{ i: 0 }])

  // 键必须带 telemetry: 前缀 —— 手侧 storage 里已经住着 infra 与 witness:*,撞了会互删。
  assert.deepEqual(Object.keys(store.state).sort(), ['telemetry:meta', 'telemetry:u:0'])

  // 填满第一片后再追加,不得重写那一片 —— 这是分片存在的全部理由。
  const rest = Array.from({ length: TELEMETRY_CHUNK - 1 }, (_, n) => ({ i: n + 1 }))
  await telemetryAppend(store, TELEMETRY_KIND_UPLOAD, rest, TELEMETRY_MAX_UPLOAD_CHUNKS)
  store.writes.length = 0
  await telemetryAppend(store, TELEMETRY_KIND_UPLOAD, [{ i: TELEMETRY_CHUNK }], TELEMETRY_MAX_UPLOAD_CHUNKS)
  const touched = Object.keys(store.writes.at(-1).items)
  assert.ok(!touched.includes('telemetry:u:0'), `重写了已满的片: ${touched.join(',')}`)
  assert.equal((await telemetryReadAll(store, TELEMETRY_KIND_UPLOAD)).length, TELEMETRY_CHUNK + 1)
})

test('观测分片环:超上限丢最旧整片,不搬数据', async () => {
  const store = memoryWitnessStorage()
  const many = Array.from({ length: TELEMETRY_CHUNK * 2 }, (_, n) => ({ i: n }))
  await telemetryAppend(store, TELEMETRY_KIND_UPLOAD, many, 2)

  // 两满片 + 新开的空片 = 3 片,超上限 2,最旧那片整片删除。
  assert.ok(!Object.hasOwn(store.state, 'telemetry:u:0'), '最旧的片应当被整片删除')
  const kept = await telemetryReadAll(store, TELEMETRY_KIND_UPLOAD)
  assert.equal(kept.length, TELEMETRY_CHUNK)
  assert.deepEqual(kept[0], { i: TELEMETRY_CHUNK }, '留下的应当是较新的那片')
})

test('观测分片环:空追加是空操作,两个环互不挤占', async () => {
  const store = memoryWitnessStorage()
  await telemetryAppend(store, TELEMETRY_KIND_UPLOAD, [], TELEMETRY_MAX_UPLOAD_CHUNKS)
  assert.deepEqual(store.state, {}, '空数组不该写任何键')
  assert.deepEqual(await telemetryReadAll(store, TELEMETRY_KIND_UPLOAD), [], '没有 meta 时读回空数组')

  // 轨迹单独一个环,存在的意义就是不被明细的页面加载噪声挤掉。
  await telemetryAppend(store, TELEMETRY_KIND_UPLOAD, [{ u: 1 }], TELEMETRY_MAX_UPLOAD_CHUNKS)
  await telemetryAppend(store, TELEMETRY_KIND_CLICK, [{ c: 1 }], TELEMETRY_MAX_CLICK_CHUNKS)
  assert.deepEqual(await telemetryReadAll(store, TELEMETRY_KIND_UPLOAD), [{ u: 1 }])
  assert.deepEqual(await telemetryReadAll(store, TELEMETRY_KIND_CLICK), [{ c: 1 }])
})


test('埋点捕获:请求体还原覆盖 formData 与 raw 两路,解不开也不丢', async () => {
  const bytes = (s) => new TextEncoder().encode(s).buffer

  // 表单编码时 Chrome 已经解过码,不能再 decodeURIComponent 一次。
  assert.equal(bodyText({ formData: { content: ['{"a":1}'] } }), '{"a":1}')
  // 字段名不是 content —— 整个交出去,别猜。
  assert.equal(bodyText({ formData: { other: ['x'] } }), '{"other":["x"]}')

  assert.equal(bodyText({ raw: [{ bytes: bytes('content=%7B%22a%22%3A1%7D') }] }), '{"a":1}')
  assert.equal(bodyText({ raw: [{ bytes: bytes('content=a+b') }] }), 'a b', '+ 是空格')
  assert.equal(bodyText({ raw: [{ bytes: bytes('{"plain":1}') }] }), '{"plain":1}', '没有 content= 外壳就是原文')
  // 非法 percent-encoding:剥了壳的原文照样交出去。
  assert.equal(bodyText({ raw: [{ bytes: bytes('content=%zz') }] }), '%zz')
  // 多段字节要拼起来。
  assert.equal(bodyText({ raw: [{ bytes: bytes('con') }, { bytes: bytes('tent=hi') }] }), 'hi')

  assert.equal(bodyText(null), null)
  assert.equal(bodyText(undefined), null)
  assert.equal(bodyText({}), null)

  assert.deepEqual(deepFind({ a: [{ p2: 1 }, { b: { p2: 2 } }], p2: 3 }, 'p2'), [1, 2, 3])
  assert.deepEqual(deepFind(null, 'p2'), [])
})

test('埋点捕获:解析成功存结构+摘要,失败存原文+错因,摘要抽取炸了也照样落盘', async () => {
  const store = memoryWitnessStorage()
  const digest = (payload) => ({ summary: { seen: [payload?.k] }, shots: [{ s: payload?.k }] })

  await recordUpload(store, digest, 111, 'https://x/t', '{"k":"v"}')
  let rows = await telemetryReadAll(store, TELEMETRY_KIND_UPLOAD)
  assert.equal(rows.length, 1)
  assert.deepEqual(rows[0].payload, { k: 'v' })
  assert.deepEqual(rows[0].summary, { seen: ['v'] })
  assert.equal(rows[0].raw, undefined, '解析成功不该同时存原文')
  assert.equal(rows[0].parseError, undefined)
  assert.deepEqual(await telemetryReadAll(store, TELEMETRY_KIND_CLICK), [{ s: 'v' }])

  // 形状不对正是最该看见的信息 —— 存原文、记错因,绝不静默丢。
  await recordUpload(store, digest, 222, 'https://x/t', 'not json')
  rows = await telemetryReadAll(store, TELEMETRY_KIND_UPLOAD)
  assert.equal(rows[1].raw, 'not json')
  assert.equal(rows[1].payload, undefined)
  assert.match(rows[1].parseError, /JSON 解析失败/)

  // 站点的抽取逻辑没跟上平台改版:载荷仍完整落盘,只是摘要为空 + 记一笔。
  const boom = () => { throw new Error('字段没了') }
  await recordUpload(store, boom, 333, 'https://x/t', '{"k":"still here"}')
  rows = await telemetryReadAll(store, TELEMETRY_KIND_UPLOAD)
  assert.deepEqual(rows[2].payload, { k: 'still here' }, '抽取失败不该连载荷一起丢')
  assert.match(rows[2].parseError, /摘要抽取失败.*字段没了/)
  assert.deepEqual(rows[2].summary, {})
})

test('BOSS 观测站点:抽 p2 与 cnTextCount,只有带轨迹的点击进轨迹环', async () => {
  assert.deepEqual(telemetrySites.map((s) => s.id), ['boss'])
  assert.deepEqual(bossTelemetrySite.urls, ['https://apm-fe.zhipin.com/wapi/zpApm/actionLog/*'])

  const digested = bossTelemetrySite.digest({
    items: [
      { action: 'web-event-click', p2: '0', p6: { x: [1, 2] }, p4: 'a', p: '{"time":999}' },
      { action: 'web-event-input', p2: '30004', p6: 'not-an-object', inner: { cnTextCount: 7 } },
      { action: 'device-action-report', p2: '800001', p6: 'x|y' },
      { action: 'web-event-click', p2: '0' },
    ],
  }, 500)

  assert.deepEqual(digested.summary.codes, ['0', '30004', '800001', '0'])
  assert.deepEqual(digested.summary.cnTextCount, [7], 'cnTextCount 只在载荷里,本地账本根本没有')

  // 只有 action=web-event-click 且 p6 是对象的那条带轨迹。
  assert.equal(digested.shots.length, 1)
  assert.deepEqual(digested.shots[0].p6, { x: [1, 2] })
  assert.equal(digested.shots[0].t, 999, '平台自己的时刻优先')
  assert.equal(digested.shots[0].at, 500)

  // p 缺失或解不开时退回捕获时刻,不为它丢一条轨迹。
  const noTime = bossTelemetrySite.digest({
    items: [{ action: 'web-event-click', p6: {}, p: 'not json' }],
  }, 700)
  assert.equal(noTime.shots[0].t, 700)

  // 形状不认识就返回空,不猜。
  assert.deepEqual(bossTelemetrySite.digest(null, 1), { summary: {}, shots: [] })
  assert.deepEqual(bossTelemetrySite.digest({ items: 'nope' }, 1).shots, [])
})


test('观测分片环:清空只动自己的键,另一个环与外人的键不受影响', async () => {
  const store = memoryWitnessStorage({ 'infra': { keep: 1 }, 'witness:meta': { keep: 2 } })
  await telemetryAppend(store, TELEMETRY_KIND_UPLOAD, [{ u: 1 }], TELEMETRY_MAX_UPLOAD_CHUNKS)
  await telemetryAppend(store, TELEMETRY_KIND_CLICK, [{ c: 1 }], TELEMETRY_MAX_CLICK_CHUNKS)

  await telemetryClear(store, TELEMETRY_KIND_UPLOAD)
  assert.deepEqual(await telemetryReadAll(store, TELEMETRY_KIND_UPLOAD), [])
  assert.deepEqual(await telemetryReadAll(store, TELEMETRY_KIND_CLICK), [{ c: 1 }], '另一个环不该被牵连')
  assert.deepEqual(store.state['infra'], { keep: 1 }, '手侧既有的键一个都不许动')
  assert.deepEqual(store.state['witness:meta'], { keep: 2 })

  await telemetryClear(store, TELEMETRY_KIND_UPLOAD)  // 幂等
  assert.deepEqual(await telemetryReadAll(store, TELEMETRY_KIND_UPLOAD), [])
})

test('BOSS 判读:指纹上报算例行,其余码算命中,全局名差集单独拎出来', async () => {
  const aegis = 'https://apm-fe.zhipin.com/wapi/zpApm/actionLog/fe/ie/common.json'

  const c = classifyBossEntry(aegis, {
    items: [
      { action: 'device-action-report', p2: '800001', p6: 'foo|bar' },
      { action: 'device-action-report', p2: '550003' },
      // 2026-09-02 Windows 真机导出:同一份指纹还会挂在心跳通道下重发,p6 是 UA-CH brands。
      { action: 'web-action-heartbeat', p2: '800001', p6: 'Not=A?Brand=99|Google Chrome=151' },
      { action: 'web-event-input', p2: '0' },
      { action: 'web-event-input', p2: '30099' },
      { action: 'web-event-click', p2: '' },
    ],
  })
  // 指纹上报每次页面加载无条件发,跟检测到什么无关 —— 算例行;心跳重发的那份也是。
  assert.deepEqual(c.routine.map((r) => r.code), ['800001', '800001', '0'])
  // 设备族里其它任何码才是探测命中。
  assert.deepEqual(c.hits.map((h) => h.code), ['550003', '30099'])
  // 全局名差集只从 device-action-report 的 p6 取——心跳版的 p6 是 brands 串,切开是假名字。
  assert.deepEqual(c.unknownGlobals, ['foo', 'bar'], '这一栏就是"我们隐不隐形"的答案')

  // patas APM 通道:码藏在 action 的 JSON 字符串字段里,p2 是页面 URL。
  const patas = 'https://apm-fe.zhipin.com/wapi/zpApm/actionLog/fe/common.json'
  const p = classifyBossEntry(patas, {
    items: [
      { action: 'action_js_risk_monitor', p7: JSON.stringify({ insertList: ['x.js'] }) },
      { action: 'action_js_risk_monitor', p7: 'not json' },
      { action: 'action_api_monitor', p4: JSON.stringify({ url: 'http://127.0.0.1:8931/a' }) },
      { action: 'action_api_monitor', p4: JSON.stringify({ url: 'https://example.com/a' }) },
    ],
  })
  assert.deepEqual(p.injected, ['x.js'])
  assert.deepEqual(p.localProbes, ['http://127.0.0.1:8931/a'], '只收本机端口')
  assert.deepEqual(p.hits, [], 'patas 通道的 p2 是页面 URL,不该被当成事件码')

  assert.match(bossCodeLabel('800001'), /^800001\(设备指纹/)
  assert.match(bossCodeMeaning('99999').label, /未知码/, '码表里没有的原样记下,不编')
  // 本机端口探测那一族的判据是"1 秒内有反应"而不是"连上了",端口关着瞬间拒绝
  // 照样算真 —— 面板必须把它们跟真信号分开,否则每台机器都是一片红。
  assert.equal(bossCodeMeaning('550239').nearUniversal, true)
  assert.equal(bossCodeMeaning('550094').nearUniversal, undefined)
  assert.deepEqual(classifyBossEntry(aegis, null).hits, [])

  // 行为层那一族绕开 isTrusted,是合成点击真正会踩的码 —— 面板不该把它们报成"新东西"。
  assert.equal(bossCodeMeaning('700051').known, true, '2026-09-03 本机命中过一次')
  for (const code of ['700028', '700053', '700061', '700071', '761005', '910015']) {
    assert.equal(bossCodeMeaning(code).known, true, `${code} 该在码表里`)
  }
  // 首页(C 端)那套 SDK 的码与我们无关:端口探测同样是"1 秒内有反应"的坏判据,折进噪音。
  assert.equal(bossCodeMeaning('410001').nearUniversal, true)
  // 470000 是 C 端每次点击的基线码,不是命中。
  const geek = classifyBossEntry(aegis, {
    items: [{ action: 'web-event-click-geek', p2: '470000' }, { action: 'web-event-click-geek', p2: '470001' }],
  })
  assert.deepEqual(geek.routine.map((r) => r.code), ['470000'])
  assert.deepEqual(geek.hits.map((h) => h.code), ['470001'], 'isTrusted 为假那条仍是命中')
  // 输入分类一族与打字/回车同源,不是命中。2026-09-09 本机真人手打一句带表情的话,
  // 30001(EMOJI)被面板报成「新东西」,补进例行。
  const inputKinds = classifyBossEntry(aegis, {
    items: [
      { action: 'web-event-input', p2: '30001' },
      { action: 'web-event-input', p2: '30002' },
      { action: 'web-event-input', p2: '30003' },
      { action: 'web-event-input', p2: '30004' },
    ],
  })
  assert.deepEqual(inputKinds.routine.map((r) => r.code), ['30001', '30002', '30003', '30004'])
  assert.deepEqual(inputKinds.hits, [])
  assert.match(bossCodeMeaning('30001').label, /EMOJI/)
})

test('BOSS 高风险码:补进码表不等于从结论区消失,「踩雷」那行只看 severe', () => {
  // 2026-09-03 的教训:700051 补进码表后从「新东西」那行掉下去,结论区三个绿勾,
  // 可它是平台抓到合成点击的实锤。第四行「踩雷」就是为它立的。
  assert.equal(bossCodeMeaning('700051').severe, true)
  assert.equal(bossCodeMeaning('700051').known, true, '已知且高风险,两个位互不遮盖')

  // 甲方 2026-09-03 裁定的名单:平台自己豁免不计聚合的这五条照样列高风险——
  // 我方 OS 注入的点击 isTrusted 为真,它们若亮就是 CDP 或调试路径漏出来了。
  for (const code of ['700009', '700001', '700005', '700007', '700013']) {
    assert.equal(bossCodeMeaning(code).severe, true, `${code} 该是高风险`)
  }
  for (const code of ['550003', '800015', '700061', '761005', '700019', '559999', '470001', '550247']) {
    assert.equal(bossCodeMeaning(code).severe, true, `${code} 该是高风险`)
  }
  // 明确不列:干净机器照发的聚合、第三方扩展、与「隐形」同源的差集、真人也会的首次点击、
  // 反映环境而非行为的两条。列进去每台机器都红,那行就没人看了。
  for (const code of ['559991', '550094', '99003', '700028', '910013', '910015', '800001', '0', '30004']) {
    assert.notEqual(bossCodeMeaning(code).severe, true, `${code} 不该是高风险`)
  }
  assert.equal(bossCodeMeaning('99999').severe, undefined, '码表里没有的归「新东西」,不归这里')

  // 不变量:坏判据噪音与高风险互斥,一个码不能既"每台机器都亮"又"一亮就被看穿"。
  for (const code of bossKnownCodes()) {
    const m = bossCodeMeaning(code)
    assert.ok(!(m.nearUniversal && m.severe), `${code} 同时标了 nearUniversal 与 severe`)
  }

  // 聚合:按码计数、多的在前、同数按码排;非高风险与未知码一律不进。
  const hits = [
    { code: '700051', action: 'web-event-click' },
    { code: '700009', action: 'web-event-click' },
    { code: '700009', action: 'web-event-click' },
    { code: '559991', action: 'device-action-report' },
    { code: '550239', action: 'device-action-report' },
    { code: '99999', action: 'device-action-report' },
  ]
  assert.deepEqual(bossSevereHits(hits).map((h) => [h.code, h.n]), [['700009', 2], ['700051', 1]])
  assert.match(bossSevereHits(hits)[0].label, /isTrusted/)
  assert.deepEqual(bossSevereHits([]), [])
  assert.deepEqual(bossSevereHits([{ code: '559991', action: 'x' }]), [], '干净机器照发的聚合不算踩雷')
})

// ---- 平台输入行为账本的解析 ----

test('输入账本解析:分出写盘时刻,非数字字段一律忽略而不当 0', () => {
  const snap = telemetryParseLedger({
    _ZP_CNT_: JSON.stringify({
      input_count: 3,
      composition_abnormal_count: 1,
      t: 1787985771986,
      // 平台哪天加了别的形态,不许静默算成「没异常」
      some_new_shape: { a: 1 },
      a_string: 'x',
    }),
    __local__sec__store___: '{"max":373}',
  }, 5000)
  assert.equal(snap.writtenAt, 1787985771986)
  assert.equal(snap.readAt, 5000)
  assert.deepEqual(snap.counts, { input_count: 3, composition_abnormal_count: 1 })
  assert.equal('some_new_shape' in snap.counts, false)
  assert.equal('a_string' in snap.counts, false)
  assert.equal(snap.extras.__local__sec__store___, '{"max":373}')
  assert.equal(snap.note, undefined)
})

test('输入账本解析:账本缺席、坏 JSON、不是对象,三种都说清原因且不谎报为零异常', () => {
  const gone = telemetryParseLedger({ _ZP_CNT_: null, __local__sec__store___: null }, 1)
  assert.match(gone.note ?? '', /还没有这个账本/)
  assert.deepEqual(gone.counts, {})

  const broken = telemetryParseLedger({ _ZP_CNT_: '{oops' }, 1)
  assert.match(broken.note ?? '', /不是 JSON/)

  const notObject = telemetryParseLedger({ _ZP_CNT_: '[1,2]' }, 1)
  assert.match(notObject.note ?? '', /不是对象/)
  // extras 的键即使源里没有也要出现,免得读侧分不清「没读到」和「没这个键」
  assert.equal('__local__sec__store___' in notObject.extras, true)
})

test('输入账本:会触发聚合上报的恰好是那五项,且 input_count 不算异常项', () => {
  const armed = BOSS_INPUT_COUNTERS.filter((c) => c.triggersReport).map((c) => c.key).sort()
  assert.deepEqual(armed, [
    'flimbot_abnormal_count',
    'input_rhythm_abnormal_count',
    'input_trait_abnormal_count',
    'keycode_abnormal_count',
    'keycode_abnormal_count_2',
  ])
  assert.equal(BOSS_INPUT_COUNTERS.length, 16)
  assert.equal(BOSS_REPORT_EVERY, 25)
  const total = BOSS_INPUT_COUNTERS.find((c) => c.key === 'input_count')
  assert.equal(total?.triggersReport, false)
})



// ——— BOSS 站点身份与适配器骨架(2026-08-30) ———

test('BOSS 站点身份:只认 www.zhipin.com 与沟通页,登录态恒 unknown 且如实声明自己读不出', () => {
  assert.equal(bossSite.id, 'boss')
  assert.ok(bossSite.matches('https://www.zhipin.com/web/chat/index'))
  assert.ok(!bossSite.matches('https://m.zhipin.com/web/chat/index'), '子域不算——匹配式和判据必须同一个口径')
  assert.ok(!bossSite.matches('http://www.zhipin.com/web/chat/index'), '非 https 不认')
  assert.ok(!bossSite.matches(undefined))

  assert.equal(bossSite.pageKind('https://www.zhipin.com/web/chat/index'), 'im')
  assert.equal(bossSite.pageKind('https://www.zhipin.com/web/chat/recommend'), 'recommend', '2026-09-04 真机:推荐页路径')
  // 其余路径(职位管理页、C 端页)按「平台枚举面事实门」一律 other,不猜。
  assert.equal(bossSite.pageKind('https://www.zhipin.com/web/chat/job/list'), 'other')
  assert.equal(bossSite.pageKind('https://www.zhipin.com/web/geek/recommend'), 'other')
  assert.equal(bossSite.pageKind('不是个 URL'), 'other')

  // **永不报 out**:掉登录形态从未观测过(2026-08-30 甲方裁决 BOSS 不做停机通道),
  // 用"读不到"去顶"已登出"会把页面没加载完说成账号掉了。
  assert.equal(bossSite.readLoginState(), 'unknown')
  assert.equal(bossSite.sensesLoginState, false)
  assert.equal(zhilianSite.sensesLoginState, true, '智联那条真通道不许被这次改动带塌')
})

test('全文档观察器只给能读登录态的站点装:BOSS 上一个都不装,那颗帧率的雷是构造掉的', async () => {
  await esbuild.build({
    entryPoints: ['src/base/content.ts'],
    bundle: true, format: 'esm', platform: 'neutral',
    outfile: 'test/dist/content-wiring.mjs', logLevel: 'error',
  })
  const url = pathToFileURL(process.cwd() + '/test/dist/content-wiring.mjs').href

  const originals = {
    chrome: globalThis.chrome, document: globalThis.document,
    window: globalThis.window, location: globalThis.location,
    MutationObserver: globalThis.MutationObserver,
  }
  async function loadOn(href) {
    const observed = []
    globalThis.chrome = {
      runtime: {
        sendMessage: async () => undefined,
        onMessage: { addListener() {} },
      },
    }
    globalThis.location = { href }
    globalThis.document = { documentElement: {}, scripts: [] }
    globalThis.window = { addEventListener() {} }
    globalThis.MutationObserver = class {
      constructor(callback) { this.callback = callback; observed.push('constructed') }
      observe() { observed.push('observe') }
      disconnect() {}
    }
    await import(`${url}?t=${observed.length}-${encodeURIComponent(href)}-${Math.random()}`)
    return observed
  }
  try {
    const zhilian = await loadOn('https://rd6.zhaopin.com/app/im')
    assert.deepEqual(zhilian, ['constructed', 'observe'],
      '智联仍要装:掉登录即时停机通道就架在这个观察器上')

    const boss = await loadOn('https://www.zhipin.com/web/chat/index')
    assert.deepEqual(boss, [],
      'BOSS 上不许装。onDOMMutation 的实质消费者只有登录态双读,而 BOSS 读不出登录态——' +
      '在一个会记 rAF 帧率的平台上白跑全子树观察器,代价是行为上的,金丝雀那种查可枚举痕迹的实验量不到')

    const stranger = await loadOn('https://example.invalid/whatever')
    assert.deepEqual(stranger, [], '不认识的站点上什么都不做')
  } finally {
    Object.assign(globalThis, originals)
  }
})

test('BOSS 适配器:MAIN world + os 通道,三条探针加场景一七条加场景二三条加场景三一条加第二刀七条加简历长图一条,其余显式拒绝', () => {
  assert.equal(bossAdapter.id, 'boss')
  assert.equal(bossAdapter.hostMatch, bossSite.match, '适配器与站点表必须是同一个"BOSS 是谁"')
  // MAIN 是 2026-08-28 取数通道裁决的直接后果:isolated world 拿不到 user$ 与消息数组。
  assert.equal(bossAdapter.world, 'MAIN')
  // BOSS 查 isTrusted,页面内合成事件在这里不成立——这正是整条 OS 注入链存在的理由。
  assert.equal(bossAdapter.input, 'os')
  assert.equal(bossAdapter.envReportGuard, undefined,
    '拦不拦 BOSS 的埋点尚未裁决,按事实门不得凭空声明一个守卫')

  const declared = Object.keys(bossAdapter)
    .filter((key) => typeof bossAdapter[key] === 'function')
    .sort()
  // **这张名单只在过了出口之后才准变长。** 三条探针(2026-08-28/08-30/09-01 各自出口)之后,
  // 2026-09-03 甲方批准场景一出口,加了七条会话原语——恰好是「仅回复一轮闭环」要的那七条。
  // 2026-09-04 甲方批准场景二出口,加了换微信线三条(sendWechatInvite/acceptWechat/readWechatExchangeOutcome);
  // 同日批准场景三出口,加了邀面卡一条(sendInviteCard)。2026-09-04 夜甲方批准第二刀出口(采集 + 打招呼),
  // 加了七条:职位管理页一条、采集四条、招呼两条(readPublishedJobs / selectSourcingPosition / applySourcingFilters /
  // readSourcingWindow / readSourcingTargetResume / sendGreeting / readGreetingOutcome)。
  assert.deepEqual(declared, [
    'acceptWechat', 'applySourcingFilters', 'captureResumeScreenshot', 'captureThreadScreenshot', 'ensureSurface', 'identifyCurrentConversation', 'openConversation',
    'osClick', 'osProbe', 'osScroll', 'osType', 'probePlatform',
    'readGreetingOutcome', 'readList', 'readPublishedJobs', 'readResume', 'readSourcingTargetResume', 'readSourcingWindow',
    'readThread', 'readUnreadTotal', 'readWechatExchangeOutcome',
    'selectSourcingPosition', 'sendGreeting', 'sendInviteCard', 'sendMessage', 'sendWechatInvite',
  ], '适配器能力变了。这张名单每加一条都要先过出口(readResume:2026-09-03 甲方选 B;osScroll/osClick:2026-09-03 探针出口;换微信三条:2026-09-04 场景二出口;邀面卡:2026-09-04 场景三出口;第二刀七条:2026-09-04 夜出口;ensureSurface:2026-09-07 首趟真机后甲方批;captureResumeScreenshot:2026-09-09 甲方「顺便把简历截图也做了」)')

  // 未声明的能力必须在运行期显式拒绝(反模式 18),不得默认回成功。
  assert.throws(() => requireCapability(bossAdapter, 'readSourcingResume'), /未实现原语能力/, '一次读一位的旧采集原语,BOSS 走窗口 + 目标两条,不实现它')
  assert.throws(() => requireCapability(bossAdapter, 'readWechatSetting'), /未实现原语能力/, '开工闸按无能力跳过(脑侧 capabilityMissing)')
})

test('hello 平台能力表:智联表等于并集减 BOSS 专属三条,BOSS 表恰为平台无关四条加探针五条加场景一七条加场景二三条加场景三一条加第二刀七条加简历长图一条', async () => {
  // 原语的 capability 字段是与 handler 内 callPlatform 字面量并行的第二份声明;
  // 这两条断言把它钉住:漏填一条,BOSS 表会多出一条(第二条红);填错名字,
  // 智联表会少一条(第一条红)。
  registerDebugPrimitives()
  registerM2Primitives()
  registerM3Primitives()
  registerM4Primitives()
  registerM5Primitives()
  registerM6Primitives()
  registerM7Primitives()
  registerJobPublishPrimitives()
  registerAccountPrimitives()
  await withPlatforms([zhilianAdapter, bossAdapter], async () => {
    const tables = capabilitiesByPlatform()
    assert.deepEqual(tables.map((t) => t.id), ['zhilian', 'boss'])
    const union = capabilities()
    assert.equal(union.length, 44, '契约原语全集应为 44 条')
    // 智联少的是 debug.osType@1、debug.osScroll@1、debug.osClick@1:键盘线、滚轮线与考古点击
    // 只在 BOSS 上有靶子(智联走页面内输入与程序化滚动,考古点击后置)。除此之外一条不少——
    // 少一条就是某原语的 capability 名填错了。
    const bossOnly = ['debug.osType@1', 'debug.osScroll@1', 'debug.osClick@1']
    assert.deepEqual(tables[0].caps, union.filter((c) => !bossOnly.includes(c)),
      '智联表应等于并集减 BOSS 专属三条')
    assert.deepEqual(tables[1].caps, [
      'candidate.applySourcingFilters@1', 'candidate.captureResumeScreenshot@1', 'candidate.readResume@1', 'candidate.readSourcingTargetResume@1',
      'candidate.readSourcingWindow@1', 'candidate.selectSourcingPosition@1',
      'chat.acceptWechat@1', 'chat.captureThreadScreenshot@1', 'chat.identifyCurrentConversation@1', 'chat.openConversation@1',
      'chat.readGreetingOutcome@1', 'chat.readList@1', 'chat.readThread@1', 'chat.readUnreadTotal@1', 'chat.readWechatExchangeOutcome@1',
      'chat.sendGreeting@1', 'chat.sendInviteCard@1', 'chat.sendMessage@1', 'chat.sendWechatInvite@1',
      'debug.osClick@1', 'debug.osProbe@1', 'debug.osScroll@1', 'debug.osType@1', 'debug.ping@1', 'debug.reload@1',
      'debug.slowEcho@1', 'debug.switchWindow@1', 'job.readPublishedList@1', 'nav.ensureSurface@1', 'probe.platform@1',
    ], 'BOSS 表变了:要么适配器长了能力(先过出口),要么某条原语漏填 capability')
    for (const capability of tables[1].caps) {
      assert.ok(union.includes(capability), `BOSS 表 ⊆ 并集:${capability}`)
    }
    assert.equal(hasCapability(bossAdapter, 'osType'), true)
    assert.equal(hasCapability(bossAdapter, 'sendMessage'), true)
    assert.equal(hasCapability(bossAdapter, 'sendGreeting'), true, '第二刀 2026-09-04 夜过出口,招呼线进表')
    assert.equal(hasCapability(bossAdapter, 'readSourcingResume'), false,
      '一次读一位的旧采集原语 BOSS 不声明——脑按表不派,手也不该声明')
  })
})



// ---------------------------------------------------------------------------
// 场景一(2026-09-03):七条会话原语的纯函数与页面函数。页面函数用假 document 跑,
// 判据全是形状(uid/friendSource/newMsgCount/encryptUid、conversation$、list$+isToTop),
// 不认组件名;夹具里没有任何真实候选人。

test('标签页导航代数:只认主框架 commit,关闭即清,SPA 内路由推进不算', () => {
  resetTabGenerationsForTest()
  const listeners = {}
  const saved = globalThis.chrome
  globalThis.chrome = {
    webNavigation: { onCommitted: { addListener(fn) { listeners.committed = fn } } },
    tabs: { onRemoved: { addListener(fn) { listeners.removed = fn } } },
  }
  try {
    registerTabGenerationTracking()
    assert.equal(tabNavigationGeneration(7), 0, '没见过的标签页是 0')
    listeners.committed({ tabId: 7, frameId: 0, url: 'https://www.zhipin.com/web/chat/index' })
    assert.equal(tabNavigationGeneration(7), 1)
    listeners.committed({ tabId: 7, frameId: 3, url: 'https://ad.example/iframe' })
    assert.equal(tabNavigationGeneration(7), 1, '子框架导航与"页面换了"无关')
    listeners.committed({ tabId: 7, frameId: 0, url: 'https://www.zhipin.com/web/user/?ka=bticket' })
    assert.equal(tabNavigationGeneration(7), 2, '登出跳登录页是主框架导航,代数必须变')
    assert.equal(tabNavigationGeneration(8), 0, '按标签页各算各的')
    listeners.removed(7)
    assert.equal(tabNavigationGeneration(7), 0)
    noteMainFrameNavigation(9); forgetTab(9)
    assert.equal(tabNavigationGeneration(9), 0)
  } finally {
    globalThis.chrome = saved
    resetTabGenerationsForTest()
  }
})

test('身份复核缓存:指纹同、代数同、没超期三者齐才能顶掉一次 MAIN 读', () => {
  const now = 1_700_000_000_000
  const cached = { fingerprint: 'f'.repeat(64), generation: 3, verifiedAt: now - 60_000 }
  assert.equal(identityCacheUsable(cached, 'f'.repeat(64), 3, now), true)
  assert.equal(identityCacheUsable(undefined, 'f'.repeat(64), 3, now), false, '没缓存就读')
  assert.equal(identityCacheUsable(cached, 'e'.repeat(64), 3, now), false, '脑要求的指纹变了就读')
  assert.equal(identityCacheUsable(cached, 'f'.repeat(64), 4, now), false, '标签页导航过就读——换账号必经导航')
  assert.equal(identityCacheUsable(cached, 'f'.repeat(64), 3, now + 30 * 60_000), false, '30 分钟到期就读')
  assert.equal(identityCacheUsable(cached, 'f'.repeat(64), 3, now - 120_000), false, '时钟倒退按失效处理')
  resetBossIdentityCacheForTest()
})

test('BOSS 会话引用:uid-friendSource,两层都有;不是这个形状的一律不认', () => {
  const { bossConversationRef, parseBossConversationRef } = bossTestHooks
  assert.equal(bossConversationRef(650166511, 0), '650166511-0')
  assert.deepEqual(parseBossConversationRef('650166511-0'), { uid: 650166511, friendSource: 0 })
  for (const bad of ['', '0-0', 'abc', '650166511', '650166511-', '-1', '650166511-0-1', 'session-42']) {
    assert.equal(parseBossConversationRef(bad), null, `不该认:${bad}`)
  }
})

test('BOSS 侧栏角标文本:空是 0、数字照读、99+ 向多算', () => {
  const parse = bossTestHooks.parseBossUnreadBadgeText
  assert.equal(parse(''), 0)
  assert.equal(parse(' 24 '), 24)
  assert.equal(parse('99+'), 99)
  assert.equal(parse('新'), 1, '认不出的非空文本按 1:不能把满格未读读成零')
})

test('BOSS 消息投影:只实现真机已见的 bizType,未见值归并 system 并带出原始类型', async () => {
  const { projectBossMessage } = bossTestHooks
  const base = { mid: '4123', direction: 'in', type: 'text', bizType: 101, bodyType: 1, status: 1, time: 1788402198000, text: ' 你好   世界 ', interviewCondition: null, actionAid: null, templateId: 1, dialogOperated: null, dialogAids: [] }
  const text = projectBossMessage(base)
  assert.deepEqual([text.kind, text.direction, text.text, text.hashInput], ['text', 'in', '你好 世界', '你好 世界'])
  const noBiz = projectBossMessage({ ...base, bizType: null })
  assert.equal(noBiz.kind, 'text', '早期普通文本没有 bizType 字段(平台事实 §二)')
  const wx = projectBossMessage({ ...base, bizType: 12, direction: 'in', text: '某人的微信号:abc' })
  assert.equal(wx.kind, 'text', 'bizType 12 的普通文本(templateId 1)仍是文本')
  // 换微信线(平台事实 §十四):微信号消息 templateId=5 投 accepted 卡且正文不带号码;对方请求 dialog 投 pending,答过仍 pending。
  const wxNumber = projectBossMessage({ ...base, bizType: 12, bodyType: 1, templateId: 5, direction: 'in', text: '某人的微信号:&lt;copy&gt;abc123&lt;/copy&gt;' })
  assert.deepEqual([wxNumber.kind, wxNumber.cardType, wxNumber.cardState, wxNumber.text, wxNumber.hashInput],
    ['card', 'wechatExchange', 'accepted', '[微信交换成功]', 'card\x1fwechatExchange'], '微信号不得进 text')
  const wxAsk = projectBossMessage({ ...base, bizType: 12, bodyType: 7, type: 'dialog', direction: 'in', text: '我想要和您交换微信，您是否同意', dialogOperated: false, dialogAids: [33, 34] })
  assert.deepEqual([wxAsk.kind, wxAsk.cardType, wxAsk.cardState, wxAsk.text, wxAsk.hashInput],
    ['card', 'wechatExchange', 'pending', '[交换微信请求]', 'card\x1fwechatExchange'])
  const wxAsked = projectBossMessage({ ...base, bizType: 12, bodyType: 7, type: 'dialog', direction: 'in', text: '我想要和您交换微信，您是否同意', dialogOperated: true, dialogAids: [33, 34] })
  assert.equal(wxAsked.cardState, 'pending', '答过的请求卡仍投 pending(出口 §四 第 2 条:完成态由微信号消息表达,Exchanged 只触发一次)')
  const otherAsk = projectBossMessage({ ...base, bizType: 12, bodyType: 7, type: 'dialog', direction: 'in', text: '别的请求', dialogOperated: false, dialogAids: [41, 42] })
  assert.equal(otherAsk.kind, 'text', '不带 aid 33 的 dialog 不是换微信请求,按既有文本路径走')
  const recalled = projectBossMessage({ ...base, status: 3, text: '' })
  assert.deepEqual([recalled.kind, recalled.text], ['system', '[消息已撤回]'])
  // 邀面卡三码按 bizType 分支(出口 §2.1,2026-09-04 真机两时机复核):我方发出投 unknown(意图行写 unknown,投 pending 会被
  // 当成 unknown→pending 跃迁转人工);接受投 accepted;取消不论方向都投 system(出站会被读成又发一张,入站会判 unknownEvent)。
  for (const [bizType, condition, state, direction, noisy] of [
    [21130009, 1, 'unknown', 'out', false], [21130008, 3, 'accepted', 'in', false],
    [21130009, 4, 'unknown', 'out', true], [21130008, 2, 'unknown', 'in', true],
  ]) {
    const card = projectBossMessage({ ...base, bizType, bodyType: 14, direction, text: '发送了面试邀请', interviewCondition: condition })
    assert.deepEqual([card.kind, card.cardType, card.cardState, card.direction], ['card', 'interviewInvite', state, direction], `bizType=${bizType}`)
    assert.equal(card.hashInput, 'card\x1finterviewInvite', '契约包 1.2(2026-09-04):BOSS 卡无参数,投常量配方,状态分离在 cardState')
    assert.equal(card.unrecognized !== undefined, noisy, `bizType=${bizType} condition=${condition}:未见 condition 只进日志`)
  }
  for (const direction of ['out', 'in']) {
    const cancel = projectBossMessage({ ...base, bizType: 21130006, bodyType: 14, direction, text: '取消了面试', interviewCondition: 5 })
    assert.deepEqual([cancel.kind, cancel.cardType, cancel.direction, cancel.text, cancel.unrecognized], ['system', undefined, direction, '取消了面试', undefined],
      `取消行(${direction})投 system 不投 card:脑侧对出站 interviewInvite 任何状态都读成又发一张,对入站非 accepted 判 unknownEvent`)
  }
  const cancelEmpty = projectBossMessage({ ...base, bizType: 21130006, bodyType: 14, direction: 'in', text: '', interviewCondition: 5 })
  assert.equal(cancelEmpty.text, '[系统消息:21130006]')
  const attachment = projectBossMessage({ ...base, bizType: 21050008, bodyType: 12, type: 'hyperLink', direction: 'in', text: '简历.pdf' })
  assert.deepEqual([attachment.kind, attachment.unrecognized], ['system', undefined], '接受面试时自动发的附件简历已见,归 system 不再报 unrecognized')
  const wxReq = projectBossMessage({ ...base, bizType: 21050024, bodyType: 4, type: 'action', direction: 'out', text: '请求交换微信已发送', actionAid: 32 })
  assert.deepEqual([wxReq.kind, wxReq.cardType, wxReq.cardState, wxReq.hashInput], ['card', 'wechatExchange', 'pending', 'card\x1fwechatExchange'])
  const resumeReq = projectBossMessage({ ...base, bizType: 14, bodyType: 7, text: '对方请求发送附件简历' })
  assert.deepEqual([resumeReq.kind, resumeReq.cardType, resumeReq.unrecognized], ['system', undefined, undefined],
    '「请求发送附件简历」是候选人的请求对话框,不是契约的"已投递简历",先归 system 观测')
  const jobCard = projectBossMessage({ ...base, bizType: 21050004, bodyType: 9, text: '9月3日 沟通的职位-销售经理' })
  assert.deepEqual([jobCard.kind, jobCard.cardType, jobCard.text, jobCard.unrecognized], ['system', undefined, '9月3日 沟通的职位-销售经理', undefined],
    '会话开头的职位卡投成 card/other 会让脑把每个候选人都判成 unknownPlatformEvent 转人工(2026-09-03 Mac 四跑实证)')
  const tip = projectBossMessage({ ...base, bizType: 21050060, bodyType: 12, direction: 'system', text: '平台提示' })
  assert.deepEqual([tip.kind, tip.unrecognized], ['system', undefined])
  const empty = projectBossMessage({ ...base, bizType: 21130010, bodyType: 4, direction: 'system', text: '' })
  assert.deepEqual([empty.kind, empty.text], ['system', '[系统消息:21130010]'])
  const unseen = projectBossMessage({ ...base, bizType: 99999999, bodyType: 1, text: '像文本但类型没见过' })
  assert.equal(unseen.kind, 'system', '枚举面事实门:未见值不得实现成文本')
  assert.match(unseen.unrecognized, /bizType=99999999/)
})

test('BOSS 换微信线纯函数:待答请求筛选与结果行选取(带锚恰一条、无锚取最新、零/多都不猜)', () => {
  const { pendingBossWechatRequests, selectBossExchangeResult } = bossTestHooks
  const row = (mid, over) => ({ mid, direction: 'in', type: 'text', bizType: 12, bodyType: 1, status: 2, time: null, text: '', interviewCondition: null, actionAid: null, templateId: 1, dialogOperated: null, dialogAids: [], ...over })
  const ask = (mid, operated) => row(mid, { bodyType: 7, type: 'dialog', dialogOperated: operated, dialogAids: [33, 34] })
  const num = (mid) => row(mid, { templateId: 5 })
  const text = (mid) => row(mid, { bizType: 101 })
  assert.deepEqual(pendingBossWechatRequests([text('1'), ask('2', false), ask('3', true), num('4')]).map((r) => r.mid), ['2'])
  assert.deepEqual(pendingBossWechatRequests([ask('2', false), { ...ask('5', false), direction: 'out' }]).map((r) => r.mid), ['2'], '出站 dialog 不算对方请求')
  // 带锚:锚后、下一条请求卡前恰一条
  assert.equal(selectBossExchangeResult([ask('10', true), text('11'), num('12')], '10').row.mid, '12')
  assert.equal(selectBossExchangeResult([ask('10', true), text('11')], '10').status, 'none')
  assert.equal(selectBossExchangeResult([ask('10', true), num('11'), num('12')], '10').status, 'many')
  assert.equal(selectBossExchangeResult([ask('10', true), ask('11', false), num('12')], '10').status, 'none', '下一条请求卡之后的结果不归前一个锚')
  assert.equal(selectBossExchangeResult([num('12')], '10').status, 'anchor_missing')
  assert.equal(selectBossExchangeResult([text('10'), num('12')], '10').status, 'anchor_missing', '锚不是请求卡也算缺失')
  // 无锚:最新一条
  assert.equal(selectBossExchangeResult([num('3'), text('4'), num('5')], null).row.mid, '5')
  assert.equal(selectBossExchangeResult([text('4')], null).status, 'none')
  assert.equal(selectBossExchangeResult([num('9'), num('3')], null).row.mid, '9', '按 mid 数值排序取最新,不按数组顺序')
})

test('BOSS 接受前最后一道闸:只认换微信请求卡里的「同意」,附件简历请求卡的同意键不算;落点、可用、卡文案、选中行四者缺一不点', () => {
  const { domReadBossAcceptButton, domAcceptGate } = bossTestHooks
  const mkBtn = (text, cardText, disabled = false, w = 111) => {
    const card = { textContent: `${cardText} 拒绝 ${text}` }
    const btn = {
      tagName: 'SPAN', textContent: text,
      classList: { contains(c) { return c === 'disabled' && disabled } },
      getBoundingClientRect() { return { x: 700, y: 500, width: w, height: 34, left: 700, top: 500, right: 700 + w, bottom: 534 } },
      closest(sel) { return sel === '.message-item' ? card : null },
      contains(node) { return node === btn },
    }
    return btn
  }
  const resumeAgree = mkBtn('同意', '对方请求发送附件简历')
  const wxAgree = mkBtn('同意', '我想要和您交换微信，您是否同意')
  const wxRefuse = mkBtn('拒绝', '我想要和您交换微信，您是否同意')
  const rows = [
    { getAttribute() { return '11-0' }, classList: { contains(c) { return c === 'selected' } } },
    { getAttribute() { return '12-0' }, classList: { contains() { return false } } },
  ]
  const saved = globalThis.document
  const savedWindow = globalThis.window
  const install = (buttons, overrides = {}) => {
    globalThis.document = {
      querySelectorAll(selector) { return selector === '.message-card-buttons .card-btn' ? buttons : selector === '.geek-item' ? (overrides.rows ?? rows) : [] },
      elementFromPoint() { return overrides.at === undefined ? wxAgree : overrides.at },
    }
    globalThis.window = { innerWidth: 1470, innerHeight: 800 }
  }
  try {
    install([resumeAgree, wxRefuse, wxAgree])
    const read = domReadBossAcceptButton('.message-card-buttons .card-btn', '.message-item', '交换微信')
    assert.deepEqual([read.found, read.count, read.index, read.clipOk], [true, 1, 2, true], '简历请求卡的同意键不算,换微信卡的才算')
    install([resumeAgree, wxRefuse, mkBtn('同意', '我想要和您交换微信，您是否同意', true)])
    assert.deepEqual(domReadBossAcceptButton('.message-card-buttons .card-btn', '.message-item', '交换微信').count, 0, '已 disabled 的不算')
    install([resumeAgree, wxAgree, mkBtn('同意', '交换微信(第二张)')])
    assert.equal(domReadBossAcceptButton('.message-card-buttons .card-btn', '.message-item', '交换微信').found, false, '两张换微信卡都可用时不猜')

    const gate = (buttons, index, overrides) => {
      install(buttons, overrides)
      return domAcceptGate('.message-card-buttons .card-btn', index, 1, 1, '.message-item', '交换微信', '.geek-item', '11-0', 'selected')
    }
    assert.equal(gate([resumeAgree, wxRefuse, wxAgree], 2).onTarget, true)
    assert.match(gate([resumeAgree, wxRefuse, wxAgree], 0, { at: resumeAgree }).found, /不是换微信请求卡/, '落到简历请求卡的同意键上不点')
    assert.match(gate([resumeAgree, wxRefuse, wxAgree], 2, { at: wxRefuse }).found, /别的元素/, '落点偏到拒绝键上不点')
    assert.match(gate([resumeAgree, wxRefuse, wxAgree], 1).found, /文案已变/, 'index 指到拒绝键不点')
    assert.match(gate([resumeAgree, wxRefuse, wxAgree], 2, { rows: [rows[1]] }).found, /选中行不是目标会话/, '真人切走会话后不点')
    assert.match(gate([resumeAgree, wxRefuse], 2).found, /不在原来的位置/, '卡片重排后 index 落空不点')
  } finally {
    globalThis.document = saved
    globalThis.window = savedWindow
  }
})

test('BOSS 邀面参数换算:按本机时区拆日期与起止,一律宽松时间;两种形态结束=开始+1h 由手填;不合格一律 invalid 不取整', () => {
  const { planBossInterviewForm } = bossTestHooks
  const now = new Date(2026, 8, 4, 12, 0).getTime()
  const at = (d, h, m = 0, sec = 0) => new Date(2026, 8, d, h, m, sec).getTime()
  const onsite = planBossInterviewForm({ method: 'onsite', startsAt: at(5, 10) }, now)
  assert.equal(onsite.status, 'ok')
  assert.deepEqual(
    [onsite.plan.method, onsite.plan.radioText, onsite.plan.meetingText, onsite.plan.meetingCode, onsite.plan.date, onsite.plan.year, onsite.plan.month, onsite.plan.day, onsite.plan.isToday,
      onsite.plan.startText, onsite.plan.endText, onsite.plan.firstEndText, onsite.plan.timeValue, onsite.plan.endsAtMs],
    ['onsite', '线下面试', null, null, '2026-09-05', 2026, 9, 5, false, '10:00', '11:00', '11:00', '10:00-11:00', at(5, 11)],
    '线下:表单结束=开始+1 小时,随 data.interview 回脑')
  // 命令只带开始与方式(2026-09-08 甲方裁决):线上同样由手填开始+1 小时——脑侧此前写死的
  // 30 分钟在本表单上无法表达,2026-09-08 真机首张线上卡就被拒在这里。
  const video = planBossInterviewForm({ method: 'wechatVideo', startsAt: at(4, 14, 30) }, now)
  assert.equal(video.status, 'ok')
  assert.deepEqual([video.plan.radioText, video.plan.meetingText, video.plan.meetingCode, video.plan.date, video.plan.isToday, video.plan.timeValue, video.plan.firstEndText, video.plan.endsAtMs],
    ['线上面试', '微信视频', '8', '2026-09-04', true, '14:30-15:30', '15:30', at(4, 15, 30)])
  const edge = planBossInterviewForm({ method: 'wechatVideo', startsAt: at(5, 20) }, now)
  assert.deepEqual([edge.status, edge.plan.timeValue], ['ok', '20:00-21:00'], '平台上下限 08:00 与 21:00 是闭区间')
  const nextMonth = planBossInterviewForm({ method: 'onsite', startsAt: new Date(2026, 9, 3, 9).getTime() }, now)
  assert.deepEqual([nextMonth.status, nextMonth.plan.year, nextMonth.plan.month, nextMonth.plan.day], ['ok', 2026, 10, 3], '下月:日历翻一页')
  const jan = planBossInterviewForm({ method: 'onsite', startsAt: new Date(2027, 0, 5, 9).getTime() }, new Date(2026, 11, 20, 12).getTime())
  assert.deepEqual([jan.status, jan.plan.year, jan.plan.month], ['ok', 2027, 1], '跨年也是翻一页')
  const bad = (interview, when = now) => {
    const r = planBossInterviewForm(interview, when)
    assert.equal(r.status, 'invalid', JSON.stringify(interview))
    return r.detail
  }
  assert.match(bad({ method: 'onsite', startsAt: at(5, 10, 15) }), /30 分钟格/)
  assert.match(bad({ method: 'onsite', startsAt: at(5, 10, 0, 20) }), /30 分钟格/, '带秒也不取整')
  assert.match(bad({ method: 'onsite', startsAt: at(5, 7, 30) }), /08:00–20:00/)
  assert.match(bad({ method: 'onsite', startsAt: at(5, 20, 30) }), /08:00–20:00/)
  assert.match(bad({ method: 'onsite', startsAt: at(4, 11) }), /已过/, '开始时间不在未来')
  assert.match(bad({ method: 'onsite', startsAt: new Date(2026, 10, 5, 9).getTime() }), /只翻一页/)
  assert.match(bad({ method: 'phone', startsAt: at(5, 10) }), /不在本平台开放范围/)
  assert.match(bad({ method: 'onsite', startsAt: 'x' }), /毫秒时间戳/)
})

test('BOSS 日历:月份头「2026年 九月」解析;目标格按数字挑,今天那格按 today 类挑(文本是「今」);零或多格都不猜', () => {
  const { parseBossCalendarMonth, pickBossCalendarCell } = bossTestHooks
  assert.deepEqual(parseBossCalendarMonth('2026年 九月'), { year: 2026, month: 9 })
  assert.deepEqual(parseBossCalendarMonth('2026年十月'), { year: 2026, month: 10 })
  assert.deepEqual(parseBossCalendarMonth('2027年 十一月'), { year: 2027, month: 11 })
  assert.deepEqual(parseBossCalendarMonth(' 2026年 十二月 '), { year: 2026, month: 12 })
  assert.equal(parseBossCalendarMonth('九月'), null)
  assert.equal(parseBossCalendarMonth('2026年 9月'), null, '阿拉伯数字月份没见过,不猜')
  const rect = { x: 0, y: 0, w: 32, h: 32 }
  const cell = (index, text, over = {}) => ({ index, text, disabled: false, today: false, blank: false, rect, clip: rect, ...over })
  const cells = [cell(0, '', { blank: true }), cell(1, '1', { disabled: true }), cell(2, '2', { disabled: true }), cell(3, '3', { disabled: true }),
    cell(4, '今', { today: true }), cell(5, '5'), cell(6, '6'), cell(7, '7')]
  assert.equal(pickBossCalendarCell(cells, 5, false).cell.index, 5)
  assert.equal(pickBossCalendarCell(cells, 4, true).cell.index, 4, '今天那格按 today 类找,不按数字')
  assert.deepEqual(pickBossCalendarCell(cells, 4, false), { cell: null, count: 0 }, '说目标不是今天却要 4 日:格文本是「今」,数字 4 不存在,不猜')
  assert.deepEqual(pickBossCalendarCell(cells, 2, false), { cell: null, count: 0 }, '过去的日子 disabled 不选')
  assert.equal(pickBossCalendarCell([...cells, cell(8, '5')], 5, false).count, 2, '两格同文不猜')
})

test('BOSS 时间列:项在可见区就点可见部分(半截露 20px 即可),不在就瞄合格区近侧边界+40px 滚;过头修正量 <100px 只走一格;到边无处可滚如实说', () => {
  const { planBossTimeItemReach } = bossTestHooks
  const list = { rect: { x: 100, y: 300, w: 107, h: 196 }, scrollTop: 0, scrollHeight: 1100, clientHeight: 196 }
  const item = (i, scrollTop = 0) => ({ rect: { x: 100, y: 300 + i * 44 - scrollTop, w: 107, h: 44 } })
  assert.deepEqual(planBossTimeItemReach(list, item(0), 20), { status: 'visible', rect: { x: 100, y: 300, w: 107, h: 44 } })
  assert.deepEqual(planBossTimeItemReach(list, item(4), 20), { status: 'visible', rect: { x: 100, y: 476, w: 107, h: 20 } }, '第 5 项露 20px,点可见部分')
  // 第 11 项(itemTop 440):合格区 [264,464],瞄 264+40。
  assert.deepEqual(planBossTimeItemReach(list, item(10), 20), { status: 'scroll', direction: 'down', distancePx: 304 }, '瞄近侧边界+40,不瞄正中')
  assert.deepEqual(planBossTimeItemReach({ ...list, scrollTop: 500 }, item(10, 500), 20), { status: 'scroll', direction: 'up', distancePx: 76 }, '过头 36px:修正量 <100 只走一格,不振荡')
  assert.deepEqual(planBossTimeItemReach({ ...list, scrollTop: 480 }, item(10, 480), 20), { status: 'scroll', direction: 'up', distancePx: 56 },
    '瞄正中时的经典振荡起点(480):现在只回 56px,一格 120 落到 360 仍在合格区')
  assert.equal(planBossTimeItemReach({ ...list, scrollTop: 360 }, item(10, 360), 20).status, 'visible')
  assert.equal(planBossTimeItemReach({ ...list, scrollTop: 904 }, item(24, 904), 20).status, 'visible', '到底后最后一项在可见区')
  assert.deepEqual(planBossTimeItemReach(list, item(24), 20), { status: 'scroll', direction: 'down', distancePx: 904 }, '想瞄 920 只能到 904:按 maxTop 夹')
  assert.equal(planBossTimeItemReach({ ...list, scrollHeight: 196 }, { rect: { x: 100, y: 900, w: 107, h: 44 } }, 20).status, 'unreachable')
  assert.match(planBossTimeItemReach(list, { rect: { x: 900, y: 300, w: 107, h: 44 } }, 20).detail, /横向不相交/, '纵向已在合格区却不可见:滚动解决不了,不瞎滚')
  // Mac 120px/格全程模拟:runOsScroll 每次调用起手按 100px/格估 ceil(d/100) 格(≤3 格时恰取该数,4~8 格随机取 3~cap),
  // 对 25 个开始项各从 scrollTop=0 出发,最坏情况(每簇取 cap 格)也在 3 次内到可见区。
  const NOTCH = 120
  for (let i = 0; i < 25; i += 1) {
    let scrollTop = 0
    let attempts = 0
    let reach = planBossTimeItemReach({ ...list, scrollTop }, item(i, scrollTop), 20)
    while (reach.status === 'scroll') {
      attempts += 1
      assert.ok(attempts <= 3, `第 ${i} 项 ${attempts} 次仍未到:scrollTop=${scrollTop}`)
      let remaining = reach.distancePx
      let pxPerNotch = 100
      let moved = 0
      while (remaining > 0) {
        const ticks = Math.min(8, Math.max(1, Math.ceil(remaining / pxPerNotch)))
        const step = ticks * NOTCH * (reach.direction === 'down' ? 1 : -1)
        const next = Math.min(904, Math.max(0, scrollTop + step))
        moved += Math.abs(next - scrollTop)
        if (next === scrollTop) break
        scrollTop = next
        pxPerNotch = NOTCH
        remaining = reach.distancePx - moved
      }
      reach = planBossTimeItemReach({ ...list, scrollTop }, item(i, scrollTop), 20)
    }
    assert.equal(reach.status, 'visible', `第 ${i} 项最终应可见(scrollTop=${scrollTop})`)
  }
})

test('BOSS 邀面发送前复核:模态/类型/平台/日期/时间/发送键六项逐字对,缺一列出;线下不看平台', () => {
  const { bossInterviewFormMismatch } = bossTestHooks
  const plan = { radioText: '线上面试', meetingText: '微信视频', meetingCode: '8', date: '2026-09-05', timeValue: '10:00-11:00' }
  const ok = {
    modal: 1, radios: [{ text: '线下面试', checked: false }, { text: '线上面试', checked: true }],
    meeting: { selected: '微信视频', code: '8' }, date: { value: '2026-09-05' }, time: { value: '10:00-11:00' }, send: { found: true, disabled: false },
  }
  assert.deepEqual(bossInterviewFormMismatch(ok, plan), [])
  assert.deepEqual(bossInterviewFormMismatch({ ...ok, meeting: { selected: '', code: '8' } }, plan), [], '下拉关着选中类没了,隐藏 input 的码 8 单独就算(或关系)')
  assert.deepEqual(bossInterviewFormMismatch({ ...ok, meeting: { selected: '微信视频', code: '0' } }, plan), [], '文案单独也算')
  assert.match(bossInterviewFormMismatch({ ...ok, modal: 0 }, plan).join(';'), /模态数 0/)
  assert.match(bossInterviewFormMismatch({ ...ok, radios: [{ text: '线下面试', checked: true }, { text: '线上面试', checked: false }] }, plan).join(';'), /面试类型「线下面试」/)
  assert.match(bossInterviewFormMismatch({ ...ok, meeting: { selected: 'BOSS视频面试间 (AI总结面试，推荐更精准)', code: '0' } }, plan).join(';'), /面试平台/)
  assert.match(bossInterviewFormMismatch({ ...ok, date: { value: '2026-09-06' } }, plan).join(';'), /日期「2026-09-06」/)
  assert.match(bossInterviewFormMismatch({ ...ok, time: { value: '10:00-11:30' } }, plan).join(';'), /时间「10:00-11:30」/)
  assert.match(bossInterviewFormMismatch({ ...ok, send: { found: false, disabled: false } }, plan).join(';'), /发送键不在/)
  assert.match(bossInterviewFormMismatch({ ...ok, send: { found: true, disabled: true } }, plan).join(';'), /disabled/)
  const offline = { ...plan, radioText: '线下面试', meetingText: null, meetingCode: null }
  assert.equal(bossInterviewFormMismatch({ ...ok, meeting: { selected: '', code: '' } }, offline).length, 1, '线下不看平台,只剩类型不对这一条')
  assert.deepEqual(bossInterviewFormMismatch({ ...ok, radios: [{ text: '线下面试', checked: true }, { text: '线上面试', checked: false }], meeting: { selected: '', code: '' } }, offline), [])
})

test('BOSS 工具栏约面试钮:文案含 tooltip 副本按"含"分类,查看面试优先,恰一个才认;含版命中测试在点前一刻核文本', () => {
  const { domReadBossInterviewButton, domHitTestContains } = bossTestHooks
  const SEL = '.conversation-operate .operate-btn'
  const btn = (text, disabled = false) => {
    const el = {
      tagName: 'DIV', textContent: text, classList: { contains: (c) => c === 'disabled' && disabled },
      getBoundingClientRect: () => ({ x: 500, y: 80, width: 77, height: 24 }), contains: (n) => n === el,
    }
    return el
  }
  const saved = globalThis.document
  const install = (buttons, at) => { globalThis.document = { querySelectorAll: (s) => (s === SEL ? buttons : []), elementFromPoint: () => at } }
  try {
    const invite = btn('约面试\n牛人接受面试后可查看电话\n约面试')
    install([btn('换电话'), btn('换微信'), btn('求简历'), invite])
    const read = domReadBossInterviewButton(SEL, '约面试', '查看面试')
    assert.deepEqual([read.found, read.kind, read.index, read.disabled, read.rect], [true, 'invite', 3, false, { x: 500, y: 80, w: 77, h: 24 }])
    install([btn('换电话'), btn('查看微信'), btn('求简历'), btn('查看面试')])
    assert.equal(domReadBossInterviewButton(SEL, '约面试', '查看面试').kind, 'view')
    install([btn('约面试'), btn('查看面试')])
    assert.deepEqual([domReadBossInterviewButton(SEL, '约面试', '查看面试').found, domReadBossInterviewButton(SEL, '约面试', '查看面试').count], [false, 2], '两个都像就不猜')
    install([btn('换电话')])
    assert.equal(domReadBossInterviewButton(SEL, '约面试', '查看面试').count, 0)
    install([btn('换电话'), btn('换微信'), btn('求简历'), btn('约面试', true)])
    assert.equal(domReadBossInterviewButton(SEL, '约面试', '查看面试').disabled, true)
    install([btn('换电话'), btn('换微信'), btn('求简历'), invite], invite)
    assert.equal(domHitTestContains(SEL, 3, '约面试', '查看面试', 1, 1).onTarget, true)
    const other = btn('换微信')
    install([btn('换电话'), other, btn('求简历'), invite], other)
    assert.match(domHitTestContains(SEL, 3, '约面试', '查看面试', 1, 1).found, /别的元素/)
    const flipped = btn('查看面试')
    install([btn('换电话'), btn('换微信'), btn('求简历'), flipped], flipped)
    assert.match(domHitTestContains(SEL, 3, '约面试', '查看面试', 1, 1).found, /文本已变/, '点前一刻已变成「查看面试」就不点')
    install([btn('换电话')], btn('换电话'))
    assert.match(domHitTestContains(SEL, 3, '约面试', '查看面试', 1, 1).found, /不在原来的位置/)
  } finally { globalThis.document = saved }
})

test('BOSS 邀面模态页面读与发送闸:可点的东西带全序列下标与矩形,时间列项归各自的 ul,成功弹窗按文案认;发送闸六项缺一不点', () => {
  const { domReadBossInterviewModal, domBossInterviewSendGate, INTERVIEW_SEL: sel } = bossTestHooks
  const hidden = { x: 0, y: 0, w: 0, h: 0 }
  const mk = ({ text = '', classes = [], rect = { x: 10, y: 10, w: 50, h: 20 }, value, kids = [], scroll } = {}) => {
    const el = {
      tagName: 'DIV', textContent: text, classList: { contains: (c) => classes.includes(c) },
      getBoundingClientRect: () => ({ x: rect.x, y: rect.y, width: rect.w, height: rect.h }),
      contains: (n) => n === el || kids.includes(n),
      getAttribute: () => null,
      ...(value === undefined ? {} : { value }),
      ...(scroll ?? {}),
    }
    return el
  }
  const startItems = Array.from({ length: 25 }, (_, i) => mk({
    text: `${String(8 + Math.floor(i / 2)).padStart(2, '0')}:${i % 2 ? '30' : '00'}`, classes: i === 4 ? ['selected'] : [], rect: { x: 700, y: 400 + i * 44, w: 107, h: 44 },
  }))
  const endItems = Array.from({ length: 23 }, (_, i) => mk({
    text: `${String(11 + Math.floor(i / 2)).padStart(2, '0')}:${i % 2 ? '30' : '00'}`, rect: { x: 807, y: 400 + i * 44, w: 107, h: 44 },
  }))
  const listRect = (x) => ({ x, y: 400, w: 107, h: 196 })
  const startList = mk({ rect: listRect(700), kids: startItems, scroll: { scrollTop: 0, scrollHeight: 1100, clientHeight: 196 } })
  const endList = mk({ rect: listRect(807), kids: endItems, scroll: { scrollTop: 88, scrollHeight: 1012, clientHeight: 196 } })
  const hiddenList = mk({ rect: hidden, kids: [] })
  const send = mk({ text: '发送', rect: { x: 900, y: 600, w: 74, h: 36 } })
  const cancel = mk({ text: '取消', rect: { x: 820, y: 600, w: 74, h: 36 } })
  const radios = [
    mk({ text: '线下面试', rect: { x: 500, y: 200, w: 78, h: 20 } }),
    mk({ text: '线上面试', classes: ['radio-checked'], rect: { x: 600, y: 200, w: 78, h: 20 } }),
  ]
  const meetingItems = [
    mk({ text: 'BOSS视频面试间 (AI总结面试，推荐更精准)' }), mk({ text: '电话面试' }),
    mk({ text: '微信视频', classes: ['ui-select-item-selected'] }), mk({ text: '其他方式（腾讯会议、钉钉、飞书）' }),
  ]
  const close = mk({ rect: { x: 1000, y: 100, w: 20, h: 20 } })
  const popup = mk({ text: '面试邀请已发出 请您等待牛人接受面试邀请 转发面试', rect: { x: 0, y: 0, w: 1470, h: 746 }, kids: [close] })
  const rows = [
    { getAttribute: () => '11-0', classList: { contains: (c) => c === 'selected' } },
    { getAttribute: () => '12-0', classList: { contains: () => false } },
  ]
  const modalRect = { x: 443, y: 105, w: 584, h: 536 }
  const dateInput = (value) => [mk({ value, rect: { x: 600, y: 300, w: 210, h: 36 } })]
  const timeInput = (value) => [mk({ value, rect: { x: 820, y: 300, w: 214, h: 36 } })]
  const doc = {
    [sel.modal]: [mk({ rect: modalRect })],
    [sel.title]: [mk({ text: ' 线上面试邀请 ', rect: { x: 460, y: 110, w: 200, h: 30 } })],
    [sel.radio]: radios,
    [sel.address]: [],
    [sel.meeting]: [mk({ classes: [], rect: { x: 600, y: 240, w: 210, h: 36 } })],
    [sel.meetingItem]: meetingItems,
    [sel.meetingHidden]: [mk({ value: '8', rect: hidden })],
    [sel.dateWrap]: [mk({ classes: [] })],
    [sel.dateInput]: dateInput('2026-09-05'),
    [sel.dateMonth]: [], [sel.dateNext]: [], [sel.dateCell]: [],
    [sel.timeContainer]: [mk({ classes: ['dropdown-menu-open'] })],
    [sel.timeInput]: timeInput(''),
    [sel.timeTab]: [mk({ text: '精准时间', rect: { x: 820, y: 350, w: 56, h: 22 } }), mk({ text: '宽松时间', classes: ['selected'], rect: { x: 900, y: 350, w: 56, h: 22 } })],
    [sel.timeList]: [hiddenList, startList, endList],
    [sel.timeItem]: [...startItems, ...endItems],
    [sel.cancel]: [cancel],
    [sel.send]: [send],
    [sel.popup]: [],
    [sel.popupClose]: [mk({ rect: hidden }), close],
    '.geek-item': rows,
  }
  const saved = { document: globalThis.document, window: globalThis.window }
  let at = send
  globalThis.window = { innerWidth: 1470, innerHeight: 746 }
  globalThis.document = { querySelectorAll: (s) => doc[s] ?? [], elementFromPoint: () => at }
  try {
    const read = domReadBossInterviewModal(sel)
    assert.equal(read.modal, 1)
    assert.deepEqual([read.title, read.titleIndex, read.titleRect], ['线上面试邀请', 0, { x: 460, y: 110, w: 200, h: 30 }])
    assert.deepEqual(read.radios.map((r) => [r.index, r.text, r.checked]), [[0, '线下面试', false], [1, '线上面试', true]])
    assert.equal(read.address, null, '线上没有地址行')
    assert.deepEqual([read.meeting.present, read.meeting.index, read.meeting.open, read.meeting.selected, read.meeting.code], [true, 0, false, '微信视频', '8'])
    assert.equal(read.meeting.items[2].index, 2)
    assert.deepEqual([read.date.index, read.date.value, read.date.open, read.date.month, read.date.nextIndex, read.date.nextRect, read.date.cells],
      [0, '2026-09-05', false, '', -1, null, []])
    assert.deepEqual([read.time.index, read.time.value, read.time.open], [0, '', true])
    assert.deepEqual(read.time.tabs.map((t) => [t.index, t.text, t.selected]), [[0, '精准时间', false], [1, '宽松时间', true]])
    assert.equal(read.time.lists.length, 2, '隐藏的 ul 不算')
    assert.deepEqual([read.time.lists[0].index, read.time.lists[0].scrollTop, read.time.lists[0].rect], [1, 0, { x: 700, y: 400, w: 107, h: 196 }])
    assert.deepEqual([read.time.lists[1].index, read.time.lists[1].scrollTop, read.time.lists[1].items.length], [2, 88, 23])
    assert.deepEqual(read.time.lists[0].items.slice(0, 5).map((i) => [i.index, i.text, i.selected]),
      [[0, '08:00', false], [1, '08:30', false], [2, '09:00', false], [3, '09:30', false], [4, '10:00', true]])
    assert.deepEqual([read.time.lists[1].items[0].index, read.time.lists[1].items[0].text], [25, '11:00'], '结束列项的下标接在开始列之后:全序列下标')
    assert.deepEqual([read.cancel.found, read.cancel.index, read.send.found, read.send.index, read.send.disabled], [true, 0, true, 0, false])
    assert.deepEqual(read.popup, { found: false, closeIndex: -1, closeRect: hidden })
    assert.deepEqual(read.viewport, { w: 1470, h: 746 })

    doc[sel.popup] = [popup]
    assert.deepEqual(domReadBossInterviewModal(sel).popup, { found: true, closeIndex: 1, closeRect: { x: 1000, y: 100, w: 20, h: 20 } },
      '成功弹窗按文案认,关闭键取它里面可见的那个')
    doc[sel.popup] = []
    doc[sel.address] = [mk({ value: ' 上海市静安区xx路1号 ' })]
    assert.equal(domReadBossInterviewModal(sel).address, '上海市静安区xx路1号')
    doc[sel.address] = [mk({ value: '' })]
    assert.equal(domReadBossInterviewModal(sel).address, '', '地址行在但为空:平台未配地址')
    doc[sel.dateWrap] = [mk({ classes: ['ui-datepicker-visible'] })]
    doc[sel.dateMonth] = [mk({ text: '2026年 九月' })]
    doc[sel.dateNext] = [mk({ rect: { x: 800, y: 340, w: 20, h: 20 } })]
    doc[sel.dateCell] = [mk({ text: '', classes: ['blank'], rect: hidden }), mk({ text: '', classes: ['blank'] }), mk({ text: '今', classes: ['today'] }), mk({ text: '5', rect: { x: 700, y: 740, w: 32, h: 32 } })]
    const calendar = domReadBossInterviewModal(sel).date
    assert.deepEqual([calendar.open, calendar.month, calendar.nextIndex, calendar.nextRect], [true, '2026年 九月', 0, { x: 800, y: 340, w: 20, h: 20 }])
    assert.deepEqual(calendar.cells.map((c) => [c.index, c.text, c.blank, c.today, c.clip.h]), [[1, '', true, false, 20], [2, '今', false, true, 20], [3, '5', false, false, 6]],
      '零尺寸的格不算;下标是全序列下标;超出视口底的格 clip 只剩 6px')

    const expect = { radioText: '线上面试', meetingText: '微信视频', meetingCode: '8', date: '2026-09-05', timeValue: '10:00-11:00' }
    doc[sel.timeInput] = timeInput('10:00-11:00')
    const gate = () => domBossInterviewSendGate(sel, expect, '.geek-item', '11-0', 'selected', 1, 1)
    assert.deepEqual(gate(), { onTarget: true, found: '邀面发送键' })
    at = cancel
    assert.match(gate().found, /别的元素/, '落点偏到取消键上不点')
    at = send
    doc[sel.modal] = []
    assert.match(gate().found, /模态数 0/)
    doc[sel.modal] = [mk({ rect: modalRect })]
    doc[sel.radio] = [mk({ text: '线下面试', classes: ['radio-checked'], rect: { x: 500, y: 200, w: 78, h: 20 } }), radios[1]]
    assert.match(gate().found, /面试类型「线下面试\/线上面试」/, '两个都选中也不对')
    doc[sel.radio] = radios
    doc[sel.meetingItem] = [mk({ text: '电话面试', classes: ['ui-select-item-selected'] })]
    doc[sel.meetingHidden] = [mk({ value: '1', rect: hidden })]
    assert.match(gate().found, /面试平台「电话面试」\/码「1」/)
    doc[sel.meetingItem] = [mk({ text: '电话面试' })]
    doc[sel.meetingHidden] = [mk({ value: '8', rect: hidden })]
    assert.deepEqual(gate(), { onTarget: true, found: '邀面发送键' }, '选中类没了但隐藏 input 码是 8:或关系,放行')
    doc[sel.meetingItem] = meetingItems
    doc[sel.dateInput] = dateInput('2026-09-06')
    assert.match(gate().found, /日期「2026-09-06」/)
    doc[sel.dateInput] = dateInput('2026-09-05')
    doc[sel.timeInput] = timeInput('10:00-11:30')
    assert.match(gate().found, /时间「10:00-11:30」/)
    doc[sel.timeInput] = timeInput('10:00-11:00')
    doc['.geek-item'] = [rows[1]]
    assert.match(gate().found, /选中行不是目标会话/, '真人切走会话后不点')
    doc['.geek-item'] = rows
    doc[sel.send] = [mk({ text: '发送', rect: { x: 900, y: 600, w: 74, h: 36 }, classes: ['disabled'] })]
    at = doc[sel.send][0]
    assert.match(gate().found, /disabled/)
    doc[sel.send] = [send]
    at = send
    assert.deepEqual(gate(), { onTarget: true, found: '邀面发送键' }, '全部复原后仍通过')
    assert.equal(domBossInterviewSendGate(sel, { ...expect, radioText: '线下面试', meetingText: null, meetingCode: null }, '.geek-item', '11-0', 'selected', 1, 1).found,
      '面试类型「线上面试」≠「线下面试」', '线下不看平台')
  } finally {
    globalThis.document = saved.document
    globalThis.window = saved.window
  }
})

test('BOSS 锚尾匹配:唯一命中才给起点,零命中与重复命中都不裁', () => {
  const { matchAnchorTail } = bossTestHooks
  const h = (c) => c.repeat(64)
  const messages = [
    { direction: 'in', contentHash: h('a') }, { direction: 'out', contentHash: h('b') },
    { direction: 'in', contentHash: h('c') }, { direction: 'out', contentHash: h('b') },
    { direction: 'in', contentHash: h('c') },
  ]
  assert.deepEqual(matchAnchorTail(messages, []), { count: 0, start: null })
  assert.deepEqual(matchAnchorTail(messages, [{ direction: 'in', contentHash: h('a') }, { direction: 'out', contentHash: h('b') }]), { count: 1, start: 0 })
  assert.deepEqual(matchAnchorTail(messages, [{ direction: 'out', contentHash: h('b') }, { direction: 'in', contentHash: h('c') }]), { count: 2, start: null }, '重复命中必须保留完整候选窗口')
  assert.deepEqual(matchAnchorTail(messages, [{ direction: 'out', contentHash: h('z') }]), { count: 0, start: null })
  assert.deepEqual(matchAnchorTail(messages.slice(0, 1), [{ direction: 'in', contentHash: h('a') }, { direction: 'out', contentHash: h('b') }]), { count: 0, start: null }, '锚比窗口还长不算命中')
})

test('BOSS 历史翻页收场:数组长了是拉到上一页,没长但 isToTop 翻真是到顶,都没有是没读完', () => {
  const { bossHistoryLoadOutcome } = bossTestHooks
  const rows = (n) => Array.from({ length: n }, (_, i) => ({ mid: String(i) }))
  // 2026-09-09 真机:第 1 页 19 条 isToTop=false,滚到顶拉回 3 条变 22 条、isToTop 翻 true
  assert.equal(bossHistoryLoadOutcome({ rows: rows(19) }, { rows: rows(22), isToTop: true }), 'grew')
  assert.equal(bossHistoryLoadOutcome({ rows: rows(19) }, { rows: rows(22), isToTop: false }), 'grew')
  assert.equal(bossHistoryLoadOutcome({ rows: rows(22) }, { rows: rows(22), isToTop: true }), 'top', '页面自己翻真才算到顶')
  assert.equal(bossHistoryLoadOutcome({ rows: rows(19) }, { rows: rows(19), isToTop: false }), 'stalled', '网卡:没长也没翻真,绝不当到顶')
  assert.equal(bossHistoryLoadOutcome({ rows: rows(5) }, { rows: rows(5), isToTop: false }), 'stalled', '少于一页也不按条数猜到顶')
})

test('BOSS 对不齐的判定现场只带方向、种类与 hash 前 8 位,不带正文', () => {
  const { describeBossThreadAlignment } = bossTestHooks
  const h = (c) => c.repeat(64)
  const projected = [
    { direction: 'out', kind: 'card', contentHash: h('5'), text: '请求交换微信已发送' },
    { direction: 'in', kind: 'text', contentHash: h('3'), text: '好的' },
    { direction: 'out', kind: 'text', contentHash: h('b'), text: '好了～晚点加你' },
  ]
  const anchors = [{ direction: 'out', contentHash: h('5') }, { direction: 'in', contentHash: h('3') }]
  const detail = describeBossThreadAlignment(projected, anchors)
  assert.equal(detail, `锚尾=[out:${'5'.repeat(8)},in:${'3'.repeat(8)}] 页面尾=[out:card:${'5'.repeat(8)},in:text:${'3'.repeat(8)},out:text:${'b'.repeat(8)}]`)
  assert.ok(!detail.includes('好的') && !detail.includes('微信'), '判定现场不得带正文')
  const long = Array.from({ length: 10 }, (_, i) => ({ direction: 'in', kind: 'text', contentHash: h(String(i)) }))
  assert.equal(describeBossThreadAlignment(long, []).split(',').length, 6, '页面尾只带最后 6 行')
})

test('BOSS 截图逐帧上滚的要求量:按带高能装的整格数规划,实际位移不越过带高;剩余不足按剩余', () => {
  const { bossCaptureScrollRequest } = bossTestHooks
  const ticks = (request) => Math.ceil(request / 100) // runOsScroll 首簇的估格法
  // 2026-09-09 真机:简历面板展开时露出带 224px,要 100 走 1 格 120;收起后约 450px
  assert.equal(bossCaptureScrollRequest(224, 2000), 100)
  assert.ok(ticks(100) * 120 <= 224)
  assert.equal(bossCaptureScrollRequest(450, 2000), 300)
  assert.ok(ticks(300) * 120 <= 450, '3 格 360 不越过 450;旧算法要 320 会派 4 格 480 越过')
  assert.equal(bossCaptureScrollRequest(130, 2000), 100, '刚好装一格(留 10px)')
  assert.equal(bossCaptureScrollRequest(129, 2000), 129, '差 1px 装不下整格:按带高要')
  assert.equal(bossCaptureScrollRequest(450, 80), 80, '剩余不足一步只要剩余')
  assert.equal(bossCaptureScrollRequest(100, 2000), 100, '带矮于一格:只能按带高要,越过与否由回读裁')
  assert.equal(bossCaptureScrollRequest(1, 1), 1)
  assert.equal(bossCaptureScrollRequest(450, 0), 1, '下限 1,不给 0 让滚轮空转')
})

test('BOSS 拼接到头判定:底部锚定带顶碰起点或滚到 0,顶部锚定带底碰覆盖终点或滚到底;剩余量随之', () => {
  const { bossCaptureCovered, bossCaptureRemaining } = bossTestHooks
  // 聊天(底部锚定):内容 1946,露出带 280 从容器顶 0 起,预算覆盖全部 → 起点 0
  const chat = (scrollTop) => ({ scrollTop, clientH: 280, bandOffset: 0, bandHeight: 280, totalScroll: 1946, startTop: 0, coveredCssH: 1946 })
  assert.equal(bossCaptureCovered('bottom', chat(1666)), false)
  assert.equal(bossCaptureRemaining('bottom', chat(1666)), 1666)
  assert.equal(bossCaptureCovered('bottom', chat(0)), true, '滚到 0 即到头')
  const partial = (scrollTop) => ({ scrollTop, clientH: 280, bandOffset: 0, bandHeight: 280, totalScroll: 1946, startTop: 1000, coveredCssH: 946 })
  assert.equal(bossCaptureCovered('bottom', partial(1001)), true, '带顶碰到起点(±1)即到头')
  assert.equal(bossCaptureRemaining('bottom', partial(1300)), 300)
  // 简历(顶部锚定):内容 2906,可见 662 全露,预算覆盖全部
  const resume = (scrollTop) => ({ scrollTop, clientH: 662, bandOffset: 0, bandHeight: 662, totalScroll: 2906, startTop: 0, coveredCssH: 2906 })
  assert.equal(bossCaptureCovered('top', resume(0)), false)
  assert.equal(bossCaptureRemaining('top', resume(0)), 2244)
  assert.equal(bossCaptureCovered('top', resume(2244)), true, '滚到底即到头')
  const capped = (scrollTop) => ({ scrollTop, clientH: 662, bandOffset: 0, bandHeight: 662, totalScroll: 2906, startTop: 0, coveredCssH: 1324 })
  assert.equal(bossCaptureCovered('top', capped(600)), false)
  assert.equal(bossCaptureRemaining('top', capped(600)), 62)
  assert.equal(bossCaptureCovered('top', capped(662)), true, '带底碰到覆盖终点即到头')
  // 露出带被顶部遮掉 50px:带顶 = scrollTop + 50
  assert.equal(bossCaptureRemaining('bottom', { scrollTop: 100, clientH: 280, bandOffset: 50, bandHeight: 230, totalScroll: 1946, startTop: 0, coveredCssH: 1946 }), 150)
})

test('BOSS 拼接帧预算:按每帧真实前进量(滚轮要求量)算帧数,不按带高;画布高同口径', () => {
  const { bossCaptureFrameBudget } = bossTestHooks
  // 2026-09-09 第二场彩排:带 334、内容 1946,每帧要 200 走 240;旧算法 ceil(1946/334)=6 帧就停
  const chat = bossCaptureFrameBudget(334, 1946, 16, 2)
  assert.equal(chat.advanceCss, 200)
  assert.equal(chat.budget, 10, '1 + ceil((1946-334)/200)')
  assert.equal(chat.coveredCssH, 1946)
  // 简历:带 662、内容 2906,要 500 走 600 → 预算 6,实拍 5 帧到底
  const resume = bossCaptureFrameBudget(662, 2906, 16, 2)
  assert.deepEqual([resume.advanceCss, resume.budget, resume.coveredCssH], [500, 6, 2906])
  // 帧数封顶时画布只到能拼到的高度
  const capped = bossCaptureFrameBudget(280, 5000, 4, 2)
  assert.deepEqual([capped.budget, capped.coveredCssH], [4, 280 + 3 * 200])
  // 一屏装下:1 帧
  assert.deepEqual(bossCaptureFrameBudget(662, 500, 16, 2).budget, 1)
})

test('OS 探针原地判定:光标在靶子矩形内缩 4px 之内才算在靶上,贴边与靶外不算,位置未知不算', async () => {
  const { cursorRestsInRect } = await import(unitBundleURL)
  const rect = { x: 579, y: 167, w: 810, h: 280 } // 2026-09-09 真机:收起面板后的消息容器
  assert.equal(cursorRestsInRect({ x: 984, y: 300 }, rect, 4), true, '容器中央')
  assert.equal(cursorRestsInRect({ x: 583, y: 171 }, rect, 4), true, '恰在内缩边上')
  assert.equal(cursorRestsInRect({ x: 580, y: 300 }, rect, 4), false, '贴左边 1px 不算')
  assert.equal(cursorRestsInRect({ x: 984, y: 446 }, rect, 4), false, '贴底边不算')
  assert.equal(cursorRestsInRect({ x: 200, y: 300 }, rect, 4), false, '靶外')
  assert.equal(cursorRestsInRect({ x: null, y: 300 }, rect, 4), false, '位置未知')
  assert.equal(cursorRestsInRect({ x: 3, y: 3 }, { x: 0, y: 0, w: 6, h: 6 }, 4), true, '靶子比内缩还小时内缩封顶到一半:只剩正中一点')
  assert.equal(cursorRestsInRect({ x: 5, y: 5 }, { x: 0, y: 0, w: 6, h: 6 }, 4), false)
})

test('BOSS 列表行摘要:引用与候选人引用取自内存 id,职位名空则省略,秒级 lastTS 转毫秒', () => {
  const { summarizeBossListRow } = bossTestHooks
  const row = { uid: 650166511, friendSource: 0, name: ' 宋先生 ', jobName: '销售经理', newMsgCount: 1, lastTS: 1788402198000, lastText: '您好,对贵公司很感兴趣', lastIsSelf: false }
  const summary = summarizeBossListRow(row)
  assert.equal(summary.conversationRef, '650166511-0')
  assert.equal(summary.peer.platformUserRef, '650166511')
  assert.equal(summary.peer.displayName, '宋先生')
  assert.equal(summary.positionTitle, '销售经理')
  assert.equal(summary.unreadCount, 1)
  assert.equal(summary.lastActivityTs, 1788402198000)
  assert.deepEqual(summary.lastMessage, { direction: 'in', kind: 'text', textPreview: '您好,对贵公司很感兴趣' })
  const bare = summarizeBossListRow({ ...row, jobName: ' ', lastTS: 1788402198, lastText: '', lastIsSelf: true, newMsgCount: -1 })
  assert.equal('positionTitle' in bare, false, '缺失必须省略,不得猜')
  assert.equal(bare.lastActivityTs, 1788402198000)
  assert.deepEqual(bare.lastMessage, { direction: 'out', kind: 'system', textPreview: '' })
  assert.equal(bare.unreadCount, 0)
})

/** 假页面:querySelectorAll 按选择器分发;带 __vue__ 的元素模拟 Vue 实例。 */
function installBossPageFixture({ instances = [], rows = [], viewport = { w: 1470, h: 746 } } = {}) {
  const saved = { document: globalThis.document, window: globalThis.window }
  const vueElements = instances.map((instance) => ({ __vue__: instance }))
  const rowElements = rows.map((row) => ({
    getAttribute(name) { return name === 'data-id' ? row.dataId : null },
    classList: { contains(cls) { return cls === 'selected' && row.selected === true } },
    querySelector(selector) {
      return selector === '.badge-count' && row.bubble
        ? { textContent: row.bubble, getClientRects() { return [{}] } }
        : null
    },
    getBoundingClientRect() { return row.rect ?? { x: 229, y: 352, width: 339, height: 74, left: 229, top: 352, right: 568, bottom: 426 } },
  }))
  globalThis.window = { innerWidth: viewport.w, innerHeight: viewport.h }
  globalThis.document = {
    querySelectorAll(selector) { return selector === '*' ? vueElements : selector === '.geek-item' ? rowElements : [] },
  }
  return { restore() { globalThis.document = saved.document; globalThis.window = saved.window } }
}

const listRow = (uid, friendSource, extra = {}) => ({
  uid, friendSource, encryptUid: `enc-${uid}`, uniqueId: `u${uid}`, newMsgCount: 0, name: `候选${uid}`,
  jobName: '销售经理', lastTS: 1788402198000, lastText: '你好', lastIsSelf: false, ...extra,
})

test('BOSS 列表数据层:按形状找数组,取与 DOM 行顺序对齐的那份、最长者胜;未读数与总数如实', () => {
  const rows = [listRow(11, 0, { newMsgCount: 2 }), listRow(12, 0), listRow(13, 1, { newMsgCount: 1 }), listRow(14, 0)]
  const chat = { 'list$': rows, 'allList$': [listRow(99, 0), ...rows] }
  const virtual = { $props: { dataSources: rows, other: 1 } }
  const decoy = { 'list$': [{ label: '全部', labelId: 1 }] }
  const page = installBossPageFixture({
    instances: [chat, virtual, decoy],
    rows: rows.slice(0, 3).map((r) => ({ dataId: `${r.uid}-${r.friendSource}` })),
  })
  try {
    const read = bossTestHooks.mainReadBossListWindow(32, '.geek-item')
    assert.equal(read.status, 'ready')
    assert.equal(read.total, 4, 'DOM 只画 3 行,数据层 4 条才是窗口')
    assert.equal(read.rows.length, 4)
    assert.deepEqual(read.rows.map((r) => [r.uid, r.friendSource, r.newMsgCount]), [[11, 0, 2], [12, 0, 0], [13, 1, 1], [14, 0, 0]])
    assert.equal(read.rows[0].name, '候选11')
    assert.equal(bossTestHooks.mainReadBossListWindow(2, '.geek-item').rows.length, 2, 'limit 只裁窗口不裁 total')
  } finally { page.restore() }

  const misaligned = installBossPageFixture({ instances: [chat], rows: [{ dataId: '12-0' }, { dataId: '11-0' }] })
  try {
    const read = bossTestHooks.mainReadBossListWindow(32, '.geek-item')
    assert.equal(read.status, 'missing', '没有一份数组与 DOM 顺序对齐就是读不到,不猜一份')
  } finally { misaligned.restore() }

  const empty = installBossPageFixture({ instances: [{ 'list$': [] }], rows: [] })
  try {
    assert.equal(bossTestHooks.mainReadBossListWindow(32, '.geek-item').status, 'empty')
  } finally { empty.restore() }

  const nothing = installBossPageFixture({ instances: [decoy], rows: [] })
  try {
    assert.equal(bossTestHooks.mainReadBossListWindow(32, '.geek-item').status, 'missing', '连空数组都没有不算可信空态')
  } finally { nothing.restore() }
})

test('BOSS 当前会话:没打开时零持有者→none,十几个组件持同一对象→ready,两个不同对象→ambiguous', () => {
  const conversation = { uid: 650166511, friendSource: 0, encryptUid: 'enc', uniqueId: 'u', name: '宋先生', weixin: null }
  const none = installBossPageFixture({ instances: [{ 'list$': [] }, { other: 1 }] })
  try { assert.deepEqual(bossTestHooks.mainReadBossCurrentConversation(), { status: 'none' }) } finally { none.restore() }
  const many = installBossPageFixture({ instances: [{ 'conversation$': conversation }, { 'conversation$': conversation }, { 'conversation$': { ...conversation } }] })
  try {
    assert.deepEqual(bossTestHooks.mainReadBossCurrentConversation(), { status: 'ready', uid: 650166511, friendSource: 0, name: '宋先生' })
  } finally { many.restore() }
  const two = installBossPageFixture({ instances: [{ 'conversation$': conversation }, { 'conversation$': { ...conversation, uid: 7 } }] })
  try { assert.deepEqual(bossTestHooks.mainReadBossCurrentConversation(), { status: 'ambiguous', count: 2 }) } finally { two.restore() }
  const proto = Object.create({ 'conversation$': conversation })
  const inherited = installBossPageFixture({ instances: [proto] })
  try { assert.deepEqual(bossTestHooks.mainReadBossCurrentConversation(), { status: 'none' }, '只认自有属性') } finally { inherited.restore() }
})

test('BOSS 消息数组:方向只认 fromId 对我方 userId,绑定核对用同一次注入里的 conversation$,userId 不出页面', () => {
  const me = 765357657
  const peer = 650166511
  const conversation = { uid: peer, friendSource: 0, name: '宋先生' }
  const list = [
    { mid: 4001, body: { type: 1, text: '您好' }, fromId: peer, isSelf: false, type: 'text', bizType: 101, status: 2, time: 1788402100000 },
    { mid: 4002, body: { type: 1, text: '你好,方便聊聊吗' }, fromId: me, isSelf: true, type: 'text', bizType: 101, status: 1, time: 1788402200000 },
    { mid: 4003, body: { type: 4, text: '' }, fromId: 0, isSelf: false, type: 'action', bizType: 21130010, status: 1, time: 1788402300000 },
    { mid: 4004, body: { type: 14, text: '', interview: { condition: 1, text: '发送了面试邀请' } }, fromId: me, isSelf: true, type: 'text', bizType: 21130009, status: 1, time: 1788402400000 },
  ]
  const holder = { 'list$': list, isToTop: true, 'conversation$': conversation }
  const app = { 'user$': { userId: me, token: 'secret-token', phone: '13800000000' } }
  const page = installBossPageFixture({ instances: [app, holder, { 'conversation$': conversation }] })
  try {
    const read = bossTestHooks.mainReadBossThread(peer, 0)
    assert.equal(read.status, 'ready')
    assert.equal(read.isToTop, true)
    assert.equal(read.peerName, '宋先生')
    assert.deepEqual(read.rows.map((r) => [r.mid, r.direction, r.text, r.interviewCondition]),
      [['4001', 'in', '您好', null], ['4002', 'out', '你好,方便聊聊吗', null], ['4003', 'system', '', null], ['4004', 'out', '', 1]])
    const serialized = JSON.stringify(read)
    for (const secret of ['secret-token', '13800000000', String(me)]) {
      assert.equal(serialized.includes(secret), false, `页面读数泄露:${secret}`)
    }
    assert.equal(bossTestHooks.mainReadBossThread(7, 0).status, 'binding_mismatch', '列表绑定的不是目标就不交')
  } finally { page.restore() }
  const noUser = installBossPageFixture({ instances: [holder] })
  try { assert.equal(bossTestHooks.mainReadBossThread(peer, 0).status, 'identity_missing', '读不到我方身份就判不了方向,不猜') } finally { noUser.restore() }
  const noList = installBossPageFixture({ instances: [app] })
  try { assert.equal(bossTestHooks.mainReadBossThread(peer, 0).status, 'missing') } finally { noList.restore() }
})

test('清空输入框的按键序列:全选加删除,修饰键按操作系统选,留足修饰键窗口', () => {
  for (const [os, modifier] of [['darwin', 'MetaLeft'], ['windows', 'ControlLeft'], [undefined, 'ControlLeft']]) {
    const keys = composeClearKeys(os, () => 0.5)
    assert.deepEqual(keys.map((k) => k.code), [modifier, 'KeyA', 'Backspace'], `${os} 的修饰键`)
    const [mod, a, bs] = keys
    assert.equal(mod.modifier, true)
    assert.ok(mod.down < a.down && a.down < a.up && a.up < mod.up, '字母键在修饰键按住期间按下并松开')
    assert.ok(bs.down - mod.up >= 40, '修饰键松手到 Backspace 按下至少 40ms,否则手服务校验不放行')
    for (const k of keys) assert.ok(k.up > k.down, `${k.code} 松手晚于按下`)
  }
  // 抖动极值下窗口仍够
  for (const jitter of [() => 0, () => 0.999]) {
    const [mod, a, bs] = composeClearKeys('darwin', jitter)
    assert.ok(a.up < mod.up && bs.down - mod.up >= 40, '抖动极值下顺序与窗口仍成立')
  }
})

test('滚轮排版器:一簇 3~8 格同向、首格在 0、间隔在量程内且逐格放慢,同种子可复现', () => {
  for (let seed = 1; seed <= 300; seed++) {
    const direction = seed % 2 ? 1 : -1
    const burst = composeScrollBurst(direction, mulberry32(seed))
    assert.ok(burst.length >= SCROLL_BURST.minTicks && burst.length <= SCROLL_BURST.maxTicks, `种子 ${seed} 簇长 ${burst.length}`)
    assert.equal(burst[0].at, 0)
    for (const t of burst) assert.equal(t.dy, direction, '一簇只滚一个方向,否则手服务校验不放行')
    for (let i = 1; i < burst.length; i++) {
      const gap = burst[i].at - burst[i - 1].at
      // 减速系数最多把量程上限抬到 1 + 0.08×6 倍
      assert.ok(gap >= SCROLL_BURST.gapMinMs && gap <= SCROLL_BURST.gapMaxMs * (1 + SCROLL_BURST.decelPerTick * 6) + 1,
        `种子 ${seed} 第 ${i} 格间隔 ${gap}ms 出了量程`)
    }
    assert.ok(burst[burst.length - 1].at < 20_000, '整簇跨度远小于手服务 20s 封顶')
  }
  // 中位随机数下逐格放慢:后一格间隔严格大于前一格
  const flat = composeScrollBurst(1, () => 0.5)
  for (let i = 2; i < flat.length; i++) {
    assert.ok(flat[i].at - flat[i - 1].at > flat[i - 1].at - flat[i - 2].at, '簇内应逐格放慢')
  }
  // 预算比最小簇还小时按预算给,最后一簇短一点、不为凑数多滚
  assert.equal(composeScrollBurst(1, mulberry32(3), 2).length, 2)
  assert.equal(composeScrollBurst(1, mulberry32(3), 1).length, 1)
  assert.ok(composeScrollBurst(1, mulberry32(3), 100).length <= SCROLL_BURST.maxTicks, '预算再大也不超一簇上限')
  assert.deepEqual(composeScrollBurst(-1, mulberry32(9)), composeScrollBurst(-1, mulberry32(9)), '同种子同一簇')
  // 簇间停顿
  for (let seed = 1; seed <= 100; seed++) {
    const pause = composeScrollPause(mulberry32(seed))
    assert.ok(pause >= SCROLL_BURST.pauseMinMs && pause <= SCROLL_BURST.pauseMaxMs, `停顿 ${pause}ms`)
  }
})

/** 一个假滚动容器:每格 pxPerNotch 像素,钳在 [0, max];flip 为真时方向反着动(模拟注入器符号不符)。 */
function fakeScrollBox({ top = 0, scrollHeight = 5000, clientHeight = 700, pxPerNotch = 100, flip = false, cursorInside = false } = {}) {
  const box = { top, scrollHeight, clientHeight }
  // 假手报的光标固定在 (700,300):默认把容器放在它右边,让落点线真的走一遍;cursorInside 时容器盖住它,走原地路径。
  const rect = cursorInside ? { x: 600, y: 120, w: 500, h: 600 } : { x: 800, y: 120, w: 500, h: 600 }
  const max = scrollHeight - clientHeight
  return {
    box,
    onScroll(ticks) {
      for (const t of ticks) {
        const d = (flip ? -t.dy : t.dy) * pxPerNotch
        box.top = Math.max(0, Math.min(max, box.top + d))
      }
      return null
    },
    target: {
      label: '假容器',
      rect,
      async hitTest() { return { onTarget: true, found: '靶子(div)' } },
      async readMetrics() { return { scrollTop: box.top, scrollHeight, clientHeight } },
    },
  }
}

test('滚轮闭环:落到容器上再一簇簇滚,回读 scrollTop 滚够即停;最后一簇按剩余像素收短、绝不点击', async () => {
  const fake = fakeScrollBox()
  const hand = osClickHarness({ calibrated: true, onScroll: fake.onScroll })
  try {
    const out = await runOsScroll({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), fake.target, 'down', 650)
    assert.equal(out.outcome, 'scrolled', out.detail)
    assert.ok(out.scrolledPx >= 650 && out.scrolledPx <= 650 + 800, `滚了 ${out.scrolledPx}px`)
    assert.equal(out.scrollTopBefore, 0)
    assert.equal(out.scrollTopAfter, fake.box.top)
    assert.ok(out.bursts >= 1 && out.ticks >= 7, `簇 ${out.bursts} 格 ${out.ticks}`)
    assert.equal(hand.clicks(), 0, '滚轮探针绝不点击')
    assert.ok(hand.plays() >= 1, '要先经鼠标线落到容器上')
    assert.equal(hand.scrolls(), out.bursts)
    assert.match(out.detail, /簇#1/)
    // 契约装配:全整数、attempts 封顶
    const data = osScrollContractData(out, 1700000000000)
    for (const k of ['scrollTopBefore', 'scrollTopAfter', 'scrolledPx', 'ticks', 'bursts', 'attempts', 'elapsedMs', 'lagMaxUs']) {
      assert.ok(Number.isInteger(data[k]), `${k} 应为整数`)
    }
  } finally { hand.restore() }
})

test('滚轮闭环:剩余像素不足一簇时只排够用的格数——不为凑数多滚', async () => {
  const fake = fakeScrollBox()
  const hand = osClickHarness({ calibrated: true, onScroll: fake.onScroll })
  try {
    const out = await runOsScroll({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), fake.target, 'down', 150)
    assert.equal(out.outcome, 'scrolled', out.detail)
    assert.ok(out.ticks <= 2, `150px 按每格 100px 估只该排 2 格,实际 ${out.ticks}`)
  } finally { hand.restore() }
})

test('滚轮闭环:已在顶上就是 edge,一格不发;滚到底也是 edge', async () => {
  const atTop = fakeScrollBox({ top: 0 })
  let hand = osClickHarness({ calibrated: true, onScroll: atTop.onScroll })
  try {
    const out = await runOsScroll({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), atTop.target, 'up', 500)
    assert.equal(out.outcome, 'edge')
    assert.equal(hand.scrolls(), 0, '已在顶上不该发滚轮')
    assert.equal(out.ticks, 0)
  } finally { hand.restore() }
  const nearBottom = fakeScrollBox({ top: 4200 }) // max=4300,只剩 100px
  hand = osClickHarness({ calibrated: true, onScroll: nearBottom.onScroll })
  try {
    const out = await runOsScroll({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), nearBottom.target, 'down', 2000)
    assert.equal(out.outcome, 'edge', out.detail)
    assert.equal(out.scrollTopAfter, 4300)
    assert.ok(out.scrolledPx < 2000)
  } finally { hand.restore() }
})

test('滚轮闭环:一簇下去纹丝不动是 stuck,方向反了也是 stuck——如实报,不换方向重试', async () => {
  const dead = fakeScrollBox({ pxPerNotch: 0 })
  let hand = osClickHarness({ calibrated: true, onScroll: dead.onScroll })
  try {
    const out = await runOsScroll({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), dead.target, 'down', 500)
    assert.equal(out.outcome, 'stuck')
    assert.equal(hand.scrolls(), 1, '纹丝不动就停,不再发第二簇')
    assert.match(out.detail, /纹丝不动/)
  } finally { hand.restore() }
  const flipped = fakeScrollBox({ top: 2000, flip: true })
  hand = osClickHarness({ calibrated: true, onScroll: flipped.onScroll })
  try {
    const out = await runOsScroll({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), flipped.target, 'down', 500)
    assert.equal(out.outcome, 'stuck')
    assert.equal(hand.scrolls(), 1, '方向反了就停,不换方向再试')
    assert.match(out.detail, /方向反了/)
    assert.ok(out.scrollTopAfter < 2000, '页面确实反着动了,如实带出')
  } finally { hand.restore() }
})

test('滚轮闭环:落点闸没过就一格不滚;手服务拒绝(光标被动过)按未放行收场', async () => {
  const fake = fakeScrollBox()
  let hand = osClickHarness({ armed: false, onScroll: fake.onScroll })
  try {
    const out = await runOsScroll({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), fake.target, 'down', 500)
    assert.equal(out.outcome, 'refusedByGate')
    assert.equal(hand.scrolls(), 0, '没落到容器上不得发滚轮')
    assert.match(out.detail, /没落到容器上/)
  } finally { hand.restore() }
  hand = osClickHarness({ calibrated: true, onScroll: () => 'refuse' })
  try {
    const out = await runOsScroll({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), fake.target, 'down', 500)
    assert.equal(out.outcome, 'refusedByGate')
    assert.match(out.detail, /手服务拒绝滚轮/)
    assert.equal(hand.scrolls(), 1)
  } finally { hand.restore() }
})

test('滚轮闭环:光标已经停在容器上就不重落——不走鼠标线,直接滚;停在容器边上或靶外仍照常落点', async () => {
  const resting = fakeScrollBox({ cursorInside: true })
  let hand = osClickHarness({ calibrated: true, onScroll: resting.onScroll })
  try {
    const out = await runOsScroll({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), resting.target, 'down', 300)
    assert.equal(out.outcome, 'scrolled', out.detail)
    assert.equal(hand.plays(), 0, '光标已在靶上,一步不动')
    assert.ok(hand.scrolls() >= 1)
    assert.match(out.detail, /不重落/)
    assert.equal(out.attempts, 0)
  } finally { hand.restore() }
  // 标定没就绪时不走原地路径(热路径同款前提):照常散开落点
  hand = osClickHarness({ calibrated: false, onScroll: resting.onScroll })
  try {
    await runOsScroll({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), resting.target, 'down', 300)
    assert.ok(hand.plays() >= 1, '冷启动仍要先落点')
  } finally { hand.restore() }
  // 命中测试说那一点上不是靶子(被盖住):照常落点
  const covered = fakeScrollBox({ cursorInside: true })
  let hits = 0
  covered.target.hitTest = async () => { hits += 1; return hits === 1 ? { onTarget: false, found: '遮挡物(div.popup)' } : { onTarget: true, found: '靶子(div)' } }
  hand = osClickHarness({ calibrated: true, onScroll: covered.onScroll })
  try {
    const out = await runOsScroll({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), covered.target, 'down', 300)
    assert.ok(hand.plays() >= 1, '原地命中不是靶子就照常落点')
    assert.equal(out.outcome, 'scrolled', out.detail)
  } finally { hand.restore() }
})

test('考古定位:selector 命中不唯一且没给 index 即拒,越界即拒,可见部分太小即拒;命中给出可见矩形与签名', () => {
  const el = (rect, text = '', cls = 'chat-message-list') => ({
    tagName: 'DIV', className: cls, textContent: text, parentElement: null,
    getBoundingClientRect() { return { x: rect.x, y: rect.y, width: rect.w, height: rect.h, left: rect.x, top: rect.y, right: rect.x + rect.w, bottom: rect.y + rect.h } },
    scrollTop: 40, scrollHeight: 3000, clientHeight: 600,
  })
  const saved = { document: globalThis.document, window: globalThis.window }
  globalThis.window = { innerWidth: 1470, innerHeight: 746 }
  const nodes = {
    '.one': [el({ x: 600, y: 100, w: 500, h: 600 }, '你好')],
    '.many': [el({ x: 0, y: 0, w: 100, h: 100 }), el({ x: 200, y: 0, w: 100, h: 100 }, '第二个')],
    '.off': [el({ x: 1460, y: 100, w: 500, h: 600 })],
    '.none': [],
  }
  globalThis.document = { body: {}, querySelectorAll(sel) { if (sel === ':bad(') throw new SyntaxError('bad'); return nodes[sel] ?? [] } }
  try {
    const { domLocateBySelector, domReadScrollMetrics } = bossTestHooks
    assert.equal(domLocateBySelector('.none', -1).status, 'none')
    assert.equal(domLocateBySelector(':bad(', -1).status, 'bad_selector')
    assert.equal(domLocateBySelector('.many', -1).status, 'ambiguous', '不唯一不猜第一个')
    assert.equal(domLocateBySelector('.many', 1).status, 'ok', '给了 index 就按 index')
    assert.equal(domLocateBySelector('.many', 5).status, 'out_of_range')
    assert.equal(domLocateBySelector('.off', -1).status, 'offscreen', '只露 10px 光标没处落')
    const ok = domLocateBySelector('.one', -1)
    assert.equal(ok.status, 'ok')
    assert.deepEqual(ok.clip, { x: 600, y: 100, w: 500, h: 600 })
    assert.match(ok.signature, /^div\.chat-message-list\[500x600\]「你好」$/)
    assert.deepEqual(domReadScrollMetrics('.one', 0), { found: true, scrollTop: 40, scrollHeight: 3000, clientHeight: 600 })
    assert.equal(domReadScrollMetrics('.none', 0).found, false)
  } finally { globalThis.document = saved.document; globalThis.window = saved.window }
})

test('考古定位的框架跳转 A >>> B:矩形加 iframe 偏移换算回顶层视口、可见部分裁到 iframe 视口;命中测试先问顶层再进 iframe;文档自身滚动读 scrollingElement', () => {
  const rectOf = (r) => ({ x: r.x, y: r.y, width: r.w, height: r.h, left: r.x, top: r.y, right: r.x + r.w, bottom: r.y + r.h })
  const el = (rect, text = '', cls = 'btn btn-greet') => ({ tagName: 'BUTTON', className: cls, textContent: text, parentElement: null, getBoundingClientRect() { return rectOf(rect) }, contains(x) { return x === this } })
  const saved = { document: globalThis.document, window: globalThis.window }
  globalThis.window = { innerWidth: 1470, innerHeight: 662 }
  // iframe 在顶层 (168,40) 处,视口 1302x622;按钮在 iframe 视口坐标 (1122,90) 92x32 → 顶层 (1290,130)。
  const inner = el({ x: 1122, y: 90, w: 92, h: 32 }, '打招呼')
  const half = el({ x: 1122, y: 610, w: 92, h: 32 }, '半露')
  // html 的矩形只有视口那么高、滚过 300 后整个在视口上方——真机形态;定位要按 scrollingElement 合成。
  const html = { tagName: 'HTML', className: '', textContent: '', parentElement: null, scrollTop: 0, scrollHeight: 2814, clientHeight: 622, getBoundingClientRect() { return rectOf({ x: 0, y: -300, w: 1302, h: 622 }) } }
  const scrolling = { scrollTop: 300, scrollHeight: 2814, clientHeight: 622, clientWidth: 1302 }
  let innerAt = inner
  const innerDoc = { body: { tagName: 'BODY' }, documentElement: html, scrollingElement: scrolling, querySelectorAll(sel) { return sel === 'button.btn-greet' ? [inner] : sel === '.half' ? [half] : sel === 'html' ? [html] : [] }, elementFromPoint() { return innerAt } }
  html.ownerDocument = innerDoc
  const frame = { tagName: 'IFRAME', className: '', contentDocument: innerDoc, clientLeft: 0, clientTop: 0, clientWidth: 1302, clientHeight: 622, getBoundingClientRect() { return rectOf({ x: 168, y: 40, w: 1302, h: 622 }) } }
  const crossOrigin = { tagName: 'IFRAME', contentDocument: null, getBoundingClientRect() { return rectOf({ x: 0, y: 0, w: 10, h: 10 }) } }
  let topAt = frame
  globalThis.document = { body: {}, querySelector(sel) { return sel === 'iframe[name=recommendFrame]' ? frame : sel === 'iframe.cross' ? crossOrigin : null }, querySelectorAll() { return [] }, elementFromPoint() { return topAt } }
  try {
    const { domLocateBySelector, domReadScrollMetrics, domHitTestExpected, domHitTestIndexed } = bossTestHooks
    const ok = domLocateBySelector('iframe[name=recommendFrame] >>> button.btn-greet', -1)
    assert.equal(ok.status, 'ok', ok.detail)
    assert.deepEqual(ok.rect, { x: 1290, y: 130, w: 92, h: 32 }, '矩形换算到顶层视口')
    assert.deepEqual(ok.clip, { x: 1290, y: 130, w: 92, h: 32 })
    assert.equal(ok.text, '打招呼')
    const clipped = domLocateBySelector('iframe[name=recommendFrame] >>> .half', -1)
    assert.equal(clipped.status, 'offscreen', '按钮下沿超出 iframe 视口(40+622=662),只露 12px 就拒——不能按顶层视口算')
    assert.equal(domLocateBySelector('iframe.none >>> button', -1).status, 'none')
    assert.equal(domLocateBySelector('iframe.cross >>> button', -1).status, 'none', '不同源读不到文档也是 none')
    assert.equal(domLocateBySelector('a >>> b >>> c', -1).status, 'bad_selector', '只支持一层')
    // 命中测试:先问顶层像素上是不是那个 iframe,再减偏移进 iframe 问。
    assert.equal(domHitTestExpected('iframe[name=recommendFrame] >>> button.btn-greet', 0, '打招呼', 1336, 146).onTarget, true)
    topAt = { tagName: 'DIV', textContent: '快捷聊天窗', className: 'chat-global-outer-wrap', parentElement: null, getBoundingClientRect() { return rectOf({ x: 900, y: 100, w: 500, h: 500 }) } }
    const covered = domHitTestExpected('iframe[name=recommendFrame] >>> button.btn-greet', 0, '打招呼', 1336, 146)
    assert.equal(covered.onTarget, false); assert.match(covered.found, /顶层的别的元素/, '顶层浮层盖住 iframe 时在顶层就拒')
    assert.equal(covered.occluded, true, '顶层遮挡要报 occluded,引擎据此退让一次')
    const coveredIdx = domHitTestIndexed('iframe[name=recommendFrame] >>> button.btn-greet', 0, 1336, 146)
    assert.equal(coveredIdx.onTarget, false); assert.match(coveredIdx.found, /遮挡物\(顶层\)/, '滚轮探针把顶层遮挡物签名带出去')
    assert.equal(coveredIdx.occluded, true)
    topAt = frame; innerAt = half
    assert.match(domHitTestExpected('iframe[name=recommendFrame] >>> button.btn-greet', 0, '打招呼', 1336, 146).found, /别的元素/, 'iframe 里落点上是别的元素')
    // 滚动指标:靶子是 iframe 的 html 时读 scrollingElement,不读 html.scrollTop(标准模式恒 0)。
    assert.deepEqual(domReadScrollMetrics('iframe[name=recommendFrame] >>> html', 0), { found: true, scrollTop: 300, scrollHeight: 2814, clientHeight: 622 })
    const doc = domLocateBySelector('iframe[name=recommendFrame] >>> html', -1)
    assert.equal(doc.status, 'ok', '滚过一屏后 html 自身矩形在视口上方,但按内容高度合成后仍可落')
    assert.deepEqual(doc.rect, { x: 168, y: 40 - 300, w: 1302, h: 2814 })
    assert.deepEqual(doc.clip, { x: 168, y: 40, w: 1302, h: 622 }, '可见部分就是整个 iframe 视口')
    assert.equal(domReadScrollMetrics('iframe.none >>> html', 0).found, false)
  } finally { globalThis.document = saved.document; globalThis.window = saved.window }
})

test('只落不点:action=land 走完靠近、落点确认与命中测试就停,收场 landed、一次都不按', async () => {
  const hand = osClickHarness({ calibrated: true })
  try {
    const plan = { ...togglePlan({ onTarget: true, observed: null }), action: 'land' }
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), plan)
    assert.equal(out.outcome, 'landed', out.detail)
    assert.equal(hand.clicks(), 0, '仅移动模式按下去了')
    assert.equal(hand.plays(), 1, '热标定下只靠近一趟')
    assert.match(out.detail, /命中=是/, '命中测试照过,并留在现场')
    const data = osClickContractData('move', out, 1700000000000)
    assert.equal(data.mode, 'move')
    assert.equal(data.outcome, 'landed')
    for (const k of ['attempts', 'unreachableFrames', 'planMs', 'elapsedMs', 'lagMaxUs']) assert.ok(Number.isInteger(data[k]), k)
  } finally { hand.restore() }
  // 命中测试不过时仅移动同样拒——落在别的东西上也不算落到
  const miss = osClickHarness({ calibrated: true })
  try {
    const plan = { ...togglePlan({ onTarget: false, observed: null }), action: 'land' }
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), plan)
    assert.equal(out.outcome, 'refusedByGate')
    assert.equal(miss.clicks(), 0)
  } finally { miss.restore() }
})

test('考古点击的命中测试:落点上是靶子或其后代,且 expectText 给了就核文本——列表重排后同一 index 指到别人时拒', () => {
  const mk = (text, children = []) => {
    const node = { tagName: 'LI', textContent: text, contains(x) { return x === node || children.includes(x) } }
    return node
  }
  const rowA = mk('张先生 · 销售'); const rowB = mk('李女士 · 客服')
  const inner = { tagName: 'SPAN', textContent: '销售' }; rowA.contains = (x) => x === rowA || x === inner
  const saved = globalThis.document
  let rows = [rowA, rowB]
  let atPoint = rowA
  globalThis.document = { querySelectorAll() { return rows }, elementFromPoint() { return atPoint } }
  try {
    const { domHitTestExpected } = bossTestHooks
    assert.equal(domHitTestExpected('.row', 0, null, 1, 1).onTarget, true)
    atPoint = inner
    assert.equal(domHitTestExpected('.row', 0, '张先生 · 销售', 1, 1).onTarget, true, '后代也算命中,文本相符')
    atPoint = rowB
    const off = domHitTestExpected('.row', 0, null, 1, 1)
    assert.equal(off.onTarget, false); assert.match(off.found, /别的元素/)
    // 列表重排:index 0 现在是李女士,expectText 还是张先生
    rows = [rowB, rowA]; atPoint = rowB
    const swapped = domHitTestExpected('.row', 0, '张先生 · 销售', 1, 1)
    assert.equal(swapped.onTarget, false, '同一 index 已指到别人,文本不符就不点')
    assert.match(swapped.found, /文本已变/)
    rows = []
    assert.match(domHitTestExpected('.row', 0, null, 1, 1).found, /不在原来的位置/)
  } finally { globalThis.document = saved }
})

test('BOSS 消息数组就绪判据:空数组不算就绪继续等,身份缺失立即收束', () => {
  const { bossThreadReadSettled } = bossTestHooks
  assert.equal(bossThreadReadSettled({ status: 'ready', rows: [], isToTop: true, peerName: '' }), false,
    '点开瞬间 message-list 先挂空数组,不能当成"这个会话没有消息"')
  assert.equal(bossThreadReadSettled({ status: 'ready', rows: [{ mid: '1' }], isToTop: false, peerName: '宋先生' }), true)
  assert.equal(bossThreadReadSettled({ status: 'identity_missing' }), true, '读不到我方身份等也等不来,立即收束')
  assert.equal(bossThreadReadSettled({ status: 'missing' }), false)
  assert.equal(bossThreadReadSettled({ status: 'binding_mismatch', detail: 'x' }), false)
})

test('BOSS 行定位:按 data-id 唯一命中,带出选中态、气泡与是否在视口内', () => {
  const page = installBossPageFixture({ rows: [
    { dataId: '11-0', selected: true },
    { dataId: '12-0', bubble: '1' },
    { dataId: '13-0', rect: { x: 229, y: 900, width: 339, height: 74, left: 229, top: 900, right: 568, bottom: 974 } },
    { dataId: '14-0' }, { dataId: '14-0' },
  ] })
  try {
    const locate = (ref) => bossTestHooks.domLocateBossRow('.geek-item', ref, 'selected', '.badge-count')
    assert.deepEqual([locate('11-0').count, locate('11-0').selected, locate('11-0').bubble, locate('11-0').inViewport], [1, true, false, true])
    assert.deepEqual([locate('12-0').selected, locate('12-0').bubble, locate('12-0').bubbleText], [false, true, '1'])
    assert.equal(locate('13-0').inViewport, false, '视口外的行点不到——BOSS 上没有滚动注入')
    assert.equal(locate('14-0').count, 2, '重复 data-id 不猜哪一个')
    assert.equal(locate('99-0').count, 0)
  } finally { page.restore() }
})

test('BOSS 发送用换行处理:换成一个空格而不是删掉——脑侧 contentHash 把换行当空白折叠', () => {
  const { newlinesToSpaces } = bossTestHooks
  assert.deepEqual(newlinesToSpaces('你好\n方便聊聊吗'), { text: '你好 方便聊聊吗', removed: 1 })
  assert.deepEqual(newlinesToSpaces('a\r\n\r\nb\rc'), { text: 'a b c', removed: 3 })
  assert.deepEqual(newlinesToSpaces('无换行'), { text: '无换行', removed: 0 })
  // 与脑侧规范化一致:发出去的文本经 NFC/空白折叠/trim 后,哈希输入逐字节相同。
  const norm = (v) => v.normalize('NFC').replace(/ /gu, ' ').replace(/\s+/gu, ' ').trim()
  for (const text of ['你好\n方便聊聊吗', ' 首行 \n\n 次行 ', 'a\r\nb']) {
    assert.equal(norm(newlinesToSpaces(text).text), norm(text), `规范化后必须相同:${JSON.stringify(text)}`)
  }
})

test('BOSS 发送前最后一道闸:落点是发送钮、选中行仍是目标、输入框仍是那句话,三者缺一不点', () => {
  const button = { tagName: 'DIV', textContent: '发送', contains(node) { return node === button } }
  const rows = [
    { getAttribute() { return '11-0' }, classList: { contains(c) { return c === 'selected' } } },
    { getAttribute() { return '12-0' }, classList: { contains() { return false } } },
  ]
  const composer = { textContent: '你好 方便聊聊吗' }
  const saved = globalThis.document
  const install = (overrides = {}) => {
    globalThis.document = {
      querySelectorAll(selector) { return selector === '.submit-content .submit' ? [button] : selector === '.geek-item' ? (overrides.rows ?? rows) : [] },
      elementFromPoint() { return overrides.at === undefined ? button : overrides.at },
      getElementById() { return overrides.composer === undefined ? composer : overrides.composer },
    }
  }
  const gate = () => bossTestHooks.domSendGate('.submit-content .submit', 1, 1, '.geek-item', '11-0', 'selected', 'boss-chat-editor-input', '你好 方便聊聊吗')
  try {
    install()
    assert.deepEqual(gate(), { onTarget: true, found: '发送钮' }, 'nbsp 与空格规范化后相同')
    install({ at: { tagName: 'SPAN', textContent: '换微信', contains() { return false } } })
    assert.equal(gate().onTarget, false, '落点不是发送钮')
    install({ rows: [rows[1], { getAttribute() { return '11-0' }, classList: { contains() { return false } } }] })
    const r = gate(); assert.equal(r.onTarget, false); assert.match(r.found, /选中行/, '真人切走了会话就不点')
    install({ composer: { textContent: '你好 方便聊聊吗 再加一句' } })
    const c = gate(); assert.equal(c.onTarget, false); assert.match(c.found, /输入框内容与文案不同/, '真人追打了字就不点')
    install({ composer: null })
    assert.equal(gate().onTarget, false, '输入框不见了就不点')
  } finally { globalThis.document = saved }
})

test('BOSS 简历摘要:从 conversation$ 拼五分区,标签与智联对齐,空值整行省略,期望与自我评价按事实为空', () => {
  const read = { status: 'ready', name: ' 候选甲 ', ageDesc: '26岁', year: '10年以上', edu: '高中', city: '上海', activeTimeDesc: '刚刚活跃',
    work: [{ company: '甲公司', positionName: '销售', timeDesc: '2023.10-2026.06' }, { company: '', positionName: '', timeDesc: '' }],
    education: [{ school: '某中学', major: '', degree: '高中', timeDesc: '2015-2018' }] }
  const data = bossTestHooks.projectBossResume(read, '11-0', '11', 1788402198000)
  assert.deepEqual(data.basic, [
    { label: '姓名', value: '候选甲' }, { label: '年龄', value: '26岁' }, { label: '工作经验', value: '10年以上' },
    { label: '最高学历', value: '高中' }, { label: '现居地', value: '上海' }, { label: '活跃时间', value: '刚刚活跃' },
  ])
  assert.equal(data.workExperiences, '2023.10-2026.06 甲公司 · 销售', '空段落整段省略')
  assert.equal(data.education, '2015-2018 某中学 · 高中', '空专业不留分隔符')
  assert.deepEqual([data.expectations, data.selfEvaluation], [[], ''], 'BOSS 内存里没有候选人期望与自我评价,不猜')
  assert.deepEqual([data.conversationRef, data.platformUserRef, data.observedAt], ['11-0', '11', 1788402198000])
  const sparse = bossTestHooks.projectBossResume({ ...read, ageDesc: '', year: '', edu: '', city: '', activeTimeDesc: '', work: [], education: [] }, '11-0', '11', 1)
  assert.deepEqual(sparse.basic, [{ label: '姓名', value: '候选甲' }])
  assert.equal(sparse.workExperiences, '')
})

test('BOSS 简历摘要页面读:按形状找当前会话对象,uid 不对就 mismatch,没开会话就 none,职位薪资与性别码不出页面', () => {
  const conversation = { uid: 11, friendSource: 0, name: '候选甲', ageDesc: '26岁', year: '10年以上', edu: '高中', city: '上海', activeTimeDesc: '刚刚活跃',
    workExpList: [{ company: '甲公司', positionName: '销售', timeDesc: '2023.10-2026.06' }], eduExpList: [{ school: '某中学', major: '', degree: '高中', degreeCode: 206, timeDesc: '2015-2018' }],
    toPosition: '销售经理', salaryDesc: '14-28K', gender: 1, token: 'must-not-leak' }
  const page = installBossPageFixture({ instances: [{ 'conversation$': conversation }, { 'conversation$': conversation }] })
  try {
    const read = bossTestHooks.mainReadBossResume(11, 0)
    assert.equal(read.status, 'ready')
    assert.deepEqual(read.work, [{ company: '甲公司', positionName: '销售', timeDesc: '2023.10-2026.06' }])
    assert.deepEqual(read.education, [{ school: '某中学', major: '', degree: '高中', timeDesc: '2015-2018' }])
    const serialized = JSON.stringify(read)
    for (const forbidden of ['must-not-leak', '销售经理', '14-28K', '"gender"']) {
      assert.equal(serialized.includes(forbidden), false, `摘要不该带出:${forbidden}`)
    }
    assert.equal(bossTestHooks.mainReadBossResume(12, 0).status, 'mismatch', '当前会话不是目标就不交')
  } finally { page.restore() }
  const none = installBossPageFixture({ instances: [{ other: 1 }] })
  try { assert.equal(bossTestHooks.mainReadBossResume(11, 0).status, 'none') } finally { none.restore() }
})

test('BOSS 清场白名单:只有真机见过的三处营销位与引导,「不合适」永远不在名单里', () => {
  assert.deepEqual(BOSS_DISMISS_WHITELIST.map((e) => e.container), ['.batch-chat-intention', '.c-menu-bottom-ad', '.guide-intention .dialog-wrap'])
  for (const entry of BOSS_DISMISS_WHITELIST) {
    assert.ok(!/not-fit|operate-btn|toolbar/.test(entry.container + entry.closer), `业务动作不得进清场名单:${entry.label}`)
    assert.ok(entry.closer.startsWith('.'), '关闭控件必须是容器内的选择器')
  }
})

test('BOSS 清场页面读:容器可见且唯一才算在,关闭控件要在视口内;命中测试只认那个关闭控件', () => {
  const rect = (x, y, w, h) => ({ x, y, width: w, height: h, left: x, top: y, right: x + w, bottom: y + h })
  const mk = (r, children = []) => ({ getBoundingClientRect() { return r }, querySelectorAll(sel) { return children.filter((c) => c.sel === sel).map((c) => c.el) }, querySelector(sel) { const hit = children.find((c) => c.sel === sel); return hit ? hit.el : null }, contains(n) { return children.some((c) => c.el === n) } })
  const closer = mk(rect(546, 196, 22, 18))
  const card = mk(rect(229, 196, 339, 74), [{ sel: '.close', el: closer }])
  const offscreenCloser = mk(rect(148, 900, 16, 16))
  const ad = mk(rect(0, 652, 168, 94), [{ sel: '.ad-banner-close', el: offscreenCloser }])
  const saved = { document: globalThis.document, window: globalThis.window, getComputedStyle: globalThis.getComputedStyle }
  globalThis.window = { innerWidth: 1470, innerHeight: 746 }
  globalThis.getComputedStyle = () => ({ display: 'block', visibility: 'visible' })
  globalThis.document = {
    querySelectorAll(sel) { return sel === '.batch-chat-intention' ? [card] : sel === '.c-menu-bottom-ad' ? [ad] : [] },
    elementFromPoint(x, y) { return x === 557 ? closer : { tagName: 'DIV', textContent: '别的' } },
  }
  try {
    const states = bossTestHooks.domReadBossOverlays(BOSS_DISMISS_WHITELIST)
    assert.deepEqual(states.map((s) => [s.label, s.visible, s.closerInViewport]), [
      ['列表顶营销卡', true, true], ['左下客户端下载横幅', true, false], ['右栏意向沟通引导气泡', false, false],
    ])
    assert.deepEqual(states[0].closerRect, { x: 546, y: 196, w: 22, h: 18 })
    assert.equal(bossTestHooks.domHitTestOverlayCloser('.batch-chat-intention', '.close', 557, 205).onTarget, true)
    assert.equal(bossTestHooks.domHitTestOverlayCloser('.batch-chat-intention', '.close', 100, 100).onTarget, false, '落点不在关闭控件上就不点')
    assert.equal(bossTestHooks.domHitTestOverlayCloser('.guide-intention .dialog-wrap', '.iboss-close', 557, 205).onTarget, false, '容器不在就不点')
  } finally { Object.assign(globalThis, saved) }
})

test('BOSS 命中测试被遮时带出遮挡物签名:类名链、尺寸、文本头几个字——白名单靠它长', () => {
  const saved = { document: globalThis.document }
  const cover = { tagName: 'DIV', className: 'dialog-body promo', textContent: '限时优惠 立即领取', parentElement: { tagName: 'DIV', className: 'dialog-wrap active', parentElement: null, getBoundingClientRect() { return { width: 0, height: 0 } } }, getBoundingClientRect() { return { width: 352, height: 144 } } }
  const target = { tagName: 'SPAN', textContent: '未读', contains() { return false } }
  globalThis.document = { body: {}, querySelectorAll() { return [target] }, elementFromPoint() { return cover } }
  try {
    const hit = bossTestHooks.domHitTestIndexed('.chat-message-filter-left span', 0, 10, 10)
    assert.equal(hit.onTarget, false)
    assert.match(hit.found, /^遮挡物 div\.dialog-body\.promo<div\.dialog-wrap\.active\[352x144\]「限时优惠 立即领」$/)
  } finally { Object.assign(globalThis, saved) }
})

// ---------------------------------------------------------------------------
// 第二刀(2026-09-05):推荐页采集 + 打招呼的纯函数与页面函数。页面函数用假 document 跑,
// 判据全是形状(pageList/geekInfo/geek/message prop、class 与 data-geekid),不认组件名;夹具里没有真实候选人。

test('BOSS 职位选择器匹配:整段/职位名/「标题 _ 」前缀三种相等,名字回传标题本身;零命中与多命中如实,不就近', () => {
  const { matchBossJobItem, bossJobItemName, parseBossGeekId, bossFilterGroupName } = bossTestHooks
  const items = [
    { text: '新媒体运营(获客号操盘） _ 上海  15-25K', value: 'ENCJOB0000000000000000000001', current: true },
    { text: '销售经理 _ 北京 8-12K', value: 'ENCJOB0000000000000000000002', current: false },
  ]
  assert.equal(bossJobItemName(items[0].text), '新媒体运营(获客号操盘）')
  assert.equal(bossJobItemName('只有职位名'), '只有职位名')
  assert.deepEqual(matchBossJobItem(items, ' 新媒体运营(获客号操盘） '),
    { status: 'ok', index: 0, name: '新媒体运营(获客号操盘）', value: 'ENCJOB0000000000000000000001', current: true })
  assert.equal(matchBossJobItem(items, '销售经理').current, false)
  assert.deepEqual(matchBossJobItem(items, '新媒体'), { status: 'none', count: 0 }, '前缀不算相等')
  assert.deepEqual(matchBossJobItem([...items, items[1]], '销售经理'), { status: 'ambiguous', count: 2 })
  assert.deepEqual(matchBossJobItem(items, ''), { status: 'none', count: 0 })
  const odd = [{ text: 'A _ B _ 上海 10K', value: 'v', current: true }]
  assert.equal(matchBossJobItem(odd, 'A _ B').status, 'ok', '标题自带「 _ 」:整段以「标题 _ 」开头也算')
  assert.equal(matchBossJobItem(odd, 'A _ B').name, 'A _ B')
  assert.equal(parseBossGeekId('590000001'), 590000001)
  assert.equal(parseBossGeekId('0590'), null); assert.equal(parseBossGeekId('abc'), null); assert.equal(parseBossGeekId(''), null)
  assert.equal(bossFilterGroupName('薪资待遇[单选]'), '薪资待遇'); assert.equal(bossFilterGroupName(' 学历要求 '), '学历要求')
})

test('BOSS 筛选计划:契约枚举 → 面板文案;VIP 锁定组要求不限、MBA 没有对应项都干净失败', () => {
  const { planBossSourcingFilters } = bossTestHooks
  const base = { age: { mode: 'any' }, activeWindow: 'any', gender: 'any', excludeViewed: false, excludeCoworkerContacted: false, careerStatuses: [], educations: [] }
  assert.deepEqual(planBossSourcingFilters(base), { ok: true, plan: { career: [], education: [] } })
  const some = planBossSourcingFilters({ ...base, careerStatuses: ['leftLooking', 'employedOpen', 'leftLooking'], educations: ['bachelor', 'master'] })
  assert.deepEqual(some, { ok: true, plan: { career: ['离职-随时到岗', '在职-考虑机会'], education: ['本科', '硕士'] } }, '重复枚举去重')
  const mba = planBossSourcingFilters({ ...base, educations: ['mbaEmba'] })
  assert.equal(mba.ok, false); assert.match(mba.reason, /mbaEmba/)
  const vip = planBossSourcingFilters({ ...base, activeWindow: 'today', gender: 'female', age: { mode: 'range', minAge: 20, maxAge: 30 }, excludeViewed: true })
  assert.equal(vip.ok, false); assert.match(vip.reason, /年龄\/活跃度\/性别\/近期没有看过/)
})

const filterPanelRead = (overrides = {}) => {
  const group = (name, boxKey, options, vip = false, activeIdx = [0]) =>
    ({ name, boxKey, vip, options: options.map((text, i) => ({ text, active: activeIdx.includes(i), isDefault: i === 0 })) })
  return {
    frame: true, panel: true, masked: true, buttons: ['清除', '确定'],
    groups: [
      group('活跃度[单选]', 'activation', ['不限', '刚刚活跃', '今日活跃'], true),
      group('性别', 'gender', ['不限', '男', '女'], true),
      group('近期没有看过', 'recentNotView', ['不限', '近14天没有'], true),
      group('是否与同事交换简历', 'exchangeResumeWithColleague', ['不限', '近一个月没有'], true),
      group('求职状态', 'intention', ['不限', '离职-随时到岗', '在职-暂不考虑', '在职-考虑机会', '在职-月内到岗'], false, overrides.career ?? [0]),
      group('学历要求', 'degree', ['不限', '初中及以下', '中专/中技', '高中', '大专', '本科', '硕士', '博士'], false, overrides.degree ?? [0]),
      group('经验要求', 'experience', ['不限', '在校/应届', '1-3年'], false, overrides.experience ?? [0]),
      group('薪资待遇[单选]', 'salary', ['不限', '3K以下', '3-5K'], false, overrides.salary ?? [0]),
    ],
  }
}

test('BOSS 筛选差异覆盖:目标空就点不限,目标非空先选后退;经验/薪资恒回不限;全一致零点击;缺项与缺组干净失败', () => {
  const { bossFilterClicks } = bossTestHooks
  const read = filterPanelRead({ degree: [5], experience: [2], career: [2] })
  const out = bossFilterClicks(read, { career: ['离职-随时到岗'], education: [] })
  assert.equal(out.ok, true)
  assert.deepEqual(out.clicks, [
    { boxKey: 'intention', index: 1, text: '离职-随时到岗', expectActive: true },
    { boxKey: 'intention', index: 2, text: '在职-暂不考虑', expectActive: false },
    { boxKey: 'degree', index: 0, text: '不限', expectActive: true },
    { boxKey: 'experience', index: 0, text: '不限', expectActive: true },
  ])
  assert.deepEqual(bossFilterClicks(filterPanelRead(), { career: [], education: [] }).clicks, [], '全不限时零点击')
  assert.deepEqual(bossFilterClicks(filterPanelRead({ degree: [5] }), { career: [], education: ['本科'] }).clicks, [], '已是目标态零点击')
  const missing = bossFilterClicks(filterPanelRead(), { career: [], education: ['MBA'] })
  assert.equal(missing.ok, false); assert.match(missing.reason, /没有「MBA」项/)
  const base = filterPanelRead()
  const noGroup = bossFilterClicks({ ...base, groups: base.groups.filter((g) => g.boxKey !== 'salary') }, { career: [], education: [] })
  assert.equal(noGroup.ok, false); assert.match(noGroup.reason, /薪资待遇/)
})

test('BOSS 筛选回读投影:逐组集合相等就原样回传请求(脑侧 DeepEqual 连顺序也比);不等带出组名与实际;VIP 组缺席视为不限、非不限即拒', () => {
  const { projectBossSourcingFilters } = bossTestHooks
  const requested = { age: { mode: 'any' }, activeWindow: 'any', gender: 'any', excludeViewed: false, excludeCoworkerContacted: false, careerStatuses: ['employedOpen', 'leftLooking'], educations: ['master', 'bachelor'] }
  const read = filterPanelRead({ career: [1, 3], degree: [5, 6] })
  const ok = projectBossSourcingFilters(read, requested)
  assert.equal(ok.ok, true); assert.equal(ok.filters, requested)
  const drift = projectBossSourcingFilters(filterPanelRead({ career: [1], degree: [5, 6] }), requested)
  assert.equal(drift.ok, false); assert.match(drift.reason, /求职状态.*回读为「离职-随时到岗」/)
  const stale = projectBossSourcingFilters(filterPanelRead({ career: [1, 3], degree: [5, 6], salary: [1] }), requested)
  assert.equal(stale.ok, false); assert.match(stale.reason, /薪资待遇.*3K以下/, '真人留下的手工筛选不放过')
  const noVip = projectBossSourcingFilters({ ...read, groups: read.groups.filter((g) => !g.vip) }, requested)
  assert.equal(noVip.ok, true)
  const vipOn = filterPanelRead({ career: [1, 3], degree: [5, 6] })
  vipOn.groups[1].options[2].active = true; vipOn.groups[1].options[0].active = false
  const rejected = projectBossSourcingFilters(vipOn, requested)
  assert.equal(rejected.ok, false); assert.match(rejected.reason, /性别/)
  const unsupported = projectBossSourcingFilters(read, { ...requested, gender: 'male' })
  assert.equal(unsupported.ok, false); assert.match(unsupported.reason, /VIP 锁定/)
})

test('BOSS 职位管理页分区:页签定分区(去「全部」与计数后缀)、行按状态归入、「开放中」投影成契约「在线中」、陌生状态原样另起一区', () => {
  const { bossJobListSections } = bossTestHooks
  const out = bossJobListSections(['全部 · 4', '开放中 · 2', '待开放', '审核未通过', '已关闭 1'], [
    { name: ' 新媒体运营 ', status: '开放中' }, { name: '销售', status: '开放中' }, { name: '旧岗', status: '已关闭' }, { name: '怪岗', status: '神秘状态' },
  ])
  assert.equal(out.ok, true)
  assert.deepEqual(out.sections, [
    { label: '在线中', names: ['新媒体运营', '销售'] }, { label: '待开放', names: [] }, { label: '审核未通过', names: [] },
    { label: '已关闭', names: ['旧岗'] }, { label: '神秘状态', names: ['怪岗'] },
  ])
  assert.deepEqual(out.extraLabels, ['神秘状态'], '陌生状态单独带出,给日志留痕用')
  assert.deepEqual(bossJobListSections(['开放中'], [{ name: 'a', status: '开放中' }]).extraLabels, [])
  assert.equal(bossJobListSections(['全部'], []).ok, false, '只有「全部」不算有分区')
  assert.match(bossJobListSections(['开放中'], [{ name: 'x', status: '' }]).reason, /读不到状态/)
  assert.match(bossJobListSections(['开放中'], [{ name: '', status: '开放中' }]).reason, /读不到职位名/)
  assert.match(bossJobListSections(['开放中', '开放中'], []).reason, /重名/)
})

const recommendCard = (over = {}) => ({
  geekId: 590000001, geekSource: 0, encryptGeekId: 'ENCGEEK00000000000000000001', encryptJobId: 'ENCJOB0000000000000000000001',
  isFriend: 0, buttonText: '打招呼', visible: true,
  name: '候选甲', ageDesc: '28岁', degree: '本科', workYear: '5年', salary: '15-20K', activeTimeDesc: '刚刚活跃', desc: '  擅长  短视频 ',
  edus: [{ school: '某大学', major: '新闻', degree: '本科', start: '2014', end: '2018' }],
  works: [{ company: '某公司', position: '运营', start: '2018.07', end: '至今', responsibility: '负责账号' }, { company: '', position: '', start: '', end: '', responsibility: '' }],
  expect: { location: '上海', position: '新媒体运营', salary: '15-20' },
  ...over,
})

test('BOSS 卡片摘要投影:五分区、标签与智联对齐、空值整行省略;关系态按 isFriend/按钮文案三态', () => {
  const { projectBossSourcingResume, bossCardContactState } = bossTestHooks
  const data = projectBossSourcingResume(recommendCard(), 'ENCJOB0000000000000000000001', '新媒体运营', 1700000000000)
  assert.equal(data.platformUserRef, '590000001'); assert.equal(data.displayName, '候选甲'); assert.equal(data.contactState, 'unestablished')
  assert.deepEqual(data.basic, [
    { label: '姓名', value: '候选甲' }, { label: '年龄', value: '28岁' }, { label: '工作经验', value: '5年' },
    { label: '最高学历', value: '本科' }, { label: '活跃时间', value: '刚刚活跃' },
  ])
  assert.deepEqual(data.expectations, [{ label: '期望职位', value: '新媒体运营' }, { label: '期望城市', value: '上海' }, { label: '期望薪资', value: '15-20K' }])
  assert.equal(data.selfEvaluation, '擅长 短视频')
  assert.equal(data.education, '2014-2018 某大学 · 新闻 · 本科')
  assert.equal(data.workExperiences, '2018.07-至今 某公司 · 运营\n负责账号', '空经历整条省略')
  assert.equal(data.positionTitle, '新媒体运营'); assert.equal(data.positionRef, 'ENCJOB0000000000000000000001'); assert.equal(data.observedAt, 1700000000000)
  assert.equal(bossCardContactState({ isFriend: 1, buttonText: '打招呼' }), 'established', 'isFriend 翻了就算,按钮渲染晚一拍')
  assert.equal(bossCardContactState({ isFriend: 0, buttonText: '继续沟通' }), 'established')
  assert.equal(bossCardContactState({ isFriend: 0, buttonText: '' }), 'unknown')
  assert.equal(bossCardContactState({ isFriend: null, buttonText: '打招呼' }), 'unknown')
  const empty = projectBossSourcingResume(recommendCard({ name: '', edus: [], works: [], desc: '', expect: { location: '', position: '', salary: '' }, salary: '' }), 'j', null, 1)
  assert.equal(empty.displayName, null); assert.equal(empty.education, ''); assert.deepEqual(empty.expectations, []); assert.equal(empty.positionTitle, null)
})

function installBossRecommendFixture({ cards = [], loading = false, finished = false, jobItems = [], scroll = { top: 0, height: 2814, client: 622 }, misalignIndex = -1, noList = false, quick = null, user = 9 } = {}) {
  const saved = { document: globalThis.document, window: globalThis.window }
  const infos = cards.map((c) => ({
    geekId: c.geekId, geekSource: c.geekSource ?? 0, encryptGeekId: c.enc, encryptJobId: c.job, isFriend: c.isFriend ?? 0,
    geekName: c.name ?? '', ageDesc: '28岁', geekDegree: '本科', geekWorkYear: '5年', salary: '15-20K', activeTimeDesc: '刚刚活跃',
    geekDesc: { content: c.desc ?? '' }, geekEdus: c.edus ?? [], geekWorks: c.works ?? [],
    viewExpect: { location: '上海', position: '运营', lowSalary: 15, highSalary: 20 },
  }))
  const items = cards.map((c, i) => ({
    querySelector(sel) {
      if (sel === '.card-inner') return { getAttribute: (n) => (n === 'data-geekid' ? (i === misalignIndex ? 'WRONG' : c.enc) : null) }
      if (sel === '.button-chat-wrap button') return { textContent: ` ${c.button ?? '打招呼'} ` }
      return null
    },
    getBoundingClientRect() { return c.rect ?? { top: i * 190, bottom: i * 190 + 180 } },
  }))
  const jobEls = jobItems.map((j) => ({ textContent: j.text, getAttribute: (n) => (n === 'value' ? j.value : null), classList: { contains: (cls) => cls === 'curr' && j.current } }))
  const inner = {
    scrollingElement: { clientHeight: scroll.client, scrollTop: scroll.top, scrollHeight: scroll.height },
    documentElement: { clientHeight: scroll.client },
    querySelector(sel) {
      if (sel === 'ul.card-list') return noList ? null : { __vue__: { pageList: infos } }
      if (sel === '#recommend-list') return { __vue__: { loading, finished } }
      return null
    },
    querySelectorAll(sel) { return sel === 'li.card-item' ? items : sel === '.job-selecter-wrap .job-item' ? jobEls : [] },
  }
  const frame = { contentDocument: inner }
  const topEls = [{ __vue__: user ? { user$: { userId: user } } : { other: 1 } }]
  globalThis.window = { innerWidth: 1470, innerHeight: 746 }
  globalThis.document = {
    querySelector(sel) { return sel === 'iframe[name=recommendFrame]' ? frame : sel === '.chat-global-conversation' ? quick : null },
    querySelectorAll(sel) { return sel === '*' ? topEls : [] },
    getElementById() { return null },
  }
  return { restore() { Object.assign(globalThis, saved) }, infos }
}

const quickWindow = (geek, messages) => {
  const rowEls = messages.map((m) => ({ __vue__: { $props: { message: m } } }))
  return {
    querySelector(sel) { return sel === '.chat-global-msg-content' ? { __vue__: { geek } } : null },
    querySelectorAll(sel) { return sel === '*' ? [{ __vue__: { other: 1 } }, ...rowEls, ...rowEls.slice(0, 1)] : [] },
  }
}

test('BOSS 推荐窗口页面读:pageList 与 DOM 对齐才就绪、可见性按 iframe 视口、当前职位取 .curr 的 value;没 iframe/没列表如实', () => {
  const { mainReadBossRecommendWindow, RECOMMEND_SEL: S } = bossTestHooks
  const call = () => mainReadBossRecommendWindow('iframe[name=recommendFrame]', S.listView, S.cardList, S.cardItem, S.cardInner, S.cardButton, S.jobItem, S.jobItemCurrentClass)
  const cards = [
    { geekId: 11, enc: 'E11', job: 'J1' },
    { geekId: 12, enc: 'E12', job: 'J1', isFriend: 1, button: '继续沟通' },
    { geekId: 13, enc: 'E13', job: 'J1', rect: { top: 700, bottom: 880 } },
  ]
  const jobItems = [{ text: ' 新媒体运营 _ 上海  15-25K ', value: 'J1', current: true }]
  const page = installBossRecommendFixture({ cards, jobItems, scroll: { top: 300, height: 2814, client: 622 } })
  try {
    const read = call()
    assert.equal(read.status, 'ready'); assert.equal(read.aligned, true); assert.equal(read.loading, false); assert.equal(read.finished, false)
    assert.equal(read.positionRef, 'J1'); assert.equal(read.positionText, '新媒体运营 _ 上海 15-25K')
    assert.deepEqual(read.cards.map((c) => [c.geekId, c.isFriend, c.buttonText, c.visible, c.encryptGeekId, c.encryptJobId]),
      [[11, 0, '打招呼', true, 'E11', 'J1'], [12, 1, '继续沟通', true, 'E12', 'J1'], [13, 0, '打招呼', false, 'E13', 'J1']])
    assert.equal(read.domCount, 3); assert.equal(read.scrollTop, 300); assert.equal(read.scrollHeight, 2814); assert.equal(read.clientHeight, 622)
    assert.deepEqual(read.jobItems, [{ text: '新媒体运营 _ 上海 15-25K', value: 'J1', current: true }])
  } finally { page.restore() }
  const misaligned = installBossRecommendFixture({ cards, jobItems, misalignIndex: 1 })
  try { assert.equal(call().aligned, false, '刚翻页的卡 vm 晚挂,data-geekid 对不上就是没就绪') } finally { misaligned.restore() }
  const twoCurrent = installBossRecommendFixture({ cards, jobItems: [...jobItems, { text: 'x _ y', value: 'J2', current: true }] })
  try { assert.equal(call().positionRef, '', '两个 .curr 不猜') } finally { twoCurrent.restore() }
  const noList = installBossRecommendFixture({ cards, jobItems, noList: true })
  try { assert.equal(call().status, 'no_list') } finally { noList.restore() }
  const saved = { document: globalThis.document }
  globalThis.document = { querySelector() { return null } }
  try { assert.equal(call().status, 'no_frame') } finally { Object.assign(globalThis, saved) }
})

test('BOSS 目标卡页面读:按 geekId 唯一匹配、DOM 卡对齐才交、摘要字段从 geekInfo 取;不在/重复如实', () => {
  const { mainReadBossRecommendTarget, RECOMMEND_SEL: S } = bossTestHooks
  const call = (id) => mainReadBossRecommendTarget('iframe[name=recommendFrame]', S.cardList, S.cardItem, S.cardInner, S.cardButton, S.jobItem, S.jobItemCurrentClass, id)
  const cards = [
    { geekId: 11, enc: 'E11', job: 'J1', name: '候选甲', desc: '简介',
      edus: [{ school: 'U', major: 'M', degreeName: '本科', startDate: 2014, endDate: 2018 }],
      works: [{ company: 'C', positionName: 'P', startDate: '2018', endDate: '至今', responsibility: 'R' }] },
    { geekId: 12, enc: 'E12', job: 'J1' }, { geekId: 12, enc: 'E12b', job: 'J1' },
  ]
  const page = installBossRecommendFixture({ cards, jobItems: [{ text: '新媒体运营 _ 上海 15-25K', value: 'J1', current: true }] })
  try {
    const read = call(11)
    assert.equal(read.status, 'ready'); assert.equal(read.positionRef, 'J1'); assert.equal(read.positionText, '新媒体运营 _ 上海 15-25K')
    assert.equal(read.card.name, '候选甲'); assert.equal(read.card.desc, '简介'); assert.equal(read.card.buttonText, '打招呼'); assert.equal(read.card.isFriend, 0)
    assert.deepEqual(read.card.edus, [{ school: 'U', major: 'M', degree: '本科', start: '2014', end: '2018' }], '数字日期转成字符串')
    assert.deepEqual(read.card.works, [{ company: 'C', position: 'P', start: '2018', end: '至今', responsibility: 'R' }])
    assert.deepEqual(read.card.expect, { location: '上海', position: '运营', salary: '15-20' })
    assert.equal(call(99).status, 'absent')
    assert.deepEqual(call(12), { status: 'duplicated', count: 2 })
  } finally { page.restore() }
  const misaligned = installBossRecommendFixture({ cards, misalignIndex: 0 })
  try { assert.equal(call(11).status, 'absent', 'DOM 卡与 pageList 对不上不交') } finally { misaligned.restore() }
})

test('BOSS 快捷窗页面读:geek 绑定谁、消息从各 message-component 的 message prop 收并按 mid 排序去重、方向首选 isSelf、退回 fromId==我方($parent 上行找 user$)、都没有归 system;没窗/没 geek 如实', () => {
  const { mainReadBossQuickChat, QUICK_CHAT_SEL: Q } = bossTestHooks
  const msgs = [
    { mid: 200, body: { text: '你好', type: 1 }, bizType: 101, fromId: 9, isSelf: true, time: 1700000000500, status: 1 },
    { mid: 100, body: { type: 9 }, bizType: 21050004, fromId: 590000001, isSelf: false, time: 1700000000000, status: 2 },
    { mid: 300, body: { text: '旧形态', type: 1 }, bizType: 101, fromId: 9, time: 1700000000600, status: 1 },
    { mid: 400, body: { text: '谁发的', type: 1 }, bizType: 101, fromId: 77, isSelf: false, time: 1700000000700, status: 1 },
  ]
  const page = installBossRecommendFixture({ quick: quickWindow({ uid: 590000001, friendSource: 0, encryptUid: 'ENC' }, msgs) })
  try {
    const read = mainReadBossQuickChat(Q.window, Q.messageList)
    assert.equal(read.status, 'ready'); assert.equal(read.uid, 590000001); assert.equal(read.friendSource, 0); assert.equal(read.encryptUid, 'ENC')
    assert.deepEqual(read.rows.map((r) => [r.mid, r.direction, r.text, r.status, r.bizType]),
      [['100', 'in', '', 2, 21050004], ['200', 'out', '你好', 1, 101], ['300', 'out', '旧形态', 1, 101], ['400', 'system', '谁发的', 1, 101]],
      'isSelf 优先;没 isSelf 时退回 fromId==我方(夹具顶层元素自持 user$);isSelf=false 且 fromId 不是对方归 system')
  } finally { page.restore() }
  const closed = installBossRecommendFixture({})
  try { assert.deepEqual(mainReadBossQuickChat(Q.window, Q.messageList), { status: 'closed' }) } finally { closed.restore() }
  const noGeek = installBossRecommendFixture({ quick: quickWindow(null, []) })
  try { assert.deepEqual(mainReadBossQuickChat(Q.window, Q.messageList), { status: 'no_geek' }) } finally { noGeek.restore() }
  // 顶层元素自身没有 user$、要沿 $parent 上行才有(推荐页真机形态,§十七):没 isSelf 的行仍能定方向。
  const parentUser = installBossRecommendFixture({ quick: quickWindow({ uid: 1, friendSource: 0 }, [{ mid: 5, body: { text: 'x', type: 1 }, bizType: 101, fromId: 9, time: 1, status: 1 }]), user: 0 })
  globalThis.document.querySelectorAll = (sel) => (sel === '*' ? [{ __vue__: { $parent: { $parent: { user$: { userId: 9 } } } } }] : [])
  try { assert.equal(mainReadBossQuickChat(Q.window, Q.messageList).rows[0].direction, 'out') } finally { parentUser.restore() }
  const nobody = installBossRecommendFixture({ quick: quickWindow({ uid: 1, friendSource: 0 }, [{ mid: 5, body: { text: 'x', type: 1 }, bizType: 101, fromId: 9, time: 1, status: 1 }]), user: 0 })
  try {
    const read = mainReadBossQuickChat(Q.window, Q.messageList)
    assert.equal(read.status, 'ready'); assert.equal(read.rows[0].direction, 'system', '既没 isSelf 又找不到我方身份,永不猜成 out——正证读不到只会 possible')
  } finally { nobody.restore() }
})

test('BOSS 筛选面板页面读:两块按 vip-filters 分、组名/组键/选项选中态、遮罩与按钮;面板不在或 iframe 不在如实', () => {
  const { domReadBossFilterPanel, RECOMMEND_SEL: S } = bossTestHooks
  const call = () => domReadBossFilterPanel('iframe[name=recommendFrame]', S.filterPanel, S.filterBlock, S.filterVipBlockClass, S.filterGroup, S.filterGroupName,
    S.filterBox, S.filterOption, S.filterOptionActiveClass, S.filterOptionDefaultClass, S.vipMask, S.filterButton)
  const opt = (text, classes) => ({ textContent: ` ${text} `, classList: { contains: (c) => classes.includes(c) } })
  const group = (name, key, options) => ({
    querySelector: (sel) => sel === '.name' ? { textContent: name } : sel === '.check-box' ? { classList: ['check-box', key] } : null,
    querySelectorAll: (sel) => sel === '.option' ? options : [],
  })
  const blocks = [
    { classList: { contains: (c) => c === 'vip-filters' }, querySelectorAll: (sel) => sel === '.filter-wrap' ? [group('性别', 'gender', [opt('不限', ['default', 'active']), opt('男', []), opt('女', [])])] : [] },
    { classList: { contains: () => false }, querySelectorAll: (sel) => sel === '.filter-wrap' ? [group('学历要求', 'degree', [opt('不限', ['default']), opt('本科', ['active'])])] : [] },
  ]
  const inner = {
    querySelector: (sel) => sel === '.filter-panel' ? {} : sel === '.vip-mask' ? { getBoundingClientRect: () => ({ width: 861, height: 298 }) } : null,
    querySelectorAll: (sel) => sel === '.filter-panel .filters-wrap' ? blocks : sel === '.filter-panel .btns .btn' ? [{ textContent: '清除' }, { textContent: ' 确定 ' }] : [],
  }
  const saved = { document: globalThis.document }
  globalThis.document = { querySelector: (sel) => sel === 'iframe[name=recommendFrame]' ? { contentDocument: inner } : null }
  try {
    const read = call()
    assert.equal(read.frame, true); assert.equal(read.panel, true); assert.equal(read.masked, true); assert.deepEqual(read.buttons, ['清除', '确定'])
    assert.deepEqual(read.groups, [
      { name: '性别', boxKey: 'gender', vip: true, options: [{ text: '不限', active: true, isDefault: true }, { text: '男', active: false, isDefault: false }, { text: '女', active: false, isDefault: false }] },
      { name: '学历要求', boxKey: 'degree', vip: false, options: [{ text: '不限', active: false, isDefault: true }, { text: '本科', active: true, isDefault: false }] },
    ])
    inner.querySelector = () => null
    assert.deepEqual(call(), { frame: true, panel: false, masked: false, groups: [], buttons: [] })
    globalThis.document = { querySelector: () => null }
    assert.equal(call().frame, false)
  } finally { Object.assign(globalThis, saved) }
})

test('BOSS 职位管理页页面读:按 src 找唯一 iframe、页签与行、页脚「共 N 个职位」;iframe 不唯一或不在如实', () => {
  const { domReadBossJobList } = bossTestHooks
  const args = ['/web/frame/job_v2/list', '.tab-item', 'li.job-item-container', '.job-name', '.status-box']
  const row = (name, status) => ({ querySelector: (sel) => sel === '.job-name' ? { textContent: ` ${name} ` } : sel === '.status-box' ? { textContent: status } : null })
  const inner = {
    body: { innerText: '全部 开放中 ... 共 2 个职位' },
    querySelectorAll: (sel) => sel === '.tab-item' ? [{ textContent: '全部' }, { textContent: ' 开放中 ' }] : sel === 'li.job-item-container' ? [row('A', '开放中'), row('B', '已关闭')] : [],
  }
  const frame = { getAttribute: (n) => n === 'src' ? 'https://www.zhipin.com/web/frame/job_v2/list?x=1' : null, contentDocument: inner }
  const saved = { document: globalThis.document }
  globalThis.document = { querySelectorAll: (sel) => sel === 'iframe' ? [frame, { getAttribute: () => '/other', contentDocument: {} }] : [] }
  try {
    assert.deepEqual(domReadBossJobList(...args), { frame: true, tabs: ['全部', '开放中'], rows: [{ name: 'A', status: '开放中' }, { name: 'B', status: '已关闭' }], total: 2 })
    inner.body.innerText = '没有页脚'
    assert.equal(domReadBossJobList(...args).total, null)
    globalThis.document = { querySelectorAll: () => [frame, frame] }
    assert.equal(domReadBossJobList(...args).frame, false, '两个同 src 的 iframe 不猜')
  } finally { Object.assign(globalThis, saved) }
})

test('BOSS 快捷窗发送闸与外壳:落点在唯一发送钮且编辑器文本规范化后等于文案才放行;关闭键唯一才给矩形', () => {
  const { domBossQuickSendGate, domReadBossQuickChatShell, QUICK_CHAT_SEL: Q } = bossTestHooks
  const button = { contains: (n) => n === button }
  const composer = { textContent: '你好  方便聊聊吗 ' }
  const saved = { document: globalThis.document }
  globalThis.document = {
    querySelectorAll: (sel) => sel === Q.sendButton ? [button] : sel === Q.close ? [{ getBoundingClientRect: () => ({ x: 1, y: 2, width: 24, height: 24 }) }] : [],
    elementFromPoint: () => button,
    getElementById: (id) => (id === Q.composerId ? composer : null),
    querySelector: (sel) => (sel === Q.window ? {} : null),
  }
  try {
    assert.deepEqual(domBossQuickSendGate(Q.sendButton, 1, 1, Q.composerId, '你好 方便聊聊吗'), { onTarget: true, found: '发送钮' })
    const wrong = domBossQuickSendGate(Q.sendButton, 1, 1, Q.composerId, '别的话')
    assert.equal(wrong.onTarget, false); assert.match(wrong.found, /输入框内容与文案不同/)
    assert.deepEqual(domReadBossQuickChatShell(Q.window, Q.close), { open: true, closeCount: 1, closeRect: { x: 1, y: 2, w: 24, h: 24 } })
    globalThis.document.elementFromPoint = () => ({ tagName: 'DIV', textContent: '别的' })
    assert.match(domBossQuickSendGate(Q.sendButton, 1, 1, Q.composerId, '你好 方便聊聊吗').found, /落点上是 div/)
    globalThis.document.querySelector = () => null
    assert.equal(domReadBossQuickChatShell(Q.window, Q.close).open, false)
  } finally { Object.assign(globalThis, saved) }
})

test('靶子被盖住就退让一次再靠近:命中测试报 occluded 且计划带退让点时,先落到空白处等弹层收回,再从那里靠近同一个瞄点;仍盖着就收场,一下都不点', async () => {
  const retreat = { rect: { x: 176, y: 160, w: 40, h: 300 }, async hitTest() { return { onTarget: true, found: '空白 div' } } }
  // 第一次靠近被顶层弹层盖住,退让后第二次命中。
  const hand = osClickHarness({ calibrated: true })
  try {
    let calls = 0
    const plan = {
      ...togglePlan({ onTarget: true, observed: { trusted: true, onTarget: true, eventDriftPx: 0, after: '切了' } }),
      async hitTest() { calls += 1; return calls === 1 ? { onTarget: false, occluded: true, found: '落点上是顶层的别的元素 a「王依琳」' } : { onTarget: true, found: '靶子(div)' } },
      retreat,
    }
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), plan)
    assert.equal(out.outcome, 'clicked', out.detail)
    assert.equal(hand.clicks(), 1)
    assert.equal(hand.plays(), 3, '靠近、退让、再靠近各一趟')
    assert.match(out.detail, /退让 靶\(\d+,\d+\) 落\(\d+,\d+\) 空白=是/)
    const [, away] = hand.playTargets()
    assert.ok(away[0] >= 176 && away[0] <= 216 && away[1] >= 160 && away[1] <= 460, `退让要落在空白带里,实际 (${away})`)
  } finally { hand.restore() }
  // 一直盖着:退让后再靠近一次仍拒,三次靠近收场,零点击。
  const stuck = osClickHarness({ calibrated: true })
  try {
    const plan = {
      ...togglePlan({ onTarget: false, observed: null }),
      async hitTest() { return { onTarget: false, occluded: true, found: '遮挡物(顶层) div.dialog' } },
      retreat,
    }
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), plan)
    assert.equal(out.outcome, 'refusedByGate')
    assert.equal(stuck.clicks(), 0)
    assert.equal(stuck.plays(), 4, '两次靠近 + 一次退让 + 退让后一次靠近')
    assert.match(out.detail, /3 次靠近\(含退让后一次\)都没过闸/)
  } finally { stuck.restore() }
  // 没报 occluded:照旧连拒两次,不多走一步。
  const plain = osClickHarness({ calibrated: true })
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(),
      { ...togglePlan({ onTarget: false, observed: null }), retreat })
    assert.equal(out.outcome, 'refusedByGate'); assert.equal(plain.plays(), 2, '没报 occluded 就不退让')
    assert.match(out.detail, /2 次靠近都没过闸/)
  } finally { plain.restore() }
  // 退让落点不在空白处(比如落到了卡片上):不再靠近,如实收场。
  const bad = osClickHarness({ calibrated: true })
  try {
    const plan = {
      ...togglePlan({ onTarget: false, observed: null }),
      async hitTest() { return { onTarget: false, occluded: true, found: '遮挡物(顶层) div.dialog' } },
      retreat: { ...retreat, async hitTest() { return { onTarget: false, found: '落在 li.card-item 上,不是空白处' } } },
    }
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), plan)
    assert.equal(out.outcome, 'refusedByGate'); assert.equal(bad.plays(), 2, '靠近一次 + 退让一次,退让没落到空白处就停')
    assert.match(out.detail, /退让未成\(落在 li\.card-item 上/)
  } finally { bad.restore() }
})

test('BOSS 招呼钮选择器:身份绑在 li 上再往下找按钮——按钮在 .operate-side 里,是 .card-inner 的兄弟不是后代(第三趟真机三次首击全 none)', () => {
  const { greetButtonSelector, RECOMMEND_SEL: S } = bossTestHooks
  const sel = greetButtonSelector('ENCGEEK00000000000000000001', S.greetButton)
  assert.equal(sel, 'li.card-item:has(.card-inner[data-geekid="ENCGEEK00000000000000000001"]) button.btn-greet')
  assert.equal(greetButtonSelector('E', S.continueButton), 'li.card-item:has(.card-inner[data-geekid="E"]) button.btn-continue')
  // 用真实 DOM 形状验一遍选择器语义(jsdom 没有,用 node 里的最小假 querySelectorAll 说明意图):li 里有 .card-inner 与兄弟 .operate-side
  assert.ok(!sel.startsWith('.card-inner'), '不能再从 .card-inner 往下找')
})

test('BOSS 停靠空白带:iframe 左侧边距里的一条竖带,上下各留 120px;闸只认顶层是 iframe 且 iframe 内落点不在卡片/页头/面板/可点元素上', () => {
  const { domReadBossParkSpot, domBossParkGate } = bossTestHooks
  const saved = { document: globalThis.document, window: globalThis.window }
  const busyLi = { tagName: 'LI', className: 'card-item' }
  let innerAt = { tagName: 'DIV', className: '', closest: () => null }
  const inner = { elementFromPoint: () => innerAt }
  const frame = { contentDocument: inner, clientLeft: 0, clientTop: 0, getBoundingClientRect: () => ({ left: 168, top: 40, right: 1470, bottom: 662, width: 1302, height: 622 }) }
  let topAt = frame
  globalThis.window = { innerWidth: 1470, innerHeight: 662 }
  globalThis.document = { querySelector: (sel) => (sel === 'iframe[name=recommendFrame]' ? frame : null), elementFromPoint: () => topAt }
  try {
    assert.deepEqual(domReadBossParkSpot('iframe[name=recommendFrame]'), { found: true, rect: { x: 176, y: 160, w: 40, h: 382 } })
    assert.deepEqual(domBossParkGate('iframe[name=recommendFrame]', 190, 300), { onTarget: true, found: '空白 div' })
    innerAt = { tagName: 'DIV', className: 'name-wrap', closest: (sel) => (sel.includes('li.card-item') ? busyLi : null) }
    assert.match(domBossParkGate('iframe[name=recommendFrame]', 190, 300).found, /落在 li\.card-item 上/)
    innerAt = { tagName: 'DIV', className: '', closest: () => null }
    topAt = { tagName: 'DIV' }
    assert.match(domBossParkGate('iframe[name=recommendFrame]', 190, 300).found, /顶层是 div/)
    globalThis.window = { innerWidth: 1470, innerHeight: 300 }
    assert.equal(domReadBossParkSpot('iframe[name=recommendFrame]').found, true, '视口矮也还有 60px 以上就给')
    globalThis.window = { innerWidth: 1470, innerHeight: 200 }
    assert.equal(domReadBossParkSpot('iframe[name=recommendFrame]').found, false, '不足 60px 的带子不给,别硬凑')
    globalThis.document = { querySelector: () => null }
    assert.equal(domReadBossParkSpot('iframe[name=recommendFrame]').found, false)
    assert.equal(domBossParkGate('iframe[name=recommendFrame]', 1, 1).onTarget, false)
  } finally { Object.assign(globalThis, saved) }
})

test('OS 注入的观测器装在 isolated world:落点/点击观测的每一次注入都不进 MAIN', async () => {
  const hand = osClickHarness({})
  const worlds = []
  const original = globalThis.chrome.scripting.executeScript
  globalThis.chrome.scripting.executeScript = async (request) => {
    worlds.push([request.func.name, request.world])
    return original(request)
  }
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx())
    assert.equal(out.outcome, 'landed')
    assert.ok(worlds.length >= 2)
    for (const [name, world] of worlds) {
      assert.equal(world, 'ISOLATED', `${name} 注入到了 ${world}:观测器挂在 MAIN 的 window 上会被 getOwnPropertyNames 看见`)
    }
  } finally { hand.restore() }
})

test('装上第二个平台之后,不带 context 的 probe.platform 一律被拒 —— 而账号绑定正走这条路', async () => {
  // 拒绝本身是对的:ProbePlatformData 没有平台身份字段,脑既无从指定探哪个、
  // 也无从从回包分辨探到了哪个,猜一个顶上就是错靶的开始(见 registry.ts)。
  //
  // 但脑侧 /admin/accounts/bind 与 suspect 现场取证**恰恰**是不带 context 派发的。
  // 所以"注册第二个平台"这一步会当场打掉账号绑定 —— 包括智联自己的。
  // 这条用例把那个后果钉在这里,免得下一个人以为只要写完适配器就能跑。
  const probeCapability = {
    probePlatform: () => Promise.resolve({
      pageKind: 'im', contentScriptOk: true, loginState: 'in',
      principalFingerprint: 'fp-fixture', surface: null,
    }),
  }
  const one = fakePlatform('platform-solo', probeCapability)
  const two = fakePlatform('platform-second', probeCapability)
  const unbound = () => command(Primitive.ProbePlatform, {})

  await withPlatforms([one], async () => {
    registerM2Primitives()
    const out = recorder()
    const dispatcher = new Dispatcher(out.send)
    await dispatcher.handleCmd('probe-solo', 's', 's', unbound())
    await eventually(() => results(out.frames, 'probe-solo').length === 1, '单平台 probe 未收束')
    assert.equal(results(out.frames, 'probe-solo')[0].body.status, 'ok',
      '只有一个平台时,不带 context 的 probe 照旧能跑 —— 这是绑定第一个账号的唯一入口')
  })

  await withPlatforms([one, two], async () => {
    registerM2Primitives()
    const out = recorder()
    const dispatcher = new Dispatcher(out.send)
    await dispatcher.handleCmd('probe-two', 's', 's', unbound())
    await eventually(() => results(out.frames, 'probe-two').length === 1, '双平台 probe 未收束')
    const body = results(out.frames, 'probe-two')[0].body
    assert.equal(body.status, 'failed')
    assert.equal(body.error.code, ErrorCode.CtxNotReady)
    assert.match(body.error.message, /注册了 2 个平台/)
  })
})


test('args.platform 解开死结:双平台下脑说探谁就探谁,说不出或说岔了一律拒', async () => {
  // 2026-08-30 加的可选字段(probe.platform / debug.capturePage 各一个)。
  // 它让脑**说出来**要探哪个,而不是让手猜 —— 猜一个顶上就是错靶的开始。
  const tagged = (id) => ({
    probePlatform: () => Promise.resolve({
      pageKind: 'im', contentScriptOk: true, loginState: 'in',
      principalFingerprint: `fp-${id}`, surface: null,
    }),
  })
  const one = fakePlatform('platform-solo', tagged('solo'))
  const two = fakePlatform('platform-second', tagged('second'))
  const probeCmd = (args, overrides = {}) => command(Primitive.ProbePlatform, args, overrides)

  await withPlatforms([one, two], async () => {
    registerM2Primitives()
    const out = recorder()
    const dispatcher = new Dispatcher(out.send)

    // 说了探谁:照它路由,而且回来的确实是那个平台的数据。
    await dispatcher.handleCmd('args-hit', 's', 's', probeCmd({ platform: 'platform-second' }))
    await eventually(() => results(out.frames, 'args-hit').length === 1, 'args 路由未收束')
    assert.equal(results(out.frames, 'args-hit')[0].body.status, 'ok')
    assert.equal(results(out.frames, 'args-hit')[0].body.data.principalFingerprint, 'fp-second',
      '路由到了别的平台 —— 这正是错靶的形状')

    // 说了一个没注册的:拒绝,不退化成"随便挑一个"。
    await dispatcher.handleCmd('args-miss', 's', 's', probeCmd({ platform: 'platform-nope' }))
    await eventually(() => results(out.frames, 'args-miss').length === 1, '未注册平台未收束')
    assert.equal(results(out.frames, 'args-miss')[0].body.status, 'failed')
    assert.match(results(out.frames, 'args-miss')[0].body.error.message, /未注册平台 platform-nope/)

    // context 与 args 打架:拒绝。这两处恰恰决定动作落在谁的页面上,
    // 挑一个信等于替脑做决定。
    await dispatcher.handleCmd('args-clash', 's', 's', probeCmd({ platform: 'platform-second' }, {
      context: { platform: 'platform-solo', accountRef: 'account-clash' },
    }))
    await eventually(() => results(out.frames, 'args-clash').length === 1, '矛盾命令未收束')
    assert.equal(results(out.frames, 'args-clash')[0].body.status, 'failed')
    assert.match(results(out.frames, 'args-clash')[0].body.error.message, /自相矛盾/)

    // 两者一致:照跑,context 优先不改变结果。
    await dispatcher.handleCmd('args-agree', 's', 's', probeCmd({ platform: 'platform-solo' }, {
      context: { platform: 'platform-solo', accountRef: 'account-agree' },
    }))
    await eventually(() => results(out.frames, 'args-agree').length === 1, '一致命令未收束')
    assert.equal(results(out.frames, 'args-agree')[0].body.status, 'ok')
    assert.equal(results(out.frames, 'args-agree')[0].body.data.principalFingerprint, 'fp-solo')
  })
})


// ——— OS 注入的点击闸(2026-08-30) ———

/**
 * 假手服务 + 假页面。落点恒等于最后一次 /play 的终点(标定完美),于是
 * 判据只剩「闸放不放行」这一件事,不掺几何噪声。
 */
function osClickHarness({ armed = true, refuseClick = null, calibrated = true, observeLanding = true, windowFocused = true, tabActive = true, windowState = 'normal', injectAuthorized = true, onScroll = null } = {}) {
  const posts = []
  const methods = []
  const targets = []
  let lastPoint = { x: 0, y: 0 }
  const savedChrome = globalThis.chrome
  const savedFetch = globalThis.fetch

  globalThis.chrome = {
    storage: {
      local: {
        async get() { return { infra: { wsUrl: 'ws://127.0.0.1:17872/v1/channel', handId: 'hand-os' } } },
        async set() {}, async remove() {},
      },
    },
    tabs: { async get() { return { id: 7, active: tabActive, windowId: 3 } } },
    windows: { async get() { return { id: 3, focused: typeof windowFocused === 'function' ? windowFocused() : windowFocused, state: windowState } } },
    scripting: {
      async executeScript({ func }) {
        if (func.name === 'pageInstallObserverAndReadViewport') {
          return [{ result: { innerW: 1470, innerH: 662, screenX: 0, screenY: 0, dpr: 2, availLeft: 0, availTop: 25, docFocused: true, visibility: 'visible' } }]
        }
        // 落点读取:恒报最后一帧的终点(observeLanding=false 时模拟"页面一个
        // mousemove 都没收到",也就是光标压根不在页面上)。
        return [{ result: observeLanding ? { x: lastPoint.x, y: lastPoint.y } : { x: null, y: null } }]
      },
    },
  }
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname
    const body = init && init.body ? JSON.parse(init.body) : {}
    posts.push(path)
    methods.push((init && init.method) || 'GET')
    if (path === '/handinput/state') {
      return { ok: true, status: 200, async json() { return { cursorCssX: 700, cursorCssY: 300, calibrated, clickArmed: false, samples: calibrated ? 4 : 0, injectAuthorized, platform: injectAuthorized ? 'fake' : 'darwin/CGEventPost 开发机专用 未授权(要授权的是**启动本进程的那个应用**)' } } }
    }
    if (path === '/handinput/play') {
      const last = body.points[body.points.length - 1]
      lastPoint = { x: Math.round(last.x), y: Math.round(last.y) }
      targets.push([lastPoint.x, lastPoint.y])
      return { ok: true, status: 200, async json() { return { unreachable: 0, lagMaxUs: 12 } } }
    }
    if (path === '/handinput/landing') {
      return { ok: true, status: 200, async json() { return { status: armed ? 'ready' : 'suspect', clickArmed: armed, residualPx: 0, samples: 4 } } }
    }
    if (path === '/handinput/reseed') {
      return { ok: true, status: 200, async json() { return { cursorCssX: 700, cursorCssY: 300, calibrated: false, clickArmed: false, samples: 0 } } }
    }
    if (path === '/handinput/click') {
      if (refuseClick) return { ok: false, status: 409, async json() { return { refused: refuseClick } } }
      return { ok: true, status: 200, async json() { return { clicked: true } } }
    }
    if (path === '/handinput/scroll') {
      // onScroll(ticks) 由用例决定页面怎么动;返回 'refuse' 模拟 409(光标已不在落点,一格没发)。
      const verdict = onScroll ? onScroll(body.ticks) : null
      if (verdict === 'refuse') {
        return { ok: false, status: 409, async json() { return { ticks: 0, notches: 0, lagMeanUs: 0, lagMaxUs: 0, status: '未放行:落点确认之后光标被动过(偏 900 像素)' } } }
      }
      return { ok: true, status: 200, async json() { return { ticks: body.ticks.length, notches: body.ticks.reduce((a, t) => a + Math.abs(t.dy), 0), lagMeanUs: 3, lagMaxUs: 9, status: 'ok' } } }
    }
    throw new Error(`假手服务不认识 ${path}`)
  }
  return {
    posts,
    /** 手服务四个端点全是 POST-only:发成 GET 就是 405,而那会在闸都放行了之后才炸。 */
    nonPost: () => methods.filter((m) => m !== 'POST'),
    clicks: () => posts.filter((p) => p === '/handinput/click').length,
    scrolls: () => posts.filter((p) => p === '/handinput/scroll').length,
    plays: () => posts.filter((p) => p === '/handinput/play').length,
    reseeds: () => posts.filter((p) => p === '/handinput/reseed').length,
    playTargets: () => targets.slice(),
    restore() { globalThis.chrome = savedChrome; globalThis.fetch = savedFetch },
  }
}

function osClickCtx() {
  return { cmdMsgId: 'm-os-click', checkpoint() {}, progress() {} }
}

function togglePlan({ onTarget, observed }) {
  return {
    label: '收藏',
    rect: { x: 700, y: 60, w: 60, h: 30 },
    async hitTest() { return onTarget ? { onTarget: true, found: '靶子(div)' } : { onTarget: false, found: 'span「全部」' } },
    async observe() {
      // observed 为 null 表示"这条用例根本不该走到点后观察那一步"。让它响亮地炸,
      // 而不是回一个 null 让断言在别处以看不懂的形态失败。
      if (observed === null) throw new Error('闸没拦住:走到了点后观察')
      return observed
    },
  }
}

test('命中测试不过就不点:这一下会打中谁,由平台自己的命中测试答,不由我们的标定答', async () => {
  const hand = osClickHarness({})
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(),
      togglePlan({ onTarget: false, observed: null }))
    assert.equal(out.outcome, 'refusedByGate')
    assert.equal(hand.clicks(), 0, '命中测试没过却按下去了')
    assert.match(out.detail, /落点上不是靶子/)
    // 判定现场必须留下来:偏了多少、落点上实际是谁。
    assert.match(out.detail, /命中=否/)
  } finally { hand.restore() }
})

test('落点没被接受就不点:标定存疑与命中测试是两道独立的闸', async () => {
  const hand = osClickHarness({ armed: false })
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(),
      togglePlan({ onTarget: true, observed: null }))
    assert.equal(out.outcome, 'refusedByGate')
    assert.equal(hand.clicks(), 0, '落点存疑却按下去了')
  } finally { hand.restore() }
})

test('三道闸齐才点,而且只点一次——原语内不重试是内核', async () => {
  const hand = osClickHarness({})
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(),
      togglePlan({ onTarget: true, observed: { trusted: true, onTarget: true, eventDriftPx: 0, after: '选中=收藏 列表=0' } }))
    assert.equal(out.outcome, 'clicked')
    assert.equal(hand.clicks(), 1, '点击必须恰好一次')
    assert.deepEqual(hand.nonPost(), [],
      '手服务四个端点全是 POST-only —— 2026-08-30 真机首次点击就是被一个 GET /state 打成 405,' +
      '而那时标定已经收敛、闸都要放行了')
    assert.match(out.detail, /isTrusted=true/)
    assert.match(out.detail, /后置=选中=收藏/)
  } finally { hand.restore() }
})

test('手服务自己拒了点击(光标被动过)就收场,不换个姿势再试一次', async () => {
  const hand = osClickHarness({ refuseClick: '未放行:落点确认之后光标被动过(偏 900 像素)' })
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(),
      togglePlan({ onTarget: true, observed: null }))
    assert.equal(out.outcome, 'refusedByGate')
    assert.equal(hand.clicks(), 1, '被拒之后不得再按第二次')
    assert.match(out.detail, /手服务拒绝点击/)
  } finally { hand.restore() }
})

test('Chrome 不在最前面就一步不动:有正面证词才拒,读不到按未知放行', async () => {
  // deadline 给得很短:等待封顶取 min(20s, 命令 deadline),用例不必真等 20 秒。
  const shortCtx = () => ({ ...osClickCtx(), deadlineMs: Date.now() + 300 })
  const hand = osClickHarness({ windowFocused: false })
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, shortCtx(),
      togglePlan({ onTarget: true, observed: null }))
    assert.equal(out.outcome, 'refusedByGate')
    assert.equal(hand.plays(), 0, '窗口没焦点时光标一步都不该飞——2026-09-03 Mac 首跑整轮都在别的窗口上飞了一圈才停')
    assert.match(out.detail, /等了 \d+ 秒仍然:Chrome 窗口不在最前面\(系统焦点在别的窗口\)/)
    assert.match(out.detail, /窗口焦点=否 窗口状态=normal 标签激活=是 页面可见=visible 文档焦点=是/)
  } finally { hand.restore() }
  const minimized = osClickHarness({ windowState: 'minimized' })
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, shortCtx())
    assert.equal(out.outcome, 'refusedByGate'); assert.match(out.detail, /已最小化/); assert.equal(minimized.plays(), 0)
  } finally { minimized.restore() }
  const inactive = osClickHarness({ tabActive: false })
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, shortCtx())
    assert.equal(out.outcome, 'refusedByGate'); assert.match(out.detail, /不是当前激活标签/); assert.equal(inactive.plays(), 0)
  } finally { inactive.restore() }
  assert.equal(refuseWhenNotInFront({ windowFocused: null, windowState: null, tabActive: null, docFocused: null, visibility: null }), null, '全未知不拒绝')
  assert.equal(refuseWhenNotInFront({ windowFocused: true, windowState: 'normal', tabActive: true, docFocused: false, visibility: 'visible' }), null, '文档焦点不参与判据:地址栏有焦点时鼠标照样到页面')
  assert.match(describeFront({ windowFocused: true, windowState: 'fullscreen', tabActive: true, docFocused: false, visibility: 'visible' }), /文档焦点=否/)
})

test('人在 20 秒内把 Chrome 切过来,命令照常往下走——点「开始」那一刻客户端必然在最前面', async () => {
  let asks = 0
  const hand = osClickHarness({ windowFocused: () => { asks += 1; return asks >= 3 } })
  try {
    const progress = []
    const ctx = { ...osClickCtx(), progress(stage) { progress.push(stage) } }
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, ctx)
    assert.equal(out.outcome, 'landed', '切过来之后就该继续,不是拒')
    assert.ok(hand.plays() > 0)
    assert.ok(asks >= 3, '等的时候在复查前台状态')
    assert.ok(progress.includes('等待 Chrome 切到最前'), '等待要向脑汇报进度,人才知道它在等什么')
  } finally { hand.restore() }
})

test('系统没给鼠标注入授权就一步不动,拒绝里带"要授权哪个应用";旧脑没这个字段按未知放行', async () => {
  const denied = osClickHarness({ injectAuthorized: false })
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(), togglePlan({ onTarget: true, observed: null }))
    assert.equal(out.outcome, 'refusedByGate')
    assert.equal(denied.plays(), 0, '没授权时事件会被系统丢掉,飞一圈只是白飞')
    assert.match(out.detail, /系统没有给鼠标注入授权.*要授权的是/)
  } finally { denied.restore() }
  const legacy = osClickHarness({ injectAuthorized: undefined })
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx())
    assert.equal(out.outcome, 'landed', '字段缺席是未知,不拒')
  } finally { legacy.restore() }
})

test('前台判据通过后,零观测的拒绝里仍带移动前的前台状态——好分清"没在前台"和"飞到屏幕外"', async () => {
  const hand = osClickHarness({ observeLanding: false })
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx())
    assert.equal(out.outcome, 'refusedByGate')
    assert.match(out.detail, /没有观测到任何 mousemove/)
    assert.match(out.detail, /移动前 窗口焦点=是/)
  } finally { hand.restore() }
})

test('不带点击计划时,一次点击请求都不许发出去', async () => {
  const hand = osClickHarness({})
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx())
    assert.equal(out.outcome, 'landed')
    assert.equal(hand.clicks(), 0, 'viewportSpread 绝不点击')
  } finally { hand.restore() }
})


// ——— 落点抖动(2026-08-31 立案) ———

/** 采一批落点。种子固定,所以整组统计量是确定的,不会 flake。 */
function aimSamples(rect, n) {
  const out = []
  for (let i = 0; i < n; i++) out.push(clickAimPoint(rect, mulberry32(i * 7919 + 1)))
  return out
}

test('落点不再是元素中心:真机四趟事件偏差 1,0,1,0,0 —— 零方差本身就是机器签名', () => {
  const rect = { x: 800, y: 60, w: 72, h: 30 }
  const cx = rect.x + rect.w / 2, cy = rect.y + rect.h / 2
  const s = aimSamples(rect, 2000)

  // 1) 不是每次都中心
  const atCenter = s.filter((p) => p.x === Math.round(cx) && p.y === Math.round(cy)).length
  assert.ok(atCenter < s.length * 0.15, `${atCenter}/${s.length} 落在正中心,抖动等于没加`)

  // 2) 均值仍在中心附近(没有系统性偏向某一侧)
  const mx = s.reduce((a, p) => a + p.x, 0) / s.length
  const my = s.reduce((a, p) => a + p.y, 0) / s.length
  assert.ok(Math.abs(mx - cx) < 1, `x 均值偏了 ${(mx - cx).toFixed(2)}`)
  assert.ok(Math.abs(my - cy) < 1, `y 均值偏了 ${(my - cy).toFixed(2)}`)

  // 3) 散布随目标形状拉长:宽扁按钮横向更散
  const sd = (vals, m) => Math.sqrt(vals.reduce((a, v) => a + (v - m) ** 2, 0) / vals.length)
  const sdx = sd(s.map((p) => p.x), mx), sdy = sd(s.map((p) => p.y), my)
  assert.ok(sdx > sdy * 1.5, `宽 ${rect.w} 高 ${rect.h} 的按钮,横纵散布应当拉开:${sdx.toFixed(1)} vs ${sdy.toFixed(1)}`)

  // 4) 全部落在中间 60% 以内 —— 抖出边界就是错靶
  for (const p of s) {
    assert.ok(Math.abs(p.x - cx) <= rect.w * 0.3 + 0.5 && Math.abs(p.y - cy) <= rect.h * 0.3 + 0.5,
      `抖出了夹取范围: (${p.x},${p.y})`)
  }

  // 5) **边界上不许堆出一道脊。** 越界若用夹取处理,所有越界样本会被压到边界那
  //    两个值上,堆成一道零方差的脊 —— 那比"总在中心"更好认,等于用一个签名换
  //    另一个。所以越界走重采。判据直接查有没有堆:最外那一格的样本数不该明显
  //    多于紧挨着它的内侧一格(夹取的话会高出几十倍)。
  const limitX = Math.round(rect.w * 0.3)
  const at = (d) => s.filter((p) => Math.abs(p.dx) === d).length
  //    截断正态的密度向外单调递减,所以最外格不该**多于**内侧格。+3 只是噪声余量。
  //    (变异验证:改回夹取时这里是 27 vs 13,当场红)
  assert.ok(at(limitX) <= at(limitX - 1) + 3,
    `边界格 ${at(limitX)} 个 vs 内侧格 ${at(limitX - 1)} 个 —— 堆脊了,说明退回了夹取`)
})

test('落点抖动是确定性的:同一条命令重放必须瞄同一点', () => {
  const rect = { x: 800, y: 60, w: 72, h: 30 }
  const a = clickAimPoint(rect, mulberry32(4242))
  const b = clickAimPoint(rect, mulberry32(4242))
  assert.deepEqual(a, b, '同种子瞄到了两个点,现场就没法复现了')
})

test('目标太小就不抖,退回中心并留痕——失效方向是点得准,不是硬凑一个随机数', () => {
  const tiny = clickAimPoint({ x: 10, y: 10, w: 10, h: 30 }, mulberry32(1))
  assert.equal(tiny.centered, true)
  assert.deepEqual([tiny.x, tiny.y, tiny.dx, tiny.dy], [15, 25, 0, 0])

  const ok = clickAimPoint({ x: 10, y: 10, w: 12, h: 12 }, mulberry32(1))
  assert.equal(ok.centered, false, '恰好到门限就该抖')
})


test('热路径只走一趟:标定就绪时不再为点亮闸白走一趟散开(那一趟每次都停在同一个像素)', async () => {
  const hand = osClickHarness({ calibrated: true })
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(),
      togglePlan({ onTarget: true, observed: { trusted: true, onTarget: true, eventDriftPx: 3, after: '选中=收藏' } }))
    assert.equal(out.outcome, 'clicked')
    assert.equal(hand.plays(), 1,
      `标定就绪时应当只播一次(直接去靶子),实际播了 ${hand.plays()} 次 —— 散开那一趟白走了`)
    // 唯一那一趟必须是**去靶子**,不是去散开点。靶心 (730,75) 附近即可(有抖动)。
    const [x, y] = hand.playTargets()[0]
    assert.ok(Math.abs(x - 730) < 30 && Math.abs(y - 75) < 20,
      `唯一一趟应当终于靶子附近,实际终于 (${x},${y})`)
    assert.equal(hand.clicks(), 1)
  } finally { hand.restore() }
})

test('冷路径照旧散开:标定没就绪时必须先把样本张开,否则 scale 永远解不出来', async () => {
  const hand = osClickHarness({ calibrated: false })
  try {
    await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(),
      togglePlan({ onTarget: true, observed: { trusted: true, onTarget: true, eventDriftPx: 3, after: '选中=收藏' } }))
    assert.ok(hand.plays() >= 2,
      '冷启动必须先走散开:解 scale 要两个样本在两轴各张开 200 CSS px,只去靶子那一趟张不开')
    const [x] = hand.playTargets()[0]
    assert.ok(Math.abs(x - 730) > 100, `冷启动第一趟应当去散开点而不是靶子,实际 x=${x}`)
  } finally { hand.restore() }
})

test('零观测触发一次重新播种,但本条命令照样不点——自愈留给下一条命令', async () => {
  const hand = osClickHarness({ observeLanding: false })
  try {
    const out = await runOsProbe({ world: 'MAIN', label: '假平台' }, 7, osClickCtx(),
      togglePlan({ onTarget: true, observed: null }))
    assert.equal(out.outcome, 'refusedByGate')
    assert.equal(hand.clicks(), 0, '零观测意味着光标不在页面上,绝不能点')
    assert.equal(hand.reseeds(), 1,
      '零观测是死循环(修映射要观测,拿观测要对的映射),必须重新播种才出得来')
    assert.match(out.detail, /已重新播种/)
  } finally { hand.restore() }
})


test('散开靶子必须躲开工具栏,同时保住解 scale 的跨度——两条一起才成立', () => {
  const f = SPREAD_FRACTIONS
  // 浏览器工具栏高度,2026-08-28 真机实测。粗估拿 window.screenY 当页面原点用,
  // 而那是窗口外框顶部,所以冷启动第一趟必然比预期高这么多。
  const CHROME_PX = 121
  // MinSpanPx:解 scale 要求样本在单轴张开这么多 CSS px(推导见 piggyback.go)。
  const MIN_SPAN = 200

  // 419 = 1080p 上下分屏时的内容高(540 外框 − 121 工具栏),真实存在的窗口形状。
  for (const h of [419, 662, 718]) {
    const firstY = Math.round(h * f.yNear)
    assert.ok(firstY - CHROME_PX >= 40,
      `视口高 ${h} 时冷启动第一趟落进页面只有 ${firstY - CHROME_PX}px —— ` +
      '落到页面上方就拿不到样本,拿不到样本就修不了映射,死角(2026-08-31 真机踩到)')
    assert.ok((f.yFar - f.yNear) * h >= MIN_SPAN,
      `视口高 ${h} 时 y 跨度只有 ${((f.yFar - f.yNear) * h).toFixed(0)}px,解不出 scale`)
  }
  for (const w of [900, 1470]) {
    assert.ok((f.xFar - f.xNear) * w >= MIN_SPAN,
      `视口宽 ${w} 时 x 跨度只有 ${((f.xFar - f.xNear) * w).toFixed(0)}px,解不出 scale`)
  }
})

let failures = 0

// ── 实发正文即事实(2026-09-07 甲方裁决):手侧三个纯判定点 ──────────────────────────────

test('上屏地板:逐字相等与同音错字放行,半截、拼音残留、空白、错窗拒绝', () => {
  const { typedTextFloor, TYPED_TEXT_FLOOR, describeTypedTextFloor } = bossTestHooks
  const expected = '您好，看到您的简历很匹配我们的岗位，方便聊聊吗？期待您的回复。'
  const exact = typedTextFloor(expected, '  您好，看到您的简历很匹配我们的岗位，方便聊聊吗？期待您的回复。 ')
  assert.deepEqual([exact.ok, exact.exact, exact.distance], [true, true, 0], 'nbsp/空白只在规范化里抹平,仍算逐字相等')
  const homophone = typedTextFloor(expected, '您好，看到您的简历很匹配我们的岗位，方便了了吗？期待您的回付。')
  assert.equal(homophone.ok, true, '同音错字相似度高,按裁决照发')
  assert.equal(TYPED_TEXT_FLOOR.minSimilarity, 0.6, '09-07 甲方裁定:Mac 短文案样本 0.68~0.81,门槛放到 0.6')
  // 36 字里错 11 处(相似度 0.69):09-07 真机被 0.7 拦在门外的那种,0.6 下放行
  const mac36 = typedTextFloor('今天下午三点方便电话沟通吗？我们这边可以详细介绍岗位情况和薪资待遇。', '今天下午三点方便电话购通吗？我们这边可以祥细介绍岗位情况和新资待遇。')
  assert.ok(mac36.ok && mac36.similarity >= 0.6 && mac36.similarity < 0.95, `36 字级同音错字应放行: ${describeTypedTextFloor(mac36)}`)
  // 相似度 0.5 以下仍是灾难性错乱:一半的字都不对
  const garbage = typedTextFloor('今天下午三点方便电话沟通吗？我们这边可以详细介绍岗位情况和薪资待遇。', '今天下午三点方便电话沟通吗？一二三四五六七八九十甲乙丙丁戊己庚辛壬癸。')
  assert.equal(garbage.ok, false, `一半字不对仍拒: ${describeTypedTextFloor(garbage)}`)
  assert.equal(homophone.exact, false)
  assert.ok(homophone.similarity >= TYPED_TEXT_FLOOR.minSimilarity && homophone.distance === 3, describeTypedTextFloor(homophone))
  const dropped = typedTextFloor(expected, '您好，看到您的简历很匹配我们的岗位，方便聊聊吗？期待您的回复')
  assert.equal(dropped.ok, true, '掉一个字元在字数差之内')
  const half = typedTextFloor(expected, '您好，看到您的简历很匹配我们的')
  assert.deepEqual([half.ok, half.lengthDiff > TYPED_TEXT_FLOOR.maxLengthDiff], [false, true], '只打了一半:相似度不低但字数差挡住')
  const residue = typedTextFloor(expected, expected + 'qidai')
  assert.equal(residue.ok, false, '拼音字母残留没上屏:字数差挡住')
  assert.equal(typedTextFloor(expected, '').ok, false, '一个字没打进去')
  assert.equal(typedTextFloor(expected, '这是另一段完全不同的话，长度大致相同，用来模拟打到别的窗口去了').ok, false, '错窗/乱码:相似度接近零')
  assert.doesNotMatch(describeTypedTextFloor(homophone), /您好/, '留痕只有数字,不带正文')
})

test('TIP 活动核对:只在驱动了上屏词且回报少于计划时拒绝', () => {
  const { tipWordsShortfall } = bossTestHooks
  assert.equal(tipWordsShortfall({}), null, 'macOS 没有 TIP,不适用')
  assert.equal(tipWordsShortfall({ wordsDriven: false, wordsPlanned: 6, wordsCommitted: 0 }), null)
  assert.equal(tipWordsShortfall({ wordsDriven: true, wordsPlanned: 6, wordsCommitted: 6, words: 'TIP 上屏 6/6 词,词表已用完' }), null)
  assert.equal(tipWordsShortfall({ wordsDriven: true, wordsPlanned: 0, wordsCommitted: 0 }), null, '没有需要上屏的词(纯英文/标点)不算缺口')
  const short = tipWordsShortfall({ wordsDriven: true, wordsPlanned: 6, wordsCommitted: 2, words: 'TIP 只上屏了 2/6 词' })
  assert.match(short, /只上屏了 2\/6 词/, '键落到了别的输入法,发送前必须拒')
})

test('发后认行:基线之外、出站、文本、服务端确认、时间在窗内的行取最新,零匹配为 null', () => {
  const { pickSentBossRow } = bossTestHooks
  const dispatchedAt = 1_788_500_000_000
  const row = (mid, over) => ({ mid, direction: 'out', type: 'text', bizType: 101, bodyType: 1, status: 1, time: dispatchedAt + 800, text: '实发的一句', interviewCondition: null, actionAid: null, templateId: 1, dialogOperated: null, dialogAids: [], ...over })
  const baseline = new Set(['100'])
  const rows = [
    row('100', {}),                                          // 基线里的旧行
    row('101', { direction: 'in', text: '候选人插话' }),     // 入站
    row('102', { status: 0, text: '在途/乐观行' }),           // 未确认
    row('103', { time: dispatchedAt - 60_000, text: '化石' }),// 派发窗口之前
    row('104', { text: '同音错字版本' }),                     // 命中(较旧)
    row('105', { status: 2, text: '最新的一条' }),            // 命中(最新)
    row('106', { bizType: 21130009, bodyType: 14, text: '发送了面试邀请' }), // 卡片
  ]
  const hit = pickSentBossRow(rows, baseline, dispatchedAt)
  assert.deepEqual([hit.row.mid, hit.text, hit.hashInput], ['105', '最新的一条', '最新的一条'], '多条取 mid 最大者,正文以该行为准')
  assert.equal(pickSentBossRow(rows.slice(0, 4), baseline, dispatchedAt), null, '没有合格行即 null,交 possible 验证读')
  const untimed = pickSentBossRow([row('107', { time: null })], baseline, dispatchedAt)
  assert.equal(untimed.row.mid, '107', 'time 缺席不作窗口判定(与既有口径一致),仍可命中')
})

test('排版器清洗档:打不出的字元摘掉后照排,摘了什么随 dropped 带回;摘空即 ok:false', async () => {
  const { planType: plan, bossTestHooks: hooks } = await import(unitBundleURL)
  const withEmoji = await plan('你好😊世界', 7, { sanitize: true })
  assert.equal(withEmoji.ok, true, `清洗后应能排出:${withEmoji.ok ? '' : withEmoji.reasons.join(';')}`)
  assert.equal(withEmoji.text, '你好世界')
  assert.deepEqual(withEmoji.dropped.map((d) => d.kind), ['other'])
  assert.equal(hooks.summarizeDroppedKinds(withEmoji.dropped), 'other 1', '留痕只按 kind 计数,不带字元')
  const onlyEmoji = await plan('😊', 7, { sanitize: true })
  assert.deepEqual([onlyEmoji.ok, onlyEmoji.reasons], [false, ['清理之后没有内容可打']])
  const strict = await plan('你好😊世界', 7)
  assert.equal(strict.ok, false, '不传 sanitize 仍保持严格(debug.osType 要的正是这个)')
})

test('快捷窗打开后先看一眼再打字:停顿落在 3~6 秒的有界区间,端点可达', () => {
  const { sampleQuickChatReadPause, QUICK_CHAT_READ_PAUSE_MS } = bossTestHooks
  assert.equal(sampleQuickChatReadPause(() => 0), QUICK_CHAT_READ_PAUSE_MS.min)
  assert.equal(sampleQuickChatReadPause(() => 0.999999), QUICK_CHAT_READ_PAUSE_MS.max)
  const seen = new Set()
  for (let i = 0; i < 500; i += 1) {
    const ms = sampleQuickChatReadPause()
    assert.ok(ms >= QUICK_CHAT_READ_PAUSE_MS.min && ms <= QUICK_CHAT_READ_PAUSE_MS.max, `越界 ${ms}`)
    seen.add(ms)
  }
  assert.ok(seen.size > 50, '必须是随机值,不是常数')
})

test('会话行滚进视口的计划:带内不滚、带外朝行滚到带中心、不在 DOM 按扫描方向滚一屏', () => {
  const { planBossRowScroll } = bossTestHooks
  // 2026-09-07 只读考古的真实尺寸:容器 y=196 高 446,行高 74,窗口 662 高时可见带里 5 行
  const band = { top: 196, bottom: 642 }
  const rect = (y) => ({ x: 219, y, w: 359, h: 74 })
  assert.equal(planBossRowScroll({ count: 1, rect: rect(272) }, band, 'down'), null, '带内不滚')
  assert.deepEqual(planBossRowScroll({ count: 1, rect: rect(1000) }, band, 'down'), { direction: 'down', distancePx: 618 }, '带外朝行滚到带中心')
  const above = planBossRowScroll({ count: 1, rect: rect(-200) }, band, 'down')
  assert.deepEqual([above.direction, above.distancePx >= 74], ['up', true])
  const partial = planBossRowScroll({ count: 1, rect: rect(600) }, band, 'down')
  assert.deepEqual([partial.direction, partial.distancePx], ['down', 218], '只露一半也要滚:距离至少一行高')
  assert.deepEqual(planBossRowScroll({ count: 0, rect: rect(0) }, band, 'down'), { direction: 'down', distancePx: 446 }, '虚拟列表没渲染:按扫描方向滚一屏')
  assert.equal(planBossRowScroll({ count: 0, rect: rect(0) }, band, 'up').direction, 'up')
})

test('上屏键按 OS 选:darwin 只用空格,windows 与不传 OS 沿用默认表(含数字选词)', async () => {
  const { planType: plan } = await import(unitBundleURL)
  const text = '您好，看到您的简历和我们岗位很匹配，方便的话聊一聊近期的求职打算，期待您的回复，谢谢。'
  const commits = (result) => result.plan.words.filter((w) => w.commit).map((w) => w.commit.code)
  for (const seed of [3, 11, 29]) {
    const mac = await plan(text, seed, { sanitize: true, os: 'darwin' })
    assert.equal(mac.ok, true, `darwin seed=${seed}: ${mac.ok ? '' : mac.reasons.join(';')}`)
    const macCommits = commits(mac)
    assert.ok(macCommits.length > 10, 'fixture 得有足够多的组字词')
    assert.ok(macCommits.every((code) => code === 'Space'), `darwin 只能用空格上屏: ${macCommits.filter((c) => c !== 'Space').join(',')}`)
  }
  const digitSeen = new Set()
  for (const seed of [3, 11, 29, 47]) {
    const win = await plan(text, seed, { sanitize: true, os: 'windows' })
    const plain = await plan(text, seed, { sanitize: true })
    assert.equal(win.ok && plain.ok, true)
    for (const code of [...commits(win), ...commits(plain)]) if (code !== 'Space') digitSeen.add(code)
  }
  assert.ok(digitSeen.size > 0, `windows / 不传 OS 应保留默认表的数字选词(14%),四个种子一个都没抽到不合理`)
})

test('智联邀面参数展开:命令只带开始与方式,线上由手加 30 分钟结束时间,现场结束缺席(2026-09-08 甲方裁决)', () => {
  const startsAt = new Date(2026, 8, 9, 13, 0).getTime()
  assert.deepEqual(zhilianInterviewDetails({ startsAt, method: 'wechatVideo' }),
    { startsAt, endsAt: startsAt + 30 * 60_000, method: 'wechatVideo' },
    '线上:时长项固定选 30 分钟,展开值与改前脑侧派生的逐字一致')
  assert.deepEqual(zhilianInterviewDetails({ startsAt, method: 'onsite' }),
    { startsAt, method: 'onsite' },
    '现场:平台无时长控件,结束缺席、不合成')
})


// ---- 请求录制 ----

test('请求录制上限:只记平台站点发出的请求,别的网站任何形态都不记(2026-09-08 甲方要求)', () => {
  assert.deepEqual(netCaptureSites.map((s) => s.id), ['boss', 'zhilian'], '上限就是这两家,扩要在表里明写')
  const rec = (facts, tabHost) => shouldRecord(facts, tabHost, netCaptureSites)

  // 判据一:发起方是平台,目的地不限。
  assert.equal(rec({ url: 'https://apm-fe.zhipin.com/wapi/zpApm/actionLog/x', type: 'xmlhttprequest', initiator: 'https://www.zhipin.com', tabId: 5 }, 'www.zhipin.com'), true)
  assert.equal(rec({ url: 'ws://127.0.0.1:9222/', type: 'websocket', initiator: 'https://www.zhipin.com', tabId: 5 }, 'www.zhipin.com'), true, '平台页向本机端口的探测正是要看的')
  assert.equal(rec({ url: 'https://hm.baidu.com/hm.js', type: 'script', initiator: 'https://login.zhipin.com', tabId: -1 }, null), true, '子域也是平台;service worker 没有标签页也记')
  assert.equal(rec({ url: 'https://rd6.zhaopin.com/api/x', type: 'xmlhttprequest', initiator: 'https://rd6.zhaopin.com', tabId: 3 }, 'rd6.zhaopin.com'), true)

  // 判据二:主文档导航只看目的地,所在标签页不作数。
  assert.equal(rec({ url: 'https://www.zhipin.com/web/chat/index', type: 'main_frame', tabId: 5 }, 'www.google.com'), true)
  assert.equal(rec({ url: 'https://www.google.com/', type: 'main_frame', tabId: 5 }, 'www.zhipin.com'), false, '平台标签页地址栏敲别的网址,那一跳不记')

  // 判据三:平台标签页里第三方 iframe 发的。
  assert.equal(rec({ url: 'https://turing.captcha.qcloud.com/cap', type: 'xmlhttprequest', initiator: 'https://turing.captcha.qcloud.com', tabId: 5 }, 'www.zhipin.com'), true)

  // 一律不记。
  assert.equal(rec({ url: 'https://www.zhipin.com/favicon.ico', type: 'image', initiator: 'https://www.google.com', tabId: 8 }, 'www.google.com'), false, '别的网站访问平台资源不记:目的地不是判据')
  assert.equal(rec({ url: 'ws://127.0.0.1:17872/', type: 'websocket', initiator: 'chrome-extension://abcdefghijklmnop', tabId: -1 }, null), false, '插件自己连脑不记')
  assert.equal(rec({ url: 'https://api.example.com/', type: 'xmlhttprequest', initiator: 'null', tabId: 9 }, 'www.example.com'), false, '不透明源不记')
  assert.equal(rec({ url: 'https://api.example.com/', type: 'xmlhttprequest', tabId: 9 }, null), false, '什么都没有不记')
  assert.equal(rec({ url: 'https://evilzhipin.com/x', type: 'script', initiator: 'https://evilzhipin.com', tabId: 9 }, 'evilzhipin.com'), false, '后缀以点分界,同尾不同域不算')
  assert.equal(rec({ url: 'https://x.zhipin.com.evil.com/', type: 'script', initiator: 'https://x.zhipin.com.evil.com', tabId: 9 }, 'x.zhipin.com.evil.com'), false)

  assert.equal(siteOfHost('www.zhipin.com', netCaptureSites)?.id, 'boss')
  assert.equal(siteOfHost('zhaopin.com', netCaptureSites)?.id, 'zhilian')
  assert.equal(siteOfHost(null, netCaptureSites), null)
  assert.equal(hostOf('https://WWW.Zhipin.com/a?b'), 'www.zhipin.com')
  assert.equal(hostOf('null'), null)
  assert.equal(hostOf('about:blank'), null)
  assert.equal(hostOf(undefined), null)
})

test('请求录制:凭据头剥值留名,长值截断,二进制值不带,没有头不造空数组', () => {
  for (const n of ['Cookie', 'set-cookie', 'Authorization', 'Proxy-Authorization', 'X-Zp-Token', 'x-csrf-token', 'X-Session-Id', 'zp_auth']) {
    assert.equal(sensitiveHeader(n), true, `${n} 应当算凭据`)
  }
  for (const n of ['Accept', 'Content-Type', 'User-Agent', 'Referer', 'traceparent', 'x-requested-with']) {
    assert.equal(sensitiveHeader(n), false, `${n} 不该被剥`)
  }
  const long = 'v'.repeat(HEADER_VALUE_KEEP_CHARS + 10)
  assert.deepEqual(stripHeaders([
    { name: 'Cookie', value: 'a=b' },
    { name: 'Accept', value: '*/*' },
    { name: 'Link', value: long },
    { name: 'X-Bin', binaryValue: new ArrayBuffer(2) },
    { name: 'X-Empty' },
  ]), [
    { name: 'Cookie', value: STRIPPED_VALUE },
    { name: 'Accept', value: '*/*' },
    { name: 'Link', value: 'v'.repeat(HEADER_VALUE_KEEP_CHARS) },
    { name: 'X-Bin', value: '[二进制]' },
    { name: 'X-Empty', value: '' },
  ])
  assert.equal(stripHeaders(undefined), undefined)
})

test('请求录制:请求体摘要——表单原样、raw 解 utf-8 记总字节、超长截断、文件只记名、空即缺席', () => {
  const enc = new TextEncoder()
  const json = '{"a":"中文"}'
  assert.deepEqual(describeBody({ raw: [{ bytes: enc.encode(json).buffer }] }), { bytes: enc.encode(json).byteLength, text: json })
  assert.deepEqual(describeBody({ formData: { content: ['{"k":1}'], x: ['1', '2'] } }), { form: { content: ['{"k":1}'], x: ['1', '2'] } })
  assert.deepEqual(describeBody({ raw: [{ file: '/tmp/a.png' }] }), { files: ['/tmp/a.png'] })
  const long = 'x'.repeat(BODY_KEEP_CHARS + 5)
  const cut = describeBody({ raw: [{ bytes: enc.encode(long).buffer }] })
  assert.equal(cut.text.length, BODY_KEEP_CHARS)
  assert.equal(cut.truncated, true)
  assert.equal(cut.bytes, BODY_KEEP_CHARS + 5)
  // 多段 raw 拼起来解,别按段截断。
  const two = describeBody({ raw: [{ bytes: enc.encode('ab').buffer }, { bytes: enc.encode('cd').buffer }] })
  assert.deepEqual(two, { bytes: 4, text: 'abcd' })
  assert.deepEqual(describeBody({ error: 'Unknown error.' }), { error: 'Unknown error.' })
  assert.equal(describeBody(null), undefined)
  assert.equal(describeBody(undefined), undefined)
  assert.equal(describeBody({}), undefined)
  assert.equal(describeBody({ raw: [] }), undefined)
})

test('请求录制:四事件合成一条,重定向同 id 记跳,错误记码,未知 id 忽略,结束时在途按 unfinished 落盘', async () => {
  const store = memoryWitnessStorage()
  const timers = []
  const s = new CaptureSession({ store, flushAtCount: 100, flushAfterMs: 1000, schedule: (fn) => timers.push(fn) })

  s.begin({ requestId: '1', url: 'https://www.zhipin.com/wapi/a', method: 'POST', type: 'xmlhttprequest', initiator: 'https://www.zhipin.com', tabId: 5, frameId: 0, timeStamp: 1000, requestBody: { formData: { content: ['{}'] } } }, 'www.zhipin.com')
  s.sendHeaders({ requestId: '1', timeStamp: 1001, requestHeaders: [{ name: 'Cookie', value: 'a=b' }, { name: 'Accept', value: '*/*' }] })
  s.headersReceived({ requestId: '1', timeStamp: 1050, statusCode: 200, statusLine: 'HTTP/1.1 200 OK', responseHeaders: [{ name: 'Set-Cookie', value: 'x' }, { name: 'Content-Type', value: 'application/json' }] })
  s.completed({ requestId: '1', timeStamp: 1060, statusCode: 200, fromCache: false, ip: '1.2.3.4' })

  s.begin({ requestId: '2', url: 'https://www.zhipin.com/old', method: 'GET', type: 'main_frame', tabId: 5, frameId: 0, timeStamp: 2000 }, null)
  s.headersReceived({ requestId: '2', timeStamp: 2010, statusCode: 302 })
  s.begin({ requestId: '2', url: 'https://www.zhipin.com/new', method: 'GET', type: 'main_frame', tabId: 5, frameId: 0, timeStamp: 2020 }, null)
  s.headersReceived({ requestId: '2', timeStamp: 2090, statusCode: 200 })
  s.completed({ requestId: '2', timeStamp: 2100, statusCode: 200 })

  s.begin({ requestId: '3', url: 'https://rd6.zhaopin.com/api/security/environment', method: 'POST', type: 'xmlhttprequest', initiator: 'https://rd6.zhaopin.com', tabId: 7, frameId: 0, timeStamp: 3000 }, 'rd6.zhaopin.com')
  s.errored({ requestId: '3', timeStamp: 3001, error: 'net::ERR_BLOCKED_BY_CLIENT' })

  s.completed({ requestId: '9', timeStamp: 4000 })
  s.sendHeaders({ requestId: '9', timeStamp: 4000 })

  s.begin({ requestId: '4', url: 'wss://ws.zhipin.com/', method: 'GET', type: 'websocket', initiator: 'https://www.zhipin.com', tabId: 5, frameId: 0, timeStamp: 5000 }, 'www.zhipin.com')
  assert.equal(s.pendingCount(), 1)

  assert.deepEqual(await telemetryReadAll(store, TELEMETRY_KIND_REQUEST), [], '未到阈值也未到时,还在缓冲')
  assert.equal(timers.length, 1, '缓冲非空只挂一个定时器')
  timers[0]()
  await s.flush()

  const rows = await telemetryReadAll(store, TELEMETRY_KIND_REQUEST)
  assert.equal(rows.length, 3)
  const [r1, r2, r3] = rows
  assert.equal(r1.requestHeaders.find((h) => h.name === 'Cookie').value, STRIPPED_VALUE)
  assert.equal(r1.requestHeaders.find((h) => h.name === 'Accept').value, '*/*')
  assert.equal(r1.responseHeaders.find((h) => h.name === 'Set-Cookie').value, STRIPPED_VALUE)
  assert.equal(r1.statusCode, 200)
  assert.equal(r1.statusLine, 'HTTP/1.1 200 OK')
  assert.equal(r1.ip, '1.2.3.4')
  assert.equal(r1.fromCache, false)
  assert.deepEqual([r1.at, r1.sentAt, r1.headersAt, r1.endedAt], [1000, 1001, 1050, 1060])
  assert.deepEqual(r1.body, { form: { content: ['{}'] } })
  assert.equal(r1.tabHost, 'www.zhipin.com')
  assert.equal(r1.initiator, 'https://www.zhipin.com')
  assert.deepEqual(r2.hops, ['https://www.zhipin.com/old'])
  assert.equal(r2.url, 'https://www.zhipin.com/new')
  assert.equal(r2.at, 2020)
  assert.equal(r2.statusCode, 200)
  assert.equal(r2.tabHost, undefined)
  assert.equal(r3.error, 'net::ERR_BLOCKED_BY_CLIENT')
  assert.equal(r3.endedAt, 3001)
  assert.equal(s.writtenCount(), 3)

  await s.end()
  const after = await telemetryReadAll(store, TELEMETRY_KIND_REQUEST)
  assert.equal(after.length, 4)
  assert.equal(after[3].requestId, '4')
  assert.equal(after[3].unfinished, true)
  assert.equal(s.pendingCount(), 0)
  assert.equal(s.writtenCount(), 4)
  assert.ok(Object.keys(store.state).every((k) => k.startsWith('telemetry:')), '键都带 telemetry: 前缀')
})

test('请求录制:缓冲攒够条数立刻落盘,写是串行的,reset 清掉在途与缓冲', async () => {
  const store = memoryWitnessStorage()
  const s = new CaptureSession({ store, flushAtCount: 3, flushAfterMs: 1000, schedule: () => {} })
  for (let i = 0; i < 7; i += 1) {
    s.begin({ requestId: String(i), url: `https://www.zhipin.com/${i}`, method: 'GET', type: 'image', tabId: 1, frameId: 0, timeStamp: i }, 'www.zhipin.com')
    s.completed({ requestId: String(i), timeStamp: i + 1 })
  }
  await s.settled()
  assert.equal((await telemetryReadAll(store, TELEMETRY_KIND_REQUEST)).length, 6, '两次阈值落盘 6 条,余 1 条在缓冲等定时器')
  assert.equal(TELEMETRY_MAX_REQUEST_CHUNKS, 25)
  await s.end()
  assert.equal((await telemetryReadAll(store, TELEMETRY_KIND_REQUEST)).length, 7)
  assert.equal(s.writtenCount(), 7)

  s.begin({ requestId: 'x', url: 'https://www.zhipin.com/x', method: 'GET', type: 'image', tabId: 1, frameId: 0, timeStamp: 10 }, null)
  s.reset()
  assert.equal(s.pendingCount(), 0)
  assert.equal(s.writtenCount(), 0)
  s.completed({ requestId: 'x', timeStamp: 11 })
  await s.end()
  assert.equal((await telemetryReadAll(store, TELEMETRY_KIND_REQUEST)).length, 7, 'reset 后旧 id 的收尾不再落盘')
})

test('请求录制:窗口判定与标签页主机表', () => {
  assert.equal(CAPTURE_DURATION_MS, 10 * 60_000)
  assert.equal(captureActive(null, 1), false)
  assert.equal(captureActive(undefined, 1), false)
  assert.equal(captureActive({ startedAt: 0, until: 100 }, 99), true)
  assert.equal(captureActive({ startedAt: 0, until: 100 }, 100), false, '到点即止')
  assert.equal(captureActive({ startedAt: 0, until: 100, endedAt: 50 }, 60), false, '人工停止后不再记')

  resetTabHostsForTest()
  noteTabUrl(1, 'https://www.zhipin.com/web/chat/index')
  assert.equal(tabHost(1), 'www.zhipin.com')
  noteTabUrl(1, 'about:blank')
  assert.equal(tabHost(1), null, '解不出主机就当没有')
  noteTabUrl(2, undefined)
  assert.equal(tabHost(2), null)
  noteTabUrl(3, 'https://rd6.zhaopin.com/')
  assert.equal(tabHost(3), 'rd6.zhaopin.com')
  forgetTabHost(3)
  assert.equal(tabHost(3), null)
  resetTabHostsForTest()
})

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
