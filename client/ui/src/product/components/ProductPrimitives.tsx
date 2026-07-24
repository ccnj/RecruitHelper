import type { ReactNode } from 'react'
import { ProductIcon, type ProductIconName } from './ProductIcon'

export function PageHeader({
  title,
  description,
  meta,
}: {
  title: string
  description: string
  meta?: ReactNode
}) {
  return (
    <header className="rh-page-header">
      <div>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {meta && <div className="rh-page-meta">{meta}</div>}
    </header>
  )
}

export function EmptyState({
  icon = 'inbox',
  title,
  description,
}: {
  icon?: ProductIconName
  title: string
  description: string
}) {
  return (
    <div className="rh-empty-state">
      <div className="rh-empty-icon"><ProductIcon name={icon} size={25} /></div>
      <strong>{title}</strong>
      <p>{description}</p>
    </div>
  )
}

export function MetricValue({ value }: { value: number | null }) {
  return <>{value === null ? '—' : value.toLocaleString('zh-CN')}</>
}

export function StatusPill({
  label,
  tone = 'slate',
}: {
  label: string
  tone?: 'blue' | 'amber' | 'green' | 'red' | 'slate' | 'neutral' | 'success' | 'warning' | 'danger'
}) {
  return <span className={`rh-status-pill is-${tone}`}>{label}</span>
}

export function CandidateAvatar({ name, size = 'normal' }: { name: string; size?: 'normal' | 'large' }) {
  const initial = name.trim().slice(0, 1) || '候'
  return <span className={`rh-candidate-avatar${size === 'large' ? ' is-large' : ''}`}>{initial}</span>
}

