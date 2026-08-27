package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	digest, turnID, err := DialogueTurnIdentity(fixture.ProfileID, greeting, inbound, 0)
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
		InputDigest: digest, HistoryThroughSeq: inbound[0].Seq - 1,
		InboundFromSeq: inbound[0].Seq, InboundThroughSeq: inbound[len(inbound)-1].Seq,
		ExpectedProjectedThroughSeq: targets[0].Aggregate.ProjectedThroughSeq,
		OutboundAnchorSeq:           greeting.Seq,
		ContextRevisionHash:         material.ContextRevision.RevisionHash,
		ResumeSnapshotID:            material.ResumeSnapshot.SnapshotID,
		RecommendedTimeText:         "合成推荐时段",
		RenderFormatVersion:         m5ai.DialogueRenderFormatVersion,
		FrozenAt:                    time.Now().UTC().Truncate(time.Millisecond),
	}
}

func setCommunicationV4FixedPhrasePackage(
	t *testing.T,
	s *Store,
	revisionHash string,
) {
	t.Helper()
	setCommunicationV4FixedPhrasePackageContent(
		t,
		s,
		revisionHash,
		`{
			"rejectWechat":{
				"message":"{称呼}合成挽留",
				"messages":["{称呼}合成挽留"],
				"actions":[],
				"enabled":true
			},
			"wechatAccepted":{
				"message":"合成微信回执",
				"messages":["合成微信回执"],
				"actions":[],
				"enabled":true
			},
			"meetingAccepted":{
				"message":"合成面试回执",
				"messages":["合成面试回执"],
				"actions":[],
				"enabled":true
			}
		}`,
	)
}

func setCommunicationV4FixedPhrasePackageContent(
	t *testing.T,
	s *Store,
	revisionHash string,
	content string,
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
			Content: content,
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

func TestFreezeCommunicationV4TurnIgnoresLegacyAuditBinding(t *testing.T) {
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
	if err != nil || result == nil || !result.Created {
		t.Fatalf("旧审计绑定不得控制新 turn 配置: result=%+v err=%v", result, err)
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
	if turns != 1 || applications != 1 {
		t.Fatalf("BackendJobID 路由应正常冻结: turns=%d applications=%d", turns, applications)
	}
}

func TestFreezeCommunicationV4TurnUsesLatestHeadThenKeepsFrozenRevision(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-turn-latest-head")
	next := advanceCommunicationJobHead(
		t,
		s,
		fixture.ProfileID,
		"revision-profile-v4-turn-latest-head-v2",
		time.Now().UTC().Add(time.Minute),
	)
	text := "合成最新配置消息"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: "v4-turn-latest-head-2", Text: &text,
	})
	req := communicationV4TurnRequest(t, s, fixture, inbound)
	if req.ContextRevisionHash != next.RevisionHash {
		t.Fatalf("新 turn 请求未读取最新 head: req=%+v next=%+v", req, next)
	}
	first, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || first == nil || !first.Created ||
		first.Turn.ContextRevisionHash != next.RevisionHash {
		t.Fatalf("新 turn 未冻结最新配置: first=%+v err=%v", first, err)
	}

	advanceCommunicationJobHead(
		t,
		s,
		fixture.ProfileID,
		"revision-profile-v4-turn-latest-head-v3",
		time.Now().UTC().Add(2*time.Minute),
	)
	replayed, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || replayed == nil || replayed.Created ||
		replayed.Turn.ContextRevisionHash != next.RevisionHash {
		t.Fatalf("已建 turn 重放不得漂移到新 head: replayed=%+v err=%v", replayed, err)
	}
	current, err := s.RecheckDialogueTurnCurrent(first.Turn.TurnID, time.Now())
	if err != nil || !current {
		t.Fatalf("head 更新不应使冻结 turn 失效: current=%v err=%v", current, err)
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

func TestCommunicationV4ReplyActionPersistsMeetingPlanAndReplaysWithoutGrowth(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-meeting-advice")
	text := "合成普通回复"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: "v4-meeting-advice-2", Text: &text,
	})
	request := communicationV4TurnRequest(t, s, fixture, inbound)
	var err error
	request.RecommendedTimeText, err = m5ai.FreezeRecommendedTimeText(
		time.Date(2026, 7, 10, 14, 23, 0, 0, time.FixedZone("CST", 8*60*60)),
		[]string{"2026-07-14 09:00:00", "2026-07-14 14:00:00"},
	)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := s.FreezeCommunicationV4Turn(request)
	if err != nil {
		t.Fatal(err)
	}

	intentID := "invocation-v4-meeting-intent"
	if reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: intentID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-v4-meeting-intent",
	}); err != nil || !reserved.Created {
		t.Fatalf("邀面意向调用未预留: result=%+v err=%v", reserved, err)
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

	replyID := "invocation-v4-meeting-reply"
	if reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: replyID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-v4-meeting-reply",
	}); err != nil || !reserved.Created {
		t.Fatalf("邀面回复调用未预留: result=%+v err=%v", reserved, err)
	}
	replyText := "那我们约在这个时间视频面试。"
	completion := successfulInvocationCompletion(
		replyID,
		time.Now().UTC().Truncate(time.Millisecond),
	)
	complete := CompleteReplyInvocationRequest{
		Completion:  completion,
		ActionID:    "caller-action-id-is-not-authoritative",
		Text:        replyText,
		Action:      m5ai.ReplyActionStartOnlineMeeting,
		MeetingTime: " \n7月14日14:00\t",
		ContentHash: textcanon.Hash(replyText),
		PlannedAt:   completion.FinishedAt,
	}
	action, err := s.CompleteReplyInvocation(complete)
	if err != nil || action == nil ||
		action.ActionID != frozen.Turn.TurnID+"|replyText" ||
		action.Status != CommunicationActionPlanned {
		t.Fatalf("邀面建议没有先实体化唯一正文动作: action=%+v err=%v", action, err)
	}
	actions, err := s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 1 ||
		actions[0].Kind != CommunicationActionReplyText {
		t.Fatalf("正文正证前不得实体化邀面卡: actions=%+v err=%v", actions, err)
	}
	var advice CommunicationV4ProjectionApplication
	if err := s.db.First(
		&advice,
		"profile_id = ? AND input_kind = ? AND input_key = ?",
		fixture.ProfileID,
		CommunicationV4InputDialogueAdvice,
		communicationV4DialogueAdviceKey(frozen.Turn.TurnID, m5ai.PurposeReply),
	).Error; err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(
		2026,
		7,
		14,
		14,
		0,
		0,
		0,
		time.FixedZone("CST", 8*60*60),
	).UnixMilli()
	if len(advice.Outcome.PlannedActions) != 2 ||
		advice.Outcome.PlannedActions[0].Text != "" ||
		advice.Outcome.PlannedActions[1].Kind != communication.V4ActionInterviewInvite ||
		advice.Outcome.PlannedActions[1].InterviewStartsAtMs == nil ||
		*advice.Outcome.PlannedActions[1].InterviewStartsAtMs != wantStart ||
		advice.Outcome.PlannedActions[1].InterviewEndsAtMs == nil ||
		*advice.Outcome.PlannedActions[1].InterviewEndsAtMs !=
			wantStart+communication.V4InterviewDurationMs ||
		advice.Outcome.PlannedActions[1].InterviewMethod == nil ||
		*advice.Outcome.PlannedActions[1].InterviewMethod != "wechatVideo" {
		t.Fatalf("邀面 continuation 没有保留脱敏两动作计划: %+v", advice.Outcome)
	}

	replayed, err := s.CompleteReplyInvocation(complete)
	if err != nil || replayed == nil || replayed.ActionID != action.ActionID {
		t.Fatalf("同一邀面 completion 重放失败: action=%+v err=%v", replayed, err)
	}
	actions, err = s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 1 {
		t.Fatalf("重复完成发生动作增生: actions=%+v err=%v", actions, err)
	}
	var applications int64
	if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
		Where("profile_id = ?", fixture.ProfileID).
		Count(&applications).Error; err != nil || applications != 3 {
		t.Fatalf("重复完成发生 projection 增生: count=%d err=%v", applications, err)
	}

	changed := complete
	changed.Action = m5ai.ReplyActionInviteWechat
	changed.MeetingTime = ""
	if _, err := s.CompleteReplyInvocation(changed); !errors.Is(err, ErrCommunicationV4Conflict) {
		t.Fatalf("同一 invocation 的闭合动作建议变化未被 digest 拒绝: %v", err)
	}
}

