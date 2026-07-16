# C · 感知与同步模型:手如何让脑知道"候选人会话有新消息"

> 2026-07-16。依据:CLAUDE.md 宪法(脑手模型、手的五条禁令)、沟通逻辑重构 v3(巡检模型)、脑手通信协议草案 v0.1(§4.6/§6/§7/§13)、xq-resume-program/docs 三份一手 IM 资料、重写方向评估第七节血泪资产。
> 本文对三个悬置问题给出裁决,不罗列选项;并给出可直接进 contract 的命令/事件契约表与四个报文级时序推演。

---

## 0. 三个裁决(一屏版)

| 问题 | 裁决 |
|---|---|
| 轮询循环归谁 | **归脑**。巡检循环只存在于脑侧账号 actor;手零自主循环。手侧仅保留被动传感(MutationObserver 提示事件)作为**降延迟加速器**,提示只能把下一轮巡检拉前,永不构成第二条循环;最新稳定读数搭心跳帧便车。方案 (c) 否决,不存在非手侧定时器不可的场景。 |
| 要不要第三类命令 | **要**。新增 `intrusive` 类:业务只读、可自由重试(同 readonly),但驱动/占用页面且可能产生平台侧已读效果,故与 effectful 同队按账号串行(同 effectful)。二分类下这类命令放哪边都会破坏协议已有的正确性论证(见 §4.2)。 |
| 游标与快照形态 | **脑持会话级游标,手回有界窗口全量快照,新增投影在脑侧做**。游标 = 脑账本的"锚尾"(最近 ≤5 条消息的 方向+内容哈希 序列),随命令下发仅作滚动止损提示;手无任何跨轮记忆。对齐算法 = 快照前缀匹配账本后缀、取最大重叠;卡片消息按"身份哈希 + 可变状态分离"处理,按钮事件 = 对齐位置上的状态跃迁,而非新消息。 |

正确性只由巡检对账层承担;事件与心跳搭载只影响延迟,丢光了系统仍然正确(慢几分钟,而规格明文允许分钟级)。

---

## 1. 前提回顾(压缩到本题所需)

**宪法约束**:手不得有业务定时器(setInterval/chrome.alarms 仅限重连兜底与心跳保活);手不持久化业务状态,失忆是设计前提;一切动作由 cmd 触发,事件只上报不决策;事件是提示不是账本;命令语义平台无关,DOM/selector 只许出现在手侧实现注记与 evidence。

**业务规格输入**(v3):巡检驱动,数分钟一轮,仅 8:00–24:00;一切时间门槛是下界;无巡检外实时通道 ⇒ **分钟级延迟是规格明文接受的,秒级实时是过度设计**。单档案处理顺序:先事件层 → 再对话分支 → 最后时刻表;7 天兜底最优先。

**平台事实**(一手资料,智联):

| 事实 | 来源 | 对设计的意义 |
|---|---|---|
| 会话列表可经页面 API `POST /api/im/session/list` 分页拉取(`filterType/pageNum/pageSize`),归档中见过 1800+ 会话 | zhilian-reverse-engineering | 列表读有低侵入实现路径;必须分页,必须有截止策略 |
| 列表项只含"最后一句"摘要;候选人连发多条会漏 | 同上 | 列表只能做变更检测,不能做账本来源 |
| 完整消息历史无稳定 REST 接口,必须点开会话读 DOM 气泡,更早消息需向上滚动加载 | 同上 + zhilian-im-scenes | 会话读必然占用页面 |
| 点开会话产生平台侧"已读"效果,候选人可感知"已读不回" | 产品输入 | "业务只读"却有候选人可感知的平台侧效果 |
| 消息无稳定 id;会话有稳定 sessionId(可作直达与去重键) | 协议草案 §13 + im-scenes | 游标只能建立在内容序列上,但可锚定在会话上 |
| 未读徽章:左导航"聊天"有总数徽章(平台任意页面可见);列表页每会话有徽章,存在 `display:none` 隐藏徽章陷阱 | im-scenes | 总徽章是最佳被动传感点;一切徽章读数必须"可见才算 + 两次一致" |
| 列表摘要节点可能混入隐藏状态文本(如 `[1条]`) | im-scenes | 摘要取直接文本节点,实现注记级纪律 |

**血泪三条**(重写方向评估 §7,直接约束本题):
1. 插件每轮回传全量,"本轮新增"投影必须在脑侧做;
2. 事件检测只吃本轮新入库投影,扫全量历史 = 旧卡片跨轮重复触发、永久锁死;
3. 读数两次一致才可信(未读徽章闪空教训)。

---

## 2. 裁决一:轮询循环归脑,手是纯传感器

### 2.1 三方案对比

| 维度 | (a) 脑下发同步命令,手零循环 | (b) 手侧被动观察者,只发提示事件 | (c) 手自起定时器轮询回传 |
|---|---|---|---|
| 与宪法关系 | 完全符合("感知类工作以脑下发 readonly 对账命令为主") | 符合(MutationObserver 不是定时器;事件只上报不决策,§4.6 已定义 unreadBadge) | **违反禁令一**(业务定时器) |
| 正确性承载 | 能:巡检 + 对账,丢事件无影响 | 不能:content script 随导航死、SW 睡眠、页面不在时全盲;事件 QoS0 可丢 | 表面能,实际不能:手失忆,无法判断"哪些是新的",只能回传全量,判断仍在脑 |
| 延迟 | 巡检间隔(分钟级,规格允许) | 秒级提示(仅当页面在、脚本活) | 定时器间隔,与 (a) 无本质差别 |
| 失效形态 | 脑不在 → 全停(可接受:脑是唯一记忆体,脑不在时手采到的东西无处入账、无人决策) | 静默失效(页面关了就没提示)——所以只能当加速器 | 旧系统原样:插件自循环、节奏散落两端、测试不可控 |
| 复杂度 | 脑侧一个调度器(反正要有) | content script 一个 Observer + 去抖 | 手侧节流/互斥/模式判断全套(旧系统 zhilian-automation-scheduling 那一堆) |

