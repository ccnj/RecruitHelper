// content world 的纯传感状态机。DOM/Chrome 适配在 content.ts；本类只做双读、
// 去抖、节流和稳定导航收敛，便于用确定性假时钟测试生产同一代码。
import {
  LoginState,
  ManualInteractionKind,
  PageKind,
  SensorParams,
} from './protocol'
import {
  CONTENT_MESSAGE,
  ContentUpMessage,
  MANUAL_EMIT_MIN_MS,
} from './contentMessages'

export interface ContentSensorEnvironment {
  clearTimer(handle: unknown): void
  currentURL(): string
  emit(message: ContentUpMessage): void
  now(): number
  pageKind(): PageKind
  readLoginState(): LoginState
  readUnreadTotal(): number | null
  setTimer(callback: () => void, delayMs: number): unknown
}

export class ContentSensor {
  private config: Readonly<SensorParams> | null = null
  private started = false
  private badgeTimer: unknown = null
  private loginTimer: unknown = null
  private navigationTimer: unknown = null
  private lastStableUnread: number | null = null
  private lastEmittedUnread: number | null = null
  private lastUnreadEmitAt = Number.NEGATIVE_INFINITY
  private lastStableLogin: LoginState | null = null
  private lastReportedURL: string | null = null
  private lastManualEmitAt = Number.NEGATIVE_INFINITY

  constructor(private readonly env: ContentSensorEnvironment) {}

  start(): void {
    if (this.started) return
    this.started = true
    this.env.emit({
      type: CONTENT_MESSAGE.Ready,
      at: this.env.now(),
      pageKind: this.env.pageKind(),
      url: this.env.currentURL(),
    })
    this.armAll()
  }

  configure(config: SensorParams | null): void {
    this.config = config ? Object.freeze({ ...config }) : null
    this.clearSamplingTimers()
    this.armAll()
  }

  dispose(): void {
    this.started = false
    this.clearSamplingTimers()
  }

  onDOMMutation(): void {
    if (!this.started || !this.config) return
    this.armBadge()
    this.armLogin()
    if (this.env.currentURL() !== this.lastReportedURL) this.armNavigation()
  }

  onNavigationSignal(): void {
    if (!this.started || !this.config) return
    this.armNavigation()
  }

  onTrustedPointer(isTrusted: boolean): void {
    if (!isTrusted) return
    this.emitManual(ManualInteractionKind.Pointer)
  }

  onTrustedKeyboard(isTrusted: boolean): void {
    if (!isTrusted) return
    this.emitManual(ManualInteractionKind.Keyboard)
  }

  onTrustedNavigationIntent(isTrusted: boolean): void {
    if (!isTrusted || !this.started) return
    this.env.emit({ type: CONTENT_MESSAGE.TrustedNavigationIntent, at: this.env.now() })
  }

  private armAll(): void {
    if (!this.started || !this.config) return
    this.armBadge()
    this.armLogin()
    this.armNavigation()
  }

  private armBadge(): void {
    const config = this.config
    if (!config) return
    if (this.badgeTimer !== null) this.env.clearTimer(this.badgeTimer)
    const first = this.env.readUnreadTotal()
    if (first === null) {
      this.badgeTimer = null
      return
    }
    this.badgeTimer = this.env.setTimer(() => {
      this.badgeTimer = null
      const second = this.env.readUnreadTotal()
      if (second === null || second !== first || second === this.lastStableUnread) return
      const observedAt = this.env.now()
      const prev = this.lastEmittedUnread
      const emitEvent = second !== prev && observedAt - this.lastUnreadEmitAt >= config.badgeMinEmitIntervalMs
      this.lastStableUnread = second
      if (emitEvent) {
        this.lastEmittedUnread = second
        this.lastUnreadEmitAt = observedAt
      }
      this.env.emit({
        type: CONTENT_MESSAGE.UnreadStable,
        emitEvent,
        observedAt,
        prev,
        value: second,
      })
    }, config.badgeDebounceMs)
  }

  private armLogin(): void {
    const config = this.config
    if (!config) return
    if (this.loginTimer !== null) this.env.clearTimer(this.loginTimer)
    const first = this.env.readLoginState()
    this.loginTimer = this.env.setTimer(() => {
      this.loginTimer = null
      const second = this.env.readLoginState()
      if (second !== first || second === this.lastStableLogin) return
      this.lastStableLogin = second
      this.env.emit({
        type: CONTENT_MESSAGE.LoginStable,
        observedAt: this.env.now(),
        state: second,
      })
    }, config.badgeDebounceMs)
  }

  private armNavigation(): void {
    const config = this.config
    if (!config) return
    if (this.navigationTimer !== null) this.env.clearTimer(this.navigationTimer)
    const firstURL = this.env.currentURL()
    this.navigationTimer = this.env.setTimer(() => {
      this.navigationTimer = null
      const secondURL = this.env.currentURL()
      if (secondURL !== firstURL) {
        this.armNavigation()
        return
      }
      if (secondURL === this.lastReportedURL) return
      this.lastReportedURL = secondURL
      const at = this.env.now()
      this.env.emit({
        type: CONTENT_MESSAGE.PageNavigated,
        at,
        pageKind: this.env.pageKind(),
        url: secondURL,
      })
    }, config.navSettleMs)
  }

  private emitManual(kind: Exclude<ManualInteractionKind, 'navigation'>): void {
    if (!this.started) return
    const at = this.env.now()
    if (at - this.lastManualEmitAt < MANUAL_EMIT_MIN_MS) return
    this.lastManualEmitAt = at
    this.env.emit({
      type: CONTENT_MESSAGE.ManualInteraction,
      at,
      kind,
      pageKind: this.env.pageKind(),
    })
  }

  private clearSamplingTimers(): void {
    for (const handle of [this.badgeTimer, this.loginTimer, this.navigationTimer]) {
      if (handle !== null) this.env.clearTimer(handle)
    }
    this.badgeTimer = null
    this.loginTimer = null
    this.navigationTimer = null
  }
}