// 旧 turn(仅保存旧渲染文本、无 canonical 时段)上的邀面建议按规格 §五属
// "本次建议整体无效":2026-08-02 裁决后不再第 1 次尝试即停靠,而是零动作、
// 样本作废、安排下轮重采;5 次梯子耗尽后的停靠形态由重采测试族另行覆盖。
func TestCommunicationV4MeetingActionOnLegacyTurnSchedulesResampleWithZeroAction(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-legacy-meeting")
	text := "合成普通回复"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: "v4-legacy-meeting-2", Text: &text,
	})
	request := communicationV4TurnRequest(t, s, fixture, inbound)
	request.RecommendedTimeText = `{"inline":"旧内联时段","block":"旧时段块"}`
	frozen, err := s.FreezeCommunicationV4Turn(request)
	if err != nil {
		t.Fatal(err)
	}
	intentID := "invocation-v4-legacy-meeting-intent"
	if reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: intentID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-v4-legacy-meeting-intent",
	}); err != nil || !reserved.Created {
		t.Fatalf("旧轮 intent 未预留: result=%+v err=%v", reserved, err)
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
	replyID := "invocation-v4-legacy-meeting-reply"
	if reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: replyID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-v4-legacy-meeting-reply",
	}); err != nil || !reserved.Created {
		t.Fatalf("旧轮 reply 未预留: result=%+v err=%v", reserved, err)
	}
	replyText := "这条承诺正文也不能单独发送。"
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: successfulInvocationCompletion(
			replyID,
			time.Now().UTC().Truncate(time.Millisecond),
		),
		ActionID: "must-not-be-used",
		Text:     replyText, Action: m5ai.ReplyActionStartOnlineMeeting,
		MeetingTime: "7月14日14:00", ContentHash: textcanon.Hash(replyText),
	})
	var resample *AIAdviceResampleScheduledError
	if action != nil || !errors.As(err, &resample) ||
		resample.Reason != string(communication.V4ManualReplyInvalid) ||
		resample.Attempt != 1 {
		t.Fatalf("旧 turn 邀面建议必须零动作并安排重采: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(frozen.Turn.TurnID)
	if err != nil || turn == nil ||
		turn.Status != DialogueTurnClassified ||
		turn.FailureReason != "" {
		t.Fatalf("重采样本不得留下轮终局: turn=%+v err=%v", turn, err)
	}
	actions, err := s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 0 {
		t.Fatalf("旧 turn 邀面建议产生了动作: actions=%+v err=%v", actions, err)
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
	archiveAt := beforeArchive.State.LastBodyAt.Add(8 * 24 * time.Hour)
	archiveReq := communicationV4ArchiveRequestForTest(
		t, s, *beforeArchive, archiveAt, true,
	)
	archiveResult, err := s.ApplyCommunicationV4ArchiveAction(archiveReq)
	if err != nil || archiveResult == nil || !archiveResult.Applied ||
		archiveResult.Aggregate.State.MainStatus != communication.V4StatusEnded ||
		archiveResult.Aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("归档没有保持档案可唤醒: result=%+v err=%v",
			archiveResult, err)
	}
	archived := archiveResult.Aggregate
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

	replayed, err := s.ApplyCommunicationV4ArchiveAction(archiveReq)
	if err != nil || replayed.Applied || replayed.Aggregate.Revision != archived.Revision {
		t.Fatalf("归档重放发生增生: result=%+v err=%v",
			replayed, err)
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
	requestSourceKey := strings.Repeat("9", 64)
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
		CardState: "pending", ContentHash: "v4-turn-prerequisite-2",
		SourceKey: &requestSourceKey,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil ||
		frozen.Application.Outcome.DialogueStatus != communication.V4DialogueWaitingPrerequisite ||
		frozen.Application.Outcome.NextAdvice != communication.V4AdviceNone ||
		!frozen.Application.Outcome.DialogueAfterActions {
		t.Fatalf("主动换微信没有冻结为前置动作等待: frozen=%+v err=%v", frozen, err)
	}
	eventActions, err := s.CommunicationV4EventActionsBySource(
		fixture.ProfileID,
		CommunicationV4InputDialogueTurn,
		frozen.Turn.TurnID,
	)
	if err != nil || len(eventActions) != 2 {
		t.Fatalf("主动换微信前置动作未物化: actions=%+v err=%v", eventActions, err)
	}
	for _, action := range eventActions {
		switch action.V4Kind {
		case communication.V4ActionAcceptWechat:
			fingerprint, fingerprintErr := AcceptWechatFingerprint(requestSourceKey)
			if fingerprintErr != nil {
				t.Fatal(fingerprintErr)
			}
			if action.Status != CommunicationV4EventActionPlanned ||
				action.FailureReason != "" ||
				action.ContentHash != fingerprint ||
				action.ContentHash == requestSourceKey {
				t.Fatalf("接受换微信动作未冻结为可派发摘要: %+v", action)
			}
		case communication.V4ActionNotifyWechat:
			if action.Status != CommunicationV4EventActionDeferred ||
				action.FailureReason !=
					CommunicationV4EventActionFailureNotificationOutboxOwned {
				t.Fatalf("换微信通知未标记为发件箱承接: %+v", action)
			}
		default:
			t.Fatalf("主动换微信出现未知事件动作: %+v", action)
		}
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

func TestFreezeCommunicationV4WechatReceiptUsesProfileScopedEventAction(t *testing.T) {
	s := openTest(t)
	profileIDs := []string{
		"profile-v4-dialogue-receipt-a",
		"profile-v4-dialogue-receipt-b",
	}
	type observedReceipt struct {
		actionID    string
		semanticKey string
		turnID      string
	}
	observed := make([]observedReceipt, 0, len(profileIDs))
	for _, profileID := range profileIDs {
		fixture := seedReadyCommunicationTarget(t, s, profileID)
		setCommunicationV4FixedPhrasePackage(
			t,
			s,
			"revision-"+profileID,
		)
		inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
			Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
			CardState: "accepted", ContentHash: "v4-wechat-receipt-accepted",
		})
		var greeting Message
		if err := s.db.First(
			&greeting,
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
			fixture.Platform,
			fixture.AccountRef,
			fixture.ConversationRef,
			1,
		).Error; err != nil {
			t.Fatal(err)
		}
		digest, turnID, err := DialogueTurnIdentity(profileID, greeting, inbound, 0)
		if err != nil {
			t.Fatal(err)
		}
		material, ready, err := s.CommunicationAIMaterialForProfile(profileID)
		if err != nil || !ready {
			t.Fatalf("换微信回执材料未就绪: ready=%v err=%v", ready, err)
		}
		aggregateRow, err := s.CommunicationV4AggregateByProfile(profileID)
		if err != nil {
			t.Fatal(err)
		}
		req := FreezeDialogueTurnRequest{
			TurnID: turnID, ProfileID: profileID, ConversationRef: fixture.ConversationRef,
			InputDigest: digest, HistoryThroughSeq: inbound[0].Seq - 1,
			InboundFromSeq: inbound[0].Seq, InboundThroughSeq: inbound[0].Seq,
			ExpectedProjectedThroughSeq: aggregateRow.ProjectedThroughSeq,
			OutboundAnchorSeq:           greeting.Seq,
			ContextRevisionHash:         material.ContextRevision.RevisionHash,
			ResumeSnapshotID:            material.ResumeSnapshot.SnapshotID,
			RecommendedTimeText:         "合成推荐时段",
			RenderFormatVersion:         m5ai.DialogueRenderFormatVersion,
			FrozenAt:                    time.Now().UTC().Truncate(time.Millisecond),
		}
		frozen, err := s.FreezeCommunicationV4Turn(req)
		if err != nil || !frozen.Created ||
			frozen.Turn.Status != DialogueTurnCompleted ||
			frozen.Application.Outcome.DialogueStatus != communication.V4DialogueNoAction ||
			len(frozen.Application.Outcome.PlannedActions) != 0 ||
			len(frozen.Application.Outcome.Actions) != 2 {
			t.Fatalf("换微信回执未由事件动作独占: frozen=%+v err=%v", frozen, err)
		}
		dialogueActions, err := s.CommunicationActionsByTurn(frozen.Turn.TurnID)
		if err != nil || len(dialogueActions) != 0 {
			t.Fatalf("新回执轮不得创建 CommunicationAction: actions=%+v err=%v",
				dialogueActions, err)
		}
		eventActions, err := s.CommunicationV4EventActionsBySource(
			profileID,
			CommunicationV4InputDialogueTurn,
			frozen.Turn.TurnID,
		)
		if err != nil || len(eventActions) != 2 {
			t.Fatalf("换微信事件动作未原子物化: actions=%+v err=%v", eventActions, err)
		}
		var receipt *CommunicationV4EventAction
		var notification *CommunicationV4EventAction
		for actionIndex := range eventActions {
			action := &eventActions[actionIndex]
			switch action.V4Kind {
			case communication.V4ActionWechatReceipt:
				receipt = action
			case communication.V4ActionNotifyWechat:
				notification = action
			}
		}
		if receipt == nil ||
			receipt.Status != CommunicationV4EventActionPlanned ||
			receipt.Text != "合成微信回执" ||
			receipt.ContentHash != textcanon.Hash("合成微信回执") ||
			receipt.ContextRevisionHash != "revision-"+profileID ||
			receipt.FailureReason != "" ||
			receipt.DependsOnActionID != nil ||
			notification == nil ||
			notification.Status != CommunicationV4EventActionDeferred ||
			notification.FailureReason !=
				CommunicationV4EventActionFailureNotificationOutboxOwned {
			t.Fatalf("换微信事件动作处置错误: actions=%+v", eventActions)
		}
		observed = append(observed, observedReceipt{
			actionID: receipt.ActionID, semanticKey: receipt.SemanticActionKey,
			turnID: frozen.Turn.TurnID,
		})

		replayed, err := s.FreezeCommunicationV4Turn(req)
		if err != nil || replayed.Created {
			t.Fatalf("换微信回执轮重放失败: replayed=%+v err=%v", replayed, err)
		}
		replayedActions, err := s.CommunicationV4EventActionsBySource(
			profileID,
			CommunicationV4InputDialogueTurn,
			frozen.Turn.TurnID,
		)
		if err != nil || len(replayedActions) != 2 {
			t.Fatalf("换微信回执重放发生增生: actions=%+v err=%v",
				replayedActions, err)
		}
	}
	if observed[0].semanticKey != observed[1].semanticKey ||
		observed[0].actionID == observed[1].actionID {
		t.Fatalf("同 seq 回执未按 profile 隔离: observed=%+v", observed)
	}
	crossSource, err := s.CommunicationV4EventActionsBySource(
		profileIDs[0],
		CommunicationV4InputDialogueTurn,
		observed[1].turnID,
	)
	if err != nil || len(crossSource) != 0 {
		t.Fatalf("DialogueTurn 事件动作来源查询串 profile: actions=%+v err=%v",
			crossSource, err)
	}
}

func TestFreezeCommunicationV4InterviewReceiptChainsEventInvite(t *testing.T) {
	s := openTest(t)
	profileID := "profile-v4-dialogue-interview-receipt"
	fixture := seedReadyCommunicationTarget(t, s, profileID)
	setCommunicationV4FixedPhrasePackage(t, s, "revision-"+profileID)
	candidateText := "合成前置候选人消息"
	appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		Text: &candidateText, ContentHash: "v4-interview-before-invite",
	})
	at := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := s.ApplyCommunicationV4BusinessEvent(
		ApplyCommunicationV4BusinessEventRequest{
			ProfileID: profileID,
			Event: communication.BusinessEvent{
				Key: "message:2", Kind: communication.EventCandidateExpressionReceived,
				Source: communication.EventSourceMessage, MessageSeq: 2,
				ExpressionKind: communication.ExpressionText, Text: candidateText,
			},
			AppliedAt: at,
		},
	); err != nil {
		t.Fatal(err)
	}
	inviteMessage := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "out", Kind: "card", CardType: "interviewInvite",
		CardState: "pending", ContentHash: "v4-interview-invite-card", Origin: "self",
		CreatedAt: at.Add(time.Second),
	})[0]
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		_, _, _, err := applyCommunicationV4ConfirmedActionTx(
			tx,
			profileID,
			communication.V4ConfirmedAction{
				ActionKey:  "fixture-interview-invite",
				Kind:       communication.V4ActionInterviewInvite,
				MessageSeq: inviteMessage.Seq,
				SentAt:     &inviteMessage.CreatedAt,
			},
			inviteMessage.CreatedAt,
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	accepted := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 4, Direction: "in", Kind: "card", CardType: "interviewInvite",
		CardState: "accepted", ContentHash: "v4-interview-accepted",
		CreatedAt: at.Add(2 * time.Second),
	})
	digest, turnID, err := DialogueTurnIdentity(profileID, inviteMessage, accepted, 0)
	if err != nil {
		t.Fatal(err)
	}
	material, ready, err := s.CommunicationAIMaterialForProfile(profileID)
	if err != nil || !ready {
		t.Fatalf("邀面接受轮材料未就绪: ready=%v err=%v", ready, err)
	}
	aggregateRow, err := s.CommunicationV4AggregateByProfile(profileID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := s.FreezeCommunicationV4Turn(FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: profileID, ConversationRef: fixture.ConversationRef,
		InputDigest: digest, HistoryThroughSeq: accepted[0].Seq - 1,
		InboundFromSeq: accepted[0].Seq, InboundThroughSeq: accepted[0].Seq,
		ExpectedProjectedThroughSeq: aggregateRow.ProjectedThroughSeq,
		OutboundAnchorSeq:           inviteMessage.Seq,
		ContextRevisionHash:         material.ContextRevision.RevisionHash,
		ResumeSnapshotID:            material.ResumeSnapshot.SnapshotID,
		RecommendedTimeText:         "合成推荐时段",
		RenderFormatVersion:         m5ai.DialogueRenderFormatVersion,
		FrozenAt:                    at.Add(3 * time.Second),
	})
	if err != nil || frozen.Turn.Status != DialogueTurnCompleted ||
		frozen.Application.Outcome.DialogueStatus != communication.V4DialogueNoAction ||
		len(frozen.Application.Outcome.PlannedActions) != 0 {
		t.Fatalf("邀面接受轮没有交给事件动作: frozen=%+v err=%v", frozen, err)
	}
	dialogueActions, err := s.CommunicationActionsByTurn(turnID)
	if err != nil || len(dialogueActions) != 0 {
		t.Fatalf("邀面接受轮不得创建 CommunicationAction: actions=%+v err=%v",
			dialogueActions, err)
	}
	eventActions, err := s.CommunicationV4EventActionsBySource(
		profileID,
		CommunicationV4InputDialogueTurn,
		turnID,
	)
	if err != nil || len(eventActions) != 3 {
		t.Fatalf("邀面接受事件动作未物化: actions=%+v err=%v", eventActions, err)
	}
	var receipt *CommunicationV4EventAction
	var invite *CommunicationV4EventAction
	for index := range eventActions {
		action := &eventActions[index]
		switch action.V4Kind {
		case communication.V4ActionInterviewAcceptedReceipt:
			receipt = action
		case communication.V4ActionInviteWechat:
			invite = action
		}
	}
	if receipt == nil ||
		receipt.Status != CommunicationV4EventActionPlanned ||
		receipt.Text != "合成面试回执" ||
		invite == nil ||
		invite.Status != CommunicationV4EventActionPlanned ||
		invite.DependsOnActionID == nil ||
		*invite.DependsOnActionID != receipt.ActionID ||
		*invite.DependsOnActionID == receipt.SemanticActionKey {
		t.Fatalf("邀面回执→换微信卡依赖未留在 EventAction: actions=%+v", eventActions)
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
	// 2026-08-02 裁决:边界失配的 pre-effect 轮作废,聚合保持 active,不再
	// 局部转人工;后续新输入按最新账本边界重开新轮。
	turn, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
	if turn == nil || turn.Status != DialogueTurnSuperseded ||
		turn.FailureReason != "boundarySuperseded" {
		t.Fatalf("旧 revision 轮未作废: %+v", turn)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" {
		t.Fatalf("边界失配不得冻结聚合: aggregate=%+v err=%v", aggregate, err)
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
		actions[0].Text != "候选人合成挽留" ||
		actions[0].ContentHash != textcanon.Hash("候选人合成挽留") ||
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
	var action CommunicationAction
	if err := s.db.First(
		&action,
		"turn_id = ?",
		frozen.Turn.TurnID,
	).Error; err != nil ||
		action.Text != "候选人合成挽留" ||
		action.ContentHash != textcanon.Hash("候选人合成挽留") {
		t.Fatalf("拒绝短路没有共用固定话术渲染器: action=%+v err=%v", action, err)
	}
	replayed, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || replayed.Created ||
		replayed.Turn.Status != DialogueTurnAdviceReady ||
		replayed.Aggregate.Revision != frozen.Aggregate.Revision {
		t.Fatalf("拒绝短路冻结重放不幂等: result=%+v err=%v", replayed, err)
	}
}

func TestCommunicationV4RejectionFixedMessagesRenderAndMaterializeOneAtATime(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-short-rejected-bubbles")
	setCommunicationV4FixedPhrasePackageContent(
		t,
		s,
		"revision-profile-v4-short-rejected-bubbles",
		`{
			"rejectWechat":{
				"message":"{称呼}第一项",
				"messages":[
					"{称呼}第一项",
					"第二项第一行。\n第二项第二行。",
					"第三项。仍然是同一个气泡。"
				],
				"actions":[],
				"enabled":true
			}
		}`,
	)
	inboundText := "暂时不考虑，谢谢"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: "v4-short-rejected-bubbles-2", Text: &inboundText,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	wantPhrases := []string{
		"候选人第一项",
		"第二项第一行。\n第二项第二行。",
		"第三项。仍然是同一个气泡。",
	}
	if err != nil ||
		frozen.Turn.Status != DialogueTurnAdviceReady ||
		!reflect.DeepEqual(frozen.Turn.ReplyPhrases, wantPhrases) ||
		len(frozen.Application.Outcome.PlannedActions) != len(wantPhrases)+1 {
		t.Fatalf("固定挽留数组未按渲染后边界冻结: result=%+v err=%v", frozen, err)
	}
	for index, plan := range frozen.Application.Outcome.PlannedActions {
		if plan.Text != "" {
			t.Fatalf("冻结计划[%d]泄露固定话术正文: %+v", index, plan)
		}
	}

	effectFixture := communicationV4AutomaticEffectFixture{
		resumeStoreFixture: fixture,
		Turn:               frozen.Turn,
		Now:                frozen.Turn.UpdatedAt,
	}
	for index, want := range wantPhrases {
		actions, actionErr := s.CommunicationActionsByTurn(frozen.Turn.TurnID)
		if actionErr != nil || len(actions) != index+1 {
			t.Fatalf("第 %d 个气泡前出现后续动作: actions=%+v err=%v", index+1, actions, actionErr)
		}
		current := actions[index]
		if current.Kind != CommunicationActionReplyText ||
			current.Status != CommunicationActionPlanned ||
			current.Text != want {
			t.Fatalf("第 %d 个固定气泡正文或状态错误: %+v", index+1, current)
		}
		if index == 0 {
			if current.DependsOnActionID != nil {
				t.Fatalf("首个固定气泡不得有父动作: %+v", current)
			}
		} else if current.DependsOnActionID == nil ||
			*current.DependsOnActionID != actions[index-1].ActionID {
			t.Fatalf("第 %d 个固定气泡未依赖上一项正证: %+v", index+1, current)
		}
		confirmCommunicationV4Bubble(
			t,
			s,
			effectFixture,
			current,
			frozen.Turn.InboundThroughSeq+int64(index),
			"fixed-rejection-bubble-"+string(rune('1'+index)),
		)
	}

	actions, err := s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != len(wantPhrases)+1 {
		t.Fatalf("最后固定气泡正证后未物化卡片: actions=%+v err=%v", actions, err)
	}
	card := actions[len(actions)-1]
	if card.Kind != CommunicationActionInviteWechat ||
		card.Status != CommunicationActionPlanned ||
		card.DependsOnActionID == nil ||
		*card.DependsOnActionID != actions[len(actions)-2].ActionID {
		t.Fatalf("换微信卡没有只依赖最后固定气泡: %+v", card)
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

// 2026-08-16 甲方裁决开启思考模式后,reasoning 用量非零/reasoning_content 非空
// 都是预期形态,V4 事务不再据此阻断。原
// TestCommunicationV4ReasoningUsageUnsafeNeverPlansAction 钉的是已撤销的非思考
// 硬闸,整条移除;新语义由 patrol 包的
// TestM5ReplyReasoningTokensNonzeroPlansActionNormally 与
// TestM5NonemptyReasoningContentPassesThroughToReply 钉住。

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
	// 2026-08-02 裁决:边界变化的 completion 保留 invocation 终局并作废旧轮,
	// 聚合不冻结,新消息属于下一轮。
	if err != nil || turn.Status != DialogueTurnSuperseded ||
		turn.FailureReason != "boundarySuperseded" {
		t.Fatalf("边界变化后的 completion 未保留终局并作废旧轮: turn=%+v err=%v", turn, err)
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
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" {
		t.Fatalf("边界失配不得冻结聚合: aggregate=%+v err=%v", aggregate, err)
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
	// 2026-08-02 裁决:重启恢复时发现边界已变,同样作废旧轮而不冻结候选人;
	// 中断的 invocation 仍如实终局。
	turn, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
	if turn == nil || turn.Status != DialogueTurnSuperseded ||
		turn.FailureReason != "boundarySuperseded" {
		t.Fatalf("重启恢复未安全收敛 stale turn: %+v", turn)
	}
	invocations, err := s.AIInvocationsForTurn(frozen.Turn.TurnID)
	if err != nil || len(invocations) != 1 || invocations[0].FinishedAt == nil ||
		invocations[0].ErrorClass != "processInterrupted" {
		t.Fatalf("重启恢复未终局化 pending invocation: invocations=%+v err=%v", invocations, err)
	}
}

// 同一出站锚下的连续两个候选人轮次（0727当日计划3）：轮1完成且无我方
// 出站后，候选人再次开口必须能以同锚、不同身份开出轮2，而不是撞上
// "游标必须指向出站"的旧断言。
func TestCommunicationV4ConsecutiveTurnsShareAnchorWithoutOutbound(t *testing.T) {
	s := openTest(t)
	profileID := "profile-v4-consecutive"
	fixture := seedReadyCommunicationTarget(t, s, profileID)
	setCommunicationV4FixedPhrasePackage(t, s, "revision-"+profileID)
	first := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
		CardState: "accepted", ContentHash: "wechat-accepted-consecutive",
	})
	firstReq := communicationV4TurnRequest(t, s, fixture, first)
	firstFrozen, err := s.FreezeCommunicationV4Turn(firstReq)
	if err != nil || firstFrozen.Turn.Status != DialogueTurnCompleted ||
		firstFrozen.Aggregate.ProjectedThroughSeq != 2 {
		t.Fatalf("轮1未完成或游标未停在候选人行: result=%+v err=%v", firstFrozen, err)
	}

	text := "我再考虑一下"
	second := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "in", Kind: "text",
		Text: &text, ContentHash: "consecutive-second",
	})
	secondReq := communicationV4TurnRequest(t, s, fixture, second)
	if secondReq.OutboundAnchorSeq != 1 || secondReq.HistoryThroughSeq != 2 ||
		secondReq.ExpectedProjectedThroughSeq != 2 {
		t.Fatalf("轮2请求未按解耦语义派生: %+v", secondReq)
	}
	secondFrozen, err := s.FreezeCommunicationV4Turn(secondReq)
	if err != nil || !secondFrozen.Created ||
		secondFrozen.Turn.TurnID == firstFrozen.Turn.TurnID ||
		secondFrozen.Turn.HistoryThroughSeq != 2 ||
		secondFrozen.Aggregate.ProjectedThroughSeq != 3 {
		t.Fatalf("同锚第二轮未独立开出: result=%+v err=%v", secondFrozen, err)
	}
	current, err := s.RecheckDialogueTurnCurrent(
		secondFrozen.Turn.TurnID,
		secondReq.FrozenAt.Add(time.Second),
	)
	if err != nil || !current {
		t.Fatalf("同锚第二轮未通过统一重验: current=%v err=%v", current, err)
	}
}

