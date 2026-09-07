// 排版器：动态文案 → 输入计划。
//
// 固定模板可以「一次调好参数永远复用」，动态文案不行 —— 每条消息的长度、词数、
// 词长分布都不同，同一套参数在 20 字上通过、在 6 字上可能因样本量前置门根本不进判据，
// 在 80 字上又可能因分位数的最近秩取值漂移而翻车。所以必须是闭环：
//
//   生成 → 本地预检 → （可选）oracle 判定 → 不过就换种子重采
//
// 本地预检不需要 oracle：序列由 capture/ 的同一份口径算出，quorble 是
// 纯算术（clamp(1−CV−IQR/μ)），speed / 键码占比 / 间隔上限也都是纯计算。
// 于是绝大多数不合格的计划在本地就被淘汰，oracle 只做最终确认。
//
// **BOSS 判据不写在本文件里，在 criteria.mjs 那张表上。** 本文件只留
// 物理/注入可行性（Shift 窗口、同键复现、debounce 上限）—— 它们不是判据，
// 是「真机上按不按得出来」。判据要改，改表；表头讲了为什么不能就地写 if。
//
// engine 纪律：本模块只 import engine 内部与 pinyin-pro，绝不依赖 lab/oracle。
// 外部判定通过 `verify` 回调注入 —— 生产环境里没有 aegis wasm。

import { segment, granularity, UntypableError } from './segment.mjs'
import { sanitize } from './sanitize.mjs'
import { makeRng, sampleMix, sampleMixAbove, moments, removeOutliers } from './timing.mjs'
import { buildView, dominantShare, evaluate } from './criteria.mjs'
import { withParams } from './params.mjs'
import { InputTracker } from '../capture/tracker.mjs'
import { synthTyping } from '../capture/synth.mjs'

function pickWeighted(rng, list) {
  const total = list.reduce((a, c) => a + c.p, 0)
  let r = rng.rnd() * total
  for (const c of list) {
    r -= c.p
    if (r <= 0) return c
  }
  return list[list.length - 1]
}

