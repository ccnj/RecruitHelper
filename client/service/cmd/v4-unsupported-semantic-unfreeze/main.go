// Command v4-unsupported-semantic-unfreeze applies the one approved batch-D
// sweep (2026-07-28 ruling): profiles frozen as unsupportedSemantic before
// mixed resume turns were activated, whose turn input is legal under the new
// shape evaluator, get an appended manualUnfreeze receipt, their turn back to
// collected and their aggregate back to active. It is deliberately not wired
// into the service binary, HTTP API, patrol loop, or restart recovery path;
// run it with the brain stopped.
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

	st, err := store.Open(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "解冻失败: 无法打开脑账本")
		os.Exit(1)
	}
	defer st.Close()

	results, err := st.UnfreezeV4UnsupportedSemanticProfiles()
	if err != nil {
		fmt.Fprintln(os.Stderr, "解冻失败: 事务未提交")
		os.Exit(1)
	}
	unfrozen := 0
	for _, result := range results {
		if result.Unfrozen {
			unfrozen++
			fmt.Printf("已解冻 profile=%s turn=%s\n", result.ProfileID, result.TurnID)
			continue
		}
		fmt.Printf("保留冻结 profile=%s turn=%s reason=%s\n",
			result.ProfileID, result.TurnID, result.SkipReason)
	}
	fmt.Printf("解冻完成 unfrozen=%d skipped=%d\n", unfrozen, len(results)-unfrozen)
}
