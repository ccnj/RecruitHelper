package store

import (
	"errors"
	"sync"
	"testing"
)

func TestM5PlannedActionConcurrentConstructionCreatesOneIntentCommandAndHead(t *testing.T) {
	s := openTest(t)
	fixture := seedPlannedM5AutomaticAction(t, s, "concurrent-construction")
	first := automaticEffectIntentRequest(t, fixture, "concurrent-construction")
	second := first
	second.Command.MsgID = first.Command.MsgID + "-competitor"
	second.Intent.RootMsgID = ""

	type outcome struct {
		result *CreateEffectIntentResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, request := range []CreateEffectIntentRequest{first, second} {
		request := request
		go func() {
			ready.Done()
			<-start
			result, err := s.CreateEffectIntentAndCmd(request)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	ready.Wait()
	close(start)

	created, collected, conflicted := 0, 0, 0
	for index := 0; index < 2; index++ {
		got := <-outcomes
		if got.err != nil {
			if !errors.Is(got.err, ErrCommunicationActionConflict) &&
				!errors.Is(got.err, ErrEffectIntentConflict) {
				t.Fatalf("并发构造返回非授权错误: %v", got.err)
			}
			conflicted++
			continue
		}
		if got.result == nil {
			t.Fatal("并发构造返回空结果")
		}
		if got.result.Created {
			created++
		} else {
			collected++
		}
	}
	if created != 1 || collected+conflicted != 1 {
		t.Fatalf("同一 action 并发构造未收敛为一建一收编/冲突: created=%d collected=%d conflicted=%d",
			created, collected, conflicted)
	}

	intentID, err := M5AutomaticIntentID(fixture.Action.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	var intents, commands, heads int64
	if err := s.db.Model(&EffectIntent{}).Where("intent_id = ?", intentID).Count(&intents).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CmdRecord{}).Where("intent_id = ?", intentID).Count(&commands).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&ConversationEffectHead{}).Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ?",
		fixture.Platform, fixture.AccountRef, fixture.ConversationRef,
	).Count(&heads).Error; err != nil {
		t.Fatal(err)
	}
	if intents != 1 || commands != 1 || heads != 1 {
		t.Fatalf("并发构造发生账本增生: intents=%d commands=%d heads=%d", intents, commands, heads)
	}
	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionEffectPending ||
		action.EffectIntentID == nil || *action.EffectIntentID != intentID {
		t.Fatalf("并发构造后 action 未唯一绑定 intent: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnDispatching {
		t.Fatalf("并发构造后 turn 未唯一推进 dispatching: turn=%+v err=%v", turn, err)
	}
}
