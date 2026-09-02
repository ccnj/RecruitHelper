// 原语注册表(program 层)。program 不注册任何 chrome 监听、只经此表暴露能力(宪法禁令 5)。
// 这是单一监听/分发入口与整包可验证构建的边界：base 只认这张表，不认具体业务。
import { CmdClass, PRIMITIVE_META } from '../base/protocol'
import type { ErrorBody, Evidence, ResultStatus } from '../base/protocol'
import type { ExecutionHooks } from '../base/dispatcher'
import type { CapabilityName } from './platform/types'
import { hasCapability, registeredPlatforms } from './platform/registry'

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
  /**
   * 该原语依赖的平台适配器能力名(与 handler 里 `callPlatform(ctx, '<能力名>')` 的
   * 字面量一致)。缺省表示平台无关(debug.ping 之类不经适配器),每个平台表都列它。
   * 它是 hello `platforms[].caps` 的数据来源(2026-09-02 甲方裁决);与 handler 内
   * 字面量并行的第二份声明,由单测「智联表===并集」「BOSS 表===探针三条+平台无关四条」钉住。
   */
  capability?: CapabilityName
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
// 它是全部平台能力的**并集**,旧脑只认它;按平台细分见 capabilitiesByPlatform。
export function capabilities(): string[] {
  const out: string[] = []
  for (const name of registry.keys()) {
    out.push(capabilityId(name))
  }
  return out.sort()
}

function capabilityId(name: string): string {
  const meta = PRIMITIVE_META[name as keyof typeof PRIMITIVE_META]
  return `${name}@${meta ? meta.ver : 1}`
}

/**
 * capabilitiesByPlatform:hello `platforms` 字段的数据来源(2026-09-02 甲方裁决)。
 * 对每个已注册平台,列出「该适配器实现了其 capability 的原语」加「平台无关原语」。
 * 判据与运行期 `requireCapability` 同源(方法在不在),不另造第二份真相。
 * 每张表按构造 ⊆ capabilities();调用方若再按证词可用性过滤并集,须对每张表做同一过滤。
 */
export function capabilitiesByPlatform(): Array<{ id: string; caps: string[] }> {
  return registeredPlatforms().map((adapter) => {
    const caps: string[] = []
    for (const primitive of registry.values()) {
      if (primitive.capability === undefined || hasCapability(adapter, primitive.capability)) {
        caps.push(capabilityId(primitive.name))
      }
    }
    return { id: adapter.id, caps: caps.sort() }
  })
}
