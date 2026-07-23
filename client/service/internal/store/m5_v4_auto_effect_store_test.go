package store

import (
	"reflect"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"
	"recruithelper/contract/gen/go/protocol"

	"gorm.io/gorm"
)

type communicationV4AutomaticEffectFixture struct {
	resumeStoreFixture
	Turn   DialogueTurn
	Action CommunicationAction
	Now    time.Time
}

func seedPlannedCommunicationV4AutomaticAction(
	t *testing.T,
	s *Store,
	suffix string,
) communicationV4AutomaticEffectFixture {
	t.Helper()
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-auto-effect-"+suffix)
	if err := s.db.Where("profile_id = ?", fixture.ProfileID).
		Delete(&M5TrialSelection{}).Error; err != nil {
		t.Fatal(err)
	}
	inboundText := "合成候选人普通回复-" + suffix
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: textcanon.Hash(inboundText), Text: &inboundText,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	intentID := "invocation-v4-auto-intent-" + suffix
	if reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: intentID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-v4-auto-intent-" + suffix, CreatedAt: now,
	}); err != nil || !reserved.Created {
		t.Fatalf("V4 intent 未预留: result=%+v err=%v", reserved, err)
	}
	if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: successfulInvocationCompletion(intentID, now.Add(time.Second)),
		Label:      m5ai.IntentInterested,
		Source:     DialogueIntentLLM,
	}); err != nil {
		t.Fatal(err)
	}
	replyID := "invocation-v4-auto-reply-" + suffix
	if reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: replyID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-v4-auto-reply-" + suffix, CreatedAt: now.Add(2 * time.Second),
	}); err != nil || !reserved.Created {
		t.Fatalf("V4 reply 未预留: result=%+v err=%v", reserved, err)
	}
	replyText := "合成自动回复-" + suffix
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: successfulInvocationCompletion(replyID, now.Add(3*time.Second)),
		ActionID:   "caller-action-id-is-ignored-" + suffix,
		Text:       replyText, ContentHash: textcanon.Hash(replyText),
		PlannedAt: now.Add(3 * time.Second),
	})
	if err != nil || action == nil || action.Status != CommunicationActionPlanned ||
		action.ActionID != frozen.Turn.TurnID+"|replyText" {
		t.Fatalf("V4 planned action 构造失败: action=%+v err=%v", action, err)
	}
	return communicationV4AutomaticEffectFixture{
		resumeStoreFixture: fixture,
		Turn:               frozen.Turn,
		Action:             *action,
		Now:                now.Add(4 * time.Second),
	}
}

