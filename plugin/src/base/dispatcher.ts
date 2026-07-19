// 命令分发器(base 层,手侧唯一命令入口)。测试端与正式调度器共用此路径(宪法 3)。
// 职责:校验/去重 -> FIFO/背压 -> 单执行槽 -> progress/result/cancel。
import {
  AckStatus,
  CmdBody,
  CmdClass,
  CmdContext,
  DEFAULTS,
  ErrorBody,
  ErrorCode,
  Kind,
  PRIMITIVE_META,
  ProgressBody,
  ResultBody,
  ResultStatus,
  Retryable,
  SideEffect,
  ValidationIssue,
  validateKindBody,
  validatePrimitiveData,
} from './protocol'
import { lookup, Primitive, PrimitiveResult } from '../program/registry'

export type SendOutcome = 'sent' | 'queued' | 'dropped' | 'tooLarge'

// 送帧回调:由 connection 提供,负责生成信封 msgId、大小检查与写 WS。
export type SendFn = (kind: Kind, session: string | null, body: unknown) => SendOutcome | void
export type CommandContextObserver = (context: Readonly<CmdContext>) => void

interface QueueItem {
  msgId: string
  session: string | null
  body: CmdBody
  primitive: Primitive
}

interface InFlight {
  item: QueueItem
  controller: AbortController
  startedAt: number
  budgetEndsAt: number
  cancelRequested: boolean
  pastCancellationPoint: boolean
  terminalSent: boolean
}

interface DedupEntry {
  state: 'queued' | 'executing' | 'done'
  cmdClass: CmdClass
  resultBody?: ResultBody // 终局 result body,供重复投递重放
}

export interface DispatcherSnapshot {
  queueDepth: number
  inFlight: string | null
}

// program 可选择使用的合作式执行钩子。当前 debug 原语只依赖旧字段；真实长原语接入时
// 应在分页/滚动边界调用 checkpoint(),并在不可逆动作前最后一刻调用 beforeSideEffect()。
export interface ExecutionHooks {
  cmdMsgId: string
  deadlineMs: number
  readonly commandContext: Readonly<CmdContext> | undefined
  signal: AbortSignal
  progress: (stage: string, pct?: number) => void
  checkpoint: () => void
  beforeSideEffect: () => void
}

type StopKind = 'canceled' | 'expired' | 'budget'

class StopExecution extends Error {
  constructor(readonly stopKind: StopKind) {
    super(stopKind)
    this.name = 'StopExecution'
  }
}

const DEDUP_CAP = DEFAULTS.handDedupLru
const QUEUE_CAP = DEFAULTS.handQueueDepth
export class Dispatcher {
  // 去重表:内存,作用域=SW 一次生命(bootId)。键=cmd msgId。禁写 chrome.storage(宪法禁令 2/16)。
  private dedup = new Map<string, DedupEntry>()
  private cancelDedup = new Map<string, true>()
  private queue: QueueItem[] = []
  private inFlight: InFlight | null = null
  private sessionCaps: ReadonlySet<string> | null = null

  constructor(
    private send: SendFn,
    private readonly observeCommandContext?: CommandContextObserver,
  ) {}

  snapshot(): DispatcherSnapshot {
    return { queueDepth: this.queue.length, inFlight: this.inFlight?.item.msgId ?? null }
  }

  // hello 时冻结本会话能力快照；program 若热更，必须重连后才能接收新增原语。
  setSessionCapabilities(caps: readonly string[]): void {
    this.sessionCaps = new Set(caps)
  }

