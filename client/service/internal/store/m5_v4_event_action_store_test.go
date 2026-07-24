package store

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"

	"gorm.io/gorm"
)

func communicationV4EventActionContextFixture(
	contextID string,
	revisionHash string,
	wechatReceipt string,
	interviewReceipt string,
	at time.Time,
) m5ai.ContextRevision {
	revision := contextRevisionFixture(contextID, revisionHash, at)
	revision.SourcePackage.Documents = append(
		revision.SourcePackage.Documents,
		m5ai.JobConfigDocument{
			DocType: "固定话术",
			Content: `{
				"wechatAccepted":{"message":"` + wechatReceipt + `","messages":["` + wechatReceipt + `"],"actions":[],"enabled":true},
				"meetingAccepted":{"message":"` + interviewReceipt + `","messages":["` + interviewReceipt + `"],"actions":[],"enabled":true}
			}`,
		},
	)
	sort.Slice(revision.SourcePackage.Documents, func(left, right int) bool {
		return revision.SourcePackage.Documents[left].DocType < revision.SourcePackage.Documents[right].DocType
	})
	return revision
}

func bindCommunicationV4EventActionContext(
	t *testing.T,
	s *Store,
	profileID string,
	revision m5ai.ContextRevision,
	at time.Time,
) {
	t.Helper()
	revision.SourceKind = legacyJobConfigSourceKind
	revision.SourceJobRef = "job-" + profileID
	if _, err := s.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{revision},
		at,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).
		Where("profile_id = ?", profileID).
		UpdateColumn("backend_job_id", revision.SourceJobRef).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindActiveM5TrialProfileAIContext(BindProfileAIContextRequest{
		BindingID: "binding-" + profileID + "-" + revision.RevisionHash,
		ProfileID: profileID, ContextID: revision.ContextID,
		RevisionHash: revision.RevisionHash,
		Reason:       "test", BoundBy: "test", BoundAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}

func createCommunicationV4BusinessEventApplication(
	t *testing.T,
	s *Store,
	profileID string,
	inputKey string,
	revision uint64,
	actions []communication.V4EventAction,
	at time.Time,
) {
	t.Helper()
	application := CommunicationV4ProjectionApplication{
		ProfileID: profileID, InputKind: CommunicationV4InputBusinessEvent, InputKey: inputKey,
		InputDigest: strings.Repeat("a", 64), SemanticKind: "fixtureEvent",
		MessageSeq:   actions[0].CardMessageSeq,
		FromRevision: revision - 1, ToRevision: revision,
		Outcome: CommunicationV4ApplicationOutcome{
			Dialogue: communication.V4DialogueNone,
			Actions:  actions,
		},
		AppliedAt: at,
	}
	if err := s.db.Create(&application).Error; err != nil {
		t.Fatal(err)
	}
}

func TestCommunicationV4EventActionAutoMigrateAndScopedID(t *testing.T) {
	s := openTest(t)
	if !s.db.Migrator().HasTable(&CommunicationV4EventAction{}) {
		t.Fatal("AutoMigrate 未创建 V4 事件动作表")
	}
	for _, field := range []string{
		"action_id", "profile_id", "source_input_kind", "source_input_key",
		"semantic_action_key", "v4_kind", "effect_kind", "status",
	} {
		if !s.db.Migrator().HasColumn(&CommunicationV4EventAction{}, field) {
			t.Fatalf("AutoMigrate 缺少 V4 事件动作列 %s", field)
		}
	}

	first, err := CommunicationV4EventActionID("profile-a", "message:2|wechatReceipt")
	if err != nil {
		t.Fatal(err)
	}
	repeated, _ := CommunicationV4EventActionID("profile-a", "message:2|wechatReceipt")
	isolated, _ := CommunicationV4EventActionID("profile-b", "message:2|wechatReceipt")
	if first != repeated || first == isolated || len(first) != 64 || first != strings.ToLower(first) {
		t.Fatalf("ActionID 未按 profile scope 形成稳定 SHA-256: first=%q repeated=%q isolated=%q",
			first, repeated, isolated)
	}
}

func TestCommunicationV4EventActionMaterializesSixKindsAndFreezesText(t *testing.T) {
	s := openTest(t)
	profileID := "profile-v4-event-action-six"
	seedResumeStoreFixture(t, s, profileID)
	at := time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)
	firstRevision := communicationV4EventActionContextFixture(
		"context-v4-event-action",
		"revision-v4-event-action-one",
		"好的，晚点加你",
		"面试安排已确认",
		at,
	)
	bindCommunicationV4EventActionContext(t, s, profileID, firstRevision, at)

	wechatSource := "message:20"
	createCommunicationV4BusinessEventApplication(
		t,
		s,
		profileID,
		wechatSource,
		1,
		[]communication.V4EventAction{
			{ActionKey: wechatSource + "|acceptWechat", Kind: communication.V4ActionAcceptWechat, CardMessageSeq: 20},
			{ActionKey: wechatSource + "|notifyWechat", Kind: communication.V4ActionNotifyWechat, CardMessageSeq: 20},
			{ActionKey: wechatSource + "|wechatReceipt", Kind: communication.V4ActionWechatReceipt, CardMessageSeq: 20},
		},
		at,
	)
	wechatResult, err := s.MaterializeCommunicationV4EventActions(
		MaterializeCommunicationV4EventActionsRequest{
			ProfileID: profileID, SourceInputKey: wechatSource, MaterializedAt: at.Add(time.Minute),
		},
	)
	if err != nil || !wechatResult.Created || len(wechatResult.Actions) != 3 {
		t.Fatalf("微信事件动作未完整物化: result=%+v err=%v", wechatResult, err)
	}
	if wechatResult.Actions[0].Status != CommunicationV4EventActionDeferred ||
		wechatResult.Actions[0].FailureReason != CommunicationV4EventActionFailurePrimitiveUnavailable ||
		wechatResult.Actions[1].Status != CommunicationV4EventActionDeferred ||
		wechatResult.Actions[1].FailureReason != CommunicationV4EventActionFailureNotificationChannelDeferred {
		t.Fatalf("未开放能力未显式 deferred: %+v", wechatResult.Actions)
	}
	wechatReceipt := wechatResult.Actions[2]
	if wechatReceipt.EffectKind != CommunicationV4EventEffectReplyText ||
		wechatReceipt.Status != CommunicationV4EventActionPlanned ||
		wechatReceipt.Text != "好的，晚点加你" ||
		wechatReceipt.ContentHash != textcanon.Hash(wechatReceipt.Text) ||
		wechatReceipt.ContextRevisionHash != firstRevision.RevisionHash {
		t.Fatalf("微信回执未冻结话术与 context revision: %+v", wechatReceipt)
	}

	interviewSource := "message:30"
	createCommunicationV4BusinessEventApplication(
		t,
		s,
		profileID,
		interviewSource,
		2,
		[]communication.V4EventAction{
			{ActionKey: interviewSource + "|interviewAcceptedReceipt", Kind: communication.V4ActionInterviewAcceptedReceipt, CardMessageSeq: 30},
			{ActionKey: interviewSource + "|notifyInterviewAccepted", Kind: communication.V4ActionNotifyInterviewAccepted, CardMessageSeq: 30},
			{ActionKey: interviewSource + "|inviteWechat", Kind: communication.V4ActionInviteWechat, CardMessageSeq: 30},
		},
		at.Add(2*time.Minute),
	)
	interviewResult, err := s.MaterializeCommunicationV4EventActions(
		MaterializeCommunicationV4EventActionsRequest{
			ProfileID: profileID, SourceInputKey: interviewSource, MaterializedAt: at.Add(3 * time.Minute),
		},
	)
	if err != nil || !interviewResult.Created || len(interviewResult.Actions) != 3 {
		t.Fatalf("邀面接受事件动作未完整物化: result=%+v err=%v", interviewResult, err)
	}
	interviewReceipt := interviewResult.Actions[0]
	inviteWechat := interviewResult.Actions[2]
	if interviewReceipt.Text != "面试安排已确认" ||
		inviteWechat.EffectKind != CommunicationV4EventEffectInviteWechat ||
		inviteWechat.Status != CommunicationV4EventActionPlanned ||
		inviteWechat.ContentHash != communicationWechatInviteContentHash() ||
		inviteWechat.DependsOnActionID == nil ||
		*inviteWechat.DependsOnActionID != interviewReceipt.ActionID {
		t.Fatalf("回执→换微信依赖未冻结: receipt=%+v invite=%+v", interviewReceipt, inviteWechat)
	}

	profileActions, err := s.CommunicationV4EventActionsByProfile(profileID)
	if err != nil || len(profileActions) != 6 {
		t.Fatalf("按 profile 查询未返回六类动作: actions=%+v err=%v", profileActions, err)
	}
	sourceActions, err := s.CommunicationV4EventActionsBySource(
		profileID,
		CommunicationV4InputBusinessEvent,
		interviewSource,
	)
	if err != nil || len(sourceActions) != 3 ||
		sourceActions[0].SourceOrdinal != 0 || sourceActions[2].SourceOrdinal != 2 {
		t.Fatalf("按 source 查询未保持 outcome 顺序: actions=%+v err=%v", sourceActions, err)
	}

	secondRevision := communicationV4EventActionContextFixture(
		firstRevision.ContextID,
		"revision-v4-event-action-two",
		"新版微信回执",
		"新版面试回执",
		at.Add(time.Hour),
	)
	bindCommunicationV4EventActionContext(t, s, profileID, secondRevision, at.Add(time.Hour))
	replayed, err := s.MaterializeCommunicationV4EventActions(
		MaterializeCommunicationV4EventActionsRequest{
			ProfileID: profileID, SourceInputKey: interviewSource, MaterializedAt: at.Add(2 * time.Hour),
		},
	)
	if err != nil || replayed.Created || len(replayed.Actions) != 3 ||
		replayed.Actions[0].Text != "面试安排已确认" ||
		replayed.Actions[0].ContextRevisionHash != firstRevision.RevisionHash ||
		!replayed.Actions[0].PlannedAt.Equal(at.Add(3*time.Minute)) {
		t.Fatalf("配置改版后的重放改写了冻结事实: result=%+v err=%v", replayed, err)
	}
}

