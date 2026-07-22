// 智联平台实现接缝。这里可以知道 host、路由、页面字段与 NIM 消息形态；
// base、脑端与协议只看平台无关的原语结果。

import type { PrimitiveContext } from '../registry'
import { beginCommandNavigation } from '../../base/navigation'
import type {
  CandidateReadCurrentData,
  CandidateReadResumeArgs,
  CandidateReadResumeData,
  CandidateReadSourcingResumeArgs,
  CandidateReadSourcingResumeData,
  CandidateResumeLabelValue,
  ChatReadGreetingOutcomeArgs,
  ChatReadGreetingOutcomeData,
  ChatReadListArgs,
  ChatReadListData,
  ChatReadThreadArgs,
  ChatReadThreadData,
  ChatSendGreetingArgs,
  ChatSendGreetingData,
  ChatSendGreetingGuards,
  ChatSendMessageArgs,
  ChatSendMessageData,
  ChatSendMessageGuards,
  ConversationSummary,
  DebugInspectSendSurfaceData,
  ErrorCode,
  MessageAnchor,
  NotReadyReason,
  ProbePlatformData,
  Retryable,
  SideEffect,
  ThreadMessage,
} from '../../base/protocol'
import { Primitive as PrimitiveName, validatePrimitiveData } from '../../base/protocol'

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
export type ZhilianCurrentCandidate = CandidateReadCurrentData
export type ZhilianResumeArgs = CandidateReadResumeArgs
export type ZhilianResumeData = CandidateReadResumeData
export type ZhilianSourcingResumeArgs = CandidateReadSourcingResumeArgs
export type ZhilianSourcingResumeData = CandidateReadSourcingResumeData
export type ZhilianGreetingArgs = ChatSendGreetingArgs
export type ZhilianGreetingGuards = ChatSendGreetingGuards
export type ZhilianGreetingData = ChatSendGreetingData
export type ZhilianGreetingOutcomeArgs = ChatReadGreetingOutcomeArgs
export type ZhilianGreetingOutcomeData = ChatReadGreetingOutcomeData
export type ZhilianListArgs = ChatReadListArgs
export type ZhilianConversationSummary = ConversationSummary
export type ZhilianListPage = ChatReadListData
export type ZhilianMessageAnchor = MessageAnchor
export type ZhilianThreadArgs = ChatReadThreadArgs
export type ZhilianThreadMessage = ThreadMessage
export type ZhilianThreadPage = ChatReadThreadData
export type ZhilianSendArgs = ChatSendMessageArgs
export type ZhilianSendGuards = ChatSendMessageGuards
export type ZhilianSendData = ChatSendMessageData

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

const MAIN_CURRENT_CANDIDATE_FAILURE_REASONS = [
  'route_missing',
  'detail_absent',
  'detail_cardinality',
  'list_source_unavailable',
  'detail_binding_ambiguous',
  'candidate_identity_unavailable',
  'candidate_identity_duplicated',
  'position_identity_unavailable',
  'position_identity_mismatch',
  'position_title_ambiguous',
  'position_title_mismatch',
  'unexpected',
] as const

type MainCurrentCandidateFailureReason = typeof MAIN_CURRENT_CANDIDATE_FAILURE_REASONS[number]

interface MainCurrentCandidateReady {
  status: 'ready'
  data: ZhilianCurrentCandidate
}

interface MainCurrentCandidateFailed {
  status: 'failed'
  reason: MainCurrentCandidateFailureReason
}

type MainCurrentCandidateResult = MainCurrentCandidateReady | MainCurrentCandidateFailed

const MAIN_RESUME_FAILURE_REASONS = [
  'route_changed',
  'session_unavailable',
  'target_changed',
  'detail_cardinality',
  'stale_modal',
  'entry_cardinality',
  'modal_cardinality',
  'basic_unresolved',
  'expectations_unresolved',
  'work_unresolved',
  'education_unresolved',
  'self_evaluation_unresolved',
  'payload_limit',
  'unexpected',
] as const

type MainResumeFailureReason = typeof MAIN_RESUME_FAILURE_REASONS[number]

interface MainResumeReady {
  status: 'ready'
  data: ZhilianResumeData
}

interface MainResumeFailed {
  status: 'failed'
  reason: MainResumeFailureReason
}

type MainResumeResult = MainResumeReady | MainResumeFailed

const MAIN_SOURCING_RESUME_FAILURE_REASONS = [
  'route_changed',
  'list_source_unavailable',
  'candidate_identity_unavailable',
  'candidate_identity_duplicated',
  'no_candidate',
  'position_identity_unavailable',
  'position_identity_mismatch',
  'position_title_ambiguous',
  'position_title_mismatch',
  'stale_detail_ambiguous',
  'close_unavailable',
  'entry_cardinality',
  'modal_cardinality',
  'detail_binding_ambiguous',
  'target_changed',
  'basic_unresolved',
  'expectations_unresolved',
  'work_unresolved',
  'education_unresolved',
  'self_evaluation_unresolved',
  'payload_limit',
  'unexpected',
] as const

const MAIN_SOURCING_RESUME_SKIPPABLE_FAILURE_REASONS = [
  'basic_unresolved',
  'expectations_unresolved',
  'work_unresolved',
  'education_unresolved',
  'self_evaluation_unresolved',
  'payload_limit',
] as const

type MainSourcingResumeFailureReason = typeof MAIN_SOURCING_RESUME_FAILURE_REASONS[number]

interface MainSourcingResumeReady {
  status: 'ready'
  data: ZhilianSourcingResumeData
}

interface MainSourcingResumeFailed {
  status: 'failed'
  reason: MainSourcingResumeFailureReason
  // 只在稳定身份已确定、失败又仅属于该候选人简历内容时携带。它只在
  // 同一条手侧命令内扩充临时排除集，不进入协议 result、日志或脑账本。
  failedPlatformUserRef?: string
}

type MainSourcingResumeResult = MainSourcingResumeReady | MainSourcingResumeFailed

const MAIN_GREETING_FAILURE_REASONS = [
  'action_window_elapsed',
  'identity_changed',
  'target_changed',
  'relationship_changed',
  'existing_editor',
  'two_step_surface_unavailable',
  'editor_not_opened',
  'custom_option_unavailable',
  'editor_unavailable',
  'editor_changed',
  'default_setting_unresolved',
  'default_setting_selected',
  'send_surface_unavailable',
  'input_rejected',
] as const

type MainGreetingFailureReason = typeof MAIN_GREETING_FAILURE_REASONS[number]
type MainGreetingPhase = 'prepare' | 'preflight' | 'commit'

type MainGreetingActionResult =
  | { status: 'prepared' }
  | { status: 'ready' }
  | { status: 'clicked' }
  | { status: 'failed'; reason: MainGreetingFailureReason }

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

const MAIN_SEND_SURFACE_STAGES = [
  'ok',
  'route_target_missing',
  'composer_count',
  'detail_ambiguous',
  'button_count',
  'wrapper_mismatch',
  'button_form_unsafe',
] as const

type MainSendSurfaceStage = typeof MAIN_SEND_SURFACE_STAGES[number]

const SEND_SURFACE_STAGE_TO_DIAGNOSTIC: Readonly<Record<
  MainSendSurfaceStage,
  DebugInspectSendSurfaceData['stage']
>> = Object.freeze({
  ok: 'ready',
  route_target_missing: 'route_missing',
  composer_count: 'composer_cardinality',
  detail_ambiguous: 'detail_cardinality',
  button_count: 'button_cardinality',
  wrapper_mismatch: 'dom_containment',
  button_form_unsafe: 'button_form_unsafe',
})

interface MainSendSurfaceResult {
  selected: boolean
  composerBindingResolved: boolean
  composerBindingMatched: boolean
  composerCount: number
  composerValue: string
  sendButtonCount: number
  diagnosticStage: MainSendSurfaceStage
}

interface MainFindConversationResult {
  status: 'found' | 'failed'
  reason?: 'list_surface_missing' | 'list_items_missing' | 'list_binding_unresolved' |
    'list_window_repeated' | 'target_binding_duplicated' | 'target_binding_changed' |
    'target_not_found' | 'list_scroll_stalled'
}

interface MainClickConversationResult {
  status: 'clicked' | 'already_selected' | 'failed'
  reason?: 'action_window_elapsed' | 'identity_changed' | 'route_changed' | 'list_items_missing' |
    'list_binding_unresolved' | 'target_binding_duplicated' | 'target_binding_changed' |
    'click_target_missing' | 'composer_ambiguous' | 'composer_nonempty'
}

interface MainSendOnceResult {
  status: 'ready' | 'clicked' | 'failed'
  reason?: 'route_changed' | 'guard_unresolved' | 'target_changed' | 'baseline_changed' |
    'composer_nonempty' | 'composer_missing' | 'input_rejected' | 'identity_changed' |
    'action_window_elapsed'
}

type MainSendPhase = 'preflight' | 'commit'

const MAIN_SEND_BASELINE_FAILURE_STAGES = [
  'engine_unavailable',
  'route_changed',
  'session_unavailable',
  'history_first_unavailable',
  'guard_snapshot_uncovered',
  'hash_unavailable',
  'unexpected',
] as const

type MainSendBaselineFailureStage = typeof MAIN_SEND_BASELINE_FAILURE_STAGES[number]

interface MainSendBaselineReady {
  status: 'ready'
  stage: 'ready'
  serverSourceKeys: string[]
  targetBindingToken: string
}

interface MainSendBaselineFailed {
  status: 'failed'
  stage: MainSendBaselineFailureStage
}

type MainSendBaselineResult = MainSendBaselineReady | MainSendBaselineFailed

function validatedMainSendBaseline(value: unknown): MainSendBaselineResult | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const record = value as Record<string, unknown>
  if (record.status === 'failed') {
    return typeof record.stage === 'string' &&
      (MAIN_SEND_BASELINE_FAILURE_STAGES as readonly string[]).includes(record.stage)
      ? record as unknown as MainSendBaselineFailed
      : null
  }
  if (record.status !== 'ready' || record.stage !== 'ready' ||
      !Array.isArray(record.serverSourceKeys) || record.serverSourceKeys.length > 64 ||
      typeof record.targetBindingToken !== 'string') return null
  const hashPattern = /^[0-9a-f]{64}$/u
  if (!hashPattern.test(record.targetBindingToken) ||
      record.serverSourceKeys.some((key) => typeof key !== 'string' || !hashPattern.test(key)) ||
      new Set(record.serverSourceKeys).size !== record.serverSourceKeys.length) return null
  return record as unknown as MainSendBaselineReady
}

