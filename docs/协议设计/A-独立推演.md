# 脑-手通信协议规格 · 独立推演(A 卷)

状态:清白室独立推演稿 v1。本卷未阅读既有协议草案(仅从 CLAUDE.md 知其一句话摘要"localhost WebSocket 传输,MQTT 5 语义面"),其余全部机制为独立推导,供与既有草案对撞定稿。

---

## 0. 设计前提与公理

从宪法(CLAUDE.md)与运行环境提炼出的公理。后文每个机制都能回溯到这里。

**A1 脑是唯一可靠记忆体。** 手随时失忆(SW 空闲 30 秒被杀、页面导航杀 content script、浏览器重启、扩展更新),失忆是常态不是故障。因此:一切"账本"只在脑(SQLite);手侧任何存储只能是"证词"(witness),用于缩小歧义窗口,丢了只准退化为"更多转人工",不准导致错误的自主行为。

**A2 语义方向永远是脑到手;传输连接方向是手到脑。** 浏览器扩展无法接受入站连接,所以手主动拨号、断线重连、发心跳;但这只是脑管理"不可靠的手"的方式,协议语义上手没有主动权:手只执行命令、只上报传感事件。

**A3 语义传输无关。** 不允许把任何正确性押在 WebSocket 的特性上(连接即会话、帧内有序、断开即边界)。检验法:把传输换成 MQTT 5(QoS1、可跨会话重投、无连接同一性),协议语义一条不改仍然正确。本文 §13 给出该检验的逐条对照。

**A4 外部副作用极贵且不可回滚。** 给候选人重复发消息等于骚扰,可能封号。纪律:宁可少发;任何歧义不自动重发,只转人工。协议必须让"多发"在机制上不可能由自动逻辑产生,让"少发+人工"成为所有歧义的唯一归宿。

**A5 契约单一源头。** 旧系统约 26% 生产事故源于协议破损(字段别名漂移、回执丢关联 id、动作名不匹配、多通道语义漂移)。对策不是"小心",是构造性保证:`contract/` 目录单一契约文件,codegen 出 Go 类型与 TS 常量,两端禁止手写协议类型;关联字段在生成类型里是非可空的,想丢都编译不过。

**A6 测试页与调度器共用同一命令通道。** 协议里不存在"测试命令"这个概念,只有命令。调试用原语放 `debug.*` 命名空间,走完全相同的信封与分发路径。

**A7 平台无关。** 命令与结果的语义字段一律意图级(candidateRef、threadRef)。DOM、selector、tabId、windowId、平台专有 id 只允许出现在 evidence 与日志字段,脑的逻辑不得读取 evidence 内容做分支。

MV3 事实基线(已联网核实,2026-07):SW 空闲约 30s 被杀;Chrome 116+ 起 WS 上收发消息会重置该空闲计时器(官方推荐约 20s 心跳);Chrome 120+ 起 chrome.alarms 最小间隔 30s;单个事件处理超过约 5 分钟会被强杀;chrome.storage 跨 SW 死亡与浏览器重启存活。

---

## 1. 术语与标识符

| 术语 | 定义 |
|---|---|
| 脑(brain) | Electron 内的 Go 本地常驻服务。命令唯一生产方(测试页与调度器都是它体内的命令生产者)、账本持有方。 |
| 手(hand) | Chrome MV3 扩展。base 层持有 WS 连接、心跳、命令分发器;program 层是原语注册表。 |
| 原语(primitive) | 手可执行的最小意图级动作,契约中登记 `name、argsSchema、class(readonly/effectful)、impact、preconditions、evidenceSchema`。 |
| 命令(cmd) | 脑对某原语的一次调用指示。命令的身份 = 承载它的消息 msgId(重传不变)。 |
| 意图(intent) | 脑侧业务决策的一次"要对某候选人做某事"。一个意图可能先后产生多条命令(重发),它们共享同一 idemKey。 |
| 会话(session) | 一次成功握手到连接终止之间的时段,脑分配 sessionId。命令队列是会话作用域;结果与事件是跨会话持久的。 |
| 重传(retransmit) | 投递层行为:同一 msgId 原样再发,attempt 递增。 |
| 重发(reissue) | 调度层行为:同一意图铸造新命令(新 msgId,同 idemKey)。 |

标识符规范:

| 标识符 | 格式 | 谁生成 | 说明 |
|---|---|---|---|
| msgId | ULID(26 字符 Crockford base32) | 消息发送方 | 全局唯一、按时间可排序。重传不变。 |
| handId | `h-` + 20 随机字符 | 手首次运行生成,存 chrome.storage.local | 稳定标识一个浏览器 profile 里的手。属基础设施数据(宪法禁令 2 允许)。 |
| sessionId | `s-` + ULID | 脑在 welcome 中分配 | |
| idemKey | 见 §5.4 | 脑 | 幂等键,确定性派生自业务行,非随机。 |
| blob 引用 | `sha256:<64hex>` | 内容寻址,谁上传谁算 | 见 §11。 |
| accountRef / candidateRef / threadRef | 不透明字符串 | 脑的业务层定义 | 协议只传递不解释。 |

时间:一律 Unix epoch 毫秒,int64。时间戳只用于日志、诊断、过期判定(deadline);**永不**用于去重或排序的正确性(反模式 §14-4)。脑手同机,时钟共享;将来跨机部署时 deadline 语义需重审(见 §16)。

命名纪律:线上字段一律 camelCase(TS 零映射;Go 用 tag),全部字段名是 contract codegen 输出的常量,两端手写字段名视为事故。同一概念全协议只有一个名字:所有"指向另一条消息"的字段都叫 `ref`,不存在 taskId/cmdId/jobId 别名——这是对旧系统"别名漂移 + 回执丢关联字段"两类事故的构造性封杀。

---

## 2. 消息信封

一条 WS 文本帧承载一条 JSON 消息,不批量、不拼接。信封 6 个字段,冻结;扩展只许发生在 body 内。

```json
{
  "proto": 1,
  "kind": "cmd",
  "msgId": "01JB2K3G8XF5N7Q9R0S1T2U3V4",
  "session": "s-01JB2K1AAAAAAAAAAAAAAAAAAA",
  "ts": 1768546201345,
  "attempt": 1,
  "body": { }
}
```

| 字段 | 类型 | 谁生成 | 语义 | 示例 |
|---|---|---|---|---|
| proto | int | 发送方(编译期常量) | 协议主版本。不兼容变更才递增;主版本内只做加字段,接收方必须忽略不认识的字段(must-ignore,借鉴 protobuf unknown fields)。 | `1` |
| kind | string 枚举 | 发送方 | 消息种类,见 §3。接收方不认识的 kind:命令方向必须 ack 拒绝(PROTO_UNSUPPORTED_KIND),上报方向记日志丢弃。 | `"cmd"` |
| msgId | string(ULID) | 发送方 | 该消息的逻辑身份。重传时不变,由此成为去重键与关联锚点。对 kind=cmd,它就是命令 id。 | `"01JB..."` |
| session | string 或 null | 发送方(值来自 welcome) | 该消息**创建时**所属会话。hello/welcome/bye 为 null。命令只在其 session 等于手的当前会话时可执行;结果与事件带创建时会话,跨会话仍有效(§7)。 | `"s-01JB..."` |
| ts | int64 | 发送方 | 发送方本地时钟,毫秒。仅诊断。重传时更新为当次发送时刻。 | `1768546201345` |
| attempt | int ≥1 | 发送方 | 同一 msgId 的第几次发送。仅诊断与告警(attempt>1 说明投递层在重传)。 | `1` |
| body | object | 发送方 | kind 专属载荷,schema 由 contract 定义。 | |

重传时信封的可变字段:ts、attempt、session(命令跨会话重投时更新为当前会话);msgId 与 body 不可变。

为什么信封这么小:信封是两端 base 层唯一需要理解的东西,越小越不会漂移。对标 JSON-RPC 2.0(jsonrpc/id/method/params 四件套)——取其"极小信封 + id 关联"的形,弃其不足(无生命周期、无投递语义、error 不够结构化,见 §15)。

大小纪律:单条 WS 消息序列化后上限 128 KB(welcome 下发 `limits.maxMsgBytes`);其中建议 body 内联数据不超过 32 KB(`limits.inlineBytes`),超过的内容走 blob 通道(§11)。超限消息接收方 ack 拒绝 `PROTO_MSG_TOO_LARGE`。

