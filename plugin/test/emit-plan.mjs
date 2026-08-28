// 吐一份**线上形状**的移动计划,给跨语言接缝做基准。
//
//   node test/emit-plan.mjs <fromX> <fromY> <toX> <toY> <seed>
//
// 它经与生产完全相同的 esbuild 路径加载 plan.ts —— 所以吐出来的就是插件真会
// 发给手服务的那份 JSON,字段名与取整口径都是线上的。Go 侧的用例直接吃它,
// 于是「JS 产出的形状 == Go 消费的形状」这件事有用例锁着,不靠人对字段名。
import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['test/unitentry.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/planemit.mjs',
  logLevel: 'error',
})
const { planMove, DEFAULT_MAX_DWELL_MS } = await import(
  pathToFileURL(process.cwd() + '/test/dist/planemit.mjs').href
)

const [fx, fy, tx, ty, seed] = process.argv.slice(2).map(Number)
if ([fx, fy, tx, ty, seed].some((v) => !Number.isFinite(v))) {
  console.error('用法: node test/emit-plan.mjs <fromX> <fromY> <toX> <toY> <seed>')
  process.exit(2)
}
const plan = planMove({
  from: { x: fx, y: fy },
  to: { x: tx, y: ty },
  // 28 是上游拟合出来的点击目标缺省宽度;基准固定用它,免得基准跟着靶子变。
  targetW: 28,
  maxDwellMs: DEFAULT_MAX_DWELL_MS,
  seed,
})
process.stdout.write(
  JSON.stringify({ from: { x: fx, y: fy }, to: { x: tx, y: ty }, seed,
    points: plan.points, pressMs: plan.pressMs }) + '\n',
)
