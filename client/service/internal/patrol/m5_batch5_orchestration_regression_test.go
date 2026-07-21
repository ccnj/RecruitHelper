package patrol

import (
	"context"
	"fmt"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

func TestM5ReplyProviderFailureStopsAfterTwoInvocationsWithoutEffect(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		switch call {
		case 1:
			if request.Purpose != m5ai.PurposeIntent {
				return m5ai.CompletionResponse{}, fmt.Errorf("首次调用用途错误: %s", request.Purpose)
			}
			return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
		case 2:
			if request.Purpose != m5ai.PurposeReply {
				return m5ai.CompletionResponse{}, fmt.Errorf("第二次调用用途错误: %s", request.Purpose)
			}
			return m5ai.CompletionResponse{}, &m5ai.ProviderError{Class: "rateLimited"}
		default:
			return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次调用", call)
		}
	}}
	h.manager.advice = advice
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}

	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil {
		t.Fatalf("读取失败轮: turn=%+v err=%v", turn, err)
	}
	// 重复推进同一终局轮，证明失败不会触发第三次 provider 调用。
	h.manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), *turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 {
		t.Fatalf("reply 失败后不得再调用 provider: calls=%d err=%v", len(advice.requests), err)
	}

	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != 2 || invocations[0].Purpose != m5ai.PurposeIntent ||
		invocations[0].Status != store.AIInvocationOK || invocations[1].Purpose != m5ai.PurposeReply ||
		invocations[1].Status != store.AIInvocationProviderRejected || invocations[1].ErrorClass != "rateLimited" {
		t.Fatalf("reply 失败 invocation 事实错误: invocations=%+v err=%v", invocations, err)
	}
	assertM5OrchestrationStoppedWithoutEffect(t, h, fixture, "replyFailed", false)
}

func TestM5ReplyReasoningTokensNonzeroRequiresManualWithoutEffect(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		switch call {
		case 1:
			if request.Purpose != m5ai.PurposeIntent {
				return m5ai.CompletionResponse{}, fmt.Errorf("首次调用用途错误: %s", request.Purpose)
			}
			return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
		case 2:
			if request.Purpose != m5ai.PurposeReply {
				return m5ai.CompletionResponse{}, fmt.Errorf("第二次调用用途错误: %s", request.Purpose)
			}
			one := 1
			return m5ai.CompletionResponse{
				JSONText: `{"话术_序列":["不得派发的合成回复"],"动作":"忽略"}`,
				Usage: m5ai.CompletionUsage{
					InputTokens: 12, CachedInputTokens: 2, OutputTokens: 4, ReasoningTokens: &one,
				},
				ReasoningContentEmpty: true,
			}, nil
		default:
			return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次调用", call)
		}
	}}
	h.manager.advice = advice
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}

	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 {
		t.Fatalf("reasoning 非零轮调用次数错误: calls=%d err=%v", len(advice.requests), err)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != 2 || invocations[1].Purpose != m5ai.PurposeReply ||
		invocations[1].Status != store.AIInvocationOK ||
		invocations[1].UsageShape != store.AIInvocationUsageComplete ||
		invocations[1].ReasoningTokens == nil || *invocations[1].ReasoningTokens != 1 {
		t.Fatalf("reasoning 非零 invocation 事实错误: invocations=%+v err=%v", invocations, err)
	}
	assertM5OrchestrationStoppedWithoutEffect(t, h, fixture, "reasoningUsageUnsafe", false)
}