---

## 3. 消息种类总表

13 种。方向列中 B=脑,H=手。

| kind | 方向 | 需要对方 ack? | 用途 |
|---|---|---|---|
| hello | H→B | 以 welcome/bye 作答 | 握手第一步:自报身份、能力、版本 |
| welcome | B→H | 否 | 握手完成:分配会话、下发参数 |
| bye | 双向 | 否 | 拒绝握手或宣告关闭,带原因码 |
| cmd | B→H | 是(ack) | 下发命令 |
| ack | 双向 | 否 | 收妥回执:对 cmd/result/event 的"已收到并落地" |
| progress | H→B | 否(可丢) | 命令执行中的阶段汇报,兼作执行期活性信号 |
| result | H→B | 是(ack) | 命令终局回报,唯一携带 data/error/evidence 的地方 |
| event | H→B | 是(ack) | 传感事件上报(感知通道的形态,§12) |
| query | B→H | 以 report 作答 | 脑询问某命令在手侧的状态(对账/歧义消解) |
| report | H→B | 否 | 对 query 的回答,从手的日志/内存作答 |
| cancel | B→H | 是(ack) | 请求取消在途命令,尽力而为 |
| ping | H→B | 以 pong 作答 | 应用层心跳,携带上下文就绪度 |
| pong | B→H | 否 | 心跳应答,带脑侧时钟 |

设计要点与对标:

- **ack 是端到端应用层回执,不是传输回执。** WS 没有应用 ack;将来换 MQTT,PUBACK 只代表 broker 收到,不代表对端收到——把 L1 确认做成显式消息,才在任何传输下语义一致。对标 Socket.IO 的 ack 回调(取其分层确认思想,但做成显式消息而非传输库特性)。
- **ack 双向复用同一 kind。** 脑对 result/event 的 ack 表示"已持久化到 SQLite,可从 outbox 删除";手对 cmd 的 ack 表示"已解析、已过验、已入执行队列(或拒绝)"。同一机制,消灭"第三种回执"的漂移空间。
- **progress 不需要 ack、允许丢。** 它是活性信号与诊断,不承载正确性;给它可靠性会白白增加两端状态。对标 Temporal activity heartbeat(同样是"丢了没关系,断了才有意义")。
- **query/report 是对账原语。** 对标 AWS IoT Jobs 的 DescribeJobExecution:设备端(手)对指定 job(cmd)报告 execution 状态,是失联恢复后消歧义的关键工具。

### 3.1 hello(手→脑)

```json
{
  "proto": 1, "kind": "hello", "msgId": "01JB2K00HELLO0000000000000",
  "session": null, "ts": 1768546200000, "attempt": 1,
  "body": {
    "handId": "h-7f3ak2m9qwe8rty0uiop",
    "auth": "pair-token-9f8e7d6c",
    "protoSupported": [1],
    "contractHash": "sha256:2b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfe",
    "app": { "extVersion": "0.3.1", "browser": "chrome/126.0.6478.127" },
    "caps": [
      "debug.echo@1",
      "probe.context@1",
      "collect.candidateList@1",
      "collect.candidateDetail@1",
      "nav.ensurePage@1",
      "chat.openThread@1",
      "chat.sendGreeting@1",
      "chat.sendMessage@1",
      "chat.sendInviteCard@1"
    ],
    "features": ["blobHttp/1", "cancel/1", "progress/1"],
    "outboxPending": 2,
    "journalOpen": 1
  }
}
```

| body 字段 | 类型 | 语义 |
|---|---|---|
| handId | string | 手的稳定身份。脑用它做手注册表主键与会话接管判定。 |
| auth | string | 配对令牌。首次使用时用户从客户端 UI 抄进扩展选项页,存 chrome.storage(基础设施数据)。防的是本机其他进程连上 WS 冒充手;脑同时校验 WS Origin 头必须等于登记的 `chrome-extension://<id>`(防跨站 WebSocket 劫持)。 |
| protoSupported | int[] | 手能说的协议主版本列表,脑从中选。 |
| contractHash | string | 手编译期嵌入的契约文件哈希。仅用于漂移告警,不做硬门禁(§7.1、§9)。 |
| app | object | 版本诊断信息。 |
| caps | string[] | 能力集:`原语名@版本`。会话内不可变;program 热更后以重连新 hello 重新声明(localhost 重连成本可忽略,换来"会话内能力恒定"这一强不变量)。 |
| features | string[] | 协议特性开关(非原语):blob 通道、cancel 支持等。 |
| outboxPending / journalOpen | int | 诊断计数:待补投的上报数、未终局的副作用日志条数。真正的补投与对账在 welcome 后进行。 |

### 3.2 welcome(脑→手)

```json
{
  "proto": 1, "kind": "welcome", "msgId": "01JB2K00WELC0000000000000",
  "session": null, "ts": 1768546200450, "attempt": 1,
  "body": {
    "session": "s-01JB2K1AAAAAAAAAAAAAAAAAAA",
    "proto": 1,
    "hb": { "intervalMs": 20000, "graceMs": 50000 },
    "limits": { "maxMsgBytes": 131072, "inlineBytes": 32768 },
    "blob": {
      "endpoint": "http://127.0.0.1:17872/v1/blobs",
      "token": "bt-01JB2K...",
      "maxBytes": 20971520
    },
    "brain": { "version": "0.3.0", "epoch": 42 },
    "contractHash": "sha256:2b7e1516...",
    "contractMatch": true,
    "now": 1768546200449
  }
}
```

| body 字段 | 语义 |
|---|---|
| session | 本会话 id。此后双方所有消息信封携带之。 |
| proto | 脑选定的协议主版本。 |
| hb | 心跳参数:手每 intervalMs 发 ping;任一方向静默超 graceMs 判定链路死亡(§6)。 |
| limits | 消息大小纪律,§2。 |
| blob | 大载荷通道:端点、会话作用域令牌、单件上限(§11)。 |
| brain.epoch | 脑侧单调递增的世代号(存 SQLite,每次脑进程启动 +1)。用途:日志与将来多写者围栏的钩子(§7.4)。 |
| contractMatch | 契约哈希是否一致。false 时仅告警与上报遥测,能力门禁靠 caps 的逐原语版本(§9)。 |
| now | 脑时钟,供手做时钟差诊断(不用于正确性)。 |

握手失败时脑以 bye 作答后关闭连接:`{"code":"AUTH_FAILED"}`、`PROTO_INCOMPATIBLE`、`SUPERSEDED`(同 handId 新连接顶掉旧连接时发给旧连接,对标 MQTT 的同 ClientID 会话接管)、`SHUTTING_DOWN`。

### 3.3 cmd(脑→手)

```json
{
  "proto": 1, "kind": "cmd", "msgId": "01JB2K3G8XF5N7Q9R0S1T2U3V4",
  "session": "s-01JB2K1AAAAAAAAAAAAAAAAAAA", "ts": 1768546201345, "attempt": 1,
  "body": {
    "name": "chat.sendGreeting",
    "ver": 1,
    "context": { "platform": "zhilian", "accountRef": "acc-01" },
    "args": {
      "candidateRef": "cand-98a7f3",
      "text": "您好,看到您投递的Go后端岗位,想和您聊聊。"
    },
    "idemKey": "ik1:zhilian:acc-01:chat.sendGreeting:cand-98a7f3:int-01JB2K2ZZZ",
    "deadline": 1768546801345,
    "execBudgetMs": 60000,
    "guards": { "noPriorMessageFromUs": true }
  }
}
```

| body 字段 | 类型 | 必填 | 语义 |
|---|---|---|---|
| name / ver | string / int | 是 | 原语名与版本,必须在该会话 caps 内,否则 ack 拒绝 PROTO_UNSUPPORTED_CMD。 |
| context | object | 是 | 路由上下文:平台 + 账号引用。两端不必理解 args 即可完成路由、就绪度判断与串行化。 |
| args | object | 是 | 原语参数,schema 来自契约。意图级,无 DOM 词汇。 |
| idemKey | string | effectful-external 必填,其余禁止 | 幂等键,§5.4。 |
| deadline | int64 | 是 | 绝对过期时刻(脑钟)。手在出队时与不可逆动作前一刻双重检查,过期即回 result(expired),绝不执行。对标 MQTT 5 message expiry / Azure IoT Hub C2D 消息 TTL:僵尸命令自灭,防"手睡醒后执行昨天的命令"。 |
| execBudgetMs | int | 是 | 手侧单次执行预算,超时手自行中止并回 EXEC_TIMEOUT_HAND。上限 240000(MV3 单事件约 5 分钟强杀,留余量)。 |
| guards | object | 可选 | 语义化前置条件断言,词汇由契约按原语定义。手执行前在页面上核验,不成立则回 GUARD_FAILED 且保证零副作用。对标 HTTP 条件请求(If-Match):把"读-判-写"的判定推到离真相最近的一端,压缩决策与执行之间的竞态窗口。 |