/** 单次生成：段序列 + 参数 → plan（可直接喂 synthTyping） */
export function composeOnce(text, params, rng, startTime) {
  const P = params
  const segs = segment(text, rng)
  const dwell = () => Math.max(P.limits.minDwellMs, Math.round(sampleMix(rng, P.dwell.mix)))

  let t = startTime
  let first = true
  const words = []
  /** 上一个 Shift 松手 + shiftGuardMs：下一个事件不得早于它（见 params.limits 里 Xin子 的教训） */
  let minNextDown = -Infinity
  /** code → 该键上一次松手的时刻。同键复现的间隔约束用它。 */
  const lastUp = new Map()
  /** 上一个非 Shift 键的松手时刻。ShiftLeft 不得在它之前按下（真人不会在上一个键还按着时去压 Shift）。 */
  let lastKeyUp = -Infinity

  /**
   * 排下一个键：从 mix 采「距上一个 keydown 的间隔」，**条件是它清掉本次全部物理下限**。
   *
   * 物理下限有四条，全都能写成「本次最早事件不得早于某时刻」，也就是对 gap 的一个下限：
   *   同键复现  目标键 down ≥ 该键上次 up + sameKeyGuardMs（你按不下一个正按着的键）
   *   Shift 退出 最早事件 ≥ 上一个 Shift 的 up + shiftGuardMs（否则下一个字母被带成大写）
   *   Shift 进入 ShiftLeft.down ≥ 上一个键的 up + entryMarginMs（真人先松键再压 Shift）
   *   Shift 同键 ShiftLeft.down ≥ 上一个 ShiftLeft 的 up + sameKeyGuardMs
   * 取最大者，按条件分布 gap | gap ≥ lower 采一次 —— 条件采样与拒绝重采同分布（真人满足
   * 这些约束就是靠拉长间隔，见 params.limits.sameKeyGuardMs），但没有次数上限、不会漏。
   *
   * **为什么不能是「重采几次不成就交给 localCheck 拒绝整篇」（一稿的做法）。** 这些约束
   * 是局部的：只牵涉相邻两三个键。一篇 600 键的文案里同键相邻的位置有几十处，每处都独立地
   * 有一个小概率超出重采次数、漏出去；整篇通过要求全部不漏，概率随长度指数衰减 ——
   * 实测 285 字单次通过率 4.5%，40 次重采 0/10 篇收敛。局部约束用整篇拒绝兜底，
   * 粒度就错了；而且漏与不漏并不改变被接受样本的分布（都是同一个条件分布），
   * 次数上限唯一的作用就是把「采得慢」变成「整篇扔掉」。
   *
   * 也不能是钳制：钳制会在下限上堆出一批精确重复值，Shift 那条约束吃过这个亏。
   *
   * @param {Array} mix    这一步间隔的分布（段内 intraKey / 段间 segMix / 上屏前 commitKey）
   * @param {string} code  要排的键位
   * @param {number} [lead] 带 Shift 的字元：ShiftLeft 比目标键早按下的量。此时本次最早事件是
   *        ShiftLeft.down = base + gap，目标键在 base + gap + lead。只约束目标键是不够的：
   *        下一个字元若也带 Shift，它的 ShiftLeft 会压在上一个 Shift 的 up 之前，
   *        两个 Shift 重叠、前一个的 up 会把后一个需要的 Shift 一起松掉。
   */
  const advance = (mix, code, lead = 0) => {
    // 首键落在 startTime 上，没有「距上一个键」可言
    if (first) { first = false; return }
    const base = t
    const guard = P.limits.sameKeyGuardMs
    let lower = minNextDown - base
    const up = lastUp.get(code)
    if (up != null) lower = Math.max(lower, up + guard - lead - base)
    if (lead) {
      lower = Math.max(lower, lastKeyUp + P.shift.entryMarginMs - base)
      const shiftUp = lastUp.get('ShiftLeft')
      if (shiftUp != null) lower = Math.max(lower, shiftUp + guard - base)
    }
    let g = Math.max(1, Math.round(sampleMixAbove(rng, mix, lower)))
    // maxGapMs 是防 debounce 的余量，只在不与物理下限冲突时生效 —— 下限赢了就让 localCheck
    // 用 debounce 那条拒绝整篇（那是全局约束，本就该在那一层），不能在这里排出物理不可能的键
    g = Math.min(g, Math.max(P.limits.maxGapMs, Math.ceil(lower)))
    t = base + g + lead
  }

  /**
   * 段间间隔的分布。**统一加在段的开头**，不加在段的末尾。
   *
   * 一稿把它加在 ime 段末尾、direct 段末尾却没加，于是「标点 → 汉字」只剩段内的
   * intraKey（中位 105ms）—— 真人在标点后是有句读停顿的，而且这让带 Shift 的标点
   * 每次都撞上 guard 下限（实测四次余量全是 40ms，靠钳制而非分布拉开）。
   */
  const segMix = (prev, cur) => {
    // 带 Shift 的标点之后按段间取 —— 打完它是句读位置，真人要松 Shift、切手型，
    // 停顿本就更像段间而非段内。用较小的 directGap 会让它频繁撞上 guard 下限，
    // 变成「靠钳制拉开」而不是「靠分布拉开」。
    if (prev.kind === 'direct' && prev.shift) return P.interSeg.mix
    return prev.kind === 'direct' || cur.kind === 'direct' ? P.directGap.mix : P.interSeg.mix
  }

  let prevSeg = null
  for (const s of segs) {
    // 首段之前没有段间间隔（advance 对首键不采），这里给的 mix 不会被用到
    const mix = prevSeg ? segMix(prevSeg, s) : P.interSeg.mix

    if (s.kind === 'direct' && s.shift) {
      // ── 带 Shift 的字元 ──
      // Shift 是一个会被完整统计的普通键，lead 与 dwell 各自独立采样（见 params.shift）。
      // 这两个量一变大，重叠风险就跟着变大，所以三条约束必须同时成立：
      //   A  ShiftLeft.down ≥ 上一个键.up + entryMargin → 先松上一个键再压 Shift
      //   B  shiftDwell ≥ lead + kDwell + cover          → Shift 盖过被修饰键的整个按下过程
      //   C  下一个键 ≥ Shift.up + guard                 → 下一个字母不被带成大写（Xin子 的教训）
      const lead = Math.max(1, Math.round(sampleMix(rng, P.shift.lead.mix)))
      const kDwell = Math.min(dwell(), P.limits.shiftMaxDwellMs)
      const shiftDwell = Math.max(
        Math.round(sampleMix(rng, P.shift.dwell.mix)),
        lead + kDwell + P.shift.coverMs // B
      )
      // 段间间隔由**本段最早的事件**（ShiftLeft，早目标键 lead）去承接：advance 采的 gap
      // 是 base → ShiftLeft.down，目标键再晚 lead。把 lead 从 gap 里扣是错的：那样上一个键
      // 到 ShiftLeft 的实得间隔只剩 gap−lead，中位从 327ms 塌到 176ms，且 33% 的 ShiftLeft
      // 落在上一个键（IME 上屏键）的按住区间内。A 与 C 都是 advance 里 gap 的下限。
      advance(mix, s.code, lead)
      const kDown = t
      const shiftDown = kDown - lead
      const shiftUp = shiftDown + shiftDwell
      words.push({
        text: s.text, direct: true, shift: true, passthrough: !!s.passthrough,
        keys: [
          { code: 'ShiftLeft', down: shiftDown, up: shiftUp, modifier: true },
          { code: s.code, down: kDown, up: kDown + kDwell, shift: true },
        ],
      })
      minNextDown = shiftUp + P.limits.shiftGuardMs // C
      lastUp.set('ShiftLeft', shiftUp)
      lastUp.set(s.code, kDown + kDwell)
      lastKeyUp = kDown + kDwell
    } else if (s.kind === 'direct') {
      advance(mix, s.code)
      const d = dwell()
      words.push({
        text: s.text, direct: true, passthrough: !!s.passthrough,
        keys: [{ code: s.code, down: t, up: t + d }],
      })
      lastUp.set(s.code, t + d)
      lastKeyUp = t + d
      minNextDown = -Infinity
    } else {
      const keys = []
      let firstLetter = true
      for (const ch of s.pinyin) {
        // 键位要先算出来 —— advance 需要它才能满足同键复现的间隔约束
        const code = /[a-z]/i.test(ch) ? 'Key' + ch.toUpperCase() : 'Key' + ch
        advance(firstLetter ? mix : P.intraKey.mix, code)
        firstLetter = false
        minNextDown = -Infinity
        const d = dwell()
        keys.push({ code, letter: ch, down: t, up: t + d })
        lastUp.set(code, t + d)
        lastKeyUp = t + d
      }
      // 上屏键同样先选定，才能参与同键约束（Space 高频，很容易撞上上一段的 Space）
      const commitCode = pickWeighted(rng, P.commitKeys).code
      advance(P.commitKey.mix, commitCode)
      const cd = dwell()
      const commit = { code: commitCode, letter: '', down: t, up: t + cd }
      lastUp.set(commitCode, t + cd)
      lastKeyUp = t + cd
      // 音节边界（相对拼音串起点的偏移），供 TIP 在组字区显示分隔撇号。
      // 只是显示：不多按任何键，不改变任何被判定的字段。见 tip/src/compose.rs。
      const splits = []
      let acc = 0
      for (const n of (s.syllables ?? []).slice(0, -1)) splits.push((acc += n))
      words.push({ text: s.text, keys, commit, splits })
      minNextDown = -Infinity
    }
    prevSeg = s
  }

  return { startTime, words, _segs: segs }
}

