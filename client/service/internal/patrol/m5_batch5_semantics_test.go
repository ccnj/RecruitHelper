package patrol

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

func TestM5IntentPostResponseInputBudgetBlockedKeepsUsageAndStopsBeforeReplyAndAction(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		if call != 1 || request.Purpose != m5ai.PurposeIntent {
			return m5ai.CompletionResponse{}, fmt.Errorf("输入预算阻断后发生额外调用: call=%d purpose=%s", call, request.Purpose)
		}
		zero := 0
		return m5ai.CompletionResponse{
			JSONText: `{"信号":"有意向"}`,
			Usage: m5ai.CompletionUsage{
				InputTokens:       m5ai.IntentInputTokenLimit + 1,
				CachedInputTokens: 101,
				OutputTokens:      4,
				ReasoningTokens:   &zero,
			},
			ReasoningContentEmpty: true,
		}, &m5ai.ProviderError{Class: "inputTokenBudgetExceeded"}
	}}
	h.manager.advice = advice
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 1 {
		t.Fatalf("intent 输入预算阻断必须只调用一次: calls=%d err=%v", len(advice.requests), err)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != 1 || invocations[0].Status != store.AIInvocationBudgetBlocked ||
		invocations[0].ErrorClass != "inputTokenBudgetExceeded" ||
		invocations[0].OutputHash == "" ||
		invocations[0].InputTokens != m5ai.IntentInputTokenLimit+1 ||
		invocations[0].CachedInputTokens != 101 || invocations[0].OutputTokens != 4 ||
		invocations[0].EstimatedCostMicros != m5ai.EstimatedCostMicros(m5ai.CompletionUsage{
			InputTokens: m5ai.IntentInputTokenLimit + 1, CachedInputTokens: 101, OutputTokens: 4,
		}) || invocations[0].UsageShape != store.AIInvocationUsageComplete ||
		invocations[0].ReasoningTokens == nil || *invocations[0].ReasoningTokens != 0 {
		t.Fatalf("输入预算 invocation 事实错误: invocations=%+v err=%v", invocations, err)
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnManualRequired ||
		turn.FailureReason != "inputBudgetBlocked" || turn.IntentLabel != "" || turn.IntentSource != "" {
		t.Fatalf("输入预算阻断未直接转人工: turn=%+v err=%v", turn, err)
	}
	action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || action != nil {
		t.Fatalf("输入预算阻断不得创建 action: action=%+v err=%v", action, err)
	}
}

func TestM5ReplyPostResponseInputBudgetBlockedKeepsUsageAndCreatesNoAction(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		switch call {
		case 1:
			return safeFakeResponse(`{"信号":"有意向"}`), nil
		case 2:
			if request.Purpose != m5ai.PurposeReply {
				return m5ai.CompletionResponse{}, fmt.Errorf("第二次调用用途错误: %s", request.Purpose)
			}
			zero := 0
			return m5ai.CompletionResponse{
				JSONText: `{"话术_序列":["不得形成 action"]}`,
				Usage: m5ai.CompletionUsage{
					InputTokens:       m5ai.ReplyInputTokenLimit + 1,
					CachedInputTokens: 201,
					OutputTokens:      7,
					ReasoningTokens:   &zero,
				},
				ReasoningContentEmpty: true,
			}, &m5ai.ProviderError{Class: "inputTokenBudgetExceeded"}
		default:
			return m5ai.CompletionResponse{}, fmt.Errorf("超预算后发生额外调用: %d", call)
		}
	}}
	h.manager.advice = advice
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 {
		t.Fatalf("reply 输入预算阻断编排错误: calls=%d err=%v", len(advice.requests), err)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != 2 {
		t.Fatalf("reply 输入预算 invocation 缺失: invocations=%+v err=%v", invocations, err)
	}
	replyInvocation := invocations[1]
	if replyInvocation.Purpose != m5ai.PurposeReply ||
		replyInvocation.Status != store.AIInvocationBudgetBlocked ||
		replyInvocation.ErrorClass != "inputTokenBudgetExceeded" ||
		replyInvocation.OutputHash == "" ||
		replyInvocation.InputTokens != m5ai.ReplyInputTokenLimit+1 ||
		replyInvocation.CachedInputTokens != 201 || replyInvocation.OutputTokens != 7 ||
		replyInvocation.EstimatedCostMicros != m5ai.EstimatedCostMicros(m5ai.CompletionUsage{
			InputTokens: m5ai.ReplyInputTokenLimit + 1, CachedInputTokens: 201, OutputTokens: 7,
		}) {
		t.Fatalf("reply 输入预算计量事实错误: %+v", replyInvocation)
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnManualRequired {
		t.Fatalf("reply 输入预算阻断未转人工: turn=%+v err=%v", turn, err)
	}
	action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || action != nil {
		t.Fatalf("reply 输入预算阻断不得创建 action: action=%+v err=%v", action, err)
	}
}

func TestM5ImportedRevisionWithoutRebindLeavesFrozenTurnExecutable(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	oldRevision, err := h.db.JobAIContextRevisionByHash(fixture.turn.ContextRevisionHash)
	if err != nil || oldRevision == nil {
		t.Fatalf("读取旧冻结 revision: revision=%+v err=%v", oldRevision, err)
	}
	documents := append([]m5ai.JobConfigDocument(nil), oldRevision.SourcePackage.Documents...)
	newReplyPrompt := oldRevision.Communication.ReplyPrompt + "\n新版仅供后续轮次"
	for index := range documents {
		if documents[index].DocType == "多轮沟通" {
			documents[index].Content = newReplyPrompt
		}
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	newRevision := m5ai.ContextRevision{
		ContextID:    oldRevision.ContextID,
		RevisionHash: oldRevision.RevisionHash + "-new",
		SourceKind:   oldRevision.SourceKind,
		SourceJobRef: oldRevision.SourceJobRef,
		DisplayName:  oldRevision.DisplayName,
		Environment:  oldRevision.Environment,
		SourcePackage: m5ai.JobConfigDocumentPackage{
			Documents: documents,
		},
		Communication: oldRevision.Communication,
		CreatedAt:     h.clock.Now().Add(time.Minute),
	}
	newRevision.Communication.ReplyPrompt = newReplyPrompt
	if _, created, err := h.db.SaveJobAIContextRevision(newRevision); err != nil || !created {
		t.Fatalf("只导入新版 revision 失败: created=%v err=%v", created, err)
	}
	active, err := h.db.ActiveProfileAIContext(fixture.profileID)
	if err != nil || active == nil || active.Binding.RevisionHash != oldRevision.RevisionHash {
		t.Fatalf("只导入新版不得隐式改绑: active=%+v err=%v", active, err)
	}

	advice := &recordingAdviceExecutor{}
	h.manager.advice = advice
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	h.manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 {
		t.Fatalf("旧冻结 turn 未继续完成建议: calls=%d err=%v", len(advice.requests), err)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != 2 {
		t.Fatalf("旧 turn invocation 事实不完整: invocations=%+v err=%v", invocations, err)
	}
	for _, invocation := range invocations {
		if invocation.ContextRevisionHash != oldRevision.RevisionHash {
			t.Fatalf("旧 turn 被新版 revision 污染: invocation=%+v", invocation)
		}
	}
	action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || action == nil || action.Status != store.CommunicationActionPlanned {
		t.Fatalf("旧冻结 turn 未形成建议 action: action=%+v err=%v", action, err)
	}
}
