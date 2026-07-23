package store

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"

	"gorm.io/gorm"
)

func seedSuccessfulV4Greeting(
	t *testing.T,
	s *Store,
	profileID string,
	conversationRef string,
	at time.Time,
) (greetingLedgerFixture, CommunicationV4Aggregate) {
	t.Helper()
	fixture := seedGreetingLedger(t, s, profileID)
	req := greetingIntentRequest(fixture, "intent-"+profileID, "", at.Add(-time.Minute))
	req.Intent.SendFingerprint = "content-" + profileID
	created, err := s.CreateGreetingEffectIntentAndCmd(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyResultMessage(
		created.Command.MsgID,
		"result-"+profileID,
		"result",
		fixture.HandID,
		func(command *CmdRecord) (ResultCommandMutation, error) {
			command.Status = CmdOk
			command.TerminalAt = &at
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk,
					Text:         "测试招呼",
					ContentHash:  req.Intent.SendFingerprint,
					Greeting: &GreetingResultMutation{
						PlatformUserRef: "person-" + profileID,
						PositionRef:     "position-" + profileID,
						ConversationRef: conversationRef,
						Text:            "测试招呼",
						ContentHash:     req.Intent.SendFingerprint,
						ObservedAtMs:    at.UnixMilli(),
					},
				},
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(profileID)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, *aggregate
}

func TestCommunicationV4SchemaHasRestrictedProfileRootAndNoDelete(t *testing.T) {
	s := openTest(t)
	type tableColumn struct {
		Name string
		PK   int `gorm:"column:pk"`
	}
	var columns []tableColumn
	if err := s.db.Raw("PRAGMA table_info('communication_v4_aggregates')").Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, column := range columns {
		seen[column.Name] = true
	}
	for _, required := range []string{
		"profile_id", "root_greeting_intent_id", "state_schema_version", "revision",
		"projected_through_seq", "state", "automation_status",
	} {
		if !seen[required] {
			t.Fatalf("V4 aggregate 缺少 %s: %+v", required, columns)
		}
	}
	if seen["deleted_at"] {
		t.Fatal("V4 aggregate 不得引入隐式软删除")
	}
	type foreignKeyRow struct {
		Table    string `gorm:"column:table"`
		From     string `gorm:"column:from"`
		To       string `gorm:"column:to"`
		OnUpdate string `gorm:"column:on_update"`
		OnDelete string `gorm:"column:on_delete"`
	}
	var foreignKeys []foreignKeyRow
	if err := s.db.Raw("PRAGMA foreign_key_list('communication_v4_aggregates')").Scan(&foreignKeys).Error; err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range foreignKeys {
		if row.Table == "candidate_profiles" && row.From == "profile_id" && row.To == "profile_id" {
			found = true
			if row.OnUpdate != "RESTRICT" || row.OnDelete != "RESTRICT" {
				t.Fatalf("V4 aggregate 外键不得级联: %+v", row)
			}
		}
	}
	if !found {
		t.Fatalf("V4 aggregate 缺少 profile 外键: %+v", foreignKeys)
	}
}

func TestSuccessfulGreetingCreatesV4RootAndReplayDoesNotResetIt(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	_, aggregate := seedSuccessfulV4Greeting(t, s, "v4-root", "conversation-v4-root", at)
	if aggregate.RootGreetingIntentID != "intent-v4-root" || aggregate.Revision != 0 ||
		aggregate.ProjectedThroughSeq != 1 || aggregate.State.MainStatus != communication.V4StatusGreeted ||
		aggregate.State.ColdPromptRemaining != 2 || aggregate.State.ColdWechatRemaining != 1 ||
		aggregate.State.LastOutboundAt == nil {
		t.Fatalf("成功招呼未建立完整 V4 根: %+v", aggregate)
	}
	profile, err := s.CandidateProfileByID(aggregate.ProfileID)
	if err != nil || profile.GreetedAt == nil || !aggregate.State.LastOutboundAt.Equal(*profile.GreetedAt) {
		t.Fatalf("V4 根时钟未锚定同事务 greetedAt: aggregate=%+v profile=%+v err=%v", aggregate, profile, err)
	}

	manualAt := at.Add(time.Minute)
	if err := s.db.Model(&CommunicationV4Aggregate{}).Where("profile_id = ?", aggregate.ProfileID).
		Updates(map[string]any{
			"automation_status":  ProfileCommunicationAutomationManualRequired,
			"manual_reason":      "testManual",
			"manual_required_at": manualAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		_, created, err := applyCommunicationV4RootTx(
			tx, aggregate.ProfileID, aggregate.RootGreetingIntentID, 1, at.Add(2*time.Minute),
		)
		if created {
			t.Fatal("相同招呼根重放不得新建")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.CommunicationV4AggregateByProfile(aggregate.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		replayed.ManualReason != "testManual" || replayed.Revision != 0 {
		t.Fatalf("根重放重置了既有聚合: %+v", replayed)
	}
	var count int64
	if err := s.db.Model(&CommunicationV4Aggregate{}).Where("profile_id = ?", aggregate.ProfileID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("根重放发生增生: count=%d err=%v", count, err)
	}
}

func TestGreetingRootFailureRollsBackWholeResultTransaction(t *testing.T) {
	s := openTest(t)
	fixture := seedGreetingLedger(t, s, "v4-root-rollback")
	at := time.Date(2026, 7, 23, 9, 30, 0, 0, time.UTC)
	req := greetingIntentRequest(fixture, "intent-v4-root-rollback", "", at.Add(-time.Minute))
	req.Intent.SendFingerprint = "content-v4-root-rollback"
	created, err := s.CreateGreetingEffectIntentAndCmd(req)
	if err != nil {
		t.Fatal(err)
	}
	forced := errors.New("forced V4 root create failure")
	callbackName := "test:fail_v4_root_create"
	if err := s.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CommunicationV4Aggregate" {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Create().Remove(callbackName)

	_, err = s.ApplyResultMessage(
		created.Command.MsgID,
		"result-v4-root-rollback",
		"result",
		fixture.HandID,
		func(command *CmdRecord) (ResultCommandMutation, error) {
			command.Status = CmdOk
			command.TerminalAt = &at
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk,
					Text:         "测试招呼",
					ContentHash:  req.Intent.SendFingerprint,
					Greeting: &GreetingResultMutation{
						PlatformUserRef: "person-" + fixture.ProfileID,
						PositionRef:     "position-" + fixture.ProfileID,
						ConversationRef: "conversation-v4-root-rollback",
						Text:            "测试招呼",
						ContentHash:     req.Intent.SendFingerprint,
						ObservedAtMs:    at.UnixMilli(),
					},
				},
			}, nil
		},
	)
	if !errors.Is(err, forced) {
		t.Fatalf("应返回聚合根注入失败: %v", err)
	}
	profile, _ := s.CandidateProfileByID(fixture.ProfileID)
	if profile == nil || profile.MainStatus != CandidateProfileSelected ||
		profile.SuccessfulGreetingIntentID != nil || profile.ConversationRef != nil || profile.GreetedAt != nil {
		t.Fatalf("聚合根失败后档案未回滚: %+v", profile)
	}
	var aggregates, conversations, messages int64
	_ = s.db.Model(&CommunicationV4Aggregate{}).Count(&aggregates).Error
	_ = s.db.Model(&Conversation{}).Where("account_ref = ?", fixture.AccountRef).Count(&conversations).Error
	_ = s.db.Model(&Message{}).Where("account_ref = ?", fixture.AccountRef).Count(&messages).Error
	if aggregates != 0 || conversations != 0 || messages != 0 {
		t.Fatalf("聚合根失败泄漏业务事实: aggregates=%d conversations=%d messages=%d",
			aggregates, conversations, messages)
	}
}

func TestRejectedGreetingDoesNotCreateV4Root(t *testing.T) {
	s := openTest(t)
	fixture := seedGreetingLedger(t, s, "v4-rejected")
	at := time.Date(2026, 7, 23, 9, 45, 0, 0, time.UTC)
	req := greetingIntentRequest(fixture, "intent-v4-rejected", "", at.Add(-time.Minute))
	created, err := s.CreateGreetingEffectIntentAndCmd(req)
	if err != nil {
		t.Fatal(err)
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var intent EffectIntent
		if err := tx.First(&intent, "intent_id = ?", created.Intent.IntentID).Error; err != nil {
			return err
		}
		_, err := applyGreetingResultTx(tx, &intent, GreetingResultMutation{Rejected: true}, at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.CandidateProfileByID(fixture.ProfileID)
	if err != nil || profile.MainStatus != CandidateProfileEnded || profile.EndReason == nil ||
		*profile.EndReason != CandidateProfileEndGreetingFailed {
		t.Fatalf("明确拒绝未保持 M4 终态: profile=%+v err=%v", profile, err)
	}
	if _, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID); !errors.Is(err, ErrCommunicationV4Missing) {
		t.Fatalf("招呼未发生不得建立 V4 根: %v", err)
	}
}

func TestCommunicationV4BusinessEventIsAtomicIdempotentAndRestartSafe(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	_, root := seedSuccessfulV4Greeting(t, s, "v4-event", "conversation-v4-event", at)
	text := "普通候选回复"
	event := communication.BusinessEvent{
		Key: "message:2", Kind: communication.EventCandidateExpressionReceived,
		Source: communication.EventSourceMessage, MessageSeq: 2,
		OccurredAt: &at, ExpressionKind: communication.ExpressionText, Text: text,
	}
	first, err := s.ApplyCommunicationV4BusinessEvent(ApplyCommunicationV4BusinessEventRequest{
		ProfileID: root.ProfileID, Event: event, AppliedAt: at.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Applied || first.Aggregate.Revision != 1 || first.Aggregate.ProjectedThroughSeq != 2 ||
		first.Aggregate.State.MainStatus != communication.V4StatusCommunicating ||
		first.Application.Outcome.Dialogue != communication.V4DialogueClassifyAndReply {
		t.Fatalf("事件未原子推进聚合与 outcome: %+v", first)
	}
	profile, _ := s.CandidateProfileByID(root.ProfileID)
	if profile == nil || profile.MainStatus != CandidateProfileCommunicating ||
		profile.FirstRealMessageSeq == nil || *profile.FirstRealMessageSeq != 2 ||
		profile.CommunicatingAt == nil {
		t.Fatalf("CandidateProfile 镜像未同步: %+v", profile)
	}

	replay, err := s.ApplyCommunicationV4BusinessEvent(ApplyCommunicationV4BusinessEventRequest{
		ProfileID: root.ProfileID, Event: event, AppliedAt: at.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Applied || replay.Aggregate.Revision != 1 ||
		replay.Application.InputDigest != first.Application.InputDigest {
		t.Fatalf("相同事件重放发生增生: %+v", replay)
	}
	conflicting := event
	conflicting.Text = "同 key 偷换正文"
	if _, err := s.ApplyCommunicationV4BusinessEvent(ApplyCommunicationV4BusinessEventRequest{
		ProfileID: root.ProfileID, Event: conflicting, AppliedAt: at.Add(3 * time.Minute),
	}); !errors.Is(err, ErrCommunicationV4Conflict) {
		t.Fatalf("同 key 偷换事实必须冲突: %v", err)
	}
	var applications int64
	if err := s.db.Model(&CommunicationV4ProjectionApplication{}).Count(&applications).Error; err != nil || applications != 1 {
		t.Fatalf("事件重放后 application 数错误: count=%d err=%v", applications, err)
	}
	var rawOutcome string
	if err := s.db.Raw(
		"SELECT CAST(outcome AS TEXT) FROM communication_v4_projection_applications WHERE profile_id = ?",
		root.ProfileID,
	).Scan(&rawOutcome).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rawOutcome, text) {
		t.Fatalf("投影回执不得保存候选人正文: %s", rawOutcome)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restored, err := s.CommunicationV4AggregateByProfile(root.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 1 || restored.State.MainStatus != communication.V4StatusCommunicating ||
		restored.State.LastRealMessageSeq != 2 || restored.State.RealMessageRound != 2 {
		t.Fatalf("重启后 V4 状态丢失: %+v", restored)
	}
}

func TestCommunicationV4UnknownEventPersistsManualBlockAndConcurrentReplayDoesNotGrow(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 23, 11, 0, 0, 0, time.UTC)
	_, root := seedSuccessfulV4Greeting(t, s, "v4-unknown", "conversation-v4-unknown", at)
	event := communication.BusinessEvent{
		Key: "message:2", Kind: communication.EventUnknownPlatform,
		Source: communication.EventSourceMessage, MessageSeq: 2, ConservativeCode: "unseenType343",
	}
	start := make(chan struct{})
	results := make([]*ApplyCommunicationV4BusinessEventResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = s.ApplyCommunicationV4BusinessEvent(
				ApplyCommunicationV4BusinessEventRequest{
					ProfileID: root.ProfileID, Event: event, AppliedAt: at.Add(time.Minute),
				},
			)
		}(i)
	}
	close(start)
	wg.Wait()
	applied := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("并发同事件[%d]失败: %v", i, errs[i])
		}
		if results[i].Applied {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("并发重放必须恰好一项真正应用: %d", applied)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(root.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Revision != 1 ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != string(communication.V4ManualUnknownPlatformEvent) ||
		aggregate.ManualRequiredAt == nil {
		t.Fatalf("未知事实未持久转人工: %+v", aggregate)
	}
	var applications int64
	_ = s.db.Model(&CommunicationV4ProjectionApplication{}).Count(&applications).Error
	if applications != 1 {
		t.Fatalf("并发重放 application 增生: %d", applications)
	}
}
