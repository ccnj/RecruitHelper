// 临时只读诊断探针(2026-08-28 加入)。
//
// 要回答的事实:**扩展 origin 发起的同源 fetch,BOSS 服务端认不认。**
// 扩展/content script 的请求带 `Origin: chrome-extension://<id>` 而非页面 origin,
// 页面内的 JS 永远拿不到这个事实(它自己就是页面 origin),所以按 AGENTS.md
// 「页面事实发现顺序」允许的最小只读临时探针路径加入。
//
// 它决定 BOSS 适配器的取数架构:通,则消息身份(mid)、精确毫秒、bizType 与
// DOM 漏掉的整行都能从公开 HTTP 接口拿到,完全不必碰 MAIN world;
// 不通,才退到 MAIN world 一次性读取 Vue 内部(高脆)。
//
// 纪律:只发 GET,不写不改,不参与任何业务裁决,结果只落 chrome.storage.local
// 供同机诊断页显示。**结论拿到后整文件删除**,不得残留为 capability 或契约表面。

const RESULT_KEY = 'bossOriginProbe:result'
/** 可选:同机诊断页可写入一个 gid,让探针额外打一次真实的消息历史接口。 */
const GID_KEY = 'bossOriginProbe:gid'

export interface ProbeRow {
  label: string
  url: string
  ok: boolean
  httpStatus?: number
  bizCode?: number
  bizMessage?: string
  /** zpData 序列化后的字节数——用来区分「真取到数据」与「200 但空壳」。 */
  payloadBytes?: number
  error?: string
}

export interface ProbeResult {
  at: number
  extensionOrigin: string
  rows: ProbeRow[]
}

async function probeOne(label: string, url: string): Promise<ProbeRow> {
  try {
    const res = await fetch(url, { credentials: 'include' })
    let body: unknown = null
    try {
      body = await res.json()
    } catch {
      return { label, url, ok: false, httpStatus: res.status, error: '响应不是 JSON' }
    }
    const j = body as { code?: number; message?: string; zpData?: unknown }
    const bytes = JSON.stringify(j.zpData ?? null).length
    return {
      label,
      url,
      ok: res.ok && j.code === 0 && bytes > 2,
      httpStatus: res.status,
      bizCode: j.code,
      bizMessage: j.message,
      payloadBytes: bytes,
    }
  } catch (error) {
    return { label, url, ok: false, error: String(error).slice(0, 200) }
  }
}

export async function runBossOriginProbe(): Promise<ProbeResult> {
  const rows: ProbeRow[] = []

  // 基准:无参数、要登录、返回本账号自己的职位列表(不含任何候选人信息)。
  // 未登录或被 origin 拒绝时,它不会返回 code=0 且有内容的载荷。
  rows.push(
    await probeOne('职位列表(无参数,验登录态)', 'https://www.zhipin.com/wapi/zpjob/interview/job/list'),
  )

  // 可选:真正关心的那个接口。gid 不写进仓库,由诊断页在本机填。
  const stored = await chrome.storage.local.get(GID_KEY)
  const gid = stored[GID_KEY]
  if (typeof gid === 'string' && /^\d+$/.test(gid)) {
    rows.push(
      await probeOne(
        '消息历史(historyMsg)',
        `https://www.zhipin.com/wapi/zpchat/boss/historyMsg?src=0&gid=${gid}&maxMsgId=0&c=1&page=1`,
      ),
    )
  }

  const result: ProbeResult = {
    at: Date.now(),
    extensionOrigin: `chrome-extension://${chrome.runtime.id}`,
    rows,
  }
  await chrome.storage.local.set({ [RESULT_KEY]: result })
  return result
}

export async function readBossOriginProbe(): Promise<ProbeResult | null> {
  const stored = await chrome.storage.local.get(RESULT_KEY)
  return (stored[RESULT_KEY] as ProbeResult | undefined) ?? null
}

export async function setBossOriginProbeGid(gid: string): Promise<void> {
  await chrome.storage.local.set({ [GID_KEY]: gid })
}
