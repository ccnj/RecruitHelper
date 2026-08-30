// BOSS 的输入行为检测账本:页面 localStorage 的 `_ZP_CNT_`,16 个计数器。
//
// **它是平台在盯我们打字。** 2026-08-29 真机实测:一次中文的 CDP 合成输入,
// 六项异常同时开火(见 docs/boss/BOSS输入检测实测-2026-08-29.md)。
//
// 两条使用纪律:
//
//  1. **它是批量延迟写盘的**,不是每次输入立刻落盘。读早了会看见旧值——
//     本轮已经因此得出过两个错误的机制性结论。别拿单次读的差值下定论。
//  2. **计数语义是二手的**(源自 hiBoss 的 behavior-layer-checks 与反混淆 sec 脚本),
//     只有 `verified` 标记为 true 的几项经本仓库真机对照确认过。
//     其余按「平台枚举面事实门」当考古线索,不作判据。

export interface BossInputCounter {
  /** `_ZP_CNT_` 里的键名。 */
  readonly key: string
  /** 人话。 */
  readonly label: string
  /**
   * 二手描述称「日内只增不减」,其余项会在聚合轮被清零。
   * **但跨日界的清零未观测到**:2026-08-30 读到的账本写盘于 08-29 14:42、
   * 落后 25.5 小时,`input_count` 仍是前一日的值。所以别把 lasting 读成
   * 「每天归零」——归零时机目前只有聚合轮那一条是有依据的。
   */
  readonly lasting: boolean
  /** 属于会触发聚合上报的五项之一。 */
  readonly triggersReport: boolean
  /** 本仓库真机对照确认过它在什么条件下开火。 */
  readonly verified: boolean
}

export const BOSS_INPUT_COUNTERS: readonly BossInputCounter[] = [
  { key: 'input_count', label: '总输入次数', lasting: true, triggersReport: false, verified: true },
  { key: 'speed_abnormal_count', label: '打字太快(<100ms/字)', lasting: true, triggersReport: false, verified: true },
  { key: 'composition_abnormal_count', label: '中文没走输入法', lasting: true, triggersReport: false, verified: true },
  { key: 'keyboard_abnormal_count', label: '有文字却没按键', lasting: true, triggersReport: false, verified: true },
  { key: 'key_word_ratio_abnormal_count', label: '按键数远少于字数', lasting: true, triggersReport: false, verified: false },
  { key: 'input_rhythm_abnormal_count', label: '每一下都极短(全 <=15ms)', lasting: false, triggersReport: true, verified: true },
  { key: 'input_rhythm_abnormal_200_count', label: '剔掉停顿后仍每字 <=200ms', lasting: false, triggersReport: false, verified: true },
  { key: 'input_trait_abnormal_count', label: '每次上屏都是单字', lasting: false, triggersReport: true, verified: true },
  { key: 'quorble_abnormal_count', label: '节奏太规整', lasting: false, triggersReport: false, verified: false },
  { key: 'flimbot_abnormal_count', label: '剔掉停顿后仍太规整(>0.6)', lasting: false, triggersReport: true, verified: false },
  { key: 'flimbot_abnormal_0_count', label: '同一张更宽的网(>0)', lasting: true, triggersReport: false, verified: false },
  { key: 'flimbot_kd_abnormal_count', label: '每个键按得太一致(dwell)', lasting: false, triggersReport: false, verified: false },
  { key: 'flimbot_kr_abnormal_count', label: '按键间隔太规整', lasting: false, triggersReport: false, verified: false },
  { key: 'keycode_abnormal_count', label: '键码全是 0(合成事件默认值)', lasting: false, triggersReport: true, verified: false },
  { key: 'keycode_abnormal_count_2', label: '某个物理键占比 >0.7', lasting: false, triggersReport: true, verified: false },
  { key: 'gobbledygook_count', label: '上一项的孪生计数器(值恒等)', lasting: true, triggersReport: false, verified: false },
]

/** 聚合上报的判据:`input_count % REPORT_EVERY == 0` 且 ★ 五项任一非 0。 */
export const REPORT_EVERY = 25

/** 要从页面 localStorage 取回的键。 */
export const BOSS_COUNTER_KEYS = ['_ZP_CNT_', '__local__sec__store___'] as const

/** 匹配 BOSS 页面的标签页。 */
export const BOSS_TAB_MATCH = ['https://*.zhipin.com/*'] as const
