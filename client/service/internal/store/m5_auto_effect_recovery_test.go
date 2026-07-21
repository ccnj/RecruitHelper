package store

import (
	"testing"
	"time"
)

func TestM5AutomaticAmbiguityStaysOnM3VerificationRailUntilSuspect(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createM5AutomaticEffect(t, s, "verification-rail")
	verifyAt := fixture.Now.Add(time.Minute)
	if err := s.MoveEffectToVerification(created.Command.MsgID, "sideEffectPossible", verifyAt); err != nil {
		t.Fatal(err)
	}
	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionEffectPending ||
		action.EffectIntentID == nil || *action.EffectIntentID != req.Intent.IntentID {
		t.Fatalf("验证中的 M5 action 必须继续由原 effect intent 持有: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnDispatching {
		t.Fatalf("验证中不得伪造业务终局: turn=%+v err=%v", turn, err)
	}

	suspectAt := verifyAt.Add(time.Minute)
	if err := s.MarkEffectSuspect(created.Command.MsgID, "verificationExhausted", suspectAt); err != nil {
		t.Fatal(err)
	}
	action, err = s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionManualRequired ||
		action.FailureReason != "effectSuspect" || action.SentAt != nil {
		t.Fatalf("suspect 必须收敛 M5 action 转人工: action=%+v err=%v", action, err)
	}
	assertTrialManualRequired(t, s, "effectSuspect")

	resolvedAt := suspectAt.Add(time.Minute)
	if err := s.ResolveSuspectVerdict(ResolveSuspectVerdictRequest{
		Ref: created.Command.MsgID, Verdict: CmdResolvedFailed,
		ConversationKey: ConversationKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ConversationRef: fixture.ConversationRef,
		},
		At: resolvedAt,
	}); err != nil {
		t.Fatal(err)
	}
	action, err = s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionManualRequired ||
		action.FailureReason != "effectResolvedFailed" || action.SentAt != nil {
		t.Fatalf("resolvedFailed 必须保持少发终局: action=%+v err=%v", action, err)
	}
	var messages int64
	if err := s.db.Model(&Message{}).Where("outbound_intent_id = ?", req.Intent.IntentID).
		Count(&messages).Error; err != nil || messages != 0 {
		t.Fatalf("resolvedFailed 不得追加 self 消息: count=%d err=%v", messages, err)
	}
}

func TestM5AutomaticLatePositiveVerdictCorrectsSuspectToOneSentFact(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createM5AutomaticEffect(t, s, "late-positive")
	verifyAt := fixture.Now.Add(time.Minute)
	if err := s.MoveEffectToVerification(created.Command.MsgID, "resultLost", verifyAt); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEffectSuspect(created.Command.MsgID, "verificationExhausted", verifyAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	resolvedAt := verifyAt.Add(2 * time.Minute)
	if err := s.ResolveSuspectVerdict(ResolveSuspectVerdictRequest{
		Ref: created.Command.MsgID, Verdict: CmdResolvedOk,
		ConversationKey: ConversationKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ConversationRef: fixture.ConversationRef,
		},
		Text: fixture.Action.Text, ContentHash: fixture.Action.ContentHash, At: resolvedAt,
	}); err != nil {
		t.Fatal(err)
	}
	assertM5AutomaticSent(t, s, fixture, req.Intent.IntentID)
	var messages int64
	if err := s.db.Model(&Message{}).Where(
		"outbound_intent_id = ? AND direction = ? AND origin = ? AND retracted_at IS NULL",
		req.Intent.IntentID, "out", "self",
	).Count(&messages).Error; err != nil || messages != 1 {
		t.Fatalf("迟到正证必须只收敛一条 self 消息: count=%d err=%v", messages, err)
	}

	// A terminal verdict is immutable; retrying the human operation cannot
	// append another message or forge a second business action.
	if err := s.ResolveSuspectVerdict(ResolveSuspectVerdictRequest{
		Ref: created.Command.MsgID, Verdict: CmdResolvedOk,
		ConversationKey: ConversationKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ConversationRef: fixture.ConversationRef,
		},
		Text: fixture.Action.Text, ContentHash: fixture.Action.ContentHash,
		At: resolvedAt.Add(time.Second),
	}); err == nil {
		t.Fatal("终局人工裁决不得再次执行")
	}
	if err := s.db.Model(&Message{}).Where("outbound_intent_id = ?", req.Intent.IntentID).
		Count(&messages).Error; err != nil || messages != 1 {
		t.Fatalf("重复裁决后 self 消息数量漂移: count=%d err=%v", messages, err)
	}
}
