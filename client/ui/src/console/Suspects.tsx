// suspect 队列常驻在所有页之上：它是唯一会挡住后续发送的东西，
// 藏进某一页就等于让人在别的页面上看不见自己被卡住了。
import { useState } from 'react'
import { api, Suspect } from '../api'
import { dateTime, errorText } from './format'
import { usePolling } from './usePolling'

export function Suspects() {
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
      {message && <div className="hint">{message}</div>}
    </section>
  )
}
