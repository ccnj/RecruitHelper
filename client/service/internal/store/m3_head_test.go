package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func seedEffectHeadTarget(t *testing.T, s *Store) ConversationKey {
	t.Helper()
	key := ConversationKey{Platform: "zhilian", AccountRef: "account-head", ConversationRef: "conversation-head"}
	if err := s.CreateAccount(&Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindAccountPrincipal(AccountKey{Platform: key.Platform, AccountRef: key.AccountRef},
		"hand-head", "principal-head", "session-head", "boot-head", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, Complete: true,
		Entries: []ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "candidate-head"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrackConversation(key, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	history := "history"
	if _, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: 0, PlatformUserRef: "candidate-head", Adopt: true,
		NewMessages: []MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: "history-hash", Text: &history, Origin: "external",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

func createHeadIntent(
	t *testing.T, s *Store, key ConversationKey,
	intentID, previousIntentID string, createdAt, now time.Time,
) (*CreateEffectIntentResult, error) {
	t.Helper()
	msgID := "msg-" + intentID
	idemKey := "idem-" + intentID
	deadline := now.Add(time.Hour).UnixMilli()
	return s.CreateEffectIntentAndCmd(CreateEffectIntentRequest{
		Intent: EffectIntent{
			IntentID: intentID, IdemKey: idemKey, Platform: key.Platform, AccountRef: key.AccountRef,
			Primitive: "chat.sendMessage", TargetRef: key.ConversationRef,
			PayloadHash: "payload-" + intentID, GuardsHash: "guards-" + intentID,
			Status: EffectIntentDispatching, DeadlineMs: deadline, SendFingerprint: "fingerprint-" + intentID,
			CreatedAt: createdAt,
		},
		Command: CmdRecord{
			MsgID: msgID, Name: "chat.sendMessage", Class: "effectful", IdemKey: idemKey,
			Domain: key.Platform + ":" + key.AccountRef, Platform: key.Platform, AccountRef: key.AccountRef,
			ExpectedPrincipalFingerprint: "principal-head", IntentID: intentID,
			HandID: "hand-head", Session: "session-head", BootIDAtDispatch: "boot-head",
			Status: CmdQueued, DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		ExpectedTailSeq: 1, PreviousIntentID: previousIntentID, Now: now,
	})
}

func settleHeadIntent(t *testing.T, s *Store, intentID string, at time.Time) {
	t.Helper()
	if err := s.db.Model(&CmdRecord{}).Where("intent_id = ?", intentID).
		Updates(map[string]any{"status": CmdOk, "terminal_at": at}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&EffectIntent{}).Where("intent_id = ?", intentID).
		Updates(map[string]any{"status": EffectIntentOk, "resolved_at": at}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestEffectHeadRejectsStalePredecessorAcrossClockRollback(t *testing.T) {
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	// A 的 wall clock 故意比稍后创建的 B 大十年。任何按 created_at
	// 求 latest 的实现都会在 B 之后错误地重新选回 A。
	if _, err := createHeadIntent(t, s, key, "intent-a", "", now.AddDate(10, 0, 0), now); err != nil {
		t.Fatal(err)
	}
	settleHeadIntent(t, s, "intent-a", now)
	if _, err := createHeadIntent(t, s, key, "intent-b", "intent-a", now.AddDate(-10, 0, 0), now); err != nil {
		t.Fatal(err)
	}
	settleHeadIntent(t, s, "intent-b", now)

	_, err := createHeadIntent(t, s, key, "intent-stale-c", "intent-a", now, now)
	var conflict *EffectIntentCASConflictError
	if !errors.As(err, &conflict) || conflict.Current == nil || conflict.Current.IntentID != "intent-b" {
		t.Fatalf("时钟回拨后 stale A 必须被单调 head 拒绝并报告 B: conflict=%+v err=%v", conflict, err)
	}
	latest, err := s.LatestEffectIntent(key.Platform, key.AccountRef, key.ConversationRef)
	if err != nil || latest == nil || latest.IntentID != "intent-b" {
		t.Fatalf("latest 必须只沿 head 得到 B: latest=%+v err=%v", latest, err)
	}
	var head ConversationEffectHead
	if err := s.db.First(&head, "platform = ? AND account_ref = ? AND conversation_ref = ?",
		key.Platform, key.AccountRef, key.ConversationRef).Error; err != nil || head.Generation != 2 {
		t.Fatalf("head generation 未单调推进: head=%+v err=%v", head, err)
	}
}

func TestLegacyHeadBackfillPersistsAcrossClockMutationVacuumAndRestarts(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "brain.db")
	legacy, err := gorm.Open(sqlite.Open("file:"+dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.AutoMigrate(&EffectIntent{}); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	for _, id := range []string{"legacy-a", "legacy-b"} {
		intent := EffectIntent{
			IntentID: id, IdemKey: "idem-" + id, Platform: "zhilian", AccountRef: "legacy-account",
			Primitive: "chat.sendMessage", TargetRef: "legacy-conversation",
			PayloadHash: "payload-" + id, GuardsHash: "guards-" + id, RootMsgID: "msg-" + id,
			Status: EffectIntentFailed, DeadlineMs: createdAt.Add(time.Hour).UnixMilli(), CreatedAt: createdAt,
		}
		if err := legacy.Create(&intent).Error; err != nil {
			t.Fatal(err)
		}
	}
	legacySQL, _ := legacy.DB()
	_ = legacySQL.Close()

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestEffectIntent("zhilian", "legacy-account", "legacy-conversation")
	if err != nil || latest == nil || latest.IntentID != "legacy-b" {
		t.Fatalf("旧库应按 created_at+intent_id 确定性一次回填: latest=%+v err=%v", latest, err)
	}
	var head ConversationEffectHead
	if err := s.db.First(&head).Error; err != nil || head.Generation != 2 {
		t.Fatalf("旧库 head generation 应等于历史意图数: head=%+v err=%v", head, err)
	}
	// 回填后篡改旧 A 的时间使其远晚于 B，再 VACUUM/多次 Open；已有
	// head 必须保持 B，证明运行期不再重算时间/rowid 顺序。
	if err := s.db.Model(&EffectIntent{}).Where("intent_id = ?", "legacy-a").
		Update("created_at", createdAt.AddDate(50, 0, 0)).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec("VACUUM").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for generation := 0; generation < 2; generation++ {
		reopened, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		latest, err := reopened.LatestEffectIntent("zhilian", "legacy-account", "legacy-conversation")
		if err != nil || latest == nil || latest.IntentID != "legacy-b" {
			_ = reopened.Close()
			t.Fatalf("VACUUM/第 %d 次重启不得重算 head: latest=%+v err=%v", generation+1, latest, err)
		}
		var persisted ConversationEffectHead
		if err := reopened.db.First(&persisted).Error; err != nil || persisted.Generation != 2 {
			_ = reopened.Close()
			t.Fatalf("第 %d 次重启 head 未持久: head=%+v err=%v", generation+1, persisted, err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEffectHeadCorruptionAndGenerationOverflowFailClosed(t *testing.T) {
	t.Run("missing head with existing intents survives restart as corruption", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		key := seedEffectHeadTarget(t, s)
		now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
		if _, err := createHeadIntent(t, s, key, "missing-head-a", "", now, now); err != nil {
			t.Fatal(err)
		}
		settleHeadIntent(t, s, "missing-head-a", now)
		deleted := s.db.Where("platform = ? AND account_ref = ? AND conversation_ref = ?",
			key.Platform, key.AccountRef, key.ConversationRef).Delete(&ConversationEffectHead{})
		if deleted.Error != nil || deleted.RowsAffected != 1 {
			t.Fatalf("删除单个 head 失败: rows=%d err=%v", deleted.RowsAffected, deleted.Error)
		}

		if latest, err := s.LatestEffectIntent(key.Platform, key.AccountRef, key.ConversationRef); !errors.Is(err, ErrEffectIntentHeadCorrupt) || latest != nil {
			t.Fatalf("已有意图却缺 head 的读必须 fail-closed: latest=%+v err=%v", latest, err)
		}
		// 这是原漏洞的精确形状：如果把缺 head 误当成空会话，
		// previous="" 会穿过 CAS 并创建第二条真实副作用意图。
		if _, err := createHeadIntent(t, s, key, "missing-head-b", "", now, now); !errors.Is(err, ErrEffectIntentHeadCorrupt) {
			t.Fatalf("缺 head 时 previous=\"\" 不得创建: %v", err)
		}
		if leaked, err := s.EffectIntentByID("missing-head-b"); err != nil || leaked != nil {
			t.Fatalf("失败事务不得泄漏新意图: leaked=%+v err=%v", leaked, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		reopened, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		if latest, err := reopened.LatestEffectIntent(key.Platform, key.AccountRef, key.ConversationRef); !errors.Is(err, ErrEffectIntentHeadCorrupt) || latest != nil {
			t.Fatalf("重启不得回填丢失 head 并掩盖损坏: latest=%+v err=%v", latest, err)
		}
	})

	t.Run("generation exhaustion rolls back intent and command", func(t *testing.T) {
		s := openTest(t)
		key := seedEffectHeadTarget(t, s)
		now := time.Date(2026, 7, 19, 15, 0, 0, 0, time.UTC)
		if _, err := createHeadIntent(t, s, key, "overflow-a", "", now, now); err != nil {
			t.Fatal(err)
		}
		settleHeadIntent(t, s, "overflow-a", now)
		updated := s.db.Model(&ConversationEffectHead{}).
			Where("platform = ? AND account_ref = ? AND conversation_ref = ?", key.Platform, key.AccountRef, key.ConversationRef).
			UpdateColumn("generation", int64(maxSQLiteEffectHeadGeneration))
		if updated.Error != nil || updated.RowsAffected != 1 {
			t.Fatalf("布置 generation 上限失败: rows=%d err=%v", updated.RowsAffected, updated.Error)
		}
		if _, err := createHeadIntent(t, s, key, "overflow-b", "overflow-a", now, now); !errors.Is(err, ErrEffectIntentHeadCorrupt) {
			t.Fatalf("generation 耗尽必须 fail-closed: %v", err)
		}
		if leaked, err := s.EffectIntentByID("overflow-b"); err != nil || leaked != nil {
			t.Fatalf("溢出事务不得泄漏新意图: leaked=%+v err=%v", leaked, err)
		}
	})
}
