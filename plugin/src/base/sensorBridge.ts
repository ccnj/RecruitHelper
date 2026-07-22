// SW 侧页面桥：只把 content 的稳定现货变成 QoS0 提示/心跳缓存。
// 这里没有巡检、业务判断或持久化；canonical 外传感静音，manual 例外。
import {
  EventDataByName,
  EventName,
  LoginState,
  ManualInteractionKind,
  NotReadyReason,
  PageKind,
  PingContext,
  PingSensors,
  SensorParams,
} from './protocol'
import type { CmdContext } from './protocol'
import {
  CONTENT_MESSAGE,
  ContentReadyResponse,
  ContentUpMessage,
  MANUAL_EMIT_MIN_MS,
  ZHILIAN_CONTENT_MATCH,
  isZhilianURL,
} from './contentMessages'
import { NavigationTracker, navigationTracker } from './navigation'
import type { SendOutcome } from './dispatcher'

const PLATFORM = 'zhilian'

export interface SensorConnectionPort {
  currentCommandContext(platform: string): Readonly<CmdContext> | undefined
  emitPlatformSensorEvent<N extends keyof EventDataByName>(
    name: N,
    platform: string,
    data: EventDataByName[N],
    observedAt?: number,
  ): SendOutcome
  onCommandContext(listener: (context: Readonly<CmdContext>) => void): () => void
  onSensorConfig(listener: (config: Readonly<Partial<SensorParams>>) => void): () => void
  sensorConfig(): Readonly<Partial<SensorParams>>
  setContextHealth(contexts: readonly PingContext[]): void
  setSensorSnapshot(snapshot: PingSensors | null): void
}

export interface ContentSource {
  active: boolean
  tabId: number
  url: string
  windowId?: number
}

interface CachedUnread {
  observedAt: number
  value: number
}

interface ContentTabState {
  activeRank: number
  lastSeenAt: number
  loginState: LoginState
  pageKind: PageKind
  tabId: number
  unread: CachedUnread | null
  url: string
  windowId?: number
}

export class SensorBridge {
  private readonly tabStates = new Map<number, ContentTabState>()
  private readonly platformTabs = new Set<number>()
  private activeSequence = 0
  private started = false
  private lastCanonicalLogin: LoginState | null = null
  private lastManualEmitAt = Number.NEGATIVE_INFINITY

  constructor(
    private readonly connection: SensorConnectionPort,
    private readonly navigation: NavigationTracker = navigationTracker,
    private readonly now: () => number = Date.now,
  ) {}

