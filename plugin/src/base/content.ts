// 智联 rd6 页面上的唯一 content script 入口。全部 DOM 监听只在 base 注册；
// program 不在页面里安装监听，也不会从这里收到任何业务状态或调度权。
import { ContentSensor, ContentSensorEnvironment } from './contentSensor'
import { readZhilianUnreadTotal } from './contentDom'
import {
  CONTENT_MESSAGE,
  ContentConfigureMessage,
  ContentDownMessage,
  ContentUpMessage,
  isZhilianURL,
} from './contentMessages'
import { LoginState, PageKind } from './protocol'

function pageKind(): PageKind {
  const path = location.pathname
  if (path === '/app/im' || path.startsWith('/app/im/')) return PageKind.Im
  if (path.startsWith('/app/recommend')) return PageKind.Recommend
  return PageKind.Other
}

// 登录只接受真机已验证的 isLoggedIn===true + staffId；残留 staff 不能降级判 in。
function readLoginState(): LoginState {
  const marker = '__INITIAL_STATE__='
  const source = Array.from(document.scripts)
    .map((script) => script.textContent ?? '')
    .find((text) => text.includes(marker))
  if (!source) return LoginState.Unknown
  const candidate = source.slice(source.indexOf(marker) + marker.length).trim().replace(/;$/u, '')
  try {
    const initial = JSON.parse(candidate) as Record<string, unknown>
    const sessionModule = asRecord(initial.session)
    const session = asRecord(sessionModule?.session)
    const staff = asRecord(session?.staff)
    if (session?.isLoggedIn === false) return LoginState.Out
    if (session?.isLoggedIn === true && staff?.staffId != null) return LoginState.In
  } catch {
    // 启动脚本形状变化时返回 unknown；绝不凭导航文案猜登录成功。
  }
  return LoginState.Unknown
}

function emit(message: ContentUpMessage): void {
  // QoS0：只尝试一次，不持久化、不重发；SW/脑不在时丢失是协议设计内行为。
  void chrome.runtime.sendMessage(message).catch(() => undefined)
}

const environment: ContentSensorEnvironment = {
  clearTimer(handle) { clearTimeout(handle as ReturnType<typeof setTimeout>) },
  currentURL: () => location.href,
  emit,
  now: Date.now,
  pageKind,
  readLoginState,
  readUnreadTotal: () => readZhilianUnreadTotal(document),
  setTimer(callback, delayMs) { return setTimeout(callback, delayMs) },
}

function isNavigationTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false
  return target.closest('a[href],area[href]') !== null
}

function applyConfig(sensor: ContentSensor, message: ContentConfigureMessage): void {
  sensor.configure(message.sensors)
  if (message.requestSnapshot) sensor.onDOMMutation()
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

if (isZhilianURL(location.href)) {
  const sensor = new ContentSensor(environment)
  sensor.start()

  const observer = new MutationObserver(() => sensor.onDOMMutation())
  observer.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ['class', 'style'],
    characterData: true,
    childList: true,
    subtree: true,
  })

  document.addEventListener('pointerdown', (event) => sensor.onTrustedPointer(event.isTrusted), true)
  document.addEventListener('keydown', (event) => {
    sensor.onTrustedKeyboard(event.isTrusted)
    if (event.isTrusted && event.key === 'Enter' && isNavigationTarget(event.target)) {
      sensor.onTrustedNavigationIntent(true)
    }
  }, true)
  document.addEventListener('click', (event) => {
    if (event.isTrusted && isNavigationTarget(event.target)) sensor.onTrustedNavigationIntent(true)
  }, true)
  window.addEventListener('popstate', (event) => {
    sensor.onTrustedNavigationIntent(event.isTrusted)
    sensor.onNavigationSignal()
  })
  window.addEventListener('hashchange', (event) => {
    sensor.onTrustedNavigationIntent(event.isTrusted)
    sensor.onNavigationSignal()
  })
  window.addEventListener('pagehide', () => {
    observer.disconnect()
    sensor.dispose()
  }, { once: true })

  chrome.runtime.onMessage.addListener((raw: unknown, _sender, sendResponse) => {
    const message = asRecord(raw) as unknown as ContentDownMessage | null
    if (message?.type === CONTENT_MESSAGE.Probe) {
      sendResponse({ ok: true })
      return true
    }
    if (message?.type === CONTENT_MESSAGE.Configure) {
      applyConfig(sensor, message)
      sendResponse({ ok: true })
      return true
    }
    if (message?.type === CONTENT_MESSAGE.NavigationObserved) {
      sensor.onNavigationSignal()
      sendResponse({ ok: true })
      return true
    }
    return undefined
  })
}
