package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"

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
	material, materialReady, err := s.CommunicationAIMaterialForProfile(fixture.ProfileID)
	if err != nil || !materialReady {
		t.Fatalf("构造 V4 turn 前 AI 材料未就绪: material=%+v ready=%v err=%v",
			material, materialReady, err)
	}
	return FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: fixture.ProfileID, ConversationRef: fixture.ConversationRef,
		InputDigest: digest, HistoryThroughSeq: greeting.Seq,
		InboundFromSeq: inbound[0].Seq, InboundThroughSeq: inbound[len(inbound)-1].Seq,
		ContextRevisionHash: material.ContextRevision.RevisionHash,
		ResumeSnapshotID:    material.ResumeSnapshot.SnapshotID,
		RecommendedTimeText: "合成推荐时段",
		RenderFormatVersion: m5ai.DialogueRenderFormatVersion,
		FrozenAt:            time.Now().UTC().Truncate(time.Millisecond),
	}
}

func setCommunicationV4FixedPhrasePackage(
	t *testing.T,
	s *Store,
	revisionHash string,
) {
	t.Helper()
	var revision JobAIContextRevision
	if err := s.db.First(&revision, "revision_hash = ?", revisionHash).Error; err != nil {
		t.Fatal(err)
	}
	revision.SourcePackage.Documents = append(
		revision.SourcePackage.Documents,
		m5ai.JobConfigDocument{
			DocType: "固定话术",
			Content: `{
				"rejectWechat":{
					"message":"合成挽留",
					"messages":["合成挽留"],
					"actions":[],
					"enabled":true
				}
			}`,
		},
	)
	body, err := json.Marshal(revision.SourcePackage)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&JobAIContextRevision{}).
		Where("revision_hash = ?", revisionHash).
		UpdateColumn("source_package", string(body)).Error; err != nil {
		t.Fatal(err)
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
		result.Application.Outcome.Dialogue != communication.V4DialogueClassifyAndReply ||
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

func TestFreezeCommunicationV4TurnReusesOnDemandAIMaterialValidation(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-material-recheck")
	text := "合成材料重验消息"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: "v4-turn-material-recheck-2", Text: &text,
	})
	req := communicationV4TurnRequest(t, s, fixture, inbound)
	if err := s.db.Model(&ProfileAIContextBinding{}).
		Where("profile_id = ? AND status = ?", fixture.ProfileID, ProfileAIContextBindingActive).
		Update("context_id", "fixture-mismatched-context").Error; err != nil {
		t.Fatal(err)
	}
	result, err := s.FreezeCommunicationV4Turn(req)
	if result != nil || !errors.Is(err, ErrCommunicationTargetConflict) {
		t.Fatalf("Freeze 必须复用按需材料冲突判定: result=%+v err=%v", result, err)
	}
	var turns, applications int64
	if err := s.db.Model(&DialogueTurn{}).
		Where("profile_id = ?", fixture.ProfileID).
		Count(&turns).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
		Where("profile_id = ?", fixture.ProfileID).
		Count(&applications).Error; err != nil {
		t.Fatal(err)
	}
	if turns != 0 || applications != 0 {
		t.Fatalf("材料冲突不得留下半成品: turns=%d applications=%d", turns, applications)
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
	replyText := "工作地点在上海，方便继续聊聊吗？"
	replyCompletion := successfulInvocationCompletion(
		reply.Invocation.InvocationID,
		time.Now().UTC().Truncate(time.Millisecond),
	)
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: replyCompletion, ActionID: "caller-action-id-is-not-authoritative",
		Text: replyText, ContentHash: textcanon.Hash(replyText),
		PlannedAt: replyCompletion.FinishedAt,
	})
	if err != nil || action == nil ||
		action.ActionID != frozen.Turn.TurnID+"|replyText" ||
		action.Status != CommunicationActionPlanned ||
		action.Text != replyText {
		t.Fatalf("V4 回复建议未原子形成 reducer 动作: action=%+v err=%v", action, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.Revision != 3 {
		t.Fatalf("V4 intent/reply continuation 没有逐次推进 revision: aggregate=%+v err=%v", aggregate, err)
	}
	var applications int64
	if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
		Where("profile_id = ?", fixture.ProfileID).
		Count(&applications).Error; err != nil || applications != 3 {
		t.Fatalf("V4 continuation 回执数错误: count=%d err=%v", applications, err)
	}
	replayed, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: replyCompletion, ActionID: "caller-action-id-is-not-authoritative",
		Text: replyText, ContentHash: textcanon.Hash(replyText),
		PlannedAt: replyCompletion.FinishedAt,
	})
	if err != nil || replayed == nil || replayed.ActionID != action.ActionID {
		t.Fatalf("V4 回复完成重放不幂等: action=%+v err=%v", replayed, err)
	}
	aggregate, _ = s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if aggregate.Revision != 3 {
		t.Fatalf("V4 回复完成重放推进了 revision: %+v", aggregate)
	}
	var rawOutcome string
	if err := s.db.Raw(
		"SELECT CAST(outcome AS TEXT) FROM communication_v4_projection_applications "+
			"WHERE profile_id = ? AND input_kind = ? AND input_key = ?",
		fixture.ProfileID,
		CommunicationV4InputDialogueAdvice,
		communicationV4DialogueAdviceKey(frozen.Turn.TurnID, m5ai.PurposeReply),
	).Scan(&rawOutcome).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rawOutcome, replyText) {
		t.Fatalf("不可变 continuation 不得复制模型正文: %s", rawOutcome)
	}
}

