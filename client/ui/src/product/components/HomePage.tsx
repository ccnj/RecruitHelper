import type { ProductActions, ProductData, ProductMetric } from '../types'
import { ProductIcon } from './ProductIcon'
import { EmptyState, MetricValue, StatusPill } from './ProductPrimitives'

interface HomePageProps {
  customer: ProductData['customer']
  overview: ProductData['overview']
  actions: ProductActions
  onOpenConfirmation: () => void
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

export function HomePage({ customer, overview, actions, onOpenConfirmation }: HomePageProps) {
  const { workflow } = overview
  const startFullReason = controlDisabledReason(
    actions.startWorkflow ? () => actions.startWorkflow?.('full') : undefined,
    workflow.canStart && customer.job.backendJobId !== null,
    workflow.unavailableReason ??
      (customer.job.backendJobId === null ? '同步并绑定职位后可开始今日任务' : null),
  )
  const startReplyReason = controlDisabledReason(
    actions.startWorkflow ? () => actions.startWorkflow?.('replyOnly') : undefined,
    workflow.canStart,
    workflow.unavailableReason,
  )
  const pauseReason = controlDisabledReason(actions.pauseWorkflow, workflow.canPause, workflow.unavailableReason)
  const resumeReason = controlDisabledReason(actions.resumeWorkflow, workflow.canResume, workflow.unavailableReason)
  const additionalBatchReason = controlDisabledReason(
    actions.startWorkflow ? () => actions.startWorkflow?.('full') : undefined,
    workflow.canAddBatch && customer.job.backendJobId !== null,
    workflow.unavailableReason ??
      (customer.job.backendJobId === null ? '同步并绑定职位后可再次采集' : null),
  )
  const confirmationCount = overview.funnel.stages.find((stage) => stage.key === 'confirm')?.target ?? 0
  const taskPosition = workflow.state === 'running' && overview.funnel.stage === 'completed'
    ? '本批候选人已经处理完成，正在继续回复候选人消息。'
    : workflow.state === 'idle'
      ? '点击开始后，系统会自动采集、评分并生成招呼语。'
      : workflow.positionLabel ?? workflow.unavailableReason ?? '今日任务准备就绪。'

  return (
    <div className="rh-page rh-home-page">
      <section className="rh-home-welcome">
        <div>
          <span className="rh-kicker">招聘工作台</span>
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
          <span>{customer.job.environment}</span>
          <span>{customer.job.lastSyncedAt ? `同步于 ${customer.job.lastSyncedAt}` : '尚无同步记录'}</span>
        </div>
      </section>

      <div className="rh-home-primary-grid">
        <section className="rh-panel rh-task-card">
          <div className="rh-panel-heading">
            <div>
              <span className="rh-section-label">今日任务</span>
              <h2>{workflow.stateLabel}</h2>
            </div>
            <StatusPill
              label={workflow.mode === 'full' ? '自动招聘' : workflow.mode === 'replyOnly' ? '只处理消息' : '等待开始'}
              tone={workflow.state === 'running' || workflow.state === 'awaitingConfirmation' ? 'green' : 'slate'}
            />
          </div>
          <p className="rh-task-position">
            {taskPosition}
          </p>
          <div className="rh-task-actions">
            {(workflow.state === 'idle' || workflow.state === 'failed') && (
              <>
                <button
                  className="rh-button is-primary"
                  disabled={startFullReason !== null}
                  onClick={() => void actions.startWorkflow?.('full')}
                  title={startFullReason ?? undefined}
                  type="button"
                >
                  <ProductIcon name="play" size={17} />
                  {workflow.state === 'failed' ? '重新开始今日任务' : '开始今日任务'}
                </button>
                <button
                  className="rh-button is-quiet"
                  disabled={startReplyReason !== null}
                  onClick={() => void actions.startWorkflow?.('replyOnly')}
                  title={startReplyReason ?? undefined}
                  type="button"
                >
                  只处理新消息
                </button>
              </>
            )}
            {(workflow.state === 'paused' || workflow.state === 'waitingDailyWindow') && (
              <>
                <button
                  className="rh-button is-primary"
                  disabled={resumeReason !== null}
                  onClick={() => void actions.resumeWorkflow?.()}
                  title={resumeReason ?? undefined}
                  type="button"
                >
                  <ProductIcon name="play" size={17} />
                  继续今日任务
                </button>
                {workflow.state === 'paused' && workflow.canAddBatch && (
                  <button
                    className="rh-button is-quiet"
                    disabled={additionalBatchReason !== null}
                    onClick={() => void actions.startWorkflow?.('full')}
                    title={additionalBatchReason ?? undefined}
                    type="button"
                  >
                    再采一批（30 人）
                  </button>
                )}
              </>
            )}
            {workflow.state === 'awaitingConfirmation' && (
              <>
                <button className="rh-button is-primary" onClick={onOpenConfirmation} type="button">
                  去确认候选人
                  <ProductIcon name="chevron" size={16} />
                </button>
                <button
                  className="rh-button is-quiet"
                  disabled={pauseReason !== null}
                  onClick={() => void actions.pauseWorkflow?.()}
                  title={pauseReason ?? undefined}
                  type="button"
                >
                  暂停
                </button>
              </>
            )}
            {workflow.state === 'running' && (
              <>
                <button
                  className="rh-button is-primary"
                  disabled={pauseReason !== null}
                  onClick={() => void actions.pauseWorkflow?.()}
                  title={pauseReason ?? undefined}
                  type="button"
                >
                  <ProductIcon name="pause" size={17} />
                  暂停
                </button>
                {workflow.canAddBatch && (
                  <button
                    className="rh-button is-quiet"
                    disabled={additionalBatchReason !== null}
                    onClick={() => void actions.startWorkflow?.('full')}
                    title={additionalBatchReason ?? undefined}
                    type="button"
                  >
                    再采一批（30 人）
                  </button>
                )}
              </>
            )}
          </div>
          {(startFullReason || startReplyReason) &&
            (workflow.state === 'idle' || workflow.state === 'failed') && (
            <div className="rh-inline-note"><ProductIcon name="warning" size={15} />{startFullReason}</div>
          )}
        </section>

        <section className="rh-panel rh-communication-card">
          <div className="rh-panel-heading">
            <div>
              <span className="rh-section-label">沟通引擎</span>
              <h2>{overview.communication.stateLabel}</h2>
            </div>
            <span className={`rh-engine-pulse is-${overview.communication.state}`} />
          </div>
          <p>持续巡检已建立的候选人会话，与候选漏斗相互独立。</p>
          <dl className="rh-compact-facts">
            <div><dt>最近巡检</dt><dd>{overview.communication.lastPatrolAt ?? '尚无巡检记录'}</dd></div>
            <div><dt>运行方式</dt><dd>确定性状态机</dd></div>
          </dl>
        </section>
      </div>

      <section className="rh-panel rh-pipeline-panel">
        <div className="rh-panel-heading">
          <div>
            <span className="rh-section-label">流程进度</span>
            <h2>{overview.funnel.stateLabel}</h2>
          </div>
          <div className="rh-pipeline-summary">
            <span>目标 <strong>{overview.funnel.target ?? '—'}</strong></span>
            <span>待处理 <strong>{overview.funnel.pending ?? '—'}</strong></span>
            <span>失败 <strong>{overview.funnel.failed ?? '—'}</strong></span>
          </div>
        </div>
        <div className="rh-pipeline-track">
          {overview.funnel.stages.map((stage, index) => (
            <div className={`rh-pipeline-stage is-${stage.state}`} key={stage.key}>
              <div className="rh-stage-line" />
              <div className="rh-stage-node">
                {stage.state === 'complete' ? <ProductIcon name="check" size={14} /> : index + 1}
              </div>
              <strong>{stage.label}</strong>
              <span>
                {stage.target === null ? '尚未开始' : `${stage.completed} / ${stage.target}`}
                {stage.failed > 0 ? `，失败 ${stage.failed}` : ''}
              </span>
            </div>
          ))}
        </div>
        {overview.funnel.latestFailure && (
          <div className="rh-inline-note is-danger">
            <ProductIcon name="warning" size={15} />
            {overview.funnel.latestFailure}
          </div>
        )}
        {workflow.state === 'awaitingConfirmation' && confirmationCount > 0 && (
          <div className="rh-confirmation-callout">
            <div>
              <strong>{confirmationCount} 位候选人的招呼语等待确认</strong>
              <span>系统不会自动发送，请在候选确认页全选后发送。</span>
            </div>
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
            <div><dt>新约面</dt><dd>{metricText(overview.todayActivity.newInterviews)}</dd></div>
            <div><dt>完成面试</dt><dd>{metricText(overview.todayActivity.completedInterviews)}</dd></div>
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
