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
import type { ClickObservation, ClickPlan } from './osinput'
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



// ————————————————————————————————————————————————————————————————————————
// reversibleToggle:OS 注入的点击靶子(开发期)
// ————————————————————————————————————————————————————————————————————————
//
// 靶子是会话列表顶上那排筛选页签里的「全部」与「收藏」。2026-08-30 真机考古
// (见 docs/boss/BOSS平台事实-2026-08-28.md 第十节):
//
//   - 两个独立的可见后置状态:selected 类的位置、列表条数(38 ↔ 0)
//   - 不打开任何会话、不动未读角标 —— 候选人侧零影响
//   - 双向可逆,来回各切一次已实证
//
// **它不是"零请求"的**:切一次发 15 条请求,其中 filterByLabel 是服务端筛选,
// 其余是埋点(三条 e.gif 的事件名里直接带着 warlock)。考古推翻了这个假设,
// 而结论不是换靶子 —— BOSS 上不存在无痕的点击。选靶判据因此是**语义最无害**:
// 只读查询、不碰候选人、可逆。
//
// **点哪个由页面当前状态决定**:两个里选没被选中的那个。于是连跑两趟自然回到原位,
// 一趟之内不需要点第二下(原语内不重试是内核,补一下"点回去"就破了这条)。
const TOGGLE_SELECTOR = '.chat-label-item'
const TOGGLE_SELECTED_CLASS = 'selected'
// 判据用**可见文本**,不用类名或位置:文本是平台的公开语义,类名是私有内部。
const TOGGLE_LABELS = ['全部', '收藏'] as const
// 页面上存点击观测的键。与落点观测器同样**必须不可枚举**——BOSS 会把
// Object.keys(window) 的未知全局名原样上送。
const CLICK_KEY = '__recruitHelperOsClick'

interface ToggleSnapshot {
  /** 在 selector 命中的序列里的下标。 */
  index: number
  label: string
  rect: { x: number; y: number; w: number; h: number }
  /** 平台的可见后置状态,自由文本,只给人读。 */
  state: string
}

/** 一次注入里做三件事:读页签状态、挑出要点的那个、装上点击观测器。 */
function mainLocateToggleAndObserve(
  selector: string,
  selectedClass: string,
  labels: readonly string[],
  key: string,
): ToggleSnapshot | { reason: string } {
  const items = Array.from(document.querySelectorAll(selector))
  const labelOf = (el: Element): string => (el.textContent ?? '').trim()
  const matched = items
    .map((el, index) => ({ el, index, text: labelOf(el) }))
    .filter((row) => labels.includes(row.text))
  if (matched.length !== labels.length) {
    return { reason: `筛选页签认不全:期望 ${labels.join('/')},在 ${items.length} 个候选里只认出 ${matched.length} 个` }
  }
  const unselected = matched.find((row) => !row.el.classList.contains(selectedClass))
  if (!unselected) {
    return { reason: '两个页签都没被选中,页面形态不认识' }
  }
  const rect = unselected.el.getBoundingClientRect()
  if (!(rect.width > 8) || !(rect.height > 8)) {
    return { reason: `靶子尺寸异常 ${Math.round(rect.width)}x${Math.round(rect.height)}` }
  }
  if (rect.left < 0 || rect.top < 0 || rect.right > window.innerWidth || rect.bottom > window.innerHeight) {
    return { reason: '靶子没有完整落在视口内' }
  }

  const w = window as unknown as Record<string, unknown>
  const previous = w[key] as { off?: () => void } | undefined
  if (previous && typeof previous.off === 'function') previous.off()
  const seen: {
    trusted: boolean | null; x: number | null; y: number | null
    target: EventTarget | null; off?: () => void
  } = { trusted: null, x: null, y: null, target: null }
  const onClick = (event: MouseEvent): void => {
    seen.trusted = event.isTrusted
    seen.x = event.clientX
    seen.y = event.clientY
    seen.target = event.target
  }
  document.addEventListener('click', onClick, true)
  seen.off = (): void => document.removeEventListener('click', onClick, true)
  Object.defineProperty(w, key, { value: seen, enumerable: false, configurable: true, writable: true })

  const selectedNow = matched.find((row) => row.el.classList.contains(selectedClass))
  return {
    index: unselected.index,
    label: unselected.text,
    rect: { x: rect.x, y: rect.y, w: rect.width, h: rect.height },
    state: `选中=${selectedNow ? selectedNow.text : '无'} 列表=${document.querySelectorAll('.geek-item').length}`,
  }
}

