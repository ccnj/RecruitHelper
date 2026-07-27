package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/textcanon"
)

func seedV4BoundaryLockedProfile(
	t *testing.T,
	s *Store,
	suffix string,
	stripAnchor bool,
) (string, ConversationKey) {
	t.Helper()
	at := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	profileID := "profile-lock-" + suffix
	conversationRef := "conversation-lock-" + suffix
	ledger, _ := seedSuccessfulV4Greeting(t, s, profileID, conversationRef, at)
	if stripAnchor {
		stripV4AnchorForHistoricalShape(t, s, profileID)
	}
	key := ConversationKey{
		Platform: ledger.Platform, AccountRef: ledger.AccountRef,
		ConversationRef: conversationRef,
	}
	systemText := "[系统消息:99]"
	systemAt := at.Add(time.Minute).UnixMilli()
	if err := s.db.Create(&Message{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: conversationRef,
		Seq: 2, Direction: "system", Kind: "system",
		ContentHash: textcanon.Hash(systemText), Text: &systemText,
		TsApproxMs: &systemAt, Origin: "external", FirstSeenRoundID: "round-lock-" + suffix,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCommunicationV4BusinessEvent(ApplyCommunicationV4BusinessEventRequest{
		ProfileID: profileID,
		Event: communication.BusinessEvent{
			Key: "message:2", Kind: communication.EventSystemNotice,
			Source: communication.EventSourceMessage, MessageSeq: 2,
		},
		AppliedAt: at.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	replyText := "我不考虑换行业"
	replyAt := at.Add(3 * time.Minute).UnixMilli()
	fullSourceKey := textcanon.Hash("source-" + suffix)
	if err := s.db.Create(&Message{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: conversationRef,
		Seq: 3, Direction: "in", Kind: "text",
		ContentHash: textcanon.Hash(replyText), Text: &replyText,
		TsApproxMs: &replyAt, Origin: "external",
		FirstSeenRoundID: "round-lock-" + suffix, SourceKey: &fullSourceKey,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCommunicationV4AutomationManualRequired(
		profileID, "outboundBoundaryMissing", at.Add(4*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	return profileID, key
}

func TestRecoverV4OutboundBoundaryLockUnfreezesAndIsIdempotent(t *testing.T) {
	s := openTest(t)
	profileID, key := seedV4BoundaryLockedProfile(t, s, "main", true)

	result, err := s.RecoverV4OutboundBoundaryLock(profileID)
	if err != nil || !result.Applied || result.AlreadyRecovered ||
		result.CursorSeq != 2 || result.AnchorSeq != 1 || result.UncoveredInboundSeq != 3 {
		t.Fatalf("恢复未按事实门解除冻结: result=%+v err=%v", result, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(profileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" || aggregate.ManualRequiredAt != nil ||
		aggregate.ProjectedThroughSeq != 2 {
		t.Fatalf("恢复改动超出授权范围: aggregate=%+v err=%v", aggregate, err)
	}
	var audits int64
	if err := s.db.Model(&AuditEntry{}).
		Where(
			"category = ? AND platform = ? AND account_ref = ? AND conversation_ref = ?",
			v4BoundaryRecoveryAuditCategory, key.Platform, key.AccountRef, key.ConversationRef,
		).
		Count(&audits).Error; err != nil || audits != 1 {
		t.Fatalf("恢复审计缺失: audits=%d err=%v", audits, err)
	}

	replayed, err := s.RecoverV4OutboundBoundaryLock(profileID)
	if err != nil || replayed.Applied || !replayed.AlreadyRecovered {
		t.Fatalf("恢复重放不幂等: result=%+v err=%v", replayed, err)
	}
	if err := s.db.Model(&AuditEntry{}).
		Where("category = ?", v4BoundaryRecoveryAuditCategory).
		Count(&audits).Error; err != nil || audits != 1 {
		t.Fatalf("恢复重放增生审计: audits=%d err=%v", audits, err)
	}
}

func TestRecoverV4OutboundBoundaryLockRefusesForeignShapes(t *testing.T) {
	s := openTest(t)

	wrongReason, _ := seedV4BoundaryLockedProfile(t, s, "wrong-reason", false)
	if err := s.db.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ?", wrongReason).
		Update("manual_reason", "unsupportedSemantic").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverV4OutboundBoundaryLock(wrongReason); !errors.Is(err, ErrV4BoundaryRecoveryUnsafe) {
		t.Fatalf("非本事故原因必须拒绝: %v", err)
	}

	noInbound := "profile-lock-no-inbound"
	at := time.Date(2026, 7, 27, 9, 30, 0, 0, time.UTC)
	ledger, _ := seedSuccessfulV4Greeting(t, s, noInbound, "conversation-lock-no-inbound", at)
	systemText := "[系统消息:104]"
	systemAt := at.Add(time.Minute).UnixMilli()
	if err := s.db.Create(&Message{
		Platform: ledger.Platform, AccountRef: ledger.AccountRef,
		ConversationRef: "conversation-lock-no-inbound",
		Seq:             2, Direction: "system", Kind: "system",
		ContentHash: textcanon.Hash(systemText), Text: &systemText,
		TsApproxMs: &systemAt, Origin: "external", FirstSeenRoundID: "round-no-inbound",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCommunicationV4BusinessEvent(ApplyCommunicationV4BusinessEventRequest{
		ProfileID: noInbound,
		Event: communication.BusinessEvent{
			Key: "message:2", Kind: communication.EventSystemNotice,
			Source: communication.EventSourceMessage, MessageSeq: 2,
		},
		AppliedAt: at.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCommunicationV4AutomationManualRequired(
		noInbound, "outboundBoundaryMissing", at.Add(3*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverV4OutboundBoundaryLock(noInbound); !errors.Is(err, ErrV4BoundaryRecoveryUnsafe) {
		t.Fatalf("无未处理候选人消息必须拒绝: %v", err)
	}
}
