// 职位与发布：后台启用职位总览 + 预检 + 三步发布（定类别 / 试填 / 真发）。
// 这一页是唯一会对平台产生不可逆副作用的入口，所以从模型配置里单拆出来。
import { useCallback, useEffect, useState } from 'react'
import {
  api, AccountView, BackendJobView, DetailedError, JobClassResolveView, JobDraftReport,
  JobPublishPrecheckView, JobPublishResult, PublishParamsState, PublishVerdict,
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

// 试填失败现场。按固定顺序摊平成人读的行，未知键一律兜底显示，
// 避免手侧以后加了字段界面这边静默丢掉。
const DIAG_LABELS: Array<[string, string]> = [
  ['step', '卡在'],
  ['reason', '原因'],
  ['platformHints', '平台提示'],
  ['descriptionLength', '描述已填字数'],
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
// 点击提交控件，且回读后必须离开表单。
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
      {keywords.sectionTitles.length > 0 && (
        <p className="publish-draft-sections">本次分组：{keywords.sectionTitles.join(' / ')}</p>
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

// 类别决定面板。候选是平台针对这个职位现给的封闭集合，点一个即可覆盖脑的选择——
// 大模型只是建议者，最后按哪个发由人定。释义是平台自己的定义，也是判断贴合度的
// 依据，所以必须显示出来而不是只列名字。
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
          {view.source === 'configuredExactMatch' ? '后台配置值精确命中' : '大模型选定'}
          {view.confidence !== undefined && ` · 置信度 ${view.confidence.toFixed(2)}`}
          {overridden && ' · 已被人工改选'}
        </strong>
      </p>
      {view.configuredClass && (
        <p>
          <span>后台填的是</span>
          <strong>
            {view.configuredClass}
            {view.source === 'model' && ' —— 不在平台候选里，未生效'}
          </strong>
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

// 预检结论面板：呈现结论、试填诊断与发布入口。发布按钮需要连点两次，
// 因为它是这条链上唯一不可逆的动作。
function PublishPrecheckPanel({
  view, at, account,
}: { view: JobPublishPrecheckView; at: string; account: AccountView | null }) {
  const [drafts, setDrafts] = useState<Record<string, JobDraftReport>>({})
  const [draftBusy, setDraftBusy] = useState('')
  const [draftError, setDraftError] = useState<Record<string, string>>({})
  const [draftDiag, setDraftDiag] = useState<Record<string, Record<string, unknown>>>({})
  const [publishBusy, setPublishBusy] = useState('')
  const [publishArmed, setPublishArmed] = useState('')
  const [publishResult, setPublishResult] = useState<Record<string, JobPublishResult>>({})
  const [classes, setClasses] = useState<Record<string, JobClassResolveView>>({})
  const [classPick, setClassPick] = useState<Record<string, string>>({})
  const [classBusy, setClassBusy] = useState('')

  // 生效类别：默认脑定的那个，人工改选后用改选值。
  const effectiveClass = (jobId: string): string =>
    classPick[jobId] || classes[jobId]?.jobClass || ''

  // 第一趟：读平台候选并定类别。零对外副作用，可以随便重跑。
  const resolveClass = async (jobId: string, jobName: string) => {
    if (!account) return
    setClassBusy(jobId)
    setDraftError((prev) => ({ ...prev, [jobId]: '' }))
    setDraftDiag((prev) => ({ ...prev, [jobId]: {} }))
    try {
      const view = await api.jobPublishClassCandidates(account.platform, account.accountRef, jobId)
      setClasses((prev) => ({ ...prev, [jobId]: view }))
      setClassPick((prev) => ({ ...prev, [jobId]: '' }))
    } catch (reason) {
      setDraftError((prev) => ({
        ...prev, [jobId]: `定「${jobName}」的职位类别未成功：${errorText(reason)}`,
      }))
      if (reason instanceof DetailedError && reason.diagnostics) {
        setDraftDiag((prev) => ({ ...prev, [jobId]: reason.diagnostics as Record<string, unknown> }))
      }
    } finally {
      setClassBusy('')
    }
  }

  // 发布是唯一不可逆的动作：第一次点只解锁按钮，第二次点才真发。
  const publish = async (jobId: string, jobName: string) => {
    if (!account) return
    if (publishArmed !== jobId) {
      setPublishArmed(jobId)
      return
    }
    const jobClass = effectiveClass(jobId)
    if (!jobClass) return
    setPublishArmed('')
    setPublishBusy(jobId)
    setDraftError((prev) => ({ ...prev, [jobId]: '' }))
    setDraftDiag((prev) => ({ ...prev, [jobId]: {} }))
    try {
      const result = await api.jobPublishPublish(account.platform, account.accountRef, jobId, jobClass)
      setPublishResult((prev) => ({ ...prev, [jobId]: result }))
    } catch (reason) {
      setDraftError((prev) => ({ ...prev, [jobId]: `发布「${jobName}」未成功：${errorText(reason)}` }))
      if (reason instanceof DetailedError && reason.diagnostics) {
        setDraftDiag((prev) => ({ ...prev, [jobId]: reason.diagnostics as Record<string, unknown> }))
      }
    } finally {
      setPublishBusy('')
    }
  }

  const tryDraft = async (jobId: string) => {
    if (!account) return
    const jobClass = effectiveClass(jobId)
    if (!jobClass) return
    setDraftBusy(jobId)
    setDraftError((prev) => ({ ...prev, [jobId]: '' }))
    setDraftDiag((prev) => ({ ...prev, [jobId]: {} }))
    try {
      const result = await api.jobPublishPrepareDraft(account.platform, account.accountRef, jobId, jobClass)
      setDrafts((prev) => ({ ...prev, [jobId]: result.report }))
    } catch (reason) {
      setDraftError((prev) => ({ ...prev, [jobId]: errorText(reason) }))
      // 失败现场快照：卡在哪一步、当时的分组与词库长什么样。没有它就只能靠
      // 反复重跑去猜，而分组和词库每次都随职位类别变化、事后无从复原。
      if (reason instanceof DetailedError && reason.diagnostics) {
        setDraftDiag((prev) => ({ ...prev, [jobId]: reason.diagnostics as Record<string, unknown> }))
      }
    } finally {
      setDraftBusy('')
    }
  }

  // 三步共用一条忙碌闸:同一时刻只允许一条链路占用页面。
  const busy = classBusy !== '' || draftBusy !== '' || publishBusy !== ''
  const ready = view.rows.filter((row) => row.verdict === 'ready')
  const others = view.rows.filter((row) => row.verdict !== 'ready')
  return (
    <div className="publish-precheck">
      <div className="publish-precheck-head">
        <strong>预检结论</strong>
        <small>
          可发布 {ready.length} 个 · 平台现存 {view.platformPostingCount} 个职位
          {at && ` · 预检于 ${at}`}
        </small>
      </div>
      <ul className="publish-precheck-list">
        {[...ready, ...others].map((row) => (
          <li key={row.jobId} className={`is-${row.verdict}`}>
            <div className="publish-precheck-row">
              <span className="publish-precheck-verdict">{PUBLISH_VERDICT_LABEL[row.verdict] || row.verdict}</span>
              <strong>{row.jobName || '未命名职位'}</strong>
              {row.isCurrent && <em className="backend-jobs-current">当前职位</em>}
              <code>#{row.jobId}</code>
              {row.verdict === 'ready' && (
                <button
                  type="button"
                  disabled={busy || !account}
                  title="读平台给这个职位的类别候选并定下用哪个；不会填其余字段、不会发布"
                  onClick={() => void resolveClass(row.jobId, row.jobName)}
                >
                  {classBusy === row.jobId
                    ? '正在读候选…'
                    : classes[row.jobId] ? '重新定类别' : '① 定职位类别'}
                </button>
              )}
              {row.verdict === 'ready' && (
                <button
                  type="button"
                  disabled={busy || !account || !effectiveClass(row.jobId)}
                  title={effectiveClass(row.jobId)
                    ? '在发布页试填一次并回读，不会点击发布'
                    : '先定职位类别：关键词弹层要等类别定下才打得开'}
                  onClick={() => void tryDraft(row.jobId)}
                >
                  {draftBusy === row.jobId ? '正在试填…' : '② 试填一次'}
                </button>
              )}
              {row.verdict === 'ready' && !publishResult[row.jobId] && (
                <button
                  type="button"
                  className={publishArmed === row.jobId ? 'danger-button' : undefined}
                  disabled={busy || !account || !effectiveClass(row.jobId)}
                  title={effectiveClass(row.jobId)
                    ? '真正发布到平台，求职者立刻可见；不可撤销，只能到平台手动下架'
                    : '先定职位类别'}
                  onClick={() => void publish(row.jobId, row.jobName)}
                >
                  {publishBusy === row.jobId
                    ? '正在发布…'
                    : publishArmed === row.jobId
                      ? `确认以「${effectiveClass(row.jobId)}」发布？再点一次`
                      : '③ 发布到平台'}
                </button>
              )}
            </div>
            {draftError[row.jobId] && (
              <p className="publish-precheck-issue">{draftError[row.jobId]}</p>
            )}
            {draftDiag[row.jobId] && Object.keys(draftDiag[row.jobId]).length > 0 && (
              <DraftDiagnosticsBlock diagnostics={draftDiag[row.jobId]} />
            )}
            {classes[row.jobId] && (
              <JobClassBlock
                view={classes[row.jobId]}
                effective={effectiveClass(row.jobId)}
                onPick={(name) => setClassPick((prev) => ({ ...prev, [row.jobId]: name }))}
              />
            )}
            {publishResult[row.jobId] && <PublishResultBlock result={publishResult[row.jobId]} />}
            {drafts[row.jobId] && <DraftReportBlock report={drafts[row.jobId]} />}
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
        ))}
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
              <th scope="col">操作</th>
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
                <td>
                  {job.publishParams === 'present' ? (
                    <button type="button" disabled title="发布能力尚未实现">发布到智联</button>
                  ) : (
                    <span className="backend-jobs-none">—</span>
                  )}
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
