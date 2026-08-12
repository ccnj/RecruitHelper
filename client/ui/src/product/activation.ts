import type { JobConfigActivationInput, JobConfigSourceView } from '../api'
import type { CustomerView } from './types'

export type ProductReadState = 'loading' | 'ready' | 'stale'

export function shouldShowActivation(
  readState: ProductReadState,
  customer: Pick<CustomerView, 'authorized' | 'activationRequired'>,
): boolean {
  return readState === 'ready' && !customer.authorized && customer.activationRequired
}

// 后台地址已内置于脑(jobconfig.DefaultBaseURL),激活页只收激活码;
// /admin/job-config/activate 的 API 层仍接受显式 base_url 供开发指向假后台。
export function activationInputError(
  source: JobConfigSourceView | null,
  inviteCode: string,
): string | null {
  if (source && !source.machineIdentityReady) {
    return '当前机器身份暂不可用，请重启客户端后再试。'
  }
  if (!inviteCode.trim()) {
    return '请输入激活码。'
  }
  return null
}

export function buildActivationInput(inviteCode: string): JobConfigActivationInput {
  return {
    invite_code: inviteCode.trim(),
  }
}
