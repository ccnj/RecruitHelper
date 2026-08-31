// 采集口径 —— TypingTracker / InputTracker 状态机的精确复刻。
//
// 逐行对照 evidence/deobfuscated/sec-entry.deob.js:1617-2100。
//
// **输入永远是事件流**，两个方向共用这一份口径：
//   正向  排版器计划 → synth.mjs 合成事件流 → 本模块 → OptionParams → oracle 判定
//   反向  真人操作   → 浏览器真实事件流   → 本模块 → 四条序列 → 基线分布
// 口径只有一份，正反不会漂。
//
// 与原实现的唯一结构差异：原实现在事件回调里调 `_.now()`（= Date.now），
// 因为事件是实时到达的；这里时间戳由事件自带（`ev.t`），以便离线重放与合成。
//
// engine/ 的纪律（lab/README.md）：不 import lab/ 之外的东西、不读 evidence/、
// 依赖从外部注入。quorble/flimbot 正是注入项 —— 原实现同样是从
// `window.zpAegis` 取的（sec-entry.deob.js:1719/1806），不是自己算的。

// ── 口径内的判定函数（原样复刻）─────────────────────────────────────────────

/** sec-entry.deob.js:1289 */
export const containsChinese = (s) => /[一-鿿＀-￯　-〿]/.test(s)

/** sec-entry.deob.js:1300 —— 注意汉字区间是 4e00-9fa5，比 containsChinese 窄 */
export const isChineseOrPunctuationOrNumber = (s) =>
  /[一-龥]/.test(s) || /[　-〿＀-￯]/.test(s) || /\d|[０-９]/.test(s)

/** sec-entry.deob.js:1617 —— 决定一次 keydown 是否足以创建 TypingTracker */
export function isPrintableKey(e) {
  const k = e.key || ''
  const n = e.keyCode || e.which || 0
  if (k === ' ' || n === 32) return true
  if ((k === 'Enter' || n === 13) && (e.shiftKey || e.ctrlKey)) return true
  if (k === 'Backspace' || n === 8 || k === 'Delete' || n === 46) return true
  if (k.length === 1 && !e.ctrlKey && !e.metaKey) return true
  if (!e.ctrlKey && !e.metaKey) {
    if ((n >= 48 && n <= 90) || (n >= 96 && n <= 111) || (n >= 186 && n <= 192) || (n >= 219 && n <= 222))
      return true
    if (k.length === 1 && /[ -~ -ÿ]/.test(k)) return true
  }
  return false
}

/** sec-entry.deob.js:1511 —— p7 用；阈值 0.6，输入是物理键码序列 */
export function findDominantString(list, threshold = 0.6) {
  const m = new Map()
  for (const v of list) if (v) m.set(v, (m.get(v) || 0) + 1)
  let total = 0
  for (const c of m.values()) total += c
  if (total === 0) return null
  for (const [k, c] of m) if (c / total > threshold) return [k, c / total]
  return null
}

// ── TypingTracker ──────────────────────────────────────────────────────────

export class TypingTracker {
  /**
   * @param {object} o
   * @param {number} o.startTime 构造时刻（原实现是 _.now()）
   * @param {string} [o.startText] inputEl.innerText 的构造时快照
   * @param {string} [o.module]    '' | 'chatJob'
   */
  constructor({ startTime, startText = '', module = '' }) {
    this.typings = []
    this.keydownRecords = []
    this.keyboardPairs = []
    this.compositionstart = 0
    this.startText = startText
    this.start = startTime
    this.end = startTime
    this.isTyping = false
    this.isComposing = false
    this.isDeleteing = false
    this.module = module
    this.inputRhythms = { lastTime: 0, rhythms: [], datas: [] }
  }

  /** :1632 —— 注意 type 1 的 duration 恒为 0 */
  recordInput(e, now) {
    this.isTyping = true
    this.typings.push({
      type: 1,
      data: e.data || '',
      time: now,
      duration: 0,
      inputType: e.inputType,
      isComposing: e.isComposing,
    })
    this.endTyping(now)
  }

  /** :1641 —— 只有 compositionend 入 typings；compositionstart 只记时刻 */
  recordComposition(e, now) {
    this.isTyping = true
    if (e.type === 'compositionstart') {
      this.isComposing = true
      this.compositionstart = now
    } else if (e.type === 'compositionend') {
      this.isComposing = false
      this.typings.push({
        type: 2,
        data: e.data,
        time: now,
        duration: this.compositionstart ? now - this.compositionstart : 0,
        inputType: 'compositionText',
        isComposing: false,
      })
      this.compositionstart = 0
      this.endTyping(now)
    }
  }

