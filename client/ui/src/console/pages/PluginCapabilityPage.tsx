// 插件能力测试：在真实页面上验证手侧原语确实能跑通，但全部不产生候选人可见
// 动作。页面按"一项能力一个区块"组织，后续新增能力照此追加。
//
// 邀面编辑器彩排（debug.probeInterviewEditor@1）与 chat.sendInviteCard 字面
// 共用同一编辑器准备实现，走完整选择与终核回读后停留 5 秒供肉眼确认，再取消
// 并复核弹窗已关，构造性不含发送路径。
//
// 运营通知彩排则会真的发一条消息到运营群（候选人那边仍然什么都收不到）。它
// 一行都不写发件箱，构造性影响不到线上那条真通知。
import { useCallback, useMemo, useState } from 'react'
import {
  AccountView,
  ConversationView,
  InterviewProbeResult,
  NotifyProbeImage,
  NotifyProbeResult,
  api,
} from '../../api'
import { errorText } from '../format'

const FIVE_MINUTES_MS = 5 * 60 * 1000

export function PluginCapabilityPage({ account, conversations, conversationsLoading, conversationsError }: {
  account: AccountView | null
  conversations: ConversationView[]
  conversationsLoading: boolean
  conversationsError?: unknown
}) {
  const picker = { conversations, conversationsLoading, conversationsError }
  return (
    <div className="panel">
      <p>
        在真实页面上验证手侧原语能不能跑通。这里的每一项都不产生候选人可见动作，
        但会操作招聘人员自己看到的页面（开弹窗、点选择框），跑的时候别同时用浏览器。
      </p>
      <InterviewEditorProbe account={account} {...picker} />
      <NotifyProbe account={account} {...picker} />
    </div>
  )
}

function InterviewEditorProbe({ account, conversations, conversationsLoading, conversationsError }: {
  account: AccountView | null
  conversations: ConversationView[]
  conversationsLoading: boolean
  conversationsError?: unknown
}) {
  const [conversationRef, setConversationRef] = useState('')
  const [method, setMethod] = useState<'onsite' | 'wechatVideo'>('onsite')
  const [startsAtText, setStartsAtText] = useState(defaultStartsAtText)
  const [result, setResult] = useState<InterviewProbeResult | null>(null)
  const [transportError, setTransportError] = useState<string | null>(null)
  const [running, setRunning] = useState(false)
  const [elapsedMs, setElapsedMs] = useState<number | null>(null)

  const startsAt = useMemo(() => parseLocalMinute(startsAtText), [startsAtText])
  const timeProblem = useMemo(() => {
    if (startsAt === null) return '请选择面试时间'
    if (startsAt <= Date.now()) return '面试时间必须在将来'
    if (startsAt % FIVE_MINUTES_MS !== 0) return '平台时间选择器是 5 分钟一格，分钟必须是 0/5/10…'
    return ''
  }, [startsAt])

  const blocked = !account
    ? '先在「账号与巡检」选一个账号'
    : !account.handOnline
      ? '该账号绑定的手不在线'
      : !conversationRef
        ? '先选一个会话，或粘贴 sessionId'
        : timeProblem

  const run = useCallback(async () => {
    if (running || blocked || !account || startsAt === null) return
    setRunning(true)
    setTransportError(null)
    setResult(null)
    const startedAt = performance.now()
    try {
      setResult(await api.probeInterviewEditor({
        platform: account.platform,
        accountRef: account.accountRef,
        conversationRef,
        startsAt,
        method,
      }))
    } catch (reason) {
      setTransportError(errorText(reason))
    } finally {
      setElapsedMs(Math.round(performance.now() - startedAt))
      setRunning(false)
    }
  }, [running, blocked, account, conversationRef, startsAt, method])

  return (
    <section className="probe-block">
      <h3>邀面编辑器彩排</h3>
      <p>
        把发面试卡的整套编辑器操作真做一遍——开弹窗、切面试类型、填日期时间
        （线上还要填时长和方式）、回读核对——然后<strong>停 5 秒给你看清</strong>，
        再自己点取消并确认弹窗已关。全程不碰发送按钮，候选人那边什么都收不到。
      </p>
      <p className="probe-note">
        两个前提：目标会话的聊天页必须已经在浏览器里打开（它不会自己导航过去）；
        已经约过面试的会话不能彩排——那种会话的入口按钮会变成「面试详情」，
        点开的是详情而不是邀请。
      </p>

      <ConversationPicker
        value={conversationRef}
        onChange={setConversationRef}
        disabled={running}
        account={account}
        conversations={conversations}
        conversationsLoading={conversationsLoading}
        conversationsError={conversationsError}
        note={(
          <>
            会话就是平台聊天页地址栏 <code>?sessionId=</code> 后面那串。手侧拿它去匹配
            你已经打开的标签页，所以<strong>不需要这条会话在脑的库里</strong>——没巡检过、
            没收编过都能彩排。填错或页面没开只会报「目标页面不存在」，不会跑到别人身上。
          </>
        )}
      />

      <label className="probe-field">
        <span>面试类型</span>
        <span className="probe-radios">
          <label>
            <input
              type="radio"
              name="probe-method"
              checked={method === 'onsite'}
              onChange={() => setMethod('onsite')}
              disabled={running}
            />
            现场面试
          </label>
          <label>
            <input
              type="radio"
              name="probe-method"
              checked={method === 'wechatVideo'}
              onChange={() => setMethod('wechatVideo')}
              disabled={running}
            />
            线上面试（微信视频）
          </label>
        </span>
      </label>

      <label className="probe-field">
        <span>面试时间</span>
        <input
          className="sql-input"
          type="datetime-local"
          step={300}
          value={startsAtText}
          onChange={(event) => setStartsAtText(event.target.value)}
          disabled={running}
        />
      </label>
      <p className="probe-note">
        现场面试没有时长可填，平台也不返回结束时间；线上面试固定按 30 分钟彩排。
      </p>

      <div className="sql-bar">
        <button onClick={() => void run()} disabled={running || Boolean(blocked)}>
          {running ? '彩排中，请看浏览器…' : '开始彩排'}
        </button>
        {blocked && !running ? <small className="probe-blocked">{blocked}</small> : null}
        {elapsedMs !== null && !running ? <small className="mono">{elapsedMs} ms</small> : null}
      </div>

      <ProbeOutcome result={result} transportError={transportError} />
    </section>
  )
}

