// 客户端(脑)UI 的数据层:访问本地脑服务的 admin 端点。纯 fetch,可在 Node 下单测。
// 测试页与调度器共用同一命令通道,这里不提供任何测试旁路。

declare global {
  interface Window {
    recruitHelper?: {
      adminBase?: string
      adminToken?: string
    }
  }
}

const preloadConfig = typeof window !== 'undefined' ? window.recruitHelper : undefined

export const ADMIN_BASE = preloadConfig?.adminBase
  || (typeof localStorage !== 'undefined' && localStorage.getItem('adminBase'))
  || 'http://127.0.0.1:17872'

// token 只从 Electron preload 的内存桥读取，不进入 localStorage、URL 或导出值。
const adminToken = preloadConfig?.adminToken || ''

function authorizationHeaders(): Record<string, string> {
  return adminToken ? { Authorization: `Bearer ${adminToken}` } : {}
}

export type TimeValue = string | number | null

async function readResponse<T>(response: Response, path: string): Promise<T> {
  const text = await response.text()
  let body: unknown
  if (text) {
    try {
      body = JSON.parse(text)
    } catch {
      throw new Error(`${path}: 脑返回了无法识别的数据`)
    }
  }
  if (!response.ok) {
    const detail = body && typeof body === 'object' && 'error' in body
      ? String((body as { error: unknown }).error)
      : `HTTP ${response.status}`
    throw new Error(detail)
  }
  return (body ?? {}) as T
}

async function get<T>(path: string): Promise<T> {
  const response = await fetch(ADMIN_BASE + path, { headers: authorizationHeaders() })
  return readResponse<T>(response, path)
}

// 普通产品页只允许读取独立的 /app/* 投影。产品数据不写入浏览器持久存储，
// bearer 仍只来自 Electron preload 的内存桥。
export async function appGet<T>(path: string): Promise<T> {
  if (!path.startsWith('/app/')) {
    throw new Error('产品读取只允许访问 /app/*')
  }
  return get<T>(path)
}

async function post<T>(path: string, body?: unknown): Promise<T> {
  const response = await fetch(ADMIN_BASE + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authorizationHeaders() },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  return readResponse<T>(response, path)
}

function query(path: string, values: Record<string, string | undefined>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(values)) {
    if (value) params.set(key, value)
  }
  const encoded = params.toString()
  return encoded ? `${path}?${encoded}` : path
}

export interface Health {
  ok: boolean
  proto: number
  contract: string
  activeHands: string[]
}

export interface HandHealth {
  handId: string
  online: boolean
  health: string
  caps: string[]
  bootId: string
  contractHash: string
  contractMatch: boolean
  extensionVersion: string
  lastHbAgoMs: number
  witnessReady: boolean
  outboxPending: number
  journalOpen: number
}

export interface ReloadHandView {
  ready: boolean
  handId: string
  msgId: string
  previousBootId: string
  bootId: string
  contractHash: string
  extensionVersion: string
}

export interface LedgerRow {
  msgId: string
  name: string
  class: string
  status: string
  attempt: number
  errorCode?: string
}

export interface Suspect {
  msgId: string
  name: string
  handId: string
  reason: string
  idemKey: string
  reviewReady: boolean
  reviewAfter?: number
  verificationAttempts: number
}

export interface FrameEvent {
  seq: number
  dir: string
  kind: string
  handId: string
  msgId: string
  ref?: string
  ts: number
}

export interface PatrolRoundView {
  roundId: string
  trigger: string
  status: string
  stage: string
  newMessageCount: number
  errorCode?: string
  startedAt: TimeValue
  finishedAt: TimeValue
}

export interface AccountView {
  platform: string
  accountRef: string
  handId: string
  handOnline: boolean
  identityState: string
  identityCurrent: boolean
  enabledToday: boolean
  enabledDate: string
  pausedReason: string
  nextPatrolAt: TimeValue
  lastPatrolAt: TimeValue
  manualQuietUntil: TimeValue
  dirtyHint: boolean
  pageHealth: string
  sensorHealth: string
  unreadTotal: number | null
  latestRound: PatrolRoundView | null
}

export interface ConversationView {
  conversationRef: string
  peerDisplayName: string
  unreadCount: number
  lastMessageDirection: string
  lastMessageKind: string
  lastMessagePreview: string
  lastActivityMs: number | null
  trackingState: string
  adoptedBoundarySeq: number
  lastMessageSeq: number
  lastSyncedAt: TimeValue
}

export interface MessageView {
  seq: number
  direction: string
  kind: string
  text: string | null
  cardType: string
  cardState: string
  tsApproxMs: number | null
  origin: string
  firstSeenRoundId: string
}

