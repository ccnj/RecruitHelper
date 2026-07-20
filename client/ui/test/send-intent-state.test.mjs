import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/send-intent-state.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/send-intent-state.mjs',
  logLevel: 'error',
})
const moduleUrl = pathToFileURL(process.cwd() + '/test/dist/send-intent-state.mjs').href
const { sendStateLabel, sendSucceeded, sendSuspect, sendTerminal } = await import(moduleUrl)

let fail = 0
const check = (condition, message) => {
  console.log(condition ? '  PASS' : '  FAIL', message)
  if (!condition) fail++
}

const queued = { intentId: 'intent-queued', logicalDispatchId: 'logical', msgId: 'msg', status: 'dispatching', commandStatus: 'sent' }
const ok = { ...queued, status: 'ok', commandStatus: 'ok' }
const failed = { ...queued, status: 'failed', commandStatus: 'failed' }
const suspect = { ...queued, status: 'suspect', commandStatus: 'suspect', verificationAttempts: 3 }

check(!sendTerminal(queued) && !sendSucceeded(queued) && !sendSuspect(queued), '在途意图保持轮询且不冒充终局')
check(sendTerminal(ok) && sendSucceeded(ok) && !sendSuspect(ok), 'ok 是已确认成功终局')
check(sendTerminal(failed) && !sendSucceeded(failed) && !sendSuspect(failed), 'failed 是可由人确认的非成功终局')
check(sendTerminal(suspect) && !sendSucceeded(suspect) && sendSuspect(suspect), 'suspect 单独识别，不能按普通失败解锁')
check(sendStateLabel(suspect).includes('转人工') && sendStateLabel(suspect).includes('已验证 3 轮'), 'suspect 状态文案保留验证轮次与人工出口')

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
process.exit(fail === 0 ? 0 : 1)
