import type {
  CandidateActionView,
  CandidateDecisionView,
  CandidateMessageView,
  CandidateView,
  CandidateViewItem,
  ConfirmationBatchView,
  ConfirmationCandidateView,
  ConfirmationSendState,
  FunnelStageKey,
  FunnelStageView,
  ProductConnectionView,
  ProductData,
  ProductMetric,
  WorkflowView,
} from './types'
import { createEmptyProductData } from './fixtures'

export interface AppMetricRaw {
  value: number | null
  exact: boolean
  unavailableReason?: string
}

export interface AppJobRaw {
  available: boolean
  backendJobId?: string
  name?: string
  environment?: string
  syncStatus: string
  lastSyncedAt?: string | null
}

export interface AppFunnelRaw {
  available: boolean
  batchId?: string
  stage?: string
  targetCount: number
  capturedCount: number
  scoredCount: number
  selectedCount: number
  greetingReady: number
  pendingConfirm: number
  sentCount: number
  generationFailedCount: number
  sendFailedCount: number
  suspectCount: number
  lastFailureReason?: string
  startedAt?: string | null
  finishedAt?: string | null
}

export interface AppOverviewStatisticsRaw {
  todayRated: AppMetricRaw
  todayConfirmation: AppMetricRaw
  todayGreeted: AppMetricRaw
  todayInvited: AppMetricRaw
  totalGreeted: AppMetricRaw
  totalInterviewed: AppMetricRaw
  totalWechat: AppMetricRaw
  todayNewReplies: AppMetricRaw
  todayNewAppointments: AppMetricRaw
  todayElapsedInterviews: AppMetricRaw
}

export interface AppInterviewRaw {
  profileId: string
  displayName: string
  jobName?: string
  startsAtMs: number
  endsAtMs?: number | null
  method?: string
  state?: string
}

export interface AppOverviewRaw {
  job: AppJobRaw
  funnel: AppFunnelRaw
  statistics: AppOverviewStatisticsRaw
  todayInterviews: AppInterviewRaw[]
  businessSince?: string | null
  refreshedAt: string
}

export interface AppRuntimeRaw {
  available: boolean
  customerName?: string
  customerStatus?: string
  authorized: boolean
  providerConfigured: boolean
  provider?: string
  model?: string
  pluginOnline: boolean
  pluginHealth?: string
  pluginVersion?: string
  contractMatch: boolean
  businessWindowOpen: boolean
  workflowMode?: string
  workflowStatus?: string
  canAddBatch: boolean
  canEnd: boolean
  workflowPendingAction?: 'sourcing' | 'end'
  communicationState?: string
}

export interface AppOverviewResponse {
  overview: AppOverviewRaw
  runtime: AppRuntimeRaw
}

export interface AppConfirmationCandidateRaw {
  profileId: string
  displayName: string
  jobName?: string
  score: number | null
  greetingText?: string
  status: string
  selectable: boolean
  failure?: string
}

export interface AppConfirmationRaw {
  available: boolean
  ready: boolean
  reason?: string
  batchId?: string
  jobName?: string
  createdAt?: string | null
  scoredCount: number
  selectedCount: number
  generatedCount: number
  generationFailed: number
  generationPending: number
  selectableCount: number
  candidates: AppConfirmationCandidateRaw[]
}

export interface AppConfirmationResponse {
  confirmation: AppConfirmationRaw
}

export interface AppCandidateListItemRaw {
  profileId: string
  displayName: string
  jobName?: string
  status: string
  endReason?: string
  lastMessagePreview?: string
  lastActivityAtMs?: number | null
  unreadCount: number
  manualRequired: boolean
  manualReason?: string
  wechat?: string | null
  wechatObservedAtMs?: number | null
  interviewStartsAtMs?: number | null
  interviewEndsAtMs?: number | null
  interviewMethod?: string | null
  interviewCardState?: string
}

export interface AppCandidateListRaw {
  view: 'communicating' | 'interviewed' | 'interviewElapsed' | 'wechat'
  total: number
  items: AppCandidateListItemRaw[]
  limit: number
  offset: number
}

export interface AppCandidateListResponse {
  candidates: AppCandidateListRaw
}

export interface AppResumeFieldRaw {
  label: string
  value: string
}

export interface AppResumeRaw {
  available: boolean
  basic: AppResumeFieldRaw[]
  expectations: AppResumeFieldRaw[]
  selfEvaluation?: string
  education?: string
  workExperiences?: string
  truncated: boolean
}

export interface AppMessageRaw {
  seq: number
  direction: string
  kind: string
  text?: string | null
  cardType?: string
  cardState?: string
  tsApproxMs?: number | null
  interviewStartsAtMs?: number | null
  interviewEndsAtMs?: number | null
  interviewMethod?: string | null
}

export interface AppAIJudgementRaw {
  available: boolean
  status?: string
  intentLabel?: string
  intentSource?: string
  failure?: string
  classifiedAt?: string | null
}

export interface AppActionRaw {
  kind: string
  status: string
  failure?: string
  createdAt?: string | null
}

export interface AppCandidateDetailRaw {
  candidate: AppCandidateListItemRaw
  resume: AppResumeRaw
  messages: AppMessageRaw[]
  latestAi: AppAIJudgementRaw
  actions: AppActionRaw[]
}

