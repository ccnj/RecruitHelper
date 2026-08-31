// 汉字 → 拼音。薄封装，可替换。
//
// **先对整句求拼音，再切分** —— 顺序不能反。pinyin-pro 靠词组识别处理多音字：
//   整句  '重要的会议要重新安排' → zhong yao … chong xin   ✅
//   逐字  同一段                → zhong yao … zhong xin    ❌
// 先切分再逐段求拼音会丢掉跨切点的词组信息，多音字必错。

import { pinyin } from 'pinyin-pro'

const DIGIT = /[0-9０-９]/
const LATIN = /[A-Za-z]/
/** containsChinese 认定的「中文」区间之一 —— 全角与 CJK 标点。它们是 cnTextCount 的来源 */
const CJK_PUNCT = /[　-〿＀-￯]/

/**
 * 把文案切成带拼音的字元序列。
 *
 * 用 type:'all' —— 它**逐字元**返回，每项自带 origin 与 isZh，天然与 [...text] 一一对应。
 *
 * 一稿用的是 type:'array' + nonZh:'consecutive'，那个要法会把连续的非汉字**合并成一项**
 * （"做java开发" 只回 4 项而原文 7 个字元），于是得自己把它摊回逐字。当时的摊法是
 * 「看这一项长得像不像拼音（全小写英文字母）」—— 靠猜，两个方向都会错：
 *
 *   "java" 长得像拼音 → 被当成某个汉字的读音，其后汉字全部**左移**一位
 *                        （做java开发 → 开=java、发=kai；base在深圳 → 在=base）
 *   "nü" 不全是 [a-z] → 被当成非汉字，该字读音丢失，其后**右移**一位
 *                        （招女生做旅游 → nv 丢失，旅=you，行业段拼音直接空掉）
 *
 * 根子不在补哪个字符的判断，而在**信息流向反了**：让一份压缩过的数据去决定
 * 「哪个字元是汉字」，而这件事 pinyin-pro 自己就能直接告诉我们。换成逐字接口后，
 * ü、英文、全角、emoji 代理对这一整类错位一次性消失，不需要逐种打补丁。
 *
 * @returns {Array<{ch, py, kind}>} kind: han | cjkPunct | asciiPunct | digit | latin | space | newline | other
 *          py 仅 han 非空；han 而 py 为空表示该字不在词典里（见 segment 的显式失败）
 */
export function tokenize(text) {
  const all = pinyin(text, { type: 'all', toneType: 'none' })
  const chars = [...text]
  if (all.length !== chars.length) {
    // 一一对应是这个接口的前提。不成立就是我们对它的理解出了问题 ——
    // 显式失败，绝不退回「猜」的老路。
    throw new Error(
      `pinyin-pro 返回 ${all.length} 项，字元 ${chars.length} 个，无法一一对应；` +
        `文案片段: ${JSON.stringify(text.slice(0, 30))}`
    )
  }
  return all.map((it) => {
    const ch = it.origin
    let kind
    if (ch === '\n') kind = 'newline'
    else if (/\s/.test(ch)) kind = 'space'
    else if (it.isZh) kind = 'han'
    else if (CJK_PUNCT.test(ch)) kind = 'cjkPunct'
    else if (DIGIT.test(ch)) kind = 'digit'
    else if (LATIN.test(ch)) kind = 'latin'
    else if (/^[\x20-\x7e]$/.test(ch)) kind = 'asciiPunct'
    // 生僻汉字（𰻞 㸚 𠮷）、emoji、外文 —— pinyin-pro 一律判为非汉字。
    // 单列出来，错误信息才说得准；一稿把它们混进 asciiPunct，
    // 报错会说「半角标点需切换输入法模式」，把人往错方向指。
    else kind = 'other'
    return { ch, py: kind === 'han' ? toKeyboardPinyin(it.pinyin) : null, kind }
  })
}

/**
 * 拼音 → 键盘上实际要敲的字母。
 * ü 在拼音输入法里打 v（nü→nv、lü→lv）；j/q/x/y 后的 ü 本就写作 u，不受影响。
 */
function toKeyboardPinyin(py) {
  const s = String(py || '').replace(/ü/g, 'v')
  return /^[a-z]+$/.test(s) ? s : ''
}

