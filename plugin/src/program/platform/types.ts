// 平台适配层的类型面(program 层)。
//
// 立此文件之前,「智联」是被 27 处 `if (platform !== 'zhilian') throw` 硬编码进
// 每一个原语的;第二个平台的任何一条命令都会在这 27 个点上被逐一拒绝。本文件
// 把那 27 份拷贝收敛成一个可注册的适配器接口:**加一个平台 = 新增一个适配器
// 文件,不在既有文件里插 if**。
//
// 三条纪律,直接来自宪法,别在这里放松:
//
//   1. 方法面严格等于原语面(35 个),不发明「通用 DOM 操作层」。原语已经是
//      意图级的正确粒度(大方向 2);再往下抽就是拿单一实现猜抽象,正是
//      docs/多平台扩展-设计与风险.md 点名的陷阱。
//   2. DOM、selector、tabId、平台专有 id 一律不进本文件的类型(协议规格 §1
//      公理 7、反模式 3)。这里出现的全部是契约里已有的意图级类型。
//   3. 未实现的能力必须显式拒绝,不得默认回成功(反模式 18)。
import type {
  AccountReadWechatSettingArgs,
  AccountReadWechatSettingData,
  CandidateApplySourcingFiltersArgs,
  CandidateApplySourcingFiltersData,
  CandidateCaptureResumeScreenshotArgs,
  CandidateReadCurrentArgs,
  CandidateReadCurrentData,
  CandidateReadResumeArgs,
  CandidateReadResumeData,
  CandidateReadSourcingResumeArgs,
  CandidateReadSourcingResumeData,
  CandidateReadSourcingTargetResumeArgs,
  CandidateReadSourcingWindowArgs,
  CandidateReadSourcingWindowData,
  CandidateSelectSourcingPositionArgs,
  CandidateSelectSourcingPositionData,
  CaptureScreenshotData,
  ChatAcceptWechatArgs,
  ChatAcceptWechatData,
  ChatCaptureThreadScreenshotArgs,
  ChatIdentifyCurrentConversationArgs,
  ChatIdentifyCurrentConversationData,
  ChatOpenConversationArgs,
  ChatOpenConversationData,
  ChatReadGreetingOutcomeArgs,
  ChatReadGreetingOutcomeData,
  ChatReadListArgs,
  ChatReadListData,
  ChatReadPeerPhoneArgs,
  ChatReadPeerPhoneData,
  ChatReadThreadArgs,
  ChatReadThreadData,
  ChatReadUnreadTotalArgs,
  ChatReadUnreadTotalData,
  ChatReadWechatExchangeOutcomeArgs,
  ChatReadWechatExchangeOutcomeData,
  ChatSendGreetingArgs,
  ChatSendGreetingData,
  ChatSendGreetingGuards,
  ChatSendInviteCardArgs,
  ChatSendInviteCardData,
  ChatSendMessageArgs,
  ChatSendMessageData,
  ChatSendMessageGuards,
  ChatSendWechatInviteArgs,
  ChatSendWechatInviteData,
  DebugCapturePageArgs,
  DebugInspectSendSurfaceArgs,
  DebugInspectSendSurfaceData,
  DebugProbeInterviewEditorArgs,
  DebugProbeInterviewEditorData,
  ErrorCode,
  JobPrepareDraftArgs,
  JobPrepareDraftData,
  JobPublishDraftData,
  JobPublishDraftGuards,
  JobReadClassCandidatesArgs,
  JobReadClassCandidatesData,
  JobReadKeywordVocabularyArgs,
  JobReadKeywordVocabularyData,
  JobReadPublishedListArgs,
  JobReadPublishedListData,
  JobTakeOfflineArgs,
  JobTakeOfflineData,
  JobTakeOfflineGuards,
  NavEnsureSurfaceArgs,
  NavEnsureSurfaceData,
  NotReadyReason,
  ProbePlatformArgs,
  ProbePlatformData,
  Retryable,
  SideEffect,
} from '../../base/protocol'
import type { PrimitiveContext } from '../registry'

