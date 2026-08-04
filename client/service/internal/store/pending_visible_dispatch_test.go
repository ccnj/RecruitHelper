package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
)

const (
	pendingDispatchPlatform = "zhilian"
	pendingDispatchAccount  = "a-pending-dispatch"
)

var pendingDispatchPlannedAt = time.Date(2026, 8, 4, 11, 46, 22, 0, time.UTC)

func seedPendingDispatchProfile(
	t *testing.T,
	s *Store,
	suffix string,
	automation ProfileCommunicationAutomationStatus,
) (string, ConversationKey) {
	t.Helper()
	profileID := "profile-" + suffix
	conversationRef := "conversation-" + suffix
	key := ConversationKey{
		Platform:        pendingDispatchPlatform,
		AccountRef:      pendingDispatchAccount,
		ConversationRef: conversationRef,
	}
	// 档案的 (platform, platformUserRef) 外键指向人的平台身份根,先立根。
	if err := s.db.Create(&Candidate{
		Platform:        key.Platform,
		PlatformUserRef: "person-" + suffix,
		FirstSeenAt:     pendingDispatchPlannedAt,
		LastSeenAt:      pendingDispatchPlannedAt,
	}).Error; err != nil {
		t.Fatalf("造候选人身份根失败: %v", err)
	}
	if err := s.db.Create(&CandidateProfile{
		ProfileID:       profileID,
		Platform:        key.Platform,
		AccountRef:      key.AccountRef,
		PlatformUserRef: "person-" + suffix,
		PositionRef:     "position-" + suffix,
		MainStatus:      CandidateProfileSelected,
		ConversationRef: &conversationRef,
	}).Error; err != nil {
		t.Fatalf("造候选人档案失败: %v", err)
	}
	if err := s.db.Create(&CommunicationV4Aggregate{
		ProfileID:            profileID,
		RootGreetingIntentID: "intent-greeting-" + suffix,
		StateSchemaVersion:   1,
		Revision:             1,
		State:                communication.V4State{},
		AutomationStatus:     automation,
	}).Error; err != nil {
		t.Fatalf("造 v4 聚合失败: %v", err)
	}
	return profileID, key
}

func seedPendingDispatchLegacyAction(
	t *testing.T,
	s *Store,
	profileID string,
	key ConversationKey,
	suffix string,
	kind CommunicationActionKind,
	status CommunicationActionStatus,
	dependsOn *string,
) {
	t.Helper()
	turnID := "turn-" + suffix
	if err := s.db.Create(&DialogueTurn{
		TurnID:          turnID,
		ProfileID:       profileID,
		ConversationRef: key.ConversationRef,
		InputDigest:     "digest-" + suffix,
		Status:          DialogueTurnAdviceReady,
	}).Error; err != nil {
		t.Fatalf("造对话轮失败: %v", err)
	}
	if err := s.db.Create(&CommunicationAction{
		ActionID:          "action-" + suffix,
		TurnID:            turnID,
		Kind:              kind,
		Text:              "合成话术",
		ContentHash:       "hash-" + suffix,
		DependsOnActionID: dependsOn,
		Status:            status,
		PlannedAt:         pendingDispatchPlannedAt,
	}).Error; err != nil {
		t.Fatalf("造对话轨动作失败: %v", err)
	}
}

func seedPendingDispatchV4Action(
	t *testing.T,
	s *Store,
	profileID string,
	suffix string,
	effect CommunicationV4EventEffectKind,
	status CommunicationV4EventActionStatus,
	dependsOn *string,
) {
	t.Helper()
	if err := s.db.Create(&CommunicationV4EventAction{
		ActionID:          "v4-action-" + suffix,
		ProfileID:         profileID,
		SourceInputKind:   CommunicationV4InputDialogueTurn,
		SourceInputKey:    "turn-" + suffix,
		SourceOrdinal:     0,
		SemanticActionKey: "semantic-" + suffix,
		EffectKind:        effect,
		Status:            status,
		DependsOnActionID: dependsOn,
		PlannedAt:         pendingDispatchPlannedAt,
	}).Error; err != nil {
		t.Fatalf("造 v4 事件动作失败: %v", err)
	}
}

