package dispatch

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// debug.osScroll 在脑侧只许有一个生产者:诊断台那个手动端点。
//
// 它会接管鼠标并滚动页面上的任意容器。少了这道门禁,哪天有人在巡检里顺手加一句
// "翻一页看看",推荐页运行连续性与页面现场就在无人值守的运行里被动了——生产滚动能力
// (readList move=next 等)要另立、另裁,不走这条探针。两条判据一起查,少一条就有绕路。
func TestOsScrollHasNoBusinessProducer(t *testing.T) {
	allowed := map[string]bool{
		"client/service/internal/dispatch/osscroll_dispatch.go":      true,
		"client/service/internal/dispatch/osscroll_producer_test.go": true,
		"client/service/internal/adminhttp/os_scroll.go":             true,
	}
	assertSoleProducer(t, allowed, []string{"PrimDebugOsScroll", ".OsScroll("},
		"debug.osScroll 只许由诊断台手动端点生产,以下文件越界了:%v\n它会接管鼠标滚动任意容器——业务路径一律不得铸它")
}

// assertSoleProducer 扫全仓 .go 文件,除 allowed 外不得出现任何 needle。
func assertSoleProducer(t *testing.T, allowed map[string]bool, needles []string, msg string) {
	t.Helper()
	root := repoRoot(t)
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "data", "release", ".claude", "gen":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if allowed[rel] {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		text := string(b)
		for _, n := range needles {
			if strings.Contains(text, n) {
				offenders = append(offenders, rel)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf(msg, offenders)
	}
}
