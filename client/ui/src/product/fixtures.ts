import type {
  CandidateViewItem,
  ConfirmationCandidateView,
  FunnelStageView,
  ProductData,
} from './types'

const funnelStages: FunnelStageView[] = [
  { key: 'collect', label: '采集', state: 'complete', completed: 30, target: 30, failed: 0 },
  { key: 'score', label: '评分', state: 'complete', completed: 30, target: 30, failed: 0 },
  { key: 'select', label: '筛选', state: 'complete', completed: 18, target: 30, failed: 0 },
  { key: 'greeting', label: '生成招呼语', state: 'complete', completed: 18, target: 18, failed: 0 },
  { key: 'confirm', label: '等待确认', state: 'active', completed: 0, target: 18, failed: 0 },
  { key: 'send', label: '发送招呼', state: 'pending', completed: 0, target: 18, failed: 0 },
]

function candidate(index: number, overrides: Partial<CandidateViewItem> = {}): CandidateViewItem {
  return {
    profileId: `fixture-profile-${index}`,
    displayName: `演示候选人 ${String.fromCharCode(64 + index)}`,
    age: 25 + index,
    education: index % 2 === 0 ? '本科' : '硕士',
    experience: `${index + 2} 年经验`,
    city: '上海',
    currentRole: index % 2 === 0 ? '招聘顾问' : '客户成功',
    jobName: '高级招聘顾问',
    statusLabel: '已回复',
    statusTone: 'blue',
    lastMessage: '您好，我想进一步了解岗位的团队情况。',
    lastActiveAt: `今天 ${9 + index}:20`,
    unreadCount: index === 1 ? 2 : 0,
    manualRequired: false,
    resumeSummary: '具备招聘全流程经验，熟悉中高端岗位交付和候选人关系维护。',
    deterministicState: '已回复，等待我方处理',
    latestAiDecision: '候选人有明确了解意愿，建议先回答团队问题，再邀请线上沟通。',
    manualReason: null,
    interviewAt: null,
    interviewMethod: null,
    interviewResult: null,
    wechatAccount: null,
    wechatExchangedAt: null,
    stillInAutoCommunication: true,
    messages: [
      { id: `fixture-message-${index}-1`, direction: 'outgoing', content: '您好，想和您沟通一下这个机会。', occurredAt: '今天 09:12' },
      { id: `fixture-message-${index}-2`, direction: 'incoming', content: '您好，我想进一步了解岗位的团队情况。', occurredAt: '今天 09:20' },
    ],
    decisions: [
      { id: `fixture-decision-${index}`, label: '建议回复', summary: '回答团队情况并推进一次线上沟通。', occurredAt: '今天 09:21' },
    ],
    actions: [
      { id: `fixture-action-${index}`, label: '首次招呼', resultLabel: '服务端已确认', occurredAt: '今天 09:12', tone: 'success' },
    ],
    ...overrides,
  }
}

const communicating = [
  candidate(1),
  candidate(2, {
    displayName: '演示候选人 B',
    statusLabel: '需要人工',
    statusTone: 'red',
    manualRequired: true,
    manualReason: '候选人问题超出当前职位事实库。',
    lastMessage: '这份岗位是否支持异地办公？',
  }),
  candidate(3, {
    displayName: '演示候选人 C',
    statusLabel: '等候回复',
    statusTone: 'slate',
    lastMessage: '已发送岗位介绍，正在等待候选人回复。',
  }),
]

const pendingInterview = [
  candidate(4, {
    displayName: '演示候选人 D',
    statusLabel: '已确认',
    statusTone: 'green',
    lastMessage: '好的，周五下午可以。',
    interviewAt: '7 月 25 日 14:00',
    interviewMethod: '微信视频',
  }),
  candidate(5, {
    displayName: '演示候选人 E',
    statusLabel: '待候选人确认',
    statusTone: 'amber',
    interviewAt: '7 月 25 日 16:30',
    interviewMethod: '微信视频',
  }),
]

