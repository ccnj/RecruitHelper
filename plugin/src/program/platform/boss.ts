// BOSS 直聘适配器。三条探针(`probe.platform`、`debug.osProbe`、`debug.osType`)加
// 场景一的七条会话原语(2026-09-03 开工,见文件下半部)。没实现的能力由 registry 的
// `requireCapability` 在运行期显式拒绝(反模式 18),不会默认回成功。
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
import { tabNavigationGeneration } from '../../base/tabGeneration'
import { composeClearKeys, isHandServiceDown, osClickContractData, osProbeContractData, playKeys, playTypePlan, readHandOS, runOsProbe, seedFrom } from './osinput'
import { planType } from '../osengine/plan'
import { osScrollContractData, runOsScroll } from './osscroll'
import type { ScrollTarget } from './osscroll'
import type { ClickObservation, ClickPlan } from './osinput'
import { PlatformError } from './types'
import { BOSS_MATCH, BOSS_PLATFORM, bossSite } from './bossSite'
import type { InjectOptions } from './inject'
import type { PlatformAdapter } from './types'
import type { PrimitiveContext } from '../registry'
import { Primitive as PrimitiveName, validatePrimitiveArgs, validatePrimitiveData } from '../../base/protocol'
import { BlobChannelError, captureVisibleTabJpegDataUrl, putSessionBlob, sessionBlobParams } from '../../base/capture'
import { describeError, reportHandLog } from '../../base/handLog'
import type { BlobPutOutcome } from '../../base/capture'
import type {
  CandidateReadResumeArgs,
  CandidateReadResumeData,
  CandidateResumeLabelValue,
  CaptureScreenshotData,
  ChatCaptureThreadScreenshotArgs,
  ChatIdentifyCurrentConversationData,
  ChatOpenConversationArgs,
  ChatOpenConversationData,
  ChatReadListArgs,
  ChatReadListData,
  ChatReadThreadArgs,
  ChatReadThreadData,
  ChatReadUnreadTotalData,
  ChatAcceptWechatArgs,
  ChatAcceptWechatData,
  ChatReadWechatExchangeOutcomeArgs,
  ChatReadWechatExchangeOutcomeData,
  ChatSendMessageArgs,
  ChatSendMessageData,
  ChatSendMessageGuards,
  ChatSendWechatInviteArgs,
  ChatSendWechatInviteData,
  ConversationSummary,
  DebugOsClickArgs,
  DebugOsClickData,
  DebugOsProbeArgs,
  DebugOsScrollArgs,
  DebugOsScrollData,
  DebugOsTypeArgs,
  DebugOsTypeData,
  DebugOsProbeData,
  MessageAnchor,
  PeerSummary,
  ProbePlatformData,
  ThreadMessage,
} from '../../base/protocol'

export { BOSS_PLATFORM, BOSS_MATCH }

const BOSS_INJECT: InjectOptions = { world: 'MAIN', label: 'BOSS ' }
/**
 * DOM 读与观测器走 isolated world(2026-09-03 甲方裁决,取数通道文档 §十之二)。
 * MAIN world 只留给 DOM 给不了的事实:消息数组、当前会话对象、身份指纹。
 */
const BOSS_DOM: InjectOptions = { world: 'ISOLATED', label: 'BOSS ' }

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
/** 缓存有效期。协议规格 §12 第 9 条(2026-09-03 增补)定的 30 分钟。 */
const IDENTITY_CACHE_TTL_MS = 30 * 60_000

interface VerifiedIdentity {
  fingerprint: string
  /** 读指纹**之前**记下的导航代数:读的过程中若发生导航,下次比对必然不等,方向是重读。 */
  generation: number
  verifiedAt: number
}

/** 按标签页缓存的身份核对结果。只在内存:后台进程重启即清,那正是该失效的时刻之一。 */
const verifiedIdentities = new Map<number, VerifiedIdentity>()

/**
 * 缓存能不能顶掉这一次 MAIN world 读。三个条件缺一即重读:指纹与脑要求的相同、
 * 标签页自那次读取后没有主框架导航、没超过有效期。纯函数,单测直接钉。
 */
export function identityCacheUsable(
  cached: VerifiedIdentity | undefined,
  expectedFingerprint: string,
  currentGeneration: number,
  now: number,
): boolean {
  if (!cached) return false
  if (cached.fingerprint !== expectedFingerprint) return false
  if (cached.generation !== currentGeneration) return false
  if (now - cached.verifiedAt >= IDENTITY_CACHE_TTL_MS || now < cached.verifiedAt) return false
  return true
}

/** 测试专用。 */
export function resetBossIdentityCacheForTest(): void {
  verifiedIdentities.clear()
}

async function verifiedBossTab(expectedFingerprint: string | undefined): Promise<chrome.tabs.Tab> {
  if (!expectedFingerprint) {
    throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  }
  // 缓存命中就不进 MAIN world(协议规格 §12 第 9 条 2026-09-03 增补):BOSS 上换账号必经
  // 整页导航,导航代数没变、指纹相同、没超期,页面就还是核对过的那个账号。
  const cachedTab = await bossTab()
  if (cachedTab && cachedTab.id !== undefined) {
    const cached = verifiedIdentities.get(cachedTab.id)
    if (identityCacheUsable(cached, expectedFingerprint, tabNavigationGeneration(cachedTab.id), Date.now())) {
      return cachedTab
    }
  }
  const generationBeforeRead = cachedTab && cachedTab.id !== undefined
    ? tabNavigationGeneration(cachedTab.id)
    : 0
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
  verifiedIdentities.set(tab.id, {
    fingerprint: probe.principalFingerprint,
    generation: generationBeforeRead,
    verifiedAt: Date.now(),
  })
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
  const located = await runInPage(BOSS_DOM, tabId, mainLocateToggleAndObserve,
    [TOGGLE_SELECTOR, TOGGLE_SELECTED_CLASS, TOGGLE_LABELS as unknown as string[], CLICK_KEY])
  if ('reason' in located) {
    throw new PlatformError('TARGET_NOT_FOUND', `点击靶子不可用:${located.reason}`, 'manualOnly')
  }
  const snapshot = located
  return {
    label: `${snapshot.label}(点前 ${snapshot.state})`,
    rect: snapshot.rect,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, mainHitTestToggle,
      [TOGGLE_SELECTOR, snapshot.index, snapshot.label, x, y]),
    observe: async (): Promise<ClickObservation> => runInPage(BOSS_DOM, tabId, mainReadClickObservation,
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
        await runInPage(BOSS_DOM, tab.id!, mainDetachClickObserver, [CLICK_KEY])
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
  windowFocused: boolean
} {
  const el = document.getElementById(id)
  const windowFocused = document.hasFocus()
  if (!el) return { found: false, x: 0, y: 0, w: 0, h: 0, text: '', focused: false, windowFocused }
  const r = el.getBoundingClientRect()
  return {
    found: true,
    x: r.x, y: r.y, w: r.width, h: r.height,
    // 空态实测是真的空(innerHTML ""、childNodes 0),没有 <br> 哨兵,
    // 所以不需要额外归一化。
    text: el.textContent ?? '',
    // 页面内部的焦点。**它不足以放行打字** —— 见 windowFocused。
    focused: document.activeElement === el,
    /**
     * 这个文档所在的窗口是不是系统当前的活动窗口。
     *
     * **这是键盘线的「零观测」闸。** 鼠标那半有个天然的:页面没观测到 mousemove
     * 就说明光标不在页面上,拒绝。键盘没有——我们是盲发,发完才回读。
     *
     * 而 `activeElement === 输入框` 只说明**页面内部**的焦点在那儿:Chrome 退到
     * 后台时它照样为真,可 CGEventPost / SendInput 打的是**最前台的那个窗口**。
     * 少了这道闸,Chrome 不在前台时这条命令会把拼音字母敲进用户正在看的别的应用。
     *
     * `document.hasFocus()` 是标准 DOM API,只在文档所在窗口是系统活动窗口时为真。
     */
    windowFocused,
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
  const before = await runInPage(BOSS_DOM, tabId, mainReadComposer, [COMPOSER_ID])
  if (!before.found) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '页面上找不到聊天输入框', 'manualOnly')
  }
  return {
    label: `输入框(点前 焦点=${before.focused ? '在' : '不在'})`,
    rect: { x: before.x, y: before.y, w: before.w, h: before.h },
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, mainHitTestComposer, [COMPOSER_ID, x, y]),
    observe: async (): Promise<ClickObservation> => {
      const after = await runInPage(BOSS_DOM, tabId, mainReadComposer, [COMPOSER_ID])
      return {
        trusted: null,
        onTarget: null,
        eventDriftPx: null,
        after: `焦点=${after.focused ? '在输入框' : '不在'} 内容长度=${after.text.length}`,
      }
    },
  }
}

/**
 * 把换行符去掉再打。**删掉,不是拒绝。**
 *
 * 排版器能把换行排成 Shift+Enter(上游 2026-09-01 起,透传段),TIP 也不吃 Enter;
 * 拦在我们 Go 键码表那一层的「Enter 不认识」会让整条命令失败、一个键不发——
 * 甲方 2026-09-02 裁决:一个换行不是大问题,不该让上层调用方为它兜底。
 * 两条路里选删掉而不是放行:放行 Enter 赌的是「Shift 松早了就把半截话发出去」那条红线,
 * 而 TIP 里还没有「换行段但 Shift 没按住就吃掉 Enter」的闸;删掉只是少一个换行,
 * 属于宁可少做那一侧。
 *
 * `\r` 一并去掉:Windows 剪贴板来的文案常带 `\r\n`,单独的 `\r` 排版器本来就打不出。
 *
 * **删了多少必须报出去。** 上游 sanitize.mjs 那条洞见对这里同样成立:脑写的是原文,
 * 实际发出去的是删过的,候选人回复之后脑会基于一段它从没发出去过的历史往下写。
 * 所以计数进 detail,回读也拿删过的文案比——发出去的才是事实。
 */
export function stripNewlines(text: string): { text: string; removed: number } {
  const stripped = text.replace(/\r\n|\r|\n/g, '')
  return { text: stripped, removed: text.length - stripped.length }
}

/**
 * 发送用:把换行串换成**一个空格**,不是删掉。
 *
 * 脑侧 contentHash 按 §4.5 把换行当空白折叠成一个空格;删掉换行会让打出去的文本与
 * 哈希对不上,发后正证必然零命中、每条都转人工(2026-09-03 出口审查 O3)。换成空格
 * 之后再经同一套规范化,两边逐字节相同。debug.osType 仍按 2026-09-02 裁决删掉换行,
 * 它不发送、没有哈希要对。
 */
export function newlinesToSpaces(text: string): { text: string; removed: number } {
  const replaced = text.replace(/(?:\r\n|\r|\n)+/gu, ' ')
  return { text: replaced, removed: (text.match(/\r\n|\r|\n/gu) ?? []).length }
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

  const before = await runInPage(BOSS_DOM, tab.id!, mainReadComposer, [COMPOSER_ID])
  if (!before.found) {
    return osTypeData('refusedByGate', started, { detail: '页面上找不到聊天输入框' })
  }
  // 输入框非空不再拒(2026-09-03 甲方裁决撤销 composer.empty):取到焦点、前台就绪后
  // 走与发送原语同一条清空路径,见下方。
  trace.push(`点前 焦点=${before.focused ? '在' : '不在'} 窗口=${before.windowFocused ? '在前台' : '不在前台'}` +
    ` 矩形=${Math.round(before.w)}x${Math.round(before.h)}`)

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
    const after = await runInPage(BOSS_DOM, tab.id!, mainReadComposer, [COMPOSER_ID])
    if (!after.focused) {
      // 点中了却没拿到焦点。停手——没有焦点的按键会打到别处去。
      return osTypeData('refusedByGate', started, {
        detail: `${trace.join(' | ')} | 点中了但焦点没到输入框`,
      })
    }
  }

  // **发键之前最后一道闸:窗口必须在前台。** 现读而不是用开头那次的值——
  // 中间可能刚做过一次 OS 点击,而点击本身会改变哪个窗口在前台。
  const beforeKeys = await runInPage(BOSS_DOM, tab.id!, mainReadComposer, [COMPOSER_ID])
  if (!beforeKeys.windowFocused) {
    return osTypeData('refusedByGate', started, {
      detail: `${trace.join(' | ')} | Chrome 不在前台,按键会打到别的应用上`,
    })
  }
  if (!beforeKeys.focused) {
    return osTypeData('refusedByGate', started, {
      detail: `${trace.join(' | ')} | 焦点不在输入框,按键会打到别处`,
    })
  }
  // 框里有字就先清掉,与发送原语同一条路径(2026-09-03 裁决撤销 composer.empty)。
  if (beforeKeys.text !== '') {
    try {
      await clearBossComposerByKeys(tab.id!, ctx, trace, beforeKeys.text.length)
    } catch (error) {
      const outcome = error instanceof PlatformError && error.code === 'CTX_NOT_READY' ? 'handServiceUnavailable' : 'refusedByGate'
      return osTypeData(outcome, started, {
        detail: `${trace.join(' | ')} | 清空输入框失败:${describeError(error).slice(0, 300)}`,
      })
    }
  }

  // 换行先删掉(理由见 stripNewlines)。删了就留痕——发出去的才是事实。
  const { text: typedText, removed: newlinesRemoved } = stripNewlines(args.text)
  if (newlinesRemoved > 0) trace.push(`去掉 ${newlinesRemoved} 个换行符`)
  if (typedText === '') {
    return osTypeData('planFailed', started, {
      detail: `${trace.join(' | ')} | 去掉换行之后没有内容可打`,
    })
  }

  // 排版。**排不出来就不打**,不许兜底成"那就随便打一份"。
  const planStarted = Date.now()
  let composed
  try {
    composed = await planType(typedText, seedFrom(ctx.cmdMsgId, 0))
  } catch (error) {
    // 走到这里的是**排版器自己坏了**(配置缺参数、平台名写错、pinyin-pro 对不齐),
    // 不是文案打不出——后者自上游 2026-09-01 起走返回值 ok:false。前者每一条文案
    // 都会撞上,如实标出来,别让人往文案上找原因。
    return osTypeData('planFailed', started, {
      planMs: Date.now() - planStarted,
      detail: `${trace.join(' | ')} | 排版器自身异常:${String(error instanceof Error ? error.message : error).slice(0, 300)}`,
    })
  }
  const planMs = Date.now() - planStarted
  if (!composed.ok) {
    // 两种来源一种形状:「有打不出的字元」(会指名是哪个)与「重采 N 次都没过预检」。
    // reasons 原样带出——它是判定现场,收窄成一句"排不出"就丢了。
    return osTypeData('planFailed', started, {
      planMs, tries: composed.tries,
      detail: `${trace.join(' | ')} | 排版器 ${composed.tries} 次重采:${composed.reasons.join(';').slice(0, 300)}`,
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

  const read = await runInPage(BOSS_DOM, tab.id!, mainReadComposer, [COMPOSER_ID])
  // 比的是删过换行的那份——那才是真发出去的。拿原文比会把自己删掉的换行记成"上屏错了"。
  const matched = read.text === typedText
  trace.push(`发了 ${played.keys} 次按键 滞后最大 ${Math.round(played.lagMaxUs)}us` +
    // 有 TIP 的平台才有这一段。它回答的是 matched=false 时最要紧的那个岔路:
    // **词表到底有没有被上屏机制用上**——「TIP 上屏 0/7」与「上屏 7/7 但选错」
    // 是两种完全不同的病,而回读文本本身分不出来。
    (played.words ? ` | ${played.words}` : ''))
  // 回读不同时**把实得的原文带出来**。只报"期望 12 字、实得 12 字"等于把判据
  // 压成一个布尔——而这一块存在的全部理由就是看 TIP 选了哪个词:「加个」出成
  // 「价格」和出成「家哥」是两种病,字数一样。「错误收敛必须留痕」正是禁这个形状。
  //
  // 不涉隐私:输入框进来时是空的(硬前置),里面只可能是我方从诊断台发出去的那句
  // 话被输入法改写的样子,不含候选人任何内容。
  trace.push(`回读 ${matched ? '逐字相同' : `不同:期望「${typedText}」实得「${read.text.slice(0, 200)}」`}`)

  return osTypeData('typed', started, {
    keys: played.keys,
    tries: composed.tries,
    planMs,
    lagMaxUs: Math.round(played.lagMaxUs),
    matched,
    detail: trace.join(' | '),
  })
}


// ============================================================================
// 场景一:会话感知与回复的七个原语(2026-09-03 开工,出口见对话记录与记忆)。
//
// 读取分工按取数通道文档 §十之二:MAIN world 只读 DOM 给不了的三样——消息数组
// (含同一次注入顺带读到的 conversation$ 作会话绑定核对)、发送前基线、当前会话对象;
// 其余全部在 isolated world 读 DOM。ID 一律取自内存;引用格式 `uid-friendSource`,
// 它在内存里可拼、在行的 data-id 上也有,点击定位一句选择器即到。
//
// 事实依据:docs/boss/BOSS平台事实-2026-08-28.md §二(消息形状与枚举)、§十一(输入区)、
// §十二(列表数据层、打开会话的后置状态、message-list 持有者、「未读」小页签)。
// ============================================================================

const RESULT_DATA_BUDGET = 60 * 1024
const LIST_WINDOW_MAX = 32
const THREAD_WINDOW_MAX = 64
/** 条件等待上限(AGENTS「平台交互节奏与条件等待」2026-08-26 放宽到 20 秒,是封顶不是必须用满)。 */
const READY_WAIT_MS = 20_000
/** 全选删除之后等输入框回读为空的封顶。三次按键之后页面一帧就该空,给 5 秒是宽裕。 */
const CLEAR_WAIT_MS = 5_000
const READY_POLL_MS = 250
/** 发后验证读的时钟容差,与脑侧 §9.4.1 同款 5 秒。 */
const SEND_CLOCK_TOLERANCE_MS = 5_000

/** 侧栏 IM 未读总角标。全页唯一(2026-09-03 实测);零态未观测,节点缺席按 null。 */
const BOSS_UNREAD_BADGE_SELECTOR = '.menu-chat-badge'
const ROW_SELECTOR = '.geek-item'
const ROW_SELECTED_CLASS = 'selected'
const ROW_BUBBLE_SELECTOR = '.badge-count'
const LABEL_TAB_SELECTOR = '.chat-label-item'
const LABEL_TAB_SELECTED_CLASS = 'selected'
const SUB_TAB_SELECTOR = '.chat-message-filter-left span'
const SUB_TAB_ACTIVE_CLASS = 'active'
const JOB_LABEL_SELECTOR = '.chat-select-job'
const SEND_BUTTON_SELECTOR = '.submit-content .submit'
const CHAT_LIST_SELECTOR = '.chat-message-list'
const LABEL_ALL = '全部'
const SUB_TAB_ALL = '全部'
const SUB_TAB_UNREAD = '未读'
const JOB_ALL = '全部职位'

function jsonBytes(value: unknown): number {
  return new TextEncoder().encode(JSON.stringify(value)).length
}

async function sha256Hex(value: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value))
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('')
}

