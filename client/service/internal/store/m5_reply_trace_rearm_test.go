package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

type m5ReplyTraceRearmFixture struct {
	store        *Store
	turn         DialogueTurn
	failed       M5TrialSelection
	attempt1     AIInvocation
	attempt2     AIInvocation
	originalFail M5TrialSelection
}

func seedM5ReplyTraceRearmFixture(t *testing.T) m5ReplyTraceRearmFixture {
	t.Helper()
	legacy := seedM5ReplyBudgetRecoveryFixture(t, "trace-rearm")
	oldTurnID := legacy.turn.TurnID
	if err := legacy.store.db.Model(&DialogueTurn{}).
		Where("turn_id = ?", oldTurnID).
		Update("turn_id", m5ReplyTraceRearmTurnID).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.store.db.Model(&AIInvocation{}).
		Where("turn_id = ?", oldTurnID).
		Update("turn_id", m5ReplyTraceRearmTurnID).Error; err != nil {
		t.Fatal(err)
	}
	legacy.turn.TurnID = m5ReplyTraceRearmTurnID
	legacy.invocation.TurnID = m5ReplyTraceRearmTurnID

	authorized, err := legacy.store.AuthorizeM5ReplyBudgetRecovery(
		AuthorizeM5ReplyBudgetRecoveryRequest{
			FailedSelectionID: legacy.failed.SelectionID,
			NewSelectionID:    m5ReplyTraceRearmFailedSelectionID,
			AuthorizedAt:      time.Now().UTC().Truncate(time.Millisecond),
		},
	)
	if err != nil || authorized.Turn.Status != DialogueTurnClassified {
		t.Fatalf("建立 attempt=2 授权失败: result=%+v err=%v", authorized, err)
	}
	reserved, err := legacy.store.ReserveAuthorizedM5ReplyBudgetRecovery(
		ReserveM5ReplyBudgetRecoveryRequest{
			InvocationID: m5ReplyTraceRearmAttempt2InvocationID,
			TurnID:       m5ReplyTraceRearmTurnID,
			Provider:     legacy.invocation.Provider,
			Model:        legacy.invocation.Model,
			InputHash:    legacy.invocation.InputHash,
			CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
		},
	)
	if err != nil || !reserved.Created || reserved.Invocation.Attempt != 2 {
		t.Fatalf("建立 attempt=2 预留失败: result=%+v err=%v", reserved, err)
	}
	finishedAt := time.Now().UTC().Add(time.Second).Truncate(time.Millisecond)
	if _, err := legacy.store.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: AIInvocationCompletion{
			InvocationID: reserved.Invocation.InvocationID,
			Status:       AIInvocationInvalidOutput,
			OutputHash:   m5ReplyTraceRearmAttempt2OutputHash,
			InputTokens:  7315, CachedInputTokens: 384, OutputTokens: 199,
			UsageShape: AIInvocationReasoningFieldAbsent,
			LatencyMs:  5014, ErrorClass: "invalidOutput",
			EstimatedCostMicros: 3190, FinishedAt: finishedAt,
		},
		ManualReason: "replyFailed",
		PlannedAt:    finishedAt,
	}); err != nil {
		t.Fatal(err)
	}
	var turn DialogueTurn
	if err := legacy.store.db.First(&turn, "turn_id = ?", m5ReplyTraceRearmTurnID).Error; err != nil {
		t.Fatal(err)
	}
	var failed M5TrialSelection
	if err := legacy.store.db.First(&failed,
		"selection_id = ?", m5ReplyTraceRearmFailedSelectionID,
	).Error; err != nil {
		t.Fatal(err)
	}
	var attempt2 AIInvocation
	if err := legacy.store.db.First(&attempt2,
		"invocation_id = ?", m5ReplyTraceRearmAttempt2InvocationID,
	).Error; err != nil {
		t.Fatal(err)
	}
	return m5ReplyTraceRearmFixture{
		store: legacy.store, turn: turn, failed: failed,
		attempt1: legacy.invocation, attempt2: attempt2, originalFail: legacy.failed,
	}
}

