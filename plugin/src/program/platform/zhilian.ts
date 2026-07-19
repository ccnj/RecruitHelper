// 智联平台实现接缝。这里可以知道 host、路由、页面字段与 NIM 消息形态；
// base、脑端与协议只看平台无关的原语结果。

import type { PrimitiveContext } from '../registry'
import { beginCommandNavigation } from '../../base/navigation'
import type {
  ChatReadListArgs,
  ChatReadListData,
  ChatReadThreadArgs,
  ChatReadThreadData,
  ConversationSummary,
  ErrorCode,
  MessageAnchor,
  NotReadyReason,
  ProbePlatformData,
  Retryable,
  SideEffect,
  ThreadMessage,
} from '../../base/protocol'

export const ZHILIAN_PLATFORM = 'zhilian'
export const ZHILIAN_HOST = 'rd6.zhaopin.com'
export const ZHILIAN_IM_URL = `https://${ZHILIAN_HOST}/app/im`

const TAB_QUERY = `https://${ZHILIAN_HOST}/*`
// 每个平台页只取 8 条，保证单页即便字段接近 schema 上限也不会顶穿 64KiB data 门禁。
const LIST_PAGE_SIZE = 8
const RESULT_DATA_BUDGET = 60 * 1024

export class ZhilianPlatformError extends Error {
  constructor(
    readonly code: ErrorCode,
    message: string,
    readonly retryable: Retryable = 'afterRecovery',
    readonly reason?: NotReadyReason,
    readonly sideEffect: SideEffect = 'none',
  ) {
    super(message)
    this.name = 'ZhilianPlatformError'
  }
}

// 平台实现只给生成类型起本地别名，不再手写一份协议 DTO。
export type ZhilianProbe = ProbePlatformData
export type ZhilianListArgs = ChatReadListArgs
export type ZhilianConversationSummary = ConversationSummary
export type ZhilianListPage = ChatReadListData
export type ZhilianMessageAnchor = MessageAnchor
export type ZhilianThreadArgs = ChatReadThreadArgs
export type ZhilianThreadMessage = ThreadMessage
export type ZhilianThreadPage = ChatReadThreadData

interface MainProbeResult {
  pageKind: 'im' | 'recommend' | 'other'
  loginState: 'in' | 'out' | 'unknown'
  principalFingerprint: string | null
  imListVisible: boolean
}

interface MainListPageResult {
  sessions: ZhilianConversationSummary[]
  hasMore: boolean
  unstable: boolean
}

interface MainListDOMWindowResult {
  sessions: ZhilianConversationSummary[]
  atBottom: boolean
  moved: boolean
  scrollHeight: number
  scrollTop: number
  unstable: boolean
}

interface MainThreadPageResult {
  messages: Array<Omit<ZhilianThreadMessage, 'idx'> & { sourceKey: string }>
  reachedTop: boolean
  cursor: { endTime: number; lastMsgId: string } | null
  peer: { displayName: string; platformUserRef?: string } | null
}

interface ListCursor {
  v: 1
  kind: 'list'
  mode: 'api' | 'dom'
  binding: string
  pageNo: number
  offset: number
  pageDigest: string
}

interface ThreadCursor {
  v: 1
  kind: 'thread'
  mode: 'api'
  binding: string
  endTime: number
  lastMsgId: string
  // 位 s 表示“已返回的较新聚合前缀匹配 anchorTail[s:]”。脑端下一页
  // 会前插，较旧页借此识别横跨 protocol page 的完整锚点。
  ap?: number
}

const SHA256_HEX = /^[0-9a-f]{64}$/u

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error))
}

function jsonBytes(value: unknown): number {
  return new TextEncoder().encode(JSON.stringify(value)).length
}

function encodeCursor(value: ListCursor | ThreadCursor): string {
  const bytes = new TextEncoder().encode(JSON.stringify(value))
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary).replace(/\+/gu, '-').replace(/\//gu, '_').replace(/=+$/u, '')
}

async function sha256Hex(value: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value))
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('')
}

async function bindingHash(value: unknown): Promise<string> {
  return sha256Hex(JSON.stringify(value))
}

function decodeCursor(value: string | null | undefined): ListCursor | ThreadCursor | null {
  if (!value) return null
  try {
    const base64 = value.replace(/-/gu, '+').replace(/_/gu, '/')
    const padded = base64 + '='.repeat((4 - (base64.length % 4)) % 4)
    const binary = atob(padded)
    const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0))
    const parsed = JSON.parse(new TextDecoder().decode(bytes)) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed) ||
        (parsed as { v?: unknown }).v !== 1) {
      throw new Error('cursor version or shape invalid')
    }
    return parsed as ListCursor | ThreadCursor
  } catch {
    throw new ZhilianPlatformError('CURSOR_INVALID', '分页游标无效')
  }
}

function sameThreadPosition(
  left: { endTime: number; lastMsgId: string } | null,
  right: { endTime: number; lastMsgId: string } | null,
): boolean {
  return left !== null && right !== null &&
    left.endTime === right.endTime && left.lastMsgId === right.lastMsgId
}

async function runMain<A extends unknown[], R>(
  tabId: number,
  func: (...args: A) => Promise<R>,
  args: A,
): Promise<R> {
  const result = await chrome.scripting.executeScript({
    target: { tabId },
    world: 'MAIN',
    func,
    args,
  })
  const first = result[0] as unknown as { result?: R | null; error?: unknown } | undefined
  if (!first) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联页面脚本尚未就绪', 'afterRecovery', 'contentScriptDead')
  }
  if (first.error !== undefined && first.error !== null) {
    let detail = ''
    if (typeof first.error === 'string') {
      detail = first.error.trim()
    } else if (typeof first.error === 'object') {
      try {
        const message = (first.error as { message?: unknown }).message
        if (typeof message === 'string') detail = message.trim()
      } catch {
        // Chrome 的 InjectionResult.error 形态尚未在所有版本稳定；不读取其他字段。
      }
    }
    const suffix = detail ? `：${detail.slice(0, 300)}` : ''
    throw new ZhilianPlatformError(
      'CTX_NOT_READY',
      `智联页面脚本执行失败${suffix}`,
      'afterRecovery',
      'contentScriptDead',
    )
  }
  if (first.result === undefined || first.result === null) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联页面脚本未返回结果', 'afterRecovery', 'contentScriptDead')
  }
  return first.result
}

function pageKindFromURL(url: string | undefined): MainProbeResult['pageKind'] {
  if (!url) return 'other'
  try {
    const path = new URL(url).pathname
    if (path === '/app/im' || path.startsWith('/app/im/')) return 'im'
    if (path.startsWith('/app/recommend')) return 'recommend'
  } catch {
    // URL 来自 chrome.tabs，解析失败即按 other 响亮降级，不猜页面。
  }
  return 'other'
}

export async function canonicalZhilianTab(): Promise<chrome.tabs.Tab | null> {
  const tabs = await chrome.tabs.query({ url: TAB_QUERY })
  const candidates = tabs.filter((tab) => tab.id !== undefined)
  const healthPairs = await Promise.all(candidates.map(async (tab) => [
    tab.id as number,
    await contentScriptHealthy(tab.id as number),
  ] as const))
  const healthy = new Map<number, boolean>(healthPairs)
  candidates.sort((a, b) => {
    const tier = (tab: chrome.tabs.Tab): number => {
      const script = healthy.get(tab.id as number) === true
      const im = pageKindFromURL(tab.url) === 'im'
      if (script && im) return 3
      if (script) return 2
      if (im) return 1
      return 0
    }
    const aTier = tier(a)
    const bTier = tier(b)
    if (aTier !== bTier) return bTier - aTier
    const aIM = pageKindFromURL(a.url) === 'im' ? 1 : 0
    const bIM = pageKindFromURL(b.url) === 'im' ? 1 : 0
    if (aIM !== bIM) return bIM - aIM
    const aActive = a.active ? 1 : 0
    const bActive = b.active ? 1 : 0
    if (aActive !== bActive) return bActive - aActive
    const aSeen = a.lastAccessed ?? 0
    const bSeen = b.lastAccessed ?? 0
    if (aSeen !== bSeen) return bSeen - aSeen
    return (a.id ?? Number.MAX_SAFE_INTEGER) - (b.id ?? Number.MAX_SAFE_INTEGER)
  })
  return candidates[0] ?? null
}

async function contentScriptHealthy(tabId: number): Promise<boolean> {
  try {
    const response = await chrome.tabs.sendMessage(tabId, { type: 'recruithelper.content.probe' }) as unknown
    return typeof response === 'object' && response !== null && (response as { ok?: unknown }).ok === true
  } catch {
    return false
  }
}