// 开轮准入闸拆腿回归(2026-08-02 甲方裁决,规格 v4 §一"旧轮失效"):从未派发
// 过发送意图的停靠旧轮,在候选人新输入进入开轮流程时被同一冻结事务作废,
// 新轮照常建立;聚合始终不冻结。
func TestFreezeCommunicationV4TurnSupersedesParkedPreEffectTurn(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-gate-park")
	text := "第一轮入站"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-gate-park-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.db.Create(&CommunicationAction{
		ActionID: "action-v4-gate-park", TurnID: frozen.Turn.TurnID,
		Kind: CommunicationActionReplyText, Text: "未派发的合成建议",
		ContentHash: "hash-v4-gate-park", Status: CommunicationActionPlanned,
		PlannedAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// 以豁免原因停靠(第 3 族形态):turn manualRequired、动作连带停靠,聚合
	// 保持 active——这正是拆腿前会把候选人终身卡死的形状。
	if err := s.MarkDialogueTurnManualRequired(
		frozen.Turn.TurnID, "inputBudgetBlocked", now,
	); err != nil {
		t.Fatal(err)
	}
	parked, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || parked.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("停靠前提不成立(聚合应保持 active): aggregate=%+v err=%v", parked, err)
	}

	later := "候选人再次开口"
	second := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "in", Kind: "text", ContentHash: "v4-gate-park-3", Text: &later,
	})
	reopened, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, second),
	)
	if err != nil || !reopened.Created || reopened.Turn.TurnID == frozen.Turn.TurnID ||
		reopened.Turn.InboundFromSeq != 3 {
		t.Fatalf("新输入未重开新轮: result=%+v err=%v", reopened, err)
	}
	stale, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
	if stale == nil || stale.Status != DialogueTurnSuperseded ||
		stale.FailureReason != "boundarySuperseded" {
		t.Fatalf("停靠旧轮未在开轮事务内作废: %+v", stale)
	}
	var action CommunicationAction
	if err := s.db.First(&action, "action_id = ?", "action-v4-gate-park").Error; err != nil ||
		action.Status != CommunicationActionSuperseded ||
		action.FailureReason != "boundarySuperseded" {
		t.Fatalf("未派发动作未随轮作废: action=%+v err=%v", action, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ProjectedThroughSeq != 3 {
		t.Fatalf("重开后聚合未推进或被冻结: aggregate=%+v err=%v", aggregate, err)
	}
}

// 承重墙回归之一(2026-08-02 裁决红线):dispatching 旧轮照旧挡住开轮,等
// WAL/suspect 收敛,旧轮原样不动。
func TestFreezeCommunicationV4TurnStillRejectsDispatchingTurn(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-gate-dispatching")
	text := "第一轮入站"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-gate-dispatch-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&DialogueTurn{}).
		Where("turn_id = ?", frozen.Turn.TurnID).
		Update("status", DialogueTurnDispatching).Error; err != nil {
		t.Fatal(err)
	}
	later := "派发在途时的新消息"
	second := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "in", Kind: "text", ContentHash: "v4-gate-dispatch-3", Text: &later,
	})
	if _, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, second),
	); !errors.Is(err, ErrDialogueTurnState) {
		t.Fatalf("dispatching 旧轮必须照旧拒绝开轮: %v", err)
	}
	stale, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
	if stale == nil || stale.Status != DialogueTurnDispatching || stale.FailureReason != "" {
		t.Fatalf("被拒绝的开轮不得触碰 dispatching 旧轮: %+v", stale)
	}
	var turns int64
	if err := s.db.Model(&DialogueTurn{}).
		Where("profile_id = ?", fixture.ProfileID).
		Count(&turns).Error; err != nil || turns != 1 {
		t.Fatalf("被拒绝的开轮不得留下新轮: count=%d err=%v", turns, err)
	}
}

