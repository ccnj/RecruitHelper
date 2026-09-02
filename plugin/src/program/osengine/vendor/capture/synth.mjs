// 正向：输入计划 → 事件流。
//
// 计划由排版器（第 4 轮）产出；本模块只负责把「词 + 按键时刻」展开成浏览器会
// 真实产生的那串事件，再交给 tracker.mjs。合成事件流与真人事件流走同一个口径，
// 所以排版器的输出可以直接喂 oracle 判定，无需上真机。
//
// ✅ `commitInputAfterEnd` 已由**两个平台**的真人基线定案：**false**
//
//   macOS   Chrome 147 + 简体拼音   46 次 compositionend，其后 0 次
//   Windows Chrome 151 + 微软拼音   77 次 compositionend，其后 0 次
//
// 最后一个 input 落在 compositionend 之前且 isComposing=true。不存在平台差异 ——
// 这个顺序由 Blink 的平台无关代码决定，Windows 的 TSF 层与 macOS 的 Cocoa 层
// 调的是同一组 mojo 接口。
//
// 后果与先前的猜测相反：typings 里每次上屏只留一条 type2，textLen **不翻倍**，
// 于是 speed 判据的真实要求仍是 100ms/字而非 200ms。
//
// 但 cnTextCount 并不因此归零 —— 它统计的是 type1（isComposing=false 的 input）中
// containsChinese 为真的条目，而 containsChinese 的字符区间 **包含全角标点**
// (\uff00-\uffef) 与 CJK 标点 (\u3000-\u303f)。真人实测 cnTextCount=11，
// 全部来自「，、～。？」这类标点：汉字都走 composition 记为 type2，
// 只有标点与数字直接上屏记为 type1。
//
// → 合成事件流若不含标点，cnTextCount 会是 0，一整批前置门跳过，判定结果失真。
//   故 words 支持 `direct: true`（不走 composition，直接一次 input 上屏）。
//
// ⚠ **上面这段推理只对 macOS 成立。** Windows 微软拼音把中文标点也走 composition
//   （实测 77 次 compositionend 里 13 次 data 是纯中文标点），于是标点记 type2、
//   cnTextCount = 0、那批前置门在 Windows 上**永远关闭**。
//   `direct` 词在 Windows 画像下会被展开成单键 composition 段，不是直接 input。
//   见 platform.mjs。

import { resolvePlatform, directGoesThroughIme } from './platform.mjs'

/** 拼音字母 → KeyboardEvent.code */
export function letterCode(ch) {
  const c = ch.toUpperCase()
  if (c >= 'A' && c <= 'Z') return 'Key' + c
  if (c >= '0' && c <= '9') return 'Digit' + c
  if (ch === ' ') return 'Space'
  return c
}

/**
 * @param {object} plan
 * @param {number} plan.startTime 绝对起始时刻（ms）
 * @param {Array}  plan.words 每项 { text, keys: [{code, down, up}], commit: {code, down, up} }
 *        keys/commit 的 down/up 是**绝对时刻**（ms），由排版器排好
 * @param {object} [opt]
 * @param {string|object} [opt.platform] 平台画像名或对象，见 platform.mjs。
 *        缺省用 DEFAULT_PLATFORM（= Windows，生产目标）。
 *        **平台不是可选项**：同一份计划在 macOS 与 Windows 上会展开成不同的事件流，
 *        每一处差异都改变被判定的字段。逐项差异与实测来源见 platform.mjs。
 * @returns {Array} 事件流，按时间升序
 */
