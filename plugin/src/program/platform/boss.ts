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
import type { OsScrollResult, ScrollTarget } from './osscroll'
import type { ClickObservation, ClickPlan, RetreatPlan, TypePlayResult } from './osinput'
import { PlatformError } from './types'
import { BOSS_CHAT_URL, BOSS_MATCH, BOSS_ORIGIN, BOSS_PLATFORM, bossSite } from './bossSite'
import type { InjectOptions } from './inject'
import type { PlatformAdapter } from './types'
import type { PrimitiveContext } from '../registry'
import { Primitive as PrimitiveName, validatePrimitiveArgs, validatePrimitiveData } from '../../base/protocol'
import { BlobChannelError, captureVisibleTabJpegDataUrl, putSessionBlob, sessionBlobParams } from '../../base/capture'
import { describeError, reportHandLog } from '../../base/handLog'
import type { BlobPutOutcome } from '../../base/capture'
import type {
  CandidateApplySourcingFiltersArgs,
  CandidateApplySourcingFiltersData,
  CandidateContactState,
  CandidateReadResumeArgs,
  CandidateReadResumeData,
  CandidateReadSourcingResumeData,
  CandidateReadSourcingTargetResumeArgs,
  CandidateReadSourcingWindowArgs,
  CandidateReadSourcingWindowData,
  CandidateResumeLabelValue,
  CandidateSelectSourcingPositionArgs,
  CandidateSelectSourcingPositionData,
  CandidateSourcingFilters,
  CaptureScreenshotData,
  ChatCaptureThreadScreenshotArgs,
  ChatIdentifyCurrentConversationData,
  ChatOpenConversationArgs,
  ChatOpenConversationData,
  ChatReadGreetingOutcomeArgs,
  ChatReadGreetingOutcomeData,
  ChatReadListArgs,
  ChatReadListData,
  ChatReadThreadArgs,
  ChatReadThreadData,
  ChatReadUnreadTotalData,
  ChatAcceptWechatArgs,
  ChatAcceptWechatData,
  ChatReadWechatExchangeOutcomeArgs,
  ChatReadWechatExchangeOutcomeData,
  ChatSendGreetingArgs,
  ChatSendGreetingData,
  ChatSendGreetingGuards,
  ChatSendInviteCardArgs,
  ChatSendInviteCardData,
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
  InterviewDetails,
  InterviewMethod,
  JobPostingSection,
  JobReadPublishedListData,
  MessageAnchor,
  NavEnsureSurfaceArgs,
  NavEnsureSurfaceData,
  PeerSummary,
  ProbePlatformData,
  SourcingCareerStatus,
  SourcingEducation,
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
async function bossComposerClickPlan(tabId: number, composerId: string = COMPOSER_ID): Promise<ClickPlan> {
  const before = await runInPage(BOSS_DOM, tabId, mainReadComposer, [composerId])
  if (!before.found) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '页面上找不到聊天输入框', 'manualOnly')
  }
  return {
    label: `输入框(点前 焦点=${before.focused ? '在' : '不在'})`,
    rect: { x: before.x, y: before.y, w: before.w, h: before.h },
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, mainHitTestComposer, [composerId, x, y]),
    observe: async (): Promise<ClickObservation> => {
      const after = await runInPage(BOSS_DOM, tabId, mainReadComposer, [composerId])
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
//
// retryable 口径(2026-09-04 甲方逐条裁决,与场景二三同款):不可逆点击之前的失败一律 afterRecovery。
// 只读原语抛进巡检的错误按 no/manualOnly 会被脑侧隔离会话(会话级命令)或停整个账号(readList 这类
// 账号级命令),afterRecovery 只是本轮跳过、下一轮 readList(reset) 自带恢复;effectful 的 sendMessage
// 脑侧只看 sideEffect,点前提示改的是口径。保留的两处:readList 职位范围不是「全部职位」仍 manualOnly
// (人确实得切回去,停账号是唯一可见信号,直到把切回自动化);move=next 的 no 由脑侧专用分支识别为
// 「翻不了窗」部分收束。POSTCONDITION_UNCONFIRMED 的 manualOnly 是点后路径,不动。
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


// ── 实发正文即事实(2026-09-07 甲方裁决,AGENTS 防护成本预算第 9 条同名段) ──────────────
//
// 发送前「回读逐字等于文案」的拒绝解除,换成三道确定性判据:TIP 活动核对(Windows)、
// 上屏地板(字数差 + 编辑距离相似度)、点击时编辑器等于刚被接受的回读文本;发后按派发窗口
// 认行,实发正文随 data.sentText 回脑。下面几个纯函数是这条规则在手侧的全部判定点,单测钉住。

/** 上屏地板的两个数。起步值,Windows / Mac 各跑出数据再标定;它只挡灾难性错乱,挡不了同音错字。 */
export const TYPED_TEXT_FLOOR = Object.freeze({ maxLengthDiff: 2, minSimilarity: 0.7 })

export interface TypedTextFloorResult {
  ok: boolean
  exact: boolean
  expectedLength: number
  actualLength: number
  lengthDiff: number
  distance: number
  similarity: number
}

/** 字元级编辑距离(按 code point,不按 UTF-16 单元)。文案至多几百字,O(n·m) 足够。 */
export function levenshteinChars(a: readonly string[], b: readonly string[]): number {
  if (a.length === 0) return b.length
  if (b.length === 0) return a.length
  let previous = Array.from({ length: b.length + 1 }, (_, i) => i)
  for (let i = 1; i <= a.length; i += 1) {
    const current = [i]
    for (let j = 1; j <= b.length; j += 1) {
      current.push(Math.min(previous[j]! + 1, current[j - 1]! + 1, previous[j - 1]! + (a[i - 1] === b[j - 1] ? 0 : 1)))
    }
    previous = current
  }
  return previous[b.length]!
}

/**
 * 上屏地板:回读与目标文案(清洗后)规范化后比,字数差 ≤ maxLengthDiff 且相似度 ≥ minSimilarity 才放行。
 * 字数差挡「只打了一半」与拼音字母残留(它们的相似度可能还不低);相似度挡空白、错窗、乱码。
 * 同音错字天生相似度高(09-03 真机一例 0.90),按裁决照发。
 */
export function typedTextFloor(expected: string, actual: string): TypedTextFloorResult {
  const target = Array.from(normalizeBossMessageText(expected))
  const got = Array.from(normalizeBossMessageText(actual))
  const exact = target.length === got.length && target.every((ch, i) => ch === got[i])
  const distance = exact ? 0 : levenshteinChars(target, got)
  const similarity = 1 - distance / Math.max(target.length, got.length, 1)
  const lengthDiff = Math.abs(target.length - got.length)
  return {
    ok: exact || (got.length > 0 && lengthDiff <= TYPED_TEXT_FLOOR.maxLengthDiff && similarity >= TYPED_TEXT_FLOOR.minSimilarity),
    exact, expectedLength: target.length, actualLength: got.length, lengthDiff, distance, similarity,
  }
}

/** 地板判定的留痕文本:只有数字,不带正文(候选人称呼可能在文案里)。 */
export function describeTypedTextFloor(result: TypedTextFloorResult): string {
  return `期望 ${result.expectedLength} 字,实得 ${result.actualLength} 字,字数差 ${result.lengthDiff},编辑距离 ${result.distance},相似度 ${result.similarity.toFixed(2)}`
}

/**
 * TIP 活动核对(Windows):手服务驱动了上屏词却回报少于计划,说明键落到了系统输入法——
 * 屏上是微软拼音的首选词,与「TIP 装了但选错」在现场长得一样,发送前必须拒。
 * 不驱动上屏词的平台(macOS)没有这道核对,返回 null。
 */
export function tipWordsShortfall(
  played: Pick<TypePlayResult, 'wordsDriven' | 'wordsPlanned' | 'wordsCommitted' | 'words'>,
): string | null {
  if (!played.wordsDriven) return null
  const planned = played.wordsPlanned ?? 0
  const committed = played.wordsCommitted ?? 0
  if (planned <= 0 || committed >= planned) return null
  return `TIP 只上屏了 ${committed}/${planned} 词,当前输入法可能不是我们的 TIP${played.words ? `(${played.words})` : ''}`
}

/** 清洗摘除的字元只按 kind 计数留痕,不记字元本身(可能是称呼里的生僻字)。 */
export function summarizeDroppedKinds(dropped: readonly { kind: string }[]): string {
  const counts = new Map<string, number>()
  for (const item of dropped) counts.set(item.kind, (counts.get(item.kind) ?? 0) + 1)
  return Array.from(counts, ([kind, n]) => `${kind} ${n}`).join(',')
}

export interface SentBossRow {
  row: BossRawMessage
  /** 实发正文(已规范化,与 hashInput 同源)。 */
  text: string
  hashInput: string
}

/**
 * 发后认行(《协议规格-v1》§9.4.1 同款口径的手侧即时版):基线之外、方向 out、投影为文本、
 * 服务端已确认(status 1 已送达 / 2 已读;乐观本地行与在途行不算)、时间不早于派发减容差的行里
 * 取 mid 最大者即本次;正文与 hash 以该行为准。零匹配返回 null,由调用方按 possible 交验证读。
 */
export function pickSentBossRow(
  rows: readonly BossRawMessage[], baselineMids: ReadonlySet<string>, dispatchedAt: number,
): SentBossRow | null {
  let best: SentBossRow | null = null
  for (const row of rows) {
    if (baselineMids.has(row.mid) || row.direction !== 'out' || !(row.status === 1 || row.status === 2)) continue
    if (row.time !== null && row.time < dispatchedAt - SEND_CLOCK_TOLERANCE_MS) continue
    const projected = projectBossMessage(row)
    if (projected.kind !== 'text' || !projected.text) continue
    if (!best || Number(row.mid) > Number(best.row.mid)) best = { row, text: projected.text, hashInput: projected.hashInput }
  }
  return best
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
): { onTarget: boolean; found: string; occluded?: boolean } {
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
  // `A >>> B` 框架跳转,与 domLocateBySelector 同一约定(那里有说明);遮挡物可能在顶层
  // (盖在 iframe 上的浮层)也可能在 iframe 里,两层各问一次,签名都带出去。
  const hop = selector.split('>>>')
  let root: Document | null = document
  let inner = selector
  let frame: Element | null = null
  let offX = 0
  let offY = 0
  if (hop.length === 2) {
    inner = hop[1].trim()
    try {
      frame = document.querySelector(hop[0].trim())
    } catch {
      frame = null
    }
    root = frame && 'contentDocument' in frame ? (frame as HTMLIFrameElement).contentDocument : null
    if (frame) {
      const fr = frame.getBoundingClientRect()
      offX = fr.left + frame.clientLeft
      offY = fr.top + frame.clientTop
    }
  } else if (hop.length > 2) {
    root = null
  }
  let target: Element | undefined
  try {
    target = root ? Array.from(root.querySelectorAll(inner))[index] : undefined
  } catch {
    target = undefined
  }
  if (!target || !root) return { onTarget: false, found: '靶子已经不在原来的位置上' }
  if (frame) {
    const topAt = document.elementFromPoint(x, y)
    if (topAt !== frame) return topAt ? { onTarget: false, occluded: true, found: `遮挡物(顶层) ${signature(topAt)}` } : { onTarget: false, found: '落点上什么都没有' }
  }
  const at = root.elementFromPoint(x - offX, y - offY)
  if (!at) return { onTarget: false, found: '落点上什么都没有' }
  const onTarget = at === target || target.contains(at)
  // 不命中时把遮挡物的签名带出去(类名链、尺寸、文本头几个字):它是清场白名单的唯一数据来源。
  return onTarget ? { onTarget, found: `靶子(${at.tagName.toLowerCase()})` } : { onTarget, occluded: true, found: `遮挡物 ${signature(at)}` }
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
  // 邀面卡三码按 bizType 分支,condition 只作印证(出口 §2.1,平台事实 §四/§十六 两时机复核,2026-09-04):
  // condition 是快照永不回写,接受与取消是独立新行不是跃迁。我方发出的 21130009 投 unknown——意图 ok 收编把账本行
  // 写成 unknown,投 pending 会在下一轮读回时被当成 unknown→pending 跃迁转人工;21130008 接受投 accepted;
  // 21130006 取消不论方向都投 system——出站 interviewInvite 任何状态都会被脑读成「又发了一张邀请」,入站非
  // accepted 邀面卡会判 unknownEvent 转人工,「已约面后候选人取消」另立规格案。三码之外的 condition 值按枚举面
  // 事实门归 unknown 并把原始值带进日志。
  if (bizType === 21130009 || bizType === 21130008) {
    const expected = bizType === 21130009 ? 1 : 3
    const cardState: ThreadMessage['cardState'] = bizType === 21130008 && raw.interviewCondition === 3 ? 'accepted' : 'unknown'
    return {
      kind: 'card', direction: raw.direction, text: text || null,
      cardType: 'interviewInvite', cardState,
      hashInput: 'card\x1finterviewInvite',
      ...(raw.interviewCondition === expected ? {} : { unrecognized: `bizType=${bizType} condition=${raw.interviewCondition ?? 'null'}` }),
    }
  }
  if (bizType === 21130006) {
    const label = text || '[系统消息:21130006]'
    return {
      kind: 'system', direction: raw.direction, text: label, hashInput: label,
      ...(raw.interviewCondition === 5 ? {} : { unrecognized: `bizType=21130006 condition=${raw.interviewCondition ?? 'null'}` }),
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
    bizType === 21130011 || bizType === 21050004 || bizType === 14 ||
    // 21050008:候选人接受面试时自动发的附件简历(文本行 + hyperLink 行,平台事实 §十六);投 card/resumeAttachment
    // 触发 EventResumeSubmitted 是出口 §2.1 的后置项,本轮只去掉 unrecognized 噪音。
    bizType === 21050008
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
 *
 * **框架跳转 `A >>> B`**:A 是顶层文档里的同源 iframe,B 在它的文档里定位,只支持一层。
 * 矩形一律换算回顶层视口坐标(加 iframe 的位置与边框),可见部分同时裁到 iframe 视口与顶层视口——
 * 光标最终落在屏幕上,而屏幕只认顶层坐标。BOSS 推荐页整张列表画在 `iframe[name=recommendFrame]`
 * 里,顶层 `querySelectorAll` 看不见它(2026-09-04 真机)。下面四个页面函数各自内联这段解析:
 * 它们经 executeScript 序列化注入,引用不到模块里的共享函数。
 */
function domLocateBySelector(selector: string, index: number): DomLocated {
  const hop = selector.split('>>>')
  if (hop.length > 2) return { status: 'bad_selector', count: 0, detail: '>>> 只支持一层 iframe' }
  let root: Document = document
  let inner = selector
  let offX = 0
  let offY = 0
  let view = { left: 0, top: 0, right: window.innerWidth, bottom: window.innerHeight }
  if (hop.length === 2) {
    inner = hop[1].trim()
    let frame: Element | null
    try {
      frame = document.querySelector(hop[0].trim())
    } catch (error) {
      return { status: 'bad_selector', count: 0, detail: `iframe 选择器无效:${String(error).slice(0, 120)}` }
    }
    const doc = frame && 'contentDocument' in frame ? (frame as HTMLIFrameElement).contentDocument : null
    if (!frame || !doc) return { status: 'none', count: 0, detail: 'iframe 没命中,或不同源、读不到它的文档' }
    const fr = frame.getBoundingClientRect()
    offX = fr.left + frame.clientLeft
    offY = fr.top + frame.clientTop
    view = {
      left: Math.max(0, offX), top: Math.max(0, offY),
      right: Math.min(window.innerWidth, offX + frame.clientWidth), bottom: Math.min(window.innerHeight, offY + frame.clientHeight),
    }
    root = doc
  }
  let all: Element[]
  try {
    all = Array.from(root.querySelectorAll(inner))
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
  // 靶子是文档本身(html / body)时,矩形按 scrollingElement 的内容高度合成:html/body 的
  // getBoundingClientRect 只有视口那么高、滚过一屏后整个矩形跑到视口上方,可见部分算成 0,
  // 第二次滚就被当 offscreen 拒掉(2026-09-04 真机,BOSS 推荐页 iframe 文档滚 720 后)。
  const own = el.ownerDocument
  const isDoc = !!own && (el === own.documentElement || el === own.body)
  const se = isDoc && own ? own.scrollingElement : null
  const r0 = isDoc && se
    ? { x: 0, y: -se.scrollTop, width: se.clientWidth, height: se.scrollHeight, left: 0, top: -se.scrollTop, right: se.clientWidth, bottom: se.scrollHeight - se.scrollTop }
    : el.getBoundingClientRect()
  const r = { x: r0.x + offX, y: r0.y + offY, width: r0.width, height: r0.height, left: r0.left + offX, top: r0.top + offY, right: r0.right + offX, bottom: r0.bottom + offY }
  const left = Math.max(view.left, r.left)
  const top = Math.max(view.top, r.top)
  const right = Math.min(view.right, r.right)
  const bottom = Math.min(view.bottom, r.bottom)
  const clip = { x: left, y: top, w: Math.max(0, right - left), h: Math.max(0, bottom - top) }
  const parts: string[] = []
  let cursor: Element | null = el
  for (let depth = 0; cursor && cursor !== root.body && depth < 4; depth += 1) {
    const cls = typeof cursor.className === 'string' ? cursor.className.trim().split(/\s+/u).slice(0, 2).join('.') : ''
    parts.push(cursor.tagName.toLowerCase() + (cls ? '.' + cls : ''))
    cursor = cursor.parentElement
  }
  const text = (el.textContent ?? '').trim()
  const signature = `${parts.join('<')}[${Math.round(r.width)}x${Math.round(r.height)}]「${text.slice(0, 8)}」`
  // 考古探针的落点余量:16px。BOSS 邀面表单的 radio 标签(20px 高)与时间页签(22px)都比 24 小,
  // 而落点抖动只有几个像素、标定就绪时偏 0(2026-09-04 真机);生产原语各自的计划另有判据,不走这里。
  if (!(clip.w >= 16 && clip.h >= 16)) {
    return { status: 'offscreen', count: all.length, detail: `${signature} 可见部分只有 ${Math.round(clip.w)}x${Math.round(clip.h)},光标没处落` }
  }
  return { status: 'ok', count: all.length, index: i, rect: { x: r.x, y: r.y, w: r.width, h: r.height }, clip, text, signature }
}

/** 容器的滚动指标。找不到就 found=false,不抛。selector 同样接受 `A >>> B` 框架跳转。 */
function domReadScrollMetrics(selector: string, index: number): { found: boolean; scrollTop: number; scrollHeight: number; clientHeight: number } {
  const hop = selector.split('>>>')
  let root: Document | null = document
  let inner = selector
  if (hop.length === 2) {
    inner = hop[1].trim()
    let frame: Element | null = null
    try {
      frame = document.querySelector(hop[0].trim())
    } catch {
      frame = null
    }
    root = frame && 'contentDocument' in frame ? (frame as HTMLIFrameElement).contentDocument : null
  } else if (hop.length > 2) {
    root = null
  }
  let el: Element | undefined
  try {
    el = root ? Array.from(root.querySelectorAll(inner))[index] : undefined
  } catch {
    el = undefined
  }
  if (!el) return { found: false, scrollTop: 0, scrollHeight: 0, clientHeight: 0 }
  // 整个文档在滚(靶子是 html 或 body)时读 scrollingElement:标准模式下 body.scrollTop 恒为 0,
  // 读它会把真滚了的页面报成 stuck。BOSS 推荐列表就是 iframe 文档自己在滚(2026-09-04 真机)。
  const doc = el.ownerDocument
  const scroller = doc && (el === doc.documentElement || el === doc.body) && doc.scrollingElement ? doc.scrollingElement : el
  return { found: true, scrollTop: scroller.scrollTop, scrollHeight: scroller.scrollHeight, clientHeight: scroller.clientHeight }
}

/**
 * 考古点击的命中测试:落点上是 selector[index] 或其后代,**且**给了 expectText 时元素此刻的
 * 文本仍逐字相等——这是点击前的最后一次读。列表重排后同一个 index 可能指到另一行,
 * 只看 index 会点错人;文本是那一行的身份。
 * 带 `A >>> B` 时先问顶层"这个像素上是不是那个 iframe"——顶层的浮层(快捷聊天窗、弹窗)会盖在
 * iframe 上,顶层 elementFromPoint 只会回答 iframe 本身,盖没盖要在顶层问;再把坐标减去 iframe
 * 偏移进它的文档问第二次。
 */
function domHitTestExpected(
  selector: string, index: number, expectText: string | null, x: number, y: number,
): { onTarget: boolean; found: string; occluded?: boolean } {
  const hop = selector.split('>>>')
  let root: Document | null = document
  let inner = selector
  let frame: Element | null = null
  let offX = 0
  let offY = 0
  if (hop.length === 2) {
    inner = hop[1].trim()
    try {
      frame = document.querySelector(hop[0].trim())
    } catch {
      frame = null
    }
    root = frame && 'contentDocument' in frame ? (frame as HTMLIFrameElement).contentDocument : null
    if (frame) {
      const fr = frame.getBoundingClientRect()
      offX = fr.left + frame.clientLeft
      offY = fr.top + frame.clientTop
    }
  } else if (hop.length > 2) {
    root = null
  }
  let target: Element | undefined
  try {
    target = root ? Array.from(root.querySelectorAll(inner))[index] : undefined
  } catch {
    target = undefined
  }
  if (!target || !root) return { onTarget: false, found: '靶子已经不在原来的位置上' }
  if (frame) {
    const topAt = document.elementFromPoint(x, y)
    if (topAt !== frame) {
      // 顶层盖在 iframe 上的东西(头像 hover 弹层、快捷窗):报 occluded 给引擎一个退让的理由。
      return { onTarget: false, occluded: true, found: `落点上是顶层的别的元素 ${topAt ? topAt.tagName.toLowerCase() + '「' + (topAt.textContent ?? '').trim().slice(0, 8) + '」' : '(空)'}` }
    }
  }
  const at = root.elementFromPoint(x - offX, y - offY)
  if (!at) return { onTarget: false, found: '落点上什么都没有' }
  if (!(at === target || target.contains(at))) {
    return { onTarget: false, occluded: true, found: `落点上是别的元素 ${at.tagName.toLowerCase()}「${(at.textContent ?? '').trim().slice(0, 8)}」` }
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
  if (located.count > 1) throw new PlatformError('ELEMENT_UNRESOLVED', '目标会话在列表里不唯一', 'afterRecovery')
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
      throw new PlatformError('ELEMENT_UNRESOLVED', '当前没有打开的会话', 'afterRecovery')
    }
    if (current.status === 'ambiguous') {
      throw new PlatformError('ELEMENT_UNRESOLVED', `页面同时呈现 ${current.count} 个当前会话`, 'afterRecovery')
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
    throw new PlatformError('GUARD_FAILED', '会话列表读取参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
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
    throw new PlatformError('ELEMENT_UNRESOLVED', '会话列表结果不符合当前契约', 'afterRecovery')
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
    throw new PlatformError('GUARD_FAILED', '打开会话参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  if (!parseBossConversationRef(args.conversationRef)) {
    throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'afterRecovery')
  }
  const tab = await verifiedBossChatTab(fingerprint)
  const tabId = tab.id!
  // 契约:只在当前已回读为「全部职位+未读」的 fresh 列表里打开。
  const state = await readListState(tabId)
  if (state.jobLabel !== JOB_ALL || state.labelSelected !== LABEL_ALL || state.subTabActive !== SUB_TAB_UNREAD) {
    throw new PlatformError('GUARD_FAILED',
      `当前列表不是「全部职位+未读」(职位「${state.jobLabel}」页签「${state.labelSelected}」小页签「${state.subTabActive}」)`,
      'afterRecovery')
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
    throw new PlatformError('GUARD_FAILED', '会话读取参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'afterRecovery')
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
    throw new PlatformError('ELEMENT_UNRESOLVED', '会话读取结果不符合当前契约', 'afterRecovery', undefined, 'possible')
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
    throw new PlatformError('ELEMENT_UNRESOLVED', `发送钮认不出(命中 ${button.count} 个)`, 'afterRecovery')
  }
  if (button.text !== '发送') {
    throw new PlatformError('ELEMENT_UNRESOLVED', `发送钮文案不是「发送」(读到「${button.text}」)`, 'afterRecovery')
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
  tabId: number, ctx: PrimitiveContext, trace: string[], beforeLength: number, composerId: string = COMPOSER_ID,
): Promise<void> {
  let played
  try {
    played = await playKeys(composeClearKeys(await readHandOS()))
  } catch (error) {
    if (isHandServiceDown(error)) {
      throw new PlatformError('CTX_NOT_READY', '手服务不可用,清空输入框未开始', 'afterRecovery', 'pageBroken')
    }
    throw new PlatformError('ELEMENT_UNRESOLVED',
      `清空输入框的按键半途失败:${describeError(error).slice(0, 300)}`, 'afterRecovery')
  }
  const settled = await pollUntil(ctx,
    () => runInPage(BOSS_DOM, tabId, mainReadComposer, [composerId]),
    (read) => read.found && read.text === '', CLEAR_WAIT_MS)
  if (!settled.satisfied) {
    throw new PlatformError('ELEMENT_UNRESOLVED',
      `全选删除后输入框仍有 ${settled.value.text.length} 字,未清空`, 'afterRecovery')
  }
  trace.push(`清掉 ${beforeLength} 字旧内容(${played.keys} 次按键)`)
  reportHandLog('warn', 'composerDraftCleared',
    `BOSS 打字前清掉输入框里 ${beforeLength} 字旧内容(2026-09-03 裁决:草稿不再保护)`)
}

async function sendBossMessage(
  args: ChatSendMessageArgs, guards: ChatSendMessageGuards, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<ChatSendMessageData> {
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'afterRecovery')
  const normalizedText = normalizeBossMessageText(args.text)
  if (!normalizedText) throw new PlatformError('GUARD_FAILED', '规范化后的消息为空,拒绝发送', 'afterRecovery')
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
      throw new PlatformError('GUARD_FAILED', '点开目标后当前会话仍不是发送目标,已取消', 'afterRecovery')
    }
  }
  // 输入框里已有的字不再挡路(2026-09-03 甲方裁决撤销 composer.empty):取到焦点、
  // Chrome 在前台之后用真实按键全选删除,回读为空再打。见下方 clearBossComposerByKeys。
  const composerBefore = await runInPage(BOSS_DOM, tabId, mainReadComposer, [COMPOSER_ID])
  if (!composerBefore.found) throw new PlatformError('ELEMENT_UNRESOLVED', '页面上找不到聊天输入框', 'afterRecovery')
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
  if (typedText === '') throw new PlatformError('GUARD_FAILED', '去掉换行之后没有内容可打', 'afterRecovery')
  ctx.checkpoint()
  let composed
  try {
    composed = await planType(typedText, seedFrom(ctx.cmdMsgId, 0), { sanitize: true })
  } catch (error) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `排版器自身异常:${describeError(error).slice(0, 300)}`, 'afterRecovery')
  }
  if (!composed.ok) {
    throw new PlatformError('GUARD_FAILED', `文案排不出合格键序(${composed.tries} 次):${composed.reasons.join(';').slice(0, 300)}`, 'afterRecovery')
  }
  // 实发正文即事实(2026-09-07):打不出的字元由清洗档摘掉后照打,目标文案从此是清洗后的。
  const targetText = composed.text
  if (composed.dropped.length > 0) {
    const note = `清洗摘掉 ${composed.dropped.length} 个打不出的字元(${summarizeDroppedKinds(composed.dropped)})`
    trace.push(note)
    reportHandLog('warn', 'typedTextSanitized', `chat.sendMessage ${note}`)
  }
  // 从这里起输入框会被写入。任何失败都留着草稿,所以都是 manualOnly:人来清。
  let played
  try {
    played = await playTypePlan(composed.plan)
  } catch (error) {
    if (isHandServiceDown(error)) throw new PlatformError('CTX_NOT_READY', '手服务不可用,打字未开始', 'afterRecovery', 'pageBroken')
    throw new PlatformError('ELEMENT_UNRESOLVED', `打字半途失败,输入框可能残留草稿:${describeError(error).slice(0, 300)}`, 'afterRecovery')
  }
  trace.push(`发了 ${played.keys} 次按键${played.words ? ` | ${played.words}` : ''}`)
  // TIP 活动核对(Windows 硬闸):上屏词数少于计划即键落到了别的输入法,不发。
  const shortfall = tipWordsShortfall(played)
  if (shortfall) throw new PlatformError('GUARD_FAILED', `${shortfall};已停在草稿,不发`, 'afterRecovery')
  const typed = await runInPage(BOSS_DOM, tabId, mainReadComposer, [COMPOSER_ID])
  // 上屏地板(实发正文即事实,2026-09-07):不再要求逐字相等,只挡灾难性错乱;差异在地板之内照发,
  // 只留数字不留正文。两边都过规范化再比:contenteditable 会把连续/尾部空格渲染成 nbsp(出口审查 O3)。
  const floor = typedTextFloor(targetText, typed.text)
  if (!floor.ok) {
    throw new PlatformError('GUARD_FAILED', `上屏文本与文案差得太远,已停在草稿:${describeTypedTextFloor(floor)}`, 'afterRecovery')
  }
  if (!floor.exact) {
    const note = `上屏与文案有差,地板之内照发:${describeTypedTextFloor(floor)}`
    trace.push(note)
    reportHandLog('warn', 'typedTextDrift', `chat.sendMessage ${note}`)
  }
  // 点击时编辑器必须仍逐字等于刚被接受的回读文本——防的是回读到点击之间有人改过。
  const acceptedText = typed.text
  // 最后一道闸之后立即唯一一次点击发送。
  const plan = await sendButtonClickPlan(tabId, args.conversationRef, acceptedText)
  ctx.checkpoint()
  await verifiedBossChatTab(fingerprint)
  if (Date.now() > ctx.irreversibleNotAfterMs) {
    throw new PlatformError('CTX_LOST_DURING_EXEC', '不可逆动作窗口已过,已停在草稿', 'afterRecovery')
  }
  await ctx.beforeSideEffect()
  const dispatchedAt = Date.now()
  const probe = await runOsProbe(BOSS_INJECT, tabId, ctx, plan)
  if (probe.outcome !== 'clicked') {
    throw new PlatformError(
      probe.outcome === 'handServiceUnavailable' ? 'CTX_NOT_READY' : 'ELEMENT_UNRESOLVED',
      `发送钮未点击,已停在草稿:${probe.detail ?? probe.outcome}`, 'afterRecovery')
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
        // 按窗口认行(实发正文即事实):基线之外最新一条服务端确认的我方文本行即本次,正文与 hash 以它为准。
        const hit = pickSentBossRow(after.rows, baselineMids, dispatchedAt)
        lastSeen = `基线外新行 ${after.rows.filter((row) => !baselineMids.has(row.mid)).length},命中 ${hit ? 1 : 0}`
        if (hit) {
          const sentHash = await sha256Hex(hit.hashInput)
          if (sentHash !== contentHash) {
            const note = `实发正文与计划不同:计划 ${Array.from(normalizedText).length} 字,实发 ${Array.from(hit.text).length} 字`
            trace.push(note)
            reportHandLog('warn', 'sentTextDiffers', `chat.sendMessage ${note}`)
          }
          await verifiedBossChatTab(fingerprint)
          ctx.progress('已从当前消息列表确认新已发文本', 100)
          console.info('[RecruitHelper] boss_send_message', trace.join(' | '))
          return {
            conversationRef: args.conversationRef,
            contentHash: sentHash,
            sentText: hit.text,
            sourceKey: await sha256Hex(`source-v1|${hit.row.mid}`),
            observedAt: Date.now(),
            ...(hit.row.time !== null && hit.row.time > 0 ? { tsApprox: hit.row.time } : {}),
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

/**
 * 快捷窗打开后先「看一眼」再打字的停顿区间(毫秒)。真人不会窗一弹出就敲键(2026-09-07 甲方:
 * 「几乎瞬间就开始打字,太假了」);有界随机,不是常数——常数会是机器签名。
 */
export const QUICK_CHAT_READ_PAUSE_MS = Object.freeze({ min: 3_000, max: 6_000 })

export function sampleQuickChatReadPause(random: () => number = Math.random): number {
  const span = QUICK_CHAT_READ_PAUSE_MS.max - QUICK_CHAT_READ_PAUSE_MS.min
  return QUICK_CHAT_READ_PAUSE_MS.min + Math.min(span, Math.floor(random() * (span + 1)))
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
//
// retryable 口径(2026-09-04 甲方裁决,与场景三同款):不可逆点击之前的一切失败都是零副作用,一律
// afterRecovery(本轮跳过、下轮重来);manualOnly 只留给已点过确定/同意而正证读不到的那条路。
// 脑侧对 effectful 失败结果只看 sideEffect=none 就按 §8.4 重铸,这里的提示改的是口径而不是结果;
// 只读的 readWechatExchangeOutcome 抛进巡检的错误则真会按 no/manualOnly 隔离会话,更不能写它。

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
    throw new PlatformError('GUARD_FAILED', '换微信邀请参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'afterRecovery')
  const contentHash = await sha256Hex('card\x1fwechatExchange')
  const tab = await verifiedBossChatTab(fingerprint)
  const tabId = tab.id!
  await ensureBossSendTarget(tab, ctx, fingerprint, args.conversationRef)

  // 世界状态核对一:会话级。已换成 / 未双向对话都是脑预期之外,干净失败不点。
  const wechat = await readBossWechatState(tab, parsed)
  if (wechat.weixin !== null) throw new PlatformError('GUARD_FAILED', '该会话微信已换成,不再发起邀请', 'afterRecovery')
  if (!wechat.bothTalked) throw new PlatformError('GUARD_FAILED', '双方尚未都说过话,平台不开放换微信(bothTalked=false)', 'afterRecovery')
  // 基线 + 世界状态核对二:对方是否已有待答请求(并发前置第一道)。
  const baseline = await readBossThreadRows(tab, ctx, parsed.uid, parsed.friendSource)
  await observeBossExpectedTail(baseline.rows, guards, 'chat.sendWechatInvite')
  if (pendingBossWechatRequests(baseline.rows).length > 0) {
    throw new PlatformError('GUARD_FAILED', '对方已有待答的换微信请求,本轮不发起邀请,由 acceptWechat 接手', 'afterRecovery')
  }
  const baselineMids = new Set(baseline.rows.map((row) => row.mid))
  // 工具栏按钮:文案必须恰为「换微信」且不带 disabled。「换微信 请求中」= 我方已有待答请求,「查看微信」= 已换成。
  const button = await runInPage(BOSS_DOM, tabId, domReadBossWechatButton, [TOOLBAR_BUTTON_SELECTOR])
  if (!button.found) throw new PlatformError('ELEMENT_UNRESOLVED', `工具栏换微信钮认不出(命中 ${button.count} 个)`, 'afterRecovery')
  if (button.text === '查看微信') throw new PlatformError('GUARD_FAILED', '工具栏已是「查看微信」,微信已换成', 'afterRecovery')
  if (button.disabled || button.text !== '换微信') {
    throw new PlatformError('GUARD_FAILED', `换微信钮不可用(读到「${button.text}」${button.disabled ? ',disabled' : ''})`, 'afterRecovery')
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
    // 己方微信号未配才会弹模态(平台事实 §六 第 2 步;生产机一律预配,§十四)。不填、不点;配置不完美一律降级
    // 不转人工(2026-09-04 甲方),人在平台配一次号即自愈。模态的关闭控件没有事实记录,按事实门不点,留着;
    // 它会挡住后续点击直到真人关掉——非生产路径,记录级。
    reportHandLog('warn', 'wechatInviteModalUnexpected', 'BOSS 点换微信后弹出填号模态(己方微信号未配置?),未填号、未确认,本轮不发')
    throw new PlatformError('ELEMENT_UNRESOLVED', '点换微信后弹出模态(己方微信号未配置?),未填号、未确认', 'afterRecovery')
  }
  if (!tip.visible || tip.confirmIndex < 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `点换微信后未见内联确认(可见=${tip.visible} 确定键=${tip.confirmIndex})`, 'afterRecovery')
  }
  // 最后一道闸:同一 evaluator 再读一次(并发前置第二道)。这时才出现的待答请求 → 点取消、干净拒绝。
  const again = await readBossThreadRows(tab, ctx, parsed.uid, parsed.friendSource)
  if (pendingBossWechatRequests(again.rows).length > 0) {
    await cancelBossExchangeTooltip(tabId, ctx, '确认前读到对方刚发来的换微信请求')
    throw new PlatformError('GUARD_FAILED', '确认前读到对方刚发来的换微信请求,已取消,由 acceptWechat 接手', 'afterRecovery')
  }
  const wechatAgain = await readBossWechatState(tab, parsed)
  if (wechatAgain.weixin !== null) {
    await cancelBossExchangeTooltip(tabId, ctx, '确认前微信已落值')
    throw new PlatformError('GUARD_FAILED', '确认前读到微信已换成,已取消', 'afterRecovery')
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
    throw new PlatformError('GUARD_FAILED', '接受微信参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'afterRecovery')
  const tab = await verifiedBossChatTab(fingerprint)
  const tabId = tab.id!
  await ensureBossSendTarget(tab, ctx, fingerprint, args.conversationRef)
  const rows = (await readBossThreadRows(tab, ctx, parsed.uid, parsed.friendSource)).rows
  await observeBossExpectedTail(rows, guards, 'chat.acceptWechat')
  const anchor = await findBossRowBySourceKey(rows, args.requestSourceKey)
  if (!anchor) throw new PlatformError('GUARD_FAILED', '请求锚在当前消息数组里找不到', 'afterRecovery')
  if (!isBossWechatRequestRow(anchor)) {
    throw new PlatformError('GUARD_FAILED', '请求锚不是对方的换微信请求卡(方向/类型/按钮码不符)', 'afterRecovery')
  }
  if (anchor.dialogOperated === true) throw new PlatformError('GUARD_FAILED', '该请求已答过,不再点击', 'afterRecovery')
  const wechat = await readBossWechatState(tab, parsed)
  if (wechat.weixin !== null) throw new PlatformError('GUARD_FAILED', '该会话微信已换成,不再点击', 'afterRecovery')
  const pending = pendingBossWechatRequests(rows)
  if (pending.length !== 1 || pending[0]!.mid !== anchor.mid) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `待答的换微信请求不唯一(${pending.length} 条),不猜`, 'afterRecovery')
  }
  const button = await runInPage(BOSS_DOM, tabId, domReadBossAcceptButton, [CARD_BUTTON_SELECTOR, CARD_ITEM_SELECTOR, WECHAT_CARD_TEXT])
  if (!button.found) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `卡内同意钮认不出(可用 ${button.count} 个)`, 'afterRecovery')
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
    throw new PlatformError('GUARD_FAILED', '微信交换结果读取参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'afterRecovery')
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
    throw new PlatformError('ELEMENT_UNRESOLVED', '微信交换结果结构不符合当前契约', 'afterRecovery')
  }
  await assertBossCurrent(tab, args.conversationRef, 'none')
  ctx.progress(data.confirmed ? '已确认微信交换结果' : `本轮未确认微信交换结果(${why})`, 100)
  return data
}

// ── 邀面卡线(场景三,2026-09-04 出口) ───────────────────────────────────────────
//
// 表单驱动的多步 OS 点击:工具栏「约面试」→ 模态 → 面试类型 radio(线上再选「微信视频」)→ 日期 → 宽松时间起止
// → 「发送」(出口 §2.2;DOM 事实见平台事实 §十五,主流程后置见 §十六)。判据按出口 §〇:卡已发出只认出站
// 21130009 行(不看 status,卡片行恒 0)**或**工具栏变「查看面试」,两者两时机都可见;模态关闭、成功弹窗、
// relationType 只作观测。BOSS 一律走宽松时间(30 分钟格,2026-09-04 甲方对齐);onsite 契约无 endsAt,表单里
// 结束取开始+1 小时——它只是填表的平台细节,进 detail 留痕,不进 data、不进账本。
//
// 失效方向(2026-09-04 甲方重申):「发送」按下之前的一切异常——参数不在格上、地址未配、模态/下拉/日历未就绪、
// 列表项滚不到、最后一道闸对不上——都点「取消」收回模态后干净失败 afterRecovery,交脑按协议 §8.4 下轮重铸,
// 不留人工票;manualOnly 只留给「已点过发送、正证读不到」这一条路(世界可能已被改动)。

const INTERVIEW_SENT_BIZ = 21130009
const INTERVIEW_BUTTON_INVITE = '约面试'
const INTERVIEW_BUTTON_VIEW = '查看面试'
const INTERVIEW_TIME_LOOSE = '宽松时间'
const INTERVIEW_MEETING_WECHAT = '微信视频'
/** 面试平台下拉的隐藏 input 存类型码:BOSS视频面试间=0、微信视频=8(平台事实 §十五)。与选中项文案二选一即认。 */
const INTERVIEW_MEETING_WECHAT_CODE = '8'
const INTERVIEW_SLOT_MINUTES = 30
const INTERVIEW_START_MIN = 8 * 60
const INTERVIEW_START_MAX = 20 * 60
const INTERVIEW_END_MAX = 21 * 60
const INTERVIEW_MIN_DURATION = 60
/** 时间列 li 高 44px、一屏 4 项半(平台事实 §十五):可见部分至少这么多才落光标。 */
const INTERVIEW_TIME_ITEM_MIN_VISIBLE_PX = 20
/** 滚动瞄准点:合格区近侧边界再进这么多。见 planBossTimeItemReach 的说明。 */
const INTERVIEW_TIME_ITEM_AIM_MARGIN_PX = 40
const INTERVIEW_SCROLL_ATTEMPTS = 5
/** 收回模态 / 关成功弹窗的等待封顶:一次点击后页面一帧就该变,给 5 秒是宽裕。 */
const INTERVIEW_DISMISS_WAIT_MS = 5_000

/** 邀面模态的 selector 包:整包传进页面函数(闭包变量到不了那边)。事实全在平台事实 §十五/§十六。 */
const INTERVIEW_SEL = Object.freeze({
  modal: '.interview-invite-dialog-ui',
  title: '.interview-invite-dialog-ui h3.tab',
  radio: '.interview-invite-dialog-ui label.radio.radio-item',
  radioChecked: 'radio-checked',
  address: '.interview-invite-dialog-ui .interview-address input',
  meeting: '.interview-invite-dialog-ui .meetingtype-select',
  meetingOpen: 'ui-select-visible',
  meetingItem: '.interview-invite-dialog-ui .meetingtype-select li.ui-select-item',
  meetingSelected: 'ui-select-item-selected',
  meetingHidden: '.interview-invite-dialog-ui .meetingtype-select input[type=hidden]',
  dateWrap: '.interview-invite-dialog-ui .datepicker-wrap',
  dateInput: '.interview-invite-dialog-ui .datepicker-wrap input',
  dateOpen: 'ui-datepicker-visible',
  dateMonth: '.datepicker-pannel.datepicker-day .day-month-btn',
  dateNext: '.datepicker-pannel.datepicker-day .next',
  dateCell: '.datepicker-pannel.datepicker-day span.cell.day',
  timeContainer: '.interview-invite-dialog-ui .time-select-container',
  timeInput: '.interview-invite-dialog-ui input.time-select',
  timeOpen: 'dropdown-menu-open',
  timeTab: '.time-title span',
  timeTabSelected: 'selected',
  timeList: 'ul.time-select-ul',
  timeItem: 'ul.time-select-ul li',
  timeItemSelected: 'selected',
  cancel: '.interview-invite-dialog-ui .interview-btns button.btn-outline-v2',
  send: '.interview-invite-dialog-ui .interview-btns button.btn-sure-v2',
  popup: '.boss-popup__wrapper, .dialog-wrap.active',
  popupText: '面试邀请已发出',
  popupClose: '.boss-popup__close',
})
type InterviewSelectors = typeof INTERVIEW_SEL

// ── 参数换算与格校验(纯函数,单测钉) ──────────────────────────────────────────

export interface BossInterviewFormPlan {
  method: InterviewMethod
  /** radio 文案:线下面试 / 线上面试。 */
  radioText: string
  /** 线上才有:面试平台下拉要选的项;线下为 null。 */
  meetingText: string | null
  /** 线上才有:该项对应的隐藏 input 类型码;线下为 null。 */
  meetingCode: string | null
  year: number
  month: number
  day: number
  /** 日期框回填值 YYYY-MM-DD。 */
  date: string
  /** 目标日就是今天:日历里那格文本是「今」不是数字。 */
  isToday: boolean
  startText: string
  endText: string
  /** 选定开始后结束列重过滤,首项应为开始+1 小时。 */
  firstEndText: string
  /** 时间框回填值 HH:mm-HH:mm。 */
  timeValue: string
  /** onsite:契约无 endsAt,结束由开始+1 小时合成,只填表、只留痕。 */
  endSynthesized: boolean
}

function pad2(value: number): string {
  return value < 10 ? `0${value}` : String(value)
}

function hhmm(minutes: number): string {
  return `${pad2(Math.floor(minutes / 60))}:${pad2(minutes % 60)}`
}

function localDateParts(at: Date): { year: number; month: number; day: number } {
  return { year: at.getFullYear(), month: at.getMonth() + 1, day: at.getDate() }
}

/**
 * 契约 interview → 表单要填的值。按本机时区拆;任一不合格即 invalid,**不取整、不改时间**(少做方向),
 * 由调用方零点击干净失败交脑下轮重铸。脑侧 canonical 时段是整点起 1 小时格,正常全部合格。
 */
export function planBossInterviewForm(
  interview: InterviewDetails, now: number,
): { status: 'ok'; plan: BossInterviewFormPlan } | { status: 'invalid'; detail: string } {
  const invalid = (detail: string): { status: 'invalid'; detail: string } => ({ status: 'invalid', detail })
  const method = interview.method
  if (method !== 'onsite' && method !== 'wechatVideo') return invalid(`面试方式 ${String(method)} 不在本平台开放范围`)
  const startsAt = interview.startsAt
  if (typeof startsAt !== 'number' || !Number.isSafeInteger(startsAt) || startsAt <= 0) return invalid('开始时间不是合法的毫秒时间戳')
  if (startsAt <= now) return invalid(`开始时间 ${new Date(startsAt).toISOString()} 已过`)
  const start = new Date(startsAt)
  const startMin = start.getHours() * 60 + start.getMinutes()
  if (start.getSeconds() !== 0 || start.getMilliseconds() !== 0 || start.getMinutes() % INTERVIEW_SLOT_MINUTES !== 0) {
    return invalid(`开始时间 ${hhmm(startMin)}:${pad2(start.getSeconds())} 不在 30 分钟格上`)
  }
  if (startMin < INTERVIEW_START_MIN || startMin > INTERVIEW_START_MAX) {
    return invalid(`开始时间 ${hhmm(startMin)} 不在平台 08:00–20:00 范围`)
  }
  let endMin: number
  let endSynthesized = false
  if (method === 'onsite') {
    if (typeof interview.endsAt === 'number') return invalid('线下面试的契约不携带结束时间')
    endMin = startMin + INTERVIEW_MIN_DURATION
    endSynthesized = true
  } else {
    const endsAt = interview.endsAt
    if (typeof endsAt !== 'number' || !Number.isSafeInteger(endsAt)) return invalid('线上面试缺结束时间')
    const end = new Date(endsAt)
    if (end.getFullYear() !== start.getFullYear() || end.getMonth() !== start.getMonth() || end.getDate() !== start.getDate()) {
      return invalid('结束时间与开始时间不在同一天')
    }
    endMin = end.getHours() * 60 + end.getMinutes()
    if (end.getSeconds() !== 0 || end.getMilliseconds() !== 0 || end.getMinutes() % INTERVIEW_SLOT_MINUTES !== 0) {
      return invalid(`结束时间 ${hhmm(endMin)}:${pad2(end.getSeconds())} 不在 30 分钟格上`)
    }
    if (endMin < startMin + INTERVIEW_MIN_DURATION) return invalid(`结束 ${hhmm(endMin)} 早于开始 ${hhmm(startMin)}+1 小时`)
  }
  if (endMin > INTERVIEW_END_MAX) return invalid(`结束时间 ${hhmm(endMin)} 超出平台 21:00`)
  const today = localDateParts(new Date(now))
  const target = localDateParts(start)
  const monthOffset = (target.year - today.year) * 12 + (target.month - today.month)
  if (monthOffset < 0) return invalid('面试日期早于本月')
  if (monthOffset > 1) return invalid(`面试日期在 ${monthOffset} 个月后,日历只翻一页`)
  return {
    status: 'ok',
    plan: {
      method,
      radioText: method === 'onsite' ? '线下面试' : '线上面试',
      meetingText: method === 'onsite' ? null : INTERVIEW_MEETING_WECHAT,
      meetingCode: method === 'onsite' ? null : INTERVIEW_MEETING_WECHAT_CODE,
      year: target.year, month: target.month, day: target.day,
      date: `${target.year}-${pad2(target.month)}-${pad2(target.day)}`,
      isToday: target.year === today.year && target.month === today.month && target.day === today.day,
      startText: hhmm(startMin),
      endText: hhmm(endMin),
      firstEndText: hhmm(startMin + INTERVIEW_MIN_DURATION),
      timeValue: `${hhmm(startMin)}-${hhmm(endMin)}`,
      endSynthesized,
    },
  }
}

const BOSS_CALENDAR_MONTHS = ['一', '二', '三', '四', '五', '六', '七', '八', '九', '十', '十一', '十二']

/** 日历头「2026年 九月」→ 年月;认不出返回 null,不猜。 */
export function parseBossCalendarMonth(text: string): { year: number; month: number } | null {
  const match = /^(\d{4})年(十一|十二|[一二三四五六七八九十])月$/u.exec(text.replace(/\s+/gu, ''))
  if (!match) return null
  const month = BOSS_CALENDAR_MONTHS.indexOf(match[2]!) + 1
  return month > 0 ? { year: Number(match[1]), month } : null
}

export interface BossCalendarCell {
  /** 在 selector 全序列里的下标,命中测试用。 */
  index: number
  text: string
  disabled: boolean
  today: boolean
  blank: boolean
  rect: BossRect
  /** 与视口相交的可见部分。 */
  clip: BossRect
}

/** 日历里那一格:非占位、非过去、文本=日(今天那格文本是「今」);恰一格才认。 */
export function pickBossCalendarCell(
  cells: readonly BossCalendarCell[], day: number, targetIsToday: boolean,
): { cell: BossCalendarCell | null; count: number } {
  const hits = cells.filter((cell) => !cell.blank && !cell.disabled &&
    (targetIsToday ? cell.today : (!cell.today && cell.text.trim() === String(day))))
  return { cell: hits.length === 1 ? hits[0]! : null, count: hits.length }
}

export interface BossTimeItem {
  index: number
  text: string
  selected: boolean
  rect: BossRect
}

export interface BossTimeList {
  index: number
  /** 列表容器矩形(与视口相交后的可见部分)。 */
  rect: BossRect
  scrollTop: number
  scrollHeight: number
  clientHeight: number
  items: BossTimeItem[]
}

/**
 * 时间项在不在列表可见区里;不在就算要朝哪滚多少。
 *
 * **瞄合格区的近侧边界再进 40px,不瞄正中。** "露出 ≥20px"对应的 scrollTop 合格区宽 200px(项 44 + 列表 196
 * − 2×20),滚轮一格 100~120px:瞄近侧边界时任何一格过头都还落在合格区内;瞄正中时两侧余量只有 76px,Mac
 * 120px/格的过头修正量(100~140px)恰落在 runOsScroll 每次调用起手按 100px/格估算的"两格 240px"档,会来回
 * 振荡到封顶(2026-09-04 出口审查记录级第 3 条)。过头进不了合格区时修正量 <100px 只走一格,至多两三次收敛。
 */
export function planBossTimeItemReach(
  list: Pick<BossTimeList, 'rect' | 'scrollTop' | 'scrollHeight' | 'clientHeight'>,
  item: Pick<BossTimeItem, 'rect'>,
  minVisiblePx: number,
  aimMarginPx = INTERVIEW_TIME_ITEM_AIM_MARGIN_PX,
): { status: 'visible'; rect: BossRect } | { status: 'scroll'; direction: 'up' | 'down'; distancePx: number } | { status: 'unreachable'; detail: string } {
  const left = Math.max(item.rect.x, list.rect.x)
  const top = Math.max(item.rect.y, list.rect.y)
  const right = Math.min(item.rect.x + item.rect.w, list.rect.x + list.rect.w)
  const bottom = Math.min(item.rect.y + item.rect.h, list.rect.y + list.rect.h)
  if (right - left >= minVisiblePx && bottom - top >= minVisiblePx) {
    return { status: 'visible', rect: { x: left, y: top, w: right - left, h: bottom - top } }
  }
  const maxTop = Math.max(0, list.scrollHeight - list.clientHeight)
  const itemTop = item.rect.y - list.rect.y + list.scrollTop
  // scrollTop 的合格区:[itemTop + m − clientHeight, itemTop + h − m]。
  const lo = itemTop + minVisiblePx - list.clientHeight
  const hi = itemTop + item.rect.h - minVisiblePx
  if (list.scrollTop >= lo && list.scrollTop <= hi) {
    return { status: 'unreachable', detail: `项在纵向合格区内却不可见(横向不相交?scrollTop ${Math.round(list.scrollTop)}),滚动解决不了` }
  }
  const clamp = (value: number): number => Math.min(maxTop, Math.max(0, value))
  const wanted = list.scrollTop < lo ? clamp(Math.min(hi, lo + aimMarginPx)) : clamp(Math.max(lo, hi - aimMarginPx))
  const delta = wanted - list.scrollTop
  if (Math.abs(delta) < 1) {
    return { status: 'unreachable', detail: `项不在列表可见区内,而 scrollTop 已在 ${Math.round(list.scrollTop)}/${Math.round(maxTop)},无处可滚` }
  }
  return { status: 'scroll', direction: delta > 0 ? 'down' : 'up', distancePx: Math.round(Math.abs(delta)) }
}

/** 发送前复核(脑侧同一份期望):模态、类型、平台、日期、时间、发送键六项逐字对,缺一不点。返回不符项。 */
export function bossInterviewFormMismatch(modal: DomInterviewModal, plan: BossInterviewFormPlan): string[] {
  const problems: string[] = []
  if (modal.modal !== 1) problems.push(`模态数 ${modal.modal}`)
  const checked = modal.radios.filter((radio) => radio.checked).map((radio) => radio.text)
  if (!(checked.length === 1 && checked[0] === plan.radioText)) problems.push(`面试类型「${checked.join('/')}」≠「${plan.radioText}」`)
  if (plan.meetingText !== null && !bossMeetingChosen(modal, plan.meetingText, plan.meetingCode)) {
    problems.push(`面试平台「${modal.meeting.selected}」/码「${modal.meeting.code}」不是「${plan.meetingText}」`)
  }
  if (modal.date.value !== plan.date) problems.push(`日期「${modal.date.value}」≠「${plan.date}」`)
  if (modal.time.value !== plan.timeValue) problems.push(`时间「${modal.time.value}」≠「${plan.timeValue}」`)
  if (!modal.send.found) problems.push('发送键不在')
  else if (modal.send.disabled) problems.push('发送键 disabled')
  return problems
}

/** 面试平台已选中某项:选中项文案含目标,**或**隐藏 input 的类型码等于目标码(下拉关着时选中类未必还在 DOM 里)。 */
function bossMeetingChosen(modal: Pick<DomInterviewModal, 'meeting'>, text: string, code: string | null): boolean {
  return modal.meeting.selected.includes(text) || (code !== null && modal.meeting.code === code)
}

function isBossInterviewSentRow(row: BossRawMessage): boolean {
  return row.direction === 'out' && row.bizType === INTERVIEW_SENT_BIZ
}

function latestBossRow(rows: readonly BossRawMessage[]): BossRawMessage {
  return rows.reduce((best, next) => (Number(next.mid) > Number(best.mid) ? next : best))
}

// ── isolated world:邀面表单的页面函数 ─────────────────────────────────────────

/** 工具栏「约面试 / 查看面试」钮:文案含 tooltip 副本(§十五),按"含"分类而不逐字;恰一个才认。 */
function domReadBossInterviewButton(selector: string, inviteText: string, viewText: string): {
  found: boolean; count: number; index: number; kind: 'invite' | 'view' | ''; text: string; disabled: boolean; rect: BossRect
} {
  const zero = { x: 0, y: 0, w: 0, h: 0 }
  const hits: Array<{ index: number; el: Element; kind: 'invite' | 'view'; text: string }> = []
  Array.from(document.querySelectorAll(selector)).forEach((el, index) => {
    const text = (el.textContent ?? '').replace(/\s+/gu, ' ').trim()
    const kind: 'invite' | 'view' | '' = text.includes(viewText) ? 'view' : text.includes(inviteText) ? 'invite' : ''
    if (kind) hits.push({ index, el, kind, text })
  })
  if (hits.length !== 1) return { found: false, count: hits.length, index: -1, kind: '', text: '', disabled: false, rect: zero }
  const { index, el, kind, text } = hits[0]!
  const r = el.getBoundingClientRect()
  return {
    found: true, count: 1, index, kind, text: text.slice(0, 24), disabled: el.classList.contains('disabled'),
    rect: { x: r.x, y: r.y, w: r.width, h: r.height },
  }
}

/** 命中测试的"含"版本:落点是 selector[index] 或其后代,且此刻文本含 mustContain、不含 mustNotContain。 */
function domHitTestContains(
  selector: string, index: number, mustContain: string, mustNotContain: string, x: number, y: number,
): { onTarget: boolean; found: string } {
  const target = Array.from(document.querySelectorAll(selector))[index]
  const at = document.elementFromPoint(x, y)
  if (!target) return { onTarget: false, found: '靶子已经不在原来的位置上' }
  if (!at) return { onTarget: false, found: '落点上什么都没有' }
  if (!(at === target || target.contains(at))) {
    return { onTarget: false, found: `落点上是别的元素 ${at.tagName.toLowerCase()}「${(at.textContent ?? '').trim().slice(0, 8)}」` }
  }
  const now = (target.textContent ?? '').replace(/\s+/gu, ' ').trim()
  if (!now.includes(mustContain) || (mustNotContain !== '' && now.includes(mustNotContain))) {
    return { onTarget: false, found: `靶子文本已变:「${now.slice(0, 16)}」` }
  }
  return { onTarget: true, found: `靶子(${at.tagName.toLowerCase()})` }
}

interface DomInterviewModal {
  /** 可见的邀面模态数;恰 1 才算开着。 */
  modal: number
  title: string
  titleIndex: number
  titleRect: BossRect
  radios: Array<{ index: number; text: string; checked: boolean; rect: BossRect }>
  /** 线下的「面试地址」只读框的值;线上没有这行则为 null。 */
  address: string | null
  meeting: {
    present: boolean
    index: number
    open: boolean
    /** 当前选中项文案(带选中类的 li)。 */
    selected: string
    /** 隐藏 input 里的类型码(微信视频=8)。 */
    code: string
    rect: BossRect
    items: Array<{ index: number; text: string; selected: boolean; rect: BossRect }>
  }
  date: {
    index: number
    value: string
    open: boolean
    rect: BossRect
    month: string
    nextIndex: number
    nextRect: BossRect | null
    cells: BossCalendarCell[]
  }
  time: {
    index: number
    value: string
    open: boolean
    rect: BossRect
    tabs: Array<{ index: number; text: string; selected: boolean; rect: BossRect }>
    lists: BossTimeList[]
  }
  cancel: { found: boolean; index: number; rect: BossRect }
  send: { found: boolean; index: number; disabled: boolean; rect: BossRect }
  /** 发送后的全屏成功对话框「面试邀请已发出」与它的关闭键。 */
  popup: { found: boolean; closeIndex: number; closeRect: BossRect }
  viewport: { w: number; h: number }
}

/** 邀面模态一次读全:每个可点的东西都带 selector 全序列下标(命中测试用)与矩形。只读。 */
function domReadBossInterviewModal(sel: InterviewSelectors): DomInterviewModal {
  const zero: BossRect = { x: 0, y: 0, w: 0, h: 0 }
  const rectOf = (el: Element | undefined | null): BossRect => {
    if (!el) return zero
    const r = el.getBoundingClientRect()
    return { x: r.x, y: r.y, w: r.width, h: r.height }
  }
  const clipOf = (r: BossRect): BossRect => {
    const left = Math.max(0, r.x)
    const top = Math.max(0, r.y)
    const right = Math.min(window.innerWidth, r.x + r.w)
    const bottom = Math.min(window.innerHeight, r.y + r.h)
    return { x: left, y: top, w: Math.max(0, right - left), h: Math.max(0, bottom - top) }
  }
  const visible = (el: Element): boolean => {
    const r = el.getBoundingClientRect()
    return r.width > 0 && r.height > 0
  }
  const text = (el: Element | undefined | null): string => (el ? (el.textContent ?? '') : '').replace(/\s+/gu, ' ').trim()
  const valueOf = (el: Element | undefined | null): string => {
    const raw = (el as { value?: unknown } | null | undefined)?.value
    return typeof raw === 'string' ? raw.trim() : ''
  }
  const has = (el: Element, cls: string): boolean => el.classList.contains(cls)
  const all = (selector: string): Element[] => Array.from(document.querySelectorAll(selector))
  const firstVisible = (selector: string): { el: Element | undefined; index: number } => {
    const list = all(selector)
    const index = list.findIndex(visible)
    return { el: index >= 0 ? list[index] : undefined, index }
  }

  const modal = all(sel.modal).filter(visible).length
  const titleHit = firstVisible(sel.title)
  const title = text(titleHit.el)
  const radios = all(sel.radio).map((el, index) => ({ index, text: text(el), checked: has(el, sel.radioChecked), rect: rectOf(el) }))
    .filter((radio) => radio.rect.w > 0 && radio.rect.h > 0)
  const addressEl = firstVisible(sel.address).el
  const address = addressEl ? valueOf(addressEl) : null

  const meetingHit = firstVisible(sel.meeting)
  const meetingItems = all(sel.meetingItem).map((el, index) => ({ index, text: text(el), selected: has(el, sel.meetingSelected), rect: rectOf(el) }))
  const meeting = {
    present: !!meetingHit.el, index: meetingHit.index,
    open: !!meetingHit.el && has(meetingHit.el, sel.meetingOpen),
    selected: meetingItems.filter((item) => item.selected).map((item) => item.text).join('/'),
    code: valueOf(all(sel.meetingHidden)[0]),
    rect: rectOf(meetingHit.el), items: meetingItems,
  }

  const dateWrap = firstVisible(sel.dateWrap).el
  const dateInput = firstVisible(sel.dateInput)
  const nextHit = firstVisible(sel.dateNext)
  const cells: BossCalendarCell[] = []
  all(sel.dateCell).forEach((el, index) => {
    if (!visible(el)) return
    const rect = rectOf(el)
    cells.push({ index, text: text(el), disabled: has(el, 'disabled'), today: has(el, 'today'), blank: has(el, 'blank'), rect, clip: clipOf(rect) })
  })
  const date = {
    index: dateInput.index, value: valueOf(dateInput.el),
    open: !!dateWrap && has(dateWrap, sel.dateOpen),
    rect: rectOf(dateInput.el), month: text(firstVisible(sel.dateMonth).el),
    nextIndex: nextHit.index, nextRect: nextHit.el ? rectOf(nextHit.el) : null, cells,
  }

  const timeContainer = firstVisible(sel.timeContainer).el
  const timeInput = firstVisible(sel.timeInput)
  const tabs = all(sel.timeTab).map((el, index) => ({ index, text: text(el), selected: has(el, sel.timeTabSelected), rect: rectOf(el) }))
    .filter((tab) => tab.rect.w > 0 && tab.rect.h > 0)
  const allItems = all(sel.timeItem)
  const lists: BossTimeList[] = []
  all(sel.timeList).forEach((el, index) => {
    if (!visible(el)) return
    const items: BossTimeItem[] = []
    allItems.forEach((item, itemIndex) => {
      if (!el.contains(item)) return
      items.push({ index: itemIndex, text: text(item), selected: has(item, sel.timeItemSelected), rect: rectOf(item) })
    })
    lists.push({ index, rect: clipOf(rectOf(el)), scrollTop: el.scrollTop, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight, items })
  })
  const time = {
    index: timeInput.index, value: valueOf(timeInput.el),
    open: !!timeContainer && has(timeContainer, sel.timeOpen),
    rect: rectOf(timeInput.el), tabs, lists,
  }

  const cancelHit = firstVisible(sel.cancel)
  const sendHit = firstVisible(sel.send)
  const sendEl = sendHit.el as (Element & { disabled?: unknown }) | undefined
  const cancel = { found: !!cancelHit.el, index: cancelHit.index, rect: rectOf(cancelHit.el) }
  const send = { found: !!sendEl, index: sendHit.index, disabled: !!sendEl && (sendEl.disabled === true || has(sendEl, 'disabled')), rect: rectOf(sendEl) }

  const closers = all(sel.popupClose)
  let popup = { found: false, closeIndex: -1, closeRect: zero }
  const popupEl = all(sel.popup).find((el) => visible(el) && (el.textContent ?? '').includes(sel.popupText))
  if (popupEl) {
    const closeIndex = closers.findIndex((el) => popupEl.contains(el) && visible(el))
    popup = { found: true, closeIndex, closeRect: rectOf(closeIndex >= 0 ? closers[closeIndex] : undefined) }
  }
  return {
    modal, title, titleIndex: titleHit.index, titleRect: rectOf(titleHit.el),
    radios, address, meeting, date, time, cancel, send, popup,
    viewport: { w: window.innerWidth, h: window.innerHeight },
  }
}

/**
 * 发送前最后一道闸(与 domSendGate / domAcceptGate 同款,点击前的最后一次读):落点是「发送」键、模态仍在、
 * 面试类型/平台/日期/时间逐字等于期望、选中行仍是目标会话——缺一不点。
 */
function domBossInterviewSendGate(
  sel: InterviewSelectors,
  expect: { radioText: string; meetingText: string | null; meetingCode: string | null; date: string; timeValue: string },
  rowSelector: string, conversationRef: string, selectedClass: string,
  x: number, y: number,
): { onTarget: boolean; found: string } {
  const visible = (el: Element): boolean => {
    const r = el.getBoundingClientRect()
    return r.width > 0 && r.height > 0
  }
  const text = (el: Element): string => (el.textContent ?? '').replace(/\s+/gu, ' ').trim()
  const valueOf = (el: Element | undefined): string => {
    const raw = (el as { value?: unknown } | undefined)?.value
    return typeof raw === 'string' ? raw.trim() : ''
  }
  const all = (selector: string): Element[] => Array.from(document.querySelectorAll(selector))
  const problems: string[] = []
  const modals = all(sel.modal).filter(visible)
  if (modals.length !== 1) problems.push(`模态数 ${modals.length}`)
  const send = all(sel.send).find(visible) as (Element & { disabled?: unknown }) | undefined
  const at = document.elementFromPoint(x, y)
  if (!send) problems.push('发送键不在')
  else {
    if (!at) problems.push('落点上什么都没有')
    else if (!(at === send || send.contains(at))) problems.push(`落点上是别的元素 ${at.tagName.toLowerCase()}「${text(at).slice(0, 8)}」`)
    if (send.disabled === true || send.classList.contains('disabled')) problems.push('发送键 disabled')
  }
  const checked = all(sel.radio).filter((el) => visible(el) && el.classList.contains(sel.radioChecked)).map(text)
  if (!(checked.length === 1 && checked[0] === expect.radioText)) problems.push(`面试类型「${checked.join('/')}」≠「${expect.radioText}」`)
  if (expect.meetingText !== null) {
    const selected = all(sel.meetingItem).filter((el) => el.classList.contains(sel.meetingSelected)).map(text)
    const code = valueOf(all(sel.meetingHidden)[0])
    const byText = selected.length === 1 && selected[0]!.includes(expect.meetingText)
    const byCode = expect.meetingCode !== null && code === expect.meetingCode
    if (!byText && !byCode) problems.push(`面试平台「${selected.join('/')}」/码「${code}」不是「${expect.meetingText}」`)
  }
  const dateValue = valueOf(all(sel.dateInput).find(visible))
  if (dateValue !== expect.date) problems.push(`日期「${dateValue}」≠「${expect.date}」`)
  const timeValue = valueOf(all(sel.timeInput).find(visible))
  if (timeValue !== expect.timeValue) problems.push(`时间「${timeValue}」≠「${expect.timeValue}」`)
  const selectedRows = all(rowSelector).filter((el) => el.classList.contains(selectedClass))
  if (!(selectedRows.length === 1 && selectedRows[0]!.getAttribute('data-id') === conversationRef)) {
    problems.push(`选中行不是目标会话(选中 ${selectedRows.length} 行)`)
  }
  return problems.length === 0 ? { onTarget: true, found: '邀面发送键' } : { onTarget: false, found: problems.join(';') }
}

// ── 编排 ────────────────────────────────────────────────────────────────────────

function isStopExecution(error: unknown): boolean {
  return !!error && typeof error === 'object' && (error as { name?: unknown }).name === 'StopExecution'
}

async function readInterviewModal(tabId: number): Promise<DomInterviewModal> {
  return runInPage(BOSS_DOM, tabId, domReadBossInterviewModal, [INTERVIEW_SEL])
}

async function readInterviewButton(tabId: number) {
  return runInPage(BOSS_DOM, tabId, domReadBossInterviewButton, [TOOLBAR_BUTTON_SELECTOR, INTERVIEW_BUTTON_INVITE, INTERVIEW_BUTTON_VIEW])
}

function describeInterviewModal(m: DomInterviewModal): string {
  return `模态=${m.modal} 类型=${m.radios.filter((r) => r.checked).map((r) => r.text).join('/') || '无'}` +
    ` 平台=${m.meeting.selected || '无'}/码${m.meeting.code || '空'} 日期=${m.date.value || '空'}${m.date.open ? '(开)' : ''}` +
    ` 时间=${m.time.value || '空'}${m.time.open ? '(开)' : ''} 弹窗=${m.popup.found ? '有' : '无'}`
}

/** 模态里一个靶子的点击计划:命中测试按 selector 全序列下标 + 文本。 */
function interviewClickPlan(
  tabId: number, label: string, selector: string, index: number, expectText: string | null, rect: BossRect,
): ClickPlan {
  return {
    label, rect,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestExpected, [selector, index, expectText, x, y]),
    observe: async (): Promise<ClickObservation> =>
      ({ trusted: null, onTarget: null, eventDriftPx: null, after: describeInterviewModal(await readInterviewModal(tabId)) }),
  }
}

/**
 * 收回模态:先点标题把开着的下拉/日历收掉(它们会盖住底部按钮),再点「取消」。零副作用;收不回只记日志——
 * 下一次本原语开工时会先把残留模态取消掉再走。
 */
async function cancelBossInterviewModal(tabId: number, ctx: PrimitiveContext, why: string): Promise<void> {
  try {
    let modal = await readInterviewModal(tabId)
    if (modal.modal === 0) return
    if ((modal.time.open || modal.date.open || modal.meeting.open) && modal.titleIndex >= 0) {
      await paceBeforeClick()
      await osClickOnce(tabId, ctx,
        interviewClickPlan(tabId, '邀面模态标题(收下拉)', INTERVIEW_SEL.title, modal.titleIndex, null, modal.titleRect), '点模态标题收下拉')
      modal = (await pollUntil(ctx, () => readInterviewModal(tabId), (m) => !(m.time.open || m.date.open || m.meeting.open), INTERVIEW_DISMISS_WAIT_MS)).value
    }
    if (!modal.cancel.found) {
      reportHandLog('warn', 'interviewInviteCancelFailed', `BOSS 邀面模态在但取消键认不出(${why})`)
      return
    }
    await paceBeforeClick()
    await osClickOnce(tabId, ctx, interviewClickPlan(tabId, '邀面取消键', INTERVIEW_SEL.cancel, modal.cancel.index, '取消', modal.cancel.rect), '点邀面取消键')
    const gone = await pollUntil(ctx, () => readInterviewModal(tabId), (m) => m.modal === 0, INTERVIEW_DISMISS_WAIT_MS)
    reportHandLog('warn', gone.satisfied ? 'interviewInviteCancelled' : 'interviewInviteCancelStuck',
      `BOSS 邀面模态${gone.satisfied ? '已取消' : '点了取消仍在'}:${why}`)
  } catch (error) {
    if (isStopExecution(error)) throw error
    reportHandLog('warn', 'interviewInviteCancelFailed', `BOSS 邀面模态未能取消(${why}):${describeError(error).slice(0, 200)}`)
  }
}


/**
 * 发送后的全屏成功对话框:正证读完后点它的关闭键;关不掉只记日志,不影响正证。它不在 BOSS_DISMISS_WHITELIST 里
 * (那份名单只收营销位与引导,容器选择器认不出它),关不掉页面就锁着,此后每次 OS 点击都会被命中测试干净拒到
 * 真人关掉为止——真机 §十六 一点就掉,先不为它加机制。
 */
async function dismissBossInterviewSuccessPopup(tabId: number, ctx: PrimitiveContext): Promise<void> {
  try {
    const modal = await readInterviewModal(tabId)
    if (!modal.popup.found) return
    if (modal.popup.closeIndex < 0) {
      reportHandLog('warn', 'interviewSuccessPopupDismissFailed', 'BOSS 邀面成功弹窗在但关闭键认不出')
      return
    }
    await paceBeforeClick()
    await osClickOnce(tabId, ctx, {
      label: '邀面成功弹窗关闭键',
      rect: modal.popup.closeRect,
      hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestExpected, [INTERVIEW_SEL.popupClose, modal.popup.closeIndex, null, x, y]),
      observe: async (): Promise<ClickObservation> =>
        ({ trusted: null, onTarget: null, eventDriftPx: null, after: `弹窗=${(await readInterviewModal(tabId)).popup.found ? '仍在' : '已关'}` }),
    }, '关邀面成功弹窗')
    const gone = await pollUntil(ctx, () => readInterviewModal(tabId), (m) => !m.popup.found, INTERVIEW_DISMISS_WAIT_MS)
    if (!gone.satisfied) reportHandLog('warn', 'interviewSuccessPopupStuck', 'BOSS 邀面成功弹窗点了关闭仍在,页面锁着,要真人关掉')
  } catch (error) {
    if (isStopExecution(error)) throw error
    reportHandLog('warn', 'interviewSuccessPopupDismissFailed', `BOSS 邀面成功弹窗未能关闭:${describeError(error).slice(0, 200)}`)
  }
}

// ── chat.sendInviteCard ──────────────────────────────────────────────────────

async function sendBossInviteCard(
  args: ChatSendInviteCardArgs, guards: ChatSendMessageGuards, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<ChatSendInviteCardData> {
  // 「发送」之前的一切失败都是零副作用的干净失败,一律 afterRecovery 交脑下轮重铸(2026-09-04 甲方)。
  if (validatePrimitiveArgs(PrimitiveName.ChatSendInviteCard, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '邀面卡参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  const parsed = parseBossConversationRef(args.conversationRef)
  if (!parsed) throw new PlatformError('GUARD_FAILED', '会话引用不是本平台形态', 'afterRecovery')
  const planned = planBossInterviewForm(args.interview, Date.now())
  if (planned.status !== 'ok') {
    // 零点击干净失败:不取整、不改时间;detail 留实际值。
    throw new PlatformError('GUARD_FAILED', `邀面参数不合本平台表单:${planned.detail}`, 'afterRecovery')
  }
  const plan = planned.plan
  const contentHash = await sha256Hex('card\x1finterviewInvite')
  const tab = await verifiedBossChatTab(fingerprint)
  const tabId = tab.id!
  await ensureBossSendTarget(tab, ctx, fingerprint, args.conversationRef)

  // 世界状态核对:会话级 bothTalked、消息数组无出站邀面行、工具栏恰为「约面试」。
  const wechat = await readBossWechatState(tab, parsed)
  if (!wechat.bothTalked) {
    throw new PlatformError('GUARD_FAILED', '双方尚未都说过话,平台不开放约面试(bothTalked=false)', 'afterRecovery')
  }
  const baseline = await readBossThreadRows(tab, ctx, parsed.uid, parsed.friendSource)
  await observeBossExpectedTail(baseline.rows, guards, 'chat.sendInviteCard')
  if (baseline.rows.some(isBossInterviewSentRow)) {
    throw new PlatformError('GUARD_FAILED', '消息数组里已有我方发出的邀面卡,不再发', 'afterRecovery')
  }
  const baselineMids = new Set(baseline.rows.map((row) => row.mid))
  // 上一趟的残留模态(取消没收回):先收掉,收不掉就本轮不动。
  const stale = await readInterviewModal(tabId)
  if (stale.modal > 0) {
    await cancelBossInterviewModal(tabId, ctx, '开工时发现残留的邀面模态')
    if ((await readInterviewModal(tabId)).modal > 0) {
      throw new PlatformError('ELEMENT_UNRESOLVED', '页面上有残留的邀面模态且收不回,本轮不动', 'afterRecovery')
    }
  }
  const button = await readInterviewButton(tabId)
  if (!button.found) throw new PlatformError('ELEMENT_UNRESOLVED', `工具栏约面试钮认不出(命中 ${button.count} 个)`, 'afterRecovery')
  if (button.kind === 'view') {
    throw new PlatformError('GUARD_FAILED', '工具栏已是「查看面试」,该会话有过面试,不再发', 'afterRecovery')
  }
  if (button.disabled) throw new PlatformError('GUARD_FAILED', '约面试钮 disabled,本轮不发', 'afterRecovery')

  const trace: string[] = [
    `计划 ${plan.radioText}${plan.meetingText ? '/' + plan.meetingText : ''} ${plan.date} ${plan.timeValue}${plan.endSynthesized ? '(结束=开始+1h,只填表)' : ''}`,
  ]
  const sendExpect = { radioText: plan.radioText, meetingText: plan.meetingText, meetingCode: plan.meetingCode, date: plan.date, timeValue: plan.timeValue }

  // 点一下、等到后置;不就绪即抛(外层 catch 负责收回模态)。
  const clickAndSettle = async (
    clickPlan: ClickPlan, what: string, done: (m: DomInterviewModal) => boolean,
  ): Promise<DomInterviewModal> => {
    ctx.checkpoint()
    await paceBeforeClick()
    await verifiedBossChatTab(fingerprint)
    await osClickOnce(tabId, ctx, clickPlan, what)
    const settled = await pollUntil(ctx, () => readInterviewModal(tabId), done)
    if (!settled.satisfied) {
      throw new PlatformError('ELEMENT_UNRESOLVED', `${what}后未就绪:${describeInterviewModal(settled.value)}`, 'afterRecovery')
    }
    return settled.value
  }

  // 第一步:点「约面试」。可逆——弹的是带「取消」的模态。
  const openPlan: ClickPlan = {
    label: '工具栏约面试钮',
    rect: button.rect,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestContains,
      [TOOLBAR_BUTTON_SELECTOR, button.index, INTERVIEW_BUTTON_INVITE, INTERVIEW_BUTTON_VIEW, x, y]),
    observe: async (): Promise<ClickObservation> =>
      ({ trusted: null, onTarget: null, eventDriftPx: null, after: describeInterviewModal(await readInterviewModal(tabId)) }),
  }
  let modal = await clickAndSettle(openPlan, '点约面试', (m) => m.modal === 1 && m.radios.length === 2 && m.send.found)
  trace.push('模态已开')
  ctx.progress('邀面表单已打开', 20)

  let sent = false
  let dispatchedAt = 0
  try {
    // 面试类型:选中态只在 label 的 radio-checked 上(§五 坑);标题「线上/线下面试邀请」只作观测进 trace。
    const radio = modal.radios.find((r) => r.text === plan.radioText)
    if (!radio) throw new PlatformError('ELEMENT_UNRESOLVED', `面试类型 radio 认不出(${modal.radios.map((r) => r.text).join('/')})`, 'afterRecovery')
    if (!radio.checked) {
      modal = await clickAndSettle(
        interviewClickPlan(tabId, `面试类型「${plan.radioText}」`, INTERVIEW_SEL.radio, radio.index, plan.radioText, radio.rect),
        `点${plan.radioText}`, (m) => m.radios.some((r) => r.text === plan.radioText && r.checked))
      trace.push(`类型=${plan.radioText}(标题「${modal.title}」)`)
    }
    if (plan.method === 'onsite') {
      if (modal.address === null || modal.address === '') {
        // 配置不完美一律降级不转人工:平台没配地址,本轮不发,响亮记日志,脑下轮重来(人在平台配一次地址即自愈)。
        reportHandLog('warn', 'interviewAddressMissing', 'BOSS 邀面表单「面试地址」为空(平台未配地址),本轮零点击不发')
        throw new PlatformError('GUARD_FAILED', '面试地址为空(平台未配地址),本轮不发', 'afterRecovery')
      }
      trace.push('地址已预填')
    } else {
      if (!modal.meeting.present) throw new PlatformError('ELEMENT_UNRESOLVED', '线上面试的「面试平台」下拉认不出', 'afterRecovery')
      if (!bossMeetingChosen(modal, INTERVIEW_MEETING_WECHAT, INTERVIEW_MEETING_WECHAT_CODE)) {
        if (!modal.meeting.open) {
          modal = await clickAndSettle(
            interviewClickPlan(tabId, '面试平台下拉', INTERVIEW_SEL.meeting, modal.meeting.index, null, modal.meeting.rect),
            '点面试平台下拉',
            (m) => m.meeting.open && m.meeting.items.some((i) => i.text.includes(INTERVIEW_MEETING_WECHAT) && i.rect.w > 0 && i.rect.h > 0))
        }
        const item = modal.meeting.items.find((i) => i.text.includes(INTERVIEW_MEETING_WECHAT) && i.rect.w > 0 && i.rect.h > 0)
        if (!item) throw new PlatformError('ELEMENT_UNRESOLVED', `面试平台下拉里没有可见的「${INTERVIEW_MEETING_WECHAT}」`, 'afterRecovery')
        modal = await clickAndSettle({
          label: `面试平台「${INTERVIEW_MEETING_WECHAT}」`,
          rect: item.rect,
          hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestContains, [INTERVIEW_SEL.meetingItem, item.index, INTERVIEW_MEETING_WECHAT, '', x, y]),
          observe: async (): Promise<ClickObservation> =>
            ({ trusted: null, onTarget: null, eventDriftPx: null, after: describeInterviewModal(await readInterviewModal(tabId)) }),
        }, `点${INTERVIEW_MEETING_WECHAT}`, (m) => bossMeetingChosen(m, INTERVIEW_MEETING_WECHAT, INTERVIEW_MEETING_WECHAT_CODE) && !m.meeting.open)
      }
      trace.push(`平台=${INTERVIEW_MEETING_WECHAT}`)
    }
    ctx.progress('面试类型已选', 35)

    // 日期:readonly,只能点开日历选;所在月不是当月就 .next 一次(再远在换算阶段已拒)。
    if (modal.date.value !== plan.date) {
      if (!modal.date.open) {
        modal = await clickAndSettle(
          interviewClickPlan(tabId, '日期框', INTERVIEW_SEL.dateInput, modal.date.index, null, modal.date.rect),
          '点日期框', (m) => m.date.open && m.date.cells.length > 0 && parseBossCalendarMonth(m.date.month) !== null)
      }
      const shown = parseBossCalendarMonth(modal.date.month)
      if (!shown) throw new PlatformError('ELEMENT_UNRESOLVED', `日历月份认不出「${modal.date.month}」`, 'afterRecovery')
      if (shown.year !== plan.year || shown.month !== plan.month) {
        const offset = (plan.year - shown.year) * 12 + (plan.month - shown.month)
        if (offset !== 1) {
          throw new PlatformError('GUARD_FAILED', `日历当前 ${shown.year}-${pad2(shown.month)},目标 ${plan.year}-${pad2(plan.month)},只翻一页`, 'afterRecovery')
        }
        if (!modal.date.nextRect) throw new PlatformError('ELEMENT_UNRESOLVED', '日历翻页钮认不出', 'afterRecovery')
        modal = await clickAndSettle(
          interviewClickPlan(tabId, '日历下一月', INTERVIEW_SEL.dateNext, modal.date.nextIndex, null, modal.date.nextRect),
          '点日历下一月', (m) => {
            const p = parseBossCalendarMonth(m.date.month)
            return !!p && p.year === plan.year && p.month === plan.month && m.date.cells.length > 0
          })
        trace.push('日历翻到下月')
      }
      const picked = pickBossCalendarCell(modal.date.cells, plan.day, plan.isToday)
      if (!picked.cell) throw new PlatformError('ELEMENT_UNRESOLVED', `日历里 ${plan.day} 日的格认不出(命中 ${picked.count})`, 'afterRecovery')
      if (picked.cell.clip.w < 16 || picked.cell.clip.h < 16) {
        throw new PlatformError('ELEMENT_UNRESOLVED',
          `日历 ${plan.day} 日的格在视口外(可见 ${Math.round(picked.cell.clip.w)}x${Math.round(picked.cell.clip.h)},视口高 ${modal.viewport.h})`, 'afterRecovery')
      }
      modal = await clickAndSettle(
        interviewClickPlan(tabId, `日期格 ${picked.cell.text}`, INTERVIEW_SEL.dateCell, picked.cell.index, picked.cell.text, picked.cell.clip),
        `点日期 ${plan.day}`, (m) => m.date.value === plan.date && !m.date.open)
      trace.push(`日期=${plan.date}`)
    }
    ctx.progress('日期已选', 55)

    // 时间:点开 → 「宽松时间」页签 → 开始列 → 结束列(重过滤后)。列表项不在可见区先滚 ul(滚轮注入生产首用)。
    if (modal.time.value !== plan.timeValue) {
      if (!modal.time.open) {
        modal = await clickAndSettle(
          interviewClickPlan(tabId, '时间框', INTERVIEW_SEL.timeInput, modal.time.index, null, modal.time.rect),
          '点时间框', (m) => m.time.open && m.time.tabs.length >= 2)
      }
      const looseReady = (m: DomInterviewModal): boolean => m.time.open &&
        m.time.tabs.some((t) => t.text === INTERVIEW_TIME_LOOSE && t.selected) &&
        m.time.lists.length === 2 && m.time.lists[0]!.items.some((i) => /^\d{2}:\d{2}$/u.test(i.text))
      if (!looseReady(modal)) {
        const looseTab = modal.time.tabs.find((t) => t.text === INTERVIEW_TIME_LOOSE)
        if (!looseTab) throw new PlatformError('ELEMENT_UNRESOLVED', `时间页签认不出(${modal.time.tabs.map((t) => t.text).join('/')})`, 'afterRecovery')
        modal = await clickAndSettle(
          interviewClickPlan(tabId, '宽松时间页签', INTERVIEW_SEL.timeTab, looseTab.index, INTERVIEW_TIME_LOOSE, looseTab.rect),
          '点宽松时间', looseReady)
        trace.push('页签=宽松时间')
      }
      const pickTimeItem = async (listPos: 0 | 1, text: string, what: string, done: (m: DomInterviewModal) => boolean): Promise<DomInterviewModal> => {
        let current = modal
        for (let attempt = 0; ; attempt += 1) {
          const list = current.time.lists[listPos]
          if (!list) throw new PlatformError('ELEMENT_UNRESOLVED', `时间列 ${listPos === 0 ? '开始' : '结束'}不在(可见列 ${current.time.lists.length})`, 'afterRecovery')
          const item = list.items.find((i) => i.text === text)
          if (!item) throw new PlatformError('ELEMENT_UNRESOLVED', `时间列里没有 ${text}(共 ${list.items.length} 项)`, 'afterRecovery')
          const reach = planBossTimeItemReach(list, item, INTERVIEW_TIME_ITEM_MIN_VISIBLE_PX)
          if (reach.status === 'visible') {
            return clickAndSettle(interviewClickPlan(tabId, `时间项 ${text}`, INTERVIEW_SEL.timeItem, item.index, text, reach.rect), what, done)
          }
          if (reach.status === 'unreachable' || attempt >= INTERVIEW_SCROLL_ATTEMPTS) {
            throw new PlatformError('ELEMENT_UNRESOLVED',
              `时间项 ${text} 滚不到可见区(${reach.status === 'unreachable' ? reach.detail : `已滚 ${attempt} 次`})`, 'afterRecovery')
          }
          const target: ScrollTarget = {
            label: `时间列 ${listPos === 0 ? '开始' : '结束'}`,
            rect: list.rect,
            hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestIndexed, [INTERVIEW_SEL.timeList, list.index, x, y]),
            readMetrics: async () => {
              const m = await runInPage(BOSS_DOM, tabId, domReadScrollMetrics, [INTERVIEW_SEL.timeList, list.index])
              return m.found ? { scrollTop: m.scrollTop, scrollHeight: m.scrollHeight, clientHeight: m.clientHeight } : null
            },
          }
          ctx.checkpoint()
          await paceBeforeClick()
          const res = await runOsScroll(BOSS_INJECT, tabId, ctx, target, reach.direction, reach.distancePx)
          trace.push(`滚时间列${reach.direction === 'down' ? '下' : '上'} ${reach.distancePx}px→${res.outcome}(${res.scrollTopBefore}→${res.scrollTopAfter})`)
          if (res.outcome === 'handServiceUnavailable') {
            throw new PlatformError('CTX_NOT_READY', `时间列滚动:手服务不可用(${res.detail ?? ''})`, 'afterRecovery', 'pageBroken')
          }
          if (res.outcome === 'refusedByGate' || res.outcome === 'stuck') {
            throw new PlatformError('ELEMENT_UNRESOLVED', `时间列滚动失败(${res.outcome}):${(res.detail ?? '').slice(0, 200)}`, 'afterRecovery')
          }
          current = await readInterviewModal(tabId)
        }
      }
      modal = await pickTimeItem(0, plan.startText, `点开始 ${plan.startText}`, (m) => m.time.open && m.time.lists.length === 2 &&
        m.time.lists[0]!.items.some((i) => i.text === plan.startText && i.selected) &&
        m.time.lists[1]!.items.length > 0 && m.time.lists[1]!.items[0]!.text === plan.firstEndText)
      trace.push(`开始=${plan.startText}`)
      modal = await pickTimeItem(1, plan.endText, `点结束 ${plan.endText}`, (m) => m.time.value === plan.timeValue && !m.time.open)
      trace.push(`时间=${plan.timeValue}`)
    }
    ctx.progress('时间已选', 75)

    // 最后一道闸(同一 evaluator 复核):六项逐字等于期望,缺一不点。
    const final = await readInterviewModal(tabId)
    const problems = bossInterviewFormMismatch(final, plan)
    if (problems.length > 0) throw new PlatformError('GUARD_FAILED', `发送前复核不过:${problems.join(';')}`, 'afterRecovery')
    const sendPlan: ClickPlan = {
      label: '邀面发送钮',
      rect: final.send.rect,
      hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domBossInterviewSendGate,
        [INTERVIEW_SEL, sendExpect, ROW_SELECTOR, args.conversationRef, ROW_SELECTED_CLASS, x, y]),
      observe: async (): Promise<ClickObservation> => {
        // 「发送」已按下才会走到这里:页面这一秒里被登出/跳转读不到,也不能让错误带着 sideEffect=none 从
        // runOsProbe 抛出去(脑会按 §8.4 重铸)——只记观测,正证循环去判。
        try {
          return { trusted: null, onTarget: null, eventDriftPx: null, after: describeInterviewModal(await readInterviewModal(tabId)) }
        } catch (error) {
          if (isStopExecution(error)) throw error
          return { trusted: null, onTarget: null, eventDriftPx: null, after: `点后读取失败:${describeError(error).slice(0, 120)}` }
        }
      },
    }
    ctx.checkpoint()
    await paceBeforeClick()
    await verifiedBossChatTab(fingerprint)
    if (Date.now() > ctx.irreversibleNotAfterMs) {
      throw new PlatformError('CTX_LOST_DURING_EXEC', '不可逆动作窗口已过,未点发送', 'afterRecovery')
    }
    await ctx.beforeSideEffect()
    dispatchedAt = Date.now()
    const probe = await runOsProbe(BOSS_INJECT, tabId, ctx, sendPlan)
    if (probe.outcome !== 'clicked') {
      // 闸在按下之前拒:没点。收回模态,下轮重来。
      throw new PlatformError(
        probe.outcome === 'handServiceUnavailable' ? 'CTX_NOT_READY' : 'ELEMENT_UNRESOLVED',
        `发送钮未点击:${probe.detail ?? probe.outcome}`, 'afterRecovery')
    }
    sent = true
    trace.push(`点了发送 ${probe.detail ?? ''}`)
  } catch (error) {
    if (!sent && !isStopExecution(error)) await cancelBossInterviewModal(tabId, ctx, describeError(error).slice(0, 120))
    throw error
  }

  // 正证(出口 §〇,或关系):新增出站 21130009 行、time 不早于派发(不看 status,卡片行恒 0);或工具栏变「查看面试」
  // (此时 sourceKey 取窗口最新一条出站邀面行,读不到行继续等)。点击之后任何读取异常都只记 lastSeen,绝不以
  // sideEffect=none 的错误出去——世界可能已被改动。
  const deadline = Date.now() + READY_WAIT_MS
  let lastSeen = ''
  let hit: BossRawMessage | null = null
  let viaToolbar = false
  await sleep(500)
  while (Date.now() < deadline && hit === null) {
    ctx.checkpoint()
    try {
      const after = await runInPage(BOSS_INJECT, tabId, mainReadBossThread, [parsed.uid, parsed.friendSource])
      if (after.status === 'ready') {
        const fresh = after.rows.filter((row) => !baselineMids.has(row.mid) && isBossInterviewSentRow(row))
        const inWindow = fresh.filter((row) => !(row.time !== null && row.time < dispatchedAt - SEND_CLOCK_TOLERANCE_MS))
        lastSeen = `新行 ${after.rows.filter((row) => !baselineMids.has(row.mid)).length},出站邀面行 ${fresh.length},窗内 ${inWindow.length}`
        if (inWindow.length >= 1) {
          hit = latestBossRow(inWindow)
          break
        }
        const toolbar = await readInterviewButton(tabId)
        if (toolbar.found && toolbar.kind === 'view') {
          if (fresh.length >= 1) {
            hit = latestBossRow(fresh)
            viaToolbar = true
            break
          }
          lastSeen += ',工具栏已「查看面试」但无出站邀面行'
        }
      } else {
        lastSeen = `消息列表 ${after.status}`
      }
    } catch (error) {
      if (isStopExecution(error)) throw error
      lastSeen = `读取异常 ${describeError(error).slice(0, 120)}`
    }
    await sleep(500)
  }
  // 清场:成功对话框「面试邀请已发出」锁着页面,关不掉只记日志(见 dismissBossInterviewSuccessPopup 说明)。
  await dismissBossInterviewSuccessPopup(tabId, ctx)
  if (hit === null) {
    throw new PlatformError('POSTCONDITION_UNCONFIRMED',
      `只点击了一次发送,但未确认出站邀面行或工具栏「查看面试」(${lastSeen};${trace.join(' | ')})`,
      'manualOnly', undefined, 'possible')
  }
  const data: ChatSendInviteCardData = {
    conversationRef: args.conversationRef,
    contentHash,
    sourceKey: await bossSourceKey(hit.mid),
    observedAt: Date.now(),
    interview: args.interview,
    ...(hit.time !== null && hit.time > 0 ? { tsApprox: hit.time } : {}),
  }
  if (validatePrimitiveData(PrimitiveName.ChatSendInviteCard, 1, data).length !== 0) {
    throw new PlatformError('POSTCONDITION_UNCONFIRMED', '邀面卡结果不符合当前契约', 'manualOnly', undefined, 'possible')
  }
  try {
    await verifiedBossChatTab(fingerprint)
  } catch (error) {
    if (isStopExecution(error)) throw error
    throw new PlatformError('POSTCONDITION_UNCONFIRMED', `邀面卡已发出但账号页复核失败:${describeError(error).slice(0, 120)}`,
      'manualOnly', undefined, 'possible')
  }
  ctx.progress(viaToolbar ? '工具栏已变「查看面试」,邀面卡已发出' : '已从当前消息列表确认邀面卡已发出', 100)
  console.info('[RecruitHelper] boss_send_invite_card', trace.join(' | '))
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

// ── 第二刀:推荐页采集 + 打招呼(2026-09-05 开工) ─────────────────────────────────
//
// 出口:docs/boss/第二刀出口-采集与打招呼-2026-09-04.md(甲方定方案一:招呼正文在推荐页的快捷聊天窗里发,
// 脑与契约零改动);事实:docs/boss/BOSS平台事实-2026-08-28.md §十七。推荐页整张画在同源 iframe
// `iframe[name=recommendFrame]` 里——定位、内存读、OS 落点全部经 contentDocument,坐标换算回顶层视口。
// 下面的页面函数各自内联这段框架跳转:它们经 executeScript 序列化注入,引用不到模块里的共享函数。
//
// 身份一条线(§十七,已坐实):卡片 geekId/geekSource/encryptGeekId 与 IM 侧 uid/friendSource/encryptUid
// 逐字相等。platformUserRef = String(geekId),conversationRef = "{geekId}-{geekSource}" 首击当场可算,
// positionRef = encryptJobId(职位选择器 .job-item[value] 与卡片同值;出口 §四 第 3 件)。

const BOSS_RECOMMEND_URL = `${BOSS_ORIGIN}/web/chat/recommend`
const BOSS_JOB_LIST_URL = `${BOSS_ORIGIN}/web/chat/job/list`
const RECOMMEND_FRAME = 'iframe[name=recommendFrame]'
/** 导航后推荐页/职位管理页的就绪等待:2026-09-03 甲方裁决同款 60 秒封顶(智联侧 84771d6);其余等待仍 20 秒。 */
const RECOMMEND_NAV_WAIT_MS = 60_000
/** 列表刷新后的稳定判据:签名连续这么久没变才算稳。 */
const RECOMMEND_STABLE_MS = 1_500
const RECOMMEND_SEL = Object.freeze({
  jobItem: '.job-selecter-wrap .job-item',
  jobItemCurrentClass: 'curr',
  filterLabel: '.filter-wrap .filter-label',
  filterPanel: '.filter-panel',
  filterBlock: '.filter-panel .filters-wrap',
  filterVipBlockClass: 'vip-filters',
  filterGroup: '.filter-wrap',
  filterGroupName: '.name',
  filterBox: '.check-box',
  filterOption: '.option',
  filterOptionActiveClass: 'active',
  filterOptionDefaultClass: 'default',
  filterButton: '.filter-panel .btns .btn',
  vipMask: '.vip-mask',
  listView: '#recommend-list',
  cardList: 'ul.card-list',
  cardItem: 'li.card-item',
  cardInner: '.card-inner',
  cardButton: '.button-chat-wrap button',
  greetButton: 'button.btn-greet',
  continueButton: 'button.btn-continue',
})
const JOB_LIST_SEL = Object.freeze({
  frameSrc: '/web/frame/job_v2/list',
  tab: '.tab-item',
  row: 'li.job-item-container',
  rowName: '.job-name',
  rowStatus: '.status-box',
})
const QUICK_CHAT_SEL = Object.freeze({
  window: '.chat-global-conversation',
  messageList: '.chat-global-msg-content',
  composerId: 'boss-chat-global-input',
  sendButton: '.submit-content .submit',
  close: '.chat-global-top .iboss-close',
})
const FILTER_GROUP = Object.freeze({ career: '求职状态', education: '学历要求', experience: '经验要求', salary: '薪资待遇' })
/** VIP 锁定组(平台事实 §十七 `.vip-mask`):只回读不点,配置须为不限(出口 §四 第 2 件,运营侧约定)。 */
const FILTER_VIP_GROUPS = Object.freeze(['活跃度', '性别', '近期没有看过', '是否与同事交换简历'])
const FILTER_ANY = '不限'
const FILTER_CONFIRM = '确定'
const FILTER_LABEL = '筛选'
const BOSS_ONLINE_LABEL_PLATFORM = '开放中'
const BOSS_ONLINE_LABEL_CONTRACT = '在线中'
const BOSS_JOB_TAB_ALL = '全部'
const GREET_TEXT = '打招呼'
const CONTINUE_TEXT = '继续沟通'
const SEND_TEXT = '发送'

const BOSS_CAREER_LABELS: Readonly<Record<SourcingCareerStatus, string>> = Object.freeze({
  leftLooking: '离职-随时到岗',
  employedNotLooking: '在职-暂不考虑',
  employedOpen: '在职-考虑机会',
  employedLooking: '在职-月内到岗',
})
/** BOSS 学历组没有 MBA/EMBA 项(平台事实 §十七 面板枚举),配置要它就干净失败,不就近取硕士。 */
const BOSS_EDUCATION_LABELS: Readonly<Partial<Record<SourcingEducation, string>>> = Object.freeze({
  juniorHighOrBelow: '初中及以下',
  secondaryVocational: '中专/中技',
  highSchool: '高中',
  associate: '大专',
  bachelor: '本科',
  master: '硕士',
  doctorate: '博士',
})

function isBossRecommendUrl(value: string | undefined): boolean {
  if (!value) return false
  try { return new URL(value).pathname === '/web/chat/recommend' } catch { return false }
}

function isBossJobListUrl(value: string | undefined): boolean {
  if (!value) return false
  try { return new URL(value).pathname === '/web/chat/job/list' } catch { return false }
}

export function parseBossGeekId(platformUserRef: string): number | null {
  if (!/^[1-9]\d{0,17}$/u.test(platformUserRef)) return null
  const value = Number(platformUserRef)
  return Number.isSafeInteger(value) ? value : null
}

/** 组名去掉「[单选]」一类后缀:面板里「薪资待遇[单选]」「活跃度[单选]」是同一组的展示写法。 */
export function bossFilterGroupName(raw: string): string {
  return normalizeBossMessageText(raw).replace(/\[[^\]]*\]\s*$/u, '').trim()
}

/** 职位选择器项文案「职位名 _ 城市 薪资」取职位名;没有分隔就是整段。 */
export function bossJobItemName(text: string): string {
  const cleaned = normalizeBossMessageText(text)
  const at = cleaned.indexOf(' _ ')
  return at < 0 ? cleaned : cleaned.slice(0, at).trim()
}

export interface BossJobItem { text: string; value: string; current: boolean }

/**
 * 按标题在职位选择器里做唯一精确匹配:整段相等、职位名相等、或整段以「标题 _ 」开头(标题自带下划线时
 * 前一种切法会切错)。不模糊、不就近。
 */
export function matchBossJobItem(
  items: readonly BossJobItem[], positionTitle: string,
): { status: 'ok'; index: number; name: string; value: string; current: boolean } | { status: 'none' | 'ambiguous'; count: number } {
  const title = normalizeBossMessageText(positionTitle)
  if (!title) return { status: 'none', count: 0 }
  const hits = items
    .map((item, index) => ({ item, index, text: normalizeBossMessageText(item.text) }))
    .filter(({ item, text }) => text === title || bossJobItemName(item.text) === title || text.startsWith(`${title} _ `))
  if (hits.length === 0) return { status: 'none', count: 0 }
  if (hits.length > 1) return { status: 'ambiguous', count: hits.length }
  const hit = hits[0]!
  // 三种相等都蕴含「页面上的职位名就是这个标题」,名字回传规范化后的标题本身(标题自带「 _ 」时切职位名会切错)。
  return { status: 'ok', index: hit.index, name: title, value: hit.item.value.trim(), current: hit.item.current }
}

export interface BossFilterPlan { career: string[]; education: string[] }

/** 契约筛选 → 面板目标(只有非 VIP 的求职状态/学历能按图索骥);VIP 锁定组要求配置为不限。 */
export function planBossSourcingFilters(
  filters: CandidateSourcingFilters,
): { ok: true; plan: BossFilterPlan } | { ok: false; reason: string } {
  const locked: string[] = []
  if (filters.age.mode !== 'any') locked.push('年龄')
  if (filters.activeWindow !== 'any') locked.push('活跃度')
  if (filters.gender !== 'any') locked.push('性别')
  if (filters.excludeViewed) locked.push('近期没有看过')
  if (filters.excludeCoworkerContacted) locked.push('是否与同事交换简历')
  if (locked.length > 0) {
    return { ok: false, reason: `这些筛选组在 BOSS 上被 VIP 锁定、手侧只回读不点,候选人筛选文档须配「不限」:${locked.join('/')}` }
  }
  const career: string[] = []
  for (const status of filters.careerStatuses) {
    const label = BOSS_CAREER_LABELS[status]
    if (!label) return { ok: false, reason: `求职状态 ${status} 在 BOSS 上没有对应选项` }
    if (!career.includes(label)) career.push(label)
  }
  const education: string[] = []
  for (const level of filters.educations) {
    const label = BOSS_EDUCATION_LABELS[level]
    if (!label) return { ok: false, reason: `学历 ${level} 在 BOSS 上没有对应选项(面板只有初中及以下/中专中技/高中/大专/本科/硕士/博士)` }
    if (!education.includes(label)) education.push(label)
  }
  return { ok: true, plan: { career, education } }
}

export interface BossFilterOptionRead { text: string; active: boolean; isDefault: boolean }
export interface BossFilterGroupRead { name: string; boxKey: string; vip: boolean; options: BossFilterOptionRead[] }
export interface BossFilterPanelRead { frame: boolean; panel: boolean; masked: boolean; groups: BossFilterGroupRead[]; buttons: string[] }

export interface BossFilterClick { boxKey: string; index: number; text: string; expectActive: boolean }

/**
 * 逐项差异覆盖的点击清单(契约 applySourcingFilters「逐项差异覆盖」)。四个非 VIP 组各自:目标为空
 * 就点「不限」(若尚未选中);目标非空就先点该点上的、再点该点掉的——「不限」在点上别的项时由平台自动
 * 退选(2026-09-05 真机:点「本科」后「不限」失去 active)。经验/薪资契约里没有,目标恒为不限:
 * 真人留下的手工筛选会悄悄收窄推荐流,配置才是唯一事实源。
 */
export function bossFilterClicks(
  read: BossFilterPanelRead, plan: BossFilterPlan,
): { ok: true; clicks: BossFilterClick[] } | { ok: false; reason: string } {
  const targets: Array<[string, string[]]> = [
    [FILTER_GROUP.career, plan.career], [FILTER_GROUP.education, plan.education],
    [FILTER_GROUP.experience, []], [FILTER_GROUP.salary, []],
  ]
  const clicks: BossFilterClick[] = []
  for (const [groupName, wanted] of targets) {
    const groups = read.groups.filter((group) => !group.vip && bossFilterGroupName(group.name) === groupName)
    if (groups.length !== 1) return { ok: false, reason: `筛选组「${groupName}」命中 ${groups.length} 个` }
    const group = groups[0]!
    if (!group.boxKey) return { ok: false, reason: `筛选组「${groupName}」没有可定位的组键` }
    const anyIndex = group.options.findIndex((option) => option.text === FILTER_ANY)
    if (anyIndex < 0) return { ok: false, reason: `筛选组「${groupName}」没有「${FILTER_ANY}」项` }
    for (const label of wanted) {
      if (!group.options.some((option) => option.text === label)) {
        return { ok: false, reason: `筛选组「${groupName}」没有「${label}」项(现有:${group.options.map((o) => o.text).join('/')})` }
      }
    }
    if (wanted.length === 0) {
      if (!group.options[anyIndex]!.active) clicks.push({ boxKey: group.boxKey, index: anyIndex, text: FILTER_ANY, expectActive: true })
      continue
    }
    group.options.forEach((option, index) => {
      if (option.text !== FILTER_ANY && wanted.includes(option.text) && !option.active) {
        clicks.push({ boxKey: group.boxKey, index, text: option.text, expectActive: true })
      }
    })
    group.options.forEach((option, index) => {
      if (option.text !== FILTER_ANY && !wanted.includes(option.text) && option.active) {
        clicks.push({ boxKey: group.boxKey, index, text: option.text, expectActive: false })
      }
    })
  }
  return { ok: true, clicks }
}

/**
 * 面板回读 → 契约 filters。与目标逐组核对(集合相等,不看顺序);全部相等就原样回传请求的 filters——
 * 脑侧用 reflect.DeepEqual 比较,数组顺序也算,回传请求本身是唯一与配置逐字节相等的写法。
 * VIP 组缺席视为不限(平台没给这组就没有东西在过滤);年龄滑块 DOM 上读不出值,被 `.vip-mask` 盖着时
 * 不可能被改过——VIP 账号的滑块回读是后置项(出口 §五 4)。
 */
export function projectBossSourcingFilters(
  read: BossFilterPanelRead, requested: CandidateSourcingFilters,
): { ok: true; filters: CandidateSourcingFilters } | { ok: false; reason: string } {
  const planned = planBossSourcingFilters(requested)
  if (!planned.ok) return planned
  const sameSet = (a: string[], b: string[]): boolean => a.length === b.length && a.every((x) => b.includes(x))
  const check = (groupName: string, vip: boolean, wanted: string[]): string | null => {
    const groups = read.groups.filter((group) => group.vip === vip && bossFilterGroupName(group.name) === groupName)
    if (groups.length === 0) return vip ? null : `筛选组「${groupName}」认不出`
    if (groups.length > 1) return `筛选组「${groupName}」命中 ${groups.length} 个`
    const active = groups[0]!.options.filter((option) => option.active).map((option) => option.text)
    const expected = wanted.length > 0 ? wanted : [FILTER_ANY]
    if (!sameSet(active, expected)) return `筛选组「${groupName}」回读为「${active.join('/') || '(空)'}」,目标「${expected.join('/')}」`
    return null
  }
  const problems = [
    check(FILTER_GROUP.career, false, planned.plan.career),
    check(FILTER_GROUP.education, false, planned.plan.education),
    check(FILTER_GROUP.experience, false, []),
    check(FILTER_GROUP.salary, false, []),
    ...FILTER_VIP_GROUPS.map((name) => check(name, true, [])),
  ].filter((problem): problem is string => problem !== null)
  if (problems.length > 0) return { ok: false, reason: problems.join(';') }
  return { ok: true, filters: requested }
}

/**
 * 职位管理页「全部」页签一屏列出所有职位、逐行带状态文案(平台事实 §十七),所以不逐分区切页签:
 * 分区 = 页签文案(去「全部」、去计数后缀),行按自己的状态文案归入;状态不在页签里的行按原样文案另起
 * 一区带出(存在性判定取并集,少一个名字方向就是多发)。「开放中」投影成契约/脑侧的「在线中」
 * (出口 §四 第 4 件:映射放手侧,脑保持平台无关)。
 */
export function bossJobListSections(
  tabs: readonly string[], rows: ReadonlyArray<{ name: string; status: string }>,
): { ok: true; sections: JobPostingSection[]; extraLabels: string[] } | { ok: false; reason: string } {
  const stripCount = (raw: string): string => normalizeBossMessageText(raw).replace(/[\s·]*\d+$/u, '').trim()
  const labels = tabs.map(stripCount).filter((label) => label !== '' && label !== BOSS_JOB_TAB_ALL)
  if (labels.length === 0) return { ok: false, reason: '职位管理页没有状态分区页签' }
  if (new Set(labels).size !== labels.length) return { ok: false, reason: `状态分区页签重名:${labels.join('/')}` }
  const sections: JobPostingSection[] = labels.map((label) => ({
    label: label === BOSS_ONLINE_LABEL_PLATFORM ? BOSS_ONLINE_LABEL_CONTRACT : label,
    names: [],
  }))
  const extras = new Map<string, string[]>()
  for (const row of rows) {
    const name = normalizeBossMessageText(row.name)
    const status = stripCount(row.status)
    if (!name) return { ok: false, reason: '有职位行读不到职位名' }
    if (!status) return { ok: false, reason: `职位「${name}」读不到状态文案` }
    const index = labels.indexOf(status)
    if (index >= 0) {
      sections[index]!.names.push(name)
    } else {
      const list = extras.get(status) ?? []
      list.push(name)
      extras.set(status, list)
    }
  }
  for (const [label, names] of extras) sections.push({ label, names })
  return { ok: true, sections, extraLabels: [...extras.keys()] }
}

export interface BossRecommendCardLite {
  geekId: number
  geekSource: number
  encryptGeekId: string
  encryptJobId: string
  isFriend: number | null
  buttonText: string
  visible: boolean
}

export interface BossRecommendCard extends BossRecommendCardLite {
  name: string
  ageDesc: string
  degree: string
  workYear: string
  salary: string
  activeTimeDesc: string
  desc: string
  edus: Array<{ school: string; major: string; degree: string; start: string; end: string }>
  works: Array<{ company: string; position: string; start: string; end: string; responsibility: string }>
  expect: { location: string; position: string; salary: string }
}

/** 关系态判据(出口 §〇):isFriend=1 或按钮「继续沟通」= 已建立;isFriend=0 且按钮「打招呼」= 未建立;其余 unknown。 */
export function bossCardContactState(card: Pick<BossRecommendCardLite, 'isFriend' | 'buttonText'>): CandidateContactState {
  if (card.isFriend === 1 || card.buttonText === CONTINUE_TEXT) return 'established'
  if (card.isFriend === 0 && card.buttonText === GREET_TEXT) return 'unestablished'
  return 'unknown'
}

/**
 * 卡片 geekInfo → 契约五分区(出口 §四 第 6 件:不开详情——详情正文是 canvas,右栏只多一份经历概览)。
 * 标签与智联/readResume 对齐;空值整行省略。
 */
export function projectBossSourcingResume(
  card: BossRecommendCard, positionRef: string, positionTitle: string | null, observedAt: number,
): CandidateReadSourcingResumeData {
  const clean = (value: string): string => normalizeBossMessageText(value)
  const push = (list: CandidateResumeLabelValue[], label: string, value: string): void => {
    const cleaned = clean(value)
    if (cleaned) list.push({ label, value: cleaned })
  }
  const basic: CandidateResumeLabelValue[] = []
  push(basic, '姓名', card.name)
  push(basic, '年龄', card.ageDesc)
  push(basic, '工作经验', card.workYear)
  push(basic, '最高学历', card.degree)
  push(basic, '活跃时间', card.activeTimeDesc)
  const expectations: CandidateResumeLabelValue[] = []
  push(expectations, '期望职位', card.expect.position)
  push(expectations, '期望城市', card.expect.location)
  push(expectations, '期望薪资', card.salary || card.expect.salary)
  const range = (start: string, end: string): string => [clean(start), clean(end)].filter(Boolean).join('-')
  const join = (parts: string[]): string => parts.map(clean).filter(Boolean).join(' · ')
  const education = card.edus
    .map((item) => [range(item.start, item.end), join([item.school, item.major, item.degree])].filter(Boolean).join(' '))
    .filter(Boolean)
    .join('\n\n')
  const workExperiences = card.works
    .map((item) => {
      const head = [range(item.start, item.end), join([item.company, item.position])].filter(Boolean).join(' ')
      const body = clean(item.responsibility)
      return [head, body].filter(Boolean).join('\n')
    })
    .filter(Boolean)
    .join('\n\n')
  const name = clean(card.name)
  return {
    platformUserRef: String(card.geekId),
    displayName: name ? name.slice(0, 256) : null,
    positionRef,
    positionTitle: positionTitle && positionTitle.length <= 256 ? positionTitle : null,
    contactState: bossCardContactState(card),
    observedAt,
    basic,
    expectations,
    selfEvaluation: clean(card.desc),
    education,
    workExperiences,
  }
}

// ── 页面函数:推荐页(MAIN world 读 iframe 内存;isolated 读 DOM) ────────────────

type BossRecommendWindowRead =
  | { status: 'no_frame' }
  | { status: 'no_list' }
  | {
    status: 'ready'
    loading: boolean
    finished: boolean
    positionRef: string
    positionText: string
    jobItems: BossJobItem[]
    cards: BossRecommendCardLite[]
    domCount: number
    aligned: boolean
    scrollTop: number
    scrollHeight: number
    clientHeight: number
  }

/**
 * 推荐窗口:`ul.card-list` 的 vm 持有 `pageList`(与 DOM `li.card-item` 顺序一致,30/30 已核),`#recommend-list`
 * 的 vm 持有 loading/finished。每张卡只取身份、关系态、按钮文案与可见性;简历投影另走 mainReadBossRecommendTarget。
 * `aligned`:DOM 卡数等于 pageList 且每张 `.card-inner[data-geekid]` 等于 encryptGeekId——刚翻页的卡 vm 会晚几百毫秒
 * 才挂上(§十七),没对齐就是没就绪。
 */
function mainReadBossRecommendWindow(
  frameSel: string, listViewSel: string, cardListSel: string, cardItemSel: string, cardInnerSel: string,
  buttonSel: string, jobItemSel: string, currentClass: string,
): BossRecommendWindowRead {
  type AnyRecord = Record<string, unknown>
  const asRecord = (value: unknown): AnyRecord | null =>
    value && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const num = (value: unknown): number | null => (typeof value === 'number' && Number.isFinite(value) ? value : null)
  const str = (value: unknown): string => (typeof value === 'string' ? value : typeof value === 'number' ? String(value) : '')
  const frame = document.querySelector(frameSel)
  const doc = frame && 'contentDocument' in frame ? (frame as HTMLIFrameElement).contentDocument : null
  if (!doc) return { status: 'no_frame' }
  const listEl = doc.querySelector(cardListSel)
  const listVm = listEl ? (listEl as unknown as { __vue__?: AnyRecord }).__vue__ : undefined
  let pageList: unknown
  try { pageList = listVm?.pageList } catch { pageList = undefined }
  if (!listVm || !Array.isArray(pageList)) return { status: 'no_list' }
  const viewEl = doc.querySelector(listViewSel)
  const viewVm = viewEl ? (viewEl as unknown as { __vue__?: AnyRecord }).__vue__ : undefined
  let loading = false
  let finished = false
  try { loading = viewVm?.loading === true; finished = viewVm?.finished === true } catch { loading = false }
  const items = Array.from(doc.querySelectorAll(cardItemSel))
  const scroller = doc.scrollingElement
  const viewportHeight = scroller ? scroller.clientHeight : doc.documentElement.clientHeight
  let aligned = items.length === pageList.length
  const cards: BossRecommendCardLite[] = pageList.map((raw, index) => {
    const info = asRecord(raw)
    const item = items[index]
    const inner = item ? item.querySelector(cardInnerSel) : null
    const rect = item ? item.getBoundingClientRect() : null
    const button = item ? item.querySelector(buttonSel) : null
    const encryptGeekId = str(info?.encryptGeekId)
    if (!inner || encryptGeekId === '' || inner.getAttribute('data-geekid') !== encryptGeekId) aligned = false
    return {
      geekId: num(info?.geekId) ?? 0,
      geekSource: num(info?.geekSource) ?? 0,
      encryptGeekId,
      encryptJobId: str(info?.encryptJobId),
      isFriend: num(info?.isFriend),
      buttonText: (button?.textContent ?? '').trim(),
      visible: !!rect && rect.bottom > 1 && rect.top < viewportHeight - 1,
    }
  })
  const jobItems: BossJobItem[] = Array.from(doc.querySelectorAll(jobItemSel)).map((el) => ({
    text: (el.textContent ?? '').replace(/\s+/gu, ' ').trim(),
    value: el.getAttribute('value') ?? '',
    current: el.classList.contains(currentClass),
  }))
  const current = jobItems.filter((item) => item.current)
  return {
    status: 'ready',
    loading,
    finished,
    positionRef: current.length === 1 ? current[0]!.value.trim() : '',
    positionText: current.length === 1 ? current[0]!.text : '',
    jobItems,
    cards,
    domCount: items.length,
    aligned,
    scrollTop: scroller ? scroller.scrollTop : 0,
    scrollHeight: scroller ? scroller.scrollHeight : 0,
    clientHeight: viewportHeight,
  }
}

type BossRecommendTargetRead =
  | { status: 'no_frame' }
  | { status: 'no_list' }
  | { status: 'absent' }
  | { status: 'duplicated'; count: number }
  | { status: 'ready'; positionRef: string; positionText: string; card: BossRecommendCard }

/** 目标卡的完整投影源:按 geekId 在 pageList 里唯一匹配,读 geekInfo 的摘要字段,不开详情、不点任何东西。 */
function mainReadBossRecommendTarget(
  frameSel: string, cardListSel: string, cardItemSel: string, cardInnerSel: string, buttonSel: string,
  jobItemSel: string, currentClass: string, geekId: number,
): BossRecommendTargetRead {
  type AnyRecord = Record<string, unknown>
  const asRecord = (value: unknown): AnyRecord | null =>
    value && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const num = (value: unknown): number | null => (typeof value === 'number' && Number.isFinite(value) ? value : null)
  const str = (value: unknown): string => (typeof value === 'string' ? value : typeof value === 'number' ? String(value) : '')
  const list = (value: unknown): AnyRecord[] =>
    Array.isArray(value) ? value.map(asRecord).filter((item): item is AnyRecord => item !== null) : []
  const frame = document.querySelector(frameSel)
  const doc = frame && 'contentDocument' in frame ? (frame as HTMLIFrameElement).contentDocument : null
  if (!doc) return { status: 'no_frame' }
  const listEl = doc.querySelector(cardListSel)
  const listVm = listEl ? (listEl as unknown as { __vue__?: AnyRecord }).__vue__ : undefined
  let pageList: unknown
  try { pageList = listVm?.pageList } catch { pageList = undefined }
  if (!listVm || !Array.isArray(pageList)) return { status: 'no_list' }
  const hits: Array<{ info: AnyRecord; index: number }> = []
  pageList.forEach((raw, index) => {
    const info = asRecord(raw)
    if (info && num(info.geekId) === geekId) hits.push({ info, index })
  })
  if (hits.length === 0) return { status: 'absent' }
  if (hits.length > 1) return { status: 'duplicated', count: hits.length }
  const { info, index } = hits[0]!
  const items = Array.from(doc.querySelectorAll(cardItemSel))
  const item = items[index]
  const inner = item ? item.querySelector(cardInnerSel) : null
  const encryptGeekId = str(info.encryptGeekId)
  if (!inner || encryptGeekId === '' || inner.getAttribute('data-geekid') !== encryptGeekId) return { status: 'absent' }
  const rect = item!.getBoundingClientRect()
  const scroller = doc.scrollingElement
  const viewportHeight = scroller ? scroller.clientHeight : doc.documentElement.clientHeight
  const button = item!.querySelector(buttonSel)
  const desc = asRecord(info.geekDesc)
  const expect = asRecord(info.viewExpect)
  const low = str(expect?.lowSalary)
  const high = str(expect?.highSalary)
  const jobItems = Array.from(doc.querySelectorAll(jobItemSel)).filter((el) => el.classList.contains(currentClass))
  return {
    status: 'ready',
    positionRef: jobItems.length === 1 ? (jobItems[0]!.getAttribute('value') ?? '').trim() : '',
    positionText: jobItems.length === 1 ? (jobItems[0]!.textContent ?? '').replace(/\s+/gu, ' ').trim() : '',
    card: {
      geekId,
      geekSource: num(info.geekSource) ?? 0,
      encryptGeekId,
      encryptJobId: str(info.encryptJobId),
      isFriend: num(info.isFriend),
      buttonText: (button?.textContent ?? '').trim(),
      visible: rect.bottom > 1 && rect.top < viewportHeight - 1,
      name: str(info.geekName),
      ageDesc: str(info.ageDesc),
      degree: str(info.geekDegree),
      workYear: str(info.geekWorkYear),
      salary: str(info.salary),
      activeTimeDesc: str(info.activeTimeDesc),
      desc: str(desc?.content),
      edus: list(info.geekEdus).map((edu) => ({
        school: str(edu.school), major: str(edu.major), degree: str(edu.degreeName), start: str(edu.startDate), end: str(edu.endDate),
      })),
      works: list(info.geekWorks).map((work) => ({
        company: str(work.company), position: str(work.positionName), start: str(work.startDate), end: str(work.endDate),
        responsibility: str(work.responsibility),
      })),
      expect: {
        location: str(expect?.location),
        position: str(expect?.position),
        salary: low && high ? `${low}-${high}` : low || high,
      },
    },
  }
}

type BossQuickChatRead =
  | { status: 'closed' }
  | { status: 'no_geek' }
  | { status: 'ready'; uid: number; friendSource: number; encryptUid: string; rows: BossRawMessage[] }

/**
 * 顶层快捷聊天窗(平台事实 §十七「继续沟通与快捷聊天窗」):`.chat-global-conversation` 里
 * `.chat-global-msg-content` 的 vm 自持 `geek`(与 IM 列表行同形:uid/friendSource/encryptUid);消息行是与 IM 页
 * 同一个 message-component,从各实例的 `message` prop 收(mid/body/fromId/time/status 同形)。顶层没有 IM 页那套
 * `list$/conversation$/user$`(§十七),所以不能复用 mainReadBossThread,也不能靠扫元素自有的 `user$` 定我方身份。
 * 方向首选消息对象自带的 `isSelf`(出口判据表第三行);它不是布尔时退回 fromId === 我方 userId(沿 `$parent` 上行找
 * `user$`,与 mainReadBossPrincipal 同一走法);两者都没有就归 system——永不猜成 out(出口审查 O2)。
 */
function mainReadBossQuickChat(windowSel: string, listSel: string): BossQuickChatRead {
  type AnyRecord = Record<string, unknown>
  const asRecord = (value: unknown): AnyRecord | null =>
    value && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : null
  const num = (value: unknown): number | null => (typeof value === 'number' && Number.isFinite(value) ? value : null)
  const win = document.querySelector(windowSel)
  if (!win) return { status: 'closed' }
  const listEl = win.querySelector(listSel)
  const listVm = listEl ? (listEl as unknown as { __vue__?: AnyRecord }).__vue__ : undefined
  let geek: AnyRecord | null = null
  try { geek = asRecord(listVm?.geek) } catch { geek = null }
  if (!geek || typeof geek.uid !== 'number' || typeof geek.friendSource !== 'number') return { status: 'no_geek' }
  const uid = geek.uid
  const friendSource = geek.friendSource
  let myUserId: number | null = null
  const visited = new Set<unknown>()
  for (const element of Array.from(document.querySelectorAll('*'))) {
    let node = (element as unknown as { __vue__?: AnyRecord }).__vue__
    for (let hops = 0; node && hops < 100; hops += 1) {
      if (visited.has(node)) break
      visited.add(node)
      let user: unknown
      try { user = node.user$ } catch { user = undefined }
      const raw = asRecord(user)?.userId
      if (typeof raw === 'number' && Number.isSafeInteger(raw) && raw > 0) { myUserId = raw; break }
      node = asRecord(node.$parent) ?? undefined
    }
    if (myUserId !== null) break
  }
  const rows: BossRawMessage[] = []
  const mids = new Set<string>()
  const seenRows = new Set<unknown>()
  for (const element of Array.from(win.querySelectorAll('*'))) {
    const instance = (element as unknown as { __vue__?: AnyRecord }).__vue__
    if (!instance || seenRows.has(instance)) continue
    seenRows.add(instance)
    let message: AnyRecord | null = null
    try {
      const props = asRecord(instance.$props)
      message = asRecord(props ? props.message : instance.message)
    } catch { message = null }
    if (!message) continue
    const mid = message.mid
    if ((typeof mid !== 'number' && typeof mid !== 'string') || !('body' in message)) continue
    const key = String(mid)
    if (mids.has(key)) continue
    mids.add(key)
    const body = asRecord(message.body)
    const fromId = num(message.fromId)
    const isSelf = message.isSelf
    const direction: BossRawMessage['direction'] = typeof isSelf === 'boolean'
      ? (isSelf ? 'out' : fromId === uid ? 'in' : 'system')
      : (myUserId !== null && fromId === myUserId ? 'out' : fromId === uid ? 'in' : 'system')
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
      mid: key,
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
  rows.sort((a, b) => (Number(a.mid) < Number(b.mid) ? -1 : Number(a.mid) > Number(b.mid) ? 1 : 0))
  return { status: 'ready', uid, friendSource, encryptUid: typeof geek.encryptUid === 'string' ? geek.encryptUid : '', rows }
}

/** 筛选面板(isolated):两块 `.filters-wrap`(VIP 块带 vip-filters 类且被 `.vip-mask` 盖住),每组名+选项+选中态。 */
function domReadBossFilterPanel(
  frameSel: string, panelSel: string, blockSel: string, vipClass: string, groupSel: string, nameSel: string,
  boxSel: string, optionSel: string, activeClass: string, defaultClass: string, maskSel: string, buttonSel: string,
): BossFilterPanelRead {
  const frame = document.querySelector(frameSel)
  const doc = frame && 'contentDocument' in frame ? (frame as HTMLIFrameElement).contentDocument : null
  if (!doc) return { frame: false, panel: false, masked: false, groups: [], buttons: [] }
  const panel = doc.querySelector(panelSel)
  if (!panel) return { frame: true, panel: false, masked: false, groups: [], buttons: [] }
  const groups: BossFilterGroupRead[] = []
  for (const block of Array.from(doc.querySelectorAll(blockSel))) {
    const vip = block.classList.contains(vipClass)
    for (const group of Array.from(block.querySelectorAll(groupSel))) {
      const box = group.querySelector(boxSel)
      const boxKey = box
        ? Array.from(box.classList).filter((cls) => cls !== boxSel.replace(/^\./u, '') && /^[A-Za-z0-9_-]+$/u.test(cls))[0] ?? ''
        : ''
      groups.push({
        name: (group.querySelector(nameSel)?.textContent ?? '').trim(),
        boxKey,
        vip,
        options: Array.from(group.querySelectorAll(optionSel)).map((option) => ({
          text: (option.textContent ?? '').trim(),
          active: option.classList.contains(activeClass),
          isDefault: option.classList.contains(defaultClass),
        })),
      })
    }
  }
  const mask = doc.querySelector(maskSel)
  const maskRect = mask ? mask.getBoundingClientRect() : null
  return {
    frame: true,
    panel: true,
    masked: !!maskRect && maskRect.width > 0 && maskRect.height > 0,
    groups,
    buttons: Array.from(doc.querySelectorAll(buttonSel)).map((button) => (button.textContent ?? '').trim()),
  }
}

interface BossJobListRead { frame: boolean; tabs: string[]; rows: Array<{ name: string; status: string }>; total: number | null }

/** 职位管理页(isolated):内容在同源无名 iframe `/web/frame/job_v2/list` 里;页脚「共 N 个职位」是读全的判据。 */
function domReadBossJobList(frameSrc: string, tabSel: string, rowSel: string, nameSel: string, statusSel: string): BossJobListRead {
  const frames = Array.from(document.querySelectorAll('iframe')).filter((el) => (el.getAttribute('src') ?? '').includes(frameSrc))
  const frame = frames.length === 1 ? frames[0]! : null
  const doc = frame ? frame.contentDocument : null
  if (!doc) return { frame: false, tabs: [], rows: [], total: null }
  const tabs = Array.from(doc.querySelectorAll(tabSel)).map((tab) => (tab.textContent ?? '').trim())
  const rows = Array.from(doc.querySelectorAll(rowSel)).map((row) => ({
    name: (row.querySelector(nameSel)?.textContent ?? '').trim(),
    status: (row.querySelector(statusSel)?.textContent ?? '').trim(),
  }))
  const footer = /共\s*(\d+)\s*个职位/u.exec(doc.body ? doc.body.innerText : '')
  return { frame: true, tabs, rows, total: footer ? Number(footer[1]) : null }
}

/** 快捷窗外壳(isolated):开着没有、关闭键在哪。 */
function domReadBossQuickChatShell(windowSel: string, closeSel: string): { open: boolean; closeCount: number; closeRect: DomRect4 } {
  const win = document.querySelector(windowSel)
  if (!win) return { open: false, closeCount: 0, closeRect: { x: 0, y: 0, w: 0, h: 0 } }
  const closers = Array.from(document.querySelectorAll(closeSel))
  const closer = closers.length === 1 ? closers[0]! : null
  const rect = closer ? closer.getBoundingClientRect() : null
  return { open: true, closeCount: closers.length, closeRect: rect ? { x: rect.x, y: rect.y, w: rect.width, h: rect.height } : { x: 0, y: 0, w: 0, h: 0 } }
}

/** 快捷窗发送前最后一道闸(isolated 那半):落点在唯一的发送钮上,且编辑器文本与文案规范化后相等。目标绑定那半在 MAIN 读 geek.uid。 */
function domBossQuickSendGate(
  buttonSelector: string, x: number, y: number, composerId: string, expectedText: string,
): { onTarget: boolean; found: string } {
  const normalize = (value: string): string =>
    value.normalize('NFC').replace(/ /gu, ' ').replace(/\s+/gu, ' ').trim()
  const buttons = Array.from(document.querySelectorAll(buttonSelector))
  const button = buttons.length === 1 ? buttons[0] : undefined
  const at = document.elementFromPoint(x, y)
  const onButton = !!button && !!at && (at === button || button.contains(at))
  const composer = document.getElementById(composerId)
  const composerText = composer ? (composer.textContent ?? '') : ''
  const textOk = !!composer && normalize(composerText) === normalize(expectedText)
  const problems: string[] = []
  if (!button) problems.push(`发送钮命中 ${buttons.length} 个`)
  else if (!onButton) problems.push(at ? `落点上是 ${at.tagName.toLowerCase()}「${(at.textContent ?? '').trim().slice(0, 8)}」` : '落点上什么都没有')
  if (!textOk) problems.push(composer ? `输入框内容与文案不同(${composerText.length} 字)` : '输入框不见了')
  const occluded = !!button && !onButton && !!at
  return { onTarget: onButton && textOk, found: problems.length ? problems.join(';') : '发送钮', ...(occluded ? { occluded } : {}) }
}

/**
 * 停靠/退让用的空白带(isolated):推荐页 iframe 左侧 56px 边距里没有任何控件(2026-09-07 真机:
 * 落点处是无类名的包裹 div),取其中一条竖带,上下各留 120px 避开 iframe 页头与视口底缘。
 */
function domReadBossParkSpot(frameSel: string): { found: boolean; rect: DomRect4 } {
  const frame = document.querySelector(frameSel)
  if (!frame) return { found: false, rect: { x: 0, y: 0, w: 0, h: 0 } }
  const fr = frame.getBoundingClientRect()
  const top = Math.max(fr.top + 120, 0)
  const bottom = Math.min(fr.bottom - 120, window.innerHeight - 20)
  if (!(bottom - top >= 60) || !(fr.width >= 80)) return { found: false, rect: { x: 0, y: 0, w: 0, h: 0 } }
  return { found: true, rect: { x: fr.left + 8, y: top, w: 40, h: bottom - top } }
}

/** 停靠落点是不是真的空白:顶层是 iframe 本身,iframe 内落点不在卡片、页头控件、筛选面板或任何可点元素上。 */
function domBossParkGate(frameSel: string, x: number, y: number): { onTarget: boolean; found: string } {
  const frame = document.querySelector(frameSel)
  const doc = frame && 'contentDocument' in frame ? (frame as HTMLIFrameElement).contentDocument : null
  if (!frame || !doc) return { onTarget: false, found: 'iframe 不在' }
  const topAt = document.elementFromPoint(x, y)
  if (topAt !== frame) return { onTarget: false, found: `顶层是 ${topAt ? topAt.tagName.toLowerCase() : '空'}` }
  const fr = frame.getBoundingClientRect()
  const inner = doc.elementFromPoint(x - fr.left - frame.clientLeft, y - fr.top - frame.clientTop)
  if (!inner) return { onTarget: false, found: 'iframe 内落点空' }
  const busy = inner.closest('li.card-item, .candidate-head, .filter-wrap, .filter-panel, button, a, input, [role="button"]')
  if (busy) return { onTarget: false, found: `落在 ${busy.tagName.toLowerCase()}.${String(busy.className).trim().split(/\s+/u)[0] ?? ''} 上,不是空白处` }
  return { onTarget: true, found: `空白 ${inner.tagName.toLowerCase()}` }
}

// ── 推荐页编排 helper ───────────────────────────────────────────────────────

type BossRecommendWindowReady = Extract<BossRecommendWindowRead, { status: 'ready' }>

async function readBossRecommendWindow(tabId: number): Promise<BossRecommendWindowRead> {
  return runInPage(BOSS_INJECT, tabId, mainReadBossRecommendWindow, [
    RECOMMEND_FRAME, RECOMMEND_SEL.listView, RECOMMEND_SEL.cardList, RECOMMEND_SEL.cardItem, RECOMMEND_SEL.cardInner,
    RECOMMEND_SEL.cardButton, RECOMMEND_SEL.jobItem, RECOMMEND_SEL.jobItemCurrentClass,
  ])
}

async function readBossRecommendTarget(tabId: number, geekId: number): Promise<BossRecommendTargetRead> {
  return runInPage(BOSS_INJECT, tabId, mainReadBossRecommendTarget, [
    RECOMMEND_FRAME, RECOMMEND_SEL.cardList, RECOMMEND_SEL.cardItem, RECOMMEND_SEL.cardInner, RECOMMEND_SEL.cardButton,
    RECOMMEND_SEL.jobItem, RECOMMEND_SEL.jobItemCurrentClass, geekId,
  ])
}

async function readBossFilterPanel(tabId: number): Promise<BossFilterPanelRead> {
  return runInPage(BOSS_DOM, tabId, domReadBossFilterPanel, [
    RECOMMEND_FRAME, RECOMMEND_SEL.filterPanel, RECOMMEND_SEL.filterBlock, RECOMMEND_SEL.filterVipBlockClass,
    RECOMMEND_SEL.filterGroup, RECOMMEND_SEL.filterGroupName, RECOMMEND_SEL.filterBox, RECOMMEND_SEL.filterOption,
    RECOMMEND_SEL.filterOptionActiveClass, RECOMMEND_SEL.filterOptionDefaultClass, RECOMMEND_SEL.vipMask, RECOMMEND_SEL.filterButton,
  ])
}

async function readBossQuickChat(tabId: number): Promise<BossQuickChatRead> {
  return runInPage(BOSS_INJECT, tabId, mainReadBossQuickChat, [QUICK_CHAT_SEL.window, QUICK_CHAT_SEL.messageList])
}

function bossRecommendWindowReady(read: BossRecommendWindowRead): read is BossRecommendWindowReady {
  return read.status === 'ready' && !read.loading && read.cards.length >= 1 && read.aligned && read.positionRef !== ''
}

function describeBossRecommendRead(read: BossRecommendWindowRead): string {
  if (read.status !== 'ready') return read.status
  return `loading=${read.loading} 卡=${read.cards.length}/${read.domCount} 对齐=${read.aligned} 职位=${read.positionRef ? '有' : '无'}`
}

function bossRecommendSignature(read: BossRecommendWindowReady): string {
  return `${read.positionRef}|${read.loading}|${read.cards.map((card) => card.geekId).join(',')}`
}

/** 推荐页就绪:iframe 与列表 vm 都在、不在加载、至少一张卡且 DOM 与 pageList 对齐、当前职位可读。 */
async function waitBossRecommendReady(tabId: number, ctx: PrimitiveContext, maxMs: number): Promise<BossRecommendWindowReady> {
  const settled = await pollUntil(ctx, () => readBossRecommendWindow(tabId), bossRecommendWindowReady, maxMs)
  if (!bossRecommendWindowReady(settled.value)) {
    throw new PlatformError('CTX_NOT_READY',
      `BOSS 推荐页 ${Math.round(maxMs / 1000)} 秒内未就绪(${describeBossRecommendRead(settled.value)})`, 'afterRecovery', 'pageBroken')
  }
  return settled.value
}

/** 就绪之上再要求签名(职位、加载态、卡片身份序列)连续 RECOMMEND_STABLE_MS 没变:列表刷新与翻页都是异步的。 */
async function waitBossRecommendStable(tabId: number, ctx: PrimitiveContext, maxMs: number): Promise<BossRecommendWindowReady> {
  const deadline = Date.now() + maxMs
  let last: BossRecommendWindowRead = await readBossRecommendWindow(tabId)
  let signature = bossRecommendWindowReady(last) ? bossRecommendSignature(last) : ''
  let since = Date.now()
  while (Date.now() < deadline) {
    if (bossRecommendWindowReady(last) && signature !== '' && Date.now() - since >= RECOMMEND_STABLE_MS) return last
    ctx.checkpoint()
    await sleep(READY_POLL_MS)
    last = await readBossRecommendWindow(tabId)
    const next = bossRecommendWindowReady(last) ? bossRecommendSignature(last) : ''
    if (next !== signature) { signature = next; since = Date.now() }
  }
  throw new PlatformError('CTX_NOT_READY',
    `BOSS 推荐列表 ${Math.round(maxMs / 1000)} 秒内未稳定(${describeBossRecommendRead(last)})`, 'afterRecovery', 'pageBroken')
}

/** 身份已核对且停在推荐页的标签页;不在推荐页就干净失败——批次内不导航(推荐页运行连续性)。 */
async function requireBossRecommendTab(fingerprint: string | undefined): Promise<chrome.tabs.Tab> {
  const tab = await verifiedBossTab(fingerprint)
  if (!isBossRecommendUrl(tab.url)) {
    throw new PlatformError('CTX_NOT_READY', '请停在 BOSS 推荐页(当前不是 /web/chat/recommend)', 'afterRecovery', 'pageAbsent')
  }
  return tab
}

/**
 * 把唯一的 BOSS 标签页导航到指定页并等它加载完。整页加载不是候选人可见动作;调用方只在批次之外用它
 * (读职位管理页、切到推荐页开批),批次内的原语一律 requireBossRecommendTab。导航代数一变身份缓存即失效,
 * 返回前重新核身份。
 */
async function ensureBossTabAt(
  tab: chrome.tabs.Tab, ctx: PrimitiveContext, fingerprint: string, url: string,
  isAt: (value: string | undefined) => boolean, what: string,
): Promise<chrome.tabs.Tab> {
  if (isAt(tab.url)) return tab
  const tabId = tab.id!
  await chrome.tabs.update(tabId, { url })
  const deadline = Date.now() + RECOMMEND_NAV_WAIT_MS
  let latest = await chrome.tabs.get(tabId)
  const arrived = (): boolean => latest.status === 'complete' && isAt(latest.url)
  while (!arrived() && Date.now() < deadline) {
    ctx.checkpoint()
    await sleep(READY_POLL_MS)
    latest = await chrome.tabs.get(tabId)
  }
  if (!arrived()) {
    throw new PlatformError('CTX_NOT_READY',
      `导航到${what}后 ${RECOMMEND_NAV_WAIT_MS / 1000} 秒内未加载完(status=${latest.status ?? '?'})`, 'afterRecovery', 'pageBroken')
  }
  // 标签页报 complete 时页面的 Vue 应用未必挂好,`user$` 这一刻常读不到(2026-09-07 首趟真机:导航到职位管理页后
  // 第一次读身份 identityUnverified,当日计划把第一个职位整个跳过;3 秒后第二条命令同一页就读到了)。
  // 身份读不到不是账号不对,是还没就绪:按条件等待封顶 20 秒重读;别的失败(账号不一致等)原样抛。
  const identityDeadline = Date.now() + READY_WAIT_MS
  for (;;) {
    try {
      return await verifiedBossTab(fingerprint)
    } catch (error) {
      const notReady = error instanceof PlatformError && error.code === 'CTX_NOT_READY' && error.reason === 'identityUnverified'
      if (!notReady || Date.now() >= identityDeadline) throw error
      ctx.checkpoint()
      await sleep(500)
    }
  }
}

/** 退让/停靠点:推荐页 iframe 左侧空白带;iframe 不在(沟通页)就没有,计划照旧不带退让。 */
async function bossRetreatPlan(tabId: number): Promise<RetreatPlan | undefined> {
  const spot = await runInPage(BOSS_DOM, tabId, domReadBossParkSpot, [RECOMMEND_FRAME])
  if (!spot.found) return undefined
  return { rect: spot.rect, hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domBossParkGate, [RECOMMEND_FRAME, x, y]) }
}

/**
 * 命令收尾停靠:把光标停回 iframe 左侧空白带,下一条命令从安全位置出发,不再从页头附近起步穿过头像
 * (2026-09-07 真机:「确定」→「筛选」入口那一程碰出头像 hover 弹层,盖住入口)。尽力而为,不影响结果。
 */
async function parkBossCursor(tabId: number, ctx: PrimitiveContext, why: string): Promise<void> {
  try {
    const retreat = await bossRetreatPlan(tabId)
    if (!retreat || !retreat.hitTest) return
    const gate = retreat.hitTest
    await paceBeforeClick()
    const probe = await runOsProbe(BOSS_INJECT, tabId, ctx, {
      label: '停靠空白处',
      rect: retreat.rect,
      action: 'land',
      hitTest: (x, y) => gate(x, y),
      observe: async (): Promise<ClickObservation> => ({ trusted: null, onTarget: null, eventDriftPx: null, after: '停靠' }),
    })
    if (probe.outcome !== 'landed') {
      reportHandLog('warn', 'cursorParkSkipped', `BOSS 停靠未完成(${why}):${probe.outcome} ${probe.detail ?? ''}`.slice(0, 400))
    }
  } catch (error) {
    if (isStopExecution(error)) throw error
    reportHandLog('warn', 'cursorParkSkipped', `BOSS 停靠异常(${why}):${describeError(error).slice(0, 200)}`)
  }
}

/** 按 selector(+index)的点击计划:定位、文本核对、命中测试、点后观测全走 domLocateBySelector 一套(支持 `A >>> B`)。 */
async function selectorClickPlan(
  tabId: number, selector: string, index: number, expectText: string | null, label: string, expectPrefix?: string,
): Promise<ClickPlan> {
  const located = await runInPage(BOSS_DOM, tabId, domLocateBySelector, [selector, index])
  if (located.status !== 'ok') {
    throw new PlatformError('ELEMENT_UNRESOLVED', `${label}定位失败(${located.status}):${located.detail}`, 'afterRecovery')
  }
  if (expectText !== null && located.text !== expectText) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `${label}文本不符:页面「${located.text.slice(0, 16)}」,期望「${expectText}」`, 'afterRecovery')
  }
  // 前缀核对给带计数后缀的控件:筛选入口有筛选生效时显示「筛选·1」(2026-09-05 真机),逐字相等会把它拒掉。
  if (expectPrefix !== undefined && !located.text.startsWith(expectPrefix)) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `${label}文本不符:页面「${located.text.slice(0, 16)}」,期望以「${expectPrefix}」开头`, 'afterRecovery')
  }
  const retreat = await bossRetreatPlan(tabId)
  return {
    label: `${label}(${located.signature})`,
    rect: located.clip,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestExpected, [selector, located.index, expectText, x, y]),
    observe: async (): Promise<ClickObservation> => {
      const after = await runInPage(BOSS_DOM, tabId, domLocateBySelector, [selector, located.index])
      return { trusted: null, onTarget: null, eventDriftPx: null,
        after: after.status === 'ok' ? `靶子仍在:${after.signature}` : `靶子已不在(${after.status})` }
    },
    ...(retreat === undefined ? {} : { retreat }),
  }
}

