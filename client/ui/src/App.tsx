import { useEffect, useState } from 'react'
import { ConsoleApp } from './console/ConsoleApp'
import { ConsoleLock } from './console/ConsoleLock'
import { consoleUnlocked } from './console/lock-state'
import { ProductConnectedApp } from './product'

export function App() {
  const [diagnosticsVisible, setDiagnosticsVisible] = useState(false)
  // 只在进入那一刻查一次存量：闸的作用是拦住"进来"，把已经在里面干活的人
  // 在 04:00 踢出去既不增加防护，也只会打断排查。
  const [unlocked, setUnlocked] = useState(false)

  useEffect(() => {
    const toggleDiagnostics = (event: KeyboardEvent) => {
      if (
        event.shiftKey
        && (event.metaKey || event.ctrlKey)
        && event.key.toLowerCase() === 'd'
      ) {
        event.preventDefault()
        setDiagnosticsVisible((visible) => {
          if (!visible) setUnlocked(consoleUnlocked())
          return !visible
        })
      }
    }
    window.addEventListener('keydown', toggleDiagnostics)
    return () => window.removeEventListener('keydown', toggleDiagnostics)
  }, [])

  // 诊断台是深色仪表舱，产品端是浅色工作台；根节点标记让两套主题
  // 各自成立，退出时必须还原，否则产品页会留在深色背景上。
  useEffect(() => {
    const root = document.documentElement
    if (diagnosticsVisible) root.setAttribute('data-console', 'on')
    else root.removeAttribute('data-console')
    return () => root.removeAttribute('data-console')
  }, [diagnosticsVisible])

  if (diagnosticsVisible) {
    // 锁屏也带"返回客户端"：闸只能挡住进来，不能把人关在里面出不去。
    return (
      <>
        <button className="diagnostics-return" onClick={() => setDiagnosticsVisible(false)}>
          返回客户端
        </button>
        {unlocked ? (
          <ConsoleApp />
        ) : (
          <ConsoleLock
            onUnlocked={() => setUnlocked(true)}
            onCancel={() => setDiagnosticsVisible(false)}
          />
        )}
      </>
    )
  }
  return <ProductConnectedApp />
}
