package patrol

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

func TestM5PauseDuringAdviceStopsNextStageAndResumesSameTurn(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	gateClosed := errors.New("fixture workflow paused")
	gateOpen := true
	gateCalls := 0
	advice := &recordingAdviceExecutor{complete: func(
		call int,
		request m5ai.CompletionRequest,
	) (m5ai.CompletionResponse, error) {
		switch call {
		case 1:
			if request.Purpose != m5ai.PurposeIntent {
				return m5ai.CompletionResponse{}, fmt.Errorf("首次调用用途错误: %s", request.Purpose)
			}
			// Simulate the durable workflow pause linearizing while the
			// provider call is in flight. The invocation itself may finish,
			// but its caller must pass the same member gate before reply AI.
			gateOpen = false
			return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
		case 2:
			if request.Purpose != m5ai.PurposeReply {
				return m5ai.CompletionResponse{}, fmt.Errorf("恢复后调用用途错误: %s", request.Purpose)
			}
			return safeFakeResponse(`{"话术_序列":["恢复后的合成回复"],"动作":"无"}`), nil
		default:
			return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次调用", call)
		}
	}}
	h.manager.advice = advice
	h.manager.SetWorkflowMemberGate(func() error {
		gateCalls++
		if !gateOpen {
			return gateClosed
		}
		return nil
	})
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号: account=%+v err=%v", account, err)
	}
	actor := &roundActor{
		manager: h.manager,
		account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		now:     h.clock.Now(),
	}

	h.manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if !errors.Is(err, gateClosed) || len(advice.requests) != 1 || gateCalls != 2 {
		t.Fatalf(
			"暂停后仍越过 AI 阶段边界: calls=%d gateCalls=%d err=%v",
			len(advice.requests),
			gateCalls,
			err,
		)
	}
	pausedTurn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || pausedTurn == nil ||
		pausedTurn.Status != store.DialogueTurnClassified {
		t.Fatalf("在途 intent 结果没有停在可恢复分类态: turn=%+v err=%v", pausedTurn, err)
	}
	if action, actionErr := h.db.CommunicationActionByTurn(
		fixture.turn.TurnID,
	); actionErr != nil || action != nil {
		t.Fatalf("暂停后不应提前物化回复动作: action=%+v err=%v", action, actionErr)
	}

	gateOpen = true
	h.manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), *pausedTurn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 {
		t.Fatalf("恢复后没有从同一轮继续 reply AI: calls=%d err=%v", len(advice.requests), err)
	}
	action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || action == nil ||
		action.Status != store.CommunicationActionPlanned ||
		action.EffectIntentID != nil {
		t.Fatalf("恢复后的轮没有停在既有 planned seam: action=%+v err=%v", action, err)
	}
}

func TestM5PreWALPauseAndDailyBoundaryKeepPlannedActionRecoverable(t *testing.T) {
	tests := []struct {
		name      string
		interrupt func(*Manager, *harness) error
		wantErr   error
	}{
		{
			name: "user_pause",
			interrupt: func(manager *Manager, h *harness) error {
				return manager.PauseNow(h.key)
			},
			wantErr: ErrActorPaused,
		},
		{
			name: "daily_boundary",
			interrupt: func(_ *Manager, h *harness) error {
				h.clock.Add(15 * time.Hour)
				return nil
			},
			wantErr: ErrDailyWindowExpired,
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
			plannedTurn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
			if err != nil || plannedTurn == nil ||
				plannedTurn.Status != store.DialogueTurnAdviceReady {
				t.Fatalf("没有形成待派发轮: turn=%+v err=%v", plannedTurn, err)
			}

			hand := &m5PositiveHand{now: h.clock.Now}
			dispatcher := dispatch.New(h.db, hand)
			hand.setDispatcher(dispatcher)
			runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
			config := h.config
			var manager *Manager
			config.InteractionPaceWait = func(context.Context) error {
				return test.interrupt(manager, h)
			}
			manager, err = NewManager(h.db, runner, h.hands, config, advice)
			if err != nil {
				t.Fatal(err)
			}
			account, err := h.db.AccountByKey(h.key)
			if err != nil || account == nil {
				t.Fatalf("读取试运行账号: account=%+v err=%v", account, err)
			}
			actor := &roundActor{
				manager: manager,
				account: account,
				hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
				now:     h.clock.Now(),
			}
			beforeCommands := countM5SendMessageCommands(t, h)
			manager.mu.Lock()
			err = actor.advanceM5Turn(context.Background(), *plannedTurn)
			manager.mu.Unlock()
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("预 WAL 中断错误: got=%v want=%v", err, test.wantErr)
			}
			action, actionErr := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
			if actionErr != nil || action == nil ||
				action.Status != store.CommunicationActionPlanned ||
				action.EffectIntentID != nil || action.FailureReason != "" {
				t.Fatalf("普通中断不可把 planned 动作终局化: action=%+v err=%v", action, actionErr)
			}
			if hand.commandCount() != 0 ||
				countM5SendMessageCommands(t, h) != beforeCommands {
				t.Fatalf(
					"预 WAL 中断仍构造发送: hand=%d before=%d after=%d",
					hand.commandCount(),
					beforeCommands,
					countM5SendMessageCommands(t, h),
				)
			}
		})
	}
}

