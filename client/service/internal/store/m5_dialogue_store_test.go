package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

type dialogueStoreFixture struct {
	resumeStoreFixture
	SnapshotID   string
	RevisionHash string
	Greeting     Message
	FirstMessage Message
}

func seedDialogueStoreFixture(t *testing.T, s *Store, profileID, kind string) dialogueStoreFixture {
	t.Helper()
	base := seedResumeStoreFixture(t, s, profileID)
	now := time.Now().UTC().Truncate(time.Millisecond)
	fixture := dialogueStoreFixture{
		resumeStoreFixture: base,
		SnapshotID:         "snapshot-" + profileID,
		RevisionHash:       "revision-" + profileID,
	}
	backendJobID := "job-" + profileID
	revision := contextRevisionFixture(
		"context-"+profileID,
		fixture.RevisionHash,
		now,
	)
	revision.SourceKind = legacyJobConfigSourceKind
	revision.SourceJobRef = backendJobID
	if _, err := s.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{revision},
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&CandidateResumeSnapshot{
		SnapshotID: fixture.SnapshotID, ProfileID: profileID, SourceKind: "imConversation",
		SourceConversationRef: base.ConversationRef, SourceLogicalDispatchID: "capture-" + profileID,
		ObservedAt: now.UnixMilli(), CapturedAt: now, SchemaVersion: 1,
		ContentHash: "resume-hash-" + profileID, ResumeJSON: `{"basic":[]}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", profileID).Updates(map[string]any{
		"backend_job_id": backendJobID, "resume_capture_state": ResumeCaptureCaptured,
		"active_resume_snapshot_id": fixture.SnapshotID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&ProfileAIContextBinding{
		BindingID: "binding-" + profileID, ProfileID: profileID, ContextID: "context-" + profileID,
		RevisionHash: fixture.RevisionHash, Status: ProfileAIContextBindingActive,
		BoundBy: "user", BoundAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	greetingText := "合成招呼"
	fixture.Greeting = Message{
		Platform: base.Platform, AccountRef: base.AccountRef, ConversationRef: base.ConversationRef,
		Seq: 1, Direction: "out", Kind: "text", ContentHash: "greeting-hash-" + profileID,
		Text: &greetingText, Origin: "self", OutboundIntentID: &base.GreetingIntent,
		CreatedAt: now, UpdatedAt: now,
	}
	var text *string
	if kind == "text" {
		value := "合成入站消息"
		text = &value
	}
	fixture.FirstMessage = Message{
		Platform: base.Platform, AccountRef: base.AccountRef, ConversationRef: base.ConversationRef,
		Seq: 2, Direction: "in", Kind: kind, ContentHash: "message-hash-" + profileID,
		Text: text, Origin: "external", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.Create(&[]Message{fixture.Greeting, fixture.FirstMessage}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Conversation{}).Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ?",
		base.Platform, base.AccountRef, base.ConversationRef,
	).Update("last_message_seq", int64(2)).Error; err != nil {
		t.Fatal(err)
	}
	return fixture
}

func dialogueTurnRequest(fixture dialogueStoreFixture, turnID, _ string) FreezeDialogueTurnRequest {
	digest, _, err := DialogueTurnIdentity(fixture.ProfileID, fixture.Greeting, []Message{fixture.FirstMessage}, 0)
	if err != nil {
		panic(err)
	}
	return FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: fixture.ProfileID, ConversationRef: fixture.ConversationRef,
		InputDigest: digest, HistoryThroughSeq: 1, InboundFromSeq: 2, InboundThroughSeq: 2,
		ContextRevisionHash: fixture.RevisionHash, ResumeSnapshotID: fixture.SnapshotID,
		RecommendedTimeText: "合成推荐时段", RenderFormatVersion: "m5-render-v1",
		FrozenAt: time.Now().UTC().Truncate(time.Millisecond),
	}
}

func seedFrozenDialogueTurn(t *testing.T, s *Store, profileID string) (dialogueStoreFixture, DialogueTurn) {
	t.Helper()
	fixture := seedDialogueStoreFixture(t, s, profileID, "text")
	result, err := s.FreezeDialogueTurn(dialogueTurnRequest(fixture, "turn-"+profileID, "digest-"+profileID))
	if err != nil || !result.Created {
		t.Fatalf("冻结测试 turn: result=%+v err=%v", result, err)
	}
	return fixture, result.Turn
}

func TestFreezeDialogueTurnIsAtomicImmutableAndIdempotent(t *testing.T) {
	s := openTest(t)
	fixture := seedDialogueStoreFixture(t, s, "profile-dialogue-freeze", "text")
	req := dialogueTurnRequest(fixture, "turn-freeze", "digest-freeze")
	first, err := s.FreezeDialogueTurn(req)
	if err != nil || !first.Created || first.Turn.Status != DialogueTurnCollected {
		t.Fatalf("首次轮冻结失败: result=%+v err=%v", first, err)
	}
	profile, err := s.CandidateProfileByID(fixture.ProfileID)
	if err != nil || profile.MainStatus != CandidateProfileCommunicating || profile.CommunicatingAt == nil ||
		profile.FirstRealMessageSeq == nil || *profile.FirstRealMessageSeq != 2 {
		t.Fatalf("轮冻结未原子推进 communicating: profile=%+v err=%v", profile, err)
	}
	repeatedReq := req
	repeatedReq.TurnID = "turn-freeze-replayed-id"
	repeated, err := s.FreezeDialogueTurn(repeatedReq)
	if err != nil || repeated.Created || repeated.Turn.TurnID != first.Turn.TurnID {
		t.Fatalf("同 profile+digest 必须复用原轮: result=%+v err=%v", repeated, err)
	}
	conflicting := repeatedReq
	conflicting.RecommendedTimeText = "另一份时段"
	if _, err := s.FreezeDialogueTurn(conflicting); !errors.Is(err, ErrDialogueTurnConflict) {
		t.Fatalf("同 digest 的冻结材料变化必须冲突: %v", err)
	}
	var count int64
	if err := s.db.Model(&DialogueTurn{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("重复观察不得增生 turn: count=%d err=%v", count, err)
	}
}

func TestFreezeDialogueTurnRechecksActiveMessageTail(t *testing.T) {
	s := openTest(t)
	fixture := seedDialogueStoreFixture(t, s, "profile-dialogue-tail", "text")
	req := dialogueTurnRequest(fixture, "turn-tail", "digest-tail")
	secondText := "稍后到达的合成消息"
	if err := s.db.Create(&Message{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef, ConversationRef: fixture.ConversationRef,
		Seq: 3, Direction: "in", Kind: "text", ContentHash: "message-hash-later", Text: &secondText,
		Origin: "external",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.FreezeDialogueTurn(req); !errors.Is(err, ErrDialogueTurnBinding) {
		t.Fatalf("消息尾变化必须阻断旧边界冻结: %v", err)
	}
	profile, _ := s.CandidateProfileByID(fixture.ProfileID)
	if profile.MainStatus != CandidateProfileGreeted || profile.CommunicatingAt != nil || profile.FirstRealMessageSeq != nil {
		t.Fatalf("失败事务不得推进 profile: %+v", profile)
	}
	var count int64
	if err := s.db.Model(&DialogueTurn{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("失败事务不得留下 turn: count=%d err=%v", count, err)
	}
}

func seedMultiMessageDialogueTurn(t *testing.T, s *Store, profileID string) (dialogueStoreFixture, DialogueTurn) {
	t.Helper()
	fixture := seedDialogueStoreFixture(t, s, profileID, "text")
	secondText, thirdText := "合成中间消息", "合成末尾消息"
	second := Message{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef, ConversationRef: fixture.ConversationRef,
		Seq: 3, Direction: "in", Kind: "text", ContentHash: "middle-message-hash",
		Text: &secondText, Origin: "external",
	}
	third := Message{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef, ConversationRef: fixture.ConversationRef,
		Seq: 4, Direction: "in", Kind: "text", ContentHash: "last-message-hash",
		Text: &thirdText, Origin: "external",
	}
	if err := s.db.Create(&[]Message{second, third}).Error; err != nil {
		t.Fatal(err)
	}
	digest, _, err := DialogueTurnIdentity(
		fixture.ProfileID, fixture.Greeting, []Message{fixture.FirstMessage, second, third}, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	req := dialogueTurnRequest(fixture, "turn-"+profileID, "")
	req.InputDigest = digest
	req.InboundThroughSeq = 4
	turn, err := s.FreezeDialogueTurn(req)
	if err != nil || !turn.Created {
		t.Fatalf("冻结多消息轮失败: turn=%+v err=%v", turn, err)
	}
	return fixture, turn.Turn
}

func TestAIReservationToleratesRetractedMiddleMessageWhenTailUnchanged(t *testing.T) {
	// 2026-08-27 停机点第二步(协议 §7.4 bnd-v1):轮身份只锚输入尾条,
	// 中段撤回不再作废轮——AI 边界的中间新鲜度重验已删除,输入过时最多
	// 浪费一次 token;尾条变化仍由重建校验(candidateTail)阻断。
	s := openTest(t)
	fixture, turn := seedMultiMessageDialogueTurn(t, s, "profile-dialogue-retracted-middle")
	retractedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.db.Model(&Message{}).Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
		fixture.Platform, fixture.AccountRef, fixture.ConversationRef, 3,
	).Update("retracted_at", retractedAt).Error; err != nil {
		t.Fatal(err)
	}
	reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-retracted-middle", TurnID: turn.TurnID, Purpose: m5ai.PurposeIntent,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-retracted-middle",
	})
	if err != nil || reserved == nil || !reserved.Created {
		t.Fatalf("尾条未变时中段撤回不得阻断预留: reserved=%+v err=%v", reserved, err)
	}
	stored, _ := s.DialogueTurnByID(turn.TurnID)
	if stored == nil || stored.Status != DialogueTurnCollected {
		t.Fatalf("尾条未变的轮不得被作废: %+v", stored)
	}
}

func TestAdviceReadyActionSurvivesRetractedMiddleMessageWhenTailUnchanged(t *testing.T) {
	s := openTest(t)
	fixture, turn := seedMultiMessageDialogueTurn(t, s, "profile-dialogue-action-retracted")
	intentID := "invocation-action-retracted-intent"
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: intentID, TurnID: turn.TurnID, Purpose: m5ai.PurposeIntent,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-action-intent",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: successfulInvocationCompletion(intentID, now),
		Label:      m5ai.IntentInterested, Source: DialogueIntentLLM,
	}); err != nil {
		t.Fatal(err)
	}
	replyID := "invocation-action-retracted-reply"
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: replyID, TurnID: turn.TurnID, Purpose: m5ai.PurposeReply,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-action-reply",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: successfulInvocationCompletion(replyID, now.Add(time.Second)),
		ActionID:   "action-retracted-middle", Text: "不会自动发送的合成建议",
		ContentHash: "action-retracted-hash", PlannedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Message{}).Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
		fixture.Platform, fixture.AccountRef, fixture.ConversationRef, 3,
	).Update("retracted_at", now.Add(2*time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	current, err := s.RecheckDialogueTurnCurrent(turn.TurnID, now.Add(2*time.Second))
	if err != nil || !current {
		t.Fatalf("尾条未变时中段撤回不得作废 adviceReady 轮(§7.4 bnd-v1 只锚尾条): current=%v err=%v", current, err)
	}
	storedTurn, _ := s.DialogueTurnByID(turn.TurnID)
	action, actionErr := s.CommunicationActionByTurn(turn.TurnID)
	if storedTurn == nil || storedTurn.Status != DialogueTurnAdviceReady ||
		actionErr != nil || action == nil || action.Status != CommunicationActionPlanned {
		t.Fatalf("尾条未变的 turn/action 不得被作废: turn=%+v action=%+v err=%v", storedTurn, action, actionErr)
	}
}

func TestFreezeDialogueTurnKeepsPreviousOutboundSeparateFromCurrentInbound(t *testing.T) {
	s := openTest(t)
	fixture := seedDialogueStoreFixture(t, s, "profile-dialogue-outbound-boundary", "text")
	req := dialogueTurnRequest(fixture, "turn-outbound-boundary", "digest-outbound-boundary")
	result, err := s.FreezeDialogueTurn(req)
	if err != nil || !result.Created || result.Turn.HistoryThroughSeq != 1 || result.Turn.InboundThroughSeq != 2 {
		t.Fatalf("轮前 outbound 与当前 inbound 边界未分离冻结: result=%+v err=%v", result, err)
	}
}

func TestActiveTrialRemainsEligibleAfterProfileStartsCommunicating(t *testing.T) {
	s := openTest(t)
	fixture := seedDialogueStoreFixture(t, s, "profile-active-communicating", "text")
	// Q5:trial 死入口 MarkProfileCommunicating 已删,直接落 greeted→communicating
	// 事实行造现场;被测对象是活的 ActiveM5TrialForAccount 读语义。
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.db.Model(&CandidateProfile{}).
		Where("profile_id = ? AND main_status = ?", fixture.ProfileID, CandidateProfileGreeted).
		Updates(map[string]any{
			"main_status": CandidateProfileCommunicating, "communicating_at": now,
			"first_real_message_seq": 2,
		}).Error; err != nil {
		t.Fatal(err)
	}
	target, err := s.ActiveM5TrialForAccount(AccountKey{Platform: fixture.Platform, AccountRef: fixture.AccountRef})
	if err != nil || target == nil || target.Profile.MainStatus != CandidateProfileCommunicating {
		t.Fatalf("既有 active 试运行不得因 communicating 状态丢失补采资格: target=%+v err=%v", target, err)
	}
}

func TestLLMRejectedClassificationIsPreservedWhenTrialTurnsManual(t *testing.T) {
	s := openTest(t)
	_, turn := seedFrozenDialogueTurn(t, s, "profile-dialogue-llm-rejected")
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-llm-rejected", TurnID: turn.TurnID, Purpose: m5ai.PurposeIntent,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-llm-rejected",
	}); err != nil {
		t.Fatal(err)
	}
	classified, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: successfulInvocationCompletion(
			"invocation-llm-rejected", time.Now().UTC().Truncate(time.Millisecond),
		),
		Label: m5ai.IntentRejected, Source: DialogueIntentLLM, ManualReason: "intentRejected",
	})
	if err != nil || classified.Status != DialogueTurnManualRequired ||
		classified.IntentLabel != m5ai.IntentRejected || classified.IntentSource != DialogueIntentLLM ||
		classified.ClassifiedAt == nil || classified.FailureReason != "intentRejected" {
		t.Fatalf("LLM rejected 分类事实必须保留后再转人工: turn=%+v err=%v", classified, err)
	}
	var invocations, actions int64
	_ = s.db.Model(&AIInvocation{}).Count(&invocations).Error
	_ = s.db.Model(&CommunicationAction{}).Count(&actions).Error
	if invocations != 1 || actions != 0 {
		t.Fatalf("LLM rejected 必须一条 intent invocation、零 action: invocations=%d actions=%d", invocations, actions)
	}
	assertTrialManualRequired(t, s, "intentRejected")
}

func assertTrialManualRequired(t *testing.T, s *Store, reason string) {
	t.Helper()
	status, err := s.M5TrialStatus()
	if err != nil || status == nil || status.Selection.Status != M5TrialSelectionManualRequired ||
		status.Selection.ActiveSlot != nil || status.Selection.Reason != reason {
		t.Fatalf("试运行未释放 active slot 并转人工: status=%+v err=%v", status, err)
	}
}

// assertTrialParkedWithoutFreeze 钉住 2026-08-02 裁决:纯计算失败族只停靠
// turn,试运行 active slot 原样保留、不连带冻结。
func assertTrialParkedWithoutFreeze(t *testing.T, s *Store) {
	t.Helper()
	status, err := s.M5TrialStatus()
	if err != nil || status == nil || status.Selection.Status != M5TrialSelectionActive ||
		status.Selection.ActiveSlot == nil {
		t.Fatalf("纯计算失败不得冻结试运行: status=%+v err=%v", status, err)
	}
}

func successfulInvocationCompletion(invocationID string, at time.Time) AIInvocationCompletion {
	zero := 0
	return AIInvocationCompletion{
		InvocationID: invocationID, Status: AIInvocationOK, OutputHash: "output-" + invocationID,
		InputTokens: 10, CachedInputTokens: 2, OutputTokens: 3, ReasoningTokens: &zero,
		UsageShape: AIInvocationUsageComplete, ReasoningContentEmpty: true,
		LatencyMs: 25, EstimatedCostMicros: 7, FinishedAt: at,
	}
}

func TestAIInvocationReservationCASAndActionPlanning(t *testing.T) {
	s := openTest(t)
	_, turn := seedFrozenDialogueTurn(t, s, "profile-dialogue-ai")
	intentReservation := ReserveAIInvocationRequest{
		InvocationID: "invocation-intent", TurnID: turn.TurnID, Purpose: m5ai.PurposeIntent,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-intent",
	}
	reserved, err := s.ReserveAIInvocation(intentReservation)
	if err != nil || !reserved.Created || reserved.Invocation.FinishedAt != nil ||
		reserved.Invocation.Status != AIInvocationTransportFailed {
		t.Fatalf("调用前预留失败: result=%+v err=%v", reserved, err)
	}
	replayedReq := intentReservation
	replayedReq.InvocationID = "invocation-intent-other-id"
	replayed, err := s.ReserveAIInvocation(replayedReq)
	if err != nil || replayed.Created || replayed.Invocation.InvocationID != reserved.Invocation.InvocationID {
		t.Fatalf("重复预留不得授权第二次调用: result=%+v err=%v", replayed, err)
	}
	intentDoneAt := time.Now().UTC().Truncate(time.Millisecond)
	intentCompletion := successfulInvocationCompletion("invocation-intent", intentDoneAt)
	classified, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: intentCompletion, Label: m5ai.IntentInterested, Source: DialogueIntentLLM,
	})
	if err != nil || classified.Status != DialogueTurnClassified || classified.IntentLabel != m5ai.IntentInterested {
		t.Fatalf("意向 invocation 未与分类同事务收束: turn=%+v err=%v", classified, err)
	}
	afterDone, err := s.ReserveAIInvocation(intentReservation)
	if err != nil || afterDone.Created || afterDone.Invocation.FinishedAt == nil {
		t.Fatalf("完成后重复预留只能收编终局: result=%+v err=%v", afterDone, err)
	}
	conflictingCompletion := intentCompletion
	conflictingCompletion.OutputHash = "different-output"
	if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: conflictingCompletion, Label: m5ai.IntentInterested, Source: DialogueIntentLLM,
	}); !errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("同 invocation 不同回包必须冲突: %v", err)
	}

	replyReservation := ReserveAIInvocationRequest{
		InvocationID: "invocation-reply", TurnID: turn.TurnID, Purpose: m5ai.PurposeReply,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-reply",
	}
	if result, err := s.ReserveAIInvocation(replyReservation); err != nil || !result.Created {
		t.Fatalf("reply 调用前预留失败: result=%+v err=%v", result, err)
	}
	replyCompletion := successfulInvocationCompletion("invocation-reply", intentDoneAt.Add(time.Second))
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: replyCompletion, ActionID: "action-reply", Text: "合成回复建议",
		ContentHash: "reply-content-hash", PlannedAt: intentDoneAt.Add(time.Second),
	})
	if err != nil || action == nil || action.Status != CommunicationActionPlanned || action.EffectIntentID != nil {
		t.Fatalf("reply invocation 未原子创建 planned action: action=%+v err=%v", action, err)
	}
	replayedAction, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: replyCompletion, ActionID: "action-reply", Text: "合成回复建议",
		ContentHash: "reply-content-hash", PlannedAt: intentDoneAt.Add(time.Second),
	})
	if err != nil || replayedAction == nil || replayedAction.ActionID != action.ActionID {
		t.Fatalf("回包重放必须复用 action: action=%+v err=%v", replayedAction, err)
	}
	finalTurn, _ := s.DialogueTurnByID(turn.TurnID)
	if finalTurn.Status != DialogueTurnAdviceReady {
		t.Fatalf("建议成功后 turn 状态错误: %+v", finalTurn)
	}
	var invocations, actions, effectIntents, commands int64
	_ = s.db.Model(&AIInvocation{}).Count(&invocations).Error
	_ = s.db.Model(&CommunicationAction{}).Count(&actions).Error
	_ = s.db.Model(&EffectIntent{}).Count(&effectIntents).Error
	_ = s.db.Model(&CmdRecord{}).Count(&commands).Error
	if invocations != 2 || actions != 1 || effectIntents != 1 || commands != 0 {
		// fixture 自带一条成功 greeting effect intent；批次 3 不得再增加
		// effect intent，更不得构造命令。
		t.Fatalf("批次 3 事实数量错误: invocations=%d actions=%d effectIntents=%d commands=%d",
			invocations, actions, effectIntents, commands)
	}
}

func TestInvocationFailuresConvergeWithoutRetriesOrDeletion(t *testing.T) {
	s := openTest(t)
	_, turn := seedFrozenDialogueTurn(t, s, "profile-dialogue-failure")
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-intent-failed", TurnID: turn.TurnID, Purpose: m5ai.PurposeIntent,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-failed",
	}); err != nil {
		t.Fatal(err)
	}
	intentFinished := time.Now().UTC().Truncate(time.Millisecond)
	classified, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: AIInvocationCompletion{
			InvocationID: "invocation-intent-failed", Status: AIInvocationTransportFailed,
			LatencyMs: 30, ErrorClass: "timeout", FinishedAt: intentFinished,
		},
		Label: m5ai.IntentNeutral, Source: DialogueIntentLLMFailure,
	})
	if err != nil || classified.Status != DialogueTurnClassified || classified.IntentLabel != m5ai.IntentNeutral ||
		classified.IntentSource != DialogueIntentLLMFailure {
		t.Fatalf("意向失败未一次落 neutral fallback: turn=%+v err=%v", classified, err)
	}
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-reply-invalid", TurnID: turn.TurnID, Purpose: m5ai.PurposeReply,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "reply-invalid",
	}); err != nil {
		t.Fatal(err)
	}
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: AIInvocationCompletion{
			InvocationID: "invocation-reply-invalid", Status: AIInvocationInvalidOutput,
			OutputHash: "invalid-output-hash", LatencyMs: 20, ErrorClass: "invalidJSON",
			FinishedAt: intentFinished.Add(time.Second),
		},
	})
	if err != nil || action != nil {
		t.Fatalf("非法回复不得创建 action: action=%+v err=%v", action, err)
	}
	finalTurn, _ := s.DialogueTurnByID(turn.TurnID)
	if finalTurn.Status != DialogueTurnManualRequired {
		t.Fatalf("回复失败必须转人工: %+v", finalTurn)
	}
	var invocations, actions int64
	_ = s.db.Model(&AIInvocation{}).Count(&invocations).Error
	_ = s.db.Model(&CommunicationAction{}).Count(&actions).Error
	if invocations != 2 || actions != 0 {
		t.Fatalf("失败事实必须保留且不得增生动作: invocations=%d actions=%d", invocations, actions)
	}
	assertTrialManualRequired(t, s, "replyinvalidOutput")
}

func TestInvocationCompletionWithoutReservationFailsClosed(t *testing.T) {
	s := openTest(t)
	_, turn := seedFrozenDialogueTurn(t, s, "profile-dialogue-no-reservation")
	completion := successfulInvocationCompletion("missing-invocation", time.Now().UTC().Truncate(time.Millisecond))
	if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: completion, Label: m5ai.IntentInterested, Source: DialogueIntentLLM,
	}); !errors.Is(err, ErrAIInvocationNotFound) {
		t.Fatalf("未预留不得收编 provider 回包: %v", err)
	}
	stored, _ := s.DialogueTurnByID(turn.TurnID)
	if stored.Status != DialogueTurnCollected {
		t.Fatalf("失败收编不得推进 turn: %+v", stored)
	}
}

func TestIntentCompletionRechecksBoundaryAndPreservesInvocation(t *testing.T) {
	s := openTest(t)
	fixture, turn := seedFrozenDialogueTurn(t, s, "profile-dialogue-boundary-after-call")
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-boundary", TurnID: turn.TurnID, Purpose: m5ai.PurposeIntent,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-boundary",
	}); err != nil {
		t.Fatal(err)
	}
	newText := "调用期间到达的合成消息"
	if err := s.db.Create(&Message{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef, ConversationRef: fixture.ConversationRef,
		Seq: 3, Direction: "in", Kind: "text", ContentHash: "boundary-changed-hash",
		Text: &newText, Origin: "external",
	}).Error; err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC().Truncate(time.Millisecond)
	result, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: successfulInvocationCompletion("invocation-boundary", completedAt),
		Label:      m5ai.IntentInterested, Source: DialogueIntentLLM,
	})
	// 2026-08-02 裁决:边界变化收 invocation 事实后作废旧轮,不再冻结候选人。
	if err != nil || result.Status != DialogueTurnSuperseded || result.FailureReason != "boundarySuperseded" {
		t.Fatalf("输入变化后必须收 invocation 并作废旧轮: turn=%+v err=%v", result, err)
	}
	invocations, err := s.AIInvocationsForTurn(turn.TurnID)
	if err != nil || len(invocations) != 1 || invocations[0].FinishedAt == nil || invocations[0].Status != AIInvocationOK {
		t.Fatalf("边界变化不得回滚已发生的 provider 事实: invocations=%+v err=%v", invocations, err)
	}
	assertTrialParkedWithoutFreeze(t, s)
}

// 2026-08-16 甲方裁决开启思考模式后,reasoning_content 非空是预期形态:intent
// 必须照常落定分类,不再阻断。本用例替代旧的
// TestIntentNonemptyReasoningContentTurnsManual。
func TestIntentNonemptyReasoningContentStillClassifies(t *testing.T) {
	s := openTest(t)
	_, turn := seedFrozenDialogueTurn(t, s, "profile-dialogue-reasoning")
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-reasoning", TurnID: turn.TurnID, Purpose: m5ai.PurposeIntent,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-reasoning",
	}); err != nil {
		t.Fatal(err)
	}
	completion := successfulInvocationCompletion("invocation-reasoning", time.Now().UTC().Truncate(time.Millisecond))
	some := 9
	completion.UsageShape = AIInvocationUsageComplete
	completion.ReasoningTokens = &some
	completion.ReasoningContentEmpty = false
	result, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: completion, Label: m5ai.IntentInterested, Source: DialogueIntentLLM,
	})
	if err != nil || result.Status == DialogueTurnManualRequired ||
		result.FailureReason == "reasoningUsageUnsafe" ||
		result.IntentLabel != m5ai.IntentInterested || result.IntentSource != DialogueIntentLLM {
		t.Fatalf("思考模式下 intent 须照常落定分类: turn=%+v err=%v", result, err)
	}
}

func TestFailAIInvocationForRetryKeepsTurnWaitingAndOpensNextAttempt(t *testing.T) {
	s := openTest(t)
	_, turn := seedFrozenDialogueTurn(t, s, "profile-retry-midchain")
	reserve := func(attempt int) (*ReserveAIInvocationResult, error) {
		return s.ReserveAIInvocation(ReserveAIInvocationRequest{
			InvocationID: fmt.Sprintf("invocation-retry-midchain-%d", attempt),
			TurnID:       turn.TurnID, Purpose: m5ai.PurposeIntent, Attempt: attempt,
			Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-retry-midchain",
		})
	}
	first, err := reserve(1)
	if err != nil || !first.Created {
		t.Fatalf("首次预留失败: first=%+v err=%v", first, err)
	}
	if err := s.FailAIInvocationForRetry(AIInvocationCompletion{
		InvocationID: first.Invocation.InvocationID, Status: AIInvocationProviderRejected,
		ErrorClass: "rateLimited", FinishedAt: time.Now().UTC().Truncate(time.Millisecond),
	}, m5ai.PurposeIntent); err != nil {
		t.Fatalf("落中间失败事实: %v", err)
	}

	stored, err := s.DialogueTurnByID(turn.TurnID)
	if err != nil || stored == nil || stored.Status != DialogueTurnCollected || stored.FailureReason != "" {
		t.Fatalf("中间失败不得推进 turn: turn=%+v err=%v", stored, err)
	}
	again, err := reserve(1)
	if err != nil || again.Created || again.Invocation.FinishedAt == nil {
		t.Fatalf("同一 attempt 不得再次授权调用: again=%+v err=%v", again, err)
	}
	next, err := reserve(2)
	if err != nil || !next.Created {
		t.Fatalf("下一个 attempt 必须能接着开: next=%+v err=%v", next, err)
	}
	if _, err := reserve(MaxAIInvocationAttempts + 1); !errors.Is(err, ErrAIInvocationInvalid) {
		t.Fatalf("超出上限的 attempt 必须被拒: err=%v", err)
	}
}

func TestInterruptedInvocationRecoveryNeverRecallsProvider(t *testing.T) {
	t.Run("intent neutral fallback", func(t *testing.T) {
		s := openTest(t)
		fixture, turn := seedFrozenDialogueTurn(t, s, "profile-dialogue-recover-intent")
		if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
			InvocationID: "invocation-recover-intent", TurnID: turn.TurnID, Purpose: m5ai.PurposeIntent,
			Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-recover-intent",
		}); err != nil {
			t.Fatal(err)
		}
		recovered, err := s.RecoverInterruptedAIInvocations(time.Now().UTC().Truncate(time.Millisecond))
		if err != nil || recovered != 1 {
			t.Fatalf("intent 遗留预留恢复失败: recovered=%d err=%v", recovered, err)
		}
		stored, _ := s.DialogueTurnByID(turn.TurnID)
		if stored.Status != DialogueTurnClassified || stored.IntentLabel != m5ai.IntentNeutral ||
			stored.IntentSource != DialogueIntentLLMFailure {
			t.Fatalf("intent 中断未落 neutral fallback: %+v", stored)
		}
		invocations, _ := s.AIInvocationsForTurn(turn.TurnID)
		if len(invocations) != 1 || invocations[0].ErrorClass != "processInterrupted" || invocations[0].FinishedAt == nil {
			t.Fatalf("intent 中断 invocation 未终局化: %+v", invocations)
		}
		latest, err := s.LatestDialogueTurnForProfile(fixture.ProfileID)
		if err != nil || latest == nil || latest.TurnID != turn.TurnID {
			t.Fatalf("latest turn 读口错误: latest=%+v err=%v", latest, err)
		}
		snapshot, err := s.CandidateResumeSnapshotByID(fixture.ProfileID, fixture.SnapshotID)
		if err != nil || snapshot == nil || snapshot.ContentHash == "" {
			t.Fatalf("snapshot material 读口错误: snapshot=%+v err=%v", snapshot, err)
		}
	})

	t.Run("historical binding change does not invalidate frozen turn", func(t *testing.T) {
		s := openTest(t)
		fixture, turn := seedFrozenDialogueTurn(t, s, "profile-dialogue-recover-binding")
		if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
			InvocationID: "invocation-recover-binding", TurnID: turn.TurnID, Purpose: m5ai.PurposeIntent,
			Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-recover-binding",
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&ProfileAIContextBinding{}).
			Where("profile_id = ? AND status = ?", fixture.ProfileID, ProfileAIContextBindingActive).
			Updates(map[string]any{
				"status": ProfileAIContextBindingSuperseded, "reason": "fixtureChanged",
			}).Error; err != nil {
			t.Fatal(err)
		}
		recovered, err := s.RecoverInterruptedAIInvocations(time.Now().UTC().Truncate(time.Millisecond))
		if err != nil || recovered != 1 {
			t.Fatalf("审计绑定变化后的 intent 遗留恢复失败: recovered=%d err=%v", recovered, err)
		}
		stored, _ := s.DialogueTurnByID(turn.TurnID)
		if stored == nil || stored.Status != DialogueTurnClassified ||
			stored.FailureReason != "" || stored.IntentLabel != m5ai.IntentNeutral ||
			stored.IntentSource != DialogueIntentLLMFailure || stored.ClassifiedAt == nil {
			t.Fatalf("冻结 revision 应继续收敛 neutral fallback: %+v", stored)
		}
		invocations, _ := s.AIInvocationsForTurn(turn.TurnID)
		if len(invocations) != 1 || invocations[0].FinishedAt == nil ||
			invocations[0].ErrorClass != "processInterrupted" {
			t.Fatalf("中断 invocation 未终局化: %+v", invocations)
		}
	})

	t.Run("reply manual", func(t *testing.T) {
		s := openTest(t)
		_, turn := seedFrozenDialogueTurn(t, s, "profile-dialogue-recover-reply")
		// Q5:ApplyCodeClassification 已随死代码删除,直接落 classified 事实行
		// 造现场;被测对象是活的 RecoverInterruptedAIInvocations 收敛语义。
		if err := s.db.Model(&DialogueTurn{}).
			Where("turn_id = ? AND status = ?", turn.TurnID, DialogueTurnCollected).
			Updates(map[string]any{
				"status": DialogueTurnClassified, "intent_label": m5ai.IntentInterested,
				"intent_source": DialogueIntentCodeShortCircuit, "classified_at": time.Now(),
			}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
			InvocationID: "invocation-recover-reply", TurnID: turn.TurnID, Purpose: m5ai.PurposeReply,
			Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-recover-reply",
		}); err != nil {
			t.Fatal(err)
		}
		recovered, err := s.RecoverInterruptedAIInvocations(time.Now().UTC().Truncate(time.Millisecond))
		if err != nil || recovered != 1 {
			t.Fatalf("reply 遗留预留恢复失败: recovered=%d err=%v", recovered, err)
		}
		stored, _ := s.DialogueTurnByID(turn.TurnID)
		if stored.Status != DialogueTurnManualRequired || stored.FailureReason != "replyProcessInterrupted" {
			t.Fatalf("reply 中断未转人工: %+v", stored)
		}
		// 2026-08-02 裁决:崩溃收束是纯计算失败,turn 停靠但候选人不冻结。
		assertTrialParkedWithoutFreeze(t, s)
		var actions int64
		if err := s.db.Model(&CommunicationAction{}).Count(&actions).Error; err != nil || actions != 0 {
			t.Fatalf("reply 中断不得创建 action: count=%d err=%v", actions, err)
		}
	})
}

// 预留比对只认编号原料(2026-08-03 甲方裁决)。跨轮重试每次都从 attempt=1
// 重新登记、逐个路过已完成的行,而 provider/model/输入指纹会在两轮之间自己
// 变(换 model、微信线推进改写【本轮可选动作】块)。旧实现把它们也纳入比对,
// 一变就永远对不上,于是每轮在同一处报冲突、同一处跳过,静默把这一轮永久
// 卡死。两条路径都不取既有行的结果,比对防不住任何误用,只剩误伤。
func TestAIInvocationReservationIgnoresCallShapeButGuardsIDCollision(t *testing.T) {
	s := openTest(t)
	_, turn := seedFrozenDialogueTurn(t, s, "profile-dialogue-shape")
	first := ReserveAIInvocationRequest{
		InvocationID: "invocation-shape", TurnID: turn.TurnID, Purpose: m5ai.PurposeIntent,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-before",
	}
	reserved, err := s.ReserveAIInvocation(first)
	if err != nil || !reserved.Created {
		t.Fatalf("首次预留失败: result=%+v err=%v", reserved, err)
	}

	// 换 model + 微信线推进改写了块 => provider/model/指纹三项全变,同一个
	// attempt 号重新登记必须照常收编既有行,不得报冲突。
	reshaped := first
	reshaped.Provider = "openai"
	reshaped.Model = "gpt-4o-mini"
	reshaped.InputHash = "input-after-wechat-exchanged"
	replayed, err := s.ReserveAIInvocation(reshaped)
	if err != nil {
		t.Fatalf("调用形态变化不得报冲突: err=%v", err)
	}
	if replayed.Created {
		t.Fatalf("既有 attempt 行必须收编而不是新建: %+v", replayed.Invocation)
	}
	if replayed.Invocation.InputHash != "input-before" ||
		replayed.Invocation.Model != "deepseek-v4-pro" {
		t.Fatalf("收编的必须是既有事实原样,不得被新形态改写: %+v", replayed.Invocation)
	}

	// 编号三项仍是硬闸:同一个 invocationID 落到不同用途,只可能是 ID 生成
	// 撞车,继续走会把两件事记在一个编号底下。
	collided := first
	collided.Purpose = m5ai.PurposeReply
	if _, err := s.ReserveAIInvocation(collided); !errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("编号原料对不上必须报冲突: %v", err)
	}
}
