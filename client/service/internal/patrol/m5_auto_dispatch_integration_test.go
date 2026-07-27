package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type m5AutomaticReplyRunner struct {
	base       *fakeRunner
	dispatcher *dispatch.Dispatcher
}

type m5InboundAutomaticRunner struct {
	*m5AutomaticReplyRunner
}

func (r *m5AutomaticReplyRunner) Start(ctx context.Context, req RunRequest) (RunHandle, error) {
	return r.base.Start(ctx, req)
}

func (r *m5InboundAutomaticRunner) StartResumeCapture(
	ctx context.Context,
	req ResumeCaptureRequest,
) (ResumeCaptureHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	receipt, err := r.dispatcher.DispatchResumeCapture(
		dispatch.ResumeCaptureDispatchRequest{
			ProfileID: req.ProfileID, HandID: req.HandID,
			ExpectedSession: req.ExpectedSession, ExpectedBootID: req.ExpectedBootID,
			Platform: req.Platform, AccountRef: req.AccountRef,
			ExpectedPrincipalFingerprint: req.ExpectedPrincipalFingerprint,
		},
	)
	if err != nil {
		return nil, err
	}
	return &m5InboundResumeHandle{
		dispatcher: r.dispatcher,
		logicalID:  receipt.LogicalDispatchID,
	}, nil
}

type m5InboundResumeHandle struct {
	dispatcher *dispatch.Dispatcher
	logicalID  string
}

func (h *m5InboundResumeHandle) LogicalDispatchID() string { return h.logicalID }

func (h *m5InboundResumeHandle) Wait(ctx context.Context) (json.RawMessage, error) {
	logical, err := h.dispatcher.WaitLogical(ctx, h.logicalID)
	if err != nil {
		return nil, err
	}
	if logical == nil || logical.Leaf.ResultBody == "" {
		return nil, errors.New("简历补采缺少持久结果")
	}
	var result protocol.ResultBody
	if err := json.Unmarshal([]byte(logical.Leaf.ResultBody), &result); err != nil {
		return nil, err
	}
	if result.Status != protocol.ResultStatusOk ||
		len(result.Data) == 0 ||
		string(result.Data) == "null" {
		return nil, errors.New("简历补采未成功")
	}
	return append(json.RawMessage(nil), result.Data...), nil
}

func (r *m5AutomaticReplyRunner) StartAutomaticReply(
	ctx context.Context,
	req AutomaticReplyRequest,
) (AutomaticReplyHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	receipt, err := r.dispatcher.SendMessage(dispatch.SendMessageRequest{
		IntentID: req.IntentID, PreviousIntentID: req.PreviousIntentID,
		AutomaticActionID: req.ActionID,
		ExpectedSession:   req.ExpectedSession, ExpectedBootID: req.ExpectedBootID,
		Platform: req.Platform, AccountRef: req.AccountRef,
		ConversationRef: req.ConversationRef, Text: req.Text,
	})
	if err != nil {
		return nil, err
	}
	if receipt == nil || receipt.LogicalDispatchID == "" {
		return nil, errors.New("真实 dispatcher 未返回自动回复 logical dispatch")
	}
	return &m5AutomaticReplyHandle{
		dispatcher: r.dispatcher,
		logicalID:  receipt.LogicalDispatchID,
	}, nil
}

func (r *m5AutomaticReplyRunner) StartAutomaticCard(
	ctx context.Context,
	req AutomaticCardRequest,
) (AutomaticCardHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	primitive := ""
	switch req.Kind {
	case store.CommunicationActionInviteWechat:
		primitive = protocol.PrimChatSendWechatInvite
	case store.CommunicationActionInterviewInvite:
		primitive = protocol.PrimChatSendInviteCard
	default:
		return nil, store.ErrCommunicationActionInvalid
	}
	receipt, err := r.dispatcher.SendAutomaticCard(dispatch.SendAutomaticCardRequest{
		IntentID: req.IntentID, PreviousIntentID: req.PreviousIntentID,
		AutomaticActionID: req.ActionID,
		ExpectedSession:   req.ExpectedSession, ExpectedBootID: req.ExpectedBootID,
		Platform: req.Platform, AccountRef: req.AccountRef,
		ConversationRef: req.ConversationRef, Primitive: primitive,
		Interview: req.Interview,
	})
	if err != nil {
		return nil, err
	}
	if receipt == nil || receipt.LogicalDispatchID == "" {
		return nil, errors.New("真实 dispatcher 未返回自动卡片 logical dispatch")
	}
	return &m5AutomaticReplyHandle{
		dispatcher: r.dispatcher,
		logicalID:  receipt.LogicalDispatchID,
	}, nil
}