// 第一次吐了废话、第二次吐对了,turn 就该正常拿到建议:这是重试机制存在的
// 理由。中间那次失败只留下自己的 invocation 事实,不污染 turn 终局。
// 2026-08-02 裁决改断言:失败不再在同一巡检轮内连打,失败那轮就地跳过且不
// 冻结任何状态,下一巡检轮经 attempt 游走走到 attempt 2 再调用。
func TestM5ReplyRetriesAfterUnparsableOutputThenSucceeds(t *testing.T) {
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
			return safeFakeResponse(`{"话术_序列":`), nil
		case 3:
			if request.Purpose != m5ai.PurposeReply {
				return m5ai.CompletionResponse{}, fmt.Errorf("第三次调用用途错误: %s", request.Purpose)
			}
			return safeFakeResponse(`{"话术_序列":["重试之后吐对了的回复"],"动作":"无"}`), nil
		default:
			return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次调用", call)
		}
	}}
	h.manager.advice = advice
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}

	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	// 失败那轮到 reply attempt 1 为止:intent 一次 + reply 一次,随后跳过。
	if err != nil || len(advice.requests) != 2 {
		t.Fatalf("失败轮应只调用 intent+reply 各一次后跳过: calls=%d err=%v", len(advice.requests), err)
	}
	parked, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || parked == nil || parked.Status != store.DialogueTurnClassified ||
		parked.FailureReason != "" {
		t.Fatalf("失败轮不得写 turn 终局: turn=%+v err=%v", parked, err)
	}
	assertM5TrialStillActive(t, h)

	h.manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), *parked)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 3 {
		t.Fatalf("下一巡检轮应经游走走到 attempt 2 并成功: calls=%d err=%v", len(advice.requests), err)
	}

	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	action, actionErr := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnAdviceReady || turn.FailureReason != "" ||
		actionErr != nil || action == nil || action.Status != store.CommunicationActionPlanned ||
		action.Kind != store.CommunicationActionReplyText || action.EffectIntentID != nil {
		t.Fatalf("重试成功后未产出唯一 planned action: turn=%+v action=%+v err=%v actionErr=%v",
			turn, action, err, actionErr)
	}

	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != 3 {
		t.Fatalf("重试 invocation 条数错误: invocations=%+v err=%v", invocations, err)
	}
	// 失败那次与成功那次各自独立留痕,attempt 号连续可追。
	if invocations[1].Purpose != m5ai.PurposeReply || invocations[1].Attempt != 1 ||
		invocations[1].Status == store.AIInvocationOK {
		t.Fatalf("失败 attempt 未如实留痕: %+v", invocations[1])
	}
	if invocations[2].Purpose != m5ai.PurposeReply || invocations[2].Attempt != 2 ||
		invocations[2].Status != store.AIInvocationOK {
		t.Fatalf("成功 attempt 事实错误: %+v", invocations[2])
	}

	// 终局后重复推进不得再碰 provider。
	h.manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), *turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 3 {
		t.Fatalf("重试成功后重复推进不得再调用 provider: calls=%d err=%v", len(advice.requests), err)
	}
}

