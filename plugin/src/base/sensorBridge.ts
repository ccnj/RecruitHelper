// SW 侧页面桥：只把 content 的稳定现货变成 QoS0 提示/心跳缓存。
// 这里没有巡检、业务判断或持久化；canonical 外传感静音，推荐页 reload 换代例外。
//
// **按平台分区**：每个标签页属于某个已登记站点（program/platform/sites.ts），
// canonical 标签页、登录态基线、上下文健康都是**每平台各一份**。本文件不知道
// 任何具体平台的名字。
//
// 为什么必须分区：传感事件带的 platform 决定脑把它记到谁头上。一份共用的
// canonical 会让 B 平台的掉登录以 A 平台的名义发出去 —— 脑随即停掉一个根本
// 没掉线的账号，而真掉线的那个继续跑。这是错靶，不是显示问题。
import {
  EventDataByName,
  EventName,
  LoginState,
  ManualInteractionKind,
  NotReadyReason,
  PageKind,
  PingContext,
  SensorParams,
} from './protocol'
import type { CmdContext } from './protocol'
import {
  CONTENT_MESSAGE,
  ContentReadyResponse,
  ContentUpMessage,
} from './contentMessages'
import { allSites, siteById, siteForURL } from '../program/platform/sites'
import type { PlatformSite } from '../program/platform/sites'
import type { SendOutcome } from './dispatcher'

export interface SensorConnectionPort {
  currentCommandContext(platform: string): Readonly<CmdContext> | undefined
  emitPlatformSensorEvent<N extends keyof EventDataByName>(
    name: N,
    platform: string,
    data: EventDataByName[N],
    observedAt?: number,
  ): SendOutcome
  onCommandContext(listener: (context: Readonly<CmdContext>) => void): () => void
  onHeartbeat(listener: () => void): () => void
  onSensorConfig(listener: (config: Readonly<Partial<SensorParams>>) => void): () => void
  sensorConfig(): Readonly<Partial<SensorParams>>
  setContextHealth(contexts: readonly PingContext[]): void
}

export interface ContentSource {
  active: boolean
  tabId: number
  url: string
  windowId?: number
}

interface ContentTabState {
  activeRank: number
  lastSeenAt: number
  loginState: LoginState
  pageKind: PageKind
  platformId: string
  tabId: number
  url: string
  windowId?: number
}

export class SensorBridge {
  private readonly tabStates = new Map<number, ContentTabState>()
  /** tabId → platformId。用于区分「这个平台有页但脚本死了」与「这个平台没页」。 */
  private readonly platformTabs = new Map<number, string>()
  private activeSequence = 0
  private started = false
  private readonly lastCanonicalLogin = new Map<string, LoginState>()

  constructor(
    private readonly connection: SensorConnectionPort,
    private readonly now: () => number = Date.now,
  ) {}

