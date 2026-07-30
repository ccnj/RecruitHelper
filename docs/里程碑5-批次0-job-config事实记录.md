# 里程碑 5 · 批次 0 job-config 事实记录

> 采样日期：2026-07-21（Asia/Shanghai）
>
> 结论：**事实门未通过，停止在批次 0；不得进入规范批次或实现批次。**
>
> 隐私口径：本文及配套 fixture 不含客户/职位原名与 ID、候选人身份、聊天正文、prompt/事实库/话术正文、license token、LLM key、model/base_url 原值。

## 一、结论先行

本批证实了两件能继续继承的事实：

1. 生产 `/api/v1/client/job-config` 与 `/api/v1/client/job-configs` 的真实响应可完整填充 M5 拟议 adapter；其中 10 类 `job_version_docs` 文档集合可由 `SourcePackage` 逐字节无损表达，并在生产数据库活动版本、单数回包和复数当前职位回包之间通过内存摘要逐项确认全等。
2. `固定语1/2/3` 没有三个可继承的业务场景。目标客户全部历史版本中也不存在这四类遗留槽位：`固定语1/2/3/礼貌结束话术`。

但可执行 `CommunicationView` 不能按原计划冻结：

- 现网当前 `多轮沟通` prompt 使用 `{对话历史}`、`{推荐时段}`、`{简历}`、`{话术_序列}`；
- 新项目 M4 只有候选人平台身份、职位观察值、会话消息账本，没有简历、推荐时段或话术序列事实；
- `{话术_序列}` 在旧客户端生产渲染器中也没有可证替换器，不能猜其语义；
- 实际 Electron 数据库虽有 4 个正式 profile（3 个 `selected`、1 个 `greeted`），但当前生产职位样本尚未与其中任一 profile 由真人显式绑定；样本职位与现有 profile 的职位标题也不相等，禁止按标题自动绑定。

这命中《里程碑5-实施与验收计划》事实门的三个停止条件：“目标 prompt 依赖当前不存在的事实”“未知 placeholder”“需要猜测配置归属”。因此本批只固化事实，不修改 `AGENTS.md`、沟通规格、协议规格、machine contract 或运行代码。

## 二、授权、方法与实际触达

### 2.1 授权边界

甲方明确批准一次受限生产 SSH 探针。探针遵守：

- 生产凭据只在远端进程内存读取和使用；
- 原始 HTTP 回包和数据库正文只在进程内存存在；
- stdout 白名单只含 key/type、空值、计数、placeholder 名、临时 UTF-8 字节数、临时 SHA-256 和布尔交叉核验；
- 不输出 token、LLM key、model/base_url 原值、客户/职位/候选人原始标识或任何正文；
- 不改配置、不重启服务、不写职位/候选人/消息业务事实。

`job-config(s)` 内部会调用旧后台 `verify_client`，按既有实现更新 binding `last_seen_at` 并追加 `client.verified` 审计。本批实际完成两个三请求组：第一个确认旧本地授权所指客户是空配置客户；第二个对管理端可见且明确带当前职位的客户完成正式采样。预计产生 6 次既有授权审计写，不产生业务副作用。最初用过期本地 token 的一次请求返回 401，不读取错误体。

### 2.2 目标选择不是标题猜测

旧本地授权所指客户真实返回：

- 单数端点 `200`，`job=null`，其余块为空结构；
- 复数端点 `200`，`currentJobId=null`、`jobs=[]`；
- 数据库无 `current_job_id`、无职位和历史固定语槽位。

它不能作为 M5 上下文来源。

正式样本通过已登录管理端的可见客户选择器取得公开数值标识，并由“客户配置”的“当前职位”列与“职位配置”的“当前版本”页面双向确认。探针只按这个客户标识读取 `customers.current_job_id`，没有按职位标题查询或匹配。

### 2.3 部署代码指纹

生产服务器 `/opt/console-server` 两个决定本批语义的源文件与旧工作区当前 checkout 逐字节 SHA-256 一致：

