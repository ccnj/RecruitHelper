#!/usr/bin/env bash
# 产出 Windows 免安装目录包(开发验证用,未签名)。在 macOS/Linux 上执行即可,
# target=dir 不需要 wine。产物:release/win-unpacked/,整个目录拷到 Windows 即用。
#
# 这是"开发验证包",不是正式客户交付物:插件仍需人工在 chrome://extensions 加载,
# 不含代码签名、安装器与自动更新。用法见 scripts/windows-run.md。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

STAGE="$REPO_ROOT/build/stage"
OUT="$REPO_ROOT/release"
UNPACKED="$OUT/win-unpacked"

echo "==> 1/4 交叉编译脑服务(CGO_ENABLED=0 GOOS=windows GOARCH=amd64)"
rm -rf "$STAGE"
mkdir -p "$STAGE/brain"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -o "$STAGE/brain/service.exe" ./client/service

echo "==> 2/4 构建 UI"
(cd client/ui && pnpm build)

echo "==> 3/4 构建插件"
(cd plugin && pnpm build)

echo "==> 4/4 打包 Electron 壳"
rm -rf "$OUT"
(cd client/electron && pnpm exec electron-builder --win --x64)

echo "==> 核对产物完整性"
missing=0
for f in \
  "$UNPACKED/RecruitHelper.exe" \
  "$UNPACKED/resources/brain/service.exe" \
  "$UNPACKED/resources/ui/index.html" \
  "$UNPACKED/resources/plugin/manifest.json"
do
  if [ -f "$f" ]; then
    echo "  OK   ${f#"$UNPACKED/"}"
  else
    echo "  缺失 ${f#"$UNPACKED/"}"
    missing=1
  fi
done
# 壳的启动路径在缺二进制时会硬失败并弹窗,这里提前把不完整的包拦在交付前。
if [ "$missing" -ne 0 ]; then
  echo "构建产物不完整,已中止" >&2
  exit 1
fi

echo
echo "完成:$UNPACKED"
echo "把整个 win-unpacked 目录拷到 Windows,双击 RecruitHelper.exe 启动。"
