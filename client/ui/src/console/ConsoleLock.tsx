// 诊断台入口锁屏。策略（口令、过期点、存量）在 ./lock-state，这里只管呈现。
import { useEffect, useRef, useState } from 'react'
import { CONSOLE_PASSPHRASE, rememberUnlock } from './lock-state'

export function ConsoleLock({ onUnlocked, onCancel }: {
  onUnlocked: () => void
  onCancel: () => void
}) {
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => { inputRef.current?.focus() }, [])

  const submit = (event: React.FormEvent) => {
    event.preventDefault()
    if (value !== CONSOLE_PASSPHRASE) {
      setError('口令不对')
      setValue('')
      inputRef.current?.focus()
      return
    }
    rememberUnlock()
    onUnlocked()
  }

  return (
    <div className="dc-lock">
      <form className="dc-lock-panel" onSubmit={submit}>
        <div className="dc-lock-brand">
          <span className="dc-sidebar-brand-mark" aria-hidden="true" />
          <strong>诊断台</strong>
        </div>
        <p className="dc-lock-note">开发者入口。输入口令继续，Esc 返回客户端。</p>
        <label className="dc-lock-field">
          <span>口令</span>
          <input
            ref={inputRef}
            type="password"
            value={value}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => { setValue(event.target.value); setError('') }}
            onKeyDown={(event) => { if (event.key === 'Escape') { event.preventDefault(); onCancel() } }}
          />
        </label>
        <div className="dc-lock-actions">
          <button className="primary-button" type="submit" disabled={value === ''}>进入</button>
          <button type="button" onClick={onCancel}>返回客户端</button>
        </div>
        {/* 错误行常驻占位，出错时才上色——否则面板会跳一下高度。 */}
        <p className={`dc-lock-error${error ? ' is-shown' : ''}`} role="alert">{error || ' '}</p>
      </form>
    </div>
  )
}
