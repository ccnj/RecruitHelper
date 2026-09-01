// 平台画像 —— 同一份计划，在不同「浏览器 + 输入法」组合下会展开成**不同的事件流**。
//
// 这些差异不是细节，每一条都改变被判定的字段。它们曾经散落在 synth.mjs 的默认参数里，
// 由三个调用点各自决定，于是排版器的重采回路验的永远是 macOS 语义（planner.mjs 无参调用）。
// 现在收敛成一处：谁要展开事件流，谁就得先说清楚是哪个平台。
//
// **每一项都由真机实测确定，不许猜。** 来源标在字段上。
// 未列出的差异见文件末尾「已知但未建模」。

/** sec-entry.deob.js:1289 的同款判定 —— 用来区分「中文标点」与 ASCII 字元 */
const containsChinese = (s) => /[一-鿿＀-￯　-〿]/.test(s || '')

export const PLATFORMS = {
  'macos-chrome-pinyin': {
    label: 'macOS · Chrome 147 · 系统简体拼音',
    source: 'lab/calibrate/baseline/human-2026-08-20.json',

    /**
     * IME 处理中 keydown 的 `key` 字段。
     * macOS 上是**实际字母**（'n'/'i'/…）—— Chromium 只把 keyCode 改写成 229，
     * 源码里那段 hack 的注释原文是「为了模仿 Windows」，key 保持原样。
     * 后果：isPrintableKey 对单字符 key 返回 true，首个拼音键就把 tracker 建起来。
     */
    imeKeyField: 'letter',
    imeKeyCode: 229,

    /** compositionend 之后是否还有一条 isComposing=false 的 input。实测 46 次里 0 次。 */
    commitInputAfterEnd: false,

    /** 一次按键几条 keyup。macOS 实测只有一条，且 keyCode 是真实键码不是 229。 */
    doubleKeyUp: false,

    /**
     * 中文标点走不走 composition。
     * macOS 简体拼音**直接 input 上屏**（type1），于是 containsChinese 为真、
     * cnTextCount=11 —— 真人的 cnTextCount 全部来自标点，汉字都走 composition 记 type2。
     */
    punctuationViaComposition: false,
  },

  'windows-chrome-mspinyin': {
    label: 'Windows · Chrome 151 · 微软拼音',
    source: 'lab/calibrate/baseline/human-win-2026-08-21.json',

    /**
     * Windows 上是字符串 "Process"（keyCode/which 均为 229，`code` 仍是物理键）。
     * 实测 416 个 keydown 里 390 个如此（其余是 Backspace/Shift/数字，IME 不吃）。
     * 后果：isPrintableKey 对 "Process" 返回 false，tracker 要等 compositionstart 才建，
     * **整个 session 的首键丢失配对**。实测 416 keydown → 415 配对，恰好丢一个。
     */
    imeKeyField: 'Process',
    imeKeyCode: 229,

    /** 实测 77 次 compositionend 里 0 次 —— 与 macOS 一致，不存在平台差异。 */
    commitInputAfterEnd: false,

    /**
     * **两条**：先 keyup(key="Process", keyCode=229)，中位 6ms 后再
     * keyup(key=真实键, keyCode=真实码)，两条的 `code` 相同。
     * 实测 807 keyup 对 416 keydown。
     *
     * 口径本身免疫（pairKeys 配对后删掉全部同 key 记录，第二条找不到东西可配），
     * 但在**按键重叠**时它会改变哪个 keydown 配上哪个 keyup：实测同一份数据，
     * 只留 Process keyup 算出 dwell quorble 0.435，两条都在是 0.354。所以必须建模。
     */
    doubleKeyUp: true,
    doubleKeyUpDelay: 6,

    /**
     * **走 composition。** 实测 77 次 compositionend 里 13 次 data 是纯中文标点。
     * 于是标点记 type2 而不是 type1，**cnTextCount = 0**（macOS 是 11），
     * `cnTextCount >= 8` 那道前置门在 Windows 上永远关闭、matchTrait 恒 false。
     *
     * 这是 synth.mjs 里 `direct: true` 那个设计的前提在 Windows 上失效的地方：
     * 排版器照样发 Comma 键，但 IME 会把它走 composition —— 若仍按 direct 展开，
     * 离线判定算的是一个真机上不存在的流形状。
     *
     * 只对中文标点成立。ASCII 数字与符号（实测 1/0/+/~）仍是直接 insertText。
     */
    punctuationViaComposition: true,
  },
}

/** 生产目标是 Windows。默认即生产。 */
export const DEFAULT_PLATFORM = 'windows-chrome-mspinyin'

export function resolvePlatform(p = DEFAULT_PLATFORM) {
  if (p && typeof p === 'object') return p
  const hit = PLATFORMS[p]
  if (!hit) throw new Error(`未知平台 ${JSON.stringify(p)}，可选：${Object.keys(PLATFORMS).join(' / ')}`)
  return hit
}

/** 本次上屏该走 composition 还是直接 input */
export function directGoesThroughIme(platform, text) {
  return !!platform.punctuationViaComposition && containsChinese(text)
}

// ── 已知但未建模的平台差异 ────────────────────────────────────────────────
//
// 1. **音节分隔符**。微软拼音在组字串里插 `'`：kan → kan'd → kan'dao。
//    macOS 简体拼音不插。它只出现在 compositionupdate / input 的 data 里，
//    而 data 唯一参与判定的地方是 matchTrait 的
//    `datas.every(d => d.length === 1 && …)` —— 组字串本来就是多字符，
//    两个平台都让它为 false。故判定后果为零，不建模。
//
// 2. **Backspace / Shift / 数字键不被 IME 吃掉**。Windows 实测这几个键的
//    keydown 仍带真实 key 与 keyCode（Backspace=8、Shift=16、数字 48-57）。
//    排版器目前不排退格，Shift 已经是普通键，数字走 direct，故无需特殊处理。
//    将来若要排「打错了退回去改」的行为，这里要补。
