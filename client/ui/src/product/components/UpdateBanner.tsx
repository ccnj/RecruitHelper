import { useEffect, useState } from 'react'
import { installProductUpdate, type ProductUpdateStatus } from '../api'
import { ProductIcon } from './ProductIcon'

interface UpdateBannerProps {
  status: ProductUpdateStatus | null
  /** 有任务正在跑。装之前必须先结束它，所以确认文案要说清后果。 */
  workflowActive: boolean
}

/**
 * 主区顶部那条"有新版"提示，以及点下去之后的确认弹窗。
 *
 * 分成两层是因为这两件事的体量差得远：条上只说"有新版、要不要装"，而"会中断
 * 当前任务、本批进度会丢"这段后果必须让人看清楚再点，塞进一行里没人会读。
 *
 * 只在包已经下好并校验通过（ready）时才给按钮：还在下载的时候给按钮，用户点了
 * 只会得到一句"没有已备好的安装包"，不如不给。
 */
export function UpdateBanner({ status, workflowActive }: UpdateBannerProps) {
  const [confirming, setConfirming] = useState(false)
  const [installing, setInstalling] = useState(false)
  const [error, setError] = useState('')
  const [dismissedKey, setDismissedKey] = useState('')

  const version = status?.version ?? ''
  const ready = status?.ready ?? false
  // 下载中收起过，包备好之后还要再提醒一次——那才是真能装的时刻。
  const noticeKey = `${version}:${ready ? 'ready' : 'downloading'}`

  useEffect(() => {
    // 收尾期间不给退路：这时候关掉弹窗，用户就看不到"正在等任务停下来"了。
    if (!confirming || installing) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setConfirming(false)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [confirming, installing])

  if (!status?.available || !version) return null
  if (dismissedKey === noticeKey && !confirming) return null

  const start = async () => {
    setInstalling(true)
    setError('')
    try {
      await installProductUpdate()
      // 成功之后客户端马上退出并被覆盖，这里不必再收尾。
    } catch (err) {
      setInstalling(false)
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <>
      <div className="rh-update-bar">
        <ProductIcon name="refresh" size={17} />
        <span className="rh-update-copy">
          {ready ? (
            <>新版 <strong>v{version}</strong> 已就绪，安装约需一分钟</>
          ) : (
            // 下载中也要说一声，否则用户只看到版本号没变，会以为坏了。
            <>正在下载新版 <strong>v{version}</strong>，完成后可一键更新</>
          )}
        </span>
        {ready && (
          <button
            className="rh-update-install"
            onClick={() => { setError(''); setConfirming(true) }}
            type="button"
          >
            立即更新
          </button>
        )}
        <button
          className="rh-update-later"
          onClick={() => setDismissedKey(noticeKey)}
          type="button"
        >
          稍后
        </button>
      </div>

      {confirming && (
        <>
          <button
            aria-label="关闭更新确认"
            className="rh-update-backdrop"
            disabled={installing}
            onClick={() => setConfirming(false)}
            type="button"
          />
          <div
            aria-labelledby="rh-update-modal-title"
            aria-modal="true"
            className="rh-update-modal"
            role="dialog"
          >
            <h2 id="rh-update-modal-title">更新到 v{version}</h2>
            {status.notes ? <p className="rh-update-notes">{status.notes}</p> : null}
            <p>客户端会关闭并重新启动，大约需要一分钟，更新期间请不要关机。</p>
            {workflowActive ? (
              <p className="rh-update-warning">
                <ProductIcon name="warning" size={15} />
                <span>
                  当前有任务正在运行，更新前会先结束它。已采集的候选人和已发出的消息都会保留，
                  但本批剩余进度会丢失。
                </span>
              </p>
            ) : (
              <p className="rh-update-calm">当前没有任务在运行。</p>
            )}
            {installing && (
              // 结束任务不是一瞬间的事：脑要等当前候选人把已经开始的动作跑完，
              // 才敢关掉进程，否则一条正在发送的消息会变成需要人工判定的悬案。
              // 这段等待最长几分钟，界面上不说一声就会被当成卡死。
              <p className="rh-update-waiting">
                正在等待当前任务收尾，请稍候，不要关闭窗口。收尾完成后客户端会自动重启。
              </p>
            )}
            {error ? <p className="rh-update-error">{error}</p> : null}
            <div className="rh-update-actions">
              <button
                className="rh-update-cancel"
                disabled={installing}
                onClick={() => setConfirming(false)}
                type="button"
              >
                取消
              </button>
              <button
                className="rh-update-install"
                disabled={installing}
                onClick={() => void start()}
                type="button"
              >
                {installing ? '正在收尾…' : '确认更新'}
              </button>
            </div>
          </div>
        </>
      )}
    </>
  )
}
