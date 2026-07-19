# CLAUDE.md

本仓库是"招聘自动化助手"(RecruitHelper):对旧产品"智联招聘自动化"(工作区 `../AutoZhilian`,五仓库)的全新重构,**不是延续开发**。定位是全新产品、全新安装:不背旧系统的任何二进制与协议契约——machine_id 出生逻辑、端口 17321、`%APPDATA%\ai-assistant-console` 数据目录、electron-updater 更新链、Native Messaging host、旧扩展 ID,一概不保留。旧工作区的一切文档默认过时、仅作考古线索(见"文档信任边界与知识继承")。旧后台(xq-resume-backend)暂不改动,本仓库现阶段完全不连云端。

## 大方向(一切设计决策的根)

1. **脑手模型,主动权在脑。** client(Go 本地服务)是脑,Chrome 插件是手。旧系统是插件自循环、客户端被动应答(领待办、canRun 询问都由插件发起);新系统一切业务节奏(巡检、调度、重试、超时判定)只存在于脑,手只做两件事:执行脑下发的原语命令、上报传感事件。协议按本仓库 **`contract/协议规格-v1.md`** 实施(机器骨架 `contract/contract.v1.json`,推导底稿在 `docs/协议设计/`);旧的 `../AutoZhilian/脑手通信协议设计草案.md` 已退役、仅作历史参考——其"同 id 重发 3 次"等条款已被红队否决,不得照抄。注意区分两个方向:传输连接方向是手→脑(插件主动连 WS、断线重连、心跳),这是脑管理"不可靠的手"的方式;语义方向永远是脑→手。
2. **脑是平台无关的。** 命令语义一律意图级(如 openConversation(candidateRef)),DOM 结构、selector、tabId/windowId、平台专有 ID 不得进入协议语义,只允许出现在 result 的 evidence 与日志字段。验收判据:把智联换成任何别的招聘平台,client 端代码零改动,只改 plugin 的 program 层。
3. **测试与生产运行同一代码,构造性保证。** 插件端唯一入口是命令分发器;客户端测试页与未来的自动调度器都只是"命令生产者",共用同一信封、同一通道、同一分发路径。两端都禁止出现"测试模式"代码分支。

## 工程布局(monorepo)

| 目录 | 内容 |
|---|---|
| `contract/` | 协议契约单一源头:《协议规格-v1.md》(规范文本)+ `contract.v1.json`(codegen 输入,生成 Go 类型与 TS 常量)。协议改动只改这里,禁止两端各自手写对齐 |
| `client/` | Electron 外壳(窗口 + 启停 Go 服务)+ React 最小 UI(状态页、测试页)+ Go 本地服务(逻辑中枢:WS 服务、手注册表、命令派发、GORM + SQLite) |
| `plugin/` | Chrome MV3 插件。base = 稳定壳(manifest、监听注册、WS 连接、心跳/重连、命令分发器);program = 业务模块(原语注册表,普通 TS 模块) |

技术选型:Go 侧 SQLite 用 GORM + glebarez/sqlite(纯 Go 驱动,避免 cgo 拖累 Windows 交叉编译);UI 沿用 React + TypeScript(strict)。本地服务端口新定、可配置,不复用 17321。

## 手的禁令(插件端纪律,骨架期即立法)

1. **不得有业务定时器。** setInterval / chrome.alarms 只允许基础设施用途:WS 重连兜底、心跳保活。什么时候干活由脑决定,感知类工作也以脑下发 readonly 对账命令为主。
2. **不得持久化业务状态。** chrome.storage 只放基础设施数据(连接配置、能力缓存)。手的失忆是设计前提,不是要修的 bug。(远期例外:协议规格 §9 的投递层证词 journal/outbox——四道栅栏下的基础设施数据、允许整体丢失、不构成第二账本,随首个真实副作用原语批次引入。)
3. **一切动作由 cmd 触发。** 传感事件(新消息、页面跳转、登录态变化)只上报不决策;事件是提示不是账本,丢了由脑的对账轮兜底。
4. **手不自作主张重试。** effectful 动作失败就诚实上报 result(带 sideEffectPossible),重试与验证策略全在脑。
5. **监听注册只在 base。** program 保持可远程交付形态:不注册任何 chrome 监听、只经原语注册表暴露能力——这是"交付机制悬置"得以成立的前提。

## 原语契约(手的能力怎么登记)

每个原语按协议草案 §9 注册:`name、argsSchema、class(readonly|effectful)、preconditions、evidenceSchema(effectful 必有)`。调试用原语归 `debug.*` 命名空间,不占用平台无关词汇表。effectful 原语的完成信号是"观察到后置条件"(evidence),"我点了按钮"不算完成。

