// 职位与发布：后台启用职位总览 + 预检 + 两段式循环发布。
// 这一页是唯一会对平台产生不可逆副作用的入口，所以从模型配置里单拆出来。
//
// 两段式（甲方 2026-07-31 裁决）：
//   阶段 A  零副作用，逐个职位跑「定类别 → 读词库 → 选关键词」，失败即跳过
//   二次确认清单  把两项 AI 决定摊开给人看过，默认全选可发的
//   阶段 B  逐个真发；单条干净失败或 suspect 跳过当前、继续下一个
import { useCallback, useEffect, useRef, useState } from 'react'
import {
  api, AccountView, BackendJobView, DetailedError, JobClassResolveView, JobDraftReport,
  JobKeywordPlanView, JobPublishPrecheckView, JobPublishResult, PublishParamsState, PublishVerdict,
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
  classView?: JobClassResolveView
  /** 人工改选的类别。改了就得重新选关键词——词库随类别变。 */
  classPick?: string
  plan?: JobKeywordPlanView
  draft?: JobDraftReport
  publishResult?: JobPublishResult
  error?: string
  diagnostics?: Record<string, unknown>
  /** 阶段 A 失败被跳过。它不阻塞其余职位。 */
  skipped?: boolean
  /** 二次确认清单里的勾选。 */
  selected?: boolean
}

type LoopPhase = 'idle' | 'planning' | 'publishing'

export function JobPublishPage({ account }: { account: AccountView | null }) {
  return <BackendJobsTable account={account} />
}

function formatDiagValue(value: unknown): string {
  if (value === null || value === undefined) return '—'
  if (Array.isArray(value)) return value.length === 0 ? '(空)' : value.join('、')
  if (typeof value === 'boolean') return value ? '是' : '否'
  return String(value)
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
function DraftReportBlock({ report }: { report: JobDraftReport }) {
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
    </div>
  )
}

