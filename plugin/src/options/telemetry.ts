// 平台埋点观测的查看页。
//
// 它要回答的核心问题只有一个:**我们的插件在平台眼里隐不隐形。**
// BOSS 把 `Object.keys(window)` 与自己那份白名单求差、未知全局名原样上送
// (800001 的 `p6`),所以这个问题可以点名查,不用靠"我们应该是安全的"这种推理。
//
// 读侧与写侧共用 `base/telemetry/store` 的同一份分片实现——上游的教训是
// 两边各抄一份,谁都测不到,而**存储 bug 是会丢数据的**。

import { KIND_CLICK, KIND_UPLOAD, TelemetryStorage, clear, readAll } from '../base/telemetry/store'
import { TelemetryEntry } from '../base/telemetry/capture'
import { bossCodeMeaning, classifyBossEntry } from '../program/platform/telemetrySites'

const storage: TelemetryStorage = {
  get: (keys) => chrome.storage.local.get(keys as string | string[]),
  set: (items) => chrome.storage.local.set(items),
  remove: (keys) => chrome.storage.local.remove(keys as string | string[]),
}

function el(id: string): HTMLElement {
  const node = document.getElementById(id)
  if (!node) throw new Error(`缺少节点 ${id}`)
  return node
}

function list(items: readonly string[]): string {
  if (!items.length) return '<span class="muted">无</span>'
  return `<ul>${items.map((s) => `<li><code>${escapeHTML(s)}</code></li>`).join('')}</ul>`
}

function escapeHTML(text: string): string {
  return text.replace(/[&<>"]/g, (c) => (
    c === '&' ? '&amp;' : c === '<' ? '&lt;' : c === '>' ? '&gt;' : '&quot;'
  ))
}

function tally(values: readonly string[]): string[] {
  const counts = new Map<string, number>()
  for (const v of values) counts.set(v, (counts.get(v) ?? 0) + 1)
  return [...counts.entries()]
    .sort((a, b) => b[1] - a[1])
    .map(([value, n]) => `${value} x${n}`)
}

function renderHits(hits: readonly { code: string; action: string }[]): string {
  if (!hits.length) {
    return '<span class="muted">无。注意:设备指纹上报(800001/800003/800009)每次页面加载'
      + '无条件发,算例行、不算命中。</span>'
  }

  const counts = new Map<string, number>()
  for (const h of hits) counts.set(h.code, (counts.get(h.code) ?? 0) + 1)

  const real: string[] = []
  const noisy: string[] = []
  for (const [code, n] of [...counts.entries()].sort((a, b) => b[1] - a[1])) {
    const meaning = bossCodeMeaning(code)
    const line = `<b>${escapeHTML(code)}</b> x${n} — ${escapeHTML(meaning.label)}`
    ;(meaning.nearUniversal ? noisy : real).push(line)
  }

  let html = real.length
    ? `<ul>${real.map((l) => `<li>${l}</li>`).join('')}</ul>`
    : '<span class="muted">没有值得看的命中。</span>'

  if (noisy.length) {
    html += '<p class="muted" style="margin-top:0.8rem">下面这些<b>近乎每台机器都会报</b>,不是探到了东西 ——'
      + ' 本机端口探测的回调是 <code>onopen = onclose = onerror</code>,参数是"1 秒内有反应"'
      + '而不是"连上了";localhost 上端口关着会瞬间拒绝,照样算真。</p>'
      + `<ul class="muted">${noisy.map((l) => `<li>${l}</li>`).join('')}</ul>`
  }
  return html
}

