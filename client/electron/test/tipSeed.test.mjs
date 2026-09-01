// 验证 TIP 固定目录的安置。与插件那套共用一个核心,所以这里只验**它自己特有的**:
// 选址、PE 头校验,以及最要紧的那条——**替换失败时不能把已注册的那份弄没了**。
//
// 为什么这条最要紧:regsvr32 把 `<固定目录>\hiboss_tip.dll` 的绝对路径写进
// HKCR\CLSID。文件没了,注册还在,症状是「输入法还在语言栏里,一按键什么都不发生」
// ——而这跟「TIP 从没装上」现场看起来一模一样。
import { createRequire } from 'node:module'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, existsSync, readdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
const require = createRequire(import.meta.url)
const { tipInstallDir, ensureTipInstalled, TIP_DLL } = require('../pluginSeed.js')

let fail = 0
const check = (c, m) => { console.log(c ? '  PASS' : '  FAIL', m); if (!c) fail++ }

const root = mkdtempSync(join(tmpdir(), 'recruithelper-tipseed-'))
const source = join(root, 'resources', 'tip')
const target = join(root, 'installed', 'tip')

// 最小可信的 PE:头两字节 MZ。校验查的就是这个。
const writeSource = (body, name = TIP_DLL) => {
  rmSync(source, { recursive: true, force: true })
  mkdirSync(source, { recursive: true })
  writeFileSync(join(source, name), body)
}

// —— 选址 ——
check(
  tipInstallDir({ platform: 'win32', env: { LOCALAPPDATA: 'C:\\U\\Local' }, userDataDir: '/ud' })
    === join('C:\\U\\Local', 'RecruitHelper', 'tip'),
  'Windows 用 LOCALAPPDATA 下的固定目录',
)
check(
  tipInstallDir({ platform: 'darwin', env: {}, userDataDir: '/ud' }) === join('/ud', 'tip'),
  '非 Windows 回落到 userData 下',
)
// 它必须与插件目录同级、不在安装目录里 —— 那正是搬出来的全部意义。
check(
  !tipInstallDir({ platform: 'win32', env: { LOCALAPPDATA: 'C:\\U\\Local' }, userDataDir: '/ud' })
    .includes('Programs'),
  '固定目录不在安装目录(%LOCALAPPDATA%\\Programs\\RecruitHelper)之下',
)

// —— 首次安置 ——
writeSource('MZ\u0000\u0000fake-pe-v1')
const first = ensureTipInstalled({ sourceDir: source, targetDir: target })
check(first.action === 'installed', '首次安置返回 installed')
check(existsSync(join(target, TIP_DLL)), 'DLL 落到了固定目录')
check(readFileSync(join(target, TIP_DLL), 'utf8').startsWith('MZ'), '内容原样搬过去')

// —— 同一版不重复搬 ——
check(ensureTipInstalled({ sourceDir: source, targetDir: target }).action === 'skipped',
  '同一版第二次启动直接跳过')

// —— 换版 ——
writeSource('MZ\u0000\u0000fake-pe-v2')
const upgraded = ensureTipInstalled({ sourceDir: source, targetDir: target })
check(upgraded.action === 'updated', '换版返回 updated')
check(readFileSync(join(target, TIP_DLL), 'utf8').includes('v2'), '固定目录里换成了新版')

// —— PE 头校验:半写入的文件不许成为唯一副本 ——
// 拷贝断了在别处都看不出来,症状是 regsvr32 报一句看不懂的错,
// 那时没人会怀疑是文件本身坏了。
writeSource('<html>not a dll</html>')
const notPe = ensureTipInstalled({ sourceDir: source, targetDir: target })
check(notPe.action === 'failed', '不是 PE 文件时拒绝安置')
check(readFileSync(join(target, TIP_DLL), 'utf8').includes('v2'), '拒绝之后旧版原封不动')

// —— 缺文件 ——
writeSource('MZ\u0000\u0000whatever', 'wrong_name.dll')
const missing = ensureTipInstalled({ sourceDir: source, targetDir: target })
check(missing.action === 'failed', '源里没有 hiboss_tip.dll 时失败')
check(readFileSync(join(target, TIP_DLL), 'utf8').includes('v2'), '旧版仍在')

// —— 源整个不可读 ——
const gone = ensureTipInstalled({ sourceDir: join(root, 'nonexistent'), targetDir: target })
check(gone.action === 'failed' && gone.reason === 'source-unreadable', '源不可读时降级,不抛')
check(readFileSync(join(target, TIP_DLL), 'utf8').includes('v2'), '源不可读也不动已装的那份')

// —— 目录被占住(Windows 上 TSF 映射着 DLL 时就是这样) ——
// 真机上是 renameSync 失败。这里用假 fs 只替换那一个调用来构造,
// 其余照走真实文件操作。
writeSource('MZ\u0000\u0000fake-pe-v3')
const fs = require('node:fs')
const lockedFs = Object.create(fs)
lockedFs.renameSync = (from, to) => {
  if (from === target) throw new Error('EBUSY: resource busy or locked')
  return fs.renameSync(from, to)
}
const locked = ensureTipInstalled({ sourceDir: source, targetDir: target, fsImpl: lockedFs })
check(locked.action === 'failed', '目录被占住时安置失败')
check(existsSync(join(target, TIP_DLL)), '**被占住也绝不能把已注册的那份弄没了**')
check(readFileSync(join(target, TIP_DLL), 'utf8').includes('v2'), '占住时旧版内容完好')

// —— 不留残骸 ——
const parent = join(root, 'installed')
check(readdirSync(parent).every((n) => !n.startsWith('tip.tmp-') && !n.startsWith('tip.old-')),
  '失败后不留 tmp/old 残骸')

rmSync(root, { recursive: true, force: true })
console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAILED`)
process.exit(fail === 0 ? 0 : 1)
