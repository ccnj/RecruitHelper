package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"

	"gorm.io/gorm"
)

type m5ReplyBudgetRecoveryFixture struct {
	store      *Store
	dialogue   dialogueStoreFixture
	turn       DialogueTurn
	failed     M5TrialSelection
	invocation AIInvocation
}

func seedM5ReplyBudgetRecoveryFixture(t *testing.T, suffix string) m5ReplyBudgetRecoveryFixture {
	t.Helper()
	s := openTest(t)
	dialogue := seedDialogueStoreFixture(t, s, "profile-reply-budget-"+suffix, "card")
	dialogue.FirstMessage.CardType = "resumeAttachment"
	dialogue.FirstMessage.CardState = "unknown"
	if err := s.db.Model(&Message{}).Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
		dialogue.Platform, dialogue.AccountRef, dialogue.ConversationRef, dialogue.FirstMessage.Seq,
	).Updates(map[string]any{
		"card_type": "resumeAttachment", "card_state": "unknown",
	}).Error; err != nil {
		t.Fatal(err)
	}
	frozen, err := s.FreezeDialogueTurn(
		dialogueTurnRequest(dialogue, "turn-reply-budget-"+suffix, ""),
	)
	if err != nil || !frozen.Created {
		t.Fatalf("冻结恢复测试 turn: result=%+v err=%v", frozen, err)
	}
	classifiedAt := time.Now().UTC().Truncate(time.Millisecond)
	classified, err := s.ApplyResumeBusinessClassification(frozen.Turn.TurnID, classifiedAt)
	if err != nil || classified.Status != DialogueTurnClassified ||
		classified.IntentLabel != m5ai.IntentInterested ||
		classified.IntentSource != DialogueIntentBusinessEvent {
		t.Fatalf("建立强意向分类: turn=%+v err=%v", classified, err)
	}
	invocationID := "invocation-reply-budget-" + suffix
	reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: invocationID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-reply-budget-" + suffix, CreatedAt: classifiedAt.Add(time.Second),
	})
	if err != nil || !reserved.Created {
		t.Fatalf("建立旧误判预留: result=%+v err=%v", reserved, err)
	}
	if _, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: AIInvocationCompletion{
			InvocationID: invocationID, Status: AIInvocationBudgetBlocked,
			ErrorClass: "budgetBlocked", FinishedAt: classifiedAt.Add(2 * time.Second),
		},
		ManualReason: "replyFailed", PlannedAt: classifiedAt.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	stoppedAt := classifiedAt.Add(3 * time.Second)
	if err := s.db.Model(&Account{}).Where(
		"platform = ? AND account_ref = ?", dialogue.Platform, dialogue.AccountRef,
	).Updates(map[string]any{
		"stopped_at": stoppedAt, "paused_reason": "userStopped",
	}).Error; err != nil {
		t.Fatal(err)
	}
	var failed M5TrialSelection
	if err := s.db.First(&failed,
		"profile_id = ? AND status = ?", dialogue.ProfileID, M5TrialSelectionManualRequired,
	).Error; err != nil {
		t.Fatal(err)
	}
	var storedTurn DialogueTurn
	if err := s.db.First(&storedTurn, "turn_id = ?", frozen.Turn.TurnID).Error; err != nil {
		t.Fatal(err)
	}
	var invocation AIInvocation
	if err := s.db.First(&invocation, "invocation_id = ?", invocationID).Error; err != nil {
		t.Fatal(err)
	}
	return m5ReplyBudgetRecoveryFixture{
		store: s, dialogue: dialogue, turn: storedTurn, failed: failed, invocation: invocation,
	}
}