手的执行模型(v1):**全局严格串行**,先 ack 入队,队列 FIFO,深度上限 16,满则 ack 拒绝 QUEUE_FULL(背压信号,脑本来就应节流)。不设并行车道——一只手操作一个浏览器的一个前台现实,串行是诚实的模型;将来要并行属主版本演进。

### 3.4 ack(双向)

```json
{
  "proto": 1, "kind": "ack", "msgId": "01JB2K3H0AAAAAAAAAAAAAAAAA",
  "session": "s-01JB2K1AAAAAAAAAAAAAAAAAAA", "ts": 1768546201398, "attempt": 1,
  "body": {
    "ref": "01JB2K3G8XF5N7Q9R0S1T2U3V4",
    "status": "accepted"
  }
}
```

| body 字段 | 语义 |
|---|---|
| ref | 被确认消息的 msgId。生成类型里非可空——"回执丢关联字段导致任务静默挂起"从类型上灭绝。 |
| status | `accepted` / `rejected` / `duplicate`。 |
| error | status=rejected 时必填,§8 错误对象(此层错误一定 sideEffect=none)。 |

语义按被确认对象:对 cmd,accepted=已入执行队列;对 result/event,accepted=脑已持久化,手可从 outbox 删除。duplicate=去重命中:**去重不等于沉默**,重复到达必须重新 ack(经典 at-least-once 规则,否则对端永远重传);若重复的 cmd 已有终局,手在 duplicate ack 后立即补发一份 result(replayed=true),对标 Stripe 幂等键的"重放已存结果"。

### 3.5 progress(手→脑)

```json
{
  "proto": 1, "kind": "progress", "msgId": "01JB2K3J5PPPPPPPPPPPPPPPPP",
  "session": "s-01JB2K1AAAAAAAAAAAAAAAAAAA", "ts": 1768546203100, "attempt": 1,
  "body": { "ref": "01JB2K3G8XF5N7Q9R0S1T2U3V4", "stage": "threadOpened", "detail": null }
}
```

stage 为自由字符串,**明确不入契约、不承载语义**,脑只用于:重置执行期活性计时(progressGap,§4.3)、日志、UI 展示。禁止脑逻辑对 stage 值做分支(反模式 §14-15 变体)。

### 3.6 result(手→脑)

```json
{
  "proto": 1, "kind": "result", "msgId": "01JB2K3M9RRRRRRRRRRRRRRRRR",
  "session": "s-01JB2K1AAAAAAAAAAAAAAAAAAA", "ts": 1768546208420, "attempt": 1,
  "body": {
    "ref": "01JB2K3G8XF5N7Q9R0S1T2U3V4",
    "status": "ok",
    "data": { "greetingSentAt": 1768546208001, "threadRef": "th-5f6a7b" },
    "evidence": [
      { "type": "postcondition", "text": "会话流末尾出现我方消息气泡,文案前缀匹配" },
      { "type": "screenshot", "blob": "sha256:9c56cc51b374c3ba189210d5b6d4bf57790d351c96c47c02190ecf1e430635ab" }
    ],
    "replayed": false,
    "execMs": 6890
  }
}
```

| body 字段 | 类型 | 语义 |
|---|---|---|
| ref | string | 对应命令 msgId。 |
| status | 枚举 | `ok` / `failed` / `canceled` / `expired`。四者皆为终局;"拒收"不在此处(那是 ack 层)。 |
| data | object | status=ok 时按原语契约的结果 schema。 |
| error | object | status=failed 时必填,§8。 |
| evidence | array | 证据列表,effectful 原语 ok 时必填(evidenceSchema 契约登记)。**effectful 的 ok 定义为"观察到后置条件"**,"我点了按钮"不算——宪法原文。元素形如 `{type, text?, blob?}`,内容对脑是不透明的(只入日志与人工审核界面,A7)。 |
| replayed | bool | true 表示这是幂等去重后重放的历史结果,非新执行。 |
| execMs | int | 诊断。 |

result 进入手侧持久 outbox,直到收到脑的 ack 才删除——手失忆或脑重启都不丢终局(§7.3)。

### 3.7 event(手→脑)

```json
{
  "proto": 1, "kind": "event", "msgId": "01JB2K4A0EEEEEEEEEEEEEEEEE",
  "session": "s-01JB2K1AAAAAAAAAAAAAAAAAAA", "ts": 1768546260012, "attempt": 1,
  "body": {
    "name": "sense.inboxSnapshot",
    "context": { "platform": "zhilian", "accountRef": "acc-01" },
    "observedAt": 1768546259800,
    "dedupKey": "inbox:acc-01",
    "data": {
      "threads": [
        { "threadRef": "th-5f6a7b", "unread": 2, "lastMsgAt": 1768546255000 },
        { "threadRef": "th-9c0d1e", "unread": 1, "lastMsgAt": 1768546240000 }
      ]
    }
  }
}
```

事件是提示不是账本(宪法禁令 3):脑收到只作为"提前巡检"的触发线索,真相靠脑下发 readonly 对账命令取回。dedupKey 可选,供脑对同键事件做"新值覆盖旧值"的合并。形态上**推荐快照语义而非增量语义**(level-triggered,对标 Kubernetes 调和哲学):快照天然幂等、丢失无害(下一份快照自愈),完美匹配"事件可丢、对账兜底"的宪法立场。具体感知策略由另一路设计,本协议只保证:event 至少一次送达(outbox)+ 脑侧按 msgId 去重 + dedupKey/observedAt 两个合并钩子。

### 3.8 query / report(脑→手 / 手→脑)

```json
{ "proto": 1, "kind": "query", "msgId": "01JB2K5B0QQQQQQQQQQQQQQQQQ",
  "session": "s-01JB2K6NEW0000000000000000", "ts": 1768546500000, "attempt": 1,
  "body": { "ref": "01JB2K3G8XF5N7Q9R0S1T2U3V4" } }

{ "proto": 1, "kind": "report", "msgId": "01JB2K5C0TTTTTTTTTTTTTTTTT",
  "session": "s-01JB2K6NEW0000000000000000", "ts": 1768546500090, "attempt": 1,
  "body": {
    "ref": "01JB2K3G8XF5N7Q9R0S1T2U3V4",
    "state": "attempting",
    "result": null,
    "journal": { "idemKey": "ik1:zhilian:acc-01:chat.sendGreeting:cand-98a7f3:int-01JB2K2ZZZ",
                 "startedAt": 1768546203000 }
  }
}
```

report.state 枚举:

| state | 含义 | 脑可推出 |
|---|---|---|
| unknown | 手的内存与日志均无此命令 | 命令从未在此手开始过副作用(前提:S 类执行前必写日志,§5.5)→ 可安全重发 |
| queued | 在队列尚未开始 | 零副作用;可 cancel 后重发或就地等待 |
| executing | 正在执行 | 等 result;不许重发 |
| attempting | (仅 effectful-external)日志显示已越过"提交点前写入",终局未知 | 歧义,只可转人工 |
| done | 有终局,result 字段内嵌完整 result body | 直接采信,补记账本 |

### 3.9 cancel(脑→手)

```json
{ "proto": 1, "kind": "cancel", "msgId": "01JB2K5D0CCCCCCCCCCCCCCCCC",
  "session": "s-01JB2K1AAAAAAAAAAAAAAAAAAA", "ts": 1768546230000, "attempt": 1,
  "body": { "ref": "01JB2K3G8XF5N7Q9R0S1T2U3V4", "reason": "timeout" } }
```