export function synthTyping(plan, opt = {}) {
  const P = resolvePlatform(opt.platform)
  const { commitInputAfterEnd, imeKeyCode, imeKeyField } = P
  const dblUp = P.doubleKeyUp ? (P.doubleKeyUpDelay ?? 6) : 0

  /**
   * 被 IME 吃掉的键的 keyup。
   *
   * Windows 上一般是**两条**：先 keyup(key="Process", keyCode=229)，
   * 约 6ms 后再 keyup(key=真实键, keyCode=真实码)，两条的 code 相同。
   * 口径本身免疫（pairKeys 配对后删掉全部同 key 记录），但按键重叠时它决定
   * 哪个 keydown 配上哪个 keyup，会改变 dwell 分布 —— 必须建模。
   *
   * **例外**：若这个键还按着的时候，组字被**别的键**结束了（典型情形是它被
   * 上屏键盖过去了 —— 真人快打时上一个字母的 keyup 常常拖到空格之后），
   * IME 已经丢弃了对本键的处理状态，于是只放行真实键那一条。
   *
   * 这条规则在两份真机数据上 **419 次按下、0 反例**：
   *   注入 plan1  29/29
   *   真人 127 字 390/390
   * 注意判据是「由**别的**键结束」而不是「keyup 晚于 compositionend」——
   * 标点键自己结束自己的组字，它的两条 keyup 也都在 compositionend 之后，
   * 照样双发。后一种说法会把标点段全判错。
   *
   * @param {boolean} orphaned 按住期间组字被别的键结束了
   * @param {number}  ceAt     本段 compositionend 的时刻（决定 isComposing 取值）
   */
  // 组字窗口一次算好：keyup 双发与否取决于**那一刻有没有任何组字在进行**，
  // 跟这个键属于哪一段无关。真机实测（tip-win-newline-2026-09-02）：
  //   KeyG 的 keyup 晚于「方便」的 compositionend，但「吗」的组字已经开始 → 双 keyup
  //   KeyA 的 keyup 晚于「吗」的 compositionend，其后没有新组字        → 单 keyup
  // 先前 orphan 规则只看「晚于自己段的 compositionend」，KeyG 那种会被判成单条，
  // 离线预测的 keyup 数就少一个 —— 而 keyup 参与配对，配对决定 keydurations。
  // 窗口的起止与下面展开事件时写的时刻完全一致（compositionstart 在首键 down+1；
  // compositionend 在上屏键 down+3、标点键 down+4），改一处必须改另一处。
  // 直接段走不走组字 —— **唯一出处**，窗口计算与下面的展开分支都问它。
  // 由计划说了算（排版器在 passthrough 里标好了，判据是「这个键在美式布局上打出来的
  // 就是这个字元吗」，见 pinyin.mjs 的 keyFor）；老计划（本字段之前归档的 baseline）
  // 没有它，退回按字符判，与它们当初被合成时的口径一致，归档采集的复核结果不随代码变。
  const viaImeOf = (w) => (w.passthrough == null
    ? directGoesThroughIme(P, w.text)
    : !!P.punctuationViaComposition && !w.passthrough)
  const windows = []
  for (const w of plan.words) {
    if (w.direct) {
      if (viaImeOf(w)) {
        const k = w.keys.find((x) => !x.modifier)
        if (k) windows.push([k.down + 1, k.down + 4])
      }
    } else if (w.commit && w.keys.length) {
      windows.push([w.keys[0].down + 1, w.commit.down + 3])
    }
  }
  const composingAt = (t) => windows.some(([a, b]) => t >= a && t < b)

  const pushKeyUp = (push, t, code, realKeyCode, realKey, orphaned) => {
    const composing = composingAt(t)
    if (!dblUp) {
      push(t, 'keyup', { code, keyCode: imeKeyCode ? 229 : realKeyCode, key: 'Process', isComposing: composing })
      return
    }
    if (orphaned) {
      push(t, 'keyup', { code, keyCode: realKeyCode, key: realKey, isComposing: false })
      return
    }
    push(t, 'keyup', { code, keyCode: imeKeyCode ? 229 : realKeyCode, key: 'Process', isComposing: composing })
    push(t + dblUp, 'keyup', { code, keyCode: realKeyCode, key: realKey, isComposing: composing })
  }
  const evs = []
  const push = (t, type, extra) => evs.push({ t, type, ...extra })

  for (const w of plan.words) {
    // 标点/数字：不走 IME，一次 keydown + 一次 input(isComposing=false) 直接上屏。
    // 它们是 cnTextCount 的唯一来源（见文件头说明）。
    if (w.direct) {
      // 中文标点在 Windows 上被 IME 接管、走 composition；在 macOS 上直接上屏。
      // 判定后果天差地别：前者记 type2（cnTextCount 不计），后者记 type1（cnTextCount +1）。
      // 走不走组字：由计划说了算，出处见上面的 viaImeOf。先前这里自己按字符判，
      // 与 TIP 的行为是两条独立推理 —— 而 TIP 那边压根没有这个信息、只能按键码猜，
      // 于是两边长期对不上账（2026-09-01 真机实测：一个字面数字就让五项对账里四项
      // 不一致）。现在同一个标记喂给两边。
      const viaIme = viaImeOf(w)

      // 遍历全部 keys —— 带 Shift 的字元里，ShiftLeft 是 keys[0]。
      // **修饰键必须出现在事件流里**：它有 down 有 up，pairKeys 会正常配对，
      // 于是 dwell 进 keydurations、startTime 进 keyboardRhythm、
      // "ShiftLeft" 进 key 序列参与 find_dominant_char 占比。
      // 早先的合成流只发目标键，与真机对不上账，标定会失真。
      //
      // Shift 本身**不被 IME 吃掉**（Windows 实测 keydown 仍是 key="Shift"/keyCode=16），
      // 所以两个平台、走不走 composition，它的展开方式都一样。
      for (const k of w.keys) {
        if (k.modifier) {
          push(k.down, 'keydown', { code: k.code, keyCode: 16, key: 'Shift',
            ctrlKey: false, shiftKey: true, metaKey: false, repeat: false })
          push(k.up, 'keyup', { code: k.code, keyCode: 16, key: 'Shift', shiftKey: false })
          continue
        }
        if (viaIme) {
          // 单键 composition 段。真机实测序列（两份数据一致）：
          //   keydown → compositionstart("") → [update + input] ×2 → compositionend
          //
          // **两对**，不是一对。真人 13 个标点段全是 2 条 input，注入 2/2 段亦然。
          // 这就是先前 synth 预测 rhythms 项数比真机少 2 的全部原因（每个标点段少 1 条）。
          // rhythms 是被判定的序列（trait.quorble / trait.flimbot 都算在它上面），
          // 项数少了会让离线判定算的分布不是真机那一个。
          push(k.down, 'keydown', {
            code: k.code, keyCode: imeKeyCode ? 229 : k.keyCode ?? 0,
            key: imeKeyField === 'letter' ? (k.letter ?? w.text) : 'Process',
            ctrlKey: false, shiftKey: !!k.shift, metaKey: false, repeat: false,
          })
          push(k.down + 1, 'compositionstart', { data: '' })
          for (const dt of [2, 3]) {
            push(k.down + dt, 'compositionupdate', { data: w.text })
            push(k.down + dt, 'input', { data: w.text, inputType: 'insertCompositionText', isComposing: true })
          }
          const ceAt = k.down + 4
          push(ceAt, 'compositionend', { data: w.text })
          // 标点键自己结束自己的组字 —— 不算 orphaned，照常双发
          pushKeyUp(push, k.up, k.code, k.keyCode ?? 0, w.text, false)
          continue
        }
        // 换行是唯一一个「打出来的不是它的文本」的透传段。真机实测
        // （lab/calibrate/baseline/tip-win-newline-2026-09-02.json）：
        //   keydown/keyup  key="Enter" keyCode=13（不是 "\n"）
        //   input          inputType="insertLineBreak" data=null（不是 insertText "\n"）
        // 这三处先前都是猜的（写成 "\n" / insertText / "\n"），落进 typingFragment 就是
        // `1,1,1,0,0,1` 对真机的 `1,1,0,0,0,insertLineBreak` —— 长度、inputType 两处不同，
        // 而 typingFragment 是上传字段。换行段能发出去之前它是死代码，现在不是了。
        const nl = w.text === '\n'
        const key = nl ? 'Enter' : w.text
        const keyCode = nl ? 13 : k.keyCode ?? 0
        push(k.down, 'keydown', { code: k.code, keyCode, key,
          ctrlKey: false, shiftKey: !!k.shift, metaKey: false, repeat: false })
        push(k.down + 2, 'input', nl
          ? { data: null, inputType: 'insertLineBreak', isComposing: false }
          : { data: w.text, inputType: 'insertText', isComposing: false })
        push(k.up, 'keyup', { code: k.code, keyCode, key, shiftKey: !!k.shift })
      }
      continue
    }
    const all = [...w.keys, w.commit].filter(Boolean)
    let composing = ''
    // 本段 compositionend 的时刻。仍按着的拼音键（up 晚于它）会被上屏键「盖掉」，
    // IME 丢弃对它们的处理状态，只放行真实键那一条 keyup。见 pushKeyUp。
    const ceAt = w.commit ? w.commit.down + 3 : null

    all.forEach((k, i) => {
      const isCommit = k === w.commit
      push(k.down, 'keydown', {
        code: k.code,
        // IME 吃掉按键时，keydown 的 keyCode 是 229 —— 这是真实浏览器行为，
        // 也正是「中文经输入法」在事件层最直接的痕迹。
        keyCode: imeKeyCode ? 229 : k.keyCode ?? 0,
        key: k.key ?? (imeKeyField === 'letter' ? (k.letter ?? 'Process') : 'Process'),
        ctrlKey: false,
        shiftKey: false,
        metaKey: false,
        repeat: false,
      })

      if (i === 0) push(k.down + 1, 'compositionstart', { data: '' })

      if (!isCommit) {
        // 每敲一个拼音字母：compositionupdate + input(isComposing=true)
        composing += k.letter ?? ''
        push(k.down + 2, 'compositionupdate', { data: composing })
        push(k.down + 2, 'input', {
          data: composing,
          inputType: 'insertCompositionText',
          isComposing: true,
        })
      } else {
        // 上屏：候选确定 → compositionupdate(词) → input(composing) → compositionend
        push(k.down + 2, 'compositionupdate', { data: w.text })
        push(k.down + 2, 'input', { data: w.text, inputType: 'insertCompositionText', isComposing: true })
        push(k.down + 3, 'compositionend', { data: w.text })
        if (commitInputAfterEnd) {
          push(k.down + 4, 'input', {
            data: w.text,
            inputType: 'insertCompositionText',
            isComposing: false,
          })
        }
      }

      // 上屏键自己结束组字，不算 orphaned；其余键要看 keyup 那一刻**有没有任何组字**
      // 在进行 —— 包括下一段的。见文件头 composingAt 的说明与出处。
      const orphaned = !isCommit && !composingAt(k.up)
      pushKeyUp(push, k.up, k.code, k.keyCode ?? 0, k.letter ?? '', orphaned)
    })
  }

  return evs.sort((a, b) => a.t - b.t)
}

