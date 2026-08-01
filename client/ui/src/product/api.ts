import { appGet, appPost } from '../api'
import {
  adaptCandidateDetail,
  adaptProductSnapshot,
  productCandidatePath,
  type AppCandidateDetailResponse,
  type AppCandidateListResponse,
  type AppConfirmationResponse,
  type AppOverviewResponse,
  type AppReadSnapshot,
} from './data'
import type { CandidateView, CandidateViewItem, ProductData } from './types'

const candidateViews: CandidateView[] = [
  'communicating',
  'interviewed',
  'interviewElapsed',
  'wechat',
]

export async function readProductData(now = new Date()): Promise<ProductData> {
  const [overview, confirmation, ...candidateResponses] = await Promise.all([
    appGet<AppOverviewResponse>('/app/overview'),
    appGet<AppConfirmationResponse>('/app/confirmation'),
    ...candidateViews.map((view) => appGet<AppCandidateListResponse>(productCandidatePath(view))),
  ])
  const candidates = {} as AppReadSnapshot['candidates']
  candidateViews.forEach((view, index) => {
    const response = candidateResponses[index]
    if (response) candidates[view] = response
  })
  return adaptProductSnapshot({ overview, confirmation, candidates }, now)
}

export async function readCandidateDetail(
  profileId: string,
  fallback?: CandidateViewItem,
  now = new Date(),
): Promise<CandidateViewItem> {
  const response = await appGet<AppCandidateDetailResponse>(
    `/app/candidates/${encodeURIComponent(profileId)}`,
  )
  return adaptCandidateDetail(response, fallback, now)
}

interface ProductAcceptedResponse {
  accepted: boolean
}

export async function startProductWorkflow(
  mode: 'full' | 'replyOnly',
  backendJobId?: string | null,
): Promise<void> {
  if (mode === 'full') {
    const normalized = backendJobId?.trim() ?? ''
    if (!normalized) throw new Error('当前没有已绑定职位，暂时不能开始今日任务')
    await appPost<ProductAcceptedResponse>('/app/workflow/start', {
      mode,
      backendJobId: normalized,
    })
    return
  }
  await appPost<ProductAcceptedResponse>('/app/workflow/start', { mode })
}

export async function pauseProductWorkflow(): Promise<void> {
  await appPost<ProductAcceptedResponse>('/app/workflow/pause', {})
}

export async function resumeProductWorkflow(): Promise<void> {
  await appPost<ProductAcceptedResponse>('/app/workflow/resume', {})
}

export async function endProductWorkflow(): Promise<void> {
  await appPost<ProductAcceptedResponse>('/app/workflow/end', {})
}

export async function syncProductJobs(): Promise<void> {
  await appPost<ProductAcceptedResponse>('/app/jobs/sync', {})
}

export async function sendProductConfirmation(
  batchId: string,
  profileIds: string[],
): Promise<void> {
  await appPost<ProductAcceptedResponse>('/app/confirmation/send', { batchId, profileIds })
}

/** 一段可面试窗口，起止都是 'HH:MM' 整点，右开区间。 */
export interface InterviewWindow {
  start: string
  end: string
}

/** 按星期循环的可面试时段周表。key 是中文星期名，脑侧保证七天都在。 */
export type InterviewSchedule = Record<string, InterviewWindow[]>

export interface InterviewScheduleResponse {
  schedule: InterviewSchedule
  /** 星期顺序由脑侧给出，两端不各自硬编码。 */
  weekdays: string[]
}

export async function readInterviewSchedule(): Promise<InterviewScheduleResponse> {
  return appGet<InterviewScheduleResponse>('/app/interview-schedule')
}

/**
 * 整表保存。脑侧是唯一校验点——空表会被它拒掉并把原因带回来，这里不复制一份规则。
 * 抛错即未落库，调用方必须据此提示，不能让界面停在用户刚拖出来的样子。
 */
export async function saveInterviewSchedule(schedule: InterviewSchedule): Promise<void> {
  await appPost<{ saved: boolean }>('/app/interview-schedule', { schedule })
}

/** 客户端版本更新状态。只回答"有没有新版、备好了没有"。 */
export interface ProductUpdateStatus {
  currentVersion?: string
  available: boolean
  version?: string
  ready: boolean
  notes?: string
}

export async function readProductUpdateStatus(): Promise<ProductUpdateStatus> {
  return appGet<ProductUpdateStatus>('/app/update')
}

/**
 * 装新版。走 Electron 主进程 —— renderer 起不了进程，也不该能起。
 *
 * 成功意味着安装器已经交出去、客户端马上要退出并被覆盖，所以这个 Promise
 * resolve 之后不必再更新界面：窗口很快就没了。
 */
export async function installProductUpdate(): Promise<void> {
  const bridge = typeof window !== 'undefined' ? window.recruitHelper : undefined
  if (!bridge?.installUpdate) {
    throw new Error('当前环境不支持一键更新，请手动安装')
  }
  const result = await bridge.installUpdate()
  if (!result.ok) throw new Error(result.error || '更新失败')
}
