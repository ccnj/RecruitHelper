// WS 连接生命周期(base 层):拨号、hello、会话心跳、断线重连。
// 传输连接方向手→脑(浏览器无法接受入站);语义方向永远脑→手。
import {
  AckBody,
  AckStatus,
  Batch,
  ByeBody,
  ByeCode,
  CONTRACT_HASH,
  CmdContext,
  CmdClass,
  DEFAULTS,
  Envelope,
  ErrorCode,
  EventContext,
  EventDataByName,
  EventName,
  Feature,
  Kind,
  Limits,
  PingBody,
  PingContext,
  PingSensors,
  PRIMITIVE_META,
  PROTO_VERSION,
  QueryBody,
  ResultBody,
  ResultEnvelope,
  Retryable,
  SensorParams,
  SideEffect,
  TypedEventBody,
  ValidationIssue,
  WelcomeBody,
  validateFrameSize,
  validateKindBody,
} from './protocol'
import {
  BOOT_ID,
  RECONNECT,
  RECONNECT_STABLE_MS,
  HB_INTERVAL_MS,
  HELLO_TIMEOUT_MS,
  getHandId,
  getWsUrl,
  newMsgId,
} from './config'
import { capabilities } from '../program/registry'
import { Dispatcher, SendOutcome } from './dispatcher'
import { WitnessAdvertisement, WitnessStore, WitnessStorage } from './witness'

type Phase = 'connecting' | 'preSession' | 'session' | 'closed'

interface IncomingEnvelope {
  proto: number
  kind: string
  msgId: string
  session: string | null
  ts: number
  attempt: number
  body: unknown
}

interface PendingResult {
  envelope: Envelope
  sent: boolean
}

interface CachedSensorSnapshot {
  unreadTotal: { observedAt: number; value: number } | null
}

export type ContextHealth = PingContext

const BASE_FEATURES = [Feature.Progress1, Feature.Lease1, Feature.Cancel1] as const
const RESULT_QUEUE_CAP = DEFAULTS.handResultQueue
const KNOWN_KINDS = new Set<string>(Object.values(Kind))
const KNOWN_EVENTS = new Set<string>(Object.values(EventName))

export class Connection {
  private ws: WebSocket | null = null
  private session: string | null = null
  private phase: Phase = 'closed'
  private hbTimer: ReturnType<typeof setInterval> | null = null
  private helloTimer: ReturnType<typeof setTimeout> | null = null
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private reconnectStableTimer: ReturnType<typeof setTimeout> | null = null
  private reconnectStableGeneration = 0
  private connectGeneration = 0
  private reconnectDelay: number = RECONNECT.baseMs
  private heartbeatIntervalMs: number = HB_INTERVAL_MS
  private awaitingPong = false
  private missedPongs = 0
  private receiveChain: Promise<void> = Promise.resolve()
  private maxMsgBytes: number = DEFAULTS.maxMsgBytes
  private inlineBytes: number = DEFAULTS.inlineBytes
  private pendingResults = new Map<string, PendingResult>()
  private contexts: PingContext[] = []
  private sensorSnapshot: CachedSensorSnapshot | null = null
  private currentSensorConfig: Readonly<Partial<SensorParams>> = Object.freeze({})
  private commandContexts = new Map<string, Readonly<CmdContext>>()
  private commandContextListeners = new Set<(context: Readonly<CmdContext>) => void>()
  private sensorConfigListeners = new Set<(config: Readonly<Partial<SensorParams>>) => void>()
  private readonly witness = new WitnessStore(chrome.storage.local as unknown as WitnessStorage)
  private witnessEnabledForSession = false
  private dispatcher: Dispatcher

  constructor() {
    this.dispatcher = new Dispatcher(
      (kind, session, body) => this.rawSend(kind, session, body),
      (context) => this.rememberCommandContext(context),
      this.witness,
      (session, body, commitIdemKey) => this.sendDurableResult(session, body, commitIdemKey),
    )
  }