func TestCommunicationV4ArchiveSupersedesAdviceReadyBeforeEffect(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-archive-advice")
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "card", CardType: "resumeAttachment",
		CardState: "unknown", ContentHash: "v4-archive-advice-2",
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil || frozen.Turn.Status != DialogueTurnClassified {
		t.Fatalf("投简历轮没有进入待回复状态: frozen=%+v err=%v", frozen, err)
	}
	invocationID := "invocation-v4-archive-advice"
	reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: invocationID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-v4-archive-advice",
		CreatedAt: time.Now(),
	})
	if err != nil || !reserved.Created {
		t.Fatalf("回复调用未获授权: result=%+v err=%v", reserved, err)
	}
	replyText := "合成的未发送回复"
	completion := successfulInvocationCompletion(
		invocationID,
		time.Now().UTC().Truncate(time.Millisecond),
	)
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: completion, ActionID: "caller-action-id-is-not-authoritative",
		Text: replyText, ContentHash: textcanon.Hash(replyText),
		PlannedAt: completion.FinishedAt,
	})
	if err != nil || action == nil || action.Status != CommunicationActionPlanned ||
		action.EffectIntentID != nil {
		t.Fatalf("未发送回复动作没有停在 planned: action=%+v err=%v", action, err)
	}
	beforeArchive, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	archiveAction := communication.V4PlannedAction{
		ActionKey: fixture.ProfileID + "|fixture|archive-before-effect",
		Kind:      communication.V4ActionArchive,
		EndReason: communication.V4EndFallback,
	}
	archived, applied, err := s.ApplyCommunicationV4ArchiveAction(
		fixture.ProfileID,
		beforeArchive.Revision,
		archiveAction,
		time.Now().Add(8*24*time.Hour),
	)
	if err != nil || !applied ||
		archived.State.MainStatus != communication.V4StatusEnded ||
		archived.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("归档没有保持档案可唤醒: aggregate=%+v applied=%v err=%v",
			archived, applied, err)
	}
	turn, turnErr := s.DialogueTurnByID(frozen.Turn.TurnID)
	storedAction, actionErr := s.CommunicationActionByTurn(frozen.Turn.TurnID)
	if turnErr != nil || turn == nil || turn.Status != DialogueTurnSuperseded ||
		turn.FailureReason != communicationV4ArchiveSuperseded ||
		actionErr != nil || storedAction == nil ||
		storedAction.Status != CommunicationActionSuperseded ||
		storedAction.FailureReason != communicationV4ArchiveSuperseded ||
		storedAction.EffectIntentID != nil {
		t.Fatalf("归档未原子作废 pre-effect 轮与动作: turn=%+v turnErr=%v action=%+v actionErr=%v",
			turn, turnErr, storedAction, actionErr)
	}
	current, err := s.RecheckDialogueTurnCurrent(frozen.Turn.TurnID, time.Now())
	if err != nil || current {
		t.Fatalf("已作废旧轮不得再转人工或恢复执行: current=%v err=%v", current, err)
	}

	replayed, applied, err := s.ApplyCommunicationV4ArchiveAction(
		fixture.ProfileID,
		beforeArchive.Revision,
		archiveAction,
		time.Now().Add(9*24*time.Hour),
	)
	if err != nil || applied || replayed.Revision != archived.Revision {
		t.Fatalf("归档重放发生增生: aggregate=%+v applied=%v err=%v",
			replayed, applied, err)
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
				ActionKey: "fixture-concurrent-notification",
				Kind:      communication.V4ActionNotifyWechat,
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

func TestCommunicationV4RejectedIntentPlansTextBeforeWechatCard(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-rejected")
	setCommunicationV4FixedPhrasePackage(
		t,
		s,
		"revision-profile-v4-turn-rejected",
	)
	text := "合成拒绝消息"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-turn-rejected-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-v4-rejected", TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-v4-rejected",
		CreatedAt: time.Now(),
	})
	if err != nil || !reserved.Created {
		t.Fatalf("拒绝意向调用未预留: result=%+v err=%v", reserved, err)
	}
	completion := successfulInvocationCompletion(
		reserved.Invocation.InvocationID,
		time.Now().UTC().Truncate(time.Millisecond),
	)
	turn, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: completion, Label: m5ai.IntentRejected,
		Source: DialogueIntentLLM, ManualReason: "intentRejected",
	})
	if err != nil || turn.Status != DialogueTurnAdviceReady ||
		turn.IntentLabel != m5ai.IntentRejected ||
		turn.FailureReason != "" {
		t.Fatalf("拒绝组合没有进入正文待发: turn=%+v err=%v", turn, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" ||
		aggregate.State.ColdPromptRemaining != 0 ||
		aggregate.State.ColdWechatRemaining != 0 ||
		aggregate.State.RejectionStage != communication.V4RejectionStageRetention {
		t.Fatalf("拒绝业务状态未原子推进: aggregate=%+v err=%v", aggregate, err)
	}
	actions, err := s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 1 ||
		actions[0].Kind != CommunicationActionReplyText ||
		actions[0].Status != CommunicationActionPlanned ||
		actions[0].DependsOnActionID != nil {
		t.Fatalf("正文正证前只能实体化第一动作: actions=%+v err=%v", actions, err)
	}
	var advice CommunicationV4ProjectionApplication
	if err := s.db.First(
		&advice,
		"profile_id = ? AND input_kind = ? AND input_key = ?",
		fixture.ProfileID,
		CommunicationV4InputDialogueAdvice,
		communicationV4DialogueAdviceKey(frozen.Turn.TurnID, m5ai.PurposeIntent),
	).Error; err != nil {
		t.Fatal(err)
	}
	if len(advice.Outcome.PlannedActions) != 2 ||
		advice.Outcome.PlannedActions[0].Text != "" ||
		advice.Outcome.PlannedActions[1].Text != "" {
		t.Fatalf("拒绝计划元数据未保留或泄露正文: %+v", advice.Outcome)
	}
}

func TestCommunicationV4RejectionShortCircuitPlansTextBeforeWechatCardAtFreeze(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-short-rejected")
	setCommunicationV4FixedPhrasePackage(
		t,
		s,
		"revision-profile-v4-short-rejected",
	)
	text := "暂时不考虑，谢谢"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: "v4-short-rejected-2", Text: &text,
	})
	req := communicationV4TurnRequest(t, s, fixture, inbound)
	frozen, err := s.FreezeCommunicationV4Turn(req)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Turn.Status != DialogueTurnAdviceReady ||
		frozen.Turn.IntentLabel != m5ai.IntentRejected ||
		frozen.Turn.IntentSource != DialogueIntentCodeShortCircuit ||
		frozen.Turn.FailureReason != "" {
		t.Fatalf("拒绝短路未在冻结事务内规划正文: %+v", frozen.Turn)
	}
	if frozen.Aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		frozen.Aggregate.ManualReason != "" ||
		frozen.Aggregate.State.RejectionStage != communication.V4RejectionStageRetention ||
		frozen.Aggregate.State.ColdPromptRemaining != 0 ||
		frozen.Aggregate.State.ColdWechatRemaining != 0 {
		t.Fatalf("拒绝短路状态错误: %+v", frozen.Aggregate)
	}
	if frozen.Application.Outcome.DialogueStatus != communication.V4DialogueActionsPlanned ||
		frozen.Application.Outcome.ManualReason != "" ||
		len(frozen.Application.Outcome.PlannedActions) != 2 {
		t.Fatalf("拒绝短路回执未保留组合计划: %+v", frozen.Application.Outcome)
	}
	for _, plan := range frozen.Application.Outcome.PlannedActions {
		if plan.Text != "" {
			t.Fatalf("不可变冻结回执不得复制固定话术正文: %+v", plan)
		}
	}
	var actions, invocations int64
	if err := s.db.Model(&CommunicationAction{}).
		Where("turn_id = ?", frozen.Turn.TurnID).
		Count(&actions).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&AIInvocation{}).
		Where("turn_id = ?", frozen.Turn.TurnID).
		Count(&invocations).Error; err != nil {
		t.Fatal(err)
	}
	if actions != 1 || invocations != 0 {
		t.Fatalf("拒绝短路只能规划正文且不得调用模型: actions=%d invocations=%d", actions, invocations)
	}
	replayed, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || replayed.Created ||
		replayed.Turn.Status != DialogueTurnAdviceReady ||
		replayed.Aggregate.Revision != frozen.Aggregate.Revision {
		t.Fatalf("拒绝短路冻结重放不幂等: result=%+v err=%v", replayed, err)
	}
}

