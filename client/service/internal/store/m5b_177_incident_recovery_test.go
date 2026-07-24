package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/textcanon"
	"recruithelper/contract/gen/go/protocol"
)

type m5B177IncidentFixture struct {
	store      *Store
	key        ConversationKey
	profileID  string
	freshMsgID string
	sourceKey  string
	oldHash    string
	freshHash  string
	resumeAtMs int64
	greeting   Message
	initialApp CommunicationV4ProjectionApplication
	initialV4  CommunicationV4Aggregate
}

func seedM5B177IncidentFixture(t *testing.T, preceding int) m5B177IncidentFixture {
	t.Helper()
	s := openTest(t)
	now := time.Now().UTC()
	greetedAt := now.Add(-5 * time.Minute)
	profileID := "profile-m5b-177"
	conversationRef := "conversation-m5b-177"
	ledger, _ := seedSuccessfulV4Greeting(t, s, profileID, conversationRef, greetedAt)
	key := ConversationKey{
		Platform: ledger.Platform, AccountRef: ledger.AccountRef, ConversationRef: conversationRef,
	}

	pausedAt := now.Add(-2 * time.Minute)
	if err := s.db.Model(&Account{}).
		Where("platform = ? AND account_ref = ?", key.Platform, key.AccountRef).
		Updates(map[string]any{"stopped_at": pausedAt, "paused_reason": "userPaused"}).Error; err != nil {
		t.Fatal(err)
	}

	var greeting Message
	if err := s.db.First(
		&greeting,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = 1",
		key.Platform, key.AccountRef, key.ConversationRef,
	).Error; err != nil {
		t.Fatal(err)
	}

	resumeAtMs := greetedAt.Add(time.Minute).UnixMilli()
	sourceKey := strings.Repeat("a", 64)
	oldHash := textcanon.Hash(m5B177ResumeText)
	resumeText := m5B177ResumeText
	if err := s.db.Create(&Message{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		Seq: 2, Direction: "system", Kind: "system", ContentHash: oldHash,
		Text: &resumeText, TsApproxMs: &resumeAtMs, Origin: "external",
		FirstSeenRoundID: "round-m5b-177-old", SourceKey: &sourceKey,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Conversation{}).
		Where(conversationWhere(key), conversationArgs(key)...).
		Updates(map[string]any{
			"last_message_seq":       2,
			"last_message_direction": "in",
			"last_message_kind":      "text",
			"last_message_preview":   m5B177ResumeText,
			"adopted_boundary_seq":   1,
		}).Error; err != nil {
		t.Fatal(err)
	}

	oldEvent, err := communication.NormalizeLedgerMessage(communication.LedgerMessageFact{
		Seq: 2, Direction: "system", Kind: "system", Text: &resumeText,
		Origin: "external", TsApproxMs: &resumeAtMs,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldProjection, err := s.ApplyCommunicationV4BusinessEvent(
		ApplyCommunicationV4BusinessEventRequest{
			ProfileID: profileID, Event: oldEvent, AppliedAt: now.Add(-3 * time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !oldProjection.Applied {
		t.Fatal("fixture 必须先投影一条错误 systemNotice")
	}

	freshHash := strings.Repeat("b", 64)
	messages := make([]protocol.ThreadMessage, 0, preceding+1)
	for index := 0; index < preceding; index++ {
		text := "脱敏前置历史"
		ts := greetedAt.Add(time.Duration(index) * time.Second).UnixMilli()
		messages = append(messages, protocol.ThreadMessage{
			Idx: index, Direction: protocol.MessageDirectionSystem, Kind: protocol.MessageKindSystem,
			Text: &text, ContentHash: strings.Repeat(string(rune('c'+index)), 64),
			SourceKey: strings.Repeat(string(rune('f'+index)), 64), TsApprox: &ts,
		})
	}
	cardType := protocol.CardTypeResumeAttachment
	cardState := protocol.CardStateUnknown
	messages = append(messages, protocol.ThreadMessage{
		Idx: preceding, Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindCard,
		Text: &resumeText, ContentHash: freshHash, SourceKey: sourceKey,
		CardType: &cardType, CardState: &cardState, TsApprox: &resumeAtMs,
	})
	freshMsgID := "msg-m5b-177-fresh"
	storeM5B177FreshRead(t, s, key, freshMsgID, messages, now.Add(-time.Minute))

	return m5B177IncidentFixture{
		store: s, key: key, profileID: profileID, freshMsgID: freshMsgID,
		sourceKey: sourceKey, oldHash: oldHash, freshHash: freshHash,
		resumeAtMs: resumeAtMs, greeting: greeting,
		initialApp: oldProjection.Application, initialV4: oldProjection.Aggregate,
	}
}

func storeM5B177FreshRead(
	t *testing.T,
	s *Store,
	key ConversationKey,
	msgID string,
	messages []protocol.ThreadMessage,
	terminalAt time.Time,
) {
	t.Helper()
	argsRaw, err := json.Marshal(protocol.ChatReadThreadArgs{
		ConversationRef: key.ConversationRef,
		RequireCurrent:  true,
		Window: protocol.ThreadWindow{
			Deep: true, MaxMessages: protocol.DefaultPaginationReadThreadMaxItems,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dataRaw, err := json.Marshal(protocol.ChatReadThreadData{
		Complete: true, ReachedTop: true, Messages: messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	resultRaw, err := json.Marshal(protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusOk, Data: dataRaw, ExecMs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&CmdRecord{
		MsgID: msgID, Name: protocol.PrimChatReadThread, Class: string(protocol.ClassIntrusive),
		Domain: key.Platform + ":" + key.AccountRef, Platform: key.Platform, AccountRef: key.AccountRef,
		Args: string(argsRaw), HandID: "hand-m5b-177-proof", LogicalDispatchID: msgID,
		Status: CmdOk, ResultBody: string(resultRaw),
		CreatedAt: terminalAt.Add(-time.Second), UpdatedAt: terminalAt, TerminalAt: &terminalAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func mutateM5B177FreshMessages(
	t *testing.T,
	fixture m5B177IncidentFixture,
	mutate func(*[]protocol.ThreadMessage),
) {
	t.Helper()
	var command CmdRecord
	if err := fixture.store.db.First(&command, "msg_id = ?", fixture.freshMsgID).Error; err != nil {
		t.Fatal(err)
	}
	var result protocol.ResultBody
	if err := json.Unmarshal([]byte(command.ResultBody), &result); err != nil {
		t.Fatal(err)
	}
	var data protocol.ChatReadThreadData
	if err := json.Unmarshal(result.Data, &data); err != nil {
		t.Fatal(err)
	}
	mutate(&data.Messages)
	for index := range data.Messages {
		data.Messages[index].Idx = index
	}
	dataRaw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	result.Data = dataRaw
	resultRaw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.db.Model(&CmdRecord{}).
		Where("msg_id = ?", fixture.freshMsgID).
		UpdateColumn("result_body", string(resultRaw)).Error; err != nil {
		t.Fatal(err)
	}
}

func TestRecoverM5B177IncidentCorrectsInPlaceArchivesApplicationAndRewindsCursor(t *testing.T) {
	fixture := seedM5B177IncidentFixture(t, 3)

	result, err := fixture.store.RecoverM5B177Incident(fixture.freshMsgID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.AlreadyApplied || !result.FreshTailUnique ||
		!result.ApplicationKeyArchived || result.ProjectedThroughSeqBefore != 2 ||
		result.ProjectedThroughSeqAfter != 1 {
		t.Fatalf("恢复结果错误: %+v", result)
	}
	assertM5B177RecoveredState(t, fixture)

	replayed, err := fixture.store.RecoverM5B177Incident(fixture.freshMsgID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Applied || !replayed.AlreadyApplied || !replayed.FreshTailUnique ||
		!replayed.ApplicationKeyArchived {
		t.Fatalf("同一 fresh msgId 立即重跑必须幂等: %+v", replayed)
	}
	assertM5B177RecoveredState(t, fixture)
}

func TestRecoverM5B177IncidentAcceptsPrecedingHistoryOnlyWithUniqueMatchingTail(t *testing.T) {
	fixture := seedM5B177IncidentFixture(t, 7)
	if _, err := fixture.store.RecoverM5B177Incident(fixture.freshMsgID); err != nil {
		t.Fatalf("N 条前置历史且唯一尾匹配应通过: %v", err)
	}
	assertM5B177RecoveredState(t, fixture)
}

func TestRecoverM5B177IncidentRejectsAmbiguousOrMissingExpectedTail(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*[]protocol.ThreadMessage)
	}{
		{
			name: "尾部歧义",
			mutate: func(messages *[]protocol.ThreadMessage) {
				(*messages)[0].SourceKey = (*messages)[len(*messages)-1].SourceKey
			},
		},
		{
			name: "缺失预期尾",
			mutate: func(messages *[]protocol.ThreadMessage) {
				*messages = (*messages)[:len(*messages)-1]
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := seedM5B177IncidentFixture(t, 2)
			mutateM5B177FreshMessages(t, fixture, test.mutate)
			if _, err := fixture.store.RecoverM5B177Incident(fixture.freshMsgID); !errors.Is(err, ErrM5B177IncidentRecoveryUnsafe) {
				t.Fatalf("不完整新鲜证明必须拒绝: %v", err)
			}
			assertM5B177InitialState(t, fixture)
		})
	}
}

func TestRecoverM5B177IncidentRejectsAccountNotUserPaused(t *testing.T) {
	fixture := seedM5B177IncidentFixture(t, 2)
	if err := fixture.store.db.Model(&Account{}).
		Where("platform = ? AND account_ref = ?", fixture.key.Platform, fixture.key.AccountRef).
		Updates(map[string]any{"stopped_at": nil, "paused_reason": ""}).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.store.RecoverM5B177Incident(fixture.freshMsgID); !errors.Is(err, ErrM5B177IncidentRecoveryUnsafe) {
		t.Fatalf("账号非 userPaused 必须拒绝: %v", err)
	}
	assertM5B177InitialState(t, fixture)
}

func TestRecoverM5B177IncidentRejectsExistingDownstreamFacts(t *testing.T) {
	fixture := seedM5B177IncidentFixture(t, 2)
	if err := fixture.store.db.Create(&M5TrialSelection{
		SelectionID: "selection-m5b-177-downstream", ProfileID: fixture.profileID,
		Status: M5TrialSelectionCompleted, SelectedBy: "test",
		SelectedAt: time.Now().UTC(), EndedAt: ptrTime(time.Now().UTC()),
	}).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.store.RecoverM5B177Incident(fixture.freshMsgID); !errors.Is(err, ErrM5B177IncidentRecoveryUnsafe) {
		t.Fatalf("存在下游事实必须拒绝: %v", err)
	}
	assertM5B177InitialState(t, fixture)
}

func TestRecoverM5B177IncidentLateAuditFailureRollsBackAllChanges(t *testing.T) {
	fixture := seedM5B177IncidentFixture(t, 2)
	forced := errors.New("forced m5b 177 recovery audit failure")
	callbackName := "test:fail_m5b_177_recovery_audit"
	if err := fixture.store.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		entry, ok := tx.Statement.Dest.(*AuditEntry)
		if ok && entry.Category == m5B177RecoveryAuditCategory {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer fixture.store.db.Callback().Create().Remove(callbackName)

	if _, err := fixture.store.RecoverM5B177Incident(fixture.freshMsgID); !errors.Is(err, forced) {
		t.Fatalf("必须返回事务尾部审计失败: %v", err)
	}
	assertM5B177InitialState(t, fixture)
}

func TestRecoverM5B177IncidentAllowsCanonicalResumeProjectionAfterRecovery(t *testing.T) {
	fixture := seedM5B177IncidentFixture(t, 4)
	if _, err := fixture.store.RecoverM5B177Incident(fixture.freshMsgID); err != nil {
		t.Fatal(err)
	}

	var corrected Message
	if err := fixture.store.db.First(
		&corrected,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = 2",
		fixture.key.Platform, fixture.key.AccountRef, fixture.key.ConversationRef,
	).Error; err != nil {
		t.Fatal(err)
	}
	event, err := communication.NormalizeLedgerMessage(communication.LedgerMessageFact{
		Seq: corrected.Seq, Direction: corrected.Direction, Kind: corrected.Kind,
		Text: corrected.Text, CardType: corrected.CardType, CardState: corrected.CardState,
		Origin: corrected.Origin, TsApproxMs: corrected.TsApproxMs,
	})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := fixture.store.ApplyCommunicationV4BusinessEvent(
		ApplyCommunicationV4BusinessEventRequest{
			ProfileID: fixture.profileID, Event: event, AppliedAt: time.Now().UTC(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !projected.Applied || projected.Aggregate.Revision != 2 ||
		projected.Aggregate.ProjectedThroughSeq != 2 ||
		projected.Application.InputKey != m5B177CanonicalInputKey ||
		projected.Application.SemanticKind != string(communication.EventResumeSubmitted) ||
		projected.Application.FromRevision != 1 || projected.Application.ToRevision != 2 {
		t.Fatalf("恢复后 canonical resume 投影错误: %+v", projected)
	}

	var applications []CommunicationV4ProjectionApplication
	if err := fixture.store.db.Where("profile_id = ?", fixture.profileID).
		Order("to_revision").Find(&applications).Error; err != nil {
		t.Fatal(err)
	}
	if len(applications) != 2 ||
		applications[0].InputKey != m5B177ArchivedInputKey ||
		applications[0].FromRevision != 0 || applications[0].ToRevision != 1 ||
		applications[1].InputKey != m5B177CanonicalInputKey ||
		applications[1].FromRevision != 1 || applications[1].ToRevision != 2 {
		t.Fatalf("应用收据链没有形成 archive 0→1 + canonical 1→2: %+v", applications)
	}
}

func assertM5B177InitialState(t *testing.T, fixture m5B177IncidentFixture) {
	t.Helper()
	var message Message
	if err := fixture.store.db.First(
		&message,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = 2",
		fixture.key.Platform, fixture.key.AccountRef, fixture.key.ConversationRef,
	).Error; err != nil {
		t.Fatal(err)
	}
	if message.Direction != "system" || message.Kind != "system" ||
		message.ContentHash != fixture.oldHash || message.CardType != "" || message.CardState != "" {
		t.Fatalf("失败事务留下消息部分修改: %+v", message)
	}
	conversation, err := fixture.store.ConversationByKey(fixture.key)
	if err != nil || conversation.LastMessageDirection != "in" ||
		conversation.LastMessageKind != "text" || conversation.LastMessageSeq != 2 {
		t.Fatalf("失败事务留下会话部分修改: conversation=%+v err=%v", conversation, err)
	}
	var aggregate CommunicationV4Aggregate
	if err := fixture.store.db.First(&aggregate, "profile_id = ?", fixture.profileID).Error; err != nil {
		t.Fatal(err)
	}
	if aggregate.Revision != fixture.initialV4.Revision || aggregate.ProjectedThroughSeq != 2 ||
		!reflect.DeepEqual(aggregate.State, fixture.initialV4.State) {
		t.Fatalf("失败事务留下聚合部分修改: %+v", aggregate)
	}
	var applications []CommunicationV4ProjectionApplication
	if err := fixture.store.db.Where("profile_id = ?", fixture.profileID).Find(&applications).Error; err != nil {
		t.Fatal(err)
	}
	if len(applications) != 1 || applications[0].InputKey != m5B177CanonicalInputKey ||
		applications[0].InputDigest != fixture.initialApp.InputDigest {
		t.Fatalf("失败事务留下应用键部分修改: %+v", applications)
	}
	var audits int64
	if err := fixture.store.db.Model(&AuditEntry{}).
		Where("category = ?", m5B177RecoveryAuditCategory).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 0 {
		t.Fatalf("失败事务不得留下恢复审计: %d", audits)
	}
}

func assertM5B177RecoveredState(t *testing.T, fixture m5B177IncidentFixture) {
	t.Helper()
	var messages []Message
	if err := fixture.store.db.
		Where(conversationWhere(fixture.key), conversationArgs(fixture.key)...).
		Order("seq").Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || !reflect.DeepEqual(messages[0], fixture.greeting) {
		t.Fatalf("恢复必须原位保留两条物理事实与招呼行: %+v", messages)
	}
	corrected := messages[1]
	if corrected.Seq != 2 || corrected.Direction != "in" || corrected.Kind != "card" ||
		corrected.ContentHash != fixture.freshHash || corrected.CardType != "resumeAttachment" ||
		corrected.CardState != "unknown" || corrected.SourceKey == nil ||
		*corrected.SourceKey != fixture.sourceKey || corrected.Text == nil ||
		*corrected.Text != m5B177ResumeText || corrected.TsApproxMs == nil ||
		*corrected.TsApproxMs != fixture.resumeAtMs || corrected.Origin != "external" ||
		corrected.FirstSeenRoundID != "round-m5b-177-old" ||
		corrected.RetractedAt != nil || corrected.RetractionReason != "" {
		t.Fatalf("事故行未按批准字段原位修正: %+v", corrected)
	}

	conversation, err := fixture.store.ConversationByKey(fixture.key)
	if err != nil || conversation.LastMessageSeq != 2 || conversation.LastMessageDirection != "in" ||
		conversation.LastMessageKind != "card" || conversation.LastMessagePreview != m5B177ResumeText {
		t.Fatalf("会话摘要未同步: conversation=%+v err=%v", conversation, err)
	}
	var aggregate CommunicationV4Aggregate
	if err := fixture.store.db.First(&aggregate, "profile_id = ?", fixture.profileID).Error; err != nil {
		t.Fatal(err)
	}
	if aggregate.Revision != 1 || aggregate.ProjectedThroughSeq != 1 ||
		!reflect.DeepEqual(aggregate.State, fixture.initialV4.State) {
		t.Fatalf("聚合必须保留 revision/state 并只回拨游标: %+v", aggregate)
	}
	var applications []CommunicationV4ProjectionApplication
	if err := fixture.store.db.Where("profile_id = ?", fixture.profileID).Find(&applications).Error; err != nil {
		t.Fatal(err)
	}
	if len(applications) != 1 || applications[0].InputKey != m5B177ArchivedInputKey ||
		applications[0].InputDigest != fixture.initialApp.InputDigest ||
		applications[0].SemanticKind != fixture.initialApp.SemanticKind ||
		applications[0].MessageSeq != 2 || applications[0].FromRevision != 0 ||
		applications[0].ToRevision != 1 ||
		!reflect.DeepEqual(applications[0].Outcome, fixture.initialApp.Outcome) ||
		!applications[0].AppliedAt.Equal(fixture.initialApp.AppliedAt) {
		t.Fatalf("旧 systemNotice 收据必须只改归档键: %+v", applications)
	}
	var audits []AuditEntry
	if err := fixture.store.db.Where("category = ?", m5B177RecoveryAuditCategory).Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].RefMsgID != fixture.freshMsgID ||
		audits[0].Detail != m5B177RecoveryAuditDetail() {
		t.Fatalf("恢复审计错误: %+v", audits)
	}
	for _, secret := range []string{
		fixture.sourceKey, fixture.oldHash, fixture.freshHash, m5B177ResumeText,
	} {
		if strings.Contains(audits[0].Detail, secret) {
			t.Fatalf("恢复审计泄漏候选事实: %q", secret)
		}
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
