// 鼠标移动计划的唯一生成入口。
//
// **本文件在「数据」那一侧。** 它产出一份计划(去哪儿、什么时候),不碰 OS、不碰
// 时钟、不等待、不发出任何东西。按时刻把计划播出去是脑进程 `handinput` 包的事。
//
// 这条分界不是审美:上游引擎的文件头写着「不调 Date.now / setTimeout —— 输出的是
// 数据,不是过程。一旦引擎里出现『等一下再走下一步』,它就搬不走」。我们这一层
// 一旦开始等待或重试,同样搬不回去,而且 hiBoss 那边的离线跑分就对不上我们真机
// 跑出来的东西了。
//
// 与 `vendor/` 的关系见 ./README.md。

import { UNITS } from './vendor/route.mjs'
import type { RoutePoint } from './vendor/route.mjs'
import { DEFAULT_ENGINE } from './vendor/engines.mjs'
import { ROUTES } from './vendor/route.bjh.mjs'
import { PRESS_MS } from './vendor/press.bjh.mjs'

/**
 * 上游版本钉子。**池子换了轨迹就变了**,而 hiBoss 的跑分是对着某一版池子做的;
 * 同步上游时这里必须一起改,否则「我们跑的」与「那边验的」是两个东西。
 */
export const OSENGINE_SOURCE = {
  repo: 'hiBoss',
  commit: 'ef1b134',
  files: 'lab/engine/mouse/{route,engines}.mjs + lab/probe/baseline/{route,press}.bjh.mjs',
  pooledAt: '2026-08-27',
} as const

/**
 * 引擎参数,必须与 hiBoss 跑分时用的一致(`lab/probe/emit-trajectory.mjs`)。
 * 改这两个数等于换了一个没被判别器验过的引擎配置。
 */
const ENGINE_PARAMS = { spd: 1, arc: 0.12 } as const

/**
 * 停顿截断的缺省值。
 *
 * 路由池里有真人离开数分钟再回来的真实记录,播放方必须截断,否则一条原语会在
 * 那儿等足几分钟。3000 砍掉真人 1870 次驻留里的 154 次(8.2%);**这个代价能不能
 * 被判别器认出来,上游没验过**。往上调直接买时间,单窗口 p99 从 23.5s 涨到 33.2s(10s 档)。
 */
export const DEFAULT_MAX_DWELL_MS = 3000

/** 视口 CSS 坐标。原点在视口左上角,与页面的 clientX/clientY 同一口径。 */
export interface ViewportPoint {
  readonly x: number
  readonly y: number
}

/** 计划里的一帧。`t` 是相对本计划起点的毫秒。 */
export interface PlanPoint extends ViewportPoint {
  readonly t: number
}

export interface MovePlanInput {
  /** 起点。**必须是光标此刻真实所在**,不能用记忆里的值——理由见 README「起点」一节。 */
  readonly from: ViewportPoint
  readonly to: ViewportPoint
  /**
   * 点击目标沿运动方向的有效宽度(CSS px)。
   *
   * 上游拟合出来的是标量 28,**从没见过那些点击目标的矩形**,所以「矩形该换算成
   * min(w,h) 还是沿运动方向的投影」没有依据,由调用方定。我们取沿运动方向的投影
   * (Fitts 律里的 W 本来就是这个),这是判断不是实测。
   */
  readonly targetW: number
  readonly maxDwellMs: number
  readonly seed: number
  /** 可选的中途落脚点。传了就用页面元素位置当停脚处,不传由路由自己算。 */
  readonly landmarks?: readonly ViewportPoint[]
}

export interface MovePlan {
  readonly points: readonly PlanPoint[]
  /**
   * 打算按多久(ms)。**从 464 条实测池等概率抽,不是常数**:平台侧的按压时长跨
   * 数十次点击累积从不清零,常数会给出一串一模一样的数字,那是零误伤的机器签名。
   *
   * 它与 points 并列,不是轨迹的尾巴——先走完 points、确认落点、过了放行判据,
   * 才拿这个数去按。判据不过就不按。
   */
  readonly pressMs: number
  readonly engine: string
  readonly seed: number
  readonly distPx: number
}

/**
 * 确定性伪随机。
 *
 * **必须与 hiBoss 逐位一致**,否则同种子生成不出同一条轨迹,那边的离线跑分与
 * 我们真机播的就不是一个东西。原式在 `lab/probe/emit-trajectory.mjs`,那边有五处
 * 逐字重复的同一份实现。
 *
 * 全部走 `Math.imul` 与 `|0`/`>>>0`,是精确 32 位运算——**不要"优化"成看起来更
 * 干净的写法**,任何一处改动都会换掉整条序列。
 */
function mulberry32(seed: number): () => number {
  let s = seed
  return () => (
    (s = (s + 0x6d2b79f5) | 0),
    ((Math.imul(s ^ (s >>> 15), 1 | s) ^ (Math.imul(s ^ (s >>> 7), 61 | s) + s)) >>> 0) /
      4294967296
  )
}

/**
 * 生成一次「两次点击之间」的完整移动计划。
 *
 * 输入输出同在视口 CSS 坐标空间:传真实的起点与目标进去,出来的最后一点就落在
 * 目标上,**不需要任何平移**。
 */
export function planMove(input: MovePlanInput): MovePlan {
  const dx = input.to.x - input.from.x
  const dy = input.to.y - input.from.y
  const distPx = Math.hypot(dx, dy)

  // 两个独立种子,派生方式照抄上游:路由用 rr,其余用 r。rr 掺进距离,
  // 于是「同种子同距离」必然拿到同一条路由——差异才归因得到引擎头上。
  const r = mulberry32(input.seed * 7919)
  const rr = mulberry32(input.seed * 104729 + distPx)

  const raw: RoutePoint[] = UNITS.window(
    DEFAULT_ENGINE,
    input.from.x,
    input.from.y,
    input.to.x,
    input.to.y,
    r,
    rr,
    {
      ...ENGINE_PARAMS,
      targetW: input.targetW,
      maxDwellMs: input.maxDwellMs,
      ...(input.landmarks ? { landmarks: input.landmarks } : {}),
    },
    { ROUTES },
  )

  // 取整口径照抄上游的产出格式(x/y 三位、t 两位)。Go 侧无论如何要取整到 CSS
  // 整数才能 Snap,所以精度够用;照抄是为了让 hiBoss 的离线工具能直接吃我们的计划。
  const points: PlanPoint[] = raw.map((p) => ({
    x: Number(p.x.toFixed(3)),
    y: Number(p.y.toFixed(3)),
    t: Number(p.t.toFixed(2)),
  }))

  // **抽 pressMs 必须在生成轨迹之后**:它取的是 rr 这条流的下一个值,
  // 提前抽会把整条路由的随机序列错开一位。
  const pressMs = PRESS_MS[Math.floor(rr() * PRESS_MS.length)] ?? 0

  return { points, pressMs, engine: DEFAULT_ENGINE, seed: input.seed, distPx }
}
