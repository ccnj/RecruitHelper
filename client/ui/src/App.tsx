import { useCallback, useEffect, useRef, useState } from 'react'
import {
  api, ADMIN_BASE,
  Health, Pending, HandHealth, LedgerRow, Suspect, FrameEvent,
} from './api'

function usePoll<T>(fn: () => Promise<T>, ms: number): [T | undefined, string | null] {
  const [data, setData] = useState<T>()
  const [err, setErr] = useState<string | null>(null)
  useEffect(() => {
    let alive = true
    const tick = async () => {
      try {
        const d = await fn()
        if (alive) { setData(d); setErr(null) }
      } catch (e) {
        if (alive) setErr(String(e))
      }
    }
    tick()
    const id = setInterval(tick, ms)
    return () => { alive = false; clearInterval(id) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ms])
  return [data, err]
}

export function App() {
  const [health, healthErr] = usePoll<Health>(api.health, 1500)
  return (
    <div className="wrap">
      <header>
        <h1>招聘助手 · 客户端(脑)</h1>
        <div className="statusbar">
          {healthErr ? (
            <span className="bad">脑服务未连接 · {ADMIN_BASE}</span>
          ) : health ? (
            <>
              <span className="ok">脑在线</span>
              <span>proto v{health.proto}</span>
              <span>配对窗 {health.pairingOpen ? '开' : '关'}</span>
              <span>在线手 {health.activeHands.length}</span>
              <span className="dim mono">{health.contract.slice(0, 22)}…</span>
            </>
          ) : (
            <span className="dim">连接中…</span>
          )}
        </div>
      </header>

      <div className="grid">
        <Pairing />
        <Hands />
        <Commands />
        <Suspects />
        <Ledger />
        <Frames />
      </div>
    </div>
  )
}

function Card({ title, children, wide }: { title: string; children: React.ReactNode; wide?: boolean }) {
  return (
    <section className={'card' + (wide ? ' wide' : '')}>
      <h2>{title}</h2>
      {children}
    </section>
  )
}

function Pairing() {
  const [p] = usePoll(api.pending, 1000)
  const [msg, setMsg] = useState('')
  return (
    <Card title="配对">
      <button onClick={async () => { await api.openPairing(); setMsg('配对窗已开启(60 秒)') }}>开启配对窗</button>
      <span className="hint">{p?.open ? '窗口开启中' : '窗口关闭'} {msg && '· ' + msg}</span>
      <ul className="list">
        {(p?.pending ?? []).map((it: Pending) => (
          <li key={it.bootId}>
            <span className="mono dim">{it.origin.slice(0, 28)}…</span>
            <span>bootId {it.bootId}</span>
            <span className="dim">{it.caps.length} 能力</span>
            <button onClick={async () => { const r = await api.confirm(it.origin, it.bootId); setMsg(r.handId ? `已签发 ${r.handId}` : r.error || '') }}>确认</button>
          </li>
        ))}
        {p && p.pending.length === 0 && <li className="dim">无待配对</li>}
      </ul>
    </Card>
  )
}

function Hands() {
  const [h] = usePoll(api.handsHealth, 1500)
  return (
    <Card title="手(在线状态)">
      <table>
        <thead><tr><th>handId</th><th>状态</th><th>健康</th><th>心跳</th></tr></thead>
        <tbody>
          {(h?.hands ?? []).map((x: HandHealth) => (
            <tr key={x.handId}>
              <td>{x.handId}</td>
              <td className={x.online ? 'ok' : 'dim'}>{x.online ? '在线' : '离线'}</td>
              <td className={x.health === 'stalled' ? 'bad' : ''}>{x.health}</td>
              <td className="dim">{x.online ? `${Math.round(x.lastHbAgoMs / 1000)}s 前` : '-'}</td>
            </tr>
          ))}
          {h && h.hands.length === 0 && <tr><td colSpan={4} className="dim">暂无手</td></tr>}
        </tbody>
      </table>
    </Card>
  )
}

function Commands() {
  const [h] = usePoll(api.handsHealth, 2000)
  const online = (h?.hands ?? []).filter((x) => x.online)
  const [handId, setHandId] = useState('')
  const [outcome, setOutcome] = useState('ok')
  const [last, setLast] = useState('')
  const target = handId || online[0]?.handId || ''

  const send = async (name: string, args: unknown) => {
    if (!target) { setLast('无在线手'); return }
    const r = await api.dispatch(target, name, args)
    setLast(r.msgId ? `${name} → ${r.msgId.slice(0, 14)}…` : r.error || '失败')
  }

  return (
    <Card title="派发命令(测试)">
      <div className="row">
        <label>目标手</label>
        <select value={target} onChange={(e) => setHandId(e.target.value)}>
          {online.length === 0 && <option value="">(无在线手)</option>}
          {online.map((x) => <option key={x.handId} value={x.handId}>{x.handId}</option>)}
        </select>
      </div>
      <div className="btns">
        <button onClick={() => send('debug.ping', { echo: 'hi' })}>debug.ping</button>
        <button onClick={() => send('debug.switchWindow', {})}>debug.switchWindow(切标签页)</button>
      </div>
      <div className="row">
        <button onClick={() => send('debug.slowEcho', { ms: 500, outcome })}>debug.slowEcho</button>
        <select value={outcome} onChange={(e) => setOutcome(e.target.value)}>
          <option value="ok">ok</option>
          <option value="failed">failed(possible→suspect)</option>
          <option value="silent">silent(超时→suspect)</option>
        </select>
      </div>
      <div className="hint mono">{last}</div>
    </Card>
  )
}

function Suspects() {
  const [s] = usePoll(api.suspects, 1500)
  const [msg, setMsg] = useState('')
  const verdict = async (msgId: string, v: 'resolvedOk' | 'resolvedFailed') => {
    const r = await api.verdict(msgId, v)
    setMsg(r.error ? `拒:${r.error}` : `已裁决 ${v}`)
  }
  const rows = s?.suspects ?? []
  return (
    <Card title={`suspect 队列(转人工)${rows.length ? ' · ' + rows.length : ''}`}>
      {rows.length === 0 ? <p className="dim">无 suspect</p> : (
        <ul className="list">
          {rows.map((x: Suspect) => (
            <li key={x.msgId}>
              <span>{x.name}</span>
              <span className="dim">{x.reason}</span>
              <button onClick={() => verdict(x.msgId, 'resolvedOk')}>确认已发生</button>
              <button onClick={() => verdict(x.msgId, 'resolvedFailed')}>确认未发生</button>
            </li>
          ))}
        </ul>
      )}
      <div className="hint">{msg}</div>
    </Card>
  )
}

function Ledger() {
  const [l] = usePoll(api.ledger, 1200)
  const cls = (s: string) => (s === 'ok' ? 'ok' : s === 'suspect' ? 'bad' : ['failed', 'void', 'rejected', 'expired'].includes(s) ? 'warn' : 'dim')
  return (
    <Card title="命令账本" wide>
      <table>
        <thead><tr><th>msgId</th><th>原语</th><th>类</th><th>状态</th><th>试</th></tr></thead>
        <tbody>
          {(l?.ledger ?? []).slice(0, 15).map((r: LedgerRow) => (
            <tr key={r.msgId}>
              <td className="mono dim">{r.msgId.slice(0, 14)}…</td>
              <td>{r.name}</td>
              <td className="dim">{r.class}</td>
              <td className={cls(r.status)}>{r.status}{r.errorCode ? ` (${r.errorCode})` : ''}</td>
              <td className="dim">{r.attempt}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  )
}

function Frames() {
  const [frames, setFrames] = useState<FrameEvent[]>([])
  const boxRef = useRef<HTMLDivElement>(null)
  const onEvt = useCallback((e: MessageEvent) => {
    try {
      const ev = JSON.parse(e.data) as FrameEvent
      setFrames((prev) => [...prev.slice(-120), ev])
    } catch { /* ignore */ }
  }, [])
  useEffect(() => {
    const es = new EventSource(api.framesUrl())
    es.onmessage = onEvt
    return () => es.close()
  }, [onEvt])
  useEffect(() => { boxRef.current?.scrollTo(0, boxRef.current.scrollHeight) }, [frames])
  return (
    <Card title="协议帧观测台(实时)" wide>
      <div className="frames mono" ref={boxRef}>
        {frames.map((f) => (
          <div key={f.seq} className="frameline">
            <span className={f.dir === 'out' ? 'out' : 'in'}>{f.dir === 'out' ? '脑→手' : '手→脑'}</span>
            <span className="kind">{f.kind}</span>
            <span className="dim">{f.handId || '-'}</span>
            {f.ref && <span className="dim">ref={f.ref.slice(0, 12)}…</span>}
          </div>
        ))}
        {frames.length === 0 && <div className="dim">等待帧…(派发命令后可见 cmd/ack/result)</div>}
      </div>
    </Card>
  )
}
