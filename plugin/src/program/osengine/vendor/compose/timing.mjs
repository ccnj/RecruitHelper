// 时序采样。混合对数正态 —— 单个对数正态匹配不了真人。
//
// 真人 dwell 实测 CV=0.628 但 IQR/μ 只有 0.376（lab/calibrate/baseline）：
// 主体窄、尾巴长，是重尾形状。单个对数正态的这两个量被 σ 绑死，
// 想要 CV=0.628 就得 σ≈0.576，那时 IQR/μ 会到 0.675 —— 中段比真人散得多。
// 混合两个成分（主体 + 偶发长按）才能同时贴上两个矩。
//
// 判据只看 quorble = clamp(1 − CV − IQR/μ, 0, 1)，所以这两个矩就是全部目标；
// 更高阶的形状 BOSS 看不见（全部判据都是经验分布的泛函，见 detection-math §6.3）。

/** 确定性伪随机 —— 标定必须可复现，禁止 Math.random */
export function makeRng(seed = 1) {
  let s = seed >>> 0 || 1
  const rnd = () => {
    s = (s * 1103515245 + 12345) & 0x7fffffff
    return s / 0x7fffffff
  }
  const normal = () => {
    let u = 0, v = 0
    while (u === 0) u = rnd()
    while (v === 0) v = rnd()
    return Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v)
  }
  const logn = (median, sigma) => Math.exp(Math.log(median) + sigma * normal())
  return { rnd, normal, logn }
}

/** @param {Array<{w, median, sigma}>} mix 权重不必归一 */
export function sampleMix(rng, mix) {
  const total = mix.reduce((a, c) => a + c.w, 0)
  let r = rng.rnd() * total
  for (const c of mix) {
    r -= c.w
    if (r <= 0) return rng.logn(c.median, c.sigma)
  }
  return rng.logn(mix[mix.length - 1].median, mix[mix.length - 1].sigma)
}

/**
 * 标准正态的上尾概率 Q(z) = P(Z ≥ z)。走 erfc 的 Chebyshev 逼近（NR erfcc），
 * **相对**误差 < 1.2e-7 —— 尾部也准，所以能拿来算「某成分落在 [lo, ∞) 的质量」。
 * 写成 1 − Φ(z) 的形式在 z 大时会被抵消成 0，这里刻意不那么写。
 */
function normTail(z) {
  const x = z / Math.SQRT2
  const t = 1 / (1 + 0.5 * Math.abs(x))
  const e = t * Math.exp(-x * x - 1.26551223 + t * (1.00002368 + t * (0.37409196 + t * (0.09678418 +
    t * (-0.18628806 + t * (0.27886807 + t * (-1.13520398 + t * (1.48851587 +
    t * (-0.82215223 + t * 0.17087277)))))))))
  return 0.5 * (x >= 0 ? e : 2 - e)
}

/** 标准正态分位数 Φ⁻¹(p)。Acklam 有理逼近，相对误差 ~1e-9，两侧尾部单独处理。 */
function normInv(p) {
  const a = [-3.969683028665376e1, 2.209460984245205e2, -2.759285104469687e2, 1.38357751867269e2, -3.066479806614716e1, 2.506628277459239]
  const b = [-5.447609879822406e1, 1.615858368580409e2, -1.556989798598866e2, 6.680131188771972e1, -1.328068155288572e1]
  const c = [-7.784894002430293e-3, -3.223964580411365e-1, -2.400758277161838, -2.549732539343734, 4.374664141464968, 2.938163982698783]
  const d = [7.784695709041462e-3, 3.224671290700398e-1, 2.445134137142996, 3.754408661907416]
  const lo = 0.02425
  if (p < lo) {
    const q = Math.sqrt(-2 * Math.log(p))
    return (((((c[0] * q + c[1]) * q + c[2]) * q + c[3]) * q + c[4]) * q + c[5]) /
      ((((d[0] * q + d[1]) * q + d[2]) * q + d[3]) * q + 1)
  }
  if (p > 1 - lo) {
    const q = Math.sqrt(-2 * Math.log(1 - p))
    return -(((((c[0] * q + c[1]) * q + c[2]) * q + c[3]) * q + c[4]) * q + c[5]) /
      ((((d[0] * q + d[1]) * q + d[2]) * q + d[3]) * q + 1)
  }
  const q = p - 0.5
  const r = q * q
  return (((((a[0] * r + a[1]) * r + a[2]) * r + a[3]) * r + a[4]) * r + a[5]) * q /
    (((((b[0] * r + b[1]) * r + b[2]) * r + b[3]) * r + b[4]) * r + 1)
}

