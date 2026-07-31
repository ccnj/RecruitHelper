// 现场数据上报(AGENTS.md「全局约定·现场数据上报」,2026-07-31 甲方裁决)。
// 点一下,脑现场打包 brain.log / brain.db / ai-traces.db 传到旧后台,我方在
// 管理前台的审计页取回。按裁决只由人显式点击触发:这里没有定时、没有自动重试,
// 传失败就再点一次。
import { useCallback, useEffect, useState } from 'react'
import { api, FieldReportResult, FieldReportSettings } from '../../api'
import { errorText } from '../format'

export function FieldReportPage() {
  return (
    <>
      <AutoUploadSwitch />
      <ManualUpload />
    </>
  )
}

// 每日自动上传开关。裁决把这里定成**唯一**的开启路径：安装、升级、迁移、配置
// 下发都不得把它翻成开启，所以这个勾选框是它能变 true 的全部来源。
function AutoUploadSwitch() {
  const [settings, setSettings] = useState<FieldReportSettings | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    try {
      setSettings(await api.devReportSettings())
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
      setSettings(await api.setDevReportAutoUpload(next))
    } catch (reason) {
      setError(errorText(reason))
    } finally {
      setSaving(false)
    }
  }, [saving])

  const enabled = settings?.autoUploadEnabled === true
  return (
    <div className="panel">
      <label>
        <input
          type="checkbox"
          checked={enabled}
          disabled={saving || settings === null}
          onChange={(event) => void toggle(event.target.checked)}
        />
        {' '}自动上传数据至服务器
      </label>
      <p>
        勾选后每天凌晨 00:10 自动打包上传。那一刻若还有工作流在跑或有命令没收束，
        会往后顺延，到 02:00 仍不合适就跳过当天；客户端没开着而错过的当天不补传。
      </p>
      {error && <p className="sql-error">{error}</p>}
      <LastAutoRun settings={settings} />
    </div>
  )
}

function LastAutoRun({ settings }: { settings: FieldReportSettings | null }) {
  if (!settings?.lastAutoAt) return null
  const when = new Date(settings.lastAutoAt)
  const text = Number.isNaN(when.getTime()) ? settings.lastAutoAt : when.toLocaleString('zh-CN')
  if (settings.lastAutoOk) {
    return <p className="sql-ok">上次自动上传：{text} 成功</p>
  }
  return (
    <p className="sql-error">
      上次自动上传：{text} 未成功{settings.lastAutoError ? ` —— ${settings.lastAutoError}` : ''}
    </p>
  )
}

function ManualUpload() {
  const [result, setResult] = useState<FieldReportResult | null>(null)
  const [transportError, setTransportError] = useState<string | null>(null)
  const [running, setRunning] = useState(false)
  const [elapsedMs, setElapsedMs] = useState<number | null>(null)

  const run = useCallback(async () => {
    if (running) return
    setRunning(true)
    setTransportError(null)
    setResult(null)
    const startedAt = performance.now()
    try {
      setResult(await api.devReport())
    } catch (reason) {
      setTransportError(errorText(reason))
    } finally {
      setElapsedMs(Math.round(performance.now() - startedAt))
      setRunning(false)
    }
  }, [running])

  return (
    <div className="panel">
      <p>
        把本机的运行日志与两个数据库打包上传，供我方远程排障。包里含候选人明文，
        只在我方服务器留存，取回要管理后台登录。
      </p>
      <p>
        打包期间会给业务库做一次一致性快照，脑的写入要排队等它，几十 MB 的库约几秒。
        建议在没有工作流跑着的时候点。
      </p>
      <div className="sql-bar">
        <button onClick={() => void run()} disabled={running}>
          {running ? '打包上传中…' : '打包并上传'}
        </button>
        {elapsedMs !== null && !running && <small className="mono">{elapsedMs} ms</small>}
      </div>
      <FieldReportOutcome result={result} transportError={transportError} />
    </div>
  )
}

function FieldReportOutcome({ result, transportError }: {
  result: FieldReportResult | null
  transportError: string | null
}) {
  if (transportError) return <p className="sql-error">连不上脑：{transportError}</p>
  if (!result) return null

  const manifest = result.manifest
  return (
    <>
      {result.error ? (
        <p className="sql-error">上报失败：{result.error}</p>
      ) : (
        <p className="sql-ok">
          已上传 {formatBytes(result.sizeBytes)}
          {result.reportKey ? <> · 回执 <span className="mono">{result.reportKey}</span></> : null}
        </p>
      )}

      {manifest?.files?.length ? (
        <table className="sql-table mono">
          <thead>
            <tr><th>包内文件</th><th>字节</th></tr>
          </thead>
          <tbody>
            {manifest.files.map((file) => (
              <tr key={file.name}>
                <td>{file.name}</td>
                <td>{formatBytes(file.bytes)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}

      {manifest?.skipped?.length ? (
        <>
          <p className="sql-error">以下项没能进包：</p>
          <ul>
            {manifest.skipped.map((item) => <li key={item} className="mono">{item}</li>)}
          </ul>
        </>
      ) : null}
    </>
  )
}

function formatBytes(bytes: number | undefined): string {
  if (!bytes || bytes < 0) return '0 B'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
