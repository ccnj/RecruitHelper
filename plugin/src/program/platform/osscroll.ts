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
