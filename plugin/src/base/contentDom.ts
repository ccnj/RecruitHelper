// 经当前真实 rd6 页面确认的唯一总未读观察点。DOM 事实只留在手端，
// 不进入协议语义；页面改版后在这里重新核对，不做模糊数字回退。
export const ZHILIAN_UNREAD_BADGE_SELECTOR = 'div.app-im-unread.app-menu-item__im-unread'

export function readZhilianUnreadTotal(root: Pick<ParentNode, 'querySelector'>): number | null {
	const element = root.querySelector(ZHILIAN_UNREAD_BADGE_SELECTOR)
	if (!element) return null
  const text = (element.textContent ?? '').trim()
  if (!/^\d+$/u.test(text)) return null
  const value = Number(text)
  return Number.isSafeInteger(value) && value >= 0 && value <= 1_000_000 ? value : null
}
