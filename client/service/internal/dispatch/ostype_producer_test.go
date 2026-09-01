package dispatch

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// debug.osType 在脑侧只许有一个生产者:诊断台那个手动端点。
//
// 与 osProbe 那道门禁同款,但**这条更要紧**:探针接管的是鼠标,顶多点错地方;
// 打字探针接管的是键盘,而键盘被接管会**往候选人的输入框里写字**。少了这道门禁,
// 哪天有人在巡检或工作流里顺手加一句"顺便打一行试试",那一行看起来无害的调用
// 就会在无人值守的运行里往真人的对话框里敲字。
//
// 两条判据一起查,少一条就有绕路:
//
//	PrimDebugOsType   常量。堵住"绕过 Dispatcher 自己拼 DispatchRequest"。
//	.OsType(          派发方法。堵住"正常调 Dispatcher"。
func TestOsTypeHasNoBusinessProducer(t *testing.T) {
	allowed := map[string]bool{
		"client/service/internal/dispatch/ostype_dispatch.go":      true,
		"client/service/internal/dispatch/ostype_producer_test.go": true,
		"client/service/internal/adminhttp/os_type.go":             true,
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
		if strings.Contains(text, "PrimDebugOsType") || strings.Contains(text, ".OsType(") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("debug.osType 只许由诊断台手动端点生产,以下文件越界了:%v\n"+
			"它会接管键盘并往输入框里写字——业务路径一律不得铸它", offenders)
	}
}
