import { useCallback, useEffect, useRef, useState } from 'react'
import {
  api, ADMIN_BASE, SendIntentConflictError, SendIntentRejectedError,
  AccountView, AuditView, ConversationView, FrameEvent, HandHealth, Health,
  LedgerRow, MessageView, MutationResult, SendIntentView, Suspect, TimeValue,
} from './api'
import {
  acknowledgeSendIntent, discardRejectedSendProposal, readSendResume, rememberSendProposal,
} from './send-resume'

interface PollState<T> {
  data: T | undefined
  error: string | null
  loading: boolean
  refresh: () => void
}

function usePolling<T>(load: () => Promise<T>, intervalMs: number, key: string): PollState<T> {
  const loader = useRef(load)
  const previousKey = useRef(key)
  loader.current = load
  const [data, setData] = useState<T>()
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    let alive = true
    let running = false
    if (previousKey.current !== key) {
      previousKey.current = key
      setData(undefined)
    }
    setLoading(true)
    const run = async () => {
      if (running) return
      running = true
      try {
        const next = await loader.current()
        if (alive) {
          setData(next)
          setError(null)
        }
      } catch (reason) {
        if (alive) setError(errorText(reason))
      } finally {
        running = false
        if (alive) setLoading(false)
      }
    }
    void run()
    const timer = window.setInterval(run, intervalMs)
    return () => {
      alive = false
      window.clearInterval(timer)
    }
  }, [intervalMs, key, revision])

  return { data, error, loading, refresh: () => setRevision((value) => value + 1) }
}

function errorText(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason)
}

function toDate(value: TimeValue): Date | null {
  if (value === null || value === '' || value === 0) return null
  if (typeof value === 'number') {
    const millis = value < 10_000_000_000 ? value * 1000 : value
    const date = new Date(millis)
    return Number.isNaN(date.getTime()) ? null : date
  }
  const date = new Date(value)
  return Number.isNaN(date.getTime()) || date.getFullYear() <= 1 ? null : date
}

function clock(value: TimeValue, fallback = '未安排'): string {
  const date = toDate(value)
  if (!date) return fallback
  return new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false }).format(date)
}

function dateTime(value: TimeValue, fallback = '—'): string {
  const date = toDate(value)
  if (!date) return fallback
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(date)
}

function approximateTime(ms: number | null): string {
  if (!ms) return '时间未知'
  return dateTime(ms)
}

function shortRef(value: string, front = 8): string {
  if (!value) return '—'
  return value.length > front + 5 ? `${value.slice(0, front)}…${value.slice(-4)}` : value
}

function accountIdentity(account: Pick<AccountView, 'platform' | 'accountRef'>): string {
  return JSON.stringify([account.platform, account.accountRef])
}

function identityLabel(state: string): string {
  const labels: Record<string, string> = {
    verified: '身份已核验', bound: '身份已核验', invalid: '需重新绑定', unbound: '等待绑定',
    unobservable: '当前页面不可核验', stale: '核验已过期', unknown: '等待核验',
  }
  return labels[state] ?? (state || '等待核验')
}

function effectiveIdentityState(account: Pick<AccountView, 'identityState' | 'identityCurrent'>): string {
  if (!account.identityCurrent && (account.identityState === 'verified' || account.identityState === 'bound')) {
    return 'stale'
  }
  return account.identityState
}

function roundStatus(status: string): string {
  const labels: Record<string, string> = {
    running: '巡检中', ok: '完成', completed: '完成', failed: '失败', paused: '已暂停', cancelled: '已取消',
  }
  return labels[status] ?? (status || '无记录')
}

function directionLabel(direction: string): string {
  const labels: Record<string, string> = { incoming: '对方', in: '对方', outgoing: '我方', out: '我方', system: '系统' }
  return labels[direction] ?? (direction || '未知')
}

function isTracked(state: string): boolean {
  return state === 'tracked' || state === 'pending' || state === 'adopted'
}

function trackingLabel(state: string): string {
  if (state === 'pending') return '基线待建立'
  if (state === 'adopted' || state === 'tracked') return '跟踪中'
  return '未跟踪'
}

function pauseReasonLabel(reason: string): string {
  const labels: Record<string, string> = {
    userPaused: '人工立即暂停', userStopped: '人工停止今天巡检', midnight: '已到午夜自动收班',
    handOffline: '手离线', identityInvalid: '身份需要重新核验', pageUnavailable: '页面暂不可用',
    handManualReview: '手侧异常，需人工复核后重开',
  }
  return labels[reason] ?? (reason || '未暂停')
}

function detailText(detail: unknown): string {
  if (typeof detail === 'string') return detail
  if (detail === null || detail === undefined) return '无补充信息'
  try { return JSON.stringify(detail) } catch { return String(detail) }
}

