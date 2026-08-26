// content world 的纯传感状态机。DOM/Chrome 适配在 content.ts；本类只做双读、
// 去抖、节流和稳定导航收敛，便于用确定性假时钟测试生产同一代码。
import {
  LoginState,
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
  readLoginState(): LoginState
  setTimer(callback: () => void, delayMs: number): unknown
}

export class ContentSensor {
  private config: Readonly<SensorParams> | null = null
  private started = false
  private loginTimer: unknown = null
  private navigationTimer: unknown = null
  // 重新同步票据。传感是增量上报（值没变就不报），而 SW 侧那份缓存可能单边
  // 丢失；没有这张票，接收方一旦丢了状态就只能等值本身变化——而登录态几乎从
  // 不变化，等于永久失联。票由 configure(requestSnapshot) 发放，成功上报后消费。
  // 导航刻意不发票：PageNavigated 在脑侧会拉前巡检，强制重发等于伪造用户行为。
  //
  // 2026-08-26:未读那张票随被动未读传感一并删除(角标已改由 chat.readUnreadTotal
  // 现场读)。登录态这张必须留——它是掉登录即时停机通道的必要前置。
  private forceLoginSnapshot = false
  private lastStableLogin: LoginState | null = null
  private lastReportedURL: string | null = null

  constructor(private readonly env: ContentSensorEnvironment) {}

  start(): void {
    if (this.started) return
    this.started = true
    this.env.emit({
      type: CONTENT_MESSAGE.Ready,
      at: this.env.now(),
      url: this.env.currentURL(),
    })
    this.armAll()
  }

  configure(config: SensorParams | null, requestSnapshot = false): void {
    this.config = config ? Object.freeze({ ...config }) : null
    this.forceLoginSnapshot = this.config !== null && requestSnapshot
    this.clearSamplingTimers()
    this.armAll()
  }

  dispose(): void {
    this.started = false
    this.clearSamplingTimers()
  }

  onDOMMutation(): void {
    if (!this.started || !this.config) return
    this.armLogin()
    if (this.env.currentURL() !== this.lastReportedURL) this.armNavigation()
  }

  onNavigationSignal(): void {
    if (!this.started || !this.config) return
    this.armNavigation()
  }

  private armAll(): void {
    if (!this.started || !this.config) return
    this.armLogin()
    this.armNavigation()
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
        url: secondURL,
      })
    }, config.navSettleMs)
  }


  private clearSamplingTimers(): void {
    for (const handle of [this.loginTimer, this.navigationTimer]) {
      if (handle !== null) this.env.clearTimer(handle)
    }
    this.loginTimer = null
    this.navigationTimer = null
  }
}
