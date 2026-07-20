// 插件自重载的最小维护接缝。命令仍走正式 Dispatcher；这里仅保存一次性
// 基础设施 marker，并把真正的 runtime.reload 延迟到脑已 ACK result 之后。
import { ZHILIAN_CONTENT_MATCH } from './contentMessages'

const RELOAD_MARKER_KEY = 'infraReloadRequest'

interface ReloadMarker {
  ref: string
  requestedAt: number
}

let armedRef: string | null = null

export async function armRuntimeReload(ref: string): Promise<void> {
  if (!ref) throw new Error('重载命令缺少 ref')
  const marker: ReloadMarker = { ref, requestedAt: Date.now() }
  await chrome.storage.local.set({ [RELOAD_MARKER_KEY]: marker })
  armedRef = ref
}

// 只接受本 SW 亲自 armed 的同一命令；duplicate ACK 也只能触发一次。
export function acknowledgeRuntimeReloadResult(ref: string): boolean {
  if (armedRef !== ref) return false
  armedRef = null
  chrome.runtime.reload()
  return true
}

// 新 SW 在建立脑手连接前消费 marker，并刷新当前已打开的平台页，让新的
// content script 随页面导航重新注入。当前有人值守阶段显式不检查聊天草稿。
export async function refreshPagesAfterRuntimeReload(): Promise<number> {
  const stored = await chrome.storage.local.get(RELOAD_MARKER_KEY)
  const marker = parseMarker(stored[RELOAD_MARKER_KEY])
  if (!marker) return 0

  // 先消费再刷新，保证一次 marker 至多触发一轮页面重载；个别标签页在查询
  // 后关闭属于可恢复瞬态，不重新武装 marker。
  await chrome.storage.local.remove(RELOAD_MARKER_KEY)
  const tabs = await chrome.tabs.query({ url: ZHILIAN_CONTENT_MATCH })
  let refreshed = 0
  for (const tab of tabs) {
    if (tab.id === undefined) continue
    try {
      await chrome.tabs.reload(tab.id)
      refreshed += 1
    } catch {
      // 标签页可能在 query 与 reload 之间关闭；其页面已经不再需要换代。
    }
  }
  return refreshed
}

function parseMarker(value: unknown): ReloadMarker | null {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null
  const candidate = value as Partial<ReloadMarker>
  return typeof candidate.ref === 'string' && candidate.ref.length > 0 &&
    typeof candidate.requestedAt === 'number' && Number.isFinite(candidate.requestedAt)
    ? { ref: candidate.ref, requestedAt: candidate.requestedAt }
    : null
}