### 2.2 裁决

**(a) 为骨干,(b) 为加速器,(c) 否决。**

- 巡检循环、节律、8:00–24:00 窗口、每账号频率,全部只存在于脑的账号 actor。手侧不存在任何"每 N 分钟做某事"的代码。
- content script 在平台页面挂 MutationObserver 盯左导航总未读徽章(平台任意页面可见,不限 IM 页),变化经两次一致确认后发 `unreadBadge` 提示事件。提示唯一效果:把该账号下一轮巡检**拉前**(见 §7.b),永不直接触发业务动作,永不绕过巡检代码路径。
- **对 (c) 的诚实评估**:逐一检验"非它不可"的候选场景——
  - *脑掉线期间盯梢?* 无意义:手无记忆无账本,采了无处放;脑回来后第一轮巡检对账天然补齐,规格接受分钟级。
  - *SW 睡着错过变化?* MutationObserver 活在页面进程,不依赖 SW 醒着;事件经 port 发送本身会唤醒 SW。不需要定时器。
  - *页面不在时轮询?* 页面不在时定时器也无 DOM 可读,轮询无对象。该场景的正解是脑的 `ensurePlatformReady`(§8)。
  - 结论:不存在 (c) 独占的能力,否决无保留。

### 2.3 定时器边界立法(骨架期即写进插件 lint 白名单)

- 禁止:一切周期性定时器用于业务(采集节奏、状态检测节奏、互动间隔——旧系统那三个节流器全部不得复活)。
- 允许:WS 重连退避与 chrome.alarms 兜底重连;心跳定时器;**传感读数去抖的一次性 setTimeout**(≤3 秒、由一次具体 DOM 变更触发、到点只做一次复读)。去抖是"两次一致才可信"纪律的实现细节,不是循环,立法点名放行以免执行期扯皮。

### 2.4 心跳便车

心跳帧(20–25s)增加可选字段 `sensors`,搭载**已缓存的最新稳定读数**,不搭载新采集动作:

```json
{ "kind": "hb", "epoch": 12, "page": {...},
  "sensors": { "unreadTotal": { "value": 3, "observedAgoMs": 4200 } } }
```

- 值来源:content script 每次产出稳定读数时推给 SW,SW 存内存(不进 chrome.storage);组心跳时原样附上。无读数(页面不在/脚本死/SW 刚重生)则为 `null`——这本身就是能力级健康信号。
- **禁止**心跳定时器到点时反向拉取 DOM 读数——那等于把业务轮询挂在基础设施定时器上,是禁令一的绕行。便车只运现货,不下单。
- 作用:补偿 QoS0 事件丢失,给脑一条 ≤25s 粒度的兜底提示通道。仍是提示不是账本。

### 2.5 感知三层栈(总图)

| 层 | 通道 | 延迟 | 可靠性 | 承担什么 |
|---|---|---|---|---|
| 1 事件 | evt(QoS0) | 秒级 | 可丢、可迟到、可假 | 只降低延迟:拉前巡检 |
| 2 心跳搭载 | hb.sensors | ≤25s | 可丢(连丢=健康问题) | 事件丢失的提示兜底 |
| 3 巡检对账 | 脑下发 intrusive 同步命令 | 分钟级 | **唯一正确性来源** | 账本、去重、事件检测、一切业务判断 |

任何业务决策禁止读取层 1/2 的值本身;层 1/2 只允许影响"层 3 什么时候跑"。这一条是徽章假阳性无害化的根本(§7.d)。

---

## 3. "读"的真实代价:智联页面上的动作清单

| 业务意图 | 页面真实动作 | 平台侧效果 | 重复执行的额外代价 |
|---|---|---|---|
| 读会话列表 | 优路径:页面上下文调 `session/list` 分页 API,零 UI 动作。兜底路径:需在 `/app/im`,滚动左侧列表逐批加载 DOM | 无(读列表不清未读) | 无 |
| 读某会话完整消息 | 必须点开会话(导航到 `?sessionId=`),读气泡 DOM;更早消息需向上滚动 | **清未读徽章 + 候选人侧显示已读**(不可逆,一次性) | 无(已读效果不叠加) |
| 确保平台页面就位 | 可能创建/激活标签页、SPA 导航 | 无 | 无 |
| 发消息(对照) | 打开会话、输入、发送(或 sendText API)+ 回读验证 | **候选人收到一条消息**(不可逆,可叠加) | **每重复一次多骚扰一次** |

注意"已读不回"并非只是代价——规格明文把它当行为用(v3 §8:已淘汰者来消息→已读不回;例 8:骂人→一个字不回)。所以协议不需要"避免已读"的机制,只需要让脑**知道并掌控**何时产生已读:即读会话必须是脑显式下发的命令,且与发消息同队串行(读完紧接着回,candidate 看到"已读→几秒后回复"是自然节奏)。

---

## 4. 裁决二:命令三分类,新增 `intrusive`

### 4.1 两轴分析

命令其实有两个正交属性:

- **副作用轴**:执行两次与执行一次,候选人可感知世界/平台账务是否有额外差别。发消息=有(多一条骚扰);读会话=无(已读效果一次性、幂等);读列表=无。
- **占用轴**:是否驱动页面(导航/点击/滚动),即是否与其他驱动命令在同一账号页面上互斥。

