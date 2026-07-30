package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"0.2.3", "0.2.2", 1},
		{"0.2.2", "0.2.3", -1},
		{"0.2.2", "0.2.2", 0},
		{"0.3", "0.3.0", 0}, // 缺省段按 0，两者是同一个版本
		{"1.0", "0.9.9.9", 1},
		{"0.2.10", "0.2.9", 1}, // 按数值比，不是按字典序
		{"1.2.3.4", "1.2.3.5", -1},
	} {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q,%q)=%d，期望 %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestParseFeedAcceptsWellFormed(t *testing.T) {
	feed, err := ParseFeed([]byte(`{
		"version":"0.2.3","path":"pkg/RecruitHelper-0.2.3-setup.exe",
		"sha256":"` + strings.Repeat("a", 64) + `","size":1024,"notes":"修了个 bug"}`))
	if err != nil {
		t.Fatal(err)
	}
	if feed.Version != "0.2.3" || feed.Size != 1024 || feed.Notes != "修了个 bug" {
		t.Fatalf("解析结果不对: %+v", feed)
	}
}

func TestParseFeedRejectsMalformed(t *testing.T) {
	good := map[string]any{
		"version": "0.2.3", "path": "pkg/app.exe",
		"sha256": strings.Repeat("a", 64), "size": 1024,
	}
	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{"版本号带后缀", "version", "0.2.3-beta"},
		{"版本号为空", "version", ""},
		{"版本号超过四段", "version", "1.2.3.4.5"},
		{"哈希长度不对", "sha256", strings.Repeat("a", 63)},
		{"哈希含非十六进制", "sha256", strings.Repeat("g", 64)},
		{"大小为零", "size", 0},
		{"大小为负", "size", -1},
		{"大小超上限", "size", int64(1) << 40},
		{"路径为空", "path", ""},
		// 以下几条是安全关键：清单走明文 http，一旦被改就能把客户端引去别处取
		// 可执行文件。
		{"路径是绝对 URL", "path", "http://evil.example/x.exe"},
		{"路径是协议相对 URL", "path", "//evil.example/x.exe"},
		{"路径以斜杠开头", "path", "/etc/passwd"},
		{"路径含上跳", "path", "../../etc/passwd"},
		{"路径含反斜杠", "path", `pkg\..\..\x.exe`},
		{"路径含查询串", "path", "pkg/app.exe?x=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{}
			for k, v := range good {
				payload[k] = v
			}
			payload[tc.field] = tc.value
			raw, _ := json.Marshal(payload)
			if _, err := ParseFeed(raw); err == nil {
				t.Fatalf("非法清单必须整体拒绝: %s", raw)
			}
		})
	}
}

func TestPackageURLStaysWithinFeedDirectory(t *testing.T) {
	feed := Feed{Path: "pkg/app.exe"}
	got, err := PackageURL("http://example.com/rh-updates/", feed)
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://example.com/rh-updates/pkg/app.exe" {
		t.Fatalf("拼接结果不对: %s", got)
	}
	// feed 目录写法不带尾斜杠时也不该把最后一段吃掉。
	if got, err = PackageURL("http://example.com/rh-updates", feed); err != nil ||
		got != "http://example.com/rh-updates/pkg/app.exe" {
		t.Fatalf("无尾斜杠的 feed 地址拼接不对: %s err=%v", got, err)
	}
}

// updateSource 是一个假更新源:按需返回清单与包体,并记下包体被取了几次。
type updateSource struct {
	server    *httptest.Server
	feed      []byte
	pkg       []byte
	pkgHits   int
	pkgStatus int
}

func newUpdateSource(t *testing.T) *updateSource {
	t.Helper()
	src := &updateSource{pkgStatus: http.StatusOK}
	src.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, FeedName):
			if src.feed == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(src.feed)
		case strings.HasSuffix(r.URL.Path, ".exe"):
			src.pkgHits++
			if src.pkgStatus != http.StatusOK {
				w.WriteHeader(src.pkgStatus)
				return
			}
			_, _ = w.Write(src.pkg)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(src.server.Close)
	return src
}

// publish 摆好一个版本。declaredSHA 非空时用它覆盖真实哈希,用来造"包坏了"。
func (s *updateSource) publish(version string, body []byte, declaredSHA string) {
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	if declaredSHA != "" {
		sha = declaredSHA
	}
	s.pkg = body
	s.feed = []byte(fmt.Sprintf(
		`{"version":%q,"path":"pkg/RecruitHelper-%s-setup.exe","sha256":%q,"size":%d}`,
		version, version, sha, len(body)))
}

func newTestChecker(t *testing.T, src *updateSource, current string) *Checker {
	t.Helper()
	checker := New(src.server.URL+"/rh-updates/", current, t.TempDir(), time.Hour)
	if checker == nil {
		t.Fatal("配置齐全时不该返回 nil")
	}
	return checker
}