// 必须是自包含函数：chrome.scripting.executeScript 会把它序列化到 MAIN world，
// 不得引用模块闭包。原始账号字段只在页面内参与哈希，绝不跨出页面。
async function mainProbeZhilian(): Promise<MainProbeResult> {
  type AnyRecord = Record<string, unknown>
  const w = window as unknown as AnyRecord
  const path = location.pathname
  const pageKind: MainProbeResult['pageKind'] =
    path === '/app/im' || path.startsWith('/app/im/')
      ? 'im'
      : path.startsWith('/app/recommend')
        ? 'recommend'
        : 'other'

  const runtimeSession = w.$session as AnyRecord | undefined
  let initialSession: AnyRecord | undefined
  let personal: AnyRecord | undefined
  const source = Array.from(document.scripts)
    .map((script) => script.textContent ?? '')
    .find((text) => text.includes('__INITIAL_STATE__='))
  if (source) {
    const marker = '__INITIAL_STATE__='
    const start = source.indexOf(marker) + marker.length
    const candidate = source.slice(start).trim()
    try {
      let jsonText = candidate.replace(/;$/u, '')
      if (!jsonText.startsWith('{') || !jsonText.endsWith('}')) {
        const objectStart = candidate.indexOf('{')
        let depth = 0
        let quoted = false
        let escaped = false
        let objectEnd = -1
        for (let index = objectStart; index >= 0 && index < candidate.length; index += 1) {
          const char = candidate[index]
          if (quoted) {
            if (escaped) escaped = false
            else if (char === '\\') escaped = true
            else if (char === '"') quoted = false
            continue
          }
          if (char === '"') quoted = true
          else if (char === '{') depth += 1
          else if (char === '}') {
            depth -= 1
            if (depth === 0) {
              objectEnd = index + 1
              break
            }
          }
        }
        if (objectStart < 0 || objectEnd < 0) throw new Error('initial state boundary absent')
        jsonText = candidate.slice(objectStart, objectEnd)
      }
      const initial = JSON.parse(jsonText) as AnyRecord
      const sessionModule = initial.session as AnyRecord | undefined
      initialSession = sessionModule?.session as AnyRecord | undefined
      personal = initial.personal as AnyRecord | undefined
    } catch {
      // SSR 注入格式变化时不冒险解析；loginState=unknown 会让脑暂停而不是误绑定。
    }
  }

  const session: AnyRecord = { ...(initialSession ?? {}), ...(runtimeSession ?? {}) }
  const staff = {
    ...((initialSession?.staff as AnyRecord | undefined) ?? {}),
    ...((runtimeSession?.staff as AnyRecord | undefined) ?? {}),
  }
  const imUserInfo = personal?.imUserInfo as AnyRecord | undefined
  const isLoggedIn = session?.isLoggedIn
  const org = session.org as AnyRecord | undefined
  const normalizeIdentityPart = (value: unknown): string | null => {
    if (typeof value === 'string') {
      const normalized = value.trim()
      return normalized.length > 0 ? normalized : null
    }
    if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
    return null
  }
  const staffID = normalizeIdentityPart(staff?.staffId)
  const sessionOrgID = normalizeIdentityPart(org?.orgId)
  const legacyRootCompanyID = normalizeIdentityPart(imUserInfo?.rootCompanyId)
  const organizationID = sessionOrgID ?? legacyRootCompanyID
  const loginPoint = normalizeIdentityPart(staff.defaultLoginPoint)

  let loginState: MainProbeResult['loginState'] = 'unknown'
  if (isLoggedIn === false) loginState = 'out'
  else if (isLoggedIn === true && staffID !== null) loginState = 'in'

  let principalFingerprint: string | null = null
  if (loginState === 'in' && staffID !== null && organizationID !== null && loginPoint !== null) {
    const pieces = ['zhilian-principal-v2', staffID, organizationID, loginPoint]
    const canonical = pieces.map((piece) => `${new TextEncoder().encode(piece).length}:${piece}`).join('|')
    const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(canonical))
    principalFingerprint = Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('')
  }

  return {
    pageKind,
    loginState,
    principalFingerprint,
    imListVisible: document.querySelector('.im-session-list') !== null,
  }
}

async function probeTab(tab: chrome.tabs.Tab): Promise<ZhilianProbe> {
  if (tab.id === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', 'canonical 标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  let main: MainProbeResult
  try {
    main = await runMain(tab.id, mainProbeZhilian, [])
  } catch (error) {
    return {
      pageKind: pageKindFromURL(tab.url),
      contentScriptOk: false,
      loginState: 'unknown',
      principalFingerprint: null,
      surface: null,
    }
  }
  const contentScriptOk = await contentScriptHealthy(tab.id)
  return {
    pageKind: main.pageKind,
    contentScriptOk,
    loginState: main.loginState,
    principalFingerprint: main.principalFingerprint,
    surface: main.pageKind === 'im' ? { imListVisible: main.imListVisible } : null,
  }
}

export async function probeZhilian(): Promise<ZhilianProbe> {
  const tab = await canonicalZhilianTab()
  if (!tab) {
    return {
      pageKind: 'none',
      contentScriptOk: false,
      loginState: 'unknown',
      principalFingerprint: null,
      surface: null,
    }
  }
  return probeTab(tab)
}

function assertExpectedPrincipal(probe: ZhilianProbe, expected: string | undefined): void {
  if (!expected) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  if (probe.loginState === 'out') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联账号已退出登录', 'manualOnly', 'loginRequired')
  }
  if (!probe.principalFingerprint) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '当前无法确证智联登录身份', 'afterRecovery', 'identityUnverified')
  }
  if (probe.principalFingerprint !== expected) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '当前智联登录账号与脑侧绑定不一致', 'manualOnly')
  }
}

async function waitForIMReady(tab: chrome.tabs.Tab, ctx: PrimitiveContext): Promise<ZhilianProbe> {
  if (tab.id === undefined) throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  for (let attempt = 0; attempt < 80; attempt += 1) {
    ctx.checkpoint()
    const latest = await chrome.tabs.get(tab.id)
    if (latest.status === 'complete' && pageKindFromURL(latest.url) === 'im') {
      const probe = await probeTab(latest)
      if (probe.loginState === 'out') return probe
      if (probe.contentScriptOk && probe.surface?.imListVisible) return probe
    }
    if (attempt % 8 === 0) await ctx.progress('等待智联 IM 页面就绪', Math.min(95, 15 + attempt))
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  throw new ZhilianPlatformError('CTX_NOT_READY', '智联 IM 页面在期限内未就绪', 'afterRecovery', 'pageBroken')
}

export async function ensureZhilianIM(
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<{ ready: boolean; loginState: 'in' | 'out' | 'unknown'; createdTab: boolean }> {
  ctx.checkpoint()
  await ctx.progress('选择智联 canonical 标签页', 5)
  let tab = await canonicalZhilianTab()
  let createdTab = false
  if (!tab) {
    tab = await chrome.tabs.create({ url: ZHILIAN_IM_URL, active: false })
    createdTab = true
  } else if (pageKindFromURL(tab.url) !== 'im') {
    if (tab.id === undefined) throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
    const commandNavigation = beginCommandNavigation(tab.id)
    try {
      tab = await chrome.tabs.update(tab.id, { url: ZHILIAN_IM_URL })
    } catch (error) {
      // update 未产生导航时不会有 webNavigation 来消费窗口，必须显式撤销。
      commandNavigation.end()
      throw error
    }
  } else {
    const existingProbe = await probeTab(tab)
    if (!existingProbe.contentScriptOk) {
      if (tab.id === undefined) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
      }
      const commandNavigation = beginCommandNavigation(tab.id)
      try {
        await chrome.tabs.reload(tab.id)
        tab = await chrome.tabs.get(tab.id)
      } catch (error) {
        commandNavigation.end()
        throw error
      }
    }
  }

  const probe = await waitForIMReady(tab, ctx)
  if (probe.loginState === 'out') {
    return { ready: false, loginState: 'out', createdTab }
  }
  // nav.ensureSurface 是 pageAbsent 恢复例外；身份一旦可观测，返回前仍必须复核。
  assertExpectedPrincipal(probe, expectedPrincipalFingerprint)
  await ctx.progress('智联 IM 页面已就绪', 100)
  return {
    ready: probe.contentScriptOk && probe.surface?.imListVisible === true,
    loginState: probe.loginState,
    createdTab,
  }
}

// 自包含 MAIN-world 列表读取。两次请求不是失败重试，而是 unread/排序一致性采样；
// 任一差异交由脑下轮重算，手不在本命令内继续猜。
async function mainReadListPage(
  pageNo: number,
  pageSize: number,
  filter: 'all' | 'unread',
): Promise<MainListPageResult> {
  type AnyRecord = Record<string, unknown>
  const w = window as unknown as AnyRecord
  const engine = w.imEngine as AnyRecord | undefined
  const getSessions = engine?.getSessions
  if (typeof getSessions !== 'function') throw new Error('imEngine.getSessions unavailable')

  const payload: AnyRecord = { pageNo, pageSize, includeResume: true }
  if (filter === 'unread') payload.filterUnread = true
  const first = await (getSessions as (arg: AnyRecord) => Promise<AnyRecord>).call(engine, { ...payload })
  await new Promise((resolve) => setTimeout(resolve, 120))
  const second = await (getSessions as (arg: AnyRecord) => Promise<AnyRecord>).call(engine, { ...payload })
  if (!first || !second || !Array.isArray(first.curSessions) || !Array.isArray(second.curSessions)) {
    throw new Error('imEngine.getSessions response shape invalid')
  }
  const hasMoreOf = (value: AnyRecord): boolean => {
    if (typeof value.hasMoreSession === 'boolean') return value.hasMoreSession
    if (typeof value.hasMore === 'boolean') return value.hasMore
    throw new Error('imEngine.getSessions hasMore missing')
  }
  const firstHasMore = hasMoreOf(first)
  const secondHasMore = hasMoreOf(second)
  const firstRows = first.curSessions as AnyRecord[]
  const secondRows = second.curSessions as AnyRecord[]
  const firstByID = new Map(firstRows.map((row) => [String(row.sessionId ?? ''), row]))
  const stableProjection = (row: AnyRecord): unknown[] => [
    row.sessionId, row.peerPartnerId, row.unreadCount, row.isUnRead,
    row.lastSentence, row.sortTime, row.modifiedTime,
  ]
  let unstable = firstHasMore !== secondHasMore ||
    JSON.stringify(firstRows.map(stableProjection)) !== JSON.stringify(secondRows.map(stableProjection))

  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/\u00a0/gu, ' ')
    .replace(/\s+/gu, ' ')
    .trim()
  const clampPreview = (value: string): string => Array.from(value).slice(0, 200).join('')
  const toMillis = (value: unknown): number | null => {
    const number = Number(value)
    if (!Number.isFinite(number) || number <= 0) return null
    return number < 1_000_000_000_000 ? Math.trunc(number * 1000) : Math.trunc(number)
  }

  const sessions: ZhilianConversationSummary[] = []
  for (const row of secondRows) {
    const conversationRef = clean(row.sessionId)
    if (!conversationRef) throw new Error('session identity missing')
    const unreadRaw = Number(row.unreadCount)
    if (!Number.isInteger(unreadRaw) || unreadRaw < 0) throw new Error('session unreadCount invalid')
    const prior = firstByID.get(conversationRef)
    if (!prior || Number(prior.unreadCount) !== unreadRaw) unstable = true
    if (filter === 'unread' && unreadRaw <= 0) continue
    let last: AnyRecord = {}
    try {
      last = typeof row.lastSentence === 'string' ? JSON.parse(row.lastSentence) as AnyRecord : (row.lastSentence as AnyRecord) ?? {}
    } catch {
      unstable = true
    }
    const senderType = clean(last.senderType).toUpperCase()
    const direction: 'in' | 'out' | 'system' =
      senderType === 'STAFF' ? 'out' : senderType === 'USER' ? 'in' : 'system'
    const displayName = clean(row.name ?? row.realName) || '未命名候选人'
    const platformUserRef = clean(row.peerPartnerId ?? row.typeUserId ?? row.userId)
    const textPreview = clampPreview(clean(last.text) || clean(last.title))
    sessions.push({
      conversationRef,
      peer: platformUserRef ? { displayName, platformUserRef } : { displayName },
      unreadCount: unreadRaw,
      lastMessage: {
        direction,
        kind: direction === 'system' ? 'system' : 'text',
        textPreview,
      },
      lastActivityTs: toMillis(row.sortTime ?? last.sendTime ?? row.modifiedTime),
    })
  }
  return {
    sessions,
    hasMore: secondHasMore,
    unstable,
  }
}

