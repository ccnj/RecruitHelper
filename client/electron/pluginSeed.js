// 把随包资产安置到安装目录**之外**的固定目录(可独立于 Electron 测试)。
//
// 现在有两样:Chrome 加载的插件,与 TSF 加载的自研输入法 DLL。两者出于同一个理由
// 不能留在安装目录里 —— **NSIS 升级会整体替换安装目录,而它们正被别的进程占着**:
//
//   插件  Chrome 开着读扩展文件,被覆盖会报「扩展损坏」
//   TIP   TSF 把 DLL 映射进 chrome.exe 等进程,映像文件在 Windows 上锁死,
//         覆盖或改名直接失败;而且 regsvr32 把**绝对路径**写进注册表,
//         位置一旦被人注册过就是长期承诺,不能随安装目录走
//
// 放到固定目录后,客户端升级与这两样的文件生命周期解耦。
//
// 本模块只在**壳启动时**动手。那一刻还没有任何批次在跑,天然落在业务安全窗口内,
// 不需要窗口检查。运行期间的暂存、延迟重试与 debug.reload 握手不属于本模块。
'use strict'
const crypto = require('node:crypto')
const fs = require('node:fs')
const path = require('node:path')

// 记录固定目录里当前是哪一版,避免每次启动都白白重写一遍。
// 名字里的 plugin 是历史 —— TIP 目录也用同一个文件名,改名会让存量目录白白重装一次,
// 而这个字符串对外没有任何契约意义。
const STAMP_FILE = '.recruithelper-plugin-stamp'

/** 固定插件目录。Windows 用 LOCALAPPDATA(本机资产,不该随域账户漫游)。 */
function pluginInstallDir(opts) {
  const { platform = process.platform, env = process.env, userDataDir } = opts
  if (platform === 'win32' && env.LOCALAPPDATA) {
    return path.join(env.LOCALAPPDATA, 'RecruitHelper', 'plugin')
  }
  return path.join(userDataDir, 'plugin')
}

/**
 * 固定 TIP 目录,与插件目录同级。
 *
 * **这个路径是长期承诺**:`regsvr32` 把 `<这里>\hiboss_tip.dll` 的绝对路径写进
 * `HKCR\CLSID`,改路径等于让已注册的客户机指向一个不存在的文件,而症状是
 * 「输入法还在语言栏里,但一按键什么都不发生」。改它之前先想清楚存量机器怎么办。
 */
function tipInstallDir(opts) {
  const { platform = process.platform, env = process.env, userDataDir } = opts
  if (platform === 'win32' && env.LOCALAPPDATA) {
    return path.join(env.LOCALAPPDATA, 'RecruitHelper', 'tip')
  }
  return path.join(userDataDir, 'tip')
}

/**
 * 安装包暂存目录,与插件目录同级。
 *
 * 刻意不放安装目录:NSIS 升级会 `RMDir /r` 整个安装目录,下好的包会连同一起没了。
 * 也不放业务数据目录 —— 那里装的是业务事实,不该混进可执行文件。
 */
function updateStageDir(opts) {
  const { platform = process.platform, env = process.env, userDataDir } = opts
  if (platform === 'win32' && env.LOCALAPPDATA) {
    return path.join(env.LOCALAPPDATA, 'RecruitHelper', 'updates')
  }
  return path.join(userDataDir, 'updates')
}

/**
 * 目录内容指纹:按相对路径排序后把路径与内容一起摘要,子目录递归。
 * 只要有一个文件改了名或改了内容,指纹就变。
 */
function directoryDigest(dir, fsImpl = fs) {
  const hash = crypto.createHash('sha256')
  const walk = (current, prefix) => {
    const entries = fsImpl.readdirSync(current, { withFileTypes: true })
      .filter((e) => e.name !== STAMP_FILE)
      .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
    for (const entry of entries) {
      const full = path.join(current, entry.name)
      const rel = prefix ? `${prefix}/${entry.name}` : entry.name
      if (entry.isDirectory()) {
        hash.update(`D:${rel}\n`)
        walk(full, rel)
      } else {
        hash.update(`F:${rel}\n`)
        hash.update(fsImpl.readFileSync(full))
      }
    }
  }
  walk(dir, '')
  return hash.digest('hex')
}

function readStamp(dir, fsImpl = fs) {
  try {
    return fsImpl.readFileSync(path.join(dir, STAMP_FILE), 'utf8').trim()
  } catch {
    return ''
  }
}

/**
 * 确保固定目录里是随包这一版资产。插件与 TIP 共用这一套。
 *
 * 失败一律降级为"保留旧版 + 上报",不抛给启动流程:为了一次文件替换失败就让客户端
 * 起不来,是拿可恢复的问题换不可用。两样各有自己的兜底——
 *
 *   插件  版本落后时既有 contractMatch 会挡住 effectful 派发,不会带着错版发副作用
 *   TIP   旧版留着照样能打字;真要紧的是**别把已注册的那个文件弄没了**
 *
 * `validate` 在新版成为正式目录**之前**跑,避免半写入的目录成为唯一副本。
 *
 * @returns {{action:'skipped'|'installed'|'updated'|'failed', digest?:string, reason?:string}}
 */
