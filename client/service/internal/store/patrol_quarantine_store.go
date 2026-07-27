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
