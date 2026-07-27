package store

import (
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
)

// 冒烟直发闸的判定真值表：只有 active 聚合阻断；无档案、无聚合、挂人工放行。
// 直插行仅用于只读判定测试，不构成业务事实（参照 m5_context_store_test 先例）。
func TestCommunicationV4DirectSendBlockedOnlyForActiveAggregates(t *testing.T) {
	s := openTest(t)
	key := ConversationKey{
		Platform: "zhilian", AccountRef: "account-gate", ConversationRef: "conversation-gate",
	}

	blocked, err := s.CommunicationV4DirectSendBlocked(key)
	if err != nil || blocked {
		t.Fatalf("无档案会话必须放行: blocked=%v err=%v", blocked, err)
	}

	now := time.Now()
	if err := s.db.Create(&Candidate{
		Platform: key.Platform, PlatformUserRef: "peer-gate", FirstSeenAt: now, LastSeenAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	conversationRef := key.ConversationRef
	rootIntentID := "intent-root-gate"
	if err := s.db.Create(&CandidateProfile{
		ProfileID: "profile-gate", Platform: key.Platform, AccountRef: key.AccountRef,
		PlatformUserRef: "peer-gate", PositionRef: "position-gate",
		MainStatus: CandidateProfileGreeted, ConversationRef: &conversationRef,
		SuccessfulGreetingIntentID: &rootIntentID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	blocked, err = s.CommunicationV4DirectSendBlocked(key)
	if err != nil || blocked {
		t.Fatalf("有档案无聚合必须放行: blocked=%v err=%v", blocked, err)
	}

	if err := s.db.Create(&CommunicationV4Aggregate{
		ProfileID: "profile-gate", RootGreetingIntentID: "intent-root-gate",
		StateSchemaVersion: 1, Revision: 0, ProjectedThroughSeq: 0,
		State:            communication.NewV4GreetedState(&now),
		AutomationStatus: ProfileCommunicationAutomationActive,
	}).Error; err != nil {
		t.Fatal(err)
	}
	blocked, err = s.CommunicationV4DirectSendBlocked(key)
	if err != nil || !blocked {
		t.Fatalf("active 聚合必须阻断: blocked=%v err=%v", blocked, err)
	}

	if err := s.db.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ?", "profile-gate").
		Updates(map[string]any{
			"automation_status":  ProfileCommunicationAutomationManualRequired,
			"manual_reason":      "effectFailed",
			"manual_required_at": now,
		}).Error; err != nil {
		t.Fatal(err)
	}
	blocked, err = s.CommunicationV4DirectSendBlocked(key)
	if err != nil || blocked {
		t.Fatalf("挂人工聚合必须放行: blocked=%v err=%v", blocked, err)
	}
}