func TestAuthorizeM5ReplyBudgetRecoveryPreservesLegacyFactsAndIsIdempotent(t *testing.T) {
	fixture := seedM5ReplyBudgetRecoveryFixture(t, "authorize")
	authorizedAt := time.Now().UTC().Truncate(time.Millisecond)
	result, err := fixture.store.AuthorizeM5ReplyBudgetRecovery(
		AuthorizeM5ReplyBudgetRecoveryRequest{
			FailedSelectionID: fixture.failed.SelectionID,
			NewSelectionID:    "selection-reply-budget-recovery",
			AuthorizedAt:      authorizedAt,
		},
	)
	if err != nil || result.AlreadyAuthorized ||
		result.Selection.Status != M5TrialSelectionActive ||
		result.Selection.Reason != m5ReplyBudgetRecoverySelectionReason ||
		result.Turn.Status != DialogueTurnClassified ||
		result.Turn.IntentLabel != m5ai.IntentInterested ||
		result.Turn.IntentSource != DialogueIntentBusinessEvent ||
		result.Turn.FailureReason != "" ||
		result.Turn.ClassifiedAt == nil || fixture.turn.ClassifiedAt == nil ||
		!result.Turn.ClassifiedAt.Equal(*fixture.turn.ClassifiedAt) ||
		result.Turn.InputDigest != fixture.turn.InputDigest ||
		result.Turn.ContextRevisionHash != fixture.turn.ContextRevisionHash ||
		result.Turn.ResumeSnapshotID != fixture.turn.ResumeSnapshotID ||
		result.Turn.HistoryThroughSeq != fixture.turn.HistoryThroughSeq ||
		result.Turn.InboundFromSeq != fixture.turn.InboundFromSeq ||
		result.Turn.InboundThroughSeq != fixture.turn.InboundThroughSeq {
		t.Fatalf("恢复授权投影错误: result=%+v err=%v", result, err)
	}
	var failed M5TrialSelection
	if err := fixture.store.db.First(&failed,
		"selection_id = ?", fixture.failed.SelectionID).Error; err != nil {
		t.Fatal(err)
	}
	if failed.Status != fixture.failed.Status || failed.ActiveSlot != nil ||
		failed.Reason != fixture.failed.Reason || failed.EndedAt == nil ||
		!failed.EndedAt.Equal(*fixture.failed.EndedAt) {
		t.Fatalf("旧 selection 被改写: before=%+v after=%+v", fixture.failed, failed)
	}
	var invocation AIInvocation
	if err := fixture.store.db.First(&invocation,
		"invocation_id = ?", fixture.invocation.InvocationID).Error; err != nil {
		t.Fatal(err)
	}
	if !sameInvocationReservation(invocation, fixture.invocation) ||
		!sameInvocationCompletion(invocation, AIInvocationCompletion{
			InvocationID: fixture.invocation.InvocationID,
			Status:       AIInvocationBudgetBlocked, ErrorClass: "budgetBlocked",
			FinishedAt: *fixture.invocation.FinishedAt,
		}) {
		t.Fatalf("旧 invocation 被改写: before=%+v after=%+v", fixture.invocation, invocation)
	}
	account, err := fixture.store.AccountByKey(AccountKey{
		Platform: fixture.dialogue.Platform, AccountRef: fixture.dialogue.AccountRef,
	})
	if err != nil || account.StoppedAt == nil || account.PausedReason != "userStopped" {
		t.Fatalf("恢复授权不得擅自启用账号: account=%+v err=%v", account, err)
	}
	var audits []AuditEntry
	if err := fixture.store.db.Where(
		"category = ?", m5ReplyBudgetRecoveryAuditCategory,
	).Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].RefMsgID != fixture.failed.SelectionID ||
		audits[0].RoundID != fixture.turn.TurnID ||
		audits[0].Detail != m5ReplyBudgetRecoveryAuditDetail ||
		audits[0].Platform != "" || audits[0].AccountRef != "" ||
		audits[0].ConversationRef != "" {
		t.Fatalf("恢复审计不满足脱敏边界: %+v", audits)
	}

	replayed, err := fixture.store.AuthorizeM5ReplyBudgetRecovery(
		AuthorizeM5ReplyBudgetRecoveryRequest{
			FailedSelectionID: fixture.failed.SelectionID,
			NewSelectionID:    "selection-reply-budget-recovery-other",
			AuthorizedAt:      authorizedAt.Add(time.Minute),
		},
	)
	if err != nil || !replayed.AlreadyAuthorized ||
		replayed.Selection.SelectionID != result.Selection.SelectionID {
		t.Fatalf("授权重放必须幂等: result=%+v err=%v", replayed, err)
	}
}

func TestReserveAuthorizedM5ReplyBudgetRecoveryAllowsExactlyAttemptTwo(t *testing.T) {
	fixture := seedM5ReplyBudgetRecoveryFixture(t, "reserve")
	if _, err := fixture.store.AuthorizeM5ReplyBudgetRecovery(
		AuthorizeM5ReplyBudgetRecoveryRequest{
			FailedSelectionID: fixture.failed.SelectionID,
			NewSelectionID:    "selection-reply-budget-reserve",
		},
	); err != nil {
		t.Fatal(err)
	}
	request := ReserveM5ReplyBudgetRecoveryRequest{
		InvocationID: "invocation-reply-budget-attempt-2",
		TurnID:       fixture.turn.TurnID,
		Provider:     fixture.invocation.Provider,
		Model:        fixture.invocation.Model,
		InputHash:    fixture.invocation.InputHash,
	}
	first, err := fixture.store.ReserveAuthorizedM5ReplyBudgetRecovery(request)
	if err != nil || !first.Created || first.Invocation.Attempt != 2 ||
		first.Invocation.Purpose != m5ai.PurposeReply {
		t.Fatalf("attempt=2 预留失败: result=%+v err=%v", first, err)
	}
	replayedRequest := request
	replayedRequest.InvocationID = "invocation-reply-budget-attempt-2-other"
	replayed, err := fixture.store.ReserveAuthorizedM5ReplyBudgetRecovery(replayedRequest)
	if err != nil || replayed.Created ||
		replayed.Invocation.InvocationID != first.Invocation.InvocationID {
		t.Fatalf("attempt=2 重放不得再次授权 provider: result=%+v err=%v", replayed, err)
	}
	var count int64
	if err := fixture.store.db.Model(&AIInvocation{}).
		Where("turn_id = ?", fixture.turn.TurnID).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("恢复只能保留 attempt1+attempt2: count=%d err=%v", count, err)
	}
}

