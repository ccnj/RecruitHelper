import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

// 状态栏开发版"最近命令"的折行与状态归类。
mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/overlay/ledger-rows.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/overlay-ledger-rows.mjs',
  logLevel: 'error',
})
const { foldLedgerRows, ledgerStatus, elapsedLabel } = await import(
  pathToFileURL(process.cwd() + '/test/dist/overlay-ledger-rows.mjs').href
)

let fail = 0
const check = (condition, message) => {
  console.log(condition ? '  PASS' : '  FAIL', message)
  if (!condition) fail++
}

const now = Date.parse('2026-09-08T14:32:10+08:00')
const row = (over) => ({
  msgId: over.msgId, name: over.name, class: over.class ?? 'readonly', status: over.status ?? 'ok',
  errorCode: over.errorCode, target: over.target ?? '', createdAtMs: over.createdAtMs ?? now - 5000,
  terminalAtMs: over.terminalAtMs ?? now - 2000,
})

// —— 状态归类 ——
check(ledgerStatus({ status: 'verifying' }).label === '进行中', '验证中归进行中')
check(ledgerStatus({ status: 'resolvedOk' }).tone === 'ok', '人工判成功归成功')
check(ledgerStatus({ status: 'failed', errorCode: 'ELEMENT_UNRESOLVED' }).label === '失败 ELEMENT_UNRESOLVED', '失败带错误码')
check(ledgerStatus({ status: 'expired' }).label === '超时', '超时单独叫法')
check(ledgerStatus({ status: 'suspect' }).tone === 'suspect', 'suspect 独立语气')
check(ledgerStatus({ status: 'void' }).label === '作废', 'void 作废')

// —— 耗时 ——
check(elapsedLabel(now - 3200, now - 0, now) === '3.2s', '十秒内带一位小数')
check(elapsedLabel(now - 21000, 0, now) === '21s', '未终局按现在算')
check(elapsedLabel(now - 150000, 0, now) === '2m', '超过百秒按分钟')

// —— 折行:连续同名只读折成一行,有副作用的永远独立 ——
const folded = foldLedgerRows([
  row({ msgId: 'a', name: 'chat.readThread', status: 'verifying', terminalAtMs: 0, target: '张三' }),
  row({ msgId: 'b', name: 'chat.sendMessage', class: 'effectful', target: '张三' }),
  row({ msgId: 'c', name: 'chat.readList' }),
  row({ msgId: 'd', name: 'chat.readList' }),
  row({ msgId: 'e', name: 'chat.readList' }),
  row({ msgId: 'f', name: 'chat.readList', status: 'failed', errorCode: 'X' }),
  row({ msgId: 'g', name: 'chat.sendMessage', class: 'effectful', status: 'failed', errorCode: 'ELEMENT_UNRESOLVED', target: '李四' }),
  row({ msgId: 'h', name: 'chat.readList' }),
], now, 5)
check(folded.length === 5, `最多五行(得到 ${folded.length})`)
check(folded[0].name === 'chat.readThread' && folded[0].statusLabel === '进行中' && folded[0].elapsedLabel === '5.0s', '进行中的行按现在算耗时')
check(folded[1].name === 'chat.sendMessage' && folded[1].effectful && folded[1].count === 1, '有副作用的单独一行')
check(folded[2].name === 'chat.readList' && folded[2].count === 3 && !folded[2].effectful, '连续三条只读同名同状态折成一行')
check(folded[3].statusLabel === '失败 X' && folded[3].count === 1, '状态不同不折')
check(folded[4].name === 'chat.sendMessage' && folded[4].target === '李四', '第五行是另一条发送')
check(folded.every((r) => r.key), '每行有 key')

// —— 折行发生在上限之外也要计数:第 6 条若与第 5 条可折,应折进去而不是被截掉 ——
const tail = foldLedgerRows([
  row({ msgId: '1', name: 'x.a', class: 'effectful' }),
  row({ msgId: '2', name: 'x.b', class: 'effectful' }),
  row({ msgId: '3', name: 'x.c', class: 'effectful' }),
  row({ msgId: '4', name: 'x.d', class: 'effectful' }),
  row({ msgId: '5', name: 'chat.readList' }),
  row({ msgId: '6', name: 'chat.readList' }),
  row({ msgId: '7', name: 'x.e', class: 'effectful' }),
], now, 5)
check(tail.length === 5 && tail[4].count === 2, '第五行之后的可折行仍计入次数,第七条被截掉')

// —— 两条同名只读但中间夹了别的,不折 ——
const split = foldLedgerRows([
  row({ msgId: 'p', name: 'chat.readList' }),
  row({ msgId: 'q', name: 'chat.openConversation', class: 'intrusive' }),
  row({ msgId: 'r', name: 'chat.readList' }),
], now, 5)
check(split.length === 3, '不连续不折')

// —— 空目标画破折号 ——
check(folded[2].target === '—', '无目标显示破折号')

if (fail) {
  console.error(`\n${fail} 项失败`)
  process.exit(1)
}
console.log('\n状态栏账本折行测试通过')
