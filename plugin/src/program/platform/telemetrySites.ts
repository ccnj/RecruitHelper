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

const CODE_LABELS: Record<string, string> = {
  '0': '0(无异常)',
  '30004': '30004(TYPING·正常打字分类)',
  '30005': '30005(ENTER·回车分类)',
  '30006': '30006(SDK 版本自报)',
  '800001': '800001(设备指纹·IP/全局名/API 矩阵)',
  '800003': '800003(设备指纹·屏幕/GPU/canvas)',
  '800009': '800009(设备指纹·语音合成)',
}

export function bossCodeLabel(code: string): string {
  return CODE_LABELS[code] ?? code
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
