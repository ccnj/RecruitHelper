export type ProductPage =
  | 'home'
  | 'confirmation'
  | 'communicating'
  // interviewed = 已约面(面试时间界未过)，interviewElapsed = 已面试(时间界
  // 已过)。两者同源于脑侧 main_status = interviewed，只按时间分流；已邀面
  // (invited)没有独立页面，并入 communicating。
  | 'interviewed'
  | 'interviewElapsed'
  | 'wechat'
  | 'settings'

export type CandidateView = Exclude<ProductPage, 'home' | 'confirmation' | 'settings'>

export type ProductMetric = number | null

export interface BoundJobView {
  backendJobId: string | null
  name: string | null
  syncState: 'synced' | 'syncing' | 'stale' | 'unavailable'
  syncStateLabel: string
  environment: string
  lastSyncedAt: string | null
}

export interface CustomerView {
  name: string
  shortName: string
  authorizationLabel: string
  authorized: boolean
  activationRequired: boolean
  job: BoundJobView
}

export interface WorkflowView {
  mode: 'none' | 'full' | 'replyOnly'
  state: 'idle' | 'running' | 'paused' | 'waitingDailyWindow' | 'awaitingConfirmation' | 'failed'
  stateLabel: string
  positionLabel: string | null
  canStart: boolean
  canAddBatch: boolean
  canEnd: boolean
  canPause: boolean
  canResume: boolean
  pendingAction: 'sourcing' | 'end' | null
  unavailableReason: string | null
}

export type FunnelStageKey =
  | 'collect'
  | 'score'
  | 'select'
  | 'greeting'
  | 'confirm'
  | 'send'

export interface FunnelStageView {
  key: FunnelStageKey
  label: string
  state: 'pending' | 'active' | 'complete' | 'failed'
  completed: number
  target: number | null
  failed: number
}

export interface FunnelView {
  stage: string | null
  stateLabel: string
  target: number | null
  pending: number | null
  failed: number | null
  latestFailure: string | null
  stages: FunnelStageView[]
}

export interface CommunicationEngineView {
  state: 'idle' | 'running' | 'paused' | 'waitingDailyWindow' | 'manualRequired'
  stateLabel: string
  lastPatrolAt: string | null
}

export interface MetricItem {
  label: string
  value: ProductMetric
  tone: 'blue' | 'amber' | 'green' | 'red' | 'slate'
}

export interface LedgerItem {
  label: string
  value: ProductMetric
}

export interface TodayInterviewView {
  profileId: string
  displayName: string
  jobName: string
  interviewAt: string
  method: string
  confirmationLabel: string
}

// 「已过面试时间」只说明约定时间过了,不代表面试真发生过——系统没有面试完成
// 写入口。对客户是干扰,2026-07-31 换成今日新换微信;后端统计仍在,诊断台可查。
export interface TodayActivityView {
  greeted: ProductMetric
  greetingDisplayTarget: number | null
  newReplies: ProductMetric
  newWechat: ProductMetric
  newInterviews: ProductMetric
}

// HomeStatusView 是首页那一句话的状态。产品端只讲"现在在干什么、要不要你
// 动手",不讲阶段机:运行中只有招呼中与沟通中两种说法(2026-07-31 甲方裁决),
// 其余都是等用户动手的情形。阶段级进度在诊断台看。
export interface HomeStatusView {
  label: string
  hint: string
  tone: 'running' | 'idle' | 'attention' | 'failed'
}

export interface OverviewView {
  dateLabel: string
  refreshedAt: string | null
  businessWindowLabel: string
  businessWindowOpen: boolean
  homeStatus: HomeStatusView
  workflow: WorkflowView
  funnel: FunnelView
  communication: CommunicationEngineView
  todayMetrics: MetricItem[]
  ledgerStartedAt: string | null
  ledger: LedgerItem[]
  todayInterviews: TodayInterviewView[]
  todayActivity: TodayActivityView
}

export interface CandidateMessageView {
  id: string
  direction: 'incoming' | 'outgoing' | 'system'
  content: string
  occurredAt: string
  kindLabel?: string
}

export interface CandidateDecisionView {
  id: string
  label: string
  summary: string
  occurredAt: string
}

export interface CandidateActionView {
  id: string
  label: string
  resultLabel: string
  occurredAt: string
  tone: 'neutral' | 'success' | 'warning' | 'danger'
}

export interface CandidateViewItem {
  profileId: string
  displayName: string
  age: number | null
  education: string | null
  experience: string | null
  city: string | null
  currentRole: string | null
  jobName: string
  statusLabel: string
  statusTone: 'blue' | 'amber' | 'green' | 'red' | 'slate'
  lastMessage: string | null
  lastActiveAt: string | null
  manualRequired: boolean
  resumeSummary: string | null
  deterministicState: string | null
  latestAiDecision: string | null
  manualReason: string | null
  interviewAt: string | null
  interviewMethod: string | null
  wechatAccount: string | null
  wechatExchangedAt: string | null
  stillInAutoCommunication: boolean | null
  messages: CandidateMessageView[]
  decisions: CandidateDecisionView[]
  actions: CandidateActionView[]
}

export type ConfirmationSendState =
  | 'ready'
  | 'sending'
  | 'sent'
  | 'failed'
  | 'suspect'
  // 招呼语已就绪但最终没能发出。与 ineligible(招呼语没生成)分开，才能让
  // 发送进度的分母在整批过程中单调不减。
  | 'settledWithoutSend'
  | 'ineligible'

export interface ConfirmationCandidateView extends CandidateViewItem {
  greeting: string | null
  generationStateLabel: string
  sendState: ConfirmationSendState
  sendStateLabel: string
  selectable: boolean
}

export interface ConfirmationBatchView {
  ready: boolean
  readinessReason: string | null
  batchId: string | null
  createdAt: string | null
  scoreCompleted: ProductMetric
  selectedCount: ProductMetric
  greetingSucceeded: ProductMetric
  greetingFailed: ProductMetric
  greetingPending: ProductMetric
  workflowPaused: boolean
  businessWindowOpen: boolean
  candidates: ConfirmationCandidateView[]
}

export interface ProductConnectionView {
  label: string
  value: string
  tone: 'neutral' | 'success' | 'warning' | 'danger'
  detail?: string
}

export interface ProductData {
  customer: CustomerView
  overview: OverviewView
  confirmation: ConfirmationBatchView
  candidates: Record<CandidateView, CandidateViewItem[]>
  // candidateTotals 是脑侧该视图的真实总数，与 candidates 同处构造。
  // 单页读取有上限，candidates 可能只是前若干位；页面上的人数必须报
  // 这个总数，否则超过上限后计数会永远停在上限值，与首页累计账面互相
  // 矛盾。两者之差即被截断的数量。
  candidateTotals: Record<CandidateView, number>
  connections: ProductConnectionView[]
  confirmationBadge: number
  clientVersion: string
}

export interface ProductActions {
  startWorkflow?: (mode: 'full' | 'replyOnly') => void | Promise<void>
  pauseWorkflow?: () => void | Promise<void>
  resumeWorkflow?: () => void | Promise<void>
  endWorkflow?: () => void | Promise<void>
  syncJobs?: () => void | Promise<void>
  sendConfirmationBatch?: (batchId: string, profileIds: string[]) => void | Promise<void>
  loadCandidateDetail?: (profileId: string, fallback?: CandidateViewItem) => Promise<CandidateViewItem>
  refresh?: () => void | Promise<void>
  copyWechat?: (wechatAccount: string) => void | Promise<void>
}
