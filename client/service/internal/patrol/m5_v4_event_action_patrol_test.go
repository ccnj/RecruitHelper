package patrol

import (
	"context"
	"errors"
	"testing"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func TestCommunicationV4EventActionPatrolSendsReceiptThenWechatInviteOnce(
	t *testing.T,
) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingInterviewTransition(
		t,
		h,
		"event-action-positive",
		"accepted",
	)
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config)
	if err != nil {
		t.Fatal(err)
	}
	actor := *fixture.actor
	actor.manager = manager

	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	hand.mu.Lock()
	commands := append([]protocol.CmdBody(nil), hand.commands...)
	hand.mu.Unlock()
	if len(commands) != 2 ||
		commands[0].Name != protocol.PrimChatSendMessage ||
		commands[1].Name != protocol.PrimChatSendWechatInvite {
		t.Fatalf("事件动作没有严格按回执→换微信卡派发: %+v", commands)
	}
	actions, err := h.db.CommunicationV4EventActionsByProfile(
		fixture.target.profileID,
	)
	var receipt *store.CommunicationV4EventAction
	var invite *store.CommunicationV4EventAction
	var notification *store.CommunicationV4EventAction
	for index := range actions {
		switch actions[index].EffectKind {
		case store.CommunicationV4EventEffectReplyText:
			receipt = &actions[index]
		case store.CommunicationV4EventEffectInviteWechat:
			invite = &actions[index]
		case store.CommunicationV4EventEffectNotification:
			notification = &actions[index]
		}
	}
	if err != nil || len(actions) != 3 ||
		receipt == nil ||
		receipt.Status != store.CommunicationV4EventActionSent ||
		receipt.EffectIntentID == nil ||
		invite == nil ||
		invite.Status != store.CommunicationV4EventActionSent ||
		invite.EffectIntentID == nil ||
		invite.DependsOnActionID == nil ||
		*invite.DependsOnActionID != receipt.ActionID ||
		notification == nil ||
		notification.Status != store.CommunicationV4EventActionDeferred {
		t.Fatalf("事件动作 WAL/依赖没有收敛: actions=%+v err=%v", actions, err)
	}
	firstIntent, err := h.db.EffectIntentByID(*receipt.EffectIntentID)
	if err != nil || firstIntent == nil ||
		firstIntent.Primitive != protocol.PrimChatSendMessage ||
		firstIntent.Status != store.EffectIntentOk {
		t.Fatalf("回执未走正证轨道: intent=%+v err=%v", firstIntent, err)
	}
	secondIntent, err := h.db.EffectIntentByID(*invite.EffectIntentID)
	if err != nil || secondIntent == nil ||
		secondIntent.Primitive != protocol.PrimChatSendWechatInvite ||
		secondIntent.Status != store.EffectIntentOk {
		t.Fatalf("依赖卡未串在回执正证之后: intent=%+v err=%v", secondIntent, err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(
		fixture.target.profileID,
	)
	if err != nil {
		t.Fatal(err)
	}
	revision := aggregate.Revision

	restartedDispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(restartedDispatcher)
	restartedRunner := &m5AutomaticReplyRunner{
		base: h.runner, dispatcher: restartedDispatcher,
	}
	restarted, err := NewManager(h.db, restartedRunner, h.hands, h.config)
	if err != nil {
		t.Fatal(err)
	}
	restartedActor := actor
	restartedActor.manager = restarted
	for attempt := 0; attempt < 2; attempt++ {
		restarted.mu.Lock()
		err = restartedActor.processCommunicationV4Targets(context.Background())
		restarted.mu.Unlock()
		if err != nil {
			t.Fatalf("重启后重复 drain 失败: attempt=%d err=%v", attempt+1, err)
		}
	}
	replayed, err := h.db.CommunicationV4AggregateByProfile(
		fixture.target.profileID,
	)
	if err != nil || replayed.Revision != revision || hand.commandCount() != 2 {
		t.Fatalf("重启/下一轮发生事件动作增生: aggregate=%+v sends=%d err=%v",
			replayed, hand.commandCount(), err)
	}
	replayedActions, err := h.db.CommunicationV4EventActionsByProfile(
		fixture.target.profileID,
	)
	if err != nil || len(replayedActions) != 3 {
		t.Fatalf("重启后事件动作事实增生: actions=%+v err=%v", replayedActions, err)
	}
}

func TestCommunicationV4EventActionPatrolMissingPhraseIsolatesOnlySourceProfile(
	t *testing.T,
) {
	h := newHarness(t)
	missing := seedCommunicationV4PatrolTargetWithBoundaryAndFixedPhrases(
		t,
		h,
		"event-action-missing-phrase",
		[]store.MessageDraft{{
			Direction: "in", Kind: "card",
			CardType: "wechatExchange", CardState: "accepted",
			ContentHash: "missing-phrase-wechat-card", Origin: "external",
		}},
		`{}`,
	)
	healthy := seedCommunicationV4PatrolTarget(
		t,
		h,
		"event-action-healthy",
		"我想继续了解岗位",
	)
	roundID := "round-v4-event-action-missing-phrase"
	beginCommunicationV4PatrolRound(t, h, roundID)
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
	}
	actor := &roundActor{
		manager: h.manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}

	h.manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	missingAggregate, err := h.db.CommunicationV4AggregateByProfile(missing.profileID)
	if err != nil ||
		missingAggregate.AutomationStatus != store.ProfileCommunicationAutomationManualRequired ||
		missingAggregate.ManualReason !=
			store.CommunicationV4EventActionFailureFixedPhraseUnavailable {
		t.Fatalf("缺固定话术没有隔离来源档案: aggregate=%+v err=%v",
			missingAggregate, err)
	}
	missingActions, err := h.db.CommunicationV4EventActionsByProfile(missing.profileID)
	foundMissingPhrase := false
	for index := range missingActions {
		if missingActions[index].Status == store.CommunicationV4EventActionManualRequired &&
			missingActions[index].FailureReason ==
				store.CommunicationV4EventActionFailureFixedPhraseUnavailable {
			foundMissingPhrase = true
		}
	}
	if err != nil || len(missingActions) != 2 || !foundMissingPhrase {
		t.Fatalf("缺话术动作事实不正确: actions=%+v err=%v", missingActions, err)
	}
	healthyAggregate, err := h.db.CommunicationV4AggregateByProfile(healthy.profileID)
	if err != nil ||
		healthyAggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
		t.Fatalf("缺话术隔离误伤其他档案: aggregate=%+v err=%v",
			healthyAggregate, err)
	}
	if len(h.runner.names()) != 0 {
		t.Fatalf("缺话术不得构造任何 effect: %+v", h.runner.names())
	}
}

