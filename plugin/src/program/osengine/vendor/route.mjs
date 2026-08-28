// **可搬走的纯计算层。** 规矩跟 lab/engine 的其它子目录一样：
//   - 不 import `lab/` 之外的东西
//   - 不碰 OS、不碰文件系统、不碰坐标换算（输入输出全是**网页坐标 CSS px**）
//   - **不调 Date.now / setTimeout** —— 输出的是**数据**（带相对毫秒的点序列），
//     不是**过程**。一旦引擎里出现「等一下再走下一步」，它就搬不走、测不了、
//     也没法在 Go 侧复现。打字线的计划 JSON 就是这么设计的。
//   - 实测值池（路由、帧间隔）**从外部注入**，不在这里 import ——
//     那些是 `lab/probe/baseline/` 的生成文件，属于研究设备。
//
// # 生成单元：一个「窗口」是什么
//
// BOSS 量的是**上一次点击到这一次点击之间光标做的全部事情**，不是一段干净的 A→B。
// 这是这一季度最贵的一条认知：三个解不掉的特征（efficiency / accelerationCV /
// linearity）是同一个病，改对单元之后一起消失。
//
// # 一个窗口分成四件事，它们**机制不同，必须分开生成**
//
// 2026-08-26 把 817 个真人窗口的 `p6.x`/`p6.y`/`p6.z` 逐点量了一遍
// （`lab/probe/measure-route.mjs`），落脚点分成三类，各自的统计量完全不同：
//
//     起始驻留   115 个窗口（14%）  点完鼠标手就没动，停 333ms
//     中途停顿   1008 次   s 中位 0.71  |n| 0.21   停 333ms   ← 去别处
//     末次瞄准    576 次   s 中位 0.98  |n| 0.05   停 216ms   ← 离目标 ~30px
//
// **先前这里是一个机制在冒充三件事**：无条件先「残余滑出」，再沿主方向撒几个
// 法向偏移 0.30D~0.90D 的落脚点。对照实测：
//
//                 真人      旧公式
//     |n| 中位     0.12D     0.60D     差 4.9 倍
//     s   中位     0.85      0.36      真人停在快到目标处，旧公式停在中间
//     s < 0        14%       0%        产不出来
//     s > 1        29%       0%        产不出来
//
// 而 efficiency 正是我们跟真人差得最狠的一项 —— 偏 5 倍的法向绕路就是它的来源。
//
// # 落脚点该不该来自页面元素
//
// 原本的计划是「中途落脚点必须来自页面元素，否则有目的的绕路会退化成为了凑
// efficiency 的抖动」。**实测不支持这个前提**：中途落脚点跟普通移动帧量不出区别
// （离最近点击位置 18px vs 16px、格子集中度 23% vs 24%，两者都明显区别于均匀
// 随机的 28px / 32%）—— 落脚点「贴着元素」这件事，全部由「光标本来就在元素之间跑」
// 解释掉了。而 BOSS 的七个特征全是**路径形状统计量**，没有一个看你停在哪个元素上。
//
// 所以做法是：**留接口，不强制**。`cfg.landmarks` 传进来就用（生产环境从 DOM 读），
// 不传就从实测池抽。将来 BOSS 若加语义特征、或要照顾 hover 事件那条通道，
// 换的是调用方，不是这一层。

import { ENGINES, W_CLICK, W_WAYPOINT } from './engines.mjs'

// ── 第二层：生成单元 ──────────────────────────────────────────────────

/**
 * 点击后的残余滑出：从 v0 减速到 0。
 * **它和起始驻留互斥** —— 真人要么点完手就没动（14%），要么带着余速滑出去。
 */
function glideOut(x, y, r) {
  const out = []
  const n = 4 + Math.floor(r() * 6)
  let v = 8 + r() * 10, t = 0
  const a = r() * Math.PI * 2, ux = Math.cos(a), uy = Math.sin(a)
  for (let i = 0; i < n; i++) {
    v *= 0.82 + r() * 0.12
    x += ux * v + (r() * 2 - 1); y += uy * v + (r() * 2 - 1); t += 16
    out.push({ x, y, t })
  }
  return out
}

/**
 * 一次停顿。**不是一段静默** —— 真人手还搭在鼠标上，微颤把光标推过整数边界，
 * 浏览器就派发一次 mousemove。所以停顿期**有样本**，只是稀疏、位移小。
 *
 * 脉冲直接用**这个真人停顿自己的** `[等多久, 漂多远]` 序列，不再从全局池独立抽 ——
 * 一次停顿里几个脉冲是相关的（同一只手同一段时间）。
 *
 * `g >= 500` 的那些位移中位 14~102px，**那不是抖动，是「离开很久再回来」**，
 * 由路由负责，这里只推进时间；随机方向漂 100px 会凭空造出真人没有的绕路。
 */
function holdAt(pulses, st, out, rr, cap) {
  for (const [g0, d] of pulses) {
    const g = Math.min(g0, cap)
    st.t += g
    if (g < 500 && d > 0) {
      const a = rr() * 2 * Math.PI
      st.x += d * Math.cos(a); st.y += d * Math.sin(a)
      out.push({ x: st.x, y: st.y, t: st.t })
    }
  }
}

