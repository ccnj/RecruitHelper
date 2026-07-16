# 脑手通信协议草案对抗审查(红队)与 v1 最小实现裁剪

> 2026-07-16。审查对象:`../AutoZhilian/脑手通信协议设计草案.md` v0.1(MQTT 5 语义面 + v1 localhost WS 承载)。
> 攻击面:MV3 现实(SW 空闲约 30 秒被杀、活跃 WS 收发可延寿 Chrome 116+、chrome.alarms 最小约 30 秒、chrome.storage 跨死亡存活、手随时失忆)、WS-only 形态、旧事故账本(协议破损占 26% 事故;已知问题 2/3/9/16/23 等)、单脑单手 localhost 的真实规模、业务纪律"宁可少发"与分钟级巡检节奏。
> 判决词表:采纳 = 原样进 v1;简化 = v1 用降级形态并写明升级触发条件;修改 = 草案该处有错或有洞,按本文改;删除 = 不进契约(至少 v1 线上形态不进)。

总体结论先行:草案的语义骨架(命令二分类、三层确认、成功=观察到后置条件、幂等键从业务意图派生、脑单权威时钟)全部站得住,是对旧事故账本的正确回应。真正的问题集中在四处:一,若干机制是按"消息与连接解耦的 broker 世界"设计的,搬到"单脑单手、一条 localhost TCP"上变成无意义甚至有毒的动作(同 id 重发、消息级 epoch 全量围栏、chan 字段);二,§10 映射表回避了"脑就是 broker,脑重启即 broker 失忆"这一行,手侧内存队列在脑重启窗口内必然全灭,必须用脑侧派发前写库(write-ahead)补;三,§8 握手是真空且 token 一句"沿用 plugin_token.json"直接违宪;四,v1 没有验证原语时"结果未知"的降级行为草案没写,必须显式立法为 suspect 终态 + 冻结幂等键 + 转人工。

---

## 第一部分:逐机制判决表

### A. 信封字段

| # | 机制 | 判决 | 理由(攻击结论) | 升级触发条件 |
|---|---|---|---|---|
| A1 | `v` 协议版本 | 采纳 | 插件与脑的发版通道天然不同步(插件走商店/CRX/unpacked,脑走客户端更新),版本歪斜是真实场景;一个 int 的保险,不删 | — |
| A2 | `id` 全局唯一 | 采纳(生成器放宽) | 关联(`re`)与去重的地基,旧问题 3"回执丢 taskId 任务永久 pending"的直接解药。ULID 不强制:SW 里 `crypto.randomUUID()` 零依赖,接收方一律按不透明串处理,排序需求用 ts+日志满足 | — |
| A3 | `kind` | 采纳 | 分发依据,无可攻击 | — |
| A4 | `ts` 毫秒时钟 | 采纳 | 同机部署,ts 审计价值高、成本零;反模式 4 已禁止拿它做去重/判定,保持 | — |
| A5 | `epoch` 世代号 | 简化 | 单脑单手 localhost 下,WS 连接本身就是世代边界:旧世代的消息只可能来自旧连接,脑按"只认当前连接"即可围栏。最小形态见 1.1 详述;换代收编改 keyed bootId | 传输层解耦连接(MQTT/broker)时,恢复消息级全量围栏语义 |
| A6 | `chan` 字段(§10 第 1 行) | 删除 | `kind` 已完全决定路由(cmd/ack/progress/result/event/hb/hello/hello_ack),`chan` 是 MQTT 主题残影;双字段 = 两处可以对不上 = 旧系统"字段名不一致"事故的新孵化器 | MQTT 时由 topic 承担路由,仍然不需要 chan |
| A7 | `accountId` 进 cmd 信封 | 删除(线上字段) | 账号互斥由脑的调度层裁决(草案原则 7 自己说的),手不消费该字段;放进线上信封是纯审计冗余,留在脑侧派发表即可 | 一手多账号、或互斥需要手侧感知时再进契约 |

### B. 确认、幂等与重试