func communicationV4AutomaticEffectRequest(
	t *testing.T,
	s *Store,
	fixture communicationV4AutomaticEffectFixture,
	suffix string,
) CreateEffectIntentRequest {
	t.Helper()
	intentID, err := M5AutomaticIntentID(fixture.Action.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	args, err := protocol.Encode(protocol.ChatSendMessageArgs{
		ConversationRef: fixture.ConversationRef,
		Text:            fixture.Action.Text,
	})
	if err != nil {
		t.Fatal(err)
	}
	previousIntentID := ""
	latest, err := s.LatestEffectIntent(
		fixture.Platform,
		fixture.AccountRef,
		fixture.ConversationRef,
	)
	if err != nil {
		t.Fatal(err)
	}
	if latest != nil {
		previousIntentID = latest.IntentID
	}
	deadline := fixture.Now.Add(time.Hour).UnixMilli()
	return CreateEffectIntentRequest{
		Intent: EffectIntent{
			IntentID: intentID, IdemKey: "idem-v4-auto-effect-" + suffix,
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			Primitive: primitiveChatSendMessage, TargetRef: fixture.ConversationRef,
			PayloadHash: "payload-v4-auto-effect-" + suffix,
			GuardsHash:  "guards-v4-auto-effect-" + suffix,
			Status:      EffectIntentDispatching, DeadlineMs: deadline,
			SendFingerprint: fixture.Action.ContentHash,
		},
		Command: CmdRecord{
			MsgID: "msg-v4-auto-effect-" + suffix,
			Name:  primitiveChatSendMessage, Class: "effectful",
			IdemKey:  "idem-v4-auto-effect-" + suffix,
			Domain:   fixture.Platform + ":" + fixture.AccountRef,
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ExpectedPrincipalFingerprint: fixture.Principal,
			IntentID:                     intentID, HandID: fixture.HandID, Session: fixture.Session,
			BootIDAtDispatch: fixture.BootID, Args: string(args),
			Status: CmdQueued, DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		ExpectedTailSeq:   fixture.Turn.InboundThroughSeq,
		PreviousIntentID:  previousIntentID,
		AutomaticActionID: fixture.Action.ActionID,
		Now:               fixture.Now,
	}
}

func createCommunicationV4AutomaticEffect(
	t *testing.T,
	s *Store,
	suffix string,
) (
	communicationV4AutomaticEffectFixture,
	CreateEffectIntentRequest,
	*CreateEffectIntentResult,
) {
	t.Helper()
	fixture := seedPlannedCommunicationV4AutomaticAction(t, s, suffix)
	req := communicationV4AutomaticEffectRequest(t, s, fixture, suffix)
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("V4 intent/WAL 构造失败: result=%+v err=%v", created, err)
	}
	return fixture, req, created
}

func assertCommunicationV4HasNoTrial(
	t *testing.T,
	s *Store,
	profileID string,
) {
	t.Helper()
	var trials int64
	if err := s.db.Model(&M5TrialSelection{}).
		Where("profile_id = ?", profileID).
		Count(&trials).Error; err != nil || trials != 0 {
		t.Fatalf("V4 生产路径不得依赖 trial: count=%d err=%v", trials, err)
	}
}

func TestCommunicationV4AutomaticActionConstructsWALWithoutTrial(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createCommunicationV4AutomaticEffect(t, s, "construct")
	assertCommunicationV4HasNoTrial(t, s, fixture.ProfileID)

	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionEffectPending ||
		action.EffectIntentID == nil || *action.EffectIntentID != req.Intent.IntentID {
		t.Fatalf("V4 action 未与 WAL 原子绑定: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnDispatching {
		t.Fatalf("V4 turn 未进入 dispatching: turn=%+v err=%v", turn, err)
	}
	replayed, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || replayed.Created ||
		replayed.Intent.IntentID != created.Intent.IntentID ||
		replayed.Command.MsgID != created.Command.MsgID {
		t.Fatalf("V4 WAL 重放发生增生: result=%+v err=%v", replayed, err)
	}
}

func TestCommunicationV4AutomaticPositiveEvidenceAdvancesCursorAndAllowsNextTurn(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createCommunicationV4AutomaticEffect(t, s, "positive")
	before, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || before.State.LastBodyAt == nil {
		t.Fatalf("缺少招呼正文时钟基线: aggregate=%+v err=%v", before, err)
	}
	resultAt := fixture.Now.Add(time.Minute)
	result, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-v4-auto-effect-positive",
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdOk
			cmd.TerminalAt = &resultAt
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentOk, Append: true,
				Text: fixture.Action.Text, ContentHash: fixture.Action.ContentHash,
				ObservedAtMs: resultAt.UnixMilli(),
			}}, nil
		},
	)
	if err != nil || !result.CommandFound || result.AlreadyProcessed {
		t.Fatalf("V4 正证入账失败: result=%+v err=%v", result, err)
	}
	assertCommunicationV4HasNoTrial(t, s, fixture.ProfileID)
	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionSent ||
		action.SentAt == nil || action.EffectIntentID == nil ||
		*action.EffectIntentID != req.Intent.IntentID {
		t.Fatalf("V4 action 未收敛 sent: action=%+v err=%v", action, err)
	}
	confirmedAt := *action.SentAt
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnCompleted {
		t.Fatalf("V4 turn 未收敛 completed: turn=%+v err=%v", turn, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.Revision != 4 || aggregate.ProjectedThroughSeq != 3 ||
		aggregate.State.LastOutboundMessageSeq != 3 ||
		aggregate.State.LastOutboundAt == nil ||
		!aggregate.State.LastOutboundAt.Equal(confirmedAt) ||
		aggregate.State.ClockUncertain ||
		aggregate.State.LastBodyAt == nil ||
		!aggregate.State.LastBodyAt.Equal(confirmedAt) ||
		aggregate.State.BodyClockUncertain ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("V4 正证未以脑侧确认时刻推进状态/游标: aggregate=%+v err=%v", aggregate, err)
	}
	var confirmedMessage Message
	if err := s.db.First(
		&confirmedMessage,
		"outbound_intent_id = ?",
		req.Intent.IntentID,
	).Error; err != nil || confirmedMessage.TsApproxMs != nil {
		t.Fatalf("保守 V4 时钟不得伪造平台消息时间: message=%+v err=%v",
			confirmedMessage, err)
	}
	var messages, confirmations int64
	if err := s.db.Model(&Message{}).
		Where("outbound_intent_id = ? AND retracted_at IS NULL", req.Intent.IntentID).
		Count(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
		Where("profile_id = ? AND input_kind = ? AND input_key = ?",
			fixture.ProfileID, CommunicationV4InputConfirmedAction, fixture.Action.ActionID).
		Count(&confirmations).Error; err != nil {
		t.Fatal(err)
	}
	if messages != 1 || confirmations != 1 {
		t.Fatalf("V4 正证发生事实增生: messages=%d confirmations=%d", messages, confirmations)
	}
	laterReplayAt := confirmedAt.Add(time.Hour)
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return applyM5AutomaticEffectStatusByIDTx(
			tx,
			req.Intent.IntentID,
			laterReplayAt,
		)
	}); err != nil {
		t.Fatalf("V4 正证状态重放失败: %v", err)
	}
	replayedAction, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	replayedAggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || replayedAction == nil || replayedAction.SentAt == nil ||
		!replayedAction.SentAt.Equal(confirmedAt) ||
		aggregateErr != nil || replayedAggregate.Revision != aggregate.Revision ||
		replayedAggregate.State.LastOutboundAt == nil ||
		!replayedAggregate.State.LastOutboundAt.Equal(confirmedAt) ||
		replayedAggregate.State.LastBodyAt == nil ||
		!replayedAggregate.State.LastBodyAt.Equal(confirmedAt) {
		t.Fatalf(
			"较晚重放不得漂移首次确认锚: action=%+v aggregate=%+v err=%v aggregateErr=%v",
			replayedAction,
			replayedAggregate,
			err,
			aggregateErr,
		)
	}

	nextText := "合成第二轮候选人回复"
	nextInbound := appendCommunicationV4Inbound(t, s, fixture.resumeStoreFixture, Message{
		Seq: 4, Direction: "in", Kind: "text",
		ContentHash: textcanon.Hash(nextText), Text: &nextText,
	})
	var lastOutbound Message
	if err := s.db.First(
		&lastOutbound,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
		fixture.Platform, fixture.AccountRef, fixture.ConversationRef, 3,
	).Error; err != nil {
		t.Fatal(err)
	}
	digest, turnID, err := DialogueTurnIdentity(
		fixture.ProfileID,
		lastOutbound,
		nextInbound,
	)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := s.CommunicationTargetsForAccount(AccountKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	})
	if err != nil || len(targets) != 1 {
		t.Fatalf("正证后目标不可继续: targets=%+v err=%v", targets, err)
	}
	next, err := s.FreezeCommunicationV4Turn(FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: fixture.ProfileID,
		ConversationRef: fixture.ConversationRef, InputDigest: digest,
		HistoryThroughSeq: 3, InboundFromSeq: 4, InboundThroughSeq: 4,
		ContextRevisionHash: targets[0].ContextRevision.RevisionHash,
		ResumeSnapshotID:    targets[0].ResumeSnapshot.SnapshotID,
		RecommendedTimeText: "合成推荐时段",
		RenderFormatVersion: m5ai.DialogueRenderFormatVersion,
		FrozenAt:            resultAt.Add(time.Minute),
	})
	if err != nil || !next.Created || next.Turn.Status != DialogueTurnCollected ||
		next.Aggregate.ProjectedThroughSeq != 4 {
		t.Fatalf("V4 正证后第二轮无法冻结: result=%+v err=%v", next, err)
	}
}

