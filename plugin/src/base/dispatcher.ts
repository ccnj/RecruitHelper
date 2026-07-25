// 命令分发器(base 层,手侧唯一命令入口)。测试端与正式调度器共用此路径(宪法 3)。
// 职责:校验/去重 -> FIFO/背压 -> 单执行槽 -> progress/result/cancel。
import {
  AckStatus,
  Batch,
  CmdBody,
  CmdClass,
  CmdContext,
  DEFAULTS,
  ErrorBody,
  ErrorCode,
  JournalState,
  Kind,
  PRIMITIVE_META,
  ProgressBody,
  ReportBody,
  ReportState,
  ResultBody,
  ResultStatus,
  Retryable,
  SideEffect,
  ValidationIssue,
  WitnessUnavailableReason,
  validateKindBody,
  validatePrimitiveResult,
} from './protocol'
import { lookup, Primitive, PrimitiveResult } from '../program/registry'
import { WitnessStore, WitnessStoreError } from './witness'

export type SendOutcome = 'sent' | 'queued' | 'dropped' | 'tooLarge'

// 送帧回调:由 connection 提供,负责生成信封 msgId、大小检查与写 WS。
export type SendFn = (kind: Kind, session: string | null, body: unknown) => SendOutcome | void
export type DurableResultFn = (
  session: string | null,
  body: ResultBody,
  commitIdemKey?: string,
) => Promise<SendOutcome>
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
  barrierWriting: boolean
  barrierStopKind: StopKind | null
  barrierStopResolve: ((kind: StopKind) => void) | null
  terminalSent: boolean
}

interface DedupEntry {
  state: 'queued' | 'executing' | 'done'
  cmdClass: CmdClass
  witnessed?: boolean
  resultBody?: ResultBody // 终局 result body,供重复投递重放
}

interface QuarantinedSX {
  // 只用于校验脑重投的新信封与处理 cancel；这个旧 QueueItem
  // 永远不会被放回 FIFO，从构造上避免 unknown 后“旧副本+重投副本”并存。
  item: QueueItem
  bodyJSON: string
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
  // 不可逆动作的绝对最晚时刻=min(cmd.deadline, 本次 execBudget 终点)。
  // program 必须把它传进 attempting 后的同步 MAIN task，并在 click 前复核。
  readonly irreversibleNotAfterMs: number
  readonly commandContext: Readonly<CmdContext> | undefined
  readonly guards: Readonly<Record<string, unknown>> | undefined
  signal: AbortSignal
  progress: (stage: string, pct?: number) => void
  checkpoint: () => void
  beforeSideEffect: () => Promise<void>
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
const LEASE_PULSE_MAX_MS = 10_000
const LEASE_PULSE_MIN_MS = 1_000

export class Dispatcher {
  // 去重表:内存,作用域=SW 一次生命(bootId)。键=cmd msgId。禁写 chrome.storage(宪法禁令 2/16)。
  private dedup = new Map<string, DedupEntry>()
  private cancelDedup = new Map<string, true>()
  private queue: QueueItem[] = []
  private inFlight: InFlight | null = null
  private sessionCaps: ReadonlySet<string> | null = null
  private durableResultFused = false
  private fusedRefs = new Set<string>()
  private fusedOriginalSessions = new Map<string, string | null>()
  private fusedReportSessions = new Map<string, string>()
  private quarantinedSX = new Map<string, QuarantinedSX>()
  private authorizedRecoverySX = new Set<string>()
  private leasePulses = new Map<string, ReturnType<typeof setInterval>>()

  constructor(
    private send: SendFn,
    private readonly observeCommandContext?: CommandContextObserver,
    private readonly witness?: WitnessStore,
    private readonly sendDurableResult?: DurableResultFn,
  ) {}

  snapshot(): DispatcherSnapshot {
    return { queueDepth: this.queue.length, inFlight: this.inFlight?.item.msgId ?? null }
  }

