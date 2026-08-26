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

// appendCommunicationV4CandidateText 在会话账本尾部追加一条候选人文字消息,
// 返回新行 seq。suffix 必须与 seedCommunicationV4PatrolTarget 的 suffix 一致。
func appendCommunicationV4CandidateText(
	t *testing.T,
	h *harness,
	fixture communicationV4PatrolFixture,
	suffix string,
	tailSeq int64,
	text string,
) int64 {
	t.Helper()
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: store.ConversationKey{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ConversationRef: fixture.conversationRef,
		},
		ExpectedTailSeq: tailSeq,
		PlatformUserRef: "person-v4-patrol-" + suffix,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text",
			ContentHash: syncledger.HashText(text), Text: &text, Origin: "external",
		}},
		SyncedAt: h.clock.Now(),
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加候选人消息失败: changes=%+v err=%v", changes, err)
	}
	return changes.Inserted[0].Seq
}

// 停机点体检战役第 4 族回归其一(2026-08-02 甲方裁决,规格 v4 §一"旧轮失效"):
// 轮悬置期间候选人插话(AI 调用在途时账本长出新 in 行),旧轮连同其未发建议
// 一律作废,候选人聚合保持 active;下一巡检轮按最新账本边界重开新轮,正常出
// 建议并发送。拆腿前该序列会把 turn 判 inputBoundaryChanged 并终身冻结候选人。
func TestCommunicationV4PatrolSupersedesStaleTurnAndReopensNextRound(t *testing.T) {
	h := newHarness(t)
	suffix := "supersede-reopen"
	fixture := seedCommunicationV4PatrolTarget(t, h, suffix, "我想了解一下岗位")

	interjected := false
	var interjectSeq int64
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				if !interjected {
					// 模拟模型在途时候选人插话:意向调用返回前账本长出新行。
					interjected = true
					interjectSeq = appendCommunicationV4CandidateText(
						t, h, fixture, suffix, fixture.inboundSeq, "补充一下,我今晚方便细聊",
					)
				}
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				return safeFakeResponse(`{"话术_序列":["重开后的合成回复"],"动作":"无"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
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

	beginCommunicationV4PatrolRound(t, h, "round-supersede-1")
	first := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: "round-supersede-1", now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = first.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	staleTurn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || staleTurn == nil || staleTurn.Status != store.DialogueTurnSuperseded ||
		staleTurn.FailureReason != "boundarySuperseded" ||
		staleTurn.InboundThroughSeq != fixture.inboundSeq {
		t.Fatalf("插话后旧轮未作废: turn=%+v err=%v", staleTurn, err)
	}
	if hand.commandCount() != 0 {
		t.Fatalf("作废轮不得发送: sends=%d", hand.commandCount())
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" {
		t.Fatalf("插话作废不得冻结聚合: aggregate=%+v err=%v", aggregate, err)
	}

	beginCommunicationV4PatrolRound(t, h, "round-supersede-2")
	second := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: "round-supersede-2", now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = second.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	// 2026-08-27 停机点第二步:边界按 v4 §一纯定义现算(锚后全部候选人
	// 消息),重开轮把被作废旧轮消费过的输入与插话并成一轮一并回应——
	// 「多条新输入并一响应」跨作废成立,插话不再被切出旧输入单独作答。
	if err != nil || reopened == nil || reopened.TurnID == staleTurn.TurnID ||
		reopened.Status != store.DialogueTurnCompleted ||
		reopened.InboundFromSeq != fixture.inboundSeq ||
		reopened.InboundThroughSeq != interjectSeq {
		t.Fatalf("下一轮未按最新账本边界重开: turn=%+v err=%v", reopened, err)
	}
	if unchanged, err := h.db.DialogueTurnByID(staleTurn.TurnID); err != nil ||
		unchanged == nil || unchanged.Status != store.DialogueTurnSuperseded {
		t.Fatalf("重开不得复活旧轮: turn=%+v err=%v", unchanged, err)
	}
	if countM5SendMessageCommands(t, h) != 1 {
		t.Fatalf("重开后的新轮应恰好发送一条正文: sends=%d", countM5SendMessageCommands(t, h))
	}
	final, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil ||
		final.AutomationStatus != store.ProfileCommunicationAutomationActive ||
		final.ManualReason != "" ||
		final.ProjectedThroughSeq <= interjectSeq {
		t.Fatalf("重开后聚合未保持 active 并推进游标: aggregate=%+v err=%v", final, err)
	}
}

// 第 4 族回归其二:停靠轮(纯计算失败族 manualRequired,无任何 effect 动作)
// 不再终身卡死候选人——没有新输入时保持停靠原状(不作废、不跑时刻表),新
// 输入到达时由开轮闸在冻结事务内作废停靠轮并重开新轮。
func TestCommunicationV4PatrolReopensParkedTurnOnNewCandidateInput(t *testing.T) {
	h := newHarness(t)
	suffix := "parked-reopen"
	fixture := seedCommunicationV4PatrolTarget(t, h, suffix, "岗位还招人吗")

	replyBlocked := true
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				if replyBlocked {
					return m5ai.CompletionResponse{}, fmt.Errorf("合成 provider 故障")
				}
				return safeFakeResponse(`{"话术_序列":["重开后的合成回复"],"动作":"无"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
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
	runRound := func(roundID string) {
		t.Helper()
		beginCommunicationV4PatrolRound(t, h, roundID)
		actor := &roundActor{
			manager: manager, account: account,
			hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
			roundID: roundID, now: h.clock.Now(),
		}
		manager.mu.Lock()
		err := actor.processCommunicationV4Targets(context.Background())
		manager.mu.Unlock()
		if err != nil {
			t.Fatalf("巡检轮 %s 失败: %v", roundID, err)
		}
	}

	// 轮 1:建轮、意向成功、回复调用失败跳过,turn 停在 classified。
	runRound("round-parked-1")
	parked, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || parked == nil || parked.Status != store.DialogueTurnClassified {
		t.Fatalf("轮 1 未形成待建议 turn: turn=%+v err=%v", parked, err)
	}
	// 以豁免原因停靠(等价于预算类失败的第 3 族停靠形态):turn manualRequired,
	// 聚合保持 active。
	if err := h.db.MarkDialogueTurnManualRequired(
		parked.TurnID, "inputBudgetBlocked", h.clock.Now(),
	); err != nil {
		t.Fatal(err)
	}
	if aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID); err != nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
		t.Fatalf("停靠前提不成立(聚合应保持 active): aggregate=%+v err=%v", aggregate, err)
	}

	// 轮 2:没有新输入,停靠轮保持原状——不作废、不开新轮、不发送。
	runRound("round-parked-2")
	still, err := h.db.DialogueTurnByID(parked.TurnID)
	if err != nil || still == nil || still.Status != store.DialogueTurnManualRequired ||
		still.FailureReason != "inputBudgetBlocked" {
		t.Fatalf("无新输入不得触碰停靠轮: turn=%+v err=%v", still, err)
	}
	if latest, err := h.db.LatestDialogueTurnForProfile(fixture.profileID); err != nil ||
		latest == nil || latest.TurnID != parked.TurnID {
		t.Fatalf("无新输入不得开新轮: turn=%+v err=%v", latest, err)
	}
	if hand.commandCount() != 0 {
		t.Fatalf("停靠期间不得发送: sends=%d", hand.commandCount())
	}

	// 轮 3:候选人再次开口,开轮闸作废停靠轮并重开新轮,建议与发送照常。
	replyBlocked = false
	newSeq := appendCommunicationV4CandidateText(
		t, h, fixture, suffix, fixture.inboundSeq, "我改主意了,想约个时间聊聊",
	)
	runRound("round-parked-3")
	superseded, err := h.db.DialogueTurnByID(parked.TurnID)
	if err != nil || superseded == nil || superseded.Status != store.DialogueTurnSuperseded ||
		superseded.FailureReason != "boundarySuperseded" {
		t.Fatalf("新输入未作废停靠轮: turn=%+v err=%v", superseded, err)
	}
	reopened, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	// 2026-08-27 停机点第二步:重开轮按纯定义边界并入停靠轮消费过的输入,
	// 与新输入一并回应(并一响应跨作废)。
	if err != nil || reopened == nil || reopened.TurnID == parked.TurnID ||
		reopened.Status != store.DialogueTurnCompleted ||
		reopened.InboundFromSeq != parked.InboundFromSeq ||
		reopened.InboundThroughSeq != newSeq {
		t.Fatalf("停靠轮作废后未按最新边界重开: turn=%+v err=%v", reopened, err)
	}
	if countM5SendMessageCommands(t, h) != 1 {
		t.Fatalf("重开后的新轮应恰好发送一条正文: sends=%d", countM5SendMessageCommands(t, h))
	}
	final, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil ||
		final.AutomationStatus != store.ProfileCommunicationAutomationActive ||
		final.ManualReason != "" ||
		final.ProjectedThroughSeq <= newSeq {
		t.Fatalf("重开后聚合未保持 active 并推进游标: aggregate=%+v err=%v", final, err)
	}
}
