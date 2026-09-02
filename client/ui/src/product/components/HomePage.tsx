import type { DailyPlanEntry, DailyPlanView } from '../api'
import type { PlatformChoice, ProductActions, ProductData, ProductMetric } from '../types'
import { ProductIcon } from './ProductIcon'
import { EmptyState, MetricValue, StatusPill } from './ProductPrimitives'

interface HomePageProps {
  customer: ProductData['customer']
  overview: ProductData['overview']
  actions: ProductActions
  onOpenConfirmation: () => void
  dailyPlan?: DailyPlanView | null
  platformChoice?: PlatformChoice | null
}

// 平台 id 是脑手契约的公开路由键,界面只做展示映射;未知 id 原样显示,不猜。
const PLATFORM_LABELS: Record<string, string> = {
  zhilian: '智联招聘',
  boss: 'BOSS 直聘',
}

export function platformLabel(platform: string): string {
  return PLATFORM_LABELS[platform] ?? platform
}

function controlDisabledReason(
  callback: (() => void | Promise<void>) | undefined,
  allowed: boolean,
  businessReason: string | null,
): string | null {
  if (!allowed) return businessReason ?? '当前状态不可执行此操作'
  if (!callback) return '运行控制尚未接入'
  return null
}

export const END_WORKFLOW_CONFIRMATION =
  '结束后不能继续本次任务；已有数据和已发送消息不会删除。'

export function confirmEndWorkflow(
  confirm: (message: string) => boolean,
  endWorkflow: () => void | Promise<void>,
): void | Promise<void> {
  if (!confirm(END_WORKFLOW_CONFIRMATION)) return
  return endWorkflow()
}

// 跳过原因形如「batch:<原因码>|<判定现场>」:竖线前按码归类成中文,竖线后是
// 批次留痕的原话(错误码/原因/手报文本),原样附在后面供排障——UI 不吞证据。
export function planSkipReasonParts(reason: string | undefined): { label: string; detail: string } {
  if (!reason) return { label: '', detail: '' }
  const bar = reason.indexOf('|')
  const code = bar >= 0 ? reason.slice(0, bar) : reason
  const detail = bar >= 0 ? reason.slice(bar + 1).trim() : ''
  return { label: planSkipCodeText(code), detail }
}

function planSkipCodeText(code: string): string {
  if (code === 'zeroQuota') return '配额不足，今日轮空'
  if (code.startsWith('jobNotOnlineAtPlan:')) return `职位未在线（${code.split(':')[1] ?? ''}）`
  if (code === 'batch:jobNotOnline') return '开批时职位已下线'
  if (code === 'batch:jobStatusReadFailed') return '职位状态读取失败'
  if (code === 'batch:positionSelectFailed') return '推荐页职位列表里找不到该职位或无法唯一确定'
  if (code === 'batch:recommendPageNotReady') return '推荐页加载超时（已重试一次）'
  return code
}

function planSkipReasonText(reason: string | undefined): string {
  const { label, detail } = planSkipReasonParts(reason)
  return detail ? `${label} · ${detail}` : label
}