// API 不可用时的保守虚拟列表兜底。顶部先用当前真机已验证的启动状态；滚动后的
// 动态窗只接受带稳定 sessionId/peerPartnerId 的 MAIN-world Vue source，否则响亮失败。
// 滚到底本身不证明平台 hasMore=false，外层只在跨过时间 cutoff 时宣告 complete。
async function mainReadListDOMWindow(
  advance: boolean,
  resetToTop: boolean,
): Promise<MainListDOMWindowResult> {
  type AnyRecord = Record<string, unknown>
  const virtual = document.querySelector<HTMLElement>('.im-session-list .im-session-list__virtual')
  if (!virtual) throw new Error('dom_list_virtual_missing')
  const itemSelector =
    '.im-session-list .im-session-list__virtual .im-session-list__virtual--box div[role="listitem"]'

  const scrollCandidates = [
    virtual,
    ...Array.from(virtual.querySelectorAll<HTMLElement>('.km-scrollbar__wrap, .km-scrollbar__view')),
    virtual.parentElement,
  ].filter((item): item is HTMLElement => item !== null)
  const scrollElement = scrollCandidates.sort((left, right) =>
    (right.scrollHeight - right.clientHeight) - (left.scrollHeight - left.clientHeight))[0]
  if (!scrollElement || scrollElement.clientHeight <= 0) throw new Error('dom_list_scroll_surface_missing')

  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/\u00a0/gu, ' ')
    .replace(/\s+/gu, ' ')
    .trim()
  const clampPreview = (value: string): string => Array.from(value).slice(0, 200).join('')
  const toMillis = (value: unknown): number | null => {
    const number = Number(value)
    if (!Number.isFinite(number) || number <= 0) return null
    return number < 1_000_000_000_000 ? Math.trunc(number * 1000) : Math.trunc(number)
  }
  const parseObject = (value: unknown): AnyRecord => {
    if (value && typeof value === 'object' && !Array.isArray(value)) return value as AnyRecord
    if (typeof value !== 'string' || value.length === 0) throw new Error('dom_list_last_sentence_missing')
    const parsed = JSON.parse(value) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      throw new Error('dom_list_last_sentence_invalid')
    }
    return parsed as AnyRecord
  }
  const initialSessions = (): AnyRecord[] => {
    const source = Array.from(document.scripts)
      .map((script) => script.textContent ?? '')
      .find((text) => text.includes('__INITIAL_STATE__='))
    if (!source) return []
    const marker = '__INITIAL_STATE__='
    const candidate = source.slice(source.indexOf(marker) + marker.length).trim()
    const objectStart = candidate.indexOf('{')
    let depth = 0
    let quoted = false
    let escaped = false
    let objectEnd = -1
    for (let index = objectStart; index >= 0 && index < candidate.length; index += 1) {
      const char = candidate[index]
      if (quoted) {
        if (escaped) escaped = false
        else if (char === '\\') escaped = true
        else if (char === '"') quoted = false
        continue
      }
      if (char === '"') quoted = true
      else if (char === '{') depth += 1
      else if (char === '}') {
        depth -= 1
        if (depth === 0) {
          objectEnd = index + 1
          break
        }
      }
    }
    if (objectStart < 0 || objectEnd < 0) throw new Error('dom_list_initial_state_boundary_invalid')
    const initial = JSON.parse(candidate.slice(objectStart, objectEnd)) as AnyRecord
    const im = initial.im as AnyRecord | undefined
    return Array.isArray(im?.sessions) ? im.sessions as AnyRecord[] : []
  }
  const startupSessions = initialSessions()
  const vueSource = (node: HTMLElement): AnyRecord => {
    const candidates = [node, ...Array.from(node.querySelectorAll<HTMLElement>('*'))]
    for (const candidate of candidates) {
      const vue = (candidate as HTMLElement & { __vue__?: AnyRecord }).__vue__
      const props = vue?._props as AnyRecord | undefined
      const source = props?.source
      if (source && typeof source === 'object' && !Array.isArray(source) &&
          clean((source as AnyRecord).sessionId)) {
        return source as AnyRecord
      }
    }
    throw new Error('dom_list_vue_source_unavailable')
  }
  const collect = (): {
    sessions: ZhilianConversationSummary[]
    scrollTop: number
    scrollHeight: number
    clientHeight: number
    atBottom: boolean
  } => {
    const scrollTop = scrollElement.scrollTop
    const useStartupState = scrollTop <= 2 && startupSessions.length > 0
    const nodes = useStartupState
      ? []
      : Array.from(document.querySelectorAll<HTMLElement>(itemSelector))
          .filter((node) => node.querySelector('.im-session-item__box, .im-session-item') !== null)
    if (!useStartupState && nodes.length === 0) throw new Error('dom_list_items_missing')
    const sourcePairs: Array<{ source: AnyRecord; node: HTMLElement | null }> = useStartupState
      ? startupSessions.map((source) => ({ source, node: null }))
      : nodes.map((node) => ({ source: vueSource(node), node }))
    const seen = new Set<string>()
    const sessions: ZhilianConversationSummary[] = []
    for (const { source, node } of sourcePairs) {
      const conversationRef = clean(source.sessionId)
      const platformUserRef = clean(source.peerPartnerId)
      if (!conversationRef || !platformUserRef || seen.has(conversationRef)) {
        throw new Error('dom_list_identity_invalid')
      }
      seen.add(conversationRef)
      const unreadCount = Number(source.unreadCount)
      if (!Number.isInteger(unreadCount) || unreadCount < 0) throw new Error('dom_list_unread_invalid')
      const last = parseObject(source.lastSentence)
      const senderType = clean(last.senderType).toUpperCase()
      const direction: 'in' | 'out' | 'system' =
        senderType === 'STAFF' || senderType === 'SALES'
          ? 'out'
          : senderType === 'USER'
            ? 'in'
            : 'system'
      const displayName = clean(source.name ?? source.realName) ||
        clean(node?.querySelector('.im-session-item__name-title')?.textContent) || '未命名候选人'
      sessions.push({
        conversationRef,
        peer: { displayName, platformUserRef },
        unreadCount,
        lastMessage: {
          direction,
          kind: direction === 'system' ? 'system' : 'text',
          textPreview: clampPreview(clean(last.text) || clean(last.title)),
        },
        lastActivityTs: toMillis(source.sortTime ?? last.sendTime ?? source.modifiedTime),
      })
    }
    const orderedTimes = sessions.map((session) => session.lastActivityTs).filter((value): value is number => value !== null)
    for (let index = 1; index < orderedTimes.length; index += 1) {
      if (orderedTimes[index] > orderedTimes[index - 1]) throw new Error('dom_list_sort_order_invalid')
    }
    const scrollHeight = scrollElement.scrollHeight
    const clientHeight = scrollElement.clientHeight
    return {
      sessions,
      scrollTop,
      scrollHeight,
      clientHeight,
      atBottom: scrollTop + clientHeight >= scrollHeight - 2,
    }
  }
  const projection = (snapshot: ReturnType<typeof collect>): string => JSON.stringify({
    sessions: snapshot.sessions,
    scrollTop: Math.round(snapshot.scrollTop),
    scrollHeight: snapshot.scrollHeight,
    clientHeight: snapshot.clientHeight,
    atBottom: snapshot.atBottom,
  })

  if (resetToTop) {
    scrollElement.scrollTop = 0
    scrollElement.dispatchEvent(new Event('scroll', { bubbles: true }))
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  const before = collect()
  if (advance) {
    const maxTop = Math.max(0, before.scrollHeight - before.clientHeight)
    const step = Math.max(Math.floor(before.clientHeight * 0.8), 8 * 72)
    scrollElement.scrollTop = Math.min(maxTop, before.scrollTop + step)
    scrollElement.dispatchEvent(new Event('scroll', { bubbles: true }))
  }

  await new Promise((resolve) => setTimeout(resolve, advance ? 500 : 150))
  let latest = collect()
  let stableRounds = 0
  for (let attempt = 0; attempt < 6 && stableRounds < 2; attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 250))
    const next = collect()
    stableRounds = projection(next) === projection(latest) ? stableRounds + 1 : 0
    latest = next
  }
  const beforeRefs = before.sessions.map((session) => session.conversationRef).join('|')
  const afterRefs = latest.sessions.map((session) => session.conversationRef).join('|')
  return {
    sessions: latest.sessions,
    atBottom: latest.atBottom,
    moved: !advance || latest.scrollTop > before.scrollTop + 1 || afterRefs !== beforeRefs,
    scrollHeight: latest.scrollHeight,
    scrollTop: latest.scrollTop,
    unstable: stableRounds < 2,
  }
}

