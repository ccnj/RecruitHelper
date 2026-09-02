import { useCallback, useEffect, useRef, useState } from 'react'
import { shouldShowActivation } from './activation'
import {
  endProductWorkflow,
  pauseProductWorkflow,
  readCandidateDetail,
  readDailyPlan,
  readProductData,
  readProductUpdateStatus,
  type DailyPlanView,
  type ProductUpdateStatus,
  PlatformChoiceRequired,
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
  const [dailyPlan, setDailyPlan] = useState<DailyPlanView | null>(null)
  // 多平台歧义(2026-09-02 批 D 2.1):脑报歧义后才出现候选列表;记住的只是用户自己选的
  // 平台 id 字符串,不是 /app 响应,且只作为下次歧义时的默认选中项,不自动发送——
  // 免得记住的平台登出后把另一个已登录平台的开始也拦下。
  const [platformOptions, setPlatformOptions] = useState<string[] | null>(null)
  const [selectedPlatform, setSelectedPlatform] = useState<string | null>(() => readRememberedPlatform())
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

  // 当日职位计划:随业务数据同刷,读不到只保留上次结果(投影只服务留痕展示)。
  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const next = await readDailyPlan()
        if (!cancelled) setDailyPlan(next)
      } catch {
        // 忽略:计划面板缺一拍不影响业务操作。
      }
    }
    void load()
    const timer = window.setInterval(load, 15_000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [refreshRevision])

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
      setPlatformOptions(null)
      refresh()
    } catch (reason) {
      if (reason instanceof PlatformChoiceRequired) {
        setPlatformOptions(reason.platforms)
        setActionMessage(`${label}未能开始：${reason.message}，请在下方选择平台后再点一次`)
        return
      }
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
          mode === 'full' ? '今日任务' : '只处理消息',
          // 只有选择控件在场(脑刚报过歧义)时才带 platform;单平台客户请求体不变。
          () => startProductWorkflow(mode, platformOptions ? selectedPlatform ?? undefined : undefined),
        ),
        copyWechat: async (wechatAccount) => {
          await navigator.clipboard.writeText(wechatAccount)
        },
      }}
      dailyPlan={dailyPlan}
      data={data}
      platformChoice={platformOptions ? {
        options: platformOptions,
        selected: selectedPlatform && platformOptions.includes(selectedPlatform) ? selectedPlatform : null,
        onSelect: (platform) => {
          setSelectedPlatform(platform)
          rememberPlatform(platform)
        },
      } : null}
      statusMessage={statusMessage}
      updateStatus={updateStatus}
    />
  )
}

const PLATFORM_STORAGE_KEY = 'recruithelper.product.platform'

// 隐私模式下连读 localStorage 都会抛,try 裹住访问本身;拿不到就当没记过。
// 这里存的只是用户自己选的平台 id,不是任何 /app 响应(同机产品 UI 业务投影例外)。
function readRememberedPlatform(): string | null {
  try {
    if (typeof localStorage === 'undefined') return null
    const value = localStorage.getItem(PLATFORM_STORAGE_KEY)
    return value && value.length <= 64 ? value : null
  } catch {
    return null
  }
}

function rememberPlatform(platform: string): void {
  try {
    if (typeof localStorage === 'undefined') return
    localStorage.setItem(PLATFORM_STORAGE_KEY, platform)
  } catch {
    // 存不上只是下次要再选一次,不是失效。
  }
}

function errorText(reason: unknown): string {
  if (!(reason instanceof Error)) return '读取失败'
  if (reason.message === 'Failed to fetch') return '本机服务未连接'
  return reason.message
}
