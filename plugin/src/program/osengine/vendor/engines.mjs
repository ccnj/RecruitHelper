// **可搬走的纯计算层。** 规矩跟 lab/engine 的其它子目录一样：
//   - 不 import `lab/` 之外的东西
//   - 不碰 OS、不碰文件系统、不碰坐标换算（输入输出全是**网页坐标 CSS px**）
//   - **不调 Date.now / setTimeout** —— 输出的是**数据**（带相对毫秒的点序列），
//     不是**过程**。一旦引擎里出现「等一下再走下一步」，它就搬不走、测不了、
//     也没法在 Go 侧复现。打字线的计划 JSON 就是这么设计的。
//   - 实测值池（帧间隔、段结构）**从外部注入**，不在这里 import ——
//     那些是 `lab/probe/baseline/` 的生成文件，属于研究设备。
//
// # 段内引擎：相邻两点之间怎么走
//
// 签名 `(x0, y0, x1, y1, rng, cfg) → [{x, y, t}]`，`t` 是相对毫秒（从 0 起）。
// 纯函数，不需要任何实测池。换引擎 = 换一个字符串。

// ── 第一层：段内引擎 ──────────────────────────────────────────────────
// 签名 (x0,y0,x1,y1,r,cfg) → [{x,y,t}]，t 是发出时刻（ms，从 0 起）。
// `cfg.spd` 是时长倒数倍率，`cfg.arc` 是空间路径的法向弧幅。

const smootherstep = (t) => t * t * t * (t * (t * 6 - 15) + 10)
const bezier3 = (p0, p1, p2, p3, t) => {
  const u = 1 - t, a = u*u*u, b = 3*u*u*t, c = 3*u*t*t, d = t*t*t
  return { x: a*p0.x + b*p1.x + c*p2.x + d*p3.x, y: a*p0.y + b*p1.y + c*p2.y + d*p3.y }
}
/**
 * 目标宽度 W —— Fitts 定律的另一半。**两个值都是实测的**（`report/runtime-evidence.md` §33）。
 *
 * 落到点击处的段要**瞄准**，去中途落脚点的段只要**甩到大概那儿**。同一组运动常数
 * 配两个 W，同时拟合两条曲线：
 *
 *     时长(ms) = 80 + 115 × log2(2D/W + 1)     R² = 0.916
 *     对照 · 只许一个 W                         R² = 0.668，且 W 被推到边界 4px
 *                                              （参数顶到边界 = 模型设定错了的征兆）
 *
 * **这是建模不是重放**：一个参数解释掉两个段群的全部差异，斜率 115ms/bit 还落在
 * Fitts 文献区间内。
 */
/** 点击目标。BOSS 的按钮/列表项就是这个尺寸。**调用方给了元素矩形就该用矩形的**，这只是缺省。 */
export const W_CLICK = 28
/**
 * 中途落脚点。**它不是页面元素** —— §32 用三组对照证伪过这件事（中途停顿离最近可点
 * 元素 18px，普通移动帧 16px，均匀随机 28px：停顿跟普通帧没区别，两者都明显不同于随机）。
 * 所以这儿没有矩形可读，96.5 就是它本身。
 */
export const W_WAYPOINT = 96.5

/**
 * 段时长。**常数是实测的，不是 Fitts 原文的。**
 *
 * 817 个真人窗口切出 1803 个移动段。**先前这里只有一个 W、写死 24**，于是「瞄准点击
 * 目标」和「甩到中途落脚点」两种段被迫共用一条曲线 —— 拟合把 W 顶到边界 4px，
 * R² 只有 0.668。分成两个 W 之后 R² = 0.916，常数也跟着换成 (80, 115)。
 *
 * 再往前一版是 `200 + 100 × …` —— 斜率几乎对，**截距是编的，差 239ms**；而调用方拿
 * `spd = 4` 去全局补偿它，代价是把斜率也一起缩了 4 倍。一个编的常数配一个补偿它的
 * 旋钮，两边都对不上，而且互相掩盖。这一条留着当教训。
 *
 * **W 必须由调用方按落脚点性质给，这里不猜、不设缺省。** 漏传就是 NaN，闸门当场红 ——
 * 比悄悄用一个缺省值响。
 *
 * 没有下限保护：`log2(2D/W + 1) ≥ 0` 恒成立，最小值就是截距 80ms。先前那个
 * `Math.max(50, …)` 是给负截距准备的，现在永远不会生效，删掉。
 */
