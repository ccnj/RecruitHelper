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

	// 当前上限恰好够 12 个职位；第 13 个开始就要被截断。这个数字是"一批最多
	// 发几个职位"的实际天花板，改动 ReplyOutputTokenLimit 或每条开销估算时
	// 必须重新核对它。
	full := (m5ai.ReplyOutputTokenLimit - jobClassTokensWrapper) / jobClassTokensPerJob
	if full != 12 {
		t.Fatalf("输出预算够用的职位数变了: %d（原为 12），请复核规格与提示词开销", full)
	}
	if jobClassOutputTokens(full) != m5ai.ReplyOutputTokenLimit {
		t.Fatalf("%d 个职位应当刚好用满预算", full)
	}
}
