# D · 规格对抗审查(红队终审)

> 2026-07-16。审查对象:`contract/协议规格-v1.md`(2026-07-16 仲裁合成稿)与 `contract/contract.v1.json`;对照物:CLAUDE.md 宪法、A/B/C 三卷底稿。
> 方法:沿六个攻击面(双发安全时序、规格与契约互查、M1 可实现性、宪法合规、合成丢失、模糊地带)逐项推演;只报能构造出具体失败时序或文本矛盾证据的缺陷,"感觉不妥"一律不报。
> 总评先行:合成稿的骨架(先记账后发送、bootId 一刀切、三分类、suspect 五法条、三道闸)是成立的;手的五条禁令逐条核查未见实质违反;A/B/C 三卷的承重机制绝大多数有落点或有显式裁决。真正的问题集中在**重连重传这条最要命的路径上,规格自己的五处条款互相咬**(致命一),以及一批"实现者被迫发明语义"的洞,它们恰好都埋在 effectful 生命周期与 M1 验收清单正中央。
> 历史边界（2026-07-17）：本卷审查的是 2026-07-16 版本；F6 及其他配对等待期结论保留为历史证据，现已由 `contract/协议规格-v1.md` §2.2 的本地可信即时握手取代。

统计:致命 1 条,重要 6 条,次要 4 条,编辑性 4 条。

---

## 致命

### F1【重连后重传帧的 session 语义缺失,§3/§7.2/§7.3/§8.1/§10 五处互相矛盾,验收 6 按字面不可实现】

失败时序(全部引用现行文本,不引入任何假设):

1. 脑在会话 S1 派发 cmd(msgId=M,effectful),手 ack(accepted),开始执行(或已执行完、result 在内存队列)。
2. WS 闪断,SW 存活。按 §7.3"断线不弃队",M 继续执行。
3. 手重连,hello.bootId 未变,welcome 分配新会话 S2。
4. 按 §7.2 规则 2,脑对 M 原 msgId 重发。重发帧的 session 字段是什么?§3 定义 session="消息**创建时**所属会话",且明确列出重传时可变字段只有 ts("重传时更新")与 attempt——**session 不在其中**。A 卷 §2 原有"重传时可变字段:ts、attempt、session(命令跨会话重投时更新为当前会话)",合成时这半句被丢掉了。按合成稿字面,重发帧携带 S1。
5. 手收到 S1 帧。§3:"命令只在 session=手当前会话时可执行"→ 回 ack(rejected, STALE_SESSION)。而验收 6 要求的是"重连 bootId 未变→原 msgId 重发→手 duplicate ack(或补 result)"。**规范文本与验收判据直接冲突。**
6. 矛盾继续向上传导:§8.1 状态机"sent --ack(rejected)--> rejected(终局)",但 §10 错误码表说 STALE_SESSION retryable=yes(新会话重投)——同一事件一处说终局、一处说可重试,无优先级裁决。
7. 最毒的一环:§4.4 断言 ack 层错误"恒 sideEffect=none",而本时序里被 STALE_SESSION 拒掉的是一条**正在执行或已产生副作用**的命令的重复帧。若实现者信了"rejected=零副作用的失败终局",[X] 时代业务层就获得了为同一意图重新铸造命令的许可(§7.5 闸一只拦"非失败终局或 suspect",不拦 rejected)——此时只剩 journal 和 guards 两道闸,"三道闸同时失效才会双发"的论证被削掉了权威的一道。

同一缺陷的另一个面:手侧管线的检查顺序(msgId 去重 vs session 校验 vs caps)在合成稿里完全没写。A 卷 §4.2 有明确顺序(session 校验先于 msgId 去重),若按 A 卷顺序实现,上述 STALE_SESSION 分支必然发生;§7.3 宣称的"这是规则 7.2 与去重表的自洽点"只有在"去重命中先于 session 校验"或"重传帧更新 session"二者至少一个成立时才真自洽,而两者都没写。另外"命令只在当前会话可执行"的判定时点(受理时还是出队时)也未定义:若出队时复查 session,断线期间入队的 S1 命令在重连后出队会全部变 STALE——裁决 #10"断线不弃队"被后门废除。

建议修法:恢复 A 卷条款"重传时 session 更新为手当前会话";明文规定手侧管线顺序为"信封校验 → msgId 去重(命中即 duplicate,终局则重放,**不再看 session**)→ session 校验 → caps → 队列";明确 session 围栏只在受理时刻生效,已 accepted 的命令跨会话继续执行;状态机为 STALE_SESSION 拒绝加"回到待重投"的非终局分支(或规定该码不落 rejected 终局)。

---

## 重要

