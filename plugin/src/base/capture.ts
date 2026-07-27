// 截图与 blob 上行的 SW 基础设施(base):chrome.tabs.captureVisibleTab 只能在
// service worker 调用;blob 端点与会话作用域 token 来自当次 welcome(协议规格 §13
// 上行子集)。本模块不注册任何 chrome 监听(宪法禁令 5),也不含业务决策。
import { BlobParams } from './protocol'

// 会话作用域 blob 参数:welcome 写入、新一次握手清空。缺席=本会话未协商 blob,
// 依赖它的原语必须在执行前响亮失败(PAYLOAD_LIMIT),禁止内联或截断替代。
let activeBlobParams: BlobParams | null = null

export function setSessionBlobParams(params: BlobParams | null | undefined): void {
  activeBlobParams = params ?? null
}

export function sessionBlobParams(): Readonly<BlobParams> | null {
  return activeBlobParams
}

// captureVisibleTab 一帧:返回 JPEG data URL。只截指定窗口当前活动标签的可见区,
// 配额限速(约 1 次/秒)超限会 reject,由调用方决定等待重试还是放弃。
export async function captureVisibleTabJpegDataUrl(windowId: number, quality: number): Promise<string> {
  return await new Promise<string>((resolve, reject) => {
    try {
      chrome.tabs.captureVisibleTab(windowId, { format: 'jpeg', quality }, (dataUrl) => {
        const lastError = chrome.runtime.lastError
        if (lastError) {
          reject(new Error(lastError.message || 'captureVisibleTab failed'))
          return
        }
        if (!dataUrl) {
          reject(new Error('captureVisibleTab returned empty'))
          return
        }
        resolve(dataUrl)
      })
    } catch (error) {
      reject(error instanceof Error ? error : new Error(String(error)))
    }
  })
}

export interface BlobPutOutcome {
  ref: string
  byteSize: number
}

export class BlobChannelError extends Error {
  constructor(message: string, readonly permanent: boolean) {
    super(message)
    this.name = 'BlobChannelError'
  }
}

// 按 §13 内容寻址上行:SW 内算 sha256 → PUT {endpoint}/{ref} → 脑逐字节复核。
// 不重试(整段截图流程的重拍策略在调用方);任何失败响亮抛出,绝不静默截断。
export async function putSessionBlob(bytes: ArrayBuffer): Promise<BlobPutOutcome> {
  const params = activeBlobParams
  if (!params) {
    throw new BlobChannelError('当前会话未协商 blob 通道', true)
  }
  if (bytes.byteLength <= 0) {
    throw new BlobChannelError('blob 内容为空', true)
  }
  if (bytes.byteLength > params.maxBytes) {
    throw new BlobChannelError(`blob 超过会话上限(${bytes.byteLength} > ${params.maxBytes})`, true)
  }
  const digest = await crypto.subtle.digest('SHA-256', bytes)
  const hexDigest = Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('')
  const ref = `sha256:${hexDigest}`
  const endpoint = params.endpoint.replace(/\/+$/u, '')
  let response: Response
  try {
    response = await fetch(`${endpoint}/${ref}`, {
      method: 'PUT',
      headers: {
        Authorization: `Bearer ${params.token}`,
        'Content-Type': 'application/octet-stream',
      },
      body: bytes,
    })
  } catch (error) {
    throw new BlobChannelError(
      `blob 上行网络失败:${error instanceof Error ? error.message : String(error)}`,
      false,
    )
  }
  if (!response.ok) {
    // 401(token 换代)与 5xx 属可恢复;4xx 其余按协议错误处理,同样响亮失败。
    throw new BlobChannelError(`blob 上行被拒:HTTP ${response.status}`, response.status !== 401 && response.status < 500)
  }
  return { ref, byteSize: bytes.byteLength }
}
