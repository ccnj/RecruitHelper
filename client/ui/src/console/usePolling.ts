import { useEffect, useRef, useState } from 'react'
import { errorText } from './format'

export interface PollState<T> {
  data: T | undefined
  error: string | null
  loading: boolean
  refresh: () => void
}

export function usePolling<T>(load: () => Promise<T>, intervalMs: number, key: string): PollState<T> {
  const loader = useRef(load)
  const previousKey = useRef(key)
  loader.current = load
  const [data, setData] = useState<T>()
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    let alive = true
    let running = false
    if (previousKey.current !== key) {
      previousKey.current = key
      setData(undefined)
    }
    setLoading(true)
    const run = async () => {
      if (running) return
      running = true
      try {
        const next = await loader.current()
        if (alive) {
          setData(next)
          setError(null)
        }
      } catch (reason) {
        if (alive) setError(errorText(reason))
      } finally {
        running = false
        if (alive) setLoading(false)
      }
    }
    void run()
    const timer = window.setInterval(run, intervalMs)
    return () => {
      alive = false
      window.clearInterval(timer)
    }
  }, [intervalMs, key, revision])

  return { data, error, loading, refresh: () => setRevision((value) => value + 1) }
}
