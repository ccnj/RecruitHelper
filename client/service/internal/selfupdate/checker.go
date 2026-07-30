package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultInterval:清单只有几百字节,查得勤一点几乎不花什么,换来的是发布后
	// 客户很快就能拿到。真正的负载是那 96MB 的包,而它下好校验过就不再重下。
	DefaultInterval = 15 * time.Minute
	// InitialDelay:启动那阵子壳、脑、插件都在忙,让一让。
	InitialDelay = 30 * time.Second
	// feedTimeout 只取一个几百字节的 JSON。
	feedTimeout = 30 * time.Second
	// downloadTimeout 覆盖约 95MB 的整包下载,给慢网络留足余量。
	downloadTimeout = 30 * time.Minute
	// maxVerifyFailures:同一个版本连续校验失败这么多次就不再重下。坏包重下一百次
	// 还是坏包,继续只是白耗带宽;真修好了会换版本号或经客户端重启再来。
	maxVerifyFailures = 3
	// UpdateDirEnv / AppVersionEnv:壳启脑时告诉脑安装包往哪放、自己是哪一版。
	// 开发期两者都不传,自更新检查因此整个不启用。
	UpdateDirEnv  = "RECRUITHELPER_UPDATE_DIR"
	AppVersionEnv = "RECRUITHELPER_APP_VERSION"
)

// Status 是给产品 UI 的只读投影。它只回答"有没有新版、备好了没有",不含地址、
// 哈希与任何可据以动作的东西 —— 装不装是脑的裁决,不是 UI 的。
type Status struct {
	CurrentVersion string `json:"currentVersion,omitempty"`
	Available      bool   `json:"available"`
	Version        string `json:"version,omitempty"`
	// Ready 为真表示安装包已经在本机、且 sha256 与清单一致。
	Ready bool   `json:"ready"`
	Notes string `json:"notes,omitempty"`
	// CheckedAt 是最近一次成功读到清单的时刻,读不到时保持上次的值。
	CheckedAt time.Time `json:"checkedAt,omitempty"`
}

// Checker 周期性地问更新源"有没有新版",有就把包下到本地并校验。
//
// 它到此为止:不安装、不结束进程、不碰业务。批 1 的全部意义是让"新版包已经在客户
// 机上、且完整"成为一个事实,安装是另一批的事。
type Checker struct {
	FeedURL        string
	CurrentVersion string
	DownloadDir    string
	HTTP           *http.Client
	Interval       time.Duration

	mu          sync.Mutex
	status      Status
	failures    map[string]int
	lastFailure string // 上次报告过的失败原因，只为日志去重
	// readySHA256 是已备好那个包的期望哈希。留着是为了安装前再验一次:下载与
	// 安装之间隔着任意长的时间,磁盘上的东西可能被动过,而那是要执行的文件。
	readySHA256 string
}

// ReadyPackage 返回已备好的安装包路径与它的期望哈希。ok 为假表示当前没有可装的。
func (c *Checker) ReadyPackage() (path, sha256Hex, version string, ok bool) {
	if c == nil {
		return "", "", "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.status.Ready || c.status.Version == "" || c.readySHA256 == "" {
		return "", "", "", false
	}
	return c.packagePath(c.status.Version), c.readySHA256, c.status.Version, true
}

// New 返回一个可用的 Checker;配置不全时返回 nil,调用方据此不启用自更新。
// 缺配置是开发期的常态,不该让脑起不来,也不该悄悄用一组猜出来的默认值。
func New(feedURL, currentVersion, downloadDir string, interval time.Duration) *Checker {
	feedURL = strings.TrimSpace(feedURL)
	currentVersion = strings.TrimSpace(currentVersion)
	downloadDir = strings.TrimSpace(downloadDir)
	if feedURL == "" || downloadDir == "" || !ValidVersion(currentVersion) {
		return nil
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Checker{
		FeedURL:        feedURL,
		CurrentVersion: currentVersion,
		DownloadDir:    downloadDir,
		Interval:       interval,
		HTTP:           &http.Client{},
		failures:       map[string]int{},
		status:         Status{CurrentVersion: currentVersion},
	}
}

// Status 返回当前投影的快照。
func (c *Checker) Status() Status {
	if c == nil {
		return Status{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// jitter 把一个固定间隔散成 0.75~1.25 倍,期望值仍是原值。
//
// 固定间隔会把本来分散的客户端**同步化**:这是个上班时间用的工具,客户多半在同一
// 个早晨时段开机,固定首检延迟就意味着他们在同一分钟一起来取 96MB 的包。更糟的是
// 失败之后 —— 所有人同一刻超时、又在同一刻重试,同步性反而被强化。抖动只改时刻、
// 不改期望频率,代价接近于零。
//
// 它治的是"撞在一起",治不了带宽本身:出口就那么宽,客户数上去之后该换 CDN 还是
// 得换。
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return time.Duration(float64(d) * (0.75 + rand.Float64()*0.5))
}

// Run 周期检查,直到 ctx 取消。首次检查延后 InitialDelay(带抖动)。
func (c *Checker) Run(ctx context.Context) {
	if c == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter(InitialDelay)):
	}
	for {
		err := c.CheckOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			// 网络不通、404、超时都走这里。更新源不可达不是故障,下一轮再说 ——
			// 但只在"由通转不通"时说一次:15 分钟一轮,一台断网的机器不该在日志里
			// 每天刷出上百条同样的话。
			if c.noteFailure(err) {
				slog.Info("检查客户端更新未成功，稍后重试", "err", err)
			}
		} else if err == nil {
			c.noteSuccess()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(c.Interval)):
		}
	}
}

