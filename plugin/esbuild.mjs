// 打包 base service worker 与 rd6 allowlist content script。
// program 与 base 一起编译进 SW，并作为一个可验证插件构建整体交付；
// program 保持"不注册任何 chrome 监听、只经原语注册表暴露能力"的形态，故此约束靠约定 + code review 守。
import * as esbuild from 'esbuild'
import { cpSync, mkdirSync } from 'node:fs'

const watch = process.argv.includes('--watch')

const common = {
  bundle: true,
  target: 'es2020',
  outdir: 'dist',
  logLevel: 'info',
}

const backgroundOptions = {
  ...common,
  entryPoints: { background: 'src/base/background.ts' },
  format: 'esm',
}

// manifest content_scripts 不支持 type=module；bundle 后用 IIFE，内部无全局导出。
const contentOptions = {
  ...common,
  entryPoints: { content: 'src/base/content.ts' },
  format: 'iife',
}

// 观测查看页。走打包而不是像 options.js 那样原样拷贝 —— 它要和写侧共用
// base/telemetry/store 的同一份分片实现;上游的教训是读写各抄一份,
// 谁都测不到,而存储 bug 是会丢数据的。
const telemetryViewOptions = {
  ...common,
  entryPoints: { 'options/telemetry': 'src/options/telemetry.ts' },
  format: 'iife',
}

// content script 的体积闸。
//
// 它守的不是"包小一点好看",是**三张表那个架构本身**:content.js 与 background.js
// 是两个 bundle、两个 realm,content 侧付不起适配器那 16000 行的重量(切干净后
// 只有 8.3KB)。而 osengine 的引擎与实测池又有 90KB,一旦有人从 content 侧
// 顺手 import 到 program 层,这个数会当场翻两个数量级——而构建不会报任何错。
//
// 上限取实测值的两倍多一点:够容纳正常演进,又拦得住"整层被拽进来"。
const CONTENT_MAX_BYTES = 20_000

async function checkContentSize() {
  const { statSync } = await import('node:fs')
  const bytes = statSync('dist/content.js').size
  if (bytes > CONTENT_MAX_BYTES) {
    console.error(`dist/content.js 有 ${bytes} 字节，超过上限 ${CONTENT_MAX_BYTES}。` +
      `多半是 content 侧 import 到了 program/platform 或 osengine —— 那会把适配器与` +
      `轨迹引擎整层拽进 content bundle。`)
    process.exit(1)
  }
}

mkdirSync('dist', { recursive: true })

// 静态资源(manifest、options、declarativeNetRequest 规则)拷进 dist,
// dist/ 即可直接作为 unpacked 扩展加载。
function copyStatic() {
  cpSync('manifest.json', 'dist/manifest.json')
  cpSync('src/options', 'dist/options', { recursive: true, filter: (from) => !from.endsWith('.ts') })
  cpSync('src/rules', 'dist/rules', { recursive: true })
}

if (watch) {
  const backgroundContext = await esbuild.context({
    ...backgroundOptions,
    plugins: [{ name: 'copy-static', setup(b) { b.onEnd(copyStatic) } }],
  })
  const contentContext = await esbuild.context(contentOptions)
  const telemetryViewContext = await esbuild.context(telemetryViewOptions)
  await Promise.all([backgroundContext.watch(), contentContext.watch(), telemetryViewContext.watch()])
  console.log('watching...')
} else {
  await Promise.all([
    esbuild.build(backgroundOptions),
    esbuild.build(contentOptions),
    esbuild.build(telemetryViewOptions),
  ])
  copyStatic()
  await checkContentSize()
  console.log('built dist/')
}
