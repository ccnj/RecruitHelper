import type { SendIntentView } from './api'

export function sendStateLabel(view: SendIntentView | null): string {
  if (!view) return ''
  const intentTerminal = ['ok', 'failed', 'suspect', 'expired', 'canceled', 'resolvedOk', 'resolvedFailed']
  const state = intentTerminal.includes(view.status) ? view.status : view.commandStatus || view.status
  const labels: Record<string, string> = {
    dispatching: '发送意图已入账，正在交给手', reconciling: '连接已变化，正在安全对账',
    verifying: '发送结果未知，正在回读平台记录',
    queued: '已安全入账，等待手执行', sent: '已交给手，等待接收', accepted: '手已接收，正在执行',
    executing: '正在发送并观察页面结果', pendingReconcile: '连接已变化，正在安全对账',
    pendingVerification: '结果未知，正在回读验证', ok: '页面与本地账本均已确认',
    resolvedOk: '已确认发生', failed: '未完成', resolvedFailed: '已确认未发生',
    suspect: '结果仍有歧义，已停止并转人工', expired: '意图已过期，未继续发送',
  }
  const attempts = view.verificationAttempts ? ` · 已验证 ${view.verificationAttempts} 轮` : ''
  return `${labels[state] || state || '状态未知'}${attempts}`
}

export function sendTerminal(view: SendIntentView | null): boolean {
  if (!view) return false
  const states = [view.status, view.commandStatus]
  return states.some((state) => state && [
    'ok', 'failed', 'suspect', 'expired', 'canceled', 'resolvedOk', 'resolvedFailed',
  ].includes(state))
}

export function sendSucceeded(view: SendIntentView | null): boolean {
  return Boolean(view && [view.status, view.commandStatus].some((state) => state === 'ok' || state === 'resolvedOk'))
}

export function sendSuspect(view: SendIntentView | null): boolean {
  return Boolean(view && (view.status === 'suspect' || view.commandStatus === 'suspect'))
}