  status(): {
    phase: Phase
    session: string | null
    bootId: string
    queueDepth: number
    inFlight: string | null
    pendingResults: number
    heartbeatIntervalMs: number
    missedPongs: number
  } {
    const execution = this.dispatcher.snapshot()
    return {
      phase: this.phase,
      session: this.session,
      bootId: BOOT_ID,
      queueDepth: execution.queueDepth,
      inFlight: execution.inFlight,
      pendingResults: this.pendingResults.size + (this.witness.advertisement()?.outboxPending ?? 0),
      heartbeatIntervalMs: this.heartbeatIntervalMs,
      missedPongs: this.missedPongs,
    }
  }

  // base 监听器只缓存已经观测到的现货；心跳到点绝不反向采集 DOM。
  setContextHealth(contexts: readonly ContextHealth[]): void {
    this.contexts = contexts.map((context) => ({ ...context }))
  }

  setSensorSnapshot(snapshot: PingSensors | null): void {
    const probe: PingBody = { queueDepth: 0, inFlight: null, sensors: snapshot }
    if (validateKindBody(Kind.Ping, probe).length > 0) return
    this.sensorSnapshot = snapshot === null
      ? null
      : {
          unreadTotal: snapshot.unreadTotal === null
            ? null
            : {
                value: snapshot.unreadTotal.value,
                observedAt: Date.now() - snapshot.unreadTotal.observedAgoMs,
              },
        }
  }

  sensorConfig(): Readonly<Partial<SensorParams>> {
    return this.currentSensorConfig
  }

  onSensorConfig(listener: (config: Readonly<Partial<SensorParams>>) => void): () => void {
    this.sensorConfigListeners.add(listener)
    return () => this.sensorConfigListeners.delete(listener)
  }

  currentCommandContext(platform: string): Readonly<CmdContext> | undefined {
    return this.commandContexts.get(platform)
  }

  onCommandContext(listener: (context: Readonly<CmdContext>) => void): () => void {
    this.commandContextListeners.add(listener)
    return () => this.commandContextListeners.delete(listener)
  }

  emitPlatformSensorEvent<N extends keyof EventDataByName>(
    name: N,
    platform: string,
    data: EventDataByName[N],
    observedAt = Date.now(),
  ): SendOutcome {
    const context = this.commandContexts.get(platform)
    if (!context) return 'dropped'
    return this.emitSensorEvent(name, {
      platform: context.platform,
      accountRef: context.accountRef,
    }, data, observedAt)
  }

  // QoS0 传感提示：不 ack、不重发、不持久化，也不在手侧据此做业务决策。
  emitSensorEvent<N extends keyof EventDataByName>(
    name: N,
    context: EventContext,
    data: EventDataByName[N],
    observedAt = Date.now(),
  ): SendOutcome {
    if (!KNOWN_EVENTS.has(name)) return 'dropped'
    const body = { name, context, observedAt, data } as TypedEventBody
    if (validateKindBody(Kind.Event, body).length > 0) return 'dropped'
    return this.rawSend(Kind.Event, this.session, body)
  }

  // ensureConnected:幂等启动/续连(background 的 alarm 看门狗与启动都调它)。
  ensureConnected(): void {
    if (this.phase === 'connecting' || this.phase === 'preSession' || this.phase === 'session') return
    this.connect()
  }

