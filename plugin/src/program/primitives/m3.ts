// 里程碑 3 首个真实外部副作用原语。program 只操作页面；attempting/outbox
// 的持久顺序由 base Dispatcher 构造性保证，本模块不可访问 chrome.storage。
import {
  ChatSendMessageArgs,
  ChatSendMessageGuards,
  CmdClass,
  Primitive as PrimitiveName,
  SendMessageEvidenceType,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import {
  sendZhilianMessage,
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
      sideEffect: error.sideEffect,
      ...(error.reason ? { data: { reason: error.reason } } : {}),
    },
  }
}

const sendMessage: Primitive = {
  name: PrimitiveName.ChatSendMessage,
  class: CmdClass.Effectful,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await sendZhilianMessage(
        rawArgs as ChatSendMessageArgs,
        ctx.guards as unknown as ChatSendMessageGuards,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return {
        status: 'ok',
        data,
        evidence: [{ type: SendMessageEvidenceType.OutboundMessageObserved }],
      }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

export function registerM3Primitives(): void {
  register(sendMessage)
}
