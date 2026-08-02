// 模型与配置：职位同步、模型连接兜底，以及半退役的 M5 试运行档案。
// 后台职位与发布已拆到「职位与发布」页，这里不再承载对外副作用入口。
import { useCallback, useEffect, useState } from 'react'
import { api, JobConfigSourceView, M5AIContextView, M5ProviderConfigView } from '../../api'
import { errorText } from '../format'

// 前端已经不提交任何预算或超时参数了:provider 标签由脑从 base_url 推导，model
// 随旧后台 job-config 下发(AGENTS.md 2026-07-30 裁决)；token 预算自 2026-08-01、
// 请求超时自 2026-08-02 起都只由脑的代码常量固定。展示用的实际值一律从 GET
// 响应里读——前端猜一个写在这里，只会在脑改了常量之后变成假信息。

export function ModelConfigPage() {
  const [contexts, setContexts] = useState<M5AIContextView[]>([])
  const [contextsLoading, setContextsLoading] = useState(true)
  const [contextsError, setContextsError] = useState('')
  const [bundleText, setBundleText] = useState('')
  const [selectedRevision, setSelectedRevision] = useState('')
  const [contextBusy, setContextBusy] = useState<'sync' | 'import' | 'bind' | ''>('')
  const [contextNotice, setContextNotice] = useState<{ kind: 'ok' | 'bad'; text: string } | null>(null)
  const [jobSource, setJobSource] = useState<JobConfigSourceView | null>(null)
  const [jobSourceLoading, setJobSourceLoading] = useState(true)
  const [jobSourceError, setJobSourceError] = useState('')
  const [providerConfig, setProviderConfig] = useState<M5ProviderConfigView | null>(null)
  const [providerLoading, setProviderLoading] = useState(true)
  const [providerError, setProviderError] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [apiKey, setAPIKey] = useState('')
  const [providerSaving, setProviderSaving] = useState(false)
  const [providerNotice, setProviderNotice] = useState<{ kind: 'ok' | 'bad'; text: string } | null>(null)

  const loadContexts = useCallback(async () => {
    setContextsLoading(true)
    try {
      const result = await api.m5Contexts()
      setContexts(Array.isArray(result.contexts) ? result.contexts : [])
      setContextsError('')
    } catch (reason) {
      setContextsError(errorText(reason))
    } finally {
      setContextsLoading(false)
    }
  }, [])

  const loadProvider = useCallback(async () => {
    setProviderLoading(true)
    try {
      const result = await api.m5ProviderConfig()
      setProviderConfig(result.config)
      setProviderError('')
    } catch (reason) {
      setProviderError(errorText(reason))
    } finally {
      setProviderLoading(false)
    }
  }, [])

  const loadJobSource = useCallback(async () => {
    setJobSourceLoading(true)
    try {
      const result = await api.jobConfigSource()
      setJobSource(result.config)
      setJobSourceError('')
    } catch (reason) {
      setJobSourceError(errorText(reason))
    } finally {
      setJobSourceLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadContexts()
    void loadProvider()
    void loadJobSource()
  }, [loadContexts, loadJobSource, loadProvider])

  const syncCurrentJob = async () => {
    setContextBusy('sync')
    setContextNotice(null)
    try {
      const result = await api.syncCurrentJobConfig()
      const synced = Array.isArray(result.contexts) ? result.contexts : []
      if (synced[0]) setSelectedRevision(synced[0].revisionHash)
      setContextNotice({ kind: 'ok', text: '旧后台当前职位已同步为本地不可变版本。' })
      await loadContexts()
    } catch (reason) {
      setContextNotice({ kind: 'bad', text: errorText(reason) })
    } finally {
      setContextBusy('')
    }
  }

  const importBundle = async () => {
    setContextNotice(null)
    let parsed: unknown
    try {
      parsed = JSON.parse(bundleText)
    } catch {
      setContextNotice({ kind: 'bad', text: 'JSON 无法解析，请检查是否粘贴了完整响应。' })
      return
    }
    if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
      setContextNotice({ kind: 'bad', text: '职位资料必须是一个完整的 JSON 对象。' })
      return
    }
    const bundle = parsed as Record<string, unknown>
    setContextBusy('import')
    try {
      await api.importM5Contexts(bundle)
      setBundleText('')
      setContextNotice({ kind: 'ok', text: '职位资料已导入；请在上方列表明确选择要绑定的版本。' })
      await loadContexts()
    } catch (reason) {
      setContextNotice({ kind: 'bad', text: errorText(reason) })
    } finally {
      setContextBusy('')
    }
  }

  const bindContext = async () => {
    const selected = contexts.find((context) => context.revisionHash === selectedRevision)
    if (!selected) return
    setContextBusy('bind')
    setContextNotice(null)
    try {
      await api.bindM5Context(selected.contextId, selected.revisionHash)
      setContextNotice({ kind: 'ok', text: `“${selected.displayName}”已绑定到当前试运行档案。` })
    } catch (reason) {
      setContextNotice({ kind: 'bad', text: errorText(reason) })
    } finally {
      setContextBusy('')
    }
  }

  const saveProvider = async () => {
    setProviderSaving(true)
    setProviderNotice(null)
    try {
      const result = await api.saveM5ProviderConfig({
        base_url: baseURL.trim(),
        api_key: apiKey.trim(),
      })
      setProviderConfig(result.config)
      setBaseURL('')
      setAPIKey('')
      setProviderNotice({ kind: 'ok', text: '模型连接已保存在本机；页面不会回显地址或密钥。下次同步职位配置时，后台下发的地址与密钥会覆盖这里手填的值。重启客户端后生效。' })
    } catch (reason) {
      setProviderNotice({ kind: 'bad', text: errorText(reason) })
    } finally {
      setProviderSaving(false)
    }
  }

  const providerReady = providerConfig?.baseUrlConfigured === true && providerConfig.keyConfigured === true
  const sourceReady = jobSource?.configured === true
    && jobSource.machineIdentityReady === true && jobSource.machineMatch === true
  return (
    <section className="m5-ai-panel" aria-labelledby="m5-ai-title">
      <div className="m5-ai-heading">
        <h2 id="m5-ai-title">模型连接与职位同步</h2>
        <div className={`m5-ai-readiness ${providerReady ? 'is-ready' : ''}`}>
          <span />
          {providerLoading ? '正在核对模型配置' : providerReady ? '模型连接材料已齐' : '模型连接尚未配齐'}
        </div>
      </div>

      <div className="m5-ai-wire">
        <section className="m5-ai-step" aria-labelledby="m5-source-title">
          <header><strong id="m5-source-title">职位同步</strong><small>激活与首次绑定在产品端「激活这台电脑」完成，此处只重新同步</small></header>
          <div className="m5-provider-state m5-source-state">
            <span>后台地址 <strong>{jobSource?.baseUrlConfigured ? '已配置' : '未配置'}</strong></span>
            <span>本机身份 <strong>{jobSource?.machineIdentityReady ? '可用' : '不可用'}</strong></span>
            <span>正式授权 <strong>{sourceReady ? jobSource?.customerName || '已激活' : '未激活或不匹配'}</strong></span>
          </div>
          {jobSourceError && <p className="m5-ai-message bad" role="alert">{jobSourceError}</p>}
          <button
            type="button"
            disabled={contextBusy !== '' || jobSourceLoading || !sourceReady}
            onClick={() => void syncCurrentJob()}
          >
            {contextBusy === 'sync' ? '正在同步…' : '仅重新同步当前职位'}
          </button>
          {!sourceReady && !jobSourceLoading && (
            <p className="dc-note">本机尚未激活或身份不匹配，先在产品端完成激活。</p>
          )}
        </section>

        <section className="m5-ai-step" aria-labelledby="m5-provider-title">
          <header><strong id="m5-provider-title">模型连接</strong><small>随旧后台职位配置自动下发 · 此处仅作后台缺配时的兜底</small></header>
          <div className="m5-provider-state">
            <span>服务地址 <strong>{providerConfig?.baseUrlConfigured ? '已配置' : '未配置'}</strong></span>
            <span>API Key <strong>{providerConfig?.keyConfigured ? '已配置' : '未配置'}</strong></span>
            <span>模型 <strong>{providerConfig?.model || '未配置'}</strong></span>
          </div>
          <label htmlFor="m5-base-url">新的服务地址</label>
          <input
            id="m5-base-url"
            type="url"
            value={baseURL}
            onChange={(event) => setBaseURL(event.target.value)}
            autoComplete="off"
            placeholder={providerConfig?.baseUrlConfigured ? '留空则保留现有地址' : 'https://…'}
          />
          <label htmlFor="m5-api-key">新的 API Key</label>
          <input
            id="m5-api-key"
            type="password"
            value={apiKey}
            onChange={(event) => setAPIKey(event.target.value)}
            autoComplete="new-password"
            placeholder={providerConfig?.keyConfigured ? '留空则保留现有密钥' : '输入密钥'}
          />
          <div className="m5-budget-line">
            <span>30 秒超时</span><span>输入 16K</span><span>意向 64</span><span>回复 512</span>
          </div>
          {providerError && <p className="m5-ai-message bad" role="alert">{providerError}</p>}
          <button
            type="button"
            disabled={providerSaving || (!providerConfig?.baseUrlConfigured && baseURL.trim() === '') || (!providerConfig?.keyConfigured && apiKey.trim() === '')}
            onClick={() => void saveProvider()}
          >
            {providerSaving ? '正在保存…' : '保存模型连接'}
          </button>
        </section>
      </div>

      <details className="dc-legacy">
        <summary>
          <span>M5 遗留 · 半退役</span>
          <small>试运行档案绑定与手工导入。processM5Trial 在生产代码里已无调用者，删除前需专门核实</small>
        </summary>
        <div className="dc-legacy-body">
          <div className="m5-context-list" role="radiogroup" aria-label="可用职位资料版本">
            {contextsLoading && <p className="m5-ai-empty">正在读取已导入资料…</p>}
            {!contextsLoading && contexts.length === 0 && !contextsError && (
              <p className="m5-ai-empty">还没有可用资料。</p>
            )}
            {contexts.map((context) => (
              <label key={context.revisionHash} className={`m5-context-option ${selectedRevision === context.revisionHash ? 'is-selected' : ''}`}>
                <input
                  type="radio"
                  name="m5-context"
                  value={context.revisionHash}
                  checked={selectedRevision === context.revisionHash}
                  onChange={() => setSelectedRevision(context.revisionHash)}
                />
                <span>
                  <strong>{context.displayName}</strong>
                  <small>{context.environment || '环境未标注'} · {context.documentCount} 份文档</small>
                  <code>context {context.contextId}</code>
                  <code>revision {context.revisionHash}</code>
                </span>
              </label>
            ))}
          </div>
          {contextsError && <p className="m5-ai-message bad" role="alert">{contextsError}</p>}
          <button
            type="button"
            disabled={contextBusy !== '' || selectedRevision === ''}
            onClick={() => void bindContext()}
          >
            {contextBusy === 'bind' ? '正在绑定…' : '绑定所选版本到 active 试运行档案'}
          </button>
          <details className="m5-manual-import">
            <summary>开发期手工导入 JSON</summary>
            <textarea
              value={bundleText}
              onChange={(event) => setBundleText(event.target.value)}
              rows={5}
              spellCheck={false}
              autoComplete="off"
              placeholder="粘贴完整 job-config JSON"
              aria-label="旧 job-config JSON"
            />
            <button
              type="button"
              disabled={contextBusy !== '' || bundleText.trim() === ''}
              onClick={() => void importBundle()}
            >
              {contextBusy === 'import' ? '正在导入…' : '手工导入'}
            </button>
          </details>
        </div>
      </details>

      {(contextNotice || providerNotice) && (
        <div className="m5-ai-notices" aria-live="polite">
          {contextNotice && <p className={`m5-ai-message ${contextNotice.kind}`}>{contextNotice.text}</p>}
          {providerNotice && <p className={`m5-ai-message ${providerNotice.kind}`}>{providerNotice.text}</p>}
        </div>
      )}
    </section>
  )
}