async function verifiedIMTab(expected: string | undefined): Promise<chrome.tabs.Tab> {
  const tab = await canonicalZhilianTab()
  if (!tab || pageKindFromURL(tab.url) !== 'im') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联 IM 页面不存在', 'afterRecovery', 'pageAbsent')
  }
  const probe = await probeTab(tab)
  if (!probe.contentScriptOk || !probe.surface?.imListVisible) {
    throw new ZhilianPlatformError(
      'CTX_NOT_READY',
      '智联 content script 或 IM 列表未就绪',
      'afterRecovery',
      probe.contentScriptOk ? 'pageBroken' : 'contentScriptDead',
    )
  }
  assertExpectedPrincipal(probe, expected)
  return tab
}

async function readZhilianListFromDOM(
  args: ZhilianListArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
  tab: chrome.tabs.Tab,
  binding: string,
  cursor: ListCursor | null,
  cutoffMs: number,
  maxSessions: number,
): Promise<ZhilianListPage> {
  if (tab.id === undefined) throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  if (cursor && cursor.mode !== 'dom') {
    throw new ZhilianPlatformError('CURSOR_INVALID', 'API 列表游标不能切换到 DOM 兜底')
  }
  let windowNo = cursor?.pageNo ?? 1
  let windowOffset = cursor?.offset ?? 0
  let expectedWindowDigest = cursor?.pageDigest ?? null
  let resumeCursor: ListCursor | null = cursor
  let previousWindowDigest: string | null = null
  let advance = false
  let resetToTop = cursor === null
  let complete = false
  const sessions: ZhilianConversationSummary[] = []
  const seen = new Set<string>()

  while (sessions.length < maxSessions) {
    if (windowNo > 100_000) throw new ZhilianPlatformError('CURSOR_INVALID', 'DOM 列表分页超过安全窗数')
    ctx.checkpoint()
    await ctx.progress(`DOM 兜底读取智联会话列表第 ${windowNo} 窗`, Math.min(95, 5 + sessions.length))
    let page: MainListDOMWindowResult
    try {
      page = await runMain(tab.id, mainReadListDOMWindow, [advance, resetToTop])
    } catch (error) {
      throw new ZhilianPlatformError(
        'CTX_LOST_DURING_EXEC',
        `智联会话列表 DOM 兜底失败：${asError(error).message}`,
      )
    }
    if (page.unstable) {
      throw new ZhilianPlatformError('USER_ACTIVE', 'DOM 虚拟列表未稳定，交由下一轮巡检重算')
    }
    if (advance && !page.moved) {
      if (page.atBottom) {
        throw new ZhilianPlatformError(
          'ELEMENT_UNRESOLVED',
          'DOM 已到可见底部但没有平台 hasMore 证词，也尚未跨过时间截止线',
          'manualOnly',
        )
      }
      throw new ZhilianPlatformError('CURSOR_INVALID', 'DOM 虚拟列表滚动没有向前推进')
    }
    const pageDigest = await bindingHash({
      sessions: page.sessions,
      atBottom: page.atBottom,
      scrollTop: Math.round(page.scrollTop),
      scrollHeight: page.scrollHeight,
    })
    if (expectedWindowDigest !== null && expectedWindowDigest !== pageDigest) {
      throw new ZhilianPlatformError('CURSOR_INVALID', 'DOM 列表在分页间发生重排或被人工滚动')
    }
    if (previousWindowDigest === pageDigest) {
      throw new ZhilianPlatformError('CURSOR_INVALID', 'DOM 虚拟列表分页没有向前推进')
    }
    if (windowOffset > page.sessions.length) {
      throw new ZhilianPlatformError('CURSOR_INVALID', 'DOM 列表游标超出当前稳定窗')
    }
    expectedWindowDigest = null
    resetToTop = false

    let crossedCutoff = false
    let payloadFull = false
    let consumed = 0
    const accepted: ZhilianConversationSummary[] = []
    const candidates = page.sessions.slice(windowOffset)
    for (const item of candidates) {
      consumed += 1
      if (item.lastActivityTs !== null && item.lastActivityTs < cutoffMs) {
        crossedCutoff = true
        continue
      }
      if (args.filter === 'unread' && item.unreadCount === 0) continue
      if (seen.has(item.conversationRef)) continue
      if (jsonBytes({
        sessions: [...sessions, ...accepted, item],
        complete: false,
        nextCursor: 'x'.repeat(512),
      }) > RESULT_DATA_BUDGET) {
        consumed -= 1
        if (sessions.length === 0 && accepted.length === 0) {
          throw new ZhilianPlatformError('PAYLOAD_LIMIT', '单条 DOM 会话索引超过内联载荷上限')
        }
        payloadFull = true
        break
      }
      accepted.push(item)
      if (sessions.length + accepted.length >= maxSessions) break
    }
    resumeCursor = {
      v: 1,
      kind: 'list',
      mode: 'dom',
      binding,
      pageNo: windowNo,
      offset: windowOffset + consumed,
      pageDigest,
    }
    for (const item of accepted) {
      seen.add(item.conversationRef)
      sessions.push(item)
    }
    if (crossedCutoff) {
      complete = true
      break
    }
    if (payloadFull || consumed < candidates.length || sessions.length >= maxSessions) break
    if (page.atBottom) {
      throw new ZhilianPlatformError(
        'ELEMENT_UNRESOLVED',
        'DOM 已到可见底部但没有平台 hasMore 证词，也尚未跨过时间截止线',
        'manualOnly',
      )
    }
    previousWindowDigest = pageDigest
    windowNo += 1
    windowOffset = 0
    advance = true
  }

  assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
  await ctx.progress('智联会话列表 DOM 兜底读取完成', 100)
  if (!complete && !resumeCursor) throw new ZhilianPlatformError('CURSOR_INVALID', 'DOM 列表没有可恢复游标')
  return {
    sessions,
    complete,
    nextCursor: complete ? null : encodeCursor(resumeCursor as ListCursor),
  }
}

