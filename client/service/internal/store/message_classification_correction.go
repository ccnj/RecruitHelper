package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const messageRetractionReasonClassificationCorrected = "classification_corrected"

var (
	ErrMessageClassificationCorrectionUnsafe = errors.New("消息分类修正缺少完整唯一证据")
	ErrMessageSourceKeyConflict              = errors.New("消息 sourceKey 已绑定到冲突事实")
)

// CorrectMessageClassificationRequest 只承载已由 syncledger 证明的单条尾行
// system/system -> in/text 修正。SourceKey 的原文平台身份留在手内；这里仅
// 持久化不可解析的等值键。
type CorrectMessageClassificationRequest struct {
	Key             ConversationKey
	RoundID         string
	ExpectedTailSeq int64
	OldSeq          int64
	Corrected       MessageDraft
	PauseReason     string
	SyncedAt        time.Time
}

type CorrectMessageClassificationResult struct {
	Corrected          Message
	TailSeq            int64
	AdoptedBoundarySeq int64
	AlreadyApplied     bool
}

func validateMessageClassificationCorrection(req CorrectMessageClassificationRequest) error {
	if req.Key.Platform == "" || req.Key.AccountRef == "" || req.Key.ConversationRef == "" ||
		req.RoundID == "" || strings.TrimSpace(req.PauseReason) == "" ||
		req.ExpectedTailSeq <= 0 || req.OldSeq != req.ExpectedTailSeq {
		return ErrMessageClassificationCorrectionUnsafe
	}
	draft := req.Corrected
	if draft.Direction != "in" || draft.Kind != "text" || draft.Origin != "external" ||
		draft.ContentHash == "" || draft.Text == nil || strings.TrimSpace(*draft.Text) == "" ||
		draft.TsApproxMs == nil || draft.SourceKey == nil || !validMessageSourceKey(*draft.SourceKey) ||
		draft.BlobRef != "" || draft.CardType != "" || draft.CardState != "" {
		return ErrMessageClassificationCorrectionUnsafe
	}
	return nil
}

func correctionSourceKeyWhere(key ConversationKey) (string, []any) {
	return "platform = ? AND account_ref = ? AND conversation_ref = ? AND source_key = ?", []any{
		key.Platform, key.AccountRef, key.ConversationRef,
	}
}

func sameCorrectedMessage(message Message, draft MessageDraft) bool {
	return message.RetractedAt == nil && message.Kind == "text" && message.Origin == "external" &&
		message.Text != nil && draft.Text != nil && *message.Text == *draft.Text &&
		message.TsApproxMs != nil && draft.TsApproxMs != nil && *message.TsApproxMs == *draft.TsApproxMs &&
		message.BlobRef == "" && message.CardType == "" && message.CardState == ""
}

func correctionOldMessageMatches(old Message, draft MessageDraft) bool {
	return old.RetractedAt == nil && old.Direction == "system" && old.Kind == "system" &&
		old.Origin == "external" && old.SourceKey == nil && old.ContentHash == draft.ContentHash &&
		old.Text != nil && draft.Text != nil && *old.Text == *draft.Text &&
		old.TsApproxMs != nil && draft.TsApproxMs != nil && *old.TsApproxMs == *draft.TsApproxMs &&
		old.BlobRef == "" && old.CardType == "" && old.CardState == ""
}