  private async connect(): Promise<void> {
    const generation = ++this.connectGeneration
    // connect 可能在旧 close/timer 的回调之后进入；先使上一 session 的稳定窗口失效，
    // 即使 clearTimeout 遇到已排队回调，generation 守卫也不会污染新连接。
    this.invalidateReconnectStableWindow()
    this.phase = 'connecting'
    // welcome 下发的 limits 只属于上一会话；新握手必须从协议默认硬边界开始。
    this.maxMsgBytes = DEFAULTS.maxMsgBytes
    this.inlineBytes = DEFAULTS.inlineBytes
    this.heartbeatIntervalMs = HB_INTERVAL_MS
    this.resetPongWatch()
    this.applySensorConfig(undefined)
    // 关掉可能残留的旧连接,避免"孤儿 socket"的迟到事件污染共享状态(真机 supersede 风暴根因)。
    const previous = this.ws
    this.ws = null
    if (previous) try { previous.close() } catch { /* ignore */ }
    try {
      // storage 读取同样是拨号流程的一部分；失败必须落回 closed 并退避，不能让
      // ensureConnected 留在 connecting。generation 防止陈旧 rejection 关闭新连接。
      const url = await getWsUrl()
      if (this.connectGeneration !== generation || this.phase !== 'connecting') return
      const ws = new WebSocket(url)
      this.ws = ws
      // 陈旧处理器守卫:只有当前 ws 的事件才作数;被替换的旧 ws 迟到事件一律忽略。
      ws.onopen = () => {
        if (this.ws !== ws) return
        void this.onOpen(ws).catch((error: unknown) => {
          console.warn('[hand] 初始化本地标识失败', error)
          if (this.ws === ws) {
            try { ws.close() } catch { /* onclose 统一续连 */ }
          }
        })
      }
      ws.onmessage = (event) => {
        if (this.ws !== ws) return
        // Blob 解码可能异步，串行链保证帧顺序不被解码时长打乱。
        this.receiveChain = this.receiveChain
          .then(() => this.ws === ws ? this.onMessage(event, ws) : undefined)
          .catch((error: unknown) => console.warn('[hand] 收帧失败', error))
      }
      ws.onclose = () => { if (this.ws === ws) this.onClose() }
      ws.onerror = () => {} // onclose 随后触发,统一在那里处理
    } catch {
      if (this.connectGeneration !== generation) return
      this.session = null
      this.phase = 'closed'
      this.ws = null
      this.scheduleReconnect()
    }
  }

  private async onOpen(source: WebSocket): Promise<void> {
    const handId = await getHandId()
    if (this.ws !== source || source.readyState !== WebSocket.OPEN) return
    let witnessAdvertisement: WitnessAdvertisement | null = null
    try {
      await this.witness.initialize()
      witnessAdvertisement = this.witness.advertisement()
    } catch (error) {
      // 仍允许只读/无外部副作用能力上线；未声明 witness/1 时真实 SX cap 一并剔除。
      console.error('[hand] 证词库不可用，真实外部副作用能力停用', error)
    }
    const sessionCaps = capabilities().filter((capability) =>
      witnessAdvertisement !== null || !isWitnessedCapability(capability),
    )
    this.witnessEnabledForSession = witnessAdvertisement !== null
    this.dispatcher.setSessionCapabilities(sessionCaps)
    const hello = {
      handId,
      bootId: BOOT_ID,
      protoSupported: [PROTO_VERSION],
      contractHash: CONTRACT_HASH,
      app: { extVersion: chrome.runtime.getManifest().version, browser: 'chrome' },
      caps: sessionCaps,
      features: witnessAdvertisement
        ? [...BASE_FEATURES, Feature.Witness1]
        : [...BASE_FEATURES],
      ...(witnessAdvertisement ?? {}),
    }
    this.rawSend(Kind.Hello, null, hello)
    this.phase = 'preSession'
    // 本地 handId 不需要人工确认；脑必须在日常 hello 硬上限内自动建立会话。
    this.helloTimer = setTimeout(() => {
      if (this.ws === source) source.close()
    }, HELLO_TIMEOUT_MS)
  }