二分类把两轴捏成一轴,对"业务只读但侵入页面"的命令两边都放不下:

- **判成 readonly**:§6 矩阵允许 readonly 自由重发重派——一条正在重派的 readConversation 会把同账号在途 sendText 的页面踩掉(导航走了,发送/验证读全毁);账号互斥只覆盖 effectful,保护不到它。
- **判成 effectful**:"永不自动重派"生效——但重读会话本来绝对安全,白白丧失自动重试;更致命的是**循环依赖**:effectful 结果未知时,§6 规定"只允许下发只读验证命令",而验证命令(读时间线找证据)恰恰就是这条读会话命令——若它自己也是 effectful,验证流程无法启动,协议的结果未知态论证塌掉。

### 4.2 裁决:三分类

| class | 定义 | 重试语义 | 排队语义 | 幂等键 | evidence |
|---|---|---|---|---|---|
| `readonly` | 不驱动页面、无平台侧效果的纯探测(probePlatform) | 自由重发、重派、可并行于任何队列 | 不进账号串行队,随到随执行(手侧单命令执行天然排队) | 不需要 | 不需要 |
| `intrusive`(新增) | 业务只读、**平台侧效果幂等**(至多产生一次性已读/页面就位),但驱动或占用页面 | **同 readonly:可自由重发重派**(换 id 重派也安全);失败重试策略在脑,有轮次封顶 | **同 effectful:进同一账号串行队**,同账号同时刻至多一条 {intrusive ∪ effectful} 在途,跨手生效 | 不需要 | 不需要(data 即产出) |
| `effectful` | 有不可逆、可叠加的外部副作用 | 永不自动重派;结果未知走验证流程 | 账号串行队 | 必填 | 必填 |

**归类判据(litmus,写进 contract 注释)**:执行两次与一次对候选人可感知世界无任何额外差别、且不发出任何业务内容 → 看是否驱动页面:驱动 = intrusive,不驱动 = readonly;否则 = effectful。拿不准 → effectful(与 sideEffectPossible"拿不准标 true"同一保守方向)。

类别在 contract 里**静态声明、按最坏实现定**:readConversationList 即使手侧走 API 零 UI,也声明 intrusive(DOM 兜底路径存在);声明从紧无害(多排一次队),声明从宽会踩页面。

### 4.3 §6 超时重试矩阵新增行

| 故障点 | intrusive 命令 |
|---|---|
| L2 超时(无 ack) | 原 id 重发 ×3(同 readonly) |
| 租约超时(有 ack 无 result) | 直接重派(新 id 同 args),**无结果未知仪式**;连续 3 轮租约超时 → 能力级告警,转 ensurePlatformReady 自愈,再失败转人工 |
| result: failed, retriable=true | 退避重派,轮次封顶 3 |
| result: precondition_failed | 按 ack/result 的 detail 子码处置(§8.1),多为先派 ensurePlatformReady |
| 旧 epoch 的 result | 丢弃 + 审计(与 readonly 同;对齐算法本可幂等吸收,但规则统一优先) |
| sideEffectPossible 字段 | 不适用。凡不能保证平台侧效果幂等的原语,无资格声明 intrusive |

**冻结槽规则**:某账号存在结果未知态的 effectful 命令时,该账号串行队冻结,不派新 effectful/intrusive;唯一例外是脑为解决该未知态下发的验证读(readConversation),它占用冻结命令自己的槽位执行。

### 4.4 排队模型(v1 刻意做小)

- 每账号一个串行域:{effectful ∪ intrusive} 同域互斥,跨手生效(两个浏览器开同一账号也只有一条在途)。readonly 不占域,但手侧执行器本就单命令串行(hb.inFlightCmd 是单值),实际也是 FIFO 插队跑。
- v1 **没有多生产者优先级队列**:巡检轮本身就是账号 actor 的一段顺序程序——列表同步 → 逐个脏会话同步 → 决策 → 逐条发送。所谓"排队"就是这段程序的串行执行,加三个闸:提示拉前(轮与轮之间)、手动静默窗(§4.5)、结果未知冻结(§4.3)。旧系统"阶段串联、互斥、跳过"的全部智慧收敛为脑侧一段普通顺序代码,这正是主动权在脑的红利。

### 4.5 用户手动操作冲突

- **感知**:content script 对平台标签页上的受信任用户输入(isTrusted 的 pointer/keyboard/导航)发 `manualInteraction` 事件,节流 ≥1 次/5s。
- **脑的反应**:收到后进入该账号"手动静默窗"(默认 45s,sensorConfig 可调,每次新事件重置):静默窗内不派发新的 intrusive/effectful;**在途命令不撤销**,让它自然完成或失败(撤销语义比冲突本身贵)。
- **冲突失败形态**:用户操作把页面从命令脚下抽走 → 原语后置自检不过 → result{failed, retriable:true}(intrusive)或结果未知(effectful,由既有矩阵接管)→ 脑等静默窗过后重试。
- **用户先动我们后动**:用户自己点开会话读了消息 → 徽章清零、平台已读已产生 → 对脑无损:脏检测不依赖徽章(§5.5),巡检照常读到内容;用户手发的消息在下轮快照对齐中以"账本外 out 消息"出现,入账并滑动沉默锚点(方向保守:多算一次我方触达只会推迟催,不会多催)。

---

## 5. 裁决三:游标与快照

### 5.1 先立柱:手侧增量不可行

手算增量需要"上一轮我看到了什么"的记忆;SW 30 秒即死、content script 随导航死、chrome.storage 禁存业务状态 ⇒ 任何手侧"since last time"在重启后都是谎言。血泪 1 的旧系统结论与失忆前提在这里重合:**手回有界全量快照,脑做本轮新增投影**。快照重复回传是幂等安全的(对齐吸收),localhost 带宽不构成约束。

