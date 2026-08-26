// 里程碑 2 正式感知原语。所有入口都经 base Dispatcher；没有测试模式分支。
//
// 平台实现由 callPlatform 按 cmd.context.platform 路由(见 ../platform/registry)。
// 本文件不认识任何具体平台。

import {
  ChatIdentifyCurrentConversationArgs,
  ChatOpenConversationArgs,
  ChatReadListArgs,
  ChatReadThreadArgs,
  ChatReadUnreadTotalArgs,
  CmdClass,
  NavEnsureSurfaceArgs,
  Primitive as PrimitiveName,
  ProbePlatformArgs,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import { callPlatform, callPlatformUnbound } from '../platform/registry'
import { PlatformError } from '../platform/types'

function failed(error: PlatformError): PrimitiveOutcome {
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

function failKnownOrThrow(error: unknown): PrimitiveOutcome {
  if (error instanceof PlatformError) return failed(error)
  // StopExecution 与 base 的执行控制异常必须回到 Dispatcher；未知异常也由 base
  // 统一落 INTERNAL_HAND，program 不得把它们伪装成页面丢失。
  throw error
}

const probePlatform: Primitive = {
  name: PrimitiveName.ProbePlatform,
  class: CmdClass.Readonly,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      // 唯一一条允许在账号绑定前执行的原语,命令可以不带 context。
      const data = await callPlatformUnbound(
        ctx, 'probePlatform', rawArgs as ProbePlatformArgs,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const ensureSurface: Primitive = {
  name: PrimitiveName.NavEnsureSurface,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      // 「本平台有没有这个 surface」是平台自己的事实,判断在适配器里。
      const data = await callPlatform(ctx, 'ensureSurface', rawArgs as NavEnsureSurfaceArgs)
      return {
        status: 'ok',
        data,
        evidence: [{ type: 'postcondition', text: data.ready ? 'IM 页面已就绪' : 'IM 页面未就绪' }],
      }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const readList: Primitive = {
  name: PrimitiveName.ChatReadList,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(ctx, 'readList', rawArgs as ChatReadListArgs)
      return {
        status: 'ok',
        data,
        evidence: [{ type: 'snapshot', text: `读取 ${data.sessions.length} 条会话索引` }],
      }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const identifyCurrentConversation: Primitive = {
  name: PrimitiveName.ChatIdentifyCurrentConversation,
  class: CmdClass.Readonly,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(
        ctx, 'identifyCurrentConversation', rawArgs as ChatIdentifyCurrentConversationArgs,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const openConversation: Primitive = {
  name: PrimitiveName.ChatOpenConversation,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(ctx, 'openConversation', rawArgs as ChatOpenConversationArgs)
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const readUnreadTotal: Primitive = {
  name: PrimitiveName.ChatReadUnreadTotal,
  class: CmdClass.Readonly,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(ctx, 'readUnreadTotal', rawArgs as ChatReadUnreadTotalArgs)
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const readThread: Primitive = {
  name: PrimitiveName.ChatReadThread,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(ctx, 'readThread', rawArgs as ChatReadThreadArgs)
      return {
        status: 'ok',
        data,
        evidence: [{ type: 'snapshot', text: `读取 ${data.messages.length} 条有界消息快照` }],
      }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

export function registerM2Primitives(): void {
  register(probePlatform)
  register(ensureSurface)
  register(readList)
  register(identifyCurrentConversation)
  register(readUnreadTotal)
  register(openConversation)
  register(readThread)
}