func TestCommunicationV4InterruptedIntentPersistsFallbackContinuation(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-interrupted")
	text := "合成普通回复"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-turn-interrupted-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-v4-interrupted", TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-v4-interrupted",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.RecoverInterruptedAIInvocations(
		time.Now().UTC().Truncate(time.Millisecond),
	)
	if err != nil || recovered != 1 {
		t.Fatalf("V4 中断意向调用未收敛: recovered=%d err=%v", recovered, err)
	}
	turn, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if turn == nil || turn.Status != DialogueTurnClassified ||
		turn.IntentLabel != m5ai.IntentNeutral ||
		turn.IntentSource != DialogueIntentLLMFailure ||
		aggregateErr != nil || aggregate.Revision != 2 {
		t.Fatalf("V4 中断意向未形成可恢复 neutral continuation: turn=%+v aggregate=%+v err=%v",
			turn, aggregate, aggregateErr)
	}
	reply, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-v4-after-interrupted", TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-v4-after-interrupted",
		CreatedAt: time.Now(),
	})
	if err != nil || !reply.Created {
		t.Fatalf("中断 fallback 后唯一回复调用未开放: result=%+v err=%v", reply, err)
	}
}

func TestCommunicationV4AdviceContinuationSurvivesRestartWithoutProliferation(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-restart")
	text := "合成普通回复"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-turn-restart-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	intentID := "invocation-v4-restart-intent"
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: intentID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-v4-restart-intent",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	intentCompletion := successfulInvocationCompletion(
		intentID,
		time.Now().UTC().Truncate(time.Millisecond),
	)
	if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: intentCompletion, Label: m5ai.IntentInterested, Source: DialogueIntentLLM,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	replyID := "invocation-v4-restart-reply"
	reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: replyID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-v4-restart-reply",
		CreatedAt: time.Now(),
	})
	if err != nil || !reserved.Created {
		t.Fatalf("重启后未从 intent continuation 恢复 reply 权限: result=%+v err=%v", reserved, err)
	}
	replyText := "重启后继续生成的唯一回复"
	replyCompletion := successfulInvocationCompletion(
		replyID,
		time.Now().UTC().Truncate(time.Millisecond),
	)
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: replyCompletion, ActionID: "ignored-after-restart",
		Text: replyText, ContentHash: textcanon.Hash(replyText),
		PlannedAt: replyCompletion.FinishedAt,
	})
	if err != nil || action == nil {
		t.Fatalf("重启后 reply continuation 未形成动作: action=%+v err=%v", action, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.Revision != 3 {
		t.Fatalf("二次重启后 aggregate 未恢复: aggregate=%+v err=%v", aggregate, err)
	}
	var applications, actions int64
	if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
		Where("profile_id = ?", fixture.ProfileID).
		Count(&applications).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CommunicationAction{}).
		Where("turn_id = ?", frozen.Turn.TurnID).
		Count(&actions).Error; err != nil {
		t.Fatal(err)
	}
	if applications != 3 || actions != 1 {
		t.Fatalf("重启恢复发生事实增生: applications=%d actions=%d", applications, actions)
	}
}

