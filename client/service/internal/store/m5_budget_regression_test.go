package store

import (
	"errors"
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

func TestAIInvocationDailyBudgetReplaysExistingBeforeRejectingTwentyFirst(t *testing.T) {
	s := openTest(t)
	_, turn := seedFrozenDialogueTurn(t, s, "profile-provider-daily-budget")
	at := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	request := ReserveAIInvocationRequest{
		InvocationID: "invocation-provider-budget-current",
		TurnID:       turn.TurnID,
		Purpose:      m5ai.PurposeIntent,
		Attempt:      1,
		Provider:     "deepseek",
		Model:        "deepseek-v4-pro",
		InputHash:    "input-provider-budget-current",
		CreatedAt:    at,
	}
	first, err := s.ReserveAIInvocation(request)
	if err != nil || !first.Created {
		t.Fatalf("首条 provider 预留失败: result=%+v err=%v", first, err)
	}

	finishedAt := at.Add(time.Minute)
	zero := 0
	for index := 1; index < int(m5DailyProviderCallLimit); index++ {
		turn := historicalBudgetTurn(index, at.Add(-time.Duration(index)*time.Minute))
		invocation := AIInvocation{
			InvocationID:        fmt.Sprintf("invocation-provider-budget-history-%02d", index),
			TurnID:              turn.TurnID,
			Purpose:             m5ai.PurposeIntent,
			Attempt:             1,
			Provider:            "deepseek",
			Model:               "deepseek-v4-pro",
			ContextRevisionHash: turn.ContextRevisionHash,
			InputHash:           fmt.Sprintf("input-provider-budget-history-%02d", index),
			OutputHash:          fmt.Sprintf("output-provider-budget-history-%02d", index),
			ReasoningTokens:     &zero,
			UsageShape:          AIInvocationUsageComplete,
			Status:              AIInvocationOK,
			CreatedAt:           turn.CreatedAt,
			FinishedAt:          &finishedAt,
		}
		if err := s.db.Create(&turn).Error; err != nil {
			t.Fatalf("写入历史 turn[%d]: %v", index, err)
		}
		if err := s.db.Create(&invocation).Error; err != nil {
			t.Fatalf("写入历史 invocation[%d]: %v", index, err)
		}
	}

	replayed, err := s.ReserveAIInvocation(request)
	if err != nil || replayed.Created || replayed.Invocation.InvocationID != request.InvocationID {
		t.Fatalf("满额后既有 invocation 未优先收编: result=%+v err=%v", replayed, err)
	}
	completion := successfulInvocationCompletion(request.InvocationID, at.Add(2*time.Minute))
	if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: completion,
		Label:      m5ai.IntentNeutral,
		Source:     DialogueIntentLLM,
	}); err != nil {
		t.Fatalf("完成第20条 intent invocation: %v", err)
	}

	twentyFirst := request
	twentyFirst.InvocationID = "invocation-provider-budget-twenty-first"
	twentyFirst.Purpose = m5ai.PurposeReply
	twentyFirst.InputHash = "input-provider-budget-twenty-first"
	if result, err := s.ReserveAIInvocation(twentyFirst); result != nil || !errors.Is(err, ErrAIInvocationBudget) {
		t.Fatalf("第21条新预留未被日预算拒绝: result=%+v err=%v", result, err)
	}
	dayStart, nextDay := localDayBounds(at)
	var count int64
	if err := s.db.Model(&AIInvocation{}).
		Where("created_at >= ? AND created_at < ?", dayStart, nextDay).
		Count(&count).Error; err != nil || count != m5DailyProviderCallLimit {
		t.Fatalf("预算拒绝后 invocation 数量漂移: count=%d err=%v", count, err)
	}
}

func TestDialogueTurnMonthlyBudgetReplaysExistingBeforeRejectingHundredFirst(t *testing.T) {
	s := openTest(t)
	fixture, turn := seedFrozenDialogueTurn(t, s, "profile-turn-monthly-budget-current")
	at := turn.CreatedAt
	for index := 1; index < int(m5MonthlyTurnLimit); index++ {
		historical := historicalBudgetTurn(index, at)
		if err := s.db.Create(&historical).Error; err != nil {
			t.Fatalf("写入历史 turn[%d]: %v", index, err)
		}
	}

	replayRequest := dialogueTurnRequest(fixture, turn.TurnID, "")
	replayed, err := s.FreezeDialogueTurn(replayRequest)
	if err != nil || replayed.Created || replayed.Turn.TurnID != turn.TurnID {
		t.Fatalf("满额后既有 turn 未优先收编: result=%+v err=%v", replayed, err)
	}
	if err := s.MarkDialogueTurnManualRequired(turn.TurnID, "budgetFixtureClosed", at.Add(time.Minute)); err != nil {
		t.Fatalf("释放既有试运行槽: %v", err)
	}

	newFixture := seedDialogueStoreFixture(t, s, "profile-turn-monthly-budget-new", "text")
	newRequest := dialogueTurnRequest(newFixture, "turn-monthly-budget-hundred-first", "")
	newRequest.FrozenAt = at.Add(2 * time.Minute)
	if result, err := s.FreezeDialogueTurn(newRequest); result != nil || !errors.Is(err, ErrDialogueTurnBudget) {
		t.Fatalf("第101个新 turn 未被月预算拒绝: result=%+v err=%v", result, err)
	}
	var count int64
	monthStart, nextMonth := localMonthBounds(at)
	if err := s.db.Model(&DialogueTurn{}).
		Where("created_at >= ? AND created_at < ?", monthStart, nextMonth).
		Count(&count).Error; err != nil || count != m5MonthlyTurnLimit {
		t.Fatalf("预算拒绝后 turn 数量漂移: count=%d err=%v", count, err)
	}
	profile, err := s.CandidateProfileByID(newFixture.ProfileID)
	if err != nil || profile == nil || profile.MainStatus != CandidateProfileGreeted {
		t.Fatalf("预算失败事务不得推进新 profile: profile=%+v err=%v", profile, err)
	}
}
