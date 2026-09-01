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
import { isHandServiceDown, osProbeContractData, playTypePlan, runOsProbe, seedFrom } from './osinput'
import { planType } from '../osengine/plan'
import type { ClickObservation, ClickPlan } from './osinput'
import { PlatformError } from './types'
import { BOSS_MATCH, BOSS_PLATFORM, bossSite } from './bossSite'
import type { InjectOptions } from './inject'
import type { PlatformAdapter } from './types'
import type { PrimitiveContext } from '../registry'
import type {
  DebugOsProbeArgs,
  DebugOsTypeArgs,
  DebugOsTypeData,
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

/**
 * 读点击观测与后置状态,并摘掉观测器。
 *
 * **先条件轮询等后置状态落定,再读。** 页签的 `selected` 类翻得快,但列表条数要等
 * 服务端 filterByLabel 回来才变;点完立刻读会记下一个"选中已经变了、列表还是旧的"
 * 的半截现场,而这份文本是事后还原真机的唯一依据。
 *
 * 上限只给 3 秒,不是 20 秒:这是**诊断读**不是放行判据 —— 3 秒还没翻本身就是那个
 * 发现,再等下去不会让结论变好。等不到也照样读、照样上报。
 */
async function mainReadClickObservation(
  selector: string, selectedClass: string, index: number, label: string, key: string,
): Promise<{ trusted: boolean | null; onTarget: boolean | null; eventDriftPx: number | null; after: string }> {
  const deadline = Date.now() + 3000
  while (Date.now() < deadline) {
    const at = Array.from(document.querySelectorAll(selector))[index]
    if (at && at.classList.contains(selectedClass)) break
    await new Promise((resolve) => setTimeout(resolve, 50))
  }
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

/** 摘掉点击观测器。幂等:observe() 已经摘过就什么都不做。 */
function mainDetachClickObserver(key: string): { detached: boolean } {
  const w = window as unknown as Record<string, unknown>
  const seen = w[key] as { off?: () => void } | undefined
  if (!seen) return { detached: false }
  if (typeof seen.off === 'function') seen.off()
  delete w[key]
  return { detached: true }
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
  try {
    const probe = await runOsProbe(BOSS_INJECT, tab.id!, ctx, click)
    return osProbeContractData(args.target, probe, Date.now())
  } finally {
    // 点击观测器只活在这条命令里(手的禁令 2:页面上不留常驻状态)。
    // 闸没放行时 observe() 压根不会被调用,那条路上没人摘它 —— 所以摘在这儿。
    if (click) {
      try {
        await runInPage(BOSS_INJECT, tab.id!, mainDetachClickObserver, [CLICK_KEY])
      } catch {
        // 页面可能已经导航走了。摘不掉不是失败——它随页面一起没。
      }
    }
  }
}

// ── 打字:输入框 ────────────────────────────────────────────────────────────
//
// 事实见 docs/boss/BOSS平台事实-2026-08-28.md §十一(2026-09-01 只读实测)。

/**
 * 输入框的 id。**全页唯一**(id 与 class 各命中 1 个),用 id 因为它是公开且按定义
 * 唯一的锚点,比 class 稳。
 *
 * 它是 `contenteditable` 的 div,不是 textarea——读写都走 `textContent`。
 * 用 `.value` 会静默拿到 undefined,而 `undefined === ''` 为假,"空不空"的判据
 * 会永远说非空。
 */
const COMPOSER_ID = 'boss-chat-editor-input'

/** 输入框现在的样子。找不到就 found=false,不抛——调用方要据此收成拒绝而不是失败。 */
function mainReadComposer(id: string): {
  found: boolean
  x: number; y: number; w: number; h: number
  text: string
  focused: boolean
} {
  const el = document.getElementById(id)
  if (!el) return { found: false, x: 0, y: 0, w: 0, h: 0, text: '', focused: false }
  const r = el.getBoundingClientRect()
  return {
    found: true,
    x: r.x, y: r.y, w: r.width, h: r.height,
    // 空态实测是真的空(innerHTML ""、childNodes 0),没有 <br> 哨兵,
    // 所以不需要额外归一化。
    text: el.textContent ?? '',
    focused: document.activeElement === el,
  }
}

function mainHitTestComposer(id: string, x: number, y: number): { onTarget: boolean; found: string } {
  const el = document.getElementById(id)
  const node = document.elementFromPoint(x, y)
  return {
    onTarget: !!(el && node && (node === el || el.contains(node))),
    found: node ? node.tagName.toLowerCase() + (node.id ? '#' + node.id : '') : '无',
  }
}

/**
 * 输入框的点击计划——**只为拿焦点**。
 *
 * `observe()` 的事件三项如实报 null:我们没在这条路上装点击观测器。装它对拿焦点
 * 这件事没有增量——`isTrusted` 在这个页面上已经由筛选页签那个靶子验过了,
 * 而这里真正的后置条件是"焦点到了没有",那是标准 DOM 属性,直接读。
 */
async function bossComposerClickPlan(tabId: number): Promise<ClickPlan> {
  const before = await runInPage(BOSS_INJECT, tabId, mainReadComposer, [COMPOSER_ID])
  if (!before.found) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '页面上找不到聊天输入框', 'manualOnly')
  }
  return {
    label: `输入框(点前 焦点=${before.focused ? '在' : '不在'})`,
    rect: { x: before.x, y: before.y, w: before.w, h: before.h },
    hitTest: (x, y) => runInPage(BOSS_INJECT, tabId, mainHitTestComposer, [COMPOSER_ID, x, y]),
    observe: async (): Promise<ClickObservation> => {
      const after = await runInPage(BOSS_INJECT, tabId, mainReadComposer, [COMPOSER_ID])
      return {
        trusted: null,
        onTarget: null,
        eventDriftPx: null,
        after: `焦点=${after.focused ? '在输入框' : '不在'} 内容长度=${after.text.length}`,
      }
    },
  }
}

