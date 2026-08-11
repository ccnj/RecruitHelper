// 平台埋点上报拦截的在线自检(base 层 —— 宪法禁令 5:监听只在 base)。
//
// 拦的是什么:智联 environment-check 脚本在 document 上全局监听 click 与 keydown,
// 凡 event.isTrusted 为假(即代码合成的事件)就把企业 ID、员工账号 ID 与被点元素的
// DOM 路径 POST 到 /api/security/environment。拦截规则见 rules/zhilian-env-report.json,
// 事实与选型依据见 docs/点击输入通道选型-2026-08-11.md。
//
// 为什么必须自检:平台改端点、改路径,或把这段检测挪进反爬 VMP,规则就不再匹配 ——
// 而那不会有任何报错,只会安静地恢复上报,我方毫不知情。所以"沉默"必须能被区分:
//
//   检测脚本还在 + 近期有命令派发 + 规则零命中   → 规则失效了,报给脑
//   检测脚本已不在                              → 平台不查了,正常
//
// 判据刻意拿"近期有命令派发"当作"必然发生过合成点击"的代理:精确的点击计数取不到
// (点击在 MAIN world 的注入函数里,SW 数不着),而只要巡检在跑就必然点过东西。粗一点
// 无妨 —— 这道自检的失效方向是"漏报一次失效",不是"误停业务"。它只观测、只上报,
// 任何一步出问题都不得影响命令派发。
import { HandLogCode, describeError, reportHandLog } from './handLog'

/** manifest 里声明的静态规则集 id。改名必须同步改 manifest,否则启用核对会误报。 */
const RULESET_ID = 'zhilian_env_report'

/** 检测脚本执行后在页面 window 上留下的键(2026-08-11 真机实测存在;值为 undefined,故用 in 判)。 */
const ENV_CHECK_MARKER = 'ada:extension:shared-module:rd6.zhaopin.com:.:environment-check:default'

const PLATFORM_URL_PATTERN = 'https://rd6.zhaopin.com/*'

/** 这段时间内命中过规则就算健康,不必探测。 */
const HEALTHY_WINDOW_MS = 30 * 60_000
/** 攒够这么多条命令仍零命中,才值得去页面上问一次。 */
const PROBE_AFTER_DISPATCHES = 20
/** 两次探测的最小间隔,避免反复注入。 */
const PROBE_MIN_INTERVAL_MS = 10 * 60_000

let blockedCount = 0
let lastBlockedAt = 0
let dispatchedSinceProbe = 0
let lastProbeAt = 0
let probing = false
/** 能不能观测到规则命中。观测不到就整体关闭自检 —— 判据的前提是"零命中可信",
 *  前提不成立时再去探测,只会每隔一段时间报一次假的"规则失效"。 */
let feedbackAvailable = false

export interface NetGuardStats {
  /** 自 SW 启动以来拦下的上报次数。SW 重启即归零 —— 这是观测量,不是账本。 */
  readonly blockedCount: number
  /** 最近一次拦下的时刻(epoch ms),0 表示从未。 */
  readonly lastBlockedAt: number
}

export function netGuardStats(): NetGuardStats {
  return { blockedCount, lastBlockedAt }
}

/** 测试用:把计数复位。 */
export function resetNetGuardForTest(): void {
  blockedCount = 0
  lastBlockedAt = 0
  dispatchedSinceProbe = 0
  lastProbeAt = 0
  probing = false
  feedbackAvailable = false
}

interface RuleMatchedDebugEvent {
  addListener(callback: () => void): void
}

/**
 * 由 background 启动时装上。
 *
 * `onRuleMatchedDebug` 只对开发者模式加载的扩展开放 —— 本产品正是以 unpacked 形式
 * 交付(见 docs/插件交付与更新决策-2026-07-25.md),符合;若日后改为打包分发,这道
 * 自检会失明,故此处显式报一条 warn 而不是静默跳过。
 */