export interface AppCandidateDetailResponse {
  candidate: AppCandidateDetailRaw
}

export interface AppReadSnapshot {
  overview: AppOverviewResponse
  confirmation: AppConfirmationResponse
  candidates: Record<CandidateView, AppCandidateListResponse>
}

const stageOrder: FunnelStageKey[] = ['collect', 'score', 'select', 'greeting', 'confirm', 'send']
const stageLabels: Record<FunnelStageKey, string> = {
  collect: '采集',
  score: '评分',
  select: '筛选',
  greeting: '生成招呼语',
  confirm: '等待确认',
  send: '发送招呼',
}

const funnelStageAliases: Record<string, FunnelStageKey> = {
  collecting: 'collect',
  scoring: 'score',
  selecting: 'select',
  generatingGreetings: 'greeting',
  awaitingConfirmation: 'confirm',
  sendingGreetings: 'send',
}

const candidateViewToAPI: Record<CandidateView, AppCandidateListRaw['view']> = {
  communicating: 'communicating',
  interviewed: 'interviewed',
  interviewElapsed: 'interviewElapsed',
  wechat: 'wechat',
}

export function productCandidatePath(view: CandidateView): string {
  return `/app/candidates?view=${candidateViewToAPI[view]}&limit=200`
}

export function adaptProductSnapshot(snapshot: AppReadSnapshot, now = new Date()): ProductData {
  const empty = createEmptyProductData()
  const { overview: rawOverview, runtime } = snapshot.overview
  const businessWindowOpen = runtime.businessWindowOpen
  const customerName = clean(runtime.customerName) || (
    runtime.available
      ? runtime.authorized ? '当前客户' : '尚未激活'
      : '客户状态暂不可读取'
  )
  const job = adaptJob(rawOverview.job)
  const workflow = adaptWorkflow(runtime, businessWindowOpen)
  const funnel = adaptFunnel(rawOverview.funnel)
  const confirmation = adaptConfirmation(snapshot.confirmation.confirmation, {
    businessWindowOpen,
    workflowPaused: workflow.state === 'paused' || workflow.state === 'waitingDailyWindow',
  })
  const candidates = {
    communicating: adaptCandidateList(snapshot.candidates.communicating, 'communicating', now),
    interviewed: adaptCandidateList(snapshot.candidates.interviewed, 'interviewed', now),
    interviewElapsed: adaptCandidateList(
      snapshot.candidates.interviewElapsed,
      'interviewElapsed',
      now,
    ),
    wechat: adaptCandidateList(snapshot.candidates.wechat, 'wechat', now),
  }
  const candidateTotals = {
    communicating: candidateTotal(snapshot.candidates.communicating, candidates.communicating),
    interviewed: candidateTotal(snapshot.candidates.interviewed, candidates.interviewed),
    interviewElapsed: candidateTotal(
      snapshot.candidates.interviewElapsed,
      candidates.interviewElapsed,
    ),
    wechat: candidateTotal(snapshot.candidates.wechat, candidates.wechat),
  }
  const statistics = rawOverview.statistics

  return {
    customer: {
      name: customerName,
      shortName: Array.from(customerName)[0] ?? '客',
      authorizationLabel: authorizationLabel(runtime),
      authorized: runtime.authorized,
      activationRequired: runtime.available && !runtime.authorized,
      job,
    },
    overview: {
      dateLabel: formatDateHeading(now),
      refreshedAt: formatRelativeDateTime(rawOverview.refreshedAt, now),
      businessWindowLabel: '运行时间 08:00～24:00',
      businessWindowOpen,
      workflow,
      funnel,
      communication: adaptCommunication(runtime.communicationState, businessWindowOpen),
      todayMetrics: [
        { label: 'AI 评级人数', value: metricValue(statistics.todayRated), tone: 'blue' },
        { label: '候选确认人数', value: metricValue(statistics.todayConfirmation), tone: 'amber' },
        { label: '打招呼', value: metricValue(statistics.todayGreeted), tone: 'green' },
        { label: '已邀面', value: metricValue(statistics.todayInvited), tone: 'red' },
      ],
      ledgerStartedAt: formatDateOnly(rawOverview.businessSince),
      ledger: [
        { label: '累计招呼', value: metricValue(statistics.totalGreeted) },
        { label: '累计已约面', value: metricValue(statistics.totalInterviewed) },
        { label: '累计已换微信', value: metricValue(statistics.totalWechat) },
      ],
      todayInterviews: (rawOverview.todayInterviews ?? []).map((interview) => ({
        profileId: interview.profileId,
        displayName: clean(interview.displayName) || '未命名候选人',
        jobName: clean(interview.jobName) || '职位未记录',
        interviewAt: formatClock(interview.startsAtMs),
        method: clean(interview.method) || '方式待确认',
        confirmationLabel: interviewStateLabel(interview.state),
      })),
      todayActivity: {
        greeted: metricValue(statistics.todayGreeted),
        greetingDisplayTarget: 100,
        newReplies: metricValue(statistics.todayNewReplies),
        newInterviews: metricValue(statistics.todayNewAppointments),
        elapsedInterviews: metricValue(statistics.todayElapsedInterviews),
      },
    },
    confirmation,
    candidates,
    candidateTotals,
    connections: adaptConnections(runtime, job),
    confirmationBadge: snapshot.confirmation.confirmation.ready
      ? safeCount(snapshot.confirmation.confirmation.selectableCount)
      : 0,
    clientVersion: empty.clientVersion,
  }
}

