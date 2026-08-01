import { useState } from 'react'
import { installProductUpdate, type ProductUpdateStatus } from '../api'

interface UpdateBannerProps {
  status: ProductUpdateStatus | null
  /** 有任务正在跑。装之前必须先结束它，所以确认文案要说清后果。 */
  workflowActive: boolean
}

/**
 * 侧边栏版本行下面那条"有新版"提示。
 *
 * 只在包已经下好并校验通过（ready）时才出现按钮：还在下载的时候给按钮，用户点了
 * 只会得到一句"没有已备好的安装包"，不如不给。
 */
export function UpdateBanner({ status, workflowActive }: UpdateBannerProps) {
  const [confirming, setConfirming] = useState(false)
  const [installing, setInstalling] = useState(false)
  const [error, setError] = useState('')

  if (!status?.available || !status.version) return null

  if (!status.ready) {
    // 下载中。说清楚在做什么，否则用户只看到版本号没变，会以为坏了。
    return (
      <div className="rh-update-banner">
        <span className="rh-update-title">发现新版 v{status.version}</span>
        <span className="rh-update-hint">正在下载，完成后可一键更新</span>
      </div>
    )
  }

  const start = async () => {
    setInstalling(true)
    setError('')
    try {
      await installProductUpdate()
      // 成功之后客户端马上退出并被覆盖，这里不必再收尾。
    } catch (err) {
      setInstalling(false)
      setConfirming(false)
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <div className="rh-update-banner">
      <span className="rh-update-title">新版 v{status.version} 已就绪</span>
      {status.notes ? <span className="rh-update-hint">{status.notes}</span> : null}
      {error ? <span className="rh-update-error">{error}</span> : null}

      {confirming ? (
        <div className="rh-update-confirm">
          <p>
            更新会关闭并重新启动客户端，大约需要一分钟。
            {workflowActive
              ? '当前有任务正在运行，更新前会先结束它——已采集的候选人和已发出的消息都会保留，但本批剩余进度会丢失。'
              : '当前没有任务在运行。'}
          </p>
          {installing ? (
            // 结束任务不是一瞬间的事：脑要等当前候选人把已经开始的动作跑完，
            // 才敢关掉进程，否则一条正在发送的消息会变成需要人工判定的悬案。
            // 这段等待最长几分钟，界面上不说一声就会被当成卡死。
            <p className="rh-update-waiting">
              正在等待当前任务收尾，请稍候，不要关闭窗口。收尾完成后客户端会自动重启。
            </p>
          ) : (
            <p className="rh-update-confirm-hint">更新期间请不要关机。</p>
          )}
          <div className="rh-update-actions">
            <button
              className="rh-update-primary"
              disabled={installing}
              onClick={() => void start()}
              type="button"
            >
              {installing ? '正在收尾…' : '确认更新'}
            </button>
            <button
              disabled={installing}
              onClick={() => setConfirming(false)}
              type="button"
            >
              取消
            </button>
          </div>
        </div>
      ) : (
        <button
          className="rh-update-primary"
          onClick={() => { setError(''); setConfirming(true) }}
          type="button"
        >
          立即更新
        </button>
      )}
    </div>
  )
}
