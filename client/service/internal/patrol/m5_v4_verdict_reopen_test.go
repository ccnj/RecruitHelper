package patrol

// 裁决即恢复的巡检层端到端回归(2026-08-27 停机点第二步,审查修复 B4 的正
// 主验证):suspect 经人工裁决 resolvedFailed 后,账本没有任何新行,下一巡检
// 轮必须在同一输入边界立即重开新轮(裁决代次入指纹)、重新规划并真正把新
// 回复发出去——不再等候选人开口或时刻表触发。批内曾有两处会把它短路:巡检
// 的"账本尾<=游标"快路径(已拆)与冻结校验的游标窗口(已放开为 锚≤游标≤边界尾)。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/textcanon"
	"recruithelper/contract/gen/go/protocol"
)

func TestCommunicationV4PatrolReopensSameBoundaryAfterResolvedFailedVerdict(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(t, h, "verdict-reopen", "岗位还在招人吗")
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
		fixture.profileID, *greeting, []store.Message{*inbound}, 0,
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
		t.Fatalf("首轮冻结失败: result=%+v err=%v", frozen, err)
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
		InvocationID: "invocation-verdict-intent", TurnID: turnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-verdict-intent", CreatedAt: now,
	}); err != nil || !reserved.Created {
		t.Fatalf("intent 预留失败: result=%+v err=%v", reserved, err)
	}
	if _, err := h.db.CompleteIntentInvocation(store.CompleteIntentInvocationRequest{
		Completion: completion("invocation-verdict-intent", now.Add(time.Second)),
		Label:      m5ai.IntentInterested,
		Source:     store.DialogueIntentLLM,
	}); err != nil {
		t.Fatal(err)
	}
	if reserved, err := h.db.ReserveAIInvocation(store.ReserveAIInvocationRequest{
		InvocationID: "invocation-verdict-reply", TurnID: turnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-verdict-reply", CreatedAt: now.Add(2 * time.Second),
	}); err != nil || !reserved.Created {
		t.Fatalf("reply 预留失败: result=%+v err=%v", reserved, err)
	}
	replyText := "第一次尝试的合成回复"
	action, err := h.db.CompleteReplyInvocation(store.CompleteReplyInvocationRequest{
		Completion: completion("invocation-verdict-reply", now.Add(3*time.Second)),
		ActionID:   "ignored",
		Text:       replyText, ContentHash: textcanon.Hash(replyText),
		PlannedAt: now.Add(3 * time.Second),
	})
	if err != nil || action == nil || action.Status != store.CommunicationActionPlanned {
		t.Fatalf("planned 动作构造失败: action=%+v err=%v", action, err)
	}
	intentID, err := store.M5AutomaticIntentID(action.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	args, err := protocol.Encode(protocol.ChatSendMessageArgs{
		ConversationRef: fixture.conversationRef, Text: action.Text,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(time.Hour).UnixMilli()
	created, err := h.db.CreateEffectIntentAndCmd(store.CreateEffectIntentRequest{
		Intent: store.EffectIntent{
			IntentID: intentID, IdemKey: "idem-verdict-reopen",
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			Primitive: protocol.PrimChatSendMessage, TargetRef: fixture.conversationRef,
			PayloadHash: "payload-verdict-reopen", GuardsHash: "guards-verdict-reopen",
			Status: store.EffectIntentDispatching, DeadlineMs: deadline,
			SendFingerprint: action.ContentHash,
		},
		Command: store.CmdRecord{
			MsgID: "msg-verdict-reopen",
			Name:  protocol.PrimChatSendMessage, Class: string(protocol.ClassEffectful),
			IdemKey:  "idem-verdict-reopen",
			Domain:   h.key.Platform + ":" + h.key.AccountRef,
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ExpectedPrincipalFingerprint: "principal-1",
			IntentID:                     intentID, HandID: "hand-1",
			Session: "session-1", BootIDAtDispatch: "boot-1", Args: string(args),
			Status: store.CmdQueued, DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		ExpectedTailSeq:   inbound.Seq,
		AutomaticActionID: action.ActionID,
		Now:               now.Add(4 * time.Second),
	})
	if err != nil || !created.Created {
		t.Fatalf("WAL 构造失败: result=%+v err=%v", created, err)
	}
	if err := h.db.MoveEffectToVerification(
		created.Command.MsgID, "resultLost", now.Add(5*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if err := h.db.MarkEffectSuspect(
		created.Command.MsgID, "verificationExhausted", now.Add(6*time.Second),
	); err != nil {
		t.Fatal(err)
	}

	// 人工裁决:没发出去。裁决事务应作废旧轮、代次加一、解冻聚合。
	if err := h.db.ResolveSuspectVerdict(store.ResolveSuspectVerdictRequest{
		Ref: created.Command.MsgID, Verdict: store.CmdResolvedFailed,
		ConversationKey: store.ConversationKey{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ConversationRef: fixture.conversationRef,
		},
		At: now.Add(7 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil ||
		resolved.AutomationStatus != store.ProfileCommunicationAutomationActive ||
		resolved.VerdictGeneration != 1 {
		t.Fatalf("裁决即恢复未解冻或未加代次: aggregate=%+v err=%v", resolved, err)
	}

	// 账本零新行,下一巡检轮必须原边界立即重开并把新回复真正发出去。
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				return safeFakeResponse(`{"话术_序列":["裁决后重新规划的回复"],"动作":"无"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
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
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	beginCommunicationV4PatrolRound(t, h, "round-verdict-reopen")
	actor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: "round-verdict-reopen", now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || reopened == nil || reopened.TurnID == turnID ||
		reopened.Status != store.DialogueTurnCompleted ||
		reopened.InboundFromSeq != inbound.Seq ||
		reopened.InboundThroughSeq != inbound.Seq {
		t.Fatalf("裁决后未在原边界立即重开新轮: turn=%+v err=%v", reopened, err)
	}
	// 账本里恰两条发送命令:被裁决的旧 suspect 与重开轮的新回复;真正经手
	// 派发的只有新回复一条。
	if countM5SendMessageCommands(t, h) != 2 || hand.commandCount() != 1 {
		t.Fatalf("重开轮应恰好新发送一条回复: cmds=%d handSends=%d",
			countM5SendMessageCommands(t, h), hand.commandCount())
	}
	old, err := h.db.DialogueTurnByID(turnID)
	if err != nil || old == nil || old.Status != store.DialogueTurnSuperseded {
		t.Fatalf("旧轮必须保持作废留档: turn=%+v err=%v", old, err)
	}
	intents, err := h.db.EffectIntentByID(intentID)
	if err != nil || intents == nil || intents.Status != store.EffectIntentResolvedFailed {
		t.Fatalf("旧 intent 终局必须原样留档: %+v err=%v", intents, err)
	}
}
