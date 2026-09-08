// 请求录制:把平台页面在一段时间窗口内发出的全部请求抄一份存本机。
//
// 通道与平台埋点观测是同一条:`chrome.webRequest` 的观察型监听器。不阻塞、不改头、
// 不改体、不进页面 JS 世界,平台收到与发出的字节和没有我们时逐字节相同——这是
// 「BOSS 下不新增暴露面」(2026-09-08 甲方要求)的全部依据,机制上没有第二条通道。
//
// 本文件只有**平台无关的纯逻辑**:发起方闸、敏感头剥离、请求体摘要、四事件合成、
// 缓冲落盘。chrome API 的接线在 netCaptureRegister.ts,平台域名表在 program 侧的
// netCaptureSites.ts(平台知识住 program,监听注册只在 base——手的禁令第 5 条)。
//
// # 能力上限(2026-09-08 甲方要求)
//
// 只记平台站点发出的请求,别的网站一律不记。webRequest 的过滤器只认目的地 URL,
// 没有「按发起方过滤」这一项,所以上限落在 `shouldRecord` 这一个纯函数里,由单测
// 钉死:发起方不是平台、所在标签页也不是平台的请求,任何形态下都不得进入记录。
// 目的地刻意不限——平台页面向 CDN、第三方统计与 127.0.0.1 端口探测发的请求正是
// 要看的东西。
//
// # 看不到的东西(webRequest 的硬限制,不是本实现的取舍)
//
// 响应正文、WebSocket 帧(只见握手)。Cookie / Authorization 一类凭据头本来要额外
// 申请 `extraHeaders` 才可见,这里不申请,并且落盘前再按名字剥一遍——导出文件会被
// 人传来传去,账号会话不能跟着出去。

import { KIND_REQUEST, MAX_REQUEST_CHUNKS, TelemetryStorage, append } from './store'

/** 一个可录制的站点:id 只作标注,`domains` 是可注册域,子域一律算在内。 */
export interface CaptureSite {
  readonly id: string
  readonly domains: readonly string[]
}

/** 一轮录制的时长。 */
export const CAPTURE_DURATION_MS = 10 * 60_000

/** 录制状态在 chrome.storage.local 里的键。与其余观测键同前缀,避开 infra 与 witness:*。 */
export const CAPTURE_STATE_KEY = 'telemetry:capture:state'

/** 一轮录制的状态。SW 会被杀,所以它必须落盘,不能只在内存。 */
export interface CaptureState {
  readonly startedAt: number
  readonly until: number
  /** 到点或人工停止的时刻;缺席即仍在窗口内(是否真在录还要看 until)。 */
  readonly endedAt?: number
}

export function captureActive(state: CaptureState | null | undefined, now: number): boolean {
  return state !== null && state !== undefined && state.endedAt === undefined && now < state.until
}

/** 取主机名。`initiator` 是 origin 字符串而非 URL,同样能解;解不出(如 'null')返回 null。 */
export function hostOf(url: string | undefined | null): string | null {
  if (!url) return null
  try {
    const host = new URL(url).hostname.toLowerCase()
    return host === '' ? null : host
  } catch {
    return null
  }
}

/** 主机属于哪个站点;不属于任何一个返回 null。 */
export function siteOfHost(host: string | null, sites: readonly CaptureSite[]): CaptureSite | null {
  if (host === null) return null
  for (const site of sites) {
    for (const domain of site.domains) {
      if (host === domain || host.endsWith(`.${domain}`)) return site
    }
  }
  return null
}

/** `shouldRecord` 需要的最小事实面,和 webRequest 各阶段的 details 结构兼容。 */
export interface RequestFacts {
  readonly url: string
  readonly type: string
  readonly initiator?: string | undefined
  readonly tabId: number
}

/**
 * 能力上限本身。三条判据命中任一即记:
 *
 * 1. 发起方 origin 属于平台——页面、页面里的脚本、页面的 service worker 发的都算,
 *    目的地是 CDN、第三方还是本机端口一概不问。
 * 2. 主文档导航:initiator 常为空,只能看目的地是不是平台。此时所在标签页还是旧页,
 *    刻意不作数——否则在平台标签页的地址栏敲别的网址,那一跳会被记下来。
 * 3. 其余请求:所在标签页顶层是平台页即记。这一条接住平台页里第三方 iframe 发的请求,
 *    它们的 initiator 是第三方 origin,却是平台页面上发生的事。
 *
 * 插件自己连脑、其他扩展的请求,initiator 都是 chrome-extension 前缀,三条都不中。
 */
