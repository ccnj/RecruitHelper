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
 * @returns {Array<{ch, py, pyWhy, kind}>} kind: han | cjkPunct | asciiPunct | digit | latin | space | newline | other
 *          py 仅 han 非空；han 而 py 为空表示这个字当前打不出，pyWhy 说明是哪一种
 *          （无拼音 / 注音不是可输入音节），由 segment 的显式失败原样报出
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
    // 报错会拿半角标点那一套说事（「改用对应的全角标点即可」），把人往错方向指。
    else kind = 'other'
    const kb = kind === 'han' ? toKeyboardPinyin(ch, it.pinyin) : { py: null, why: null }
    return { ch, py: kb.py, pyWhy: kb.why, kind }
  })
}

/**
 * 普通话**可输入音节**表，键盘形式（ü 已写成 v）。
 *
 * 为什么要有它：本模块的契约是「键盘上实际要敲的字母」，而 pinyin-pro 给的是
 * **词典注音**。两者绝大多数时候相同，但不总是 —— 「嗯」的注音是 `ng`
 * （《现代汉语词典》注 ń/ňg/ǹg），真人打的是 `en`。先前这里只处理了 ü→v 一种
 * 「注音≠输入音」的情况，其余照单全收，于是「嗯嗯」被排成 KeyN,KeyG,KeyN,KeyG
 * —— 一个没有真人会敲的键序，而且计划照样返回 ok。
 *
 * 表是**硬编码**的，不用声母×韵母规则生成：音节集是封闭且不变的语言事实，
 * 而规则化容易 overgenerate（第一版校验的正则就漏了 j/q/x/y 后的 üe，
 * 把 xue/jue/que/yue 一并误判成打不出）。
 *
 * 交叉验证是免费的、且已进闸门（lab/probe/pinyin-typable.mjs）：pinyin-pro 在
 * CJK 基本区产出的全部读音，除 `ng`/`m` 外必须逐个落在本表内。
 */
const SYLLABLES = new Set(`
  a ai an ang ao o ou e ei en eng er
  yi ya yo ye yao you yan yin yang ying yong yu yue yuan yun
  wu wa wo wai wei wan wen wang weng
  ba bo bai bei bao ban ben bang beng bi bie biao bian bin bing bu
  pa po pai pei pao pou pan pen pang peng pi pie piao pian pin ping pu
  ma mo me mai mei mao mou man men mang meng mi mie miao miu mian min ming mu
  fa fo fei fou fan fen fang feng fu fiao
  da de dai dei dao dou dan den dang deng dong di dia die diao diu dian ding du duan dui dun duo
  ta te tai tei tao tou tan tang teng tong ti tie tiao tian ting tu tuan tui tun tuo
  na ne nai nei nao nou nan nen nang neng nong ni nie niao niu nian nin niang ning nu nuan nun nuo nv nve
  la lo le lai lei lao lou lan lang leng long li lia lie liao liu lian lin liang ling lu luan lun luo lv lve
  ga ge gai gei gao gou gan gen gang geng gong gu gua guai guan guang gui gun guo
  ka ke kai kei kao kou kan ken kang keng kong ku kua kuai kuan kuang kui kun kuo
  ha he hai hei hao hou han hen hang heng hong hu hua huai huan huang hui hun huo
  ji jia jie jiao jiu jian jin jiang jing jiong ju jue juan jun
  qi qia qie qiao qiu qian qin qiang qing qiong qu que quan qun
  xi xia xie xiao xiu xian xin xiang xing xiong xu xue xuan xun
  zha zhe zhi zhai zhei zhao zhou zhan zhen zhang zheng zhong zhu zhua zhuai zhuan zhuang zhui zhun zhuo
  cha che chi chai chao chou chan chen chang cheng chong chu chua chuai chuan chuang chui chun chuo
  sha she shi shai shei shao shou shan shen shang sheng shu shua shuai shuan shuang shui shun shuo
  re ri rao rou ran ren rang reng rong ru rua ruan rui run ruo
  za ze zi zai zei zao zou zan zen zang zeng zong zu zuan zui zun zuo
  ca ce ci cai cao cou can cen cang ceng cong cu cuan cui cun cuo
  sa se si sai sao sou san sen sang seng song su suan sui sun suo
`.split(/\s+/).filter(Boolean))

/**
 * 词典注音 → 真人实际敲的键序。**只收「注音本身不是可输入音节」的字**。
 *
 * 不是多音字表，也不是口语音表 —— 那些 pinyin-pro 自己按词组选，选得对不对
 * 影响的是「像不像真人的输入习惯」，是另一件事。这里只管一件：注音压根敲不出来。
 */
