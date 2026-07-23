# 里程碑 5 · 批次 0B IM 简历与模板事实记录

> 采样日期：2026-07-21（Asia/Shanghai）
>
> 结论：**重开事实门通过，只授权进入批次 1。**
>
> 隐私口径：本文与配套 fixture 不含候选人姓名、聊天/简历/prompt/客户事实正文、平台原始 ID、职位原名、secret、无盐正文 hash 或可反推摘要。

## 一、结论先行

1. 当前智联 IM 的完整简历必须从已绑定会话的当前详情区点击标准“查看详情”后读取，因此 `candidate.readResume@1` 采用获批的 `intrusive` 分支，而不是 `readonly`。
2. 本次打开未改变 IM 路由或会话/候选人绑定，未新增当前时刻的“查看简历”处理记录，也未观察到候选人可见副作用；`platformSideEffect` 写死为 `none`。元数据随获批分支固定为 `manualQuiet`、账号串行域、`execBudget=60s`、`deadline=120s`、`lease=30s`，不设 idemKey、guards、evidence 或 verification。
3. 同一唯一、完整就绪的详情面板可表达五个必有分区：`basic`、`expectations`、`selfEvaluation`、`education`、`workExperiences`。当前样本的自评可选区未出现，页面以完整详情中的区块缺省表达为空；只有完整根与四个稳定锚区均已就绪时才允许映射为空串，根/锚不完整、零/多个面板或解析异常都必须失败且不得返回部分 data。
4. 脱敏 canonical 形状小于 4 KiB，低于既有 64 KiB result data 硬上限；M5 不触发 `blob/1`。永久约束只写整包 `≤64 KiB`，不从单个小样本倒推出过紧字段上限。
5. `多轮沟通` 的活输入 token 恰为 `{简历}{推荐时段}{对话历史}`；`意向判断` 的活输入 token 恰为 `{回复}{招呼语}`。`{话术_序列}` 是回复输出示例中的 key，不是第四个输入 token。普通 JSON 花括号不按 token 处理，白名单外活 token 响亮失败。
6. 当前页面同一生产感知源只扫描已加载的 21 个会话，不滚动、不搜索、不为造样本发消息。三类旧短路候选逐条均为“未见样本”，生产启用清单为空；M5-A 正常轮因此全部走一次独立意向 provider 调用，不能拿旧代码或列表预览反向授权规则。
7. 历史、默认时段、provider 单 user message、意向数据信封、严格回复/意向解析均已用无生产内容的 golden 冻结。客户事实库追加字面借用旧实现，但用于普通回复是 M5 新设计，不伪称旧普通回复已有该行为。
8. 未来试运行的单个 M4 greeted profile 与本批生产 context revision 可由真人明确选择，记录为随机标签 `profile-A × context-A`；本批不持久绑定，也不按职位标题预选。

## 二、授权边界与实际触达

本批沿用甲方已明确给出的真实账号测试授权。实际操作仅限一个当前 IM 会话：不发送消息、不切换候选人、不搜索、不滚动候选人列表。为读取只在页面内存存在的结构化形状，曾临时编译一次只返回计数、布尔值和大小分桶的 MAIN-world 探针；它未返回或持久化原始 ID/正文，探针源已删除，正式插件构建已恢复并以新 boot、相同 contractHash 重新就绪。

插件外另只读考古旧代码以取得历史、时段和 provider 的候选字面。旧代码仍只是候选方案：本批采用的部分都由当前已批准需求或 synthetic golden 固化；旧 parser 的宽松回退、AI 动作解释、隐式重试与其他模板 token 一律不继承。

## 三、IM 简历事实

### 3.1 同人绑定与信任边界

合法绑定链为：命令指定 `conversationRef + platformUserRef` → 当前路由恰有一个匹配会话且当前 peer 相等 → 当前详情区唯一 → 在该详情区内执行一次标准 click → 唯一详情面板出现 → 返回前路由、会话与 peer 仍相等。

这条链只问世界状态与标准 click 的公开语义，符合 `AGENTS.md` 第 9 条。平台一次标准 click 至多作用于界面所示对象是被信任公理；不再要求 modal/Vue/SDK 私有对象里存在第二份平台 ID 来互证平台是否把点击接对。MAIN world 只是简历内容的感知通道，读不到时固定“不能确认 → 失败 → 转人工”，不得降级到姓名、职位标题、resumeNumber、列表位置或 DOM 位置猜人。