  private async onMessage(event: MessageEvent, source: WebSocket): Promise<void> {
    const decoded = await decodeFrame(event.data, this.maxMsgBytes)
    if (this.ws !== source) return
    if (decoded.kind === 'invalid') {
      this.closeProtocol(1003, '协议只接受 UTF-8 文本帧')
      return
    }
    if (decoded.kind === 'tooLarge') {
      this.closeProtocol(1009, '协议帧超过 maxMsgBytes')
      return
    }
    const frame = decoded.frame
    if (validateFrameSize(frame.wire, this.maxMsgBytes).length > 0) {
      // 在 JSON parse 前执行硬边界；超限帧没有资格进入分发器或产生副作用。
      this.closeProtocol(1009, '协议帧超过 maxMsgBytes')
      return
    }

    let raw: unknown
    try {
      raw = JSON.parse(frame.text)
    } catch {
      this.closeProtocol(1002, 'JSON 非法')
      return
    }
    const parsed = parseEnvelope(raw)
    if ('error' in parsed) {
      const maybe = asRecord(raw)
      if (maybe?.kind === Kind.Cmd && typeof maybe.msgId === 'string') {
        this.rejectInbound(maybe.msgId, ErrorCode.ProtoMalformed, parsed.error)
      } else {
        this.closeProtocol(1002, parsed.error)
      }
      return
    }
    const env = parsed.envelope
    if (env.proto !== PROTO_VERSION) {
      this.rejectInbound(env.msgId, ErrorCode.ProtoMalformed, `不支持 proto=${env.proto}`)
      return
    }
    if (!KNOWN_KINDS.has(env.kind)) {
      this.rejectInbound(env.msgId, ErrorCode.ProtoUnsupportedKind, `未知 kind=${env.kind}`)
      return
    }
    const kind = env.kind as Kind
    if (kind !== Kind.Cmd && kind !== Kind.Cancel) {
      const issues = validateKindBody(kind, env.body)
      if (issues.length > 0) {
        this.closeProtocol(1002, `body 违反契约: ${formatIssues(issues)}`)
        return
      }
    }

    switch (kind) {
      case Kind.Welcome:
        await this.onWelcome(env.body, source)
        break
      case Kind.Bye:
        this.onBye(env.body)
        break
      case Kind.Pong:
        this.onPong(env.session)
        break
      case Kind.Cmd:
        await this.dispatcher.handleCmd(env.msgId, env.session, this.session, env.body)
        break
      case Kind.Cancel:
        this.dispatcher.handleCancel(env.msgId, env.session, this.session, env.body)
        break
      case Kind.Ack:
        await this.onAck(env.body)
        break
      case Kind.Query:
        if (this.witnessEnabledForSession && env.session === this.session) {
          await this.dispatcher.handleQuery((env.body as QueryBody).ref, this.session)
        }
        break
      default:
        // 已知但本批未在脑→手方向启用的 kind，响亮拒绝，禁止默认成功。
        this.rejectInbound(env.msgId, ErrorCode.ProtoUnsupportedKind, `手不接受 kind=${env.kind}`)
    }
  }

  private async onWelcome(rawBody: unknown, source: WebSocket): Promise<void> {
    if (this.ws !== source) return
    const body = rawBody as WelcomeBody
    if (this.helloTimer) {
      clearTimeout(this.helloTimer)
      this.helloTimer = null
    }
    this.applyWelcomeLimits(body.limits)
    this.applySensorConfig(body.sensors)
    this.heartbeatIntervalMs = body.hb.intervalMs
    this.resetPongWatch()
    this.session = body.session
    this.phase = 'session'
    this.startReconnectStableWindow(source, body.session)
    // 四阶段恢复的第一阶段必须先于 receiveChain 中后续 query:先补投持久 outbox。
    if (this.witnessEnabledForSession) await this.flushDurableResults()
    if (this.ws !== source || source.readyState !== WebSocket.OPEN) return
    this.flushPendingResults()
    this.startHeartbeat()
    console.log('[hand] 会话建立', body.session)
  }

  private onBye(rawBody: unknown): void {
    const body = rawBody as ByeBody
    console.log('[hand] bye', body.code, body.message ?? '')
    // bye 已宣告本 session 结束；即使 close 事件稍后才到，也不能再把它算作稳定连接。
    this.invalidateReconnectStableWindow()
    if (body.code === ByeCode.Superseded) {
      // 已被更新的连接顶替:别立刻重连(否则与顶替者 ping-pong),退避到上限慢试。
      this.reconnectDelay = RECONNECT.capMs
    }
    // 其余关链均由 onclose 按基础设施退避自动重连。
  }