function adaptJob(raw: AppJobRaw): ProductData['customer']['job'] {
  const status = clean(raw.syncStatus)
  const syncState = raw.available && status === 'synced'
    ? 'synced'
    : status === 'syncing'
      ? 'syncing'
      : status === 'stale'
        ? 'stale'
        : 'unavailable'
  const syncStateLabel = syncState === 'synced'
    ? '配置已同步'
    : syncState === 'syncing'
      ? '正在同步'
      : syncState === 'stale'
        ? '配置待更新'
        : status === 'ambiguous'
          ? '职位配置不唯一'
          : '尚未同步职位'
  return {
    backendJobId: raw.available ? clean(raw.backendJobId) || null : null,
    name: raw.available ? clean(raw.name) || null : null,
    syncState,
    syncStateLabel,
    environment: clean(raw.environment) || '智联招聘',
    lastSyncedAt: formatRelativeDateTime(raw.lastSyncedAt),
  }
}

function adaptWorkflow(
  runtime: AppRuntimeRaw,
  businessWindowOpen: boolean,
): WorkflowView {
  const rawMode = clean(runtime.workflowMode)
  const mode: WorkflowView['mode'] = rawMode === 'full' || rawMode === 'replyOnly' ? rawMode : 'none'
  const rawState = clean(runtime.workflowStatus)
  const state: WorkflowView['state'] = [
    'running',
    'paused',
    'waitingDailyWindow',
    'awaitingConfirmation',
    'failed',
  ].includes(rawState)
    ? rawState as WorkflowView['state']
    : 'idle'
  const rawPendingAction = clean(runtime.workflowPendingAction)
  const pendingAction: WorkflowView['pendingAction'] =
    rawPendingAction === 'sourcing' || rawPendingAction === 'end'
      ? rawPendingAction
      : null
  let unavailableReason: string | null = null
  if (!runtime.available) unavailableReason = '授权状态暂不可读取'
  else if (!runtime.authorized) unavailableReason = '完成激活后可开始'
  else if (!businessWindowOpen) unavailableReason = '运行时间为 08:00～24:00'

  const labels: Record<WorkflowView['state'], string> = {
    idle: '尚未开始',
    running: '运行中',
    paused: '已暂停',
    waitingDailyWindow: businessWindowOpen ? '等待手动恢复' : '等待 08:00 开启',
    awaitingConfirmation: '等待人工确认',
    failed: '运行失败',
  }
  return {
    mode,
    state,
    stateLabel: labels[state],
    positionLabel: workflowPositionLabel(state, mode, businessWindowOpen),
    canStart: (state === 'idle' || state === 'failed') && unavailableReason === null,
    canAddBatch: runtime.canAddBatch && pendingAction === null && unavailableReason === null,
    canEnd: runtime.canEnd === true && pendingAction === null,
    canPause: pendingAction !== 'end' &&
      (state === 'running' || state === 'awaitingConfirmation') && runtime.authorized,
    canResume: pendingAction !== 'end' &&
      (state === 'paused' || state === 'waitingDailyWindow') && unavailableReason === null,
    pendingAction,
    unavailableReason,
  }
}

function workflowPositionLabel(
  state: WorkflowView['state'],
  mode: WorkflowView['mode'],
  businessWindowOpen: boolean,
): string | null {
  if (state === 'awaitingConfirmation') return '招呼语已经生成，等待候选确认'
  if (state === 'waitingDailyWindow') {
    return businessWindowOpen
      ? '业务运行已停在成员边界，等待手动恢复'
      : '业务运行已停在成员边界，08:00 后需手动恢复'
  }
  if (state === 'paused') return mode === 'replyOnly' ? '消息处理已暂停' : '今日任务已暂停'
  if (state === 'running') return mode === 'replyOnly' ? '正在处理候选人消息' : '今日任务正在运行'
  if (state === 'failed') return '今日任务没有完成，请查看下方失败原因'
  return null
}