func TestNewRejectsIncompleteConfiguration(t *testing.T) {
	// 缺配置是开发期常态。返回 nil 让调用方不启用自更新,好过用一组猜出来的默认值
	// 去连一个不该连的地方。
	for _, tc := range []struct{ name, feed, version, dir string }{
		{"缺更新源", "", "0.2.2", "/tmp/x"},
		{"缺当前版本", "http://x/", "", "/tmp/x"},
		{"当前版本形状不对", "http://x/", "dev", "/tmp/x"},
		{"缺下载目录", "http://x/", "0.2.2", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if New(tc.feed, tc.version, tc.dir, time.Hour) != nil {
				t.Fatal("配置不全时必须返回 nil")
			}
		})
	}
}

func TestCheckOnceDownloadsAndVerifies(t *testing.T) {
	src := newUpdateSource(t)
	body := []byte("pretend this is a 95MB installer")
	src.publish("0.2.3", body, "")
	checker := newTestChecker(t, src, "0.2.2")

	if err := checker.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := checker.Status()
	if !status.Available || status.Version != "0.2.3" || !status.Ready {
		t.Fatalf("新版应被发现并备好: %+v", status)
	}
	saved, err := os.ReadFile(checker.PackagePath("0.2.3"))
	if err != nil || string(saved) != string(body) {
		t.Fatalf("安装包未落盘或内容不对: err=%v", err)
	}
}

func TestCheckOnceIgnoresVersionNotNewer(t *testing.T) {
	src := newUpdateSource(t)
	src.publish("0.2.2", []byte("same version"), "")
	checker := newTestChecker(t, src, "0.2.2")

	if err := checker.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := checker.Status(); status.Available || status.Ready {
		t.Fatalf("同版本不该被当成新版: %+v", status)
	}
	if src.pkgHits != 0 {
		t.Fatalf("不该下载，实际取了 %d 次包", src.pkgHits)
	}
}

func TestCheckOnceDiscardsPackageFailingChecksum(t *testing.T) {
	// 校验不过的东西一个字节都不能留在最终路径上 —— 那是个待执行的文件,而下一轮
	// 会把最终路径上的文件当成"已经备好"。
	src := newUpdateSource(t)
	src.publish("0.2.3", []byte("corrupted payload"), strings.Repeat("b", 64))
	checker := newTestChecker(t, src, "0.2.2")

	if err := checker.CheckOnce(context.Background()); err == nil {
		t.Fatal("哈希不符必须报错")
	}
	if _, err := os.Stat(checker.PackagePath("0.2.3")); !os.IsNotExist(err) {
		t.Fatal("校验失败的包必须被丢弃，不得留在最终路径上")
	}
	if status := checker.Status(); !status.Available || status.Ready {
		t.Fatalf("应报告有新版但未备好: %+v", status)
	}
	// 半截文件也不能留在目录里冒充别的东西。
	entries, _ := os.ReadDir(checker.DownloadDir)
	for _, entry := range entries {
		t.Fatalf("下载目录应保持干净，残留 %s", entry.Name())
	}
}

func TestCheckOnceStopsRetryingAPersistentlyBadPackage(t *testing.T) {
	// 坏包重下一百次还是坏包,继续只是白耗带宽。
	src := newUpdateSource(t)
	src.publish("0.2.3", []byte("corrupted payload"), strings.Repeat("b", 64))
	checker := newTestChecker(t, src, "0.2.2")

	for i := 0; i < maxVerifyFailures; i++ {
		if err := checker.CheckOnce(context.Background()); err == nil {
			t.Fatalf("第 %d 轮应失败", i+1)
		}
	}
	hitsBefore := src.pkgHits
	if err := checker.CheckOnce(context.Background()); err == nil {
		t.Fatal("超过上限后仍应报错")
	}
	if src.pkgHits != hitsBefore {
		t.Fatalf("超过上限后不得再下载，又取了 %d 次", src.pkgHits-hitsBefore)
	}
}

func TestCheckOnceReusesAlreadyVerifiedPackage(t *testing.T) {
	// 重启后不该为同一个版本再下一次 95MB。认账的依据是重算哈希,不是文件名 ——
	// 磁盘上的东西可能被动过。
	src := newUpdateSource(t)
	src.publish("0.2.3", []byte("installer bytes"), "")
	checker := newTestChecker(t, src, "0.2.2")

	if err := checker.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	hitsAfterFirst := src.pkgHits
	if err := checker.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if src.pkgHits != hitsAfterFirst {
		t.Fatalf("已备好的版本不该重复下载，又取了 %d 次", src.pkgHits-hitsAfterFirst)
	}
	if !checker.Status().Ready {
		t.Fatal("复用本地包后仍应报告已备好")
	}
}

