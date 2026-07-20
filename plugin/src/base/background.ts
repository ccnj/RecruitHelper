// SW 入口(base):注册全部 chrome 监听(宪法禁令 5:监听只在 base),接线连接与原语。
import { Connection } from './connection'
import { handleInfrastructureMessage } from './optionsBridge'
import { registerSensorBridge } from './sensorBridge'
import { registerDebugPrimitives } from '../program/primitives/debug'
import { registerM2Primitives } from '../program/primitives/m2'
import { registerM3Primitives } from '../program/primitives/m3'

// program 原语注册(program 不注册任何 chrome 监听,只填这张表)。
registerDebugPrimitives()
registerM2Primitives()
registerM3Primitives()

const conn = new Connection()
registerSensorBridge(conn)
conn.ensureConnected()

// 看门狗:chrome.alarms 是基础设施用途(禁令 1 豁免)。SW 死透后 setTimeout 重连链断,
// alarm 周期唤醒 SW 并续连。最小间隔约 30s,这里 60s。
const WATCHDOG_ALARM = 'reconnect-watchdog'
chrome.alarms.create(WATCHDOG_ALARM, { periodInMinutes: 1 })

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === WATCHDOG_ALARM) conn.ensureConnected()
})

chrome.runtime.onStartup.addListener(() => conn.ensureConnected())
chrome.runtime.onInstalled.addListener(() => conn.ensureConnected())

// options 页与将来的 popup 可查连接状态。
chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg?.type === 'getStatus') {
    sendResponse(conn.status())
    return true
  }
  if (handleInfrastructureMessage(msg, sendResponse)) return true
  return undefined
})