// 类别决定面板。候选是平台针对这个职位现给的封闭集合，点一个即可覆盖大模型的
// 选择——模型只是建议者，最后按哪个发由人定。释义是平台自己的定义，也是判断
// 贴合度的依据，所以必须显示出来而不是只列名字。
function JobClassBlock({
  view, effective, onPick,
}: { view: JobClassResolveView; effective: string; onPick: (name: string) => void }) {
  const overridden = effective !== view.jobClass
  return (
    <div className="publish-draft-report publish-job-class">
      <p>
        <span>将以此类别发布</span><strong className="ok">{effective}</strong>
        <span>来源</span>
        <strong>
          大模型选定
          {view.confidence !== undefined && ` · 置信度 ${view.confidence.toFixed(2)}`}
          {overridden && ' · 已被人工改选'}
        </strong>
      </p>
      {view.deadConfiguredClass && (
        <p>
          <span>后台填的是</span>
          <strong>{view.deadConfiguredClass} —— 死字段，不参与发布</strong>
          {view.prefilledClass && <><span>平台预填</span><strong>{view.prefilledClass}</strong></>}
        </p>
      )}
      {view.reason && <p className="publish-draft-sections">选择理由：{view.reason}</p>}
      <ul className="publish-job-class-list">
        {view.candidates.map((candidate: JobClassResolveView['candidates'][number]) => (
          <li key={candidate.name} className={candidate.name === effective ? 'is-picked' : undefined}>
            <button type="button" onClick={() => onPick(candidate.name)} title="改用这个类别发布">
              {candidate.name}
            </button>
            <small>{candidate.definition}</small>
          </li>
        ))}
      </ul>
      {view.attempts && view.attempts.length > 1 && (
        <p className="publish-draft-dropped">大模型尝试：{view.attempts.join('、')}</p>
      )}
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
      <p className="publish-draft-sections">
        本次词库：{plan.sections.map((section) => section.title).join(' / ') || '(无分组)'}
        {plan.totalQuota ? ` · 平台总配额 ${plan.totalQuota}` : ''}
        {plan.formReused ? ' · 复用了上一趟表单' : ' · 重填了表单'}
      </p>
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
  const [cursor, setCursor] = useState<{ done: number; total: number; jobName: string } | null>(null)
  const [publishArmed, setPublishArmed] = useState(false)
  const [draftBusy, setDraftBusy] = useState('')
  // 停止只影响"要不要开始下一个职位"。已经派出去的那条命令照常收束——半路
  // 掐掉一条在途的 effectful 命令只会制造一个结果未知的 suspect。
  const stopRef = useRef(false)

  const patch = (jobId: string, next: Partial<RowState>) => {
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

  // 阶段 A 的一个职位：定类别 → 读词库选词。零对外副作用，可以随便重跑。
  const planOne = async (jobId: string, jobName: string): Promise<void> => {
    if (!account) return
    patch(jobId, { error: undefined, diagnostics: undefined, skipped: false })
    let jobClass = ''
    try {
      // keepForm=true：把填好三项的表单留给下一趟，省掉一次填表与失焦等待。
      const classView = await api.jobPublishClassCandidates(
        account.platform, account.accountRef, jobId, true,
      )
      jobClass = classView.jobClass
      patch(jobId, { classView, classPick: undefined, plan: undefined })
    } catch (reason) {
      failRow(jobId, `定「${jobName}」的职位类别未成功`, reason, { skipped: true, selected: false })
      return
    }
    try {
      const plan = await api.jobPublishKeywordPlan(
        account.platform, account.accountRef, jobId, jobClass,
      )
      patch(jobId, { plan, selected: true })
    } catch (reason) {
      failRow(jobId, `给「${jobName}」选关键词未成功`, reason, { skipped: true, selected: false })
    }
  }

  // 类别被人工改选后重选关键词：只跑第二趟，类别不动。
  const replanKeywords = async (jobId: string, jobName: string): Promise<void> => {
    if (!account) return
    const jobClass = effectiveClass(jobId)
    if (!jobClass) return
    setBusyJob(jobId)
    patch(jobId, { error: undefined, diagnostics: undefined })
    try {
      const plan = await api.jobPublishKeywordPlan(
        account.platform, account.accountRef, jobId, jobClass,
      )
      patch(jobId, { plan, selected: true, skipped: false })
    } catch (reason) {
      failRow(jobId, `给「${jobName}」重选关键词未成功`, reason)
    } finally {
      setBusyJob('')
    }
  }

  const runPhaseA = async (): Promise<void> => {
    if (!account) return
    stopRef.current = false
    setPhase('planning')
    setPublishArmed(false)
    try {
      for (const [index, row] of readyRows.entries()) {
        if (stopRef.current) break
        setCursor({ done: index, total: readyRows.length, jobName: row.jobName })
        await planOne(row.jobId, row.jobName)
      }
    } finally {
      setCursor(null)
      setPhase('idle')
    }
  }

  const publishOne = async (jobId: string, jobName: string): Promise<void> => {
    if (!account) return
    const jobClass = effectiveClass(jobId)
    const keywords = rows[jobId]?.plan?.keywords ?? []
    if (!jobClass || keywords.length === 0) return
    patch(jobId, { error: undefined, diagnostics: undefined })
    try {
      const result = await api.jobPublishPublish(
        account.platform, account.accountRef, jobId, jobClass, keywords,
      )
      patch(jobId, { publishResult: result })
    } catch (reason) {
      // 甲方 2026-07-31 裁决：单条干净失败或 suspect 跳过当前、继续下一个。
      // suspect 的意图按账本纪律永久冻结等人裁决，绝不在本批内重试。
      failRow(jobId, `发布「${jobName}」未成功`, reason)
    }
  }

  const runPhaseB = async (): Promise<void> => {
    if (!account) return
    if (!publishArmed) {
      setPublishArmed(true)
      return
    }
    setPublishArmed(false)
    stopRef.current = false
    setPhase('publishing')
    const targets = readyRows.filter((row) => rows[row.jobId]?.selected && decided(row.jobId))
    try {
      for (const [index, row] of targets.entries()) {
        if (stopRef.current) break
        setCursor({ done: index, total: targets.length, jobName: row.jobName })
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
      patch(jobId, { draft: result.report })
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
  const skippedCount = readyRows.filter((row) => rows[row.jobId]?.skipped).length
  const publishedCount = readyRows.filter((row) => rows[row.jobId]?.publishResult).length

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
          title="逐个职位定类别、读词库、选关键词。全程零对外副作用，跑砸了随时重来"
          onClick={() => void runPhaseA()}
        >
          {phase === 'planning' ? '正在选…' : `阶段 A：定类别与关键词（${readyRows.length} 个）`}
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
          <button type="button" className="danger-button" onClick={() => { stopRef.current = true }}>
            跑完当前这个就停
          </button>
        )}
        {cursor && (
          <small className="publish-loop-cursor">
            {phase === 'publishing' ? '正在发布' : '正在处理'} {cursor.done + 1}/{cursor.total}：
            {cursor.jobName}
          </small>
        )}
        {!running && (selectedCount > 0 || skippedCount > 0 || publishedCount > 0) && (
          <small className="publish-loop-cursor">
            已选定 {selectable.length} 个 · 勾选 {selectedCount} 个
            {skippedCount > 0 && ` · 跳过 ${skippedCount} 个`}
            {publishedCount > 0 && ` · 已发 ${publishedCount} 个`}
          </small>
        )}
      </div>

      <ul className="publish-precheck-list">
        {[...readyRows, ...otherRows].map((row) => {
          const state = rows[row.jobId] ?? {}
          const stale = planStale(row.jobId)
          return (
            <li key={row.jobId} className={`is-${row.verdict}`}>
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
                <span className="publish-precheck-verdict">
                  {PUBLISH_VERDICT_LABEL[row.verdict] || row.verdict}
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
                <DraftDiagnosticsBlock diagnostics={state.diagnostics} />
              )}
              {state.classView && (
                <JobClassBlock
                  view={state.classView}
                  effective={effectiveClass(row.jobId)}
                  onPick={(name) => patch(row.jobId, { classPick: name })}
                />
              )}
              {state.plan && <KeywordPlanBlock plan={state.plan} stale={stale} />}
              {state.publishResult && <PublishResultBlock result={state.publishResult} />}
              {state.draft && <DraftReportBlock report={state.draft} />}
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
