package patrol

import (
	"context"
	"fmt"
	"testing"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
)

// 生产锁死链的直接复现（0727当日计划3）：平台系统消息先被单独投影、
// 游标停在 system 行，候选人回复晚一轮到达。修复前该序列判
// outboundBoundaryMissing 并永久挂起；修复后必须正常建轮、完成 AI
// 建议并发送恰一条回复。
func TestCommunicationV4PatrolSplitSystemThenCandidateAcrossRounds(t *testing.T) {
	h := newHarness(t)
	systemText := "对方拒绝与您交换微信"
	fixture := seedCommunicationV4PatrolTargetWithBoundary(t, h, "split-system", []store.MessageDraft{
		{
			Direction: "system", Kind: "system",
			ContentHash: syncledger.HashText(systemText), Text: &systemText, Origin: "external",
		},
	})

	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				return safeFakeResponse(`{"话术_序列":["合成分批回复"],"动作":"无"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
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
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}

	beginCommunicationV4PatrolRound(t, h, "round-split-1")
	first := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: "round-split-1", now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = first.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive ||
		aggregate.ProjectedThroughSeq != fixture.inboundSeq {
		t.Fatalf("轮1未把游标停在 system 行且保持 active: aggregate=%+v err=%v", aggregate, err)
	}
	if turn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID); err != nil || turn != nil {
		t.Fatalf("仅系统消息不得建轮: turn=%+v err=%v", turn, err)
	}
	if hand.commandCount() != 0 {
		t.Fatalf("仅系统消息不得发送: sends=%d", hand.commandCount())
	}

	replyText := "我不考虑换行业"
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: store.ConversationKey{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ConversationRef: fixture.conversationRef,
		},
		ExpectedTailSeq: fixture.inboundSeq,
		PlatformUserRef: "person-v4-patrol-split-system",
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text",
			ContentHash: syncledger.HashText(replyText), Text: &replyText, Origin: "external",
		}},
		SyncedAt: h.clock.Now(),
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加候选人回复失败: changes=%+v err=%v", changes, err)
	}
	inSeq := changes.Inserted[0].Seq

	beginCommunicationV4PatrolRound(t, h, "round-split-2")
	second := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: "round-split-2", now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = second.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	turn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
		turn.InboundFromSeq != inSeq || turn.InboundThroughSeq != inSeq ||
		turn.HistoryThroughSeq != inSeq-1 {
		t.Fatalf("分批到达未按解耦语义建轮: turn=%+v err=%v", turn, err)
	}
	messages, err := h.db.MessagesForConversation(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	var greeting, inbound *store.Message
	for index := range messages {
		switch messages[index].Seq {
		case 1:
			greeting = &messages[index]
		case inSeq:
			inbound = &messages[index]
		}
	}
	if greeting == nil || inbound == nil {
		t.Fatalf("账本缺少锚或入站行: messages=%+v", messages)
	}
	digest, turnID, err := store.DialogueTurnIdentity(
		fixture.profileID, *greeting, []store.Message{*inbound},
	)
	if err != nil || digest != turn.InputDigest || turnID != turn.TurnID {
		t.Fatalf("分批身份必须与同批公式一致: digest=%s turnID=%s turn=%+v err=%v",
			digest, turnID, turn, err)
	}
	names := make([]string, 0, len(hand.commands))
	for index := range hand.commands {
		names = append(names, string(hand.commands[index].Name))
	}
	if len(names) != 2 || names[0] != "chat.sendMessage" || names[1] != "chat.sendWechatInvite" {
		t.Fatalf("分批到达应发送正文加换微信邀请各一条: names=%v", names)
	}
	final, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil ||
		final.AutomationStatus != store.ProfileCommunicationAutomationActive ||
		final.ManualReason != "" ||
		final.ProjectedThroughSeq <= inSeq {
		t.Fatalf("分批到达后档案未保持 active 并推进游标: aggregate=%+v err=%v", final, err)
	}
}