func TestM5NonemptyReasoningContentRequiresManualWithoutEffect(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	zero := 0
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		if call != 1 || request.Purpose != m5ai.PurposeIntent {
			return m5ai.CompletionResponse{}, fmt.Errorf("reasoning_content 非空后发生额外调用: call=%d purpose=%s", call, request.Purpose)
		}
		return m5ai.CompletionResponse{
			JSONText: `{"信号":"有意向","理由":"fixture"}`,
			Usage: m5ai.CompletionUsage{
				InputTokens: 12, CachedInputTokens: 2, OutputTokens: 4, ReasoningTokens: &zero,
			},
			ReasoningContentEmpty: false,
		}, nil
	}}
	h.manager.advice = advice
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}

	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 1 {
		t.Fatalf("reasoning_content 非空必须在 intent 后阻断: calls=%d err=%v", len(advice.requests), err)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != 1 || invocations[0].Status != store.AIInvocationOK ||
		invocations[0].UsageShape != store.AIInvocationUsageComplete || invocations[0].ReasoningTokens == nil ||
		*invocations[0].ReasoningTokens != 0 || invocations[0].OutputTokens != 4 || invocations[0].EstimatedCostMicros <= 0 {
		t.Fatalf("reasoning_content 非空仍须如实记录 usage: invocations=%+v err=%v", invocations, err)
	}
	assertM5OrchestrationStoppedWithoutEffect(t, h, fixture, "reasoningUsageUnsafe", false)
}

func TestM5PlannedActionRecheckStopsChangedWorldBeforeDispatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *harness, m5AdviceFixture)
	}{
		{
			name: "candidate_new_message",
			mutate: func(t *testing.T, h *harness, fixture m5AdviceFixture) {
				t.Helper()
				appendM5BoundaryMessage(t, h, fixture, "in", "候选人稍后补充的合成消息")
			},
		},
		{
			name: "human_outbound",
			mutate: func(t *testing.T, h *harness, fixture m5AdviceFixture) {
				t.Helper()
				appendM5BoundaryMessage(t, h, fixture, "out", "真人先行回复的合成消息")
			},
		},
		{
			name: "explicit_context_rebind",
			mutate: func(t *testing.T, h *harness, fixture m5AdviceFixture) {
				t.Helper()
				rebindM5FixtureContext(t, h, fixture)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			fixture := seedM5AdviceFixture(t, h)
			advice := &recordingAdviceExecutor{}
			h.manager.advice = advice
			planningActor := &roundActor{manager: h.manager, now: h.clock.Now()}
			h.manager.mu.Lock()
			err := planningActor.advanceM5Turn(context.Background(), fixture.turn)
			h.manager.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
			if err != nil || action == nil || action.Status != store.CommunicationActionPlanned {
				t.Fatalf("未形成待复核 planned action: action=%+v err=%v", action, err)
			}

			test.mutate(t, h, fixture)
			beforeCommands := countM5SendMessageCommands(t, h)
			hand := &m5PositiveHand{}
			dispatcher := dispatch.New(h.db, hand)
			hand.setDispatcher(dispatcher)
			runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
			manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
			if err != nil {
				t.Fatal(err)
			}
			account, err := h.db.AccountByKey(h.key)
			if err != nil || account == nil {
				t.Fatalf("读取试运行账号: account=%+v err=%v", account, err)
			}
			actor := &roundActor{
				manager: manager, account: account,
				hand: HandState{Online: true, Session: "session-1", BootID: "boot-1"},
				now:  h.clock.Now(),
			}
			manager.mu.Lock()
			err = actor.processM5Trial(context.Background())
			manager.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if hand.commandCount() != 0 || countM5SendMessageCommands(t, h) != beforeCommands {
				t.Fatalf("世界状态变化后仍进入 chat.sendMessage: hand=%d before=%d after=%d",
					hand.commandCount(), beforeCommands, countM5SendMessageCommands(t, h))
			}
			assertM5OrchestrationStoppedWithoutEffect(t, h, fixture, "inputBoundaryChanged", true)
		})
	}
}

