// 滚轮排版器(数据那一侧)。
//
// 与鼠标轨迹、打字计划同一条分界:**插件产出一份带时刻的序列,手服务按时刻播**
// (osinput.ts 文件头、Go 侧 handinput/doc.go)。这里只算"什么时候发哪一格",
// 不发系统输入、不等时刻、不读页面。
//
// # 形状
//
// 真人的滚轮不是匀速的。手指"甩"一下,几格挤在几十毫秒里,然后停一停看内容,再甩。
// 所以一次调用排**一簇**:
//
//   - 一簇 3~8 格,每格一个事件、一个刻度(手服务那边一格一次 SendInput,不并格)
//   - 簇内格间隔对数正态(中位 55ms),不是均匀:均匀间隔一眼就是机器
//   - 簇内逐格放慢(每格再长 8%):滚轮在指尖减速,末尾几格比开头稀
//   - 簇间停顿 250~700ms,由闭环调用方在读完页面之后补足
//
// **这些数是定的,不是量的。** 没有真人滚轮的基线(与落点散布同一条挂账),定它们的
// 依据只有"甩动成簇、簇内减速"这两条直觉。真机跑过之后拿 bossInputCounters 里的
// 滚轮计数形状回来校,并把这段注释一起改掉。
//
// # 为什么闭环在调用方而不在这里
//
// 一次滚多少格才到目标,只有读了页面才知道(一格值多少像素随平台与缩放变)。所以
// 排版器不收"要滚多少像素",只收方向与这一簇最多几格;调用方每簇之后回读 scrollTop,
// 到了就停。一次算到位等于在一份计划里预排一个"猜的"格数。

import { mulberry32 } from '../osengine/plan'
import { isHandServiceDown, playScroll, runOsProbe, seedFrom } from './osinput'
import type { ClickPlan, RetreatPlan, ScrollPlayResult } from './osinput'
import type { InjectOptions } from './inject'
import type { PrimitiveContext } from '../registry'
import type { DebugOsScrollData, OsScrollDirection } from '../../base/protocol'

/** 一格滚轮:相对本簇起点的毫秒,与刻度(±1,符号按 W3C deltaY:正=向下)。 */
export interface ScrollTick {
  at: number
  dy: number
}

export const SCROLL_BURST = {
  minTicks: 3,
  maxTicks: 8,
  /** 簇内格间隔:对数正态的中位与形状参数。 */
  gapMedianMs: 55,
  gapSigma: 0.35,
  /** 间隔的合理量程。越界**重采**不夹取——夹取会在边界堆出一道零方差的脊。 */
  gapMinMs: 25,
  gapMaxMs: 160,
  gapMaxDraws: 8,
  /** 簇内逐格放慢的比例(第 i 格的间隔乘 1 + i × 该值)。 */
  decelPerTick: 0.08,
  /** 簇间停顿。 */
  pauseMinMs: 250,
  pauseMaxMs: 700,
} as const

function gaussian(rand: () => number): number {
  // Box-Muller。u1 不能是 0(ln 0),抬一个极小值。
  const u1 = Math.max(rand(), 1e-12)
  const u2 = rand()
  return Math.sqrt(-2 * Math.log(u1)) * Math.cos(2 * Math.PI * u2)
}

/** 抽一个簇内间隔(未乘减速系数)。量程外重采,极端情况下才夹。 */
function drawGapMs(rand: () => number): number {
  let gap = SCROLL_BURST.gapMedianMs
  for (let i = 0; i < SCROLL_BURST.gapMaxDraws; i++) {
    gap = SCROLL_BURST.gapMedianMs * Math.exp(SCROLL_BURST.gapSigma * gaussian(rand))
    if (gap >= SCROLL_BURST.gapMinMs && gap <= SCROLL_BURST.gapMaxMs) return gap
  }
  return Math.min(SCROLL_BURST.gapMaxMs, Math.max(SCROLL_BURST.gapMinMs, gap))
}

