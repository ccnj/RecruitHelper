export type ProductPage =
  | 'home'
  | 'confirmation'
  | 'communicating'
  | 'pendingInterview'
  | 'interviewed'
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

export interface TodayActivityView {
  greeted: ProductMetric
  greetingDisplayTarget: number | null
  newReplies: ProductMetric
  newInterviews: ProductMetric
  completedInterviews: ProductMetric
}

export interface OverviewView {
  dateLabel: string
  refreshedAt: string | null
  businessWindowLabel: string
  businessWindowOpen: boolean
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
  unreadCount: number
  manualRequired: boolean
  resumeSummary: string | null
  deterministicState: string | null
  latestAiDecision: string | null
  manualReason: string | null
  interviewAt: string | null
  interviewMethod: string | null
  interviewResult: string | null
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
  | 'ineligible'

export interface ConfirmationCandidateView extends CandidateViewItem {
  aiScore: number | null
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
