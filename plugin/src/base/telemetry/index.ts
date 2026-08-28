// 平台埋点观测的监听注册。**监听注册只在 base**(手的禁令第 5 条)。
//
// 通道是 `chrome.webRequest` 的观察型监听器,**不碰页面 JS 环境**:观察发生在
// 浏览器进程的网络栈里,与页面所在的渲染器隔着进程边界;MV3 的观察型监听器
// 不挂起请求,这里只抄一份、请求原样继续。
//
// 上游选这条通道的理由不是"怕钩子被识破"——BOSS 并不查 fetch/XHR 有没有被换;
// 是因为 `risk-detection` 会把 `Object.keys(window)` 与一份白名单求差、
// **未知全局名原样上送**。在 MAIN world 建任何全局名都会被这条通道带出去,
// 而 webRequest 一个名字都不建。
//
// 已知代价(甲方 2026-08-28 知情接受):持有 `webRequest` 权限会让本 profile
// 全部请求多走一次代理(`WebRequestAPI::MayHaveProxies()` 在创建
// URLLoaderFactory 时就决定,那时还没有请求 URL,所以 urls 过滤器只决定
// "哪些请求派发给我们",不决定"哪些请求走代理")。单条亚毫秒,埋在几十到
// 几百毫秒的网络方差里提取不出来;而请求耗时本就不是可用的检测判据——
// 拿它判人会误伤每个网慢的、开 VPN 的、走公司代理的用户。

import { TelemetryStorage } from './store'
import { bodyText, recordUpload } from './capture'
import { telemetrySites } from '../../program/platform/telemetrySites'

const storage: TelemetryStorage = {
  get: (keys) => chrome.storage.local.get(keys as string | string[]),
  set: (items) => chrome.storage.local.set(items),
  remove: (keys) => chrome.storage.local.remove(keys as string | string[]),
}

// 串行化读改写。SW 是单线程,但多个标签页的请求会交错到达,
// 而落盘是"读当前片 → 追加 → 写回"的读改写序列。
let queue: Promise<void> = Promise.resolve()

function serialize(work: () => Promise<void>): void {
  queue = queue.then(work).catch((error: unknown) => {
    console.error('[hand] 平台埋点观测落盘失败', error)
  })
}

/** 给每个观测站点挂一个观察型监听器。缺权限时安静跳过——观测不该拦住插件上线。 */
export function registerTelemetryCapture(): void {
  if (typeof chrome.webRequest?.onBeforeRequest?.addListener !== 'function') {
    console.warn('[hand] 无 webRequest 权限,平台埋点观测未启用')
    return
  }

  for (const site of telemetrySites) {
    chrome.webRequest.onBeforeRequest.addListener(
      (details) => {
        const text = bodyText(details.requestBody)
        if (text === null) return
        const at = Date.now()
        serialize(() => recordUpload(storage, (p, t) => site.digest(p, t), at, details.url, text))
        // 观察型监听器:不返回任何东西,请求原样继续。
      },
      { urls: [...site.urls] },
      ['requestBody'],
    )
  }
}
