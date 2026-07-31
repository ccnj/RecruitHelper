import { ADMIN_BASE } from '../api'

export type ConsolePage =
  | 'overview'
  | 'account'
  | 'conversation'
  | 'jobPublish'
  | 'modelConfig'
  | 'devTools'
  | 'fieldReport'

interface NavItem {
  key: ConsolePage
  label: string
  hint: string
}

// hint 是这一页"能对外造成什么"的一句话，不是功能罗列：切页前先知道
// 自己要进的是只读面还是会动平台的面。
const navItems: NavItem[] = [
  { key: 'overview', label: '总览', hint: '帧流 · 命令账本 · 快捷派发' },
  { key: 'account', label: '账号与巡检', hint: '绑定 · 开停巡检 · 人工建档' },
  { key: 'conversation', label: '会话与消息', hint: '会话账 · 消息 · 审计证词' },
  { key: 'jobPublish', label: '职位与发布', hint: '预检 · 定类别 · 试填 · 真发' },
  { key: 'modelConfig', label: '模型与配置', hint: '职位同步 · 模型连接' },
  { key: 'devTools', label: 'SQL 控制台', hint: '直连业务库，无护栏' },
  { key: 'fieldReport', label: '现场上报', hint: '打包日志与数据库上传我方' },
]

export function ConsoleSidebar({ activePage, onNavigate }: {
  activePage: ConsolePage
  onNavigate: (page: ConsolePage) => void
}) {
  return (
    <aside className="dc-sidebar" aria-label="诊断台导航">
      <div className="dc-sidebar-brand">
        <span className="dc-sidebar-brand-mark" aria-hidden="true" />
        <strong>诊断台</strong>
      </div>
      <nav className="dc-sidebar-nav">
        {navItems.map((item) => (
          <button
            key={item.key}
            type="button"
            className={`dc-nav-item${activePage === item.key ? ' is-active' : ''}`}
            aria-current={activePage === item.key ? 'page' : undefined}
            onClick={() => onNavigate(item.key)}
            title={`${item.label} · ${item.hint}`}
          >
            <span className="dc-nav-label">{item.label}</span>
            <small className="dc-nav-hint">{item.hint}</small>
          </button>
        ))}
      </nav>
      <div className="dc-sidebar-foot">
        <span className="mono">{ADMIN_BASE}</span>
      </div>
    </aside>
  )
}