function recommendClickPlan(
  tabId: number, innerSelector: string, index: number, expectText: string | null, label: string, expectPrefix?: string,
): Promise<ClickPlan> {
  return selectorClickPlan(tabId, `${RECOMMEND_FRAME} >>> ${innerSelector}`, index, expectText, label, expectPrefix)
}

/** 滚 iframe 文档本身(推荐列表的滚动容器就是它,§十七):与 debug.osScroll 同一内核。 */
async function scrollBossRecommendDocument(
  tabId: number, ctx: PrimitiveContext, direction: 'up' | 'down', distancePx: number,
): Promise<OsScrollResult> {
  const selector = `${RECOMMEND_FRAME} >>> html`
  const located = await runInPage(BOSS_DOM, tabId, domLocateBySelector, [selector, -1])
  if (located.status !== 'ok') {
    throw new PlatformError('ELEMENT_UNRESOLVED', `推荐列表滚动容器定位失败(${located.status}):${located.detail}`, 'afterRecovery')
  }
  const retreat = await bossRetreatPlan(tabId)
  const target: ScrollTarget = {
    label: `推荐列表 ${located.signature}`,
    rect: located.clip,
    hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestIndexed, [selector, located.index, x, y]),
    readMetrics: async () => {
      const m = await runInPage(BOSS_DOM, tabId, domReadScrollMetrics, [selector, located.index])
      return m.found ? { scrollTop: m.scrollTop, scrollHeight: m.scrollHeight, clientHeight: m.clientHeight } : null
    },
    ...(retreat === undefined ? {} : { retreat }),
  }
  const result = await runOsScroll(BOSS_INJECT, tabId, ctx, target, direction, distancePx)
  if (result.outcome === 'handServiceUnavailable') {
    throw new PlatformError('CTX_NOT_READY', `手服务不可用,推荐列表未滚动(${result.detail ?? ''})`, 'afterRecovery', 'pageBroken')
  }
  if (result.outcome === 'refusedByGate') {
    throw new PlatformError('ELEMENT_UNRESOLVED', `推荐列表滚动被闸拒绝:${result.detail ?? ''}`, 'afterRecovery')
  }
  return result
}

