// 载荷捕获:把平台自己发出去的埋点请求体抄一份存下来。
//
// 搬自 hiBoss `lab/extension/background.js` 的网络层,判读逻辑一处未改。
// 本文件只有**平台无关的机制**——请求体还原、深搜、落盘;"哪个端点、载荷里
// 哪个字段要紧"是平台知识,由 program 侧的观测站点表提供(见 telemetrySites.ts)。
//
// # 为什么在 service worker 上做,不在 content script 里做
//
// 上游的教训:content script 跑在页面主线程上,而平台的时序判据取的就是那条
// 线程上的 `Date.now()`。把 `chrome.storage` 的同步序列化留在那边,等于往被
// 判定的数据里掺我们自己的停顿。这一层跑在 SW 上,不占渲染器主线程。
//
// # 形状未知时的原则:先原样存下来,再谈解读
//
// 我们没见过真实载荷。所以解析成功就存结构、顺带抽摘要,失败就**存原文**
// (截断防爆)并记下错在哪——**形状不对正是最该看见的信息**,绝不静默丢。

import { KIND_CLICK, KIND_UPLOAD, MAX_CLICK_CHUNKS, MAX_UPLOAD_CHUNKS, TelemetryStorage, append } from './store'

/** 解析失败时保留的原文上限。 */
const RAW_KEEP_CHARS = 4000

/** 一条落盘的观测记录。`payload` 与 `raw` 互斥:解析成功存前者,失败存后者。 */
export interface TelemetryEntry {
  readonly at: number
  readonly url: string
  readonly payload?: unknown
  readonly raw?: string
  readonly parseError?: string
  /** 站点抽出的摘要,免得看每一条都要翻原文。 */
  readonly summary?: Record<string, readonly unknown[]>
}

/** 站点从一条载荷里抽出来的东西。 */
export interface TelemetryDigest {
  /** 摘要字段,直接进 entry。 */
  readonly summary: Record<string, readonly unknown[]>
  /** 单独进轨迹环的条目;没有就空数组。 */
  readonly shots: readonly unknown[]
}

/** 空摘要——载荷形状不认识时用它,不猜。 */
export const EMPTY_DIGEST: TelemetryDigest = { summary: {}, shots: [] }

/**
 * 把 `webRequest` 给的请求体还原成文本。
 *
 * 两种形态都处理:表单编码时 Chrome 直接给 `formData`(**已经解码过**,
 * 不能再 decodeURIComponent 一次);否则从 `raw` 的字节自己解,并剥掉
 * `content=` 这层 urlencode 外壳。字段名不是 `content` 就整个交出去,不猜。
 */
export function bodyText(body: chrome.webRequest.WebRequestBody | undefined | null): string | null {
  if (!body) return null

  const formData = body.formData
  if (formData) {
    const content = formData['content']
    if (Array.isArray(content) && content.length) return content[0] ?? null
    return JSON.stringify(formData)
  }

  const raw = body.raw
  if (raw?.length) {
    let text = ''
    for (const part of raw) {
      if (part.bytes) text += new TextDecoder('utf-8').decode(part.bytes)
    }
    const wrapped = /^content=([\s\S]*)$/.exec(text)
    if (!wrapped) return text
    try {
      return decodeURIComponent(wrapped[1].replace(/\+/g, ' '))
    } catch {
      // 不是合法的 percent-encoding:交出剥了壳的原文,别丢。
      return wrapped[1]
    }
  }

  return null
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

/** 在任意深度的对象里找一个键的全部取值——载荷的嵌套我们不完全掌握。 */
export function deepFind(value: unknown, key: string, out: unknown[] = []): unknown[] {
  if (value === null || typeof value !== 'object') return out
  if (Array.isArray(value)) {
    for (const item of value) deepFind(item, key, out)
    return out
  }
  for (const [k, v] of Object.entries(value)) {
    if (k === key) out.push(v)
    else deepFind(v, key, out)
  }
  return out
}

/** 解析一条载荷并落盘。`digest` 由站点提供;它抛异常时按"形状不认识"降级,不掀翻记录。 */
export async function recordUpload(
  store: TelemetryStorage,
  digest: (payload: unknown, at: number) => TelemetryDigest,
  at: number,
  url: string,
  text: string,
): Promise<void> {
  let payload: unknown = null
  let parsed = false
  let parseError: string | undefined

  try {
    payload = JSON.parse(text)
    parsed = true
  } catch (error) {
    parseError = `JSON 解析失败: ${message(error)}`
  }

  let extracted = EMPTY_DIGEST
  if (parsed) {
    try {
      extracted = digest(payload, at)
    } catch (error) {
      // 站点的抽取逻辑没跟上平台改版。载荷本身已经完整落盘,记一笔继续。
      parseError = `摘要抽取失败: ${message(error)}`
    }
  }

  // 解析成功存结构,失败存原文——两者互斥,但**任何一种失败都不丢**。
  const entry: TelemetryEntry = {
    at,
    url,
    ...(parsed ? { payload } : { raw: String(text).slice(0, RAW_KEEP_CHARS) }),
    ...(parseError === undefined ? {} : { parseError }),
    summary: extracted.summary,
  }

  await append(store, KIND_UPLOAD, [entry], MAX_UPLOAD_CHUNKS)
  if (extracted.shots.length) await append(store, KIND_CLICK, extracted.shots, MAX_CLICK_CHUNKS)
}
