// 职位与发布：后台启用职位总览 + 预检 + 两段式循环发布。
// 这一页是唯一会对平台产生不可逆副作用的入口，所以从模型配置里单拆出来。
//
// 两段式（甲方 2026-07-31 裁决）：
//   阶段 A  零副作用，逐个职位跑「定类别 → 读词库 → 选关键词」，失败即跳过
//   二次确认清单  把两项 AI 决定摊开给人看过，默认全选可发的
//   阶段 B  逐个真发，每个发成之后紧接着把它下线；单条干净失败或 suspect
//           跳过当前、继续下一个
import { useCallback, useEffect, useRef, useState } from 'react'
import {
  api, AccountView, BackendJobView, DetailedError, JobClassAssignmentView, JobDraftReport,
  JobKeywordPlanView, JobPublishPrecheckView, JobPublishResult, JobTakeOfflineResult,
  PublishParamsState, PublishVerdict,
} from '../../api'
import { errorText } from '../format'

const PUBLISH_PARAMS_LABEL: Record<PublishParamsState, string> = {
  present: '已填写',
  empty: '空白',
  absent: '未创建',
}

const PUBLISH_VERDICT_LABEL: Record<PublishVerdict, string> = {
  ready: '可发布',
  existing: '平台已存在',
  blocked: '参数有问题',
}

// 失败现场。按固定顺序摊平成人读的行，未知键一律兜底显示，
// 避免手侧以后加了字段界面这边静默丢掉。
const DIAG_LABELS: Array<[string, string]> = [
  ['step', '卡在'],
  ['reason', '原因'],
  ['platformHints', '平台提示'],
  ['descriptionLength', '描述已填字数'],
  ['formReused', '复用了上一趟表单'],
  ['keywordMore', '更多折叠区'],
  ['keywordAt', '关键词进度'],
  ['keyword', '当前关键词'],
  ['keywordRoute', '走的路径'],
  ['jobClass', '选定类别'],
  ['prefilledClass', '平台预填类别'],
  ['matched', '已命中'],
  ['custom', '已自定义'],
  ['dropped', '已丢弃'],
  ['sectionTitles', '当时分组'],
  ['availableSample', '当时词库'],
  ['customInputVisible', '自定义输入框可见'],
  ['handMessage', '手侧报错'],
]

// 一个职位在本次循环里的全部状态。
interface RowState {
  classView?: JobClassAssignmentView
  /** 人工改选的类别。改了就得重新选关键词——词库随类别变。 */
  classPick?: string
  plan?: JobKeywordPlanView
  draft?: JobDraftReport
  /** 试填时代招公司实际选中情况与后台配置的比对提示，随 draft 一起来。 */
  draftHint?: string
  publishResult?: JobPublishResult
  /** 发布成功后紧跟的下线结果。它独立于 publishResult——下线没成不改发布结论。 */
  offlineResult?: JobTakeOfflineResult
  /** 下线整个没跑成（派发失败、超时）时的人读原因。同样不影响发布结论。 */
  offlineError?: string
  error?: string
  diagnostics?: Record<string, unknown>
  /** 阶段 A 失败被跳过。它不阻塞其余职位。 */
  skipped?: boolean
  /** 二次确认清单里的勾选。 */
  selected?: boolean
}

type LoopPhase = 'idle' | 'planning' | 'publishing'

// 循环进度分两种形态:收齐候选加统一分配是一整段、没有中途里程碑,显示 1/N
// 会被读成"第 1 个职位卡住了",只能给耗时;逐个跑的段落才有 1/N 与职位名。
type LoopCursor =
  | { kind: 'gather' }
  | { kind: 'step'; done: number; total: number; jobName: string }

export function JobPublishPage({ account }: { account: AccountView | null }) {
  return <BackendJobsTable account={account} />
}

function formatClock(seconds: number): string {
  const minutes = Math.floor(seconds / 60)
  return minutes > 0 ? `${minutes} 分 ${String(seconds % 60).padStart(2, '0')} 秒` : `${seconds} 秒`
}

function formatDiagValue(value: unknown): string {
  if (value === null || value === undefined) return '—'
  if (Array.isArray(value)) return value.length === 0 ? '(空)' : value.join('、')
  if (typeof value === 'boolean') return value ? '是' : '否'
  return String(value)
}

// 行首状态锚点。它回答的是"这一行现在处于什么阶段、要不要人管",细节都在
// 折叠块与结果块里;没有任何运行期状态时返回 null,行首回落到预检结论。
// 色彩语义全页统一:绿=定案的好结果,黄=要人拿主意,红=要人善后,灰=中间态。
function rowRuntimeStatus(
  state: RowState, stale: boolean,
): { label: string; tone: 'ok' | 'warn' | 'bad' | 'dim' } | null {
  if (state.publishResult) {
    return state.publishResult.report?.postingVisible === true
      ? { label: '已发成', tone: 'ok' }
      : { label: '发布未确认', tone: 'bad' }
  }
  if (state.skipped) return { label: '已跳过', tone: 'warn' }
  if (state.error) return { label: '出错', tone: 'bad' }
  if (stale) return { label: '待重选词', tone: 'warn' }
  if (state.plan) return { label: '已选定', tone: 'ok' }
  if (state.classView) return { label: '已定类别', tone: 'dim' }
  return null
}

