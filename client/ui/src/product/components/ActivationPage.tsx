import { type FormEvent, useEffect, useMemo, useState } from 'react'
import {
  api,
  type JobConfigActivationResult,
  type JobConfigSourceView,
} from '../../api'
import { activationInputError, buildActivationInput } from '../activation'

export interface ActivationPageProps {
  onActivated: () => Promise<void>
}

type Notice = {
  tone: 'success' | 'danger'
  text: string
}

export function ActivationPage({ onActivated }: ActivationPageProps) {
  const [source, setSource] = useState<JobConfigSourceView | null>(null)
  const [sourceLoading, setSourceLoading] = useState(true)
  const [sourceError, setSourceError] = useState<string | null>(null)
  const [baseURL, setBaseURL] = useState('')
  const [inviteCode, setInviteCode] = useState('')
  const [attempted, setAttempted] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [activationComplete, setActivationComplete] = useState(false)
  const [notice, setNotice] = useState<Notice | null>(null)

  async function readSource() {
    setSourceLoading(true)
    setSourceError(null)
    try {
      const result = await api.jobConfigSource()
      setSource(result.config)
    } catch (reason) {
      setSource(null)
      setSourceError(errorText(reason))
    } finally {
      setSourceLoading(false)
    }
  }

  useEffect(() => {
    void readSource()
  }, [])

  const validationError = useMemo(
    () => activationInputError(source, baseURL, inviteCode),
    [baseURL, inviteCode, source],
  )

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setAttempted(true)
    if (validationError || submitting || activationComplete) return

    setSubmitting(true)
    setNotice(null)
    let confirmed = false
    try {
      const result = await api.activateJobConfigSource(buildActivationInput(baseURL, inviteCode))
      if (!result.activated) throw new Error('激活结果未确认，请重新读取授权状态。')

      confirmed = true
      // 激活码只存在于这次表单的内存状态；一旦后台确认即立即清空。
      setInviteCode('')
      setBaseURL('')
      setActivationComplete(true)
      setNotice(activationNotice(result))
      await onActivated()
    } catch (reason) {
      setNotice({
        tone: 'danger',
        text: confirmed
          ? `激活已成功，但产品数据暂时无法刷新：${errorText(reason)}`
          : errorText(reason),
      })
    } finally {
      setSubmitting(false)
    }
  }

  const baseURLConfigured = source?.baseUrlConfigured === true
  const sourceStatus = sourceLoading
    ? '正在读取本机授权配置…'
    : sourceError
      ? '本机授权配置暂时无法读取'
      : baseURLConfigured
        ? '已找到本机保存的后台地址'
        : '这台电脑尚未配置后台地址'

  return (
    <main className="rh-activation-page">
      <div className="rh-activation-shell">
        <section className="rh-activation-context" aria-label="激活说明">
          <div className="rh-activation-brand">
            <span className="rh-activation-brand-mark" aria-hidden="true">R</span>
            <span>招聘自动化助手</span>
          </div>
          <div className="rh-activation-intro">
            <span className="rh-activation-eyebrow">首次使用</span>
            <h1>激活这台电脑</h1>
            <p>连接管理员提供的后台后，客户端会绑定当前客户并同步正在使用的职位配置。</p>
          </div>
          <ol className="rh-activation-steps">
            <li className="is-current">
              <span>1</span>
              <div><strong>连接后台</strong><small>确认配置来源</small></div>
            </li>
            <li>
              <span>2</span>
              <div><strong>绑定设备</strong><small>使用一次性激活码</small></div>
            </li>
            <li>
              <span>3</span>
              <div><strong>同步职位</strong><small>自动进入工作台</small></div>
            </li>
          </ol>
          <p className="rh-activation-local-note">
            激活信息只由这台电脑上的客户端处理，Chrome 插件不会接触激活码。
          </p>
        </section>

        <section className="rh-activation-form-panel">
          <header>
            <span className="rh-activation-status-dot" aria-hidden="true" />
            <div>
              <h2>输入激活信息</h2>
              <p>{sourceStatus}</p>
            </div>
          </header>

          {sourceError && (
            <div className="rh-activation-alert is-warning">
              <span>读取失败：{sourceError}</span>
              <button onClick={() => void readSource()} type="button">重新读取</button>
            </div>
          )}

          <form autoComplete="off" noValidate onSubmit={(event) => void submit(event)}>
            <label className="rh-activation-field">
              <span>
                后台地址
                {baseURLConfigured && <small>已保存，可留空</small>}
              </span>
              <input
                autoCapitalize="none"
                disabled={submitting || activationComplete}
                inputMode="url"
                onChange={(event) => setBaseURL(event.target.value)}
                placeholder={baseURLConfigured ? '留空沿用本机已保存地址' : 'https://example.com'}
                spellCheck={false}
                type="url"
                value={baseURL}
              />
              <small>
                {baseURLConfigured
                  ? '留空会沿用原地址；只有切换到另一套后台时才需要重新填写。'
                  : '请输入管理员提供的完整地址，包含 http:// 或 https://。'}
              </small>
            </label>

            <label className="rh-activation-field">
              <span>激活码</span>
              <input
                autoCapitalize="none"
                autoComplete="one-time-code"
                disabled={submitting || activationComplete}
                onChange={(event) => setInviteCode(event.target.value)}
                placeholder="请输入管理员提供的激活码"
                spellCheck={false}
                type="text"
                value={inviteCode}
              />
              <small>激活码仅用于本次绑定，客户端不会保存它。</small>
            </label>

            {attempted && validationError && (
              <p className="rh-activation-validation" role="alert">{validationError}</p>
            )}
            {notice && (
              <p
                className={`rh-activation-result is-${notice.tone}`}
                role={notice.tone === 'danger' ? 'alert' : 'status'}
              >
                {notice.text}
              </p>
            )}

            <button
              className="rh-activation-submit"
              disabled={submitting || activationComplete || sourceLoading}
              type="submit"
            >
              {activationComplete
                ? '正在进入工作台'
                : submitting
                  ? '正在激活…'
                  : '激活并同步职位'}
            </button>
          </form>

          <footer>
            <span>需要新的激活码时，请联系管理员。</span>
            <span>客户端版本 {__APP_VERSION__}</span>
          </footer>
        </section>
      </div>
    </main>
  )
}

function activationNotice(result: JobConfigActivationResult): Notice {
  if (result.synced) {
    return { tone: 'success', text: '激活成功，职位配置已同步，正在进入工作台。' }
  }
  return {
    tone: 'success',
    text: `激活成功，但职位配置尚未同步：${result.syncError || '稍后可在配置页重试同步。'}`,
  }
}

function errorText(reason: unknown): string {
  return reason instanceof Error ? reason.message : '激活请求失败'
}
