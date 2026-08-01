// 总览：派一条命令，紧接着在同一屏看到帧流与账本落地。
// 三块留在一页，是因为它们本来就是同一个调试闭环。
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, FrameEvent, HandHealth, LedgerRow } from '../../api'
import { PipelinePanel } from '../../product/components/PipelinePanel'
import { readProductData } from '../../product/api'
import type { ProductData } from '../../product/types'
import { elapsed, errorText, shortRef, timeOfDay } from '../format'
import { DiagnosticCard, RawField } from '../shared/Primitives'
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
      <Pipeline />
      <div className="dc-split">
        <Ledger />
        <Frames hands={hands} />
      </div>
    </>
  )
}

// 流程进度 2026-07-31 从客户首页搬到这里。客户看六段阶段条只会把正常的漏斗
// 收窄("筛选 18/30")当成故障;排障时它仍是最快看清卡在哪一段的东西。数据走
// 产品端 /app/overview——诊断台与产品页同在一个渲染进程,两套 API 都能调。
function Pipeline() {
  const [funnel, setFunnel] = useState<ProductData['overview']['funnel'] | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let alive = true
    const read = () => {
      readProductData()
        .then((data) => {
          if (!alive) return
          setFunnel(data.overview.funnel)
          setError('')
        })
        .catch((cause) => {
          if (alive) setError(errorText(cause))
        })
    }
    read()
    const timer = setInterval(read, 4000)
    return () => {
      alive = false
      clearInterval(timer)
    }
  }, [])
  if (error !== '') {
    return (
      <DiagnosticCard title="流程进度">
        <RawField label="读取失败" value={error} />
      </DiagnosticCard>
    )
  }
  if (funnel === null) return null
  return <div className="dc-pipeline-host">{<PipelinePanel funnel={funnel} />}</div>
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

function statusTone(status: string): string {
  if (status === 'ok') return 'ok'
  if (status === 'suspect') return 'bad'
  return ['failed', 'void', 'rejected', 'expired', 'canceled'].includes(status) ? 'warn' : 'dim'
}

// 命令账本。收起时一行：什么时候、什么原语、对谁、结果、花了多久——这是
// 扫一眼要回答的全部问题。msgId 挪进展开层：它换行占两行高，是这张表最占
// 地方的东西，而绝大多数时候没人看它。
function Ledger() {
  const ledger = usePolling(api.ledger, 1200, 'diagnostic-ledger')
  const rows = ledger.data?.ledger ?? []
  return (
    <DiagnosticCard title="命令账本" aside={rows.length > 0 ? `最近 ${rows.length}` : undefined}>
      <div className="dc-ledger-scroll">
        {rows.map((row: LedgerRow) => <LedgerEntry key={row.msgId} row={row} />)}
        {rows.length === 0 && (
          <p className="dc-stream-empty">{ledger.error ? `读取失败 · ${ledger.error}` : '暂无命令记录'}</p>
        )}
      </div>
    </DiagnosticCard>
  )
}

function LedgerEntry({ row }: { row: LedgerRow }) {
  const [open, setOpen] = useState(false)
  const took = elapsed(row.createdAtMs, row.terminalAtMs)
  return (
    <details className="dc-ledger-row" onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>
        <span className="dc-ledger-at dim">{timeOfDay(row.createdAtMs)}</span>
        <span className="dc-ledger-name">{row.name}</span>
        <span className={`dc-ledger-class is-${row.class}`}>{row.class}</span>
        <span className="dc-ledger-target">{row.target}</span>
        <span className="dc-ledger-took dim">{took || (row.terminalAtMs ? '' : '进行中')}</span>
        {/* 状态放行尾：它是唯一变宽的一格（"ok" 到 "failed (ELEMENT_UNRESOLVED)"），
            夹在中间会把每行的目标挤成不同长度，列一乱就没法竖着扫失败。 */}
        <span
          className={`dc-ledger-status ${statusTone(row.status)}`}
          title={row.errorCode ? `${row.status} (${row.errorCode})` : row.status}
        >
          {row.status}{row.errorCode ? ` (${row.errorCode})` : ''}
        </span>
      </summary>

      {open && <>
        <dl className="dc-ledger-facts">
          {row.summary && (<><dt>内容</dt><dd className="dc-ledger-body">{row.summary}</dd></>)}
          <dt>命令</dt>
          <dd>
            <code>{shortRef(row.msgId, 12)}</code>
            {row.attempt > 1 && <> · 第 {row.attempt} 次发送</>}
            {row.deadlineMs > 0 && <> · deadline {timeOfDay(row.deadlineMs)}</>}
          </dd>
          {(row.platform || row.accountRef) && (
            <>
              <dt>账号</dt>
              <dd>{row.platform || '—'} <code>{shortRef(row.accountRef, 10)}</code> · 手 <code>{shortRef(row.handId, 10)}</code></dd>
            </>
          )}
          {(row.intentId || row.idemKey) && (
            <>
              <dt>幂等</dt>
              <dd>
                {row.intentId && <>意图 <code>{shortRef(row.intentId, 12)}</code> </>}
                {row.idemKey && <>idemKey <code>{shortRef(row.idemKey, 16)}</code></>}
              </dd>
            </>
          )}
          {(row.sideEffect || row.suspectReason) && (
            <>
              <dt>判定</dt>
              <dd>
                {row.sideEffect && <>副作用标注 <code>{row.sideEffect}</code> </>}
                {row.suspectReason && <>· {row.suspectReason}</>}
              </dd>
            </>
          )}
        </dl>
        <LedgerRaw row={row} />
      </>}
    </details>
  )
}

