// BOSS 判据表 —— 单一事实来源。
//
// ## 为什么要有这张表（2026-08-31）
//
// localCheck 一稿是「照着报告把阈值抄进 if 里」。抄的时候把**前置门**丢了，
// 而且丢得不止一处（RecruitHelper 侧报短文案排不出来，顺藤核出四处）：
//
//   量            我们写的                  BOSS 实际（门以下面第二段的实扫为准）
//   ───────────  ───────────────────────  ────────────────────────────────────────
//   quorble      四条序列各自 n≥4 且 >0    #9  门是 cnTextCount≠0 且 rhythm 项数≥8，
//                                          且只评 rhythms 一条
//   flimbotKd    同上（当第三条序列评）    #12 门是 cnTextCount≠0 且 keydurations 项数≥8
//   flimbotKr    同上（当第二条序列评）    #13 门是 cnTextCount≠0 且 keyboardRhythm 项数≥8
//   inputRhythm  也评了它的 quorble        BOSS 从不给它打分，也不拿它当任何门
//   键码占比     >0.45，无样本门           #15 元素数>9 阈值0.7；35010 元素数≥9 阈值0.5
//   speed        <130ms/字，无长度门       #2  containsChinese 且 textLen≥8 且 speed<100
//
// ## 门是扫出来的，不是抄来的（2026-08-31，`node lab/probe/gate-scan.mjs`，跑真 wasm）
//
// 报告判据表 #12/#13 下面那条「前置项数序列与被检查评分错位」的注**与实测不符**，
// 照它抄会同时抄错两处门。实扫（其余字段固定，逐个变量单独扫）：
//
//   cnTextCount    0 → 家族五项全灭；1 就够（不是 ≥8）→ 五项全亮
//   rhythm 项数    ≤7 → q/f/f0 灭，kd/kr 不受影响；8 → 全亮   ← 门是 rhythm、评的是 rhythms（真错位）
//   keydurations   ≤7 → kd 灭，其余不动；8 → 亮               ← 门就是它自己的序列，不错位
//   keyboardRhythm ≤7 → kr 灭，其余不动；8 → 亮               ← 同上，不错位
//   inputRhythm    0~12 → 五项无变化                          ← 它谁的门都不是
//   rhythms        0~12 → 五项无变化                          ← 被评分，但不参与任何门
//
// 所以真正错位的只有 #9/#10/#11 这一处（评 rhythms、数 rhythm）。
// **cnTextCount≠0 是整个家族的总闸**，而 Windows 上它恒为 0（中文全走 IME），
// 于是这五项在生产目标平台上根本不进账本 —— 真人实测同样如此（16 计数器全 0，
// 见 verify-capture.mjs 跑 human-win 基线）。这是运气不是安全边际：文案里只要有
// 一个字符不经输入法直接落进去（半角标点 / emoji / 粘贴 / 英文），闸门就开。
//
// 代价是实打实的：「嗯嗯」100% 排不出来（配对键只有 `KeyG,KeyN,KeyG,Space` 四个，
// BOSS 两个分支都不到样本门、根本不查，我们却按 0.5>0.45 否决，而这四个键是文案
// 决定的、真人打同一句话也是这四个 —— 重采一万次都变不了）；「好的」单次通过率 5%、
// 中位重采 8 次、端到端仍有 5% 彻底失败。
//
// 「把 4 改成 8」修不掉这些：常数只是其中一处，还有门用的序列（#9 那处真错位）、
// 整个家族的总闸 cnTextCount、以及 inputRhythm 这条根本不该被评分的序列。
// 只能把四元组**（被评分的量 / 前置门 / 阈值 / 归属）**放在一处，让检查去遍历它。
// 新增判据时改这张表，不要再往 localCheck 里加 if。
//
// ## 合同：本地保的是「上传的分数像真人」，不是「本地账本不加分」
//
// 计数器与上传是**两条独立的路**：计数器归 wasm 管、要过 cnTextCount 与项数≥8 的门；
// sec-entry 则**无条件**把算好的四个分数随每次输入发出去（HANDOFF.md §4；
// runtime-evidence.md §11 真号实测：载荷里 quorble=1，本地 quorble_abnormal_count 是 0）。
// 所以门只决定「进不进账本」，不决定「服务端看不看得见」。
//
// 于是每条判据的两个门分开写，各自说明理由：
//
//   gate   BOSS 自己的前置（照抄，含错位）。`apply:'gated'` 时参与本地否决，
//          `apply:'always'` 时只登记、只用于对账与解释。
//   floor  我们自己的**统计有效性下限**：样本量少到「真人打同一句话也会得出同一个值」
//          时，这个量没有区分力，本地不该拿它否决。它与 BOSS 的门是两回事 ——
//          BOSS 的门管账本，floor 管「这个数还说不说明问题」。
//
// 判断一条判据该不该在本地生效，标准只有一条：**真人打这句话会不会得出同样的值**。
// 会 → 它不是破绽，重采也改不掉，不该否决（「嗯嗯」的键码占比正是此例）。
//
// 表外还有一类检查不在这里：Shift 窗口、同键复现、debounce 上限。那些是
// **物理/注入可行性**（真机上按不出来或会被拆成两条消息），不是 BOSS 判据，
// 留在 planner.js 的 localCheck 里。