/**
 * 把「词 + 拼音 + 时序数组」这种更朴素的描述铺成 plan.words。
 * 排版器最终会产出更讲究的时序；这个助手用于测试与手写用例。
 *
 * @param {Array} words [{text, pinyin, commit='Space'}]
 * @param {function} timing (wordIdx, keyIdx, isCommit) => {gap, dwell}
 *        gap = 距上一个 keydown 的间隔；dwell = 本键按压时长
 */
export function layKeys(words, startTime, timing) {
  let t = startTime
  let first = true
  return words.map((w, wi) => {
    if (w.direct) {
      const { gap, dwell } = timing(wi, 0, false)
      t += first ? 0 : gap
      first = false
      return { text: w.text, direct: true, keys: [{ code: w.code ?? 'Comma', down: t, up: t + dwell }] }
    }
    const letters = [...w.pinyin]
    const keys = letters.map((ch, ki) => {
      const { gap, dwell } = timing(wi, ki, false)
      t += first ? 0 : gap
      first = false
      return { code: letterCode(ch), letter: ch, down: t, up: t + dwell }
    })
    const { gap, dwell } = timing(wi, letters.length, true)
    t += gap
    const commit = { code: w.commit ?? 'Space', letter: '', down: t, up: t + dwell }
    return { text: w.text, keys, commit }
  })
}
