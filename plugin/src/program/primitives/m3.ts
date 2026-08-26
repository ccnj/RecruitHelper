// 里程碑 3 首个真实外部副作用原语。program 只操作页面；attempting/outbox
// 的持久顺序由 base Dispatcher 构造性保证，本模块不可访问 chrome.storage。
//
// 平台实现由 callPlatform 按 cmd.context.platform 路由(见 ../platform/registry)。
// guards 原样透传给平台层——动作前的账号身份、路由与目标绑定核对在那里执行,
// 本文件不解读、不放宽其中任何一条。
import {
  AcceptWechatEvidenceType,
  ChatAcceptWechatArgs,
  ChatReadWechatExchangeOutcomeArgs,
  ChatSendInviteCardArgs,
  ChatSendMessageArgs,
  ChatSendMessageGuards,
  ChatSendWechatInviteArgs,
  CmdClass,
  Primitive as PrimitiveName,
  SendInviteCardEvidenceType,
  SendMessageEvidenceType,
  SendWechatInviteEvidenceType,
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

const sendMessage: Primitive = {
  name: PrimitiveName.ChatSendMessage,
  class: CmdClass.Effectful,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(
        ctx, 'sendMessage',
        rawArgs as ChatSendMessageArgs,
        ctx.guards as unknown as ChatSendMessageGuards,
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

const sendWechatInvite: Primitive = {
  name: PrimitiveName.ChatSendWechatInvite,
  class: CmdClass.Effectful,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(
        ctx, 'sendWechatInvite',
        rawArgs as ChatSendWechatInviteArgs,
        ctx.guards as unknown as ChatSendMessageGuards,
      )
      return {
        status: 'ok',
        data,
        evidence: [{ type: SendWechatInviteEvidenceType.OutboundWechatInviteObserved }],
      }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const acceptWechat: Primitive = {
  name: PrimitiveName.ChatAcceptWechat,
  class: CmdClass.Effectful,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(
        ctx, 'acceptWechat',
        rawArgs as ChatAcceptWechatArgs,
        ctx.guards as unknown as ChatSendMessageGuards,
      )
      return {
        status: 'ok',
        data,
        evidence: [{ type: AcceptWechatEvidenceType.CandidateWechatRequestAcceptedObserved }],
      }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const readWechatExchangeOutcome: Primitive = {
  name: PrimitiveName.ChatReadWechatExchangeOutcome,
  class: CmdClass.Readonly,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(
        ctx, 'readWechatExchangeOutcome', rawArgs as ChatReadWechatExchangeOutcomeArgs,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const sendInviteCard: Primitive = {
  name: PrimitiveName.ChatSendInviteCard,
  class: CmdClass.Effectful,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(
        ctx, 'sendInviteCard',
        rawArgs as ChatSendInviteCardArgs,
        ctx.guards as unknown as ChatSendMessageGuards,
      )
      return {
        status: 'ok',
        data,
        evidence: [{ type: SendInviteCardEvidenceType.OutboundInterviewInviteObserved }],
      }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

export function registerM3Primitives(): void {
  register(sendMessage)
  register(sendWechatInvite)
  register(acceptWechat)
  register(readWechatExchangeOutcome)
  register(sendInviteCard)
}