## 悬置决策(有截止点,不许默默溜过)

- **program 交付机制**:l7eval5 远程下发(秒级热更、ES5 链)vs 编译进插件 + 自托管 CRX 策略强装(现代链、分钟至小时级更新)。骨架期保持机制无关、unpacked 加载;**截止点 = 第一次给真实客户装插件之前**。
- **表结构**:当前只建骨架所需最少表,全部视为临时;正式设计时候选人主键必须带 platform 维度、锚平台 userId(不锚 resumeNumber——旧系统教训)。
- **授权/云端对接**:后置。届时后台不改,client 按现有 `/api/v1/client/*` 冻结契约(bind / heartbeat / job-config)对接。

## 文档信任边界与知识继承

- **唯一信任源是本仓库的文档**:CLAUDE.md(宪法)、`contract/协议规格-v1.md`(协议)、`docs/沟通逻辑规格-v4.md`(沟通行为目标规格,已自旧工作区收编,以本仓库副本为准)、`docs/协议设计/`(推导底稿)。文档冲突时按 CLAUDE.md > 协议规格 > 其余 裁决。
- **旧工作区(`../AutoZhilian`)的一切文档默认过时,不作依据**:只当考古线索。从中取用任何事实(页面结构、接口行为、平台参数)前必须真机验证;经验证的结论固化进本仓库文档后方可引用。新系统真正依赖的旧文档,应收编进本仓库成为正式副本,不跨目录引用。
- **代码级对抗知识照旧是搬运素材,但同样不盲信**:可以主动参考旧项目实现各种功能的方式,但它们只进入候选方案池;必须结合新架构约束、当前真实页面和自动化证据独立判断,只有确实更好的部分才迁入。旧 program 约 1 万行 DOM 页面对抗代码与 `../AutoZhilian/html定位/` DOM 样本,搬运时以真实页面为准逐条核对(重写不等于重踩坑,但旧注释与旧文档不是证据)。
- 几条已固化进协议规格的红线(此处仅提醒):宁可少发,核验失败不自动重发、只转人工;AI 是建议者不是执行者,状态推进由确定性代码裁决;沉默锚点只算真正已发出的消息。

## 全局约定

- 界面文案与交流全部中文;源码不使用 emoji。
- TypeScript 开 strict;协议类型一律来自 contract codegen,禁止手写重复定义。
- 常用命令(仓库根目录执行):
  - `go run ./contract/codegen` — 从 contract.v1.json 重新生成两端协议代码到 `contract/gen/`;**改契约后必跑**,产物一并提交。
  - `go run ./contract/codegen -check` — 校验产物与契约一致(CI 门禁,漂移退出码 1)。
  - `go run ./client/service` — 起脑服务(`-port` 默认 17872,`-data` 默认 `data/`,已 gitignore)。
  - `go test ./...` / `go test -race ./client/service/...` — 全部 Go 测试 / 竞态。
  - **JS 包管理用 pnpm workspace**(根 `pnpm-workspace.yaml` 统管 plugin / client/ui / client/electron)。仓库根 `pnpm install` 一次装全部;`pnpm approve-builds` 已固化到根 package.json 的 `onlyBuiltDependencies`(esbuild/electron 原生二进制)。禁止再引入 npm/yarn 锁文件。
  - `cd plugin && pnpm build` — 打包手端插件到 `plugin/dist`(unpacked 加载目录);`pnpm typecheck` strict;`pnpm test:node` 连真脑端到端(需先起脑)。
  - `cd client/ui && pnpm dev` — 起 UI(Vite,5273);`pnpm build` 构建;`pnpm test:node` 数据层连真脑。
  - `cd client/electron && pnpm start` — Electron 壳(启停脑 + 开窗);`pnpm test:node` 服务生命周期。
  - 人眼验收操作:见 `scripts/dev-run.md`;§16 验收对照见 `docs/里程碑1-验收报告.md`。
- Go 侧 SQLite 走 glebarez/sqlite(纯 Go);**禁止引入 cgo 依赖**,`CGO_ENABLED=0 GOOS=windows go build ./client/service` 必须常年通过(Windows 交叉编译是发布路径)。
- SQLite 写靠 `SetMaxOpenConns(1)` 串行化(SQLite 单写;红队复现过并发 BUSY 静默丢结果→双发的致命链)。
- 协议实现:唯一规范源 `contract/协议规格-v1.md`;里程碑 1 验收记录见 `docs/里程碑1-验收报告.md`。