/**
 * 排一簇滚轮。
 *
 * `direction` 是 W3C deltaY 的符号(1=向下,-1=向上);`maxTicks` 是这一簇最多几格——
 * 调用方拿剩余预算来限,预算比最小簇还小时就按预算给(最后一簇短一点,不为凑数多滚)。
 * 同一个 `rand` 序列排出同一簇:可复现是排障的前提。
 */
export function composeScrollBurst(
  direction: 1 | -1,
  rand: () => number,
  maxTicks: number = SCROLL_BURST.maxTicks,
): ScrollTick[] {
  const cap = Math.max(1, Math.min(SCROLL_BURST.maxTicks, Math.floor(maxTicks)))
  const span = Math.max(0, cap - SCROLL_BURST.minTicks)
  const count = Math.min(cap, SCROLL_BURST.minTicks + Math.floor(rand() * (span + 1)))
  const ticks: ScrollTick[] = [{ at: 0, dy: direction }]
  let at = 0
  for (let i = 1; i < count; i++) {
    const gap = drawGapMs(rand) * (1 + SCROLL_BURST.decelPerTick * (i - 1))
    at += gap
    ticks.push({ at: Math.round(at), dy: direction })
  }
  return ticks
}

/** 簇间停顿(毫秒)。两个均匀数取平均:中间厚、两端薄,比纯均匀少一点机器味。 */
export function composeScrollPause(rand: () => number): number {
  const u = (rand() + rand()) / 2
  return Math.round(SCROLL_BURST.pauseMinMs + u * (SCROLL_BURST.pauseMaxMs - SCROLL_BURST.pauseMinMs))
}

// ── 闭环编排:落到容器上,一簇一簇滚,每簇后回读 ─────────────────────────────

/** 容器此刻的滚动指标,由平台在 isolated world 读。 */
export interface ScrollMetrics {
  scrollTop: number
  scrollHeight: number
  clientHeight: number
}

/** 要滚的容器。平台知识全在这儿,编排层一个 selector 都不认识。 */
export interface ScrollTarget {
  /** 人话,只进 detail。 */
  readonly label: string
  /** 容器**可见部分**的矩形(视口 CSS 坐标):光标要落在看得见的地方。 */
  readonly rect: { x: number; y: number; w: number; h: number }
  /** 落点上的元素是不是容器或其后代——平台自己的命中测试。 */
  hitTest(clientX: number, clientY: number): Promise<{ onTarget: boolean; found: string }>
  /** 回读指标;容器没了返回 null。 */
  readMetrics(): Promise<ScrollMetrics | null>
  /** 落点被顶层弹层盖住时的退让点,语义同 ClickPlan.retreat。 */
  readonly retreat?: RetreatPlan
}

export interface OsScrollResult {
  outcome: 'scrolled' | 'edge' | 'stuck' | 'refusedByGate' | 'handServiceUnavailable'
  scrollTopBefore: number
  scrollTopAfter: number
  scrolledPx: number
  ticks: number
  bursts: number
  /** 落点趟数(散开加靠近),沿用 osProbe 的口径。 */
  attempts: number
  elapsedMs: number
  lagMaxUs: number
  detail?: string
}

/**
 * 一次调用最多几簇。每簇 3~8 格、加停顿约一秒,60 簇约一分钟——远在 execBudget 之内,
 * 又足够把 20000px 的 distancePx 上限滚完(一格通常 40~120px)。撞到它是"滚不动却又
 * 每簇都在动"的怪形状,如实报 stuck。
 */
const SCROLL_MAX_BURSTS = 60
/**
 * 每簇播完后的回读:滚轮事件的派发与平滑滚动动画都是异步的,/scroll 返回只表示最后一格
 * 已注入。判据是「连续两次读到同一个 scrollTop」,且至少观察满 SETTLE_MIN 再下结论——
 * 否则动画还没起步就读到两个相同的旧值,把"还在动"判成"没动"。
 */
const SCROLL_SETTLE_POLL_MS = 60
const SCROLL_SETTLE_MIN_MS = 150
const SCROLL_SETTLE_MAX_MS = 700
/** 到顶/到底的容差:浏览器的 scrollTop 可能是小数或差一像素。 */
const EDGE_SLACK_PX = 1
/** 一格值多少像素的初始猜测,只用来定第一簇的格数上限;第一簇之后按实测更新。 */
const INITIAL_PX_PER_NOTCH = 100