/** 落点上的元素是不是靶子。用平台自己的命中测试问,不自己算几何。 */
function mainHitTestToggle(
  selector: string, index: number, label: string, x: number, y: number,
): { onTarget: boolean; found: string } {
  const items = Array.from(document.querySelectorAll(selector))
  const target = items[index]
  if (!target || (target.textContent ?? '').trim() !== label) {
    return { onTarget: false, found: '靶子已经不在原来的位置上' }
  }
  const at = document.elementFromPoint(x, y)
  if (!at) return { onTarget: false, found: '落点上什么都没有' }
  const onTarget = at === target || target.contains(at)
  const tag = at.tagName.toLowerCase()
  const text = (at.textContent ?? '').trim().slice(0, 12)
  return { onTarget, found: onTarget ? `靶子(${tag})` : `${tag}「${text}」` }
}

/** 读点击观测与后置状态,并摘掉观测器。 */
function mainReadClickObservation(
  selector: string, selectedClass: string, index: number, label: string, key: string,
): { trusted: boolean | null; onTarget: boolean | null; eventDriftPx: number | null; after: string } {
  const w = window as unknown as Record<string, unknown>
  const seen = w[key] as {
    trusted: boolean | null; x: number | null; y: number | null
    target: EventTarget | null; off?: () => void
  } | undefined
  if (seen && typeof seen.off === 'function') seen.off()
  delete w[key]

  const items = Array.from(document.querySelectorAll(selector))
  const target = items[index]
  const selectedNow = items.find((el) => el.classList.contains(selectedClass))
  const after = `选中=${selectedNow ? (selectedNow.textContent ?? '').trim() : '无'}` +
    ` 列表=${document.querySelectorAll('.geek-item').length}` +
    ` 靶子「${label}」${target && target.classList.contains(selectedClass) ? '已选中' : '未选中'}`

  if (!seen) return { trusted: null, onTarget: null, eventDriftPx: null, after }
  let drift: number | null = null
  if (target && seen.x !== null && seen.y !== null) {
    const rect = target.getBoundingClientRect()
    drift = Math.ceil(Math.hypot(seen.x - (rect.x + rect.width / 2), seen.y - (rect.y + rect.height / 2)))
  }
  const node = seen.target instanceof Element ? seen.target : null
  return {
    trusted: seen.trusted,
    onTarget: target && node ? (node === target || target.contains(node)) : null,
    eventDriftPx: drift,
    after,
  }
}

/** 组装 BOSS 的点击计划。平台知识全在这儿,编排层一个 selector 都不认识。 */
async function bossClickPlan(tabId: number): Promise<ClickPlan> {
  const located = await runInPage(BOSS_INJECT, tabId, mainLocateToggleAndObserve,
    [TOGGLE_SELECTOR, TOGGLE_SELECTED_CLASS, TOGGLE_LABELS as unknown as string[], CLICK_KEY])
  if ('reason' in located) {
    throw new PlatformError('TARGET_NOT_FOUND', `点击靶子不可用:${located.reason}`, 'manualOnly')
  }
  const snapshot = located
  return {
    label: `${snapshot.label}(点前 ${snapshot.state})`,
    rect: snapshot.rect,
    hitTest: (x, y) => runInPage(BOSS_INJECT, tabId, mainHitTestToggle,
      [TOGGLE_SELECTOR, snapshot.index, snapshot.label, x, y]),
    observe: async (): Promise<ClickObservation> => runInPage(BOSS_INJECT, tabId, mainReadClickObservation,
      [TOGGLE_SELECTOR, TOGGLE_SELECTED_CLASS, snapshot.index, snapshot.label, CLICK_KEY]),
  }
}

async function bossOsProbe(
  args: DebugOsProbeArgs,
  ctx: PrimitiveContext,
  fingerprint: string | undefined,
): Promise<DebugOsProbeData> {
  const tab = await verifiedBossTab(fingerprint)
  // 靶子在**移动之前**就要定位好:定不到就一步都不动。
  const click = args.target === 'reversibleToggle' ? await bossClickPlan(tab.id!) : undefined
  const probe = await runOsProbe(BOSS_INJECT, tab.id!, ctx, click)
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