// provider 调用失败按 2026-08-01 甲方裁决重试到上限(终身每 turn 每用途至多
// 5 次调用),耗尽后 turn 转人工、零副作用。2026-08-02 裁决改断言:重试改为
// 跨巡检轮进行——每轮至多一次真实调用;耗尽后 turn 停靠 manualRequired,但
// 试运行不再连带冻结。
func TestM5ReplyProviderFailureRetriesToLimitThenStopsWithoutEffect(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	maxCalls := 1 + store.MaxAIInvocationAttempts
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		if call == 1 {
			if request.Purpose != m5ai.PurposeIntent {
				return m5ai.CompletionResponse{}, fmt.Errorf("首次调用用途错误: %s", request.Purpose)
			}
			return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
		}
		if call > maxCalls {
			return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次调用", call)
		}
		if request.Purpose != m5ai.PurposeReply {
			return m5ai.CompletionResponse{}, fmt.Errorf("第 %d 次调用用途错误: %s", call, request.Purpose)
		}
		return m5ai.CompletionResponse{}, &m5ai.ProviderError{Class: "rateLimited"}
	}}
	h.manager.advice = advice
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}

	// 首轮:intent 一次 + reply attempt 1 一次,失败后本轮跳过、不写终局。
	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 {
		t.Fatalf("首轮应恰好 intent+reply 各一次: calls=%d err=%v", len(advice.requests), err)
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnClassified ||
		turn.FailureReason != "" {
		t.Fatalf("首轮失败不得写 turn 终局: turn=%+v err=%v", turn, err)
	}
	assertM5TrialStillActive(t, h)

	// 后续每一巡检轮恰好一次 reply 调用,直到第 5 次耗尽落终局。
	for round := 2; round <= store.MaxAIInvocationAttempts; round++ {
		h.manager.mu.Lock()
		err = actor.advanceM5Turn(context.Background(), *turn)
		h.manager.mu.Unlock()
		if err != nil || len(advice.requests) != round+1 {
			t.Fatalf("第 %d 轮应只追加一次 reply 调用: calls=%d err=%v",
				round, len(advice.requests), err)
		}
		turn, err = h.db.DialogueTurnByID(fixture.turn.TurnID)
		if err != nil || turn == nil {
			t.Fatalf("读取重试轮: turn=%+v err=%v", turn, err)
		}
	}
	if len(advice.requests) != maxCalls {
		t.Fatalf("reply 失败调用总数应达上限: calls=%d want=%d", len(advice.requests), maxCalls)
	}
	// 重复推进同一终局轮，证明重试耗尽后不会再触发 provider 调用。
	h.manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), *turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != maxCalls {
		t.Fatalf("重试耗尽后不得再调用 provider: calls=%d err=%v", len(advice.requests), err)
	}

	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != maxCalls ||
		invocations[0].Purpose != m5ai.PurposeIntent || invocations[0].Status != store.AIInvocationOK {
		t.Fatalf("reply 失败 invocation 事实错误: invocations=%+v err=%v", invocations, err)
	}
	for i := 1; i < len(invocations); i++ {
		if invocations[i].Purpose != m5ai.PurposeReply || invocations[i].Attempt != i ||
			invocations[i].Status != store.AIInvocationProviderRejected ||
			invocations[i].ErrorClass != "rateLimited" {
			t.Fatalf("第 %d 次 reply attempt 事实错误: %+v", i, invocations[i])
		}
	}
	assertM5OrchestrationParkedWithoutFreeze(t, h, fixture, "replyFailed")
}