export interface AuditView {
  id: number | string
  at: TimeValue
  category: string
  conversationRef: string
  roundId: string
  detail: unknown
}

export interface MutationResult {
  ok?: boolean
  error?: string
  account?: AccountView
  trackingState?: string
  roundId?: string
}

export interface M5AIContextView {
  contextId: string
  revisionHash: string
  displayName: string
  environment: string
  documentCount: number
}

export interface M5ProviderConfigView {
  provider: string
  model: string
  baseUrlConfigured: boolean
  keyConfigured: boolean
  request_timeout_ms: number
  max_input_tokens: number
  max_intent_output_tokens: number
  max_reply_output_tokens: number
}

export interface M5ProviderConfigInput {
  provider: 'deepseek'
  model: 'deepseek-v4-pro'
  base_url: string
  api_key: string
  request_timeout_ms: 30000
  max_input_tokens: 16000
  max_intent_output_tokens: 64
  max_reply_output_tokens: 512
}

export interface JobConfigSourceView {
  configured: boolean
  baseUrlConfigured: boolean
  machineIdConfigured: boolean
  licenseTokenConfigured: boolean
  machineIdentityReady: boolean
  machineMatch: boolean
  customerName?: string
  customerStatus?: string
}

export interface JobConfigActivationInput {
  base_url: string
  invite_code: string
}

export interface JobConfigActivationResult {
  activated: boolean
  synced: boolean
  status: string
  customer: {
    id?: number
    name?: string
    status?: string
    subscription_ends_at?: string
  }
  contexts: M5AIContextView[]
  syncError?: string
}

export interface SendIntentView {
  intentId: string
  logicalDispatchId: string
  msgId: string
  status: string
  created?: boolean
  commandStatus?: string
  verificationAttempts?: number
  suspectReason?: string
}

export type CandidateContactState = 'unestablished' | 'established' | 'unknown'

// M4 人工建档只向 UI 暴露确认所需的最小视图。平台用户 ID、职位 ID 等
// 原始引用留在脑侧命令账本，不能穿过这个数据层进入组件状态。
export interface CandidateCurrentPreview {
  selectionRef: string
  displayName: string | null
  positionTitle: string | null
  contactState: CandidateContactState
}

export interface CandidateProfileSelectionView {
  profileId: string
  status: string
  created: boolean
}

export const CANDIDATE_READ_ERROR = '未能读取当前候选人，请确认页面只打开了一份候选人详情后重试。'
export const CANDIDATE_SELECT_ERROR = '未能确认候选人，请重新读取页面后再试。'

export class SendIntentConflictError extends Error {
  readonly current: SendIntentView

  constructor(message: string, current: SendIntentView) {
    super(message)
    this.name = 'SendIntentConflictError'
    this.current = current
  }
}

// 只有脑明确返回“请求在创建 intent/cmd 前被拒绝”时才使用此错误。
// 网络中断、5xx 或已创建回执仍属于结果不确定，不得解除当前意图或重铸 intentId。
export class SendIntentRejectedError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'SendIntentRejectedError'
  }
}

async function postSendMessage(body: {
  intentId: string
  previousIntentId: string
  platform: string
  accountRef: string
  conversationRef: string
  text: string
}): Promise<SendIntentView> {
  const path = '/admin/messages/send'
  const response = await fetch(ADMIN_BASE + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authorizationHeaders() },
    body: JSON.stringify(body),
  })
  if (response.status === 409) {
    const conflict = await readResponse<{ error?: string; current?: SendIntentView }>(
      new Response(await response.text(), { status: 200 }),
      path,
    )
    if (conflict.current?.intentId) {
      throw new SendIntentConflictError(conflict.error || '发送账本已出现更新', conflict.current)
    }
    throw new SendIntentRejectedError(conflict.error || '发送前安全检查未通过')
  }
  if (response.status === 400 || response.status === 404) {
    const rejected = await readResponse<{ error?: string }>(
      new Response(await response.text(), { status: 200 }),
      path,
    )
    throw new SendIntentRejectedError(rejected.error || '发送请求在创建意图前被拒绝')
  }
  return readResponse<SendIntentView>(response, path)
}

async function latestSendIntent(platform: string, accountRef: string, conversationRef: string): Promise<SendIntentView | null> {
  const path = query('/admin/messages/send', { platform, accountRef, conversationRef })
  const response = await fetch(ADMIN_BASE + path, { headers: authorizationHeaders() })
  if (response.status === 404) return null
  return readResponse<SendIntentView>(response, path)
}

