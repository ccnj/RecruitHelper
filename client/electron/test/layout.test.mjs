// 验证客户端资源布局解析:开发态沿用仓库工作树,打包态只认随包 resources。
// 重点是两条不许退化的边:安装包缺脑二进制必须硬失败(不得退回 `go run`),
// 打包态不得被残留的 BRAIN_DATA_DIR 指去写别处的业务库。不需 Electron/显示。
import { createRequire } from 'node:module'
import { join } from 'node:path'
const require = createRequire(import.meta.url)
const { resolveLayout, resolveDataDir, brainBinaryName } = require('../layout.js')

let fail = 0
const check = (c, m) => { console.log(c ? '  PASS' : '  FAIL', m); if (!c) fail++ }

const REPO = '/repo'
const RES = '/app/resources'
const present = () => true
const absent = () => false

// —— 开发态 ——
const devDefault = resolveLayout({ packaged: false, repoRoot: REPO, env: {} })
check(devDefault.brainBin === 'go', '开发态默认用 go')
check(
  devDefault.brainArgs.join(' ') === 'run ./client/service',
  '开发态默认 go run ./client/service',
)
check(devDefault.brainCwd === REPO, '开发态 cwd 是仓库根')
check(
  devDefault.uiEntry === join(REPO, 'client', 'ui', 'dist', 'index.html'),
  '开发态 UI 取自仓库工作树',
)

const devOverride = resolveLayout({
  packaged: false,
  repoRoot: REPO,
  env: { BRAIND_BIN: '/tmp/braind' },
})
check(devOverride.brainBin === '/tmp/braind', '开发态 BRAIND_BIN 优先')
check(devOverride.brainArgs.length === 0, 'BRAIND_BIN 时不带 go run 参数')

// —— 打包态 ——
check(brainBinaryName('win32') === 'RecruitHelperBrain.exe', 'Windows 脑二进制带 .exe')
check(brainBinaryName('darwin') === 'RecruitHelperBrain', '非 Windows 脑二进制无扩展名')

const win = resolveLayout({
  packaged: true,
  resourcesPath: RES,
  repoRoot: REPO,
  platform: 'win32',
  env: {},
  exists: present,
})
check(win.brainBin === join(RES, 'brain', 'RecruitHelperBrain.exe'), '打包态脑二进制取自 resources/brain')
check(win.brainArgs.length === 0, '打包态不带 go run 参数')
check(win.brainCwd === join(RES, 'brain'), '打包态 cwd 是脑二进制所在目录')
check(win.uiEntry === join(RES, 'ui', 'index.html'), '打包态 UI 取自 resources/ui')
check(win.pluginDir === join(RES, 'plugin'), '打包态插件目录随包')

const mac = resolveLayout({
  packaged: true,
  resourcesPath: RES,
  repoRoot: REPO,
  platform: 'darwin',
  env: {},
  exists: present,
})
check(mac.brainBin === join(RES, 'brain', 'RecruitHelperBrain'), '打包态 mac 脑二进制无扩展名')

// 安装包不完整:必须抛错,绝不静默退回开发期的 `go run`(客户机没有 Go 工具链)。
let threw = null
try {
  resolveLayout({
    packaged: true,
    resourcesPath: RES,
    repoRoot: REPO,
    platform: 'win32',
    env: {},
    exists: absent,
  })
} catch (error) {
  threw = error
}
check(threw !== null, '打包态缺脑二进制时抛错')
check(
  threw !== null && /安装包不完整/.test(String(threw.message)),
  '错误信息指出安装包不完整',
)
// 2026-08-10 真机现场:文件缺失的实际成因是杀毒软件隔离删除。提示必须点名
// 这个原因并给出处置路径,否则现场只会反复重装、反复被隔离。
check(
  threw !== null && /杀毒软件/.test(String(threw.message)) &&
    /排除项/.test(String(threw.message)),
  '错误信息点名杀毒软件误报并给出排除项指引',
)

// 打包态即使带着 BRAIND_BIN 也不得改用它:包只信自己携带的二进制。
let threwWithOverride = null
try {
  resolveLayout({
    packaged: true,
    resourcesPath: RES,
    repoRoot: REPO,
    platform: 'win32',
    env: { BRAIND_BIN: '/tmp/braind' },
    exists: absent,
  })
} catch (error) {
  threwWithOverride = error
}
check(threwWithOverride !== null, '打包态不因 BRAIND_BIN 绕过随包二进制')

// —— 数据目录 ——
check(
  resolveDataDir({ packaged: false, userDataDir: '/ud', env: {} }) === join('/ud', 'data'),
  '开发态无覆盖时用 userData/data',
)
check(
  resolveDataDir({ packaged: false, userDataDir: '/ud', env: { BRAIN_DATA_DIR: '/repo/data' } })
    === '/repo/data',
  '开发态 BRAIN_DATA_DIR 生效',
)
check(
  resolveDataDir({ packaged: true, userDataDir: '/ud', env: { BRAIN_DATA_DIR: '/repo/data' } })
    === join('/ud', 'data'),
  '打包态忽略 BRAIN_DATA_DIR,固定 userData/data',
)

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
process.exit(fail === 0 ? 0 : 1)
