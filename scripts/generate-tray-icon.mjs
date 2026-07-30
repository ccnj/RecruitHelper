// 生成托盘图标,产出 client/electron/trayIcon.js(内嵌 base64 PNG)。
//
//   node scripts/generate-tray-icon.mjs
//
// 为什么要自己编 PNG:Electron 的 nativeImage 只认 PNG/JPEG,不支持 SVG ——
// 而它遇到解析不了的 data URL 不抛错、返回一个空 image,于是 Tray 构造成功、
// 托盘项出现、图标却是空白。这个失败模式在 macOS 开发机上根本看不见(那里跑不起
// GUI),只有装到 Windows 才暴露,所以图标必须是构建期就确定的真 PNG。
//
// 为什么产物是 .js 而不是 .png:client/electron 的 electron-builder `files` 用的是
// "*.js" glob,js 模块自动进 asar,不需要另外配 extraResources。图标只有 1KB 级别,
// 内嵌的代价可以忽略。
//
// 改图标就改这个脚本再重跑,不要手改产物。
import { deflateSync } from 'node:zlib'
import { writeFileSync } from 'node:fs'

const SIZE = 32
const SS = 4 // 超采样倍数:32×32 太小,不做超采样边缘会碎成锯齿
const BG = [0x35, 0x68, 0xe8] // 与产品主色一致
const FG = [0xff, 0xff, 0xff]

// —— 图形:圆角蓝底 + 白色人形(头 + 肩)。招聘助手的托盘图标要在 16×16 缩放后
// 仍能一眼认出,所以只留两个几何体,不放边框和文字笔画那种细节。
function sample(px, py) {
  const x = px / SS
  const y = py / SS
  const r = 7 // 圆角半径
  const inCorner = (cx, cy) => (x - cx) ** 2 + (y - cy) ** 2 > r ** 2
  const outside =
    (x < r && y < r && inCorner(r, r)) ||
    (x > SIZE - r && y < r && inCorner(SIZE - r, r)) ||
    (x < r && y > SIZE - r && inCorner(r, SIZE - r)) ||
    (x > SIZE - r && y > SIZE - r && inCorner(SIZE - r, SIZE - r))
  if (outside) return null // 透明

  const head = (x - 16) ** 2 / 5.6 ** 2 + (y - 11.5) ** 2 / 5.6 ** 2 <= 1
  const shoulders = y >= 18 && (x - 16) ** 2 / 10 ** 2 + (y - 30) ** 2 / 11 ** 2 <= 1
  return head || shoulders ? FG : BG
}

// 超采样后取平均,顺便得到边缘的 alpha
const pixels = Buffer.alloc(SIZE * SIZE * 4)
for (let y = 0; y < SIZE; y++) {
  for (let x = 0; x < SIZE; x++) {
    let r = 0, g = 0, b = 0, a = 0
    for (let sy = 0; sy < SS; sy++) {
      for (let sx = 0; sx < SS; sx++) {
        const c = sample(x * SS + sx + 0.5, y * SS + sy + 0.5)
        if (c) { r += c[0]; g += c[1]; b += c[2]; a += 255 }
      }
    }
    const n = SS * SS
    const i = (y * SIZE + x) * 4
    // 颜色按不透明样本平均,避免透明区把颜色拉黑
    const opaque = a / 255 || 1
    pixels[i] = Math.round(r / opaque)
    pixels[i + 1] = Math.round(g / opaque)
    pixels[i + 2] = Math.round(b / opaque)
    pixels[i + 3] = Math.round(a / n)
  }
}

const CRC_TABLE = Array.from({ length: 256 }, (_, i) => {
  let c = i
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
  return c >>> 0
})

function crc32(buf) {
  let crc = 0xffffffff
  for (const byte of buf) crc = CRC_TABLE[(crc ^ byte) & 0xff] ^ (crc >>> 8)
  return (crc ^ 0xffffffff) >>> 0
}

function chunk(type, data) {
  const length = Buffer.alloc(4)
  length.writeUInt32BE(data.length)
  const typed = Buffer.concat([Buffer.from(type, 'ascii'), data])
  const crc = Buffer.alloc(4)
  crc.writeUInt32BE(crc32(typed))
  return Buffer.concat([length, typed, crc])
}

const ihdr = Buffer.alloc(13)
ihdr.writeUInt32BE(SIZE, 0)
ihdr.writeUInt32BE(SIZE, 4)
ihdr[8] = 8 // 每通道 8 位
ihdr[9] = 6 // 色彩类型 6 = RGBA
// 余下三字节为 0:deflate 压缩、自适应过滤、不隔行

// 每条扫描线前置一个过滤类型字节(0 = 无过滤)
const raw = Buffer.alloc((SIZE * 4 + 1) * SIZE)
for (let y = 0; y < SIZE; y++) {
  const start = y * (SIZE * 4 + 1)
  raw[start] = 0
  pixels.copy(raw, start + 1, y * SIZE * 4, (y + 1) * SIZE * 4)
}

const png = Buffer.concat([
  Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
  chunk('IHDR', ihdr),
  chunk('IDAT', deflateSync(raw, { level: 9 })),
  chunk('IEND', Buffer.alloc(0)),
])

const out = new URL('../client/electron/trayIcon.js', import.meta.url)
writeFileSync(
  out,
  `// 由 scripts/generate-tray-icon.mjs 生成,不要手改 —— 改图标请改那个脚本并重跑。
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
console.log(`已生成 ${SIZE}x${SIZE} PNG,${png.length} 字节 → client/electron/trayIcon.js`)