/** 关快捷窗,尽力而为:它盖住卡片右侧整列按钮,不关下一张卡的「打招呼」点不到。关不掉只留痕,不影响本次结果。 */
async function closeBossQuickChatBestEffort(tabId: number, ctx: PrimitiveContext, why: string): Promise<boolean> {
  const readShell = (): Promise<{ open: boolean; closeCount: number; closeRect: DomRect4 }> =>
    runInPage(BOSS_DOM, tabId, domReadBossQuickChatShell, [QUICK_CHAT_SEL.window, QUICK_CHAT_SEL.close])
  try {
    const shell = await readShell()
    if (!shell.open) return true
    if (shell.closeCount !== 1) {
      reportHandLog('warn', 'quickChatCloseSkipped', `BOSS 快捷窗关闭键命中 ${shell.closeCount} 个,不点(${why})`)
      return false
    }
    await paceBeforeClick()
    const retreat = await bossRetreatPlan(tabId)
    await osClickOnce(tabId, ctx, {
      label: '快捷窗关闭键',
      rect: shell.closeRect,
      hitTest: (x, y) => runInPage(BOSS_DOM, tabId, domHitTestExpected, [QUICK_CHAT_SEL.close, 0, null, x, y]),
      observe: async (): Promise<ClickObservation> => {
        const after = await readShell()
        return { trusted: null, onTarget: null, eventDriftPx: null, after: `快捷窗 ${after.open ? '仍开着' : '已关闭'}` }
      },
      ...(retreat === undefined ? {} : { retreat }),
    }, '关快捷窗')
    const gone = await pollUntil(ctx, readShell, (shell) => !shell.open, CLEAR_WAIT_MS)
    if (!gone.satisfied) {
      reportHandLog('warn', 'quickChatCloseFailed', `BOSS 点了快捷窗关闭键但窗口仍在(${why})`)
      return false
    }
    return true
  } catch (error) {
    if (isStopExecution(error)) throw error
    reportHandLog('warn', 'quickChatCloseFailed', `BOSS 快捷窗关闭异常(${why}):${describeError(error).slice(0, 200)}`)
    return false
  }
}

