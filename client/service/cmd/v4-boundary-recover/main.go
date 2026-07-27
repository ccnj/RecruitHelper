// Command v4-boundary-recover applies the one approved recovery for profiles
// wrongly frozen as outboundBoundaryMissing by the pre-decoupling cursor
// assertion (0727当日计划3). It is deliberately not wired into the service
// binary, HTTP API, patrol loop, or restart recovery path; run it with the
// brain stopped, one profile at a time, under human supervision.
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
		fmt.Fprintln(os.Stderr, "用法: v4-boundary-recover [-data DIR] <profile-id>")
		os.Exit(2)
	}

	st, err := store.Open(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "恢复失败: 无法打开脑账本")
		os.Exit(1)
	}
	defer st.Close()

	result, err := st.RecoverV4OutboundBoundaryLock(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "恢复失败: 前置事实未通过或事务未提交")
		os.Exit(1)
	}
	fmt.Printf(
		"恢复完成 applied=%t alreadyRecovered=%t cursorSeq=%d anchorSeq=%d uncoveredInboundSeq=%d\n",
		result.Applied,
		result.AlreadyRecovered,
		result.CursorSeq,
		result.AnchorSeq,
		result.UncoveredInboundSeq,
	)
}
