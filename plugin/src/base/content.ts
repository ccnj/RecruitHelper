// 平台页面上的唯一 content script 入口。全部 DOM 监听只在 base 注册；
// program 不在页面里安装监听，也不会从这里收到任何业务状态或调度权。
//
// **本文件不含任何平台知识**：这是哪个平台的页、登录态怎么读，都从站点登记表
// (program/platform/sites.ts) 现查。加一个平台不必改本文件。
import { ContentSensor, ContentSensorEnvironment } from './contentSensor'
import {
  CONTENT_MESSAGE,
  ContentConfigureMessage,
  ContentDownMessage,
  ContentUpMessage,
} from './contentMessages'
import { siteForURL } from '../program/platform/sites'

function emit(message: ContentUpMessage): void {
  // QoS0：只尝试一次，不持久化、不重发；SW/脑不在时丢失是协议设计内行为。
  void chrome.runtime.sendMessage(message).catch(() => undefined)
}

function applyConfig(sensor: ContentSensor, message: ContentConfigureMessage): void {
  sensor.configure(message.sensors, message.requestSnapshot === true)
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

// manifest 的 matches 已经把注入面限死了，这里再查一次是因为两者可能漂移
// （matches 加了新平台但站点表没加）。认不出的页面上什么都不做，绝不用
// 兜底解析冒充某个平台的登录态。
const site = siteForURL(location.href)

if (site) {
  const environment: ContentSensorEnvironment = {
    clearTimer(handle) { clearTimeout(handle as ReturnType<typeof setTimeout>) },
    currentURL: () => location.href,
    emit,
    now: Date.now,
    readLoginState: () => site.readLoginState(),
    setTimer(callback, delayMs) { return setTimeout(callback, delayMs) },
  }

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

  // 真人 DOM 输入不再上报(2026-08-11 甲方裁决):manualInteraction 收窄为
  // 推荐页 reload 换代信号,由 SW 侧 webNavigation 直接发射。此处只保留导航
  // 信号本身,它驱动 pageNavigated,与 isTrusted 无关。
  window.addEventListener('popstate', () => sensor.onNavigationSignal())
  window.addEventListener('hashchange', () => sensor.onNavigationSignal())
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