func TestCheckOnceRedownloadsWhenLocalPackageWasTamperedWith(t *testing.T) {
	src := newUpdateSource(t)
	body := []byte("installer bytes")
	src.publish("0.2.3", body, "")
	checker := newTestChecker(t, src, "0.2.2")
	if err := checker.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// 有人动了本地那个包。
	if err := os.WriteFile(checker.PackagePath("0.2.3"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	hitsBefore := src.pkgHits
	if err := checker.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if src.pkgHits == hitsBefore {
		t.Fatal("本地包被改过时必须重下，不能凭文件名认账")
	}
	saved, _ := os.ReadFile(checker.PackagePath("0.2.3"))
	if string(saved) != string(body) {
		t.Fatal("重下后内容应恢复为清单声明的那一份")
	}
}

func TestCheckOnceRejectsSizeMismatch(t *testing.T) {
	// 清单说 N 字节、实体给了别的,说明清单与实体对不上,不能只信哈希。
	src := newUpdateSource(t)
	src.publish("0.2.3", []byte("declared length"), "")
	src.pkg = []byte("this body is much longer than declared")
	checker := newTestChecker(t, src, "0.2.2")

	if err := checker.CheckOnce(context.Background()); err == nil {
		t.Fatal("大小与清单不符必须报错")
	}
	if _, err := os.Stat(checker.PackagePath("0.2.3")); !os.IsNotExist(err) {
		t.Fatal("大小不符的包不得落到最终路径")
	}
}

func TestCheckOnceSurfacesFeedFailures(t *testing.T) {
	src := newUpdateSource(t)
	src.feed = nil // 404
	checker := newTestChecker(t, src, "0.2.2")
	if err := checker.CheckOnce(context.Background()); err == nil {
		t.Fatal("清单取不到时应报错，由调用方退避重试")
	}
	if status := checker.Status(); status.Available || status.Ready {
		t.Fatalf("取不到清单时不得凭空报告有新版: %+v", status)
	}
}

func TestCheckOnceRejectsMalformedFeedWithoutDownloading(t *testing.T) {
	src := newUpdateSource(t)
	src.feed = []byte(`{"version":"0.2.3-beta","path":"pkg/x.exe","sha256":"z","size":1}`)
	checker := newTestChecker(t, src, "0.2.2")
	if err := checker.CheckOnce(context.Background()); err == nil {
		t.Fatal("非法清单必须报错")
	}
	if src.pkgHits != 0 {
		t.Fatal("清单不合法时一个字节都不该下载")
	}
}

func TestJitterSpreadsWithoutShiftingTheAverage(t *testing.T) {
	// 抖动的意义是把同时开机的客户散开;它必须真的散(不是常量),又不能把间隔拉到
	// 离谱的地方去。
	const base = time.Hour
	seen := map[time.Duration]bool{}
	var sum time.Duration
	const samples = 500
	for i := 0; i < samples; i++ {
		got := jitter(base)
		if got < base*3/4 || got > base*5/4 {
			t.Fatalf("抖动越界: %v 不在 [45m, 75m] 内", got)
		}
		seen[got] = true
		sum += got
	}
	if len(seen) < samples/2 {
		t.Fatalf("抖动没有真正散开，%d 次只产生 %d 个不同值", samples, len(seen))
	}
	// 期望值应当仍是基数附近，否则等于偷偷改了检查频率。
	average := sum / samples
	if average < base*9/10 || average > base*11/10 {
		t.Fatalf("抖动把平均间隔挪走了: %v", average)
	}
}

func TestJitterHandlesNonPositiveInput(t *testing.T) {
	if got := jitter(0); got != 0 {
		t.Fatalf("零间隔应原样返回，得到 %v", got)
	}
	if got := jitter(-time.Second); got != -time.Second {
		t.Fatalf("负间隔应原样返回，得到 %v", got)
	}
}

func TestRepeatedFailuresAreLoggedOnce(t *testing.T) {
	// 15 分钟一轮,一台断网的机器一天会走近百轮。同一句话刷上百条,真正的异常反而
	// 被埋掉。
	src := newUpdateSource(t)
	checker := newTestChecker(t, src, "0.2.2")
	sameError := fmt.Errorf("dial tcp: connection refused")

	if !checker.noteFailure(sameError) {
		t.Fatal("首次失败必须报告")
	}
	for i := 0; i < 10; i++ {
		if checker.noteFailure(sameError) {
			t.Fatalf("连续同类失败不该重复报告，第 %d 次又报了", i+2)
		}
	}
	// 换了一种失败,说明情况变了,值得再说一次。
	if !checker.noteFailure(fmt.Errorf("更新清单响应 500")) {
		t.Fatal("失败原因变化时应重新报告")
	}
	// 恢复正常后再失败，也该重新说一次 —— 那是一次新的中断。
	checker.noteSuccess()
	if !checker.noteFailure(sameError) {
		t.Fatal("恢复后再次失败应重新报告")
	}
}

func TestStatusOnNilCheckerIsQuiet(t *testing.T) {
	var checker *Checker
	if status := checker.Status(); status.Available || status.Ready {
		t.Fatal("未启用自更新时应返回空状态而不是 panic")
	}
	checker.Run(context.Background()) // 不得 panic
}

func TestPackagePathLandsInDownloadDir(t *testing.T) {
	src := newUpdateSource(t)
	checker := newTestChecker(t, src, "0.2.2")
	path := checker.PackagePath("0.2.3")
	if filepath.Dir(path) != checker.DownloadDir {
		t.Fatalf("安装包必须落在下载目录内: %s", path)
	}
	if !strings.HasSuffix(path, "RecruitHelper-0.2.3-setup.exe") {
		t.Fatalf("文件名应带版本号便于人工核对: %s", path)
	}
}
