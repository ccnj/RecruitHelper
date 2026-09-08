// 屏幕顶层状态栏(2026-09-08 甲方裁决,出口 docs/boss/状态栏出口-2026-09-08.md)。
//
// 它跑在 Electron 的第二个窗里:透明、置顶、不可聚焦、鼠标穿透。这里只画,
// 不碰窗口——两种模式切换全靠渲染多少字段,不 resize、不 show/hide,因为
// 任何窗口级动作都可能在 BOSS 打字期间抢一次焦点。
//
// 客户版:首页那句状态(与产品首页同一份推导)+ 插件连接点 + 今日三个数。
// 开发版:多画最近命令五行与一行摘要;开关在脑里,这里每几秒读一次。
// 数据只在内存里,不写 localStorage/IndexedDB(同机产品 UI 业务投影例外的前端义务)。
import { useEffect, useRef, useState } from 'react'
import { api, appGet, type HandHealth } from '../api'
import {
  adaptOverviewSnapshot,
  type AppOverviewResponse,
  type OverviewSnapshotView,
} from '../product/data'
import { foldLedgerRows, type OverlayLedgerRow } from './ledger-rows'

const OVERVIEW_INTERVAL_MS = 5_000
const SETTINGS_INTERVAL_MS = 5_000
const LEDGER_INTERVAL_MS = 3_000
const HEALTH_INTERVAL_MS = 5_000
const LEDGER_FETCH_LIMIT = 12
const LEDGER_ROWS = 5

// usePoll:intervalMs 为 0 即停;上一轮没回来不叠下一轮。
function usePoll(intervalMs: number, task: () => Promise<void>) {
  const taskRef = useRef(task)
  taskRef.current = task
  useEffect(() => {
    if (intervalMs <= 0) return
    let cancelled = false
    let running = false
    const tick = async () => {
      if (running || cancelled) return
      running = true
      try {
        await taskRef.current()
      } finally {
        running = false
      }
    }
    void tick()
    const timer = window.setInterval(() => void tick(), intervalMs)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [intervalMs])
}

export function OverlayApp() {
  const [view, setView] = useState<OverviewSnapshotView | null>(null)
  const [reachable, setReachable] = useState(false)
  const [detail, setDetail] = useState(false)
  const [ledger, setLedger] = useState<OverlayLedgerRow[]>([])
  const [hands, setHands] = useState<HandHealth[]>([])
  const [suspectCount, setSuspectCount] = useState<number | null>(null)

  usePoll(OVERVIEW_INTERVAL_MS, async () => {
    try {
      const response = await appGet<AppOverviewResponse>('/app/overview')
      setView(adaptOverviewSnapshot(response))
      setReachable(true)
    } catch {
      setReachable(false)
    }
  })
  usePoll(SETTINGS_INTERVAL_MS, async () => {
    try {
      setDetail((await api.statusBarSettings()).detailEnabled === true)
    } catch {
      // 读不到开关就维持上一次的模式;脑整体不可达时顶行已经在说"未就绪"。
    }
  })
  usePoll(detail ? LEDGER_INTERVAL_MS : 0, async () => {
    try {
      const { ledger: rows } = await api.ledgerBrief(LEDGER_FETCH_LIMIT)
      setLedger(foldLedgerRows(rows, Date.now(), LEDGER_ROWS))
    } catch {
      setLedger([])
    }
  })
  usePoll(detail ? HEALTH_INTERVAL_MS : 0, async () => {
    try {
      const [health, suspects] = await Promise.all([api.handsHealth(), api.suspects()])
      setHands(health.hands ?? [])
      setSuspectCount((suspects.suspects ?? []).length)
    } catch {
      setHands([])
      setSuspectCount(null)
    }
  })

  return (
    <div className={`ov-bar${detail ? ' is-detail' : ''}`}>
      <TopLine view={reachable ? view : null} />
      {detail && <LedgerLines rows={ledger} />}
      {detail && <FooterLine view={reachable ? view : null} hands={hands} suspectCount={suspectCount} />}
    </div>
  )
}

function TopLine({ view }: { view: OverviewSnapshotView | null }) {
  if (!view) {
    return (
      <div className="ov-top">
        <span className="ov-dot is-neutral" />
        <span className="ov-label is-idle">客户端未就绪</span>
        <span className="ov-hint">脑服务没有响应,稍后自动重试。</span>
      </div>
    )
  }
  const plugin = view.connections.find((item) => item.label === 'Chrome 插件')
  const pluginTone = plugin?.tone ?? 'neutral'
  const { homeStatus, todayActivity } = view.overview
  return (
    <div className="ov-top">
      <span className={`ov-dot is-${pluginTone}`} />
      {plugin && pluginTone !== 'success' && (
        <span className="ov-plugin">插件{plugin.value}</span>
      )}
      <span className={`ov-label is-${homeStatus.tone}`}>{homeStatus.label}</span>
      <span className="ov-hint">{homeStatus.hint}</span>
      <span className="ov-nums">
        今日 招呼 <b>{metric(todayActivity.greeted)}</b>
        {' · '}换微信 <b>{metric(todayActivity.newWechat)}</b>
        {' · '}约面 <b>{metric(todayActivity.newInterviews)}</b>
      </span>
    </div>
  )
}

function LedgerLines({ rows }: { rows: OverlayLedgerRow[] }) {
  return (
    <div className="ov-ledger">
      {rows.length === 0 && <div className="ov-row is-empty">账本暂无命令</div>}
      {rows.map((row) => (
        <div key={row.key} className={`ov-row${row.effectful ? ' is-effectful' : ' is-readonly'}`}>
          <span className="ov-time">{row.time}</span>
          <span className="ov-name">{row.name}{row.count > 1 ? ` ×${row.count}` : ''}</span>
          <span className="ov-target">{row.target}</span>
          <span className={`ov-status is-${row.tone}`}>{row.statusLabel}</span>
          <span className="ov-elapsed">{row.elapsedLabel}</span>
        </div>
      ))}
    </div>
  )
}

function FooterLine({
  view, hands, suspectCount,
}: {
  view: OverviewSnapshotView | null
  hands: HandHealth[]
  suspectCount: number | null
}) {
  const funnel = view?.overview.funnel
  const collect = funnel?.stages.find((stage) => stage.key === 'collect')
  const send = funnel?.stages.find((stage) => stage.key === 'send')
  const failed = funnel?.stages.reduce((sum, stage) => sum + stage.failed, 0) ?? 0
  const hand = hands.find((item) => item.online) ?? hands[0]
  return (
    <div className="ov-foot">
      <span>
        {funnel?.stage
          ? <>批次 采集 <b>{collect?.completed ?? 0}</b>/{collect?.target ?? '?'} · 已发 <b>{send?.completed ?? 0}</b> · 失败 <b>{failed}</b></>
          : '无采集批次'}
      </span>
      <span>
        {hand?.online
          ? <>插件 心跳 <b>{(hand.lastHbAgoMs / 1000).toFixed(1)}s</b> · {hand.contractMatch ? '契约一致' : '契约不一致'} · journal <b>{hand.journalOpen}</b> · outbox <b>{hand.outboxPending}</b></>
          : '插件离线'}
      </span>
      <span>待裁决 <b>{suspectCount ?? '—'}</b></span>
    </div>
  )
}

function metric(value: number | null): string {
  return value === null ? '—' : String(value)
}
