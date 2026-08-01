// suspect 队列常驻在所有页之上：它是唯一会挡住后续发送的东西，
// 藏进某一页就等于让人在别的页面上看不见自己被卡住了。
//
// 每条默认只占一行——动作、是谁、发的什么、什么时候，够认出"这是哪件事"；
// 裁决要看的全部现场折在里面。裁决 resolvedOk/resolvedFailed 是替平台事实
// 下结论，判 resolvedFailed 而其实已经发出去，后续补发就是多发一条给真人，
// 所以现场必须给全，但不能把队列撑成一堵墙。
import { useState } from 'react'
import { api, Suspect } from '../api'
import { dateTime, errorText, shortRef } from './format'
import { RawField } from './shared/Primitives'
import { usePolling } from './usePolling'

export function Suspects({ onOpenConversation }: {
  onOpenConversation: (platform: string, accountRef: string, conversationRef: string) => void
}) {
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
  if (rows.length === 0) {
    return (
      <div className="dc-suspect-clear">
        <span>suspect 队列为空</span>
        {message && <span className="dim">{message}</span>}
      </div>
    )
  }
  return (
    <section className="dc-suspects" aria-label="suspect 队列">
      <h3>suspect 队列 · 待人工裁决 {rows.length}</h3>
      <div className="dc-suspect-list">
        {rows.map((item: Suspect) => (
          <SuspectRow
            key={item.msgId}
            item={item}
            onVerdict={verdict}
            onOpenConversation={onOpenConversation}
          />
        ))}
      </div>
      {message && <div className="hint">{message}</div>}
    </section>
  )
}

function SuspectRow({ item, onVerdict, onOpenConversation }: {
  item: Suspect
  onVerdict: (msgId: string, value: 'resolvedOk' | 'resolvedFailed') => void
  onOpenConversation: (platform: string, accountRef: string, conversationRef: string) => void
}) {
  const [open, setOpen] = useState(false)
  const who = item.peerDisplayName || (item.conversationRef ? shortRef(item.conversationRef, 8) : '')
  const jumpable = Boolean(item.platform && item.accountRef && item.conversationRef)
  // <details> 收起时子节点仍在 DOM 里。队列长的时候（真出现过 50+），那就是
  // 每 1.5 秒一次轮询把上百段 JSON 重新格式化一遍、挂上百个 <pre>。只在展开
  // 时渲染详情，收起的行就只是一行字。
  return (
    <details className="dc-suspect" onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>
        <span className="dc-suspect-action">{item.action || item.name}</span>
        {who && <span className="dc-suspect-who">{who}</span>}
        {/* 摘要为空也要占位：它是伸缩项，缺了这一格时间与按钮就会左移，
            整列对不齐，密集列表就没法一眼扫下来。 */}
        <span className="dc-suspect-summary">{item.summary}</span>
        <span className="dc-suspect-when dim">{dateTime(item.dispatchedAtMs)}</span>
        <span className="dc-suspect-verdicts">
          <button
            className="compact-button"
            disabled={!item.reviewReady}
            onClick={(event) => { event.preventDefault(); onVerdict(item.msgId, 'resolvedOk') }}
          >
            确认已发生
          </button>
          <button
            className="compact-button"
            disabled={!item.reviewReady}
            onClick={(event) => { event.preventDefault(); onVerdict(item.msgId, 'resolvedFailed') }}
          >
            确认未发生
          </button>
        </span>
      </summary>

      {open && <>
      <dl className="dc-suspect-facts">
        <dt>原因</dt>
        <dd>{item.reasonText || item.reason || '未记录'}</dd>

        {item.summary && (<><dt>内容</dt><dd className="dc-suspect-body">{item.summary}</dd></>)}

        {item.conversationRef && (
          <>
            <dt>会话</dt>
            <dd>
              {item.peerDisplayName || '姓名未投影'} <code>{shortRef(item.conversationRef, 10)}</code>
              {jumpable && (
                <button
                  className="compact-button"
                  onClick={() => onOpenConversation(item.platform, item.accountRef, item.conversationRef)}
                >
                  去看这条会话
                </button>
              )}
            </dd>
          </>
        )}

        <dt>账号</dt>
        <dd>{item.platform || '—'} <code>{shortRef(item.accountRef, 10)}</code></dd>

        <dt>命令</dt>
        <dd>
          <code>{item.name}</code> <code>{shortRef(item.msgId, 10)}</code>
          {' · '}已验证 {item.verificationAttempts} 轮
          {item.intentId && <> · 意图 <code>{shortRef(item.intentId, 10)}</code></>}
          {!item.reviewReady && item.reviewAfter
            ? ` · 最早 ${dateTime(item.reviewAfter)} 可裁决`
            : ' · 可裁决'}
        </dd>

        {(item.errorCode || item.sideEffect) && (
          <>
            <dt>结果</dt>
            <dd>
              {item.errorCode && <>错误码 <code>{item.errorCode}</code> </>}
              {item.sideEffect && <>副作用标注 <code>{item.sideEffect}</code></>}
            </dd>
          </>
        )}
      </dl>

      {/* 本地账是脑的投影，很可能正因为这条 suspect 而不完整。不写这句，
          这个按钮会诱导人按本地看不到就判 resolvedFailed——而那正是最贵的
          判错方向：判错了后续补发就是实打实多发一条给真人。 */}
      <p className="dc-suspect-caution">
        本地会话账是脑的投影，可能正因为这条未确认而不完整。<strong>本地看不到不等于没发生</strong>，以平台页面为准。
      </p>

      <RawSite item={item} />
      </>}
    </details>
  )
}

function RawSite({ item }: { item: Suspect }) {
  const [open, setOpen] = useState(false)
  return (
    <details className="dc-raw-fold" onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>原始现场</summary>
      {open && <>
        <RawField label="args" value={item.args} />
        <RawField label="guards" value={item.guards} />
        <RawField label="result" value={item.resultBody} />
      </>}
    </details>
  )
}
