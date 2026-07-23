package store

import (
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/m5ai"

	"gorm.io/gorm"
)

var ErrM5ReplyTraceRearmUnsafe = errors.New("M5 回复原始轨迹补验前置事实不完整")

const (
	m5ReplyTraceRearmTurnID               = "turn-20a94a0610113b8cc7c0e1e0b0972a9e35e8c3c67a16fcdf3351626d0b7da85a"
	m5ReplyTraceRearmAttempt2InvocationID = "invocation-" +
		"1a8eef7e0089c40d1bb1cf6e8f810b081265ca8ca528d2b6803974aa8273725d"
	m5ReplyTraceRearmFailedSelectionID  = "ts-631e39791d2a8750965bd742"
	m5ReplyTraceRearmAttempt2OutputHash = "091e592f1b204e0147686b88d33fa2eee0ed45e72eee9a5298af599917633350"

	m5ReplyTraceRearmSelectionReason = "replyTraceRearmAuthorized"
	m5ReplyTraceRearmAuditCategory   = "m5_reply_trace_rearm_authorized"
	m5ReplyTraceRearmAuditDetail     = "legacyInvalidOutputWithoutRawTrace"
	m5ReplyTraceRearmAttempt         = 3
)

// IsM5ReplyTraceRearmTarget 将运行时代码的例外限制为已经获批的唯一 turn。
func IsM5ReplyTraceRearmTarget(turnID string) bool {
	return strings.TrimSpace(turnID) == m5ReplyTraceRearmTurnID
}

type AuthorizeM5ReplyTraceRearmRequest struct {
	FailedSelectionID string
	TurnID            string
	NewSelectionID    string
	AuthorizedAt      time.Time
}

type AuthorizeM5ReplyTraceRearmResult struct {
	Selection         M5TrialSelection
	Turn              DialogueTurn
	AlreadyAuthorized bool
}