/**
 * 平台标识。脑对它不透明——协议 `CmdContext.platform` 是自由字符串,脑侧
 * 明确不维护平台枚举(见 client/service/internal/adminhttp/m2.go 的
 * validatePlatform)。手侧同样不该建枚举,注册了哪个就是哪个。
 */
export type PlatformId = string

/**
 * 页面代码的执行世界。
 *
 * 智联声明 `MAIN`,**原因是感知不是动作**:读会话消息数组要经页面的 Vue 实例
 * 拿 Vuex getter,那个属性在 isolated world 不可见(事实见
 * docs/点击输入通道选型-2026-08-11.md §5.1)。
 *
 * 有的平台会检测主世界痕迹,只能用 `ISOLATED` ——那意味着**没有页面状态通道**,
 * 一切感知只能从 DOM 爬。所以这不是一个可以想当然的开关:同一个契约 data 结构,
 * 两种世界下的可信度与失效模式完全不同,适配器实现必须各自承担。
 */
export type ExecutionWorld = 'MAIN' | 'ISOLATED'

/**
 * 点击与打字的派发通道。
 *
 * **本轮只有 `intrinsic` 有实现**;`cdp` 与 `os` 是给输入通道战役占的位置,
 * 选型与实测依据见 docs/点击输入通道选型-2026-08-11.md,当前均未裁决。
 *
 * - `intrinsic` 页面内合成事件(`element.click()`)。快,但 `isTrusted` 为假。
 * - `cdp`       chrome.debugger 派发真实输入事件。
 * - `os`        操作系统级键鼠注入。一次点击含完整鼠标移动,**耗时从毫秒级
 *               变成秒级**——凡按此通道实现的平台,原语的时间预算必须重估
 *               (协议 `execBudgetMs` 上限 240000 是两端硬校验)。
 *
 * 在这里只是一个声明。真要接 `cdp`/`os`,先看反模式 11(过程式命令)——
 * 「移到 (x,y) 点一下」不是声明式命令,那一关得甲方裁决,不是实现细节。
 */
export type InputChannel = 'intrinsic' | 'cdp' | 'os'

/**
 * 平台层的失败。
 *
 * 它替代了原来的 `ZhilianPlatformError`(该名字在 zhilian.ts 里仍作别名导出,
 * 378 处构造点因此一个字都不用改)。字段语义原样保留:
 *
 * - `code`       契约 ErrorCode,进 result.error.code
 * - `retryable`  重试语义,脑据此裁决,手不自作主张(手的禁令 4)
 * - `reason`     notReady 原因,进 error.data.reason
 * - `sideEffect` **effectful 必须如实**:点击前 none、点击后 possible。
 *                readonly/intrusive 没有资格用 possible/confirmed。
 * - `diagnostics` 失败现场快照,只给人读,不参与任何业务判定
 */
export class PlatformError extends Error {
  constructor(
    readonly code: ErrorCode,
    message: string,
    readonly retryable: Retryable = 'afterRecovery',
    readonly reason?: NotReadyReason,
    readonly sideEffect: SideEffect = 'none',
    readonly diagnostics?: Record<string, unknown>,
  ) {
    super(message)
    this.name = 'PlatformError'
  }
}

/**
 * 适配器方法的统一入参。
 *
 * 刻意用具名字段而不是位置参数:35 个方法原本有 5 种位置约定
 * (`()`、`(fingerprint)`、`(ctx, fingerprint)`、`(args, ctx, fingerprint)`、
 * `(args, guards, ctx, fingerprint)`),第二个平台照抄这 5 种约定毫无道理,
 * 而具名字段让「参数传错位置」这类错误在构造上不可能发生。
 */
export interface PrimitiveInput<A, G = undefined> {
  readonly args: A
  readonly guards: G
  readonly ctx: PrimitiveContext
  /** 脑下发的账号身份指纹。适配器据此核对「现在登录的还是同一个人」。 */
  readonly fingerprint: string | undefined
}

