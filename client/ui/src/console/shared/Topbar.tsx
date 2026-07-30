// 常驻仪表带：脑的连接封印 + 每只手的卡槽。不属于任何一页，切页时不重挂。
import { useState } from 'react'
import { api, HandHealth, Health } from '../../api'
import { errorText, shortRef } from '../format'

export function ServiceSeal({ health, error }: { health: Health | undefined; error: string | null }) {
  if (error) {
    return (
      <div className="dc-seal is-offline" title={error}>
        <span className="signal-dot" />
        <strong>脑未连接</strong>
      </div>
    )
  }
  if (!health) {
    return (
      <div className="dc-seal is-waiting">
        <span className="signal-dot" />
        <strong>连接中</strong>
      </div>
    )
  }
  return (
    <div className="dc-seal is-online">
      <span className="signal-dot" />
      <strong>脑在线</strong>
      <span className="mono dim">proto v{health.proto}</span>
    </div>
  )
}

export function HandsBar({ hands, onRefresh }: { hands: HandHealth[]; onRefresh: () => void }) {
  const [reloading, setReloading] = useState('')
  const [message, setMessage] = useState('')
  const reload = async (handId: string) => {
    setReloading(handId)
    setMessage('已请求手自重载，等待新版本重新报到…')
    try {
      const result = await api.reloadHand(handId)
      setMessage(`新版本已就绪 · boot ${shortRef(result.bootId, 10)} · 扩展 ${result.extensionVersion || '版本未知'}`)
      onRefresh()
    } catch (reason) {
      setMessage(errorText(reason))
    } finally {
      setReloading('')
    }
  }
  return (
    <div className="dc-hands">
      {hands.length === 0 && <span className="dim">暂无手</span>}
      {hands.map((hand) => (
        <div key={hand.handId} className={`dc-hand ${hand.online ? 'is-online' : 'is-offline'}`}>
          <span className="signal-dot" />
          <span className="mono">{shortRef(hand.handId, 10)}</span>
          <span className={!hand.contractMatch ? 'warn' : hand.health === 'stalled' ? 'bad' : 'dim'}>
            {hand.contractMatch ? hand.extensionVersion || '契约匹配' : '契约不匹配'}
          </span>
          <span className="dim mono">{hand.online ? `${Math.round(hand.lastHbAgoMs / 1000)}s` : '离线'}</span>
          <button
            className="compact-button"
            disabled={!hand.online || reloading !== '' || !hand.caps.includes('debug.reload@1')}
            onClick={() => void reload(hand.handId)}
            title="重载并确认：换代插件后必须在安全窗口内执行"
          >
            {reloading === hand.handId ? '重载中…' : '重载'}
          </button>
        </div>
      ))}
      {message && <span className="dc-hand-msg dim">{message}</span>}
    </div>
  )
}