// 依据折叠块的标题。必须自带一眼结论:词库是"模型没选到好词"还是"平台压根
// 没给",不点开也分得清——这是词库此前整份摊开想保住的判断,折叠后由标题接棒。
function evidenceSummary(state: RowState): string {
  const parts: string[] = []
  if (state.classView) parts.push(`类别候选 ${state.classView.candidates.length} 个`)
  const plan = state.plan
  if (plan) {
    const wordCount = plan.sections.reduce((sum, section) => sum + section.words.length, 0)
    parts.push(wordCount === 0
      ? '平台没给现成词条'
      : `词库 ${plan.sections.length} 组 ${wordCount} 词`)
    parts.push(`命中 ${plan.matched.length} · 自定义 ${plan.custom.length}`)
  }
  return parts.join(' · ')
}

function DraftDiagnosticsBlock({ diagnostics }: { diagnostics: Record<string, unknown> }) {
  const known = new Set(DIAG_LABELS.map(([key]) => key))
  const extras = Object.keys(diagnostics).filter((key) => !known.has(key))
  return (
    <div className="publish-draft-report publish-draft-diag">
      {DIAG_LABELS.filter(([key]) => diagnostics[key] !== undefined).map(([key, label]) => (
        <p key={key}><span>{label}</span><strong>{formatDiagValue(diagnostics[key])}</strong></p>
      ))}
      {extras.map((key) => (
        <p key={key}><span>{key}</span><strong>{formatDiagValue(diagnostics[key])}</strong></p>
      ))}
    </div>
  )
}

// 试填回读报告。它证明的是"能不能填进去"，不是"发布了"——手侧原语不允许
// 点击提交控件，且回读后必须离开表单。试填不在循环里，只作单职位排障。
function DraftReportBlock({ report, hint }: { report: JobDraftReport; hint?: string }) {
  const { keywords } = report
  return (
    <div className="publish-draft-report">
      <p>
        <span>类别</span><strong>{report.jobClass}</strong>
        <span>薪资</span><strong>{report.salaryMin}-{report.salaryMax} × {report.salaryMonths}</strong>
        <span>学历经验</span><strong>{report.education} · {report.experience}</strong>
      </p>
      <p>
        <span>描述</span><strong>{report.descriptionLength} 字</strong>
        <span>人数</span><strong>{report.headcount}</strong>
        <span>地址</span><strong>{report.workplace}</strong>
      </p>
      <p>
        <span>关键词</span>
        <strong>命中 {keywords.matched.length} · 自定义 {keywords.custom.length} · 丢弃 {keywords.dropped.length}</strong>
      </p>
      {keywords.dropped.length > 0 && (
        <p className="publish-draft-dropped">未能填入：{keywords.dropped.join('、')}</p>
      )}
      {hint && (
        <p className="publish-draft-sections">代招公司：{hint}</p>
      )}
      <p className={report.discarded ? 'publish-draft-clean' : 'publish-draft-dropped'}>
        {report.discarded ? '已离开发布表单，页面未留草稿' : '警告：未确认离开发布表单，请人工检查页面'}
      </p>
    </div>
  )
}

// 发布结果。postingVisible 是平台正证：没有它就不算成功，界面必须说清"可能已经
// 发出去了但没验到"，而不是含糊地显示失败——那会诱导人再发一次。
function PublishResultBlock({ result }: { result: JobPublishResult }) {
  const confirmed = result.report?.postingVisible === true
  return (
    <div className={`publish-draft-report ${confirmed ? '' : 'publish-draft-diag'}`}>
      <p>
        <span>发布</span>
        <strong className={confirmed ? 'ok' : 'bad'}>
          {confirmed ? '已取得平台正证' : '未确认——请去平台核对，不要重发'}
        </strong>
        <span>意图</span><strong>{result.intentId}</strong>
        <span>账本状态</span><strong>{result.status}</strong>
      </p>
      {result.report && (
        <p>
          <span>选定类别</span><strong>{result.report.jobClass}</strong>
          <span>回读轮次</span><strong>{result.report.verifyRounds}</strong>
        </p>
      )}
      {result.report?.platformFeedback && (
        <p className="publish-draft-sections">平台提示：{result.report.platformFeedback}</p>
      )}
      {result.partnerCompanyHint && (
        <p className="publish-draft-sections">代招公司：{result.partnerCompanyHint}</p>
      )}
    </div>
  )
}

// 下线结果。刻意与发布结果分开渲染，也刻意不用 bad 配色：甲方 2026-08-13 裁决
// 下线只是锦上添花，没成就是一行记录，不是要人去处理的故障。
function OfflineResultBlock(
  { result, error }: { result?: JobTakeOfflineResult; error?: string },
) {
  if (!result && !error) return null
  const confirmed = result?.report?.offlineVisible === true
  return (
    <div className="publish-draft-report">
      <p>
        <span>下线</span>
        <strong className={confirmed ? 'ok' : ''}>
          {confirmed ? '已取得平台正证' : '未确认——已记一笔，不影响上面的发布结论'}
        </strong>
        {result && <><span>意图</span><strong>{result.intentId}</strong></>}
        {result && <><span>账本状态</span><strong>{result.status}</strong></>}
      </p>
      {error && <p className="publish-draft-sections">{error}</p>}
      {result?.report?.platformFeedback && (
        <p className="publish-draft-sections">平台提示：{result.report.platformFeedback}</p>
      )}
    </div>
  )
}

