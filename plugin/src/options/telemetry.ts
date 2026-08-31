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
import { BOSS_INPUT_COUNTERS, REPORT_EVERY } from '../program/platform/bossInputCounters'
import { CounterSnapshot, clearBaseline, readBaseline, setBaseline } from '../base/telemetry/counters'

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

// ---- 鼠标轨迹:平台自己给每次点击打的自动化分 ----
//
// `p6.za` 是 BOSS 的评分结果(`za = (100 * confidence).toFixed(1)`,
// hiBoss `report/runtime-evidence.md:482`)。**它是平台的判决,不是我们的推断** ——
// `isTrusted` 只说明事件来自浏览器输入栈,不代表平台认了。
//
// 阈值 72 不是拍的:真机对账过一次 za=66.0 的点击,平台内部 `totalScore` 是 0.55,
// 而 `isAutomated` 的线是 0.60(同报告 :495);按 `za = totalScore x 120` 折算,
// **za >= 72 即平台判定为自动化**。
//
// 主数字刻意是「za=0 的条数」而不是「没过线的条数」:验收线是 `za == 0`,
// 不是 `za < 72`(同报告 :505) —— 目标是一盏灯都不亮,不是压在线下。

/** 平台判定为自动化的分数线。推导见上。 */
const CLICK_AUTO_LINE = 72

/** za 分档。写成谓词而不是上下界——za 带一位小数,用界比较要靠 epsilon,那是坏味道。 */
const ZA_BUCKETS: readonly {
  readonly label: string
  readonly over: boolean
  readonly test: (za: number) => boolean
}[] = [
  { label: '0(干净)', over: false, test: (z) => z === 0 },
  { label: '1–30', over: false, test: (z) => z > 0 && z <= 30 },
  { label: '31–60', over: false, test: (z) => z > 30 && z <= 60 },
  { label: '61–71', over: false, test: (z) => z > 60 && z < CLICK_AUTO_LINE },
  { label: '>=72 过线', over: true, test: (z) => z >= CLICK_AUTO_LINE },
]

function shotField(shot: unknown, key: string): unknown {
  if (shot === null || typeof shot !== 'object') return undefined
  const p6 = (shot as Record<string, unknown>)['p6']
  if (p6 === null || typeof p6 !== 'object') return undefined
  return (p6 as Record<string, unknown>)[key]
}

/** 取 za。形状不认识就返回 null —— 读不出的条数单独报,不并进任何一档。 */
function shotZa(shot: unknown): number | null {
  const raw = shotField(shot, 'za')
  if (typeof raw !== 'string' && typeof raw !== 'number') return null
  const n = Number(raw)
  return Number.isFinite(n) ? n : null
}

/** 取轨迹点数。`l <= 2` 是连点/瞬移的形态。 */
function shotPoints(shot: unknown): number | null {
  const raw = shotField(shot, 'l')
  return typeof raw === 'number' && Number.isFinite(raw) ? raw : null
}

function bar(n: number, max: number, bad: boolean): string {
  const pct = max > 0 ? Math.round((100 * n) / max) : 0
  return `<div class="bar${bad ? ' bad' : ''}"><i style="width:${pct}%"></i></div>`
}