// 承重墙回归之二(2026-08-02 裁决红线):manualRequired 旧轮只要有动作行绑定
// 过发送意图(EffectIntentID/EffectStartedAt/SentAt 任一非空),开轮照旧拒绝
// ——判据是动作行事实,不按 FailureReason 字符串判。
func TestFreezeCommunicationV4TurnStillRejectsEffectBoundParkedTurn(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-gate-suspect")
	text := "第一轮入站"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-gate-suspect-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	intentID := "intent-v4-gate-suspect"
	if err := s.db.Create(&CommunicationAction{
		ActionID: "action-v4-gate-suspect", TurnID: frozen.Turn.TurnID,
		Kind: CommunicationActionReplyText, Text: "已绑定发送意图的合成动作",
		ContentHash: "hash-v4-gate-suspect", Status: CommunicationActionManualRequired,
		EffectIntentID: &intentID, FailureReason: "effectSuspect",
		PlannedAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&DialogueTurn{}).
		Where("turn_id = ?", frozen.Turn.TurnID).
		Updates(map[string]any{
			"status": DialogueTurnManualRequired, "failure_reason": "effectSuspect",
		}).Error; err != nil {
		t.Fatal(err)
	}
	later := "suspect 未裁决时的新消息"
	second := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "in", Kind: "text", ContentHash: "v4-gate-suspect-3", Text: &later,
	})
	if _, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, second),
	); !errors.Is(err, ErrDialogueTurnState) {
		t.Fatalf("带 effect 案底的旧轮必须照旧拒绝开轮: %v", err)
	}
	stale, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
	if stale == nil || stale.Status != DialogueTurnManualRequired ||
		stale.FailureReason != "effectSuspect" {
		t.Fatalf("被拒绝的开轮不得触碰带案底旧轮: %+v", stale)
	}
	var action CommunicationAction
	if err := s.db.First(&action, "action_id = ?", "action-v4-gate-suspect").Error; err != nil ||
		action.Status != CommunicationActionManualRequired ||
		action.FailureReason != "effectSuspect" ||
		action.EffectIntentID == nil || *action.EffectIntentID != intentID {
		t.Fatalf("被拒绝的开轮不得触碰案底动作: action=%+v err=%v", action, err)
	}
	var turns int64
	if err := s.db.Model(&DialogueTurn{}).
		Where("profile_id = ?", fixture.ProfileID).
		Count(&turns).Error; err != nil || turns != 1 {
		t.Fatalf("被拒绝的开轮不得留下新轮: count=%d err=%v", turns, err)
	}
}

