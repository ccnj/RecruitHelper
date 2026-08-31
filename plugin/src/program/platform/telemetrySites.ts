// 观测站点表:哪个平台把埋点发到哪个端点、载荷里哪几样要紧。
//
// 这是**平台知识**,所以住在 program。base 只管机制(请求体还原、深搜、落盘),
// 监听注册仍在 base——手的禁令第 5 条,program 不注册任何 chrome 监听。
//
// 站点表和 sites.ts 是两件事:那张表管"这是哪个平台的页、登录态怎么读",
// 是业务感知;这张表管"平台自己往外发什么",是观测,不参与任何业务裁决。

import { EMPTY_DIGEST, TelemetryDigest, deepFind } from '../../base/telemetry/capture'

export interface TelemetrySite {
  readonly id: string
  /** `chrome.webRequest` 过滤器的 urls。 */
  readonly urls: readonly string[]
  /** 从解析后的载荷抽摘要与轨迹窗口。形状不认识就返回空,不猜。 */
  digest(payload: unknown, at: number): TelemetryDigest
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

/**
 * 平台在 `it.p` 里放的是一个 JSON 字符串,其中 `time` 是**平台自己的时刻**。
 * 取不到就退回本次捕获时刻——差的是几十毫秒,不值得为它丢一条轨迹。
 */
function platformTime(item: Record<string, unknown>, fallback: number): number {
  if (typeof item['p'] !== 'string') return fallback
  try {
    const parsed = asRecord(JSON.parse(item['p']))
    const time = Number(parsed?.['time'])
    return Number.isFinite(time) ? time : fallback
  } catch {
    return fallback
  }
}

/**
 * BOSS 直聘。端点与字段取自 hiBoss 的逆向结论(`report/runtime-evidence.md` §12/§13),
 * 我方**尚未真机见过任何一条载荷**——所以这里只抽两样最要紧的,其余原样落盘。
 *
 * - `p2` 是 zpAegis 通道的事件码(设备指纹上报本身、行为异常命中都在这里)
 * - `cnTextCount` 属于 inputTrait,**只在载荷里、本地账本根本没有**
 * - `web-event-click` 且 `p6` 是对象的那些 item 带完整鼠标轨迹,单独进轨迹环,
 *   免得被页面加载噪声(单条几十 KB 的性能上报)挤掉
 */
export const bossTelemetrySite: TelemetrySite = {
  id: 'boss',
  urls: ['https://apm-fe.zhipin.com/wapi/zpApm/actionLog/*'],
  digest(payload, at) {
    const root = asRecord(payload)
    if (!root) return EMPTY_DIGEST

    const shots: unknown[] = []
    const items = root['items']
    if (Array.isArray(items)) {
      for (const raw of items) {
        const item = asRecord(raw)
        if (!item) continue
        if (item['action'] !== 'web-event-click') continue
        if (asRecord(item['p6']) === null) continue
        shots.push({
          at,
          t: platformTime(item, at),
          p6: item['p6'],
          p4: item['p4'],
          p7: item['p7'],
          p8: item['p8'],
        })
      }
    }

    return {
      summary: {
        codes: deepFind(payload, 'p2').map(String),
        cnTextCount: deepFind(payload, 'cnTextCount'),
      },
      shots,
    }
  },
}

/** 全部观测站点。加一个平台要动两处:本表,和 manifest 的 host_permissions。 */
export const telemetrySites: readonly TelemetrySite[] = [bossTelemetrySite]

// ── 判读:一条载荷里哪些是例行、哪些是探测命中 ──────────────────────────────
//
// 判读表取自 hiBoss 的逆向结论(`report/boss-detection-report.md` §354/§692),
// 我方**尚未真机核对过任何一条**。分类只看 (端点, action),不看码长什么样。

/** zpAegis 通道:`p2` 是事件码;patas APM 通道的 `p2` 是页面 URL,码藏在别处。 */
const AEGIS_ENDPOINT = /\/actionLog\/fe\/ie\/common\.json/

/** 行为通道里的良性码。 */
const ROUTINE_BEHAVIOR = new Set(['0', '30004', '30005', '30006'])

/** 设备指纹上报本身——每次页面加载无条件发,跟检测到什么无关。 */
const FINGERPRINT = new Set(['800001', '800003', '800009'])

export interface BossCodeMeaning {
  readonly code: string
  /** 一句话说明这个码报的是什么。不认识的码原样回显,不编。 */
  readonly label: string
  /**
   * 在 hiBoss 的码表里查得到吗。
   *
   * 查不到就是**平台出了我们没见过的东西**——要么它改版了,要么我们碰到了新分支。
   * 这值得单独抬头看,所以做成布尔位;靠比对 label 文案来判是脆的。
   */
  readonly known: boolean
  /**
   * 近乎每台机器都会报,**不是探到了东西**。
   *
   * 本机端口探测走 `si()`,而它的回调是 `onopen = onclose = onerror`,
   * 参数是「**1 秒内有反应**」而不是「连上了」(实证:`sec370.clean.js:2136-2147`)。
   * localhost 上端口关着会瞬间 ECONNREFUSED —— 照样算真。所以这一族码在
   * 任何一台机器上都会亮,判据本身有问题。
   */
  readonly nearUniversal?: boolean
}

/**
 * 事件码释义。取自 hiBoss `report/boss-detection-report.md` §4.2/§4.3 与
 * `evidence/deobfuscated/sec370.clean.js` 的码表(`co`/`za`),我方未逐条真机核对。
 *
 * `nearUniversal` 那一族是例外:判据语义由我方 2026-08-28 直接读反混淆源码确认。
 */
const CODE_MEANINGS: Record<string, Omit<BossCodeMeaning, 'code' | 'known'>> = {
  '0': { label: '无异常' },
  '30004': { label: 'TYPING·正常打字分类' },
  '30005': { label: 'ENTER·回车分类' },
  '30006': { label: 'SDK 版本自报' },

  // 设备指纹上报本身:每次页面加载无条件发,跟检测到什么无关。
  '800001': { label: '设备指纹·内网 IP + 未知全局名差集 + 缺失 API 矩阵' },
  '800003': { label: '设备指纹·UA-CH/屏幕/GPU/canvas 哈希(注意:800001/800009 在聚合豁免表里,唯独它不在,会被算进 559991)' },
  '800009': { label: '设备指纹·语音合成深度指纹' },

  // 本机端口 / 伴随进程探测。整族都受 si() 那个判据缺陷影响。
  '550237': { label: '探本机 18789 端口(openGateway),1 秒内有反应', nearUniversal: true },
  '550238': { label: '探本机 18789 的 /__openclaw__/ping' },
  '550239': { label: '探本机 9222 端口(Chrome 默认远程调试口),1 秒内有反应', nearUniversal: true },
  '550241': { label: '探本机 8642 端口(hermes)' },
  '550243': { label: '探本机 opencli' },
  '550245': { label: '探本机 10086 端口(kimi),1 秒内有反应(含连接被拒)', nearUniversal: true },
  '550247': { label: '本机 10086 WebSocket 真正握手成功(kimi)' },
  '910013': { label: '探本机 35600 端口,五秒内毫无反应(不是拒绝,是没声音);p3 带 CDP 状态' },
  '900067': { label: '探到 AdsPower(20725)' },
  '900073': { label: '探到花漾(47326)' },
  '900075': { label: '探到 VMLogin(5100)' },

  // 扩展与自动化痕迹。
  '550091': { label: '页面上 chrome.runtime 可用,且被探的那个扩展 ID 确实装着' },
  // 2026-08-28 对照实验坐实:关掉 MetaMask 后这条当场消失。MetaMask 的
  // externally_connectable 写的是 `http://*/*` + `https://*/*`(对全互联网开放),
  // 所以任何装了它的机器都会触发这条。实测干净 Chrome 148 上
  // window.chrome 是 object 而 window.chrome.runtime 是 undefined,可见它确实
  // 只在有扩展对该域声明了 externally_connectable 时才出现。
  '550094': { label: '页面上 chrome.runtime 竟然可用——有扩展对该域声明了 externally_connectable(实测:MetaMask 对全站声明,装了就报)' },
  '550003': { label: 'CDP / Playwright 痕迹' },
  '800015': { label: 'devtools/CDP 探测命中(不可配置的 stack getter 被读)' },
  '800025': { label: 'navigator 原型链上有非原生实现' },
  '550013': { label: 'navigator 指纹串命中(userAgent,userAgent,sendBeacon)' },
  '700009': { label: 'isTrusted 检查' },
  '99003': { label: 'Object.keys(window) 与白名单的差集(未知全局名)' },

  // 聚合补报。
  // 2026-08-28 两次真机 + 一次对照实验。第一次聚合的是
  // 800003 + 550094 + 550237/550239/550245;关掉 MetaMask(550094 的唯一来源)后
  // 重测,它**照样发**,聚合成 800003 + 550237/550239/550245 —— 也就是说
  // **一台没有任何可疑扩展的干净机器,仍然会触发 BOSS 的聚合告警**,
  // 而构成它的全是 BOSS 自己的例行指纹上报加自己那族坏掉的端口探测。
  //
  // 这与 hiBoss「559991/559999 都需 window.Block 为真(已进入封禁/拦截场景)才发」
  // 的读法对不上:两次都是普通浏览,无任何封禁迹象。要么该标志在正常会话里也为真、
  // 根本不是封禁位,要么那条前置读得不全 —— 待查。按文档信任边界,以我方真机为准。
  '559991': { label: '聚合补报:累计命中 >2 个不同的非豁免码(p3=码列表、p4=数量)。真机首验:普通会话即会触发' },
  '559999': { label: '聚合补报:命中 Sa 表' },
}

export function bossCodeMeaning(code: string): BossCodeMeaning {
  const found = CODE_MEANINGS[code]
  return found
    ? { code, ...found, known: true }
    : { code, label: '未知码(hiBoss 的码表里没有,原样记下)', known: false }
}

export function bossCodeLabel(code: string): string {
  const meaning = bossCodeMeaning(code)
  return meaning.label === '' ? code : `${code}(${meaning.label})`
}

export interface BossClassification {
  /** 非例行的事件码——**这些才是探测命中**。 */
  readonly hits: readonly { code: string; action: string }[]
  /** 例行码:指纹上报本身、正常打字分类。 */
  readonly routine: readonly { code: string; action: string }[]
  /**
   * 800001 的 `p6`:BOSS 拿 `Object.keys(window)` 与自己那份白名单求的差集,
   * **原样上送**。我们的插件有没有被点名,看的就是这一栏。
   */
  readonly unknownGlobals: readonly string[]
  /** risk-monitor 报告的注入项。 */
  readonly injected: readonly unknown[]
  /** 被探到的本机端口。 */
  readonly localProbes: readonly string[]
}

const EMPTY_CLASSIFICATION: BossClassification = {
  hits: [], routine: [], unknownGlobals: [], injected: [], localProbes: [],
}

export function classifyBossEntry(url: string, payload: unknown): BossClassification {
  const root = asRecord(payload)
  const items = root?.['items']
  if (!Array.isArray(items)) return EMPTY_CLASSIFICATION

  const aegis = AEGIS_ENDPOINT.test(url)
  const hits: { code: string; action: string }[] = []
  const routine: { code: string; action: string }[] = []
  const unknownGlobals: string[] = []
  const injected: unknown[] = []
  const localProbes: string[] = []

  for (const raw of items) {
    const item = asRecord(raw)
    if (!item) continue
    const action = String(item['action'] ?? '')

    if (aegis) {
      const code = String(item['p2'] ?? '')
      if (code === '') continue
      const isDevice = action === 'device-action-report'
      const benign = isDevice ? FINGERPRINT.has(code) : ROUTINE_BEHAVIOR.has(code)
      ;(benign ? routine : hits).push({ code, action })
      if (isDevice && code === '800001' && typeof item['p6'] === 'string' && item['p6'] !== 'null') {
        unknownGlobals.push(...item['p6'].split('|').filter(Boolean))
      }
      continue
    }

    if (action === 'action_js_risk_monitor') {
      const parsed = typeof item['p7'] === 'string' ? tryParse(item['p7']) : null
      const list = asRecord(parsed)?.['insertList']
      if (Array.isArray(list)) injected.push(...list)
    } else if (action === 'action_api_monitor') {
      const parsed = asRecord(typeof item['p4'] === 'string' ? tryParse(item['p4']) : null)
      const probed = parsed?.['url']
      if (typeof probed === 'string' && /^https?:\/\/(127\.0\.0\.1|localhost)[:/]/.test(probed)) {
        localProbes.push(probed)
      }
    }
  }

  return { hits, routine, unknownGlobals, injected, localProbes }
}

function tryParse(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    // 形状变了 —— 原文已经完整落在 payload 里,不猜。
    return null
  }
}