async function render(): Promise<void> {
  const entries = await readAll(storage, KIND_UPLOAD) as TelemetryEntry[]
  const shots = await readAll(storage, KIND_CLICK)

  const hits: { code: string; action: string }[] = []
  const routine: string[] = []
  const globals = new Set<string>()
  const injected: string[] = []
  const probes = new Set<string>()
  let unparsed = 0

  for (const entry of entries) {
    if (entry.raw !== undefined || entry.parseError !== undefined) unparsed += 1
    const c = classifyBossEntry(entry.url, entry.payload)
    for (const h of c.hits) hits.push(h)
    for (const r of c.routine) routine.push(`${r.code}(${bossCodeMeaning(r.code).label})`)
    for (const g of c.unknownGlobals) globals.add(g)
    for (const i of c.injected) injected.push(typeof i === 'string' ? i : JSON.stringify(i))
    for (const p of c.localProbes) probes.add(p)
  }

  // 核心结论。
  const verdict = el('verdict')
  const names = [...globals].sort()
  if (!entries.length) {
    verdict.className = 'verdict idle'
    verdict.innerHTML = '<b>还没抓到任何载荷</b><span class="muted">打开平台页面走一走,再回来刷新。</span>'
  } else if (!names.length) {
    verdict.className = 'verdict good'
    verdict.innerHTML = '<b>平台上送的未知全局名清单:空</b>'
      + '<span class="muted">平台把 Object.keys(window) 与自己的白名单求差后原样上送,这一栏为空,'
      + '说明它没有在页面全局里看见任何计划外的名字。</span>'
  } else {
    verdict.className = 'verdict warn'
    verdict.innerHTML = `<b>平台上送了 ${names.length} 个未知全局名</b>`
      + '<span class="muted">逐个核对有没有我们自己的东西。平台自己的第三方脚本也会出现在这里,'
      + '出现不等于是我们的。</span>'
      + list(names)
  }

  el('hits').innerHTML = renderHits(hits)

  const first = entries[0]?.at
  const last = entries[entries.length - 1]?.at
  const span = first && last
    ? `${new Date(first).toLocaleString('zh-CN')} — ${new Date(last).toLocaleString('zh-CN')}`
    : '-'
  el('overview').innerHTML = `<ul>`
    + `<li>明细 <b>${entries.length}</b> 条,其中解析失败 <b>${unparsed}</b> 条</li>`
    + `<li>带鼠标轨迹的点击窗口 <b>${shots.length}</b> 条</li>`
    + `<li>时间范围 ${escapeHTML(span)}</li>`
    + `<li>例行码 ${escapeHTML(tally(routine).join('、') || '无')}</li>`
    + `</ul>`

  el('misc').innerHTML = `<div>注入检测报告:${list(tally(injected))}</div>`
    + `<div>被探到的本机端口:${list([...probes].sort())}</div>`
}

function download(): void {
  void (async () => {
    const data = {
      exportedAt: new Date().toISOString(),
      uploads: await readAll(storage, KIND_UPLOAD),
      clicks: await readAll(storage, KIND_CLICK),
    }
    const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }))
    const a = document.createElement('a')
    a.href = url
    a.download = `telemetry-${Date.now()}.json`
    a.click()
    URL.revokeObjectURL(url)
  })()
}

el('refresh').addEventListener('click', () => { void render() })
el('export').addEventListener('click', download)
el('clear').addEventListener('click', () => {
  void (async () => {
    await clear(storage, KIND_UPLOAD)
    await clear(storage, KIND_CLICK)
    el('status').textContent = '已清空'
    await render()
  })()
})

// ---- 扩展 origin fetch 探针(临时,验完即删) ----

interface ProbeRow {
  label: string
  url: string
  ok: boolean
  httpStatus?: number
  bizCode?: number
  bizMessage?: string
  payloadBytes?: number
  error?: string
}
interface ProbeResult {
  at: number
  extensionOrigin: string
  rows: ProbeRow[]
}

function renderProbe(r: ProbeResult | null): void {
  const box = el('probe')
  if (!r) {
    box.innerHTML = '<p class="muted">还没跑过。点「跑一次」。</p>'
    return
  }
  const verdict = r.rows.length && r.rows.every((x) => x.ok)
    ? '<div class="verdict good"><b>通了 —— 扩展 origin 能取到登录态数据</b>BOSS 适配器可以走公开 HTTP 接口取数,不必碰 MAIN world。</div>'
    : '<div class="verdict warn"><b>没通 —— 扩展 origin 取不到</b>下面看是哪一步失败;退路是 MAIN world 一次性读取(高脆)。</div>'
  const rows = r.rows.map((x) => {
    const bits = [
      x.ok ? '<b>OK</b>' : '<b>失败</b>',
      x.httpStatus === undefined ? '' : `HTTP ${x.httpStatus}`,
      x.bizCode === undefined ? '' : `code=${x.bizCode}`,
      x.bizMessage ? `“${x.bizMessage}”` : '',
      x.payloadBytes === undefined ? '' : `载荷 ${x.payloadBytes} 字节`,
      x.error ? `错误: ${x.error}` : '',
    ].filter(Boolean).join(' · ')
    return `<li>${x.label}<br /><span class="muted">${bits}</span></li>`
  }).join('')
  box.innerHTML = `${verdict}<p class="muted">本扩展 origin: <code>${r.extensionOrigin}</code> · ${new Date(r.at).toLocaleString('zh-CN')}</p><ul>${rows}</ul>`
}

const ask = <T,>(msg: unknown): Promise<T> =>
  new Promise((resolve) => { chrome.runtime.sendMessage(msg, (r: T) => resolve(r)) })

