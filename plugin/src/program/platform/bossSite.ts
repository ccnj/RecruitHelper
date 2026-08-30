// BOSS 直聘的**站点身份**:域名、URL 形状、以及"这个站点感不感知登录态"。
//
// 与 zhilianSite.ts 同一分工理由:content script 与 service worker 是两个 bundle、
// 两个 realm,站点身份必须是两边都付得起的小模块——只依赖契约里的两个枚举,
// 不碰 chrome.* 也不碰任何注入路径。
//
// # 这个站点不感知登录态,而那是一条裁决,不是漏做
//
// BOSS **掉登录后的形态从未观测过**(要真退一次登录才看得清:`user$` 变 null?
// 整页跳登录页?弹登录框?)。按「平台枚举面事实门」,未见值不得实现;所以
// `readLoginState` 只能恒返回 unknown ——**永远不报 out**。
//
// 2026-08-30 甲方裁决:BOSS 上不需要掉登录即时停机通道。于是这里把它记成一条
// 站点事实(`sensesLoginState: false`),而不是一个"以后要补"的空实现。
// 直接后果见 base/content.ts:全文档 MutationObserver 不再装。
import { LoginState, PageKind } from '../../base/protocol'
import type { PlatformSite } from './sites'

export const BOSS_PLATFORM = 'boss'
export const BOSS_HOST = 'www.zhipin.com'
export const BOSS_ORIGIN = `https://${BOSS_HOST}`
export const BOSS_MATCH = `${BOSS_ORIGIN}/*`
export const BOSS_CHAT_URL = `${BOSS_ORIGIN}/web/chat/index`

function matchesBoss(value: string | undefined): boolean {
  if (!value) return false
  try {
    const url = new URL(value)
    return url.protocol === 'https:' && url.hostname === BOSS_HOST
  } catch {
    return false
  }
}

// 只认真机见过的一种页:`/web/chat/index` 是沟通页。
// 推荐页等其余形态**尚未真机确认路径**,一律 other——认不出就不猜。
function bossPageKind(value: string): PageKind {
  try {
    const path = new URL(value).pathname
    if (path === '/web/chat/index' || path.startsWith('/web/chat/index/')) return PageKind.Im
  } catch {
    return PageKind.Other
  }
  return PageKind.Other
}

// 恒 unknown。见文件头:掉登录形态未观测,不得凭 DOM 上"有退出登录入口"反推已登录
// ——那是拿一个未验证的判据去顶一个安全位。
function readBossLoginState(): LoginState {
  return LoginState.Unknown
}

export const bossSite: PlatformSite = {
  id: BOSS_PLATFORM,
  origin: BOSS_ORIGIN,
  match: BOSS_MATCH,
  matches: matchesBoss,
  pageKind: bossPageKind,
  readLoginState: readBossLoginState,
  sensesLoginState: false,
}
