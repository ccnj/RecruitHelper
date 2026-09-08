// 平台埋点观测的查看页。
//
// 它要回答的核心问题只有一个:**我们的插件在平台眼里隐不隐形。**
// BOSS 把 `Object.keys(window)` 与自己那份白名单求差、未知全局名原样上送
// (800001 的 `p6`),所以这个问题可以点名查,不用靠"我们应该是安全的"这种推理。
//
// 读侧与写侧共用 `base/telemetry/store` 的同一份分片实现——上游的教训是
// 两边各抄一份,谁都测不到,而**存储 bug 是会丢数据的**。

import { KIND_CLICK, KIND_REQUEST, KIND_UPLOAD, TelemetryStorage, clear, readAll } from '../base/telemetry/store'
import { RequestRecord, hostOf } from '../base/telemetry/netCapture'
import type { CaptureStatus } from '../base/telemetry/netCaptureRegister'
import { TelemetryEntry } from '../base/telemetry/capture'
import { BossSevereHit, bossCodeMeaning, bossSevereHits, classifyBossEntry } from '../program/platform/telemetrySites'
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

  // 三档:高风险排最前、红行(与结论区「踩雷」那行说的是同一件事);其余已知命中
  // 与码表里没有的码随后;坏判据噪音折叠在最下。
  const severe: string[] = []
  const real: string[] = []
  const noisy: string[] = []
  for (const [code, n] of [...counts.entries()].sort((a, b) => b[1] - a[1])) {
    const meaning = bossCodeMeaning(code)
    const line = `<b>${escapeHTML(code)}</b> x${n} — ${escapeHTML(meaning.label)}`
    ;(meaning.nearUniversal ? noisy : meaning.severe ? severe : real).push(line)
  }

  const shown = [
    ...severe.map((l) => `<li class="bad-row">${l}</li>`),
    ...real.map((l) => `<li>${l}</li>`),
  ]
  let html = shown.length
    ? `<ul>${shown.join('')}</ul>`
    : '<span class="muted">没有值得看的命中。</span>'

  if (noisy.length) {
    // 折叠:它们每台机器都亮,常显只会把真信号淹掉。summary 带类数,不展开也知道分量。
    html += `<details><summary>例行噪音 ${noisy.length} 类(几乎每台机器都报)</summary>`
      + '<p class="muted">本机端口探测的回调是 <code>onopen = onclose = onerror</code>,'
      + '参数是"1 秒内有反应"而不是"连上了";localhost 上端口关着会瞬间拒绝,照样算真。</p>'
      + `<ul class="muted">${noisy.map((l) => `<li>${l}</li>`).join('')}</ul></details>`
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

// ---- 判决与证据的渲染 ----

/** 我们往页面上放东西时用的前缀。「有没有被平台点名」只认它。 */
const OUR_GLOBAL_PREFIX = '__recruitHelper'

function overLineCount(shots: readonly unknown[]): number {
  return shots.filter((s) => {
    const z = shotZa(s)
    return z !== null && z >= CLICK_AUTO_LINE
  }).length
}

/** 三态:true 好、false 坏、null 还没数据。 */
function verdictLine(ok: boolean | null, name: string, detail: string): string {
  const mark = ok === null ? '—' : ok ? '\u2713' : '\u2717'
  const cls = ok === null ? 'idle' : ok ? 'ok' : 'bad'
  return `<div class="vline ${cls}"><span class="vmark">${mark}</span>`
    + `<span class="vname">${escapeHTML(name)}</span>`
    + `<span class="vdetail">${detail}</span></div>`
}

/**
 * 一屏之内回答四个问题。**这四条是本页存在的理由**,其余都是给它们做证。
 *
 * 第三条问的是"有没有码表里没有的码",不是"踩雷了吗"——800001 这族指纹上报
 * 每台机器每次加载都发,算例行;真正值得抬头的是出现了我们没见过的东西。
 *
 * 第四条问的是"已知的码里,有没有一亮就等于被看穿的"。它和第三条互补:一个码补进
 * 码表后就从第三条消失,2026-09-03 的 700051 就是这样从结论区掉下去的——它是平台
 * 抓到合成点击的实锤,不能因为我们认识它就三个绿勾。
 */
function renderVerdict(
  hasData: boolean, ours: readonly string[], platformGlobals: number,
  shots: number, over: number, unknownCodes: readonly string[],
  severe: readonly BossSevereHit[],
): string {
  if (!hasData) {
    return verdictLine(null, '隐形', '还没抓到载荷')
      + verdictLine(null, '像人', '还没抓到点击')
      + verdictLine(null, '新东西', '还没抓到载荷')
      + verdictLine(null, '踩雷', '还没抓到载荷')
  }
  return verdictLine(ours.length === 0, '隐形',
    ours.length === 0
      ? `平台上送的 ${platformGlobals} 个未知全局名里没有我们的`
      : `<b>平台点名了我们的 ${ours.length} 个全局</b>:<code>${escapeHTML(ours.join(', '))}</code>`)
    + verdictLine(shots === 0 ? null : over === 0, '像人',
      shots === 0 ? '还没抓到点击'
        : over === 0 ? `${shots} 次点击没有一次过 isAutomated 线`
          : `<b>${over}/${shots} 次点击被平台判定为自动化</b>`)
    + verdictLine(unknownCodes.length === 0, '新东西',
      unknownCodes.length === 0 ? '所有事件码都在 hiBoss 的码表里'
        : `<b>${unknownCodes.length} 个码表里没有的码</b>:<code>${escapeHTML(unknownCodes.join('、'))}</code>`)
    + verdictLine(severe.length === 0, '踩雷',
      severe.length === 0 ? '已知的高风险码一个没亮'
        : `<b>命中 ${severe.length} 类高风险码</b>:` + severe.map((s) =>
          `<code title="${escapeHTML(s.label)}">${escapeHTML(s.code)}</code> x${s.n}`).join('、')
          + ',释义见下方「探测命中」')
}

function renderGlobals(names: readonly string[]): string {
  const ours = names.filter((n) => n.startsWith(OUR_GLOBAL_PREFIX))
  const theirs = names.filter((n) => !n.startsWith(OUR_GLOBAL_PREFIX))
  const head = ours.length
    ? `<div class="bad-row">我们的:${ours.map((n) => `<code>${escapeHTML(n)}</code>`).join('、')}</div>`
    : '<div>我们的:<span class="muted">无</span></div>'
  if (!theirs.length) return head
  return head
    + `<details><summary>平台自己的 ${theirs.length} 个(第三方脚本)</summary>${list(theirs)}</details>`
}

function asRec(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

/**
 * 注入检测项归并成一行一类。
 *
 * 原样印每一条等于印几千字符的 inline style,人读不了。
 * 认不出来源的**原样保留 key、标「未知」**,不并进兜底桶——
 * 没见过的形态不猜也不静默丢(与 capture 层同款纪律)。
 */
function injectionDigest(item: unknown): { key: string; source: string; ours: boolean } {
  const rec = asRec(item)
  if (rec === null) return { key: String(item).slice(0, 40), source: '未知', ours: false }

  if (rec['code'] === 99003) {
    const keys = rec['windowKeys']
    const n = Array.isArray(keys) ? keys.length : 0
    return { key: `99003 未知全局名差集(${n} 个)`, source: '平台自报', ours: false }
  }

  const node = asRec(rec['nodeJson'])
  const tag = String(node?.['tag'] ?? '?')
  const attrs = asRec(node?.['attrs'])
  const id = typeof attrs?.['id'] === 'string' ? attrs['id'] : ''
  const text = typeof rec['textContent'] === 'string' ? rec['textContent'].trim() : ''
  const key = id ? `${tag}#${id}` : `${tag}\u300c${text.slice(0, 16)}\u300d`

  const ours = key.includes(OUR_GLOBAL_PREFIX) || key.includes('recruitHelper')
  const source = ours ? '我们的'
    : id.startsWith('claude-') ? 'Claude in Chrome'
      : text === 'mmmmmmmmmmlli' ? '平台自己的字体探针'
        : '未知'
  return { key, source, ours }
}

function renderRaw(injected: readonly unknown[], probes: readonly string[], routine: readonly string[]): string {
  const merged = new Map<string, { source: string; ours: boolean; n: number }>()
  for (const item of injected) {
    const d = injectionDigest(item)
    const seen = merged.get(d.key)
    if (seen) seen.n += 1
    else merged.set(d.key, { source: d.source, ours: d.ours, n: 1 })
  }
  const rows = [...merged.entries()]
    .sort((a, b) => b[1].n - a[1].n)
    .map(([key, v]) => `<li${v.ours ? ' class="bad-row"' : ''}><code>${escapeHTML(key)}</code>`
      + ` x${v.n} <span class="muted">${escapeHTML(v.source)}</span></li>`)
    .join('')

  const tallied = tally(routine)
  return `<details><summary>注入检测报告 ${merged.size} 类 x${injected.length}</summary>`
    + (rows ? `<ul>${rows}</ul>` : '<p class="muted">无</p>')
    + '</details>'
    + `<details><summary>被探到的本机端口 ${probes.length} 个</summary>${list(probes)}</details>`
    + `<details><summary>例行码 ${tallied.length} 类</summary>${list(tallied)}</details>`
}


async function render(): Promise<void> {
  const entries = await readAll(storage, KIND_UPLOAD) as TelemetryEntry[]
  const shots = await readAll(storage, KIND_CLICK)

  const hits: { code: string; action: string }[] = []
  const routine: string[] = []
  const globals = new Set<string>()
  const injected: unknown[] = []
  const probes = new Set<string>()
  let unparsed = 0

  for (const entry of entries) {
    if (entry.raw !== undefined || entry.parseError !== undefined) unparsed += 1
    const c = classifyBossEntry(entry.url, entry.payload)
    for (const h of c.hits) hits.push(h)
    for (const r of c.routine) routine.push(`${r.code}(${bossCodeMeaning(r.code).label})`)
    for (const g of c.unknownGlobals) globals.add(g)
    for (const i of c.injected) injected.push(i)
    for (const p of c.localProbes) probes.add(p)
  }

  const names = [...globals].sort()
  const ours = names.filter((n) => n.startsWith(OUR_GLOBAL_PREFIX))
  const unknownCodes = [...new Set(hits.map((h) => h.code))].filter((c) => !bossCodeMeaning(c).known).sort()
  const over = overLineCount(shots)
  const severe = bossSevereHits(hits)

  const first = entries[0]?.at
  const last = entries[entries.length - 1]?.at
  const span = first && last
    ? `${new Date(first).toLocaleString('zh-CN')} — ${new Date(last).toLocaleString('zh-CN')}`
    : '-'
  el('dataline').innerHTML = entries.length
    ? `<b>${entries.length}</b> 条 · ${escapeHTML(span)} · 解析失败 <b>${unparsed}</b> 条`
    : '<span class="muted">还没抓到任何载荷。打开平台页面走一走,再回来刷新。</span>'

  el('verdict').innerHTML = renderVerdict(
    entries.length > 0, ours, names.length - ours.length, shots.length, over, unknownCodes, severe)
  el('clicks').innerHTML = renderClicks(shots)
  el('globals').innerHTML = renderGlobals(names)
  el('hits').innerHTML = renderHits(hits)
  el('raw').innerHTML = renderRaw(injected, [...probes].sort(), routine)
}

function saveJSON(filename: string, data: unknown): void {
  const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

function download(): void {
  void (async () => {
    saveJSON(`telemetry-${Date.now()}.json`, {
      exportedAt: new Date().toISOString(),
      uploads: await readAll(storage, KIND_UPLOAD),
      clicks: await readAll(storage, KIND_CLICK),
    })
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

// ---- 请求录制 ----
//
// 状态住在 SW(录制跨着弹窗的开关,弹窗只是个看板);记录住在分片环,弹窗直接读。
// 录制中每秒问一次 SW;不在录制时不轮询——这是看板的刷新,不是手的业务定时器。

function clock(ms: number): string {
  return new Date(ms).toLocaleTimeString('zh-CN')
}

function renderCaptureStatus(s: CaptureStatus): string {
  if (s.unavailable !== undefined) return `<b>不可用</b>:${escapeHTML(s.unavailable)}`
  if (s.state === null) return '还没录过。'
  const st = s.state
  if (s.active) {
    const left = Math.max(0, Math.ceil((st.until - s.now) / 1000))
    const mm = String(Math.floor(left / 60)).padStart(2, '0')
    const ss = String(left % 60).padStart(2, '0')
    return `<b>录制中</b>,剩 ${mm}:${ss} · 已落盘 <b>${s.recorded}</b> 条,在途 ${s.pending} 条 · 开始于 ${clock(st.startedAt)}`
  }
  return `已结束:${clock(st.startedAt)} — ${clock(st.endedAt ?? st.until)} · 共 <b>${s.recorded}</b> 条`
}

function tallyOf(values: readonly string[], limit: number): string {
  const lines = tally(values).slice(0, limit)
  return list(lines)
}

/** 只回答"这十分钟平台页面往哪发了什么":按主机、类型、状态归并,明细看导出。 */
function renderCaptureSummary(records: readonly RequestRecord[]): string {
  if (!records.length) return '<span class="muted">无记录。</span>'

  const hosts: string[] = []
  const types: string[] = []
  const statuses: string[] = []
  const errors: string[] = []
  let unfinished = 0
  let localProbes = 0
  let withBody = 0
  for (const r of records) {
    hosts.push(hostOf(r.url) ?? '(解不出主机)')
    types.push(r.type)
    if (r.error !== undefined) errors.push(r.error)
    else if (r.statusCode !== undefined) statuses.push(`${Math.floor(r.statusCode / 100)}xx`)
    if (r.unfinished) unfinished += 1
    const host = hostOf(r.url)
    if (host === '127.0.0.1' || host === 'localhost' || host === '[::1]') localProbes += 1
    if (r.body !== undefined) withBody += 1
  }
  const first = records[0].at
  const last = records[records.length - 1].at

  return `<p class="muted"><b>${records.length}</b> 条 · ${escapeHTML(clock(first))} — ${escapeHTML(clock(last))}`
    + ` · 带请求体 ${withBody} 条 · 出错 ${errors.length} 条 · 未收尾 ${unfinished} 条`
    + (localProbes ? ` · <b class="bad-row">打到本机端口 ${localProbes} 条</b>` : '') + '</p>'
    + `<details open><summary>目的主机 ${new Set(hosts).size} 个</summary>${tallyOf(hosts, 20)}</details>`
    + `<details><summary>资源类型 ${new Set(types).size} 种</summary>${tallyOf(types, 20)}</details>`
    + `<details><summary>状态 ${new Set(statuses).size} 档</summary>${tallyOf(statuses, 10)}</details>`
    + (errors.length ? `<details><summary>错误 ${new Set(errors).size} 种</summary>${tallyOf(errors, 10)}</details>` : '')
}

let capturePoll: ReturnType<typeof setTimeout> | null = null

async function refreshCapture(): Promise<void> {
  if (capturePoll !== null) {
    clearTimeout(capturePoll)
    capturePoll = null
  }
  const s = await ask<CaptureStatus>({ type: 'netCapture:status' })
  el('capStatus').innerHTML = renderCaptureStatus(s)
  if (s.active) {
    el('capSummary').innerHTML = '<span class="muted">录制中,结束后显示汇总。</span>'
    capturePoll = setTimeout(() => { void refreshCapture() }, 1000)
    return
  }
  const records = await readAll(storage, KIND_REQUEST) as RequestRecord[]
  el('capSummary').innerHTML = renderCaptureSummary(records)
}

el('capStart').addEventListener('click', () => {
  void (async () => {
    await ask<CaptureStatus>({ type: 'netCapture:start' })
    el('status').textContent = '录制已开始'
    await refreshCapture()
  })()
})
el('capStop').addEventListener('click', () => {
  void (async () => {
    await ask<CaptureStatus>({ type: 'netCapture:stop' })
    el('status').textContent = '录制已停止'
    await refreshCapture()
  })()
})
el('capExport').addEventListener('click', () => {
  void (async () => {
    const s = await ask<CaptureStatus>({ type: 'netCapture:status' })
    saveJSON(`requests-${Date.now()}.json`, {
      exportedAt: new Date().toISOString(),
      capture: s.state,
      requests: await readAll(storage, KIND_REQUEST),
    })
  })()
})
el('capClear').addEventListener('click', () => {
  void (async () => {
    const s = await ask<CaptureStatus>({ type: 'netCapture:status' })
    if (s.active) {
      el('status').textContent = '录制中不能清空,先停止'
      return
    }
    await clear(storage, KIND_REQUEST)
    el('status').textContent = '请求记录已清空'
    await refreshCapture()
  })()
})

void render()
void refreshCapture()
