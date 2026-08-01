// 诊断台共用的取值与文案转换。纯函数，不碰 api、不碰 React。
import type { AccountView, TimeValue } from '../api'

export function errorText(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason)
}

export function toDate(value: TimeValue): Date | null {
  if (value === null || value === '' || value === 0) return null
  if (typeof value === 'number') {
    const millis = value < 10_000_000_000 ? value * 1000 : value
    const date = new Date(millis)
    return Number.isNaN(date.getTime()) ? null : date
  }
  const date = new Date(value)
  return Number.isNaN(date.getTime()) || date.getFullYear() <= 1 ? null : date
}

export function clock(value: TimeValue, fallback = '未安排'): string {
  const date = toDate(value)
  if (!date) return fallback
  return new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false }).format(date)
}

export function dateTime(value: TimeValue, fallback = '—'): string {
  const date = toDate(value)
  if (!date) return fallback
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(date)
}

// 帧和命令都是同一天里挨着发生的事，日期是噪音；毫秒才是信号——
// 一条命令的 cmd/ack/result 常常落在同一秒里，秒级精度分不出先后。
export function timeOfDay(ms: number, withMillis = false): string {
  const date = toDate(ms)
  if (!date) return '—'
  const hh = String(date.getHours()).padStart(2, '0')
  const mm = String(date.getMinutes()).padStart(2, '0')
  const ss = String(date.getSeconds()).padStart(2, '0')
  if (!withMillis) return `${hh}:${mm}:${ss}`
  return `${hh}:${mm}:${ss}.${String(date.getMilliseconds()).padStart(3, '0')}`
}

// 命令耗时。未终局回空串——那是"还在跑"，不是"用了 0 毫秒"。
export function elapsed(startMs: number, endMs: number): string {
  if (!startMs || !endMs || endMs < startMs) return ''
  const span = endMs - startMs
  if (span < 1000) return `${span}ms`
  if (span < 60_000) return `${(span / 1000).toFixed(1)}s`
  return `${Math.floor(span / 60_000)}m${Math.round((span % 60_000) / 1000)}s`
}

export function approximateTime(ms: number | null): string {
  if (!ms) return '时间未知'
  return dateTime(ms)
}

export function shortRef(value: string, front = 8): string {
  if (!value) return '—'
  return value.length > front + 5 ? `${value.slice(0, front)}…${value.slice(-4)}` : value
}

export function accountIdentity(account: Pick<AccountView, 'platform' | 'accountRef'>): string {
  return JSON.stringify([account.platform, account.accountRef])
}

export function identityLabel(state: string): string {
  const labels: Record<string, string> = {
    verified: '身份已核验', bound: '身份已核验', invalid: '需重新绑定', unbound: '等待绑定',
    unobservable: '当前页面不可核验', stale: '核验已过期', unknown: '等待核验',
  }
  return labels[state] ?? (state || '等待核验')
}

export function effectiveIdentityState(account: Pick<AccountView, 'identityState' | 'identityCurrent'>): string {
  if (!account.identityCurrent && (account.identityState === 'verified' || account.identityState === 'bound')) {
    return 'stale'
  }
  return account.identityState
}

export function roundStatus(status: string): string {
  const labels: Record<string, string> = {
    running: '巡检中', ok: '完成', completed: '完成', failed: '失败', paused: '已暂停', cancelled: '已取消',
  }
  return labels[status] ?? (status || '无记录')
}

export function directionLabel(direction: string): string {
  const labels: Record<string, string> = { incoming: '对方', in: '对方', outgoing: '我方', out: '我方', system: '系统' }
  return labels[direction] ?? (direction || '未知')
}

export function isTracked(state: string): boolean {
  return state === 'pending' || state === 'adopted'
}

export function trackingLabel(state: string): string {
  if (state === 'pending') return '基线待建立'
  if (state === 'adopted') return '跟踪中'
  return '未跟踪'
}

export function pauseReasonLabel(reason: string): string {
  const labels: Record<string, string> = {
    userPaused: '人工立即暂停', userStopped: '人工停止今天巡检', midnight: '已到午夜自动收班',
    handOffline: '手离线', identityInvalid: '身份需要重新核验', pageUnavailable: '页面暂不可用',
    handManualReview: '手侧异常，需人工复核后重开',
  }
  return labels[reason] ?? (reason || '未暂停')
}

export function detailText(detail: unknown): string {
  if (typeof detail === 'string') return detail
  if (detail === null || detail === undefined) return '无补充信息'
  try { return JSON.stringify(detail) } catch { return String(detail) }
}