func TestM5ReplyBudgetRecoveryRejectsDriftWithoutWrites(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(m5ReplyBudgetRecoveryFixture)
	}{
		{
			name: "account enabled",
			mutate: func(fixture m5ReplyBudgetRecoveryFixture) {
				if err := fixture.store.db.Model(&Account{}).Where(
					"platform = ? AND account_ref = ?",
					fixture.dialogue.Platform, fixture.dialogue.AccountRef,
				).Updates(map[string]any{"stopped_at": nil, "paused_reason": ""}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "message boundary changed",
			mutate: func(fixture m5ReplyBudgetRecoveryFixture) {
				text := "晚到消息"
				if err := fixture.store.db.Create(&Message{
					Platform: fixture.dialogue.Platform, AccountRef: fixture.dialogue.AccountRef,
					ConversationRef: fixture.dialogue.ConversationRef, Seq: 3,
					Direction: "in", Kind: "text", ContentHash: "late-message-hash",
					Text: &text, Origin: "external",
				}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "legacy usage is nonzero",
			mutate: func(fixture m5ReplyBudgetRecoveryFixture) {
				if err := fixture.store.db.Model(&AIInvocation{}).Where(
					"invocation_id = ?", fixture.invocation.InvocationID,
				).Update("input_tokens", 1).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := seedM5ReplyBudgetRecoveryFixture(t, stringsForTestName(test.name))
			test.mutate(fixture)
			_, err := fixture.store.AuthorizeM5ReplyBudgetRecovery(
				AuthorizeM5ReplyBudgetRecoveryRequest{
					FailedSelectionID: fixture.failed.SelectionID,
					NewSelectionID:    "selection-rejected-" + stringsForTestName(test.name),
				},
			)
			if !errors.Is(err, ErrM5ReplyBudgetRecoveryUnsafe) {
				t.Fatalf("漂移必须阻断恢复: %v", err)
			}
			var selections, audits int64
			_ = fixture.store.db.Model(&M5TrialSelection{}).Count(&selections).Error
			_ = fixture.store.db.Model(&AuditEntry{}).
				Where("category = ?", m5ReplyBudgetRecoveryAuditCategory).Count(&audits).Error
			turn, _ := fixture.store.DialogueTurnByID(fixture.turn.TurnID)
			if selections != 1 || audits != 0 || turn == nil ||
				turn.Status != DialogueTurnManualRequired ||
				turn.FailureReason != "replyFailed" {
				t.Fatalf("失败恢复必须零写入: selections=%d audits=%d turn=%+v",
					selections, audits, turn)
			}
		})
	}
}

func TestReserveM5ReplyBudgetRecoveryRejectsInputDriftBeforeAttemptTwo(t *testing.T) {
	fixture := seedM5ReplyBudgetRecoveryFixture(t, "input-drift")
	if _, err := fixture.store.AuthorizeM5ReplyBudgetRecovery(
		AuthorizeM5ReplyBudgetRecoveryRequest{
			FailedSelectionID: fixture.failed.SelectionID,
			NewSelectionID:    "selection-input-drift",
		},
	); err != nil {
		t.Fatal(err)
	}
	request := ReserveM5ReplyBudgetRecoveryRequest{
		InvocationID: "invocation-input-drift-attempt-2",
		TurnID:       fixture.turn.TurnID,
		Provider:     fixture.invocation.Provider,
		Model:        fixture.invocation.Model,
		InputHash:    "different-input-hash",
	}
	if _, err := fixture.store.ReserveAuthorizedM5ReplyBudgetRecovery(request); !errors.Is(err, ErrM5ReplyBudgetRecoveryUnsafe) {
		t.Fatalf("输入漂移必须阻断 attempt=2: %v", err)
	}
	var count int64
	if err := fixture.store.db.Model(&AIInvocation{}).
		Where("turn_id = ?", fixture.turn.TurnID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("漂移后不得新增 invocation: count=%d err=%v", count, err)
	}
}

func TestApprovedM5ReplyTraceRearmReservesAttemptThreeOnly(t *testing.T) {
	fixture := seedM5ReplyBudgetRecoveryFixture(t, "trace-rearm-once")
	oldTurnID := fixture.turn.TurnID
	if err := fixture.store.db.Model(&DialogueTurn{}).
		Where("turn_id = ?", oldTurnID).
		Update("turn_id", m5ReplyTraceRearmTurnID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.db.Model(&AIInvocation{}).
		Where("turn_id = ?", oldTurnID).
		Update("turn_id", m5ReplyTraceRearmTurnID).Error; err != nil {
		t.Fatal(err)
	}
	fixture.turn.TurnID = m5ReplyTraceRearmTurnID
	fixture.invocation.TurnID = m5ReplyTraceRearmTurnID

	authorized, err := fixture.store.AuthorizeM5ReplyBudgetRecovery(
		AuthorizeM5ReplyBudgetRecoveryRequest{
			FailedSelectionID: fixture.failed.SelectionID,
			NewSelectionID:    "selection-trace-rearm-attempt-2",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt2, err := fixture.store.ReserveAuthorizedM5ReplyBudgetRecovery(
		ReserveM5ReplyBudgetRecoveryRequest{
			InvocationID: m5ReplyTraceRearmAttempt2InvocationID,
			TurnID:       fixture.turn.TurnID,
			Provider:     fixture.invocation.Provider,
			Model:        fixture.invocation.Model,
			InputHash:    fixture.invocation.InputHash,
		},
	)
	if err != nil || !attempt2.Created {
		t.Fatalf("建立 attempt=2 失败事实: result=%+v err=%v", attempt2, err)
	}
	finishedAt := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := fixture.store.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: AIInvocationCompletion{
			InvocationID: attempt2.Invocation.InvocationID,
			Status:       AIInvocationInvalidOutput,
			OutputHash:   "trace-rearm-invalid-output",
			ErrorClass:   "invalidOutput",
			FinishedAt:   finishedAt,
		},
		ManualReason: "replyFailed",
		PlannedAt:    finishedAt,
	}); err != nil {
		t.Fatal(err)
	}

	slot := m5TrialActiveSlot
	if err := fixture.store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&M5TrialSelection{
			SelectionID: "selection-trace-rearm-attempt-3",
			ProfileID:   authorized.Selection.ProfileID,
			Status:      M5TrialSelectionActive,
			ActiveSlot:  &slot,
			SelectedBy:  "user",
			Reason:      m5ReplyTraceRearmSelectionReason,
			SelectedAt:  finishedAt.Add(time.Second),
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&DialogueTurn{}).
			Where("turn_id = ? AND status = ? AND failure_reason = ?",
				fixture.turn.TurnID, DialogueTurnManualRequired, "replyFailed").
			Updates(map[string]any{
				"status": DialogueTurnClassified, "failure_reason": "",
			}).Error; err != nil {
			return err
		}
		return tx.Create(&AuditEntry{
			At: finishedAt.Add(time.Second), Category: m5ReplyTraceRearmAuditCategory,
			RoundID: fixture.turn.TurnID, Detail: m5ReplyTraceRearmAuditDetail,
		}).Error
	}); err != nil {
		t.Fatal(err)
	}

	request := ReserveAIInvocationRequest{
		InvocationID: "invocation-trace-rearm-attempt-3",
		TurnID:       fixture.turn.TurnID,
		Purpose:      m5ai.PurposeReply,
		Attempt:      M5ReplyInvocationAttempt(fixture.turn.TurnID),
		Provider:     fixture.invocation.Provider,
		Model:        fixture.invocation.Model,
		InputHash:    fixture.invocation.InputHash,
	}
	reserved, err := fixture.store.ReserveAIInvocation(request)
	if err != nil || !reserved.Created || reserved.Invocation.Attempt != 3 {
		t.Fatalf("获批 attempt=3 未被唯一预留: result=%+v err=%v", reserved, err)
	}
	request.Attempt = 4
	request.InvocationID = "invocation-forbidden-attempt-4"
	if _, err := fixture.store.ReserveAIInvocation(request); !errors.Is(err, ErrAIInvocationInvalid) {
		t.Fatalf("attempt=4 必须被通用预留入口拒绝: %v", err)
	}
}

func stringsForTestName(value string) string {
	sum := 0
	for _, r := range value {
		sum = (sum*33 + int(r)) & 0xffff
	}
	return time.Unix(int64(sum), 0).UTC().Format("150405")
}
