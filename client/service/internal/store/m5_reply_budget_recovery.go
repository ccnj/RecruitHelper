package store

import (
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/m5ai"

	"gorm.io/gorm"
)

var ErrM5ReplyBudgetRecoveryUnsafe = errors.New("M5 回复预算恢复前置事实不完整")

const (
	m5ReplyBudgetRecoverySelectionReason = "replyBudgetRecoveryAuthorized"
	m5ReplyBudgetRecoveryAuditCategory   = "m5_reply_budget_recovery_authorized"
	m5ReplyBudgetRecoveryAuditDetail     = "legacyPreTransportByteBudgetFalsePositive"
	m5ReplyBudgetRecoveryLegacyError     = "budgetBlocked"
	m5ReplyBudgetRecoveryAttempt         = 2

	m5ReplyTraceRearmTurnID               = "turn-20a94a0610113b8cc7c0e1e0b0972a9e35e8c3c67a16fcdf3351626d0b7da85a"
	m5ReplyTraceRearmAttempt2InvocationID = "invocation-" +
		"1a8eef7e0089c40d1bb1cf6e8f810b081265ca8ca528d2b6803974aa8273725d"
	m5ReplyTraceRearmSelectionReason = "replyTraceRearmAuthorized"
	m5ReplyTraceRearmAuditCategory   = "m5_reply_trace_rearm_authorized"
	m5ReplyTraceRearmAuditDetail     = "legacyInvalidOutputWithoutRawTrace"
)

// M5ReplyInvocationAttempt 只为已获批的一个开发期 turn 返回 attempt=3。
func M5ReplyInvocationAttempt(turnID string) int {
	if strings.TrimSpace(turnID) == m5ReplyTraceRearmTurnID {
		return 3
	}
	return 1
}

func isM5ReplyTraceRearmRequest(req ReserveAIInvocationRequest) bool {
	return req.TurnID == m5ReplyTraceRearmTurnID &&
		req.Purpose == m5ai.PurposeReply &&
		req.Attempt == 3
}

func validateM5ReplyTraceRearmTx(
	tx *gorm.DB,
	turn DialogueTurn,
	req ReserveAIInvocationRequest,
) error {
	if !isM5ReplyTraceRearmRequest(req) ||
		turn.Status != DialogueTurnClassified ||
		turn.IntentLabel != m5ai.IntentInterested ||
		turn.IntentSource != DialogueIntentBusinessEvent ||
		turn.FailureReason != "" {
		return ErrM5ReplyBudgetRecoveryUnsafe
	}
	var selection M5TrialSelection
	if err := tx.First(&selection,
		"profile_id = ? AND status = ? AND active_slot = ? AND reason = ?",
		turn.ProfileID, M5TrialSelectionActive, m5TrialActiveSlot,
		m5ReplyTraceRearmSelectionReason,
	).Error; err != nil {
		return ErrM5ReplyBudgetRecoveryUnsafe
	}
	var attempt2 AIInvocation
	if err := tx.First(&attempt2,
		"invocation_id = ? AND turn_id = ?",
		m5ReplyTraceRearmAttempt2InvocationID, turn.TurnID,
	).Error; err != nil ||
		attempt2.Purpose != m5ai.PurposeReply ||
		attempt2.Attempt != 2 ||
		attempt2.Status != AIInvocationInvalidOutput ||
		attempt2.FinishedAt == nil ||
		attempt2.Provider != req.Provider ||
		attempt2.Model != req.Model ||
		attempt2.InputHash != req.InputHash ||
		attempt2.ContextRevisionHash != turn.ContextRevisionHash {
		return ErrM5ReplyBudgetRecoveryUnsafe
	}
	var audits, actions int64
	if err := tx.Model(&AuditEntry{}).Where(
		"category = ? AND round_id = ? AND detail = ?",
		m5ReplyTraceRearmAuditCategory, turn.TurnID, m5ReplyTraceRearmAuditDetail,
	).Count(&audits).Error; err != nil {
		return err
	}
	if err := tx.Model(&CommunicationAction{}).
		Where("turn_id = ?", turn.TurnID).Count(&actions).Error; err != nil {
		return err
	}
	if audits != 1 || actions != 0 {
		return ErrM5ReplyBudgetRecoveryUnsafe
	}
	return nil
}

type AuthorizeM5ReplyBudgetRecoveryRequest struct {
	FailedSelectionID string
	NewSelectionID    string
	AuthorizedAt      time.Time
}

