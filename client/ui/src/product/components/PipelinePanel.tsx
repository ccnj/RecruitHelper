import type { FunnelView } from '../types'
import { ProductIcon } from './ProductIcon'

// 流程进度是六个阶段的逐级进度,原来摆在客户首页正中。它是排障视角:客户看到
// "筛选 18/30"只会怀疑是不是出了问题,而这恰恰是正常的漏斗收窄。2026-07-31
// 甲方裁决整块移到诊断台;客户端首页只讲"招呼中/沟通中"两种说法。
export function PipelinePanel({ funnel }: { funnel: FunnelView }) {
  return (
    <section className="rh-panel rh-pipeline-panel">
      <div className="rh-panel-heading">
        <div>
          <span className="rh-section-label">流程进度</span>
          <h2>{funnel.stateLabel}</h2>
        </div>
        <div className="rh-pipeline-summary">
          <span>目标 <strong>{funnel.target ?? '—'}</strong></span>
          <span>待处理 <strong>{funnel.pending ?? '—'}</strong></span>
          <span>失败 <strong>{funnel.failed ?? '—'}</strong></span>
        </div>
      </div>
      <div className="rh-pipeline-track">
        {funnel.stages.map((stage, index) => (
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
      {funnel.latestFailure && (
        <div className="rh-inline-note is-danger">
          <ProductIcon name="warning" size={15} />
          {funnel.latestFailure}
        </div>
      )}
    </section>
  )
}
