import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
import assert from 'node:assert/strict'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/console/lock-state.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/console-lock-state.mjs',
  logLevel: 'error',
})
const moduleUrl = pathToFileURL(`${process.cwd()}/test/dist/console-lock-state.mjs`).href
const { consoleUnlocked, nextResetAt, rememberUnlock } = await import(moduleUrl)

function fakeStorage(initial = {}) {
  const map = new Map(Object.entries(initial))
  return {
    getItem: (key) => (map.has(key) ? map.get(key) : null),
    setItem: (key, value) => { map.set(key, String(value)) },
    removeItem: (key) => { map.delete(key) },
    _dump: () => Object.fromEntries(map),
  }
}

function hostileStorage() {
  return {
    getItem() { throw new Error('隐私模式') },
    setItem() { throw new Error('隐私模式') },
  }
}

const tests = []
function test(name, fn) { tests.push([name, fn]) }

// 过期点是每天 04:00：08:00 开窗前、跨 24 点收尾之后。
test('04:00 之前解锁只管到当天 04:00', () => {
  const at = new Date(2026, 6, 31, 2, 30)
  assert.equal(nextResetAt(at), new Date(2026, 6, 31, 4, 0, 0, 0).getTime())
})

test('04:00 之后解锁管到次日 04:00', () => {
  const at = new Date(2026, 6, 31, 10, 0)
  assert.equal(nextResetAt(at), new Date(2026, 7, 1, 4, 0, 0, 0).getTime())
})

test('正好 04:00 算已过当日重置点，顺延到次日', () => {
  const at = new Date(2026, 6, 31, 4, 0, 0, 0)
  assert.equal(nextResetAt(at), new Date(2026, 7, 1, 4, 0, 0, 0).getTime())
})

test('解锁后当天不再问', () => {
  const storage = fakeStorage()
  const at = new Date(2026, 6, 31, 10, 0)
  rememberUnlock(at, storage)
  assert.equal(consoleUnlocked(new Date(2026, 6, 31, 23, 59), storage), true)
})

test('过了次日 04:00 就要重新输', () => {
  const storage = fakeStorage()
  rememberUnlock(new Date(2026, 6, 31, 10, 0), storage)
  assert.equal(consoleUnlocked(new Date(2026, 7, 1, 4, 1), storage), false)
})

test('没有存量时必须问', () => {
  assert.equal(consoleUnlocked(new Date(2026, 6, 31, 10, 0), fakeStorage()), false)
})

// 被人改过或系统时钟跳过的值不许把闸永久打开。
test('存量比下一个重置点还远，一律当过期', () => {
  const storage = fakeStorage({
    'recruithelper.console.v1.unlockUntil': String(new Date(2099, 0, 1).getTime()),
  })
  assert.equal(consoleUnlocked(new Date(2026, 6, 31, 10, 0), storage), false)
})

test('存量是垃圾字符串时当过期，不抛', () => {
  const storage = fakeStorage({ 'recruithelper.console.v1.unlockUntil': '不是数字' })
  assert.equal(consoleUnlocked(new Date(2026, 6, 31, 10, 0), storage), false)
})

test('存量是空串时当过期（Number("") 是 0 不是 NaN）', () => {
  const storage = fakeStorage({ 'recruithelper.console.v1.unlockUntil': '' })
  assert.equal(consoleUnlocked(new Date(2026, 6, 31, 10, 0), storage), false)
})

// 拿不到存储就降级为每次都问：问多了只是麻烦，漏问才是失效。
test('存储不可用时判未解锁，且写入不抛', () => {
  assert.equal(consoleUnlocked(new Date(2026, 6, 31, 10, 0), null), false)
  assert.equal(consoleUnlocked(new Date(2026, 6, 31, 10, 0), hostileStorage()), false)
  assert.doesNotThrow(() => rememberUnlock(new Date(2026, 6, 31, 10, 0), hostileStorage()))
  assert.doesNotThrow(() => rememberUnlock(new Date(2026, 6, 31, 10, 0), null))
})

let failed = 0
for (const [name, fn] of tests) {
  try { fn(); console.log(`  PASS ${name}`) } catch (reason) {
    failed += 1
    console.error(`  FAIL ${name}\n    ${reason.message}`)
  }
}
console.log(failed === 0 ? '\nALL PASS' : `\n${failed} FAILED`)
process.exit(failed === 0 ? 0 : 1)