// ── nav.ensureSurface(沟通页) ──────────────────────────────────────────────────

/**
 * 把唯一的 BOSS 标签页带到沟通页并等列表页签渲染。脑侧巡检在 readList 报 pageAbsent 时走 surfaceRecovery
 * 调它(2026-09-07 首趟真机:采集批次收口后标签页停在推荐页,巡检每两分钟失败一次)。没有 BOSS 标签页就新开一个。
 * 登录态:身份核对过就是 in——BOSS 站点不感知掉登录(bossSite),永不报 out。
 */
async function ensureBossSurface(
  args: NavEnsureSurfaceArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<NavEnsureSurfaceData> {
  if (args.surface !== 'im') throw new PlatformError('TARGET_NOT_FOUND', '当前手不支持该页面 surface', 'no')
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  ctx.checkpoint()
  let tab = await bossTab()
  let createdTab = false
  if (!tab || tab.id === undefined) {
    tab = await chrome.tabs.create({ url: BOSS_CHAT_URL, active: false })
    createdTab = true
  }
  if (tab.id === undefined) throw new PlatformError('CTX_NOT_READY', 'BOSS 标签页缺少 id', 'afterRecovery', 'pageBroken')
  const isIm = (url: string | undefined): boolean => !!url && bossSite.pageKind(url) === 'im'
  const ready = await ensureBossTabAt(tab, ctx, fingerprint, BOSS_CHAT_URL, isIm, '沟通页')
  const tabId = ready.id!
  const settled = await pollUntil(ctx, () => readListState(tabId), (state) => state.labelTabs.length > 0)
  await dismissBossOverlaysBestEffort(ready, ctx)
  await verifiedBossTab(fingerprint)
  ctx.progress(settled.satisfied ? 'BOSS 沟通页已就绪' : 'BOSS 沟通页列表页签未就绪', 100)
  return { createdTab, loginState: 'in', ready: settled.satisfied }
}

// ── job.readPublishedList ─────────────────────────────────────────────────────

async function readBossPublishedJobs(ctx: PrimitiveContext, fingerprint: string | undefined): Promise<JobReadPublishedListData> {
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  ctx.checkpoint()
  const tab = await ensureBossTabAt(await verifiedBossTab(fingerprint), ctx, fingerprint, BOSS_JOB_LIST_URL, isBossJobListUrl, '职位管理页')
  const tabId = tab.id!
  ctx.progress('核对 BOSS 职位管理页与登录身份', 15)
  const read = (): Promise<BossJobListRead> => runInPage(BOSS_DOM, tabId, domReadBossJobList,
    [JOB_LIST_SEL.frameSrc, JOB_LIST_SEL.tab, JOB_LIST_SEL.row, JOB_LIST_SEL.rowName, JOB_LIST_SEL.rowStatus])
  // 读全的判据:页脚「共 N 个职位」读到且等于行数。页脚没读到不放行(出口审查 O3:分批渲染时先看见几行、
  // 页脚还没挂,放行就是部分结果);分页形态未见(测试账号只有一个职位),超一页会在这里如实停,不截断。
  const settled = await pollUntil(ctx, read,
    (list) => list.frame && list.tabs.length >= 1 && list.total !== null && list.total === list.rows.length,
    RECOMMEND_NAV_WAIT_MS)
  const list = settled.value
  if (!settled.satisfied) {
    throw new PlatformError('ELEMENT_UNRESOLVED',
      `BOSS 职位管理页 ${RECOMMEND_NAV_WAIT_MS / 1000} 秒内未完整渲染(iframe=${list.frame} 页签=${list.tabs.length} 行=${list.rows.length} 页脚共=${list.total ?? '未读到'})`,
      'afterRecovery')
  }
  const projected = bossJobListSections(list.tabs, list.rows)
  if (!projected.ok) throw new PlatformError('ELEMENT_UNRESOLVED', projected.reason, 'afterRecovery')
  if (projected.extraLabels.length > 0) {
    reportHandLog('warn', 'jobListStatusUnknown', `BOSS 职位管理页有行的状态文案不在页签里,按原样另起分区:${projected.extraLabels.join('/')}`)
  }
  if (projected.sections.length > 16) throw new PlatformError('ELEMENT_UNRESOLVED', `职位分区数量 ${projected.sections.length} 超出契约上限`, 'afterRecovery')
  if (projected.sections.reduce((count, section) => count + section.names.length, 0) > 200) {
    throw new PlatformError('PAYLOAD_LIMIT', '平台职位数量超过当前契约上限', 'manualOnly')
  }
  const data: JobReadPublishedListData = { sections: projected.sections, observedAt: Date.now() }
  if (validatePrimitiveData(PrimitiveName.JobReadPublishedList, 1, data).length !== 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '职位分区清单不符合当前契约', 'afterRecovery')
  }
  await verifiedBossTab(fingerprint)
  ctx.progress('BOSS 职位分区清单读取完成', 100)
  return data
}