function adaptFunnel(raw: AppFunnelRaw): ProductData['overview']['funnel'] {
  if (!raw.available) {
    return createEmptyProductData().overview.funnel
  }
  const activeKey = failureStage(raw) ?? funnelStageAliases[clean(raw.stage)] ?? null
  const activeIndex = activeKey ? stageOrder.indexOf(activeKey) : -1
  const completedAll = raw.stage === 'completed'
  const failed = raw.stage === 'failed'
  const target = safeCount(raw.targetCount)
  const selected = safeCount(raw.selectedCount)
  const confirmed = raw.stage === 'awaitingConfirmation'
    ? Math.max(0, selected - safeCount(raw.pendingConfirm))
    : activeIndex > stageOrder.indexOf('confirm') || completedAll
      ? selected
      : 0
  const values: Record<FunnelStageKey, { completed: number; target: number | null; failed: number }> = {
    collect: { completed: safeCount(raw.capturedCount), target, failed: 0 },
    score: { completed: safeCount(raw.scoredCount), target, failed: 0 },
    select: { completed: selected, target: safeCount(raw.scoredCount), failed: 0 },
    greeting: {
      completed: safeCount(raw.greetingReady),
      target: selected,
      failed: safeCount(raw.generationFailedCount),
    },
    confirm: { completed: confirmed, target: selected, failed: 0 },
    send: {
      completed: safeCount(raw.sentCount),
      target: selected,
      // suspect 是结果未确认，归到发送阶段的异常里由人工收敛。
      failed: safeCount(raw.sendFailedCount) + safeCount(raw.suspectCount),
    },
  }
  const stages: FunnelStageView[] = stageOrder.map((key, index) => ({
    key,
    label: stageLabels[key],
    state: failed && key === activeKey
      ? 'failed'
      : completedAll || (activeIndex >= 0 && index < activeIndex)
        ? 'complete'
        : index === activeIndex
          ? 'active'
          : 'pending',
    ...values[key],
  }))
  return {
    stage: clean(raw.stage) || null,
    stateLabel: funnelStateLabel(raw.stage),
    target,
    pending: safeCount(raw.pendingConfirm),
    // 顶部汇总是两个阶段的失败之和，明细各归各格。
    failed: safeCount(raw.generationFailedCount) + safeCount(raw.sendFailedCount) +
      safeCount(raw.suspectCount),
    latestFailure: clean(raw.lastFailureReason) || null,
    stages,
  }
}

function failureStage(raw: AppFunnelRaw): FunnelStageKey | null {
  if (raw.stage !== 'failed') return null
  const target = safeCount(raw.targetCount)
  if (safeCount(raw.capturedCount) < target) return 'collect'
  if (safeCount(raw.scoredCount) < target) return 'score'
  if (safeCount(raw.selectedCount) === 0) return 'select'
  if (safeCount(raw.greetingReady) < safeCount(raw.selectedCount)) return 'greeting'
  if (safeCount(raw.pendingConfirm) > 0) return 'confirm'
  return 'send'
}

function funnelStateLabel(stage: string | undefined): string {
  const labels: Record<string, string> = {
    collecting: '正在采集',
    scoring: '正在评分',
    selecting: '正在筛选',
    generatingGreetings: '正在生成招呼语',
    awaitingConfirmation: '等待候选确认',
    sendingGreetings: '正在发送招呼',
    completed: '本批已完成',
    failed: '本批运行失败',
  }
  return labels[clean(stage)] ?? '候选漏斗'
}

function adaptCommunication(
  raw: string | undefined,
  businessWindowOpen: boolean,
): ProductData['overview']['communication'] {
  const state = clean(raw)
  if (state === 'running' || state === 'active') {
    return { state: 'running', stateLabel: '运行中', lastPatrolAt: null }
  }
  if (state === 'paused') return { state: 'paused', stateLabel: '已暂停', lastPatrolAt: null }
  if (state === 'waitingDailyWindow') {
    return {
      state: 'waitingDailyWindow',
      stateLabel: businessWindowOpen ? '等待手动恢复' : '等待 08:00 开启',
      lastPatrolAt: null,
    }
  }
  if (state === 'manualRequired') {
    return { state: 'manualRequired', stateLabel: '需要人工', lastPatrolAt: null }
  }
  return { state: 'idle', stateLabel: '未运行', lastPatrolAt: null }
}

function adaptConfirmation(
  raw: AppConfirmationRaw,
  context: { workflowPaused: boolean; businessWindowOpen: boolean },
): ConfirmationBatchView {
  return {
    ready: raw.available && raw.ready,
    readinessReason: confirmationReadinessReason(raw),
    batchId: raw.available && clean(raw.batchId) ? clean(raw.batchId) : null,
    createdAt: formatRelativeDateTime(raw.createdAt),
    scoreCompleted: raw.available ? safeCount(raw.scoredCount) : null,
    selectedCount: raw.available ? safeCount(raw.selectedCount) : null,
    greetingSucceeded: raw.available ? safeCount(raw.generatedCount) : null,
    greetingFailed: raw.available ? safeCount(raw.generationFailed) : null,
    greetingPending: raw.available ? safeCount(raw.generationPending) : null,
    workflowPaused: context.workflowPaused,
    businessWindowOpen: context.businessWindowOpen,
    candidates: (raw.candidates ?? []).map((candidate) => adaptConfirmationCandidate(candidate)),
  }
}

function confirmationReadinessReason(raw: AppConfirmationRaw): string | null {
  if (!raw.available) return '当前没有等待确认的批次'
  if (raw.ready) return null
  const reasons: Record<string, string> = {
    selectionPending: '候选筛选尚未完成',
    greetingGenerationPending: '招呼语仍在生成，整批就绪后才能全选发送',
  }
  return reasons[clean(raw.reason)] ?? '当前批次尚未完成，暂不能全选发送'
}

function adaptConfirmationCandidate(raw: AppConfirmationCandidateRaw): ConfirmationCandidateView {
  const sendState = confirmationSendState(raw.status)
  return {
    ...emptyCandidate(raw.profileId, raw.displayName, raw.jobName),
    statusLabel: confirmationStatusLabel(raw.status),
    statusTone: confirmationStatusTone(sendState),
    aiScore: raw.score,
    greeting: clean(raw.greetingText) || null,
    generationStateLabel: generationStateLabel(raw),
    sendState,
    sendStateLabel: confirmationStatusLabel(raw.status),
    selectable: raw.selectable,
    manualRequired: raw.status === 'suspect',
    manualReason: clean(raw.failure) ? '本条招呼语未能生成，请人工复核' : null,
  }
}

