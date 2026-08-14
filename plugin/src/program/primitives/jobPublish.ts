// 职位发布四个原语。页面驱动全部留在智联 program;本模块只把 generated 契约接到
// 唯一注册表。
//
//   job.readPublishedList      intrusive  只读平台已存在的职位名,供发布前判同名
//   job.readClassCandidates    intrusive  填三项后读回平台给的职位类别候选全集
//   job.readKeywordVocabulary  intrusive  定完类别后读回关键词弹层的分组词库
//   job.prepareDraft           intrusive  试填并回读,填完主动离开表单,绝不提交
//   job.publishDraft           effectful  真正发布,唯一一次点击 + 平台列表正证
//   job.takeOffline            effectful  把已在线职位下线,入口 + 二次确认各一次点击
//
// 前四个不创建、不编辑、不上下线任何职位;只有后两个产生对外副作用。
import {
  CmdClass,
  JobPrepareDraftArgs,
  JobPublishDraftGuards,
  JobReadClassCandidatesArgs,
  JobReadKeywordVocabularyArgs,
  JobTakeOfflineArgs,
  JobTakeOfflineGuards,
  Primitive as PrimitiveName,
  PublishDraftEvidenceType,
  TakeOfflineEvidenceType,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import {
  prepareZhilianJobDraft,
  publishZhilianJobDraft,
  readZhilianJobClassCandidates,
  readZhilianJobKeywordVocabulary,
  readZhilianPublishedJobs,
  takeZhilianJobOffline,
  ZHILIAN_PLATFORM,
  ZhilianPlatformError,
} from '../platform/zhilian'

function failKnownOrThrow(error: unknown): PrimitiveOutcome {
  if (!(error instanceof ZhilianPlatformError)) throw error
  // 试填链路长且每步都依赖平台的异步行为,失败现场快照直接进 error.data
  // (契约里是 raw 对象)。它只给人读,不参与任何业务判定。
  const data: Record<string, unknown> = { ...(error.diagnostics ?? {}) }
  if (error.reason && data.reason === undefined) data.reason = error.reason
  return {
    status: 'failed',
    error: {
      code: error.code,
      message: error.message,
      retryable: error.retryable,
      // intrusive 原语没有资格用 effectful 的 possible/confirmed 语义。
      sideEffect: 'none',
      ...(Object.keys(data).length > 0 ? { data } : {}),
    },
  }
}

const readPublishedList: Primitive = {
  name: PrimitiveName.JobReadPublishedList,
  class: CmdClass.Intrusive,
  async handler(_rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await readZhilianPublishedJobs(
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const readClassCandidates: Primitive = {
  name: PrimitiveName.JobReadClassCandidates,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await readZhilianJobClassCandidates(
        rawArgs as JobReadClassCandidatesArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const readKeywordVocabulary: Primitive = {
  name: PrimitiveName.JobReadKeywordVocabulary,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await readZhilianJobKeywordVocabulary(
        rawArgs as JobReadKeywordVocabularyArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const prepareDraft: Primitive = {
  name: PrimitiveName.JobPrepareDraft,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await prepareZhilianJobDraft(
        rawArgs as JobPrepareDraftArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

// effectful 专用:sideEffect 必须如实取平台层的判断,不能像 intrusive 那样一律
// 报 none。点击之前的失败是 none(未发布、可安全重试),点击之后是 possible
// (可能已经发布,只能由脑的验证轮与 suspect 收敛,绝不重试)。
function failPublishOrThrow(error: unknown): PrimitiveOutcome {
  if (!(error instanceof ZhilianPlatformError)) throw error
  const data: Record<string, unknown> = { ...(error.diagnostics ?? {}) }
  if (error.reason && data.reason === undefined) data.reason = error.reason
  return {
    status: 'failed',
    error: {
      code: error.code,
      message: error.message,
      retryable: error.retryable,
      sideEffect: error.sideEffect,
      ...(Object.keys(data).length > 0 ? { data } : {}),
    },
  }
}

const publishDraft: Primitive = {
  name: PrimitiveName.JobPublishDraft,
  class: CmdClass.Effectful,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await publishZhilianJobDraft(
        rawArgs as JobPrepareDraftArgs,
        ctx.guards as unknown as JobPublishDraftGuards,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return {
        status: 'ok',
        data,
        evidence: [{ type: PublishDraftEvidenceType.PlatformPostingObserved }],
      }
    } catch (error) {
      return failPublishOrThrow(error)
    }
  },
}

const takeOffline: Primitive = {
  name: PrimitiveName.JobTakeOffline,
  class: CmdClass.Effectful,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await takeZhilianJobOffline(
        rawArgs as JobTakeOfflineArgs,
        ctx.guards as unknown as JobTakeOfflineGuards,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return {
        status: 'ok',
        data,
        evidence: [{ type: TakeOfflineEvidenceType.PlatformPostingOffline }],
      }
    } catch (error) {
      // 与发布共用同一套 sideEffect 口径:点确认之前 none,之后 possible。
      return failPublishOrThrow(error)
    }
  },
}

export function registerJobPublishPrimitives(): void {
  register(readPublishedList)
  register(readClassCandidates)
  register(readKeywordVocabulary)
  register(prepareDraft)
  register(publishDraft)
  register(takeOffline)
}
