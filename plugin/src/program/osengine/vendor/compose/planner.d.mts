// 本文件是**我们写的**类型声明,不是上游产物。同目录的 `.mjs` 一行不改,见 ../../README.md。
//
// 它只声明我们实际调用的那一个导出(`compose`)。声明少不是偷懒:这份 `.d.mts`
// 就是我们与 hiBoss 排版器之间的契约面,上游改了签名,编译期当场红——把没用到的
// 也抄一遍,反而会在无关改动上误报。

/**
 * 计划里的一次按键。
 *
 * `down`/`up` 是**相对本次计划起点的毫秒**,不是间隔。相邻键会重叠(rollover):
 * 上一个键的 `up` 常常晚于下一个键的 `down`,真人快打时就是这样,普通字母键无害。
 *
 * `shift`/`modifier` **为假时字段不出现**(实测),所以两个都是可选。
 * 修饰键与被修饰键的关系:`？` 排成 ShiftLeft(`modifier:true`) + Slash(`shift:true`),
 * 且 ShiftLeft 的 `up` 晚于 Slash 的 `up`。下一个键必须等它松手之后至少
 * `shiftGuard`(上游 40ms)——否则输入法会收到大写字母当成英文。
 * 2026-08-21 上游真机教训:「薪资」曾因此变成「Xin子」。**时序职责在排版器**,
 * 只有它有全局视野;注入层排不开时显式报冲突,不静默出错字。
 */
export interface PlanKey {
  /** W3C `KeyboardEvent.code`,如 `KeyN` / `Space` / `Digit3` / `ShiftLeft` / `Slash`。 */
  code: string
  /** 这一键对应的拼音字母;上屏键与修饰键是空串。注入层不用,保留是为了对得上上游。 */
  letter: string
  down: number
  up: number
  /** 本键是在 Shift 按住时按下的。 */
  shift?: boolean
  /** 本键**就是**修饰键(ShiftLeft)。 */
  modifier?: boolean
}

/**
 * 一个上屏单元。
 *
 * `direct` 为真表示直接键入(英文、标点),**不走 composition**,也就没有 `commit`。
 * 其余的走输入法:敲完 `keys` 里的拼音字母,再按 `commit` 上屏。
 * `commit` 有 14% 的概率是 `Digit2/3/4`(按数字选第 N 个候选)而不是 `Space`。
 */
export interface PlanWord {
  /** 要上屏的词。自研 TIP 直接照它上屏,不查词库——候选词随机性从根上消掉。 */
  text: string
  keys: PlanKey[]
  /** 上屏键。`direct` 的词没有。 */
  commit?: PlanKey
  /**
   * 音节边界(相对拼音串起点的偏移)。**只影响 TIP 组字区的显示**——
   * 微软拼音把 nihao 显示成 ni'hao,撇号不是按出来的,是输入法画上去的。
   * 注入层自己不用,只负责转交给 TIP。
   */
  splits?: number[]
}

export interface Plan {
  startTime: number
  words: PlanWord[]
}

/** 一次重采尝试的失败记录。结构对我们不透明,原样带出去用于诊断。 */
export interface ComposeAttempt {
  seed: number
  stage: string
  reasons: unknown
}

export type ComposeResult =
  | { ok: true; plan: Plan; seed: number; tries: number; attempts: ComposeAttempt[] }
  | { ok: false; plan: null; tries: number; attempts: ComposeAttempt[] }

export interface ComposeOptions {
  /** 起始种子。排版器会从它开始逐个加一重采,所以同一个 seed 未必产出同一次尝试的计划。 */
  seed?: number
  /** 计划起点的毫秒。缺省 0。 */
  startTime?: number
  /** 最多重采多少次。缺省 40。 */
  maxTries?: number
}

/**
 * 排版:一句文案 → 一份带时刻的按键计划。
 *
 * **它是闭环自验的**:每算出一份计划,就把它合成成事件流、再量一遍参数,与真人
 * 基线对不上就换种子重来(最多 `maxTries` 次)。所以 `ok:false` 是正常返回值
 * 而不是异常——文案本身可能就排不出合格的形状。
 *
 * 纯计算:不碰 OS、不碰时钟、不等待。按时刻把它播出去是脑进程 `handinput` 的事。
 */
export declare function compose(text: string, o?: ComposeOptions): Promise<ComposeResult>