### 5.2 账本与游标

- 脑侧账本(SQLite):每会话一张有序消息序列 `message(convId, seq, direction, kind, contentHash, text/blobRef, cardType?, cardState?, origin{self|external}, firstSeenRound)`。seq 是脑内自增,不是平台 id。
- **游标 = 会话级,由账本尾部派生**:`anchorTail = 账本最近 ≤5 条的 [{direction, contentHash}]`。不用时间戳(DOM 时间是稀疏粗粒度分隔条,不可靠);不用全局游标(列表按活跃度重排,页间无稳定序)。
- 游标**只存在于脑**,随 readConversation 的 args 下发;对手而言它只是"向上滚动到匹配即可停"的止损提示,手对它零记忆零解释义务(v1 手可以忽略它,固定读最近窗口,只慢不错)。
- contentHash = sha256(规范化内容),规范化规则进 contract(NFC、trim、连续空白折叠;图片/语音/文件用占位符 `[image]/[voice]/[file]` 参与哈希——与规格的判意向占位符同源)。卡片哈希见 §5.4。

### 5.3 对齐算法(脑侧新增投影)

设账本序列 L(全量),快照 S(最近窗口,长度 ≤ maxMessages):

1. 求最大的 j,使 S[0..j] 恰为 L 的某个后缀(快照前缀 ⟷ 账本后缀,取**最大重叠**)。
2. S[j+1..] 即"本轮新增",逐条 append 入账;事件层与对话分支**只吃这段投影**(血泪 2)。
3. j 不存在(零重叠)且账本非空 → 视为窗口不够深,升级为深读(readConversation deep=true,窗口放大、滚动至锚匹配或到顶);仍零重叠 → 审计告警 + 按收编处理(§5.6),不静默吞。
4. 歧义(多个 j 可行,只在候选人贴尾连发逐字相同内容且窗口边界恰好落在其间时可能):取最大 j(最少新增)并写审计。偏置理由:对话轮合并使少量重复无害化,但重复卡片会重触发事件层;而漏消息的风险由锚尾 ≥3 条的上下文携带压到实际为零——单条重复文本("好""好")因锚尾包含前一条不同消息而自然消歧。残余风险登记在 §10。

**不重**:重复快照、迟到快照、双手先后各交一份快照,对齐后新增投影都为空,天然幂等。**不漏**:漏只可能来自"结果永远没回来",而 intrusive 可无限重派直至成功;单轮窗口不够深由第 3 步升级兜住;彻底漏(对齐歧义误吸收)概率见上。最后一道网:7 天兜底归档保证任何静默失误不会让档案永久悬挂。

### 5.4 卡片消息:身份与状态分离(解开"旧卡片重复触发"死结)

平台卡片(邀面卡、换微信卡、附件简历卡)有**可变状态**:候选人点"接受"后,同一条消息的渲染从 pending 变 accepted。若把状态混进 contentHash,老卡变状态会击穿中段对齐;若忽略状态,就看不见"点接受面试"这一最关键事件。裁决:

- `contentHash` 只哈希卡片**身份**(cardType + 关键参数,如邀面时段),状态走独立字段 `cardState ∈ {pending, accepted, rejected, expired, unknown}`。
- 对齐用身份哈希 → 老卡永远对得上位置;脑比较**对齐位置上的 cardState 差**:账本 pending、快照 accepted ⇒ 产出一次"acceptedInterview"业务事件并更新账本状态。
- 去重自动成立:状态更新入账后,下轮两侧同为 accepted,无跃迁无事件——旧卡片跨轮重复触发在机制上不可能,且天然支持规格要求的"已结束者点旧卡直进已约面"(状态跃迁与档案状态无关,谁跃迁谁触发)。
- 已知缺口:老卡在快照窗口之外被点(候选人翻两周前的卡接受)。依赖平台在会话尾部追加系统提示("已接受面试邀请"类 toast,会作为新消息进入投影);若真机验证发现无 toast,需补一条低频对账原语(读平台面试管理面,backlog,§10)。

### 5.5 会话列表:变更检测,不是账本

`readConversationList` 返回列表快照,脑对每个已绑定会话做**脏检测**,任一命中即标脏:

1. `unreadCount > 0`;
2. `lastMessage` 摘要(方向 + 预览哈希)≠ 账本已知最后一条——这条是徽章的独立备胎:用户手动点开读过(徽章清零)、用户/手机端替我们发过话,都逃不过摘要差。

脏 ⇒ 该档案本轮进入 readConversation;不脏且无到期时钟 ⇒ 本轮零页面动作。到期时钟(催/归档)不要求先读会话:列表摘要未变即证明无新入站,直接按时刻表执行(发送类命令自带回读验证)。

**分页与截止**:列表按最近活跃排序;巡检读采用 `filter:"all"` + `stopOlderThanDays:8` + `maxSessions` 上限,单命令内手自行翻页、每页发 progress。8 天窗覆盖一切沉默轨时钟(最长 7 天兜底);唤醒场景(归档两周后来消息)不会漏——新消息使会话跳回列表顶端且带未读,天然落在窗口内。日打招呼 80–90 × 8 天 ≈ 700 会话 ≈ 15 页,API 路径秒级,DOM 兜底路径分钟级(progress 续租扛住)。1800+ 存量老会话永远不用全扫:未绑定档案的会话,脑只记日志不处理(主动联系留白,规格明文暂不响应)。

