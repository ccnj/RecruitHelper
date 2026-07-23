package store

import (
	"testing"
	"time"

	"recruithelper/contract/gen/go/protocol"
)

type lateGreetingFixture struct {
	ledger          greetingLedgerFixture
	platformUserRef string
	positionRef     string
	intentID        string
}

func seedLateGreetingFixture(t *testing.T, s *Store, suffix string) lateGreetingFixture {
	t.Helper()
	ledger := seedGreetingLedger(t, s, "profile-late-bind-"+suffix)
	now := time.Date(2026, 7, 23, 11, 0, 0, 0, time.UTC)
	intentID := "intent-late-bind-" + suffix
	request := greetingIntentRequest(ledger, intentID, "", now)
	request.Intent.SendFingerprint = sourcingGreetingSendFingerprint("测试招呼")
	args, err := protocol.Encode(protocol.ChatSendGreetingArgs{
		PlatformUserRef: "person-" + ledger.ProfileID,
		PositionRef:     "position-" + ledger.ProfileID,
		Text:            "测试招呼",
	})
	if err != nil {
		t.Fatal(err)
	}
	request.Command.Args = string(args)
	created, err := s.CreateGreetingEffectIntentAndCmd(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MoveEffectToVerification(created.Command.MsgID, "lateBindFixture", now); err != nil {
		t.Fatal(err)
	}
	platformUserRef := "person-" + ledger.ProfileID
	positionRef := "position-" + ledger.ProfileID
	message, err := s.ResolveGreetingVerified(VerifiedGreetingSuccess{
		Ref: created.Command.MsgID, ProfileID: ledger.ProfileID,
		PlatformUserRef: platformUserRef, PositionRef: positionRef,
		Text: "测试招呼", ContentHash: request.Intent.SendFingerprint,
		ResolutionReason: "lateBindFixture", At: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if message != nil {
		t.Fatalf("缺少会话引用的招呼成功不得伪造消息: %+v", message)
	}
	// 真实存量的成功 Cmd 可保持空 side_effect；安全正证来自终局状态、
	// greeted Profile、成功 EffectIntent 与经过契约校验的不可变 args。
	if err := s.db.Model(&CmdRecord{}).Where("msg_id = ?", created.Command.MsgID).
		Update("side_effect", "").Error; err != nil {
		t.Fatal(err)
	}
	return lateGreetingFixture{
		ledger: ledger, platformUserRef: platformUserRef, positionRef: positionRef, intentID: intentID,
	}
}

func saveLateConversation(t *testing.T, s *Store, platform, accountRef, conversationRef, platformUserRef string) {
	t.Helper()
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform: platform, AccountRef: accountRef, ObservedAt: time.Now(), Complete: true,
		Entries: []ListIndexEntry{{
			ConversationRef: conversationRef, PlatformUserRef: platformUserRef,
			LastMessageDirection: "in", LastMessageKind: "card", LastMessagePreview: "已投递在线简历",
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

func lateBindRequest(fixture lateGreetingFixture, conversationRef string) LateBindGreetedConversationsRequest {
	return LateBindGreetedConversationsRequest{
		Platform: fixture.ledger.Platform, AccountRef: fixture.ledger.AccountRef,
		ObservedAt: time.Date(2026, 7, 23, 11, 5, 0, 0, time.UTC),
		Conversations: []LateGreetingConversationObservation{{
			ConversationRef: conversationRef, PlatformUserRef: fixture.platformUserRef,
		}},
	}
}

func TestLateBindGreetedConversationAdoptsAtomicallyAndIsIdempotent(t *testing.T) {
	s := openTest(t)
	fixture := seedLateGreetingFixture(t, s, "success")
	conversationRef := "conversation-late-bind-success"
	saveLateConversation(t, s, fixture.ledger.Platform, fixture.ledger.AccountRef,
		conversationRef, fixture.platformUserRef)

	request := lateBindRequest(fixture, conversationRef)
	bound, err := s.LateBindGreetedConversations(request)
	if err != nil || bound != 1 {
		t.Fatalf("唯一稳定身份未完成晚到回绑: bound=%d err=%v", bound, err)
	}
	profile, _ := s.CandidateProfileByID(fixture.ledger.ProfileID)
	intent, _ := s.EffectIntentByID(fixture.intentID)
	key := ConversationKey{
		Platform: fixture.ledger.Platform, AccountRef: fixture.ledger.AccountRef,
		ConversationRef: conversationRef,
	}
	conversation, _ := s.ConversationByKey(key)
	tracked, _ := s.TrackedIntentByConversation(key)
	messages, _ := s.MessagesForConversation(key)
	if profile == nil || profile.ConversationRef == nil || *profile.ConversationRef != conversationRef ||
		intent == nil || intent.ResultConversationRef == nil || *intent.ResultConversationRef != conversationRef ||
		intent.ResultMessageSeq == nil || *intent.ResultMessageSeq != 1 ||
		conversation == nil || conversation.TrackingState != TrackingAdopted ||
		conversation.AdoptedBoundarySeq != 1 || conversation.LastMessageSeq != 1 || tracked == nil ||
		tracked.Status != TrackingAdopted || tracked.AdoptedAt == nil ||
		tracked.RequestedBy != lateGreetingTrackedRequestedBy || len(messages) != 1 ||
		messages[0].Direction != "out" || messages[0].Kind != "text" || messages[0].Text == nil ||
		*messages[0].Text != "测试招呼" || messages[0].OutboundIntentID == nil ||
		*messages[0].OutboundIntentID != fixture.intentID {
		t.Fatalf("晚到回绑没有原子闭合: profile=%+v intent=%+v conversation=%+v tracked=%+v messages=%+v",
			profile, intent, conversation, tracked, messages)
	}
	audits, err := s.AuditEntries(20)
	if err != nil {
		t.Fatal(err)
	}
	foundAudit := false
	for _, audit := range audits {
		if audit.Category == "conversation_adopted" && audit.ConversationRef == conversationRef &&
			audit.Detail == "adoptedBoundarySeq=1 source=lateGreetingBind" {
			foundAudit = true
		}
	}
	if !foundAudit {
		t.Fatalf("晚到 adopted 未留下脱敏审计: %+v", audits)
	}

	bound, err = s.LateBindGreetedConversations(request)
	if err != nil || bound != 0 {
		t.Fatalf("重复晚到回绑必须幂等 no-op: bound=%d err=%v", bound, err)
	}
	var trackedN, auditN int64
	_ = s.db.Model(&TrackedIntent{}).Where(conversationWhere(key), conversationArgs(key)...).Count(&trackedN).Error
	_ = s.db.Model(&AuditEntry{}).
		Where("category = ? AND platform = ? AND account_ref = ? AND conversation_ref = ?",
			"conversation_adopted", key.Platform, key.AccountRef, key.ConversationRef).Count(&auditN).Error
	if trackedN != 1 || auditN != 1 {
		t.Fatalf("幂等重放产生重复事实: tracked=%d audit=%d", trackedN, auditN)
	}
}

func TestLateBindGreetedConversationRejectsWrongOrAmbiguousBindings(t *testing.T) {
	t.Run("different account", func(t *testing.T) {
		s := openTest(t)
		fixture := seedLateGreetingFixture(t, s, "other-account")
		otherAccount := "account-late-bind-other"
		createM4Account(t, s, fixture.ledger.Platform, otherAccount)
		conversationRef := "conversation-late-bind-other-account"
		saveLateConversation(t, s, fixture.ledger.Platform, otherAccount, conversationRef, fixture.platformUserRef)
		request := lateBindRequest(fixture, conversationRef)
		request.AccountRef = otherAccount
		bound, err := s.LateBindGreetedConversations(request)
		if err != nil || bound != 0 {
			t.Fatalf("不同账号不得匹配: bound=%d err=%v", bound, err)
		}
		profile, _ := s.CandidateProfileByID(fixture.ledger.ProfileID)
		conversation, _ := s.ConversationByKey(ConversationKey{
			Platform: fixture.ledger.Platform, AccountRef: otherAccount, ConversationRef: conversationRef,
		})
		if profile.ConversationRef != nil || conversation.TrackingState != TrackingUntracked {
			t.Fatalf("不同账号匹配改写了事实: profile=%+v conversation=%+v", profile, conversation)
		}
	})

	t.Run("already bound profile", func(t *testing.T) {
		s := openTest(t)
		fixture := seedLateGreetingFixture(t, s, "already-bound")
		firstRef := "conversation-late-bind-first"
		saveLateConversation(t, s, fixture.ledger.Platform, fixture.ledger.AccountRef, firstRef, fixture.platformUserRef)
		if bound, err := s.LateBindGreetedConversations(lateBindRequest(fixture, firstRef)); err != nil || bound != 1 {
			t.Fatalf("首次回绑失败: bound=%d err=%v", bound, err)
		}
		otherRef := "conversation-late-bind-second"
		saveLateConversation(t, s, fixture.ledger.Platform, fixture.ledger.AccountRef, otherRef, fixture.platformUserRef)
		if bound, err := s.LateBindGreetedConversations(lateBindRequest(fixture, otherRef)); err != nil || bound != 0 {
			t.Fatalf("已有绑定不得改绑: bound=%d err=%v", bound, err)
		}
		profile, _ := s.CandidateProfileByID(fixture.ledger.ProfileID)
		other, _ := s.ConversationByKey(ConversationKey{
			Platform: fixture.ledger.Platform, AccountRef: fixture.ledger.AccountRef, ConversationRef: otherRef,
		})
		if profile.ConversationRef == nil || *profile.ConversationRef != firstRef || other.TrackingState != TrackingUntracked {
			t.Fatalf("已有绑定被覆盖: profile=%+v other=%+v", profile, other)
		}
	})

	t.Run("conversation peer conflict", func(t *testing.T) {
		s := openTest(t)
		fixture := seedLateGreetingFixture(t, s, "peer-conflict")
		conversationRef := "conversation-late-bind-peer-conflict"
		saveLateConversation(t, s, fixture.ledger.Platform, fixture.ledger.AccountRef,
			conversationRef, "another-platform-user")
		bound, err := s.LateBindGreetedConversations(lateBindRequest(fixture, conversationRef))
		if err != nil || bound != 0 {
			t.Fatalf("会话身份冲突不得回绑: bound=%d err=%v", bound, err)
		}
		profile, _ := s.CandidateProfileByID(fixture.ledger.ProfileID)
		conversation, _ := s.ConversationByKey(ConversationKey{
			Platform: fixture.ledger.Platform, AccountRef: fixture.ledger.AccountRef,
			ConversationRef: conversationRef,
		})
		if profile.ConversationRef != nil || conversation.PlatformUserRef != "another-platform-user" ||
			conversation.TrackingState != TrackingUntracked {
			t.Fatalf("身份冲突改写了事实: profile=%+v conversation=%+v", profile, conversation)
		}
	})

	t.Run("greeting intent state anomaly", func(t *testing.T) {
		s := openTest(t)
		fixture := seedLateGreetingFixture(t, s, "intent-state")
		conversationRef := "conversation-late-bind-intent-state"
		saveLateConversation(t, s, fixture.ledger.Platform, fixture.ledger.AccountRef,
			conversationRef, fixture.platformUserRef)
		if err := s.db.Model(&EffectIntent{}).Where("intent_id = ?", fixture.intentID).
			Update("status", EffectIntentFailed).Error; err != nil {
			t.Fatal(err)
		}
		bound, err := s.LateBindGreetedConversations(lateBindRequest(fixture, conversationRef))
		if err != nil || bound != 0 {
			t.Fatalf("异常招呼状态不得回绑: bound=%d err=%v", bound, err)
		}
		profile, _ := s.CandidateProfileByID(fixture.ledger.ProfileID)
		conversation, _ := s.ConversationByKey(ConversationKey{
			Platform: fixture.ledger.Platform, AccountRef: fixture.ledger.AccountRef,
			ConversationRef: conversationRef,
		})
		if profile.ConversationRef != nil || conversation.TrackingState != TrackingUntracked ||
			conversation.LastMessageSeq != 0 {
			t.Fatalf("异常招呼状态改写了事实: profile=%+v conversation=%+v", profile, conversation)
		}
	})

	t.Run("same peer in multiple conversations", func(t *testing.T) {
		s := openTest(t)
		fixture := seedLateGreetingFixture(t, s, "ambiguous-conversation")
		firstRef, secondRef := "conversation-late-bind-ambiguous-a", "conversation-late-bind-ambiguous-b"
		saveLateConversation(t, s, fixture.ledger.Platform, fixture.ledger.AccountRef,
			firstRef, fixture.platformUserRef)
		saveLateConversation(t, s, fixture.ledger.Platform, fixture.ledger.AccountRef,
			secondRef, fixture.platformUserRef)
		request := lateBindRequest(fixture, firstRef)
		request.Conversations = append(request.Conversations, LateGreetingConversationObservation{
			ConversationRef: secondRef, PlatformUserRef: fixture.platformUserRef,
		})
		bound, err := s.LateBindGreetedConversations(request)
		if err != nil || bound != 0 {
			t.Fatalf("同一身份多个会话不得猜测: bound=%d err=%v", bound, err)
		}
		profile, _ := s.CandidateProfileByID(fixture.ledger.ProfileID)
		if profile.ConversationRef != nil {
			t.Fatalf("多义会话改写了档案: %+v", profile)
		}
	})
}