/**
 * 本地预检：把 plan 走一遍 capture 口径，检查全部硬约束。
 * 不依赖 oracle —— quorble 是纯算术，speed / 占比 / 间隔上限也是。
 * BOSS 判据部分交给 criteria.mjs 的判据表；返回值里的 `criteria` 是逐条对账结果。
 *
 * **platform 必须一路传进来。** 这里曾经是 `synthTyping(plan)` 无参调用，
 * 于是不管目标平台是什么，重采回路验的永远是 synth 的默认值（当时是 macOS）。
 * 在 Windows 上跑排版器，会用错误的平台假设判断「这份计划安全吗」然后接受它。
 *
 * @param {string|object} [platform] 见 lab/engine/capture/platform.mjs
 */
export function localCheck(plan, params, platform) {
  const P = params
  const events = synthTyping(plan, { platform })
  const it = new InputTracker({ module: '' })
  it.feedAll(events)
  // 本地 quorble：与 wasm 同式（总体方差、最近秩分位、clamp、3 位小数）
  const q = (a) => moments(a).quorble
  const p = it.flush({ quorble: q, flimbot: (a) => q(removeOutliers(a)) })
  if (!p) return { ok: false, reasons: ['未产生任何 typings'], params: null }

  // 判据表的视图（五条序列 + 四个上传分数 + 标量）。BOSS 判据一律从这里读，
  // 不在本函数里就地取值 —— 那正是先前门与序列错配的来源。
  const v = buildView(p)
  const stats = v.stats
  // 登记用的键码集中度。口径按 chatJob 的 v2（单字符键码不参与统计），
  // 与判据表 dominantCharV2 那条同源。
  const share = dominantShare(v.keys, { skipSingleChar: true }).share
  // debounce 的计时依据是 reportTyping 的调用间隔 —— 它只在 compositionend 与
  // 非 composing 的 input 上被调（sec-entry.deob.js:2070/2085），正对应 typings
  // 相邻差 inputRhythm。composing 期间的 input 不重置 debounce，故 rhythms 不算。
  const maxGap = Math.max(0, ...v.seq.inputRhythm)

  const reasons = []
  // ── 物理/注入可行性。**这几条由 composeOnce 的 advance 按构造保证**（全部写成了间隔的
  // 下限、按条件分布采样），这里是对账而不是筛选：正常情况下一条都不该命中。命中了说明
  // composeOnce 有 bug（或 params 被改出了矛盾），不是这一篇运气差 —— 别拿重采次数去盖它。
  // 仍然保留为拒绝理由而不是抛错，是因为它拦的是「真机上按不按得出来」，宁可多拒一篇。
  //
  // Shift 窗口：带 Shift 的键松手后必须留够 shiftGuardMs，下一个键才能按下。
  // 少了这道，注入层无论怎么排都会让下一个字母变大写。
  const flat = []
  for (const w of plan.words) {
    for (const k of w.keys) flat.push(k)
    if (w.commit) flat.push(w.commit)
  }
  flat.sort((a, b) => a.down - b.down)
  for (let i = 0; i < flat.length; i++) {
    const k = flat[i]
    if (!k.modifier) continue
    // A：不得早于上一个非本 Shift 的键**松开** —— 比「按下」严：真人不会在上一个键
    // 还按着的时候就去压 Shift。违例走重采，不靠钳制。
    const prev = flat.slice(0, i).filter((x) => !x.modifier).pop()
    if (prev && k.down <= prev.up)
      reasons.push(`Shift 早于上一个键松开：${Math.round(prev.up - k.down)}ms`)
    // B：必须盖过被修饰键
    const target = flat.find((x) => x.shift && x.down > k.down && x.down < k.up)
    if (!target) reasons.push('Shift 未盖住被修饰键')
    else if (k.up < target.up) reasons.push(`Shift 早于被修饰键松手 ${Math.round(target.up - k.up)}ms`)
    // C：松手后必须留够余量，下一个键才能按下 —— **含下一个 Shift**，
    // 漏掉它会让两个 Shift 重叠（前者的 up 把后者需要的 Shift 一起松掉）
    const next = flat.slice(i + 1).find((x) => x !== target)
    if (next) {
      const slack = next.down - k.up
      if (slack < P.limits.shiftGuardMs)
        reasons.push(`Shift 窗口不足：松手后仅 ${Math.round(slack)}ms < ${P.limits.shiftGuardMs}ms`)
    }
  }
  // 同键复现：上一次松手到下一次按下必须留够 sameKeyGuardMs。
  // 违反它是**物理不可能**（按不下一个正按着的键），而不只是不像真人。
  // 真机后果：Windows 把第二次 keydown 标成 repeat=true，pairKeys 跳过 repeat
  // 记录、永不配对，那一对就凭空消失 —— 离线模型与真机从此对不上账。
  {
    const lastUp = new Map()
    for (const k of flat) {
      const prevUp = lastUp.get(k.code)
      if (prevUp != null) {
        const slack = k.down - prevUp
        if (slack < P.limits.sameKeyGuardMs)
          reasons.push(
            `同键 ${k.code} 间隔不足：松手后仅 ${Math.round(slack)}ms < ${P.limits.sameKeyGuardMs}ms` +
              (slack < 0 ? '（自重叠，真机会报 repeat=true）' : '')
          )
      }
      lastUp.set(k.code, k.up)
    }
  }
  if (maxGap >= P.limits.debounceMs) reasons.push(`最大间隔 ${maxGap} ≥ debounce ${P.limits.debounceMs}`)

  // ── 以上是物理/注入可行性；以下是 BOSS 判据，全部走判据表 ──
  // 判据不在这里写 if，改 criteria.mjs。表头讲了为什么：
  // 门与被评分序列在 BOSS 侧是错位配对的，就地写 if 必然抄漏。
  const { reasons: bossReasons, rows: criteria } = evaluate(v, P)
  reasons.push(...bossReasons)

  return {
    ok: reasons.length === 0, reasons, params: p, stats, criteria,
    keyShare: +share.toFixed(4), maxGap, events,
  }
}

