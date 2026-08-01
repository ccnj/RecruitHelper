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
  homeStatus: {
    label: '沟通中',
    hint: '正在自动回复候选人消息。',
    tone: 'running',
  },
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
    newWechat: 0,
    newInterviews: 0,
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

// 运行中的控制 2026-07-31 收成一条:唯一的刹车是「结束」,暂停与「再采一批」已从
// 客户界面撤下(脑侧能力都还在)。原先针对那两个按钮的断言随之删除——它们测的
// 是不再存在的 UI,留着只会在下次重构时再红一次。
const withoutAuthorization = render()
assert.equal(buttons(withoutAuthorization).some((button) => button.text === '结束'), false,
  '后端未授权 canEnd 时不得显示结束入口')

const authorized = render({ canEnd: true })
assert.equal(buttons(authorized).find((button) => button.text === '结束')?.html.includes('disabled'), false,
  '后端授权后显示可点击的普通用户结束入口')

const sourcingPending = render({ canEnd: true, pendingAction: 'sourcing' })
assert.match(sourcingPending, /当前候选人处理完后会开始新一批/u,
  '待切换采集时说明为什么结束入口点不动')
assert.ok(buttons(sourcingPending).find((button) => button.text === '结束')?.html.includes('disabled'),
  '待切换采集时结束入口保持可见但禁用')

const endPending = render({ canEnd: true, pendingAction: 'end' })
assert.match(endPending, /正在结束当前候选人，请稍候/u,
  '待结束时说明为什么结束入口点不动')
const endPendingButton = buttons(endPending).find((button) => button.text === '正在结束…')
assert.ok(endPendingButton, '待结束时结束入口改说正在结束')
assert.ok(endPendingButton.html.includes('disabled'),
  '待结束时结束入口保持可见但禁用')

// 首页那句话由 data.ts 的 homeStatus 算好后原样渲染,HomePage 不自己拼文案。
// "等待确认人数取 funnel.pending 而不是本批选中总数"归 product-data 测——那才是
// 算它的层;放在这里只能断言一个自己编的字符串,证明不了任何事。
const statusCard = render()
assert.ok(
  statusCard.includes(overview.homeStatus.label) && statusCard.includes(overview.homeStatus.hint),
  '状态卡原样显示 homeStatus 的说法与说明',
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
