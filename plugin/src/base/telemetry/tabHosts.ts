// 标签页顶层主机表(base)。
//
// 回答一个问题:「这个 tabId 现在开着哪个站?」请求录制的第三条判据要它:平台页里
// 第三方 iframe 发的请求,initiator 是第三方 origin,只有看标签页才知道它发生在平台页上。
//
// 与 tabGeneration.ts 同源(都听主框架 commit)但语义相反:那边的状态该随 SW 重启失效,
// 这边的状态重启后要**立刻补齐**——录制可能正跨着 SW 的一次死活,补不齐就漏记。
// 所以注册时用 tabs.query 把当前全部标签页喂一遍。监听只在 base(禁令 5)。
import { hostOf } from './netCapture'

const hosts = new Map<number, string>()

export function tabHost(tabId: number): string | null {
  return hosts.get(tabId) ?? null
}

/** 解不出主机(about:blank、无 url)就当没有:宁可少记。 */
export function noteTabUrl(tabId: number, url: string | undefined): void {
  const host = hostOf(url)
  if (host === null) hosts.delete(tabId)
  else hosts.set(tabId, host)
}

export function forgetTabHost(tabId: number): void {
  hosts.delete(tabId)
}

/** 测试专用。 */
export function resetTabHostsForTest(): void {
  hosts.clear()
}

export function registerTabHostTracking(): void {
  chrome.webNavigation.onCommitted.addListener((details) => {
    if (details.frameId !== 0) return
    noteTabUrl(details.tabId, details.url)
  })
  chrome.tabs.onRemoved.addListener((tabId) => forgetTabHost(tabId))
  chrome.tabs.query({})
    .then((tabs) => {
      for (const tab of tabs) if (tab.id !== undefined) noteTabUrl(tab.id, tab.url)
    })
    .catch((error: unknown) => {
      console.warn('[hand] 标签页主机表初始化失败', error)
    })
}
