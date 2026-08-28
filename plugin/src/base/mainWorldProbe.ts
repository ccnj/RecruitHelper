// 临时只读诊断探针(2026-08-28)。
//
// 要回答四件事,全部关于「MAIN world 一次性读会不会被 BOSS 发现」:
//   A. executeScript({world:'MAIN'}) 会不会往页面 window 上留下全局
//      —— BOSS 拿 Object.keys(window) 差集白名单,上报码 99003
//   B. 注入的执行会不会出现在页面的 performance.getEntries() 里
//      —— BOSS 跑着真的 APM SDK(apm-fe.zhipin.com),这是它的视野
//   C. 按数据形状搜整棵 Vue 树要多久,对比页面自己的 longtask
//      —— BOSS 把 rAF 帧率记进 __local__sec__store___
//   D. files 形式与 func 形式在 A/B 上有没有差别
//
// 只读,不写页面、不改页面、不参与任何业务裁决。结论拿到后整文件删除。

export interface MainWorldShot {
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

export interface MainWorldProbeResult {
  at: number
  tabUrl: string
  shots: MainWorldShot[]
  /** A:第二枪相对第一枪多出来的全局名。空 = 注入不留残渣。 */
  globalsAddedBetweenShots: string[]
  /** B/D:files 形式注入之后,performance 上出现的扩展相关条目。 */
  afterFilesInjection: { perfExtensionHits: { name: string; type: string }[]; globalsAdded: string[] } | null
  notes: string[]
}

/**
 * 被注入进 MAIN world 的取样函数。**必须自包含**——它会被序列化后在页面里求值,
 * 不能闭包捕获模块作用域的任何东西。
 */
function shot(): MainWorldShot {
  const out: MainWorldShot = {
    walkMs: 0,
    visitedComponents: 0,
    foundMessageArrayLen: -1,
    globalCount: 0,
    globals: [],
    perfCount: 0,
    perfExtensionHits: [],
    perfTypes: {},
    longTasks: [],
  }
  try {
    // C:按「元素带 mid/from/body 的数组」这个数据形状搜整棵 Vue 树,
    //    不认组件名——组件名会改,协议形状不会(它和 HTTP 接口是同一套)。
    const t0 = performance.now()
    const rootEl = document.querySelector('#wrap') as (Element & { __vue__?: unknown }) | null
    const root = rootEl && rootEl.__vue__
    if (root) {
      const seen = new Set<unknown>()
      const stack: any[] = [root]
      while (stack.length) {
        const vm = stack.pop()
        if (!vm || seen.has(vm)) continue
        seen.add(vm)
        out.visitedComponents++
        const bag: Record<string, unknown> = vm._data || {}
        const names = Object.keys(bag).concat(
          Object.keys(vm).filter((k) => k.endsWith('$')),
        )
        for (const k of names) {
          let v: unknown
          try {
            v = vm[k]
          } catch {
            continue
          }
          if (Array.isArray(v) && v.length && v[0] && typeof v[0] === 'object') {
            const e = v[0] as Record<string, unknown>
            // 判据要同时容得下两种形状:HTTP 线上的 from/to 是对象,
            // Vue 归一化后是扁平的 fromId/toId。只认线上形状会搜不到。
            if ('mid' in e && 'body' in e && ('from' in e || 'fromId' in e) && v.length > out.foundMessageArrayLen) {
              out.foundMessageArrayLen = v.length
            }
          }
        }
        const kids = vm.$children
        if (kids) for (const c of kids) stack.push(c)
      }
    }
    out.walkMs = Math.round((performance.now() - t0) * 1000) / 1000

    // A
    const g = Object.keys(window)
    out.globalCount = g.length
    out.globals = g

    // B
    const entries = performance.getEntries()
    out.perfCount = entries.length
    for (const e of entries) {
      out.perfTypes[e.entryType] = (out.perfTypes[e.entryType] || 0) + 1
      const n = String((e as PerformanceEntry & { name?: string }).name || '')
      if (n.includes('chrome-extension') || n.includes('oankodckocoibcofboconjjeinpjpdnb')) {
        out.perfExtensionHits.push({ name: n.slice(0, 160), type: e.entryType })
      }
      if (e.entryType === 'longtask') {
        out.longTasks.push({ start: Math.round(e.startTime), dur: Math.round(e.duration) })
      }
    }
  } catch (error) {
    out.error = String(error).slice(0, 200)
  }
  return out
}

async function inject(tabId: number): Promise<MainWorldShot> {
  const [res] = await chrome.scripting.executeScript({
    target: { tabId },
    world: 'MAIN',
    func: shot,
  })
  return res.result as MainWorldShot
}

export async function probeMainWorld(): Promise<MainWorldProbeResult> {
  const tabs = await chrome.tabs.query({ url: ['https://*.zhipin.com/*'] })
  const tab = tabs[0]
  const notes: string[] = []
  if (!tab || tab.id === undefined) {
    return {
      at: Date.now(),
      tabUrl: '',
      shots: [],
      globalsAddedBetweenShots: [],
      afterFilesInjection: null,
      notes: ['没找到打开着的 zhipin.com 标签页,先把 BOSS 页面打开再跑。'],
    }
  }

  const shots: MainWorldShot[] = []
  // 连打两枪:第二枪相对第一枪的全局差集,就是「注入自己留下的残渣」。
  shots.push(await inject(tab.id))
  shots.push(await inject(tab.id))
  const a = new Set(shots[0].globals)
  const globalsAddedBetweenShots = shots[1].globals.filter((k) => !a.has(k))

  // D:换 files 形式再来一次,单独看它会不会产生 resource 条目。
  let afterFilesInjection: MainWorldProbeResult['afterFilesInjection'] = null
  try {
    await chrome.scripting.executeScript({
      target: { tabId: tab.id },
      world: 'MAIN',
      files: ['mainWorldProbe.js'],
    })
    const third = await inject(tab.id)
    shots.push(third)
    const b = new Set(shots[1].globals)
    afterFilesInjection = {
      perfExtensionHits: third.perfExtensionHits,
      globalsAdded: third.globals.filter((k) => !b.has(k)),
    }
  } catch (error) {
    notes.push(`files 形式注入失败: ${String(error).slice(0, 160)}`)
  }

  if (shots[0] && shots[0].longTasks.length === 0) {
    notes.push('longtask 列表是空的 —— 页面自己没注册 longtask 的 PerformanceObserver(buffered),' +
      '拿不到历史 longtask。要对比就先在页面里注册一个观察者再跑。')
  }

  return {
    at: Date.now(),
    tabUrl: tab.url ?? '',
    shots,
    globalsAddedBetweenShots,
    afterFilesInjection,
    notes,
  }
}

/**
 * 甲方不在电脑前时的取结论路径。**不用 setTimeout** —— MV3 的 SW 随时可能被回收,
 * 定时器不可靠;直接在模块加载时跑一次,SW 每次重启(含 debug.reload)都会重跑。
 *
 * 结果有两个出口:
 *   1. 最后一步**故意**往页面写 window.__bossProbeResult —— 这是阳性对照。
 *      一个刻意不留痕的探针没法自证跑过,所以要有个确定会留痕的标记。
 *      它写在全部测量之后,不污染测量。
 *   2. handLog 回脑写进 brain.log(可能因连接未就绪而丢,只当附赠)。
 */
export async function runProbeAtBoot(
  report: (level: 'warn' | 'error', code: string, message: string, detail?: string) => void,
  runOriginProbe: () => Promise<unknown>,
): Promise<void> {
  const payload: Record<string, unknown> = { at: Date.now() }
  try {
    const r = await probeMainWorld()
    payload.mainWorld = r
    if (r.shots.length) {
      const s0 = r.shots[0]
      const filesHits = r.afterFilesInjection ? r.afterFilesInjection.perfExtensionHits.length : -1
      const filesGlobals = r.afterFilesInjection ? r.afterFilesInjection.globalsAdded.length : -1
      payload.verdict =
        r.globalsAddedBetweenShots.length === 0 &&
        s0.perfExtensionHits.length === 0 &&
        filesHits === 0 &&
        filesGlobals === 0
          ? 'clean'
          : 'traces'
    }
  } catch (error) {
    payload.mainWorldError = String(error).slice(0, 400)
  }

  try {
    payload.origin = await runOriginProbe()
  } catch (error) {
    payload.originError = String(error).slice(0, 400)
  }

  // 阳性对照:最后写标记。放在全部测量之后,measurements 不受影响。
  try {
    const tabs = await chrome.tabs.query({ url: ['https://*.zhipin.com/*'] })
    const tabId = tabs[0] && tabs[0].id
    if (tabId !== undefined) {
      await chrome.scripting.executeScript({
        target: { tabId },
        world: 'MAIN',
        func: (data: unknown) => {
          // **必须不可枚举** —— BOSS 拿 Object.keys(window) 差集白名单上报 99003,
          // 一个普通赋值会被它当成未知全局带出去(2026-08-28 实测:全局数 332→333)。
          // defineProperty + enumerable:false 之后照样能按名字读到,但不进 keys。
          Object.defineProperty(window, '__bossProbeResult', {
            value: data,
            enumerable: false,
            configurable: true,
            writable: true,
          })
        },
        args: [payload],
      })
    }
  } catch (error) {
    payload.markerError = String(error).slice(0, 200)
  }

  try {
    report('warn', 'tempProbeBoot', '临时探针结果', JSON.stringify(payload).slice(0, 4000))
  } catch {
    // 报不出去就算了,标记那条路够用。
  }
}
