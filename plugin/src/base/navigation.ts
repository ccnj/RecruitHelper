// 命令导航与真人导航的 SW 内存窗口。它不持久化、不自行定时；过期项只在下一次
// 导航/输入时惰性清理。pageNavigated 无论分类如何都照报，分类仅控制 manualInteraction。
import { DEFAULTS } from './protocol'
import { MANUAL_EMIT_MIN_MS } from './contentMessages'

export type NavigationOrigin = 'command' | 'manual' | 'unknown'

interface WindowEntry {
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

  beginCommandNavigation(tabId: number): CommandNavigationWindow {
    const token = this.nextToken++
    this.commandWindows.set(tabId, {
      token,
      expiresAt: this.now() + DEFAULTS.execBudgetDefaultMs.intrusive,
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
    if (command) {
      this.commandWindows.delete(tabId)
      if (at <= command.expiresAt) {
        this.trustedIntents.delete(tabId)
        return 'command'
      }
    }
    const trustedAt = this.trustedIntents.get(tabId)
    if (trustedAt !== undefined) {
      this.trustedIntents.delete(tabId)
      if (at >= trustedAt && at - trustedAt <= MANUAL_EMIT_MIN_MS) return 'manual'
    }
    return 'unknown'
  }
}

export const navigationTracker = new NavigationTracker()

export function beginCommandNavigation(tabId: number): CommandNavigationWindow {
  return navigationTracker.beginCommandNavigation(tabId)
}
