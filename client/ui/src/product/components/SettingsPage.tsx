import type { ProductData } from '../types'
import { PageHeader, StatusPill } from './ProductPrimitives'
import { ProductIcon } from './ProductIcon'
import { InterviewSchedulePanel } from './InterviewSchedulePanel'
import { AutoStartPanel } from './AutoStartPanel'

interface SettingsPageProps {
  customer: ProductData['customer']
}

export function SettingsPage({ customer }: SettingsPageProps) {
  return (
    <div className="rh-page">
      <PageHeader
        title="配置"
        description="设置每日自动开始与可面试时段,查看客户授权与职位配置。敏感配置不会在普通页面显示。"
      />

      <AutoStartPanel />

      <InterviewSchedulePanel />

      <section className="rh-panel rh-settings-identity">
        <div className="rh-settings-avatar">{customer.shortName || '客'}</div>
        <div>
          <span className="rh-section-label">当前客户</span>
          <h2>{customer.name}</h2>
          <p>{customer.authorizationLabel}</p>
        </div>
        <div className="rh-settings-job">
          <ProductIcon name="briefcase" size={20} />
          <div>
            <span>当前绑定职位</span>
            <strong>{customer.job.name ?? '尚未绑定职位'}</strong>
          </div>
          <StatusPill
            label={customer.job.syncStateLabel}
            tone={customer.job.syncState === 'synced' ? 'green' : customer.job.syncState === 'stale' ? 'amber' : 'slate'}
          />
        </div>
      </section>

    </div>
  )
}

