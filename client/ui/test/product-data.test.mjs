import * as esbuild from 'esbuild'
import { mkdirSync, readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

// __APP_VERSION__ 由 vite.config.ts 在构建期注入；测试自己走 esbuild，
// 必须同样注入，否则打包产物一引用它就 ReferenceError。
const { version } = JSON.parse(
  readFileSync(new URL('../package.json', import.meta.url), 'utf8'),
)

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/product/data.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/product-data.mjs',
  define: { __APP_VERSION__: JSON.stringify(version) },
  logLevel: 'error',
})
const moduleUrl = pathToFileURL(process.cwd() + '/test/dist/product-data.mjs').href
const {
  adaptCandidateDetail,
  adaptProductSnapshot,
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
      businessWindowOpen: true,
      workflowMode: 'full',
      workflowStatus: 'awaitingConfirmation',
      canAddBatch: false,
      canEnd: false,
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
    // 已邀面(invited)并入沟通中:它是推进态、跟催时钟仍在跑,不能因为没有
    // 独立页面就从产品端消失。
    communicating: candidateResponse('communicating', [rawCandidate(), rawCandidate({
      profileId: 'profile-invited',
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
    interviewElapsed: candidateResponse('interviewElapsed', [rawCandidate({
      profileId: 'profile-elapsed',
      status: 'interviewed',
      interviewStartsAtMs: todayAt(8, 0),
      interviewEndsAtMs: todayAt(9, 0),
      interviewCardState: 'accepted',
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
const addBatchSnapshot = structuredClone(snapshot)
addBatchSnapshot.overview.runtime.workflowStatus = 'running'
addBatchSnapshot.overview.runtime.canAddBatch = true
check(
  adaptProductSnapshot(addBatchSnapshot, now).overview.workflow.canAddBatch,
  '追加采集入口只采用脑返回的明确授权',
)
addBatchSnapshot.overview.runtime.workflowStatus = 'paused'
check(
  adaptProductSnapshot(addBatchSnapshot, now).overview.workflow.canAddBatch,
  '暂停状态保留脑明确授权的追加采集入口',
)
const communicationSnapshot = structuredClone(snapshot)
communicationSnapshot.overview.runtime.workflowStatus = 'running'
communicationSnapshot.overview.runtime.canEnd = true
check(
  adaptProductSnapshot(communicationSnapshot, now).overview.workflow.canEnd,
  '结束入口只采用脑返回的明确授权',
)
communicationSnapshot.overview.runtime.workflowPendingAction = 'sourcing'
check(
  adaptProductSnapshot(communicationSnapshot, now).overview.workflow.pendingAction === 'sourcing' &&
    !adaptProductSnapshot(communicationSnapshot, now).overview.workflow.canAddBatch &&
    !adaptProductSnapshot(communicationSnapshot, now).overview.workflow.canEnd,
  '待切换采集时禁止重复追加和结束',
)
communicationSnapshot.overview.runtime.workflowPendingAction = 'end'
check(
  adaptProductSnapshot(communicationSnapshot, now).overview.workflow.pendingAction === 'end' &&
    !adaptProductSnapshot(communicationSnapshot, now).overview.workflow.canPause &&
    !adaptProductSnapshot(communicationSnapshot, now).overview.workflow.canResume,
  '待结束时禁用其余运行控制',
)
check(product.overview.todayMetrics[0].value === 30, '精确统计值进入首页')
check(product.overview.todayMetrics[3].value === null, '非精确统计保持不可用，不用列表长度猜值')
check(product.overview.funnel.stages.find((stage) => stage.key === 'confirm').state === 'active', '漏斗正确定位等待确认阶段')
check(product.confirmation.candidates[0].sendState === 'ready' && product.confirmationBadge === 1, '候选确认只把可发送成员计入徽章')
const generationInProgress = structuredClone(snapshot)
generationInProgress.confirmation.confirmation.ready = false
generationInProgress.confirmation.confirmation.reason = 'greetingGenerationPending'
check(
  !adaptProductSnapshot(generationInProgress, now).confirmation.ready &&
    adaptProductSnapshot(generationInProgress, now).confirmationBadge === 0,
  '整批未就绪时不提前开放候选确认或侧边栏徽章',
)
check(
  product.candidates.communicating.find((item) => item.profileId === 'profile-invited')
    ?.statusLabel === '邀面已确认',
  '已邀面候选人并入沟通中且标签可分辨',
)
// "已面试"只表示约定时间已过，不代表候选人到场；系统没有面试结果事实。
check(
  product.candidates.interviewElapsed[0].statusLabel === '已面试' &&
    product.candidates.interviewElapsed[0].deterministicState === '约定的面试时间已过',
  '已面试分类按时间已过表述，不冒充面试结果',
)
check(
  product.candidates.interviewed[0].statusLabel === '已约面',
  '面试时间未过的仍留在已约面',
)
// 命名必须与脑侧 notify/render.go 的 mainStatusLabels 一致:invited=已邀面
// (发出邀面卡)、interviewed=已约面(候选人点了接受)。系统没有面试完成事实,
// 任何"已面试/面试完成"措辞都会让人把已约面读成已经面完。
check(
  product.candidates.interviewed[0].statusLabel === '已约面' &&
    product.candidates.interviewed[0].deterministicState === '候选人已接受面试邀约',
  'interviewed 视图按已约面表述，不冒充面试完成',
)
check(
  product.overview.ledger.find((item) => item.label === '累计已约面') !== undefined &&
    product.overview.ledger.every((item) => !item.label.includes('已面试')),
  '累计账面按已约面表述',
)
check(
  product.overview.todayMetrics.find((item) => item.label === '已邀面') !== undefined &&
    product.overview.todayMetrics.every((item) => item.label !== '已约面'),
  '今日邀面卡人数按已邀面表述，不与已约面混名',
)
check(product.candidates.wechat[0].wechatAccount === 'candidate_wechat', '已收编微信资产只留在产品内存模型')
// 单页读取有上限,items 只是前若干位;人数必须来自脑侧 total,否则超过
// 上限后计数会永远停在上限值,与首页累计账面互相矛盾。
const truncatedList = structuredClone(snapshot)
truncatedList.candidates.communicating.candidates.total = 320
check(
  adaptProductSnapshot(truncatedList, now).candidateTotals.communicating === 320,
  '候选人总数取脑侧 total，不用本页条数代替',
)
const brokenTotal = structuredClone(snapshot)
const loadedCommunicating = snapshot.candidates.communicating.candidates.items.length
brokenTotal.candidates.communicating.candidates.total = 0
check(
  adaptProductSnapshot(brokenTotal, now).candidateTotals.communicating === loadedCommunicating,
  '总数小于本页条数时退回本页条数，不报出比看得见的人还少的数',
)
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

const brainClosedWindow = structuredClone(replyOnlyWithoutJob)
brainClosedWindow.overview.runtime.businessWindowOpen = false
check(
  !adaptProductSnapshot(brainClosedWindow, new Date(2026, 6, 25, 12, 0)).overview.workflow.canStart,
  'UI 不用渲染进程的中午时钟覆盖脑返回的闭窗结论',
)
const brainOpenedWindow = structuredClone(replyOnlyWithoutJob)
brainOpenedWindow.overview.runtime.businessWindowOpen = true
check(
  adaptProductSnapshot(brainOpenedWindow, new Date(2026, 6, 25, 1, 0)).overview.workflow.canStart,
  'UI 在凌晨也只采用脑返回的开发期开窗结论',
)
brainOpenedWindow.overview.runtime.workflowMode = 'replyOnly'
brainOpenedWindow.overview.runtime.workflowStatus = 'waitingDailyWindow'
brainOpenedWindow.overview.runtime.communicationState = 'waitingDailyWindow'
const openedWaiting = adaptProductSnapshot(brainOpenedWindow, new Date(2026, 6, 25, 1, 0))
check(
  openedWaiting.overview.workflow.canResume &&
    openedWaiting.overview.workflow.stateLabel === '等待手动恢复' &&
    openedWaiting.overview.workflow.positionLabel === '业务运行已停在成员边界，等待手动恢复' &&
    openedWaiting.overview.communication.stateLabel === '等待手动恢复',
  '开发期开窗后旧 waitingDailyWindow 状态不再误提示等待 08:00',
)
check(
  productCandidatePath('interviewed').includes('view=interviewed') &&
    productCandidatePath('interviewElapsed').includes('view=interviewElapsed'),
  '产品页名称映射到唯一后端候选视图',
)

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

const unknownInternalCodes = structuredClone(snapshot)
unknownInternalCodes.candidates.communicating.candidates.items = [rawCandidate({
  status: 'newPlatformState',
  endReason: 'opaqueReasonCode',
})]
const unknownProduct = adaptProductSnapshot(unknownInternalCodes, now)
check(
  unknownProduct.candidates.communicating[0].statusLabel === '状态待确认' &&
    !JSON.stringify(unknownProduct.candidates.communicating[0]).includes('newPlatformState'),
  '未知候选状态不向普通客户端透出内部枚举值',
)
const unknownDetail = adaptCandidateDetail({
  candidate: {
    candidate: rawCandidate(),
    resume: {
      available: false,
      basic: [],
      expectations: [],
      selfEvaluation: '',
      education: '',
      workExperiences: '',
      truncated: false,
    },
    messages: [{
      seq: 1,
      direction: 'in',
      kind: 'opaqueMessageKind',
      tsApproxMs: todayAt(9, 0),
    }],
    latestAi: {
      available: true,
      status: 'completed',
      intentLabel: 'opaqueIntent',
      intentSource: 'opaqueSource',
      failure: 'providerInternalCode',
      classifiedAt: new Date(2026, 6, 25, 9, 21).toISOString(),
    },
    actions: [{
      kind: 'opaqueAction',
      status: 'opaqueStatus',
      failure: 'internalActionFailure',
      createdAt: new Date(2026, 6, 25, 9, 22).toISOString(),
    }],
  },
}, product.candidates.communicating[0], now)
check(
  unknownDetail.messages[0].kindLabel === '其他消息' &&
    unknownDetail.decisions[0].summary === '意向：待确认；来源：其他来源；异常：本轮判断未完成' &&
    unknownDetail.actions[0].label === '业务动作' &&
    unknownDetail.actions[0].resultLabel === '需要人工复核',
  '未知消息、AI 与动作枚举统一收敛为普通用户文案',
)

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
process.exit(fail === 0 ? 0 : 1)
