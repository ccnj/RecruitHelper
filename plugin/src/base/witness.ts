// 真实外部副作用的持久证词层。这里只保存协议标识符、终局信封与有限期证词，
// 不保存业务决策输入；program 层不可见，也不得直接访问 chrome.storage。
import {
  DEFAULTS,
  JournalEntry,
  JournalSnapshot,
  JournalState,
  OutboxEntry,
  ResultStatus,
  ResultEnvelope,
  WitnessStoreMeta,
  WitnessUnavailableReason,
  validateSchema,
} from './protocol'

const META_KEY = 'witness:meta'
const JOURNAL_PREFIX = 'journal:'
const OUTBOX_PREFIX = 'outbox:'
const DAY_MS = 24 * 60 * 60 * 1000

export interface WitnessStorage {
  get(keys?: string | string[] | null): Promise<Record<string, unknown>>
  set(items: Record<string, unknown>): Promise<void>
  remove(keys: string | string[]): Promise<void>
}

export interface WitnessAdvertisement {
  witnessStoreId: string
  outboxPending: number
  journalOpen: number
}

export class WitnessStoreError extends Error {
  constructor(readonly reason: WitnessUnavailableReason, message: string) {
    super(message)
    this.name = 'WitnessStoreError'
  }
}

// 读回校验的降级档位(协议规格 §9.5)。A 档不在此列——它直接抛 storeCorrupt。
// B = 无法排除"有记录已消失",旋转 witnessStoreId 后继续服务;
// C = 记录都在、身份都对,只是附属字段不自洽,原样继续。
type DegradeTier = 'B' | 'C'

interface WitnessDegrade {
  tier: DegradeTier
  // 触发身份 = 键名 + 判据。换代闩锁按它记账并跨世代保留(§9.5 处置纪律第 9 条):
  // 按世代记账会让持久触发在换代产生的新世代里重新算作新触发,永不收敛。
  trigger: string
  detail: string
}

interface WitnessLoad {
  meta: WitnessStoreMeta
  journals: Map<string, JournalEntry>
  outbox: Map<string, OutboxEntry>
  // storage 中实际存在的键,含因 B 档而未装进内存视图的条目。key 集缩小
  // 必须以它为准,否则"隔离不装载"会立刻被误判成"记录消失"(§9.5 B 档第 6 条)。
  storageJournalKeys: Set<string>
  storageOutboxKeys: Set<string>
  // C 档就地修正后待回写的条目,键为完整 storage key。
  repairs: Record<string, unknown>
  degrades: WitnessDegrade[]
}

export class WitnessStore {
  private meta: WitnessStoreMeta | null = null
  private journals = new Map<string, JournalEntry>()
  private outbox = new Map<string, OutboxEntry>()
  private state: 'new' | 'ready' | 'corrupt' = 'new'
  private initialization: Promise<void> | null = null
  private serial: Promise<void> = Promise.resolve()
  // 换代闩锁:已经为某个触发换过代就不再重复换。跨世代保留,只随 SW 生命周期
  // 重置(§9.5 处置纪律第 9 条)。它绝不能退化成"我已降级故不再换代"——那样
  // 此后每一次真实的记录丢失都会停在旧 storeId 下,脑随即安全重投,直接多发。
  private rotatedFor = new Set<string>()

  constructor(
    private readonly storage: WitnessStorage,
    private readonly now: () => number = Date.now,
    private readonly newStoreId: () => string = () =>
      `witness-${crypto.randomUUID().replace(/-/g, '').slice(0, 24)}`,
  ) {}

  async initialize(): Promise<void> {
    if (this.state === 'ready') return
    if (this.state === 'corrupt') throw this.storeCorrupt('证词库已被判定损坏')
    if (this.initialization) return this.initialization
    this.initialization = this.exclusive(async () => {
      if (this.state === 'ready') return
      await this.loadOrCreateLocked()
    }).catch((error: unknown) => {
      if (this.state !== 'corrupt') this.initialization = null
      throw asWitnessError(error)
    })
    return this.initialization
  }

  advertisement(): WitnessAdvertisement | null {
    if (this.state !== 'ready' || !this.meta) return null
    return {
      witnessStoreId: this.meta.storeId,
      outboxPending: this.outbox.size,
      journalOpen: [...this.journals.values()].filter((entry) => entry.state === JournalState.Attempting).length,
    }
  }

