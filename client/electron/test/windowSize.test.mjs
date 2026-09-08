// 验证主窗口初始尺寸按工作区收窗:大屏保持 1200×840,小屏或高缩放下宽收到八成、
// 高留余量,任何输入都不低于 600 宽。不需 Electron/显示。
import { createRequire } from 'node:module'
const require = createRequire(import.meta.url)
const { resolveWindowSize, MIN_WIDTH } = require('../windowSize.js')

let fail = 0
const check = (c, m) => { console.log(c ? '  PASS' : '  FAIL', m); if (!c) fail++ }
const same = (a, b) => a.width === b.width && a.height === b.height && a.minWidth === b.minWidth

// 1080p 100%:工作区 1920×1040,两项都不触发,原样 1200×840。
check(
  same(resolveWindowSize({ width: 1920, height: 1040 }), { width: 1200, height: 840, minWidth: 600 }),
  '1080p 100% 保持 1200×840',
)
// 1080p 125%:1536×824,宽不变、高收到 764。
check(
  same(resolveWindowSize({ width: 1536, height: 824 }), { width: 1200, height: 764, minWidth: 600 }),
  '1080p 125% 高收到 764',
)
// 1080p 150%:1280×672(温胜堂01 形态),1024×612,两侧各露 128。
check(
  same(resolveWindowSize({ width: 1280, height: 672 }), { width: 1024, height: 612, minWidth: 600 }),
  '1080p 150% 收到 1024×612',
)
// 1366×768 100%:工作区 1366×728,1092×668。
check(
  same(resolveWindowSize({ width: 1366, height: 728 }), { width: 1092, height: 668, minWidth: 600 }),
  '1366×768 收到 1092×668',
)
// 极小工作区:宽不低于 600,高不低于内部下限。
check(
  same(resolveWindowSize({ width: 700, height: 500 }), { width: 600, height: 440, minWidth: 600 }),
  '极小工作区宽钳在 600',
)
check(resolveWindowSize({ width: 300, height: 100 }).height === 400, '荒唐高度钳在内部下限')
// 读不到工作区:退回默认值。
for (const bad of [undefined, null, {}, { width: 'x', height: -1 }, { width: 0, height: 0 }]) {
  check(
    same(resolveWindowSize(bad), { width: 1200, height: 840, minWidth: 600 }),
    `工作区不合法(${JSON.stringify(bad)})退回默认`,
  )
}
check(MIN_WIDTH === 600, '最小宽度常量为 600')

if (fail) { console.error(`${fail} 项失败`); process.exit(1) }
console.log('windowSize 测试全部通过')