/**
 * **全部平台能力的并集**,一条原语一个方法。
 *
 * 注意这里的集合关系:平台之间是**相交但互不包含**——各家都有对方没有的东西,
 * 不存在「谁是谁的子集」。所以本接口不是「某个平台的完整能力」,而是所有平台
 * 各自那一份的并集;每个平台只挑自己有的实现,没实现的由 `requireCapability`
 * 在运行期显式拒绝。
 *
 * 由此有两条纪律:
 *
 *   1. **不要给任何平台加编译期完备性断言。** 适配器一律用 `satisfies
 *      PlatformAdapter`,只校验「写了的那些签名对不对、有没有写错名字」,不校验
 *      「写全了没有」。若改成要求全量实现,新平台带来一条独有能力,就会把既有
 *      平台一起搞到编译不过——而那条能力跟既有平台根本无关。
 *      「某平台该有哪些」是那个平台自己的事实,由它自己的用例断言
 *      (见 test/unit.mjs 里智联的 35 条清单)。
 *   2. 往这里加方法**等于加一条原语**,必须先走契约(新原语名、args/data schema、
 *      PRIMITIVE_META),不能只在本文件加一行。契约的原语表就是这个并集。
 */
export interface PlatformCapabilities {
  // —— 探测与导航 ——
  probePlatform(input: PrimitiveInput<ProbePlatformArgs>): Promise<ProbePlatformData>
  ensureSurface(input: PrimitiveInput<NavEnsureSurfaceArgs>): Promise<NavEnsureSurfaceData>

  // —— 账号 ——
  readWechatSetting(
    input: PrimitiveInput<AccountReadWechatSettingArgs>,
  ): Promise<AccountReadWechatSettingData>

  // —— 会话感知 ——
  readList(input: PrimitiveInput<ChatReadListArgs>): Promise<ChatReadListData>
  readThread(input: PrimitiveInput<ChatReadThreadArgs>): Promise<ChatReadThreadData>
  readUnreadTotal(input: PrimitiveInput<ChatReadUnreadTotalArgs>): Promise<ChatReadUnreadTotalData>
  identifyCurrentConversation(
    input: PrimitiveInput<ChatIdentifyCurrentConversationArgs>,
  ): Promise<ChatIdentifyCurrentConversationData>
  openConversation(
    input: PrimitiveInput<ChatOpenConversationArgs>,
  ): Promise<ChatOpenConversationData>

  // —— 候选人与简历 ——
  readCurrentCandidate(
    input: PrimitiveInput<CandidateReadCurrentArgs>,
  ): Promise<CandidateReadCurrentData>
  readResume(input: PrimitiveInput<CandidateReadResumeArgs>): Promise<CandidateReadResumeData>

  // —— 采集 ——
  selectSourcingPosition(
    input: PrimitiveInput<CandidateSelectSourcingPositionArgs>,
  ): Promise<CandidateSelectSourcingPositionData>
  applySourcingFilters(
    input: PrimitiveInput<CandidateApplySourcingFiltersArgs>,
  ): Promise<CandidateApplySourcingFiltersData>
  readSourcingWindow(
    input: PrimitiveInput<CandidateReadSourcingWindowArgs>,
  ): Promise<CandidateReadSourcingWindowData>
  readSourcingResume(
    input: PrimitiveInput<CandidateReadSourcingResumeArgs>,
  ): Promise<CandidateReadSourcingResumeData>
  readSourcingTargetResume(
    input: PrimitiveInput<CandidateReadSourcingTargetResumeArgs>,
  ): Promise<CandidateReadSourcingResumeData>

  // —— 真实外部副作用(effectful)。手侧原语内一律不重试(手的禁令 4)。——
  sendGreeting(
    input: PrimitiveInput<ChatSendGreetingArgs, ChatSendGreetingGuards>,
  ): Promise<ChatSendGreetingData>
  sendMessage(
    input: PrimitiveInput<ChatSendMessageArgs, ChatSendMessageGuards>,
  ): Promise<ChatSendMessageData>
  sendWechatInvite(
    input: PrimitiveInput<ChatSendWechatInviteArgs, ChatSendMessageGuards>,
  ): Promise<ChatSendWechatInviteData>
  acceptWechat(
    input: PrimitiveInput<ChatAcceptWechatArgs, ChatSendMessageGuards>,
  ): Promise<ChatAcceptWechatData>
  sendInviteCard(
    input: PrimitiveInput<ChatSendInviteCardArgs, ChatSendMessageGuards>,
  ): Promise<ChatSendInviteCardData>

