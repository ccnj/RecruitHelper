// SQL 控制台:语句原样交给脑执行,结果原样回显(AGENTS.md「开发者 SQL 控制台
// 例外」,2026-07-30 甲方裁决)。按裁决不设护栏——不挑语句、不预览、不备份、
// 不确认。前端同样不得把结果落到 localStorage、导出文件或错误上报里。
import { useCallback, useEffect, useState } from 'react'
import { api, DevSQLResult, STATUS_BAR_POSITIONS, StatusBarPosition, StatusBarSettings } from '../../api'
import { errorText } from '../format'

export function DevToolsPage() {
  return (
    <>
      <StatusBarSwitch />
      <SQLConsole />
    </>
  )
}

// 屏幕顶层状态栏的详细模式(2026-09-08 甲方裁决,出口 docs/boss/状态栏出口-2026-09-08.md)。
// 开关落在脑里、重启不丢;这是唯一能打开它的路径。开了之后最近命令(带候选人
// 姓名)常驻屏幕最上层,远程协助时对方也看得见——所以默认关,用完记得关。
function StatusBarSwitch() {
  const [settings, setSettings] = useState<StatusBarSettings | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    try {
      setSettings(await api.statusBarSettings())
      setError(null)
    } catch (reason) {
      setError(errorText(reason))
    }
  }, [])
  useEffect(() => { void load() }, [load])

  const toggle = useCallback(async (next: boolean) => {
    if (saving) return
    setSaving(true)
    setError(null)
    try {
      setSettings(await api.setStatusBarDetail(next))
    } catch (reason) {
      setError(errorText(reason))
    } finally {
      setSaving(false)
    }
  }, [saving])

  const toggleVisible = useCallback(async (next: boolean) => {
    if (saving) return
    setSaving(true)
    setError(null)
    try {
      setSettings(await api.setStatusBarVisible(next))
    } catch (reason) {
      setError(errorText(reason))
    } finally {
      setSaving(false)
    }
  }, [saving])

  const setPosition = useCallback(async (position: StatusBarPosition) => {
    if (saving) return
    setSaving(true)
    setError(null)
    try {
      setSettings(await api.setStatusBarPosition(position))
    } catch (reason) {
      setError(errorText(reason))
    } finally {
      setSaving(false)
    }
  }, [saving])

  const enabled = settings?.detailEnabled === true
  const visible = settings?.visible === true
  const visibleNote = settings === null
    ? ''
    : settings.visibleSource === 'platformDefault'
      ? `（当前按平台默认：${settings.platform === 'boss' ? 'BOSS，开' : '智联，关'}）`
      : '（已手动设置，不再跟平台）'
  return (
    <div className="panel">
      <h3>状态栏</h3>
      <div className="dc-switch-row">
        <button
          aria-checked={visible}
          aria-label="显示状态栏"
          className={`dc-switch${visible ? ' is-on' : ''}`}
          disabled={saving || settings === null}
          onClick={() => void toggleVisible(!visible)}
          role="switch"
          type="button"
        />
        <span>显示状态栏{visibleNote}</span>
      </div>
      <div className="dc-switch-row" role="radiogroup" aria-label="状态栏位置">
        <span>位置</span>
        {STATUS_BAR_POSITIONS.map((item) => (
          <label key={item.value} className="dc-radio">
            <input
              type="radio"
              name="statusbar-position"
              value={item.value}
              checked={settings?.position === item.value}
              disabled={saving || settings === null}
              onChange={() => void setPosition(item.value)}
            />
            {item.label}
          </label>
        ))}
      </div>
      <div className="dc-switch-row">
        <button
          aria-checked={enabled}
          aria-label="状态栏详细模式"
          className={`dc-switch${enabled ? ' is-on' : ''}`}
          disabled={saving || settings === null}
          onClick={() => void toggle(!enabled)}
          role="switch"
          type="button"
        />
        <span>详细模式</span>
      </div>
      <p>
        显示开关没人拨过时跟平台：BOSS 客户开、智联客户关；拨过一次就以拨的为准、重启仍保持。
        状态栏贴在主屏工作区的某个角，位置几秒内生效、重启仍保持。默认只显示一句话状态；
        打开详细模式后多显示最近五条命令（原语、候选人、状态、耗时）与批次、插件、待裁决摘要，
        同样几秒内生效、重启仍保持。候选人姓名会常驻在屏幕最上层，远程协助时对方也看得见，用完记得关。
      </p>
      {error && <p className="sql-error">{error}</p>}
    </div>
  )
}

function SQLConsole() {
  const [sql, setSQL] = useState('')
  const [result, setResult] = useState<DevSQLResult | null>(null)
  const [transportError, setTransportError] = useState<string | null>(null)
  const [running, setRunning] = useState(false)
  const [elapsedMs, setElapsedMs] = useState<number | null>(null)

  const run = useCallback(async () => {
    if (!sql.trim() || running) return
    setRunning(true)
    setTransportError(null)
    const startedAt = performance.now()
    try {
      setResult(await api.devSQL(sql))
    } catch (reason) {
      setResult(null)
      setTransportError(errorText(reason))
    } finally {
      setElapsedMs(Math.round(performance.now() - startedAt))
      setRunning(false)
    }
  }, [sql, running])

  return (
    <div className="sql-console">
      <textarea
        className="sql-input mono"
        value={sql}
        spellCheck={false}
        placeholder="select * from candidate_profiles limit 20"
        onChange={(event) => setSQL(event.target.value)}
        onKeyDown={(event) => {
          if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
            event.preventDefault()
            void run()
          }
        }}
      />
      <div className="sql-bar">
        <button onClick={() => void run()} disabled={running || !sql.trim()}>
          {running ? '执行中…' : '执行'}
        </button>
        <small>Cmd/Ctrl + Enter 执行。直接写库，没有确认也没有备份。</small>
        {elapsedMs !== null && !running && <small className="mono">{elapsedMs} ms</small>}
      </div>
      <SQLResult result={result} transportError={transportError} />
    </div>
  )
}

function SQLResult({ result, transportError }: {
  result: DevSQLResult | null
  transportError: string | null
}) {
  if (transportError) return <p className="sql-error">连不上脑：{transportError}</p>
  if (!result) return null
  if (result.error) return <p className="sql-error mono">{result.error}</p>

  if (!result.returnedRows) {
    return <p className="sql-ok">执行成功，影响 {result.rowsAffected ?? 0} 行。</p>
  }
  const columns = result.columns ?? []
  const rows = result.rows ?? []
  if (rows.length === 0) {
    return <p className="sql-ok">执行成功，0 行。</p>
  }
  return (
    <>
      <p className="sql-ok">{rows.length} 行。</p>
      <div className="sql-table-scroll">
        <table className="sql-table mono">
          <thead>
            <tr>{columns.map((column) => <th key={column}>{column}</th>)}</tr>
          </thead>
          <tbody>
            {rows.map((row, rowIndex) => (
              <tr key={rowIndex}>
                {row.map((cell, cellIndex) => (
                  <td key={cellIndex} className={cell === null ? 'sql-null' : undefined}>
                    {sqlCellText(cell)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  )
}

function sqlCellText(cell: unknown): string {
  if (cell === null || cell === undefined) return 'NULL'
  if (typeof cell === 'string') return cell
  if (typeof cell === 'number' || typeof cell === 'boolean') return String(cell)
  return JSON.stringify(cell)
}
