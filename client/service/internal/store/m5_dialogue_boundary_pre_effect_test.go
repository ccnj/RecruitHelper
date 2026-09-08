package store

import (
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

// 2026-09-08 补齐两个漏改角落(2026-08-02 裁决:什么都没发的轮遇边界失配一律
// 作废、不冻结候选人):简历业务事件分类与崩溃恢复此前在 binding 失效时直接
// 标 inputBoundaryChanged 并冻结候选人。

func TestResumeBusinessClassificationSupersedesTurnWhenBindingMoved(t *testing.T) {
	s := openTest(t)
	fixture := seedDialogueStoreFixture(t, s, "profile-dialogue-resume-boundary", "card")
	// 夹具只造裸 card 行,补上简历卡类型使其成为简历业务事件轮。
	if err := s.db.Model(&Message{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
			fixture.Platform, fixture.AccountRef, fixture.ConversationRef, int64(2),
		).
		Updates(map[string]any{"card_type": "resumeAttachment", "card_state": "unknown"}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.FirstMessage.CardType = "resumeAttachment"
	fixture.FirstMessage.CardState = "unknown"
	req := dialogueTurnRequest(fixture, "turn-resume-boundary", "digest-resume-boundary")
	frozen, err := s.FreezeDialogueTurn(req)
	if err != nil || !frozen.Created {
		t.Fatalf("冻结简历轮失败: result=%+v err=%v", frozen, err)
	}
	// 世界变了:档案已被绑到另一个会话,旧轮的 binding 不再成立。
	if err := s.db.Model(&CandidateProfile{}).
		Where("profile_id = ?", fixture.ProfileID).
		Update("conversation_ref", "conversation-elsewhere").Error; err != nil {
		t.Fatal(err)
	}
	classified, err := s.ApplyResumeBusinessClassification(frozen.Turn.TurnID, time.Now().UTC())
	if err != nil || classified == nil || classified.Status != DialogueTurnSuperseded ||
		classified.FailureReason != dialogueTurnBoundarySuperseded {
		t.Fatalf("binding 失效的简历轮应作废而非转人工: turn=%+v err=%v", classified, err)
	}
	assertTrialParkedWithoutFreeze(t, s)
	var actions int64
	if err := s.db.Model(&CommunicationAction{}).Count(&actions).Error; err != nil || actions != 0 {
		t.Fatalf("作废不得造动作: count=%d err=%v", actions, err)
	}
}

func TestInterruptedIntentRecoverySupersedesTurnWhenBindingMoved(t *testing.T) {
	s := openTest(t)
	fixture, turn := seedFrozenDialogueTurn(t, s, "profile-dialogue-recover-boundary")
	if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: "invocation-recover-boundary", TurnID: turn.TurnID, Purpose: m5ai.PurposeIntent,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-recover-boundary",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).
		Where("profile_id = ?", fixture.ProfileID).
		Update("conversation_ref", "conversation-elsewhere").Error; err != nil {
		t.Fatal(err)
	}
	recovered, err := s.RecoverInterruptedAIInvocations(time.Now().UTC().Truncate(time.Millisecond))
	if err != nil || recovered != 1 {
		t.Fatalf("binding 失效的 intent 遗留恢复失败: recovered=%d err=%v", recovered, err)
	}
	stored, _ := s.DialogueTurnByID(turn.TurnID)
	if stored == nil || stored.Status != DialogueTurnSuperseded ||
		stored.FailureReason != dialogueTurnBoundarySuperseded {
		t.Fatalf("binding 失效的中断轮应作废而非转人工: %+v", stored)
	}
	invocations, _ := s.AIInvocationsForTurn(turn.TurnID)
	if len(invocations) != 1 || invocations[0].FinishedAt == nil ||
		invocations[0].ErrorClass != "processInterrupted" {
		t.Fatalf("中断 invocation 未终局化: %+v", invocations)
	}
	assertTrialParkedWithoutFreeze(t, s)
}