func TestCommunicationV4EventActionLegacyDialogueReceiptKeepsSingleOwner(t *testing.T) {
	s := openTest(t)
	profileID := "profile-v4-event-action-legacy-dialogue"
	seedResumeStoreFixture(t, s, profileID)
	at := time.Date(2026, 7, 24, 10, 30, 0, 0, time.UTC)
	turnID := "turn-v4-event-action-legacy-dialogue"
	receiptKey := "message:8|interviewAcceptedReceipt"
	application := CommunicationV4ProjectionApplication{
		ProfileID: profileID, InputKind: CommunicationV4InputDialogueTurn,
		InputKey: turnID, InputDigest: strings.Repeat("b", 64),
		SemanticKind: communicationV4DialogueTurnSemanticKind,
		MessageSeq:   8, FromRevision: 0, ToRevision: 1,
		Outcome: CommunicationV4ApplicationOutcome{
			Dialogue:       communication.V4DialogueNone,
			DialogueStatus: communication.V4DialogueActionsPlanned,
			Actions: []communication.V4EventAction{
				{
					ActionKey:      receiptKey,
					Kind:           communication.V4ActionInterviewAcceptedReceipt,
					CardMessageSeq: 8,
				},
				{
					ActionKey:      "message:8|notifyInterviewAccepted",
					Kind:           communication.V4ActionNotifyInterviewAccepted,
					CardMessageSeq: 8,
				},
				{
					ActionKey:      "message:8|inviteWechat",
					Kind:           communication.V4ActionInviteWechat,
					CardMessageSeq: 8,
				},
			},
			PlannedActions: []communication.V4PlannedAction{{
				ActionKey:      receiptKey,
				Kind:           communication.V4ActionInterviewAcceptedReceipt,
				CardMessageSeq: 8,
			}},
		},
		AppliedAt: at,
	}
	receiptText := "历史面试接受回执"
	legacyAction := CommunicationAction{
		ActionID: receiptKey, TurnID: turnID, Kind: CommunicationActionReplyText,
		Text: receiptText, ContentHash: textcanon.Hash(receiptText),
		Status:    CommunicationActionPlanned,
		PlannedAt: at, CreatedAt: at, UpdatedAt: at,
	}
	if err := s.db.Create(&legacyAction).Error; err != nil {
		t.Fatal(err)
	}
	var first []CommunicationV4EventAction
	var created bool
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		first, created, err = materializeCommunicationV4EventActionsTx(
			tx,
			application,
			at,
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !created || len(first) != 3 {
		t.Fatalf("历史 DialogueTurn 事件动作未物化: created=%v actions=%+v",
			created, first)
	}
	var receipt *CommunicationV4EventAction
	var invite *CommunicationV4EventAction
	for index := range first {
		action := &first[index]
		switch action.V4Kind {
		case communication.V4ActionInterviewAcceptedReceipt:
			receipt = action
		case communication.V4ActionInviteWechat:
			invite = action
		}
	}
	if receipt == nil ||
		receipt.Status != CommunicationV4EventActionDeferred ||
		receipt.FailureReason != CommunicationV4EventActionFailureDialogueActionOwned ||
		receipt.Text != "" ||
		receipt.ContentHash != "" ||
		receipt.ActionID == legacyAction.ActionID ||
		invite == nil ||
		invite.DependsOnActionID == nil ||
		*invite.DependsOnActionID != legacyAction.ActionID {
		t.Fatalf("历史回执没有保持 CommunicationAction 唯一归属: actions=%+v", first)
	}
	var repeated []CommunicationV4EventAction
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		repeated, created, err = materializeCommunicationV4EventActionsTx(
			tx,
			application,
			at.Add(time.Hour),
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if created || len(repeated) != 3 {
		t.Fatalf("历史回执重放发生增生: created=%v actions=%+v", created, repeated)
	}
	var communicationActions int64
	if err := s.db.Model(&CommunicationAction{}).
		Where("turn_id = ?", turnID).
		Count(&communicationActions).Error; err != nil || communicationActions != 1 {
		t.Fatalf("历史回执出现第二个候选人可见 owner: count=%d err=%v",
			communicationActions, err)
	}
}

func TestCommunicationV4EventActionLegacyDialogueReceiptMissingOwnerConflicts(t *testing.T) {
	s := openTest(t)
	profileID := "profile-v4-event-action-legacy-missing-owner"
	seedResumeStoreFixture(t, s, profileID)
	application := CommunicationV4ProjectionApplication{
		ProfileID: profileID, InputKind: CommunicationV4InputDialogueTurn,
		InputKey: "turn-v4-event-action-legacy-missing-owner",
		Outcome: CommunicationV4ApplicationOutcome{
			Actions: []communication.V4EventAction{{
				ActionKey:      "message:9|wechatReceipt",
				Kind:           communication.V4ActionWechatReceipt,
				CardMessageSeq: 9,
			}},
			PlannedActions: []communication.V4PlannedAction{{
				ActionKey:      "message:9|wechatReceipt",
				Kind:           communication.V4ActionWechatReceipt,
				CardMessageSeq: 9,
			}},
		},
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		_, _, err := materializeCommunicationV4EventActionsTx(
			tx,
			application,
			time.Now(),
		)
		return err
	})
	if !errors.Is(err, ErrCommunicationV4EventActionConflict) {
		t.Fatalf("历史回执缺 owner 必须响亮冲突: %v", err)
	}
	var rows int64
	if err := s.db.Model(&CommunicationV4EventAction{}).
		Where("profile_id = ?", profileID).
		Count(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("历史回执冲突不得留下半成品: rows=%d err=%v", rows, err)
	}
}

func TestCommunicationV4EventActionMissingPhraseIsManualWithoutAggregateMutation(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	profileID := "profile-v4-event-action-missing-phrase"
	_, before := seedSuccessfulV4Greeting(t, s, profileID, "conversation-missing-phrase", at)
	occurredAt := at.Add(time.Minute)
	event := communication.BusinessEvent{
		Key: "message:2", Kind: communication.EventWechatExchanged,
		Source: communication.EventSourceMessage, MessageSeq: 2, OccurredAt: &occurredAt,
	}
	projected, err := s.ApplyCommunicationV4BusinessEvent(
		ApplyCommunicationV4BusinessEventRequest{
			ProfileID: profileID, Event: event, AppliedAt: at.Add(2 * time.Minute),
		},
	)
	if err != nil || len(projected.Application.Outcome.Actions) != 2 {
		t.Fatalf("构造微信交换投影失败: result=%+v err=%v", projected, err)
	}
	aggregateBeforeMaterialize := projected.Aggregate
	automatic, err := s.CommunicationV4EventActionsBySource(
		profileID,
		CommunicationV4InputBusinessEvent,
		event.Key,
	)
	if err != nil || len(automatic) != 2 ||
		!automatic[0].PlannedAt.Equal(projected.Application.AppliedAt) {
		t.Fatalf("business-event 投影未在同事务物化动作: actions=%+v err=%v", automatic, err)
	}
	receipt := automatic[1]
	if receipt.V4Kind != communication.V4ActionWechatReceipt ||
		receipt.Status != CommunicationV4EventActionManualRequired ||
		receipt.FailureReason != CommunicationV4EventActionFailureFixedPhraseUnavailable ||
		receipt.Text != "" || receipt.ContentHash != "" {
		t.Fatalf("缺话术未局部转人工: %+v", receipt)
	}

	// Simulate a pre-ledger application left by an older build. Test fixtures
	// are disposable; production code never physically deletes these facts.
	if err := s.db.
		Where(
			"profile_id = ? AND source_input_kind = ? AND source_input_key = ?",
			profileID,
			CommunicationV4InputBusinessEvent,
			event.Key,
		).
		Delete(&CommunicationV4EventAction{}).Error; err != nil {
		t.Fatal(err)
	}
	replayed, err := s.ApplyCommunicationV4BusinessEvent(
		ApplyCommunicationV4BusinessEventRequest{
			ProfileID: profileID, Event: event, AppliedAt: at.Add(4 * time.Minute),
		},
	)
	if err != nil || replayed.Applied {
		t.Fatalf("既有 application 重放未补账: result=%+v err=%v", replayed, err)
	}
	repaired, err := s.CommunicationV4EventActionsBySource(
		profileID,
		CommunicationV4InputBusinessEvent,
		event.Key,
	)
	if err != nil || len(repaired) != 2 ||
		!repaired[0].PlannedAt.Equal(projected.Application.AppliedAt) {
		t.Fatalf("既有 application 未按原 AppliedAt 补齐动作: actions=%+v err=%v", repaired, err)
	}
	after, err := s.CommunicationV4AggregateByProfile(profileID)
	if err != nil || after.Revision != aggregateBeforeMaterialize.Revision ||
		after.State.WechatState != aggregateBeforeMaterialize.State.WechatState ||
		after.Revision == before.Revision {
		t.Fatalf("物化动作意外改写主聚合: before=%+v projected=%+v after=%+v err=%v",
			before, aggregateBeforeMaterialize, after, err)
	}
}

func TestCommunicationV4EventActionConflictRollsBackBusinessEventProjection(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 24, 10, 30, 0, 0, time.UTC)
	profileID := "profile-v4-event-action-atomic"
	_, before := seedSuccessfulV4Greeting(t, s, profileID, "conversation-v4-event-action-atomic", at)
	semanticKey := "message:2|notifyWechat"
	collisionID, err := CommunicationV4EventActionID(profileID, semanticKey)
	if err != nil {
		t.Fatal(err)
	}
	collision := CommunicationV4EventAction{
		ActionID: collisionID, ProfileID: profileID,
		SourceInputKind: CommunicationV4InputBusinessEvent,
		SourceInputKey:  "legacy:collision",
		SourceOrdinal:   0, SemanticActionKey: semanticKey,
		V4Kind: communication.V4ActionNotifyWechat, CardMessageSeq: 2,
		EffectKind:    CommunicationV4EventEffectNotification,
		Status:        CommunicationV4EventActionDeferred,
		FailureReason: CommunicationV4EventActionFailureNotificationChannelDeferred,
		PlannedAt:     at, CreatedAt: at, UpdatedAt: at,
	}
	if err := s.db.Create(&collision).Error; err != nil {
		t.Fatal(err)
	}
	occurredAt := at.Add(time.Minute)
	event := communication.BusinessEvent{
		Key: "message:2", Kind: communication.EventWechatExchanged,
		Source: communication.EventSourceMessage, MessageSeq: 2, OccurredAt: &occurredAt,
	}
	if _, err := s.ApplyCommunicationV4BusinessEvent(
		ApplyCommunicationV4BusinessEventRequest{
			ProfileID: profileID, Event: event, AppliedAt: at.Add(2 * time.Minute),
		},
	); !errors.Is(err, ErrCommunicationV4EventActionConflict) {
		t.Fatalf("动作冲突未终止投影事务: %v", err)
	}
	after, err := s.CommunicationV4AggregateByProfile(profileID)
	if err != nil || after.Revision != before.Revision ||
		after.State.WechatState != before.State.WechatState {
		t.Fatalf("动作冲突后聚合未回滚: before=%+v after=%+v err=%v", before, after, err)
	}
	var applications int64
	if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
		Where(
			"profile_id = ? AND input_kind = ? AND input_key = ?",
			profileID,
			CommunicationV4InputBusinessEvent,
			event.Key,
		).
		Count(&applications).Error; err != nil || applications != 0 {
		t.Fatalf("动作冲突后投影 application 未回滚: count=%d err=%v", applications, err)
	}
}