/**
 * 标点 → 中文输入法下的物理键 **及修饰键**。
 *
 * shift 不能省：美式键盘上「？」是 Shift+/，不带 Shift 打出来的是「/」。
 * 这是 2026-08-21 真机注入实测抓到的 —— 计划里写「？」，屏幕上出来的是「/」，
 * 因为映射只记了键位。「，」「。」无需 Shift（实测 Comma 直接出「，」），
 * 但「？！：（）《》—…" 」这一批都要。
 */
export const PUNCT_KEY = {
  '，': { code: 'Comma' },      '。': { code: 'Period' },
  '、': { code: 'Backslash' },  '；': { code: 'Semicolon' },
  '’': { code: 'Quote' },       '‘': { code: 'Quote' },
  '？': { code: 'Slash', shift: true },      '！': { code: 'Digit1', shift: true },
  '：': { code: 'Semicolon', shift: true },  '～': { code: 'Backquote', shift: true },
  '（': { code: 'Digit9', shift: true },     '）': { code: 'Digit0', shift: true },
  '《': { code: 'Comma', shift: true },      '》': { code: 'Period', shift: true },
  '—': { code: 'Minus', shift: true },       '…': { code: 'Digit6', shift: true },
  '“': { code: 'Quote', shift: true },       '”': { code: 'Quote', shift: true },
}
export const digitKey = (ch) => 'Digit' + String(ch).replace(/[０-９]/, (c) => String.fromCharCode(c.charCodeAt(0) - 0xfee0))

/**
 * 字元 → 键位。**没有兜底**：打不出来就返回 null，由调用方显式失败。
 *
 * 一稿在这里写了 `?? { code: 'Comma' }`，于是任何没收录的字元都被静默打成「，」。
 * 危害远不止几个 ASCII 标点 —— pinyin-pro 把生僻汉字（𰻞 㸚 𠮷）、emoji、
 * 外文字母一律判为非汉字，它们全都会走到这条兜底上。实测「你好𰻞方便聊聊吗」
 * 排版返回 ok=true、emit-plan 退出码 0，真机上却打出「你好，方便聊聊吗」——
 * 错误被完全掩盖，连查都没处查。
 *
 * 换个默认值治不了本。根子是这张映射表**不完备**，而代码假装它完备。
 * 正确做法是把「能不能打」变成显式属性：认识就给键位，不认识就说不认识。
 *
 * 当前方案（借用系统中文输入法）的能力边界：
 *   ✅ 汉字（走 IME）· 中文全角标点 · 数字 · 空格 · 换行
 *   ❌ 半角 ASCII 标点、英文字母 —— 它们要求切换输入法模式，
 *      而「输入法状态」这个概念当前模型里没有。这属于 TSF 那一轮的能力
 *      （自研 TIP 想上屏什么就上屏什么，不需要切模式），不在此处打补丁。
 *
 * @returns {{code: string, shift?: boolean} | null}
 */
export function keyFor(tok) {
  switch (tok.kind) {
    case 'han':
      return null // 汉字走 composition，不经这里
    case 'space':
      return { code: 'Space' }
    case 'newline':
      // 聊天框里换行是 Shift+Enter（裸 Enter 会发送）
      return { code: 'Enter', shift: true }
    case 'digit':
      return { code: digitKey(tok.ch) }
    case 'cjkPunct':
      return PUNCT_KEY[tok.ch] ?? null
    default:
      // asciiPunct / latin / 以及被 pinyin-pro 判为非汉字的生僻字与 emoji
      return PUNCT_KEY[tok.ch] ?? null
  }
}

/** 为什么打不出来 —— 供错误信息使用 */
export function whyUntypable(tok) {
  switch (tok.kind) {
    case 'latin':
      return '英文字母需切换输入法模式（TSF 轮次解决）'
    case 'asciiPunct':
      return '半角标点需切换输入法模式；改用对应全角标点即可'
    case 'cjkPunct':
      return '该全角标点尚未收录键位（可补进 PUNCT_KEY）'
    case 'han':
      return '该汉字无拼音，输入法打不出'
    default:
      return '生僻字 / emoji / 外文字符，输入法打不出'
  }
}
