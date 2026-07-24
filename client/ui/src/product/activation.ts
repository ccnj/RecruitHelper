import type { JobConfigActivationInput, JobConfigSourceView } from '../api'
import type { CustomerView } from './types'

export type ProductReadState = 'loading' | 'ready' | 'stale'

export function shouldShowActivation(
  readState: ProductReadState,
  customer: Pick<CustomerView, 'authorized' | 'activationRequired'>,
): boolean {
  return readState === 'ready' && !customer.authorized && customer.activationRequired
}

export function activationInputError(
  source: JobConfigSourceView | null,
  baseURL: string,
  inviteCode: string,
): string | null {
  if (source && !source.machineIdentityReady) {
    return '当前机器身份暂不可用，请重启客户端后再试。'
  }
  const normalizedBaseURL = baseURL.trim()
  if (!source?.baseUrlConfigured && !normalizedBaseURL) {
    return '请输入管理员提供的后台地址。'
  }
  if (normalizedBaseURL) {
    try {
      const parsed = new URL(normalizedBaseURL)
      if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
        return '后台地址必须以 http:// 或 https:// 开头。'
      }
    } catch {
      return '请输入完整的后台地址。'
    }
  }
  if (!inviteCode.trim()) {
    return '请输入激活码。'
  }
  return null
}

export function buildActivationInput(
  baseURL: string,
  inviteCode: string,
): JobConfigActivationInput {
  return {
    base_url: baseURL.trim(),
    invite_code: inviteCode.trim(),
  }
}
