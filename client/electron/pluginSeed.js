// 把随包插件模板安置到 Chrome 实际加载的固定目录(可独立于 Electron 测试)。
//
// 为什么不让 Chrome 直接读安装目录:NSIS 升级会整体替换安装目录,而 Chrome 正
// 开着读那里的扩展文件 —— 覆盖一个运行中的扩展目录会让 Chrome 报扩展损坏。固定
// 目录在安装目录之外,客户端升级与插件文件因此解耦。
//
// 本模块只在**壳启动时**动手。那一刻还没有任何批次在跑,天然落在业务安全窗口内,
// 不需要窗口检查。运行期间的暂存、延迟重试与 debug.reload 握手不属于本模块。
'use strict'
const crypto = require('node:crypto')
const fs = require('node:fs')
const path = require('node:path')

// 记录固定目录里当前是哪一版,避免每次启动都白白重写一遍。
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
 * 确保固定目录里是随包这一版插件。
 *
 * 失败一律降级为"保留旧版 + 上报",不抛给启动流程:插件版本落后时既有
 * contractMatch 会挡住 effectful 派发,系统不会带着错版插件发出副作用;而为了
 * 一次文件替换失败就让客户端起不来,是拿可恢复的问题换不可用。
 *
 * @returns {{action:'skipped'|'installed'|'updated'|'failed', digest?:string, reason?:string}}
 */
function ensurePluginInstalled(opts) {
  const { sourceDir, targetDir, fsImpl = fs, log = () => {} } = opts
  let digest
  try {
    digest = directoryDigest(sourceDir, fsImpl)
  } catch (error) {
    log(`[plugin] 随包插件模板不可读,跳过安置:${error.message}`)
    return { action: 'failed', reason: 'source-unreadable' }
  }

  const existed = fsImpl.existsSync(targetDir)
  if (existed && readStamp(targetDir, fsImpl) === digest) {
    return { action: 'skipped', digest }
  }

  const parent = path.dirname(targetDir)
  const tmpDir = path.join(parent, `plugin.tmp-${digest.slice(0, 12)}`)
  const oldDir = path.join(parent, `plugin.old-${digest.slice(0, 12)}`)
  try {
    fsImpl.mkdirSync(parent, { recursive: true })
    fsImpl.rmSync(tmpDir, { recursive: true, force: true })
    fsImpl.cpSync(sourceDir, tmpDir, { recursive: true })
    // 校验后才让它成为正式目录,避免半写入的目录成为唯一副本。
    const manifest = path.join(tmpDir, 'manifest.json')
    if (!fsImpl.existsSync(manifest)) throw new Error('新版缺少 manifest.json')
    JSON.parse(fsImpl.readFileSync(manifest, 'utf8'))
    fsImpl.writeFileSync(path.join(tmpDir, STAMP_FILE), digest)

    fsImpl.rmSync(oldDir, { recursive: true, force: true })
    if (existed) {
      // Chrome 占着旧目录时这一步会失败 —— 那就整体放弃,旧版原封不动。
      fsImpl.renameSync(targetDir, oldDir)
    }
    try {
      fsImpl.renameSync(tmpDir, targetDir)
    } catch (error) {
      if (existed) fsImpl.renameSync(oldDir, targetDir) // 回滚,别留下空目录
      throw error
    }
    fsImpl.rmSync(oldDir, { recursive: true, force: true })
    log(`[plugin] 已${existed ? '更新' : '安置'}插件到 ${targetDir}`)
    return { action: existed ? 'updated' : 'installed', digest }
  } catch (error) {
    fsImpl.rmSync(tmpDir, { recursive: true, force: true })
    log(`[plugin] 安置插件失败,保留原有版本:${error.message}`)
    return { action: 'failed', reason: error.message }
  }
}

module.exports = { pluginInstallDir, ensurePluginInstalled, directoryDigest, STAMP_FILE }