  // —— 副作用的配套验证读 ——
  readGreetingOutcome(
    input: PrimitiveInput<ChatReadGreetingOutcomeArgs>,
  ): Promise<ChatReadGreetingOutcomeData>
  readWechatExchangeOutcome(
    input: PrimitiveInput<ChatReadWechatExchangeOutcomeArgs>,
  ): Promise<ChatReadWechatExchangeOutcomeData>

  // —— 取证 ——
  captureThreadScreenshot(
    input: PrimitiveInput<ChatCaptureThreadScreenshotArgs>,
  ): Promise<CaptureScreenshotData>
  captureResumeScreenshot(
    input: PrimitiveInput<CandidateCaptureResumeScreenshotArgs>,
  ): Promise<CaptureScreenshotData>
  readPeerPhone(input: PrimitiveInput<ChatReadPeerPhoneArgs>): Promise<ChatReadPeerPhoneData>
  revealPeerPhone(input: PrimitiveInput<ChatReadPeerPhoneArgs>): Promise<ChatReadPeerPhoneData>

  // —— 职位发布 ——
  readPublishedJobs(
    input: PrimitiveInput<JobReadPublishedListArgs>,
  ): Promise<JobReadPublishedListData>
  readJobClassCandidates(
    input: PrimitiveInput<JobReadClassCandidatesArgs>,
  ): Promise<JobReadClassCandidatesData>
  readJobKeywordVocabulary(
    input: PrimitiveInput<JobReadKeywordVocabularyArgs>,
  ): Promise<JobReadKeywordVocabularyData>
  prepareJobDraft(input: PrimitiveInput<JobPrepareDraftArgs>): Promise<JobPrepareDraftData>
  publishJobDraft(
    input: PrimitiveInput<JobPrepareDraftArgs, JobPublishDraftGuards>,
  ): Promise<JobPublishDraftData>
  takeJobOffline(
    input: PrimitiveInput<JobTakeOfflineArgs, JobTakeOfflineGuards>,
  ): Promise<JobTakeOfflineData>

  // —— 开发期诊断 ——
  inspectSendSurface(
    input: PrimitiveInput<DebugInspectSendSurfaceArgs>,
  ): Promise<DebugInspectSendSurfaceData>
  probeInterviewEditor(
    input: PrimitiveInput<DebugProbeInterviewEditorArgs>,
  ): Promise<DebugProbeInterviewEditorData>
  capturePageSnapshot(input: PrimitiveInput<DebugCapturePageArgs>): Promise<CaptureScreenshotData>
}

/** 能力名。`requireCapability` 用它做键。 */
export type CapabilityName = keyof PlatformCapabilities

/**
 * 一个已注册的平台。
 *
 * 元数据四项是**声明**,不是实现:它们让「这个平台怎么碰页面」有一个地方可查,
 * 而不是散在 91 处注入与 51 处点击里。
 */
export interface PlatformAdapter extends Partial<PlatformCapabilities> {
  /** 与脑下发的 `cmd.context.platform` 逐字相等。 */
  readonly id: PlatformId
  /** 该平台页面的 URL 匹配式(chrome.tabs.query / content_scripts 口径)。 */
  readonly hostMatch: string
  readonly world: ExecutionWorld
  readonly input: InputChannel
}

/**
 * 平台适配器的声明方式,写死在这里当范本:
 *
 * ```ts
 * export const fooAdapter = {
 *   id: 'foo', hostMatch: '...', world: 'ISOLATED', input: 'os',
 *   readList: (input) => ...,   // 只写这个平台真有的
 * } satisfies PlatformAdapter
 * ```
 *
 * 用 `satisfies` 而不是 `: PlatformAdapter`,有两个好处:签名写错、方法名拼错
 * 都在编译期红;同时不要求写全,新平台的独有能力不会波及既有平台。
 */
export type PlatformAdapterShape = PlatformAdapter
