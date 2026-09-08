package store

import (
	"errors"
	"testing"
	"time"
)

// 2026-09-08 甲方裁决(规格 v4 §一"旧轮失效"):已发前缀不再是承重墙。轮内
// 全部 intent 都已终局(这里是 ok 已发 + 干净失败重铸的 planned 重试代)时,
// 候选人新输入到达即作废旧轮:已发行零触碰、未发 planned 作废、轮收
// completed、聚合保持 active、新轮照常开。此前该形态返回 ErrDialogueTurnState,
// 候选人以 inputBoundaryChanged 整个冻结等人。
func TestFreezeCommunicationV4TurnSupersedesSentPrefixTurnOnNewInput(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-gate-sent-prefix")
	text := "第一轮入站"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-gate-sent-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	sentIntentID := "intent-v4-gate-sent-ok"
	failedIntentID := "intent-v4-gate-sent-failed"
	for _, intent := range []EffectIntent{
		{
			IntentID: sentIntentID, IdemKey: "ik1:test:gate-sent-ok",
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			Primitive: "chat.sendMessage", TargetRef: fixture.ConversationRef,
			PayloadHash: "payload-1", GuardsHash: "guards", RootMsgID: "root-gate-sent-ok",
			Status: EffectIntentOk, DeadlineMs: 1, CreatedAt: now, UpdatedAt: now,
		},
		{
			IntentID: failedIntentID, IdemKey: "ik1:test:gate-sent-failed",
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			Primitive: "chat.sendMessage", TargetRef: fixture.ConversationRef,
			PayloadHash: "payload-2", GuardsHash: "guards", RootMsgID: "root-gate-sent-failed",
			Status: EffectIntentFailed, DeadlineMs: 1, CreatedAt: now, UpdatedAt: now,
		},
	} {
		if err := s.db.Create(&intent).Error; err != nil {
			t.Fatal(err)
		}
	}
	firstID := "action-v4-gate-sent-1"
	secondID := "action-v4-gate-sent-2"
	retryID := secondID + "|try2"
	sentAt := now
	for _, action := range []CommunicationAction{
		{
			ActionID: firstID, TurnID: frozen.Turn.TurnID,
			Kind: CommunicationActionReplyText, Text: "已发出的第一条",
			ContentHash: "hash-gate-sent-1", Status: CommunicationActionSent,
			EffectIntentID: &sentIntentID, SentAt: &sentAt,
			PlannedAt: now, CreatedAt: now, UpdatedAt: now,
		},
		{
			ActionID: secondID, TurnID: frozen.Turn.TurnID,
			Kind: CommunicationActionReplyText, Text: "干净失败的第二条",
			ContentHash: "hash-gate-sent-2", Status: CommunicationActionRetried,
			EffectIntentID: &failedIntentID, FailureReason: "effectFailed",
			DependsOnActionID: &firstID,
			PlannedAt:         now, CreatedAt: now, UpdatedAt: now,
		},
		{
			ActionID: retryID, TurnID: frozen.Turn.TurnID,
			Kind: CommunicationActionReplyText, Text: "干净失败的第二条",
			ContentHash: "hash-gate-sent-2", Status: CommunicationActionPlanned,
			DependsOnActionID: &firstID,
			PlannedAt:         now, CreatedAt: now, UpdatedAt: now,
		},
	} {
		if err := s.db.Create(&action).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Model(&DialogueTurn{}).
		Where("turn_id = ?", frozen.Turn.TurnID).
		Updates(map[string]any{"status": DialogueTurnAdviceReady}).Error; err != nil {
		t.Fatal(err)
	}

	later := "已发两条之后候选人的回复"
	second := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "in", Kind: "text", ContentHash: "v4-gate-sent-3", Text: &later,
	})
	next, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, second),
	)
	if err != nil || next == nil || !next.Created || next.Turn.TurnID == frozen.Turn.TurnID {
		t.Fatalf("已发前缀轮遇新输入必须作废重开: next=%+v err=%v", next, err)
	}
	stale, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
	if stale == nil || stale.Status != DialogueTurnCompleted ||
		stale.FailureReason != dialogueTurnBoundarySuperseded {
		t.Fatalf("已发前缀轮应收 completed/boundarySuperseded: %+v", stale)
	}
	var first, retried, retry CommunicationAction
	if err := s.db.First(&first, "action_id = ?", firstID).Error; err != nil ||
		first.Status != CommunicationActionSent || first.SentAt == nil ||
		first.EffectIntentID == nil || *first.EffectIntentID != sentIntentID {
		t.Fatalf("已发行必须零触碰: action=%+v err=%v", first, err)
	}
	if err := s.db.First(&retried, "action_id = ?", secondID).Error; err != nil ||
		retried.Status != CommunicationActionRetried || retried.FailureReason != "effectFailed" {
		t.Fatalf("干净失败留档行必须零触碰: action=%+v err=%v", retried, err)
	}
	if err := s.db.First(&retry, "action_id = ?", retryID).Error; err != nil ||
		retry.Status != CommunicationActionSuperseded ||
		retry.FailureReason != dialogueTurnBoundarySuperseded ||
		retry.EffectIntentID != nil || retry.SentAt != nil {
		t.Fatalf("未发的重试代必须作废、不得补发: action=%+v err=%v", retry, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" {
		t.Fatalf("作废不得冻结候选人: aggregate=%+v err=%v", aggregate, err)
	}
	// 幂等:再来一次 settle 是 no-op,不改轮状态、不报错。
	if err := s.SupersedeDialogueTurnForBoundary(frozen.Turn.TurnID, now.Add(time.Minute)); err != nil {
		t.Fatalf("已收束的轮再次 settle 必须幂等: %v", err)
	}
	if again, _ := s.DialogueTurnByID(frozen.Turn.TurnID); again == nil ||
		again.Status != DialogueTurnCompleted || again.FailureReason != dialogueTurnBoundarySuperseded {
		t.Fatalf("幂等 settle 不得改写终局: %+v", again)
	}
}

