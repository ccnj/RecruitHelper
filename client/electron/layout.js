// 客户端资源布局解析(可独立于 Electron 测试)。开发期从仓库工作树读,打包后
// 只从随包 resources 读;两种形态的差异集中在本文件,主进程不再散落路径拼接。
'use strict'
const fs = require('node:fs')
const path = require('node:path')

// 脑二进制的文件名。刻意不叫 service.exe:安装器升级前要按映像名结束残留的脑
// 进程,`taskkill /IM service.exe` 会打到系统上任何同名进程。独特名字让这一刀
// 精确,也让用户在任务管理器里认得出这是谁。
const BRAIN_BINARY_BASENAME = 'RecruitHelperBrain'

/** 随包脑二进制的文件名(按目标平台)。 */
function brainBinaryName(platform) {
  return platform === 'win32' ? `${BRAIN_BINARY_BASENAME}.exe` : BRAIN_BINARY_BASENAME
}

/**
 * 解析脑二进制、UI 入口与随包插件目录。
 *
 * 打包态一切取自 resources,不依赖仓库工作树,也不依赖客户机的 Go 工具链。
 * 开发态沿用原有两条路径:`BRAIND_BIN` 指定预编译二进制,否则 `go run`。
 *
 * @param {{
 *   packaged: boolean,
 *   resourcesPath?: string,
 *   repoRoot: string,
 *   platform?: string,
 *   env?: Record<string, string|undefined>,
 *   exists?: (p: string) => boolean,
 * }} opts
 * @returns {{brainBin:string, brainArgs:string[], brainCwd:string, uiEntry:string, pluginDir:string}}
 */
function resolveLayout(opts) {
  const {
    packaged,
    resourcesPath,
    repoRoot,
    platform = process.platform,
    env = process.env,
    exists = fs.existsSync,
  } = opts

  if (packaged) {
    if (!resourcesPath) throw new Error('打包态缺少 resourcesPath')
    const brainBin = path.join(resourcesPath, 'brain', brainBinaryName(platform))
    if (!exists(brainBin)) {
      // 客户机没有 Go 工具链,退回 `go run` 只会把"包没装全"变成开机后难以
      // 诊断的静默异常。这里必须硬失败,让缺失在启动瞬间可见。
      throw new Error(`脑服务二进制缺失,安装包不完整:${brainBin}`)
    }
    return {
      brainBin,
      brainArgs: [],
      brainCwd: path.dirname(brainBin),
      uiEntry: path.join(resourcesPath, 'ui', 'index.html'),
      pluginDir: path.join(resourcesPath, 'plugin'),
    }
  }

  const uiEntry = path.join(repoRoot, 'client', 'ui', 'dist', 'index.html')
  const pluginDir = path.join(repoRoot, 'plugin', 'dist')
  const override = env.BRAIND_BIN
  if (override) {
    return { brainBin: override, brainArgs: [], brainCwd: repoRoot, uiEntry, pluginDir }
  }
  return {
    brainBin: 'go',
    brainArgs: ['run', './client/service'],
    brainCwd: repoRoot,
    uiEntry,
    pluginDir,
  }
}

/**
 * 解析脑数据目录。
 *
 * 打包态固定用 Electron 标准 userData,**不认** `BRAIN_DATA_DIR`:装到机器上的
 * 包不该因为一个残留的开发期环境变量,去写另一份业务库。开发期覆盖仍然保留。
 *
 * @param {{packaged:boolean, userDataDir:string, env?:Record<string,string|undefined>}} opts
 */
function resolveDataDir(opts) {
  const { packaged, userDataDir, env = process.env } = opts
  const configured = env.BRAIN_DATA_DIR?.trim()
  if (packaged || !configured) return path.join(userDataDir, 'data')
  return path.resolve(configured)
}

module.exports = { resolveLayout, resolveDataDir, brainBinaryName, BRAIN_BINARY_BASENAME }
