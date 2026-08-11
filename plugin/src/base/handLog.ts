// 手侧故障日志(base 层)。出事时经既有 WS 的 handLog 事件报给脑,由脑统一出站
// —— 插件自己不连任何云端,这一点是构造性的,别在这里开 http。
//
// 三条纪律,改这个文件前先读清楚:
//
//  1. **不携带任何候选人明文。** message/detail 里不许出现姓名、聊天正文、简历、
//     微信号,也不许把页面文本原样塞进来。手不该知道也不需要知道 —— 脑那边按
//     引用自己去补(见 client/service/internal/logcontext)。
//  2. **不设定时器。** 节流靠比较上次发送时间戳,不是 setInterval/alarms。
//     手的禁令第 1 条只给重连兜底与心跳留了口子。
//  3. **不持久化。** 连不上脑就丢 —— handLog 走 QoS0 事件通道,本来就是可丢的。
//     手的失忆是设计前提,不是要修的 bug。
import type { HandLogEventData, HandLogLevel } from './protocol'

export type { HandLogLevel }

/** 手侧故障分类。刻意不是契约里的封闭枚举:手侧故障形态不可预知,每加一种都要
 *  改契约并让客户机换代插件,代价与收益不相称(契约里 code 就是自由字符串)。 */
export const HandLogCode = {
  /** 证词库不可用 —— 最严重的一种:真实外部副作用能力会整体停用。 */
  WitnessUnavailable: 'witnessUnavailable',
  /** result 落不进持久 outbox。发送前的最后一道保险没了。 */
  OutboxWriteFailed: 'outboxWriteFailed',
  /** 重连后补投持久 outbox 失败。 */
  OutboxReplayFailed: 'outboxReplayFailed',
  /** 要发的 body 不符合生成契约。通常意味着两端版本对不上。 */
  OutboundBodyInvalid: 'outboundBodyInvalid',
  /** 平台埋点上报的拦截规则疑似失效:检测脚本还在,规则却长期零命中。 */
  EnvReportGuardStale: 'envReportGuardStale',
  /** 拦截规则装上了,但没法自检(onRuleMatchedDebug 不可用)。 */
  EnvReportGuardBlind: 'envReportGuardBlind',
} as const

/** 同 code 的最小上报间隔。与契约里 handLog 的 throttle 标注一致。
 *  真正的合并与速率控制在脑侧(logreport/throttle.go),这里只挡住最粗的刷屏。 */
const MIN_INTERVAL_MS = 60_000

type Sink = (data: HandLogEventData) => void

let sink: Sink | null = null
const lastSentAt = new Map<string, number>()

/** 由 background 在建好连接后装上。传 null 卸载(测试用)。 */
export function installHandLogSink(next: Sink | null): void {
  sink = next
  if (next === null) lastSentAt.clear()
}

/**
 * 报一条手侧故障。**永远不抛异常** —— 它挂在各种错误处理路径上,
 * 在那里再抛一次只会把原始故障盖掉。
 */
export function reportHandLog(
  level: HandLogLevel,
  code: string,
  message: string,
  detail?: string,
): void {
  try {
    const current = sink
    if (current === null) return

    const now = Date.now()
    const previous = lastSentAt.get(code)
    if (previous !== undefined && now - previous < MIN_INTERVAL_MS) return
    lastSentAt.set(code, now)

    const data: HandLogEventData = {
      level,
      code: code.slice(0, 64),
      // 契约上限 2048/4096。截断而不是拒发:半条诊断信息也比没有强。
      message: message.slice(0, 2048),
      at: now,
    }
    if (detail !== undefined && detail !== '') data.detail = detail.slice(0, 4096)
    current(data)
  } catch {
    // 上报自身出问题时静默 —— 手侧没有别的地方可报了,而原始故障更重要。
  }
}

/** 把任意 catch 到的东西压成一行不含页面内容的说明。 */
export function describeError(error: unknown): string {
  if (error instanceof Error) return `${error.name}: ${error.message}`
  if (typeof error === 'string') return error
  return typeof error
}