// TestConversationHasPlannedVisibleDispatchLegacyTrack 锁住对话轨(legacy
// communication_actions)的判据:2026-08-04 真机的 42 次 pageAbsent 全部出自
// 这一轨。
func TestConversationHasPlannedVisibleDispatchLegacyTrack(t *testing.T) {
	parent := "action-parent"
	tests := []struct {
		name       string
		kind       CommunicationActionKind
		status     CommunicationActionStatus
		dependsOn  *string
		automation ProfileCommunicationAutomationStatus
		want       bool
	}{
		{
			name:       "链首 planned 回复是读取理由",
			kind:       CommunicationActionReplyText,
			status:     CommunicationActionPlanned,
			automation: ProfileCommunicationAutomationActive,
			want:       true,
		},
		{
			name:       "链首 planned 邀微信同样需要页面",
			kind:       CommunicationActionInviteWechat,
			status:     CommunicationActionPlanned,
			automation: ProfileCommunicationAutomationActive,
			want:       true,
		},
		{
			name:       "链内后续项搭链首打开的页面,不自己开",
			kind:       CommunicationActionReplyText,
			status:     CommunicationActionPlanned,
			dependsOn:  &parent,
			automation: ProfileCommunicationAutomationActive,
			want:       false,
		},
		{
			name:       "被冻结档案的 planned 残留永不被派发遭遇",
			kind:       CommunicationActionReplyText,
			status:     CommunicationActionPlanned,
			automation: ProfileCommunicationAutomationManualRequired,
			want:       false,
		},
		{
			name:       "已发出的动作不再需要页面",
			kind:       CommunicationActionReplyText,
			status:     CommunicationActionSent,
			automation: ProfileCommunicationAutomationActive,
			want:       false,
		},
		{
			name:       "留档的重试代不是待派发行",
			kind:       CommunicationActionReplyText,
			status:     CommunicationActionRetried,
			automation: ProfileCommunicationAutomationActive,
			want:       false,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := openTest(t)
			suffix := "legacy-" + string(rune('a'+index))
			profileID, key := seedPendingDispatchProfile(t, s, suffix, test.automation)
			seedPendingDispatchLegacyAction(
				t, s, profileID, key, suffix, test.kind, test.status, test.dependsOn,
			)
			pending, err := s.ConversationHasPlannedVisibleDispatch(key)
			if err != nil {
				t.Fatalf("查询待派发可见动作失败: %v", err)
			}
			if pending != test.want {
				t.Fatalf("待派发判据 = %v, want %v", pending, test.want)
			}
		})
	}
}

// TestConversationHasPlannedVisibleDispatchV4Track 锁住事件动作轨的判据,
// 重点是 notification(运营通知 webhook)不经页面、不构成读取理由。
func TestConversationHasPlannedVisibleDispatchV4Track(t *testing.T) {
	parent := "v4-action-parent"
	tests := []struct {
		name      string
		effect    CommunicationV4EventEffectKind
		status    CommunicationV4EventActionStatus
		dependsOn *string
		want      bool
	}{
		{
			name:   "链首 planned 回复是读取理由",
			effect: CommunicationV4EventEffectReplyText,
			status: CommunicationV4EventActionPlanned,
			want:   true,
		},
		{
			name:   "运营通知不经页面",
			effect: CommunicationV4EventEffectNotification,
			status: CommunicationV4EventActionPlanned,
			want:   false,
		},
		{
			name:      "等父项正证的链内项不自己开页面",
			effect:    CommunicationV4EventEffectInviteWechat,
			status:    CommunicationV4EventActionPlanned,
			dependsOn: &parent,
			want:      false,
		},
		{
			name:   "deferred 不是待派发行",
			effect: CommunicationV4EventEffectReplyText,
			status: CommunicationV4EventActionDeferred,
			want:   false,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := openTest(t)
			suffix := "v4-" + string(rune('a'+index))
			profileID, key := seedPendingDispatchProfile(
				t, s, suffix, ProfileCommunicationAutomationActive,
			)
			seedPendingDispatchV4Action(
				t, s, profileID, suffix, test.effect, test.status, test.dependsOn,
			)
			pending, err := s.ConversationHasPlannedVisibleDispatch(key)
			if err != nil {
				t.Fatalf("查询待派发可见动作失败: %v", err)
			}
			if pending != test.want {
				t.Fatalf("待派发判据 = %v, want %v", pending, test.want)
			}
		})
	}
}

// TestConversationHasPlannedVisibleDispatchScope 锁住作用域:判据必须按
// platform+accountRef+conversationRef 三项定位,不得让别的会话或别的账号
// 的待派发动作把当前会话拖成"该读"。
func TestConversationHasPlannedVisibleDispatchScope(t *testing.T) {
	s := openTest(t)
	profileID, key := seedPendingDispatchProfile(
		t, s, "scope-owner", ProfileCommunicationAutomationActive,
	)
	seedPendingDispatchLegacyAction(
		t, s, profileID, key, "scope-owner",
		CommunicationActionReplyText, CommunicationActionPlanned, nil,
	)
	_, otherKey := seedPendingDispatchProfile(
		t, s, "scope-other", ProfileCommunicationAutomationActive,
	)

	pending, err := s.ConversationHasPlannedVisibleDispatch(otherKey)
	if err != nil {
		t.Fatalf("查询待派发可见动作失败: %v", err)
	}
	if pending {
		t.Fatal("别的会话的待派发动作不得让本会话判脏")
	}

	crossAccount := key
	crossAccount.AccountRef = "a-pending-dispatch-other"
	pending, err = s.ConversationHasPlannedVisibleDispatch(crossAccount)
	if err != nil {
		t.Fatalf("查询待派发可见动作失败: %v", err)
	}
	if pending {
		t.Fatal("同名会话在别的账号下不得命中")
	}
}

func TestConversationHasPlannedVisibleDispatchRejectsEmptyKey(t *testing.T) {
	s := openTest(t)
	for _, key := range []ConversationKey{
		{AccountRef: pendingDispatchAccount, ConversationRef: "c-1"},
		{Platform: pendingDispatchPlatform, ConversationRef: "c-1"},
		{Platform: pendingDispatchPlatform, AccountRef: pendingDispatchAccount},
	} {
		if _, err := s.ConversationHasPlannedVisibleDispatch(key); !errors.Is(
			err, ErrPendingVisibleDispatchInvalid,
		) {
			t.Fatalf("会话键缺项必须报错: key=%+v err=%v", key, err)
		}
	}
}
