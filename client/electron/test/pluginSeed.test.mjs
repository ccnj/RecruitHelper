// 验证插件固定目录的安置与原子替换。用真实临时目录跑 —— 这个模块的要害就是
// 文件操作本身,注入假 fs 会把要验的东西验没了。不需 Electron/显示。
import { createRequire } from 'node:module'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, existsSync, readdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
const require = createRequire(import.meta.url)
const { pluginInstallDir, ensurePluginInstalled, directoryDigest } = require('../pluginSeed.js')

let fail = 0
const check = (c, m) => { console.log(c ? '  PASS' : '  FAIL', m); if (!c) fail++ }

const root = mkdtempSync(join(tmpdir(), 'recruithelper-pluginseed-'))
const source = join(root, 'resources', 'plugin')
const target = join(root, 'installed', 'plugin')

const writeSource = (manifestName, extra) => {
  rmSync(source, { recursive: true, force: true })
  mkdirSync(join(source, 'options'), { recursive: true })
  if (manifestName) {
    writeFileSync(join(source, 'manifest.json'), JSON.stringify({ name: manifestName, key: 'PUB' }))
  }
  writeFileSync(join(source, 'background.js'), extra || 'v1')
  writeFileSync(join(source, 'options', 'options.html'), '<html></html>')
}

// —— 固定目录选址 ——
check(
  pluginInstallDir({ platform: 'win32', env: { LOCALAPPDATA: 'C:\\U\\Local' }, userDataDir: '/ud' })
    === join('C:\\U\\Local', 'RecruitHelper', 'plugin'),
  'Windows 用 LOCALAPPDATA 下的固定目录',
)
check(
  pluginInstallDir({ platform: 'darwin', env: {}, userDataDir: '/ud' }) === join('/ud', 'plugin'),
  '非 Windows 回落到 userData 下',
)
check(
  pluginInstallDir({ platform: 'win32', env: {}, userDataDir: '/ud' }) === join('/ud', 'plugin'),
  'Windows 缺 LOCALAPPDATA 时也有确定去处',
)

// —— 首次安置 ——
writeSource('手端 v1')
const first = ensurePluginInstalled({ sourceDir: source, targetDir: target })
check(first.action === 'installed', '首次安置返回 installed')
check(existsSync(join(target, 'manifest.json')), '安置后 manifest 到位')
check(existsSync(join(target, 'options', 'options.html')), '子目录一并安置')
check(readFileSync(join(target, 'background.js'), 'utf8') === 'v1', '文件内容正确')

// —— 同一版重复启动不重写 ——
const again = ensurePluginInstalled({ sourceDir: source, targetDir: target })
check(again.action === 'skipped', '内容未变时跳过,不每次启动都重写')
check(again.digest === first.digest, '指纹稳定')

// —— 升级 ——
writeSource('手端 v2', 'v2')
const upgraded = ensurePluginInstalled({ sourceDir: source, targetDir: target })
check(upgraded.action === 'updated', '内容变化时更新')
check(readFileSync(join(target, 'background.js'), 'utf8') === 'v2', '更新后是新内容')
check(upgraded.digest !== first.digest, '新版指纹不同')

// —— 关键:新版损坏时,旧版必须完好无损 ——
// 半写入的目录绝不能成为唯一副本(决策文档第三节第 3 项)。
writeSource(null, 'v3-broken') // 没有 manifest.json
const broken = ensurePluginInstalled({ sourceDir: source, targetDir: target })
check(broken.action === 'failed', '新版缺 manifest 时判失败')
check(existsSync(join(target, 'manifest.json')), '失败后旧版 manifest 仍在')
check(readFileSync(join(target, 'background.js'), 'utf8') === 'v2', '失败后旧版内容未被破坏')
const leftovers = readdirSync(join(root, 'installed')).filter((n) => n.startsWith('plugin.'))
check(leftovers.length === 0, '失败后不留 tmp/old 残骸')

// —— 源不可读只降级,不抛 ——
const missing = ensurePluginInstalled({ sourceDir: join(root, 'nonexistent'), targetDir: target })
check(missing.action === 'failed' && missing.reason === 'source-unreadable', '源缺失时降级而非抛出')
check(existsSync(join(target, 'manifest.json')), '源缺失时已装版本不受影响')

// —— 指纹对改名敏感 ——
const d1 = directoryDigest(source)
writeFileSync(join(source, 'renamed.js'), 'v3-broken')
rmSync(join(source, 'background.js'))
check(directoryDigest(source) !== d1, '仅文件改名也会改变指纹')

rmSync(root, { recursive: true, force: true })
console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
process.exit(fail === 0 ? 0 : 1)
