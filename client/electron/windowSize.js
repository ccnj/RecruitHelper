// 主窗口初始尺寸(可独立于 Electron 测试)。
//
// 此前写死 1200×840 逻辑像素。2026-09-08 温胜堂01 客户机(1080p、150% 缩放,
// 逻辑工作区 1280×672)上,这个窗口左右只剩 40px 缝、下边伸出屏幕;它把 Chrome
// 几乎整个盖住,Chrome 把被盖住的窗口按隐藏页面处理、超 5 分钟后把页面计时器
// 按整分钟节流,手侧原语的 20 秒等待必超时,当天十个职位的筛选全部失败、零招呼。
// 这里按工作区收窗口,让 Chrome 在旁边露出一截,是顺手的次要缓解;主修在手侧
// (采集线导航到推荐页时激活标签页并聚焦窗口),锁屏、切标签、最大化客户端
// 三种情况本函数一个都管不了。
'use strict'

const DEFAULT_WIDTH = 1200
const DEFAULT_HEIGHT = 840
// 甲方 2026-09-08 定:最小宽度 600。侧栏自本批起永远展开(不再按窗口宽度折叠),
// 600 是主内容区不被挤坏的下限。
const MIN_WIDTH = 600
// 宽最多占工作区八成,高留 60px 余量给标题栏与任务栏;大屏两项都不生效,
// 仍是 1200×840。
const WIDTH_SHARE = 0.8
const HEIGHT_MARGIN = 60
// 高的内部下限,只防工作区读到荒唐值(如 0 或几十像素)时把窗口压成一条。
const HEIGHT_FLOOR = 400

/**
 * 按主显示器工作区算主窗口初始尺寸。
 *
 * @param {{ width?: unknown, height?: unknown } | null | undefined} workArea
 *   Electron `screen.getPrimaryDisplay().workAreaSize`;读不到或不合法时退回默认值。
 * @returns {{ width: number, height: number, minWidth: number }}
 */
function resolveWindowSize(workArea) {
  const areaWidth = Number(workArea && workArea.width)
  const areaHeight = Number(workArea && workArea.height)
  const width = Number.isFinite(areaWidth) && areaWidth > 0
    ? Math.min(DEFAULT_WIDTH, Math.floor(areaWidth * WIDTH_SHARE))
    : DEFAULT_WIDTH
  const height = Number.isFinite(areaHeight) && areaHeight > 0
    ? Math.min(DEFAULT_HEIGHT, areaHeight - HEIGHT_MARGIN)
    : DEFAULT_HEIGHT
  return {
    width: Math.max(MIN_WIDTH, width),
    height: Math.max(HEIGHT_FLOOR, height),
    minWidth: MIN_WIDTH,
  }
}

module.exports = { resolveWindowSize, DEFAULT_WIDTH, DEFAULT_HEIGHT, MIN_WIDTH }