/** 与智联、与协议 §4.5 同一套规范化:NFC、nbsp、空白串折叠、trim。 */
export function normalizeBossMessageText(value: string): string {
  return value.normalize('NFC').replace(/ /gu, ' ').replace(/\s+/gu, ' ').trim()
}

/** 会话引用。两层都有:内存里 uid+friendSource 拼出来,DOM 行的 data-id 就是它。 */
export function bossConversationRef(uid: number, friendSource: number): string {
  return `${uid}-${friendSource}`
}

export function parseBossConversationRef(ref: string): { uid: number; friendSource: number } | null {
  const match = /^([1-9]\d{0,17})-(\d{1,6})$/u.exec(ref)
  if (!match) return null
  const uid = Number(match[1])
  const friendSource = Number(match[2])
  if (!Number.isSafeInteger(uid) || !Number.isSafeInteger(friendSource)) return null
  return { uid, friendSource }
}

/**
 * 角标文本 → 未读数。口径抄智联(2026-08-03 教训):空文本是"无未读"的正式形态,不是缺失;
 * "99+" 取前导数字向多算,绝不落回 0。
 */
export function parseBossUnreadBadgeText(raw: string): number | null {
  const text = raw.trim()
  if (text === '') return 0
  if (/^\d+$/u.test(text)) {
    const value = Number(text)
    return Number.isSafeInteger(value) && value >= 0 && value <= 1_000_000 ? value : null
  }
  const leading = /^(\d+)/u.exec(text)
  if (leading) {
    const value = Number(leading[1])
    return Number.isSafeInteger(value) && value >= 0 && value <= 1_000_000 ? value : 1
  }
  return 1
}

async function sleep(ms: number): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, ms))
}

/** 条件轮询:满足即走,封顶 READY_WAIT_MS。返回最后一次读值,由调用方判定与留痕。 */
async function pollUntil<T>(
  ctx: PrimitiveContext,
  read: () => Promise<T>,
  done: (value: T) => boolean,
  maxMs = READY_WAIT_MS,
): Promise<{ value: T; satisfied: boolean }> {
  const deadline = Date.now() + maxMs
  let value = await read()
  while (!done(value) && Date.now() < deadline) {
    ctx.checkpoint()
    await sleep(READY_POLL_MS)
    value = await read()
  }
  return { value, satisfied: done(value) }
}

// ── isolated world:DOM 读 ────────────────────────────────────────────────────
//
// 以下 dom* 函数会被序列化送进 isolated world,闭包变量到不了那边,选择器一律经参数传。

interface DomRect4 { x: number; y: number; w: number; h: number }

function domReadBossUnreadBadge(selector: string): { found: boolean; text: string; count: number } {
  const nodes = document.querySelectorAll(selector)
  const first = nodes[0]
  return { found: !!first, text: first ? (first.textContent ?? '') : '', count: nodes.length }
}

interface DomListState {
  labelSelected: string
  subTabActive: string
  subTabs: Array<{ text: string; rect: DomRect4 }>
  labelTabs: Array<{ text: string; rect: DomRect4 }>
  jobLabel: string
  rows: number
}