interface MainObserveStableOutboundResult {
  selected: boolean
  matchingNewServerMessages: number
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

export function normalizeZhilianMessageText(value: string): string {
  return value.normalize('NFC').replace(/\u00a0/gu, ' ').replace(/\s+/gu, ' ').trim()
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
  func: (...args: A) => R | Promise<R>,
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
  const mainError = typeof first.result === 'object' && !Array.isArray(first.result)
    ? (first.result as Record<string, unknown>).__recruitHelperMainError
    : undefined
  if (typeof mainError === 'string' && mainError.length > 0) {
    throw new Error(mainError.slice(0, 300))
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

// M4 当前候选人读取必须是单次、自包含的 MAIN-world 投影。resumeNumber 只在
// 本函数局部把 body 下的唯一详情瞬时连接到唯一来源卡；它永不进入返回值，
// 也不作为 userMasterId 读取失败后的身份降级。
function mainReadCurrentCandidate(): MainCurrentCandidateResult {
  type AnyRecord = Record<string, unknown>
  const asRecord = (value: unknown): AnyRecord | null =>
    value !== null && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const opaque = (value: unknown): string => {
    if (typeof value === 'string') return value.trim()
    if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
    return ''
  }
  const text = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/\u00a0/gu, ' ')
    .replace(/\s+/gu, ' ')
    .trim()
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const failed = (reason: MainCurrentCandidateFailureReason): MainCurrentCandidateFailed => ({
    status: 'failed',
    reason,
  })

  try {
    const route = new URL(location.href)
    if (!route.pathname.startsWith('/app/recommend')) return failed('route_missing')

    const details = Array.from(document.querySelectorAll<HTMLElement>('.new-shortcut-resume__modal'))
      .filter(visible)
    if (details.length === 0) return failed('detail_absent')
    if (details.length !== 1) return failed('detail_cardinality')
    const detail = details[0]

    const routeResumeNumber = opaque(route.searchParams.get('resumeNumber'))
    const routeJobNumber = opaque(route.searchParams.get('jobNumber'))
    if (!routeResumeNumber) return failed('detail_binding_ambiguous')
    if (!routeJobNumber) return failed('position_identity_unavailable')

    const listItems = Array.from(document.querySelectorAll<HTMLElement>('[role="listitem"]'))
    if (listItems.length === 0) return failed('list_source_unavailable')
    const sources: Array<{ owner: AnyRecord; source: AnyRecord }> = []
    for (const item of listItems) {
      const owner = asRecord((item as HTMLElement & { __vue__?: unknown }).__vue__)
      const props = asRecord(owner?._props)
      const source = asRecord(props?.source)
      if (!owner || !source) return failed('list_source_unavailable')
      sources.push({ owner, source })
    }

    const matches = sources.filter(({ source }) => opaque(source.resumeNumber) === routeResumeNumber)
    if (matches.length !== 1) return failed('detail_binding_ambiguous')
    const matched = matches[0]
    const platformUserRef = opaque(matched.source.userMasterId)
    if (!platformUserRef) return failed('candidate_identity_unavailable')
    const sameUserCount = sources.filter(({ source }) =>
      opaque(source.userMasterId) === platformUserRef).length
    if (sameUserCount !== 1) return failed('candidate_identity_duplicated')

    // 只读已由批次 0 真机确认的三个固定字面来源；candidate.searchCondition
    // 中的 activeJob 是已证实的陈旧值，不能参与 positionRef。
    const root = asRecord(matched.owner.$root)
    const ownerRoute = asRecord(root?._route)
    const ownerQuery = asRecord(ownerRoute?.query)
    const store = asRecord(matched.owner.$store)
    const state = asRecord(store?.state)
    const talent = asRecord(state?.talent)
    const activeJob = asRecord(talent?.activeJob)
    const routedJobNumber = opaque(ownerQuery?.jobNumber)
    const activeJobNumber = opaque(activeJob?.jobNumber)
    if (!routedJobNumber || !activeJobNumber) return failed('position_identity_unavailable')
    if (routeJobNumber !== routedJobNumber || routeJobNumber !== activeJobNumber) {
      return failed('position_identity_mismatch')
    }

    const visibleJobTitles = Array.from(document.querySelectorAll<HTMLElement>(
      '.job-pane__item--active .job-pane__item-job-title',
    )).filter(visible).map((element) => text(element.textContent)).filter(Boolean)
    if (visibleJobTitles.length > 1) return failed('position_title_ambiguous')
    const visibleJobTitle = visibleJobTitles[0] ?? ''
    const activeJobTitle = text(activeJob?.jobTitle)
    if (visibleJobTitle && activeJobTitle && visibleJobTitle !== activeJobTitle) {
      return failed('position_title_mismatch')
    }

    const visibleNames = Array.from(detail.querySelectorAll<HTMLElement>('.resume-basic-new__name'))
      .filter(visible).map((element) => text(element.textContent)).filter(Boolean)
    const displayName = visibleNames.length === 1 && visibleNames[0].length <= 256
      ? visibleNames[0]
      : null
    const title = visibleJobTitle || activeJobTitle
    const positionTitle = title && title.length <= 256 ? title : null

    const actionButtons = Array.from(detail.querySelectorAll<HTMLButtonElement>('button[type="button"]'))
      .filter(visible)
    const greetingButtons = actionButtons
      .filter((button) => !button.disabled && text(button.textContent) === '打招呼')
    const continueButtons = actionButtons
      .filter((button) => text(button.textContent) === '继续沟通')
    const contactState: ZhilianCurrentCandidate['contactState'] =
      greetingButtons.length === 1 && continueButtons.length === 0
        ? 'unestablished'
        : greetingButtons.length === 0 && continueButtons.length === 1
          ? 'established'
          : 'unknown'

    return {
      status: 'ready',
      data: {
        platformUserRef,
        displayName,
        positionRef: routeJobNumber,
        positionTitle,
        contactState,
      },
    }
  } catch {
    return failed('unexpected')
  }
}

// IM 简历补采只接受当前 route 与 imEngine 唯一会话的直接绑定。单次 evaluator
// 在同一 MAIN task 内复核、只点一次当前详情标准入口、完整读取并再次复核。
async function mainReadCurrentResume(
  conversationRef: string,
  platformUserRef: string,
): Promise<MainResumeResult> {
  type AnyRecord = Record<string, unknown>
  const asRecord = (value: unknown): AnyRecord | null =>
    value !== null && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/\u00a0/gu, ' ')
    .replace(/\s+/gu, ' ')
    .trim()
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const blockText = (element: HTMLElement): string => {
    const raw = typeof element.innerText === 'string' ? element.innerText : element.textContent ?? ''
    return raw.normalize('NFC').replace(/\u00a0/gu, ' ').split(/\n+/u)
      .map((line) => line.replace(/[\t ]+/gu, ' ').trim()).filter(Boolean).join('\n')
  }
  const failed = (reason: MainResumeFailureReason): MainResumeFailed => ({ status: 'failed', reason })
  const visibleAll = (root: ParentNode, selector: string): HTMLElement[] =>
    Array.from(root.querySelectorAll<HTMLElement>(selector)).filter(visible)
  const routeMatches = (): boolean => {
    try {
      const route = new URL(location.href)
      return route.pathname === '/app/im' && route.searchParams.get('sessionId') === conversationRef
    } catch {
      return false
    }
  }
  const initialSessions = (): AnyRecord[] | null => {
    const source = Array.from(document.scripts ?? [])
      .map((script) => script.textContent ?? '')
      .find((candidate) => candidate.includes('__INITIAL_STATE__='))
    if (!source) return null
    const candidate = source.slice(source.indexOf('__INITIAL_STATE__=') + '__INITIAL_STATE__='.length).trim()
    const start = candidate.indexOf('{')
    let depth = 0
    let quoted = false
    let escaped = false
    for (let index = start; index >= 0 && index < candidate.length; index += 1) {
      const char = candidate[index]
      if (quoted) {
        if (escaped) escaped = false
        else if (char === '\\') escaped = true
        else if (char === '"') quoted = false
        continue
      }
      if (char === '"') quoted = true
      else if (char === '{') depth += 1
      else if (char === '}' && --depth === 0) {
        try {
          const initial = asRecord(JSON.parse(candidate.slice(start, index + 1)))
          const im = asRecord(initial?.im)
          return Array.isArray(im?.sessions) ? im.sessions as AnyRecord[] : null
        } catch {
          return null
        }
      }
    }
    return null
  }
  const bindingSessions = (): AnyRecord[] | null => {
    const engine = asRecord((window as unknown as AnyRecord).imEngine)
    if (engine && Array.isArray(engine.sessions)) return engine.sessions as AnyRecord[]
    return initialSessions()
  }
  const targetMatches = (): boolean => {
    const sessions = bindingSessions()
    if (sessions === null) return false
    const matches = sessions.filter((item) => clean(item.sessionId) === conversationRef)
    return matches.length === 1 && clean(matches[0].peerPartnerId) === platformUserRef
  }

  try {
    if (!routeMatches()) return failed('route_changed')
    if (bindingSessions() === null) return failed('session_unavailable')
    if (!targetMatches()) return failed('target_changed')

    const details = visibleAll(document, '.im-session-detail')
    if (details.length !== 1) return failed('detail_cardinality')
    const detail = details[0]
    if (visibleAll(document, '.new-shortcut-resume__modal').length !== 0) return failed('stale_modal')
    const entries = visibleAll(detail, '.hover-resume-footer__button, button, a, [role="button"]')
      .filter((element) => clean(element.textContent) === '查看详情' &&
        element.closest('.im-session-detail') === detail)
    if (entries.length !== 1) return failed('entry_cardinality')

    entries[0].click()
    let modals: HTMLElement[] = []
    const waitUntil = Date.now() + 6_000
    while (Date.now() < waitUntil) {
      modals = visibleAll(document, '.new-shortcut-resume__modal')
      if (modals.length !== 0) break
      await new Promise((resolve) => setTimeout(resolve, 120))
    }
    if (modals.length !== 1) return failed('modal_cardinality')
    const modal = modals[0]
    const roots = visibleAll(modal, '.resume-detail')
    if (roots.length !== 1) return failed('modal_cardinality')
    const root = roots[0]

    const names = visibleAll(root, '.resume-basic-new__name').map((element) => clean(element.textContent))
      .filter(Boolean)
    const meta = visibleAll(root, '.resume-basic-new__meta-item').map((element) => clean(element.textContent))
      .filter(Boolean)
    const semanticCounts = {
      age: meta.filter((value) => /\d{1,3}\s*岁/u.test(value)).length,
      work: meta.filter((value) => !/岁/u.test(value) && /(?:\d+\s*年|应届|无经验)/u.test(value)).length,
      education: meta.filter((value) => /(?:博士|硕士|本科|大专|高中|中专|技校|学历)/u.test(value)).length,
    }
    if (names.length !== 1 || meta.length < 3 || semanticCounts.age !== 1 ||
        semanticCounts.work !== 1 || semanticCounts.education !== 1) {
      return failed('basic_unresolved')
    }
    let otherIndex = 0
    const basicLabel = (value: string): string => {
      if (/\d{1,3}\s*岁/u.test(value)) return '年龄'
      if (!/岁/u.test(value) && /(?:\d+\s*年|应届|无经验)/u.test(value)) return '工作经验'
      if (/(?:博士|硕士|本科|大专|高中|中专|技校|学历)/u.test(value)) return '最高学历'
      if (/(?:在校|离校|在职|离职|求职|看看机会|暂无工作|正在找工作)/u.test(value)) return '求职状态'
      if (/户口/u.test(value)) return '户口地'
      if (/(?:现居|居住)/u.test(value)) return '现居地'
      otherIndex += 1
      return `其他信息${otherIndex}`
    }
    const basic: CandidateResumeLabelValue[] = [
      { label: '姓名', value: names[0] },
      ...meta.map((value) => ({ label: basicLabel(value), value })),
    ]

    const purposes = visibleAll(root, '.new-resume-purposes__item')
    if (purposes.length === 0) return failed('expectations_unresolved')
    const expectations: CandidateResumeLabelValue[] = []
    for (const purpose of purposes) {
      const fields = [
        ['期望地点', '.new-resume-purposes__item-city'],
        ['期望职位', '.new-resume-purposes__item-type'],
        ['期望薪资', '.new-resume-purposes__item-salary'],
      ] as const
      for (const [label, selector] of fields) {
        const values = visibleAll(purpose, selector).map((element) => clean(element.textContent)).filter(Boolean)
        if (values.length !== 1) return failed('expectations_unresolved')
        expectations.push({ label, value: values[0] })
      }
    }

    const sectionText = (selector: string): string | null => {
      const sections = visibleAll(root, selector)
      if (sections.length !== 1) return null
      const items = visibleAll(sections[0], `${selector}__item`)
      if (items.length > 0) {
        const values = items.map(blockText).filter(Boolean)
        return values.length === items.length ? values.join('\n\n') : null
      }
      return null
    }
    const workExperiences = sectionText('.new-work-experiences')
    if (workExperiences === null) return failed('work_unresolved')
    const education = sectionText('.new-education-experiences')
    if (education === null) return failed('education_unresolved')

    const selfStructures = visibleAll(root,
      '.resume-section-self-evaluation, .new-self-evaluation, .new-resume-self-evaluation')
    const selfHeadings = visibleAll(root, 'h1, h2, h3, h4, h5, b, .resume-section-new__title')
      .filter((element) => ['自我评价', '自我描述'].includes(clean(element.textContent)))
    let selfEvaluation = ''
    if (selfStructures.length > 1 || selfHeadings.length > 1 ||
        (selfStructures.length === 0 && selfHeadings.length !== 0)) {
      return failed('self_evaluation_unresolved')
    }
    if (selfStructures.length === 1) {
      const lines = blockText(selfStructures[0]).split('\n')
        .filter((line) => line !== '自我评价' && line !== '自我描述')
      if (lines.length === 0) return failed('self_evaluation_unresolved')
      selfEvaluation = lines.join('\n')
    }

    if (!routeMatches() || !targetMatches() || visibleAll(document, '.im-session-detail').length !== 1 ||
        visibleAll(document, '.new-shortcut-resume__modal').length !== 1) {
      return failed('target_changed')
    }
    const data: ZhilianResumeData = {
      conversationRef,
      platformUserRef,
      observedAt: Date.now(),
      basic,
      expectations,
      selfEvaluation,
      education,
      workExperiences,
    }
    if (new TextEncoder().encode(JSON.stringify(data)).length > 65_536) return failed('payload_limit')
    return { status: 'ready', data }
  } catch {
    return failed('unexpected')
  }
}

// 冒烟冲刺的推荐页采集 evaluator。它在同一个 MAIN task 内完成“稳定来源卡
// -> 打开详情 -> resumeNumber 瞬时连接 -> 五分区 -> 再次绑定复核”，返回值中
// 永不包含 resumeNumber；列表顺序只决定本轮先读谁，不承担任何身份语义。
async function mainReadSourcingResume(
  excludePlatformUserRefs: string[],
): Promise<MainSourcingResumeResult> {
  type AnyRecord = Record<string, unknown>
  interface SourceSnapshot {
    item: HTMLElement
    owner: AnyRecord
    source: AnyRecord
    platformUserRef: string
    resumeNumber: string
  }
  type PositionSnapshot =
    | { status: 'ready'; positionRef: string; positionTitle: string | null }
    | { status: 'failed'; reason: MainSourcingResumeFailureReason }

  const asRecord = (value: unknown): AnyRecord | null =>
    value !== null && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const opaque = (value: unknown): string => {
    if (typeof value === 'string') return value.trim()
    if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
    return ''
  }
  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/\u00a0/gu, ' ')
    .replace(/\s+/gu, ' ')
    .trim()
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const visibleAll = (root: ParentNode, selector: string): HTMLElement[] =>
    Array.from(root.querySelectorAll<HTMLElement>(selector)).filter(visible)
  const blockText = (element: HTMLElement): string => {
    const raw = typeof element.innerText === 'string' ? element.innerText : element.textContent ?? ''
    return raw.normalize('NFC').replace(/\u00a0/gu, ' ').split(/\n+/u)
      .map((line) => line.replace(/[\t ]+/gu, ' ').trim()).filter(Boolean).join('\n')
  }
  const failed = (
    reason: MainSourcingResumeFailureReason,
    failedPlatformUserRef?: string,
  ): MainSourcingResumeFailed => ({
    status: 'failed',
    reason,
    ...(failedPlatformUserRef ? { failedPlatformUserRef } : {}),
  })
  const route = (): URL | null => {
    try {
      const current = new URL(location.href)
      return current.pathname.startsWith('/app/recommend') ? current : null
    } catch {
      return null
    }
  }
  const collectSources = (): SourceSnapshot[] | MainSourcingResumeFailed => {
    const items = visibleAll(document, '.recommend-list__left div[role="listitem"]')
    if (items.length === 0) return failed('list_source_unavailable')
    const sources: SourceSnapshot[] = []
    for (const item of items) {
      const owner = asRecord((item as HTMLElement & { __vue__?: unknown }).__vue__)
      const props = asRecord(owner?._props)
      const source = asRecord(props?.source)
      if (!owner || !source) return failed('list_source_unavailable')
      const platformUserRef = opaque(source.userMasterId)
      if (!platformUserRef) return failed('candidate_identity_unavailable')
      const resumeNumber = opaque(source.resumeNumber)
      if (!resumeNumber) return failed('detail_binding_ambiguous')
      sources.push({ item, owner, source, platformUserRef, resumeNumber })
    }
    const identities = new Set<string>()
    for (const source of sources) {
      if (identities.has(source.platformUserRef)) return failed('candidate_identity_duplicated')
      identities.add(source.platformUserRef)
    }
    return sources
  }
  const readPosition = (source: SourceSnapshot): PositionSnapshot => {
    const currentRoute = route()
    if (!currentRoute) return failed('route_changed')
    const routeJobNumber = opaque(currentRoute.searchParams.get('jobNumber'))
    const root = asRecord(source.owner.$root)
    const ownerRoute = asRecord(root?._route)
    const ownerQuery = asRecord(ownerRoute?.query)
    const store = asRecord(source.owner.$store)
    const state = asRecord(store?.state)
    const talent = asRecord(state?.talent)
    const activeJob = asRecord(talent?.activeJob)
    const routedJobNumber = opaque(ownerQuery?.jobNumber)
    const activeJobNumber = opaque(activeJob?.jobNumber)
    if (!routeJobNumber || !routedJobNumber || !activeJobNumber) {
      return failed('position_identity_unavailable')
    }
    if (routeJobNumber !== routedJobNumber || routeJobNumber !== activeJobNumber) {
      return failed('position_identity_mismatch')
    }
    const visibleJobTitles = visibleAll(document,
      '.job-pane__item--active .job-pane__item-job-title')
      .map((element) => clean(element.textContent)).filter(Boolean)
    if (visibleJobTitles.length > 1) return failed('position_title_ambiguous')
    const visibleJobTitle = visibleJobTitles[0] ?? ''
    const activeJobTitle = clean(activeJob?.jobTitle)
    if (visibleJobTitle && activeJobTitle && visibleJobTitle !== activeJobTitle) {
      return failed('position_title_mismatch')
    }
    const title = visibleJobTitle || activeJobTitle
    return {
      status: 'ready',
      positionRef: routeJobNumber,
      positionTitle: title && title.length <= 256 ? title : null,
    }
  }
  const contactState = (item: HTMLElement): ZhilianSourcingResumeData['contactState'] => {
    if (blockText(item).includes('同事聊过')) return 'established'
    const buttons = visibleAll(item, 'button[type="button"]')
      .filter((button) => clean(button.textContent) === '打招呼') as HTMLButtonElement[]
    return buttons.length === 1 && !buttons[0].disabled ? 'unestablished' : 'unknown'
  }
  const closeOpenedDetail = async (): Promise<MainSourcingResumeFailed | null> => {
    let opened = visibleAll(document, '.new-shortcut-resume__modal')
    if (opened.length !== 1) return failed(opened.length === 0 ? 'close_unavailable' : 'modal_cardinality')
    const closeButtons = visibleAll(opened[0], '.new-shortcut-resume__close')
    if (closeButtons.length !== 1) return failed('close_unavailable')
    closeButtons[0].click()
    const closeUntil = Date.now() + 6_000
    while (Date.now() < closeUntil) {
      opened = visibleAll(document, '.new-shortcut-resume__modal')
      const latestRoute = route()
      if (opened.length === 0 && latestRoute && !latestRoute.searchParams.get('resumeNumber')) return null
      await new Promise((resolve) => setTimeout(resolve, 120))
    }
    return failed('close_unavailable')
  }