  start(): void {
    if (this.started) return
    this.started = true

    this.connection.onSensorConfig(() => { void this.broadcastConfiguration(true) })
    this.connection.onHeartbeat(() => { this.resyncStaleSensors() })
    this.connection.onCommandContext((context) => {
      if (siteById(context.platform)) this.refreshCachedState()
    })

    chrome.runtime.onMessage.addListener((raw, sender, sendResponse) => {
      const source = sourceFromSender(sender)
      if (!source) return undefined
      const response = this.acceptContentMessage(raw, source)
      if (response) {
        sendResponse(response)
        return true
      }
      return undefined
    })

    chrome.tabs.onActivated.addListener((activeInfo) => this.noteTabActivated(activeInfo.tabId))
    chrome.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
      const site = siteForURL(changeInfo.url ?? tab.url)
      if (!site) {
        if (changeInfo.url) this.removeTab(tabId)
        return
      }
      this.platformTabs.set(tabId, site.id)
      if (changeInfo.status === 'loading') {
        this.tabStates.delete(tabId)
        this.refreshCachedState()
      }
    })
    chrome.tabs.onRemoved.addListener((tabId) => this.removeTab(tabId))
    chrome.windows.onFocusChanged.addListener((windowId) => {
      if (windowId === chrome.windows.WINDOW_ID_NONE) return
      void chrome.tabs.query({ active: true, windowId }).then((tabs) => {
        const tabId = tabs[0]?.id
        if (tabId !== undefined) this.noteTabActivated(tabId)
      }).catch(() => undefined)
    })

    const onNavigation = (details: chrome.webNavigation.WebNavigationFramedCallbackDetails): void => {
      if (details.frameId !== 0) return
      const site = siteForURL(details.url)
      if (!site) return
      this.platformTabs.set(details.tabId, site.id)
      void chrome.tabs.sendMessage(details.tabId, { type: CONTENT_MESSAGE.NavigationObserved }).catch(() => undefined)
    }
    chrome.webNavigation.onCommitted.addListener((details) => {
      onNavigation(details)
      if (details.frameId !== 0 || details.transitionType !== 'reload') return
      const site = siteForURL(details.url)
      if (!site || site.pageKind(details.url) !== PageKind.Recommend) return
      this.emitRecommendationFeedReload(site.id, Math.trunc(details.timeStamp))
    })
    chrome.webNavigation.onHistoryStateUpdated.addListener(onNavigation)
    chrome.webNavigation.onReferenceFragmentUpdated.addListener(onNavigation)

    void this.discoverPlatformTabs()
  }

  acceptContentMessage(raw: unknown, source: ContentSource): ContentReadyResponse | null {
    const site = siteForURL(source.url)
    if (!site) return null
    const message = parseContentMessage(raw)
    if (!message) return null
    this.platformTabs.set(source.tabId, site.id)
    const state = this.upsertState(source, site)
    state.lastSeenAt = this.now()

    switch (message.type) {
      case CONTENT_MESSAGE.Ready:
        state.url = source.url
        state.pageKind = site.pageKind(source.url)
        this.refreshCachedState()
        void this.sendConfiguration(source.tabId, true)
        return { ok: true, sensors: completeSensorConfig(this.connection.sensorConfig()) }

      case CONTENT_MESSAGE.LoginStable: {
        state.loginState = message.state
        this.refreshCachedState()
        if (this.canonicalTab(site.id)?.tabId === source.tabId && message.state !== LoginState.Unknown) {
          const previous = this.lastCanonicalLogin.get(site.id) ?? null
          this.lastCanonicalLogin.set(site.id, message.state)
          if (previous !== null && previous !== LoginState.Unknown && previous !== message.state) {
            this.emitIfContext(site.id, EventName.LoginStateChanged, {
              at: message.observedAt,
              stable: true,
              state: message.state,
            }, message.observedAt)
          }
        }
        return null
      }

      case CONTENT_MESSAGE.PageNavigated: {
        state.url = source.url
        state.pageKind = site.pageKind(source.url)
        this.refreshCachedState()
        if (this.canonicalTab(site.id)?.tabId === source.tabId) {
          this.emitIfContext(site.id, EventName.PageNavigated, { at: message.at }, message.at)
        }
        return null
      }
    }
  }

  noteTabActivated(tabId: number): void {
    const state = this.tabStates.get(tabId)
    if (!state) return
    state.activeRank = ++this.activeSequence
    this.refreshCachedState()
  }

  noteChromeNavigation(tabId: number, url: string): void {
    const site = siteForURL(url)
    if (!site) return
    this.platformTabs.set(tabId, site.id)
  }

  removeTab(tabId: number): void {
    this.platformTabs.delete(tabId)
    this.tabStates.delete(tabId)
    this.refreshCachedState()
  }

  // 每个已登记平台各报一条；脑没下发过命令上下文的平台不出现在数组里 ——
  // 没有 accountRef 就没有可归属的对象，不得用平台名占位。
  refreshCachedState(): void {
    const contexts: PingContext[] = []
    for (const site of allSites()) {
      const context = this.connection.currentCommandContext(site.id)
      if (!context) continue
      contexts.push(this.contextHealth(context, this.canonicalTab(site.id)))
    }
    this.connection.setContextHealth(contexts)
  }

  private contextHealth(context: Readonly<CmdContext>, canonical: ContentTabState | null): PingContext {
    if (!canonical) {
      return {
        platform: context.platform,
        accountRef: context.accountRef,
        // 「有页但脚本死了」只看本平台自己的标签页；别的平台开着页不能替它作证。
        ready: false,
        reason: this.hasTabsFor(context.platform) ? NotReadyReason.ContentScriptDead : NotReadyReason.PageAbsent,
      }
    }
    if (canonical.loginState === LoginState.Out) {
      return {
        platform: context.platform,
        accountRef: context.accountRef,
        ready: false,
        reason: NotReadyReason.LoginRequired,
      }
    }
    if (canonical.loginState !== LoginState.In) {
      return {
        platform: context.platform,
        accountRef: context.accountRef,
        ready: false,
        reason: NotReadyReason.IdentityUnverified,
      }
    }
    if (canonical.pageKind !== PageKind.Im) {
      return {
        platform: context.platform,
        accountRef: context.accountRef,
        ready: false,
        reason: NotReadyReason.PageAbsent,
      }
    }
    return { platform: context.platform, accountRef: context.accountRef, ready: true }
  }

  private hasTabsFor(platformId: string): boolean {
    for (const id of this.platformTabs.values()) {
      if (id === platformId) return true
    }
    return false
  }

  private emitRecommendationFeedReload(platformId: string, at: number): void {
    // 只把 Chrome 公开确认的主框架 reload 当作整页换代；平台打开/关闭
    // 简历详情同样可能令 tab 短暂 loading，不能据此终止批次。
    this.emitIfContext(platformId, EventName.ManualInteraction, {
      at,
      kind: ManualInteractionKind.Navigation,
      pageKind: PageKind.Recommend,
    }, at)
  }

  private emitIfContext<N extends keyof EventDataByName>(
    platformId: string,
    name: N,
    data: EventDataByName[N],
    observedAt: number,
  ): void {
    // 事件没有脑下发过 accountRef 时直接丢弃；严禁用 tabId、host 或占位串伪造账号。
    if (!this.connection.currentCommandContext(platformId)) return
    this.connection.emitPlatformSensorEvent(name, platformId, data, observedAt)
  }

  private upsertState(source: ContentSource, site: PlatformSite): ContentTabState {
    const existing = this.tabStates.get(source.tabId)
    if (existing) {
      existing.url = source.url
      existing.pageKind = site.pageKind(source.url)
      existing.platformId = site.id
      existing.windowId = source.windowId
      if (source.active) existing.activeRank = ++this.activeSequence
      return existing
    }
    const state: ContentTabState = {
      activeRank: source.active ? ++this.activeSequence : 0,
      lastSeenAt: this.now(),
      loginState: LoginState.Unknown,
      pageKind: site.pageKind(source.url),
      platformId: site.id,
      tabId: source.tabId,
      url: source.url,
      windowId: source.windowId,
    }
    this.tabStates.set(source.tabId, state)
    return state
  }

  // 增量上报的接收方必须自带一条重新同步路径。页面侧"值没变就不报"是对的，
  // 但本地这份缓存会单边丢失（真机 2026-08-03：SW 未重启、只有一个平台标签页，
  // 缓存仍然空着），此时干等值本身变化就是死锁——登录态几乎从不变化，等于永久
  // 失联。只在确实缺失时索要，齐全时零开销；借既有心跳节奏，不新增定时器。
  //
  // 2026-08-26 删被动未读传感时，**这里只删掉未读那一半判据**。登录态这一半是
  // lastCanonicalLogin 拿到第一个非 unknown 采样的路径，而登录态变化事件要求
  // "有过前一个非 unknown 采样"才触发（见 LoginStable 分支）。一并删掉的后果是
  // 掉登录的即时停机通道永久失效，**而且没有任何症状**。
  private resyncStaleSensors(): void {
    if (!this.started) return
    for (const site of allSites()) {
      const canonical = this.canonicalTab(site.id)
      if (!canonical) continue
      if (canonical.loginState !== LoginState.Unknown) continue
      void this.sendConfiguration(canonical.tabId, true)
    }
  }

  private canonicalTab(platformId: string): ContentTabState | null {
    const candidates = [...this.tabStates.values()].filter((state) => state.platformId === platformId)
    candidates.sort((a, b) => {
      const aIM = a.pageKind === PageKind.Im ? 1 : 0
      const bIM = b.pageKind === PageKind.Im ? 1 : 0
      if (aIM !== bIM) return bIM - aIM
      if (a.activeRank !== b.activeRank) return b.activeRank - a.activeRank
      return a.tabId - b.tabId
    })
    return candidates[0] ?? null
  }

  private async discoverPlatformTabs(): Promise<void> {
    for (const site of allSites()) {
      try {
        const tabs = await chrome.tabs.query({ url: site.match })
        for (const tab of tabs) {
          if (tab.id === undefined) continue
          this.platformTabs.set(tab.id, site.id)
          await this.sendConfiguration(tab.id, true)
        }
      } catch {
        // 启动发现失败只退化为 sensors=null；下一条 content Ready 会自然补齐。
        // 逐平台 catch：一个平台查失败不得连累其余平台的发现与下面这次刷新。
      }
    }
    this.refreshCachedState()
  }

  private async broadcastConfiguration(requestSnapshot: boolean): Promise<void> {
    const tabIds = new Set([...this.platformTabs.keys(), ...this.tabStates.keys()])
    await Promise.all([...tabIds].map((tabId) => this.sendConfiguration(tabId, requestSnapshot)))
  }

  private async sendConfiguration(tabId: number, requestSnapshot: boolean): Promise<void> {
    try {
      await chrome.tabs.sendMessage(tabId, {
        type: CONTENT_MESSAGE.Configure,
        sensors: completeSensorConfig(this.connection.sensorConfig()),
        requestSnapshot,
      })
    } catch {
      // QoS0/脚本可能正在导航；不得重试。content Ready 会重新请求当前配置。
    }
  }
}

