// WS 连接生命周期(base 层):拨号、hello、心跳保活、断线重连、pre-session 保活。
// 传输连接方向手→脑(浏览器无法接受入站);语义方向永远脑→手。
import { PROTO_VERSION, CONTRACT_HASH, Kind, ByeCode } from './protocol'
import {
  BOOT_ID, RECONNECT, HB_INTERVAL_MS, HELLO_TIMEOUT_MS,
  getWsUrl, getCreds, setCreds, clearCreds, newMsgId,
} from './config'
import { capabilities } from '../program/registry'
import { Dispatcher } from './dispatcher'

type Phase = 'connecting' | 'preSession' | 'session' | 'closed'

export class Connection {
  private ws: WebSocket | null = null
  private session: string | null = null
  private phase: Phase = 'closed'
  private hbTimer: ReturnType<typeof setInterval> | null = null
  private helloTimer: ReturnType<typeof setTimeout> | null = null
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private reconnectDelay: number = RECONNECT.baseMs
  private dispatcher = new Dispatcher((k, s, b) => this.rawSend(k, s, b))

  status(): { phase: Phase; session: string | null; bootId: string } {
    return { phase: this.phase, session: this.session, bootId: BOOT_ID }
  }

  // ensureConnected:幂等启动/续连(background 的 alarm 看门狗与启动都调它)。
  ensureConnected(): void {
    if (this.phase === 'connecting' || this.phase === 'preSession' || this.phase === 'session') return
    this.connect()
  }

  private async connect(): Promise<void> {
    this.phase = 'connecting'
    const url = await getWsUrl()
    // 关掉可能残留的旧连接,避免"孤儿 socket"的迟到事件污染共享状态(真机 supersede 风暴根因)。
    if (this.ws) {
      try { this.ws.close() } catch { /* ignore */ }
      this.ws = null
    }
    try {
      const ws = new WebSocket(url)
      this.ws = ws
      // 陈旧处理器守卫:只有当前 ws 的事件才作数;被替换的旧 ws 迟到事件一律忽略。
      ws.onopen = () => { if (this.ws === ws) void this.onOpen() }
      ws.onmessage = (e) => { if (this.ws === ws) this.onMessage(e) }
      ws.onclose = () => { if (this.ws === ws) this.onClose() }
      ws.onerror = () => {} // onclose 随后触发,统一在那里处理
    } catch {
      this.scheduleReconnect()
    }
  }

  private async onOpen(): Promise<void> {
    const creds = await getCreds()
    this.rawSend(Kind.Hello, null, {
      handId: creds?.handId ?? null,
      auth: creds?.token ?? null,
      bootId: BOOT_ID,
      protoSupported: [PROTO_VERSION],
      contractHash: CONTRACT_HASH,
      app: { extVersion: chrome.runtime.getManifest().version, browser: 'chrome' },
      caps: capabilities(),
      features: [],
    })
    this.phase = 'preSession'
    // hello 应答超时:未收 welcome/bye 即断开重试。
    this.helloTimer = setTimeout(() => this.ws?.close(), HELLO_TIMEOUT_MS)
    // 心跳(pre-session 也发,session=null):WS 活动保活 SW。
    this.startHeartbeat()
  }

  private onMessage(e: MessageEvent): void {
    let env: { kind: string; msgId: string; session: string | null; body: any }
    try {
      env = JSON.parse(e.data as string)
    } catch {
      return
    }
    switch (env.kind) {
      case Kind.Welcome:
        void this.onWelcome(env.body)
        break
      case Kind.Bye:
        this.onBye(env.body)
        break
      case Kind.Pong:
        break
      case Kind.Cmd:
        void this.dispatcher.handleCmd(env.msgId, this.session, env.body)
        break
      case Kind.Ack:
        this.dispatcher.onResultAck()
        break
    }
  }

  private async onWelcome(body: { session: string; issued?: { handId: string; auth: string } }): Promise<void> {
    if (this.helloTimer) clearTimeout(this.helloTimer)
    this.session = body.session
    this.phase = 'session'
    this.reconnectDelay = RECONNECT.baseMs // 连接稳定,退避归零
    if (body.issued) {
      await setCreds({ handId: body.issued.handId, token: body.issued.auth })
      console.log('[hand] 配对成功', body.issued.handId)
    }
    console.log('[hand] 会话建立', body.session)
  }

  private onBye(body: { code: string; message?: string }): void {
    console.log('[hand] bye', body.code, body.message ?? '')
    if (body.code === ByeCode.AuthFailed) {
      // 工牌无效:清掉(下次 hello 走配对);慢重试等待客户端开启配对窗,不 hammer。
      void clearCreds()
      this.reconnectDelay = RECONNECT.capMs
    }
    if (body.code === ByeCode.Superseded) {
      // 已被更新的连接顶替:别立刻重连(否则与顶替者 ping-pong),退避到上限慢试。
      this.reconnectDelay = RECONNECT.capMs
    }
    // 其余(PAIRING_TIMEOUT 等):onclose 随后触发,按退避重连。
  }

  private onClose(): void {
    this.stopHeartbeat()
    if (this.helloTimer) clearTimeout(this.helloTimer)
    this.session = null
    this.phase = 'closed'
    this.ws = null
    this.scheduleReconnect()
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer) return
    const jitter = 1 + (Math.random() * 2 - 1) * RECONNECT.jitter
    const delay = Math.min(this.reconnectDelay * jitter, RECONNECT.capMs)
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      this.connect()
    }, delay)
    this.reconnectDelay = Math.min(this.reconnectDelay * RECONNECT.factor, RECONNECT.capMs)
  }

  private startHeartbeat(): void {
    this.stopHeartbeat()
    this.hbTimer = setInterval(() => {
      this.rawSend(Kind.Ping, this.session, { queueDepth: 0, inFlight: null })
    }, HB_INTERVAL_MS)
  }

  private stopHeartbeat(): void {
    if (this.hbTimer) {
      clearInterval(this.hbTimer)
      this.hbTimer = null
    }
  }

  private rawSend(kind: string, session: string | null, body: unknown): void {
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    const env = {
      proto: PROTO_VERSION,
      kind,
      msgId: newMsgId(),
      session,
      ts: Date.now(),
      attempt: 1,
      body,
    }
    ws.send(JSON.stringify(env))
  }
}