  // hello 时冻结本会话能力快照；program 若热更，必须重连后才能接收新增原语。
  setSessionCapabilities(caps: readonly string[]): void {
    this.sessionCaps = new Set(caps)
  }

  // 收到 cmd 信封。去重必须先于 session 校验；只有 accepted 的命令才进 dedup。
  async handleCmd(
    cmdMsgId: string,
    envelopeSession: string | null,
    activeSession: string | null,
    rawBody: unknown,
  ): Promise<void> {
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
        const outcome = seen.witnessed
          ? await this.publishResult(responseSession, replay, true)
          : this.send(Kind.Result, responseSession, replay)
        if (outcome === 'tooLarge') {
          const compact = this.tooLargeResult(cmdMsgId, seen.cmdClass)
          this.remember(cmdMsgId, {
            state: 'done',
            cmdClass: seen.cmdClass,
            witnessed: seen.witnessed,
            resultBody: compact,
          })
          if (seen.witnessed) {
            const compactOutcome = await this.publishResult(responseSession, this.asReplay(compact), true)
            this.fuseAfterDurableFailure(cmdMsgId, responseSession, compactOutcome)
          } else {
            this.send(Kind.Result, responseSession, this.asReplay(compact))
          }
        } else if (seen.witnessed) {
          this.fuseAfterDurableFailure(cmdMsgId, responseSession, outcome)
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

    const quarantined = this.quarantinedSX.get(cmdMsgId)
    if (quarantined && quarantined.bodyJSON !== JSON.stringify(body)) {
      this.reject(cmdMsgId, responseSession, this.protocolError(
        ErrorCode.ProtoBadArgs,
        '对账重投的同 msgId 命令体发生变化',
        Retryable.No,
      ))
      return
    }
    if (this.isWitnessed(item) && this.durableResultFused) {
      // 脑在整手 recovery barrier 未收束时不会下发新 SX。只有新
      // session 已对所有 fused ref 完成 query/report 后到达的 SX 信封，
      // 才能作为脑侧屏障已解除的显式证词。被隔离的旧 QueueItem
      // 不会复活；下方只接收当前新信封。
      if (!activeSession || !this.fusedReportsComplete(activeSession)) {
        this.reject(cmdMsgId, responseSession, this.protocolError(
          ErrorCode.QueueFull,
          '真实副作用终局持久失败，当前手仍在对账熔断中',
          Retryable.Yes,
        ))
        return
      }
      if (this.quarantinedSX.size > 0) {
        if (!quarantined) {
          this.reject(cmdMsgId, responseSession, this.protocolError(
            ErrorCode.QueueFull,
            '尚有未启动 SX 在隔离中，只接受其原 msgId 安全重投',
            Retryable.Yes,
          ))
          return
        }
        this.authorizedRecoverySX.add(cmdMsgId)
      } else {
        // quarantine 已逐条以新信封收束。此时脑仍受
        // HasEffectRecoveryForHand 约束；它能下发新 SX 本身就是剩余
        // attempting/verifying 已收束的屏障证词，才可整体解熔。
        this.durableResultFused = false
        this.fusedRefs.clear()
        this.fusedOriginalSessions.clear()
        this.fusedReportSessions.clear()
        this.authorizedRecoverySX.clear()
      }
    }
    // quarantine 只保留诊断用的旧对象；同 msgId 的安全重投
    // 通过上面的新信封重新入队，不会执行旧副本。
    if (quarantined) this.quarantinedSX.delete(cmdMsgId)

    // 真实 SX 的 idemKey 证词闸在入队前执行。attempting 永不再次执行；committed
    // 直接重放原终局。读库失败属于 accepted 后的执行失败，不能伪装成 receipt reject。
    if (this.isWitnessed(item)) {
      try {
        const existing = await this.requireWitness().findJournalByIdemKey(body.idemKey as string, cmdMsgId)
        if (existing) {
          this.send(Kind.Ack, responseSession, { ref: cmdMsgId, status: AckStatus.Duplicate })
          if (existing.state === JournalState.Committed && existing.result) {
            const replay = this.asReplay(existing.result)
            this.remember(cmdMsgId, {
              state: 'done',
              cmdClass: primitive.class,
              witnessed: true,
              resultBody: replay,
            })
            const outcome = await this.publishResult(item.session, replay, true)
            this.fuseAfterDurableFailure(item.msgId, item.session, outcome)
          } else {
            this.remember(cmdMsgId, { state: 'executing', cmdClass: primitive.class, witnessed: true })
          }
          return
        }
      } catch (error) {
        this.publishCommandContext(body)
        this.remember(cmdMsgId, { state: 'queued', cmdClass: primitive.class, witnessed: true })
        this.send(Kind.Ack, responseSession, { ref: cmdMsgId, status: AckStatus.Accepted })
        await this.finish(item, this.resultBody(cmdMsgId, this.witnessUnavailable(error), 0))
        return
      }
    }

    // 4) 已过期命令不占 FIFO 容量，也不能被 QUEUE_FULL 改写：先 accepted，
    // 随即从不可执行终局分支回 expired，handler 调用次数恒为 0。
    if (Date.now() > body.deadline) {
      this.publishCommandContext(body)
      this.remember(cmdMsgId, {
        state: 'queued',
        cmdClass: primitive.class,
        witnessed: this.isWitnessed(item),
      })
      this.send(Kind.Ack, responseSession, { ref: cmdMsgId, status: AckStatus.Accepted })
      await this.finish(item, this.resultBody(cmdMsgId, { status: ResultStatus.Expired }, 0))
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
    this.remember(cmdMsgId, {
      state: 'queued',
      cmdClass: primitive.class,
      witnessed: this.isWitnessed(item),
    })
    this.send(Kind.Ack, responseSession, { ref: cmdMsgId, status: AckStatus.Accepted })
    this.startLeasePulse(item)
    this.pump()
  }

  async handleQuery(ref: string, session: string | null): Promise<void> {
    if (!this.witness) return
    try {
      const journal = await this.witness.findJournalByRef(ref)
      const advertisement = this.witness.advertisement()
      if (!advertisement) return
      let body: ReportBody
      if (journal?.state === JournalState.Attempting) {
        body = {
          ref,
          witnessStoreId: advertisement.witnessStoreId,
          state: ReportState.Attempting,
          result: null,
          journal: this.witness.journalSnapshot(journal),
        }
      } else if (journal?.state === JournalState.Committed && journal.result) {
        body = {
          ref,
          witnessStoreId: advertisement.witnessStoreId,
          state: ReportState.Done,
          result: journal.result,
          journal: this.witness.journalSnapshot(journal),
        }
      } else {
        const memory = this.dedup.get(ref)
        const state = memory?.state === 'queued'
          ? ReportState.Queued
          : memory?.state === 'executing'
            ? ReportState.Executing
            : ReportState.Unknown
        body = {
          ref,
          witnessStoreId: advertisement.witnessStoreId,
          state,
          result: null,
          journal: null,
        }
      }
      const outcome = this.send(Kind.Report, session, body)
      const originalSession = this.fusedOriginalSessions.get(ref)
      if (outcome === 'sent' && session && this.fusedRefs.has(ref) && session !== originalSession) {
        this.fusedReportSessions.set(ref, session)
      }
    } catch (error) {
      // 证词不可读时绝不能用 unknown 假造“零副作用证明”。脑超时后走 suspect。
      console.warn('[hand] query 无法读取连续证词，拒绝作答', error)
    }
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

    const quarantined = this.quarantinedSX.get(body.ref)
    if (quarantined) {
      // quarantine 证明 handler 从未启动，cancel 可直接产生安全
      // canceled 终局。若该终局仍无法持久，内存保留 queued 而不回
      // unknown，防止脑在不知 cancel 的情况下安全重投。
      this.quarantinedSX.delete(body.ref)
      this.remember(body.ref, {
        state: 'queued', cmdClass: quarantined.item.primitive.class, witnessed: true,
      })
      void this.finish(quarantined.item, this.resultBody(body.ref, { status: ResultStatus.Canceled }, 0))
      return
    }

    const queuedIndex = this.queue.findIndex((item) => item.msgId === body.ref)
    if (queuedIndex >= 0) {
      const [item] = this.queue.splice(queuedIndex, 1)
      void this.finish(item, this.resultBody(item.msgId, { status: ResultStatus.Canceled }, 0))
      return
    }

    const running = this.inFlight
    if (!running || running.item.msgId !== body.ref || running.terminalSent) return
    running.cancelRequested = true
    // 过了不可逆动作安全点,取消只记请求、不打断；原结果获胜。
    if (running.barrierWriting) {
      running.barrierStopKind = 'canceled'
      running.barrierStopResolve?.('canceled')
    } else if (!running.pastCancellationPoint && !running.controller.signal.aborted) {
      running.controller.abort(new StopExecution('canceled'))
    }
  }

  private pump(): void {
    if (this.inFlight) return
    if (this.durableResultFused) this.quarantineQueuedWitnessed()
    const item = this.queue.shift()
    if (!item) return

    // 出队 deadline 检查。过期命令 terminal expired,handler 零调用。
    if (Date.now() > item.body.deadline) {
      void this.finish(item, this.resultBody(item.msgId, { status: ResultStatus.Expired }, 0))
        .finally(() => queueMicrotask(() => this.pump()))
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
      barrierWriting: false,
      barrierStopKind: null,
      barrierStopResolve: null,
      terminalSent: false,
    }
    this.inFlight = running
    this.remember(item.msgId, {
      state: 'executing',
      cmdClass: item.primitive.class,
      witnessed: this.isWitnessed(item),
    })
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
      if (running.barrierWriting) {
        running.barrierStopKind = timerKind
        running.barrierStopResolve?.(timerKind)
      }
      if (!running.controller.signal.aborted) {
        running.controller.abort(new StopExecution(timerKind))
      }
      stopResolve?.(timerKind)
    }, timerDelay)

    const hooks: ExecutionHooks = {
      cmdMsgId: item.msgId,
      deadlineMs: item.body.deadline,
      irreversibleNotAfterMs: Math.min(item.body.deadline, running.budgetEndsAt),
      commandContext: item.body.context ? Object.freeze({ ...item.body.context }) : undefined,
      guards: item.body.guards ? Object.freeze({ ...item.body.guards }) : undefined,
      signal: running.controller.signal,
      progress: (stage, pct) => this.reportProgress(running, stage, pct),
      checkpoint: () => this.checkpoint(running),
      beforeSideEffect: async () => {
        // deadline 双查的第二个位置必须紧贴不可逆动作。
        this.checkpoint(running)
        if (!this.isWitnessed(item)) {
          running.pastCancellationPoint = true
          return
        }
        if (running.pastCancellationPoint || running.barrierWriting) {
          throw new Error('同一命令重复调用 beforeSideEffect')
        }
        running.barrierWriting = true
        const barrierStop = new Promise<StopKind>((resolve) => { running.barrierStopResolve = resolve })
        try {
          const write = this.requireWitness().markAttempting(item.msgId, item.body.idemKey as string)
          const first = await Promise.race([
            write.then((entry) => ({ kind: 'written' as const, entry })),
            barrierStop.then((stopKind) => ({ kind: 'stopped' as const, stopKind })),
          ])
          if (first.kind === 'stopped') throw new StopExecution(first.stopKind)
          running.pastCancellationPoint = true
          if (first.entry.ref !== item.msgId || first.entry.state !== JournalState.Attempting) {
            throw new Error('idemKey 已存在其他 attempting/committed 证词，拒绝再次执行')
          }
          if (running.barrierStopKind) throw new StopExecution(running.barrierStopKind)
          this.checkpoint(running)
        } finally {
          running.barrierWriting = false
          running.barrierStopResolve = null
        }
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
        await this.finish(
          item,
          this.resultBody(item.msgId, first.outcome, Date.now() - running.startedAt),
          running.pastCancellationPoint,
        )
        running.terminalSent = true
      }
    } else if (first.kind === 'error') {
      if (timer) clearTimeout(timer)
      const result = this.resultForThrown(running, first.error)
      await this.finish(
        item,
        this.resultBody(item.msgId, result, Date.now() - running.startedAt),
        running.pastCancellationPoint,
      )
      running.terminalSent = true
    } else {
      // 不可信 handler 可能忽略 AbortSignal。先响亮终局,但保持执行槽隔离，直到它真正退出；
      // 绝不让下一条命令与僵尸 handler 并发操作页面。
      const result = this.resultForStop(running, first.stopKind)
      await this.finish(
        item,
        this.resultBody(item.msgId, result, Date.now() - running.startedAt),
        running.pastCancellationPoint,
      )
      running.terminalSent = true
      await handlerPromise // 晚到结果只作本地收敛,不能覆盖已发送终局
    }

    if (timer) clearTimeout(timer)
    this.stopLeasePulse(item.msgId)
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
    this.publishProgress(running.item, stage, pct)
  }