| 文件 | SHA-256 |
|---|---|
| `app/api/client_auth.py` | `589182e1d5b130a07d0723258a524f2c9298ea79755a7c1d851c6cb5b7833a23` |
| `app/db/jobs_repository.py` | `0728c9e9e1ebb3992ea7bee1b17f42379b85cb285b34dc84e67de82d1c4cec83` |

对应旧 backend checkout：`19c60b018ff701cd944a04013ebbffac14daaee4`。这只证明上述两个生产文件的字节一致，不把旧仓其他文档或代码整体升级为新仓信任源。

## 三、真实 HTTP 形状

### 3.1 单职位端点

`POST /api/v1/client/job-config` 返回 `200`，顶层恰有：

```text
candidateSelection
communication
documents
facts
filters
fixedPhrases
fixedRules
greeting
intent
job
scoring
silenceFollowup
```

字段形状：

- `job`：`{id:number,name:string,environment:string}`；
- 普通 prompt block：`{prompt:string,apiKey:string|null,model:string|null,baseUrl:string|null}`；
- `greeting` 在 prompt block 外多 `usePlatformDefault:boolean`；
- `facts/fixedRules`：`{content:string}`；
- `fixedPhrases`：`{content:string,scenes:object}`；
- `documents`：`Record<string,string>`；
- `filters`、`candidateSelection` 均为 object。

2026-07-21 本批采样时，provider key/model 有值，base URL 是空字符串；M5 不继承三者，本批未记录原值。

> **2026-07-30 复采更正（本段采样事实已过时）。** 同一端点复采：`baseUrl` 已下发且五个 prompt block 同值（非空、https）；`apiKey` 五块全非空且去重后仅 1 个值，证实它在旧后台是**客户级**配置，该客户下所有职位、所有提示词共用同一值；`model` 是**逗号分隔的模型链**（形如主备两个 ID），不是单个模型 ID。据此 AGENTS.md 2026-07-30 甲方裁决改为：`base_url`、key 与 `model` 允许且仅允许来自本响应，`model` 只取首项、其余忽略、不实现降级切换。本文其余采样事实未复核，仍按 2026-07-21 口径阅读。

“整包可填充”不等于“响应每个字节都持久化”：adapter 会读取和校验完整响应，但 `SourcePackage` 的无损保证只覆盖完整文档集合与批准保留的职位/来源元数据。provider 三字段不进入 `SourcePackage`，也不参与 `revisionHash`——2026-07-30 起它们改为落盘同机 `llm-provider.json`，与不可变职位配置版本各走各路。

### 3.2 多职位端点

`POST /api/v1/client/job-configs` 的真实顶层为：

```text
{currentJobId:number|null,jobs:array}
```

正式样本事实：

- 复数端点启用，返回 3 个职位包；
- 每个职位包与单数包同形并增加 `missingDocs:string[]`；
- `includeDocuments=false` 与 `true` 的职位数量和顺序一致；
- `false` 时只把每包 `documents` 清空，其余派生块仍保留；
- `true` 时原始 `documents` 存在；
- `currentJobId` 在 `jobs` 中恰出现一次；
- 当前职位 `missingDocs=[]`。

### 3.3 活动版本核对

生产只读事务与 HTTP 内存交叉核验全部为真：

- `customers.current_job_id = jobs.id`；
- `jobs.customer_id` 属于显式目标客户；
- `jobs.active_version_id = job_versions.id`；
- `job_versions.job_id = jobs.id`；
- 每条 `job_version_docs.job_version_id = jobs.active_version_id`；
- 每条反范化 `job_version_docs.job_id = jobs.id`；
- 单数 `job.id = currentJobId = 数据库 current_job_id`；
- 单数、复数当前包和数据库活动版本三份文档集合经 `doc_type + content SHA-256` 内存比对全等。