func TestCommunicationV4AutomaticFailureRequiresManualWithoutTrial(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createCommunicationV4AutomaticEffect(t, s, "failed")
	resultAt := fixture.Now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-v4-auto-effect-failed",
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdFailed
			cmd.SideEffect = "none"
			cmd.TerminalAt = &resultAt
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentFailed, Reason: "failedNone",
			}}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	assertCommunicationV4HasNoTrial(t, s, fixture.ProfileID)
	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil ||
		action.Status != CommunicationActionManualRequired ||
		action.FailureReason != "effectFailed" || action.SentAt != nil {
		t.Fatalf("V4 failed action 未转人工: action=%+v err=%v", action, err)
	}
	turn, _ := s.DialogueTurnByID(fixture.Turn.TurnID)
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if turn == nil || turn.Status != DialogueTurnManualRequired ||
		turn.FailureReason != "effectFailed" ||
		aggregateErr != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != "effectFailed" ||
		aggregate.ProjectedThroughSeq != 2 {
		t.Fatalf("V4 failed 未原子收敛: turn=%+v aggregate=%+v err=%v",
			turn, aggregate, aggregateErr)
	}
	var messages int64
	if err := s.db.Model(&Message{}).
		Where("outbound_intent_id = ?", req.Intent.IntentID).
		Count(&messages).Error; err != nil || messages != 0 {
		t.Fatalf("V4 failed 不得伪造消息: count=%d err=%v", messages, err)
	}
}