// ── candidate.selectSourcingPosition ─────────────────────────────────────────

/**
 * 单职位直通:导航到推荐页、等列表就绪、按标题在职位选择器里唯一匹配、核当前项就是它、核全窗卡片的
 * encryptJobId 都等于它。多职位账号的切换(展开 .ui-dropmenu 点 .job-item)后置未验,目标不是当前项就干净失败。
 */
async function selectBossSourcingPosition(
  args: CandidateSelectSourcingPositionArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<CandidateSelectSourcingPositionData> {
  if (validatePrimitiveArgs(PrimitiveName.CandidateSelectSourcingPosition, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '选择职位参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  ctx.checkpoint()
  const tab = await ensureBossTabAt(await verifiedBossTab(fingerprint), ctx, fingerprint, BOSS_RECOMMEND_URL, isBossRecommendUrl, '推荐页')
  const tabId = tab.id!
  ctx.progress('核对 BOSS 推荐页与登录身份', 10)
  const ready = await waitBossRecommendReady(tabId, ctx, RECOMMEND_NAV_WAIT_MS)
  const match = matchBossJobItem(ready.jobItems, args.positionTitle)
  if (match.status !== 'ok') {
    throw new PlatformError('ELEMENT_UNRESOLVED',
      `职位选择器里「${args.positionTitle}」命中 ${match.count} 项(候选:${ready.jobItems.map((item) => bossJobItemName(item.text)).join('/') || '无'})`,
      'afterRecovery')
  }
  if (!match.current) {
    throw new PlatformError('ELEMENT_UNRESOLVED',
      `「${match.name}」不是当前选中职位(当前「${bossJobItemName(ready.positionText)}」);BOSS 多职位切换是第二刀后置项,本轮不点`,
      'afterRecovery')
  }
  if (!match.value || match.value !== ready.positionRef) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '当前职位项读不到稳定 value', 'afterRecovery')
  }
  const strangers = ready.cards.filter((card) => card.encryptJobId !== match.value).length
  if (strangers > 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `${strangers} 张卡片的职位与选择器当前项不一致,推荐页未稳定`, 'afterRecovery')
  }
  const data: CandidateSelectSourcingPositionData = { positionRef: match.value, positionTitle: match.name, observedAt: Date.now() }
  if (validatePrimitiveData(PrimitiveName.CandidateSelectSourcingPosition, 1, data).length !== 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '职位选择结果不符合当前契约', 'afterRecovery')
  }
  await verifiedBossTab(fingerprint)
  ctx.progress('BOSS 当前职位已核对', 100)
  return data
}

