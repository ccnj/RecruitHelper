// 本文件是**我们写的**类型声明,不是上游产物。同目录的 `.mjs` 一行不改,见 ../README.md。
//
// 它只声明我们实际调用的那一个导出。声明少不是偷懒:这份 `.d.mts` 就是我们与
// hiBoss 引擎之间的契约面,上游改了签名,编译期当场红——把没用到的也抄一遍,
// 反而会在无关改动上误报。

/** 视口 CSS 坐标下的一帧。与调用方传进去的坐标同一空间。 */
export interface RoutePoint {
  x: number
  y: number
  /** 相对本次生成起点的毫秒。 */
  t: number
}

export interface RouteConfig {
  /** 速度系数。取值必须与 hiBoss 判别器跑分时一致,见 ../plan.ts 的 ENGINE_PARAMS。 */
  spd: number
  /** 弧度系数。同上。 */
  arc: number
  /** 点击目标沿运动方向的有效宽度(CSS px)。不传上游用实测缺省 28。 */
  targetW?: number
  /** 截断单次停顿。池子里有真人离开数分钟的真实记录,播放方必须给。 */
  maxDwellMs?: number
  /** 可选的中途落脚点(页面元素位置, CSS px)。 */
  landmarks?: ReadonlyArray<{ x: number; y: number }>
}

/** 实测路由池。结构对我们不透明,原样转交。 */
export type RoutePools = { ROUTES: unknown }

export declare const UNITS: {
  /**
   * 两次点击之间的整个窗口:起始驻留或残余滑出 → 中途停顿 → 末次瞄准 → 落到目标 → 落点静止。
   *
   * `r` 与 `rr` 是两个**独立**的 0..1 随机源;`rr` 专供路由挑选。
   */
  window(
    engine: string,
    x0: number,
    y0: number,
    x1: number,
    y1: number,
    r: () => number,
    rr: () => number,
    cfg: RouteConfig,
    pools: RoutePools,
  ): RoutePoint[]
}
