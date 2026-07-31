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

export class WitnessStore {
  private meta: WitnessStoreMeta | null = null
  private journals = new Map<string, JournalEntry>()
  private outbox = new Map<string, OutboxEntry>()
  private state: 'new' | 'ready' | 'corrupt' = 'new'
  private initialization: Promise<void> | null = null
  private serial: Promise<void> = Promise.resolve()

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
      const meta: WitnessStoreMeta = {
        ...this.requireMeta(),
        outboxCount: this.outbox.size - 1,
        journalCount: this.journals.size - (journalIdemKey === null ? 0 : 1),
      }
      assertSchema('WitnessStoreMeta', meta)
      try {
        await this.write({ [META_KEY]: meta })
      } catch (error) {
        // remove 已经成功，此时旧 count 与真实 key 数不一致；继续运行会把丢失
        // 伪装成 unknown，必须立刻熔断为 corrupt。
        this.state = 'corrupt'
        throw this.storeCorrupt(`ack 收割后 meta 更新失败: ${message(error)}`)
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
    this.loadValidated(all, rawMeta)
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
    this.loadValidated(all, rawMeta)
    if (previousStoreId && this.meta?.storeId !== previousStoreId) {
      this.state = 'corrupt'
      throw this.storeCorrupt('运行中 witnessStoreId 发生变化')
    }
    await this.pruneExpiredLocked()
  }

  private loadValidated(all: Record<string, unknown>, rawMeta: unknown): void {
    if (validateSchema('WitnessStoreMeta', rawMeta).length > 0) {
      this.state = 'corrupt'
      throw this.storeCorrupt('证词 meta 违反协议 schema')
    }
    const meta = rawMeta as WitnessStoreMeta
    if (meta.schemaVersion !== DEFAULTS.witnessSchemaVersion) {
      this.state = 'corrupt'
      throw this.storeCorrupt(`不支持证词 schemaVersion=${meta.schemaVersion}`)
    }
    const journals = new Map<string, JournalEntry>()
    const outbox = new Map<string, OutboxEntry>()
    const refs = new Set<string>()
    for (const [key, value] of Object.entries(all)) {
      if (key.startsWith(JOURNAL_PREFIX)) {
        if (validateSchema('JournalEntry', value).length > 0) {
          this.state = 'corrupt'
          throw this.storeCorrupt(`journal 条目损坏: ${key}`)
        }
        const entry = value as JournalEntry
        if (key !== journalKey(entry.idemKey) || journals.has(entry.idemKey)) {
          this.state = 'corrupt'
          throw this.storeCorrupt(`journal 键与内容不一致: ${key}`)
        }
        if (entry.state === JournalState.Committed && entry.result?.ref !== entry.ref) {
          this.state = 'corrupt'
          throw this.storeCorrupt(`committed journal 的 result.ref 不相关: ${key}`)
        }
        if (refs.has(entry.ref)) {
          this.state = 'corrupt'
          throw this.storeCorrupt(`多个 journal 复用了同一命令 ref: ${key}`)
        }
        if (entry.expiresAt <= entry.startedAt ||
            (entry.committedAt !== undefined &&
              (entry.committedAt < entry.startedAt || entry.committedAt >= entry.expiresAt))) {
          this.state = 'corrupt'
          throw this.storeCorrupt(`journal 时间关系损坏: ${key}`)
        }
        refs.add(entry.ref)
        journals.set(entry.idemKey, clone(entry))
      } else if (key.startsWith(OUTBOX_PREFIX)) {
        if (validateSchema('OutboxEntry', value).length > 0) {
          this.state = 'corrupt'
          throw this.storeCorrupt(`outbox 条目损坏: ${key}`)
        }
        const entry = value as OutboxEntry
        if (key !== outboxKey(entry.message.msgId) || outbox.has(entry.message.msgId)) {
          this.state = 'corrupt'
          throw this.storeCorrupt(`outbox 键与内容不一致: ${key}`)
        }
        if (entry.expiresAt <= entry.createdAt) {
          this.state = 'corrupt'
          throw this.storeCorrupt(`outbox 时间关系损坏: ${key}`)
        }
        outbox.set(entry.message.msgId, clone(entry))
      } else if (key.startsWith('witness:') && key !== META_KEY) {
        this.state = 'corrupt'
        throw this.storeCorrupt(`未知证词键: ${key}`)
      }
    }
    if (journals.size > DEFAULTS.witnessCapacity || outbox.size > DEFAULTS.witnessCapacity) {
      this.state = 'corrupt'
      throw this.storeCorrupt('证词条目数量超过协议容量')
    }
    if (meta.journalCount !== journals.size || meta.outboxCount !== outbox.size) {
      this.state = 'corrupt'
      throw this.storeCorrupt(
        `证词计数与真实 key 不一致: journal=${meta.journalCount}/${journals.size}, ` +
        `outbox=${meta.outboxCount}/${outbox.size}`,
      )
    }
    for (const [msgId, entry] of outbox) {
      const correlatedJournals = [...journals.values()]
        .filter((candidate) => candidate.ref === entry.message.body.ref)
      const journal = correlatedJournals[0]
      // barrier 前的非 ok 终局没有 journal，是合法的零副作用路径；ok 或任何
      // 已有关联 journal 的终局都必须证明 atomic committed 相关性。
      if (entry.message.body.status !== ResultStatus.Ok && correlatedJournals.length === 0) continue
      if (correlatedJournals.length !== 1 || journal.state !== JournalState.Committed || !journal.result) {
        this.state = 'corrupt'
        throw this.storeCorrupt(`终局 outbox 缺少唯一 committed journal: ${msgId}`)
      }
      // 原始终局必须逐字段完全一致。协议要求的 dedup 重放只允许把
      // replayed 从 false 改为 true，其余字段仍必须与同一 committed 事实一致。
      const correlatedBody = entry.message.body.replayed
        ? { ...entry.message.body, replayed: false }
        : entry.message.body
      if (JSON.stringify(journal.result) !== JSON.stringify(correlatedBody)) {
        this.state = 'corrupt'
        throw this.storeCorrupt(`outbox 与 committed journal 终局不一致: ${msgId}`)
      }
    }
    // 同一 SW 生命周期内，即使有人同时改小 meta.count，既有 key 的消失仍不可
    // 被解释为合法清理；合法删除路径会先更新内存视图，再允许下一次 reload。
    if (this.state === 'ready') {
      const missingJournal = [...this.journals.keys()].find((key) => !journals.has(key))
      const missingOutbox = [...this.outbox.keys()].find((key) => !outbox.has(key))
      if (missingJournal || missingOutbox) {
        this.state = 'corrupt'
        throw this.storeCorrupt('运行中证词 key 集缩小，连续性已断')
      }
    }
    this.meta = clone(meta)
    this.journals = journals
    this.outbox = outbox
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
    const updated: WitnessStoreMeta = {
      ...rotated,
      journalCount: this.journals.size - expiredJournals.length,
      outboxCount: this.outbox.size - expiredOutbox.length,
    }
    assertSchema('WitnessStoreMeta', updated)
    try {
      await this.write({ [META_KEY]: updated })
    } catch (error) {
      this.state = 'corrupt'
      throw this.storeCorrupt(`TTL 删除后 meta 更新失败: ${message(error)}`)
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