func TestCommunicationV4EventActionProfileIsolationAndConflict(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 24, 11, 0, 0, 0, time.UTC)
	const semanticKey = "shared-event|inviteWechat"
	actionIDs := make([]string, 0, 2)
	for index, profileID := range []string{"profile-v4-event-action-a", "profile-v4-event-action-b"} {
		source := "source-" + profileID
		createCommunicationV4BusinessEventApplication(
			t,
			s,
			profileID,
			source,
			1,
			[]communication.V4EventAction{
				{ActionKey: semanticKey, Kind: communication.V4ActionInviteWechat, CardMessageSeq: int64(40 + index)},
			},
			at,
		)
		result, err := s.MaterializeCommunicationV4EventActions(
			MaterializeCommunicationV4EventActionsRequest{
				ProfileID: profileID, SourceInputKey: source, MaterializedAt: at,
			},
		)
		if err != nil || len(result.Actions) != 1 {
			t.Fatalf("profile %s 物化失败: result=%+v err=%v", profileID, result, err)
		}
		actionIDs = append(actionIDs, result.Actions[0].ActionID)
	}
	if actionIDs[0] == actionIDs[1] {
		t.Fatalf("相同 semantic key 跨 profile 未隔离: %v", actionIDs)
	}

	conflictSource := "source-profile-v4-event-action-a-conflict"
	createCommunicationV4BusinessEventApplication(
		t,
		s,
		"profile-v4-event-action-a",
		conflictSource,
		2,
		[]communication.V4EventAction{
			{ActionKey: semanticKey, Kind: communication.V4ActionInviteWechat, CardMessageSeq: 99},
		},
		at.Add(time.Minute),
	)
	if _, err := s.MaterializeCommunicationV4EventActions(
		MaterializeCommunicationV4EventActionsRequest{
			ProfileID:      "profile-v4-event-action-a",
			SourceInputKey: conflictSource,
			MaterializedAt: at.Add(time.Minute),
		},
	); !errors.Is(err, ErrCommunicationV4EventActionConflict) {
		t.Fatalf("同 profile semantic key 被不同 source 复用时未响亮冲突: %v", err)
	}
}