  async findJournalByIdemKey(idemKey: string, expectedRef?: string): Promise<JournalEntry | null> {
    return this.withReady(async () => {
      await this.reloadLocked()
      const entry = this.journals.get(idemKey) ?? null
      if (entry && expectedRef !== undefined && entry.ref !== expectedRef) {
        this.state = 'corrupt'
        throw this.storeCorrupt('同一 idemKey 的既有 ref 与当前命令不一致')
      }
      return clone(entry)
    })
  }

  async findJournalByRef(ref: string): Promise<JournalEntry | null> {
    return this.withReady(async () => {
      await this.reloadLocked()
      for (const entry of this.journals.values()) {
        if (entry.ref === ref) return clone(entry)
      }
      return null
    })
  }

  async markAttempting(ref: string, idemKey: string): Promise<JournalEntry> {
    return this.withReady(async () => {
      await this.reloadLocked()
      const existing = this.journals.get(idemKey)
      if (existing) {
        if (existing.ref !== ref) {
          this.state = 'corrupt'
          throw this.storeCorrupt('同一 idemKey 对应了不同命令 ref，拒绝覆盖证词')
        }
        return clone(existing)
      }
      if (this.journals.size >= DEFAULTS.witnessCapacity || this.outbox.size >= DEFAULTS.witnessCapacity) {
        throw new WitnessStoreError(
          WitnessUnavailableReason.CapacityExceeded,
          `不可逆动作证词容量不足: journal=${this.journals.size}, outbox=${this.outbox.size}, ` +
          `上限=${DEFAULTS.witnessCapacity}`,
        )
      }
      const startedAt = this.now()
      const entry: JournalEntry = {
        ref,
        idemKey,
        state: JournalState.Attempting,
        startedAt,
        expiresAt: startedAt + DEFAULTS.journalTtlDays * DAY_MS,
      }
      const meta: WitnessStoreMeta = {
        ...this.requireMeta(),
        journalCount: this.journals.size + 1,
      }
      assertSchema('JournalEntry', entry)
      assertSchema('WitnessStoreMeta', meta)
      await this.write({ [journalKey(idemKey)]: entry, [META_KEY]: meta })
      this.meta = meta
      this.journals.set(idemKey, entry)
      return clone(entry)
    })
  }

  // attempting 写点后的完整终局（无论 ok/failed 与 sideEffect 取值）必须把
  // committed journal 与对应 result outbox 在同一次 chrome.storage.local.set
  // 中落盘；任一失败都不能推进内存视图。committed 证明终局持久，不证明副作用发生。
  async commitAndEnqueue(idemKey: string, message: ResultEnvelope): Promise<JournalEntry> {
    return this.withReady(async () => {
      await this.reloadLocked()
      const existing = this.journals.get(idemKey)
      if (!existing) {
        this.state = 'corrupt'
        throw this.storeCorrupt('attempting 证词丢失，拒绝伪造 committed')
      }
      if (message.body.ref !== existing.ref) {
        this.state = 'corrupt'
        throw this.storeCorrupt('committed result.ref 与 attempting ref 不一致')
      }
      if (existing.state === JournalState.Committed) {
        if (JSON.stringify(existing.result) !== JSON.stringify(message.body)) {
          this.state = 'corrupt'
          throw this.storeCorrupt('同一 committed journal 出现不同终局')
        }
        return clone(existing)
      }
      if (this.outbox.has(message.msgId)) {
        this.state = 'corrupt'
        throw this.storeCorrupt('新 committed result 的 outbox msgId 已存在')
      }
      if (this.outbox.size >= DEFAULTS.witnessCapacity) {
        throw new WitnessStoreError(
          WitnessUnavailableReason.CapacityExceeded,
          `outbox 已达容量上限 ${DEFAULTS.witnessCapacity}`,
        )
      }
      const committedAt = this.now()
      const committed: JournalEntry = {
        ...existing,
        state: JournalState.Committed,
        committedAt,
        result: clone(message.body),
      }
      const outboxEntry: OutboxEntry = {
        message: clone(message),
        createdAt: committedAt,
        expiresAt: committedAt + DEFAULTS.outboxTtlDays * DAY_MS,
      }
      const meta: WitnessStoreMeta = {
        ...this.requireMeta(),
        outboxCount: this.outbox.size + 1,
      }
      assertSchema('JournalEntry', committed)
      assertSchema('OutboxEntry', outboxEntry)
      assertSchema('WitnessStoreMeta', meta)
      await this.write({
        [journalKey(idemKey)]: committed,
        [outboxKey(message.msgId)]: outboxEntry,
        [META_KEY]: meta,
      })
      this.meta = meta
      this.journals.set(idemKey, committed)
      this.outbox.set(message.msgId, outboxEntry)
      return clone(committed)
    })
  }