| # | 机制 | 判决 | 理由(攻击结论) | 升级触发条件 |
|---|---|---|---|---|
| B1 | 三层确认模型 L1/L2/L3 | 采纳 | WS 下依然成立且更干净:L1 在 WS 上退化为"写调用没报错"(连 PUBACK 都没有,字节可能只进了内核缓冲),而模型本来就规定 L1 不作业务判据,语义零损失迁移 | — |
| B2 | ack(L2)机制 | 简化 | 手侧恒发(分发器统一实现,约 15 行);脑侧 v1 不建 ackTimeout/leaseTimeout 双定时器,每命令单一 deadline;ack 只落日志与状态页观测。里程碑 1 全是亚秒级命令,双定时器机制是为分钟级命令准备的 | 首个 typicalDurationMs > 15 秒的原语(增量对账/批量采集)上线时,启用双定时器 |
| B3 | ackTimeout 内同 id 重发 | 修改 | 见 1.2 详述。同一条 localhost TCP 连接上重发帧无意义且有毒;正确规则:ackTimeout 的动作是"判连接可疑,关闭重建";帧重发只发生在新连接建立之后,且仅当 bootId 未变;bootId 变了,readonly 重新派发、effectful 一律进未知态 | 规则本身即终态,不随传输升级改变 |
| B4 | 租约 leaseMs + progress 续租 | 简化 | 里程碑 1 与 v1 早期没有任何分钟级命令(ping/切标签页亚秒;将来 openConversation/采集单会话也在秒级)。v1:cmd 带单一 timeoutMs,无续租;progress 的 kind 保留为纯观测(note 直落脑日志,兼作活性触碰),无租约语义 | 同 B2:首个分钟级原语(readConversationList 增量对账)上线 |
| B5 | expiresAt 执行前必查 | 采纳 | 一次整数比较的成本。同机同钟,这个检查在 localhost 上反而比分布式更可靠(草案担心的 broker 时钟问题不存在);防的是脑侧派发 bug 与重连窗口里的陈旧命令 | — |
| B6 | 幂等键构造(业务意图派生) | 修改(补强) | 构造规则正确(Stripe 式,从状态机持久状态派生)。但草案把去重重心放在手侧内存表,第一道防线必须显式挪到脑:SQLite 持久幂等台账(idemKey -> 状态),同键 in-flight/suspect 期间脑拒绝签发新 cmd。"跨 epoch 幂等由脑兜底"只有在脑真有台账时才成立,否则是一句口号 | — |
| B7 | 手侧去重记忆(内存 1000 条) | 简化 | 容量层面 1000 条对单手是笑话级过剩(在途 ≤ 个位数),256 LRU 足够,但这不是重点。重点是明确其唯一使命:防同 incarnation 内的重复投递——而这恰好是同 id 重发唯一安全的窗口(B3),两者自洽。条目要缓存终态 result,命中重复投递时重放 result 而非静默忽略。严禁把去重表写 chrome.storage(见第二部分禁令 2 分析) | 容量是参数,无升级问题 |
| B8 | "跨 epoch 幂等由脑的验证流程兜底"的 v1 降级 | 简化(降级显式化) | 草案预设有 readTimeline 类验证原语,v1 没有。必须写死降级行为,见 1.5 详述:结果未知 = suspect 终态,冻结幂等键,通知人工,永不自动重试。并立法:今后任何业务 effectful 原语必须与其配对验证 readonly 原语同批交付,缺验证原语的 effectful 不准上线 | 首个业务 effectful 原语(发消息/打招呼)上线时,实现验证闭环(readonly 验证、轮次封顶 3) |
| B9 | 超时重试矩阵(§6 全表) | 简化 | v1 裁成三行:(1) readonly 超时,重建连接后重试至多 2 次,仍败则手标 suspect;(2) effectful 无 result、或 result 带 sideEffectPossible=true,一律 suspect 转人工;(3) result failed 且 retriable=true 且 sideEffectPossible=false,允许同 idemKey 重发一次。其余行(验证流程、旧 epoch result 的 evidence 采信)随验证原语回归 | 验证原语上线后恢复完整矩阵 |
| B10 | "转人工必须通知,禁止静默挂起" | 采纳(通道适配) | 问题 23(双端降噪叠加成 78 条招呼静默死锁)的直接教训,一个字不能松。v1 无云端无企微:通知 = client UI 显著告警面板 + 结构化日志,suspect 队列在状态页一眼可见 | 云端对接后追加企微通道 |

### C. 健康、心跳与保活

| # | 机制 | 判决 | 理由(攻击结论) | 升级触发条件 |
|---|---|---|---|---|
| C1 | 心跳 20-25 秒 | 采纳 | 核实成立,见 1.4 详述:Chrome 116+ 活跃 WS 收发重置 SW 的 30 秒空闲计时,心跳要独立承担保活就必须 < 30 秒;20-25 秒加抖动是正确区间。心跳定时器属基础设施定时器,在禁令 1 的豁免区内 | — |
| C2 | chrome.alarms 兜底重连 | 采纳(定位钉死) | alarms 最小约 30 秒,做不了心跳,只做"SW 死透后的复活闹钟"。SW 死亡-复活循环是设计内常态:脑不在时手连不上,SW 约 30 秒内也死,每个 alarm 周期短暂醒来试连一次,失败即再死——廉价且正确。复活最坏约一个 alarm 周期 + 连接时间,分钟级业务可容 | — |
| C3 | 健康三级判定 | 简化 | v1 两级:连接级(WS close = 即时暂停派发,静默处理不告警——SW 正常死亡也走这条,告警会刷屏)+ 进程级(连接开着但 hb 静默 >= 2 周期约 45-50 秒 = 真异常:SW 事件循环卡死或心跳代码坏了,告警)。能力级(contentScriptOk/探活/自愈刷新原语)推迟——里程碑 1 的命令全在 SW 层,没有内容脚本参与 | 首个内容脚本参与的原语上线时启用能力级 + 自愈刷新原语 |
| C4 | LWT 映射 = WS close + 心跳缺失 | 采纳(非自欺) | LWT 的本质是"broker 代替你观察对端死亡";WS 下脑自己就是对端,直接观察严格优于代理观察。localhost 上进程死亡(含 SIGKILL、SW 被杀)内核必然关闭套接字、脑立刻看到 close——分布式里"对端悄然消失"的经典难题在 loopback 上不存在。反模式 3(禁用 LWT 当健康信号)已把剩余的坑(连接在不等于手可用)填了 | — |
| C5 | retained status 映射 = 脑内存手状态表 | 采纳 | retained 的语义是"新订阅者能拿到最后状态";脑是唯一消费者,内存状态表即等价物 | — |
| C6 | 手侧重连退避(1s 指数退避,封顶 30s,加抖动) | 采纳(补一条) | 标准做法。补:hello 被拒(token 失效/被吊销)时必须停止退避、转"待配对"态并在扩展 UI 提示,不许无限重试刷脑侧日志 | — |