  private publishProgress(item: QueueItem, stage: string, pct?: number): void {
    if (typeof stage !== 'string' || stage.length === 0) return
    const body: ProgressBody = { ref: item.msgId, stage }
    if (typeof pct === 'number' && Number.isInteger(pct)) body.pct = Math.max(0, Math.min(100, pct))
    if (validateKindBody(Kind.Progress, body).length > 0) return
    // progress 是 QoS0/租约活性触碰：无 ack、无重发；stage 只供人读。
    this.send(Kind.Progress, item.session, body)
  }

  private startLeasePulse(item: QueueItem): void {
    if (!item.body.leaseMs || this.leasePulses.has(item.msgId)) return
    const intervalMs = Math.min(
      LEASE_PULSE_MAX_MS,
      Math.max(LEASE_PULSE_MIN_MS, Math.floor(item.body.leaseMs / 3)),
    )
    const timer = setInterval(() => {
      const state = this.dedup.get(item.msgId)?.state
      if (state !== 'queued' && state !== 'executing') {
        this.stopLeasePulse(item.msgId)
        return
      }
      if (state === 'executing') {
        const running = this.inFlight
        if (!running || running.item.msgId !== item.msgId || running.terminalSent ||
            running.controller.signal.aborted) {
          return
        }
      }
      this.publishProgress(item, state === 'queued' ? '命令排队中' : '命令执行中')
    }, intervalMs)
    this.leasePulses.set(item.msgId, timer)
  }

