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

void render()
