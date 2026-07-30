// 把托盘图标源图内嵌成 client/electron/trayIcon.js。
//
//   node scripts/generate-tray-icon.mjs
//
// 为什么要有这一步:Electron 的 nativeImage 只认 PNG/JPEG,而它遇到解析不了的
// data URL 不抛错、返回一个空 image,于是 Tray 构造成功、托盘项出现、图标却是
// 空白。这个失败模式在 macOS 开发机上根本看不见(那里跑不起 GUI),只有装到
// Windows 才暴露 —— 所以图标必须是构建期就确定下来的真 PNG。
//
// 为什么产物是 .js 而不是直接读 .png:client/electron 的 electron-builder `files`
// 用的是 "*.js" glob,js 模块自动进 asar,不需要另外配 extraResources。图标只有
// 2KB 级别,内嵌的代价可以忽略。
//
// 换图标:替换 assets/app-icon.png,重新生成托盘尺寸,再跑本脚本。
//
//   sips -z 32 32 assets/app-icon.png --out assets/tray-icon-32.png
//   node scripts/generate-tray-icon.mjs
//
// 缩放之所以留在脚本外:sips 是 macOS 专有,而纯 node 缩放要自己写 PNG 解码与
// 重采样,几十行只为省一条命令,不划算;托盘尺寸的图入库后也便于直接核对效果。
import { readFileSync, writeFileSync } from 'node:fs'

const source = new URL('../assets/tray-icon-32.png', import.meta.url)
const png = readFileSync(source)

// 起码确认它真是 PNG —— 否则又会退化成"托盘有、图标空"那种只能装到 Windows
// 才发现的故障。
const SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
if (!png.subarray(0, 8).equals(SIGNATURE)) {
  throw new Error('assets/tray-icon-32.png 不是 PNG')
}
const width = png.readUInt32BE(16)
const height = png.readUInt32BE(20)
if (width !== height || width > 64) {
  throw new Error(`托盘图标应是不大于 64 的正方形,当前 ${width}x${height}`)
}

const out = new URL('../client/electron/trayIcon.js', import.meta.url)
writeFileSync(
  out,
  `// 由 scripts/generate-tray-icon.mjs 从 assets/tray-icon-32.png 生成,不要手改。
//
// 必须是 PNG:Electron 的 nativeImage 不支持 SVG,而它对解析不了的 data URL
// 不报错、返回空 image,结果就是托盘项出现但图标一片空白(只有装到 Windows
// 才看得见,macOS 开发机跑不起 GUI)。
'use strict'
module.exports = {
  TRAY_ICON_PNG_BASE64:
    '${png.toString('base64')}',
}
`,
)
console.log(`已内嵌 ${width}x${height} PNG,${png.length} 字节 → client/electron/trayIcon.js`)
