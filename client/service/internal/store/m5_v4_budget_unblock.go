package store

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// 本文件是 2026-07-27 废除全局调用量配额裁决的一次性配套解冻：被已删除
// 的日/月配额闸挂起的 v4 档案恢复 active，被同一原因挡在分类或建议步的
// turn 按其已冻结事实回到可续跑状态（collected/classified），不重造输入、
// 不触碰消息与游标。它有意不接入服务二进制、API、巡检或重启恢复路径，
// 只由独立 CLI 在停脑后调用。
const v4BudgetUnblockAuditCategory = "v4BudgetQuotaRemovalUnblock"

var v4BudgetUnblockReasons = []string{
	"dailyProviderBudgetBlocked",
	"monthlyTurnBudgetBlocked",
}

type V4BudgetUnblockResult struct {
	ProfileID  string
	Reason     string
	TurnsReset int
}

func (s *Store) UnblockV4BudgetQuotaProfiles() ([]V4BudgetUnblockResult, error) {
	var out []V4BudgetUnblockResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var aggregates []CommunicationV4Aggregate
		if err := tx.
			Where("automation_status = ? AND manual_reason IN ?",
				ProfileCommunicationAutomationManualRequired, v4BudgetUnblockReasons).
			Order("profile_id").
			Find(&aggregates).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		for index := range aggregates {
			aggregate := aggregates[index]
			var profile CandidateProfile
			if err := tx.First(&profile, "profile_id = ?", aggregate.ProfileID).Error; err != nil {
				return err
			}
			var turns []DialogueTurn
			if err := tx.
				Where("profile_id = ? AND status = ? AND failure_reason IN ?",
					aggregate.ProfileID, DialogueTurnManualRequired, v4BudgetUnblockReasons).
				Find(&turns).Error; err != nil {
				return err
			}
			for turnIndex := range turns {
				turn := turns[turnIndex]
				target := DialogueTurnCollected
				if turn.ClassifiedAt != nil {
					target = DialogueTurnClassified
				}
				updated := tx.Model(&DialogueTurn{}).
					Where("turn_id = ? AND status = ?", turn.TurnID, DialogueTurnManualRequired).
					Updates(map[string]any{
						"status":         target,
						"failure_reason": "",
						"updated_at":     now,
					})
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return ErrCommunicationV4Conflict
				}
			}
			updated := tx.Model(&CommunicationV4Aggregate{}).
				Where("profile_id = ? AND automation_status = ? AND manual_reason = ?",
					aggregate.ProfileID,
					ProfileCommunicationAutomationManualRequired,
					aggregate.ManualReason,
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
			conversationRef := ""
			if profile.ConversationRef != nil {
				conversationRef = *profile.ConversationRef
			}
			if err := tx.Create(&AuditEntry{
				At: now, Category: v4BudgetUnblockAuditCategory,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef: conversationRef,
				Detail: fmt.Sprintf(
					"reason=%s turnsReset=%d",
					aggregate.ManualReason, len(turns),
				),
			}).Error; err != nil {
				return err
			}
			out = append(out, V4BudgetUnblockResult{
				ProfileID:  aggregate.ProfileID,
				Reason:     aggregate.ManualReason,
				TurnsReset: len(turns),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
