// 请求录制的 chrome 接线(base)。纯逻辑在 netCapture.ts,这里只做四件事:
// 监听注册(顶层、常驻)、录制状态的落盘与读回、到点告警、给弹窗的消息面。
//
// # 为什么监听常驻而不是开始时才注册
//
// MV3 的 SW 会被杀;只有在脚本顶层同步注册的监听器,事件到来时才会把 SW 叫醒。
// 录制中途动态 addListener 的监听器随 SW 一起死,录制就静默中断。所以五个监听器
// 永远挂着、过滤 `<all_urls>`,由「录制截止时刻」决定记不记;截止时刻落盘,SW 重启后
// 读回来,不靠内存。不在窗口内时监听器只做一次时间比较就返回。
//
// 派发宽、记录窄:过滤器宽只影响「哪些事件送到我们这里」,页面看不见;记录范围由
// shouldRecord 收到平台站点(能力上限,见 netCapture.ts)。
//
// 到点靠 chrome.alarms 一次性告警——基础设施用途,禁令 1 豁免;它只负责 flush 在途
// 记录并把状态改成已结束,记不记本身每条事件都核对截止时刻,告警不是闸。
import {
  CAPTURE_DURATION_MS, CAPTURE_STATE_KEY, CaptureSession, CaptureState, captureActive, shouldRecord,
} from './netCapture'
import { KIND_REQUEST, TelemetryStorage, clear, count } from './store'
import { registerTabHostTracking, tabHost } from './tabHosts'
import { netCaptureSites } from '../../program/platform/netCaptureSites'

const END_ALARM = 'net-capture-end'

const storage: TelemetryStorage = {
  get: (keys) => chrome.storage.local.get(keys as string | string[]),
  set: (items) => chrome.storage.local.set(items),
  remove: (keys) => chrome.storage.local.remove(keys as string | string[]),
}

let state: CaptureState | null = null
/** 本 SW 生命周期之前已落盘的条数;本周期的在 session.writtenCount()。 */
let writtenBefore = 0
let loaded: Promise<void> = Promise.resolve()
let session: CaptureSession | null = null

export interface CaptureStatus {
  readonly active: boolean
  readonly state: CaptureState | null
  readonly recorded: number
  readonly pending: number
  readonly now: number
  /** 权限缺席时给弹窗的一句话。 */
  readonly unavailable?: string
}

function snapshot(now: number): CaptureStatus {
  if (session === null) {
    return { active: false, state: null, recorded: 0, pending: 0, now, unavailable: '无 webRequest 权限,请求录制未启用' }
  }
  return {
    active: captureActive(state, now),
    state,
    recorded: writtenBefore + session.writtenCount(),
    pending: session.pendingCount(),
    now,
  }
}

async function endCapture(now: number): Promise<void> {
  await chrome.alarms.clear(END_ALARM)
  if (session) await session.end()
  if (state && state.endedAt === undefined) {
    state = { ...state, endedAt: now }
    await storage.set({ [CAPTURE_STATE_KEY]: state })
  }
}

/** 到点了但告警还没来(SW 死过、时钟拨过):谁先看到谁收尾。 */
async function settleIfExpired(now: number): Promise<void> {
  if (state && state.endedAt === undefined && now >= state.until) await endCapture(now)
}

export function registerNetCapture(): void {
  if (typeof chrome.webRequest?.onBeforeRequest?.addListener !== 'function') {
    console.warn('[hand] 无 webRequest 权限,请求录制未启用')
    return
  }
  const live = new CaptureSession({
    store: storage,
    onError: (error) => { console.error('[hand] 请求录制落盘失败', error) },
  })
  session = live
  registerTabHostTracking()

  loaded = Promise.all([storage.get(CAPTURE_STATE_KEY), count(storage, KIND_REQUEST)])
    .then(([got, written]) => {
      const raw = got[CAPTURE_STATE_KEY]
      state = typeof raw === 'object' && raw !== null ? raw as CaptureState : null
      writtenBefore = written
    })
    .catch((error: unknown) => {
      console.warn('[hand] 请求录制状态读取失败,本周期不录', error)
      state = null
    })

  const filter = { urls: ['<all_urls>'] }
  // 五个阶段都排在状态读回之后:冷启动时前几条请求的事件才不会乱序到 session 里。
  chrome.webRequest.onBeforeRequest.addListener((details) => {
    void loaded.then(() => {
      if (!captureActive(state, Date.now())) return
      const host = tabHost(details.tabId)
      if (!shouldRecord(details, host, netCaptureSites)) return
      live.begin(details, host)
    })
    // 观察型监听器:不返回任何东西,请求原样继续。
  }, filter, ['requestBody'])
  chrome.webRequest.onSendHeaders.addListener((details) => {
    void loaded.then(() => live.sendHeaders(details))
  }, filter, ['requestHeaders'])
  chrome.webRequest.onHeadersReceived.addListener((details) => {
    void loaded.then(() => live.headersReceived(details))
  }, filter, ['responseHeaders'])
  chrome.webRequest.onCompleted.addListener((details) => {
    void loaded.then(() => live.completed(details))
  }, filter)
  chrome.webRequest.onErrorOccurred.addListener((details) => {
    void loaded.then(() => live.errored(details))
  }, filter)

  chrome.alarms.onAlarm.addListener((alarm) => {
    if (alarm.name !== END_ALARM) return
    void loaded
      .then(() => endCapture(Date.now()))
      .catch((error: unknown) => { console.error('[hand] 请求录制到点收尾失败', error) })
  })
}

/** 开始新一轮。上一轮的记录清掉——一轮一份,导出的文件就是这一轮;按钮文案已写明。 */
export async function startCapture(now = Date.now()): Promise<CaptureStatus> {
  await loaded
  if (session === null) return snapshot(now)
  if (captureActive(state, now)) return snapshot(now)
  await settleIfExpired(now)
  await clear(storage, KIND_REQUEST)
  session.reset()
  writtenBefore = 0
  state = { startedAt: now, until: now + CAPTURE_DURATION_MS }
  await storage.set({ [CAPTURE_STATE_KEY]: state })
  await chrome.alarms.create(END_ALARM, { when: state.until })
  return snapshot(now)
}

export async function stopCapture(now = Date.now()): Promise<CaptureStatus> {
  await loaded
  if (session !== null && state !== null && state.endedAt === undefined) await endCapture(now)
  return snapshot(now)
}

export async function captureStatus(now = Date.now()): Promise<CaptureStatus> {
  await loaded
  if (session !== null) await settleIfExpired(now)
  return snapshot(now)
}
