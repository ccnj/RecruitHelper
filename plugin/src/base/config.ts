// 基础设施配置与身份。手侧持久化只放脑地址与稳定 handId，
// 禁存任何业务状态(宪法禁令 2)。handId 是本地随机标识，不是凭据。
import { TRANSPORT, DEFAULTS } from './protocol'

// bootId:SW 每次出生新生成的内存值,是"手记忆连续性"的唯一指示器(重连收编据此)。
// 模块级常量 → 随 SW 生命周期,SW 被杀重生即换新值。
export const BOOT_ID = 'b-' + crypto.randomUUID().replace(/-/g, '').slice(0, 16)

// SW 出生时刻(debug.ping 回显用)。
export const SW_STARTED_AT = Date.now()

interface StoredConfig {
  wsUrl?: string
  handId?: string
}

const KEY = 'infra' // chrome.storage.local 下唯一的基础设施键
let handIdPromise: Promise<string> | null = null

async function readAll(): Promise<StoredConfig> {
  const o = await chrome.storage.local.get(KEY)
  return (o[KEY] as StoredConfig) ?? {}
}

async function writeAll(c: StoredConfig): Promise<void> {
  await chrome.storage.local.set({ [KEY]: c })
}

export async function getWsUrl(): Promise<string> {
  const c = await readAll()
  return normalizeLocalWsUrl(
    c.wsUrl ?? `${TRANSPORT.scheme}://${TRANSPORT.host}:${TRANSPORT.portDefault}${TRANSPORT.path}`,
  )
}

export async function setWsUrl(url: string): Promise<string> {
  const normalized = normalizeLocalWsUrl(url)
  // 先等稳定标识落盘，避免首次启动时两个基础设施写相互覆盖。
  const handId = await getHandId()
  await writeAll({ wsUrl: normalized, handId })
  return normalized
}

// 本地可信是脑手握手不做 token 的前提，因此可配置地址也必须留在 loopback。
// 端口可以调整，但协议、路径、凭据和查询片段不能借配置面改变。
export function normalizeLocalWsUrl(raw: string): string {
  let parsed: URL
  try {
    parsed = new URL(raw.trim())
  } catch {
    throw new Error('脑服务地址不是合法 URL')
  }
  const localHost = parsed.hostname === '127.0.0.1' || parsed.hostname === 'localhost'
  const explicitPort = parsed.port === '' ? null : Number(parsed.port)
  if (
    parsed.protocol !== 'ws:' || !localHost ||
    parsed.pathname !== TRANSPORT.path || parsed.username !== '' || parsed.password !== '' ||
    parsed.search !== '' || parsed.hash !== '' ||
    (explicitPort !== null && (!Number.isInteger(explicitPort) || explicitPort < 1 || explicitPort > 65_535))
  ) {
    throw new Error(`脑服务地址只允许 ws://127.0.0.1 或 ws://localhost 的 ${TRANSPORT.path}`)
  }
  return parsed.toString()
}

// 一次 SW 生命内共享同一 Promise：并发首读只能生成/写入一次。
// 模块重载后则从 chrome.storage.local 读回同一值。
export function getHandId(): Promise<string> {
  if (handIdPromise === null) {
    handIdPromise = loadOrCreateHandId().catch((error: unknown) => {
      // 瞬时存储失败不应把本次 SW 永久卡在 rejected Promise。
      handIdPromise = null
      throw error
    })
  }
  return handIdPromise
}

async function loadOrCreateHandId(): Promise<string> {
  const c = await readAll()
  const handId = typeof c.handId === 'string' && c.handId.length > 0 && c.handId.length <= 128
    ? c.handId
    : `hand-${crypto.randomUUID().replace(/-/g, '').slice(0, 24)}`
  // 只回写当前基础设施字段，同时自动清掉旧版本留下的无关键。
  await writeAll({ ...(c.wsUrl ? { wsUrl: c.wsUrl } : {}), handId })
  return handId
}

// 重连退避与看门狗参数(连不上脑时唯一需要手侧自主的数,故硬编码自 DEFAULTS)。
export const RECONNECT = DEFAULTS.reconnect
// welcome 只证明握手完成；同一 session 连续存活满此窗口，才证明链路足够稳定
// 可以清零指数退避。该值是手侧连接状态机常量，不进入脑手机器契约。
export const RECONNECT_STABLE_MS = 60_000
export const HB_INTERVAL_MS = DEFAULTS.hbIntervalMs
export const HELLO_TIMEOUT_MS = DEFAULTS.helloTimeoutMs

export function newMsgId(): string {
  return 'm-' + crypto.randomUUID().replace(/-/g, '').slice(0, 24)
}