  try {
    if (!route()) return failed('route_changed')
    const excluded = new Set(excludePlatformUserRefs)
    let sources = collectSources()
    if (!Array.isArray(sources)) return sources
    let target = sources.find((source) => !excluded.has(source.platformUserRef))
    if (!target) return failed('no_candidate')
    const initialPosition = readPosition(target)
    if (initialPosition.status === 'failed') return initialPosition
    const initialContactState = contactState(target.item)

    let modals = visibleAll(document, '.new-shortcut-resume__modal')
    if (modals.length > 1) return failed('modal_cardinality')
    const currentRoute = route()
    if (!currentRoute) return failed('route_changed')
    const openedResumeNumber = opaque(currentRoute.searchParams.get('resumeNumber'))
    let targetAlreadyOpen = false
    if (modals.length === 1) {
      const openedMatches = sources.filter((source) => source.resumeNumber === openedResumeNumber)
      if (!openedResumeNumber || openedMatches.length !== 1) return failed('stale_detail_ambiguous')
      targetAlreadyOpen = openedMatches[0].platformUserRef === target.platformUserRef
      if (!targetAlreadyOpen) {
        const closeButtons = visibleAll(modals[0], '.new-shortcut-resume__close')
        if (closeButtons.length !== 1) return failed('close_unavailable')
        closeButtons[0].click()
        const closeUntil = Date.now() + 6_000
        while (Date.now() < closeUntil) {
          modals = visibleAll(document, '.new-shortcut-resume__modal')
          const latestRoute = route()
          if (modals.length === 0 && latestRoute && !latestRoute.searchParams.get('resumeNumber')) break
          await new Promise((resolve) => setTimeout(resolve, 120))
        }
        if (modals.length !== 0) return failed('modal_cardinality')
        const afterCloseRoute = route()
        if (!afterCloseRoute || afterCloseRoute.searchParams.get('resumeNumber')) {
          return failed('stale_detail_ambiguous')
        }
        sources = collectSources()
        if (!Array.isArray(sources)) return sources
        const rebound = sources.filter((source) => source.platformUserRef === target?.platformUserRef)
        if (rebound.length !== 1 || rebound[0].resumeNumber !== target.resumeNumber) {
          return failed('target_changed')
        }
        target = rebound[0]
      }
    } else if (openedResumeNumber) {
      return failed('stale_detail_ambiguous')
    }

    if (!targetAlreadyOpen) {
      const entries = visibleAll(target.item, '.resume-item__content')
      if (entries.length !== 1) return failed('entry_cardinality')
      entries[0].click()
      const openUntil = Date.now() + 6_000
      while (Date.now() < openUntil) {
        modals = visibleAll(document, '.new-shortcut-resume__modal')
        if (modals.length !== 0) break
        await new Promise((resolve) => setTimeout(resolve, 120))
      }
    }
    if (modals.length !== 1) return failed('modal_cardinality')
    const modal = modals[0]
    const expectedTarget = target

    const evaluateOpenedDetail = (): MainSourcingResumeResult => {
      const boundRoute = route()
      if (!boundRoute) return failed('route_changed')
      const boundResumeNumber = opaque(boundRoute.searchParams.get('resumeNumber'))
      if (boundResumeNumber !== expectedTarget.resumeNumber) return failed('detail_binding_ambiguous')
      sources = collectSources()
      if (!Array.isArray(sources)) return sources
      const rebound = sources.filter((source) => source.resumeNumber === boundResumeNumber)
      if (rebound.length !== 1 || rebound[0].platformUserRef !== expectedTarget.platformUserRef) {
        return failed('target_changed')
      }
      target = rebound[0]
      const finalPosition = readPosition(target)
      if (finalPosition.status === 'failed') return finalPosition
      if (finalPosition.positionRef !== initialPosition.positionRef ||
          finalPosition.positionTitle !== initialPosition.positionTitle ||
          contactState(target.item) !== initialContactState) {
        return failed('target_changed')
      }

      const names = visibleAll(modal, '.resume-basic-new__name')
        .map((element) => clean(element.textContent)).filter(Boolean)
      const meta = visibleAll(modal, '.resume-basic-new__meta-item')
        .map((element) => clean(element.textContent)).filter(Boolean)
      const semanticCounts = {
        age: meta.filter((value) => /\d{1,3}\s*岁/u.test(value)).length,
        work: meta.filter((value) => !/岁/u.test(value) && /(?:\d+\s*年|应届|无经验)/u.test(value)).length,
        education: meta.filter((value) => /(?:博士|硕士|本科|大专|高中|中专|技校|学历)/u.test(value)).length,
      }
      if (names.length !== 1 || meta.length < 3 || semanticCounts.age !== 1 ||
          semanticCounts.work !== 1 || semanticCounts.education !== 1) {
        return failed('basic_unresolved', target.platformUserRef)
      }
      let otherIndex = 0
      const basicLabel = (value: string): string => {
        if (/\d{1,3}\s*岁/u.test(value)) return '年龄'
        if (!/岁/u.test(value) && /(?:\d+\s*年|应届|无经验)/u.test(value)) return '工作经验'
        if (/(?:博士|硕士|本科|大专|高中|中专|技校|学历)/u.test(value)) return '最高学历'
        if (/(?:在校|离校|在职|离职|求职|看看机会|暂无工作|正在找工作)/u.test(value)) return '求职状态'
        if (/户口/u.test(value)) return '户口地'
        if (/(?:现居|居住)/u.test(value)) return '现居地'
        otherIndex += 1
        return `其他信息${otherIndex}`
      }
      const basic: CandidateResumeLabelValue[] = [
        { label: '姓名', value: names[0] },
        ...meta.map((value) => ({ label: basicLabel(value), value })),
      ]

      const purposeSections = visibleAll(modal, '.resume-section-purposes')
      if (purposeSections.length !== 1) return failed('expectations_unresolved', target.platformUserRef)
      const purposeText = blockText(purposeSections[0])
      if (!purposeText) return failed('expectations_unresolved', target.platformUserRef)
      const expectations: CandidateResumeLabelValue[] = [{ label: '求职期望', value: purposeText }]

      const workSections = visibleAll(modal, '.new-work-experiences')
      if (workSections.length !== 1) return failed('work_unresolved', target.platformUserRef)
      const workExperiences = blockText(workSections[0])
      if (!workExperiences) return failed('work_unresolved', target.platformUserRef)
      const educationSections = visibleAll(modal, '.new-education-experiences')
      if (educationSections.length !== 1) return failed('education_unresolved', target.platformUserRef)
      const education = blockText(educationSections[0])
      if (!education) return failed('education_unresolved', target.platformUserRef)

      const selfSections = visibleAll(modal,
        '.resume-section-self-evaluation, .new-self-evaluation, .new-resume-self-evaluation')
      if (selfSections.length > 1) return failed('self_evaluation_unresolved', target.platformUserRef)
      let selfEvaluation = ''
      if (selfSections.length === 1) {
        const lines = blockText(selfSections[0]).split('\n')
          .filter((line) => line !== '自我评价' && line !== '自我描述')
        if (lines.length === 0) return failed('self_evaluation_unresolved', target.platformUserRef)
        selfEvaluation = lines.join('\n')
      }

      if (visibleAll(document, '.new-shortcut-resume__modal').length !== 1) {
        return failed('target_changed')
      }
      const data: ZhilianSourcingResumeData = {
        platformUserRef: target.platformUserRef,
        displayName: names[0].length <= 256 ? names[0] : null,
        positionRef: finalPosition.positionRef,
        positionTitle: finalPosition.positionTitle,
        contactState: initialContactState,
        observedAt: Date.now(),
        basic,
        expectations,
        selfEvaluation,
        education,
        workExperiences,
      }
      if (new TextEncoder().encode(JSON.stringify(data)).length > 65_536) {
        return failed('payload_limit', target.platformUserRef)
      }
      return { status: 'ready', data }
    }
    let evaluated: MainSourcingResumeResult
    try {
      evaluated = evaluateOpenedDetail()
    } catch {
      evaluated = failed('unexpected')
    }
    const closeFailure = await closeOpenedDetail()
    return closeFailure ?? evaluated
  } catch {
    return failed('unexpected')
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

async function recommendTabs(): Promise<chrome.tabs.Tab[]> {
  return (await chrome.tabs.query({
    url: TAB_QUERY,
  })).filter((tab) => tab.id !== undefined && pageKindFromURL(tab.url) === 'recommend')
}

interface CurrentCandidateSnapshot {
  tab: chrome.tabs.Tab
  result: MainCurrentCandidateReady
}

async function uniqueCurrentCandidate(
  expectedPrincipalFingerprint: string | undefined,
): Promise<CurrentCandidateSnapshot> {
  const tabs = await recommendTabs()
  if (tabs.length === 0) {
    throw new ZhilianPlatformError(
      'CTX_NOT_READY',
      '请在 Chrome 中打开智联推荐候选人详情',
      'manualOnly',
      'pageAbsent',
    )
  }

  const ready: CurrentCandidateSnapshot[] = []
  for (const tab of tabs) {
    if (tab.id === undefined || tab.status !== 'complete') {
      throw new ZhilianPlatformError(
        'CTX_NOT_READY',
        '当前智联推荐页尚未就绪',
        'afterRecovery',
        'pageBroken',
      )
    }
    assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
    const result = await runMain(tab.id, mainReadCurrentCandidate, [])
    if (!validCurrentCandidateResult(result)) {
      throw new ZhilianPlatformError(
        'ELEMENT_UNRESOLVED',
        '当前候选人或职位无法唯一确证',
        'manualOnly',
      )
    }
    if (result.status === 'ready') {
      ready.push({ tab, result })
    } else if (result.reason !== 'detail_absent') {
      throw new ZhilianPlatformError(
        'ELEMENT_UNRESOLVED',
        '当前候选人或职位无法唯一确证',
        'manualOnly',
      )
    }
  }
  if (ready.length !== 1) {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      '当前候选人或职位无法唯一确证',
      'manualOnly',
    )
  }
  return ready[0]
}

function validCurrentCandidateResult(value: unknown): value is MainCurrentCandidateResult {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  if (record.status === 'failed') {
    return typeof record.reason === 'string' &&
      (MAIN_CURRENT_CANDIDATE_FAILURE_REASONS as readonly string[]).includes(record.reason)
  }
  if (record.status !== 'ready' || !record.data ||
      typeof record.data !== 'object' || Array.isArray(record.data)) return false
  const data = record.data as Record<string, unknown>
  const nullableText = (candidate: unknown): boolean => candidate === null ||
    (typeof candidate === 'string' && candidate.length >= 1 && candidate.length <= 256 &&
      new TextEncoder().encode(candidate).length <= 1024)
  const opaqueRef = (candidate: unknown): boolean => typeof candidate === 'string' &&
    candidate.length >= 1 && candidate.length <= 512 && new TextEncoder().encode(candidate).length <= 2048
  return opaqueRef(data.platformUserRef) && opaqueRef(data.positionRef) &&
    nullableText(data.displayName) && nullableText(data.positionTitle) &&
    (data.contactState === 'unestablished' || data.contactState === 'established' ||
      data.contactState === 'unknown')
}

export async function readZhilianCurrentCandidate(
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianCurrentCandidate> {
  ctx.checkpoint()
  await ctx.progress('读取真人当前打开的智联候选人', 10)
  const current = await uniqueCurrentCandidate(expectedPrincipalFingerprint)
  ctx.checkpoint()

  // 返回任何候选人数据前，再次从全部推荐页中解析唯一详情并确证账号；
  // 其他窗口可以停留在没有详情的推荐页，但第二个详情或目标变化都失败。
  const latest = await uniqueCurrentCandidate(expectedPrincipalFingerprint)
  if (latest.tab.id !== current.tab.id ||
      latest.result.data.platformUserRef !== current.result.data.platformUserRef ||
      latest.result.data.positionRef !== current.result.data.positionRef) {
    throw new ZhilianPlatformError(
      'CTX_LOST_DURING_EXEC',
      '读取期间当前智联推荐页发生变化',
      'manualOnly',
    )
  }
  await ctx.progress('当前智联候选人读取完成', 100)
  return latest.result.data
}

// 三个 phase 由同一份 MAIN-world evaluator 承担。prepare 只执行批次 0 已证明
// 可逆的“打开弹层→选择统一招呼→展开编辑区”；preflight 与 commit 字面复用
// evaluateFinal。commit 的最后一份绿色结果之后立即调用一次标准 click，不再读页面。
async function mainSendGreetingOnce(
  platformUserRef: string,
  positionRef: string,
  text: string,
  expectedPrincipalFingerprint: string,
  irreversibleNotAfterMs: number,
  expectedOwnedDraft: string,
  phase: MainGreetingPhase,
): Promise<MainGreetingActionResult> {
  type AnyRecord = Record<string, unknown>
  type IntrinsicClick = (this: HTMLElement) => void
  type TextareaValueSetter = (this: HTMLTextAreaElement, value: string) => void
  const w = window as unknown as AnyRecord
  const asRecord = (value: unknown): AnyRecord | null =>
    value !== null && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/\u00a0/gu, ' ')
    .replace(/\s+/gu, ' ')
    .trim()
  const opaque = (value: unknown): string => {
    if (typeof value === 'string') return value.trim()
    if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
    return ''
  }
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const failed = (reason: MainGreetingFailureReason): MainGreetingActionResult => ({ status: 'failed', reason })
  const rotateRight = (value: number, count: number): number =>
    (value >>> count) | (value << (32 - count))
  const digest = (value: string): string => {
    const input = new TextEncoder().encode(value)
    const totalLength = Math.ceil((input.length + 9) / 64) * 64
    const padded = new Uint8Array(totalLength)
    padded.set(input)
    padded[input.length] = 0x80
    const bitLength = input.length * 8
    const view = new DataView(padded.buffer)
    view.setUint32(totalLength - 8, Math.floor(bitLength / 0x100000000), false)
    view.setUint32(totalLength - 4, bitLength >>> 0, false)
    const constants = new Uint32Array([
      0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
      0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
      0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
      0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
      0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
      0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
      0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
      0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
    ])
    const state = new Uint32Array([
      0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
      0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
    ])
    const words = new Uint32Array(64)
    for (let offset = 0; offset < totalLength; offset += 64) {
      for (let index = 0; index < 16; index += 1) words[index] = view.getUint32(offset + index * 4, false)
      for (let index = 16; index < 64; index += 1) {
        const left = words[index - 15]
        const right = words[index - 2]
        const sigma0 = rotateRight(left, 7) ^ rotateRight(left, 18) ^ (left >>> 3)
        const sigma1 = rotateRight(right, 17) ^ rotateRight(right, 19) ^ (right >>> 10)
        words[index] = (words[index - 16] + sigma0 + words[index - 7] + sigma1) >>> 0
      }
      let a = state[0]
      let b = state[1]
      let c = state[2]
      let d = state[3]
      let e = state[4]
      let f = state[5]
      let g = state[6]
      let h = state[7]
      for (let index = 0; index < 64; index += 1) {
        const sum1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25)
        const choose = (e & f) ^ (~e & g)
        const temp1 = (h + sum1 + choose + constants[index] + words[index]) >>> 0
        const sum0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22)
        const majority = (a & b) ^ (a & c) ^ (b & c)
        const temp2 = (sum0 + majority) >>> 0
        h = g
        g = f
        f = e
        e = (d + temp1) >>> 0
        d = c
        c = b
        b = a
        a = (temp1 + temp2) >>> 0
      }
      state[0] = (state[0] + a) >>> 0
      state[1] = (state[1] + b) >>> 0
      state[2] = (state[2] + c) >>> 0
      state[3] = (state[3] + d) >>> 0
      state[4] = (state[4] + e) >>> 0
      state[5] = (state[5] + f) >>> 0
      state[6] = (state[6] + g) >>> 0
      state[7] = (state[7] + h) >>> 0
    }
    return Array.from(state, (word) => word.toString(16).padStart(8, '0')).join('')
  }
  const readInitialState = (): AnyRecord => {
    const source = Array.from(document.scripts ?? [])
      .map((script) => script.textContent ?? '')
      .find((candidate) => candidate.includes('__INITIAL_STATE__='))
    if (!source) return {}
    const candidate = source.slice(source.indexOf('__INITIAL_STATE__=') + '__INITIAL_STATE__='.length).trim()
    const start = candidate.indexOf('{')
    let depth = 0
    let quoted = false
    let escaped = false
    for (let index = start; index >= 0 && index < candidate.length; index += 1) {
      const char = candidate[index]
      if (quoted) {
        if (escaped) escaped = false
        else if (char === '\\') escaped = true
        else if (char === '"') quoted = false
        continue
      }
      if (char === '"') quoted = true
      else if (char === '{') depth += 1
      else if (char === '}' && --depth === 0) {
        try { return asRecord(JSON.parse(candidate.slice(start, index + 1))) ?? {} } catch { return {} }
      }
    }
    return {}
  }
  const initial = readInitialState()
  const normalizeIdentityPart = (value: unknown): string | null => {
    if (typeof value === 'string') return value.trim() || null
    if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
    return null
  }
  const principalMatches = (): boolean => {
    const initialSession = asRecord(asRecord(initial.session)?.session)
    const runtimeSession = asRecord(w.$session)
    const session: AnyRecord = { ...(initialSession ?? {}), ...(runtimeSession ?? {}) }
    const staff: AnyRecord = {
      ...(asRecord(initialSession?.staff) ?? {}),
      ...(asRecord(runtimeSession?.staff) ?? {}),
    }
    const staffID = normalizeIdentityPart(staff.staffId)
    const organizationID = normalizeIdentityPart(asRecord(session.org)?.orgId) ??
      normalizeIdentityPart(asRecord(asRecord(initial.personal)?.imUserInfo)?.rootCompanyId)
    const loginPoint = normalizeIdentityPart(staff.defaultLoginPoint)
    if (session.isLoggedIn !== true || !staffID || !organizationID || !loginPoint) return false
    const pieces = ['zhilian-principal-v2', staffID, organizationID, loginPoint]
    const canonical = pieces.map((piece) => `${new TextEncoder().encode(piece).length}:${piece}`).join('|')
    return digest(canonical) === expectedPrincipalFingerprint
  }
  interface TargetSurface {
    detail: HTMLElement
    openButton: HTMLButtonElement
  }
  const targetSurface = (): TargetSurface | null => {
    let route: URL
    try { route = new URL(location.href) } catch { return null }
    if (!route.pathname.startsWith('/app/recommend')) return null
    const routeResumeNumber = opaque(route.searchParams.get('resumeNumber'))
    const routeJobNumber = opaque(route.searchParams.get('jobNumber'))
    if (!routeResumeNumber || routeJobNumber !== positionRef) return null
    const details = Array.from(document.querySelectorAll<HTMLElement>('.new-shortcut-resume__modal')).filter(visible)
    if (details.length !== 1) return null
    const detail = details[0]
    const listItems = Array.from(document.querySelectorAll<HTMLElement>('[role="listitem"]'))
    if (listItems.length === 0) return null
    const sources: Array<{ owner: AnyRecord; source: AnyRecord }> = []
    for (const item of listItems) {
      const owner = asRecord((item as HTMLElement & { __vue__?: unknown }).__vue__)
      const source = asRecord(asRecord(owner?._props)?.source)
      if (!owner || !source) return null
      sources.push({ owner, source })
    }
    const matches = sources.filter(({ source }) => opaque(source.resumeNumber) === routeResumeNumber)
    if (matches.length !== 1 || opaque(matches[0].source.userMasterId) !== platformUserRef) return null
    if (sources.filter(({ source }) => opaque(source.userMasterId) === platformUserRef).length !== 1) return null
    const root = asRecord(matches[0].owner.$root)
    const routedJobNumber = opaque(asRecord(asRecord(root?._route)?.query)?.jobNumber)
    const activeJob = asRecord(asRecord(asRecord(matches[0].owner.$store)?.state)?.talent)
    const activeJobNumber = opaque(asRecord(activeJob?.activeJob)?.jobNumber)
    if (routedJobNumber !== positionRef || activeJobNumber !== positionRef) return null
    const buttons = Array.from(detail.querySelectorAll<HTMLButtonElement>('button[type="button"]'))
      .filter((button) => visible(button) && clean(button.textContent) === '打招呼')
    if (buttons.length !== 1) return null
    const openButton = buttons[0]
    if (openButton.form !== null && openButton.type !== 'button') return null
    return { detail, openButton }
  }
  const greetingModals = (): HTMLElement[] =>
    Array.from(document.querySelectorAll<HTMLElement>('.ai-greeting-modal')).filter(visible)
  const customOptionOf = (modal: HTMLElement): HTMLElement | null => {
    const options = Array.from(modal.querySelectorAll<HTMLElement>('.ai-greeting-modal__option')).filter(visible)
    if (options.length !== 2) return null
    const custom = options.filter((option) => option.querySelector('.ai-greeting-modal__ai-icon') === null)
    return custom.length === 1 ? custom[0] : null
  }
  const customSelected = (option: HTMLElement): boolean =>
    option.classList.contains('is-selected') ||
    option.querySelector('.ai-greeting-modal__radio.is-checked') !== null
  const defaultSettingState = (modal: HTMLElement): 'unchecked' | 'checked' | 'unresolved' => {
    const controls = Array.from(modal.querySelectorAll<HTMLElement>('.km-checkbox')).filter((control) => {
      const label = clean(control.textContent)
      return label.includes('设置为默认') || label.includes('默认使用该统一招呼语')
    })
    if (controls.length !== 1) return 'unresolved'
    const control = controls[0]
    const inputs = Array.from(control.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))
    if (inputs.length > 1) return 'unresolved'
    const checked = inputs[0]?.checked === true || control.getAttribute('aria-checked') === 'true' ||
      control.classList.contains('km-checkbox--checked') ||
      control.querySelector('.km-checkbox__icon--checked') !== null
    return checked ? 'checked' : 'unchecked'
  }
  const sleep = (delayMs: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, delayMs))
  const waitFor = async (predicate: () => boolean): Promise<boolean> => {
    for (let round = 0; round < 20; round += 1) {
      if (predicate()) return true
      await sleep(50)
    }
    return false
  }

  if (!Number.isFinite(irreversibleNotAfterMs) || Date.now() > irreversibleNotAfterMs) {
    return failed('action_window_elapsed')
  }
  if (!principalMatches()) return failed('identity_changed')
  if (!clean(text)) return failed('input_rejected')

  if (phase === 'prepare') {
    if (greetingModals().length !== 0) return failed('existing_editor')
    const target = targetSurface()
    if (!target) return failed('two_step_surface_unavailable')
    if (Date.now() > irreversibleNotAfterMs || !principalMatches()) {
      return failed(Date.now() > irreversibleNotAfterMs ? 'action_window_elapsed' : 'identity_changed')
    }
    const invokeOpen = Function.prototype.call.bind(
      HTMLElement.prototype.click as IntrinsicClick,
      target.openButton,
    )
    // 这里之后才允许观察弹层；本轮不再调用“打招呼”入口。
    invokeOpen()
    if (!await waitFor(() => greetingModals().length === 1)) return failed('editor_not_opened')
    if (!principalMatches() || !targetSurface()) return failed('target_changed')
    let modal = greetingModals()[0]
    let custom = customOptionOf(modal)
    if (!custom) return failed('custom_option_unavailable')
    if (!customSelected(custom)) {
      const invokeOption = Function.prototype.call.bind(
        HTMLElement.prototype.click as IntrinsicClick,
        custom,
      )
      invokeOption()
      await sleep(50)
    }
    modal = greetingModals()[0]
    custom = modal ? customOptionOf(modal) : null
    if (!modal || !custom || !customSelected(custom)) return failed('custom_option_unavailable')
    let textareas = Array.from(custom.querySelectorAll<HTMLTextAreaElement>(
      '.ai-greeting-modal__edit-area textarea',
    )).filter(visible)
    if (textareas.length === 0) {
      const editIcons = Array.from(custom.querySelectorAll<HTMLElement>('.ai-greeting-modal__edit-icon'))
      if (editIcons.length !== 1) return failed('editor_unavailable')
      const invokeEdit = Function.prototype.call.bind(
        HTMLElement.prototype.click as IntrinsicClick,
        editIcons[0],
      )
      invokeEdit()
      await waitFor(() => {
        const currentModal = greetingModals()[0]
        const currentCustom = currentModal ? customOptionOf(currentModal) : null
        return currentCustom !== null && Array.from(currentCustom.querySelectorAll<HTMLTextAreaElement>(
          '.ai-greeting-modal__edit-area textarea',
        )).filter(visible).length === 1
      })
    }
    modal = greetingModals()[0]
    custom = modal ? customOptionOf(modal) : null
    textareas = custom
      ? Array.from(custom.querySelectorAll<HTMLTextAreaElement>('.ai-greeting-modal__edit-area textarea')).filter(visible)
      : []
    if (!modal || !custom || !customSelected(custom) || textareas.length !== 1) {
      return failed('editor_unavailable')
    }
    const defaultState = defaultSettingState(modal)
    if (defaultState === 'unresolved') return failed('default_setting_unresolved')
    if (defaultState === 'checked') return failed('default_setting_selected')
    if (!principalMatches() || !targetSurface()) return failed('target_changed')
    const ownedDraft = textareas[0].value
    if (new TextEncoder().encode(ownedDraft).length > 2048) return failed('editor_unavailable')
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set as
      TextareaValueSetter | undefined
    if (typeof setter !== 'function') return failed('editor_unavailable')
    const restoreOwnedDraft = (): void => {
      try { setter.call(textareas[0], ownedDraft) } catch { return }
      try {
        textareas[0].dispatchEvent(new InputEvent('input', {
          bubbles: true,
          inputType: 'insertText',
          data: ownedDraft,
        }))
        textareas[0].dispatchEvent(new Event('change', { bubbles: true }))
      } catch {
        // prepare 仍在 attempting 前；恢复 DOM 值后由人工处理页面事件异常。
      }
    }
    try {
      setter.call(textareas[0], text)
      textareas[0].dispatchEvent(new InputEvent('input', {
        bubbles: true,
        inputType: 'insertText',
        data: text,
      }))
      textareas[0].dispatchEvent(new Event('change', { bubbles: true }))
      const latestModal = greetingModals()[0]
      const latestCustom = latestModal ? customOptionOf(latestModal) : null
      const latestTextareas = latestCustom
        ? Array.from(latestCustom.querySelectorAll<HTMLTextAreaElement>(
            '.ai-greeting-modal__edit-area textarea',
          )).filter(visible)
        : []
      if (!latestModal || !latestCustom || !customSelected(latestCustom) || latestTextareas.length !== 1 ||
          latestTextareas[0].value !== text || defaultSettingState(latestModal) !== 'unchecked' ||
          !principalMatches() || !targetSurface()) {
        restoreOwnedDraft()
        return failed('input_rejected')
      }
      return { status: 'prepared' }
    } catch {
      restoreOwnedDraft()
      return failed('input_rejected')
    }
  }

  interface FinalSurface {
    textarea: HTMLTextAreaElement
    sendButton: HTMLButtonElement
    setter: TextareaValueSetter
    intrinsicClick: IntrinsicClick
  }
  type FinalEvaluation = { status: 'ready'; surface: FinalSurface } |
    { status: 'failed'; reason: MainGreetingFailureReason }
  const evaluateFinal = (expectedValue: string): FinalEvaluation => {
    if (Date.now() > irreversibleNotAfterMs) return { status: 'failed', reason: 'action_window_elapsed' }
    if (!principalMatches()) return { status: 'failed', reason: 'identity_changed' }
    if (!targetSurface()) return { status: 'failed', reason: 'relationship_changed' }
    const modals = greetingModals()
    if (modals.length !== 1) return { status: 'failed', reason: 'editor_changed' }
    const modal = modals[0]
    const custom = customOptionOf(modal)
    if (!custom || !customSelected(custom)) return { status: 'failed', reason: 'editor_changed' }
    const textareas = Array.from(custom.querySelectorAll<HTMLTextAreaElement>(
      '.ai-greeting-modal__edit-area textarea',
    )).filter(visible)
    if (textareas.length !== 1 || textareas[0].value !== expectedValue) {
      return { status: 'failed', reason: 'editor_changed' }
    }
    const defaultState = defaultSettingState(modal)
    if (defaultState === 'unresolved') return { status: 'failed', reason: 'default_setting_unresolved' }
    if (defaultState === 'checked') return { status: 'failed', reason: 'default_setting_selected' }
    const footers = Array.from(modal.querySelectorAll<HTMLElement>('.ai-greeting-modal__footer')).filter(visible)
    if (footers.length !== 1) return { status: 'failed', reason: 'send_surface_unavailable' }
    const sendButtons = Array.from(footers[0].querySelectorAll<HTMLButtonElement>('button[type="button"]'))
      .filter((button) => visible(button) && clean(button.textContent) === '发送')
    if (sendButtons.length !== 1) return { status: 'failed', reason: 'send_surface_unavailable' }
    const sendButton = sendButtons[0]
    if (sendButton.form !== null && sendButton.type !== 'button') {
      return { status: 'failed', reason: 'send_surface_unavailable' }
    }
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set as
      TextareaValueSetter | undefined
    const intrinsicClick = HTMLElement.prototype.click as IntrinsicClick | undefined
    if (typeof setter !== 'function' || typeof intrinsicClick !== 'function') {
      return { status: 'failed', reason: 'send_surface_unavailable' }
    }
    return { status: 'ready', surface: { textarea: textareas[0], sendButton, setter, intrinsicClick } }
  }

  const prepared = evaluateFinal(expectedOwnedDraft)
  if (prepared.status === 'failed') return prepared
  if (phase === 'preflight') return { status: 'ready' }
  const invokeSend = Function.prototype.call.bind(
    prepared.surface.intrinsicClick,
    prepared.surface.sendButton,
  )
  // attempting 后不再写/恢复输入；最终绿色后立即唯一 click，且不再读取页面。
  invokeSend()
  return { status: 'clicked' }
}