/**
 * 从混合对数正态里采一个 **≥ lo** 的值：条件分布 X | X ≥ lo。
 *
 * 与「反复 sampleMix 直到 ≥ lo」**同分布** —— 拒绝采样得到的就是这个条件分布，
 * 区别只在成本：拒绝采样的期望次数是 1/P(X ≥ lo)，lo 在尾巴上时成千上万次，
 * 于是实现里只能设一个次数上限，上限一到就漏出一个不满足约束的样本。
 * 这里按成分尾质量选成分、再在该成分的上尾里按均匀分位反查，一步到位、
 * 没有次数、不会漏。对数正态右尾无界，所以任何有限的 lo 都采得出来。
 *
 * lo ≤ 0 时就是无条件的 sampleMix（消耗的随机数也相同）。
 */
export function sampleMixAbove(rng, mix, lo) {
  if (!(lo > 0)) return sampleMix(rng, mix)
  const L = Math.log(lo)
  // 各成分落在 [lo, ∞) 的质量 w_i · P(X_i ≥ lo)，标准化后 z_i = (ln lo − ln median_i) / σ_i
  const parts = mix.map((c) => {
    const z = (L - Math.log(c.median)) / c.sigma
    return { c, z, q: normTail(z), m: c.w * normTail(z) }
  })
  const total = parts.reduce((a, x) => a + x.m, 0)
  // 全部成分都在 lo 的 8σ 之外才会到这里（double 的尾部已归零）—— 不是分布该覆盖的位置，
  // 退化成下限本身；这么远的 lo 意味着上游已经排出了物理上说不通的时序，会被 localCheck 拦下。
  if (!(total > 0)) return lo
  let r = rng.rnd() * total
  let pick = parts[parts.length - 1]
  for (const x of parts) {
    r -= x.m
    if (r <= 0) { pick = x; break }
  }
  // 在该成分的上尾 (0, q] 上均匀取分位：p 是上尾概率，z = Q⁻¹(p) = −Φ⁻¹(p)
  let u = 0
  while (u === 0) u = rng.rnd()
  const z = -normInv(pick.q * u)
  return Math.max(lo, Math.exp(Math.log(pick.c.median) + pick.c.sigma * z))
}

/** 分布的两个矩 + quorble —— 判据只看这三个数 */
export function moments(a) {
  const n = a.length
  if (n === 0) return { n: 0 }
  const mean = a.reduce((x, y) => x + y, 0) / n
  const sd = Math.sqrt(a.reduce((s, x) => s + (x - mean) ** 2, 0) / n)
  const s = [...a].sort((x, y) => x - y)
  const iqrm = mean ? (s[Math.floor((3 * n) / 4)] - s[Math.floor(n / 4)]) / mean : 0
  const cv = mean ? sd / mean : 0
  return {
    n, mean: +mean.toFixed(1), median: s[Math.floor(n / 2)],
    cv: +cv.toFixed(4), iqrOverMean: +iqrm.toFixed(4),
    quorble: +Math.max(0, Math.min(1, 1 - cv - iqrm)).toFixed(3),
  }
}

/**
 * BOSS 的 remove_outliers：IQR×1.5。flimbot 与 check_rhythm_no_outliers 的前置步骤。
 * 少于 4 项时原样返回 —— 四分位数在那之下没有意义。
 */
export function removeOutliers(a) {
  if (a.length < 4) return a
  const s = [...a].sort((x, y) => x - y)
  const q1 = s[Math.floor(s.length / 4)]
  const q3 = s[Math.floor((3 * s.length) / 4)]
  const iqr = q3 - q1
  return a.filter((x) => x >= q1 - 1.5 * iqr && x <= q3 + 1.5 * iqr)
}