// noteFailure 报告这次失败是否值得说出来:同一类失败连着发生时只说第一次。
func (c *Checker) noteFailure(err error) bool {
	message := err.Error()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastFailure == message {
		return false
	}
	c.lastFailure = message
	return true
}

func (c *Checker) noteSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastFailure = ""
}

// CheckOnce 走一遍:取清单 → 比版本 → 本地已备好就认账 → 否则下载校验。
func (c *Checker) CheckOnce(ctx context.Context) error {
	feed, err := c.fetchFeed(ctx)
	if err != nil {
		return err
	}

	if CompareVersions(feed.Version, c.CurrentVersion) <= 0 {
		c.mu.Lock()
		c.status = Status{
			CurrentVersion: c.CurrentVersion, Available: false, CheckedAt: time.Now(),
		}
		c.mu.Unlock()
		return nil
	}

	c.mu.Lock()
	c.status = Status{
		CurrentVersion: c.CurrentVersion, Available: true, Version: feed.Version,
		Notes: feed.Notes, CheckedAt: time.Now(),
	}
	failures := c.failures[feed.Version]
	c.mu.Unlock()

	target := c.packagePath(feed.Version)
	// 已经下过就别再下一次 95MB。不信任文件名,重算哈希 —— 磁盘上的东西可能被动过,
	// 而这是个待执行的文件。
	if verifyFile(target, feed.SHA256) == nil {
		c.markReady(feed.Version, feed.SHA256)
		return nil
	}
	if failures >= maxVerifyFailures {
		return fmt.Errorf("版本 %s 已连续 %d 次校验失败，停止重下", feed.Version, failures)
	}
	if err := c.download(ctx, feed, target); err != nil {
		c.mu.Lock()
		c.failures[feed.Version]++
		c.mu.Unlock()
		return err
	}
	c.markReady(feed.Version, feed.SHA256)
	slog.Info("新版客户端已下载并校验通过，等待安装时机",
		"version", feed.Version, "current", c.CurrentVersion)
	return nil
}

func (c *Checker) markReady(version, sha256Hex string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.Version == version {
		c.status.Ready = true
		c.readySHA256 = sha256Hex
	}
	delete(c.failures, version)
}

// PackagePath 是某个版本安装包在本机的落点。安装那一批要用它。
func (c *Checker) PackagePath(version string) string { return c.packagePath(version) }

func (c *Checker) packagePath(version string) string {
	return filepath.Join(c.DownloadDir, fmt.Sprintf("RecruitHelper-%s-setup.exe", version))
}

func (c *Checker) fetchFeed(ctx context.Context) (Feed, error) {
	feedURL := strings.TrimSuffix(c.FeedURL, "/") + "/" + FeedName
	ctx, cancel := context.WithTimeout(ctx, feedTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return Feed{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Feed{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Feed{}, fmt.Errorf("更新清单响应 %d", resp.StatusCode)
	}
	// 清单是几百字节的东西;设个上限免得服务器错配时把响应体读成无底洞。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Feed{}, err
	}
	return ParseFeed(raw)
}

// download 先写临时文件,校验通过才改名到最终路径 —— 中断的残片绝不能占着最终
// 文件名,否则下一轮会把半个包当成"已经备好"。
func (c *Checker) download(ctx context.Context, feed Feed, target string) error {
	packageURL, err := PackageURL(c.FeedURL, feed)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.DownloadDir, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, packageURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载安装包响应 %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp(c.DownloadDir, "download-*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 成功改名后这里是 no-op

	digest := sha256.New()
	// 多读一个字节:正好读满上限说明服务器给的比声明的多,那是清单与实体对不上。
	written, err := io.Copy(io.MultiWriter(tmp, digest), io.LimitReader(resp.Body, feed.Size+1))
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if written != feed.Size {
		return fmt.Errorf("安装包大小与清单不符：声明 %d，实收 %d", feed.Size, written)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != feed.SHA256 {
		return fmt.Errorf("安装包 sha256 与清单不符，已丢弃")
	}
	return os.Rename(tmpName, target)
}

// verifyFile 重算文件哈希。nil 表示这个文件就是清单说的那一个。
func verifyFile(path, wantSHA256 string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != wantSHA256 {
		return errors.New("sha256 不符")
	}
	return nil
}
