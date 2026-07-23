package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"

	"gorm.io/gorm"
)

func appendCommunicationV4Inbound(
	t *testing.T,
	s *Store,
	fixture resumeStoreFixture,
	messages ...Message,
) []Message {
	t.Helper()
	for index := range messages {
		messages[index].Platform = fixture.Platform
		messages[index].AccountRef = fixture.AccountRef
		messages[index].ConversationRef = fixture.ConversationRef
		if messages[index].Origin == "" {
			messages[index].Origin = "external"
		}
	}
	if err := s.db.Create(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Conversation{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ?",
			fixture.Platform,
			fixture.AccountRef,
			fixture.ConversationRef,
		).
		Update("last_message_seq", messages[len(messages)-1].Seq).Error; err != nil {
		t.Fatal(err)
	}
	return messages
}

func communicationV4TurnRequest(
	t *testing.T,
	s *Store,
	fixture resumeStoreFixture,
	inbound []Message,
) FreezeDialogueTurnRequest {
	t.Helper()
	var greeting Message
	if err := s.db.First(
		&greeting,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = 1",
		fixture.Platform,
		fixture.AccountRef,
		fixture.ConversationRef,
	).Error; err != nil {
		t.Fatal(err)
	}
	digest, turnID, err := DialogueTurnIdentity(fixture.ProfileID, greeting, inbound)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := s.CommunicationTargetsForAccount(AccountKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	})
	if err != nil || len(targets) != 1 {
		t.Fatalf("构造 V4 turn 前目标未就绪: targets=%+v err=%v", targets, err)
	}
	return FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: fixture.ProfileID, ConversationRef: fixture.ConversationRef,
		InputDigest: digest, HistoryThroughSeq: greeting.Seq,
		InboundFromSeq: inbound[0].Seq, InboundThroughSeq: inbound[len(inbound)-1].Seq,
		ContextRevisionHash: targets[0].ContextRevision.RevisionHash,
		ResumeSnapshotID:    targets[0].ResumeSnapshot.SnapshotID,
		RecommendedTimeText: "合成推荐时段",
		RenderFormatVersion: m5ai.DialogueRenderFormatVersion,
		FrozenAt:            time.Now().UTC().Truncate(time.Millisecond),
	}
}

func TestFreezeCommunicationV4TurnPersistsAggregateAndTurnWithoutTrial(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-text")
	firstText, secondText := "合成第一句", "合成第二句"
	inbound := appendCommunicationV4Inbound(t, s, fixture,
		Message{
			Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-turn-text-2",
			Text: &firstText, CreatedAt: time.Now().Add(-time.Second),
		},
		Message{
			Seq: 3, Direction: "in", Kind: "text", ContentHash: "v4-turn-text-3",
			Text: &secondText, CreatedAt: time.Now(),
		},
	)
	req := communicationV4TurnRequest(t, s, fixture, inbound)

	result, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || !result.Created ||
		result.Turn.Status != DialogueTurnCollected ||
		result.Aggregate.Revision != 1 ||
		result.Aggregate.ProjectedThroughSeq != 3 ||
		result.Aggregate.State.MainStatus != communication.V4StatusCommunicating ||
		result.Application.InputKind != CommunicationV4InputDialogueTurn ||
		result.Application.Outcome.DialogueStatus != communication.V4DialogueWaitingAdvice ||
		result.Application.Outcome.NextAdvice != communication.V4AdviceIntent {
		t.Fatalf("V4 普通轮未原子冻结: result=%+v err=%v", result, err)
	}
	profile, err := s.CandidateProfileByID(fixture.ProfileID)
	if err != nil || profile.MainStatus != CandidateProfileCommunicating ||
		profile.FirstRealMessageSeq == nil || *profile.FirstRealMessageSeq != 2 ||
		profile.CommunicatingAt == nil {
		t.Fatalf("V4 profile 镜像或首条事实错误: profile=%+v err=%v", profile, err)
	}
	var activeTrials int64
	if err := s.db.Model(&M5TrialSelection{}).
		Where("status = ? AND active_slot = ?", M5TrialSelectionActive, m5TrialActiveSlot).
		Count(&activeTrials).Error; err != nil || activeTrials != 0 {
		t.Fatalf("V4 冻结不得依赖或重建试运行槽: count=%d err=%v", activeTrials, err)
	}

	replayed, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || replayed.Created ||
		replayed.Turn.TurnID != result.Turn.TurnID ||
		replayed.Aggregate.Revision != result.Aggregate.Revision {
		t.Fatalf("V4 turn 重放不幂等: replayed=%+v err=%v", replayed, err)
	}
	var turns, applications int64
	_ = s.db.Model(&DialogueTurn{}).Where("profile_id = ?", fixture.ProfileID).Count(&turns).Error
	_ = s.db.Model(&CommunicationV4ProjectionApplication{}).
		Where("profile_id = ?", fixture.ProfileID).Count(&applications).Error
	if turns != 1 || applications != 1 {
		t.Fatalf("V4 turn 重放增生: turns=%d applications=%d", turns, applications)
	}
}

func TestFreezeCommunicationV4ResumeTurnSkipsIntentAdvice(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-resume")
	cardType := "resumeAttachment"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "card", CardType: cardType, CardState: "unknown",
		ContentHash: "v4-turn-resume-2",
	})
	result, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil || result.Turn.Status != DialogueTurnClassified ||
		result.Turn.IntentLabel != m5ai.IntentInterested ||
		result.Turn.IntentSource != DialogueIntentBusinessEvent ||
		result.Application.Outcome.NextAdvice != communication.V4AdviceReply {
		t.Fatalf("投简历强意向未直接进入回复建议: result=%+v err=%v", result, err)
	}
}

