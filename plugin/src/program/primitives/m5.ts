// M5 简历补采原语。页面驱动与字段解析全部留在智联 program；本模块只把
// generated 契约接到唯一注册表，不保存正文、不引入第二条执行路径。
import {
  CandidateReadResumeArgs,
  CmdClass,
  Primitive as PrimitiveName,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import {
  readZhilianResume,
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
      sideEffect: 'none',
      ...(error.reason ? { data: { reason: error.reason } } : {}),
    },
  }
}

const readResume: Primitive = {
  name: PrimitiveName.CandidateReadResume,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await readZhilianResume(
        rawArgs as CandidateReadResumeArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

export function registerM5Primitives(): void {
  register(readResume)
}
