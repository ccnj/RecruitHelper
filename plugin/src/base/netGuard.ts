// 平台埋点上报拦截的在线自检(base 层 —— 宪法禁令 5:监听只在 base)。
//
// 拦的是什么:智联 environment-check 脚本在 document 上全局监听 click 与 keydown,
// 凡 event.isTrusted 为假(即代码合成的事件)就把企业 ID、员工账号 ID 与被点元素的
// DOM 路径 POST 到 /api/security/environment。拦截规则见 rules/zhilian-env-report.json,
// 事实与选型依据见 docs/点击输入通道选型-2026-08-11.md。
//
// **为什么拦脚本而不是拦那个上报端点(2026-08-11 真机改定)**:拦端点时请求照常发起
// 并失败,而平台 http 模块的错误处理会把这次失败连同 staffId、请求路径一并送进它自己
// 的异常上报通道 —— 结果比不拦更糟:既暴露了"这里有合成事件"(正常用户的点击
// isTrusted 为真,根本不会产生这个请求),又暴露了"有人在拦"。拦脚本把链条掐在源头:
// 脚本不加载 → 监听器不注册 → 请求不发起 → 没有失败 → 没有异常上报。
//
// 真机实测(2026-08-11):脚本被拦后 window 标记不存在、无 CDN 换域重试;派发 4 次
// 合成事件后新增请求为零(经阳性对照确认记录功能正常);资源加载失败虽会触发平台
// 捕获阶段的 error 监听,但其上报函数首行 `if (!t.message) return` 把没有 message 的
// 资源错误挡在了上报之前;页面推荐列表等功能不受影响。
//
// 自检判据比拦端点时简单且强:**规则若生效,该脚本永远不会执行,它在页面上留下的
// window 键就永远不存在;键存在即规则失效**。无歧义,也不需要"零命中"这类旁证——
// 因此即使命中数不可观测(非开发者模式加载),自检照样成立。
import { HandLogCode, describeError, reportHandLog } from './handLog'

/** manifest 里声明的静态规则集 id。改名必须同步改 manifest,否则启用核对会误报。 */
const RULESET_ID = 'zhilian_env_report'

/** 检测脚本执行后在页面 window 上留下的键(2026-08-11 真机实测:未拦时存在,拦后不存在)。 */
const ENV_CHECK_MARKER = 'ada:extension:shared-module:rd6.zhaopin.com:.:environment-check:default'

const PLATFORM_URL_PATTERN = 'https://rd6.zhaopin.com/*'

/** 攒够这么多条命令才值得去页面上问一次。 */
const PROBE_AFTER_DISPATCHES = 20
/** 两次探测的最小间隔,避免反复注入。 */
const PROBE_MIN_INTERVAL_MS = 10 * 60_000

let blockedCount = 0
let lastBlockedAt = 0
let dispatchedSinceProbe = 0
let lastProbeAt = 0
let probing = false

export interface NetGuardStats {
  /** 自 SW 启动以来拦下的脚本请求次数。SW 重启即归零 —— 观测量,不是账本,也不是判据。 */
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
}

interface RuleMatchedDebugEvent {
  addListener(callback: () => void): void
}

/**
 * 由 background 启动时装上。
 *
 * `onRuleMatchedDebug` 只对开发者模式加载的扩展开放。本产品正是以 unpacked 形式交付
 * (见 docs/插件交付与更新决策-2026-07-25.md),但**即便它不可用,自检也不受影响**——
 * 判据是"脚本有没有执行",与能否观测命中无关。这里仍报一条 warn,只为说明命中数失去
 * 观测,不代表自检失效。
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
      '埋点上报拦截命中数不可观测(非开发者模式加载?);自检判据不依赖它,仍照常工作',
    )
  } else {
    event.addListener(() => {
      blockedCount += 1
      lastBlockedAt = Date.now()
    })
  }
  void verifyRulesetEnabled()
}

/** 每收到一条 cmd 调一次。**永远不抛异常** —— 它挂在命令派发的主路上。 */
export function noteCommandDispatched(): void {
  try {
    dispatchedSinceProbe += 1
    const now = Date.now()
    if (dispatchedSinceProbe < PROBE_AFTER_DISPATCHES) return
    if (now - lastProbeAt < PROBE_MIN_INTERVAL_MS) return
    if (probing) return
    probing = true
    lastProbeAt = now
    dispatchedSinceProbe = 0
    void probeEnvCheckAbsent().finally(() => {
      probing = false
    })
  } catch {
    // 自检自身出问题绝不影响命令派发。
  }
}

/**
 * 去平台页上确认那个检测脚本**没有**执行。
 *
 * 判据单向:标记存在 = 脚本跑起来了 = 规则没拦住,合成事件正在被实名上报,必须报。
 * 标记不存在或问不到,都不下"失效"的结论。
 */
async function probeEnvCheckAbsent(): Promise<void> {
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
    if (results[0]?.result !== true) return
    reportHandLog(
      'error',
      HandLogCode.EnvReportGuardStale,
      '埋点上报拦截规则已失效:检测脚本仍在页面上执行,合成事件正被实名上报',
      `blocked=${blockedCount} lastBlockedAt=${lastBlockedAt}`,
    )
  } catch {
    // 探测失败(页面未就绪、注入被拒)不构成"失效"结论,静默等下一轮。
  }
}

/**
 * 规则集有没有被 Chrome 真正启用 —— 规则文件路径写错、JSON 语法有问题、或者当前
 * Chrome 版本不认某个字段时,规则集会静默不启用。这种情形靠探测要等攒够派发数才发现,
 * 而启动时一查就知道,所以单独核对一次。
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
      `埋点上报拦截规则集未启用:${RULESET_ID} 不在已启用列表内,检测脚本将照常加载`,
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