尽力而为、幂等:目标在队列中则移除并回 result(canceled);正在执行则只在**取消安全点**(不可逆动作之前的步骤边界)生效;已过提交点则忽略取消、如实回原 result。对已终局/未知命令的 cancel,手回 ack(accepted)后无后续(脑靠 query 看真相)。取消与结果赛跑时 **result 赢**:脑收到 ok 就按 ok 记账(副作用已发生,记账必须反映真相)。

### 3.10 ping / pong

```json
{ "proto": 1, "kind": "ping", "msgId": "01JB2K5E0GGGGGGGGGGGGGGGGG",
  "session": "s-01JB2K1AAAAAAAAAAAAAAAAAAA", "ts": 1768546220000, "attempt": 1,
  "body": {
    "contexts": [
      { "platform": "zhilian", "accountRef": "acc-01", "ready": true, "reason": null },
      { "platform": "zhilian", "accountRef": "acc-02", "ready": false, "reason": "pageAbsent" }
    ],
    "queueDepth": 1,
    "swStartedAt": 1768546190000
  } }

{ "proto": 1, "kind": "pong", "msgId": "01JB2K5F0HHHHHHHHHHHHHHHHH",
  "session": "s-01JB2K1AAAAAAAAAAAAAAAAAAA", "ts": 1768546220004, "attempt": 1,
  "body": { "now": 1768546220003 } }
```

ping 由**手**发起,一石三鸟:(1) Chrome 116+ 下 WS 收发重置 SW 空闲计时器,20s < 30s,SW 连接期间不死;(2) 手凭 pong 缺失检测半开连接并主动重连;(3) 携带上下文就绪度,是脑健康模型(§6)的数据源。ready 的判定标准(哪些页面算就绪)属手内实现,协议只传布尔与 reason 枚举(`pageAbsent`/`loggedOut`/`pageBroken`/`unknown`),不传 tabId——A7。

---

## 4. 命令生命周期与确认层级

### 4.1 脑侧状态机(账本状态,SQLite 持久)

```
                    +--------------------------- retransmit(同msgId,attempt+1) ---+
                    |                                                             |
 draft -> queued -> sent --ack(accepted)--> accepted --progress*--> (executing)   |
   |        ^        |  \--ack(rejected)--> rejected(终局)                        |
   |        |        +--ackTimeout×3耗尽 / 连接死----> queued(R/N/S均可,见§5.3)--+
   |        |
   |        +---- 会话重建后对未 ack 命令重新投递
   |
   |  accepted/executing --result--> ok | failed | canceled | expired  (终局,以 result 为准)
   |  accepted/executing --execTimeout/progressGap--> cancel+query --report--> 终局
   |                                        \-- 手失联/report=attempting --> ambiguous(终局,转人工)
   |  queued/sent --deadline 到--> expired(终局;若曾 accepted 且 S 类,先走 query 消歧)
   |
 人工处置 ambiguous --> resolvedOk | resolvedFailed(+可选地由人铸造新意图)
```

状态含义与允许转移全部入契约(codegen 出 Go 的状态常量与合法转移表,非法转移直接 panic 进告警)。对标 AWS IoT Jobs 的 execution 状态集(QUEUED/IN_PROGRESS/SUCCEEDED/FAILED/TIMED_OUT/REJECTED/CANCELED):同构的"云侧持久任务 + 设备离线也不丢 + 状态由设备回报推进"模型,这正是脑手关系。

不变量:**先记账后发送**(write-ahead:命令必先以 queued 落 SQLite 再进 socket)。由此脑的账本永远是手所见命令的超集,手日志里出现脑不认识的命令即为严重告警。

### 4.2 手侧处理管线

```
收到 cmd
 -> 信封/schema 校验失败 --> ack(rejected, PROTO_*)          [零副作用]
 -> session 不是当前会话 --> ack(rejected, STALE_SESSION)     [零副作用]
 -> caps 无此原语      --> ack(rejected, PROTO_UNSUPPORTED_CMD)
 -> msgId 命中近期已见 --> ack(duplicate) [+ 若已有终局补发 result(replayed)]
 -> idemKey 命中日志   --> ack(duplicate) + 按日志状态:done→重放 result;attempting/executing→等待原执行
 -> 队列满            --> ack(rejected, QUEUE_FULL)
 -> 入队              --> ack(accepted)
出队
 -> deadline 已过 --> result(expired)
 -> guards 核验失败 --> result(failed, GUARD_FAILED, sideEffect=none)
 -> [仅 effectful-external] 日志写入 attempting(含 idemKey、ref)  ...(a)
 -> 执行原语(期间可发 progress;取消安全点仅存在于 (a) 之前)
 -> 观察后置条件成立 --> [仅 effectful-external] 日志改写 committed+缓存 result  ...(b)
 -> result 入 outbox,发送,等脑 ack 后删除
```

关键顺序论证(A4 的机制化):(a) 在任何不可逆动作**之前**落盘,(b) 在观察到后置条件**之后**落盘。SW 在任意点死亡的后果:死于 (a) 前=零副作用,重发安全;死于 (a)(b) 之间=日志停在 attempting,report 只会说 attempting,脑归 ambiguous 转人工——**可能少发,绝不多发**;死于 (b) 后=终局在日志与 outbox,复活后补投。任何自动路径都不会在 attempting 之上再次执行同 idemKey——多发在手侧机制上不可能,脑侧再由账本把第二道闸(§5.6)。

### 4.3 确认层级:每层能推出什么

| 层 | 信号 | 能推出 | 不能推出 |
|---|---|---|---|
| L0 | WS write 返回 | 字节进了本端缓冲 | 对端收到(哪怕 TCP ACK 也只到内核) |
| L1 | ack(accepted) | 手收到、解析通过、入了执行队列 | 会被执行、页面可用、会成功 |
| L2 | result 终局 | 手对该命令的终局判断 + 副作用标注 + 证据;effectful ok=后置条件已在页面观察到 | 平台真把消息投给了候选人(UI 可能假成功、风控可能吞消息) |
| L3 | 脑的业务核验(下发 readonly 对账命令回读会话流/沉默锚点) | 逼近平台侧真相 | 绝对真相(平台内部状态不可见) |

这是端到端论证(Saltzer end-to-end argument)的直接应用:低层确认永远不能替代高层核验,所以协议只承诺把 L1/L2 做可靠、把 L3 需要的证据带回来;L3 本身是脑的业务巡检(宪法:沉默锚点只算真正已发出的消息)。超时基线:L1 缺失按投递问题处理(重传);L2 缺失按执行问题处理(查询/取消/歧义);L3 不通过**不自动重发,只转人工**——红线原文。

---

## 5. 投递保证与幂等

### 5.1 选型结论

- **命令通道:至少一次投递 + 手侧两级去重(msgId、idemKey)= 有效一次入队。**
- **执行语义:readonly / effectful-local 至少一次(可重发);effectful-external 至多一次(歧义即停,人工兜底)。**
- **上报通道(result/event):至少一次(手侧持久 outbox 直到脑 ack)+ 脑侧按 msgId 持久去重。**

### 5.2 为什么不是"恰好一次"

两将军问题:确认本身会丢,任何有限轮确认都无法让双方对"已送达"达成共识,协议级恰好一次投递不存在。MQTT QoS 2 的四步握手只保证 broker 与 client 之间报文不重,应用崩溃重启后照样可能重复处理,代价还翻倍——这是"QoS2 迷信"(反模式 §14-9)。业界共识路径是 **至少一次 + 幂等收敛**(Kafka 事务/EOS 的本质、Temporal activity 的官方立场:activity 至少执行一次,业务自己幂等)。本场景再叠加 A4:对外副作用连"幂等重试"都嫌贵,于是 effectful-external 在歧义处降级为至多一次——宁可少发。

### 5.3 重传与去重的安全论证

前置规则(缺一不可,均入实现验收):

1. 手 ack 顺序:**先 ack 后执行**(accepted 表示入队,不表示开始)。
2. 手断线即弃队:连接断开时,队列中尚未开始执行的命令全部丢弃(它们属于死会话);正在执行的原子步骤走完并按 §4.2 落日志。脑靠重投恢复。
3. 命令会话围栏:cmd.session 不等于手当前会话即拒收(STALE_SESSION)。防将来传输层(如 MQTT broker)跨会话吐旧命令。
4. 去重先于一切副作用:msgId 近期表(内存,会话级)与 idemKey 日志(持久)的查询发生在入队前。