function ensureSeeded(opts) {
  const { sourceDir, targetDir, label, what, validate, fsImpl = fs, log = () => {} } = opts
  let digest
  try {
    digest = directoryDigest(sourceDir, fsImpl)
  } catch (error) {
    log(`[${label}] 随包${what}不可读,跳过安置:${error.message}`)
    return { action: 'failed', reason: 'source-unreadable' }
  }

  const existed = fsImpl.existsSync(targetDir)
  if (existed && readStamp(targetDir, fsImpl) === digest) {
    return { action: 'skipped', digest }
  }

  const parent = path.dirname(targetDir)
  const tmpDir = path.join(parent, `${label}.tmp-${digest.slice(0, 12)}`)
  const oldDir = path.join(parent, `${label}.old-${digest.slice(0, 12)}`)
  try {
    fsImpl.mkdirSync(parent, { recursive: true })
    fsImpl.rmSync(tmpDir, { recursive: true, force: true })
    fsImpl.cpSync(sourceDir, tmpDir, { recursive: true })
    validate(tmpDir, fsImpl)
    fsImpl.writeFileSync(path.join(tmpDir, STAMP_FILE), digest)

    fsImpl.rmSync(oldDir, { recursive: true, force: true })
    if (existed) {
      // 别的进程占着旧目录时这一步会失败 —— 那就整体放弃,旧版原封不动。
      // 插件是 Chrome 开着读扩展文件;TIP 是 TSF 把 DLL 映射进了 chrome.exe,
      // 映像文件在 Windows 上锁死,连目录改名都做不到。两种都走这一条。
      fsImpl.renameSync(targetDir, oldDir)
    }
    try {
      fsImpl.renameSync(tmpDir, targetDir)
    } catch (error) {
      if (existed) fsImpl.renameSync(oldDir, targetDir) // 回滚,别留下空目录
      throw error
    }
    fsImpl.rmSync(oldDir, { recursive: true, force: true })
    log(`[${label}] 已${existed ? '更新' : '安置'}${what}到 ${targetDir}`)
    return { action: existed ? 'updated' : 'installed', digest }
  } catch (error) {
    fsImpl.rmSync(tmpDir, { recursive: true, force: true })
    log(`[${label}] 安置${what}失败,保留原有版本:${error.message}`)
    return { action: 'failed', reason: error.message }
  }
}

/** 确保固定目录里是随包这一版插件。 */
function ensurePluginInstalled(opts) {
  return ensureSeeded({
    ...opts,
    label: 'plugin',
    what: '插件',
    validate: (tmpDir, fsImpl) => {
      const manifest = path.join(tmpDir, 'manifest.json')
      if (!fsImpl.existsSync(manifest)) throw new Error('新版缺少 manifest.json')
      JSON.parse(fsImpl.readFileSync(manifest, 'utf8'))
    },
  })
}

/** 随包 TIP 目录里必须有的那个文件。名字由 hiBoss 的 crate 名决定,不是我们能改的。 */
const TIP_DLL = 'hiboss_tip.dll'

/**
 * 确保固定目录里是随包这一版 TIP。
 *
 * **本函数不注册,也不可能注册。** `regsvr32` 写 HKCR\CLSID 与 TSF profile,
 * 必须管理员权限,而本产品的安装器刻意是 `RequestExecutionLevel user`、不弹 UAC
 * (静默升级正靠这一点)。注册是一次性的人工步骤,见 third_party/tip/README.md。
 *
 * **换了 TIP 版本之后可能要重新 regsvr32。** 文件路径不变所以注册通常照旧有效,
 * 但 CLSID 与 profile GUID 是烧在 DLL 里的,它们变了就得重注册——那是升级 TIP
 * 时要一起想的事,这里只负责把文件放对地方。
 */
function ensureTipInstalled(opts) {
  return ensureSeeded({
    ...opts,
    label: 'tip',
    what: 'TIP',
    validate: (tmpDir, fsImpl) => {
      const dll = path.join(tmpDir, TIP_DLL)
      if (!fsImpl.existsSync(dll)) throw new Error(`新版缺少 ${TIP_DLL}`)
      // 查 PE 头。半写入的文件在别处都看不出来,而它的症状是 regsvr32 报一句
      // 看不懂的错 —— 那时没人会怀疑是拷贝断了。
      const head = fsImpl.readFileSync(dll).subarray(0, 2).toString('latin1')
      if (head !== 'MZ') throw new Error(`${TIP_DLL} 不是 PE 文件(头两字节 ${JSON.stringify(head)})`)
    },
  })
}

module.exports = {
  pluginInstallDir, tipInstallDir, updateStageDir,
  ensurePluginInstalled, ensureTipInstalled,
  directoryDigest, STAMP_FILE, TIP_DLL,
}