export function App() {
  const health = usePolling<Health>(api.health, 1800, 'health')
  const hands = usePolling(api.handsHealth, 2200, 'hands')
  const accounts = usePolling(api.accounts, 2400, 'accounts')
  const [selectedAccountKey, setSelectedAccountKey] = useState('')
  const [selectedConversationRef, setSelectedConversationRef] = useState('')
  const [activityTab, setActivityTab] = useState<'messages' | 'audits'>('messages')
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState<{ kind: 'ok' | 'bad'; text: string } | null>(null)
  const [diagnosticsOpen, setDiagnosticsOpen] = useState(false)

  const accountRows = accounts.data?.accounts ?? []
  const selectedAccount = accountRows.find((row) => accountIdentity(row) === selectedAccountKey) ?? null
  const accountKey = selectedAccount ? accountIdentity(selectedAccount) : 'none'
  const conversations = usePolling(
    () => selectedAccount
      ? api.conversations(selectedAccount.platform, selectedAccount.accountRef)
      : Promise.resolve({ conversations: [] }),
    2800,
    accountKey,
  )
  const selectedConversation = (conversations.data?.conversations ?? [])
    .find((row) => row.conversationRef === selectedConversationRef) ?? null
  const conversationKey = selectedConversation ? `${accountKey}:${selectedConversation.conversationRef}` : `${accountKey}:none`
  const messages = usePolling(
    () => selectedAccount && selectedConversation
      ? api.messages(selectedAccount.platform, selectedAccount.accountRef, selectedConversation.conversationRef)
      : Promise.resolve({ messages: [] }),
    3200,
    conversationKey,
  )
  const audits = usePolling(
    () => selectedAccount
      ? api.audits(selectedAccount.platform, selectedAccount.accountRef)
      : Promise.resolve({ audits: [] }),
    4200,
    accountKey,
  )

  useEffect(() => {
    if (accountRows.length === 0) {
      setSelectedAccountKey('')
      return
    }
    if (!accountRows.some((row) => accountIdentity(row) === selectedAccountKey)) {
      setSelectedAccountKey(accountIdentity(accountRows[0]))
    }
  }, [accountRows, selectedAccountKey])

  useEffect(() => {
    const rows = conversations.data?.conversations ?? []
    if (rows.length === 0) {
      setSelectedConversationRef('')
      return
    }
    if (!rows.some((row) => row.conversationRef === selectedConversationRef)) {
      setSelectedConversationRef(rows[0].conversationRef)
    }
  }, [accountKey, conversations.data, selectedConversationRef])

  const runMutation = async (key: string, success: string, operation: () => Promise<MutationResult>) => {
    setBusy(key)
    setNotice(null)
    try {
      const result = await operation()
      if (result.error) throw new Error(result.error)
      if (result.account?.accountRef) setSelectedAccountKey(accountIdentity(result.account))
      setNotice({ kind: 'ok', text: success })
      accounts.refresh()
      conversations.refresh()
      messages.refresh()
      audits.refresh()
    } catch (reason) {
      setNotice({ kind: 'bad', text: errorText(reason) })
    } finally {
      setBusy('')
    }
  }

  const target = selectedAccount
    ? { platform: selectedAccount.platform, accountRef: selectedAccount.accountRef }
    : null

  return (
    <div className="app-shell">
      <header className="masthead">
        <div className="brand-block">
          <span className="eyebrow">RecruitHelper · 本地脑</span>
          <h1>值班巡检台</h1>
          <p>今天由脑决定何时看，手只负责如实读取。</p>
        </div>
        <ServiceSeal health={health.data} error={health.error} />
      </header>

      {notice && (
        <div className={`notice ${notice.kind}`} role="status" aria-live="polite">
          <span>{notice.kind === 'ok' ? '已记入值班账' : '本次操作未完成'}</span>
          <strong>{notice.text}</strong>
          <button className="icon-button" onClick={() => setNotice(null)} aria-label="关闭提示">×</button>
        </div>
      )}

      <div className="desk-layout">
        <AccountRail
          accounts={accountRows}
          accountsError={accounts.error}
          hands={hands.data?.hands ?? []}
          selectedKey={selectedAccountKey}
          busy={busy}
          onSelect={setSelectedAccountKey}
          onBind={(platform, handId, accountRef) => runMutation(
            accountRef ? 'rebind' : 'bind',
            accountRef ? '账号绑定已更新' : '当前平台账号已加入值班台',
            () => api.bindAccount(platform, handId, accountRef),
          )}
        />

        <main className="workbench">
          {selectedAccount ? (
            <>
              <AccountOverview
                account={selectedAccount}
                busy={busy}
                onEnable={() => target && runMutation('enable', '已开启今天的自动巡检', () => api.enableAccount(target))}
                onStop={() => target && runMutation('stop', '今天的自动巡检已停止', () => api.stopAccount(target))}
                onPause={() => target && runMutation('pause', '已立即暂停，等待人工恢复', () => api.pauseAccount(target))}
                onRun={() => target && runMutation('run', '已请求立即巡检一轮', () => api.runAccount(target))}
              />

              <div className="ledger-grid">
                <ConversationLedger
                  rows={conversations.data?.conversations ?? []}
                  loading={conversations.loading}
                  error={conversations.error}
                  selectedRef={selectedConversationRef}
                  onSelect={(conversationRef) => {
                    setSelectedConversationRef(conversationRef)
                    setActivityTab('messages')
                  }}
                />
                <ActivityLedger
                  account={selectedAccount}
                  conversation={selectedConversation}
                  messages={messages.data?.messages ?? []}
                  audits={audits.data?.audits ?? []}
                  messagesError={messages.error}
                  auditsError={audits.error}
                  tab={activityTab}
                  busy={busy}
                  onTab={setActivityTab}
                  onTrack={() => selectedConversation && runMutation(
                    'track',
                    '该会话已纳入跟踪；现有消息只建立基线，不算作新消息',
                    () => api.trackConversation(selectedAccount.platform, selectedAccount.accountRef, selectedConversation.conversationRef),
                  )}
                  onSendChanged={() => {
                    conversations.refresh()
                    messages.refresh()
                    audits.refresh()
                  }}
                />
              </div>
            </>
          ) : (
            <EmptyWorkbench loading={accounts.loading} error={accounts.error} />
          )}
        </main>
      </div>

      <details
        className="diagnostics"
        onToggle={(event) => setDiagnosticsOpen(event.currentTarget.open)}
      >
        <summary>
          <span>协议与运行诊断</span>
          <small>M1 工具 · 手状态、命令账本、suspect 与实时帧</small>
        </summary>
        {diagnosticsOpen && <Diagnostics />}
      </details>

      <footer className="footer-note">
        <span>脑服务地址 {ADMIN_BASE}</span>
        <span>所有资料只保存在本机</span>
      </footer>
    </div>
  )
}

