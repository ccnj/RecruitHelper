// 冒烟冲刺的单候选人采集原语。候选人选择、详情动作与完整性核验只存在于
// 智联 program；本模块仅把生成契约接入唯一 dispatcher 注册表。
import {
  CandidateApplySourcingFiltersArgs,
  CandidateReadSourcingResumeArgs,
  CandidateReadSourcingTargetResumeArgs,
  CandidateReadSourcingWindowArgs,
  CandidateSelectSourcingPositionArgs,
  CmdClass,
  Primitive as PrimitiveName,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import {
  applyZhilianSourcingFilters,
  readZhilianSourcingResume,
  readZhilianSourcingTargetResume,
  readZhilianSourcingWindow,
  selectZhilianSourcingPosition,
  ZHILIAN_PLATFORM,
  ZhilianPlatformError,
} from '../platform/zhilian'

function failKnownOrThrow(error: unknown): PrimitiveOutcome {
  if (!(error instanceof ZhilianPlatformError)) throw error
  // 失败现场快照与 notReady 原因合并进 error.data(只给人读，不参与任何业务
  // 判定)。简历读取的十余种失败原因原本全被压成同一句话，账本、日志与诊断包
  // 里都看不出是弹窗没打开还是某个区块缺内容——2026-08-05 一个真实候选人卡在
  // 这里、采集侧累计 97 次同码失败，都只能靠猜。
  const data: Record<string, unknown> = { ...(error.diagnostics ?? {}) }
  if (error.reason) data.reason = error.reason
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

const selectSourcingPosition: Primitive = {
  name: PrimitiveName.CandidateSelectSourcingPosition,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await selectZhilianSourcingPosition(
        rawArgs as CandidateSelectSourcingPositionArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const applySourcingFilters: Primitive = {
  name: PrimitiveName.CandidateApplySourcingFilters,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await applyZhilianSourcingFilters(
        rawArgs as CandidateApplySourcingFiltersArgs,
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
  register(selectSourcingPosition)
  register(applySourcingFilters)
  register(readSourcingResume)
  register(readSourcingWindow)
  register(readSourcingTargetResume)
}