export function registerNetGuard(): void {
  const api = chrome.declarativeNetRequest as unknown as {
    onRuleMatchedDebug?: RuleMatchedDebugEvent
  }
  const event = api.onRuleMatchedDebug
  if (event === undefined || typeof event.addListener !== 'function') {
    reportHandLog(
      'warn',
      HandLogCode.EnvReportGuardBlind,
      '埋点上报拦截规则无法自检:onRuleMatchedDebug 不可用(非开发者模式加载?)',
    )
    return
  }
  event.addListener(() => {
    blockedCount += 1
    lastBlockedAt = Date.now()
    dispatchedSinceProbe = 0
  })
  feedbackAvailable = true
  void verifyRulesetEnabled()
}

/**
 * 规则集有没有被 Chrome 真正启用 —— 规则文件路径写错、JSON 语法有问题、或者当前
 * Chrome 版本不认某个字段时,规则集会静默不启用。这种情形靠"零命中探测"要绕一大圈
 * 才发现且原因不明,而启动时一查就知道,所以单独核对一次。
 *
 * 只报告,不尝试补救:动态规则是另一套 API 与另一套失效模式,为一个配置错误再引一套
 * 机制不划算 —— 报出来让人改文件即可。
 */
async function verifyRulesetEnabled(): Promise<void> {
  try {
    const enabled = await chrome.declarativeNetRequest.getEnabledRulesets()
    if (enabled.includes(RULESET_ID)) return
    reportHandLog(
      'error',
      HandLogCode.EnvReportGuardOff,
      `埋点上报拦截规则集未启用:${RULESET_ID} 不在已启用列表内,上报未被拦截`,
      `enabled=${enabled.join(',')}`,
    )
  } catch (error) {
    reportHandLog(
      'warn',
      HandLogCode.EnvReportGuardOff,
      '无法确认埋点上报拦截规则集是否已启用',
      describeError(error),
    )
  }
}

/** 每收到一条 cmd 调一次。**永远不抛异常** —— 它挂在命令派发的主路上。 */
export function noteCommandDispatched(): void {
  try {
    // 观测不到命中时,"零命中"不是证据,整条判据失去意义。已经报过一条
    // EnvReportGuardBlind 说明这个事实,不再反复探测与误报。
    if (!feedbackAvailable) return
    dispatchedSinceProbe += 1
    const now = Date.now()
    if (now - lastBlockedAt < HEALTHY_WINDOW_MS) return
    if (dispatchedSinceProbe < PROBE_AFTER_DISPATCHES) return
    if (now - lastProbeAt < PROBE_MIN_INTERVAL_MS) return
    if (probing) return
    probing = true
    lastProbeAt = now
    void probeEnvCheckPresence().finally(() => {
      probing = false
    })
  } catch {
    // 自检自身出问题绝不影响命令派发。
  }
}

async function probeEnvCheckPresence(): Promise<void> {
  try {
    const tabs = await chrome.tabs.query({ url: PLATFORM_URL_PATTERN })
    let tabId: number | undefined
    for (const tab of tabs) {
      if (typeof tab.id === 'number') {
        tabId = tab.id
        break
      }
    }
    // 没有平台页时无从判断,不下结论。
    if (tabId === undefined) return
    const results = await chrome.scripting.executeScript({
      target: { tabId },
      world: 'MAIN',
      // 自包含函数:executeScript 会序列化它,不得引用模块闭包。
      func: (marker: string) => marker in window,
      args: [ENV_CHECK_MARKER],
    })
    const present = results[0]?.result === true
    if (!present) {
      // 平台不查了,别再反复探。
      dispatchedSinceProbe = 0
      return
    }
    reportHandLog(
      'error',
      HandLogCode.EnvReportGuardStale,
      '埋点上报拦截规则疑似失效:检测脚本仍在页面上,但规则长期零命中',
      `dispatched=${dispatchedSinceProbe} blocked=${blockedCount} lastBlockedAt=${lastBlockedAt}`,
    )
    dispatchedSinceProbe = 0
  } catch {
    // 探测失败(页面未就绪、注入被拒)不构成"失效"结论,静默等下一轮。
  }
}