// CorrectMessageClassification 把被更强平台事实推翻的旧分类、修正行、会话尾、
// 账号暂停与审计放在同一事务。它不修改 adoptedBoundarySeq，也不把修正重复计入
// 巡检轮 newMessageCount。重复调用只接受本函数已经完整落下的同一修正与暂停事实。
func (s *Store) CorrectMessageClassification(
	req CorrectMessageClassificationRequest,
) (*CorrectMessageClassificationResult, error) {
	if err := validateMessageClassificationCorrection(req); err != nil {
		return nil, err
	}
	if req.SyncedAt.IsZero() {
		req.SyncedAt = time.Now()
	}
	result := &CorrectMessageClassificationResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var account Account
		if err := tx.First(&account, "platform = ? AND account_ref = ?", req.Key.Platform, req.Key.AccountRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		var conversation Conversation
		if err := tx.Where(conversationWhere(req.Key), conversationArgs(req.Key)...).First(&conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationNotFound
			}
			return err
		}
		if conversation.TrackingState != TrackingAdopted {
			return ErrConversationNotTracked
		}
		var intent TrackedIntent
		if err := tx.Where(conversationWhere(req.Key), conversationArgs(req.Key)...).First(&intent).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTrackingStateCorrupt
			}
			return err
		}
		if intent.Status != conversation.TrackingState {
			return ErrTrackingStateCorrupt
		}
		if err := requirePatrolRound(tx, req.Key.Platform, req.Key.AccountRef, req.RoundID); err != nil {
			return err
		}

		var old Message
		if err := tx.First(&old,
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
			req.Key.Platform, req.Key.AccountRef, req.Key.ConversationRef, req.OldSeq).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationVersionConflict
			}
			return err
		}

		where, args := correctionSourceKeyWhere(req.Key)
		args = append(args, *req.Corrected.SourceKey)
		var existing Message
		existingErr := tx.Where(where, args...).First(&existing).Error
		if existingErr != nil && !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}
		if existingErr == nil {
			if existing.Direction != req.Corrected.Direction || existing.ContentHash != req.Corrected.ContentHash {
				return ErrMessageSourceKeyConflict
			}
			if !sameCorrectedMessage(existing, req.Corrected) ||
				old.RetractedAt == nil || old.RetractionReason != messageRetractionReasonClassificationCorrected ||
				existing.Seq <= old.Seq || existing.FirstSeenRoundID != old.FirstSeenRoundID {
				return ErrMessageClassificationCorrectionUnsafe
			}
			if conversation.LastMessageSeq < existing.Seq {
				return ErrMessageClassificationCorrectionUnsafe
			}
			if account.StoppedAt == nil || account.PausedReason != req.PauseReason || !account.DirtyHint {
				return ErrMessageClassificationCorrectionUnsafe
			}
			result.Corrected = existing
			result.TailSeq = conversation.LastMessageSeq
			result.AdoptedBoundarySeq = conversation.AdoptedBoundarySeq
			result.AlreadyApplied = true
			return nil
		}

		if conversation.LastMessageSeq != req.ExpectedTailSeq {
			return ErrConversationVersionConflict
		}
		if !correctionOldMessageMatches(old, req.Corrected) {
			return ErrMessageClassificationCorrectionUnsafe
		}
		marked := tx.Model(&Message{}).
			Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND "+activeMessageCondition+" AND source_key IS NULL",
				req.Key.Platform, req.Key.AccountRef, req.Key.ConversationRef, req.OldSeq).
			Updates(map[string]any{
				"retracted_at": req.SyncedAt, "retraction_reason": messageRetractionReasonClassificationCorrected,
			})
		if marked.Error != nil {
			return marked.Error
		}
		if marked.RowsAffected != 1 {
			return ErrConversationVersionConflict
		}

		newSeq, err := nextPhysicalMessageSeqTx(tx, req.Key)
		if err != nil {
			return err
		}
		textCopy := *req.Corrected.Text
		tsCopy := *req.Corrected.TsApproxMs
		sourceKeyCopy := *req.Corrected.SourceKey
		corrected := Message{
			Platform: req.Key.Platform, AccountRef: req.Key.AccountRef, ConversationRef: req.Key.ConversationRef,
			Seq: newSeq, Direction: "in", Kind: "text", ContentHash: req.Corrected.ContentHash,
			Text: &textCopy, TsApproxMs: &tsCopy, Origin: "external", FirstSeenRoundID: old.FirstSeenRoundID,
			SourceKey: &sourceKeyCopy,
		}
		if err := tx.Create(&corrected).Error; err != nil {
			return err
		}
		updated := tx.Model(&Conversation{}).
			Where(conversationWhere(req.Key), conversationArgs(req.Key)...).
			Where("last_message_seq = ?", req.ExpectedTailSeq).
			Updates(map[string]any{
				"last_message_seq": newSeq, "last_message_direction": "in", "last_message_kind": "text",
				"last_message_preview": textCopy, "last_synced_round_id": req.RoundID, "last_synced_at": req.SyncedAt,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrConversationVersionConflict
		}
		paused := tx.Model(&Account{}).
			Where("platform = ? AND account_ref = ?", req.Key.Platform, req.Key.AccountRef).
			Updates(map[string]any{
				"stopped_at": req.SyncedAt, "paused_reason": req.PauseReason, "dirty_hint": true,
			})
		if paused.Error != nil {
			return paused.Error
		}
		if paused.RowsAffected != 1 {
			return ErrAccountNotFound
		}
		detail := fmt.Sprintf(
			"oldSeq=%d newSeq=%d from=system/system to=in/text reason=%s roundId=%s",
			req.OldSeq, newSeq, messageRetractionReasonClassificationCorrected, req.RoundID,
		)
		if err := tx.Create(&AuditEntry{
			At: req.SyncedAt, Category: "conversation_message_classification_corrected",
			Platform: req.Key.Platform, AccountRef: req.Key.AccountRef,
			ConversationRef: req.Key.ConversationRef, RoundID: req.RoundID, Detail: detail,
		}).Error; err != nil {
			return err
		}

		result.Corrected = corrected
		result.TailSeq = newSeq
		result.AdoptedBoundarySeq = conversation.AdoptedBoundarySeq
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