function atEdge(m: ScrollMetrics, dir: 1 | -1): boolean {
  return dir < 0
    ? m.scrollTop <= EDGE_SLACK_PX
    : m.scrollTop + m.clientHeight >= m.scrollHeight - EDGE_SLACK_PX
}

async function sleep(ms: number): Promise<void> {
  await new Promise((r) => setTimeout(r, ms))
}

async function settledMetrics(target: ScrollTarget): Promise<ScrollMetrics | null> {
  const started = Date.now()
  let last = await target.readMetrics()
  if (last === null) return null
  while (Date.now() - started < SCROLL_SETTLE_MAX_MS) {
    await sleep(SCROLL_SETTLE_POLL_MS)
    const now = await target.readMetrics()
    if (now === null) return null
    if (now.scrollTop === last.scrollTop && Date.now() - started >= SCROLL_SETTLE_MIN_MS) return now
    last = now
  }
  return last
}

/**
 * 把一个容器朝一个方向滚 distancePx。
 *
 * 先经 runOsProbe 以「只落不点」把光标放到容器可见部分上(前台、授权、标定、落点确认、
 * 命中测试一道不少),再一簇一簇播滚轮,每簇后回读 scrollTop:
 *
 *   scrolled  累计像素达到 distancePx(最后一簇允许滚过一点,不为凑数反向补)
 *   edge      容器已到顶/到底
 *   stuck     一簇下去纹丝不动而又不在边上,或动的方向反了——如实报,不换方向、不重试。
 *             方向反了在 macOS 上是可能的(注入器的 wheel1 符号尚未真机钉死),这条就是钉它的闭环。
 *
 * 手服务拒绝(光标已不在落点)是 refusedByGate;手服务不可达是 handServiceUnavailable。
 * 格数由闭环决定:每簇的上限按「剩余像素 / 实测每格像素」取,最后一簇短一点。
 */
