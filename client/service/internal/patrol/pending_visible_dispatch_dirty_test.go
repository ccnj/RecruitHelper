package patrol

import (
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/textcanon"
	"recruithelper/contract/gen/go/protocol"
)

const pendingDispatchDirtyInbound = "这个岗位还在招吗"

// seedPlannedChainHeadReply 把 fixture 推到"链首回复已 planned、尚未绑定发送
// 意图"的现场:冻结对话轮 → intent 建议落账 → reply 建议落账产出 planned 动作。
// 刻意停在 CreateEffectIntentAndCmd 之前——一旦绑定发送意图,这一行就不再是
// 待派发行,也就不该再构成读取理由。
func seedPlannedChainHeadReply(
	t *testing.T,
	h *harness,
	fixture communicationV4PatrolFixture,
) *store.CommunicationAction {
	t.Helper()
	now := h.clock.Now()
	messages, err := h.db.MessagesForConversation(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	var greeting, inbound *store.Message
	for index := range messages {
		switch messages[index].Seq {
		case 1:
			greeting = &messages[index]
		case fixture.inboundSeq:
			inbound = &messages[index]
		}
	}
	if greeting == nil || inbound == nil {
		t.Fatalf("账本缺少锚或入站行: messages=%+v", messages)
	}
	digest, turnID, err := store.DialogueTurnIdentity(
		fixture.profileID, *greeting, []store.Message{*inbound},
	)
	if err != nil {
		t.Fatal(err)
	}
	material, materialReady, err := h.db.CommunicationAIMaterialForProfile(fixture.profileID)
	if err != nil || !materialReady {
		t.Fatalf("AI 材料未就绪: ready=%v err=%v", materialReady, err)
	}
	aggregateRow, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := h.db.FreezeCommunicationV4Turn(store.FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: fixture.profileID,
		ConversationRef: fixture.conversationRef,
		InputDigest:     digest, HistoryThroughSeq: inbound.Seq - 1,
		InboundFromSeq: inbound.Seq, InboundThroughSeq: inbound.Seq,
		ExpectedProjectedThroughSeq: aggregateRow.ProjectedThroughSeq,
		OutboundAnchorSeq:           greeting.Seq,
		ContextRevisionHash:         material.ContextRevision.RevisionHash,
		ResumeSnapshotID:            material.ResumeSnapshot.SnapshotID,
		RecommendedTimeText:         "合成推荐时段",
		RenderFormatVersion:         m5ai.DialogueRenderFormatVersion,
		FrozenAt:                    now,
	})
	if err != nil || !frozen.Created {
		t.Fatalf("对话轮冻结失败: result=%+v err=%v", frozen, err)
	}
	zero := 0
	completion := func(invocationID string, at time.Time) store.AIInvocationCompletion {
		return store.AIInvocationCompletion{
			InvocationID: invocationID, Status: store.AIInvocationOK,
			OutputHash:  "output-" + invocationID,
			InputTokens: 10, CachedInputTokens: 2, OutputTokens: 3,
			ReasoningTokens: &zero, UsageShape: store.AIInvocationUsageComplete,
			ReasoningContentEmpty: true, LatencyMs: 25, EstimatedCostMicros: 7,
			FinishedAt: at,
		}
	}
	if reserved, err := h.db.ReserveAIInvocation(store.ReserveAIInvocationRequest{
		InvocationID: "invocation-pending-intent", TurnID: turnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-pending-intent", CreatedAt: now,
	}); err != nil || !reserved.Created {
		t.Fatalf("intent 预留失败: result=%+v err=%v", reserved, err)
	}
	if _, err := h.db.CompleteIntentInvocation(store.CompleteIntentInvocationRequest{
		Completion: completion("invocation-pending-intent", now.Add(time.Second)),
		Label:      m5ai.IntentInterested,
		Source:     store.DialogueIntentLLM,
	}); err != nil {
		t.Fatal(err)
	}
	if reserved, err := h.db.ReserveAIInvocation(store.ReserveAIInvocationRequest{
		InvocationID: "invocation-pending-reply", TurnID: turnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-pending-reply", CreatedAt: now.Add(2 * time.Second),
	}); err != nil || !reserved.Created {
		t.Fatalf("reply 预留失败: result=%+v err=%v", reserved, err)
	}
	replyText := "合成链首回复"
	action, err := h.db.CompleteReplyInvocation(store.CompleteReplyInvocationRequest{
		Completion: completion("invocation-pending-reply", now.Add(3*time.Second)),
		ActionID:   "ignored",
		Text:       replyText, ContentHash: textcanon.Hash(replyText),
		PlannedAt: now.Add(3 * time.Second),
	})
	if err != nil || action == nil || action.Status != store.CommunicationActionPlanned {
		t.Fatalf("planned 链首动作构造失败: action=%+v err=%v", action, err)
	}
	if action.DependsOnActionID != nil {
		t.Fatalf("本用例需要链首动作,实际带依赖: %v", *action.DependsOnActionID)
	}
	return action
}

