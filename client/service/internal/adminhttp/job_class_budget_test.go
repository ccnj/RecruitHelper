package adminhttp

import (
	"testing"

	"recruithelper/client/service/internal/m5ai"
)

// 全批分配一次带几个职位，守的是**输入侧**：maxProviderRequestBytes 那道 256 KB
// 自限与调用延迟。它不再从输出预算推——2026-08-02 客户机 10 个职位全废，正是
// 因为把这两个不同的约束绑在了一个数上：每条按 40 token 估、算出 max_tokens=432、
// 模型写到一半被 finish_reason=length 切断。
func TestJobClassChunkSizeStaysInsideTheRequestByteGate(t *testing.T) {
	// 客户机实测：10 个职位的请求 47.9 KB，即每个职位约 4.8 KB。
	const observedBytesPerJob = 4800
	// 我们自己的保守自限，不是 provider 公布的限制（见 provider.go）。
	const requestByteGate = 256 << 10

	if jobClassChunkSize < 1 {
		t.Fatalf("一块至少要装一个职位: %d", jobClassChunkSize)
	}
	// 一整块必须离那道自限有明显余量。职位描述长短差异很大，贴着闸走会在描述偏长的
	// 客户那里直接撞 requestPayloadTooLarge。
	if used := jobClassChunkSize * observedBytesPerJob; used*4 > requestByteGate {
		t.Fatalf("一块 %d 个职位约 %d 字节，超过那道自限的四分之一，余量不够",
			jobClassChunkSize, used)
	}
	// 客户机那 10 个职位要能一次决策完——分成两块的话，后一块只能避开前一块
	// 占用的类别、不能反过来影响它，全局差异化就打了折。
	if jobClassChunkSize < 10 {
		t.Fatalf("一块装不下 10 个职位(%d)，客户机现有批量会被拆开决策", jobClassChunkSize)
	}
}

// 输出预算不再按职位数估算：max_tokens 是上限不是预付额度，模型不吐就不计费，
// 而估低一次就是整批干净失败。这条用例钉住"用的是共享常量、没有本地的估算层"。
func TestJobClassOutputBudgetUsesTheSharedLimit(t *testing.T) {
	// 全批分配与关键词选词共用同一档输出上限，两者都不再自行加码或折扣。
	if m5ai.JobClassOutputTokenLimit != m5ai.JobKeywordsOutputTokenLimit {
		t.Fatalf("类别与关键词的输出上限应当同档: %d vs %d",
			m5ai.JobClassOutputTokenLimit, m5ai.JobKeywordsOutputTokenLimit)
	}
	// 一整块的实际输出量级：每条分配约 45~55 token（结构约 25，中文理由 20~30
	// 字又是 20~30），外层另算。上限要能装下一整块，且留足余量。
	const worstCaseTokensPerJob = 72
	const wrapperTokens = 32
	if need := wrapperTokens + worstCaseTokensPerJob*jobClassChunkSize; need > m5ai.JobClassOutputTokenLimit {
		t.Fatalf("一块 %d 个职位最坏需要 %d token，超过上限 %d——会被 finish_reason=length 切断",
			jobClassChunkSize, need, m5ai.JobClassOutputTokenLimit)
	}
}
