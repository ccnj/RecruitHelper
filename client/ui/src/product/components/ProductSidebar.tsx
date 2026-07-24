import type { ProductPage } from '../types'
import { ProductIcon, type ProductIconName } from './ProductIcon'

interface NavItem {
  key: ProductPage
  label: string
  icon: ProductIconName
}

const navItems: NavItem[] = [
  { key: 'home', label: '首页', icon: 'home' },
  { key: 'confirmation', label: '候选确认', icon: 'confirmation' },
  { key: 'communicating', label: '沟通中', icon: 'chat' },
  { key: 'pendingInterview', label: '待面试', icon: 'calendar' },
  { key: 'interviewed', label: '已面试', icon: 'interviewed' },
  { key: 'wechat', label: '已换微信', icon: 'wechat' },
  { key: 'settings', label: '配置', icon: 'settings' },
]

interface ProductSidebarProps {
  activePage: ProductPage
  customerName: string
  customerShortName: string
  jobName: string | null
  confirmationBadge: number
  searchValue: string
  version: string
  onNavigate: (page: ProductPage) => void
  onSearch: (value: string) => void
  onOpenDiagnostics?: () => void
}

export function ProductSidebar({
  activePage,
  customerName,
  customerShortName,
  jobName,
  confirmationBadge,
  searchValue,
  version,
  onNavigate,
  onSearch,
  onOpenDiagnostics,
}: ProductSidebarProps) {
  return (
    <aside className="rh-sidebar">
      <div className="rh-sidebar-customer">
        <div className="rh-sidebar-avatar" aria-hidden="true">{customerShortName || '客'}</div>
        <div className="rh-sidebar-customer-copy">
          <strong title={customerName}>{customerName}</strong>
          <span title={jobName ?? '尚未绑定职位'}>
            <ProductIcon name="briefcase" size={13} />
            {jobName ?? '尚未绑定职位'}
          </span>
        </div>
      </div>

      <nav className="rh-sidebar-nav" aria-label="产品导航">
        {navItems.map((item) => {
          const badge = item.key === 'confirmation' ? confirmationBadge : 0
          return (
            <button
              className={`rh-nav-item${activePage === item.key ? ' is-active' : ''}`}
              key={item.key}
              onClick={() => onNavigate(item.key)}
              type="button"
            >
              <ProductIcon name={item.icon} size={19} />
              <span>{item.label}</span>
              {badge > 0 && <span className="rh-nav-badge">{badge > 99 ? '99+' : badge}</span>}
            </button>
          )
        })}
      </nav>

      <div className="rh-sidebar-bottom">
        <label className="rh-sidebar-search">
          <ProductIcon name="search" size={16} />
          <span className="rh-sr-only">搜索候选人</span>
          <input
            onChange={(event) => onSearch(event.target.value)}
            placeholder="搜索候选人"
            type="search"
            value={searchValue}
          />
        </label>
        {onOpenDiagnostics && (
          <button className="rh-diagnostics-link" onClick={onOpenDiagnostics} type="button">
            <ProductIcon name="code" size={15} />
            开发者诊断
          </button>
        )}
        <div className="rh-sidebar-version">RecruitHelper v{version}</div>
      </div>
    </aside>
  )
}

