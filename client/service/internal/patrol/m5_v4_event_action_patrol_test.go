package patrol

import (
	"context"
	"errors"
	"testing"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
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

	readsBefore := h.runner.count(protocol.PrimChatReadThread)
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// 2026-07-30 出口回归:派发前的定向对账只属于时刻表链首。本轮唯一一次
	// readThread 来自卡片迁移取证前的既有重开会话;事件输入的动作链不得在
	// 此之外新增任何读取。
	if h.runner.count(protocol.PrimChatReadThread) != readsBefore+1 {
		t.Fatalf(
			"事件输入动作排空只允许卡片迁移那一次既有读取: before=%d after=%d",
			readsBefore,
			h.runner.count(protocol.PrimChatReadThread),
		)
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

// 一条固定话术配成多个气泡时,第二个气泡必然以前一个气泡的正证为父。这条路曾
// 两层都不通:巡检分派表按 v4Kind 白名单只放行催2正文,记账层的依赖校验又假设
// 回执有且只有一个气泡,于是接受邀面的第二句永远发不出去,连带把挂在它后面的
// 换微信卡永久留在 planned、把整个候选人隔离成人工。
func TestCommunicationV4EventActionPatrolSendsEveryReceiptBubbleThenWechatInvite(
	t *testing.T,
) {
	h := newHarness(t)
	fixture := seedCommunicationV4InterviewAcceptedInbound(
		t,
		h,
		"receipt-bubbles",
		`{
			"rejectWechat":{"enabled":true,"messages":["合成挽留"]},
			"silence48Wechat":{"enabled":true,"messages":["合成冷催"]},
			"wechatAccepted":{"enabled":true,"messages":["好的，晚点加你"]},
			"meetingAccepted":{"enabled":true,"messages":[
				"好的，面试安排已确认",
				"咱们加个微信吧，平台消息不太及时"
			]}
		}`,
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
	if len(commands) != 3 ||
		commands[0].Name != protocol.PrimChatSendMessage ||
		commands[1].Name != protocol.PrimChatSendMessage ||
		commands[2].Name != protocol.PrimChatSendWechatInvite {
		t.Fatalf("回执两气泡没有逐条发完再发换微信卡: %+v", commands)
	}

	actions, err := h.db.CommunicationV4EventActionsByProfile(fixture.target.profileID)
	if err != nil {
		t.Fatal(err)
	}
	var receipts []store.CommunicationV4EventAction
	var invite *store.CommunicationV4EventAction
	for index := range actions {
		switch actions[index].EffectKind {
		case store.CommunicationV4EventEffectReplyText:
			receipts = append(receipts, actions[index])
		case store.CommunicationV4EventEffectInviteWechat:
			invite = &actions[index]
		}
	}
	if len(receipts) != 2 || invite == nil {
		t.Fatalf("回执未展开成两个气泡: actions=%+v", actions)
	}
	for index := range receipts {
		if receipts[index].Status != store.CommunicationV4EventActionSent ||
			receipts[index].EffectIntentID == nil {
			t.Fatalf("回执气泡未走正证轨道: ordinal=%d action=%+v",
				index+1, receipts[index])
		}
		intent, intentErr := h.db.EffectIntentByID(*receipts[index].EffectIntentID)
		if intentErr != nil || intent == nil ||
			intent.Primitive != protocol.PrimChatSendMessage ||
			intent.Status != store.EffectIntentOk {
			t.Fatalf("回执气泡 WAL 未收敛: ordinal=%d intent=%+v err=%v",
				index+1, intent, intentErr)
		}
	}
	if receipts[0].DependsOnActionID != nil {
		t.Fatalf("第一个气泡不该有依赖: %+v", receipts[0])
	}
	// 中间夹着一条对候选人不可见的运营通知,父仍必须是前一个气泡。
	if receipts[1].DependsOnActionID == nil ||
		*receipts[1].DependsOnActionID != receipts[0].ActionID {
		t.Fatalf("第二个气泡未挂在第一个之后: %+v", receipts[1])
	}
	if invite.Status != store.CommunicationV4EventActionSent ||
		invite.DependsOnActionID == nil ||
		*invite.DependsOnActionID != receipts[1].ActionID {
		t.Fatalf("换微信卡未挂在最后一个气泡之后: %+v", invite)
	}

	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
		t.Fatalf("多气泡回执不得隔离候选人: %+v", aggregate)
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
	// 缺话术隔离的是 effectful 动作。2026-08-06 甲方裁决取消收号的请求锚定后,
	// 账本里没有请求卡的档案也会被派 readonly 收号读——它无副作用、不推进任何
	// 业务状态,收到的号留待人工解除隔离后使用,不属于本用例要防的 effect。
	for _, name := range h.runner.names() {
		if name != protocol.PrimChatReadThread &&
			name != protocol.PrimChatReadWechatExchangeOutcome {
			t.Fatalf("缺话术不得构造任何 effect: %+v", h.runner.names())
		}
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

type communicationV4InboundAcceptFixture struct {
	target communicationV4PatrolFixture
	actor  *roundActor
}

// seedCommunicationV4InterviewAcceptedInbound 复刻真机上接受邀面的那条路:候选
// 人自己发来一条 accepted 的邀面卡消息,由对话轮而不是卡片跃迁对账承接。两条路
// 的动作展开并不相同,固定话术的多气泡只在这一条上出现。
func seedCommunicationV4InterviewAcceptedInbound(
	t *testing.T,
	h *harness,
	suffix string,
	fixedPhrases string,
) communicationV4InboundAcceptFixture {
	t.Helper()
	inboundText := "我想继续了解岗位"
	target := seedCommunicationV4PatrolTargetWithBoundaryAndFixedPhrases(
		t, h, "inbound-accept-"+suffix,
		[]store.MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: syncledger.HashText(inboundText),
			Text: &inboundText, Origin: "external",
		}},
		fixedPhrases,
	)
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: target.conversationRef,
	}
	project := func(seq int64, label string) {
		t.Helper()
		message, err := h.db.MessageBySeq(key, seq)
		if err != nil || message == nil {
			t.Fatalf("读取%s: message=%+v err=%v", label, message, err)
		}
		event, err := communication.NormalizeLedgerMessage(
			communication.LedgerMessageFact{
				Seq: message.Seq, Direction: message.Direction, Kind: message.Kind,
				Text: message.Text, CardType: message.CardType, CardState: message.CardState,
				Origin: message.Origin, TsApproxMs: message.TsApproxMs,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.ApplyCommunicationV4BusinessEvent(
			store.ApplyCommunicationV4BusinessEventRequest{
				ProfileID: target.profileID, Event: event, AppliedAt: h.clock.Now(),
			},
		); err != nil {
			t.Fatalf("投影%s: %v", label, err)
		}
	}
	project(target.inboundSeq, "候选人回复")

	inviteText := "合成邀面卡"
	inviteSourceKey := syncledger.HashText("invite-source-" + suffix)
	invited, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: target.inboundSeq,
		NewMessages: []store.MessageDraft{{
			Direction: "out", Kind: "card",
			ContentHash: syncledger.HashText("invite-card-" + suffix),
			Text:        &inviteText, CardType: "interviewInvite", CardState: "pending",
			Origin: "self", SourceKey: &inviteSourceKey,
		}},
		SyncedAt: h.clock.Now(),
	})
	if err != nil || len(invited.Inserted) != 1 {
		t.Fatalf("追加邀面卡: result=%+v err=%v", invited, err)
	}
	project(invited.Inserted[0].Seq, "邀面卡")

	acceptText := "我已接受贵司的面试邀请，将准时参加面试"
	accepted, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: invited.Inserted[0].Seq,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "card",
			ContentHash: syncledger.HashText("accept-card-" + suffix),
			Text:        &acceptText, CardType: "interviewInvite", CardState: "accepted",
			Origin: "external",
		}},
		SyncedAt: h.clock.Now(),
	})
	if err != nil || len(accepted.Inserted) != 1 {
		t.Fatalf("追加接受卡: result=%+v err=%v", accepted, err)
	}

	roundID := "round-v4-inbound-accept-" + suffix
	beginCommunicationV4PatrolRound(t, h, roundID)
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号: account=%+v err=%v", account, err)
	}
	return communicationV4InboundAcceptFixture{
		target: target,
		actor: &roundActor{
			manager: h.manager,
			account: account,
			hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
			roundID: roundID,
			now:     h.clock.Now(),
		},
	}
}