// 类别决定面板。候选是平台针对这个职位现给的封闭集合，点一个即可覆盖大模型的
// 选择——模型只是建议者，最后按哪个发由人定。释义是平台自己的定义，也是判断
// 贴合度的依据，所以必须显示出来而不是只列名字。
function JobClassBlock({
  view, effective, collidesWith, onPick,
}: {
  view: JobClassAssignmentView
  effective: string
  collidesWith: string[]
  onPick: (name: string) => void
}) {
  const overridden = effective !== view.jobClass
  return (
    <div className="publish-draft-report publish-job-class">
      <p>
        <span>将以此类别发布</span>
        <strong className={collidesWith.length > 0 ? 'bad' : 'ok'}>{effective || '未分配'}</strong>
        <span>来源</span>
        <strong>
          大模型统一分配
          {view.confidence !== undefined && ` · 置信度 ${view.confidence.toFixed(2)}`}
          {overridden && ' · 已被人工改选'}
        </strong>
      </p>
      {collidesWith.length > 0 && (
        <p className="publish-draft-dropped">
          与 {collidesWith.join('、')} 撞了同一个类别。平台会把它们推给同一批人，
          多发的那几个等于白发——换一个还没被占用的候选，或者接受。
        </p>
      )}
      {view.problem && <p className="publish-precheck-issue">{view.problem}</p>}
      {view.deadConfiguredClass && (
        <p>
          <span>后台填的是</span>
          <strong>{view.deadConfiguredClass} —— 死字段，不参与发布</strong>
          {view.prefilledClass && <><span>平台预填</span><strong>{view.prefilledClass}</strong></>}
        </p>
      )}
      {view.reason && <p className="publish-draft-sections">选择理由：{view.reason}</p>}
      <ul className="publish-job-class-list">
        {view.candidates.map((candidate: JobClassAssignmentView['candidates'][number]) => (
          <li key={candidate.name} className={candidate.name === effective ? 'is-picked' : undefined}>
            <button type="button" onClick={() => onPick(candidate.name)} title="改用这个类别发布">
              {candidate.name}
            </button>
            <small>{candidate.definition}</small>
          </li>
        ))}
      </ul>
    </div>
  )
}

// 关键词决定面板。词库是平台在这个类别下现给的封闭集合；命中词库的走点选，
// 其余走兜底组自定义——两条路语义不同，所以分开显示而不是混成一串。
function KeywordPlanBlock({ plan, stale }: { plan: JobKeywordPlanView; stale: boolean }) {
  return (
    <div className={`publish-draft-report publish-keywords ${stale ? 'publish-draft-diag' : ''}`}>
      <p>
        <span>将填入这些关键词</span>
        <strong className={stale ? 'bad' : 'ok'}>{plan.keywords.join(' · ')}</strong>
      </p>
      {stale && (
        <p className="publish-draft-dropped">
          类别已被改选，这组关键词是按上一个类别的词库选的，必须重新选。
        </p>
      )}
      <p>
        <span>命中词库</span><strong>{plan.matched.length > 0 ? plan.matched.join('、') : '(无)'}</strong>
        <span>自定义</span><strong>{plan.custom.length > 0 ? plan.custom.join('、') : '(无)'}</strong>
      </p>
      {plan.reason && <p className="publish-draft-sections">选择理由：{plan.reason}</p>}
      {/* 平台这次给出的词库要原样摊开。它是选词的封闭候选集，而且只有这一次
          机会被看见——弹层关掉就没了，词库又随类别与描述变化、事后无从复原。
          只列分组标题不够：那样看不出"模型没选到好词"和"平台压根没给"的区别。 */}
      <p className="publish-draft-sections">
        平台这次给出的词库（选中的标绿）
        {plan.totalQuota ? ` · 平台总配额 ${plan.totalQuota}` : ''}
        {plan.formReused ? ' · 复用了上一趟表单' : ' · 重填了表单'}
      </p>
      <ul className="publish-vocab">
        {plan.sections.map((section) => (
          <li key={section.title}>
            <em>
              {section.title}
              {section.limit ? `（最多 ${section.limit}）` : ''}
            </em>
            {section.words.length === 0
              ? <small className="publish-vocab-empty">平台没给现成词条，只能自定义</small>
              : (
                <span className="publish-vocab-words">
                  {section.words.map((word) => (
                    <code
                      key={word}
                      className={plan.matched.includes(word) ? 'is-picked' : undefined}
                    >
                      {word}
                    </code>
                  ))}
                </span>
              )}
          </li>
        ))}
        {plan.sections.length === 0 && <li><em>平台没有给出任何分组</em></li>}
      </ul>
      {plan.deadConfiguredKeywords && plan.deadConfiguredKeywords.length > 0 && (
        <p className="publish-draft-sections">
          后台填的是：{plan.deadConfiguredKeywords.join('、')} —— 死字段，不参与发布
        </p>
      )}
      {plan.attempts && plan.attempts.length > 1 && (
        <p className="publish-draft-dropped">大模型尝试：{plan.attempts.join('、')}</p>
      )}
    </div>
  )
}

