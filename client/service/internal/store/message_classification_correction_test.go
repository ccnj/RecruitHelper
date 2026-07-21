package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

type classificationCorrectionStoreFixture struct {
	store             *Store
	key               ConversationKey
	request           CorrectMessageClassificationRequest
	originalRoundID   string
	correctionRoundID string
	legacyText        string
	legacyHash        string
	sourceKey         string
}

func seedClassificationCorrectionStoreFixture(t *testing.T) classificationCorrectionStoreFixture {
	t.Helper()
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	originalRoundID := "round-classification-original"
	correctionRoundID := "round-classification-correction"
	base := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	for index, roundID := range []string{originalRoundID, correctionRoundID} {
		if err := s.CreatePatrolRound(&PatrolRound{
			Platform: key.Platform, AccountRef: key.AccountRef, RoundID: roundID,
			StartedAt: base.Add(time.Duration(index) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	legacyText := "我暂时不考虑，祝你早日找到合适的人"
	legacyHash := "legacy-rejection-hash"
	timestamp := base.UnixMilli()
	if _, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, RoundID: originalRoundID, ExpectedTailSeq: 1,
		NewMessages: []MessageDraft{{
			Direction: "system", Kind: "system", ContentHash: legacyHash,
			Text: &legacyText, TsApproxMs: &timestamp, Origin: "external",
		}},
		SyncedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Conversation{}).
		Where(conversationWhere(key), conversationArgs(key)...).
		Updates(map[string]any{
			"last_message_direction": "system", "last_message_kind": "system",
			"last_message_preview": legacyText,
		}).Error; err != nil {
		t.Fatal(err)
	}
	sourceKey := strings.Repeat("c", 64)
	request := CorrectMessageClassificationRequest{
		Key: key, RoundID: correctionRoundID, ExpectedTailSeq: 2, OldSeq: 2,
		PauseReason: "userPaused",
		Corrected: MessageDraft{
			Direction: "in", Kind: "text", ContentHash: legacyHash,
			Text: &legacyText, TsApproxMs: &timestamp, Origin: "external", SourceKey: &sourceKey,
		},
		SyncedAt: base.Add(2 * time.Minute),
	}
	return classificationCorrectionStoreFixture{
		store: s, key: key, request: request, originalRoundID: originalRoundID,
		correctionRoundID: correctionRoundID, legacyText: legacyText,
		legacyHash: legacyHash, sourceKey: sourceKey,
	}
}

func TestCorrectMessageClassificationAtomicallyReplacesActiveTailAndIsIdempotent(t *testing.T) {
	fixture := seedClassificationCorrectionStoreFixture(t)
	result, err := fixture.store.CorrectMessageClassification(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if result.AlreadyApplied || result.TailSeq != 3 || result.Corrected.Seq != 3 ||
		result.AdoptedBoundarySeq != 1 {
		t.Fatalf("修正结果错误: %+v", result)
	}

	active, err := fixture.store.MessagesForConversation(fixture.key)
	if err != nil || len(active) != 2 || active[0].Seq != 1 || active[1].Seq != 3 ||
		active[1].Direction != "in" || active[1].Kind != "text" || active[1].SourceKey == nil ||
		*active[1].SourceKey != fixture.sourceKey || active[1].FirstSeenRoundID != fixture.originalRoundID {
		t.Fatalf("活动账本未以修正行取代旧尾: messages=%+v err=%v", active, err)
	}
	facts := physicalMessageFacts(t, fixture.store, fixture.key)
	if len(facts) != 3 || facts[1].RetractedAt == nil ||
		facts[1].RetractionReason != messageRetractionReasonClassificationCorrected ||
		facts[1].SourceKey != nil || facts[2].RetractedAt != nil {
		t.Fatalf("旧事实撤回或新事实追加错误: %+v", facts)
	}
	firstRetractionAt := *facts[1].RetractedAt
	conversation, err := fixture.store.ConversationByKey(fixture.key)
	if err != nil || conversation.LastMessageSeq != 3 || conversation.AdoptedBoundarySeq != 1 ||
		conversation.LastMessageDirection != "in" || conversation.LastMessageKind != "text" ||
		conversation.LastMessagePreview != fixture.legacyText {
		t.Fatalf("会话活动尾与摘要未同步修正: conversation=%+v err=%v", conversation, err)
	}
	account, err := fixture.store.AccountByKey(AccountKey{
		Platform: fixture.key.Platform, AccountRef: fixture.key.AccountRef,
	})
	if err != nil || account.StoppedAt == nil || !account.StoppedAt.Equal(fixture.request.SyncedAt) ||
		account.PausedReason != fixture.request.PauseReason || !account.DirtyHint {
		t.Fatalf("修正与账号暂停必须同事务落下: account=%+v err=%v", account, err)
	}
	originalRound, _ := fixture.store.PatrolRoundByKey(
		fixture.key.Platform, fixture.key.AccountRef, fixture.originalRoundID,
	)
	correctionRound, _ := fixture.store.PatrolRoundByKey(
		fixture.key.Platform, fixture.key.AccountRef, fixture.correctionRoundID,
	)
	if originalRound.NewMessageCount != 1 || correctionRound.NewMessageCount != 0 {
		t.Fatalf("修正不得改变任一轮的新增消息计数: original=%+v correction=%+v", originalRound, correctionRound)
	}
	audits, _ := fixture.store.AuditEntries(50)
	if got := correctionAuditCount(audits); got != 1 {
		t.Fatalf("修正审计数量=%d, want 1: %+v", got, audits)
	}
	expectedDetail := fmt.Sprintf(
		"oldSeq=2 newSeq=3 from=system/system to=in/text reason=classification_corrected roundId=%s",
		fixture.correctionRoundID,
	)
	for _, audit := range audits {
		if audit.Category != "conversation_message_classification_corrected" {
			continue
		}
		if audit.Detail != expectedDetail || strings.Contains(audit.Detail, fixture.sourceKey) ||
			strings.Contains(audit.Detail, fixture.legacyHash) || strings.Contains(audit.Detail, fixture.legacyText) {
			t.Fatalf("修正审计内容错误或泄露正文/哈希/等值键: %+v", audit)
		}
	}

	replayed, err := fixture.store.CorrectMessageClassification(fixture.request)
	if err != nil || !replayed.AlreadyApplied || replayed.Corrected.Seq != 3 || replayed.TailSeq != 3 {
		t.Fatalf("相同请求必须幂等成功: result=%+v err=%v", replayed, err)
	}
	facts = physicalMessageFacts(t, fixture.store, fixture.key)
	audits, _ = fixture.store.AuditEntries(50)
	if len(facts) != 3 || facts[1].RetractedAt == nil || !facts[1].RetractedAt.Equal(firstRetractionAt) ||
		correctionAuditCount(audits) != 1 {
		t.Fatalf("幂等重放不得改写事实或增生审计: facts=%+v audits=%+v", facts, audits)
	}
	account, _ = fixture.store.AccountByKey(AccountKey{
		Platform: fixture.key.Platform, AccountRef: fixture.key.AccountRef,
	})
	if account.StoppedAt == nil || !account.StoppedAt.Equal(fixture.request.SyncedAt) ||
		account.PausedReason != fixture.request.PauseReason || !account.DirtyHint {
		t.Fatalf("幂等重放不得丢失或改写暂停事实: %+v", account)
	}
	if err := fixture.store.MutateAccount(AccountKey{
		Platform: fixture.key.Platform, AccountRef: fixture.key.AccountRef,
	}, func(account *Account) error {
		account.StoppedAt = nil
		account.PausedReason = ""
		account.DirtyHint = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CorrectMessageClassification(fixture.request); !errors.Is(err, ErrMessageClassificationCorrectionUnsafe) {
		t.Fatalf("修正事实存在但暂停事实缺失时不得伪报幂等成功: %v", err)
	}
	facts = physicalMessageFacts(t, fixture.store, fixture.key)
	audits, _ = fixture.store.AuditEntries(50)
	if len(facts) != 3 || correctionAuditCount(audits) != 1 {
		t.Fatalf("不完整幂等链不得增生事实或审计: facts=%+v audits=%+v", facts, audits)
	}
}

func TestCorrectMessageClassificationLateAuditFailureRollsBackEverything(t *testing.T) {
	fixture := seedClassificationCorrectionStoreFixture(t)
	forced := errors.New("forced classification correction audit failure")
	callbackName := "test:fail_classification_correction_audit"
	if err := fixture.store.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "AuditEntry" {
			return
		}
		entry, ok := tx.Statement.Dest.(*AuditEntry)
		if ok && entry.Category == "conversation_message_classification_corrected" {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer fixture.store.db.Callback().Create().Remove(callbackName)

	_, err := fixture.store.CorrectMessageClassification(fixture.request)
	if !errors.Is(err, forced) {
		t.Fatalf("必须返回审计失败: %v", err)
	}
	facts := physicalMessageFacts(t, fixture.store, fixture.key)
	conversation, _ := fixture.store.ConversationByKey(fixture.key)
	account, _ := fixture.store.AccountByKey(AccountKey{
		Platform: fixture.key.Platform, AccountRef: fixture.key.AccountRef,
	})
	audits, _ := fixture.store.AuditEntries(50)
	if len(facts) != 2 || facts[1].RetractedAt != nil || facts[1].RetractionReason != "" ||
		conversation.LastMessageSeq != 2 || conversation.LastMessageDirection != "system" ||
		conversation.LastMessageKind != "system" || conversation.LastMessagePreview != fixture.legacyText ||
		account.StoppedAt != nil || account.PausedReason != "" ||
		correctionAuditCount(audits) != 0 {
		t.Fatalf("审计失败后事务出现部分写: facts=%+v conversation=%+v account=%+v audits=%+v",
			facts, conversation, account, audits)
	}
}

func TestCorrectMessageClassificationRejectsStaleTailWithoutPartialWrite(t *testing.T) {
	fixture := seedClassificationCorrectionStoreFixture(t)
	competitor := "另一条正常消息"
	if _, err := fixture.store.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: fixture.key, RoundID: fixture.correctionRoundID, ExpectedTailSeq: 2,
		NewMessages: []MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: "competitor-hash",
			Text: &competitor, Origin: "external",
		}},
		SyncedAt: fixture.request.SyncedAt,
	}); err != nil {
		t.Fatal(err)
	}
	before := physicalMessageFacts(t, fixture.store, fixture.key)
	_, err := fixture.store.CorrectMessageClassification(fixture.request)
	if !errors.Is(err, ErrConversationVersionConflict) {
		t.Fatalf("被并发消息抢先推进的计划必须 CAS 失败: %v", err)
	}
	after := physicalMessageFacts(t, fixture.store, fixture.key)
	audits, _ := fixture.store.AuditEntries(50)
	if len(after) != len(before) || after[1].RetractedAt != nil || correctionAuditCount(audits) != 0 {
		t.Fatalf("CAS 失败不得撤回旧行、追加修正或留审计: before=%+v after=%+v audits=%+v", before, after, audits)
	}
}

func TestCorrectMessageClassificationSourceKeyConflictIsPrivateAndZeroWrite(t *testing.T) {
	fixture := seedClassificationCorrectionStoreFixture(t)
	conflictingText := "冲突正文不得泄露"
	conflictingHash := "conflicting-private-hash"
	if err := fixture.store.db.Create(&Message{
		Platform: fixture.key.Platform, AccountRef: fixture.key.AccountRef,
		ConversationRef: fixture.key.ConversationRef, Seq: 3,
		Direction: "out", Kind: "text", ContentHash: conflictingHash,
		Text: &conflictingText, Origin: "external", SourceKey: &fixture.sourceKey,
		FirstSeenRoundID: fixture.originalRoundID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	before := physicalMessageFacts(t, fixture.store, fixture.key)
	_, err := fixture.store.CorrectMessageClassification(fixture.request)
	if !errors.Is(err, ErrMessageSourceKeyConflict) {
		t.Fatalf("作用域内同 key 方向/哈希冲突必须专用失败: %v", err)
	}
	for _, secret := range []string{fixture.sourceKey, fixture.legacyText, fixture.legacyHash, conflictingText, conflictingHash} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("冲突错误泄露敏感内容 %q: %v", secret, err)
		}
	}
	after := physicalMessageFacts(t, fixture.store, fixture.key)
	audits, _ := fixture.store.AuditEntries(50)
	if len(after) != len(before) || after[1].RetractedAt != nil || correctionAuditCount(audits) != 0 {
		t.Fatalf("sourceKey 冲突不得产生任何修正写: before=%+v after=%+v audits=%+v", before, after, audits)
	}
}

func correctionAuditCount(entries []AuditEntry) int {
	count := 0
	for _, entry := range entries {
		if entry.Category == "conversation_message_classification_corrected" {
			count++
		}
	}
	return count
}