const fitts = (D, spd, W) => (80 + 115 * Math.log2(2 * D / W + 1)) / spd
/**
 * 还要补几发校正。**先前是固定 2~3 发 —— 那是编的，而且是段时长偏长的主因。**
 *
 * 真正决定发数的是「打进去了没有」：主运动欠冲 8~18%，每发校正吃掉剩余误差的
 * 70~90%，落进目标（误差 < W/2）就收手。**这条规则不引入任何新常数** —— 欠冲比例、
 * 校正增益、W 全是已有的量。
 *
 * 它补上的正是实测里缺的那一头：D=50 瞄 28px 的按钮，主运动的欠冲只有 6.5px，
 * 已经落在目标里了 —— **一发都不用补**。而先前无条件补 2~3 发，光固定开销
 * （每发 30~80ms 视动延迟 + 60~130ms 行程）就 375ms，**比真人整段还长**（229ms）。
 *
 * 上限 4 发是防跑飞的护栏，不是模型的一部分：gain ≥ 0.7 时误差每发缩 5 倍以上，
 * 正常参数下第 4 发之前必定收敛。
 */
const corrections = (err, W, gain = 0.8) => {
  let e = err, n = 0
  while (e > W / 2 && n < 4) { e *= 1 - gain; n++ }
  return n
}

/** 二次贝塞尔的控制点：中点 + 法向偏移。真人手臂划过去是弧不是直线。 */
const arcCtrl = (x0, y0, x1, y1, r, arc) => {
  const dx = x1-x0, dy = y1-y0, D = Math.hypot(dx, dy) || 1
  const off = D * arc * (r() < 0.5 ? -1 : 1) * (0.6 + r() * 0.8)
  return { x: x0 + dx*0.5 - dy/D*off, y: y0 + dy*0.5 + dx/D*off }
}