### D. §10 WS 映射表逐行核查与补缺

| # | 机制 | 判决 | 理由(攻击结论) | 升级触发条件 |
|---|---|---|---|---|
| D1 | broker 离线排队 -> 手侧内存有界队列 | 修改 | 队列本身保留(有界、result 优先、刷新即丢)。但映射表回避了致命一行:脑重启/升级瞬间的命运。见 1.3 详述——手侧队列在脑宕机窗口内必然全灭,v1 必须补脑侧派发前写库(write-ahead 在途日志),否则脑重启后连"我曾经问过什么"都不知道,复刻旧系统 26% 协议破损事故里最毒的"静默丢失"形态 | MQTT 时 broker 排队"白捡回来",但 write-ahead 仍保留(broker 一样会丢:session expiry、expiry interval) |
| D2 | QoS1+PUBACK -> 应用层原 id 重发 | 修改 | 等价性成立,但重发时机按 B3 改写(同连接不重发,重建连接后按 bootId 决定) | — |
| D3 | Message Expiry -> 手侧 expiresAt 检查 | 采纳 | 本就是第二道防线,唯一防线化后在同机时钟下反而更可靠(B5) | — |
| D4 | 映射表缺行:连接认证 | 修改(新增行) | MQTT 的 username/password + broker ACL 在 WS 上的等价物是 Origin 校验 + 配对 token,草案完全没写(§8 真空的一部分),见 1.6 配对设计 | — |
| D5 | 映射表缺行:CONNACK 拒绝语义 | 修改(新增行) | hello_ack 在草案里没有拒绝形态。补:`hello_ack{accepted:false, reason: bad_token \| version_incompatible \| superseded}` 后关闭连接;这是配对、单活连接、版本歪斜三件事的共用出口 | — |
| D6 | 映射表缺行:会话(clean_start=false / session expiry 1h) | 简化(明示无等价物) | §3 的持久会话文本在 WS 下没有承载物:"会话"= 脑的在途表 + 手的 bootId,如此而已。显式作废,防止有人照 §3 去实现一个一小时的假会话层 | MQTT 时恢复 |
| D7 | env / machineId 主题命名空间 | 删除(v1) | 单机单租户,环境隔离用端口 + 配置文件;进信封是纯噪声,还会诱导测试环境分支(违宪风险) | 云端 broker / 多租户时恢复为主题前缀 |
| D8 | 数据面 blob HTTP 通道 | 简化(推迟) | 里程碑 1 无大载荷。v1 只做一件事:定线上帧上限(建议 1MB,超限 result 直接报错),把反模式 6"大载荷上总线"从纪律变成机械约束 | 截图/简历 HTML 证据类原语上线时建 blob 通道 |

### E. 握手与配对(§8 真空填补)

