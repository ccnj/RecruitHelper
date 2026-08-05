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
      sideEffect: 'none',
      ...(Object.keys(data).length > 0 ? { data } : {}),
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
