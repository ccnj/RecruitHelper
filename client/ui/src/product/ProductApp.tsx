import { useEffect, useMemo, useRef, useState } from 'react'
import { CandidateDrawer } from './components/CandidateDrawer'
import { CandidateStagePage } from './components/CandidateStagePage'
import { ConfirmationPage } from './components/ConfirmationPage'
import { HomePage } from './components/HomePage'
import { ProductSidebar } from './components/ProductSidebar'
import { SettingsPage } from './components/SettingsPage'
import type { ProductUpdateStatus } from './api'
import { createEmptyProductData, createProductFixture } from './fixtures'
import type {
  CandidateView,
  CandidateViewItem,
  ProductActions,
  ProductData,
  ProductPage,
} from './types'
import './product.css'

export interface ProductAppProps {
  data?: ProductData
  actions?: ProductActions
  initialPage?: ProductPage
  fixtureNotice?: string
  statusMessage?: string | null
  updateStatus?: ProductUpdateStatus | null
}

const candidatePages = new Set<ProductPage>([
  'communicating',
  'interviewed',
  'interviewElapsed',
  'wechat',
])

export function ProductApp({
  data = createEmptyProductData(),
  actions = {},
  initialPage = 'home',
  fixtureNotice,
  statusMessage,
  updateStatus = null,
}: ProductAppProps) {
  const [activePage, setActivePage] = useState<ProductPage>(initialPage)
  const [globalSearch, setGlobalSearch] = useState('')
  const [drawerCandidate, setDrawerCandidate] = useState<CandidateViewItem | null>(null)
  const [drawerReadError, setDrawerReadError] = useState<string | null>(null)
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const detailRequest = useRef(0)
  const batchSelectionKey = useMemo(
    () => `${data.confirmation.batchId ?? 'none'}:${data.confirmation.candidates.map((candidate) => candidate.profileId).join(',')}`,
    [data.confirmation.batchId, data.confirmation.candidates],
  )

  useEffect(() => {
    setSelectedIds(new Set())
  }, [batchSelectionKey])

  function navigate(page: ProductPage) {
    detailRequest.current += 1
    setActivePage(page)
    setDrawerCandidate(null)
    setDrawerReadError(null)
  }

  async function openCandidate(candidate: CandidateViewItem) {
    const request = detailRequest.current + 1
    detailRequest.current = request
    setDrawerCandidate(candidate)
    setDrawerReadError(null)
    if (!actions.loadCandidateDetail) return
    try {
      const detail = await actions.loadCandidateDetail(candidate.profileId, candidate)
      if (detailRequest.current === request) setDrawerCandidate(detail)
    } catch (reason) {
      if (detailRequest.current === request) {
        setDrawerReadError(reason instanceof Error ? reason.message : '候选人详情读取失败')
      }
    }
  }

  function closeCandidate() {
    detailRequest.current += 1
    setDrawerCandidate(null)
    setDrawerReadError(null)
  }

  let content: JSX.Element
  if (activePage === 'home') {
    content = (
      <HomePage
        actions={actions}
        customer={data.customer}
        onOpenConfirmation={() => navigate('confirmation')}
        overview={data.overview}
      />
    )
  } else if (activePage === 'confirmation') {
    content = (
      <ConfirmationPage
        actions={actions}
        batch={data.confirmation}
        customer={data.customer}
        onOpenCandidate={(candidate) => void openCandidate(candidate)}
        onSelectionChange={setSelectedIds}
        selectedIds={selectedIds}
      />
    )
  } else if (candidatePages.has(activePage)) {
    const candidateView = activePage as CandidateView
    content = (
      <CandidateStagePage
        candidates={data.candidates[candidateView]}
        globalSearch={globalSearch}
        key={candidateView}
        onOpenCandidate={(candidate) => void openCandidate(candidate)}
        total={data.candidateTotals[candidateView]}
        view={candidateView}
      />
    )
  } else {
    content = <SettingsPage connections={data.connections} customer={data.customer} />
  }

  return (
    <div className="rh-product-app">
      <ProductSidebar
        activePage={activePage}
        confirmationBadge={data.confirmationBadge}
        customerName={data.customer.name}
        customerShortName={data.customer.shortName}
        jobName={data.customer.job.name}
        onNavigate={navigate}
        onSearch={setGlobalSearch}
        searchValue={globalSearch}
        updateStatus={updateStatus ?? null}
        version={data.clientVersion}
        workflowActive={
          data.overview.workflow.state === 'running'
          || data.overview.workflow.state === 'awaitingConfirmation'
        }
      />
      <main className="rh-product-main">
        {fixtureNotice && <div className="rh-fixture-notice">{fixtureNotice}</div>}
        {statusMessage && (
          <div className="rh-product-notice">
            <span>{statusMessage}</span>
            {actions.refresh && (
              <button onClick={() => void actions.refresh?.()} type="button">重新读取</button>
            )}
          </div>
        )}
        {drawerReadError && (
          <div className="rh-product-notice is-warning">
            候选人详情暂时无法刷新，抽屉保留列表中的基础信息：{drawerReadError}
          </div>
        )}
        {content}
      </main>
      <CandidateDrawer actions={actions} candidate={drawerCandidate} onClose={closeCandidate} />
    </div>
  )
}

export function ProductPreviewApp() {
  return (
    <ProductApp
      data={createProductFixture()}
      fixtureNotice="本地视觉预览数据，不代表真实业务事实"
    />
  )
}
