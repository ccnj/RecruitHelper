// OS 级注入的编排(program 层)。
//
// # 分界:数据 / 过程
//
// **插件产出数据**(一份带时刻的坐标序列),**脑进程里的手服务执行过程**(按时刻播)。
// 本文件是数据那一侧的编排:算计划、请手服务播、读落点、喂标定。它不发系统输入,
// 也不自己等时刻——那两件事都在 Go 那边。
//
// 分界这么划有一条硬依据:上游打字线的随机数是一条 LCG,`s*1103515245` 在 s 接近
// 2^31 时乘积超过 2^53,JS 的精度丢失是结果的一部分;Go 用精确整数照抄公式第一步
// 就分叉。把引擎移植进 Go 等于作废上游已有的判别器验收。
//
// # 通道
//
// 手服务挂在脑已有的 loopback mux 上(/handinput/*),不是新端口、也不是第二条 WS:
// 全部由本文件发起、一问一答,标定样本搭在下一个请求里回去,不需要推送。
//
// **坐标只走这条路,不进脑手协议。** 脑的业务层永远不知道有坐标这回事。
import { getWsUrl } from '../../base/config'
import { planMove, DEFAULT_MAX_DWELL_MS } from '../osengine/plan'
import { runInPage } from './inject'
import { PlatformError } from './types'
import type { DebugOsProbeData, OsProbeTarget } from '../../base/protocol'
import type { InjectOptions } from './inject'
import type { PrimitiveContext } from '../registry'

/** 冷启动粗估必然打偏,所以要允许重来几趟。上游实测两趟就追上。 */
const MAX_ATTEMPTS = 6

/**
 * 落点偏成这样就算"几何没算对"。
 *
 * 冷启动的粗估误差按上游实测是 121~227 物理像素,所以门限要高过它,否则正常的
 * 冷启动会被误判成失控。取 400:比最坏的正常冷启动还宽一倍,又远小于"算到别的屏上去"
 * 那个量级。
 */
const WILD_DRIFT_PX = 400

/** 相邻可见交互的下限(AGENTS「平台交互节奏与条件等待」),加小幅抖动。 */
const PACE_MIN_MS = 1000
const PACE_JITTER_MS = 800

/**
 * 落点读取前的稳定等待。
 *
 * **不能在 /play 一返回就读。** 手服务返回的时刻只表示"最后一帧已经发出去了",
 * 而浏览器派发 mousemove 是异步的——那时页面记的很可能还是轨迹中途的某个位置。
 *
 * 2026-08-28 真机就是这么翻的车:按种子推算,两趟的落点偏差都该恒定是 121,
 * 实测却是 318 → 1196。**递增**正是"读到中途位置"的形状:第二趟靶子离中途更远,
 * 陈旧值就偏得更多。而这个误差会被当成标定误差喂进搭车拟合,越学越歪。
 *
 * 判据是"连续两次读到同一个位置",不是固定等待:静止之后浏览器就不再派发事件,
 * 所以位置稳定即已落定。
 */
const LANDING_SETTLE_POLL_MS = 40
const LANDING_SETTLE_MAX_MS = 800

/**
 * 页面上存落点观测的键。装一次、读一次、读完摘掉,不留常驻状态(手的禁令 2)。
 *
 * **必须不可枚举**(见下面的 defineProperty)。BOSS 的 risk-detection 会把
 * `Object.keys(window)` 与一份白名单求差、**未知全局名原样上送**(2026-08-28
 * 阳性对照实证,码 800001 的 p6)。一个 `w[key] = state` 就等于自报家门,
 * 而这件事在智联上没有症状 —— 换平台才炸,且炸在对方服务器上,我们看不见。
 */
const LANDING_KEY = '__recruitHelperOsLanding'

/**
 * 一次点击的计划。**平台知识全在这里,编排层一个 selector 都不认识。**
 *
 * 靶子矩形、命中测试、后置状态三样都由适配器提供:前两样决定"这一下会打中谁",
 * 后一样是平台自己给的正证。编排层只负责几何与闸序。
 */