  /** :1653 —— 每次 input 事件都记，**含 composing 中**。与 typings 是两条不同的序列 */
  recordInputRhythms(e, now) {
    const r = this.inputRhythms
    if (r.lastTime) r.rhythms.push(now - r.lastTime)
    r.datas.push(e.data || '')
    r.lastTime = now
  }

  /** :1659 */
  recordKeyboard(kind, e, now) {
    if (kind === 'keydown') {
      this.keydownRecords.push({
        key: e.code || e.keyCode,
        functionKeys: `${e.ctrlKey},${e.shiftKey},${e.metaKey}`,
        repeat: e.repeat,
        time: now,
      })
    } else if (kind === 'keyup') {
      this.pairKeys(e, now)
    }
  }

  /**
   * :1740 —— 倒序找同 key 的 keydown，跳过 repeat=true（但计 repeatCount），
   * 配对后**删除全部同 key 的 keydownRecords**（不只是配上的那条）。
   */
  pairKeys(e, now) {
    const key = e.code || e.keyCode
    let repeatCount = 0
    for (let i = this.keydownRecords.length - 1; i >= 0; i--) {
      const rec = this.keydownRecords[i]
      if (key !== rec.key) continue
      if (!rec.repeat) {
        this.keyboardPairs.push({
          key,
          functionKeys: rec.functionKeys,
          startTime: rec.time,
          endTime: now,
          duration: now - rec.time,
          repeatCount,
        })
        this.keyboardPairs.sort((a, b) => a.startTime - b.startTime)
        this.keydownRecords = this.keydownRecords.filter((x) => x.key !== key)
        break
      }
      repeatCount++
    }
  }

  /** :1666 —— 原实现还挂了 1s 的 setTimeout 收尾，离线重放不需要计时器 */
  endTyping(now) {
    this.end = now
  }

  setDeleteing(v) {
    this.isDeleteing = v
  }

  /** :1694 —— typings 相邻 time 差（type 1 与 2 都算） */
  inputRhythm() {
    const r = []
    this.typings.forEach((t, i) => {
      if (i > 0) r.push(t.time - this.typings[i - 1].time)
    })
    return r.join(',')
  }

  /** :1731 —— keyboardPairs 相邻 startTime 差（已按 startTime 排序） */
  keyboardRhythm() {
    const r = []
    this.keyboardPairs.forEach((p, i) => {
      if (i > 0) r.push(p.startTime - this.keyboardPairs[i - 1].startTime)
    })
    return r.join(',')
  }

  keydurations() {
    return this.keyboardPairs.map((p) => p.duration).join(',')
  }

  /** :1703 —— 只统计 type===1 的 typings；rhythm 与 rhythms 是两条不同序列 */
  inputEventTrait({ quorble, flimbot }) {
    const ins = this.typings.filter((t) => t.type === 1)
    const acc = { cnTextCount: 0, rhythm: [], textLength: 0 }
    ins.reduce((e, t, i) => {
      if (i > 0) e.rhythm.push(t.time - ins[i - 1].time)
      if (containsChinese(t.data)) e.cnTextCount++
      e.textLength += t.data?.length ?? 0
      return e
    }, acc)
    const matchTrait =
      this.inputRhythms.datas.every((d) => d.length === 1 && isChineseOrPunctuationOrNumber(d)) &&
      acc.cnTextCount >= 8
    const rhythmsCsv = this.inputRhythms.rhythms.join(',')
    return {
      cnTextCount: acc.cnTextCount,
      rhythm: acc.rhythm.join(','),
      textLength: acc.textLength,
      rhythms: rhythmsCsv,
      allInput: ins.length === this.typings.length,
      isSingleChar: ins.length > 0 && ins.every((e) => e.data?.length === 1),
      quorble: quorble(this.inputRhythms.rhythms),
      flimbot: flimbot(this.inputRhythms.rhythms),
      matchTrait,
    }
  }

