// 会话与消息：左边会话账，右边所选会话的消息 / 审计证词与发送口。
import { useEffect, useRef, useState } from 'react'
import {
  api, AccountView, AuditView, ConversationView, MessageView,
  SendIntentConflictError, SendIntentRejectedError, SendIntentView,
} from '../../api'
import { sendStateLabel, sendSucceeded, sendSuspect, sendTerminal } from '../../send-intent-state'
import { acknowledgeSendIntent, readSendResume } from '../../send-resume'
import {
  approximateTime, clock, dateTime, detailText, directionLabel, errorText,
  isTracked, shortRef, toDate, trackingLabel,
} from '../format'
import { EmptyWorkbench, InlineError, LedgerEmpty } from '../shared/Primitives'
import { newIntentId, type PendingSend } from '../shared/send'

export function ConversationPage({
  account, accountsError, accountsLoading, conversations, conversationsLoading, conversationsError,
  selectedConversationRef, selectedConversation, messages, audits, messagesError, auditsError,
  tab, busy, onTab, onSelectConversation, onTrack, onSelectM5Trial, onSendChanged,
}: {
  account: AccountView | null
  accountsError: string | null
  accountsLoading: boolean
  conversations: ConversationView[]
  conversationsLoading: boolean
  conversationsError: string | null
  selectedConversationRef: string
  selectedConversation: ConversationView | null
  messages: MessageView[]
  audits: AuditView[]
  messagesError: string | null
  auditsError: string | null
  tab: 'messages' | 'audits'
  busy: string
  onTab: (tab: 'messages' | 'audits') => void
  onSelectConversation: (conversationRef: string) => void
  onTrack: () => void
  onSelectM5Trial: () => void
  onSendChanged: () => void
}) {
  if (!account) return <EmptyWorkbench loading={accountsLoading} error={accountsError} />
  return (
    <div className="ledger-grid">
      <ConversationLedger
        rows={conversations}
        loading={conversationsLoading}
        error={conversationsError}
        selectedRef={selectedConversationRef}
        onSelect={onSelectConversation}
      />
      <ActivityLedger
        account={account}
        conversation={selectedConversation}
        messages={messages}
        audits={audits}
        messagesError={messagesError}
        auditsError={auditsError}
        tab={tab}
        busy={busy}
        onTab={onTab}
        onTrack={onTrack}
        onSelectM5Trial={onSelectM5Trial}
        onSendChanged={onSendChanged}
      />
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
        <h2 id="conversation-title">最近对话</h2>
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
        <h2 id="activity-title">{conversation?.peerDisplayName || '账号记录'}</h2>
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