由此:未见 ack 的命令,无论重传多少次(同 msgId)都安全——要么从未到达(重传即首达),要么到达过但被 2/3 丢弃(重传即首执行),要么已入队/已执行(去重命中,duplicate ack + 必要时重放 result)。见过 ack(accepted) 之后,S 类**永不盲重传/盲重发**,只走 query 消歧(§7.2)。

### 5.4 幂等键(idemKey)规范

```
ik1:{platform}:{accountRef}:{primitive}:{targetRef}:{intentId}
例  ik1:zhilian:acc-01:chat.sendGreeting:cand-98a7f3:int-01JB2K2ZZZ
```

- **脑生成、确定性派生**:intentId 是脑 SQLite 意图行主键。同一业务意图无论重发多少次命令、无论脑重启多少次,idemKey 恒同——这是与 Stripe 幂等键的关键同异:同(客户端生成、服务端存首个结果并重放),异(Stripe 建议随机 V4 键,靠调用方在重试时复用;我们直接从业务行派生,把"忘了复用"这种人祸也灭掉)。
- **语义粒度 = 意图**,不是"候选人+动作"全局唯一:业务允许对同一候选人隔天再发跟进消息,那是新意图新键;而"这一轮打招呼"无论怎么重试都是一个键。粒度裁决权在脑的业务层,协议只规定格式与唯一性。
- 仅 effectful-external 命令携带;readonly/effectful-local 禁止携带(避免假安全感)。

### 5.5 手侧日志与 outbox(证词,不是账本)

chrome.storage.local 中仅 base 层可读写的两张表:

```
journal:{idemKey} = { ref, state: "attempting"|"committed", startedAt, committedAt?, result? }
outbox:{msgId}    = { 完整 result/event 消息, createdAt }
```

与宪法禁令 2(不得持久化业务状态)的关系,明确论证:这两张表是**投递层基础设施**,同类于 TCP 重传缓冲——(1) 键与值全是协议标识符与协议消息,不含业务决策输入;(2) program 层无法访问,只有 base 的投递层读写;(3) 有 TTL(committed 日志 14 天、outbox 7 天)与容量上限(各 512 条,超限告警并拒新 S 类命令);(4) **允许整体丢失**:丢失的后果是更多命令走 ambiguous 转人工(可用性下降),绝不产生错误自主行为(正确性无损)。它使手"失忆后至少记得自己动过手术刀",把人工介入频率从"每次 SW 死在执行窗口"压到"日志也一起丢了才需要"。

### 5.6 幂等的最终兜底是脑

三道闸,层层冗余:

1. **脑账本闸(权威)**:intents 表 UNIQUE(idemKey);派发 effectful-external 前查账本,已有非失败终局的意图拒绝铸造新命令。手日志全丢也拦得住,因为脑从不忘。
2. **手日志闸(缩窗)**:§5.5,拦住"脑在歧义中被人工误判后重发"与执行窗口内的重复投递。
3. **平台回读闸(L3)**:发送前 guards(如 noPriorMessageFromUs)+ 发送后对账回读。哪怕前两道全失效,读到"已有我方消息"就停手转人工。

重复发送需要三道闸同时失效;而每道闸单独失效的后果都是"少发+人工"。这就是 A4 要求的形状。

---

## 6. 命令分类学与超时重发矩阵

契约按宪法登记 `class(readonly|effectful)`;effectful 再按影响面分 `impact(local|external)`。三档待遇:

| 档 | 定义 | 例 | 协议待遇 |
|---|---|---|---|
| R:readonly | 不改变任何状态 | collect.candidateList、collect.candidateDetail、probe.context、debug.echo | 自由重传重发;无 idemKey;无 evidence 强制 |
| NL:effectful-local | 只改浏览器内状态,声明式、天然幂等 | nav.ensurePage、chat.openThread | 可重发;命令必须写成"确保处于目标状态"而非过程步骤(幂等靠构造,反模式 §14-11) |
| SX:effectful-external | 对外部世界产生不可回滚副作用 | chat.sendGreeting、chat.sendMessage、chat.sendInviteCard | idemKey 必填、日志 (a)(b) 必做、evidence 必填、歧义即人工 |

矩阵(默认值,全部可由契约按原语覆写;时间基于 localhost):

| 参数 | R | NL | SX | cancel/query |
|---|---|---|---|---|
| ackTimeout(同 msgId 重传间隔) | 3s,重传于 3s/6s,共 3 次尝试 | 同 R | 同 R(§5.3 论证了安全性) | 3s × 3 |
| 三次无 ack 后 | 判连接可疑:主动断开触发重连,命令回 queued 待重投 | 同 R | 同 R | 放弃(等重连对账) |
| execBudgetMs 默认 | 30s(采集类可契约覆写至 120s) | 20s | 60s | 5s |
| progressGap(无进展超时) | 15s | 不适用 | 20s | 不适用 |
| 执行超时/无进展时脑的动作 | cancel + 放弃本次 | cancel | cancel + query,按 report 归类;拿不到 report → ambiguous | — |
| 自动重发(新 msgId 同意图) | ≤2 次,间隔 5s/15s | ≤2 次 | **仅当**拿到零副作用证明(report=unknown/queued,或 result 明示 sideEffect=none)才允许 1 次;凡 sideEffect=possible 一律 ambiguous 转人工 | 不限(无副作用) |
| deadline 典型值 | 下发后 5 分钟 | 2 分钟 | 10 分钟(=意图有效期) | 1 分钟 |

对标:R/NL 的"执行超时后重新变为可派发"就是 SQS visibility timeout 的形状(取其自动重投),但对 SX 明确**拒绝**该形状——SQS 重投假设消费是幂等或可容重复,本场景的昂贵动作不满足假设,重投权上收给人与账本。Temporal 按 activity 类型配 RetryPolicy 的做法被完整借鉴为"矩阵可由契约按原语覆写"。

---

## 7. 失联与恢复

### 7.1 重连与握手序列

手侧重连退避:1s 起,×2 至上限 30s,每次 ±30% 抖动;连接稳定 60s 后退避归零。另设 chrome.alarms 每 60s 的看门狗:若发现未连接且无在途连接尝试则立即发起(alarms 是基础设施用途,禁令 1 允许;它保证重连循环在 SW 被杀后仍会复活)。chrome.runtime onStartup/onInstalled 时立即尝试。

```
手                                     脑
 |-- WS connect ws://127.0.0.1:{port}/v1/channel -->|
 |-- hello(handId, auth, caps, contractHash) ------>|   校验 auth+Origin;同 handId 旧连接
 |<-- welcome(session, hb, limits, blob, epoch) ----|   先收 bye(SUPERSEDED) 后被关闭
 |== 阶段1:手补投 outbox(旧 session 值原样保留,attempt+1)==>| 脑逐条 ack(msgId 去重)
 |<== 阶段2:脑对账:对账本中所有非终局命令逐条 query ==|
 |== 手按 journal/内存回 report ==>|                      脑按 §3.8 表归类处置
 |<== 阶段3:脑重投:未 ack 且未过期命令(session 更新为新会话,attempt+1)==|
 |<== 阶段4:恢复正常派发;手开始 20s ping ==|
```

阶段顺序有讲究:先收终局(阶段 1)再对账(阶段 2),避免对已有答案的命令白问;对账完再重投(阶段 3),避免对 attempting 的意图误重投。

### 7.2 手失忆后,脑侧在途命令的悬空处置

脑在连接断开时对每条非终局命令标注 suspect;重连对账后按下表终局化或续跑:

| 断开时脑侧状态 | 对账结果 | 处置 |
|---|---|---|
| sent(从未 ack) | report=unknown | R/NL/SX 均安全重投(§5.3) |
| accepted/executing | report=done(含 result) | 直接记账终局 |
| accepted/executing | report=queued | 队列已按规则 2 丢弃,此值仅出现于同 SW 未死的短断线;等价 unknown 处理 |
| accepted/executing | report=executing | 继续等 result,重置 progressGap |
| accepted/executing | report=attempting | **ambiguous,转人工**,附 journal 与账本两侧证据 |
| accepted/executing | report=unknown(日志无) | R/NL:重发;SX:说明 (a) 尚未写就死了 → 零副作用,允许按矩阵重发 1 次 |
| 任意 | 手在 graceMs 内没回来 | R/NL:过 deadline 自然 expired;SX:挂 pendingReconcile,手回来走对账,超过意图 deadline 仍无对账 → ambiguous 转人工 |