| # | 机制 | 判决 | 理由(攻击结论) | 升级触发条件 |
|---|---|---|---|---|
| E1 | token"沿用 plugin_token.json 机制" | 删除(违宪) | 宪法明文:不背旧系统任何契约,plugin_token.json 点名在列。且旧机制依赖 Native Messaging host 或安装器落文件,新形态下根本没有这条腿。替代设计见 1.6 | — |
| E2 | 端口发现 | 修改(新增设计) | 固定默认新端口 + 双端可配置,是唯一可行解。动态端口 + 发现机制全部被否:扩展读不了本地文件、做不了 mDNS、扫端口丑陋且慢,而"发现端点"自身又需要一个固定端口——无穷回归。详见 1.6 | — |
| E3 | token 获取 UX | 修改(新增设计) | 推荐:客户端 UI"配对模式"(用户点配对,60 秒窗口内接受无 token 的 hello 为待配对,UI 确认后脑签发 handId+token,经该连接下发,手写入 chrome.storage.local);兜底:扩展 options 页手工粘贴。安装器写 chrome.storage 做不到(Chrome 无此机制,managed storage 是企业策略这条路不走);二维码/一次性码是跨设备手段,同机无意义。详见 1.6 | 首次给真实客户安装前,补配对管理 UI(吊销/重命名/多手列表) |
| E4 | handId 分配与多 profile 持久 | 修改(新增设计) | 脑在配对时签发 handId(单一权威,与 epoch 同哲学,顺便得到 hand-01/hand-02 可读日志名);手持久化于 chrome.storage.local——它天然按 profile 隔离,每个浏览器 profile 自动成为一只独立的手,无需任何额外设计。扩展重装 = 存储清空 = 重新配对,脑侧旧手记录由 UI 清理。v1 单活策略:同一 handId 新 hello 顶掉旧连接(close reason=superseded,响亮记日志);不同 handId 同时在线允许注册,但业务派发只认绑定的那只手 | Android 手 / 真多手并发需求转正时,改多手注册表 + 按手派发策略 |
| E5 | hello_ack.inFlightCmds 问答回合 | 简化 | v1 不需要这个来回:bootId 变了 = 手已失忆,问也白问,脑直接全部收编(readonly 作废待重派,effectful 转 suspect);bootId 未变 = 手记忆连续,脑直接重发未 ack 命令,手侧去重表吸收重复。省一个协议回合和一个双端状态机 | 多手或长命令跨重连恢复变复杂时再评估 |
| E6 | hello_ack.bundleAction 版本协商 | 简化(v1 删) | 交付机制被宪法悬置(截止点 = 首个真实客户安装),v1 脑不指挥升级。hello 报 bundleVersion + capabilities,脑按能力门控派发即可——这正是重写评估 5.5"无连接能力协商"教训的转正形态,而且方向反转后更简单:旧系统是插件驱动、要在插件侧缓存客户端能力;新系统脑驱动,每次 hello 全量收到手的能力表,脑侧内存即缓存,chrome.storage 能力缓存那套复杂度整个消失 | 交付机制拍板后,bundleAction 或其替代物再进契约 |

### F. 原语契约、传感与未决项

| # | 机制 | 判决 | 理由(攻击结论) | 升级触发条件 |
|---|---|---|---|---|
| F1 | §9 原语注册五要素 | 修改(补一条) | 五要素(name/argsSchema/class/preconditions/evidenceSchema)采纳,typicalDurationMs 明确为脑侧 deadline 的数据来源。必须补第六条纪律:原语实现内部禁止自主重试——禁令 4 落进协议文本。旧问题 16 的修复(9a286ac"点击后核验,失败重试一次")移植时必须剥离内部重试:原语 = 单次尝试 + 观察后置条件 + 诚实上报,重试决策归脑。否则脑手两层重试相乘,重复发送风险翻倍 | — |
| F2 | 传感 event(QoS0)+ 初始事件集 | 简化(推迟实现) | kind 与"事件是提示不是账本"语义进契约;v1 不实现任何事件类型——里程碑 1 无传感需求,分钟级巡检也不靠事件驱动。"两次一致才可信"(F1 闪空教训)随首个事件实现落地 | 脑的对账轮设计落地、需要 unreadBadge/manualInteraction 提示时 |
| F3 | 反模式清单(§11 九条) | 采纳(增一条) | 九条全部保留,对 WS 形态字字适用。新增第十条:"未知原语/未知指令默认回成功"为禁止项——旧问题 9(假成功 + 回传裁剪 + 无事件映射三层互掩)的直接立法,分发器默认分支必须是 ack{accepted:false, reason:unsupported} + result{status:unsupported} | — |
| F4 | §13.1 readConversationList 游标/progress 细化 | 简化(推迟) | 依赖真机验证列表排序稳定性,且与租约机制(B4)同批才有意义 | 对账原语里程碑启动时 |
| F5 | §13.2 结果未知态证据匹配规则(textHash+方向+相对位置) | 简化(推迟) | 与验证闭环(B8)同批;v1 的 suspect 降级不依赖它 | 首个业务 effectful 原语上线时,随验证原语一起真机验证 |
| F6 | §13.3 手包版本协商与远程代码去留 | 简化 | v1 只做能力门控(hello.capabilities);交付机制按宪法悬置,协议侧不预埋任何一方的机制 | 与宪法截止点相同:首个真实客户安装前 |
| F7 | §13.4 Android 手 evidence 无障碍树形态 | 删除(v1 范围) | YAGNI。evidenceSchema 按原语声明、平台无关,已是正确的扩展点,Android 立项时自然长出,现在定义纯属想象 | Android 手立项 |

---

## 1.x 关键攻击详述

### 1.1 epoch:对单脑单手 localhost 是过度设计,最小形态这样保留

epoch 是 fencing token,防的是"旧世代的手的消息污染新世代"。这个威胁在 broker 世界真实存在:消息与连接解耦,旧会话排队的消息可以在新世代到达。但在单脑单手 localhost WS 下,消息永远绑在某条 TCP 连接上,旧世代消息只可能从旧连接来,而脑完全知道每条消息来自哪条连接。连接本身就是世代边界。

