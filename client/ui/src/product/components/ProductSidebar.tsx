import type { BoundJobView, CandidateView, ProductPage } from '../types'
import { ProductIcon, type ProductIconName } from './ProductIcon'

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

const SYNC_JOBS_EXPLANATION =
  '同步职位：重新读取后台职位，更新当前绑定职位，并让主动来聊的候选人能匹配到后台在招的其他职位'

// 刷新图标的悬停提示:一句说明 + 当前同步状态与最近同步时间。首页那条职位卡
// (2026-09-08 甲方裁决撤下)原先承载的「配置已同步」「同步于 …」两样信息都收
// 进这里,不丢。
export function syncJobsTitle(job: BoundJobView, available: boolean): string {
  if (!available) return '运行控制尚未接入'
  const when = job.lastSyncedAt ? `同步于 ${job.lastSyncedAt}` : '尚无同步记录'
  return `${SYNC_JOBS_EXPLANATION}\n${job.syncStateLabel} · ${when}`
}

interface ProductSidebarProps {
  activePage: ProductPage
  customerName: string
  customerShortName: string
  job: BoundJobView
  confirmationBadge: number
  // 必须是脑侧真实总数(ProductData.candidateTotals),不能拿列表长度充数——那是
  // 单页加载上限,截断时数字会停在上限值、看起来像"正好这么多人",还跟进页后的
  // 「N 位候选人」对不上。两处同源才不会让人怀疑哪个是真的。
  candidateTotals: Record<CandidateView, number>
  searchValue: string
  version: string
  onNavigate: (page: ProductPage) => void
  onSearch: (value: string) => void
  // 同步职位。2026-09-08 甲方裁决:首页「当前绑定职位」整条卡撤下,同步动作收成
  // 左上角客户块里的一个刷新图标;动作本身不变,仍是产品 API 的同步职位。
  onSyncJobs?: () => void | Promise<void>
}

export function ProductSidebar({
  activePage,
  customerName,
  customerShortName,
  job,
  confirmationBadge,
  candidateTotals,
  searchValue,
  version,
  onNavigate,
  onSearch,
  onSyncJobs,
}: ProductSidebarProps) {
  const syncing = job.syncState === 'syncing'
  return (
    <aside className="rh-sidebar">
      <div className="rh-sidebar-customer">
        <div className="rh-sidebar-avatar" aria-hidden="true">{customerShortName || '客'}</div>
        <div className="rh-sidebar-customer-copy">
          <strong title={customerName}>{customerName}</strong>
          <span title={job.name ?? '尚未绑定职位'}>
            <ProductIcon name="briefcase" size={13} />
            {job.name ?? '尚未绑定职位'}
          </span>
        </div>
        <button
          aria-label="同步职位"
          className={`rh-sidebar-sync${syncing ? ' is-syncing' : ''}`}
          disabled={!onSyncJobs || syncing}
          onClick={() => void onSyncJobs?.()}
          title={syncJobsTitle(job, Boolean(onSyncJobs))}
          type="button"
        >
          <ProductIcon name="refresh" size={16} />
        </button>
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
      </div>
    </aside>
  )
}