type AuthorizeM5ReplyBudgetRecoveryResult struct {
	Selection         M5TrialSelection
	Turn              DialogueTurn
	AlreadyAuthorized bool
}

// AuthorizeM5ReplyBudgetRecovery 只纠正已经发生的旧 UTF-8 字节预算误判。
// 原 selection 与 attempt=1 invocation 永久保留；授权事务只恢复 turn 的
// classified 投影、创建一个新的 active selection，并写入不含业务正文的审计。
func (s *Store) AuthorizeM5ReplyBudgetRecovery(
	req AuthorizeM5ReplyBudgetRecoveryRequest,
) (*AuthorizeM5ReplyBudgetRecoveryResult, error) {
	req.FailedSelectionID = strings.TrimSpace(req.FailedSelectionID)
	req.NewSelectionID = strings.TrimSpace(req.NewSelectionID)
	if req.FailedSelectionID == "" || req.NewSelectionID == "" ||
		req.FailedSelectionID == req.NewSelectionID {
		return nil, ErrM5ReplyBudgetRecoveryUnsafe
	}
	if req.AuthorizedAt.IsZero() {
		req.AuthorizedAt = time.Now()
	}

	out := &AuthorizeM5ReplyBudgetRecoveryResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		failed, turn, _, err := loadM5ReplyBudgetRecoveryLegacyTx(tx, req.FailedSelectionID)
		if err != nil {
			return err
		}
		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", failed.ProfileID).Error; err != nil {
			return err
		}
		var account Account
		if err := tx.First(&account,
			"platform = ? AND account_ref = ?", profile.Platform, profile.AccountRef,
		).Error; err != nil {
			return err
		}
		if account.StoppedAt == nil || account.PausedReason != "userStopped" {
			return ErrM5ReplyBudgetRecoveryUnsafe
		}

		var active M5TrialSelection
		activeErr := tx.First(&active,
			"status = ? AND active_slot = ?", M5TrialSelectionActive, m5TrialActiveSlot,
		).Error
		if activeErr == nil {
			if active.ProfileID != failed.ProfileID ||
				active.Reason != m5ReplyBudgetRecoverySelectionReason ||
				turn.Status != DialogueTurnClassified ||
				turn.IntentLabel != m5ai.IntentInterested ||
				turn.IntentSource != DialogueIntentBusinessEvent ||
				turn.FailureReason != "" ||
				requireM5ReplyBudgetRecoveryAuditTx(tx, failed.SelectionID, turn.TurnID) != nil ||
				validateDialogueTurnCurrentTx(tx, turn) != nil {
				return ErrM5ReplyBudgetRecoveryUnsafe
			}
			out.Selection = active
			out.Turn = turn
			out.AlreadyAuthorized = true
			return nil
		}
		if !errors.Is(activeErr, gorm.ErrRecordNotFound) {
			return activeErr
		}
		if turn.Status != DialogueTurnManualRequired ||
			turn.IntentLabel != m5ai.IntentInterested ||
			turn.IntentSource != DialogueIntentBusinessEvent ||
			turn.ClassifiedAt == nil ||
			turn.FailureReason != "replyFailed" {
			return ErrM5ReplyBudgetRecoveryUnsafe
		}
		var invocationCount int64
		if err := tx.Model(&AIInvocation{}).
			Where("turn_id = ?", turn.TurnID).Count(&invocationCount).Error; err != nil {
			return err
		}
		if invocationCount != 1 {
			return ErrM5ReplyBudgetRecoveryUnsafe
		}
		if err := requireNoM5ReplyBudgetRecoveryOutputTx(tx, turn.TurnID); err != nil {
			return err
		}

		slot := m5TrialActiveSlot
		active = M5TrialSelection{
			SelectionID: req.NewSelectionID,
			ProfileID:   failed.ProfileID,
			Status:      M5TrialSelectionActive,
			ActiveSlot:  &slot,
			SelectedBy:  "user",
			Reason:      m5ReplyBudgetRecoverySelectionReason,
			SelectedAt:  req.AuthorizedAt,
		}
		if err := tx.Create(&active).Error; err != nil {
			return err
		}
		updated := tx.Model(&DialogueTurn{}).
			Where("turn_id = ? AND status = ? AND failure_reason = ?",
				turn.TurnID, DialogueTurnManualRequired, "replyFailed").
			Updates(map[string]any{
				"status": DialogueTurnClassified, "failure_reason": "", "updated_at": req.AuthorizedAt,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrM5ReplyBudgetRecoveryUnsafe
		}
		if err := tx.First(&turn, "turn_id = ?", turn.TurnID).Error; err != nil {
			return err
		}
		if err := validateDialogueTurnCurrentTx(tx, turn); err != nil {
			return ErrM5ReplyBudgetRecoveryUnsafe
		}
		if err := tx.Create(&AuditEntry{
			At: req.AuthorizedAt, Category: m5ReplyBudgetRecoveryAuditCategory,
			RefMsgID: failed.SelectionID, RoundID: turn.TurnID,
			Detail: m5ReplyBudgetRecoveryAuditDetail,
		}).Error; err != nil {
			return err
		}
		out.Selection = active
		out.Turn = turn
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type ReserveM5ReplyBudgetRecoveryRequest struct {
	InvocationID string
	TurnID       string
	Provider     string
	Model        string
	InputHash    string
	CreatedAt    time.Time
}

// ReserveAuthorizedM5ReplyBudgetRecovery 是本次事故 attempt=2 的唯一调用授权点。
// Created=false 仍只允许收编已有事实，不授权再次触碰 provider。
func (s *Store) ReserveAuthorizedM5ReplyBudgetRecovery(
	req ReserveM5ReplyBudgetRecoveryRequest,
) (*ReserveAIInvocationResult, error) {
	req.InvocationID = strings.TrimSpace(req.InvocationID)
	req.TurnID = strings.TrimSpace(req.TurnID)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	req.InputHash = strings.TrimSpace(req.InputHash)
	if req.InvocationID == "" || req.TurnID == "" || req.Provider == "" ||
		req.Model == "" || req.InputHash == "" {
		return nil, ErrM5ReplyBudgetRecoveryUnsafe
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}

	out := &ReserveAIInvocationResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var turn DialogueTurn
		if err := tx.First(&turn, "turn_id = ?", req.TurnID).Error; err != nil {
			return err
		}
		if turn.Status != DialogueTurnClassified ||
			turn.IntentLabel != m5ai.IntentInterested ||
			turn.IntentSource != DialogueIntentBusinessEvent ||
			turn.FailureReason != "" {
			return ErrM5ReplyBudgetRecoveryUnsafe
		}
		var active M5TrialSelection
		if err := tx.First(&active,
			"profile_id = ? AND status = ? AND active_slot = ? AND reason = ?",
			turn.ProfileID, M5TrialSelectionActive, m5TrialActiveSlot,
			m5ReplyBudgetRecoverySelectionReason,
		).Error; err != nil {
			return ErrM5ReplyBudgetRecoveryUnsafe
		}
		var audits []AuditEntry
		if err := tx.Where(
			"category = ? AND round_id = ? AND detail = ?",
			m5ReplyBudgetRecoveryAuditCategory, turn.TurnID, m5ReplyBudgetRecoveryAuditDetail,
		).Find(&audits).Error; err != nil {
			return err
		}
		if len(audits) != 1 || strings.TrimSpace(audits[0].RefMsgID) == "" {
			return ErrM5ReplyBudgetRecoveryUnsafe
		}
		_, authorizedTurn, legacy, err := loadM5ReplyBudgetRecoveryLegacyTx(tx, audits[0].RefMsgID)
		if err != nil || authorizedTurn.TurnID != turn.TurnID ||
			legacy.Provider != req.Provider || legacy.Model != req.Model ||
			legacy.InputHash != req.InputHash ||
			legacy.ContextRevisionHash != turn.ContextRevisionHash {
			return ErrM5ReplyBudgetRecoveryUnsafe
		}
		if err := validateDialogueTurnCurrentTx(tx, turn); err != nil {
			return ErrDialogueTurnBinding
		}
		if err := requireNoM5ReplyBudgetRecoveryOutputTx(tx, turn.TurnID); err != nil {
			return err
		}

		wanted := AIInvocation{
			InvocationID: req.InvocationID, TurnID: turn.TurnID,
			Purpose: m5ai.PurposeReply, Attempt: m5ReplyBudgetRecoveryAttempt,
			Provider: req.Provider, Model: req.Model,
			ContextRevisionHash: turn.ContextRevisionHash, InputHash: req.InputHash,
			Status: AIInvocationTransportFailed, CreatedAt: req.CreatedAt,
		}
		var invocations []AIInvocation
		if err := tx.Where("turn_id = ?", turn.TurnID).
			Order("attempt, invocation_id").Find(&invocations).Error; err != nil {
			return err
		}
		switch len(invocations) {
		case 1:
			if invocations[0].InvocationID != legacy.InvocationID {
				return ErrM5ReplyBudgetRecoveryUnsafe
			}
		case 2:
			existing := invocations[1]
			if invocations[0].InvocationID != legacy.InvocationID ||
				!sameInvocationReservation(existing, wanted) {
				return ErrM5ReplyBudgetRecoveryUnsafe
			}
			out.Invocation = existing
			return nil
		default:
			return ErrM5ReplyBudgetRecoveryUnsafe
		}
		dayStart, nextDay := localDayBounds(req.CreatedAt)
		var dailyCalls int64
		if err := tx.Model(&AIInvocation{}).
			Where("created_at >= ? AND created_at < ?", dayStart, nextDay).
			Count(&dailyCalls).Error; err != nil {
			return err
		}
		if dailyCalls >= m5DailyProviderCallLimit {
			return ErrAIInvocationBudget
		}
		if err := tx.Create(&wanted).Error; err != nil {
			return err
		}
		out.Invocation = wanted
		out.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func loadM5ReplyBudgetRecoveryLegacyTx(
	tx *gorm.DB,
	failedSelectionID string,
) (M5TrialSelection, DialogueTurn, AIInvocation, error) {
	var failed M5TrialSelection
	if err := tx.First(&failed, "selection_id = ?", failedSelectionID).Error; err != nil {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, ErrM5ReplyBudgetRecoveryUnsafe
	}
	if failed.Status != M5TrialSelectionManualRequired ||
		failed.ActiveSlot != nil || failed.Reason != "replyFailed" ||
		failed.SelectedBy != "user" || failed.EndedAt == nil {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, ErrM5ReplyBudgetRecoveryUnsafe
	}
	var turns []DialogueTurn
	if err := tx.Where("profile_id = ?", failed.ProfileID).
		Order("created_at, turn_id").Find(&turns).Error; err != nil {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, err
	}
	if len(turns) != 1 {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, ErrM5ReplyBudgetRecoveryUnsafe
	}
	turn := turns[0]
	var invocations []AIInvocation
	if err := tx.Where("turn_id = ?", turn.TurnID).
		Order("attempt, invocation_id").Find(&invocations).Error; err != nil {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, err
	}
	if len(invocations) == 0 ||
		!isLegacyM5ReplyBudgetFalsePositive(invocations[0], turn) {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, ErrM5ReplyBudgetRecoveryUnsafe
	}
	return failed, turn, invocations[0], nil
}

func isLegacyM5ReplyBudgetFalsePositive(invocation AIInvocation, turn DialogueTurn) bool {
	return invocation.TurnID == turn.TurnID &&
		invocation.Purpose == m5ai.PurposeReply &&
		invocation.Attempt == 1 &&
		invocation.Provider != "" && invocation.Model != "" &&
		invocation.ContextRevisionHash == turn.ContextRevisionHash &&
		invocation.InputHash != "" &&
		invocation.Status == AIInvocationBudgetBlocked &&
		invocation.ErrorClass == m5ReplyBudgetRecoveryLegacyError &&
		invocation.FinishedAt != nil &&
		invocation.OutputHash == "" &&
		invocation.InputTokens == 0 &&
		invocation.CachedInputTokens == 0 &&
		invocation.OutputTokens == 0 &&
		invocation.ReasoningTokens == nil &&
		invocation.UsageShape == "" &&
		invocation.LatencyMs == 0 &&
		invocation.EstimatedCostMicros == 0
}

func requireNoM5ReplyBudgetRecoveryOutputTx(tx *gorm.DB, turnID string) error {
	var actions int64
	if err := tx.Model(&CommunicationAction{}).
		Where("turn_id = ?", turnID).Count(&actions).Error; err != nil {
		return err
	}
	if actions != 0 {
		return ErrM5ReplyBudgetRecoveryUnsafe
	}
	return nil
}

func requireM5ReplyBudgetRecoveryAuditTx(
	tx *gorm.DB,
	failedSelectionID string,
	turnID string,
) error {
	var count int64
	if err := tx.Model(&AuditEntry{}).Where(
		"category = ? AND ref_msg_id = ? AND round_id = ? AND detail = ?",
		m5ReplyBudgetRecoveryAuditCategory, failedSelectionID, turnID,
		m5ReplyBudgetRecoveryAuditDetail,
	).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrM5ReplyBudgetRecoveryUnsafe
	}
	return nil
}
