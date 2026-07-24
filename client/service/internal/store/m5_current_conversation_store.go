package store

import (
	"strings"

	"gorm.io/gorm"
)

// CommunicationTargetForProfile 复用生产目标装配规则，只解析一个明确档案。
// “处理当前会话”先由平台当前路由反查 profile，再调用本方法，因此不会枚举
// 或推进同账号下的其他候选人。
func (s *Store) CommunicationTargetForProfile(
	profileID string,
) (*CommunicationTarget, bool, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, false, ErrCommunicationTargetInvalid
	}
	var target CommunicationTarget
	ready := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		target, ready, err = communicationTargetTx(tx, profileID)
		return err
	})
	if err != nil || !ready {
		return nil, ready, err
	}
	return &target, true, nil
}
