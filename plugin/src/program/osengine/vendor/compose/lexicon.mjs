// 打字粒度词表。
//
// 为什么需要它：分词若不看词边界，会切出「便聊」这种非词组合（「方便聊聊吗」被
// 切成 方|便聊|聊吗）。两个后果——
//
//   1. 真机上输入法按拼音串出字，「bianliao」出不来「便聊」，屏幕上就是错字；
//   2. 更本质的是**真人不会这么切**。真人的上屏边界总落在词上，
//      切分本身就是「像不像人」的一部分。
//
// 第 1 条在 TSF 终态会消失（TIP 不查词库、直接上屏指定的词），第 2 条不会。
//
// pinyin-pro 内置的 4101 条词典只覆盖**含多音字**的词（实测 27 个招聘常用词只
// 命中「了解/重新/银行/行业」4 个），做不了通用分词，故自建。
//
// 规模刻意克制：只收招聘场景与通用高频词。未命中的字按单字处理 —— 单字切分
// 永远不会切出非词组合，只是粒度偏碎，属可接受的退化。

const WORDS = `
你好 您好 我是 我们 你们 咱们 这边 那边 这个 那个 这些 一下 一个 一些 什么 怎么 为什么
可以 需要 应该 能否 是否 有没有 方便 合适 匹配 不错 挺好 很好 太好 确实 的确 其实 目前
现在 今天 明天 后天 上午 下午 晚上 早上 中午 时间 什么时候 随时 稍后 马上 立刻 尽快
招聘 应聘 求职 岗位 职位 职务 工作 就业 入职 离职 在职 转正 试用 试用期 实习 全职 兼职
简历 履历 投递 筛选 面试 初试 复试 终面 笔试 面谈 沟通 联系 电话 微信 邮箱 号码
薪资 薪酬 工资 月薪 年薪 底薪 提成 奖金 绩效 补贴 福利 待遇 五险一金 社保 公积金 双休
经验 能力 技能 专业 学历 本科 硕士 大专 毕业 应届 往届 背景 履历 项目 业绩 成绩
公司 企业 团队 部门 同事 领导 老板 上级 下属 客户 用户 老师 顾问 助理 主管 经理 总监
业务 行业 领域 方向 产品 技术 研发 运营 销售 市场 设计 财务 人事 行政 客服 售后
地点 地址 城市 区域 附近 通勤 距离 远近 交通 地铁 公交 上班 下班 加班 出差 弹性
了解 知道 清楚 明白 理解 考虑 想想 看看 聊聊 谈谈 说说 问问 商量 沟通 确认 核实
安排 约定 预约 定个 定在 改到 推迟 提前 取消 通知 回复 反馈 跟进 处理 解决 帮忙
感兴趣 有兴趣 没兴趣 考虑一下 再看看 不合适 不太合适 挺合适 很匹配 很感兴趣
机会 平台 发展 前景 空间 晋升 成长 学习 培训 资源 支持 保障 稳定 靠谱 正规
情况 问题 需求 要求 条件 标准 流程 环节 结果 进展 状态 消息 通知 信息 资料 材料
如果 因为 所以 但是 不过 而且 并且 或者 还是 然后 接着 另外 此外 关于 对于 根据
麻烦 打扰 抱歉 不好意思 谢谢 感谢 辛苦 客气 没关系 没问题 好的 行的 可以的
重要 重新 银行 行业 差不多 大概 大约 左右 至少 最多 起码 基本 主要 一般 通常
微信 加上 收入 结构 路径 认识 关系 哪个 那个 分钟 小时 半天 全天 线上 线下 视频 语音
给你 给您 帮你 帮您 跟你 跟您 和你 和您 为你 为您 你的 您的 我的 他的 它的 这里 那里
一遍 一次 一趟 一起 一直 一定 一样 一点 一些 一年 一个月 两年 三年 五年 十年 多年
不透 不错 不行 不用 不必 不太 不会 不能 不是 不到 不过 没有 没事 还有 还是 就是 就当
讲讲 说明 介绍 描述 展示 分享 交流 讨论 咨询 提问 解答 回答 说清 讲清 聊清
清楚 明确 具体 详细 简单 复杂 容易 困难 顺利 麻烦 方便 快速 及时 尽早 抓紧
三言两语 收入结构 晋升路径 职业发展 工作内容 团队氛围 公司规模 发展前景
`.trim().split(/\s+/).filter(Boolean)

/** 按长度分组，供逆向最大匹配用 */
const byLen = new Map()
let maxWordLen = 1
for (const w of WORDS) {
  const n = [...w].length
  if (n < 2) continue
  if (!byLen.has(n)) byLen.set(n, new Set())
  byLen.get(n).add(w)
  if (n > maxWordLen) maxWordLen = n
}

export const LEXICON_SIZE = WORDS.length
export const MAX_WORD_LEN = maxWordLen

export function isWord(s) {
  const n = [...s].length
  return byLen.has(n) && byLen.get(n).has(s)
}

/**
 * 逆向最大匹配 —— 对中文分词，逆向通常优于正向。
 * @param {string[]} chars 一段**连续汉字**
 * @returns {Array<{text, chars, known}>} known=false 表示词表未命中、按单字退化
 */
export function segmentWords(chars) {
  const out = []
  let i = chars.length
  while (i > 0) {
    let matched = null
    const upper = Math.min(MAX_WORD_LEN, i)
    for (let len = upper; len >= 2; len--) {
      const cand = chars.slice(i - len, i).join('')
      if (isWord(cand)) {
        matched = { text: cand, chars: len, known: true }
        break
      }
    }
    if (!matched) matched = { text: chars[i - 1], chars: 1, known: false }
    out.unshift(matched)
    i -= matched.chars
  }
  // 把连续的未命中单字并成一个 unknown 段整体交给输入法自己分词。
  // 不这么做，下游的「按长度分布合并」会把它们拼成非词——实测「三言两语」被
  // 切成 三|言两|语讲，而「言两」「语讲」都不是词，真机上必然出错字。
  // 合成一段后输入法能按 sanyanliangyu 正确出词；真人打成语也是一口气打完。
  const merged = []
  for (const w of out) {
    const last = merged[merged.length - 1]
    if (!w.known && last && !last.known && last.chars + w.chars <= UNKNOWN_RUN_MAX) {
      last.text += w.text
      last.chars += w.chars
    } else {
      merged.push({ ...w })
    }
  }
  return merged
}

/**
 * 未命中段的字数上限。取 4：
 *   太大 → 一次上屏过长、拼音串长到输入法也分不准，粒度也离真人越来越远；
 *   太小 → 又会把成语之类切碎成非词。
 * 词表越全，未命中段越少，这个常数的影响越小。
 */
export const UNKNOWN_RUN_MAX = 4
