// 站点身份登记表:「这个 URL 是哪个平台的页面、那是什么页、登录了没有」。
//
// 与 registry.ts 的分工(**两张表,不是一张表的两半**):
//
//   registry.ts  35 条能力,只在 service worker 里。加平台 = background.ts 多一行。
//   sites.ts     站点身份,**content script 与 SW 两个 bundle 共用**。
//
// 之所以拆成两张,是因为两个 bundle 是两个 realm:content script 付不起适配器
// 那 750KB 业务层的重量(理由详见 zhilianSite.ts 开头)。
//
// **这里刻意用静态 import 而不是 registerSite() 回调**。registry.ts 那张表由
// background.ts 在启动时填,是因为 SW 有组合根;而站点集合在编译期就已经被
// manifest.json 的 content_scripts.matches 钉死了——运行期再灵活也没用,页面上
// 根本不会有那个 content script。静态列表是对这件事的如实建模。
//
// 加一个平台要动两处:本文件的 BUILT_IN,和 manifest.json 的 matches/host_permissions。
// **漏掉后者不会报错,只会让 content script 永远不注入**(没有任何症状),
// 所以有一条构建期用例专门核对两者一致,见 test/unit.mjs「站点登记表与 manifest」。
import type { LoginState, PageKind } from '../../base/protocol'
import { zhilianSite } from './zhilianSite'

export interface PlatformSite {
  /** 与脑下发的 `cmd.context.platform`、适配器的 `id` 逐字相等。 */
  readonly id: string
  /** `https://host` 形态,无尾斜杠。 */
  readonly origin: string
  /** chrome.tabs.query / manifest matches 口径的匹配式。 */
  readonly match: string
  /** 这个 URL 是不是本平台的页面。判据只能用 URL 的公开部分。 */
  matches(url: string | undefined): boolean
  /** URL → 页面种类。认不出一律 other,不猜。 */
  pageKind(url: string): PageKind
  /**
   * 在页面里读登录态。**只允许标准 DOM**——它在 content script 的 isolated
   * world 里跑,拿不到页面 JS 的运行期对象。
   *
   * 读不出来必须返回 unknown。某个平台若根本无法被动感知登录态,就恒返回
   * unknown:代价是这个平台没有掉登录即时停机通道,只能靠命令失败暴露,
   * 但方向仍是"不确认",不会假报已登录。
   */
  readLoginState(): LoginState
}

const BUILT_IN: readonly PlatformSite[] = [zhilianSite]

let sites: readonly PlatformSite[] = BUILT_IN

export function allSites(): readonly PlatformSite[] {
  return sites
}

/** 测试专用:换掉站点集合,好在只装了一个平台时也能验多平台分区。生产路径永不调用。 */
export function setSitesForTest(next: readonly PlatformSite[]): void {
  sites = next
}

/** 测试专用:恢复内建集合。 */
export function resetSitesForTest(): void {
  sites = BUILT_IN
}

/** URL 属于哪个平台;不属于任何已登记平台返回 null。 */
export function siteForURL(url: string | undefined): PlatformSite | null {
  if (!url) return null
  for (const site of sites) {
    if (site.matches(url)) return site
  }
  return null
}

export function siteById(id: string): PlatformSite | null {
  return sites.find((site) => site.id === id) ?? null
}