  // 收到 cmd 信封。去重必须先于 session 校验；只有 accepted 的命令才进 dedup。
  handleCmd(
    cmdMsgId: string,
    envelopeSession: string | null,
    activeSession: string | null,
    rawBody: unknown,
  ): void {
    const responseSession = activeSession

    // 1) 去重:命中即重新 ack,有终局则重放 result；不再检查 session。
    const seen = this.dedup.get(cmdMsgId)
    if (seen) {
      this.touchDedup(cmdMsgId, seen)
      this.send(Kind.Ack, responseSession, { ref: cmdMsgId, status: AckStatus.Duplicate })
      if (seen.state === 'done' && seen.resultBody) {
        const replay = this.asReplay(seen.resultBody)
        // 重投信封的 session 是可变传输字段；跨会话命中 dedup 时用手的当前会话，
        // result body 仍是原命令的同一终局。
        const outcome = this.send(Kind.Result, responseSession, replay)
        if (outcome === 'tooLarge') {
          const compact = this.tooLargeResult(cmdMsgId, seen.cmdClass)
          this.remember(cmdMsgId, { state: 'done', cmdClass: seen.cmdClass, resultBody: compact })
          this.send(Kind.Result, responseSession, this.asReplay(compact))
        }
      }
      return
    }

    // 2) 未见过的命令必须属于当前会话。拒收不进 dedup,给同 msgId 换会话重投留路。
    if (!activeSession || envelopeSession !== activeSession) {
      this.reject(cmdMsgId, responseSession, this.protocolError(
        ErrorCode.StaleSession,
        '命令 session 不是手的当前会话',
        Retryable.Yes,
      ))
      return
    }

    const parsed = this.parseCmd(rawBody)
    if ('error' in parsed) {
      this.reject(cmdMsgId, responseSession, parsed.error)
      return
    }
    const body = parsed.body

    // 3) caps/name/ver/类约束校验。
    const primitive = lookup(body.name)
    if (!primitive) {
      this.reject(cmdMsgId, responseSession, this.protocolError(
        ErrorCode.ProtoUnsupportedCmd,
        `未知原语 ${body.name}`,
        Retryable.No,
      ))
      return
    }
    const meta = PRIMITIVE_META[body.name as keyof typeof PRIMITIVE_META]
    if (meta && body.ver !== meta.ver) {
      this.reject(cmdMsgId, responseSession, this.protocolError(
        ErrorCode.ProtoUnsupportedCmd,
        `原语版本不支持 ${body.name}@${body.ver}`,
        Retryable.No,
      ))
      return
    }
    if (meta && primitive.class !== meta.class) {
      this.reject(cmdMsgId, responseSession, this.protocolError(
        ErrorCode.ProtoUnsupportedCmd,
        `原语运行时 class 与生成契约不一致 ${body.name}`,
        Retryable.No,
      ))
      return
    }
    if (this.sessionCaps && !this.sessionCaps.has(`${body.name}@${body.ver}`)) {
      this.reject(cmdMsgId, responseSession, this.protocolError(
        ErrorCode.ProtoUnsupportedCmd,
        `原语未在本会话 hello 中声明 ${body.name}@${body.ver}`,
        Retryable.No,
      ))
      return
    }

    const item: QueueItem = {
      msgId: cmdMsgId,
      session: envelopeSession,
      body,
      primitive,
    }

    // 4) 已过期命令不占 FIFO 容量，也不能被 QUEUE_FULL 改写：先 accepted，
    // 随即从不可执行终局分支回 expired，handler 调用次数恒为 0。
    if (Date.now() > body.deadline) {
      this.publishCommandContext(body)
      this.remember(cmdMsgId, { state: 'queued', cmdClass: primitive.class })
      this.send(Kind.Ack, responseSession, { ref: cmdMsgId, status: AckStatus.Accepted })
      this.finish(item, this.resultBody(cmdMsgId, { status: ResultStatus.Expired }, 0))
      return
    }

    // 5) 真 FIFO 背压。queueDepth 不含正在执行的单槽,与 ping.inFlight 分开上报。
    if (this.queue.length >= QUEUE_CAP) {
      this.reject(cmdMsgId, responseSession, this.protocolError(
        ErrorCode.QueueFull,
        `手侧 FIFO 已满(${QUEUE_CAP})`,
        Retryable.Yes,
      ))
      return
    }

    // 6) accepted 只表示已入队；排队期间仍可能过期，出队时会再次检查。
    this.publishCommandContext(body)
    this.queue.push(item)
    this.remember(cmdMsgId, { state: 'queued', cmdClass: primitive.class })
    this.send(Kind.Ack, responseSession, { ref: cmdMsgId, status: AckStatus.Accepted })
    this.pump()
  }

