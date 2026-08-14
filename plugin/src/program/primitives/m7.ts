// 成功取证原语(2026-07-28 截图契约增量 + 2026-08-06 电话侧栏读取增量)。滚动
// 拼接与页面驱动全部留在智联 program;本模块只把 generated 契约接到唯一注册表。
// 取证是尽力而为的降级型感知:失败只产生"缺图/缺号",不推进业务状态、不授权
// 任何 effectful 重试。
import {
  CandidateCaptureResumeScreenshotArgs,
  ChatCaptureThreadScreenshotArgs,
  ChatReadPeerPhoneArgs,
  CmdClass,
  Primitive as PrimitiveName,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import {
  captureZhilianResumeScreenshot,
  captureZhilianThreadScreenshot,
  readZhilianPeerPhone,
  revealZhilianPeerPhone,
  ZHILIAN_PLATFORM,
  ZhilianPlatformError,
} from '../platform/zhilian'

function failKnownOrThrow(error: unknown): PrimitiveOutcome {
  if (!(error instanceof ZhilianPlatformError)) throw error
  // 失败现场快照与 notReady 原因合并进 error.data(只给人读,不参与任何业务
  // 判定),同 m6:随失败 result 落进脑侧账本与日志,页面刷新也不丢。
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

const captureResumeScreenshot: Primitive = {
  name: PrimitiveName.CandidateCaptureResumeScreenshot,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await captureZhilianResumeScreenshot(
        rawArgs as CandidateCaptureResumeScreenshotArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const readPeerPhone: Primitive = {
  name: PrimitiveName.ChatReadPeerPhone,
  class: CmdClass.Readonly,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await readZhilianPeerPhone(
        rawArgs as ChatReadPeerPhoneArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const revealPeerPhone: Primitive = {
  name: PrimitiveName.ChatRevealPeerPhone,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await revealZhilianPeerPhone(
        rawArgs as ChatReadPeerPhoneArgs,
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
  register(captureResumeScreenshot)
  register(readPeerPhone)
  register(revealPeerPhone)
}