// 预检结论 + 两段式循环。发布按钮需要连点两次，因为它是这条链上唯一不可逆的动作。
function PublishPrecheckPanel({
  view, at, account,
}: { view: JobPublishPrecheckView; at: string; account: AccountView | null }) {
  const [rows, setRows] = useState<Record<string, RowState>>({})
  const [phase, setPhase] = useState<LoopPhase>('idle')
  const [busyJob, setBusyJob] = useState('')
  const [cursor, setCursor] = useState<LoopCursor | null>(null)
  const [gatherSeconds, setGatherSeconds] = useState(0)
  const [publishArmed, setPublishArmed] = useState(false)
  const [draftBusy, setDraftBusy] = useState('')
  // 脑侧那次全局分配的尝试记录。撞车不存这里：人工改选类别后原来的撞车可能
  // 解开、也可能撞上别人，只能按当前生效值实时重算，见 collisionsNow。
  const [attempts, setAttempts] = useState<string[]>([])
  const [batchError, setBatchError] = useState('')
  // 停止只影响"要不要开始下一个职位"。已经派出去的那条命令照常收束——半路
  // 掐掉一条在途的 effectful 命令只会制造一个结果未知的 suspect。
  // stopRequested 是它的显示镜像:ref 变了不重渲染,没有镜像人看不出点没点上。
  const stopRef = useRef(false)
  const [stopRequested, setStopRequested] = useState(false)

  // "再点一次"的确认针对的是确认那一刻看到的集合。行状态一变(勾选、改类别、
  // 重选词)旧确认就作废,不允许拿旧确认发新集合;放几秒不点也自动解除。
  useEffect(() => {
    if (!publishArmed) return
    const timer = setTimeout(() => setPublishArmed(false), 8000)
    return () => clearTimeout(timer)
  }, [publishArmed])

  // 收齐候选段的走表。这一段十来分钟没有别的可动的数字,表不走会被当成卡死。
  useEffect(() => {
    if (cursor?.kind !== 'gather') return
    setGatherSeconds(0)
    const timer = setInterval(() => setGatherSeconds((prev) => prev + 1), 1000)
    return () => clearInterval(timer)
  }, [cursor?.kind])

  const patch = (jobId: string, next: Partial<RowState>) => {
    setPublishArmed(false)
    setRows((prev) => ({ ...prev, [jobId]: { ...prev[jobId], ...next } }))
  }
  const failRow = (jobId: string, prefix: string, reason: unknown, next?: Partial<RowState>) => {
    patch(jobId, {
      error: `${prefix}：${errorText(reason)}`,
      diagnostics: reason instanceof DetailedError && reason.diagnostics
        ? reason.diagnostics as Record<string, unknown>
        : undefined,
      ...next,
    })
  }

  const readyRows = view.rows.filter((row) => row.verdict === 'ready')
  const otherRows = view.rows.filter((row) => row.verdict !== 'ready')
  const effectiveClass = (jobId: string): string => {
    const row = rows[jobId]
    return row?.classPick || row?.classView?.jobClass || ''
  }
  // 关键词是按某个类别的词库选的。类别一改，这组词就过期了——词库随类别变。
  const planStale = (jobId: string): boolean => {
    const plan = rows[jobId]?.plan
    return plan !== undefined && plan.jobClass !== effectiveClass(jobId)
  }
  const decided = (jobId: string): boolean => {
    const row = rows[jobId]
    return Boolean(row?.plan) && !planStale(jobId) && !row?.publishResult
  }
  // 撞车按**当前生效**的类别实时重算,不能直接用脑侧那次返回的那份:人工在
  // 清单上改选之后,原来的撞车可能解开了、也可能撞上了别人。
  const collisionsNow = ((): Record<string, string[]> => {
    const byClass: Record<string, string[]> = {}
    for (const row of readyRows) {
      const jobClass = effectiveClass(row.jobId)
      if (!jobClass) continue
      byClass[jobClass] = [...(byClass[jobClass] ?? []), row.jobName || row.jobId]
    }
    const out: Record<string, string[]> = {}
    for (const [jobClass, names] of Object.entries(byClass)) {
      if (names.length > 1) out[jobClass] = names
    }
    return out
  })()
  const collidesWith = (jobId: string, jobName: string): string[] => {
    const jobClass = effectiveClass(jobId)
    if (!jobClass) return []
    return (collisionsNow[jobClass] ?? []).filter((name) => name !== (jobName || jobId))
  }

  // A2 的一个职位：在已定类别下读词库并选关键词。零对外副作用。
  //
  // jobClass 必须由调用方传进来，**不能在这里从 rows 里取**：A2 紧跟在写入
  // 分配结果的 setRows 之后，而 React 的状态更新是异步的，同一个闭包里读到的
  // 还是写之前的 rows——取出来是空串，于是整个 A2 悄无声息地一个都不跑。
  const planKeywordsFor = async (
    jobId: string, jobName: string, jobClass: string,
  ): Promise<void> => {
    if (!account) return
    if (!jobClass) {
      // 走到这里说明调用方自己就没拿到类别。宁可留一条明账，也不静默跳过。
      patch(jobId, {
        error: `「${jobName}」没有可用的职位类别，跳过选关键词`,
        skipped: true, selected: false,
      })
      return
    }
    try {
      const plan = await api.jobPublishKeywordPlan(
        account.platform, account.accountRef, jobId, jobClass,
      )
      patch(jobId, { plan, selected: true, skipped: false })
    } catch (reason) {
      failRow(jobId, `给「${jobName}」选关键词未成功`, reason, { skipped: true, selected: false })
    }
  }

  // 单独重跑一个职位：类别与关键词都重来。把其余职位已定的类别作为"已占用"
  // 传进去，模型才会主动避开——否则单独重跑必然撞上别人已经占好的位置。
  const planOne = async (jobId: string, jobName: string): Promise<void> => {
    if (!account) return
    patch(jobId, { error: undefined, diagnostics: undefined, skipped: false })
    const occupied = readyRows
      .filter((row) => row.jobId !== jobId)
      .map((row) => effectiveClass(row.jobId))
      .filter(Boolean)
    let jobClass = ''
    try {
      const result = await api.jobPublishClassPlan(
        account.platform, account.accountRef, [jobId], occupied,
      )
      const assigned = result.jobs.find((row) => row.jobId === jobId)
      if (!assigned || !assigned.jobClass) {
        failRow(jobId, `定「${jobName}」的职位类别未成功`,
          new Error(assigned?.problem || '模型没有给出分配'), { skipped: true, selected: false })
        return
      }
      jobClass = assigned.jobClass
      patch(jobId, { classView: assigned, classPick: undefined, plan: undefined })
    } catch (reason) {
      failRow(jobId, `定「${jobName}」的职位类别未成功`, reason, { skipped: true, selected: false })
      return
    }
    // 用刚拿到的那个值，不回头读 rows——patch 还没生效。
    await planKeywordsFor(jobId, jobName, jobClass)
  }

  // 类别被人工改选后重选关键词：只跑第二趟，类别不动。这里从 rows 取类别是
  // 安全的——它由人点击触发，状态早已落定，不像 A2 那样紧跟在 setRows 后面。
  const replanKeywords = async (jobId: string, jobName: string): Promise<void> => {
    setBusyJob(jobId)
    patch(jobId, { error: undefined, diagnostics: undefined })
    try {
      await planKeywordsFor(jobId, jobName, effectiveClass(jobId))
    } finally {
      setBusyJob('')
    }
  }

  // 阶段 A 分三段：A1 收齐全部职位的候选 → 一次全局分配 → A2 逐个选关键词。
  //
  // A1 与全局分配合在同一次调用里（脑侧串行跑完全部职位的填页再问模型），所以
  // 那一段没有可上报的里程碑，只能给一句说明加耗时走表。这是"统一分配"的
  // 必然形态——候选没收齐就没法通盘决定谁该让开谁。
  const runPhaseA = async (): Promise<void> => {
    if (!account) return
    stopRef.current = false
    setStopRequested(false)
    setPhase('planning')
    setPublishArmed(false)
    const targets = readyRows
    try {
      setCursor({ kind: 'gather' })
      let planned: JobClassAssignmentView[]
      try {
        const result = await api.jobPublishClassPlan(
          account.platform, account.accountRef, targets.map((row) => row.jobId),
        )
        planned = result.jobs
        setAttempts(result.attempts ?? [])
        setBatchError('')
      } catch (reason) {
        setBatchError(`统一分配职位类别未成功：${errorText(reason)}`)
        return
      }
      setRows((prev) => {
        const next = { ...prev }
        for (const assigned of planned) {
          next[assigned.jobId] = {
            ...next[assigned.jobId],
            classView: assigned,
            classPick: undefined,
            plan: undefined,
            error: assigned.jobClass ? undefined : assigned.problem,
            skipped: !assigned.jobClass,
            selected: false,
          }
        }
        return next
      })

      // A2：只给分到类别的职位选关键词。没分到的已经标记跳过，不占用页面。
      const withClass = planned.filter((assigned) => Boolean(assigned.jobClass))
      for (const [index, assigned] of withClass.entries()) {
        if (stopRef.current) break
        setCursor({ kind: 'step', done: index, total: withClass.length, jobName: assigned.jobName })
        await planKeywordsFor(assigned.jobId, assigned.jobName, assigned.jobClass ?? '')
      }
    } finally {
      setCursor(null)
      setPhase('idle')
    }
  }

  // 发布成功后紧接着把它下线（甲方 2026-08-13 裁决）。
  //
  // 它是独立的一条意图，所以这里独立 try/catch，且**任何失败都不走 failRow**：
  // failRow 会给这一行挂上 error，看起来像发布出了问题，而发布那笔账已经成立。
  // 下线只是锦上添花，没成就记一笔，人不必处理。
  //
  // 2026-08-17 甲方指示：暂时取消发布后自动下线，整条链注释保留待恢复
  // （连同 publishOne 里的 published 正证跟踪与调用块）。
  // const takeOfflineAfterPublish = async (jobId: string, jobName: string): Promise<void> => {
  //   if (!account) return
  //   try {
  //     const result = await api.jobTakeOffline(account.platform, account.accountRef, jobId)
  //     patch(jobId, { offlineResult: result })
  //   } catch (reason) {
  //     patch(jobId, { offlineError: `下线「${jobName}」未成功：${errorText(reason)}` })
  //   }
  // }

  const publishOne = async (jobId: string, jobName: string): Promise<void> => {
    if (!account) return
    const jobClass = effectiveClass(jobId)
    const keywords = rows[jobId]?.plan?.keywords ?? []
    if (!jobClass || keywords.length === 0) return
    patch(jobId, { error: undefined, diagnostics: undefined, offlineResult: undefined, offlineError: undefined })
    // 2026-08-17 甲方指示：暂时取消发布后自动下线，published 正证跟踪随之一并注释。
    // let published = false
    try {
      const result = await api.jobPublishPublish(
        account.platform, account.accountRef, jobId, jobClass, keywords,
      )
      patch(jobId, { publishResult: result })
      // 只有拿到平台正证才下线。没拿到正证的那些，职位到底在不在线本身就不确定，
      // 再去点一次下线只会把一个不确定的现场搅得更乱——留给人去平台核对。
      // published = result.report?.postingVisible === true
    } catch (reason) {
      // 甲方 2026-07-31 裁决：单条干净失败或 suspect 跳过当前、继续下一个。
      // suspect 的意图按账本纪律永久冻结等人裁决，绝不在本批内重试。
      failRow(jobId, `发布「${jobName}」未成功`, reason)
    }
    // if (published && !stopRef.current) {
    //   await takeOfflineAfterPublish(jobId, jobName)
    // }
  }

  const runPhaseB = async (): Promise<void> => {
    if (!account) return
    if (!publishArmed) {
      setPublishArmed(true)
      return
    }
    setPublishArmed(false)
    stopRef.current = false
    setStopRequested(false)
    setPhase('publishing')
    const targets = readyRows.filter((row) => rows[row.jobId]?.selected && decided(row.jobId))
    try {
      for (const [index, row] of targets.entries()) {
        if (stopRef.current) break
        setCursor({ kind: 'step', done: index, total: targets.length, jobName: row.jobName })
        await publishOne(row.jobId, row.jobName)
      }
    } finally {
      setCursor(null)
      setPhase('idle')
    }
  }

  const tryDraft = async (jobId: string, jobName: string): Promise<void> => {
    if (!account) return
    const jobClass = effectiveClass(jobId)
    const keywords = rows[jobId]?.plan?.keywords ?? []
    if (!jobClass || keywords.length === 0) return
    setDraftBusy(jobId)
    patch(jobId, { error: undefined, diagnostics: undefined })
    try {
      const result = await api.jobPublishPrepareDraft(
        account.platform, account.accountRef, jobId, jobClass, keywords,
      )
      patch(jobId, { draft: result.report, draftHint: result.partnerCompanyHint })
    } catch (reason) {
      failRow(jobId, `试填「${jobName}」未成功`, reason)
    } finally {
      setDraftBusy('')
    }
  }

  const running = phase !== 'idle'
  const busy = running || busyJob !== '' || draftBusy !== ''
  const selectable = readyRows.filter((row) => decided(row.jobId))
  const selectedCount = selectable.filter((row) => rows[row.jobId]?.selected).length
  const publishedCount = readyRows.filter((row) => rows[row.jobId]?.publishResult).length
  const collisionCount = Object.keys(collisionsNow).length

  // "要人看"汇总条的四类。发布未确认排最前——它是全页最不该被淹没的信息。
  const unconfirmedRows = readyRows.filter((row) => {
    const result = rows[row.jobId]?.publishResult
    return result !== undefined && result.report?.postingVisible !== true
  })
  const erroredRows = readyRows.filter((row) => {
    const state = rows[row.jobId]
    return Boolean(state?.error) && !state?.skipped && !state?.publishResult
  })
  const skippedRows = readyRows.filter((row) => rows[row.jobId]?.skipped)
  const collisionFirstRow = readyRows.find((row) => collidesWith(row.jobId, row.jobName).length > 0)

  // 点汇总条跳到第一个该类的行。开发台够用,不做逐个循环跳。
  const jumpTo = (jobId: string) => {
    document.getElementById(`publish-row-${jobId}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }

  return (
    <div className="publish-precheck">
      <div className="publish-precheck-head">
        <strong>预检结论</strong>
        <small>
          可发布 {readyRows.length} 个 · 平台现存 {view.platformPostingCount} 个职位
          {at && ` · 预检于 ${at}`}
        </small>
      </div>

      <div className="publish-loop-bar">
        <button
          type="button"
          disabled={busy || !account || readyRows.length === 0}
          title="先收齐全部职位的类别候选并统一分配（尽量互不相同），再逐个读词库选关键词。全程零对外副作用，跑砸了随时重来"
          onClick={() => void runPhaseA()}
        >
          {phase === 'planning' ? '正在选…' : `阶段 A：统一定类别与关键词（${readyRows.length} 个）`}
        </button>
        <button
          type="button"
          className={publishArmed ? 'danger-button' : undefined}
          disabled={busy || !account || selectedCount === 0}
          title={selectedCount > 0
            ? '逐个真正发布到平台，求职者立刻可见；不可撤销，只能到平台手动下架'
            : '先跑阶段 A，并在下面勾选要发的职位'}
          onClick={() => void runPhaseB()}
        >
          {phase === 'publishing'
            ? '正在发布…'
            : publishArmed
              ? `确认发布这 ${selectedCount} 个？再点一次`
              : `确认发布（共 ${selectedCount} 个）`}
        </button>
        {running && (
          <button
            type="button"
            className="danger-button"
            disabled={stopRequested}
            onClick={() => { stopRef.current = true; setStopRequested(true) }}
          >
            {stopRequested ? '将在当前这个收尾后停止' : '跑完当前这个就停'}
          </button>
        )}
        {cursor && (
          <small className="publish-loop-cursor">
            {cursor.kind === 'gather'
              ? `正在收齐全部职位的类别候选并统一分配。这一段没有中途进度,通常要十来分钟 · 已等 ${formatClock(gatherSeconds)}`
              : `${phase === 'publishing' ? '正在发布' : '正在选关键词'} ${cursor.done + 1}/${cursor.total}：${cursor.jobName}`}
          </small>
        )}
        {!running && (selectedCount > 0 || publishedCount > 0) && (
          <small className="publish-loop-cursor">
            已选定 {selectable.length} 个 · 勾选 {selectedCount} 个
            {publishedCount > 0 && ` · 已发 ${publishedCount} 个`}
          </small>
        )}
      </div>
      {(unconfirmedRows.length > 0 || erroredRows.length > 0 || collisionFirstRow !== undefined
        || skippedRows.length > 0) && (
        <p className="publish-attention">
          <strong>要人看</strong>
          {unconfirmedRows.length > 0 && (
            <button
              type="button" className="st-bad"
              title="发出去了但没拿到平台正证。去平台核对，不要重发"
              onClick={() => jumpTo(unconfirmedRows[0].jobId)}
            >
              发布未确认 {unconfirmedRows.length}
            </button>
          )}
          {erroredRows.length > 0 && (
            <button
              type="button" className="st-bad"
              title="发布或准备阶段出了错，点过去看失败现场"
              onClick={() => jumpTo(erroredRows[0].jobId)}
            >
              出错 {erroredRows.length}
            </button>
          )}
          {collisionFirstRow && (
            <button
              type="button" className="st-warn"
              title="多个职位撞了同一类别，平台会推给同一批人，扩池打折。可逐个改选，也可以接受"
              onClick={() => jumpTo(collisionFirstRow.jobId)}
            >
              类别撞车 {collisionCount}
            </button>
          )}
          {skippedRows.length > 0 && (
            <button
              type="button" className="st-warn"
              title="阶段 A 没跑成被跳过的职位，不阻塞其余。可单独重跑"
              onClick={() => jumpTo(skippedRows[0].jobId)}
            >
              跳过 {skippedRows.length}
            </button>
          )}
        </p>
      )}
      {batchError && <p className="publish-precheck-issue">{batchError}</p>}
      {attempts.length > 1 && (
        <p className="publish-precheck-notice">大模型分配尝试：{attempts.join('、')}</p>
      )}

      <ul className="publish-precheck-list">
        {[...readyRows, ...otherRows].map((row) => {
          const state = rows[row.jobId] ?? {}
          const stale = planStale(row.jobId)
          const status = row.verdict === 'ready' ? rowRuntimeStatus(state, stale) : null
          const rowCollisions = row.verdict === 'ready' ? collidesWith(row.jobId, row.jobName) : []
          return (
            <li key={row.jobId} id={`publish-row-${row.jobId}`} className={`is-${row.verdict}`}>
              <div className="publish-precheck-row">
                {row.verdict === 'ready' && decided(row.jobId) && (
                  <label className="publish-precheck-pick" title="不勾选就不发这一个">
                    <input
                      type="checkbox"
                      checked={Boolean(state.selected)}
                      disabled={running}
                      onChange={(event) => patch(row.jobId, { selected: event.target.checked })}
                    />
                  </label>
                )}
                <span className={`publish-precheck-verdict${status ? ` st-${status.tone}` : ''}`}>
                  {status ? status.label : (PUBLISH_VERDICT_LABEL[row.verdict] || row.verdict)}
                </span>
                <strong>{row.jobName || '未命名职位'}</strong>
                {row.isCurrent && <em className="backend-jobs-current">当前职位</em>}
                <code>#{row.jobId}</code>
                {state.plan && !stale && (
                  <em className="publish-precheck-decided">
                    {effectiveClass(row.jobId)} · {state.plan.keywords.join(' ')}
                  </em>
                )}
                {row.verdict === 'ready' && !state.publishResult && (
                  <button
                    type="button"
                    disabled={busy || !account}
                    title="只重跑这一个职位的阶段 A"
                    onClick={() => {
                      setBusyJob(row.jobId)
                      void planOne(row.jobId, row.jobName).finally(() => setBusyJob(''))
                    }}
                  >
                    {busyJob === row.jobId ? '正在选…' : state.plan ? '重选' : '单独选一次'}
                  </button>
                )}
                {row.verdict === 'ready' && stale && (
                  <button
                    type="button"
                    disabled={busy || !account}
                    title="类别改了，词库跟着变，必须按新类别重选关键词"
                    onClick={() => void replanKeywords(row.jobId, row.jobName)}
                  >
                    重选关键词
                  </button>
                )}
                {row.verdict === 'ready' && decided(row.jobId) && (
                  <button
                    type="button"
                    disabled={busy || !account}
                    title="在发布页试填一次并回读，不会点击发布。排障用，不在循环里"
                    onClick={() => void tryDraft(row.jobId, row.jobName)}
                  >
                    {draftBusy === row.jobId ? '正在试填…' : '试填一次'}
                  </button>
                )}
              </div>
              {state.error && <p className="publish-precheck-issue">{state.error}</p>}
              {state.diagnostics && Object.keys(state.diagnostics).length > 0 && (
                <details className="publish-evidence">
                  <summary>失败现场（{Object.keys(state.diagnostics).length} 项）</summary>
                  <DraftDiagnosticsBlock diagnostics={state.diagnostics} />
                </details>
              )}
              {rowCollisions.length > 0 && (
                <p className="publish-precheck-warn">
                  与 {rowCollisions.join('、')} 撞了同一类别——平台会推给同一批人，扩池打折。
                  点开下面的依据可改选，也可以接受。
                </p>
              )}
              {(state.classView || state.plan) && (
                <details className="publish-evidence">
                  <summary>依据 · {evidenceSummary(state)}</summary>
                  {state.classView && (
                    <JobClassBlock
                      view={state.classView}
                      effective={effectiveClass(row.jobId)}
                      collidesWith={rowCollisions}
                      onPick={(name) => patch(row.jobId, { classPick: name })}
                    />
                  )}
                  {state.plan && <KeywordPlanBlock plan={state.plan} stale={stale} />}
                </details>
              )}
              {state.publishResult && <PublishResultBlock result={state.publishResult} />}
              <OfflineResultBlock result={state.offlineResult} error={state.offlineError} />
              {state.draft && <DraftReportBlock report={state.draft} hint={state.draftHint} />}
              {row.issues?.map((issue, index) => (
                <p key={`i-${index}`} className="publish-precheck-issue">
                  {issue.field ? `${issue.field}：` : ''}{issue.message}
                </p>
              ))}
              {row.notices?.map((notice, index) => (
                <p key={`n-${index}`} className="publish-precheck-notice">
                  {notice.field ? `${notice.field}：` : ''}{notice.message}
                </p>
              ))}
            </li>
          )
        })}
      </ul>
      {view.rows.length === 0 && <p className="m5-ai-empty">后台没有可预检的启用职位。</p>}
    </div>
  )
}

// 旧后台该客户的启用职位总览。只读投影：拉一次列表不写任何本地业务事实，
// 也不做执行约束校验——配置不全的职位正是这张表要显示的内容。
function BackendJobsTable({ account }: { account: AccountView | null }) {
  const [jobs, setJobs] = useState<BackendJobView[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [loadedAt, setLoadedAt] = useState('')
  const [precheck, setPrecheck] = useState<JobPublishPrecheckView | null>(null)
  const [precheckBusy, setPrecheckBusy] = useState(false)
  const [precheckError, setPrecheckError] = useState('')
  const [precheckAt, setPrecheckAt] = useState('')

  const precheckable = account !== null && account.handOnline && account.identityCurrent

  const runPrecheck = async () => {
    if (!account) return
    setPrecheckBusy(true)
    setPrecheckError('')
    try {
      const result = await api.jobPublishPrecheck(account.platform, account.accountRef)
      setPrecheck(result)
      setPrecheckAt(new Date().toLocaleTimeString('zh-CN'))
    } catch (reason) {
      // 预检失败时清掉上一次结论：它描述的是另一个时刻的平台状态，
      // 留着会让人以为"可发布"仍然成立。
      setPrecheck(null)
      setPrecheckError(errorText(reason))
    } finally {
      setPrecheckBusy(false)
    }
  }

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const result = await api.backendJobs()
      setJobs(Array.isArray(result.jobs) ? result.jobs : [])
      setLoadedAt(new Date().toLocaleTimeString('zh-CN'))
      setError('')
    } catch (reason) {
      // 刻意保留上一次的行：错误横幅与"数据时刻"一起呈现，
      // 比清空表格更能让人看出自己在看旧数据。
      setError(errorText(reason))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <section className="backend-jobs" aria-labelledby="backend-jobs-title">
      <header className="backend-jobs-head">
        <div>
          <strong id="backend-jobs-title">后台职位与发布</strong>
          <small>
            旧后台该客户的启用职位。归档的、没有生效版本的、以及重名被服务端去重的职位不在其中
            {loadedAt && ` · 数据读取于 ${loadedAt}`}
          </small>
        </div>
        <div className="backend-jobs-actions">
          <button
            type="button"
            disabled={precheckBusy || !precheckable}
            title={precheckable ? '读取平台现存职位并逐个校验发布参数，不发送任何内容' : '需要账号在线且身份已核对'}
            onClick={() => void runPrecheck()}
          >
            {precheckBusy ? '正在预检…' : '全部发布前预检'}
          </button>
          <button type="button" disabled={loading} onClick={() => void load()}>
            {loading ? '正在读取…' : '刷新'}
          </button>
        </div>
      </header>
      {error && <p className="m5-ai-message bad" role="alert">{error}</p>}
      {precheckError && <p className="m5-ai-message bad" role="alert">{precheckError}</p>}
      {precheck && (
        <PublishPrecheckPanel view={precheck} at={precheckAt} account={account} />
      )}
      <div className="backend-jobs-scroll">
        <table>
          <thead>
            <tr>
              <th scope="col">职位</th>
              <th scope="col">环境</th>
              <th scope="col">文档</th>
              <th scope="col">发布参数</th>
            </tr>
          </thead>
          <tbody>
            {jobs.map((job) => (
              <tr key={job.jobId}>
                <td>
                  <strong>{job.jobName || '未命名职位'}</strong>
                  {job.isCurrent && <em className="backend-jobs-current">当前职位</em>}
                  <code>#{job.jobId}</code>
                </td>
                <td>{job.environment || '未标注'}</td>
                <td>
                  {job.documentCount} 份
                  {job.missingDocs && job.missingDocs.length > 0 && (
                    <em className="backend-jobs-missing" title={`回复链路缺: ${job.missingDocs.join('、')}`}>
                      缺 {job.missingDocs.length} 份
                    </em>
                  )}
                </td>
                <td className={`backend-jobs-params is-${job.publishParams}`}>
                  {PUBLISH_PARAMS_LABEL[job.publishParams] || job.publishParams}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {jobs.length === 0 && !loading && !error && (
          <p className="m5-ai-empty">后台没有可下发的启用职位。若确认后台有职位，检查多职位下发是否被止血开关关闭。</p>
        )}
        {jobs.length === 0 && loading && <p className="m5-ai-empty">正在读取旧后台职位…</p>}
      </div>
    </section>
  )
}