唯一的残余场景:旧连接半死(脑尚未收到 close),新连接已建立,旧连接内核缓冲里的字节还能送达。对策不需要 epoch 字段——脑维护"当前连接"指针,收到新 hello 即切换并主动关闭旧连接,非当前连接来的任何消息直接丢弃并记审计。这就是全部围栏。

v1 最小保留形态(建议保留而非删除,成本约 10 行):脑侧每手一个持久单调计数(SQLite),每次接受 hello 加一,经 hello_ack 下发;手存内存变量,盖进此后每条消息;脑校验"消息 epoch == 当前连接的 epoch",不匹配即丢弃 + 审计 + 关闭来源连接。它捕获的是手侧代码 bug(重连竞态下旧发送路径没切换),属于廉价断言。

必须修的一处:草案把"手换代(在途命令收编)"挂在 epoch 递增上,而 epoch 每次 hello 都递增——这意味着一次瞬断重连(SW 没死、记忆完好、命令还在执行)也会把在途 effectful 全部打成"结果未知",自造转人工噪声。换代判定必须 keyed bootId(SW 内存变量,SW 死即变,记忆连续性的真实指示器):bootId 未变的重连,在途命令继续等,result 在新连接上到达照常入账(按 re 关联,不按 epoch 拒收);bootId 变了才收编。

### 1.2 同 id 重发:同一条 TCP 连接上无意义,盲重发是双发事故的协议级入口

草案 §6:L2 超时(无 ack)-> 原 id 重发至多 3 次,并论证"effectful 同 id 重发是安全的,手按 id 去重"。逐条攻击:

一,在同一条存活的 localhost TCP 连接上重发帧毫无意义。loopback 不丢包;帧要么已送达,要么连接已经/即将报错。送达了却没 ack,说明手内部有问题(SW 事件循环卡死、分发器 bug),把同一帧再塞进同一个卡死的分发器,只是多一条日志。MQTT 世界里"重发"有意义是因为 broker 转发链路真的会丢 QoS 消息,WS 直连没有这个环节。

二,"同 id 重发安全"的论证依赖手侧去重表,而去重表在内存里、与 SW 同生共死。最危险时序:cmd 送达 -> 手开始执行、副作用已发生(消息已发出)-> SW 在回 result 前被杀 -> 去重表清零 -> 脑盲目同 id 重发 -> 新 SW 不认识这个 id -> 再执行一遍 -> 重复给候选人发消息。这正是业务上最贵的事故,而草案的重发规则为它开了协议级入口。草案矩阵里"手换代时在途 effectful 进结果未知态"其实已经暗含了正确答案,但没有和"原 id 重发"条款打通——两条规则之间的裂缝就是事故窗口。

修改后的完整规则(v1 即终态,不随传输升级改变):

- ackTimeout 到期的动作不是重发帧,是判定连接可疑:主动关闭连接,走重连。
- 帧重发只发生在新连接建立之后,且仅当新 hello 的 bootId 与命令派发时一致(手记忆连续,去重表有效,重复投递会被重放 result 吸收)。
- bootId 变了:readonly 命令重新派发(新 id,无所谓);effectful 命令一律进结果未知流程(v1 即 suspect 转人工),绝不重发。

### 1.3 §10 最大真空:脑重启/升级瞬间,手侧排队 result 的命运

推演完整时序:脑进程退出 -> 手的 WS 立即 close -> 手把未送出的 result 放进内存队列,开始退避重连 -> 连接失败,WS 无活动,SW 约 30 秒内被杀 -> 内存队列全灭。脑重启耗时超过 30 秒(升级、崩溃后人工重启都远超),等脑回来,经 alarm 复活的是一个全新 bootId 的空白 SW。结论:脑的任何一次重启,手侧排队数据必然全部丢失,"手侧内存队列"对脑重启场景的兜底价值恰好为零。§10 那行"由验证流程与对账轮补偿"在 v1(两者都不存在)是空头支票。

丢失清单与可接受性:

- event:可接受。事件是提示不是账本,本就允许丢,对账轮兜底(禁令 3 语义)。
- readonly 命令的 result:可接受。脑重启后按需重派,零损失。
- effectful 命令的 result:不可接受静默丢。这是"命令可能已执行,但没有任何人记得结果"的形态——旧账本里最毒的静默失败族。
- hb:无所谓。

兜底方案(v1 必做,不可推迟):脑在派发任何命令之前,先写 SQLite 在途日志(cmd id、name、class、idemKey、dispatchedAt、状态),终态时更新;脑重启后加载在途日志,未终态的 readonly 直接作废,未终态的 effectful 全部标 suspect + 通知。这样脑重启的代价从"静默丢失"降为"每账号至多一条(在途 effectful 恒 <= 1)需要人工确认的 suspect"——可接受,且响亮。write-ahead 是纯脑侧改动,约几十行,是 v1 里性价比最高的一条纪律。将来升 MQTT,broker 排队能追回一部分 result,但 write-ahead 仍保留:broker 的会话过期与消息过期一样会丢。

