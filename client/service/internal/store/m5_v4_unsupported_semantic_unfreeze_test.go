package store

import (
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"
)

// 手工重建批 A 激活前的真实冻结账本形状(对照生产库 2026-07-28 个案):
// 招呼 seq1 + 文字 seq2 + 在线简历卡 seq3 + 文字 seq4,轮回执 outcome 为
// manualRequired/unsupportedSemantic,聚合 revision=1 冻结。
func seedFrozenUnsupportedSemanticResumeMix(t *testing.T, s *Store) (string, string) {
	t.Helper()
	at := time.Date(2026, 7, 28, 3, 47, 55, 0, time.UTC)
	displayName := "候选人乙"
	profileID := "profile-unfreeze-mix"
	platform, accountRef := "zhilian", "acct-unfreeze"
	conversationRef := "conv-unfreeze-mix"
	platformUserRef := "person-unfreeze-mix"
	if err := s.db.Create(&Candidate{
		Platform: platform, PlatformUserRef: platformUserRef, DisplayName: &displayName,
		FirstSeenAt: at, LastSeenAt: at,
	}).Error; err != nil {
		t.Fatal(err)
	}
	greetingIntentID := "intent-unfreeze-root"
	backendJobID := "job-unfreeze"
	snapshotID := "snapshot-unfreeze"
	logicalID := "logical-unfreeze"
	if err := s.db.Create(&CandidateProfile{
		ProfileID: profileID, Platform: platform, AccountRef: accountRef,
		PlatformUserRef: platformUserRef, PositionRef: "position-unfreeze",
		MainStatus:                     CandidateProfileCommunicating,
		SuccessfulGreetingIntentID:     &greetingIntentID,
		ConversationRef:                &conversationRef,
		BackendJobID:                   &backendJobID,
		ResumeCaptureState:             ResumeCaptureCaptured,
		ResumeCaptureLogicalDispatchID: &logicalID,
		ActiveResumeSnapshotID:         &snapshotID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&CandidateResumeSnapshot{
		SnapshotID: snapshotID, ProfileID: profileID,
		SourceKind: resumeSnapshotSourceIM, SourceConversationRef: conversationRef,
		SourceLogicalDispatchID: logicalID,
		ObservedAt:              at.Add(-time.Hour).UnixMilli(), CapturedAt: at.Add(-time.Hour),
		SchemaVersion: resumeSnapshotSchemaV1,
		ContentHash:   "resume-content-unfreeze", ResumeJSON: `{"basic":[]}`,
		CreatedAt: at.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	contextRevision := contextRevisionFixture("ctx-unfreeze", "revision-unfreeze", at.Add(-time.Hour))
	contextRevision.SourceKind = legacyJobConfigSourceKind
	contextRevision.SourceJobRef = backendJobID
	if _, err := s.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{contextRevision}, at.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	adoptedAt := at.Add(-2 * time.Hour)
	if err := s.db.Create(&TrackedIntent{
		Platform: platform, AccountRef: accountRef, ConversationRef: conversationRef,
		Status: TrackingAdopted, RequestedBy: "fixture", RequestedAt: adoptedAt, AdoptedAt: &adoptedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&Conversation{
		Platform: platform, AccountRef: accountRef, ConversationRef: conversationRef,
		PlatformUserRef: platformUserRef, TrackingState: TrackingAdopted,
		AdoptedBoundarySeq: 1, LastMessageSeq: 4,
	}).Error; err != nil {
		t.Fatal(err)
	}
	greeting := "招呼正文"
	first := "请问做几休几"
	second := "工作时间是"
	ts := at.Add(-time.Minute).UnixMilli()
	rows := []Message{
		{Platform: platform, AccountRef: accountRef, ConversationRef: conversationRef,
			Seq: 1, Direction: "out", Kind: "text", ContentHash: "greeting-hash", Text: &greeting, Origin: "self"},
		{Platform: platform, AccountRef: accountRef, ConversationRef: conversationRef,
			Seq: 2, Direction: "in", Kind: "text", ContentHash: "first-hash", Text: &first, Origin: "external", TsApproxMs: &ts},
		{Platform: platform, AccountRef: accountRef, ConversationRef: conversationRef,
			Seq: 3, Direction: "in", Kind: "card", ContentHash: "resume-hash",
			CardType: "resumeAttachment", CardState: "unknown", Origin: "external", TsApproxMs: &ts},
		{Platform: platform, AccountRef: accountRef, ConversationRef: conversationRef,
			Seq: 4, Direction: "in", Kind: "text", ContentHash: "second-hash", Text: &second, Origin: "external", TsApproxMs: &ts},
	}
	for index := range rows {
		if err := s.db.Create(&rows[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	digest, turnID, err := DialogueTurnIdentity(profileID, rows[0], rows[1:], 0)
	if err != nil {
		t.Fatal(err)
	}
	recommended, err := m5ai.FreezeRecommendedTimeText(at, m5ai.GenerateDefaultSlots(at))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&DialogueTurn{
		TurnID: turnID, ProfileID: profileID, ConversationRef: conversationRef,
		InputDigest: digest, HistoryThroughSeq: 1, InboundFromSeq: 2, InboundThroughSeq: 4,
		ContextRevisionHash: "revision-unfreeze", ResumeSnapshotID: "snapshot-unfreeze",
		RecommendedTimeText: recommended, RenderFormatVersion: m5ai.DialogueRenderFormatVersion,
		Status: DialogueTurnManualRequired, FailureReason: v4UnsupportedSemanticReason,
	}).Error; err != nil {
		t.Fatal(err)
	}
	outboundAt := at.Add(-5 * time.Minute)
	frozenState := communication.V4State{
		MainStatus:          communication.V4StatusCommunicating,
		WechatState:         communication.V4WechatNotInvited,
		ColdPromptRemaining: 2, ColdWechatRemaining: 1,
		RealMessageRound: 2, LastRealMessageSeq: 4, LastOutboundMessageSeq: 1,
		LastOutboundAt: &outboundAt, LastBodyAt: &outboundAt,
	}
	if err := s.db.Create(&CommunicationV4Aggregate{
		ProfileID: profileID, RootGreetingIntentID: "intent-unfreeze-root",
		StateSchemaVersion: communicationV4StateSchemaVersion,
		Revision:           1, ProjectedThroughSeq: 4, State: frozenState,
		AutomationStatus: ProfileCommunicationAutomationManualRequired,
		ManualReason:     v4UnsupportedSemanticReason, ManualRequiredAt: &at,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&CommunicationV4ProjectionApplication{
		ProfileID: profileID, InputKind: CommunicationV4InputDialogueTurn, InputKey: turnID,
		InputDigest: digest, SemanticKind: communicationV4DialogueTurnSemanticKind,
		MessageSeq: 4, FromRevision: 0, ToRevision: 1,
		Outcome: CommunicationV4ApplicationOutcome{
			Dialogue:       communication.V4DialogueNone,
			ManualReason:   communication.V4ManualUnsupportedSemantic,
			DialogueStatus: communication.V4DialogueManualRequired,
			NextAdvice:     communication.V4AdviceNone,
		},
		AppliedAt: at,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return profileID, turnID
}

func TestUnfreezeV4UnsupportedSemanticResumeMixRestoresWaitingReply(t *testing.T) {
	s := openTest(t)
	profileID, turnID := seedFrozenUnsupportedSemanticResumeMix(t, s)

	results, err := s.UnfreezeV4UnsupportedSemanticProfiles()
	if err != nil || len(results) != 1 || !results[0].Unfrozen ||
		results[0].ProfileID != profileID || results[0].TurnID != turnID {
		t.Fatalf("解冻结果错误: results=%+v err=%v", results, err)
	}
	turn, err := s.DialogueTurnByID(turnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnCollected || turn.FailureReason != "" {
		t.Fatalf("turn 未回 collected: turn=%+v err=%v", turn, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(profileID)
	if err != nil || aggregate == nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" || aggregate.ManualRequiredAt != nil ||
		aggregate.Revision != 2 || aggregate.ProjectedThroughSeq != 4 ||
		aggregate.State.RealMessageRound != 2 || aggregate.State.LastRealMessageSeq != 4 {
		t.Fatalf("聚合未回 active 或状态被改写: aggregate=%+v err=%v", aggregate, err)
	}
	next, owned, err := s.CommunicationV4NextAdvice(turnID)
	if err != nil || !owned || next != communication.V4AdviceReply {
		t.Fatalf("解冻链环未把 head 演进为等待回复建议: next=%v owned=%v err=%v", next, owned, err)
	}
	var audits int64
	if err := s.db.Model(&AuditEntry{}).
		Where("category = ?", v4UnsupportedSemanticUnfreezeAuditCategory).
		Count(&audits).Error; err != nil || audits != 1 {
		t.Fatalf("解冻审计缺失: audits=%d err=%v", audits, err)
	}

	replayed, err := s.UnfreezeV4UnsupportedSemanticProfiles()
	if err != nil || len(replayed) != 0 {
		t.Fatalf("解冻重跑必须为空: results=%+v err=%v", replayed, err)
	}
}

// 直击 2026-07-28 真机回归:解冻后分类、预约并完成回复建议的持久化全链
// 必须走通——对话要求必须以回执链 head(含 manualUnfreeze 链环)为准,而
// 不是原始轮回执里冻结的 none。
func TestUnfreezeThenReplyCompletionPersistsPlannedAction(t *testing.T) {
	s := openTest(t)
	profileID, turnID := seedFrozenUnsupportedSemanticResumeMix(t, s)
	if results, err := s.UnfreezeV4UnsupportedSemanticProfiles(); err != nil ||
		len(results) != 1 || !results[0].Unfrozen {
		t.Fatalf("解冻失败: results=%+v err=%v", results, err)
	}
	now := time.Date(2026, 7, 28, 7, 30, 0, 0, time.UTC)
	if _, err := s.ApplyResumeBusinessClassification(turnID, now); err != nil {
		t.Fatalf("businessEvent 分类失败: %v", err)
	}
	if turnMid, err := s.DialogueTurnByID(turnID); err != nil || turnMid.Status != DialogueTurnClassified {
		t.Fatalf("分类后中间态: turn=%+v err=%v", turnMid, err)
	}
	if next, owned, err := s.CommunicationV4NextAdvice(turnID); err != nil || !owned || next != communication.V4AdviceReply {
		t.Fatalf("分类后 head 建议: next=%v owned=%v err=%v", next, owned, err)
	}
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-unfreeze-reply", TurnID: turnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "fake-provider", Model: "fake-model", InputHash: "input-hash",
		CreatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("预约回复调用失败: %v", err)
	}
	text := "简历收到了，工作时间我们详细说下。"
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: AIInvocationCompletion{
			InvocationID: "invocation-unfreeze-reply", Status: AIInvocationOK,
			OutputHash: "output-hash", UsageShape: AIInvocationReasoningFieldAbsent, ReasoningContentEmpty: true,
			LatencyMs: 10, FinishedAt: now.Add(2 * time.Second),
		},
		ActionID: "action-unfreeze-reply", Phrases: []string{text}, Text: text,
		ContentHash: textcanon.Hash(text), PlannedAt: now.Add(2 * time.Second),
	})
	if err != nil || action == nil || action.Status != CommunicationActionPlanned ||
		action.Kind != CommunicationActionReplyText || action.Text != text {
		debugTurn, _ := s.DialogueTurnByID(turnID)
		t.Fatalf("回复建议持久化未产出 planned 动作: action=%+v err=%v turnStatus=%s failureReason=%s",
			action, err, debugTurn.Status, debugTurn.FailureReason)
	}
	turn, err := s.DialogueTurnByID(turnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnAdviceReady {
		t.Fatalf("turn 未停在 adviceReady: turn=%+v err=%v", turn, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(profileID)
	if err != nil || aggregate == nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.Revision != 3 {
		t.Fatalf("advice 回执未接在解冻链环之后: aggregate=%+v err=%v", aggregate, err)
	}
}

// 直击 2026-07-28 启动阻断回归:聚合已被巡检隔离(原因先到先得)+ 该轮
// 存在未终局 reply 调用时,启动收束必须诚实终局调用与轮并继续启动,不改
// 写聚合原因,更不能 exit。
func TestRecoverInterruptedInvocationOnQuarantinedAggregateKeepsBooting(t *testing.T) {
	s := openTest(t)
	profileID, turnID := seedFrozenUnsupportedSemanticResumeMix(t, s)
	if results, err := s.UnfreezeV4UnsupportedSemanticProfiles(); err != nil ||
		len(results) != 1 || !results[0].Unfrozen {
		t.Fatalf("解冻失败: results=%+v err=%v", results, err)
	}
	now := time.Date(2026, 7, 28, 7, 30, 0, 0, time.UTC)
	if _, err := s.ApplyResumeBusinessClassification(turnID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-quarantine-boot", TurnID: turnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "fake-provider", Model: "fake-model", InputHash: "input-hash",
		CreatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	quarantineReason := "patrolQuarantine:communicationV4Conflict"
	quarantinedAt := now.Add(2 * time.Second)
	if err := s.db.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ?", profileID).
		Updates(map[string]any{
			"automation_status": ProfileCommunicationAutomationManualRequired,
			"manual_reason":     quarantineReason, "manual_required_at": quarantinedAt,
		}).Error; err != nil {
		t.Fatal(err)
	}

	recovered, err := s.RecoverInterruptedAIInvocations(now.Add(time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("启动收束在隔离现场必须成功: recovered=%d err=%v", recovered, err)
	}
	var invocation AIInvocation
	if err := s.db.First(&invocation, "invocation_id = ?", "invocation-quarantine-boot").Error; err != nil ||
		invocation.FinishedAt == nil || invocation.Status != AIInvocationTransportFailed ||
		invocation.ErrorClass != "processInterrupted" {
		t.Fatalf("中断调用未诚实终局: invocation=%+v err=%v", invocation, err)
	}
	turn, err := s.DialogueTurnByID(turnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnManualRequired ||
		turn.FailureReason != "replyProcessInterrupted" {
		t.Fatalf("悬轮未交给人工: turn=%+v err=%v", turn, err)
	}
	var aggregate CommunicationV4Aggregate
	if err := s.db.First(&aggregate, "profile_id = ?", profileID).Error; err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != quarantineReason {
		t.Fatalf("隔离原因不得被收束改写: aggregate=%+v err=%v", aggregate, err)
	}

	again, err := s.RecoverInterruptedAIInvocations(now.Add(2 * time.Minute))
	if err != nil || again != 0 {
		t.Fatalf("收束重跑必须为空: recovered=%d err=%v", again, err)
	}
}

func TestUnfreezeV4UnsupportedSemanticKeepsNonResumeShapesFrozen(t *testing.T) {
	s := openTest(t)
	profileID, turnID := seedFrozenUnsupportedSemanticResumeMix(t, s)
	// 把简历卡改造成换微信请求卡:同样被 unsupportedSemantic 冻住,但批 A
	// 不授权解冻这种形态,必须原样保留并报告原因。
	if err := s.db.Model(&Message{}).
		Where("conversation_ref = ? AND seq = 3", "conv-unfreeze-mix").
		Updates(map[string]any{"card_type": "wechatExchange", "card_state": "pending"}).Error; err != nil {
		t.Fatal(err)
	}

	results, err := s.UnfreezeV4UnsupportedSemanticProfiles()
	if err != nil || len(results) != 1 || results[0].Unfrozen ||
		results[0].SkipReason != "inputShapeStillUnsupported" {
		t.Fatalf("非简历形态不得解冻: results=%+v err=%v", results, err)
	}
	turn, err := s.DialogueTurnByID(turnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnManualRequired ||
		turn.FailureReason != v4UnsupportedSemanticReason {
		t.Fatalf("skip 分支不得触碰 turn: turn=%+v err=%v", turn, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(profileID)
	if err != nil || aggregate == nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		aggregate.Revision != 1 {
		t.Fatalf("skip 分支不得触碰聚合: aggregate=%+v err=%v", aggregate, err)
	}
	next, owned, err := s.CommunicationV4NextAdvice(turnID)
	if err != nil || !owned || next != communication.V4AdviceNone {
		t.Fatalf("skip 分支 head 不得演进: next=%v owned=%v err=%v", next, owned, err)
	}
}
