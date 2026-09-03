// 标签页导航代数(base)。
//
// 回答一个问题:「这个标签页自某一时刻以来发生过主框架导航吗?」答案是一个只增的
// 整数——每次主框架 commit 加一,标签页关闭即清。program 层把它与一次读取一起记下,
// 下次比较:代数没变,页面就还是那一页。
//
// 它是账号身份复核缓存的失效依据(协议规格 §12 第 9 条 2026-09-03 增补):BOSS 上
// 换账号必经整页导航(登出即跳登录页,2026-09-03 实测),所以「导航未变」足以沿用
// 上一次核对通过的指纹,不必每条命令都进 MAIN world 读一次。
//
// 监听只在 base(宪法禁令 5)。状态只在内存:后台进程重启即清空,那正是缓存该失效的
// 时刻之一——不持久化不是省事,是语义。
const generations = new Map<number, number>()

/** 当前代数。没见过的标签页是 0:与"刚开始记"等价,program 侧按未变处理即可。 */
export function tabNavigationGeneration(tabId: number): number {
  return generations.get(tabId) ?? 0
}

/** 主框架 commit 一次,代数加一。导出只为单测直接驱动,生产路径经监听调用。 */
export function noteMainFrameNavigation(tabId: number): void {
  generations.set(tabId, tabNavigationGeneration(tabId) + 1)
}

export function forgetTab(tabId: number): void {
  generations.delete(tabId)
}

/** 测试专用。 */
export function resetTabGenerationsForTest(): void {
  generations.clear()
}

/**
 * 注册监听。onCommitted 只认 frameId=0:子框架(广告、内嵌页)的导航与"页面换了"无关。
 * onHistoryStateUpdated 刻意不算——SPA 内部的路由推进不换页面、不换账号,算进去只会让
 * 缓存在智联这类 SPA 上每次切会话都失效,等于没缓存。
 */
export function registerTabGenerationTracking(): void {
  chrome.webNavigation.onCommitted.addListener((details) => {
    if (details.frameId !== 0) return
    noteMainFrameNavigation(details.tabId)
  })
  chrome.tabs.onRemoved.addListener((tabId) => forgetTab(tabId))
}