export function shouldRecord(
  facts: RequestFacts,
  tabHost: string | null,
  sites: readonly CaptureSite[],
): boolean {
  if (siteOfHost(hostOf(facts.initiator), sites) !== null) return true
  if (facts.type === 'main_frame') return siteOfHost(hostOf(facts.url), sites) !== null
  return siteOfHost(tabHost, sites) !== null
}

// ---- 头 ----

export interface RecordedHeader {
  readonly name: string
  readonly value: string
}

/** 被剥掉的值统一换成这个标记:名字留着,形状还看得见,值没了。 */
export const STRIPPED_VALUE = '[已剥离]'

/** 单个头值的保留上限。 */
export const HEADER_VALUE_KEEP_CHARS = 2048

const SENSITIVE_HEADER_NAMES: ReadonlySet<string> = new Set([
  'cookie', 'set-cookie', 'set-cookie2',
  'authorization', 'proxy-authorization', 'www-authenticate', 'proxy-authenticate',
])

/** 名字里带这些片段的一律当凭据:平台自定义头的命名我们不掌握,宁可多剥。 */
const SENSITIVE_HEADER_PARTS: readonly string[] = ['token', 'cookie', 'auth', 'secret', 'session', 'passw']

export function sensitiveHeader(name: string): boolean {
  const lower = name.toLowerCase()
  if (SENSITIVE_HEADER_NAMES.has(lower)) return true
  return SENSITIVE_HEADER_PARTS.some((part) => lower.includes(part))
}

export interface HeaderLike {
  readonly name: string
  readonly value?: string | undefined
  readonly binaryValue?: unknown
}

/** 剥敏感头、截长值。没有头就返回 undefined,不造空数组。 */
export function stripHeaders(headers: readonly HeaderLike[] | undefined | null): RecordedHeader[] | undefined {
  if (!headers) return undefined
  return headers.map((h) => {
    if (sensitiveHeader(h.name)) return { name: h.name, value: STRIPPED_VALUE }
    if (h.value === undefined) return { name: h.name, value: h.binaryValue === undefined ? '' : '[二进制]' }
    return { name: h.name, value: h.value.length > HEADER_VALUE_KEEP_CHARS ? h.value.slice(0, HEADER_VALUE_KEEP_CHARS) : h.value }
  })
}

// ---- 请求体 ----

/** 请求体文本保留上限(字符)。 */
export const BODY_KEEP_CHARS = 8192

/** 解码时最多读这么多字节:上传文件的 body 可以有几十 MB,不为截断去解整段。 */
const BODY_DECODE_BYTES = 65536

export interface BodyDigest {
  /** raw 形态的总字节数(截断前)。 */
  readonly bytes?: number
  readonly text?: string
  /** Chrome 已解码的表单(multipart / urlencoded)。 */
  readonly form?: Record<string, string[]>
  /** multipart 里的文件只记文件名。 */
  readonly files?: string[]
  readonly truncated?: true
  readonly error?: string
}

export interface RequestBodyLike {
  readonly error?: string | undefined
  readonly formData?: Record<string, string[]> | undefined
  readonly raw?: readonly { readonly bytes?: ArrayBuffer | undefined; readonly file?: string | undefined }[] | undefined
}