function confirmationSendState(status: string): ConfirmationSendState {
  switch (status) {
  case 'ready': return 'ready'
  case 'sending': return 'sending'
  case 'sent': return 'sent'
  case 'failed': return 'failed'
  case 'suspect': return 'suspect'
  // 招呼语已就绪、但最终没能发出(推荐流已变化、档案不再可发送)。它必须与
  // "招呼语根本没生成"区分开:这些人当初是在确认名单里的,发送进度的分母得
  // 一直算上他们,否则分母会在发送途中往下走。
  case 'abandoned':
  case 'unavailable':
    return 'settledWithoutSend'
  default: return 'ineligible'
  }
}

function confirmationStatusLabel(status: string): string {
  const labels: Record<string, string> = {
    ready: '待发送',
    sending: '发送中',
    sent: '已发送',
    failed: '发送失败',
    suspect: '结果待人工确认',
    generationPending: '等待生成',
    generating: '生成中',
    generationFailed: '生成失败',
    abandoned: '推荐流已变化',
    unavailable: '不再可发送',
  }
  return labels[status] ?? '不可发送'
}

function generationStateLabel(raw: AppConfirmationCandidateRaw): string {
  if (raw.status === 'generationPending') return '等待生成招呼语'
  if (raw.status === 'generating') return '正在生成招呼语'
  if (raw.status === 'generationFailed') return '招呼语生成失败'
  if (clean(raw.greetingText)) return '招呼语已生成'
  return '当前无可用招呼语'
}

function confirmationStatusTone(
  state: ConfirmationSendState,
): ConfirmationCandidateView['statusTone'] {
  const tones: Record<ConfirmationSendState, ConfirmationCandidateView['statusTone']> = {
    ready: 'blue',
    sending: 'amber',
    sent: 'green',
    failed: 'red',
    suspect: 'red',
    settledWithoutSend: 'slate',
    ineligible: 'slate',
  }
  return tones[state]
}

export function adaptCandidateList(
  response: AppCandidateListResponse,
  view: CandidateView,
  now = new Date(),
): CandidateViewItem[] {
  return (response.candidates.items ?? []).map((item) => adaptCandidateListItem(item, view, now))
}

// candidateTotal 取脑侧 total。脑总是随列表一起给出总数,但它若缺失或比
// 本页条数还小(只可能是响应损坏),就退回本页条数——宁可少报,不能报出
// 一个比看得见的人还少的总数。
function candidateTotal(
  response: AppCandidateListResponse | undefined,
  items: CandidateViewItem[],
): number {
  return Math.max(safeCount(response?.candidates?.total), items.length)
}

function adaptCandidateListItem(
  raw: AppCandidateListItemRaw,
  view: CandidateView,
  now: Date,
): CandidateViewItem {
  const base = emptyCandidate(raw.profileId, raw.displayName, raw.jobName)
  const status = candidateStatus(raw, view)
  return {
    ...base,
    statusLabel: status.label,
    statusTone: status.tone,
    lastMessage: clean(raw.lastMessagePreview) || null,
    lastActiveAt: formatEpochRelative(raw.lastActivityAtMs, now),
    unreadCount: safeCount(raw.unreadCount),
    manualRequired: Boolean(raw.manualRequired),
    deterministicState: status.deterministicState,
    manualReason: clean(raw.manualReason) || null,
    interviewAt: formatEpochRelative(raw.interviewStartsAtMs, now),
    interviewMethod: clean(raw.interviewMethod) || null,
    wechatAccount: clean(raw.wechat) || null,
    wechatExchangedAt: formatEpochRelative(raw.wechatObservedAtMs, now),
    stillInAutoCommunication: raw.status
      ? !raw.manualRequired && raw.status !== 'ended'
      : null,
  }
}

function emptyCandidate(
  profileId: string,
  displayName: string | undefined,
  jobName: string | undefined,
): CandidateViewItem {
  return {
    profileId,
    displayName: clean(displayName) || '未命名候选人',
    age: null,
    education: null,
    experience: null,
    city: null,
    currentRole: null,
    jobName: clean(jobName) || '职位未记录',
    statusLabel: '状态待确认',
    statusTone: 'slate',
    lastMessage: null,
    lastActiveAt: null,
    unreadCount: 0,
    manualRequired: false,
    resumeSummary: null,
    deterministicState: null,
    latestAiDecision: null,
    manualReason: null,
    interviewAt: null,
    interviewMethod: null,
    wechatAccount: null,
    wechatExchangedAt: null,
    stillInAutoCommunication: null,
    messages: [],
    decisions: [],
    actions: [],
  }
}

