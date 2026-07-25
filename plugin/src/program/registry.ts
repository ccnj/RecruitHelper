// 原语注册表(program 层)。program 不注册任何 chrome 监听、只经此表暴露能力(宪法禁令 5)。
// 这是单一监听/分发入口与整包可验证构建的边界：base 只认这张表，不认具体业务。
import { CmdClass, PRIMITIVE_META } from '../base/protocol'
import type { ErrorBody, Evidence, ResultStatus } from '../base/protocol'
import type { ExecutionHooks } from '../base/dispatcher'

// 这是 base 与 program 之间唯一的执行控制接缝。长原语只在确定的分页/滚动边界
// 调用这些合作式钩子；program 不自行维护超时、租约或取消状态。
export interface PrimitiveContext extends ExecutionHooks {}

export interface PrimitiveResult {
  status: ResultStatus
  data?: unknown
  error?: ErrorBody
  evidence?: Evidence[]
}

// 'silent':故意不回 result(演练超时/suspect)。effectful 才有资格。
export type PrimitiveOutcome = PrimitiveResult | 'silent'

export interface Primitive {
  name: string
  class: CmdClass
  handler: (args: unknown, ctx: PrimitiveContext) => Promise<PrimitiveOutcome>
}

const registry = new Map<string, Primitive>()

export function register(p: Primitive): void {
  registry.set(p.name, p)
}

export function lookup(name: string): Primitive | undefined {
  return registry.get(name)
}

// capabilities:hello 上报的能力集 `name@ver`(ver 取自契约 PRIMITIVE_META)。
export function capabilities(): string[] {
  const out: string[] = []
  for (const name of registry.keys()) {
    const meta = PRIMITIVE_META[name as keyof typeof PRIMITIVE_META]
    out.push(`${name}@${meta ? meta.ver : 1}`)
  }
  return out.sort()
}
