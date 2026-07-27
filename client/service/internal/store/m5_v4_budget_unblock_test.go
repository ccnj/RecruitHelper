package store

import (
	"testing"
	"time"
)

func TestUnblockV4BudgetQuotaProfilesSweepsAndIsIdempotent(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

	blockedAtIntent, _ := seedV4BoundaryLockedProfile(t, s, "budget-intent", false)
	if err := s.db.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ?", blockedAtIntent).
		Updates(map[string]any{"manual_reason": "dailyProviderBudgetBlocked"}).Error; err != nil {
		t.Fatal(err)
	}
	intentTurn := historicalBudgetTurn(901, at)
	intentTurn.TurnID = "turn-budget-unblock-intent"
	intentTurn.ProfileID = blockedAtIntent
	intentTurn.Status = DialogueTurnManualRequired
	intentTurn.FailureReason = "dailyProviderBudgetBlocked"
	if err := s.db.Create(&intentTurn).Error; err != nil {
		t.Fatal(err)
	}

	blockedAtReply, _ := seedV4BoundaryLockedProfile(t, s, "budget-reply", false)
	if err := s.db.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ?", blockedAtReply).
		Updates(map[string]any{"manual_reason": "monthlyTurnBudgetBlocked"}).Error; err != nil {
		t.Fatal(err)
	}
	classifiedAt := at.Add(time.Minute)
	replyTurn := historicalBudgetTurn(902, at)
	replyTurn.TurnID = "turn-budget-unblock-reply"
	replyTurn.ProfileID = blockedAtReply
	replyTurn.Status = DialogueTurnManualRequired
	replyTurn.FailureReason = "dailyProviderBudgetBlocked"
	replyTurn.IntentLabel = "rejected"
	replyTurn.ClassifiedAt = &classifiedAt
	if err := s.db.Create(&replyTurn).Error; err != nil {
		t.Fatal(err)
	}

	foreign, _ := seedV4BoundaryLockedProfile(t, s, "budget-foreign", false)

	results, err := s.UnblockV4BudgetQuotaProfiles()
	if err != nil || len(results) != 2 {
		t.Fatalf("解冻未覆盖恰好两个配额档案: results=%+v err=%v", results, err)
	}
	for _, profileID := range []string{blockedAtIntent, blockedAtReply} {
		aggregate, err := s.CommunicationV4AggregateByProfile(profileID)
		if err != nil ||
			aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
			aggregate.ManualReason != "" || aggregate.ManualRequiredAt != nil {
			t.Fatalf("配额档案未恢复 active: profile=%s aggregate=%+v err=%v", profileID, aggregate, err)
		}
	}
	var resetIntent, resetReply DialogueTurn
	if err := s.db.First(&resetIntent, "turn_id = ?", intentTurn.TurnID).Error; err != nil ||
		resetIntent.Status != DialogueTurnCollected || resetIntent.FailureReason != "" {
		t.Fatalf("未分类被挡轮应回 collected: turn=%+v err=%v", resetIntent, err)
	}
	if err := s.db.First(&resetReply, "turn_id = ?", replyTurn.TurnID).Error; err != nil ||
		resetReply.Status != DialogueTurnClassified || resetReply.FailureReason != "" ||
		resetReply.IntentLabel != "rejected" {
		t.Fatalf("已分类被挡轮应回 classified 且保留分类: turn=%+v err=%v", resetReply, err)
	}
	foreignAggregate, err := s.CommunicationV4AggregateByProfile(foreign)
	if err != nil ||
		foreignAggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		foreignAggregate.ManualReason != "outboundBoundaryMissing" {
		t.Fatalf("非配额原因不得被扫掉: aggregate=%+v err=%v", foreignAggregate, err)
	}
	var audits int64
	if err := s.db.Model(&AuditEntry{}).
		Where("category = ?", v4BudgetUnblockAuditCategory).
		Count(&audits).Error; err != nil || audits != 2 {
		t.Fatalf("解冻审计数不符: audits=%d err=%v", audits, err)
	}

	replayed, err := s.UnblockV4BudgetQuotaProfiles()
	if err != nil || len(replayed) != 0 {
		t.Fatalf("二次扫描必须零命中: results=%+v err=%v", replayed, err)
	}
	if err := s.db.Model(&AuditEntry{}).
		Where("category = ?", v4BudgetUnblockAuditCategory).
		Count(&audits).Error; err != nil || audits != 2 {
		t.Fatalf("二次扫描增生审计: audits=%d err=%v", audits, err)
	}
}