**绑定**:列表项带 `peer.platformUserRef`(实现注记:智联主窗口会话身份可回填 userId),脑用 platform+userId 锚绑定到档案(CLAUDE.md:不锚 resumeNumber)。取不到 userRef 时以 displayName+职位启发式暂绑、首次 readConversation 确证,列入真机验证清单。

### 5.6 收编边界与事件抑制(bootstrap)

每会话账本记 `adoptedBoundarySeq`:

- **系统亲生的会话**(我方招呼开场):账本自 evidence 起即有第一条,边界 = 0,此后一切新增投影正常触发事件——哪怕系统停机两天后回来,期间候选人点的卡也是"边界后新增/状态跃迁",照常触发。
- **收编的存量会话**(未来接入主动联系等场景):首次深读拿到的全部历史整体入账,边界 = 该快照末尾;边界前消息不产生事件层触发、不进对话轮,仅作语境。血泪 2 的"扫全量历史=旧卡重复触发"由此在收编场景也被封死。

---

## 6. 感知契约表

### 6.1 命令表(平台无关;智联细节只在 §6.4 实现注记)

| name | class | args | 返回 data | 典型时长/租约 | 用途 |
|---|---|---|---|---|---|
| `probePlatform` | readonly | `{}` | `{ pageKind, contentScriptOk, loginState, surface }` | <1s / 5s | 能力级探活;派驱动命令前的低成本预检 |
| `ensurePlatformReady` | intrusive | `{ surface: "im" }` | `{ ready, loginState, createdTab }` | 3–15s / 30s | 平台页面就位:定位/激活/必要时新建标签页并等 SPA 与 content script 就绪。**不做登录**(登录永远人工) |
| `readConversationList` | intrusive | `{ filter, stopOlderThanDays?, maxSessions? }` | `{ sessions: [...], complete }` | 2–60s / 60s(progress 每页续) | 巡检变更检测的唯一入口 |
| `readConversation` | intrusive | `{ conversationRef, window }` | `{ messages: [...], reachedTop, anchorMatched, peer? }` | 3–30s / 30s(progress 每滚动批续) | 会话对账;兼任 effectful 结果未知态的验证读 |

字段明细(直接进 contract 的 schema 粒度):

```
probePlatform.result.data:
  pageKind         enum: "im" | "recommend" | "other" | "none"   // none = 无平台页面
  contentScriptOk  bool
  loginState       enum: "in" | "out" | "unknown"
  surface          { imListVisible: bool } | null

ensurePlatformReady.args:
  surface          enum: "im"                      // 预留 "recommend"
ensurePlatformReady.result.data:
  ready            bool
  loginState       enum: "in" | "out" | "unknown"  // out ⇒ ready 恒 false
  createdTab       bool                            // 审计:本次是否新建了标签页

readConversationList.args:
  filter             enum: "all" | "unread"        // 巡检用 all;提示快查可用 unread
  stopOlderThanDays  int, default 8                // 按最近活跃截止
  maxSessions        int, default 1000
readConversationList.result.data:
  sessions[]:
    conversationRef  string                        // 平台会话稳定键的不透明封装
    peer             { displayName: string, platformUserRef?: string }
    unreadCount      int                           // 遵守"可见才算 + 两次一致"
    lastMessage      { direction: "in"|"out"|"system", kind: MsgKind, textPreview: string(<=200) }
    lastActivityTs   int|null                      // 列表可见的近似时间,仅排序参考
  complete           bool                          // false = 截止/上限截断

readConversation.args:
  conversationRef  string
  window:
    maxMessages    int, default 50, max 500
    anchorTail     [{ direction, contentHash }] (<=5, 可缺省)   // 滚动止损提示,手可忽略
    deep           bool, default false             // true = 放大窗口滚动至锚匹配或到顶
readConversation.result.data:
  messages[]  (按时间正序):
    idx            int                             // 快照内相对序号
    direction      enum: "in" | "out" | "system"
    kind           enum: "text" | "image" | "voice" | "file" | "card" | "system"
    text           string|null                     // >2KB 转 blobRef
    blobRef        string|null
    contentHash    string                          // 规范化内容哈希;card 为身份哈希
    cardType       enum|null: "interviewInvite" | "wechatExchange" | "resumeAttachment" | "other"
    cardState      enum|null: "pending" | "accepted" | "rejected" | "expired" | "unknown"
    tsApprox       int|null                        // 来自稀疏时间分隔条,仅审计
  reachedTop       bool
  anchorMatched    bool
  peer             { displayName, platformUserRef? } | null      // 用于绑定确证
```

通用规则:result.data 超过 64KB 走 blobRef;两条 read 原语内部一切徽章/列表读数遵守两次一致纪律(§9 原语纪律的具体化)。

### 6.2 事件表(全部 QoS0,提示不是账本)

| type | 字段 | 触发条件 | 去抖与两次一致 | 脑的唯一许可反应 |
|---|---|---|---|---|
| `unreadBadge` | `{ scope:"total", value:int, prev:int\|null, stable:true, observedAt }` | 平台页左导航总未读徽章 DOM 变化 | 变化后 badgeDebounceMs(默认 800ms)复读,两次相等才发;与上次已发值相同不发;最小发射间隔 badgeMinEmitIntervalMs(默认 5s) | value 增大 → 拉前该账号下一轮巡检(§7.b);value 减小/为 0 → 仅记录,**永不**据此跳过或取消任何巡检 |
| `pageNavigated` | `{ pageKind, at }` | 平台标签页 SPA 路由/整页导航完成且稳定 navSettleMs(500ms) | 稳定后单发 | 更新手状态表;IM 页离开时校准能力级健康 |
| `loginStateChanged` | `{ state:"in"\|"out", stable:true, at }` | 登录态 DOM 特征变化 | 两次一致 | out → 暂停该账号派发 + 告警转人工(登录永远人工) |
| `manualInteraction` | `{ kind:"pointer"\|"keyboard"\|"navigation", pageKind, at }` | 平台标签页收到 isTrusted 用户输入 | 节流 ≥1 次/5s | 开/续手动静默窗(默认 45s),窗内不派驱动命令 |

