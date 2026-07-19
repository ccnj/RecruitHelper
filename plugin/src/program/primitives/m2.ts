// 里程碑 2 正式感知原语。所有入口都经 base Dispatcher；没有测试模式分支。

import {
  ChatReadListArgs,
  ChatReadListData,
  ChatReadThreadArgs,
  ChatReadThreadData,
  CmdClass,
  NavEnsureSurfaceArgs,
  NavEnsureSurfaceData,
  Primitive as PrimitiveName,
  ProbePlatformData,
} from '../../base/protocol'
import { Primitive, PrimitiveOutcome, register } from '../registry'
import {
  ensureZhilianIM,
  probeZhilian,
  readZhilianList,
  readZhilianThread,
  ZHILIAN_PLATFORM,
  ZhilianPlatformError,
} from '../platform/zhilian'

function failed(error: ZhilianPlatformError): PrimitiveOutcome {
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
  if (error instanceof ZhilianPlatformError) return failed(error)
  // StopExecution 与 base 的执行控制异常必须回到 Dispatcher；未知异常也由 base
  // 统一落 INTERNAL_HAND，program 不得把它们伪装成页面丢失。
  throw error
}

function assertZhilianContext(context: { platform?: string } | undefined): void {
  if (!context || context.platform !== ZHILIAN_PLATFORM) {
    throw new ZhilianPlatformError('CTX_NOT_READY', '命令未绑定智联平台上下文', 'no', 'unknown')
  }
}

const probePlatform: Primitive = {
  name: PrimitiveName.ProbePlatform,
  class: CmdClass.Readonly,
  async handler(): Promise<PrimitiveOutcome> {
    try {
      const data: ProbePlatformData = await probeZhilian()
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
      assertZhilianContext(ctx.commandContext)
      const args = rawArgs as NavEnsureSurfaceArgs
      if (args.surface !== 'im') {
        throw new ZhilianPlatformError('TARGET_NOT_FOUND', '当前手不支持该页面 surface', 'no')
      }
      const data: NavEnsureSurfaceData = await ensureZhilianIM(
        ctx,
        ctx.commandContext?.expectedPrincipalFingerprint,
      )
      return {
        status: 'ok',
        data,
        evidence: [{ type: 'postcondition', text: data.ready ? '智联 IM 页面已就绪' : '智联 IM 页面未就绪' }],
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
      assertZhilianContext(ctx.commandContext)
      const data: ChatReadListData = await readZhilianList(
        rawArgs as ChatReadListArgs,
        ctx,
        ctx.commandContext?.expectedPrincipalFingerprint,
      )
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

const readThread: Primitive = {
  name: PrimitiveName.ChatReadThread,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      assertZhilianContext(ctx.commandContext)
      const data: ChatReadThreadData = await readZhilianThread(
        rawArgs as ChatReadThreadArgs,
        ctx,
        ctx.commandContext?.expectedPrincipalFingerprint,
      )
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
  register(readThread)
}
