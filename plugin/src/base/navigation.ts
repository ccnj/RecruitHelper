// 命令导航与真人导航的 SW 内存窗口。它不持久化、不自行定时；过期项只在下一次
// 导航/输入时惰性清理。pageNavigated 无论分类如何都照报，分类仅控制 manualInteraction。
import { DEFAULTS } from './protocol'
import { MANUAL_EMIT_MIN_MS } from './contentMessages'

export type NavigationOrigin = 'command' | 'manual' | 'unknown'

interface WindowEntry {
  startedAt: number
  expiresAt: number
  token: number
}

interface PendingNavigation {
  at: number
  origin: NavigationOrigin
  url: string
}

export interface CommandNavigationWindow {
  end(): void
}

export class NavigationTracker {
  private commandWindows = new Map<number, WindowEntry>()
  private trustedIntents = new Map<number, number>()
  private pending = new Map<number, PendingNavigation>()
  private nextToken = 1

  constructor(private readonly now: () => number = Date.now) {}

  beginCommandNavigation(tabId: number, actionNotAfterMs: number): CommandNavigationWindow {
    const token = this.nextToken++
    const startedAt = this.now()
    this.commandWindows.set(tabId, {
      token,
      startedAt,
      // 归因窗口不得比真实动作资格活得更久。无效截止时间自然形成空窗口，
      // 后续导航只会得到 unknown，保持 fail closed。
      expiresAt: Number.isFinite(actionNotAfterMs) ? actionNotAfterMs : Number.NEGATIVE_INFINITY,
    })
    return {
      end: () => {
        const current = this.commandWindows.get(tabId)
        if (current?.token === token) this.commandWindows.delete(tabId)
      },
    }
  }

  noteTrustedNavigationIntent(tabId: number, at: number): void {
    if (!Number.isSafeInteger(at) || at < 0) return
    this.trustedIntents.set(tabId, at)
  }

  noteChromeNavigation(tabId: number, url: string, at: number): NavigationOrigin {
    const origin = this.classify(tabId, at)
    this.pending.set(tabId, { at, origin, url })
    return origin
  }

  resolveContentNavigation(tabId: number, url: string, at: number): NavigationOrigin {
    const pending = this.pending.get(tabId)
    if (pending && pending.url === url && at >= pending.at && at - pending.at <= DEFAULTS.execBudgetDefaultMs.intrusive) {
      this.pending.delete(tabId)
      return pending.origin
    }
    return this.classify(tabId, at)
  }

  removeTab(tabId: number): void {
    this.commandWindows.delete(tabId)
    this.trustedIntents.delete(tabId)
    this.pending.delete(tabId)
  }

  private classify(tabId: number, at: number): NavigationOrigin {
    const command = this.commandWindows.get(tabId)
    // 事件时间早于窗口创建时间，说明它属于更早的导航。它既不得消费窗口，
    // 也不得借一份窗口前的可信输入间接撤销窗口。
    if (command && at < command.startedAt) return 'unknown'

    // 真人可信输入优先于宽泛的命令窗口。用户若在自动切换等待期间主动导航，
    // 必须立即打断命令，而不能被窗口吞成 command。
    const trustedAt = this.trustedIntents.get(tabId)
    if (trustedAt !== undefined) {
      // webNavigation 可能乱序送达；早于可信输入的旧事件不得消费这份输入。
      if (at >= trustedAt) {
        this.trustedIntents.delete(tabId)
        if (at - trustedAt <= MANUAL_EMIT_MIN_MS &&
            (!command || trustedAt >= command.startedAt)) {
          this.commandWindows.delete(tabId)
          return 'manual'
        }
      }
    }
    if (command) {
      this.commandWindows.delete(tabId)
      if (at <= command.expiresAt) {
        return 'command'
      }
    }
    return 'unknown'
  }
}

export const navigationTracker = new NavigationTracker()

export function beginCommandNavigation(tabId: number, actionNotAfterMs: number): CommandNavigationWindow {
  return navigationTracker.beginCommandNavigation(tabId, actionNotAfterMs)
}
