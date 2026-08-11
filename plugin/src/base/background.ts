// SW 入口(base):注册全部 chrome 监听(宪法禁令 5:监听只在 base),接线连接与原语。
import { Connection } from './connection'
import { handleInfrastructureMessage } from './optionsBridge'
import { registerSensorBridge } from './sensorBridge'
import { registerDebugPrimitives } from '../program/primitives/debug'
import { registerM2Primitives } from '../program/primitives/m2'
import { registerM3Primitives } from '../program/primitives/m3'
import { registerM4Primitives } from '../program/primitives/m4'
import { registerM5Primitives } from '../program/primitives/m5'
import { registerM6Primitives } from '../program/primitives/m6'
import { registerM7Primitives } from '../program/primitives/m7'
import { registerJobPublishPrimitives } from '../program/primitives/jobPublish'
import { refreshPagesAfterRuntimeReload } from './reload'
import { installHandLogSink } from './handLog'
import { registerNetGuard } from './netGuard'

// program 原语注册(program 不注册任何 chrome 监听,只填这张表)。
registerDebugPrimitives()
registerM2Primitives()
registerM3Primitives()
registerM4Primitives()
registerM5Primitives()
registerM6Primitives()
registerM7Primitives()
registerJobPublishPrimitives()

const conn = new Connection()
registerSensorBridge(conn)
// 手侧故障日志经既有 WS 报给脑,由脑统一出站。插件自己不连任何云端。
installHandLogSink((data) => {
  conn.emitHandLog(data)
})
// 平台埋点上报拦截规则的自检。必须排在 installHandLogSink 之后 —— 它可能立刻
// 报一条"无法自检",sink 还没装上就报会被丢掉。
registerNetGuard()
const reloadStartup = refreshPagesAfterRuntimeReload()
  .then((count) => {
    if (count > 0) console.log('[hand] 自重载后已刷新平台页', count)
  })
  .catch((error: unknown) => {
    // 当前有人值守阶段允许人工刷新兜底；插件自身仍须上线，让脑能报告结果。
    console.warn('[hand] 自重载后的页面刷新未完成', error)
  })

function ensureConnectedAfterReload(): void {
  void reloadStartup.then(() => conn.ensureConnected())
}

ensureConnectedAfterReload()

// 看门狗:chrome.alarms 是基础设施用途(禁令 1 豁免)。SW 死透后 setTimeout 重连链断,
// alarm 周期唤醒 SW 并续连。最小间隔约 30s,这里 60s。
const WATCHDOG_ALARM = 'reconnect-watchdog'
chrome.alarms.create(WATCHDOG_ALARM, { periodInMinutes: 1 })

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === WATCHDOG_ALARM) ensureConnectedAfterReload()
})

chrome.runtime.onStartup.addListener(ensureConnectedAfterReload)
chrome.runtime.onInstalled.addListener(ensureConnectedAfterReload)

// options 页与将来的 popup 可查连接状态。
chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg?.type === 'getStatus') {
    sendResponse(conn.status())
    return true
  }
  if (handleInfrastructureMessage(msg, sendResponse)) return true
  return undefined
})
