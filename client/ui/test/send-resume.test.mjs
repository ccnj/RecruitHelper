import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/send-resume.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/send-resume.mjs',
  logLevel: 'error',
})
const moduleUrl = pathToFileURL(process.cwd() + '/test/dist/send-resume.mjs').href
const { acknowledgeSendIntent, readSendResume } = await import(moduleUrl)

class MemoryStorage {
  #values = new Map()
  get length() { return this.#values.size }
  clear() { this.#values.clear() }
  getItem(key) { return this.#values.get(key) ?? null }
  key(index) { return [...this.#values.keys()][index] ?? null }
  removeItem(key) { this.#values.delete(key) }
  setItem(key, value) { this.#values.set(key, String(value)) }
  values() { return [...this.#values.values()] }
}

let fail = 0
const check = (condition, message) => {
  console.log(condition ? '  PASS' : '  FAIL', message)
  if (!condition) fail++
}

const storage = new MemoryStorage()
const targetA = 'zhilian\u001faccount-a\u001fconversation-a'
const targetB = 'zhilian\u001faccount-a\u001fconversation-b'
check(readSendResume(targetA, storage).acknowledgedIntentId === '', '未人工确认时没有 acknowledged intentId')

acknowledgeSendIntent(targetA, 'intent-a', storage)
check(readSendResume(targetA, storage).acknowledgedIntentId === 'intent-a', '显式人工确认后记录 acknowledged latest')
check(readSendResume(targetB, storage).acknowledgedIntentId === '', '不同会话的人工确认严格隔离')
check(storage.values().every((value) => value === 'intent-a'), 'sessionStorage 只保存已确认 intentId，不保存正文或账号材料')

const failingStorage = new MemoryStorage()
failingStorage.setItem = () => { throw new Error('quota') }
let blocked = false
try { acknowledgeSendIntent(targetA, 'intent-c', failingStorage) } catch { blocked = true }
check(blocked, '人工确认写失败时 M3 编辑器可继续保持锁定')

const lyingStorage = new MemoryStorage()
lyingStorage.setItem = () => {}
blocked = false
try { acknowledgeSendIntent(targetA, 'intent-c', lyingStorage) } catch { blocked = true }
check(blocked, '人工确认未稳定回读时 M3 编辑器可继续保持锁定')

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
process.exit(fail === 0 ? 0 : 1)