function validMainGreetingActionResult(value: unknown): value is MainGreetingActionResult {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  if (record.status === 'prepared') return true
  if (record.status === 'ready' || record.status === 'clicked') return true
  return record.status === 'failed' && typeof record.reason === 'string' &&
    (MAIN_GREETING_FAILURE_REASONS as readonly string[]).includes(record.reason)
}

function throwGreetingActionFailure(result: MainGreetingActionResult): never {
  if (result.status !== 'failed') {
    throw new ZhilianPlatformError('INTERNAL_HAND', '招呼动作返回了不可能的阶段', 'manualOnly')
  }
  if (result.reason === 'existing_editor' || result.reason === 'editor_changed' ||
      result.reason === 'default_setting_selected') {
    throw new ZhilianPlatformError('USER_ACTIVE', '检测到已有招呼弹窗、输入或默认设置，拒绝接管', 'manualOnly')
  }
  if (result.reason === 'identity_changed') {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '招呼前登录身份发生变化', 'manualOnly')
  }
  if (result.reason === 'action_window_elapsed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '招呼动作窗口已过，未执行最终发送', 'manualOnly')
  }
  if (result.reason === 'target_changed' || result.reason === 'relationship_changed') {
    throw new ZhilianPlatformError('GUARD_FAILED', '当前候选人、职位或关系状态发生变化', 'manualOnly')
  }
  if (result.reason === 'editor_not_opened') {
    throw new ZhilianPlatformError(
      'POSTCONDITION_UNCONFIRMED',
      '第一步后未确认进入已验证的两步招呼编辑器',
      'manualOnly',
      undefined,
      'possible',
    )
  }
  if (result.reason === 'two_step_surface_unavailable') {
    throw new ZhilianPlatformError('GUARD_FAILED', '当前页面不满足已验证的两步招呼动作表面', 'manualOnly')
  }
  throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '招呼编辑器或发送表面无法唯一确认', 'manualOnly')
}

export async function sendZhilianGreeting(
  args: ZhilianGreetingArgs,
  guards: ZhilianGreetingGuards,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianGreetingData> {
  if (!expectedPrincipalFingerprint) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  if (guards.expectUnestablished !== true) {
    throw new ZhilianPlatformError('GUARD_FAILED', '招呼命令缺少未建联条件写闸', 'manualOnly')
  }
  const normalizedText = normalizeZhilianMessageText(args.text)
  if (!normalizedText) throw new ZhilianPlatformError('GUARD_FAILED', '规范化后的招呼为空', 'manualOnly')
  const contentHash = await sha256Hex(normalizedText)
  const current = await uniqueCurrentCandidate(expectedPrincipalFingerprint)
  const tabId = current.tab.id
  if (tabId === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '当前智联推荐页缺少 id', 'afterRecovery', 'pageBroken')
  }
  if (current.result.data.platformUserRef !== args.platformUserRef ||
      current.result.data.positionRef !== args.positionRef ||
      current.result.data.contactState !== 'unestablished') {
    throw new ZhilianPlatformError('GUARD_FAILED', '当前候选人、职位或关系状态与招呼意图不一致', 'manualOnly')
  }
  const evaluatorBase = [
    args.platformUserRef,
    args.positionRef,
    args.text,
    expectedPrincipalFingerprint,
    ctx.irreversibleNotAfterMs,
  ] as const
  ctx.checkpoint()
  const preparedRaw = await runMain(tabId, mainSendGreetingOnce, [
    ...evaluatorBase,
    '',
    'prepare',
  ])
  if (!validMainGreetingActionResult(preparedRaw)) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '招呼准备阶段返回结构无效', 'manualOnly')
  }
  if (preparedRaw.status !== 'prepared') throwGreetingActionFailure(preparedRaw)
  const evaluatorArgs = [...evaluatorBase, args.text] as const

  ctx.checkpoint()
  const preflight = await runMain(tabId, mainSendGreetingOnce, [...evaluatorArgs, 'preflight'])
  if (!validMainGreetingActionResult(preflight)) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '招呼预检返回结构无效', 'manualOnly')
  }
  if (preflight.status !== 'ready') throwGreetingActionFailure(preflight)
  ctx.checkpoint()
  await ctx.beforeSideEffect()

  const action = await runMain(tabId, mainSendGreetingOnce, [...evaluatorArgs, 'commit'])
  if (!validMainGreetingActionResult(action)) {
    throw new ZhilianPlatformError(
      'CTX_LOST_DURING_EXEC',
      '最终招呼动作返回结构无效',
      'manualOnly',
      undefined,
      'possible',
    )
  }
  if (action.status !== 'clicked') throwGreetingActionFailure(action)

  for (let round = 0; round < 20; round += 1) {
    ctx.checkpoint()
    try {
      const latestTab = await chrome.tabs.get(tabId)
      if (pageKindFromURL(latestTab.url) === 'recommend') {
        assertExpectedPrincipal(await probeTab(latestTab), expectedPrincipalFingerprint)
        const observed = await runMain(tabId, mainReadCurrentCandidate, [])
        if (validCurrentCandidateResult(observed) && observed.status === 'ready' &&
            observed.data.platformUserRef === args.platformUserRef &&
            observed.data.positionRef === args.positionRef &&
            observed.data.contactState === 'established') {
          await ctx.progress('已确认同一候选人关系状态变为已建立', 100)
          return {
            platformUserRef: args.platformUserRef,
            positionRef: args.positionRef,
            contentHash,
            observedAt: Date.now(),
          }
        }
      }
    } catch {
      // 目标消失、读取异常或身份无法复核都只是未确认，不证明未发送。
    }
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  throw new ZhilianPlatformError(
    'POSTCONDITION_UNCONFIRMED',
    '最终发送只调用一次，但未确认同一候选人的关系状态变为已建立',
    'manualOnly',
    undefined,
    'possible',
  )
}

export async function readZhilianGreetingOutcome(
  args: ZhilianGreetingOutcomeArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianGreetingOutcomeData> {
  if (!expectedPrincipalFingerprint) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  ctx.checkpoint()
  // intrusive/idempotentReadReceipt 紧贴第一次平台读取设置取消安全点；不写 witness。
  await ctx.beforeSideEffect()
  try {
    const current = await uniqueCurrentCandidate(expectedPrincipalFingerprint)
    const data = current.result.data
    if (data.platformUserRef === args.platformUserRef &&
        data.positionRef === args.positionRef &&
        data.contactState === 'established') {
      return {
        confirmed: true,
        contentHash: args.contentHash,
        observedAt: Date.now(),
      }
    }
  } catch {
    // 页面不在、目标消失、账号无法复核或读取异常都不证明招呼失败。
  }
  return { confirmed: false, observedAt: Date.now() }
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
    const commandNavigation = beginCommandNavigation(tab.id, ctx.irreversibleNotAfterMs)
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
      const commandNavigation = beginCommandNavigation(tab.id, ctx.irreversibleNotAfterMs)
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

async function sendZhilianTab(conversationRef: string): Promise<chrome.tabs.Tab> {
  const candidates = (await chrome.tabs.query({ url: TAB_QUERY }))
    .filter((tab) => {
      if (tab.id === undefined || pageKindFromURL(tab.url) !== 'im') return false
      try {
        const url = new URL(tab.url ?? '')
        return url.pathname === '/app/im' && url.searchParams.get('sessionId') === conversationRef
      } catch {
        return false
      }
    })
    .sort((left, right) => {
      if (left.active !== right.active) return left.active ? -1 : 1
      const lastAccessed = (right.lastAccessed ?? 0) - (left.lastAccessed ?? 0)
      return lastAccessed !== 0 ? lastAccessed : (left.id ?? 0) - (right.id ?? 0)
    })
  const tab = candidates[0]
  if (!tab) {
    throw new ZhilianPlatformError(
      'CTX_NOT_READY',
      '人工打开的目标智联会话页面不存在',
      'afterRecovery',
      'pageAbsent',
    )
  }
  return tab
}

function throwResumeFailure(result: MainResumeFailed): never {
  if (result.reason === 'payload_limit') {
    throw new ZhilianPlatformError('PAYLOAD_LIMIT', '完整简历超过当前内联载荷上限', 'manualOnly')
  }
  if (result.reason === 'route_changed' || result.reason === 'target_changed' ||
      result.reason === 'session_unavailable') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '简历读取期间目标会话绑定无法确证', 'manualOnly')
  }
  throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '当前简历详情无法完整且唯一读取', 'manualOnly')
}

export async function readZhilianResume(
  args: ZhilianResumeArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianResumeData> {
  if (!args || typeof args.conversationRef !== 'string' || !args.conversationRef ||
      typeof args.platformUserRef !== 'string' || !args.platformUserRef) {
    throw new ZhilianPlatformError('GUARD_FAILED', '简历读取缺少目标会话或候选人引用', 'manualOnly')
  }
  ctx.checkpoint()
  const tab = await sendZhilianTab(args.conversationRef)
  if (tab.id === undefined || tab.status !== 'complete') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '目标智联会话页面尚未就绪', 'afterRecovery', 'pageBroken')
  }
  const initialProbe = await probeTab(tab)
  if (!initialProbe.contentScriptOk || initialProbe.pageKind !== 'im' || !initialProbe.surface?.imListVisible) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联 IM 页面感知通道尚未就绪', 'afterRecovery', 'contentScriptDead')
  }
  assertExpectedPrincipal(initialProbe, expectedPrincipalFingerprint)
  ctx.progress('核对简历详情入口', 10)

  ctx.checkpoint()
  const beforeClickTab = await chrome.tabs.get(tab.id)
  assertExpectedPrincipal(await probeTab(beforeClickTab), expectedPrincipalFingerprint)
  const result = await runMain(tab.id, mainReadCurrentResume, [
    args.conversationRef,
    args.platformUserRef,
  ])
  if (result.status === 'failed') throwResumeFailure(result)
  if (!result.data || result.data.conversationRef !== args.conversationRef ||
      result.data.platformUserRef !== args.platformUserRef) {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '简历读取结果的目标绑定无法确证', 'manualOnly')
  }
  if (jsonBytes(result.data) > 65_536) {
    throw new ZhilianPlatformError('PAYLOAD_LIMIT', '完整简历超过当前内联载荷上限', 'manualOnly')
  }
  if (validatePrimitiveData(PrimitiveName.CandidateReadResume, 1, result.data).length !== 0) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '完整简历结构不符合当前契约', 'manualOnly')
  }
  ctx.checkpoint()
  assertExpectedPrincipal(await probeTab(await chrome.tabs.get(tab.id)), expectedPrincipalFingerprint)
  ctx.progress('简历读取完成', 100)
  return result.data
}

function validSourcingResumeResult(value: unknown): value is MainSourcingResumeResult {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  if (record.status === 'failed') {
    const failedRefValid = record.failedPlatformUserRef === undefined ||
      (typeof record.failedPlatformUserRef === 'string' && record.failedPlatformUserRef.length >= 1 &&
        record.failedPlatformUserRef.length <= 512 &&
        new TextEncoder().encode(record.failedPlatformUserRef).length <= 2_048 &&
        typeof record.reason === 'string' &&
        (MAIN_SOURCING_RESUME_SKIPPABLE_FAILURE_REASONS as readonly string[]).includes(record.reason))
    return failedRefValid && typeof record.reason === 'string' &&
      (MAIN_SOURCING_RESUME_FAILURE_REASONS as readonly string[]).includes(record.reason)
  }
  return record.status === 'ready' && record.data !== null && typeof record.data === 'object' &&
    !Array.isArray(record.data) &&
    validatePrimitiveData(PrimitiveName.CandidateReadSourcingResume, 1, record.data).length === 0
}

async function activeSourcingTabs(): Promise<chrome.tabs.Tab[]> {
  return (await chrome.tabs.query({
    url: TAB_QUERY,
  })).filter((tab) => tab.id !== undefined && pageKindFromURL(tab.url) === 'recommend')
}

function throwSourcingResumeFailure(result: MainSourcingResumeFailed): never {
  if (result.reason === 'payload_limit') {
    throw new ZhilianPlatformError('PAYLOAD_LIMIT', '完整采集简历超过当前内联载荷上限', 'manualOnly')
  }
  if (result.reason === 'no_candidate') {
    throw new ZhilianPlatformError('TARGET_NOT_FOUND', '当前推荐页没有未采集候选人', 'afterRecovery')
  }
  if (result.reason === 'route_changed' || result.reason === 'target_changed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '采集期间当前推荐页或候选人发生变化', 'manualOnly')
  }
  throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '当前推荐候选人无法完整且唯一读取', 'manualOnly')
}

export async function readZhilianSourcingResume(
  args: ZhilianSourcingResumeArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianSourcingResumeData> {
  if (!args || !Array.isArray(args.excludePlatformUserRefs) ||
      args.excludePlatformUserRefs.length > 32 ||
      args.excludePlatformUserRefs.some((value) => typeof value !== 'string' || value.length === 0)) {
    throw new ZhilianPlatformError('GUARD_FAILED', '采集读取缺少合法的候选人排除列表', 'manualOnly')
  }
  ctx.checkpoint()
  const initialTabs = await activeSourcingTabs()
  if (initialTabs.length === 0) {
    throw new ZhilianPlatformError(
      'CTX_NOT_READY',
      '请保留一个已就绪的智联推荐页标签',
      'afterRecovery',
      'pageAbsent',
    )
  }
  if (initialTabs.length !== 1) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '智联推荐页标签无法唯一确定', 'manualOnly')
  }
  const tab = initialTabs[0]
  if (tab.id === undefined || tab.status !== 'complete') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '当前智联推荐页尚未就绪', 'afterRecovery', 'pageBroken')
  }
  const initialProbe = await probeTab(tab)
  if (initialProbe.pageKind !== 'recommend') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '当前智联页面不是推荐页', 'afterRecovery', 'pageAbsent')
  }
  assertExpectedPrincipal(initialProbe, expectedPrincipalFingerprint)
  ctx.progress('核对当前推荐页与登录身份', 10)

  ctx.checkpoint()
  const beforeActionTabs = await activeSourcingTabs()
  if (beforeActionTabs.length !== 1 || beforeActionTabs[0].id !== tab.id ||
      beforeActionTabs[0].status !== 'complete') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '采集动作前推荐页标签发生切换', 'manualOnly')
  }
  assertExpectedPrincipal(await probeTab(beforeActionTabs[0]), expectedPrincipalFingerprint)
  // 打开/切换详情最坏会产生幂等已查看记录；intrusive 命令在第一次页面动作前
  // 越过唯一 cancellation barrier，但不写 effectful witness。
  await ctx.beforeSideEffect()
  const commandExclusions = [...args.excludePlatformUserRefs]
  let result: MainSourcingResumeResult = { status: 'failed', reason: 'no_candidate' }
  let skippedCandidates = 0
  for (;;) {
    result = await runMain(tab.id, mainReadSourcingResume, [[...commandExclusions]])
    if (!validSourcingResumeResult(result)) {
      throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '推荐页采集结果结构不符合当前契约', 'manualOnly')
    }
    if (result.status === 'ready') break
    if (!result.failedPlatformUserRef) throwSourcingResumeFailure(result)
    if (skippedCandidates >= 5) throwSourcingResumeFailure(result)
    if (commandExclusions.includes(result.failedPlatformUserRef)) {
      throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '推荐页采集候选人跳过集合未推进', 'manualOnly')
    }
    commandExclusions.push(result.failedPlatformUserRef)
    skippedCandidates += 1
    ctx.checkpoint()
  }
  ctx.checkpoint()
  const latestTabs = await activeSourcingTabs()
  if (latestTabs.length !== 1 || latestTabs[0].id !== tab.id || latestTabs[0].status !== 'complete') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '采集期间活动推荐页发生切换', 'manualOnly')
  }
  assertExpectedPrincipal(await probeTab(latestTabs[0]), expectedPrincipalFingerprint)
  if (validatePrimitiveData(PrimitiveName.CandidateReadSourcingResume, 1, result.data).length !== 0 ||
      jsonBytes(result.data) > 65_536) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '完整采集简历结构不符合当前契约', 'manualOnly')
  }
  ctx.progress('推荐候选人采集完成', 100)
  return result.data
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
  // 当前 Chrome 版本在 MAIN-world Promise reject 时可能只给 InjectionResult.result=undefined，
  // 不附带 error。把本函数内部异常转换为脱敏、可序列化的哨兵，使外层能响亮区分
  // “执行上下文真的丢失”和“页面 API/数据结构不满足契约”；哨兵绝不携带平台原始值。
  let diagnosticStage = 'start'
  const execute = async (): Promise<MainThreadPageResult> => {
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
  diagnosticStage = 'resolve_session_runtime'
  let sessions = (engine && Array.isArray(engine.sessions) ? engine.sessions : []) as AnyRecord[]
  let session = sessions.find((item) => String(item.sessionId ?? '') === conversationRef)
  if (!session && engine && typeof engine.getSessions === 'function') {
    diagnosticStage = 'resolve_session_scan'
    const getSessions = engine.getSessions as (arg: AnyRecord) => Promise<unknown>
    const seenPages = new Set<string>()
    const readPage = async (pageNo: number): Promise<{ rows: AnyRecord[]; hasMore: boolean }> => {
      const loaded = await getSessions.call(engine, { pageNo, pageSize: 8, includeResume: true })
      if (!loaded || typeof loaded !== 'object' || Array.isArray(loaded) ||
          !Array.isArray((loaded as AnyRecord).curSessions)) {
        throw new Error('session_lookup_response_invalid')
      }
      const rawHasMore = (loaded as AnyRecord).hasMoreSession ?? (loaded as AnyRecord).hasMore
      if (typeof rawHasMore !== 'boolean') throw new Error('session_lookup_has_more_missing')
      return { rows: (loaded as AnyRecord).curSessions as AnyRecord[], hasMore: rawHasMore }
    }
    for (let pageNo = 1; pageNo <= 128; pageNo += 1) {
      const firstPage = await readPage(pageNo)
      const page = firstPage.rows
      const pageKey = `${String(page[0]?.sessionId ?? '')}\u001f${String(page[page.length - 1]?.sessionId ?? '')}\u001f${page.length}`
      if (seenPages.has(pageKey)) throw new Error('session_lookup_pagination_stalled')
      seenPages.add(pageKey)
      const firstMatches = page.filter((item) => String(item.sessionId ?? '') === conversationRef)
      if (firstMatches.length > 1) throw new Error('session_lookup_duplicate_target')
      if (firstMatches.length === 1) {
        // 命中页再采一次，只要求目标身份投影稳定；列表中其他会话的实时变化不能
        // 迫使我们猜目标，也不能把一次瞬态命中当成跨会话绑定证词。
        const secondPage = await readPage(pageNo)
        const secondMatches = secondPage.rows.filter((item) => String(item.sessionId ?? '') === conversationRef)
        if (secondMatches.length !== 1) throw new Error('session_lookup_target_unstable')
        const targetProjection = (item: AnyRecord): string => JSON.stringify([
          String(item.sessionId ?? ''),
          String(item.peerPartnerId ?? ''),
          item.scene ?? item.sessionType ?? null,
        ])
        if (targetProjection(firstMatches[0]) !== targetProjection(secondMatches[0])) {
          throw new Error('session_lookup_target_unstable')
        }
        session = secondMatches[0]
        break
      }
      if (!firstPage.hasMore) break
      if (pageNo === 128) throw new Error('session_lookup_page_limit')
    }
  }
  diagnosticStage = 'resolve_session_initial_state'
  if (!session) session = readInitialSessions().find((item) => String(item.sessionId ?? '') === conversationRef)
  if (!session) throw new Error('conversation_not_found')

  const target = String(session.peerPartnerId ?? '')
  if (!target) throw new Error('conversation_target_missing')
  let history: unknown = null
  let historyFailure: 'unavailable' | 'rejected' | 'shape' | null = null
  if (engine && typeof engine.getHistoryMsgs === 'function' && target) {
    diagnosticStage = 'read_history_api'
    const request: AnyRecord = { to: target, limit, asc: true }
    const scene = session.scene ?? session.sessionType
    if (scene != null && String(scene) !== '') request.scene = scene
    if (cursor) {
      request.endTime = cursor.endTime
      request.lastMsgId = cursor.lastMsgId
    }
    try {
      history = await (engine.getHistoryMsgs as (arg: AnyRecord) => Promise<unknown[]>).call(engine, request)
      if (!Array.isArray(history)) {
        history = null
        historyFailure = 'shape'
      }
    } catch {
      history = null
      historyFailure = 'rejected'
    }
  } else {
    historyFailure = 'unavailable'
  }

  let usedDOM = false
  let domReachedTop = false
  if (!Array.isArray(history)) {
    diagnosticStage = 'read_history_dom_fallback'
    const selected = new URL(location.href).searchParams.get('sessionId')
    if (selected !== conversationRef) {
      throw new Error(`history_api_${historyFailure ?? 'unavailable'}_on_base_route`)
    }
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
  const stableMessageIdentity = (value: unknown): string => {
    if (typeof value === 'string') return value
    return typeof value === 'number' && Number.isFinite(value) ? String(value) : ''
  }
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

  diagnosticStage = 'resolve_staff_identity'
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
  diagnosticStage = 'normalize_messages'
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
    } else if (rawType === 'custom' && customType === 148 && Boolean(from) && direction === 'in' &&
      clean(row.status).toLowerCase() === 'success' && clean(details.staffText)) {
      // 智联 148 是候选人侧“暂不考虑”反馈。只接受已解析为候选人入站、
      // 平台成功且带 staffText 的形状；其余 148 继续走保守 system 分支。
      kind = 'text'
      text = clean(details.staffText)
    } else {
      direction = 'system'
      kind = 'system'
      text = clean(
        details.staffText ?? details.userText ?? details.title ?? details.content ??
        envelope.msgb ?? envelope.msgc ?? envelope.text ?? row.text,
      ) || `[系统消息:${Number.isFinite(customType) ? customType : clean(rawType) || 'unknown'}]`
    }

    if (direction === 'out' && clean(row.status).toLowerCase() !== 'success') {
      throw new Error('outbound_delivery_unconfirmed')
    }

    // 脑侧 ambiguity verifier 复用 chat.readThread；client sendMessageId 可能只是
    // 乐观本地行，不能被结构化成“已发送”正证词。稳定消息身份只认服务端 idServer。
    const stableMessageID = stableMessageIdentity(row.idServer)
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
  const lastMsgId = oldest ? stableMessageIdentity(oldest.idServer) : ''
  const displayName = clean(session.name ?? session.realName) || '未命名候选人'
  const platformUserRef = clean(session.peerPartnerId ?? session.typeUserId ?? session.userId)
  diagnosticStage = 'return_result'
  return {
    messages: output,
    reachedTop: usedDOM ? domReachedTop : rows.length < limit,
    cursor: (usedDOM ? !domReachedTop && rows.length > 0 : rows.length >= limit) && endTime > 0 && lastMsgId
      ? { endTime, lastMsgId }
      : null,
    peer: platformUserRef ? { displayName, platformUserRef } : { displayName },
  }
  }

  try {
    return await execute()
  } catch (error) {
    const raw = error instanceof Error ? error.message : ''
    const known = [
      'session_lookup_response_invalid',
      'session_lookup_pagination_stalled',
      'session_lookup_has_more_missing',
      'session_lookup_duplicate_target',
      'session_lookup_target_unstable',
      'session_lookup_page_limit',
      'conversation_not_found',
      'conversation_target_missing',
      'history_api_unavailable_on_base_route',
      'history_api_rejected_on_base_route',
      'history_api_shape_on_base_route',
      'dom_thread_route_not_selected',
      'dom_thread_timeline_missing',
      'dom_thread_data_unavailable',
      'dom_thread_unstable',
      'dom_thread_cursor_anchor_missing',
      'thread_history_unavailable',
      'getHistoryMsgs row shape invalid',
      'staff_identity_missing',
      'message_direction_unresolved',
      'outbound_delivery_unconfirmed',
      'message_identity_missing',
    ].find((code) => raw.includes(code)) ?? 'unexpected'
    return {
      __recruitHelperMainError: `read_thread_main_failed:${diagnosticStage}:${known}`,
    } as unknown as MainThreadPageResult
  }
}

