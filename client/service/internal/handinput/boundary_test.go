package handinput

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const selfPkg = "recruithelper/client/service/internal/handinput"

// 只有 main.go 可以 import 本包。
//
// 这条比「不许 import 业务包」更严也更好查:本包是**手的另一半**,它的唯一消费者
// 是进程装配。任何业务包 import 了它,就说明脑开始直接指挥鼠标了——那正是
// 「坐标不许进协议」要防的形状,而且一旦发生,事后从代码里很难一眼看出来。
func TestOnlyProcessAssemblyImportsHandInput(t *testing.T) {
	allowed := map[string]bool{"client/service/main.go": true}
	root := repoRoot(t)
	fset := token.NewFileSet()
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "data", "release", ".claude":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "client/service/internal/handinput/") || allowed[rel] {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil
		}
		for _, im := range f.Imports {
			if strings.Trim(im.Path.Value, `"`) == selfPkg {
				offenders = append(offenders, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("handinput 只允许被进程装配 import,以下文件越界了:%v\n"+
			"它是手的另一半、租住在脑的地址空间里,不是脑的能力;业务层永远不该知道有坐标这回事", offenders)
	}
}

// 反向:本包不许 import 任何业务包。
//
// 与上一条合起来是一条双向的墙。少了这一条,本包会慢慢长出「读一下当前工作流状态
// 再决定怎么动鼠标」这类东西,而那就是在**决定**,不是在执行——数据/过程的分界一破,
// 上游的引擎就搬不回去了。
func TestHandInputImportsNoBusinessPackage(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, perr := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatal(perr)
		}
		for _, im := range f.Imports {
			p := strings.Trim(im.Path.Value, `"`)
			if strings.HasPrefix(p, "recruithelper/") && p != selfPkg {
				t.Fatalf("%s import 了 %s——本包只许依赖标准库与系统调用封装", e.Name(), p)
			}
		}
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