顺带钉死一条 v1 行为:脑对离线的手不排队命令。巡检 tick 现算现发,手不在线就本 tick 跳过,下个 tick 重新从状态机推导——命令是状态的投影,投影留在脑侧(重写评估 5.5 的原话),陈旧命令排队是反模式。expiresAt 取短值(建议 <= 2 倍 timeoutMs)配合执行。

### 1.4 心跳与 MV3 保活:20-25 秒核实成立,阈值要容忍 SW 死亡-复活循环

- 心跳必须 < 30 秒吗:是——只要 WS 活动是 SW 唯一的保活来源。Chrome 116+ 活跃 WS 收发(任一方向)重置 30 秒空闲计时;20-25 秒加抖动留出了调度毛刺余量,正确。低于 116 的 Chrome 不在支持基线内(新产品,声明 Chrome >= 116;老版本上系统退化为"30-60 秒一次的连接脉冲",分钟级业务仍勉强可用但噪声大,不承诺)。
- 心跳只能用 SW 内 setTimeout/setInterval 驱动(活着的 SW 里合法,且属禁令 1 豁免的基础设施定时器);chrome.alarms 最小约 30 秒,做不了心跳,只做复活闹钟:每 30-60 秒醒来查"WS 在吗",不在就重连。两者分工不可混。
- 判定阈值必须区分两种"没消息":连接已 close(SW 正常死亡/脑主动关)——即时暂停派发,静默处理,这是设计内常态,不许告警刷屏;连接开着但 hb 静默 >= 45-50 秒——真异常(localhost 上 SW 死亡必伴随 close,连接还开着却没心跳只剩事件循环卡死/心跳代码坏两种解释),告警。草案的"hb 缺失 >= 2 周期标 offline"要按这个二分落地,否则每次 SW 打盹都是一条假告警,复刻旧系统"降噪-然后真事故也静默"的死循环起点。
- LWT 映射不是自欺,理由见判决 C4:localhost 上 close 是可靠信号,比 LWT(broker 代理观察)更强;自欺的风险点(连接在 != 手可用)草案已用三级判定和反模式 3 防掉。

### 1.5 结果未知态的 v1 降级(没有验证原语时怎么办)

显式立法,写进脑的派发器,骨架期即生效:

1. 任何 effectful 命令,凡出现以下情形即进入 suspect 终态:超时无 result;result 携带 sideEffectPossible=true;派发后手换代(bootId 变)且无 result;脑重启后在途日志里未终态。
2. suspect 是自动化的终态:永不自动重试,永不静默。落审计,进 client UI 的 suspect 队列(状态页显著位置),有通知。
3. suspect 冻结其幂等键:该键在人工裁决前,脑拒绝签发任何新命令(宁可少发落实为机械约束,不是口号)。
4. 人工在 UI 上二选一裁决:确认已发生(补记完成)/ 确认未发生(解锁,允许重新派发)。裁决记审计。
5. 升级路径:验证 readonly 原语(readTimeline 类)上线后,第 2 步前插入自动验证(轮次封顶 3),验证不能才落人工。suspect 的数据结构与 UI 不变,只是入口多了一道自动闸——所以 v1 先建 suspect 台账不是弯路,是终态的子集。

v1 里这条几乎零成本(里程碑 1 唯一标 effectful 的命令无真实副作用),但轨道必须先铺:等发消息原语上线那天才补,一定来不及。

### 1.6 配对设计(填补 §8 真空)

前提共识:安装器写 chrome.storage 不存在可行机制(Chrome 只有企业策略 managed storage 一条外部注入路,不走);扩展读不了本地文件;localhost WS 端口任何本地进程和(受混合内容限制的)网页 JS 都可能试探,所以不能裸奔,但威胁模型里"恶意本地进程"意味着机器已沦陷(SQLite 都能直接读),防御目标定在:挡住网页脚本、挡住误连、把特定扩展 profile 与脑绑定。

端口发现,三个选项:

- 甲(推荐):固定默认端口(新定,非 17321,选个冷门值),脑侧配置文件与手侧 options 页均可改。冲突时脑启动失败并在 UI 响亮报错,由用户改配置。成本最低,单机产品够用。
- 乙:动态端口 + 固定"发现端口"上的 HTTP 描述端点。被否:把问题平移回固定端口,还多一个组件。
- 丙:动态端口 + 文件/注册表发现。被否:扩展没有读本地文件的能力,除非再引入 Native Messaging——违宪。

token 与身份,推荐组合:

1. 传输前置校验:脑只接受 Origin 为 chrome-extension:// 前缀的握手(挡所有网页脚本;不锚定具体扩展 ID——锚 ID 就是旧契约的回魂)。
2. 首次配对:用户在 client UI 点"配对",脑进入 60 秒配对窗口;手(尚无 token)连上后发 hello{token:null, bootId, bundleVersion, capabilities};脑把它列为待配对,UI 展示(扩展 Origin、bundleVersion),用户点确认;脑签发 handId(hand-01 式可读名)+ 128 位随机 token,经 hello_ack 下发;手写入 chrome.storage.local(连接配置,禁令 2 允许的基础设施数据);脑侧 SQLite 存 paired_hands(handId, tokenHash, label, createdAt, lastSeenAt)。
3. 日常握手:hello 带 handId+token,校验失败 hello_ack{accepted:false, reason:bad_token} 后关闭;手停退避,转"待配对"态并在扩展图标/options 页提示。
4. 兜底路径:options 页手工粘贴(脑 UI 可显示一次性配对码)。留作配对模式故障时的逃生门,同一 hello 流程,不另开通道。
5. 多 profile:chrome.storage.local 按 profile 隔离,每个 profile 自动是独立的手,各自配对各拿 handId。v1 单活策略见判决 E4。
6. 显式不做(记录以防回潮):二维码(同机无意义)、Native Messaging 发现(违宪)、扩展 ID 白名单锚定(旧契约回魂)、token 轮换(首装截止点前补)。

---

## 第二部分:宪法一致性核查

| 宪法条款 | 草案核查结果 |
|---|---|
| 不背旧契约(plugin_token.json、17321、Native Messaging) | 冲突一处:§8"token 沿用 plugin_token.json 机制"——违宪,删除,替代见 1.6。端口与 Native Messaging 草案未涉及,无冲突 |
| 脑手模型,语义方向永远脑到手 | 合规。传感 event 只报不决策;hello_ack 下发 sensorConfig(参数是脑下发的数据)方向正确,v1 保留其最小形态(heartbeatIntervalMs 由脑下发,手不得自定心跳参数) |
| 禁令 1(不得有业务定时器) | 合规。心跳/重连退避/alarms 复活均为基础设施定时器,在豁免区;草案未要求手侧任何业务定时 |
| 禁令 2(不得持久化业务状态) | 合规,但要钉一条边界:去重表、result 队列必须留在内存,严禁"顺手"写 chrome.storage 提高可靠性——那是在手侧造第二账本,失忆是设计前提。chrome.storage 只放 handId/token/端口/能力缓存 |
| 禁令 3(一切动作由 cmd 触发,事件是提示) | 合规,§4.6 原文即此语义 |
| 禁令 4(手不自作主张重试) | 草案 §9 未明说,存在被旧代码带歪的缺口:旧问题 16 修复含"原语内部重试一次",照搬即违禁。已在 F1 补第六条纪律:原语 = 单次尝试 + 观察 + 诚实上报 |
| 禁令 5(监听注册只在 base) | 协议层无涉;提醒:WS 连接、心跳、alarms 注册全属 base,program 只进原语注册表 |
| 测试 = 生产同一通道 | 合规且被本审查强化:debug.* 原语走同一信封同一分发器;配对流程无测试旁路(开发期用同一配对流程,脑 CLI 可打印配对码);D7 删除 env 字段也顺带消灭了"按环境分支"的诱惑 |
| 脑平台无关(意图级命令) | 合规。debug.* 命名空间不占平台词汇;switchTab 类调试原语的 tabId 只出现在 args/evidence,不进平台无关词汇表 |

---

## 第三部分:v1 最小协议清单(里程碑 1)

### 3.1 消息种类与信封

实现六种 kind:`hello`、`hello_ack`、`cmd`、`ack`、`result`、`hb`。契约中保留但 v1 不实现:`progress`、`event`。

所有消息公共信封:`v`(int, =1)、`id`(uuid 串)、`kind`、`ts`(毫秒)。hello 之后的所有消息加 `epoch`(int,hello_ack 下发值的回显);ack/result 加 `re`(关联 cmd id)。

| 消息 | 字段 |
|---|---|
| hello(手->脑) | handId(首次配对为 null)、token(同)、bootId(SW 内存随机值,SW 每次出生新生成)、bundleVersion、capabilities[](原语名列表) |
| hello_ack(脑->手) | accepted(bool)、reason(拒绝时:bad_token / version_incompatible / superseded)、epoch、handId 与 token(仅配对签发时)、heartbeatIntervalMs |
| cmd(脑->手) | name、args、class(readonly / effectful)、idempotencyKey(effectful 必填)、expiresAt、timeoutMs(告知值,脑侧同款 deadline 为准) |
| ack(手->脑) | re、accepted、reason(expired / precondition_failed / busy / unsupported) |
| result(手->脑) | re、status(ok / failed / precondition_failed / expired / unsupported)、error{code, message, retriable, sideEffectPossible}、data、durationMs;evidence 字段保留位(debug 命令可空,业务 effectful 上线起强制) |
| hb(手->脑) | inFlightCmdId(可空)。page 信息推迟到内容脚本原语上线 |

### 3.2 必须实现的行为(编号即验收项)