func TestFreezeCommunicationV4TurnStillRejectsSuspectIntentCaseRecord(t *testing.T) {
	// Q6 状态判据的反向钉桩(审查补测):案底 intent 存在真实 suspect 终局时,
	// 承重墙必须继续拒绝作废重开——此前唯一反向测试打在悬空引用分支上,
	// 状态白名单写错也测不出。
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-gate-suspect-intent")
	text := "第一轮入站"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-gate-si-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	intentID := "intent-v4-gate-suspect-real"
	if err := s.db.Create(&EffectIntent{
		IntentID: intentID, IdemKey: "ik1:test:gate-suspect-real",
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		Primitive: "chat.sendMessage", TargetRef: fixture.ConversationRef,
		PayloadHash: "payload", GuardsHash: "guards", RootMsgID: "root-gate-suspect-real",
		Status: EffectIntentSuspect, DeadlineMs: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&CommunicationAction{
		ActionID: "action-v4-gate-suspect-real", TurnID: frozen.Turn.TurnID,
		Kind: CommunicationActionReplyText, Text: "suspect 案底动作",
		ContentHash: "hash-gate-suspect-real", Status: CommunicationActionManualRequired,
		EffectIntentID: &intentID, FailureReason: "effectSuspect",
		PlannedAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&DialogueTurn{}).
		Where("turn_id = ?", frozen.Turn.TurnID).
		Updates(map[string]any{
			"status": DialogueTurnManualRequired, "failure_reason": "effectSuspect",
		}).Error; err != nil {
		t.Fatal(err)
	}
	later := "suspect 未裁决时的新消息"
	second := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "in", Kind: "text", ContentHash: "v4-gate-si-3", Text: &later,
	})
	if _, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, second),
	); !errors.Is(err, ErrDialogueTurnState) {
		t.Fatalf("真实 suspect 案底必须照旧拒绝作废重开: %v", err)
	}
	stale, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
	if stale == nil || stale.Status != DialogueTurnManualRequired {
		t.Fatalf("被拒绝的开轮不得触碰 suspect 案底轮: %+v", stale)
	}
}

