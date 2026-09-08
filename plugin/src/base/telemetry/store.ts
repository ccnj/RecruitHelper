// 观测数据的落盘层:分片环。
//
// 搬自 hiBoss `lab/extension/store.js`,存储语义一处未改,只做三项适配:
// TypeScript 化;键名统一加 `telemetry:` 前缀,避开手侧既有的 `infra`、
// reload marker 与 `witness:*`(上游用的是裸 `meta`/`u:0`/`c:0`,直接搬会撞);
// 去掉上游的版本迁移函数——我们是全新落地,没有遗留键可折。
//
// # 为什么必须分片
//
// 直觉写法是每来一条就 `get` 整个数组 → `push` → `set` 整个数组。200 条时每次
// 重写约 700KB,存到几千条(重度用户一天的量)就是**每次重写 10MB**、一分钟好几次,
// 而 `chrome.storage.local` 的序列化是同步发生的——撑不住。
//
// 按 `CHUNK` 条一片之后,**只有正在写的那一片会被重写**:单次写盘成本从 O(全部)
// 降到 O(一片),淘汰变成整片删除、不用搬数据。
//
// # 为什么单独成文件
//
// 上游的教训:第一版写侧在 background、读侧在面板抄了一份,两边都测不到,
// 而**存储 bug 是会丢数据的**。抽出来之后可以拿假的 storage 直接测这份代码本身。

/** `chrome.storage.local` 的最小面。抽成接口是为了能用假实现测本文件。 */
export interface TelemetryStorage {
  get(keys: string | readonly string[]): Promise<Record<string, unknown>>
  set(items: Record<string, unknown>): Promise<void>
  remove(keys: string | readonly string[]): Promise<void>
}

/** 每片多少条。**只有这一片会被重写**,所以这个数决定单次写盘的成本。 */
export const CHUNK = 200

/** 上报明细:15 片 x 200 = 3000 条。上游实测单条均 3.6KB,约 11MB。 */
export const MAX_UPLOAD_CHUNKS = 15

/** 鼠标窗口:25 片 x 200 = 5000 条。上游实测单条均 2.2KB,约 11MB。 */
export const MAX_CLICK_CHUNKS = 25

/**
 * 请求录制环:25 片 x 200 = 5000 条。一轮十分钟,平台页面几百到几千条,单条约 1~3KB。
 * 超出丢最旧整片——面板的条数会对不上开始时的计数,人看得出来。
 */
export const MAX_REQUEST_CHUNKS = 25

/** 明细环。 */
export const KIND_UPLOAD = 'u'

/** 请求录制环。每轮开始前整环清空,一轮一份记录。 */
export const KIND_REQUEST = 'r'

/** 轨迹环。单独一个环,免得被页面加载噪声挤掉(见 capture.ts)。 */
export const KIND_CLICK = 'c'

const META_KEY = 'telemetry:meta'

const chunkKey = (kind: string, n: number): string => `telemetry:${kind}:${n}`

/** 一种数据当前占用的片区间;`hi` 是正在写的那一片。 */
interface ChunkRange {
  lo: number
  hi: number
}

type Meta = Record<string, ChunkRange>

function readMeta(got: Record<string, unknown>): Meta {
  const raw = got[META_KEY]
  return typeof raw === 'object' && raw !== null ? { ...(raw as Meta) } : {}
}

function readChunk(got: Record<string, unknown>, key: string): unknown[] {
  const raw = got[key]
  return Array.isArray(raw) ? [...raw] : []
}

/** 追加。只读写当前片 + meta;超出片数上限时整片 remove,不搬数据。 */
export async function append(
  store: TelemetryStorage,
  kind: string,
  items: readonly unknown[],
  maxChunks: number,
): Promise<void> {
  if (!items.length) return

  const meta = readMeta(await store.get(META_KEY))
  const range: ChunkRange = meta[kind] ?? { lo: 0, hi: 0 }
  meta[kind] = range

  let key = chunkKey(kind, range.hi)
  let current = readChunk(await store.get(key), key)
  const write: Record<string, unknown> = {}

  for (const item of items) {
    current.push(item)
    if (current.length >= CHUNK) {
      // 这一片满了:落盘并开新片。
      write[key] = current
      range.hi += 1
      key = chunkKey(kind, range.hi)
      current = []
    }
  }
  write[key] = current
  write[META_KEY] = meta

  // 淘汰:`hi` 是正在写的那片,所以片数是 hi - lo + 1。
  const drop: string[] = []
  while (range.hi - range.lo + 1 > maxChunks) {
    drop.push(chunkKey(kind, range.lo))
    range.lo += 1
  }

  await store.set(write)
  if (drop.length) await store.remove(drop)
}

/** 读全部片并拼回一个数组。 */
export async function readAll(store: TelemetryStorage, kind: string): Promise<unknown[]> {
  const meta = readMeta(await store.get(META_KEY))
  const range = meta[kind]
  if (!range) return []

  const keys: string[] = []
  for (let n = range.lo; n <= range.hi; n += 1) keys.push(chunkKey(kind, n))

  const got = await store.get(keys)
  return keys.flatMap((k) => readChunk(got, k))
}

/** 当前存了多少条(不拼数组,给面板显示进度用)。 */
export async function count(store: TelemetryStorage, kind: string): Promise<number> {
  return (await readAll(store, kind)).length
}

/** 清空一种数据的全部片。观测数据不是业务事实,不受禁止物理删除约束。 */
export async function clear(store: TelemetryStorage, kind: string): Promise<void> {
  const meta = readMeta(await store.get(META_KEY))
  const range = meta[kind]
  if (!range) return

  const keys: string[] = []
  for (let n = range.lo; n <= range.hi; n += 1) keys.push(chunkKey(kind, n))
  await store.remove(keys)

  delete meta[kind]
  await store.set({ [META_KEY]: meta })
}
