import assert from 'node:assert/strict'
import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/product/components/InterviewSchedulePanel.tsx'],
  bundle: true,
  format: 'esm',
  packages: 'external',
  platform: 'node',
  outfile: 'test/dist/product-interview-schedule.mjs',
  logLevel: 'error',
})
const moduleUrl = pathToFileURL(process.cwd() + '/test/dist/product-interview-schedule.mjs').href
const { expandToCells, mergeToWindows, countHours, defaultSchedule } = await import(
  moduleUrl + `?run=${Date.now()}`
)

const WEEKDAYS = ['周一', '周二', '周三', '周四', '周五', '周六', '周日']

// 展开是右开区间、按半小时格(2026-09-04 甲方裁决,此前每小时一格):
// 09:00-11:00 是 09:00、09:30、10:00、10:30 四格,不含 11:00。
{
  const cells = expandToCells([{ start: '09:00', end: '11:00' }])
  assert.deepEqual([...cells].sort(), ['09:00', '09:30', '10:00', '10:30'])
}

// 半点起止的窗口原样展开——这正是本次放宽要能表达的形态。
{
  const cells = expandToCells([{ start: '09:30', end: '10:30' }])
  assert.deepEqual([...cells].sort(), ['09:30', '10:00'])
}

{
  assert.equal(expandToCells(undefined).size, 0)
  assert.equal(expandToCells([]).size, 0)
}

// 起止格式坏掉时不能死循环:字符串比较对 "9:00" 这种永远为真,靠 24:00 硬上界收敛。
{
  const cells = expandToCells([{ start: '23:00', end: '9:00' }])
  assert.deepEqual([...cells].sort(), ['23:00', '23:30'])
}

// 合并回窗口:相邻半小时格并成一段,断开处切段。
{
  const windows = mergeToWindows(['09:00', '09:30', '10:00', '14:00', '14:30'])
  assert.deepEqual(windows, [
    { start: '09:00', end: '10:30' },
    { start: '14:00', end: '15:00' },
  ])
}

// 只差一个整点、中间缺半点的两格不是相邻:必须切成两段,否则 09:30 会被悄悄选上。
{
  const windows = mergeToWindows(['09:00', '10:00'])
  assert.deepEqual(windows, [
    { start: '09:00', end: '09:30' },
    { start: '10:00', end: '10:30' },
  ])
}

// 单格也要合成一段合法窗口,不能塌成起止相等——脑侧会把 start>=end 判非法。
{
  const windows = mergeToWindows(['20:30'])
  assert.deepEqual(windows, [{ start: '20:30', end: '21:00' }])
}

{
  assert.deepEqual(mergeToWindows([]), [])
}

// 乱序与重复输入都要归一:拖拽矩形跨行时产生的集合顺序不确定。
{
  const windows = mergeToWindows(['10:00', '09:00', '09:30', '09:00'])
  assert.deepEqual(windows, [{ start: '09:00', end: '10:30' }])
}

// 展开与合并必须互为逆运算,否则一次拖拽就会让周表悄悄漂移。半点起止一并覆盖。
{
  const original = [
    { start: '08:00', end: '09:30' },
    { start: '13:30', end: '18:00' },
  ]
  const roundTripped = mergeToWindows([...expandToCells(original)])
  assert.deepEqual(roundTripped, original)
}

// 内置默认必须与脑侧一致:七天全 09:00-18:00、共 63 小时(2026-08-01 裁决,
// 此前周末为空)。两端各存一份默认值与步长,这条断言是它们唯一的对齐点:
// 步长若一端改了另一端没改,63 就对不上。
{
  const schedule = defaultSchedule(WEEKDAYS)
  for (const day of WEEKDAYS) {
    assert.deepEqual(schedule[day], [{ start: '09:00', end: '18:00' }])
  }
  assert.equal(countHours(schedule, WEEKDAYS), 63)
}

// 空表计数必须是 0 —— 组件靠它拦下"拖没了"的那次提交;半小时格计 0.5。
{
  assert.equal(countHours({}, WEEKDAYS), 0)
  assert.equal(countHours({ 周一: [] }, WEEKDAYS), 0)
  assert.equal(countHours({ 周三: [{ start: '14:00', end: '15:00' }] }, WEEKDAYS), 1)
  assert.equal(countHours({ 周三: [{ start: '14:00', end: '14:30' }] }, WEEKDAYS), 0.5)
}

console.log('product-interview-schedule: ok')