  private async onAck(rawBody: unknown): Promise<void> {
    const body = rawBody as AckBody
    if (body.status !== AckStatus.Accepted && body.status !== AckStatus.Duplicate) return
    // 对 result 的 accepted/duplicate 等价：脑已持久化，可删手侧内存待投。
    this.pendingResults.delete(body.ref)
    if ((this.witness.advertisement()?.outboxPending ?? 0) > 0) {
      try {
        await this.witness.acknowledgeResult(body.ref)
      } catch (error) {
        // 删除失败就保留并在下次重连补投；绝不能因 ack 丢掉唯一终局证词。
        console.warn('[hand] result ack 后清理 outbox 失败，将继续保留', error)
      }
    }
  }

  private onClose(): void {
    this.connectGeneration += 1
    this.invalidateReconnectStableWindow()
    this.stopHeartbeat()
    if (this.helloTimer) clearTimeout(this.helloTimer)
    this.helloTimer = null
    this.session = null
    this.witnessEnabledForSession = false
    this.phase = 'closed'
    this.ws = null
    this.scheduleReconnect()
  }

  private startReconnectStableWindow(source: WebSocket, session: string): void {
    this.invalidateReconnectStableWindow()
    const generation = this.reconnectStableGeneration
    this.reconnectStableTimer = setTimeout(() => {
      if (
        this.reconnectStableGeneration !== generation ||
        this.ws !== source || source.readyState !== WebSocket.OPEN ||
        this.phase !== 'session' || this.session !== session
      ) return
      this.reconnectStableTimer = null
      this.reconnectDelay = RECONNECT.baseMs
    }, RECONNECT_STABLE_MS)
  }