// 会话选择器：手打/粘贴 sessionId 为主，库里已巡检过的会话作为便捷入口。
// 两个彩排区块共用，说明文字各自传入——它们对"必须在库里吗"的答案正相反。
function ConversationPicker({
  value, onChange, disabled, note,
  account, conversations, conversationsLoading, conversationsError,
}: {
  value: string
  onChange: (next: string) => void
  disabled: boolean
  note: React.ReactNode
  account: AccountView | null
  conversations: ConversationView[]
  conversationsLoading: boolean
  conversationsError?: unknown
}) {
  return (
    <>
      <label className="probe-field">
        <span>会话 sessionId</span>
        <input
          className="sql-input"
          value={value}
          onChange={(event) => onChange(normalizeConversationRef(event.target.value))}
          placeholder="从聊天页地址栏复制，整条 URL 粘进来也行"
          disabled={disabled}
        />
      </label>
      <p className="probe-note">{note}</p>

      <label className="probe-field">
        <span>或从库里选</span>
        <select
          value={conversations.some((row) => row.conversationRef === value) ? value : ''}
          onChange={(event) => onChange(event.target.value)}
          disabled={disabled || !account}
        >
          <option value="">
            {!account
              ? '先选账号'
              : conversationsLoading
                ? '读取中…'
                : conversations.length ? '请选择' : '该账号下没有巡检过的会话'}
          </option>
          {conversations.map((row) => (
            <option key={row.conversationRef} value={row.conversationRef}>
              {row.peerDisplayName || '未命名'} · {row.conversationRef.slice(0, 12)}…
            </option>
          ))}
        </select>
      </label>
      {conversationsError ? <p className="sql-error">会话列表读取失败：{errorText(conversationsError)}</p> : null}
    </>
  )
}

