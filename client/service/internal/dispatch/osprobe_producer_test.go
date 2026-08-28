package dispatch

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// debug.osProbe 在脑侧只许有一个生产者:诊断台那个手动端点。
//
// 探针会接管鼠标。少了这道门禁,哪天有人在巡检或工作流里顺手加一句"顺便探一下",
// 鼠标就会在无人值守的运行里被接管——而且事后从代码里很难一眼看出来,因为那会是
// 一行看起来无害的调用。
//
// 两条判据一起查,少一条就有绕路:
//
//	PrimDebugOsProbe   常量。堵住"绕过 Dispatcher 自己拼 DispatchRequest"。
//	.OsProbe(          派发方法。堵住"正常调 Dispatcher"。
func TestOsProbeHasNoBusinessProducer(t *testing.T) {
	allowed := map[string]bool{
		"client/service/internal/dispatch/osprobe_dispatch.go":      true,
		"client/service/internal/dispatch/osprobe_producer_test.go": true,
		"client/service/internal/adminhttp/os_probe.go":             true,
	}
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
		if strings.Contains(text, "PrimDebugOsProbe") || strings.Contains(text, ".OsProbe(") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("debug.osProbe 只许由诊断台手动端点生产,以下文件越界了:%v\n"+
			"它会接管鼠标——业务路径一律不得铸它", offenders)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("找不到仓库根(go.mod)")
	return ""
}
