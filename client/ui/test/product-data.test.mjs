import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/product/data.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/product-data.mjs',
  logLevel: 'error',
})
const moduleUrl = pathToFileURL(process.cwd() + '/test/dist/product-data.mjs').href
const {
  adaptCandidateDetail,
  adaptProductSnapshot,
  isBusinessWindowOpen,
  productCandidatePath,
} = await import(moduleUrl)

let fail = 0
const check = (condition, message) => {
  console.log(condition ? '  PASS' : '  FAIL', message)
  if (!condition) fail++
}

const exact = (value) => ({ value, exact: true })
const unknown = (reason) => ({ value: null, exact: false, unavailableReason: reason })
const now = new Date(2026, 6, 25, 9, 30, 0)
const todayAt = (hour, minute) => new Date(2026, 6, 25, hour, minute, 0).getTime()
const rawCandidate = (overrides = {}) => ({
  profileId: 'profile-one',
  displayName: '候选人甲',
  jobName: '产品经理',
  status: 'communicating',
  lastMessagePreview: '我想了解一下团队情况',
  lastActivityAtMs: todayAt(9, 20),
  unreadCount: 2,
  manualRequired: false,
  ...overrides,
})
const candidateResponse = (view, items) => ({
  candidates: { view, total: items.length, items, limit: 200, offset: 0 },
})
const snapshot = {
  overview: {
    overview: {
      job: {
        available: true,
        backendJobId: '42',
        name: '产品经理',
        environment: 'online',
        syncStatus: 'synced',
        lastSyncedAt: new Date(2026, 6, 25, 8, 5).toISOString(),
      },
      funnel: {
        available: true,
        batchId: 'batch-one',
        stage: 'awaitingConfirmation',
        targetCount: 30,
        capturedCount: 30,
        scoredCount: 30,
        selectedCount: 12,
        greetingReady: 12,
        pendingConfirm: 12,
        sentCount: 0,
        failedCount: 0,
        suspectCount: 0,
      },
      statistics: {
        todayRated: exact(30),
        todayConfirmation: exact(12),
        todayGreeted: exact(4),
        todayInvited: unknown('当前口径不可用'),
        totalGreeted: exact(83),
        totalInterviewed: exact(7),
        totalWechat: exact(11),
        todayNewReplies: exact(3),
        todayNewAppointments: exact(1),
        todayCompletedInterviews: exact(0),
      },
      todayInterviews: [{
        profileId: 'profile-interview',
        displayName: '候选人乙',
        jobName: '产品经理',
        startsAtMs: todayAt(14, 0),
        method: '微信视频',
        state: 'confirmed',
      }],
      businessSince: new Date(2026, 6, 20, 8, 0).toISOString(),
      refreshedAt: new Date(2026, 6, 25, 9, 29).toISOString(),
    },
    runtime: {
      available: true,
      customerName: '微领',
      customerStatus: 'active',
      authorized: true,
      providerConfigured: true,
      provider: 'deepseek',
      model: 'deepseek-v4-pro',
      pluginOnline: true,
      pluginHealth: 'ready',
      pluginVersion: '0.1.0',
      contractMatch: true,
      workflowMode: 'full',
      workflowStatus: 'awaitingConfirmation',
      communicationState: 'running',
    },
  },
  confirmation: {
    confirmation: {
      available: true,
      ready: true,
      batchId: 'batch-one',
      jobName: '产品经理',
      createdAt: new Date(2026, 6, 25, 8, 10).toISOString(),
      scoredCount: 30,
      selectedCount: 12,
      generatedCount: 12,
      generationFailed: 0,
      generationPending: 0,
      selectableCount: 1,
      candidates: [{
        profileId: 'profile-ready',
        displayName: '候选人丙',
        jobName: '产品经理',
        score: 91,
        greetingText: '您好，想和您聊聊这个职位。',
        status: 'ready',
        selectable: true,
      }],
    },
  },
  candidates: {
    communicating: candidateResponse('communicating', [rawCandidate()]),
    pendingInterview: candidateResponse('pending', [rawCandidate({
      profileId: 'profile-pending',
      status: 'invited',
      interviewStartsAtMs: todayAt(15, 0),
      interviewMethod: '微信视频',
      interviewCardState: 'confirmed',
    })]),
    interviewed: candidateResponse('interviewed', [rawCandidate({
      profileId: 'profile-interviewed',
      status: 'interviewed',
      interviewStartsAtMs: todayAt(10, 0),
      interviewCardState: 'rejected',
    })]),
    wechat: candidateResponse('wechat', [rawCandidate({
      profileId: 'profile-wechat',
      wechat: 'candidate_wechat',
    })]),
  },
}

