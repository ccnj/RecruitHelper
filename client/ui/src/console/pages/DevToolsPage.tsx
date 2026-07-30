// SQL 控制台:语句原样交给脑执行,结果原样回显(AGENTS.md「开发者 SQL 控制台
// 例外」,2026-07-30 甲方裁决)。按裁决不设护栏——不挑语句、不预览、不备份、
// 不确认。前端同样不得把结果落到 localStorage、导出文件或错误上报里。
import { useCallback, useState } from 'react'
import { api, DevSQLResult } from '../../api'
import { errorText } from '../format'

export function DevToolsPage() {
  return <SQLConsole />
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
