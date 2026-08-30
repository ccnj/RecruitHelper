// 平台适配器注册表(program 层)。
//
// 全手**唯一**一处「这条命令归哪个平台执行」的判断。此前这个判断有 27 份拷贝,
// 每个原语一份;任何一份写错或漏改都不会被编译器发现。
//
// 与 `../registry.ts`(原语注册表)的分工:
//   原语注册表   name -> Primitive        base 分发器认的那张表,线上契约面
//   平台注册表   platformId -> Adapter    同一条原语在不同平台上的实现
//
// 注意本表**不进 hello 的 caps**。协议今天的 caps 是扁平的 `name@ver`,说不出
// 「这条原语只有智联有」;真要表达,得改契约(HelloBody 加按平台的能力声明)。
// 本轮不改契约,所以下面的 `requireCapability` 是运行期兜底,不是能力协商的
// 替代品——只装了一个平台时它永远不会触发。
import { PlatformError } from './types'
import type { ErrorCode, Retryable } from '../../base/protocol'
import type {
  CapabilityName,
  PlatformAdapter,
  PlatformCapabilities,
  PlatformId,
  PrimitiveInput,
} from './types'
import type { PrimitiveContext } from '../registry'

const adapters = new Map<PlatformId, PlatformAdapter>()

/**
 * 注册一个平台。由 base/background.ts 在 SW 启动时调用,与原语注册同一时机。
 *
 * 同 id 重复注册直接抛错:那只可能是接线写重了,静默覆盖会让「到底跑的哪个
 * 实现」在事后无从追查。
 */
export function registerPlatform(adapter: PlatformAdapter): void {
  if (adapters.has(adapter.id)) {
    throw new Error(`平台 ${adapter.id} 重复注册`)
  }
  adapters.set(adapter.id, adapter)
}

export function lookupPlatform(id: PlatformId): PlatformAdapter | undefined {
  return adapters.get(id)
}

/** 已注册平台的快照。传感层用它决定要盯哪些标签页。 */
export function registeredPlatforms(): PlatformAdapter[] {
  return [...adapters.values()]
}

/** 测试专用:清空注册表。生产路径永不调用。 */
export function resetPlatformsForTest(): void {
  adapters.clear()
}

/**
 * 从命令上下文解析出该由谁执行。
 *
 * 失败语义与它替代的那 27 份拷贝逐字一致:`CTX_NOT_READY` / `retryable=no` /
 * `reason=unknown`。方向是「不确认就不做」——上下文缺失或平台不认识时拒绝执行,
 * 绝不猜一个平台顶上。
 */
export function resolveAdapter(ctx: PrimitiveContext): PlatformAdapter {
  const platform = ctx.commandContext?.platform
  if (!platform) {
    throw new PlatformError('CTX_NOT_READY', '命令未携带平台上下文', 'no', 'unknown')
  }
  const adapter = adapters.get(platform)
  if (!adapter) {
    throw new PlatformError(
      'CTX_NOT_READY',
      `本手未注册平台 ${platform} 的实现`,
      'no',
      'unknown',
    )
  }
  return adapter
}

/**
 * 取适配器上的某个能力。
 *
 * 未实现即显式拒绝(反模式 18:未知原语默认分支必须是显式拒绝,不得默认回成功)。
 * `retryable=no` —— 平台没实现这条能力是部署事实,重试多少次都一样。
 */
export function requireCapability<K extends CapabilityName>(
  adapter: PlatformAdapter,
  name: K,
): NonNullable<PlatformCapabilities[K]> {
  const method = adapter[name]
  if (typeof method !== 'function') {
    throw new PlatformError(
      'PROTO_UNSUPPORTED_CMD',
      `平台 ${adapter.id} 未实现原语能力 ${name}`,
      'no',
    )
  }
  // 绑回适配器,方法体里的 this 才是它自己。
  return (method as (...a: unknown[]) => unknown).bind(adapter) as NonNullable<
    PlatformCapabilities[K]
  >
}

type InputOf<K extends CapabilityName> = Parameters<PlatformCapabilities[K]>[0]
type DataOf<K extends CapabilityName> = Awaited<ReturnType<PlatformCapabilities[K]>>

function invoke<K extends CapabilityName>(
  adapter: PlatformAdapter,
  name: K,
  input: PrimitiveInput<InputOf<K>['args'], InputOf<K>['guards']>,
): Promise<DataOf<K>> {
  const method = requireCapability(adapter, name) as (i: unknown) => Promise<DataOf<K>>
  return method(input)
}

/**
 * 原语调用平台实现的唯一入口。
 *
 * 它把「解析平台 → 核对能力 → 装配入参」三步收在一处;原语侧因此只剩一行,
 * 既有的错误映射、evidence 与 args 断言原样保留在各自文件里。
 *
 * 账号身份指纹从命令上下文取,与改造前逐字一致——它是「现在登录的还是不是
 * 同一个人」的硬前置,任何平台都不得跳过。
 */
export function callPlatform<K extends CapabilityName>(
  ctx: PrimitiveContext,
  name: K,
  args: InputOf<K>['args'],
  guards?: InputOf<K>['guards'],
): Promise<DataOf<K>> {
  return invoke(resolveAdapter(ctx), name, {
    args,
    guards: guards as InputOf<K>['guards'],
    ctx,
    fingerprint: ctx.commandContext?.expectedPrincipalFingerprint,
  })
}

