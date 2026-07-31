// 诊断台跨页公用的小件。只管呈现，不持有业务状态。
//
// 原来的 <Fold> 折叠区随拆页一起废除：四个 Fold 各自成了一页，
// 再留一层折叠只会让内容多藏一次。对应的 .dc-fold 样式同批删除。
export function LedgerEmpty({ title, detail }: { title: string; detail?: string }) {
  return <div className="ledger-empty"><strong>{title}</strong>{detail && <p>{detail}</p>}</div>
}

export function InlineError({ text }: { text: string }) {
  return <div className="inline-error" role="alert"><strong>读取失败</strong><span>{text}</span></div>
}

export function EmptyWorkbench({ loading, error }: { loading: boolean; error: string | null }) {
  return (
    <section className="empty-workbench">
      <h2>{loading ? '正在读取…' : error ? '账号数据暂时不可用' : '先绑定一个已登录的平台账号'}</h2>
      <p>{error || '在左侧选择一只在线的手。脑会核验当前身份并建立账号绑定，不会保存登录凭据。'}</p>
    </section>
  )
}

export function DiagnosticCard({ title, children, aside }: {
  title: string
  children: React.ReactNode
  aside?: React.ReactNode
}) {
  return (
    <section className="diagnostic-card">
      <h3>{title}{aside && <span className="diagnostic-card-aside">{aside}</span>}</h3>
      {children}
    </section>
  )
}

// 原语的 args/guards/result 原文。suspect 队列与命令账本共用：摘要挑不出来的
// 线索都在这里，能读的按 JSON 缩进，读不动的原样显示，绝不因为解析失败就吞掉。
export function RawField({ label, value }: { label: string; value: string }) {
  if (!value) return null
  let pretty = value
  try { pretty = JSON.stringify(JSON.parse(value), null, 2) } catch { /* 非 JSON 就原样显示 */ }
  return (
    <div className="dc-raw-field">
      <span>{label}</span>
      <pre className="mono">{pretty}</pre>
    </div>
  )
}