import { moments, removeOutliers } from './timing.mjs'

/** BOSS 的 check_rhythm：整条序列全部 ≤ T。空序列在 wasm 侧行为未验证，这里按「不成立」处理 */
export const checkRhythm = (a, t) => a.length > 0 && a.every((x) => x <= t)
/** check_rhythm_no_outliers = check_rhythm ∘ remove_outliers（a.wat:267007-267080 同族） */
export const checkRhythmNoOutliers = (a, t) => checkRhythm(removeOutliers(a), t)

/**
 * find_dominant_char（a.wat:130647-131519）：按 `,` 拆，元素总数 > 9 才统计，
 * 出现占比 > 阈值即命中。
 * v2（chat_job::find_dominant_char_v2）两处不同：**单字符键码不参与统计**，
 * 样本门是 ≥9（实测 total=9 即触发，见 chatjob-branch.md §3）。
 * @returns {{n:number, share:number}} n 为参与统计的元素数
 */
export function dominantShare(keys, { skipSingleChar = false } = {}) {
  const items = skipSingleChar ? keys.filter((k) => k.length > 1) : keys
  const c = {}
  for (const k of items) c[k] = (c[k] || 0) + 1
  const max = Math.max(0, ...Object.values(c))
  return { n: items.length, share: items.length ? max / items.length : 0 }
}

/**
 * 把一份 optionParams 摊成判据表要用的视图。
 *
 * 四个分数直接取 tracker 算好的 —— 它们就是 sec-entry 上传的那四个，
 * 本地不再自己算一遍（同一件事算两遍正是这次要消掉的毛病）。
 */
export function buildView(p) {
  const num = (csv) => (csv ? String(csv).split(',').map(Number).filter((x) => !Number.isNaN(x)) : [])
  const it = p.inputTrait ?? {}
  const seq = {
    // typings 相邻差（全部 typings）—— 实扫下来 BOSS 既不评它、也不拿它当门；
    // 留着是因为 debounce 那条要用它（见 planner 的 maxGap）
    inputRhythm: num(p.inputRhythm),
    // keyboardPairs 相邻 startTime 差 —— flimbotKr 的被评分序列，同时是 #13 自己的门
    keyboardRhythm: num(p.keyboardRhythm),
    // 每次按键的按住时长 —— flimbotKd 的被评分序列，同时是 #12 自己的门
    keydurations: num(p.keydurations),
    // 每次 input 事件的相邻差（含 composing 中）—— quorble/flimbot 的被评分序列
    rhythms: num(it.rhythms),
    // 只数 type===1（不经组字直接上屏）的 typings 相邻差 —— #9/#10/#11 的门，本身不被评分。
    // Windows 上恒为空（中文全走 IME），这也是 cnTextCount 恒为 0 的同一个原因
    rhythm: num(it.rhythm),
  }
  return {
    seq,
    stats: Object.fromEntries(Object.entries(seq).map(([k, v]) => [k, moments(v)])),
    keys: p.key ? String(p.key).split(',') : [],
    score: {
      quorble: it.quorble,      // = quorble(rhythms)
      flimbot: it.flimbot,      // = flimbot(rhythms) = quorble ∘ remove_outliers
      flimbotKd: it.flimbotKd,  // = quorble(keydurations)   ← 注意不是 flimbot，未去离群
      flimbotKr: it.flimbotKr,  // = quorble(keyboardRhythm)  ← 同上
    },
    cnTextCount: it.cnTextCount ?? 0,
    matchTrait: !!it.matchTrait,
    textLen: p.textLen,
    speed: p.speed,
    keyWordRatio: p.keyWordRatio,
    containsChinese: !!p.containsChinese,
    compositionAbnormal: !!p.compositionAbnormal,
    keyboardAbnormal: !!p.keyboardAbnormal,
  }
}

