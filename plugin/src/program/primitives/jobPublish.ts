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
