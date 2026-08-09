package store

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// self 消息的时间维度只收平台证据(result/验证读带回的 tsApprox),不收
// 观察时刻:平台不给时间时保持未知,给了时如实收编,重复收编幂等返回
// 首次事实。2026-08-06 tsApprox 立案后本测试从"恒 nil"扩展为两向。
func TestAppendOutboundMessageProjectsOnlyPlatformEvidenceTime(t *testing.T) {
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	at := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)

	appendOnce := func(intent *EffectIntent, text, contentHash string, platformTsMs *int64) *Message {
		t.Helper()
		var message *Message
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			var err error
			message, err = appendOutboundMessageTx(tx, intent, text, contentHash, platformTsMs, "", at)
			return err
		}); err != nil {
			t.Fatalf("appendOutboundMessageTx: %v", err)
		}
		return message
	}

	// 无平台证据:时间保持未知,不得投影任何本机时刻。
	bare := testOutboundIntent(key, "intent-no-platform-time")
	first := appendOnce(&bare, "self message", "self-message-hash", nil)
	if first.TsApproxMs != nil || first.Direction != "out" || first.Kind != "text" ||
		first.Origin != "self" || first.Text == nil || *first.Text != "self message" ||
		first.ContentHash != "self-message-hash" || first.OutboundIntentID == nil ||
		*first.OutboundIntentID != bare.IntentID || first.Seq != 2 {
		t.Fatalf("self 消息字段或时间投影错误: %+v", first)
	}

	// 有平台证据:如实收编;重复收编携带不同值也必须幂等返回首次事实。
	platformTs := at.Add(3 * time.Second).UnixMilli()
	evidenced := testOutboundIntent(key, "intent-platform-time")
	withTs := appendOnce(&evidenced, "timed message", "timed-message-hash", &platformTs)
	if withTs.TsApproxMs == nil || *withTs.TsApproxMs != platformTs || withTs.Seq != 3 {
		t.Fatalf("平台时间证据未如实收编: %+v", withTs)
	}
	laterTs := at.Add(40 * time.Minute).UnixMilli()
	replayed := appendOnce(&evidenced, "timed message", "timed-message-hash", &laterTs)
	if replayed.Seq != withTs.Seq || replayed.TsApproxMs == nil || *replayed.TsApproxMs != platformTs {
		t.Fatalf("重复收编必须返回同一事实: first=%+v replayed=%+v", withTs, replayed)
	}
	var count int64
	if err := s.db.Model(&Message{}).Where("outbound_intent_id = ?", evidenced.IntentID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("重复收编不得增生消息: count=%d err=%v", count, err)
	}
	conversation, err := s.ConversationByKey(key)
	if err != nil || conversation.LastMessageSeq != withTs.Seq || conversation.LastMessageDirection != "out" ||
		conversation.LastMessageKind != "text" || conversation.LastMessagePreview != "timed message" {
		t.Fatalf("时间收编不得改变会话摘要: conversation=%+v err=%v", conversation, err)
	}
}

// 卡片 self 行与文本同一纪律:只收平台证据,无证据保持未知,重复幂等。
func TestApplyCardResultProjectsOnlyPlatformEvidenceTime(t *testing.T) {
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	at := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	contentHash := "c0ffee" + strings.Repeat("0", 58)
	sourceKey := "ab" + strings.Repeat("1", 62)
	intent := EffectIntent{
		IntentID: "intent-card-platform-time", Platform: key.Platform, AccountRef: key.AccountRef,
		Primitive: primitiveChatSendWechatInvite, TargetRef: key.ConversationRef,
		SendFingerprint: contentHash,
	}
	platformTs := at.Add(2 * time.Second).UnixMilli()
	applyOnce := func(platformTsMs *int64) *Message {
		t.Helper()
		var message *Message
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			var err error
			message, err = applyCardResultTx(tx, &intent, CardResultMutation{
				ConversationRef: key.ConversationRef,
				CardType:        "wechatExchange", CardState: "pending",
				ContentHash: contentHash, SourceKey: sourceKey,
				PlatformTsMs: platformTsMs,
			}, at)
			return err
		}); err != nil {
			t.Fatalf("applyCardResultTx: %v", err)
		}
		return message
	}

	first := applyOnce(&platformTs)
	if first.TsApproxMs == nil || *first.TsApproxMs != platformTs ||
		first.Kind != "card" || first.Origin != "self" {
		t.Fatalf("卡片平台时间证据未如实收编: %+v", first)
	}
	replayed := applyOnce(nil)
	if replayed.Seq != first.Seq || replayed.TsApproxMs == nil || *replayed.TsApproxMs != platformTs {
		t.Fatalf("卡片重复收编必须返回同一事实: first=%+v replayed=%+v", first, replayed)
	}
	var count int64
	if err := s.db.Model(&Message{}).Where("outbound_intent_id = ?", intent.IntentID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("卡片重复收编不得增生消息: count=%d err=%v", count, err)
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