  start(): void {
    if (this.started) return
    this.started = true

    this.connection.onSensorConfig(() => { void this.broadcastConfiguration(true) })
    this.connection.onCommandContext((context) => {
      if (context.platform === PLATFORM) this.refreshCachedState()
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
      const url = changeInfo.url ?? tab.url
      if (!isZhilianURL(url)) {
        if (changeInfo.url) this.removeTab(tabId)
        return
      }
      this.platformTabs.add(tabId)
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
      if (details.frameId !== 0 || !isZhilianURL(details.url)) return
      this.platformTabs.add(details.tabId)
      this.navigation.noteChromeNavigation(details.tabId, details.url, Math.trunc(details.timeStamp))
      void chrome.tabs.sendMessage(details.tabId, { type: CONTENT_MESSAGE.NavigationObserved }).catch(() => undefined)
    }
    chrome.webNavigation.onCommitted.addListener((details) => {
      onNavigation(details)
      if (details.frameId === 0 && details.transitionType === 'reload' &&
          pageKindFromURL(details.url) === PageKind.Recommend) {
        this.emitRecommendationFeedReload(Math.trunc(details.timeStamp))
      }
    })
    chrome.webNavigation.onHistoryStateUpdated.addListener(onNavigation)
    chrome.webNavigation.onReferenceFragmentUpdated.addListener(onNavigation)

    void this.discoverPlatformTabs()
  }

  acceptContentMessage(raw: unknown, source: ContentSource): ContentReadyResponse | null {
    if (!isZhilianURL(source.url)) return null
    const message = parseContentMessage(raw)
    if (!message) return null
    this.platformTabs.add(source.tabId)
    const state = this.upsertState(source)
    state.lastSeenAt = this.now()

    switch (message.type) {
      case CONTENT_MESSAGE.Ready:
        state.url = source.url
        state.pageKind = pageKindFromURL(source.url)
        this.refreshCachedState()
        void this.sendConfiguration(source.tabId, true)
        return { ok: true, sensors: completeSensorConfig(this.connection.sensorConfig()) }

      case CONTENT_MESSAGE.UnreadStable:
        state.unread = { observedAt: message.observedAt, value: message.value }
        this.refreshCachedState()
        if (message.emitEvent && this.canonicalTab()?.tabId === source.tabId) {
          this.emitIfContext(EventName.UnreadBadge, {
            scope: 'total',
            value: message.value,
            prev: message.prev,
            stable: true,
          }, message.observedAt)
        }
        return null

      case CONTENT_MESSAGE.LoginStable: {
        state.loginState = message.state
        this.refreshCachedState()
        if (this.canonicalTab()?.tabId === source.tabId && message.state !== LoginState.Unknown) {
          const previous = this.lastCanonicalLogin
          this.lastCanonicalLogin = message.state
          if (previous !== null && previous !== LoginState.Unknown && previous !== message.state) {
            this.emitIfContext(EventName.LoginStateChanged, {
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
        state.pageKind = pageKindFromURL(source.url)
        this.refreshCachedState()
        if (this.canonicalTab()?.tabId === source.tabId) {
          this.emitIfContext(EventName.PageNavigated, {
            at: message.at,
            pageKind: state.pageKind,
          }, message.at)
        }
        const origin = this.navigation.resolveContentNavigation(source.tabId, source.url, message.at)
        if (origin === 'manual') {
          this.emitManual(ManualInteractionKind.Navigation, state.pageKind, message.at)
        }
        return null
      }

      case CONTENT_MESSAGE.ManualInteraction:
        // manual 是 canonical 静音规则的唯一例外；用户在哪个智联页操作都应开静默窗。
        this.emitManual(message.kind, pageKindFromURL(source.url), message.at)
        return null

      case CONTENT_MESSAGE.TrustedNavigationIntent:
        this.navigation.noteTrustedNavigationIntent(source.tabId, message.at)
        return null
    }
  }

  noteTabActivated(tabId: number): void {
    const state = this.tabStates.get(tabId)
    if (!state) return
    state.activeRank = ++this.activeSequence
    this.refreshCachedState()
  }

  noteChromeNavigation(tabId: number, url: string, at: number): void {
    if (!isZhilianURL(url)) return
    this.platformTabs.add(tabId)
    this.navigation.noteChromeNavigation(tabId, url, at)
  }

  removeTab(tabId: number): void {
    this.platformTabs.delete(tabId)
    this.tabStates.delete(tabId)
    this.navigation.removeTab(tabId)
    this.refreshCachedState()
  }

  refreshCachedState(): void {
    const canonical = this.canonicalTab()
    if (!canonical) {
      this.connection.setSensorSnapshot(null)
    } else if (canonical.unread) {
      this.connection.setSensorSnapshot({
        unreadTotal: {
          value: canonical.unread.value,
          observedAgoMs: Math.min(86_400_000, Math.max(0, this.now() - canonical.unread.observedAt)),
        },
      })
    } else {
      this.connection.setSensorSnapshot({ unreadTotal: null })
    }

    const context = this.connection.currentCommandContext(PLATFORM)
    if (!context) {
      this.connection.setContextHealth([])
      return
    }
    this.connection.setContextHealth([this.contextHealth(context, canonical)])
  }

  private contextHealth(context: Readonly<CmdContext>, canonical: ContentTabState | null): PingContext {
    if (!canonical) {
      return {
        platform: context.platform,
        accountRef: context.accountRef,
        ready: false,
        reason: this.platformTabs.size > 0 ? NotReadyReason.ContentScriptDead : NotReadyReason.PageAbsent,
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

  private emitManual(kind: ManualInteractionKind, pageKind: PageKind, at: number): void {
    if (!this.connection.currentCommandContext(PLATFORM)) return
    if (at - this.lastManualEmitAt < MANUAL_EMIT_MIN_MS) return
    this.lastManualEmitAt = at
    this.emitIfContext(EventName.ManualInteraction, { at, kind, pageKind }, at)
  }

  private emitRecommendationFeedReload(at: number): void {
    if (!this.connection.currentCommandContext(PLATFORM)) return
    // 只把 Chrome 公开确认的主框架 reload 当作整页换代；智联打开/关闭
    // 简历详情同样可能令 tab 短暂 loading，不能据此终止批次。
    this.lastManualEmitAt = at
    this.emitIfContext(EventName.ManualInteraction, {
      at,
      kind: ManualInteractionKind.Navigation,
      pageKind: PageKind.Recommend,
    }, at)
  }

  private emitIfContext<N extends keyof EventDataByName>(
    name: N,
    data: EventDataByName[N],
    observedAt: number,
  ): void {
    // 事件没有脑下发过 accountRef 时直接丢弃；严禁用 tabId、host 或占位串伪造账号。
    if (!this.connection.currentCommandContext(PLATFORM)) return
    this.connection.emitPlatformSensorEvent(name, PLATFORM, data, observedAt)
  }

  private upsertState(source: ContentSource): ContentTabState {
    const existing = this.tabStates.get(source.tabId)
    if (existing) {
      existing.url = source.url
      existing.pageKind = pageKindFromURL(source.url)
      existing.windowId = source.windowId
      if (source.active) existing.activeRank = ++this.activeSequence
      return existing
    }
    const state: ContentTabState = {
      activeRank: source.active ? ++this.activeSequence : 0,
      lastSeenAt: this.now(),
      loginState: LoginState.Unknown,
      pageKind: pageKindFromURL(source.url),
      tabId: source.tabId,
      unread: null,
      url: source.url,
      windowId: source.windowId,
    }
    this.tabStates.set(source.tabId, state)
    return state
  }

  private canonicalTab(): ContentTabState | null {
    const candidates = [...this.tabStates.values()]
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
    try {
      const tabs = await chrome.tabs.query({ url: ZHILIAN_CONTENT_MATCH })
      for (const tab of tabs) {
        if (tab.id === undefined) continue
        this.platformTabs.add(tab.id)
        await this.sendConfiguration(tab.id, true)
      }
      this.refreshCachedState()
    } catch {
      // 启动发现失败只退化为 sensors=null；下一条 content Ready 会自然补齐。
    }
  }

  private async broadcastConfiguration(requestSnapshot: boolean): Promise<void> {
    const tabIds = new Set([...this.platformTabs, ...this.tabStates.keys()])
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
  if (tab?.id === undefined || !isZhilianURL(tab.url)) return null
  return {
    tabId: tab.id,
    url: tab.url as string,
    active: tab.active,
    windowId: tab.windowId,
  }
}

function pageKindFromURL(value: string): PageKind {
  try {
    const path = new URL(value).pathname
    if (path === '/app/im' || path.startsWith('/app/im/')) return PageKind.Im
    if (path.startsWith('/app/recommend')) return PageKind.Recommend
  } catch {
    return PageKind.Other
  }
  return PageKind.Other
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
        pageKind: pageKindFromURL(message.url),
        url: message.url,
      }
    case CONTENT_MESSAGE.UnreadStable:
      if (!safeTime(message.observedAt) || !sensorValue(message.value) ||
          (message.prev !== null && !sensorValue(message.prev)) || typeof message.emitEvent !== 'boolean') return null
      return {
        type: CONTENT_MESSAGE.UnreadStable,
        observedAt: message.observedAt,
        value: message.value,
        prev: message.prev,
        emitEvent: message.emitEvent,
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
        pageKind: pageKindFromURL(message.url),
        url: message.url,
      }
    case CONTENT_MESSAGE.ManualInteraction:
      if (!safeTime(message.at) ||
          (message.kind !== ManualInteractionKind.Pointer && message.kind !== ManualInteractionKind.Keyboard)) return null
      return {
        type: CONTENT_MESSAGE.ManualInteraction,
        at: message.at,
        kind: message.kind,
        pageKind: PageKind.Other,
      }
    case CONTENT_MESSAGE.TrustedNavigationIntent:
      if (!safeTime(message.at)) return null
      return { type: CONTENT_MESSAGE.TrustedNavigationIntent, at: message.at }
    default:
      return null
  }
}

function completeSensorConfig(value: Readonly<Partial<SensorParams>>): SensorParams | null {
  const keys: Array<keyof SensorParams> = [
    'badgeDebounceMs',
    'badgeMinEmitIntervalMs',
    'navSettleMs',
    'manualQuietMs',
  ]
  if (!keys.every((key) => Number.isSafeInteger(value[key]))) return null
  return {
    badgeDebounceMs: value.badgeDebounceMs as number,
    badgeMinEmitIntervalMs: value.badgeMinEmitIntervalMs as number,
    navSettleMs: value.navSettleMs as number,
    manualQuietMs: value.manualQuietMs as number,
  }
}

function safeTime(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0
}

function nonnegativeInt(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0
}

function sensorValue(value: unknown): value is number {
  return nonnegativeInt(value) && value <= 1_000_000
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}
