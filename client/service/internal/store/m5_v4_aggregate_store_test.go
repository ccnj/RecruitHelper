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

func seedLegacyGreetedProfile(
	t *testing.T,
	s *Store,
	profileID string,
	conversationRef string,
	greetingSeq int64,
	at time.Time,
) greetingLedgerFixture {
	t.Helper()
	fixture := seedGreetingLedger(t, s, profileID)
	req := greetingIntentRequest(fixture, "intent-"+profileID, "", at.Add(-time.Minute))
	req.Intent.SendFingerprint = "content-" + profileID
	created, err := s.CreateGreetingEffectIntentAndCmd(req)
	if err != nil {
		t.Fatal(err)
	}
	resolvedAt := at
	intentUpdates := map[string]any{"status": EffectIntentOk, "resolved_at": resolvedAt}
	profileUpdates := map[string]any{
		"main_status": CandidateProfileGreeted, "successful_greeting_intent_id": created.Intent.IntentID,
		"greeted_at": at,
	}
	if conversationRef != "" {
		intentUpdates["result_conversation_ref"] = conversationRef
		intentUpdates["result_message_seq"] = greetingSeq
		profileUpdates["conversation_ref"] = conversationRef
	}
	if err := s.db.Model(&EffectIntent{}).Where("intent_id = ?", created.Intent.IntentID).
		Updates(intentUpdates).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", profileID).
		Updates(profileUpdates).Error; err != nil {
		t.Fatal(err)
	}
	if conversationRef == "" {
		return fixture
	}
	conversation := Conversation{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef, ConversationRef: conversationRef,
		PlatformUserRef: "person-" + profileID, TrackingState: TrackingAdopted,
		AdoptedBoundarySeq: 0, LastMessageSeq: greetingSeq,
	}
	if err := s.db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	adoptedAt := at
	if err := s.db.Create(&TrackedIntent{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef, ConversationRef: conversationRef,
		Status: TrackingAdopted, RequestedBy: greetingTrackedRequestedBy,
		RequestedAt: at, AdoptedAt: &adoptedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	text := "历史招呼"
	intentID := created.Intent.IntentID
	if err := s.db.Create(&Message{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef, ConversationRef: conversationRef,
		Seq: greetingSeq, Direction: "out", Kind: "text", ContentHash: req.Intent.SendFingerprint,
		Text: &text, Origin: "self", OutboundIntentID: &intentID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return fixture
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

func TestLegacyGreetedProfileRootActivationUsesExactGreetingFact(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 23, 9, 15, 0, 0, time.UTC)
	fixture := seedLegacyGreetedProfile(t, s, "legacy-bound", "conversation-legacy-bound", 1, at)
	unrooted, err := s.UnrootedGreetedProfileIDsForAccount(AccountKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	})
	if err != nil || len(unrooted) != 1 || unrooted[0] != "legacy-bound" {
		t.Fatalf("账号激活扫描未找到精确档案: ids=%v err=%v", unrooted, err)
	}
	bound, created, err := s.EnsureCommunicationV4RootForGreetedProfile(
		"legacy-bound", at.Add(time.Minute),
	)
	if err != nil || !created || bound.ProjectedThroughSeq != 1 ||
		bound.RootGreetingIntentID != "intent-legacy-bound" {
		t.Fatalf("已绑定历史档案根激活失败: aggregate=%+v created=%v err=%v", bound, created, err)
	}
	replayed, created, err := s.EnsureCommunicationV4RootForGreetedProfile(
		"legacy-bound", at.Add(2*time.Minute),
	)
	if err != nil || created || replayed.Revision != 0 {
		t.Fatalf("历史根重放不幂等: aggregate=%+v created=%v err=%v", replayed, created, err)
	}
	unrooted, err = s.UnrootedGreetedProfileIDsForAccount(AccountKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	})
	if err != nil || len(unrooted) != 0 {
		t.Fatalf("已激活档案仍被重复扫描: ids=%v err=%v", unrooted, err)
	}

	seedLegacyGreetedProfile(t, s, "legacy-unbound", "", 0, at)
	unbound, created, err := s.EnsureCommunicationV4RootForGreetedProfile(
		"legacy-unbound", at.Add(time.Minute),
	)
	if err != nil || !created || unbound.ProjectedThroughSeq != 0 {
		t.Fatalf("未绑定历史档案不得猜会话: aggregate=%+v created=%v err=%v", unbound, created, err)
	}

	seedLegacyGreetedProfile(t, s, "legacy-wrong-seq", "conversation-legacy-wrong-seq", 2, at)
	if _, _, err := s.EnsureCommunicationV4RootForGreetedProfile(
		"legacy-wrong-seq", at.Add(time.Minute),
	); !errors.Is(err, ErrCommunicationV4Conflict) {
		t.Fatalf("非首条招呼事实必须阻断激活: %v", err)
	}
	var leaked int64
	if err := s.db.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ?", "legacy-wrong-seq").Count(&leaked).Error; err != nil || leaked != 0 {
		t.Fatalf("冲突激活泄漏聚合根: count=%d err=%v", leaked, err)
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

func TestCommunicationV4ConfirmedActionAndArchiveAreDurableAndIdempotent(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	fixture, root := seedSuccessfulV4Greeting(t, s, "v4-actions", "conversation-v4-actions", at)
	if root.State.LastOutboundAt == nil {
		t.Fatal("测试招呼根缺少出站时钟")
	}
	eventAt := root.State.LastOutboundAt.Add(time.Minute)
	event := communication.BusinessEvent{
		Key: "message:2", Kind: communication.EventCandidateExpressionReceived,
		Source: communication.EventSourceMessage, MessageSeq: 2,
		OccurredAt: &eventAt, ExpressionKind: communication.ExpressionText, Text: "在吗",
	}
	if _, err := s.ApplyCommunicationV4BusinessEvent(ApplyCommunicationV4BusinessEventRequest{
		ProfileID: root.ProfileID, Event: event, AppliedAt: eventAt,
	}); err != nil {
		t.Fatal(err)
	}

	sentAt := eventAt.Add(time.Minute)
	action := communication.V4ConfirmedAction{
		ActionKey: "turn:v4-actions|replyText", Kind: communication.V4ActionReplyText,
		MessageSeq: 3, SentAt: &sentAt,
	}
	var confirmed CommunicationV4Aggregate
	err := s.db.Transaction(func(tx *gorm.DB) error {
		next, _, applied, err := applyCommunicationV4ConfirmedActionTx(
			tx, root.ProfileID, action, sentAt,
		)
		if err == nil && !applied {
			t.Fatal("首次正证必须推进")
		}
		confirmed = next
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Revision != 2 || confirmed.State.LastOutboundMessageSeq != 3 ||
		confirmed.State.LastBodyAt == nil || !confirmed.State.LastBodyAt.Equal(sentAt) {
		t.Fatalf("正证未推进预算/正文时钟: %+v", confirmed)
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		next, _, applied, err := applyCommunicationV4ConfirmedActionTx(
			tx, root.ProfileID, action, sentAt.Add(time.Minute),
		)
		if err == nil && (applied || next.Revision != 2) {
			t.Fatalf("正证重放发生增生: applied=%v aggregate=%+v", applied, next)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	swapped := action
	swapped.MessageSeq = 4
	err = s.db.Transaction(func(tx *gorm.DB) error {
		_, _, _, err := applyCommunicationV4ConfirmedActionTx(
			tx, root.ProfileID, swapped, sentAt.Add(2*time.Minute),
		)
		return err
	})
	if !errors.Is(err, ErrCommunicationV4Conflict) {
		t.Fatalf("同 actionKey 偷换正证必须冲突: %v", err)
	}
	inboundText := "在吗"
	outboundText := "回复"
	if changes, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: ConversationKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ConversationRef: "conversation-v4-actions",
		},
		ExpectedTailSeq: 1,
		NewMessages: []MessageDraft{
			{
				Direction: "in", Kind: "text", ContentHash: "v4-actions-inbound",
				Text: &inboundText, Origin: "external",
			},
			{
				Direction: "out", Kind: "text", ContentHash: "v4-actions-outbound",
				Text: &outboundText, Origin: "self",
			},
		},
		SyncedAt: sentAt,
	}); err != nil || len(changes.Inserted) != 2 ||
		changes.Inserted[1].Seq != confirmed.ProjectedThroughSeq {
		t.Fatalf("补齐归档 CAS 的活动账本尾失败: changes=%+v err=%v", changes, err)
	}

	archiveAt := at.Add(8 * 24 * time.Hour)
	archiveReq := communicationV4ArchiveRequestForTest(
		t, s, confirmed, archiveAt, false,
	)
	staleReq := archiveReq
	staleReq.ExpectedRevision--
	if _, err := s.ApplyCommunicationV4ArchiveAction(staleReq); !errors.Is(err, ErrCommunicationV4Conflict) {
		t.Fatalf("旧聚合快照不得授权归档: %v", err)
	}
	archiveResult, err := s.ApplyCommunicationV4ArchiveAction(archiveReq)
	if err != nil {
		t.Fatal(err)
	}
	if !archiveResult.Applied {
		t.Fatal("首次归档必须推进")
	}
	archived := archiveResult.Aggregate
	if archived.Revision != 3 || archived.State.MainStatus != communication.V4StatusEnded ||
		archived.State.EndReason != communication.V4EndFallback {
		t.Fatalf("归档未推进聚合: %+v", archived)
	}
	profile, _ := s.CandidateProfileByID(root.ProfileID)
	if profile == nil || profile.MainStatus != CandidateProfileEnded || profile.EndReason == nil ||
		*profile.EndReason != CandidateProfileEndFallbackArchive {
		t.Fatalf("归档未同步 CandidateProfile: %+v", profile)
	}
	replayed, err := s.ApplyCommunicationV4ArchiveAction(archiveReq)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Applied || replayed.Aggregate.Revision != 3 {
		t.Fatalf("归档重放发生增生: applied=%v aggregate=%+v", replayed.Applied, replayed.Aggregate)
	}
}