function osTypeData(
  outcome: DebugOsTypeData['outcome'],
  started: number,
  parts: Partial<DebugOsTypeData>,
): DebugOsTypeData {
  return {
    outcome,
    keys: 0, tries: 0, planMs: 0, lagMaxUs: 0,
    ...parts,
    elapsedMs: Date.now() - started,
    observedAt: Date.now(),
  }
}

/**
 * 把一句中文打进 IM 输入框,然后停手——**不点发送**。
 *
 * 五段:读输入框状态 → 焦点不在就 OS 点一下拿回来 → 排版 → 交手服务播 → 回读比对。
 *
 * **焦点不是必须靠点击拿的**:实测 BOSS 打开/切换会话时自己就把焦点放上去了,
 * 真人在这个页面上也是直接开始敲。所以只在焦点不在时才点——那既少一次动作,
 * 也更贴近真人。但 `.focus()` 不在选项里:那会造出一个没有前置点击的焦点,
 * 是真人身上不存在的形状。
 *
 * **matched 只报不判。** macOS 上没有自研 TIP、走系统输入法,上屏词不可控;
 * Windows 上 TIP 说了算,应当逐字相同。两边同一份代码、同一条判定,差别如实带出。
 */
async function bossOsType(
  args: DebugOsTypeArgs,
  ctx: PrimitiveContext,
  fingerprint: string | undefined,
): Promise<DebugOsTypeData> {
  const started = Date.now()
  const tab = await verifiedBossTab(fingerprint)
  const trace: string[] = []

  const before = await runInPage(BOSS_INJECT, tab.id!, mainReadComposer, [COMPOSER_ID])
  if (!before.found) {
    return osTypeData('refusedByGate', started, { detail: '页面上找不到聊天输入框' })
  }
  // composer.empty 是硬前置。覆盖用户已经敲进去的字是三条红线之一,
  // 这条闸与发送原语用的是同一个,不为调试放宽。
  if (before.text !== '') {
    return osTypeData('refusedByGate', started, {
      detail: `输入框非空(${before.text.length} 字),不覆盖用户已经敲进去的内容`,
    })
  }
  trace.push(`点前 焦点=${before.focused ? '在' : '不在'} 矩形=${Math.round(before.w)}x${Math.round(before.h)}`)

  // 焦点不在才点。点的这一段完全复用鼠标线的闸链(标定、命中测试、光标未被动过)。
  if (!before.focused) {
    const probe = await runOsProbe(BOSS_INJECT, tab.id!, ctx, await bossComposerClickPlan(tab.id!))
    trace.push(`取焦点 ${probe.outcome} ${probe.detail ?? ''}`)
    if (probe.outcome === 'handServiceUnavailable') {
      return osTypeData('handServiceUnavailable', started, { detail: trace.join(' | ') })
    }
    if (probe.outcome !== 'clicked') {
      return osTypeData('refusedByGate', started, { detail: trace.join(' | ') })
    }
    const after = await runInPage(BOSS_INJECT, tab.id!, mainReadComposer, [COMPOSER_ID])
    if (!after.focused) {
      // 点中了却没拿到焦点。停手——没有焦点的按键会打到别处去。
      return osTypeData('refusedByGate', started, {
        detail: `${trace.join(' | ')} | 点中了但焦点没到输入框`,
      })
    }
  }

  // 排版。**排不出来就不打**,不许兜底成"那就随便打一份"。
  const planStarted = Date.now()
  let composed
  try {
    composed = await planType(args.text, seedFrom(ctx.cmdMsgId, 0))
  } catch (error) {
    // 打不出的字元(英文字母、半角标点)在这里显式抛,不是排不出合格形状。
    return osTypeData('planFailed', started, {
      planMs: Date.now() - planStarted,
      detail: `${trace.join(' | ')} | ${String(error instanceof Error ? error.message : error).slice(0, 300)}`,
    })
  }
  const planMs = Date.now() - planStarted
  if (!composed.ok) {
    return osTypeData('planFailed', started, {
      planMs, tries: composed.tries,
      detail: `${trace.join(' | ')} | 排版器 ${composed.tries} 次重采都没排出合格形状`,
    })
  }
  trace.push(`排版 ${composed.tries} 次重采 ${composed.plan.words.length} 词 ${planMs}ms`)

  let played
  try {
    played = await playTypePlan(composed.plan)
  } catch (error) {
    if (isHandServiceDown(error)) {
      return osTypeData('handServiceUnavailable', started, {
        planMs, tries: composed.tries, detail: trace.join(' | '),
      })
    }
    // 手服务收下了但拒绝或失败(键码不认识、修饰键窗口不足、注入半途失败)。
    // 那些都不是"排不出来",而是发出去这一段的事——如实带出,不改判成 planFailed。
    return osTypeData('refusedByGate', started, {
      planMs, tries: composed.tries,
      detail: `${trace.join(' | ')} | 手服务:${String(error instanceof Error ? error.message : error).slice(0, 300)}`,
    })
  }

  const read = await runInPage(BOSS_INJECT, tab.id!, mainReadComposer, [COMPOSER_ID])
  const matched = read.text === args.text
  trace.push(`发了 ${played.keys} 次按键 滞后最大 ${Math.round(played.lagMaxUs)}us`)
  trace.push(`回读 ${matched ? '逐字相同' : `不同:期望 ${args.text.length} 字、实得 ${read.text.length} 字`}`)

  return osTypeData('typed', started, {
    keys: played.keys,
    tries: composed.tries,
    planMs,
    lagMaxUs: Math.round(played.lagMaxUs),
    matched,
    detail: trace.join(' | '),
  })
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
  osType: ({ args, ctx, fingerprint }) => bossOsType(args, ctx, fingerprint),
} satisfies PlatformAdapter