function ServiceSeal({ health, error }: { health: Health | undefined; error: string | null }) {
  if (error) {
    return (
      <div className="service-seal is-offline">
        <span className="signal-dot" />
        <div><strong>脑未连接</strong><small>检查本地服务是否启动</small></div>
      </div>
    )
  }
  if (!health) {
    return (
      <div className="service-seal is-waiting">
        <span className="signal-dot" />
        <div><strong>正在接通</strong><small>读取本地值班账</small></div>
      </div>
    )
  }
  return (
    <div className="service-seal is-online">
      <span className="signal-dot" />
      <div>
        <strong>脑在线 · {health.activeHands.length} 只手</strong>
        <small>协议 v{health.proto} · 本地自动登记</small>
      </div>
    </div>
  )
}

function AccountRail({
  accounts, accountsError, hands, selectedKey, busy, onSelect, onBind,
}: {
  accounts: AccountView[]
  accountsError: string | null
  hands: HandHealth[]
  selectedKey: string
  busy: string
  onSelect: (accountKey: string) => void
  onBind: (platform: string, handId: string, accountRef?: string) => void
}) {
  const onlineHands = hands.filter((hand) => hand.online)
  const [handId, setHandId] = useState('')
  const [platform, setPlatform] = useState('')
  const selected = accounts.find((account) => accountIdentity(account) === selectedKey)
  const chosenHand = handId || onlineHands[0]?.handId || ''
  const chosenPlatform = platform.trim()
  const knownPlatforms = Array.from(new Set(accounts.map((account) => account.platform)))

  useEffect(() => {
    if (handId && !onlineHands.some((hand) => hand.handId === handId)) setHandId('')
  }, [handId, onlineHands])

  useEffect(() => {
    if (!platform && knownPlatforms.length === 1) setPlatform(knownPlatforms[0])
  }, [knownPlatforms, platform])

  return (
    <aside className="account-rail" aria-label="招聘账号">
      <div className="rail-heading">
        <span className="section-index">账号轨</span>
        <strong>{accounts.length} 个值班账号</strong>
      </div>

      <div className="account-list">
        {accounts.map((account) => (
          <button
            key={accountIdentity(account)}
            className={`account-ticket ${selectedKey === accountIdentity(account) ? 'is-selected' : ''}`}
            onClick={() => onSelect(accountIdentity(account))}
            aria-pressed={selectedKey === accountIdentity(account)}
          >
            <span className={`status-pin ${account.handOnline ? 'online' : 'offline'}`} />
            <span className="account-ticket-copy">
              <strong>{account.platform}</strong>
              <small className="mono">{shortRef(account.accountRef)}</small>
              <em>{account.pausedReason ? pauseReasonLabel(account.pausedReason) : account.enabledToday ? '今天巡检中' : '今天未开启'}</em>
            </span>
            {(account.unreadTotal ?? 0) > 0 && <span className="unread-stamp">{account.unreadTotal}</span>}
          </button>
        ))}
        {accounts.length === 0 && !accountsError && (
          <div className="rail-empty">还没有账号。填写平台标识并选择一只在线的手，读取当前已登录账号。</div>
        )}
        {accountsError && <InlineError text={accountsError} />}
      </div>

      <div className="bind-station">
        <label htmlFor="bind-platform">平台标识</label>
        <input
          id="bind-platform"
          list="known-platforms"
          value={platform}
          maxLength={64}
          placeholder="由当前手的 program 约定"
          onChange={(event) => setPlatform(event.target.value)}
        />
        <datalist id="known-platforms">
          {knownPlatforms.map((value) => <option key={value} value={value} />)}
        </datalist>
        <label htmlFor="bind-hand">从哪只手读取</label>
        <select id="bind-hand" value={chosenHand} onChange={(event) => setHandId(event.target.value)}>
          {onlineHands.length === 0 && <option value="">没有在线的手</option>}
          {onlineHands.map((hand) => <option key={hand.handId} value={hand.handId}>{shortRef(hand.handId, 10)}</option>)}
        </select>
        <button
          className="primary-button"
          disabled={!chosenPlatform || !chosenHand || busy !== ''}
          onClick={() => onBind(chosenPlatform, chosenHand)}
        >
          {busy === 'bind' ? '正在核验…' : '绑定当前平台账号'}
        </button>
        {selected && (
          <button
            className="text-button"
            disabled={!chosenHand || busy !== ''}
            onClick={() => onBind(selected.platform, chosenHand, selected.accountRef)}
          >
            {busy === 'rebind' ? '正在重新核验…' : '用这只手重新绑定所选账号'}
          </button>
        )}
        <p>绑定只读取当前登录身份，不会代替你登录。</p>
      </div>
    </aside>
  )
}

