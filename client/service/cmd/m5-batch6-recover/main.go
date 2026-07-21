// Command m5-batch6-recover applies the one approved M5 batch-6 incident repair.
// It is deliberately not wired into the service binary, HTTP API, patrol loop,
// or restart recovery path.
package main

import (
	"flag"
	"fmt"
	"os"

	"recruithelper/client/service/internal/store"
)

func main() {
	dataDir := flag.String("data", "data", "脑数据目录")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "用法: m5-batch6-recover [-data DIR] <fresh-read-msg-id>")
		os.Exit(2)
	}

	st, err := store.Open(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "恢复失败: 无法打开脑账本")
		os.Exit(1)
	}
	defer st.Close()

	result, err := st.RecoverM5Batch6Incident(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "恢复失败: 前置事实未通过或事务未提交")
		os.Exit(1)
	}
	fmt.Printf(
		"恢复完成 applied=%t alreadyApplied=%t freshTailUnique=%t reachedTop=%t "+
			"sourceKeyMatched=%t contentHashMatched=%t shapeMatched=%t activeBefore=%d activeAfter=%d\n",
		result.Applied,
		result.AlreadyApplied,
		result.FreshTailUnique,
		result.ReachedTop,
		result.SourceKeyMatched,
		result.ContentHashMatched,
		result.ShapeMatched,
		result.ActiveBefore,
		result.ActiveAfter,
	)
}