注意最后一行:SX 命令在手长期失联时**不会**被脑单方面判死,因为"手死前发没发出去"不可知;它变成人工队列里一条带全部证据的待核验项。这正是"核验失败不自动重发、只转人工"的协议化。

### 7.3 脑进程重启/升级时,手侧未送出的回报

- result/event 在 outbox,重连新脑会话后原 msgId 补投;脑按 msgId 去重(processed 消息表,保留 30 天)——重启不丢终局、不重复记账。
- 手侧发现连接断开即进入重连循环,对手而言脑重启与网络闪断无区别(A3:不依赖对端进程同一性)。
- 脑重启后 epoch+1;账本里 sent/accepted 状态的命令自动进入 §7.2 流程。**先记账后发送**保证重启后账本完整。

### 7.4 会话接管与围栏

- 同 handId 二次连接:新连接胜出,旧连接收 bye(SUPERSEDED) 后被关——对标 MQTT 同 ClientID 接管语义。防扩展多实例/僵尸 SW 双连。
- 不同 handId 声称同一 accountRef:业务层仲裁(脑的手注册表对每个 context 只授一只"活手",其余手对该 context 的就绪度被忽略)。v1 单脑单手,完整的 lease/epoch 围栏(Kafka zombie fencing 的形状)不进协议,只留钩子:welcome.brain.epoch 已下发,将来命令级围栏需要时升主版本。理由:单机上"脑"的唯一性由端口独占天然保证,现在引入命令级 epoch 校验是为不存在的写者付复杂度。

---

## 8. 错误分类学

### 8.1 错误对象(result.error 与 ack.error 共用)

```json
{
  "code": "CTX_LOST_DURING_EXEC",
  "message": "会话页面在执行中途发生导航,content script 被销毁",
  "retryable": "afterRecovery",
  "sideEffect": "possible",
  "data": { "phase": "afterSendClick" },
  "evidence": [ { "type": "screenshot", "blob": "sha256:..." } ]
}
```

| 字段 | 类型 | 语义 |
|---|---|---|
| code | string 枚举(契约) | 稳定错误码,双端 codegen 常量。 |
| message | string | 人类可读,禁止程序分支依赖(反模式 §14-7 变体:自由文本不是契约)。 |
| retryable | `yes` / `no` / `afterRecovery` / `manualOnly` | 可重试性**建议**。yes=可立即按矩阵重发;afterRecovery=等相应资源恢复(如 context ready)后可重发;no=重发无意义(参数错等);manualOnly=只许人工裁决。脑的矩阵是最终裁决者,手只能收窄(把 yes 说成 no),不能放宽。 |
| sideEffect | `none` / `possible` / `confirmed` | **副作用三值标注,本协议对"重复极贵"场景的核心表达。** none=保证外部零副作用(失败发生在提交点前);possible=无法排除(死在窗口内、页面状态不可判);confirmed=副作用已确认发生(后置条件已观察到,但整体仍算失败,例如发送成功但后续验证步骤失败)。宪法要求的 sideEffectPossible 语义被强化为三值:possible 与 confirmed 的处置不同(possible 转人工核验;confirmed 记"已发出",只人工处理残余步骤)。 |
| data | object | 结构化细节,schema 随 code 入契约。 |
| evidence | array | 同 result.evidence,对脑不透明。 |

硬规则:**effectful-external 的失败 result,sideEffect 字段必填且手必须诚实标注;拿不准一律 possible。** 自动逻辑对 possible/confirmed 的唯一合法反应是停止与上报(A4)。

### 8.2 错误码表(v1 全集,契约单一源头)

| code | 谁产生 | retryable 默认 | sideEffect 可能值 | 说明 |
|---|---|---|---|---|
| PROTO_MALFORMED | 双方 | no | none | JSON/schema 不合法 |
| PROTO_UNSUPPORTED_KIND | 双方 | no | none | 未知 kind |
| PROTO_UNSUPPORTED_CMD | 手 | no | none | 原语不在会话 caps |
| PROTO_BAD_ARGS | 手 | no | none | args 不合原语 schema |
| PROTO_MSG_TOO_LARGE | 双方 | no | none | 超 maxMsgBytes |
| STALE_SESSION | 手 | yes(新会话重投) | none | 命令带旧会话号 |
| QUEUE_FULL | 手 | yes(退避后) | none | 背压 |
| EXPIRED_BEFORE_EXEC | 手 | no(铸新意图属业务) | none | 出队/执行前 deadline 已过(result.status=expired) |
| GUARD_FAILED | 手 | manualOnly | none | 前置断言不成立(如已有我方消息) |
| CTX_NOT_READY | 手 | afterRecovery | none | 平台页面不在/未登录 |
| CTX_LOST_DURING_EXEC | 手 | afterRecovery(R/NL);manualOnly(SX) | none/possible | 执行中页面导航/关闭 |
| TARGET_NOT_FOUND | 手 | no | none | candidateRef/threadRef 在页面上找不到对应实体 |
| ELEMENT_UNRESOLVED | 手 | manualOnly | none/possible | 手无法在页面定位完成动作所需元素(疑似平台改版;selector 细节只进 evidence) |
| PLATFORM_LIMIT | 手 | manualOnly | none/possible | 平台风控/频控迹象(弹验证码、发送被拦) |
| POSTCONDITION_UNCONFIRMED | 手 | manualOnly | possible | 动作已做但后置条件在预算内未观察到 |
| EXEC_TIMEOUT_HAND | 手 | R/NL:yes;SX:manualOnly | none/possible | 手侧执行预算耗尽自行中止 |
| CANCELED_BY_BRAIN | 手 | — | none | 取消生效(result.status=canceled) |
| INTERNAL_HAND | 手 | manualOnly | possible | 手内部异常,状态不可判 |

（脑侧内部错误不进协议,脑自己记账。）表列的 retryable/sideEffect 是契约默认值,手在具体一次失败中只能向更保守方向偏离。

---

## 9. 版本与能力协商

- **proto(信封 int)**:唯一的破坏性版本开关。手 hello 报 protoSupported 列表,脑选双方都会说的最高版本,选不出则 bye(PROTO_INCOMPATIBLE)。主版本内一切变更必须是加法(新 kind 对上报方向可忽略、新可选字段 must-ignore、新错误码收编进 manualOnly 兜底)。
- **原语粒度版本(caps)**:能力字符串 `name@ver`。args/result/evidence schema 的不兼容变化 = 原语版本 +1;脑派发前查手的会话 caps,没有能力的命令不派发而是把意图挂起并在 UI 呈现(能力缺口是运营事件,不是错误循环)。这解决双端灰度不同步:脑 0.4 可以同时伺候 caps 里是 v1 和 v2 原语的两只手。对标 MQTT 5 的功能协商(broker 能力回传)与 CDP 的 domain 版本思路,但按原语细粒度化。
- **contractHash**:构建期把契约文件哈希嵌入两端;welcome 回告 contractMatch。不一致只告警上报(哈希对加法变更也会变,做硬门禁会把无害灰度变成瘫痪);真正门禁是 caps 逐原语版本。
- **能力会话不可变**:program 热更(交付机制虽悬置,协议先立规矩)后手必须断线重连、以新 hello 重报 caps。消灭"会话中途能力漂移"这一整类状态错乱,代价是 localhost 一次重连(毫秒级)。
- codegen 流程:`contract/protocol.yaml`(信封、kind schema、原语表、错误码表、默认矩阵)→ 生成 Go(类型+校验+状态机常量)与 TS(类型+常量+校验)。CI 校验:两端生成物哈希一致才可合并;禁止手写协议类型(宪法 A5)。

---

## 10. 健康判定

三个正交问题,三个信号源,禁止互相推断(反模式 §14-6):

| 问题 | 信号 | 判定 |
|---|---|---|
| 链路通不通 | WS 连接 + ping 到达 | 超 graceMs(50s = 2.5 × interval)无 ping:脑主动关闭连接,状态 DISCONNECTED |
| 手是否有行为能力 | ack/result/report 的及时性 | 连接在、ping 在,但连续 N(=3)条命令 ack 超时 → HAND_WEDGED(连接在但手残废):脑主动断开该连接,逼手走重连自愈;仍 wedged 则告警人工 |
| 目标页面在不在 | ping.contexts[].ready | ready=false → 该 context 状态 CTX_MISSING:R 类探测照发,SX 不派发(挂起等 ready),不算手的故障 |

