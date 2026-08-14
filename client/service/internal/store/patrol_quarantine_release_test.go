package store

import (
	"testing"
	"time"
)

// 解除隔离要同时把巡检冻结的档案聚合解冻,但只解自己冻的:manual_reason
// 与隔离原因不一致时(effectSuspect、业务转人工)不得顺手动别人的冻结。
func TestReleasePatrolQuarantineResumesOnlyOwnFreeze(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	key := ConversationKey{Platform: "zhilian", AccountRef: "acc-q", ConversationRef: "conv-q"}
	now := time.Now()
	if err := s.db.Create(&Conversation{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		TrackingState: "adopted",
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 未隔离时解除是幂等空操作。
	released, resumed, err := s.ReleasePatrolQuarantine(key, now)
	if err != nil || released || resumed {
		t.Fatalf("未隔离时应为空操作: %v %v %v", released, resumed, err)
	}

	reason := "patrolQuarantine:hand:EXEC_TIMEOUT_HAND"
	if _, err := s.QuarantineConversationPatrol(key, reason, now); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&Candidate{
		Platform: key.Platform, PlatformUserRef: "peer-q",
		FirstSeenAt: now, LastSeenAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	conversationRef := key.ConversationRef
	if err := s.db.Create(&CandidateProfile{
		ProfileID: "p-q", Platform: key.Platform, AccountRef: key.AccountRef,
		PlatformUserRef: "peer-q", PositionRef: "pos-q", MainStatus: "communicating",
		ConversationRef: &conversationRef,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&CommunicationV4Aggregate{
		ProfileID: "p-q", RootGreetingIntentID: "gi-q", StateSchemaVersion: 1,
		AutomationStatus: ProfileCommunicationAutomationManualRequired,
		ManualReason:     reason,
	}).Error; err != nil {
		t.Fatal(err)
	}

	released, resumed, err = s.ReleasePatrolQuarantine(key, now.Add(time.Minute))
	if err != nil || !released || !resumed {
		t.Fatalf("应解除并解冻: released=%v resumed=%v err=%v", released, resumed, err)
	}
	var conversation Conversation
	if err := s.db.First(&conversation,
		"platform = ? AND account_ref = ? AND conversation_ref = ?",
		key.Platform, key.AccountRef, key.ConversationRef).Error; err != nil {
		t.Fatal(err)
	}
	if conversation.PatrolQuarantinedAt != nil || conversation.PatrolQuarantineReason != "" {
		t.Fatalf("隔离标记未清干净: %+v", conversation)
	}
	var aggregate CommunicationV4Aggregate
	if err := s.db.First(&aggregate, "profile_id = ?", "p-q").Error; err != nil {
		t.Fatal(err)
	}
	if aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" {
		t.Fatalf("档案未解冻: %+v", aggregate)
	}

	// 第二段:档案是别的原因冻的,解除隔离不得碰它。
	key2 := ConversationKey{Platform: "zhilian", AccountRef: "acc-q", ConversationRef: "conv-q2"}
	if err := s.db.Create(&Conversation{
		Platform: key2.Platform, AccountRef: key2.AccountRef, ConversationRef: key2.ConversationRef,
		TrackingState: "adopted",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.QuarantineConversationPatrol(key2, reason, now); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&Candidate{
		Platform: key2.Platform, PlatformUserRef: "peer-q2",
		FirstSeenAt: now, LastSeenAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	conversationRef2 := key2.ConversationRef
	if err := s.db.Create(&CandidateProfile{
		ProfileID: "p-q2", Platform: key2.Platform, AccountRef: key2.AccountRef,
		PlatformUserRef: "peer-q2", PositionRef: "pos-q", MainStatus: "communicating",
		ConversationRef: &conversationRef2,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&CommunicationV4Aggregate{
		ProfileID: "p-q2", RootGreetingIntentID: "gi-q2", StateSchemaVersion: 1,
		AutomationStatus: ProfileCommunicationAutomationManualRequired,
		ManualReason:     "effectSuspect",
	}).Error; err != nil {
		t.Fatal(err)
	}
	released, resumed, err = s.ReleasePatrolQuarantine(key2, now.Add(2*time.Minute))
	if err != nil || !released {
		t.Fatalf("会话应解除: %v %v", released, err)
	}
	if resumed {
		t.Fatal("effectSuspect 的冻结不是巡检冻的,不得顺手解冻")
	}
	var other CommunicationV4Aggregate
	if err := s.db.First(&other, "profile_id = ?", "p-q2").Error; err != nil {
		t.Fatal(err)
	}
	if other.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		other.ManualReason != "effectSuspect" {
		t.Fatalf("别人的冻结被动了: %+v", other)
	}
}