const interviewed = [
  candidate(6, {
    displayName: '演示候选人 F',
    statusLabel: '待回填',
    statusTone: 'amber',
    interviewAt: '7 月 24 日 15:00',
    interviewMethod: '微信视频',
    interviewResult: null,
  }),
  candidate(7, {
    displayName: '演示候选人 G',
    statusLabel: '已录用',
    statusTone: 'green',
    interviewAt: '7 月 23 日 10:30',
    interviewMethod: '微信视频',
    interviewResult: '已录用',
  }),
]

const wechat = [
  candidate(8, {
    displayName: '演示候选人 H',
    statusLabel: '已换微信',
    statusTone: 'green',
    wechatAccount: 'fixture_wechat_account',
    wechatExchangedAt: '7 月 24 日 11:08',
  }),
  candidate(9, {
    displayName: '演示候选人 I',
    statusLabel: '账号待收编',
    statusTone: 'slate',
    wechatAccount: null,
    wechatExchangedAt: '7 月 23 日 17:42',
  }),
]

const confirmationCandidates: ConfirmationCandidateView[] = Array.from({ length: 4 }, (_, index) => ({
  ...candidate(index + 10, {
    displayName: `演示待确认 ${String.fromCharCode(65 + index)}`,
    lastMessage: null,
    messages: [],
    decisions: [],
    actions: [],
  }),
  aiScore: 86 - index * 2,
  greeting: '您好，看到您的经历与我们正在招聘的高级招聘顾问岗位比较匹配，想和您简单沟通一下。',
  generationStateLabel: '已生成',
  sendState: 'ready',
  sendStateLabel: '待发送',
  selectable: true,
}))

export function createProductFixture(): ProductData {
  return {
    customer: {
      name: '演示客户',
      shortName: '演',
      authorizationLabel: '授权有效',
      authorized: true,
      activationRequired: false,
      job: {
        backendJobId: 'fixture-job-42',
        name: '高级招聘顾问',
        syncState: 'synced',
        syncStateLabel: '配置已同步',
        environment: '智联招聘',
        lastSyncedAt: '今天 07:58',
      },
    },
    overview: {
      dateLabel: '2026 年 7 月 25 日 星期六',
      refreshedAt: '今天 09:32',
      businessWindowLabel: '运行时间 08:00～24:00',
      businessWindowOpen: true,
      workflow: {
        mode: 'full',
        state: 'awaitingConfirmation',
        stateLabel: '等待人工确认',
        positionLabel: '18 位候选人的招呼语已经生成',
        canStart: false,
        canPause: true,
        canResume: false,
        unavailableReason: null,
      },
      funnel: {
        stateLabel: '等待候选确认',
        target: 30,
        pending: 18,
        failed: 0,
        latestFailure: null,
        stages: funnelStages,
      },
      communication: {
        state: 'running',
        stateLabel: '运行中',
        lastPatrolAt: '今天 09:31',
      },
      todayMetrics: [
        { label: 'AI 评级人数', value: 30, tone: 'blue' },
        { label: '候选确认人数', value: 18, tone: 'amber' },
        { label: '打招呼', value: 6, tone: 'green' },
        { label: '已约面', value: 2, tone: 'red' },
      ],
      ledgerStartedAt: '2026-07-20',
      ledger: [
        { label: '累计招呼', value: 126 },
        { label: '累计已面试', value: 14 },
        { label: '累计已换微信', value: 31 },
      ],
      todayInterviews: [
        {
          profileId: 'fixture-interview-1',
          displayName: '演示候选人 D',
          jobName: '高级招聘顾问',
          interviewAt: '14:00',
          method: '微信视频',
          confirmationLabel: '已确认',
        },
      ],
      todayActivity: {
        greeted: 6,
        greetingDisplayTarget: 100,
        newReplies: 3,
        newInterviews: 2,
        completedInterviews: 1,
      },
    },
    confirmation: {
      batchId: 'fixture-batch',
      createdAt: '今天 08:16',
      scoreCompleted: 30,
      selectedCount: 18,
      greetingSucceeded: 18,
      greetingFailed: 0,
      greetingPending: 0,
      workflowPaused: false,
      businessWindowOpen: true,
      candidates: confirmationCandidates,
    },
    candidates: {
      communicating,
      pendingInterview,
      interviewed,
      wechat,
    },
    connections: [
      { label: '客户授权', value: '授权有效', tone: 'success', detail: '当前设备已绑定' },
      { label: '后台职位配置', value: '已同步', tone: 'success', detail: '最近同步：今天 07:58' },
      { label: 'AI 模型', value: 'DeepSeek V4 Pro', tone: 'success', detail: '已配置，密钥不在此处显示' },
      { label: 'Chrome 插件', value: '在线', tone: 'success', detail: '版本与契约一致' },
      { label: '客户端版本', value: '0.1.0', tone: 'neutral', detail: '开发者模式' },
    ],
    confirmationBadge: confirmationCandidates.length,
    clientVersion: '0.1.0',
  }
}

