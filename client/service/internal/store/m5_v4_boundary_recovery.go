package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 本文件是 0727当日计划3 批准的一次性定向恢复：解除因"游标必须指向出站"
// 旧断言而被错误冻结的 outboundBoundaryMissing 档案。它有意不接入服务
// 二进制、HTTP API、巡检循环或重启恢复路径，只由独立 CLI 在停脑后调用。
// 它不触碰消息、turn、投影游标或任何发送事实。

const v4BoundaryRecoveryAuditCategory = "v4BoundaryLockRecovery"

var ErrV4BoundaryRecoveryUnsafe = errors.New("V4 边界锁死恢复前置事实不完整")

type V4BoundaryLockRecoveryResult struct {
	Applied             bool
	AlreadyRecovered    bool
	AnchorSeq           int64
	CursorSeq           int64
	UncoveredInboundSeq int64
}

func (s *Store) RecoverV4OutboundBoundaryLock(
	profileID string,
) (*V4BoundaryLockRecoveryResult, error) {
	if strings.TrimSpace(profileID) == "" {
		return nil, ErrCommunicationV4Invalid
	}
	out := &V4BoundaryLockRecoveryResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		aggregate, err := communicationV4AggregateTx(tx, profileID)
		if err != nil {
			return err
		}
		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
			return err
		}
		if profile.ConversationRef == nil {
			return ErrV4BoundaryRecoveryUnsafe
		}
		var priorAudits int64
		if err := tx.Model(&AuditEntry{}).
			Where(
				"category = ? AND platform = ? AND account_ref = ? AND conversation_ref = ?",
				v4BoundaryRecoveryAuditCategory,
				profile.Platform, profile.AccountRef, *profile.ConversationRef,
			).
			Count(&priorAudits).Error; err != nil {
			return err
		}
		if aggregate.AutomationStatus == ProfileCommunicationAutomationActive {
			if priorAudits > 0 {
				out.AlreadyRecovered = true
				out.CursorSeq = aggregate.ProjectedThroughSeq
				return nil
			}
			return ErrV4BoundaryRecoveryUnsafe
		}
		if aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
			aggregate.ManualReason != "outboundBoundaryMissing" {
			return ErrV4BoundaryRecoveryUnsafe
		}

		// 游标必须停在一条已入账的平台中性行上——这正是被错误定罪的形状。
		var cursorRow Message
		if err := tx.First(
			&cursorRow,
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND retracted_at IS NULL",
			profile.Platform, profile.AccountRef, *profile.ConversationRef,
			aggregate.ProjectedThroughSeq,
		).Error; err != nil {
			return ErrV4BoundaryRecoveryUnsafe
		}
		if cursorRow.Direction != "system" &&
			!(cursorRow.Direction == "in" && cursorRow.Kind == "system") {
			return ErrV4BoundaryRecoveryUnsafe
		}

		// 游标之后必须存在未处理的候选人消息；游标即投影边界，其后的
		// 候选人输入必然未被任何 turn 覆盖。
		var uncovered Message
		if err := tx.Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL "+
				"AND direction = ? AND kind <> ? AND seq > ?",
			profile.Platform, profile.AccountRef, *profile.ConversationRef,
			"in", "system", aggregate.ProjectedThroughSeq,
		).Order("seq").First(&uncovered).Error; err != nil {
			return ErrV4BoundaryRecoveryUnsafe
		}

		anchorSeq, err := communicationV4OutboundAnchorSeqTx(tx, aggregate)
		if err != nil {
			return ErrV4BoundaryRecoveryUnsafe
		}

		var unfinishedTurns int64
		if err := tx.Model(&DialogueTurn{}).
			Where("profile_id = ? AND status IN ?", profileID, []DialogueTurnStatus{
				DialogueTurnCollected, DialogueTurnClassified,
				DialogueTurnAdviceReady, DialogueTurnDispatching,
			}).
			Count(&unfinishedTurns).Error; err != nil {
			return err
		}
		var openActions int64
		if err := tx.Model(&CommunicationAction{}).
			Joins("JOIN dialogue_turns ON dialogue_turns.turn_id = communication_actions.turn_id").
			Where("dialogue_turns.profile_id = ? AND communication_actions.status IN ?",
				profileID,
				[]CommunicationActionStatus{
					CommunicationActionPlanned, CommunicationActionEffectPending,
				}).
			Count(&openActions).Error; err != nil {
			return err
		}
		var openEventActions int64
		if err := tx.Model(&CommunicationV4EventAction{}).
			Where("profile_id = ? AND status IN ?", profileID,
				[]CommunicationV4EventActionStatus{
					CommunicationV4EventActionPlanned,
					CommunicationV4EventActionEffectPending,
				}).
			Count(&openEventActions).Error; err != nil {
			return err
		}
		var openIntents int64
		if err := tx.Model(&EffectIntent{}).
			Where(
				"intent_id IN (?) AND status NOT IN ?",
				tx.Model(&CommunicationV4EventAction{}).
					Select("effect_intent_id").
					Where("profile_id = ? AND effect_intent_id IS NOT NULL", profileID),
				[]string{"ok", "resolvedOk", "failed", "resolvedFailed"},
			).
			Count(&openIntents).Error; err != nil {
			return err
		}
		if unfinishedTurns != 0 || openActions != 0 || openEventActions != 0 || openIntents != 0 {
			return ErrV4BoundaryRecoveryUnsafe
		}

		now := time.Now().UTC()
		updated := tx.Model(&CommunicationV4Aggregate{}).
			Where(
				"profile_id = ? AND automation_status = ? AND manual_reason = ?",
				profileID,
				ProfileCommunicationAutomationManualRequired,
				"outboundBoundaryMissing",
			).
			Updates(map[string]any{
				"automation_status":  ProfileCommunicationAutomationActive,
				"manual_reason":      "",
				"manual_required_at": nil,
				"updated_at":         now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCommunicationV4Conflict
		}
		if err := tx.Create(&AuditEntry{
			At: now, Category: v4BoundaryRecoveryAuditCategory,
			Platform: profile.Platform, AccountRef: profile.AccountRef,
			ConversationRef: *profile.ConversationRef,
			Detail: fmt.Sprintf(
				"reason=outboundBoundaryMissing cursorSeq=%d cursorRow=%s/%s anchorSeq=%d uncoveredInboundSeq=%d",
				aggregate.ProjectedThroughSeq, cursorRow.Direction, cursorRow.Kind,
				anchorSeq, uncovered.Seq,
			),
		}).Error; err != nil {
			return err
		}
		out.Applied = true
		out.AnchorSeq = anchorSeq
		out.CursorSeq = aggregate.ProjectedThroughSeq
		out.UncoveredInboundSeq = uncovered.Seq
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