1. 手主动连 WS;指数退避 1s 起、封顶 30s、带抖动;chrome.alarms(30-60 秒)复活兜底;hello 被拒即停退避转待配对态。
2. 心跳按 hello_ack.heartbeatIntervalMs(默认 22 秒)加抖动;脑记 lastHbAt。
3. 单活连接:同 handId 新 hello 顶旧连接(旧连接收 close, reason=superseded,记日志);非当前连接的消息丢弃 + 审计。
4. 配对:配对模式 60 秒窗口 + UI 确认 + 脑签发 handId/token;日常握手 Origin 校验(chrome-extension:// 前缀)+ token 校验;paired_hands 表落 SQLite(存 tokenHash)。
5. 脑派发前写在途日志(SQLite:id、name、class、idemKey、dispatchedAt、status),终态更新;脑重启加载:未终态 readonly 作废,未终态 effectful 转 suspect。
6. 脑每命令单一 deadline;超时:readonly 重建连接后重试 <= 2 次,再败标手 suspect;effectful 一律转 suspect(1.5 的五条规则全部落地,含幂等键冻结与 UI suspect 队列)。
7. 同 id 重发仅发生在重连后且 bootId 未变(1.2 规则);bootId 变化 = 在途收编。
8. 手分发器:唯一入口;未知原语 ack{accepted:false, reason:unsupported} + result{status:unsupported},禁止默认成功;执行前查 expiresAt;effectful 查内存去重表,命中即重放缓存 result。
9. 手侧去重表:内存 LRU 256,键 = cmd id(idemKey 冗余记录),值含终态 result;不落 chrome.storage。
10. 手侧 result 内存有界队列(容量 50,result 优先于其他),断线暂存、重连即冲;SW 死亡即丢(设计内,由第 5/6 条兜底)。
11. 脑对离线手不排队命令;tick 现算现发;expiresAt <= 2 倍 timeoutMs。
12. 调试原语:debug.ping(readonly)、debug.switchTab(readonly,result 带激活后 activeTabId 作为后置条件观察的练习);建议加 debug.slowEcho(声明 class=effectful、无真实副作用,sleep args.ms 后回 result)——用真通道演练 ack、超时、suspect、幂等键冻结全路径,不必等发消息原语才第一次踩这些轨道。
13. 客户端测试页与未来调度器共用同一信封、同一 WS、同一派发器(宪法 3 的构造性验收)。
14. 契约(信封、kind、错误码、close reason)全部出自 contract/ codegen,两端零手写对齐。

### 3.3 显式推迟项(各带升级触发条件)

| 推迟项 | 触发条件 |
|---|---|
| ackTimeout/leaseTimeout 双定时器 + progress 续租 | 首个 typicalDurationMs > 15 秒的原语(增量对账/批量采集)上线 |
| 结果未知自动验证闭环(readonly 验证、轮次封顶 3) | 首个业务 effectful 原语上线;立法:effectful 原语必须与其配对验证 readonly 原语同批交付 |
| evidence 强制与证据匹配规则(textHash+方向+相对位置) | 同上,随真机验证 |
| 能力级健康(contentScriptOk / 探活 probe / 自愈刷新原语)与 hb.page | 首个内容脚本参与的原语上线 |
| event 传感(unreadBadge / pageNavigated / loginStateChanged / manualInteraction)与两次一致读数纪律 | 脑对账轮设计落地 |
| blob 数据面 HTTP 通道 | 截图 / 简历 HTML 证据原语上线 |
| 多手并发注册表与按手派发(替代单活顶替) | Android 手立项或多 profile 并发需求转正 |
| 消息级 epoch 全量围栏语义回归 | 传输层解耦连接(MQTT / broker) |
| env / machineId 命名空间 | 云端 broker / 多租户 |
| token 轮换、配对管理 UI(吊销/重命名) | 首个真实客户安装之前(与交付机制同截止点) |
| hello_ack.bundleAction 或其替代物 | program 交付机制拍板 |
| 企微通知通道(suspect / 转人工) | 云端对接;v1 用 client UI 告警面板 + 日志顶上 |
| MQTT 迁移本身 | 远程手 / 云端多客户共存需求 |

---

## 判决统计

共 44 条(A 组 7、B 组 10、C 组 6、D 组 8、E 组 6、F 组 7):

| 判决 | 条数 | 条目 |
|---|---|---|
| 采纳 | 14 | A1 A2 A3 A4、B1 B5 B10、C1 C2 C4 C5 C6、D3、F3 |
| 简化 | 15 | A5、B2 B4 B7 B8 B9、C3、D6 D8、E5 E6、F2 F4 F5 F6 |
| 修改 | 10 | B3 B6、D1 D2 D4 D5、E2 E3 E4、F1 |
| 删除 | 5 | A6 A7、D7、E1、F7 |

注:E1(plugin_token.json)计删除;E2/E3/E4 是填补 §8 真空的新增设计,计修改。