/**
 * 闭环：生成 → 本地预检 → 可选外部判定 → 不过就换种子重采。
 * @param {string} text 动态文案
 * @param {object} o
 * @param {object} [o.params]  排版参数（默认取自真人基线）
 * @param {number} [o.seed]    起始种子；每次重采 +1，故整个过程可复现
 * @param {number} [o.startTime]
 * @param {number} [o.maxTries]
 * @param {boolean} [o.sanitize] 先摘掉打不出来的字元再排（见 sanitize.mjs）。
 *        默认 **false** —— 排版器的严格契约不变，清理是调用方显式要的一层。
 * @param {function} [o.verify] async (plan, localResult) => {ok, detail} —— 注入 oracle 判定
 *
 * **文案层面的失败不抛异常**：打不出的字元、重采多少次都不过 —— 一律走返回值
 * `{ok:false, reasons}`。先前打不出的字元是 `throw`、重采不收敛是 `ok:false`，
 * 同一件事两种形状，调用方得写两套处理，而漏掉 catch 的那一套会让**一条**文案
 * 停掉**整批**。跑业务时「因为小事停机」的实际机制就是这个，不是生僻字本身。
 *
 * **排版器自己坏了照样抛**（配置缺参数、平台名写错、pinyin-pro 对不齐）。那种失败
 * 每一条文案都会撞上，吞成返回值等于把一次故障伪装成一堆坏文案、让生产侧安静地
 * 逐条跳过。只 catch `UntypableError`，就是为了把这两类分开。
 */