func TestFreezeCommunicationV4TurnSupersedesTurnWithOnlyCleanFailedEffects(t *testing.T) {
	// Q6(2026-08-03 甲方批准,2026-08-27 随停机点第二步实施):案底 intent
	// 终局全部属于 failed/resolvedFailed(构造性零副作用)的旧轮,新输入到达
	// 时允许作废重开;带终局案底的动作行原样留档,不被作废触碰。
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-gate-clean-failed")
	text := "第一轮入站"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-gate-clean-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	intentID := "intent-v4-gate-clean-failed"
	if err := s.db.Create(&EffectIntent{
		IntentID: intentID, IdemKey: "ik1:test:gate-clean-failed",
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		Primitive: "chat.sendMessage", TargetRef: fixture.ConversationRef,
		PayloadHash: "payload", GuardsHash: "guards", RootMsgID: "root-gate-clean-failed",
		Status: EffectIntentFailed, DeadlineMs: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&CommunicationAction{
		ActionID: "action-v4-gate-clean-failed", TurnID: frozen.Turn.TurnID,
		Kind: CommunicationActionReplyText, Text: "干净失败留档动作",
		ContentHash: "hash-gate-clean-failed", Status: CommunicationActionRetried,
		EffectIntentID: &intentID, FailureReason: "effectFailed",
		PlannedAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&DialogueTurn{}).
		Where("turn_id = ?", frozen.Turn.TurnID).
		Updates(map[string]any{
			"status": DialogueTurnManualRequired, "failure_reason": "effectFailed",
		}).Error; err != nil {
		t.Fatal(err)
	}
	later := "终局后的新消息"
	second := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "in", Kind: "text", ContentHash: "v4-gate-clean-3", Text: &later,
	})
	next, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, second),
	)
	if err != nil || next == nil || !next.Created {
		t.Fatalf("纯干净失败案底轮必须允许作废重开(Q6): next=%+v err=%v", next, err)
	}
	stale, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
	if stale == nil || stale.Status != DialogueTurnSuperseded ||
		stale.FailureReason != dialogueTurnBoundarySuperseded {
		t.Fatalf("旧轮未按 Q6 作废: %+v", stale)
	}
	var action CommunicationAction
	if err := s.db.First(&action, "action_id = ?", "action-v4-gate-clean-failed").Error; err != nil ||
		action.Status != CommunicationActionRetried || action.EffectIntentID == nil {
		t.Fatalf("终局案底动作必须原样留档: action=%+v err=%v", action, err)
	}
}

func TestFreezeCommunicationV4WechatAcceptedWithTextReplacesReceiptByContinuation(t *testing.T) {
	s := openTest(t)
	profileID := "profile-v4-batchb-accepted-text"
	fixture := seedReadyCommunicationTarget(t, s, profileID)
	setCommunicationV4FixedPhrasePackage(t, s, "revision-"+profileID)
	text := "加好了,后面微信聊"
	inbound := appendCommunicationV4Inbound(t, s, fixture,
		Message{
			Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
			CardState: "accepted", ContentHash: "v4-batchb-accepted-card",
		},
		Message{
			Seq: 3, Direction: "in", Kind: "text", Text: &text,
			ContentHash: "v4-batchb-accepted-text",
		},
	)
	req := communicationV4TurnRequest(t, s, fixture, inbound)
	frozen, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || !frozen.Created ||
		frozen.Turn.Status != DialogueTurnClassified ||
		frozen.Turn.IntentLabel != m5ai.IntentInterested ||
		frozen.Turn.IntentSource != DialogueIntentBusinessEvent ||
		frozen.Application.Outcome.Dialogue != communication.V4DialogueReplyKnownInterested ||
		frozen.Application.Outcome.DialogueStatus != communication.V4DialogueWaitingAdvice ||
		frozen.Application.Outcome.NextAdvice != communication.V4AdviceReply ||
		len(frozen.Application.Outcome.Actions) != 1 ||
		frozen.Application.Outcome.Actions[0].Kind != communication.V4ActionNotifyWechat ||
		!frozen.Aggregate.State.WechatReceiptSent ||
		frozen.Aggregate.State.WechatState != communication.V4WechatExchanged {
		t.Fatalf("交换成功+文字轮应由承接替代固定回执并置位: frozen=%+v err=%v", frozen, err)
	}
	eventActions, err := s.CommunicationV4EventActionsBySource(
		profileID,
		CommunicationV4InputDialogueTurn,
		frozen.Turn.TurnID,
	)
	if err != nil || len(eventActions) != 1 ||
		eventActions[0].V4Kind != communication.V4ActionNotifyWechat ||
		eventActions[0].Status != CommunicationV4EventActionDeferred {
		t.Fatalf("批B交换成功轮只应物化通知动作: actions=%+v err=%v", eventActions, err)
	}
	kind, ok := DialogueTurnInputKindOf(inbound)
	if !ok || kind != DialogueTurnInputWechatCard {
		t.Fatalf("输入形态应判为换微信卡混合: kind=%v ok=%v", kind, ok)
	}
	replayed, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || replayed.Created {
		t.Fatalf("批B交换成功轮重放失败: replayed=%+v err=%v", replayed, err)
	}
}

