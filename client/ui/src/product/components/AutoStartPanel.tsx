import { useEffect, useState } from 'react'
import { readAutoStartSetting, saveAutoStartSetting } from '../api'
import type { AutoStartSetting } from '../api'

// 脑侧封闭结果码 → 用户可读标签。detail 里已是中文原因,这里只翻码。
const OUTCOME_LABELS: Record<string, string> = {
  started: '已自动开始',
  resumed: '已自动继续昨日任务',
  startFailed: '开始失败',
  resumeFailed: '继续失败',
  skippedActiveRun: '已跳过',
  skippedAlreadyRanToday: '已跳过',
  error: '检查失败',
}

function formatAttemptAt(iso: string): string {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) return ''
  const month = at.getMonth() + 1
  const day = at.getDate()
  const hh = String(at.getHours()).padStart(2, '0')
  const mm = String(at.getMinutes()).padStart(2, '0')
  return `${month}月${day}日 ${hh}:${mm}`
}

type SaveState =
  | { kind: 'idle' }
  | { kind: 'saving' }
  | { kind: 'saved' }
  | { kind: 'error'; message: string }

export function AutoStartPanel() {
  const [setting, setSetting] = useState<AutoStartSetting | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [saveState, setSaveState] = useState<SaveState>({ kind: 'idle' })

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await readAutoStartSetting()
        if (cancelled) return
        setSetting(response)
        setLoadError(null)
      } catch (error) {
        if (cancelled) return
        // 读不出来就不给开关 —— 显示一个猜出来的状态,用户会以为那就是生效的配置。
        setLoadError(error instanceof Error ? error.message : '每日自动开始配置读取失败')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  async function toggle(enabled: boolean) {
    if (!setting) return
    const previous = setting
    setSetting({ ...setting, enabled })
    setSaveState({ kind: 'saving' })
    try {
      await saveAutoStartSetting(enabled)
      setSaveState({ kind: 'saved' })
    } catch (error) {
      // 保存失败必须把界面退回落库前的样子,否则界面显示已开、库里还是关。
      setSetting(previous)
      setSaveState({
        kind: 'error',
        message: error instanceof Error ? error.message : '保存失败',
      })
    }
  }

  const outcomeLabel = setting?.lastOutcome ? (OUTCOME_LABELS[setting.lastOutcome] ?? setting.lastOutcome) : ''
  const attemptAt = setting?.lastAttemptAt ? formatAttemptAt(setting.lastAttemptAt) : ''

  return (
    <section className="rh-panel rh-autostart-panel">
      <div className="rh-panel-heading">
        <div>
          <span className="rh-section-label">自动运行</span>
          <h2>每日自动开始</h2>
        </div>
      </div>
      {loadError ? (
        <div className="rh-schedule-load-error">
          <strong>读取失败,暂时不能修改</strong>
          <p>{loadError}</p>
        </div>
      ) : !setting ? (
        <div className="rh-schedule-loading">读取中…</div>
      ) : (
        <>
          <label className="rh-autostart-toggle">
            <input
              type="checkbox"
              checked={setting.enabled}
              disabled={saveState.kind === 'saving'}
              onChange={(event) => void toggle(event.target.checked)}
            />
            <span>每天早上 07:05～07:30 之间(随机时刻)自动开始全流程</span>
          </label>
          <p className="rh-autostart-hint">
            需要电脑与 Chrome 保持开启并已登录智联。每天只自动尝试一次:
            当天已运行过、或到点时客户端没在运行,则跳过;失败当天不重试,
            随时可以手动开始。
          </p>
          {saveState.kind === 'error' ? (
            <p className="rh-autostart-error">{saveState.message}</p>
          ) : null}
          {attemptAt && outcomeLabel ? (
            <p className="rh-autostart-last">
              上次:{attemptAt} · {outcomeLabel}
              {setting.lastDetail ? `(${setting.lastDetail})` : ''}
            </p>
          ) : null}
        </>
      )}
    </section>
  )
}
