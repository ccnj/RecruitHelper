// 命令分发器(base 层,手侧唯一命令入口)。测试端与正式调度器共用此路径(宪法 3)。
// 职责:dedup → 前置校验 → 经原语注册表执行 → ack/result。
import { Kind, AckStatus } from './protocol'
import { lookup, PrimitiveResult } from '../program/registry'

// 送帧回调:由 connection 提供,负责生成信封 msgId 并写 WS。
export type SendFn = (kind: string, session: string | null, body: unknown) => void

interface CmdBody {
  name: string
  ver: number
  args?: unknown
  idemKey?: string
  deadline: number
  execBudgetMs: number
}

interface DedupEntry {
  done: boolean
  resultBody?: unknown // 终局 result body,供重复投递重放
}

const DEDUP_CAP = 256

export class Dispatcher {
  // 去重表:内存,作用域=SW 一次生命(bootId)。键=cmd msgId。禁写 chrome.storage(宪法禁令 2/16)。
  private dedup = new Map<string, DedupEntry>()

  constructor(private send: SendFn) {}

  // 收到 cmd 信封。session 是当前会话(用于回执信封)。
  async handleCmd(cmdMsgId: string, session: string | null, body: CmdBody): Promise<void> {
    // 1) 去重:命中即重新 ack(静默去重是禁令),有终局则重放 result。
    const seen = this.dedup.get(cmdMsgId)
    if (seen) {
      this.send(Kind.Ack, session, { ref: cmdMsgId, status: AckStatus.Duplicate })
      if (seen.done && seen.resultBody) {
        this.send(Kind.Result, session, seen.resultBody)
      }
      return
    }

    // 2) 未知原语 → 显式拒绝(禁止默认成功)。
    const prim = lookup(body.name)
    if (!prim) {
      this.send(Kind.Ack, session, {
        ref: cmdMsgId,
        status: AckStatus.Rejected,
        error: { code: 'PROTO_UNSUPPORTED_CMD', message: `未知原语 ${body.name}`, sideEffect: 'none' },
      })
      return
    }

    // 3) 受理入队。
    this.remember(cmdMsgId, { done: false })
    this.send(Kind.Ack, session, { ref: cmdMsgId, status: AckStatus.Accepted })

    // 4) 出队前查 deadline,过期即回 expired(绝不执行)。
    if (typeof body.deadline === 'number' && Date.now() > body.deadline) {
      const rb = this.resultBody(cmdMsgId, { status: 'expired' }, 0)
      this.finish(cmdMsgId, session, rb)
      return
    }

    // 5) 执行原语。
    const start = Date.now()
    try {
      const outcome = await prim.handler(body.args, { cmdMsgId, deadlineMs: body.deadline })
      if (outcome === 'silent') {
        // 故意不回 result(演练超时/suspect);去重条目保持 executing。
        return
      }
      this.finish(cmdMsgId, session, this.resultBody(cmdMsgId, outcome, Date.now() - start))
    } catch (e) {
      // 原语内部异常:effectful 标 possible(拿不准),其余 none。诚实上报,不自作主张重试(禁令 4)。
      const sideEffect = prim.class === 'effectful' ? 'possible' : 'none'
      const rb = this.resultBody(cmdMsgId, {
        status: 'failed',
        error: { code: 'INTERNAL_HAND', message: String(e), sideEffect },
      }, Date.now() - start)
      this.finish(cmdMsgId, session, rb)
    }
  }

  private finish(cmdMsgId: string, session: string | null, resultBody: unknown): void {
    this.remember(cmdMsgId, { done: true, resultBody })
    this.send(Kind.Result, session, resultBody)
  }

  private resultBody(ref: string, r: PrimitiveResult, execMs: number): unknown {
    return {
      ref,
      status: r.status,
      data: r.data,
      error: r.error,
      evidence: r.evidence,
      replayed: false,
      execMs,
    }
  }

  private remember(key: string, entry: DedupEntry): void {
    this.dedup.set(key, entry)
    if (this.dedup.size > DEDUP_CAP) {
      const oldest = this.dedup.keys().next().value
      if (oldest !== undefined) this.dedup.delete(oldest)
    }
  }

  // 脑对 result 的 ack(brain→hand):M1 无持久 outbox,记日志即可。
  onResultAck(): void {
    /* no-op:M1 result 走内存,不需删持久队列 */
  }
}
