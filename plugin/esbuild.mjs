// 打包 base service worker 为单文件 dist/background.js。
// program 与 base 在 M1 一起编译进 SW(l7eval5 远程交付机制悬置);
// program 保持"不注册任何 chrome 监听、只经原语注册表暴露能力"的形态,故此约束靠约定 + code review 守。
import * as esbuild from 'esbuild'
import { cpSync, mkdirSync } from 'node:fs'

const watch = process.argv.includes('--watch')

const opts = {
  entryPoints: { background: 'src/base/background.ts' },
  bundle: true,
  format: 'esm',
  target: 'es2020',
  outdir: 'dist',
  logLevel: 'info',
}

mkdirSync('dist', { recursive: true })

// 静态资源(manifest、options)拷进 dist,dist/ 即可直接作为 unpacked 扩展加载。
function copyStatic() {
  cpSync('manifest.json', 'dist/manifest.json')
  cpSync('src/options', 'dist/options', { recursive: true })
}

if (watch) {
  const ctx = await esbuild.context({
    ...opts,
    plugins: [{ name: 'copy-static', setup(b) { b.onEnd(copyStatic) } }],
  })
  await ctx.watch()
  console.log('watching...')
} else {
  await esbuild.build(opts)
  copyStatic()
  console.log('built dist/')
}