export function describeBody(body: RequestBodyLike | undefined | null): BodyDigest | undefined {
  if (!body) return undefined
  const out: { -readonly [K in keyof BodyDigest]: BodyDigest[K] } = {}
  if (body.error) out.error = body.error

  if (body.formData) {
    const form: Record<string, string[]> = {}
    for (const [key, values] of Object.entries(body.formData)) {
      form[key] = values.map((v) => {
        if (v.length <= BODY_KEEP_CHARS) return v
        out.truncated = true
        return v.slice(0, BODY_KEEP_CHARS)
      })
    }
    out.form = form
  }

  if (body.raw?.length) {
    const files: string[] = []
    const chunks: Uint8Array[] = []
    let bytes = 0
    let kept = 0
    for (const part of body.raw) {
      if (part.file !== undefined) files.push(part.file)
      if (!part.bytes) continue
      bytes += part.bytes.byteLength
      if (kept >= BODY_DECODE_BYTES) continue
      const take = Math.min(part.bytes.byteLength, BODY_DECODE_BYTES - kept)
      chunks.push(new Uint8Array(part.bytes, 0, take))
      kept += take
    }
    if (files.length) out.files = files
    if (bytes > 0) {
      out.bytes = bytes
      const joined = new Uint8Array(kept)
      let offset = 0
      for (const c of chunks) {
        joined.set(c, offset)
        offset += c.byteLength
      }
      const text = new TextDecoder('utf-8').decode(joined)
      out.text = text.length > BODY_KEEP_CHARS ? text.slice(0, BODY_KEEP_CHARS) : text
      if (text.length > BODY_KEEP_CHARS || kept < bytes) out.truncated = true
    }
  }

  return Object.keys(out).length ? out : undefined
}

// ---- 一条请求的记录 ----

/** 一条落盘的请求记录。四个阶段各填一部分;没走到的阶段字段缺席。 */
export interface RequestRecord {
  readonly requestId: string
  /** onBeforeRequest 时刻(重定向后取最后一跳)。 */
  at: number
  url: string
  method: string
  type: string
  initiator?: string
  tabId: number
  frameId: number
  /** 记录时所在标签页的顶层主机,给"这是哪一页发的"做证。 */
  tabHost?: string
  body?: BodyDigest
  /** 重定向链上之前各跳的 URL。 */
  hops?: string[]
  requestHeaders?: RecordedHeader[]
  sentAt?: number
  statusCode?: number
  statusLine?: string
  responseHeaders?: RecordedHeader[]
  headersAt?: number
  fromCache?: boolean
  ip?: string
  endedAt?: number
  /** onErrorOccurred 给的错误码,如 net::ERR_BLOCKED_BY_CLIENT。 */
  error?: string
  /** 录制结束时仍未收到 onCompleted / onErrorOccurred。 */
  unfinished?: true
}

export interface BeginDetails extends RequestFacts {
  readonly requestId: string
  readonly method: string
  readonly frameId: number
  readonly timeStamp: number
  readonly requestBody?: RequestBodyLike | null | undefined
}

export interface SendHeadersDetails {
  readonly requestId: string
  readonly timeStamp: number
  readonly requestHeaders?: readonly HeaderLike[] | undefined
}

export interface HeadersReceivedDetails {
  readonly requestId: string
  readonly timeStamp: number
  readonly statusCode: number
  readonly statusLine?: string | undefined
  readonly responseHeaders?: readonly HeaderLike[] | undefined
}

export interface CompletedDetails {
  readonly requestId: string
  readonly timeStamp: number
  readonly statusCode?: number | undefined
  readonly fromCache?: boolean | undefined
  readonly ip?: string | undefined
}

export interface ErrorDetails extends CompletedDetails {
  readonly error: string
}

export interface CaptureSessionOptions {
  readonly store: TelemetryStorage
  /** 缓冲攒到这么多条就落盘。 */
  readonly flushAtCount?: number
  /** 缓冲非空后最迟这么久落盘。 */
  readonly flushAfterMs?: number
  /** 定时器注入点,测试用;生产是 setTimeout。 */
  readonly schedule?: (fn: () => void, ms: number) => void
  readonly onError?: (error: unknown) => void
}

/**
 * 四事件合成 + 缓冲落盘。
 *
 * 为什么缓冲:每条请求直接 append 是一次「读当前片 → 追加 → 写回」,十分钟几千条
 * 就是几千次读改写。攒一秒或一百条再写,写盘次数降两个量级。代价是 SW 被杀时丢掉
 * 缓冲里的那不到一秒——但有事件在流 SW 就不会被杀,真空闲时缓冲早已写空。
 *
 * 为什么 pending 不落盘:请求进行中的记录只在内存;SW 若在长连接进行中被杀,那几条
 * 在途请求就丢了。接受:窗口内平台页面一直有事件,SW 不会闲到被杀。
 */
export class CaptureSession {
  private readonly pending = new Map<string, RequestRecord>()
  private buffer: RequestRecord[] = []
  private timerArmed = false
  private queue: Promise<void> = Promise.resolve()
  private written = 0
  private readonly store: TelemetryStorage
  private readonly flushAtCount: number
  private readonly flushAfterMs: number
  private readonly schedule: (fn: () => void, ms: number) => void
  private readonly onError: (error: unknown) => void

