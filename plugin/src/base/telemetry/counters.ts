// 把平台的输入行为账本(BOSS 的 `_ZP_CNT_`)取回来给诊断页看。
//
// **只读,而且不常驻。** 由诊断页按需触发一次 `chrome.scripting.executeScript`,
// 读完即返回;不设定时器、不注册监听、不给平台域加 content_scripts
// ——那会把 content.js 那套全文档 MutationObserver 拉起来,撞平台记的 rAF 帧率。
//
// 用 **isolated world**(executeScript 的默认值):isolated 与页面共享同源
// localStorage,读它不需要碰页面的 JS 世界,比 MAIN world 还轻一档。

import { BOSS_COUNTER_KEYS, BOSS_TAB_MATCH } from '../../program/platform/bossInputCounters'

const BASELINE_KEY = 'telemetry:counters:baseline'

export interface CounterSnapshot {
  /** 读取时刻(本机)。 */
  readAt: number
  /** 平台自己写盘的时刻,取自账本里的 `t`。**它可能远早于 readAt**——账本是批量写的。 */
  writtenAt: number | null
  /** 计数器现值。 */
  counts: Record<string, number>
  /** 附带的其他账本原文(如 rAF 帧率),不解析,只带出来看。 */
  extras: Record<string, string | null>
  /** 读不到时的说明。 */
  note?: string
}

export interface CounterStorage {
  get(keys: string | readonly string[]): Promise<Record<string, unknown>>
  set(items: Record<string, unknown>): Promise<void>
  remove(keys: string | readonly string[]): Promise<void>
}

/** 注入进页面的取样函数。**必须自包含**——会被序列化后在目标页求值。 */
function grab(keys: readonly string[]): {
  raw: Record<string, string | null>
  storageError?: string
} {
  const raw: Record<string, string | null> = {}
  // 读不到与「压根没这个键」必须分开:两者都返回 null 的话,面板会把
  // 「我们读不了页面存储」误报成「平台还没建账本」,而这两件事的处置完全不同。
  try {
    for (const k of keys) raw[k] = localStorage.getItem(k)
  } catch (error) {
    return { raw, storageError: String(error).slice(0, 200) }
  }
  return { raw }
}

/**
 * 把取回的 localStorage 原文解析成快照。**纯函数**——I/O 在 readCounters 里,
 * 这里只负责「拿到什么就如实变成什么」,好单测。
 */
export function parseLedger(
  raw: Record<string, string | null>,
  readAt: number,
): CounterSnapshot {
  const extras: Record<string, string | null> = {}
  for (const k of BOSS_COUNTER_KEYS.slice(1)) extras[k] = raw[k] ?? null

  const ledger = raw[BOSS_COUNTER_KEYS[0]]
  if (ledger === null || ledger === undefined) {
    return { readAt, writtenAt: null, counts: {}, extras, note: '页面上还没有这个账本 —— 平台要等到有过输入才会建。' }
  }

  let parsed: unknown
  try {
    parsed = JSON.parse(ledger)
  } catch (error) {
    return { readAt, writtenAt: null, counts: {}, extras, note: `账本不是 JSON: ${String(error).slice(0, 120)}` }
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { readAt, writtenAt: null, counts: {}, extras, note: '账本不是对象。' }
  }

  const counts: Record<string, number> = {}
  let writtenAt: number | null = null
  for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
    // `t` 是平台的写盘时刻,不是计数器;其余非数字字段一律忽略而不是当 0,
    // 免得平台哪天加了别的形态时我们静默把它算成「没异常」。
    if (k === 't') writtenAt = typeof v === 'number' ? v : null
    else if (typeof v === 'number') counts[k] = v
  }
  return { readAt, writtenAt, counts, extras }
}

export async function readCounters(): Promise<CounterSnapshot> {
  const readAt = Date.now()
  const empty: CounterSnapshot = { readAt, writtenAt: null, counts: {}, extras: {} }

  const tabs = await chrome.tabs.query({ url: [...BOSS_TAB_MATCH] })
  const tabId = tabs[0]?.id
  if (tabId === undefined) {
    return { ...empty, note: '没有打开着的平台标签页 —— 先把平台页面开着再读。' }
  }

  try {
    const [res] = await chrome.scripting.executeScript({
      target: { tabId },
      func: grab,
      args: [[...BOSS_COUNTER_KEYS]],
    })
    const out = res.result as { raw: Record<string, string | null>; storageError?: string }
    if (out.storageError !== undefined) {
      return { ...empty, note: `读不了页面存储(不是「账本不存在」): ${out.storageError}` }
    }
    return parseLedger(out.raw, readAt)
  } catch (error) {
    return { ...empty, note: `读取失败: ${String(error).slice(0, 200)}` }
  }
}

export async function readBaseline(store: CounterStorage): Promise<CounterSnapshot | null> {
  const stored = await store.get(BASELINE_KEY)
  return (stored[BASELINE_KEY] as CounterSnapshot | undefined) ?? null
}

export async function setBaseline(store: CounterStorage, snapshot: CounterSnapshot): Promise<void> {
  await store.set({ [BASELINE_KEY]: snapshot })
}

export async function clearBaseline(store: CounterStorage): Promise<void> {
  await store.remove(BASELINE_KEY)
}
