import { useMemo, useState } from 'react'
import type { CandidateView, CandidateViewItem } from '../types'
import { CandidateAvatar, EmptyState, PageHeader, StatusPill } from './ProductPrimitives'
import { ProductIcon } from './ProductIcon'

interface StageConfig {
  title: string
  description: string
  emptyTitle: string
  emptyDescription: string
  filters: string[]
}

const stageConfigs: Record<CandidateView, StageConfig> = {
  communicating: {
    title: '沟通中',
    description: '查看正在沟通、等候回复和需要人工关注的候选人。本页不提供发送入口。',
    emptyTitle: '当前没有沟通中的候选人',
    emptyDescription: '取得首次招呼发送正证的候选人会自动进入沟通范围。',
    filters: ['全部', '已招呼', '已回复', '需要人工', '沟通已结束'],
  },
  pendingInterview: {
    title: '待面试',
    description: '查看已约面候选人的时间、方式和确认状态。本页只读。',
    emptyTitle: '当前没有待面试候选人',
    emptyDescription: '确定性状态机确认约面成功后，候选人会出现在这里。',
    filters: ['全部', '今天', '待候选人确认', '已确认'],
  },
  interviewed: {
    title: '已面试',
    description: '查看数据库中已有的真实面试结果，不在此处回填或修改。',
    emptyTitle: '当前没有已面试记录',
    emptyDescription: '完成面试并形成正式业务事实后，记录会出现在这里。',
    filters: ['全部'],
  },
  wechat: {
    title: '已换微信',
    description: '查看已收编的候选人微信资产和当前沟通范围。',
    emptyTitle: '当前没有已换微信候选人',
    emptyDescription: '系统取得微信交换成功事实后，候选人会出现在这里。',
    filters: ['全部', '仍在自动沟通', '已结束沟通', '账号待收编'],
  },
}

interface CandidateStagePageProps {
  view: CandidateView
  candidates: CandidateViewItem[]
  globalSearch: string
  onOpenCandidate: (candidate: CandidateViewItem) => void
}

export function CandidateStagePage({
  view,
  candidates,
  globalSearch,
  onOpenCandidate,
}: CandidateStagePageProps) {
  const config = stageConfigs[view]
  const [filter, setFilter] = useState('全部')
  const filtered = useMemo(
    () => candidates.filter((candidate) => matchesSearch(candidate, globalSearch) && matchesFilter(view, candidate, filter)),
    [candidates, filter, globalSearch, view],
  )

  return (
    <div className="rh-page">
      <PageHeader
        title={config.title}
        description={config.description}
        meta={<span className="rh-readonly-label">只读页面</span>}
      />

      <div className="rh-stage-toolbar">
        <div className="rh-filter-tabs" role="group" aria-label={`${config.title}筛选`}>
          {config.filters.map((item) => (
            <button
              className={filter === item ? 'is-active' : ''}
              key={item}
              onClick={() => setFilter(item)}
              type="button"
            >
              {item}
            </button>
          ))}
        </div>
        <span className="rh-result-count">{filtered.length} 位候选人</span>
      </div>

      <section className="rh-panel rh-candidate-list-panel">
        {filtered.length === 0 ? (
          <EmptyState
            icon={globalSearch || filter !== '全部' ? 'search' : 'inbox'}
            title={globalSearch || filter !== '全部' ? '没有符合条件的候选人' : config.emptyTitle}
            description={globalSearch || filter !== '全部' ? '调整筛选条件或左侧搜索关键词后再试。' : config.emptyDescription}
          />
        ) : (
          <div className="rh-candidate-list">
            {filtered.map((candidate) => (
              <button
                className="rh-candidate-row"
                key={candidate.profileId}
                onClick={() => onOpenCandidate(candidate)}
                type="button"
              >
                <CandidateAvatar name={candidate.displayName} />
                <div className="rh-candidate-main">
                  <div>
                    <strong>{candidate.displayName}</strong>
                    {candidate.age !== null && <span>{candidate.age} 岁</span>}
                    <StatusPill label={candidate.statusLabel} tone={candidate.statusTone} />
                    {candidate.manualRequired && <span className="rh-manual-badge">需要人工</span>}
                  </div>
                  <p>{candidate.lastMessage ?? '暂无对话内容'}</p>
                </div>
                <CandidateAuxiliary view={view} candidate={candidate} />
                <div className="rh-candidate-side">
                  <span>{candidate.jobName}</span>
                  <time>{candidate.lastActiveAt ?? '时间未知'}</time>
                </div>
                {candidate.unreadCount > 0 && <span className="rh-unread-badge">{candidate.unreadCount}</span>}
                <ProductIcon className="rh-row-chevron" name="chevron" size={17} />
              </button>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

function CandidateAuxiliary({ view, candidate }: { view: CandidateView; candidate: CandidateViewItem }) {
  if (view === 'pendingInterview') {
    return (
      <div className="rh-candidate-aux">
        <span>面试时间</span>
        <strong>{candidate.interviewAt ?? '—'}</strong>
        <small>{candidate.interviewMethod ?? '方式待确认'}</small>
      </div>
    )
  }
  if (view === 'interviewed') {
    return (
      <div className="rh-candidate-aux">
        <span>面试结果</span>
        <strong>{candidate.interviewResult ?? '待回填'}</strong>
        <small>{candidate.interviewAt ?? '时间未知'}</small>
      </div>
    )
  }
  if (view === 'wechat') {
    return (
      <div className="rh-candidate-aux">
        <span>微信账号</span>
        <strong>{candidate.wechatAccount ?? '尚未收编明文账号'}</strong>
        <small>{candidate.wechatExchangedAt ?? '时间未知'}</small>
      </div>
    )
  }
  return null
}

function matchesSearch(candidate: CandidateViewItem, rawQuery: string): boolean {
  const query = rawQuery.trim().toLocaleLowerCase('zh-CN')
  if (!query) return true
  return [
    candidate.displayName,
    candidate.jobName,
    candidate.statusLabel,
    candidate.currentRole,
    candidate.lastMessage,
  ].some((value) => value?.toLocaleLowerCase('zh-CN').includes(query))
}

function matchesFilter(view: CandidateView, candidate: CandidateViewItem, filter: string): boolean {
  if (filter === '全部') return true
  if (filter === '需要人工') return candidate.manualRequired
  if (view === 'communicating' && filter === '沟通已结束') {
    return candidate.deterministicState?.startsWith('沟通已结束') ?? false
  }
  if (view === 'pendingInterview' && filter === '今天') return candidate.interviewAt?.includes('今天') ?? false
  if (view === 'wechat' && filter === '仍在自动沟通') return candidate.stillInAutoCommunication === true
  if (view === 'wechat' && filter === '已结束沟通') return candidate.stillInAutoCommunication === false
  return candidate.statusLabel.includes(filter)
}