/**
 * 契约上**不要求** `context.platform` 的原语专用。当前三条:
 *
 *   probe.platform            标了 contextOptionalBeforeBinding;首次绑定账号前
 *                             脑还不知道该探谁,命令不带 context
 *   debug.capturePage         preconditions 为空。suspect 现场取证真的会不带
 *                             context 派发(见脑侧 dispatch/suspect_scene.go:
 *                             DispatchRequest 只填 HandID/Name/Args)
 *   debug.inspectSendSurface  preconditions 为空,同上
 *
 * 路由优先级:**context > args.platform > 恰好一个平台**。
 *
 * `args.platform` 是 2026-08-30 加的可选字段(`probe.platform` 与
 * `debug.capturePage` 各一个)。它解决的是这样一个真实死结:脑侧
 * `bindAccount` 与 suspect 现场取证都不带 context 派发,而装上第二个平台之后
 * `soleAdapter()` 必然拒绝——于是**任何平台**(包括智联自己)都绑不了账号。
 *
 * 拒绝本身是对的、也**必须**保留:`ProbePlatformData` 没有平台身份字段,
 * hello 的 caps 也说不出手服务哪些平台,脑既无从指定探哪个、也无从从回包分辨
 * 探到了哪个,猜一个顶上就是错靶的开始。所以补法是让脑**说出来**,而不是让手猜。
 *
 * 为什么放 args 而不是改成传完整 context:一是 `CmdContext.accountRef` 是必填,
 * 而 bind 的时候账号还不存在;二是带 context 会把串行域从 `debug:handID` 换成
 * `platform:accountRef`(脑侧 m2_dispatch.go),suspect 截图会挤进账号队列,
 * 违反它自己「不阻塞批次」的设计。
 *
 * 两者都在且不一致时**拒绝**,不选一个信:那说明脑侧两处来源打架,而这两处
 * 恰恰决定动作落在谁的页面上。
 */
export function callPlatformUnbound<K extends CapabilityName>(
  ctx: PrimitiveContext,
  name: K,
  args: InputOf<K>['args'],
): Promise<DataOf<K>> {
  const bound = ctx.commandContext?.platform
  const hinted = platformHint(args)
  if (bound && hinted && bound !== hinted) {
    throw new PlatformError(
      'CTX_NOT_READY',
      `命令自相矛盾:context 说 ${bound}、args 说 ${hinted}`,
      'no',
      'unknown',
    )
  }
  const adapter = bound
    ? resolveAdapter(ctx)
    : hinted
      ? resolveNamedAdapter(hinted)
      : soleAdapter()
  return invoke(adapter, name, {
    args,
    guards: undefined as InputOf<K>['guards'],
    ctx,
    fingerprint: ctx.commandContext?.expectedPrincipalFingerprint,
  })
}

/** 从 args 里取平台提示。只认字符串,别的形状一律当没有。 */
function platformHint(args: unknown): string | undefined {
  if (typeof args !== 'object' || args === null) return undefined
  const value = (args as { platform?: unknown }).platform
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

/** 按名字取适配器。失败语义与 resolveAdapter 逐字一致:不认识就拒,不猜。 */
function resolveNamedAdapter(platform: string): PlatformAdapter {
  const adapter = adapters.get(platform)
  if (!adapter) {
    throw new PlatformError(
      'CTX_NOT_READY',
      `本手未注册平台 ${platform} 的实现`,
      'no',
      'unknown',
    )
  }
  return adapter
}

function soleAdapter(): PlatformAdapter {
  if (adapters.size === 1) return [...adapters.values()][0]
  throw new PlatformError(
    'CTX_NOT_READY',
    adapters.size === 0
      ? '本手未注册任何平台实现'
      : `本手注册了 ${adapters.size} 个平台,命令未指明探测目标`,
    'no',
    'unknown',
  )
}

/**
 * 把平台层的失败翻成契约错误。**原语必须接住它,否则会逃成 INTERNAL_HAND。**
 *
 * 真机第一跑就撞上了:`debug.osProbe` 没接,于是"智联 IM 页面不存在"这条本该是
 * `CTX_NOT_READY / pageAbsent / sideEffect=none` 的失败,回到脑那边成了
 * `INTERNAL_HAND / sideEffect=possible`——一次什么都没做的失败被记成"副作用可能发生了",
 * 方向正好反了。
 *
 * `sideEffect` 恒为 `none`:readonly 与 intrusive 没有资格用 effectful 的
 * possible/confirmed 语义。effectful 原语另有自己的映射(要如实带出点击前后的区别),
 * 不走这里。
 *
 * **这是这段逻辑的规范出处。** `primitives/{account,m2,m5,jobPublish}.ts` 里各有一份
 * 早于本函数的同名拷贝,过手时应迁到这里来;本轮不主动翻修它们(一因果一 commit)。
 */
export function platformFailure(error: unknown): {
  status: 'failed'
  error: { code: ErrorCode; message: string; retryable: Retryable; sideEffect: 'none'; data?: Record<string, unknown> }
} {
  if (!(error instanceof PlatformError)) throw error
  return {
    status: 'failed',
    error: {
      code: error.code,
      message: error.message,
      retryable: error.retryable,
      sideEffect: 'none',
      ...(error.reason ? { data: { reason: error.reason } } : {}),
    },
  }
}