  async enqueueResult(message: ResultEnvelope): Promise<void> {
    await this.withReady(async () => {
      await this.reloadLocked()
      const journals = [...this.journals.values()].filter((entry) => entry.ref === message.body.ref)
      const journal = journals[0]
      if (message.body.status === ResultStatus.Ok || journal) {
        const correlatedBody = message.body.replayed
          ? { ...message.body, replayed: false }
          : message.body
        if (journals.length !== 1 || journal.state !== JournalState.Committed || !journal.result ||
            JSON.stringify(journal.result) !== JSON.stringify(correlatedBody)) {
          this.state = 'corrupt'
          throw this.storeCorrupt('拒绝把终局 outbox 关联到 attempting 或不同终局的 journal')
        }
      }
      const existing = this.outbox.get(message.msgId)
      if (existing) {
        if (JSON.stringify(existing.message) !== JSON.stringify(message)) {
          this.state = 'corrupt'
          throw this.storeCorrupt('同一 outbox msgId 对应不同信封')
        }
        return
      }
      if (this.outbox.size >= DEFAULTS.witnessCapacity) {
        throw new WitnessStoreError(
          WitnessUnavailableReason.CapacityExceeded,
          `outbox 已达容量上限 ${DEFAULTS.witnessCapacity}`,
        )
      }
      const createdAt = this.now()
      const entry: OutboxEntry = {
        message: clone(message),
        createdAt,
        expiresAt: createdAt + DEFAULTS.outboxTtlDays * DAY_MS,
      }
      const meta: WitnessStoreMeta = {
        ...this.requireMeta(),
        outboxCount: this.outbox.size + 1,
      }
      assertSchema('OutboxEntry', entry)
      assertSchema('WitnessStoreMeta', meta)
      await this.write({ [outboxKey(message.msgId)]: entry, [META_KEY]: meta })
      this.meta = meta
      this.outbox.set(message.msgId, entry)
    })
  }

  async listOutbox(): Promise<OutboxEntry[]> {
    return this.withReady(async () => {
      await this.reloadLocked()
      return [...this.outbox.values()]
        .sort((a, b) => a.createdAt - b.createdAt)
        .map((entry) => clone(entry))
    })
  }

  // 跨会话补投前先把当前 session/attempt/ts 的变化落盘；msgId/body 保持不变。
  async nextOutboxAttempt(msgId: string, currentSession: string): Promise<ResultEnvelope | null> {
    return this.withReady(async () => {
      await this.reloadLocked()
      const existing = this.outbox.get(msgId)
      if (!existing) return null
      const updated: OutboxEntry = {
        ...existing,
        message: {
          ...existing.message,
          session: currentSession,
          attempt: existing.message.attempt + 1,
          ts: this.now(),
        },
      }
      assertSchema('OutboxEntry', updated)
      await this.write({ [outboxKey(msgId)]: updated })
      this.outbox.set(msgId, updated)
      return clone(updated.message)
    })
  }

  // ack 即脑账本已持久该终局，此后脑不再对该命令 query 或重投；outbox 与
  // 对应 committed journal 在同一次删除中收割，journal 的 TTL 只兜底从未
  // 获 ack 的条目。删除/计数崩溃缝由下次读取的 required-count 校验判 corrupt。
  async acknowledgeResult(msgId: string): Promise<void> {
    await this.withReady(async () => {
      await this.reloadLocked()
      const existing = this.outbox.get(msgId)
      if (!existing) return
      const ref = existing.message.body.ref
      let journalIdemKey: string | null = null
      for (const [idemKey, entry] of this.journals) {
        if (entry.ref !== ref) continue
        if (entry.state !== JournalState.Committed) {
          this.state = 'corrupt'
          throw this.storeCorrupt(`ack 目标的同 ref journal 仍为 attempting: ${msgId}`)
        }
        journalIdemKey = idemKey
        break
      }
      const keys = [outboxKey(msgId)]
      if (journalIdemKey !== null) keys.push(journalKey(journalIdemKey))
      await this.erase(keys)
      let meta: WitnessStoreMeta = {
        ...this.requireMeta(),
        outboxCount: this.outbox.size - 1,
        journalCount: this.journals.size - (journalIdemKey === null ? 0 : 1),
      }
      assertSchema('WitnessStoreMeta', meta)
      try {
        await this.write({ [META_KEY]: meta })
      } catch (error) {
        // remove 已经成功、旧 count 与真实 key 数不再一致。留在同一 storeId 下
        // 继续运行会把这份差额伪装成 unknown,但整库其余条目仍可解释——按 B 档
        // 换代后继续服务(§9.5 B 档第 5 条),不必熔断。换代自身失败才升 A 档。
        meta = await this.rotateLocked(meta, [`ack-meta-write:${msgId}`])
        console.warn(
          `[witness] B 档降级 trigger=ack-meta-write:${msgId} ack 收割后 meta 更新失败: ${message(error)}`,
        )
      }
      this.meta = meta
      this.outbox.delete(msgId)
      if (journalIdemKey !== null) this.journals.delete(journalIdemKey)
    })
  }