func TestM5ReplyReasoningTokensNonzeroRequiresManualWithoutEffect(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	maxCalls := 1 + store.MaxAIInvocationAttempts
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		if call == 1 {
			if request.Purpose != m5ai.PurposeIntent {
				return m5ai.CompletionResponse{}, fmt.Errorf("首次调用用途错误: %s", request.Purpose)
			}
			return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
		}
		if call > maxCalls {
			return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次调用", call)
		}
		if request.Purpose != m5ai.PurposeReply {
			return m5ai.CompletionResponse{}, fmt.Errorf("第 %d 次调用用途错误: %s", call, request.Purpose)
		}
		one := 1
		return m5ai.CompletionResponse{
			JSONText: `{"话术_序列":["不得派发的合成回复"],"动作":"无"}`,
			Usage: m5ai.CompletionUsage{
				InputTokens: 12, CachedInputTokens: 2, OutputTokens: 4, ReasoningTokens: &one,
			},
			ReasoningContentEmpty: true,
		}, nil
	}}
	h.manager.advice = advice
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}

	// reasoning 用量可疑属于"这次没吐好",总额仍是 5 次调用;2026-08-02 裁决
	// 后每巡检轮至多一次真实调用,每一次都不得派发。
	turn := fixture.turn
	for round := 1; round <= store.MaxAIInvocationAttempts; round++ {
		h.manager.mu.Lock()
		err := actor.advanceM5Turn(context.Background(), turn)
		h.manager.mu.Unlock()
		wantCalls := round + 1
		if round == 1 {
			wantCalls = 2
		}
		if err != nil || len(advice.requests) != wantCalls {
			t.Fatalf("reasoning 非零第 %d 轮调用次数错误: calls=%d want=%d err=%v",
				round, len(advice.requests), wantCalls, err)
		}
		reloaded, reloadErr := h.db.DialogueTurnByID(fixture.turn.TurnID)
		if reloadErr != nil || reloaded == nil {
			t.Fatalf("读取重试轮: turn=%+v err=%v", reloaded, reloadErr)
		}
		turn = *reloaded
	}
	if len(advice.requests) != maxCalls {
		t.Fatalf("reasoning 非零调用总数错误: calls=%d want=%d", len(advice.requests), maxCalls)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != maxCalls || invocations[1].Purpose != m5ai.PurposeReply ||
		invocations[1].Status != store.AIInvocationOK ||
		invocations[1].UsageShape != store.AIInvocationUsageComplete ||
		invocations[1].ReasoningTokens == nil || *invocations[1].ReasoningTokens != 1 {
		t.Fatalf("reasoning 非零 invocation 事实错误: invocations=%+v err=%v", invocations, err)
	}
	assertM5OrchestrationParkedWithoutFreeze(t, h, fixture, "reasoningUsageUnsafe")
}

func TestM5NonemptyReasoningContentRequiresManualWithoutEffect(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	zero := 0
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		if call > store.MaxAIInvocationAttempts || request.Purpose != m5ai.PurposeIntent {
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

	// intent 阶段就 reasoning 可疑:重试到上限仍可疑,绝不放行到 reply。
	// 2026-08-02 裁决后每巡检轮至多一次 intent 调用,五轮耗尽。
	turn := fixture.turn
	for round := 1; round <= store.MaxAIInvocationAttempts; round++ {
		h.manager.mu.Lock()
		err := actor.advanceM5Turn(context.Background(), turn)
		h.manager.mu.Unlock()
		if err != nil || len(advice.requests) != round {
			t.Fatalf("reasoning_content 非空第 %d 轮调用次数错误: calls=%d err=%v",
				round, len(advice.requests), err)
		}
		reloaded, reloadErr := h.db.DialogueTurnByID(fixture.turn.TurnID)
		if reloadErr != nil || reloaded == nil {
			t.Fatalf("读取重试轮: turn=%+v err=%v", reloaded, reloadErr)
		}
		turn = *reloaded
	}
	if len(advice.requests) != store.MaxAIInvocationAttempts {
		t.Fatalf("reasoning_content 非空必须在 intent 阶段耗尽重试后阻断: calls=%d want=%d",
			len(advice.requests), store.MaxAIInvocationAttempts)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != store.MaxAIInvocationAttempts ||
		invocations[0].Status != store.AIInvocationOK ||
		invocations[0].UsageShape != store.AIInvocationUsageComplete || invocations[0].ReasoningTokens == nil ||
		*invocations[0].ReasoningTokens != 0 || invocations[0].OutputTokens != 4 || invocations[0].EstimatedCostMicros <= 0 {
		t.Fatalf("reasoning_content 非空仍须如实记录 usage: invocations=%+v err=%v", invocations, err)
	}
	for i := range invocations {
		if invocations[i].Purpose != m5ai.PurposeIntent || invocations[i].Attempt != i+1 {
			t.Fatalf("intent 重试 attempt 记账错误: %+v", invocations[i])
		}
	}
	assertM5OrchestrationParkedWithoutFreeze(t, h, fixture, "reasoningUsageUnsafe")
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
			hand := &m5PositiveHand{now: h.clock.Now}
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
			// 2026-08-02 裁决:pre-effect 的旧轮连同 planned 动作作废,不再
			// 转人工冻结候选人;下轮巡检按最新账本边界重开新轮。
			assertM5OrchestrationSupersededWithoutEffect(t, h, fixture)
		})
	}
}

