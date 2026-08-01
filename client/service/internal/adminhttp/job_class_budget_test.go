package adminhttp

import (
	"testing"

	"recruithelper/client/service/internal/m5ai"
)

// 全批分配的输出预算按职位数申请，但收在回复输出上限之内。这条用例钉住那个
// 上限对应多少个职位——超过它模型会被截断、解析失败、重试 3 次后整批 A 干净
// 失败（零副作用，可重跑），而不是给出一份少了几个职位的分配表。
func TestJobClassOutputTokensScalesWithJobsAndStaysInBudget(t *testing.T) {
	// 单职位时不低于原来的固定值：少给反而让模型没空间写完一句理由。
	if got := jobClassOutputTokens(1); got != m5ai.JobClassOutputTokenLimit {
		t.Fatalf("单职位应取 %d，实得 %d", m5ai.JobClassOutputTokenLimit, got)
	}
	// 中间段按职位数线性增长。
	if got := jobClassOutputTokens(8); got != jobClassTokensWrapper+jobClassTokensPerJob*8 {
		t.Fatalf("8 个职位的预算算错: %d", got)
	}
	// 永远不越回复输出上限——越了 provider 会直接判 budgetBlocked，
	// 那样连一次尝试都发不出去，比截断更难排查。
	for _, jobs := range []int{12, 13, 40, 500} {
		if got := jobClassOutputTokens(jobs); got > m5ai.ReplyOutputTokenLimit {
			t.Fatalf("%d 个职位的预算越界: %d", jobs, got)
		}
	}

	// 分块大小必须由输出预算推出来，不能是拍的：一块的输出恰好用得满、又不
	// 越界。改动 ReplyOutputTokenLimit 或每条开销估算时这条会先红。
	chunk := jobClassChunkSize()
	if chunk != 12 {
		t.Fatalf("分块大小变了: %d（原为 12），请复核输出预算与每条开销估算", chunk)
	}
	if jobClassOutputTokens(chunk) != m5ai.ReplyOutputTokenLimit {
		t.Fatalf("一块 %d 个职位应当刚好用满预算，实得 %d", chunk, jobClassOutputTokens(chunk))
	}
	if jobClassOutputTokens(chunk+1) != m5ai.ReplyOutputTokenLimit {
		t.Fatal("越界那一档必须被收回上限，否则 provider 会直接判 budgetBlocked")
	}
}