// debug 的单次公开 surface 诊断：只检查当前 route、可见控件、
// DOM containment、草稿与 form/type。它不参与生产 sendMessage 授权，
// 也不向生产 preflight 传递任何读数。
async function mainInspectSendSurface(conversationRef: string): Promise<MainSendSurfaceResult> {
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const selected = new URL(location.href).searchParams.get('sessionId') === conversationRef
  const composers = Array.from(document.querySelectorAll<HTMLTextAreaElement>(
    'textarea.km-input__original.is-normal.is-textarea.is-autoresize',
  )).filter((element) => visible(element) && element.closest('.im-sender__input-wrapper') !== null)
  const detail = composers.length === 1 ? composers[0].closest('.im-session-detail') : null
  const visibleDetails = Array.from(document.querySelectorAll<HTMLElement>('.im-session-detail')).filter(visible)
  const buttons = Array.from(document.querySelectorAll<HTMLButtonElement>('.im-sender__input-wrapper button'))
    .filter((element) => visible(element) && String(element.textContent ?? '').trim() === '发送')
  const wrapper = composers.length === 1
    ? composers[0].closest<HTMLElement>('.im-sender__input-wrapper')
    : null
  // 左侧列表的 `is-active` 是在线状态样式，不是选中会话证据。此诊断只确认
  // 当前 route 以及唯一可见 detail 内控件的 DOM containment，不证明平台内部接线。
  const sameWrapper = detail !== null && visibleDetails.length === 1 && visibleDetails[0] === detail &&
    wrapper !== null && buttons.length === 1 &&
    buttons[0].closest('.im-session-detail') === detail &&
    buttons[0].closest('.im-sender__input-wrapper') === wrapper
  const buttonFormSafe = buttons.length === 1 &&
    (buttons[0].form === null || buttons[0].type === 'button')
  const diagnosticStage: MainSendSurfaceStage = !selected
    ? 'route_target_missing'
    : composers.length !== 1
      ? 'composer_count'
      : detail === null || visibleDetails.length !== 1 || visibleDetails[0] !== detail
        ? 'detail_ambiguous'
        : wrapper === null || (buttons.length === 1 && (
            buttons[0].closest('.im-session-detail') !== detail ||
            buttons[0].closest('.im-sender__input-wrapper') !== wrapper
          ))
          ? 'wrapper_mismatch'
          : buttons.length !== 1
            ? 'button_count'
            : !buttonFormSafe
              ? 'button_form_unsafe'
              : 'ok'
  const composerBindingResolved = diagnosticStage === 'ok' && sameWrapper
  return {
    selected,
    composerBindingResolved,
    composerBindingMatched: composerBindingResolved,
    composerCount: composers.length,
    composerValue: composers.length === 1 ? composers[0].value : '',
    sendButtonCount: buttons.length,
    diagnosticStage,
  }
}

// debug 只确认当前 live timeline 能被单次同步投影，不读取
// imEngine/session/peer，也不复用生产 baseline 的目标绑定守卫。
function mainInspectSendTimeline(conversationRef: string): boolean {
  type AnyRecord = Record<string, unknown>
  const w = window as unknown as AnyRecord
  const asRecord = (value: unknown): AnyRecord | null =>
    value !== null && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const timelineSlot = (root: AnyRecord): unknown | null => {
    const store = asRecord(root.$store)
    const state = asRecord(store?.state)
    const im = asRecord(state?.im)
    const timelineMap = asRecord(im?.timelineMap)
    if (!timelineMap || !Object.prototype.hasOwnProperty.call(timelineMap, conversationRef)) return null
    const entry = asRecord(timelineMap[conversationRef])
    if (!entry || !Object.prototype.hasOwnProperty.call(entry, 'timeline')) return null
    return entry.timeline === null || entry.timeline === undefined ? null : entry.timeline
  }
  const resolveTimeline = (): unknown | null => {
    const nuxt = asRecord(w.$nuxt)
    const nuxtRoot = asRecord(nuxt?.$root) ?? nuxt
    if (nuxtRoot) {
      const timeline = timelineSlot(nuxtRoot)
      if (timeline !== null) return timeline
    }
    const timelines = Array.from(document.querySelectorAll<HTMLElement>('.im-timeline__wrapper')).filter(visible)
    if (timelines.length !== 1) return null
    let current: HTMLElement | null = timelines[0]
    for (let depth = 0; current && depth < 64; depth += 1) {
      const holder = current as HTMLElement & { __vue__?: unknown }
      if (Object.prototype.hasOwnProperty.call(holder, '__vue__')) {
        const owner = asRecord(holder.__vue__)
        const root = asRecord(owner?.$root)
        if (root) {
          const timeline = timelineSlot(root)
          if (timeline !== null) return timeline
        }
      }
      current = current.parentElement
    }
    return null
  }

  try {
    const source = resolveTimeline()
    if (!Array.isArray(source) || source.length > 4096) return false
    const rows: Array<{ idServer: string; time: number; sourceIndex: number }> = []
    for (let sourceIndex = 0; sourceIndex < source.length; sourceIndex += 1) {
      const row = asRecord(source[sourceIndex])
      if (!row) return false
      const idServer = String(row.idServer ?? '').trim()
      const time = Number(row.time)
      if (!idServer || !Number.isFinite(time) || time <= 0) return false
      rows.push({ idServer, time, sourceIndex })
    }
    rows.sort((left, right) => left.time - right.time || left.sourceIndex - right.sourceIndex)
    const seen = new Set<string>()
    const projected = rows.filter((row) => {
      if (seen.has(row.idServer)) return false
      seen.add(row.idServer)
      return true
    }).slice(-64)
    return projected.length <= 64
  } catch {
    return false
  }
}

// 只读诊断包装器只输出协议收编的粗粒度阶段码。会话标识和草稿只在本函数
// 内参与当前单次读取，任何页面值、异常或细粒度实现线索都不会越过返回边界。
export async function inspectZhilianSendSurfaceDiagnostic(): Promise<DebugInspectSendSurfaceData> {
  const unavailable = (): DebugInspectSendSurfaceData => ({
    ready: false,
    stage: 'diagnostic_unavailable',
  })
  const validSnapshot = (value: MainSendSurfaceResult): boolean =>
    typeof value.selected === 'boolean' &&
    typeof value.composerBindingResolved === 'boolean' &&
    typeof value.composerBindingMatched === 'boolean' &&
    Number.isSafeInteger(value.composerCount) && value.composerCount >= 0 &&
    typeof value.composerValue === 'string' &&
    Number.isSafeInteger(value.sendButtonCount) && value.sendButtonCount >= 0 &&
    Object.prototype.hasOwnProperty.call(SEND_SURFACE_STAGE_TO_DIAGNOSTIC, value.diagnosticStage)
  try {
    const tab = await canonicalZhilianTab()
    if (!tab || tab.id === undefined || pageKindFromURL(tab.url) !== 'im') {
      return { ready: false, stage: 'page_absent' }
    }
    const conversationRef = new URL(tab.url as string).searchParams.get('sessionId')?.trim() ?? ''
    if (!conversationRef) return { ready: false, stage: 'route_missing' }

    const snapshot = await runMain(tab.id, mainInspectSendSurface, [conversationRef])
    if (!validSnapshot(snapshot)) return unavailable()
    if (snapshot.composerValue !== '') return { ready: false, stage: 'draft_present' }

    const surfaceStage = SEND_SURFACE_STAGE_TO_DIAGNOSTIC[snapshot.diagnosticStage]
    if (surfaceStage !== 'ready') return { ready: false, stage: surfaceStage }

    const timelineAvailable = await runMain(tab.id, mainInspectSendTimeline, [conversationRef])
    if (timelineAvailable !== true) return { ready: false, stage: 'thread_unavailable' }
    return { ready: true, stage: 'ready' }
  } catch {
    return unavailable()
  }
}

// 直接拼接 ?sessionId=... 在目标尚未进入智联当前虚拟列表时会被平台剥回 /app/im。
// finder 只滚动并把完整 sessionId 唯一命中的行留在视口，绝不 click。即使注入在
// dispatcher 取消/超时后才返回，最多改变滚动位置，不可能产生迟到导航。
async function mainFindConversation(conversationRef: string): Promise<MainFindConversationResult> {
  type AnyRecord = Record<string, unknown>
  const w = window as unknown as AnyRecord
  const asRecord = (value: unknown): AnyRecord | null =>
    value && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const current = (): string => {
    try { return new URL(location.href).searchParams.get('sessionId') ?? '' } catch { return '' }
  }
  if (current() === conversationRef) return { status: 'found' }

  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const virtual = document.querySelector<HTMLElement>('.im-session-list .im-session-list__virtual')
  if (!virtual) return { status: 'failed', reason: 'list_surface_missing' }
  const scrollCandidates = [
    virtual,
    ...Array.from(virtual.querySelectorAll<HTMLElement>('.km-scrollbar__wrap, .km-scrollbar__view')),
    virtual.parentElement,
  ].filter((item): item is HTMLElement => item !== null)
  const scrollElement = scrollCandidates.sort((left, right) =>
    (right.scrollHeight - right.clientHeight) - (left.scrollHeight - left.clientHeight))[0]
  if (!scrollElement || scrollElement.clientHeight <= 0) {
    return { status: 'failed', reason: 'list_surface_missing' }
  }
  const itemSelector =
    '.im-session-list .im-session-list__virtual .im-session-list__virtual--box div[role="listitem"]'
  const refsOf = (node: HTMLElement): Set<string> => {
    const refs = new Set<string>()
    const addSource = (component: AnyRecord | null): void => {
      if (!component) return
      for (const container of [component, asRecord(component._props), asRecord(component.$props), asRecord(component.$data)]) {
        if (!container) continue
        const direct = String(container.sessionId ?? '').trim()
        const source = String(asRecord(container.source)?.sessionId ?? '').trim()
        if (direct) refs.add(direct)
        if (source) refs.add(source)
      }
    }
    for (const candidate of [node, ...Array.from(node.querySelectorAll<HTMLElement>('*'))]) {
      addSource(asRecord((candidate as HTMLElement & { __vue__?: unknown }).__vue__))
    }
    const root = asRecord(w.$nuxt)
    if (!root) return refs
    const queue: AnyRecord[] = [root]
    const seen = new Set<AnyRecord>()
    while (queue.length > 0 && seen.size < 4096) {
      const component = queue.shift()
      if (!component || seen.has(component)) continue
      seen.add(component)
      const element = component.$el as HTMLElement | undefined
      if (element && typeof element.contains === 'function' &&
          (element === node || node.contains(element))) addSource(component)
      const children = component.$children
      if (Array.isArray(children)) {
        for (const child of children) {
          const record = asRecord(child)
          if (record) queue.push(record)
        }
      }
    }
    return refs
  }
  const readWindow = (): Array<{ node: HTMLElement; ref: string }> | null => {
    const nodes = Array.from(document.querySelectorAll<HTMLElement>(itemSelector))
      .filter((node) => visible(node) && node.querySelector('.im-session-item__box, .im-session-item') !== null)
    if (nodes.length === 0) return []
    const pairs = nodes.map((node) => ({ node, refs: refsOf(node) }))
    if (pairs.some((pair) => pair.refs.size !== 1)) return null
    return pairs.map(({ node, refs }) => ({ node, ref: [...refs][0] }))
  }

  scrollElement.scrollTop = 0
  scrollElement.dispatchEvent(new Event('scroll', { bubbles: true }))
  await new Promise((resolve) => setTimeout(resolve, 250))
  const seenWindows = new Set<string>()
  for (let windowNo = 0; windowNo < 128; windowNo += 1) {
    let rows = readWindow()
    if (rows !== null && rows.length === 0) {
      await new Promise((resolve) => setTimeout(resolve, 200))
      rows = readWindow()
    }
    if (rows === null) return { status: 'failed', reason: 'list_binding_unresolved' }
    if (rows.length === 0) return { status: 'failed', reason: 'list_items_missing' }
    const refs = rows.map(({ ref }) => ref)
    // 虚拟列表带 overscan：相邻两个合法 scrollTop 可能暂时呈现同一首尾行。
    // 把实际位置纳入停滞键，真正不前进由下方 scrollTop 断言单独拦截。
    const windowKey = `${Math.round(scrollElement.scrollTop)}\u001f${refs[0]}\u001f${refs[refs.length - 1]}\u001f${refs.length}`
    if (seenWindows.has(windowKey)) return { status: 'failed', reason: 'list_window_repeated' }
    seenWindows.add(windowKey)
    const matches = rows.filter(({ ref }) => ref === conversationRef)
    if (matches.length > 1) return { status: 'failed', reason: 'target_binding_duplicated' }
    if (matches.length === 1) {
      matches[0].node.scrollIntoView({ block: 'center', inline: 'nearest' })
      await new Promise((resolve) => setTimeout(resolve, 150))
      const rebound = readWindow()
      if (rebound === null) return { status: 'failed', reason: 'list_binding_unresolved' }
      const reboundMatches = rebound.filter(({ ref }) => ref === conversationRef)
      if (reboundMatches.length !== 1) return { status: 'failed', reason: 'target_binding_changed' }
      return { status: 'found' }
    }
    const maxTop = Math.max(0, scrollElement.scrollHeight - scrollElement.clientHeight)
    if (scrollElement.scrollTop >= maxTop - 2) return { status: 'failed', reason: 'target_not_found' }
    const beforeTop = scrollElement.scrollTop
    const step = Math.max(200, Math.floor(scrollElement.clientHeight * 0.8))
    scrollElement.scrollTop = Math.min(maxTop, beforeTop + step)
    scrollElement.dispatchEvent(new Event('scroll', { bubbles: true }))
    await new Promise((resolve) => setTimeout(resolve, 250))
    if (scrollElement.scrollTop <= beforeTop) return { status: 'failed', reason: 'list_scroll_stalled' }
  }
  return { status: 'failed', reason: 'target_not_found' }
}