export const ENGINES = {
  /**
   * `testBoss/osclick-demo/input.go:29-110` 的逐行复刻。**历史对照组，已否决。**
   * 保留是因为它是本项目第一个候选，用来说明「随机性投错维度」长什么样。
   */
  bezier: (x0, y0, x1, y1, r) => {
    const path = (ax, ay, bx, by) => {
      const dx = bx-ax, dy = by-ay, dist = Math.hypot(dx, dy)
      if (dist < 1) return [{ x: bx, y: by }]
      const nx = -dy/dist, ny = dx/dist
      let amp = Math.min(dist * 0.12, 90) * (r() * 0.8 + 0.3)
      if (r() < 0.5) amp = -amp
      const p0 = { x: ax, y: ay }, p3 = { x: bx, y: by }
      const p1 = { x: ax + dx*0.28 + nx*amp,     y: ay + dy*0.28 + ny*amp }
      const p2 = { x: ax + dx*0.68 + nx*amp*0.6, y: ay + dy*0.68 + ny*amp*0.6 }
      const steps = Math.floor(Math.max(14, Math.min(dist / 7, 90)))
      return Array.from({ length: steps + 1 }, (_, i) => bezier3(p0, p1, p2, p3, smootherstep(i / steps)))
    }
    const dist = Math.hypot(x1-x0, y1-y0), legs = []
    if (dist > 160) {
      const ux = (x1-x0)/dist, uy = (y1-y0)/dist, over = 4 + r()*9
      const ox = x1 + ux*over + (r()-0.5)*6, oy = y1 + uy*over + (r()-0.5)*6
      legs.push(path(x0, y0, ox, oy), path(ox, oy, x1, y1))
    } else legs.push(path(x0, y0, x1, y1))
    const out = []; let t = 0
    legs.forEach((leg, li) => {
      for (const p of leg) { out.push({ ...p, t }); t += 6 + r() * 8 }
      if (li === 0 && legs.length > 1) t += 25 + Math.floor(r() * 45)
    })
    return out
  },

  /**
   * Flash & Hogan (1985) 最小急动度，**单段直线**。
   * 五次多项式 `s(u)=10u³−15u⁴+6u⁵`，速度 `v(u)=30u²(1−u)²` 是钟形。
   * `report/detection-math.md:916-935` 算过它的精确矩：**CV = √(3/7) = 0.654654**，
   * 而判定是 `speedCV < 0.7 → 亮灯` —— 差 0.045。
   */
  minjerk: (x0, y0, x1, y1, r, cfg) => {
    const T = fitts(Math.hypot(x1-x0, y1-y0), cfg.spd, cfg.W), dt = 1000/250, out = []
    for (let t = 0; t <= T; t += dt) {
      const u = t/T, s = 10*u**3 - 15*u**4 + 6*u**5
      out.push({ x: x0 + (x1-x0)*s, y: y0 + (y1-y0)*s, t })
    }
    return out
  },

  /** 同一套时间律，**空间路径换成弧**（二次贝塞尔）。 */
  'minjerk-arc': (x0, y0, x1, y1, r, cfg) => {
    const c = arcCtrl(x0, y0, x1, y1, r, cfg.arc)
    const T = fitts(Math.hypot(x1-x0, y1-y0), cfg.spd, cfg.W), dt = 1000/250, out = []
    for (let t = 0; t <= T; t += dt) {
      const u = t/T, s = 10*u**3 - 15*u**4 + 6*u**5, m = 1-s
      out.push({ x: m*m*x0 + 2*m*s*c.x + s*s*x1, y: m*m*y0 + 2*m*s*c.y + s*s*y1, t })
    }
    return out
  },

  /**
   * Meyer 等的 optimized-submovement：**初级弹道段欠冲 + 若干校正性子运动**。
   * `detection-math.md:955` 提过这个模型会让速度剖面出现多个峰 —— 而
   * `speedCV` 与 `autocorrelationLag1` 要的正是这个，单段光滑曲线给不了。
   */
  'minjerk-sub': (x0, y0, x1, y1, r, cfg) => {
    const out = []; let cx = x0, cy = y0, t = 0
    const dt = 1000/250
    const stroke = (tx, ty, T) => {
      for (let k = 0; k <= T; k += dt) {
        const u = k/T, s = 10*u**3 - 15*u**4 + 6*u**5
        out.push({ x: cx + (tx-cx)*s, y: cy + (ty-cy)*s, t: t + k })
      }
      t += T; cx = tx; cy = ty
    }
    const D = Math.hypot(x1-x0, y1-y0)
    const frac = 0.82 + r() * 0.10                      // 初级段欠冲 8~18%
    const T0 = (150 + 70*Math.log2(2*D/cfg.W + 1)) / cfg.spd
    const n = corrections(D * (1 - frac), cfg.W)        // 发数是推出来的，见 corrections
    if (n === 0) {
      // 目标够宽 / 距离够近，欠冲量本身就落在目标里 —— **一发直达，不补**。
      stroke(x1, y1, T0)
    } else {
      const ang = (r() - 0.5) * 0.12                    // 并带方向误差
      const ca = Math.cos(ang), sa = Math.sin(ang)
      const vx = (x1-x0)*frac, vy = (y1-y0)*frac
      stroke(x0 + vx*ca - vy*sa, y0 + vx*sa + vy*ca, T0)
      for (let i = 0; i < n; i++) {
        t += 30 + r() * 50                              // 视觉反馈回路延迟
        const k = i === n - 1 ? 1 : 0.7 + r() * 0.2     // 每段吃掉剩余误差的 70~90%
        stroke(cx + (x1-cx)*k, cy + (y1-cy)*k, (60 + r() * 70) / cfg.spd)
      }
    }
    return out
  },

  /**
   * JDmover 的径向 PD（`jd-video-server/pkg/autobot/utils.go`）原样：
   * Kp=6 Kd=2 Ki=0，误差压成标量 `dist`、输出速度沿误差单位向量、
   * `speedCmd` 下夹 0 上夹 1400、`step ≤ dist−1` 显式禁止冲过目标。
   *
   * 把 `s' = Kp·d' − Kd·s` 线性化，递推是
   * `d[n+1] = (1 − Kp·DT − Kd)·d[n] + Kd·d[n−1]`，特征值 **0.976 / −2.048**。
   * `|−2.048| > 1`，离散域发散；不炸只是因为被 `≥0` 和 `maxV` 两个夹子按住，
   * 于是塌成周期 2 的 bang-bang 极限环。
   */
  'pd-unstable': (x0, y0, x1, y1, r, cfg) => pdRadial(x0, y0, x1, y1, r, 6 * cfg.spd, 2),
  /** 同结构、增益换到稳定区：特征值 **0.787 / −0.635**，都在单位圆内。 */
  'pd-stable': (x0, y0, x1, y1, r, cfg) => pdRadial(x0, y0, x1, y1, r, 29 * cfg.spd, 0.5),

  /**
   * PD **追一个沿弧移动的设定点**，去掉整数取整与 1px 兜底。
   * 这是闭环相对开环唯一的结构性优势：设定点可以是任意曲线，
   * 光标跟随时天然产生滞后与切角，不用手工设计校正亚运动。
   */
  'pd-chase': (x0, y0, x1, y1, r, cfg) => {
    const c = arcCtrl(x0, y0, x1, y1, r, cfg.arc)
    const T = fitts(Math.hypot(x1-x0, y1-y0), cfg.spd, cfg.W), DT = 12
    const Kp = 29 * cfg.spd, Kd = 0.5, MAXV = 4000
    let px = x0, py = y0, prev = -1, t = 0
    const out = [{ x: px, y: py, t }]
    for (let k = 0; k < 2000; k++) {
      const u = Math.min(1, (t + DT) / T), s = 10*u**3 - 15*u**4 + 6*u**5, m = 1-s
      const sx = m*m*x0 + 2*m*s*c.x + s*s*x1, sy = m*m*y0 + 2*m*s*c.y + s*s*y1
      const ex = sx - px, ey = sy - py, dist = Math.hypot(ex, ey)
      if (u >= 1 && dist <= 1) break
      t += DT
      if (dist < 1e-6) { out.push({ x: px, y: py, t }); continue }
      const dd = prev >= 0 ? (dist - prev) / (DT / 1000) : 0
      prev = dist
      const speed = Math.max(0, Math.min(MAXV, Kp * dist + Kd * dd))
      const step = Math.min(speed * DT / 1000, dist)
      px += ex / dist * step; py += ey / dist * step
      out.push({ x: px, y: py, t })
    }
    return out
  },

  /**
   * **力驱动 + 生理性震颤 + 延迟视觉反馈** —— 运动控制文献里的标准结构。
   *
   * 前面几个引擎都是**运动学**的：直接规定位置怎么走，没有质量、没有惯性、
   * 速度可以瞬变。真实的手推鼠标是**二阶系统**：手施力，鼠标有质量和摩擦。
   *
   *   plant     a = (F − c·v) / m        时间常数 m/c ≈ 60ms
   *   tremor    F += 8~12Hz 带限噪声      生理性震颤，指尖幅度换算到屏幕是亚像素~1px
   *   主运动    **前馈**，欠冲到 ~90%     太快了，反馈来不及；欠冲是学来的策略
   *   校正段    **延迟 PD**，τ = 100ms   眼睛看到的偏差是 τ 毫秒前的
   *
   * # 为什么主运动必须是前馈
   *
   * 纯粹的「延迟 PD」会**冲过头**：看到的误差是 τ 前的、已经偏大，于是继续推。
   * 而真人主运动是**欠冲**的 —— 那是学来的前馈策略，专门避开冲过头之后昂贵的回修。
   * 拿延迟 PD 去做主运动，符号就反了。
   *
   * # 震颤为什么不能用白噪声
   *
   * 生理性震颤是**带限**的（8~12Hz），不是白的。`autocorrelationLag1` 是六盏灯之一，
   * 而它量的正是相邻帧速度的相关性 —— 白噪声和带限噪声在这一项上完全不同。
   */
  'force-pd': (x0, y0, x1, y1, r, cfg) => {
    const DT = 1, EMIT = 8                      // 1ms 积分，125Hz 发点
    const M = 1, C = 1 / 60                     // 质量与粘滞阻尼，时间常数 60ms
    const D0 = Math.hypot(x1 - x0, y1 - y0) || 1

    // 生理性震颤：8~12Hz 的正弦叠加。**带限，不是白噪声。**
    const tre = Array.from({ length: 6 }, () => ({
      w: 2 * Math.PI * (8 + r() * 4) / 1000, ph: r() * 2 * Math.PI, a: 0.00025 + r() * 0.00025 }))
    const tremor = (t, k) => tre.slice(k * 3, k * 3 + 3).reduce((s, o) => s + o.a * Math.sin(o.w * t + o.ph), 0)

    let px = x0, py = y0, vx = 0, vy = 0, t = 0
    const out = [{ x: px, y: py, t }]

    /** 一发开环弹道：按 min-jerk 的期望轨迹反解所需的力，整段不看反馈。 */
    const ballistic = (tx, ty, T) => {
      const dx = tx - px, dy = ty - py
      for (let k = 0; k <= T; k += DT) {
        const u = k / T
        const vs = 30 * u * u * (1 - u) * (1 - u) / T
        const as = 30 * (2 * u - 6 * u * u + 4 * u ** 3) / (T * T)
        let fx = M * dx * as + C * dx * vs + tremor(t, 0)
        let fy = M * dy * as + C * dy * vs + tremor(t, 1)
        vx += (fx - C * vx) / M * DT; vy += (fy - C * vy) / M * DT
        px += vx * DT; py += vy * DT
        t += DT
        if (t % EMIT === 0) out.push({ x: px, y: py, t })
      }
    }
    /** 延迟窗口：眼睛还没看到刚才落在哪。手不是静止的 —— 震颤照旧、速度按阻尼衰减。 */
    const wait = (ms) => {
      for (let k = 0; k < ms; k += DT) {
        vx += (tremor(t, 0) - C * vx) / M * DT; vy += (tremor(t, 1) - C * vy) / M * DT
        px += vx * DT; py += vy * DT
        t += DT
        if (t % EMIT === 0) out.push({ x: px, y: py, t })
      }
    }

    // ── 主运动：前馈，欠冲 8~18%，带方向误差 ──────────────────────────
    const frac = 0.82 + r() * 0.10
    const ang = (r() - 0.5) * 0.12, ca = Math.cos(ang), sa = Math.sin(ang)
    const vx0 = (x1 - x0) * frac, vy0 = (y1 - y0) * frac
    ballistic(x0 + vx0 * ca - vy0 * sa, y0 + vx0 * sa + vy0 * ca,
      Math.max(40, (150 + 70 * Math.log2(2 * D0 / cfg.W + 1)) / cfg.spd))

    // ── 校正：离散的，不是连续的 ──────────────────────────────────────
    //
    // **这一段的结构是被延迟逼出来的，不是选出来的。**
    // 第一版写成连续的延迟 PD，当场数值爆炸（speedMean 到 1e64）：
    // 闭环时间常数 64ms 比视动延迟 100ms 还短 —— 经典的延迟致振。
    //
    // 所以人只能离散地做：**看一眼（延迟 τ 之后才看到）→ 开环打一发 → 再看一眼**。
    // Meyer 的亚运动模型不是随便设的结构，是延迟逼出来的唯一稳定解。
    for (let i = 0; i < 4; i++) {
      const TAU = 90 + r() * 60                 // 视动延迟，看到的是 τ 毫秒前的自己
      wait(TAU)
      const ex = x1 - px, ey = y1 - py, e = Math.hypot(ex, ey)
      if (e < cfg.W / 2) break            // 收手门限 = 落进目标，跟 corrections 同一条规则
      const gain = i === 3 ? 1 : 0.75 + r() * 0.2   // 每发吃掉误差的 75~95%
      ballistic(px + ex * gain, py + ey * gain,
        Math.max(30, (60 + 50 * Math.log2(2 * e / cfg.W + 1)) / cfg.spd))
    }
    return out
  },

  /** 对照组：匀速直线插值 —— 最朴素的做法。 */
  linear: (x0, y0, x1, y1) => {
    const n = Math.max(2, Math.round(Math.hypot(x1-x0, y1-y0) / 7))
    return Array.from({ length: n + 1 }, (_, i) =>
      ({ x: x0 + (x1-x0)*i/n, y: y0 + (y1-y0)*i/n, t: i * 10 }))
  },
  /** 对照组：瞬移。真机实测 za=66.0。 */
  teleport: (x0, y0, x1, y1) => [{ x: x1, y: y1, t: 0 }],
}