const INPUT_PY = {
  // 注音 ng；真人打 en。**2026-08-31 真机确认**（微软拼音 en 的候选里有「嗯」）。
  嗯: 'en',
  // 「呣」（注音 m）不收：m 不是完整音节，IME 会把它当声母等韵母，真人也打不出，
  // 那就该走显式失败，而不是编一个假的键序。
}

/**
 * 拼音 → 键盘上实际要敲的字母。
 * ü 在拼音输入法里打 v（nü→nv、lü→lv）；j/q/x/y 后的 ü 本就写作 u，不受影响。
 *
 * **打不出来就说打不出来，不猜。** 返回 why 而不是空串，是因为「这个字没有拼音」
 * 与「这个字的注音不是可输入的音节」是两种不同的失败，错误信息不该混。
 *
 * @returns {{py: string|null, why: string|null}}
 */
function toKeyboardPinyin(ch, rawPy) {
  const override = INPUT_PY[ch]
  if (override) return { py: override, why: null }
  const s = String(rawPy || '').replace(/ü/g, 'v')
  if (!/^[a-z]+$/.test(s)) return { py: null, why: '该汉字无拼音，输入法打不出' }
  if (!SYLLABLES.has(s)) {
    return { py: null, why: `注音「${rawPy}」不是可输入的音节 —— 真人敲不出这个键序，` +
      '要么在 pinyin.mjs 的 INPUT_PY 里给出它的输入音，要么就是打不出' }
  }
  return { py: s, why: null }
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
 * 当前方案（自研 TSF 输入法）的能力边界：
 *   ✅ 汉字（走 IME）· 中文全角标点 · 数字 · 空格 · 换行
 *   ✅ 英文字母 —— 但**不经这个函数**。它和汉字走同一条路（字母键进组字串、
 *      上屏键出词），键位由 segment.mjs 按字母直取，故这里对 latin 仍返回 null：
 *      谁把 latin 送到这儿来，谁就走错路了。可打性问下面的 `typable`。
 *   ❌ 半角 ASCII 标点 —— 真微软拼音下它走 insertText 直接上屏（type1），
 *      而我们的 TIP 会把这些键当上屏键吃掉、走 composition（type2）。形状对不上，
 *      且差异就落在上报载荷里（typingType / typingFragment）。要支持得给管道协议
 *      加「这个词透传、别吃」的标记，那是另一轮的事，不在此处打补丁。
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
      // asciiPunct / 以及被 pinyin-pro 判为非汉字的生僻字与 emoji。
      // latin 也会落到这里并得到 null —— 这是对的，它根本不该走 keyFor。
      return PUNCT_KEY[tok.ch] ?? null
  }
}

/**
 * 这个字元打不打得出来。**可打性的唯一出处。**
 *
 * 先前这个判断散在 segment 的前置校验里，写成「汉字看 py、其余看 keyFor」——
 * 于是「怎么打」和「能不能打」被绑死在一起。英文字母打得出来，但它不经 keyFor
 * （走 composition），在那种写法下就只能特判一次。收成一个函数，调用方问的是
 * 「能不能打」，不必知道是哪条路。
 */
export function typable(tok) {
  switch (tok.kind) {
    case 'han':
      return !!tok.py
    // 字母全部可打：A–Z / a–z 都落在 KeyA–KeyZ 上，大写也按同一个物理键
    // （不排 Shift，大写靠上屏的词本身带 —— 理由见 segment.mjs 的英文段一节）。
    case 'latin':
      return true
    default:
      return !!keyFor(tok)
  }
}

/** 为什么打不出来 —— 供错误信息使用 */
export function whyUntypable(tok) {
  switch (tok.kind) {
    // latin 不在这里 —— 它一律可打，走不到这个函数
    case 'asciiPunct':
      return '半角标点在真输入法下直接上屏、不走组字，而我们的 TIP 会把它当上屏键吃掉，'
        + '形状对不上（见 keyFor 的能力边界）；改用对应的全角标点即可'
    case 'cjkPunct':
      return '该全角标点尚未收录键位（可补进 PUNCT_KEY）'
    case 'han':
      // 具体原因由 tokenize 定：无拼音 / 注音不是可输入音节，两者不该混为一谈
      return tok.pyWhy ?? '该汉字无拼音，输入法打不出'
    default:
      return '生僻字 / emoji / 外文字符，输入法打不出'
  }
}
