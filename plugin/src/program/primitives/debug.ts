// debug 原语归 debug.* 命名空间,不占平台无关业务词汇表。
// 平台无关纪律:tabId 之类只进 evidence/日志,不进 result 的语义字段。
import { Primitive, PrimitiveOutcome, register } from '../registry'
import { CmdClass, Primitive as PrimName } from '../../base/protocol'
import { SW_STARTED_AT } from '../../base/config'
import { armRuntimeReload } from '../../base/reload'
import { inspectZhilianSendSurfaceDiagnostic } from '../platform/zhilian'

const pingPrim: Primitive = {
  name: PrimName.DebugPing,
  class: CmdClass.Readonly,
  async handler(args): Promise<PrimitiveOutcome> {
    return { status: 'ok', data: { echo: args, swStartedAt: SW_STARTED_AT } }
  },
}

// 发送面诊断仍经唯一 Dispatcher 入口执行。它只返回契约 allowlist 阶段码，
// 不驱动页面、不读取出站结果之外的业务数据，也不把页面异常透传给脑。
const inspectSendSurfacePrim: Primitive = {
  name: PrimName.DebugInspectSendSurface,
  class: CmdClass.Readonly,
  async handler(): Promise<PrimitiveOutcome> {
    return { status: 'ok', data: await inspectZhilianSendSurfaceDiagnostic() }
  },
}

// debug.switchWindow:激活当前窗口的下一个标签页(肉眼可见的"随便一个动作")。intrusive。
const switchWindowPrim: Primitive = {
  name: PrimName.DebugSwitchWindow,
  class: CmdClass.Intrusive,
  async handler(): Promise<PrimitiveOutcome> {
    const win = await chrome.windows.getCurrent()
    const tabs = await chrome.tabs.query({ windowId: win.id })
    if (tabs.length < 2) {
      return { status: 'ok', data: { switched: false }, evidence: [{ type: 'postcondition', text: '窗口不足两个标签页,无从切换' }] }
    }
    const activeIdx = tabs.findIndex((t) => t.active)
    const next = tabs[(activeIdx + 1) % tabs.length]
    if (next.id === undefined) {
      return { status: 'failed', error: { code: 'INTERNAL_HAND', message: '下一个标签页无 id', sideEffect: 'none' } }
    }
    await chrome.tabs.update(next.id, { active: true })
    // tabId 只进 evidence,不进 result 语义字段(平台无关纪律)。
    return { status: 'ok', data: { switched: true }, evidence: [{ type: 'postcondition', text: `已激活标签页 index=${next.index}` }] }
  },
}

// debug.reload:只准备一次基础设施重载。真正的 chrome.runtime.reload 由
// connection 在收到脑对本 result 的 ACK 后触发，避免旧 SW 先死、终局未落脑。
const reloadPrim: Primitive = {
  name: PrimName.DebugReload,
  class: CmdClass.Intrusive,
  async handler(_args, ctx): Promise<PrimitiveOutcome> {
    await armRuntimeReload(ctx.cmdMsgId)
    return { status: 'ok', data: {} }
  },
}

// debug.slowEcho:声明 effectful(无真实副作用),按 outcome 演练 ok/failed/silent 全轨道。
const slowEchoPrim: Primitive = {
  name: PrimName.DebugSlowEcho,
  class: CmdClass.Effectful,
  async handler(args): Promise<PrimitiveOutcome> {
    const a = (args ?? {}) as { ms?: number; outcome?: string }
    const ms = typeof a.ms === 'number' ? a.ms : 0
    if (ms > 0) await new Promise((r) => setTimeout(r, ms))
    switch (a.outcome) {
      case 'failed':
        return { status: 'failed', error: { code: 'INTERNAL_HAND', message: '演练失败', sideEffect: 'possible' } }
      case 'silent':
        return 'silent'
      default:
        return { status: 'ok', data: { echoedAfterMs: ms } }
    }
  },
}

export function registerDebugPrimitives(): void {
  register(pingPrim)
  register(inspectSendSurfacePrim)
  register(reloadPrim)
  register(switchWindowPrim)
  register(slowEchoPrim)
}