### 3.2 动作表面与副作用

| 事实 | 结论 |
|---|---|
| 完整五区是否已在 IM 初始详情渲染 | 否 |
| 是否必须点击标准入口 | 是 |
| 是否离开当前 `/app/im` 语义路由 | 否 |
| 打开后 route/session/peer 是否仍绑定原对象 | 是 |
| 页面是否已有历史“查看简历”处理记录 | 是 |
| 本次打开是否新增当前探针时刻的查看记录 | 否 |
| 是否观察到消息、卡片、关系状态等候选人可见副作用 | 否 |
| primitive class | `intrusive` |
| `platformSideEffect` | `none` |

这里的 `none` 覆盖并取代探针过程中曾作出的保守候选判断 `idempotentReadReceipt`：详情里唯一“查看简历”记录的页面时刻严格早于本次探针，本次打开后没有当前时刻的新记录。这里不把平台内部“每次打开是否另写私有访问日志”升级成动作授权守卫；当前公开世界没有新增读回执或候选人可见事实，继续点击只为审计内部实现的边际价值不足。若未来真实页面出现候选人可见或非幂等效果，按事实漂移停止该能力并重新立案。

### 3.3 五分区与空值

成功 DTO 冻结为：

- `conversationRef`、`platformUserRef` 原样回显，另有 epoch milliseconds 的 `observedAt:int64`；
- `basic`、`expectations` 是按页面顺序保存的 `{label,value}[]`；
- `selfEvaluation`、`education`、`workExperiences` 是完整文本；
- 五个分区全部 required。明确空分别用空数组/空字符串表达；字段缺失、目标换绑、结构读不到或 payload 超限都返回 failed 且无 data。

当前样本覆盖：基本、期望、教育、工作有内容；自评可选区在唯一完整详情中缺省。当前页面对可选空区的公开语义就是“不渲染该标题与内容区”，故本事实门把这个完整页面状态冻结为显式空；不额外要求平台私有模型再提供一份 null 互证。空值判据不是“任意 selector 没找到”：必须先证明唯一面板与简历根就绪、基本/期望/教育/工作四个锚区均可读，同时用区块结构与可见标题两条公开读数确认页面没有自评区；任一条件不成立都归 `ELEMENT_UNRESOLVED`。这仍有单一区块同时改类名和标题的页面漂移残余风险，但在当前有人值守阶段按防护成本预算接受；观察到漂移即停用转人工，不把读失败编码为空。

期望区保留原页面 label/value，不把期望职位、最近投递与我方沟通职位压成固定字段：当前样本能区分期望职位与面板外的沟通职位；最近投递未在样本中出现，记录为空/缺省而不伪造，也不拿沟通职位补位。

当前整包 shape `<4 KiB`。契约只保留现有 compact result data `≤64 KiB` 总上限；后续真实单例超过 64 KiB 走 `PAYLOAD_LIMIT → manualRequired`。

## 四、模板、格式与 provider 事实

### 4.1 活 token 与输出

| 文档 | 活输入 token | 输出契约 |
|---|---|---|
| 多轮沟通 | `{简历}`、`{推荐时段}`、`{对话历史}` | 必有 `话术_序列:string[]`；可选 `动作`、`会议时间:string` 只丢弃 |
| 意向判断 | `{回复}`、`{招呼语}` | `信号`/`signal` 映射 `有意向/拒绝/中性`；`理由`/`reason` 只用于本次判错，不持久化 |

`{话术_序列}` 在真实多轮 prompt 中属于输出示例，不参与输入替换。批次 0B 当时冻结的回复 parser 顶层白名单只有 `话术_序列` 与 `动作`；旧实现曾读取的 `策略`、会议时间字段和 `reply/content/text/话术` 回退都没有本批授权。若将来原 prompt 改为声明额外纯元数据 key，必须先更新事实与 fixture，不做 must-ignore。

**2026-07-23 勘误：** 当前正式 `legacyJobConfig` 修订中的 `多轮沟通` prompt 已三次声明 JSON 输出键 `会议时间`，一次真实 provider 回包也见到该键且值为空字符串；事实记录只保留声明次数、类型与空值结论，不保存 prompt、回复正文或候选人身份。原“会议时间字段未获授权”结论对当前修订作废。甲方据此批准把可选 `会议时间:string` 收编为只丢弃元数据：空或非空字符串均不得推进邀面状态、创建卡片、改变回复正文或产生其他业务动作；非字符串、别名及其他未知键仍保守失败。`策略` 与 `reply/content/text/话术` 回退继续不在白名单。