  private invalidateReconnectStableWindow(): void {
    this.reconnectStableGeneration += 1
    if (this.reconnectStableTimer) {
      clearTimeout(this.reconnectStableTimer)
      this.reconnectStableTimer = null
    }
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer) return
    const jitter = 1 + (Math.random() * 2 - 1) * RECONNECT.jitter
    const delay = Math.min(this.reconnectDelay * jitter, RECONNECT.capMs)
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      this.connect()
    }, delay)
    this.reconnectDelay = Math.min(this.reconnectDelay * RECONNECT.factor, RECONNECT.capMs)
  }

  private startHeartbeat(): void {
    this.stopHeartbeat()
    this.resetPongWatch()
    this.scheduleHeartbeatTick()
  }

  private stopHeartbeat(): void {
    if (this.hbTimer) {
      clearTimeout(this.hbTimer)
      this.hbTimer = null
    }
    this.resetPongWatch()
  }

  private scheduleHeartbeatTick(): void {
    if (this.phase !== 'session') return
    this.hbTimer = setTimeout(() => {
      this.hbTimer = null
      this.heartbeatTick()
    }, heartbeatDelayMs(this.heartbeatIntervalMs))
  }

  private heartbeatTick(): void {
    if (this.phase !== 'session') return
    if (this.awaitingPong) {
      this.missedPongs += 1
      if (this.missedPongs >= 2) {
        console.warn('[hand] 连续两次心跳未收到 pong，主动重连')
        this.stopHeartbeat()
        try { this.ws?.close() } catch { /* onclose 统一续连 */ }
        return
      }
    }

    const execution = this.dispatcher.snapshot()
    const body: PingBody = {
      queueDepth: execution.queueDepth,
      inFlight: execution.inFlight,
    }
    body.contexts = this.contexts
    body.sensors = this.currentPingSensors()
    const witness = this.witnessEnabledForSession ? this.witness.advertisement() : null
    if (witness) Object.assign(body, witness)
    if (this.rawSend(Kind.Ping, this.session, body) === 'sent') this.awaitingPong = true
    this.scheduleHeartbeatTick()
  }

  private onPong(envelopeSession: string | null): void {
    if (this.phase !== 'session' || envelopeSession !== this.session) return
    this.resetPongWatch()
  }

  private resetPongWatch(): void {
    this.awaitingPong = false
    this.missedPongs = 0
  }

  private rawSend(kind: Kind, session: string | null, body: unknown): SendOutcome {
    const bodyIssues = validateKindBody(kind, body)
    if (bodyIssues.length > 0) {
      console.error('[hand] 拒发违反生成契约的 body', kind, formatIssues(bodyIssues))
      return 'dropped'
    }
    const envelope: Envelope = {
      proto: PROTO_VERSION,
      kind,
      msgId: newMsgId(),
      session,
      ts: Date.now(),
      attempt: 1,
      body,
    }
    const encoded = this.encodeEnvelope(envelope)
    if (!encoded) return 'dropped'
    if (validateFrameSize(encoded.text, this.maxMsgBytes).length > 0) {
      console.warn('[hand] 拒发超限帧', kind, encoded.bytes, this.maxMsgBytes)
      return 'tooLarge'
    }

    if (kind === Kind.Result) {
      if (this.pendingResults.size >= RESULT_QUEUE_CAP) {
        // 正常背压下最多只有 17 条 accepted 未收束；到 50 说明对端长期不 ack。
        // 不把终局改写为静默成功，保留 dedup 缓存供同 bootId 重投命中，并响亮断链。
        console.error('[hand] result 内存队列已满', RESULT_QUEUE_CAP)
        this.ws?.close()
        return 'dropped'
      }
      const pending: PendingResult = { envelope, sent: false }
      this.pendingResults.set(envelope.msgId, pending)
      const sent = this.sendEncoded(encoded.text)
      pending.sent = sent
      return sent ? 'sent' : 'queued'
    }

    return this.sendEncoded(encoded.text) ? 'sent' : 'dropped'
  }

  private async sendDurableResult(
    session: string | null,
    body: ResultBody,
    commitIdemKey?: string,
  ): Promise<SendOutcome> {
    if (!session) {
      console.error('[hand] 真实 SX result 缺少命令会话，拒绝持久与发送')
      return 'dropped'
    }
    const bodyIssues = validateKindBody(Kind.Result, body)
    if (bodyIssues.length > 0) {
      console.error('[hand] 拒发违反生成契约的持久 result', formatIssues(bodyIssues))
      return 'dropped'
    }
    const envelope: ResultEnvelope = {
      proto: PROTO_VERSION,
      kind: Kind.Result,
      msgId: newMsgId(),
      session,
      ts: Date.now(),
      attempt: 1,
      body,
    }
    const encoded = this.encodeEnvelope(envelope)
    if (!encoded) return 'dropped'
    if (validateFrameSize(encoded.text, this.maxMsgBytes).length > 0) return 'tooLarge'
    try {
      // 先持久 outbox，再进行第一次 WS write；顺序不可交换。commitIdemKey
      // 表示 handler 已成功越过 attempting 写点，与 ResultBody 是 ok 或 failed 无关。
      if (commitIdemKey) await this.witness.commitAndEnqueue(commitIdemKey, envelope)
      else await this.witness.enqueueResult(envelope)
    } catch (error) {
      console.error('[hand] result 无法先入持久 outbox，拒绝发送', error)
      try { this.ws?.close() } catch { /* onclose 统一重连 */ }
      return 'dropped'
    }
    return this.sendEncoded(encoded.text) ? 'sent' : 'queued'
  }

  private encodeEnvelope(envelope: Envelope): { text: string; bytes: number } | null {
    try {
      const bodyText = JSON.stringify(envelope.body)
      const bodyBytes = utf8ByteLength(bodyText)
      if (bodyBytes > this.inlineBytes && envelope.kind !== Kind.Ping) {
        // inlineBytes 是建议线而非硬拒绝线；完整 envelope 仍以 maxMsgBytes 为唯一硬边界。
        console.warn('[hand] body 超过 inlineBytes 建议线', envelope.kind, bodyBytes, this.inlineBytes)
      }
      const text = JSON.stringify(envelope)
      return { text, bytes: utf8ByteLength(text) }
    } catch (error) {
      console.error('[hand] 信封不可序列化', envelope.kind, error)
      return null
    }
  }

  private sendEncoded(text: string): boolean {
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return false
    try {
      ws.send(text)
      return true
    } catch (error) {
      console.warn('[hand] WS 写入失败', error)
      try { ws.close() } catch { /* ignore */ }
      return false
    }
  }

  private flushPendingResults(): void {
    for (const [msgId, pending] of this.pendingResults) {
      const envelope: Envelope = {
        ...pending.envelope,
        // 跨会话重投只更新信封传输字段；msgId 与终局 body 保持不变。
        session: this.session,
        ts: Date.now(),
        attempt: pending.sent ? pending.envelope.attempt + 1 : pending.envelope.attempt,
      }
      const encoded = this.encodeEnvelope(envelope)
      if (!encoded || validateFrameSize(encoded.text, this.maxMsgBytes).length > 0) {
        console.error('[hand] 待投 result 在新会话限制下超限', msgId)
        continue
      }
      pending.envelope = envelope
      pending.sent = this.sendEncoded(encoded.text)
    }
  }

  private async flushDurableResults(): Promise<void> {
    try {
      const pending = await this.witness.listOutbox()
      for (const entry of pending) {
        if (!this.session) return
        const envelope = await this.witness.nextOutboxAttempt(entry.message.msgId, this.session)
        if (!envelope) continue
        const encoded = this.encodeEnvelope(envelope)
        if (!encoded || validateFrameSize(encoded.text, this.maxMsgBytes).length > 0) {
          console.error('[hand] 持久 outbox result 在当前限制下超限', envelope.msgId)
          continue
        }
        // 跨会话补投更新 session/attempt/ts；msgId 与终局 body 保持不变。
        this.sendEncoded(encoded.text)
      }
    } catch (error) {
      // 补投失败时不进入 query 的伪完成路径；断链后由退避重连再次尝试阶段 1。
      console.error('[hand] 持久 outbox 补投失败', error)
      try { this.ws?.close() } catch { /* onclose 统一重连 */ }
    }
  }

  private rejectInbound(ref: string, code: string, message: string): void {
    this.rawSend(Kind.Ack, this.session, {
      ref,
      status: AckStatus.Rejected,
      error: { code, message, retryable: Retryable.No, sideEffect: SideEffect.None },
    })
  }

  private applyWelcomeLimits(limits: Limits): void {
    // 256KB 是协议绝对硬顶；脑可向下收紧，不能把手协商到更大的无界帧。
    this.maxMsgBytes = Math.min(limits.maxMsgBytes, DEFAULTS.maxMsgBytes)
    this.inlineBytes = Math.min(limits.inlineBytes, this.maxMsgBytes)
  }

  private applySensorConfig(sensors: SensorParams | undefined): void {
    this.currentSensorConfig = Object.freeze(sensors ? { ...sensors } : {})
    for (const listener of this.sensorConfigListeners) listener(this.currentSensorConfig)
  }

  private rememberCommandContext(context: Readonly<CmdContext>): void {
    const frozen = Object.freeze({ ...context })
    this.commandContexts.set(frozen.platform, frozen)
    for (const listener of this.commandContextListeners) listener(frozen)
  }

  private currentPingSensors(): PingSensors | null {
    if (this.sensorSnapshot === null) return null
    const reading = this.sensorSnapshot.unreadTotal
    return {
      unreadTotal: reading === null
        ? null
        : {
            value: reading.value,
            // generated SensorReading 上限为 24h；更老仍表示“至少一天前”，不能让陈旧
            // 缓存把整个基础设施 ping 校验掉。
            observedAgoMs: Math.min(86_400_000, Math.max(0, Date.now() - reading.observedAt)),
          },
    }
  }

  private closeProtocol(code: number, reason: string): void {
    const ws = this.ws
    if (!ws) return
    try { ws.close(code, reason.slice(0, 120)) } catch { ws.close() }
  }
}

