package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"recruithelper/contract/gen/go/protocol"
)

type m5Batch6IncidentFixture struct {
	store              *Store
	key                ConversationKey
	freshMsgID         string
	rebaselineRoundID  string
	greetingText       string
	rejectionText      string
	greetingHash       string
	rejectionHash      string
	rejectionSourceKey string
}

func seedM5Batch6IncidentFixture(t *testing.T) m5Batch6IncidentFixture {
	t.Helper()
	s := openTest(t)
	terminalAt := time.Now().UTC().Add(-time.Minute)
	base := terminalAt.Add(-21 * time.Minute)
	key := ConversationKey{
		Platform: "zhilian", AccountRef: "account-m5-batch6-incident", ConversationRef: "conversation-m5-batch6-incident",
	}
	pausedAt := terminalAt.Add(-30 * time.Second)
	if err := s.db.Create(&Account{
		Platform: key.Platform, AccountRef: key.AccountRef, IdentityState: IdentityVerified,
		StoppedAt: &pausedAt, PausedReason: "userPaused", DirtyHint: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&Conversation{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		PlatformUserRef: "candidate-m5-batch6-incident", TrackingState: TrackingAdopted,
		AdoptedBoundarySeq: 0, LastMessageSeq: 6, LastMessageDirection: "in", LastMessageKind: "text",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&TrackedIntent{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		Status: TrackingAdopted, RequestedBy: "fixture", RequestedAt: base, AdoptedAt: &base,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&Candidate{
		Platform: key.Platform, PlatformUserRef: "candidate-m5-batch6-incident",
		FirstSeenAt: base, LastSeenAt: base,
	}).Error; err != nil {
		t.Fatal(err)
	}
	conversationRef := key.ConversationRef
	if err := s.db.Create(&CandidateProfile{
		ProfileID: "profile-m5-batch6-incident", Platform: key.Platform, AccountRef: key.AccountRef,
		PlatformUserRef: "candidate-m5-batch6-incident", PositionRef: "position-m5-batch6-incident",
		MainStatus: CandidateProfileCommunicating, ConversationRef: &conversationRef,
		ResumeCaptureState: ResumeCaptureCaptured,
	}).Error; err != nil {
		t.Fatal(err)
	}

	greetingText := "你好"
	rejectionText := "暂时不考虑"
	precedingOne := "前置系统事实一"
	precedingTwo := "前置系统事实二"
	greetingHash := "fixture-greeting-content-hash"
	rejectionHash := "fixture-rejection-content-hash"
	rejectionAt := base.Add(4 * time.Minute).UnixMilli()
	greetingAt := base.Add(3 * time.Minute).UnixMilli()
	outboundIntentID := "fixture-greeting-intent"
	rebaselineRoundID := "round-m5-batch6-erroneous-rebaseline"
	source3 := strings.Repeat("a", 64)
	source4 := strings.Repeat("b", 64)
	source5 := strings.Repeat("c", 64)
	source6 := strings.Repeat("d", 64)
	messages := []Message{
		{
			Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
			Seq: 1, Direction: "out", Kind: "text", ContentHash: greetingHash, Text: &greetingText,
			TsApproxMs: &greetingAt, Origin: "self", FirstSeenRoundID: "round-original",
			OutboundIntentID: &outboundIntentID,
		},
		{
			Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
			Seq: 2, Direction: "system", Kind: "system", ContentHash: rejectionHash, Text: &rejectionText,
			TsApproxMs: &rejectionAt, Origin: "external", FirstSeenRoundID: "round-original",
		},
		{
			Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
			Seq: 3, Direction: "system", Kind: "system", ContentHash: "fixture-preceding-one-hash", Text: &precedingOne,
			TsApproxMs: &greetingAt, Origin: "external", FirstSeenRoundID: rebaselineRoundID, SourceKey: &source3,
		},
		{
			Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
			Seq: 4, Direction: "system", Kind: "system", ContentHash: "fixture-preceding-two-hash", Text: &precedingTwo,
			TsApproxMs: &greetingAt, Origin: "external", FirstSeenRoundID: rebaselineRoundID, SourceKey: &source4,
		},
		{
			Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
			Seq: 5, Direction: "out", Kind: "text", ContentHash: greetingHash, Text: &greetingText,
			TsApproxMs: &greetingAt, Origin: "external", FirstSeenRoundID: rebaselineRoundID, SourceKey: &source5,
		},
		{
			Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
			Seq: 6, Direction: "in", Kind: "text", ContentHash: rejectionHash, Text: &rejectionText,
			TsApproxMs: &rejectionAt, Origin: "external", FirstSeenRoundID: rebaselineRoundID, SourceKey: &source6,
		},
	}
	if err := s.db.Create(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Conversation{}).Where(conversationWhere(key), conversationArgs(key)...).
		Update("last_message_preview", rejectionText).Error; err != nil {
		t.Fatal(err)
	}
	rebaselineAt := base.Add(10 * time.Minute)
	if err := s.db.Create(&AuditEntry{
		At: rebaselineAt, Category: "conversation_zero_overlap_rebaseline",
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		RoundID: rebaselineRoundID,
		Detail:  "oldTail=2 historicalFrom=3 historicalThrough=6 imported=4",
	}).Error; err != nil {
		t.Fatal(err)
	}

	argsRaw, err := json.Marshal(protocol.ChatReadThreadArgs{
		ConversationRef: key.ConversationRef,
		Window:          protocol.ThreadWindow{Deep: true, MaxMessages: protocol.DefaultPaginationReadThreadMaxItems},
	})
	if err != nil {
		t.Fatal(err)
	}
	threadData := protocol.ChatReadThreadData{
		Complete: true, ReachedTop: true,
		Messages: []protocol.ThreadMessage{
			{
				Idx: 0, Direction: protocol.MessageDirectionSystem, Kind: protocol.MessageKindSystem,
				ContentHash: messages[2].ContentHash, Text: &precedingOne, TsApprox: &greetingAt, SourceKey: source3,
			},
			{
				Idx: 1, Direction: protocol.MessageDirectionSystem, Kind: protocol.MessageKindSystem,
				ContentHash: messages[3].ContentHash, Text: &precedingTwo, TsApprox: &greetingAt, SourceKey: source4,
			},
			{
				Idx: 2, Direction: protocol.MessageDirectionOut, Kind: protocol.MessageKindText,
				ContentHash: greetingHash, Text: &greetingText, TsApprox: &greetingAt, SourceKey: source5,
			},
			{
				Idx: 3, Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindText,
				ContentHash: rejectionHash, Text: &rejectionText, TsApprox: &rejectionAt, SourceKey: source6,
			},
		},
	}
	dataRaw, err := json.Marshal(threadData)
	if err != nil {
		t.Fatal(err)
	}
	freshMsgID := "msg-fresh-m5-batch6-proof"
	resultRaw, err := json.Marshal(protocol.ResultBody{
		Ref: freshMsgID, Status: protocol.ResultStatusOk, Data: dataRaw, ExecMs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := base.Add(20 * time.Minute)
	if err := s.db.Create(&CmdRecord{
		MsgID: freshMsgID, Name: protocol.PrimChatReadThread, Class: string(protocol.ClassIntrusive),
		Domain: key.Platform + ":" + key.AccountRef, Platform: key.Platform, AccountRef: key.AccountRef,
		Args: string(argsRaw), HandID: "hand-m5-batch6-proof", LogicalDispatchID: freshMsgID,
		Status: CmdOk, ResultBody: string(resultRaw), CreatedAt: createdAt, UpdatedAt: terminalAt, TerminalAt: &terminalAt,
	}).Error; err != nil {
		t.Fatal(err)
	}

	return m5Batch6IncidentFixture{
		store: s, key: key, freshMsgID: freshMsgID, rebaselineRoundID: rebaselineRoundID,
		greetingText: greetingText, rejectionText: rejectionText,
		greetingHash: greetingHash, rejectionHash: rejectionHash, rejectionSourceKey: source6,
	}
}

func TestRecoverM5Batch6IncidentAtomicallyConvergesAndReplaysIdempotently(t *testing.T) {
	fixture := seedM5Batch6IncidentFixture(t)
	result, err := fixture.store.RecoverM5Batch6Incident(fixture.freshMsgID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.AlreadyApplied || !result.FreshTailUnique || !result.ReachedTop ||
		!result.SourceKeyMatched || !result.ContentHashMatched || !result.ShapeMatched ||
		result.ActiveBefore != 6 || result.ActiveAfter != 2 {
		t.Fatalf("恢复结果错误: %+v", result)
	}
	assertM5Batch6RecoveredFacts(t, fixture)

	replayed, err := fixture.store.RecoverM5Batch6Incident(fixture.freshMsgID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Applied || !replayed.AlreadyApplied || replayed.ActiveBefore != 2 || replayed.ActiveAfter != 2 {
		t.Fatalf("完整重放必须幂等: %+v", replayed)
	}
	assertM5Batch6RecoveredFacts(t, fixture)

	staleTerminal := time.Now().Add(-m5Batch6FreshProofMaxAge - time.Minute)
	if err := fixture.store.db.Model(&CmdRecord{}).Where("msg_id = ?", fixture.freshMsgID).
		Updates(map[string]any{"terminal_at": staleTerminal, "updated_at": staleTerminal}).Error; err != nil {
		t.Fatal(err)
	}
	staleReplay, err := fixture.store.RecoverM5Batch6Incident(fixture.freshMsgID)
	if err != nil || !staleReplay.AlreadyApplied || staleReplay.Applied {
		t.Fatalf("已提交事故事务的同 msgId 晚到重放仍须幂等: result=%+v err=%v", staleReplay, err)
	}
}

func TestRecoverM5Batch6IncidentRejectsFreshProofMismatchWithoutWrites(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*protocol.ThreadMessage)
	}{
		{
			name: "sourceKey proof false",
			mutate: func(message *protocol.ThreadMessage) {
				message.SourceKey = strings.Repeat("e", 64)
			},
		},
		{
			name: "contentHash proof false",
			mutate: func(message *protocol.ThreadMessage) {
				message.ContentHash = "different-content-hash"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := seedM5Batch6IncidentFixture(t)
			mutateM5Batch6FreshTail(t, fixture, test.mutate)
			if _, err := fixture.store.RecoverM5Batch6Incident(fixture.freshMsgID); !errors.Is(err, ErrM5Batch6IncidentRecoveryUnsafe) {
				t.Fatalf("proof 漂移必须停止: %v", err)
			}
			assertM5Batch6IncidentUnchanged(t, fixture)
		})
	}
}

func TestRecoverM5Batch6IncidentRejectsStaleFreshProofWithoutWrites(t *testing.T) {
	fixture := seedM5Batch6IncidentFixture(t)
	staleTerminal := time.Now().Add(-m5Batch6FreshProofMaxAge - time.Minute)
	if err := fixture.store.db.Model(&CmdRecord{}).Where("msg_id = ?", fixture.freshMsgID).
		Updates(map[string]any{"terminal_at": staleTerminal, "updated_at": staleTerminal}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.RecoverM5Batch6Incident(fixture.freshMsgID); !errors.Is(err, ErrM5Batch6IncidentRecoveryUnsafe) {
		t.Fatalf("超过窄窗口的 readThread 结果必须停止: %v", err)
	}
	assertM5Batch6IncidentUnchanged(t, fixture)
}

func TestRecoverM5Batch6IncidentRejectsWrongRoundOrShapeWithoutWrites(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(m5Batch6IncidentFixture)
	}{
		{
			name: "rebaseline round drift",
			mutate: func(fixture m5Batch6IncidentFixture) {
				if err := fixture.store.db.Model(&Message{}).
					Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = 4",
						fixture.key.Platform, fixture.key.AccountRef, fixture.key.ConversationRef).
					Update("first_seen_round_id", "different-round").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "physical shape drift",
			mutate: func(fixture m5Batch6IncidentFixture) {
				if err := fixture.store.db.Model(&Message{}).
					Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = 3",
						fixture.key.Platform, fixture.key.AccountRef, fixture.key.ConversationRef).
					Update("direction", "in").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "rebaseline audit drift",
			mutate: func(fixture m5Batch6IncidentFixture) {
				if err := fixture.store.db.Model(&AuditEntry{}).
					Where("category = ?", "conversation_zero_overlap_rebaseline").
					Update("detail", "oldTail=2 historicalFrom=3 historicalThrough=5 imported=3").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := seedM5Batch6IncidentFixture(t)
			test.mutate(fixture)
			if _, err := fixture.store.RecoverM5Batch6Incident(fixture.freshMsgID); !errors.Is(err, ErrM5Batch6IncidentRecoveryUnsafe) {
				t.Fatalf("事故形态漂移必须停止: %v", err)
			}
			assertM5Batch6NoRecoveryAudit(t, fixture.store)
		})
	}
}

func TestRecoverM5Batch6IncidentRejectsPartialRecoveryState(t *testing.T) {
	fixture := seedM5Batch6IncidentFixture(t)
	retractedAt := time.Now()
	if err := fixture.store.db.Model(&Message{}).
		Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = 2",
			fixture.key.Platform, fixture.key.AccountRef, fixture.key.ConversationRef).
		Updates(map[string]any{
			"retracted_at": retractedAt, "retraction_reason": messageRetractionReasonClassificationCorrected,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.RecoverM5Batch6Incident(fixture.freshMsgID); !errors.Is(err, ErrM5Batch6IncidentRecoveryUnsafe) {
		t.Fatalf("部分恢复态必须停止: %v", err)
	}
	facts := physicalMessageFacts(t, fixture.store, fixture.key)
	if facts[1].RetractedAt == nil {
		t.Fatal("fixture 的部分撤回事实意外丢失")
	}
	for _, index := range []int{2, 3, 4} {
		if facts[index].RetractedAt != nil {
			t.Fatalf("部分态停止后不得继续撤回 seq=%d", facts[index].Seq)
		}
	}
	assertM5Batch6NoRecoveryAudit(t, fixture.store)
}

func TestRecoverM5Batch6IncidentAuditFailureRollsBackAllRetractions(t *testing.T) {
	fixture := seedM5Batch6IncidentFixture(t)
	forced := errors.New("forced m5 batch6 recovery audit failure")
	callbackName := "test:fail_m5_batch6_recovery_audit"
	if err := fixture.store.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "AuditEntry" {
			return
		}
		entry, ok := tx.Statement.Dest.(*AuditEntry)
		if ok && entry.Category == m5Batch6RecoveryAuditCategory {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer fixture.store.db.Callback().Create().Remove(callbackName)

	if _, err := fixture.store.RecoverM5Batch6Incident(fixture.freshMsgID); !errors.Is(err, forced) {
		t.Fatalf("必须返回审计失败: %v", err)
	}
	assertM5Batch6IncidentUnchanged(t, fixture)
}

func assertM5Batch6RecoveredFacts(t *testing.T, fixture m5Batch6IncidentFixture) {
	t.Helper()
	active, err := fixture.store.MessagesForConversation(fixture.key)
	if err != nil || len(active) != 2 || active[0].Seq != 1 || active[1].Seq != 6 {
		t.Fatalf("活动账本没有收敛为 [1,6]: messages=%+v err=%v", active, err)
	}
	facts := physicalMessageFacts(t, fixture.store, fixture.key)
	wantReasons := map[int64]string{
		2: messageRetractionReasonClassificationCorrected,
		3: m5Batch6RetractionReasonPrecedingHistory1,
		4: m5Batch6RetractionReasonPrecedingHistory2,
		5: m5Batch6RetractionReasonDuplicateGreeting,
	}
	if len(facts) != 6 || facts[0].RetractedAt != nil || facts[5].RetractedAt != nil {
		t.Fatalf("物理事实行必须全部保留且仅保留 seq1/6 活动: %+v", facts)
	}
	for _, message := range facts[1:5] {
		if message.RetractedAt == nil || message.RetractionReason != wantReasons[message.Seq] {
			t.Fatalf("撤回原因错误: seq=%d reason=%q", message.Seq, message.RetractionReason)
		}
	}
	conversation, err := fixture.store.ConversationByKey(fixture.key)
	if err != nil || conversation.AdoptedBoundarySeq != 0 || conversation.LastMessageSeq != 6 || conversation.LastMessageDirection != "in" ||
		conversation.LastMessageKind != "text" || conversation.LastMessagePreview != fixture.rejectionText {
		t.Fatalf("活动尾摘要错误: conversation=%+v err=%v", conversation, err)
	}
	account, err := fixture.store.AccountByKey(AccountKey{Platform: fixture.key.Platform, AccountRef: fixture.key.AccountRef})
	if err != nil || account.StoppedAt == nil || account.PausedReason != "userPaused" {
		t.Fatalf("恢复不得解除账号暂停: account=%+v err=%v", account, err)
	}
	var audits []AuditEntry
	if err := fixture.store.db.Where("category = ?", m5Batch6RecoveryAuditCategory).Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].Detail != m5Batch6RecoveryAuditDetail() ||
		audits[0].Platform != fixture.key.Platform || audits[0].AccountRef != fixture.key.AccountRef ||
		audits[0].ConversationRef != fixture.key.ConversationRef ||
		audits[0].RoundID != fixture.rebaselineRoundID || audits[0].HandID != "" ||
		audits[0].RefMsgID != fixture.freshMsgID {
		t.Fatalf("恢复审计数量、内容或脱敏边界错误: %+v", audits)
	}
	for _, secret := range []string{
		fixture.key.Platform, fixture.key.AccountRef, fixture.key.ConversationRef,
		fixture.greetingText, fixture.rejectionText, fixture.greetingHash,
		fixture.rejectionHash, fixture.rejectionSourceKey, fixture.rebaselineRoundID, fixture.freshMsgID,
	} {
		if strings.Contains(audits[0].Detail, secret) {
			t.Fatalf("恢复审计泄露敏感事实: %q", secret)
		}
	}
}

func assertM5Batch6IncidentUnchanged(t *testing.T, fixture m5Batch6IncidentFixture) {
	t.Helper()
	facts := physicalMessageFacts(t, fixture.store, fixture.key)
	if len(facts) != 6 {
		t.Fatalf("事故事实数量漂移: %d", len(facts))
	}
	for _, message := range facts {
		if message.RetractedAt != nil || message.RetractionReason != "" {
			t.Fatalf("失败事务不得撤回 seq=%d", message.Seq)
		}
	}
	conversation, err := fixture.store.ConversationByKey(fixture.key)
	if err != nil || conversation.AdoptedBoundarySeq != 0 || conversation.LastMessageSeq != 6 || conversation.LastMessagePreview != fixture.rejectionText {
		t.Fatalf("失败事务不得改变活动尾: conversation=%+v err=%v", conversation, err)
	}
	assertM5Batch6NoRecoveryAudit(t, fixture.store)
}

func assertM5Batch6NoRecoveryAudit(t *testing.T, s *Store) {
	t.Helper()
	var count int64
	if err := s.db.Model(&AuditEntry{}).Where("category = ?", m5Batch6RecoveryAuditCategory).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("失败/部分状态不得产生恢复审计: %d", count)
	}
}

func mutateM5Batch6FreshTail(
	t *testing.T,
	fixture m5Batch6IncidentFixture,
	mutate func(*protocol.ThreadMessage),
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
	mutate(&data.Messages[len(data.Messages)-1])
	dataRaw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	result.Data = dataRaw
	resultRaw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.db.Model(&CmdRecord{}).Where("msg_id = ?", fixture.freshMsgID).
		Update("result_body", string(resultRaw)).Error; err != nil {
		t.Fatal(err)
	}
}