传感参数全部由脑经 `hello_ack.sensorConfig` 下发:`{ badgeDebounceMs, badgeMinEmitIntervalMs, navSettleMs, manualQuietMs }`——手无自带策略数值。

### 6.3 对协议草案既有帧的扩展

- `hb.sensors`(§2.4):`{ unreadTotal: { value:int, observedAgoMs:int } | null }`。
- `ack` 与 `result` 增加机器可读子码 `detail`(可选 string),用于 precondition_failed 分诊:`"no_platform_surface" | "login_required" | "content_script_dead" | "conversation_not_found" | "user_active"`。
- cmd 信封不变;intrusive 作为 `class` 第三枚举值进 contract codegen。

### 6.4 手侧实现注记(智联专有,不进协议语义)

- readConversationList 优先页面上下文调 `POST /api/im/session/list`(`{filterType, pageNum, pageSize}`)翻页;API 不可用时兜底 DOM:确保 `/app/im`,滚动 `aside.im-aside--left`,会话项 `.im-session-item__box`,姓名 `.im-session-item__name-title`,摘要 `.im-session-item__msg`(**只取直接文本节点**,防 `[1条]` 隐藏文本污染),未读 `.im-session-item__unread .km-badge__item` 且 **display:none 不算**。
- readConversation:导航 `/app/im?sessionId=<ref>`(sessionId 即 conversationRef 的载体);气泡 `.im-timeline .im-message__bubble`,我方 `--me` 后缀 ⇒ out,`.im-message__toast` ⇒ system;向上滚动加载更早消息;打开会话即产生平台已读——这是 intrusive 分类的由来,不是 bug。
- platformUserRef 回填:主窗口会话身份含 userId/sessionId/sessionType(旧系统 data-resume-* 回填机制),锚 userId,不锚 resumeNumber。
- 总未读徽章观察点:左导航"聊天"项红色计数,rd6 域任意页面可见;MutationObserver 挂 characterData+childList+attributes(含 style/class,捕获显隐切换)。
- 多标签页仲裁见 §8.3:仅 canonical 标签页执行驱动命令与发射传感事件。

---

## 7. 端到端时序推演(报文级)

约定:B=脑,H=手;帧只写关键字段;巡检间隔取 5 分钟示例;账号 `zhilian:acc01`。

### 7.a 候选人在两轮巡检之间回消息:下一轮如何发现、入库、去重

前情:10:00 轮我方对会话 s123(档案 P)发出回复"明天下午三点方便吗",账本尾 = […, out:"明天下午三点方便吗"]。10:02 候选人回两条:"可以的""地址发我一下"。(设徽章事件恰好全丢——只靠层 3。)

```
10:05:00  B 巡检 tick(acc01 在 8:00–24:00 窗口内,串行域空闲,无手动静默窗)
10:05:00  B→H cmd { id:C1, name:"readConversationList", class:"intrusive",
                    args:{ filter:"all", stopOlderThanDays:8 }, leaseMs:60000, expiresAt:+120s }
10:05:00  H→B ack { re:C1, accepted:true }
10:05:01  H→B progress { re:C1, note:"page 1/…", pct:20 }        // 每页一帧
10:05:03  H→B result { re:C1, status:"ok", data:{ sessions:[
              { conversationRef:"s123", peer:{displayName:"朱女士", platformUserRef:"u778"},
                unreadCount:2,
                lastMessage:{ direction:"in", kind:"text", textPreview:"地址发我一下" } },
              …其余会话… ], complete:true } }
10:05:03  B 脏检测:s123 unread=2 且 lastMessage ≠ 账本尾 ⇒ 档案 P 标脏
10:05:03  B→H cmd { id:C2, name:"readConversation", class:"intrusive",
                    args:{ conversationRef:"s123",
                           window:{ maxMessages:50,
                                    anchorTail:[ {dir:"in",hash:"h_a"},{dir:"out",hash:"h_b"},
                                                 {dir:"out",hash:"h_c"} ] } }, leaseMs:30000 }
          // h_c = "明天下午三点方便吗" 的哈希;此步产生平台侧已读(P 即将被回复,规格自然)
10:05:04  H→B ack { re:C2, accepted:true }
10:05:07  H→B result { re:C2, status:"ok", anchorMatched:true, data:{ messages:[
              …, {direction:"out", contentHash:"h_c", text:"明天下午三点方便吗"},
                 {direction:"in",  contentHash:"h_d", text:"可以的"},
                 {direction:"in",  contentHash:"h_e", text:"地址发我一下"} ], reachedTop:false } }
10:05:07  B 对齐:快照前缀匹配账本后缀,最大重叠止于 h_c ⇒ 新增投影 = [in:h_d, in:h_e]
          入账 seq+1,+2;事件层扫投影:无卡片跃迁;对话分支:两条合并为一轮 → 判意向(LLM)
10:05:12  B→H cmd { id:C3, name:"sendTextAndVerify", class:"effectful",
                    idempotencyKey:"send:P:round7", args:{ conversationRef:"s123", text:"地址是…" } }
10:05:16  H→B result { re:C3, status:"ok", evidence:{ matched:{ textHash:"h_f", direction:"out",
                    position:"last" } } }
10:05:16  B 账本追加 out:h_f(origin:self),滑动沉默锚点,判意向结果落库
```