function LedgerRaw({ row }: { row: LedgerRow }) {
  const [open, setOpen] = useState(false)
  return (
    <details className="dc-raw-fold" onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>原始现场</summary>
      {open && <>
        <RawField label="args" value={row.args} />
        <RawField label="guards" value={row.guards} />
        <RawField label="result" value={row.resultBody} />
      </>}
    </details>
  )
}

const heartbeatKinds = new Set(['ping', 'pong'])

// 协议帧观测台。心跳默认不进缓冲区——不是渲染时过滤：ping/pong 每两秒一对，
// 留在 120 条的环里会几分钟内把真正的命令帧全挤掉。代价是打开心跳只看得到
// 从那一刻起的心跳，这是想要的取舍。
function Frames({ hands }: { hands: HandHealth[] }) {
  const [frames, setFrames] = useState<FrameEvent[]>([])
  const [streamError, setStreamError] = useState('')
  const [showHeartbeat, setShowHeartbeat] = useState(false)
  const showHeartbeatRef = useRef(showHeartbeat)
  showHeartbeatRef.current = showHeartbeat
  const boxRef = useRef<HTMLDivElement>(null)
  // 只有一只手时逐行重复 handId 是纯噪音，标题上说一次就够。
  const singleHand = hands.length === 1

  const onEvent = useCallback((frame: FrameEvent) => {
    setStreamError('')
    if (!showHeartbeatRef.current && heartbeatKinds.has(frame.kind)) return
    setFrames((previous) => previous.some((item) => item.seq === frame.seq)
      ? previous
      : [...previous.slice(-119), frame])
  }, [])
  useEffect(() => {
    return api.subscribeFrames(onEvent, setStreamError)
  }, [onEvent])
  useEffect(() => { boxRef.current?.scrollTo(0, boxRef.current.scrollHeight) }, [frames])

  return (
    <DiagnosticCard
      title="协议帧观测台（实时）"
      aside={
        <>
          {singleHand && <span className="mono dim">{shortRef(hands[0].handId, 10)}</span>}
          <label className="dc-frame-toggle">
            <input
              type="checkbox"
              checked={showHeartbeat}
              onChange={(event) => setShowHeartbeat(event.target.checked)}
            />
            心跳
          </label>
        </>
      }
    >
      <div className="frames mono" ref={boxRef}>
        {frames.map((frame) => (
          <div key={frame.seq} className="frame-line">
            <span className="frame-at dim">{timeOfDay(frame.ts, true)}</span>
            <span className={frame.dir === 'out' ? 'out' : 'in'}>{frame.dir === 'out' ? '脑→手' : '手→脑'}</span>
            <span className="kind">{frame.kind}</span>
            {!singleHand && <span className="dim">{shortRef(frame.handId, 8)}</span>}
            <span className="dim frame-ref">
              {frame.ref ? `ref=${shortRef(frame.ref, 10)}` : frame.msgId ? shortRef(frame.msgId, 10) : ''}
            </span>
          </div>
        ))}
        {frames.length === 0 && (
          <div className="dim dc-stream-empty">
            等待帧…（派发命令后可见 cmd / ack / result{showHeartbeat ? '' : '；心跳已隐藏'}）
          </div>
        )}
        {streamError && <div className="bad dc-stream-empty">实时帧暂时断开 · {streamError}</div>}
      </div>
    </DiagnosticCard>
  )
}