// 运营通知彩排：把线上那条通知的完整链路真跑一遍——现场截聊天长图与简历长图、
// 按候选人此刻的状态/微信/画像现算正文、直发运营群。
//
// 它一行都不写发件箱：不入队、不占 event_key、不落截图事实行。这不是洁癖——
// 发件箱的 event_key 是唯一索引且入队时撞了就静默跳过，彩排只要占掉一个 key，
// 日后那个人真的换到微信或真的约成面试时，入队会被悄悄吃掉，线上永远少发一条
// 且不报错。
function NotifyProbe({ account, conversations, conversationsLoading, conversationsError }: {
  account: AccountView | null
  conversations: ConversationView[]
  conversationsLoading: boolean
  conversationsError?: unknown
}) {
  const [conversationRef, setConversationRef] = useState('')
  const [notifyType, setNotifyType] = useState<'' | 'wecomInterviewAccepted' | 'wecomWechatAdded'>('')
  const [result, setResult] = useState<NotifyProbeResult | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [running, setRunning] = useState(false)
  const [elapsedMs, setElapsedMs] = useState<number | null>(null)

  const blocked = !account
    ? '先在「账号与巡检」选一个账号'
    : !account.handOnline
      ? '该账号绑定的手不在线'
      : !conversationRef
        ? '先选一个会话，或粘贴 sessionId'
        : ''

  const run = useCallback(async () => {
    if (running || blocked || !account) return
    setRunning(true)
    setFailure(null)
    setResult(null)
    const startedAt = performance.now()
    try {
      setResult(await api.probeNotify({
        platform: account.platform,
        accountRef: account.accountRef,
        conversationRef,
        ...(notifyType ? { notifyType } : {}),
      }))
    } catch (reason) {
      setFailure(errorText(reason))
    } finally {
      setElapsedMs(Math.round(performance.now() - startedAt))
      setRunning(false)
    }
  }, [running, blocked, account, conversationRef, notifyType])

  return (
    <section className="probe-block">
      <h3>运营通知彩排</h3>
      <p>
        把运营群通知的整条链路真跑一遍：截聊天记录长图、截简历长图，按这个人
        <strong>此刻</strong>的状态、微信号和画像现算正文，然后
        <strong>真的发到运营群</strong>。候选人那边什么都收不到。
      </p>
      <p className="probe-note">
        正文<strong>不带任何测试标记</strong>，和线上通知长得一模一样——这是为了看到
        真实观感，代价是群里的人分不出真假，跑之前最好跟他们打个招呼。
      </p>
      <p className="probe-note">
        它一行都不写发件箱，所以<strong>影响不到线上那条真通知</strong>：这个人日后真换到
        微信、真约成面试，该发的照发，也不会因为你彩排过就被顶掉。
      </p>
      <p className="probe-note">
        两个前提：目标会话的聊天页必须已经在浏览器里打开并在前台（截图不会自己
        导航过去）；这个人必须<strong>已经在库里收编过</strong>——简历截图和正文里的状态、
        微信号都得从档案里取，陌生会话会被直接拒绝。截图期间会开一次简历弹窗，
        跑的时候别同时用浏览器。
      </p>

      <ConversationPicker
        value={conversationRef}
        onChange={setConversationRef}
        disabled={running}
        account={account}
        conversations={conversations}
        conversationsLoading={conversationsLoading}
        conversationsError={conversationsError}
        note={(
          <>
            会话就是平台聊天页地址栏 <code>?sessionId=</code> 后面那串，整条 URL 粘进来
            也行。它同时用来定位你打开的标签页和反查候选人档案。
          </>
        )}
      />

      <label className="probe-field">
        <span>通知模板</span>
        <select
          value={notifyType}
          onChange={(event) => setNotifyType(event.target.value as typeof notifyType)}
          disabled={running}
        >
          <option value="">按当前状态自动选（已约面→面试确认，否则微信互加）</option>
          <option value="wecomInterviewAccepted">面试确认</option>
          <option value="wecomWechatAdded">微信互加</option>
        </select>
      </label>

      <div className="sql-bar">
        <button onClick={() => void run()} disabled={running || Boolean(blocked)}>
          {running ? '正在截图并发送，可能要一两分钟…' : '截图并发通知'}
        </button>
        {blocked && !running ? <small className="probe-blocked">{blocked}</small> : null}
        {elapsedMs !== null && !running ? <small className="mono">{elapsedMs} ms</small> : null}
      </div>

      {failure ? <p className="sql-error">没发出去：{failure}</p> : null}
      {result ? (
        <>
          <p className="sql-ok">
            已发到运营群（{result.notifyType === 'wecomInterviewAccepted' ? '面试确认' : '微信互加'}模板）
          </p>
          <p className="probe-note">发出去的正文原文：</p>
          <pre className="probe-content">{result.content}</pre>
          <dl className="probe-readback">
            <dt>聊天截图</dt>
            <dd><ImageOutcome image={result.chat} note={result.chatNote} /></dd>
            <dt>简历截图</dt>
            <dd><ImageOutcome image={result.resume} note={result.resumeNote} /></dd>
          </dl>
        </>
      ) : null}
    </section>
  )
}

