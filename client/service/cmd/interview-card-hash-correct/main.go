// 一次性事实同步工具（2026-07-27 甲方批准）：把枚举修复前由兜底分支投影的唯一
// 一张邀面卡（洪先生会话 seq=12）的派生列对齐到新解析规范。幂等，可重复执行。
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"recruithelper/client/service/internal/store"
)

func main() {
	dataDir := flag.String("data", "data", "脑数据目录")
	flag.Parse()

	st, err := store.Open(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开数据目录失败:", err)
		os.Exit(1)
	}
	defer st.Close()

	result, err := st.CorrectLegacyInterviewCardContentHash(store.CorrectLegacyInterviewCardRequest{
		Platform:        "zhilian",
		AccountRef:      "a-01f477e92d9bb925",
		ConversationRef: "e93b9135b29b5ee0d09f71a073dfbfb4",
		Seq:             12,
		LegacyHash:      "7e81f6e79dde1ad5698c14072ab8dc7e2e7db448c34fed29575349c481189922",
		CanonicalHash:   "932eab2bcbfe6a5f47d67b57b34b1c71ea434bc14d79e7fb176305387670ffcb",
		StartsAtMs:      1785218400000,
		EndsAtMs:        1785220200000,
		Now:             time.Now(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "修正失败:", err)
		os.Exit(1)
	}
	if result.AlreadyCorrected {
		fmt.Println("alreadyCorrected=true (幂等：目标行已是规范哈希)")
		return
	}
	fmt.Println("corrected=true")
}
