// M3 有人值守 UI 用 sessionStorage 记住“该终局已由人确认”。发送意图的
// 恢复真相只来自脑侧 latest/head，不依赖浏览器存储。

const PREFIX = 'recruithelper.send.v1.'

function storageKey(targetKey: string): string {
  return `${PREFIX}acknowledged.${encodeURIComponent(targetKey)}`
}

function browserStorage(): Storage {
  if (typeof sessionStorage === 'undefined') throw new Error('当前窗口不支持保存人工确认')
  return sessionStorage
}

function validIntentId(value: string | null): string {
  if (!value || value.length > 128 || /[\u0000-\u001f]/.test(value)) return ''
  return value
}

export function readSendResume(targetKey: string, storage: Storage = browserStorage()): {
  acknowledgedIntentId: string
} {
  return {
    acknowledgedIntentId: validIntentId(storage.getItem(storageKey(targetKey))),
  }
}

export function acknowledgeSendIntent(targetKey: string, intentId: string, storage: Storage = browserStorage()): void {
  const value = validIntentId(intentId)
  if (!value) throw new Error('发送意图标识无效')
  const acknowledgedKey = storageKey(targetKey)
  storage.setItem(acknowledgedKey, value)
  if (storage.getItem(acknowledgedKey) !== value) throw new Error('发送确认未能稳定保存')
}
