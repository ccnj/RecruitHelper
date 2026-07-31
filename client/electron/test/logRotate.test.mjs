// 验证运行日志的多代轮转(2026-07-31 方案 A)。守三条:
// 代际链不许错位、超阈值要在运行期就触发(而不是等下次启动)、轮转那一瞬间的
// 日志行不许丢——故障往往就发生在日志暴涨的时刻,丢的正是要看的那几行。
import { createRequire } from 'node:module'
import { mkdtempSync, readFileSync, existsSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
const require = createRequire(import.meta.url)
const { RotatingLog, rotateFiles, logFileNames } = require('../logRotate.js')

let fail = 0
const check = (c, m) => { console.log(c ? '  PASS' : '  FAIL', m); if (!c) fail++ }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

// —— 代际链 ——
{
  const dir = mkdtempSync(join(tmpdir(), 'rot-chain-'))
  const logPath = join(dir, 'brain.log')
  writeFileSync(logPath, '当前')
  writeFileSync(`${logPath}.1`, '上一代')
  writeFileSync(`${logPath}.2`, '上上代')

  rotateFiles(logPath, 5)

  check(!existsSync(logPath), '轮转后当前文件已让位')
  check(readFileSync(`${logPath}.1`, 'utf8') === '当前', '当前 -> .1')
  check(readFileSync(`${logPath}.2`, 'utf8') === '上一代', '.1 -> .2')
  check(readFileSync(`${logPath}.3`, 'utf8') === '上上代', '.2 -> .3')
  rmSync(dir, { recursive: true, force: true })
}

// —— 最老的一代被丢弃，不会无限堆积 ——
{
  const dir = mkdtempSync(join(tmpdir(), 'rot-cap-'))
  const logPath = join(dir, 'brain.log')
  writeFileSync(logPath, '当前')
  for (let i = 1; i <= 3; i += 1) writeFileSync(`${logPath}.${i}`, `第${i}代`)

  rotateFiles(logPath, 3)

  check(!existsSync(`${logPath}.4`), 'keep=3 时不产生第 4 代')
  check(readFileSync(`${logPath}.3`, 'utf8') === '第2代', '超出保留数的最老一代被覆盖')
  rmSync(dir, { recursive: true, force: true })
}

// —— 运行期触发（不是等下次启动），且不丢行 ——
{
  const dir = mkdtempSync(join(tmpdir(), 'rot-live-'))
  const log = new RotatingLog({ dir, rotateBytes: 200, keep: 3 }).open()

  for (let i = 0; i < 60; i += 1) log.write(`第${i}行内容填充填充`)
  await log.settled()
  await sleep(30)
  log.close()
  await sleep(30)

  const logPath = join(dir, 'brain.log')
  check(existsSync(`${logPath}.1`), '运行期就发生了轮转，没等到下次启动')

  let joined = readFileSync(logPath, 'utf8')
  for (let i = 1; i <= 3; i += 1) {
    if (existsSync(`${logPath}.${i}`)) joined += readFileSync(`${logPath}.${i}`, 'utf8')
  }
  const missing = []
  for (let i = 0; i < 60; i += 1) {
    if (!joined.includes(`第${i}行内容填充填充`)) missing.push(i)
  }
  check(missing.length === 0, `轮转期间没有丢行（缺失 ${missing.length} 行）`)
  rmSync(dir, { recursive: true, force: true })
}

// —— 上报要取的文件名是固定枚举，不是扫目录 ——
{
  const names = logFileNames('brain.log', 5)
  check(names[0] === 'brain.log', '首项是当前日志')
  check(names.includes('brain.log.5'), '含末代')
  check(names.includes('brain.log.old'), '含旧格式 .old，已装机器的历史不丢')
  check(names.length === 7, `固定 7 项（实际 ${names.length}）`)
}

console.log(fail === 0 ? 'logRotate: 全部通过' : `logRotate: ${fail} 项失败`)
process.exit(fail === 0 ? 0 : 1)