// finder 返回后由 extension 先 checkpoint 并重新核验账号；只有这个无 await 的短 task
// 可以 click。它从当前虚拟窗口重新按完整 sessionId 解析目标，复核空草稿、账号与
// 绝对期限，并只点击一次。迟到 task、虚拟行复用或人工输入都会在页面内变成零 click。
function mainClickConversationOnce(
  conversationRef: string,
  expectedCurrentConversationRef: string,
  expectedPrincipalFingerprint: string,
  notAfterMs: number,
): MainClickConversationResult {
  type AnyRecord = Record<string, unknown>
  const w = window as unknown as AnyRecord
  const asRecord = (value: unknown): AnyRecord | null =>
    value && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const clean = (value: unknown): string => String(value ?? '').trim()
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const rotateRight = (value: number, count: number): number =>
    (value >>> count) | (value << (32 - count))
  const digest = (value: string): string => {
    const input = new TextEncoder().encode(value)
    const totalLength = Math.ceil((input.length + 9) / 64) * 64
    const padded = new Uint8Array(totalLength)
    padded.set(input)
    padded[input.length] = 0x80
    const bitLength = input.length * 8
    const view = new DataView(padded.buffer)
    view.setUint32(totalLength - 8, Math.floor(bitLength / 0x100000000), false)
    view.setUint32(totalLength - 4, bitLength >>> 0, false)
    const constants = new Uint32Array([
      0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
      0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
      0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
      0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
      0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
      0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
      0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
      0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
    ])
    const state = new Uint32Array([
      0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
      0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
    ])
    const words = new Uint32Array(64)
    for (let offset = 0; offset < totalLength; offset += 64) {
      for (let index = 0; index < 16; index += 1) words[index] = view.getUint32(offset + index * 4, false)
      for (let index = 16; index < 64; index += 1) {
        const left = words[index - 15]
        const right = words[index - 2]
        const sigma0 = rotateRight(left, 7) ^ rotateRight(left, 18) ^ (left >>> 3)
        const sigma1 = rotateRight(right, 17) ^ rotateRight(right, 19) ^ (right >>> 10)
        words[index] = (words[index - 16] + sigma0 + words[index - 7] + sigma1) >>> 0
      }
      let a = state[0]
      let b = state[1]
      let c = state[2]
      let d = state[3]
      let e = state[4]
      let f = state[5]
      let g = state[6]
      let h = state[7]
      for (let index = 0; index < 64; index += 1) {
        const sum1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25)
        const choose = (e & f) ^ (~e & g)
        const temp1 = (h + sum1 + choose + constants[index] + words[index]) >>> 0
        const sum0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22)
        const majority = (a & b) ^ (a & c) ^ (b & c)
        const temp2 = (sum0 + majority) >>> 0
        h = g
        g = f
        f = e
        e = (d + temp1) >>> 0
        d = c
        c = b
        b = a
        a = (temp1 + temp2) >>> 0
      }
      state[0] = (state[0] + a) >>> 0
      state[1] = (state[1] + b) >>> 0
      state[2] = (state[2] + c) >>> 0
      state[3] = (state[3] + d) >>> 0
      state[4] = (state[4] + e) >>> 0
      state[5] = (state[5] + f) >>> 0
      state[6] = (state[6] + g) >>> 0
      state[7] = (state[7] + h) >>> 0
    }
    return Array.from(state, (word) => word.toString(16).padStart(8, '0')).join('')
  }
  const initialState = (): AnyRecord => {
    const source = Array.from(document.scripts)
      .map((script) => script.textContent ?? '')
      .find((candidate) => candidate.includes('__INITIAL_STATE__='))
    if (!source) return {}
    const candidate = source.slice(source.indexOf('__INITIAL_STATE__=') + '__INITIAL_STATE__='.length).trim()
    const start = candidate.indexOf('{')
    let depth = 0
    let quoted = false
    let escaped = false
    for (let index = start; index >= 0 && index < candidate.length; index += 1) {
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
          try { return JSON.parse(candidate.slice(start, index + 1)) as AnyRecord } catch { return {} }
        }
      }
    }
    return {}
  }
  const normalizeIdentityPart = (value: unknown): string | null => {
    if (typeof value === 'string') {
      const normalized = value.trim()
      return normalized.length > 0 ? normalized : null
    }
    if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
    return null
  }
  const initial = initialState()
  const principalCanonical = (): string | null => {
    const initialSession = asRecord(asRecord(initial.session)?.session)
    const runtimeSession = asRecord(w.$session)
    const session: AnyRecord = { ...(initialSession ?? {}), ...(runtimeSession ?? {}) }
    const staff: AnyRecord = {
      ...(asRecord(initialSession?.staff) ?? {}),
      ...(asRecord(runtimeSession?.staff) ?? {}),
    }
    const staffID = normalizeIdentityPart(staff.staffId)
    const organizationID = normalizeIdentityPart(asRecord(session.org)?.orgId) ??
      normalizeIdentityPart(asRecord(asRecord(initial.personal)?.imUserInfo)?.rootCompanyId)
    const loginPoint = normalizeIdentityPart(staff.defaultLoginPoint)
    if (session.isLoggedIn !== true || !staffID || !organizationID || !loginPoint) return null
    const pieces = ['zhilian-principal-v2', staffID, organizationID, loginPoint]
    return pieces.map((piece) => `${new TextEncoder().encode(piece).length}:${piece}`).join('|')
  }
  const refsOf = (node: HTMLElement): Set<string> => {
    const refs = new Set<string>()
    const addSource = (component: AnyRecord | null): void => {
      if (!component) return
      for (const container of [component, asRecord(component._props), asRecord(component.$props), asRecord(component.$data)]) {
        if (!container) continue
        const direct = clean(container.sessionId)
        const source = clean(asRecord(container.source)?.sessionId)
        if (direct) refs.add(direct)
        if (source) refs.add(source)
      }
    }
    for (const candidate of [node, ...Array.from(node.querySelectorAll<HTMLElement>('*'))]) {
      addSource(asRecord((candidate as HTMLElement & { __vue__?: unknown }).__vue__))
    }
    const root = asRecord(w.$nuxt)
    if (!root) return refs
    const queue: AnyRecord[] = [root]
    const seen = new Set<AnyRecord>()
    while (queue.length > 0 && seen.size < 4096) {
      const component = queue.shift()
      if (!component || seen.has(component)) continue
      seen.add(component)
      const element = component.$el as HTMLElement | undefined
      if (element && typeof element.contains === 'function' &&
          (element === node || node.contains(element))) addSource(component)
      const children = component.$children
      if (Array.isArray(children)) {
        for (const child of children) {
          const record = asRecord(child)
          if (record) queue.push(record)
        }
      }
    }
    return refs
  }
  const itemSelector =
    '.im-session-list .im-session-list__virtual .im-session-list__virtual--box div[role="listitem"]'
  const readRows = (): Array<{ node: HTMLElement; ref: string }> | null => {
    const nodes = Array.from(document.querySelectorAll<HTMLElement>(itemSelector))
      .filter((node) => visible(node) && node.querySelector('.im-session-item__box, .im-session-item') !== null)
    if (nodes.length === 0) return []
    const rows = nodes.map((node) => ({ node, refs: refsOf(node) }))
    if (rows.some(({ refs }) => refs.size !== 1)) return null
    return rows.map(({ node, refs }) => ({ node, ref: [...refs][0] }))
  }
  const composerDraftSafe = (): 'ok' | 'ambiguous' | 'nonempty' => {
    const composers = Array.from(document.querySelectorAll<HTMLTextAreaElement>(
      'textarea.km-input__original.is-normal.is-textarea.is-autoresize',
    )).filter((element) => visible(element) && element.closest('.im-sender__input-wrapper') !== null)
    if (composers.length > 1) return 'ambiguous'
    return composers.length === 1 && composers[0].value !== '' ? 'nonempty' : 'ok'
  }

  if (!Number.isFinite(notAfterMs) || Date.now() > notAfterMs) {
    return { status: 'failed', reason: 'action_window_elapsed' }
  }
  const principal = principalCanonical()
  if (!principal || digest(principal) !== expectedPrincipalFingerprint) {
    return { status: 'failed', reason: 'identity_changed' }
  }
  const draft = composerDraftSafe()
  if (draft === 'ambiguous') return { status: 'failed', reason: 'composer_ambiguous' }
  if (draft === 'nonempty') return { status: 'failed', reason: 'composer_nonempty' }
  const route = new URL(location.href)
  if (route.pathname !== '/app/im') return { status: 'failed', reason: 'route_changed' }
  const currentConversationRef = route.searchParams.get('sessionId') ?? ''
  if (currentConversationRef === conversationRef) {
    return expectedCurrentConversationRef === conversationRef
      ? { status: 'already_selected' }
      : { status: 'failed', reason: 'route_changed' }
  }
  if (currentConversationRef !== expectedCurrentConversationRef) {
    return { status: 'failed', reason: 'route_changed' }
  }
  const firstRows = readRows()
  if (firstRows === null) return { status: 'failed', reason: 'list_binding_unresolved' }
  if (firstRows.length === 0) return { status: 'failed', reason: 'list_items_missing' }
  const firstMatches = firstRows.filter(({ ref }) => ref === conversationRef)
  if (firstMatches.length > 1) return { status: 'failed', reason: 'target_binding_duplicated' }
  if (firstMatches.length !== 1) return { status: 'failed', reason: 'target_binding_changed' }
  const firstRow = firstMatches[0].node
  const clickTarget = firstRow.querySelector<HTMLElement>('.im-session-item') ?? firstRow
  if (!firstRow.isConnected || !clickTarget.isConnected ||
      (clickTarget !== firstRow && !firstRow.contains(clickTarget)) || !visible(clickTarget)) {
    return { status: 'failed', reason: 'click_target_missing' }
  }
  const finalRows = readRows()
  if (finalRows === null) return { status: 'failed', reason: 'list_binding_unresolved' }
  const finalMatches = finalRows.filter(({ ref }) => ref === conversationRef)
  const finalRowRefs = refsOf(firstRow)
  if (finalMatches.length !== 1 || finalMatches[0].node !== firstRow ||
      finalRowRefs.size !== 1 || [...finalRowRefs][0] !== conversationRef) {
    return { status: 'failed', reason: 'target_binding_changed' }
  }
  if (Date.now() > notAfterMs) return { status: 'failed', reason: 'action_window_elapsed' }
  if (principalCanonical() !== principal) return { status: 'failed', reason: 'identity_changed' }
  const finalRoute = new URL(location.href)
  if (finalRoute.pathname !== '/app/im' ||
      (finalRoute.searchParams.get('sessionId') ?? '') !== expectedCurrentConversationRef) {
    return { status: 'failed', reason: 'route_changed' }
  }
  const finalDraft = composerDraftSafe()
  if (finalDraft === 'ambiguous') return { status: 'failed', reason: 'composer_ambiguous' }
  if (finalDraft === 'nonempty') return { status: 'failed', reason: 'composer_nonempty' }
  if (!firstRow.isConnected || !clickTarget.isConnected ||
      (clickTarget !== firstRow && !firstRow.contains(clickTarget))) {
    return { status: 'failed', reason: 'click_target_missing' }
  }
  clickTarget.click()
  return { status: 'clicked' }
}

// 发送基线不调用 history API，也不复用 chat.readThread 的 API/DOM/SSR 混合回退。
// 每次只选择一个实时 Vuex timelineMap 通道：优先 $nuxt；该通道没有目标槽时，
// 才从唯一可见时间线容器的祖先 Vue root 兜底，不取两根互证。
async function mainCaptureSendBaseline(
  conversationRef: string,
  expectedTail: ZhilianMessageAnchor[],
): Promise<MainSendBaselineResult> {
  type AnyRecord = Record<string, unknown>
  interface SnapshotRow {
    idServer: string
    status: string
    type: string | number
    from: string
    text: string
    content: string
    time: number
    sourceIndex: number
  }
  const failed = (stage: MainSendBaselineFailureStage): MainSendBaselineFailed => ({ status: 'failed', stage })
  try {
    const w = window as unknown as AnyRecord
    const asRecord = (value: unknown): AnyRecord | null =>
      value !== null && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
    const visible = (element: Element): boolean => {
      const node = element as HTMLElement
      const style = getComputedStyle(node)
      return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
    }
    const engine = asRecord(w.imEngine)
    if (!engine) return failed('engine_unavailable')
    const clean = (value: unknown): string => String(value ?? '')
      .normalize('NFC')
      .replace(/\u00a0/gu, ' ')
      .replace(/\s+/gu, ' ')
      .trim()
    const stableMessageIdentity = (value: unknown): string => {
      if (typeof value === 'string') return value
      return typeof value === 'number' && Number.isFinite(value) ? String(value) : ''
    }
    const digest = async (value: string): Promise<string> => {
      const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value))
      return Array.from(new Uint8Array(bytes), (byte) => byte.toString(16).padStart(2, '0')).join('')
    }
    const routeMatches = (): boolean => {
      try {
        const route = new URL(location.href)
        return route.pathname === '/app/im' && route.searchParams.get('sessionId') === conversationRef
      } catch {
        return false
      }
    }

    if (!routeMatches()) return failed('route_changed')

    const sessions = (Array.isArray(engine.sessions) ? engine.sessions : []) as AnyRecord[]
    const sessionMatches = sessions.filter((item) => clean(item.sessionId) === conversationRef)
    const session = sessionMatches.length === 1 ? sessionMatches[0] : undefined
    const target = clean(session?.peerPartnerId)
    if (!session || !target) return failed('session_unavailable')
    let initialState: AnyRecord | null | undefined
    const readInitialState = (): AnyRecord | null => {
      if (initialState !== undefined) return initialState
      initialState = null
      const source = Array.from(document.scripts ?? [])
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
      initialState = asRecord(parsed)
      return initialState
    }
    const timelineSlot = (im: AnyRecord | null): { present: boolean; value: unknown } => {
      const timelineMap = asRecord(im?.timelineMap)
      if (!timelineMap || !Object.prototype.hasOwnProperty.call(timelineMap, conversationRef)) {
        return { present: false, value: null }
      }
      const entry = asRecord(timelineMap[conversationRef])
      if (!entry || !Object.prototype.hasOwnProperty.call(entry, 'timeline')) {
        return { present: false, value: null }
      }
      return { present: true, value: entry.timeline }
    }
    const resolveLiveTimeline = (): unknown | null => {
      const nuxt = asRecord(w.$nuxt)
      const nuxtRoot = asRecord(nuxt?.$root) ?? nuxt
      if (nuxtRoot) {
        const store = asRecord(nuxtRoot.$store)
        const state = asRecord(store?.state)
        const slot = timelineSlot(asRecord(state?.im))
        if (slot.present && slot.value !== null && slot.value !== undefined) return slot.value
      }
      const timelines = Array.from(document.querySelectorAll<HTMLElement>('.im-timeline__wrapper')).filter(visible)
      if (timelines.length !== 1) return null
      let current: HTMLElement | null = timelines[0]
      for (let depth = 0; current && depth < 64; depth += 1) {
        const holder = current as HTMLElement & { __vue__?: unknown }
        if (Object.prototype.hasOwnProperty.call(holder, '__vue__')) {
          const owner = asRecord(holder.__vue__)
          const root = asRecord(owner?.$root)
          if (root) {
            const store = asRecord(root.$store)
            const state = asRecord(store?.state)
            const slot = timelineSlot(asRecord(state?.im))
            if (slot.present && slot.value !== null && slot.value !== undefined) return slot.value
          }
        }
        current = current.parentElement
      }
      return null
    }
    const snapshotContent = (value: unknown): string => {
      if (typeof value === 'string') return value
      if (value === null || value === undefined) return ''
      const serialized = JSON.stringify(value)
      return serialized === undefined ? String(value) : serialized
    }
    const projectRows = (value: unknown): SnapshotRow[] | null => {
      if (!Array.isArray(value) || value.length > 4096) return null
      const projected: SnapshotRow[] = []
      for (let sourceIndex = 0; sourceIndex < value.length; sourceIndex += 1) {
        const raw = value[sourceIndex]
        const row = asRecord(raw)
        if (!row) return null
        const idServer = stableMessageIdentity(row.idServer)
        if (!idServer) return null
        const rawType = row.type
        const time = Number(row.time)
        if (!Number.isFinite(time) || time <= 0) return null
        projected.push({
          idServer,
          status: clean(row.status),
          type: typeof rawType === 'number' || typeof rawType === 'string' ? rawType : String(rawType ?? ''),
          from: clean(row.from),
          text: String(row.text ?? ''),
          content: snapshotContent(row.content),
          time,
          sourceIndex,
        })
      }
      return projected
    }
    const stableWindow = (rows: SnapshotRow[]): SnapshotRow[] => {
      const sorted = [...rows].sort((left, right) => left.time - right.time || left.sourceIndex - right.sourceIndex)
      const seen = new Set<string>()
      const deduped = sorted.filter((row) => {
        if (seen.has(row.idServer)) return false
        seen.add(row.idServer)
        return true
      })
      return deduped.slice(Math.max(0, deduped.length - 64))
    }
    const parseObject = (value: unknown): AnyRecord => {
      if (value && typeof value === 'object' && !Array.isArray(value)) return value as AnyRecord
      if (typeof value !== 'string' || value.length === 0) return {}
      try {
        const parsed = JSON.parse(value) as unknown
        return asRecord(parsed) ?? {}
      } catch {
        return {}
      }
    }
    const anchorFor = async (row: SnapshotRow, staffID: string): Promise<ZhilianMessageAnchor | null> => {
      const envelope = parseObject(row.content)
      const inner = parseObject(envelope.content)
      const details = Object.keys(inner).length > 0 ? inner : envelope
      const rawType = row.type
      const customType = Number(
        typeof rawType === 'number' || /^\d+$/u.test(String(rawType)) ? rawType : envelope.type,
      )
      const from = clean(row.from)
      let direction: ZhilianMessageAnchor['direction'] = from
        ? from === staffID ? 'out' : 'in'
        : 'system'
      let kind: 'text' | 'card' | 'system' = 'system'
      let text: string | null = null
      let cardType: 'wechatExchange' | null = null
      let identity = ''
      if (rawType === 'text') {
        if (!from) return null
        kind = 'text'
        text = clean(row.text)
      } else if (customType === 105) {
        if (!from) return null
        kind = 'card'
        cardType = 'wechatExchange'
        text = clean(
          direction === 'out'
            ? details.staffContent ?? details.senderText ?? details.detail
            : details.userContent ?? details.receiverText ?? details.detail,
        ) || '[交换微信请求]'
        identity = clean(details.requestId ?? details.id ?? details.cardId)
      } else if (customType === 131) {
        if (!from) return null
        kind = 'text'
        text = clean(details.greetingText ?? envelope.greetingText)
      } else if (rawType === 'custom' && customType === 148 && Boolean(from) && direction === 'in' &&
        clean(row.status).toLowerCase() === 'success' && clean(details.staffText)) {
        kind = 'text'
        text = clean(details.staffText)
      } else {
        direction = 'system'
        text = clean(
          details.staffText ?? details.userText ?? details.title ?? details.content ??
          envelope.msgb ?? envelope.msgc ?? envelope.text ?? row.text,
        ) || `[系统消息:${Number.isFinite(customType) ? customType : clean(rawType) || 'unknown'}]`
      }
      if (direction === 'out' && clean(row.status).toLowerCase() !== 'success') return null
      const canonicalContent = clean(text)
      const contentHash = kind === 'card'
        ? await digest(`card\x1f${cardType ?? 'other'}\x1f${clean(identity || row.idServer || text)}`)
        : await digest(canonicalContent)
      return { direction, contentHash }
    }
    const tailMatches = async (rows: SnapshotRow[], staffID: string): Promise<boolean> => {
      if (expectedTail.length === 0) return true
      if (rows.length < expectedTail.length) return false
      const tail = rows.slice(-expectedTail.length)
      for (let index = 0; index < tail.length; index += 1) {
        const actual = await anchorFor(tail[index], staffID)
        const expected = expectedTail[index]
        if (!actual || actual.direction !== expected.direction || actual.contentHash !== expected.contentHash) {
          return false
        }
      }
      return true
    }
    const runtimeSession = asRecord(w.$session)
    const runtimeStaff = asRecord(runtimeSession?.staff)
    let staffID = clean(runtimeStaff?.staffId)
    if (expectedTail.length > 0 && !staffID) {
      const initialSession = asRecord(asRecord(readInitialState()?.session)?.session)
      staffID = clean(asRecord(initialSession?.staff)?.staffId)
    }
    if (expectedTail.length > 0 && !staffID) return failed('history_first_unavailable')

    const source = resolveLiveTimeline()
    if (!source) return failed('history_first_unavailable')
    const complete = projectRows(source)
    if (!complete) return failed('history_first_unavailable')
    const snapshot = stableWindow(complete)
    let snapshotTailMatches: boolean
    try {
      snapshotTailMatches = await tailMatches(snapshot, staffID)
    } catch {
      return failed('hash_unavailable')
    }
    if (!snapshotTailMatches) return failed('guard_snapshot_uncovered')

    const keys: string[] = []
    try {
      for (const row of snapshot) keys.push(await digest(`source-v1|${row.idServer}`))
    } catch {
      return failed('hash_unavailable')
    }

    let targetBindingToken: string
    try {
      targetBindingToken = await digest(JSON.stringify([conversationRef, target]))
    } catch {
      return failed('hash_unavailable')
    }
    return {
      status: 'ready',
      stage: 'ready',
      serverSourceKeys: keys,
      targetBindingToken,
    }
  } catch {
    return failed('unexpected')
  }
}