### F2【v1 的 suspect"超时无 result"定时器没有锚点,"execBudget+缓冲"按字面不可实现,串行队列等待会制造假 suspect】

证据与时序:§8.2 的触发条件是"无 result 超 execBudget+缓冲"——"缓冲"全文无定义、不在 §17 参数表、不在 contract.defaults;更关键的是起算点无定义。execBudgetMs 的定义是"手侧**单次执行**预算"(从出队起算),而 v1 无 progress、无租约,脑侧无法直接观测出队时刻。若实现者从派发时刻起算(唯一天然可选的锚点):手侧全局严格串行,两条 debug.slowEcho(ms=200s)先后入队(debug 无 context,不受账号串行域约束,完全合法),第二条在队列里等 200 秒、执行 200 秒,t=400s 才有 result,而脑在 240s+缓冲处已将其判 suspect——冻结幂等键、告警、进人工队列,随后真 result 到达(转 F3 的无规则地带)。假 suspect 在 M1 验收环境即可稳定复现。附带矛盾:§16 推迟表把"progress/租约"推到"首个 >15s 原语",而 M1 自己交付的 slowEcho 就是最长 200s 的原语——推迟触发条件在定稿当天已被击穿。

建议修法:v1 明文"脑侧对 effectful 的唯一超时定时器 = deadline(绝对时刻)",删除"execBudget+缓冲"这个第二定时器;或规定 exec 计时自 ping.inFlight 首次携带该 msgId 起算,且"缓冲"值入 contract 默认表。推迟表措辞改为"首个 >15s **业务**原语"。

### F3【suspect(终局)与迟到 result 的赛跑没有裁决规则;人工裁决与重连对账之间无互斥】

失败时序:slowEcho(或 [X] 的真实发送)已执行完,result 在手侧内存队列;脑重启,启动扫描按 §8.1 把该命令判 suspect(终局)并进人工队列;数秒后手重连冲队,result(ok) 到达——规格对"终局态收到迟到终局"零规定(cancel 有"result 赢"条款,suspect 没有)。三种实现都说得通且后果不同:忽略(账本永久失真,ok 被记成 suspect,人工工单白跑)、覆盖(违反"suspect 是自动化终态"字面)、报错。更糟的组合:人工在 result 到达前按法条 5 裁"确认未发生→解锁允许重派",与迟到 result(ok) 并发——[X] 时代若人工同时选择"铸新意图"(新 idemKey,journal 不拦、guards 是最后一道),规格亲手制造了"人工误判+自动执行"的双发窗口。v1 每次脑重启带一条在途 slowEcho 都会踩进这个无规则地带,这是验收 5 的必经路径。

建议修法:立"迟到终局自动核销 suspect"条款(与 cancel 的"result 赢"同构,落审计);并规定 suspect 的人工裁决入口在"手已重连且 result 队列冲完(或手 DOWN 超阈值)"之前置灰——对账未完成不许人裁。

### F4【debug/无 context 命令与 effectful 契约的三角矛盾:idemKey 格式、账号串行域、冻结域三者对 slowEcho 都无解】

证据:(1) contract 规定 effectful idemKey 必填,格式硬编码 `ik1:{platform}:{accountRef}:{primitive}:{targetRef}:{intentId}`,intentId 是"脑 SQLite 意图主键";(2) §4.3 说 debug.* 的 context 可缺省——slowEcho 没有 platform/accountRef,测试页也不产生业务意图行;(3) §6 要求 intrusive/effectful 进"账号串行域",suspect 法条 4 要"冻结账号串行域"——slowEcho/switchWindow 无账号,进哪个域、冻结哪个域?验收 7(幂等键冻结)与验收 5/6(slowEcho 转 suspect)的每一步都踩在这三处空白上,实现者必须自己发明格式与域语义——这正是规格自己定义的"未来字段漂移点"。顺带:若 debug 命令完全不进任何串行域,F2 的双 slowEcho 排队时序就是合法输入。

建议修法:契约明文保留串行域 `debug`(每手一个),debug effectful 的 idemKey 允许 `ik1:debug:{handId}:{primitive}:{targetRef}:{测试页生成的 intentId}` 形态;suspect 冻结域对 debug 即冻结该 debug 域。

### F5【ack(rejected) 之后的重试语义三方打架:retryable=yes vs 反模式 19 vs effectful 永不重派 vs 去重表收录规则】