/**
 * 走一段：把段内引擎的相对时刻接到窗口时间轴上。
 *
 * `W` 是**这一段瞄的东西有多宽**，Fitts 定律的另一半（见 `engines.mjs` 的 W_CLICK /
 * W_WAYPOINT）。它是每段一个值，不是每次播放一个值，所以从这儿逐段注进 cfg，
 * 而不是搁在调用方的 cfg 里。**必传**：漏了就是 NaN，当场红。
 */
function leg(eng, st, tx, ty, r, cfg, out, W) {
  const seg = ENGINES[eng](st.x, st.y, tx, ty, r, { ...cfg, W })
  for (const p of seg) out.push({ x: p.x, y: p.y, t: st.t + p.t })
  const L = seg.at(-1)
  if (L) { st.x = L.x; st.y = L.y; st.t += L.t }
}

/**
 * 从池里抽一条**距离相近**的真人路由。
 *
 * 为什么按距离分档，而不是归一化了一把抓：实测两种标度**都不成立**。
 *
 *     D 档        |n| 归一化中位   |n| 绝对中位
 *     [ 20, 120)      0.73            44px
 *     [120, 300)      0.21            44px
 *     [300, 600)      0.18            72px
 *     [600,1200)      0.14           109px
 *
 * 归一化的在缩、绝对的在涨。硬选一个就是在拟合一条数据不支持的标度律。
 * 而且不分档还会出事：D=142 那条路由的 s 到 −6.17（小 D 让归一化炸开），
 * 套到 D=900 上就是 5500px 开外的落脚点。
 */
function pickRoute(ROUTES, D, rr) {
  for (const k of [2, 3, 6, Infinity]) {
    const band = ROUTES.filter((e) => e[0] >= D / k && e[0] <= D * k)
    if (band.length >= 40 || k === Infinity) return band[Math.floor(rr() * band.length)]
  }
}

export const UNITS = {
  /** 对照组：移到目标就结束。**这是错的单元**，留着是为了让差异可见。 */
  stroke: (eng, x0, y0, x1, y1, r, _rr, cfg) =>
    ENGINES[eng](x0, y0, x1, y1, r, { ...cfg, W: cfg.targetW ?? W_CLICK }),

  /**
   * 两次点击之间的整个窗口。
   *
   *   起始驻留（14%）**或**残余滑出 → 中途停顿×k → 末次瞄准（70%）→ 修正到目标 → 落点静止
   *
   * 路由用**独立的种子** `rr`，所以同一 (距离, 种子) 下**所有引擎拿到完全一样的一条路由**，
   * 差异才归因得到引擎头上。
   *
   * `pools.ROUTES` 是实测路由池（`baseline/route.*.mjs`）。
   * `cfg.landmarks` 是可选的页面元素位置（CSS px），传了就用它当中途落脚点。
   * `cfg.targetW` 是点击目标的有效宽度（CSS px，沿运动方向）。**不传就用实测缺省 28**。
   *   注意：我们拟合出来的是一个**标量** 28，从没见过那些点击目标的矩形 —— 所以
   *   「矩形该换算成 `min(w,h)` 还是沿运动方向的投影」**没有依据**，由调用方定。
   * `cfg.maxDwellMs` 截断单次脉冲 —— 池子里有真人离开 5 分钟的真实记录，
   * 拿去真机播放的调用方需要它，评分不需要。
   */
  window: (eng, x0, y0, x1, y1, r, rr, cfg, pools) => {
    const dx = x1 - x0, dy = y1 - y0, D = Math.hypot(dx, dy) || 1
    const ux = dx / D, uy = dy / D, nx = -uy, ny = ux
    const [, init, mids, aim] = pickRoute(pools.ROUTES, D, rr)
    const cap = cfg.maxDwellMs ?? Infinity

    const out = []
    const st = { x: x0, y: y0, t: 0 }
    if (init) {
      holdAt(init, st, out, rr, cap)
    } else {
      for (const p of glideOut(x0, y0, r)) out.push(p)
      const L = out.at(-1)
      if (L) { st.x = L.x; st.y = L.y; st.t = L.t }
    }

    const lm = cfg.landmarks
    mids.forEach(([s, n, pulses], j) => {
      const w = (lm && lm[j]) || { x: x0 + D * (s * ux + n * nx), y: y0 + D * (s * uy + n * ny) }
      leg(eng, st, w.x, w.y, r, cfg, out, W_WAYPOINT)
      holdAt(pulses, st, out, rr, cap)
    })

    // 末次瞄准：停在离目标 (along, normal) 处，再走完最后那一小段。
    // **along > 0 是冲过头**（实测 36%），负的是没到位就停。
    if (aim) {
      const [al, nm, pulses] = aim
      // 末次瞄准也是走向一个**停顿**，不是走向按钮 —— 所以按落脚点算宽。
      // 这是判断不是实测：§33 的分群是「直达窗口 vs 犹豫窗口」，没有按停顿类型切过。
      leg(eng, st, x1 + al * ux + nm * nx, y1 + al * uy + nm * ny, r, cfg, out, W_WAYPOINT)
      holdAt(pulses, st, out, rr, cap)
    }
    // 只有这一段真的要落在点击目标上。`cfg.targetW` 来自调用方读到的元素矩形。
    leg(eng, st, x1, y1, r, cfg, out, cfg.targetW ?? W_CLICK)

    for (let i = 0, n = 1 + Math.floor(r() * 3); i < n; i++) {
      st.t += 16; out.push({ x: st.x, y: st.y, t: st.t })   // 落点静止 → v_last = 0
    }
    return out
  },
}
