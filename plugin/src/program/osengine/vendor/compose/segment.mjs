// 打字粒度分词。
//
// 注意这不是 NLP 语义分词，是「真人一次上屏几个字」。两者不同，而且**只有前者要紧**：
// 报告已确证客户端不存在任何文本内容判据（behavior-layer-checks 03 节末），
// BOSS 看不见你切得对不对，只看得见每次 compositionend 的 data 长度分布、
// 以及由此产生的节奏。
//
// 目标分布取自真人基线（lab/calibrate/baseline/human-2026-08-20.json）：
//   1 字 26.1% · 2 字 58.7% · 3 字 10.9% · 4 字 2.2%   平均 1.85 字/次
//
// 汉字之外的字元（标点、数字、字母）走 direct 单独成段 —— 它们不经 IME，
// 而且是 cnTextCount 的唯一来源（全角标点被 containsChinese 算作中文）。
//
// **切分只在词边界上进行**（2026-08-21 真机实测的教训）。一稿按长度分布随机切，
// 把「方便聊聊吗」切成 方|便聊|聊吗 —— 「便聊」不是词，输入法按 bianliao 出不来它。
// 两个后果：真机上出错字；更本质的是真人的上屏边界总落在词上，切分本身就是
// 「像不像人」的一部分。前者在 TSF 终态会消失（TIP 直接上屏指定词），后者不会。
//
// mode:
//   human  按真人长度分布在词边界上**合并**相邻词（默认，用于判定验证）
//   safe   整个汉字连续段一次性输入、不切，交给输入法自己分词
//          （真机验证专用：候选正确率最高，代价是粒度不真实）

import { tokenize, keyFor, whyUntypable } from './pinyin.mjs'
import { segmentWords } from './lexicon.mjs'

/** 真人基线的上屏字数分布 */
export const COMMIT_LEN_DIST = [
  { len: 1, p: 0.261 },
  { len: 2, p: 0.587 },
  { len: 3, p: 0.109 },
  { len: 4, p: 0.022 },
]

function sampleLen(rng, dist, max) {
  const usable = dist.filter((d) => d.len <= max)
  if (usable.length === 0) return max
  const total = usable.reduce((a, c) => a + c.p, 0)
  let r = rng.rnd() * total
  for (const d of usable) {
    r -= d.p
    if (r <= 0) return d.len
  }
  return usable[usable.length - 1].len
}

/**
 * @returns {Array} 段序列
 *   IME 段    { kind:'ime',    text, pinyin, chars, syllables }
 *   直接段    { kind:'direct', text, code }
 *   换行      { kind:'direct', text:'\n', code:'Enter' }
 */
export function segment(text, rng, opt = {}) {
  const dist = opt.dist ?? COMMIT_LEN_DIST
  const mode = opt.mode ?? 'human'
  const toks = tokenize(text)

  // 前置校验：一次报出**全部**打不出的字元，而不是遇到第一个就抛。
  // 逐个抛的话，调用方改掉一个又撞下一个，来回好几趟。
  const bad = []
  toks.forEach((t, i) => {
    if (t.kind === 'han') {
      if (!t.py) bad.push({ i, ch: t.ch, why: whyUntypable(t) })
      return
    }
    if (!keyFor(t)) bad.push({ i, ch: t.ch, why: whyUntypable(t) })
  })
  if (bad.length) {
    const detail = bad.map((b) => `第 ${b.i + 1} 个字元 ${JSON.stringify(b.ch)}：${b.why}`).join('；')
    throw new Error(`文案含 ${bad.length} 个当前方案打不出的字元 —— ${detail}`)
  }
  const out = []
  let buf = []

  const emit = (take) => {
    const missing = take.filter((t) => !t.py)
    if (missing.length) {
      // 拼音为空的汉字打不出来。旧版会静默产出 pinyin 为空的 ime 段，
      // 计划里 keys=0、只剩一个 commit 键 —— 真机上 composition 为空时按下它，
      // 屏幕直接落一个字面字符。宁可在这里显式失败。
      throw new Error(
        `以下字缺拼音，无法用输入法打出：${missing.map((t) => t.ch).join('')}` +
          `（片段 ${JSON.stringify(take.map((t) => t.ch).join(''))}）`
      )
    }
    out.push({
      kind: 'ime',
      text: take.map((t) => t.ch).join(''),
      pinyin: take.map((t) => t.py).join(''),
      chars: take.length,
      // 逐字拼音长度 —— 音节边界的来源。输入法组字时在音节之间显示分隔撇号
      // （微软拼音把 nihao 显示成 ni'hao），而撇号**不是按出来的**：
      // 它是输入法自己画进组字区的，按键序列里没有 Quote。
      // 这件事只有这里知道：到了 planner 拼音已经拼成一串，切不回去。
      syllables: take.map((t) => t.py.length),
    })
  }

  const flushHan = () => {
    if (buf.length === 0) return
    if (mode === 'safe') {
      emit(buf.splice(0))
      return
    }
    // 词边界由词表给出；随后只在这些边界上**合并**，绝不切开词。
    const words = segmentWords(buf.map((t) => t.ch))
    let i = 0
    while (i < words.length) {
      // 未命中段整体成段，不与邻词合并 —— 合并会拼出非词
      if (!words[i].known) {
        emit(buf.splice(0, words[i].chars))
        i++
        continue
      }
      const target = sampleLen(rng, dist, Infinity)
      let taken = 0
      let count = 0
      while (
        i + count < words.length &&
        words[i + count].known &&
        (count === 0 || taken + words[i + count].chars <= target)
      ) {
        taken += words[i + count].chars
        count++
        if (taken >= target) break
      }
      emit(buf.splice(0, taken))
      i += count
    }
  }

  for (const t of toks) {
    if (t.kind === 'han') {
      buf.push(t)
      continue
    }
    flushHan()
    const k = keyFor(t) // 前置校验已保证非 null
    out.push({ kind: 'direct', text: t.ch, code: k.code, shift: !!k.shift })
  }
  flushHan()
  return out
}

/** 段序列的粒度统计 —— 与基线比对用 */
export function granularity(segs) {
  const ime = segs.filter((s) => s.kind === 'ime')
  const hist = {}
  for (const s of ime) hist[s.chars] = (hist[s.chars] || 0) + 1
  const n = ime.length || 1
  return {
    imeSegments: ime.length,
    directSegments: segs.length - ime.length,
    perCommitChars: hist,
    meanChars: +(ime.reduce((a, s) => a + s.chars, 0) / n).toFixed(3),
    meanKeys: +(ime.reduce((a, s) => a + s.pinyin.length + 1, 0) / n).toFixed(3),
  }
}
