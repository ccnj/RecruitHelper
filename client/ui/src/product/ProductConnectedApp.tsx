import { useCallback, useEffect, useState } from 'react'
import { readCandidateDetail, readProductData } from './api'
import { ProductApp } from './ProductApp'
import { createEmptyProductData } from './fixtures'
import type { ProductData } from './types'

export interface ProductConnectedAppProps {
  onOpenDiagnostics?: () => void
  pollIntervalMs?: number
}

export function ProductConnectedApp({
  onOpenDiagnostics,
  pollIntervalMs = 5_000,
}: ProductConnectedAppProps) {
  const [data, setData] = useState<ProductData>(() => createEmptyProductData())
  const [readState, setReadState] = useState<'loading' | 'ready' | 'stale'>('loading')
  const [readError, setReadError] = useState<string | null>(null)
  const [refreshRevision, setRefreshRevision] = useState(0)

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

  const statusMessage = readState === 'loading'
    ? '正在读取本机业务数据…'
    : readState === 'stale'
      ? `本机业务数据暂时无法刷新，页面保留上次成功结果：${readError ?? '读取失败'}`
      : null

  return (
    <ProductApp
      actions={{
        loadCandidateDetail: (profileId, fallback) => readCandidateDetail(profileId, fallback),
        openDiagnostics: onOpenDiagnostics,
        refresh,
      }}
      data={data}
      statusMessage={statusMessage}
    />
  )
}

function errorText(reason: unknown): string {
  return reason instanceof Error ? reason.message : '读取失败'
}
