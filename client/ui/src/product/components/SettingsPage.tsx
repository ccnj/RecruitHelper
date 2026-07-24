import type { ProductConnectionView, ProductData } from '../types'
import { PageHeader, StatusPill } from './ProductPrimitives'
import { ProductIcon } from './ProductIcon'

interface SettingsPageProps {
  customer: ProductData['customer']
  connections: ProductConnectionView[]
}

export function SettingsPage({ customer, connections }: SettingsPageProps) {
  return (
    <div className="rh-page">
      <PageHeader
        title="配置"
        description="查看客户授权、职位配置和本机连接状态。敏感配置不会在普通页面显示。"
        meta={<span className="rh-readonly-label">只读配置</span>}
      />

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

      <section className="rh-panel">
        <div className="rh-panel-heading">
          <div><span className="rh-section-label">本机状态</span><h2>连接与配置</h2></div>
          <span className="rh-readonly-label">不显示密钥</span>
        </div>
        <div className="rh-connection-list">
          {connections.map((connection) => (
            <div key={connection.label}>
              <span className={`rh-connection-indicator is-${connection.tone}`} />
              <div><strong>{connection.label}</strong><span>{connection.detail ?? '暂无补充信息'}</span></div>
              <StatusPill label={connection.value} tone={connectionTone(connection.tone)} />
            </div>
          ))}
        </div>
      </section>

      <div className="rh-settings-note">
        <ProductIcon name="warning" size={18} />
        <div>
          <strong>普通配置页不提供开发操作</strong>
          <p>模型密钥、插件重载、账本、suspect 和协议帧保留在开发者诊断入口。</p>
        </div>
      </div>
    </div>
  )
}

function connectionTone(tone: ProductConnectionView['tone']) {
  const tones: Record<ProductConnectionView['tone'], 'slate' | 'green' | 'amber' | 'red'> = {
    neutral: 'slate',
    success: 'green',
    warning: 'amber',
    danger: 'red',
  }
  return tones[tone]
}