  // cancel 自身也要 ack。目标 queued 时就地移除；executing 时仅发合作式取消信号。
  // handler 正常给出结果时始终以原 result 为准(result wins cancel)。
  handleCancel(
    cancelMsgId: string,
    envelopeSession: string | null,
    activeSession: string | null,
    rawBody: unknown,
  ): void {
    if (this.cancelDedup.has(cancelMsgId)) {
      this.touchCancelDedup(cancelMsgId)
      this.send(Kind.Ack, activeSession, { ref: cancelMsgId, status: AckStatus.Duplicate })
      return
    }
    if (!activeSession || envelopeSession !== activeSession) {
      this.reject(cancelMsgId, activeSession, this.protocolError(
        ErrorCode.StaleSession,
        'cancel session 不是手的当前会话',
        Retryable.Yes,
      ))
      return
    }
    const issues = validateKindBody(Kind.Cancel, rawBody)
    if (issues.length > 0) {
      this.reject(cancelMsgId, activeSession, this.validationError(ErrorCode.ProtoBadArgs, issues))
      return
    }
    const body = rawBody as import('./protocol').CancelBody

    this.rememberCancel(cancelMsgId)
    this.send(Kind.Ack, activeSession, { ref: cancelMsgId, status: AckStatus.Accepted })

    const queuedIndex = this.queue.findIndex((item) => item.msgId === body.ref)
    if (queuedIndex >= 0) {
      const [item] = this.queue.splice(queuedIndex, 1)
      this.finish(item, this.resultBody(item.msgId, { status: ResultStatus.Canceled }, 0))
      return
    }

    const running = this.inFlight
    if (!running || running.item.msgId !== body.ref || running.terminalSent) return
    running.cancelRequested = true
    // 过了不可逆动作安全点,取消只记请求、不打断；原结果获胜。
    if (!running.pastCancellationPoint && !running.controller.signal.aborted) {
      running.controller.abort(new StopExecution('canceled'))
    }
  }

  private pump(): void {
    if (this.inFlight) return
    const item = this.queue.shift()
    if (!item) return

    // 出队 deadline 检查。过期命令 terminal expired,handler 零调用。
    if (Date.now() > item.body.deadline) {
      this.finish(item, this.resultBody(item.msgId, { status: ResultStatus.Expired }, 0))
      queueMicrotask(() => this.pump())
      return
    }

    const startedAt = Date.now()
    const running: InFlight = {
      item,
      controller: new AbortController(),
      startedAt,
      budgetEndsAt: startedAt + item.body.execBudgetMs,
      cancelRequested: false,
      pastCancellationPoint: false,
      terminalSent: false,
    }
    this.inFlight = running
    this.remember(item.msgId, { state: 'executing', cmdClass: item.primitive.class })
    void this.execute(running)
  }

  private async execute(running: InFlight): Promise<void> {
    const { item } = running
    let timer: ReturnType<typeof setTimeout> | null = null
    let stopResolve: ((kind: StopKind) => void) | null = null
    const stopPromise = new Promise<StopKind>((resolve) => { stopResolve = resolve })

    // deadline 是绝对硬上限；execBudget 是从真正出队开始计的单次执行预算。
    const deadlineDelay = Math.max(0, item.body.deadline - Date.now())
    const budgetDelay = Math.max(0, item.body.execBudgetMs)
    const timerKind: StopKind = deadlineDelay <= budgetDelay ? 'expired' : 'budget'
    const timerDelay = Math.min(deadlineDelay, budgetDelay)
    timer = setTimeout(() => {
      if (!running.controller.signal.aborted) {
        running.controller.abort(new StopExecution(timerKind))
      }
      stopResolve?.(timerKind)
    }, timerDelay)

    const hooks: ExecutionHooks = {
      cmdMsgId: item.msgId,
      deadlineMs: item.body.deadline,
      commandContext: item.body.context ? Object.freeze({ ...item.body.context }) : undefined,
      signal: running.controller.signal,
      progress: (stage, pct) => this.reportProgress(running, stage, pct),
      checkpoint: () => this.checkpoint(running),
      beforeSideEffect: () => {
        // deadline 双查的第二个位置必须紧贴不可逆动作。
        this.checkpoint(running)
        running.pastCancellationPoint = true
      },
    }

    let executionPromise: Promise<Awaited<ReturnType<Primitive['handler']>>>
    try {
      // 立即进入 handler，使它能在 handleCmd 返回前安装 AbortSignal 监听；同步 throw 仍转成 rejection。
      executionPromise = Promise.resolve(item.primitive.handler(item.body.args, hooks))
    } catch (error) {
      executionPromise = Promise.reject(error)
    }
    const handlerPromise = executionPromise
      .then((outcome) => ({ kind: 'outcome' as const, outcome }))
      .catch((error: unknown) => ({ kind: 'error' as const, error }))

    const first = await Promise.race([
      handlerPromise,
      stopPromise.then((stopKind) => ({ kind: 'stop' as const, stopKind })),
    ])

    if (first.kind === 'outcome') {
      if (timer) clearTimeout(timer)
      if (first.outcome !== 'silent') {
        // cancel 已到但 handler 已经正常完成时,原始 result 赢。
        const checked = this.validateOutcome(running, first.outcome)
        this.finish(item, this.resultBody(item.msgId, checked, Date.now() - running.startedAt))
        running.terminalSent = true
      }
    } else if (first.kind === 'error') {
      if (timer) clearTimeout(timer)
      const result = this.resultForThrown(running, first.error)
      this.finish(item, this.resultBody(item.msgId, result, Date.now() - running.startedAt))
      running.terminalSent = true
    } else {
      // 不可信 handler 可能忽略 AbortSignal。先响亮终局,但保持执行槽隔离，直到它真正退出；
      // 绝不让下一条命令与僵尸 handler 并发操作页面。
      const result = this.resultForStop(running, first.stopKind)
      this.finish(item, this.resultBody(item.msgId, result, Date.now() - running.startedAt))
      running.terminalSent = true
      await handlerPromise // 晚到结果只作本地收敛,不能覆盖已发送终局
    }

    if (timer) clearTimeout(timer)
    if (this.inFlight === running) this.inFlight = null
    this.pump()
  }