async function postCandidateGreeting(body: {
  intentId: string
  previousIntentId: string
  profileId: string
  text: string
}): Promise<SendIntentView> {
  const path = '/admin/candidates/greeting/send'
  const response = await fetch(ADMIN_BASE + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authorizationHeaders() },
    body: JSON.stringify(body),
  })
  if (response.status === 409) {
    const conflict = await readResponse<{ error?: string; current?: SendIntentView }>(
      new Response(await response.text(), { status: 200 }),
      path,
    )
    if (conflict.current?.intentId) {
      throw new SendIntentConflictError(conflict.error || '招呼账本已出现更新', conflict.current)
    }
    throw new SendIntentRejectedError(conflict.error || '招呼前安全检查未通过')
  }
  if (response.status === 400 || response.status === 404) {
    const rejected = await readResponse<{ error?: string }>(
      new Response(await response.text(), { status: 200 }),
      path,
    )
    throw new SendIntentRejectedError(rejected.error || '招呼请求在创建意图前被拒绝')
  }
  return readResponse<SendIntentView>(response, path)
}

async function latestGreetingIntent(profileId: string): Promise<SendIntentView | null> {
  const path = query('/admin/candidates/greeting/send', { profileId })
  const response = await fetch(ADMIN_BASE + path, { headers: authorizationHeaders() })
  if (response.status === 404) return null
  return readResponse<SendIntentView>(response, path)
}

async function consumeFrameStream(signal: AbortSignal, onFrame: (frame: FrameEvent) => void): Promise<void> {
  const response = await fetch(ADMIN_BASE + '/admin/frames', {
    headers: { Accept: 'text/event-stream', ...authorizationHeaders() },
    signal,
  })
  if (!response.ok) throw new Error(`/admin/frames: HTTP ${response.status}`)
  if (!response.body) throw new Error('/admin/frames: 浏览器不支持流式读取')

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  while (!signal.aborted) {
    const { done, value } = await reader.read()
    buffer += decoder.decode(value, { stream: !done })
    let boundary = buffer.search(/\r?\n\r?\n/)
    while (boundary >= 0) {
      const block = buffer.slice(0, boundary)
      const separator = buffer.slice(boundary).match(/^\r?\n\r?\n/)?.[0].length ?? 2
      buffer = buffer.slice(boundary + separator)
      const data = block.split(/\r?\n/)
        .filter((line) => line.startsWith('data:'))
        .map((line) => line.slice(5).trimStart())
        .join('\n')
      if (data) {
        try { onFrame(JSON.parse(data) as FrameEvent) } catch { /* 畸形观测帧不污染界面 */ }
      }
      boundary = buffer.search(/\r?\n\r?\n/)
    }
    if (done) return
  }
}

function subscribeFrames(onFrame: (frame: FrameEvent) => void, onError?: (message: string) => void): () => void {
  const controller = new AbortController()
  let stopped = false
  let retryTimer: number | undefined
  const connect = async () => {
    try {
      await consumeFrameStream(controller.signal, onFrame)
    } catch (reason) {
      if (!controller.signal.aborted) onError?.(errorMessage(reason))
    }
    if (!stopped) retryTimer = window.setTimeout(connect, 1500)
  }
  void connect()
  return () => {
    stopped = true
    controller.abort()
    if (retryTimer !== undefined) window.clearTimeout(retryTimer)
  }
}

function errorMessage(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason)
}

interface AccountTarget {
  platform: string
  accountRef: string
}