/**
 * 径向 PD 的公共实现。**那句防卡死兜底是三个 PD 里两个的死因**：
 * 接近目标时 `round()` 归零 → 强制 1px → 拖出 20~40 帧完美笔直的逐像素运动，
 * 正好撞在 `linearity` 的 `R² > 0.95` 上。改增益救不了
 * （特征值从 −2.048 换到 −0.635，linearity 只从 24/25 动到 19/25）。
 */
function pdRadial(x0, y0, x1, y1, r, Kp, Kd) {
  const MAXV = 1400, TOL = 1, DT = 0.012
  let px = Math.round(x0), py = Math.round(y0), prev = -1, t = 0
  const out = [{ x: px, y: py, t }]
  for (let i = 0; i < 4000; i++) {
    const ex = x1 - px, ey = y1 - py, dist = Math.hypot(ex, ey)
    if (dist <= TOL) break
    const ux = ex / dist, uy = ey / dist
    const dd = prev >= 0 ? (dist - prev) / DT : 0
    prev = dist
    const speed = Math.max(0, Math.min(MAXV, Kp * dist + Kd * dd))
    const step = Math.min(speed * DT, dist - TOL)
    let jx = 0, jy = 0
    if (r() < 0.35) {
      const amp = 1.2 * Math.min(1, dist / 20), j = (r() * 2 - 1) * amp
      jx = -uy * j; jy = ux * j
    }
    let dx = Math.round(ux * step + jx), dy = Math.round(uy * step + jy)
    if (dx === 0 && dy === 0) {                          // 防卡死：强制沿主轴 1px
      if (Math.abs(ex) >= Math.abs(ey)) dx = Math.sign(ex) || 1; else dy = Math.sign(ey) || 1
    }
    px += dx; py += dy; t += 12
    out.push({ x: px, y: py, t })
  }
  return out
}
/**
 * **当前选定的生成器。** 不带参数时所有入口都用它。
 *
 * # 为什么要有这个常量
 *
 * 先前每个调用方各写各的默认：`emit-trajectory.mjs` 是 `minjerk-sub`、
 * `discriminate.mjs` 是 `minjerk-arc`、`measure-route.mjs` 写死 `minjerk-arc`，
 * 两个 Go 文件的示例命令还各写一个。**同一件事有四份，而且互相矛盾。**
 *
 * 后果不是「不一致」这种抽象问题：**生成计划的入口跑的引擎，跟量「像不像人」的
 * 入口跑的引擎不是同一个**。2026-08-27 上 Windows 真机时我每次都显式传了
 * `minjerk-arc` 所以没踩到 —— 而那纯属运气。将来搬到生产若照默认值走，
 * 跑的就不是验过的那个。
 *
 * # 为什么是 minjerk-arc
 *
 * 两个目标宽度接进 `fitts()`（§34）之后，它在**两把尺子上同时最好**：
 *
 *     引擎           AUC（分不分得出来）   灯差（BOSS 的灯）
 *     minjerk-arc         0.862  ←最好         0.51  ←最好
 *     minjerk-sub         0.867                0.59
 *     minjerk             0.877                0.55
 *     bezier              0.912                0.72
 *
 * 改 W 之前两把尺子指向不同的引擎（arc 的 AUC 0.912 最差、灯差 0.43 最好），
 * 改完才合到一处。
 *
 * # ⚠ 它不是「通过验收的」，是「矮子里的高个」
 *
 * `check.sh` 第 5 段：**八个候选一个都没过双验收线。** 这个常量只回答
 * 「现在暂时用哪个」，不回答「够不够好」。
 *
 * **改这个值要重跑 `check.sh` 的第 4、5 段**（判别器与验收），因为上面那张表
 * 是它们算出来的；改完把新表抄进这段注释，别只改字符串。
 */
export const DEFAULT_ENGINE = 'minjerk-arc'
