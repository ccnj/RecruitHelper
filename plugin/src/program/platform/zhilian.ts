// 智联平台实现接缝。这里可以知道 host、路由、页面字段与 NIM 消息形态；
// base、脑端与协议只看平台无关的原语结果。

import type { PrimitiveContext } from '../registry'
import { beginCommandNavigation } from '../../base/navigation'
import type {
  CandidateApplySourcingFiltersArgs,
  CandidateApplySourcingFiltersData,
  CandidateCaptureResumeScreenshotArgs,
  CandidateReadCurrentData,
  CaptureScreenshotData,
  ChatCaptureThreadScreenshotArgs,
  CandidateReadResumeArgs,
  CandidateReadResumeData,
  CandidateReadSourcingResumeArgs,
  CandidateReadSourcingResumeData,
  CandidateReadSourcingTargetResumeArgs,
  CandidateReadSourcingWindowArgs,
  CandidateReadSourcingWindowData,
  CandidateResumeLabelValue,
  CandidateSelectSourcingPositionArgs,
  CandidateSelectSourcingPositionData,
  CandidateSourcingFilters,
  ChatAcceptWechatArgs,
  ChatAcceptWechatData,
  ChatOpenConversationArgs,
  ChatOpenConversationData,
  ChatReadGreetingOutcomeArgs,
  ChatReadGreetingOutcomeData,
  ChatIdentifyCurrentConversationData,
  ChatReadListArgs,
  ChatReadListData,
  ChatReadThreadArgs,
  ChatReadThreadData,
  ChatReadWechatExchangeOutcomeArgs,
  ChatReadWechatExchangeOutcomeData,
  ChatSendGreetingArgs,
  ChatSendGreetingData,
  ChatSendGreetingGuards,
  ChatSendInviteCardArgs,
  ChatSendInviteCardData,
  ChatSendMessageArgs,
  ChatSendMessageData,
  ChatSendMessageGuards,
  ChatSendWechatInviteArgs,
  ChatSendWechatInviteData,
  ConversationSummary,
  DebugInspectSendSurfaceData,
  DebugProbeInterviewEditorArgs,
  DebugProbeInterviewEditorData,
  ErrorCode,
  InterviewDetails,
  JobPrepareDraftArgs,
  JobPrepareDraftData,
  JobPublishDraftData,
  JobPublishDraftGuards,
  JobReadPublishedListData,
  MessageAnchor,
  NotReadyReason,
  ProbePlatformData,
  Retryable,
  SideEffect,
  ThreadMessage,
} from '../../base/protocol'
import {
  Primitive as PrimitiveName,
  validatePrimitiveArgs,
  validatePrimitiveData,
} from '../../base/protocol'
import {
  BlobChannelError,
  BlobPutOutcome,
  captureVisibleTabJpegDataUrl,
  putSessionBlob,
  sessionBlobParams,
} from '../../base/capture'

export const ZHILIAN_PLATFORM = 'zhilian'
export const ZHILIAN_HOST = 'rd6.zhaopin.com'
export const ZHILIAN_IM_URL = `https://${ZHILIAN_HOST}/app/im`
export const ZHILIAN_RECOMMEND_URL = `https://${ZHILIAN_HOST}/app/recommend`

const TAB_QUERY = `https://${ZHILIAN_HOST}/*`
// 历史消息每次只取 8 条，保证单页即便字段接近 schema 上限也不会顶穿 64KiB data 门禁。
const THREAD_PAGE_SIZE = 8
const LIST_WINDOW_MAX_SESSIONS = 32
const RESULT_DATA_BUDGET = 60 * 1024

export class ZhilianPlatformError extends Error {
  constructor(
    readonly code: ErrorCode,
    message: string,
    readonly retryable: Retryable = 'afterRecovery',
    readonly reason?: NotReadyReason,
    readonly sideEffect: SideEffect = 'none',
    // 失败现场快照,只给人读:哪一步、当时页面处于什么状态。它进 error.data
    // (契约里是 raw 对象),不参与任何业务判定,也不进 evidence。
    readonly diagnostics?: Record<string, unknown>,
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
export type ZhilianSourcingTargetResumeArgs = CandidateReadSourcingTargetResumeArgs
export type ZhilianSourcingWindowArgs = CandidateReadSourcingWindowArgs
export type ZhilianSourcingWindowData = CandidateReadSourcingWindowData
export type ZhilianSelectSourcingPositionArgs = CandidateSelectSourcingPositionArgs
export type ZhilianSelectSourcingPositionData = CandidateSelectSourcingPositionData
export type ZhilianApplySourcingFiltersArgs = CandidateApplySourcingFiltersArgs
export type ZhilianApplySourcingFiltersData = CandidateApplySourcingFiltersData
export type ZhilianGreetingArgs = ChatSendGreetingArgs
export type ZhilianGreetingGuards = ChatSendGreetingGuards
export type ZhilianGreetingData = ChatSendGreetingData
export type ZhilianGreetingOutcomeArgs = ChatReadGreetingOutcomeArgs
export type ZhilianGreetingOutcomeData = ChatReadGreetingOutcomeData
export type ZhilianOpenConversationArgs = ChatOpenConversationArgs
export type ZhilianOpenConversationData = ChatOpenConversationData
export type ZhilianListArgs = ChatReadListArgs
export type ZhilianConversationSummary = ConversationSummary
export type ZhilianListPage = ChatReadListData
export type ZhilianMessageAnchor = MessageAnchor
export type ZhilianCurrentConversation = ChatIdentifyCurrentConversationData
export type ZhilianThreadArgs = ChatReadThreadArgs
export type ZhilianThreadMessage = ThreadMessage
export type ZhilianThreadPage = ChatReadThreadData
export type ZhilianSendArgs = ChatSendMessageArgs
export type ZhilianSendGuards = ChatSendMessageGuards
export type ZhilianSendData = ChatSendMessageData
export type ZhilianSendWechatInviteArgs = ChatSendWechatInviteArgs
export type ZhilianSendWechatInviteData = ChatSendWechatInviteData
export type ZhilianAcceptWechatArgs = ChatAcceptWechatArgs
export type ZhilianAcceptWechatData = ChatAcceptWechatData
export type ZhilianReadWechatExchangeOutcomeArgs = ChatReadWechatExchangeOutcomeArgs
export type ZhilianReadWechatExchangeOutcomeData = ChatReadWechatExchangeOutcomeData
export type ZhilianSendInviteCardArgs = ChatSendInviteCardArgs
export type ZhilianSendInviteCardData = ChatSendInviteCardData

interface MainProbeResult {
  pageKind: 'im' | 'recommend' | 'other'
  loginState: 'in' | 'out' | 'unknown'
  principalFingerprint: string | null
  imListVisible: boolean
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
  'close_unavailable',
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

const MAIN_SOURCING_WINDOW_FAILURE_REASONS = [
  'route_changed',
  'list_source_unavailable',
  'candidate_identity_unavailable',
  'candidate_identity_duplicated',
  'position_identity_unavailable',
  'position_identity_mismatch',
  'position_title_ambiguous',
  'position_title_mismatch',
  'scroll_unavailable',
  'page_unstable',
  'unexpected',
] as const

type MainSourcingWindowFailureReason = typeof MAIN_SOURCING_WINDOW_FAILURE_REASONS[number]

interface MainSourcingWindowReady {
  status: 'ready'
  data: ZhilianSourcingWindowData
}

interface MainSourcingWindowFailed {
  status: 'failed'
  reason: MainSourcingWindowFailureReason
}

type MainSourcingWindowResult = MainSourcingWindowReady | MainSourcingWindowFailed

const MAIN_SELECT_SOURCING_POSITION_FAILURE_REASONS = [
  'route_changed',
  'trigger_cardinality',
  'drawer_cardinality',
  'drawer_not_ready',
  'item_source_unavailable',
  'target_absent',
  'target_ambiguous',
  'active_item_ambiguous',
  'selection_not_confirmed',
  'close_unavailable',
  'close_not_confirmed',
  'position_identity_unavailable',
  'position_title_ambiguous',
  'position_title_mismatch',
  'page_unstable',
  'unexpected',
] as const

type MainSelectSourcingPositionFailureReason =
  typeof MAIN_SELECT_SOURCING_POSITION_FAILURE_REASONS[number]

interface MainSelectSourcingPositionReady {
  status: 'ready'
  data: ZhilianSelectSourcingPositionData
}

interface MainSelectSourcingPositionFailed {
  status: 'failed'
  reason: MainSelectSourcingPositionFailureReason
}

type MainSelectSourcingPositionResult =
  MainSelectSourcingPositionReady | MainSelectSourcingPositionFailed

const MAIN_APPLY_SOURCING_FILTERS_FAILURE_REASONS = [
  'route_changed',
  'position_identity_unavailable',
  'position_identity_mismatch',
  'position_title_ambiguous',
  'position_title_mismatch',
  'trigger_cardinality',
  'drawer_cardinality',
  'drawer_not_ready',
  'group_cardinality',
  'group_title_mismatch',
  'option_set_mismatch',
  'selection_unreadable',
  'custom_selector_unavailable',
  'range_select_unavailable',
  'range_option_unavailable',
  'filter_mismatch',
  'confirm_unavailable',
  'confirm_not_closed',
  'list_unavailable',
  'list_unstable',
  'cancel_unavailable',
  'cancel_not_closed',
  'unexpected',
] as const

type MainApplySourcingFiltersFailureReason =
  typeof MAIN_APPLY_SOURCING_FILTERS_FAILURE_REASONS[number]

interface MainApplySourcingFiltersReady {
  status: 'ready'
  data: ZhilianApplySourcingFiltersData
}

interface MainApplySourcingFiltersFailed {
  status: 'failed'
  reason: MainApplySourcingFiltersFailureReason
}

type MainApplySourcingFiltersResult =
  MainApplySourcingFiltersReady | MainApplySourcingFiltersFailed

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

const MAIN_GREETING_LIST_TARGET_FAILURE_REASONS = [
  'route_mismatch',
  'detail_present',
  'list_source_unavailable',
  'candidate_identity_unavailable',
  'candidate_identity_duplicated',
  'target_absent',
  'unexpected',
] as const

type MainGreetingListTargetFailureReason = typeof MAIN_GREETING_LIST_TARGET_FAILURE_REASONS[number]
type MainGreetingListTargetResult =
  | {
    status: 'ready'
    data: { contactState: ZhilianCurrentCandidate['contactState'] }
  }
  | { status: 'failed'; reason: MainGreetingListTargetFailureReason }

interface MainListDOMWindowResult {
  sessions: ZhilianConversationSummary[]
  atBottom: boolean
  moved: boolean
  scrollHeight: number
  scrollTop: number
  unstable: boolean
}

type MainChatListFilterResult =
  | { status: 'ready'; changed: boolean }
  | { status: 'needs_action' }
  | {
    status: 'failed'
    reason: 'route_changed' | 'job_trigger_cardinality' | 'job_label_unreadable' |
      'job_option_cardinality' | 'job_selection_unconfirmed' |
      'unread_control_cardinality' | 'unread_control_unsafe' |
      'unread_selection_unconfirmed' | 'unexpected'
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
  unreadMarkerCleared?: boolean
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

type MainCardAction = 'wechatInvite' | 'interviewInvite'
type MainCardEvaluatorAction = MainCardAction | 'wechatAccept'
type MainCardPhase = 'preflight' | 'commit'

interface MainPreparedInterviewEditor {
  startsAt: number
  endsAt: number
  method: 'wechatVideo'
  dateValue: string
  timeValue: string
  durationValue: string
  methodValue: string
}

type MainPrepareInterviewEditorResult =
  | { status: 'ready'; prepared: MainPreparedInterviewEditor }
  | {
    status: 'failed'
    reason: 'route_changed' | 'identity_changed' | 'target_changed' | 'composer_nonempty' |
      'surface_unavailable' | 'editor_unavailable' | 'date_unavailable' | 'time_unavailable' |
      'duration_unavailable' | 'method_unavailable' | 'input_rejected' | 'action_window_elapsed' |
      'unexpected'
    // 失败点定位与白名单现场快照（只含选择器命中数、控件三态和我方自设值的
    // 回读），随 error.message 上行帮助脑侧诊断；绝不携带候选人/联系人字段。
    detail?: string
  }

type MainSendCardOnceResult =
  | { status: 'ready'; wechatCopyCards?: number }
  // wechatCopyCards：click 前观察到的“复制微信号”形态请求卡数量，只在
  // wechatAccept 下给出，作为可见后置状态正证的比较基准（协议规格 §9.3）。
  | { status: 'clicked'; wechatCopyCards?: number }
  | {
    status: 'failed'
    reason: 'route_changed' | 'identity_changed' | 'target_changed' | 'baseline_changed' |
      'guard_unresolved' | 'composer_nonempty' | 'surface_unavailable' | 'input_rejected' |
      'action_window_elapsed'
    // 失败点细分：只含选择器命中数与我方自设常量，随 error.message 上行帮助
    // 脑侧诊断该改哪一处判据；绝不携带候选人/联系人字段或任何页面文本。
    detail?: string
  }

interface MainObserveStableOutboundCardResult {
  selected: boolean
  matchingNewServerMessages: number
  contentHash?: string
  sourceKey?: string
  interview?: InterviewDetails
}

interface MainWechatExchangeOutcomeResult {
  confirmed: boolean
  exchangeSourceKey?: string
  peerWechat?: string
  // 请求卡的可见后置状态观测：只在 chat.acceptWechat 的正证判定里使用，
  // 不进契约、不进 readonly 面的 data。只有路由、目标绑定与请求锚全部复核
  // 通过时才带出，否则整体缺席（未解析）。
  surface?: {
    // 仍有可见同意动作的待处理请求卡数量。
    pendingRequestCards: number
    // 已转入“复制微信号”可见形态的请求卡数量。
    copyWechatCards: number
  }
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

function encodeCursor(value: ThreadCursor): string {
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

function decodeCursor(value: string | null | undefined): ThreadCursor | null {
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
    return parsed as ThreadCursor
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
  const randomDelayMs = (minimumMs: number, maximumMs: number): number =>
    minimumMs + Math.floor(Math.random() * (maximumMs - minimumMs + 1))
  const sleep = (delayMs: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, delayMs))
  const closeOpenedModal = async (
    ownedModal: HTMLElement,
    closeNotBefore: number,
  ): Promise<MainResumeFailed | null> => {
    try {
      while (Date.now() < closeNotBefore) {
        await sleep(closeNotBefore - Date.now())
      }
      let opened = visibleAll(document, '.new-shortcut-resume__modal')
      if (opened.length !== 1) {
        return failed(opened.length === 0 ? 'close_unavailable' : 'modal_cardinality')
      }
      if (opened[0] !== ownedModal) return failed('stale_modal')
      const closeButtons = visibleAll(opened[0], '.new-shortcut-resume__close')
      if (closeButtons.length !== 1) return failed('close_unavailable')
      closeButtons[0].click()

      const closeUntil = Date.now() + 10_000
      while (true) {
        opened = visibleAll(document, '.new-shortcut-resume__modal')
        if (opened.length === 0) return null
        if (opened.length !== 1) return failed('modal_cardinality')
        const remainingMs = closeUntil - Date.now()
        if (remainingMs <= 0) return failed('close_unavailable')
        await sleep(Math.min(120, remainingMs))
      }
    } catch {
      return failed('close_unavailable')
    }
  }
  let ownedModal: HTMLElement | null = null
  let closeNotBefore = 0
  let cleanupAttempted = false
  const cleanupOpenedModal = async (): Promise<MainResumeFailed | null> => {
    if (!ownedModal || cleanupAttempted) return null
    cleanupAttempted = true
    return closeOpenedModal(ownedModal, closeNotBefore)
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

    const openNotBefore = Date.now() + randomDelayMs(1_000, 1_500)
    while (Date.now() < openNotBefore) {
      await sleep(openNotBefore - Date.now())
    }
    if (!routeMatches() || !targetMatches()) return failed('target_changed')
    if (visibleAll(document, '.new-shortcut-resume__modal').length !== 0) return failed('stale_modal')
    const currentDetails = visibleAll(document, '.im-session-detail')
    if (currentDetails.length !== 1 || currentDetails[0] !== detail) return failed('target_changed')
    const currentEntries = visibleAll(detail, '.hover-resume-footer__button, button, a, [role="button"]')
      .filter((element) => clean(element.textContent) === '查看详情' &&
        element.closest('.im-session-detail') === detail)
    if (currentEntries.length !== 1 || currentEntries[0] !== entries[0]) return failed('target_changed')
    currentEntries[0].click()
    let modals: HTMLElement[] = []
    const waitUntil = Date.now() + 6_000
    while (Date.now() < waitUntil) {
      modals = visibleAll(document, '.new-shortcut-resume__modal')
      if (modals.length !== 0) break
      await sleep(120)
    }
    if (modals.length !== 1) return failed('modal_cardinality')
    ownedModal = modals[0]
    closeNotBefore = Date.now() + randomDelayMs(2_000, 2_500)
    const modal = ownedModal
    const readResult = (() : MainResumeResult => {
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
    })()
    const cleanupFailure = await cleanupOpenedModal()
    return cleanupFailure ?? readResult
  } catch {
    const cleanupFailure = await cleanupOpenedModal()
    return cleanupFailure ?? failed('unexpected')
  }
}

// 正式采集批次的职位选择 evaluator。它只操作推荐页的职位选择器，绝不触碰
// 候选人卡片上的招呼、电话或详情入口。最终结果只要求公开路由与可见活动职位
// 连续稳定；候选列表与内部职位源留给紧接的 readSourcingWindow 独立回读。
async function mainSelectSourcingPosition(
  requestedPositionTitle: string,
): Promise<MainSelectSourcingPositionResult> {
  type FinalSnapshot =
    | {
      status: 'ready'
      positionRef: string
      positionTitle: string
    }
    | { status: 'failed'; reason: MainSelectSourcingPositionFailureReason }

  const opaque = (value: unknown): string => {
    if (typeof value === 'string') return value.trim()
    if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
    return ''
  }
  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/[\s\u00a0]+/gu, ' ')
    .trim()
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const visibleAll = (root: ParentNode, selector: string): HTMLElement[] =>
    Array.from(root.querySelectorAll<HTMLElement>(selector)).filter(visible)
  const failed = (
    reason: MainSelectSourcingPositionFailureReason,
  ): MainSelectSourcingPositionFailed => ({ status: 'failed', reason })
  const sleep = (delayMs: number): Promise<void> =>
    new Promise((resolve) => setTimeout(resolve, delayMs))
  const randomInteractionGapMs = (): number =>
    1_000 + Math.floor(Math.random() * 501)
  let nextInteractionNotBefore = 0
  const interact = async (action: () => void): Promise<void> => {
    const delayMs = nextInteractionNotBefore - Date.now()
    if (delayMs > 0) await sleep(delayMs)
    action()
    nextInteractionNotBefore = Date.now() + randomInteractionGapMs()
  }
  const waitFor = async <T>(read: () => T | null, timeoutMs = 10_000): Promise<T | null> => {
    const deadline = Date.now() + timeoutMs
    while (true) {
      const value = read()
      if (value !== null) return value
      if (Date.now() >= deadline) return null
      await sleep(120)
    }
  }
  const drawers = (): HTMLElement[] => visibleAll(document, '.job-side-selector')
  const titleOf = (item: HTMLElement): string => {
    const titleNode = item.querySelector<HTMLElement>('.job-side-selector__title')
    if (!titleNode) return ''
    let title = clean(titleNode.textContent)
    const decorations = Array.from(titleNode.querySelectorAll<HTMLElement>(
      '.job-tag-withdrawn, .job-tag-coordination, .icon-eye',
    )).map((node) => clean(node.textContent)).filter(Boolean)
    for (const decoration of decorations) {
      if (title.startsWith(decoration)) {
        title = clean(title.slice(decoration.length))
      } else if (title.endsWith(decoration)) {
        title = clean(title.slice(0, -decoration.length))
      } else {
        return ''
      }
    }
    return title
  }
  const closeOwnedDrawer = async (): Promise<MainSelectSourcingPositionFailureReason | null> => {
    const openDrawers = drawers()
    if (openDrawers.length === 0) return null
    if (openDrawers.length !== 1) return 'drawer_cardinality'
    const drawer = openDrawers[0]
    const root = drawer.closest<HTMLElement>('.km-modal__wrapper--right.job-side-selector') ?? drawer
    const closeButtons = visibleAll(root, '.km-modal__close-btn')
    if (closeButtons.length !== 1) return 'close_unavailable'
    await interact(() => closeButtons[0].click())
    const closed = await waitFor(() => drawers().length === 0 ? true : null)
    return closed === true ? null : 'close_not_confirmed'
  }
  const finalSnapshot = (targetTitle: string): FinalSnapshot => {
    let currentRoute: URL
    try {
      currentRoute = new URL(location.href)
    } catch {
      return failed('route_changed')
    }
    if (!currentRoute.pathname.startsWith('/app/recommend')) return failed('route_changed')
    const positionRef = opaque(currentRoute.searchParams.get('jobNumber'))
    if (!positionRef) return failed('position_identity_unavailable')

    const activeTitles = visibleAll(
      document,
      '.job-pane__item--active .job-pane__item-job-title',
    ).map((element) => clean(element.textContent)).filter(Boolean)
    if (activeTitles.length !== 1) return failed('position_title_ambiguous')
    if (activeTitles[0] !== targetTitle) return failed('position_title_mismatch')

    return {
      status: 'ready',
      positionRef,
      positionTitle: activeTitles[0],
    }
  }

  let ownsDrawer = false
  try {
    const targetTitle = clean(requestedPositionTitle)
    if (!targetTitle || targetTitle.length > 256) return failed('target_absent')
    let currentRoute: URL
    try {
      currentRoute = new URL(location.href)
    } catch {
      return failed('route_changed')
    }
    if (!currentRoute.pathname.startsWith('/app/recommend')) return failed('route_changed')
    if (drawers().length !== 0) return failed('drawer_cardinality')

    const triggers = visibleAll(document, 'a[zp-stat-id="talent_more_jobs"]')
    if (triggers.length !== 1) return failed('trigger_cardinality')
    await interact(() => triggers[0].click())
    ownsDrawer = true

    const drawer = await waitFor(() => {
      const openDrawers = drawers()
      return openDrawers.length === 1 ? openDrawers[0] : null
    })
    if (!drawer) return failed('drawer_not_ready')
    if (drawers().length !== 1) return failed('drawer_cardinality')
    let latestTitledItems: Array<{ item: HTMLElement; title: string }> | null = null
    const targetItems = await waitFor(() => {
      const items = visibleAll(drawer, '.job-side-selector__item')
      if (items.length === 0) return null
      const titledItems = items.map((item) => ({ item, title: titleOf(item) }))
      if (titledItems.some(({ title }) => !title)) return null
      latestTitledItems = titledItems
      const matches = titledItems.filter(({ title }) => title === targetTitle)
      return matches.length > 0 ? { items, matches } : null
    })
    if (!targetItems) {
      const closeFailure = await closeOwnedDrawer()
      return failed(closeFailure ?? (latestTitledItems ? 'target_absent' : 'item_source_unavailable'))
    }
    const { items, matches } = targetItems
    if (matches.length !== 1) {
      const closeFailure = await closeOwnedDrawer()
      return failed(closeFailure ?? 'target_ambiguous')
    }
    const activeItems = items.filter((item) => item.classList.contains('is-active'))
    if (activeItems.length > 1) {
      const closeFailure = await closeOwnedDrawer()
      return failed(closeFailure ?? 'active_item_ambiguous')
    }
    const target = matches[0].item
    if (!target.classList.contains('is-active')) {
      if (typeof target.scrollIntoView !== 'function') {
        const closeFailure = await closeOwnedDrawer()
        return failed(closeFailure ?? 'item_source_unavailable')
      }
      await interact(() => target.scrollIntoView({ block: 'center', inline: 'nearest' }))
      await interact(() => target.click())
      const selected = await waitFor(() => {
        const openDrawers = drawers()
        if (openDrawers.length === 0) return true
        if (openDrawers.length !== 1) return null
        const currentMatches = visibleAll(openDrawers[0], '.job-side-selector__item')
          .filter((item) => titleOf(item) === targetTitle)
        return currentMatches.length === 1 && currentMatches[0].classList.contains('is-active')
          ? true
          : null
      })
      if (selected !== true) {
        const closeFailure = await closeOwnedDrawer()
        return failed(closeFailure ?? 'selection_not_confirmed')
      }
    }

    const closeFailure = await closeOwnedDrawer()
    if (closeFailure) return failed(closeFailure)
    ownsDrawer = false

    const settleUntil = Date.now() + 10_000
    let previousSignature = ''
    let stableRounds = 0
    let latestFailure: MainSelectSourcingPositionFailureReason = 'page_unstable'
    while (Date.now() <= settleUntil) {
      const snapshot = finalSnapshot(targetTitle)
      if (snapshot.status === 'ready') {
        const signature = `${snapshot.positionRef}|${snapshot.positionTitle}`
        stableRounds = signature === previousSignature ? stableRounds + 1 : 0
        previousSignature = signature
        if (stableRounds >= 2) {
          return {
            status: 'ready',
            data: {
              positionRef: snapshot.positionRef,
              positionTitle: snapshot.positionTitle,
              observedAt: Date.now(),
            },
          }
        }
      } else {
        if (snapshot.reason === 'route_changed') return snapshot
        latestFailure = snapshot.reason
        previousSignature = ''
        stableRounds = 0
      }
      await sleep(120)
    }
    return failed(latestFailure === 'page_unstable' ? 'page_unstable' : latestFailure)
  } catch {
    if (ownsDrawer) {
      try {
        await closeOwnedDrawer()
      } catch {
        // 主失败保持 unexpected；清理失败只意味着页面仍需人工收口。
      }
    }
    return failed('unexpected')
  }
}

// 正式采集批次的筛选 evaluator。六组 DOM 映射、差异覆盖、提交前回读与
// 提交后二次回读都封闭在同一个 MAIN task；readFilters 是三个阶段字面同一
// 份读取器。它不触碰候选人卡片、招呼、电话或详情入口。
async function mainApplySourcingFilters(
  requestedPositionRef: string,
  requestedPositionTitle: string,
  requestedFilters: ZhilianApplySourcingFiltersArgs['filters'],
): Promise<MainApplySourcingFiltersResult> {
  type AnyRecord = Record<string, unknown>
  type FilterKey =
    | 'age'
    | 'activeTime'
    | 'careerStatuses'
    | 'educations'
    | 'gender'
    | 'filterTypes'
  type OptionState = {
    node: HTMLElement
    label: string
    selected: boolean
  }
  type GroupState = {
    node: HTMLElement
    options: OptionState[]
    selectedLabels: string[]
  }
  type FilterSnapshot = {
    filters: CandidateSourcingFilters
    groups: Record<FilterKey, GroupState>
  }
  type FilterRead = FilterSnapshot | MainApplySourcingFiltersFailed
  type PositionRead =
    | { status: 'ready'; positionRef: string; positionTitle: string }
    | MainApplySourcingFiltersFailed
  type ListRead =
    | { status: 'ready'; signature: string }
    | MainApplySourcingFiltersFailed

  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/[\s\u00a0]+/gu, ' ')
    .trim()
  const asRecord = (value: unknown): AnyRecord | null =>
    value !== null && typeof value === 'object' && !Array.isArray(value)
      ? value as AnyRecord
      : null
  const opaque = (value: unknown): string => {
    if (typeof value === 'string') return value.trim()
    if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
    return ''
  }
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' &&
      node.getClientRects().length > 0
  }
  const visibleAll = (root: ParentNode, selector: string): HTMLElement[] =>
    Array.from(root.querySelectorAll<HTMLElement>(selector)).filter(visible)
  const failed = (
    reason: MainApplySourcingFiltersFailureReason,
  ): MainApplySourcingFiltersFailed => ({ status: 'failed', reason })
  const sleep = (delayMs: number): Promise<void> =>
    new Promise((resolve) => setTimeout(resolve, delayMs))
  const randomInteractionGapMs = (): number =>
    1_000 + Math.floor(Math.random() * 501)
  let nextInteractionNotBefore = 0
  const interact = async (action: () => void): Promise<void> => {
    const delayMs = nextInteractionNotBefore - Date.now()
    if (delayMs > 0) await sleep(delayMs)
    action()
    nextInteractionNotBefore = Date.now() + randomInteractionGapMs()
  }
  const waitFor = async <T>(read: () => T | null, timeoutMs = 10_000): Promise<T | null> => {
    const deadline = Date.now() + timeoutMs
    while (true) {
      const value = read()
      if (value !== null) return value
      if (Date.now() >= deadline) return null
      await sleep(120)
    }
  }
  const sameStringSet = (left: string[], right: string[]): boolean =>
    left.length === right.length &&
    [...left].sort().every((value, index) => value === [...right].sort()[index])
  const sameFilters = (
    left: CandidateSourcingFilters,
    right: CandidateSourcingFilters,
  ): boolean => JSON.stringify(left) === JSON.stringify(right)

  const careerOrder = [
    'employedLooking',
    'leftLooking',
    'employedOpen',
    'employedNotLooking',
  ] as const
  const educationOrder = [
    'juniorHighOrBelow',
    'highSchool',
    'secondaryVocational',
    'associate',
    'bachelor',
    'master',
    'mbaEmba',
    'doctorate',
  ] as const
  const normalizeFilters = (filters: CandidateSourcingFilters): CandidateSourcingFilters => ({
    age: filters.age.mode === 'any'
      ? { mode: 'any' }
      : {
          mode: 'range',
          minAge: filters.age.minAge as number,
          ...(filters.age.maxAge === undefined ? {} : { maxAge: filters.age.maxAge }),
        },
    activeWindow: filters.activeWindow,
    careerStatuses: careerOrder.filter((value) => filters.careerStatuses.includes(value)),
    educations: educationOrder.filter((value) => filters.educations.includes(value)),
    gender: filters.gender,
    excludeViewed: filters.excludeViewed,
    excludeCoworkerContacted: filters.excludeCoworkerContacted,
  })

  const schemas: Record<FilterKey, {
    title: string
    selector: string
    optionSelector: string
    labels: string[]
    control: 'checkbox' | 'radio'
  }> = {
    age: {
      title: '年龄要求',
      selector: '.filter-item-age',
      optionSelector: '.recommend-checkbox-group__item',
      labels: ['不限', '20-25', '25-30', '30-35', '35-40', '40以上', '自定义'],
      control: 'checkbox',
    },
    activeTime: {
      title: '活跃日期',
      selector: '.filter-item-activeTime',
      optionSelector: '.km-radio',
      labels: ['不限', '今日活跃', '3天内活跃', '7天内活跃', '30天内活跃'],
      control: 'radio',
    },
    careerStatuses: {
      title: '求职状态可多选',
      selector: '.filter-item-careerStatuses',
      optionSelector: '.recommend-checkbox-group__item',
      labels: ['不限', '在职-正在找工作', '离职-正在找工作', '在职-看看机会', '在职-暂不找工作'],
      control: 'checkbox',
    },
    educations: {
      title: '学历要求可多选',
      selector: '.filter-item-educations',
      optionSelector: '.recommend-checkbox-group__item',
      labels: ['不限', '初中及以下', '高中', '中专/中技', '大专', '本科', '硕士', 'MBA/EMBA', '博士'],
      control: 'checkbox',
    },
    gender: {
      title: '性别要求',
      selector: '.filter-item-gender',
      optionSelector: '.km-radio',
      labels: ['不限', '男', '女'],
      control: 'radio',
    },
    filterTypes: {
      title: '人才范围可多选',
      selector: '.filter-item-filterTypes',
      optionSelector: '.recommend-checkbox-group__item',
      labels: ['不限', '过滤我已看过', '过滤同事已聊'],
      control: 'checkbox',
    },
  }
  const filterKeys = Object.keys(schemas) as FilterKey[]

  const optionLabel = (option: HTMLElement): string => {
    const radioLabels = visibleAll(option, '.km-radio__label')
    if (radioLabels.length === 1) return clean(radioLabels[0].textContent)
    const firstSpan = option.querySelector<HTMLElement>('span')
    return clean(firstSpan?.textContent ?? option.textContent)
  }
  const selectText = (select: HTMLElement): string => {
    const display = select.querySelector<HTMLElement>('.km-input__custom, .km-input__inner')
    if (display) return clean(display.textContent)
    const input = select.querySelector<HTMLInputElement>('input')
    if (input && typeof input.value === 'string' && clean(input.value)) return clean(input.value)
    return clean(select.textContent)
  }
  const rangeToken = (value: string): string => {
    const normalized = clean(value).replace(/\s/gu, '').replace(/岁/gu, '')
    if (normalized === '及以上' || normalized === '不限') return 'open'
    const matched = normalized.match(/^\d+$/u)
    return matched ? matched[0] : ''
  }
  const ageFromSelected = (
    group: HTMLElement,
    selectedLabel: string,
  ): CandidateSourcingFilters['age'] | null => {
    const predefined: Record<string, CandidateSourcingFilters['age']> = {
      '不限': { mode: 'any' },
      '20-25': { mode: 'range', minAge: 20, maxAge: 25 },
      '25-30': { mode: 'range', minAge: 25, maxAge: 30 },
      '30-35': { mode: 'range', minAge: 30, maxAge: 35 },
      '35-40': { mode: 'range', minAge: 35, maxAge: 40 },
      '40以上': { mode: 'range', minAge: 40 },
    }
    if (selectedLabel !== '自定义') return predefined[selectedLabel] ?? null
    const selectors = visibleAll(group, '.recommend-checkbox-group__selector')
    if (selectors.length !== 1) return null
    const starts = visibleAll(selectors[0], '.filter-select-two__start .km-select')
    const ends = visibleAll(selectors[0], '.filter-select-two__end .km-select')
    if (starts.length !== 1 || ends.length !== 1) return null
    const minToken = rangeToken(selectText(starts[0]))
    const maxToken = rangeToken(selectText(ends[0]))
    if (!/^\d+$/u.test(minToken) || (!/^\d+$/u.test(maxToken) && maxToken !== 'open')) return null
    const minAge = Number(minToken)
    const maxAge = maxToken === 'open' ? undefined : Number(maxToken)
    if (!Number.isInteger(minAge) || minAge < 16 || minAge > 65 ||
        (maxAge !== undefined &&
          (!Number.isInteger(maxAge) || maxAge < minAge || maxAge > 65))) {
      return null
    }
    return {
      mode: 'range',
      minAge,
      ...(maxAge === undefined ? {} : { maxAge }),
    }
  }
  const enumLabels = {
    activeTime: {
      '不限': 'any',
      '今日活跃': 'today',
      '3天内活跃': 'days3',
      '7天内活跃': 'days7',
      '30天内活跃': 'days30',
    },
    careerStatuses: {
      '在职-正在找工作': 'employedLooking',
      '离职-正在找工作': 'leftLooking',
      '在职-看看机会': 'employedOpen',
      '在职-暂不找工作': 'employedNotLooking',
    },
    educations: {
      '初中及以下': 'juniorHighOrBelow',
      '高中': 'highSchool',
      '中专/中技': 'secondaryVocational',
      '大专': 'associate',
      '本科': 'bachelor',
      '硕士': 'master',
      'MBA/EMBA': 'mbaEmba',
      '博士': 'doctorate',
    },
    gender: {
      '不限': 'any',
      '男': 'male',
      '女': 'female',
    },
  } as const

  // 这是初读、提交前完整回读与提交后二次回读共用的字面同一份 evaluator。
  const readFilters = (drawer: HTMLElement): FilterRead => {
    const groups = {} as Record<FilterKey, GroupState>
    for (const key of filterKeys) {
      const schema = schemas[key]
      const matches = visibleAll(drawer, schema.selector)
      if (matches.length !== 1) return failed('group_cardinality')
      const group = matches[0]
      const titles = visibleAll(
        group,
        '.tr-talent-filter-item__title, .filter-group-major__title, .filter-item__title',
      )
      if (titles.length !== 1 || clean(titles[0].textContent) !== schema.title) {
        return failed('group_title_mismatch')
      }
      const optionNodes = visibleAll(group, schema.optionSelector)
      const options = optionNodes.map((node): OptionState | null => {
        const label = optionLabel(node)
        if (!label) return null
        if (schema.control === 'checkbox') {
          const active = node.classList.contains('recommend-checkbox-group__active')
          const inactive = node.classList.contains('recommend-checkbox-group__inactive')
          if (active === inactive) return null
          return { node, label, selected: active }
        }
        return {
          node,
          label,
          selected: node.classList.contains('km-radio--checked'),
        }
      })
      if (options.some((option) => option === null)) return failed('selection_unreadable')
      const concreteOptions = options as OptionState[]
      const labels = concreteOptions.map(({ label }) => label)
      if (new Set(labels).size !== labels.length || !sameStringSet(labels, schema.labels)) {
        return failed('option_set_mismatch')
      }
      const selectedLabels = concreteOptions.filter(({ selected }) => selected)
        .map(({ label }) => label)
      if (schema.control === 'radio' && selectedLabels.length !== 1) {
        return failed('selection_unreadable')
      }
      if (schema.control === 'checkbox' &&
          (selectedLabels.length === 0 ||
            (selectedLabels.includes('不限') && selectedLabels.length !== 1))) {
        return failed('selection_unreadable')
      }
      groups[key] = { node: group, options: concreteOptions, selectedLabels }
    }

    const age = ageFromSelected(groups.age.node, groups.age.selectedLabels[0])
    const activeWindow = enumLabels.activeTime[
      groups.activeTime.selectedLabels[0] as keyof typeof enumLabels.activeTime
    ]
    const gender = enumLabels.gender[
      groups.gender.selectedLabels[0] as keyof typeof enumLabels.gender
    ]
    if (!age || !activeWindow || !gender) return failed('selection_unreadable')
    const careerStatuses = groups.careerStatuses.selectedLabels
      .filter((label) => label !== '不限')
      .map((label) => enumLabels.careerStatuses[
        label as keyof typeof enumLabels.careerStatuses
      ])
    const educations = groups.educations.selectedLabels
      .filter((label) => label !== '不限')
      .map((label) => enumLabels.educations[label as keyof typeof enumLabels.educations])
    if (careerStatuses.some((value) => !value) || educations.some((value) => !value)) {
      return failed('selection_unreadable')
    }
    return {
      filters: normalizeFilters({
        age,
        activeWindow,
        careerStatuses,
        educations,
        gender,
        excludeViewed: groups.filterTypes.selectedLabels.includes('过滤我已看过'),
        excludeCoworkerContacted: groups.filterTypes.selectedLabels.includes('过滤同事已聊'),
      }),
      groups,
    }
  }

  const position = (): PositionRead => {
    let route: URL
    try {
      route = new URL(location.href)
    } catch {
      return failed('route_changed')
    }
    if (!route.pathname.startsWith('/app/recommend')) return failed('route_changed')
    const currentRef = clean(route.searchParams.get('jobNumber'))
    if (!currentRef) return failed('position_identity_unavailable')
    if (currentRef !== requestedPositionRef) return failed('position_identity_mismatch')
    const titles = visibleAll(
      document,
      '.job-pane__item--active .job-pane__item-job-title',
    ).map((node) => clean(node.textContent)).filter(Boolean)
    if (titles.length !== 1) return failed('position_title_ambiguous')
    if (titles[0] !== requestedPositionTitle) return failed('position_title_mismatch')
    return { status: 'ready', positionRef: currentRef, positionTitle: titles[0] }
  }
  const drawerNodes = (): HTMLElement[] =>
    visibleAll(document, '.km-modal.km-modal--open.km-modal--right')
  const trigger = (): HTMLElement | null => {
    const matches = visibleAll(document, 'a[zp-stat-id="talent-recommend-filter-click"]')
    return matches.length === 1 ? matches[0] : null
  }
  const cancelButton = (drawer: HTMLElement): HTMLElement | null => {
    const matches = visibleAll(drawer, 'button').filter((button) => clean(button.textContent) === '取消')
    return matches.length === 1 ? matches[0] : null
  }
  const listSnapshot = (): ListRead => {
    const currentPosition = position()
    if (currentPosition.status === 'failed') return currentPosition
    const items = visibleAll(document, '.recommend-list__left div[role="listitem"]')
    if (items.length === 0) return failed('list_unavailable')
    const identities = items.map((item) => {
      const owner = asRecord((item as HTMLElement & { __vue__?: unknown }).__vue__)
      const source = asRecord(asRecord(owner?._props)?.source)
      return opaque(source?.userMasterId)
    })
    if (identities.some((identity) => !identity) ||
        new Set(identities).size !== identities.length) {
      return failed('list_unavailable')
    }
    return {
      status: 'ready',
      signature: `${currentPosition.positionRef}|${identities.join('|')}`,
    }
  }
  const stableList = async (): Promise<ListRead> => {
    const deadline = Date.now() + 10_000
    let previous: ListRead | null = null
    while (true) {
      const current = listSnapshot()
      if (current.status === 'failed') {
        if (current.reason === 'route_changed' ||
            current.reason === 'position_identity_mismatch' ||
            current.reason === 'position_title_mismatch') {
          return current
        }
        previous = null
      } else if (previous?.status === 'ready' &&
          previous.signature === current.signature) {
        return current
      } else {
        previous = current
      }
      const remainingMs = deadline - Date.now()
      if (remainingMs <= 0) break
      await sleep(Math.min(1_000, remainingMs))
    }
    return failed('list_unstable')
  }
  const openDrawer = async (): Promise<HTMLElement | null> => {
    const openTrigger = trigger()
    if (!openTrigger) return null
    await interact(() => openTrigger.click())
    return waitFor(() => {
      const drawers = drawerNodes()
      return drawers.length === 1 ? drawers[0] : null
    })
  }
  const closeOwnedDrawer = async (drawer: HTMLElement): Promise<boolean> => {
    if (!drawer.isConnected && drawerNodes().length === 0) return true
    const cancel = cancelButton(drawer)
    if (!cancel) return false
    await interact(() => cancel.click())
    return await waitFor(() => drawerNodes().length === 0 ? true : null) === true
  }
  const clickOption = async (group: GroupState, label: string): Promise<boolean> => {
    const matches = group.options.filter((option) => option.label === label)
    if (matches.length !== 1) return false
    await interact(() => matches[0].node.click())
    return true
  }
  const desiredLabels = (filters: CandidateSourcingFilters): Record<FilterKey, string[]> => {
    const careerLabels: Record<string, string> = {
      employedLooking: '在职-正在找工作',
      leftLooking: '离职-正在找工作',
      employedOpen: '在职-看看机会',
      employedNotLooking: '在职-暂不找工作',
    }
    const educationLabels: Record<string, string> = {
      juniorHighOrBelow: '初中及以下',
      highSchool: '高中',
      secondaryVocational: '中专/中技',
      associate: '大专',
      bachelor: '本科',
      master: '硕士',
      mbaEmba: 'MBA/EMBA',
      doctorate: '博士',
    }
    const activeLabels: Record<string, string> = {
      any: '不限',
      today: '今日活跃',
      days3: '3天内活跃',
      days7: '7天内活跃',
      days30: '30天内活跃',
    }
    const genderLabels: Record<string, string> = { any: '不限', male: '男', female: '女' }
    const rangePresets: Array<{ minAge: number; maxAge?: number; label: string }> = [
      { minAge: 20, maxAge: 25, label: '20-25' },
      { minAge: 25, maxAge: 30, label: '25-30' },
      { minAge: 30, maxAge: 35, label: '30-35' },
      { minAge: 35, maxAge: 40, label: '35-40' },
      { minAge: 40, label: '40以上' },
    ]
    const preset = filters.age.mode === 'range'
      ? rangePresets.find((candidate) =>
          candidate.minAge === filters.age.minAge && candidate.maxAge === filters.age.maxAge)
      : undefined
    return {
      age: [filters.age.mode === 'any' ? '不限' : preset?.label ?? '自定义'],
      activeTime: [activeLabels[filters.activeWindow]],
      careerStatuses: filters.careerStatuses.length === 0
        ? ['不限']
        : filters.careerStatuses.map((value) => careerLabels[value]),
      educations: filters.educations.length === 0
        ? ['不限']
        : filters.educations.map((value) => educationLabels[value]),
      gender: [genderLabels[filters.gender]],
      filterTypes: [
        ...(filters.excludeViewed ? ['过滤我已看过'] : []),
        ...(filters.excludeCoworkerContacted ? ['过滤同事已聊'] : []),
      ].length === 0
        ? ['不限']
        : [
            ...(filters.excludeViewed ? ['过滤我已看过'] : []),
            ...(filters.excludeCoworkerContacted ? ['过滤同事已聊'] : []),
          ],
    }
  }
  const selectRangeValue = async (
    select: HTMLElement,
    value: number | undefined,
  ): Promise<boolean> => {
    const targetToken = value === undefined ? 'open' : String(value)
    if (rangeToken(selectText(select)) === targetToken) return true
    await interact(() => select.click())
    const option = await waitFor(() => {
      const popovers = visibleAll(document, '.km-popover.filter-select-two__popover')
      if (popovers.length !== 1) return null
      const matches = visibleAll(popovers[0], '.km-option')
        .filter((candidate) => rangeToken(clean(candidate.textContent)) === targetToken)
      return matches.length === 1 ? matches[0] : null
    })
    if (!option) return false
    await interact(() => option.click())
    const closed = await waitFor(() =>
      visibleAll(document, '.km-popover.filter-select-two__popover').length === 0 ? true : null)
    return closed === true
  }

  const targetFilters = normalizeFilters(requestedFilters)
  let ownedDrawer: HTMLElement | null = null
  try {
    if (drawerNodes().length !== 0) return failed('drawer_cardinality')
    const initialPosition = position()
    if (initialPosition.status === 'failed') return initialPosition
    if (!trigger()) return failed('trigger_cardinality')

    ownedDrawer = await openDrawer()
    if (!ownedDrawer) return failed('drawer_not_ready')
    if (drawerNodes().length !== 1) return failed('drawer_cardinality')
    const initialSnapshot = readFilters(ownedDrawer)
    if ('status' in initialSnapshot) return initialSnapshot
    const targets = desiredLabels(targetFilters)
    if (Object.values(targets).some((labels) => labels.some((label) => !label))) {
      return failed('filter_mismatch')
    }

    for (const key of filterKeys) {
      const group = initialSnapshot.groups[key]
      const targetLabels = targets[key]
      if (key === 'age' || key === 'activeTime' || key === 'gender') {
        if (group.selectedLabels[0] !== targetLabels[0] &&
            !await clickOption(group, targetLabels[0])) {
          return failed('option_set_mismatch')
        }
        continue
      }
      if (targetLabels.length === 1 && targetLabels[0] === '不限') {
        if (!(group.selectedLabels.length === 1 && group.selectedLabels[0] === '不限') &&
            !await clickOption(group, '不限')) {
          return failed('option_set_mismatch')
        }
        continue
      }
      for (const selected of group.selectedLabels) {
        if (selected !== '不限' && !targetLabels.includes(selected) &&
            !await clickOption(group, selected)) {
          return failed('option_set_mismatch')
        }
      }
      for (const desired of targetLabels) {
        if (!group.selectedLabels.includes(desired) &&
            !await clickOption(group, desired)) {
          return failed('option_set_mismatch')
        }
      }
    }

    if (targetFilters.age.mode === 'range' && targets.age[0] === '自定义') {
      const selector = await waitFor(() => {
        const matches = visibleAll(initialSnapshot.groups.age.node, '.recommend-checkbox-group__selector')
        return matches.length === 1 ? matches[0] : null
      })
      if (!selector) return failed('custom_selector_unavailable')
      const starts = visibleAll(selector, '.filter-select-two__start .km-select')
      const ends = visibleAll(selector, '.filter-select-two__end .km-select')
      if (starts.length !== 1 || ends.length !== 1) return failed('range_select_unavailable')
      if (!await selectRangeValue(starts[0], targetFilters.age.minAge) ||
          !await selectRangeValue(ends[0], targetFilters.age.maxAge)) {
        return failed('range_option_unavailable')
      }
    }

    const confirmedSnapshot = await waitFor(() => {
      const current = readFilters(ownedDrawer as HTMLElement)
      return !('status' in current) && sameFilters(current.filters, targetFilters) ? current : null
    })
    if (!confirmedSnapshot) return failed('filter_mismatch')
    const currentPosition = position()
    if (currentPosition.status === 'failed') return currentPosition

    const confirmButtons = visibleAll(
      ownedDrawer,
      'button[zp-stat-id="rsmlist-confirm"]',
    )
    if (confirmButtons.length !== 1 || confirmButtons[0].hasAttribute('disabled') ||
        confirmButtons[0].getAttribute('aria-disabled') === 'true') {
      return failed('confirm_unavailable')
    }
    const beforeSubmitList = listSnapshot()
    if (beforeSubmitList.status === 'failed') return beforeSubmitList
    await interact(() => confirmButtons[0].click())
    const submittedDrawer = ownedDrawer
    const closed = await waitFor(() => drawerNodes().length === 0 ? true : null)
    if (closed !== true) return failed('confirm_not_closed')
    if (submittedDrawer.isConnected && drawerNodes().length !== 0) {
      return failed('confirm_not_closed')
    }
    ownedDrawer = null

    // “确定”可能因目标条件本来就已生效而保留同一推荐列表。这里确认的是
    // 列表重新进入连续稳定状态；筛选是否真正生效由随后字面同一份
    // readFilters 二次回读裁决，不能把“列表必须变化”当作平台契约。
    const stable = await stableList()
    if (stable.status === 'failed') return stable
    const reopened = await openDrawer()
    if (!reopened) return failed('drawer_not_ready')
    ownedDrawer = reopened
    if (drawerNodes().length !== 1) return failed('drawer_cardinality')
    const finalSnapshot = readFilters(reopened)
    if ('status' in finalSnapshot) return finalSnapshot
    if (!sameFilters(finalSnapshot.filters, targetFilters)) return failed('filter_mismatch')
    const finalPosition = position()
    if (finalPosition.status === 'failed') return finalPosition

    const cancel = cancelButton(reopened)
    if (!cancel) return failed('cancel_unavailable')
    await interact(() => cancel.click())
    const cancelClosed = await waitFor(() => drawerNodes().length === 0 ? true : null)
    if (cancelClosed !== true) return failed('cancel_not_closed')
    ownedDrawer = null
    const afterPosition = position()
    if (afterPosition.status === 'failed') return afterPosition

    return {
      status: 'ready',
      data: {
        positionRef: afterPosition.positionRef,
        positionTitle: afterPosition.positionTitle,
        filters: finalSnapshot.filters,
        observedAt: Date.now(),
      },
    }
  } catch {
    return failed('unexpected')
  } finally {
    if (ownedDrawer) {
      try {
        await closeOwnedDrawer(ownedDrawer)
      } catch {
        // 返回原失败；页面收口失败由 outer 统一暴露为命令失败。
      }
    }
  }
}

// 正式采集批次的推荐窗口 evaluator。只读取当前虚拟列表窗口中的稳定身份；
// reset/next 最多各执行一次滚动动作，moved 只陈述本次是否观察到窗口或滚动位置推进。
// 它没有“耗尽”语义，也不读取姓名与 resumeNumber。
async function mainReadSourcingWindow(
  move: ZhilianSourcingWindowArgs['move'],
): Promise<MainSourcingWindowResult> {
  type AnyRecord = Record<string, unknown>
  interface WindowSource {
    item: HTMLElement
    owner: AnyRecord
    platformUserRef: string
  }
  type WindowSnapshot = {
    sources: WindowSource[]
    positionRef: string
    positionTitle: string | null
  }

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
  const failed = (reason: MainSourcingWindowFailureReason): MainSourcingWindowFailed => ({
    status: 'failed',
    reason,
  })
  const route = (): URL | null => {
    try {
      const current = new URL(location.href)
      return current.pathname.startsWith('/app/recommend') ? current : null
    } catch {
      return null
    }
  }
  const collect = (scroller: HTMLElement | null): WindowSnapshot | MainSourcingWindowFailed => {
    const currentRoute = route()
    if (!currentRoute) return failed('route_changed')
    const items = viewportItems(
      visibleAll(document, '.recommend-list__left div[role="listitem"]'),
      scroller,
    )
    if (items.length === 0) return failed('list_source_unavailable')
    const sources: WindowSource[] = []
    for (const item of items) {
      const owner = asRecord((item as HTMLElement & { __vue__?: unknown }).__vue__)
      const props = asRecord(owner?._props)
      const source = asRecord(props?.source)
      if (!owner || !source) return failed('list_source_unavailable')
      const platformUserRef = opaque(source.userMasterId)
      if (!platformUserRef) return failed('candidate_identity_unavailable')
      sources.push({ item, owner, platformUserRef })
    }
    const identities = new Set<string>()
    for (const source of sources) {
      if (identities.has(source.platformUserRef)) return failed('candidate_identity_duplicated')
      identities.add(source.platformUserRef)
    }

    const routeJobNumber = opaque(currentRoute.searchParams.get('jobNumber'))
    if (!routeJobNumber) return failed('position_identity_unavailable')
    let activeJobTitle = ''
    for (const source of sources) {
      const root = asRecord(source.owner.$root)
      const ownerRoute = asRecord(root?._route)
      const ownerQuery = asRecord(ownerRoute?.query)
      const store = asRecord(source.owner.$store)
      const state = asRecord(store?.state)
      const talent = asRecord(state?.talent)
      const activeJob = asRecord(talent?.activeJob)
      const routedJobNumber = opaque(ownerQuery?.jobNumber)
      const activeJobNumber = opaque(activeJob?.jobNumber)
      if (!routedJobNumber || !activeJobNumber) return failed('position_identity_unavailable')
      if (routeJobNumber !== routedJobNumber || routeJobNumber !== activeJobNumber) {
        return failed('position_identity_mismatch')
      }
      const sourceTitle = clean(activeJob?.jobTitle)
      if (activeJobTitle && sourceTitle && activeJobTitle !== sourceTitle) {
        return failed('position_title_mismatch')
      }
      activeJobTitle ||= sourceTitle
    }
    const visibleJobTitles = visibleAll(document,
      '.job-pane__item--active .job-pane__item-job-title')
      .map((element) => clean(element.textContent)).filter(Boolean)
    if (visibleJobTitles.length > 1) return failed('position_title_ambiguous')
    const visibleJobTitle = visibleJobTitles[0] ?? ''
    if (visibleJobTitle && activeJobTitle && visibleJobTitle !== activeJobTitle) {
      return failed('position_title_mismatch')
    }
    const title = visibleJobTitle || activeJobTitle
    return {
      sources,
      positionRef: routeJobNumber,
      positionTitle: title && title.length <= 256 ? title : null,
    }
  }
  const signature = (snapshot: WindowSnapshot): string =>
    `${snapshot.positionRef}|${snapshot.sources.map((source) => source.platformUserRef).join('|')}`
  const scrollable = (element: HTMLElement): boolean => {
    const style = getComputedStyle(element)
    return /(auto|scroll)/u.test(`${style.overflowY ?? ''} ${style.overflow ?? ''}`) &&
      Number(element.scrollHeight) > Number(element.clientHeight) + 1
  }
  const scrollContainer = (item: HTMLElement): HTMLElement | null => {
    let current = item.parentElement
    while (current && current !== document.body) {
      if (scrollable(current)) return current
      current = current.parentElement
    }
    const documentScroller = document.scrollingElement as HTMLElement | null
    // Chromium 的 document root 即便可滚动，computed overflow 也常是 visible；
    // root 以可滚动尺寸为公开判据，普通祖先仍要求 auto/scroll。
    return documentScroller &&
      Number(documentScroller.scrollHeight) > Number(documentScroller.clientHeight) + 1
      ? documentScroller
      : null
  }
  const scrollTop = (element: HTMLElement): number => Number(element.scrollTop) || 0
  const scrollTo = (element: HTMLElement, top: number): void => {
    if (typeof element.scrollTo === 'function') {
      element.scrollTo({ top, behavior: 'auto' })
    } else {
      element.scrollTop = top
    }
    element.dispatchEvent?.(new Event('scroll', { bubbles: true }))
  }
  const viewportItems = (
    items: HTMLElement[],
    scroller: HTMLElement | null,
  ): HTMLElement[] => {
    if (!scroller) return items.slice(0, 32)
    const rootScroller = scroller === document.scrollingElement
    const containerRect = rootScroller
      ? { top: 0, bottom: Math.max(Number(globalThis.innerHeight) || Number(scroller.clientHeight) || 0, 1) }
      : scroller.getBoundingClientRect()
    return items.filter((item) => {
      const rect = item.getBoundingClientRect()
      return rect.bottom > containerRect.top + 1 && rect.top < containerRect.bottom - 1
    }).slice(0, 32)
  }

  try {
    if (!['current', 'reset', 'next'].includes(move)) return failed('unexpected')
    const initialItems = visibleAll(document, '.recommend-list__left div[role="listitem"]')
    if (initialItems.length === 0) return failed('list_source_unavailable')
    const scroller = scrollContainer(initialItems[0])
    const initial = collect(scroller)
    if ('status' in initial) return initial
    const initialSignature = signature(initial)
    const beforeTop = scroller ? scrollTop(scroller) : 0
    if (move !== 'current') {
      if (!scroller) return failed('scroll_unavailable')
      if (move === 'reset') {
        scrollTo(scroller, 0)
      } else {
        const viewport = Math.max(Number(scroller.clientHeight) || 0, 1)
        const maxTop = Math.max((Number(scroller.scrollHeight) || 0) - viewport, 0)
        scrollTo(scroller, Math.min(beforeTop + viewport, maxTop))
      }
    }

    let latest = collect(scroller)
    let latestSignature = 'status' in latest ? '' : signature(latest)
    let stableRounds = 0
    let settled = false
    const settleUntil = Date.now() + 10_000
    while (Date.now() < settleUntil) {
      if (!('status' in latest)) {
        const movementObserved = latestSignature !== initialSignature ||
          (scroller ? scrollTop(scroller) !== beforeTop : false)
        if (stableRounds >= 2 && (move === 'current' || move === 'reset' || movementObserved)) {
          settled = true
          break
        }
      } else if (latest.reason === 'route_changed') {
        return latest
      }
      await new Promise((resolve) => setTimeout(resolve, 120))
      const next = collect(scroller)
      if ('status' in next) {
        latest = next
        latestSignature = ''
        stableRounds = 0
        continue
      }
      const nextSignature = signature(next)
      stableRounds = nextSignature === latestSignature ? stableRounds + 1 : 0
      latest = next
      latestSignature = nextSignature
    }
    if ('status' in latest) return latest
    // next 到达真实尾部时不会再产生 movement，但只有等满稳定窗口后才允许把
    // 连续相同读解释为合法的 moved=false；不能在两三个采样点后抢跑判尾。
    if (!settled && move === 'next' && stableRounds >= 2) settled = true
    if (!settled) return failed('page_unstable')
    if (latest.positionRef !== initial.positionRef) return failed('position_identity_mismatch')
    if (latest.positionTitle !== initial.positionTitle) return failed('position_title_mismatch')
    return {
      status: 'ready',
      data: {
        positionRef: latest.positionRef,
        positionTitle: latest.positionTitle,
        platformUserRefs: latest.sources.map((source) => source.platformUserRef),
        moved: move === 'current'
          ? false
          : latestSignature !== initialSignature ||
            (scroller ? scrollTop(scroller) !== beforeTop : false),
        observedAt: Date.now(),
      },
    }
  } catch {
    return failed('unexpected')
  }
}

// 冒烟冲刺的推荐页采集 evaluator。它在同一个 MAIN task 内完成“稳定来源卡
// -> 打开详情 -> resumeNumber 瞬时连接 -> 五分区 -> 再次绑定复核”，返回值中
// 永不包含 resumeNumber；列表顺序只决定本轮先读谁，不承担任何身份语义。
async function mainReadSourcingResume(
  excludePlatformUserRefs: string[],
  requestedTarget?: { platformUserRef: string; positionRef: string },
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
  const randomDelayMs = (minimumMs: number, maximumMs: number): number =>
    minimumMs + Math.floor(Math.random() * (maximumMs - minimumMs + 1))
  const sleep = (delayMs: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, delayMs))
  let nextInteractionNotBefore = 0
  let targetCloseNotBefore = 0
  const waitBeforeInteraction = async (notBeforeMs = 0): Promise<void> => {
    const waitUntil = Math.max(notBeforeMs, nextInteractionNotBefore)
    const delayMs = waitUntil - Date.now()
    if (delayMs > 0) await sleep(delayMs)
  }
  const clickInteraction = async (element: HTMLElement, notBeforeMs = 0): Promise<void> => {
    await waitBeforeInteraction(notBeforeMs)
    element.click()
    nextInteractionNotBefore = Date.now() + randomDelayMs(1_000, 1_500)
  }
  const closeOpenedDetail = async (): Promise<MainSourcingResumeFailed | null> => {
    let opened = visibleAll(document, '.new-shortcut-resume__modal')
    if (opened.length !== 1) return failed(opened.length === 0 ? 'close_unavailable' : 'modal_cardinality')
    await waitBeforeInteraction(targetCloseNotBefore)
    opened = visibleAll(document, '.new-shortcut-resume__modal')
    if (opened.length !== 1) return failed(opened.length === 0 ? 'close_unavailable' : 'modal_cardinality')
    const closeButtons = visibleAll(opened[0], '.new-shortcut-resume__close')
    if (closeButtons.length !== 1) return failed('close_unavailable')
    await clickInteraction(closeButtons[0])
    const closeUntil = Date.now() + 10_000
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
    let target: SourceSnapshot | undefined
    if (requestedTarget) {
      const requestedPlatformUserRef = opaque(requestedTarget.platformUserRef)
      const requestedPositionRef = opaque(requestedTarget.positionRef)
      if (!requestedPlatformUserRef) return failed('candidate_identity_unavailable')
      if (!requestedPositionRef) return failed('position_identity_unavailable')
      const matches = sources.filter((source) => source.platformUserRef === requestedPlatformUserRef)
      if (matches.length > 1) return failed('candidate_identity_duplicated')
      target = matches[0]
    } else {
      target = sources.find((source) => !excluded.has(source.platformUserRef))
    }
    if (!target) return failed('no_candidate')
    const initialPosition = readPosition(target)
    if (initialPosition.status === 'failed') return initialPosition
    if (requestedTarget && initialPosition.positionRef !== opaque(requestedTarget.positionRef)) {
      return failed('position_identity_mismatch')
    }
    const initialContactState = contactState(target.item)

    let modals = visibleAll(document, '.new-shortcut-resume__modal')
    if (modals.length > 1) return failed('modal_cardinality')
    const currentRoute = route()
    if (!currentRoute) return failed('route_changed')
    const openedResumeNumber = opaque(currentRoute.searchParams.get('resumeNumber'))
    let targetAlreadyOpen = false
    const initiallyOpenedAt = modals.length === 1 ? Date.now() : 0
    const initiallyCloseNotBefore = initiallyOpenedAt > 0
      ? initiallyOpenedAt + randomDelayMs(2_000, 2_500)
      : 0
    if (modals.length === 1) {
      const openedMatches = sources.filter((source) => source.resumeNumber === openedResumeNumber)
      if (!openedResumeNumber || openedMatches.length !== 1) return failed('stale_detail_ambiguous')
      targetAlreadyOpen = openedMatches[0].platformUserRef === target.platformUserRef
      if (!targetAlreadyOpen) {
        const closeButtons = visibleAll(modals[0], '.new-shortcut-resume__close')
        if (closeButtons.length !== 1) return failed('close_unavailable')
        await clickInteraction(closeButtons[0], initiallyCloseNotBefore)
        const closeUntil = Date.now() + 10_000
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
      } else {
        targetCloseNotBefore = initiallyCloseNotBefore
      }
    } else if (openedResumeNumber) {
      return failed('stale_detail_ambiguous')
    }

    if (!targetAlreadyOpen) {
      const entries = visibleAll(target.item, '.resume-item__content')
      if (entries.length !== 1) return failed('entry_cardinality')
      await clickInteraction(entries[0])
      const openUntil = Date.now() + 10_000
      while (Date.now() < openUntil) {
        modals = visibleAll(document, '.new-shortcut-resume__modal')
        if (modals.length !== 0) {
          targetCloseNotBefore = Date.now() + randomDelayMs(2_000, 2_500)
          break
        }
        await new Promise((resolve) => setTimeout(resolve, 120))
      }
    }
    if (modals.length !== 1) return failed('modal_cardinality')
    let modal = modals[0]
    const expectedTarget = target

    const evaluateOpenedDetail = (): MainSourcingResumeResult => {
      const currentModals = visibleAll(document, '.new-shortcut-resume__modal')
      if (currentModals.length !== 1) return failed('target_changed')
      modal = currentModals[0]
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
    const readinessFailures = new Set<MainSourcingResumeFailureReason>([
      'detail_binding_ambiguous',
      'basic_unresolved',
      'expectations_unresolved',
      'work_unresolved',
      'education_unresolved',
      'self_evaluation_unresolved',
    ])
    const stableProjection = (data: ZhilianSourcingResumeData): string => JSON.stringify({
      ...data,
      observedAt: 0,
    })
    const settleUntil = Date.now() + 10_000
    let evaluated: MainSourcingResumeResult = failed('unexpected')
    let readyProjection = ''
    while (true) {
      try {
        evaluated = evaluateOpenedDetail()
      } catch {
        evaluated = failed('unexpected')
      }
      if (evaluated.status === 'ready') {
        const nextProjection = stableProjection(evaluated.data)
        if (nextProjection === readyProjection) break
        if (Date.now() >= settleUntil) {
          evaluated = failed('target_changed')
          break
        }
        readyProjection = nextProjection
      } else {
        readyProjection = ''
        if (!readinessFailures.has(evaluated.reason) || Date.now() >= settleUntil) break
      }
      await new Promise((resolve) => setTimeout(resolve, 120))
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

// M6 列表招呼的只读目标投影。职位只认公开 URL，候选人只认当前可见卡片的
// 稳定 userMasterId；详情、resumeNumber、姓名和列表位置都不参与匹配。
function mainReadGreetingListTarget(
  platformUserRef: string,
  positionRef: string,
): MainGreetingListTargetResult {
  type AnyRecord = Record<string, unknown>
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
  const failed = (reason: MainGreetingListTargetFailureReason): MainGreetingListTargetResult => ({
    status: 'failed',
    reason,
  })

  try {
    const route = new URL(location.href)
    if (!route.pathname.startsWith('/app/recommend') ||
        opaque(route.searchParams.get('jobNumber')) !== positionRef) {
      return failed('route_mismatch')
    }
    if (Array.from(document.querySelectorAll<HTMLElement>('.new-shortcut-resume__modal'))
      .filter(visible).length !== 0) {
      return failed('detail_present')
    }
    const items = Array.from(document.querySelectorAll<HTMLElement>(
      '.recommend-list__left div[role="listitem"]',
    )).filter(visible)
    if (items.length === 0) return failed('list_source_unavailable')
    const sources: Array<{ item: HTMLElement; platformUserRef: string }> = []
    for (const item of items) {
      const owner = asRecord((item as HTMLElement & { __vue__?: unknown }).__vue__)
      const source = asRecord(asRecord(owner?._props)?.source)
      if (!owner || !source) return failed('list_source_unavailable')
      const observedRef = opaque(source.userMasterId)
      if (!observedRef) return failed('candidate_identity_unavailable')
      sources.push({ item, platformUserRef: observedRef })
    }
    const matches = sources.filter((source) => source.platformUserRef === platformUserRef)
    if (matches.length === 0) return failed('target_absent')
    if (matches.length !== 1) return failed('candidate_identity_duplicated')

    const buttons = Array.from(matches[0].item.querySelectorAll<HTMLButtonElement>(
      'button[type="button"]',
    )).filter(visible)
    const greetingButtons = buttons.filter((button) => clean(button.textContent) === '打招呼')
    const continueButtons = buttons.filter((button) => clean(button.textContent) === '继续沟通')
    const contactState: ZhilianCurrentCandidate['contactState'] =
      greetingButtons.length === 1 && continueButtons.length === 0
        ? 'unestablished'
        : greetingButtons.length === 0 && continueButtons.length === 1
          ? 'established'
          : 'unknown'
    return { status: 'ready', data: { contactState } }
  } catch {
    return failed('unexpected')
  }
}

function validGreetingListTargetResult(value: unknown): value is MainGreetingListTargetResult {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  if (record.status === 'failed') {
    return typeof record.reason === 'string' &&
      (MAIN_GREETING_LIST_TARGET_FAILURE_REASONS as readonly string[]).includes(record.reason)
  }
  if (record.status !== 'ready' || !record.data ||
      typeof record.data !== 'object' || Array.isArray(record.data)) return false
  const contactState = (record.data as Record<string, unknown>).contactState
  return contactState === 'unestablished' || contactState === 'established' || contactState === 'unknown'
}

interface GreetingListTargetSnapshot {
  tab: chrome.tabs.Tab
  result: Extract<MainGreetingListTargetResult, { status: 'ready' }>
}

async function uniqueGreetingListTarget(
  platformUserRef: string,
  positionRef: string,
  expectedPrincipalFingerprint: string | undefined,
): Promise<GreetingListTargetSnapshot> {
  const tabs = await recommendTabs()
  if (tabs.length === 0) {
    throw new ZhilianPlatformError(
      'CTX_NOT_READY',
      '请在 Chrome 中打开智联推荐列表',
      'manualOnly',
      'pageAbsent',
    )
  }
  const matchingTabs = tabs.filter((tab) => {
    try {
      const route = new URL(tab.url ?? '')
      return route.searchParams.get('jobNumber') === positionRef
    } catch {
      return false
    }
  })
  const ready: GreetingListTargetSnapshot[] = []
  for (const tab of matchingTabs) {
    if (tab.id === undefined || tab.status !== 'complete') {
      throw new ZhilianPlatformError(
        'CTX_NOT_READY',
        '当前智联推荐列表尚未就绪',
        'afterRecovery',
        'pageBroken',
      )
    }
    assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
    const result = await runMain(tab.id, mainReadGreetingListTarget, [platformUserRef, positionRef])
    if (!validGreetingListTargetResult(result)) {
      throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '推荐列表目标读取结果无效', 'manualOnly')
    }
    if (result.status === 'ready') ready.push({ tab, result })
    else if (result.reason !== 'target_absent') {
      throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '推荐列表目标无法唯一确证', 'manualOnly')
    }
  }
  if (ready.length !== 1) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '推荐列表目标无法唯一确证', 'manualOnly')
  }
  return ready[0]
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
    card: HTMLElement
    openButton: HTMLButtonElement
  }
  const targetSurface = (): TargetSurface | null => {
    let route: URL
    try { route = new URL(location.href) } catch { return null }
    if (!route.pathname.startsWith('/app/recommend')) return null
    const routeJobNumber = opaque(route.searchParams.get('jobNumber'))
    if (routeJobNumber !== positionRef) return null
    const details = Array.from(document.querySelectorAll<HTMLElement>('.new-shortcut-resume__modal')).filter(visible)
    if (details.length !== 0) return null
    const listItems = Array.from(document.querySelectorAll<HTMLElement>(
      '.recommend-list__left div[role="listitem"]',
    )).filter(visible)
    if (listItems.length === 0) return null
    const sources: Array<{ item: HTMLElement; source: AnyRecord }> = []
    for (const item of listItems) {
      const owner = asRecord((item as HTMLElement & { __vue__?: unknown }).__vue__)
      const source = asRecord(asRecord(owner?._props)?.source)
      if (!owner || !source) return null
      if (!opaque(source.userMasterId)) return null
      sources.push({ item, source })
    }
    const matches = sources.filter(({ source }) => opaque(source.userMasterId) === platformUserRef)
    if (matches.length !== 1) return null
    const buttons = Array.from(matches[0].item.querySelectorAll<HTMLButtonElement>('button[type="button"]'))
      .filter(visible)
    const greetingButtons = buttons.filter((button) => clean(button.textContent) === '打招呼')
    const continueButtons = buttons.filter((button) => clean(button.textContent) === '继续沟通')
    if (greetingButtons.length !== 1 || continueButtons.length !== 0) return null
    const openButton = greetingButtons[0]
    if (openButton.form !== null && openButton.type !== 'button') return null
    return { card: matches[0].item, openButton }
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
  const randomInteractionGapMs = (): number => 1_000 + Math.floor(Math.random() * 501)
  let nextInteractionNotBefore = 0
  const waitInteractionGap = async (): Promise<void> => {
    const delayMs = nextInteractionNotBefore - Date.now()
    if (delayMs > 0) await sleep(delayMs)
  }
  const performInteraction = async (
    interaction: () => void,
    allowed: () => boolean = () => true,
  ): Promise<boolean> => {
    await waitInteractionGap()
    if (!allowed()) return false
    try {
      interaction()
    } finally {
      nextInteractionNotBefore = Date.now() + randomInteractionGapMs()
    }
    return true
  }
  const waitFor = async (predicate: () => boolean): Promise<boolean> => {
    for (let round = 0; round < 200; round += 1) {
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
    await performInteraction(invokeOpen)
    let humanTouched = false
    const observeTrustedInput = (event: Event): void => {
      if (event.isTrusted) humanTouched = true
    }
    for (const eventName of ['pointerdown', 'keydown', 'input'] as const) {
      document.addEventListener(eventName, observeTrustedInput, true)
    }
    try {
      if (!await waitFor(() => greetingModals().length === 1)) return failed('editor_not_opened')
      if (humanTouched) return failed('existing_editor')
      if (!principalMatches() || !targetSurface()) return failed('target_changed')
      let modal = greetingModals()[0]
      let custom = customOptionOf(modal)
      if (!custom) return failed('custom_option_unavailable')
      if (!customSelected(custom)) {
        const invokeOption = Function.prototype.call.bind(
          HTMLElement.prototype.click as IntrinsicClick,
          custom,
        )
        if (!await performInteraction(invokeOption, () => !humanTouched)) return failed('existing_editor')
        const selected = await waitFor(() => {
          if (humanTouched) return true
          const currentModal = greetingModals()[0]
          const currentCustom = currentModal ? customOptionOf(currentModal) : null
          return currentCustom !== null && customSelected(currentCustom)
        })
        if (humanTouched) return failed('existing_editor')
        if (!selected) return failed('custom_option_unavailable')
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
        if (!await performInteraction(invokeEdit, () => !humanTouched)) return failed('existing_editor')
        await waitFor(() => {
          const currentModal = greetingModals()[0]
          const currentCustom = currentModal ? customOptionOf(currentModal) : null
          return currentCustom !== null && Array.from(currentCustom.querySelectorAll<HTMLTextAreaElement>(
            '.ai-greeting-modal__edit-area textarea',
          )).filter(visible).length === 1
        })
        if (humanTouched) return failed('existing_editor')
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
      if (humanTouched) return failed('existing_editor')
      const ownedDraft = textareas[0].value
      if (new TextEncoder().encode(ownedDraft).length > 2048) return failed('editor_unavailable')
      const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set as
        TextareaValueSetter | undefined
      if (typeof setter !== 'function') return failed('editor_unavailable')
      const restoreOwnedDraft = async (): Promise<boolean> =>
        performInteraction(() => {
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
        }, () => !humanTouched)
      try {
        if (!await performInteraction(() => {
          setter.call(textareas[0], text)
          textareas[0].dispatchEvent(new InputEvent('input', {
            bubbles: true,
            inputType: 'insertText',
            data: text,
          }))
          textareas[0].dispatchEvent(new Event('change', { bubbles: true }))
        }, () => !humanTouched)) return failed('existing_editor')
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
          if (!await restoreOwnedDraft()) return failed('existing_editor')
          return failed('input_rejected')
        }
        return { status: 'prepared' }
      } catch {
        if (!await restoreOwnedDraft()) return failed('existing_editor')
        return failed('input_rejected')
      }
    } finally {
      for (const eventName of ['pointerdown', 'keydown', 'input'] as const) {
        document.removeEventListener(eventName, observeTrustedInput, true)
      }
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
  const current = await uniqueGreetingListTarget(
    args.platformUserRef,
    args.positionRef,
    expectedPrincipalFingerprint,
  )
  const tabId = current.tab.id
  if (tabId === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '当前智联推荐页缺少 id', 'afterRecovery', 'pageBroken')
  }
  if (current.result.data.contactState !== 'unestablished') {
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

  await new Promise<void>((resolve) => setTimeout(resolve, 1_000 + Math.floor(Math.random() * 501)))
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
        const observed = await runMain(tabId, mainReadGreetingListTarget, [
          args.platformUserRef,
          args.positionRef,
        ])
        if (validGreetingListTargetResult(observed) && observed.status === 'ready' &&
            observed.data.contactState === 'established') {
          await ctx.progress('已确认同一候选人关系状态变为已建立', 100)
          // 正证已经成立；这里只给列表卡片的关系态重渲染一个短收敛窗口，
          // 避免批次编排在下一候选人 reset 时撞上临时缺少 Vue owner 的 DOM。
          // 不追加成功判据，也不在此期间重读或重试任何平台动作。
          await new Promise((resolve) => setTimeout(resolve, 250))
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
    const current = await uniqueGreetingListTarget(
      args.platformUserRef,
      args.positionRef,
      expectedPrincipalFingerprint,
    )
    const data = current.result.data
    if (data.contactState === 'established') {
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
  const sourcingTabs = await activeSourcingTabs()
  if (sourcingTabs.length > 1) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '智联推荐页标签无法唯一确定', 'manualOnly')
  }
  // 推荐→IM 是同一产品工作页的阶段交接。即使浏览器里还留有健康 IM 页，
  // 也应优先把本轮唯一推荐页导航到 IM；只有没有推荐页时才走 canonical
  // 兜底复用其他智联页面。
  let tab = sourcingTabs[0] ?? await canonicalZhilianTab()
  let createdTab = false
  if (!tab) {
    tab = await chrome.tabs.create({ url: ZHILIAN_IM_URL, active: false })
    createdTab = true
  } else if (pageKindFromURL(tab.url) !== 'im') {
    if (tab.id === undefined) throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
    // 产品工作流只会在推荐批次已经终局后恢复 IM。复用同一 canonical
    // 智联工作页，避免为两个互斥业务阶段长期保留第二张后台标签。
    const commandNavigation = beginCommandNavigation(tab.id, ctx.irreversibleNotAfterMs)
    try {
      tab = await chrome.tabs.update(tab.id, { url: ZHILIAN_IM_URL, active: true })
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

// 只使用当前真机已经确认的公开控件：职位触发器的可见标题与标准 checkbox.checked。
// apply=false 是零交互预检；只有明确返回 needs_action 后，extension 才穿过取消闸并
// 以 apply=true 执行最小可逆交互。每次 click 后都先满足 1s+抖动，再做最长 10s 条件等待。
async function mainEnsureChatListFilter(
  unread: boolean,
  apply: boolean,
): Promise<MainChatListFilterResult> {
  const failed = (reason: Extract<MainChatListFilterResult, { status: 'failed' }>['reason']):
  MainChatListFilterResult => ({ status: 'failed', reason })
  try {
    const clean = (value: unknown): string => String(value ?? '')
      .normalize('NFC')
      .replace(/\u00a0/gu, ' ')
      .replace(/\s+/gu, ' ')
      .trim()
    const visible = (element: Element): boolean => {
      const node = element as HTMLElement
      const style = getComputedStyle(node)
      return style.display !== 'none' && style.visibility !== 'hidden' &&
        node.getClientRects().length > 0
    }
    const routeReady = (): boolean => {
      try { return new URL(location.href).pathname === '/app/im' } catch { return false }
    }
    const triggers = (): HTMLElement[] =>
      Array.from(document.querySelectorAll<HTMLElement>('.app-job-selector'))
        .filter((node) => visible(node))
    const selectedTitle = (): string | null => {
      const candidates = Array.from(
        document.querySelectorAll<HTMLElement>('.app-job-selector .im-job-filter__label'),
      ).filter((node) => visible(node))
      return candidates.length === 1 ? clean(candidates[0].textContent) : null
    }
    const unreadControls = (): Array<{ wrapper: HTMLElement; input: HTMLInputElement }> =>
      Array.from(document.querySelectorAll<HTMLElement>('.side-panel-header__checkbox'))
        .filter((wrapper) => visible(wrapper) && clean(wrapper.textContent).includes('未读'))
        .flatMap((wrapper) => {
          const inputs = Array.from(wrapper.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))
          return inputs.length === 1 ? [{ wrapper, input: inputs[0] }] : []
        })
    const readState = ():
    | { status: 'ready'; trigger: HTMLElement; input: HTMLInputElement; allJobs: boolean; unread: boolean }
    | { status: 'failed'; reason: Extract<MainChatListFilterResult, { status: 'failed' }>['reason'] } => {
      if (!routeReady()) return { status: 'failed', reason: 'route_changed' }
      const triggerMatches = triggers()
      if (triggerMatches.length !== 1) return { status: 'failed', reason: 'job_trigger_cardinality' }
      const title = selectedTitle()
      if (!title) return { status: 'failed', reason: 'job_label_unreadable' }
      const controls = unreadControls()
      if (controls.length !== 1) return { status: 'failed', reason: 'unread_control_cardinality' }
      const input = controls[0].input
      if (input.type !== 'checkbox' || input.disabled) {
        return { status: 'failed', reason: 'unread_control_unsafe' }
      }
      return {
        status: 'ready',
        trigger: triggerMatches[0],
        input,
        allJobs: title === '全部职位',
        unread: input.checked,
      }
    }
    const waitInteraction = async (): Promise<void> => {
      await new Promise((resolve) => setTimeout(resolve, 1_000 + Math.floor(Math.random() * 401)))
    }
    const waitFor = async <T>(read: () => T | null, timeoutMs = 10_000): Promise<T | null> => {
      const deadline = Date.now() + timeoutMs
      while (Date.now() < deadline) {
        const value = read()
        if (value !== null) return value
        await new Promise((resolve) => setTimeout(resolve, 100))
      }
      return read()
    }

    const initial = readState()
    if (initial.status === 'failed') return failed(initial.reason)
    if (initial.allJobs && initial.unread === unread) return { status: 'ready', changed: false }
    if (!apply) return { status: 'needs_action' }

    let changed = false
    // 脑侧 readList 没有跨命令统一节拍器；首个可见动作前也必须主动留足间隔，
    // 不能只在本函数相邻 click 之间等待。
    await waitInteraction()
    if (!initial.allJobs) {
      initial.trigger.click()
      changed = true
      await waitInteraction()
      let ambiguousOption = false
      const option = await waitFor(() => {
        if (!routeReady()) return null
        const matches = Array.from(document.querySelectorAll<HTMLElement>(
          '.app-job-selector-item, .km-select-option, .km-option, [role="option"]',
        )).filter((node) => visible(node) && clean(node.textContent) === '全部职位')
        if (matches.length > 1) ambiguousOption = true
        return matches.length === 1 ? matches[0] : null
      })
      if (!routeReady()) return failed('route_changed')
      if (!option || ambiguousOption) return failed('job_option_cardinality')
      option.click()
      await waitInteraction()
      const selected = await waitFor(() => selectedTitle() === '全部职位' ? true : null)
      if (!routeReady()) return failed('route_changed')
      if (selected !== true) return failed('job_selection_unconfirmed')
    }

    const beforeUnread = readState()
    if (beforeUnread.status === 'failed') return failed(beforeUnread.reason)
    if (beforeUnread.unread !== unread) {
      beforeUnread.input.click()
      changed = true
      await waitInteraction()
      const confirmed = await waitFor(() => {
        const latest = readState()
        return latest.status === 'ready' && latest.allJobs && latest.unread === unread
          ? true
          : null
      })
      if (!routeReady()) return failed('route_changed')
      if (confirmed !== true) return failed('unread_selection_unconfirmed')
    }

    const finalState = readState()
    if (finalState.status === 'failed') return failed(finalState.reason)
    if (!finalState.allJobs) return failed('job_selection_unconfirmed')
    if (finalState.unread !== unread) return failed('unread_selection_unconfirmed')
    return { status: 'ready', changed }
  } catch {
    return failed('unexpected')
  }
}

// 页面驱动巡检只读取当前真实渲染、且能被后续会话定位器再次找到的虚拟列表窗口。
// 每一行必须带稳定 sessionId/peerPartnerId 的 MAIN-world Vue source；不再用
// getSessions 的独立分页结果或页面启动快照充当导航目标。
// 当前可见底部只收束本轮页面扫描，不承诺平台历史永久穷尽；后续定时巡检仍从
// 新鲜顶部开始，不能因为本轮到底把账号转入人工暂停。
async function mainReadListDOMWindow(
  advance: boolean,
  resetToTop: boolean,
): Promise<MainListDOMWindowResult> {
  // Chrome 在 MAIN-world Promise reject 时可能只返回空 result。与线程读取一致，
  // 在页面函数内部把异常收敛为脱敏哨兵，避免真实 dom_list_* 阶段丢失。
  let diagnosticStage = 'start'
  const execute = async (): Promise<MainListDOMWindowResult> => {
  type AnyRecord = Record<string, unknown>
  diagnosticStage = 'resolve_surface'
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
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' &&
      node.getClientRects().length > 0
  }
  const asRecord = (value: unknown): AnyRecord | null =>
    value && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const rowSource = (node: HTMLElement): AnyRecord => {
    const components: AnyRecord[] = []
    const addComponent = (value: unknown): void => {
      const component = asRecord(value)
      if (component) components.push(component)
    }
    for (const candidate of [node, ...Array.from(node.querySelectorAll<HTMLElement>('*'))]) {
      addComponent((candidate as HTMLElement & { __vue__?: unknown }).__vue__)
    }

    // 智联实时插入新会话后，行节点上的 __vue__ 可能不再暴露；同一页面
    // 的 Nuxt 组件树仍持有与该行 $el 直接绑定的 source。这里只扩宽只读
    // 感知通道，仍要求稳定 sessionId + peerPartnerId 唯一一致，不按姓名、
    // 文本或 DOM 位置猜身份。
    const root = asRecord((window as unknown as AnyRecord).$nuxt)
    if (root) {
      const queue: AnyRecord[] = [root]
      const seen = new Set<AnyRecord>()
      while (queue.length > 0 && seen.size < 4096) {
        const component = queue.shift()
        if (!component || seen.has(component)) continue
        seen.add(component)
        const element = component.$el as HTMLElement | undefined
        if (element && (element === node ||
            (typeof node.contains === 'function' && node.contains(element)))) {
          components.push(component)
        }
        const children = component.$children
        if (Array.isArray(children)) {
          for (const child of children) {
            const record = asRecord(child)
            if (record) queue.push(record)
          }
        }
      }
    }

    const sources: AnyRecord[] = []
    const seenSources = new Set<AnyRecord>()
    const addSource = (value: unknown): void => {
      const source = asRecord(value)
      if (!source || seenSources.has(source)) return
      seenSources.add(source)
      if (clean(source.sessionId) && clean(source.peerPartnerId)) sources.push(source)
    }
    for (const component of components) {
      for (const container of [
        component,
        asRecord(component._props),
        asRecord(component.$props),
        asRecord(component.$data),
      ]) {
        if (!container) continue
        addSource(container)
        addSource(container.source)
      }
    }
    const identityPairs = new Set(sources.map((source) =>
      `${clean(source.sessionId)}\u0000${clean(source.peerPartnerId)}`))
    if (identityPairs.size !== 1) {
      throw new Error(identityPairs.size === 0
        ? 'dom_list_identity_source_unavailable'
        : 'dom_list_identity_invalid')
    }
    sources.sort((left, right) => {
      const richness = (source: AnyRecord): number =>
        (source.lastSentence !== undefined ? 8 : 0) +
        (Number.isInteger(Number(source.unreadCount)) ? 4 : 0) +
        (source.sortTime !== undefined || source.modifiedTime !== undefined ? 2 : 0) +
        (clean(source.name ?? source.realName) ? 1 : 0)
      return richness(right) - richness(left)
    })
    return sources[0]
  }
  const collect = (): {
    sessions: ZhilianConversationSummary[]
    scrollTop: number
    scrollHeight: number
    clientHeight: number
    atBottom: boolean
  } => {
    const scrollTop = scrollElement.scrollTop
    const nodes = Array.from(document.querySelectorAll<HTMLElement>(itemSelector))
      .filter((node) => visible(node) &&
        node.querySelector('.im-session-item__box, .im-session-item') !== null)
    if (nodes.length === 0) throw new Error('dom_list_items_missing')
    const sourcePairs = nodes.map((node) => ({ source: rowSource(node), node }))
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
      const rawPositionTitle = clean(source.jobTitle ?? source.subtitlePrefix) ||
        clean(node?.querySelector('.im-session-item-subtitle__suffix')?.getAttribute('title'))
      const positionTitle = rawPositionTitle &&
        Array.from(rawPositionTitle).length <= 256 &&
        new TextEncoder().encode(rawPositionTitle).length <= 1_024
        ? rawPositionTitle
        : null
      sessions.push({
        conversationRef,
        peer: { displayName, platformUserRef },
        positionTitle,
        unreadCount,
        lastMessage: {
          direction,
          kind: direction === 'system' ? 'system' : 'text',
          textPreview: clampPreview(clean(last.text) || clean(last.title)),
        },
        lastActivityTs: toMillis(source.sortTime ?? last.sendTime ?? source.modifiedTime),
      })
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
  // 稳定判定只看窗口身份与几何。未读数、预览正文和活动时间会被实时新消息
  // 合法改写，不能因为这些业务字段变化把整个可定位窗口误判为 USER_ACTIVE。
  const projection = (snapshot: ReturnType<typeof collect>): string => JSON.stringify({
    conversationRefs: snapshot.sessions.map((session) => session.conversationRef),
    scrollTop: Math.round(snapshot.scrollTop),
    scrollHeight: snapshot.scrollHeight,
    clientHeight: snapshot.clientHeight,
    atBottom: snapshot.atBottom,
  })
  const waitAfterVisibleInteraction = async (): Promise<void> => {
    const delayMs = 1_000 + Math.floor(Math.random() * 401)
    await new Promise((resolve) => setTimeout(resolve, delayMs))
  }

  diagnosticStage = 'reset_to_top'
  if (resetToTop) {
    if (scrollElement.scrollTop > 1) {
      scrollElement.scrollTop = 0
      scrollElement.dispatchEvent(new Event('scroll', { bubbles: true }))
      await waitAfterVisibleInteraction()
    }
  }
  diagnosticStage = 'collect_initial'
  const before = collect()
  if (advance) {
    diagnosticStage = 'advance_window'
    const maxTop = Math.max(0, before.scrollHeight - before.clientHeight)
    const step = Math.max(1, Math.floor(before.clientHeight * 0.7))
    scrollElement.scrollTop = Math.min(maxTop, before.scrollTop + step)
    scrollElement.dispatchEvent(new Event('scroll', { bubbles: true }))
    await waitAfterVisibleInteraction()
  }

  diagnosticStage = 'settle_window'
  await new Promise((resolve) => setTimeout(resolve, 150))
  let latest = collect()
  let stableRounds = 0
  diagnosticStage = 'collect_stability'
  // 条件满足即走；持续渲染时最多等待约 10 秒，不把短暂虚拟列表抖动当失败。
  for (let attempt = 0; attempt < 40 && stableRounds < 2; attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 250))
    const next = collect()
    stableRounds = projection(next) === projection(latest) ? stableRounds + 1 : 0
    latest = next
  }
  const beforeRefs = before.sessions.map((session) => session.conversationRef).join('|')
  const afterRefs = latest.sessions.map((session) => session.conversationRef).join('|')
  diagnosticStage = 'return_result'
  return {
    sessions: latest.sessions,
    atBottom: latest.atBottom,
    moved: !advance || latest.scrollTop > before.scrollTop + 1 || afterRefs !== beforeRefs,
    scrollHeight: latest.scrollHeight,
    scrollTop: latest.scrollTop,
    unstable: stableRounds < 2,
  }
  }

  try {
    return await execute()
  } catch (error) {
    const raw = error instanceof Error ? error.message : ''
    const known = [
      'dom_list_virtual_missing',
      'dom_list_scroll_surface_missing',
      'dom_list_last_sentence_missing',
      'dom_list_last_sentence_invalid',
      'dom_list_identity_source_unavailable',
      'dom_list_items_missing',
      'dom_list_identity_invalid',
      'dom_list_unread_invalid',
    ].find((code) => raw.includes(code)) ?? 'unexpected'
    return {
      __recruitHelperMainError: `read_list_main_failed:${diagnosticStage}:${known}`,
    } as unknown as MainListDOMWindowResult
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

async function uniqueVerifiedIMTab(expected: string | undefined): Promise<chrome.tabs.Tab> {
  const candidates = (await chrome.tabs.query({ url: TAB_QUERY }))
    .filter((tab) => tab.id !== undefined && pageKindFromURL(tab.url) === 'im')
  if (candidates.length === 0) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联 IM 页面不存在', 'afterRecovery', 'pageAbsent')
  }
  if (candidates.length !== 1) {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      '存在多个智联 IM 标签页，无法唯一确认真人当前打开的会话',
      'manualOnly',
    )
  }
  const tab = candidates[0]
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

function currentConversationRefFromTab(tab: chrome.tabs.Tab): string {
  try {
    const route = new URL(tab.url ?? '')
    if (route.pathname !== '/app/im') throw new Error('not im')
    const conversationRef = route.searchParams.get('sessionId')?.trim() ?? ''
    if (!conversationRef ||
        conversationRef.length > 512 ||
        new TextEncoder().encode(conversationRef).length > 2048) {
      throw new Error('conversation missing')
    }
    return conversationRef
  } catch {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      '当前智联 IM 标签页 URL 没有可确认的会话标识',
      'manualOnly',
    )
  }
}

async function assertCurrentThreadRoute(
  tabId: number,
  conversationRef: string,
  expectedPrincipalFingerprint: string,
  sideEffect: SideEffect,
): Promise<chrome.tabs.Tab> {
  const current = await chrome.tabs.get(tabId)
  if (currentConversationRefFromTab(current) !== conversationRef) {
    throw new ZhilianPlatformError(
      'USER_ACTIVE',
      '读取期间真人切换了当前会话，本轮已停止',
      'afterRecovery',
      undefined,
      sideEffect,
    )
  }
  assertExpectedPrincipal(await probeTab(current), expectedPrincipalFingerprint)
  return current
}

export async function identifyZhilianCurrentConversation(
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianCurrentConversation> {
  if (!expectedPrincipalFingerprint) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  const tab = await uniqueVerifiedIMTab(expectedPrincipalFingerprint)
  if (tab.id === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  const before = currentConversationRefFromTab(await chrome.tabs.get(tab.id))
  const latest = await assertCurrentThreadRoute(
    tab.id,
    before,
    expectedPrincipalFingerprint,
    'none',
  )
  const after = currentConversationRefFromTab(latest)
  if (before !== after) {
    throw new ZhilianPlatformError('USER_ACTIVE', '识别期间真人切换了当前会话', 'afterRecovery')
  }
  return { conversationRef: after, observedAt: Date.now() }
}

export async function openZhilianConversation(
  args: ZhilianOpenConversationArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianOpenConversationData> {
  if (validatePrimitiveArgs(PrimitiveName.ChatOpenConversation, 1, args).length !== 0) {
    throw new ZhilianPlatformError('GUARD_FAILED', '打开未读会话参数不符合当前契约', 'manualOnly')
  }
  if (!expectedPrincipalFingerprint) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  const tab = await verifiedIMTab(expectedPrincipalFingerprint)
  if (tab.id === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  }

  ctx.checkpoint()
  const filterState = await runMain(tab.id, mainEnsureChatListFilter, [true, false])
  if (filterState.status !== 'ready') {
    const reason = filterState.status === 'failed' ? filterState.reason : 'not_unread'
    throw new ZhilianPlatformError(
      'GUARD_FAILED',
      `当前聊天列表尚未确认为全部职位未读列表：${reason}`,
      'manualOnly',
    )
  }
  let selected = ''
  try {
    const route = new URL((await chrome.tabs.get(tab.id)).url ?? '')
    if (route.pathname !== '/app/im') throw new Error('not im')
    selected = route.searchParams.get('sessionId') ?? ''
  } catch {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '打开会话前页面离开智联沟通页', 'manualOnly')
  }
  if (selected === args.conversationRef) {
    throw new ZhilianPlatformError(
      'TARGET_NOT_FOUND',
      '目标未读会话已经是当前路由，本原语没有新的打开动作可执行',
      'no',
    )
  }

  let currentWindow: MainListDOMWindowResult
  try {
    currentWindow = await runMain(tab.id, mainReadListDOMWindow, [false, false])
  } catch (error) {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      `当前未读列表无法建立 fresh 可见窗口：${asError(error).message}`,
      'manualOnly',
    )
  }
  if (currentWindow.unstable) {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      '当前未读列表尚未稳定',
      'manualOnly',
    )
  }
  const targetMatches = currentWindow.sessions
    .filter((session) => session.conversationRef === args.conversationRef)
  if (targetMatches.length === 0) {
    throw new ZhilianPlatformError('TARGET_NOT_FOUND', '目标已离开本轮 fresh 未读列表', 'no')
  }
  if (targetMatches.length !== 1) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '目标在当前未读窗口内身份不唯一', 'manualOnly')
  }

  // 上一条列表命令可能刚刚滚动或切换筛选；无论实际间隔多少，本命令在唯一 click
  // 前都再留出 1s+抖动，保证跨命令相邻可见交互也不贴连。
  await new Promise((resolve) => setTimeout(resolve, 1_000 + Math.floor(Math.random() * 401)))
  ctx.checkpoint()
  const changed = await ensureThreadRoute(
    tab,
    args.conversationRef,
    expectedPrincipalFingerprint,
    ctx,
  )
  if (!changed) {
    throw new ZhilianPlatformError(
      'GUARD_FAILED',
      '目标会话没有产生本原语要求的唯一打开动作',
      'manualOnly',
    )
  }

  let positiveRounds = 0
  for (let attempt = 0; attempt < 40; attempt += 1) {
    ctx.checkpoint()
    const latest = await chrome.tabs.get(tab.id)
    let routeMatches = false
    try {
      const route = new URL(latest.url ?? '')
      routeMatches = route.pathname === '/app/im' &&
        route.searchParams.get('sessionId') === args.conversationRef
    } catch {
      routeMatches = false
    }
    if (!routeMatches) {
      throw new ZhilianPlatformError(
        'POSTCONDITION_UNCONFIRMED',
        '打开会话后公开路由未保持目标绑定',
        'manualOnly',
        undefined,
        'possible',
      )
    }
    const latestFilter = await runMain(tab.id, mainEnsureChatListFilter, [true, false])
    if (latestFilter.status !== 'ready') {
      throw new ZhilianPlatformError(
        'POSTCONDITION_UNCONFIRMED',
        '打开会话后全部职位未读筛选状态发生变化',
        'manualOnly',
        undefined,
        'possible',
      )
    }
    const observed = await runMain(tab.id, mainFindConversation, [
      args.conversationRef,
      true,
    ])
    const positive = observed.status === 'found'
      ? observed.unreadMarkerCleared === true
      : observed.reason === 'target_not_found'
    positiveRounds = positive ? positiveRounds + 1 : 0
    if (positiveRounds >= 2) {
      assertExpectedPrincipal(await probeTab(latest), expectedPrincipalFingerprint)
      const data: ZhilianOpenConversationData = {
        conversationRef: args.conversationRef,
        observedAt: Date.now(),
      }
      if (validatePrimitiveData(PrimitiveName.ChatOpenConversation, 1, data).length !== 0) {
        throw new ZhilianPlatformError(
          'POSTCONDITION_UNCONFIRMED',
          '打开会话结果不符合当前契约',
          'manualOnly',
          undefined,
          'possible',
        )
      }
      await ctx.progress('目标未读会话已打开并确认已读收敛', 100)
      return data
    }
    if (observed.status === 'failed' && observed.reason !== 'target_not_found') {
      throw new ZhilianPlatformError(
        'POSTCONDITION_UNCONFIRMED',
        `打开会话后未读行无法复核：${observed.reason ?? 'unknown'}`,
        'manualOnly',
        undefined,
        'possible',
      )
    }
    if (attempt % 8 === 0) {
      await ctx.progress('等待目标会话未读状态收敛', Math.min(90, 20 + attempt))
    }
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  throw new ZhilianPlatformError(
    'POSTCONDITION_UNCONFIRMED',
    '目标只打开一次，但未确认未读标记清除或会话行离开未读列表',
    'manualOnly',
    undefined,
    'possible',
  )
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

function validSourcingWindowResult(value: unknown): value is MainSourcingWindowResult {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  if (record.status === 'failed') {
    return typeof record.reason === 'string' &&
      (MAIN_SOURCING_WINDOW_FAILURE_REASONS as readonly string[]).includes(record.reason)
  }
  return record.status === 'ready' && record.data !== null && typeof record.data === 'object' &&
    !Array.isArray(record.data) &&
    validatePrimitiveData(PrimitiveName.CandidateReadSourcingWindow, 1, record.data).length === 0
}

function validSelectSourcingPositionResult(
  value: unknown,
): value is MainSelectSourcingPositionResult {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  if (record.status === 'failed') {
    return typeof record.reason === 'string' &&
      (MAIN_SELECT_SOURCING_POSITION_FAILURE_REASONS as readonly string[]).includes(record.reason)
  }
  return record.status === 'ready' && record.data !== null && typeof record.data === 'object' &&
    !Array.isArray(record.data) &&
    validatePrimitiveData(PrimitiveName.CandidateSelectSourcingPosition, 1, record.data).length === 0
}

function validApplySourcingFiltersResult(
  value: unknown,
): value is MainApplySourcingFiltersResult {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  if (record.status === 'failed') {
    return typeof record.reason === 'string' &&
      (MAIN_APPLY_SOURCING_FILTERS_FAILURE_REASONS as readonly string[]).includes(record.reason)
  }
  return record.status === 'ready' && record.data !== null && typeof record.data === 'object' &&
    !Array.isArray(record.data) &&
    validatePrimitiveData(PrimitiveName.CandidateApplySourcingFilters, 1, record.data).length === 0
}

async function activeSourcingTabs(): Promise<chrome.tabs.Tab[]> {
  return (await chrome.tabs.query({
    url: TAB_QUERY,
  })).filter((tab) => tab.id !== undefined && pageKindFromURL(tab.url) === 'recommend')
}

async function waitForSourcingReady(
  tab: chrome.tabs.Tab,
  ctx: PrimitiveContext,
): Promise<{ tab: chrome.tabs.Tab; probe: ZhilianProbe }> {
  if (tab.id === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联标签页缺少 id', 'afterRecovery', 'pageBroken')
  }

  // 页面导航也是一次交互；即使 Chrome 很快返回 complete，也给页面至少一秒完成初始化。
  await new Promise<void>((resolve) => {
    setTimeout(resolve, 1_000 + Math.floor(Math.random() * 501))
  })
  for (let attempt = 0; attempt < 34; attempt += 1) {
    ctx.checkpoint()
    const latest = await chrome.tabs.get(tab.id)
    if (latest.status === 'complete' && pageKindFromURL(latest.url) === 'recommend') {
      try {
        const probe = await probeTab(latest)
        if (probe.loginState === 'out' || probe.contentScriptOk) {
          return { tab: latest, probe }
        }
      } catch (error) {
        if (!(error instanceof ZhilianPlatformError) || error.code !== 'CTX_NOT_READY') throw error
      }
    }
    if (attempt % 8 === 0) {
      await ctx.progress('等待智联推荐页就绪', Math.min(95, 15 + attempt * 2))
    }
    if (attempt < 33) {
      await new Promise<void>((resolve) => setTimeout(resolve, 250))
    }
  }
  throw new ZhilianPlatformError(
    'CTX_NOT_READY',
    '智联推荐页在期限内未就绪',
    'afterRecovery',
    'pageBroken',
  )
}

async function ensureZhilianSourcingTab(
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<{ tab: chrome.tabs.Tab; probe: ZhilianProbe | null }> {
  const recommend = await activeSourcingTabs()
  if (recommend.length > 1) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '智联推荐页标签无法唯一确定', 'manualOnly')
  }
  if (recommend.length === 1) return { tab: recommend[0], probe: null }

  ctx.checkpoint()
  await ctx.progress('准备智联推荐页', 5)
  let tab = await canonicalZhilianTab()
  if (!tab) {
    // pageAbsent 仍由脑通过既有 nav.ensureSurface 唯一恢复；恢复后重入本
    // 原语时会复用 nav 创建的智联标签，而不是另开第二张推荐页。
    throw new ZhilianPlatformError(
      'CTX_NOT_READY',
      'Chrome 中没有可复用的智联页面',
      'afterRecovery',
      'pageAbsent',
    )
  }
  if (tab.id === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  // 复用其他智联路由前先核对账号；不能先切页再发现切动了错误账号。
  assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
  const commandNavigation = beginCommandNavigation(tab.id, ctx.irreversibleNotAfterMs)
  try {
    tab = await chrome.tabs.update(tab.id, { url: ZHILIAN_RECOMMEND_URL })
  } catch (error) {
    commandNavigation.end()
    throw error
  }
  return waitForSourcingReady(tab, ctx)
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

function throwSourcingWindowFailure(result: MainSourcingWindowFailed): never {
  if (result.reason === 'route_changed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '窗口读取期间当前推荐页发生变化', 'manualOnly')
  }
  if (result.reason === 'scroll_unavailable') {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '当前推荐列表滚动窗口无法唯一确定', 'manualOnly')
  }
  if (result.reason === 'page_unstable') {
    throw new ZhilianPlatformError(
      'CTX_NOT_READY',
      '当前推荐列表窗口尚未连续稳定',
      'afterRecovery',
      'pageBroken',
    )
  }
  throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '当前推荐窗口身份或职位无法完整且唯一读取', 'manualOnly')
}

function throwSelectSourcingPositionFailure(result: MainSelectSourcingPositionFailed): never {
  if (result.reason === 'route_changed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '职位选择期间当前推荐页发生变化', 'manualOnly')
  }
  if (result.reason === 'target_absent') {
    throw new ZhilianPlatformError('TARGET_NOT_FOUND', '后台绑定职位不在当前智联职位列表中', 'manualOnly')
  }
  if (result.reason === 'drawer_not_ready' || result.reason === 'page_unstable') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联职位选择页面尚未稳定', 'afterRecovery', 'pageBroken')
  }
  throw new ZhilianPlatformError(
    'ELEMENT_UNRESOLVED',
    `智联职位无法唯一选择并确认（${result.reason}）`,
    'manualOnly',
  )
}

function throwApplySourcingFiltersFailure(result: MainApplySourcingFiltersFailed): never {
  if (result.reason === 'route_changed' ||
      result.reason === 'position_identity_mismatch' ||
      result.reason === 'position_title_mismatch') {
    throw new ZhilianPlatformError(
      'CTX_LOST_DURING_EXEC',
      '筛选应用期间当前推荐页或职位发生变化',
      'manualOnly',
    )
  }
  if (result.reason === 'drawer_not_ready' ||
      result.reason === 'list_unavailable' ||
      result.reason === 'list_unstable') {
    throw new ZhilianPlatformError(
      'CTX_NOT_READY',
      '智联筛选面或推荐列表尚未稳定',
      'afterRecovery',
      'pageBroken',
    )
  }
  if (result.reason === 'range_option_unavailable') {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      '目标年龄在当前智联筛选选项中没有精确值',
      'manualOnly',
    )
  }
  throw new ZhilianPlatformError(
    'ELEMENT_UNRESOLVED',
    `智联筛选条件无法完整覆盖并回读确认（${result.reason}）`,
    'manualOnly',
  )
}

function sameSourcingFilters(
  left: CandidateSourcingFilters,
  right: CandidateSourcingFilters,
): boolean {
  const sameSet = (a: string[], b: string[]): boolean =>
    a.length === b.length &&
    [...a].sort().every((value, index) => value === [...b].sort()[index])
  return left.age.mode === right.age.mode &&
    left.age.minAge === right.age.minAge &&
    left.age.maxAge === right.age.maxAge &&
    left.activeWindow === right.activeWindow &&
    sameSet(left.careerStatuses, right.careerStatuses) &&
    sameSet(left.educations, right.educations) &&
    left.gender === right.gender &&
    left.excludeViewed === right.excludeViewed &&
    left.excludeCoworkerContacted === right.excludeCoworkerContacted
}

export async function selectZhilianSourcingPosition(
  args: ZhilianSelectSourcingPositionArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianSelectSourcingPositionData> {
  const positionTitle = typeof args?.positionTitle === 'string'
    ? args.positionTitle.normalize('NFC').replace(/[\s\u00a0]+/gu, ' ').trim()
    : ''
  if (!positionTitle || positionTitle.length > 256 ||
      new TextEncoder().encode(positionTitle).length > 1_024) {
    throw new ZhilianPlatformError('GUARD_FAILED', '职位选择缺少合法的职位标题', 'manualOnly')
  }
  ctx.checkpoint()
  const prepared = await ensureZhilianSourcingTab(ctx, expectedPrincipalFingerprint)
  const tab = prepared.tab
  if (tab.id === undefined || tab.status !== 'complete') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '当前智联推荐页尚未就绪', 'afterRecovery', 'pageBroken')
  }
  const initialProbe = prepared.probe ?? await probeTab(tab)
  if (initialProbe.pageKind !== 'recommend') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '当前智联页面不是推荐页', 'afterRecovery', 'pageAbsent')
  }
  assertExpectedPrincipal(initialProbe, expectedPrincipalFingerprint)
  ctx.progress('核对当前推荐页与登录身份', 10)

  ctx.checkpoint()
  const beforeActionTabs = await activeSourcingTabs()
  if (beforeActionTabs.length !== 1 || beforeActionTabs[0].id !== tab.id ||
      beforeActionTabs[0].status !== 'complete') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '职位选择前推荐页标签发生切换', 'manualOnly')
  }
  assertExpectedPrincipal(await probeTab(beforeActionTabs[0]), expectedPrincipalFingerprint)
  await ctx.beforeSideEffect()
  const result = await runMain(tab.id, mainSelectSourcingPosition, [positionTitle])
  if (!validSelectSourcingPositionResult(result)) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '职位选择结果结构不符合当前契约', 'manualOnly')
  }
  if (result.status === 'failed') throwSelectSourcingPositionFailure(result)
  if (result.data.positionTitle !== positionTitle) {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '职位选择结果与目标标题不一致', 'manualOnly')
  }

  ctx.checkpoint()
  const latestTabs = await activeSourcingTabs()
  if (latestTabs.length !== 1 || latestTabs[0].id !== tab.id || latestTabs[0].status !== 'complete') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '职位选择期间推荐页标签发生切换', 'manualOnly')
  }
  assertExpectedPrincipal(await probeTab(latestTabs[0]), expectedPrincipalFingerprint)
  if (validatePrimitiveData(
    PrimitiveName.CandidateSelectSourcingPosition,
    1,
    result.data,
  ).length !== 0 || jsonBytes(result.data) > 4_096) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '职位选择结果不符合当前契约', 'manualOnly')
  }
  ctx.progress('智联推荐页职位选择完成', 100)
  return result.data
}

export async function applyZhilianSourcingFilters(
  args: ZhilianApplySourcingFiltersArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianApplySourcingFiltersData> {
  if (validatePrimitiveArgs(
    PrimitiveName.CandidateApplySourcingFilters,
    1,
    args,
  ).length !== 0) {
    throw new ZhilianPlatformError('GUARD_FAILED', '采集筛选目标不符合当前契约', 'manualOnly')
  }
  const positionRef = args.positionRef.trim()
  const positionTitle = args.positionTitle.normalize('NFC').replace(/[\s\u00a0]+/gu, ' ').trim()
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
  ctx.progress('核对筛选目标、推荐页与登录身份', 10)

  ctx.checkpoint()
  const beforeActionTabs = await activeSourcingTabs()
  if (beforeActionTabs.length !== 1 || beforeActionTabs[0].id !== tab.id ||
      beforeActionTabs[0].status !== 'complete') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '筛选应用前推荐页标签发生切换', 'manualOnly')
  }
  assertExpectedPrincipal(await probeTab(beforeActionTabs[0]), expectedPrincipalFingerprint)
  await ctx.beforeSideEffect()
  const result = await runMain(tab.id, mainApplySourcingFilters, [
    positionRef,
    positionTitle,
    args.filters,
  ])
  if (!validApplySourcingFiltersResult(result)) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '筛选应用结果结构不符合当前契约', 'manualOnly')
  }
  if (result.status === 'failed') throwApplySourcingFiltersFailure(result)
  if (result.data.positionRef !== positionRef ||
      result.data.positionTitle !== positionTitle ||
      !sameSourcingFilters(result.data.filters, args.filters)) {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '筛选回读结果与目标绑定不一致', 'manualOnly')
  }

  ctx.checkpoint()
  const latestTabs = await activeSourcingTabs()
  if (latestTabs.length !== 1 || latestTabs[0].id !== tab.id || latestTabs[0].status !== 'complete') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '筛选应用期间推荐页标签发生切换', 'manualOnly')
  }
  assertExpectedPrincipal(await probeTab(latestTabs[0]), expectedPrincipalFingerprint)
  if (validatePrimitiveData(
    PrimitiveName.CandidateApplySourcingFilters,
    1,
    result.data,
  ).length !== 0) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '筛选应用结果不符合当前契约', 'manualOnly')
  }
  ctx.progress('智联采集筛选已覆盖并二次回读确认', 100)
  return result.data
}

export async function readZhilianSourcingWindow(
  args: ZhilianSourcingWindowArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianSourcingWindowData> {
  if (!args || !['current', 'reset', 'next'].includes(args.move)) {
    throw new ZhilianPlatformError('GUARD_FAILED', '窗口读取缺少合法的移动指令', 'manualOnly')
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
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '窗口动作前推荐页标签发生切换', 'manualOnly')
  }
  assertExpectedPrincipal(await probeTab(beforeActionTabs[0]), expectedPrincipalFingerprint)
  await ctx.beforeSideEffect()
  const result = await runMain(tab.id, mainReadSourcingWindow, [args.move])
  if (!validSourcingWindowResult(result)) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '推荐窗口读取结果结构不符合当前契约', 'manualOnly')
  }
  if (result.status === 'failed') throwSourcingWindowFailure(result)

  ctx.checkpoint()
  const latestTabs = await activeSourcingTabs()
  if (latestTabs.length !== 1 || latestTabs[0].id !== tab.id || latestTabs[0].status !== 'complete') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '窗口读取期间推荐页标签发生切换', 'manualOnly')
  }
  assertExpectedPrincipal(await probeTab(latestTabs[0]), expectedPrincipalFingerprint)
  if (validatePrimitiveData(PrimitiveName.CandidateReadSourcingWindow, 1, result.data).length !== 0 ||
      jsonBytes(result.data) > 32_768) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '推荐窗口结构不符合当前契约', 'manualOnly')
  }
  ctx.progress('推荐候选人窗口读取完成', 100)
  return result.data
}

export async function readZhilianSourcingTargetResume(
  args: ZhilianSourcingTargetResumeArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianSourcingResumeData> {
  if (!args || typeof args.platformUserRef !== 'string' || !args.platformUserRef ||
      typeof args.positionRef !== 'string' || !args.positionRef) {
    throw new ZhilianPlatformError('GUARD_FAILED', '定点简历读取缺少候选人或职位引用', 'manualOnly')
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
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '定点读取前推荐页标签发生切换', 'manualOnly')
  }
  assertExpectedPrincipal(await probeTab(beforeActionTabs[0]), expectedPrincipalFingerprint)
  await ctx.beforeSideEffect()
  const result = await runMain(tab.id, mainReadSourcingResume, [[], {
    platformUserRef: args.platformUserRef,
    positionRef: args.positionRef,
  }])
  if (!validSourcingResumeResult(result)) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '定点简历读取结果结构不符合当前契约', 'manualOnly')
  }
  if (result.status === 'failed') throwSourcingResumeFailure(result)
  if (result.data.platformUserRef !== args.platformUserRef || result.data.positionRef !== args.positionRef) {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '定点简历读取结果与目标绑定不一致', 'manualOnly')
  }

  ctx.checkpoint()
  const latestTabs = await activeSourcingTabs()
  if (latestTabs.length !== 1 || latestTabs[0].id !== tab.id || latestTabs[0].status !== 'complete') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '定点读取期间推荐页标签发生切换', 'manualOnly')
  }
  assertExpectedPrincipal(await probeTab(latestTabs[0]), expectedPrincipalFingerprint)
  if (validatePrimitiveData(PrimitiveName.CandidateReadSourcingTargetResume, 1, result.data).length !== 0 ||
      jsonBytes(result.data) > 65_536) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '定点完整简历结构不符合当前契约', 'manualOnly')
  }
  ctx.progress('推荐候选人定点简历读取完成', 100)
  return result.data
}

// ── 职位发布前预检:读取本账号在平台上已存在的职位名全集 ──────────────
//
// 只服务发布前的"同名是否已存在"判定。分区(在线中/未上线/审核中/未过审)
// 必须全部读到并合并:少读一个分区会把"已存在"误判成"可发",方向即多发。
// 分区按 DOM 顺序整体遍历而不按文案匹配,平台改文案或新增分区时仍然全覆盖。

const ZHILIAN_JOB_LIST_URL = `https://${ZHILIAN_HOST}/app/job`

function isZhilianJobListURL(url: string | undefined): boolean {
  if (!url) return false
  try {
    const path = new URL(url).pathname
    return path === '/app/job' || path.startsWith('/app/job/')
  } catch {
    // URL 来自 chrome.tabs,解析失败即按"不是职位页"响亮降级,不猜。
    return false
  }
}

type MainJobSectionResult =
  | {
    status: 'ok'
    sectionCount: number
    activeIndex: number
    names: string[]
    // null = 页面未渲染分页统计,此时 names 必须为空(空分区)。
    total: number | null
  }
  | { status: 'failed'; reason: string }

function mainReadZhilianJobSection(): MainJobSectionResult {
  const bar = document.querySelector('.job-status-bar ul')
  if (!bar) return { status: 'failed', reason: 'section_bar_absent' }
  const items = Array.from(bar.children).filter((node) => node.tagName === 'LI')
  if (items.length === 0) return { status: 'failed', reason: 'section_bar_absent' }
  const activeIndex = items.findIndex((node) => node.className.split(/\s+/).includes('is-active'))
  if (activeIndex < 0) return { status: 'failed', reason: 'section_not_active' }

  const names: string[] = []
  for (const anchor of Array.from(document.querySelectorAll('a.job-item__title--jobname'))) {
    const text = ((anchor as HTMLElement).innerText || '')
      .normalize('NFC').replace(/[\s ]+/gu, ' ').trim()
    // 读到一张卡片却拿不到名字时不能跳过:那正是会被误判成"可发"的那一个。
    if (!text || text.length > 256) return { status: 'failed', reason: 'posting_name_unreadable' }
    names.push(text)
  }

  let total: number | null = null
  const totalNode = document.querySelector('.km-pagination__total')
  if (totalNode) {
    const matched = /共\s*(\d+)\s*条/.exec((totalNode as HTMLElement).innerText || '')
    if (!matched) return { status: 'failed', reason: 'total_unreadable' }
    total = Number(matched[1])
    if (!Number.isSafeInteger(total) || total < 0) {
      return { status: 'failed', reason: 'total_unreadable' }
    }
  }
  return { status: 'ok', sectionCount: items.length, activeIndex, names, total }
}

function mainClickZhilianJobSection(index: number): { status: 'ok' } | { status: 'failed'; reason: string } {
  const bar = document.querySelector('.job-status-bar ul')
  if (!bar) return { status: 'failed', reason: 'section_bar_absent' }
  const items = Array.from(bar.children).filter((node) => node.tagName === 'LI')
  if (index < 0 || index >= items.length) return { status: 'failed', reason: 'section_index_out_of_range' }
  const target = items[index] as HTMLElement
  if (typeof target.click !== 'function') return { status: 'failed', reason: 'section_not_clickable' }
  target.click()
  return { status: 'ok' }
}

function validJobSectionResult(value: unknown): value is MainJobSectionResult {
  if (typeof value !== 'object' || value === null) return false
  const record = value as Record<string, unknown>
  if (record.status === 'failed') return typeof record.reason === 'string'
  if (record.status !== 'ok') return false
  return typeof record.sectionCount === 'number' && typeof record.activeIndex === 'number' &&
    Array.isArray(record.names) && record.names.every((name) => typeof name === 'string') &&
    (record.total === null || typeof record.total === 'number')
}

async function ensureZhilianJobListTab(
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<chrome.tabs.Tab> {
  const existing = (await chrome.tabs.query({ url: TAB_QUERY }))
    .filter((tab) => tab.id !== undefined && isZhilianJobListURL(tab.url))
  if (existing.length > 1) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '智联职位页标签无法唯一确定', 'manualOnly')
  }
  if (existing.length === 1 && existing[0].status === 'complete') {
    assertExpectedPrincipal(await probeTab(existing[0]), expectedPrincipalFingerprint)
    return existing[0]
  }

  ctx.checkpoint()
  await ctx.progress('准备智联职位页', 5)
  let tab = existing.length === 1 ? existing[0] : await canonicalZhilianTab()
  if (!tab || tab.id === undefined) {
    throw new ZhilianPlatformError(
      'CTX_NOT_READY', 'Chrome 中没有可复用的智联页面', 'afterRecovery', 'pageAbsent',
    )
  }
  // 复用其他智联路由前先核对账号;不能先切页再发现切动了错误账号。
  assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
  const commandNavigation = beginCommandNavigation(tab.id, ctx.irreversibleNotAfterMs)
  try {
    tab = await chrome.tabs.update(tab.id, { url: ZHILIAN_JOB_LIST_URL })
  } catch (error) {
    commandNavigation.end()
    throw error
  }
  return waitForZhilianJobListReady(tab, ctx)
}

async function waitForZhilianJobListReady(
  tab: chrome.tabs.Tab,
  ctx: PrimitiveContext,
): Promise<chrome.tabs.Tab> {
  if (tab.id === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  // 页面导航也是一次交互;即使 Chrome 很快返回 complete,也给页面至少一秒完成初始化。
  await new Promise<void>((resolve) => {
    setTimeout(resolve, 1_000 + Math.floor(Math.random() * 501))
  })
  // 条件轮询:就绪即继续,最长约 10 秒,不做固定等待。
  for (let attempt = 0; attempt < 40; attempt += 1) {
    ctx.checkpoint()
    const latest = await chrome.tabs.get(tab.id)
    if (latest.status === 'complete' && isZhilianJobListURL(latest.url)) {
      // 同上：complete 不代表 SPA 已经渲染完，注入可能拿不到返回值。
      try {
        const probed = await runMain(latest.id as number, mainReadZhilianJobSection, [])
        if (validJobSectionResult(probed) && probed.status === 'ok') return latest
      } catch (error) {
        if (!(error instanceof ZhilianPlatformError)) throw error
      }
    }
    if (attempt % 10 === 0) {
      await ctx.progress('等待智联职位页就绪', Math.min(20, 5 + attempt))
    }
    await new Promise<void>((resolve) => setTimeout(resolve, 250))
  }
  throw new ZhilianPlatformError(
    'CTX_NOT_READY', '智联职位页在期限内未就绪', 'afterRecovery', 'pageBroken',
  )
}

function throwJobSectionFailure(reason: string): never {
  if (reason === 'posting_name_unreadable' || reason === 'total_unreadable') {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '智联职位页条目无法完整读取', 'manualOnly')
  }
  throw new ZhilianPlatformError('CTX_NOT_READY', '智联职位页分区结构不可识别', 'afterRecovery', 'pageBroken')
}

// 切到指定分区并读取它。条件轮询等待分区真正激活且条目渲染完整。
async function readZhilianJobSection(
  tabId: number,
  index: number,
  ctx: PrimitiveContext,
): Promise<{ names: string[]; sectionCount: number }> {
  const clicked = await runMain(tabId, mainClickZhilianJobSection, [index])
  if (typeof clicked !== 'object' || clicked === null || (clicked as { status?: string }).status !== 'ok') {
    const reason = (clicked as { reason?: string } | null)?.reason ?? 'section_click_failed'
    throwJobSectionFailure(reason)
  }
  for (let attempt = 0; attempt < 40; attempt += 1) {
    ctx.checkpoint()
    const result = await runMain(tabId, mainReadZhilianJobSection, [])
    if (!validJobSectionResult(result)) {
      throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '智联职位分区结果结构不符合预期', 'manualOnly')
    }
    if (result.status === 'failed') throwJobSectionFailure(result.reason)
    // 分区已激活,且"共 N 条"与实际读到的卡片数一致才算渲染完整。
    // 数量对不上意味着还在渲染,或者存在未读到的分页——两种都不能当作读全。
    if (result.activeIndex === index) {
      const expected = result.total ?? 0
      if (result.names.length === expected) return { names: result.names, sectionCount: result.sectionCount }
    }
    await new Promise<void>((resolve) => setTimeout(resolve, 250))
  }
  // 稳定不下来的最常见原因是条目超过单页:此时读到的一定少于"共 N 条"。
  throw new ZhilianPlatformError(
    'ELEMENT_UNRESOLVED', '智联职位分区未在期限内完整渲染(可能存在未读取的分页)', 'manualOnly',
  )
}

export async function readZhilianPublishedJobs(
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<JobReadPublishedListData> {
  ctx.checkpoint()
  const tab = await ensureZhilianJobListTab(ctx, expectedPrincipalFingerprint)
  const tabId = tab.id
  if (tabId === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
  await ctx.progress('核对智联职位页与登录身份', 15)

  const first = await runMain(tabId, mainReadZhilianJobSection, [])
  if (!validJobSectionResult(first)) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '智联职位分区结果结构不符合预期', 'manualOnly')
  }
  if (first.status === 'failed') throwJobSectionFailure(first.reason)
  const sectionCount = first.sectionCount
  if (sectionCount < 1 || sectionCount > 16) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '智联职位分区数量超出已知范围', 'manualOnly')
  }

  const collected = new Set<string>()
  for (let index = 0; index < sectionCount; index += 1) {
    ctx.checkpoint()
    // 相邻平台可见交互至少间隔一秒并带随机抖动。
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 1_000 + Math.floor(Math.random() * 501))
    })
    const section = await readZhilianJobSection(tabId, index, ctx)
    if (section.sectionCount !== sectionCount) {
      throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '读取期间智联职位分区结构发生变化', 'manualOnly')
    }
    for (const name of section.names) collected.add(name)
    await ctx.progress(`已读取第 ${index + 1}/${sectionCount} 个职位分区`, 15 + Math.floor((index + 1) * 70 / sectionCount))
  }

  // 分区遍历会切走页面;返回前再复核一次身份,避免中途换号。
  const latest = await chrome.tabs.get(tabId)
  if (!isZhilianJobListURL(latest.url) || latest.status !== 'complete') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '读取期间智联职位页发生跳转', 'manualOnly')
  }
  assertExpectedPrincipal(await probeTab(latest), expectedPrincipalFingerprint)

  const postingNames = Array.from(collected)
  if (postingNames.length > 200) {
    throw new ZhilianPlatformError('PAYLOAD_LIMIT', '平台职位数量超过当前契约上限', 'manualOnly')
  }
  const data: JobReadPublishedListData = { postingNames, observedAt: Date.now() }
  if (validatePrimitiveData(PrimitiveName.JobReadPublishedList, 1, data).length !== 0) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '职位名清单不符合当前契约', 'manualOnly')
  }
  await ctx.progress('智联职位名清单读取完成', 100)
  return data
}

// ── 职位发布表单试填与回读 ────────────────────────────────────────────
//
// 只填不交:全程不点击"发布"或任何提交控件,回读完成后主动离开表单。
// 一个填满的发布表单只差一次点击,留在页面上等同于给人工误操作递刀。
//
// 下面这些 MAIN world 片段的每一条判据都在真机上逐条验证过:富文本要靠
// 异步同步到隐藏 textarea、职位类别只在描述失焦后才由平台生成、下拉面板
// 挂在 body 下且两种结构不同、关键词分组随职位类别变化。

const ZHILIAN_JOB_PUBLISH_URL = `https://${ZHILIAN_HOST}/job/publish`

function isZhilianJobPublishURL(url: string | undefined): boolean {
  if (!url) return false
  try {
    return new URL(url).pathname === '/job/publish'
  } catch {
    return false
  }
}

type MainStep = { status: 'ok'; detail?: string } | { status: 'failed'; reason: string }

function validMainStep(value: unknown): value is MainStep {
  if (typeof value !== 'object' || value === null) return false
  const record = value as Record<string, unknown>
  if (record.status === 'ok') return record.detail === undefined || typeof record.detail === 'string'
  return record.status === 'failed' && typeof record.reason === 'string'
}

// 职位名称:Vue 的 v-model 只认原生 input 事件,且必须走原型 setter,
// 否则框架对 value 的属性劫持会吃掉这次赋值。
function mainFillZhilianJobName(name: string): MainStep {
  const input = document.querySelector('.publish-form__name input') as HTMLInputElement | null
  if (!input) return { status: 'failed', reason: 'name_input_absent' }
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
  if (!setter) return { status: 'failed', reason: 'name_setter_absent' }
  setter.call(input, name)
  input.dispatchEvent(new Event('input', { bubbles: true }))
  input.dispatchEvent(new Event('change', { bubbles: true }))
  return input.value === name ? { status: 'ok' } : { status: 'failed', reason: 'name_not_applied' }
}

// 职位描述:jqte 富文本。按行包 div 与真实键盘输入产生的结构一致;
// 隐藏 textarea 是异步同步的,所以这里只写入,由调用方轮询确认同步完成。
function mainWriteZhilianDescription(lines: string[]): MainStep {
  const editor = document.querySelector('.jqte_editor') as HTMLElement | null
  if (!editor) return { status: 'failed', reason: 'description_editor_absent' }
  const escape = (text: string): string =>
    text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  const html = lines.length === 0
    ? ''
    : escape(lines[0]) + lines.slice(1).map((line) => `<div>${escape(line)}</div>`).join('')
  editor.innerHTML = html
  editor.dispatchEvent(new Event('input', { bubbles: true }))
  return { status: 'ok', detail: html }
}

function mainReadZhilianDescriptionSync(expectedHTML: string): MainStep {
  const editor = document.querySelector('.jqte_editor') as HTMLElement | null
  const items = Array.from(document.querySelectorAll('.km-form-item'))
  const item = items.find((node) => {
    const label = node.querySelector(':scope > label, :scope > [class*="label"]') as HTMLElement | null
    return label?.innerText.trim() === '职位描述'
  })
  const area = item?.querySelector('textarea') as HTMLTextAreaElement | null
  if (!editor || !area) return { status: 'failed', reason: 'description_editor_absent' }
  if (area.value !== expectedHTML) return { status: 'failed', reason: 'description_not_synced' }
  return { status: 'ok', detail: String(editor.innerText.trim().length) }
}

// 平台只在描述失焦后才判定职位类别;这一串事件是真机验证过的最小触发集。
function mainBlurZhilianDescription(): MainStep {
  const editor = document.querySelector('.jqte_editor') as HTMLElement | null
  if (!editor) return { status: 'failed', reason: 'description_editor_absent' }
  editor.focus()
  editor.dispatchEvent(new Event('input', { bubbles: true }))
  editor.dispatchEvent(new Event('keyup', { bubbles: true }))
  editor.blur()
  editor.dispatchEvent(new Event('blur', { bubbles: true }))
  editor.dispatchEvent(new FocusEvent('focusout', { bubbles: true }))
  return { status: 'ok' }
}

function mainReadZhilianAutoJobClass(): MainStep {
  const node = document.querySelector('.job-subType-input') as HTMLElement | null
  if (!node) return { status: 'failed', reason: 'job_class_absent' }
  const text = node.innerText.trim().split('\n')[0].trim()
  if (!text || text === '请选择') return { status: 'failed', reason: 'job_class_pending' }
  return { status: 'ok', detail: text.slice(0, 64) }
}

function mainPickZhilianEmployment(label: string): MainStep {
  const items = Array.from(document.querySelectorAll('.km-form-item'))
  const item = items.find((node) => {
    const node2 = node.querySelector(':scope > label, :scope > [class*="label"]') as HTMLElement | null
    return node2?.innerText.trim() === '工作性质'
  })
  if (!item) return { status: 'failed', reason: 'employment_group_absent' }
  const buttons = Array.from(item.querySelectorAll('button')) as HTMLElement[]
  const hit = buttons.filter((button) => button.innerText.trim() === label)
  if (hit.length !== 1) {
    return { status: 'failed', reason: hit.length === 0 ? 'employment_absent' : 'employment_ambiguous' }
  }
  hit[0].click()
  return { status: 'ok' }
}

function mainReadZhilianEmployment(): MainStep {
  const items = Array.from(document.querySelectorAll('.km-form-item'))
  const item = items.find((node) => {
    const label = node.querySelector(':scope > label, :scope > [class*="label"]') as HTMLElement | null
    return label?.innerText.trim() === '工作性质'
  })
  if (!item) return { status: 'failed', reason: 'employment_group_absent' }
  // 选中项是 km-button--primary，未选中是 km-button--light。这组按钮没有
  // aria-pressed 之类的标准属性可读，只能认这个可见样式类。
  const active = Array.from(item.querySelectorAll('button')).filter((button) =>
    button.className.split(/\s+/).includes('km-button--primary'))
  if (active.length !== 1) return { status: 'failed', reason: 'employment_state_unreadable' }
  return { status: 'ok', detail: (active[0] as HTMLElement).innerText.trim() }
}

function mainCloseZhilianPanels(): MainStep {
  const visible = Array.from(document.querySelectorAll('.km-select__dropdown, .salary-popover')).filter((node) => {
    const style = window.getComputedStyle(node)
    return node.getBoundingClientRect().height > 0 && style.opacity === '1' && style.visibility !== 'hidden'
  })
  if (visible.length === 0) return { status: 'ok', detail: 'closed' }
  document.body.click()
  return { status: 'ok', detail: 'closing' }
}

// 下拉:面板挂在 body 下、与触发控件没有 DOM 父子关系,只能按水平位置就近匹配。
// 选项在两种面板里结构不同(普通下拉是 li,薪资是 a.km-option),不能用宽泛的
// [class*="option"]——列表容器自身也带 option 字样,会把整张列表当成一个选项。
function mainOpenZhilianSelect(placeholder: string): MainStep {
  const input = document.querySelector(`input[placeholder="${placeholder}"]`) as HTMLInputElement | null
  if (!input) return { status: 'failed', reason: 'select_input_absent' }
  input.click()
  return { status: 'ok', detail: String(Math.round(input.getBoundingClientRect().left)) }
}

function mainPickZhilianSelectOption(anchorLeft: number, wanted: string): MainStep {
  const visible = Array.from(document.querySelectorAll('.km-select__dropdown, .salary-popover')).filter((node) => {
    const style = window.getComputedStyle(node)
    return node.getBoundingClientRect().height > 0 && style.opacity === '1' && style.visibility !== 'hidden'
  })
  if (visible.length === 0) return { status: 'failed', reason: 'select_panel_absent' }
  const panel = visible.sort((a, b) =>
    Math.abs(a.getBoundingClientRect().left - anchorLeft) -
    Math.abs(b.getBoundingClientRect().left - anchorLeft))[0]
  const options = Array.from(panel.querySelectorAll('li, a.km-option')) as HTMLElement[]
  const hit = options.filter((option) => option.textContent?.trim() === wanted)
  if (hit.length !== 1) {
    return { status: 'failed', reason: hit.length === 0 ? 'option_absent' : 'option_ambiguous' }
  }
  hit[0].click()
  return { status: 'ok' }
}

function mainReadZhilianSelectValue(placeholder: string): MainStep {
  const input = document.querySelector(`input[placeholder="${placeholder}"]`) as HTMLInputElement | null
  if (!input) return { status: 'failed', reason: 'select_input_absent' }
  return { status: 'ok', detail: input.value }
}

// 薪资月数没有 placeholder(平台预置了默认值),只能按当前值形态定位。
function mainReadZhilianSalaryMonths(): MainStep {
  const inputs = Array.from(document.querySelectorAll('input')).filter((input) =>
    /^\d+个月$/.test((input as HTMLInputElement).value.trim())) as HTMLInputElement[]
  if (inputs.length !== 1) return { status: 'failed', reason: 'salary_months_unresolved' }
  return { status: 'ok', detail: inputs[0].value.trim() }
}

function mainOpenZhilianSalaryMonths(): MainStep {
  const inputs = Array.from(document.querySelectorAll('input')).filter((input) =>
    /^\d+个月$/.test((input as HTMLInputElement).value.trim())) as HTMLInputElement[]
  if (inputs.length !== 1) return { status: 'failed', reason: 'salary_months_unresolved' }
  inputs[0].click()
  return { status: 'ok', detail: String(Math.round(inputs[0].getBoundingClientRect().left)) }
}

function mainFillZhilianHeadcount(headcount: number): MainStep {
  const items = Array.from(document.querySelectorAll('.km-form-item'))
  const item = items.find((node) => {
    const label = node.querySelector(':scope > label, :scope > [class*="label"]') as HTMLElement | null
    return label?.innerText.trim() === '招聘人数'
  })
  const input = item?.querySelector('input[type="text"], input:not([type])') as HTMLInputElement | null
  if (!input) return { status: 'failed', reason: 'headcount_input_absent' }
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
  if (!setter) return { status: 'failed', reason: 'headcount_setter_absent' }
  setter.call(input, String(headcount))
  input.dispatchEvent(new Event('input', { bubbles: true }))
  input.dispatchEvent(new Event('change', { bubbles: true }))
  return { status: 'ok', detail: input.value.trim() }
}

// 读页面自己给出的校验/合规提示。平台会因为内容不合规而拒绝往下走(例如描述里
// 写了年龄要求就不判定职位类别),此时只有平台原话能说清要改什么。
function mainReadZhilianFormHints(): MainStep {
  const seen: string[] = []
  for (const node of Array.from(document.querySelectorAll('*'))) {
    if (node.children.length !== 0) continue
    const text = (node.textContent ?? '').trim()
    if (!text || text.length > 200) continue
    if (!/不能|不可|请勿|违规|敏感|不合规|超过|必填|请输入|请选择/.test(text)) continue
    if (node.getBoundingClientRect().height <= 0) continue
    if (!seen.includes(text)) seen.push(text)
    if (seen.length >= 6) break
  }
  return { status: 'ok', detail: seen.join(' ▍ ').slice(0, 900) }
}

function mainReadZhilianWorkplace(): MainStep {
  const input = document.querySelector('input[placeholder="请输入工作地址"]') as HTMLInputElement | null
  if (!input) return { status: 'failed', reason: 'workplace_absent' }
  const value = input.value.trim()
  if (!value) return { status: 'failed', reason: 'workplace_empty' }
  return { status: 'ok', detail: value.slice(0, 256) }
}

// 关键词弹层。分组数量、分组名称、组内词库与是否自动预选全都随职位类别变化,
// 因此这里不认任何固定词表:先整体清空平台的预选(消除不可预测的配额占用),
// 再按当前实际词库全等匹配。
// 注意:下面每个 main* 片段都会被序列化后注入 MAIN world 执行,
// 因此不能引用模块作用域的任何常量,选择器一律就地内联。
type MainKeywordSnapshot =
  | {
    status: 'ok'
    selected: string[]
    sectionTitles: string[]
    available: string[]
    confirmLabel: string
  }
  | { status: 'failed'; reason: string }

function mainOpenZhilianKeywords(): MainStep {
  const items = Array.from(document.querySelectorAll('.km-form-item'))
  const item = items.find((node) => {
    const label = node.querySelector(':scope > label, :scope > [class*="label"]') as HTMLElement | null
    return label?.innerText.trim() === '职位关键词'
  })
  const button = item?.querySelector('button') as HTMLElement | null
  if (!button) return { status: 'failed', reason: 'keyword_entry_absent' }
  button.click()
  return { status: 'ok' }
}

function mainSnapshotZhilianKeywords(): MainKeywordSnapshot {
  const nodes = Array.from(document.querySelectorAll('[class*="dialog"], [class*="modal"]')) as HTMLElement[]
  const dialog = nodes.find((node) =>
    node.getBoundingClientRect().height > 200 && /职位关键词/.test(node.innerText)) ?? null
  if (!dialog) return { status: 'failed', reason: 'keyword_dialog_absent' }
  const items = Array.from(dialog.querySelectorAll('li.s-checkbutton-drilldown-multi-limit__item')) as HTMLElement[]
  const selected = items.filter((item) => item.className.includes('--selected'))
    .map((item) => (item.textContent ?? '').trim()).filter(Boolean)
  const available = items.map((item) => (item.textContent ?? '').trim()).filter(Boolean)
  // 兜底组「您还有哪些招聘要求？」的 DOM 结构与其他分组不同,不在 __list-item
  // 里。按 (已选/上限) 计数文本抓才能覆盖全部分组——只认 __list-item 会漏掉它,
  // 让诊断看起来像"这个职位没有兜底组",而实际上词就是加进它里面的。
  const sectionTitles: string[] = []
  for (const node of Array.from(dialog.querySelectorAll('div'))) {
    // 分组标题的形态固定是「……？ (已选/上限)」。按它匹配 innerText 的首行,
    // 既能覆盖结构与众不同的兜底组「您还有哪些招聘要求？」,又不会把裸计数
    // 节点当成分组。嵌套 div 的首行相同,靠去重收敛。
    const firstLine = ((node as HTMLElement).innerText ?? '')
      .split('\n')[0].trim().replace(/\s+/g, ' ')
    if (!/？\s*\(\d+\/\d+\)$/.test(firstLine) || firstLine.length > 60) continue
    if (!sectionTitles.includes(firstLine)) sectionTitles.push(firstLine)
    if (sectionTitles.length >= 24) break
  }
  const confirm = Array.from(dialog.querySelectorAll('.s-button')).map((button) =>
    (button as HTMLElement).innerText.trim()).find((text) => /^确定/.test(text)) ?? ''
  return { status: 'ok', selected, sectionTitles, available, confirmLabel: confirm }
}

function validKeywordSnapshot(value: unknown): value is MainKeywordSnapshot {
  if (typeof value !== 'object' || value === null) return false
  const record = value as Record<string, unknown>
  if (record.status === 'failed') return typeof record.reason === 'string'
  return record.status === 'ok' && Array.isArray(record.selected) &&
    Array.isArray(record.sectionTitles) && Array.isArray(record.available) &&
    typeof record.confirmLabel === 'string'
}

// 再次点击已选词条即取消。整体清空后配额才算得准。
function mainClearOneZhilianKeyword(): MainStep {
  const nodes = Array.from(document.querySelectorAll('[class*="dialog"], [class*="modal"]')) as HTMLElement[]
  const dialog = nodes.find((node) =>
    node.getBoundingClientRect().height > 200 && /职位关键词/.test(node.innerText)) ?? null
  if (!dialog) return { status: 'failed', reason: 'keyword_dialog_absent' }
  const selected = Array.from(dialog.querySelectorAll('li.s-checkbutton-drilldown-multi-limit__item'))
    .filter((item) => item.className.includes('--selected')) as HTMLElement[]
  if (selected.length === 0) return { status: 'ok', detail: 'empty' }
  selected[0].click()
  return { status: 'ok', detail: 'cleared' }
}

function mainPickZhilianKeyword(word: string): MainStep {
  const nodes = Array.from(document.querySelectorAll('[class*="dialog"], [class*="modal"]')) as HTMLElement[]
  const dialog = nodes.find((node) =>
    node.getBoundingClientRect().height > 200 && /职位关键词/.test(node.innerText)) ?? null
  if (!dialog) return { status: 'failed', reason: 'keyword_dialog_absent' }
  const items = Array.from(dialog.querySelectorAll('li.s-checkbutton-drilldown-multi-limit__item')) as HTMLElement[]
  const hit = items.filter((item) => (item.textContent ?? '').trim() === word)
  if (hit.length === 0) return { status: 'failed', reason: 'keyword_absent' }
  if (hit[0].className.includes('--selected')) return { status: 'ok', detail: 'already' }
  hit[0].click()
  return { status: 'ok', detail: 'picked' }
}

// 未命中词库的词一律进最后一个分组「您还有哪些招聘要求？」:它没有预设词条,
// 语义上就是兜底组。分组顺序由平台给出,这里只取最后一个自定义入口。
//
// 点开与填写必须分两次注入:输入框是点开后异步渲染的,同一次同步执行里紧接着
// 找它必然找不到。合成一步会让每轮的第一个自定义词稳定落空。
function mainClickZhilianCustomEntry(): MainStep {
  const nodes = Array.from(document.querySelectorAll('[class*="dialog"], [class*="modal"]')) as HTMLElement[]
  const dialog = nodes.find((node) =>
    node.getBoundingClientRect().height > 200 && /职位关键词/.test(node.innerText)) ?? null
  if (!dialog) return { status: 'failed', reason: 'keyword_dialog_absent' }
  const visible = Array.from(dialog.querySelectorAll('input.s-input__inner'))
    .filter((node) => node.getBoundingClientRect().height > 0)
  if (visible.length > 0) return { status: 'ok', detail: 'ready' }
  // 只认可见入口:分组多时平台会把靠后的分组折叠起来,折叠区里的入口点了没反应,
  // 会让整个自定义分支静默卡住。可见的最后一个即兜底组「您还有哪些招聘要求？」。
  const adds = (Array.from(dialog.querySelectorAll('li.s-checkbutton-drilldown-multi-limit__item__add')) as HTMLElement[])
    .filter((node) => node.getBoundingClientRect().height > 0)
  if (adds.length === 0) return { status: 'failed', reason: 'keyword_custom_entry_absent' }
  adds[adds.length - 1].click()
  return { status: 'ok', detail: 'clicked' }
}

// 只探测,不点击。等待必须与点击分开:把点击放进轮询体里会每轮再点一次,
// 反复开关反而永远等不到稳定的输入框。
function mainProbeZhilianCustomInput(): MainStep {
  const nodes = Array.from(document.querySelectorAll('[class*="dialog"], [class*="modal"]')) as HTMLElement[]
  const dialog = nodes.find((node) =>
    node.getBoundingClientRect().height > 200 && /职位关键词/.test(node.innerText)) ?? null
  if (!dialog) return { status: 'failed', reason: 'keyword_dialog_absent' }
  const visible = Array.from(dialog.querySelectorAll('input.s-input__inner'))
    .filter((node) => node.getBoundingClientRect().height > 0)
  if (visible.length === 0) return { status: 'failed', reason: 'keyword_custom_input_pending' }
  return { status: 'ok', detail: 'ready' }
}

function mainSubmitZhilianCustomKeyword(word: string): MainStep {
  const nodes = Array.from(document.querySelectorAll('[class*="dialog"], [class*="modal"]')) as HTMLElement[]
  const dialog = nodes.find((node) =>
    node.getBoundingClientRect().height > 200 && /职位关键词/.test(node.innerText)) ?? null
  if (!dialog) return { status: 'failed', reason: 'keyword_dialog_absent' }
  const input = Array.from(dialog.querySelectorAll('input.s-input__inner'))
    .find((node) => node.getBoundingClientRect().height > 0) as HTMLInputElement | undefined
  if (!input) return { status: 'failed', reason: 'keyword_custom_input_absent' }
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
  if (!setter) return { status: 'failed', reason: 'keyword_custom_setter_absent' }
  setter.call(input, word)
  input.dispatchEvent(new Event('input', { bubbles: true }))
  const confirm = Array.from(dialog.querySelectorAll('button, .s-button, [class*="btn"]'))
    .filter((node) => (node as HTMLElement).innerText.trim() === '添加' &&
      node.getBoundingClientRect().height > 0) as HTMLElement[]
  if (confirm.length === 0) return { status: 'failed', reason: 'keyword_custom_add_absent' }
  confirm[0].click()
  return { status: 'ok' }
}

// 自定义输入框添加成功后不会自动收起,残留的打开态会让平台拒绝「确定」并提示
// "请先完成添加"。这里点它自己的取消把它收掉——必须与「添加」同父才是自定义区
// 的取消,弹层底部还有一个同名「取消」，点错会丢弃全部已选。
function mainDismissZhilianCustomInput(): MainStep {
  const dialogs = Array.from(document.querySelectorAll('[class*="dialog"], [class*="modal"]')) as HTMLElement[]
  const dialog = dialogs.find((node) =>
    node.getBoundingClientRect().height > 200 && /职位关键词/.test(node.innerText)) ?? null
  if (!dialog) return { status: 'failed', reason: 'keyword_dialog_absent' }
  const visible = Array.from(dialog.querySelectorAll('input.s-input__inner'))
    .filter((node) => node.getBoundingClientRect().height > 0)
  if (visible.length === 0) return { status: 'ok', detail: 'none' }
  const buttons = Array.from(dialog.querySelectorAll('button, .s-button, [class*="btn"]')) as HTMLElement[]
  const add = buttons.find((button) =>
    button.innerText.trim() === '添加' && button.getBoundingClientRect().height > 0)
  if (!add) return { status: 'failed', reason: 'keyword_custom_add_absent' }
  const cancel = buttons.find((button) =>
    button.innerText.trim() === '取消' && button.parentElement === add.parentElement)
  if (!cancel) return { status: 'failed', reason: 'keyword_custom_cancel_absent' }
  cancel.click()
  return { status: 'ok', detail: 'dismissed' }
}

function mainConfirmZhilianKeywords(): MainStep {
  const nodes = Array.from(document.querySelectorAll('[class*="dialog"], [class*="modal"]')) as HTMLElement[]
  const dialog = nodes.find((node) =>
    node.getBoundingClientRect().height > 200 && /职位关键词/.test(node.innerText)) ?? null
  if (!dialog) return { status: 'failed', reason: 'keyword_dialog_absent' }
  const confirm = Array.from(dialog.querySelectorAll('.s-button'))
    .find((button) => /^确定/.test((button as HTMLElement).innerText.trim())) as HTMLElement | undefined
  if (!confirm) return { status: 'failed', reason: 'keyword_confirm_absent' }
  confirm.click()
  return { status: 'ok' }
}

function mainReadZhilianKeywordTags(): MainStep {
  const items = Array.from(document.querySelectorAll('.km-form-item'))
  const item = items.find((node) => {
    const label = node.querySelector(':scope > label, :scope > [class*="label"]') as HTMLElement | null
    return label?.innerText.trim() === '职位关键词'
  })
  if (!item) return { status: 'failed', reason: 'keyword_entry_absent' }
  const dialogs = Array.from(document.querySelectorAll('[class*="dialog"], [class*="modal"]')) as HTMLElement[]
  const stillOpen = dialogs.some((node) =>
    node.getBoundingClientRect().height > 200 && /职位关键词/.test(node.innerText))
  if (stillOpen) return { status: 'failed', reason: 'keyword_dialog_open' }
  const text = (item as HTMLElement).innerText.replace(/\s+/g, ' ').replace('职位关键词', '').trim()
  return { status: 'ok', detail: text.slice(0, 512) }
}

// 试填过程中累积的失败现场。它只用于人读诊断:试填链路长、每步都依赖平台的
// 异步行为,没有这份快照就只能靠反复重跑猜卡在哪。
interface DraftProgress {
  step: string
  descriptionLength?: number
  autoJobClass?: string
  keywordTotal?: number
  keywordIndex?: number
  keyword?: string
  keywordRoute?: string
  matched: string[]
  custom: string[]
  dropped: string[]
  sectionTitles: string[]
  availableSample: string[]
  customInputVisible?: boolean
}

function newDraftProgress(): DraftProgress {
  return {
    step: 'init', matched: [], custom: [], dropped: [],
    sectionTitles: [], availableSample: [],
  }
}

function snapshotProgress(progress: DraftProgress, reason: string): Record<string, unknown> {
  return {
    reason,
    step: progress.step,
    descriptionLength: progress.descriptionLength ?? null,
    autoJobClass: progress.autoJobClass ?? null,
    keyword: progress.keyword ?? null,
    keywordRoute: progress.keywordRoute ?? null,
    keywordAt: progress.keywordIndex === undefined
      ? null
      : `${progress.keywordIndex + 1}/${progress.keywordTotal ?? 0}`,
    matched: progress.matched,
    custom: progress.custom,
    dropped: progress.dropped,
    sectionTitles: progress.sectionTitles,
    // 当次实际词库的样本:关键词能否命中全看它,而它随职位类别变化。
    availableSample: progress.availableSample.slice(0, 40),
    customInputVisible: progress.customInputVisible ?? null,
  }
}

function throwPrepareDraftFailure(reason: string, progress?: DraftProgress): never {
  const diagnostics = progress ? snapshotProgress(progress, reason) : undefined
  if (reason === 'job_class_pending') {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED', '平台未在期限内按职位描述判定职位类别', 'manualOnly',
      undefined, 'none', diagnostics,
    )
  }
  if (reason.endsWith('_absent') || reason.endsWith('_unresolved')) {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED', `发布表单元素不可解析: ${reason}`, 'manualOnly',
      undefined, 'none', diagnostics,
    )
  }
  throw new ZhilianPlatformError(
    'GUARD_FAILED', `发布表单填充未通过: ${reason}`, 'manualOnly',
    undefined, 'none', diagnostics,
  )
}

// 注意:func 会被序列化后注入 MAIN world,**闭包变量到不了那边**。
// 参数一律经 args 数组传递,绝不能写成 () => mainXxx(param) ——那样 param
// 在页面里是未定义的,executeScript 会静默返回 undefined。
async function runStep<A extends unknown[]>(
  tabId: number,
  func: (...args: A) => MainStep,
  args: A,
  label: string,
  progress?: DraftProgress,
): Promise<string> {
  if (progress) progress.step = label
  const result = await runMain(tabId, func, args)
  if (!validMainStep(result)) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', `${label}结果结构不符合预期`, 'manualOnly',
      undefined, 'none', progress ? snapshotProgress(progress, 'main_step_shape') : undefined)
  }
  if (result.status === 'failed') throwPrepareDraftFailure(result.reason, progress)
  return result.detail ?? ''
}

// accept 在扩展侧执行(不序列化),用来表达"回读值必须等于目标"这类判据。
async function pollStep<A extends unknown[]>(
  tabId: number,
  func: (...args: A) => MainStep,
  args: A,
  ctx: PrimitiveContext,
  label: string,
  accept?: (detail: string) => boolean,
  attempts = 40,
  progress?: DraftProgress,
): Promise<string> {
  if (progress) progress.step = label
  let lastReason = 'unknown'
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    ctx.checkpoint()
    const result = await runMain(tabId, func, args)
    if (!validMainStep(result)) {
      throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', `${label}结果结构不符合预期`, 'manualOnly',
        undefined, 'none', progress ? snapshotProgress(progress, 'main_step_shape') : undefined)
    }
    if (result.status === 'ok') {
      const detail = result.detail ?? ''
      if (!accept || accept(detail)) return detail
      lastReason = `${label}_not_applied`
    } else {
      lastReason = result.reason
    }
    await new Promise<void>((resolve) => setTimeout(resolve, 250))
  }
  throwPrepareDraftFailure(lastReason, progress)
}

// 相邻平台可见交互至少间隔一秒并带随机抖动。
async function pace(): Promise<void> {
  await new Promise<void>((resolve) => {
    setTimeout(resolve, 1_000 + Math.floor(Math.random() * 501))
  })
}

async function pickZhilianSelect(
  tabId: number,
  ctx: PrimitiveContext,
  placeholder: string,
  wanted: string,
  progress?: DraftProgress,
): Promise<string> {
  // 上一次操作可能留着面板;不先收口,这次点击会把它 toggle 关掉。
  await pollStep(tabId, mainCloseZhilianPanels, [], ctx, '关闭下拉面板',
    (detail) => detail === 'closed', 20, progress)
  await pace()
  const anchor = Number(await runStep(tabId, mainOpenZhilianSelect, [placeholder], '展开下拉', progress))
  await pollStep(tabId, mainPickZhilianSelectOption, [anchor, wanted], ctx, `选择${placeholder}`,
    undefined, 40, progress)
  return pollStep(tabId, mainReadZhilianSelectValue, [placeholder], ctx, `回读${placeholder}`,
    (detail) => detail === wanted, 40, progress)
}

async function applyZhilianKeywords(
  tabId: number,
  ctx: PrimitiveContext,
  keywords: string[],
  progress: DraftProgress,
): Promise<{ matched: string[]; custom: string[]; dropped: string[]; sectionTitles: string[] }> {
  progress.keywordTotal = keywords.length
  await runStep(tabId, mainOpenZhilianKeywords, [], '打开关键词弹层', progress)
  // 弹层内容异步加载:分组和词库要等平台按当前职位类别返回后才渲染。
  let snapshot = await pollKeywordSnapshot(tabId, ctx, progress)

  // 先整体清空平台预选:预选一次有一次无,不清就算不准分组配额。
  for (let guard = 0; guard < 24 && snapshot.selected.length > 0; guard += 1) {
    await runStep(tabId, mainClearOneZhilianKeyword, [], '清空关键词预选', progress)
    await new Promise<void>((resolve) => setTimeout(resolve, 250))
    snapshot = await pollKeywordSnapshot(tabId, ctx, progress)
  }
  if (snapshot.selected.length > 0) {
    throw new ZhilianPlatformError('GUARD_FAILED', '关键词预选未能清空', 'manualOnly',
      undefined, 'none', snapshotProgress(progress, 'keyword_preselect_stuck'))
  }
  const sectionTitles = snapshot.sectionTitles.slice(0, 24)

  const matched: string[] = []
  const custom: string[] = []
  const dropped: string[] = []
  progress.matched = matched
  progress.custom = custom
  progress.dropped = dropped
  for (const [index, keyword] of keywords.entries()) {
    ctx.checkpoint()
    progress.keywordIndex = index
    progress.keyword = keyword
    await new Promise<void>((resolve) => setTimeout(resolve, 300))
    const available = new Set(snapshot.available)
    if (available.has(keyword)) {
      progress.keywordRoute = 'pick'
      const result = await runMain(tabId, mainPickZhilianKeyword, [keyword])
      if (!validMainStep(result)) {
        throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '关键词点选结果结构不符合预期', 'manualOnly',
          undefined, 'none', snapshotProgress(progress, 'keyword_pick_shape'))
      }
      snapshot = await pollKeywordSnapshot(tabId, ctx, progress)
      // 分组配额占满时平台会拒绝这次点选;按裁决丢弃并如实记录，不改投自定义
      // ——改投会把它落到另一个语义分组里，超出运营本意。
      if (result.status === 'ok' && snapshot.selected.includes(keyword)) matched.push(keyword)
      else dropped.push(keyword)
      continue
    }
    progress.keywordRoute = 'custom'
    // 先点开(只点一次),再等输入框渲染出来,最后填值提交。
    await runStep(tabId, mainClickZhilianCustomEntry, [], '打开自定义输入框', progress)
    // 分组配额占满后平台不再给出输入框。这不是故障,是"装不下了":按裁决记
    // dropped 继续下一个词,不能整体失败——否则配额一满整张表就填不成。
    progress.step = '等待自定义输入框'
    let inputReady = false
    for (let attempt = 0; attempt < 20 && !inputReady; attempt += 1) {
      ctx.checkpoint()
      const probed = await runMain(tabId, mainProbeZhilianCustomInput, [])
      if (validMainStep(probed) && probed.status === 'ok') inputReady = true
      else await new Promise<void>((resolve) => setTimeout(resolve, 250))
    }
    progress.customInputVisible = inputReady
    if (!inputReady) {
      dropped.push(keyword)
      continue
    }
    const added = await runMain(tabId, mainSubmitZhilianCustomKeyword, [keyword])
    if (!validMainStep(added)) {
      throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '关键词自定义结果结构不符合预期', 'manualOnly',
        undefined, 'none', snapshotProgress(progress, 'keyword_custom_shape'))
    }
    // 入列是异步的:必须等词真的出现在已选里再去收输入框,否则那次"收起"
    // 会把还没提交完的添加一起撤销——三个词会全部落空。
    let entered = false
    for (let attempt = 0; attempt < 20 && !entered; attempt += 1) {
      ctx.checkpoint()
      await new Promise<void>((resolve) => setTimeout(resolve, 250))
      const probed = await runMain(tabId, mainSnapshotZhilianKeywords, [])
      if (validKeywordSnapshot(probed) && probed.status === 'ok' && probed.selected.includes(keyword)) {
        entered = true
      }
    }
    // 无论成败都要收起:残留的打开态输入框会让「确定」被平台拒绝。
    await pollStep(tabId, mainDismissZhilianCustomInput, [], ctx, '收起自定义输入框',
      (detail) => detail === 'none', 12, progress)
    snapshot = await pollKeywordSnapshot(tabId, ctx, progress)
    if (entered && snapshot.selected.includes(keyword)) custom.push(keyword)
    else dropped.push(keyword)
  }

  progress.keywordIndex = undefined
  progress.keyword = undefined
  progress.keywordRoute = undefined
  await pace()
  // 保险再收一次:任何一处残留的打开态输入框都会让「确定」被拒。
  await pollStep(tabId, mainDismissZhilianCustomInput, [], ctx, '确认前收起自定义输入框',
    (detail) => detail === 'none', 12, progress)
  // 关键词是必填项:一个都没选上时「确定」必然被平台拒绝,与其在回读那步
  // 报一个含混的"弹层没关",不如在这里直说是关键词一个都没填进去。
  if (matched.length === 0 && custom.length === 0) {
    throw new ZhilianPlatformError(
      'GUARD_FAILED', '关键词一个都未能填入,发布表单的必填项无法满足', 'manualOnly',
      undefined, 'none', snapshotProgress(progress, 'keyword_all_missing'),
    )
  }
  await runStep(tabId, mainConfirmZhilianKeywords, [], '确认关键词', progress)
  const tags = await pollStep(tabId, mainReadZhilianKeywordTags, [], ctx, '回读关键词标签',
    undefined, 40, progress)
  for (const keyword of [...matched, ...custom]) {
    if (!tags.includes(keyword)) {
      throw new ZhilianPlatformError('GUARD_FAILED', '关键词确认后未在表单上回读到', 'manualOnly',
        undefined, 'none', snapshotProgress(progress, 'keyword_tag_missing'))
    }
  }
  return { matched, custom, dropped, sectionTitles }
}

async function pollKeywordSnapshot(
  tabId: number,
  ctx: PrimitiveContext,
  progress?: DraftProgress,
): Promise<{ selected: string[]; sectionTitles: string[]; available: string[]; confirmLabel: string }> {
  for (let attempt = 0; attempt < 40; attempt += 1) {
    ctx.checkpoint()
    const result = await runMain(tabId, mainSnapshotZhilianKeywords, [])
    if (!validKeywordSnapshot(result)) {
      throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '关键词弹层结构不符合预期', 'manualOnly',
        undefined, 'none', progress ? snapshotProgress(progress, 'keyword_snapshot_shape') : undefined)
    }
    if (result.status === 'ok') {
      // 每次快照都刷新诊断用的词库与分组:失败时最想知道的就是"当时到底有哪些
      // 分组、有哪些可选词条"，它随职位类别变化，事后无从复原。
      if (progress) {
        progress.sectionTitles = result.sectionTitles
        progress.availableSample = result.available
      }
      if (result.sectionTitles.length > 0) {
        return {
          selected: result.selected, sectionTitles: result.sectionTitles,
          available: result.available, confirmLabel: result.confirmLabel,
        }
      }
    }
    await new Promise<void>((resolve) => setTimeout(resolve, 250))
  }
  throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '关键词弹层未在期限内加载出分组', 'manualOnly',
    undefined, 'none', progress ? snapshotProgress(progress, 'keyword_sections_pending') : undefined)
}

// 填完整张发布表单并逐字段回读。试填与真发共用同一段填充逻辑——两者只在
// 收尾不同(试填离开表单,真发点一次「发布」),填充过程必须字面同一份代码,
// 否则"试填通过"就不能证明"真发也填得进去"。
//
// 本函数返回时表单已填好且仍在发布页;调用方负责收尾。
async function fillZhilianJobForm(
  args: JobPrepareDraftArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<{ tabId: number; progress: DraftProgress; data: JobPrepareDraftData }> {
  ctx.checkpoint()
  const progress = newDraftProgress()
  // 每次都从干净的发布页开始:表单只在页面内存里,残留状态会污染这次回读。
  const tab = await ensureZhilianJobPublishTab(ctx, expectedPrincipalFingerprint)
  const tabId = tab.id
  if (tabId === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
  await ctx.progress('核对智联发布页与登录身份', 10)

  await runStep(tabId, mainPickZhilianEmployment, [args.employmentType], '选择工作性质', progress)
  const employment = await pollStep(tabId, mainReadZhilianEmployment, [], ctx, '回读工作性质',
    (detail) => detail === args.employmentType, 40, progress)

  await pace()
  await runStep(tabId, mainFillZhilianJobName, [args.jobName], '填入职位名称', progress)

  await pace()
  const lines = args.description.split('\n')
  const expectedHTML = await runStep(tabId, mainWriteZhilianDescription, [lines], '写入职位描述', progress)
  // 富文本到隐藏 textarea 是异步同步的;一致即是这一步的正证。
  const descriptionLength = Number(await pollStep(
    tabId, mainReadZhilianDescriptionSync, [expectedHTML], ctx, '确认职位描述同步',
    undefined, 40, progress,
  ))
  progress.descriptionLength = descriptionLength
  await runStep(tabId, mainBlurZhilianDescription, [], '触发职位描述失焦', progress)
  await ctx.progress('等待平台按职位描述判定类别', 35)
  // 职位类别是平台自动生成的必填项,不是我们能填的字段;等它出现即可。
  // 类别由平台按描述判定,长描述明显更慢;60 轮(15 秒)在真机上不够,给到 30 秒。
  // 平台会因为内容不合规而拒绝判定类别(实测:描述里写了年龄要求就不判)。
  // 失败时把页面自己给出的提示原话带进诊断,否则只能看到一句"未在期限内判定"。
  let autoJobClass: string
  try {
    autoJobClass = await pollStep(tabId, mainReadZhilianAutoJobClass, [], ctx,
      '读取自动职位类别', undefined, 120, progress)
  } catch (error) {
    if (error instanceof ZhilianPlatformError) {
      const hints = await runMain(tabId, mainReadZhilianFormHints, [])
      const detail = validMainStep(hints) && hints.status === 'ok' ? hints.detail ?? '' : ''
      if (detail) {
        throw new ZhilianPlatformError(
          error.code, error.message, error.retryable, error.reason, error.sideEffect,
          { ...(error.diagnostics ?? {}), platformHints: detail },
        )
      }
    }
    throw error
  }
  progress.autoJobClass = autoJobClass

  const education = await pickZhilianSelect(tabId, ctx, '最低学历', args.education, progress)
  const experience = await pickZhilianSelect(tabId, ctx, '工作经验', args.experience, progress)
  await ctx.progress('填写学历与经验', 50)

  // 薪资三段是联动渲染:最高月薪与薪资月数在选定最低月薪之前根本不存在。
  const salaryMin = await pickZhilianSelect(tabId, ctx, '最低月薪', args.salaryMin, progress)
  const salaryMax = await pickZhilianSelect(tabId, ctx, '最高月薪', args.salaryMax, progress)
  await pollStep(tabId, mainCloseZhilianPanels, [], ctx, '关闭下拉面板',
    (detail) => detail === 'closed', 20, progress)
  await pace()
  let salaryMonths = await pollStep(tabId, mainReadZhilianSalaryMonths, [], ctx, '读取薪资月数',
    undefined, 40, progress)
  if (salaryMonths !== args.salaryMonths) {
    const anchor = Number(await runStep(tabId, mainOpenZhilianSalaryMonths, [], '展开薪资月数', progress))
    await pollStep(tabId, mainPickZhilianSelectOption, [anchor, args.salaryMonths], ctx, '选择薪资月数',
      undefined, 40, progress)
    salaryMonths = await pollStep(tabId, mainReadZhilianSalaryMonths, [], ctx, '回读薪资月数',
      (detail) => detail === args.salaryMonths, 40, progress)
  }
  await ctx.progress('填写薪资范围', 65)

  const keywords = await applyZhilianKeywords(tabId, ctx, args.keywords, progress)
  await ctx.progress('填写职位关键词', 85)

  await pace()
  const headcount = await runStep(tabId, mainFillZhilianHeadcount, [args.headcount], '填写招聘人数', progress)
  const workplace = await pollStep(tabId, mainReadZhilianWorkplace, [], ctx, '回读工作地址',
    undefined, 40, progress)

  const data: JobPrepareDraftData = {
    jobName: args.jobName,
    employmentType: employment,
    descriptionLength: Number.isFinite(descriptionLength) ? descriptionLength : 0,
    autoJobClass: autoJobClass || null,
    education, experience, salaryMin, salaryMax, salaryMonths,
    keywords: {
      matched: keywords.matched, custom: keywords.custom,
      dropped: keywords.dropped, sectionTitles: keywords.sectionTitles,
    },
    workplace,
    headcount: Number(headcount) || 0,
    discarded: false,
    observedAt: Date.now(),
  }

  return { tabId, progress, data }
}

// 点击「发布」之前的最后一道 guards:回读表单上几个决定性字段,确认页面里躺着的
// 就是本次意图。它问的是"世界是否处于脑预期的状态",不审计平台实现。
function mainReadZhilianPublishGuards(): MainStep {
  const name = document.querySelector('.publish-form__name input') as HTMLInputElement | null
  if (!name) return { status: 'failed', reason: 'name_input_absent' }
  const salaryMin = document.querySelector('input[placeholder="最低月薪"]') as HTMLInputElement | null
  const salaryMax = document.querySelector('input[placeholder="最高月薪"]') as HTMLInputElement | null
  const months = (Array.from(document.querySelectorAll('input')) as HTMLInputElement[])
    .filter((input) => /^\d+个月$/.test(input.value.trim()))
  const items = Array.from(document.querySelectorAll('.km-form-item'))
  const headcountItem = items.find((node) => {
    const label = node.querySelector(':scope > label, :scope > [class*="label"]') as HTMLElement | null
    return label?.innerText.trim() === '招聘人数'
  })
  const headcount = headcountItem?.querySelector('input[type="text"], input:not([type])') as HTMLInputElement | null
  if (!salaryMin || !salaryMax || months.length !== 1 || !headcount) {
    return { status: 'failed', reason: 'publish_guard_fields_absent' }
  }
  // 用  拼接,避免与字段内容里的任何可见分隔符冲突。
  return {
    status: 'ok',
    detail: [
      name.value.trim(), salaryMin.value.trim(), salaryMax.value.trim(),
      months[0].value.trim(), headcount.value.trim(),
    ].join(''),
  }
}

// 唯一一次点击。检查的是按钮自身的标准 DOM 语义(存在、可见、未禁用),
// 不去核对平台内部实现。
function mainClickZhilianPublish(): MainStep {
  const scope = document.querySelector('form.publish-form__inner') ?? document
  const buttons = (Array.from(scope.querySelectorAll('button')) as HTMLButtonElement[])
    .filter((button) => button.innerText.trim() === '发布' && button.getBoundingClientRect().height > 0)
  if (buttons.length !== 1) return { status: 'failed', reason: 'publish_button_unresolved' }
  if (buttons[0].disabled) return { status: 'failed', reason: 'publish_button_disabled' }
  if (buttons[0].type === 'reset') return { status: 'failed', reason: 'publish_button_wrong_type' }
  buttons[0].click()
  return { status: 'ok' }
}

// 手侧的同名归一化。必须与脑侧 jobconfig.normalizeJobName 保持同一套规则:
// 全半角括号与空白折叠。两侧口径都只放宽匹配、不放宽发布。
function normalizeZhilianPostingName(name: string): string {
  const folded = name
    .replace(/（/g, '(').replace(/）/g, ')')
    .replace(/［/g, '[').replace(/］/g, ']')
    .replace(/【/g, '[').replace(/】/g, ']')
    .replace(/　/g, ' ').replace(/／/g, '/')
    .replace(/，/g, ',').replace(/、/g, ',').replace(/：/g, ':')
  return folded.split(/\s+/).join('')
}

export async function publishZhilianJobDraft(
  args: JobPrepareDraftArgs,
  guards: JobPublishDraftGuards,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<JobPublishDraftData> {
  if (validatePrimitiveArgs(PrimitiveName.JobPublishDraft, 1, args).length !== 0) {
    throw new ZhilianPlatformError('GUARD_FAILED', '职位发布参数不符合当前契约', 'manualOnly')
  }
  if (guards?.expectAbsentOnPlatform !== true) {
    throw new ZhilianPlatformError('GUARD_FAILED', '职位发布缺少同名不存在的条件写', 'manualOnly')
  }
  const target = normalizeZhilianPostingName(args.jobName)
  if (!target) {
    throw new ZhilianPlatformError('GUARD_FAILED', '职位名归一化后为空', 'manualOnly')
  }

  // ── 点击之前:一切失败都是 sideEffect=none,未发布,可安全重试 ──────────
  //
  // 幂等闸:先确认平台上还没有同名职位。这一步必须在填表之前——读列表要离开
  // 发布页、会丢弃已填表单,所以复核无法挪到点击前的最后一刻。填表期间(数十秒)
  // 出现同名的 TOCTOU 由甲方 2026-07-30 知情裁决为不予防护。
  ctx.checkpoint()
  await ctx.progress('确认平台上尚无同名职位', 5)
  const before = await readZhilianPublishedJobs(ctx, expectedPrincipalFingerprint)
  if (before.postingNames.some((name) => normalizeZhilianPostingName(name) === target)) {
    throw new ZhilianPlatformError('GUARD_FAILED', '平台上已存在同名职位,不再发布', 'manualOnly')
  }

  const { tabId, progress, data: filled } = await fillZhilianJobForm(
    args, ctx, expectedPrincipalFingerprint,
  )
  await ctx.progress('表单已填好,复核后发布', 88)

  // 点击前最后一道 guards:页面里躺着的必须就是本次意图。
  const snapshot = (await runStep(
    tabId, mainReadZhilianPublishGuards, [], '发布前复核表单', progress,
  )).split('')
  const expected = [
    args.jobName, args.salaryMin, args.salaryMax, args.salaryMonths, String(args.headcount),
  ]
  if (snapshot.length !== expected.length ||
      snapshot.some((value, index) => value !== expected[index])) {
    throw new ZhilianPlatformError(
      'GUARD_FAILED', '发布前复核发现表单与本次意图不一致', 'manualOnly',
      undefined, 'none', snapshotProgress(progress, 'publish_guard_mismatch'),
    )
  }
  // 身份最后一次复核:此后就是不可逆动作。
  assertExpectedPrincipal(await probeTab(await chrome.tabs.get(tabId)), expectedPrincipalFingerprint)
  await ctx.beforeSideEffect()
  await pace()

  // ── 不可逆点击。此后一律 sideEffect=possible,原语内绝不重试、绝不第二次点击 ──
  const clicked = await runMain(tabId, mainClickZhilianPublish, [])
  if (!validMainStep(clicked)) {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED', '发布点击结果结构不符合预期', 'manualOnly', undefined, 'possible',
    )
  }
  if (clicked.status === 'failed') {
    // 按钮不可解析/被禁用都发生在点击之前,未产生副作用。
    throw new ZhilianPlatformError(
      'GUARD_FAILED', `发布按钮不可点击: ${clicked.reason}`, 'manualOnly',
      undefined, 'none', snapshotProgress(progress, clicked.reason),
    )
  }
  await ctx.progress('已点击发布,开始回读正证', 92)

  // 平台的即时反馈只作诊断:失败提示能让人一眼看出发不出去的原因,
  // 但它不参与判定——正证只认职位管理列表里出现同名职位。
  let platformFeedback: string | null = null
  try {
    const hints = await runMain(tabId, mainReadZhilianFormHints, [])
    if (validMainStep(hints) && hints.status === 'ok' && hints.detail) {
      platformFeedback = hints.detail.slice(0, 900)
    }
  } catch {
    // 读提示失败不影响正证判定,保持 null。
  }

  // 发后核验:回读列表,读到同名职位即成功。读不到只记未确认,
  // 绝不推断失败、绝不再次发布。
  let visible = false
  let rounds = 0
  for (let round = 0; round < 2 && !visible; round += 1) {
    ctx.checkpoint()
    await new Promise<void>((resolve) => setTimeout(resolve, round === 0 ? 3_000 : 6_000))
    rounds = round + 1
    try {
      const after = await readZhilianPublishedJobs(ctx, expectedPrincipalFingerprint)
      visible = after.postingNames.some((name) => normalizeZhilianPostingName(name) === target)
    } catch (error) {
      if (!(error instanceof ZhilianPlatformError)) throw error
      // 回读失败同样只是"未确认",不改变已经发生的副作用。
    }
  }

  const data: JobPublishDraftData = {
    jobName: args.jobName,
    autoJobClass: filled.autoJobClass,
    postingVisible: visible,
    verifyRounds: rounds,
    keywords: filled.keywords,
    platformFeedback,
    observedAt: Date.now(),
  }
  if (validatePrimitiveData(PrimitiveName.JobPublishDraft, 1, data).length !== 0) {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED', '发布结果不符合当前契约', 'manualOnly', undefined, 'possible',
    )
  }
  if (!visible) {
    // 副作用可能已经发生,但没有正证:交给脑的验证轮与 suspect,不在原语内重试。
    throw new ZhilianPlatformError(
      'POSTCONDITION_UNCONFIRMED', '发布后未在职位列表读到同名职位', 'manualOnly',
      undefined, 'possible',
      { ...snapshotProgress(progress, 'posting_not_visible'), platformFeedback, verifyRounds: rounds },
    )
  }
  await ctx.progress('职位发布已取得平台正证', 100)
  return data
}

export async function prepareZhilianJobDraft(
  args: JobPrepareDraftArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<JobPrepareDraftData> {
  if (validatePrimitiveArgs(PrimitiveName.JobPrepareDraft, 1, args).length !== 0) {
    throw new ZhilianPlatformError('GUARD_FAILED', '发布试填参数不符合当前契约', 'manualOnly')
  }
  const { tabId, data } = await fillZhilianJobForm(args, ctx, expectedPrincipalFingerprint)

  // 试填完成后必须离开:一个填满的发布表单只差一次点击,留在页面上等同于
  // 给人工误操作递刀。离开确认不了就整体失败,不返回"填好了"的成功结论。
  await discardZhilianJobDraft(tabId, ctx)
  data.discarded = true

  if (validatePrimitiveData(PrimitiveName.JobPrepareDraft, 1, data).length !== 0) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '发布试填回读不符合当前契约', 'manualOnly')
  }
  await ctx.progress('发布表单试填与回读完成', 100)
  return data
}

async function discardZhilianJobDraft(tabId: number, ctx: PrimitiveContext): Promise<void> {
  const commandNavigation = beginCommandNavigation(tabId, ctx.irreversibleNotAfterMs)
  try {
    await chrome.tabs.update(tabId, { url: ZHILIAN_JOB_LIST_URL })
  } catch (error) {
    commandNavigation.end()
    throw error
  }
  for (let attempt = 0; attempt < 40; attempt += 1) {
    ctx.checkpoint()
    const latest = await chrome.tabs.get(tabId)
    if (latest.status === 'complete' && !isZhilianJobPublishURL(latest.url)) return
    await new Promise<void>((resolve) => setTimeout(resolve, 250))
  }
  throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '试填后未能确认离开发布表单', 'manualOnly')
}

async function ensureZhilianJobPublishTab(
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<chrome.tabs.Tab> {
  // 只在"已经停在发布页"的标签里要求唯一。对全部智联标签要求唯一会在用户
  // 同时开着 IM 和推荐页时直接失败——那是常态，不是异常。
  const onPublish = (await chrome.tabs.query({ url: TAB_QUERY }))
    .filter((tab) => tab.id !== undefined && isZhilianJobPublishURL(tab.url))
  if (onPublish.length > 1) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '智联发布页标签无法唯一确定', 'manualOnly')
  }
  let tab = onPublish.length === 1 ? onPublish[0] : await canonicalZhilianTab()
  if (!tab || tab.id === undefined) {
    throw new ZhilianPlatformError(
      'CTX_NOT_READY', 'Chrome 中没有可复用的智联页面', 'afterRecovery', 'pageAbsent',
    )
  }
  assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
  const commandNavigation = beginCommandNavigation(tab.id, ctx.irreversibleNotAfterMs)
  try {
    // 即使当前已在发布页也重新导航一次:表单只活在页面内存里,
    // 上一次的残留会污染这次回读。
    tab = await chrome.tabs.update(tab.id, { url: ZHILIAN_JOB_PUBLISH_URL })
  } catch (error) {
    commandNavigation.end()
    throw error
  }
  const tabId = tab.id
  if (tabId === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  await new Promise<void>((resolve) => {
    setTimeout(resolve, 1_000 + Math.floor(Math.random() * 501))
  })
  for (let attempt = 0; attempt < 40; attempt += 1) {
    ctx.checkpoint()
    const latest = await chrome.tabs.get(tabId)
    if (latest.status === 'complete' && isZhilianJobPublishURL(latest.url)) {
      // 页面报 complete 时 SPA 往往还在替换文档，此刻注入会拿不到返回值。
      // 就绪探测允许失败，继续轮询即可；只有轮完仍不就绪才算真的没起来。
      try {
        const probed = await runMain(tabId, mainReadZhilianWorkplace, [])
        if (validMainStep(probed) && probed.status === 'ok') return latest
      } catch (error) {
        if (!(error instanceof ZhilianPlatformError)) throw error
      }
    }
    await new Promise<void>((resolve) => setTimeout(resolve, 250))
  }
  throw new ZhilianPlatformError(
    'CTX_NOT_READY', '智联发布页在期限内未就绪', 'afterRecovery', 'pageBroken',
  )
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
  cutoffMs: number | null,
): Promise<ZhilianListPage> {
  if (tab.id === undefined) throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  const advance = args.move === 'next'
  ctx.checkpoint()
  await ctx.progress(
    advance ? '向下移动并读取智联会话窗口' : '从顶部读取智联会话窗口',
    5,
  )
  let page: MainListDOMWindowResult
  try {
    page = await runMain(tab.id, mainReadListDOMWindow, [advance, !advance])
  } catch (error) {
    if (args.filter === 'unread' && asError(error).message.includes('dom_list_items_missing')) {
      throw new ZhilianPlatformError(
        'ELEMENT_UNRESOLVED',
        '未读筛选已开启，但当前页面没有可确认的非空列表或可信空态',
        'manualOnly',
      )
    }
    throw new ZhilianPlatformError(
      'CTX_LOST_DURING_EXEC',
      `智联会话列表页面读取失败：${asError(error).message}`,
    )
  }
  if (page.unstable) {
    throw new ZhilianPlatformError('USER_ACTIVE', 'DOM 虚拟列表未稳定，交由下一轮巡检重算')
  }
  if (advance && !page.moved && !page.atBottom) {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      'DOM 虚拟列表尚未到底，但本次向下移动没有产生新窗口',
    )
  }
  if (page.sessions.length > LIST_WINDOW_MAX_SESSIONS) {
    throw new ZhilianPlatformError('PAYLOAD_LIMIT', '当前 DOM 会话窗口超过 32 条上限')
  }

  let crossedCutoff = false
  const sessions = page.sessions.filter((item) => {
    if (cutoffMs !== null && item.lastActivityTs !== null && item.lastActivityTs < cutoffMs) {
      crossedCutoff = true
      return false
    }
    return true
  })
  const result: ZhilianListPage = {
    sessions,
    complete: page.atBottom || crossedCutoff,
  }
  if (jsonBytes(result) > RESULT_DATA_BUDGET) {
    throw new ZhilianPlatformError('PAYLOAD_LIMIT', '当前 DOM 会话窗口超过内联载荷上限')
  }

  assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
  await ctx.progress('智联会话列表页面读取完成', 100)
  return result
}

export async function readZhilianList(
  args: ZhilianListArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianListPage> {
  if (validatePrimitiveArgs(PrimitiveName.ChatReadList, 1, args).length !== 0) {
    throw new ZhilianPlatformError('GUARD_FAILED', '会话列表读取参数不符合当前契约', 'manualOnly')
  }
  const tab = await verifiedIMTab(expectedPrincipalFingerprint)
  if (tab.id === undefined) throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  const cutoffDays = args.filter === 'all'
    ? Math.min(30, Math.max(1, args.stopOlderThanDays ?? 8))
    : null

  ctx.checkpoint()
  let filterState = await runMain(tab.id, mainEnsureChatListFilter, [
    args.filter === 'unread',
    false,
  ])
  if (filterState.status === 'failed') {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      `智联聊天列表筛选状态无法回读：${filterState.reason}`,
      'manualOnly',
    )
  }
  if (filterState.status === 'needs_action') {
    if (args.move === 'next') {
      throw new ZhilianPlatformError(
        'ELEMENT_UNRESOLVED',
        'move=next 要求会话列表筛选保持不变，请用 move=reset 重新建立窗口',
      )
    }
    ctx.checkpoint()
    assertExpectedPrincipal(await probeTab(await chrome.tabs.get(tab.id)), expectedPrincipalFingerprint)
    await ctx.beforeSideEffect()
    filterState = await runMain(tab.id, mainEnsureChatListFilter, [
      args.filter === 'unread',
      true,
    ])
    if (filterState.status !== 'ready') {
      const reason = filterState.status === 'failed' ? filterState.reason : 'not_applied'
      throw new ZhilianPlatformError(
        'ELEMENT_UNRESOLVED',
        `智联聊天列表筛选无法覆盖并回读确认：${reason}`,
        'manualOnly',
      )
    }
    assertExpectedPrincipal(await probeTab(await chrome.tabs.get(tab.id)), expectedPrincipalFingerprint)
  }

  const cutoffMs = cutoffDays === null ? null : Date.now() - cutoffDays * 86_400_000
  return readZhilianListFromDOM(
    args,
    ctx,
    expectedPrincipalFingerprint,
    tab,
    cutoffMs,
  )
}

// 自包含 MAIN-world 历史读取。直接调用 imEngine.getHistoryMsgs；与 Vuex getTimeline 不同，
// 不调用 sendMsgReceipt/sendLastMsgRead，真实扩展验收仍会监测 unread 是否变化。
async function mainReadThreadPage(
  conversationRef: string,
  limit: number,
  cursor: { endTime: number; lastMsgId: string } | null,
  apiTimeoutMs = 10_000,
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
  diagnosticStage = 'resolve_session_initial_state'
  const selectedConversationRef = (() => {
    try { return new URL(location.href).searchParams.get('sessionId') ?? '' } catch { return '' }
  })()
  if (!session && selectedConversationRef === conversationRef) {
    session = readInitialSessions().find((item) => String(item.sessionId ?? '') === conversationRef)
  }
  if (!session) throw new Error('conversation_not_found')

  const target = String(session.peerPartnerId ?? '')
  if (!target) throw new Error('conversation_target_missing')
  let history: unknown = null
  let historyFailure: 'unavailable' | 'rejected' | 'shape' | null = null
  if (engine && typeof engine.getHistoryMsgs === 'function' && target) {
    diagnosticStage = 'read_history_api'
    const request: AnyRecord = { to: target, limit, asc: true }
    // 真机 2026-07-28:现网 session.scene 为 undefined,而 sessionType 不是
    // getHistoryMsgs 的合法 scene(合法枚举仅 p2p/team,来自平台参数校验的
    // 错误消息与真机对照实验)。传入非法值会让 API 主路径 100% 被拒并退化
    // 到受渲染时序影响的 DOM 兜底;不传 scene 时接口正常。因此只透传已验证
    // 的合法值,其余一律不传。
    const scene = session.scene ?? session.sessionType
    if (scene === 'p2p' || scene === 'team') request.scene = scene
    if (cursor) {
      request.endTime = cursor.endTime
      request.lastMsgId = cursor.lastMsgId
    }
    try {
      let timer: ReturnType<typeof setTimeout> | undefined
      try {
        history = await Promise.race([
          (engine.getHistoryMsgs as (arg: AnyRecord) => Promise<unknown[]>).call(engine, request),
          new Promise<never>((_resolve, reject) => {
            timer = setTimeout(() => reject(new Error('history_api_timeout')), apiTimeoutMs)
          }),
        ])
      } finally {
        if (timer !== undefined) clearTimeout(timer)
      }
      if (!Array.isArray(history)) {
        history = null
        historyFailure = 'shape'
      }
    } catch (error) {
      if (error instanceof Error && error.message === 'history_api_timeout') throw error
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
    // 只有页面明确声明 90 天可见边界，DOM 回退才有资格声称已到顶。
    // “以下是90天内的聊天消息”是真机当前文案；其余分支兼容已验证过的同义文案。
    // 边界条与消息都可能晚渲染,统一在复读收敛后再取最终值。
    const readNinetyDayBoundary = (): boolean => {
      const boundary = document.querySelector<HTMLElement>('.im-timeline-ending')
      const boundaryText = `${String(boundary?.textContent ?? '')} ${String(timeline.parentElement?.textContent ?? '')}`
      return /(?:以下是\s*90\s*天内(?:的)?聊天消息|(?:仅展示|只展示)(?:近)?\s*90\s*天(?:内)?(?:的)?(?:聊天)?消息|近\s*90\s*天(?:内)?(?:的)?(?:聊天)?消息)/u
        .test(boundaryText)
    }
    const readRows = (): unknown[] => {
      const vue = (timeline as HTMLElement & { __vue__?: AnyRecord }).__vue__
      const props = vue?._props as AnyRecord | undefined
      if (Array.isArray(props?.data) && props.data.length > 0) return [...props.data]
      // 生产页面不暴露 Vue2 __vue__；SSR 注入的 timelineMap 是同一路由的稳定消息证词。
      return readInitialTimeline()
    }
    let rawRows = readRows()
    if (rawRows.length === 0) {
      // 真机 2026-07-28:打开会话后"90 天"边界条先于消息约 150~300ms 渲染,
      // 立即读取会把仍在渲染的会话误判成"合法空"。0 条不立刻下结论:按
      // 条件轮询纪律每 150ms 复读,读出消息即继续,至多等 2 秒。
      diagnosticStage = 'read_history_dom_empty_settle'
      const emptyDeadline = performance.now() + 2_000
      while (rawRows.length === 0 && performance.now() < emptyDeadline) {
        await new Promise((resolve) => setTimeout(resolve, 150))
        rawRows = readRows()
      }
    }
    const hasExplicitNinetyDayBoundary = readNinetyDayBoundary()
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
  const unrecognizedTypeCodes = new Set<string>()
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
    const originTypeIsCandidate = details.originType === 2 ||
      (typeof details.originType === 'string' && details.originType.trim() === '2')
    const originTypeIsStaff = details.originType === 1 ||
      (typeof details.originType === 'string' && details.originType.trim() === '1')
    const customSuccess = rawType === 'custom' && typeof row.content === 'string' &&
      Object.keys(inner).length > 0 && clean(row.status).toLowerCase() === 'success'
    const isCandidateWechatRequest = customSuccess && customType === 105 &&
      from === target && originTypeIsCandidate
    const isStaffWechatRequest = customSuccess && customType === 105 &&
      from === staffID && originTypeIsStaff
    // 259 交换成功恒归属点同意的一方：我方发起(originType=1)由候选人点同意，
    // 259 归对方(in)；候选人发起(originType=2)由我方点同意，259 归我方(out)。
    // 两形态都必须双微信字段齐全。2026-07-28 生产页面双样本直读。
    const isWechatExchangeSucceeded = customSuccess && customType === 259 &&
      ((from === target && originTypeIsStaff) || (from === staffID && originTypeIsCandidate)) &&
      Boolean(clean(details.userWeChat)) && Boolean(clean(details.staffWeChat))
    const interviewStartsAt = toMillis(details.startTime)
    const interviewEndsAt = toMillis(details.endTime)
    // 2026-07-27 真机：355 卡负载以字符串枚举 interviewType="VIDEO"、
    // interviewPlatform="WECHAT_VIDEO" 表达微信视频；数字形态保留为既有容忍。
    // 其他取值（如 TENCENT）不猜映射。
    const interviewMethod = (details.interviewType === 2 || details.interviewType === 'VIDEO') &&
      (details.interviewPlatform === 4 || details.interviewPlatform === 'WECHAT_VIDEO')
      ? 'wechatVideo' as const
      : null
    const isStaffInterviewInvite = customSuccess && customType === 355 &&
      from === staffID && Boolean(clean(details.interviewId)) &&
      interviewStartsAt !== null && interviewEndsAt !== null &&
      Boolean(clean(details.interviewType)) && Boolean(clean(details.interviewPlatform)) &&
      Object.prototype.hasOwnProperty.call(details, 'state')
    const isCandidateInterviewAcceptedText = rawType === 'text' && from === target &&
      clean(row.text) === '我已接受贵司的面试邀请，将准时参加面试'
    const isCandidateOnlineResume = rawType === 'custom' && typeof row.content === 'string' &&
      typeof envelope.type === 'string' && envelope.type.trim() === '313' && customType === 313 &&
      Object.keys(inner).length > 0 && from === target &&
      clean(row.status).toLowerCase() === 'success' &&
      clean(inner.staffText) === '对方向您发送了在线简历'
    const normalizedAttachmentType =
      envelope.type === 177 || envelope.type === '177' ? 177 : null
    const isCandidateAttachmentResume = rawType === 'custom' &&
      normalizedAttachmentType === 177 &&
      from === target && clean(row.status).toLowerCase() === 'success'
    let direction: ZhilianThreadMessage['direction'] = from
      ? from === staffID ? 'out' : 'in'
      : 'system'
    let kind: ZhilianThreadMessage['kind'] = 'system'
    let text: string | null = null
    let cardType: ZhilianThreadMessage['cardType'] = null
    let state: ZhilianThreadMessage['cardState'] = null
    let interview: InterviewDetails | null = null
    let identity = ''

    if (isCandidateInterviewAcceptedText) {
      kind = 'card'
      cardType = 'interviewInvite'
      text = clean(row.text)
      state = 'accepted'
      identity = stableMessageIdentity(row.idServer)
    } else if (rawType === 'text') {
      if (!from) throw new Error('message_direction_unresolved')
      kind = 'text'
      text = clean(row.text)
    } else if (isCandidateWechatRequest) {
      kind = 'card'
      cardType = 'wechatExchange'
      text = clean(
        details.userContent ?? details.receiverText ?? details.detail,
      ) || '[交换微信请求]'
      state = 'pending'
      identity = clean(details.requestId ?? details.id ?? details.cardId)
    } else if (isStaffWechatRequest) {
      kind = 'card'
      cardType = 'wechatExchange'
      text = '[换微信请求]'
      state = 'pending'
      identity = stableMessageIdentity(row.idServer)
    } else if (isWechatExchangeSucceeded) {
      kind = 'card'
      cardType = 'wechatExchange'
      text = '[微信交换成功]'
      state = 'accepted'
      identity = stableMessageIdentity(row.idServer)
    } else if (isStaffInterviewInvite) {
      kind = 'card'
      cardType = 'interviewInvite'
      text = '[面试邀请]'
      state = 'unknown'
      identity = interviewMethod === 'wechatVideo' && interviewStartsAt !== null &&
        interviewEndsAt !== null && interviewEndsAt > interviewStartsAt
        ? [String(interviewStartsAt), String(interviewEndsAt), interviewMethod].join('\x1f')
        : [
            stableMessageIdentity(row.idServer),
            String(interviewStartsAt),
            String(interviewEndsAt),
            interviewMethod ?? 'unknown',
          ].join('\x1f')
      if (interviewMethod !== null && interviewStartsAt !== null && interviewEndsAt !== null &&
          interviewEndsAt > interviewStartsAt) {
        interview = {
          startsAt: interviewStartsAt,
          endsAt: interviewEndsAt,
          method: interviewMethod,
        }
      }
    } else if (isCandidateOnlineResume || isCandidateAttachmentResume) {
      kind = 'card'
      cardType = 'resumeAttachment'
      text = isCandidateOnlineResume ? clean(inner.staffText) : '您好，这是我的附件简历，请查收'
      state = 'unknown'
      identity = stableMessageIdentity(row.idServer)
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
      unrecognizedTypeCodes.add(Number.isFinite(customType) ? String(customType) : 'unknown')
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
    const cardProjection = cardType === 'wechatExchange' &&
      (isCandidateWechatRequest || isStaffWechatRequest || isWechatExchangeSucceeded)
      ? 'card\x1fwechatExchange'
      : cardType === 'interviewInvite' && isStaffInterviewInvite &&
          interviewMethod === 'wechatVideo' && interviewStartsAt !== null &&
          interviewEndsAt !== null && interviewEndsAt > interviewStartsAt
        ? `card\x1finterviewInvite\x1f${interviewStartsAt}\x1f${interviewEndsAt}\x1fwechatVideo`
        : `card\x1f${cardType ?? 'other'}\x1f${clean(identity || stableMessageID || text)}`
    const contentHash = kind === 'card'
      ? await digest(cardProjection)
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
      ...(interview ? { interview } : {}),
      tsApprox: toMillis(row.time),
    })
  }

  for (const typeCode of [...unrecognizedTypeCodes].sort()) {
    console.info('[RecruitHelper] zhilian_unrecognized_message_type', typeCode)
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
      'history_api_timeout',
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
// finder 只检查当前已加载窗口，完整 sessionId 唯一命中才返回 found；它不复位、
// 不滚动也不 click，数据库中的旧会话因此不能驱动手在长列表里全局搜人。
async function mainFindConversation(
  conversationRef: string,
  observeUnreadMarker = false,
): Promise<MainFindConversationResult> {
  type AnyRecord = Record<string, unknown>
  const w = window as unknown as AnyRecord
  const asRecord = (value: unknown): AnyRecord | null =>
    value && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const current = (): string => {
    try { return new URL(location.href).searchParams.get('sessionId') ?? '' } catch { return '' }
  }
  // 普通 finder 只需知道公开路由已经选中；openConversation 的后置观察还必须
  // 继续读取未读列表，不能因路由命中提前跳过“标记清除/行离开”的正证。
  if (!observeUnreadMarker && current() === conversationRef) return { status: 'found' }

  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const virtual = document.querySelector<HTMLElement>('.im-session-list .im-session-list__virtual')
  if (!virtual) return { status: 'failed', reason: 'list_surface_missing' }
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

  const rows = readWindow()
  if (rows === null) return { status: 'failed', reason: 'list_binding_unresolved' }
  if (rows.length === 0) return { status: 'failed', reason: 'list_items_missing' }
  const matches = rows.filter(({ ref }) => ref === conversationRef)
  if (matches.length > 1) return { status: 'failed', reason: 'target_binding_duplicated' }
  if (matches.length !== 1) return { status: 'failed', reason: 'target_not_found' }
  if (!observeUnreadMarker) return { status: 'found' }
  const unreadMarkers = Array.from(
    matches[0].node.querySelectorAll<HTMLElement>('.im-session-item__unread'),
  ).filter((node) => visible(node))
  const markerValues = unreadMarkers.map((node) => String(node.textContent ?? '').trim())
  const unreadMarkerCleared = markerValues.length === 0 ||
    (markerValues.length === 1 && markerValues[0] === '0')
  return { status: 'found', unreadMarkerCleared }
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
    contentWasString: boolean
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
          contentWasString: typeof row.content === 'string',
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
    const toMillis = (value: unknown): number | null => {
      const number = Number(value)
      if (!Number.isFinite(number) || number <= 0) return null
      return number < 1_000_000_000_000 ? Math.trunc(number * 1000) : Math.trunc(number)
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
      const originTypeIsCandidate = details.originType === 2 ||
        (typeof details.originType === 'string' && details.originType.trim() === '2')
      const originTypeIsStaff = details.originType === 1 ||
        (typeof details.originType === 'string' && details.originType.trim() === '1')
      const customSuccess = rawType === 'custom' && row.contentWasString &&
        Object.keys(inner).length > 0 && clean(row.status).toLowerCase() === 'success'
      const isCandidateWechatRequest = customSuccess && customType === 105 &&
        from === target && originTypeIsCandidate
      const isStaffWechatRequest = customSuccess && customType === 105 &&
        from === staffID && originTypeIsStaff
      // 259 交换成功恒归属点同意的一方：我方发起(originType=1)由候选人点同意，
      // 259 归对方(in)；候选人发起(originType=2)由我方点同意，259 归我方(out)。
      // 两形态都必须双微信字段齐全。2026-07-28 生产页面双样本直读。
      const isWechatExchangeSucceeded = customSuccess && customType === 259 &&
        ((from === target && originTypeIsStaff) || (from === staffID && originTypeIsCandidate)) &&
        Boolean(clean(details.userWeChat)) && Boolean(clean(details.staffWeChat))
      const interviewStartsAt = toMillis(details.startTime)
      const interviewEndsAt = toMillis(details.endTime)
      const interviewMethod = (details.interviewType === 2 || details.interviewType === 'VIDEO') &&
      (details.interviewPlatform === 4 || details.interviewPlatform === 'WECHAT_VIDEO')
        ? 'wechatVideo'
        : 'unknown'
      const isStaffInterviewInvite = customSuccess && customType === 355 &&
        from === staffID && Boolean(clean(details.interviewId)) &&
        interviewStartsAt !== null && interviewEndsAt !== null &&
        Boolean(clean(details.interviewType)) && Boolean(clean(details.interviewPlatform)) &&
        Object.prototype.hasOwnProperty.call(details, 'state')
      const isCandidateInterviewAcceptedText = rawType === 'text' && from === target &&
        clean(row.text) === '我已接受贵司的面试邀请，将准时参加面试'
      const isCandidateOnlineResume = rawType === 'custom' && row.contentWasString &&
        typeof envelope.type === 'string' && envelope.type.trim() === '313' && customType === 313 &&
        Object.keys(inner).length > 0 && from === target &&
        clean(row.status).toLowerCase() === 'success' &&
        clean(inner.staffText) === '对方向您发送了在线简历'
      const normalizedAttachmentType =
        envelope.type === 177 || envelope.type === '177' ? 177 : null
      const isCandidateAttachmentResume = rawType === 'custom' &&
        normalizedAttachmentType === 177 &&
        from === target && clean(row.status).toLowerCase() === 'success'
      let direction: ZhilianMessageAnchor['direction'] = from
        ? from === staffID ? 'out' : 'in'
        : 'system'
      let kind: 'text' | 'card' | 'system' = 'system'
      let text: string | null = null
      let cardType: 'interviewInvite' | 'wechatExchange' | 'resumeAttachment' | null = null
      let identity = ''
      if (isCandidateInterviewAcceptedText) {
        kind = 'card'
        cardType = 'interviewInvite'
        text = clean(row.text)
        identity = row.idServer
      } else if (rawType === 'text') {
        if (!from) return null
        kind = 'text'
        text = clean(row.text)
      } else if (isCandidateWechatRequest) {
        kind = 'card'
        cardType = 'wechatExchange'
        text = clean(
          details.userContent ?? details.receiverText ?? details.detail,
        ) || '[交换微信请求]'
        identity = clean(details.requestId ?? details.id ?? details.cardId)
      } else if (isStaffWechatRequest) {
        kind = 'card'
        cardType = 'wechatExchange'
        text = '[换微信请求]'
        identity = row.idServer
      } else if (isWechatExchangeSucceeded) {
        kind = 'card'
        cardType = 'wechatExchange'
        text = '[微信交换成功]'
        identity = row.idServer
      } else if (isStaffInterviewInvite) {
        kind = 'card'
        cardType = 'interviewInvite'
        text = '[面试邀请]'
        identity = interviewMethod === 'wechatVideo' && interviewStartsAt !== null &&
          interviewEndsAt !== null && interviewEndsAt > interviewStartsAt
          ? [String(interviewStartsAt), String(interviewEndsAt), interviewMethod].join('\x1f')
          : [
              row.idServer,
              String(interviewStartsAt),
              String(interviewEndsAt),
              interviewMethod,
            ].join('\x1f')
      } else if (isCandidateOnlineResume || isCandidateAttachmentResume) {
        kind = 'card'
        cardType = 'resumeAttachment'
        text = isCandidateOnlineResume ? clean(inner.staffText) : '您好，这是我的附件简历，请查收'
        identity = row.idServer
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
      const cardProjection = cardType === 'wechatExchange' &&
        (isCandidateWechatRequest || isStaffWechatRequest || isWechatExchangeSucceeded)
        ? 'card\x1fwechatExchange'
        : cardType === 'interviewInvite' && isStaffInterviewInvite &&
            interviewMethod === 'wechatVideo' && interviewStartsAt !== null &&
            interviewEndsAt !== null && interviewEndsAt > interviewStartsAt
          ? `card\x1finterviewInvite\x1f${interviewStartsAt}\x1f${interviewEndsAt}\x1fwechatVideo`
          : `card\x1f${cardType ?? 'other'}\x1f${clean(identity || row.idServer || text)}`
      const contentHash = kind === 'card'
        ? await digest(cardProjection)
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
    contentWasString: boolean
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
        contentWasString: typeof row.content === 'string',
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
  const toMillis = (value: unknown): number | null => {
    const number = Number(value)
    if (!Number.isFinite(number) || number <= 0) return null
    return number < 1_000_000_000_000 ? Math.trunc(number * 1000) : Math.trunc(number)
  }
  const runtimeStaffID = (): string => {
    const runtimeSession = asRecord(w.$session)
    const runtimeStaff = asRecord(runtimeSession?.staff)
    const initialSession = asRecord(asRecord(initial.session)?.session)
    return clean(runtimeStaff?.staffId) || clean(asRecord(initialSession?.staff)?.staffId)
  }
  const actionAnchorFor = (
    row: ActionSnapshotRow,
    staffID: string,
    target: string,
  ): ZhilianMessageAnchor | null => {
    const envelope = parseObject(row.content)
    const inner = parseObject(envelope.content)
    const details = Object.keys(inner).length > 0 ? inner : envelope
    const rawType = row.type
    const customType = Number(
      typeof rawType === 'number' || /^\d+$/u.test(String(rawType)) ? rawType : envelope.type,
    )
    const from = clean(row.from)
    const originTypeIsCandidate = details.originType === 2 ||
      (typeof details.originType === 'string' && details.originType.trim() === '2')
    const originTypeIsStaff = details.originType === 1 ||
      (typeof details.originType === 'string' && details.originType.trim() === '1')
    const customSuccess = rawType === 'custom' && row.contentWasString &&
      Object.keys(inner).length > 0 && clean(row.status).toLowerCase() === 'success'
    const isCandidateWechatRequest = customSuccess && customType === 105 &&
      from === target && originTypeIsCandidate
    const isStaffWechatRequest = customSuccess && customType === 105 &&
      from === staffID && originTypeIsStaff
    // 259 交换成功恒归属点同意的一方：我方发起(originType=1)由候选人点同意，
    // 259 归对方(in)；候选人发起(originType=2)由我方点同意，259 归我方(out)。
    // 两形态都必须双微信字段齐全。2026-07-28 生产页面双样本直读。
    const isWechatExchangeSucceeded = customSuccess && customType === 259 &&
      ((from === target && originTypeIsStaff) || (from === staffID && originTypeIsCandidate)) &&
      Boolean(clean(details.userWeChat)) && Boolean(clean(details.staffWeChat))
    const interviewStartsAt = toMillis(details.startTime)
    const interviewEndsAt = toMillis(details.endTime)
    const interviewMethod = (details.interviewType === 2 || details.interviewType === 'VIDEO') &&
      (details.interviewPlatform === 4 || details.interviewPlatform === 'WECHAT_VIDEO')
      ? 'wechatVideo'
      : 'unknown'
    const isStaffInterviewInvite = customSuccess && customType === 355 &&
      from === staffID && Boolean(clean(details.interviewId)) &&
      interviewStartsAt !== null && interviewEndsAt !== null &&
      Boolean(clean(details.interviewType)) && Boolean(clean(details.interviewPlatform)) &&
      Object.prototype.hasOwnProperty.call(details, 'state')
    const isCandidateInterviewAcceptedText = rawType === 'text' && from === target &&
      clean(row.text) === '我已接受贵司的面试邀请，将准时参加面试'
    const isCandidateOnlineResume = rawType === 'custom' && row.contentWasString &&
      typeof envelope.type === 'string' && envelope.type.trim() === '313' && customType === 313 &&
      Object.keys(inner).length > 0 && from === target &&
      clean(row.status).toLowerCase() === 'success' &&
      clean(inner.staffText) === '对方向您发送了在线简历'
    const normalizedAttachmentType =
      envelope.type === 177 || envelope.type === '177' ? 177 : null
    const isCandidateAttachmentResume = rawType === 'custom' &&
      normalizedAttachmentType === 177 &&
      from === target && clean(row.status).toLowerCase() === 'success'
    let direction: ZhilianMessageAnchor['direction'] = from
      ? from === staffID ? 'out' : 'in'
      : 'system'
    let kind: 'text' | 'card' | 'system' = 'system'
    let normalizedText = ''
    let cardType: 'interviewInvite' | 'wechatExchange' | 'resumeAttachment' | null = null
    let identity = ''
    if (isCandidateInterviewAcceptedText) {
      kind = 'card'
      cardType = 'interviewInvite'
      normalizedText = clean(row.text)
      identity = row.idServer
    } else if (rawType === 'text') {
      if (!from) return null
      kind = 'text'
      normalizedText = clean(row.text)
    } else if (isCandidateWechatRequest) {
      kind = 'card'
      cardType = 'wechatExchange'
      normalizedText = clean(
        details.userContent ?? details.receiverText ?? details.detail,
      ) || '[交换微信请求]'
      identity = clean(details.requestId ?? details.id ?? details.cardId)
    } else if (isStaffWechatRequest) {
      kind = 'card'
      cardType = 'wechatExchange'
      normalizedText = '[换微信请求]'
      identity = row.idServer
    } else if (isWechatExchangeSucceeded) {
      kind = 'card'
      cardType = 'wechatExchange'
      normalizedText = '[微信交换成功]'
      identity = row.idServer
    } else if (isStaffInterviewInvite) {
      kind = 'card'
      cardType = 'interviewInvite'
      normalizedText = '[面试邀请]'
      identity = interviewMethod === 'wechatVideo' && interviewStartsAt !== null &&
        interviewEndsAt !== null && interviewEndsAt > interviewStartsAt
        ? [String(interviewStartsAt), String(interviewEndsAt), interviewMethod].join('\x1f')
        : [
            row.idServer,
            String(interviewStartsAt),
            String(interviewEndsAt),
            interviewMethod,
          ].join('\x1f')
    } else if (isCandidateOnlineResume || isCandidateAttachmentResume) {
      kind = 'card'
      cardType = 'resumeAttachment'
      normalizedText = isCandidateOnlineResume ? clean(inner.staffText) : '您好，这是我的附件简历，请查收'
      identity = row.idServer
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
    const cardProjection = cardType === 'wechatExchange' &&
      (isCandidateWechatRequest || isStaffWechatRequest || isWechatExchangeSucceeded)
      ? 'card\x1fwechatExchange'
      : cardType === 'interviewInvite' && isStaffInterviewInvite &&
          interviewMethod === 'wechatVideo' && interviewStartsAt !== null &&
          interviewEndsAt !== null && interviewEndsAt > interviewStartsAt
        ? `card\x1finterviewInvite\x1f${interviewStartsAt}\x1f${interviewEndsAt}\x1fwechatVideo`
        : `card\x1f${cardType ?? 'other'}\x1f${clean(identity || row.idServer || normalizedText)}`
    const contentHash = kind === 'card'
      ? digest(cardProjection)
      : digest(clean(normalizedText))
    return { direction, contentHash }
  }
  const liveTailMatches = (rows: ActionSnapshotRow[], target: string): boolean | null => {
    if (expectedTail.length === 0) return true
    if (rows.length < expectedTail.length) return false
    const staffID = runtimeStaffID()
    if (!staffID) return null
    const tail = rows.slice(-expectedTail.length)
    return tail.every((row, index) => {
      const actual = actionAnchorFor(row, staffID, target)
      const expected = expectedTail[index]
      return actual !== null && actual.direction === expected.direction && actual.contentHash === expected.contentHash
    })
  }
  const baselineState = (target: string): 'match' | 'unresolved' | 'changed' => {
    const actual = liveTimelineProjection()
    if (!actual) return 'unresolved'
    if (actual.sourceKeys.length !== expectedBaselineServerSourceKeys.length ||
        actual.sourceKeys.some((key, index) => key !== expectedBaselineServerSourceKeys[index])) return 'changed'
    const tailMatches = liveTailMatches(actual.windowRows, target)
    return tailMatches === null ? 'unresolved' : tailMatches ? 'match' : 'changed'
  }
  const currentTargetBinding = (): { target: string; token: string } | null => {
    const engine = asRecord(w.imEngine)
    const sessions = Array.isArray(engine?.sessions) ? engine.sessions as AnyRecord[] : []
    const matches = sessions.filter((item) => clean(item.sessionId) === conversationRef)
    const target = matches.length === 1 ? clean(matches[0].peerPartnerId) : ''
    return target ? { target, token: digest(JSON.stringify([conversationRef, target])) } : null
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
    const binding = currentTargetBinding()
    if (!binding) return failedEvaluation('guard_unresolved')
    if (binding.token !== expectedTargetBindingToken) return failedEvaluation('target_changed')

    const currentSurface = surface()
    if (!currentSurface) return failedEvaluation(surfaceFailure)
    if (currentSurface.composer.value !== expectedComposerValue) return failedEvaluation(valueFailure)
    const currentBaselineState = baselineState(binding.target)
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

// 邀面准备阶段只操作可撤销编辑器控件，绝不触碰最终发送按钮。所有相邻
// 页面交互至少间隔 1 秒并带有限随机抖动；元素等待是条件轮询，最长 10 秒。
async function mainPrepareInterviewEditor(
  conversationRef: string,
  interview: InterviewDetails,
  expectedPrincipalFingerprint: string,
  irreversibleNotAfterMs: number,
): Promise<MainPrepareInterviewEditorResult> {
  type AnyRecord = Record<string, unknown>
  type FailureReason = Extract<MainPrepareInterviewEditorResult, { status: 'failed' }>['reason']
  // detail 只允许白名单内容：abort 点标记、选择器命中数、控件存在/可见/禁用
  // 三态、以及我方自己填入的日期/时间/时长回读值。弹窗标题、联系人、留言等
  // 含身份信息的字段一律不采（数据边界裁决：微信号/手机号不得进入错误详情）。
  const capDetail = (value: string): string => {
    let out = value.slice(0, 220)
    while (new TextEncoder().encode(out).length > 880) out = out.slice(0, -8)
    return out
  }
  const failed = (reason: FailureReason, detail?: string): MainPrepareInterviewEditorResult =>
    detail === undefined || detail === ''
      ? { status: 'failed', reason }
      : { status: 'failed', reason, detail: capDetail(detail) }
  let emergencyCleanup: (() => Promise<void>) | null = null
  try {
    const w = window as unknown as AnyRecord
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
    const digest = async (value: string): Promise<string> => {
      const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value))
      return Array.from(new Uint8Array(bytes), (byte) => byte.toString(16).padStart(2, '0')).join('')
    }
    const normalizeIdentityPart = (value: unknown): string | null => {
      if (typeof value === 'string') {
        const normalized = value.trim()
        return normalized.length > 0 ? normalized : null
      }
      if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
      return null
    }
    const readInitialState = (): AnyRecord => {
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
        else if (char === '}' && --depth === 0) {
          try { return JSON.parse(candidate.slice(start, index + 1)) as AnyRecord } catch { return {} }
        }
      }
      return {}
    }
    const initial = readInitialState()
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
    const routeMatches = (): boolean => {
      try {
        const route = new URL(location.href)
        return route.pathname === '/app/im' && route.searchParams.get('sessionId') === conversationRef
      } catch {
        return false
      }
    }
    const targetResolved = (): boolean => {
      const engine = asRecord(w.imEngine)
      const sessions = Array.isArray(engine?.sessions) ? engine.sessions as AnyRecord[] : []
      const matches = sessions.filter((item) => clean(item.sessionId) === conversationRef)
      return matches.length === 1 && clean(matches[0].peerPartnerId) !== ''
    }
    const composerEmpty = (): boolean => {
      const composers = Array.from(document.querySelectorAll<HTMLTextAreaElement>(
        'textarea.km-input__original.is-normal.is-textarea.is-autoresize',
      )).filter((element) => visible(element) && element.closest('.im-sender__input-wrapper') !== null)
      return composers.length === 1 && composers[0].value === ''
    }
    const wait = (delayMs: number): Promise<void> =>
      new Promise((resolve) => setTimeout(resolve, delayMs))
    const interactionGap = (): Promise<void> => wait(1_000 + Math.floor(Math.random() * 501))
    const interact = async (node: HTMLElement): Promise<boolean> => {
      await interactionGap()
      if (!node.isConnected || !visible(node) || Date.now() > irreversibleNotAfterMs) return false
      const intrinsicClick = HTMLElement.prototype.click as (this: HTMLElement) => void
      Function.prototype.call.call(intrinsicClick, node)
      return true
    }
    // 翻月按钮不响应 HTMLElement.prototype.click 合成调用，只认按压事件序列
    // （2026-07-29 真机：intrinsic click 页头纹丝不动，pointer/mouse 序列翻页
    // 成功）。事件直接派发给节点本身，坐标只是事件属性，不按坐标命中，不存在
    // 误中其他元素的可能。仅翻月按钮使用本变体；其余控件 intrinsic click 已被
    // 真机证明有效，不换扳机。节奏与守卫同 interact。
    const interactByPointer = async (node: HTMLElement): Promise<boolean> => {
      await interactionGap()
      if (!node.isConnected || !visible(node) || Date.now() > irreversibleNotAfterMs) return false
      const rect = node.getBoundingClientRect()
      const clientX = rect.x + rect.width / 2
      const clientY = rect.y + rect.height / 2
      const fireMouse = (type: string, extra: MouseEventInit = {}): void => {
        node.dispatchEvent(new MouseEvent(type, {
          bubbles: true, cancelable: true, view: window, clientX, clientY, ...extra,
        }))
      }
      const firePointer = (type: string): void => {
        try {
          node.dispatchEvent(new PointerEvent(type, {
            bubbles: true, cancelable: true, clientX, clientY, pointerId: 1, isPrimary: true,
          }))
        } catch { /* PointerEvent 不可用时退化为纯鼠标序列 */ }
      }
      firePointer('pointerdown')
      fireMouse('mousedown', { buttons: 1 })
      firePointer('pointerup')
      fireMouse('mouseup')
      fireMouse('click')
      return true
    }
    const waitFor = async <T>(read: () => T | null, timeoutMs = 10_000): Promise<T | null> => {
      const deadline = Date.now() + Math.min(10_000, Math.max(0, timeoutMs))
      while (Date.now() <= deadline) {
        const value = read()
        if (value !== null) return value
        await wait(120)
      }
      return null
    }
    if (!Number.isSafeInteger(interview.startsAt) || !Number.isSafeInteger(interview.endsAt) ||
        interview.startsAt <= 0 || interview.endsAt <= interview.startsAt ||
        interview.method !== 'wechatVideo' ||
        (interview.endsAt - interview.startsAt) % 60_000 !== 0) {
      return failed('input_rejected')
    }
    // 平台时间选择器是 5 分钟格（2026-07-28 真机）；脑侧已在时间出生点向上
    // 取整，非格点时间抵达手侧说明上游有 bug——立即拒绝，绝不在手侧擅自
    // 取整（平台读回正证按脑侧毫秒值精确配对，手侧改时会让发出的卡片永远
    // 无法确认），也不再让它走到分钟项查找处白等 10 秒。
    if (interview.startsAt % 300_000 !== 0) return failed('input_rejected', 'startGrid')
    if (!Number.isFinite(irreversibleNotAfterMs) || Date.now() > irreversibleNotAfterMs) {
      return failed('action_window_elapsed')
    }
    if (!routeMatches()) return failed('route_changed')
    const principal = principalCanonical()
    if (!principal || await digest(principal) !== expectedPrincipalFingerprint) return failed('identity_changed')
    if (!targetResolved()) return failed('target_changed')
    if (!composerEmpty()) return failed('composer_nonempty')

    const details = Array.from(document.querySelectorAll<HTMLElement>('.im-session-detail')).filter(visible)
    if (details.length !== 1) return failed('surface_unavailable')
    const detail = details[0]
    const launchers = Array.from(
      detail.querySelectorAll<HTMLElement>('a[zp-stat-id="im_interview_invite_click"][type="button"]'),
    ).filter((node) => visible(node) && clean(node.textContent) === '约面试')
    if (launchers.length !== 1) return failed('surface_unavailable', `at=launcher n=${launchers.length}`)
    if (!await interact(launchers[0])) return failed('surface_unavailable', 'at=launcher.click')

    const modal = await waitFor(() => {
      const candidates = Array.from(
        document.querySelectorAll<HTMLElement>('.km-modal__wrapper.interview-modal'),
      ).filter((node) => visible(node) && node.querySelector('.interview-form') !== null)
      return candidates.length === 1 ? candidates[0] : null
    })
    if (!modal) return failed('editor_unavailable', 'at=modal')

    const closeEditor = async (): Promise<void> => {
      try {
        if (!modal.isConnected || !visible(modal)) return
        const cancelButtons = Array.from(
          modal.querySelectorAll<HTMLElement>('button[type="button"], a[type="button"]'),
        ).filter((node) => visible(node) && clean(node.textContent) === '取消')
        if (cancelButtons.length !== 1) return
        await interactionGap()
        if (!cancelButtons[0].isConnected || !visible(cancelButtons[0])) return
        const intrinsicClick = HTMLElement.prototype.click as (this: HTMLElement) => void
        const invokeClick = Function.prototype.call.bind(intrinsicClick, cancelButtons[0])
        invokeClick()
        // 取消也是页面交互：先等待至少一秒，再以条件轮询确认编辑器确已关闭。
        await wait(1_000)
        await waitFor(() => !modal.isConnected || !visible(modal) ? true : null)
      } catch {
        // best effort：清理失败不能篡改原始失败原因。
      }
    }
    emergencyCleanup = closeEditor
    // 白名单现场快照：两类浮层可见数、日期标签/时间/时长输入的当前值（全部
    // 是我方自设值或平台预置枚举）、以及表单校验错误的平台文案。逐项 try，
    // 任何一项读不到都不影响其余与主流程。
    const snapshot = (): string => {
      const parts: string[] = []
      try {
        const datePoppers = Array.from(document.querySelectorAll<HTMLElement>(
          '.km-popover.km-date-picker__popper, .km-date-picker__popper',
        )).filter(visible).length
        const timerPoppers = Array.from(document.querySelectorAll<HTMLElement>(
          '.km-popover.timer-cascader__popper, .timer-cascader__popper',
        )).filter(visible).length
        parts.push(`dpop=${datePoppers}`, `tpop=${timerPoppers}`)
      } catch { parts.push('pop=?') }
      try {
        const labels = Array.from(modal.querySelectorAll<HTMLElement>('.km-date-picker__label'))
          .filter(visible).map((node) => clean(node.textContent))
        parts.push(`lbl=${labels.join(',') || '-'}`)
      } catch { parts.push('lbl=?') }
      try {
        const timeValues = Array.from(modal.querySelectorAll<HTMLInputElement>(
          'input[placeholder="请选择时间"]',
        )).filter(visible).map((node) => clean(node.value) || '-')
        parts.push(`t=${timeValues.join(',') || '-'}`)
      } catch { parts.push('t=?') }
      try {
        const durationValues = Array.from(modal.querySelectorAll<HTMLInputElement>(
          'input[placeholder="面试时长"]',
        )).filter(visible).map((node) => clean(node.value) || '-')
        parts.push(`u=${durationValues.join(',') || '-'}`)
      } catch { parts.push('u=?') }
      try {
        // 校验文案是快照里唯一的页面生成文本。若平台某条错误回显字段内容
        // （手机号/微信号形状的长英数串），先打码再上行：微信号不得进入
        // 错误详情是冻结条款，不赌平台文案的实现。
        const formErrors = Array.from(modal.querySelectorAll<HTMLElement>('.km-form-item__error'))
          .filter(visible).map((node) => clean(node.textContent).replace(/[0-9A-Za-z_-]{6,}/gu, '#'))
          .filter((text) => text !== '').slice(0, 2)
        if (formErrors.length > 0) parts.push(`err=${formErrors.join('|').slice(0, 60)}`)
      } catch { /* 无校验错误面板时静默 */ }
      return parts.join(' ')
    }
    const abort = async (reason: FailureReason, at?: string): Promise<MainPrepareInterviewEditorResult> => {
      let detail: string | undefined
      try {
        detail = `${at === undefined ? '' : `at=${at} `}${snapshot()}`
      } catch {
        detail = at === undefined ? undefined : `at=${at}`
      }
      await closeEditor()
      emergencyCleanup = null
      return failed(reason, detail)
    }

    const onlineItems = Array.from(
      modal.querySelectorAll<HTMLElement>('.interview-form-way-list-item'),
    ).filter((node) => visible(node) && clean(node.textContent).includes('线上面试'))
    if (onlineItems.length !== 1) return await abort('editor_unavailable', `online n=${onlineItems.length}`)
    const online = onlineItems[0]
    const onlineSelected = (): boolean => {
      // 2026-07-27 真机：弹窗标题是"邀请{候选人姓名}参加 线上面试"并实时跟随类型
      // 切换（现场面试时为"参加 现场面试"），页面上不存在字面恰好"参加 线上面试"
      // 的节点；按包含匹配取叶层节点。
      const onlineTitle = /参加\s*线上面试/u
      const titleNodes = Array.from(modal.querySelectorAll<HTMLElement>('*'))
        .filter((node) => visible(node) && onlineTitle.test(clean(node.textContent)))
        .filter((node) => !Array.from(node.children).some(
          (child) => visible(child) && onlineTitle.test(clean(child.textContent)),
        ))
      return titleNodes.length >= 1
    }
    if (!onlineSelected()) {
      if (!await interact(online)) return await abort('editor_unavailable', 'online.click')
      if (!await waitFor(() => onlineSelected() ? true : null)) {
        return await abort('editor_unavailable', 'online.echo')
      }
    }

    const start = new Date(interview.startsAt)
    if (!Number.isFinite(start.getTime())) return await abort('input_rejected', 'startInvalid')
    const year = start.getFullYear()
    const month = start.getMonth() + 1
    const day = start.getDate()
    const hour = start.getHours()
    const minute = start.getMinutes()
    const pad2 = (value: number): string => String(value).padStart(2, '0')
    const expectedDateText = `${year}-${pad2(month)}-${pad2(day)}`
    const expectedTimeText = `${pad2(hour)}:${pad2(minute)}`
    const durationMinutes = (interview.endsAt - interview.startsAt) / 60_000
    const expectedDurationText = durationMinutes === 60 ? '1小时' : `${durationMinutes}分钟`
    if (![15, 30, 45, 60].includes(durationMinutes)) {
      return await abort('input_rejected', `durationEnum min=${durationMinutes}`)
    }

    const timeFormItem = Array.from(modal.querySelectorAll<HTMLElement>('.km-form-item'))
      .find((node) => visible(node) && /面试时间/u.test(clean(node.textContent))) ?? modal
    const dateControl = timeFormItem.querySelector<HTMLElement>('.km-date-picker') ??
      Array.from(modal.querySelectorAll<HTMLElement>('.km-date-picker')).find(visible)
    if (!dateControl) return await abort('date_unavailable', 'dateCtl.miss')
    if (!await interact(dateControl)) return await abort('date_unavailable', 'dateCtl.click')
    let datePopover = await waitFor(() =>
      Array.from(document.querySelectorAll<HTMLElement>(
        '.km-popover.km-date-picker__popper, .km-date-picker__popper',
      )).find(visible) ?? null)
    if (!datePopover) return await abort('date_unavailable', 'datePop')
    const calendarParts = (root: ParentNode): { year: number; month: number } => ({
      year: Number(clean(root.querySelector('.km-date-picker__header-year')?.textContent).match(/\d+/u)?.[0]),
      month: Number(clean(root.querySelector('.km-date-picker__header-month')?.textContent).match(/\d+/u)?.[0]),
    })
    // 智联邀面日历真机只开放今天起数天（2026-07-28 实测 4 天），目标月至多
    // 相邻；翻月按钮无 disabled 标记、点击可能毫无反应（真机已证）。因此按
    // 月距离精确圈定翻页轮数、越界立即失败，不再让不可达月份把等待链拖满。
    const initialCalendar = calendarParts(datePopover)
    if (!initialCalendar.year || !initialCalendar.month) return await abort('date_unavailable', 'calHdr')
    const expectedMoves = Math.abs(year * 12 + month - (initialCalendar.year * 12 + initialCalendar.month))
    if (expectedMoves > 12) {
      return await abort(
        'date_unavailable',
        `navRange cal=${initialCalendar.year}-${initialCalendar.month} want=${year}-${month}`,
      )
    }
    for (let moves = 0; moves < expectedMoves + 1; moves += 1) {
      const current = calendarParts(datePopover)
      if (current.year === year && current.month === month) break
      if (!current.year || !current.month) return await abort('date_unavailable', 'calHdr')
      const buttons = Array.from(datePopover.querySelectorAll<HTMLElement>(
        '.km-date-picker__header button',
      )).filter(visible)
      if (buttons.length < 2) return await abort('date_unavailable', `navBtn n=${buttons.length}`)
      const direction = year * 12 + month > current.year * 12 + current.month ? 1 : -1
      const button = direction > 0 ? buttons[buttons.length - 1] : buttons[0]
      if (!await interactByPointer(button)) return await abort('date_unavailable', 'nav.click')
      // 翻月点击生效与否 2.5 秒内必有分晓（真机翻不动时页头永不变化），
      // 不沿用 10 秒默认等待。
      datePopover = await waitFor(() => {
        const candidate = Array.from(document.querySelectorAll<HTMLElement>(
          '.km-popover.km-date-picker__popper, .km-date-picker__popper',
        )).find(visible)
        if (!candidate) return null
        const next = calendarParts(candidate)
        return next.year !== current.year || next.month !== current.month ? candidate : null
      }, 2_500)
      if (!datePopover) {
        return await abort(
          'date_unavailable',
          `navStuck cal=${current.year}-${current.month} want=${year}-${month}`,
        )
      }
    }
    const finalCalendar = calendarParts(datePopover)
    if (finalCalendar.year !== year || finalCalendar.month !== month) {
      return await abort(
        'date_unavailable',
        `calMiss cal=${finalCalendar.year}-${finalCalendar.month} want=${year}-${month}`,
      )
    }
    const dayCells = Array.from(datePopover.querySelectorAll<HTMLElement>('.km-date-picker__cell'))
      .filter((cell) => visible(cell) &&
        !cell.classList.contains('km-date-picker__cell--disabled') &&
        !cell.classList.contains('km-date-picker__cell--silent') &&
        clean(cell.querySelector('.km-date-picker__cell-value')?.textContent ?? cell.textContent) === String(day))
    if (dayCells.length !== 1) return await abort('date_unavailable', `dayCell n=${dayCells.length} d=${day}`)
    if (!await interact(dayCells[0])) return await abort('date_unavailable', 'dayCell.click')
    const exactDate = await waitFor(() => {
      const labels = Array.from(
        modal.querySelectorAll<HTMLElement>('.km-date-picker__label'),
      ).filter((node) => visible(node) && clean(node.textContent) === expectedDateText)
      return labels.length === 1 ? labels[0] : null
    })
    if (!exactDate) return await abort('date_unavailable', 'dateEcho')

    const timeControl = Array.from(
      modal.querySelectorAll<HTMLElement>('.timer-cascader .km-select, .interview-form__time--item .km-select'),
    ).filter(visible).filter((node) =>
      node.querySelector<HTMLInputElement>('input[placeholder="请选择时间"]') !== null)
    if (timeControl.length !== 1) return await abort('time_unavailable', `timeCtl n=${timeControl.length}`)
    if (!await interact(timeControl[0])) return await abort('time_unavailable', 'timeCtl.click')
    const timerPopover = await waitFor(() =>
      Array.from(document.querySelectorAll<HTMLElement>(
        '.km-popover.timer-cascader__popper, .timer-cascader__popper',
      )).find(visible) ?? null)
    if (!timerPopover) return await abort('time_unavailable', 'timerPop')
    // 时间浮层普查：项总数、带 data-hour 的小时项数、目标项三态。只含计数
    // 与状态词，不含任何页面自由文本。
    const timerCensus = (): string => {
      try {
        const items = timerPopover.querySelectorAll('.timer-cascader__item').length
        const hourItems = timerPopover.querySelectorAll('.timer-cascader__item[data-hour]').length
        return `items=${items} hours=${hourItems}`
      } catch { return 'items=?' }
    }
    const minuteState = (): string => {
      try {
        const node = timerPopover.querySelector<HTMLElement>(
          `.timer-cascader__item[data-value="${expectedTimeText}"]`,
        )
        if (!node) return 'target=miss'
        const disabled = node.classList.contains('timer-cascader__item--disabled')
        return `target=${visible(node) ? 'vis' : 'hidden'}${disabled ? '.disabled' : ''}`
      } catch { return 'target=?' }
    }
    const hourNode = timerPopover.querySelector<HTMLElement>(`.timer-cascader__item[data-hour="${hour}"]`)
    if (!hourNode) return await abort('time_unavailable', `hourNode.miss h=${hour} ${timerCensus()}`)
    if (!visible(hourNode)) return await abort('time_unavailable', `hourNode.invisible h=${hour} ${timerCensus()}`)
    if (!await interact(hourNode)) return await abort('time_unavailable', `hourNode.click h=${hour} ${timerCensus()}`)
    const minuteNode = await waitFor(() => {
      const candidate = timerPopover.querySelector<HTMLElement>(
        `.timer-cascader__item[data-value="${expectedTimeText}"]`,
      )
      return candidate && visible(candidate) && !candidate.classList.contains('timer-cascader__item--disabled')
        ? candidate
        : null
    })
    if (!minuteNode) return await abort('time_unavailable', `minuteNode ${minuteState()} ${timerCensus()}`)
    if (!await interact(minuteNode)) return await abort('time_unavailable', `minuteNode.click ${timerCensus()}`)
    const exactTime = await waitFor(() => {
      const inputs = Array.from(
        modal.querySelectorAll<HTMLInputElement>('input[placeholder="请选择时间"]'),
      ).filter((node) => visible(node) && clean(node.value) === expectedTimeText)
      return inputs.length === 1 ? inputs[0] : null
    })
    if (!exactTime) return await abort('time_unavailable', 'timeEcho')

    // 2026-07-27 真机："面试时长"只出现在输入框 placeholder（不进任何表单项文本），
    // 控件与日期/时间同处"面试时间"一行；按 placeholder 反向定位所属 km-select。
    const durationInputs = Array.from(
      modal.querySelectorAll<HTMLInputElement>('input[placeholder="面试时长"]'),
    ).filter((node) => visible(node))
    const durationControl = durationInputs.length === 1
      ? durationInputs[0].closest<HTMLElement>('.km-select')
      : null
    if (!durationControl) return await abort('duration_unavailable', `durCtl n=${durationInputs.length}`)
    if (!await interact(durationControl)) return await abort('duration_unavailable', 'durCtl.click')
    const durationOption = await waitFor(() => {
      const optionMinutes = (text: string): number | null => {
        const normalized = clean(text)
        const hours = Number(normalized.match(/(\d+(?:\.\d+)?)\s*小时/u)?.[1] ?? 0)
        const minutes = Number(normalized.match(/(\d+)\s*分钟/u)?.[1] ?? 0)
        const total = hours * 60 + minutes
        return Number.isFinite(total) && total > 0 ? total : null
      }
      const options = Array.from(document.querySelectorAll<HTMLElement>(
        '.km-popover [role="option"], .km-select-dropdown__item, .km-option',
      )).filter(visible)
      const matches = options.filter((node) => optionMinutes(node.textContent ?? '') === durationMinutes)
      return matches.length === 1 ? matches[0] : null
    })
    if (!durationOption) return await abort('duration_unavailable', `durOpt min=${durationMinutes}`)
    if (!await interact(durationOption)) return await abort('duration_unavailable', 'durOpt.click')
    const exactDuration = await waitFor(() => {
      const inputs = Array.from(
        modal.querySelectorAll<HTMLInputElement>('input[placeholder="面试时长"]'),
      ).filter((node) => visible(node) && clean(node.value) === expectedDurationText)
      return inputs.length === 1 ? inputs[0] : null
    })
    if (!exactDuration) return await abort('duration_unavailable', 'durEcho')

    const methodCandidates = Array.from(
      modal.querySelectorAll<HTMLElement>('.interview-platform__btn'),
    ).filter((node) => visible(node) && clean(node.textContent) === '微信视频')
    if (methodCandidates.length !== 1) {
      return await abort('method_unavailable', `method n=${methodCandidates.length}`)
    }
    let method = methodCandidates[0]
    if (!method.classList.contains('is-checked')) {
      if (!await interact(method)) return await abort('method_unavailable', 'method.click')
      const exactMethod = await waitFor(() => {
        const matches = Array.from(
          modal.querySelectorAll<HTMLElement>('.interview-platform__btn.is-checked'),
        ).filter((node) => visible(node) && clean(node.textContent) === '微信视频')
        return matches.length === 1 ? matches[0] : null
      })
      if (!exactMethod) return await abort('method_unavailable', 'method.echo')
      method = exactMethod
    }

    // 最后一次编辑器交互与随后可能发生的最终发送之间也必须留出节奏间隔。
    await interactionGap()
    if (Date.now() > irreversibleNotAfterMs) return await abort('action_window_elapsed')
    if (!routeMatches()) return await abort('route_changed')
    if (!targetResolved()) return await abort('target_changed')
    if (!composerEmpty()) return await abort('composer_nonempty')
    if (!onlineSelected()) return await abort('input_rejected', 'finalOnline')
    const dateValue = clean(exactDate.textContent)
    const timeValue = clean(exactTime.value)
    const durationValue = clean(exactDuration.value)
    const methodValue = clean(method.textContent)
    if (dateValue !== expectedDateText || timeValue !== expectedTimeText ||
        durationValue !== expectedDurationText || methodValue !== '微信视频' ||
        !method.classList.contains('is-checked')) {
      // 四个回读值全部来自我方设定或平台预置枚举，可整体回带。
      return await abort(
        'input_rejected',
        `finalEcho d=${dateValue || '-'} t=${timeValue || '-'} u=${durationValue || '-'} m=${methodValue || '-'}`,
      )
    }
    emergencyCleanup = null
    return {
      status: 'ready',
      prepared: {
        startsAt: interview.startsAt,
        endsAt: interview.endsAt,
        method: 'wechatVideo',
        dateValue,
        timeValue,
        durationValue,
        methodValue,
      },
    }
  } catch (error) {
    if (emergencyCleanup) {
      try { await emergencyCleanup() } catch { /* best effort */ }
    }
    // 只带异常类名（TypeError 等语言级标识），不带异常消息——消息可能含页面内容。
    return failed('unexpected', `exc=${error instanceof Error ? error.name : typeof error}`)
  }
}

// 两类卡片发送与微信请求接受共用这一份同步 evaluator。preflight 与
// commit 传入字面同一函数和同一组冻结参数；commit 最后一份绿色结果后
// 不再读页面，立即调用唯一一次标准 click。
function mainSendCardOnce(
  conversationRef: string,
  cardKind: MainCardEvaluatorAction,
  interview: InterviewDetails | null,
  requestSourceKey: string | null,
  expectedTail: ZhilianMessageAnchor[],
  expectedPrincipalFingerprint: string,
  irreversibleNotAfterMs: number,
  expectedBaselineServerSourceKeys: string[],
  expectedTargetBindingToken: string,
  phase: MainCardPhase,
): MainSendCardOnceResult {
  type AnyRecord = Record<string, unknown>
  interface SnapshotRow {
    idServer: string
    status: string
    type: string | number
    from: string
    text: string
    content: string
    contentWasString: boolean
    time: number
    sourceIndex: number
  }
  type FailureReason = Extract<MainSendCardOnceResult, { status: 'failed' }>['reason']
  const failed = (reason: FailureReason, detail?: string): MainSendCardOnceResult =>
    detail === undefined ? { status: 'failed', reason } : { status: 'failed', reason, detail }
  let wechatCopyCardsBeforeClick: number | undefined
  const w = window as unknown as AnyRecord
  const asRecord = (value: unknown): AnyRecord | null =>
    value !== null && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC')
    .replace(/\u00a0/gu, ' ')
    .replace(/\s+/gu, ' ')
    .trim()
  const stableMessageIdentity = (value: unknown): string => {
    if (typeof value === 'string') return value
    return typeof value === 'number' && Number.isFinite(value) ? String(value) : ''
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
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
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
  const toMillis = (value: unknown): number | null => {
    const number = Number(value)
    if (!Number.isFinite(number) || number <= 0) return null
    return number < 1_000_000_000_000 ? Math.trunc(number * 1000) : Math.trunc(number)
  }
  const readInitialState = (): AnyRecord => {
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
      else if (char === '}' && --depth === 0) {
        try { return JSON.parse(candidate.slice(start, index + 1)) as AnyRecord } catch { return {} }
      }
    }
    return {}
  }
  const initial = readInitialState()
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
  const routeMatches = (): boolean => {
    try {
      const route = new URL(location.href)
      return route.pathname === '/app/im' && route.searchParams.get('sessionId') === conversationRef
    } catch {
      return false
    }
  }
  const currentTargetBinding = (): { target: string; token: string } | null => {
    const engine = asRecord(w.imEngine)
    const sessions = Array.isArray(engine?.sessions) ? engine.sessions as AnyRecord[] : []
    const matches = sessions.filter((item) => clean(item.sessionId) === conversationRef)
    const target = matches.length === 1 ? clean(matches[0].peerPartnerId) : ''
    return target ? { target, token: digest(JSON.stringify([conversationRef, target])) } : null
  }
  const snapshotContent = (value: unknown): string => {
    if (typeof value === 'string') return value
    if (value === null || value === undefined) return ''
    const serialized = JSON.stringify(value)
    return serialized === undefined ? String(value) : serialized
  }
  const liveTimeline = (): { sourceKeys: string[]; rows: SnapshotRow[] } | null => {
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
    const rows: SnapshotRow[] = []
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
        contentWasString: typeof row.content === 'string',
        time,
        sourceIndex,
      })
    }
    rows.sort((left, right) => left.time - right.time || left.sourceIndex - right.sourceIndex)
    const seen = new Set<string>()
    const ordered = rows.filter((row) => {
      if (seen.has(row.idServer)) return false
      seen.add(row.idServer)
      return true
    }).slice(-64)
    return {
      sourceKeys: ordered.map((row) => digest(`source-v1|${row.idServer}`)),
      rows: ordered,
    }
  }
  const runtimeStaffID = (): string => {
    const runtimeSession = asRecord(w.$session)
    const initialSession = asRecord(asRecord(initial.session)?.session)
    return clean(asRecord(runtimeSession?.staff)?.staffId) || clean(asRecord(initialSession?.staff)?.staffId)
  }
  const anchorFor = (row: SnapshotRow, staffID: string, target: string): ZhilianMessageAnchor | null => {
    const envelope = parseObject(row.content)
    const inner = parseObject(envelope.content)
    const details = Object.keys(inner).length > 0 ? inner : envelope
    const rawType = row.type
    const customType = Number(
      typeof rawType === 'number' || /^\d+$/u.test(String(rawType)) ? rawType : envelope.type,
    )
    const from = clean(row.from)
    const originTypeIsCandidate = details.originType === 2 ||
      (typeof details.originType === 'string' && details.originType.trim() === '2')
    const originTypeIsStaff = details.originType === 1 ||
      (typeof details.originType === 'string' && details.originType.trim() === '1')
    const customSuccess = rawType === 'custom' && row.contentWasString &&
      Object.keys(inner).length > 0 && clean(row.status).toLowerCase() === 'success'
    const isCandidateWechatRequest = customSuccess && customType === 105 &&
      from === target && originTypeIsCandidate
    const isStaffWechatRequest = customSuccess && customType === 105 &&
      from === staffID && originTypeIsStaff
    // 259 交换成功恒归属点同意的一方：我方发起(originType=1)由候选人点同意，
    // 259 归对方(in)；候选人发起(originType=2)由我方点同意，259 归我方(out)。
    // 两形态都必须双微信字段齐全。2026-07-28 生产页面双样本直读。
    const isWechatExchangeSucceeded = customSuccess && customType === 259 &&
      ((from === target && originTypeIsStaff) || (from === staffID && originTypeIsCandidate)) &&
      Boolean(clean(details.userWeChat)) && Boolean(clean(details.staffWeChat))
    const startsAt = toMillis(details.startTime)
    const endsAt = toMillis(details.endTime)
    const method = (details.interviewType === 2 || details.interviewType === 'VIDEO') &&
      (details.interviewPlatform === 4 || details.interviewPlatform === 'WECHAT_VIDEO')
      ? 'wechatVideo'
      : 'unknown'
    const isStaffInterviewInvite = customSuccess && customType === 355 &&
      from === staffID && Boolean(clean(details.interviewId)) &&
      startsAt !== null && endsAt !== null &&
      Boolean(clean(details.interviewType)) && Boolean(clean(details.interviewPlatform)) &&
      Object.prototype.hasOwnProperty.call(details, 'state')
    const isCandidateInterviewAcceptedText = rawType === 'text' && from === target &&
      clean(row.text) === '我已接受贵司的面试邀请，将准时参加面试'
    const isCandidateOnlineResume = rawType === 'custom' && row.contentWasString &&
      typeof envelope.type === 'string' && envelope.type.trim() === '313' && customType === 313 &&
      Object.keys(inner).length > 0 && from === target &&
      clean(row.status).toLowerCase() === 'success' &&
      clean(inner.staffText) === '对方向您发送了在线简历'
    const normalizedAttachmentType =
      envelope.type === 177 || envelope.type === '177' ? 177 : null
    const isCandidateAttachmentResume = rawType === 'custom' &&
      normalizedAttachmentType === 177 &&
      from === target && clean(row.status).toLowerCase() === 'success'
    let direction: ZhilianMessageAnchor['direction'] = from ? from === staffID ? 'out' : 'in' : 'system'
    let kind: 'text' | 'card' | 'system' = 'system'
    let text = ''
    let cardType: 'interviewInvite' | 'wechatExchange' | 'resumeAttachment' | null = null
    let identity = ''
    if (isCandidateInterviewAcceptedText) {
      kind = 'card'
      cardType = 'interviewInvite'
      text = clean(row.text)
      identity = row.idServer
    } else if (rawType === 'text') {
      if (!from) return null
      kind = 'text'
      text = clean(row.text)
    } else if (isCandidateWechatRequest) {
      kind = 'card'
      cardType = 'wechatExchange'
      text = clean(details.userContent ?? details.receiverText ?? details.detail) || '[交换微信请求]'
      identity = clean(details.requestId ?? details.id ?? details.cardId)
    } else if (isStaffWechatRequest) {
      kind = 'card'
      cardType = 'wechatExchange'
      text = '[换微信请求]'
      identity = row.idServer
    } else if (isWechatExchangeSucceeded) {
      kind = 'card'
      cardType = 'wechatExchange'
      text = '[微信交换成功]'
      identity = row.idServer
    } else if (isStaffInterviewInvite) {
      kind = 'card'
      cardType = 'interviewInvite'
      text = '[面试邀请]'
      identity = method === 'wechatVideo' && startsAt !== null &&
        endsAt !== null && endsAt > startsAt
        ? [String(startsAt), String(endsAt), method].join('\x1f')
        : [row.idServer, String(startsAt), String(endsAt), method].join('\x1f')
    } else if (isCandidateOnlineResume || isCandidateAttachmentResume) {
      kind = 'card'
      cardType = 'resumeAttachment'
      text = isCandidateOnlineResume ? clean(inner.staffText) : '您好，这是我的附件简历，请查收'
      identity = row.idServer
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
    const cardProjection = cardType === 'wechatExchange' &&
      (isCandidateWechatRequest || isStaffWechatRequest || isWechatExchangeSucceeded)
      ? 'card\x1fwechatExchange'
      : cardType === 'interviewInvite' && isStaffInterviewInvite &&
          method === 'wechatVideo' && startsAt !== null && endsAt !== null && endsAt > startsAt
        ? `card\x1finterviewInvite\x1f${startsAt}\x1f${endsAt}\x1fwechatVideo`
        : `card\x1f${cardType ?? 'other'}\x1f${clean(identity || row.idServer || text)}`
    const contentHash = kind === 'card'
      ? digest(cardProjection)
      : digest(clean(text))
    return { direction, contentHash }
  }
  const baselineMatches = (target: string): 'match' | 'changed' | 'unresolved' => {
    const current = liveTimeline()
    if (!current) return 'unresolved'
    if (current.sourceKeys.length !== expectedBaselineServerSourceKeys.length ||
        current.sourceKeys.some((key, index) => key !== expectedBaselineServerSourceKeys[index])) return 'changed'
    if (expectedTail.length === 0) return 'match'
    if (current.rows.length < expectedTail.length) return 'changed'
    const staffID = runtimeStaffID()
    if (!staffID) return 'unresolved'
    const tail = current.rows.slice(-expectedTail.length)
    for (let index = 0; index < tail.length; index += 1) {
      const actual = anchorFor(tail[index], staffID, target)
      const expected = expectedTail[index]
      if (!actual || actual.direction !== expected.direction || actual.contentHash !== expected.contentHash) {
        return 'changed'
      }
    }
    return 'match'
  }
  const validInterviewSurface = (modal: HTMLElement, value: InterviewDetails): boolean => {
    if (!Number.isSafeInteger(value.startsAt) || !Number.isSafeInteger(value.endsAt) ||
        value.startsAt <= 0 || value.endsAt <= value.startsAt || value.method !== 'wechatVideo' ||
        value.startsAt % 60_000 !== 0 || (value.endsAt - value.startsAt) % 60_000 !== 0) return false
    const start = new Date(value.startsAt)
    if (!Number.isFinite(start.getTime())) return false
    const pad2 = (part: number): string => String(part).padStart(2, '0')
    const expectedDate = `${start.getFullYear()}-${pad2(start.getMonth() + 1)}-${pad2(start.getDate())}`
    const expectedTime = `${pad2(start.getHours())}:${pad2(start.getMinutes())}`
    const durationMinutes = (value.endsAt - value.startsAt) / 60_000
    if (![15, 30, 45, 60].includes(durationMinutes)) return false
    const expectedDuration = durationMinutes === 60 ? '1小时' : `${durationMinutes}分钟`
    const dateMatches = Array.from(
      modal.querySelectorAll<HTMLElement>('.km-date-picker__label'),
    ).filter((node) => visible(node) && clean(node.textContent) === expectedDate)
    const timeMatches = Array.from(
      modal.querySelectorAll<HTMLInputElement>('input[placeholder="请选择时间"]'),
    ).filter((node) => visible(node) && clean(node.value) === expectedTime)
    const durationMatches = Array.from(
      modal.querySelectorAll<HTMLInputElement>('input[placeholder="面试时长"]'),
    ).filter((node) => visible(node) && clean(node.value) === expectedDuration)
    const methodMatches = Array.from(
      modal.querySelectorAll<HTMLElement>('.interview-platform__btn.is-checked'),
    ).filter((node) => visible(node) && clean(node.textContent) === '微信视频')
    const onlineItems = Array.from(
      modal.querySelectorAll<HTMLElement>('.interview-form-way-list-item'),
    ).filter((node) => visible(node) && clean(node.textContent).includes('线上面试'))
    if (onlineItems.length !== 1) return false
    // 2026-07-27 真机：标题为"邀请{候选人姓名}参加 线上面试"，按包含匹配取叶层节点。
    const onlineTitle = /参加\s*线上面试/u
    const titleMatches = Array.from(modal.querySelectorAll<HTMLElement>('*'))
      .filter((node) => visible(node) && onlineTitle.test(clean(node.textContent)))
      .filter((node) => !Array.from(node.children).some(
        (child) => visible(child) && onlineTitle.test(clean(child.textContent)),
      ))
    return dateMatches.length === 1 && timeMatches.length === 1 &&
      durationMatches.length === 1 && methodMatches.length === 1 &&
      titleMatches.length >= 1
  }

  if (!Number.isFinite(irreversibleNotAfterMs) || Date.now() > irreversibleNotAfterMs) {
    return failed('action_window_elapsed')
  }
  if (phase !== 'preflight' && phase !== 'commit') return failed('input_rejected')
  if ((cardKind === 'wechatInvite' && (interview !== null || requestSourceKey !== null)) ||
      (cardKind === 'interviewInvite' && (interview === null || requestSourceKey !== null)) ||
      (cardKind === 'wechatAccept' && (
        interview !== null || requestSourceKey === null ||
        !/^[0-9a-f]{64}$/u.test(requestSourceKey)
      ))) return failed('input_rejected')
  if (!routeMatches()) return failed('route_changed')
  const principal = principalCanonical()
  if (!principal || digest(principal) !== expectedPrincipalFingerprint) return failed('identity_changed')
  const binding = currentTargetBinding()
  if (!binding) return failed('guard_unresolved')
  if (binding.token !== expectedTargetBindingToken) return failed('target_changed')
  const baseline = baselineMatches(binding.target)
  if (baseline !== 'match') return failed(baseline === 'changed' ? 'baseline_changed' : 'guard_unresolved')
  const details = Array.from(document.querySelectorAll<HTMLElement>('.im-session-detail')).filter(visible)
  if (details.length !== 1) return failed('surface_unavailable')
  const composers = Array.from(document.querySelectorAll<HTMLTextAreaElement>(
    'textarea.km-input__original.is-normal.is-textarea.is-autoresize',
  )).filter((element) => visible(element) && element.closest('.im-sender__input-wrapper') !== null)
  if (cardKind !== 'wechatAccept') {
    if (composers.length !== 1) return failed('surface_unavailable')
    if (composers[0].closest('.im-session-detail') !== details[0]) return failed('surface_unavailable')
    if (composers[0].value !== '') return failed('composer_nonempty')
  }

  let actionTarget: HTMLElement | null = null
  if (cardKind === 'wechatInvite') {
    const matches = Array.from(
      details[0].querySelectorAll<HTMLElement>('a[zp-stat-id="im_ask_for_wx_open"][type="button"]'),
    ).filter((node) => visible(node) && clean(node.textContent) === '换微信')
    if (matches.length !== 1) return failed('surface_unavailable')
    actionTarget = matches[0]
  } else if (cardKind === 'interviewInvite') {
    const modals = Array.from(
      document.querySelectorAll<HTMLElement>('.km-modal__wrapper.interview-modal'),
    ).filter((node) => visible(node) && node.querySelector('.interview-form') !== null)
    if (modals.length !== 1 || interview === null || !validInterviewSurface(modals[0], interview)) {
      return failed('input_rejected')
    }
    const buttons = Array.from(modals[0].querySelectorAll<HTMLElement>('button[type="button"]'))
      .filter((node) => visible(node) && clean(node.textContent) === '发送')
    if (buttons.length !== 1) return failed('surface_unavailable')
    actionTarget = buttons[0]
  } else {
    const timeline = liveTimeline()
    const staffID = runtimeStaffID()
    if (!timeline || !staffID || requestSourceKey === null) return failed('guard_unresolved')
    const requestIndexes = timeline.sourceKeys
      .map((sourceKey, index) => sourceKey === requestSourceKey ? index : -1)
      .filter((index) => index >= 0)
    if (requestIndexes.length !== 1) return failed('input_rejected')
    const requestIndex = requestIndexes[0]
    const request = timeline.rows[requestIndex]
    const requestEnvelope = parseObject(request.content)
    const requestInner = parseObject(requestEnvelope.content)
    const requestDetails = Object.keys(requestInner).length > 0 ? requestInner : requestEnvelope
    const requestType = Number(
      typeof request.type === 'number' || /^\d+$/u.test(String(request.type))
        ? request.type
        : requestEnvelope.type,
    )
    const candidateOrigin = requestDetails.originType === 2 ||
      (typeof requestDetails.originType === 'string' && requestDetails.originType.trim() === '2')
    if (request.type !== 'custom' || !request.contentWasString ||
        Object.keys(requestInner).length === 0 ||
        clean(request.status).toLowerCase() !== 'success' || requestType !== 105 ||
        clean(request.from) !== binding.target || !candidateOrigin) {
      return failed('input_rejected')
    }
    const nextRequestOffset = timeline.rows.slice(requestIndex + 1).findIndex((row) => {
      const envelope = parseObject(row.content)
      return Number(
        typeof row.type === 'number' || /^\d+$/u.test(String(row.type)) ? row.type : envelope.type,
      ) === 105
    })
    const outcomeEnd = nextRequestOffset < 0
      ? timeline.rows.length
      : requestIndex + 1 + nextRequestOffset
    // 候选人主动发起(originType=2)时由我方点同意，259 交换成功归我方(out)，
    // 故已存在的交换结果要按 staffID 而非 target 认。2026-07-28 生产页面直读。
    const existingOutcomes = timeline.rows.slice(requestIndex + 1, outcomeEnd).filter((row) => {
      const envelope = parseObject(row.content)
      const inner = parseObject(envelope.content)
      const value = Object.keys(inner).length > 0 ? inner : envelope
      const type = Number(
        typeof row.type === 'number' || /^\d+$/u.test(String(row.type)) ? row.type : envelope.type,
      )
      const origin = value.originType === 2 ||
        (typeof value.originType === 'string' && value.originType.trim() === '2')
      return row.type === 'custom' && row.contentWasString && Object.keys(inner).length > 0 &&
        clean(row.status).toLowerCase() === 'success' && type === 259 &&
        clean(row.from) === staffID && origin &&
        Boolean(clean(value.userWeChat)) && Boolean(clean(value.staffWeChat))
    })
    if (existingOutcomes.length !== 0) {
      return failed('surface_unavailable', `wxaccept:already_exchanged n=${existingOutcomes.length}`)
    }

    // 否定式必须排除："不同意"字面包含"同意"，放宽为包含匹配后若不排除，
    // 唯一命中的可能正是拒绝控件——那是真实的错误副作用。
    const rejecting = (text: string): boolean => /不同意|拒绝|婉拒|忽略/u.test(text)
    const allCards = Array.from(
      details[0].querySelectorAll<HTMLElement>('.imc-wx-request'),
    ).filter(visible)
    const pendingCards = allCards.filter((card) =>
      !card.classList.contains('is-wx-done') &&
      card.querySelector('.is-wx-done') === null)
    if (allCards.length === 0) return failed('surface_unavailable', 'wxaccept:no_card')
    if (pendingCards.length === 0) {
      return failed('surface_unavailable', `wxaccept:all_done cards=${allCards.length}`)
    }
    if (pendingCards.length !== 1) {
      return failed(
        'surface_unavailable',
        `wxaccept:multi_card cards=${allCards.length} pending=${pendingCards.length}`,
      )
    }
    // 类名优先：.imc-wx-request__actions-success 的类名语义即"同意"，旧项目
    // 生产上长期以它为首选；缺失时才按可见文本兜底。类名路径不要求文本命中
    // （可能是图标按钮），但同样排除否定式。
    const byClass = Array.from(
      pendingCards[0].querySelectorAll<HTMLElement>('.imc-wx-request__actions-success'),
    ).filter((node) => visible(node) && !rejecting(clean(node.textContent)))
    const byText = Array.from(
      pendingCards[0].querySelectorAll<HTMLElement>('button, a'),
    ).filter((node) => visible(node) &&
      /同意/u.test(clean(node.textContent)) && !rejecting(clean(node.textContent)))
    const actions = byClass.length > 0 ? byClass : byText
    if (actions.length === 0) {
      return failed('surface_unavailable', 'wxaccept:no_action byClass=0 byText=0')
    }
    if (actions.length !== 1) {
      return failed(
        'surface_unavailable',
        `wxaccept:multi_action byClass=${byClass.length} byText=${byText.length}`,
      )
    }
    wechatCopyCardsBeforeClick = allCards.filter(
      (card) => /复制微信号/u.test(clean(card.textContent)),
    ).length
    actionTarget = actions[0]
  }
  if (!actionTarget || !actionTarget.isConnected || Date.now() > irreversibleNotAfterMs) {
    return failed('action_window_elapsed')
  }
  if (phase === 'preflight') {
    return wechatCopyCardsBeforeClick === undefined
      ? { status: 'ready' }
      : { status: 'ready', wechatCopyCards: wechatCopyCardsBeforeClick }
  }
  const intrinsicClick = HTMLElement.prototype.click as (this: HTMLElement) => void
  const invokeClick = Function.prototype.call.bind(intrinsicClick, actionTarget)
  invokeClick()
  return wechatCopyCardsBeforeClick === undefined
    ? { status: 'clicked' }
    : { status: 'clicked', wechatCopyCards: wechatCopyCardsBeforeClick }
}

// 卡片发送后的正证只认与发送基线同源的实时 Vuex timeline：严格连续新增
// 一条、服务端 id 唯一、方向/类型/参数精确匹配。任何阴性都只返回未确认。
async function mainObserveStableOutboundCard(
  conversationRef: string,
  cardKind: MainCardAction,
  interview: InterviewDetails | null,
  baselineServerSourceKeys: string[],
  expectedTargetBindingToken: string,
): Promise<MainObserveStableOutboundCardResult> {
  type AnyRecord = Record<string, unknown>
  interface SnapshotRow {
    idServer: string
    status: string
    type: string | number
    from: string
    content: string
    contentWasString: boolean
    time: number
    sourceIndex: number
  }
  const w = window as unknown as AnyRecord
  const asRecord = (value: unknown): AnyRecord | null =>
    value !== null && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
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
  const parseObject = (value: unknown): AnyRecord => {
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) return value as AnyRecord
    if (typeof value !== 'string' || value.length === 0) return {}
    try {
      const parsed = JSON.parse(value) as unknown
      return asRecord(parsed) ?? {}
    } catch {
      return {}
    }
  }
  const toMillis = (value: unknown): number | null => {
    const number = Number(value)
    if (!Number.isFinite(number) || number <= 0) return null
    return number < 1_000_000_000_000 ? Math.trunc(number * 1000) : Math.trunc(number)
  }
  const routeMatches = (): boolean => {
    try {
      const route = new URL(location.href)
      return route.pathname === '/app/im' && route.searchParams.get('sessionId') === conversationRef
    } catch {
      return false
    }
  }
  const failed = (): MainObserveStableOutboundCardResult => ({
    selected: routeMatches(),
    matchingNewServerMessages: 0,
  })
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const readInitialStaffID = (): string => {
    const source = Array.from(document.scripts)
      .map((script) => script.textContent ?? '')
      .find((candidate) => candidate.includes('__INITIAL_STATE__='))
    if (!source) return ''
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
          const session = asRecord(asRecord(initial?.session)?.session)
          return clean(asRecord(session?.staff)?.staffId)
        } catch {
          return ''
        }
      }
    }
    return ''
  }
  const resolveTarget = (): string | null => {
    const engine = asRecord(w.imEngine)
    const sessions = Array.isArray(engine?.sessions) ? engine.sessions as AnyRecord[] : []
    const matches = sessions.filter((item) => clean(item.sessionId) === conversationRef)
    const target = matches.length === 1 ? clean(matches[0].peerPartnerId) : ''
    return target || null
  }
  const resolveTimeline = (): unknown | null => {
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
    if ((cardKind === 'wechatInvite' && interview !== null) ||
        (cardKind === 'interviewInvite' && (
          interview === null || !Number.isSafeInteger(interview.startsAt) ||
          !Number.isSafeInteger(interview.endsAt) || interview.startsAt <= 0 ||
          interview.endsAt <= interview.startsAt || interview.method !== 'wechatVideo'
        ))) return failed()
    if (!routeMatches() || !/^[0-9a-f]{64}$/u.test(expectedTargetBindingToken)) return failed()
    const target = resolveTarget()
    if (!target || await digest(JSON.stringify([conversationRef, target])) !== expectedTargetBindingToken) {
      return failed()
    }
    const sourceKeyPattern = /^[0-9a-f]{64}$/u
    if (baselineServerSourceKeys.length > 64 ||
        baselineServerSourceKeys.some((key) => !sourceKeyPattern.test(key)) ||
        new Set(baselineServerSourceKeys).size !== baselineServerSourceKeys.length) return failed()

    const rawRows = resolveTimeline()
    if (!Array.isArray(rawRows) || rawRows.length > 4096) return failed()
    const projected: SnapshotRow[] = []
    for (let sourceIndex = 0; sourceIndex < rawRows.length; sourceIndex += 1) {
      const row = asRecord(rawRows[sourceIndex])
      if (!row) return failed()
      const idServer = stableMessageIdentity(row.idServer)
      const time = Number(row.time)
      if (!idServer || !Number.isFinite(time) || time <= 0) return failed()
      const rawType = row.type
      const content = typeof row.content === 'string'
        ? row.content
        : row.content === null || row.content === undefined
          ? ''
          : JSON.stringify(row.content) ?? String(row.content)
      projected.push({
        idServer,
        status: clean(row.status),
        type: typeof rawType === 'number' || typeof rawType === 'string' ? rawType : String(rawType ?? ''),
        from: clean(row.from),
        content,
        contentWasString: typeof row.content === 'string',
        time,
        sourceIndex,
      })
    }
    projected.sort((left, right) => left.time - right.time || left.sourceIndex - right.sourceIndex)
    const seen = new Set<string>()
    const rows = projected.filter((row) => {
      if (seen.has(row.idServer)) return false
      seen.add(row.idServer)
      return true
    }).slice(-64)
    const currentSourceKeys: string[] = []
    for (const row of rows) currentSourceKeys.push(await digest(`source-v1|${row.idServer}`))
    const continuous = baselineServerSourceKeys.length < 64
      ? currentSourceKeys.length === baselineServerSourceKeys.length + 1 &&
        baselineServerSourceKeys.every((key, index) => currentSourceKeys[index] === key)
      : currentSourceKeys.length === 64 &&
        baselineServerSourceKeys.slice(1).every((key, index) => currentSourceKeys[index] === key)
    if (!continuous || rows.length === 0) return failed()
    const sourceKey = currentSourceKeys[currentSourceKeys.length - 1]
    if (baselineServerSourceKeys.includes(sourceKey)) return failed()

    const staffID = clean(asRecord(asRecord(w.$session)?.staff)?.staffId) || readInitialStaffID()
    if (!staffID) return failed()
    const row = rows[rows.length - 1]
    const envelope = parseObject(row.content)
    const inner = parseObject(envelope.content)
    const commonShape = row.type === 'custom' && row.contentWasString &&
      Object.keys(inner).length > 0 && clean(row.status).toLowerCase() === 'success' &&
      row.from === staffID
    if (!commonShape) return failed()

    let contentHash = ''
    let confirmedInterview: InterviewDetails | undefined
    if (cardKind === 'wechatInvite') {
      if (Number(envelope.type) !== 105 || inner.originType !== 1) return failed()
      contentHash = await digest('card\x1fwechatExchange')
    } else {
      const expected = interview as InterviewDetails
      const startsAt = toMillis(inner.startTime)
      const endsAt = toMillis(inner.endTime)
      if (Number(envelope.type) !== 355 || !clean(inner.interviewId) ||
          !Object.prototype.hasOwnProperty.call(inner, 'state') ||
          !(inner.interviewType === 2 || inner.interviewType === 'VIDEO') ||
          !(inner.interviewPlatform === 4 || inner.interviewPlatform === 'WECHAT_VIDEO') ||
          startsAt !== expected.startsAt || endsAt !== expected.endsAt ||
          startsAt === null || endsAt === null || endsAt <= startsAt) return failed()
      confirmedInterview = { startsAt, endsAt, method: 'wechatVideo' }
      contentHash = await digest(
        `card\x1finterviewInvite\x1f${startsAt}\x1f${endsAt}\x1fwechatVideo`,
      )
    }

    // digest 会让出事件循环；阳性返回前再次核对 route 与同一目标绑定。
    if (!routeMatches() || resolveTarget() !== target) return failed()
    return {
      selected: true,
      matchingNewServerMessages: 1,
      contentHash,
      sourceKey,
      ...(confirmedInterview ? { interview: confirmedInterview } : {}),
    }
  } catch {
    return failed()
  }
}

// 微信交换结果只读面：requestSourceKey 只在当前实时 timeline 内定位两种已证实
// 的 105 请求，并只认下一条 105 之前唯一的同 originType 259。招聘方微信只作
// 布尔正证，永不返回。
async function mainReadWechatExchangeOutcome(
  conversationRef: string,
  requestSourceKey: string,
  baselineServerSourceKeys: string[] | null,
  expectedTargetBindingToken: string | null,
): Promise<MainWechatExchangeOutcomeResult> {
  type AnyRecord = Record<string, unknown>
  interface SnapshotRow {
    idServer: string
    status: string
    type: string | number
    from: string
    content: string
    contentWasString: boolean
    time: number
    sourceIndex: number
  }
  const failed = (): MainWechatExchangeOutcomeResult => ({ confirmed: false })
  const w = window as unknown as AnyRecord
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
  const stableMessageIdentity = (value: unknown): string => {
    if (typeof value === 'string') return value
    return typeof value === 'number' && Number.isFinite(value) ? String(value) : ''
  }
  const digest = async (value: string): Promise<string> => {
    const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value))
    return Array.from(new Uint8Array(bytes), (byte) => byte.toString(16).padStart(2, '0')).join('')
  }
  const parseObject = (value: unknown): AnyRecord => {
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) return value as AnyRecord
    if (typeof value !== 'string' || value.length === 0) return {}
    try {
      const parsed = JSON.parse(value) as unknown
      return asRecord(parsed) ?? {}
    } catch {
      return {}
    }
  }
  const readInitialStaffID = (): string => {
    const source = Array.from(document.scripts)
      .map((script) => script.textContent ?? '')
      .find((candidate) => candidate.includes('__INITIAL_STATE__='))
    if (!source) return ''
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
          const session = asRecord(asRecord(initial?.session)?.session)
          return clean(asRecord(session?.staff)?.staffId)
        } catch {
          return ''
        }
      }
    }
    return ''
  }
  const routeMatches = (): boolean => {
    try {
      const route = new URL(location.href)
      return route.pathname === '/app/im' && route.searchParams.get('sessionId') === conversationRef
    } catch {
      return false
    }
  }
  const targetForCurrentRoute = (): string => {
    const engine = asRecord(w.imEngine)
    const sessions = Array.isArray(engine?.sessions) ? engine.sessions as AnyRecord[] : []
    const matches = sessions.filter((item) => clean(item.sessionId) === conversationRef)
    return matches.length === 1 ? clean(matches[0].peerPartnerId) : ''
  }
  const resolveTimeline = (): unknown | null => {
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
    const nuxt = asRecord(w.$nuxt)
    const nuxtRoot = asRecord(nuxt?.$root) ?? nuxt
    if (nuxtRoot) {
      const timeline = timelineSlot(nuxtRoot)
      if (timeline !== null) return timeline
    }
    const timelines = Array.from(
      document.querySelectorAll<HTMLElement>('.im-timeline__wrapper'),
    ).filter(visible)
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
  const rowShape = (
    row: SnapshotRow,
    expectedType: number,
    expectedOrigin: 1 | 2,
    expectedFrom: string,
  ): { matches: boolean; details: AnyRecord } => {
    const envelope = parseObject(row.content)
    const inner = parseObject(envelope.content)
    const details = Object.keys(inner).length > 0 ? inner : envelope
    const type = Number(
      typeof row.type === 'number' || /^\d+$/u.test(String(row.type)) ? row.type : envelope.type,
    )
    const origin = details.originType === expectedOrigin ||
      (typeof details.originType === 'string' &&
        details.originType.trim() === String(expectedOrigin))
    return {
      matches: row.type === 'custom' && row.contentWasString &&
        Object.keys(inner).length > 0 && clean(row.status).toLowerCase() === 'success' &&
        type === expectedType && clean(row.from) === expectedFrom && origin,
      details,
    }
  }

  try {
    if (!routeMatches() || !/^[0-9a-f]{64}$/u.test(requestSourceKey)) return failed()
    const target = targetForCurrentRoute()
    const staffID = clean(asRecord(asRecord(w.$session)?.staff)?.staffId) || readInitialStaffID()
    if (!target || !staffID) return failed()
    const rawRows = resolveTimeline()
    if (!Array.isArray(rawRows) || rawRows.length > 4096) return failed()
    const projected: SnapshotRow[] = []
    for (let sourceIndex = 0; sourceIndex < rawRows.length; sourceIndex += 1) {
      const row = asRecord(rawRows[sourceIndex])
      if (!row) return failed()
      const idServer = stableMessageIdentity(row.idServer)
      const time = Number(row.time)
      if (!idServer || !Number.isFinite(time) || time <= 0) return failed()
      const rawType = row.type
      const content = typeof row.content === 'string'
        ? row.content
        : row.content === null || row.content === undefined
          ? ''
          : JSON.stringify(row.content) ?? String(row.content)
      projected.push({
        idServer,
        status: clean(row.status),
        type: typeof rawType === 'number' || typeof rawType === 'string' ? rawType : String(rawType ?? ''),
        from: clean(row.from),
        content,
        contentWasString: typeof row.content === 'string',
        time,
        sourceIndex,
      })
    }
    projected.sort((left, right) => left.time - right.time || left.sourceIndex - right.sourceIndex)
    const seen = new Set<string>()
    const rows: SnapshotRow[] = []
    const sourceKeys: string[] = []
    for (const row of projected) {
      if (seen.has(row.idServer)) continue
      seen.add(row.idServer)
      rows.push(row)
      sourceKeys.push(await digest(`source-v1|${row.idServer}`))
    }
    // 基线最后一条在当前 timeline 中的位置；其后各条都是本次动作之后的新增。
    // 无基线（独立只读面）或空基线时为 -1，即全部行都算“基线之后”。
    let baselineEnd = -1
    if (baselineServerSourceKeys !== null) {
      if (expectedTargetBindingToken === null ||
          !/^[0-9a-f]{64}$/u.test(expectedTargetBindingToken) ||
          await digest(JSON.stringify([conversationRef, target])) !== expectedTargetBindingToken ||
          baselineServerSourceKeys.length > 64 ||
          baselineServerSourceKeys.some((key) => !/^[0-9a-f]{64}$/u.test(key)) ||
          new Set(baselineServerSourceKeys).size !== baselineServerSourceKeys.length) {
        return failed()
      }
      // 基线之后允许新增 0..N 条：acceptWechat 的正证已改为可见后置状态，
      // 259 可能晚到甚至本轮不到（协议规格 §9.3）。基线仍必须是当前 timeline
      // 里的连续块、且其后全部是新增，页面重载、历史跳变与目标换绑照旧拒绝。
      if (baselineServerSourceKeys.length > 0) {
        const lastBaselineKey = baselineServerSourceKeys[baselineServerSourceKeys.length - 1]
        const anchorPositions = sourceKeys
          .map((key, index) => key === lastBaselineKey ? index : -1)
          .filter((index) => index >= 0)
        if (anchorPositions.length !== 1) return failed()
        baselineEnd = anchorPositions[0]
        const blockStart = baselineEnd - baselineServerSourceKeys.length + 1
        if (blockStart < 0) return failed()
        // 基线未被截断(<64)时它就是当时的全部历史，必须仍从头对齐。
        if (baselineServerSourceKeys.length < 64 && blockStart !== 0) return failed()
        if (!baselineServerSourceKeys.every((key, index) => sourceKeys[blockStart + index] === key)) {
          return failed()
        }
      }
    } else if (expectedTargetBindingToken !== null) {
      return failed()
    }
    const requestIndexes = sourceKeys
      .map((sourceKey, index) => sourceKey === requestSourceKey ? index : -1)
      .filter((index) => index >= 0)
    if (requestIndexes.length !== 1) return failed()
    const requestIndex = requestIndexes[0]
    const candidateRequest = rowShape(rows[requestIndex], 105, 2, target).matches
    const staffRequest = rowShape(rows[requestIndex], 105, 1, staffID).matches
    if (candidateRequest === staffRequest) return failed()
    const origin: 1 | 2 = candidateRequest ? 2 : 1
    // 路由、目标绑定与请求锚都已复核通过，此处才允许带出请求卡的可见后置状态。
    // 判据全部按可见文本：仍可点的“同意”动作 vs 已转入的“复制微信号”形态；
    // .imc-wx-request 只作卡片定位。计数不携带任何页面文本。
    const sessionDetails = baselineServerSourceKeys === null
      ? []
      : Array.from(document.querySelectorAll<HTMLElement>('.im-session-detail')).filter(visible)
    // 只有接受流程（带发送基线）才需要 surface；独立 readonly 面不看页面卡片。
    // 会话面板不唯一时也不给出 surface：缺席即“未观察到”，SW 侧只会继续等待
    // 直至预算耗尽转人工，永远不会据此判成功。
    const requestCards = sessionDetails.length === 1
      ? Array.from(sessionDetails[0].querySelectorAll<HTMLElement>('.imc-wx-request')).filter(visible)
      : null
    const surface = requestCards === null ? undefined : {
      pendingRequestCards: requestCards.filter((card) => Array.from(
        card.querySelectorAll<HTMLElement>('.imc-wx-request__actions-success, button, a'),
      ).some((node) => visible(node) && /同意/u.test(clean(node.textContent)) &&
        !/不同意|拒绝|婉拒|忽略/u.test(clean(node.textContent)))).length,
      copyWechatCards: requestCards.filter(
        (card) => /复制微信号/u.test(clean(card.textContent)),
      ).length,
    }
    const unconfirmed = (): MainWechatExchangeOutcomeResult =>
      routeMatches() && targetForCurrentRoute() === target && surface !== undefined
        ? { confirmed: false, surface }
        : failed()
    let end = rows.length
    for (let index = requestIndex + 1; index < rows.length; index += 1) {
      const envelope = parseObject(rows[index].content)
      const type = Number(
        typeof rows[index].type === 'number' || /^\d+$/u.test(String(rows[index].type))
          ? rows[index].type
          : envelope.type,
      )
      if (type === 105) {
        end = index
        break
      }
    }
    // 结果消息恒归属点同意的一方：候选人发起(origin=2)时是我方点同意，259
    // 归我方(out)；我方发起(origin=1)时是候选人点同意，259 归对方(in)。
    // 2026-07-28 生产页面双样本直读，见协议规格 §9.3。
    const outcomeFrom = origin === 2 ? staffID : target
    const matches: Array<{ index: number; peerWechat: string }> = []
    for (let index = requestIndex + 1; index < end; index += 1) {
      const outcome = rowShape(rows[index], 259, origin, outcomeFrom)
      const peerWechat = clean(outcome.details.userWeChat)
      const ownWechat = clean(outcome.details.staffWeChat)
      if (outcome.matches && peerWechat && ownWechat &&
          peerWechat.length <= 256 &&
          new TextEncoder().encode(peerWechat).length <= 1024) {
        matches.push({ index, peerWechat })
      }
    }
    if (matches.length !== 1) return unconfirmed()
    const match = matches[0]
    // 接受流程里 259 必须严格晚于基线，即确由本次动作产生；基线之前的
    // 已有交换在 evaluator 阶段就已拒绝派发。
    if (baselineServerSourceKeys !== null && match.index <= baselineEnd) return unconfirmed()
    // digest 期间若真人切走或 target 换绑，阳性必须降为未确认。
    if (!routeMatches() || targetForCurrentRoute() !== target) return failed()
    const positive: MainWechatExchangeOutcomeResult = {
      confirmed: true,
      exchangeSourceKey: sourceKeys[match.index],
      peerWechat: match.peerWechat,
    }
    return surface === undefined ? positive : { ...positive, surface }
  } catch {
    return failed()
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
  let currentWindow: MainListDOMWindowResult
  try {
    // 字面复用 readList 的同一 MAIN 窗口读取器。数据库事实或独立 API
    // 分页都不能在这里重新制造一个页面导航目标。
    currentWindow = await runMain(tab.id, mainReadListDOMWindow, [false, false])
  } catch (error) {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      `无法读取当前可定位会话窗口：${asError(error).message}`,
      'manualOnly',
    )
  }
  const matches = currentWindow.sessions
    .filter((session) => session.conversationRef === conversationRef)
  if (currentWindow.unstable || matches.length === 0) {
    // readList 观察到的行可能在 readThread 切换前因列表实时重排而离开
    // 当前虚拟窗口。此时尚未 click，也没有开始平台历史读取；把它明确
    // 表达为无副作用的陈旧页面目标，让脑结束本轮并从下一轮列表重读。
    throw new ZhilianPlatformError(
      'TARGET_NOT_FOUND',
      '目标会话已离开本轮聊天列表窗口',
      'no',
    )
  }
  if (matches.length !== 1) {
    throw new ZhilianPlatformError(
      'ELEMENT_UNRESOLVED',
      '当前可定位会话窗口内目标身份不唯一',
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

async function mainCancelPreparedInterviewEditor(): Promise<void> {
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
  const wait = (delayMs: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, delayMs))
  try {
    const modals = Array.from(
      document.querySelectorAll<HTMLElement>('.km-modal__wrapper.interview-modal'),
    ).filter((node) => visible(node) && node.querySelector('.interview-form') !== null)
    if (modals.length !== 1) return
    const modal = modals[0]
    const buttons = Array.from(
      modal.querySelectorAll<HTMLElement>('button[type="button"], a[type="button"]'),
    ).filter((node) => visible(node) && clean(node.textContent) === '取消')
    if (buttons.length !== 1) return
    await wait(1_000 + Math.floor(Math.random() * 501))
    if (!buttons[0].isConnected || !visible(buttons[0])) return
    const intrinsicClick = HTMLElement.prototype.click as (this: HTMLElement) => void
    const invokeClick = Function.prototype.call.bind(intrinsicClick, buttons[0])
    invokeClick()
    await wait(1_000)
    const deadline = Date.now() + 10_000
    while (Date.now() <= deadline && modal.isConnected && visible(modal)) await wait(120)
  } catch {
    // 清理只尽力而为；原失败仍由调用方按原语语义返回。
  }
}

// 2026-07-27 真机：邀面卡发出后平台弹出含"面试邀请已发出"的成功弹窗
// （本次观察为 interview-success-modal + 右上 .km-modal__close-btn），带全屏
// 遮罩、会拦截后续页面操作；老项目另记录过 2026-07 改版的服务号推广弹窗形态
// （真机未见，不实现类名依赖）。按文本锚 best-effort 关闭：找不到、关不掉都
// 只如实返回，不重试发送、不改变已取得的发送结果。
async function mainCloseInterviewSuccessModal(): Promise<{ found: boolean; closed: boolean }> {
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
  const wait = (delayMs: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, delayMs))
  const findModals = (): HTMLElement[] => Array.from(document.querySelectorAll<HTMLElement>(
    '.km-modal__wrapper, .km-modal, [class*="modal"], [role="dialog"]',
  )).filter((node) => visible(node) && /面试邀请已发出/u.test(clean(node.textContent)))
  try {
    const modals = findModals()
    if (modals.length === 0) return { found: false, closed: false }
    let closeButton: HTMLElement | null = null
    for (const modal of modals) {
      closeButton =
        Array.from(modal.querySelectorAll<HTMLElement>('.km-modal__close-btn')).find(visible) ??
        Array.from(modal.querySelectorAll<HTMLElement>('[class*="close"]')).find(visible) ?? null
      if (closeButton) break
    }
    if (!closeButton) return { found: true, closed: false }
    await wait(1_000 + Math.floor(Math.random() * 501))
    if (!closeButton.isConnected || !visible(closeButton)) {
      return { found: true, closed: findModals().length === 0 }
    }
    const intrinsicClick = HTMLElement.prototype.click as (this: HTMLElement) => void
    const invokeClick = Function.prototype.call.bind(intrinsicClick, closeButton)
    invokeClick()
    const deadline = Date.now() + 3_000
    while (Date.now() <= deadline) {
      if (findModals().length === 0) return { found: true, closed: true }
      await wait(120)
    }
    return { found: true, closed: false }
  } catch {
    return { found: false, closed: false }
  }
}

// 邀面编辑器准备失败的统一错误映射。sendZhilianCard 与 probeZhilianInterviewEditor
// 共用同一 mainPrepareInterviewEditor 与同一失败语义,不各自维护副本。
function throwInterviewPreparationFailure(
  preparation: Exclude<MainPrepareInterviewEditorResult, { status: 'ready' }>,
): never {
  if (preparation.reason === 'composer_nonempty') {
    throw new ZhilianPlatformError('USER_ACTIVE', '邀面准备期间出现人工草稿，已取消编辑器', 'afterRecovery')
  }
  if (preparation.reason === 'identity_changed') {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '邀面准备期间登录身份发生变化', 'manualOnly')
  }
  if (preparation.reason === 'route_changed' || preparation.reason === 'target_changed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '邀面准备期间目标会话发生变化', 'manualOnly')
  }
  if (preparation.reason === 'action_window_elapsed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '邀面准备动作窗口已过', 'manualOnly')
  }
  // detail 是手侧白名单现场快照（abort 点标记+命中数+我方自设值回读）；
  // 契约 ErrorBody.message 上限 500 字符/2000 字节，这里再做一道防御截断。
  const prepareDetail = preparation.detail === undefined ? '' : `（${preparation.detail}）`
  let prepareMessage = `邀面编辑器无法精确准备：${preparation.reason}${prepareDetail}`.slice(0, 480)
  while (new TextEncoder().encode(prepareMessage).length > 1900) {
    prepareMessage = prepareMessage.slice(0, -16)
  }
  throw new ZhilianPlatformError(
    preparation.reason === 'input_rejected' ? 'GUARD_FAILED' : 'ELEMENT_UNRESOLVED',
    prepareMessage,
    'manualOnly',
  )
}

function throwCardEvaluationFailure(evaluation: MainSendCardOnceResult): never {
  if (evaluation.status !== 'failed') {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '卡片发送 evaluator 返回未知状态', 'manualOnly')
  }
  if (evaluation.reason === 'composer_nonempty') {
    throw new ZhilianPlatformError('USER_ACTIVE', '发送前输入框出现人工草稿，已取消卡片发送', 'afterRecovery')
  }
  if (evaluation.reason === 'target_changed' || evaluation.reason === 'baseline_changed') {
    throw new ZhilianPlatformError('GUARD_FAILED', '卡片发送前目标绑定或消息基线发生变化', 'manualOnly')
  }
  if (evaluation.reason === 'route_changed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '卡片发送前目标会话发生切换', 'manualOnly')
  }
  if (evaluation.reason === 'identity_changed') {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '卡片发送前登录身份发生变化', 'manualOnly')
  }
  if (evaluation.reason === 'action_window_elapsed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '卡片不可逆动作窗口已过', 'manualOnly')
  }
  throw new ZhilianPlatformError(
    evaluation.reason === 'input_rejected' ? 'GUARD_FAILED' : 'ELEMENT_UNRESOLVED',
    `卡片发送前页面无法精确确认：${evaluation.reason}`,
    'manualOnly',
  )
}

async function sendZhilianCard(
  conversationRef: string,
  cardKind: MainCardAction,
  interview: InterviewDetails | null,
  guards: ZhilianSendGuards,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<{
  conversationRef: string
  contentHash: string
  sourceKey: string
  interview?: InterviewDetails
  observedAt: number
}> {
  if (!expectedPrincipalFingerprint) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  if (cardKind === 'interviewInvite' && (
    interview === null || !Number.isSafeInteger(interview.startsAt) ||
    !Number.isSafeInteger(interview.endsAt) || interview.startsAt <= 0 ||
    interview.endsAt <= interview.startsAt || interview.method !== 'wechatVideo' ||
    interview.startsAt % 60_000 !== 0 ||
    (interview.endsAt - interview.startsAt) % 60_000 !== 0 ||
    ![15, 30, 45, 60].includes((interview.endsAt - interview.startsAt) / 60_000)
  )) {
    throw new ZhilianPlatformError('GUARD_FAILED', '邀面时间或方式不受当前页面能力支持', 'manualOnly')
  }
  if (cardKind === 'wechatInvite' && interview !== null) {
    throw new ZhilianPlatformError('GUARD_FAILED', '换微信邀请不得携带邀面参数', 'manualOnly')
  }
  const tab = await sendZhilianTab(conversationRef)
  if (tab.id === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  const rawBaseline = await runMain(tab.id, mainCaptureSendBaseline, [
    conversationRef,
    guards.expectedTail,
  ])
  const baseline = validatedMainSendBaseline(rawBaseline)
  if (!baseline) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '卡片发送基线返回结构无效', 'afterRecovery', 'pageBroken')
  }
  if (baseline.status === 'failed') {
    if (baseline.stage === 'route_changed' || baseline.stage === 'guard_snapshot_uncovered') {
      throw new ZhilianPlatformError('GUARD_FAILED', '卡片发送基线在复核期间发生变化', 'manualOnly')
    }
    throw new ZhilianPlatformError('CTX_NOT_READY', '当前无法建立可信卡片发送基线', 'afterRecovery', 'pageBroken')
  }

  let editorPrepared = false
  let finalActionStarted = false
  const cleanupEditor = async (): Promise<void> => {
    if (!editorPrepared || finalActionStarted) return
    try {
      await runMain(tab.id as number, mainCancelPreparedInterviewEditor, [])
    } catch {
      // best effort：清理失败不能覆盖原始原语失败。
    }
    editorPrepared = false
  }
  try {
    if (cardKind === 'interviewInvite') {
      const preparation = await runMain(tab.id, mainPrepareInterviewEditor, [
        conversationRef,
        interview as InterviewDetails,
        expectedPrincipalFingerprint,
        ctx.irreversibleNotAfterMs,
      ])
      if (preparation.status !== 'ready') {
        throwInterviewPreparationFailure(preparation)
      }
      editorPrepared = true
    } else {
      // 与此前可能的人工页面交互留出全局约定的最小随机节奏。
      await new Promise((resolve) => setTimeout(resolve, 1_000 + Math.floor(Math.random() * 501)))
    }

    const evaluatorArgs = [
      conversationRef,
      cardKind,
      interview,
      null,
      guards.expectedTail,
      expectedPrincipalFingerprint,
      ctx.irreversibleNotAfterMs,
      baseline.serverSourceKeys,
      baseline.targetBindingToken,
    ] as const
    ctx.checkpoint()
    const preflight = await runMain(tab.id, mainSendCardOnce, [...evaluatorArgs, 'preflight'])
    if (preflight.status !== 'ready') {
      await cleanupEditor()
      throwCardEvaluationFailure(preflight)
    }
    ctx.checkpoint()
    await ctx.beforeSideEffect()
    finalActionStarted = true
    const action = await runMain(tab.id, mainSendCardOnce, [...evaluatorArgs, 'commit'])
    if (action.status !== 'clicked') {
      finalActionStarted = false
      await cleanupEditor()
      throwCardEvaluationFailure(action)
    }

    const expectedHash = cardKind === 'wechatInvite'
      ? await sha256Hex('card\x1fwechatExchange')
      : await sha256Hex(
          `card\x1finterviewInvite\x1f${interview?.startsAt}\x1f${interview?.endsAt}\x1fwechatVideo`,
        )
    for (let attempt = 0; attempt < 40; attempt += 1) {
      ctx.checkpoint()
      try {
        const observed = await runMain(tab.id, mainObserveStableOutboundCard, [
          conversationRef,
          cardKind,
          interview,
          baseline.serverSourceKeys,
          baseline.targetBindingToken,
        ])
        if (!observed.selected) {
          throw new ZhilianPlatformError(
            'CTX_LOST_DURING_EXEC',
            '点击卡片发送后目标会话发生切换，无法确认后置条件',
            'manualOnly',
            undefined,
            'possible',
          )
        }
        if (observed.matchingNewServerMessages === 1 &&
            observed.contentHash === expectedHash &&
            typeof observed.sourceKey === 'string' && SHA256_HEX.test(observed.sourceKey) &&
            (cardKind !== 'interviewInvite' ||
              (observed.interview?.startsAt === interview?.startsAt &&
                observed.interview?.endsAt === interview?.endsAt &&
                observed.interview?.method === interview?.method))) {
          try {
            assertExpectedPrincipal(await probeTab(await chrome.tabs.get(tab.id)), expectedPrincipalFingerprint)
          } catch (error) {
            throw new ZhilianPlatformError(
              'CTX_LOST_DURING_EXEC',
              `点击卡片发送后账号身份无法复核：${asError(error).message}`,
              'manualOnly',
              undefined,
              'possible',
            )
          }
          const observedAt = Date.now()
          if (cardKind === 'interviewInvite') {
            try {
              const closeOutcome = await runMain(tab.id, mainCloseInterviewSuccessModal, [])
              console.info(
                '[RecruitHelper] interview_success_modal_close',
                closeOutcome.found,
                closeOutcome.closed,
              )
            } catch {
              // 弹窗清理只尽力而为，不影响已取得的发送正证。
            }
          }
          await ctx.progress('已从实时消息时间线确认唯一新已发卡片', 100)
          return {
            conversationRef,
            contentHash: observed.contentHash,
            sourceKey: observed.sourceKey,
            ...(observed.interview ? { interview: observed.interview } : {}),
            observedAt,
          }
        }
      } catch (error) {
        if (error instanceof ZhilianPlatformError && error.sideEffect === 'possible') throw error
        // 观察失败只能收敛为未确认，绝不能触发第二次候选人可见动作。
      }
      await new Promise((resolve) => setTimeout(resolve, 250))
    }
    throw new ZhilianPlatformError(
      'POSTCONDITION_UNCONFIRMED',
      '卡片只点击发送一次，但未确认严格新增一条匹配的服务端消息',
      'manualOnly',
      undefined,
      'possible',
    )
  } catch (error) {
    await cleanupEditor()
    if (finalActionStarted && cardKind === 'interviewInvite') {
      // commit 已发生：无论确认结果如何，成功弹窗都可能已弹出并遮挡页面，
      // 尽力关闭以免拖垮同页后续操作；失败不改变原始错误。
      try {
        await runMain(tab.id, mainCloseInterviewSuccessModal, [])
      } catch {
        // best effort
      }
    }
    throw error
  }
}

export async function sendZhilianWechatInvite(
  args: ZhilianSendWechatInviteArgs,
  guards: ZhilianSendGuards,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianSendWechatInviteData> {
  return await sendZhilianCard(
    args.conversationRef,
    'wechatInvite',
    null,
    guards,
    ctx,
    expectedPrincipalFingerprint,
  )
}

function throwWechatAcceptEvaluationFailure(evaluation: MainSendCardOnceResult): never {
  if (evaluation.status !== 'failed') {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '微信接受 evaluator 返回未知状态', 'manualOnly')
  }
  if (evaluation.reason === 'target_changed' || evaluation.reason === 'baseline_changed' ||
      evaluation.reason === 'input_rejected') {
    throw new ZhilianPlatformError('GUARD_FAILED', '微信请求锚、目标或消息基线已经变化', 'manualOnly')
  }
  if (evaluation.reason === 'route_changed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '接受微信请求前目标会话发生切换', 'manualOnly')
  }
  if (evaluation.reason === 'identity_changed') {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '接受微信请求前登录身份发生变化', 'manualOnly')
  }
  if (evaluation.reason === 'action_window_elapsed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '微信接受不可逆动作窗口已过', 'manualOnly')
  }
  // 定位失败的细分成因随 message 上行：本批没有待处理样本可预先验证判据，
  // 首次真机若定位失败，必须能直接判断该改哪一处，而不是重跑猜测。
  // detail 只含选择器命中数与我方自设常量，不含任何页面文本。
  throw new ZhilianPlatformError(
    'ELEMENT_UNRESOLVED',
    evaluation.detail === undefined
      ? '当前无法唯一确认仍待处理的微信请求及同意动作'
      : `当前无法唯一确认仍待处理的微信请求及同意动作(${evaluation.detail})`,
    'manualOnly',
  )
}

export async function acceptZhilianWechatRequest(
  args: ZhilianAcceptWechatArgs,
  guards: ZhilianSendGuards,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianAcceptWechatData> {
  if (!expectedPrincipalFingerprint) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  if (!/^[0-9a-f]{64}$/u.test(args.requestSourceKey)) {
    throw new ZhilianPlatformError('GUARD_FAILED', '微信请求缺少稳定来源锚', 'manualOnly')
  }
  const tab = await sendZhilianTab(args.conversationRef)
  if (tab.id === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  const rawBaseline = await runMain(tab.id, mainCaptureSendBaseline, [
    args.conversationRef,
    guards.expectedTail,
  ])
  const baseline = validatedMainSendBaseline(rawBaseline)
  if (!baseline) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '微信接受基线返回结构无效', 'afterRecovery', 'pageBroken')
  }
  if (baseline.status === 'failed') {
    if (baseline.stage === 'route_changed' || baseline.stage === 'guard_snapshot_uncovered') {
      throw new ZhilianPlatformError('GUARD_FAILED', '微信接受基线在复核期间发生变化', 'manualOnly')
    }
    throw new ZhilianPlatformError('CTX_NOT_READY', '当前无法建立可信微信接受基线', 'afterRecovery', 'pageBroken')
  }

  // 与此前可能的真人交互留出全局约定的最小随机节奏；此等待发生在
  // preflight 之前，不扩大最后一次绿色 evaluator 到唯一 click 的窗口。
  await new Promise((resolve) => setTimeout(resolve, 1_000 + Math.floor(Math.random() * 501)))
  const evaluatorArgs = [
    args.conversationRef,
    'wechatAccept' as const,
    null,
    args.requestSourceKey,
    guards.expectedTail,
    expectedPrincipalFingerprint,
    ctx.irreversibleNotAfterMs,
    baseline.serverSourceKeys,
    baseline.targetBindingToken,
  ] as const
  ctx.checkpoint()
  const preflight = await runMain(tab.id, mainSendCardOnce, [...evaluatorArgs, 'preflight'])
  if (preflight.status !== 'ready') throwWechatAcceptEvaluationFailure(preflight)
  ctx.checkpoint()
  await ctx.beforeSideEffect()
  const action = await runMain(tab.id, mainSendCardOnce, [...evaluatorArgs, 'commit'])
  if (action.status !== 'clicked') throwWechatAcceptEvaluationFailure(action)

  // 成功正证是同一张请求卡的可见后置状态（协议规格 §9.3）：唯一那张待处理卡
  // 的同意动作消失、且“复制微信号”形态较 click 前净增。259 只是可选加成，
  // 晚到或不到都不推翻正证，号由 chat.readWechatExchangeOutcome 延迟收编。
  const copyCardsBeforeClick = action.wechatCopyCards ?? 0
  const readOutcome = async (): Promise<MainWechatExchangeOutcomeResult> =>
    runMain(tab.id as number, mainReadWechatExchangeOutcome, [
      args.conversationRef,
      args.requestSourceKey,
      baseline.serverSourceKeys,
      baseline.targetBindingToken,
    ])
  let confirmation: MainWechatExchangeOutcomeResult | null = null
  for (let attempt = 0; attempt < 40; attempt += 1) {
    ctx.checkpoint()
    try {
      const observed = await readOutcome()
      if (observed.surface && observed.surface.pendingRequestCards === 0 &&
          observed.surface.copyWechatCards > copyCardsBeforeClick) {
        confirmation = observed
        break
      }
    } catch {
      // 观察失败只能收敛为未确认，绝不能补第二次“同意”。
    }
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  if (!confirmation) {
    throw new ZhilianPlatformError(
      'POSTCONDITION_UNCONFIRMED',
      '微信请求只点击同意一次，但未观察到请求卡的可见后置状态',
      'manualOnly',
      undefined,
      'possible',
    )
  }
  try {
    assertExpectedPrincipal(
      await probeTab(await chrome.tabs.get(tab.id)),
      expectedPrincipalFingerprint,
    )
  } catch (error) {
    throw new ZhilianPlatformError(
      'CTX_LOST_DURING_EXEC',
      `接受微信请求后账号身份无法复核：${asError(error).message}`,
      'manualOnly',
      undefined,
      'possible',
    )
  }

  // 可选加成：正证成立后再给 259 一段独立预算；取不到只是“无号成功”，
  // 号留给 chat.readWechatExchangeOutcome 延迟收编，绝不推翻正证。
  let harvested = confirmation
  for (let attempt = 0;
    attempt < 20 && !(harvested.confirmed && harvested.exchangeSourceKey && harvested.peerWechat);
    attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 250))
    ctx.checkpoint()
    try {
      harvested = await readOutcome()
    } catch {
      break
    }
  }
  const collected = harvested.confirmed && Boolean(harvested.exchangeSourceKey) &&
    Boolean(harvested.peerWechat)
  const data: ZhilianAcceptWechatData = collected
    ? {
        conversationRef: args.conversationRef,
        requestSourceKey: args.requestSourceKey,
        exchangeSourceKey: harvested.exchangeSourceKey,
        peerWechat: harvested.peerWechat,
        observedAt: Date.now(),
      }
    : {
        conversationRef: args.conversationRef,
        requestSourceKey: args.requestSourceKey,
        observedAt: Date.now(),
      }
  if (validatePrimitiveData(PrimitiveName.ChatAcceptWechat, 1, data).length !== 0) {
    throw new ZhilianPlatformError(
      'POSTCONDITION_UNCONFIRMED',
      '微信接受结果不符合当前契约',
      'manualOnly',
      undefined,
      'possible',
    )
  }
  await ctx.progress(collected ? '已确认接受并取得交换结果' : '已确认接受，交换结果待后续收编', 100)
  return data
}

export async function readZhilianWechatExchangeOutcome(
  args: ZhilianReadWechatExchangeOutcomeArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianReadWechatExchangeOutcomeData> {
  if (!expectedPrincipalFingerprint) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  if (!/^[0-9a-f]{64}$/u.test(args.requestSourceKey)) {
    throw new ZhilianPlatformError('GUARD_FAILED', '微信结果读取缺少稳定请求锚', 'manualOnly')
  }
  ctx.checkpoint()
  const tab = await uniqueVerifiedIMTab(expectedPrincipalFingerprint)
  if (tab.id === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  await assertCurrentThreadRoute(
    tab.id,
    args.conversationRef,
    expectedPrincipalFingerprint,
    'none',
  )
  const observed = await runMain(tab.id, mainReadWechatExchangeOutcome, [
    args.conversationRef,
    args.requestSourceKey,
    null,
    null,
  ])
  ctx.checkpoint()
  await assertCurrentThreadRoute(
    tab.id,
    args.conversationRef,
    expectedPrincipalFingerprint,
    'none',
  )
  const data: ZhilianReadWechatExchangeOutcomeData = observed.confirmed &&
      observed.exchangeSourceKey && observed.peerWechat
    ? {
        confirmed: true,
        exchangeSourceKey: observed.exchangeSourceKey,
        peerWechat: observed.peerWechat,
        observedAt: Date.now(),
      }
    : { confirmed: false, observedAt: Date.now() }
  if (validatePrimitiveData(PrimitiveName.ChatReadWechatExchangeOutcome, 1, data).length !== 0) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '微信交换结果结构不符合当前契约', 'manualOnly')
  }
  await ctx.progress(data.confirmed ? '已确认微信交换结果' : '本轮未确认微信交换结果', 100)
  return data
}

export async function sendZhilianInviteCard(
  args: ZhilianSendInviteCardArgs,
  guards: ZhilianSendGuards,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianSendInviteCardData> {
  const data = await sendZhilianCard(
    args.conversationRef,
    'interviewInvite',
    args.interview,
    guards,
    ctx,
    expectedPrincipalFingerprint,
  )
  if (!data.interview) {
    throw new ZhilianPlatformError(
      'POSTCONDITION_UNCONFIRMED',
      '已观察到邀面卡但缺少精确邀面参数',
      'manualOnly',
      undefined,
      'possible',
    )
  }
  return { ...data, interview: data.interview }
}

// 彩排取消后的只读复核：邀面弹窗是否仍可见。自包含，供 runMain 注入。
async function mainIsInterviewEditorOpen(): Promise<boolean> {
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  return Array.from(
    document.querySelectorAll<HTMLElement>('.km-modal__wrapper.interview-modal'),
  ).some((node) => visible(node) && node.querySelector('.interview-form') !== null)
}

// debug.probeInterviewEditor:邀面编辑器彩排。与 sendZhilianCard 的
// interviewInvite 前半段字面共用 mainPrepareInterviewEditor(全部选择框与
// 终核回读),准备成功后停留至少 5 秒供有人值守肉眼确认,再取消编辑器并
// 复核弹窗已关。构造性不含发送路径:不建基线、不评估 expectedTail、不调用
// mainSendCardOnce。失败沿用同一 throwInterviewPreparationFailure 映射。
export async function probeZhilianInterviewEditor(
  args: DebugProbeInterviewEditorArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<DebugProbeInterviewEditorData> {
  if (!expectedPrincipalFingerprint) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  const interview = args.interview
  if (!Number.isSafeInteger(interview.startsAt) || !Number.isSafeInteger(interview.endsAt) ||
      interview.startsAt <= 0 || interview.endsAt <= interview.startsAt ||
      interview.method !== 'wechatVideo' ||
      interview.startsAt % 60_000 !== 0 ||
      (interview.endsAt - interview.startsAt) % 60_000 !== 0 ||
      ![15, 30, 45, 60].includes((interview.endsAt - interview.startsAt) / 60_000)) {
    throw new ZhilianPlatformError('GUARD_FAILED', '邀面时间或方式不受当前页面能力支持', 'manualOnly')
  }
  const tab = await sendZhilianTab(args.conversationRef)
  if (tab.id === undefined) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  const preparation = await runMain(tab.id, mainPrepareInterviewEditor, [
    args.conversationRef,
    interview,
    expectedPrincipalFingerprint,
    ctx.irreversibleNotAfterMs,
  ])
  if (preparation.status !== 'ready') {
    throwInterviewPreparationFailure(preparation)
  }
  // 甲方裁决(2026-07-29):填毕停留至少 5 秒供肉眼确认。停留放在彩排编排层,
  // 与生产共用的准备函数不因此变化。
  await new Promise((resolve) => setTimeout(resolve, 5_000))
  try {
    await runMain(tab.id, mainCancelPreparedInterviewEditor, [])
  } catch {
    // 取消尽力而为,结果由下方只读复核如实上报。
  }
  let canceled = false
  try {
    canceled = !await runMain(tab.id, mainIsInterviewEditorOpen, [])
  } catch {
    canceled = false
  }
  return {
    conversationRef: args.conversationRef,
    dateValue: preparation.prepared.dateValue,
    timeValue: preparation.prepared.timeValue,
    durationValue: preparation.prepared.durationValue,
    methodValue: preparation.prepared.methodValue,
    canceled,
  }
}

export async function readZhilianThread(
  args: ZhilianThreadArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<ZhilianThreadPage> {
  if (!expectedPrincipalFingerprint) {
    throw new ZhilianPlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  const requireCurrent = args.requireCurrent === true
  const tab = requireCurrent
    ? await uniqueVerifiedIMTab(expectedPrincipalFingerprint)
    : await verifiedIMTab(expectedPrincipalFingerprint)
  if (tab.id === undefined) throw new ZhilianPlatformError('CTX_NOT_READY', '标签页缺少 id', 'afterRecovery', 'pageBroken')
  if (requireCurrent) {
    await assertCurrentThreadRoute(
      tab.id,
      args.conversationRef,
      expectedPrincipalFingerprint,
      'none',
    )
  }
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
    requireCurrent,
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
  const routeConsumedBarrier = requireCurrent
    ? false
    : await ensureThreadRoute(
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
    if (requireCurrent) {
      await assertCurrentThreadRoute(
        tab.id,
        args.conversationRef,
        expectedPrincipalFingerprint,
        'possible',
      )
    }
    try {
      page = await runMain(tab.id, mainReadThreadPage, [
        args.conversationRef,
        Math.min(THREAD_PAGE_SIZE, maxMessages - collected.length),
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
    if (requireCurrent) {
      await assertCurrentThreadRoute(
        tab.id,
        args.conversationRef,
        expectedPrincipalFingerprint,
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

  if (requireCurrent) {
    await assertCurrentThreadRoute(
      tab.id,
      args.conversationRef,
      expectedPrincipalFingerprint,
      platformReadStarted ? 'possible' : 'none',
    )
  } else {
    assertExpectedPrincipal(await probeTab(tab), expectedPrincipalFingerprint)
  }
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

// ============================================================================
// 成功取证截图(chat.captureThreadScreenshot / candidate.captureResumeScreenshot)
//
// 技术路线照抄旧项目复盘结论:页面内 foreignObject→canvas 在 Chrome 上固有污染、
// 导不出 JPEG,唯一可行路线是 SW 里 chrome.tabs.captureVisibleTab 截真实渲染像素、
// 按内容偏移滚动拼接成单张长图。新架构下原语在 SW 执行,循环反转为:
//   SW 循环 { executeScript(量测/滚动) → captureVisibleTab → OffscreenCanvas 裁剪合成 }
// executeScript 之间无法持有 DOM 引用,每步都按同一确定性规则重解析滚动容器,
// 以冻结坐标系核对(高度/落点漂移 ≤4px)兜住"重解析到别的节点"与页面自身搅动。
// 一切异常只产生"缺图"失败,绝不发送白图/错位图。
// ============================================================================

// 页面步进结果:纯数值快照,DOM 细节不出页面。
interface MainCaptureStepReady {
  status: 'ready'
  visible: boolean
  clientH: number
  scrollHeight: number
  scrollTop: number
  dpr: number
  innerW: number
  innerH: number
  rectTop: number
  rectBottom: number
  rectLeft: number
  rectRight: number
}
interface MainCaptureStepFailed {
  status: 'failed'
  reason: 'route_changed' | 'container_unresolved'
}
type MainCaptureStepResult = MainCaptureStepReady | MainCaptureStepFailed

// 必须自包含:executeScript 序列化到 MAIN world,不得引用模块闭包。
// op: measure(解析+入视口) | pinTop(钉顶,驱动懒加载排干) | anchorBottom(底部锚定)
//     | scrollTo(滚到 requestedTop) | readback(纯读,不动滚动)
// 聊天滚动容器解析规则移植自旧项目 getZhilianImChatScreenshotTarget +
// resolveScrollContainer:km-scrollbar 结构真正可滚的是 __wrap;__view(内容层)与
// 根(overflow:hidden)写 scrollTop 是 no-op,直接驱动会拼出"首屏+大片留白"的坏图。
async function mainChatCaptureStep(
  conversationRef: string,
  op: string,
  requestedTop: number,
): Promise<MainCaptureStepResult> {
  const failed = (reason: 'route_changed' | 'container_unresolved'): MainCaptureStepFailed =>
    ({ status: 'failed', reason })
  try {
    const route = new URL(location.href)
    if (route.pathname !== '/app/im' || route.searchParams.get('sessionId') !== conversationRef) {
      return failed('route_changed')
    }
  } catch {
    return failed('route_changed')
  }
  const isScrollableY = (el: HTMLElement): boolean => {
    const oy = window.getComputedStyle(el).overflowY
    return (oy === 'auto' || oy === 'scroll') && el.scrollHeight > el.clientHeight + 4
  }
  const resolveScrollContainer = (el: HTMLElement): HTMLElement => {
    const room = (n: HTMLElement | null): n is HTMLElement =>
      !!n && n.scrollHeight > n.clientHeight + 4
    const descWrap = el.querySelector<HTMLElement>('.km-scrollbar__wrap')
    if (room(descWrap)) return descWrap
    const ancWrap = el.closest<HTMLElement>('.km-scrollbar__wrap')
    if (room(ancWrap)) return ancWrap
    if (el.scrollHeight > el.clientHeight + 4) return el
    let best: HTMLElement | null = null
    let bestRoom = 80
    el.querySelectorAll<HTMLElement>('*').forEach((c) => {
      if (isScrollableY(c) && c.scrollHeight - c.clientHeight > bestRoom) {
        best = c
        bestRoom = c.scrollHeight - c.clientHeight
      }
    })
    return best || el
  }
  const timeline = document.querySelector<HTMLElement>('.im-timeline')
  const target =
    timeline?.closest<HTMLElement>('.km-scrollbar__view') ||
    timeline ||
    document.querySelector<HTMLElement>('.im-timeline__wrapper .km-scrollbar__view') ||
    document.querySelector<HTMLElement>('.im-timeline__wrapper')
  if (!target) return failed('container_unresolved')
  const scrollEl = resolveScrollContainer(target)
  if (op === 'measure') {
    scrollEl.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  } else if (op === 'pinTop') {
    scrollEl.scrollTop = 0
  } else if (op === 'anchorBottom') {
    scrollEl.scrollTop = Math.max(0, scrollEl.scrollHeight - Math.max(1, scrollEl.clientHeight))
  } else if (op === 'scrollTo') {
    scrollEl.scrollTop = Math.max(0, requestedTop)
  }
  const rect = scrollEl.getBoundingClientRect()
  return {
    status: 'ready',
    visible: document.visibilityState === 'visible',
    clientH: Math.max(1, scrollEl.clientHeight),
    scrollHeight: Math.max(scrollEl.scrollHeight, Math.max(1, scrollEl.clientHeight)),
    scrollTop: scrollEl.scrollTop,
    dpr: window.devicePixelRatio || 1,
    innerW: window.innerWidth,
    innerH: window.innerHeight,
    rectTop: rect.top,
    rectBottom: rect.bottom,
    rectLeft: rect.left,
    rectRight: rect.right,
  }
}

// SW 侧步进器:把一次页面操作绑定到具体 tab 与页面函数。
type CaptureStepRunner = (op: string, requestedTop?: number) => Promise<MainCaptureStepReady>

class CaptureDirty extends Error {
  constructor(kind: string) {
    super(`capture-stitch-dirty:${kind}`)
    this.name = 'CaptureDirty'
  }
}

const CAPTURE_MAX_SIDE = 16_000
const CAPTURE_TARGET_MAX_BYTES = 1_900_000 // 企微 image 原图上限 2MB,留余量(旧项目同值)
const CAPTURE_FRAME_QUALITY = 92

function swSleep(delayMs: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, delayMs))
}

// 平台交互节奏(2026-07-23 甲方裁决):相邻平台可见交互(含滚动)至少间隔 1 秒 + 有界抖动。
// 拼接的逐帧滚动、懒加载排干与锚定都是滚动交互,统一经此闸;纯读 readback 不占节奏。
class MutationPacer {
  private lastMutationAt = 0
  async beforeMutation(): Promise<void> {
    const gap = 1_000 + Math.floor(Math.random() * 301)
    const wait = this.lastMutationAt + gap - Date.now()
    if (wait > 0) await swSleep(wait)
    this.lastMutationAt = Date.now()
  }
}

interface StitchOutcome {
  jpeg: Blob
  width: number
  height: number
  frames: number
  truncated: boolean
}

// 懒加载排干+锚定等稳(移植旧 settleScrollContainer):
// 聊天滚到顶会触发平台 prepend 更早历史并顶开 scrollTop,开拍前必须先排干等稳,
// 否则坐标系中途平移必出坏图。连续 2 轮(各 ≥1s 观测窗)高度零增长视为到头;
// 历史超出帧预算可拍范围时提前停止排干。
async function settleForCapture(
  step: CaptureStepRunner,
  pacer: MutationPacer,
  ctx: PrimitiveContext,
  options: { anchor: 'top' | 'bottom'; maxFrames: number },
): Promise<MainCaptureStepReady> {
  let metrics = await step('measure')
  const budgetPx = (options.maxFrames + 1) * metrics.clientH
  let quietRounds = 0
  for (let round = 0; round < 20 && quietRounds < 2; round += 1) {
    if (metrics.scrollHeight >= budgetPx) break
    const before = metrics.scrollHeight
    ctx.checkpoint()
    await pacer.beforeMutation()
    await step('pinTop')
    await swSleep(300)
    metrics = await step('readback')
    quietRounds = metrics.scrollHeight === before ? quietRounds + 1 : 0
  }
  if (options.anchor === 'bottom') {
    await pacer.beforeMutation()
    metrics = await step('anchorBottom')
  } else {
    await pacer.beforeMutation()
    metrics = await step('pinTop')
  }
  let stableReads = 0
  let lastHeight = metrics.scrollHeight
  for (let read = 0; read < 8 && stableReads < 2; read += 1) {
    ctx.checkpoint()
    await swSleep(300)
    metrics = await step('readback')
    if (metrics.scrollHeight === lastHeight) {
      stableReads += 1
    } else {
      stableReads = 0
      lastHeight = metrics.scrollHeight
    }
  }
  return metrics
}

async function decodeFrame(dataUrl: string): Promise<ImageBitmap> {
  const response = await fetch(dataUrl)
  return await createImageBitmap(await response.blob())
}

async function canvasToJpeg(canvas: OffscreenCanvas, quality: number): Promise<Blob> {
  return await canvas.convertToBlob({ type: 'image/jpeg', quality: quality / 100 })
}

// 单次拼接(移植旧 stitchScrollElementOnce 的全部护栏):
// 开拍时冻结坐标系(clientH/totalScroll);逐帧核对高度与落点漂移(≤4px),成像前后
// 双读复核;首帧必须落画布顶端;多屏画布覆盖率过低判失败。发现坐标系失效抛
// CaptureDirty 由调用方整体重拍一次。
async function stitchOnce(
  tab: chrome.tabs.Tab,
  step: CaptureStepRunner,
  pacer: MutationPacer,
  ctx: PrimitiveContext,
  settled: MainCaptureStepReady,
  options: { anchor: 'top' | 'bottom'; maxFrames: number; progressLabel: string },
): Promise<StitchOutcome> {
  const dpr = settled.dpr
  const clientH = settled.clientH
  const totalScroll = Math.max(settled.scrollHeight, clientH)

  const framesForContent = Math.max(1, Math.ceil(totalScroll / clientH))
  const framesByCanvas = Math.max(1, Math.floor(CAPTURE_MAX_SIDE / Math.max(1, Math.round(clientH * dpr))))
  const budget = Math.min(options.maxFrames, framesForContent, framesByCanvas)
  const coveredCssH = Math.min(totalScroll, budget * clientH)
  const startTop = options.anchor === 'bottom' ? Math.max(0, totalScroll - coveredCssH) : 0

  const cropCssLeft = Math.max(0, settled.rectLeft)
  const cropCssRight = Math.min(settled.innerW, settled.rectRight)
  const cropCssW = Math.max(1, cropCssRight - cropCssLeft)

  const outW = Math.max(1, Math.min(CAPTURE_MAX_SIDE, Math.round(cropCssW * dpr)))
  const outH = Math.max(1, Math.min(CAPTURE_MAX_SIDE, Math.round(coveredCssH * dpr)))

  const canvas = new OffscreenCanvas(outW, outH)
  const draw = canvas.getContext('2d')
  if (!draw) throw new ZhilianPlatformError('CTX_NOT_READY', '截图画布不可用', 'afterRecovery')
  draw.fillStyle = '#ffffff'
  draw.fillRect(0, 0, outW, outH)

  let frames = 0
  let lastTop = -1
  let drawnBottom = 0
  let drawnTop = Number.POSITIVE_INFINITY
  for (let i = 0; i < budget; i += 1) {
    ctx.checkpoint()
    const maxScrollable = Math.max(0, totalScroll - clientH)
    const requestedTop = Math.max(0, Math.min(Math.round(startTop + i * clientH), maxScrollable))
    await pacer.beforeMutation()
    await step('scrollTo', requestedTop)
    await swSleep(150)
    const before = await step('readback')
    if (!before.visible) break // 中途切走标签:captureVisibleTab 会截到别的标签,用已拼部分
    if (Math.abs(before.scrollHeight - totalScroll) > 4) throw new CaptureDirty('height-changed')
    if (Math.abs(before.scrollTop - requestedTop) > 4) throw new CaptureDirty('scroll-drift')
    if (i > 0 && before.scrollTop <= lastTop) break // 滚不动了(不可滚/到底)
    lastTop = before.scrollTop

    let frame: ImageBitmap
    try {
      frame = await decodeFrame(await captureVisibleTabJpegDataUrl(tab.windowId, CAPTURE_FRAME_QUALITY))
    } catch {
      await swSleep(1_200) // 可能是 captureVisibleTab 配额超限,多等一轮再试一次
      try {
        frame = await decodeFrame(await captureVisibleTabJpegDataUrl(tab.windowId, CAPTURE_FRAME_QUALITY))
      } catch (secondError) {
        if (frames === 0) {
          throw new ZhilianPlatformError(
            'CTX_NOT_READY',
            `截屏原语一帧未得:${secondError instanceof Error ? secondError.message : String(secondError)}`,
            'afterRecovery',
          )
        }
        break
      }
    }

    // 成像前后双读:脏检查与实际成像之间隔着异步 IPC,该窗口被搅动的帧不可采信。
    const after = await step('readback')
    if (Math.abs(after.scrollTop - requestedTop) > 4 || Math.abs(after.scrollHeight - totalScroll) > 4) {
      frame.close()
      throw new CaptureDirty('frame-moved')
    }

    const vTop = Math.max(0, after.rectTop)
    const vBottom = Math.min(after.innerH, after.rectBottom)
    const vCssH = Math.max(0, vBottom - vTop)
    if (vCssH < 1) {
      frame.close()
      break
    }
    const sx = Math.min(Math.max(0, Math.round(cropCssLeft * dpr)), Math.max(0, frame.width - 1))
    const sy = Math.min(Math.max(0, Math.round(vTop * dpr)), Math.max(0, frame.height - 1))
    const sw = Math.max(1, Math.min(frame.width - sx, outW))
    const sh = Math.max(1, Math.min(frame.height - sy, Math.round(vCssH * dpr)))

    let destY = Math.round((before.scrollTop - startTop) * dpr)
    let srcSkip = 0
    if (destY < 0 && destY >= -Math.round(6 * dpr)) {
      // 高 DPI/分数缩放下 scrollTop 对齐物理像素栅格,落点可比起点低亚像素级:
      // 裁掉帧顶溢出的一两个像素照画,不丢整帧(丢帧会让 top-blank 检查永久判死)。
      srcSkip = -destY
      destY = 0
    }
    if (destY >= 0 && destY < outH && sh > srcSkip) {
      const drawH = Math.min(sh - srcSkip, outH - destY)
      draw.drawImage(frame, sx, sy + srcSkip, sw, drawH, 0, destY, sw, drawH)
      drawnBottom = Math.max(drawnBottom, destY + drawH)
      drawnTop = Math.min(drawnTop, destY)
    }
    frame.close()
    frames += 1
    await ctx.progress(`${options.progressLabel} ${frames}/${budget}`, Math.min(90, 20 + Math.round((frames / budget) * 65)))
    if (before.scrollTop + clientH >= totalScroll - 1) break // 已到底
  }

  // 一帧未画成(入口通过后被切走/容器滚出视口)→ 纯白画布,绝不当截图发出
  if (frames === 0 || drawnBottom === 0) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '截图一帧未成,放弃取证', 'manualOnly')
  }
  // 首帧必须画在画布顶端:顶部白带=首帧落点已被顶开(懒加载/自动滚底典型产物)
  if (drawnTop > Math.round(12 * dpr)) throw new CaptureDirty('top-blank')
  // 覆盖率安全网:多屏画布只画了首屏 = 滚动没生效/容器解析错,判失败不发白图
  if (outH > Math.round(clientH * dpr) * 1.5 && drawnBottom < outH * 0.4) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '截图大面积留白,放弃取证', 'manualOnly')
  }

  let quality = CAPTURE_FRAME_QUALITY
  let jpeg = await canvasToJpeg(canvas, quality)
  let finalW = outW
  let finalH = outH
  for (const stepQuality of [80, 65, 50, 38]) {
    if (jpeg.size <= CAPTURE_TARGET_MAX_BYTES) break
    quality = stepQuality
    jpeg = await canvasToJpeg(canvas, quality)
  }
  // 分辨率兜底:最低质量仍超限(长图含照片消息时可能)就整图等比缩小重编码;
  // 企微按 2MB 硬拒收,超限发出等于白拍,缩小后看清文字足够。
  for (const scale of [0.7, 0.5]) {
    if (jpeg.size <= CAPTURE_TARGET_MAX_BYTES) break
    const scaledW = Math.max(1, Math.round(outW * scale))
    const scaledH = Math.max(1, Math.round(outH * scale))
    const scaled = new OffscreenCanvas(scaledW, scaledH)
    const scaledDraw = scaled.getContext('2d')
    if (!scaledDraw) break
    scaledDraw.drawImage(canvas, 0, 0, scaledW, scaledH)
    jpeg = await canvasToJpeg(scaled, 65)
    finalW = scaledW
    finalH = scaledH
  }
  if (jpeg.size > CAPTURE_TARGET_MAX_BYTES) {
    throw new ZhilianPlatformError('PAYLOAD_LIMIT', '截图压缩后仍超过发送上限', 'manualOnly')
  }
  return {
    jpeg,
    width: finalW,
    height: finalH,
    frames,
    truncated: coveredCssH < totalScroll - 1 || frames < budget,
  }
}

// 拼接入口:排干等稳 → 单次拼接;坐标系中途失效(CaptureDirty)整体重拍一次。
// 重拍不是重演:上一轮已加载的历史留在 DOM,这一轮 settle 从更接近排干的状态继续。
async function stitchCapture(
  tab: chrome.tabs.Tab,
  step: CaptureStepRunner,
  ctx: PrimitiveContext,
  options: { anchor: 'top' | 'bottom'; maxFrames: number; progressLabel: string },
): Promise<StitchOutcome> {
  return await stitchCaptureWithPacer(tab, step, new MutationPacer(), ctx, options)
}

// 与调用方共享同一节奏闸的拼接入口(简历路径的打开/关闭点击也计入节奏)。
async function stitchCaptureWithPacer(
  tab: chrome.tabs.Tab,
  step: CaptureStepRunner,
  pacer: MutationPacer,
  ctx: PrimitiveContext,
  options: { anchor: 'top' | 'bottom'; maxFrames: number; progressLabel: string },
): Promise<StitchOutcome> {
  for (let attempt = 1; attempt <= 2; attempt += 1) {
    const settled = await settleForCapture(step, pacer, ctx, options)
    if (!settled.visible) {
      throw new ZhilianPlatformError('CTX_NOT_READY', '目标标签页不在前台,放弃截图', 'afterRecovery')
    }
    try {
      return await stitchOnce(tab, step, pacer, ctx, settled, options)
    } catch (error) {
      if (!(error instanceof CaptureDirty) || attempt >= 2) {
        if (error instanceof CaptureDirty) {
          throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', `截图期间页面持续变动(${error.message})`, 'manualOnly')
        }
        throw error
      }
      // 容器拍摄中途变动(懒加载 prepend/自动滚底/新气泡):重新排干等稳后整体重拍一次。
    }
  }
  throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '截图期间页面持续变动', 'manualOnly')
}

function captureStepFailure(result: MainCaptureStepFailed): never {
  if (result.reason === 'route_changed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '截图期间目标会话绑定无法确证', 'manualOnly')
  }
  throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '截图目标滚动容器无法解析', 'manualOnly')
}

async function uploadCaptureJpeg(outcome: StitchOutcome): Promise<CaptureScreenshotData> {
  let put: BlobPutOutcome
  try {
    put = await putSessionBlob(await outcome.jpeg.arrayBuffer())
  } catch (error) {
    if (error instanceof BlobChannelError) {
      throw new ZhilianPlatformError(
        error.permanent ? 'PAYLOAD_LIMIT' : 'CTX_NOT_READY',
        `截图 blob 上行失败:${error.message}`,
        error.permanent ? 'manualOnly' : 'afterRecovery',
      )
    }
    throw error
  }
  return {
    imageBlobRef: put.ref,
    byteSize: put.byteSize,
    truncated: outcome.truncated,
    capturedAt: Date.now(),
  }
}

interface MainResumeCaptureFailed {
  status: 'failed'
  reason:
    | 'route_changed'
    | 'target_changed'
    | 'container_unresolved'
    | 'stale_modal'
    | 'entry_unresolved'
    | 'modal_cardinality'
    | 'open_failed'
    | 'close_failed'
}
type MainResumeCaptureStepResult = MainCaptureStepReady | MainResumeCaptureFailed

// 简历截图的页面步进(自包含,serialize 到 MAIN world)。与 mainReadCurrentResume
// 共享同一批页面事实:入口「查看详情」、弹窗 .new-shortcut-resume__modal、
// 关闭 .new-shortcut-resume__close;滚动容器经 resolveScrollContainer 归一
// (简历:根 overflow:hidden,真滚动层是内层 .km-scrollbar__wrap)。
// op: open(点入口等弹窗) | measure | pinTop | scrollTo | readback | close
async function mainResumeCaptureStep(
  conversationRef: string,
  platformUserRef: string,
  op: string,
  requestedTop: number,
): Promise<MainResumeCaptureStepResult> {
  type AnyRecord = Record<string, unknown>
  const asRecord = (value: unknown): AnyRecord | null =>
    value !== null && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const clean = (value: unknown): string => String(value ?? '')
    .normalize('NFC').replace(/\u00a0/gu, ' ').replace(/\s+/gu, ' ').trim()
  const failed = (reason: MainResumeCaptureFailed['reason']): MainResumeCaptureFailed =>
    ({ status: 'failed', reason })
  const visible = (element: Element): boolean => {
    const node = element as HTMLElement
    const style = getComputedStyle(node)
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0
  }
  const visibleAll = (root: ParentNode, selector: string): HTMLElement[] =>
    Array.from(root.querySelectorAll<HTMLElement>(selector)).filter(visible)
  const sleep = (delayMs: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, delayMs))

  try {
    const route = new URL(location.href)
    if (route.pathname !== '/app/im' || route.searchParams.get('sessionId') !== conversationRef) {
      return failed('route_changed')
    }
  } catch {
    return failed('route_changed')
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
  const targetMatches = (): boolean => {
    const engine = asRecord((window as unknown as AnyRecord).imEngine)
    const sessions = engine && Array.isArray(engine.sessions)
      ? engine.sessions as AnyRecord[]
      : initialSessions()
    if (sessions === null) return false
    const matches = sessions.filter((item) => clean(item.sessionId) === conversationRef)
    return matches.length === 1 && clean(matches[0].peerPartnerId) === platformUserRef
  }
  if (!targetMatches()) return failed('target_changed')

  const isScrollableY = (el: HTMLElement): boolean => {
    const oy = window.getComputedStyle(el).overflowY
    return (oy === 'auto' || oy === 'scroll') && el.scrollHeight > el.clientHeight + 4
  }
  const resolveScrollContainer = (el: HTMLElement): HTMLElement => {
    const room = (n: HTMLElement | null): n is HTMLElement =>
      !!n && n.scrollHeight > n.clientHeight + 4
    const descWrap = el.querySelector<HTMLElement>('.km-scrollbar__wrap')
    if (room(descWrap)) return descWrap
    const ancWrap = el.closest<HTMLElement>('.km-scrollbar__wrap')
    if (room(ancWrap)) return ancWrap
    if (el.scrollHeight > el.clientHeight + 4) return el
    let best: HTMLElement | null = null
    let bestRoom = 80
    el.querySelectorAll<HTMLElement>('*').forEach((c) => {
      if (isScrollableY(c) && c.scrollHeight - c.clientHeight > bestRoom) {
        best = c
        bestRoom = c.scrollHeight - c.clientHeight
      }
    })
    return best || el
  }
  const metricsOf = (scrollEl: HTMLElement): MainCaptureStepReady => {
    const rect = scrollEl.getBoundingClientRect()
    return {
      status: 'ready',
      visible: document.visibilityState === 'visible',
      clientH: Math.max(1, scrollEl.clientHeight),
      scrollHeight: Math.max(scrollEl.scrollHeight, Math.max(1, scrollEl.clientHeight)),
      scrollTop: scrollEl.scrollTop,
      dpr: window.devicePixelRatio || 1,
      innerW: window.innerWidth,
      innerH: window.innerHeight,
      rectTop: rect.top,
      rectBottom: rect.bottom,
      rectLeft: rect.left,
      rectRight: rect.right,
    }
  }

  if (op === 'open') {
    if (visibleAll(document, '.new-shortcut-resume__modal').length !== 0) return failed('stale_modal')
    const details = visibleAll(document, '.im-session-detail')
    if (details.length !== 1) return failed('entry_unresolved')
    const detail = details[0]
    const entries = visibleAll(detail, '.hover-resume-footer__button, button, a, [role="button"]')
      .filter((element) => clean(element.textContent) === '查看详情' &&
        element.closest('.im-session-detail') === detail)
    if (entries.length !== 1) return failed('entry_unresolved')
    entries[0].click()
    const waitUntil = Date.now() + 6_000
    let modals: HTMLElement[] = []
    while (Date.now() < waitUntil) {
      modals = visibleAll(document, '.new-shortcut-resume__modal')
      if (modals.length !== 0) break
      await sleep(120)
    }
    if (modals.length !== 1) return failed(modals.length === 0 ? 'open_failed' : 'modal_cardinality')
    return metricsOf(resolveScrollContainer(modals[0]))
  }

  const modals = visibleAll(document, '.new-shortcut-resume__modal')
  if (modals.length !== 1) return failed(modals.length === 0 ? 'open_failed' : 'modal_cardinality')
  const modal = modals[0]

  if (op === 'close') {
    const closeButtons = visibleAll(modal, '.new-shortcut-resume__close')
    if (closeButtons.length !== 1) return failed('close_failed')
    closeButtons[0].click()
    const closeUntil = Date.now() + 10_000
    while (Date.now() < closeUntil) {
      if (visibleAll(document, '.new-shortcut-resume__modal').length === 0) {
        const anchor = document.scrollingElement || document.documentElement
        return metricsOf(anchor as HTMLElement)
      }
      await sleep(120)
    }
    return failed('close_failed')
  }

  const roots = visibleAll(modal, '.resume-detail')
  if (roots.length !== 1) return failed('container_unresolved')
  const scrollEl = resolveScrollContainer(modal)
  if (op === 'measure') {
    // 弹窗自身固定居中,无需 scrollIntoView;仅量测。
  } else if (op === 'pinTop') {
    scrollEl.scrollTop = 0
  } else if (op === 'scrollTo') {
    scrollEl.scrollTop = Math.max(0, requestedTop)
  }
  return metricsOf(scrollEl)
}

function resumeCaptureStepFailure(result: MainResumeCaptureFailed): never {
  if (result.reason === 'route_changed' || result.reason === 'target_changed') {
    throw new ZhilianPlatformError('CTX_LOST_DURING_EXEC', '简历截图期间目标会话绑定无法确证', 'manualOnly')
  }
  if (result.reason === 'close_failed') {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '简历弹窗未能关闭还原', 'manualOnly')
  }
  throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', `简历截图目标无法解析(${result.reason})`, 'manualOnly')
}

// candidate.captureResumeScreenshot@1:打开与 candidate.readResume 同款的简历
// 详情弹窗,顶部锚定拼接后关闭还原。ok = 图已上行且弹窗已关闭;任何失败只产生
// "缺图",关闭在失败路径上也尽力执行,绝不静默留下残余弹窗遮挡后续原语。
export async function captureZhilianResumeScreenshot(
  args: CandidateCaptureResumeScreenshotArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<CaptureScreenshotData> {
  if (!args || typeof args.conversationRef !== 'string' || !args.conversationRef ||
      typeof args.platformUserRef !== 'string' || !args.platformUserRef) {
    throw new ZhilianPlatformError('GUARD_FAILED', '简历截图缺少目标会话或候选人引用', 'manualOnly')
  }
  if (!sessionBlobParams()) {
    throw new ZhilianPlatformError('PAYLOAD_LIMIT', '当前会话未协商 blob 通道,禁止内联图像', 'manualOnly')
  }
  ctx.checkpoint()
  const tab = await sendZhilianTab(args.conversationRef)
  if (tab.id === undefined || tab.status !== 'complete') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '目标智联会话页面尚未就绪', 'afterRecovery', 'pageBroken')
  }
  if (!tab.active) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '目标会话标签页不在前台,放弃截图', 'afterRecovery')
  }
  const probe = await probeTab(tab)
  if (!probe.contentScriptOk || probe.pageKind !== 'im') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联 IM 页面感知通道尚未就绪', 'afterRecovery', 'contentScriptDead')
  }
  assertExpectedPrincipal(probe, expectedPrincipalFingerprint)
  await ctx.progress('简历截图准备', 10)

  const tabId = tab.id
  const step: CaptureStepRunner = async (op, requestedTop = 0) => {
    const result = await runMain(tabId, mainResumeCaptureStep, [
      args.conversationRef,
      args.platformUserRef,
      op,
      requestedTop,
    ])
    if (result.status === 'failed') resumeCaptureStepFailure(result)
    return result
  }

  const pacer = new MutationPacer()
  await pacer.beforeMutation()
  const opened = await step('open')
  if (!opened.visible) {
    await closeResumeModalBestEffort(step, pacer, Date.now())
    throw new ZhilianPlatformError('CTX_NOT_READY', '目标标签页不在前台,放弃截图', 'afterRecovery')
  }
  const openedAt = Date.now()

  let outcome: StitchOutcome
  try {
    outcome = await stitchCaptureWithPacer(tab, step, pacer, ctx, {
      anchor: 'top',
      maxFrames: 16,
      progressLabel: '简历截图',
    })
  } catch (error) {
    await closeResumeModalBestEffort(step, pacer, openedAt)
    throw error
  }
  // 打开简历后必须至少停留 2 秒(交互节奏裁决);拼接通常已远超,此处兜底早退路径。
  const stayUntil = openedAt + 2_000 + Math.floor(Math.random() * 501)
  if (Date.now() < stayUntil) await swSleep(stayUntil - Date.now())
  await pacer.beforeMutation()
  await step('close')

  const data = await uploadCaptureJpeg(outcome)
  if (validatePrimitiveData(PrimitiveName.CandidateCaptureResumeScreenshot, 1, data).length !== 0) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '截图结果不符合当前契约', 'manualOnly')
  }
  ctx.checkpoint()
  assertExpectedPrincipal(await probeTab(await chrome.tabs.get(tabId)), expectedPrincipalFingerprint)
  await ctx.progress('简历截图完成', 100)
  return data
}

// 失败路径的弹窗还原:满足最短停留后尽力关闭;二次异常只吞掉(主错误优先上报)。
async function closeResumeModalBestEffort(
  step: CaptureStepRunner,
  pacer: MutationPacer,
  openedAt: number,
): Promise<void> {
  try {
    const stayUntil = openedAt + 2_000 + Math.floor(Math.random() * 501)
    if (Date.now() < stayUntil) await swSleep(stayUntil - Date.now())
    await pacer.beforeMutation()
    await step('close')
  } catch {
    // 弹窗状态交由有人值守兜底;主失败原因已在调用方抛出。
  }
}

// chat.captureThreadScreenshot@1:当前 tracked 会话聊天区长图(底部锚定)。
// 尽力而为的降级型感知:任何失败只产生"缺图",不推进业务状态、不授权重试 effectful。
export async function captureZhilianThreadScreenshot(
  args: ChatCaptureThreadScreenshotArgs,
  ctx: PrimitiveContext,
  expectedPrincipalFingerprint: string | undefined,
): Promise<CaptureScreenshotData> {
  if (!args || typeof args.conversationRef !== 'string' || !args.conversationRef) {
    throw new ZhilianPlatformError('GUARD_FAILED', '聊天截图缺少目标会话引用', 'manualOnly')
  }
  if (!sessionBlobParams()) {
    throw new ZhilianPlatformError('PAYLOAD_LIMIT', '当前会话未协商 blob 通道,禁止内联图像', 'manualOnly')
  }
  ctx.checkpoint()
  const tab = await sendZhilianTab(args.conversationRef)
  if (tab.id === undefined || tab.status !== 'complete') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '目标智联会话页面尚未就绪', 'afterRecovery', 'pageBroken')
  }
  if (!tab.active) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '目标会话标签页不在前台,放弃截图', 'afterRecovery')
  }
  const probe = await probeTab(tab)
  if (!probe.contentScriptOk || probe.pageKind !== 'im') {
    throw new ZhilianPlatformError('CTX_NOT_READY', '智联 IM 页面感知通道尚未就绪', 'afterRecovery', 'contentScriptDead')
  }
  assertExpectedPrincipal(probe, expectedPrincipalFingerprint)
  await ctx.progress('聊天截图准备', 10)

  const tabId = tab.id
  const step: CaptureStepRunner = async (op, requestedTop = 0) => {
    const result = await runMain(tabId, mainChatCaptureStep, [args.conversationRef, op, requestedTop])
    if (result.status === 'failed') captureStepFailure(result)
    return result
  }
  const outcome = await stitchCapture(tab, step, ctx, {
    anchor: 'bottom',
    maxFrames: 16,
    progressLabel: '聊天截图',
  })
  const data = await uploadCaptureJpeg(outcome)
  if (validatePrimitiveData(PrimitiveName.ChatCaptureThreadScreenshot, 1, data).length !== 0) {
    throw new ZhilianPlatformError('ELEMENT_UNRESOLVED', '截图结果不符合当前契约', 'manualOnly')
  }
  ctx.checkpoint()
  assertExpectedPrincipal(await probeTab(await chrome.tabs.get(tabId)), expectedPrincipalFingerprint)
  await ctx.progress('聊天截图完成', 100)
  return data
}

// 仅导出纯解析/游标函数供 Node 用同一份生产代码做脱敏 fixture 测试；生产 bundle
// 没有引用时由 esbuild tree-shake，不形成第二条运行路径。
export const zhilianTestHooks = Object.freeze({
  bindingHash,
  decodeCursor,
  encodeCursor,
  mainProbeZhilian,
  identifyZhilianCurrentConversation,
  mainReadCurrentCandidate,
  mainReadGreetingListTarget,
  mainReadCurrentResume,
  mainChatCaptureStep,
  mainResumeCaptureStep,
  mainSelectSourcingPosition,
  mainApplySourcingFilters,
  mainReadSourcingWindow,
  mainReadSourcingResume,
  mainEnsureChatListFilter,
  mainReadListDOMWindow,
  mainSendGreetingOnce,
  mainCaptureSendBaseline,
  mainInspectSendSurface,
  mainFindConversation,
  mainClickConversationOnce,
  mainObserveStableOutbound,
  mainObserveStableOutboundCard,
  mainReadWechatExchangeOutcome,
  mainPrepareInterviewEditor,
  mainCloseInterviewSuccessModal,
  mainReadThreadPage,
  mainSendCardOnce,
  mainSendMessageOnce,
  selectZhilianSourcingPosition,
  applyZhilianSourcingFilters,
  ensureThreadRoute,
  runMain,
})