export function registerSensorBridge(connection: SensorConnectionPort): SensorBridge {
  const bridge = new SensorBridge(connection)
  bridge.start()
  return bridge
}

function sourceFromSender(sender: chrome.runtime.MessageSender): ContentSource | null {
  const tab = sender.tab
  if (tab?.id === undefined || !siteForURL(tab.url)) return null
  return {
    tabId: tab.id,
    url: tab.url as string,
    active: tab.active,
    windowId: tab.windowId,
  }
}

function parseContentMessage(value: unknown): ContentUpMessage | null {
  const message = asRecord(value)
  if (!message || typeof message.type !== 'string') return null
  switch (message.type) {
    case CONTENT_MESSAGE.Ready:
      if (!safeTime(message.at) || typeof message.url !== 'string') return null
      return {
        type: CONTENT_MESSAGE.Ready,
        at: message.at,
        url: message.url,
      }
    case CONTENT_MESSAGE.LoginStable:
      if (!safeTime(message.observedAt) || !Object.values(LoginState).includes(message.state as LoginState)) return null
      return {
        type: CONTENT_MESSAGE.LoginStable,
        observedAt: message.observedAt,
        state: message.state as LoginState,
      }
    case CONTENT_MESSAGE.PageNavigated:
      if (!safeTime(message.at) || typeof message.url !== 'string') return null
      return {
        type: CONTENT_MESSAGE.PageNavigated,
        at: message.at,
        url: message.url,
      }
    default:
      return null
  }
}

function completeSensorConfig(value: Readonly<Partial<SensorParams>>): SensorParams | null {
  const keys: Array<keyof SensorParams> = [
    'badgeDebounceMs',
    'navSettleMs',
    'manualQuietMs',
  ]
  if (!keys.every((key) => Number.isSafeInteger(value[key]))) return null
  return {
    badgeDebounceMs: value.badgeDebounceMs as number,
    navSettleMs: value.navSettleMs as number,
    manualQuietMs: value.manualQuietMs as number,
  }
}

function safeTime(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}
