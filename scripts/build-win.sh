#!/usr/bin/env bash
# 产出 Windows 安装包(未签名)。产物:
#   release/RecruitHelper-<版本>-setup.exe   安装器,交付用
#   release/win-unpacked/                    免安装目录,调试用
#
# 全程在 macOS/Linux 本地完成,不需要 Docker、不需要 wine:Go 交叉编译、UI 与插件
# 构建本来就跨平台;electron-builder 只出 dir 包;安装器由原生 makensis 编译
# (标准 NSIS 在编译期用 WriteUninstaller 写出卸载器,不必运行任何 Windows exe)。
#
# 这仍是"开发验证包",不是正式客户交付物:没有代码签名,也没有自动更新。
# 用法与首跑步骤见 scripts/windows-run.md。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

STAGE="$REPO_ROOT/build/stage"
OUT="$REPO_ROOT/release"
UNPACKED="$OUT/win-unpacked"
# 可执行文件名各有唯一源头,别在这里另抄一份:脑的名字在 layout.js(壳靠它定位
# 二进制,安装器靠它精确 taskkill),壳的名字由 electron-builder 的 productName 决定。
BRAIN_EXE="$(node -p "require('./client/electron/layout.js').brainBinaryName('win32')")"
APP_EXE="$(node -p "require('./client/electron/package.json').build.productName").exe"
VERSION="$(node -p "require('./client/electron/package.json').version")"
SETUP="$OUT/RecruitHelper-$VERSION-setup.exe"

# makensis:**优先 electron-builder 缓存里那份**,不是系统里的。
# Homebrew 的 makensis 3.12 在 macOS 26 / arm64 上是坏的 —— 只要 Section 里有任何
# 实质指令(File、WriteUninstaller、ExecWait 都算)就 std::bad_alloc 退出,且不打印
# 任何有用错误。缓存里的 3.04 一切正常。别"顺手"把这个顺序改回来。
find_makensis() {
  local cached
  for cached in "$HOME/Library/Caches/electron-builder/nsis"/nsis-*/mac/makensis \
                "$HOME/.cache/electron-builder/nsis"/nsis-*/linux/makensis; do
    if [ -x "$cached" ]; then
      echo "$cached"
      return
    fi
  done
  if command -v makensis > /dev/null 2>&1; then
    command -v makensis
  fi
}

echo "==> 1/5 交叉编译脑服务(CGO_ENABLED=0 GOOS=windows GOARCH=amd64)"
rm -rf "$STAGE"
mkdir -p "$STAGE/brain"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -o "$STAGE/brain/$BRAIN_EXE" ./client/service

echo "==> 2/5 构建 UI"
(cd client/ui && pnpm build)

echo "==> 3/5 构建插件"
(cd plugin && pnpm build)

echo "==> 4/5 打包 Electron 壳(dir)"
rm -rf "$OUT"
(cd client/electron && pnpm exec electron-builder --win --x64)

echo "==> 5/5 编译安装器(makensis)"
MAKENSIS="$(find_makensis)"
if [ -z "$MAKENSIS" ]; then
  echo "找不到 makensis。装一个:brew install makensis" >&2
  exit 1
fi
# 用 electron-builder 缓存那份时必须指出 NSISDIR,否则它会去 /usr/local/share/nsis 找 stub。
NSIS_ROOT="$(cd "$(dirname "$MAKENSIS")/.." && pwd)"
if [ -d "$NSIS_ROOT/Stubs" ]; then
  export NSISDIR="$NSIS_ROOT"
fi
# installer.nsi 必须是纯 ASCII:macOS 的 makensis 是 ANSI-only 构建,脚本里出现
# 任何非 ASCII 字节都会 "Bad text encoding",-INPUTCHARSET 与 BOM 都不管用。
"$MAKENSIS" -V2 \
  -DSOURCE_DIR="$UNPACKED" \
  -DOUT_FILE="$SETUP" \
  -DVERSION="$VERSION" \
  -DAPP_EXE="$APP_EXE" \
  -DBRAIN_EXE="$BRAIN_EXE" \
  -DICON_FILE="$REPO_ROOT/assets/app-icon.ico" \
  "$REPO_ROOT/scripts/installer.nsi"

echo "==> 核对产物完整性"
missing=0
for f in \
  "$UNPACKED/RecruitHelper.exe" \
  "$UNPACKED/resources/brain/$BRAIN_EXE" \
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
if [ -f "$SETUP" ]; then
  echo "  OK   $(basename "$SETUP") ($(du -h "$SETUP" | cut -f1))"
else
  echo "  缺失 $(basename "$SETUP")"
  missing=1
fi
# 插件必须带着固定 key,否则扩展 ID 会退回按加载路径派生,身份不再稳定。
if grep -q '"key"' "$UNPACKED/resources/plugin/manifest.json" 2>/dev/null; then
  echo "  OK   插件 manifest 带 key"
else
  echo "  缺失 插件 manifest 的 key 字段(扩展 ID 将不稳定)"
  missing=1
fi

if [ "$missing" -ne 0 ]; then
  echo "构建产物不完整,已中止" >&2
  exit 1
fi

echo
echo "完成:"
echo "  安装器  $SETUP"
echo "  免安装  $UNPACKED"