export interface ClickPlan {
  /** 人话,只进 detail。 */
  readonly label: string
  /** 靶子矩形(视口 CSS 坐标)。 */
  readonly rect: { x: number; y: number; w: number; h: number }
  /**
   * 落点上的元素是不是靶子。**用平台自己的命中测试问**——
   * 「这个像素上是谁」是浏览器的公开语义,而"标定够不够准"我们答不了。
   */
  hitTest(clientX: number, clientY: number): Promise<{ onTarget: boolean; found: string }>
  /** 点击之后:页面观测到的 click 事件 + 平台的可见后置状态。 */
  observe(): Promise<ClickObservation>
}

export interface ClickObservation {
  /** 页面捕获期收到的那个 click 是不是真事件。读不到是 null,不是 false。 */
  readonly trusted: boolean | null
  /** 那个事件的 target 是不是靶子。 */
  readonly onTarget: boolean | null
  /** 事件坐标与靶子中心的偏差(CSS px)。 */
  readonly eventDriftPx: number | null
  /** 平台的可见后置状态,自由文本,只给人读。 */
  readonly after: string
}

/**
 * 靠近靶子的尝试次数。
 *
 * **只有 2 次,而且不是为了"多试几次会更准"。** 走到这一步标定已经收敛,同一个点
 * 再移一次不会让几何变好;留第二次只为容忍一种瞬态:真人在落点确认那一刻碰了鼠标。
 * 两次都不过就停手不点 —— 失效方向是不点,不是接着飞。
 */
const CLICK_APPROACH_ATTEMPTS = 2

/** 按压时长。上游实测池的中位数附近,手服务对 0~2000ms 之外一律拒。 */
const CLICK_PRESS_MS = 96

export interface OsProbeResult {
  outcome: 'landed' | 'clicked' | 'refusedByGate' | 'handServiceUnavailable'
  attempts: number
  landingDriftPx?: number
  calibStatus: string
  unreachableFrames: number
  planMs: number
  elapsedMs: number
  lagMaxUs: number
  detail?: string
}

export interface ViewportFacts {
  innerW: number
  innerH: number
  screenX: number
  screenY: number
  dpr: number
  // 本窗口所在显示器在全局坐标空间里的原点,主屏是 (0, 菜单栏高度)。
  // **只作诊断,不参与放行判断** —— 见 refuseBeforeMoving 里那段墓碑。
  availLeft: number
  availTop: number
}

interface HandState {
  // 还没播种(连粗估都没有)时是 null,不是 0。那时手服务是真不知道光标在哪:
  // 零值标定反算会除以零。**0 会被这里当成"光标在视口左上角"照着算一条轨迹出来**,
  // 于是第一帧就是一次几百像素的瞬移。
  cursorCssX: number | null
  cursorCssY: number | null
  calibrated: boolean
  clickArmed: boolean
  samples: number
}

interface PlayResponse {
  unreachable: number
  lagMaxUs: number
}

interface LandingResponse {
  status: string
  clickArmed: boolean
  detail?: string
}

/** 手服务的 HTTP 根。从已校验的脑 WS 地址派生——同一个进程、同一个 loopback 端口。 */
async function handInputBase(): Promise<string> {
  const ws = new URL(await getWsUrl())
  return `http://${ws.host}/handinput`
}

