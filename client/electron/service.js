// 脑服务生命周期管理(可独立于 Electron 测试)。Electron 主进程只负责窗口 + 启停这个 Go 服务
// (三层职责硬边界:壳只管窗口与进程,逻辑全在 Go 服务)。对应旧项目 python_service.ts 的角色,
// 但内嵌 Python 换成 Go 单二进制,分发管线整条消失。
'use strict'
const { spawn } = require('node:child_process')

class BrainService {
  /**
   * @param {{
   *   bin:string, args?:string[], cwd?:string, env?:Record<string,string|undefined>,
   *   onLog?:(line:string)=>void, onSpawnError?:(error:Error)=>void,
   * }} opts
   */
  constructor(opts) {
    this.opts = opts
    this.proc = null
  }

  start() {
    if (this.proc) return
    const { bin, args = [], cwd, env } = this.opts
    const child = spawn(bin, args, { cwd, env, stdio: ['ignore', 'pipe', 'pipe'] })
    this.proc = child
    const pipe = (stream) => {
      stream.on('data', (b) => {
        const s = String(b).trimEnd()
        if (s && this.opts.onLog) this.opts.onLog(s)
      })
    }
    pipe(child.stdout)
    pipe(child.stderr)
    // 'error' 不接住就是 Node 的未捕获异常,Electron 会弹英文原始异常框 ——
    // 2026-08-10 客户机真机现场:杀毒软件拦截脑二进制的执行,用户看到的是一句
    // "Error: spawn UNKNOWN"。pid 为空说明进程根本没起来(spawn 本身失败),
    // 交给壳去弹人话;pid 已存在的运行期错误(如 kill 失败)只记日志。
    child.on('error', (error) => {
      if (this.opts.onLog) this.opts.onLog(`[service] 进程错误:${error.message}`)
      if (child.pid !== undefined) return
      if (this.proc === child) this.proc = null
      if (this.opts.onSpawnError) this.opts.onSpawnError(error)
    })
    child.on('exit', (code) => {
      if (this.opts.onLog) this.opts.onLog(`[service] 退出 code=${code}`)
      if (this.proc === child) this.proc = null
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