**去重验证**:10:10 轮列表读若 s123 再次入选(如 unread 读数迟滞),readConversation 快照尾 = […h_d,h_e,h_f] 与账本完全重叠 ⇒ 新增投影为空 ⇒ 零事件零动作。同一条"可以的"永不会被判两次——这就是"事件检测只吃本轮新入库投影"的机制化。

### 7.b 未读徽章提示到达:立即同步还是标脏等巡检——裁决:拉前巡检

```
14:31:07  候选人来消息;content script Observer 捕获导航徽章 2→3
14:31:07  H 传感器读得 3,armed 一次性 setTimeout(800ms)
14:31:08  H 复读仍为 3 ⇒ H→B event { type:"unreadBadge", scope:"total", value:3, prev:2, stable:true }
14:31:08  B:不查任何业务,只调度:acc01.nextPatrolAt = min(原定 14:34:30, now + 25s 合并窗)
          // 合并窗:25s 内继续到达的徽章事件不再前移,吸收连发
          // 频控:提示拉前的巡检与上一轮间隔 ≥60s,不满足则维持原定时刻
14:31:33  B 巡检提前开跑:走 7.a 完整路径(列表读 → 脏 → 会话读 → 对话分支)
```

**为什么不"立即发同步命令"**:徽章事件可假、可抖、可在夜里来;立即翻转成命令 = 事件驱动了业务节奏,等于把循环还给了传感器,且需要为命令风暴另建频控。**为什么不"只标脏等原定巡检"**:白白放弃平均半个巡检间隔的延迟改善,而改善的代价只是挪一个定时器时刻。拉前 = 两全:事件的全部权力被收窄为"调整下一轮开跑时间",业务路径仍只有巡检一条,夜间(0:00–8:00)拉前自动失效(调度器窗口检查),脏标记留到次日 8:00 首轮。

### 7.c 同步命令执行中途 SW 死/页面刷新:超时、恢复、重发,不重不漏

```
15:20:00  B→H cmd { id:C7, name:"readConversation", class:"intrusive",
                    args:{ conversationRef:"s456", window:{ maxMessages:50, anchorTail:[…] } },
                    leaseMs:30000 }
15:20:01  H→B ack { re:C7 }        // 已点开会话:平台已读效果此刻已产生
15:20:05  用户刷新页面 / SW 被杀:content script 死,读到一半的数据随之蒸发,无 result
15:20:31  B leaseTimeout(无 progress 无 result):class=intrusive ⇒ 无结果未知仪式,
          直接判定"本次尝试作废"。账本零写入(没有 result 就没有任何入账)——漏的候选态
15:20:34  H(SW 重生/WS 重连)→B hello { handId, bootId' }
          B→H hello_ack { epoch:13, inFlightCmds:["C7"] }
          H→B 逐条回答:C7 unknown(失忆是设计前提)
15:20:34  B 关闭 C7(audit:lost),重派:
          B→H cmd { id:C8, name:"readConversation", class:"intrusive", args:同 C7, epoch:13 }
          // args 逐字节相同:期间无新数据入账,anchorTail 由账本重新派生,结果一致
15:20:39  H→B result { re:C8, status:"ok", data:{ messages:[…] } }
15:20:39  B 对齐入账。若 C7 的僵尸 result 竟然迟到(旧 epoch):丢弃 + 审计(§4.3);
          即便误收,对齐幂等也使其无害——双保险,规则从严
```

**不漏**:入账只发生在 result 之后,半途死亡 = 零写入,重派读到的是超集,升级深读兜住窗口不足。**不重**:重派读到重复内容由对齐吸收为空投影。**对照组**:若 C7 是 sendTextAndVerify(effectful),同一故障走的是结果未知态——冻结串行域、派验证读找 textHash 证据、验证不能则转人工,永不自动重发。同一故障两种命运,正是 intrusive 单列一类的全部价值:它把"可以放心重来的页面驱动"从昂贵的结果未知仪式里解放出来,又不享受 readonly 的并行豁免。

### 7.d 徽章闪空/假阳性:如何不引发误动作

场景一,闪空(F1 教训原型):渲染抖动使徽章瞬时消失。

```
16:02:10  Observer 读得 0(上次稳定值 3)
16:02:10  armed 800ms 复读
16:02:11  复读得 3 ⇒ 两次不一致 ⇒ 不发射任何事件(重新 armed,至多 K=3 次)
```

第一道闸(手侧两次一致)直接吞掉抖动。若抖动长到发出稳定 0:脑对 value 减小/为 0 的许可反应 = 仅记录(§6.2)——脏标记从不因徽章清除,巡检从不因徽章为 0 跳过。**误动作空间为零,因为徽章的值从未进入任何业务判断,它只有"拉前巡检"一种正向权力,是油门不是刹车。**

场景二,假阳性(徽章升了但其实无新消息,或用户随手点开又造成读数跳变):

```
16:40:00  B 收到 unreadBadge{value:5} ⇒ 拉前巡检
16:40:26  readConversationList 快照:所有已绑定会话 lastMessage 与账本一致、unread=0
          ⇒ 脏集为空 ⇒ 本轮零 readConversation、零业务动作
```

假阳性的最大代价 = 一次被频控约束的 intrusive 列表读(且 filter 可用 unread 更省)。用户手动读走消息造成的"假阴性"(徽章清零但有新内容)则由脏检测第 2 条(lastMessage 摘要差)捕获——徽章通道的任何谎言都不触及正确性。

---

## 8. 边界情况

### 8.1 IM 页面标签不存在/被用户关闭