// ── candidate.applySourcingFilters ───────────────────────────────────────────

/** 当前职位与 args 一致(positionRef 逐字、职位名规范化后相等)。 */
async function assertBossSourcingPosition(
  tabId: number, ctx: PrimitiveContext, positionRef: string, positionTitle: string, what: string, maxMs = READY_WAIT_MS,
): Promise<BossRecommendWindowReady> {
  const read = await waitBossRecommendReady(tabId, ctx, maxMs)
  if (read.positionRef !== positionRef) {
    throw new PlatformError('GUARD_FAILED', `${what}:当前职位与命令不一致`, 'afterRecovery')
  }
  if (bossJobItemName(read.positionText) !== normalizeBossMessageText(positionTitle)) {
    throw new PlatformError('GUARD_FAILED', `${what}:当前职位名「${bossJobItemName(read.positionText)}」与命令「${positionTitle}」不一致`, 'afterRecovery')
  }
  return read
}

/**
 * 契约流程「逐项差异覆盖 → 完整回读 → 唯一点击确定 → 等待推荐窗口连续稳定 → 重新打开筛选 → 同一读取器回读 → 点击取消」
 * 在 BOSS 上的形态(2026-09-05 真机):点「筛选」展开面板;点选项即时切 active(「不限」自动退选);点「确定」面板
 * 自行收起、列表整表刷新;BOSS 没有「取消」键,收起 = 再点一次「筛选」。VIP 锁定组只回读不点。
 */