export async function readZhilianList(
  args: ZhilianListArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianListPage> {
  const tab = await verifiedIMTab(expectedPrincipalFingerprint)
  if (tab.id === undefined) throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  const cutoffDays = Math.min(30, Math.max(1, args.stopOlderThanDays ?? 8))
  const maxSessions = Math.min(32, Math.max(1, args.maxSessions ?? 32))
  const binding = await bindingHash({
    kind: 'list', principal: expectedPrincipalFingerprint ?? '', filter: args.filter,
    cutoffDays, maxSessions,
  })
  const decoded = decodeCursor(args.cursor)
  if (decoded && (
    decoded.kind !== 'list' || !['api', 'dom'].includes(decoded.mode) || decoded.binding !== binding
  )) {
    throw new ZhilianPlatformError('CURSOR_INVALID', '列表分页游标与账号或参数不匹配')
  }
  const cursor = decoded as ListCursor | null
  let pageNo = cursor?.pageNo ?? 1
  let pageOffset = cursor?.offset ?? 0
  const maxCursorOffset = cursor?.mode === 'dom' ? 1_000 : LIST_PAGE_SIZE
  if (!Number.isInteger(pageNo) || pageNo < 1 || pageNo > 100_000 ||
      !Number.isInteger(pageOffset) || pageOffset < 0 || pageOffset > maxCursorOffset ||
      (cursor !== null && (
        !SHA256_HEX.test(cursor.binding) ||
        typeof cursor.pageDigest !== 'string' || !SHA256_HEX.test(cursor.pageDigest)
      ))) {
    throw new ZhilianPlatformError('CURSOR_INVALID', '列表分页游标越界')
  }
  const cutoffMs = Date.now() - cutoffDays * 86_400_000
  if (cursor?.mode === 'dom') {
    return readZhilianListFromDOM(
      args,
      ctx,
      expectedPrincipalFingerprint,
      tab,
      binding,
      cursor,
      cutoffMs,
      maxSessions,
    )
  }
  const sessions: ZhilianConversationSummary[] = []
  const seen = new Set<string>()
  let complete = false
  let expectedPageDigest = cursor?.pageDigest ?? null
  let resumeCursor: ListCursor | null = cursor
  let previousCompletedPageDigest: string | null = null

  while (sessions.length < maxSessions) {
    if (pageNo > 100_000) throw new ZhilianPlatformError('CURSOR_INVALID', '列表分页超过安全页数')
    ctx.checkpoint()
    await ctx.progress(`读取智联会话列表第 ${pageNo} 页`, Math.min(95, 5 + sessions.length))
    let page: MainListPageResult
    try {
      page = await runMain(tab.id, mainReadListPage, [pageNo, LIST_PAGE_SIZE, args.filter])
    } catch (error) {
      if (cursor === null && pageNo === 1 && pageOffset === 0 && sessions.length === 0) {
        await ctx.progress('页面 API 不可用，切换到保守 DOM 虚拟列表兜底', 5)
        return readZhilianListFromDOM(
          args,
          ctx,
          expectedPrincipalFingerprint,
          tab,
          binding,
          null,
          cutoffMs,
          maxSessions,
        )
      }
      throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', `读取智联会话列表失败：${asError(error).message}`)
    }
    if (page.unstable) {
      throw new ZhilianPlatformError('USER_ACTIVE', '会话列表两次采样不一致，交由下一轮巡检重算')
    }
    const pageDigest = await bindingHash({ sessions: page.sessions, hasMore: page.hasMore })
    if (expectedPageDigest !== null && pageDigest !== expectedPageDigest) {
      throw new ZhilianPlatformError('CURSOR_INVALID', '会话列表在分页间发生重排')
    }
    if (previousCompletedPageDigest === pageDigest) {
      throw new ZhilianPlatformError('CURSOR_INVALID', '会话列表分页没有向前推进')
    }
    if (pageOffset > page.sessions.length) {
      throw new ZhilianPlatformError('CURSOR_INVALID', '列表分页游标超出当前稳定页')
    }
    expectedPageDigest = null
    let crossedCutoff = false
    const accepted: ZhilianConversationSummary[] = []
    const candidates = page.sessions.slice(pageOffset)
    let consumed = 0
    let payloadFull = false
    for (const item of candidates) {
      consumed += 1
      if (item.lastActivityTs !== null && item.lastActivityTs < cutoffMs) {
        crossedCutoff = true
        continue
      }
      if (seen.has(item.conversationRef)) continue
      if (jsonBytes({
        sessions: [...sessions, ...accepted, item],
        complete: false,
        nextCursor: 'x'.repeat(512),
      }) > RESULT_DATA_BUDGET) {
        consumed -= 1 // 当前 item 尚未交付，cursor 必须停在它之前。
        if (sessions.length === 0 && accepted.length === 0) {
          throw new ZhilianPlatformError('PAYLOAD_LIMIT', '单条会话索引超过内联载荷上限')
        }
        payloadFull = true
        break
      }
      accepted.push(item)
      if (sessions.length + accepted.length >= maxSessions) break
    }
    resumeCursor = {
      v: 1, kind: 'list', mode: 'api', binding, pageNo,
      offset: pageOffset + consumed, pageDigest,
    }
    for (const item of accepted) {
      seen.add(item.conversationRef)
      sessions.push(item)
    }
    if (payloadFull) break
    if (crossedCutoff || !page.hasMore) {
      complete = true
      break
    }
    if (consumed < candidates.length) {
      pageOffset += consumed
      break
    }
    previousCompletedPageDigest = pageDigest
    pageNo += 1
    pageOffset = 0
  }

  // 返回业务数据前再次确证身份，防止长命令中途切号造成跨账号数据交付。
  assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
  await ctx.progress('智联会话列表读取完成', 100)
  if (!complete && !resumeCursor) {
    throw new ZhilianPlatformError('CURSOR_INVALID', '列表分页没有可恢复锚点')
  }
  return {
    sessions,
    complete,
    nextCursor: complete ? null : encodeCursor(resumeCursor as ListCursor),
  }
}

// 自包含 MAIN-world 历史读取。直接调用 imEngine.getHistoryMsgs；与 Vuex getTimeline 不同，
// 不调用 sendMsgReceipt/sendLastMsgRead，真实扩展验收仍会监测 unread 是否变化。
async function mainReadThreadPage(
  conversationRef: string,
  limit: number,
  cursor: { endTime: number; lastMsgId: string } | null,
): Promise<MainThreadPageResult> {
  type AnyRecord = Record<string, unknown>
  const w = window as unknown as AnyRecord
  const engine = w.imEngine as AnyRecord | undefined
  let initialState: AnyRecord | undefined
  const readInitialState = (): AnyRecord => {
    if (initialState !== undefined) return initialState
    initialState = {}
    const source = Array.from(document.scripts)
      .map((script) => script.textContent ?? '')
      .find((text) => text.includes('__INITIAL_STATE__='))
    if (!source) return initialState
    const candidate = source.slice(source.indexOf('__INITIAL_STATE__=') + '__INITIAL_STATE__='.length).trim()
    const objectStart = candidate.indexOf('{')
    let depth = 0
    let quoted = false
    let escaped = false
    let objectEnd = -1
    for (let index = objectStart; index >= 0 && index < candidate.length; index += 1) {
      const char = candidate[index]
      if (quoted) {
        if (escaped) escaped = false
        else if (char === '\\') escaped = true
        else if (char === '"') quoted = false
        continue
      }
      if (char === '"') quoted = true
      else if (char === '{') depth += 1
      else if (char === '}') {
        depth -= 1
        if (depth === 0) {
          objectEnd = index + 1
          break
        }
      }
    }
    if (objectStart < 0 || objectEnd < 0) return initialState
    const parsed = JSON.parse(candidate.slice(objectStart, objectEnd)) as unknown
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) initialState = parsed as AnyRecord
    return initialState
  }
  const readInitialSessions = (): AnyRecord[] => {
    const im = readInitialState().im as AnyRecord | undefined
    return Array.isArray(im?.sessions) ? im.sessions as AnyRecord[] : []
  }
  const readInitialTimeline = (): unknown[] => {
    const im = readInitialState().im as AnyRecord | undefined
    const timelineMap = im?.timelineMap
    if (!timelineMap || typeof timelineMap !== 'object' || Array.isArray(timelineMap)) return []
    const timelineEntry = (timelineMap as AnyRecord)[conversationRef]
    if (!timelineEntry || typeof timelineEntry !== 'object' || Array.isArray(timelineEntry)) return []
    const timeline = (timelineEntry as AnyRecord).timeline
    return Array.isArray(timeline) ? [...timeline] : []
  }
  let sessions = (engine && Array.isArray(engine.sessions) ? engine.sessions : []) as AnyRecord[]
  let session = sessions.find((item) => String(item.sessionId ?? '') === conversationRef)
  if (!session && engine && typeof engine.getSessionsByIds === 'function') {
    const loaded = await (engine.getSessionsByIds as (arg: AnyRecord) => Promise<AnyRecord>).call(engine, {
      params: { sessionIds: [conversationRef] },
    })
    if (loaded && Array.isArray(loaded.sessions)) {
      sessions = loaded.sessions as AnyRecord[]
      session = sessions.find((item) => String(item.sessionId ?? '') === conversationRef)
    }
  }
  if (!session) session = readInitialSessions().find((item) => String(item.sessionId ?? '') === conversationRef)
  if (!session) throw new Error('conversation_not_found')

  const target = String(session.peerPartnerId ?? '')
  let history: unknown = null
  if (engine && typeof engine.getHistoryMsgs === 'function' && target) {
    const request: AnyRecord = { to: target, limit, asc: true }
    const scene = session.scene ?? session.sessionType
    if (scene != null && String(scene) !== '') request.scene = scene
    if (cursor) {
      request.endTime = cursor.endTime
      request.lastMsgId = cursor.lastMsgId
    }
    try {
      history = await (engine.getHistoryMsgs as (arg: AnyRecord) => Promise<unknown[]>).call(engine, request)
      if (!Array.isArray(history)) history = null
    } catch {
      history = null
    }
  }

  let usedDOM = false
  let domReachedTop = false
  if (!Array.isArray(history)) {
    const selected = new URL(location.href).searchParams.get('sessionId')
    if (selected !== conversationRef) throw new Error('dom_thread_route_not_selected')
    const timeline = document.querySelector<HTMLElement>('.im-timeline__wrapper .km-list') ??
      document.querySelector<HTMLElement>('.im-timeline__wrapper .im-timeline')
    if (!timeline) throw new Error('dom_thread_timeline_missing')
    const boundary = document.querySelector<HTMLElement>('.im-timeline-ending')
    const boundaryText = `${String(boundary?.textContent ?? '')} ${String(timeline.parentElement?.textContent ?? '')}`
    // 只有页面明确声明 90 天可见边界，DOM 回退才有资格声称已到顶。
    // “以下是90天内的聊天消息”是真机当前文案；其余分支兼容已验证过的同义文案。
    const hasExplicitNinetyDayBoundary = /(?:以下是\s*90\s*天内(?:的)?聊天消息|(?:仅展示|只展示)(?:近)?\s*90\s*天(?:内)?(?:的)?(?:聊天)?消息|近\s*90\s*天(?:内)?(?:的)?(?:聊天)?消息)/u
      .test(boundaryText)
    const readRows = (): unknown[] => {
      const vue = (timeline as HTMLElement & { __vue__?: AnyRecord }).__vue__
      const props = vue?._props as AnyRecord | undefined
      if (Array.isArray(props?.data) && props.data.length > 0) return [...props.data]
      // 生产页面不暴露 Vue2 __vue__；SSR 注入的 timelineMap 是同一路由的稳定消息证词。
      return readInitialTimeline()
    }
    let rawRows = readRows()
    if (rawRows.length === 0 && !hasExplicitNinetyDayBoundary) {
      throw new Error('dom_thread_data_unavailable')
    }
    const scrollCandidates = [
      timeline.parentElement,
      ...Array.from(timeline.parentElement?.querySelectorAll<HTMLElement>('.km-scrollbar__wrap, .km-scrollbar__view') ?? []),
    ].filter((item): item is HTMLElement => item !== null)
    const scrollElement = scrollCandidates.sort((left, right) =>
      (right.scrollHeight - right.clientHeight) - (left.scrollHeight - left.clientHeight))[0]
    if (scrollElement && cursor) {
      scrollElement.scrollTop = 0
      scrollElement.dispatchEvent(new Event('scroll', { bubbles: true }))
      await new Promise((resolve) => setTimeout(resolve, 500))
      const afterLoad = readRows()
      await new Promise((resolve) => setTimeout(resolve, 250))
      const stable = readRows()
      const projection = (rows: unknown[]): string => JSON.stringify(rows.map((row) => {
        if (!row || typeof row !== 'object' || Array.isArray(row)) return null
        const record = row as AnyRecord
        return [record.idServer, record.sendMessageId, record.time, record.type, record.text, record.content]
      }))
      if (projection(afterLoad) !== projection(stable)) throw new Error('dom_thread_unstable')
      rawRows = stable
    }
    const rowID = (row: unknown): string => {
      if (!row || typeof row !== 'object' || Array.isArray(row)) return ''
      const record = row as AnyRecord
      return String(record.idServer ?? record.sendMessageId ?? '')
    }
    let eligible = rawRows
    if (cursor) {
      const anchorIndex = rawRows.findIndex((row) => rowID(row) === cursor.lastMsgId)
      if (anchorIndex < 0) throw new Error('dom_thread_cursor_anchor_missing')
      eligible = rawRows.slice(0, anchorIndex)
    }
    history = eligible.slice(Math.max(0, eligible.length - limit))
    domReachedTop = hasExplicitNinetyDayBoundary &&
      eligible.length <= limit
    usedDOM = true
  }
  if (!Array.isArray(history)) throw new Error('thread_history_unavailable')
  if (history.some((row) => !row || typeof row !== 'object' || Array.isArray(row))) {
    throw new Error('getHistoryMsgs row shape invalid')
  }
  const rows = history as AnyRecord[]

  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/\u00a0/gu, ' ')
    .replace(/\s+/gu, ' ')
    .trim()
  const parseObject = (value: unknown): AnyRecord => {
    if (value && typeof value === 'object' && !Array.isArray(value)) return value as AnyRecord
    if (typeof value !== 'string' || value.length === 0) return {}
    try {
      const parsed = JSON.parse(value) as unknown
      return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as AnyRecord : {}
    } catch {
      return {}
    }
  }
  const digest = async (value: string): Promise<string> => {
    const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value))
    return Array.from(new Uint8Array(bytes), (byte) => byte.toString(16).padStart(2, '0')).join('')
  }
  const toMillis = (value: unknown): number | null => {
    const number = Number(value)
    if (!Number.isFinite(number) || number <= 0) return null
    return number < 1_000_000_000_000 ? Math.trunc(number * 1000) : Math.trunc(number)
  }
  const cardState = (value: unknown): ZhilianThreadMessage['cardState'] => {
    const state = clean(value).toUpperCase()
    if (['ACCEPT', 'ACCEPTED', 'DONE', 'PASSED'].includes(state)) return 'accepted'
    if (['REFUSE', 'REFUSED', 'REJECTED'].includes(state)) return 'rejected'
    if (['EXPIRED', 'TIMEOUT', 'LOSE_EFFICACY'].includes(state)) return 'expired'
    if (['PENDING', 'EXCHANGING', 'SUBMITTED'].includes(state)) return 'pending'
    return 'unknown'
  }

  const runtimeSession = w.$session as AnyRecord | null | undefined
  const runtimeStaff = runtimeSession?.staff as AnyRecord | undefined
  // MAIN runtime 是首选证词；真机上 `$session` 可以暂时为 null，此时仅回退到同一份
  // 已解析 __INITIAL_STATE__.session.session.staff，不重复扫描/解析页面脚本。
  let staffID = clean(runtimeStaff?.staffId)
  if (!staffID) {
    const initialSessionEnvelope = readInitialState().session as AnyRecord | undefined
    const initialSession = initialSessionEnvelope?.session as AnyRecord | undefined
    const initialStaff = initialSession?.staff as AnyRecord | undefined
    staffID = clean(initialStaff?.staffId)
  }
  if (!staffID) throw new Error('staff_identity_missing')
  const output: Array<Omit<ZhilianThreadMessage, 'idx'> & { sourceKey: string }> = []
  const sorted = [...rows].sort((a, b) => Number(a.time ?? 0) - Number(b.time ?? 0))
  for (const row of sorted) {
    const envelope = parseObject(row.content)
    const inner = parseObject(envelope.content)
    const details = Object.keys(inner).length > 0 ? inner : envelope
    const rawType = row.type
    const customType = Number(
      typeof rawType === 'number' || /^\d+$/u.test(String(rawType)) ? rawType : envelope.type,
    )
    const from = clean(row.from)
    let direction: ZhilianThreadMessage['direction'] = from
      ? from === staffID ? 'out' : 'in'
      : 'system'
    let kind: ZhilianThreadMessage['kind'] = 'system'
    let text: string | null = null
    let cardType: ZhilianThreadMessage['cardType'] = null
    let state: ZhilianThreadMessage['cardState'] = null
    let identity = ''

    if (rawType === 'text') {
      if (!from) throw new Error('message_direction_unresolved')
      kind = 'text'
      text = clean(row.text)
    } else if (customType === 105) {
      if (!from) throw new Error('message_direction_unresolved')
      kind = 'card'
      cardType = 'wechatExchange'
      text = clean(
        direction === 'out'
          ? details.staffContent ?? details.senderText ?? details.detail
          : details.userContent ?? details.receiverText ?? details.detail,
      ) || '[交换微信请求]'
      state = 'unknown'
      identity = clean(details.requestId ?? details.id ?? details.cardId)
      const messageID = clean(row.sendMessageId ?? row.idServer)
      if (customType === 105 && messageID && typeof w.fetch === 'function') {
        try {
          const response = await (w.fetch as typeof fetch)('/api/im/getWxCard/state', {
            method: 'POST',
            credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ sessionId: conversationRef, messageId: messageID }),
          })
          if (!response.ok) throw new Error(`wx card state http ${response.status}`)
          const rawBody = await response.json() as unknown
          if (!rawBody || typeof rawBody !== 'object' || Array.isArray(rawBody)) {
            throw new Error('wx card state response shape invalid')
          }
          const body = rawBody as AnyRecord
          const data = body.data
          if (typeof data === 'string') state = cardState(data)
          else if (data && typeof data === 'object' && !Array.isArray(data)) {
            const namedState = (data as AnyRecord).state
            if (typeof namedState === 'string') state = cardState(namedState)
          }
          // 真机只确认过数字状态的存在，尚未确认数字→语义的稳定映射；数字必须保留 unknown。
        } catch {
          // 状态接口不可用时保留 unknown；消息本身仍完整进入账本，不伪造确定状态。
        }
      }
    } else if (customType === 131) {
      if (!from) throw new Error('message_direction_unresolved')
      kind = 'text'
      text = clean(details.greetingText ?? envelope.greetingText)
    } else {
      direction = 'system'
      kind = 'system'
      text = clean(
        details.staffText ?? details.userText ?? details.title ?? details.content ??
        envelope.msgb ?? envelope.msgc ?? envelope.text ?? row.text,
      ) || `[系统消息:${Number.isFinite(customType) ? customType : clean(rawType) || 'unknown'}]`
    }

    const stableMessageID = clean(row.idServer ?? row.sendMessageId)
    if (!stableMessageID) throw new Error('message_identity_missing')
    // contentHash 是脑手共享的规范内容哈希，不是平台消息主键：文本/系统只哈希
    // NFC+空白规范化文本，媒体使用固定占位符；卡片只哈希类型+稳定身份且排除状态。
    // 方向在 anchor 中另列，不能再次混入 hash。
    const canonicalContent = clean(text)
    const contentHash = kind === 'card'
      ? await digest(`card\x1f${cardType ?? 'other'}\x1f${clean(identity || stableMessageID || text)}`)
      : await digest(canonicalContent)
    output.push({
      sourceKey: await digest(`source-v1|${stableMessageID}`),
      direction,
      kind,
      text,
      blobRef: null,
      contentHash,
      cardType,
      cardState: state,
      tsApprox: toMillis(row.time),
    })
  }

  const oldest = sorted[0]
  // 页面自身以 oldest.time + oldest.idServer 作排他游标；不能减 1ms，
  // 否则同一毫秒内的更早消息会被跳过。
  const endTime = oldest ? (toMillis(oldest.time) ?? 0) : 0
  const lastMsgId = oldest ? clean(oldest.idServer ?? oldest.sendMessageId) : ''
  const displayName = clean(session.name ?? session.realName) || '未命名候选人'
  const platformUserRef = clean(session.peerPartnerId ?? session.typeUserId ?? session.userId)
  return {
    messages: output,
    reachedTop: usedDOM ? domReachedTop : rows.length < limit,
    cursor: (usedDOM ? !domReachedTop && rows.length > 0 : rows.length >= limit) && endTime > 0 && lastMsgId
      ? { endTime, lastMsgId }
      : null,
    peer: platformUserRef ? { displayName, platformUserRef } : { displayName },
  }
}