func TestFreezeCommunicationV4WechatPendingWithTextWaitsAcceptChain(t *testing.T) {
	s := openTest(t)
	profileID := "profile-v4-batchb-pending-text"
	fixture := seedReadyCommunicationTarget(t, s, profileID)
	setCommunicationV4FixedPhrasePackage(t, s, "revision-"+profileID)
	text := "方便加个微信细聊吗"
	requestSourceKey := strings.Repeat("ab", 32)
	inbound := appendCommunicationV4Inbound(t, s, fixture,
		Message{
			Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
			CardState: "pending", ContentHash: "v4-batchb-pending-card",
			SourceKey: &requestSourceKey,
		},
		Message{
			Seq: 3, Direction: "in", Kind: "text", Text: &text,
			ContentHash: "v4-batchb-pending-text",
		},
	)
	req := communicationV4TurnRequest(t, s, fixture, inbound)
	frozen, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || !frozen.Created ||
		frozen.Turn.Status != DialogueTurnCollected ||
		frozen.Turn.IntentLabel != m5ai.IntentInterested ||
		frozen.Turn.IntentSource != DialogueIntentBusinessEvent ||
		frozen.Application.Outcome.Dialogue != communication.V4DialogueWechatContinuation ||
		!frozen.Application.Outcome.DialogueAfterActions ||
		frozen.Application.Outcome.DialogueStatus != communication.V4DialogueWaitingPrerequisite ||
		frozen.Application.Outcome.NextAdvice != communication.V4AdviceNone ||
		len(frozen.Application.Outcome.Actions) != 2 ||
		frozen.Application.Outcome.Actions[0].Kind != communication.V4ActionAcceptWechat ||
		frozen.Application.Outcome.Actions[1].Kind != communication.V4ActionNotifyWechat {
		t.Fatalf("请求卡+文字轮应挂起等待接受链: frozen=%+v err=%v", frozen, err)
	}
	nextAdvice, v4Owned, err := s.CommunicationV4NextAdvice(frozen.Turn.TurnID)
	if err != nil || !v4Owned || nextAdvice != communication.V4AdviceNone {
		t.Fatalf("挂起轮的下一建议应为 none 由接受链接续: advice=%v owned=%v err=%v",
			nextAdvice, v4Owned, err)
	}
	eventActions, err := s.CommunicationV4EventActionsBySource(
		profileID,
		CommunicationV4InputDialogueTurn,
		frozen.Turn.TurnID,
	)
	if err != nil || len(eventActions) != 2 {
		t.Fatalf("批B请求卡轮事件动作未物化: actions=%+v err=%v", eventActions, err)
	}
	var accept *CommunicationV4EventAction
	for index := range eventActions {
		if eventActions[index].V4Kind == communication.V4ActionAcceptWechat {
			accept = &eventActions[index]
		}
	}
	if accept == nil || accept.Status != CommunicationV4EventActionPlanned {
		t.Fatalf("接受动作未按计划物化: actions=%+v", eventActions)
	}
	replayed, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || replayed.Created {
		t.Fatalf("批B请求卡轮重放失败: replayed=%+v err=%v", replayed, err)
	}
}