function domReadBossListState(
  labelSelector: string, labelSelectedClass: string,
  subTabSelector: string, subTabActiveClass: string,
  jobSelector: string, rowSelector: string,
): DomListState {
  const rect = (el: Element): DomRect4 => {
    const r = el.getBoundingClientRect()
    return { x: r.x, y: r.y, w: r.width, h: r.height }
  }
  const text = (el: Element): string => (el.textContent ?? '').replace(/\s+/gu, ' ').trim()
  const labels = Array.from(document.querySelectorAll(labelSelector))
  const subTabs = Array.from(document.querySelectorAll(subTabSelector))
  const selected = labels.find((el) => el.classList.contains(labelSelectedClass))
  const active = subTabs.find((el) => el.classList.contains(subTabActiveClass))
  const job = document.querySelector(jobSelector)
  return {
    // 「新招呼(21)」这类带计数的页签只认括号前的字。
    labelSelected: selected ? text(selected).replace(/\(.*$/u, '') : '',
    subTabActive: active ? text(active) : '',
    subTabs: subTabs.map((el) => ({ text: text(el), rect: rect(el) })),
    labelTabs: labels.map((el) => ({ text: text(el).replace(/\(.*$/u, ''), rect: rect(el) })),
    jobLabel: job ? text(job) : '',
    rows: document.querySelectorAll(rowSelector).length,
  }
}

interface DomRowLocation {
  count: number
  index: number
  selected: boolean
  bubble: boolean
  bubbleText: string
  rect: DomRect4
  inViewport: boolean
}

function domLocateBossRow(
  rowSelector: string, conversationRef: string, selectedClass: string, bubbleSelector: string,
): DomRowLocation {
  const rows = Array.from(document.querySelectorAll(rowSelector))
  const matches = rows
    .map((el, index) => ({ el, index }))
    .filter(({ el }) => el.getAttribute('data-id') === conversationRef)
  const none: DomRowLocation = {
    count: matches.length, index: -1, selected: false, bubble: false, bubbleText: '',
    rect: { x: 0, y: 0, w: 0, h: 0 }, inViewport: false,
  }
  if (matches.length !== 1) return none
  const { el, index } = matches[0]!
  const r = el.getBoundingClientRect()
  const bubble = el.querySelector(bubbleSelector)
  return {
    count: 1,
    index,
    selected: el.classList.contains(selectedClass),
    bubble: !!bubble && bubble.getClientRects().length > 0,
    bubbleText: bubble ? (bubble.textContent ?? '').trim() : '',
    rect: { x: r.x, y: r.y, w: r.width, h: r.height },
    inViewport: r.width > 8 && r.height > 8 && r.left >= 0 && r.top >= 0 &&
      r.right <= window.innerWidth && r.bottom <= window.innerHeight,
  }
}

/** 落点上的元素是不是某个 selector 序列里第 index 个(或其后代)。 */
function domHitTestIndexed(
  selector: string, index: number, x: number, y: number,
): { onTarget: boolean; found: string } {
  const signature = (node: Element): string => {
    const parts: string[] = []
    let cursor: Element | null = node
    for (let depth = 0; cursor && cursor !== document.body && depth < 4; depth += 1) {
      const cls = typeof cursor.className === 'string' ? cursor.className.trim().split(/\s+/u).slice(0, 2).join('.') : ''
      parts.push(cursor.tagName.toLowerCase() + (cls ? '.' + cls : ''))
      cursor = cursor.parentElement
    }
    const r = typeof node.getBoundingClientRect === 'function' ? node.getBoundingClientRect() : { width: 0, height: 0 }
    return `${parts.join('<')}[${Math.round(r.width)}x${Math.round(r.height)}]「${(node.textContent ?? '').trim().slice(0, 8)}」`
  }
  const target = Array.from(document.querySelectorAll(selector))[index]
  const at = document.elementFromPoint(x, y)
  if (!target) return { onTarget: false, found: '靶子已经不在原来的位置上' }
  if (!at) return { onTarget: false, found: '落点上什么都没有' }
  const onTarget = at === target || target.contains(at)
  // 不命中时把遮挡物的签名带出去(类名链、尺寸、文本头几个字):它是清场白名单的唯一数据来源。
  return { onTarget, found: onTarget ? `靶子(${at.tagName.toLowerCase()})` : `遮挡物 ${signature(at)}` }
}

/** 落点上的元素是不是 data-id 为 conversationRef 的行(或其后代)。 */
function domHitTestRow(
  rowSelector: string, conversationRef: string, x: number, y: number,
): { onTarget: boolean; found: string } {
  const rows = Array.from(document.querySelectorAll(rowSelector))
    .filter((el) => el.getAttribute('data-id') === conversationRef)
  const signature = (node: Element): string => {
    const parts: string[] = []
    let cursor: Element | null = node
    for (let depth = 0; cursor && cursor !== document.body && depth < 4; depth += 1) {
      const cls = typeof cursor.className === 'string' ? cursor.className.trim().split(/\s+/u).slice(0, 2).join('.') : ''
      parts.push(cursor.tagName.toLowerCase() + (cls ? '.' + cls : ''))
      cursor = cursor.parentElement
    }
    const r = typeof node.getBoundingClientRect === 'function' ? node.getBoundingClientRect() : { width: 0, height: 0 }
    return `${parts.join('<')}[${Math.round(r.width)}x${Math.round(r.height)}]「${(node.textContent ?? '').trim().slice(0, 8)}」`
  }
  const target = rows.length === 1 ? rows[0] : undefined
  const at = document.elementFromPoint(x, y)
  if (!target) return { onTarget: false, found: `目标行不唯一(${rows.length})` }
  if (!at) return { onTarget: false, found: '落点上什么都没有' }
  const onTarget = at === target || target.contains(at)
  return { onTarget, found: onTarget ? `目标行(${at.tagName.toLowerCase()})` : `遮挡物 ${signature(at)}` }
}

/**
 * 发送前最后一道闸,在 /click 之前的最后一次读里执行(approachAndClick 的 hitTest 时点)。
 * 三件事一次注入答完:落点上是发送钮、带 selected 的行仍是目标会话、输入框里仍是
 * 我们打进去的那句话。任一不成立就 onTarget=false,不点(出口审查 B1:契约要求
 * "输入事件后且唯一标准动作前核对账号、目标、编辑器期望值")。
 */
function domSendGate(
  buttonSelector: string, x: number, y: number,
  rowSelector: string, conversationRef: string, selectedClass: string,
  composerId: string, expectedText: string,
): { onTarget: boolean; found: string } {
  const normalize = (value: string): string =>
    value.normalize('NFC').replace(/\u00a0/gu, ' ').replace(/\s+/gu, ' ').trim()
  const signature = (node: Element): string => {
    const parts: string[] = []
    let cursor: Element | null = node
    for (let depth = 0; cursor && cursor !== document.body && depth < 4; depth += 1) {
      const cls = typeof cursor.className === 'string' ? cursor.className.trim().split(/\s+/u).slice(0, 2).join('.') : ''
      parts.push(cursor.tagName.toLowerCase() + (cls ? '.' + cls : ''))
      cursor = cursor.parentElement
    }
    const r = typeof node.getBoundingClientRect === 'function' ? node.getBoundingClientRect() : { width: 0, height: 0 }
    return `${parts.join('<')}[${Math.round(r.width)}x${Math.round(r.height)}]「${(node.textContent ?? '').trim().slice(0, 8)}」`
  }
  const buttons = Array.from(document.querySelectorAll(buttonSelector))
  const button = buttons.length === 1 ? buttons[0] : undefined
  const at = document.elementFromPoint(x, y)
  const onButton = !!button && !!at && (at === button || button.contains(at))
  const selectedRows = Array.from(document.querySelectorAll(rowSelector))
    .filter((el) => el.classList.contains(selectedClass))
  const rowOk = selectedRows.length === 1 && selectedRows[0]!.getAttribute('data-id') === conversationRef
  const composer = document.getElementById(composerId)
  const composerText = composer ? (composer.textContent ?? '') : ''
  const textOk = !!composer && normalize(composerText) === normalize(expectedText)
  const problems: string[] = []
  if (!onButton) problems.push(at ? `遮挡物 ${signature(at)}` : '落点上什么都没有')
  if (!rowOk) problems.push(`当前选中行 ${selectedRows.length === 1 ? '不是目标' : `数量 ${selectedRows.length}`}`)
  if (!textOk) problems.push(composer ? `输入框内容与文案不同(${composerText.length} 字)` : '输入框不见了')
  return { onTarget: onButton && rowOk && textOk, found: problems.length ? problems.join(';') : '发送钮' }
}

function domReadBossSendButton(selector: string): { found: boolean; count: number; text: string; rect: DomRect4 } {
  const nodes = Array.from(document.querySelectorAll(selector))
  const el = nodes[0]
  if (!el || nodes.length !== 1) return { found: false, count: nodes.length, text: '', rect: { x: 0, y: 0, w: 0, h: 0 } }
  const r = el.getBoundingClientRect()
  return { found: true, count: 1, text: (el.textContent ?? '').trim(), rect: { x: r.x, y: r.y, w: r.width, h: r.height } }
}

interface DomChatRect {
  found: boolean
  rect: DomRect4
  scrollHeight: number
  clientHeight: number
  dpr: number
  innerW: number
  innerH: number
  visible: boolean
}

function domReadBossChatRect(selector: string): DomChatRect {
  const el = document.querySelector<HTMLElement>(selector)
  if (!el) {
    return { found: false, rect: { x: 0, y: 0, w: 0, h: 0 }, scrollHeight: 0, clientHeight: 0,
      dpr: window.devicePixelRatio || 1, innerW: window.innerWidth, innerH: window.innerHeight,
      visible: document.visibilityState === 'visible' }
  }
  const r = el.getBoundingClientRect()
  return {
    found: true,
    rect: { x: r.x, y: r.y, w: r.width, h: r.height },
    scrollHeight: el.scrollHeight,
    clientHeight: el.clientHeight,
    dpr: window.devicePixelRatio || 1,
    innerW: window.innerWidth,
    innerH: window.innerHeight,
    visible: document.visibilityState === 'visible',
  }
}

// ── MAIN world:内存读 ────────────────────────────────────────────────────────

interface BossListRow {
  uid: number
  friendSource: number
  name: string
  jobName: string
  newMsgCount: number
  lastTS: number | null
  lastText: string
  lastIsSelf: boolean
}

type BossListWindowRead =
  | { status: 'ready'; rows: BossListRow[]; total: number; domRows: number; candidates: number }
  | { status: 'empty'; domRows: number }
  | { status: 'missing'; domRows: number; candidates: number }

/**
 * 会话列表的数据层。按形状找:实例自有的 `$` 结尾属性或 `$props` 里,元素同时带
 * uid/friendSource/newMsgCount/encryptUid 的数组。几份候选(chat.list$、虚拟列表的
 * dataSources、allList$)里,**前缀与 DOM 行顺序逐条相同的那份**才是页面正在展示的窗口
 * (2026-09-03 实测 DOM 是数据的前缀);同时对齐的取最长。
 */
function mainReadBossListWindow(limit: number, rowSelector: string): BossListWindowRead {
  type AnyRecord = Record<string, unknown>
  const isRow = (value: unknown): value is AnyRecord => !!value && typeof value === 'object' &&
    !Array.isArray(value) && typeof (value as AnyRecord).uid === 'number' &&
    typeof (value as AnyRecord).friendSource === 'number' &&
    'newMsgCount' in (value as AnyRecord) && 'encryptUid' in (value as AnyRecord)
  const seenInstances = new Set<unknown>()
  const seenArrays = new Set<unknown>()
  const candidates: AnyRecord[][] = []
  let emptyCandidates = 0
  for (const element of Array.from(document.querySelectorAll('*'))) {
    const instance = (element as unknown as { __vue__?: AnyRecord }).__vue__
    if (!instance || seenInstances.has(instance)) continue
    seenInstances.add(instance)
    const values: unknown[] = []
    for (const name of Object.getOwnPropertyNames(instance)) {
      if (!name.endsWith('$')) continue
      try { values.push(instance[name]) } catch { /* 访问器抛错的属性跳过 */ }
    }
    const props = instance.$props
    if (props && typeof props === 'object') {
      for (const value of Object.values(props as AnyRecord)) values.push(value)
    }
    for (const value of values) {
      if (!Array.isArray(value) || seenArrays.has(value)) continue
      if (value.length === 0) { emptyCandidates += 1; continue }
      if (!isRow(value[0])) continue
      seenArrays.add(value)
      candidates.push(value as AnyRecord[])
    }
  }
  const domIds = Array.from(document.querySelectorAll(rowSelector)).map((el) => el.getAttribute('data-id') ?? '')
  if (candidates.length === 0) {
    return domIds.length === 0 && emptyCandidates > 0
      ? { status: 'empty', domRows: 0 }
      : { status: 'missing', domRows: domIds.length, candidates: 0 }
  }
  const aligned = candidates.filter((array) => {
    const n = Math.min(domIds.length, array.length)
    if (n === 0) return false
    for (let i = 0; i < n; i += 1) {
      const row = array[i]!
      if (`${row.uid}-${row.friendSource}` !== domIds[i]) return false
    }
    return true
  })
  if (aligned.length === 0) return { status: 'missing', domRows: domIds.length, candidates: candidates.length }
  const chosen = aligned.reduce((best, next) => (next.length > best.length ? next : best))
  const num = (value: unknown): number | null => (typeof value === 'number' && Number.isFinite(value) ? value : null)
  const rows: BossListRow[] = chosen.slice(0, limit).map((row) => ({
    uid: row.uid as number,
    friendSource: row.friendSource as number,
    name: String(row.name ?? ''),
    jobName: String(row.jobName ?? ''),
    newMsgCount: num(row.newMsgCount) ?? 0,
    lastTS: num(row.lastTS),
    lastText: String(row.lastText ?? ''),
    lastIsSelf: row.lastIsSelf === true,
  }))
  return { status: 'ready', rows, total: chosen.length, domRows: domIds.length, candidates: candidates.length }
}

type BossCurrentRead =
  | { status: 'ready'; uid: number; friendSource: number; name: string }
  | { status: 'none' }
  | { status: 'ambiguous'; count: number }

/**
 * 当前打开的会话。打开会话后十几个组件各持有同一个 conversation$ 对象,没打开时零持有者
 * (2026-09-03 实测)。判据用形状:对象、uid 正整数、friendSource 数字——不认组件名。
 */
function mainReadBossCurrentConversation(): BossCurrentRead {
  type AnyRecord = Record<string, unknown>
  const seen = new Set<unknown>()
  const found = new Map<string, { uid: number; friendSource: number; name: string }>()
  for (const element of Array.from(document.querySelectorAll('*'))) {
    const instance = (element as unknown as { __vue__?: AnyRecord }).__vue__
    if (!instance || seen.has(instance)) continue
    seen.add(instance)
    if (!Object.prototype.hasOwnProperty.call(instance, 'conversation$')) continue
    let conversation: unknown
    try { conversation = instance['conversation$'] } catch { continue }
    if (!conversation || typeof conversation !== 'object' || Array.isArray(conversation)) continue
    const record = conversation as AnyRecord
    const uid = record.uid
    const friendSource = record.friendSource
    if (typeof uid !== 'number' || !Number.isSafeInteger(uid) || uid <= 0) continue
    if (typeof friendSource !== 'number' || !Number.isSafeInteger(friendSource)) continue
    const key = `${uid}-${friendSource}`
    if (!found.has(key)) found.set(key, { uid, friendSource, name: String(record.name ?? record.geekName ?? '') })
  }
  if (found.size === 0) return { status: 'none' }
  if (found.size > 1) return { status: 'ambiguous', count: found.size }
  const only = [...found.values()][0]!
  return { status: 'ready', ...only }
}

/** 页面里读到的一条消息,已去掉一切非原语值;方向在页面内算好,我方 userId 不出页面。 */
export interface BossRawMessage {
  mid: string
  direction: 'in' | 'out' | 'system'
  type: string
  bizType: number | null
  bodyType: number | null
  status: number | null
  time: number | null
  text: string
  interviewCondition: number | null
  actionAid: number | null
  /** body.templateId:微信号消息恒为 5(平台事实 §十四),普通文本为 1 或缺席。 */
  templateId: number | null
  /** body.dialog.operated:请求对话卡答过没有;非 dialog 行为 null。 */
  dialogOperated: boolean | null
  /** body.dialog.buttons[].url 里的 aid 码(33 同意 / 34 拒绝);非 dialog 行为空数组。 */
  dialogAids: number[]
}

type BossThreadRead =
  | { status: 'ready'; rows: BossRawMessage[]; isToTop: boolean; peerName: string }
  | { status: 'missing' }
  | { status: 'binding_mismatch'; detail: string }
  | { status: 'identity_missing' }

/**
 * 当前会话的消息数组。持有者是 message-list 组件:自有 `list$`(元素带 mid 与 body)、
 * 自有 `isToTop`、自有 `conversation$`(2026-09-03 实测)。conversation$ 的 uid/friendSource
 * 必须等于目标——这是 BOSS 上的会话绑定核对,与读消息同一次注入,不另读。
 *
 * 方向只认 fromId === 我方 userId(平台事实 §二:type 与 flag 都不是方向);userId 在页面内
 * 读完即用,不返回。
 */
function mainReadBossThread(uid: number, friendSource: number): BossThreadRead {
  type AnyRecord = Record<string, unknown>
  const asRecord = (value: unknown): AnyRecord | null =>
    value && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const num = (value: unknown): number | null => (typeof value === 'number' && Number.isFinite(value) ? value : null)
  const seen = new Set<unknown>()
  const instances: AnyRecord[] = []
  for (const element of Array.from(document.querySelectorAll('*'))) {
    const instance = (element as unknown as { __vue__?: AnyRecord }).__vue__
    if (!instance || seen.has(instance)) continue
    seen.add(instance)
    instances.push(instance)
  }
  let myUserId: number | null = null
  for (const instance of instances) {
    let user: unknown
    try { user = instance.user$ } catch { continue }
    const record = asRecord(user)
    const raw = record?.userId
    if (typeof raw === 'number' && Number.isSafeInteger(raw) && raw > 0) { myUserId = raw; break }
  }
  if (myUserId === null) return { status: 'identity_missing' }

  let holder: AnyRecord | null = null
  let mismatch = ''
  for (const instance of instances) {
    if (!Object.prototype.hasOwnProperty.call(instance, 'list$') ||
        !Object.prototype.hasOwnProperty.call(instance, 'isToTop')) continue
    let list: unknown
    try { list = instance['list$'] } catch { continue }
    if (!Array.isArray(list)) continue
    const first = asRecord(list[0])
    if (list.length > 0 && !(first && 'mid' in first && 'body' in first)) continue
    if (typeof instance.isToTop !== 'boolean') continue
    let conversation: unknown
    try { conversation = instance['conversation$'] } catch { continue }
    const bound = asRecord(conversation)
    if (!bound) continue
    if (bound.uid !== uid || bound.friendSource !== friendSource) {
      mismatch = `list holder bound to ${typeof bound.uid === 'number' ? 'another uid' : 'no uid'}`
      continue
    }
    holder = instance
    break
  }
  if (!holder) return mismatch ? { status: 'binding_mismatch', detail: mismatch } : { status: 'missing' }
  const list = holder['list$'] as unknown[]
  const bound = asRecord(holder['conversation$'])!
  const rows: BossRawMessage[] = []
  for (const item of list) {
    const message = asRecord(item)
    if (!message) continue
    const mid = message.mid
    if (typeof mid !== 'number' && typeof mid !== 'string') continue
    const body = asRecord(message.body)
    const fromId = num(message.fromId)
    const direction: BossRawMessage['direction'] = fromId === myUserId ? 'out' : fromId === uid ? 'in' : 'system'
    const bodyText = body && typeof body.text === 'string' ? body.text : ''
    const topText = typeof message.text === 'string' ? message.text : ''
    const interview = asRecord(body?.interview)
    const action = asRecord(body?.action)
    const dialog = asRecord(body?.dialog)
    const dialogAids: number[] = []
    if (dialog && Array.isArray(dialog.buttons)) {
      for (const button of dialog.buttons as unknown[]) {
        const record = asRecord(button)
        const url = typeof record?.url === 'string' ? record.url : ''
        const match = /[?&]aid=(\d+)/u.exec(url)
        if (match) dialogAids.push(Number(match[1]))
      }
    }
    rows.push({
      mid: String(mid),
      direction,
      type: String(message.type ?? ''),
      bizType: num(message.bizType),
      bodyType: num(body?.type),
      status: num(message.status),
      time: num(message.time),
      text: bodyText || topText,
      interviewCondition: num(interview?.condition),
      actionAid: num(action?.aid),
      templateId: num(body?.templateId),
      dialogOperated: dialog ? dialog.operated === true : null,
      dialogAids,
    })
  }
  return {
    status: 'ready',
    rows,
    isToTop: holder.isToTop === true,
    peerName: String(bound.name ?? bound.geekName ?? ''),
  }
}

// ── 消息投影(纯函数,单测钉) ────────────────────────────────────────────────

export interface BossProjectedMessage {
  kind: ThreadMessage['kind']
  direction: ThreadMessage['direction']
  text: string | null
  cardType?: ThreadMessage['cardType']
  cardState?: ThreadMessage['cardState']
  /** 进 contentHash 的原文(已规范化或卡片配方),由调用方哈希。 */
  hashInput: string
  /** 未见枚举归并到保守分支时留下的原始类型,只进日志。 */
  unrecognized?: string
}

/**
 * bizType → 契约形状。枚举面按「平台枚举面事实门」只实现真机已见值(平台事实 §二、§四、§六),
 * 未见值归并为 system 并把原始类型带出——system 行不作语义证词(协议 §4.5),方向是少做。
 *
 * 邀面卡的 contentHash 按契约包 1.2(2026-09-04 落地)投常量配方:BOSS 卡上没有时间地点
 * (平台事实 §四),interview 整体省略,hash=sha256("card\x1finterviewInvite");换微信按 §4.5
 * 既有常量配方。状态分离在 cardState 上。
 */
export function projectBossMessage(raw: BossRawMessage): BossProjectedMessage {
  const text = normalizeBossMessageText(raw.text)
  const bizType = raw.bizType
  if (raw.status === 3) {
    return { kind: 'system', direction: raw.direction, text: text || '[消息已撤回]', hashInput: text || '[消息已撤回]' }
  }
  // 换微信线(平台事实 §十四,2026-09-04 真机):对方主动请求是 bizType 12 的 dialog 卡,按钮 aid 33=同意 / 34=拒绝。
  // 答过之后仍投 pending——与智联 105 请求卡同构,完成态由独立的微信号消息表达,已换成事件只触发一次(出口 §四 第 2 条)。
  if (bizType === 12 && raw.bodyType === 7 && raw.dialogAids.includes(33)) {
    return {
      kind: 'card', direction: raw.direction, text: '[交换微信请求]',
      cardType: 'wechatExchange', cardState: 'pending', hashInput: 'card\x1fwechatExchange',
    }
  }
  // 微信号消息 templateId=5:正文含号码,投影用固定文案。号只经 readWechatExchangeOutcome 的 peerWechat 出去,
  // 不进 text / evidence / 日志(AGENTS「AI provider 数据边界」)。
  if (bizType === 12 && raw.bodyType === 1 && raw.templateId === 5) {
    return {
      kind: 'card', direction: raw.direction, text: '[微信交换成功]',
      cardType: 'wechatExchange', cardState: 'accepted', hashInput: 'card\x1fwechatExchange',
    }
  }
  const plainText = bizType === 101 || bizType === 12 || (bizType === null && raw.bodyType === 1)
  if (plainText && text !== '') {
    return { kind: 'text', direction: raw.direction, text, hashInput: text }
  }
  if (bizType === 21130009 || bizType === 21130008 || bizType === 21130006) {
    const cardState: ThreadMessage['cardState'] =
      raw.interviewCondition === 1 ? 'pending'
        : raw.interviewCondition === 3 ? 'accepted'
          : raw.interviewCondition === 5 ? 'expired'
            : 'unknown'
    return {
      kind: 'card', direction: raw.direction, text: text || null,
      cardType: 'interviewInvite', cardState,
      hashInput: 'card\x1finterviewInvite',
    }
  }
  if (bizType === 21050024 && raw.actionAid === 32) {
    return {
      kind: 'card', direction: raw.direction, text: text || null,
      cardType: 'wechatExchange', cardState: 'pending',
      hashInput: 'card\x1fwechatExchange',
    }
  }
  // 信息卡一律投成 system,不投 card/other:脑把入站的未知卡片判成 unknownPlatformEvent 直接转人工
  // (communication/events.go normalizeInboundMessage),而 BOSS 每个会话开头都有一张「沟通的职位」
  // 职位卡(bizType 21050004,2026-09-03 账本实证)——投成卡片等于每个候选人都进人工。system 行只作
  // 观测(EventSystemNotice),不作语义证词。bizType 14「对方请求发送附件简历」是候选人的请求对话框,
  // 与契约 resumeAttachment「候选人已投递简历」语义不同,同样先归 system,等真机看过再定。
  const seenSystem = bizType === 21050060 || bizType === 21050070 || bizType === 21120018 ||
    bizType === 21130010 || bizType === 21050071 || bizType === 21050177 || bizType === 21050220 ||
    bizType === 21130011 || bizType === 21050004 || bizType === 14
  const label = `[系统消息:${bizType ?? raw.type ?? 'unknown'}]`
  return {
    kind: 'system', direction: raw.direction, text: text || label, hashInput: text || label,
    ...(seenSystem ? {} : { unrecognized: `bizType=${bizType ?? 'null'} type=${raw.type} bodyType=${raw.bodyType ?? 'null'}` }),
  }
}

async function projectBossThread(rows: BossRawMessage[]): Promise<Array<Omit<ThreadMessage, 'idx'> & { sourceKey: string }>> {
  const out: Array<Omit<ThreadMessage, 'idx'> & { sourceKey: string }> = []
  const unrecognized = new Set<string>()
  for (const raw of rows) {
    const projected = projectBossMessage(raw)
    if (projected.unrecognized) unrecognized.add(projected.unrecognized)
    out.push({
      sourceKey: await sha256Hex(`source-v1|${raw.mid}`),
      direction: projected.direction,
      kind: projected.kind,
      text: projected.text,
      blobRef: null,
      contentHash: await sha256Hex(projected.hashInput),
      ...(projected.cardType ? { cardType: projected.cardType } : {}),
      ...(projected.cardState ? { cardState: projected.cardState } : {}),
      ...(raw.time !== null && raw.time > 0 ? { tsApprox: raw.time } : {}),
    })
  }
  for (const item of [...unrecognized].sort()) {
    console.info('[RecruitHelper] boss_unrecognized_message_type', item)
  }
  return out
}

/** 锚尾在窗口内的连续匹配。只有唯一命中才裁掉锚前上下文(协议 §12 第 4 条)。 */
export function matchAnchorTail(
  messages: ReadonlyArray<{ direction: string; contentHash: string }>,
  anchors: ReadonlyArray<MessageAnchor>,
): { count: number; start: number | null } {
  if (anchors.length === 0 || anchors.length > messages.length) return { count: 0, start: null }
  const starts: number[] = []
  for (let start = 0; start + anchors.length <= messages.length; start += 1) {
    let matched = true
    for (let offset = 0; offset < anchors.length; offset += 1) {
      const message = messages[start + offset]!
      const anchor = anchors[offset]!
      if (message.direction !== anchor.direction || message.contentHash !== anchor.contentHash) {
        matched = false
        break
      }
    }
    if (matched) starts.push(start)
  }
  return { count: starts.length, start: starts.length === 1 ? starts[0]! : null }
}

/** lastTS 的单位:实测是毫秒(被舍到整秒),但为防换单位,小于 1e11 的按秒处理。 */
function activityMs(value: number | null): number | null {
  if (value === null || value <= 0) return null
  return value < 1e11 ? Math.round(value * 1000) : Math.round(value)
}

function previewOf(text: string): string {
  const cleaned = normalizeBossMessageText(text)
  return Array.from(cleaned).slice(0, 200).join('')
}

export function summarizeBossListRow(row: BossListRow): ConversationSummary {
  const preview = previewOf(row.lastText)
  return {
    conversationRef: bossConversationRef(row.uid, row.friendSource),
    lastActivityTs: activityMs(row.lastTS),
    lastMessage: {
      direction: row.lastIsSelf ? 'out' : 'in',
      kind: preview === '' ? 'system' : 'text',
      textPreview: preview,
    },
    peer: {
      displayName: normalizeBossMessageText(row.name) || '未命名',
      platformUserRef: String(row.uid),
    },
    unreadCount: Math.max(0, row.newMsgCount),
    ...(normalizeBossMessageText(row.jobName) ? { positionTitle: normalizeBossMessageText(row.jobName) } : {}),
  }
}


// ── 清场:白名单关闭 + 通用识别留痕(2026-09-03 甲方裁决) ──────────────────────
//
// 关闭动作只走白名单:每一项都是真机见过、语义确认为营销位或引导的容器(平台事实 §十三)。
// 没见过的叉号一律不点——没见过的控件里就有业务动作(「不合适」长得也像个叉),误关营销位
// 代价为零,误点业务动作是错靶。识别面反过来是通用的:命中测试被遮时把遮挡物签名写进
// 手侧日志(clickTargetCovered)与错误 detail,人看一眼是营销位就加一行。
//
// 它不是原语:是 readList(move=reset) 起手的一趟尽力而为,每项至多点一次、可见才点,
// 失败只留痕不拦;与智联的 dismissGlobalPromoModalsBestEffort 同一位置、同一纪律。

interface BossDismissEntry {
  readonly label: string
  /** 容器选择器:可见才算"在"。 */
  readonly container: string
  /** 关闭控件选择器(容器内)。 */
  readonly closer: string
}

export const BOSS_DISMISS_WHITELIST: readonly BossDismissEntry[] = [
  { label: '列表顶营销卡', container: '.batch-chat-intention', closer: '.close' },
  { label: '左下客户端下载横幅', container: '.c-menu-bottom-ad', closer: '.ad-banner-close' },
  { label: '右栏意向沟通引导气泡', container: '.guide-intention .dialog-wrap', closer: '.iboss-close' },
]

interface DomOverlayState {
  index: number
  label: string
  visible: boolean
  closerRect: DomRect4
  closerInViewport: boolean
}

/** 白名单各项现在在不在、关闭控件在哪。只读。 */
function domReadBossOverlays(entries: Array<{ label: string; container: string; closer: string }>): DomOverlayState[] {
  const visible = (el: Element): boolean => {
    const r = el.getBoundingClientRect()
    const style = getComputedStyle(el)
    return r.width > 0 && r.height > 0 && style.display !== 'none' && style.visibility !== 'hidden'
  }
  return entries.map((entry, index) => {
    const containers = Array.from(document.querySelectorAll(entry.container)).filter(visible)
    const container = containers.length === 1 ? containers[0]! : undefined
    const closers = container ? Array.from(container.querySelectorAll(entry.closer)).filter(visible) : []
    const closer = closers[0]
    if (!container || !closer) {
      return { index, label: entry.label, visible: false, closerRect: { x: 0, y: 0, w: 0, h: 0 }, closerInViewport: false }
    }
    const r = closer.getBoundingClientRect()
    return {
      index, label: entry.label, visible: true,
      closerRect: { x: r.x, y: r.y, w: r.width, h: r.height },
      closerInViewport: r.width > 4 && r.height > 4 && r.left >= 0 && r.top >= 0 &&
        r.right <= window.innerWidth && r.bottom <= window.innerHeight,
    }
  })
}

function domHitTestOverlayCloser(
  container: string, closer: string, x: number, y: number,
): { onTarget: boolean; found: string } {
  const containers = Array.from(document.querySelectorAll(container))
  const root = containers.length === 1 ? containers[0]! : undefined
  const target = root ? root.querySelector(closer) : null
  const at = document.elementFromPoint(x, y)
  if (!target) return { onTarget: false, found: '关闭控件已经不在了' }
  if (!at) return { onTarget: false, found: '落点上什么都没有' }
  const onTarget = at === target || target.contains(at)
  return { onTarget, found: onTarget ? '关闭控件' : `${at.tagName.toLowerCase()}「${(at.textContent ?? '').trim().slice(0, 8)}」` }
}

/**
 * 白名单清场,尽力而为。每项至多一次 OS 点击;点完条件等待容器消失(封顶 3s,诊断读不是放行判据);
 * 关掉记 warn、关不掉记 error,都带签名。任何 PlatformError 只留痕不抛;StopExecution 原样穿过。
 */
async function dismissBossOverlaysBestEffort(tab: chrome.tabs.Tab, ctx: PrimitiveContext): Promise<void> {
  const tabId = tab.id!
  const entries = BOSS_DISMISS_WHITELIST as Array<{ label: string; container: string; closer: string }>
  let states: DomOverlayState[]
  try {
    states = await runInPage(BOSS_DOM, tabId, domReadBossOverlays, [entries])
  } catch (error) {
    if (!(error instanceof PlatformError)) throw error
    return
  }
  for (const state of states) {
    if (!state.visible) continue
    ctx.checkpoint()
    const entry = BOSS_DISMISS_WHITELIST[state.index]!
    if (!state.closerInViewport) {
      reportHandLog('warn', 'promoModalDismissSkipped', `BOSS 清场:「${entry.label}」在但关闭控件不在视口内,不点`)
      continue
    }
    const plan: ClickPlan = {
      label: `清场「${entry.label}」`,
      rect: state.closerRect,
      hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestOverlayCloser, [entry.container, entry.closer, x, y]),
      observe: async (): Promise<ClickObservation> => {
        const after = await runInPage(BOSS_DOM, tabId, domReadBossOverlays, [entries])
        return { trusted: null, onTarget: null, eventDriftPx: null, after: `容器${after[state.index]?.visible ? '仍在' : '已消失'}` }
      },
    }
    try {
      const probe = await runOsProbe(BOSS_INJECT, tabId, ctx, plan)
      if (probe.outcome !== 'clicked') {
        reportHandLog('warn', 'promoModalDismissRefused', `BOSS 清场:「${entry.label}」未点击(${probe.outcome})`, probe.detail)
        continue
      }
      const settled = await pollUntil(ctx,
        () => runInPage(BOSS_DOM, tabId, domReadBossOverlays, [entries]),
        (after) => !after[state.index]?.visible, 3_000)
      if (settled.satisfied) {
        reportHandLog('warn', 'promoModalDismissed', `BOSS 清场:已关闭「${entry.label}」`)
      } else {
        reportHandLog('error', 'promoModalDismissFailed', `BOSS 清场:点了「${entry.label}」但容器仍在`, probe.detail)
      }
    } catch (error) {
      if (!(error instanceof PlatformError)) throw error
      reportHandLog('error', 'promoModalDismissFailed', `BOSS 清场:「${entry.label}」处理异常`, error.message)
    }
  }
}

// ── 考古探针:任意 selector 的定位与滚动指标(isolated world)────────────────────
//
// 这两个页面函数只服务 debug.osScroll / debug.osClick——selector 由调用方给,是 debug.*
// 命名空间的显式例外(契约 note)。生产原语的定位各有自己的常量与页面函数,不走这里。

type DomLocated =
  | { status: 'ok'; count: number; index: number; rect: DomRect4; clip: DomRect4; text: string; signature: string }
  | { status: 'bad_selector' | 'none' | 'ambiguous' | 'out_of_range' | 'offscreen'; count: number; detail: string }

/**
 * 按 selector(+index)定位一个元素。index<0 表示"没给":命中不唯一即拒,不猜第一个。
 * 返回它的整矩形与**可见部分**矩形(与视口相交);可见部分太小算 offscreen,光标没处落。
 */
function domLocateBySelector(selector: string, index: number): DomLocated {
  let all: Element[]
  try {
    all = Array.from(document.querySelectorAll(selector))
  } catch (error) {
    return { status: 'bad_selector', count: 0, detail: `选择器无效:${String(error).slice(0, 120)}` }
  }
  if (all.length === 0) return { status: 'none', count: 0, detail: '选择器没有命中任何元素' }
  if (index < 0 && all.length > 1) {
    return { status: 'ambiguous', count: all.length, detail: `命中 ${all.length} 个元素,请给 index` }
  }
  const i = index < 0 ? 0 : index
  const el = all[i]
  if (!el) return { status: 'out_of_range', count: all.length, detail: `index ${i} 越界,只命中 ${all.length} 个` }
  const r = el.getBoundingClientRect()
  const left = Math.max(0, r.left)
  const top = Math.max(0, r.top)
  const right = Math.min(window.innerWidth, r.right)
  const bottom = Math.min(window.innerHeight, r.bottom)
  const clip = { x: left, y: top, w: Math.max(0, right - left), h: Math.max(0, bottom - top) }
  const parts: string[] = []
  let cursor: Element | null = el
  for (let depth = 0; cursor && cursor !== document.body && depth < 4; depth += 1) {
    const cls = typeof cursor.className === 'string' ? cursor.className.trim().split(/\s+/u).slice(0, 2).join('.') : ''
    parts.push(cursor.tagName.toLowerCase() + (cls ? '.' + cls : ''))
    cursor = cursor.parentElement
  }
  const text = (el.textContent ?? '').trim()
  const signature = `${parts.join('<')}[${Math.round(r.width)}x${Math.round(r.height)}]「${text.slice(0, 8)}」`
  if (!(clip.w >= 24 && clip.h >= 24)) {
    return { status: 'offscreen', count: all.length, detail: `${signature} 可见部分只有 ${Math.round(clip.w)}x${Math.round(clip.h)},光标没处落` }
  }
  return { status: 'ok', count: all.length, index: i, rect: { x: r.x, y: r.y, w: r.width, h: r.height }, clip, text, signature }
}

/** 容器的滚动指标。找不到就 found=false,不抛。 */
function domReadScrollMetrics(selector: string, index: number): { found: boolean; scrollTop: number; scrollHeight: number; clientHeight: number } {
  let el: Element | undefined
  try {
    el = Array.from(document.querySelectorAll(selector))[index]
  } catch {
    el = undefined
  }
  if (!el) return { found: false, scrollTop: 0, scrollHeight: 0, clientHeight: 0 }
  return { found: true, scrollTop: el.scrollTop, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight }
}

/**
 * 考古点击的命中测试:落点上是 selector[index] 或其后代,**且**给了 expectText 时元素此刻的
 * 文本仍逐字相等——这是点击前的最后一次读。列表重排后同一个 index 可能指到另一行,
 * 只看 index 会点错人;文本是那一行的身份。
 */
function domHitTestExpected(
  selector: string, index: number, expectText: string | null, x: number, y: number,
): { onTarget: boolean; found: string } {
  let target: Element | undefined
  try {
    target = Array.from(document.querySelectorAll(selector))[index]
  } catch {
    target = undefined
  }
  const at = document.elementFromPoint(x, y)
  if (!target) return { onTarget: false, found: '靶子已经不在原来的位置上' }
  if (!at) return { onTarget: false, found: '落点上什么都没有' }
  if (!(at === target || target.contains(at))) {
    return { onTarget: false, found: `落点上是别的元素 ${at.tagName.toLowerCase()}「${(at.textContent ?? '').trim().slice(0, 8)}」` }
  }
  if (expectText !== null) {
    const now = (target.textContent ?? '').trim()
    if (now !== expectText) return { onTarget: false, found: `靶子文本已变:「${now.slice(0, 16)}」≠「${expectText.slice(0, 16)}」` }
  }
  return { onTarget: true, found: `靶子(${at.tagName.toLowerCase()})` }
}

async function bossOsClick(
  args: DebugOsClickArgs,
  ctx: PrimitiveContext,
  fingerprint: string | undefined,
): Promise<DebugOsClickData> {
  const tab = await verifiedBossTab(fingerprint)
  const tabId = tab.id!
  const started = Date.now()
  const refused = (detail: string): DebugOsClickData => osClickContractData(args.mode, {
    outcome: 'refusedByGate', attempts: 0, calibStatus: '未知', unreachableFrames: 0, planMs: 0,
    elapsedMs: Date.now() - started, lagMaxUs: 0, detail,
  }, Date.now())
  // 靶子在**移动之前**就要定位好:定不到、不唯一、看不见、文本不符,一步都不动。
  const located = await runInPage(BOSS_DOM, tabId, domLocateBySelector, [args.selector, args.index ?? -1])
  if (located.status !== 'ok') return refused(`靶子定位失败(${located.status}):${located.detail}`)
  const expectText = args.expectText === undefined ? null : args.expectText.trim()
  if (expectText !== null && located.text !== expectText) {
    return refused(`靶子文本不符:页面是「${located.text.slice(0, 16)}」,期望「${expectText.slice(0, 16)}」(${located.signature})`)
  }
  const plan: ClickPlan = {
    label: located.signature,
    rect: located.clip,
    action: args.mode === 'move' ? 'land' : 'click',
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestExpected, [args.selector, located.index, expectText, x, y]),
    observe: async (): Promise<ClickObservation> => {
      const after = await runInPage(BOSS_DOM, tabId, domLocateBySelector, [args.selector, located.index])
      return { trusted: null, onTarget: null, eventDriftPx: null,
        after: after.status === 'ok' ? `靶子仍在:${after.signature}` : `靶子已不在(${after.status})` }
    },
  }
  const probe = await runOsProbe(BOSS_INJECT, tabId, ctx, plan)
  return osClickContractData(args.mode, probe, Date.now())
}

