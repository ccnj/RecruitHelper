import { setWsUrl } from './config'

export interface InfrastructureMessageResponse {
  ok: boolean
  wsUrl?: string
  error?: string
}

// options 不能直接 read-modify-write chrome.storage：首次启动时那会与 handId
// 出生写竞争并覆盖稳定标识。所有配置写统一回到 SW 的 config 模块串行化。
export function handleInfrastructureMessage(
  message: unknown,
  sendResponse: (response: InfrastructureMessageResponse) => void,
): true | undefined {
  if (typeof message !== 'object' || message === null) return undefined
  const candidate = message as { type?: unknown; wsUrl?: unknown }
  if (candidate.type !== 'setWsUrl') return undefined
  if (typeof candidate.wsUrl !== 'string') {
    sendResponse({ ok: false, error: '脑服务地址必须是字符串' })
    return true
  }
  const wsUrl = candidate.wsUrl
  void setWsUrl(wsUrl)
    .then((normalized) => sendResponse({ ok: true, wsUrl: normalized }))
    .catch((error: unknown) => sendResponse({
      ok: false,
      error: error instanceof Error ? error.message : '保存脑服务地址失败',
    }))
  return true
}