el('probeRun').addEventListener('click', () => {
  el('probe').innerHTML = '<p class="muted">跑着呢…</p>'
  void ask<ProbeResult>({ type: 'bossOriginProbe:run' }).then(renderProbe)
})
el('probeSetGid').addEventListener('click', () => {
  const gid = (el('probeGid') as HTMLInputElement).value.trim()
  void ask({ type: 'bossOriginProbe:setGid', gid }).then(() => {
    el('status').textContent = gid ? `已记住 gid ${gid}` : '已清空 gid'
  })
})

void ask<ProbeResult | null>({ type: 'bossOriginProbe:read' }).then(renderProbe)

// ---- MAIN world 注入足迹探针(临时,验完即删) ----

interface MwShot {
  walkMs: number
  visitedComponents: number
  foundMessageArrayLen: number
  globalCount: number
  globals: string[]
  perfCount: number
  perfExtensionHits: { name: string; type: string }[]
  perfTypes: Record<string, number>
  longTasks: { start: number; dur: number }[]
  error?: string
}
interface MwResult {
  at: number
  tabUrl: string
  shots: MwShot[]
  globalsAddedBetweenShots: string[]
  afterFilesInjection: { perfExtensionHits: { name: string; type: string }[]; globalsAdded: string[] } | null
  notes: string[]
}

function renderMw(r: MwResult | null): void {
  const box = el('mw')
  if (!r) { box.innerHTML = '<p class="muted">还没跑过。</p>'; return }
  if (!r.shots.length) {
    box.innerHTML = `<div class="verdict warn"><b>没跑成</b>${r.notes.join(' ')}</div>`
    return
  }
  const s0 = r.shots[0]
  const filesHits = r.afterFilesInjection ? r.afterFilesInjection.perfExtensionHits : []
  const filesGlobals = r.afterFilesInjection ? r.afterFilesInjection.globalsAdded : []
  const cleanGlobals = r.globalsAddedBetweenShots.length === 0 && filesGlobals.length === 0
  const cleanPerf = s0.perfExtensionHits.length === 0 && filesHits.length === 0
  const good = cleanGlobals && cleanPerf
  const verdict = good
    ? '<div class="verdict good"><b>没留下痕迹</b>两次注入之间 window 全局零增量,performance timeline 上没有任何扩展相关条目(func 与 files 两种形式都没有)。</div>'
    : `<div class="verdict warn"><b>留下了痕迹 —— 看下面</b>${cleanGlobals ? '' : '全局有增量。'}${cleanPerf ? '' : 'performance 上出现了扩展条目。'}</div>`
  const li = (t: string) => `<li>${t}</li>`
  const rows = [
    li(`window 全局:第一枪 ${s0.globalCount} 个,两枪之间新增 <b>${r.globalsAddedBetweenShots.length}</b> 个${r.globalsAddedBetweenShots.length ? ` — <code>${r.globalsAddedBetweenShots.join(', ')}</code>` : ''}`),
    li(`files 形式注入后再新增 <b>${filesGlobals.length}</b> 个${filesGlobals.length ? ` — <code>${filesGlobals.join(', ')}</code>` : ''}`),
    li(`performance 条目共 ${s0.perfCount} 条,类型分布 <code>${Object.entries(s0.perfTypes).map(([k, v]) => `${k}:${v}`).join(' ')}</code>`),
    li(`其中扩展相关条目:<b>func 形式 ${s0.perfExtensionHits.length} 条 / files 形式 ${filesHits.length} 条</b>${[...s0.perfExtensionHits, ...filesHits].map((h) => `<br /><span class="muted">${h.type} — ${h.name}</span>`).join('')}`),
    li(`Vue 树形状搜索:走了 ${s0.visitedComponents} 个组件,耗时 <b>${s0.walkMs} ms</b>,找到的最长消息数组 ${s0.foundMessageArrayLen} 条`),
    li(`页面 longtask:${s0.longTasks.length} 条${s0.longTasks.length ? ` — ${s0.longTasks.map((t) => `${t.dur}ms`).join(', ')}` : ''}`),
  ].join('')
  const notes = r.notes.length ? `<p class="muted">${r.notes.join('<br />')}</p>` : ''
  const errs = r.shots.filter((x) => x.error).map((x) => `<p class="muted">取样报错: ${x.error}</p>`).join('')
  box.innerHTML = `${verdict}<p class="muted">${r.tabUrl} · ${new Date(r.at).toLocaleString('zh-CN')}</p><ul>${rows}</ul>${notes}${errs}`
}

el('mwRun').addEventListener('click', () => {
  el('mw').innerHTML = '<p class="muted">跑着呢…</p>'
  void ask<MwResult>({ type: 'mainWorldProbe:run' }).then(renderMw)
})

void render()
