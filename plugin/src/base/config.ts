// 基础设施配置与身份。手侧持久化只放基础设施数据(连接配置、工牌),禁存业务状态(宪法禁令 2)。
import { TRANSPORT, DEFAULTS } from './protocol'

// bootId:SW 每次出生新生成的内存值,是"手记忆连续性"的唯一指示器(重连收编据此)。
// 模块级常量 → 随 SW 生命周期,SW 被杀重生即换新值。
export const BOOT_ID = 'b-' + crypto.randomUUID().replace(/-/g, '').slice(0, 16)

// SW 出生时刻(debug.ping 回显用)。
export const SW_STARTED_AT = Date.now()

export interface HandCreds {
  handId: string
  token: string
}

interface StoredConfig {
  wsUrl?: string
  creds?: HandCreds
}

const KEY = 'infra' // chrome.storage.local 下唯一的基础设施键

async function readAll(): Promise<StoredConfig> {
  const o = await chrome.storage.local.get(KEY)
  return (o[KEY] as StoredConfig) ?? {}
}

async function writeAll(c: StoredConfig): Promise<void> {
  await chrome.storage.local.set({ [KEY]: c })
}

export async function getWsUrl(): Promise<string> {
  const c = await readAll()
  return c.wsUrl ?? `${TRANSPORT.scheme}://${TRANSPORT.host}:${TRANSPORT.portDefault}${TRANSPORT.path}`
}

export async function setWsUrl(url: string): Promise<void> {
  const c = await readAll()
  c.wsUrl = url
  await writeAll(c)
}

export async function getCreds(): Promise<HandCreds | undefined> {
  return (await readAll()).creds
}

export async function setCreds(creds: HandCreds): Promise<void> {
  const c = await readAll()
  c.creds = creds
  await writeAll(c)
}

export async function clearCreds(): Promise<void> {
  const c = await readAll()
  delete c.creds
  await writeAll(c)
}

// 重连退避与看门狗参数(连不上脑时唯一需要手侧自主的数,故硬编码自 DEFAULTS)。
export const RECONNECT = DEFAULTS.reconnect
export const HB_INTERVAL_MS = DEFAULTS.hbIntervalMs
export const HELLO_TIMEOUT_MS = DEFAULTS.helloTimeoutMs
export const PRE_SESSION_PING_MS = DEFAULTS.preSessionPingMs

export function newMsgId(): string {
  return 'm-' + crypto.randomUUID().replace(/-/g, '').slice(0, 24)
}