  /**
   * :1764-1830 —— reportTyping 的数据组装部分（去掉 APM 上报与 debounce）。
   * @param {object} deps {quorble, flimbot} 由外部注入（原实现取自 window.zpAegis）
   * @param {string} [elText] 输入框当前 innerText；缺省则由 typings 推导
   */
  buildOptionParams({ quorble, flimbot }, elText) {
    const text = this.typings.map((t) => t.data).join('')
    const tail = (elText ?? this.startText + text).slice(this.startText.length)
    const dur = this.end - this.start
    const typingType = this.typings.map((t) => t.type).join('')
    const speed = text.length && dur > 0 ? parseFloat((dur / text.length).toFixed(2)) : 0
    const keyWordRatio = parseFloat((this.keyboardPairs.length / text.length).toFixed(2))
    const keydurations = this.keydurations()
    const cn = containsChinese(text)
    const compositionAbnormal = cn && !this.typings.some((t) => t.type === 2)

    const INPUT_TYPE_CODE = { insertText: 1, compositionText: 2 }
    const typingFragment = this.typings.reduce((acc, t, i) => {
      const kind = t.data == null ? 0 : containsChinese(t.data) ? 2 : 1
      const it = INPUT_TYPE_CODE[t.inputType] || t.inputType
      const seg = `${t.type},${kind},${t.data?.length ?? 0},${t.duration},${Number(t.isComposing)},${it}`
      return i === 0 ? seg : `${acc}|${seg}`
    }, '')

    const moduleTag = this.module === 'chatJob' ? 'lorem' : undefined
    const typingInfo =
      `${speed}|(${text.length},${tail?.length})|${dur}|${compositionAbnormal}|` +
      `${this.keyboardPairs.length}|${this.keydownRecords.length}|${keyWordRatio}|${moduleTag}`

    const trait = this.inputEventTrait({ quorble, flimbot })
    const keys = this.keyboardPairs.map((p) => p.key)

    return {
      module: this.module,
      inputRhythm: this.inputRhythm(),
      keyboardRhythm: this.keyboardRhythm(),
      keydurations,
      typingFragment,
      typingInfo,
      typingType,
      containsChinese: cn,
      textLen: text.length,
      speed,
      keyWordRatio,
      compositionAbnormal,
      keyboardAbnormal: !!(this.typings.length && !this.keyboardPairs.length),
      inputTrait: {
        ...trait,
        flimbotKd: quorble(keydurations.split(',').map(Number).filter((x) => !Number.isNaN(x))),
        flimbotKr: quorble(this.keyboardRhythm().split(',').map(Number).filter((x) => !Number.isNaN(x))),
      },
      key: keys.join(','),
    }
  }
}

// ── InputTracker：事件分派 ─────────────────────────────────────────────────

/**
 * :2043-2100 的事件分派逻辑。
 * 采集抑制：粘贴 / 表情 / 常用语 / 重编辑 / 拖放期间的 input 不采样；
 * inputType 以 'delete' 开头的也不采样（只置 isDeleteing）。
 */
export class InputTracker {
  constructor({ module = '', startText = '', suppress = () => false } = {}) {
    this.module = module
    this.startText = startText
    this.suppress = suppress
    this.tracker = null
    this.sessions = []
  }

  init(now) {
    if (!this.tracker) {
      this.tracker = new TypingTracker({ startTime: now, startText: this.startText, module: this.module })
    }
    return this.tracker
  }

  /** 喂一个事件。事件形如 {t, type, ...} */
  feed(ev) {
    const now = ev.t
    switch (ev.type) {
      case 'keydown': {
        if (!this.tracker && isPrintableKey(ev)) this.init(now)
        this.tracker?.recordKeyboard('keydown', ev, now)
        break
      }
      case 'keyup':
        this.tracker?.recordKeyboard('keyup', ev, now)
        break
      case 'input': {
        if (this.suppress(ev)) break
        this.init(now)
        if (ev.inputType?.startsWith('delete')) {
          this.tracker.setDeleteing(true)
          break
        }
        this.tracker.recordInputRhythms(ev, now)
        this.tracker.isTyping = true
        if (!this.tracker.isComposing) this.tracker.recordInput(ev, now)
        break
      }
      case 'compositionstart':
      case 'compositionend':
        this.init(now)
        this.tracker.recordComposition(ev, now)
        break
      case 'compositionupdate':
        break // 原实现不监听 compositionupdate
      default:
        break
    }
    return this
  }

  feedAll(events) {
    for (const ev of events) this.feed(ev)
    return this
  }

  /** 相当于 debounce 到期后的 reportTyping()：出参并清空 tracker */
  flush(deps, elText) {
    if (!this.tracker) return null
    const p = this.tracker.buildOptionParams(deps, elText)
    this.sessions.push(p)
    this.tracker = null
    return p
  }
}
