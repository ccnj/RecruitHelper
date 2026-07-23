# AI 调用追踪设计 v1

日期：2026-07-23  
状态：甲方已批准，按本文分批实施

## 1. 目标与边界

本设计同时解决两个不同需求：

1. 在本机完整保留每次 AI 调用的实际输入和 provider 原始输出，用于复现格式问题、提示词调优、模型能力对比以及成本分析；
2. 在不携带正文的小型诊断面中留下足够线索，使普通用户以后只提供 `brain.db` 和运行日志，也能定位大多数调用故障，不必传输体积大且含敏感内容的 `ai-traces.db`。

本次不改变 AI 的业务裁决、重试、预算、WAL 或脑手协议，也不新增远程上报。追踪是旁路观测能力：写入失败不得把一次原本成功的 AI 调用改判为失败，也不得授权重复调用。

## 2. 数据分层

| 层 | 内容 | 允许正文 | 主要用途 |
|---|---|---:|---|
| `ai-traces.db` | 完整请求、原始响应及调用元数据 | 是 | 本机深度复现、提示词与模型评测 |
| `brain.db` | 调用状态、精确失败阶段/错误码、hash、计量及 trace 完整性 | 否 | 业务审计、日常排障、未来支持包 |
| stdout | 单行无正文失败摘要；追踪库或业务库写入失败的兜底告警 | 否 | 实时观察与持久化失败兜底 |

三层以同一个随机 `invocationId` 关联。`ai-traces.db` 不是业务状态恢复依据；它缺失、损坏或无法打开时，`brain.db` 中的业务账本与 reducer 仍须独立成立。

## 3. `ai-traces.db` 的位置与最小表

数据库固定放在脑的数据目录中，文件名为 `ai-traces.db`，与 `brain.db` 分离。继续使用 `glebarez/sqlite`，连接池 `SetMaxOpenConns(1)`。v1 只建一张 `ai_traces` 表：

| 字段 | 类型/约束 | 含义 |
|---|---|---|
| `invocation_id` | TEXT PRIMARY KEY | 与 `brain.db` 调用事实关联的随机 ID |
| `purpose` | TEXT NOT NULL | `intent`、`reply`、`silenceFollowup`、`scoring` 或 `greeting` |
| `provider` | TEXT NOT NULL | provider 类型，不含 base URL 与凭据 |
| `model` | TEXT NOT NULL | 实际请求模型 |
| `config_hash` | TEXT NOT NULL | 本次本地模型配置的无密钥摘要 |
| `context_revision_hash` | TEXT NOT NULL | 与 `brain.db` 调用事实一致的职位上下文版本 |
| `prompt_revision` | TEXT NULL | 能确定时保存职位配置/提示词版本；不能确定时为空 |
| `request_json` | BLOB NOT NULL | 实际发往 provider 的完整 JSON body，不含任何 header |
| `request_hash` | TEXT NOT NULL | `request_json` 的 SHA-256 小写 hex |
| `request_bytes` | INTEGER NOT NULL | `request_json` 原始字节数 |
| `http_status` | INTEGER NULL | 收到 HTTP 响应时的状态码 |
| `response_body` | BLOB NULL | provider 返回的完整原始 body；未收到响应时为空 |
| `response_hash` | TEXT NULL | `response_body` 存在时的 SHA-256 小写 hex |
| `response_bytes` | INTEGER NOT NULL | 未收到响应时为 0 |
| `trace_state` | TEXT NOT NULL | `requestCaptured` 或 `completed` |
| `started_at` | DATETIME NOT NULL | 本次调用开始时刻 |
| `finished_at` | DATETIME NULL | 调用到达本轮终局的时刻 |
| `created_at` | DATETIME NOT NULL | 行创建时刻 |
| `updated_at` | DATETIME NOT NULL | 行最后补齐时刻 |

`request_json` 保存的是 adapter 最终构造并准备传输的请求 body，因此包含 model、messages、输出上限、thinking 开关等真实请求参数。禁止把 HTTP headers、API key、`Authorization`、本地凭据对象或带 key 的 provider 配置序列化进表。`response_body` 保存成功和非 2xx HTTP 响应的原始 body；网络层没有收到响应时保持为空。v1 不做压缩、分表、全文索引、自动清理或查看接口。

写入顺序冻结为：

1. 在首次 HTTP 尝试前，以 `invocationId` 插入 `requestCaptured` 行；
2. HTTP 返回或运输失败后，补齐同一行并转为 `completed`；
3. 不因第二步失败重新调用 provider；追踪更新失败只改变 `brain.db.traceStatus` 并产生 stdout 告警。

若第一步失败，业务调用照常进行，`brain.db.traceStatus=unavailable`；若请求已留存而终局补齐失败，记 `brain.db.traceStatus=responseUnavailable`。进程在二者之间退出时，遗留的 `requestCaptured` 行是诚实的未完成追踪，不冒充完整响应。

## 4. `brain.db` 与 stdout 的紧凑诊断

现有 AI invocation 事实继续作为业务审计源，并至少补充以下无正文诊断：

- `failureStage`
- `errorDetailCode`
- `providerHTTPStatus`
- `requestBytes`
- `responseBytes`
- `traceStatus`