async function applyBossSourcingFilters(
  args: CandidateApplySourcingFiltersArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<CandidateApplySourcingFiltersData> {
  if (validatePrimitiveArgs(PrimitiveName.CandidateApplySourcingFilters, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '筛选参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  const planned = planBossSourcingFilters(args.filters)
  if (!planned.ok) throw new PlatformError('GUARD_FAILED', planned.reason, 'afterRecovery')
  const tab = await requireBossRecommendTab(fingerprint)
  const tabId = tab.id!
  ctx.checkpoint()
  await assertBossSourcingPosition(tabId, ctx, args.positionRef, args.positionTitle, '筛选前')
  ctx.progress('核对 BOSS 推荐页当前职位', 10)
  const trace: string[] = []
  // 入口文案是「筛选」或带计数的「筛选·N」,按前缀认。
  const filterLabelPlan = (label: string): Promise<ClickPlan> => recommendClickPlan(tabId, RECOMMEND_SEL.filterLabel, -1, null, label, FILTER_LABEL)
  const openPanel = async (what: string): Promise<BossFilterPanelRead> => {
    const before = await readBossFilterPanel(tabId)
    if (!before.frame) throw new PlatformError('CTX_NOT_READY', `${what}:推荐页 iframe 不在`, 'afterRecovery', 'pageBroken')
    if (before.panel) return before
    await paceBeforeClick()
    await osClickOnce(tabId, ctx, await filterLabelPlan('筛选入口'), what)
    const opened = await pollUntil(ctx, () => readBossFilterPanel(tabId), (read) => read.panel)
    if (!opened.satisfied) throw new PlatformError('ELEMENT_UNRESOLVED', `${what}后 ${READY_WAIT_MS / 1000} 秒内筛选面板未展开`, 'afterRecovery')
    trace.push(what)
    return opened.value
  }
  const closePanel = async (what: string): Promise<void> => {
    const before = await readBossFilterPanel(tabId)
    if (!before.panel) return
    await paceBeforeClick()
    await osClickOnce(tabId, ctx, await filterLabelPlan('筛选入口'), what)
    const closed = await pollUntil(ctx, () => readBossFilterPanel(tabId), (read) => !read.panel)
    if (!closed.satisfied) throw new PlatformError('ELEMENT_UNRESOLVED', `${what}后筛选面板仍展开着,未收口`, 'afterRecovery')
    trace.push(what)
  }
  let panel = await openPanel('展开筛选')
  const clicks = bossFilterClicks(panel, planned.plan)
  if (!clicks.ok) throw new PlatformError('ELEMENT_UNRESOLVED', clicks.reason, 'afterRecovery')
  ctx.progress(`筛选面板已展开,${clicks.clicks.length} 项待改`, 25)
  for (const click of clicks.clicks) {
    ctx.checkpoint()
    await paceBeforeClick()
    const inner = `${RECOMMEND_SEL.filterPanel} .filters-wrap:not(.${RECOMMEND_SEL.filterVipBlockClass}) ${RECOMMEND_SEL.filterBox}.${click.boxKey} ${RECOMMEND_SEL.filterOption}`
    await osClickOnce(tabId, ctx, await recommendClickPlan(tabId, inner, click.index, click.text, `筛选项「${click.text}」`), `点筛选项「${click.text}」`)
    const settled = await pollUntil(ctx, () => readBossFilterPanel(tabId), (read) => {
      const group = read.groups.find((g) => !g.vip && g.boxKey === click.boxKey)
      const option = group?.options[click.index]
      return !!option && option.text === click.text && option.active === click.expectActive
    }, CLEAR_WAIT_MS)
    if (!settled.satisfied) {
      throw new PlatformError('ELEMENT_UNRESOLVED', `点了筛选项「${click.text}」但选中态未变成${click.expectActive ? '选中' : '未选'}`, 'afterRecovery')
    }
    trace.push(`${click.expectActive ? '选' : '退'}「${click.text}」`)
  }
  panel = await readBossFilterPanel(tabId)
  const first = projectBossSourcingFilters(panel, args.filters)
  if (!first.ok) throw new PlatformError('ELEMENT_UNRESOLVED', `覆盖后回读与目标不一致:${first.reason}`, 'afterRecovery')
  const confirmMatches = panel.buttons.map((text, index) => ({ text, index })).filter((b) => b.text === FILTER_CONFIRM)
  if (confirmMatches.length !== 1) {
    throw new PlatformError('ELEMENT_UNRESOLVED', `筛选面板「${FILTER_CONFIRM}」键命中 ${confirmMatches.length} 个(按钮:${panel.buttons.join('/')})`, 'afterRecovery')
  }
  ctx.checkpoint()
  await paceBeforeClick()
  await verifiedBossTab(fingerprint)
  await osClickOnce(tabId, ctx, await recommendClickPlan(tabId, RECOMMEND_SEL.filterButton, confirmMatches[0]!.index, FILTER_CONFIRM, '筛选确定键'), '点筛选确定')
  trace.push('确定')
  const collapsed = await pollUntil(ctx, () => readBossFilterPanel(tabId), (read) => !read.panel)
  if (!collapsed.satisfied) throw new PlatformError('ELEMENT_UNRESOLVED', '点确定后筛选面板未收起', 'afterRecovery')
  ctx.progress('筛选已提交,等待推荐列表刷新稳定', 55)
  await waitBossRecommendStable(tabId, ctx, RECOMMEND_NAV_WAIT_MS)
  await assertBossSourcingPosition(tabId, ctx, args.positionRef, args.positionTitle, '确定后')
  panel = await openPanel('重开筛选回读')
  const second = projectBossSourcingFilters(panel, args.filters)
  if (!second.ok) {
    await closePanel('回读不一致后收起')
    throw new PlatformError('ELEMENT_UNRESOLVED', `确定后重开回读与目标不一致:${second.reason}`, 'afterRecovery')
  }
  await closePanel('回读后收起')
  await parkBossCursor(tabId, ctx, '筛选收起后')
  const final = await assertBossSourcingPosition(tabId, ctx, args.positionRef, args.positionTitle, '收起后')
  const data: CandidateApplySourcingFiltersData = {
    positionRef: args.positionRef,
    positionTitle: bossJobItemName(final.positionText),
    filters: second.filters,
    observedAt: Date.now(),
  }
  if (validatePrimitiveData(PrimitiveName.CandidateApplySourcingFilters, 1, data).length !== 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '筛选结果不符合当前契约', 'afterRecovery')
  }
  await verifiedBossTab(fingerprint)
  console.info('[RecruitHelper] boss_apply_sourcing_filters', trace.join(' | '))
  ctx.progress('BOSS 筛选已生效并回读一致', 100)
  return data
}

// ── candidate.readSourcingWindow ─────────────────────────────────────────────

function firstVisibleCardIndex(read: BossRecommendWindowReady): number {
  return read.cards.findIndex((card) => card.visible)
}

async function readBossSourcingWindow(
  args: CandidateReadSourcingWindowArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<CandidateReadSourcingWindowData> {
  if (validatePrimitiveArgs(PrimitiveName.CandidateReadSourcingWindow, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '窗口读取参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  const tab = await requireBossRecommendTab(fingerprint)
  const tabId = tab.id!
  ctx.checkpoint()
  const before = await waitBossRecommendStable(tabId, ctx, READY_WAIT_MS)
  ctx.progress('推荐窗口已就绪', 20)
  const trace: string[] = []
  if (args.move === 'reset' && before.scrollTop > 0) {
    await paceBeforeClick()
    const res = await scrollBossRecommendDocument(tabId, ctx, 'up', before.scrollTop + 200)
    trace.push(`回顶 ${res.outcome} ${res.scrollTopBefore}→${res.scrollTopAfter}`)
  } else if (args.move === 'next') {
    const room = before.scrollHeight - before.scrollTop - before.clientHeight
    if (room > 1) {
      await paceBeforeClick()
      // 至多推进一个可见窗口:一屏高;滚到底会触发平台自动加载下一页(+15),由稳定等待吸收。
      const res = await scrollBossRecommendDocument(tabId, ctx, 'down', Math.max(1, Math.min(before.clientHeight, room)))
      trace.push(`下翻 ${res.outcome} ${res.scrollTopBefore}→${res.scrollTopAfter}`)
    } else {
      trace.push(`已在列表底部(finished=${before.finished})`)
    }
  }
  const after = await waitBossRecommendStable(tabId, ctx, READY_WAIT_MS)
  if (after.positionRef !== before.positionRef) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '窗口动作前后当前职位发生变化', 'afterRecovery')
  }
  const visible = after.cards.filter((card) => card.visible)
  if (visible.length === 0) throw new PlatformError('ELEMENT_UNRESOLVED', '推荐列表视口内没有卡片', 'afterRecovery')
  const refs: string[] = []
  for (const card of visible.slice(0, LIST_WINDOW_MAX)) {
    if (!(card.geekId > 0) || !Number.isSafeInteger(card.geekId)) {
      throw new PlatformError('ELEMENT_UNRESOLVED', '有卡片读不到稳定候选人身份(geekId)', 'afterRecovery')
    }
    if (card.encryptJobId !== after.positionRef) {
      throw new PlatformError('ELEMENT_UNRESOLVED', '有卡片的职位与当前职位不一致', 'afterRecovery')
    }
    const ref = String(card.geekId)
    if (refs.includes(ref)) throw new PlatformError('ELEMENT_UNRESOLVED', '视口内候选人身份重复', 'afterRecovery')
    refs.push(ref)
  }
  const moved = args.move === 'current'
    ? false
    : after.scrollTop !== before.scrollTop || firstVisibleCardIndex(after) !== firstVisibleCardIndex(before)
  const title = bossJobItemName(after.positionText)
  const data: CandidateReadSourcingWindowData = {
    positionRef: after.positionRef,
    positionTitle: title && title.length <= 256 ? title : null,
    platformUserRefs: refs,
    moved,
    observedAt: Date.now(),
  }
  if (validatePrimitiveData(PrimitiveName.CandidateReadSourcingWindow, 1, data).length !== 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '窗口读取结果不符合当前契约', 'afterRecovery')
  }
  await verifiedBossTab(fingerprint)
  if (trace.length > 0) console.info('[RecruitHelper] boss_read_sourcing_window', trace.join(' | '))
  ctx.progress(`推荐窗口 ${refs.length} 人,moved=${moved}`, 100)
  return data
}

// ── candidate.readSourcingTargetResume ───────────────────────────────────────

/**
 * 只读当前 pageList 里唯一匹配的目标,从卡片 geekInfo 投影五分区;不开详情、不滚动、不点击(出口 §四 第 6 件),
 * 契约里「关闭详情、确认弹框消失」在 BOSS 上是空操作。目标不在/身份重复/摘要全空按 manualOnly 收:脑侧
 * skipsUnreadableSourcingTarget 据此跳过该候选人、批次照常;页面级不就绪才 afterRecovery(批次停)。
 */
async function readBossSourcingTargetResume(
  args: CandidateReadSourcingTargetResumeArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<CandidateReadSourcingResumeData> {
  if (validatePrimitiveArgs(PrimitiveName.CandidateReadSourcingTargetResume, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '目标简历读取参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  const geekId = parseBossGeekId(args.platformUserRef)
  if (geekId === null) throw new PlatformError('GUARD_FAILED', '候选人引用不是本平台形态', 'afterRecovery')
  const tab = await requireBossRecommendTab(fingerprint)
  const tabId = tab.id!
  ctx.checkpoint()
  // 「不在」要连续三读(≥500ms)都不在才算:刚翻页的卡 vm 晚几百毫秒才挂上,data-geekid 对不上时会瞬时读成 absent,
  // 一读即判会把这个人永久跳过(脑侧 unreadable 集合;出口审查 R2)。
  let misses = 0
  const settled = await pollUntil(ctx, () => readBossRecommendTarget(tabId, geekId), (read) => {
    if (read.status === 'ready') return true
    if (read.status === 'absent' || read.status === 'duplicated') { misses += 1; return misses >= 3 }
    misses = 0
    return false
  })
  const read = settled.value
  if (read.status === 'no_frame' || read.status === 'no_list') {
    throw new PlatformError('CTX_NOT_READY', `推荐页未就绪(${read.status})`, 'afterRecovery', 'pageBroken')
  }
  if (read.status === 'absent') {
    throw new PlatformError('ELEMENT_UNRESOLVED', '目标不在当前推荐列表里', 'manualOnly')
  }
  if (read.status === 'duplicated') {
    throw new PlatformError('ELEMENT_UNRESOLVED', `目标在推荐列表里出现 ${read.count} 次,身份不唯一`, 'manualOnly')
  }
  if (read.card.encryptJobId !== args.positionRef || read.positionRef !== args.positionRef) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '目标卡片的职位与命令职位不一致', 'afterRecovery')
  }
  const title = bossJobItemName(read.positionText)
  const data = projectBossSourcingResume(read.card, args.positionRef, title || null, Date.now())
  if (!data.displayName && !data.workExperiences && !data.education && !data.selfEvaluation) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '目标卡片摘要全空,不返回空简历冒充读到', 'manualOnly')
  }
  if (jsonBytes(data) > RESULT_DATA_BUDGET) throw new PlatformError('PAYLOAD_LIMIT', '简历摘要超过内联载荷上限', 'manualOnly')
  if (validatePrimitiveData(PrimitiveName.CandidateReadSourcingTargetResume, 1, data).length !== 0) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '简历摘要不符合当前契约', 'manualOnly')
  }
  await verifiedBossTab(fingerprint)
  ctx.progress('目标卡片摘要读取完成', 100)
  return data
}

// ── chat.sendGreeting(方案一:首击 → 继续沟通 → 快捷窗打字 → 发送 → 正证 → 关窗) ──

/**
 * 首击已确认之后的一切失败:关系已建立、正文未发出,如实报 possible 交脑验证读(出口 §二 部分失败形态)。
 * 原因同时走手侧日志:脑收到 possible 后由验证读补记 ok,result 里的原文随之被覆盖,账本只剩「result.sideEffect=possible」
 * (2026-09-07 第四趟真机:两条招呼正文没发,原因得靠离线重放排版器才找回来——排版器拒了「」)。留痕不能只靠 result。
 */
