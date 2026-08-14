package store

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 巡检单人隔离（2026-07-27 甲方裁决）：单个候选人处理中的确定性错误只隔离
// 该会话，账号轮继续处理其他人。标记与解除都只改运行状态列；首次打标才
// 返回 true，让调用方只在首次留响亮审计，避免每轮重复刷同一事实。

// QuarantineConversationPatrol 给单个 tracked 会话打巡检隔离标记。
// 已隔离的会话保持原 reason 与时间不变（幂等，返回 false）。
func (s *Store) QuarantineConversationPatrol(
	key ConversationKey,
	reason string,
	at time.Time,
) (bool, error) {
	if strings.TrimSpace(reason) == "" {
		return false, errors.New("巡检隔离原因不能为空")
	}
	if at.IsZero() {
		at = time.Now()
	}
	newlyMarked := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var conversation Conversation
		err := tx.Where(conversationWhere(key), conversationArgs(key)...).
			First(&conversation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrConversationNotFound
		}
		if err != nil {
			return err
		}
		if conversation.PatrolQuarantinedAt != nil {
			return nil
		}
		updated := tx.Model(&Conversation{}).
			Where(conversationWhere(key), conversationArgs(key)...).
			Where("patrol_quarantined_at IS NULL").
			Updates(map[string]any{
				"patrol_quarantined_at":    at.UTC(),
				"patrol_quarantine_reason": reason,
				"updated_at":               at.UTC(),
			})
		if updated.Error != nil {
			return updated.Error
		}
		newlyMarked = updated.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return newlyMarked, nil
}

// ClearConversationPatrolQuarantine 人工解除单个会话的巡检隔离。
// 未处于隔离状态时返回 false，不视为错误。
func (s *Store) ClearConversationPatrolQuarantine(
	key ConversationKey,
	at time.Time,
) (bool, error) {
	if at.IsZero() {
		at = time.Now()
	}
	cleared := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var conversation Conversation
		err := tx.Where(conversationWhere(key), conversationArgs(key)...).
			First(&conversation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrConversationNotFound
		}
		if err != nil {
			return err
		}
		if conversation.PatrolQuarantinedAt == nil {
			return nil
		}
		updated := tx.Model(&Conversation{}).
			Where(conversationWhere(key), conversationArgs(key)...).
			Where("patrol_quarantined_at IS NOT NULL").
			Updates(map[string]any{
				"patrol_quarantined_at":    nil,
				"patrol_quarantine_reason": "",
				"updated_at":               at.UTC(),
			})
		if updated.Error != nil {
			return updated.Error
		}
		cleared = updated.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return cleared, nil
}

// PatrolQuarantineRow 是诊断台隔离列表的一行。peer 名与档案状态只为让人认出
// "这是谁、冻没冻",不参与任何判定。
type PatrolQuarantineRow struct {
	Platform        string     `json:"platform"`
	AccountRef      string     `json:"accountRef"`
	ConversationRef string     `json:"conversationRef"`
	PeerDisplayName string     `json:"peerDisplayName"`
	Reason          string     `json:"reason"`
	QuarantinedAt   time.Time  `json:"quarantinedAt"`
	ProfileID       string     `json:"profileId,omitempty"`
	ProfileFrozen   bool       `json:"profileFrozen"`
}

// PatrolQuarantinedConversations 列出全部处于巡检隔离中的会话。
// 隔离此前是"安静但永久"的:出不来、也看不见。列表是可见性的那一半,
// 解除见 ReleasePatrolQuarantine(2026-08-14 甲方裁决)。
func (s *Store) PatrolQuarantinedConversations() ([]PatrolQuarantineRow, error) {
	var conversations []Conversation
	if err := s.db.Where("patrol_quarantined_at IS NOT NULL").
		Order("patrol_quarantined_at DESC").Find(&conversations).Error; err != nil {
		return nil, err
	}
	rows := make([]PatrolQuarantineRow, 0, len(conversations))
	for i := range conversations {
		c := conversations[i]
		row := PatrolQuarantineRow{
			Platform: c.Platform, AccountRef: c.AccountRef,
			ConversationRef: c.ConversationRef, PeerDisplayName: c.PeerDisplayName,
			Reason: c.PatrolQuarantineReason,
		}
		if c.PatrolQuarantinedAt != nil {
			row.QuarantinedAt = *c.PatrolQuarantinedAt
		}
		var profile CandidateProfile
		err := s.db.First(&profile,
			"platform = ? AND account_ref = ? AND conversation_ref = ?",
			c.Platform, c.AccountRef, c.ConversationRef).Error
		if err == nil {
			row.ProfileID = profile.ProfileID
			var aggregate CommunicationV4Aggregate
			if s.db.First(&aggregate, "profile_id = ?", profile.ProfileID).Error == nil {
				row.ProfileFrozen =
					aggregate.AutomationStatus == ProfileCommunicationAutomationManualRequired
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// ReleasePatrolQuarantine 解除单个会话的巡检隔离,并在同一事务里把隔离时
// 一并冻结的档案聚合解冻。
//
// 聚合解冻带 CAS:只有 manual_reason 恰等于本会话的隔离原因才动——别的
// 人工原因(effectSuspect、业务转人工)不是巡检冻的,不得顺手解冻,与
// unfreezeCommunicationV4AggregateAfterResolvedFailedTx 同一纪律。
// 会话未处于隔离时返回 (false, false, nil),幂等。
func (s *Store) ReleasePatrolQuarantine(
	key ConversationKey,
	at time.Time,
) (released bool, profileResumed bool, err error) {
	if at.IsZero() {
		at = time.Now()
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var conversation Conversation
		findErr := tx.Where(conversationWhere(key), conversationArgs(key)...).
			First(&conversation).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			return ErrConversationNotFound
		}
		if findErr != nil {
			return findErr
		}
		if conversation.PatrolQuarantinedAt == nil {
			return nil
		}
		reason := conversation.PatrolQuarantineReason
		updated := tx.Model(&Conversation{}).
			Where(conversationWhere(key), conversationArgs(key)...).
			Where("patrol_quarantined_at IS NOT NULL").
			Updates(map[string]any{
				"patrol_quarantined_at":    nil,
				"patrol_quarantine_reason": "",
				"updated_at":               at.UTC(),
			})
		if updated.Error != nil {
			return updated.Error
		}
		released = updated.RowsAffected == 1
		if !released || reason == "" {
			return nil
		}
		var profile CandidateProfile
		profileErr := tx.First(&profile,
			"platform = ? AND account_ref = ? AND conversation_ref = ?",
			key.Platform, key.AccountRef, key.ConversationRef).Error
		if errors.Is(profileErr, gorm.ErrRecordNotFound) {
			return nil
		}
		if profileErr != nil {
			return profileErr
		}
		resumed := tx.Model(&CommunicationV4Aggregate{}).
			Where(
				"profile_id = ? AND automation_status = ? AND manual_reason = ?",
				profile.ProfileID,
				ProfileCommunicationAutomationManualRequired,
				reason,
			).
			Updates(map[string]any{
				"automation_status":  ProfileCommunicationAutomationActive,
				"manual_reason":      "",
				"manual_required_at": nil,
				"updated_at":         at.UTC(),
			})
		if resumed.Error != nil {
			return resumed.Error
		}
		profileResumed = resumed.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, false, err
	}
	return released, profileResumed, nil
}