派生状态机(脑侧手注册表,按 handId):

```
CONNECTED+ping 新鲜+目标 context ready      -> READY        (全量派发)
CONNECTED+ping 新鲜+context not ready       -> DEGRADED     (只派 probe/collect 于其余 context)
CONNECTED+ping 断供 > graceMs               -> 视同断线,关闭 socket
断线 < graceMs                              -> SUSPECT      (不派新 SX,在途按 §7.2)
断线 >= graceMs                             -> DOWN         (全部在途走悬空处置)
```

心跳设计依据:间隔 20s 是 Chrome 官方推荐的 WS 保活节奏(< 30s SW 空闲阈值,收发双向都重置计时器);由手发起,因为保活收益在手侧、半开检测也必须在拨号方。心跳内容刻意轻(上下文就绪度+队列深度,< 1KB),**心跳不携带业务数据**——心跳一旦变胖,它的按时到达就不再纯粹反映健康(反模式 §14-14)。graceMs=2.5 倍间隔沿用 MQTT keepalive 的 1.5 倍惯例并为 SW 冷启动多留一拍。执行长命令期间 progress 兼任活性信号,避免"忙到没空发 ping 被误杀"——ping 与 progress 任一到达都刷新脑侧 lastSeen。

---

## 11. 大载荷通道(blob)

**结论:简历 HTML、截图等大载荷不走 WS 命令通道,走同一 Go 进程暴露的 localhost HTTP 内容寻址端点;WS 消息只携带引用。**

```
PUT   {blob.endpoint}          Header: Authorization: Bearer {blob.token}
                               Header: X-Content-Sha256: <hex>
                               Body: 原始字节
  -> 200 { "ref": "sha256:<hex>", "size": 183204, "dedup": true }
GET   {blob.endpoint}/{ref}    (脑→手方向的大载荷同理,罕见)
```

规则:单件上限 20MB;ref 只在 WS 消息(result.data、evidence、event.data)里出现;blob 通道**零协议语义**——纯内容寻址字节,无业务字段、无状态推进,上传成功与否只影响发送方是否能在 WS 消息里放这个 ref(上传失败则该证据降级为文字描述,不阻塞 result);脑侧对 30 天无引用的 blob 做 GC。token 会话作用域,welcome 下发。

为什么不走同一条 WS:

1. **控制面延迟**:WS 单连接内一个 5MB 帧会队头阻塞后面的 ping/ack,心跳抖动直接污染健康判定(§10 的信号纯度)。
2. **SW 内存与编码税**:JSON 内联要 base64(+33%)且在 SW 里多一次全量拷贝;HTTP 流式上传则不必。且 fetch 响应 30s 强杀规则下,localhost 大文件上传毫秒级完成,风险可控。
3. **传输无关性(A3)**:MQTT 部署普遍限制报文大小(AWS IoT 128KB),"小控制报文 + 内容寻址引用"的形状迁移时不破。这是企业集成模式里的 Claim Check(行李寄存票)模式。
4. **内容寻址免幂等设计**:同内容重传天然去重,ref 即校验和,证据链免费获得防篡改锚点。

与"多通道语义漂移"教训的关系,正面回答:旧系统的病是三条**语义**通道(同一业务状态从三处推进);blob 通道不承载任何语义、不推进任何状态、双端没有第二套业务字段表,它之于 WS 如同附件之于信——不构成第二语义通道。命令、回执、状态推进 100% 只在 WS 一条通道上(A6 完好)。

---

## 12. 感知通道形态(为另一路设计预留)

协议为感知提供的机制(策略不在此裁决):

- **event 至少一次 + 脑 ack + msgId 去重**:感知报告绝不静默丢失,但允许迟到(分钟级 SLA)。
- **dedupKey + observedAt**:脑侧合并钩子;同 dedupKey 只保最新 observedAt,天然支持快照语义。
- **建议快照优先(level-triggered)**:报"当前未读全景"而非"新来了一条"。快照幂等、丢失自愈、与"事件是提示、readonly 对账是真相"的宪法立场同构(对标 Kubernetes 调和循环)。
- **事件触发的一切后续动作都是脑下发的命令**(禁令 3):协议里不存在"手因事件自主做了什么"的表达空间——event 的 body 里没有任何"我已顺手采集"字段,这是刻意的。
- 感知的采样节律若需要定时,由脑以 readonly 命令轮询驱动,或(若另一路选择页面内监听)由页面事件自然驱动;手不得为感知设业务定时器(禁令 1)。

---

## 13. 传输绑定

### 13.1 WebSocket 绑定(v1 现行)