type m5AutomaticReplyHandle struct {
	dispatcher *dispatch.Dispatcher
	logicalID  string
}

func (h *m5AutomaticReplyHandle) Wait(ctx context.Context) error {
	_, err := h.dispatcher.WaitLogical(ctx, h.logicalID)
	return err
}

type m5PositiveHand struct {
	mu         sync.Mutex
	dispatcher *dispatch.Dispatcher
	commands   []protocol.CmdBody
}

func (h *m5PositiveHand) setDispatcher(dispatcher *dispatch.Dispatcher) {
	h.mu.Lock()
	h.dispatcher = dispatcher
	h.mu.Unlock()
}

func (h *m5PositiveHand) SendEnvelope(handID string, env protocol.Envelope) error {
	if handID != "hand-1" {
		return fmt.Errorf("自动回复发往错误的手 %q", handID)
	}
	if env.Kind != protocol.KindCmd {
		return nil
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return err
	}
	h.mu.Lock()
	h.commands = append(h.commands, body)
	dispatcher := h.dispatcher
	h.mu.Unlock()
	if dispatcher == nil {
		return errors.New("fake hand 未绑定 dispatcher")
	}
	var data []byte
	var evidenceType string
	var err error
	switch body.Name {
	case protocol.PrimCandidateReadResume:
		var args protocol.CandidateReadResumeArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		data, err = protocol.Encode(protocol.CandidateReadResumeData{
			ConversationRef: args.ConversationRef,
			PlatformUserRef: args.PlatformUserRef,
			ObservedAt:      time.Now().UnixMilli(),
			Basic: []protocol.CandidateResumeLabelValue{
				{Label: "学历", Value: "本科"},
			},
			Expectations: []protocol.CandidateResumeLabelValue{
				{Label: "职位", Value: "合成职位"},
			},
			SelfEvaluation:  "合成自评",
			Education:       "合成教育",
			WorkExperiences: "合成经历",
		})
	case protocol.PrimChatSendMessage:
		var args protocol.ChatSendMessageArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		data, err = protocol.Encode(protocol.ChatSendMessageData{
			ConversationRef: args.ConversationRef,
			ContentHash:     syncledger.HashText(args.Text),
			ObservedAt:      time.Now().UnixMilli(),
		})
		evidenceType = string(protocol.SendMessageEvidenceTypeOutboundMessageObserved)
	case protocol.PrimChatSendWechatInvite:
		var args protocol.ChatSendWechatInviteArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		data, err = protocol.Encode(protocol.ChatSendWechatInviteData{
			ConversationRef: args.ConversationRef,
			ContentHash:     syncledger.WechatExchangeContentHash(),
			SourceKey:       strings.Repeat("a", 64),
			ObservedAt:      time.Now().UnixMilli(),
		})
		evidenceType = string(protocol.SendWechatInviteEvidenceTypeOutboundWechatInviteObserved)
	case protocol.PrimChatSendInviteCard:
		var args protocol.ChatSendInviteCardArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		data, err = protocol.Encode(protocol.ChatSendInviteCardData{
			ConversationRef: args.ConversationRef,
			ContentHash: syncledger.InterviewInviteContentHash(
				args.Interview.StartsAt,
				args.Interview.EndsAt,
				string(args.Interview.Method),
			),
			SourceKey:  strings.Repeat("b", 64),
			Interview:  args.Interview,
			ObservedAt: time.Now().UnixMilli(),
		})
		evidenceType = string(protocol.SendInviteCardEvidenceTypeOutboundInterviewInviteObserved)
	default:
		return fmt.Errorf("fake hand 收到非自动沟通原语 %q", body.Name)
	}
	if err != nil {
		return err
	}
	evidence := []protocol.Evidence(nil)
	if evidenceType != "" {
		evidence = []protocol.Evidence{{Type: evidenceType}}
	}
	dispatcher.OnAck(handID, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
	dispatcher.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
		Ref: env.MsgID, Status: protocol.ResultStatusOk, Data: data,
		Evidence: evidence,
	})
	return nil
}

func (*m5PositiveHand) HandSession(handID string) (string, string, bool) {
	if handID != "hand-1" {
		return "", "", false
	}
	return "session-1", "boot-1", true
}

func (*m5PositiveHand) HandContractMatch(handID string) (bool, bool) {
	return handID == "hand-1", handID == "hand-1"
}

