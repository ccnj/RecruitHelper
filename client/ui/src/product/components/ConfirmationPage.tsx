import type {
  ConfirmationBatchView,
  ConfirmationCandidateView,
  CustomerView,
  ProductActions,
} from '../types'
import { CandidateAvatar, EmptyState, MetricValue, PageHeader, StatusPill } from './ProductPrimitives'
import { ProductIcon } from './ProductIcon'

interface ConfirmationPageProps {
  batch: ConfirmationBatchView
  customer: CustomerView
  selectedIds: ReadonlySet<string>
  actions: ProductActions
  onSelectionChange: (ids: Set<string>) => void
  onOpenCandidate: (candidate: ConfirmationCandidateView) => void
}

export function ConfirmationPage({
  batch,
  customer,
  selectedIds,
  actions,
  onSelectionChange,
  onOpenCandidate,
}: ConfirmationPageProps) {
  const selectable = batch.candidates.filter((candidate) => candidate.selectable)
  const selectedEligibleCount = selectable.filter((candidate) => selectedIds.has(candidate.profileId)).length
  const allSelected = batch.ready &&
    selectable.length > 0 &&
    selectedEligibleCount === selectable.length
  const sendProgress = confirmationSendProgress(batch)
  const sendUnavailableReason = confirmationSendUnavailableReason(batch, allSelected, actions)
  const sendHint = sendProgress.started
    ? sendProgress.completed
      ? `本批发送完成：成功 ${sendProgress.sent} 人` +
        `${sendProgress.failed > 0 ? `，异常 ${sendProgress.failed} 人` : ''}` +
        `${sendProgress.settledWithoutSend > 0 ? `，未发出 ${sendProgress.settledWithoutSend} 人` : ''}`
      : `系统正在逐人发送：已成功 ${sendProgress.sent} / ${sendProgress.total} 人，请勿重复点击`
    : sendUnavailableReason

  function selectAll() {
    if (!batch.ready) return
    onSelectionChange(new Set(selectable.map((candidate) => candidate.profileId)))
  }

  function clearSelection() {
    onSelectionChange(new Set())
  }

  function toggleCandidate(profileId: string, checked: boolean) {
    if (!batch.ready) return
    const next = new Set(selectedIds)
    if (checked) next.add(profileId)
    else next.delete(profileId)
    onSelectionChange(next)
  }

  return (
    <div className="rh-page">
      <PageHeader
        title="候选确认"
        description="核对本批 AI 招呼语。只有全选并明确点击发送后，系统才会开始逐人发送。"
        meta={<StatusPill label={customer.job.name ?? '尚未绑定职位'} tone={customer.job.name ? 'blue' : 'slate'} />}
      />

      {!batch.batchId ? (
        <section className="rh-panel">
          <EmptyState
            icon="confirmation"
            title="没有等待确认的候选人"
            description="完整流程完成评分、筛选和招呼语生成后，当前批次会显示在这里。"
          />
        </section>
      ) : (
        <>
          <section className="rh-confirmation-summary">
            <div>
              <span>批次创建</span>
              <strong>{batch.createdAt ?? '—'}</strong>
            </div>
            <div>
              <span>评分完成</span>
              <strong><MetricValue value={batch.scoreCompleted} /></strong>
            </div>
            <div>
              <span>筛选入选</span>
              <strong><MetricValue value={batch.selectedCount} /></strong>
            </div>
            <div>
              <span>招呼语成功</span>
              <strong><MetricValue value={batch.greetingSucceeded} /></strong>
            </div>
            <div>
              <span>生成失败</span>
              <strong><MetricValue value={batch.greetingFailed} /></strong>
            </div>
            <div>
              <span>仍待生成</span>
              <strong><MetricValue value={batch.greetingPending} /></strong>
            </div>
          </section>

          {(batch.workflowPaused || !batch.businessWindowOpen) && (
            <div className="rh-page-alert">
              <ProductIcon name="clock" size={18} />
              <div>
                <strong>{batch.workflowPaused ? '工作流已暂停' : '当前不在业务运行时间'}</strong>
                <span>
                  {batch.workflowPaused
                    ? '仍可核对和选择候选人；恢复运行后才能发送。'
                    : '当前只允许查看；08:00 后仍需手动恢复或开始。'}
                </span>
              </div>
            </div>
          )}

          <section className="rh-panel rh-confirmation-panel">
            <div className="rh-confirmation-toolbar">
              <div>
                <button className="rh-text-button" disabled={!batch.ready || selectable.length === 0} onClick={selectAll} type="button">全选</button>
                <span className="rh-toolbar-divider" />
                <button className="rh-text-button" disabled={selectedIds.size === 0} onClick={clearSelection} type="button">取消全选</button>
                <span className="rh-selection-count">
                  {sendProgress.started
                    ? `已确认 ${sendProgress.total} 人 · 已发送 ${sendProgress.sent} 人`
                    : `已选择 ${selectedEligibleCount} / ${selectable.length}`}
                </span>
              </div>
              <button
                className="rh-button is-primary"
                disabled={sendProgress.started || sendUnavailableReason !== null}
                onClick={() => {
                  if (batch.batchId && allSelected) {
                    void actions.sendConfirmationBatch?.(batch.batchId, selectable.map((candidate) => candidate.profileId))
                  }
                }}
                title={sendHint ?? undefined}
                type="button"
              >
                <ProductIcon name="confirmation" size={17} />
                {sendProgress.completed
                  ? '本批发送完成'
                  : sendProgress.started
                    ? `正在发送 ${sendProgress.sent}/${sendProgress.total}`
                    : '发送所选候选人'}
              </button>
            </div>
            {sendHint && (
              <div className={`rh-confirmation-hint${sendProgress.started ? ' is-progress' : ''}`}>{sendHint}</div>
            )}

            {batch.candidates.length === 0 ? (
              <EmptyState title="本批没有可确认成员" description="请查看生成失败或筛选结果，当前页面不会伪造候选人。" />
            ) : (
              <div className="rh-confirmation-list">
                {batch.candidates.map((candidate) => (
                  <article className={`rh-confirmation-row${candidate.selectable ? '' : ' is-disabled'}`} key={candidate.profileId}>
                    <label className="rh-confirmation-check">
                      <input
                        checked={selectedIds.has(candidate.profileId)}
                        disabled={!batch.ready || !candidate.selectable}
                        onChange={(event) => toggleCandidate(candidate.profileId, event.target.checked)}
                        type="checkbox"
                      />
                      <span className="rh-sr-only">选择 {candidate.displayName}</span>
                    </label>
                    <CandidateAvatar name={candidate.displayName} />
                    <div className="rh-confirmation-person">
                      <button onClick={() => onOpenCandidate(candidate)} type="button">{candidate.displayName}</button>
                      <span>
                        {[candidate.age === null ? null : `${candidate.age} 岁`, candidate.education, candidate.experience]
                          .filter(Boolean)
                          .join(' · ') || '候选人信息待补充'}
                      </span>
                      <span>{candidate.currentRole ?? '当前职位待补充'}</span>
                    </div>
                    <div className="rh-confirmation-score">
                      <span>AI 评分</span>
                      <strong>{candidate.aiScore ?? '—'}</strong>
                    </div>
                    <div className="rh-greeting-copy">
                      <span>AI 招呼语</span>
                      <p>{candidate.greeting ?? '招呼语生成失败，本候选人不可发送。'}</p>
                    </div>
                    <div className="rh-confirmation-state">
                      <StatusPill label={candidate.sendStateLabel} tone={sendStateTone(candidate.sendState)} />
                      <span>{candidate.generationStateLabel}</span>
                    </div>
                  </article>
                ))}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}

// 分母是"本次确认名单的人数"，必须在整批发送过程中单调不减。它一度按
// sendState !== 'ineligible' 算，而 ineligible 把"招呼语没生成"和"招呼语就绪
// 但最终没发出"糊在一起：推荐流一失效，还没铸造发送意图的人从 ready 掉成
// abandoned，就被算进 ineligible、从分母里消失，用户看到已确认人数往下走。
// 现在只把招呼语尚未就绪的排除在外，settledWithoutSend 留在分母里。
function confirmationSendProgress(batch: ConfirmationBatchView) {
  const candidates = batch.candidates.filter((candidate) => candidate.sendState !== 'ineligible')
  const sent = candidates.filter((candidate) => candidate.sendState === 'sent').length
  const sending = candidates.filter((candidate) => candidate.sendState === 'sending').length
  const failed = candidates.filter((candidate) =>
    candidate.sendState === 'failed' || candidate.sendState === 'suspect').length
  const settledWithoutSend = candidates.filter((candidate) =>
    candidate.sendState === 'settledWithoutSend').length
  const started = sent + sending + failed > 0
  const completed = started &&
    candidates.every((candidate) =>
      candidate.sendState === 'sent' ||
      candidate.sendState === 'failed' ||
      candidate.sendState === 'suspect' ||
      candidate.sendState === 'settledWithoutSend')
  return { completed, failed, sent, settledWithoutSend, started, total: candidates.length }
}

function confirmationSendUnavailableReason(
  batch: ConfirmationBatchView,
  allSelected: boolean,
  actions: ProductActions,
): string | null {
  if (batch.workflowPaused) return '工作流暂停期间不能发送，请先在首页恢复'
  if (!batch.businessWindowOpen) return '运行时间为 08:00～24:00'
  if (!batch.ready) return batch.readinessReason ?? '当前批次尚未完成'
  if (!allSelected) return '请先全选本批所有可发送候选人'
  if (!batch.batchId) return '当前没有等待发送的批次'
  if (!actions.sendConfirmationBatch) return '发送入口尚未接入'
  return null
}

function sendStateTone(state: ConfirmationCandidateView['sendState']) {
  const tones: Record<ConfirmationCandidateView['sendState'], 'blue' | 'amber' | 'green' | 'red' | 'slate'> = {
    ready: 'blue',
    sending: 'amber',
    sent: 'green',
    failed: 'red',
    suspect: 'red',
    settledWithoutSend: 'slate',
    ineligible: 'slate',
  }
  return tones[state]
}
