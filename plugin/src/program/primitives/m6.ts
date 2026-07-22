// 冒烟冲刺的单候选人采集原语。候选人选择、详情动作与完整性核验只存在于
// 智联 program；本模块仅把生成契约接入唯一 dispatcher 注册表。
import {
  CandidateReadSourcingResumeArgs,
  CandidateReadSourcingTargetResumeArgs,
  CandidateReadSourcingWindowArgs,
  CmdClass,
  Primitive as PrimitiveName,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import {
  readZhilianSourcingResume,
  readZhilianSourcingTargetResume,
  readZhilianSourcingWindow,
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

const readSourcingResume: Primitive = {
  name: PrimitiveName.CandidateReadSourcingResume,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await readZhilianSourcingResume(
        rawArgs as CandidateReadSourcingResumeArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const readSourcingWindow: Primitive = {
  name: PrimitiveName.CandidateReadSourcingWindow,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await readZhilianSourcingWindow(
        rawArgs as CandidateReadSourcingWindowArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const readSourcingTargetResume: Primitive = {
  name: PrimitiveName.CandidateReadSourcingTargetResume,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await readZhilianSourcingTargetResume(
        rawArgs as CandidateReadSourcingTargetResumeArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

export function registerM6Primitives(): void {
  register(readSourcingResume)
  register(readSourcingWindow)
  register(readSourcingTargetResume)
}