async function bossOsScroll(
  args: DebugOsScrollArgs,
  ctx: PrimitiveContext,
  fingerprint: string | undefined,
): Promise<DebugOsScrollData> {
  const tab = await verifiedBossTab(fingerprint)
  const tabId = tab.id!
  const started = Date.now()
  const zero = { scrollTopBefore: 0, scrollTopAfter: 0, scrolledPx: 0, ticks: 0, bursts: 0, attempts: 0, lagMaxUs: 0 }
  // 定位在**移动之前**:定不到、不唯一、看不见,一步都不动。
  const located = await runInPage(BOSS_DOM, tabId, domLocateBySelector, [args.selector, args.index ?? -1])
  if (located.status !== 'ok') {
    return osScrollContractData({ ...zero, outcome: 'refusedByGate', elapsedMs: Date.now() - started,
      detail: `容器定位失败(${located.status}):${located.detail}` }, Date.now())
  }
  const metrics = await runInPage(BOSS_DOM, tabId, domReadScrollMetrics, [args.selector, located.index])
  if (metrics.scrollHeight <= metrics.clientHeight + 1) {
    return osScrollContractData({ ...zero, outcome: 'edge', scrollTopBefore: metrics.scrollTop, scrollTopAfter: metrics.scrollTop,
      elapsedMs: Date.now() - started,
      detail: `${located.signature} 没有可滚内容(scrollHeight ${metrics.scrollHeight} ≤ clientHeight ${metrics.clientHeight}),一格没滚` }, Date.now())
  }
  const target: ScrollTarget = {
    label: `容器 ${located.signature}`,
    rect: located.clip,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestIndexed, [args.selector, located.index, x, y]),
    readMetrics: async () => {
      const m = await runInPage(BOSS_DOM, tabId, domReadScrollMetrics, [args.selector, located.index])
      return m.found ? { scrollTop: m.scrollTop, scrollHeight: m.scrollHeight, clientHeight: m.clientHeight } : null
    },
  }
  const res = await runOsScroll(BOSS_INJECT, tabId, ctx, target, args.direction, args.distancePx)
  return osScrollContractData(res, Date.now())
}

// ── OS 点击的编排 ─────────────────────────────────────────────────────────────

/** 点一下,不成就按闸的原因失败。至多一次点击是 runOsProbe 的内核,这里不重试。 */
async function osClickOnce(tabId: number, ctx: PrimitiveContext, plan: ClickPlan, what: string): Promise<string> {
  const probe = await runOsProbe(BOSS_INJECT, tabId, ctx, plan)
  if (probe.outcome === 'clicked') return probe.detail ?? ''
  if (probe.outcome === 'handServiceUnavailable') {
    throw new PlatformError('CTX_NOT_READY', `${what}:手服务不可用(${probe.detail ?? ''})`, 'afterRecovery', 'pageBroken')
  }
  if (probe.detail && probe.detail.includes('遮挡物')) {
    // 通用识别、白名单关闭:遮挡物的签名在 detail 里,人看一眼是营销位就加进 BOSS_DISMISS_WHITELIST。
    reportHandLog('warn', 'clickTargetCovered', `BOSS ${what}:靶子被遮,未点`, probe.detail)
  }
  throw new PlatformError('ELEMENT_UNRESOLVED', `${what}未点击:${probe.detail ?? probe.outcome}`, 'afterRecovery')
}

async function rowClickPlan(tabId: number, conversationRef: string): Promise<ClickPlan> {
  const located = await runInPage(BOSS_DOM, tabId, domLocateBossRow,
    [ROW_SELECTOR, conversationRef, ROW_SELECTED_CLASS, ROW_BUBBLE_SELECTOR])
  if (located.count === 0) throw new PlatformError('TARGET_NOT_FOUND', '目标会话不在当前列表里', 'no')
  if (located.count > 1) throw new PlatformError('ELEMENT_UNRESOLVED', '目标会话在列表里不唯一', 'manualOnly')
  if (!located.inViewport) {
    // BOSS 上没有滚动注入:行不在视口里就点不到。失效方向是不点,由下轮再来。
    throw new PlatformError('ELEMENT_UNRESOLVED', '目标行不在视口内,BOSS 尚无滚动注入', 'afterRecovery')
  }
  return {
    label: `会话行(点前 选中=${located.selected ? '是' : '否'} 气泡=${located.bubbleText || '无'})`,
    rect: located.rect,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestRow, [ROW_SELECTOR, conversationRef, x, y]),
    observe: async (): Promise<ClickObservation> => {
      const after = await runInPage(BOSS_DOM, tabId, domLocateBossRow,
        [ROW_SELECTOR, conversationRef, ROW_SELECTED_CLASS, ROW_BUBBLE_SELECTOR])
      return { trusted: null, onTarget: null, eventDriftPx: null,
        after: `行=${after.count} 选中=${after.selected ? '是' : '否'} 气泡=${after.bubble ? after.bubbleText : '无'}` }
    },
  }
}

async function textTargetClickPlan(
  tabId: number, selector: string, text: string, what: string,
): Promise<ClickPlan> {
  const state = await runInPage(BOSS_DOM, tabId, domReadBossListState,
    [LABEL_TAB_SELECTOR, LABEL_TAB_SELECTED_CLASS, SUB_TAB_SELECTOR, SUB_TAB_ACTIVE_CLASS, JOB_LABEL_SELECTOR, ROW_SELECTOR])
  const items = selector === SUB_TAB_SELECTOR ? state.subTabs : state.labelTabs
  const index = items.findIndex((item) => item.text === text)
  const matches = items.filter((item) => item.text === text).length
  if (index < 0 || matches !== 1) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `${what}「${text}」认不出(候选 ${items.map((i) => i.text).join('/')})`, 'afterRecovery')
  }
  const rect = items[index]!.rect
  if (!(rect.w > 8) || !(rect.h > 8)) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `${what}「${text}」尺寸异常`, 'afterRecovery')
  }
  return {
    label: `${what}「${text}」`,
    rect,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestIndexed, [selector, index, x, y]),
    observe: async (): Promise<ClickObservation> => {
      const after = await runInPage(BOSS_DOM, tabId, domReadBossListState,
        [LABEL_TAB_SELECTOR, LABEL_TAB_SELECTED_CLASS, SUB_TAB_SELECTOR, SUB_TAB_ACTIVE_CLASS, JOB_LABEL_SELECTOR, ROW_SELECTOR])
      return { trusted: null, onTarget: null, eventDriftPx: null,
        after: `页签=${after.labelSelected} 小页签=${after.subTabActive} 行=${after.rows}` }
    },
  }
}

async function readListState(tabId: number): Promise<DomListState> {
  return runInPage(BOSS_DOM, tabId, domReadBossListState,
    [LABEL_TAB_SELECTOR, LABEL_TAB_SELECTED_CLASS, SUB_TAB_SELECTOR, SUB_TAB_ACTIVE_CLASS, JOB_LABEL_SELECTOR, ROW_SELECTOR])
}

/** 取一个身份已核对、且停在沟通页的标签页。 */
async function verifiedBossChatTab(fingerprint: string | undefined): Promise<chrome.tabs.Tab> {
  const tab = await verifiedBossTab(fingerprint)
  if (!tab.url || bossSite.pageKind(tab.url) !== 'im') {
    throw new PlatformError('CTX_NOT_READY', '请在 Chrome 中打开 BOSS 沟通页', 'afterRecovery', 'pageAbsent')
  }
  return tab
}

// ── chat.readUnreadTotal ────────────────────────────────────────────────────

async function readBossUnreadTotal(fingerprint: string | undefined): Promise<ChatReadUnreadTotalData> {
  const tab = await verifiedBossTab(fingerprint)
  const badge = await runInPage(BOSS_DOM, tab.id!, domReadBossUnreadBadge, [BOSS_UNREAD_BADGE_SELECTOR])
  if (badge.count > 1) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `侧栏未读角标不唯一(${badge.count})`, 'afterRecovery')
  }
  return { total: badge.found ? parseBossUnreadBadgeText(badge.text) : null, observedAt: Date.now() }
}

// ── chat.identifyCurrentConversation ────────────────────────────────────────

async function identifyBossCurrentConversation(fingerprint: string | undefined): Promise<ChatIdentifyCurrentConversationData> {
  const tab = await verifiedBossChatTab(fingerprint)
  const readCurrent = async (): Promise<string> => {
    const current = await runInPage(BOSS_INJECT, tab.id!, mainReadBossCurrentConversation, [])
    if (current.status === 'none') {
      throw new PlatformError('ELEMENT_UNRESOLVED', '当前没有打开的会话', 'manualOnly')
    }
    if (current.status === 'ambiguous') {
      throw new PlatformError('ELEMENT_UNRESOLVED', `页面同时呈现 ${current.count} 个当前会话`, 'manualOnly')
    }
    return bossConversationRef(current.uid, current.friendSource)
  }
  const before = await readCurrent()
  const after = await readCurrent()
  if (before !== after) {
    throw new PlatformError('USER_ACTIVE', '识别期间当前会话被切换', 'afterRecovery')
  }
  return { conversationRef: after, observedAt: Date.now() }
}

// ── chat.readList ───────────────────────────────────────────────────────────

/**
 * 把列表页面切到「全部」页签 + 目标小页签,并回读确认。职位范围只回读不切换:
 * 那是一个下拉两次点击,选项 DOM 没考古,不是就转人工。
 */
async function ensureBossListFilter(
  tab: chrome.tabs.Tab, ctx: PrimitiveContext, fingerprint: string, wantUnread: boolean,
): Promise<DomListState> {
  const tabId = tab.id!
  const wantSubTab = wantUnread ? SUB_TAB_UNREAD : SUB_TAB_ALL
  let state = await readListState(tabId)
  if (state.jobLabel !== JOB_ALL) {
    throw new PlatformError('GUARD_FAILED', `职位范围不是「${JOB_ALL}」(当前「${state.jobLabel || '空'}」),请人工切回`, 'manualOnly')
  }
  if (state.labelSelected !== LABEL_ALL) {
    ctx.checkpoint()
    await ctx.beforeSideEffect()
    await osClickOnce(tabId, ctx, await textTargetClickPlan(tabId, LABEL_TAB_SELECTOR, LABEL_ALL, '列表页签'), '切换列表页签')
    const settled = await pollUntil(ctx, () => readListState(tabId), (s) => s.labelSelected === LABEL_ALL)
    state = settled.value
    if (!settled.satisfied) {
      throw new PlatformError('ELEMENT_UNRESOLVED', `列表页签未回读为「${LABEL_ALL}」(最后读到「${state.labelSelected}」)`, 'afterRecovery')
    }
    await verifiedBossChatTab(fingerprint)
  }
  if (state.subTabActive !== wantSubTab) {
    ctx.checkpoint()
    await ctx.beforeSideEffect()
    await osClickOnce(tabId, ctx, await textTargetClickPlan(tabId, SUB_TAB_SELECTOR, wantSubTab, '未读小页签'), '切换未读小页签')
    const settled = await pollUntil(ctx, () => readListState(tabId), (s) => s.subTabActive === wantSubTab)
    state = settled.value
    if (!settled.satisfied) {
      throw new PlatformError('ELEMENT_UNRESOLVED', `未读小页签未回读为「${wantSubTab}」(最后读到「${state.subTabActive}」)`, 'afterRecovery')
    }
    await verifiedBossChatTab(fingerprint)
  }
  return state
}

async function readBossListWindow(tab: chrome.tabs.Tab, ctx: PrimitiveContext): Promise<BossListWindowRead> {
  // 列表切换后数据要等页面自己填好;空态要连续两次读到才信(渲染暂态)。
  let previous: BossListWindowRead | null = null
  const settled = await pollUntil(ctx,
    () => runInPage(BOSS_INJECT, tab.id!, mainReadBossListWindow, [LIST_WINDOW_MAX, ROW_SELECTOR]),
    (read) => {
      const stable = read.status === 'ready' || (read.status === 'empty' && previous?.status === 'empty')
      previous = read
      return stable
    })
  return settled.value
}

