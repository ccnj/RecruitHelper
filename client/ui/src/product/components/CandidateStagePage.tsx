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
    description: '查看正在沟通、等候回复、已发出邀面卡等确认和需要人工关注的候选人。本页不提供发送入口。',
    emptyTitle: '当前没有沟通中的候选人',
    emptyDescription: '取得首次招呼发送正证的候选人会自动进入沟通范围。',
    filters: ['全部', '已招呼', '已回复', '已邀面', '需要人工', '沟通已结束'],
  },
  interviewed: {
    title: '已约面',
    description: '查看已接受面试邀约、面试时间还没到的候选人。本页只读。',
    emptyTitle: '当前没有待进行的面试',
    emptyDescription: '候选人接受面试邀约后，记录会出现在这里；时间过了会自动转入已面试。',
    filters: ['全部', '今天'],
  },
  interviewElapsed: {
    title: '已面试',
    description: '按约定的面试时间已过自动归类。系统没有面试是否实际进行的事实，本页不代表候选人到场或结果。',
    emptyTitle: '当前没有已过面试时间的候选人',
    emptyDescription: '已约面的候选人过了面试时间会自动转入这里。',
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
  total: number
  globalSearch: string
  onOpenCandidate: (candidate: CandidateViewItem) => void
}

export function CandidateStagePage({
  view,
  candidates,
  total,
  globalSearch,
  onOpenCandidate,
}: CandidateStagePageProps) {
  const config = stageConfigs[view]
  const [filter, setFilter] = useState('全部')
  const filtered = useMemo(
    () => candidates.filter((candidate) => matchesSearch(candidate, globalSearch) && matchesFilter(view, candidate, filter)),
    [candidates, filter, globalSearch, view],
  )
  // 单页读取有上限，candidates 可能只是脑侧前若干位。计数必须报 total，
  // 否则人数会永远停在上限值、看起来像"正好这么多人"，还会跟首页累计
  // 账面对不上。筛选是在已加载的这些人里做的，所以截断时必须说清楚。
  const truncated = total > candidates.length
  const filtering = globalSearch.trim() !== '' || filter !== '全部'

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
        <span className="rh-result-count">
          {stageCountLabel(filtered.length, candidates.length, total, filtering, truncated)}
        </span>
      </div>

      {truncated && (
        <div className="rh-inline-note">
          <ProductIcon name="warning" size={15} />
          共 {total} 位候选人，本页只加载了最近活动的 {candidates.length} 位；更早的暂未显示。
        </div>
      )}

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
                <ProductIcon className="rh-row-chevron" name="chevron" size={17} />
              </button>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

export function stageCountLabel(
  filteredCount: number,
  loadedCount: number,
  total: number,
  filtering: boolean,
  truncated: boolean,
): string {
  if (!filtering) return `${total} 位候选人`
  if (truncated) return `已加载 ${loadedCount} 位中筛出 ${filteredCount} 位`
  return `${filteredCount} / ${total} 位候选人`
}

function CandidateAuxiliary({ view, candidate }: { view: CandidateView; candidate: CandidateViewItem }) {
  if (view === 'interviewed' || view === 'interviewElapsed') {
    return (
      <div className="rh-candidate-aux">
        <span>面试时间</span>
        <strong>{candidate.interviewAt ?? '—'}</strong>
        <small>{candidate.interviewMethod ?? '方式待确认'}</small>
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
  if (view === 'interviewed' && filter === '今天') return candidate.interviewAt?.includes('今天') ?? false
  if (view === 'wechat' && filter === '仍在自动沟通') return candidate.stillInAutoCommunication === true
  if (view === 'wechat' && filter === '已结束沟通') return candidate.stillInAutoCommunication === false
  return candidate.statusLabel.includes(filter)
}