func assertM5OrchestrationStoppedWithoutEffect(
	t *testing.T,
	h *harness,
	fixture m5AdviceFixture,
	reason string,
	expectAction bool,
) {
	t.Helper()
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnManualRequired || turn.FailureReason != reason {
		t.Fatalf("turn 未收敛人工: turn=%+v err=%v", turn, err)
	}
	action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if !expectAction && action != nil {
		t.Fatalf("人工收敛前不应创建 action: action=%+v", action)
	}
	if expectAction && (action == nil || action.Status != store.CommunicationActionManualRequired ||
		action.FailureReason != reason || action.EffectIntentID != nil) {
		t.Fatalf("action 未收敛人工或错误绑定 effect: action=%+v", action)
	}
	trial, err := h.db.M5TrialStatus()
	if err != nil || trial == nil || trial.Selection.Status != store.M5TrialSelectionManualRequired ||
		trial.Selection.ActiveSlot != nil || trial.Selection.Reason != reason {
		t.Fatalf("试运行未收敛人工: trial=%+v err=%v", trial, err)
	}
	intent, err := h.db.LatestEffectIntent(h.key.Platform, h.key.AccountRef, fixture.conversationRef)
	if err != nil || intent != nil {
		t.Fatalf("不得产生 chat.sendMessage effect intent: intent=%+v err=%v", intent, err)
	}
	if count := countM5SendMessageCommands(t, h); count != 0 {
		t.Fatalf("不得产生 chat.sendMessage Cmd: %d", count)
	}
}

func countM5SendMessageCommands(t *testing.T, h *harness) int {
	t.Helper()
	commands, err := h.db.RecentCmds(100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for i := range commands {
		if commands[i].Name == protocol.PrimChatSendMessage {
			count++
		}
	}
	return count
}

func appendM5BoundaryMessage(
	t *testing.T,
	h *harness,
	fixture m5AdviceFixture,
	direction string,
	text string,
) {
	t.Helper()
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: fixture.turn.InboundThroughSeq,
		NewMessages: []store.MessageDraft{{
			Direction: direction, Kind: "text", Text: &text,
			ContentHash: syncledger.HashText(text), Origin: "external",
		}},
		SyncedAt: h.clock.Now().Add(time.Minute),
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加边界变化消息: changes=%+v err=%v", changes, err)
	}
}

func rebindM5FixtureContext(t *testing.T, h *harness, fixture m5AdviceFixture) {
	t.Helper()
	current, err := h.db.JobAIContextRevisionByHash(fixture.turn.ContextRevisionHash)
	if err != nil || current == nil {
		t.Fatalf("读取当前 revision: revision=%+v err=%v", current, err)
	}
	updated := m5ai.ContextRevision{
		ContextID: current.ContextID, RevisionHash: current.RevisionHash + "-rebound",
		SourceKind: current.SourceKind, SourceJobRef: current.SourceJobRef,
		DisplayName: current.DisplayName, Environment: current.Environment,
		SourcePackage: current.SourcePackage, Communication: current.Communication,
		CreatedAt: h.clock.Now().Add(time.Minute),
	}
	updated.SourcePackage.Documents = append([]m5ai.JobConfigDocument(nil), current.SourcePackage.Documents...)
	updated.Communication.ReplyPrompt += "\n仅供后续轮次使用"
	for index := range updated.SourcePackage.Documents {
		if updated.SourcePackage.Documents[index].DocType == "多轮沟通" {
			updated.SourcePackage.Documents[index].Content = updated.Communication.ReplyPrompt
		}
	}
	if _, created, err := h.db.SaveJobAIContextRevision(updated); err != nil || !created {
		t.Fatalf("保存改绑 revision: created=%v err=%v", created, err)
	}
	if _, err := h.db.BindActiveM5TrialProfileAIContext(store.BindProfileAIContextRequest{
		BindingID: "binding-m5-boundary-rebound", ProfileID: fixture.profileID,
		ContextID: updated.ContextID, RevisionHash: updated.RevisionHash,
		Reason: "userRebound", BoundBy: "user", BoundAt: h.clock.Now().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("显式改绑 context: %v", err)
	}
}
