// BOSS 直聘适配器。**当前只有两条能力**:`probe.platform` 与 `debug.osProbe`。
//
// 这不是"还没写完",是这一段刻意的范围:BOSS 走 OS 级键鼠注入,而在坐标被证明
// 对之前不该实现任何真业务原语。没实现的能力由 registry 的 `requireCapability`
// 在运行期显式拒绝(反模式 18),不会默认回成功。
//
// # 为什么是 MAIN world
//
// 2026-08-28 甲方裁决走 MAIN world 一次性读(见 docs/boss/BOSS取数通道决策-2026-08-28.md)。
// 实测:executeScript 不留任何可枚举全局、不进 performance 条目,整棵树走一遍
// 0.9~1.2ms。isolated world 拿不到 Vue 归一化后的消息数组与 `user$`。
//
// # 身份取数经过凭据,所以取数纪律写在这里
//
// `user$` 里挨着 `token`、`wt`、`wt2`、`clientIP`、`phone`、`email`。本文件**只把
// `userId` 读成一个局部数字**,当场哈希;整个 `user$` 绝不返回、不进 evidence、
// 不进日志、不进任何上报。这条不是风格,是「AI provider 数据边界」与凭据禁令的
// 直接要求。
import { contentScriptHealthy, runInPage } from './inject'
import { osProbeContractData, runOsProbe } from './osinput'
import { PlatformError } from './types'
import { BOSS_MATCH, BOSS_PLATFORM, bossSite } from './bossSite'
import type { InjectOptions } from './inject'
import type { PlatformAdapter } from './types'
import type { PrimitiveContext } from '../registry'
import type {
  DebugOsProbeArgs,
  DebugOsProbeData,
  ProbePlatformData,
} from '../../base/protocol'

export { BOSS_PLATFORM, BOSS_MATCH }

const BOSS_INJECT: InjectOptions = { world: 'MAIN', label: 'BOSS ' }

/**
 * 页面里读身份。**自包含函数**:会被序列化送进页面,闭包变量到不了那边。
 *
 * 判据是**形状**不是组件名:找一个 `user$` 为对象且 `userId` 是正整数的 Vue 实例。
 * 组件名(实测是 `app`)属平台私有内部,拿它当判据是高脆的;而 `user$.userId` 与
 * 消息信封里的 uid 同一个空间,是这个平台对外的身份语义。
 *
 * 失效方向只有一个:找不到就 unknown。**永不返回 out** —— 掉登录的形态从未观测过,
 * 用"读不到"去顶"已登出"会把一次页面没加载完说成账号掉了。
 */
async function mainReadBossPrincipal(): Promise<{
  loginState: 'in' | 'unknown'
  principalFingerprint: string | null
}> {
  let userId: number | null = null
  const visited = new Set<unknown>()
  for (const element of Array.from(document.querySelectorAll('*'))) {
    const mounted = (element as unknown as { __vue__?: unknown }).__vue__
    if (!mounted) continue
    let node = mounted as { user$?: unknown; $parent?: unknown } | undefined
    for (let hops = 0; node && hops < 100; hops += 1) {
      if (visited.has(node)) break
      visited.add(node)
      const user = node.user$ as { userId?: unknown } | undefined
      if (user && typeof user === 'object') {
        const raw = user.userId
        // 只取这一个数,别的字段一律不碰:同一个对象里就有 token/wt/wt2/clientIP。
        if (typeof raw === 'number' && Number.isSafeInteger(raw) && raw > 0) {
          userId = raw
          break
        }
      }
      node = node.$parent as { user$?: unknown; $parent?: unknown } | undefined
    }
    if (userId !== null) break
  }
  if (userId === null) return { loginState: 'unknown', principalFingerprint: null }

  // 与智联同一套规范化:版本标签 + 逐段长度前缀,避免不同拼接产生同一原文。
  const pieces = ['boss-principal-v1', String(userId)]
  const canonical = pieces.map((piece) => `${new TextEncoder().encode(piece).length}:${piece}`).join('|')
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(canonical))
  const fingerprint = Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('')
  return { loginState: 'in', principalFingerprint: fingerprint }
}