  constructor(options: CaptureSessionOptions) {
    this.store = options.store
    this.flushAtCount = options.flushAtCount ?? 100
    this.flushAfterMs = options.flushAfterMs ?? 1000
    this.schedule = options.schedule ?? ((fn, ms) => { setTimeout(fn, ms) })
    this.onError = options.onError ?? (() => {})
  }

  /** onBeforeRequest。同 requestId 再来一次是重定向后的下一跳,不开新记录。 */
  begin(details: BeginDetails, tabHost: string | null): void {
    const previous = this.pending.get(details.requestId)
    if (previous) {
      previous.hops ??= []
      previous.hops.push(previous.url)
      previous.url = details.url
      previous.at = details.timeStamp
      return
    }
    const record: RequestRecord = {
      requestId: details.requestId,
      at: details.timeStamp,
      url: details.url,
      method: details.method,
      type: details.type,
      tabId: details.tabId,
      frameId: details.frameId,
    }
    if (details.initiator !== undefined) record.initiator = details.initiator
    if (tabHost !== null) record.tabHost = tabHost
    const body = describeBody(details.requestBody)
    if (body) record.body = body
    this.pending.set(details.requestId, record)
  }

  sendHeaders(details: SendHeadersDetails): void {
    const record = this.pending.get(details.requestId)
    if (!record) return
    record.sentAt = details.timeStamp
    const headers = stripHeaders(details.requestHeaders)
    if (headers) record.requestHeaders = headers
  }

  headersReceived(details: HeadersReceivedDetails): void {
    const record = this.pending.get(details.requestId)
    if (!record) return
    record.headersAt = details.timeStamp
    record.statusCode = details.statusCode
    if (details.statusLine !== undefined) record.statusLine = details.statusLine
    const headers = stripHeaders(details.responseHeaders)
    if (headers) record.responseHeaders = headers
  }

  completed(details: CompletedDetails): void {
    const record = this.pending.get(details.requestId)
    if (!record) return
    this.fillEnd(record, details)
    this.finish(record)
  }

  errored(details: ErrorDetails): void {
    const record = this.pending.get(details.requestId)
    if (!record) return
    this.fillEnd(record, details)
    record.error = details.error
    this.finish(record)
  }

  private fillEnd(record: RequestRecord, details: CompletedDetails): void {
    record.endedAt = details.timeStamp
    if (details.statusCode !== undefined && details.statusCode !== 0) record.statusCode = details.statusCode
    if (details.fromCache !== undefined) record.fromCache = details.fromCache
    if (details.ip !== undefined) record.ip = details.ip
  }

  private finish(record: RequestRecord): void {
    this.pending.delete(record.requestId)
    this.buffer.push(record)
    if (this.buffer.length >= this.flushAtCount) {
      void this.flush()
      return
    }
    if (this.timerArmed) return
    this.timerArmed = true
    this.schedule(() => {
      this.timerArmed = false
      void this.flush()
    }, this.flushAfterMs)
  }

  /** 把缓冲写进分片环。写是串行的:多次 flush 交错到达也只会一片接一片地写。 */
  flush(): Promise<void> {
    if (!this.buffer.length) return this.queue
    const batch = this.buffer
    this.buffer = []
    this.queue = this.queue
      .then(() => append(this.store, KIND_REQUEST, batch, MAX_REQUEST_CHUNKS))
      .then(() => { this.written += batch.length })
      .catch(this.onError)
    return this.queue
  }

  /** 等已经排队的写全部落地。不动缓冲——测试与收尾用。 */
  settled(): Promise<void> {
    return this.queue
  }

  /** 录制结束:在途请求按 unfinished 落盘,不丢。 */
  async end(): Promise<void> {
    for (const record of this.pending.values()) {
      record.unfinished = true
      this.buffer.push(record)
    }
    this.pending.clear()
    await this.flush()
  }

  /** 新一轮开始前清掉上一轮的内存残留。 */
  reset(): void {
    this.pending.clear()
    this.buffer = []
    this.written = 0
  }

  pendingCount(): number {
    return this.pending.size
  }

  /** 本 SW 生命周期内已落盘的条数。 */
  writtenCount(): number {
    return this.written
  }
}