`traceStatus` 的 v1 值冻结为：

- `complete`：请求与本轮终局响应/运输结果均已落入追踪库；
- `unavailable`：请求追踪没有建立；
- `responseUnavailable`：请求已落库，但终局补齐失败。

`brain.db` 仍只保存 input/output hash、token、延迟、费用及上述摘要；不得复制 request、response、聊天正文、简历正文、完整 prompt、provider 错误原文或候选人明文身份。

每次主调用失败须向 stdout 输出一条结构化单行摘要，至少包含 `invocationId`、purpose、provider/model、failureStage、errorDetailCode、HTTP 状态、token/字节计量、延迟与 traceStatus。任何追踪写入失败也必须单独输出无正文告警，包括主调用最终成功的情况。stdout 不输出 API key、base request/response、正文、完整 prompt、候选人身份或未经归类的 provider 错误文本。

## 5. 失败阶段与错误码

`failureStage` 描述失败发生在哪一层，v1 值冻结为：

- `requestBuild`：请求组装或本地输入校验失败；
- `transport`：未取得 HTTP 响应的网络/超时失败；
- `providerHTTP`：provider 返回非成功 HTTP 状态；
- `responseDecode`：provider 外层响应无法解码或必需字段缺失；
- `businessParse`：外层响应可读，但用途特定输出契约不成立；
- `reducer`：AI 结果已取得，但确定性状态裁决拒绝接纳；
- `persistence`：调用终局或业务结果无法写入 `brain.db`。

`errorDetailCode` 是稳定、无正文的细分码。实现可按现有失败类型补齐，但回复业务解析至少区分：

- `invalidJSON`
- `missingPhraseSequence`
- `invalidPhraseSequenceType`
- `emptyPhraseSequence`
- `unknownOutputKey`

provider 通用路径至少区分：

- `transportTimeout`
- `transportUnavailable`
- `providerRejected`
- `responseMalformed`
- `usageMissing`
- `inputTokenBudgetExceeded`
- `reasoningUsageUnsafe`
- `stateBoundaryChanged`
- `brainPersistenceFailed`

错误码只描述机器可判定的类别，不拼接原始正文或 provider 错误消息。遇到尚未分类的形态可以使用 `unknown`，但仍须保留 failureStage、hash、字节数和 traceStatus；后续以真实样本新增稳定错误码，不通过把原文塞入摘要绕过本边界。

## 6. 一致性与失败语义

1. 每次实际 provider HTTP 尝试恰有一个 `invocationId`；重放既有业务结果不伪造新 trace，真正获准的下一次 HTTP 尝试使用新的 invocation 或既有业务明确规定的 attempt 身份。
2. `ai-traces.db` 只观测调用，不参与 reducer、预算和发送授权。其写入成功不能证明业务成功，写入失败也不能授权重试。
3. `brain.db` 的 AI invocation 与 trace 通过 `invocationId` 一对零或一关联；一对零必须由 `traceStatus` 解释。
4. 追踪库失败不阻断原调用，但必须响亮可见；若 `brain.db` 同时不可写，stdout 是最低兜底。
5. `ai-traces.db` 不经管理 API、UI、验收报告、支持包或远程渠道读取和导出。测试可以直接打开隔离临时库断言，不新增生产查询面。

## 7. 隐私与交付截止点

`ai-traces.db` 会包含甲方已授权出站的简历、聊天历史和模型输出，可能自然带有姓名、电话等身份信息。它只能留在当前本机数据目录，不得上传；机器上的普通文件访问权限是当前开发期信任边界。所有层都绝不保存 API key、`Authorization` 或其他鉴权 header。

首个真实客户交付前必须重新裁决：

- 原文保留期限与清理方式；
- 是否启用静态加密及密钥来源；
- 默认开关、用户知情与显式授权；
- 客户支持时如何在本机查看或选择性导出。

这些问题未裁决前，不得把开发期“完整保存全部调用”的默认直接带入客户环境。本批不实现滚动日志文件、一键支持包、追踪查看 UI、自动清理、压缩、加密或远程评测上传。

## 8. 实施与提交边界

严格分为三个本地 commit，每个 commit 独立可构建、可回退：

1. **规范 commit**：修改 `AGENTS.md` 并加入本文，只冻结授权、数据边界、schema 与失败语义，不改运行代码。
2. **追踪库竖切 commit**：实现独立 `ai-traces.db`、请求前留存和响应后补齐；覆盖成功、非 2xx HTTP 与运输失败三类 adapter 冒烟。追踪写失败不阻断业务，也不新增管理读取面。
3. **诊断接线 commit**：增强 `brain.db` AI invocation 摘要和 stdout，接通 M5 意向/M5 回复/M5-B 沉默追问/M6 评分/M6 招呼语五种用途，补齐业务解析细分错误码及 traceStatus。

第三个 commit 后只做一次独立审查和全量门禁，再以不含候选人身份的 fixture 发起一次真实 provider 验证，确认两库能以同一 `invocationId` 对上。滚动日志、支持包、清理策略和查看 UI 保持后置，不顺手扩题。
