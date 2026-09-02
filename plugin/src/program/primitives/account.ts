// 账号配置读面原语。页面驱动留在平台适配器;本模块只把 generated 契约接到
// 唯一注册表。
//
//   account.readWechatSetting  intrusive  导航个人中心,读微信号配置是否已填
//   account.readNotices        intrusive  同一页切「通知」页签,读第一页通知
//
// readWechatSetting 的 data 只含布尔;招聘方自己的微信号不进契约(2026-08-18
// 甲方裁决立案)。readNotices 是第十一项云端出站「平台通知上报」的读面
// (2026-09-02 甲方裁决),不是闸——脑侧读失败只记日志。
import {
  AccountReadNoticesArgs,
  AccountReadWechatSettingArgs,
  CmdClass,
  Primitive as PrimitiveName,
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
      // intrusive 原语没有资格用 effectful 的 possible/confirmed 语义。
      sideEffect: 'none',
      ...(error.reason ? { data: { reason: error.reason } } : {}),
    },
  }
}

const readWechatSetting: Primitive = {
  name: PrimitiveName.AccountReadWechatSetting,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(
        ctx, 'readWechatSetting', rawArgs as AccountReadWechatSettingArgs,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

const readNotices: Primitive = {
  name: PrimitiveName.AccountReadNotices,
  class: CmdClass.Intrusive,
  async handler(rawArgs, ctx): Promise<PrimitiveOutcome> {
    try {
      const data = await callPlatform(
        ctx, 'readNotices', rawArgs as AccountReadNoticesArgs,
      )
      return { status: 'ok', data }
    } catch (error) {
      return failKnownOrThrow(error)
    }
  },
}

export function registerAccountPrimitives(): void {
  register(readWechatSetting)
  register(readNotices)
}
