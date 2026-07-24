import { useCallback, useEffect, useReducer, useRef, useState } from 'react'
import {
  api, ADMIN_BASE, CANDIDATE_READ_ERROR, CANDIDATE_SELECT_ERROR,
  SendIntentConflictError, SendIntentRejectedError,
  AccountView, AuditView, ConversationView, FrameEvent, HandHealth, Health,
  JobConfigSourceView, LedgerRow, M5AIContextView, M5ProviderConfigView, MessageView, MutationResult,
  SendIntentView, Suspect, TimeValue,
} from './api'
import {
  candidateWorkflowReducer, canConfirmCandidate, initialCandidateWorkflow,
} from './candidate-workflow'
import { sendStateLabel, sendSucceeded, sendSuspect, sendTerminal } from './send-intent-state'
import { acknowledgeSendIntent, readSendResume } from './send-resume'

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
  return state === 'pending' || state === 'adopted'
}

function trackingLabel(state: string): string {
  if (state === 'pending') return '基线待建立'
  if (state === 'adopted') return '跟踪中'
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
                key={accountKey}
                account={selectedAccount}
                busy={busy}
                onEnable={() => target && runMutation('enable', '已开启今天的自动巡检', () => api.enableAccount(target))}
                onStop={() => target && runMutation('stop', '今天的自动巡检已停止', () => api.stopAccount(target))}
                onPause={() => target && runMutation('pause', '已立即暂停，等待人工恢复', () => api.pauseAccount(target))}
                onRun={() => target && runMutation('run', '已请求立即巡检一轮', () => api.runAccount(target))}
                onProcessCurrent={() => target && runMutation(
                  'process-current',
                  '已处理浏览器当前打开会话一次',
                  () => api.processCurrentConversationOnce(target),
                )}
              />

              <M5AIConfiguration />

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
                  onSelectM5Trial={() => selectedConversation && runMutation(
                    'm5-trial',
                    '该档案已被明确选为 M5 简历补采试运行；巡检会沿正式路径执行一次',
                    () => api.selectM5Trial(selectedAccount.platform, selectedAccount.accountRef, selectedConversation.conversationRef),
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
  account, busy, onEnable, onStop, onPause, onRun, onProcessCurrent,
}: {
  account: AccountView
  busy: string
  onEnable: () => void
  onStop: () => void
  onPause: () => void
  onRun: () => void
  onProcessCurrent: () => void
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
        <button
          disabled={isBusy || !account.enabledToday || !account.handOnline}
          onClick={onProcessCurrent}
        >
          {busy === 'process-current' ? '正在处理当前会话…' : '处理当前会话'}
        </button>
        <button disabled={isBusy || !account.enabledToday} onClick={onPause}>
          {busy === 'pause' ? '正在暂停…' : '立即暂停'}
        </button>
        <button className="danger-button" disabled={isBusy || !account.enabledToday} onClick={onStop}>
          {busy === 'stop' ? '正在停止…' : '停止今天巡检'}
        </button>
      </div>

      <CandidateIntake account={account} />

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

function candidateContactLabel(state: string): string {
  if (state === 'unestablished') return '尚未建联，可以确认建档'
  if (state === 'established') return '页面显示已经建联，不能作为新建联对象确认'
  return '关系状态无法确认，不能继续建档'
}

const M5_PROVIDER_BUDGET = {
  provider: 'deepseek' as const,
  model: 'deepseek-v4-pro' as const,
  request_timeout_ms: 30000 as const,
  max_input_tokens: 16000 as const,
  max_intent_output_tokens: 64 as const,
  max_reply_output_tokens: 512 as const,
}

function M5AIConfiguration() {
  const [contexts, setContexts] = useState<M5AIContextView[]>([])
  const [contextsLoading, setContextsLoading] = useState(true)
  const [contextsError, setContextsError] = useState('')
  const [bundleText, setBundleText] = useState('')
  const [selectedRevision, setSelectedRevision] = useState('')
  const [contextBusy, setContextBusy] = useState<'activate' | 'sync' | 'import' | 'bind' | ''>('')
  const [contextNotice, setContextNotice] = useState<{ kind: 'ok' | 'bad'; text: string } | null>(null)
  const [jobSource, setJobSource] = useState<JobConfigSourceView | null>(null)
  const [jobSourceLoading, setJobSourceLoading] = useState(true)
  const [jobSourceError, setJobSourceError] = useState('')
  const [jobSourceBaseURL, setJobSourceBaseURL] = useState('')
  const [jobSourceInviteCode, setJobSourceInviteCode] = useState('')
  const [providerConfig, setProviderConfig] = useState<M5ProviderConfigView | null>(null)
  const [providerLoading, setProviderLoading] = useState(true)
  const [providerError, setProviderError] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [apiKey, setAPIKey] = useState('')
  const [providerSaving, setProviderSaving] = useState(false)
  const [providerNotice, setProviderNotice] = useState<{ kind: 'ok' | 'bad'; text: string } | null>(null)

  const loadContexts = useCallback(async () => {
    setContextsLoading(true)
    try {
      const result = await api.m5Contexts()
      setContexts(Array.isArray(result.contexts) ? result.contexts : [])
      setContextsError('')
    } catch (reason) {
      setContextsError(errorText(reason))
    } finally {
      setContextsLoading(false)
    }
  }, [])

  const loadProvider = useCallback(async () => {
    setProviderLoading(true)
    try {
      const result = await api.m5ProviderConfig()
      setProviderConfig(result.config)
      setProviderError('')
    } catch (reason) {
      setProviderError(errorText(reason))
    } finally {
      setProviderLoading(false)
    }
  }, [])

  const loadJobSource = useCallback(async () => {
    setJobSourceLoading(true)
    try {
      const result = await api.jobConfigSource()
      setJobSource(result.config)
      setJobSourceError('')
    } catch (reason) {
      setJobSourceError(errorText(reason))
    } finally {
      setJobSourceLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadContexts()
    void loadProvider()
    void loadJobSource()
  }, [loadContexts, loadJobSource, loadProvider])

  const activateJobSource = async () => {
    setContextBusy('activate')
    setContextNotice(null)
    try {
      const result = await api.activateJobConfigSource({
        base_url: jobSourceBaseURL.trim(), invite_code: jobSourceInviteCode.trim(),
      })
      const synced = Array.isArray(result.contexts) ? result.contexts : []
      if (synced[0]) setSelectedRevision(synced[0].revisionHash)
      setJobSourceBaseURL('')
      setJobSourceInviteCode('')
      setJobSourceError('')
      setContextNotice(result.synced
        ? { kind: 'ok', text: '旧后台已正式激活，当前职位已同步为本地不可变版本。' }
        : { kind: 'bad', text: result.syncError || '激活已成功，但当前职位同步失败；可直接重试同步。' })
      await loadJobSource()
      await loadContexts()
    } catch (reason) {
      setContextNotice({ kind: 'bad', text: errorText(reason) })
    } finally {
      setContextBusy('')
    }
  }

  const syncCurrentJob = async () => {
    setContextBusy('sync')
    setContextNotice(null)
    try {
      const result = await api.syncCurrentJobConfig()
      const synced = Array.isArray(result.contexts) ? result.contexts : []
      if (synced[0]) setSelectedRevision(synced[0].revisionHash)
      setContextNotice({ kind: 'ok', text: '旧后台当前职位已同步为本地不可变版本。' })
      await loadContexts()
    } catch (reason) {
      setContextNotice({ kind: 'bad', text: errorText(reason) })
    } finally {
      setContextBusy('')
    }
  }

  const importBundle = async () => {
    setContextNotice(null)
    let parsed: unknown
    try {
      parsed = JSON.parse(bundleText)
    } catch {
      setContextNotice({ kind: 'bad', text: 'JSON 无法解析，请检查是否粘贴了完整响应。' })
      return
    }
    if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
      setContextNotice({ kind: 'bad', text: '职位资料必须是一个完整的 JSON 对象。' })
      return
    }
    const bundle = parsed as Record<string, unknown>
    setContextBusy('import')
    try {
      await api.importM5Contexts(bundle)
      setBundleText('')
      setContextNotice({ kind: 'ok', text: '职位资料已导入；请在右侧明确选择要绑定的版本。' })
      await loadContexts()
    } catch (reason) {
      setContextNotice({ kind: 'bad', text: errorText(reason) })
    } finally {
      setContextBusy('')
    }
  }

  const bindContext = async () => {
    const selected = contexts.find((context) => context.revisionHash === selectedRevision)
    if (!selected) return
    setContextBusy('bind')
    setContextNotice(null)
    try {
      await api.bindM5Context(selected.contextId, selected.revisionHash)
      setContextNotice({ kind: 'ok', text: `“${selected.displayName}”已绑定到当前试运行档案。` })
    } catch (reason) {
      setContextNotice({ kind: 'bad', text: errorText(reason) })
    } finally {
      setContextBusy('')
    }
  }

  const saveProvider = async () => {
    setProviderSaving(true)
    setProviderNotice(null)
    try {
      const result = await api.saveM5ProviderConfig({
        ...M5_PROVIDER_BUDGET,
        base_url: baseURL.trim(),
        api_key: apiKey.trim(),
      })
      setProviderConfig(result.config)
      setBaseURL('')
      setAPIKey('')
      setProviderNotice({ kind: 'ok', text: '模型连接已保存在本机；页面不会回显地址或密钥。本次不会热生效，请等待 M5 验收提示后再重启客户端。' })
    } catch (reason) {
      setProviderNotice({ kind: 'bad', text: errorText(reason) })
    } finally {
      setProviderSaving(false)
    }
  }

  const providerReady = providerConfig?.baseUrlConfigured === true && providerConfig.keyConfigured === true
  const sourceReady = jobSource?.configured === true
    && jobSource.machineIdentityReady === true && jobSource.machineMatch === true
  const activationInputsReady = (jobSource?.baseUrlConfigured || jobSourceBaseURL.trim() !== '')
    && jobSourceInviteCode.trim() !== ''

  return (
    <section className="m5-ai-panel" aria-labelledby="m5-ai-title">
      <div className="m5-ai-heading">
        <div>
          <span className="section-index">M5 建议层</span>
          <h2 id="m5-ai-title">M5 AI 配置</h2>
        </div>
        <div className={`m5-ai-readiness ${providerReady ? 'is-ready' : ''}`}>
          <span />
          {providerLoading ? '正在核对模型配置' : providerReady ? '模型连接材料已齐' : '模型连接尚未配齐'}
        </div>
      </div>

      <div className="m5-ai-wire" aria-label="AI 配置步骤">
        <section className="m5-ai-step" aria-labelledby="m5-import-title">
          <header><span>01</span><div><strong id="m5-import-title">同步职位资料</strong><small>旧后台当前职位 · 不继承旧模型密钥</small></div></header>
          <div className="m5-provider-state m5-source-state">
            <span>后台地址 <strong>{jobSource?.baseUrlConfigured ? '已配置' : '未配置'}</strong></span>
            <span>本机身份 <strong>{jobSource?.machineIdentityReady ? '可用' : '不可用'}</strong></span>
            <span>正式授权 <strong>{sourceReady ? jobSource?.customerName || '已激活' : '未激活或不匹配'}</strong></span>
          </div>
          <label htmlFor="job-source-base-url">旧后台地址</label>
          <input
            id="job-source-base-url"
            type="url"
            value={jobSourceBaseURL}
            onChange={(event) => setJobSourceBaseURL(event.target.value)}
            autoComplete="off"
            placeholder={jobSource?.baseUrlConfigured ? '留空则保留现有地址' : 'http://…'}
          />
          <label htmlFor="job-source-invite-code">后台激活码</label>
          <input
            id="job-source-invite-code"
            type="password"
            value={jobSourceInviteCode}
            onChange={(event) => setJobSourceInviteCode(event.target.value)}
            autoComplete="new-password"
            placeholder="输入一次性激活码"
          />
          {jobSourceError && <p className="m5-ai-message bad" role="alert">{jobSourceError}</p>}
          <button
            type="button"
            disabled={contextBusy !== '' || jobSourceLoading || !activationInputsReady}
            onClick={() => void activateJobSource()}
          >
            {contextBusy === 'activate' ? '正在激活并同步…' : sourceReady ? '重新激活并同步' : '激活并同步当前职位'}
          </button>
          {sourceReady && (
            <button
              type="button"
              disabled={contextBusy !== '' || jobSourceLoading}
              onClick={() => void syncCurrentJob()}
            >
              {contextBusy === 'sync' ? '正在同步…' : '仅重新同步当前职位'}
            </button>
          )}
          <details className="m5-manual-import">
            <summary>开发期手工导入 JSON</summary>
            <textarea
              value={bundleText}
              onChange={(event) => setBundleText(event.target.value)}
              rows={5}
              spellCheck={false}
              autoComplete="off"
              placeholder="粘贴完整 job-config JSON"
              aria-label="旧 job-config JSON"
            />
            <button
              type="button"
              disabled={contextBusy !== '' || bundleText.trim() === ''}
              onClick={() => void importBundle()}
            >
              {contextBusy === 'import' ? '正在导入…' : '手工导入'}
            </button>
          </details>
        </section>

        <section className="m5-ai-step" aria-labelledby="m5-bind-title">
          <header><span>02</span><div><strong id="m5-bind-title">明确绑定版本</strong><small>只绑定到当前 active 试运行档案</small></div></header>
          <div className="m5-context-list" role="radiogroup" aria-label="可用职位资料版本">
            {contextsLoading && <p className="m5-ai-empty">正在读取已导入资料…</p>}
            {!contextsLoading && contexts.length === 0 && !contextsError && (
              <p className="m5-ai-empty">还没有可用资料。先完成左侧导入。</p>
            )}
            {contexts.map((context) => (
              <label key={context.revisionHash} className={`m5-context-option ${selectedRevision === context.revisionHash ? 'is-selected' : ''}`}>
                <input
                  type="radio"
                  name="m5-context"
                  value={context.revisionHash}
                  checked={selectedRevision === context.revisionHash}
                  onChange={() => setSelectedRevision(context.revisionHash)}
                />
                <span>
                  <strong>{context.displayName}</strong>
                  <small>{context.environment || '环境未标注'} · {context.documentCount} 份文档</small>
                  <code>context {context.contextId}</code>
                  <code>revision {context.revisionHash}</code>
                </span>
              </label>
            ))}
          </div>
          {contextsError && <p className="m5-ai-message bad" role="alert">{contextsError}</p>}
          <button
            type="button"
            disabled={contextBusy !== '' || selectedRevision === ''}
            onClick={() => void bindContext()}
          >
            {contextBusy === 'bind' ? '正在绑定…' : '绑定所选版本'}
          </button>
        </section>

        <section className="m5-ai-step" aria-labelledby="m5-provider-title">
          <header><span>03</span><div><strong id="m5-provider-title">配置模型连接</strong><small>DeepSeek V4 Pro · P 档</small></div></header>
          <div className="m5-provider-state">
            <span>服务地址 <strong>{providerConfig?.baseUrlConfigured ? '已配置' : '未配置'}</strong></span>
            <span>API Key <strong>{providerConfig?.keyConfigured ? '已配置' : '未配置'}</strong></span>
          </div>
          <label htmlFor="m5-base-url">新的服务地址</label>
          <input
            id="m5-base-url"
            type="url"
            value={baseURL}
            onChange={(event) => setBaseURL(event.target.value)}
            autoComplete="off"
            placeholder={providerConfig?.baseUrlConfigured ? '留空则保留现有地址' : 'https://…'}
          />
          <label htmlFor="m5-api-key">新的 API Key</label>
          <input
            id="m5-api-key"
            type="password"
            value={apiKey}
            onChange={(event) => setAPIKey(event.target.value)}
            autoComplete="new-password"
            placeholder={providerConfig?.keyConfigured ? '留空则保留现有密钥' : '输入密钥'}
          />
          <div className="m5-budget-line">
            <span>30 秒超时</span><span>输入 16K</span><span>意向 64</span><span>回复 512</span>
          </div>
          {providerError && <p className="m5-ai-message bad" role="alert">{providerError}</p>}
          <button
            type="button"
            disabled={providerSaving || (!providerConfig?.baseUrlConfigured && baseURL.trim() === '') || (!providerConfig?.keyConfigured && apiKey.trim() === '')}
            onClick={() => void saveProvider()}
          >
            {providerSaving ? '正在保存…' : '保存模型连接'}
          </button>
        </section>
      </div>

      {(contextNotice || providerNotice) && (
        <div className="m5-ai-notices" aria-live="polite">
          {contextNotice && <p className={`m5-ai-message ${contextNotice.kind}`}>{contextNotice.text}</p>}
          {providerNotice && <p className={`m5-ai-message ${providerNotice.kind}`}>{providerNotice.text}</p>}
        </div>
      )}
    </section>
  )
}

function CandidateIntake({ account }: { account: AccountView }) {
  const [state, dispatch] = useReducer(candidateWorkflowReducer, initialCandidateWorkflow)
  const running = state.phase === 'reading' || state.phase === 'selecting'
  const readable = account.handOnline && account.identityCurrent && !running

  // AccountOverview 以账号 identity 为 key 挂载；这里再显式重置一次，避免将来
  // 组件层级调整时旧 selectionRef 跨账号存活。状态从不写入任何浏览器存储。
  useEffect(() => {
    dispatch({ type: 'accountChanged' })
  }, [account.platform, account.accountRef])

  const read = async () => {
    dispatch({ type: 'readStarted' })
    try {
      const preview = await api.readCurrentCandidate({
        platform: account.platform,
        accountRef: account.accountRef,
      })
      dispatch({ type: 'readSucceeded', preview })
    } catch {
      dispatch({ type: 'readFailed', error: CANDIDATE_READ_ERROR })
    }
  }

  const confirm = async () => {
    if (state.phase !== 'preview' || state.preview.contactState !== 'unestablished') return
    const selectionRef = state.preview.selectionRef
    dispatch({ type: 'selectStarted' })
    try {
      const profile = await api.selectCurrentCandidate(selectionRef)
      dispatch({ type: 'selectSucceeded', profile })
    } catch {
      dispatch({ type: 'selectFailed', error: CANDIDATE_SELECT_ERROR })
    }
  }

  return (
    <section className="candidate-intake" aria-labelledby="candidate-intake-title">
      <div className="candidate-intake-heading">
        <div>
          <span className="section-index">主动建联</span>
          <h3 id="candidate-intake-title">人工指定页面中的候选人</h3>
        </div>
        <button
          type="button"
          disabled={!readable}
          onClick={() => void read()}
        >
          {state.phase === 'reading' ? '正在读取页面…' : state.preview ? '重新读取当前候选人' : '读取当前候选人'}
        </button>
      </div>

      {!account.handOnline && <p className="candidate-intake-hint">手当前离线，接通后才能读取页面。</p>}
      {account.handOnline && !account.identityCurrent && <p className="candidate-intake-hint">账号身份尚未核验，暂不读取候选人。</p>}
      {state.phase === 'idle' && account.handOnline && account.identityCurrent && (
        <p className="candidate-intake-hint">先在招聘页面打开一位候选人的详情；读取不会发消息，也不会直接建档。</p>
      )}

      {(state.phase === 'preview' || state.phase === 'selecting') && (
        <div className="candidate-preview">
          <div>
            <span>候选人</span>
            <strong>{state.preview.displayName || '姓名未提供'}</strong>
          </div>
          <div>
            <span>当前职位</span>
            <strong>{state.preview.positionTitle || '职位名称未提供'}</strong>
          </div>
          <div className={`candidate-contact ${state.preview.contactState === 'unestablished' ? 'is-ready' : 'is-blocked'}`}>
            <span>建联状态</span>
            <strong>{candidateContactLabel(state.preview.contactState)}</strong>
          </div>
          <div className="candidate-confirm">
            <button
              className="primary-button"
              type="button"
              disabled={!canConfirmCandidate(state) || running}
              onClick={() => void confirm()}
            >
              {state.phase === 'selecting' ? '正在确认建档…' : '确认是这位候选人并建档'}
            </button>
            <small>只有明确显示尚未建联时才能确认；页面变化后请重新读取。</small>
          </div>
        </div>
      )}

      {state.phase === 'selected' && (
        <>
          <div className="candidate-intake-result" role="status">
            <strong>{state.profile.created ? '候选人档案已建立' : '已找到同一候选人档案'}</strong>
            <span>当前状态：{state.profile.status || 'selected'} · 档案 {shortRef(state.profile.profileId, 10)}</span>
          </div>
          {state.profile.status === 'selected' && (
            <GreetingComposer
              key={state.profile.profileId}
              account={account}
              profileId={state.profile.profileId}
            />
          )}
        </>
      )}
      {state.error && <div className="candidate-intake-error" role="alert">{state.error}</div>}
    </section>
  )
}

function GreetingComposer({ account, profileId }: { account: AccountView; profileId: string }) {
  const [text, setText] = useState('你好')
  const [pending, setPending] = useState<PendingSend | null>(null)
  const [view, setView] = useState<SendIntentView | null>(null)
  const [error, setError] = useState('')
  const [posting, setPosting] = useState(false)
  const [recovering, setRecovering] = useState(true)
  const [latestIntentId, setLatestIntentId] = useState('')
  const bytes = new TextEncoder().encode(text.trim()).length
  const quietUntil = toDate(account.manualQuietUntil)
  const quiet = Boolean(quietUntil && quietUntil.getTime() > Date.now())
  const canCreate = account.identityCurrent && account.handOnline && !quiet && bytes > 0 && bytes <= 2048

  useEffect(() => {
    let alive = true
    let timer: number | undefined
    const recover = async () => {
      try {
        const latest = await api.latestGreetingIntent(profileId)
        if (!alive) return
        setLatestIntentId(latest?.intentId ?? '')
        setPending(latest ? { intentId: latest.intentId } : null)
        setView(latest)
        setError('')
        setRecovering(false)
      } catch (reason) {
        if (!alive) return
        setRecovering(true)
        setError(`无法核对最近一次招呼，编辑器已锁定：${errorText(reason)}`)
        timer = window.setTimeout(recover, 2000)
      }
    }
    void recover()
    return () => {
      alive = false
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [profileId])

  useEffect(() => {
    if (!pending || (sendTerminal(view) && !sendSuspect(view))) return
    let alive = true
    let timer: number | undefined
    const poll = async () => {
      try {
        const next = await api.greetingStatus(pending.intentId)
        if (!alive) return
        setView(next)
        setLatestIntentId(next.intentId)
        setError('')
        if (!sendTerminal(next) || sendSuspect(next)) timer = window.setTimeout(poll, 1200)
      } catch (reason) {
        if (!alive) return
        setError(`招呼状态查询暂时失败：${errorText(reason)}`)
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
    if (sendSucceeded(view)) setText('')
  }, [view?.status, view?.commandStatus])

  const submit = async () => {
    const normalized = text.trim()
    if (recovering || posting || (pending && pending.text === undefined)) return
    const request = pending ?? { intentId: newIntentId(), text: normalized }
    if (!request.text) return
    if (!pending) setPending(request)
    setPosting(true)
    setError('')
    let postStarted = false
    try {
      // 每次发送前都先收编 profile 当前 head。响应丢失时只恢复同一
      // intentId；另一窗口抢先更新时不再创建本窗口的新意图。
      const current = await api.latestGreetingIntent(profileId)
      if (current?.intentId === request.intentId) {
        setPending(request)
        setView(current)
        setLatestIntentId(current.intentId)
        return
      }
      if (current && current.intentId !== latestIntentId) {
        setPending({ intentId: current.intentId })
        setView(current)
        setLatestIntentId(current.intentId)
        setError('招呼账本已由另一窗口更新；已恢复当前意图，请先确认其结果。')
        return
      }
      if (!current && latestIntentId) {
        setError('招呼账本当前无法确认，本次未发送；请沿用当前意图重试核对。')
        return
      }
      postStarted = true
      const next = await api.sendGreeting(request.intentId, latestIntentId, profileId, request.text)
      setView(next)
      setLatestIntentId(next.intentId)
    } catch (reason) {
      if (reason instanceof SendIntentConflictError) {
        setPending({ intentId: reason.current.intentId })
        setView(reason.current)
        setLatestIntentId(reason.current.intentId)
        setError('招呼账本已由另一窗口更新；已恢复当前意图，请先确认其结果。')
      } else if (reason instanceof SendIntentRejectedError) {
        setPending((current) => current?.intentId === request.intentId ? null : current)
        setView((current) => current?.intentId === request.intentId ? null : current)
        setError(`招呼前安全检查未通过，脑未创建招呼意图：${errorText(reason)}`)
      } else if (postStarted) {
        setError(`提交结果不确定；再次操作仍会沿用同一意图，不会再发一条：${errorText(reason)}`)
      } else {
        setError(`无法核对当前招呼账本，本次未发送：${errorText(reason)}`)
      }
    } finally {
      setPosting(false)
    }
  }

  const acknowledgeTerminal = () => {
    if (!view || !sendTerminal(view) || sendSucceeded(view) || sendSuspect(view)) return
    setPending(null)
    setView(null)
    setError('')
    if (!text.trim()) setText('你好')
  }

  const disabledReason = recovering
    ? '正在核对最近一次招呼'
    : !account.identityCurrent
      ? '账号身份尚未核验'
      : !account.handOnline
        ? '手当前离线'
        : quiet
          ? `检测到真人操作，静默至 ${clock(account.manualQuietUntil)}`
          : bytes > 2048
            ? '招呼超过 2048 字节'
            : ''

  return (
    <form className="greeting-composer" onSubmit={(event) => { event.preventDefault(); void submit() }}>
      <div className="composer-heading">
        <div><strong>发送第一条招呼</strong><small>正文可以修改；只有点击下方确认按钮才会发送</small></div>
        <span className={bytes > 2048 ? 'bad' : ''}>{bytes}/2048 字节</span>
      </div>
      <textarea
        value={text}
        rows={2}
        disabled={recovering || Boolean(pending)}
        aria-label="招呼正文"
        onChange={(event) => setText(event.target.value)}
      />
      <div className="composer-actions">
        <button
          className="primary-button"
          type="submit"
          disabled={recovering || posting || Boolean(pending && (!error || pending.text === undefined)) || (!pending && !canCreate)}
        >
          {recovering ? '正在核对招呼账本…' : posting ? '正在安全入账…' : pending?.text !== undefined && error ? '沿用同一意图重试' : pending ? '等待招呼结果…' : '确认并发送这条招呼'}
        </button>
        {sendTerminal(view) && !sendSucceeded(view) && !sendSuspect(view) && (
          <button type="button" onClick={acknowledgeTerminal}>我已确认，重新编辑</button>
        )}
        {sendSuspect(view) && <small>请先在 suspect 队列完成人工裁决</small>}
        {!pending && disabledReason && <small>{disabledReason}</small>}
      </div>
      {view && (
        <div className={`send-state ${sendSucceeded(view) ? 'is-ok' : sendSuspect(view) ? 'is-bad' : ''}`}>
          <strong>{sendStateLabel(view)}</strong>
          <span>招呼意图 {shortRef(view.intentId, 10)}{view.suspectReason ? ` · ${view.suspectReason}` : ''}</span>
        </div>
      )}
      {error && <div className="composer-error" role="alert">{error}</div>}
    </form>
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
  account, conversation, messages, audits, messagesError, auditsError, tab, busy, onTab, onTrack, onSelectM5Trial, onSendChanged,
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
  onSelectM5Trial: () => void
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
        {conversation?.trackingState === 'adopted' && (
          <button className="compact-button" disabled={busy !== ''} onClick={onSelectM5Trial}>
            {busy === 'm5-trial' ? '正在记入…' : '选作简历补采试运行'}
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
  const adopted = conversation.trackingState === 'adopted'
  const identityReady = account.identityCurrent
  const quietUntil = toDate(account.manualQuietUntil)
  const quiet = Boolean(quietUntil && quietUntil.getTime() > Date.now())
  const canCreate = adopted && identityReady && account.handOnline && !quiet && bytes > 0 && bytes <= 2048

  useEffect(() => {
    let alive = true
    let timer: number | undefined
    const recover = async () => {
      try {
        // 无条件先以脑账本判定是否已有发送；sessionStorage 只保存
        // M3 有人值守 UI 的终局确认，不参与意图恢复。
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
    if (!pending) setPending(request)
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
        setPending({ intentId: reason.current.intentId })
        setView(reason.current)
        setLatestIntentId(reason.current.intentId)
        setError('发送账本已由另一窗口更新；已恢复当前意图，请先确认其结果。')
      } else if (reason instanceof SendIntentRejectedError) {
        setPending((current) => current?.intentId === request.intentId ? null : current)
        setView((current) => current?.intentId === request.intentId ? null : current)
        setError(`发送前安全检查未通过，脑未创建发送意图：${errorText(reason)}`)
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
  const [reloading, setReloading] = useState('')
  const [message, setMessage] = useState('')
  const reload = async (handId: string) => {
    setReloading(handId)
    setMessage('已请求手自重载，等待新版本重新报到…')
    try {
      const result = await api.reloadHand(handId)
      setMessage(`新版本已就绪 · boot ${shortRef(result.bootId, 10)} · 扩展 ${result.extensionVersion || '版本未知'}`)
      health.refresh()
    } catch (reason) {
      setMessage(errorText(reason))
    } finally {
      setReloading('')
    }
  }
  return (
    <DiagnosticCard title="手（在线状态）">
      <table>
        <thead><tr><th>handId</th><th>状态</th><th>构建</th><th>心跳</th><th>维护</th></tr></thead>
        <tbody>
          {(health.data?.hands ?? []).map((hand: HandHealth) => (
            <tr key={hand.handId}>
              <td>{hand.handId}</td>
              <td className={hand.online ? 'ok' : 'dim'}>{hand.online ? '在线' : '离线'}</td>
              <td className={!hand.contractMatch ? 'warn' : hand.health === 'stalled' ? 'bad' : ''}>
                {hand.contractMatch ? hand.extensionVersion || '已匹配' : '契约不匹配'}
              </td>
              <td className="dim">{hand.online ? `${Math.round(hand.lastHbAgoMs / 1000)} 秒前` : '—'}</td>
              <td>
                <button
                  disabled={!hand.online || reloading !== '' || !hand.caps.includes('debug.reload@1')}
                  onClick={() => void reload(hand.handId)}
                >
                  {reloading === hand.handId ? '重载中…' : '重载并确认'}
                </button>
              </td>
            </tr>
          ))}
          {health.data && health.data.hands.length === 0 && <tr><td colSpan={5} className="dim">暂无手</td></tr>}
        </tbody>
      </table>
      <div className="hint">{message || '硬切换前先暂停派发并等待命令收束；首次启用本能力仍需人工重载一次。'}</div>
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