  journalSnapshot(entry: JournalEntry): JournalSnapshot {
    const snapshot: JournalSnapshot = {
      ref: entry.ref,
      idemKey: entry.idemKey,
      state: entry.state,
      startedAt: entry.startedAt,
    }
    if (entry.committedAt !== undefined) snapshot.committedAt = entry.committedAt
    assertSchema('JournalSnapshot', snapshot)
    return snapshot
  }

  private async withReady<T>(operation: () => Promise<T>): Promise<T> {
    await this.initialize()
    return this.exclusive(operation)
  }

  private exclusive<T>(operation: () => Promise<T>): Promise<T> {
    const result = this.serial.then(operation, operation)
    this.serial = result.then(() => undefined, () => undefined)
    return result
  }

  private async loadOrCreateLocked(): Promise<void> {
    const all = await this.readAll()
    const rawMeta = all[META_KEY]
    const hasEntries = Object.keys(all).some(isWitnessEntryKey)
    if (rawMeta === undefined) {
      if (hasEntries) {
        this.state = 'corrupt'
        throw this.storeCorrupt('证词条目存在但 meta 缺失')
      }
      const createdAt = this.now()
      const meta: WitnessStoreMeta = {
        storeId: this.newStoreId(),
        createdAt,
        schemaVersion: DEFAULTS.witnessSchemaVersion,
        journalCount: 0,
        outboxCount: 0,
      }
      assertSchema('WitnessStoreMeta', meta)
      await this.write({ [META_KEY]: meta })
      this.meta = meta
      this.journals.clear()
      this.outbox.clear()
      this.state = 'ready'
      return
    }
    await this.applyLoad(this.loadValidated(all, rawMeta))
    await this.pruneExpiredLocked()
    this.state = 'ready'
  }

  // 每个关键读写重新读取证词命名空间，防止用户/浏览器清空 storage 后仍沿用旧 storeId。
  private async reloadLocked(): Promise<void> {
    if (this.state === 'corrupt') throw this.storeCorrupt('证词库已被判定损坏')
    const all = await this.readAll()
    const rawMeta = all[META_KEY]
    if (rawMeta === undefined) {
      this.state = 'corrupt'
      throw this.storeCorrupt('运行中证词 meta 消失，连续性已断')
    }
    const previousStoreId = this.meta?.storeId
    const load = this.loadValidated(all, rawMeta)
    // A 档:出现既非旧值、也非手自己刚写入之值的第三个 storeId。自检必须排在
    // applyLoad 之前——手自己的 B 档换代发生在其后,不能把自己烧成 A 档
    // (§9.5 处置纪律第 2 条;现有 TTL 换代能过关也正因为它排在自检之后)。
    if (previousStoreId && load.meta.storeId !== previousStoreId) {
      this.state = 'corrupt'
      throw this.storeCorrupt('运行中 witnessStoreId 发生变化')
    }
    await this.applyLoad(load)
    await this.pruneExpiredLocked()
  }