async function readBossList(
  args: ChatReadListArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<ChatReadListData> {
  if (validatePrimitiveArgs(PrimitiveName.ChatReadList, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '会话列表读取参数不符合当前契约', 'manualOnly')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  const tab = await verifiedBossChatTab(fingerprint)
  if (args.move === 'next') {
    // 窗口以数据层前 32 条为界,再往下要滚动列表——BOSS 上尚无滚轮注入。
    throw new PlatformError('ELEMENT_UNRESOLVED', 'BOSS 尚未实现列表滚动,move=next 不可用', 'no')
  }
  const wantUnread = args.filter === 'unread'
  ctx.checkpoint()
  // 每轮第一条命令起手清一趟白名单里的营销位与引导(尽力而为,失败只留痕)。
  await dismissBossOverlaysBestEffort(tab, ctx)
  await verifiedBossChatTab(fingerprint)
  await ensureBossListFilter(tab, ctx, fingerprint, wantUnread)
  // 「未读」小页签是服务端查询:类名同步翻、数据异步回(平台事实 §十二)。类名翻了之后
  // 数据层可能还是「全部」那份,所以按条件等待"窗内全部行未读数>0"(封顶 20s);超时按
  // 规格 §12.6「零值不得单独导致整窗失败,也不得由手静默过滤」照常返回并留痕。
  const allUnread = (read: BossListWindowRead): boolean =>
    read.status !== 'ready' || read.rows.every((row) => row.newMsgCount > 0)
  const settled = wantUnread
    ? await pollUntil(ctx, () => readBossListWindow(tab, ctx), allUnread)
    : { value: await readBossListWindow(tab, ctx), satisfied: true }
  const read = settled.value
  if (read.status === 'missing') {
    throw new PlatformError('ELEMENT_UNRESOLVED',
      `会话列表数据层读不到(DOM 行 ${read.domRows},候选数组 ${read.candidates})`, 'afterRecovery')
  }
  const rows = read.status === 'ready' ? read.rows : []
  const total = read.status === 'ready' ? read.total : 0
  if (wantUnread && !settled.satisfied) {
    const zero = rows.filter((row) => row.newMsgCount <= 0).length
    reportHandLog('warn', 'unreadWindowHasReadRows',
      `chat.readList(filter=unread) 窗内 ${zero}/${rows.length} 行未读数为 0,等待 ${READY_WAIT_MS}ms 未收敛,照常返回`)
  }
  const cutoffMs = args.filter === 'all'
    ? Date.now() - Math.min(30, Math.max(1, args.stopOlderThanDays ?? 8)) * 86_400_000
    : null
  let stale = 0
  const sessions = rows
    .map(summarizeBossListRow)
    .filter((session) => {
      if (cutoffMs !== null && session.lastActivityTs !== null && session.lastActivityTs < cutoffMs) {
        stale += 1
        return false
      }
      return true
    })
  const crossedCutoff = stale > 0 && sessions.length === 0
  const data: ChatReadListData = { sessions, complete: total <= LIST_WINDOW_MAX || crossedCutoff }
  if (validatePrimitiveData(PrimitiveName.ChatReadList, 1, data).length !== 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '会话列表结果不符合当前契约', 'manualOnly')
  }
  if (jsonBytes(data) > RESULT_DATA_BUDGET) {
    throw new PlatformError('PAYLOAD_LIMIT', '会话窗口超过内联载荷上限')
  }
  await verifiedBossChatTab(fingerprint)
  ctx.progress(`BOSS 会话列表读取完成(${sessions.length}/${total},${wantUnread ? '未读' : '全部'})`, 100)
  return data
}

// ── chat.openConversation ───────────────────────────────────────────────────

async function locateRow(tabId: number, conversationRef: string): Promise<DomRowLocation> {
  return runInPage(BOSS_DOM, tabId, domLocateBossRow, [ROW_SELECTOR, conversationRef, ROW_SELECTED_CLASS, ROW_BUBBLE_SELECTOR])
}

/**
 * 把目标会话点开(已开就不点)。返回有没有真点。后置只看 DOM:目标行带 selected
 * (2026-09-03 实测三层后置里 DOM 这一层);未读气泡消失由 openConversation 自己再看。
 */
/**
 * 把目标会话点到前台。armSideEffect=false 供 effectful 原语(sendMessage)用:那条命令的
 * beforeSideEffect 必须紧贴发送键那一次点击(证词 attempting 只能写一次),打开会话这一下
 * 是列表行点击、无候选人可见副作用,不能提前消耗它。
 */
async function ensureBossThreadOpen(
  tab: chrome.tabs.Tab, ctx: PrimitiveContext, fingerprint: string, conversationRef: string,
  armSideEffect = true,
): Promise<boolean> {
  const tabId = tab.id!
  const before = await locateRow(tabId, conversationRef)
  if (before.count === 1 && before.selected) return false
  const plan = await rowClickPlan(tabId, conversationRef)
  ctx.checkpoint()
  // 上一条命令可能刚点过页签;相邻可见交互再留 1s+抖动(runOsProbe 靠近阶段另有一次)。
  await sleep(1_000 + Math.floor(Math.random() * 401))
  await verifiedBossChatTab(fingerprint)
  if (armSideEffect) await ctx.beforeSideEffect()
  await osClickOnce(tabId, ctx, plan, '打开会话')
  const settled = await pollUntil(ctx, () => locateRow(tabId, conversationRef),
    (row) => row.count === 1 && row.selected)
  if (!settled.satisfied) {
    throw new PlatformError('CTX_LOST_DURING_EXEC',
      `点击后目标行未成为当前会话(最后读到 行=${settled.value.count} 选中=${settled.value.selected})`,
      'afterRecovery', undefined, 'possible')
  }
  return true
}

async function openBossConversation(
  args: ChatOpenConversationArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<ChatOpenConversationData> {
  if (validatePrimitiveArgs(PrimitiveName.ChatOpenConversation, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '打开会话参数不符合当前契约', 'manualOnly')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  if (!parseBossConversationRef(args.conversationRef)) {
    throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'manualOnly')
  }
  const tab = await verifiedBossChatTab(fingerprint)
  const tabId = tab.id!
  // 契约:只在当前已回读为「全部职位+未读」的 fresh 列表里打开。
  const state = await readListState(tabId)
  if (state.jobLabel !== JOB_ALL || state.labelSelected !== LABEL_ALL || state.subTabActive !== SUB_TAB_UNREAD) {
    throw new PlatformError('GUARD_FAILED',
      `当前列表不是「全部职位+未读」(职位「${state.jobLabel}」页签「${state.labelSelected}」小页签「${state.subTabActive}」)`,
      'manualOnly')
  }
  const performedClick = await ensureBossThreadOpen(tab, ctx, fingerprint, args.conversationRef)
  // 后置:行带 selected 且气泡消失;或行已离开未读列表。要连续两轮读到。
  let positive = 0
  let last = ''
  const settled = await pollUntil(ctx, () => locateRow(tabId, args.conversationRef), (row) => {
    const ok = (row.count === 1 && row.selected && !row.bubble) || row.count === 0
    positive = ok ? positive + 1 : 0
    last = `行=${row.count} 选中=${row.selected} 气泡=${row.bubble ? row.bubbleText : '无'}`
    return positive >= 2
  })
  if (!settled.satisfied) {
    throw new PlatformError('POSTCONDITION_UNCONFIRMED',
      `目标只打开一次,但未确认未读标记清除或会话行离开未读列表(${last})`,
      'manualOnly', undefined, performedClick ? 'possible' : 'none')
  }
  await verifiedBossChatTab(fingerprint)
  const data: ChatOpenConversationData = { conversationRef: args.conversationRef, observedAt: Date.now() }
  ctx.progress('目标未读会话已打开并确认已读收敛', 100)
  return data
}

// ── chat.readThread ─────────────────────────────────────────────────────────

/**
 * 消息数组的就绪判据:绑定到目标且至少读到一行。点开会话的瞬间 message-list 先以空数组挂到
 * 新会话上、历史要再等一个来回(2026-09-03 Mac 真机:点击后 4 秒读到 0 行,对方名也还是空),
 * 空数组不算就绪。能出现在列表里的会话至少有一条消息,等到封顶仍空就照实交出去——脑侧按
 * 空快照瞬时跳过、下轮再读,不在这里猜。读不到我方身份则立即收束,等也等不来。
 */
function bossThreadReadSettled(read: BossThreadRead): boolean {
  return (read.status === 'ready' && read.rows.length > 0) || read.status === 'identity_missing'
}

async function readBossThreadRows(
  tab: chrome.tabs.Tab, ctx: PrimitiveContext, uid: number, friendSource: number,
): Promise<Extract<BossThreadRead, { status: 'ready' }>> {
  const settled = await pollUntil(ctx,
    () => runInPage(BOSS_INJECT, tab.id!, mainReadBossThread, [uid, friendSource]),
    bossThreadReadSettled)
  const read = settled.value
  if (read.status === 'ready') {
    if (!settled.satisfied) {
      reportHandLog('warn', 'threadListEmptyAfterWait',
        `BOSS 消息数组等到封顶仍为空,照实交出 0 行(isToTop=${read.isToTop})`)
    }
    return read
  }
  if (read.status === 'identity_missing') {
    throw new PlatformError('CTX_NOT_READY', '页面上读不到我方账号身份,无法判定消息方向', 'afterRecovery', 'identityUnverified')
  }
  if (read.status === 'binding_mismatch') {
    throw new PlatformError('CTX_LOST_DURING_EXEC', `消息列表绑定的不是目标会话(${read.detail})`, 'afterRecovery')
  }
  throw new PlatformError('ELEMENT_UNRESOLVED', '目标会话的消息列表尚未就绪', 'afterRecovery')
}

async function assertBossCurrent(tab: chrome.tabs.Tab, conversationRef: string, sideEffect: 'none' | 'possible'): Promise<void> {
  const current = await runInPage(BOSS_INJECT, tab.id!, mainReadBossCurrentConversation, [])
  const ref = current.status === 'ready' ? bossConversationRef(current.uid, current.friendSource) : ''
  if (ref !== conversationRef) {
    throw new PlatformError('USER_ACTIVE', '读取期间当前会话不是目标会话,本轮已停止', 'afterRecovery', undefined, sideEffect)
  }
}

async function readBossThread(
  args: ChatReadThreadArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<ChatReadThreadData> {
  if (validatePrimitiveArgs(PrimitiveName.ChatReadThread, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '会话读取参数不符合当前契约', 'manualOnly')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'manualOnly')
  if (args.cursor) {
    // 本实现单页交付、从不签发游标;脑收到 CURSOR_INVALID 会丢弃聚合从无 cursor 重来一次。
    throw new PlatformError('CURSOR_INVALID', 'BOSS 会话读取不分页,不接受游标')
  }
  const requireCurrent = args.requireCurrent === true
  const maxMessages = Math.min(THREAD_WINDOW_MAX, Math.max(1, args.window.maxMessages ?? THREAD_WINDOW_MAX))
  const anchors = args.window.anchorTail ?? []
  const tab = await verifiedBossChatTab(fingerprint)
  let platformReadStarted = false
  if (requireCurrent) {
    await assertBossCurrent(tab, args.conversationRef, 'none')
  } else {
    // 普通巡检:手自己把会话点开(智联 ensureThreadRoute 同款)。点开可能产生已读回执,
    // 先消费本 intrusive 命令唯一的 cancellation barrier。
    platformReadStarted = await ensureBossThreadOpen(tab, ctx, fingerprint, args.conversationRef)
  }
  if (!platformReadStarted) {
    await ctx.beforeSideEffect()
    platformReadStarted = true
  }
  ctx.progress('读取 BOSS 会话消息', 20)
  const read = await readBossThreadRows(tab, ctx, parsed.uid, parsed.friendSource)
  if (requireCurrent) await assertBossCurrent(tab, args.conversationRef, 'possible')
  const ordered = [...read.rows].sort((a, b) => (Number(a.mid) < Number(b.mid) ? -1 : Number(a.mid) > Number(b.mid) ? 1 : 0))
  const truncated = ordered.length > maxMessages
  const window = truncated ? ordered.slice(ordered.length - maxMessages) : ordered
  const projected = await projectBossThread(window)
  const anchor = matchAnchorTail(projected, anchors)
  const selected = anchor.start !== null ? projected.slice(anchor.start) : projected
  const messages: ThreadMessage[] = selected.map((message, idx) => ({ ...message, idx }))
  for (const message of messages) {
    if (message.text !== null && new TextEncoder().encode(message.text).length > 2048) {
      throw new PlatformError('PAYLOAD_LIMIT', '消息正文超过当前内联上限')
    }
  }
  const reachedTop = read.isToTop && !truncated
  const anchorMatched = anchor.count > 0
  const complete = reachedTop || anchorMatched
  if (!complete) {
    // 更老的历史要滚动消息面板才会加载,BOSS 上尚无滚轮注入;不伪造 complete、不造游标。
    throw new PlatformError('ELEMENT_UNRESOLVED',
      `已加载的 ${ordered.length} 条消息既未到顶(isToTop=${read.isToTop})也未对齐账本锚尾,BOSS 尚无滚动加载`,
      'afterRecovery', undefined, 'possible')
  }
  const peer: PeerSummary = {
    displayName: normalizeBossMessageText(read.peerName) || '未命名',
    platformUserRef: String(parsed.uid),
  }
  const data: ChatReadThreadData = { messages, reachedTop, anchorMatched, complete, nextCursor: null, peer }
  if (validatePrimitiveData(PrimitiveName.ChatReadThread, 1, data).length !== 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '会话读取结果不符合当前契约', 'manualOnly', undefined, 'possible')
  }
  if (jsonBytes(data) > RESULT_DATA_BUDGET) {
    throw new PlatformError('PAYLOAD_LIMIT', '会话读取结果超过内联载荷上限')
  }
  await verifiedBossChatTab(fingerprint)
  ctx.progress('BOSS 会话读取完成', 100)
  return data
}

// ── chat.sendMessage ────────────────────────────────────────────────────────

async function sendButtonClickPlan(tabId: number, conversationRef: string, expectedText: string): Promise<ClickPlan> {
  const button = await runInPage(BOSS_DOM, tabId, domReadBossSendButton, [SEND_BUTTON_SELECTOR])
  if (!button.found) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `发送钮认不出(命中 ${button.count} 个)`, 'manualOnly')
  }
  if (button.text !== '发送') {
    throw new PlatformError('ELEMENT_UNRESOLVED', `发送钮文案不是「发送」(读到「${button.text}」)`, 'manualOnly')
  }
  return {
    label: '发送钮',
    rect: button.rect,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domSendGate,
      [SEND_BUTTON_SELECTOR, x, y, ROW_SELECTOR, conversationRef, ROW_SELECTED_CLASS, COMPOSER_ID, expectedText]),
    observe: async (): Promise<ClickObservation> => {
      const composer = await runInPage(BOSS_DOM, tabId, mainReadComposer, [COMPOSER_ID])
      return { trusted: null, onTarget: null, eventDriftPx: null, after: `输入框内容长度=${composer.text.length}` }
    },
  }
}

/**
 * 用真实按键清空输入框:全选加删除,回读为空才算清掉。前提由调用方保证:焦点已在输入框、
 * Chrome 在前台。2026-09-03 甲方裁决撤销 composer.empty——框里的字无论真人还是机器留的
 * 都清掉,系统持续运行优先。清不空按 manualOnly 拒:那时框里是什么人一眼能看到。
 * 日志只记字数,不记内容。
 */
async function clearBossComposerByKeys(
  tabId: number, ctx: PrimitiveContext, trace: string[], beforeLength: number,
): Promise<void> {
  let played
  try {
    played = await playKeys(composeClearKeys(await readHandOS()))
  } catch (error) {
    if (isHandServiceDown(error)) {
      throw new PlatformError('CTX_NOT_READY', '手服务不可用,清空输入框未开始', 'afterRecovery', 'pageBroken')
    }
    throw new PlatformError('ELEMENT_UNRESOLVED',
      `清空输入框的按键半途失败:${describeError(error).slice(0, 300)}`, 'manualOnly')
  }
  const settled = await pollUntil(ctx,
    () => runInPage(BOSS_DOM, tabId, mainReadComposer, [COMPOSER_ID]),
    (read) => read.found && read.text === '', CLEAR_WAIT_MS)
  if (!settled.satisfied) {
    throw new PlatformError('ELEMENT_UNRESOLVED',
      `全选删除后输入框仍有 ${settled.value.text.length} 字,未清空`, 'manualOnly')
  }
  trace.push(`清掉 ${beforeLength} 字旧内容(${played.keys} 次按键)`)
  reportHandLog('warn', 'composerDraftCleared',
    `BOSS 打字前清掉输入框里 ${beforeLength} 字旧内容(2026-09-03 裁决:草稿不再保护)`)
}