type AnchorComparable = Pick<ZhilianThreadMessage, 'direction' | 'contentHash'>

interface AnchorResolution {
  count: number
  outputStart: number | null
}

function sameAnchorMessage(message: AnchorComparable, anchor: ZhilianMessageAnchor): boolean {
  return message.direction === anchor.direction && message.contentHash === anchor.contentHash
}

function wholeAnchorStarts(messages: AnchorComparable[], anchors: ZhilianMessageAnchor[]): number[] {
  if (anchors.length === 0 || messages.length < anchors.length) return []
  const starts: number[] = []
  for (let start = 0; start <= messages.length - anchors.length; start += 1) {
    let matches = true
    for (let offset = 0; offset < anchors.length; offset += 1) {
      if (!sameAnchorMessage(messages[start + offset], anchors[offset])) {
        matches = false
        break
      }
    }
    if (matches) starts.push(start)
  }
  return starts
}

function anchorPrefixBit(mask: number, start: number): boolean {
  return (mask & (1 << start)) !== 0
}

// 定位当前较旧 protocol page 中的完整锚点，同时把“本页后缀 +
// 已返回较新聚合前缀”的跨页命中纳入计数。只有唯一命中时手才可裁掉
// 锚前上下文；多解必须保留完整候选窗口，让脑按账本做保守裁决并留审计。
function resolveAnchor(
  messages: AnchorComparable[],
  anchors: ZhilianMessageAnchor[],
  newerPrefixMask: number,
): AnchorResolution {
  if (anchors.length === 0) return { count: 0, outputStart: null }
  const whole = wholeAnchorStarts(messages, anchors)
  const crossingSplits: number[] = []
  for (let split = 1; split < anchors.length && split <= messages.length; split += 1) {
    if (!anchorPrefixBit(newerPrefixMask, split)) continue
    const start = messages.length - split
    let matches = true
    for (let offset = 0; offset < split; offset += 1) {
      if (!sameAnchorMessage(messages[start + offset], anchors[offset])) {
        matches = false
        break
      }
    }
    if (matches) crossingSplits.push(split)
  }
  const count = whole.length + crossingSplits.length
  if (count !== 1) return { count, outputStart: null }
  return {
    count,
    outputStart: whole.length === 1 ? whole[0] : messages.length - crossingSplits[0],
  }
}