func (*m5PositiveHand) HandNegotiation(handID string) ([]string, []string, bool) {
	if handID != "hand-1" {
		return nil, nil, false
	}
	return []string{
			protocol.PrimCandidateReadResume + "@1",
			protocol.PrimChatSendMessage + "@1",
			protocol.PrimChatSendWechatInvite + "@1",
			protocol.PrimChatSendInviteCard + "@1",
		}, []string{
			string(protocol.FeatureLease1),
			string(protocol.FeatureProgress1),
			string(protocol.FeatureCancel1),
			string(protocol.FeatureWitness1),
		}, true
}

func (*m5PositiveHand) HandWitness(handID string) (dispatch.HandWitness, bool) {
	if handID != "hand-1" {
		return dispatch.HandWitness{}, false
	}
	return dispatch.HandWitness{StoreID: "witness-m5-integration"}, true
}

func (*m5PositiveHand) CloseHand(string, string, string) bool { return true }
func (*m5PositiveHand) HandOfflineMs(string) int64            { return 0 }

func (h *m5PositiveHand) commandCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.commands)
}

func TestM5AutomaticReplyCrossesRealDispatcherOnceAndSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	advice := &recordingAdviceExecutor{}
	hand := &m5PositiveHand{}
	paceCalls := 0
	h.config.InteractionPaceWait = func(ctx context.Context) error {
		if hand.commandCount() != 0 {
			t.Fatal("自动回复必须先完成交互节奏等待再构造 WAL")
		}
		paceCalls++
		return ctx.Err()
	}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取试运行账号失败: account=%+v err=%v", account, err)
	}
	actor := &roundActor{
		manager: manager, account: account,
		hand: HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		now:  h.clock.Now(),
	}

	manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), fixture.turn)
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(advice.requests) != 2 {
		t.Fatalf("完整自动回复必须恰有 intent/reply 两次建议调用: %d", len(advice.requests))
	}
	if hand.commandCount() != 1 {
		t.Fatalf("fake hand 必须只收到一条 chat.sendMessage: %d", hand.commandCount())
	}
	if paceCalls != 1 {
		t.Fatalf("自动回复必须恰好等待一次交互节奏: %d", paceCalls)
	}

	action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || action == nil || action.Status != store.CommunicationActionSent ||
		action.EffectIntentID == nil || action.SentAt == nil {
		t.Fatalf("自动 action 未以正证收敛为 sent: action=%+v err=%v", action, err)
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted {
		t.Fatalf("自动回复后 turn 未完成: turn=%+v err=%v", turn, err)
	}
	trial, err := h.db.M5TrialStatus()
	if err != nil || trial == nil || trial.Selection.Status != store.M5TrialSelectionCompleted ||
		trial.Selection.ActiveSlot != nil {
		t.Fatalf("一次性试运行未完成并释放 active slot: trial=%+v err=%v", trial, err)
	}

	intent, err := h.db.EffectIntentByID(*action.EffectIntentID)
	if err != nil || intent == nil || intent.Status != store.EffectIntentOk || intent.ResultMessageSeq == nil {
		t.Fatalf("自动 effect intent 未以唯一正证完成: intent=%+v err=%v", intent, err)
	}
	commands, err := h.db.RecentCmds(100)
	if err != nil {
		t.Fatal(err)
	}
	sendCommands := 0
	for i := range commands {
		if commands[i].Name == protocol.PrimChatSendMessage {
			sendCommands++
			if commands[i].IntentID != intent.IntentID || commands[i].Status != store.CmdOk {
				t.Fatalf("唯一 sendMessage 命令未绑定并成功于自动 intent: %+v", commands[i])
			}
		}
	}
	if sendCommands != 1 {
		t.Fatalf("自动闭环必须只创建一个 sendMessage Cmd: %d", sendCommands)
	}
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	selfMessages := 0
	for i := range messages {
		if messages[i].OutboundIntentID != nil && *messages[i].OutboundIntentID == intent.IntentID {
			selfMessages++
			if messages[i].Origin != "self" || messages[i].Text == nil || *messages[i].Text != action.Text {
				t.Fatalf("自动回复 self 消息事实错误: %+v", messages[i])
			}
		}
	}
	if selfMessages != 1 {
		t.Fatalf("自动 intent 必须只产生一条 self 消息: %d", selfMessages)
	}

	// Recreate the brain-side dispatcher and manager, then advance the same
	// persisted turn twice. Completed facts must short-circuit before provider,
	// intent construction, or hand delivery.
	restartedDispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(restartedDispatcher)
	restartedRunner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: restartedDispatcher}
	restarted, err := NewManager(h.db, restartedRunner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	restartedActor := &roundActor{
		manager: restarted, account: account,
		hand: HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		now:  h.clock.Now(),
	}
	for attempt := 0; attempt < 2; attempt++ {
		restarted.mu.Lock()
		err = restartedActor.processM5Trial(context.Background())
		restarted.mu.Unlock()
		if err != nil {
			t.Fatalf("重启后第 %d 次重复推进失败: %v", attempt+1, err)
		}
	}
	if len(advice.requests) != 2 || hand.commandCount() != 1 {
		t.Fatalf("重启/重复推进发生增生: advice=%d handCommands=%d", len(advice.requests), hand.commandCount())
	}
	afterCommands, err := h.db.RecentCmds(100)
	if err != nil {
		t.Fatal(err)
	}
	afterSendCommands := 0
	for i := range afterCommands {
		if afterCommands[i].Name == protocol.PrimChatSendMessage {
			afterSendCommands++
		}
	}
	afterMessages, err := h.db.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || afterSendCommands != 1 || len(afterMessages) != len(messages) || len(invocations) != 2 {
		t.Fatalf("重启后持久事实增生: sendCmds=%d messages=%d/%d invocations=%d err=%v",
			afterSendCommands, len(afterMessages), len(messages), len(invocations), err)
	}
}