function candidateStatus(
  raw: AppCandidateListItemRaw,
  view: CandidateView,
): {
  label: string
  tone: CandidateViewItem['statusTone']
  deterministicState: string
} {
  if (raw.manualRequired) {
    return { label: '需要人工', tone: 'red', deterministicState: '自动沟通已转人工' }
  }
  if (view === 'wechat') {
    return {
      label: clean(raw.wechat) ? '已换微信' : '账号待收编',
      tone: clean(raw.wechat) ? 'green' : 'slate',
      deterministicState: raw.status === 'ended' ? '微信已交换，沟通主线已结束' : '微信已交换，仍在沟通主线',
    }
  }
  if (view === 'interviewed') {
    return { label: '已约面', tone: 'green', deterministicState: '候选人已接受面试邀约' }
  }
  if (view === 'interviewElapsed') {
    return {
      label: '已面试',
      tone: 'green',
      // 只表示约定时间已过，不代表候选人真的到场；系统没有面试结果事实。
      deterministicState: '约定的面试时间已过',
    }
  }
  if (raw.status === 'invited') {
    const confirmed = cardStateConfirmed(raw.interviewCardState)
    return {
      label: confirmed ? '邀面已确认' : '已邀面',
      tone: confirmed ? 'green' : 'amber',
      deterministicState: confirmed ? '候选人已确认面试' : '邀面卡已发送，等待候选人确认',
    }
  }
  if (raw.status === 'greeted') return { label: '已招呼', tone: 'blue', deterministicState: '已招呼，等待候选人回复' }
  if (raw.status === 'communicating') return { label: '已回复', tone: 'blue', deterministicState: '正在自动沟通' }
  if (raw.status === 'ended') {
    const rejected = raw.endReason === 'rejected' || raw.endReason === 'blacklisted'
    return {
      label: rejected ? '候选人已拒绝' : '沟通已结束',
      tone: rejected ? 'red' : 'slate',
      deterministicState: `沟通已结束${endReasonLabel(raw.endReason) ? `：${endReasonLabel(raw.endReason)}` : ''}`,
    }
  }
  return { label: '状态待确认', tone: 'slate', deterministicState: '状态待确认' }
}

export function adaptCandidateDetail(
  response: AppCandidateDetailResponse,
  fallback?: CandidateViewItem,
  now = new Date(),
): CandidateViewItem {
  const raw = response.candidate
  const inferredView = inferCandidateView(raw.candidate, now)
  const item = adaptCandidateListItem(raw.candidate, inferredView, now)
  const resumeFacts = raw.resume.basic ?? []
  const decisions = adaptAIJudgement(raw.latestAi)
  const merged = {
    ...(fallback ?? item),
    ...item,
    statusLabel: fallback?.statusLabel ?? item.statusLabel,
    statusTone: fallback?.statusTone ?? item.statusTone,
    deterministicState: fallback?.deterministicState ?? item.deterministicState,
    age: numberFromResume(resumeFacts, ['年龄']),
    education: resumeValue(resumeFacts, ['学历', '最高学历']) || fallback?.education || null,
    experience: resumeValue(resumeFacts, ['经验', '工作年限', '工作经验']) || fallback?.experience || null,
    city: resumeValue(resumeFacts, ['城市', '现居住地', '所在地']) || fallback?.city || null,
    currentRole: resumeValue(resumeFacts, ['当前职位', '职位', '现任职位']) || fallback?.currentRole || null,
    resumeSummary: resumeSummary(raw.resume),
    latestAiDecision: decisions[0]?.summary ?? null,
    messages: (raw.messages ?? []).map((message) => adaptMessage(message, now)),
    decisions,
    actions: (raw.actions ?? []).map((action, index) => adaptAction(action, index, now)),
  }
  return merged
}

function inferCandidateView(raw: AppCandidateListItemRaw, now: Date): CandidateView {
  if (clean(raw.wechat)) return 'wechat'
  if (raw.status === 'interviewed') {
    return interviewDeadlinePassed(raw, now) ? 'interviewElapsed' : 'interviewed'
  }
  return 'communicating'
}

// interviewDeadlinePassed 与脑侧 appLatestInterviewDeadlineMs 同判据:结束时间
// 优先、缺失退到开始时间，两者都读不到即视为已过。只用于详情页在没有列表
// fallback 时推断展示分类，列表分流始终由脑决定。
function interviewDeadlinePassed(raw: AppCandidateListItemRaw, now: Date): boolean {
  const deadline = typeof raw.interviewEndsAtMs === 'number' && raw.interviewEndsAtMs > 0
    ? raw.interviewEndsAtMs
    : raw.interviewStartsAtMs
  if (typeof deadline !== 'number' || !Number.isFinite(deadline) || deadline <= 0) return true
  return deadline < now.getTime()
}

function resumeValue(fields: AppResumeFieldRaw[], labels: string[]): string | null {
  const found = fields.find((field) => labels.some((label) => clean(field.label).includes(label)))
  return clean(found?.value) || null
}

function numberFromResume(fields: AppResumeFieldRaw[], labels: string[]): number | null {
  const value = resumeValue(fields, labels)
  const match = value?.match(/\d{1,3}/)
  if (!match) return null
  const parsed = Number(match[0])
  return Number.isFinite(parsed) && parsed > 0 && parsed < 120 ? parsed : null
}

