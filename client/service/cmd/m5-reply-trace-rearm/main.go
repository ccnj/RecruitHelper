// Command m5-reply-trace-rearm applies the one approved M5 raw-trace re-arm.
// It is deliberately not wired into the service HTTP API or restart recovery.
package main

import (
	"flag"
	"fmt"
	"os"

	"recruithelper/client/service/internal/store"
	"recruithelper/internal/ids"
)

func main() {
	dataDir := flag.String("data", "data", "脑数据目录")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr,
			"用法: m5-reply-trace-rearm [-data DIR] <failed-selection-id> <turn-id>")
		os.Exit(2)
	}

	st, err := store.Open(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "补验授权失败: 无法打开脑账本")
		os.Exit(1)
	}
	defer st.Close()

	result, err := st.AuthorizeM5ReplyTraceRearm(store.AuthorizeM5ReplyTraceRearmRequest{
		FailedSelectionID: flag.Arg(0),
		TurnID:            flag.Arg(1),
		NewSelectionID:    ids.NewTrialSelectionID(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "补验授权失败: 前置事实未通过或事务未提交")
		os.Exit(1)
	}
	fmt.Printf("补验授权完成 selectionId=%s alreadyAuthorized=%t\n",
		result.Selection.SelectionID, result.AlreadyAuthorized)
}
