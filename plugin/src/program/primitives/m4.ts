// 里程碑 4 主动建联原语。页面动作只在 platform program；attempting/outbox
// 顺序继续由既有 Dispatcher/WitnessStore 构造性保证，本模块不接触 storage。
import {
  CandidateReadCurrentData,
  ChatReadGreetingOutcomeArgs,
  ChatSendGreetingArgs,
  ChatSendGreetingGuards,
  CmdClass,
  Primitive as PrimitiveName,
  SendGreetingEvidenceType,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import {
  readZhilianGreetingOutcome,
  readZhilianCurrentCandidate,
  sendZhilianGreeting,
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

const readCurrentCandidate: Primitive = {
  name: PrimitiveName.CandidateReadCurrent,
  class: CmdClass.Readonly,
  async handler(_rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data: CandidateReadCurrentData = await readZhilianCurrentCandidate(
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const readGreetingOutcome: Primitive = {
  name: PrimitiveName.ChatReadGreetingOutcome,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await readZhilianGreetingOutcome(
        rawArgs as ChatReadGreetingOutcomeArgs,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const sendGreeting: Primitive = {
  name: PrimitiveName.ChatSendGreeting,
  class: CmdClass.Effectful,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      if (!ctx.commandContext || ctx.commandContext.platform !== ZHILIAN_PLATFORM) {
        throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
      }
      const data = await sendZhilianGreeting(
        rawArgs as ChatSendGreetingArgs,
        ctx.guards as unknown as ChatSendGreetingGuards,
        ctx,
        ctx.commandContext.expectedPrincipalFingerprint,
      )
      return {
        status: 'ok',
        data,
        evidence: [{ type: SendGreetingEvidenceType.OutboundGreetingObserved }],
      }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

export function registerM4Primitives(): void {
  register(readCurrentCandidate)
  register(readGreetingOutcome)
  register(sendGreeting)
}
