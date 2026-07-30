#!/usr/bin/env bash
# 把 build-win.sh 的产物发布到更新源。
#
#   ./scripts/publish-update.sh [服务器]
#
# 先读打包基线字条(release/BUILD_COMMIT,build-win.sh 落笔),再做四件事:
# 算 sha256 → 上传安装包 → 写 latest.json → 回读校验。
# 顺序是有意的:**包先上传、清单后写**。反过来的话,清单已经指向一个还没传完的
# 包,而客户端随时可能来取 —— 它会下到半截、校验失败、退避重试,直到上传结束。
# 不致命,但是白白制造一段失败窗口。
#
# 客户端认的是 sha256,不是文件名,所以重传同一版本是安全的。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

SERVER="${1:-8.153.161.25}"
REMOTE_ROOT="/srv/console/rh-updates"
FEED_URL="http://${SERVER}/rh-updates"

VERSION="$(node -p "require('./client/electron/package.json').version")"
PACKAGE="release/RecruitHelper-${VERSION}-setup.exe"

if [ ! -f "$PACKAGE" ]; then
  echo "找不到 $PACKAGE —— 先跑 ./scripts/build-win.sh" >&2
  exit 1
fi

# 清单 notes 必须记**打包时刻**的基线,不是现在的 HEAD:多会话并行下,打包到
# 发布之间主线可能被推进,现问 git 会错标(0.2.7 首发即中招)。基线由
# build-win.sh 写在 release/BUILD_COMMIT,这里只读字条。
STAMP="release/BUILD_COMMIT"
if [ ! -f "$STAMP" ]; then
  echo "找不到 $STAMP —— 产物出自没有基线字条的旧构建,先重跑 ./scripts/build-win.sh" >&2
  exit 1
fi
COMMIT="$(cat "$STAMP")"
HEAD_NOW="$(git rev-parse --short HEAD)"
if [ "$COMMIT" != "$HEAD_NOW" ]; then
  echo "注意:包基线 $COMMIT,当前 HEAD $HEAD_NOW —— 打包后主线被推进过;确认要发的就是这个包再回答 y"
fi
case "$COMMIT" in
  *-dirty)
    echo "警告:这个包出自有未提交改动的工作树,无法从 commit 完整追溯" >&2
    ;;
esac

SHA256="$(shasum -a 256 "$PACKAGE" | awk '{print $1}')"
SIZE="$(wc -c < "$PACKAGE" | tr -d ' ')"

echo "版本   $VERSION"
echo "commit $COMMIT"
echo "大小   $SIZE"
echo "sha256 $SHA256"
echo

read -r -p "确认发布到 ${FEED_URL} ? [y/N] " answer
[ "$answer" = "y" ] || { echo "已取消"; exit 1; }

ssh "root@${SERVER}" "mkdir -p ${REMOTE_ROOT}/pkg"

echo "上传安装包..."
scp "$PACKAGE" "root@${SERVER}:${REMOTE_ROOT}/pkg/"

# 清单最后写:在它落地之前,客户端看到的还是上一版,不会去取一个传了一半的包。
echo "写入清单..."
cat > /tmp/rh-latest.json <<JSON
{
  "version": "${VERSION}",
  "path": "pkg/RecruitHelper-${VERSION}-setup.exe",
  "sha256": "${SHA256}",
  "size": ${SIZE},
  "notes": "commit ${COMMIT}"
}
JSON
scp /tmp/rh-latest.json "root@${SERVER}:${REMOTE_ROOT}/latest.json"
rm -f /tmp/rh-latest.json

echo "回读校验..."
REMOTE_FEED="$(curl -fsS "${FEED_URL}/latest.json")"
echo "$REMOTE_FEED" | grep -q "\"version\": \"${VERSION}\"" || {
  echo "清单回读不含期望版本:" >&2; echo "$REMOTE_FEED" >&2; exit 1
}
echo "$REMOTE_FEED" | grep -q "$SHA256" || {
  echo "清单回读的 sha256 与本地不符" >&2; exit 1
}
# 包本身也确认一下可达:清单对了但包 404,客户端会一直退避重试。
curl -fsSI "${FEED_URL}/pkg/RecruitHelper-${VERSION}-setup.exe" > /dev/null || {
  echo "安装包在更新源上不可达" >&2; exit 1
}

echo
echo "已发布 ${VERSION}(commit ${COMMIT})。"
echo "已装客户端约 15 分钟内发现并后台下载校验;安装要等用户在客户端一键确认,确认后自动收束、安装并重启。"
