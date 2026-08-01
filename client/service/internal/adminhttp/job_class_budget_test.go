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
	if got := jobClassOutputTokens(4); got != jobClassTokensWrapper+jobClassTokensPerJob*4 {
		t.Fatalf("4 个职位的预算算错: %d", got)
	}
	// 永远不越回复输出上限——越了 provider 会直接判 budgetBlocked，
	// 那样连一次尝试都发不出去，比截断更难排查。
	for _, jobs := range []int{12, 13, 40, 500} {
		if got := jobClassOutputTokens(jobs); got > m5ai.ReplyOutputTokenLimit {
			t.Fatalf("%d 个职位的预算越界: %d", jobs, got)
		}
	}

	// 分块大小必须由输出预算推出来，不能是拍的。改动 ReplyOutputTokenLimit 或
	// 每条开销估算时这条会先红。
	//
	// 2026-08-02 真机把它从 12 打到了 6：原来每条按 40 token 估，10 个职位算出
	// max_tokens=432，模型写到一半被 finish_reason=length 切断，三次尝试全废。
	// 每条改按 72 估之后一块只装得下 6 个——这正是那次事故要求的容量。
	chunk := jobClassChunkSize()
	if chunk != 6 {
		t.Fatalf("分块大小变了: %d（原为 6），请复核输出预算与每条开销估算", chunk)
	}
	// 一整块要装得进预算，再多一个就装不进——这才是"块大小由预算推出来"的
	// 真正判据。不要求恰好用满：整除留下的零头是余量，不是缺陷。
	if jobClassOutputTokens(chunk) > m5ai.ReplyOutputTokenLimit {
		t.Fatalf("一块 %d 个职位就越界了: %d", chunk, jobClassOutputTokens(chunk))
	}
	if jobClassTokensWrapper+jobClassTokensPerJob*(chunk+1) <= m5ai.ReplyOutputTokenLimit {
		t.Fatalf("再多一个还装得下，说明块开小了: chunk=%d", chunk)
	}
	// 越界那一档会被收回上限，而不是原样递给 provider——递过去会直接判
	// budgetBlocked，连一次尝试都发不出去，比被截断更难排查。
	if jobClassOutputTokens(chunk+1) != m5ai.ReplyOutputTokenLimit {
		t.Fatal("越界那一档必须被收回上限")
	}
}