  private checkpoint(running: InFlight): void {
    if (Date.now() > running.item.body.deadline) throw new StopExecution('expired')
    if (Date.now() > running.budgetEndsAt) throw new StopExecution('budget')
    if (running.controller.signal.aborted) {
      const reason = running.controller.signal.reason
      if (reason instanceof StopExecution) throw reason
      throw new StopExecution(running.cancelRequested ? 'canceled' : 'budget')
    }
  }

  private reportProgress(running: InFlight, stage: string, pct?: number): void {
    if (this.inFlight !== running || running.terminalSent || running.controller.signal.aborted) return
    if (typeof stage !== 'string' || stage.length === 0) return
    const body: ProgressBody = { ref: running.item.msgId, stage }
    if (typeof pct === 'number' && Number.isInteger(pct)) body.pct = Math.max(0, Math.min(100, pct))
    if (validateKindBody(Kind.Progress, body).length > 0) return
    // progress 是 QoS0/租约活性触碰：无 ack、无重发；stage 只供人读。
    this.send(Kind.Progress, running.item.session, body)
  }

  private resultForThrown(running: InFlight, error: unknown): PrimitiveResult {
    if (error instanceof StopExecution) return this.resultForStop(running, error.stopKind)
    if (running.controller.signal.aborted && running.controller.signal.reason instanceof StopExecution) {
      return this.resultForStop(running, running.controller.signal.reason.stopKind)
    }
    return {
      status: ResultStatus.Failed,
      error: {
        code: ErrorCode.InternalHand,
        message: humanMessage(error),
        retryable: Retryable.ManualOnly,
        // INTERNAL_HAND 本身表示手无法证明发生了什么；契约只允许 possible。
        sideEffect: SideEffect.Possible,
      },
    }
  }

  private resultForStop(running: InFlight, stopKind: StopKind): PrimitiveResult {
    if (stopKind === 'canceled' && !running.pastCancellationPoint) {
      return { status: ResultStatus.Canceled }
    }
    if (stopKind === 'expired' && !running.pastCancellationPoint) {
      return { status: ResultStatus.Expired }
    }
    return {
      status: ResultStatus.Failed,
      error: {
        code: ErrorCode.ExecTimeoutHand,
        message: stopKind === 'expired' ? '执行跨过绝对 deadline' : '手侧 execBudget 已耗尽',
        retryable: running.item.primitive.class === CmdClass.Effectful
          ? Retryable.ManualOnly
          : Retryable.Yes,
        sideEffect: running.item.primitive.class === CmdClass.Effectful || running.pastCancellationPoint
          ? SideEffect.Possible
          : SideEffect.None,
      },
    }
  }

  private finish(item: QueueItem, resultBody: ResultBody): void {
    resultBody = this.ensureResultBody(item, resultBody)
    this.remember(item.msgId, { state: 'done', cmdClass: item.primitive.class, resultBody })
    const sent = this.send(Kind.Result, item.session, resultBody)
    if (sent !== 'tooLarge') return

    // 完整 envelope 超硬上限时,用小型失败终局替代，禁止静默截断 data/evidence。
    const compact = this.tooLargeResult(item.msgId, item.primitive.class)
    this.remember(item.msgId, { state: 'done', cmdClass: item.primitive.class, resultBody: compact })
    this.send(Kind.Result, item.session, compact)
  }

  private ensureResultBody(item: QueueItem, body: ResultBody): ResultBody {
    const issues = validateKindBody(Kind.Result, body)
    if (issues.length === 0) return body
    return this.resultBody(item.msgId, {
      status: ResultStatus.Failed,
      error: {
        code: ErrorCode.InternalHand,
        message: humanMessage(`原语 result 违反生成契约: ${formatIssues(issues)}`),
        retryable: Retryable.ManualOnly,
        sideEffect: SideEffect.Possible,
      },
    }, Math.min(body.execMs, DEFAULTS.execBudgetDefaultMs.capMs))
  }

