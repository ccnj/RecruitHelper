// 账号与巡检：左边一列账号票，右边当前账号的值班表。人工指定候选人与
// 第一条招呼也在这一页——它们是"对这个账号做点什么"的延伸，不是会话账。
import { useEffect, useReducer, useState } from 'react'
import {
  api, AccountView, CANDIDATE_READ_ERROR, CANDIDATE_SELECT_ERROR, HandHealth,
  SendIntentConflictError, SendIntentRejectedError, SendIntentView,
} from '../../api'
import {
  candidateWorkflowReducer, canConfirmCandidate, initialCandidateWorkflow,
} from '../../candidate-workflow'
import { sendStateLabel, sendSucceeded, sendSuspect, sendTerminal } from '../../send-intent-state'
import {
  accountIdentity, clock, effectiveIdentityState, errorText, identityLabel,
  pauseReasonLabel, roundStatus, shortRef, toDate,
} from '../format'
import { EmptyWorkbench, InlineError } from '../shared/Primitives'
import { newIntentId, type PendingSend } from '../shared/send'

export function AccountPage({
  accounts, accountsError, accountsLoading, hands, selectedKey, selectedAccount, accountKey, busy,
  onSelect, onBind, onEnable, onStop, onPause, onRun,
}: {
  accounts: AccountView[]
  accountsError: string | null
  accountsLoading: boolean
  hands: HandHealth[]
  selectedKey: string
  selectedAccount: AccountView | null
  accountKey: string
  busy: string
  onSelect: (accountKey: string) => void
  onBind: (platform: string, handId: string, accountRef?: string) => void
  onEnable: () => void
  onStop: () => void
  onPause: () => void
  onRun: () => void
}) {
  return (
    <div className="desk-layout">
      <AccountRail
        accounts={accounts}
        accountsError={accountsError}
        hands={hands}
        selectedKey={selectedKey}
        busy={busy}
        onSelect={onSelect}
        onBind={onBind}
      />
      <main className="workbench">
        {selectedAccount ? (
          <AccountOverview
            key={accountKey}
            account={selectedAccount}
            busy={busy}
            onEnable={onEnable}
            onStop={onStop}
            onPause={onPause}
            onRun={onRun}
          />
        ) : (
          <EmptyWorkbench loading={accountsLoading} error={accountsError} />
        )}
      </main>
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
        <strong>已绑定账号 {accounts.length}</strong>
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
  const quietUntil = toDate(account.manualQuietUntil)
  const quietActive = Boolean(quietUntil && quietUntil.getTime() > Date.now())
  return (
    <section className="shift-sheet" aria-labelledby="shift-title">
      <div className="sheet-heading">
        <h2 id="shift-title">{account.platform} <span className="mono">{shortRef(account.accountRef, 11)}</span></h2>
        <div className="identity-mark">
          <span className={account.identityCurrent ? 'verified' : 'attention'} />
          {identityLabel(shownIdentityState)}
        </div>
      </div>

      <div className="dc-account-facts">
        <span>今日开启 <strong>{account.enabledToday ? account.enabledDate || '已开启' : '未开启'}</strong></span>
        <span>上轮 <strong>{clock(account.lastPatrolAt, '尚无')}</strong></span>
        <span>下轮 <strong>{account.enabledToday && !account.pausedReason ? clock(account.nextPatrolAt) : '未安排'}</strong></span>
        <span>静默 <strong className={quietActive ? 'ink-amber' : ''}>{quietActive ? `至 ${clock(account.manualQuietUntil)}` : '无'}</strong></span>
        {account.pausedReason && <span>暂停 <strong className="ink-amber">{pauseReasonLabel(account.pausedReason)}</strong></span>}
      </div>

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

      <CandidateIntake account={account} />

      <div className="status-ledger">
        <div>
          <span>手与页面</span>
          <strong>{account.handOnline ? '手在线' : '手离线'} · {account.pageHealth || '页面未知'}</strong>
          <small>{account.dirtyHint ? '有待对账提示' : '无待对账提示'}</small>
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
        <h3 id="candidate-intake-title">人工指定页面中的候选人</h3>
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
