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
  'pendingInterview',
  'interviewed',
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
    if (!normalized) throw new Error('完整流程尚未绑定后台职位')
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

export async function sendProductConfirmation(
  batchId: string,
  profileIds: string[],
): Promise<void> {
  await appPost<ProductAcceptedResponse>('/app/confirmation/send', { batchId, profileIds })
}
