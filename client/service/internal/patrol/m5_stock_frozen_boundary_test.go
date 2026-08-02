package patrol

// 存量硬边界负例(2026-08-03,停机点战役独立审查轮点名):战役前形状的
// 存量冻结候选人——聚合 manualRequired/effectSuspect + 停靠轮 + suspect
// WAL——在没有新输入、没有新裁决的情况下,任凭巡检跑多少轮都必须一切
// 原样:不解冻、不作废、不重派、不触发 AI、不产生任何候选人可见动作。
// 这是"裁决即恢复"决策 1 的硬边界:恢复只由本次裁决事务触发,存量与
// 重放天然不触发。

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

func TestCommunicationV4PatrolLeavesStockFrozenCandidateUntouched(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(t, h, "stock-frozen", "岗位还在招人吗")
	now := h.clock.Now()

	// —— 用真实链构造存量冻结形状:冻结轮 → AI 建议落账 → WAL 派发 →
	//    验证穷尽 suspect(聚合 manualRequired/effectSuspect + 停靠轮)。——
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
		t.Fatalf("存量轮冻结失败: result=%+v err=%v", frozen, err)
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
		InvocationID: "invocation-stock-intent", TurnID: turnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-stock-intent", CreatedAt: now,
	}); err != nil || !reserved.Created {
		t.Fatalf("intent 预留失败: result=%+v err=%v", reserved, err)
	}
	if _, err := h.db.CompleteIntentInvocation(store.CompleteIntentInvocationRequest{
		Completion: completion("invocation-stock-intent", now.Add(time.Second)),
		Label:      m5ai.IntentInterested,
		Source:     store.DialogueIntentLLM,
	}); err != nil {
		t.Fatal(err)
	}
	if reserved, err := h.db.ReserveAIInvocation(store.ReserveAIInvocationRequest{
		InvocationID: "invocation-stock-reply", TurnID: turnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-stock-reply", CreatedAt: now.Add(2 * time.Second),
	}); err != nil || !reserved.Created {
		t.Fatalf("reply 预留失败: result=%+v err=%v", reserved, err)
	}
	replyText := "合成存量回复"
	action, err := h.db.CompleteReplyInvocation(store.CompleteReplyInvocationRequest{
		Completion: completion("invocation-stock-reply", now.Add(3*time.Second)),
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
	previousIntentID := ""
	if latest, err := h.db.LatestEffectIntent(
		h.key.Platform, h.key.AccountRef, fixture.conversationRef,
	); err != nil {
		t.Fatal(err)
	} else if latest != nil {
		previousIntentID = latest.IntentID
	}
	deadline := now.Add(time.Hour).UnixMilli()
	created, err := h.db.CreateEffectIntentAndCmd(store.CreateEffectIntentRequest{
		Intent: store.EffectIntent{
			IntentID: intentID, IdemKey: "idem-stock-frozen",
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			Primitive: protocol.PrimChatSendMessage, TargetRef: fixture.conversationRef,
			PayloadHash: "payload-stock-frozen", GuardsHash: "guards-stock-frozen",
			Status: store.EffectIntentDispatching, DeadlineMs: deadline,
			SendFingerprint: action.ContentHash,
		},
		Command: store.CmdRecord{
			MsgID: "msg-stock-frozen",
			Name:  protocol.PrimChatSendMessage, Class: string(protocol.ClassEffectful),
			IdemKey:  "idem-stock-frozen",
			Domain:   h.key.Platform + ":" + h.key.AccountRef,
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ExpectedPrincipalFingerprint: "principal-1",
			IntentID:                     intentID, HandID: "hand-1",
			Session: "session-1", BootIDAtDispatch: "boot-1", Args: string(args),
			Status: store.CmdQueued, DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		ExpectedTailSeq:   inbound.Seq,
		PreviousIntentID:  previousIntentID,
		AutomaticActionID: action.ActionID,
		Now:               now.Add(4 * time.Second),
	})
	if err != nil || !created.Created {
		t.Fatalf("存量 WAL 构造失败: result=%+v err=%v", created, err)
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

	// —— 前置自证并快照存量形状。——
	snapshot := func(label string) (
		store.DialogueTurn,
		store.CommunicationAction,
		store.CommunicationV4Aggregate,
		store.EffectIntent,
		store.CmdRecord,
	) {
		t.Helper()
		turn, err := h.db.DialogueTurnByID(turnID)
		if err != nil || turn == nil {
			t.Fatalf("%s: 轮读取失败: %+v err=%v", label, turn, err)
		}
		actionRow, err := h.db.CommunicationActionByTurn(turnID)
		if err != nil || actionRow == nil {
			t.Fatalf("%s: 动作读取失败: %+v err=%v", label, actionRow, err)
		}
		aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
		if err != nil {
			t.Fatalf("%s: 聚合读取失败: err=%v", label, err)
		}
		intent, err := h.db.EffectIntentByID(intentID)
		if err != nil || intent == nil {
			t.Fatalf("%s: intent 读取失败: %+v err=%v", label, intent, err)
		}
		cmd, err := h.db.CmdByMsgID(created.Command.MsgID)
		if err != nil || cmd == nil {
			t.Fatalf("%s: cmd 读取失败: %+v err=%v", label, cmd, err)
		}
		return *turn, *actionRow, *aggregate, *intent, *cmd
	}
	turnBefore, actionBefore, aggregateBefore, intentBefore, cmdBefore := snapshot("前置")
	if turnBefore.Status != store.DialogueTurnManualRequired ||
		actionBefore.Status != store.CommunicationActionManualRequired ||
		actionBefore.FailureReason != "effectSuspect" ||
		aggregateBefore.AutomationStatus != store.ProfileCommunicationAutomationManualRequired ||
		aggregateBefore.ManualReason != "effectSuspect" ||
		intentBefore.Status != store.EffectIntentSuspect ||
		cmdBefore.Status != store.CmdSuspect {
		t.Fatalf("存量冻结形状前置不成立: turn=%+v action=%+v aggregate=%+v intent=%+v cmd=%+v",
			turnBefore, actionBefore, aggregateBefore, intentBefore, cmdBefore)
	}

	// —— 无新输入、无新裁决,跑若干巡检轮。AI 与手一旦被触碰即失败。——
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			t.Fatalf("存量冻结候选人不得触发 AI: %+v", request.Purpose)
			return m5ai.CompletionResponse{}, nil
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
	for round := 1; round <= 3; round++ {
		roundID := fmt.Sprintf("round-stock-frozen-%d", round)
		beginCommunicationV4PatrolRound(t, h, roundID)
		actor := &roundActor{
			manager: manager, account: account,
			hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
			roundID: roundID, now: h.clock.Now(),
		}
		manager.mu.Lock()
		err = actor.processCommunicationV4Targets(context.Background())
		manager.mu.Unlock()
		if err != nil {
			t.Fatalf("第 %d 轮巡检不得因存量冻结候选人报错: %v", round, err)
		}
		h.clock.Add(h.config.PatrolInterval)
	}

	// —— 一切原样。——
	turnAfter, actionAfter, aggregateAfter, intentAfter, cmdAfter := snapshot("巡检后")
	if turnAfter.Status != turnBefore.Status ||
		turnAfter.FailureReason != turnBefore.FailureReason ||
		turnAfter.UpdatedAt != turnBefore.UpdatedAt {
		t.Fatalf("停靠轮被巡检触碰: before=%+v after=%+v", turnBefore, turnAfter)
	}
	if actionAfter.Status != actionBefore.Status ||
		actionAfter.FailureReason != actionBefore.FailureReason ||
		actionAfter.SentAt != nil ||
		actionAfter.UpdatedAt != actionBefore.UpdatedAt {
		t.Fatalf("停靠动作被巡检触碰: before=%+v after=%+v", actionBefore, actionAfter)
	}
	if aggregateAfter.AutomationStatus != aggregateBefore.AutomationStatus ||
		aggregateAfter.ManualReason != aggregateBefore.ManualReason ||
		aggregateAfter.Revision != aggregateBefore.Revision ||
		aggregateAfter.ProjectedThroughSeq != aggregateBefore.ProjectedThroughSeq {
		t.Fatalf("冻结聚合被巡检触碰: before=%+v after=%+v", aggregateBefore, aggregateAfter)
	}
	if intentAfter.Status != intentBefore.Status || cmdAfter.Status != cmdBefore.Status {
		t.Fatalf("suspect WAL 被巡检触碰: intent=%+v cmd=%+v", intentAfter, cmdAfter)
	}
	if len(advice.requests) != 0 || hand.commandCount() != 0 {
		t.Fatalf("存量冻结候选人不得触发建议或发送: advice=%d sends=%d",
			len(advice.requests), hand.commandCount())
	}
	if latestTurn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID); err != nil ||
		latestTurn == nil || latestTurn.TurnID != turnID {
		t.Fatalf("不得为冻结候选人开新轮: turn=%+v err=%v", latestTurn, err)
	}
	if latest, err := h.db.LatestEffectIntent(
		h.key.Platform, h.key.AccountRef, fixture.conversationRef,
	); err != nil || latest == nil || latest.IntentID != intentID {
		t.Fatalf("不得为冻结候选人铸新 intent: latest=%+v err=%v", latest, err)
	}
	// 解冻/作废/迟到重放族的审计一条都不许出现。
	audits, err := h.db.AuditEntries(200)
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range audits {
		switch audit.Category {
		case "communication_v4_resolved_failed_recovered",
			"communication_v4_resolved_failed_recovery_skipped",
			"communication_v4_automation_unfrozen",
			"communication_late_result_after_verdict":
			t.Fatalf("存量冻结候选人不得触发恢复/迟到重放机制: %+v", audit)
		}
	}
}
