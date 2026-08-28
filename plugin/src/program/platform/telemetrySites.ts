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
