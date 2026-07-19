// 打包 base service worker 与 rd6 allowlist content script。
// program 与 base 在 M1 一起编译进 SW(l7eval5 远程交付机制悬置);
// program 保持"不注册任何 chrome 监听、只经原语注册表暴露能力"的形态,故此约束靠约定 + code review 守。
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

mkdirSync('dist', { recursive: true })

// 静态资源(manifest、options)拷进 dist,dist/ 即可直接作为 unpacked 扩展加载。
function copyStatic() {
  cpSync('manifest.json', 'dist/manifest.json')
  cpSync('src/options', 'dist/options', { recursive: true })
}

if (watch) {
  const backgroundContext = await esbuild.context({
    ...backgroundOptions,
    plugins: [{ name: 'copy-static', setup(b) { b.onEnd(copyStatic) } }],
  })
  const contentContext = await esbuild.context(contentOptions)
  await Promise.all([backgroundContext.watch(), contentContext.watch()])
  console.log('watching...')
} else {
  await Promise.all([
    esbuild.build(backgroundOptions),
    esbuild.build(contentOptions),
  ])
  copyStatic()
  console.log('built dist/')
}
