// 验证 Electron 壳的脑服务生命周期管理(start→就绪→stop),不需 Electron/显示。
// 默认自行构建隔离的 braind；也可传入：node test/service.test.mjs <braind二进制> <数据目录> [port]
import { createRequire } from 'node:module'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
const require = createRequire(import.meta.url)
const { BrainService } = require('../service.js')

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..')
const ownedDir = process.argv[2] ? null : mkdtempSync(join(tmpdir(), 'recruithelper-electron-test-'))
const bin = process.argv[2] || join(ownedDir, 'braind')
const dataDir = process.argv[3] || join(ownedDir, 'data')
const port = process.argv[4] || String(19000 + (process.pid % 1000))
const adminBase = `http://127.0.0.1:${port}`

if (ownedDir) {
  const built = spawnSync('go', ['build', '-o', bin, './client/service'], {
    cwd: repoRoot,
    env: { ...process.env, GOCACHE: process.env.GOCACHE || '/private/tmp/recruithelper-gocache' },
    stdio: 'inherit',
  })
  if (built.status !== 0) {
    rmSync(ownedDir, { recursive: true, force: true })
    process.exit(built.status || 1)
  }
}

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

// spawn 本身失败(二进制被杀毒软件拦截/不存在)必须走 onSpawnError 回调,
// 而不是未捕获异常 —— 2026-08-10 客户机现场,用户看到的是 Electron 英文
// 异常框 "Error: spawn UNKNOWN"。回调没接住的话本测试进程会直接崩掉。
const spawnErrors = []
const bad = new BrainService({
  bin: join(tmpdir(), `no-such-brain-${process.pid}`),
  onLog: () => {},
  onSpawnError: (e) => spawnErrors.push(e),
})
bad.start()
await new Promise((res) => setTimeout(res, 300))
check(spawnErrors.length === 1, 'spawn 失败触发一次 onSpawnError')
check(!bad.running(), 'spawn 失败后 running() 为假')

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
if (ownedDir) rmSync(ownedDir, { recursive: true, force: true })
process.exit(fail === 0 ? 0 : 1)