function resumeSummary(raw: AppResumeRaw): string | null {
  if (!raw.available) return null
  const sections = [
    clean(raw.selfEvaluation) ? `自我评价：${clean(raw.selfEvaluation)}` : '',
    clean(raw.education) ? `教育经历：${clean(raw.education)}` : '',
    clean(raw.workExperiences) ? `工作经历：${clean(raw.workExperiences)}` : '',
  ].filter(Boolean)
  if (sections.length > 0) return sections.join('\n\n')
  const expectations = (raw.expectations ?? [])
    .map((field) => `${clean(field.label)}：${clean(field.value)}`)
    .filter((value) => !value.endsWith('：'))
  const basics = (raw.basic ?? [])
    .map((field) => `${clean(field.label)}：${clean(field.value)}`)
    .filter((value) => !value.endsWith('：'))
  const fallback = [...expectations, ...basics]
  return fallback.length > 0 ? fallback.join('；') : null
}

function adaptMessage(raw: AppMessageRaw, now: Date): CandidateMessageView {
  const direction: CandidateMessageView['direction'] = raw.direction === 'in'
    ? 'incoming'
    : raw.direction === 'out'
      ? 'outgoing'
      : 'system'
  return {
    id: `message-${raw.seq}`,
    direction,
    content: clean(raw.text) || cardMessageText(raw, now),
    occurredAt: formatEpochRelative(raw.tsApproxMs, now) ?? '时间未知',
    kindLabel: messageKindLabel(raw),
  }
}

function cardMessageText(raw: AppMessageRaw, now: Date): string {
  if (raw.cardType === 'interviewInvite') {
    const at = formatEpochRelative(raw.interviewStartsAtMs, now) ?? '时间待确认'
    return `面试时间：${at}；方式：${clean(raw.interviewMethod) || '待确认'}`
  }
  if (raw.kind === 'card') return `卡片状态：${clean(raw.cardState) || '待确认'}`
  return '此消息没有可展示的正文'
}

function messageKindLabel(raw: AppMessageRaw): string | undefined {
  if (raw.kind === 'card' && raw.cardType === 'interviewInvite') return '邀面卡'
  if (raw.kind === 'card' && raw.cardType === 'wechatInvite') return '换微信卡'
  if (raw.kind === 'card') return '卡片'
  if (raw.kind !== 'text') return '其他消息'
  return undefined
}

function adaptAIJudgement(raw: AppAIJudgementRaw): CandidateDecisionView[] {
  if (!raw.available) return []
  const summary = [
    raw.intentLabel ? `意向：${intentLabel(raw.intentLabel)}` : '',
    raw.intentSource ? `来源：${intentSourceLabel(raw.intentSource)}` : '',
    raw.failure ? '异常：本轮判断未完成' : '',
  ].filter(Boolean).join('；') || '本轮 AI 判断已形成'
  return [{
    id: `ai-${clean(raw.classifiedAt) || 'latest'}`,
    label: dialogueStatusLabel(raw.status),
    summary,
    occurredAt: formatRelativeDateTime(raw.classifiedAt) ?? '时间未知',
  }]
}

function adaptAction(raw: AppActionRaw, index: number, now: Date): CandidateActionView {
  return {
    id: `action-${index}-${clean(raw.createdAt) || 'unknown'}`,
    label: actionKindLabel(raw.kind),
    resultLabel: clean(raw.failure) ? '需要人工复核' : actionStatusLabel(raw.status),
    occurredAt: formatRelativeDateTime(raw.createdAt, now) ?? '时间未知',
    tone: actionTone(raw.status),
  }
}

function adaptConnections(
  runtime: AppRuntimeRaw,
  job: ProductData['customer']['job'],
): ProductConnectionView[] {
  const providerName = clean(runtime.provider)
  const modelName = clean(runtime.model)
  const pluginVersion = clean(runtime.pluginVersion)
  const pluginHealth = clean(runtime.pluginHealth)
  const pluginValue = runtime.pluginOnline
    ? runtime.contractMatch ? '已连接' : '契约不一致'
    : pluginHealth === 'stalled' ? '连接异常' : '未连接'
  const pluginTone: ProductConnectionView['tone'] = runtime.pluginOnline
    ? runtime.contractMatch ? 'success' : 'danger'
    : runtime.available ? 'warning' : 'neutral'
  return [
    {
      label: '客户授权',
      value: runtime.authorized ? '授权有效' : '等待激活',
      tone: runtime.authorized ? 'success' : 'warning',
      detail: clean(runtime.customerStatus) || (runtime.available ? '已读取本机授权状态' : '授权状态暂不可读取'),
    },
    {
      label: '后台职位配置',
      value: job.syncStateLabel,
      tone: job.syncState === 'synced' ? 'success' : job.syncState === 'stale' ? 'warning' : 'neutral',
      detail: job.lastSyncedAt ? `最近同步：${job.lastSyncedAt}` : '尚无同步记录',
    },
    {
      label: 'AI 模型',
      value: runtime.providerConfigured ? modelName || '已配置' : '未配置',
      tone: runtime.providerConfigured ? 'success' : runtime.available ? 'warning' : 'neutral',
      detail: runtime.providerConfigured
        ? `${providerName || '本地 provider'} 已配置；此处不显示密钥`
        : '请在开发者诊断入口完成本地模型配置',
    },
    {
      label: 'Chrome 插件',
      value: pluginValue,
      tone: pluginTone,
      detail: [
        pluginVersion ? `版本 ${pluginVersion}` : '',
        pluginHealth ? `状态 ${pluginHealth}` : '',
      ].filter(Boolean).join('；') || '尚未取得插件运行状态',
    },
    { label: '客户端版本', value: __APP_VERSION__, tone: 'neutral', detail: '开发者模式' },
  ]
}

