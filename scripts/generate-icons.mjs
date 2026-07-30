// 从 assets/app-icon.png 生成三样图标产物:
//
//   client/electron/icons.js   托盘(32)与窗口(256)两份内嵌 PNG
//   assets/app-icon.ico        安装器自身图标 + 快捷方式图标(多尺寸)
//
//   node scripts/generate-icons.mjs
//
// 换图标只需替换 assets/app-icon.png 再跑本脚本。
//
// 为什么内嵌成 .js:client/electron 的 electron-builder `files` 用 "*.js" glob,
// js 模块自动进 asar,不必另配 extraResources。两份加起来几十 KB,可以忽略。
//
// 为什么必须是 PNG:Electron 的 nativeImage 只认 PNG/JPEG,而它遇到解析不了的
// data URL 不抛错、返回空 image —— 于是托盘项出现、图标却空白,连 catch 都不触发。
// 这个失败模式在 macOS 开发机上看不见(跑不起 GUI),只有装到 Windows 才暴露。
//
// 依赖 macOS 的 sips 做缩放:纯 node 缩放要自己写 PNG 解码与重采样,几十行只为
// 省一条命令,不划算。构建本来就在 macOS 上进行。
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
// ICO 里放这几档:16/32 给托盘与小图标,48 给列表视图,256 给大图标与属性页。
const ICO_SIZES = [16, 32, 48, 256]
const TRAY_SIZE = 32
const WINDOW_SIZE = 256

const root = new URL('..', import.meta.url)
const sourcePath = new URL('assets/app-icon.png', root)
const source = readFileSync(sourcePath)
if (!source.subarray(0, 8).equals(SIGNATURE)) {
  throw new Error('assets/app-icon.png 不是 PNG')
}

const work = mkdtempSync(join(tmpdir(), 'recruithelper-icons-'))
try {
  /** 用 sips 缩到指定边长,返回 PNG 字节。 */
  const scale = (size) => {
    const out = join(work, `icon-${size}.png`)
    execFileSync('sips', ['-z', String(size), String(size), sourcePath.pathname, '--out', out], {
      stdio: 'ignore',
    })
    const png = readFileSync(out)
    if (!png.subarray(0, 8).equals(SIGNATURE)) {
      throw new Error(`缩放 ${size} 后不是 PNG`)
    }
    return png
  }

  const scaled = new Map(ICO_SIZES.map((size) => [size, scale(size)]))
  const trayPng = scaled.get(TRAY_SIZE) ?? scale(TRAY_SIZE)
  const windowPng = scaled.get(WINDOW_SIZE) ?? scale(WINDOW_SIZE)

  // —— ICO 组装 ——
  // 结构:6 字节 ICONDIR + 每图 16 字节 ICONDIRENTRY + 各图数据。现代 Windows
  // 接受目录项直接指向 PNG 数据(不必是 BMP),所以这里原样塞入缩放好的 PNG。
  const entries = ICO_SIZES.map((size) => ({ size, png: scaled.get(size) }))
  const header = Buffer.alloc(6)
  header.writeUInt16LE(0, 0) // 保留位
  header.writeUInt16LE(1, 2) // 1 = 图标(2 才是光标)
  header.writeUInt16LE(entries.length, 4)

  let offset = 6 + entries.length * 16
  const directory = []
  for (const entry of entries) {
    const item = Buffer.alloc(16)
    // 256 在这里写 0 —— 这两个字段只有一字节,256 表示不下
    item[0] = entry.size >= 256 ? 0 : entry.size
    item[1] = entry.size >= 256 ? 0 : entry.size
    item[2] = 0 // 调色板色数,真彩为 0
    item[3] = 0 // 保留位
    item.writeUInt16LE(1, 4) // 颜色平面
    item.writeUInt16LE(32, 6) // 位深
    item.writeUInt32LE(entry.png.length, 8)
    item.writeUInt32LE(offset, 12)
    directory.push(item)
    offset += entry.png.length
  }
  const ico = Buffer.concat([header, ...directory, ...entries.map((e) => e.png)])
  writeFileSync(new URL('assets/app-icon.ico', root), ico)

  // —— 内嵌模块 ——
  writeFileSync(
    new URL('client/electron/icons.js', root),
    `// 由 scripts/generate-icons.mjs 从 assets/app-icon.png 生成,不要手改。
//
// 必须是 PNG:Electron 的 nativeImage 不支持 SVG,而它对解析不了的 data URL
// 不报错、返回空 image,结果就是托盘项出现但图标一片空白(只有装到 Windows
// 才看得见,macOS 开发机跑不起 GUI)。
'use strict'
module.exports = {
  // 托盘:系统会按当前 DPI 再缩放,所以给 ${TRAY_SIZE} 而不是压到 16
  TRAY_ICON_PNG_BASE64:
    '${trayPng.toString('base64')}',
  // 窗口与任务栏:Windows 在不同场景取不同尺寸,给大的让它自己降采样
  APP_ICON_PNG_BASE64:
    '${windowPng.toString('base64')}',
}
`,
  )

  console.log(`assets/app-icon.ico        ${ico.length} 字节(${ICO_SIZES.join('/')})`)
  console.log(
    `client/electron/icons.js   托盘 ${trayPng.length} 字节 + 窗口 ${windowPng.length} 字节`,
  )
} finally {
  rmSync(work, { recursive: true, force: true })
}