func TestCommunicationV4AdviceKeyRejectsChangedSemanticResult(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-advice-conflict")
	text := "合成普通回复"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-turn-advice-conflict-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	invocationID := "invocation-v4-advice-conflict"
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: invocationID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-v4-advice-conflict",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	completion := successfulInvocationCompletion(
		invocationID,
		time.Now().UTC().Truncate(time.Millisecond),
	)
	if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: completion, Label: m5ai.IntentInterested, Source: DialogueIntentLLM,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: completion, Label: m5ai.IntentNeutral, Source: DialogueIntentLLM,
	}); !errors.Is(err, ErrCommunicationV4Conflict) {
		t.Fatalf("同 advice key 偷换分类必须冲突: %v", err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.Revision != 2 {
		t.Fatalf("冲突重放不应推进 aggregate: aggregate=%+v err=%v", aggregate, err)
	}
}

func TestCommunicationV4ReasoningUsageUnsafeNeverPlansAction(t *testing.T) {
	t.Run("positive reasoning tokens block intent continuation", func(t *testing.T) {
		s := openTest(t)
		fixture := seedReadyCommunicationTarget(t, s, "profile-v4-reasoning-intent")
		text := "合成普通回复"
		inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
			Seq: 2, Direction: "in", Kind: "text",
			ContentHash: "v4-reasoning-intent-2", Text: &text,
		})
		frozen, err := s.FreezeCommunicationV4Turn(
			communicationV4TurnRequest(t, s, fixture, inbound),
		)
		if err != nil {
			t.Fatal(err)
		}
		invocationID := "invocation-v4-reasoning-intent"
		if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
			InvocationID: invocationID, TurnID: frozen.Turn.TurnID,
			Purpose: m5ai.PurposeIntent, Attempt: 1,
			Provider: "deepseek", Model: "deepseek-v4-pro",
			InputHash: "input-v4-reasoning-intent",
		}); err != nil {
			t.Fatal(err)
		}
		completion := successfulInvocationCompletion(
			invocationID,
			time.Now().UTC().Truncate(time.Millisecond),
		)
		one := 1
		completion.ReasoningTokens = &one
		turn, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
			Completion: completion, Label: m5ai.IntentInterested, Source: DialogueIntentLLM,
		})
		if err != nil || turn.Status != DialogueTurnManualRequired ||
			turn.FailureReason != "reasoningUsageUnsafe" ||
			turn.IntentLabel != "" || turn.IntentSource != "" {
			t.Fatalf("V4 intent 未服从非思考硬闸: turn=%+v err=%v", turn, err)
		}
		var actions int64
		if err := s.db.Model(&CommunicationAction{}).
			Where("turn_id = ?", frozen.Turn.TurnID).
			Count(&actions).Error; err != nil || actions != 0 {
			t.Fatalf("不安全 intent 不得形成动作: count=%d err=%v", actions, err)
		}
	})

	t.Run("nonempty reasoning content blocks reply action", func(t *testing.T) {
		s := openTest(t)
		fixture := seedReadyCommunicationTarget(t, s, "profile-v4-reasoning-reply")
		text := "合成普通回复"
		inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
			Seq: 2, Direction: "in", Kind: "text",
			ContentHash: "v4-reasoning-reply-2", Text: &text,
		})
		frozen, err := s.FreezeCommunicationV4Turn(
			communicationV4TurnRequest(t, s, fixture, inbound),
		)
		if err != nil {
			t.Fatal(err)
		}
		intentID := "invocation-v4-reasoning-reply-intent"
		if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
			InvocationID: intentID, TurnID: frozen.Turn.TurnID,
			Purpose: m5ai.PurposeIntent, Attempt: 1,
			Provider: "deepseek", Model: "deepseek-v4-pro",
			InputHash: "input-v4-reasoning-reply-intent",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
			Completion: successfulInvocationCompletion(
				intentID,
				time.Now().UTC().Truncate(time.Millisecond),
			),
			Label: m5ai.IntentInterested, Source: DialogueIntentLLM,
		}); err != nil {
			t.Fatal(err)
		}
		replyID := "invocation-v4-reasoning-reply"
		if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
			InvocationID: replyID, TurnID: frozen.Turn.TurnID,
			Purpose: m5ai.PurposeReply, Attempt: 1,
			Provider: "deepseek", Model: "deepseek-v4-pro",
			InputHash: "input-v4-reasoning-reply",
		}); err != nil {
			t.Fatal(err)
		}
		completion := successfulInvocationCompletion(
			replyID,
			time.Now().UTC().Truncate(time.Millisecond),
		)
		completion.UsageShape = AIInvocationReasoningFieldAbsent
		completion.ReasoningTokens = nil
		completion.ReasoningContentEmpty = false
		action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
			Completion: completion, ActionID: "must-not-be-used",
			Text: "不得发送的模型正文", ContentHash: textcanon.Hash("不得发送的模型正文"),
		})
		if err != nil || action != nil {
			t.Fatalf("不安全 V4 reply 必须零动作收敛: action=%+v err=%v", action, err)
		}
		turn, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
		if turn == nil || turn.Status != DialogueTurnManualRequired ||
			turn.FailureReason != "reasoningUsageUnsafe" {
			t.Fatalf("不安全 V4 reply 未转人工: %+v", turn)
		}
		var actions int64
		if err := s.db.Model(&CommunicationAction{}).
			Where("turn_id = ?", frozen.Turn.TurnID).
			Count(&actions).Error; err != nil || actions != 0 {
			t.Fatalf("不安全 reply 不得形成动作: count=%d err=%v", actions, err)
		}
	})
}