证据链:QUEUE_FULL 的 retryable=yes(退避)——退避后重试必然发生在**同一条存活连接**上(没有发生任何断连):同 msgId 重发直接违反反模式 19/§7.2"同一条存活 TCP 上永不重发帧";换新 msgId 对 effectful 又撞上 §6"永不自动重派"的字面。STALE_SESSION 的"yes(新会话重投)"同理依赖同 msgId 再投。而"被拒命令的 msgId 是否已进手侧去重表"全文未定义:若实现者在收到帧时即入表(常见实现),同 msgId 再投将永远命中 duplicate 且无终局可重放——脑等不到 result,effectful 走"超时无 result"进假 suspect。三条规则加一个未定义点,四方互锁,自动重试路径在 v1 无一条能走通。

建议修法:明文三条——反模式 19 只约束"未收到任何 ack 的帧";ack(rejected) 的命令不入去重表、允许同 msgId 修正后再投;effectful 的"永不自动重派"精确化为"无终局信号的歧义下永不重派",ack 层干净拒绝(恒零副作用)不属歧义。

### F6【配对流程在 MV3 下自毁:等待用户确认期间零收发,SW 约 30 秒必死,60 秒配对窗后半程不可用;hello 无应答超时未定义】

失败时序:用户点"配对",脑开 60s 窗;t=0 手连接、发 hello(handId=null),挂起等 welcome;待配对期间连接上没有任何帧往来——环境基线明说"**活跃 WS 收发**重置 30 秒空闲计时",光开着不算;ping 不可用(hb 参数来自 welcome,§17 规定手侧硬编码仅重连退避与看门狗;且 ping 需携带 session,此刻无 session 可填)。t≈30s SW 被杀、连接断,脑侧待配对条目消失;t=35s 用户点确认,welcome 无处投递;手侧 alarms 看门狗最迟 60s 后才复活重连,配对窗大概率已过(PAIRING_TIMEOUT)。验收 4 在"用户看一眼 Origin 再点确认"这种正常速度下就会间歇性失败。另外 hello 发出后无应答(既无 welcome 也无 bye)时,手等多久、等待期间是否退避重连,全文未定义。

建议修法:待配对期间由脑周期性(<25s)向该连接发无语义保活帧(或允许 pre-session 的 ping/pong,session 允许为 null);给 hello 定应答超时(如 10s 无 welcome/bye 即断开重试),待配对状态由脑侧跨连接保持(以 Origin+bootId 关联重连)。

### F7【参数权威已分裂:§17 与 contract 对 debug deadline 相差 2 至 5 倍,且 §17 的多组默认值根本不在自称权威的 contract 默认表里】

证据:§17 宣称"参数唯一权威副本在 contract 默认值表",但 execBudget 类默认(30s/30s/60s)与 deadline 默认(debug 类 60s;巡检类 ≤2×execBudget;SX 10min)在 contract.defaults 中一概不存在;contract 里的 deadline 只有原语级:debug.ping=30s、debug.switchWindow=30s、debug.slowEcho=300s——与 §17 的"debug 类 60s"两个方向都对不上(2 倍与 5 倍)。按 §17 的 60s 实现,slowEcho(ms>60s) 在出队双查时永远 expired,验收 6/7 的 suspect/幂等冻结演练直接做不了。这是宪法点名要灭的"两处可以对不上"病,在两份规范文件出生当天已经发病。

建议修法:把类级 execBudget/deadline 默认搬进 contract.defaults;删除或改正 §17 的"debug 类 60s"为"以 contract 原语级 deadlineMs 为准";CI 里加一条"§17 表与 contract.defaults 逐项一致"的文档检查。

---

## 次要

### F8【状态机缺边+迟到消息无总则:ack(duplicate) 无转移;result 到达 void/rejected 终局无处置;§8.3.1 触发清单漏了 deadline】

证据:§8.1 只有 sent--ack(accepted)/ack(rejected) 两条边,重传后的正常应答 ack(duplicate) 没有转移(实现者需自行发明"视同 accepted");脑重启把 R/I 作废(void)后,SW 存活的手仍会执行完并跨会话补投 result——终局态收到 result 无任何规定(至少应审计,这是"账本是手所见超集"断言的观测点);§8.1 图里"deadline 过→effectful suspect"在 §8.3.1 的 suspect 触发四条清单里缺席,两处清单不同步。建议:补 ack(duplicate)=视同 accepted 的边;立"任何终局态收到迟到帧一律审计不静默"总则;§8.3.1 补第五条。

### F9【同一"未就绪原因"两套词表已冻结进 contract:ping.contexts.reason 与 CTX_NOT_READY.data.reason】

