// 文案清理 —— 把打不出来的字元摘掉，并**如实报出摘了什么**。
//
// 为什么有这一层：真跑业务时，一条招呼语里混进一个 emoji 不该让这一条停下来。
// 而排版器的契约是「打不出就说打不出」（那条规矩是有来历的：一稿给未收录字元兜底成
// 「，」，于是屏幕上出错字、compose 还返回 ok=true，错误无处可查）。两条都要，
// 那就得分层：**清理在上游，排版器保持严格**。排版器仍然是最后一道防线，
// 用来抓「清理器漏了什么」，而不是自己去猜。
//
// ── 只删，不改写 ──
//
// 先前设计过一档「语义替换」（`-`→`～`、`/`→`、`），理由是删掉 `20-35` 里的 `-`
// 会变成 `2035`，是另一个数字。那一档现在**取消**了：半角标点已经打得出来
// （走透传，见 pinyin.mjs 的 ASCII_KEY），根本轮不到替换。
//
// 也不做半角→全角的归一化。它一度是第一档，理由是「真输入法按 `,` 出的就是「，」」——
// 那个理由仍然成立，但它现在只关乎**像不像真人**，不关乎能不能打。而按字符无脑转会
// 误伤 `a@b.com`、`.NET`、`20-35`：该不该转取决于上下文，那是脑生成文案时才有的信息。
// 所以这件事留给生成侧一句提示词，不放在这里猜。
//
// ── 报出来的东西是必须的，不是日志 ──
//
// 文案由脑生成。脑写「你好😊」、实际发出去的是「你好」，而脑的对话记忆里存的是原文；
// 候选人回复之后，它会基于一段自己从没发出去过的历史继续往下写。所以 `dropped`
// 必须回到脑，让它拿清理后的文案更新记忆 —— **发出去的才是事实**。

import { tokenize, typable, whyUntypable } from './pinyin.mjs'

/**
 * @returns {{text: string, dropped: Array<{i, ch, kind, why}>}}
 *   text    实际打得出来的文案（可能是空串 —— 那是调用方要处理的一种失败）
 *   dropped 摘掉了哪些字元，带下标与原因
 */
export function sanitize(text) {
  const toks = tokenize(text)
  const kept = []
  const dropped = []
  toks.forEach((t, i) => {
    if (typable(t)) kept.push(t.ch)
    // kind 要带出去：删掉一个 emoji 和删掉一个**汉字**是两件事。前者是日常，
    // 后者意味着要么真是生僻字，要么拼音层出问题了（B-1 那个错位 bug 的形状）。
    // 后一种的护栏在闸门里 —— pinyin-typable.mjs 扫 CJK 基本区全部 20853 个汉字、
    // 断言打不出的恰好 1 个（呣），坏了会在提交前就红。所以这里放心删，
    // 但把 kind 报出去，让上面看得见。
    else dropped.push({ i, ch: t.ch, kind: t.kind, why: whyUntypable(t) })
  })
  return { text: kept.join(''), dropped }
}

/** 一行摘要，给日志用 */
export function describeDropped(dropped) {
  if (!dropped.length) return ''
  const han = dropped.filter((d) => d.kind === 'han').length
  return `摘掉 ${dropped.length} 个字元（${dropped.map((d) => JSON.stringify(d.ch)).join(' ')}）` +
    (han ? ` —— 其中 ${han} 个是汉字，值得看一眼` : '')
}