function AccountOverview({
  account, busy, onEnable, onStop, onPause, onRun,
}: {
  account: AccountView
  busy: string
  onEnable: () => void
  onStop: () => void
  onPause: () => void
  onRun: () => void
}) {
  const latest = account.latestRound
  const isBusy = busy !== ''
  const shownIdentityState = effectiveIdentityState(account)
  return (
    <section className="shift-sheet" aria-labelledby="shift-title">
      <div className="sheet-heading">
        <div>
          <span className="section-index">今日值班单</span>
          <h2 id="shift-title">{account.platform} 账号 <span className="mono">{shortRef(account.accountRef, 11)}</span></h2>
        </div>
        <div className="identity-mark">
          <span className={account.identityCurrent ? 'verified' : 'attention'} />
          {identityLabel(shownIdentityState)}
        </div>
      </div>

      <PatrolTrack account={account} />

      <div className="shift-controls" aria-label="巡检控制">
        <button className="primary-button" disabled={isBusy || account.enabledToday || !account.handOnline} onClick={onEnable}>
          {busy === 'enable' ? '正在开启…' : account.pausedReason ? '重新开启今天巡检' : '开启今天巡检'}
        </button>
        <button disabled={isBusy || !account.enabledToday || !account.handOnline} onClick={onRun}>
          {busy === 'run' ? '已排入…' : '立即巡一轮'}
        </button>
        <button disabled={isBusy || !account.enabledToday} onClick={onPause}>
          {busy === 'pause' ? '正在暂停…' : '立即暂停'}
        </button>
        <button className="danger-button" disabled={isBusy || !account.enabledToday} onClick={onStop}>
          {busy === 'stop' ? '正在停止…' : '停止今天巡检'}
        </button>
      </div>

      <div className="status-ledger">
        <div>
          <span>手与页面</span>
          <strong>{account.handOnline ? '手在线' : '手离线'} · {account.pageHealth || '页面未知'}</strong>
          <small>传感 {account.sensorHealth || '未知'}{account.dirtyHint ? ' · 有待对账提示' : ''}</small>
        </div>
        <div>
          <span>平台未读</span>
          <strong className={(account.unreadTotal ?? 0) > 0 ? 'ink-amber' : ''}>{account.unreadTotal ?? 0}</strong>
          <small>提示只催促对账，不直接推进业务</small>
        </div>
        <div>
          <span>最近一轮</span>
          <strong>{latest ? roundStatus(latest.status) : '还没有巡检记录'}</strong>
          <small>{latest ? `${latest.stage || '收尾'} · 新消息 ${latest.newMessageCount ?? 0}${latest.errorCode ? ` · ${latest.errorCode}` : ''}` : '开启今天巡检后由脑安排'}</small>
        </div>
      </div>
    </section>
  )
}

function PatrolTrack({ account }: { account: AccountView }) {
  const quietUntil = toDate(account.manualQuietUntil)
  const quietActive = Boolean(quietUntil && quietUntil.getTime() > Date.now())
  const nodes = [
    { label: '开启', value: account.enabledToday || account.pausedReason ? (account.enabledDate || '今天') : '未开启', state: account.enabledToday || account.pausedReason ? 'done' : 'idle' },
    { label: '上轮', value: clock(account.lastPatrolAt, '尚无'), state: account.lastPatrolAt ? 'done' : 'idle' },
    { label: '静默', value: quietActive ? `至 ${clock(account.manualQuietUntil)}` : '无静默', state: quietActive ? 'quiet' : 'idle' },
    { label: '下轮', value: account.enabledToday && !account.pausedReason ? clock(account.nextPatrolAt) : '未安排', state: account.enabledToday && !account.pausedReason ? 'next' : 'idle' },
    { label: '午夜', value: '自动收班', state: 'terminal' },
  ]
  return (
    <div className="patrol-track" aria-label="今日巡检轨">
      <div className="track-title"><span>今日巡检轨</span><small>{account.pausedReason ? `暂停原因：${pauseReasonLabel(account.pausedReason)}` : '节奏由脑统一安排'}</small></div>
      <ol>
        {nodes.map((node) => (
          <li key={node.label} className={node.state}>
            <span className="track-node" />
            <strong>{node.label}</strong>
            <small>{node.value}</small>
          </li>
        ))}
      </ol>
    </div>
  )
}

