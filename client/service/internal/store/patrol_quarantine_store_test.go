package store

import (
	"errors"
	"testing"
	"time"
)

// 钉住巡检单人隔离标记的存储语义（2026-07-27 甲方裁决）：首次打标返回 true、
// 重复打标幂等且不改写首因、解除后可再次打标、缺行报 ErrConversationNotFound。
func TestQuarantineConversationPatrolLifecycle(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	key := ConversationKey{
		Platform: "zhilian", AccountRef: "acc-q", ConversationRef: "conversation-quarantine",
	}
	if err := s.CreateAccount(&Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	firstAt := time.Date(2026, 7, 27, 21, 0, 0, 0, time.UTC)

	if _, err := s.QuarantineConversationPatrol(key, "sourceIdentityConflict", firstAt); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("缺行必须报 ErrConversationNotFound: %v", err)
	}

	if err := s.CreatePatrolRound(&PatrolRound{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-quarantine-seed",
		Trigger: "seed", Status: "running", StartedAt: firstAt.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("CreatePatrolRound: %v", err)
	}
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-quarantine-seed",
		ObservedAt: firstAt.Add(-time.Hour), Complete: true,
		Entries: []ListIndexEntry{{
			ConversationRef: key.ConversationRef, PlatformUserRef: "peer-quarantine",
			PeerDisplayName: "候选人", LastMessageDirection: "in", LastMessageKind: "text",
		}},
	}); err != nil {
		t.Fatalf("SaveConversationList: %v", err)
	}
	if _, err := s.TrackConversation(key, "test", firstAt.Add(-time.Hour)); err != nil {
		t.Fatalf("TrackConversation: %v", err)
	}
	newly, err := s.QuarantineConversationPatrol(key, "sourceIdentityConflict", firstAt)
	if err != nil || !newly {
		t.Fatalf("首次打标必须返回 true: newly=%v err=%v", newly, err)
	}
	conversation, err := s.ConversationByKey(key)
	if err != nil || conversation == nil ||
		conversation.PatrolQuarantinedAt == nil ||
		!conversation.PatrolQuarantinedAt.Equal(firstAt) ||
		conversation.PatrolQuarantineReason != "sourceIdentityConflict" {
		t.Fatalf("打标未持久化: conversation=%+v err=%v", conversation, err)
	}

	again, err := s.QuarantineConversationPatrol(key, "projectionConflict", firstAt.Add(time.Hour))
	if err != nil || again {
		t.Fatalf("重复打标必须幂等返回 false: again=%v err=%v", again, err)
	}
	conversation, err = s.ConversationByKey(key)
	if err != nil || conversation == nil ||
		!conversation.PatrolQuarantinedAt.Equal(firstAt) ||
		conversation.PatrolQuarantineReason != "sourceIdentityConflict" {
		t.Fatalf("重复打标不得改写首因: conversation=%+v err=%v", conversation, err)
	}

	if _, err := s.QuarantineConversationPatrol(key, "", firstAt); err == nil {
		t.Fatal("空 reason 必须拒绝")
	}

	cleared, err := s.ClearConversationPatrolQuarantine(key, firstAt.Add(2*time.Hour))
	if err != nil || !cleared {
		t.Fatalf("解除隔离失败: cleared=%v err=%v", cleared, err)
	}
	conversation, err = s.ConversationByKey(key)
	if err != nil || conversation == nil ||
		conversation.PatrolQuarantinedAt != nil || conversation.PatrolQuarantineReason != "" {
		t.Fatalf("解除后标记未清空: conversation=%+v err=%v", conversation, err)
	}
	clearedAgain, err := s.ClearConversationPatrolQuarantine(key, firstAt.Add(3*time.Hour))
	if err != nil || clearedAgain {
		t.Fatalf("重复解除必须返回 false: cleared=%v err=%v", clearedAgain, err)
	}

	remarked, err := s.QuarantineConversationPatrol(key, "handManualOnly", firstAt.Add(4*time.Hour))
	if err != nil || !remarked {
		t.Fatalf("解除后必须允许再次打标: remarked=%v err=%v", remarked, err)
	}
}