  private stopLeasePulse(ref: string): void {
    const timer = this.leasePulses.get(ref)
    if (timer !== undefined) clearInterval(timer)
    this.leasePulses.delete(ref)
  }

  private resultForThrown(running: InFlight, error: unknown): PrimitiveResult {
    if (error instanceof StopExecution) return this.resultForStop(running, error.stopKind)
    if (error instanceof WitnessStoreError && !running.pastCancellationPoint) {
      return this.witnessUnavailable(error)
    }
    if (running.controller.signal.aborted && running.controller.signal.reason instanceof StopExecution) {
      return this.resultForStop(running, running.controller.signal.reason.stopKind)
    }
    return {
      status: ResultStatus.Failed,
      error: {
        code: ErrorCode.InternalHand,
        message: humanMessage(error),
        retryable: Retryable.ManualOnly,
        // 此处捕获的是未分类异常，不能取得动作前零副作用证明，必须 possible。
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
        sideEffect: running.pastCancellationPoint ? SideEffect.Possible : SideEffect.None,
      },
    }
  }

  private async finish(
    item: QueueItem,
    resultBody: ResultBody,
    witnessBarrierPassed = false,
  ): Promise<void> {
    // 一旦本地决定发布终局，就不再允许后续 progress 越过 result。
    // result 若未送达，脑应由既有 lease/recovery 轨道收敛，而不是被旧心跳续命。
    this.stopLeasePulse(item.msgId)
    resultBody = this.ensureResultBody(item, resultBody)
    const witnessed = this.isWitnessed(item)
    const sent = await this.publishResult(
      item.session,
      resultBody,
      witnessed,
      witnessed && witnessBarrierPassed ? item.body.idemKey : undefined,
    )
    if (witnessed) {
      // X 批终局只有在 durable 回调确认“已入 outbox”（越过 attempting
      // 写点后还必须包括 atomic committed journal）后，才有资格进入内存 done。
      // committed 表示完整 ResultBody 已持久，并不等价于副作用 confirmed；因此
      // barrier 后的 failed/possible/none/timeout 与 ok 使用同一双写路径。
      // 若持久化失败，保持 executing/attempting；同 SW 的重复 cmd 只 duplicate ack，
      // 绝不能用内存终局绕过 journal 再造一个 outbox。
      if (sent === 'sent' || sent === 'queued') {
        this.remember(item.msgId, {
          state: 'done',
          cmdClass: item.primitive.class,
          witnessed: true,
          resultBody,
        })
        this.resolveFusedRef(item.msgId)
      } else {
        this.fuseAfterDurableFailure(item.msgId, item.session, sent)
      }
      return
    }

    this.remember(item.msgId, {
      state: 'done',
      cmdClass: item.primitive.class,
      witnessed: false,
      resultBody,
    })
    if (sent !== 'tooLarge') return

    // 完整 envelope 超硬上限时,用小型失败终局替代，禁止静默截断 data/evidence。
    const compact = this.tooLargeResult(item.msgId, item.primitive.class)
    this.remember(item.msgId, {
      state: 'done',
      cmdClass: item.primitive.class,
      witnessed: this.isWitnessed(item),
      resultBody: compact,
    })
    await this.publishResult(item.session, compact, this.isWitnessed(item))
  }