/**
 * 取某条被评分序列的 quorble 上限。缺键必须报错 —— 静默拿到 undefined，
 * `x > undefined` 恒为 false，判据会一声不响地永不触发。
 */
function maxQuorble(P, seq) {
  const v = P.limits.maxQuorble?.[seq]
  if (typeof v !== 'number') throw new Error(`params.limits.maxQuorble 缺少序列 ${seq} 的上限`)
  return v
}

/** 四个上传分数共用的门：BOSS 记不记账本 */
const scoreGate = (v) => v.seq.rhythm.length >= 8 && v.cnTextCount !== 0

/**
 * 判据表。字段：
 *   id/boss/cite  我们的名字 / BOSS 的计数器或上报码 / 出处
 *   scope         score = sec-entry 无条件上传的分数；ledger = wasm 内部计数器；
 *                 chatJob = chatJob 分支的即时上报（无累积）
 *   gate/gateDesc BOSS 的前置门（以真 wasm 实扫为准，见表头第二段）
 *   bossLimit     BOSS 的阈值（登记用；本地阈值另取，见 hit）
 *   sample        本判据的样本量
 *   floor         本地统计有效性下限，低于它不否决
 *   apply         gated = 照 BOSS 的门跳过；always = 门不参与本地否决（why 说明理由）
 *   hit(v,P)      违例说明，或 null
 */
