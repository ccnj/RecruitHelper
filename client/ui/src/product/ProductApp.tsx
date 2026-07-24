import { useEffect, useMemo, useState } from 'react'
import { CandidateDrawer } from './components/CandidateDrawer'
import { CandidateStagePage } from './components/CandidateStagePage'
import { ConfirmationPage } from './components/ConfirmationPage'
import { HomePage } from './components/HomePage'
import { ProductSidebar } from './components/ProductSidebar'
import { SettingsPage } from './components/SettingsPage'
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
}

const candidatePages = new Set<ProductPage>([
  'communicating',
  'pendingInterview',
  'interviewed',
  'wechat',
])

export function ProductApp({
  data = createEmptyProductData(),
  actions = {},
  initialPage = 'home',
  fixtureNotice,
}: ProductAppProps) {
  const [activePage, setActivePage] = useState<ProductPage>(initialPage)
  const [globalSearch, setGlobalSearch] = useState('')
  const [drawerCandidate, setDrawerCandidate] = useState<CandidateViewItem | null>(null)
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const batchSelectionKey = useMemo(
    () => `${data.confirmation.batchId ?? 'none'}:${data.confirmation.candidates.map((candidate) => candidate.profileId).join(',')}`,
    [data.confirmation.batchId, data.confirmation.candidates],
  )

  useEffect(() => {
    setSelectedIds(new Set())
  }, [batchSelectionKey])

  function navigate(page: ProductPage) {
    setActivePage(page)
    setDrawerCandidate(null)
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
        onOpenCandidate={setDrawerCandidate}
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
        onOpenCandidate={setDrawerCandidate}
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
        onOpenDiagnostics={actions.openDiagnostics}
        onSearch={setGlobalSearch}
        searchValue={globalSearch}
        version={data.clientVersion}
      />
      <main className="rh-product-main">
        {fixtureNotice && <div className="rh-fixture-notice">{fixtureNotice}</div>}
        {content}
      </main>
      <CandidateDrawer actions={actions} candidate={drawerCandidate} onClose={() => setDrawerCandidate(null)} />
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