  private fuseAfterDurableFailure(
    ref: string,
    originalSession: string | null,
    outcome: SendOutcome | void,
  ): void {
    if (outcome !== 'dropped' && outcome !== 'tooLarge') return
    this.durableResultFused = true
    this.fusedRefs.add(ref)
    // 每次 durable 失败都会终止当前连接。旧 session 的 report
    // 不能给这次新失败背书；必须等下一条连接重新 query/report。
    this.fusedOriginalSessions.set(ref, originalSession)
    this.fusedReportSessions.delete(ref)
    this.authorizedRecoverySX.delete(ref)

    // 某条旧 committed result 的重放也可能在另一 SX 执行期间
    // 触发熔断。尚未过不可逆点的当前 SX 要合作式停下；已过点
    // 的只能让原执行收束，其 attempting 证词会迫使脑侧验证。
    const running = this.inFlight
    if (running && this.isWitnessed(running.item)) {
      this.fusedRefs.add(running.item.msgId)
      if (!this.fusedOriginalSessions.has(running.item.msgId)) {
        this.fusedOriginalSessions.set(running.item.msgId, running.item.session)
      }
      if (running.item.msgId !== ref && !running.pastCancellationPoint) {
        if (running.barrierWriting) {
          running.barrierStopKind = 'budget'
          running.barrierStopResolve?.('budget')
        } else if (!running.controller.signal.aborted) {
          running.controller.abort(new StopExecution('budget'))
        }
      }
    }
    this.quarantineQueuedWitnessed()
  }