export const CRITERIA = [
  // ── 四个无条件上传的分数 ────────────────────────────────────────────────
  {
    id: 'quorble', boss: '#9 quorble_abnormal_count', cite: '判据表 #9',
    scope: 'score',
    gate: scoreGate, gateDesc: 'rhythm 项数≥8 且 cnTextCount≠0', bossLimit: 0,
    sample: (v) => v.seq.rhythms.length, floor: 4,
    apply: 'always',
    why: '分数随每次输入无条件上传，账本记不记不影响服务端能不能看见',
    hit: (v, P) => (v.score.quorble > maxQuorble(P, 'rhythms')
      ? `quorble(rhythms) ${v.score.quorble} > ${maxQuorble(P, 'rhythms')}` : null),
  },
  {
    id: 'flimbot', boss: '#10 flimbot_abnormal_count（>0.6）', cite: '判据表 #10',
    scope: 'score',
    gate: (v) => scoreGate(v) && checkRhythm(v.seq.rhythm, 200),
    gateDesc: 'rhythm 项数≥8 且 cnTextCount≠0 且 check_rhythm(rhythm,200)', bossLimit: 0.6,
    sample: (v) => v.seq.rhythms.length, floor: 4,
    apply: 'always',
    why: '同上；rhythms 的本地上限是 0（比 BOSS 的 0.6 严），#11 的 >0 由此一并覆盖',
    hit: (v, P) => (v.score.flimbot > maxQuorble(P, 'rhythms')
      ? `flimbot(rhythms) ${v.score.flimbot} > ${maxQuorble(P, 'rhythms')}` : null),
  },
  {
    id: 'flimbot0', boss: '#11 flimbot_abnormal_0_count（>0）', cite: '判据表 #11',
    scope: 'score',
    gate: scoreGate, gateDesc: '同 #9（无 check_rhythm）', bossLimit: 0,
    sample: (v) => v.seq.rhythms.length, floor: 4,
    apply: 'off',
    why: '与 #10 同一个分数、阈值更严；rhythms 的本地上限 0 已等价覆盖，只登记不重复报',
    hit: () => null,
  },
  {
    id: 'flimbotKd', boss: '#12 flimbot_kd_abnormal_count', cite: '判据表 #12',
    scope: 'score',
    // 实扫：门是 keydurations 自己的项数≥8，外加家族总闸 cnTextCount≠0。
    // 报告判据表 #12 记的「门是 keyboardRhythm」与实测不符，见表头第二段。
    gate: (v) => v.cnTextCount !== 0 && v.seq.keydurations.length >= 8,
    gateDesc: 'cnTextCount≠0 且 keydurations 项数≥8', bossLimit: 0,
    sample: (v) => v.seq.keydurations.length, floor: 4,
    apply: 'always',
    why: '四条里唯一有真人风险的一条：Windows 真人 dwell quorble=0.354。本地上限取 0.6 '
      + '（像真人：落回真人分布；不越线：#12 的账本线 >0 被 cnTextCount 闸住且不在五项 ★ 里）',
    hit: (v, P) => (v.score.flimbotKd > maxQuorble(P, 'keydurations')
      ? `quorble(keydurations) ${v.score.flimbotKd} > ${maxQuorble(P, 'keydurations')}` : null),
  },
  {
    id: 'flimbotKr', boss: '#13 flimbot_kr_abnormal_count', cite: '判据表 #13',
    scope: 'score',
    // 实扫：门是 keyboardRhythm 自己的项数≥8，外加 cnTextCount≠0。报告记的「inputRhythm」与实测不符。
    gate: (v) => v.cnTextCount !== 0 && v.seq.keyboardRhythm.length >= 8,
    gateDesc: 'cnTextCount≠0 且 keyboardRhythm 项数≥8', bossLimit: 0,
    sample: (v) => v.seq.keyboardRhythm.length, floor: 4,
    apply: 'always',
    why: '同 #12：分数无条件上传。真人两平台都是 0，维持 0 不花钱',
    hit: (v, P) => (v.score.flimbotKr > maxQuorble(P, 'keyboardRhythm')
      ? `quorble(keyboardRhythm) ${v.score.flimbotKr} > ${maxQuorble(P, 'keyboardRhythm')}` : null),
  },

  // ── wasm 内部计数器 ─────────────────────────────────────────────────────
  {
    id: 'speed', boss: '#2 speed_abnormal_count / 33006-33008', cite: '判据表 #2',
    scope: 'ledger',
    gate: (v) => v.containsChinese && v.textLen >= 8,
    gateDesc: 'containsChinese 且 textLen≥8', bossLimit: 100,
    sample: (v) => v.textLen, floor: 1,
    apply: 'always',
    why: '本地这条不是抄 BOSS 的门，是真人形态约束：两个字打出 50ms/字 一样不像人，'
      + '且 speed 在 typingInfo 里随每次输入上传。阈值 130 > BOSS 的 100，余量是我们自己留的',
    hit: (v, P) => (v.speed < P.limits.minSpeedMsPerChar
      ? `speed ${v.speed} < ${P.limits.minSpeedMsPerChar}ms/字` : null),
  },
  {
    id: 'composition', boss: '#3 composition_abnormal_count', cite: '判据表 #3',
    scope: 'ledger',
    gate: (v) => v.containsChinese, gateDesc: 'containsChinese + 浏览器白名单', bossLimit: null,
    sample: () => 1, floor: 1,
    apply: 'always',
    why: '含中文却没有 compositionend —— 结构性破绽，与样本量无关',
    hit: (v) => (v.compositionAbnormal ? 'compositionAbnormal（含中文却无 compositionend）' : null),
  },
  {
    id: 'keyboard', boss: '#4 keyboard_abnormal_count / 33005', cite: '判据表 #4',
    scope: 'ledger',
    gate: () => true, gateDesc: '无（不受 containsChinese 门控）', bossLimit: null,
    sample: () => 1, floor: 1,
    apply: 'always',
    why: '有 input 无按键配对 —— 结构性破绽。等价于 chatJob 的 35011（key 为空）',
    hit: (v) => (v.keyboardAbnormal ? 'keyboardAbnormal（有 input 无按键配对）' : null),
  },
  {
    id: 'keyWordRatio', boss: '#5 key_word_ratio_abnormal_count', cite: '判据表 #5',
    scope: 'ledger',
    gate: () => true, gateDesc: '无', bossLimit: 0.5,
    sample: (v) => v.textLen, floor: 1,
    apply: 'always',
    why: 'BOSS 的下界是 >0（比值 0 归 #4）；本地不设下界 —— 0 也是破绽，由 #4 一并报',
    hit: (v) => (v.keyWordRatio < 0.5 ? `keyWordRatio ${v.keyWordRatio} < 0.5` : null),
  },
  {
    id: 'inputRhythm15', boss: '#6 input_rhythm_abnormal_count', cite: '判据表 #6',
    scope: 'ledger',
    gate: (v) => v.cnTextCount >= 8, gateDesc: 'cnTextCount ≥ 8', bossLimit: 15,
    sample: (v) => v.seq.rhythm.length, floor: 1,
    apply: 'gated',
    why: 'Windows 上 cnTextCount 恒为 0，这条天然不参与；照 BOSS 的门跳过',
    hit: (v) => (checkRhythm(v.seq.rhythm, 15) ? 'rhythm 全部 ≤15ms（check_rhythm 命中）' : null),
  },
  {
    id: 'inputRhythm200', boss: '#7 input_rhythm_abnormal_200_count', cite: '判据表 #7',
    scope: 'ledger',
    gate: (v) => v.cnTextCount >= 8, gateDesc: 'cnTextCount ≥ 8', bossLimit: 200,
    sample: (v) => v.seq.rhythm.length, floor: 1,
    apply: 'gated',
    why: '同 #6',
    hit: (v) => (checkRhythmNoOutliers(v.seq.rhythm, 200)
      ? 'rhythm 去离群后全部 ≤200ms（check_rhythm_no_outliers 命中）' : null),
  },
  {
    id: 'matchTrait', boss: '#8 input_trait_abnormal_count', cite: '判据表 #8',
    scope: 'ledger',
    gate: (v) => v.cnTextCount >= 8, gateDesc: 'cnTextCount ≥ 8（已含在 matchTrait 里）',
    bossLimit: null,
    sample: () => 1, floor: 1,
    apply: 'always',
    why: 'tracker 算 matchTrait 时已经把 cnTextCount≥8 算进去了，这里不必再门一次',
    hit: (v) => (v.matchTrait ? 'matchTrait（每项都是单个中文/全角标点/数字）' : null),
  },
  {
    id: 'keycode', boss: '#14 keycode_abnormal_count', cite: '判据表 #14',
    scope: 'ledger',
    gate: (v) => v.keys.length > 9, gateDesc: '元素数 > 9', bossLimit: null,
    sample: (v) => v.keys.length, floor: 10,
    apply: 'gated',
    why: '抓的是 keyCode 数字串（每项长度 1 且首字节 "0"）；我们排的是 KeyX/Space，不会命中',
    hit: (v) => (v.keys.every((k) => k.length === 1 && k[0] === '0')
      ? 'key 全是单字符数字码（keyCode 回退路径泄露）' : null),
  },
  {
    id: 'dominantChar', boss: '#15 keycode_abnormal_count_2 / #16 gobbledygook', cite: '判据表 #15',
    scope: 'ledger',
    gate: (v) => v.keys.length > 9, gateDesc: '元素数 > 9', bossLimit: 0.7,
    sample: (v) => v.keys.length, floor: 10,
    apply: 'off',
    why: '阈值 0.7 比 chatJob 的 0.5 松，被下面那条覆盖；#16 与本条同条件同值，一并登记',
    hit: () => null,
  },

  // ── chatJob 分支（无累积，逐次判定）────────────────────────────────────
  {
    id: 'dominantCharV2', boss: 'chatJob 35010', cite: 'chatjob-branch.md §3',
    scope: 'chatJob',
    gate: (v) => dominantShare(v.keys, { skipSingleChar: true }).n >= 9,
    gateDesc: '多字符键码元素数 ≥9（单字符键码不参与统计）', bossLimit: 0.5,
    sample: (v) => dominantShare(v.keys, { skipSingleChar: true }).n, floor: 9,
    apply: 'gated',
    why: '样本门就是 BOSS 的门，也正是 floor 该在的地方：'
      + '「嗯嗯」的配对键只有 4 个，真人打这两个字也是同样四个 —— '
      + '占比 0.5 是文案决定的，不是我们造成的，重采改不动。'
      // ⚠ 短文案的占比被**系统性抬高**：Windows 上 IME keydown 的 key 是 "Process"，
      // tracker 要等 compositionstart 才建，整个 session 丢首键那一次配对
      // （platform.mjs 的 imeKeyField）。两个字的文案因此永远只有 4 个配对键，
      // 分母小一个、占比就高一档。现在被样本门挡着；等文案长到 9 个键以上、
      // 又恰好高度重复时才会咬人。
      + ' 另注：Windows 丢首键配对会让短文案的分母再少一个，见本条注释',
    hit: (v, P) => {
      const { share } = dominantShare(v.keys, { skipSingleChar: true })
      return share > P.limits.maxKeyShare
        ? `键码占比 ${share.toFixed(3)} > ${P.limits.maxKeyShare}` : null
    },
  },
  {
    id: 'chatJobQuorble', boss: 'chatJob 35012', cite: 'chatjob-branch.md §3',
    scope: 'chatJob',
    gate: () => true, gateDesc: '未观察到样本门（0.6 不触发、0.61 触发）', bossLimit: 0.6,
    sample: (v) => v.seq.rhythms.length, floor: 4,
    apply: 'off',
    why: '同一个 quorble(rhythms)，阈值 0.6 比 rhythms 的本地上限 0 松，已被 #9 覆盖',
    hit: () => null,
  },
  {
    id: 'chatJobRhythm', boss: 'chatJob 35013', cite: 'chatjob-branch.md §3',
    scope: 'chatJob',
    gate: () => true, gateDesc: '（判据未完全定位）', bossLimit: null,
    sample: (v) => v.seq.rhythm.length, floor: 1,
    apply: 'off',
    why: '「rhythm 等间隔或项数过少」——Windows 上 rhythm 恒为 0 项，真人同样如此，'
      + '无区分力且重采改不动。留待真机确认后再决定要不要管',
    hit: () => null,
  },
]

/**
 * 遍历判据表。
 * @returns {{reasons: string[], rows: Array}} rows 是逐条对账用的解释，
 *          含「门成不成立 / 样本够不够 / 本地判没判 / 结论」，给标定与排错看。
 */
export function evaluate(v, P) {
  const reasons = []
  const rows = []
  for (const c of CRITERIA) {
    const n = c.sample(v)
    const gateOk = c.gate(v)
    let status
    let reason = null
    if (c.apply === 'off') status = '只登记'
    else if (n < c.floor) status = `样本不足（n=${n} < floor ${c.floor}）`
    else if (c.apply === 'gated' && !gateOk) status = 'BOSS 前置不成立'
    else {
      reason = c.hit(v, P)
      status = reason ? '违例' : '通过'
    }
    if (reason) reasons.push(reason)
    rows.push({ id: c.id, boss: c.boss, scope: c.scope, n, gateOk, status, reason })
  }
  return { reasons, rows }
}