const product = adaptProductSnapshot(snapshot, now)
check(product.customer.name === '微领' && product.customer.authorized, '客户授权与绑定职位来自产品投影')
check(!product.customer.activationRequired, '已授权产品投影不会要求再次激活')
const unactivatedSnapshot = structuredClone(snapshot)
unactivatedSnapshot.overview.runtime.authorized = false
check(
  adaptProductSnapshot(unactivatedSnapshot, now).customer.activationRequired,
  '运行态明确未授权时要求首次激活',
)
const unreadableAuthorizationSnapshot = structuredClone(unactivatedSnapshot)
unreadableAuthorizationSnapshot.overview.runtime.available = false
check(
  !adaptProductSnapshot(unreadableAuthorizationSnapshot, now).customer.activationRequired,
  '运行态不可读取时不把未知授权状态当成首次激活',
)
check(
  product.customer.job.name === '产品经理' &&
    product.customer.job.backendJobId === '42' &&
    product.customer.job.syncState === 'synced',
  '职位同步状态和仅供启动绑定的 Job.ID 如实映射',
)
check(product.overview.workflow.state === 'awaitingConfirmation' && !product.overview.workflow.canStart, '工作流等待确认时不会误显示可开始')
check(product.overview.todayMetrics[0].value === 30, '精确统计值进入首页')
check(product.overview.todayMetrics[3].value === null, '非精确统计保持不可用，不用列表长度猜值')
check(product.overview.funnel.stages.find((stage) => stage.key === 'confirm').state === 'active', '漏斗正确定位等待确认阶段')
check(product.confirmation.candidates[0].sendState === 'ready' && product.confirmationBadge === 1, '候选确认只把可发送成员计入徽章')
check(product.candidates.pendingInterview[0].statusLabel === '已确认', '邀面卡确认状态进入待面试列表')
check(product.candidates.interviewed[0].interviewResult === null, '不从邀面卡状态猜测正式面试结果')
check(product.candidates.wechat[0].wechatAccount === 'candidate_wechat', '已收编微信资产只留在产品内存模型')
check(product.overview.todayInterviews[0].interviewAt.includes('14:00'), '今日面试时间按本地时区展示')
check(product.connections.find((item) => item.label === 'AI 模型')?.value === 'deepseek-v4-pro', '普通配置页展示安全模型配置摘要')
check(product.connections.find((item) => item.label === 'Chrome 插件')?.value === '已连接', '普通配置页展示安全插件连接摘要')

const replyOnlyWithoutJob = structuredClone(snapshot)
replyOnlyWithoutJob.overview.overview.job = {
  available: false,
  syncStatus: 'missing',
}
replyOnlyWithoutJob.overview.runtime.workflowMode = undefined
replyOnlyWithoutJob.overview.runtime.workflowStatus = undefined
check(
  adaptProductSnapshot(replyOnlyWithoutJob, now).overview.workflow.canStart,
  '无绑定职位不再误禁仅多轮回复',
)

check(!isBusinessWindowOpen(new Date(2026, 6, 25, 7, 59)), '08:00 前产品页保持只读')
check(isBusinessWindowOpen(new Date(2026, 6, 25, 8, 0)), '08:00 起进入业务窗口')
check(productCandidatePath('pendingInterview').includes('view=pending'), '产品页名称映射到唯一后端候选视图')

const detail = adaptCandidateDetail({
  candidate: {
    candidate: rawCandidate({ interviewMethod: '微信视频' }),
    resume: {
      available: true,
      basic: [
        { label: '年龄', value: '29岁' },
        { label: '最高学历', value: '硕士' },
        { label: '工作经验', value: '6年' },
        { label: '现居住地', value: '上海' },
      ],
      expectations: [],
      selfEvaluation: '擅长从 0 到 1 推进产品。',
      education: '',
      workExperiences: '负责过企业服务产品。',
      truncated: false,
    },
    messages: [
      { seq: 1, direction: 'out', kind: 'text', text: '您好', tsApproxMs: todayAt(9, 0) },
      {
        seq: 2,
        direction: 'out',
        kind: 'card',
        cardType: 'interviewInvite',
        cardState: 'confirmed',
        interviewStartsAtMs: todayAt(15, 0),
        interviewMethod: '微信视频',
        tsApproxMs: todayAt(9, 10),
      },
    ],
    latestAi: {
      available: true,
      status: 'completed',
      intentLabel: 'interested',
      intentSource: 'llm',
      classifiedAt: new Date(2026, 6, 25, 9, 21).toISOString(),
    },
    actions: [{
      kind: 'replyText',
      status: 'sent',
      createdAt: new Date(2026, 6, 25, 9, 22).toISOString(),
    }],
  },
}, product.candidates.communicating[0], now)
check(detail.age === 29 && detail.education === '硕士' && detail.city === '上海', '详情按需读取后补齐简历画像')
check(detail.messages.length === 2 && detail.messages[1].kindLabel === '邀面卡', '详情保留文字与卡片事实')
check(detail.decisions[0].summary.includes('有意向'), '最近 AI 判断转换为只读说明')
check(detail.actions[0].resultLabel === '服务端已确认', '动作结果使用业务状态文案')

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
process.exit(fail === 0 ? 0 : 1)