  private quarantineQueuedWitnessed(): void {
    if (this.queue.length === 0) return
    const retained: QueueItem[] = []
    for (const queued of this.queue) {
      if (!this.isWitnessed(queued) || this.authorizedRecoverySX.has(queued.msgId)) {
        retained.push(queued)
        continue
      }
      this.quarantinedSX.set(queued.msgId, {
        item: queued,
        bodyJSON: JSON.stringify(queued.body),
      })
      this.stopLeasePulse(queued.msgId)
      this.fusedRefs.add(queued.msgId)
      if (!this.fusedOriginalSessions.has(queued.msgId)) {
        this.fusedOriginalSessions.set(queued.msgId, queued.session)
      }
      // report=unknown 的安全性依赖这个旧副本不再属于 queued/
      // executing；同 msgId 重投将经新信封重新建立 dedup 状态。
      this.dedup.delete(queued.msgId)
    }
    this.queue = retained
  }

  private fusedReportsComplete(session: string): boolean {
    return this.durableResultFused && this.fusedRefs.size > 0 &&
      [...this.fusedRefs].every((ref) => this.fusedReportSessions.get(ref) === session)
  }

  private resolveFusedRef(ref: string): void {
    this.quarantinedSX.delete(ref)
    this.authorizedRecoverySX.delete(ref)
    this.fusedRefs.delete(ref)
    this.fusedOriginalSessions.delete(ref)
    this.fusedReportSessions.delete(ref)
    if (this.fusedRefs.size === 0) {
      this.durableResultFused = false
      this.authorizedRecoverySX.clear()
    }
  }