// 点击后的成功证词与发送基线同源：每次只读同一选择规则得到的实时 Vuex timeline。
// 全源任一行缺 server id 都闭锁；排序、首 ID 去重、last64 后才排除 baseline，
// 防止窗口外旧同文被误认成这次发送。函数绝不调用 history API。
async function mainObserveStableOutbound(
  conversationRef: string,
  textHash: string,
  baselineServerSourceKeys: string[],
  expectedTargetBindingToken: string,
): Promise<MainObserveStableOutboundResult> {
  type AnyRecord = Record<string, unknown>
  const w = window as unknown as AnyRecord
  const asRecord = (value: unknown): AnyRecord | null =>
    value !== null && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/\u00a0/gu, ' ')
    .replace(/\s+/gu, ' ')
    .trim()
  const stableMessageIdentity = (value: unknown): string => {
    if (typeof value === 'string') return value
    return typeof value === 'number' && Number.isFinite(value) ? String(value) : ''
  }
  const digest = async (value: string): Promise<string> => {
    const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value))
    return Array.from(new Uint8Array(bytes), (byte) => byte.toString(16).padStart(2, '0')).join('')
  }
  const routeMatches = (): boolean => {
    try {
      const route = new URL(location.href)
      return route.pathname === '/app/im' && route.searchParams.get('sessionId') === conversationRef
    } catch {
      return false
    }
  }
  const failed = (): MainObserveStableOutboundResult => ({
    selected: routeMatches(),
    matchingNewServerMessages: 0,
  })
  try {
    if (!routeMatches()) return failed()
    const engine = asRecord(w.imEngine)
    const sessions = (Array.isArray(engine?.sessions) ? engine.sessions : []) as AnyRecord[]
    const matches = sessions.filter((item) => clean(item.sessionId) === conversationRef)
    const session = matches.length === 1 ? matches[0] : undefined
    const target = clean(session?.peerPartnerId)
    if (!session || !target) return failed()
    if (!/^[0-9a-f]{64}$/u.test(expectedTargetBindingToken) ||
        await digest(JSON.stringify([conversationRef, target])) !== expectedTargetBindingToken) return failed()

    const timelineSlot = (root: AnyRecord): unknown | null => {
      const store = asRecord(root.$store)
      const state = asRecord(store?.state)
      const im = asRecord(state?.im)
      const timelineMap = asRecord(im?.timelineMap)
      if (!timelineMap || !Object.prototype.hasOwnProperty.call(timelineMap, conversationRef)) return null
      const entry = asRecord(timelineMap[conversationRef])
      if (!entry || !Object.prototype.hasOwnProperty.call(entry, 'timeline')) return null
      return entry.timeline === null || entry.timeline === undefined ? null : entry.timeline
    }
    const resolveLiveTimeline = (): unknown | null => {
      const nuxt = asRecord(w.$nuxt)
      const nuxtRoot = asRecord(nuxt?.$root) ?? nuxt
      if (nuxtRoot) {
        const timeline = timelineSlot(nuxtRoot)
        if (timeline !== null) return timeline
      }
      const timelines = Array.from(document.querySelectorAll<HTMLElement>('.im-timeline__wrapper')).filter(visible)
      if (timelines.length !== 1) return null
      let current: HTMLElement | null = timelines[0]
      for (let depth = 0; current && depth < 64; depth += 1) {
        const holder = current as HTMLElement & { __vue__?: unknown }
        if (Object.prototype.hasOwnProperty.call(holder, '__vue__')) {
          const owner = asRecord(holder.__vue__)
          const root = asRecord(owner?.$root)
          if (root) {
            const timeline = timelineSlot(root)
            if (timeline !== null) return timeline
          }
        }
        current = current.parentElement
      }
      return null
    }
    const rawRows = resolveLiveTimeline()
    if (!Array.isArray(rawRows) || rawRows.length > 4096) return failed()
    const projected: Array<{
      idServer: string
      status: string
      type: string | number
      from: string
      text: string
      time: number
      sourceIndex: number
    }> = []
    for (let sourceIndex = 0; sourceIndex < rawRows.length; sourceIndex += 1) {
      const raw = rawRows[sourceIndex]
      const row = asRecord(raw)
      if (!row) return failed()
      const idServer = stableMessageIdentity(row.idServer)
      if (!idServer) return failed()
      const rawType = row.type
      const time = Number(row.time)
      if (!Number.isFinite(time) || time <= 0) return failed()
      projected.push({
        idServer,
        status: clean(row.status),
        type: typeof rawType === 'string' || typeof rawType === 'number' ? rawType : String(rawType ?? ''),
        from: clean(row.from),
        text: String(row.text ?? ''),
        time,
        sourceIndex,
      })
    }
    const sorted = [...projected].sort((left, right) => left.time - right.time || left.sourceIndex - right.sourceIndex)
    const seen = new Set<string>()
    const windowRows = sorted.filter((row) => {
      if (seen.has(row.idServer)) return false
      seen.add(row.idServer)
      return true
    }).slice(-64)

    const runtimeSession = asRecord(w.$session)
    let staffID = clean(asRecord(runtimeSession?.staff)?.staffId)
    if (!staffID) {
      const source = Array.from(document.scripts ?? [])
        .map((script) => script.textContent ?? '')
        .find((candidate) => candidate.includes('__INITIAL_STATE__='))
      if (source) {
        const candidate = source.slice(source.indexOf('__INITIAL_STATE__=') + '__INITIAL_STATE__='.length).trim()
        const start = candidate.indexOf('{')
        let depth = 0
        let quoted = false
        let escaped = false
        for (let index = start; index >= 0 && index < candidate.length; index += 1) {
          const char = candidate[index]
          if (quoted) {
            if (escaped) escaped = false
            else if (char === '\\') escaped = true
            else if (char === '"') quoted = false
            continue
          }
          if (char === '"') quoted = true
          else if (char === '{') depth += 1
          else if (char === '}' && --depth === 0) {
            try {
              const initial = asRecord(JSON.parse(candidate.slice(start, index + 1)))
              const initialSession = asRecord(asRecord(initial?.session)?.session)
              staffID = clean(asRecord(initialSession?.staff)?.staffId)
            } catch {
              // identity 保持 unresolved；本轮只返回阴性观察。
            }
            break
          }
        }
      }
    }
    if (!staffID) return failed()

    const sourceKeyPattern = /^[0-9a-f]{64}$/u
    if (baselineServerSourceKeys.length > 64 ||
        baselineServerSourceKeys.some((key) => !sourceKeyPattern.test(key)) ||
        new Set(baselineServerSourceKeys).size !== baselineServerSourceKeys.length) return failed()
    const currentSourceKeys: string[] = []
    for (const row of windowRows) currentSourceKeys.push(await digest(`source-v1|${row.idServer}`))

    // 只接受“基线之后严格追加一条”。这比无序集合差更保守，但能阻止
    // time 改写、整窗替换或并发新消息把窗口外旧同文误认为本次发送。
    let continuous = false
    if (baselineServerSourceKeys.length < 64) {
      continuous = currentSourceKeys.length === baselineServerSourceKeys.length + 1 &&
        baselineServerSourceKeys.every((key, index) => currentSourceKeys[index] === key)
    } else {
      continuous = currentSourceKeys.length === 64 &&
        baselineServerSourceKeys.slice(1).every((key, index) => currentSourceKeys[index] === key)
    }
    if (!continuous || currentSourceKeys.length === 0) return failed()
    const newSourceKey = currentSourceKeys[currentSourceKeys.length - 1]
    if (baselineServerSourceKeys.includes(newSourceKey)) return failed()
    const newRow = windowRows[windowRows.length - 1]
    const matched = clean(newRow.status).toLowerCase() === 'success' &&
      newRow.type === 'text' && clean(newRow.from) === staffID &&
      await digest(clean(newRow.text)) === textHash
    // digest 会让出事件循环；返回阳性证词前必须重新确认同一 route
    // 仍唯一绑定到开始时的对话目标。消息自身会改变 session 展示字段，
    // 所以这里只冻结 sessionId + peerPartnerId，不比 modifiedTime/lastSentence。
    if (!routeMatches()) return failed()
    const finalSessions = (Array.isArray(engine?.sessions) ? engine.sessions : []) as AnyRecord[]
    const finalMatches = finalSessions.filter((item) => clean(item.sessionId) === conversationRef)
    if (finalMatches.length !== 1 || clean(finalMatches[0].peerPartnerId) !== target) return failed()
    return {
      selected: true,
      matchingNewServerMessages: matched ? 1 : 0,
    }
  } catch {
    return failed()
  }
}