func TestAuthorizeM5ReplyTraceRearmPreservesOldFactsAndReservesOnlyAttemptThree(t *testing.T) {
	fixture := seedM5ReplyTraceRearmFixture(t)
	authorizedAt := time.Now().UTC().Add(2 * time.Second).Truncate(time.Millisecond)
	result, err := fixture.store.AuthorizeM5ReplyTraceRearm(
		AuthorizeM5ReplyTraceRearmRequest{
			FailedSelectionID: fixture.failed.SelectionID,
			TurnID:            fixture.turn.TurnID,
			NewSelectionID:    "selection-reply-trace-rearm",
			AuthorizedAt:      authorizedAt,
		},
	)
	if err != nil || result.AlreadyAuthorized ||
		result.Selection.Status != M5TrialSelectionActive ||
		result.Selection.Reason != m5ReplyTraceRearmSelectionReason ||
		result.Turn.Status != DialogueTurnClassified ||
		result.Turn.FailureReason != "" ||
		result.Turn.IntentLabel != m5ai.IntentInterested ||
		result.Turn.IntentSource != DialogueIntentBusinessEvent {
		t.Fatalf("补验授权结果错误: result=%+v err=%v", result, err)
	}
	var attempt1, attempt2 AIInvocation
	if err := fixture.store.db.First(&attempt1,
		"invocation_id = ?", fixture.attempt1.InvocationID,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.db.First(&attempt2,
		"invocation_id = ?", fixture.attempt2.InvocationID,
	).Error; err != nil {
		t.Fatal(err)
	}
	if !sameInvocationReservation(attempt1, fixture.attempt1) ||
		!sameInvocationReservation(attempt2, fixture.attempt2) ||
		attempt2.Status != fixture.attempt2.Status ||
		attempt2.OutputHash != fixture.attempt2.OutputHash ||
		attempt2.InputTokens != fixture.attempt2.InputTokens ||
		attempt2.OutputTokens != fixture.attempt2.OutputTokens ||
		attempt2.FinishedAt == nil ||
		fixture.attempt2.FinishedAt == nil ||
		!attempt2.FinishedAt.Equal(*fixture.attempt2.FinishedAt) {
		t.Fatalf("补验授权改写了旧 invocation: before=%+v after=%+v", fixture.attempt2, attempt2)
	}
	var failed M5TrialSelection
	if err := fixture.store.db.First(&failed,
		"selection_id = ?", fixture.failed.SelectionID,
	).Error; err != nil {
		t.Fatal(err)
	}
	if failed.Status != fixture.failed.Status ||
		failed.Reason != fixture.failed.Reason ||
		failed.EndedAt == nil ||
		fixture.failed.EndedAt == nil ||
		!failed.EndedAt.Equal(*fixture.failed.EndedAt) {
		t.Fatalf("补验授权改写了旧 selection: before=%+v after=%+v", fixture.failed, failed)
	}

	replayed, err := fixture.store.AuthorizeM5ReplyTraceRearm(
		AuthorizeM5ReplyTraceRearmRequest{
			FailedSelectionID: fixture.failed.SelectionID,
			TurnID:            fixture.turn.TurnID,
			NewSelectionID:    "selection-reply-trace-rearm-other",
			AuthorizedAt:      authorizedAt.Add(time.Minute),
		},
	)
	if err != nil || !replayed.AlreadyAuthorized ||
		replayed.Selection.SelectionID != result.Selection.SelectionID {
		t.Fatalf("补验授权重放不幂等: result=%+v err=%v", replayed, err)
	}

	reserved, err := fixture.store.ReserveAuthorizedM5ReplyTraceRearm(
		ReserveM5ReplyTraceRearmRequest{
			InvocationID: "invocation-reply-trace-attempt-3",
			TurnID:       fixture.turn.TurnID,
			Provider:     fixture.attempt2.Provider,
			Model:        fixture.attempt2.Model,
			InputHash:    fixture.attempt2.InputHash,
			CreatedAt:    authorizedAt.Add(time.Second),
		},
	)
	if err != nil || !reserved.Created ||
		reserved.Invocation.Attempt != 3 ||
		reserved.Invocation.Purpose != m5ai.PurposeReply {
		t.Fatalf("attempt=3 预留失败: result=%+v err=%v", reserved, err)
	}
	replayedReserve, err := fixture.store.ReserveAuthorizedM5ReplyTraceRearm(
		ReserveM5ReplyTraceRearmRequest{
			InvocationID: "invocation-reply-trace-attempt-3-other",
			TurnID:       fixture.turn.TurnID,
			Provider:     fixture.attempt2.Provider,
			Model:        fixture.attempt2.Model,
			InputHash:    fixture.attempt2.InputHash,
			CreatedAt:    authorizedAt.Add(2 * time.Second),
		},
	)
	if err != nil || replayedReserve.Created ||
		replayedReserve.Invocation.InvocationID != reserved.Invocation.InvocationID {
		t.Fatalf("attempt=3 重放不得再次授权 provider: result=%+v err=%v",
			replayedReserve, err)
	}
}

func TestM5ReplyTraceRearmFailureCannotAuthorizeAttemptFour(t *testing.T) {
	fixture := seedM5ReplyTraceRearmFixture(t)
	if _, err := fixture.store.AuthorizeM5ReplyTraceRearm(
		AuthorizeM5ReplyTraceRearmRequest{
			FailedSelectionID: fixture.failed.SelectionID,
			TurnID:            fixture.turn.TurnID,
			NewSelectionID:    "selection-reply-trace-failing",
		},
	); err != nil {
		t.Fatal(err)
	}
	reserved, err := fixture.store.ReserveAuthorizedM5ReplyTraceRearm(
		ReserveM5ReplyTraceRearmRequest{
			InvocationID: "invocation-reply-trace-failing-attempt-3",
			TurnID:       fixture.turn.TurnID,
			Provider:     fixture.attempt2.Provider,
			Model:        fixture.attempt2.Model,
			InputHash:    fixture.attempt2.InputHash,
		},
	)
	if err != nil || !reserved.Created {
		t.Fatalf("attempt=3 预留失败: result=%+v err=%v", reserved, err)
	}
	finishedAt := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	if _, err := fixture.store.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: AIInvocationCompletion{
			InvocationID: reserved.Invocation.InvocationID,
			Status:       AIInvocationProviderRejected,
			ErrorClass:   "providerUnavailable",
			FinishedAt:   finishedAt,
		},
		ManualReason: "replyFailed",
		PlannedAt:    finishedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.AuthorizeM5ReplyTraceRearm(
		AuthorizeM5ReplyTraceRearmRequest{
			FailedSelectionID: fixture.failed.SelectionID,
			TurnID:            fixture.turn.TurnID,
			NewSelectionID:    "selection-forbidden-attempt-4",
		},
	); !errors.Is(err, ErrM5ReplyTraceRearmUnsafe) {
		t.Fatalf("attempt=3 失败后必须拒绝 attempt=4: %v", err)
	}
	var count int64
	if err := fixture.store.db.Model(&AIInvocation{}).
		Where("turn_id = ?", fixture.turn.TurnID).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("补验最多三条 invocation: count=%d err=%v", count, err)
	}
}