证据:contract 中 ping.contexts[].reason = `pageAbsent|loggedOut|pageBroken|unknown`,CTX_NOT_READY.dataReason = `pageAbsent|loginRequired|contentScriptDead`——同一概念(页面为何不可用)在同一份契约里两套枚举(loggedOut vs loginRequired、pageBroken vs contentScriptDead)。[S] 实装时脑的健康模型必须在两套词间写映射,别名漂移(宪法认定的 26% 事故根)在单一源头文件内部复活。建议:统一为一套 reason 枚举,两处引用同一 enumRef。

### F10【result 方向的 duplicate ack 与手侧删除规则不闭合,可造成结果队列永不清空】

时序:手补投 result,脑 processed 表命中,按 §4.4"重复到达必须重新 ack(duplicate)"回 duplicate;而手侧删除规则只写了"对 result:**accepted**=脑已持久化,手可从内存队列删除"。字面实现下 duplicate 不触发删除,该 result 留队,每次重连再冲、再 duplicate,循环到 SW 死亡为止(日志噪声+队列占位,50 条上限下可能挤掉真 result)。建议:明文"对 result 的 accepted 与 duplicate 等价,皆可删除"。

### F11【宪法仍指定已退役的旧草案为实施依据,两个"唯一规范源"并存】

证据:CLAUDE.md 大方向 1 原文"协议按 `../AutoZhilian/脑手通信协议设计草案.md`(v0.1)实施",而本规格宣布该草案"自本文起退役"。宪法效力高于规格;且旧草案含有 B 卷已判死刑的"同 id 重发 3 次"条款(双发事故的协议级入口)——按宪法字面行事的实现者(或 agent)会把毒条款搬回来。建议:修订 CLAUDE.md 该句指向 `contract/协议规格-v1.md`。

---

## 编辑性

### F12【§7.2.1"一次 ackTimeout 即关连接"与 §11"连续 3 条命令 ackTimeout → HAND_WEDGED:关连接"表述打架】

若每次 ackTimeout 已关连接,"连续 3 条且连接在、ping 在"何以成立?应写明:3 次是跨重连周期累计(或同批并发命令一次性超时),HAND_WEDGED 的动作是告警转人工,而非再关一次已关的连接。

### F13【错误码批次标记与 contract 漂移;CANCELED_BY_BRAIN 的 retryable="none" 是非法枚举值】

规格头部规则"未标记条款属 [M1]"套在 §10 表上,使 TARGET_NOT_FOUND/ELEMENT_UNRESOLVED/CTX_LOST_DURING_EXEC 读作 M1,而 contract 标 S;PLATFORM_LIMIT/POSTCONDITION_UNCONFIRMED 同理(contract 标 X)。另 contract 中 CANCELED_BY_BRAIN 的 retryable 写作 "none",不在 `yes|no|afterRecovery|manualOnly` 枚举内(规格表该格是"—")。给 §10 每行补批次标,修正契约枚举值。

### F14【welcome 的 blob 字段:contract 有占位、§13 说"经 welcome 下发",§4.2 的 welcome 定义却无此字段】

三处两种说法,[X] 实装时字段位置靠回忆。§4.2 body 表补一行 `blob [X]` 占位即可。

### F15【三处"既没落点也没显式裁决/无使用场景"的合成残渣】

(1) A 卷 welcome.brain.epoch(脑世代号钩子)被静默丢弃,附录一裁决 3 只裁了 session/bootId/msgId 级围栏,未提脑世代号——虽推不出失败场景(手不消费),按"要么落点要么裁决"的合成纪律应在附录补一句;(2) bye 声明为双向,但手→脑方向全文无场景无码(caps 热更重连是最近的候选场景,规格只说"断线重连"),应明确"手直接关连接不发 bye"或给出手侧码;(3) §16 推迟表"首个 >15s 原语"措辞(见 F2)。

---

## 附:已推演未立案的时序(证明其安全,防止复查重复劳动)

- SW 死于 effectful 执行窗任意点 + bootId 换代:§7.2.3 禁重传、§8.3 进 suspect,方向恒"少发+人工",成立。
- 脑重启 + 手侧 result 补投:先记账后发送 + processed 表去重,不丢不重,成立(遗留的是 F3 的 suspect 赛跑)。
- 手侧去重 LRU 256 驱逐重开重执行窗:重传只发生在重连后一瞬,派发到重传之间不可能插入 256 条命令,不可构造,不立案。
- 断线期间在途 intrusive 继续驱动页面 vs 手动静默窗:账号串行域保证断线时每账号至多一条在途,暴露面与"在途不撤销"条款等同,不立案。
- 手生成 msgId 重复污染 processed 表:后果是丢终局→suspect/重派,方向安全(少发),恶意手在威胁模型外,不立案。
- deadline 脑钟 vs 手钟:同机共钟,跳变对双方同向,过期判定失真方向是"提前 expired"(安全),不立案。
