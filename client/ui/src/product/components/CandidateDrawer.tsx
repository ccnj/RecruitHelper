import { useEffect, useState } from 'react'
import type { CandidateViewItem, ProductActions, ResumeSectionView } from '../types'
import { CandidateAvatar, StatusPill } from './ProductPrimitives'
import { ProductIcon } from './ProductIcon'

interface CandidateDrawerProps {
  candidate: CandidateViewItem | null
  actions: ProductActions
  onClose: () => void
}

export function CandidateDrawer({ candidate, actions, onClose }: CandidateDrawerProps) {
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')

  useEffect(() => {
    if (!candidate) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [candidate, onClose])

  useEffect(() => setCopyState('idle'), [candidate?.profileId])

  if (!candidate) return null

  async function copyWechat() {
    if (!candidate?.wechatAccount) return
    try {
      if (actions.copyWechat) await actions.copyWechat(candidate.wechatAccount)
      else await navigator.clipboard.writeText(candidate.wechatAccount)
      setCopyState('copied')
    } catch {
      setCopyState('failed')
    }
  }

  return (
    <>
      <button className="rh-drawer-backdrop" aria-label="关闭候选人详情" onClick={onClose} type="button" />
      <aside className="rh-candidate-drawer" aria-label={`${candidate.displayName}详情`}>
        <header className="rh-drawer-header">
          <div>
            <CandidateAvatar name={candidate.displayName} size="large" />
            <div>
              <div className="rh-drawer-name">
                <h2>{candidate.displayName}</h2>
                {candidate.age !== null && <span>{candidate.age} 岁</span>}
              </div>
              <p>
                {[candidate.education, candidate.experience, candidate.city, candidate.currentRole]
                  .filter(Boolean)
                  .join(' · ') || '候选人画像待补充'}
              </p>
            </div>
          </div>
          <button className="rh-icon-button" aria-label="关闭" onClick={onClose} type="button">
            <ProductIcon name="close" size={20} />
          </button>
        </header>

        <div className="rh-drawer-body">
          <section className="rh-drawer-facts">
            <div><span>所属职位</span><strong>{candidate.jobName}</strong></div>
            <div><span>当前状态</span><StatusPill label={candidate.statusLabel} tone={candidate.statusTone} /></div>
            <div><span>最后活动</span><strong>{candidate.lastActiveAt ?? '—'}</strong></div>
          </section>

          <DrawerSection title="候选人画像">
            {candidate.resumeSections.length === 0 ? (
              <p className="rh-drawer-empty-copy">当前没有可展示的简历摘要。</p>
            ) : (
              <div className="rh-resume-sections">
                {candidate.resumeSections.map((section) => (
                  <ResumeSection key={section.title} section={section} />
                ))}
              </div>
            )}
          </DrawerSection>

          {(candidate.interviewAt || candidate.interviewMethod) && (
            <DrawerSection title="面试信息">
              <dl className="rh-detail-list">
                <div><dt>面试时间</dt><dd>{candidate.interviewAt ?? '—'}</dd></div>
                <div><dt>面试方式</dt><dd>{candidate.interviewMethod ?? '—'}</dd></div>
              </dl>
            </DrawerSection>
          )}

          {(candidate.wechatAccount || candidate.wechatExchangedAt) && (
            <DrawerSection title="微信资产">
              <div className="rh-wechat-asset">
                <div>
                  <span>候选人微信号</span>
                  <strong>{candidate.wechatAccount ?? '尚未收编明文账号'}</strong>
                </div>
                {candidate.wechatAccount && (
                  <button className="rh-button is-secondary is-compact" onClick={() => void copyWechat()} type="button">
                    <ProductIcon name="copy" size={15} />
                    {copyState === 'copied' ? '已复制' : copyState === 'failed' ? '复制失败' : '复制'}
                  </button>
                )}
              </div>
              <p className="rh-detail-note">换微信时间：{candidate.wechatExchangedAt ?? '—'}</p>
            </DrawerSection>
          )}

          <DrawerSection title="对话与卡片记录">
            {candidate.messages.length === 0 ? (
              <p className="rh-drawer-empty-copy">当前没有可展示的消息事实。</p>
            ) : (
              <div className="rh-message-list">
                {candidate.messages.map((message) => (
                  <div className={`rh-message is-${message.direction}`} key={message.id}>
                    <div>
                      {message.kindLabel && <span className="rh-message-kind">{message.kindLabel}</span>}
                      <p>{message.content}</p>
                    </div>
                    <time>{message.occurredAt}</time>
                  </div>
                ))}
              </div>
            )}
          </DrawerSection>

        </div>
      </aside>
    </>
  )
}

// 工作经历实测中位 29 行、最长 113 行,整段铺开会把抽屉里其余各节全部挤到
// 屏幕外。折叠按逻辑行数判定而不是量 DOM: 判据确定、不受抽屉宽度影响,代价
// 是长行较多时会略早一点出现"展开全部"。
const RESUME_CLAMP_LINES = 12

function ResumeSection({ section }: { section: ResumeSectionView }) {
  const [expanded, setExpanded] = useState(false)
  const lineCount = section.body.split('\n').length
  const clampable = lineCount > RESUME_CLAMP_LINES

  return (
    <section className="rh-resume-section">
      <h5>{section.title}</h5>
      <p className={clampable && !expanded ? 'is-clamped' : ''}>{section.body}</p>
      {clampable && (
        <button className="rh-text-button" onClick={() => setExpanded(!expanded)} type="button">
          {expanded ? '收起' : `展开全部 ${lineCount} 行`}
        </button>
      )}
    </section>
  )
}

function DrawerSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rh-drawer-section">
      <h3>{title}</h3>
      {children}
    </section>
  )
}

