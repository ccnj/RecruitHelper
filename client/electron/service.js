// 脑服务生命周期管理(可独立于 Electron 测试)。Electron 主进程只负责窗口 + 启停这个 Go 服务
// (三层职责硬边界:壳只管窗口与进程,逻辑全在 Go 服务)。对应旧项目 python_service.ts 的角色,
// 但内嵌 Python 换成 Go 单二进制,分发管线整条消失。
'use strict'
const { spawn } = require('node:child_process')

class BrainService {
  /** @param {{bin:string, args?:string[], cwd?:string, onLog?:(line:string)=>void}} opts */
  constructor(opts) {
    this.opts = opts
    this.proc = null
  }

  start() {
    if (this.proc) return
    const { bin, args = [], cwd, env } = this.opts
    this.proc = spawn(bin, args, { cwd, env, stdio: ['ignore', 'pipe', 'pipe'] })
    const pipe = (stream) => {
      stream.on('data', (b) => {
        const s = String(b).trimEnd()
        if (s && this.opts.onLog) this.opts.onLog(s)
      })
    }
    pipe(this.proc.stdout)
    pipe(this.proc.stderr)
    this.proc.on('exit', (code) => {
      if (this.opts.onLog) this.opts.onLog(`[service] 退出 code=${code}`)
      this.proc = null
    })
  }

  /** 轮询 admin/health 直到就绪或超时。 */
  async waitHealthy(adminBase, timeoutMs = 8000) {
    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      try {
        const r = await fetch(adminBase + '/admin/health')
        if (r.ok) {
          const j = await r.json()
          if (j.ok) return true
        }
      } catch {
        // 尚未起来,继续轮询
      }
      await new Promise((res) => setTimeout(res, 200))
    }
    return false
  }

  stop() {
    if (!this.proc) return
    this.proc.kill('SIGTERM')
    this.proc = null
  }

  running() {
    return this.proc !== null
  }
}

module.exports = { BrainService }
