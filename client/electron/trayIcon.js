// 由 scripts/generate-tray-icon.mjs 生成,不要手改 —— 改图标请改那个脚本并重跑。
//
// 必须是 PNG:Electron 的 nativeImage 不支持 SVG,而它对解析不了的 data URL
// 不报错、返回空 image,结果就是托盘项出现但图标一片空白(只有装到 Windows
// 才看得见,macOS 开发机跑不起 GUI)。
'use strict'
module.exports = {
  TRAY_ICON_PNG_BASE64:
    'iVBORw0KGgoAAAANSUhEUgAAACAAAAAgCAYAAABzenr0AAABOElEQVR42u1XOwoCMRTMUbxELuABvIDNlmkFOyvBVrYU7Gxt9Qx2Ym0tSERhUUQQxScjCosmMYmJq+LAgyWflyFvsskwlgMXssSFTLmQEy4kBY7JNXeJqcCFTLiQWYSF7wNrJKrF6c2R5Lc9K4BAdinHtS5UUKQskuCshclcJ5VrC+oOtzRfHekGfKMNfa75mOvi09mBdECfKwknAuPpnp4BY6IQqLZWZAuMDU4ANbYFxv4J/J4GCj8FH/EfKPxPGCO+j0C9k9FgtHsoAdrQF40AjpZJgHkhBj+Gzd6aXIE5QQhgW31hUxIjgUpjSZvdyZsA5iKHNwEI61UghxcBMA8F0y5oH6Xt/iYYAeQyPUpT34vHFoYLKtUak1fEpxKj1pjorFloaK2ZzpxGJPBoTlX2PEIJlPb8DAj+i0Tp1bOLAAAAAElFTkSuQmCC',
}
