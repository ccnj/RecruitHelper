import { useCallback, useEffect, useRef, useState } from 'react'
import { shouldShowActivation } from './activation'
import {
  endProductWorkflow,
  pauseProductWorkflow,
  readCandidateDetail,
  readProductData,
  readProductUpdateStatus,
  type ProductUpdateStatus,
  resumeProductWorkflow,
  sendProductConfirmation,
  startProductWorkflow,
  syncProductJobs,
} from './api'
import { ProductApp } from './ProductApp'
import { ActivationPage } from './components/ActivationPage'
import { createEmptyProductData } from './fixtures'
import type { ProductData } from './types'

export interface ProductConnectedAppProps {
  pollIntervalMs?: number
}

export function ProductConnectedApp({
  pollIntervalMs = 5_000,
}: ProductConnectedAppProps) {
  const [data, setData] = useState<ProductData>(() => createEmptyProductData())
  const [readState, setReadState] = useState<'loading' | 'ready' | 'stale'>('loading')
  const [readError, setReadError] = useState<string | null>(null)
  const [actionMessage, setActionMessage] = useState<string | null>(null)
  const [refreshRevision, setRefreshRevision] = useState(0)
  const [updateStatus, setUpdateStatus] = useState<ProductUpdateStatus | null>(null)
  const actionRunning = useRef(false)

  const refresh = useCallback(() => {
    setRefreshRevision((revision) => revision + 1)
  }, [])

  useEffect(() => {
    let cancelled = false
    let requestRunning = false
    const load = async () => {
      if (requestRunning) return
      requestRunning = true
      try {
        const next = await readProductData()
        if (cancelled) return
        setData(next)
        setReadState('ready')
        setReadError(null)
      } catch (reason) {
        if (cancelled) return
        setReadState('stale')
        setReadError(errorText(reason))
      } finally {
        requestRunning = false
      }
    }
    void load()
    const timer = window.setInterval(load, pollIntervalMs)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [pollIntervalMs, refreshRevision])

  // 更新状态与业务数据分开轮询:它变化慢得多,而且读失败绝不该影响业务页面。
  // 一分钟一次足够 —— 脑那边本来就要 15 分钟才去问一次更新源。
  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const next = await readProductUpdateStatus()
        if (!cancelled) setUpdateStatus(next)
      } catch {
        // 读不到就当没有新版,不打扰用户:这是个提示,不是业务事实。
      }
    }
    void load()
    const timer = window.setInterval(load, 60_000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [])

  const readStatusMessage = readState === 'loading'
    ? '正在读取本机业务数据…'
    : readState === 'stale'
      ? `本机业务数据暂时无法刷新，页面保留上次成功结果：${readError ?? '读取失败'}`
      : null
  const statusMessage = actionMessage ?? readStatusMessage

  useEffect(() => {
    if (!actionMessage) return
    const timer = window.setTimeout(() => setActionMessage(null), 8_000)
    return () => window.clearTimeout(timer)
  }, [actionMessage])

  async function performProductAction(label: string, action: () => Promise<void>) {
    if (actionRunning.current) return
    actionRunning.current = true
    setActionMessage(`${label}已提交，正在等待脑确认…`)
    try {
      await action()
      setActionMessage(`${label}已受理，正在刷新业务状态。`)
      refresh()
    } catch (reason) {
      setActionMessage(`${label}未能执行：${errorText(reason)}`)
    } finally {
      actionRunning.current = false
    }
  }

  async function refreshAfterActivation() {
    const next = await readProductData()
    setData(next)
    setReadState('ready')
    setReadError(null)
    setActionMessage(null)
    if (!next.customer.authorized) {
      throw new Error('本机授权状态尚未更新，请稍后重新读取。')
    }
  }

  if (shouldShowActivation(readState, data.customer)) {
    return <ActivationPage onActivated={refreshAfterActivation} />
  }

  return (
    <ProductApp
      actions={{
        loadCandidateDetail: (profileId, fallback) => readCandidateDetail(profileId, fallback),
        endWorkflow: () => performProductAction('结束本次任务', endProductWorkflow),
        pauseWorkflow: () => performProductAction('暂停', pauseProductWorkflow),
        refresh,
        resumeWorkflow: () => performProductAction('恢复', resumeProductWorkflow),
        syncJobs: () => performProductAction('同步职位', syncProductJobs),
        sendConfirmationBatch: (batchId, profileIds) => performProductAction(
          '候选确认发送',
          () => sendProductConfirmation(batchId, profileIds),
        ),
        startWorkflow: (mode) => performProductAction(
          mode === 'full'
            ? (data.overview.workflow.canAddBatch ? '追加采集' : '今日任务')
            : '只处理消息',
          () => startProductWorkflow(
            mode,
            mode === 'full' ? data.customer.job.backendJobId : null,
          ),
        ),
        copyWechat: async (wechatAccount) => {
          await navigator.clipboard.writeText(wechatAccount)
        },
      }}
      data={data}
      statusMessage={statusMessage}
      updateStatus={updateStatus}
    />
  )
}

function errorText(reason: unknown): string {
  if (!(reason instanceof Error)) return '读取失败'
  if (reason.message === 'Failed to fetch') return '本机服务未连接'
  return reason.message
}