// 最终消息副作用只在这一段同步 MAIN task 中发生。函数内没有 Promise/await、网络读取、
// 定时器或第二次 click；即使 SW 在注入排队期间失联，过期 action window 也会在页面内
// 阻止迟到点击。
function mainSendMessageOnce(
  conversationRef: string,
  text: string,
  expectedTail: ZhilianMessageAnchor[],
  expectedPrincipalFingerprint: string,
  irreversibleNotAfterMs: number,
  expectedBaselineServerSourceKeys: string[],
  expectedTargetBindingToken: string,
  phase: MainSendPhase,
): MainSendOnceResult {
  type AnyRecord = Record<string, unknown>
  const w = window as unknown as AnyRecord
  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/\u00a0/gu, ' ')
    .replace(/\s+/gu, ' ')
    .trim()
  const stableMessageIdentity = (value: unknown): string => {
    if (typeof value === 'string') return value
    return typeof value === 'number' && Number.isFinite(value) ? String(value) : ''
  }
  const asRecord = (value: unknown): AnyRecord | null =>
    value && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const rotateRight = (value: number, count: number): number =>
    (value >>> count) | (value << (32 - count))
  const digest = (value: string): string => {
    const input = new TextEncoder().encode(value)
    const totalLength = Math.ceil((input.length + 9) / 64) * 64
    const padded = new Uint8Array(totalLength)
    padded.set(input)
    padded[input.length] = 0x80
    const bitLength = input.length * 8
    const view = new DataView(padded.buffer)
    view.setUint32(totalLength - 8, Math.floor(bitLength / 0x100000000), false)
    view.setUint32(totalLength - 4, bitLength >>> 0, false)
    const constants = new Uint32Array([
      0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
      0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
      0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
      0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
      0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
      0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
      0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
      0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
    ])
    const state = new Uint32Array([
      0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
      0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
    ])
    const words = new Uint32Array(64)
    for (let offset = 0; offset < totalLength; offset += 64) {
      for (let index = 0; index < 16; index += 1) {
        words[index] = view.getUint32(offset + index * 4, false)
      }
      for (let index = 16; index < 64; index += 1) {
        const left = words[index - 15]
        const right = words[index - 2]
        const sigma0 = rotateRight(left, 7) ^ rotateRight(left, 18) ^ (left >>> 3)
        const sigma1 = rotateRight(right, 17) ^ rotateRight(right, 19) ^ (right >>> 10)
        words[index] = (words[index - 16] + sigma0 + words[index - 7] + sigma1) >>> 0
      }
      let a = state[0]
      let b = state[1]
      let c = state[2]
      let d = state[3]
      let e = state[4]
      let f = state[5]
      let g = state[6]
      let h = state[7]
      for (let index = 0; index < 64; index += 1) {
        const sum1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25)
        const choose = (e & f) ^ (~e & g)
        const temp1 = (h + sum1 + choose + constants[index] + words[index]) >>> 0
        const sum0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22)
        const majority = (a & b) ^ (a & c) ^ (b & c)
        const temp2 = (sum0 + majority) >>> 0
        h = g
        g = f
        f = e
        e = (d + temp1) >>> 0
        d = c
        c = b
        b = a
        a = (temp1 + temp2) >>> 0
      }
      state[0] = (state[0] + a) >>> 0
      state[1] = (state[1] + b) >>> 0
      state[2] = (state[2] + c) >>> 0
      state[3] = (state[3] + d) >>> 0
      state[4] = (state[4] + e) >>> 0
      state[5] = (state[5] + f) >>> 0
      state[6] = (state[6] + g) >>> 0
      state[7] = (state[7] + h) >>> 0
    }
    return Array.from(state, (word) => word.toString(16).padStart(8, '0')).join('')
  }
  const initialState = (): AnyRecord => {
    const source = Array.from(document.scripts)
      .map((script) => script.textContent ?? '')
      .find((candidate) => candidate.includes('__INITIAL_STATE__='))
    if (!source) return {}
    const candidate = source.slice(source.indexOf('__INITIAL_STATE__=') + '__INITIAL_STATE__='.length).trim()
    const start = candidate.indexOf('{')
    let depth = 0
    let quoted = false
    let escaped = false
    for (let index = start; index >= 0 && index < candidate.length; index += 1) {
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
          try { return JSON.parse(candidate.slice(start, index + 1)) as AnyRecord } catch { return {} }
        }
      }
    }
    return {}
  }
  const initial = initialState()
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const currentRoute = (): boolean => {
    try {
      const route = new URL(location.href)
      return route.pathname === '/app/im' && route.searchParams.get('sessionId') === conversationRef
    } catch {
      return false
    }
  }
  const normalizeIdentityPart = (value: unknown): string | null => {
    if (typeof value === 'string') {
      const normalized = value.trim()
      return normalized.length > 0 ? normalized : null
    }
    if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
    return null
  }
  const principalCanonical = (): string | null => {
    const initialSession = asRecord(asRecord(initial.session)?.session)
    const runtimeSession = asRecord(w.$session)
    const session: AnyRecord = { ...(initialSession ?? {}), ...(runtimeSession ?? {}) }
    const staff: AnyRecord = {
      ...(asRecord(initialSession?.staff) ?? {}),
      ...(asRecord(runtimeSession?.staff) ?? {}),
    }
    const staffID = normalizeIdentityPart(staff.staffId)
    const organizationID = normalizeIdentityPart(asRecord(session.org)?.orgId) ??
      normalizeIdentityPart(asRecord(asRecord(initial.personal)?.imUserInfo)?.rootCompanyId)
    const loginPoint = normalizeIdentityPart(staff.defaultLoginPoint)
    if (session.isLoggedIn !== true || !staffID || !organizationID || !loginPoint) return null
    const pieces = ['zhilian-principal-v2', staffID, organizationID, loginPoint]
    return pieces.map((piece) => `${new TextEncoder().encode(piece).length}:${piece}`).join('|')
  }
  interface ActionSnapshotRow {
    idServer: string
    status: string
    type: string | number
    from: string
    text: string
    content: string
    time: number
    sourceIndex: number
  }
  const snapshotContent = (value: unknown): string => {
    if (typeof value === 'string') return value
    if (value === null || value === undefined) return ''
    const serialized = JSON.stringify(value)
    return serialized === undefined ? String(value) : serialized
  }
  const liveTimelineProjection = (): {
    sourceKeys: string[]
    windowRows: ActionSnapshotRow[]
  } | null => {
    const timelineSlot = (root: AnyRecord): unknown | null => {
      const store = asRecord(root.$store)
      const state = asRecord(store?.state)
      const im = asRecord(state?.im)
      const timelineMap = asRecord(im?.timelineMap)
      if (!timelineMap || !Object.prototype.hasOwnProperty.call(timelineMap, conversationRef)) return null
      const entry = asRecord(timelineMap[conversationRef])
      if (!entry || !Object.prototype.hasOwnProperty.call(entry, 'timeline') ||
          entry.timeline === null || entry.timeline === undefined) return null
      return entry.timeline
    }
    let timeline: unknown | null = null
    const nuxt = asRecord(w.$nuxt)
    const nuxtRoot = asRecord(nuxt?.$root) ?? nuxt
    if (nuxtRoot) timeline = timelineSlot(nuxtRoot)
    if (timeline === null) {
      const timelines = Array.from(document.querySelectorAll<HTMLElement>('.im-timeline__wrapper')).filter(visible)
      if (timelines.length !== 1) return null
      let current: HTMLElement | null = timelines[0]
      for (let depth = 0; current && depth < 64; depth += 1) {
        const holder = current as HTMLElement & { __vue__?: unknown }
        if (Object.prototype.hasOwnProperty.call(holder, '__vue__')) {
          const owner = asRecord(holder.__vue__)
          const root = asRecord(owner?.$root)
          if (root) {
            timeline = timelineSlot(root)
            if (timeline !== null) break
          }
        }
        current = current.parentElement
      }
    }
    if (!Array.isArray(timeline) || timeline.length > 4096) return null

    const rows: ActionSnapshotRow[] = []
    for (let sourceIndex = 0; sourceIndex < timeline.length; sourceIndex += 1) {
      const row = asRecord(timeline[sourceIndex])
      if (!row) return null
      const idServer = stableMessageIdentity(row.idServer)
      const time = Number(row.time)
      if (!idServer || !Number.isFinite(time) || time <= 0) return null
      const rawType = row.type
      rows.push({
        idServer,
        status: clean(row.status),
        type: typeof rawType === 'number' || typeof rawType === 'string' ? rawType : String(rawType ?? ''),
        from: clean(row.from),
        text: String(row.text ?? ''),
        content: snapshotContent(row.content),
        time,
        sourceIndex,
      })
    }
    rows.sort((left, right) => left.time - right.time || left.sourceIndex - right.sourceIndex)
    const seen = new Set<string>()
    const orderedRows: ActionSnapshotRow[] = []
    for (const row of rows) {
      if (seen.has(row.idServer)) continue
      seen.add(row.idServer)
      orderedRows.push(row)
    }
    const windowStart = Math.max(0, orderedRows.length - 64)
    const windowRows = orderedRows.slice(windowStart)
    return {
      sourceKeys: windowRows.map((row) => digest(`source-v1|${row.idServer}`)),
      windowRows,
    }
  }
  const parseObject = (value: unknown): AnyRecord => {
    if (value && typeof value === 'object' && !Array.isArray(value)) return value as AnyRecord
    if (typeof value !== 'string' || value.length === 0) return {}
    try {
      const parsed = JSON.parse(value) as unknown
      return asRecord(parsed) ?? {}
    } catch {
      return {}
    }
  }
  const runtimeStaffID = (): string => {
    const runtimeSession = asRecord(w.$session)
    const runtimeStaff = asRecord(runtimeSession?.staff)
    const initialSession = asRecord(asRecord(initial.session)?.session)
    return clean(runtimeStaff?.staffId) || clean(asRecord(initialSession?.staff)?.staffId)
  }
  const actionAnchorFor = (row: ActionSnapshotRow, staffID: string): ZhilianMessageAnchor | null => {
    const envelope = parseObject(row.content)
    const inner = parseObject(envelope.content)
    const details = Object.keys(inner).length > 0 ? inner : envelope
    const rawType = row.type
    const customType = Number(
      typeof rawType === 'number' || /^\d+$/u.test(String(rawType)) ? rawType : envelope.type,
    )
    const from = clean(row.from)
    let direction: ZhilianMessageAnchor['direction'] = from
      ? from === staffID ? 'out' : 'in'
      : 'system'
    let kind: 'text' | 'card' | 'system' = 'system'
    let normalizedText = ''
    let cardType: 'wechatExchange' | null = null
    let identity = ''
    if (rawType === 'text') {
      if (!from) return null
      kind = 'text'
      normalizedText = clean(row.text)
    } else if (customType === 105) {
      if (!from) return null
      kind = 'card'
      cardType = 'wechatExchange'
      normalizedText = clean(
        direction === 'out'
          ? details.staffContent ?? details.senderText ?? details.detail
          : details.userContent ?? details.receiverText ?? details.detail,
      ) || '[交换微信请求]'
      identity = clean(details.requestId ?? details.id ?? details.cardId)
    } else if (customType === 131) {
      if (!from) return null
      kind = 'text'
      normalizedText = clean(details.greetingText ?? envelope.greetingText)
    } else if (rawType === 'custom' && customType === 148 && Boolean(from) && direction === 'in' &&
      clean(row.status).toLowerCase() === 'success' && clean(details.staffText)) {
      kind = 'text'
      normalizedText = clean(details.staffText)
    } else {
      direction = 'system'
      normalizedText = clean(
        details.staffText ?? details.userText ?? details.title ?? details.content ??
        envelope.msgb ?? envelope.msgc ?? envelope.text ?? row.text,
      ) || `[系统消息:${Number.isFinite(customType) ? customType : clean(rawType) || 'unknown'}]`
    }
    if (direction === 'out' && clean(row.status).toLowerCase() !== 'success') return null
    const contentHash = kind === 'card'
      ? digest(`card\x1f${cardType ?? 'other'}\x1f${clean(identity || row.idServer || normalizedText)}`)
      : digest(clean(normalizedText))
    return { direction, contentHash }
  }
  const liveTailMatches = (rows: ActionSnapshotRow[]): boolean | null => {
    if (expectedTail.length === 0) return true
    if (rows.length < expectedTail.length) return false
    const staffID = runtimeStaffID()
    if (!staffID) return null
    const tail = rows.slice(-expectedTail.length)
    return tail.every((row, index) => {
      const actual = actionAnchorFor(row, staffID)
      const expected = expectedTail[index]
      return actual !== null && actual.direction === expected.direction && actual.contentHash === expected.contentHash
    })
  }
  const baselineState = (): 'match' | 'unresolved' | 'changed' => {
    const actual = liveTimelineProjection()
    if (!actual) return 'unresolved'
    if (actual.sourceKeys.length !== expectedBaselineServerSourceKeys.length ||
        actual.sourceKeys.some((key, index) => key !== expectedBaselineServerSourceKeys[index])) return 'changed'
    const tailMatches = liveTailMatches(actual.windowRows)
    return tailMatches === null ? 'unresolved' : tailMatches ? 'match' : 'changed'
  }
  const targetBindingToken = (): string | null => {
    const engine = asRecord(w.imEngine)
    const sessions = Array.isArray(engine?.sessions) ? engine.sessions as AnyRecord[] : []
    const matches = sessions.filter((item) => clean(item.sessionId) === conversationRef)
    const target = matches.length === 1 ? clean(matches[0].peerPartnerId) : ''
    return target ? digest(JSON.stringify([conversationRef, target])) : null
  }
  const surface = (): {
    detail: HTMLElement
    wrapper: HTMLElement
    composer: HTMLTextAreaElement
    button: HTMLButtonElement
  } | null => {
    const details = Array.from(document.querySelectorAll<HTMLElement>('.im-session-detail')).filter(visible)
    const composers = Array.from(document.querySelectorAll<HTMLTextAreaElement>(
      'textarea.km-input__original.is-normal.is-textarea.is-autoresize',
    )).filter((element) => visible(element) && element.closest('.im-sender__input-wrapper') !== null)
    if (details.length !== 1 || composers.length !== 1) return null
    const detail = details[0]
    const composer = composers[0]
    const wrapper = composer.closest<HTMLElement>('.im-sender__input-wrapper')
    if (!wrapper || composer.closest('.im-session-detail') !== detail || wrapper.closest('.im-session-detail') !== detail) {
      return null
    }
    const buttons = Array.from(document.querySelectorAll<HTMLButtonElement>('.im-sender__input-wrapper button'))
      .filter((element) => visible(element) && clean(element.textContent) === '发送')
    if (buttons.length !== 1 || buttons[0].closest('.im-session-detail') !== detail ||
        buttons[0].closest('.im-sender__input-wrapper') !== wrapper) return null
    if (buttons[0].form !== null && buttons[0].type !== 'button') return null
    return {
      detail,
      wrapper,
      composer,
      button: buttons[0],
    }
  }
  type SendSurface = NonNullable<ReturnType<typeof surface>>
  type SendFailureReason = NonNullable<MainSendOnceResult['reason']>
  type TextareaValueSetter = (this: HTMLTextAreaElement, value: string) => void
  type IntrinsicClick = (this: HTMLElement) => void
  interface EvaluatedSendState {
    surface: SendSurface
    setter: TextareaValueSetter
    intrinsicClick: IntrinsicClick
  }
  type SendEvaluation =
    | { status: 'ready'; state: EvaluatedSendState }
    | { status: 'failed'; reason: SendFailureReason }
  const failedEvaluation = (reason: SendFailureReason): SendEvaluation => ({ status: 'failed', reason })

  // preflight、commit 输入前、commit 输入后三个检查点字面复用这一份 evaluator。
  // DOM 只证明可见控件唯一且互相包含；消息语义只来自所选 live timeline 通道。
  const evaluate = (expectedComposerValue: string): SendEvaluation => {
    const surfaceFailure: SendFailureReason = expectedComposerValue === ''
      ? 'composer_missing'
      : 'input_rejected'
    const valueFailure: SendFailureReason = expectedComposerValue === ''
      ? 'composer_nonempty'
      : 'input_rejected'
    if (!Number.isFinite(irreversibleNotAfterMs) || Date.now() > irreversibleNotAfterMs) {
      return failedEvaluation('action_window_elapsed')
    }
    if (!currentRoute()) return failedEvaluation('route_changed')
    if (clean(text) === '') return failedEvaluation('input_rejected')
    const principal = principalCanonical()
    if (!principal || digest(principal) !== expectedPrincipalFingerprint) {
      return failedEvaluation('identity_changed')
    }
    const bindingToken = targetBindingToken()
    if (!bindingToken) return failedEvaluation('guard_unresolved')
    if (bindingToken !== expectedTargetBindingToken) return failedEvaluation('target_changed')

    const currentSurface = surface()
    if (!currentSurface) return failedEvaluation(surfaceFailure)
    if (currentSurface.composer.value !== expectedComposerValue) return failedEvaluation(valueFailure)
    const currentBaselineState = baselineState()
    if (currentBaselineState !== 'match') {
      return failedEvaluation(currentBaselineState === 'changed' ? 'baseline_changed' : 'guard_unresolved')
    }
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set as
      TextareaValueSetter
    const intrinsicClick = HTMLElement.prototype.click as IntrinsicClick
    if (Date.now() > irreversibleNotAfterMs) return failedEvaluation('action_window_elapsed')
    return {
      status: 'ready',
      state: {
        surface: currentSurface,
        setter,
        intrinsicClick,
      },
    }
  }

  const prepared = evaluate('')
  if (prepared.status === 'failed') return prepared
  if (phase === 'preflight') return { status: 'ready' }

  // 从写入正文到物理 click 之间任何 getter/event/surface 异常都必须
  // 强制把本次自动草稿还原为空。不能以 value===text 为前提：页面可能
  // 同步规范化正文或在 input 期间重绘，这些分支也不得留下自动草稿。
  const restoreDraft = (): void => {
    try { prepared.state.setter.call(prepared.state.surface.composer, '') } catch { /* 继续尝试用事件清理模型 */ }
    try {
      prepared.state.surface.composer.dispatchEvent(new InputEvent('input', {
        bubbles: true, inputType: 'deleteContentBackward', data: null,
      }))
    } catch { /* change 仍可能清理页面模型 */ }
    try {
      prepared.state.surface.composer.dispatchEvent(new Event('change', { bubbles: true }))
    } catch { /* best effort */ }
  }
  const failAfterInput = (reason: SendFailureReason): MainSendOnceResult => {
    restoreDraft()
    return { status: 'failed', reason }
  }
  try {
    prepared.state.setter.call(prepared.state.surface.composer, text)
    prepared.state.surface.composer.dispatchEvent(new InputEvent('input', {
      bubbles: true, inputType: 'insertText', data: text,
    }))
    prepared.state.surface.composer.dispatchEvent(new Event('change', { bubbles: true }))
    const finalEvaluation = evaluate(text)
    if (finalEvaluation.status === 'failed') return failAfterInput(finalEvaluation.reason)
    const invokeClick = Function.prototype.call.bind(
      finalEvaluation.state.intrinsicClick,
      finalEvaluation.state.surface.button,
    )
    // evaluator 返回后不再读取任何可变页面状态；立即执行唯一一次已冻结 click。
    invokeClick()
    return { status: 'clicked' }
  } catch (error) {
    restoreDraft()
    throw error
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
  expectedPrincipalFingerprint: string,
  ctx: PrimitiveContext,
): Promise<boolean> {
  if (tab.id === undefined) throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  let selected = ''
  try {
    selected = new URL(tab.url ?? '').searchParams.get('sessionId') ?? ''
  } catch {
    // verifiedIMTab 已确认 IM route；URL 解析失败仍按需要导航处理。
  }
  if (selected === conversationRef) return false
  const finder = await runMain(tab.id, mainFindConversation, [conversationRef])
  if (finder.status !== 'found') {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      `无法按完整会话标识唯一定位目标会话：${finder.reason ?? 'unknown'}`,
      'manualOnly',
    )
  }
  // 长 finder 之后必须重新穿过 dispatcher 取消闸和账号闸；此前不建立命令导航窗口。
  ctx.checkpoint()
  const beforeClick = await chrome.tabs.get(tab.id)
  assertExpectedPrincipal(await probeTab(beforeClick), expectedPrincipalFingerprint)
  let beforeClickSelected = ''
  try {
    const beforeClickURL = new URL(beforeClick.url ?? '')
    if (beforeClickURL.pathname !== '/app/im') {
      throw new Error('not im')
    }
    beforeClickSelected = beforeClickURL.searchParams.get('sessionId') ?? ''
  } catch {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '定位会话期间页面离开智联沟通页', 'manualOnly')
  }
  if (beforeClickSelected === conversationRef) return false
  if (beforeClickSelected !== selected) {
    throw new ZhilianPlatformError('USER_ACTIVE', '定位会话期间当前会话被人工切换，已取消自动点击', 'afterRecovery')
  }
  ctx.checkpoint()
  // 打开会话可能触发已读等外部效果，因此必须消费本 intrusive 命令唯一的
  // cancellation barrier。barrier 后 cancel 不再产生“终局已取消但迟到点击”。
  await ctx.beforeSideEffect()
  const clickNotAfterMs = Math.min(ctx.irreversibleNotAfterMs, Date.now() + 1_500)
  const commandNavigation = beginCommandNavigation(tab.id, clickNotAfterMs)
  try {
    const click = await runMain(tab.id, mainClickConversationOnce, [
      conversationRef,
      selected,
      expectedPrincipalFingerprint,
      clickNotAfterMs,
    ])
    if (click.status === 'failed') {
      if (click.reason === 'composer_nonempty') {
        throw new ZhilianPlatformError('USER_ACTIVE', '当前会话存在人工草稿，拒绝自动切换', 'afterRecovery')
      }
      if (click.reason === 'identity_changed') {
        throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '切换会话前登录身份发生变化', 'manualOnly')
      }
      if (click.reason === 'action_window_elapsed') {
        throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '会话切换动作窗口已过，未点击', 'manualOnly')
      }
      if (click.reason === 'route_changed') {
        throw new ZhilianPlatformError('USER_ACTIVE', '点击前当前会话被切换，已取消自动点击', 'afterRecovery')
      }
      throw new ZhilianPlatformError(
        'ELEMENT_UNRESOLVED',
        `会话行在点击前无法再次确证：${click.reason ?? 'unknown'}`,
        'manualOnly',
      )
    }
    ctx.checkpoint()
    for (let attempt = 0; attempt < 40; attempt += 1) {
      ctx.checkpoint()
      const latest = await chrome.tabs.get(tab.id)
      try {
        const url = new URL(latest.url ?? '')
        if (latest.status === 'complete' && url.pathname === '/app/im' &&
            url.searchParams.get('sessionId') === conversationRef && await contentScriptHealthy(tab.id)) {
          return true
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
      'manualOnly',
      undefined,
      'possible',
    )
  } finally {
    commandNavigation.end()
  }
}

export async function sendZhilianMessage(
  args: ZhilianSendArgs,
  guards: ZhilianSendGuards,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianSendData> {
  if (!expectedPrincipalFingerprint) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  const tab = await sendZhilianTab(args.conversationRef)
  if (tab.id === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  const normalizedText = normalizeZhilianMessageText(args.text)
  if (!normalizedText) {
    throw new ZhilianPlatformError('GUARD_FAILED', '规范化后的消息为空，拒绝发送', 'manualOnly')
  }
  const contentHash = await sha256Hex(normalizedText)

  // live Vuex 基线是消息语义的唯一权威；DOM 只在同一 evaluator 内定位发送控件。
  const rawSendBaseline = await runMain(tab.id, mainCaptureSendBaseline, [
    args.conversationRef,
    guards.expectedTail,
  ])
  const sendBaseline = validatedMainSendBaseline(rawSendBaseline)
  if (!sendBaseline) {
    throw new ZhilianPlatformError(
      'CTX_NOT_READY',
      '发送基线返回结构无效，拒绝进入不可逆动作',
      'afterRecovery',
      'pageBroken',
    )
  }
  if (sendBaseline.status === 'failed') {
    if (sendBaseline.stage === 'route_changed' || sendBaseline.stage === 'guard_snapshot_uncovered') {
      throw new ZhilianPlatformError('GUARD_FAILED', '发送基线在复核期间发生变化，拒绝发送', 'manualOnly')
    }
    throw new ZhilianPlatformError(
      'CTX_NOT_READY',
      '当前无法建立可信发送基线，拒绝进入不可逆动作',
      'afterRecovery',
      'pageBroken',
    )
  }

  const throwEvaluationFailure = (evaluation: MainSendOnceResult): never => {
    if (evaluation.reason === 'composer_nonempty') {
      throw new ZhilianPlatformError('USER_ACTIVE', '发送前输入框出现人工草稿，已取消点击', 'afterRecovery')
    }
    if (evaluation.reason === 'target_changed') {
      throw new ZhilianPlatformError('GUARD_FAILED', '发送前会话与候选人的目标绑定发生变化，已取消点击', 'manualOnly')
    }
    if (evaluation.reason === 'baseline_changed') {
      throw new ZhilianPlatformError('GUARD_FAILED', '发送前会话消息基线或尾锚发生变化，已取消点击', 'manualOnly')
    }
    if (evaluation.reason === 'route_changed') {
      throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '发送前目标会话发生切换，已取消点击', 'manualOnly')
    }
    if (evaluation.reason === 'identity_changed') {
      throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '发送前登录身份发生变化，已取消点击', 'manualOnly')
    }
    if (evaluation.reason === 'action_window_elapsed') {
      throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '不可逆动作窗口已过，已取消点击', 'manualOnly')
    }
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      `发送前页面控件无法确证：${evaluation.reason ?? 'unknown'}`,
      'manualOnly',
    )
  }
  const evaluatorArgs = [
    args.conversationRef,
    args.text,
    guards.expectedTail,
    expectedPrincipalFingerprint,
    ctx.irreversibleNotAfterMs,
    sendBaseline.serverSourceKeys,
    sendBaseline.targetBindingToken,
  ] as const

  // attempting 前的真实预检与最终点击使用字面同一份 MAIN evaluator 和同一组参数。
  ctx.checkpoint()
  const preflight = await runMain(tab.id, mainSendMessageOnce, [...evaluatorArgs, 'preflight'])
  if (preflight.status !== 'ready') throwEvaluationFailure(preflight)
  ctx.checkpoint()
  await ctx.beforeSideEffect()

  // commit 会重新执行同一 evaluator；最后一次绿色返回后不再读取页面，立即唯一 click。
  const action = await runMain(tab.id, mainSendMessageOnce, [
    ...evaluatorArgs,
    'commit',
  ])
  if (action.status !== 'clicked') throwEvaluationFailure(action)

  for (let attempt = 0; attempt < 20; attempt += 1) {
    ctx.checkpoint()
    try {
      const observed = await runMain(tab.id, mainObserveStableOutbound, [
        args.conversationRef,
        contentHash,
        sendBaseline.serverSourceKeys,
        sendBaseline.targetBindingToken,
      ])
      if (!observed.selected) {
        throw new ZhilianPlatformError(
          'CTX_LOST_DURING_EXEC',
          '点击后目标会话发生切换，无法确认发送后置条件',
          'manualOnly',
          undefined,
          'possible',
        )
      }
      if (observed.matchingNewServerMessages === 1) {
        try {
          assertExpectedPrincipal(await probeTab(await chrome.tabs.get(tab.id)), expectedPrincipalFingerprint)
        } catch (error) {
          throw new ZhilianPlatformError(
            'CTX_LOST_DURING_EXEC',
            `点击后账号身份无法复核：${asError(error).message}`,
            'manualOnly',
            undefined,
            'possible',
          )
        }
        const observedAt = Date.now()
        await ctx.progress('已从当前实时消息时间线确认唯一新已发文本', 100)
        return { conversationRef: args.conversationRef, contentHash, observedAt }
      }
    } catch (error) {
      if (error instanceof ZhilianPlatformError && error.sideEffect === 'possible') throw error
      // 页面短暂重绘或脚本读取失败只影响观察；绝不能据此触发第二次 click。
    }
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  throw new ZhilianPlatformError(
    'POSTCONDITION_UNCONFIRMED',
    '只点击了一次发送，但未在当前实时消息时间线确认唯一新 idServer 文本',
    'manualOnly',
    undefined,
    'possible',
  )
}

export async function readZhilianThread(
  args: ZhilianThreadArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianThreadPage> {
  if (!expectedPrincipalFingerprint) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
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
  // 真机已证实 getHistoryMsgs 在基础路由会拒绝：所有参数与 opaque cursor 先完成
  // 无副作用校验，再按完整 conversationRef 打开目标。切换本身可能产生已读，因此
  // ensureThreadRoute 返回 true 时已经消费了本命令唯一的 cancellation barrier。
  const routeConsumedBarrier = await ensureThreadRoute(
    tab,
    args.conversationRef,
    expectedPrincipalFingerprint,
    ctx,
  )
  const newerPrefixMask = Number(rawAnchorPrefixMask)
  let cursor: { endTime: number; lastMsgId: string } | null = threadCursor
    ? { endTime: threadCursor.endTime, lastMsgId: threadCursor.lastMsgId }
    : null
  let reachedTop = false
  let peer: MainThreadPageResult['peer'] = null
  const collected: Array<Omit<ZhilianThreadMessage, 'idx'> & { sourceKey: string }> = []
  const sourceSemantics = new Map<string, { direction: ZhilianThreadMessage['direction']; contentHash: string }>()
  let platformReadStarted = routeConsumedBarrier

  while (collected.length < maxMessages && !reachedTop) {
    ctx.checkpoint()
    await ctx.progress(`读取会话历史 ${collected.length}/${maxMessages}`, Math.min(95, 5 + collected.length))
    let page: MainThreadPageResult
    const cursorBefore = cursor
    if (!platformReadStarted) {
      // readThread 按最坏实现定为 idempotentReadReceipt；紧贴第一次平台读取设置取消安全点。
      // 该钩子抛出的 StopExecution 必须原样回到 Dispatcher，不能在平台错误映射中吞掉。
      await ctx.beforeSideEffect()
      platformReadStarted = true
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
    const pageSeen = new Map(sourceSemantics)
    const unseen = page.messages.filter((message) => {
      const seen = pageSeen.get(message.sourceKey)
      if (seen) {
        if (seen.direction !== message.direction || seen.contentHash !== message.contentHash) {
          throw new ZhilianPlatformError(
            'ELEMENT_UNRESOLVED',
            '同一稳定消息等值键的方向或正文哈希冲突',
            'manualOnly',
            undefined,
            'possible',
          )
        }
        return false
      }
      pageSeen.set(message.sourceKey, {
        direction: message.direction,
        contentHash: message.contentHash,
      })
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
      sourceSemantics.set(message.sourceKey, {
        direction: message.direction,
        contentHash: message.contentHash,
      })
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
    .map((message, idx) => ({ ...message, idx }))
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
  mainReadCurrentCandidate,
  mainReadCurrentResume,
  mainReadSourcingResume,
  mainSendGreetingOnce,
  mainCaptureSendBaseline,
  mainInspectSendSurface,
  mainFindConversation,
  mainClickConversationOnce,
  mainObserveStableOutbound,
  mainReadListPage,
  mainReadThreadPage,
  mainSendMessageOnce,
  ensureThreadRoute,
  runMain,
})
