// 观测站点表:哪个平台把埋点发到哪个端点、载荷里哪几样要紧。
//
// 这是**平台知识**,所以住在 program。base 只管机制(请求体还原、深搜、落盘),
// 监听注册仍在 base——手的禁令第 5 条,program 不注册任何 chrome 监听。
//
// 站点表和 sites.ts 是两件事:那张表管"这是哪个平台的页、登录态怎么读",
// 是业务感知;这张表管"平台自己往外发什么",是观测,不参与任何业务裁决。

import { EMPTY_DIGEST, TelemetryDigest, deepFind } from '../../base/telemetry/capture'

export interface TelemetrySite {
  readonly id: string
  /** `chrome.webRequest` 过滤器的 urls。 */
  readonly urls: readonly string[]
  /** 从解析后的载荷抽摘要与轨迹窗口。形状不认识就返回空,不猜。 */
  digest(payload: unknown, at: number): TelemetryDigest
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

/**
 * 平台在 `it.p` 里放的是一个 JSON 字符串,其中 `time` 是**平台自己的时刻**。
 * 取不到就退回本次捕获时刻——差的是几十毫秒,不值得为它丢一条轨迹。
 */
function platformTime(item: Record<string, unknown>, fallback: number): number {
  if (typeof item['p'] !== 'string') return fallback
  try {
    const parsed = asRecord(JSON.parse(item['p']))
    const time = Number(parsed?.['time'])
    return Number.isFinite(time) ? time : fallback
  } catch {
    return fallback
  }
}

/**
 * BOSS 直聘。端点与字段取自 hiBoss 的逆向结论(`report/runtime-evidence.md` §12/§13),
 * 我方**尚未真机见过任何一条载荷**——所以这里只抽两样最要紧的,其余原样落盘。
 *
 * - `p2` 是 zpAegis 通道的事件码(设备指纹上报本身、行为异常命中都在这里)
 * - `cnTextCount` 属于 inputTrait,**只在载荷里、本地账本根本没有**
 * - `web-event-click` 且 `p6` 是对象的那些 item 带完整鼠标轨迹,单独进轨迹环,
 *   免得被页面加载噪声(单条几十 KB 的性能上报)挤掉
 */
export const bossTelemetrySite: TelemetrySite = {
  id: 'boss',
  urls: ['https://apm-fe.zhipin.com/wapi/zpApm/actionLog/*'],
  digest(payload, at) {
    const root = asRecord(payload)
    if (!root) return EMPTY_DIGEST

    const shots: unknown[] = []
    const items = root['items']
    if (Array.isArray(items)) {
      for (const raw of items) {
        const item = asRecord(raw)
        if (!item) continue
        if (item['action'] !== 'web-event-click') continue
        if (asRecord(item['p6']) === null) continue
        shots.push({
          at,
          t: platformTime(item, at),
          p6: item['p6'],
          p4: item['p4'],
          p7: item['p7'],
          p8: item['p8'],
        })
      }
    }

    return {
      summary: {
        codes: deepFind(payload, 'p2').map(String),
        cnTextCount: deepFind(payload, 'cnTextCount'),
      },
      shots,
    }
  },
}

/** 全部观测站点。加一个平台要动两处:本表,和 manifest 的 host_permissions。 */
export const telemetrySites: readonly TelemetrySite[] = [bossTelemetrySite]

// ── 判读:一条载荷里哪些是例行、哪些是探测命中 ──────────────────────────────
//
// 判读表取自 hiBoss 的逆向结论(`report/boss-detection-report.md` §354/§692),
// 我方**尚未真机核对过任何一条**。分类只看 (端点, action),不看码长什么样。

/** zpAegis 通道:`p2` 是事件码;patas APM 通道的 `p2` 是页面 URL,码藏在别处。 */
const AEGIS_ENDPOINT = /\/actionLog\/fe\/ie\/common\.json/

/** 行为通道里的良性码。 */
// 470000 是 C 端每次点击都发的基线码(action `web-event-click-geek`),与 B 端 click 的
// p2=0 同位,不是命中 —— 2026-09-02 三次导出里它每次回首页都在。
// 30001/30002/30003 与 30004/30005 同族:zpAegis `getInputCode` 对每次编辑器输入打的分类
// (EMOJI/PHRASE/PASTE/TYPING/ENTER),WASM 侧只把 TYPING/ENTER 滤掉、其余累进 input_count,
// 都不是探测命中。2026-09-09 本机 10:08 真人手打一句带表情的话,30001 被面板报成「新东西」。
const ROUTINE_BEHAVIOR = new Set(['0', '30001', '30002', '30003', '30004', '30005', '30006', '470000'])

/** 设备指纹上报本身——每次页面加载无条件发,跟检测到什么无关。 */
const FINGERPRINT = new Set(['800001', '800003', '800009'])

export interface BossCodeMeaning {
  readonly code: string
  /** 一句话说明这个码报的是什么。不认识的码原样回显,不编。 */
  readonly label: string
  /**
   * 在 hiBoss 的码表里查得到吗。
   *
   * 查不到就是**平台出了我们没见过的东西**——要么它改版了,要么我们碰到了新分支。
   * 这值得单独抬头看,所以做成布尔位;靠比对 label 文案来判是脆的。
   */
  readonly known: boolean
  /**
   * 近乎每台机器都会报,**不是探到了东西**。
   *
   * 本机端口探测走 `si()`,而它的回调是 `onopen = onclose = onerror`,
   * 参数是「**1 秒内有反应**」而不是「连上了」(实证:`sec370.clean.js:2136-2147`)。
   * localhost 上端口关着会瞬间 ECONNREFUSED —— 照样算真。所以这一族码在
   * 任何一台机器上都会亮,判据本身有问题。
   */
  readonly nearUniversal?: boolean
  /**
   * 一亮就说明平台掌握了自动化或工具痕迹的证据,值得抬头。
   *
   * 「新东西」那行只问码表里有没有;一个码补进码表后就从那行消失,只剩明细里一条
   * (2026-09-03 的 700051 正是这样),结论区因此三个绿勾。第四行「踩雷」看的是这一位。
   * 名单由甲方 2026-09-03 裁定:工具痕迹、真探到的本机端口(不是"1 秒内有反应"那族)、
   * 绕开 isTrusted 的点击行为族(含平台自己豁免不计聚合的 700009/700001/005/007/013——
   * 我方 OS 注入的点击 isTrusted 为真,它们若亮就是 CDP 或调试路径漏出来了)、
   * 点击时帧率两条、559999。
   * 明确不列:559991(干净机器照发)、550094(第三方扩展)、99003(与「隐形」同源)、
   * 700028(真人首次点击也会)、910013/910015(反映环境不是行为,平台自己也豁免)。
   * 与 nearUniversal 互斥,测试钉死。
   */
  readonly severe?: boolean
}

/**
 * 事件码释义。取自 hiBoss `report/boss-detection-report.md` §4.2/§4.3 与
 * `evidence/deobfuscated/sec370.clean.js` 的码表(`co`/`za`),我方未逐条真机核对。
 *
 * `nearUniversal` 那一族是例外:判据语义由我方 2026-08-28 直接读反混淆源码确认。
 * 首页(C 端)那一族取自我方 2026-09-02 对 geek `index.js` 的解混淆,不在 hiBoss 的范围内。
 */
const CODE_MEANINGS: Record<string, Omit<BossCodeMeaning, 'code' | 'known'>> = {
  '0': { label: '无异常' },
  // 输入分类一族(sec-entry `EMOJI=30001, PHRASE=30002, PASTE=30003, TYPING=30004, ENTER=30005`)。
  // 点表情按钮那一下走的是 click 的 p3(`CLICK_EMOJI=20010`),不经 p2 判读路径,不在本表。
  '30001': { label: 'EMOJI·插入表情分类(2026-09-09 本机真人手打命中)' },
  '30002': { label: 'PHRASE·常用语分类;本机未见' },
  '30003': { label: 'PASTE·粘贴分类;本机未见' },
  '30004': { label: 'TYPING·正常打字分类' },
  '30005': { label: 'ENTER·回车分类' },
  '30006': { label: 'SDK 版本自报' },

  // 设备指纹上报本身:每次页面加载无条件发,跟检测到什么无关。
  '800001': { label: '设备指纹·内网 IP + 未知全局名差集 + 缺失 API 矩阵' },
  '800003': { label: '设备指纹·UA-CH/屏幕/GPU/canvas 哈希(注意:800001/800009 在聚合豁免表里,唯独它不在,会被算进 559991)' },
  '800009': { label: '设备指纹·语音合成深度指纹' },

  // 本机端口 / 伴随进程探测。整族都受 si() 那个判据缺陷影响。
  '550237': { label: '探本机 18789 端口(openGateway),1 秒内有反应', nearUniversal: true },
  '550238': { label: '探本机 18789 的 /__openclaw__/ping', severe: true },
  '550239': { label: '探本机 9222 端口(Chrome 默认远程调试口),1 秒内有反应', nearUniversal: true },
  '550241': { label: '探本机 8642 端口(hermes)', severe: true },
  '550243': { label: '探本机 opencli', severe: true },
  '550245': { label: '探本机 10086 端口(kimi),1 秒内有反应(含连接被拒)', nearUniversal: true },
  '550247': { label: '本机 10086 WebSocket 真正握手成功(kimi)', severe: true },
  '910013': { label: '探本机 35600 端口,五秒内毫无反应(不是拒绝,是没声音);p3 带 CDP 状态' },
  '900067': { label: '探到 AdsPower(20725)', severe: true },
  '900073': { label: '探到花漾(47326)', severe: true },
  '900075': { label: '探到 VMLogin(5100)', severe: true },

  // 扩展与自动化痕迹。
  '550091': { label: '页面上 chrome.runtime 可用,且被探的那个扩展 ID 确实装着', severe: true },
  // 2026-08-28 对照实验坐实:关掉 MetaMask 后这条当场消失。MetaMask 的
  // externally_connectable 写的是 `http://*/*` + `https://*/*`(对全互联网开放),
  // 所以任何装了它的机器都会触发这条。实测干净 Chrome 148 上
  // window.chrome 是 object 而 window.chrome.runtime 是 undefined,可见它确实
  // 只在有扩展对该域声明了 externally_connectable 时才出现。
  '550094': { label: '页面上 chrome.runtime 竟然可用——有扩展对该域声明了 externally_connectable(实测:MetaMask 对全站声明,装了就报)' },
  '550003': { label: 'CDP / Playwright 痕迹', severe: true },
  '800015': { label: 'devtools/CDP 探测命中(不可配置的 stack getter 被读)', severe: true },
  '800025': { label: 'navigator 原型链上有非原生实现', severe: true },
  '550013': { label: 'navigator 指纹串命中(userAgent,userAgent,sendBeacon)', severe: true },
  '99003': { label: 'Object.keys(window) 与白名单的差集(未知全局名)' },

  // 点击与轨迹的行为层(sec-370 `ou()` 与同一个 click 监听里的几条支线)。
  // **这一族绕开 `isTrusted`** —— 分支条件是 `!1 !== isTrusted`,只有显式 false 才跳过,
  // 所以我们经操作系统注入的点击(isTrusted 为真)照样走进来。合成点击真正会踩的就是它们。
  '700009': { label: 'isTrusted 检查', severe: true },
  '700001': { label: '点击的 pageX 或 pageY 为 0', severe: true },
  '700005': { label: '点击的 clientX 或 clientY <= 0', severe: true },
  '700007': { label: '点击的 pageX 或 pageY 为负', severe: true },
  '700013': { label: '点击目标的 DOM 路径就是 "html"(没落在任何具体元素上)', severe: true },
  '700028': { label: '页面加载后第一次点击:既没有上次落点,轨迹也是空的' },
  '700030': { label: '700009 二次升级:落在聊天区/牛人列表区(无节流)', severe: true },
  '700031': { label: '700009 二次升级:同上,50ms 节流版', severe: true },
  '700033': { label: '700009 二次升级:落在 menu-list', severe: true },
  '700035': { label: '700009 二次升级:落在 records-center', severe: true },
  '700044': { label: '累计 click 次数比累计 mousedown 多出 50 次以上,且落在聊天区', severe: true },
  // 2026-09-03 本机唯一一次命中(10:35:33,会话条目→消息筛选页签,跨 109x287、零采样、间隔
  // 3.17 秒),经甲方确认是 Chrome 插件的 CDP 点的,不是鼠标线。同日下午鼠标线的 60 余次
  // 点击零命中——每次几十到几百个轨迹点,`y=Σ|dx|` 非零,分支直接关掉。
  '700051': { label: '两次点击间光标横竖都跨过 54px 却零 mousemove,且落在聊天区(受限浏览器)', severe: true },
  '700052': { label: '同 700051,非受限浏览器', severe: true },
  '700053': { label: '跨距过线,且两次点击间的非零位移采样不足 2 个', severe: true },
  '700057': { label: '两次点击间光标横竖都跨过 54px 却零 mousemove(不要求聊天区与零耗时)', severe: true },
  '700061': { label: '匀速直线轨迹(样本>70、macOS UA 被排除);700053 分支里检出 CDP/Playwright 痕迹时也发这个码', severe: true },
  '700071': { label: '连续 10 次 700053,CLAW 未写入', severe: true },
  '700073': { label: '连续 10 次 700053,CLAW 已写入(18789 端口探测的产物)', severe: true },
  '761005': { label: '700051/052/057/053 累计每满 10 次追加一条(p4=累计数)', severe: true },
  '761011': { label: '同 761005,CDPSTATUS 为真', severe: true },
  '700017': { label: '点击时的帧率序列:低于 20 的样本超过 20 个且全部低于 20', severe: true },
  '700019': { label: '点击时的帧率序列:低于 20 的样本超过 20 个且全部低于 10', severe: true },
  '910015': { label: '帧率序列里落在 (20,40) 的样本超过 50 个' },

  // 首页(C 端)是另一套 SDK 与码表:sec-370 不在首页加载,首页走 zpAegis polyfill
  // 再动态加载 geek 的 index.js。hiBoss 只抓过 /web/chat/recommend,所以它的码表天然没有
  // 这一族。判据与 B 端同款(2026-09-02 三次导出比对确认),**与我们无关**。
  '410001': { label: 'C 端:ws 127.0.0.1:18789,1 秒内有反应(同 550237)', nearUniversal: true },
  '410003': { label: 'C 端:9222 端口,1 秒内有反应(同 550239)', nearUniversal: true },
  '410005': { label: 'C 端:ws://127.0.0.1:10086/ws,1 秒内有反应(同 550245)', nearUniversal: true },
  '410007': { label: 'C 端:10086 真正握手成功(同 550247);本机未见', severe: true },
  '410009': { label: 'C 端:fetch 127.0.0.1:8642/v1/health(同 550241);本机未见', severe: true },
  '400001': { label: 'C 端:探某个扩展的 options.js(WAR 探测);本机未见', severe: true },
  '470000': { label: 'C 端每次点击的基线码(p2 恒为 47e4,带坐标/xpath/轨迹),等价于 B 端 click 的 p2=0' },
  '470001': { label: 'C 端点击:isTrusted 为假;本机未见', severe: true },
  '470003': { label: 'C 端点击:零坐标;本机未见', severe: true },
  '470005': { label: 'C 端点击:负坐标;本机未见', severe: true },
  '470007': { label: 'C 端点击:轨迹是直线;本机未见', severe: true },
  '470009': { label: 'C 端点击:轨迹点太少;本机未见', severe: true },

  // 聚合补报。
  // 2026-08-28 两次真机 + 一次对照实验。第一次聚合的是
  // 800003 + 550094 + 550237/550239/550245;关掉 MetaMask(550094 的唯一来源)后
  // 重测,它**照样发**,聚合成 800003 + 550237/550239/550245 —— 也就是说
  // **一台没有任何可疑扩展的干净机器,仍然会触发 BOSS 的聚合告警**,
  // 而构成它的全是 BOSS 自己的例行指纹上报加自己那族坏掉的端口探测。
  //
  // 这与 hiBoss「559991/559999 都需 window.Block 为真(已进入封禁/拦截场景)才发」
  // 的读法对不上:两次都是普通浏览,无任何封禁迹象。要么该标志在正常会话里也为真、
  // 根本不是封禁位,要么那条前置读得不全 —— 待查。按文档信任边界,以我方真机为准。
  '559991': { label: '聚合补报:累计命中 >2 个不同的非豁免码(p3=码列表、p4=数量)。真机首验:普通会话即会触发' },
  '559999': { label: '聚合补报:命中 Sa 表', severe: true },
}

export function bossCodeMeaning(code: string): BossCodeMeaning {
  const found = CODE_MEANINGS[code]
  return found
    ? { code, ...found, known: true }
    : { code, label: '未知码(hiBoss 的码表里没有,原样记下)', known: false }
}

/** 码表里的全部码。给测试钉不变量用(severe 与 nearUniversal 互斥),不参与判读。 */
export function bossKnownCodes(): readonly string[] {
  return Object.keys(CODE_MEANINGS)
}

export interface BossSevereHit {
  readonly code: string
  readonly n: number
  readonly label: string
}

/**
 * hits 里的高风险码,按码聚合、次数多的在前。结论区「踩雷」那行只看这个。
 *
 * 只认 `severe`,不管 known:码表里没有的码归「新东西」那行,两行各答各的,
 * 一个码不会同时出现在两行。
 */
export function bossSevereHits(hits: readonly { code: string }[]): BossSevereHit[] {
  const counts = new Map<string, number>()
  for (const h of hits) {
    if (bossCodeMeaning(h.code).severe) counts.set(h.code, (counts.get(h.code) ?? 0) + 1)
  }
  return [...counts.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([code, n]) => ({ code, n, label: bossCodeMeaning(code).label }))
}

export function bossCodeLabel(code: string): string {
  const meaning = bossCodeMeaning(code)
  return meaning.label === '' ? code : `${code}(${meaning.label})`
}

export interface BossClassification {
  /** 非例行的事件码——**这些才是探测命中**。 */
  readonly hits: readonly { code: string; action: string }[]
  /** 例行码:指纹上报本身、正常打字分类。 */
  readonly routine: readonly { code: string; action: string }[]
  /**
   * 800001 的 `p6`:BOSS 拿 `Object.keys(window)` 与自己那份白名单求的差集,
   * **原样上送**。我们的插件有没有被点名,看的就是这一栏。
   */
  readonly unknownGlobals: readonly string[]
  /** risk-monitor 报告的注入项。 */
  readonly injected: readonly unknown[]
  /** 被探到的本机端口。 */
  readonly localProbes: readonly string[]
}

const EMPTY_CLASSIFICATION: BossClassification = {
  hits: [], routine: [], unknownGlobals: [], injected: [], localProbes: [],
}

export function classifyBossEntry(url: string, payload: unknown): BossClassification {
  const root = asRecord(payload)
  const items = root?.['items']
  if (!Array.isArray(items)) return EMPTY_CLASSIFICATION

  const aegis = AEGIS_ENDPOINT.test(url)
  const hits: { code: string; action: string }[] = []
  const routine: { code: string; action: string }[] = []
  const unknownGlobals: string[] = []
  const injected: unknown[] = []
  const localProbes: string[] = []

  for (const raw of items) {
    const item = asRecord(raw)
    if (!item) continue
    const action = String(item['action'] ?? '')

    if (aegis) {
      const code = String(item['p2'] ?? '')
      if (code === '') continue
      const isDevice = action === 'device-action-report'
      // 2026-09-02 Windows 真机导出:800001 还会挂在 `web-action-heartbeat` 下再发一遍,
      // 字段是同一套指纹(p6=UA-CH brands、p7/p8 哈希、p9 UA),只是少了 p4/p5。
      // 它是同一份指纹的心跳重发,不是探测命中——此前只认 device-action-report,
      // 于是 15 条指纹被摆进「探测命中」,与空状态那句"指纹算例行"自相矛盾。
      const isFingerprintChannel = isDevice || action === 'web-action-heartbeat'
      const benign = isFingerprintChannel ? FINGERPRINT.has(code) : ROUTINE_BEHAVIOR.has(code)
      ;(benign ? routine : hits).push({ code, action })
      // 全局名差集**只从 device-action-report 的 p6 取**:心跳版的 p6 是 UA-CH brands
      // 串(`Not=A?Brand=99|Google Chrome=151|…`),按 | 切开会变成三个假"未知全局名"。
      if (isDevice && code === '800001' && typeof item['p6'] === 'string' && item['p6'] !== 'null') {
        unknownGlobals.push(...item['p6'].split('|').filter(Boolean))
      }
      continue
    }

    if (action === 'action_js_risk_monitor') {
      const parsed = typeof item['p7'] === 'string' ? tryParse(item['p7']) : null
      const list = asRecord(parsed)?.['insertList']
      if (Array.isArray(list)) injected.push(...list)
    } else if (action === 'action_api_monitor') {
      const parsed = asRecord(typeof item['p4'] === 'string' ? tryParse(item['p4']) : null)
      const probed = parsed?.['url']
      if (typeof probed === 'string' && /^https?:\/\/(127\.0\.0\.1|localhost)[:/]/.test(probed)) {
        localProbes.push(probed)
      }
    }
  }

  return { hits, routine, unknownGlobals, injected, localProbes }
}

function tryParse(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    // 形状变了 —— 原文已经完整落在 payload 里,不猜。
    return null
  }
}
