// Command v4-budget-unblock applies the one approved sweep that releases
// profiles frozen by the removed global provider-call quotas (2026-07-27
// ruling). It is deliberately not wired into the service binary, HTTP API,
// patrol loop, or restart recovery path; run it with the brain stopped.
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

	results, err := st.UnblockV4BudgetQuotaProfiles()
	if err != nil {
		fmt.Fprintln(os.Stderr, "解冻失败: 事务未提交")
		os.Exit(1)
	}
	for _, result := range results {
		fmt.Printf("已解冻 profile=%s reason=%s turnsReset=%d\n",
			result.ProfileID, result.Reason, result.TurnsReset)
	}
	fmt.Printf("解冻完成 count=%d\n", len(results))
}
