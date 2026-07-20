# E · 首个真实副作用安全轨道设计记录

> 状态：2026-07-19 随里程碑 3（X 批）冻结；2026-07-20 按真机只读事实收口发送执行期数据源，并按《防护成本预算》第 9 条重划动作信任边界。本文记录设计理由与故障推演，便于实现审查；线上行为仍以 `AGENTS.md`、`contract/协议规格-v1.md`、`contract/contract.v1.json` 的优先级为准。

## 0. 结论

首个真实副作用原语是 `chat.sendMessage@1`。它不把“恰好一次”寄托在 WebSocket、内存去重或 DOM 点击结果上，而由三层相互独立的围栏把所有未知态导向“少发或人工”，不允许自动逻辑多发：

1. 脑侧 SQLite 意图与物理命令账本：先落库后发送，`idemKey` 冻结同一业务意图。
2. 手侧基础设施证词：不可逆动作前持久化 `attempting`，观察到后置条件后原子写 `committed + outbox`。
3. 平台事实闸：发送前、点击同步栈和点击后观察只读同一可信 live Vue root 的实时 timeline；`chat.readThread@1` 仅在结果歧义后的恢复状态机中验证。

手侧证词不是第二业务账本。它只回答“一条已经由脑签发的物理命令是否可能越过不可逆动作边界”，不保存正文、候选人档案、调度状态或下一步决策；整体丢失只会触发更多验证/人工。

## 1. 不能退让的安全不变量

| 编号 | 不变量 | 构造性约束 |
|---|---|---|
| X1 | `attempting` 成功持久化前，不可逆动作调用次数必须为 0 | program 只能经 base 提供的副作用安全点进入动作；base 等待 storage 写完成后才放行 |
| X2 | 一次物理命令最多调用一次不可逆动作 | program 内禁止点击、键盘提交、网络发送等自主重试 |
| X3 | `ok` 表示观察到平台稳定后置条件，不表示“点过按钮/出现乐观气泡” | 发送只认同一实时 timeline 的有序 last64 相对基线严格连续新增一条，且该唯一新行是带 server `idServer` 的成功我方文本；具体 data/evidence schema 再由 codegen validator 强制 |
| X4 | 任一歧义都不能推出“未发送” | `attempting`、零匹配、读失败、证词库换代均验证后进入 suspect |
| X5 | 跨 boot 自动重投只有一个入口 | 同一 `witnessStoreId` 且 `report=unknown`，证明未越过 attempting 写点后，重投原 msgId |
| X6 | 真实 SX 永不进入新 msgId replacement 链 | `replacementOf` 只属于 readonly/intrusive；真实 SX 只重投原物理命令或转 suspect |
| X7 | result 可跨会话补投但不能伪造无会话信封 | outbox 保存完整信封，`ResultEnvelope.session` 必填非空；新连接补投前先持久更新 session/ts/attempt，msgId 与 body 不变 |
| X8 | committed 不能是一张空收据 | `JournalEntry.result` 在 committed 时必填且非 null，并满足 `result.ref=journal.ref` |
| X9 | 易失成功不能越过持久终局屏障 | committed/outbox 双写失败时不缓存或发送成功 result；保留 attempting、熔断 dispatcher 已 accepted 队列、断开连接并交给 query→验证，同 SW 重复 cmd 也不能重放成功 |
| X10 | UI 重载不能把一次人工意图变成两次 | 脑以会话持久单调 head（不读 wall clock/rowid）和 `previousIntentId` 做事务 CAS，权威 current 只在脑侧；浏览器会话存储不构成安全证明。M3 有人值守 UI 的“我已确认”只负责串行化本轮人工验收，不外推为后续自动调度规范 |

第 X5 条不是一般“同 id 跨重启重试”许可。它依赖先写 attempting 再做动作的严格顺序；任何写点不确定、storeId 不同或记录损坏都会失去该证明。

## 2. 持久证词模型

`chrome.storage.local` 只允许 base 管理三类命名空间：

```text
witness:meta       -> WitnessStoreMeta（含 journalCount/outboxCount 连续性计数）
journal:{idemKey}  -> JournalEntry
outbox:{msgId}     -> OutboxEntry
```

### 2.1 `witness:meta`

`storeId` 标识这批证词的连续世代。首次初始化、整体清库、发现不可恢复损坏后重建时必须生成新值。`journalCount/outboxCount` 与条目写入一同持久维护；启动和每次关键读写都将实际 key 数与计数复核，单 key 丢失不能伪装成同世代 `unknown`。脑在派发时持久化当时的 storeId；恢复时只把相同 storeId 且连续性完整的 `unknown` 当零副作用证明。