func TestCommunicationV4CompletionSettlesChangedBoundaryWithoutPendingInvocation(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-completion-stale")
	text := "合成第一条入站"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: "v4-completion-stale-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	invocationID := "invocation-v4-completion-stale"
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: invocationID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-v4-completion-stale",
	}); err != nil {
		t.Fatal(err)
	}
	later := "模型在途时到达的新消息"
	appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "in", Kind: "text",
		ContentHash: "v4-completion-stale-3", Text: &later,
	})
	turn, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: successfulInvocationCompletion(
			invocationID,
			time.Now().UTC().Truncate(time.Millisecond),
		),
		Label: m5ai.IntentInterested, Source: DialogueIntentLLM,
	})
	if err != nil || turn.Status != DialogueTurnManualRequired ||
		turn.FailureReason != "inputBoundaryChanged" {
		t.Fatalf("边界变化后的 completion 未保留终局并转人工: turn=%+v err=%v", turn, err)
	}
	invocations, err := s.AIInvocationsForTurn(frozen.Turn.TurnID)
	if err != nil || len(invocations) != 1 || invocations[0].FinishedAt == nil ||
		invocations[0].Status != AIInvocationOK {
		t.Fatalf("边界变化回滚了 invocation 终局: invocations=%+v err=%v", invocations, err)
	}
	var applications int64
	if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
		Where("profile_id = ? AND input_kind = ?",
			fixture.ProfileID, CommunicationV4InputDialogueAdvice).
		Count(&applications).Error; err != nil || applications != 0 {
		t.Fatalf("过时模型结果不得成为 V4 continuation: count=%d err=%v", applications, err)
	}
}