func TestCommunicationV4EventActionPatrolPacingInterruptionKeepsPlan(
	t *testing.T,
) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingInterviewTransition(
		t,
		h,
		"event-action-pacing-stop",
		"accepted",
	)
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	config := h.config
	config.InteractionPaceWait = func(context.Context) error {
		return context.Canceled
	}
	manager, err := NewManager(h.db, runner, h.hands, config)
	if err != nil {
		t.Fatal(err)
	}
	actor := *fixture.actor
	actor.manager = manager

	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("节奏等待中断必须原样上抛: %v", err)
	}
	actions, readErr := h.db.CommunicationV4EventActionsByProfile(
		fixture.target.profileID,
	)
	if readErr != nil || len(actions) != 3 {
		t.Fatalf("节奏等待后动作不可读: actions=%+v err=%v", actions, readErr)
	}
	for _, action := range actions {
		if action.Status == store.CommunicationV4EventActionDeferred {
			continue
		}
		if action.Status != store.CommunicationV4EventActionPlanned || action.EffectIntentID != nil {
			t.Fatalf("pre-WAL 中断不得终局化动作: %+v", action)
		}
	}
	aggregate, readErr := h.db.CommunicationV4AggregateByProfile(
		fixture.target.profileID,
	)
	if readErr != nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive ||
		hand.commandCount() != 0 {
		t.Fatalf("pre-WAL 中断不得隔离或发送: aggregate=%+v sends=%d err=%v",
			aggregate, hand.commandCount(), readErr)
	}
}