### 2.2 journal

journal 只有两态：

- `attempting`：只含 `ref/idemKey/startedAt/expiresAt`。表示命令已经到达不可逆动作边界，动作可能尚未发生，也可能已经发生。
- `committed`：增加 `committedAt` 与完整 `ResultBody`。表示后置条件已观察并形成可重放终局。

attempting 禁止携带 `committedAt/result`；committed 强制二者存在且非 null。该限制同时进入 JSON schema、Go/TS 生成类型和运行时 validator，不能由某一端自行解释。

### 2.3 outbox

outbox 保存完整 result 信封，而不是只有 `ResultBody`。这是因为补投仍需稳定的 `msgId/attempt/ts/session/body.ref` 关联关系。信封的 session 必须非空；第一次发送使用结果创建时会话，跨会话补投前按通用重传纪律在 outbox 中先持久更新为当前 session，并只改变 session/ts/attempt。脑始终只从当前物理连接接收它。

committed journal 与对应 outbox 必须在同一次 `chrome.storage.local.set` 中写入。收到脑对该 result 的 `ack(accepted|duplicate)` 后只删 outbox，journal 保留至 TTL。

## 3. 单次发送时序

```text
脑落 intent/cmd WAL
  -> cmd(chat.sendMessage, idemKey, expectedTail)
  -> base 校验 feature/schema/context/capacity
  -> program 从唯一 live timeline 单次同步投影 baseline，冻结有序 source keys、expectedTail、targetBindingToken
  -> program 用唯一同步 evaluator 预检身份、route/目标、composer.empty、唯一可见控件与硬截止；关联 form 只接受显式 type=button
  -> base 写 journal=attempting，并等待成功
  -> program 以字面同一 evaluator 和同一组参数进入 commit，在写入正文前再核对一次；DOM 只定位控件，不提供消息语义
  -> input/change 后由同一 evaluator 重投影 live timeline 并复核身份、route/目标、正文、source keys、expectedTail、targetBindingToken 与硬截止
  -> evaluator 不读取或互证 owner/model/engine/VNode/listener；disabled/aria-disabled 不作硬闸
  -> evaluator 绿色返回后不再读取页面，立即调用一次标准 click；任一前置变化即清草稿且零点击，原语内部绝不补 click
  -> program 只读同一目标的实时 timeline，严格 +1 确认新 idServer + success + outbound text + contentHash
  -> base 同次写 journal=committed(result) + outbox(result envelope)
  -> 发送 result
  -> 脑持久终局并 ack
  -> base 删除 outbox
```

任何 program 路径都不能直接写 journal/outbox，不能读取历史 journal 决定业务行为，也不能跳过 base 安全点执行动作。

`targetBindingToken` 只回答各次读取是否仍指向同一 `conversationRef+peerPartnerId` 世界目标，不证明发送按钮、框架组件或页面 SDK 已正确接线。若按钮关联 form，显式 `type="button"` 是标准 DOM click 的公开语义硬闸，用于排除 click handler 与 default submit 两条动作路径；原生 `disabled` 只会让标准 click 无效，`aria-disabled` 没有标准阻断语义，二者都不能推出多发、错靶或覆盖输入，因此不作硬闸。

系统构造性保证的是自己的逻辑对标准 click 至多调用一次。平台把一次标准 click 重复执行或送往界面所示对象之外，属于被信任动作契约的违约；即时后置观察、配对验证读与 suspect 可以发现并升级零新增、多新增、目标换绑或读不清，不能撤销已经发生的伤害，也绝不授权第二次 click。

发送正文与线程文本使用同一内容哈希算法：Unicode NFC、首尾 trim、连续 Unicode 空白折叠为一个 ASCII 空格，再做 SHA-256 小写十六进制。证词只保存 hash 和协议结果，不回抄正文。

## 4. 恢复闸与状态裁决

每个声明 `witness/1` 的新会话在进入 normal 前依次完成：

1. outbox 补投：按 hello 快照冲完完整 result；计数漂移或损坏不准越闸。
2. query：脑逐条查询本地账本中的非终局真实 SX。
3. report：手从当前内存状态/journal 回答。
4. 分类完成：终局、等待原执行、验证、suspect，或唯一安全的原 msgId 重投；全部落库后才 normal。

