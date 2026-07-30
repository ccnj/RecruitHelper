import assert from 'node:assert/strict'
import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/product/components/ConfirmationPage.tsx'],
  bundle: true,
  format: 'esm',
  packages: 'external',
  platform: 'node',
  outfile: 'test/dist/product-confirmation.mjs',
  logLevel: 'error',
})
const moduleUrl = pathToFileURL(process.cwd() + '/test/dist/product-confirmation.mjs').href
const { ConfirmationPage } = await import(moduleUrl + `?run=${Date.now()}`)

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

function candidate(index, sendState) {
  return {
    profileId: `P-${index}`,
    displayName: `候选人${index}`,
    age: null,
    education: null,
    experience: null,
    city: null,
    currentRole: null,
    jobName: '测试职位',
    statusLabel: '待发送',
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
    wechatAccount: null,
    wechatExchangedAt: null,
    stillInAutoCommunication: null,
    messages: [],
    decisions: [],
    actions: [],
    aiScore: 90,
    greeting: '您好，想和您聊聊这个职位。',
    generationStateLabel: '招呼语已生成',
    sendState,
    sendStateLabel: '待发送',
    selectable: sendState === 'ready',
  }
}

function render(sendStates) {
  return renderToStaticMarkup(createElement(ConfirmationPage, {
    actions: { sendConfirmationBatch() {} },
    customer,
    batch: {
      ready: true,
      readinessReason: null,
      batchId: 'batch-one',
      createdAt: null,
      scoreCompleted: 30,
      selectedCount: sendStates.length,
      greetingSucceeded: sendStates.length,
      greetingFailed: 0,
      greetingPending: 0,
      workflowPaused: false,
      businessWindowOpen: true,
      candidates: sendStates.map((state, index) => candidate(index, state)),
    },
    onOpenCandidate() {},
    onSelectionChange() {},
    selectedIds: new Set(),
  }))
}

// 发送刚开始:5 人确认，1 人已发出。
const midway = render(['sent', 'ready', 'ready', 'ready', 'ready'])
assert.match(midway, /已确认 5 人 · 已发送 1 人/u, '发送途中分母是本次确认名单人数')

// 推荐流失效:两个还没铸造发送意图的人掉成 abandoned/unavailable。他们当初就在
// 确认名单里，分母必须一直算上他们，否则"已确认人数"会在发送途中往下走。
const afterFeedChange = render(['sent', 'settledWithoutSend', 'settledWithoutSend', 'ready', 'ready'])
assert.match(afterFeedChange, /已确认 5 人 · 已发送 1 人/u,
  '推荐流失效不得让已确认人数往下走')
assert.doesNotMatch(afterFeedChange, /已确认 3 人/u, '未发出的人仍属于本次确认名单')

// 招呼语根本没生成的不算进确认名单——他们从来没被确认过。
const withPendingGeneration = render(['sent', 'ready', 'ineligible'])
assert.match(withPendingGeneration, /已确认 2 人 · 已发送 1 人/u,
  '招呼语未就绪的不计入确认名单')

// 全部终局(含未发出)即完成，不能因为有人未发出就永远停在"正在发送"。
const settled = render(['sent', 'sent', 'settledWithoutSend'])
assert.match(settled, /本批发送完成/u, '未发出的人不阻塞整批完成判定')
assert.match(settled, /未发出 1 人/u, '完成提示如实说明有人未发出')

console.log('ALL PASS')
