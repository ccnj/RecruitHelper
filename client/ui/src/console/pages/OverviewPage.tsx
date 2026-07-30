// 总览：派一条命令，紧接着在同一屏看到帧流与账本落地。
// 三块留在一页，是因为它们本来就是同一个调试闭环。
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, FrameEvent, HandHealth, LedgerRow } from '../../api'
import { errorText, shortRef } from '../format'
import { DiagnosticCard } from '../shared/Primitives'
import { usePolling } from '../usePolling'

export function OverviewPage({ hands, busy, canProcessCurrent, onProcessCurrent }: {
  hands: HandHealth[]
  busy: string
  canProcessCurrent: boolean
  onProcessCurrent: () => void
}) {
  return (
    <>
      <QuickActions
        hands={hands}
        busy={busy}
        canProcessCurrent={canProcessCurrent}
        onProcessCurrent={onProcessCurrent}
      />
      <div className="dc-split">
        <Ledger />
        <Frames />
      </div>
    </>
  )
}

function QuickActions({ hands, busy, canProcessCurrent, onProcessCurrent }: {
  hands: HandHealth[]
  busy: string
  canProcessCurrent: boolean
  onProcessCurrent: () => void
}) {
  const online = hands.filter((hand) => hand.online)
  const [handId, setHandId] = useState('')
  const [outcome, setOutcome] = useState('ok')
  const [last, setLast] = useState('')
  const target = handId || online[0]?.handId || ''
  const send = async (name: string, args: unknown) => {
    if (!target) { setLast('无在线手'); return }
    try {
      const result = await api.dispatch(target, name, args)
      setLast(result.msgId ? `${name} → ${result.msgId.slice(0, 14)}…` : result.error || '失败')
    } catch (reason) { setLast(errorText(reason)) }
  }
  return (
    <section className="dc-actions" aria-label="快捷动作">
      <button
        className="primary-button"
        disabled={!canProcessCurrent || busy !== ''}
        onClick={onProcessCurrent}
        title="真人先在平台打开目标会话，脑只处理这一个"
      >
        {busy === 'process-current' ? '正在处理当前会话…' : '处理当前会话'}
      </button>
      <span className="dc-actions-sep" />
      <select
        value={target}
        aria-label="目标手"
        onChange={(event) => setHandId(event.target.value)}
      >
        {online.length === 0 && <option value="">（无在线手）</option>}
        {online.map((hand) => <option key={hand.handId} value={hand.handId}>{shortRef(hand.handId, 10)}</option>)}
      </select>
      <button onClick={() => send('debug.ping', { echo: 'hi' })}>ping</button>
      <button onClick={() => send('debug.switchWindow', {})}>switchWindow</button>
      <button onClick={() => send('debug.slowEcho', { ms: 500, outcome })}>slowEcho</button>
      <select value={outcome} aria-label="slowEcho 结果" onChange={(event) => setOutcome(event.target.value)}>
        <option value="ok">ok</option>
        <option value="failed">failed → suspect</option>
        <option value="silent">silent → suspect</option>
      </select>
      <span className="hint mono">{last}</span>
    </section>
  )
}

function Ledger() {
  const ledger = usePolling(api.ledger, 1200, 'diagnostic-ledger')
  const statusClass = (status: string) => status === 'ok' ? 'ok' : status === 'suspect' ? 'bad' : ['failed', 'void', 'rejected', 'expired'].includes(status) ? 'warn' : 'dim'
  const rows = ledger.data?.ledger ?? []
  return (
    <DiagnosticCard title="命令账本">
      <div className="dc-ledger-scroll">
        <table>
          <thead><tr><th>msgId</th><th>原语</th><th>类</th><th>状态</th><th>试</th></tr></thead>
          <tbody>
            {rows.slice(0, 15).map((row: LedgerRow) => (
              <tr key={row.msgId}>
                <td className="mono dim">{row.msgId.slice(0, 14)}…</td><td>{row.name}</td><td className="dim">{row.class}</td>
                <td className={statusClass(row.status)}>{row.status}{row.errorCode ? ` (${row.errorCode})` : ''}</td><td className="dim">{row.attempt}</td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr><td className="dim" colSpan={5}>{ledger.error ? `读取失败 · ${ledger.error}` : '暂无命令记录'}</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </DiagnosticCard>
  )
}

function Frames() {
  const [frames, setFrames] = useState<FrameEvent[]>([])
  const [streamError, setStreamError] = useState('')
  const boxRef = useRef<HTMLDivElement>(null)
  const onEvent = useCallback((frame: FrameEvent) => {
    setStreamError('')
    setFrames((previous) => previous.some((item) => item.seq === frame.seq)
      ? previous
      : [...previous.slice(-119), frame])
  }, [])
  useEffect(() => {
    return api.subscribeFrames(onEvent, setStreamError)
  }, [onEvent])
  useEffect(() => { boxRef.current?.scrollTo(0, boxRef.current.scrollHeight) }, [frames])
  return (
    <DiagnosticCard title="协议帧观测台（实时）">
      <div className="frames mono" ref={boxRef}>
        {frames.map((frame) => (
          <div key={frame.seq} className="frame-line">
            <span className={frame.dir === 'out' ? 'out' : 'in'}>{frame.dir === 'out' ? '脑→手' : '手→脑'}</span>
            <span className="kind">{frame.kind}</span><span className="dim">{frame.handId || '—'}</span>
            {frame.ref && <span className="dim">ref={frame.ref.slice(0, 12)}…</span>}
          </div>
        ))}
        {frames.length === 0 && <div className="dim">等待帧…（派发命令后可见 cmd / ack / result）</div>}
        {streamError && <div className="bad">实时帧暂时断开 · {streamError}</div>}
      </div>
    </DiagnosticCard>
  )
}
