// 取证长图的编码收口(两平台共用):质量阶梯 → 分辨率兜底,超限即失败,绝不发超过企微上限的图。
// 常量与阶梯照抄智联 stitchOnce 的尾段(2026-09-09 抽出供 BOSS 复用,智联侧原样未动)。

export const CAPTURE_MAX_SIDE = 16_000
export const CAPTURE_TARGET_MAX_BYTES = 1_900_000 // 企微 image 原图上限 2MB,留余量
export const CAPTURE_FRAME_QUALITY = 92

export interface EncodedCapture {
  jpeg: Blob
  width: number
  height: number
}

async function canvasToJpeg(canvas: OffscreenCanvas, quality: number): Promise<Blob> {
  return await canvas.convertToBlob({ type: 'image/jpeg', quality: quality / 100 })
}

/** 返回 null 表示最低质量、最小分辨率仍超上限,调用方按 PAYLOAD_LIMIT 收场。 */
export async function encodeStitchedJpeg(canvas: OffscreenCanvas, outW: number, outH: number): Promise<EncodedCapture | null> {
  let jpeg = await canvasToJpeg(canvas, CAPTURE_FRAME_QUALITY)
  let finalW = outW
  let finalH = outH
  for (const stepQuality of [80, 65, 50, 38]) {
    if (jpeg.size <= CAPTURE_TARGET_MAX_BYTES) break
    jpeg = await canvasToJpeg(canvas, stepQuality)
  }
  for (const scale of [0.7, 0.5]) {
    if (jpeg.size <= CAPTURE_TARGET_MAX_BYTES) break
    const scaledW = Math.max(1, Math.round(outW * scale))
    const scaledH = Math.max(1, Math.round(outH * scale))
    const scaled = new OffscreenCanvas(scaledW, scaledH)
    const scaledDraw = scaled.getContext('2d')
    if (!scaledDraw) break
    scaledDraw.drawImage(canvas, 0, 0, scaledW, scaledH)
    jpeg = await canvasToJpeg(scaled, 65)
    finalW = scaledW
    finalH = scaledH
  }
  if (jpeg.size > CAPTURE_TARGET_MAX_BYTES) return null
  return { jpeg, width: finalW, height: finalH }
}
