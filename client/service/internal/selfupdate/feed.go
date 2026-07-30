// Package selfupdate 是客户端版本更新的「发现」一半:读更新源清单、下载安装包、
// 校验完整性。它**不安装**,也不结束任何进程 —— 什么时候装、能不能装是另一件事
// (AGENTS.md「全局约定·客户端版本更新源」:安装时机由脑按业务状态裁决)。
//
// 出站边界照抄那条规范:匿名只读 GET,不带业务数据与凭据;响应只用于"有没有新版、
// 从哪下载、下载物是否完整"三件事,任何字段都不得成为业务裁决或配置的来源。
package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// DefaultFeedURL 是我方自建的只读静态更新源。写死在代码里 —— 规范禁止它经旧后台、
// 插件或 AI 下发,那些通道一旦能改更新源地址,就等于能让客户端去任意地方取可执行文件。
const DefaultFeedURL = "http://8.153.161.25/rh-updates/"

// FeedName 是清单在更新源目录下的固定文件名。
const FeedName = "latest.json"

// maxPackageBytes 是能接受的安装包上限。当前包约 95MB;这个上限挡的是服务器错配或
// 清单被改坏时把客户端磁盘写爆。
const maxPackageBytes int64 = 512 << 20

var (
	// 版本号沿用 Chrome 扩展那套 1-4 段纯数字(scripts/bump-version.sh 同样按这条挡),
	// 客户端与插件永远同号,不该在这里放宽出第二种形状。
	versionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)

	ErrFeedMalformed = errors.New("更新清单格式不合法")
)

// Feed 是更新源上的版本清单。字段少是有意的:多一个字段就多一条"客户端会照着做"
// 的通道,而这条通道只被允许回答三个问题。
type Feed struct {
	Version string `json:"version"`
	// Path 是安装包相对更新源目录的路径。**只接受相对路径** —— 若允许写绝对 URL,
	// 清单一旦被改就能把客户端引到任意主机去取可执行文件,而清单走的是明文 http。
	// 相对路径把下载目标钉死在更新源自己的目录下。
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Notes  string `json:"notes,omitempty"`
}

// ParseFeed 解析并校验清单。任何一处不合法都整体拒绝 —— 半信半疑地用一个坏清单去
// 下载可执行文件,比当作"没有新版"危险得多。
func ParseFeed(raw []byte) (Feed, error) {
	var feed Feed
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&feed); err != nil {
		return Feed{}, fmt.Errorf("%w: %v", ErrFeedMalformed, err)
	}
	feed.Version = strings.TrimSpace(feed.Version)
	feed.Path = strings.TrimSpace(feed.Path)
	feed.SHA256 = strings.ToLower(strings.TrimSpace(feed.SHA256))
	feed.Notes = strings.TrimSpace(feed.Notes)

	if !versionPattern.MatchString(feed.Version) {
		return Feed{}, fmt.Errorf("%w: version=%q", ErrFeedMalformed, feed.Version)
	}
	if !sha256Pattern.MatchString(feed.SHA256) {
		return Feed{}, fmt.Errorf("%w: sha256 必须是 64 位小写十六进制", ErrFeedMalformed)
	}
	if feed.Size <= 0 || feed.Size > maxPackageBytes {
		return Feed{}, fmt.Errorf("%w: size=%d 超出可接受范围", ErrFeedMalformed, feed.Size)
	}
	if err := validRelativePath(feed.Path); err != nil {
		return Feed{}, fmt.Errorf("%w: %v", ErrFeedMalformed, err)
	}
	if len(feed.Notes) > 500 {
		feed.Notes = feed.Notes[:500]
	}
	return feed, nil
}

// validRelativePath 把下载目标限制在更新源目录之内。
func validRelativePath(path string) error {
	if path == "" {
		return errors.New("path 为空")
	}
	if strings.Contains(path, "://") || strings.HasPrefix(path, "//") {
		return errors.New("path 只接受相对路径，不接受绝对 URL")
	}
	if strings.HasPrefix(path, "/") {
		return errors.New("path 不得以 / 开头")
	}
	// ".." 能爬出更新源目录;虽然服务端未必允许,但客户端不该依赖服务端来拦。
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return errors.New("path 不得包含 ..")
		}
	}
	if strings.ContainsAny(path, "?#\\") {
		return errors.New("path 不得包含查询串、片段或反斜杠")
	}
	return nil
}

// PackageURL 把清单里的相对路径拼到更新源目录上。
func PackageURL(feedURL string, feed Feed) (string, error) {
	base, err := url.Parse(feedURL)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	target, err := url.Parse(feed.Path)
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(target)
	// ResolveReference 对合法的相对路径不会换主机,这里再确认一次:校验与使用之间
	// 隔着一次字符串解析,而下载的是可执行文件。
	if resolved.Host != base.Host || resolved.Scheme != base.Scheme {
		return "", errors.New("解析后的下载地址离开了更新源主机")
	}
	if !strings.HasPrefix(resolved.Path, base.Path) {
		return "", errors.New("解析后的下载地址离开了更新源目录")
	}
	return resolved.String(), nil
}

// CompareVersions 比较两个 1-4 段版本号:a 大于 b 返回正数,小于返回负数,相等返回 0。
// 缺省段按 0 处理,所以 "0.3" 与 "0.3.0" 相等。
func CompareVersions(a, b string) int {
	left, right := splitVersion(a), splitVersion(b)
	for i := 0; i < 4; i++ {
		if left[i] != right[i] {
			if left[i] > right[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

func splitVersion(v string) [4]int {
	var parts [4]int
	for i, segment := range strings.Split(strings.TrimSpace(v), ".") {
		if i > 3 {
			break
		}
		n, err := strconv.Atoi(segment)
		if err != nil {
			return [4]int{}
		}
		parts[i] = n
	}
	return parts
}

// ValidVersion 报告一个版本号是否是本产品认的形状。
func ValidVersion(v string) bool {
	return versionPattern.MatchString(strings.TrimSpace(v))
}
