// 诊断台入口闸的策略部分：口令、过期点、存量读写。与渲染分开，好让它能被
// 单测覆盖——会出错的是"什么时候算解锁过"，不是那个密码框长什么样。
//
// 这是减速带，不是安全边界：admin token 就在渲染进程里，会开 devtools 的人
// 绕得过去，而闸后面就是无护栏 SQL 控制台。它挡的是"客户的员工手滑按到
// 快捷键"，不是存心的人。真要当安全边界，得换成脑侧校验且前端不存明文。

export const CONSOLE_PASSPHRASE = 'ls'
const STORAGE_KEY = 'recruithelper.console.v1.unlockUntil'

// 过期点取每天 04:00 而不是午夜：业务窗口是 [08:00, 24:00)，按 24 点边界
// 裁决回复链允许跨点收尾几十秒，卡在午夜重置会正好在那条尾巴上弹密码框。
// 04:00 在任何尾巴之后、开窗之前。
const RESET_HOUR = 4

export function nextResetAt(now: Date): number {
  const reset = new Date(now)
  reset.setHours(RESET_HOUR, 0, 0, 0)
  if (reset.getTime() <= now.getTime()) reset.setDate(reset.getDate() + 1)
  return reset.getTime()
}

// 隐私模式下连读 localStorage 都会抛，所以 try 必须裹住访问本身。
// 拿不到存储就降级为每次都问——问多了只是麻烦，漏问才是失效。
export function lockStorage(): Storage | null {
  try {
    if (typeof localStorage === 'undefined') return null
    return localStorage
  } catch { return null }
}

export function consoleUnlocked(now: Date = new Date(), storage = lockStorage()): boolean {
  if (!storage) return false
  let raw: string | null = null
  try { raw = storage.getItem(STORAGE_KEY) } catch { return false }
  const until = Number(raw)
  if (!raw || !Number.isFinite(until) || until <= now.getTime()) return false
  // 存量不可能比"从此刻算起的下一个重置点"更远。更远说明值被人改过或系统
  // 时钟跳过，一律当过期——不让一个坏值把闸永久打开。
  return until <= nextResetAt(now)
}

export function rememberUnlock(now: Date = new Date(), storage = lockStorage()): void {
  if (!storage) return
  try { storage.setItem(STORAGE_KEY, String(nextResetAt(now))) } catch { /* 存不下就下次再问 */ }
}
