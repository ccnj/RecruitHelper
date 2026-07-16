// 客户端(脑)UI 的数据层:访问本地脑服务的 admin 端点。纯 fetch,可在 Node 下单测。
// 测试页与未来调度器共用同一命令通道(admin/cmd → 脑派发器),无测试旁路(宪法 3)。

export const ADMIN_BASE =
  (typeof localStorage !== 'undefined' && localStorage.getItem('adminBase')) || 'http://127.0.0.1:17872'

async function get<T>(path: string): Promise<T> {
  const r = await fetch(ADMIN_BASE + path)
  if (!r.ok) throw new Error(`${path}: ${r.status}`)
  return r.json() as Promise<T>
}

async function post<T>(path: string, body?: unknown): Promise<T> {
  const r = await fetch(ADMIN_BASE + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  })
  return r.json() as Promise<T>
}

export interface Health {
  ok: boolean
  proto: number
  contract: string
  pairingOpen: boolean
  activeHands: string[]
}
export interface Pending {
  origin: string
  bootId: string
  extVersion: string
  caps: string[]
  waitingMs: number
}
export interface HandHealth {
  handId: string
  online: boolean
  health: string
  caps: string[]
  lastHbAgoMs: number
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

export const api = {
  health: () => get<Health>('/admin/health'),
  openPairing: () => post<{ open: boolean }>('/admin/pairing/open'),
  pending: () => get<{ open: boolean; pending: Pending[] }>('/admin/pairing/pending'),
  confirm: (origin: string, bootId: string) => post<{ handId?: string; error?: string }>('/admin/pairing/confirm', { origin, bootId }),
  handsHealth: () => get<{ hands: HandHealth[] }>('/admin/hands/health'),
  dispatch: (handId: string, name: string, args: unknown) => post<{ msgId?: string; error?: string }>('/admin/cmd', { handId, name, args }),
  ledger: () => get<{ ledger: LedgerRow[] }>('/admin/ledger'),
  suspects: () => get<{ suspects: Suspect[] }>('/admin/suspects'),
  verdict: (msgId: string, verdict: 'resolvedOk' | 'resolvedFailed') => post<{ error?: string }>('/admin/suspects/verdict', { msgId, verdict }),
  framesUrl: () => ADMIN_BASE + '/admin/frames',
}