  // 读回校验只负责"解释 + 定档",不再自己熔断 B/C 档。A 档(去重信息本身
  // 不可用)仍就地抛 storeCorrupt;其余归入 WitnessLoad.degrades,由 applyLoad
  // 按 §9.5 处置纪律收束。
  private loadValidated(all: Record<string, unknown>, rawMeta: unknown): WitnessLoad {
    if (validateSchema('WitnessStoreMeta', rawMeta).length > 0) {
      this.state = 'corrupt'
      throw this.storeCorrupt('证词 meta 违反协议 schema')
    }
    const meta = rawMeta as WitnessStoreMeta
    if (meta.schemaVersion !== DEFAULTS.witnessSchemaVersion) {
      this.state = 'corrupt'
      throw this.storeCorrupt(`不支持证词 schemaVersion=${meta.schemaVersion}`)
    }
    const now = this.now()
    const journals = new Map<string, JournalEntry>()
    const outbox = new Map<string, OutboxEntry>()
    const storageJournalKeys = new Set<string>()
    const storageOutboxKeys = new Set<string>()
    const repairs: Record<string, unknown> = {}
    const degrades: WitnessDegrade[] = []
    const refs = new Set<string>()
    for (const [key, value] of Object.entries(all)) {
      if (key.startsWith(JOURNAL_PREFIX)) {
        // 键仍在 = 记录仍在 storage,与内容能否读懂无关。key 集缩小以此为准。
        storageJournalKeys.add(key.slice(JOURNAL_PREFIX.length))
        const issues = validateSchema('JournalEntry', value)
        if (issues.length > 0) {
          // A 档优先:生成的 validator 里混着身份关联判据(当前唯一一条是
          // "result.ref 必须等于 journal.ref")。它命中说明这条 journal 的终局
          // 挂在别的命令身上,是身份被篡改,不是"这条读不懂"(§9.5 B 档第 1 条)。
          const correlation = issues.find((issue) => issue.rule === 'correlation')
          if (correlation) {
            this.state = 'corrupt'
            throw this.storeCorrupt(`journal 身份关联损坏: ${key} (${correlation.message})`)
          }
          // B 档:这一条读不懂,但全库其余条目仍可解释。就地留在 storage,
          // 不装进内存视图——它因此不再参与去重,安全性由本档换代承担。
          degrades.push({ tier: 'B', trigger: `journal-schema:${key}`, detail: 'journal 条目违反 schema' })
          continue
        }
        const entry = value as JournalEntry
        if (key !== journalKey(entry.idemKey) || journals.has(entry.idemKey)) {
          degrades.push({ tier: 'B', trigger: `journal-key:${key}`, detail: 'journal 键与内容不一致' })
          continue
        }
        // A 档:committed 的终局挂在别的命令身上,是身份被篡改,继续服务等于蒙眼作答。
        if (entry.state === JournalState.Committed && entry.result?.ref !== entry.ref) {
          this.state = 'corrupt'
          throw this.storeCorrupt(`committed journal 的 result.ref 不相关: ${key}`)
        }
        if (refs.has(entry.ref)) {
          this.state = 'corrupt'
          throw this.storeCorrupt(`多个 journal 复用了同一命令 ref: ${key}`)
        }
        // C 档:时间字段只服务 TTL 清理,不参与去重。就地修正后照常装载。
        const repairedJournal = repairJournalTime(entry, now)
        if (repairedJournal) {
          degrades.push({
            tier: 'C',
            trigger: `journal-time:${key}`,
            detail: `startedAt=${entry.startedAt} committedAt=${entry.committedAt ?? '-'} ` +
              `expiresAt=${entry.expiresAt} → startedAt=${repairedJournal.startedAt} ` +
              `expiresAt=${repairedJournal.expiresAt}`,
          })
          repairs[key] = repairedJournal
        }
        const loaded = repairedJournal ?? entry
        refs.add(loaded.ref)
        journals.set(loaded.idemKey, clone(loaded))
      } else if (key.startsWith(OUTBOX_PREFIX)) {
        storageOutboxKeys.add(key.slice(OUTBOX_PREFIX.length))
        if (validateSchema('OutboxEntry', value).length > 0) {
          degrades.push({ tier: 'B', trigger: `outbox-schema:${key}`, detail: 'outbox 条目违反 schema' })
          continue
        }
        const entry = value as OutboxEntry
        if (key !== outboxKey(entry.message.msgId) || outbox.has(entry.message.msgId)) {
          degrades.push({ tier: 'B', trigger: `outbox-key:${key}`, detail: 'outbox 键与内容不一致' })
          continue
        }
        const repairedOutbox = repairOutboxTime(entry, now)
        if (repairedOutbox) {
          degrades.push({
            tier: 'C',
            trigger: `outbox-time:${key}`,
            detail: `createdAt=${entry.createdAt} expiresAt=${entry.expiresAt} → ` +
              `expiresAt=${repairedOutbox.expiresAt}`,
          })
          repairs[key] = repairedOutbox
        }
        outbox.set(entry.message.msgId, clone(repairedOutbox ?? entry))
      } else if (key.startsWith('witness:') && key !== META_KEY) {
        // C 档:陌生键不读、不写、不删、不计入容量。它不影响任何已知记录。
        degrades.push({ tier: 'C', trigger: `unknown-key:${key}`, detail: '未知证词键,已忽略' })
      }
    }
    // C 档:条目过多只让去重更保守。新写入仍由 markAttempting/enqueueResult
    // 的 capacityExceeded 闸拦住(§9.1 第 3 条不变),已有记录照常服务。
    if (journals.size > DEFAULTS.witnessCapacity || outbox.size > DEFAULTS.witnessCapacity) {
      degrades.push({
        tier: 'C',
        trigger: `capacity:${journals.size}:${outbox.size}`,
        detail: `条目数量超过容量上限 ${DEFAULTS.witnessCapacity}`,
      })
    }
    const journalShortfall = meta.journalCount - storageJournalKeys.size
    const outboxShortfall = meta.outboxCount - storageOutboxKeys.size
    if (journalShortfall > 0 || outboxShortfall > 0) {
      // B 档:账面有、实物没有。SW 重启后已无内存证据区分"我按计划删的"与
      // "记录悄悄丢了",一律按后者保守处理。差额数值参与触发身份,差额每
      // 扩大一次都是一次新触发,必须各自再换一次代。
      degrades.push({
        tier: 'B',
        trigger: `count-shortfall:${journalShortfall}:${outboxShortfall}`,
        detail: `计数大于实际 key 数: journal=${meta.journalCount}/${storageJournalKeys.size}, ` +
          `outbox=${meta.outboxCount}/${storageOutboxKeys.size}`,
      })
    } else if (meta.journalCount !== storageJournalKeys.size || meta.outboxCount !== storageOutboxKeys.size) {
      // C 档:实物比账面多,只会让去重更保守。以实际 key 集为准改写 meta。
      degrades.push({
        tier: 'C',
        trigger: `count-surplus:${storageJournalKeys.size}:${storageOutboxKeys.size}`,
        detail: `计数小于实际 key 数: journal=${meta.journalCount}/${storageJournalKeys.size}, ` +
          `outbox=${meta.outboxCount}/${storageOutboxKeys.size}`,
      })
    }
    for (const [msgId, entry] of outbox) {
      const correlatedJournals = [...journals.values()]
        .filter((candidate) => candidate.ref === entry.message.body.ref)
      const journal = correlatedJournals[0]
      // barrier 前的非 ok 终局没有 journal，是合法的零副作用路径；ok 或任何
      // 已有关联 journal 的终局都必须证明 atomic committed 相关性。
      if (entry.message.body.status !== ResultStatus.Ok && correlatedJournals.length === 0) continue
      // B 档:关联破裂最可能的成因就是那条 journal 已经不在了。
      if (correlatedJournals.length !== 1 || journal.state !== JournalState.Committed || !journal.result) {
        degrades.push({
          tier: 'B',
          trigger: `outbox-link:${msgId}`,
          detail: '终局 outbox 缺少唯一 committed journal',
        })
        continue
      }
      // 原始终局必须逐字段完全一致。协议要求的 dedup 重放只允许把
      // replayed 从 false 改为 true，其余字段仍必须与同一 committed 事实一致。
      const correlatedBody = entry.message.body.replayed
        ? { ...entry.message.body, replayed: false }
        : entry.message.body
      if (JSON.stringify(journal.result) !== JSON.stringify(correlatedBody)) {
        degrades.push({
          tier: 'B',
          trigger: `outbox-terminal:${msgId}`,
          detail: 'outbox 与 committed journal 终局不一致',
        })
      }
    }
    // 同一 SW 生命周期内 key 集缩小 = 记录可能已消失,走 B 档换代(§9.5 B 档
    // 第 6 条)。判据以 storage 实际 key 为准:因 B 档隔离而未装进内存视图的
    // 条目仍在 storage 里,不算缩小,否则"隔离不装载"会立刻自升 A 档。
    if (this.state === 'ready') {
      const missingJournal = [...this.journals.keys()].find((key) => !storageJournalKeys.has(key))
      const missingOutbox = [...this.outbox.keys()].find((key) => !storageOutboxKeys.has(key))
      if (missingJournal || missingOutbox) {
        degrades.push({
          tier: 'B',
          trigger: `keyset-shrunk:${missingJournal ?? ''}:${missingOutbox ?? ''}`,
          detail: '运行中证词 key 集缩小',
        })
      }
    }
    return {
      meta: clone(meta),
      journals,
      outbox,
      storageJournalKeys,
      storageOutboxKeys,
      repairs,
      degrades,
    }
  }

