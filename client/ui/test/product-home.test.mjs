import assert from 'node:assert/strict'
import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/product/components/HomePage.tsx'],
  bundle: true,
  format: 'esm',
  packages: 'external',
  platform: 'node',
  outfile: 'test/dist/product-home.mjs',
  logLevel: 'error',
})
const moduleUrl = pathToFileURL(process.cwd() + '/test/dist/product-home.mjs').href
const {
  END_WORKFLOW_CONFIRMATION,
  HomePage,
  confirmEndWorkflow,
} = await import(moduleUrl + `?run=${Date.now()}`)

const customer = {
  name: '测试客户',
  shortName: '测',
  authorizationLabel: '授权有效',
  authorized: true,
  activationRequired: false,
  job: {
    backendJobId: 'job-one',
    name: '测试职位',
    syncState: 'synced',
    syncStateLabel: '配置已同步',
    environment: '智联招聘',
    lastSyncedAt: null,
  },
}
const overview = {
  dateLabel: '2026 年 7 月 26 日',
  refreshedAt: null,
  businessWindowLabel: '运行时间 08:00～24:00',
  businessWindowOpen: true,
  workflow: {
    mode: 'replyOnly',
    state: 'running',
    stateLabel: '运行中',
    positionLabel: '正在处理候选人消息',
    canStart: false,
    canAddBatch: true,
    canEnd: false,
    canPause: true,
    canResume: false,
    pendingAction: null,
    unavailableReason: null,
  },
  funnel: {
    stage: 'completed',
    stateLabel: '本批已完成',
    target: 30,
    pending: 0,
    failed: 0,
    latestFailure: null,
    stages: [],
  },
  communication: { state: 'running', stateLabel: '运行中', lastPatrolAt: null },
  todayMetrics: [],
  ledgerStartedAt: null,
  ledger: [],
  todayInterviews: [],
  todayActivity: {
    greeted: 0,
    greetingDisplayTarget: 100,
    newReplies: 0,
    newInterviews: 0,
    completedInterviews: 0,
  },
}
const actions = {
  startWorkflow() {},
  pauseWorkflow() {},
  resumeWorkflow() {},
  endWorkflow() {},
}

function render(workflowOverrides = {}) {
  return renderToStaticMarkup(createElement(HomePage, {
    actions,
    customer,
    onOpenConfirmation() {},
    overview: {
      ...overview,
      workflow: { ...overview.workflow, ...workflowOverrides },
    },
  }))
}

function buttons(markup) {
  return [...markup.matchAll(/<button\b[^>]*>[\s\S]*?<\/button>/gu)].map((match) => ({
    html: match[0],
    text: match[0].replace(/<[^>]+>/gu, '').replace(/\s+/gu, ' ').trim(),
  }))
}

const withoutAuthorization = render()
assert.equal(withoutAuthorization.includes('结束本次任务'), false,
  '后端未授权 canEnd 时不得显示结束入口')

const authorized = render({ canEnd: true })
assert.equal(buttons(authorized).find((button) => button.text === '结束本次任务')?.html.includes('disabled'), false,
  '后端授权后显示可点击的普通用户结束入口')

const sourcingPending = render({ canEnd: true, canAddBatch: true, pendingAction: 'sourcing' })
assert.match(sourcingPending, /当前候选人处理完后开始新一批/u)
assert.ok(buttons(sourcingPending).find((button) => button.text === '新一批已安排')?.html.includes('disabled'),
  '待切换采集时重复追加保持可见但禁用')
assert.ok(buttons(sourcingPending).find((button) => button.text === '结束本次任务')?.html.includes('disabled'),
  '待切换采集时结束入口保持可见但禁用')

const endPending = render({ canEnd: true, canAddBatch: true, pendingAction: 'end' })
assert.match(endPending, /正在结束当前候选人…/u)
const endPendingButtons = buttons(endPending).filter((button) =>
  button.text === '暂停' || button.text === '再采一批（30 人）' || button.text === '正在结束…'
)
assert.deepEqual(
  endPendingButtons.map((button) => button.text),
  ['暂停', '再采一批（30 人）', '正在结束…'],
  '待结束时三个运行控制都应保持可见',
)
assert.ok(
  endPendingButtons.every((button) => button.html.includes('disabled')),
  '待结束时首页其余运行控制全部禁用',
)

let endRequests = 0
let confirmationMessage = ''
confirmEndWorkflow(
  (message) => {
    confirmationMessage = message
    return false
  },
  () => {
    endRequests += 1
  },
)
assert.equal(confirmationMessage, END_WORKFLOW_CONFIRMATION,
  '结束操作先展示固定中文确认说明')
assert.equal(endRequests, 0,
  '用户取消确认时不得调用结束工作流写入口')

await confirmEndWorkflow(
  () => true,
  async () => {
    endRequests += 1
  },
)
assert.equal(endRequests, 1,
  '用户确认后只调用一次结束工作流写入口')

console.log('产品首页结束与待切换控制测试通过')