function ConversationLedger({ rows, loading, error, selectedRef, onSelect }: {
  rows: ConversationView[]
  loading: boolean
  error: string | null
  selectedRef: string
  onSelect: (conversationRef: string) => void
}) {
  return (
    <section className="ledger-sheet conversations" aria-labelledby="conversation-title">
      <div className="ledger-heading">
        <div><span className="section-index">会话账</span><h2 id="conversation-title">最近对话</h2></div>
        <span className="count-label">{rows.length} 条</span>
      </div>
      <div className="conversation-list">
        {rows.map((row) => (
          <button
            key={row.conversationRef}
            className={`conversation-row ${selectedRef === row.conversationRef ? 'is-selected' : ''}`}
            onClick={() => onSelect(row.conversationRef)}
            aria-pressed={selectedRef === row.conversationRef}
          >
            <span className="conversation-main">
              <strong>{row.peerDisplayName || '未命名候选人'}</strong>
              <small>{directionLabel(row.lastMessageDirection)} · {row.lastMessageKind || '消息'} · {approximateTime(row.lastActivityMs)}</small>
              <span>{row.lastMessagePreview || '暂无可显示的消息预览'}</span>
            </span>
            <span className="conversation-meta">
              {row.unreadCount > 0 && <b>{row.unreadCount}</b>}
              <em className={isTracked(row.trackingState) ? 'tracked' : ''}>
                {trackingLabel(row.trackingState)}
              </em>
            </span>
          </button>
        ))}
        {loading && rows.length === 0 && <LedgerEmpty title="正在读取会话账…" />}
        {!loading && rows.length === 0 && !error && <LedgerEmpty title="还没有会话投影" detail="完成一轮巡检后，脑会在这里建立平台无关的会话账。" />}
        {error && <InlineError text={error} />}
      </div>
    </section>
  )
}

function ActivityLedger({
  account, conversation, messages, audits, messagesError, auditsError, tab, busy, onTab, onTrack, onSendChanged,
}: {
  account: AccountView
  conversation: ConversationView | null
  messages: MessageView[]
  audits: AuditView[]
  messagesError: string | null
  auditsError: string | null
  tab: 'messages' | 'audits'
  busy: string
  onTab: (tab: 'messages' | 'audits') => void
  onTrack: () => void
  onSendChanged: () => void
}) {
  return (
    <section className="ledger-sheet activity" aria-labelledby="activity-title">
      <div className="ledger-heading activity-heading">
        <div>
          <span className="section-index">明细账</span>
          <h2 id="activity-title">{conversation?.peerDisplayName || '账号记录'}</h2>
        </div>
        {conversation && !isTracked(conversation.trackingState) && (
          <button className="compact-button" disabled={busy !== ''} onClick={onTrack}>
            {busy === 'track' ? '正在纳入…' : '纳入跟踪'}
          </button>
        )}
      </div>
      <div className="ledger-tabs" role="tablist" aria-label="明细类型">
        <button role="tab" aria-selected={tab === 'messages'} onClick={() => onTab('messages')}>消息</button>
        <button role="tab" aria-selected={tab === 'audits'} onClick={() => onTab('audits')}>审计证词</button>
      </div>

      {tab === 'messages' ? (
        <div className="message-ledger" role="tabpanel">
          {!conversation && <LedgerEmpty title="选择一条会话查看消息" />}
          {conversation && messages.map((message) => (
            <article className={`message-entry direction-${message.direction}`} key={`${message.seq}:${message.firstSeenRoundId}`}>
              <div className="message-seq">#{message.seq}</div>
              <div className="message-copy">
                <div>
                  <strong>{directionLabel(message.direction)}</strong>
                  <span>{message.kind || '消息'} · {approximateTime(message.tsApproxMs)}</span>
                </div>
                <p>{message.text || (message.cardType ? `${message.cardType} · ${message.cardState || '状态未知'}` : '该消息没有文本内容')}</p>
                <small>来源 {message.origin || '平台读取'} · 首见轮次 {shortRef(message.firstSeenRoundId, 7)}</small>
              </div>
            </article>
          ))}
          {conversation && messages.length === 0 && !messagesError && (
            <LedgerEmpty title="该会话还没有消息投影" detail="首次纳入跟踪只建立边界，不把旧消息误计为新消息。" />
          )}
          {messagesError && <InlineError text={messagesError} />}
          {conversation && (
            <MessageComposer
              key={`${account.platform}\u001f${account.accountRef}\u001f${conversation.conversationRef}`}
              account={account}
              conversation={conversation}
              onChanged={onSendChanged}
            />
          )}
        </div>
      ) : (
        <div className="audit-ledger" role="tabpanel">
          {audits.map((audit) => (
            <article className="audit-entry" key={String(audit.id)}>
              <div className="audit-mark" />
              <div>
                <strong>{audit.category || '系统记录'}</strong>
                <span>{dateTime(audit.at)}{audit.conversationRef ? ` · 会话 ${shortRef(audit.conversationRef, 7)}` : ''}</span>
                <p>{detailText(audit.detail)}</p>
                {audit.roundId && <small>轮次 {shortRef(audit.roundId, 7)}</small>}
              </div>
            </article>
          ))}
          {audits.length === 0 && !auditsError && (
            <LedgerEmpty title="还没有审计证词" detail={`${account.enabledToday ? '本轮' : '开启巡检后'}的异常、歧义和基线变化会写在这里。`} />
          )}
          {auditsError && <InlineError text={auditsError} />}
        </div>
      )}
    </section>
  )
}