// 生成下一个 opaque cursor 的跨页摘要。不携带消息正文；anchorTail 最多 5 条，
// 因此最多使用 4 bit，始终远低于 cursor 512 字符/2KiB 边界。
function nextAnchorPrefixMask(
  currentPage: AnchorComparable[],
  anchors: ZhilianMessageAnchor[],
  newerPrefixMask: number,
): number {
  let mask = 0
  for (let start = 1; start < anchors.length; start += 1) {
    const needed = anchors.length - start
    const localCount = Math.min(currentPage.length, needed)
    let matches = true
    for (let offset = 0; offset < localCount; offset += 1) {
      if (!sameAnchorMessage(currentPage[offset], anchors[start + offset])) {
        matches = false
        break
      }
    }
    if (!matches) continue
    if (localCount === needed || anchorPrefixBit(newerPrefixMask, start + localCount)) {
      mask |= 1 << start
    }
  }
  return mask
}

async function ensureThreadRoute(
  tab: chrome.tabs.Tab,
  conversationRef: string,
  ctx: PrimitiveContext,
): Promise<void> {
  if (tab.id === undefined) throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  let selected = ''
  try {
    selected = new URL(tab.url ?? '').searchParams.get('sessionId') ?? ''
  } catch {
    // verifiedIMTab 已确认 IM route；URL 解析失败仍按需要导航处理。
  }
  if (selected === conversationRef) return
  const commandNavigation = beginCommandNavigation(tab.id)
  try {
    await chrome.tabs.update(tab.id, {
      url: `${ZHILIAN_IM_URL}?sessionId=${encodeURIComponent(conversationRef)}`,
    })
  } catch (error) {
    commandNavigation.end()
    throw error
  }
  for (let attempt = 0; attempt < 80; attempt += 1) {
    ctx.checkpoint()
    const latest = await chrome.tabs.get(tab.id)
    try {
      const url = new URL(latest.url ?? '')
      if (latest.status === 'complete' && url.pathname === '/app/im' &&
          url.searchParams.get('sessionId') === conversationRef && await contentScriptHealthy(tab.id)) {
        return
      }
    } catch {
      // SPA 尚未稳定，继续受 execBudget/deadline 约束地等待。
    }
    if (attempt % 8 === 0) await ctx.progress('等待目标智联会话就绪', Math.min(90, 10 + attempt))
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  throw new ZhilianPlatformError(
    'CTX_LOST_DURING_EXEC',
    '目标智联会话在期限内未就绪',
    'afterRecovery',
    undefined,
    'possible',
  )
}

export async function readZhilianThread(
  args: ZhilianThreadArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianThreadPage> {
  const tab = await verifiedIMTab(expectedPrincipalFingerprint)
  if (tab.id === undefined) throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  const maxMessages = Math.min(64, Math.max(1, args.window.maxMessages ?? 50))
  const anchors = args.window.anchorTail ?? []
  const binding = await bindingHash({
    kind: 'thread',
    anchorBoundary: 1,
    principal: expectedPrincipalFingerprint ?? '',
    conversationRef: args.conversationRef,
    maxMessages,
    anchors,
    deep: args.window.deep === true,
  })
  const decoded = decodeCursor(args.cursor)
  if (decoded && (decoded.kind !== 'thread' || decoded.mode !== 'api' || decoded.binding !== binding)) {
    throw new ZhilianPlatformError('CURSOR_INVALID', '会话分页游标与账号、会话或窗口参数不匹配')
  }
  const threadCursor = decoded as ThreadCursor | null
  const rawAnchorPrefixMask = threadCursor === null
    ? 0
    : (threadCursor as ThreadCursor & { ap?: unknown }).ap === undefined
      ? 0
      : (threadCursor as ThreadCursor & { ap?: unknown }).ap
  const legalAnchorPrefixMask = anchors.length > 1 ? (1 << anchors.length) - 2 : 0
  if (threadCursor && (
    !SHA256_HEX.test(threadCursor.binding) ||
    !Number.isSafeInteger(threadCursor.endTime) || threadCursor.endTime <= 0 ||
    typeof threadCursor.lastMsgId !== 'string' || threadCursor.lastMsgId.length === 0 ||
    threadCursor.lastMsgId.length > 512 ||
    !Number.isSafeInteger(rawAnchorPrefixMask) || Number(rawAnchorPrefixMask) < 0 ||
    Number(rawAnchorPrefixMask) > legalAnchorPrefixMask ||
    (Number(rawAnchorPrefixMask) & ~legalAnchorPrefixMask) !== 0
  )) {
    throw new ZhilianPlatformError('CURSOR_INVALID', '会话分页游标无效')
  }
  const newerPrefixMask = Number(rawAnchorPrefixMask)
  let cursor: { endTime: number; lastMsgId: string } | null = threadCursor
    ? { endTime: threadCursor.endTime, lastMsgId: threadCursor.lastMsgId }
    : null
  let reachedTop = false
  let peer: MainThreadPageResult['peer'] = null
  const collected: Array<Omit<ZhilianThreadMessage, 'idx'> & { sourceKey: string }> = []
  const dedup = new Set<string>()
  let platformReadStarted = false

  while (collected.length < maxMessages && !reachedTop) {
    ctx.checkpoint()
    await ctx.progress(`读取会话历史 ${collected.length}/${maxMessages}`, Math.min(95, 5 + collected.length))
    let page: MainThreadPageResult
    const cursorBefore = cursor
    if (!platformReadStarted) {
      // readThread 按最坏实现定为 idempotentReadReceipt；紧贴第一次平台读取设置取消安全点。
      // 该钩子抛出的 StopExecution 必须原样回到 Dispatcher，不能在平台错误映射中吞掉。
      ctx.beforeSideEffect()
      platformReadStarted = true
      try {
        await ensureThreadRoute(tab, args.conversationRef, ctx)
      } catch (error) {
        if (error instanceof ZhilianPlatformError) throw error
        throw new ZhilianPlatformError(
          'CTX_LOST_DURING_EXEC',
          `打开目标智联会话失败：${asError(error).message}`,
          'afterRecovery',
          undefined,
          'possible',
        )
      }
    }
    try {
      page = await runMain(tab.id, mainReadThreadPage, [
        args.conversationRef,
        Math.min(LIST_PAGE_SIZE, maxMessages - collected.length),
        cursor,
      ])
    } catch (error) {
      const message = asError(error).message
      if (message.includes('conversation_not_found')) {
        throw new ZhilianPlatformError('CONVERSATION_NOT_FOUND', '智联会话不存在', 'no')
      }
      if (message.includes('message_identity_missing') || message.includes('conversation_target_missing')) {
        throw new ZhilianPlatformError(
          'ELEMENT_UNRESOLVED',
          `智联会话身份字段无法确证：${message}`,
          'manualOnly',
          undefined,
          'possible',
        )
      }
      throw new ZhilianPlatformError(
        'CTX_LOST_DURING_EXEC',
        `读取智联会话失败：${message}`,
        'afterRecovery',
        undefined,
        'possible',
      )
    }
    if (sameThreadPosition(cursorBefore, page.cursor)) {
      throw new ZhilianPlatformError('CURSOR_INVALID', '会话历史平台游标没有向前推进')
    }
    peer = page.peer ?? peer
    const pageSeen = new Set(dedup)
    const unseen = page.messages.filter((message) => {
      if (pageSeen.has(message.sourceKey)) return false
      pageSeen.add(message.sourceKey)
      return true
    })
    // 平台游标每次返回更旧的一页；页内已经是正序，所以只能整页前插。
    // 不能靠 tsApprox 全局排序：跨页同毫秒时 stable sort 会把较新的页留在前面。
    const candidate = [...unseen, ...collected]
    const anchorResolution = resolveAnchor(candidate, anchors, newerPrefixMask)
    // 唯一命中才允许手裁剪。重复锚的完整候选必须原样交给脑；否则手会
    // 隐藏歧义，使脑无法按“最晚起点/最短新增”裁决和记录审计证词。
    const returnCandidate = anchorResolution.count === 1
      ? candidate.slice(anchorResolution.outputStart as number)
      : candidate
    const candidateMessages = returnCandidate
      .map(({ sourceKey: _sourceKey, ...message }) => message)
    for (const message of candidateMessages) {
      if (message.text !== null && new TextEncoder().encode(message.text).length > 2048) {
        throw new ZhilianPlatformError('PAYLOAD_LIMIT', '消息正文超过当前内联上限')
      }
    }
    if (jsonBytes({
      messages: candidateMessages,
      reachedTop: false,
      anchorMatched: false,
      complete: false,
      nextCursor: 'cursor',
      peer,
    }) > RESULT_DATA_BUDGET) {
      if (collected.length === 0 || cursorBefore === null) {
        throw new ZhilianPlatformError('PAYLOAD_LIMIT', '单页会话消息超过内联载荷上限')
      }
      cursor = cursorBefore
      break
    }
    const beforeCount = collected.length
    for (const message of unseen) {
      dedup.add(message.sourceKey)
    }
    collected.splice(0, collected.length, ...candidate)
    reachedTop = page.reachedTop
    cursor = page.cursor
    if (!reachedTop && collected.length === beforeCount && cursor === null) {
      throw new ZhilianPlatformError(
        'ELEMENT_UNRESOLVED',
        '会话历史既未到顶也没有可恢复游标',
        'manualOnly',
        undefined,
        'possible',
      )
    }
    if (anchorResolution.count > 0 && !args.window.deep) break
    if (!cursor || page.messages.length === 0) break
  }

  assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
  const anchorResolution = resolveAnchor(collected, anchors, newerPrefixMask)
  const selected = anchorResolution.count === 1
    ? collected.slice(anchorResolution.outputStart as number)
    : collected
  const messages = selected
    .map(({ sourceKey: _sourceKey, ...message }, idx) => ({ ...message, idx }))
  for (const message of messages) {
    if (message.text !== null && new TextEncoder().encode(message.text).length > 2048) {
      throw new ZhilianPlatformError('PAYLOAD_LIMIT', '消息正文超过当前内联上限')
    }
  }
  // anchorMatched 只声明有界窗口中观察到了锚；它不承诺唯一对齐。多解时
  // selected 保留全部候选，由脑端账本对齐器选择最小新增投影并审计。
  const anchorMatched = anchorResolution.count > 0
  const complete = reachedTop || anchorMatched
  if (!complete && !cursor) {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      '历史消息无法生成稳定分页游标',
      'manualOnly',
      undefined,
      platformReadStarted ? 'possible' : 'none',
    )
  }
  const anchorPrefixMask = complete ? 0 : nextAnchorPrefixMask(collected, anchors, newerPrefixMask)
  const nextCursor = complete ? null : cursor
    ? encodeCursor({
        v: 1,
        kind: 'thread',
        mode: 'api',
        binding,
        ...cursor,
        ...(anchorPrefixMask === 0 ? {} : { ap: anchorPrefixMask }),
      })
    : null
  const result: ZhilianThreadPage = {
    messages,
    reachedTop,
    anchorMatched,
    complete,
    nextCursor,
    peer,
  }
  if (jsonBytes(result) > RESULT_DATA_BUDGET) {
    throw new ZhilianPlatformError('PAYLOAD_LIMIT', '会话读取结果超过内联载荷上限')
  }
  await ctx.progress('智联会话读取完成', 100)
  return result
}

// 仅导出纯解析/游标函数供 Node 用同一份生产代码做脱敏 fixture 测试；生产 bundle
// 没有引用时由 esbuild tree-shake，不形成第二条运行路径。
export const zhilianTestHooks = Object.freeze({
  bindingHash,
  decodeCursor,
  encodeCursor,
  mainProbeZhilian,
  mainReadListPage,
  mainReadThreadPage,
  runMain,
})