func TestFreezeCommunicationV4InterviewAcceptedWithTextServiceReplyReplacesReceipt(t *testing.T) {
	s := openTest(t)
	profileID := "profile-v4-batchc-accepted-text"
	fixture := seedReadyCommunicationTarget(t, s, profileID)
	setCommunicationV4FixedPhrasePackage(t, s, "revision-"+profileID)
	candidateText := "合成前置候选人消息"
	appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		Text: &candidateText, ContentHash: "v4-batchc-before-invite",
	})
	at := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := s.ApplyCommunicationV4BusinessEvent(
		ApplyCommunicationV4BusinessEventRequest{
			ProfileID: profileID,
			Event: communication.BusinessEvent{
				Key: "message:2", Kind: communication.EventCandidateExpressionReceived,
				Source: communication.EventSourceMessage, MessageSeq: 2,
				ExpressionKind: communication.ExpressionText, Text: candidateText,
			},
			AppliedAt: at,
		},
	); err != nil {
		t.Fatal(err)
	}
	inviteMessage := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "out", Kind: "card", CardType: "interviewInvite",
		CardState: "pending", ContentHash: "v4-batchc-invite-card", Origin: "self",
		CreatedAt: at.Add(time.Second),
	})[0]
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		_, _, _, err := applyCommunicationV4ConfirmedActionTx(
			tx,
			profileID,
			communication.V4ConfirmedAction{
				ActionKey:  "fixture-batchc-interview-invite",
				Kind:       communication.V4ActionInterviewInvite,
				MessageSeq: inviteMessage.Seq,
				SentAt:     &inviteMessage.CreatedAt,
			},
			inviteMessage.CreatedAt,
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	followText := "请问面试需要准备什么"
	inbound := appendCommunicationV4Inbound(t, s, fixture,
		Message{
			Seq: 4, Direction: "in", Kind: "card", CardType: "interviewInvite",
			CardState: "accepted", ContentHash: "v4-batchc-accepted",
			CreatedAt: at.Add(2 * time.Second),
		},
		Message{
			Seq: 5, Direction: "in", Kind: "text", Text: &followText,
			ContentHash: "v4-batchc-follow-text", CreatedAt: at.Add(3 * time.Second),
		},
	)
	digest, turnID, err := DialogueTurnIdentity(profileID, inviteMessage, inbound, 0)
	if err != nil {
		t.Fatal(err)
	}
	material, ready, err := s.CommunicationAIMaterialForProfile(profileID)
	if err != nil || !ready {
		t.Fatalf("批C轮材料未就绪: ready=%v err=%v", ready, err)
	}
	aggregateRow, err := s.CommunicationV4AggregateByProfile(profileID)
	if err != nil {
		t.Fatal(err)
	}
	req := FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: profileID, ConversationRef: fixture.ConversationRef,
		InputDigest: digest, HistoryThroughSeq: inbound[0].Seq - 1,
		InboundFromSeq: inbound[0].Seq, InboundThroughSeq: inbound[len(inbound)-1].Seq,
		ExpectedProjectedThroughSeq: aggregateRow.ProjectedThroughSeq,
		OutboundAnchorSeq:           inviteMessage.Seq,
		ContextRevisionHash:         material.ContextRevision.RevisionHash,
		ResumeSnapshotID:            material.ResumeSnapshot.SnapshotID,
		RecommendedTimeText:         "合成推荐时段",
		RenderFormatVersion:         m5ai.DialogueRenderFormatVersion,
		FrozenAt:                    at.Add(4 * time.Second),
	}
	frozen, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || !frozen.Created ||
		frozen.Turn.Status != DialogueTurnCollected ||
		frozen.Application.Outcome.Dialogue != communication.V4DialogueServiceReply ||
		frozen.Application.Outcome.DialogueStatus != communication.V4DialogueWaitingPrerequisite ||
		frozen.Application.Outcome.NextAdvice != communication.V4AdviceNone ||
		!frozen.Application.Outcome.DialogueAfterActions ||
		len(frozen.Application.Outcome.Actions) != 3 ||
		frozen.Application.Outcome.Actions[0].Kind != communication.V4ActionInterviewAcceptedReceipt ||
		frozen.Application.Outcome.Actions[1].Kind != communication.V4ActionNotifyInterviewAccepted ||
		frozen.Application.Outcome.Actions[2].Kind != communication.V4ActionInviteWechat ||
		frozen.Aggregate.State.MainStatus != communication.V4StatusInterviewed ||
		frozen.Aggregate.State.InterviewAcceptedReceiptSent {
		t.Fatalf("邀面接受+文字轮应保留固定段并等待补句前置(2026-07-31 规格): frozen=%+v err=%v", frozen, err)
	}
	eventActions, err := s.CommunicationV4EventActionsBySource(
		profileID,
		CommunicationV4InputDialogueTurn,
		frozen.Turn.TurnID,
	)
	if err != nil || len(eventActions) != 3 ||
		eventActions[0].V4Kind != communication.V4ActionInterviewAcceptedReceipt ||
		eventActions[1].V4Kind != communication.V4ActionNotifyInterviewAccepted ||
		eventActions[2].V4Kind != communication.V4ActionInviteWechat {
		t.Fatalf("批C轮应物化固定段回执、约面通知与追邀卡: actions=%+v err=%v", eventActions, err)
	}
	kind, ok := DialogueTurnInputKindOf(inbound)
	if !ok || kind != DialogueTurnInputInterviewAccepted {
		t.Fatalf("输入形态应判为邀面接受混合: kind=%v ok=%v", kind, ok)
	}
	replayed, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || replayed.Created {
		t.Fatalf("批C轮重放失败: replayed=%+v err=%v", replayed, err)
	}

	// 固定段收束链(2026-07-31 规格 §五(三)):回执气泡先终局,turn 仍等
	// 前置;收尾的换微信邀请终局后,演进投影落在收尾动作上、turn 推进到
	// 一次 serviceReply 建议。
	settleServiceSegmentAction := func(index int, seq int64) {
		t.Helper()
		target := eventActions[index]
		text := target.Text
		draftKind, cardType, cardState := "text", "", ""
		if target.V4Kind == communication.V4ActionInviteWechat {
			draftKind, cardType, cardState = "card", "wechatExchange", "pending"
			text = "换微信邀请"
		}
		latest, err := s.MessagesForConversation(ConversationKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ConversationRef: fixture.ConversationRef,
		})
		if err != nil || len(latest) == 0 {
			t.Fatalf("固定段账本不可用: err=%v", err)
		}
		intentID, err := M5AutomaticIntentID(target.ActionID)
		if err != nil {
			t.Fatal(err)
		}
		primitive := "chat.sendMessage"
		if target.V4Kind == communication.V4ActionInviteWechat {
			primitive = "chat.sendWechatInvite"
		}
		changes, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
			Key: ConversationKey{
				Platform: fixture.Platform, AccountRef: fixture.AccountRef,
				ConversationRef: fixture.ConversationRef,
			},
			ExpectedTailSeq: latest[len(latest)-1].Seq,
			NewMessages: []MessageDraft{{
				Direction: "out", Kind: draftKind, ContentHash: target.ContentHash,
				Text: &text, CardType: cardType, CardState: cardState,
				Origin: "self",
			}},
			SyncedAt: at.Add(time.Duration(10+index) * time.Second),
		})
		if err != nil || len(changes.Inserted) != 1 || changes.Inserted[0].Seq != seq {
			t.Fatalf("固定段消息入账失败: changes=%+v err=%v", changes, err)
		}
		resultSeq := changes.Inserted[0].Seq
		if err := s.db.Model(&Message{}).
			Where(
				"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
				fixture.Platform, fixture.AccountRef, fixture.ConversationRef, resultSeq,
			).
			Update("outbound_intent_id", intentID).Error; err != nil {
			t.Fatalf("固定段消息关联发送意图失败: %v", err)
		}
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			startedAt := at.Add(time.Duration(9+index) * time.Second)
			if err := tx.Model(&CommunicationV4EventAction{}).
				Where("action_id = ?", target.ActionID).
				Updates(map[string]any{
					"effect_intent_id":  intentID,
					"effect_started_at": startedAt,
					"status":            CommunicationV4EventActionEffectPending,
				}).Error; err != nil {
				return err
			}
			var refreshed CommunicationV4EventAction
			if err := tx.First(&refreshed, "action_id = ?", target.ActionID).Error; err != nil {
				return err
			}
			intent := EffectIntent{
				IntentID: intentID, IdemKey: "idem-" + intentID, Platform: fixture.Platform,
				AccountRef: fixture.AccountRef, Primitive: primitive,
				TargetRef: fixture.ConversationRef, PayloadHash: strings.Repeat("a", 64),
				GuardsHash: strings.Repeat("b", 64), RootMsgID: "root-" + intentID,
				Status: EffectIntentOk, DeadlineMs: 1, ResultMessageSeq: &resultSeq,
				SendFingerprint: target.ContentHash,
			}
			return applyCommunicationV4EventActionEffectStatusTx(
				tx, refreshed, &intent, at.Add(time.Duration(11+index)*time.Second),
			)
		}); err != nil {
			t.Fatalf("固定段动作[%d]终局失败: %v", index, err)
		}
	}

	settleServiceSegmentAction(0, inbound[len(inbound)-1].Seq+1)
	afterReceipt, err := s.DialogueTurnByID(frozen.Turn.TurnID)
	if err != nil || afterReceipt == nil || afterReceipt.Status != DialogueTurnCollected {
		t.Fatalf("回执气泡终局后补句仍须等待收尾动作: turn=%+v err=%v", afterReceipt, err)
	}
	settleServiceSegmentAction(2, inbound[len(inbound)-1].Seq+2)
	afterInvite, err := s.DialogueTurnByID(frozen.Turn.TurnID)
	if err != nil || afterInvite == nil || afterInvite.Status != DialogueTurnClassified {
		t.Fatalf("收尾动作终局后应推进到一次补句建议: turn=%+v err=%v", afterInvite, err)
	}
	var continuationRow CommunicationV4ProjectionApplication
	if err := s.db.First(
		&continuationRow,
		"profile_id = ? AND input_kind = ? AND input_key = ?",
		profileID,
		CommunicationV4InputConfirmedAction,
		eventActions[2].SemanticActionKey,
	).Error; err != nil ||
		continuationRow.Outcome.Dialogue != communication.V4DialogueServiceReply ||
		continuationRow.Outcome.DialogueStatus != communication.V4DialogueWaitingAdvice ||
		continuationRow.Outcome.NextAdvice != communication.V4AdviceServiceReply ||
		continuationRow.Outcome.IntentLabel != "" {
		t.Fatalf("收尾动作未形成补句授权投影: row=%+v err=%v", continuationRow, err)
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		head, found, err := communicationV4TurnHeadApplicationTx(tx, *afterInvite)
		if err != nil || !found ||
			head.InputKind != CommunicationV4InputConfirmedAction ||
			head.Outcome.NextAdvice != communication.V4AdviceServiceReply {
			return fmt.Errorf("head 链未演进到补句授权: head=%+v found=%v err=%v", head, found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDialogueTurnCurrentToleratesExchangeResultCardAfterTurn(t *testing.T) {
	s := openTest(t)
	profileID := "profile-v4-batchb-late-259"
	fixture := seedReadyCommunicationTarget(t, s, profileID)
	setCommunicationV4FixedPhrasePackage(t, s, "revision-"+profileID)
	text := "加个微信详聊"
	requestSourceKey := strings.Repeat("cd", 32)
	inbound := appendCommunicationV4Inbound(t, s, fixture,
		Message{
			Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
			CardState: "pending", ContentHash: "v4-late-259-card",
			SourceKey: &requestSourceKey,
		},
		Message{
			Seq: 3, Direction: "in", Kind: "text", Text: &text,
			ContentHash: "v4-late-259-text",
		},
	)
	req := communicationV4TurnRequest(t, s, fixture, inbound)
	frozen, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || !frozen.Created {
		t.Fatalf("请求卡+文字轮冻结失败: %+v err=%v", frozen, err)
	}
	// 形态 A 定向重对账把接受产生的交换结果卡(259/出站)当轮收进账本。
	appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 4, Direction: "out", Kind: "card", CardType: "wechatExchange",
		CardState: "accepted", ContentHash: "v4-late-259-result", Origin: "self",
	})
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return validateDialogueTurnCurrentTx(tx, frozen.Turn)
	})
	if err != nil {
		t.Fatalf("轮后交换结果卡不得作废承接轮: err=%v", err)
	}
	// 2026-08-27 停机点第二步:AI 边界中间新鲜度重验已删除,真人出站不再
	// 在推进途中作废本轮——防线移到派发前的账本尾终检(链首平价闸拒绝后
	// 按新失败方向作废本批下轮重开),下一轮边界现算把真人行收为新锚。
	human := "我手动回了一句"
	appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 5, Direction: "out", Kind: "text", Text: &human,
		ContentHash: "v4-late-human-text", Origin: "external",
	})
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return validateDialogueTurnCurrentTx(tx, frozen.Turn)
	})
	if err != nil {
		t.Fatalf("中游重验删除后真人出站不得中途作废本轮: err=%v", err)
	}
}