  // B 档换代:旋转 witnessStoreId 并落盘。脑看到换代只会增加验证/人工,绝不会
  // 把 report=unknown 当零副作用证明,所以换代本身就是一道完备的防多发闸
  // (§9.5 判据)。闩锁按触发身份记账并跨世代保留——按世代记账会让持久触发在
  // 新世代里重新算作新触发,永不收敛(§9.5 处置纪律第 9 条)。
  private async rotateLocked(base: WitnessStoreMeta, triggers: string[]): Promise<WitnessStoreMeta> {
    const nextStoreId = this.newStoreId()
    if (!nextStoreId || nextStoreId === base.storeId) {
      this.state = 'corrupt'
      throw this.storeCorrupt('B 档换代未生成新的 witnessStoreId')
    }
    const rotated: WitnessStoreMeta = { ...base, storeId: nextStoreId, createdAt: this.now() }
    assertSchema('WitnessStoreMeta', rotated)
    try {
      await this.write({ [META_KEY]: rotated })
    } catch (error) {
      // 换代自身写不进去时没有任何安全的继续方式:留在旧 storeId 下,脑会把
      // 后续的 report=unknown 当成零副作用证明并安全重投。只能升 A 档。
      this.state = 'corrupt'
      throw this.storeCorrupt(`B 档换代写入失败: ${message(error)}`)
    }
    this.meta = rotated
    for (const trigger of triggers) this.rotatedFor.add(trigger)
    return rotated
  }