async function sendBossMessage(
  args: ChatSendMessageArgs, guards: ChatSendMessageGuards, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<ChatSendMessageData> {
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'manualOnly')
  const normalizedText = normalizeBossMessageText(args.text)
  if (!normalizedText) throw new PlatformError('GUARD_FAILED', '规范化后的消息为空,拒绝发送', 'manualOnly')
  const contentHash = await sha256Hex(normalizedText)
  const tab = await verifiedBossChatTab(fingerprint)
  const tabId = tab.id!

  // 目标绑定:当前会话必须就是目标。不是就自己把它点到前台——与智联 ensureThreadRoute 同款,
  // 发送原语负责路由(2026-09-03 Mac 真机:轮内没有别的命令打开过目标,这里原来直接拒绝,
  // 一轮白跑、意图白铸)。打开是一次列表行点击,不产生候选人可见副作用;点开后再核对一次,
  // 仍不是目标就拒,后面的发送前最后一道闸(选中行=目标)照旧。
  const readCurrentRef = async (): Promise<string> => {
    const current = await runInPage(BOSS_INJECT, tabId, mainReadBossCurrentConversation, [])
    return current.status === 'ready' ? bossConversationRef(current.uid, current.friendSource) : ''
  }
  if ((await readCurrentRef()) !== args.conversationRef) {
    await ensureBossThreadOpen(tab, ctx, fingerprint, args.conversationRef, false)
    if ((await readCurrentRef()) !== args.conversationRef) {
      throw new PlatformError('GUARD_FAILED', '点开目标后当前会话仍不是发送目标,已取消', 'manualOnly')
    }
  }
  // 输入框里已有的字不再挡路(2026-09-03 甲方裁决撤销 composer.empty):取到焦点、
  // Chrome 在前台之后用真实按键全选删除,回读为空再打。见下方 clearBossComposerByKeys。
  const composerBefore = await runInPage(BOSS_DOM, tabId, mainReadComposer, [COMPOSER_ID])
  if (!composerBefore.found) throw new PlatformError('ELEMENT_UNRESOLVED', '页面上找不到聊天输入框', 'manualOnly')
  // 基线:发前的消息身份集合,发后只认不在基线里的新行。expectedTail 只观测不拦(2026-08-04 裁决)。
  const baseline = await readBossThreadRows(tab, ctx, parsed.uid, parsed.friendSource)
  const baselineMids = new Set(baseline.rows.map((row) => row.mid))
  const baselineProjected = await projectBossThread(
    [...baseline.rows].sort((a, b) => (Number(a.mid) < Number(b.mid) ? -1 : 1)))
  const tail = baselineProjected.slice(Math.max(0, baselineProjected.length - guards.expectedTail.length))
  const tailMatched = guards.expectedTail.length === tail.length &&
    guards.expectedTail.every((anchor, i) => anchor.direction === tail[i]!.direction && anchor.contentHash === tail[i]!.contentHash)
  if (!tailMatched && guards.expectedTail.length > 0) {
    reportHandLog('warn', 'sendBaselineDrift',
      `chat.sendMessage 基线已变(观测模式,照常发送):期望尾 ${guards.expectedTail.length} 行,实际尾 ${tail.map((m) => m.direction).join(',')}`)
  }
  const trace: string[] = []
  // 焦点:BOSS 打开会话时自己给焦点;不在时才点。
  if (!composerBefore.focused) {
    await osClickOnce(tabId, ctx, await bossComposerClickPlan(tabId), '点输入框取焦点')
    const after = await runInPage(BOSS_DOM, tabId, mainReadComposer, [COMPOSER_ID])
    if (!after.focused) throw new PlatformError('ELEMENT_UNRESOLVED', '点中输入框但焦点没到', 'afterRecovery')
    trace.push('点了输入框取焦点')
  }
  // 发键前 Chrome 必须在前台(否则拼音会敲进别的应用)。不在就条件等待人切过来,封顶 20 秒。
  const focusWait = await pollUntil(ctx, () => runInPage(BOSS_DOM, tabId, mainReadComposer, [COMPOSER_ID]),
    (read) => read.windowFocused)
  const beforeKeys = focusWait.value
  if (!beforeKeys.windowFocused) {
    throw new PlatformError('CTX_NOT_READY', `等了 ${READY_WAIT_MS / 1000} 秒 Chrome 仍不在前台,按键会打到别的应用上`, 'afterRecovery')
  }
  if (!beforeKeys.focused) {
    throw new PlatformError('USER_ACTIVE', '打字前焦点已离开输入框,已取消', 'afterRecovery')
  }
  if (beforeKeys.text !== '') {
    await clearBossComposerByKeys(tabId, ctx, trace, beforeKeys.text.length)
    const cleared = await runInPage(BOSS_DOM, tabId, mainReadComposer, [COMPOSER_ID])
    if (!cleared.focused || !cleared.windowFocused) {
      throw new PlatformError('USER_ACTIVE', '清空输入框后焦点或前台状态已变,已取消', 'afterRecovery')
    }
  }
  const { text: typedText, removed } = newlinesToSpaces(args.text)
  if (removed > 0) trace.push(`${removed} 个换行符换成空格`)
  if (typedText === '') throw new PlatformError('GUARD_FAILED', '去掉换行之后没有内容可打', 'manualOnly')
  ctx.checkpoint()
  let composed
  try {
    composed = await planType(typedText, seedFrom(ctx.cmdMsgId, 0))
  } catch (error) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `排版器自身异常:${describeError(error).slice(0, 300)}`, 'manualOnly')
  }
  if (!composed.ok) {
    throw new PlatformError('GUARD_FAILED', `文案排不出合格键序(${composed.tries} 次):${composed.reasons.join(';').slice(0, 300)}`, 'manualOnly')
  }
  // 从这里起输入框会被写入。任何失败都留着草稿,所以都是 manualOnly:人来清。
  let played
  try {
    played = await playTypePlan(composed.plan)
  } catch (error) {
    if (isHandServiceDown(error)) throw new PlatformError('CTX_NOT_READY', '手服务不可用,打字未开始', 'afterRecovery', 'pageBroken')
    throw new PlatformError('ELEMENT_UNRESOLVED', `打字半途失败,输入框可能残留草稿:${describeError(error).slice(0, 300)}`, 'manualOnly')
  }
  trace.push(`发了 ${played.keys} 次按键${played.words ? ` | ${played.words}` : ''}`)
  const typed = await runInPage(BOSS_DOM, tabId, mainReadComposer, [COMPOSER_ID])
  // 两边都过规范化再比:contenteditable 会把连续/尾部空格渲染成 nbsp,逐字比较会把
  // 这类假阴性判成"上屏不同"、留草稿转人工(出口审查 O3)。
  if (normalizeBossMessageText(typed.text) !== normalizeBossMessageText(typedText)) {
    // 上屏的不是这句话,不发:候选人看到的必须是脑写的那句。草稿留给人清。
    throw new PlatformError('GUARD_FAILED',
      `上屏文本与文案不同,已停在草稿:期望 ${typedText.length} 字,实得「${typed.text.slice(0, 200)}」`, 'manualOnly')
  }
  // 最后一道闸之后立即唯一一次点击发送。
  const plan = await sendButtonClickPlan(tabId, args.conversationRef, typedText)
  ctx.checkpoint()
  await verifiedBossChatTab(fingerprint)
  if (Date.now() > ctx.irreversibleNotAfterMs) {
    throw new PlatformError('CTX_LOST_DURING_EXEC', '不可逆动作窗口已过,已停在草稿', 'manualOnly')
  }
  await ctx.beforeSideEffect()
  const dispatchedAt = Date.now()
  const probe = await runOsProbe(BOSS_INJECT, tabId, ctx, plan)
  if (probe.outcome !== 'clicked') {
    throw new PlatformError(
      probe.outcome === 'handServiceUnavailable' ? 'CTX_NOT_READY' : 'ELEMENT_UNRESOLVED',
      `发送钮未点击,已停在草稿:${probe.detail ?? probe.outcome}`, 'manualOnly')
  }
  trace.push(`点了发送 ${probe.detail ?? ''}`)
  // 发后正证:验证读窗口里出现一条方向 out、文本哈希相等、不在基线里、时间不早于派发的行。
  const deadline = Date.now() + READY_WAIT_MS
  let lastSeen = ''
  // 首轮前让一拍:点击后页面先插乐观本地行,服务端确认要一个来回(出口审查 O1)。
  await sleep(500)
  while (Date.now() < deadline) {
    ctx.checkpoint()
    try {
      const after = await runInPage(BOSS_INJECT, tabId, mainReadBossThread, [parsed.uid, parsed.friendSource])
      if (after.status === 'ready') {
        // status 1=已送达未读 / 2=已读(平台事实 §二,真机已见)才是服务端确认;乐观渲染行与
        // 在途行不算——§4.5 明文"乐观渲染或只有平台本地临时 ID 的缓存记录都不算"。
        const fresh = after.rows.filter((row) => !baselineMids.has(row.mid) && row.direction === 'out' &&
          (row.status === 1 || row.status === 2))
        const hits: Array<{ mid: string; time: number | null }> = []
        for (const row of fresh) {
          const projected = projectBossMessage(row)
          if (projected.kind !== 'text') continue
          if (await sha256Hex(projected.hashInput) !== contentHash) continue
          if (row.time !== null && row.time < dispatchedAt - SEND_CLOCK_TOLERANCE_MS) continue
          hits.push({ mid: row.mid, time: row.time })
        }
        lastSeen = `新行 ${fresh.length},命中 ${hits.length}`
        if (hits.length >= 1) {
          // 同文多条取最新一条即本次(2026-07-29 裁决口径)。
          const hit = hits.reduce((best, next) => (Number(next.mid) > Number(best.mid) ? next : best))
          await verifiedBossChatTab(fingerprint)
          ctx.progress('已从当前消息列表确认新已发文本', 100)
          console.info('[RecruitHelper] boss_send_message', trace.join(' | '))
          return {
            conversationRef: args.conversationRef,
            contentHash,
            sourceKey: await sha256Hex(`source-v1|${hit.mid}`),
            observedAt: Date.now(),
            ...(hit.time !== null && hit.time > 0 ? { tsApprox: hit.time } : {}),
          }
        }
      } else {
        lastSeen = `消息列表 ${after.status}`
      }
    } catch (error) {
      lastSeen = `读取异常 ${describeError(error).slice(0, 120)}`
    }
    await sleep(500)
  }
  throw new PlatformError('POSTCONDITION_UNCONFIRMED',
    `只点击了一次发送,但未在消息列表确认新已发文本(${lastSeen};${trace.join(' | ')})`,
    'manualOnly', undefined, 'possible')
}


// ── 换微信线(场景二,2026-09-04 出口) ───────────────────────────────────────────
//
// 判据按甲方两条原则(出口 §〇):每个判定只认一两个最标志的信号,且信号在「刚做完动作回读」与
// 「重开会话再读」两个时机都可见。已换成只认 conversation$.weixin 落值;我方请求已发出只认 aid=32
// 出站行;对方待答只认 operated=false 且带 aid 33 的入站 dialog 行。weixinVisible、按钮文案、快捷条
// 只进 detail 当观测。脑侧零改动:三种形态投成契约三种形状,三条原语按契约 data 回值。

const TOOLBAR_BUTTON_SELECTOR = '.conversation-operate .operate-btn'
const EXCHANGE_TOOLTIP_SELECTOR = '.exchange-tooltip'
const EXCHANGE_CONFIRM_SELECTOR = '.exchange-tooltip .boss-btn-primary'
const EXCHANGE_CANCEL_SELECTOR = '.exchange-tooltip .boss-btn-outline'
const WECHAT_MODAL_SELECTOR = '.dialog-wrap.active, .add-wx-wrap'
const CARD_BUTTON_SELECTOR = '.message-card-buttons .card-btn'
const CARD_ITEM_SELECTOR = '.message-item'
const WECHAT_CARD_TEXT = '交换微信'
const WECHAT_ACCEPT_AID = 33
const WECHAT_REQUEST_SENT_AID = 32
const WECHAT_REQUEST_SENT_BIZ = 21050024
const WECHAT_NUMBER_TEMPLATE_ID = 5
const HARVEST_WAIT_MS = 5_000

type BossRect = { x: number; y: number; w: number; h: number }

type BossWechatStateRead =
  | { status: 'ready'; weixin: string | null; weixinVisible: number | null; requestWeiXin: number | null; bothTalked: boolean }
  | { status: 'missing' }
  | { status: 'binding_mismatch' }

/**
 * 当前会话对象上的微信线字段。只取这几个,不整块 dump(user$ 旁边挨着 token,凭据不落纸)。
 * weixin 是候选人微信号,只交给原语装进 typed data 的 peerWechat,不进日志。
 */
function mainReadBossWechatState(uid: number, friendSource: number): BossWechatStateRead {
  type AnyRecord = Record<string, unknown>
  const seen = new Set<unknown>()
  let mismatch = false
  for (const element of Array.from(document.querySelectorAll('*'))) {
    const instance = (element as unknown as { __vue__?: AnyRecord }).__vue__
    if (!instance || seen.has(instance)) continue
    seen.add(instance)
    if (!Object.prototype.hasOwnProperty.call(instance, 'conversation$')) continue
    let conversation: unknown
    try { conversation = instance['conversation$'] } catch { continue }
    if (!conversation || typeof conversation !== 'object' || Array.isArray(conversation)) continue
    const record = conversation as AnyRecord
    if (typeof record.uid !== 'number') continue
    if (record.uid !== uid || record.friendSource !== friendSource) { mismatch = true; continue }
    const weixin = typeof record.weixin === 'string' && record.weixin.trim() !== '' ? record.weixin.trim() : null
    const numOf = (value: unknown): number | null => (typeof value === 'number' && Number.isFinite(value) ? value : null)
    return {
      status: 'ready', weixin,
      weixinVisible: numOf(record.weixinVisible), requestWeiXin: numOf(record.requestWeiXin),
      bothTalked: record.bothTalked === true,
    }
  }
  return mismatch ? { status: 'binding_mismatch' } : { status: 'missing' }
}

/** 工具栏「换微信」钮:按文案认(换微信 / 换微信 请求中 / 查看微信 三种真机已见形态),恰一个才算认出。 */
function domReadBossWechatButton(selector: string): {
  found: boolean; count: number; index: number; text: string; disabled: boolean; rect: BossRect
} {
  const all = Array.from(document.querySelectorAll(selector))
  const hits: Array<{ index: number; el: Element; text: string }> = []
  all.forEach((el, index) => {
    const text = (el.textContent ?? '').replace(/\s+/gu, ' ').trim()
    if (text === '换微信' || text === '换微信 请求中' || text === '查看微信') hits.push({ index, el, text })
  })
  if (hits.length !== 1) return { found: false, count: hits.length, index: -1, text: '', disabled: false, rect: { x: 0, y: 0, w: 0, h: 0 } }
  const { index, el, text } = hits[0]!
  const r = el.getBoundingClientRect()
  return { found: true, count: 1, index, text, disabled: el.classList.contains('disabled'), rect: { x: r.x, y: r.y, w: r.width, h: r.height } }
}

/**
 * 内联确认 tooltip:可见且文案含「交换微信」的那份。页面常驻一份「求简历」的同类 tooltip(display none),
 * 所以确定/取消键要按"在可见那份里"筛,返回它们在各自 selector 列表里的 index 给命中测试用。
 */
function domReadBossExchangeTooltip(
  tooltipSelector: string, confirmSelector: string, cancelSelector: string, modalSelector: string,
): { visible: boolean; text: string; confirmIndex: number; cancelIndex: number; confirmRect: BossRect; cancelRect: BossRect; modal: number } {
  const zero = { x: 0, y: 0, w: 0, h: 0 }
  const modal = document.querySelectorAll(modalSelector).length
  const tip = Array.from(document.querySelectorAll(tooltipSelector)).find((el) =>
    getComputedStyle(el).display !== 'none' && (el.textContent ?? '').includes('交换微信'))
  if (!tip) return { visible: false, text: '', confirmIndex: -1, cancelIndex: -1, confirmRect: zero, cancelRect: zero, modal }
  const rectOf = (el: Element | undefined): BossRect => {
    if (!el) return zero
    const r = el.getBoundingClientRect()
    return { x: r.x, y: r.y, w: r.width, h: r.height }
  }
  const confirms = Array.from(document.querySelectorAll(confirmSelector))
  const cancels = Array.from(document.querySelectorAll(cancelSelector))
  const confirmIndex = confirms.findIndex((el) => tip.contains(el) && (el.textContent ?? '').trim() === '确定')
  const cancelIndex = cancels.findIndex((el) => tip.contains(el) && (el.textContent ?? '').trim() === '取消')
  return {
    visible: true, text: (tip.textContent ?? '').replace(/\s+/gu, ' ').trim().slice(0, 40),
    confirmIndex, cancelIndex,
    confirmRect: rectOf(confirms[confirmIndex]), cancelRect: rectOf(cancels[cancelIndex]), modal,
  }
}

/**
 * 卡内「同意」card-btn:可见、不带 disabled、文案恰「同意」、且所在消息卡文案含「交换微信」的,恰一个才认出;
 * clipOk = 视口内可见部分够落光标。附件简历请求等别的 dialog 卡同样带「同意」键(出口审查发现 1),
 * 所以按卡文案筛,不按页面唯一。
 */
function domReadBossAcceptButton(selector: string, cardSelector: string, cardText: string): {
  found: boolean; count: number; index: number; rect: BossRect; clipOk: boolean
} {
  const all = Array.from(document.querySelectorAll(selector))
  const hits: Array<{ index: number; el: Element }> = []
  all.forEach((el, index) => {
    if ((el.textContent ?? '').trim() !== '同意') return
    if (el.classList.contains('disabled')) return
    const card = el.closest(cardSelector)
    if (!card || !(card.textContent ?? '').includes(cardText)) return
    const r = el.getBoundingClientRect()
    if (r.width === 0 || r.height === 0) return
    hits.push({ index, el })
  })
  if (hits.length !== 1) return { found: false, count: hits.length, index: -1, rect: { x: 0, y: 0, w: 0, h: 0 }, clipOk: false }
  const { index, el } = hits[0]!
  const r = el.getBoundingClientRect()
  const w = Math.min(window.innerWidth, r.right) - Math.max(0, r.left)
  const h = Math.min(window.innerHeight, r.bottom) - Math.max(0, r.top)
  return { found: true, count: 1, index, rect: { x: r.x, y: r.y, w: r.width, h: r.height }, clipOk: w >= 24 && h >= 24 }
}

/**
 * 接受前最后一道闸(sendMessage 的 domSendGate 同款):落点是那个「同意」键、它仍可用、所在卡仍是换微信请求卡、
 * 选中行仍是目标会话——四者缺一不点。真人在 preflight 之后切走会话,末条恰好也是带「同意」的卡时,
 * 只查 index 会点到别人的卡(出口审查发现 1 序列二)。
 */
function domAcceptGate(
  buttonSelector: string, index: number, x: number, y: number,
  cardSelector: string, cardText: string,
  rowSelector: string, conversationRef: string, selectedClass: string,
): { onTarget: boolean; found: string } {
  const target = Array.from(document.querySelectorAll(buttonSelector))[index]
  const at = document.elementFromPoint(x, y)
  const problems: string[] = []
  if (!target) problems.push('靶子已经不在原来的位置上')
  else {
    if (!at) problems.push('落点上什么都没有')
    else if (!(at === target || target.contains(at))) problems.push(`落点上是别的元素 ${at.tagName.toLowerCase()}「${(at.textContent ?? '').trim().slice(0, 8)}」`)
    if ((target.textContent ?? '').trim() !== '同意') problems.push(`靶子文案已变「${(target.textContent ?? '').trim().slice(0, 8)}」`)
    if (target.classList.contains('disabled')) problems.push('同意键已 disabled')
    const card = target.closest(cardSelector)
    if (!card || !(card.textContent ?? '').includes(cardText)) problems.push('所在卡不是换微信请求卡')
  }
  const selectedRows = Array.from(document.querySelectorAll(rowSelector)).filter((el) => el.classList.contains(selectedClass))
  if (!(selectedRows.length === 1 && selectedRows[0]!.getAttribute('data-id') === conversationRef)) {
    problems.push(`选中行不是目标会话(选中 ${selectedRows.length} 行)`)
  }
  return problems.length === 0 ? { onTarget: true, found: '同意键' } : { onTarget: false, found: problems.join(';') }
}

/** 对方待答的换微信请求行:入站 dialog、带 aid 33、未答过。 */
export function pendingBossWechatRequests(rows: BossRawMessage[]): BossRawMessage[] {
  return rows.filter((row) => row.direction === 'in' && row.bizType === 12 && row.bodyType === 7 &&
    row.dialogAids.includes(WECHAT_ACCEPT_AID) && row.dialogOperated === false)
}

function isBossWechatRequestRow(row: BossRawMessage): boolean {
  return row.direction === 'in' && row.bizType === 12 && row.bodyType === 7 && row.dialogAids.includes(WECHAT_ACCEPT_AID)
}

function isBossWechatNumberRow(row: BossRawMessage): boolean {
  return row.direction === 'in' && row.bizType === 12 && row.bodyType === 1 && row.templateId === WECHAT_NUMBER_TEMPLATE_ID
}

/**
 * 微信号结果行的选取(契约 §4.3 两形态在 BOSS 上的同构):带锚取锚后、下一条请求卡之前恰一条;
 * 无锚取当前可见的最新一条。零条或多条都不猜。
 */