export async function compose(text, o = {}) {
  const params = withParams(o.params)
  // 清理放在最前面，且只做一次 —— 重采换的是时序种子，跟文案没关系。
  const clean = o.sanitize ? sanitize(text) : { text, dropped: [] }
  const src = clean.text
  if (o.sanitize && !src) {
    return { ok: false, plan: null, tries: 0, attempts: [], text: src, dropped: clean.dropped,
      reasons: ['清理之后没有内容可打'] }
  }
  // 平台画像：缺省即生产目标（Windows）。见 lab/engine/capture/platform.mjs。
  const platform = o.platform
  const maxTries = o.maxTries ?? 40
  const startTime = o.startTime ?? 0
  const baseSeed = o.seed ?? 1
  const attempts = []

  for (let i = 0; i < maxTries; i++) {
    const seed = baseSeed + i
    const rng = makeRng(seed)
    let plan
    try {
      plan = composeOnce(src, params, rng, startTime)
    } catch (e) {
      // 只接文案层面的失败（有打不出的字元）—— **换种子重采一万次也一样**，立刻返回。
      // 别的错误是排版器自己的问题，原样抛出去，让它停下来。
      if (!(e instanceof UntypableError)) throw e
      return { ok: false, plan: null, tries: i + 1, attempts, text: src,
        dropped: clean.dropped, reasons: [e.message] }
    }
    const local = localCheck(plan, params, platform)
    if (!local.ok) {
      attempts.push({ seed, stage: 'local', reasons: local.reasons })
      continue
    }
    if (o.verify) {
      const v = await o.verify(plan, local)
      if (!v.ok) {
        attempts.push({ seed, stage: 'verify', reasons: v.reasons ?? [v.detail] })
        continue
      }
    }
    return {
      ok: true, plan, seed, tries: i + 1, attempts,
      text: src, dropped: clean.dropped,
      optionParams: local.params, stats: local.stats,
      keyShare: local.keyShare, maxGap: local.maxGap,
      granularity: granularity(plan._segs),
    }
  }
  return { ok: false, tries: maxTries, attempts, plan: null, text: src, dropped: clean.dropped,
    reasons: [`${maxTries} 次重采都没通过本地预检`] }
}