func TestFreezeCommunicationV4UnsupportedMediaStopsOnlyProfile(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-media")
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "image", ContentHash: "v4-turn-image-2",
	})
	result, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil || result.Turn.Status != DialogueTurnManualRequired ||
		result.Turn.FailureReason != string(communication.V4ManualUnsupportedMedia) ||
		result.Aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		result.Aggregate.ManualReason != string(communication.V4ManualUnsupportedMedia) {
		t.Fatalf("不支持媒体未局部转人工: result=%+v err=%v", result, err)
	}
	targets, err := s.CommunicationTargetsForAccount(AccountKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	})
	if err != nil || len(targets) != 0 {
		t.Fatalf("转人工档案仍进入自动目标: targets=%+v err=%v", targets, err)
	}
}

func TestCommunicationV4TurnAIAdviceDoesNotNeedTrialSlot(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-ai")
	text := "合成普通回复"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-turn-ai-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	intentInvocationID := "invocation-v4-intent"
	reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: intentInvocationID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-v4-intent",
		CreatedAt: time.Now(),
	})
	if err != nil || !reserved.Created {
		t.Fatalf("V4 意向调用未获 trialless 授权: result=%+v err=%v", reserved, err)
	}
	completion := successfulInvocationCompletion(
		intentInvocationID,
		time.Now().UTC().Truncate(time.Millisecond),
	)
	classified, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: completion, Label: m5ai.IntentInterested, Source: DialogueIntentLLM,
	})
	if err != nil || classified.Status != DialogueTurnClassified {
		t.Fatalf("V4 意向结果未收编: turn=%+v err=%v", classified, err)
	}
	reply, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-v4-reply", TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-v4-reply",
		CreatedAt: time.Now(),
	})
	if err != nil || !reply.Created {
		t.Fatalf("V4 回复调用未获 trialless 授权: result=%+v err=%v", reply, err)
	}
}

func TestCommunicationV4TurnManualClosesOnlyAggregate(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-manual")
	text := "合成待人工消息"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-turn-manual-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.MarkDialogueTurnManualRequired(
		frozen.Turn.TurnID, "fixtureManual", at,
	); err != nil {
		t.Fatal(err)
	}
	turn, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if turn == nil || turn.Status != DialogueTurnManualRequired ||
		turn.FailureReason != "fixtureManual" ||
		aggregateErr != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != "fixtureManual" {
		t.Fatalf("V4 人工收敛没有局部闭合: turn=%+v aggregate=%+v err=%v",
			turn, aggregate, aggregateErr)
	}
	var activeTrials int64
	if err := s.db.Model(&M5TrialSelection{}).
		Where("status = ? AND active_slot = ?", M5TrialSelectionActive, m5TrialActiveSlot).
		Count(&activeTrials).Error; err != nil || activeTrials != 0 {
		t.Fatalf("V4 人工收敛不应铸造 trial: count=%d err=%v", activeTrials, err)
	}
}

func TestCommunicationV4WaitingPrerequisiteCannotReserveAI(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-prerequisite")
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
		CardState: "pending", ContentHash: "v4-turn-prerequisite-2",
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil ||
		frozen.Application.Outcome.DialogueStatus != communication.V4DialogueWaitingPrerequisite ||
		frozen.Application.Outcome.NextAdvice != communication.V4AdviceNone {
		t.Fatalf("主动换微信没有冻结为前置动作等待: frozen=%+v err=%v", frozen, err)
	}
	result, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-v4-prerequisite", TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-v4-prerequisite",
		CreatedAt: time.Now(),
	})
	if result != nil || !errors.Is(err, ErrDialogueTurnState) {
		t.Fatalf("前置动作等待轮不得获得 AI 权限: result=%+v err=%v", result, err)
	}
	var invocations int64
	if err := s.db.Model(&AIInvocation{}).
		Where("turn_id = ?", frozen.Turn.TurnID).
		Count(&invocations).Error; err != nil || invocations != 0 {
		t.Fatalf("错误授权不得留下 invocation: count=%d err=%v", invocations, err)
	}
}

func TestCommunicationV4AIAdviceRejectsAdvancedRevisionAtSameMessageBoundary(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-stale")
	text := "合成普通回复"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-turn-stale-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	advancedAt := time.Now().UTC().Truncate(time.Millisecond)
	err = s.db.Transaction(func(tx *gorm.DB) error {
		_, _, _, err := applyCommunicationV4ConfirmedActionTx(
			tx,
			fixture.ProfileID,
			communication.V4ConfirmedAction{
				ActionKey:  "fixture-concurrent-invite-wechat",
				Kind:       communication.V4ActionInviteWechat,
				MessageSeq: 3,
				SentAt:     &advancedAt,
			},
			advancedAt,
		)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || before.Revision != frozen.Application.ToRevision+1 ||
		before.ProjectedThroughSeq != frozen.Turn.InboundThroughSeq {
		t.Fatalf("没有构造出同消息边界的 revision 前进: aggregate=%+v err=%v", before, err)
	}
	result, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-v4-stale", TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-v4-stale",
		CreatedAt: advancedAt.Add(time.Second),
	})
	if result != nil || !errors.Is(err, ErrDialogueTurnBinding) {
		t.Fatalf("旧 revision 建议边界必须失效: result=%+v err=%v", result, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != "inputBoundaryChanged" {
		t.Fatalf("旧 revision 没有局部转人工: aggregate=%+v err=%v", aggregate, err)
	}
}