export function selectBossExchangeResult(
  rows: BossRawMessage[], anchorMid: string | null,
): { status: 'one'; row: BossRawMessage } | { status: 'none' } | { status: 'many'; count: number } | { status: 'anchor_missing' } {
  const ordered = [...rows].sort((a, b) => (Number(a.mid) < Number(b.mid) ? -1 : Number(a.mid) > Number(b.mid) ? 1 : 0))
  if (anchorMid === null) {
    const results = ordered.filter(isBossWechatNumberRow)
    return results.length === 0 ? { status: 'none' } : { status: 'one', row: results[results.length - 1]! }
  }
  const start = ordered.findIndex((row) => row.mid === anchorMid)
  if (start < 0 || !isBossWechatRequestRow(ordered[start]!)) return { status: 'anchor_missing' }
  const span: BossRawMessage[] = []
  for (const row of ordered.slice(start + 1)) {
    if (isBossWechatRequestRow(row)) break
    if (isBossWechatNumberRow(row)) span.push(row)
  }
  if (span.length === 1) return { status: 'one', row: span[0]! }
  return span.length === 0 ? { status: 'none' } : { status: 'many', count: span.length }
}

async function bossSourceKey(mid: string): Promise<string> {
  return sha256Hex(`source-v1|${mid}`)
}

async function findBossRowBySourceKey(rows: BossRawMessage[], sourceKey: string): Promise<BossRawMessage | null> {
  for (const row of rows) {
    if (await bossSourceKey(row.mid) === sourceKey) return row
  }
  return null
}

async function readBossWechatState(
  tab: chrome.tabs.Tab, parsed: { uid: number; friendSource: number },
): Promise<Extract<BossWechatStateRead, { status: 'ready' }>> {
  const read = await runInPage(BOSS_INJECT, tab.id!, mainReadBossWechatState, [parsed.uid, parsed.friendSource])
  if (read.status === 'ready') return read
  if (read.status === 'binding_mismatch') {
    throw new PlatformError('CTX_LOST_DURING_EXEC', '当前会话对象绑定的不是目标会话', 'afterRecovery')
  }
  throw new PlatformError('ELEMENT_UNRESOLVED', '页面上读不到目标会话对象', 'afterRecovery')
}

/** 目标绑定:当前会话必须是目标,不是就自己点开(sendMessage 同款),点开后仍不是就拒。 */
async function ensureBossSendTarget(
  tab: chrome.tabs.Tab, ctx: PrimitiveContext, fingerprint: string, conversationRef: string,
): Promise<void> {
  const readCurrentRef = async (): Promise<string> => {
    const current = await runInPage(BOSS_INJECT, tab.id!, mainReadBossCurrentConversation, [])
    return current.status === 'ready' ? bossConversationRef(current.uid, current.friendSource) : ''
  }
  if ((await readCurrentRef()) === conversationRef) return
  await ensureBossThreadOpen(tab, ctx, fingerprint, conversationRef, false)
  if ((await readCurrentRef()) !== conversationRef) {
    throw new PlatformError('GUARD_FAILED', '点开目标后当前会话仍不是动作目标,已取消', 'manualOnly')
  }
}

/** expectedTail 只观测不拦(2026-08-04 裁决),与 sendMessage 同款记法。 */
async function observeBossExpectedTail(rows: BossRawMessage[], guards: ChatSendMessageGuards, what: string): Promise<void> {
  if (guards.expectedTail.length === 0) return
  const projected = await projectBossThread([...rows].sort((a, b) => (Number(a.mid) < Number(b.mid) ? -1 : 1)))
  const tail = projected.slice(Math.max(0, projected.length - guards.expectedTail.length))
  const matched = guards.expectedTail.length === tail.length &&
    guards.expectedTail.every((anchor, i) => anchor.direction === tail[i]!.direction && anchor.contentHash === tail[i]!.contentHash)
  if (!matched) {
    reportHandLog('warn', 'sendBaselineDrift',
      `${what} 基线已变(观测模式,照常执行):期望尾 ${guards.expectedTail.length} 行,实际尾 ${tail.map((m) => m.direction).join(',')}`)
  }
}

function paceBeforeClick(): Promise<void> {
  return sleep(1_000 + Math.floor(Math.random() * 401))
}

async function readBossExchangeTooltip(tabId: number) {
  return runInPage(BOSS_DOM, tabId, domReadBossExchangeTooltip,
    [EXCHANGE_TOOLTIP_SELECTOR, EXCHANGE_CONFIRM_SELECTOR, EXCHANGE_CANCEL_SELECTOR, WECHAT_MODAL_SELECTOR])
}

/** 收回内联确认:点「取消」。它只关 tooltip、无副作用;收不回也只记日志,tooltip 留着无害。 */
async function cancelBossExchangeTooltip(tabId: number, ctx: PrimitiveContext, why: string): Promise<void> {
  try {
    const tip = await readBossExchangeTooltip(tabId)
    if (!tip.visible || tip.cancelIndex < 0) return
    await paceBeforeClick()
    await osClickOnce(tabId, ctx, {
      label: '换微信取消键',
      rect: tip.cancelRect,
      hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestExpected, [EXCHANGE_CANCEL_SELECTOR, tip.cancelIndex, '取消', x, y]),
      observe: async (): Promise<ClickObservation> => {
        const after = await readBossExchangeTooltip(tabId)
        return { trusted: null, onTarget: null, eventDriftPx: null, after: `tooltip 可见=${after.visible}` }
      },
    }, '点换微信取消键')
    reportHandLog('warn', 'wechatInviteCancelled', `BOSS 换微信内联确认已取消:${why}`)
  } catch (error) {
    reportHandLog('warn', 'wechatInviteCancelFailed', `BOSS 换微信内联确认未能取消(${why}):${describeError(error).slice(0, 200)}`)
  }
}

// ── chat.sendWechatInvite ────────────────────────────────────────────────────

/**
 * 生产机己方微信号一律预配,所以只有两步:点工具栏「换微信」→ 点内联 tooltip「确定」(平台事实 §十四)。
 * 并发形态(对方先发请求、脑随后按流程邀请)被平台执行成"接受":两道同 evaluator 检查——点按钮前与按确定前
 * 各读一次消息数组,读到对方待答请求就不点/点取消,交由脑下一轮走 acceptWechat;残余按记录级走 suspect
 * (2026-09-04 甲方裁决方案 1)。
 */
async function sendBossWechatInvite(
  args: ChatSendWechatInviteArgs, guards: ChatSendMessageGuards, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<ChatSendWechatInviteData> {
  if (validatePrimitiveArgs(PrimitiveName.ChatSendWechatInvite, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '换微信邀请参数不符合当前契约', 'manualOnly')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'manualOnly')
  const contentHash = await sha256Hex('card\x1fwechatExchange')
  const tab = await verifiedBossChatTab(fingerprint)
  const tabId = tab.id!
  await ensureBossSendTarget(tab, ctx, fingerprint, args.conversationRef)

  // 世界状态核对一:会话级。已换成 / 未双向对话都是脑预期之外,干净失败不点。
  const wechat = await readBossWechatState(tab, parsed)
  if (wechat.weixin !== null) throw new PlatformError('GUARD_FAILED', '该会话微信已换成,不再发起邀请', 'no')
  if (!wechat.bothTalked) throw new PlatformError('GUARD_FAILED', '双方尚未都说过话,平台不开放换微信(bothTalked=false)', 'no')
  // 基线 + 世界状态核对二:对方是否已有待答请求(并发前置第一道)。
  const baseline = await readBossThreadRows(tab, ctx, parsed.uid, parsed.friendSource)
  await observeBossExpectedTail(baseline.rows, guards, 'chat.sendWechatInvite')
  if (pendingBossWechatRequests(baseline.rows).length > 0) {
    throw new PlatformError('GUARD_FAILED', '对方已有待答的换微信请求,本轮不发起邀请,由 acceptWechat 接手', 'no')
  }
  const baselineMids = new Set(baseline.rows.map((row) => row.mid))
  // 工具栏按钮:文案必须恰为「换微信」且不带 disabled。「换微信 请求中」= 我方已有待答请求,「查看微信」= 已换成。
  const button = await runInPage(BOSS_DOM, tabId, domReadBossWechatButton, [TOOLBAR_BUTTON_SELECTOR])
  if (!button.found) throw new PlatformError('ELEMENT_UNRESOLVED', `工具栏换微信钮认不出(命中 ${button.count} 个)`, 'afterRecovery')
  if (button.text === '查看微信') throw new PlatformError('GUARD_FAILED', '工具栏已是「查看微信」,微信已换成', 'no')
  if (button.disabled || button.text !== '换微信') {
    throw new PlatformError('GUARD_FAILED', `换微信钮不可用(读到「${button.text}」${button.disabled ? ',disabled' : ''})`, 'no')
  }
  const trace: string[] = []
  // 第一步:点「换微信」。可逆——只弹内联 tooltip,有取消键。
  const buttonPlan: ClickPlan = {
    label: '工具栏换微信钮',
    rect: button.rect,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestExpected, [TOOLBAR_BUTTON_SELECTOR, button.index, '换微信', x, y]),
    observe: async (): Promise<ClickObservation> => {
      const after = await readBossExchangeTooltip(tabId)
      return { trusted: null, onTarget: null, eventDriftPx: null, after: `tooltip 可见=${after.visible} 模态=${after.modal}` }
    },
  }
  ctx.checkpoint()
  await paceBeforeClick()
  await verifiedBossChatTab(fingerprint)
  trace.push(`点了换微信 ${await osClickOnce(tabId, ctx, buttonPlan, '点换微信')}`)
  const tipWait = await pollUntil(ctx, () => readBossExchangeTooltip(tabId), (tip) => tip.visible || tip.modal > 0)
  const tip = tipWait.value
  if (tip.modal > 0) {
    // 己方微信号未配才会弹模态(平台事实 §六 第 2 步)。不填、不点,人去平台配一次。
    throw new PlatformError('ELEMENT_UNRESOLVED', '点换微信后弹出模态(己方微信号未配置?),未填号、未确认', 'manualOnly')
  }
  if (!tip.visible || tip.confirmIndex < 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `点换微信后未见内联确认(可见=${tip.visible} 确定键=${tip.confirmIndex})`, 'afterRecovery')
  }
  // 最后一道闸:同一 evaluator 再读一次(并发前置第二道)。这时才出现的待答请求 → 点取消、干净拒绝。
  const again = await readBossThreadRows(tab, ctx, parsed.uid, parsed.friendSource)
  if (pendingBossWechatRequests(again.rows).length > 0) {
    await cancelBossExchangeTooltip(tabId, ctx, '确认前读到对方刚发来的换微信请求')
    throw new PlatformError('GUARD_FAILED', '确认前读到对方刚发来的换微信请求,已取消,由 acceptWechat 接手', 'no')
  }
  const wechatAgain = await readBossWechatState(tab, parsed)
  if (wechatAgain.weixin !== null) {
    await cancelBossExchangeTooltip(tabId, ctx, '确认前微信已落值')
    throw new PlatformError('GUARD_FAILED', '确认前读到微信已换成,已取消', 'no')
  }
  const confirmPlan: ClickPlan = {
    label: '换微信确定键',
    rect: tip.confirmRect,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestExpected, [EXCHANGE_CONFIRM_SELECTOR, tip.confirmIndex, '确定', x, y]),
    observe: async (): Promise<ClickObservation> => {
      const after = await readBossExchangeTooltip(tabId)
      return { trusted: null, onTarget: null, eventDriftPx: null, after: `tooltip 可见=${after.visible}` }
    },
  }
  ctx.checkpoint()
  await verifiedBossChatTab(fingerprint)
  if (Date.now() > ctx.irreversibleNotAfterMs) {
    await cancelBossExchangeTooltip(tabId, ctx, '不可逆动作窗口已过')
    throw new PlatformError('CTX_LOST_DURING_EXEC', '不可逆动作窗口已过,已取消', 'afterRecovery')
  }
  await ctx.beforeSideEffect()
  const dispatchedAt = Date.now()
  const probe = await runOsProbe(BOSS_INJECT, tabId, ctx, confirmPlan)
  if (probe.outcome !== 'clicked') {
    // 闸在按下之前拒:没点。tooltip 留着无害,下轮重来。
    throw new PlatformError(
      probe.outcome === 'handServiceUnavailable' ? 'CTX_NOT_READY' : 'ELEMENT_UNRESOLVED',
      `确定键未点击:${probe.detail ?? probe.outcome}`, 'afterRecovery')
  }
  trace.push(`点了确定 ${probe.detail ?? ''}`)
  // 正证(判据表第一行):新增 aid=32 出站行,服务端确认(status 1/2),时间不早于派发。
  const deadline = Date.now() + READY_WAIT_MS
  let lastSeen = ''
  await sleep(500)
  while (Date.now() < deadline) {
    ctx.checkpoint()
    try {
      const after = await runInPage(BOSS_INJECT, tabId, mainReadBossThread, [parsed.uid, parsed.friendSource])
      if (after.status === 'ready') {
        const hits = after.rows.filter((row) => !baselineMids.has(row.mid) && row.direction === 'out' &&
          row.bizType === WECHAT_REQUEST_SENT_BIZ && row.actionAid === WECHAT_REQUEST_SENT_AID &&
          (row.status === 1 || row.status === 2) &&
          !(row.time !== null && row.time < dispatchedAt - SEND_CLOCK_TOLERANCE_MS))
        lastSeen = `新行 ${after.rows.filter((row) => !baselineMids.has(row.mid)).length},命中 ${hits.length}`
        if (hits.length >= 1) {
          const hit = hits.reduce((best, next) => (Number(next.mid) > Number(best.mid) ? next : best))
          await verifiedBossChatTab(fingerprint)
          ctx.progress('已从当前消息列表确认换微信请求已发出', 100)
          console.info('[RecruitHelper] boss_send_wechat_invite', trace.join(' | '))
          return {
            conversationRef: args.conversationRef,
            contentHash,
            sourceKey: await bossSourceKey(hit.mid),
            observedAt: Date.now(),
            ...(hit.time !== null && hit.time > 0 ? { tsApprox: hit.time } : {}),
          }
        }
        // 残余并发(甲方 2026-09-04 裁决方案 1,记录级):对方在最后一次读之后才发请求,确定被平台执行成接受。
        // 没有我方邀请行,只有微信落值——如实回未确认,走既有 suspect;业务线由 accepted 卡推到已换成。
        const state = await runInPage(BOSS_INJECT, tabId, mainReadBossWechatState, [parsed.uid, parsed.friendSource])
        if (state.status === 'ready' && state.weixin !== null) {
          throw new PlatformError('POSTCONDITION_UNCONFIRMED',
            `对方待答请求已被本次确定接受,微信已落值;无我方邀请行(${lastSeen};${trace.join(' | ')})`,
            'manualOnly', undefined, 'possible')
        }
      } else {
        lastSeen = `消息列表 ${after.status}`
      }
    } catch (error) {
      if (error instanceof PlatformError) throw error
      lastSeen = `读取异常 ${describeError(error).slice(0, 120)}`
    }
    await sleep(500)
  }
  throw new PlatformError('POSTCONDITION_UNCONFIRMED',
    `只点击了一次确定,但未在消息列表确认换微信请求行(${lastSeen};${trace.join(' | ')})`,
    'manualOnly', undefined, 'possible')
}

// ── chat.acceptWechat ────────────────────────────────────────────────────────

/**
 * 接受对方主动发来的换微信请求:点卡内「同意」(与 requestSourceKey 那条消息绑定,不用页面级快捷条)。
 * 正证只认 conversation$.weixin 落值,或锚行 dialog.operated=true(两时机都可见,平台事实 §十四)。
 */
async function acceptBossWechat(
  args: ChatAcceptWechatArgs, guards: ChatSendMessageGuards, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<ChatAcceptWechatData> {
  if (validatePrimitiveArgs(PrimitiveName.ChatAcceptWechat, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '接受微信参数不符合当前契约', 'manualOnly')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'manualOnly')
  const tab = await verifiedBossChatTab(fingerprint)
  const tabId = tab.id!
  await ensureBossSendTarget(tab, ctx, fingerprint, args.conversationRef)
  const rows = (await readBossThreadRows(tab, ctx, parsed.uid, parsed.friendSource)).rows
  await observeBossExpectedTail(rows, guards, 'chat.acceptWechat')
  const anchor = await findBossRowBySourceKey(rows, args.requestSourceKey)
  if (!anchor) throw new PlatformError('GUARD_FAILED', '请求锚在当前消息数组里找不到', 'afterRecovery')
  if (!isBossWechatRequestRow(anchor)) {
    throw new PlatformError('GUARD_FAILED', '请求锚不是对方的换微信请求卡(方向/类型/按钮码不符)', 'manualOnly')
  }
  if (anchor.dialogOperated === true) throw new PlatformError('GUARD_FAILED', '该请求已答过,不再点击', 'no')
  const wechat = await readBossWechatState(tab, parsed)
  if (wechat.weixin !== null) throw new PlatformError('GUARD_FAILED', '该会话微信已换成,不再点击', 'no')
  const pending = pendingBossWechatRequests(rows)
  if (pending.length !== 1 || pending[0]!.mid !== anchor.mid) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `待答的换微信请求不唯一(${pending.length} 条),不猜`, 'manualOnly')
  }
  const button = await runInPage(BOSS_DOM, tabId, domReadBossAcceptButton, [CARD_BUTTON_SELECTOR, CARD_ITEM_SELECTOR, WECHAT_CARD_TEXT])
  if (!button.found) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `卡内同意钮认不出(可用 ${button.count} 个)`, button.count > 1 ? 'manualOnly' : 'afterRecovery')
  }
  if (!button.clipOk) throw new PlatformError('ELEMENT_UNRESOLVED', '请求卡不在视口内,本轮不滚动、不点', 'afterRecovery')
  const plan: ClickPlan = {
    label: '卡内同意钮',
    rect: button.rect,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domAcceptGate,
      [CARD_BUTTON_SELECTOR, button.index, x, y, CARD_ITEM_SELECTOR, WECHAT_CARD_TEXT, ROW_SELECTOR, args.conversationRef, ROW_SELECTED_CLASS]),
    observe: async (): Promise<ClickObservation> => {
      const after = await runInPage(BOSS_DOM, tabId, domReadBossAcceptButton, [CARD_BUTTON_SELECTOR, CARD_ITEM_SELECTOR, WECHAT_CARD_TEXT])
      return { trusted: null, onTarget: null, eventDriftPx: null, after: `可用同意钮=${after.count}` }
    },
  }
  ctx.checkpoint()
  await paceBeforeClick()
  await verifiedBossChatTab(fingerprint)
  if (Date.now() > ctx.irreversibleNotAfterMs) {
    throw new PlatformError('CTX_LOST_DURING_EXEC', '不可逆动作窗口已过,未点击', 'afterRecovery')
  }
  await ctx.beforeSideEffect()
  const probe = await runOsProbe(BOSS_INJECT, tabId, ctx, plan)
  if (probe.outcome !== 'clicked') {
    throw new PlatformError(
      probe.outcome === 'handServiceUnavailable' ? 'CTX_NOT_READY' : 'ELEMENT_UNRESOLVED',
      `同意钮未点击:${probe.detail ?? probe.outcome}`, 'afterRecovery')
  }
  // 正证:weixin 落值 或 锚行 operated=true(两者任一,两时机都可见)。
  type Snap = { weixin: string | null; anchorOperated: boolean | null; rows: BossRawMessage[] }
  const snapshot = async (): Promise<Snap> => {
    const state = await runInPage(BOSS_INJECT, tabId, mainReadBossWechatState, [parsed.uid, parsed.friendSource])
    const thread = await runInPage(BOSS_INJECT, tabId, mainReadBossThread, [parsed.uid, parsed.friendSource])
    const list = thread.status === 'ready' ? thread.rows : []
    const anchorNow = list.find((row) => row.mid === anchor.mid)
    return {
      weixin: state.status === 'ready' ? state.weixin : null,
      anchorOperated: anchorNow ? anchorNow.dialogOperated : null,
      rows: list,
    }
  }
  await sleep(500)
  const confirmed = await pollUntil(ctx, snapshot, (snap) => snap.weixin !== null || snap.anchorOperated === true)
  if (!confirmed.satisfied) {
    throw new PlatformError('POSTCONDITION_UNCONFIRMED',
      `只点击了一次同意,但未确认微信落值或请求卡已答(weixin=${confirmed.value.weixin !== null} operated=${confirmed.value.anchorOperated})`,
      'manualOnly', undefined, 'possible')
  }
  // 可选加成:号 + 结果行同时齐才带,否则一并缺席,由 readWechatExchangeOutcome 延迟收编;不推翻正证。
  const harvested = await pollUntil(ctx, snapshot,
    (snap) => snap.weixin !== null && selectBossExchangeResult(snap.rows, anchor.mid).status === 'one', HARVEST_WAIT_MS)
  const result = selectBossExchangeResult(harvested.value.rows, anchor.mid)
  const data: ChatAcceptWechatData = harvested.value.weixin !== null && result.status === 'one'
    ? {
        conversationRef: args.conversationRef, requestSourceKey: args.requestSourceKey,
        exchangeSourceKey: await bossSourceKey(result.row.mid), peerWechat: harvested.value.weixin,
        observedAt: Date.now(),
      }
    : { conversationRef: args.conversationRef, requestSourceKey: args.requestSourceKey, observedAt: Date.now() }
  if (validatePrimitiveData(PrimitiveName.ChatAcceptWechat, 1, data).length !== 0) {
    throw new PlatformError('POSTCONDITION_UNCONFIRMED', '微信接受结果不符合当前契约', 'manualOnly', undefined, 'possible')
  }
  await verifiedBossChatTab(fingerprint)
  ctx.progress(data.peerWechat ? '已接受对方换微信请求并取到号' : '已接受对方换微信请求(号待收编)', 100)
  return data
}