因此当前部署确实按 `active_version_id` 下发，没有按反范化 `job_id` 混入历史版本。

## 四、当前职位完整文档集合

| `doc_type` | 空 |
|---|---:|
| 候选人筛选 | 否 |
| 固定规则 | 是 |
| 固定话术 | 否 |
| 多轮沟通 | 否 |
| 客户事实库 | 否 |
| 意向判断 | 否 |
| 打分 | 否 |
| 招呼语 | 否 |
| 沉默追问 | 否 |
| 职位筛选 | 否 |

探针曾在内存中计算精确字节数与无盐 SHA-256 完成交叉核验，但它们不进入仓库：低熵短正文可能被字典枚举反推，不能把“不是明文”误当成可靠脱敏。配套 [`m5-job-config-production-shape.v1.json`](fixtures/m5-job-config-production-shape.v1.json) 只保存类型、空值与相等性结论，并附 10 类 synthetic `docType/content` 集合供批次 2 未来 round-trip 测试使用。fixture 不是原始 API replay，不含任何真实正文或真实正文摘要。

## 五、真实 placeholder 与本地可填充性

### 5.1 现网实际集合

| 分区 | 真实 placeholder |
|---|---|
| 打分 | `{resume_json}` |
| 招呼语 | `{career_state}`、`{resume_summary_json}` |
| 多轮沟通 | `{对话历史}`、`{推荐时段}`、`{简历}`、`{话术_序列}` |
| 意向判断 | `{回复}`、`{招呼语}` |
| 沉默追问 | `{姓名}`、`{年龄}`、`{性别}`、`{简历}` |

这与旧 backend 单测示例中的 `{history}/{message}/{last_message}` 不一致，也与旧客户端渲染器的支持超集不同。生产配置事实优先；单测示例不能成为新契约。

### 5.2 M4 当前事实能力

实际 Electron 脑数据库中：

- 4 个正式 profile：3 个 `selected`、1 个 `greeted`；
- greeted profile 已有 conversation 绑定、成功招呼 intent 和 `greeted_at`；
- 该会话只有 1 条已发普通文本，还没有候选人入站消息；
- 四个 profile 属于同一平台职位观察值；
- 当前 schema 有 `candidate_profiles/candidates/conversations/messages`，没有简历、推荐时段、职位 AI context 或话术序列事实表。

可填充性判定：

| placeholder | 判定 | 理由 |
|---|---|---|
| `{招呼语}` | 可填 | 已发招呼正文存在于消息账本 |
| `{回复}` | 等待事实 | 候选人下一条普通文本到达后可填 |
| `{对话历史}` | 可填 | 正式会话消息账本可提供；当前只有招呼一条 |
| `{简历}` | **不可填** | M4 没有简历采集与存储；不能用姓名、职位标题或空串冒充 |
| `{推荐时段}` | **不可填** | M5-A 明确后置邀面/时段配置，当前无事实源 |
| `{话术_序列}` | **不可解释** | 旧客户端生产渲染器没有该 placeholder 的可证生产者，新仓不能猜映射 |

因此 `意向判断` 文档可进入未来执行视图，但现网 `多轮沟通` 文档只能无损保留在 `SourcePackage`，不能进入 M5-A 可执行回复视图。

## 六、固定语与 fixedPhrases

### 6.1 固定语 1/2/3

五仓考古只证明：

- 初始六槽位包含 `固定语1/2/3`；同期设计称它们为待命名占位；
- 迁移链为 `固定语1 → 礼貌结束话术 → 固定话术`；
- `固定语2/3 → 固定规则`，两者冲突时内容被拼接，原边界已经折叠；
- 当前五仓生产源码没有三者的独立消费者。

真实目标客户全部历史版本中，`固定语1/2/3/礼貌结束话术` 的行数均为 0。结论仍是：不建立三个新领域场景，不猜映射；未来若遇到别的客户存量，只在 `SourcePackage` 原样保留。

### 6.2 fixedPhrases 真实场景