/**
 * 挑标签页。**多于一个就拒绝**,不挑一个"看起来最像的"。
 *
 * 这不是洁癖:接下来要么绑定账号、要么真的动鼠标,而两个 BOSS 页可能登着不同账号
 * 或停在不同位置。歧义时的正确方向是不做,让人去关掉多余的那个。
 */
async function bossTab(): Promise<chrome.tabs.Tab | null> {
  const tabs = (await chrome.tabs.query({ url: BOSS_MATCH }))
    .filter((tab) => tab.id !== undefined)
  if (tabs.length === 0) return null
  if (tabs.length > 1) {
    throw new PlatformError(
      'CTX_NOT_READY',
      `打开了 ${tabs.length} 个 BOSS 页面,无法确定该用哪个——请只留一个`,
      'manualOnly',
      'pageBroken',
    )
  }
  return tabs[0]!
}

async function probeBoss(): Promise<ProbePlatformData> {
  const tab = await bossTab()
  if (!tab || tab.id === undefined || tab.url === undefined) {
    return {
      pageKind: 'none',
      contentScriptOk: false,
      loginState: 'unknown',
      principalFingerprint: null,
      surface: null,
    }
  }
  const contentScriptOk = await contentScriptHealthy(tab.id)
  let principal: { loginState: 'in' | 'unknown'; principalFingerprint: string | null }
  try {
    principal = await runInPage(BOSS_INJECT, tab.id, mainReadBossPrincipal, [])
  } catch {
    // 页面还没加载完、或注入被拒:如实"读不到",不猜登录态。
    principal = { loginState: 'unknown', principalFingerprint: null }
  }
  return {
    pageKind: bossSite.pageKind(tab.url),
    contentScriptOk,
    loginState: principal.loginState,
    principalFingerprint: principal.principalFingerprint,
    // surface 的枚举当前只有智联 IM 列表一项,BOSS 上没有对应事实,不硬凑。
    surface: null,
  }
}

/**
 * 取一个**身份已核对**的 BOSS 标签页。
 *
 * 动鼠标之前必须确认"现在登录的还是脑绑定的那个人"。这道闸和智联那边同款:
 * 它防的是错靶——把 A 账号的动作落在 B 账号的页面上。
 */
async function verifiedBossTab(expectedFingerprint: string | undefined): Promise<chrome.tabs.Tab> {
  if (!expectedFingerprint) {
    throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  const probe = await probeBoss()
  if (probe.pageKind === 'none') {
    throw new PlatformError('CTX_NOT_READY', '请在 Chrome 中打开 BOSS 直聘页面', 'manualOnly', 'pageAbsent')
  }
  if (!probe.principalFingerprint) {
    throw new PlatformError('CTX_NOT_READY', '当前无法确证 BOSS 登录身份', 'afterRecovery', 'identityUnverified')
  }
  if (probe.principalFingerprint !== expectedFingerprint) {
    throw new PlatformError('ACCOUNT_MISMATCH', '当前 BOSS 登录账号与脑侧绑定不一致', 'manualOnly')
  }
  const tab = await bossTab()
  if (!tab || tab.id === undefined) {
    throw new PlatformError('CTX_NOT_READY', 'BOSS 标签页缺少 id', 'afterRecovery', 'pageBroken')
  }
  return tab
}

async function bossOsProbe(
  args: DebugOsProbeArgs,
  ctx: PrimitiveContext,
  fingerprint: string | undefined,
): Promise<DebugOsProbeData> {
  const tab = await verifiedBossTab(fingerprint)
  const probe = await runOsProbe(BOSS_INJECT, tab.id!, ctx)
  return osProbeContractData(args.target, probe, Date.now())
}

export const bossAdapter = {
  id: BOSS_PLATFORM,
  hostMatch: BOSS_MATCH,
  world: 'MAIN',
  // OS 级键鼠注入。BOSS 会查 `isTrusted`,页面内合成事件在这里不成立
  // ——这正是整条 OS 注入链存在的理由。
  input: 'os',
  // 埋点上报拦截:BOSS 的检测形态已考古但**尚未裁决要不要拦**。按「平台枚举面
  // 事实门」不凭空实现,这里不声明 envReportGuard。
  probePlatform: () => probeBoss(),
  osProbe: ({ args, ctx, fingerprint }) => bossOsProbe(args, ctx, fingerprint),
} satisfies PlatformAdapter
