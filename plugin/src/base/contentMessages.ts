// content script 与 SW 的扩展内部消息。这里故意不含 accountRef：页面没有权力
// 猜测脑侧账号，SW 只能从已 accepted 命令的内存 CmdContext 补齐协议 EventContext。
import type {
  LoginState,
  SensorParams,
} from './protocol'

export const CONTENT_MESSAGE = {
  Ready: 'recruithelper.content.ready',
  LoginStable: 'recruithelper.content.loginStable',
  PageNavigated: 'recruithelper.content.pageNavigated',
  Configure: 'recruithelper.content.configure',
  NavigationObserved: 'recruithelper.content.navigationObserved',
  Probe: 'recruithelper.content.probe',
} as const

export const ZHILIAN_CONTENT_ORIGIN = 'https://rd6.zhaopin.com'
export const ZHILIAN_CONTENT_MATCH = `${ZHILIAN_CONTENT_ORIGIN}/*`

export interface ContentReadyMessage {
  type: typeof CONTENT_MESSAGE.Ready
  at: number
  url: string
}

export interface ContentLoginStableMessage {
  type: typeof CONTENT_MESSAGE.LoginStable
  observedAt: number
  state: LoginState
}

export interface ContentPageNavigatedMessage {
  type: typeof CONTENT_MESSAGE.PageNavigated
  at: number
  url: string
}

export type ContentUpMessage =
  | ContentReadyMessage
  | ContentLoginStableMessage
  | ContentPageNavigatedMessage

export interface ContentConfigureMessage {
  type: typeof CONTENT_MESSAGE.Configure
  sensors: SensorParams | null
  requestSnapshot?: boolean
}

export interface ContentNavigationObservedMessage {
  type: typeof CONTENT_MESSAGE.NavigationObserved
}

export interface ContentProbeMessage {
  type: typeof CONTENT_MESSAGE.Probe
}

export type ContentDownMessage = ContentConfigureMessage | ContentNavigationObservedMessage | ContentProbeMessage

export interface ContentReadyResponse {
  ok: true
  sensors: SensorParams | null
}

export function isZhilianURL(value: string | undefined): boolean {
  if (!value) return false
  try {
    const url = new URL(value)
    return url.protocol === 'https:' && url.hostname === 'rd6.zhaopin.com'
  } catch {
    return false
  }
}