  private ensureResultBody(item: QueueItem, body: ResultBody): ResultBody {
    const issues = validatePrimitiveResult(item.body.name, item.body.ver, body)
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
        code: cmdClass === CmdClass.Effectful ? ErrorCode.InternalHand : ErrorCode.ProtoMsgTooLarge,
        message: 'result 完整信封超过 maxMsgBytes，原 data/evidence 未发送',
        retryable: cmdClass === CmdClass.Effectful ? Retryable.ManualOnly : Retryable.No,
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

  private reject(ref: string, session: string | null, error: ErrorBody): void {
    this.send(Kind.Ack, session, { ref, status: AckStatus.Rejected, error })
  }

  private protocolError(code: ErrorCode, message: string, retryable: Retryable): ErrorBody {
    return { code, message: humanMessage(message), retryable, sideEffect: SideEffect.None }
  }

  private validationError(code: ErrorCode, issues: readonly ValidationIssue[]): ErrorBody {
    return this.protocolError(code, formatIssues(issues), Retryable.No)
  }

  private isWitnessed(item: QueueItem): boolean {
    const meta = PRIMITIVE_META[item.body.name as keyof typeof PRIMITIVE_META]
    return meta?.ver === item.body.ver && meta.batch === Batch.X && meta.class === CmdClass.Effectful
  }

  private requireWitness(): WitnessStore {
    if (!this.witness) {
      throw new WitnessStoreError(
        WitnessUnavailableReason.StoreCorrupt,
        '真实 SX 未接入 base 证词库',
      )
    }
    return this.witness
  }

  private witnessUnavailable(error: unknown): PrimitiveResult {
    const reason = error instanceof WitnessStoreError
      ? error.reason
      : WitnessUnavailableReason.WriteFailed
    return {
      status: ResultStatus.Failed,
      error: {
        code: ErrorCode.WitnessUnavailable,
        message: humanMessage(error),
        data: { reason },
        retryable: reason === WitnessUnavailableReason.WriteFailed
          ? Retryable.AfterRecovery
          : Retryable.ManualOnly,
        sideEffect: SideEffect.None,
      },
    }
  }

  private async publishResult(
    session: string | null,
    body: ResultBody,
    durable: boolean,
    commitIdemKey?: string,
  ): Promise<SendOutcome | void> {
    if (durable) {
      if (!this.sendDurableResult) return 'dropped'
      return this.sendDurableResult(session, body, commitIdemKey)
    }
    return this.send(Kind.Result, session, body)
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
