#!/usr/bin/env bash
# 统一升版本号。客户端与插件永远同号 —— 交付物是一个整包,插件只随安装包过去,
# 分开编号只会让"你装的哪版"变成要对两个号的问题。
#
#   ./scripts/bump-version.sh 0.2.0
#
# 改完自己 git diff 过目再提交,脚本不碰 git。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

if [ $# -ne 1 ]; then
  echo "用法: ./scripts/bump-version.sh <新版本号>" >&2
  exit 1
fi
VERSION="$1"

# Chrome 扩展的 version 只接受 1-4 段纯数字。0.2.0-beta 这类后缀会让 Chrome
# 直接拒绝加载扩展,而那要装到机器上才会发现,所以在这里就挡住。
if ! printf '%s' "$VERSION" | grep -qE '^[0-9]+(\.[0-9]+){0,3}$'; then
  echo "版本号必须是 1-4 段纯数字(Chrome 扩展限制,不接受 -beta 等后缀):$VERSION" >&2
  exit 1
fi

# 前两个是真正被消费的:electron 的 version 决定 setup.exe 文件名与控制面板里的
# DisplayVersion;manifest 的 version 由插件上报给脑,显示在诊断台。其余三个没有
# 任何构建或运行时读取,一并改只是不想在同一个仓库里留下三个各不相同的版本号。
FILES=(
  client/electron/package.json
  plugin/manifest.json
  package.json
  client/ui/package.json
  plugin/package.json
)

for file in "${FILES[@]}"; do
  before="$(node -p "require('./$file').version" 2>/dev/null || echo '?')"
  # -0777 整文件读入,s/// 因此只替换第一处 "version" —— 正好是顶层那个。
  # manifest.json 的 "manifest_version": 3 值不带引号,不会被这条正则命中。
  perl -0777 -pi -e 's/("version"\s*:\s*")[^"]*(")/${1}'"$VERSION"'${2}/' "$file"
  after="$(node -p "require('./$file').version" 2>/dev/null || echo '?')"
  if [ "$after" != "$VERSION" ]; then
    echo "  失败 $file(仍是 $after)" >&2
    exit 1
  fi
  printf "  OK   %-32s %s → %s\n" "$file" "$before" "$after"
done

echo
# 花括号在这里不是可有可无:macOS 自带的 bash 3.2 在 UTF-8 locale 下,会把紧跟
# 变量名的中文首字节吞进变量名(去找 VERSION\xe3),set -u 随即以 unbound variable
# 退出。五处版本号那时已经全改完,只有这句提示没打出来,却留下一个非零退出码——
# 单独跑看不出异常,用 && 串联后续构建时才会被挡住。凡变量后面紧跟中文,都要
# 加花括号。
echo "已全部改为 ${VERSION}。检查 git diff 后提交,再跑 ./scripts/build-win.sh 打包。"
