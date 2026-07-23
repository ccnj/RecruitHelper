package patrol

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

func m5LedgerMessage(seq int64, direction, kind, hash, text string) store.Message {
	message := store.Message{Seq: seq, Direction: direction, Kind: kind, ContentHash: hash}
	if text != "" {
		message.Text = &text
	}
	return message
}

func TestInspectM5PendingUsesLastOutboundAndRejectsUnsupportedBeforeAI(t *testing.T) {
	missingOutbound := inspectM5Pending([]store.Message{
		m5LedgerMessage(1, "in", "text", "orphan-inbound", "你好"),
	})
	if missingOutbound.manualReason != "sentGreetingMissing" || missingOutbound.lastOutbound != nil ||
		missingOutbound.firstReal == nil || missingOutbound.firstReal.Seq != 1 {
		t.Fatalf("缺少活动出站锚必须保留真实入站事实并转人工: %+v", missingOutbound)
	}

	messages := []store.Message{
		m5LedgerMessage(1, "out", "text", "greeting", "你好"),
		m5LedgerMessage(2, "in", "text", "old", "第一轮"),
		m5LedgerMessage(3, "out", "text", "human", "真人已回复"),
		m5LedgerMessage(4, "in", "text", "new-a", "新问题一"),
		m5LedgerMessage(5, "in", "text", "new-b", "新问题二"),
	}
	pending := inspectM5Pending(messages)
	if pending.manualReason != "" || pending.lastOutbound == nil || pending.lastOutbound.Seq != 3 ||
		pending.firstReal == nil || pending.firstReal.Seq != 2 || len(pending.inbound) != 2 {
		t.Fatalf("普通轮边界错误: %+v", pending)
	}
	handled := inspectM5Pending(messages[:3])
	if handled.lastOutbound == nil || handled.lastOutbound.Seq != 3 || handled.firstReal == nil ||
		handled.firstReal.Seq != 2 || len(handled.inbound) != 0 || handled.manualReason != "" {
		t.Fatalf("真人已回复时仍须保留首次候选人事实，但不得与真人抢话: %+v", handled)
	}

	media := inspectM5Pending([]store.Message{
		m5LedgerMessage(1, "out", "text", "greeting", "你好"),
		m5LedgerMessage(2, "in", "image", "image", ""),
	})
	if media.manualReason != "unsupportedMedia" || media.firstReal == nil || media.firstReal.Seq != 2 || len(media.inbound) != 0 {
		t.Fatalf("媒体没有在 AI 前转人工或未保留首次真实事实: %+v", media)
	}
	card := inspectM5Pending([]store.Message{
		m5LedgerMessage(1, "out", "text", "greeting", "你好"),
		m5LedgerMessage(2, "in", "card", "card", ""),
	})
	if card.manualReason != "unsupportedSemantic" || card.firstReal != nil {
		t.Fatalf("纯卡片不应推进 communicating 且必须转人工: %+v", card)
	}
}

func TestM5TurnIdentityIsStableAndChangesWithInputFacts(t *testing.T) {
	base := inspectM5Pending([]store.Message{
		m5LedgerMessage(1, "out", "text", "greeting-hash", "你好"),
		m5LedgerMessage(2, "in", "text", "answer-hash", "可以聊聊"),
	})
	digest, turnID, err := m5TurnIdentity("profile-fixture", base)
	if err != nil || digest == "" || turnID != "turn-"+digest {
		t.Fatalf("turn identity 无效: digest=%q turn=%q err=%v", digest, turnID, err)
	}
	repeatedDigest, repeatedID, _ := m5TurnIdentity("profile-fixture", base)
	if repeatedDigest != digest || repeatedID != turnID {
		t.Fatal("相同冻结输入没有复用 turn identity")
	}
	changed := base
	changed.inbound = append([]store.Message(nil), base.inbound...)
	changed.inbound[0].ContentHash = "changed"
	changedDigest, _, _ := m5TurnIdentity("profile-fixture", changed)
	if changedDigest == digest {
		t.Fatal("正文 hash 变化没有改变 input digest")
	}
}

func TestM5ProviderFailureUsesFixedClasses(t *testing.T) {
	status, class := m5ProviderFailure(&m5ai.ProviderError{Class: "rateLimited"})
	if status != store.AIInvocationProviderRejected || class != "rateLimited" {
		t.Fatalf("rate limit 分类错误: %s/%s", status, class)
	}
	secret := errors.New("候选人手机号13800138000")
	status, class = m5ProviderFailure(secret)
	if status != store.AIInvocationTransportFailed || class != "transport" || class == secret.Error() {
		t.Fatalf("未知 provider 错误泄漏正文: %s/%s", status, class)
	}

	status, class = m5ProviderFailure(&m5ai.ProviderError{Class: "inputTokenBudgetExceeded"})
	if status != store.AIInvocationBudgetBlocked || class != "inputTokenBudgetExceeded" {
		t.Fatalf("响应后输入 token 超限分类错误: %s/%s", status, class)
	}

	payloadErr := &m5ai.ProviderError{Class: "requestPayloadTooLarge"}
	completion := m5CompletionFromProvider(
		"invocation-payload-cap", m5ai.CompletionResponse{}, payloadErr,
		5*time.Millisecond, time.Now(),
	)
	if completion.Status != store.AIInvocationTransportFailed ||
		completion.ErrorClass != "requestPayloadTooLarge" ||
		completion.InputTokens != 0 || completion.CachedInputTokens != 0 ||
		completion.OutputTokens != 0 || completion.EstimatedCostMicros != 0 ||
		completion.OutputHash != "" || completion.UsageShape != "" ||
		completion.LatencyMs != 5 {
		t.Fatalf("请求运输上限终局错误: %+v", completion)
	}
}