当前职位真实 scene key 全部 `enabled=true`：

| scene | message 数 | 旧客户端可证消费者 |
|---|---:|---:|
| `candidateAskWechat` | 1 | 是 |
| `meetingAccepted` | 2 | 是 |
| `meetingInvitePending` | 1 | 否 |
| `rejectWechat` | 3 | 是 |
| `silence48Wechat` | 1 | 是 |
| `wechatAccepted` | 1 | 是 |

`meetingInvitePending` 只能保留为来源事实，不能进入 M5-A 行为。M5-A 本身不启用任何固定话术场景。

## 七、profile 与职位上下文绑定事实

管理端显式当前职位样本与实际 Electron 数据库中四个 profile 的 `positionTitle` 不相等。新系统也没有旧后台 job 与平台 `positionRef` 的稳定外键。

因此：

- 不能把管理端当前职位默认为 greeted profile 的职位；
- 不能按标题相似度或同名自动绑定；
- 批次 2 若继续，必须由真人在“profile × 本地 context revision”界面显式选择；
- 本批尚未发生这次选择，所以原计划的绑定出口未满足。

## 八、SourcePackage 无损性结论

响应形状本身不阻塞，但三个层次必须分开：

- adapter 读取并校验完整 HTTP response；
- `JobAIContextRevision` 的稳定来源元数据可按已批准计划接收 `job.id/name/environment`，`RevisionHash` 由完整文档包与这些稳定元数据计算；
- `SourcePackage` **只**逐项无损保存每个 `documents[doc_type]=content`，包括未知或 M5 不消费的类型。

`currentJobId`、单数/复数来源形态与 `missingDocs` 是本次采样和 adapter 校验事实，不属于拟议 `SourcePackage`；若未来要把它们新增为持久领域字段，必须另行修订计划。派生结构化块只能校验或重建文档，不能反向覆盖原包。`apiKey/model/baseUrl` 不进入 `SourcePackage`——2026-07-21 时的边界是「明确丢弃」，2026-07-30 甲方裁决改为「提取后落盘 `llm-provider.json`，仍不进入不可变职位配置版本、不参与 `revisionHash`」。对本节的 `SourcePackage` 口径而言两者一致：它从来不含这三个字段。

批次 2 的 importer 必须对配套 synthetic 集合证明：导入前后的 `docType/content` UTF-8 字节集合完全相等。本批只证明“完整 `job_version_docs` 文档集合可无损收编”以及既定来源元数据字段在形状上可填充；没有宣称 provider secret 或整个 HTTP JSON 字节级保留，也没有授权扩张领域模型或把不可执行 prompt 合法化。

## 九、DeepSeek V4 额外事实

按甲方新增模型纪律，2026-07-21 复核 DeepSeek 官方文档：

- model ID：`deepseek-v4-pro`、`deepseek-v4-flash`；
- OpenAI Chat Completions base URL：`https://api.deepseek.com`；
- V4 默认是思考模式；明确关闭方式为 `thinking: {"type":"disabled"}`；
- `usage.completion_tokens_details.reasoning_tokens` 是 reasoning token 明细字段；官方 schema 未保证非思考响应一定带该对象；
- Pro 当前公开未缓存输入/输出价为 `$0.435/$0.87` 每百万 token；Flash 为 `$0.14/$0.28`；
- `deepseek-chat/deepseek-reasoner` 将于 2026-07-24 15:59 UTC 停用；
- 官方只把 `max_tokens` 描述为 completion 最大生成量，没有明确文字保证它如何与 reasoning token 共同截断。M5 仍按甲方要求用“含思考的保守总输出上限”配置，并在批次 5 dry-run 验证 reasoning token 为 0；未经实测不得放宽 P 档。

官方来源：

- <https://api-docs.deepseek.com/quick_start/pricing/>
- <https://api-docs.deepseek.com/api/create-chat-completion/>
- <https://api-docs.deepseek.com/guides/thinking_mode/>
- <https://api-docs.deepseek.com/updates/>

