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
// 汉字之外的字元（标点、数字）走 direct 单独成段 —— 它们不经 IME，
// 而且是 cnTextCount 的唯一来源（全角标点被 containsChinese 算作中文）。
//
// **英文字母不走 direct，走 composition，与汉字同一条路。**「base深圳」里的 base
// 排成一个段：键序 b,a,s,e + 一个上屏键，上屏的文本就是这个词。这是自研 TIP 才有的
// 能力 —— 上屏什么由我们指定，于是不需要「输入法模式」这个概念（先前刻意拒绝英文，
// 正是因为借来的输入法要靠切模式，而模式是个状态机）。三件事跟着定死：
//
//   **大小写不排 Shift。**「Base」的键序仍是全小写 b,a,s,e，大写由上屏的词带出来。
//   真人在中英混输里从候选选首字母大写的那一项，键序也是全小写。好处是不必给
//   「打大写字母的 Shift」编时序：现有 shift 参数取自 4 次**打全角标点**的样本
//   （lead 70/103/149/672ms），那是句读位置的动作，套到词中间会把 B→a 顶到几百毫秒。
//
//   **上屏键沿用中文段那张 commitKeys**（Space 0.86 + Digit2/3/4）。英文词在候选里
//   排第几本来就浮动，固定一个键反而是编确定性。
//
//   **不排回车上屏。** 真输入法里回车上屏的是字母原文，是英文段最常见的打法，但
//   我们的 TIP 现在无条件透传回车；而「本该被吃的键透传出去」是一条正常代码路径
//   （组字失败时 OnKeyDown 会主动改口透传）。那一下回车漏出去就是**把半截话发出去**，
//   空格漏出去只是多一个空格。失败模式不对称，所以这条不做。
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

import { tokenize, keyFor, typable, whyUntypable } from './pinyin.mjs'
import { segmentWords } from './lexicon.mjs'

/**
 * 「这段文案打不出来」—— 文案层面的失败，与排版器自己坏了是两回事。
 *
 * 类型要分开，因为处理方式相反：前者换种子重采一万次也一样，该**跳过这条**继续；
 * 后者（配置缺参数、平台名写错、pinyin-pro 对不齐）每一条都会失败，该**停下来让人看见**。
 * `compose` 只 catch 这一个类型，其余照抛。先前靠 try 恰好包在 composeOnce 上碰巧
 * 做到了这一点，没有类型保证 —— 哪天有人把一个校验挪进 composeOnce，它就会被吞成
 * 「这条文案有问题」，生产侧逐条跳过、一直跳，把一次故障掩盖成一堆坏文案。
 */
export class UntypableError extends Error {
  constructor(message) {
    super(message)
    this.name = 'UntypableError'
  }
}

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
    if (!typable(t)) bad.push({ i, ch: t.ch, why: whyUntypable(t) })
  })
  if (bad.length) {
    const detail = bad.map((b) => `第 ${b.i + 1} 个字元 ${JSON.stringify(b.ch)}：${b.why}`).join('；')
    throw new UntypableError(`文案含 ${bad.length} 个当前方案打不出的字元 —— ${detail}`)
  }
  const out = []
  let buf = []
  let lat = []

  /**
   * 英文段：连续字母整段一次上屏。
   *
   * 不切 —— 真人不会把一个英文词拆成两次上屏，而且这里也没有「词边界」这种东西
   * 可依（汉字那套「只在词边界上合并」的理由在英文上没有对应物）。
   *
   * `pinyin` 存小写键序、`text` 存原文，两者只在大小写上可能不同 —— 这正是
   * 「按什么键」与「出什么字」的分离。`syllables` 只有一项，于是 planner 算出的
   * splits 为空、组字区不画音节分隔撇号；真输入法打英文时也不画。
   */
  const flushLatin = () => {
    if (lat.length === 0) return
    const text = lat.join('')
    lat = []
    out.push({
      kind: 'ime',
      latin: true,
      text,
      pinyin: text.toLowerCase(),
      chars: text.length,
      syllables: [text.length],
    })
  }

  const emit = (take) => {
    const missing = take.filter((t) => !t.py)
    if (missing.length) {
      // 拼音为空的汉字打不出来。旧版会静默产出 pinyin 为空的 ime 段，
      // 计划里 keys=0、只剩一个 commit 键 —— 真机上 composition 为空时按下它，
      // 屏幕直接落一个字面字符。宁可在这里显式失败。
      throw new UntypableError(
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
      flushLatin()
      buf.push(t)
      continue
    }
    if (t.kind === 'latin') {
      flushHan()
      lat.push(t.ch)
      continue
    }
    flushHan()
    flushLatin()
    const k = keyFor(t) // 前置校验已保证非 null
    // passthrough 来自键位表，见 pinyin.mjs 的 keyFor —— 判据是「这个键在美式布局上
    // 按下去，出来的就是这个字元吗」。是就透传（网页看到普通 keydown + insertText，
    // type1），不是就得走组字让 TIP 上屏（type2）。
    //
    // **这件事离线模型一直知道**（synth 靠它展开事件流），但从来没告诉过 TIP，
    // 而 TIP 把空格 / 数字 / OEM 标点键一律当上屏键吃掉。2026-09-01 真机实测确认了
    // 后果：一个字面数字就让 verify-capture 的五项里四项对不上（input 29→30、
    // compositionstart/end 5→6、keyup 55→56）。屏幕上的字是对的，歪的是我们自己的尺子。
    // 见 lab/calibrate/baseline/tip-win-digit-2026-09-01.json。
    out.push({
      kind: 'direct', text: t.ch, code: k.code, shift: !!k.shift,
      passthrough: k.passthrough,
    })
  }
  flushHan()
  flushLatin()
  return out
}

/**
 * 段序列的粒度统计 —— 与基线比对用。
 *
 * **英文段不进 perCommitChars / meanChars。** 基线那条 1.85 字/次量的是汉字上屏，
 * 而一个英文词一次上屏好几个字母（base 一次 4 个），混进去会把分布往右拽，
 * 让「跟基线差多少」这个数失去意义。单列一栏，不静默污染。
 */
export function granularity(segs) {
  const ime = segs.filter((s) => s.kind === 'ime' && !s.latin)
  const hist = {}
  for (const s of ime) hist[s.chars] = (hist[s.chars] || 0) + 1
  const n = ime.length || 1
  return {
    imeSegments: ime.length,
    latinSegments: segs.filter((s) => s.latin).length,
    // 减法算不出来了 —— 英文段既不是汉字 ime 段也不是 direct 段
    directSegments: segs.filter((s) => s.kind === 'direct').length,
    perCommitChars: hist,
    meanChars: +(ime.reduce((a, s) => a + s.chars, 0) / n).toFixed(3),
    meanKeys: +(ime.reduce((a, s) => a + s.pinyin.length + 1, 0) / n).toFixed(3),
  }
}