export function createEmptyProductData(): ProductData {
  return {
    customer: {
      name: '尚未激活',
      shortName: '客',
      authorizationLabel: '等待激活',
      authorized: false,
      activationRequired: false,
      job: {
        backendJobId: null,
        name: null,
        syncState: 'unavailable',
        syncStateLabel: '尚未同步职位',
        environment: '智联招聘',
        lastSyncedAt: null,
      },
    },
    overview: {
      dateLabel: new Intl.DateTimeFormat('zh-CN', {
        year: 'numeric',
        month: 'long',
        day: 'numeric',
        weekday: 'long',
      }).format(new Date()),
      refreshedAt: null,
      businessWindowLabel: '运行时间 08:00～24:00',
      businessWindowOpen: false,
      workflow: {
        mode: 'none',
        state: 'idle',
        stateLabel: '尚未开始',
        positionLabel: null,
        canStart: false,
        canPause: false,
        canResume: false,
        unavailableReason: '完成激活并绑定职位后可开始',
      },
      funnel: {
        stateLabel: '尚无运行批次',
        target: null,
        pending: null,
        failed: null,
        latestFailure: null,
        stages: [
          { key: 'collect', label: '采集', state: 'pending', completed: 0, target: null, failed: 0 },
          { key: 'score', label: '评分', state: 'pending', completed: 0, target: null, failed: 0 },
          { key: 'select', label: '筛选', state: 'pending', completed: 0, target: null, failed: 0 },
          { key: 'greeting', label: '生成招呼语', state: 'pending', completed: 0, target: null, failed: 0 },
          { key: 'confirm', label: '等待确认', state: 'pending', completed: 0, target: null, failed: 0 },
          { key: 'send', label: '发送招呼', state: 'pending', completed: 0, target: null, failed: 0 },
        ],
      },
      communication: {
        state: 'idle',
        stateLabel: '未运行',
        lastPatrolAt: null,
      },
      todayMetrics: [
        { label: 'AI 评级人数', value: 0, tone: 'blue' },
        { label: '候选确认人数', value: 0, tone: 'amber' },
        { label: '打招呼', value: 0, tone: 'green' },
        { label: '已约面', value: 0, tone: 'red' },
      ],
      ledgerStartedAt: null,
      ledger: [
        { label: '累计招呼', value: 0 },
        { label: '累计已面试', value: 0 },
        { label: '累计已换微信', value: 0 },
      ],
      todayInterviews: [],
      todayActivity: {
        greeted: 0,
        greetingDisplayTarget: 100,
        newReplies: 0,
        newInterviews: 0,
        completedInterviews: 0,
      },
    },
    confirmation: {
      batchId: null,
      createdAt: null,
      scoreCompleted: 0,
      selectedCount: 0,
      greetingSucceeded: 0,
      greetingFailed: 0,
      greetingPending: 0,
      workflowPaused: false,
      businessWindowOpen: false,
      candidates: [],
    },
    candidates: {
      communicating: [],
      pendingInterview: [],
      interviewed: [],
      wechat: [],
    },
    connections: [
      { label: '客户授权', value: '等待激活', tone: 'warning', detail: '激活后会显示正式授权状态' },
      { label: '后台职位配置', value: '尚未同步', tone: 'neutral' },
      { label: 'AI 模型', value: '尚未配置', tone: 'neutral' },
      { label: 'Chrome 插件', value: '等待连接', tone: 'neutral' },
      { label: '客户端版本', value: '0.1.0', tone: 'neutral' },
    ],
    confirmationBadge: 0,
    clientVersion: '0.1.0',
  }
}
