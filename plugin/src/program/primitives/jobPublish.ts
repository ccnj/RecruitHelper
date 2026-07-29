// 职位发布前预检原语。分区遍历与职位名提取全部留在智联 program;本模块只把
// generated 契约接到唯一注册表。本原语只读平台已存在的职位名,不创建、不编辑、
// 不上下线任何职位,也不产生候选人可见动作。
import {
  CmdClass,
  JobPrepareDraftArgs,
  Primitive as PrimitiveName,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import {
  prepareZhilianJobDraft,
  readZhilianPublishedJobs,
  ZHILIAN_PLATFORM,
  ZhilianPlatformError,
} from '../platform/zhilian'

function failKnownOrThrow(error: unknown): PrimitiveOutcome {
  if (!(error instanceof ZhilianPlatformError)) throw error
  return {
    status: 'failed',
    error: {
      code: error.code,
      message: error.message,
      retryable: error.retryable,
      // intrusive 原语没有资格用 effectful 的 possible/confirmed 语义。
      sideEffect: 'none',
      ...(error.reason ? { data: { reason: error.reason } } : {}),
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

export function registerJobPublishPrimitives(): void {
  register(readPublishedList)
  register(prepareDraft)
}
