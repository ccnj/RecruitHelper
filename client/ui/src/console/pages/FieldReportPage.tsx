// 现场数据上报(AGENTS.md「全局约定·现场数据上报」,2026-07-31 甲方裁决)。
// 点一下,脑现场打包 brain.log / brain.db / ai-traces.db 传到旧后台,我方在
// 管理前台的审计页取回。按裁决只由人显式点击触发:这里没有定时、没有自动重试,
// 传失败就再点一次。
import { useCallback, useEffect, useState } from 'react'
import { api, ChatReportRunResult, FieldReportResult, FieldReportSettings, LogReportSettings } from '../../api'
import { errorText } from '../format'

export function FieldReportPage() {
  return (
    <>
      <ChatReportUpload />
      <LogReportSwitch />
      <AutoUploadSwitch />
      <ManualUpload />
    </>
  )
}

// 聊天记录上报的人工即时触发(AGENTS.md「全局约定·聊天记录上报」,2026-08-20
// 甲方裁决增补)。与每晚 00:20 那轮完全同款:同一游标水位、同一幂等语义,
// 传完当晚那轮自然没剩多少。巡检跑着也能点——上报对业务账本只读,唯一的写
// 是自己的水位表;正在上传时(含定时那轮)脑侧直接拒绝,这里如实提示。
function ChatReportUpload() {
  const [result, setResult] = useState<ChatReportRunResult | null>(null)
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
      setResult(await api.devChatReportRun())
    } catch (reason) {
      setTransportError(errorText(reason))
    } finally {
      setElapsedMs(Math.round(performance.now() - startedAt))
      setRunning(false)
    }
  }, [running])

  return (
    <div className="panel">
      <h3>聊天记录上报（每晚 00:20 自动增量）</h3>
      <p>
        候选人档案与聊天记录每晚自动增量上传，在管理后台「聊天记录」页查看。
        不想等到夜里就点这里，立即把新增的部分传上去；巡检跑着也可以点。
      </p>
      <div className="sql-bar">
        <button onClick={() => void run()} disabled={running}>
          {running ? '上传中…' : '立即上传'}
        </button>
        {elapsedMs !== null && !running && <small className="mono">{elapsedMs} ms</small>}
      </div>
      <ChatReportOutcome result={result} transportError={transportError} />
    </div>
  )
}

function ChatReportOutcome({ result, transportError }: {
  result: ChatReportRunResult | null
  transportError: string | null
}) {
  if (transportError) return <p className="sql-error">连不上脑：{transportError}</p>
  if (!result) return null
  if (result.error) {
    const partial = result.messages > 0 || result.profiles > 0
      ? `（中断前已传：档案 ${result.profiles} 人、消息 ${result.messages} 条，再点一次续传剩余）`
      : ''
    return <p className="sql-error">上传失败：{result.error}{partial}</p>
  }
  if (result.messages === 0) {
    return <p className="sql-ok">已同步：档案 {result.profiles} 人已刷新，没有新增消息要传。</p>
  }
  return (
    <p className="sql-ok">
      已上传：档案 {result.profiles} 人 · 新增消息 {result.messages} 条
    </p>
  )
}

// 日志上报状态(AGENTS.md「全局约定·日志上报」,2026-08-06 甲方裁决)。
//
// **这里没有开关。** 甲方当日修订:上报常开、不设开关,理由是"只是上报日志,
// 不干过分的事"。本面板只回答"到底传没传出去、发了多少、丢了多少" ——
// 上报是后台静默进行的,没有这一栏就无从知道。
function LogReportSwitch() {
  const [settings, setSettings] = useState<LogReportSettings | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setSettings(await api.devLogReportSettings())
      setError(null)
    } catch (reason) {
      setError(errorText(reason))
    }
  }, [])
  useEffect(() => { void load() }, [load])

  return (
    <div className="panel">
      <h3>出事时即时上报日志（常开）</h3>
      <p>
        脑遇到已登记的事件（如产生一条待人工裁决的 suspect）或任何 Error 级日志，
        会在半分钟内把那一行连同定位信息推给我方，管理前台能立刻看到。同类错误
        五分钟内只报一条、之后补一条合并计数，不会刷屏。
      </p>
      <p>
        推送内容里有候选人姓名、职位名和出事那个会话最近二十条聊天记录；不含简历、
        微信号与模型原文。传不出去就丢，不重试，由每日整包上传兜底。
      </p>
      {error && <p className="sql-error">{error}</p>}
      <LogReportStatus settings={settings} />
    </div>
  )
}

function LogReportStatus({ settings }: { settings: LogReportSettings | null }) {
  if (settings === null) return null
  const counts = `累计已发 ${settings.sentCount} 条，丢弃 ${settings.droppedCount} 条`
  if (!settings.lastAt) return <p>{counts}</p>
  const when = new Date(settings.lastAt)
  const text = Number.isNaN(when.getTime()) ? settings.lastAt : when.toLocaleString('zh-CN')
  if (settings.lastOk) {
    return <p className="sql-ok">上次上报：{text} 成功。{counts}</p>
  }
  return (
    <p className="sql-error">
      上次上报：{text} 未成功{settings.lastError ? ` —— ${settings.lastError}` : ''}。{counts}
    </p>
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
      <div className="dc-switch-row">
        <button
          aria-checked={enabled}
          aria-label="自动上传数据至服务器"
          className={`dc-switch${enabled ? ' is-on' : ''}`}
          disabled={saving || settings === null}
          onClick={() => void toggle(!enabled)}
          role="switch"
          type="button"
        />
        <span>自动上传数据至服务器</span>
      </div>
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
