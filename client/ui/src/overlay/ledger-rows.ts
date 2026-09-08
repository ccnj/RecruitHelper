// 状态栏开发版"最近命令"那几行的纯推导:账本行 → 可画的行。
//
// 放在组件外面是为了能在 Node 下单测——会出错的是折行与状态归类,不是那几个
// span 长什么样。输入就是 /admin/ledger?brief=1 回来的行(最新在前)。
import type { LedgerRow } from '../api'

export type LedgerRowBrief = Pick<
  LedgerRow,
  'msgId' | 'name' | 'class' | 'status' | 'errorCode' | 'target' | 'createdAtMs' | 'terminalAtMs'
>

export type OverlayLedgerTone = 'running' | 'ok' | 'failed' | 'suspect' | 'void'

export interface OverlayLedgerRow {
  key: string
  time: string
  name: string
  target: string
  statusLabel: string
  tone: OverlayLedgerTone
  elapsedLabel: string
  // 连续同名只读命令折成一行,count 是折了几条;非只读永远是 1。
  count: number
  effectful: boolean
}

const RUNNING = new Set(['queued', 'sent', 'accepted', 'pendingReconcile', 'verifying'])
const OK = new Set(['ok', 'resolvedOk'])
const VOID = new Set(['void', 'canceled'])

export function ledgerStatus(
  row: Pick<LedgerRowBrief, 'status' | 'errorCode'>,
): { label: string; tone: OverlayLedgerTone } {
  if (RUNNING.has(row.status)) return { label: '进行中', tone: 'running' }
  if (OK.has(row.status)) return { label: '成功', tone: 'ok' }
  if (row.status === 'suspect') return { label: 'suspect', tone: 'suspect' }
  if (VOID.has(row.status)) return { label: row.status === 'void' ? '作废' : '取消', tone: 'void' }
  // failed / expired / rejected / resolvedFailed 与任何没见过的终局都按失败画,
  // 错误码原样带上——状态栏是给排障看的,码比中文有用。
  const code = (row.errorCode ?? '').trim()
  const base = row.status === 'expired' ? '超时' : row.status === 'rejected' ? '被拒' : '失败'
  return { label: code ? `${base} ${code}` : base, tone: 'failed' }
}

export function elapsedLabel(createdAtMs: number, terminalAtMs: number, nowMs: number): string {
  if (!createdAtMs) return ''
  const end = terminalAtMs > 0 ? terminalAtMs : nowMs
  const ms = Math.max(0, end - createdAtMs)
  if (ms < 10_000) return `${(ms / 1000).toFixed(1)}s`
  if (ms < 100_000) return `${Math.round(ms / 1000)}s`
  return `${Math.floor(ms / 60_000)}m`
}

export function clockLabel(ms: number): string {
  if (!ms) return '—'
  const d = new Date(ms)
  const two = (n: number) => String(n).padStart(2, '0')
  return `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`
}

// foldLedgerRows:最新在前;连续、同名、同状态的只读命令折成一行,免得巡检期间
// 几秒一条的 readList 把五行全刷成它。有副作用的命令永远单独一行。
export function foldLedgerRows(
  rows: readonly LedgerRowBrief[],
  nowMs: number,
  max = 5,
): OverlayLedgerRow[] {
  const out: OverlayLedgerRow[] = []
  for (const row of rows) {
    const status = ledgerStatus(row)
    const readonly = row.class === 'readonly'
    const last = out[out.length - 1]
    if (
      last && readonly && !last.effectful
      && last.name === row.name && last.statusLabel === status.label
    ) {
      last.count += 1
      continue
    }
    if (out.length >= max) break
    out.push({
      key: row.msgId,
      time: clockLabel(row.createdAtMs),
      name: row.name,
      target: (row.target ?? '').trim() || '—',
      statusLabel: status.label,
      tone: status.tone,
      elapsedLabel: elapsedLabel(row.createdAtMs, row.terminalAtMs, nowMs),
      count: 1,
      effectful: !readonly,
    })
  }
  return out
}
