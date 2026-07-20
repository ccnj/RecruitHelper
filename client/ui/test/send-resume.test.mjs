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
const {
  acknowledgeSendIntent, discardRejectedSendProposal, readSendResume, rememberSendProposal,
} = await import(moduleUrl)

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
rememberSendProposal(targetA, 'intent-a', storage)
rememberSendProposal(targetB, 'intent-b', storage)
check(readSendResume(targetA, storage).proposalIntentId === 'intent-a', 'reload 可按会话恢复 proposal intentId')
check(readSendResume(targetB, storage).proposalIntentId === 'intent-b', '不同会话的发送凭证严格隔离')
check(storage.values().every((value) => /^intent-[ab]$/.test(value)), 'sessionStorage value 只包含 intentId，不保存正文或账号材料')

acknowledgeSendIntent(targetA, 'intent-a', storage)
const acknowledged = readSendResume(targetA, storage)
check(acknowledged.proposalIntentId === '' && acknowledged.acknowledgedIntentId === 'intent-a', '显式人工确认后才清 proposal 并记录 acknowledged latest')

const failingStorage = new MemoryStorage()
failingStorage.setItem = () => { throw new Error('quota') }
let blocked = false
try { rememberSendProposal(targetA, 'intent-c', failingStorage) } catch { blocked = true }
check(blocked, 'sessionStorage 写失败时调用方可在 POST 前 fail-closed')

const lyingStorage = new MemoryStorage()
lyingStorage.setItem = () => {}
blocked = false
try { rememberSendProposal(targetA, 'intent-c', lyingStorage) } catch { blocked = true }
check(blocked, 'sessionStorage 未稳定回读时调用方可在 POST 前 fail-closed')

const rejectedStorage = new MemoryStorage()
rememberSendProposal(targetA, 'intent-rejected', rejectedStorage)
discardRejectedSendProposal(targetA, 'intent-rejected', rejectedStorage)
check(readSendResume(targetA, rejectedStorage).proposalIntentId === '',
  '脑明确在创建前拒绝后只清除匹配的 proposal')

rememberSendProposal(targetA, 'intent-newer', rejectedStorage)
blocked = false
try { discardRejectedSendProposal(targetA, 'intent-older', rejectedStorage) } catch { blocked = true }
check(blocked && readSendResume(targetA, rejectedStorage).proposalIntentId === 'intent-newer',
  '迟到拒绝不得清除已经变化的 proposal')

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
process.exit(fail === 0 ? 0 : 1)