// ── chat.readWechatExchangeOutcome ───────────────────────────────────────────

/** 零点击、不定位:当前会话必须已是目标。判据只认 conversation$.weixin 落值;结果行按 selectBossExchangeResult 选。 */
async function readBossWechatExchangeOutcome(
  args: ChatReadWechatExchangeOutcomeArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<ChatReadWechatExchangeOutcomeData> {
  if (validatePrimitiveArgs(PrimitiveName.ChatReadWechatExchangeOutcome, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '微信交换结果读取参数不符合当前契约', 'manualOnly')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'manualOnly')
  const tab = await verifiedBossChatTab(fingerprint)
  await assertBossCurrent(tab, args.conversationRef, 'none')
  const rows = (await readBossThreadRows(tab, ctx, parsed.uid, parsed.friendSource)).rows
  const wechat = await readBossWechatState(tab, parsed)
  let data: ChatReadWechatExchangeOutcomeData = { confirmed: false, observedAt: Date.now() }
  let why = wechat.weixin === null ? '微信未落值' : ''
  if (wechat.weixin !== null) {
    let anchorMid: string | null = null
    let anchorOk = true
    if (args.requestSourceKey) {
      const anchor = await findBossRowBySourceKey(rows, args.requestSourceKey)
      if (!anchor || !isBossWechatRequestRow(anchor)) {
        anchorOk = false
        why = anchor ? '请求锚不是对方的换微信请求卡' : '请求锚在当前消息数组里找不到'
      } else {
        anchorMid = anchor.mid
      }
    }
    if (anchorOk) {
      const result = selectBossExchangeResult(rows, anchorMid)
      if (result.status === 'one') {
        data = { confirmed: true, exchangeSourceKey: await bossSourceKey(result.row.mid), peerWechat: wechat.weixin, observedAt: Date.now() }
      } else {
        why = `微信已落值但结果行${result.status === 'none' ? '未到' : result.status === 'many' ? `不唯一(${result.count})` : '锚缺失'}`
      }
    }
  }
  if (validatePrimitiveData(PrimitiveName.ChatReadWechatExchangeOutcome, 1, data).length !== 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '微信交换结果结构不符合当前契约', 'manualOnly')
  }
  await assertBossCurrent(tab, args.conversationRef, 'none')
  ctx.progress(data.confirmed ? '已确认微信交换结果' : `本轮未确认微信交换结果(${why})`, 100)
  return data
}

// ── chat.captureThreadScreenshot ────────────────────────────────────────────

async function decodeFrame(dataUrl: string): Promise<ImageBitmap> {
  const response = await fetch(dataUrl)
  return await createImageBitmap(await response.blob())
}

/**
 * 单帧截图:只拍当前可见的聊天区,不滚动。BOSS 上程序化滚动会产生没有滚轮事件的
 * scroll(留痕形态未验),滚轮注入尚未实现;历史超出一屏时 truncated=true 如实带出。
 */
async function captureBossThreadScreenshot(
  args: ChatCaptureThreadScreenshotArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<CaptureScreenshotData> {
  if (!args || typeof args.conversationRef !== 'string' || !args.conversationRef) {
    throw new PlatformError('GUARD_FAILED', '聊天截图缺少目标会话引用', 'manualOnly')
  }
  if (!sessionBlobParams()) {
    throw new PlatformError('PAYLOAD_LIMIT', '当前会话未协商 blob 通道,禁止内联图像', 'manualOnly')
  }
  const tab = await verifiedBossChatTab(fingerprint)
  if (tab.id === undefined || tab.windowId === undefined || tab.status !== 'complete') {
    throw new PlatformError('CTX_NOT_READY', '目标 BOSS 页面尚未就绪', 'afterRecovery', 'pageBroken')
  }
  if (!tab.active) throw new PlatformError('CTX_NOT_READY', '目标标签页不在前台,放弃截图', 'afterRecovery')
  const row = await locateRow(tab.id, args.conversationRef)
  if (!(row.count === 1 && row.selected)) {
    throw new PlatformError('CTX_LOST_DURING_EXEC', '截图目标不是当前打开的会话', 'manualOnly')
  }
  ctx.checkpoint()
  const area = await runInPage(BOSS_DOM, tab.id, domReadBossChatRect, [CHAT_LIST_SELECTOR])
  if (!area.found) throw new PlatformError('ELEMENT_UNRESOLVED', '聊天区容器无法解析', 'manualOnly')
  if (!area.visible) throw new PlatformError('CTX_NOT_READY', '页面不可见,放弃截图', 'afterRecovery')
  const left = Math.max(0, area.rect.x)
  const top = Math.max(0, area.rect.y)
  const right = Math.min(area.innerW, area.rect.x + area.rect.w)
  const bottom = Math.min(area.innerH, area.rect.y + area.rect.h)
  if (right - left < 8 || bottom - top < 8) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '聊天区不在视口内,无有效像素', 'manualOnly')
  }
  let frame: ImageBitmap
  try {
    frame = await decodeFrame(await captureVisibleTabJpegDataUrl(tab.windowId, 92))
  } catch (error) {
    throw new PlatformError('CTX_NOT_READY', `截屏一帧未得:${describeError(error).slice(0, 200)}`, 'afterRecovery')
  }
  const dpr = area.dpr
  const sx = Math.min(Math.round(left * dpr), Math.max(0, frame.width - 1))
  const sy = Math.min(Math.round(top * dpr), Math.max(0, frame.height - 1))
  const sw = Math.max(1, Math.min(frame.width - sx, Math.round((right - left) * dpr)))
  const sh = Math.max(1, Math.min(frame.height - sy, Math.round((bottom - top) * dpr)))
  const canvas = new OffscreenCanvas(sw, sh)
  const draw = canvas.getContext('2d')
  if (!draw) { frame.close(); throw new PlatformError('CTX_NOT_READY', '截图画布不可用', 'afterRecovery') }
  draw.fillStyle = '#ffffff'
  draw.fillRect(0, 0, sw, sh)
  draw.drawImage(frame, sx, sy, sw, sh, 0, 0, sw, sh)
  frame.close()
  let jpeg = await canvas.convertToBlob({ type: 'image/jpeg', quality: 0.92 })
  if (jpeg.size > 1_900_000) jpeg = await canvas.convertToBlob({ type: 'image/jpeg', quality: 0.7 })
  let put: BlobPutOutcome
  try {
    put = await putSessionBlob(await jpeg.arrayBuffer())
  } catch (error) {
    if (error instanceof BlobChannelError) {
      throw new PlatformError(error.permanent ? 'PAYLOAD_LIMIT' : 'CTX_NOT_READY',
        `截图 blob 上行失败:${error.message}`, error.permanent ? 'manualOnly' : 'afterRecovery')
    }
    throw error
  }
  await verifiedBossChatTab(fingerprint)
  ctx.progress('聊天截图完成', 100)
  return {
    imageBlobRef: put.ref,
    byteSize: put.byteSize,
    truncated: area.scrollHeight > area.clientHeight + 4,
    capturedAt: Date.now(),
  }
}


// ── candidate.readResume(摘要级,零点击) ─────────────────────────────────────
//
// BOSS 的「在线简历」面板正文画在一个同源 iframe 的 canvas 上,DOM 与内存都没有描述文字
// (平台事实 §十三,2026-09-03)。机器能拿到的简历是摘要级:右侧候选人信息区的
// `conversation$` 里有年龄、年限、学历、城市、活跃时间、工作经历抬头与教育经历,
// 打开会话即有,不需要点面板。自我评价填空串(智联缺它时同款)。期望分区为空:
// `conversation$` 上的 toPosition/salaryDesc 是我方沟通职位与薪资,不是候选人期望。
// 性别码(gender)与文案的对应未真机验证,按枚举面事实门不映射、整行省略。

interface BossResumeRead {
  status: 'ready' | 'none' | 'ambiguous' | 'mismatch'
  name: string
  ageDesc: string
  year: string
  edu: string
  city: string
  activeTimeDesc: string
  work: Array<{ company: string; positionName: string; timeDesc: string }>
  education: Array<{ school: string; major: string; degree: string; timeDesc: string }>
}

/** 页面里读当前会话的候选人摘要。自包含;只取平台事实 §十三 列出的字段。 */
function mainReadBossResume(uid: number, friendSource: number): BossResumeRead {
  type AnyRecord = Record<string, unknown>
  const empty: BossResumeRead = {
    status: 'none', name: '', ageDesc: '', year: '', edu: '', city: '', activeTimeDesc: '', work: [], education: [],
  }
  const str = (value: unknown): string => (typeof value === 'string' ? value : '')
  const seen = new Set<unknown>()
  const found = new Map<string, AnyRecord>()
  for (const element of Array.from(document.querySelectorAll('*'))) {
    const instance = (element as unknown as { __vue__?: AnyRecord }).__vue__
    if (!instance || seen.has(instance)) continue
    seen.add(instance)
    if (!Object.prototype.hasOwnProperty.call(instance, 'conversation$')) continue
    let conversation: unknown
    try { conversation = instance['conversation$'] } catch { continue }
    if (!conversation || typeof conversation !== 'object' || Array.isArray(conversation)) continue
    const record = conversation as AnyRecord
    if (typeof record.uid !== 'number' || typeof record.friendSource !== 'number') continue
    const key = `${record.uid}-${record.friendSource}`
    if (!found.has(key)) found.set(key, record)
  }
  if (found.size === 0) return empty
  if (found.size > 1) return { ...empty, status: 'ambiguous' }
  const record = [...found.values()][0]!
  if (record.uid !== uid || record.friendSource !== friendSource) return { ...empty, status: 'mismatch' }
  const list = (value: unknown): AnyRecord[] =>
    Array.isArray(value) ? value.filter((item): item is AnyRecord => !!item && typeof item === 'object') : []
  return {
    status: 'ready',
    name: str(record.name),
    ageDesc: str(record.ageDesc),
    year: str(record.year),
    edu: str(record.edu),
    city: str(record.city),
    activeTimeDesc: str(record.activeTimeDesc),
    work: list(record.workExpList).map((item) => ({
      company: str(item.company), positionName: str(item.positionName), timeDesc: str(item.timeDesc),
    })),
    education: list(record.eduExpList).map((item) => ({
      school: str(item.school), major: str(item.major), degree: str(item.degree), timeDesc: str(item.timeDesc),
    })),
  }
}

/**
 * 摘要 → 契约五分区。标签名与智联对齐(脑侧评分按「工作经验」→工作年限、「现居地」→现居映射,
 * 沉默追问按「年龄」「性别」取值——性别本平台读不到,那条链在 BOSS 上会缺参,已登记):
 * 值为空的行整行省略,不填占位。
 */
export function projectBossResume(
  read: BossResumeRead, conversationRef: string, platformUserRef: string, observedAt: number,
): CandidateReadResumeData {
  const clean = (value: string): string => normalizeBossMessageText(value)
  const basic: CandidateResumeLabelValue[] = []
  const push = (label: string, value: string): void => {
    const cleaned = clean(value)
    if (cleaned) basic.push({ label, value: cleaned })
  }
  push('姓名', read.name)
  push('年龄', read.ageDesc)
  push('工作经验', read.year)
  push('最高学历', read.edu)
  push('现居地', read.city)
  push('活跃时间', read.activeTimeDesc)
  const joinParts = (parts: string[]): string => parts.map(clean).filter(Boolean).join(' · ')
  const workExperiences = read.work
    .map((item) => [clean(item.timeDesc), joinParts([item.company, item.positionName])].filter(Boolean).join(' '))
    .filter(Boolean)
    .join('\n\n')
  const education = read.education
    .map((item) => [clean(item.timeDesc), joinParts([item.school, item.major, item.degree])].filter(Boolean).join(' '))
    .filter(Boolean)
    .join('\n\n')
  return {
    conversationRef,
    platformUserRef,
    observedAt,
    basic,
    expectations: [],
    selfEvaluation: '',
    education,
    workExperiences,
  }
}

async function readBossResume(
  args: CandidateReadResumeArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<CandidateReadResumeData> {
  if (validatePrimitiveArgs(PrimitiveName.CandidateReadResume, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '简历读取参数不符合当前契约', 'manualOnly')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'manualOnly')
  if (args.platformUserRef !== String(parsed.uid)) {
    throw new PlatformError('GUARD_FAILED', '候选人引用与会话引用不属于同一人', 'manualOnly')
  }
  const tab = await verifiedBossChatTab(fingerprint)
  ctx.checkpoint()
  // 摘要挂在当前会话对象上,会话没打开就读不到:与 readThread 同款,行未 selected 时自己点开。
  await ensureBossThreadOpen(tab, ctx, fingerprint, args.conversationRef)
  const settled = await pollUntil(ctx,
    () => runInPage(BOSS_INJECT, tab.id!, mainReadBossResume, [parsed.uid, parsed.friendSource]),
    (read) => read.status === 'ready' || read.status === 'ambiguous')
  const read = settled.value
  if (read.status !== 'ready') {
    throw new PlatformError('ELEMENT_UNRESOLVED', `当前会话的候选人摘要读不到(${read.status})`, 'afterRecovery')
  }
  const data = projectBossResume(read, args.conversationRef, args.platformUserRef, Date.now())
  if (!data.workExperiences && !data.education && data.basic.length <= 1) {
    // 只有姓名、其余全空:不像"这个人简历空",更像页面没填好;不返回一份空简历冒充读到。
    throw new PlatformError('ELEMENT_UNRESOLVED', '候选人摘要除姓名外全空,拒绝返回空简历', 'afterRecovery')
  }
  if (jsonBytes(data) > 65_536) throw new PlatformError('PAYLOAD_LIMIT', '简历摘要超过内联载荷上限', 'manualOnly')
  if (validatePrimitiveData(PrimitiveName.CandidateReadResume, 1, data).length !== 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '简历摘要不符合当前契约', 'manualOnly')
  }
  await verifiedBossChatTab(fingerprint)
  ctx.progress('简历摘要读取完成', 100)
  return data
}

/** 只为 Node 单测导出纯函数与页面函数;生产 bundle 无引用时被 tree-shake。 */
export const bossTestHooks = Object.freeze({
  bossThreadReadSettled,
  domReadBossOverlays,
  domHitTestOverlayCloser,
  domHitTestIndexed,
  domLocateBySelector,
  domReadScrollMetrics,
  domHitTestExpected,
  mainReadBossResume,
  projectBossResume,
  bossConversationRef,
  parseBossConversationRef,
  parseBossUnreadBadgeText,
  projectBossMessage,
  pendingBossWechatRequests,
  selectBossExchangeResult,
  domReadBossWechatButton,
  domReadBossExchangeTooltip,
  domReadBossAcceptButton,
  domAcceptGate,
  mainReadBossWechatState,
  matchAnchorTail,
  summarizeBossListRow,
  newlinesToSpaces,
  domSendGate,
  identityCacheUsable,
  domReadBossListState,
  domLocateBossRow,
  mainReadBossListWindow,
  mainReadBossCurrentConversation,
  mainReadBossThread,
})

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
  // 考古探针(2026-09-03):滚轮,selector 由调用方给。
  osScroll: ({ args, ctx, fingerprint }) => bossOsScroll(args, ctx, fingerprint),
  osClick: ({ args, ctx, fingerprint }) => bossOsClick(args, ctx, fingerprint),

  // 场景一的七条(2026-09-03 开工):会话感知与回复。
  readList: ({ args, ctx, fingerprint }) => readBossList(args, ctx, fingerprint),
  readThread: ({ args, ctx, fingerprint }) => readBossThread(args, ctx, fingerprint),
  readUnreadTotal: ({ fingerprint }) => readBossUnreadTotal(fingerprint),
  identifyCurrentConversation: ({ fingerprint }) => identifyBossCurrentConversation(fingerprint),
  openConversation: ({ args, ctx, fingerprint }) => openBossConversation(args, ctx, fingerprint),
  sendMessage: ({ args, guards, ctx, fingerprint }) => sendBossMessage(args, guards, ctx, fingerprint),
  // 场景二的三条(2026-09-04 出口):换微信线。
  sendWechatInvite: ({ args, guards, ctx, fingerprint }) => sendBossWechatInvite(args, guards, ctx, fingerprint),
  acceptWechat: ({ args, guards, ctx, fingerprint }) => acceptBossWechat(args, guards, ctx, fingerprint),
  readWechatExchangeOutcome: ({ args, ctx, fingerprint }) => readBossWechatExchangeOutcome(args, ctx, fingerprint),
  captureThreadScreenshot: ({ args, ctx, fingerprint }) => captureBossThreadScreenshot(args, ctx, fingerprint),
  // 建档后的简历补采(2026-09-03 甲方选 B):摘要级、零点击,见 readBossResume。
  readResume: ({ args, ctx, fingerprint }) => readBossResume(args, ctx, fingerprint),
} satisfies PlatformAdapter
