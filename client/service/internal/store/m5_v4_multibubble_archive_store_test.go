package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/textcanon"
)

func TestCommunicationV4ArchiveKeepsConfirmedBubblePrefixAndSupersedesOnlyTail(
	t *testing.T,
) {
	s := openTest(t)
	phrases := []string{"fixture replaces first", "已确认的第二个气泡", "不得再发送的尾气泡"}
	fixture := seedCommunicationV4MultiBubblePlans(
		t,
		s,
		"multi-bubble-archive-prefix",
		phrases,
		false,
	)

	actions, err := s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if err != nil || len(actions) != 1 {
		t.Fatalf("首气泡未就绪: actions=%+v err=%v", actions, err)
	}
	confirmCommunicationV4Bubble(
		t,
		s,
		fixture,
		actions[0],
		fixture.Turn.InboundThroughSeq,
		"multi-bubble-archive-prefix-1",
	)
	actions, err = s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if err != nil || len(actions) != 2 {
		t.Fatalf("第二气泡未按正证物化: actions=%+v err=%v", actions, err)
	}
	confirmCommunicationV4Bubble(
		t,
		s,
		fixture,
		actions[1],
		fixture.Turn.InboundThroughSeq+1,
		"multi-bubble-archive-prefix-2",
	)
	actions, err = s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if err != nil || len(actions) != 3 {
		t.Fatalf("尾气泡未按正证物化: actions=%+v err=%v", actions, err)
	}

	beforeArchive, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || beforeArchive.State.LastBodyAt == nil {
		t.Fatalf("读取归档前聚合失败: aggregate=%+v err=%v", beforeArchive, err)
	}
	candidateText := "前缀已发出后候选人的新回复"
	appendCommunicationV4Inbound(t, s, fixture.resumeStoreFixture, Message{
		Seq:         beforeArchive.ProjectedThroughSeq + 1,
		Direction:   "in",
		Kind:        "text",
		ContentHash: textcanon.Hash(candidateText),
		Text:        &candidateText,
	})

	archiveAt := beforeArchive.State.LastBodyAt.Add(8 * 24 * time.Hour)
	req := communicationV4ArchiveRequestForTest(
		t,
		s,
		*beforeArchive,
		archiveAt,
		true,
	)
	result, err := s.ApplyCommunicationV4ArchiveAction(req)
	if err != nil || result == nil || !result.Applied ||
		result.Aggregate.State.MainStatus != communication.V4StatusEnded {
		t.Fatalf("多气泡部分正证后未完成七天归档: result=%+v err=%v", result, err)
	}

	archivedActions, err := s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if err != nil || len(archivedActions) != 3 {
		t.Fatalf("归档后动作事实缺失: actions=%+v err=%v", archivedActions, err)
	}
	for index := 0; index < 2; index++ {
		if archivedActions[index].Status != CommunicationActionSent ||
			archivedActions[index].EffectIntentID == nil ||
			archivedActions[index].SentAt == nil ||
			archivedActions[index].FailureReason != "" {
			t.Fatalf("已确认前缀第 %d 项被归档改写: %+v", index+1, archivedActions[index])
		}
	}
	tail := archivedActions[2]
	if tail.Status != CommunicationActionSuperseded ||
		tail.FailureReason != communicationV4ArchiveSuperseded ||
		tail.EffectIntentID != nil ||
		tail.EffectStartedAt != nil ||
		tail.SentAt != nil {
		t.Fatalf("归档没有只作废未开始尾项: %+v", tail)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil ||
		turn.Status != DialogueTurnSuperseded ||
		turn.FailureReason != communicationV4ArchiveSuperseded {
		t.Fatalf("归档没有收束多气泡 turn: turn=%+v err=%v", turn, err)
	}

	replayed, err := s.ApplyCommunicationV4ArchiveAction(req)
	if err != nil || replayed == nil || replayed.Applied ||
		replayed.Aggregate.Revision != result.Aggregate.Revision {
		t.Fatalf("归档重放发生增生: result=%+v err=%v", replayed, err)
	}
	afterReplay, err := s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if err != nil || len(afterReplay) != 3 ||
		afterReplay[0].Status != CommunicationActionSent ||
		afterReplay[1].Status != CommunicationActionSent ||
		afterReplay[2].Status != CommunicationActionSuperseded {
		t.Fatalf("归档重放改写了动作前缀或尾项: actions=%+v err=%v", afterReplay, err)
	}
}

func TestCommunicationV4ArchiveRejectsMoreThanOneUnstartedBubbleTail(t *testing.T) {
	s := openTest(t)
	phrases := []string{"fixture replaces first", "未开始的第二气泡", "歧义的第三气泡"}
	fixture := seedCommunicationV4MultiBubblePlans(
		t,
		s,
		"multi-bubble-archive-ambiguous-tail",
		phrases,
		false,
	)
	actions, _ := s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	confirmCommunicationV4Bubble(
		t,
		s,
		fixture,
		actions[0],
		fixture.Turn.InboundThroughSeq,
		"multi-bubble-archive-ambiguous-tail-1",
	)
	actions, _ = s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	parentID := actions[1].ActionID
	at := time.Now().UTC().Truncate(time.Millisecond)
	extra := CommunicationAction{
		ActionID:          fixture.Turn.TurnID + "|replyText|bubble:3",
		TurnID:            fixture.Turn.TurnID,
		Kind:              CommunicationActionReplyText,
		Text:              phrases[2],
		ContentHash:       textcanon.Hash(phrases[2]),
		DependsOnActionID: &parentID,
		Status:            CommunicationActionPlanned,
		PlannedAt:         at,
		CreatedAt:         at,
		UpdatedAt:         at,
	}
	if err := s.db.Create(&extra).Error; err != nil {
		t.Fatal(err)
	}

	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.State.LastBodyAt == nil {
		t.Fatalf("读取歧义尾项聚合失败: aggregate=%+v err=%v", aggregate, err)
	}
	archiveAt := aggregate.State.LastBodyAt.Add(8 * 24 * time.Hour)
	_, err = s.ApplyCommunicationV4ArchiveAction(
		communicationV4ArchiveRequestForTest(t, s, *aggregate, archiveAt, true),
	)
	if !errors.Is(err, ErrCommunicationV4Corrupt) {
		t.Fatalf("两个未开始尾项必须保守拒绝归档: %v", err)
	}
	assertCommunicationV4ArchiveFailurePreservesTurn(
		t,
		s,
		fixture.Turn.TurnID,
		2,
	)
}

func TestCommunicationV4ArchiveRejectsUnconfirmedSentBubblePrefix(t *testing.T) {
	s := openTest(t)
	phrases := []string{"fixture replaces first", "唯一未开始尾气泡"}
	fixture := seedCommunicationV4MultiBubblePlans(
		t,
		s,
		"multi-bubble-archive-unconfirmed-prefix",
		phrases,
		false,
	)
	actions, _ := s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	confirmCommunicationV4Bubble(
		t,
		s,
		fixture,
		actions[0],
		fixture.Turn.InboundThroughSeq,
		"multi-bubble-archive-unconfirmed-prefix-1",
	)
	actions, _ = s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if len(actions) != 2 || actions[0].EffectIntentID == nil {
		t.Fatalf("未形成待篡改的已发前缀: %+v", actions)
	}
	if err := s.db.Model(&EffectIntent{}).
		Where("intent_id = ?", *actions[0].EffectIntentID).
		Update("status", EffectIntentVerifying).Error; err != nil {
		t.Fatal(err)
	}

	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.State.LastBodyAt == nil {
		t.Fatalf("读取未证实前缀聚合失败: aggregate=%+v err=%v", aggregate, err)
	}
	archiveAt := aggregate.State.LastBodyAt.Add(8 * 24 * time.Hour)
	_, err = s.ApplyCommunicationV4ArchiveAction(
		communicationV4ArchiveRequestForTest(t, s, *aggregate, archiveAt, true),
	)
	if !errors.Is(err, ErrCommunicationV4Corrupt) {
		t.Fatalf("非正证 effect intent 前缀必须保守拒绝归档: %v", err)
	}
	assertCommunicationV4ArchiveFailurePreservesTurn(
		t,
		s,
		fixture.Turn.TurnID,
		1,
	)
}

func assertCommunicationV4ArchiveFailurePreservesTurn(
	t *testing.T,
	s *Store,
	turnID string,
	plannedCount int,
) {
	t.Helper()
	turn, err := s.DialogueTurnByID(turnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnAdviceReady {
		t.Fatalf("失败归档改写了 turn: turn=%+v err=%v", turn, err)
	}
	actions, err := s.CommunicationActionsByTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	actualPlanned := 0
	for index := range actions {
		if actions[index].Status == CommunicationActionSuperseded {
			t.Fatalf("失败归档作废了动作: %+v", actions[index])
		}
		if actions[index].Status == CommunicationActionPlanned {
			actualPlanned++
		}
	}
	if actualPlanned != plannedCount {
		t.Fatalf("失败归档后的 planned 数错误: got=%d want=%d actions=%+v",
			actualPlanned, plannedCount, actions)
	}
}