async function callHand<T>(path: string, body?: unknown): Promise<T> {
  const base = await handInputBase()
  let resp: Response
  try {
    resp = await fetch(base + path, body === undefined
      ? { method: 'GET' }
      : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  } catch (error) {
    // 连不上多半是脑没起来,或本平台没有注入实现(那时路由压根没挂)。
    throw new HandServiceDown(`手服务不可达:${String(error).slice(0, 120)}`)
  }
  if (resp.status === 404) {
    throw new HandServiceDown('手服务未挂载——本平台可能没有注入实现')
  }
  if (!resp.ok && resp.status !== 409) {
    throw new PlatformError('INTERNAL_HAND', `手服务返回 ${resp.status}`, 'afterRecovery')
  }
  return await resp.json() as T
}

class HandServiceDown extends Error {}

/** 装观测器并读视口。两件事一次注入完成,省一个往返,也保证观测器先于移动就位。 */
function pageInstallObserverAndReadViewport(key: string): ViewportFacts {
  const w = window as unknown as Record<string, unknown>
  const prev = w[key] as { off?: () => void } | undefined
  if (prev && typeof prev.off === 'function') prev.off()
  const state: { x: number | null; y: number | null; off?: () => void } = { x: null, y: null }
  const onMove = (e: MouseEvent): void => {
    state.x = e.clientX
    state.y = e.clientY
  }
  document.addEventListener('mousemove', onMove, true)
  state.off = (): void => document.removeEventListener('mousemove', onMove, true)
  // enumerable:false —— Object.keys(window) 里看不见它。configurable:true 让
  // 读完之后的 delete 照常生效。
  Object.defineProperty(w, key, { value: state, enumerable: false, configurable: true, writable: true })
  return {
    innerW: window.innerWidth,
    innerH: window.innerHeight,
    // screenX/Y 只能用来猜往哪个方向先动,**不许参与任何计算**:
    // 上游实测过窗口一次没动而页面报的值在 430 与 0 之间跳。
    screenX: window.screenX,
    screenY: window.screenY,
    dpr: window.devicePixelRatio,
    availLeft: (window.screen as unknown as { availLeft?: number }).availLeft ?? 0,
    availTop: (window.screen as unknown as { availTop?: number }).availTop ?? 0,
  }
}

/** 读落点并摘掉观测器。 */
function pageReadLandingAndDetach(key: string): { x: number | null; y: number | null } {
  const w = window as unknown as Record<string, unknown>
  const state = w[key] as { x: number | null; y: number | null; off?: () => void } | undefined
  if (!state) return { x: null, y: null }
  if (typeof state.off === 'function') state.off()
  delete w[key]
  return { x: state.x, y: state.y }
}

/** 只读落点,观测器留着(重试时还要用)。 */
function pageReadLanding(key: string): { x: number | null; y: number | null } {
  const w = window as unknown as Record<string, unknown>
  const state = w[key] as { x: number | null; y: number | null } | undefined
  return state ? { x: state.x, y: state.y } : { x: null, y: null }
}

function seedFrom(text: string, attempt: number): number {
  let h = 2166136261
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  // 种子取正整数;掺进 attempt,让每一趟走不同的轨迹。
  return ((h >>> 0) % 1000003) + attempt * 7919 + 1
}

async function pace(): Promise<void> {
  await new Promise((r) => setTimeout(r, PACE_MIN_MS + Math.floor(Math.random() * PACE_JITTER_MS)))
}

/**
 * 走一遍 OS 注入探针。
 *
 * 两种形态,由 `plan` 有无决定:
 *
 *   无 plan  只移动,绝不点击(viewportSpread)。
 *   有 plan  先照样用散开的靶子把标定喂到收敛,**再**贴到真靶子上、过命中测试、
 *            按一下(reversibleToggle)。
 *
 * **散开那一段不能省。** 搭车标定要样本在两轴各张开 200 CSS px 才解得出 scale;
 * 一上来就盯着一个固定靶子移动,样本挤成一团,标定会一直停在冷启动直到把重试用完
 * (2026-08-28 真机踩过)。
 */
export async function runOsProbe(
  inject: InjectOptions,
  tabId: number,
  ctx: PrimitiveContext,
  click?: ClickPlan,
): Promise<OsProbeResult> {
  const started = Date.now()
  let unreachable = 0
  let planMs = 0
  let lagMaxUs = 0
  let drift: number | undefined
  let calibStatus = '未知'

  let view: ViewportFacts
  try {
    view = await runInPage(inject, tabId, pageInstallObserverAndReadViewport, [LANDING_KEY])
  } catch (error) {
    throw error instanceof PlatformError ? error : new PlatformError(
      'CTX_NOT_READY', `读视口失败:${String(error).slice(0, 120)}`, 'afterRecovery', 'contentScriptDead')
  }

  // **副屏一律拒绝。** 上游交接文档对混合 DPI 多屏的处置原话是"回避,不是验证":
  // 虚拟桌面坐标空间是否均匀、拟合的 scale 对应哪块屏,那边一个字都没有。
  //
  // 而回避这件事必须做在**移动之前**:副屏上粗估算出来的偏移在全局坐标空间里是错的,
  // 落点判据一直不过,于是每一趟重试都是一次大范围横扫——2026-08-28 真机上就是这么
  // 让光标飞了 34 秒。失效方向必须是"不动",不是"多试几次"。
  //
  // 判据取窗口所在显示器的原点:主屏的 availLeft 是 0(availTop 在 macOS 上是菜单栏
  // 高度,所以只在明显超过菜单栏时才算)。
  const refusal = refuseBeforeMoving(view)
  if (refusal !== null) {
    return { outcome: 'refusedByGate', attempts: 0, calibStatus, unreachableFrames: 0,
      planMs: 0, elapsedMs: Date.now() - started, lagMaxUs: 0, detail: refusal }
  }

  // 靶子:视口里几个**散开**的点,按趟轮换。它们不需要任何平台 DOM 知识。
  //
  // **散开是必须的,不是为了好看。** 搭车标定要样本在两个轴上各张开 MinSpanPx
  // (200 CSS px)才解得出 scale——那个数是从 clientX 的 ±0.5 取整噪声推出来的:
  // 两个相距 R 的样本,scale 的相对误差上界是 1/R,而要落进 snapScale 的 0.5%
  // 吸附容差就得 R > 200。
  //
  // 一个固定靶子反复移动永远张不开:第一趟落在靶子附近之后,后面每一趟都只挪
  // 一百来像素,样本挤成一团,标定会一直停在冷启动直到把重试次数用完。
  const spread = (fx: number, fy: number) => ({
    x: Math.round(view.innerW * fx), y: Math.round(view.innerH * fy),
  })
  const targets = [spread(0.22, 0.22), spread(0.78, 0.78), spread(0.78, 0.22), spread(0.22, 0.78)]
  const hint = { screenX: view.screenX, screenY: view.screenY, dpr: view.dpr }

  const trace: string[] = []
  let attempts = 0
  let previousDrift = Number.POSITIVE_INFINITY
  let detail: string | undefined
  try {
    while (attempts < MAX_ATTEMPTS) {
      attempts++
      ctx.checkpoint()
      if (attempts > 1) await pace()

      // 起点必须现读:Snap 与 ToClient 用同一份标定、互为逆运算,现读再反算必然
      // 落回原地,不管标定多离谱;而用记忆里的 CSS 值没有这个抵消,标定一被修正
      // 就错位,错位量正好等于修正量——冷启动时那是一次一两百像素的干净瞬移。
      // /state 是 POST 并捎上粗估:编排要在生成计划之前问"光标在哪",而那个答案
      // 要经当前标定反算——没播种就连粗估都没有。让第一次问状态就把粗估带上,
      // 这个先有鸡还是先有蛋的坎就没了。
      const state = await callHand<HandState>('/state', { hint })
      if (state.cursorCssX === null || state.cursorCssY === null) {
        detail = '手服务报不出光标位置——标定连粗估都没建起来'
        break
      }
      const from = { x: state.cursorCssX, y: state.cursorCssY }

      const target = targets[(attempts - 1) % targets.length]!
      const plan = planMove({
        from, to: target,
        // 视口中心没有"元素矩形",用上游拟合的缺省宽度。
        targetW: 28,
        maxDwellMs: DEFAULT_MAX_DWELL_MS,
        seed: seedFrom(ctx.cmdMsgId, attempts),
      })
      if (plan.points.length === 0) {
        detail = '引擎产出空计划'
        break
      }
      planMs = Math.round(plan.points[plan.points.length - 1]!.t)
      ctx.progress(`第 ${attempts} 趟:${plan.points.length} 帧 / ${planMs}ms`)

      const play = await callHand<PlayResponse>('/play', { points: plan.points, hint })
      unreachable += play.unreachable
      lagMaxUs = Math.max(lagMaxUs, play.lagMaxUs)

      const landed = await readSettledLanding(inject, tabId)
      if (landed.x === null || landed.y === null) {
        // 一个 mousemove 都没观测到,**这时必须停,不能重试**。
        //
        // 观测不到说明光标压根不在页面上——多半是标定把它算到屏幕外去了。
        // 那时再试一趟只是让光标再飞一圈,不会变好:没有观测就没有样本,
        // 没有样本标定就学不到东西。2026-08-28 副屏那 34 秒里,六趟重试
        // 每一趟都是这个形态。失效方向是不动。
        detail = `页面没有观测到任何 mousemove——光标多半不在页面上,停手 | 靶(${target.x},${target.y}) 视口${view.innerW}x${view.innerH}`
        calibStatus = '无观测'
        break
      }
      drift = Math.ceil(Math.hypot(landed.x - target.x, landed.y - target.y))
      // **判定现场必须留下来。** 只报一个"偏差 N px"事后什么都推不出来:
      // 到底是 x 偏还是 y 偏、是恒定偏移还是随靶子放大、光标起点在哪 ——
      // 这些决定了根因是 offset 错、scale 错还是观测错,而它们只在这一刻可见。
      trace.push(`#${attempts} 从(${Math.round(from.x)},${Math.round(from.y)})` +
        `→靶(${target.x},${target.y}) 落(${landed.x},${landed.y}) 偏${drift}` +
        ` 视口${view.innerW}x${view.innerH} 粗估(${hint.screenX},${hint.screenY})x${hint.dpr}`)

      const fed = await callHand<LandingResponse>('/landing', { clientX: landed.x, clientY: landed.y })
      calibStatus = fed.status
      if (fed.detail) detail = fed.detail

      // **偏差不收敛就早停,不要把重试次数用满。**
      //
      // 每一趟重试都是一次真实的大范围横扫。标定正常时两趟就追上(上游实测),
      // 所以第二趟之后还偏着几百像素,说明这台机器的几何我们根本没算对——
      // 那时继续试只是让光标多飞几圈,不会变好。失效方向是"不动"。
      if (attempts >= 2 && drift > WILD_DRIFT_PX && drift >= previousDrift) {
        detail = `连续两趟没收敛,几何没算对,停手 | ${trace.join(' | ')}`
        break
      }
      previousDrift = drift

      if (fed.clickArmed) {
        if (!click) {
          return { outcome: 'landed', attempts, landingDriftPx: drift, calibStatus,
            unreachableFrames: unreachable, planMs, elapsedMs: Date.now() - started, lagMaxUs }
        }
        const approach = await approachAndClick(inject, tabId, ctx, click, trace)
        return {
          outcome: approach.outcome, attempts, calibStatus, planMs,
          landingDriftPx: approach.landingDriftPx ?? drift,
          unreachableFrames: unreachable + approach.unreachable,
          lagMaxUs: Math.max(lagMaxUs, approach.lagMaxUs),
          elapsedMs: Date.now() - started,
          ...(approach.detail === undefined ? {} : { detail: approach.detail }),
        }
      }
    }
  } catch (error) {
    if (error instanceof HandServiceDown) {
      return { outcome: 'handServiceUnavailable', attempts, calibStatus,
        unreachableFrames: unreachable, planMs, elapsedMs: Date.now() - started, lagMaxUs,
        detail: error.message }
    }
    throw error
  } finally {
    // 观测器只活在这条命令里:手不持久化业务状态,页面上也不留常驻监听。
    try {
      await runInPage(inject, tabId, pageReadLandingAndDetach, [LANDING_KEY])
    } catch {
      // 页面可能已经导航走了。摘不掉不是失败——它随页面一起没。
    }
  }

  return { outcome: 'refusedByGate', attempts, ...(drift === undefined ? {} : { landingDriftPx: drift }),
    calibStatus, unreachableFrames: unreachable, planMs, elapsedMs: Date.now() - started, lagMaxUs,
    detail: detail ?? `重试用尽 | ${trace.join(' | ')}` }
}


interface ApproachOutcome {
  outcome: OsProbeResult['outcome']
  landingDriftPx?: number
  detail?: string
  /** 靠近阶段自己的撞格帧数与最大滞后,由调用方并进总数。 */
  unreachable: number
  lagMaxUs: number
}

/**
 * 贴到真靶子上,过完闸再按一下。**至多一次点击,任何一步不过就不点。**
 *
 * 闸序(缺一不点):
 *
 *   1. 落点被搭车标定接受    —— 手服务答的,几何自洽
 *   2. 落点上的元素就是靶子  —— **平台自己的命中测试答的**
 *   3. 光标还在原处          —— 手服务在 /click 里再核对一次
 *
 * 第 2 道是这一段的核心。「标定够不够准」我们答不了,但它可以翻译成一个平台
 * 答得了的问题:这个像素上是谁?那是浏览器的公开语义,正落在「只核对世界状态与
 * 动作接口的公开语义」的边界内侧。
 *
 * 点完只如实上报,**不重试、不点第二下**:原语内不重试是内核。
 */
async function approachAndClick(
  inject: InjectOptions,
  tabId: number,
  ctx: PrimitiveContext,
  plan: ClickPlan,
  trace: string[],
): Promise<ApproachOutcome> {
  const center = {
    x: Math.round(plan.rect.x + plan.rect.w / 2),
    y: Math.round(plan.rect.y + plan.rect.h / 2),
  }
  let drift: number | undefined
  let lastRefusal = '没到靠近阶段'
  // 靠近阶段的撞格帧数必须如实并进总数。**这里比散开阶段更要紧**:撞格意味着
  // 那一帧鼠标根本没动、浏览器不派发事件,而这几帧恰恰是贴靶子的最后几帧。
  let unreachable = 0
  let lagMaxUs = 0

  for (let approach = 1; approach <= CLICK_APPROACH_ATTEMPTS; approach += 1) {
    ctx.checkpoint()
    await pace()

    const state = await callHand<HandState>('/state')
    if (state.cursorCssX === null || state.cursorCssY === null) {
      return { outcome: 'refusedByGate', unreachable, lagMaxUs,
        detail: `靠近靶子时读不到光标 | ${trace.join(' | ')}` }
    }
    const move = planMove({
      from: { x: state.cursorCssX, y: state.cursorCssY },
      to: center,
      targetW: Math.max(8, Math.round(plan.rect.w)),
      maxDwellMs: DEFAULT_MAX_DWELL_MS,
      seed: seedFrom(ctx.cmdMsgId, 100 + approach),
    })
    if (move.points.length === 0) {
      return { outcome: 'refusedByGate', unreachable, lagMaxUs,
        detail: `靠近靶子的计划是空的 | ${trace.join(' | ')}` }
    }
    ctx.progress(`靠近「${plan.label}」第 ${approach} 次:${move.points.length} 帧`)
    const played = await callHand<PlayResponse>('/play', { points: move.points })
    unreachable += played.unreachable
    lagMaxUs = Math.max(lagMaxUs, played.lagMaxUs)

    const landed = await readSettledLanding(inject, tabId)
    if (landed.x === null || landed.y === null) {
      return {
        outcome: 'refusedByGate', unreachable, lagMaxUs,
        detail: `靠近后页面没观测到 mousemove,光标多半不在页面上 | ${trace.join(' | ')}`,
      }
    }
    drift = Math.ceil(Math.hypot(landed.x - center.x, landed.y - center.y))

    const fed = await callHand<LandingResponse>('/landing', { clientX: landed.x, clientY: landed.y })
    // **命中测试用落点问,不用靶心问。** 靶心是我们想去的地方,落点是真到了的地方;
    // 这一下会打中谁只取决于后者。
    const hit = await plan.hitTest(landed.x, landed.y)
    trace.push(`靠近#${approach} 靶「${plan.label}」中心(${center.x},${center.y})` +
      ` 落(${landed.x},${landed.y}) 偏${drift} 标定=${fed.status}` +
      ` 命中=${hit.onTarget ? '是' : '否'}(${hit.found})`)

    if (!fed.clickArmed) {
      lastRefusal = `落点没被接受(${fed.status}${fed.detail ? ':' + fed.detail : ''})`
      continue
    }
    if (!hit.onTarget) {
      lastRefusal = `落点上不是靶子,而是 ${hit.found}`
      continue
    }

    // 三道闸齐,按下去。至多这一次。
    const clicked = await callHand<{ clicked?: boolean; refused?: string }>(
      '/click', { pressMs: CLICK_PRESS_MS })
    if (clicked.refused !== undefined) {
      // 手服务自己的第三道闸(光标此刻还在不在原处)拒了。如实上报,不再试。
      return {
        outcome: 'refusedByGate', landingDriftPx: drift, unreachable, lagMaxUs,
        detail: `手服务拒绝点击:${clicked.refused} | ${trace.join(' | ')}`,
      }
    }

    const seen = await plan.observe()
    trace.push(`点后 isTrusted=${seen.trusted === null ? '读不到' : seen.trusted}` +
      ` 命中靶子=${seen.onTarget === null ? '读不到' : seen.onTarget}` +
      ` 事件偏差=${seen.eventDriftPx === null ? '读不到' : seen.eventDriftPx}` +
      ` 后置=${seen.after}`)
    return { outcome: 'clicked', landingDriftPx: drift, unreachable, lagMaxUs,
      detail: trace.join(' | ') }
  }

  return {
    outcome: 'refusedByGate', unreachable, lagMaxUs,
    ...(drift === undefined ? {} : { landingDriftPx: drift }),
    detail: `${CLICK_APPROACH_ATTEMPTS} 次靠近都没过闸,最后一次:${lastRefusal} | ${trace.join(' | ')}`,
  }
}

/**
 * 把探针结果装配成契约 data。
 *
 * **取整在这里,不在适配器里。** 契约的 DebugOsProbeData 全是整数(契约里根本没有
 * 浮点类型),而手服务算出来的滞后是浮点——真机第一次成功跑完 34.6 秒之后,
 * result 就是被 `$.data.lagMaxUs: 需要整数` 拦在回程上,那一趟的数据全丢了。
 *
 * 收在一处的理由是:适配器不该知道哪些字段要取整。加第二个平台时它只管调编排,
 * 装配这一步共用同一份,不会有人再漏掉一个字段。
 */
export function osProbeContractData(
  target: OsProbeTarget,
  probe: OsProbeResult,
  observedAt: number,
): DebugOsProbeData {
  return {
    target,
    outcome: probe.outcome,
    attempts: Math.round(probe.attempts),
    ...(probe.landingDriftPx === undefined ? {} : { landingDriftPx: Math.ceil(probe.landingDriftPx) }),
    calibStatus: probe.calibStatus,
    unreachableFrames: Math.round(probe.unreachableFrames),
    planMs: Math.round(probe.planMs),
    elapsedMs: Math.round(probe.elapsedMs),
    lagMaxUs: Math.round(probe.lagMaxUs),
    ...(probe.detail === undefined ? {} : { detail: probe.detail.slice(0, 2048) }),
    observedAt,
  }
}

/**
 * 移动之前的一次性拒绝判据。返回原因文本表示拒绝,null 表示可以动。
 *
 * **这道闸必须在任何移动之前。** 它拦的是"我们根本算不对这台机器的几何"那一类,
 * 而那一类一旦放行,后果不是失败一次,是每一趟重试都变成一次真实的大范围横扫。
 */
export function refuseBeforeMoving(view: ViewportFacts): string | null {
  if (!(view.innerW > 40) || !(view.innerH > 40)) {
    return `视口尺寸异常 ${view.innerW}x${view.innerH}`
  }
  // **这里曾经有一条"副屏一律拒绝"。2026-08-28 实测之后撤掉了。**
  //
  // 当天副屏上光标飞了 34 秒,第一反应是照上游"混合 DPI 多屏……处置是回避"加了道闸。
  // 但两点实测把根因钉死在别处:副屏没问题,是**种子公式抄错了平台** —— macOS 的
  // CGEventPost 收 point,而那份公式照 Windows 乘了 dpr,于是 screenX=2560 的副屏上
  // 种子把光标算到桌面外 2560 点。修好种子之后副屏与主屏没有区别。
  //
  // 记这一笔是因为那道闸看起来很合理:它拿一个可修的 bug 换了一条永久的产品约束
  // (客户把浏览器摆哪块屏成了硬要求),而我们控制不了客户怎么摆屏幕。
  //
  // 真正未验证的是**跨屏**:一条轨迹横跨两块缩放不同的屏时映射不再是单一仿射。
  // 那一类还没遇到,遇到时按当时看到的形状立案,不预先造闸。
  return null
}

/**
 * 等落点稳定再读。见 LANDING_SETTLE_POLL_MS 的说明。
 *
 * 返回最后读到的位置;超时仍未稳定也返回当前值——那时它多半确实还在动,
 * 后面的偏差判据会把它拦下来。失效方向是"不确认",不是"猜一个"。
 */
async function readSettledLanding(
  inject: InjectOptions,
  tabId: number,
): Promise<{ x: number | null; y: number | null }> {
  const deadline = Date.now() + LANDING_SETTLE_MAX_MS
  let last = await runInPage(inject, tabId, pageReadLanding, [LANDING_KEY])
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, LANDING_SETTLE_POLL_MS))
    const now = await runInPage(inject, tabId, pageReadLanding, [LANDING_KEY])
    if (now.x === last.x && now.y === last.y) return now
    last = now
  }
  return last
}