export function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

// session 心跳按 welcome.hb.intervalMs 的 ±20% 独立抖动；注入 random 仅用于纯函数边界测试。
export function heartbeatDelayMs(intervalMs: number, random: () => number = Math.random): number {
  const sample = Math.min(1, Math.max(0, random()))
  return Math.max(1, Math.round(intervalMs * (0.8 + sample * 0.4)))
}

function isWitnessedCapability(capability: string): boolean {
  const separator = capability.lastIndexOf('@')
  if (separator <= 0) return false
  const name = capability.slice(0, separator) as keyof typeof PRIMITIVE_META
  const meta = PRIMITIVE_META[name]
  return meta?.batch === Batch.X && meta.class === CmdClass.Effectful
}

interface DecodedFrame {
  text: string
  bytes: number
  wire: string | Uint8Array
}

type DecodeFrameResult =
  | { kind: 'ok'; frame: DecodedFrame }
  | { kind: 'tooLarge' }
  | { kind: 'invalid' }

async function decodeFrame(data: unknown, maxBytes: number): Promise<DecodeFrameResult> {
  if (typeof data === 'string') {
    const bytes = utf8ByteLength(data)
    return bytes > maxBytes
      ? { kind: 'tooLarge' }
      : { kind: 'ok', frame: { text: data, bytes, wire: data } }
  }
  if (data instanceof ArrayBuffer) {
    const wire = new Uint8Array(data)
    return decodeBytes(wire, maxBytes)
  }
  if (ArrayBuffer.isView(data)) {
    const view = new Uint8Array(data.buffer, data.byteOffset, data.byteLength)
    return decodeBytes(view, maxBytes)
  }
  if (typeof Blob !== 'undefined' && data instanceof Blob) {
    if (data.size > maxBytes) return { kind: 'tooLarge' }
    const buffer = await data.arrayBuffer()
    const wire = new Uint8Array(buffer)
    return decodeBytes(wire, maxBytes)
  }
  return { kind: 'invalid' }
}