func TestCommunicationV4InterruptedInvocationWithChangedBoundaryRecoversAfterRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-recovery-stale")
	text := "合成第一条入站"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: "v4-recovery-stale-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	invocationID := "invocation-v4-recovery-stale"
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: invocationID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-v4-recovery-stale",
	}); err != nil {
		t.Fatal(err)
	}
	later := "重启前到达的新消息"
	appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "in", Kind: "text",
		ContentHash: "v4-recovery-stale-3", Text: &later,
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	recovered, err := s.RecoverInterruptedAIInvocations(
		time.Now().UTC().Truncate(time.Millisecond),
	)
	if err != nil || recovered != 1 {
		t.Fatalf("重启恢复不应被预期 stale 阻断: recovered=%d err=%v", recovered, err)
	}
	turn, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
	if turn == nil || turn.Status != DialogueTurnManualRequired ||
		turn.FailureReason != "inputBoundaryChanged" {
		t.Fatalf("重启恢复未安全收敛 stale turn: %+v", turn)
	}
	invocations, err := s.AIInvocationsForTurn(frozen.Turn.TurnID)
	if err != nil || len(invocations) != 1 || invocations[0].FinishedAt == nil ||
		invocations[0].ErrorClass != "processInterrupted" {
		t.Fatalf("重启恢复未终局化 pending invocation: invocations=%+v err=%v", invocations, err)
	}
}
