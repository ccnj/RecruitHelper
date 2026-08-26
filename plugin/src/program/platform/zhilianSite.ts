// 智联的**站点身份**:域名、URL 形状、页面里的登录态怎么读。
//
// 为什么单独一个文件,而不是塞进 zhilian.ts:**content script 与 service worker 是
// 两个 bundle、两个 realm**(见 esbuild.mjs)。适配器那 35 条能力只在 SW 里跑,
// zhilian.ts 光源码就 750KB;content script 现在不到 10KB,把它 import 进去等于让
// 每个平台页都背上整个业务层。所以站点身份必须是一个**两个 bundle 都付得起**的
// 小模块:这里只依赖契约里的两个枚举,不碰 chrome.* 也不碰任何注入路径。
//
// 依赖方向刻意单向:zhilian.ts(SW 侧)从这里取域名,反过来不成立。这样
// 「智联的域名是什么」全仓库只有一处。
import { LoginState, PageKind } from '../../base/protocol'
import type { PlatformSite } from './sites'

export const ZHILIAN_PLATFORM = 'zhilian'
export const ZHILIAN_HOST = 'rd6.zhaopin.com'
export const ZHILIAN_ORIGIN = `https://${ZHILIAN_HOST}`
export const ZHILIAN_MATCH = `${ZHILIAN_ORIGIN}/*`
export const ZHILIAN_IM_URL = `${ZHILIAN_ORIGIN}/app/im`
export const ZHILIAN_RECOMMEND_URL = `${ZHILIAN_ORIGIN}/app/recommend`

function matchesZhilian(value: string | undefined): boolean {
  if (!value) return false
  try {
    const url = new URL(value)
    return url.protocol === 'https:' && url.hostname === ZHILIAN_HOST
  } catch {
    return false
  }
}

function zhilianPageKind(value: string): PageKind {
  try {
    const path = new URL(value).pathname
    if (path === '/app/im' || path.startsWith('/app/im/')) return PageKind.Im
    if (path.startsWith('/app/recommend')) return PageKind.Recommend
  } catch {
    return PageKind.Other
  }
  return PageKind.Other
}

// 登录只接受真机已验证的 isLoggedIn===true + staffId;残留 staff 不能降级判 in。
//
// 在 content script 的 isolated world 里跑,只允许用标准 DOM——这里读的是
// <script> 元素的文本内容,不是页面 JS 的运行期对象,故不依赖 MAIN world。
function readZhilianLoginState(): LoginState {
  const marker = '__INITIAL_STATE__='
  const source = Array.from(document.scripts)
    .map((script) => script.textContent ?? '')
    .find((text) => text.includes(marker))
  if (!source) return LoginState.Unknown
  const candidate = source.slice(source.indexOf(marker) + marker.length).trim().replace(/;$/u, '')
  try {
    const initial = JSON.parse(candidate) as Record<string, unknown>
    const sessionModule = asRecord(initial.session)
    const session = asRecord(sessionModule?.session)
    const staff = asRecord(session?.staff)
    if (session?.isLoggedIn === false) return LoginState.Out
    if (session?.isLoggedIn === true && staff?.staffId != null) return LoginState.In
  } catch {
    // 启动脚本形状变化时返回 unknown;绝不凭导航文案猜登录成功。
  }
  return LoginState.Unknown
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

export const zhilianSite: PlatformSite = {
  id: ZHILIAN_PLATFORM,
  origin: ZHILIAN_ORIGIN,
  match: ZHILIAN_MATCH,
  matches: matchesZhilian,
  pageKind: zhilianPageKind,
  readLoginState: readZhilianLoginState,
}
