import { useEffect, useState } from 'react'
import { ConsoleApp } from './console/ConsoleApp'
import { ProductConnectedApp } from './product'

export function App() {
  const [diagnosticsVisible, setDiagnosticsVisible] = useState(false)
  useEffect(() => {
    const toggleDiagnostics = (event: KeyboardEvent) => {
      if (
        event.shiftKey
        && (event.metaKey || event.ctrlKey)
        && event.key.toLowerCase() === 'd'
      ) {
        event.preventDefault()
        setDiagnosticsVisible((visible) => !visible)
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
    return (
      <>
        <button className="diagnostics-return" onClick={() => setDiagnosticsVisible(false)}>
          返回客户端
        </button>
        <ConsoleApp />
      </>
    )
  }
  return <ProductConnectedApp />
}