// assertM5TrialStillActive 钉住 2026-08-02 裁决的另一半:纯计算失败不冻结
// 候选人,试运行 active slot 原样保留。
func assertM5TrialStillActive(t *testing.T, h *harness) {
	t.Helper()
	trial, err := h.db.M5TrialStatus()
	if err != nil || trial == nil || trial.Selection.Status != store.M5TrialSelectionActive ||
		trial.Selection.ActiveSlot == nil {
		t.Fatalf("纯计算失败不得冻结试运行: trial=%+v err=%v", trial, err)
	}
}

// assertM5OrchestrationParkedWithoutFreeze 断言 2026-08-02 裁决的停靠形态:
// turn 落 manualRequired 挡住同输入继续烧钱,零 effect;但试运行不连带冻结。
func assertM5OrchestrationParkedWithoutFreeze(
	t *testing.T,
	h *harness,
	fixture m5AdviceFixture,
	reason string,
) {
	t.Helper()
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnManualRequired || turn.FailureReason != reason {
		t.Fatalf("turn 未停靠人工: turn=%+v err=%v", turn, err)
	}
	action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || action != nil {
		t.Fatalf("停靠前不应创建 action: action=%+v err=%v", action, err)
	}
	assertM5TrialStillActive(t, h)
	intent, err := h.db.LatestEffectIntent(h.key.Platform, h.key.AccountRef, fixture.conversationRef)
	if err != nil || intent != nil {
		t.Fatalf("不得产生 chat.sendMessage effect intent: intent=%+v err=%v", intent, err)
	}
	if count := countM5SendMessageCommands(t, h); count != 0 {
		t.Fatalf("不得产生 chat.sendMessage Cmd: %d", count)
	}
}

// assertM5OrchestrationSupersededWithoutEffect 钉住 2026-08-02 裁决的边界失配
// 终局:未派发过的旧轮连同其未发动作显式作废(boundarySuperseded),候选人
// 与试运行保持 active,零 effect intent、零发送命令。
func assertM5OrchestrationSupersededWithoutEffect(
	t *testing.T,
	h *harness,
	fixture m5AdviceFixture,
) {
	t.Helper()
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnSuperseded ||
		turn.FailureReason != "boundarySuperseded" {
		t.Fatalf("turn 未作废: turn=%+v err=%v", turn, err)
	}
	action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || action == nil || action.Status != store.CommunicationActionSuperseded ||
		action.FailureReason != "boundarySuperseded" || action.EffectIntentID != nil {
		t.Fatalf("planned action 未随轮作废: action=%+v err=%v", action, err)
	}
	assertM5TrialStillActive(t, h)
	intent, err := h.db.LatestEffectIntent(h.key.Platform, h.key.AccountRef, fixture.conversationRef)
	if err != nil || intent != nil {
		t.Fatalf("不得产生 chat.sendMessage effect intent: intent=%+v err=%v", intent, err)
	}
	if count := countM5SendMessageCommands(t, h); count != 0 {
		t.Fatalf("不得产生 chat.sendMessage Cmd: %d", count)
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