| report | 必要条件 | 脑的动作 |
|---|---|---|
| `done` | committed journal + 完整 result，三者 ref 一致 | 按 result 入终局 |
| `queued/executing` | 当前 SW 内存仍持有原命令 | 等原物理执行，不另发 |
| `attempting` | attempting journal，ref/idemKey 对得上 | 冻结，走 `chat.readThread`；非唯一正匹配即 suspect |
| `unknown` | 没有内存记录与 journal | 仅当 storeId 与派发时一致，才重投原 msgId；否则验证后 suspect |

report 可重复，但脑只采信当前物理连接、当前 recovery generation、目标仍非终局的回答。迟到 report 只审计，不能复活或覆盖终局。

## 5. 故障矩阵

| 故障注入点 | 可观察证词 | 自动裁决 | 多发风险 |
|---|---|---|---|
| attempting 写失败/容量满 | 无 attempting，返回 `WITNESS_UNAVAILABLE/none` | 可由脑按明确零副作用失败策略处理 | 无，动作未放行 |
| attempting 写成功后、动作前 SW 死 | attempting | 验证；阴性也不能重发，转 suspect | 无自动多发 |
| 动作后、后置条件观察前 SW 死 | attempting | 同上 | 无自动多发 |
| 已观察后置条件、双写前 SW 死 | attempting | 同上 | 无自动多发 |
| committed+outbox 写成、发 result 前 SW 死 | committed + outbox | 重连先补投 result | 无重新执行 |
| result 发出但 ack 丢失 | outbox 仍在 | 补投同一 result，脑按 msgId 去重 | 无重新执行 |
| 脑在任一在途点重启 | 手证词仍在 | 冻结后走四阶段恢复 | 无盲重发 |
| chrome.storage 被清空/损坏后重建 | 新 storeId | 失去 unknown 零副作用证明，验证后 suspect | 无自动多发 |
| postcondition 已确认但 committed/outbox 写失败 | journal 仍为 attempting；成功事实未形成可重放终局 | 不发送易失 `ok/confirmed`，熔断执行队列并关闭连接；重连 query 后验证，阴性转 suspect | 无重新发送 |

测试的红线不是“所有故障都自动恢复成功”，而是任意故障组合下自动逻辑不能产生第二条候选人可见消息。允许的退化是少发、冻结、人工。

## 6. 容量、TTL 与健康声明

- journal TTL 14 天，outbox TTL 7 天，各最多 512 条。
- 任一 TTL 条目实际删除前都先旋转 `storeId`，再删除并更新计数；任一崩溃缝都只能留下新世代或计数不一致的 corrupt，不能留下同世代 `unknown`。不逐出未过期条目换容量。
- 任一表到上限时在 attempting 与动作前响亮失败。
- hello/ping 的 `witnessStoreId/outboxPending/journalOpen` 三字段全有或全无；声明 `witness/1` 的 hello 强制三者齐备。
- 计数是恢复诊断与闸门输入，不是业务账本；记录内容仍需逐条 schema 校验。

## 7. 本批不解决的事项

- `chat.sendGreeting`、邀面卡、换微信卡仍是 ver=0，占位但不可上报能力。
- 不引入远程 program 下发、云端连接或跨机信任；这些变化会使“本地可信 + Origin 边界”的威胁模型到期，必须另行冻结协议版本。
- 不承诺平台绝对真相。`chat.readThread` 只提供可审计的 L3 逼近；零匹配不是未发送证明。
- 不证明平台私有组件、事件层或 SDK 的内部接线；Vue owner/component tree/model/engine/VNode/listener 及其互相一致性不再作为动作前守卫或降级观测项。
- 不把候选人/职位采集、AI 决策或沟通状态机塞进投递层。

## 8. 生成与门禁

机器契约一次生成以下能力：

- Go/TS 的 witness DTO、query/report body、guards/data/evidence 类型；
- 原语 metadata：preconditions、guards/evidence schema、verification primitive 与轮次；
- `validateSchema`、`validatePrimitiveGuards`、`validatePrimitiveEvidence`、`validatePrimitiveResult`；
- `WITNESS_UNAVAILABLE` 的结构化 data schema 与 sideEffect 限制。

最低门禁：codegen `-check` 零漂移；Go/TS 同构用例覆盖 witness 三字段、attempting/committed 条件、outbox session 非空、report 状态/ref 关联、effectful evidence 与结构化错误；确定性自动化 harness 覆盖 §5 全表，真实账号只执行一条最小自然消息和恢复复读，不做故障注入。