function greetingAfterFirstClick(message: string): PlatformError {
  // 手侧日志进脑侧普通日志:引号里的上屏原文(招呼正文/候选人称呼)不进普通日志,只留长度与原因;
  // 原文留在 result 里(命令审计快照,48 小时)。
  const redacted = message.replace(/「[^」]*」/gu, '「…」')
  reportHandLog('warn', 'greetingTextNotSent', `BOSS 招呼:关系已建立,正文未发出——${redacted}`.slice(0, 600))
  return new PlatformError('POSTCONDITION_UNCONFIRMED', `关系已建立,正文未发出:${message}`, 'manualOnly', undefined, 'possible')
}

/** 首击确认之后的整段:sideEffect=none 的平台失败一律升成 possible(世界已脏);possible 与 StopExecution 原样透传。 */
async function afterFirstClick<T>(run: () => Promise<T>): Promise<T> {
  try {
    return await run()
  } catch (error) {
    if (error instanceof PlatformError && error.sideEffect === 'none') throw greetingAfterFirstClick(error.message)
    throw error
  }
}

/**
 * 目标卡的「打招呼」/「继续沟通」按钮:selector 直接绑 encryptGeekId,列表重排也不会点错人。
 * 按钮在 `.operate-side` 里,与 `.card-inner` 是同一张 `li.card-item` 下的兄弟、不是它的后代(2026-09-07 第三趟真机:
 * 三次首击都在定位这一步报「选择器没有命中任何元素」,候选人零触碰)——所以从 li 往下找,用 `:has()` 把身份绑在 li 上。
 */
export function greetButtonSelector(encryptGeekId: string, buttonSel: string): string {
  return `${RECOMMEND_SEL.cardItem}:has(${RECOMMEND_SEL.cardInner}[data-geekid="${encryptGeekId}"]) ${buttonSel}`
}

/**
 * 把目标卡滚进视口(至多两次,每次一屏内):脑在采集与发招呼之间可能已把窗口推进到别处。
 * 只在按钮定位为 offscreen 时才滚;滚完仍看不见就干净失败,不猜。
 */
async function bringBossCardIntoView(tabId: number, ctx: PrimitiveContext, encryptGeekId: string, geekId: number): Promise<void> {
  for (let attempt = 0; attempt < 2; attempt += 1) {
    const located = await runInPage(BOSS_DOM, tabId, domLocateBySelector, [`${RECOMMEND_FRAME} >>> ${greetButtonSelector(encryptGeekId, RECOMMEND_SEL.greetButton)}`, -1])
    if (located.status !== 'offscreen') return
    const target = await readBossRecommendTarget(tabId, geekId)
    const window = await readBossRecommendWindow(tabId)
    if (target.status !== 'ready' || window.status !== 'ready') return
    const index = window.cards.findIndex((card) => card.geekId === geekId)
    if (index < 0) return
    // 用卡片在 pageList 里的序号估方向:前面的卡全在上方。滚一屏,由定位闸再判。
    const firstVisible = firstVisibleCardIndex(window)
    const direction: 'up' | 'down' = firstVisible >= 0 && index < firstVisible ? 'up' : 'down'
    await paceBeforeClick()
    const res = await scrollBossRecommendDocument(tabId, ctx, direction, Math.max(120, window.clientHeight))
    if (res.outcome === 'edge' || res.outcome === 'stuck') return
    await waitBossRecommendStable(tabId, ctx, READY_WAIT_MS)
  }
}

async function sendBossGreeting(
  args: ChatSendGreetingArgs, guards: ChatSendGreetingGuards, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<ChatSendGreetingData> {
  if (validatePrimitiveArgs(PrimitiveName.ChatSendGreeting, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '招呼参数不符合当前契约', 'afterRecovery')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'afterRecovery')
  if (guards.expectUnestablished !== true) throw new PlatformError('GUARD_FAILED', '招呼命令缺少未建联条件写闸', 'afterRecovery')
  const geekId = parseBossGeekId(args.platformUserRef)
  if (geekId === null) throw new PlatformError('GUARD_FAILED', '候选人引用不是本平台形态', 'afterRecovery')
  const normalizedText = normalizeBossMessageText(args.text)
  if (!normalizedText) throw new PlatformError('GUARD_FAILED', '规范化后的招呼为空,拒绝发送', 'afterRecovery')
  // 与脑侧 SendFingerprint = HashText(GreetingText) 同配方(NFC、空白折叠、trim 后 sha256)。
  const contentHash = await sha256Hex(normalizedText)
  const tab = await requireBossRecommendTab(fingerprint)
  const tabId = tab.id!
  ctx.checkpoint()
  const trace: string[] = []
  // 快捷窗若开着(上一位候选人关窗失败)先关:它盖住卡片右侧整列按钮。
  if (!(await closeBossQuickChatBestEffort(tabId, ctx, '首击前清场'))) {
    throw new PlatformError('ELEMENT_UNRESOLVED', '首击前快捷窗关不掉,它会盖住「打招呼」', 'afterRecovery')
  }
  // 目标绑定 + 关系未建立(greeting evaluator;点前最后一刻再调同一读法)。
  const evaluate = async (what: string): Promise<Extract<BossRecommendTargetRead, { status: 'ready' }>> => {
    const read = await readBossRecommendTarget(tabId, geekId)
    if (read.status === 'no_frame' || read.status === 'no_list') {
      throw new PlatformError('CTX_NOT_READY', `${what}:推荐页未就绪(${read.status})`, 'afterRecovery', 'pageBroken')
    }
    if (read.status === 'absent') throw new PlatformError('TARGET_NOT_FOUND', `${what}:目标不在当前推荐列表里`, 'afterRecovery')
    if (read.status === 'duplicated') throw new PlatformError('ELEMENT_UNRESOLVED', `${what}:目标在列表里出现 ${read.count} 次`, 'afterRecovery')
    if (read.card.encryptJobId !== args.positionRef || read.positionRef !== args.positionRef) {
      throw new PlatformError('GUARD_FAILED', `${what}:目标卡片的职位与命令职位不一致`, 'afterRecovery')
    }
    return read
  }
  const first = await evaluate('首击前')
  const state = bossCardContactState(first.card)
  if (state !== 'unestablished') {
    throw new PlatformError('GUARD_FAILED', `目标关系态不是未建立(isFriend=${first.card.isFriend ?? 'null'} 按钮「${first.card.buttonText}」),不打招呼`, 'afterRecovery')
  }
  const encryptGeekId = first.card.encryptGeekId
  const geekSource = first.card.geekSource
  const conversationRef = bossConversationRef(geekId, geekSource)
  await bringBossCardIntoView(tabId, ctx, encryptGeekId, geekId)
  const greetPlan = await recommendClickPlan(tabId, greetButtonSelector(encryptGeekId, RECOMMEND_SEL.greetButton), -1, GREET_TEXT, '打招呼钮')
  ctx.progress('目标卡片已绑定,准备首击', 20)
  // 点前最后一刻:同一 evaluator 再读一次,身份、职位、关系态任一变化都在不可逆动作前失败。
  ctx.checkpoint()
  await paceBeforeClick()
  await verifiedBossTab(fingerprint)
  const again = await evaluate('点击前')
  if (bossCardContactState(again.card) !== 'unestablished') {
    throw new PlatformError('GUARD_FAILED', '点击前读到关系已不是未建立,已取消', 'afterRecovery')
  }
  if (Date.now() > ctx.irreversibleNotAfterMs) {
    throw new PlatformError('CTX_LOST_DURING_EXEC', '不可逆动作窗口已过,未点击', 'afterRecovery')
  }
  // 证词只能写一次(dispatcher 拒绝同一命令二次 beforeSideEffect):整条命令是一次招呼动作,attempting 紧贴首击。
  await ctx.beforeSideEffect()
  const firstProbe = await runOsProbe(BOSS_INJECT, tabId, ctx, greetPlan)
  if (firstProbe.outcome !== 'clicked') {
    throw new PlatformError(
      firstProbe.outcome === 'handServiceUnavailable' ? 'CTX_NOT_READY' : 'ELEMENT_UNRESOLVED',
      `打招呼钮未点击:${firstProbe.detail ?? firstProbe.outcome}`, 'afterRecovery')
  }
  trace.push(`首击 ${firstProbe.detail ?? ''}`)
  // 首击正证(判据表第一行):同卡 isFriend 0→1,或按钮变「继续沟通」。数秒内没翻就是未确认,不进快捷窗、不打字。
  const established = await pollUntil(ctx, () => readBossRecommendTarget(tabId, geekId),
    (read) => read.status === 'ready' && bossCardContactState(read.card) === 'established')
  if (!(established.value.status === 'ready' && bossCardContactState(established.value.card) === 'established')) {
    const seen = established.value.status === 'ready'
      ? `isFriend=${established.value.card.isFriend ?? 'null'} 按钮「${established.value.card.buttonText}」`
      : established.value.status
    throw new PlatformError('POSTCONDITION_UNCONFIRMED',
      `只点击了一次打招呼,但 ${READY_WAIT_MS / 1000} 秒内未见关系建立(${seen};${trace.join(' | ')})`,
      'manualOnly', undefined, 'possible')
  }
  trace.push('关系已建立')
  // 从这里起世界已脏:首击确认之后的任何失败——包括 runInPage 注入失败、身份复核失败这类默认 sideEffect=none 的平台错误——
  // 都要如实升成 possible 交验证读,不能让脑记成「没发」(出口审查 O1)。
  return afterFirstClick(async (): Promise<ChatSendGreetingData> => {
    ctx.progress('首击已确认关系建立,打开快捷窗', 45)
    // 第二步:点「继续沟通」弹快捷窗。从这里起任何失败都是「关系已建立,正文未发出」。
    let continuePlan: ClickPlan
    try {
      await paceBeforeClick()
      continuePlan = await recommendClickPlan(tabId, greetButtonSelector(encryptGeekId, RECOMMEND_SEL.continueButton), -1, CONTINUE_TEXT, '继续沟通钮')
    } catch (error) {
      if (isStopExecution(error)) throw error
      throw greetingAfterFirstClick(`继续沟通钮定位失败:${describeError(error).slice(0, 200)}`)
    }
    const continueProbe = await runOsProbe(BOSS_INJECT, tabId, ctx, continuePlan)
    if (continueProbe.outcome !== 'clicked') throw greetingAfterFirstClick(`继续沟通钮未点击:${continueProbe.detail ?? continueProbe.outcome}`)
    trace.push('点了继续沟通')
    const chatReady = await pollUntil(ctx, () => readBossQuickChat(tabId),
      (chat) => chat.status === 'ready' && chat.uid === geekId && chat.friendSource === geekSource)
    const chat = chatReady.value
    if (!(chat.status === 'ready' && chat.uid === geekId && chat.friendSource === geekSource)) {
      const seen = chat.status === 'ready' ? `快捷窗绑定的是别人(uid 不同)` : `快捷窗 ${chat.status}`
      throw greetingAfterFirstClick(`${READY_WAIT_MS / 1000} 秒内快捷窗未绑定目标(${seen})`)
    }
    const baselineMids = new Set(chat.rows.map((row) => row.mid))
    trace.push(`快捷窗已绑定目标,基线 ${chat.rows.length} 行`)
    // 真人不会窗一弹出就敲键:先停 3~6 秒(有界随机)再去点输入框。停顿两侧各一个 checkpoint,停止信号照常生效。
    ctx.checkpoint()
    const readPause = sampleQuickChatReadPause()
    await sleep(readPause)
    ctx.checkpoint()
    trace.push(`看了 ${(readPause / 1000).toFixed(1)} 秒`)
    // 打字:与 sendBossMessage 同一条路(焦点 → 前台闸 → 清空 → 排版 → 播放 → 回读逐字相等)。
    const composerId = QUICK_CHAT_SEL.composerId
    const readComposer = (): Promise<ReturnType<typeof mainReadComposer>> => runInPage(BOSS_DOM, tabId, mainReadComposer, [composerId])
    let composer = await readComposer()
    if (!composer.found) throw greetingAfterFirstClick('快捷窗里找不到输入框')
    if (!composer.focused) {
      try {
        await paceBeforeClick()
        await osClickOnce(tabId, ctx, await bossComposerClickPlan(tabId, composerId), '点快捷窗输入框取焦点')
      } catch (error) {
        if (isStopExecution(error)) throw error
        throw greetingAfterFirstClick(describeError(error).slice(0, 200))
      }
      composer = await readComposer()
      if (!composer.focused) throw greetingAfterFirstClick('点中输入框但焦点没到')
      trace.push('点了输入框取焦点')
    }
    const focusWait = await pollUntil(ctx, readComposer, (read) => read.windowFocused)
    if (!focusWait.value.windowFocused) throw greetingAfterFirstClick(`等了 ${READY_WAIT_MS / 1000} 秒 Chrome 仍不在前台,按键会打到别的应用上`)
    if (!focusWait.value.focused) throw greetingAfterFirstClick('打字前焦点已离开输入框')
    if (focusWait.value.text !== '') {
      try {
        await clearBossComposerByKeys(tabId, ctx, trace, focusWait.value.text.length, composerId)
      } catch (error) {
        if (isStopExecution(error)) throw error
        throw greetingAfterFirstClick(describeError(error).slice(0, 200))
      }
      const cleared = await readComposer()
      if (!cleared.focused || !cleared.windowFocused) throw greetingAfterFirstClick('清空输入框后焦点或前台状态已变')
    }
    const { text: typedText, removed } = newlinesToSpaces(args.text)
    if (removed > 0) trace.push(`${removed} 个换行符换成空格`)
    if (typedText === '') throw greetingAfterFirstClick('去掉换行之后没有内容可打')
    ctx.checkpoint()
    let composed
    try {
      composed = await planType(typedText, seedFrom(ctx.cmdMsgId, 0), { sanitize: true })
    } catch (error) {
      throw greetingAfterFirstClick(`排版器自身异常:${describeError(error).slice(0, 200)}`)
    }
    if (!composed.ok) throw greetingAfterFirstClick(`文案排不出合格键序(${composed.tries} 次):${composed.reasons.join(';').slice(0, 200)}`)
    const targetText = composed.text
    if (composed.dropped.length > 0) {
      const note = `清洗摘掉 ${composed.dropped.length} 个打不出的字元(${summarizeDroppedKinds(composed.dropped)})`
      trace.push(note)
      reportHandLog('warn', 'typedTextSanitized', `chat.sendGreeting ${note}`)
    }
    let played
    try {
      played = await playTypePlan(composed.plan)
    } catch (error) {
      throw greetingAfterFirstClick(isHandServiceDown(error) ? '手服务不可用,打字未开始' : `打字半途失败,输入框可能残留草稿:${describeError(error).slice(0, 200)}`)
    }
    trace.push(`发了 ${played.keys} 次按键${played.words ? ` | ${played.words}` : ''}`)
    const shortfall = tipWordsShortfall(played)
    if (shortfall) throw greetingAfterFirstClick(`${shortfall};已停在草稿,不发`)
    const typed = await readComposer()
    const floor = typedTextFloor(targetText, typed.text)
    if (!floor.ok) throw greetingAfterFirstClick(`上屏文本与文案差得太远,已停在草稿:${describeTypedTextFloor(floor)}`)
    if (!floor.exact) {
      const note = `上屏与文案有差,地板之内照发:${describeTypedTextFloor(floor)}`
      trace.push(note)
      reportHandLog('warn', 'typedTextDrift', `chat.sendGreeting ${note}`)
    }
    const acceptedText = typed.text
    ctx.progress('招呼正文已上屏,准备发送', 70)
    // 最后一道闸之后唯一一次点击发送:快捷窗仍绑定目标(MAIN)+ 落点在唯一发送钮上且编辑器文本等于文案(isolated)。
    const button = await runInPage(BOSS_DOM, tabId, domReadBossSendButton, [QUICK_CHAT_SEL.sendButton])
    if (!button.found) throw greetingAfterFirstClick(`快捷窗发送钮认不出(命中 ${button.count} 个)`)
    if (button.text !== SEND_TEXT) throw greetingAfterFirstClick(`发送钮文案不是「${SEND_TEXT}」(读到「${button.text}」)`)
    const sendPlan: ClickPlan = {
      label: '快捷窗发送钮',
      rect: button.rect,
      hitTest: async (x, y) => {
        const bound = await readBossQuickChat(tabId)
        if (!(bound.status === 'ready' && bound.uid === geekId && bound.friendSource === geekSource)) {
          return { onTarget: false, found: `快捷窗不再绑定目标(${bound.status})` }
        }
        return runInPage(BOSS_DOM, tabId, domBossQuickSendGate, [QUICK_CHAT_SEL.sendButton, x, y, composerId, acceptedText])
      },
      observe: async (): Promise<ClickObservation> => {
        const after = await readComposer()
        return { trusted: null, onTarget: null, eventDriftPx: null, after: `输入框内容长度=${after.text.length}` }
      },
      ...(await bossRetreatPlan(tabId).then((retreat) => (retreat === undefined ? {} : { retreat }))),
    }
    ctx.checkpoint()
    await paceBeforeClick()
    await verifiedBossTab(fingerprint)
    if (Date.now() > ctx.irreversibleNotAfterMs) throw greetingAfterFirstClick('不可逆动作窗口已过,已停在草稿')
    const dispatchedAt = Date.now()
    const sendProbe = await runOsProbe(BOSS_INJECT, tabId, ctx, sendPlan)
    if (sendProbe.outcome !== 'clicked') throw greetingAfterFirstClick(`发送钮未点击,已停在草稿:${sendProbe.detail ?? sendProbe.outcome}`)
    trace.push(`点了发送 ${sendProbe.detail ?? ''}`)
    // 正文正证(判据表第三行):快捷窗消息数组出现我方文本行,不在基线里、哈希相等、服务端确认(status 1/2)、时间不早于派发。
    const deadline = Date.now() + READY_WAIT_MS
    let lastSeen = ''
    await sleep(500)
    while (Date.now() < deadline) {
      ctx.checkpoint()
      try {
        const after = await readBossQuickChat(tabId)
        if (after.status === 'ready' && after.uid === geekId && after.friendSource === geekSource) {
          const hit = pickSentBossRow(after.rows, baselineMids, dispatchedAt)
          lastSeen = `基线外新行 ${after.rows.filter((row) => !baselineMids.has(row.mid)).length},命中 ${hit ? 1 : 0}`
          if (hit) {
            const sentHash = await sha256Hex(hit.hashInput)
            if (sentHash !== contentHash) {
              const note = `实发正文与计划不同:计划 ${Array.from(normalizedText).length} 字,实发 ${Array.from(hit.text).length} 字`
              trace.push(note)
              reportHandLog('warn', 'sentTextDiffers', `chat.sendGreeting ${note}`)
            }
            trace.push('正文已可见')
            await closeBossQuickChatBestEffort(tabId, ctx, '发送后收窗')
            await parkBossCursor(tabId, ctx, '发送后')
            await verifiedBossTab(fingerprint)
            ctx.progress('招呼已发出并在快捷窗确认', 100)
            console.info('[RecruitHelper] boss_send_greeting', trace.join(' | '))
            return {
              platformUserRef: args.platformUserRef,
              positionRef: args.positionRef,
              conversationRef,
              contentHash: sentHash,
              sentText: hit.text,
              observedAt: Date.now(),
            }
          }
        } else {
          lastSeen = `快捷窗 ${after.status === 'ready' ? '绑定已变' : after.status}`
        }
      } catch (error) {
        if (isStopExecution(error)) throw error
        lastSeen = `读取异常 ${describeError(error).slice(0, 120)}`
      }
      await sleep(500)
    }
    throw new PlatformError('POSTCONDITION_UNCONFIRMED',
      `关系已建立、只点击了一次发送,但未在快捷窗确认正文(${lastSeen};${trace.join(' | ')})`,
      'manualOnly', undefined, 'possible')
  })
}

// ── chat.readGreetingOutcome(第一版只读推荐卡,出口 §四 第 5 件) ─────────────────

async function readBossGreetingOutcome(
  args: ChatReadGreetingOutcomeArgs, ctx: PrimitiveContext, fingerprint: string | undefined,
): Promise<ChatReadGreetingOutcomeData> {
  if (validatePrimitiveArgs(PrimitiveName.ChatReadGreetingOutcome, 1, args).length !== 0) {
    throw new PlatformError('GUARD_FAILED', '招呼结果读取参数不符合当前契约', 'manualOnly')
  }
  if (!fingerprint) throw new PlatformError('ACCOUNT_MISMATCH', '命令未携带已绑定账号指纹', 'manualOnly')
  const geekId = parseBossGeekId(args.platformUserRef)
  if (geekId === null) throw new PlatformError('GUARD_FAILED', '候选人引用不是本平台形态', 'manualOnly')
  ctx.checkpoint()
  // intrusive/idempotentReadReceipt 紧贴第一次平台读取设置取消安全点;不写 witness(与智联同款)。
  await ctx.beforeSideEffect()
  try {
    const tab = await requireBossRecommendTab(fingerprint)
    const read = await readBossRecommendTarget(tab.id!, geekId)
    if (read.status === 'ready' && read.card.encryptJobId === args.positionRef && bossCardContactState(read.card) === 'established') {
      return {
        confirmed: true,
        contentHash: args.contentHash,
        conversationRef: bossConversationRef(geekId, read.card.geekSource),
        observedAt: Date.now(),
      }
    }
    reportHandLog('warn', 'greetingOutcomeUnconfirmed',
      `BOSS 招呼验证读未取得正证:${read.status === 'ready' ? `isFriend=${read.card.isFriend ?? 'null'} 按钮「${read.card.buttonText}」职位一致=${read.card.encryptJobId === args.positionRef}` : read.status}`)
  } catch (error) {
    if (isStopExecution(error)) throw error
    // 页面不在、目标消失、账号无法复核或读取异常都不证明招呼失败;留痕后如实回未确认。
    reportHandLog('warn', 'greetingOutcomeReadFailed', `BOSS 招呼验证读异常:${describeError(error).slice(0, 200)}`)
  }
  return { confirmed: false, observedAt: Date.now() }
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
  planBossInterviewForm,
  parseBossCalendarMonth,
  pickBossCalendarCell,
  planBossTimeItemReach,
  bossInterviewFormMismatch,
  domReadBossInterviewButton,
  domHitTestContains,
  domReadBossInterviewModal,
  domBossInterviewSendGate,
  INTERVIEW_SEL,
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
  // 第二刀(2026-09-05):推荐页采集 + 打招呼。
  // 实发正文即事实(2026-09-07):地板、TIP 核对、认行三个纯判定点。
  typedTextFloor,
  sampleQuickChatReadPause,
  QUICK_CHAT_READ_PAUSE_MS,
  levenshteinChars,
  describeTypedTextFloor,
  tipWordsShortfall,
  summarizeDroppedKinds,
  pickSentBossRow,
  TYPED_TEXT_FLOOR,
  parseBossGeekId,
  bossFilterGroupName,
  bossJobItemName,
  matchBossJobItem,
  planBossSourcingFilters,
  bossFilterClicks,
  projectBossSourcingFilters,
  bossJobListSections,
  bossCardContactState,
  projectBossSourcingResume,
  mainReadBossRecommendWindow,
  mainReadBossRecommendTarget,
  mainReadBossQuickChat,
  domReadBossFilterPanel,
  domReadBossJobList,
  domReadBossQuickChatShell,
  domBossQuickSendGate,
  domReadBossParkSpot,
  domBossParkGate,
  greetButtonSelector,
  RECOMMEND_SEL,
  QUICK_CHAT_SEL,
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
  // 场景三的一条(2026-09-04 出口):邀面卡,表单驱动的多步 OS 点击。
  sendInviteCard: ({ args, guards, ctx, fingerprint }) => sendBossInviteCard(args, guards, ctx, fingerprint),
  captureThreadScreenshot: ({ args, ctx, fingerprint }) => captureBossThreadScreenshot(args, ctx, fingerprint),
  // 建档后的简历补采(2026-09-03 甲方选 B):摘要级、零点击,见 readBossResume。
  readResume: ({ args, ctx, fingerprint }) => readBossResume(args, ctx, fingerprint),
  // 第二刀的七条(2026-09-05 开工,出口 docs/boss/第二刀出口-采集与打招呼-2026-09-04.md):推荐页采集 + 打招呼。
  ensureSurface: ({ args, ctx, fingerprint }) => ensureBossSurface(args, ctx, fingerprint),
  readPublishedJobs: ({ ctx, fingerprint }) => readBossPublishedJobs(ctx, fingerprint),
  selectSourcingPosition: ({ args, ctx, fingerprint }) => selectBossSourcingPosition(args, ctx, fingerprint),
  applySourcingFilters: ({ args, ctx, fingerprint }) => applyBossSourcingFilters(args, ctx, fingerprint),
  readSourcingWindow: ({ args, ctx, fingerprint }) => readBossSourcingWindow(args, ctx, fingerprint),
  readSourcingTargetResume: ({ args, ctx, fingerprint }) => readBossSourcingTargetResume(args, ctx, fingerprint),
  sendGreeting: ({ args, guards, ctx, fingerprint }) => sendBossGreeting(args, guards, ctx, fingerprint),
  readGreetingOutcome: ({ args, ctx, fingerprint }) => readBossGreetingOutcome(args, ctx, fingerprint),
} satisfies PlatformAdapter