  // applyLoad 按 §9.5 处置纪律第 2 条的顺序收束一次读回:先换代并落盘,再
  // 修正计数与 C 档条目,最后才装载内存视图、恢复服务。
  private async applyLoad(load: WitnessLoad): Promise<void> {
    let meta = load.meta
    const fresh = load.degrades.filter(
      (degrade) => degrade.tier === 'B' && !this.rotatedFor.has(degrade.trigger),
    )
    if (fresh.length > 0) {
      meta = await this.rotateLocked(meta, fresh.map((degrade) => degrade.trigger))
    }
    const counted: WitnessStoreMeta = {
      ...meta,
      journalCount: load.storageJournalKeys.size,
      outboxCount: load.storageOutboxKeys.size,
    }
    const pending: Record<string, unknown> = { ...load.repairs }
    if (counted.journalCount !== meta.journalCount || counted.outboxCount !== meta.outboxCount) {
      assertSchema('WitnessStoreMeta', counted)
      pending[META_KEY] = counted
    }
    if (Object.keys(pending).length > 0) {
      // 修正写失败不升档:内存视图已是修正后的值,下次读回会重新命中同一
      // 判据并再修一次。C 档的全部作用只是让 TTL 与计数自洽。
      try {
        await this.write(pending)
      } catch (error) {
        console.warn('[witness] 降级修正回写失败,将在下次读取重试', message(error))
      }
    }
    this.meta = counted
    this.journals = load.journals
    this.outbox = load.outbox
    for (const degrade of load.degrades) {
      console.warn(
        `[witness] ${degrade.tier} 档降级 trigger=${degrade.trigger} ${degrade.detail}`,
      )
    }
  }

  private async pruneExpiredLocked(): Promise<void> {
    const now = this.now()
    const expiredJournals: string[] = []
    const expiredOutbox: string[] = []
    for (const [idemKey, entry] of this.journals) {
      if (entry.expiresAt <= now) expiredJournals.push(journalKey(idemKey))
    }
    for (const [msgId, entry] of this.outbox) {
      if (entry.expiresAt <= now) expiredOutbox.push(outboxKey(msgId))
    }
    const expired = [...expiredJournals, ...expiredOutbox]
    if (expired.length === 0) return

    // TTL 代表证词代际边界，不是同库内的静默删除。必须先把新 storeId
    // 持久化，让脑能观察到代际变化；随后删除与 count 更新之间若崩溃，
    // 下次启动会因 required count 不匹配而熔断，绝不会伪造 same-store unknown。
    const previous = this.requireMeta()
    const nextStoreId = this.newStoreId()
    if (!nextStoreId || nextStoreId === previous.storeId) {
      this.state = 'corrupt'
      throw this.storeCorrupt('TTL 换代未生成新的 witnessStoreId')
    }
    const rotated: WitnessStoreMeta = {
      ...previous,
      storeId: nextStoreId,
      createdAt: now,
    }
    assertSchema('WitnessStoreMeta', rotated)
    await this.write({ [META_KEY]: rotated })
    this.meta = rotated

    await this.erase(expired)
    let updated: WitnessStoreMeta = {
      ...rotated,
      journalCount: this.journals.size - expiredJournals.length,
      outboxCount: this.outbox.size - expiredOutbox.length,
    }
    assertSchema('WitnessStoreMeta', updated)
    try {
      await this.write({ [META_KEY]: updated })
    } catch (error) {
      // 删除已生效、计数没落盘。本轮清理开头已经换过一次代,但那次换代的
      // storeId 已经对外宣告过;差额是本次新出现的事实,按 §9.5 处置纪律第 9 条
      // 属新触发,必须再换一次代,不能因"本世代已换过"而豁免。
      updated = await this.rotateLocked(updated, [`ttl-meta-write:${expired.length}`])
      console.warn(
        `[witness] B 档降级 trigger=ttl-meta-write:${expired.length} TTL 删除后 meta 更新失败: ${message(error)}`,
      )
    }
    this.meta = updated
    for (const key of expired) {
      if (key.startsWith(JOURNAL_PREFIX)) this.journals.delete(key.slice(JOURNAL_PREFIX.length))
      if (key.startsWith(OUTBOX_PREFIX)) this.outbox.delete(key.slice(OUTBOX_PREFIX.length))
    }
  }

