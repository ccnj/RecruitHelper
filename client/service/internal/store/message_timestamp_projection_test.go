package store

import (
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestAppendOutboundMessageDoesNotProjectObservationTime(t *testing.T) {
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	at := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	intent := testOutboundIntent(key, "intent-observation-time")
	const text = "self message"
	const contentHash = "self-message-hash"

	appendOnce := func(observedAtMs int64) *Message {
		t.Helper()
		var message *Message
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			var err error
			message, err = appendOutboundMessageTx(tx, &intent, text, contentHash, observedAtMs, at)
			return err
		}); err != nil {
			t.Fatalf("appendOutboundMessageTx: %v", err)
		}
		return message
	}

	first := appendOnce(at.Add(20 * time.Minute).UnixMilli())
	if first.TsApproxMs != nil || first.Direction != "out" || first.Kind != "text" ||
		first.Origin != "self" || first.Text == nil || *first.Text != text ||
		first.ContentHash != contentHash || first.OutboundIntentID == nil ||
		*first.OutboundIntentID != intent.IntentID || first.Seq != 2 {
		t.Fatalf("self 消息字段或时间投影错误: %+v", first)
	}

	second := appendOnce(at.Add(40 * time.Minute).UnixMilli())
	if second.Platform != first.Platform || second.AccountRef != first.AccountRef ||
		second.ConversationRef != first.ConversationRef || second.Seq != first.Seq ||
		second.TsApproxMs != nil {
		t.Fatalf("重复收编必须返回同一无平台时间事实: first=%+v second=%+v", first, second)
	}
	var count int64
	if err := s.db.Model(&Message{}).Where("outbound_intent_id = ?", intent.IntentID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("重复收编不得增生消息: count=%d err=%v", count, err)
	}
	conversation, err := s.ConversationByKey(key)
	if err != nil || conversation.LastMessageSeq != first.Seq || conversation.LastMessageDirection != "out" ||
		conversation.LastMessageKind != "text" || conversation.LastMessagePreview != text {
		t.Fatalf("self 消息时间修复不得改变会话摘要: conversation=%+v err=%v", conversation, err)
	}
}

func TestApplyGreetingResultDoesNotProjectObservationTime(t *testing.T) {
	s := openTest(t)
	fixture := seedGreetingLedger(t, s, "profile-observation-time")
	at := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	req := greetingIntentRequest(fixture, "intent-greeting-observation-time", "", at)
	created, err := s.CreateGreetingEffectIntentAndCmd(req)
	if err != nil {
		t.Fatal(err)
	}
	mutation := GreetingResultMutation{
		PlatformUserRef: "person-" + fixture.ProfileID,
		PositionRef:     "position-" + fixture.ProfileID,
		ConversationRef: "conversation-observation-time",
		Text:            "你好",
		ContentHash:     req.Intent.SendFingerprint,
		ObservedAtMs:    at.Add(25 * time.Minute).UnixMilli(),
	}

	applyOnce := func() *Message {
		t.Helper()
		intent := created.Intent
		var message *Message
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			var err error
			message, err = applyGreetingResultTx(tx, &intent, mutation, at)
			return err
		}); err != nil {
			t.Fatalf("applyGreetingResultTx: %v", err)
		}
		return message
	}

	first := applyOnce()
	if first.TsApproxMs != nil || first.Direction != "out" || first.Kind != "text" ||
		first.Origin != "self" || first.Text == nil || *first.Text != mutation.Text ||
		first.ContentHash != mutation.ContentHash || first.OutboundIntentID == nil ||
		*first.OutboundIntentID != created.Intent.IntentID || first.Seq != 1 {
		t.Fatalf("招呼 self 消息字段或时间投影错误: %+v", first)
	}

	second := applyOnce()
	if second.Platform != first.Platform || second.AccountRef != first.AccountRef ||
		second.ConversationRef != first.ConversationRef || second.Seq != first.Seq ||
		second.TsApproxMs != nil {
		t.Fatalf("招呼重复结果必须返回同一无平台时间事实: first=%+v second=%+v", first, second)
	}
	var count int64
	if err := s.db.Model(&Message{}).Where("outbound_intent_id = ?", created.Intent.IntentID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("招呼重复结果不得增生消息: count=%d err=%v", count, err)
	}
	profile, err := s.CandidateProfileByID(fixture.ProfileID)
	if err != nil || profile.MainStatus != CandidateProfileGreeted || profile.ConversationRef == nil ||
		*profile.ConversationRef != mutation.ConversationRef || profile.SuccessfulGreetingIntentID == nil ||
		*profile.SuccessfulGreetingIntentID != created.Intent.IntentID || profile.GreetedAt == nil ||
		!profile.GreetedAt.Equal(at) {
		t.Fatalf("招呼时间修复不得改变档案跃迁: profile=%+v err=%v", profile, err)
	}
	key := ConversationKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef, ConversationRef: mutation.ConversationRef,
	}
	conversation, err := s.ConversationByKey(key)
	if err != nil || conversation.TrackingState != TrackingAdopted || conversation.LastMessageSeq != 1 ||
		conversation.LastMessageDirection != "out" || conversation.LastMessageKind != "text" ||
		conversation.LastMessagePreview != mutation.Text {
		t.Fatalf("招呼时间修复不得改变会话建联: conversation=%+v err=%v", conversation, err)
	}
	var trackedCount int64
	if err := s.db.Model(&TrackedIntent{}).Where(conversationWhere(key), conversationArgs(key)...).
		Count(&trackedCount).Error; err != nil || trackedCount != 1 {
		t.Fatalf("招呼重复结果不得增生 tracked 行: count=%d err=%v", trackedCount, err)
	}
}