function decodeBytes(wire: Uint8Array, maxBytes: number): DecodeFrameResult {
  if (wire.byteLength > maxBytes) return { kind: 'tooLarge' }
  try {
    const text = new TextDecoder('utf-8', { fatal: true }).decode(wire)
    return { kind: 'ok', frame: { text, bytes: wire.byteLength, wire } }
  } catch {
    return { kind: 'invalid' }
  }
}

function parseEnvelope(raw: unknown): { envelope: IncomingEnvelope } | { error: string } {
  const env = asRecord(raw)
  if (!env) return { error: '信封必须是对象' }
  if (!Number.isInteger(env.proto)) return { error: 'proto 必须是整数' }
  if (typeof env.kind !== 'string' || env.kind.length === 0) return { error: 'kind 必须是非空字符串' }
  if (typeof env.msgId !== 'string' || env.msgId.length === 0) return { error: 'msgId 必须是非空字符串' }
  if (env.session !== null && typeof env.session !== 'string') return { error: 'session 必须是 string|null' }
  if (!Number.isSafeInteger(env.ts)) return { error: 'ts 必须是安全整数' }
  if (!Number.isInteger(env.attempt) || (env.attempt as number) < 1) return { error: 'attempt 必须是正整数' }
  if (!asRecord(env.body)) return { error: 'body 必须是对象' }
  return {
    envelope: {
      proto: env.proto as number,
      kind: env.kind,
      msgId: env.msgId,
      session: env.session as string | null,
      ts: env.ts as number,
      attempt: env.attempt as number,
      body: env.body,
    },
  }
}

function asRecord(value: unknown): Record<string, unknown> | null {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null
  return value as Record<string, unknown>
}

function formatIssues(issues: readonly ValidationIssue[]): string {
  return issues.map((issue) => `${issue.path}: ${issue.message} (${issue.rule})`).join('; ')
}