  private requireMeta(): WitnessStoreMeta {
    if (!this.meta) {
      this.state = 'corrupt'
      throw this.storeCorrupt('证词库 meta 未就绪')
    }
    return this.meta
  }

  private async readAll(): Promise<Record<string, unknown>> {
    try {
      return await this.storage.get(null)
    } catch (error) {
      throw new WitnessStoreError(WitnessUnavailableReason.WriteFailed, `读取证词库失败: ${message(error)}`)
    }
  }

  private async write(items: Record<string, unknown>): Promise<void> {
    try {
      await this.storage.set(items)
    } catch (error) {
      throw new WitnessStoreError(WitnessUnavailableReason.WriteFailed, `写入证词库失败: ${message(error)}`)
    }
  }

  private async erase(keys: string | string[]): Promise<void> {
    try {
      await this.storage.remove(keys)
    } catch (error) {
      throw new WitnessStoreError(WitnessUnavailableReason.WriteFailed, `清理证词库失败: ${message(error)}`)
    }
  }

  private storeCorrupt(detail: string): WitnessStoreError {
    return new WitnessStoreError(WitnessUnavailableReason.StoreCorrupt, detail)
  }
}

// 时间字段只服务 TTL 清理,不参与去重,因此不自洽时就地修正、照常装载
// (§9.5 C 档第 1 条)。只动时间字段:ref/idemKey/state/result 一律不碰。
function repairJournalTime(entry: JournalEntry, now: number): JournalEntry | null {
  const broken = entry.expiresAt <= entry.startedAt ||
    (entry.committedAt !== undefined &&
      (entry.committedAt < entry.startedAt || entry.committedAt >= entry.expiresAt))
  if (!broken) return null
  // 哪个才是真正的开始时刻无从判断,取较早者最保守——它只会让 TTL 更早到期,
  // 不会延长记录寿命。
  const startedAt = entry.committedAt !== undefined
    ? Math.min(entry.startedAt, entry.committedAt)
    : entry.startedAt
  // expiresAt 必须同时晚于 committedAt 与当前时刻:否则紧随其后的
  // pruneExpired 会立刻删掉它,把隔离变成删除(§9.5 处置纪律第 3 条)。
  const base = Math.max(now, entry.committedAt ?? startedAt)
  return { ...entry, startedAt, expiresAt: base + DEFAULTS.journalTtlDays * DAY_MS }
}

function repairOutboxTime(entry: OutboxEntry, now: number): OutboxEntry | null {
  if (entry.expiresAt > entry.createdAt) return null
  const base = Math.max(now, entry.createdAt)
  return { ...entry, expiresAt: base + DEFAULTS.outboxTtlDays * DAY_MS }
}

function journalKey(idemKey: string): string {
  return JOURNAL_PREFIX + idemKey
}

function outboxKey(msgId: string): string {
  return OUTBOX_PREFIX + msgId
}

function isWitnessEntryKey(key: string): boolean {
  return key.startsWith(JOURNAL_PREFIX) || key.startsWith(OUTBOX_PREFIX) || key.startsWith('witness:')
}

function assertSchema(name: string, value: unknown): void {
  const issues = validateSchema(name, value)
  if (issues.length > 0) {
    throw new WitnessStoreError(
      WitnessUnavailableReason.StoreCorrupt,
      `${name} 违反协议 schema: ${issues.map((issue) => `${issue.path}:${issue.rule}`).join(',')}`,
    )
  }
}

function asWitnessError(error: unknown): WitnessStoreError {
  return error instanceof WitnessStoreError
    ? error
    : new WitnessStoreError(WitnessUnavailableReason.WriteFailed, message(error))
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function clone<T>(value: T): T {
  return value === null || value === undefined ? value : structuredClone(value)
}
