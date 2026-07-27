package store

import (
	"errors"
	"testing"
	"time"
)

func TestCorrectLegacyInterviewCardContentHashGatesAndIdempotency(t *testing.T) {
	s := openTest(t)
	req := CorrectLegacyInterviewCardRequest{
		Platform: "zhilian", AccountRef: "account-fix", ConversationRef: "conversation-fix",
		Seq: 3, LegacyHash: "aaaa", CanonicalHash: "bbbb",
		StartsAtMs: 1_785_218_400_000, EndsAtMs: 1_785_220_200_000, Now: time.Now(),
	}

	if err := s.db.Create(&Message{
		Platform: req.Platform, AccountRef: req.AccountRef, ConversationRef: req.ConversationRef,
		Seq: 3, Direction: "out", Kind: "card", CardType: "interviewInvite",
		ContentHash: "aaaa", Origin: "external",
	}).Error; err != nil {
		t.Fatal(err)
	}

	wrong := req
	wrong.LegacyHash = "not-matching"
	if _, err := s.CorrectLegacyInterviewCardContentHash(wrong); !errors.Is(err, ErrInterviewCardCorrectionGate) {
		t.Fatalf("旧哈希不匹配必须拒绝: %v", err)
	}

	result, err := s.CorrectLegacyInterviewCardContentHash(req)
	if err != nil || result.AlreadyCorrected {
		t.Fatalf("首次修正失败: result=%+v err=%v", result, err)
	}
	var row Message
	if err := s.db.First(&row, "conversation_ref = ? AND seq = 3", req.ConversationRef).Error; err != nil {
		t.Fatal(err)
	}
	if row.ContentHash != "bbbb" || row.InterviewStartsAtMs == nil ||
		*row.InterviewStartsAtMs != req.StartsAtMs || row.InterviewMethod == nil ||
		*row.InterviewMethod != "wechatVideo" {
		t.Fatalf("派生列未对齐: %+v", row)
	}
	var audits int64
	if err := s.db.Model(&AuditEntry{}).
		Where("category = ?", "interview_card_hash_correction").Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("必须恰好一条修正审计: %d", audits)
	}

	again, err := s.CorrectLegacyInterviewCardContentHash(req)
	if err != nil || !again.AlreadyCorrected {
		t.Fatalf("重复执行必须幂等: result=%+v err=%v", again, err)
	}
	if err := s.db.Model(&AuditEntry{}).
		Where("category = ?", "interview_card_hash_correction").Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("幂等路径不得追加审计: %d", audits)
	}
}