export async function runOsScroll(
  inject: InjectOptions,
  tabId: number,
  ctx: PrimitiveContext,
  target: ScrollTarget,
  direction: OsScrollDirection,
  distancePx: number,
): Promise<OsScrollResult> {
  const started = Date.now()
  const dir: 1 | -1 = direction === 'up' ? -1 : 1
  let ticks = 0
  let bursts = 0
  let scrolledPx = 0
  let lagMaxUs = 0
  let attempts = 0
  const trace: string[] = []
  const done = (
    outcome: OsScrollResult['outcome'], before: number, after: number, detail: string,
  ): OsScrollResult => ({
    outcome, scrollTopBefore: before, scrollTopAfter: after, scrolledPx, ticks, bursts, attempts,
    elapsedMs: Date.now() - started, lagMaxUs, detail,
  })

  const plan: ClickPlan = {
    label: target.label,
    rect: target.rect,
    action: 'land',
    hitTest: (x, y) => target.hitTest(x, y),
    observe: async () => ({ trusted: null, onTarget: null, eventDriftPx: null, after: '' }),
    ...(target.retreat === undefined ? {} : { retreat: target.retreat }),
  }
  const probe = await runOsProbe(inject, tabId, ctx, plan)
  attempts = probe.attempts
  lagMaxUs = probe.lagMaxUs
  if (probe.outcome === 'handServiceUnavailable') {
    return done('handServiceUnavailable', 0, 0, probe.detail ?? '手服务不可用')
  }
  if (probe.outcome !== 'landed') {
    return done('refusedByGate', 0, 0, `没落到容器上:${probe.detail ?? probe.outcome}`)
  }
  trace.push(`落点:${(probe.detail ?? '').slice(0, 400)}`)

  const first = await target.readMetrics()
  if (first === null) return done('refusedByGate', 0, 0, '落到容器上之后容器不见了')
  const before = first.scrollTop
  let cur = first
  if (atEdge(cur, dir)) {
    return done('edge', before, cur.scrollTop, `一格没滚:容器已在${dir < 0 ? '顶' : '底'}(${cur.scrollTop}/${cur.scrollHeight - cur.clientHeight}) | ${trace.join(' | ')}`)
  }

  const rand = mulberry32(seedFrom(ctx.cmdMsgId, 7001))
  let pxPerNotch = INITIAL_PX_PER_NOTCH
  while (bursts < SCROLL_MAX_BURSTS) {
    ctx.checkpoint()
    const remaining = distancePx - scrolledPx
    const burst = composeScrollBurst(dir, rand, Math.max(1, Math.ceil(remaining / pxPerNotch)))
    let played: ScrollPlayResult
    try {
      played = await playScroll(burst)
    } catch (error) {
      if (isHandServiceDown(error)) {
        return done('handServiceUnavailable', before, cur.scrollTop, `${String((error as Error).message ?? error)} | ${trace.join(' | ')}`)
      }
      throw error
    }
    bursts += 1
    ticks += played.ticks
    lagMaxUs = Math.max(lagMaxUs, played.lagMaxUs)
    ctx.progress(`滚轮第 ${bursts} 簇:${played.ticks} 格`)
    if (played.status !== 'ok') {
      // 409:光标已不在我们放它的地方——真人碰了鼠标。手服务一格都没发。
      return done('refusedByGate', before, cur.scrollTop, `手服务拒绝滚轮:${played.status} | ${trace.join(' | ')}`)
    }
    const after = await settledMetrics(target)
    if (after === null) return done('stuck', before, cur.scrollTop, `滚动中途容器不见了 | ${trace.join(' | ')}`)
    const delta = after.scrollTop - cur.scrollTop
    trace.push(`簇#${bursts} ${burst.length}格 ${cur.scrollTop}→${after.scrollTop}(${delta >= 0 ? '+' : ''}${delta})`)
    if (delta === 0) {
      if (atEdge(after, dir)) return done('edge', before, after.scrollTop, `到${dir < 0 ? '顶' : '底'}了 | ${trace.join(' | ')}`)
      return done('stuck', before, after.scrollTop,
        `一簇 ${burst.length} 格下去 scrollTop 纹丝不动,又不在边上——光标不在容器上、页面拦截,或滚轮没送到 | ${trace.join(' | ')}`)
    }
    if (Math.sign(delta) !== dir) {
      return done('stuck', before, after.scrollTop,
        `方向反了:要${dir < 0 ? '上' : '下'},页面却动了 ${delta}px——注入器的滚轮符号与本机不符 | ${trace.join(' | ')}`)
    }
    scrolledPx += Math.abs(delta)
    pxPerNotch = Math.max(8, Math.abs(delta) / burst.length)
    cur = after
    if (scrolledPx >= distancePx) return done('scrolled', before, cur.scrollTop, trace.join(' | '))
    if (atEdge(cur, dir)) return done('edge', before, cur.scrollTop, `到${dir < 0 ? '顶' : '底'}了 | ${trace.join(' | ')}`)
    await sleep(composeScrollPause(rand))
  }
  return done('stuck', before, cur.scrollTop, `${SCROLL_MAX_BURSTS} 簇封顶仍未滚够 ${distancePx}px | ${trace.join(' | ')}`)
}

/** 装配契约 data。取整收在这里,与 osProbeContractData 同一条理由(契约里没有浮点)。 */
export function osScrollContractData(res: OsScrollResult, observedAt: number): DebugOsScrollData {
  return {
    outcome: res.outcome,
    scrollTopBefore: Math.max(0, Math.round(res.scrollTopBefore)),
    scrollTopAfter: Math.max(0, Math.round(res.scrollTopAfter)),
    scrolledPx: Math.max(0, Math.round(res.scrolledPx)),
    ticks: Math.round(res.ticks),
    bursts: Math.round(res.bursts),
    attempts: Math.min(16, Math.round(res.attempts)),
    elapsedMs: Math.round(res.elapsedMs),
    lagMaxUs: Math.round(res.lagMaxUs),
    ...(res.detail === undefined ? {} : { detail: res.detail.slice(0, 2048) }),
    observedAt,
  }
}