  private tooLargeResult(ref: string, cmdClass: CmdClass): ResultBody {
    return this.resultBody(ref, {
      status: ResultStatus.Failed,
      error: {
        code: ErrorCode.ProtoMsgTooLarge,
        message: 'result 完整信封超过 maxMsgBytes，原 data/evidence 未发送',
        retryable: Retryable.No,
        sideEffect: cmdClass === CmdClass.Effectful ? SideEffect.Possible : SideEffect.None,
      },
    }, 0)
  }

  private resultBody(ref: string, result: PrimitiveResult, execMs: number): ResultBody {
    const body: ResultBody = {
      ref,
      status: result.status,
      replayed: false,
      execMs,
    }
    if (result.data !== undefined) body.data = result.data
    if (result.error !== undefined) body.error = result.error as ErrorBody
    if (result.evidence !== undefined) body.evidence = result.evidence
    return body
  }

  private asReplay(resultBody: ResultBody): ResultBody {
    return { ...resultBody, replayed: true }
  }

  private parseCmd(rawBody: unknown): { body: CmdBody } | { error: ErrorBody } {
    const issues = validateKindBody(Kind.Cmd, rawBody)
    if (issues.length === 0) return { body: rawBody as CmdBody }
    const unsupported = issues.some((issue) => issue.rule === 'primitive' || issue.rule === 'version')
    return {
      error: this.validationError(
        unsupported ? ErrorCode.ProtoUnsupportedCmd : ErrorCode.ProtoBadArgs,
        issues,
      ),
    }
  }

  private validateOutcome(running: InFlight, outcome: PrimitiveResult): PrimitiveResult {
    if (outcome.status !== ResultStatus.Ok) return outcome
    const item = running.item
    const issues = validatePrimitiveData(item.body.name, item.body.ver, outcome.data)
    if (issues.length === 0) return outcome
    return {
      status: ResultStatus.Failed,
      error: {
        code: ErrorCode.InternalHand,
        message: humanMessage(`原语 data 违反生成契约: ${formatIssues(issues)}`),
        retryable: Retryable.ManualOnly,
        sideEffect: SideEffect.Possible,
      },
    }
  }

  private reject(ref: string, session: string | null, error: ErrorBody): void {
    this.send(Kind.Ack, session, { ref, status: AckStatus.Rejected, error })
  }

  private protocolError(code: ErrorCode, message: string, retryable: Retryable): ErrorBody {
    return { code, message: humanMessage(message), retryable, sideEffect: SideEffect.None }
  }

  private validationError(code: ErrorCode, issues: readonly ValidationIssue[]): ErrorBody {
    return this.protocolError(code, formatIssues(issues), Retryable.No)
  }

  private publishCommandContext(body: CmdBody): void {
    if (!body.context || !this.observeCommandContext) return
    try {
      this.observeCommandContext(Object.freeze({ ...body.context }))
    } catch (error) {
      // 观测接缝只更新 SW 内存健康，不得反向影响命令是否 accepted。
      console.warn('[hand] command context 内存观测失败', error)
    }
  }

  private remember(key: string, entry: DedupEntry): void {
    this.dedup.delete(key)
    this.dedup.set(key, entry)
    this.trimDedup()
  }

  private touchDedup(key: string, entry: DedupEntry): void {
    this.dedup.delete(key)
    this.dedup.set(key, entry)
  }

  private trimDedup(): void {
    if (this.dedup.size <= DEDUP_CAP) return
    // 未终局条目绝不能被 LRU 淘汰，否则重复投递可能二次执行。
    for (const [key, entry] of this.dedup) {
      if (entry.state !== 'done') continue
      this.dedup.delete(key)
      if (this.dedup.size <= DEDUP_CAP) return
    }
  }

  private rememberCancel(key: string): void {
    this.cancelDedup.delete(key)
    this.cancelDedup.set(key, true)
    while (this.cancelDedup.size > DEDUP_CAP) {
      const oldest = this.cancelDedup.keys().next().value
      if (oldest === undefined) break
      this.cancelDedup.delete(oldest)
    }
  }

  private touchCancelDedup(key: string): void {
    this.cancelDedup.delete(key)
    this.cancelDedup.set(key, true)
  }
}

function formatIssues(issues: readonly ValidationIssue[]): string {
  return issues.map((issue) => `${issue.path}: ${issue.message} (${issue.rule})`).join('; ')
}

function humanMessage(value: unknown): string {
  const raw = value instanceof Error ? value.message : String(value)
  return Array.from(raw).slice(0, 500).join('')
}
