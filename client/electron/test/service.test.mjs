// 验证 Electron 壳的脑服务生命周期管理(start→就绪→stop),不需 Electron/显示。
// 用法:node test/service.test.mjs <braind二进制> <数据目录> [port]
import { createRequire } from 'node:module'
const require = createRequire(import.meta.url)
const { BrainService } = require('../service.js')

const bin = process.argv[2]
const dataDir = process.argv[3]
const port = process.argv[4] || '17899'
const adminBase = `http://127.0.0.1:${port}`

let fail = 0
const check = (c, m) => { console.log(c ? '  PASS' : '  FAIL', m); if (!c) fail++ }

const svc = new BrainService({ bin, args: ['-port', port, '-data', dataDir], onLog: () => {} })
svc.start()
check(svc.running(), 'start 后进程在运行')

const healthy = await svc.waitHealthy(adminBase, 8000)
check(healthy, '脑服务在期限内 admin/health 就绪')

const r = await fetch(adminBase + '/admin/health').then((x) => x.json()).catch(() => ({}))
check(r.ok === true, 'health 返回 ok')

svc.stop()
await new Promise((res) => setTimeout(res, 500))
check(!svc.running(), 'stop 后进程已停')
const afterStop = await fetch(adminBase + '/admin/health').then(() => true).catch(() => false)
check(afterStop === false, 'stop 后服务不再响应')

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
process.exit(fail === 0 ? 0 : 1)