function objectValue(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

function decodeCandidatePreview(value: unknown): CandidateCurrentPreview | null {
  const candidate = objectValue(value)
  if (!candidate || typeof candidate.selectionRef !== 'string' || !candidate.selectionRef) return null
  if (candidate.displayName !== null && typeof candidate.displayName !== 'string') return null
  if (candidate.positionTitle !== null && typeof candidate.positionTitle !== 'string') return null
  if (!['unestablished', 'established', 'unknown'].includes(String(candidate.contactState))) return null
  return {
    selectionRef: candidate.selectionRef,
    displayName: candidate.displayName,
    positionTitle: candidate.positionTitle,
    contactState: candidate.contactState as CandidateContactState,
  }
}

function decodeCandidateSelection(value: unknown): CandidateProfileSelectionView | null {
  const profile = objectValue(value)
  if (!profile || typeof profile.profileId !== 'string' || !profile.profileId) return null
  if (typeof profile.status !== 'string' || !profile.status || typeof profile.created !== 'boolean') return null
  return { profileId: profile.profileId, status: profile.status, created: profile.created }
}

async function readCurrentCandidate(target: AccountTarget): Promise<CandidateCurrentPreview> {
  try {
    const value = await post<unknown>('/admin/candidates/current/read', target)
    const preview = decodeCandidatePreview(value)
    if (!preview) throw new Error('invalid candidate preview')
    return preview
  } catch {
    // 平台原始引用或内部失败细节可能出现在服务端诊断中；UI 边界只给固定提示。
    throw new Error(CANDIDATE_READ_ERROR)
  }
}

async function selectCurrentCandidate(selectionRef: string): Promise<CandidateProfileSelectionView> {
  try {
    const value = await post<unknown>('/admin/candidates/current/select', { selectionRef })
    const profile = decodeCandidateSelection(value)
    if (!profile) throw new Error('invalid candidate selection')
    return profile
  } catch {
    throw new Error(CANDIDATE_SELECT_ERROR)
  }
}

export const api = {
  health: () => get<Health>('/admin/health'),
  handsHealth: () => get<{ hands: HandHealth[] }>('/admin/hands/health'),
  reloadHand: (handId: string) => post<ReloadHandView>('/admin/hands/reload', { handId }),
  dispatch: (handId: string, name: string, args: unknown) => post<{ msgId?: string; error?: string }>('/admin/cmd', { handId, name, args }),
  ledger: () => get<{ ledger: LedgerRow[] }>('/admin/ledger'),
  suspects: () => get<{ suspects: Suspect[] }>('/admin/suspects'),
  verdict: (msgId: string, verdict: 'resolvedOk' | 'resolvedFailed') => post<{ error?: string }>('/admin/suspects/verdict', { msgId, verdict }),
  subscribeFrames,

  accounts: () => get<{ accounts: AccountView[] }>('/admin/accounts'),
  bindAccount: (platform: string, handId: string, accountRef?: string) => post<MutationResult>('/admin/accounts/bind', {
    platform, handId, ...(accountRef ? { accountRef } : {}),
  }),
  enableAccount: (target: AccountTarget) => post<MutationResult>('/admin/accounts/enable', target),
  stopAccount: (target: AccountTarget) => post<MutationResult>('/admin/accounts/stop', target),
  pauseAccount: (target: AccountTarget) => post<MutationResult>('/admin/accounts/pause', target),
  runAccount: (target: AccountTarget) => post<MutationResult>('/admin/accounts/run', target),
  processCurrentConversationOnce: (target: AccountTarget) => (
    post<MutationResult>('/admin/conversations/current/process-once', target)
  ),
  readCurrentCandidate,
  selectCurrentCandidate,
  sendGreeting: (intentId: string, previousIntentId: string, profileId: string, text: string) => (
    postCandidateGreeting({ intentId, previousIntentId, profileId, text })
  ),
  greetingStatus: (intentId: string) => get<SendIntentView>(query('/admin/candidates/greeting/send', { intentId })),
  latestGreetingIntent,
  conversations: (platform: string, accountRef: string) => get<{ conversations: ConversationView[] }>(query('/admin/conversations', { platform, accountRef })),
  trackConversation: (platform: string, accountRef: string, conversationRef: string) => post<MutationResult>('/admin/conversations/track', { platform, accountRef, conversationRef }),
  selectM5Trial: (platform: string, accountRef: string, conversationRef: string) => post<MutationResult>('/admin/m5/trial/select', {
    platform, accountRef, conversationRef,
  }),
  importM5Contexts: (bundle: Record<string, unknown>) => post<unknown>('/admin/m5/contexts/import', { bundle }),
  m5Contexts: () => get<{ contexts: M5AIContextView[] }>('/admin/m5/contexts'),
  jobConfigSource: () => get<{ config: JobConfigSourceView }>('/admin/job-config/source'),
  activateJobConfigSource: (input: JobConfigActivationInput) => post<JobConfigActivationResult>('/admin/job-config/activate', input),
  syncCurrentJobConfig: () => post<{ contexts: M5AIContextView[] }>('/admin/job-config/sync-current', {}),
  bindM5Context: (contextId: string, revisionHash: string) => post<MutationResult>('/admin/m5/context-binding', {
    contextId, revisionHash,
  }),
  m5ProviderConfig: () => get<{ config: M5ProviderConfigView }>('/admin/m5/provider-config'),
  saveM5ProviderConfig: (config: M5ProviderConfigInput) => post<{ config: M5ProviderConfigView }>('/admin/m5/provider-config', config),
  messages: (platform: string, accountRef: string, conversationRef: string) => get<{ messages: MessageView[] }>(query('/admin/messages', { platform, accountRef, conversationRef })),
  sendMessage: (
    intentId: string, previousIntentId: string, platform: string,
    accountRef: string, conversationRef: string, text: string,
  ) => postSendMessage({ intentId, previousIntentId, platform, accountRef, conversationRef, text }),
  sendStatus: (intentId: string) => get<SendIntentView>(query('/admin/messages/send', { intentId })),
  latestSendIntent,
  audits: (platform: string, accountRef: string) => get<{ audits: AuditView[] }>(query('/admin/audits', { platform, accountRef })),
}
