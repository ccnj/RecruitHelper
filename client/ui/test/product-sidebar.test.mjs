import assert from 'node:assert/strict'
import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/product/components/ProductSidebar.tsx'],
  bundle: true,
  format: 'esm',
  packages: 'external',
  platform: 'node',
  outfile: 'test/dist/product-sidebar.mjs',
  logLevel: 'error',
})
const moduleUrl = pathToFileURL(process.cwd() + '/test/dist/product-sidebar.mjs').href
const { ProductSidebar, syncJobsTitle } = await import(moduleUrl + `?run=${Date.now()}`)

const job = {
  backendJobId: 'job-one',
  name: '测试职位',
  syncState: 'synced',
  syncStateLabel: '配置已同步',
  lastSyncedAt: '今天 09:09',
}
const totals = { communicating: 23, interviewed: 0, interviewElapsed: 0, wechat: 0 }

function render(overrides = {}) {
  return renderToStaticMarkup(createElement(ProductSidebar, {
    activePage: 'home',
    customerName: '测试客户',
    customerShortName: '测',
    job,
    confirmationBadge: 0,
    candidateTotals: totals,
    searchValue: '',
    version: '3.28.0',
    onNavigate() {},
    onSearch() {},
    onSyncJobs() {},
    ...overrides,
  }))
}

// 2026-09-08 甲方裁决:同步职位收成左上角客户块里的刷新图标。动作可用时可点,
// title 带说明、同步状态与最近同步时间——首页职位卡原先的信息不丢。
{
  const markup = render()
  assert.ok(markup.includes('aria-label="同步职位"'), '客户块渲染同步职位图标按钮')
  assert.ok(markup.includes('class="rh-sidebar-sync"'), '未同步中时不带 is-syncing')
  assert.ok(!/aria-label="同步职位"[^>]*disabled/.test(markup), '动作可用时按钮可点')
  assert.ok(markup.includes('配置已同步 · 同步于 今天 09:09'), 'title 带状态与最近同步时间')
  assert.ok(markup.includes('测试职位'), '客户块仍显示当前职位名')
  // 搜索框保留(甲方 09-08 决定不删)。
  assert.ok(markup.includes('placeholder="搜索候选人"'), '搜索候选人入口保留')
}

// 同步进行中:图标转圈、禁点,避免连点重复提交。
{
  const markup = render({ job: { ...job, syncState: 'syncing', syncStateLabel: '同步中' } })
  assert.ok(markup.includes('class="rh-sidebar-sync is-syncing"'), '同步中带 is-syncing')
  assert.ok(/aria-label="同步职位"[^>]*disabled/.test(markup), '同步中禁点')
}

// 运行控制未接入(预览/未连脑):禁点并说明原因。
{
  const markup = render({ onSyncJobs: undefined })
  assert.ok(/aria-label="同步职位"[^>]*disabled/.test(markup), '无动作时禁点')
  assert.ok(markup.includes('运行控制尚未接入'), '无动作时 title 说明原因')
}

// title 文案:尚无同步记录也要说出来。
assert.equal(
  syncJobsTitle({ ...job, lastSyncedAt: null }, true).split('\n')[1],
  '配置已同步 · 尚无同步记录',
)

console.log('product-sidebar 测试全部通过')
