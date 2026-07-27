// 成功取证截图原语(2026-07-28 契约增量)。滚动拼接与页面驱动全部留在智联
// program;本模块只把 generated 契约接到唯一注册表。截图是尽力而为的降级型
// 感知:失败只产生"缺图",不推进业务状态、不授权任何 effectful 重试。
import {
  ChatCaptureThreadScreenshotArgs,
  CmdClass,
  Primitive as PrimitiveName,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import {
  captureZhilianThreadScreenshot,
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

const captureThreadScreenshot: Primitive = {
  name: PrimitiveName.ChatCaptureThreadScreenshot,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await captureZhilianThreadScreenshot(
        rawArgs as ChatCaptureThreadScreenshotArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

export function registerM7Primitives(): void {
  register(captureThreadScreenshot)
}