// AuthorizeM5ReplyTraceRearm 只授权 2026-07-23 已获批的一个真实失败事实。
// 它不改写旧 selection/invocation，只恢复可运行投影并追加一次脱敏审计。
func (s *Store) AuthorizeM5ReplyTraceRearm(
	req AuthorizeM5ReplyTraceRearmRequest,
) (*AuthorizeM5ReplyTraceRearmResult, error) {
	req.FailedSelectionID = strings.TrimSpace(req.FailedSelectionID)
	req.TurnID = strings.TrimSpace(req.TurnID)
	req.NewSelectionID = strings.TrimSpace(req.NewSelectionID)
	if req.FailedSelectionID != m5ReplyTraceRearmFailedSelectionID ||
		req.TurnID != m5ReplyTraceRearmTurnID ||
		req.NewSelectionID == "" ||
		req.NewSelectionID == req.FailedSelectionID {
		return nil, ErrM5ReplyTraceRearmUnsafe
	}
	if req.AuthorizedAt.IsZero() {
		req.AuthorizedAt = time.Now()
	}

	out := &AuthorizeM5ReplyTraceRearmResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		failed, turn, _, _, err := loadM5ReplyTraceRearmFactsTx(
			tx, req.FailedSelectionID, req.TurnID,
		)
		if err != nil {
			return err
		}
		if err := requireM5ReplyTraceRearmAccountStoppedTx(tx, failed.ProfileID); err != nil {
			return err
		}

		var active M5TrialSelection
		activeErr := tx.First(&active,
			"status = ? AND active_slot = ?", M5TrialSelectionActive, m5TrialActiveSlot,
		).Error
		if activeErr == nil {
			if active.ProfileID != failed.ProfileID ||
				active.Reason != m5ReplyTraceRearmSelectionReason ||
				turn.Status != DialogueTurnClassified ||
				turn.IntentLabel != m5ai.IntentInterested ||
				turn.IntentSource != DialogueIntentBusinessEvent ||
				turn.FailureReason != "" ||
				requireM5ReplyTraceRearmAuditTx(tx, failed.SelectionID, turn.TurnID) != nil ||
				requireNoM5ReplyBudgetRecoveryOutputTx(tx, turn.TurnID) != nil ||
				validateDialogueTurnCurrentTx(tx, turn) != nil {
				return ErrM5ReplyTraceRearmUnsafe
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
			return ErrM5ReplyTraceRearmUnsafe
		}
		var existingAudit int64
		if err := tx.Model(&AuditEntry{}).Where(
			"category = ? AND round_id = ?", m5ReplyTraceRearmAuditCategory, turn.TurnID,
		).Count(&existingAudit).Error; err != nil {
			return err
		}
		if existingAudit != 0 {
			return ErrM5ReplyTraceRearmUnsafe
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
			Reason:      m5ReplyTraceRearmSelectionReason,
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
			return ErrM5ReplyTraceRearmUnsafe
		}
		if err := tx.First(&turn, "turn_id = ?", turn.TurnID).Error; err != nil {
			return err
		}
		if err := validateDialogueTurnCurrentTx(tx, turn); err != nil {
			return ErrM5ReplyTraceRearmUnsafe
		}
		if err := tx.Create(&AuditEntry{
			At: req.AuthorizedAt, Category: m5ReplyTraceRearmAuditCategory,
			RefMsgID: failed.SelectionID, RoundID: turn.TurnID,
			Detail: m5ReplyTraceRearmAuditDetail,
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

type ReserveM5ReplyTraceRearmRequest struct {
	InvocationID string
	TurnID       string
	Provider     string
	Model        string
	InputHash    string
	CreatedAt    time.Time
}

// ReserveAuthorizedM5ReplyTraceRearm 是唯一 attempt=3 预留点。
func (s *Store) ReserveAuthorizedM5ReplyTraceRearm(
	req ReserveM5ReplyTraceRearmRequest,
) (*ReserveAIInvocationResult, error) {
	req.InvocationID = strings.TrimSpace(req.InvocationID)
	req.TurnID = strings.TrimSpace(req.TurnID)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	req.InputHash = strings.TrimSpace(req.InputHash)
	if req.InvocationID == "" || req.TurnID != m5ReplyTraceRearmTurnID ||
		req.Provider == "" || req.Model == "" || req.InputHash == "" {
		return nil, ErrM5ReplyTraceRearmUnsafe
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}

	out := &ReserveAIInvocationResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		failed, turn, _, attempt2, err := loadM5ReplyTraceRearmFactsTx(
			tx, m5ReplyTraceRearmFailedSelectionID, req.TurnID,
		)
		if err != nil {
			return err
		}
		if turn.Status != DialogueTurnClassified ||
			turn.IntentLabel != m5ai.IntentInterested ||
			turn.IntentSource != DialogueIntentBusinessEvent ||
			turn.FailureReason != "" ||
			attempt2.Provider != req.Provider ||
			attempt2.Model != req.Model ||
			attempt2.InputHash != req.InputHash ||
			attempt2.ContextRevisionHash != turn.ContextRevisionHash {
			return ErrM5ReplyTraceRearmUnsafe
		}
		if err := requireM5ReplyTraceRearmAccountStoppedTx(tx, failed.ProfileID); err != nil {
			return err
		}
		var active M5TrialSelection
		if err := tx.First(&active,
			"profile_id = ? AND status = ? AND active_slot = ? AND reason = ?",
			turn.ProfileID, M5TrialSelectionActive, m5TrialActiveSlot,
			m5ReplyTraceRearmSelectionReason,
		).Error; err != nil {
			return ErrM5ReplyTraceRearmUnsafe
		}
		if err := requireM5ReplyTraceRearmAuditTx(tx, failed.SelectionID, turn.TurnID); err != nil {
			return err
		}
		if err := validateDialogueTurnCurrentTx(tx, turn); err != nil {
			return ErrDialogueTurnBinding
		}
		if err := requireNoM5ReplyBudgetRecoveryOutputTx(tx, turn.TurnID); err != nil {
			return err
		}

		wanted := AIInvocation{
			InvocationID: req.InvocationID, TurnID: turn.TurnID,
			Purpose: m5ai.PurposeReply, Attempt: m5ReplyTraceRearmAttempt,
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
		case 2:
		case 3:
			if !sameInvocationReservation(invocations[2], wanted) {
				return ErrM5ReplyTraceRearmUnsafe
			}
			out.Invocation = invocations[2]
			return nil
		default:
			return ErrM5ReplyTraceRearmUnsafe
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

func loadM5ReplyTraceRearmFactsTx(
	tx *gorm.DB,
	failedSelectionID string,
	turnID string,
) (M5TrialSelection, DialogueTurn, AIInvocation, AIInvocation, error) {
	if failedSelectionID != m5ReplyTraceRearmFailedSelectionID ||
		turnID != m5ReplyTraceRearmTurnID {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, AIInvocation{},
			ErrM5ReplyTraceRearmUnsafe
	}
	var failed M5TrialSelection
	if err := tx.First(&failed, "selection_id = ?", failedSelectionID).Error; err != nil ||
		failed.Status != M5TrialSelectionManualRequired ||
		failed.ActiveSlot != nil ||
		failed.SelectedBy != "user" ||
		failed.Reason != "replyFailed" ||
		failed.EndedAt == nil {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, AIInvocation{},
			ErrM5ReplyTraceRearmUnsafe
	}
	var turn DialogueTurn
	if err := tx.First(&turn, "turn_id = ? AND profile_id = ?", turnID, failed.ProfileID).Error; err != nil {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, AIInvocation{},
			ErrM5ReplyTraceRearmUnsafe
	}
	var budgetAudits []AuditEntry
	if err := tx.Where(
		"category = ? AND round_id = ? AND detail = ?",
		m5ReplyBudgetRecoveryAuditCategory, turn.TurnID, m5ReplyBudgetRecoveryAuditDetail,
	).Find(&budgetAudits).Error; err != nil {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, AIInvocation{}, err
	}
	if len(budgetAudits) != 1 || strings.TrimSpace(budgetAudits[0].RefMsgID) == "" {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, AIInvocation{},
			ErrM5ReplyTraceRearmUnsafe
	}
	legacyFailed, legacyTurn, attempt1, err := loadM5ReplyBudgetRecoveryLegacyTx(
		tx, budgetAudits[0].RefMsgID,
	)
	if err != nil ||
		legacyFailed.ProfileID != failed.ProfileID ||
		legacyTurn.TurnID != turn.TurnID {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, AIInvocation{},
			ErrM5ReplyTraceRearmUnsafe
	}
	var invocations []AIInvocation
	if err := tx.Where("turn_id = ?", turn.TurnID).
		Order("attempt, invocation_id").Find(&invocations).Error; err != nil {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, AIInvocation{}, err
	}
	if len(invocations) < 2 ||
		invocations[0].InvocationID != attempt1.InvocationID ||
		!isApprovedM5ReplyTraceRearmAttempt2(invocations[1], attempt1, turn) {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, AIInvocation{},
			ErrM5ReplyTraceRearmUnsafe
	}
	if len(invocations) > 3 ||
		(len(invocations) == 3 && invocations[2].Attempt != m5ReplyTraceRearmAttempt) {
		return M5TrialSelection{}, DialogueTurn{}, AIInvocation{}, AIInvocation{},
			ErrM5ReplyTraceRearmUnsafe
	}
	return failed, turn, attempt1, invocations[1], nil
}

func isApprovedM5ReplyTraceRearmAttempt2(
	invocation AIInvocation,
	attempt1 AIInvocation,
	turn DialogueTurn,
) bool {
	return invocation.InvocationID == m5ReplyTraceRearmAttempt2InvocationID &&
		invocation.TurnID == turn.TurnID &&
		invocation.Purpose == m5ai.PurposeReply &&
		invocation.Attempt == 2 &&
		invocation.Provider == attempt1.Provider &&
		invocation.Model == attempt1.Model &&
		invocation.ContextRevisionHash == turn.ContextRevisionHash &&
		invocation.ContextRevisionHash == attempt1.ContextRevisionHash &&
		invocation.InputHash == attempt1.InputHash &&
		invocation.Status == AIInvocationInvalidOutput &&
		invocation.ErrorClass == "invalidOutput" &&
		invocation.OutputHash == m5ReplyTraceRearmAttempt2OutputHash &&
		invocation.InputTokens == 7315 &&
		invocation.CachedInputTokens == 384 &&
		invocation.OutputTokens == 199 &&
		invocation.ReasoningTokens == nil &&
		invocation.UsageShape == AIInvocationReasoningFieldAbsent &&
		invocation.LatencyMs == 5014 &&
		invocation.EstimatedCostMicros == 3190 &&
		invocation.FinishedAt != nil &&
		invocation.FailureStage == "" &&
		invocation.ErrorDetailCode == "" &&
		invocation.ProviderHTTPStatus == nil &&
		invocation.RequestBytes == 0 &&
		invocation.ResponseBytes == 0 &&
		invocation.TraceStatus == ""
}

func requireM5ReplyTraceRearmAccountStoppedTx(tx *gorm.DB, profileID string) error {
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		return err
	}
	var account Account
	if err := tx.First(&account,
		"platform = ? AND account_ref = ?", profile.Platform, profile.AccountRef,
	).Error; err != nil {
		return err
	}
	if account.StoppedAt == nil || account.PausedReason != "userStopped" {
		return ErrM5ReplyTraceRearmUnsafe
	}
	return nil
}

func requireM5ReplyTraceRearmAuditTx(
	tx *gorm.DB,
	failedSelectionID string,
	turnID string,
) error {
	var count int64
	if err := tx.Model(&AuditEntry{}).Where(
		"category = ? AND ref_msg_id = ? AND round_id = ? AND detail = ?",
		m5ReplyTraceRearmAuditCategory, failedSelectionID, turnID, m5ReplyTraceRearmAuditDetail,
	).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrM5ReplyTraceRearmUnsafe
	}
	return nil
}