func TestM5ReplyTraceRearmRejectsAccountDriftWithoutWrites(t *testing.T) {
	fixture := seedM5ReplyTraceRearmFixture(t)
	if err := fixture.store.db.Model(&Account{}).
		Where("platform = ? AND account_ref = ?",
			fixture.storeKeyPlatform(t, fixture.turn.ProfileID),
			fixture.storeKeyAccount(t, fixture.turn.ProfileID)).
		Updates(map[string]any{"stopped_at": nil, "paused_reason": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.AuthorizeM5ReplyTraceRearm(
		AuthorizeM5ReplyTraceRearmRequest{
			FailedSelectionID: fixture.failed.SelectionID,
			TurnID:            fixture.turn.TurnID,
			NewSelectionID:    "selection-rejected-account-drift",
		},
	); !errors.Is(err, ErrM5ReplyTraceRearmUnsafe) {
		t.Fatalf("账号漂移必须阻断补验: %v", err)
	}
	var audits int64
	if err := fixture.store.db.Model(&AuditEntry{}).
		Where("category = ?", m5ReplyTraceRearmAuditCategory).
		Count(&audits).Error; err != nil || audits != 0 {
		t.Fatalf("失败授权不得留下审计: count=%d err=%v", audits, err)
	}
}

func (fixture m5ReplyTraceRearmFixture) storeKeyPlatform(t *testing.T, profileID string) string {
	t.Helper()
	var profile CandidateProfile
	if err := fixture.store.db.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		t.Fatal(err)
	}
	return profile.Platform
}

func (fixture m5ReplyTraceRearmFixture) storeKeyAccount(t *testing.T, profileID string) string {
	t.Helper()
	var profile CandidateProfile
	if err := fixture.store.db.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		t.Fatal(err)
	}
	return profile.AccountRef
}
