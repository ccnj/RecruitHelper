import type { ProductUpdateStatus } from '../api'
import type { CandidateView, ProductPage } from '../types'
import { ProductIcon, type ProductIconName } from './ProductIcon'
import { UpdateBanner } from './UpdateBanner'

interface NavItem {
  key: ProductPage
  label: string
  icon: ProductIconName
  // 只有「候选确认」用红色徽章:红色的意思是"有事等你动手"。下面四个是只读
  // 存量页,「已换微信 4」不是 4 件待办,给它们红点会让红色贬值到无人再看
  // (2026-08-01 甲方裁决)。所以它们走中性计数 count,红色留给唯一要动手的那项。
  count?: CandidateView
}

const navItems: NavItem[] = [
  { key: 'home', label: '首页', icon: 'home' },
  { key: 'confirmation', label: '候选确认', icon: 'confirmation' },
  { key: 'communicating', label: '沟通中', icon: 'chat', count: 'communicating' },
  { key: 'interviewed', label: '已约面', icon: 'calendar', count: 'interviewed' },
  { key: 'interviewElapsed', label: '已面试', icon: 'interviewed', count: 'interviewElapsed' },
  { key: 'wechat', label: '已换微信', icon: 'wechat', count: 'wechat' },
  { key: 'settings', label: '配置', icon: 'settings' },
]

interface ProductSidebarProps {
  activePage: ProductPage
  customerName: string
  customerShortName: string
  jobName: string | null
  confirmationBadge: number
  // 必须是脑侧真实总数(ProductData.candidateTotals),不能拿列表长度充数——那是
  // 单页加载上限,截断时数字会停在上限值、看起来像"正好这么多人",还跟进页后的
  // 「N 位候选人」对不上。两处同源才不会让人怀疑哪个是真的。
  candidateTotals: Record<CandidateView, number>
  searchValue: string
  version: string
  updateStatus: ProductUpdateStatus | null
  workflowActive: boolean
  onNavigate: (page: ProductPage) => void
  onSearch: (value: string) => void
}

export function ProductSidebar({
  activePage,
  customerName,
  customerShortName,
  jobName,
  confirmationBadge,
  candidateTotals,
  searchValue,
  version,
  updateStatus,
  workflowActive,
  onNavigate,
  onSearch,
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
          // 0 不显示(2026-08-01 甲方裁决)。计数不截断成 99+:徽章截断是为了保住
          // 圆形不变形,而这里要的就是真实存量,「沟通中 394」显示成 99+ 等于没说。
          const count = item.count ? candidateTotals[item.count] : 0
          return (
            <button
              aria-label={item.label}
              className={`rh-nav-item${activePage === item.key ? ' is-active' : ''}`}
              key={item.key}
              onClick={() => onNavigate(item.key)}
              title={item.label}
              type="button"
            >
              <ProductIcon name={item.icon} size={19} />
              <span>{item.label}</span>
              {badge > 0 && <span className="rh-nav-badge">{badge > 99 ? '99+' : badge}</span>}
              {count > 0 && <span className="rh-nav-count">{count}</span>}
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
        <div className="rh-sidebar-version">AI增员助手 v{version}</div>
        <UpdateBanner status={updateStatus} workflowActive={workflowActive} />
      </div>
    </aside>
  )
}
