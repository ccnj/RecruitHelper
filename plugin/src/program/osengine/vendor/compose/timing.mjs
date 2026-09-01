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