function renderClicks(shots: readonly unknown[]): string {
  if (!shots.length) {
    return '<div class="verdict idle"><b>还没抓到鼠标窗口</b>'
      + '<span class="muted">页面上每点一次就上报一条(含平台自己的评分)。点几下再刷新。</span></div>'
  }

  const zas = shots.map(shotZa)
  const readable = zas.filter((z): z is number => z !== null)
  const unreadable = zas.length - readable.length
  const clean = readable.filter((z) => z === 0).length
  const over = readable.filter((z) => z >= CLICK_AUTO_LINE).length
  const stuck = shots.filter((s) => { const l = shotPoints(s); return l !== null && l <= 2 }).length

  const verdict = over > 0
    ? `<div class="verdict warn"><b>${over} 次点击被平台判定为自动化(za >= ${CLICK_AUTO_LINE})</b>`
      + '<span class="muted">这是平台自己算出来并上送的分,不是我们的推断。</span></div>'
    : clean === readable.length
      ? '<div class="verdict good"><b>全部点击 za=0</b>'
        + '<span class="muted">平台没在任何一次点击的轨迹里看出自动化特征。</span></div>'
      : `<div class="verdict good"><b>没有点击过线,但只有 ${clean}/${readable.length} 次是 za=0</b>`
        + '<span class="muted">验收线是 za=0,不是"没过线"。</span></div>'

  const counts = ZA_BUCKETS.map((b) => readable.filter((z) => b.test(z)).length)
  const max = Math.max(1, ...counts)
  const rows = ZA_BUCKETS.map((b, i) =>
    `<span>${escapeHTML(b.label)}</span>${bar(counts[i], max, b.over)}<b>${counts[i]}</b>`,
  ).join('')

  const recent = [...shots].slice(-20).reverse().map((s) => {
    const at = (s as Record<string, unknown> | null)?.['at']
    const when = typeof at === 'number' ? new Date(at).toLocaleTimeString('zh-CN') : '-'
    const za = shotZa(s)
    const l = shotPoints(s)
    const flag = za !== null && za >= CLICK_AUTO_LINE ? ' style="color:#b3541e"' : ''
    return `<li${flag}>${escapeHTML(when)} — za <b>${za === null ? '读不出' : za}</b>`
      + `,轨迹点 ${l === null ? '-' : l}</li>`
  }).join('')

  return verdict
    + `<p class="muted">窗口 <b>${shots.length}</b> 条｜za=0 的 <b>${clean}</b> 条`
    + `｜过线 <b>${over}</b> 条｜连点(轨迹点<=2) <b>${stuck}</b> 条`
    + (unreadable ? `｜<b>${unreadable}</b> 条读不出 za` : '') + '</p>'
    + `<div class="bars">${rows}</div>`
    + `<details><summary>最近 20 条明细(共 ${shots.length} 条)</summary><ul>${recent}</ul></details>`
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

  el('clicks').innerHTML = renderClicks(shots)
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

// ---- 平台的输入行为账本 ----

const ask = <T,>(msg: unknown): Promise<T> =>
  new Promise((resolve) => { chrome.runtime.sendMessage(msg, (r: T) => resolve(r)) })

let lastSnapshot: CounterSnapshot | null = null

function renderCounters(snap: CounterSnapshot, base: CounterSnapshot | null): string {
  if (snap.note !== undefined) {
    return `<div class="verdict idle"><b>没读到</b><span class="muted">${escapeHTML(snap.note)}</span></div>`
  }

  const total = snap.counts['input_count'] ?? 0
  const hits = BOSS_INPUT_COUNTERS.filter((c) => c.key !== 'input_count' && (snap.counts[c.key] ?? 0) > 0)
  const armed = hits.filter((c) => c.triggersReport)
  const toGo = total % REPORT_EVERY === 0 && total > 0 ? 0 : REPORT_EVERY - (total % REPORT_EVERY)

  const verdict = !hits.length
    ? '<div class="verdict good"><b>平台没记下任何输入异常</b></div>'
    : armed.length
      ? `<div class="verdict warn"><b>已中 ${hits.length} 项,其中 ${armed.length} 项属会触发上报的五项</b>`
        // 正文里不能用 <b> —— 本页 CSS 有 `.verdict b { display: block }`(给标题行用的),
        // 行内强调会被撑成块级,数字单独占一行。用 <strong> 走行内。
        + `<span class="muted">聚合上报的判据是「总输入次数是 ${REPORT_EVERY} 的整数倍」且这五项任一非 0。`
        + `当前 <strong>${total}</strong> 次,再有 <strong>${toGo}</strong> 次即命中一次判据。</span></div>`
      : `<div class="verdict warn"><b>已中 ${hits.length} 项,但都不在会触发上报的五项里</b></div>`

  const rows = BOSS_INPUT_COUNTERS.map((c) => {
    const now = snap.counts[c.key] ?? 0
    const was = base ? (base.counts[c.key] ?? 0) : null
    const delta = was === null ? null : now - was
    const mark = c.triggersReport ? ' <b title="会触发聚合上报">★</b>' : ''
    const src = c.verified ? '' : ' <span class="muted" title="语义为二手,本仓库未真机确认">?</span>'
    const d = delta === null || delta === 0 ? '' : ` <b>(+${delta})</b>`
    const cls = now > 0 && c.key !== 'input_count' ? ' style="color:#b3541e"' : ''
    return `<li${cls}><code>${escapeHTML(c.key)}</code>${mark}${src} — ${escapeHTML(c.label)}:<b>${now}</b>${d}</li>`
  }).join('')

  const written = snap.writtenAt === null ? '未知' : new Date(snap.writtenAt).toLocaleString('zh-CN')
  const lag = snap.writtenAt === null ? '' : `,落后本次读取 ${Math.round((snap.readAt - snap.writtenAt) / 1000)} 秒`
  const extras = Object.entries(snap.extras)
    .map(([k, v]) => `<li><code>${escapeHTML(k)}</code>:${v === null ? '<span class="muted">无</span>' : `${v.length} 字节`}</li>`)
    .join('')

  return verdict
    + `<p class="muted">读取于 ${new Date(snap.readAt).toLocaleString('zh-CN')};`
    + `平台写盘于 ${escapeHTML(written)}${lag}`
    + `${base ? `;基线取自 ${new Date(base.readAt).toLocaleString('zh-CN')}` : ';未设基线'}</p>`
    + `<ul>${rows}</ul>`
    + `<div class="muted">★ = 会触发聚合上报的五项;? = 语义为二手、本仓库未真机确认</div>`
    + `<h2>其他账本</h2><ul>${extras}</ul>`
}

async function refreshCounters(): Promise<void> {
  el('cnt').innerHTML = '<p class="muted">读着呢…</p>'
  const snap = await ask<CounterSnapshot>({ type: 'telemetryCounters:read' })
  lastSnapshot = snap
  const base = await readBaseline(storage)
  el('cnt').innerHTML = renderCounters(snap, base)
}

el('cntRead').addEventListener('click', () => { void refreshCounters() })
el('cntBase').addEventListener('click', () => {
  void (async () => {
    if (!lastSnapshot || lastSnapshot.note !== undefined) { el('status').textContent = '先成功读一次再设基线'; return }
    await setBaseline(storage, lastSnapshot)
    el('status').textContent = '已设为基线'
    await refreshCounters()
  })()
})
el('cntClearBase').addEventListener('click', () => {
  void (async () => {
    await clearBaseline(storage)
    el('status').textContent = '基线已清除'
    await refreshCounters()
  })()
})

void render()