function ImageOutcome({ image, note }: { image: NotifyProbeImage; note?: string }) {
  if (!image.present) {
    return <span className="probe-absent">没拍到，按缺图发的{note ? `：${note}` : ''}</span>
  }
  const size = `${Math.round((image.byteSize ?? 0) / 1024)} KB`
  if (image.sent) {
    return <span className="mono">已发出 · {size}{note ? ` · ${note}` : ''}</span>
  }
  return (
    <span className="probe-absent">
      拍到了但没发出（{image.skipped ? `企微跳过：${image.skipped}` : image.error}）· {size}
    </span>
  )
}

function ProbeOutcome({ result, transportError }: {
  result: InterviewProbeResult | null
  transportError: string | null
}) {
  if (transportError) return <p className="sql-error">连不上脑：{transportError}</p>
  if (!result) return null

  const data = result.data
  const failed = result.status !== 'ok'
  return (
    <>
      {failed ? (
        <p className="sql-error">
          彩排失败{result.errorCode ? `（${result.errorCode}）` : ''}
          {result.error?.message ? `：${result.error.message}` : ''}
        </p>
      ) : (
        <p className="sql-ok">
          彩排成功，编辑器已按填入值回读并取消
          {data?.canceled === false ? '（注意：弹窗未确认关闭，请手工检查）' : ''}
        </p>
      )}
      {result.automationActive ? (
        <p className="probe-note">
          该会话的沟通自动化仍是 active。彩排不再因此拦截，但巡检若在同时跑，
          可能和彩排抢同一个页面——由你判断。
        </p>
      ) : null}
      {data ? (
        <dl className="probe-readback">
          <ReadbackItem label="日期" value={data.dateValue} />
          <ReadbackItem label="时间" value={data.timeValue} />
          <ReadbackItem label="时长" value={data.durationValue} absent="现场面试无此控件" />
          <ReadbackItem label="方式" value={data.methodValue} absent="现场面试无此控件" />
          <ReadbackItem label="弹窗已关闭" value={data.canceled === undefined ? undefined : data.canceled ? '是' : '否'} />
        </dl>
      ) : null}
      {result.msgId ? <p className="mono probe-msgid">msgId {result.msgId}</p> : null}
    </>
  )
}

function ReadbackItem({ label, value, absent }: { label: string; value?: string; absent?: string }) {
  return (
    <>
      <dt>{label}</dt>
      <dd className={value === undefined ? 'probe-absent' : 'mono'}>
        {value === undefined ? absent ?? '未回读' : value}
      </dd>
    </>
  )
}

// 会话就是平台 URL 上的 sessionId。人多半直接复制整条地址栏，这里宽容地把它
// 抠出来；抠不到就按裸 id 原样收下，真假由手侧匹配标签页时裁决。
function normalizeConversationRef(input: string): string {
  const text = input.trim()
  const matched = /[?&#]sessionId=([^&#\s]+)/u.exec(text)
  if (!matched) return text
  try {
    return decodeURIComponent(matched[1])
  } catch {
    return matched[1]
  }
}

// datetime-local 给的是本地时区的「年-月-日T时:分」，按本地时区解析成毫秒。
function parseLocalMinute(text: string): number | null {
  const matched = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/u.exec(text.trim())
  if (!matched) return null
  const [, year, month, day, hour, minute] = matched
  const at = new Date(
    Number(year), Number(month) - 1, Number(day), Number(hour), Number(minute), 0, 0,
  )
  return Number.isFinite(at.getTime()) ? at.getTime() : null
}

function defaultStartsAtText(): string {
  const at = new Date()
  at.setDate(at.getDate() + 1)
  at.setHours(14, 0, 0, 0)
  const pad = (value: number): string => String(value).padStart(2, '0')
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T14:00`
}
