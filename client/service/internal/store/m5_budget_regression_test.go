package store

import (
	"fmt"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

func historicalBudgetTurn(index int, at time.Time) DialogueTurn {
	return DialogueTurn{
		TurnID:              fmt.Sprintf("turn-budget-history-%03d", index),
		ProfileID:           fmt.Sprintf("profile-budget-history-%03d", index),
		ConversationRef:     fmt.Sprintf("conversation-budget-history-%03d", index),
		InputDigest:         fmt.Sprintf("digest-budget-history-%03d", index),
		HistoryThroughSeq:   1,
		InboundFromSeq:      2,
		InboundThroughSeq:   2,
		ContextRevisionHash: "revision-budget-history",
		ResumeSnapshotID:    fmt.Sprintf("snapshot-budget-history-%03d", index),
		RecommendedTimeText: "合成推荐时段",
		RenderFormatVersion: m5ai.DialogueRenderFormatVersion,
		Status:              DialogueTurnCompleted,
		CreatedAt:           at,
		UpdatedAt:           at,
	}
}

// 2026-07-27 甲方裁决废除全局调用量配额（每日 20 次 provider、每月 100 轮）。
// 本回归钉死"配额闸不存在"：超出旧上限的调用与建轮必须照常放行，
// 不产生 budget 类错误或 manualRequired。每用途 token 上限另有其闸，不在此测。
func TestAIInvocationProceedsBeyondFormerDailyQuota(t *testing.T) {
	s := openTest(t)
	_, turn := seedFrozenDialogueTurn(t, s, "profile-provider-daily-quota")
	at := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	finishedAt := at.Add(time.Minute)
	zero := 0
	for index := 1; index <= 20; index++ {
		historical := historicalBudgetTurn(index, at.Add(-time.Duration(index)*time.Minute))
		invocation := AIInvocation{
			InvocationID:        fmt.Sprintf("invocation-provider-quota-history-%02d", index),
			TurnID:              historical.TurnID,
			Purpose:             m5ai.PurposeIntent,
			Attempt:             1,
			Provider:            "deepseek",
			Model:               "deepseek-v4-pro",
			ContextRevisionHash: historical.ContextRevisionHash,
			InputHash:           fmt.Sprintf("input-provider-quota-history-%02d", index),
			OutputHash:          fmt.Sprintf("output-provider-quota-history-%02d", index),
			ReasoningTokens:     &zero,
			UsageShape:          AIInvocationUsageComplete,
			Status:              AIInvocationOK,
			CreatedAt:           historical.CreatedAt,
			FinishedAt:          &finishedAt,
		}
		if err := s.db.Create(&historical).Error; err != nil {
			t.Fatalf("写入历史 turn[%d]: %v", index, err)
		}
		if err := s.db.Create(&invocation).Error; err != nil {
			t.Fatalf("写入历史 invocation[%d]: %v", index, err)
		}
	}

	beyond := ReserveAIInvocationRequest{
		InvocationID: "invocation-provider-quota-beyond",
		TurnID:       turn.TurnID,
		Purpose:      m5ai.PurposeIntent,
		Attempt:      1,
		Provider:     "deepseek",
		Model:        "deepseek-v4-pro",
		InputHash:    "input-provider-quota-beyond",
		CreatedAt:    at,
	}
	result, err := s.ReserveAIInvocation(beyond)
	if err != nil || result == nil || !result.Created {
		t.Fatalf("超出旧日限的调用必须放行: result=%+v err=%v", result, err)
	}
}

func TestDialogueTurnProceedsBeyondFormerMonthlyQuota(t *testing.T) {
	s := openTest(t)
	fixture := seedDialogueStoreFixture(t, s, "profile-turn-monthly-quota", "text")
	at := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
	for index := 1; index <= 100; index++ {
		historical := historicalBudgetTurn(index, at)
		if err := s.db.Create(&historical).Error; err != nil {
			t.Fatalf("写入历史 turn[%d]: %v", index, err)
		}
	}

	request := dialogueTurnRequest(fixture, "turn-monthly-quota-beyond", "")
	request.FrozenAt = at.Add(time.Minute)
	result, err := s.FreezeDialogueTurn(request)
	if err != nil || result == nil || !result.Created {
		t.Fatalf("超出旧月限的建轮必须放行: result=%+v err=%v", result, err)
	}
}
