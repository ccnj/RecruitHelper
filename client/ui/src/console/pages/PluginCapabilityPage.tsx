// 插件能力测试：在真实页面上验证手侧原语确实能跑通，但全部不产生候选人可见
// 动作。页面按"一项能力一个区块"组织，后续新增能力照此追加。
//
// 目前只有邀面编辑器彩排（debug.probeInterviewEditor@1）一项：它与
// chat.sendInviteCard 字面共用同一编辑器准备实现，走完整选择与终核回读后停留
// 5 秒供肉眼确认，再取消并复核弹窗已关，构造性不含发送路径。
import { useCallback, useMemo, useState } from 'react'
import { AccountView, ConversationView, InterviewProbeResult, api } from '../../api'
import { errorText } from '../format'

const FIVE_MINUTES_MS = 5 * 60 * 1000

export function PluginCapabilityPage({ account, conversations, conversationsLoading, conversationsError }: {
  account: AccountView | null
  conversations: ConversationView[]
  conversationsLoading: boolean
  conversationsError?: unknown
}) {
  return (
    <div className="panel">
      <p>
        在真实页面上验证手侧原语能不能跑通。这里的每一项都不产生候选人可见动作，
        但会操作招聘人员自己看到的页面（开弹窗、点选择框），跑的时候别同时用浏览器。
      </p>
      <InterviewEditorProbe
        account={account}
        conversations={conversations}
        conversationsLoading={conversationsLoading}
        conversationsError={conversationsError}
      />
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
        ? '先选一个会话'
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

      <label className="probe-field">
        <span>会话</span>
        <select
          value={conversationRef}
          onChange={(event) => setConversationRef(event.target.value)}
          disabled={running || !account}
        >
          <option value="">
            {!account
              ? '先选账号'
              : conversationsLoading
                ? '读取中…'
                : conversations.length ? '请选择' : '该账号下没有会话'}
          </option>
          {conversations.map((row) => (
            <option key={row.conversationRef} value={row.conversationRef}>
              {row.peerDisplayName || '未命名'} · {row.conversationRef.slice(0, 12)}…
            </option>
          ))}
        </select>
      </label>
      {conversationsError ? <p className="sql-error">会话列表读取失败：{errorText(conversationsError)}</p> : null}

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
