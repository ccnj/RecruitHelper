// 里程碑 4 主动建联原语。页面动作只在平台适配器；attempting/outbox
// 顺序继续由既有 Dispatcher/WitnessStore 构造性保证，本模块不接触 storage。
import {
  CandidateReadCurrentArgs,
  ChatReadGreetingOutcomeArgs,
  ChatSendGreetingArgs,
  ChatSendGreetingGuards,
  CmdClass,
  Primitive as PrimitiveName,
  SendGreetingEvidenceType,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import { callPlatform } from '../platform/registry'
import { PlatformError } from '../platform/types'

function failKnownOrThrow(error: unknown): PrimitiveOutcome {
  if (!(error instanceof PlatformError)) throw error
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
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(
        ctx, 'readCurrentCandidate', rawArgs as CandidateReadCurrentArgs,
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
      const data = await callPlatform(
        ctx, 'readGreetingOutcome', rawArgs as ChatReadGreetingOutcomeArgs,
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
      const data = await callPlatform(
        ctx, 'sendGreeting',
        rawArgs as ChatSendGreetingArgs,
        ctx.guards as unknown as ChatSendGreetingGuards,
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