export function HomePage({ customer, overview, actions, onOpenConfirmation, dailyPlan, platformChoice = null }: HomePageProps) {
  const { workflow } = overview
  const pendingEnd = workflow.pendingAction === 'end'
  const pendingSourcing = workflow.pendingAction === 'sourcing'
  const pendingEndReason = pendingEnd ? '正在结束当前候选人，请稍候' : null
  const pendingSourcingReason = pendingSourcing ? '当前候选人处理完后会开始新一批' : null
  // 当日职位计划(2026-09-01):开始不再要求绑定单一职位,职位名单由脑按
  // 后台有效职位与平台在线状态自行定稿。
  const startFullReason = controlDisabledReason(
    actions.startWorkflow ? () => actions.startWorkflow?.('full') : undefined,
    workflow.canStart && !pendingEnd,
    pendingEndReason ?? workflow.unavailableReason,
  )
  const startReplyReason = controlDisabledReason(
    actions.startWorkflow ? () => actions.startWorkflow?.('replyOnly') : undefined,
    workflow.canStart && !pendingEnd,
    pendingEndReason ?? workflow.unavailableReason,
  )
  const resumeReason = controlDisabledReason(
    actions.resumeWorkflow,
    workflow.canResume && !pendingEnd,
    pendingEndReason ?? workflow.unavailableReason,
  )
  // 暂停与"再采一批"2026-07-31 从客户界面撤下:运行中唯一的刹车是结束(脑侧
  // 已放开漏斗阶段结束),再采一批结束后重新开始即可。两者的脑侧能力都还在。
  const endReason = controlDisabledReason(
    actions.endWorkflow,
    workflow.canEnd && !pendingEnd && !pendingSourcing,
    pendingEndReason ?? pendingSourcingReason ?? workflow.unavailableReason,
  )

  // 今日职位计划的派生量:分摊条按份额分段、填充按实发;「进行中」取计划
  // 活跃时的首个待进行条目(串行执行,首个 pending 即当前批)。
  const planEntries = dailyPlan?.available ? dailyPlan.entries ?? [] : []
  const planActiveEntries = planEntries.filter(
    (entry) => entry.status !== 'skipped' && entry.quota > 0,
  )
  const planQuotaSum = planActiveEntries.reduce((sum, entry) => sum + entry.quota, 0)
  const planSentSum = planEntries.reduce((sum, entry) => sum + entry.sentCount, 0)
  const planSkippedCount = planEntries.filter((entry) => entry.status === 'skipped').length
  const planRunningSeq = dailyPlan?.status === 'active'
    ? planEntries.find((entry) => entry.status === 'pending')?.seq ?? null
    : null
  const planPercent = (entry: DailyPlanEntry) => (entry.quota > 0
    ? Math.min(100, Math.round((entry.sentCount / entry.quota) * 100))
    : 0)

  return (
    <div className="rh-page rh-home-page">
      <section className="rh-home-welcome">
        <div>
          <h1>欢迎回来，{customer.name}</h1>
          <p>{overview.dateLabel}</p>
        </div>
        <div className={`rh-business-window${overview.businessWindowOpen ? ' is-open' : ''}`}>
          <ProductIcon name="clock" size={18} />
          <div>
            <strong>{overview.businessWindowOpen ? '当前可运行' : '当前只读'}</strong>
            <span>{overview.businessWindowLabel}</span>
          </div>
        </div>
      </section>

      <section className="rh-job-strip">
        <div className="rh-job-mark"><ProductIcon name="briefcase" size={20} /></div>
        <div className="rh-job-copy">
          <span>当前绑定职位</span>
          <strong>{customer.job.name ?? '尚未绑定职位'}</strong>
        </div>
        <StatusPill
          label={customer.job.syncStateLabel}
          tone={customer.job.syncState === 'synced' ? 'green' : customer.job.syncState === 'stale' ? 'amber' : 'slate'}
        />
        <div className="rh-job-meta">
          <span>{customer.job.lastSyncedAt ? `同步于 ${customer.job.lastSyncedAt}` : '尚无同步记录'}</span>
        </div>
        <button
          className="rh-button is-quiet"
          disabled={!actions.syncJobs}
          onClick={() => void actions.syncJobs?.()}
          title={
            actions.syncJobs
              ? '重新读取后台职位：更新当前绑定职位，并让主动来聊的候选人能匹配到后台在招的其他职位'
              : '运行控制尚未接入'
          }
          type="button"
        >
          同步职位
        </button>
      </section>

      {planEntries.length > 0 && dailyPlan && (
        <section className="rh-panel rh-daily-plan">
          <header className="rh-daily-plan-head">
            <div className="rh-daily-plan-title">
              <h2>今日职位计划</h2>
              <span>
                {dailyPlan.localDate ?? ''}
                {dailyPlan.status === 'draft' && ' · 名单待平台确认'}
                {dailyPlan.status === 'aborted' && ' · 已终止'}
                {dailyPlan.status === 'completed' && ' · 已完成'}
                {planSkippedCount > 0 && ` · ${planSkippedCount} 个职位今日停发`}
              </span>
            </div>
            <div className="rh-daily-plan-total">
              <strong>{planSentSum}</strong>
              <span>/ {planQuotaSum > 0 ? planQuotaSum : dailyPlan.totalQuota ?? 0} 个招呼</span>
            </div>
          </header>
          {planQuotaSum > 0 && (
            <div className="rh-daily-plan-bar" aria-hidden="true">
              {planActiveEntries.map((entry) => (
                <span
                  key={entry.seq}
                  className={`rh-daily-plan-seg${entry.status === 'done' ? ' is-done' : ''}${entry.seq === planRunningSeq ? ' is-active' : ''}`}
                  style={{ flexGrow: entry.quota }}
                  title={`${entry.jobName} ${entry.sentCount}/${entry.quota}`}
                >
                  <span
                    className="rh-daily-plan-seg-fill"
                    style={{ width: `${planPercent(entry)}%` }}
                  />
                </span>
              ))}
            </div>
          )}
          <ul className="rh-daily-plan-list">
            {planEntries.map((entry) => {
              const isRunning = entry.seq === planRunningSeq
              const tone = entry.status === 'done'
                ? 'green'
                : entry.status === 'skipped' ? 'amber' : isRunning ? 'blue' : 'slate'
              const label = entry.status === 'done'
                ? '已完成'
                : entry.status === 'skipped' ? '已跳过' : isRunning ? '进行中' : '待进行'
              return (
                <li
                  className={`rh-daily-plan-row is-${entry.status}${isRunning ? ' is-running' : ''}`}
                  key={entry.seq}
                >
                  <span aria-hidden="true" className={`rh-daily-plan-marker is-${tone}`}>
                    {entry.status === 'done' ? (
                      <ProductIcon name="check" size={12} />
                    ) : entry.status === 'skipped' ? (
                      <ProductIcon name="pause" size={11} />
                    ) : isRunning ? (
                      <i className="rh-daily-plan-pulse" />
                    ) : (
                      entry.seq
                    )}
                  </span>
                  <div className="rh-daily-plan-job">
                    <strong>{entry.jobName}</strong>
                    {entry.status === 'skipped' ? (
                      <span className="rh-daily-plan-note" title={planSkipReasonText(entry.skipReason)}>
                        {planSkipReasonText(entry.skipReason)}
                      </span>
                    ) : entry.suspectCount > 0 ? (
                      <span className="rh-daily-plan-note is-amber">
                        {entry.suspectCount} 条发送结果待人工确认
                      </span>
                    ) : null}
                  </div>
                  <div className="rh-daily-plan-side">
                    {entry.status !== 'skipped' && entry.quota > 0 && (
                      <>
                        <span className="rh-daily-plan-track">
                          <span
                            className="rh-daily-plan-fill"
                            style={{ width: `${planPercent(entry)}%` }}
                          />
                        </span>
                        <span className="rh-daily-plan-count">
                          {entry.sentCount}
                          <i>/{entry.quota}</i>
                        </span>
                      </>
                    )}
                    <StatusPill label={label} tone={tone} />
                  </div>
                </li>
              )
            })}
          </ul>
        </section>
      )}

      <section className={`rh-panel rh-status-card is-${overview.homeStatus.tone}`}>
        <span className="rh-status-rail" aria-hidden="true" />
        <div className="rh-status-head">
          <span className="rh-status-dot" aria-hidden="true" />
          <div className="rh-status-copy">
            <h2>{overview.homeStatus.label}</h2>
            <p>{overview.homeStatus.hint}</p>
          </div>
          <div className="rh-task-actions">
            {(workflow.state === 'idle' || workflow.state === 'failed') && (
              <button
                className="rh-button is-primary"
                disabled={startFullReason !== null}
                onClick={() => void actions.startWorkflow?.('full')}
                title={startFullReason ?? undefined}
                type="button"
              >
                <ProductIcon name="play" size={17} />
                {workflow.state === 'failed' ? '重新开始' : '开始全流程'}
              </button>
            )}
            {(workflow.state === 'idle' || workflow.state === 'failed') && (
              <button
                className="rh-button is-quiet"
                disabled={startReplyReason !== null}
                onClick={() => void actions.startWorkflow?.('replyOnly')}
                title={startReplyReason ?? '不采新人，只回复已经在聊的候选人'}
                type="button"
              >
                只回复消息
              </button>
            )}
            {(workflow.state === 'paused' || workflow.state === 'waitingDailyWindow') && (
              <button
                className="rh-button is-primary"
                disabled={resumeReason !== null}
                onClick={() => void actions.resumeWorkflow?.()}
                title={resumeReason ?? undefined}
                type="button"
              >
                <ProductIcon name="play" size={17} />
                继续
              </button>
            )}
            {workflow.state === 'awaitingConfirmation' && (
              <button className="rh-button is-primary" onClick={onOpenConfirmation} type="button">
                去确认候选人
                <ProductIcon name="chevron" size={16} />
              </button>
            )}
            {workflow.state !== 'idle' && workflow.state !== 'failed' &&
              (workflow.canEnd || workflow.pendingAction !== null) && (
              <button
                className="rh-button is-quiet"
                disabled={endReason !== null}
                onClick={() => {
                  if (actions.endWorkflow) {
                    void confirmEndWorkflow(
                      (message) => window.confirm(message),
                      actions.endWorkflow,
                    )
                  }
                }}
                title={endReason ?? undefined}
                type="button"
              >
                {pendingEnd ? '正在结束…' : '结束'}
              </button>
            )}
          </div>
        </div>
        {startFullReason && (workflow.state === 'idle' || workflow.state === 'failed') && (
          <div className="rh-inline-note"><ProductIcon name="warning" size={15} />{startFullReason}</div>
        )}
        {platformChoice && platformChoice.options.length > 0 &&
          (workflow.state === 'idle' || workflow.state === 'failed') && (
          <div className="rh-inline-note rh-platform-choice">
            <ProductIcon name="warning" size={15} />
            <label htmlFor="rh-platform-select">检测到多个招聘平台已登录，请选择本次要运行的平台：</label>
            <select
              id="rh-platform-select"
              onChange={(event) => platformChoice.onSelect(event.target.value)}
              value={platformChoice.selected ?? ''}
            >
              <option disabled value="">请选择</option>
              {platformChoice.options.map((platform) => (
                <option key={platform} value={platform}>{platformLabel(platform)}</option>
              ))}
            </select>
          </div>
        )}
      </section>

      <section className="rh-dashboard-section">
        <div className="rh-section-heading">
          <div><span className="rh-section-label">今日数据</span><h2>业务快照</h2></div>
          <span>刷新于 {overview.refreshedAt ?? '尚未刷新'}</span>
        </div>
        <div className="rh-metric-grid">
          {overview.todayMetrics.map((metric) => (
            <div className={`rh-metric-card is-${metric.tone}`} key={metric.label}>
              <span>{metric.label}</span>
              <strong><MetricValue value={metric.value} /></strong>
            </div>
          ))}
        </div>
      </section>

      <section className="rh-dashboard-section">
        <div className="rh-section-heading">
          <div><span className="rh-section-label">总账面</span><h2>累计成果</h2></div>
          <span>{overview.ledgerStartedAt ? `自 ${overview.ledgerStartedAt} 开始` : '尚无业务事实'}</span>
        </div>
        <div className="rh-ledger-grid">
          {overview.ledger.map((item) => (
            <div className="rh-ledger-card" key={item.label}>
              <span>{item.label}</span>
              <strong><MetricValue value={item.value} /></strong>
            </div>
          ))}
        </div>
      </section>

      <div className="rh-home-lower-grid">
        <section className="rh-panel">
          <div className="rh-panel-heading">
            <div><span className="rh-section-label">日程</span><h2>今天的面试</h2></div>
            <span className="rh-count-label">{overview.todayInterviews.length} 场</span>
          </div>
          {overview.todayInterviews.length === 0 ? (
            <EmptyState title="今天没有面试" description="已确认的今日面试会显示在这里。" icon="calendar" />
          ) : (
            <div className="rh-interview-list">
              {overview.todayInterviews.map((interview) => (
                <div className="rh-interview-row" key={interview.profileId}>
                  <time>{interview.interviewAt}</time>
                  <div><strong>{interview.displayName}</strong><span>{interview.jobName}</span></div>
                  <span>{interview.method}</span>
                  <StatusPill label={interview.confirmationLabel} tone="green" />
                </div>
              ))}
            </div>
          )}
        </section>

        <section className="rh-panel">
          <div className="rh-panel-heading">
            <div><span className="rh-section-label">今日活动</span><h2>沟通节奏</h2></div>
          </div>
          <div className="rh-activity-primary">
            <div>
              <span>招呼</span>
              <strong>{metricText(overview.todayActivity.greeted)}</strong>
            </div>
            {overview.todayActivity.greetingDisplayTarget !== null && (
              <span>显示目标 {overview.todayActivity.greetingDisplayTarget}</span>
            )}
          </div>
          {overview.todayActivity.greetingDisplayTarget !== null && (
            <div className="rh-activity-track">
              <span style={{ width: activityWidth(overview.todayActivity.greeted, overview.todayActivity.greetingDisplayTarget) }} />
            </div>
          )}
          <dl className="rh-activity-facts">
            <div><dt>新回复</dt><dd>{metricText(overview.todayActivity.newReplies)}</dd></div>
            <div><dt>新换微</dt><dd>{metricText(overview.todayActivity.newWechat)}</dd></div>
            <div><dt>新约面</dt><dd>{metricText(overview.todayActivity.newInterviews)}</dd></div>
          </dl>
        </section>
      </div>
    </div>
  )
}

function metricText(value: ProductMetric): string {
  return value === null ? '—' : value.toLocaleString('zh-CN')
}

function activityWidth(value: ProductMetric, target: number): string {
  if (value === null || target <= 0) return '0%'
  return `${Math.min(100, Math.round((value / target) * 100))}%`
}