// 承重墙(2026-09-08 裁决明文保留):在途 intent(派发中/对账中/验证中)仍挡
// 作废重开;开轮照旧拒绝,旧轮与其动作行零触碰,等 WAL 收敛。
func TestFreezeCommunicationV4TurnStillRejectsInFlightIntent(t *testing.T) {
	for _, status := range []EffectIntentStatus{
		EffectIntentDispatching, EffectIntentReconciling, EffectIntentVerifying,
	} {
		t.Run(string(status), func(t *testing.T) {
			s := openTest(t)
			fixture := seedReadyCommunicationTarget(t, s, "profile-v4-gate-inflight-"+string(status))
			text := "第一轮入站"
			inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
				Seq: 2, Direction: "in", Kind: "text", ContentHash: "v4-gate-inflight-2", Text: &text,
			})
			frozen, err := s.FreezeCommunicationV4Turn(
				communicationV4TurnRequest(t, s, fixture, inbound),
			)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Millisecond)
			intentID := "intent-v4-gate-inflight-" + string(status)
			if err := s.db.Create(&EffectIntent{
				IntentID: intentID, IdemKey: "ik1:test:gate-inflight-" + string(status),
				Platform: fixture.Platform, AccountRef: fixture.AccountRef,
				Primitive: "chat.sendMessage", TargetRef: fixture.ConversationRef,
				PayloadHash: "payload", GuardsHash: "guards", RootMsgID: "root-gate-inflight-" + string(status),
				Status: status, DeadlineMs: 1, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			startedAt := now
			if err := s.db.Create(&CommunicationAction{
				ActionID: "action-v4-gate-inflight", TurnID: frozen.Turn.TurnID,
				Kind: CommunicationActionReplyText, Text: "在途动作",
				ContentHash: "hash-gate-inflight", Status: CommunicationActionEffectPending,
				EffectIntentID: &intentID, EffectStartedAt: &startedAt,
				PlannedAt: now, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.db.Model(&DialogueTurn{}).
				Where("turn_id = ?", frozen.Turn.TurnID).
				Updates(map[string]any{"status": DialogueTurnAdviceReady}).Error; err != nil {
				t.Fatal(err)
			}
			later := "在途时的新消息"
			second := appendCommunicationV4Inbound(t, s, fixture, Message{
				Seq: 3, Direction: "in", Kind: "text", ContentHash: "v4-gate-inflight-3", Text: &later,
			})
			if _, err := s.FreezeCommunicationV4Turn(
				communicationV4TurnRequest(t, s, fixture, second),
			); !errors.Is(err, ErrDialogueTurnState) {
				t.Fatalf("在途 intent 案底必须照旧拒绝开轮: %v", err)
			}
			stale, _ := s.DialogueTurnByID(frozen.Turn.TurnID)
			if stale == nil || stale.Status != DialogueTurnAdviceReady {
				t.Fatalf("被拒绝的开轮不得触碰在途轮: %+v", stale)
			}
			var action CommunicationAction
			if err := s.db.First(&action, "action_id = ?", "action-v4-gate-inflight").Error; err != nil ||
				action.Status != CommunicationActionEffectPending {
				t.Fatalf("被拒绝的开轮不得触碰在途动作: action=%+v err=%v", action, err)
			}
		})
	}
}
