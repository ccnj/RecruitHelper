// UI 数据层集成测试:打包 api.ts,对真脑跑关键端点,确认 UI 依赖的契约与 CORS 成立。
// 用法:先起脑 go run ./client/service -port 17872,再 node test/api.test.mjs
import * as esbuild from 'esbuild'
import { pathToFileURL } from 'node:url'
import { mkdirSync } from 'node:fs'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/api.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/api.mjs',
  logLevel: 'error',
})
const { api } = await import(pathToFileURL(process.cwd() + '/test/dist/api.mjs').href)

let fail = 0
const check = (c, m) => { console.log(c ? '  PASS' : '  FAIL', m); if (!c) fail++ }

const h = await api.health()
check(h.ok === true && typeof h.proto === 'number' && typeof h.contract === 'string', 'health 返回 ok/proto/contract')

const op = await api.openPairing()
check(op.open === true, 'openPairing 开窗成功')

const p = await api.pending()
check(p.open === true && Array.isArray(p.pending), 'pending 返回 open + 数组')

const hh = await api.handsHealth()
check(Array.isArray(hh.hands), 'handsHealth 返回 hands 数组')

const led = await api.ledger()
check(Array.isArray(led.ledger), 'ledger 返回数组')

const sus = await api.suspects()
check(Array.isArray(sus.suspects), 'suspects 返回数组')

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
process.exit(fail === 0 ? 0 : 1)
