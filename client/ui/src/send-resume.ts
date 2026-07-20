// 页面 reload 只能丢展示状态，不能丢“刚才可能已经发送”的意图身份。
// sessionStorage 只保存不含正文的 intentId；平台/账号/会话只用于隔离 key。

const PREFIX = 'recruithelper.send.v1.'

function storageKey(targetKey: string, kind: 'proposal' | 'acknowledged'): string {
  return `${PREFIX}${kind}.${encodeURIComponent(targetKey)}`
}

function browserStorage(): Storage {
  if (typeof sessionStorage === 'undefined') throw new Error('当前窗口不支持安全保存发送凭证')
  return sessionStorage
}

function validIntentId(value: string | null): string {
  if (!value || value.length > 128 || /[\u0000-\u001f]/.test(value)) return ''
  return value
}

export function readSendResume(targetKey: string, storage: Storage = browserStorage()): {
  proposalIntentId: string
  acknowledgedIntentId: string
} {
  return {
    proposalIntentId: validIntentId(storage.getItem(storageKey(targetKey, 'proposal'))),
    acknowledgedIntentId: validIntentId(storage.getItem(storageKey(targetKey, 'acknowledged'))),
  }
}

export function rememberSendProposal(targetKey: string, intentId: string, storage: Storage = browserStorage()): void {
  const value = validIntentId(intentId)
  if (!value) throw new Error('发送意图标识无效')
  const key = storageKey(targetKey, 'proposal')
  storage.setItem(key, value)
  if (storage.getItem(key) !== value) throw new Error('发送凭证未能稳定保存')
}

// 仅在脑通过明确 HTTP 拒绝证明 intent/cmd 均未创建后调用。比较后删除，
// 避免迟到的旧请求清掉同一窗口里已经换新的 proposal。
export function discardRejectedSendProposal(
  targetKey: string,
  intentId: string,
  storage: Storage = browserStorage(),
): void {
  const value = validIntentId(intentId)
  if (!value) throw new Error('发送意图标识无效')
  const key = storageKey(targetKey, 'proposal')
  if (storage.getItem(key) !== value) throw new Error('发送凭证已变化，拒绝清除')
  storage.removeItem(key)
  if (storage.getItem(key) !== null) throw new Error('发送凭证未能稳定清除')
}

export function acknowledgeSendIntent(targetKey: string, intentId: string, storage: Storage = browserStorage()): void {
  const value = validIntentId(intentId)
  if (!value) throw new Error('发送意图标识无效')
  const acknowledgedKey = storageKey(targetKey, 'acknowledged')
  storage.setItem(acknowledgedKey, value)
  if (storage.getItem(acknowledgedKey) !== value) throw new Error('发送确认未能稳定保存')
  storage.removeItem(storageKey(targetKey, 'proposal'))
}
