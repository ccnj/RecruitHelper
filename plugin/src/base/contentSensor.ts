// content world 的纯传感状态机。DOM/Chrome 适配在 content.ts；本类只做双读、
// 去抖、节流和稳定导航收敛，便于用确定性假时钟测试生产同一代码。
import {
  LoginState,
  PageKind,
  SensorParams,
} from './protocol'
import {
  CONTENT_MESSAGE,
  ContentUpMessage,
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
  // 重新同步票据。传感是增量上报（值没变就不报），而 SW 侧那份缓存可能单边
  // 丢失；没有这两张票，接收方一旦丢了状态就只能等值本身变化——未读还能靠新
  // 消息解锁，登录态几乎从不变化，等于永久失联。票由 configure(requestSnapshot)
  // 发放，各自在成功上报后消费一次。
  // 导航刻意不发票：pageKind 在 Ready 握手里已带初值不会缺失，且 PageNavigated
  // 在脑侧会拉前巡检，强制重发等于伪造用户行为。
  private forceUnreadSnapshot = false
  private forceLoginSnapshot = false
  private lastStableUnread: number | null = null
  private lastEmittedUnread: number | null = null
  private lastUnreadEmitAt = Number.NEGATIVE_INFINITY
  private lastStableLogin: LoginState | null = null
  private lastReportedURL: string | null = null

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

  configure(config: SensorParams | null, requestSnapshot = false): void {
    this.config = config ? Object.freeze({ ...config }) : null
    const force = this.config !== null && requestSnapshot
    this.forceUnreadSnapshot = force
    this.forceLoginSnapshot = force
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

  private armAll(): void {
    if (!this.started || !this.config) return
    this.armBadge()
    this.armLogin()
    this.armNavigation()
  }

  private armBadge(): void {
    const config = this.config
    if (!config) return
    // DOM 高频变化只负责保证“至少有一次采样在路上”，不能不断把同一个
    // debounce 窗口推迟；否则持续渲染的聊天列表会让未读传感器永远饿死。
    if (this.badgeTimer !== null) return
    const first = this.env.readUnreadTotal()
    if (first === null) {
      this.badgeTimer = null
      return
    }
    this.badgeTimer = this.env.setTimer(() => {
      this.badgeTimer = null
      const second = this.env.readUnreadTotal()
      if (second === null) return
      if (second !== first) {
        // 双读不一致不产生事实，但重新建立一个有界采样窗，避免只能等待
        // 下一次碰巧出现的 DOM mutation 才恢复传感。
        this.armBadge()
        return
      }
      if (second === this.lastStableUnread && !this.forceUnreadSnapshot) return
      const observedAt = this.env.now()
      const prev = this.lastEmittedUnread
      const emitEvent = !this.forceUnreadSnapshot &&
        second !== prev &&
        observedAt - this.lastUnreadEmitAt >= config.badgeMinEmitIntervalMs
      this.lastStableUnread = second
      this.forceUnreadSnapshot = false
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
      // 双读不一致不产生事实；值未变化时，仅在持票（SW 侧缺登录态、刚索要过
      // 重新同步）的情况下才补报一次。
      if (second !== first) return
      if (second === this.lastStableLogin && !this.forceLoginSnapshot) return
      this.forceLoginSnapshot = false
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


  private clearSamplingTimers(): void {
    for (const handle of [this.badgeTimer, this.loginTimer, this.navigationTimer]) {
      if (handle !== null) this.env.clearTimer(handle)
    }
    this.badgeTimer = null
    this.loginTimer = null
    this.navigationTimer = null
  }
}
