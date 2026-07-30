// 招呼与消息两个编辑器共用的待决发送句柄。intentId 一旦生成就跟到终局，
// 提交失败也只沿用同一个，不新铸——这是"不会多发一条"的前端一侧。
export interface PendingSend {
  intentId: string
  text?: string
}

export function newIntentId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  const random = Math.random().toString(36).slice(2)
  return `intent-${Date.now().toString(36)}-${random}`
}