## 十、事实门逐项判定

| 计划出口 | 判定 | 说明 |
|---|---|---|
| 至少单数真实整包形状确认 | 通过 | 空客户与正式当前职位样本均为 200，形状已记录 |
| 复数真实部署与开关状态确认 | 通过 | 正式样本启用，3 个职位；空客户正常 200+空 jobs |
| 全部 doc_type/content 可无损表达 | 通过 | 10 类文档集合及 synthetic round-trip fixture 已冻结 |
| 生效文档来自 active_version_id | 通过 | DB、单数、复数当前包三方哈希全等 |
| 固定语 1/2/3 不伪造新场景 | 通过 | 无独立消费者；真实目标历史存量为 0 |
| 多轮沟通必填 placeholder 可由当前事实填充 | **失败** | 缺简历、推荐时段；话术序列语义不可证 |
| 目标 profile 与 context 可显式绑定 | **失败** | 尚未真人选择，且当前样本标题与 profile 不相等 |

总判定：**批次 0 未通过。**

## 十一、最小重提立案建议

推荐只立一项有明确成本的加法式例外，不扩成完整职位管理：

1. 在旧 job version 文档集合中新增一个**可选、加法式** `doc_type=自动沟通`；未配置时不存在或为空，不影响旧客户端。
2. 该文档的 M5-A 唯一动态 placeholder 只允许 `{对话历史}` 与 `{最近回复}`。职位/公司事实和回复原则直接写在文档正文，不要求简历、推荐时段、姓名或平台身份。
3. 这不是“多一个常量”：旧 backend 当前用同一文档类型集合同时表达合法类型和版本创建/更新的精确必填集合。实现可选第 11 类必须拆成“旧十类必填集合 + 十一类合法集合”，修改 service 校验与测试，并核对/迁移数据库 `CHECK` 约束；既有版本不补造空行业务事实。
4. admin 必须增加独立编辑器和可选序列化逻辑，保证读取旧版本时缺字段仍可编辑、保存旧十类时不会被迫补第十一类；增加相应回归测试。
5. `job-config(s).documents` 可沿现有 raw map 透传新类型，不新增顶层字段；旧客户端继续消费旧 `多轮沟通`，忽略新类型。仍须用部署事实测试证明这一点，而不是仅靠源码推演。
6. 新项目 `SourcePackage` 无损保留返回的全部 10+1 文档；M5-A `CommunicationView.ReplyPrompt` 只认非空 `自动沟通`。现网旧 `多轮沟通` 留作来源事实，不执行、不改写。
7. 本地导入后由真人显式选择“一个 greeted profile × 一个 context revision”，界面不按标题预选；选择结果成为唯一绑定事实。

这会修改旧 backend 与 admin，和当前 `AGENTS.md` 的“旧后台暂不改”冲突；必须由甲方把它批准为明确、一次性的例外，并接受数据库约束、保存兼容和部署回归成本。若批准，它仍保持 response 加法兼容、M5 运行时不连接旧后台，同时不为空缺事实造值。

若不批准改旧后台，唯一诚实替代是让 M5 的可执行 reply prompt 成为新客户端本地 sidecar 配置；代价是“可执行 CommunicationView 能完全由旧 job-config 整包填充”不再成立，必须同步放宽已批准兼容判据。执行方不在两条路之间自行选择。

不推荐的捷径：

- 给 `{简历}/{推荐时段}/{话术_序列}` 填空串或臆造默认值；
- 把现有 profile 按职位标题自动匹配到生产 job；
- 修改现网 `多轮沟通` 以迁就新项目，影响旧客户端生产行为；
- 把 `固定规则` 偷换成新 prompt，污染已存在的领域语义。

甲方批准本节重提后，先修订《里程碑5-实施与验收计划》的批次 0/1 与拟议规范文本，再重新过一次窄事实门；未批准前保持停止。