// pendingDispatchQuietSummary 造一条"安静会话"的列表摘要:未读为 0、方向与
// 类型与账本最后一条(入站文本)一致,因此列表指纹判据完全不成立。
func pendingDispatchQuietSummary(
	fixture communicationV4PatrolFixture,
	suffix string,
) protocol.ConversationSummary {
	current := summary(
		fixture.conversationRef,
		"person-v4-patrol-"+suffix,
		pendingDispatchDirtyInbound,
		0,
	)
	activity := int64(1785812524002)
	current.LastActivityTs = &activity
	return current
}

func detectQuietConversationDirty(
	t *testing.T,
	h *harness,
	current protocol.ConversationSummary,
) *dirtyConversation {
	t.Helper()
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
	}
	actor := &roundActor{
		manager: h.manager, account: account,
		hand:                    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID:                 "round-pending-dispatch",
		now:                     h.clock.Now(),
		checkedListFingerprints: map[string]string{},
	}
	h.manager.mu.Lock()
	defer h.manager.mu.Unlock()
	tracked, err := actor.trackedConversationsByRef()
	if err != nil {
		t.Fatalf("读取已收编会话失败: %v", err)
	}
	if _, listed := tracked[current.ConversationRef]; !listed {
		t.Fatalf("用例前提不成立:会话未收编 ref=%s", current.ConversationRef)
	}
	dirty, err := actor.detectDirtySummary(current, tracked)
	if err != nil {
		t.Fatalf("判脏失败: %v", err)
	}
	return dirty
}

// TestQuietConversationWithPlannedChainHeadIsDirty 是本次修复的正向锁:候选人
// 安静下来之后才要发的链首回复,必须让会话重新被读一次(reconcileConversation
// 内的 readThread 顺带把页面导航到该会话),否则发送必然撞 pageAbsent。
//
// 对照组 TestQuietConversationWithoutPlannedActionStaysClean 证明这里判脏确实
// 来自新判据,而不是 fixture 本身就脏。
func TestQuietConversationWithPlannedChainHeadIsDirty(t *testing.T) {
	h := newHarness(t)
	suffix := "pending-dirty"
	fixture := seedCommunicationV4PatrolTarget(t, h, suffix, pendingDispatchDirtyInbound)
	seedPlannedChainHeadReply(t, h, fixture)

	current := pendingDispatchQuietSummary(fixture, suffix)
	seedVerifiedListHint(t, h, current)

	if dirty := detectQuietConversationDirty(t, h, current); dirty == nil {
		t.Fatal("挂着链首 planned 回复的安静会话必须判脏,否则派发时页面不在该会话")
	}
}

func TestQuietConversationWithoutPlannedActionStaysClean(t *testing.T) {
	h := newHarness(t)
	suffix := "pending-clean"
	fixture := seedCommunicationV4PatrolTarget(t, h, suffix, pendingDispatchDirtyInbound)

	current := pendingDispatchQuietSummary(fixture, suffix)
	seedVerifiedListHint(t, h, current)

	if dirty := detectQuietConversationDirty(t, h, current); dirty != nil {
		t.Fatal("没有待派发动作的安静会话不得判脏,否则每轮白读一次页面")
	}
}
