import assert from 'node:assert/strict'
import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/product/components/CandidateStagePage.tsx'],
  bundle: true,
  format: 'esm',
  packages: 'external',
  platform: 'node',
  outfile: 'test/dist/product-candidate-stage.mjs',
  logLevel: 'error',
})
const moduleUrl = pathToFileURL(process.cwd() + '/test/dist/product-candidate-stage.mjs').href
const { CandidateStagePage, stageCountLabel } = await import(moduleUrl + `?run=${Date.now()}`)

function candidate(index) {
  return {
    profileId: `P-${index}`,
    displayName: `候选人${index}`,
    age: null,
    education: null,
    experience: null,
    city: null,
    currentRole: null,
    jobName: '测试职位',
    statusLabel: '已招呼',
    statusTone: 'blue',
    lastMessage: null,
    lastActiveAt: null,
    unreadCount: 0,
    manualRequired: false,
    resumeSummary: null,
    deterministicState: null,
    latestAiDecision: null,
    manualReason: null,
    interviewAt: null,
    interviewMethod: null,
    interviewResult: null,
    wechatAccount: null,
    wechatExchangedAt: null,
    stillInAutoCommunication: null,
    messages: [],
    decisions: [],
    actions: [],
  }
}

function render(itemCount, total) {
  return renderToStaticMarkup(createElement(CandidateStagePage, {
    view: 'communicating',
    candidates: Array.from({ length: itemCount }, (_, index) => candidate(index)),
    total,
    globalSearch: '',
    onOpenCandidate: () => {},
  }))
}

// 单页读取有上限。人数报的必须是脑侧总数,否则超过上限后计数永远停在
// 上限值,看起来像"正好这么多人",还会跟首页累计账面互相矛盾。
const truncated = render(200, 320)
assert.match(truncated, /320 位候选人/u, '截断时人数仍报脑侧真实总数')
assert.doesNotMatch(truncated, /200 位候选人/u, '不得把本页条数当成人数')
assert.match(
  truncated,
  /共 320 位候选人，本页只加载了最近活动的 200 位/u,
  '截断必须显式告知，不能静默少列人',
)

const complete = render(12, 12)
assert.match(complete, /12 位候选人/u, '未截断时人数即总数')
assert.doesNotMatch(complete, /本页只加载/u, '未截断时不出现截断提示')

// 筛选是在已加载的这些人里做的:截断时拿 filtered 跟 total 比会误导。
assert.equal(stageCountLabel(3, 200, 320, true, true), '已加载 200 位中筛出 3 位')
assert.equal(stageCountLabel(3, 12, 12, true, false), '3 / 12 位候选人')
assert.equal(stageCountLabel(12, 12, 320, false, true), '320 位候选人')

console.log('ALL PASS')