func TestCommunicationV4AutomaticSuspectVerdictsPreserveFirstManualReason(t *testing.T) {
	t.Run("resolved failed", func(t *testing.T) {
		s := openTest(t)
		fixture, req, created := createCommunicationV4AutomaticEffect(t, s, "resolved-failed")
		verifyAt := fixture.Now.Add(time.Minute)
		if err := s.MoveEffectToVerification(
			created.Command.MsgID,
			"resultLost",
			verifyAt,
		); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkEffectSuspect(
			created.Command.MsgID,
			"verificationExhausted",
			verifyAt.Add(time.Minute),
		); err != nil {
			t.Fatal(err)
		}
		if err := s.ResolveSuspectVerdict(ResolveSuspectVerdictRequest{
			Ref: created.Command.MsgID, Verdict: CmdResolvedFailed,
			ConversationKey: ConversationKey{
				Platform: fixture.Platform, AccountRef: fixture.AccountRef,
				ConversationRef: fixture.ConversationRef,
			},
			At: verifyAt.Add(2 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		action, _ := s.CommunicationActionByTurn(fixture.Turn.TurnID)
		aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if action == nil || action.Status != CommunicationActionManualRequired ||
			action.FailureReason != "effectResolvedFailed" ||
			err != nil ||
			aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
			aggregate.ManualReason != "effectSuspect" {
			t.Fatalf("resolvedFailed 覆盖了首次人工原因或未终局: action=%+v aggregate=%+v err=%v",
				action, aggregate, err)
		}
		var messages int64
		if err := s.db.Model(&Message{}).
			Where("outbound_intent_id = ?", req.Intent.IntentID).
			Count(&messages).Error; err != nil || messages != 0 {
			t.Fatalf("resolvedFailed 不得追加消息: count=%d err=%v", messages, err)
		}
	})

	t.Run("late positive", func(t *testing.T) {
		s := openTest(t)
		fixture, req, created := createCommunicationV4AutomaticEffect(t, s, "late-positive")
		verifyAt := fixture.Now.Add(time.Minute)
		if err := s.MoveEffectToVerification(
			created.Command.MsgID,
			"resultLost",
			verifyAt,
		); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkEffectSuspect(
			created.Command.MsgID,
			"verificationExhausted",
			verifyAt.Add(time.Minute),
		); err != nil {
			t.Fatal(err)
		}
		beforeConfirmation, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if err != nil {
			t.Fatal(err)
		}
		resolvedAt := verifyAt.Add(2 * time.Minute)
		if err := s.ResolveSuspectVerdict(ResolveSuspectVerdictRequest{
			Ref: created.Command.MsgID, Verdict: CmdResolvedOk,
			ConversationKey: ConversationKey{
				Platform: fixture.Platform, AccountRef: fixture.AccountRef,
				ConversationRef: fixture.ConversationRef,
			},
			Text: fixture.Action.Text, ContentHash: fixture.Action.ContentHash,
			At: resolvedAt,
		}); err != nil {
			t.Fatal(err)
		}
		action, _ := s.CommunicationActionByTurn(fixture.Turn.TurnID)
		turn, _ := s.DialogueTurnByID(fixture.Turn.TurnID)
		aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if action == nil || action.Status != CommunicationActionSent ||
			turn == nil || turn.Status != DialogueTurnCompleted ||
			err != nil || aggregate.ProjectedThroughSeq != 3 ||
			aggregate.State.LastOutboundMessageSeq != 3 ||
			aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
			aggregate.ManualReason != "effectSuspect" {
			t.Fatalf("迟到正证未推进事实或错误解除人工闸: action=%+v turn=%+v aggregate=%+v err=%v",
				action, turn, aggregate, err)
		}
		var messages, confirmations int64
		if err := s.db.Model(&Message{}).
			Where("outbound_intent_id = ? AND retracted_at IS NULL", req.Intent.IntentID).
			Count(&messages).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
			Where("profile_id = ? AND input_kind = ?",
				fixture.ProfileID, CommunicationV4InputConfirmedAction).
			Count(&confirmations).Error; err != nil {
			t.Fatal(err)
		}
		if messages != 1 || confirmations != 1 {
			t.Fatalf("迟到正证发生增生: messages=%d confirmations=%d",
				messages, confirmations)
		}

		lateAt := resolvedAt.Add(time.Minute)
		result, err := s.ApplyResultMessage(
			created.Command.MsgID,
			"late-safe-v4-auto-effect",
			"result",
			fixture.HandID,
			func(cmd *CmdRecord) (ResultCommandMutation, error) {
				cmd.Status = CmdFailed
				cmd.SideEffect = "none"
				cmd.TerminalAt = &lateAt
				return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
					IntentStatus: EffectIntentFailed, Retract: true,
					Reason: "lateFailedNone",
				}}, nil
			},
		)
		if err != nil || !result.CommandFound || result.AlreadyProcessed {
			t.Fatalf("迟到安全终局未被收编: result=%+v err=%v", result, err)
		}
		action, _ = s.CommunicationActionByTurn(fixture.Turn.TurnID)
		turn, _ = s.DialogueTurnByID(fixture.Turn.TurnID)
		aggregate, err = s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if action == nil ||
			action.Status != CommunicationActionManualRequired ||
			action.FailureReason != "effectFailed" ||
			action.SentAt != nil ||
			turn == nil ||
			turn.Status != DialogueTurnManualRequired ||
			turn.FailureReason != "effectFailed" ||
			err != nil ||
			aggregate.Revision != 5 ||
			aggregate.ProjectedThroughSeq != beforeConfirmation.ProjectedThroughSeq ||
			!reflect.DeepEqual(aggregate.State, beforeConfirmation.State) ||
			aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
			aggregate.ManualReason != "effectSuspect" {
			t.Fatalf("迟到安全终局未补偿 V4 正证: action=%+v turn=%+v aggregate=%+v err=%v",
				action, turn, aggregate, err)
		}
		var activeMessages, retractions int64
		if err := s.db.Model(&Message{}).
			Where("outbound_intent_id = ? AND retracted_at IS NULL", req.Intent.IntentID).
			Count(&activeMessages).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
			Where("profile_id = ? AND input_kind = ? AND input_key = ?",
				fixture.ProfileID, CommunicationV4InputRetractedAction, fixture.Action.ActionID).
			Count(&retractions).Error; err != nil {
			t.Fatal(err)
		}
		if activeMessages != 0 || retractions != 1 {
			t.Fatalf("迟到安全终局仍留下活动消息或补偿增生: active=%d retractions=%d",
				activeMessages, retractions)
		}
	})
}

func TestCommunicationV4ConfirmedActionRejectsProjectionGap(t *testing.T) {
	s := openTest(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	_, root := seedSuccessfulV4Greeting(
		t,
		s,
		"v4-confirmed-gap",
		"conversation-v4-confirmed-gap",
		at,
	)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		_, _, _, err := applyCommunicationV4ConfirmedActionTx(
			tx,
			root.ProfileID,
			communication.V4ConfirmedAction{
				ActionKey: "gap-action", Kind: communication.V4ActionReplyText,
				MessageSeq: 3,
			},
			at.Add(time.Minute),
		)
		return err
	})
	if err == nil {
		t.Fatal("跨过未投影 seq 的正证必须拒绝")
	}
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(root.ProfileID)
	if aggregateErr != nil || aggregate.Revision != 0 ||
		aggregate.ProjectedThroughSeq != 1 {
		t.Fatalf("序号缺口失败事务污染聚合: aggregate=%+v err=%v",
			aggregate, aggregateErr)
	}
}
