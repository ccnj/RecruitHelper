package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// S2 审查回归:身份回配的 CAS 冲突分支与撤回行毒键预查。对齐引擎只看活动
// 账本,撤回行占用的键必须在落库层升为身份冲突哨兵(隔离+人工),不得放行
// 到唯一索引变成每轮静默失败。

func seedNullSelfRow(t *testing.T, s *Store, key ConversationKey, seq int64, text string) {
	t.Helper()
	textCopy := text
	if err := s.db.Create(&Message{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		Seq: seq, Direction: "out", Kind: "text", ContentHash: "hash-" + text, Text: &textCopy,
		Origin: "self",
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestApplyConversationChangesReclaimCASAndPoisonedKey(t *testing.T) {
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	syncedAt := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	// 正常回配:NULL 行拿到身份。
	seedNullSelfRow(t, s, key, 2, "第一条")
	goodKey := strings.Repeat("a", 64)
	if _, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: 1,
		SourceKeyReclaims: []SourceKeyReclaim{{Seq: 2, SourceKey: goodKey}},
		SyncedAt:          syncedAt,
	}); err != nil {
		t.Fatalf("正常回配失败: %v", err)
	}
	var row Message
	if err := s.db.First(&row, "seq = ? AND conversation_ref = ?", 2, key.ConversationRef).Error; err != nil ||
		row.SourceKey == nil || *row.SourceKey != goodKey {
		t.Fatalf("回配未落库: row=%+v err=%v", row, err)
	}

	// CAS 冲突:目标行已被回配(非 NULL)→ 整轮版本冲突失败,不静默改判。
	if _, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: 1,
		SourceKeyReclaims: []SourceKeyReclaim{{Seq: 2, SourceKey: strings.Repeat("b", 64)}},
		SyncedAt:          syncedAt,
	}); !errors.Is(err, ErrConversationVersionConflict) {
		t.Fatalf("已回配行的二次回配应报版本冲突: err=%v", err)
	}

	// 撤回行毒键:撤回行持有 K,回配/新增同 K 必须升为身份冲突哨兵。
	poisoned := strings.Repeat("c", 64)
	retractedAt := syncedAt.Add(time.Minute)
	retractedText := "被撤回的行"
	if err := s.db.Create(&Message{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		Seq: 3, Direction: "out", Kind: "text", ContentHash: "hash-retracted", Text: &retractedText,
		Origin: "self", SourceKey: &poisoned, RetractedAt: &retractedAt, RetractionReason: "test",
	}).Error; err != nil {
		t.Fatal(err)
	}
	seedNullSelfRow(t, s, key, 4, "等回配")
	if _, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: 1,
		SourceKeyReclaims: []SourceKeyReclaim{{Seq: 4, SourceKey: poisoned}},
		SyncedAt:          syncedAt,
	}); !errors.Is(err, ErrMessageSourceKeyConflict) {
		t.Fatalf("毒键回配应升为身份冲突哨兵: err=%v", err)
	}
	draftText := "新消息"
	if _, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: 1,
		NewMessages: []MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: "hash-new", Text: &draftText,
			Origin: "external", SourceKey: &poisoned,
		}},
		SyncedAt: syncedAt,
	}); !errors.Is(err, ErrMessageSourceKeyConflict) {
		t.Fatalf("毒键新增应升为身份冲突哨兵: err=%v", err)
	}
	// 失败必须无残留:等回配行仍 NULL、无新行。
	var after []Message
	if err := s.db.Where(conversationWhere(key), conversationArgs(key)...).Order("seq").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if len(after) != 4 || after[3].SourceKey != nil {
		t.Fatalf("失败事务不得留残留: %+v", after)
	}
}
