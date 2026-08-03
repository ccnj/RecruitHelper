// 经当前真实 rd6 页面确认的唯一总未读观察点。DOM 事实只留在手端，
// 不进入协议语义；页面改版后在这里重新核对，不做模糊数字回退。
//
// 2026-08-03 真机订正：本节点常驻聊天菜单项，未读清零时不消失，只是页面
// 摘掉 `app-im-unread` 这个类并清空文本。此前选择器同时要求两个类，于是
// “零未读”被读成“读不到”，快照塌成 null——未读子轮把未读清干净后回读
// 必然读不到收尾数，基线永远写不进去，插队随即被 unreadRetryDeferred 锁死。
// 干得越干净越判定为没跑完。故选择器收回常驻单类，由文本承担三态。
// 清零态下全页只此一处命中（其余八个菜单项均无该节点），无歧义风险。
export const ZHILIAN_UNREAD_BADGE_SELECTOR = '.app-menu-item__im-unread'

export function readZhilianUnreadTotal(root: Pick<ParentNode, 'querySelector'>): number | null {
  const element = root.querySelector(ZHILIAN_UNREAD_BADGE_SELECTOR)
  // 节点缺失才是真的读不到：页面结构已变，按既有失效方向不猜测数值。
  if (!element) return null
  const text = (element.textContent ?? '').trim()
  // 空文本是页面表达“无未读”的正式形态，不是缺失。
  if (text === '') return 0
  if (/^\d+$/u.test(text)) {
    const value = Number(text)
    return Number.isSafeInteger(value) && value >= 0 && value <= 1_000_000 ? value : null
  }
  // "99+" 一类截断展示：取前导数字，取不到按 1。此处只能向多算，绝不能
  // 落回 0——把满格未读读成零未读会让插队彻底静默。
  const leading = /^(\d+)/u.exec(text)
  if (leading) {
    const value = Number(leading[1])
    return Number.isSafeInteger(value) && value >= 0 && value <= 1_000_000 ? value : 1
  }
  return 1
}