interface PendingSend {
  intentId: string
  text?: string
}

function newIntentId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  const random = Math.random().toString(36).slice(2)
  return `intent-${Date.now().toString(36)}-${random}`
}

function sendStateLabel(view: SendIntentView | null): string {
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

function sendTerminal(view: SendIntentView | null): boolean {
  if (!view) return false
  const states = [view.status, view.commandStatus]
  return states.some((state) => state && [
    'ok', 'failed', 'suspect', 'expired', 'canceled', 'resolvedOk', 'resolvedFailed',
  ].includes(state))
}

function sendSucceeded(view: SendIntentView | null): boolean {
  return Boolean(view && [view.status, view.commandStatus].some((state) => state === 'ok' || state === 'resolvedOk'))
}

function sendSuspect(view: SendIntentView | null): boolean {
  return Boolean(view && (view.status === 'suspect' || view.commandStatus === 'suspect'))
}

function MessageComposer({ account, conversation, onChanged }: {
  account: AccountView
  conversation: ConversationView
  onChanged: () => void
}) {
  const targetKey = `${account.platform}\u001f${account.accountRef}\u001f${conversation.conversationRef}`
  const [text, setText] = useState('')
  const [pending, setPending] = useState<PendingSend | null>(null)
  const [view, setView] = useState<SendIntentView | null>(null)
  const [error, setError] = useState('')
  const [posting, setPosting] = useState(false)
  const [recovering, setRecovering] = useState(true)
  const [latestIntentId, setLatestIntentId] = useState('')
  const onChangedRef = useRef(onChanged)
  onChangedRef.current = onChanged
  const bytes = new TextEncoder().encode(text.trim()).length
  const adopted = conversation.trackingState === 'adopted' || conversation.trackingState === 'tracked'
  const identityReady = account.identityCurrent
  const quietUntil = toDate(account.manualQuietUntil)
  const quiet = Boolean(quietUntil && quietUntil.getTime() > Date.now())
  const canCreate = adopted && identityReady && account.handOnline && !quiet && bytes > 0 && bytes <= 2048

  useEffect(() => {
    let alive = true
    let timer: number | undefined
    const recover = async () => {
      try {
        // sessionStorage 只是 reload 提示；无论它是否可读，都先以脑账本
        // 判定是否已有发送，不能让浏览器存储成为恢复真相源。
        const latest = await api.latestSendIntent(account.platform, account.accountRef, conversation.conversationRef)
        if (!alive) return
        let acknowledgedIntentId = ''
        let storageWarning = ''
        try {
          acknowledgedIntentId = readSendResume(targetKey).acknowledgedIntentId
        } catch (reason) {
          storageWarning = `本地发送确认凭证不可读：${errorText(reason)}`
        }
        setLatestIntentId(latest?.intentId ?? '')
        if (!latest) {
          setPending(null)
          setView(null)
        } else if (sendTerminal(latest) && acknowledgedIntentId === latest.intentId) {
          setPending(null)
          setView(null)
        } else {
          try {
            rememberSendProposal(targetKey, latest.intentId)
          } catch (reason) {
            storageWarning = `本地发送凭证不可写；当前意图保持锁定：${errorText(reason)}`
          }
          setPending({ intentId: latest.intentId })
          setView(latest)
        }
        setError(storageWarning)
        setRecovering(false)
      } catch (reason) {
        if (!alive) return
        setRecovering(true)
        setError(`无法核对最近一次发送，编辑器已锁定：${errorText(reason)}`)
        timer = window.setTimeout(recover, 2000)
      }
    }
    void recover()
    return () => {
      alive = false
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [targetKey, account.platform, account.accountRef, conversation.conversationRef])

  useEffect(() => {
    if (!pending || (sendTerminal(view) && !sendSuspect(view))) return
    let alive = true
    let timer: number | undefined
    const poll = async () => {
      try {
        const next = await api.sendStatus(pending.intentId)
        if (!alive) return
        setView(next)
        setError('')
        onChangedRef.current()
        if (!sendTerminal(next) || sendSuspect(next)) timer = window.setTimeout(poll, 1200)
      } catch (reason) {
        if (!alive) return
        setError(`状态查询暂时失败：${errorText(reason)}`)
        timer = window.setTimeout(poll, 2000)
      }
    }
    timer = window.setTimeout(poll, 500)
    return () => {
      alive = false
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [pending, view?.status, view?.commandStatus])

  useEffect(() => {
    if (!sendSucceeded(view)) return
    setText('')
    onChangedRef.current()
  }, [view?.status, view?.commandStatus])

  const submit = async () => {
    const normalized = text.trim()
    if (recovering || (pending && pending.text === undefined)) return
    const request = pending ?? { intentId: newIntentId(), text: normalized }
    if (!request.text) return
    if (!pending) {
      try {
        // 必须先稳定保存 ID，再允许 POST 越过进程边界。
        rememberSendProposal(targetKey, request.intentId)
      } catch (reason) {
        setError(`无法保存本次发送凭证，已禁止发送：${errorText(reason)}`)
        return
      }
      setPending(request)
    }
    setPosting(true)
    setError('')
    try {
      const next = await api.sendMessage(
        request.intentId,
        latestIntentId,
        account.platform,
        account.accountRef,
        conversation.conversationRef,
        request.text,
      )
      setView(next)
      setLatestIntentId(next.intentId)
      onChangedRef.current()
    } catch (reason) {
      if (reason instanceof SendIntentConflictError) {
        try { rememberSendProposal(targetKey, reason.current.intentId) } catch { /* 保持 UI 锁定 */ }
        setPending({ intentId: reason.current.intentId })
        setView(reason.current)
        setLatestIntentId(reason.current.intentId)
        setError('发送账本已由另一窗口更新；已恢复当前意图，请先确认其结果。')
      } else if (reason instanceof SendIntentRejectedError) {
        try {
          discardRejectedSendProposal(targetKey, request.intentId)
          setPending(null)
          setView(null)
          setError(`发送前安全检查未通过，脑未创建发送意图：${errorText(reason)}`)
        } catch (storageReason) {
          setError(`脑已明确拒绝发送，但本地凭证无法安全清除，编辑器继续锁定：${errorText(storageReason)}`)
        }
      } else {
        setError(`提交结果不确定；再次操作仍会沿用同一意图，不会新发一条：${errorText(reason)}`)
      }
    } finally {
      setPosting(false)
    }
  }

  const acknowledgeTerminal = () => {
    if (!view || !sendTerminal(view) || sendSuspect(view)) return
    try {
      acknowledgeSendIntent(targetKey, view.intentId)
    } catch (reason) {
      setError(`无法保存人工确认，编辑器继续锁定：${errorText(reason)}`)
      return
    }
    setPending(null)
    setView(null)
    setText('')
    setError('')
  }

  const disabledReason = !adopted
    ? '会话完成首次收编后才能发送'
    : recovering
      ? '正在核对最近一次发送'
    : !identityReady
      ? '账号身份尚未核验'
      : !account.handOnline
        ? '手当前离线'
        : quiet
          ? `检测到真人操作，静默至 ${clock(account.manualQuietUntil)}`
          : bytes > 2048
            ? '消息超过 2048 字节'
            : ''

  return (
    <form className="message-composer" onSubmit={(event) => { event.preventDefault(); void submit() }}>
      <div className="composer-heading">
        <div><strong>发送消息</strong><small>只允许已收编会话；未知结果不会自动再发</small></div>
        <span className={bytes > 2048 ? 'bad' : ''}>{bytes}/2048 字节</span>
      </div>
      <textarea
        value={text}
        rows={3}
        disabled={recovering || Boolean(pending)}
        placeholder={adopted ? '输入自然、明确的一条消息' : '该会话尚未完成收编'}
        onChange={(event) => setText(event.target.value)}
      />
      <div className="composer-actions">
        <button className="primary-button" type="submit" disabled={recovering || posting || Boolean(pending && (!error || pending.text === undefined)) || (!pending && !canCreate)}>
          {recovering ? '正在核对发送账本…' : posting ? '正在安全入账…' : pending?.text !== undefined && error ? '沿用同一意图重试' : pending ? '等待发送结果…' : '发送这一条'}
        </button>
        {sendTerminal(view) && !sendSuspect(view) && (
          <button type="button" onClick={acknowledgeTerminal}>我已确认，开始下一条</button>
        )}
        {sendSuspect(view) && <small>请先在 suspect 队列完成人工裁决</small>}
        {!pending && disabledReason && <small>{disabledReason}</small>}
      </div>
      {view && (
        <div className={`send-state ${sendSucceeded(view) ? 'is-ok' : view.status === 'suspect' || view.commandStatus === 'suspect' ? 'is-bad' : ''}`}>
          <strong>{sendStateLabel(view)}</strong>
          <span>意图 {shortRef(view.intentId, 10)}{view.suspectReason ? ` · ${view.suspectReason}` : ''}</span>
        </div>
      )}
      {error && <div className="composer-error" role="alert">{error}</div>}
    </form>
  )
}

function LedgerEmpty({ title, detail }: { title: string; detail?: string }) {
  return <div className="ledger-empty"><strong>{title}</strong>{detail && <p>{detail}</p>}</div>
}

function InlineError({ text }: { text: string }) {
  return <div className="inline-error" role="alert"><strong>读取失败</strong><span>{text}</span></div>
}

function EmptyWorkbench({ loading, error }: { loading: boolean; error: string | null }) {
  return (
    <section className="empty-workbench">
      <span className="empty-rule" />
      <div>
        <span className="section-index">今日值班单</span>
        <h2>{loading ? '正在读取本地值班账…' : error ? '值班账暂时不可用' : '先绑定一个已登录的平台账号'}</h2>
        <p>{error || '在左侧选择一只在线的手。脑会核验当前身份并建立账号绑定，不会保存登录凭据。'}</p>
      </div>
    </section>
  )
}

function Diagnostics() {
  return (
    <div className="diagnostic-grid">
      <Hands />
      <Commands />
      <Suspects />
      <Ledger />
      <Frames />
    </div>
  )
}

function DiagnosticCard({ title, children, wide }: { title: string; children: React.ReactNode; wide?: boolean }) {
  return <section className={`diagnostic-card${wide ? ' wide' : ''}`}><h3>{title}</h3>{children}</section>
}

function Hands() {
  const health = usePolling(api.handsHealth, 1500, 'diagnostic-hands')
  return (
    <DiagnosticCard title="手（在线状态）">
      <table>
        <thead><tr><th>handId</th><th>状态</th><th>健康</th><th>心跳</th></tr></thead>
        <tbody>
          {(health.data?.hands ?? []).map((hand: HandHealth) => (
            <tr key={hand.handId}>
              <td>{hand.handId}</td>
              <td className={hand.online ? 'ok' : 'dim'}>{hand.online ? '在线' : '离线'}</td>
              <td className={hand.health === 'stalled' ? 'bad' : ''}>{hand.health}</td>
              <td className="dim">{hand.online ? `${Math.round(hand.lastHbAgoMs / 1000)} 秒前` : '—'}</td>
            </tr>
          ))}
          {health.data && health.data.hands.length === 0 && <tr><td colSpan={4} className="dim">暂无手</td></tr>}
        </tbody>
      </table>
    </DiagnosticCard>
  )
}

function Commands() {
  const health = usePolling(api.handsHealth, 2000, 'diagnostic-command-hands')
  const online = (health.data?.hands ?? []).filter((hand) => hand.online)
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
    <DiagnosticCard title="派发命令（测试）">
      <div className="row">
        <label>目标手</label>
        <select value={target} onChange={(event) => setHandId(event.target.value)}>
          {online.length === 0 && <option value="">（无在线手）</option>}
          {online.map((hand) => <option key={hand.handId} value={hand.handId}>{hand.handId}</option>)}
        </select>
      </div>
      <div className="button-row">
        <button onClick={() => send('debug.ping', { echo: 'hi' })}>debug.ping</button>
        <button onClick={() => send('debug.switchWindow', {})}>debug.switchWindow</button>
      </div>
      <div className="row">
        <button onClick={() => send('debug.slowEcho', { ms: 500, outcome })}>debug.slowEcho</button>
        <select value={outcome} onChange={(event) => setOutcome(event.target.value)}>
          <option value="ok">ok</option>
          <option value="failed">failed（possible → suspect）</option>
          <option value="silent">silent（超时 → suspect）</option>
        </select>
      </div>
      <div className="hint mono">{last}</div>
    </DiagnosticCard>
  )
}

function Suspects() {
  const suspects = usePolling(api.suspects, 1500, 'diagnostic-suspects')
  const [message, setMessage] = useState('')
  const verdict = async (msgId: string, value: 'resolvedOk' | 'resolvedFailed') => {
    try {
      const result = await api.verdict(msgId, value)
      setMessage(result.error ? `拒绝：${result.error}` : `已裁决 ${value}`)
      suspects.refresh()
    } catch (reason) { setMessage(errorText(reason)) }
  }
  const rows = suspects.data?.suspects ?? []
  return (
    <DiagnosticCard title={`suspect 队列（转人工）${rows.length ? ` · ${rows.length}` : ''}`}>
      {rows.length === 0 ? <p className="dim">无 suspect</p> : (
        <ul className="diagnostic-list">
          {rows.map((item: Suspect) => (
            <li key={item.msgId}>
              <span>{item.name}</span><span className="dim">{item.reason}</span>
              <span className="dim">
                已验证 {item.verificationAttempts} 轮
                {!item.reviewReady && item.reviewAfter ? ` · 最早 ${dateTime(item.reviewAfter)} 可裁决` : ''}
              </span>
              <button disabled={!item.reviewReady} onClick={() => verdict(item.msgId, 'resolvedOk')}>确认已发生</button>
              <button disabled={!item.reviewReady} onClick={() => verdict(item.msgId, 'resolvedFailed')}>确认未发生</button>
            </li>
          ))}
        </ul>
      )}
      <div className="hint">{message}</div>
    </DiagnosticCard>
  )
}

function Ledger() {
  const ledger = usePolling(api.ledger, 1200, 'diagnostic-ledger')
  const statusClass = (status: string) => status === 'ok' ? 'ok' : status === 'suspect' ? 'bad' : ['failed', 'void', 'rejected', 'expired'].includes(status) ? 'warn' : 'dim'
  return (
    <DiagnosticCard title="命令账本" wide>
      <table>
        <thead><tr><th>msgId</th><th>原语</th><th>类</th><th>状态</th><th>试</th></tr></thead>
        <tbody>
          {(ledger.data?.ledger ?? []).slice(0, 15).map((row: LedgerRow) => (
            <tr key={row.msgId}>
              <td className="mono dim">{row.msgId.slice(0, 14)}…</td><td>{row.name}</td><td className="dim">{row.class}</td>
              <td className={statusClass(row.status)}>{row.status}{row.errorCode ? ` (${row.errorCode})` : ''}</td><td className="dim">{row.attempt}</td>
            </tr>
          ))}
        </tbody>
      </table>
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
    <DiagnosticCard title="协议帧观测台（实时）" wide>
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