- **失败形态**:驱动命令前置自检不过 ⇒ `ack { accepted:false, reason:"precondition_failed", detail:"no_platform_surface" }`(content script 全无时由 SW 直接代答);极端情况(SW 也刚重生)表现为 ack 超时,走 §6 原 id 重发→探活。
- **脑的反应链**:收到 no_platform_surface ⇒ 若自动化处于用户启动状态 ⇒ 派 `ensurePlatformReady{surface:"im"}`(intrusive,允许 chrome.tabs.create,登录态靠 cookie 复活)⇒ ready 后重派原同步命令。detail="login_required" ⇒ 不自愈,告警转人工(登录永远人工)。
- **前置原语要不要**:要(`ensurePlatformReady` 已入表),但**惰性调用**:不做每轮巡检的固定前导(浪费一跳),只在前置失败子码或 hb.page/hb.sensors 显示无平台面时派发。
- **尊重用户**:同一巡检轮内 ensurePlatformReady 至多派 1 次;连续 N 轮(默认 3)刚建好又被用户关闭 ⇒ 判定用户在赶它走,暂停该账号自动化 + 通知用户,不与用户拉锯。

### 8.2 浏览器整个关闭/手离线

WS 断 + LWT/心跳缺失 ⇒ 健康三级判定接管(§7 草案):停止派发、巡检对该账号挂起、按既有告警策略通知。恢复后 hello 握手,首轮巡检自然对账补齐离线期一切变化——这正是"正确性只在层 3"的收益:离线不需要任何补发机制。

### 8.3 多个智联标签页同时开着

- SW 维护 **canonical 平台标签页**(每账号一个):选择序 = content script 健康且在 IM 面 > 最近活跃 > 最小 tabId;写入 hb.page 供脑观测。
- 一切驱动命令只在 canonical 标签页执行;**非 canonical 标签页传感器静音**(不发事件、不喂 hb.sensors),避免双份徽章事件;其存在仅作 canonical 失效时的接替候选。
- `manualInteraction` 例外:任何平台标签页上的用户输入都上报——用户在哪个标签页忙都应触发静默窗。
- 两个浏览器(两只手)开同一账号:账号串行域跨手唯一在途,脑按健康度选一只手作为该账号执行手;另一只手的事件照收(提示无害)。

### 8.4 夜间与窗口边界

23:59 到达的徽章提示可拉前至 23:59+25s 内开跑;0:00 后调度器窗口关闭,提示只落脏标记;次日 8:00 首轮巡检统一收割(规格:夜间到期一切档位顺延至次日首轮)。心跳与重连整夜照常(基础设施不歇)。

---

## 9. 对协议草案 v0.1 的修改清单(可直接执行)

1. §1.3/§4.1:`class` 枚举扩为 `readonly | intrusive | effectful`;新增归类判据一段(§4.2 的 litmus)。
2. §6 矩阵:新增 intrusive 列(本文 §4.3);"结果未知时只允许下发只读验证" 改写为 "只允许下发无新增副作用的验证读(readonly/intrusive),验证读占用冻结命令槽位"。
3. §4.2 ack、§4.4 result:增加可选 `detail` 机器可读子码,枚举见本文 §6.3。
4. §4.6:事件类型表替换为本文 §6.2(四类,字段与去抖规则成文);明确"事件唯一许可效果 = 调整巡检开跑时刻/静默窗/健康档",禁止事件直触业务命令。
5. §4.7 hb:增加 `sensors` 可选字段(本文 §2.4),并注明"只运缓存现货,禁止心跳触发采集"。
6. §7 能力级信号补一条:hb.sensors 持续为 null 而 hb.page.kind=im ⇒ 传感链路残废,同能力级处置。
7. §9 原语注册:intrusive 类登记要求 = readonly 同款(无 evidenceSchema、无幂等键),另须声明 `platformSideEffect: "idempotent-read-receipt" | "none"` 供审计。
8. §13 未决项两条销案:readConversationList 游标/progress 细化 ⇒ 本文 §5.5/§6.1;无稳定消息 id 的证据匹配 ⇒ 本文 §5.2–§5.4(anchorTail + 身份/状态分离),真机验证项转 §10。

---

## 10. 遗留风险与真机验证清单(按危险度排序)

1. **卡片状态跃迁的可见性**:候选人点开窗口外老卡"接受"时,平台是否必然在会话尾追加系统提示?不追加则该事件漏检,需补"面试列表低频对账"原语(backlog)。真机验证:接受/拒绝各操作一次,观察 DOM 尾部与列表摘要。
2. **session/list API 的字段与语义**:每会话 unreadCount 是否可得、lastMessage 预览的保真度(表情/图片如何呈现)、排序是否严格按最近活跃;以及经 `?sessionId=` 导航打开会话与手点是否同样即时产生已读。任一不成立,对应兜底路径(DOM 读数、脏检测权重)需调整。
3. **对齐歧义残余**:候选人贴尾连发逐字相同消息且窗口边界恰好切在其间时,"最大重叠"策略理论上可静默吸收一条真实消息(静默失败形态,回归测试测不到)。已设审计日志 + 深读升级 + 7 天兜底三道缓冲,真实发生率需真机长跑观察。
4. **参数皆为教育猜测**:去抖 800ms、合并窗 25s、频控 60s、静默窗 45s、8 天列表窗、50 条快照窗——全部走 sensorConfig/脑侧配置下发,真机调参,不影响结构。
5. **绑定的启发式回退**:platformUserRef 取不到时按姓名+职位暂绑有错绑风险,首次 readConversation 必须确证后才允许 effectful 动作(错绑 + 发消息 = 对陌生人说话,最贵事故)。
