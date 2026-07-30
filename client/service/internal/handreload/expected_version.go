package handreload

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PluginDirEnv:壳启脑时用它告诉脑「Chrome 实际加载的那个固定插件目录在哪」。
// 只有打包态会传 —— 开发期固定目录要么不存在,要么是上次装包留下的陈旧副本,
// 而 Chrome 里加载的是开发者自己的 plugin/dist,拿它当基准只会误判。
const PluginDirEnv = "RECRUITHELPER_PLUGIN_DIR"

// Chrome 扩展的 version 只接受 1-4 段纯数字(scripts/bump-version.sh 同样按这条
// 挡)。读到别的形状说明这个 manifest 不是我们认识的东西,按"不知道"处理。
var extensionVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}$`)

// ExpectedPluginVersion 读出磁盘上那一版插件的版本号。
//
// 返回空字符串一律表示「不知道」,调用方必须把它当作「本轮不做版本判断」,绝不能
// 当作「手的版本不对」。目录没传、目录不存在、manifest 读不动或版本字段是意料之
// 外的形状,全都落到这一支:一个读不到的文件不构成「该重载了」的证据,而误判的
// 代价是无谓地重载插件、顺带作废推荐流。
func ExpectedPluginVersion(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return ""
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ""
	}
	version := strings.TrimSpace(manifest.Version)
	if !extensionVersionPattern.MatchString(version) {
		return ""
	}
	return version
}