### 4.2 历史与时段

历史只读正式活动消息，排除 retracted 与空正文，取轮前最近 20 条后按 seq 正序渲染。入站/出站单条分别按 Unicode code point 截至 1000/300 个，再追加 `…(超长消息已截断)`；字面为 `候选人(消息):...`、`我(消息):...`、`我(招呼语):...`。

默认时段是 Asia/Shanghai 下未来 14 个日历日（offset 0..13），周一至周五 `[09:00,18:00)` 的整点全部选中；当天只保留不早于冻结时钟的下一个整点。无 `【可约面时间】` 块时，所有 `{推荐时段}` 变成短指针并在文末追加一次完整块；已有块时，块后首个 token 注入一次 inline 数据，其余只留指针。结果在 turn 创建时冻结。

### 4.3 provider 组装

沿用 OpenAI-compatible Chat Completions 的最窄形状：每次 intent/reply 调用都只有一条 `role=user` message，并要求 `response_format=json_object`；不新增 system/assistant role。回复 user content 是渲染后的原 `多轮沟通` prompt，末尾恰追加一次 `【客户事实库】\n{原文}`。这段分隔字面来自旧代码，但把 facts 用于普通回复是 M5 新设计。

意向 user content 先替换原 `意向判断` 的 `{回复}{招呼语}`，再追加一个不含行为指令的 `【对话数据信封/v1】` canonical JSON。信封只含轮前最近 20 条与本轮全部活动消息的本地 `seq/direction/kind/text`，不含平台 ID、displayName 或候选人身份；轮前与本轮按边界互斥，重复 seq 视为输入非法而不是静默去重。

## 五、确定性短路逐条结论

扫描范围是当前生产感知源已加载的 21 个会话；21 个均可解析，但只有 2 个 last-message 事实能稳定判定为候选人普通入站。没有滚动、搜索、读取额外历史，也没有使用浏览器列表预览文字替代正式消息账本。以下每条均 `observedCount=0`、状态 `unseen`、M5-A disabled：

- 简历投递：`M5I-RSM-01..13`；
- 拒绝正则八个语义分支：`M5I-RT-N-CONSIDER`、`M5I-RT-N-MISMATCH`、`M5I-RT-N-NOT_INTERESTED`、`M5I-RT-N-UNSUITABLE`、`M5I-RT-THINK-CONSIDER`、`M5I-RT-THINK-MISMATCH`、`M5I-RT-THINK-NOT_INTERESTED`、`M5I-RT-THINK-UNSUITABLE`；
- 短拒词：`M5I-SR-01..11`。

完整 ID 与候选字面只存在于已批准计划和 synthetic fixture；本事实记录不保存真实命中正文。结论不是“这些表达不存在”，只是“本次受限窗口没有获得启用证据”。因此生产 `enabledRuleIds=[]`，正常轮不得短路。

## 六、未来绑定与出口复核

甲方批准的最窄试运行对象仍是 M4 已 greeted 的单 profile 与本次采样的单 context revision。两者能通过随机标签 `profile-A`、`context-A` 明确选择，禁止按标题猜测；批次 2B 前 `persistedBindingCreated=false`。

出口逐项复核：同人绑定成立；五区与明确空/读失败可分；class/metadata 已写死；无非幂等候选人可见副作用；真实样本小于 64 KiB；两份 prompt token 白名单闭合；历史/时段/provider/输出/意向 fixture 确定；全部短路候选有显式结论；synthetic 五区快照可无损表达；未来 profile/context 对可真人明确选择。故批次 0B 通过。

## 七、产物与未授权范围

- 页面与生产形状：[`fixtures/m5-batch0b-production-shape.v1.json`](fixtures/m5-batch0b-production-shape.v1.json)
- 合成格式 golden：[`fixtures/m5-batch0b-format-goldens.v1.json`](fixtures/m5-batch0b-format-goldens.v1.json)

本批没有修改 `AGENTS.md`、沟通逻辑规格、协议规格、machine contract、数据库 schema 或生产运行代码；没有启用任一旧短路规则。事实门通过只授权按计划进入批次 1。
