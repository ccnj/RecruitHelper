// 页面注入的唯一机制件(program 层)。
//
// 抽出来的理由不是复用,是**让「这个平台怎么碰页面」有一个地方可查**。此前
// `world: 'MAIN'` 直接写死在智联的注入入口里,而 91 处调用全经那一个入口;
// 第二个平台若只能用 ISOLATED,那一行改起来是一处,但没有任何声明面能说清
// 「谁用哪个世界」——`ExecutionWorld` 由适配器声明、在这里被消费,就是那个声明面。
//
// 本文件只管注入这一件事:发出去、把 Chrome 的 InjectionResult 拆开、把页面里
// 的哨兵异常还原。**节奏、守卫、业务判断一律不在这里**——节奏是平台自己的事
// (不同平台的输入通道耗时差一个数量级),守卫按宪法只能在平台层核对世界状态。
import { PlatformError } from './types'
import type { ExecutionWorld } from './types'

/**
 * 页面内抛出的异常穿过 `executeScript` 的方式。
 *
 * Chrome 不会把页面函数里 throw 的 Error 原样带回来,所以页面代码把消息塞进
 * 返回值的这个键;这里识别出来再抛成真异常。键名是两端的约定,改一处必须改另一处。
 */
export const MAIN_ERROR_SENTINEL = '__recruitHelperMainError'

export interface InjectOptions {
  /** 在哪个世界执行。取自适配器声明,不在调用点写死。 */
  readonly world: ExecutionWorld
  /** 失败消息里的平台名,只给人读(例:「智联」)。 */
  readonly label: string
}

/**
 * 往标签页里注入一个自包含函数并取回它的返回值。
 *
 * **func 会被序列化送进页面,闭包变量到不了那边**;参数一律经 args 数组传递。
 * 写成 `() => foo(param)` 会让 param 在页面里 undefined,而且不报错——
 * executeScript 只会静默返回 undefined。
 *
 * 失效方向:拿不到结果一律 `CTX_NOT_READY` / `contentScriptDead`,由脑决定
 * 是先 ensureSurface 再来还是转人工。这里不猜、不重试、不降级返回空值。
 */
export async function runInPage<A extends unknown[], R>(
  options: InjectOptions,
  tabId: number,
  func: (...args: A) => R | Promise<R>,
  args: A,
): Promise<R> {
  const result = await chrome.scripting.executeScript({
    target: { tabId },
    world: options.world,
    func,
    args,
  })
  return unwrapInjection<R>(options.label, result[0])
}

/**
 * 拆 Chrome 的 InjectionResult。与 runInPage 分开导出,是为了让「先注入、
 * 再由调用方决定要不要记时」这类编排(例如节奏闸)不必复制这段拆包逻辑。
 */
export function unwrapInjection<R>(label: string, raw: unknown): R {
  const first = raw as { result?: R | null; error?: unknown } | undefined
  if (!first) {
    throw notReady(label, '页面脚本尚未就绪')
  }
  if (first.error !== undefined && first.error !== null) {
    let detail = ''
    if (typeof first.error === 'string') {
      detail = first.error.trim()
    } else if (typeof first.error === 'object') {
      try {
        const message = (first.error as { message?: unknown }).message
        if (typeof message === 'string') detail = message.trim()
      } catch {
        // Chrome 的 InjectionResult.error 形态尚未在所有版本稳定；不读取其他字段。
      }
    }
    const suffix = detail ? `：${detail.slice(0, 300)}` : ''
    throw notReady(label, `页面脚本执行失败${suffix}`)
  }
  if (first.result === undefined || first.result === null) {
    throw notReady(label, '页面脚本未返回结果')
  }
  const mainError = typeof first.result === 'object' && !Array.isArray(first.result)
    ? (first.result as Record<string, unknown>)[MAIN_ERROR_SENTINEL]
    : undefined
  if (typeof mainError === 'string' && mainError.length > 0) {
    // 页面内的原始异常。刻意抛裸 Error 而不是 PlatformError:它不是"页面没就绪",
    // 而是页面代码自己出的错,由上层按各自语义分类。
    throw new Error(mainError.slice(0, 300))
  }
  return first.result
}

function notReady(label: string, what: string): PlatformError {
  return new PlatformError('CTX_NOT_READY', `${label}${what}`, 'afterRecovery', 'contentScriptDead')
}