- 端点 `ws://127.0.0.1:{port}/v1/channel`,端口可配置、新定(明确不复用 17321),默认建议 17872;手侧存端口于 chrome.storage(基础设施数据)。
- 文本帧,UTF-8,一帧一消息;不启用分帧拼接;permessage-deflate 可选。
- 鉴权:hello.auth 配对令牌 + Origin 头白名单(chrome-extension://<id>)。localhost 明文 ws 可接受(同机、有令牌;wss 自签证书在本场景零收益高摩擦)。
- 关闭码只作日志;**任何语义不依赖关闭码**(bye 才是语义层告别)。
- 脑对 WS 的唯一信任:帧完整性与单连接内有序;且这两点也仅作性能优化的依据(少查一次去重表),不作正确性依据。

### 13.2 换 MQTT 5 的思想实验(A3 的验收)

| 本协议机制 | MQTT 下的映射 | 是否需改语义 |
|---|---|---|
| session 字段显式化 | 不信 broker session,仍用 hello/welcome 建会话 | 否(这正是当初不用"连接即会话"的原因) |
| ack 应用层回执 | 照发;PUBACK 只当传输噪音 | 否 |
| msgId 去重 | QoS1 重投被去重层吸收 | 否 |
| 命令 STALE_SESSION 围栏 | broker 跨会话吐旧命令被拒 | 否 |
| deadline | 叠加 message expiry interval 做传输级加速,语义不变 | 否 |
| blob 引用 | 128KB 报文限制无压力 | 否 |
| retained 消息 | 一律禁用(命令绝不 retain) | 部署纪律 |

结论:语义零修改。设计期做这个思想实验,正是为了防止"押在 WS 上"的隐性假设溜进来。

---

## 14. 反模式清单(明令禁止)

1. **两端各自手写协议字段/别名**(旧系统 26% 事故的根)。唯一合法来源是 contract codegen;code review 见到手写字段名即打回。
2. **回执不带 ref**。生成类型里 ref 非可空;没有 ref 的回执无法构造。
3. **DOM/selector/tabId/windowId/平台 id 进入语义字段**。只许进 evidence 与日志;脑逻辑读 evidence 内容做分支同罪(A7)。
4. **用时间戳做去重、排序或状态裁决**。时钟不是逻辑时钟;去重只认 msgId/idemKey,顺序只认"脑等到 result 再发下一步"。
5. **手自主重试 effectful 动作**(宪法禁令 4)。失败只有一种诚实姿势:result + sideEffect 三值。
6. **把"WS 连着"当"手健康"、把"心跳在"当"页面在"**。三个问题三个信号(§10)。
7. **自由文本承载语义**:程序分支依赖 error.message、progress.stage 者,同手写字段名罪。
8. **第二条命令通道**。测试页、调度器、未来的运营工具,全部经脑走同一 cmd 通道;任何"顺手加个 HTTP 接口直接叫手做事"的提议直接否决(A6)。blob 通道无语义,不在此列(§11 论证)。
9. **追求协议级恰好一次(QoS2 迷信)**。两将军定理面前,预算应花在幂等与对账上(§5.2)。
10. **大载荷内联 base64 进 WS**。走 blob 引用(§11)。
11. **过程式命令**("点第 3 个会话"“再往下滚一屏")。命令必须声明式(目标态/目标实体),这是 NL 类幂等性的构造前提,也是平台无关的自然结果。
12. **手侧批量聚合/合并命令、自行排程**。一 cmd 一 ack 一 result;节奏权 100% 在脑(禁令 1、3)。
13. **无版本报文**。proto 字段必填,收到无 proto 的报文按 PROTO_MALFORMED 拒绝。
14. **心跳携带业务载荷**。心跳变胖 = 健康信号失真(§10)。
15. **静默去重**。重复到达必须重新 ack(+必要时重放 result),否则对端合理地永远重传(§3.4)。
16. **把 chrome.storage 当账本**。手侧持久数据只有证词地位;任何"以手侧记录为准"的对账方向都是颠倒的——账本只在脑,冲突时以脑为准、疑义转人工(A1)。
17. **fire-and-forget 下发 effectful 命令**(不等 ack、不追 result)。SX 命令没有"发出去就不管"的合法状态;每条 SX 命令的账本行必须走到终局或 ambiguous 人工队列。

---

## 15. 业内对标索引

| 机制 | 借鉴来源 | 取 | 弃 |
|---|---|---|---|
| 持久任务 + 设备回报推进状态 | AWS IoT Jobs | 脑侧账本状态机、离线不丢、DescribeJobExecution 式对账(query/report) | 云端多设备扇出模型(单脑少手用不上) |
| 命令 TTL | Azure IoT Hub C2D / MQTT 5 message expiry | deadline 字段、过期自灭 | Azure 的 feedback 队列(我们的 ack/result 已覆盖) |
| keepalive 与会话 | MQTT 5 | 心跳倍数判死、同 ClientID 接管、能力协商思想、reason code 结构化 | QoS2(§5.2)、broker session 状态(会话自己管,A3) |
| 极小信封 + id 关联 + 错误对象 | JSON-RPC 2.0 | 形 | 其无生命周期/无投递语义/error 三字段太薄(扩成 §8 六字段) |
| 应用层分层 ack | Socket.IO ack | L1 收妥回执的必要性 | 传输库回调实现(显式消息化,传输无关) |
| 幂等键 + 结果重放 | Stripe idempotency key | 客户端(脑)生成、执行端存首结果重放、键随意图而非随请求 | 随机键(改为业务行确定性派生,§5.4) |
| 超时重投 | SQS visibility timeout | R/NL 类"执行超时回到可派发" | 对 SX 类的自动重投(违背 A4) |
| 僵尸围栏 | Kafka epoch/fencing | 会话接管、epoch 钩子(welcome.brain.epoch) | 命令级 epoch 校验(单写者环境暂不付此复杂度,§7.4) |
| 命令/事件命名与域划分 | Chrome DevTools Protocol | `domain.verbObject` 命名、progress 事件流 | 其可靠性假设(同进程管道无重连语义,不可照搬) |
| 按类别重试策略 + 执行心跳 + 歧义转人工 | Temporal activity | RetryPolicy 按原语覆写、progressGap、"at-least-once + 业务幂等"哲学、ambiguous 即人工 | 其重量级历史回放机制 |
| 大载荷旁路 | 企业集成模式 Claim Check | 内容寻址引用 | — |
| 快照式感知 | Kubernetes level-triggered reconcile | 事件是提示、对账是真相、快照幂等 | — |
| 分层确认的理论根 | end-to-end argument(Saltzer) | L0-L3 各层可推出边界的划分纪律 | — |

---

## 16. 待定问题(移交对撞与实测)

1. **20s 心跳在真实环境的保活可靠性**:Chrome 116+ 文档与官方博客支持"WS 收发重置空闲计时器",但节能模式、休眠唤醒、企业策略下的实测数据缺失;若实测不稳,备选方案是心跳不变+接受 SW 死亡靠 alarms 复活重连(协议无需改动,这本身就是设计的容错路径)。
2. **journal 写盘的原子性与时延**:chrome.storage.local 写入是异步的,(a) 点"落盘后才动手"会给每条 SX 命令加一次存储往返(实测约几 ms,可接受);但 storage 写入与 SW 强杀的交错是否存在"回调已跑、盘上未持久"的窗口,需要用崩溃注入实测确认。若存在,残余窗口的后果仍是"少发+人工"(§4.2 论证的方向性保证不破),但概率数字要写进运营预期。
3. **guards 的词汇表边界**:noPriorMessageFromUs 这类断言的判定成本与可判定性依平台页面而异;哪些 guard 进 v1 契约、哪些留给脑侧 L3 对账,需要与感知设计一路合并裁决。
4. **deadline 依赖同机时钟**:脑手同机是当前事实;若将来手跑在远程浏览器(云浏览器方案),deadline 需改为"脑下发相对 TTL + 手收到时刻起算"并重审 §5.3。
5. **多账号并发的串行度**:v1 手全局串行;若业务要求同浏览器多平台账号并行操作,需引入 lane 概念,属主版本演进(信封不动,cmd.body 加字段)。

---

## 17. 附录:三条完整报文流水

### 17.1 打招呼(happy path)

```
B→H  cmd     msgId=M1  chat.sendGreeting  idemKey=IK1  (§3.3 示例原文)
H→B  ack     ref=M1    accepted                        [L1:入队]
H→B  progress ref=M1   stage=threadOpened              [活性]
     (手写 journal[IK1]=attempting → 点发送 → 观察到消息气泡 → journal[IK1]=committed+result)
H→B  result  msgId=M2  ref=M1  ok  data+evidence       [L2:后置条件已观察]
B→H  ack     ref=M2    accepted                        [手删 outbox[M2],journal 留待 TTL]
     (脑账本:queued→sent→accepted→ok;稍后业务巡检以 readonly 对账做 L3)
```

### 17.2 SW 死于执行窗口(歧义转人工)

```
B→H  cmd     msgId=M3  chat.sendMessage  idemKey=IK2
H→B  ack     ref=M3    accepted
     (journal[IK2]=attempting → SW 在点击发送前后被杀;WS 断)
脑:  连接断,M3 标 suspect;graceMs 后手仍未归 → SUSPECT→DOWN
     (40 秒后 alarms 看门狗复活 SW,重连)
H→B  hello   (journalOpen=1, outboxPending=0)
B→H  welcome (新 session S2)
B→H  query   ref=M3                                    [阶段2对账]
H→B  report  ref=M3  state=attempting  journal={IK2,...}
脑:  M3 → ambiguous(终局);人工队列生成工单:含账本行、journal 证词、
     下发一条 collect.threadState(readonly)取回会话流截图辅助人工判断。
     人工裁决"未发出"→ 由人铸新意图(新 idemKey)或复用 IK2 重发,自动逻辑不代劳。
```

### 17.3 脑升级重启期间,手攒了未送出的终局

```
     (脑停机 90s 升级;手执行中的 collect.candidateList msgId=M5 完成)
手:  result msgId=M6 ref=M5 入 outbox;WS 断,发送失败,M6 留存
     (手重连退避:1s,2s,4s... 期间 SW 若死,alarms 复活继续)
H→B  hello   (outboxPending=1)
B→H  welcome (新 session S3, brain.epoch=43)
H→B  result  msgId=M6 ref=M5 (session 字段仍是旧 S2,attempt=4)   [阶段1补投]
B→H  ack     ref=M6 accepted        [脑重启前已记账 M5=accepted(先记账后发送),
                                     现终局化为 ok;processed 表记 M6 防重]
     (若 M6 重复到达:ack duplicate,不再记账 —— 静默去重禁令)
```

---

## 18. 参数速查(v1 默认)

| 参数 | 值 | 出处 |
|---|---|---|
| 心跳间隔 / 判死宽限 | 20s / 50s | §6、§10 |
| ackTimeout / 重传 | 3s,共 3 次(3s/6s) | §6 |
| execBudget R/NL/SX | 30s / 20s / 60s(上限 240s) | §6 |
| progressGap R/SX | 15s / 20s | §6 |
| deadline R/NL/SX | 5min / 2min / 10min | §6 |
| 自动重发上限 R/NL/SX | 2 / 2 / 1(且须零副作用证明) | §6 |
| 队列深度 | 16 | §3.3 |
| maxMsgBytes / inlineBytes / blob 上限 | 128KB / 32KB / 20MB | §2、§11 |
| journal TTL / outbox TTL / 容量 | 14d / 7d / 各 512 条 | §5.5 |
| processed 去重表保留 | 30d | §7.3 |
| 重连退避 | 1s×2^n 封顶 30s,±30% 抖动;alarms 看门狗 60s | §7.1 |

以上参数唯一权威副本在 contract 默认值表;welcome 可下发覆盖(手侧硬编码的只有重连退避与看门狗——连不上脑时唯一需要自主的两个数)。
