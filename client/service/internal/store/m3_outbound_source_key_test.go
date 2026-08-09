package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// S1 回执补 ID:文本发送回执带 sourceKey 落账。巡检可能已把同一条服务端
// 消息先收进账本(external 观察行),落账必须认领那一行而不是追加,否则撞
// (platform,account,conversation,source_key) 唯一索引;同 key 异语义维持报错。

func appendOutboundWithKey(
	s *Store, intent *EffectIntent, text, contentHash, sourceKey string, at time.Time,
) (*Message, error) {
	var message *Message
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		message, err = appendOutboundMessageTx(tx, intent, text, contentHash, nil, sourceKey, at)
		return err
	})
	return message, err
}

func seedObservedRow(t *testing.T, s *Store, key ConversationKey, seq int64, direction, text, contentHash, sourceKey string) {
	t.Helper()
	textCopy := text
	keyCopy := sourceKey
	if err := s.db.Create(&Message{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		Seq: seq, Direction: direction, Kind: "text", ContentHash: contentHash, Text: &textCopy,
		Origin: "external", SourceKey: &keyCopy,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestAppendOutboundClaimsObservedRowBySourceKey(t *testing.T) {
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	sourceKey := strings.Repeat("a", 64)
	seedObservedRow(t, s, key, 2, "out", "本次正文", "hash-claim", sourceKey)

	intent := testOutboundIntent(key, "intent-claim")
	at := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	message, err := appendOutboundWithKey(s, &intent, "本次正文", "hash-claim", sourceKey, at)
	if err != nil {
		t.Fatalf("认领已观察行不应报错: %v", err)
	}
	if message.Seq != 2 || message.Origin != "self" ||
		message.OutboundIntentID == nil || *message.OutboundIntentID != intent.IntentID ||
		message.SourceKey == nil || *message.SourceKey != sourceKey {
		t.Fatalf("必须认领 seq=2 观察行并绑定 intent: %+v", message)
	}
	facts := physicalMessageFacts(t, s, key)
	if len(facts) != 2 {
		t.Fatalf("认领不得追加第二条同身份事实: %d 行", len(facts))
	}

	// 同 intent 重放:byIntent 幂等命中同一行,行数不变。
	replay, err := appendOutboundWithKey(s, &intent, "本次正文", "hash-claim", sourceKey, at.Add(time.Minute))
	if err != nil || replay.Seq != 2 {
		t.Fatalf("重放必须幂等命中认领行: message=%+v err=%v", replay, err)
	}
	if facts = physicalMessageFacts(t, s, key); len(facts) != 2 {
		t.Fatalf("重放后行数必须不变: %d 行", len(facts))
	}
}

func TestAppendOutboundWritesSourceKeyOnFreshRow(t *testing.T) {
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	sourceKey := strings.Repeat("b", 64)
	intent := testOutboundIntent(key, "intent-fresh")
	at := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)

	message, err := appendOutboundWithKey(s, &intent, "全新一条", "hash-fresh", sourceKey, at)
	if err != nil {
		t.Fatalf("无同身份行时应追加: %v", err)
	}
	if message.Seq != 2 || message.Origin != "self" ||
		message.SourceKey == nil || *message.SourceKey != sourceKey {
		t.Fatalf("新行必须携带 sourceKey: %+v", message)
	}

	// 无身份路径(乐观判定/人工裁决)照旧落 NULL,不受影响。
	nullIntent := testOutboundIntent(key, "intent-null")
	nullRow, err := appendOutboundWithKey(s, &nullIntent, "无身份一条", "hash-null", "", at.Add(time.Minute))
	if err != nil || nullRow.SourceKey != nil {
		t.Fatalf("空 sourceKey 必须落 NULL: message=%+v err=%v", nullRow, err)
	}

	// 非法 sourceKey 在 store 层拒绝,不落半行。
	badIntent := testOutboundIntent(key, "intent-bad")
	if _, err := appendOutboundWithKey(s, &badIntent, "x", "hash-bad", "ZZ", at); !errors.Is(err, ErrEffectIntentConflict) {
		t.Fatalf("非法 sourceKey 应拒绝: err=%v", err)
	}
}

func TestAppendOutboundSourceKeySemanticConflictRejects(t *testing.T) {
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	at := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)

	// 同 key 异 hash:同一服务端身份两种正文,维持报错(战役出口阻断项)。
	hashConflictKey := strings.Repeat("c", 64)
	seedObservedRow(t, s, key, 2, "out", "别的正文", "hash-other", hashConflictKey)
	intent := testOutboundIntent(key, "intent-hash-conflict")
	if _, err := appendOutboundWithKey(s, &intent, "本次正文", "hash-mine", hashConflictKey, at); !errors.Is(err, ErrMessageSourceKeyConflict) {
		t.Fatalf("同 key 异 hash 应报 ErrMessageSourceKeyConflict: err=%v", err)
	}

	// 同 key 但方向为入站:候选人消息绝不能被认领为我方发送。
	inboundKey := strings.Repeat("d", 64)
	seedObservedRow(t, s, key, 3, "in", "候选人的话", "hash-inbound", inboundKey)
	inboundIntent := testOutboundIntent(key, "intent-inbound-conflict")
	if _, err := appendOutboundWithKey(s, &inboundIntent, "候选人的话", "hash-inbound", inboundKey, at); !errors.Is(err, ErrMessageSourceKeyConflict) {
		t.Fatalf("入站行不得被认领: err=%v", err)
	}

	// 同 key 已被别的 intent 认领:第二个 intent 不得抢占。
	claimedKey := strings.Repeat("e", 64)
	firstIntent := testOutboundIntent(key, "intent-first-claim")
	if _, err := appendOutboundWithKey(s, &firstIntent, "先到", "hash-claimed", claimedKey, at); err != nil {
		t.Fatal(err)
	}
	secondIntent := testOutboundIntent(key, "intent-second-claim")
	if _, err := appendOutboundWithKey(s, &secondIntent, "先到", "hash-claimed", claimedKey, at.Add(time.Minute)); !errors.Is(err, ErrMessageSourceKeyConflict) {
		t.Fatalf("已被认领的行不得再绑第二个 intent: err=%v", err)
	}
}
