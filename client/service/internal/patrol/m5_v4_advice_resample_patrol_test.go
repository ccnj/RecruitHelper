package patrol

import (
	"context"
	"fmt"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

// 复刻真机 turn-700458e4(吴先生案)的形状:回复建议动作=发起线上会议,会议
// 时间不命中冻结时段。规格 v4 §五/§一要求本轮零动作、跳过该候选人、下轮巡检
// 重采;此前实现在结算层第 1 次尝试即停靠 manualRequired(replyInvalid),
// 候选人不再开口就永远收不到回复。
func TestCommunicationV4PatrolResamplesInvalidMeetingAdviceThenDispatchesNextRound(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(
		t,
		h,
		"advice-resample",
		"明天下午方便面试",
	)
	slots := m5ai.GenerateDefaultSlots(h.clock.Now())
	if len(slots) == 0 {
		t.Fatal("测试时钟没有生成可约面时段")
	}
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := time.ParseInLocation("2006-01-02 15:04:05", slots[0], shanghai)
	if err != nil {
		t.Fatal(err)
	}
	validTime := fmt.Sprintf(
		"%d月%d日%s",
		selected.Month(), selected.Day(), selected.Format("15:04"),
	)
	// 偏移一分钟:冻结时段全为整点,该时刻真不在列表里,不落入 b809c20 已修
	// 的同时刻写法差异。
	shifted := selected.Add(time.Minute)
	invalidTime := fmt.Sprintf(
		"%d月%d日%s",
		shifted.Month(), shifted.Day(), shifted.Format("15:04"),
	)
	replyCalls := 0
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				replyCalls++
				meetingTime := invalidTime
				if replyCalls > 1 {
					meetingTime = validTime
				}
				return safeFakeResponse(fmt.Sprintf(
					`{"话术_序列":["那我们约在这个时间视频面试。"],"动作":"发起线上会议","会议时间":%q}`,
					meetingTime,
				)), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	h.config.InteractionPaceWait = func(ctx context.Context) error { return ctx.Err() }
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-advice-resample"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}

	// 首轮:意向 + 第 1 次回复采样(越界),结算判非法→样本作废、零命令、
	// 轮留在 classified,巡检轮整体正常结束(跳过不是错误)。
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 || hand.commandCount() != 0 {
		t.Fatalf("首轮应止步于结算重采: err=%v advice=%d sends=%d",
			err, len(advice.requests), hand.commandCount())
	}
	turn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || turn == nil ||
		turn.Status != store.DialogueTurnClassified || turn.FailureReason != "" {
		t.Fatalf("重采样本不得留下轮终局: turn=%+v err=%v", turn, err)
	}
	actions, err := h.db.CommunicationActionsByTurn(turn.TurnID)
	if err != nil || len(actions) != 0 {
		t.Fatalf("重采样本不得留下动作: actions=%+v err=%v", actions, err)
	}
	invocations, err := h.db.AIInvocationsForTurn(turn.TurnID)
	if err != nil || len(invocations) != 2 ||
		invocations[1].Purpose != m5ai.PurposeReply ||
		invocations[1].Attempt != 1 || invocations[1].FinishedAt == nil {
		t.Fatalf("第 1 次采样必须落失败待重采形态: invocations=%+v err=%v", invocations, err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
		t.Fatalf("重采期间聚合必须保持 active: aggregate=%+v err=%v", aggregate, err)
	}

	// 下一巡检轮:attempt 游走到第 2 次采样,输出合法→正文+邀面卡照常派发。
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil || len(advice.requests) != 3 || hand.commandCount() != 2 {
		t.Fatalf("下轮重采未走到派发: err=%v advice=%d sends=%d",
			err, len(advice.requests), hand.commandCount())
	}
	turn, err = h.db.DialogueTurnByID(turn.TurnID)
	aggregate, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
		aggregateErr != nil ||
		aggregate.State.MainStatus != communication.V4StatusInvited ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
		t.Fatalf("重采后的合法建议未完成派发: turn=%+v aggregate=%+v err=%v aggregateErr=%v",
			turn, aggregate, err, aggregateErr)
	}
	actions, err = h.db.CommunicationActionsByTurn(turn.TurnID)
	if err != nil || len(actions) != 2 ||
		actions[0].Kind != store.CommunicationActionReplyText ||
		actions[0].Status != store.CommunicationActionSent ||
		actions[1].Kind != store.CommunicationActionInterviewInvite ||
		actions[1].Status != store.CommunicationActionSent ||
		actions[1].InterviewStartsAtMs == nil ||
		*actions[1].InterviewStartsAtMs != selected.UnixMilli() {
		t.Fatalf("重采后的邀面组合动作错误: actions=%+v err=%v", actions, err)
	}
}