func TestM5ResumeBusinessEventCrossesExistingWALOnce(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5ResumeAdviceFixture(t, h)
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		if call != 1 || request.Purpose != m5ai.PurposeReply {
			return m5ai.CompletionResponse{}, fmt.Errorf("简历分支发生未授权调用: call=%d purpose=%s", call, request.Purpose)
		}
		return safeFakeResponse(`{"话术_序列":["收到简历了，我们继续聊聊这个岗位。"],"动作":"无"}`), nil
	}}
	hand := &m5PositiveHand{}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取试运行账号失败: account=%+v err=%v", account, err)
	}
	actor := &roundActor{
		manager: manager, account: account,
		hand: HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		now:  h.clock.Now(),
	}
	manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), fixture.turn)
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(advice.requests) != 1 || advice.requests[0].Purpose != m5ai.PurposeReply || hand.commandCount() != 1 {
		t.Fatalf("简历分支必须零 intent、一次 reply、一次发送: advice=%+v commands=%d", advice.requests, hand.commandCount())
	}
	action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || action == nil || action.Status != store.CommunicationActionSent || action.EffectIntentID == nil {
		t.Fatalf("简历分支 action 未经既有正证轨道完成: action=%+v err=%v", action, err)
	}
	intent, err := h.db.EffectIntentByID(*action.EffectIntentID)
	if err != nil || intent == nil || intent.Status != store.EffectIntentOk || intent.ResultMessageSeq == nil {
		t.Fatalf("简历分支唯一 effect intent 未完成: intent=%+v err=%v", intent, err)
	}
	commands, err := h.db.RecentCmds(100)
	if err != nil {
		t.Fatal(err)
	}
	sendCommands := 0
	for i := range commands {
		if commands[i].Name == protocol.PrimChatSendMessage {
			sendCommands++
		}
	}
	key := store.ConversationKey{Platform: h.key.Platform, AccountRef: h.key.AccountRef, ConversationRef: fixture.conversationRef}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	selfMessages := 0
	for i := range messages {
		if messages[i].OutboundIntentID != nil && *messages[i].OutboundIntentID == intent.IntentID {
			selfMessages++
		}
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || sendCommands != 1 || selfMessages != 1 || len(invocations) != 1 || invocations[0].Purpose != m5ai.PurposeReply {
		t.Fatalf("简历分支持久事实不唯一: sendCmd=%d self=%d invocations=%+v err=%v", sendCommands, selfMessages, invocations, err)
	}

	restartedDispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(restartedDispatcher)
	restarted, err := NewManager(h.db, &m5AutomaticReplyRunner{base: h.runner, dispatcher: restartedDispatcher}, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	restartedActor := &roundActor{
		manager: restarted, account: account,
		hand: HandState{Online: true, Session: "session-1", BootID: "boot-1"}, now: h.clock.Now(),
	}
	restarted.mu.Lock()
	err = restartedActor.processM5Trial(context.Background())
	restarted.mu.Unlock()
	if err != nil || len(advice.requests) != 1 || hand.commandCount() != 1 {
		t.Fatalf("简历分支重启重放发生增生: advice=%d commands=%d err=%v", len(advice.requests), hand.commandCount(), err)
	}
}