function authorizationLabel(runtime: AppRuntimeRaw): string {
  if (runtime.authorized) return '授权有效'
  if (!runtime.available) return '授权状态暂不可读取'
  const status = clean(runtime.customerStatus)
  if (status === 'inactive' || status === 'disabled') return '客户授权已停用'
  return '等待激活'
}

function metricValue(metric: AppMetricRaw | undefined): ProductMetric {
  if (!metric?.exact || metric.value === null || !Number.isFinite(metric.value)) return null
  return safeCount(metric.value)
}

function clean(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function safeCount(value: number | null | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.floor(value) : 0
}

function formatDateHeading(date: Date): string {
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: 'long',
    day: 'numeric',
    weekday: 'long',
  }).format(date)
}

function parseDate(value: string | null | undefined): Date | null {
  if (!value) return null
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

function formatRelativeDateTime(value: string | null | undefined, now = new Date()): string | null {
  const date = parseDate(value)
  return date ? formatRelativeDate(date, now) : null
}

function formatEpochRelative(value: number | null | undefined, now = new Date()): string | null {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) return null
  const milliseconds = value < 10_000_000_000 ? value * 1000 : value
  const date = new Date(milliseconds)
  return Number.isNaN(date.getTime()) ? null : formatRelativeDate(date, now)
}

function formatRelativeDate(date: Date, now: Date): string {
  const clock = new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
  if (sameLocalDay(date, now)) return `今天 ${clock}`
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  if (sameLocalDay(date, yesterday)) return `昨天 ${clock}`
  return new Intl.DateTimeFormat('zh-CN', {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

function sameLocalDay(left: Date, right: Date): boolean {
  return left.getFullYear() === right.getFullYear() &&
    left.getMonth() === right.getMonth() &&
    left.getDate() === right.getDate()
}

function formatDateOnly(value: string | null | undefined): string | null {
  const date = parseDate(value)
  if (!date) return null
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function formatClock(epochMs: number): string {
  if (!Number.isFinite(epochMs) || epochMs <= 0) return '—'
  const date = new Date(epochMs < 10_000_000_000 ? epochMs * 1000 : epochMs)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

function interviewStateLabel(state: string | undefined): string {
  const normalized = clean(state)
  if (cardStateConfirmed(normalized)) return '已确认'
  if (['rejected', 'declined', 'cancelled'].includes(normalized)) return '未确认'
  return normalized ? '待确认' : '状态待回读'
}

function cardStateConfirmed(state: string | undefined): boolean {
  return ['accepted', 'confirmed', 'ok', 'success', 'agreed'].includes(clean(state))
}

function endReasonLabel(reason: string | undefined): string {
  const labels: Record<string, string> = {
    greetingFailed: '招呼失败',
    rejected: '候选人拒绝',
    blacklisted: '候选人明确拒绝后停止',
    fallbackArchive: '兜底归档',
    silentInterviewPending: '邀面后沉默归档',
    silentWechatInvited: '换微信邀请后沉默归档',
    silentWechatExchanged: '换微信后沉默归档',
    silent: '长期无回复归档',
  }
  return labels[clean(reason)] ?? (clean(reason) ? '其他终止原因' : '')
}

function intentLabel(value: string): string {
  const labels: Record<string, string> = {
    interested: '有意向',
    neutral: '中性',
    rejected: '拒绝',
  }
  return labels[value] ?? '待确认'
}

function intentSourceLabel(value: string): string {
  const labels: Record<string, string> = {
    llm: '模型判断',
    deterministic: '确定性规则',
    resumeSubmission: '投递简历事件',
  }
  return labels[value] ?? '其他来源'
}

function dialogueStatusLabel(value: string | undefined): string {
  const labels: Record<string, string> = {
    collected: '消息已收编',
    classified: '意向已判断',
    adviceReady: 'AI 建议已生成',
    dispatching: '动作执行中',
    completed: '本轮已完成',
    manualRequired: '本轮转人工',
    superseded: '本轮已被更新',
  }
  return labels[clean(value)] ?? '最近 AI 判断'
}

function actionKindLabel(value: string): string {
  const labels: Record<string, string> = {
    replyText: '回复文本',
    inviteWechat: '发起换微信邀请',
    acceptWechat: '接受换微信邀请',
    interviewInvite: '发起线上会议',
  }
  return labels[value] ?? '业务动作'
}

function actionStatusLabel(value: string): string {
  const labels: Record<string, string> = {
    planned: '已计划',
    effectPending: '结果确认中',
    sent: '服务端已确认',
    manualRequired: '需要人工',
    superseded: '已被更新',
    ok: '服务端已确认',
    failed: '执行失败',
    suspect: '结果待人工确认',
  }
  return labels[value] ?? '状态待确认'
}

function actionTone(value: string): CandidateActionView['tone'] {
  if (value === 'sent' || value === 'ok') return 'success'
  if (value === 'failed' || value === 'suspect') return 'danger'
  if (value === 'manualRequired' || value === 'effectPending') return 'warning'
  return 'neutral'
}
