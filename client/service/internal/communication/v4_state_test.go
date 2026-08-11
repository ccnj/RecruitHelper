package communication

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func v4Time(hour int) *time.Time {
	at := time.Date(2026, 7, 23, hour, 0, 0, 0, time.UTC)
	return &at
}

func v4MessageEvent(key string, seq int64, kind BusinessEventKind) BusinessEvent {
	return BusinessEvent{Key: key, Kind: kind, Source: EventSourceMessage, MessageSeq: seq}
}

func TestNewV4GreetedStateFreezesInitialBudgetsAndClocks(t *testing.T) {
	at := v4Time(8)
	state := NewV4GreetedState(at)
	if state.MainStatus != V4StatusGreeted || state.WechatState != V4WechatNotInvited ||
		state.ColdPromptRemaining != 2 || state.ColdWechatRemaining != 1 || state.RealMessageRound != 1 || state.ClockUncertain ||
		state.LastOutboundAt == nil || state.LastBodyAt == nil || !state.LastOutboundAt.Equal(*at) || !state.LastBodyAt.Equal(*at) {
		t.Fatalf("招呼后初态错误: %+v", state)
	}
	*at = at.Add(time.Hour)
	if state.LastOutboundAt.Equal(*at) {
		t.Fatal("状态不应持有调用方时间指针")
	}

	unknown := NewV4GreetedState(nil)
	if !unknown.ClockUncertain || !unknown.BodyClockUncertain || unknown.LastOutboundAt != nil || unknown.LastBodyAt != nil {
		t.Fatalf("缺时钟必须禁用推算而不是猜时间: %+v", unknown)
	}
}

func TestNewV4InboundConversationStateHasNoInventedOutboundFact(t *testing.T) {
	state := NewV4InboundConversationState()
	if err := ValidateV4State(state); err != nil {
		t.Fatalf("主动来聊初态应满足统一不变量: %v", err)
	}
	if state.MainStatus != V4StatusCommunicating ||
		state.WechatState != V4WechatNotInvited ||
		state.ColdPromptRemaining != 2 ||
		state.ColdWechatRemaining != 1 ||
		state.RealMessageRound != 0 ||
		state.LastRealMessageSeq != 0 ||
		state.LastOutboundMessageSeq != 0 ||
		state.LastOutboundAt != nil ||
		state.LastBodyAt != nil ||
		!state.ClockUncertain ||
		!state.BodyClockUncertain {
		t.Fatalf("主动来聊初态伪造了招呼或时钟事实: %+v", state)
	}
}

func TestV4RealExpressionAdvancesMainlineOpensRoundAndCancelsCards(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusInvited
	state.InterviewGroups = []V4InterviewFollowupGroup{
		{MessageSeq: 10, NextStage: 1, Active: true},
		{MessageSeq: 20, NextStage: 2, Active: true},
	}
	event := v4MessageEvent("message:30", 30, EventCandidateExpressionReceived)

	decision, err := ApplyV4BusinessEvent(state, event)
	if err != nil || decision.State.MainStatus != V4StatusInvited || decision.State.RealMessageRound != 2 ||
		decision.State.LastRealMessageSeq != 30 || decision.Dialogue != V4DialogueClassifyAndReply ||
		decision.ManualReason != "" {
		t.Fatalf("真实表达未交回普通对话: decision=%+v err=%v", decision, err)
	}
	for _, group := range decision.State.InterviewGroups {
		if group.Active {
			t.Fatalf("真实表达后卡片跟催组仍活跃: %+v", decision.State.InterviewGroups)
		}
	}
	if !state.InterviewGroups[0].Active {
		t.Fatal("纯函数不应修改输入状态")
	}

	replayed, err := ApplyV4BusinessEvent(decision.State, event)
	if err != nil || replayed.State.RealMessageRound != 2 || replayed.Dialogue != V4DialogueClassifyAndReply {
		t.Fatalf("同一消息重放应复用同一轮，而不是新开轮: decision=%+v err=%v", replayed, err)
	}
}

func TestV4RealExpressionWakesEndedButNeverWakesEliminated(t *testing.T) {
	ended := NewV4GreetedState(v4Time(8))
	archiveV4State(&ended, V4EndSilent)
	decision, err := ApplyV4BusinessEvent(ended, v4MessageEvent("message:2", 2, EventCandidateExpressionReceived))
	if err != nil || decision.State.MainStatus != V4StatusCommunicating || decision.State.EndReason != "" ||
		decision.State.ColdPromptRemaining != 0 || decision.State.ColdWechatRemaining != 0 ||
		decision.Dialogue != V4DialogueClassifyAndReply {
		t.Fatalf("归档唤醒不应恢复预算: decision=%+v err=%v", decision, err)
	}

	eliminated := NewV4GreetedState(v4Time(8))
	eliminated.MainStatus = V4StatusEliminated
	eliminated.ColdPromptRemaining = 0
	eliminated.ColdWechatRemaining = 0
	decision, err = ApplyV4BusinessEvent(eliminated, v4MessageEvent("message:3", 3, EventCandidateExpressionReceived))
	if err != nil || decision.State.MainStatus != V4StatusEliminated || decision.Dialogue != V4DialogueNone ||
		decision.State.RealMessageRound != 1 {
		t.Fatalf("已淘汰档案不应被唤醒: decision=%+v err=%v", decision, err)
	}
}

func TestV4ServiceStateSkipsIntentAndTimeStateMachine(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusInterviewed
	decision, err := ApplyV4BusinessEvent(state, v4MessageEvent("message:2", 2, EventCandidateExpressionReceived))
	if err != nil || decision.State.MainStatus != V4StatusInterviewed || decision.Dialogue != V4DialogueServiceReply {
		t.Fatalf("服务态真实表达不应进入意向判断: decision=%+v err=%v", decision, err)
	}
}

func TestV4InterviewedWechatRequestAcceptsWithoutDialogueFollowup(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusInterviewed
	event := v4MessageEvent("message:2", 2, EventWechatRequested)

	decision, err := ApplyV4BusinessEvent(state, event)
	if err != nil || decision.State.MainStatus != V4StatusInterviewed ||
		decision.Dialogue != V4DialogueNone || decision.DialogueAfterActions ||
		decision.ManualReason != "" {
		t.Fatalf("服务态主动换微信不应安排 AI 对话跟随: decision=%+v err=%v", decision, err)
	}
	if len(decision.Actions) != 2 ||
		decision.Actions[0].Kind != V4ActionAcceptWechat ||
		decision.Actions[1].Kind != V4ActionNotifyWechat {
		t.Fatalf("服务态主动换微信仍必须无条件同意并通知运营: %+v", decision.Actions)
	}
	if decision.State.RealMessageRound != 2 || decision.State.LastRealMessageSeq != 2 ||
		decision.State.ColdPromptRemaining != 2 || decision.State.ColdWechatRemaining != 1 {
		t.Fatalf("主动换微信视同真实文字且不动预算: %+v", decision.State)
	}

	replayed, err := ApplyV4BusinessEvent(decision.State, event)
	if err != nil || replayed.Dialogue != V4DialogueNone || replayed.DialogueAfterActions ||
		!reflect.DeepEqual(replayed.Actions, decision.Actions) ||
		replayed.State.RealMessageRound != 2 {
		t.Fatalf("服务态换微信重放应幂等: decision=%+v err=%v", replayed, err)
	}
}

func TestV4ResumeAndWechatRequestChooseTheirOwnDeterministicBranches(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	resume, err := ApplyV4BusinessEvent(state, v4MessageEvent("message:2", 2, EventResumeSubmitted))
	if err != nil || resume.State.MainStatus != V4StatusCommunicating || resume.Dialogue != V4DialogueReplyKnownInterested ||
		len(resume.Actions) != 0 {
		t.Fatalf("投递简历应跳过意向 AI 而进入回复: decision=%+v err=%v", resume, err)
	}

	request, err := ApplyV4BusinessEvent(state, v4MessageEvent("message:3", 3, EventWechatRequested))
	if err != nil || request.State.WechatState != V4WechatNotInvited || request.Dialogue != V4DialogueWechatContinuation ||
		!request.DialogueAfterActions || len(request.Actions) != 2 ||
		request.Actions[0].Kind != V4ActionAcceptWechat || request.Actions[1].Kind != V4ActionNotifyWechat {
		t.Fatalf("主动换微信应先确定性同意再 AI 承接: decision=%+v err=%v", request, err)
	}
	repeated, err := ApplyV4BusinessEvent(request.State, v4MessageEvent("message:3", 3, EventWechatRequested))
	if err != nil || repeated.Dialogue != V4DialogueWechatContinuation || len(repeated.Actions) != 2 ||
		!reflect.DeepEqual(repeated.Actions, request.Actions) {
		t.Fatalf("主动请求重放必须重给相同稳定动作，而不是漏动作或铸新键: decision=%+v err=%v", repeated, err)
	}
}

func TestV4WechatExchangeIsMonotoneAndPlansStableReceipt(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	event := BusinessEvent{
		Key: "card:9:pending:accepted", Kind: EventWechatExchanged,
		Source: EventSourceCardTransition, MessageSeq: 9,
	}
	decision, err := ApplyV4BusinessEvent(state, event)
	if err != nil || decision.State.WechatState != V4WechatExchanged || len(decision.Actions) != 2 ||
		decision.Actions[0].Kind != V4ActionNotifyWechat || decision.Actions[1].Kind != V4ActionWechatReceipt {
		t.Fatalf("换微信成功事件动作错误: decision=%+v err=%v", decision, err)
	}
	if decision.Actions[0].ActionKey == decision.Actions[1].ActionKey {
		t.Fatal("同一事件的不同动作必须有不同稳定键")
	}

	decision.State.WechatReceiptSent = true
	replayed, err := ApplyV4BusinessEvent(decision.State, event)
	if err != nil || decision.State.WechatState != V4WechatExchanged || len(replayed.Actions) != 1 ||
		replayed.Actions[0].Kind != V4ActionNotifyWechat {
		t.Fatalf("固定回执已发后不得再规划: decision=%+v err=%v", replayed, err)
	}
}

func TestV4InterviewCardGroupsUseExactIdentityAndButtonSemantics(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusCommunicating
	invite := BusinessEvent{
		Key: "message:20", Kind: EventInterviewInvited, Source: EventSourceMessage,
		MessageSeq: 20, OccurredAt: v4Time(9),
	}
	invited, err := ApplyV4BusinessEvent(state, invite)
	if err != nil || invited.State.MainStatus != V4StatusInvited || len(invited.State.InterviewGroups) != 1 ||
		invited.State.InterviewGroups[0].MessageSeq != 20 || !invited.State.InterviewGroups[0].Active {
		t.Fatalf("实际邀面卡未建立独立跟催组: decision=%+v err=%v", invited, err)
	}

	rejectedEvent := BusinessEvent{
		Key: "card:20:pending:rejected", Kind: EventInterviewRejected,
		Source: EventSourceCardTransition, MessageSeq: 20,
	}
	rejected, err := ApplyV4BusinessEvent(invited.State, rejectedEvent)
	if err != nil || rejected.Dialogue != V4DialogueInterviewRejectionReceipt ||
		rejected.State.InterviewGroups[0].Active || !rejected.State.InterviewGroups[0].Rejected {
		t.Fatalf("拒卡未精确作废所属组并只请求回执 AI: decision=%+v err=%v", rejected, err)
	}

	missing := rejectedEvent
	missing.Key = "card:19:pending:rejected"
	missing.MessageSeq = 19
	manual, err := ApplyV4BusinessEvent(invited.State, missing)
	if err != nil || manual.ManualReason != V4ManualInterviewCardMissing || !reflect.DeepEqual(manual.State, invited.State) {
		t.Fatalf("不存在的卡不能命中相邻组: decision=%+v err=%v", manual, err)
	}
}

func TestV4InterviewAcceptanceFollowsPlatformTruthAndEntersService(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	archiveV4State(&state, V4EndSilent)
	event := BusinessEvent{
		Key: "card:8:pending:accepted", Kind: EventInterviewAccepted,
		Source: EventSourceCardTransition, MessageSeq: 8,
	}
	decision, err := ApplyV4BusinessEvent(state, event)
	if err != nil || decision.State.MainStatus != V4StatusInterviewed || decision.State.EndReason != "" ||
		len(decision.Actions) != 3 || decision.Actions[0].Kind != V4ActionInterviewAcceptedReceipt ||
		decision.Actions[1].Kind != V4ActionNotifyInterviewAccepted || decision.Actions[2].Kind != V4ActionInviteWechat {
		t.Fatalf("归档后接受旧卡没有跟随平台真相: decision=%+v err=%v", decision, err)
	}

	replayed, err := ApplyV4BusinessEvent(decision.State, event)
	if err != nil || replayed.State.MainStatus != V4StatusInterviewed || !reflect.DeepEqual(replayed.Actions, decision.Actions) {
		t.Fatalf("接受事件重放必须重给相同稳定动作供账本去重: decision=%+v err=%v", replayed, err)
	}
}

func TestV4SentFlagsAndStateAdvanceOnlyFromPositiveConfirmation(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusCommunicating
	exchange := BusinessEvent{
		Key: "card:9:pending:accepted", Kind: EventWechatExchanged,
		Source: EventSourceCardTransition, MessageSeq: 9,
	}
	planned, err := ApplyV4BusinessEvent(state, exchange)
	if err != nil || planned.State.WechatReceiptSent {
		t.Fatalf("计划固定回执时不应提前记已发: decision=%+v err=%v", planned, err)
	}

	confirmed, err := ApplyV4ConfirmedAction(planned.State, V4ConfirmedAction{
		ActionKey: "card:9:pending:accepted|wechatReceipt", Kind: V4ActionWechatReceipt,
		MessageSeq: 10, SentAt: v4Time(9),
	})
	if err != nil || !confirmed.WechatReceiptSent || confirmed.LastOutboundMessageSeq != 10 ||
		confirmed.LastOutboundAt == nil || confirmed.LastOutboundAt.Hour() != 9 ||
		confirmed.LastBodyAt == nil || confirmed.LastBodyAt.Hour() != 8 {
		t.Fatalf("唯一正证没有推进固定回执与普通锚，或错误滑动正文锚: state=%+v err=%v", confirmed, err)
	}

	replayed, err := ApplyV4ConfirmedAction(confirmed, V4ConfirmedAction{
		ActionKey: "card:9:pending:accepted|wechatReceipt", Kind: V4ActionWechatReceipt,
		MessageSeq: 10, SentAt: v4Time(9),
	})
	if err != nil || !reflect.DeepEqual(replayed, confirmed) {
		t.Fatalf("同一正证重放必须幂等: first=%+v replayed=%+v err=%v", confirmed, replayed, err)
	}
}

func TestV4RejectionClosingArchivesOnlyAfterRetentionAndClosingAreConfirmed(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusCommunicating

	if _, err := ApplyV4ConfirmedAction(state, V4ConfirmedAction{
		ActionKey: "turn:1|closing", Kind: V4ActionRejectionClosing, MessageSeq: 2, SentAt: v4Time(9),
	}); !errors.Is(err, ErrInvalidV4StateTransition) {
		t.Fatalf("没有挽留正证不能越级确认收场: %v", err)
	}

	retained, err := ApplyV4ConfirmedAction(state, V4ConfirmedAction{
		ActionKey: "turn:1|retention", Kind: V4ActionRejectionRetention, MessageSeq: 2, SentAt: v4Time(9),
	})
	if err != nil || !retained.RetentionSent || retained.MainStatus != V4StatusCommunicating {
		t.Fatalf("挽留正证推进错误: state=%+v err=%v", retained, err)
	}

	closed, err := ApplyV4ConfirmedAction(retained, V4ConfirmedAction{
		ActionKey: "turn:2|closing", Kind: V4ActionRejectionClosing, MessageSeq: 3, SentAt: v4Time(10),
	})
	if err != nil || !closed.ClosingSent || closed.MainStatus != V4StatusEnded || closed.EndReason != V4EndRejected ||
		closed.ColdPromptRemaining != 0 || closed.ColdWechatRemaining != 0 {
		t.Fatalf("收场正证没有原子收束拒绝分支: state=%+v err=%v", closed, err)
	}
}

func TestV4ConfirmedCardsAdvanceOnlyTheirOwnMonotoneFacts(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusCommunicating

	wechat, err := ApplyV4ConfirmedAction(state, V4ConfirmedAction{
		ActionKey: "turn:1|inviteWechat", Kind: V4ActionInviteWechat, MessageSeq: 2, SentAt: v4Time(9),
	})
	if err != nil || wechat.WechatState != V4WechatInvited || wechat.MainStatus != V4StatusCommunicating ||
		wechat.LastBodyAt.Hour() != 8 || wechat.LastOutboundAt.Hour() != 9 {
		t.Fatalf("微信邀请正证不应伪造正文或邀面状态: state=%+v err=%v", wechat, err)
	}

	invited, err := ApplyV4ConfirmedAction(wechat, V4ConfirmedAction{
		ActionKey: "turn:1|interviewInvite", Kind: V4ActionInterviewInvite, MessageSeq: 3, SentAt: v4Time(10),
	})
	if err != nil || invited.MainStatus != V4StatusInvited || len(invited.InterviewGroups) != 1 ||
		invited.InterviewGroups[0].MessageSeq != 3 || !invited.InterviewGroups[0].Active {
		t.Fatalf("邀面卡正证没有建立唯一跟催组: state=%+v err=%v", invited, err)
	}

	exchanged, err := ApplyV4ConfirmedAction(invited, V4ConfirmedAction{
		ActionKey: "message:4|acceptWechat", Kind: V4ActionAcceptWechat,
	})
	if err != nil || exchanged.WechatState != V4WechatExchanged || exchanged.LastOutboundMessageSeq != 3 {
		t.Fatalf("同意换微信只应推进微信线: state=%+v err=%v", exchanged, err)
	}
}

func TestV4InterviewRejectionReceiptRequiresExactRejectedCard(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusInvited
	state.InterviewGroups = []V4InterviewFollowupGroup{{MessageSeq: 20, NextStage: 1, Rejected: true}}
	confirmed, err := ApplyV4ConfirmedAction(state, V4ConfirmedAction{
		ActionKey: "card:20|reply", Kind: V4ActionInterviewRejectionReply,
		MessageSeq: 30, CardMessageSeq: 20, SentAt: v4Time(9),
	})
	if err != nil || !confirmed.InterviewGroups[0].RejectionReceiptSent {
		t.Fatalf("拒卡回执正证没有命中所属卡: state=%+v err=%v", confirmed, err)
	}

	if _, err := ApplyV4ConfirmedAction(state, V4ConfirmedAction{
		ActionKey: "card:19|reply", Kind: V4ActionInterviewRejectionReply,
		MessageSeq: 31, CardMessageSeq: 19, SentAt: v4Time(10),
	}); !errors.Is(err, ErrInvalidV4StateTransition) {
		t.Fatalf("拒卡回执不能命中相邻卡: %v", err)
	}
}

func TestV4OldConfirmationReplayCannotMoveClockBackwards(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusCommunicating
	latest, err := ApplyV4ConfirmedAction(state, V4ConfirmedAction{
		ActionKey: "turn:2|reply", Kind: V4ActionReplyText, MessageSeq: 20, SentAt: v4Time(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	replayedOld, err := ApplyV4ConfirmedAction(latest, V4ConfirmedAction{
		ActionKey: "turn:1|reply", Kind: V4ActionReplyText, MessageSeq: 10, SentAt: v4Time(9),
	})
	if err != nil || replayedOld.ClockUncertain || replayedOld.LastOutboundMessageSeq != 20 ||
		replayedOld.LastOutboundAt.Hour() != 10 || replayedOld.LastBodyAt.Hour() != 10 {
		t.Fatalf("旧正证重放不应污染当前锚点: state=%+v err=%v", replayedOld, err)
	}
}

func TestV4OutboundFactsSlideOnlyProvenClocks(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	body := BusinessEvent{
		Key: "message:2", Kind: EventHumanOutboundObserved, Source: EventSourceMessage,
		MessageSeq: 2, OccurredAt: v4Time(10), IsBody: true, BodyKindKnown: true,
	}
	decision, err := ApplyV4BusinessEvent(state, body)
	if err != nil || decision.State.LastOutboundAt == nil || decision.State.LastBodyAt == nil ||
		decision.State.LastOutboundAt.Hour() != 10 || decision.State.LastBodyAt.Hour() != 10 {
		t.Fatalf("真人正文没有滑动两个锚点: decision=%+v err=%v", decision, err)
	}

	card := body
	card.Key = "message:3"
	card.Kind = EventWechatInvited
	card.MessageSeq = 3
	card.OccurredAt = v4Time(11)
	card.IsBody = false
	decision, err = ApplyV4BusinessEvent(decision.State, card)
	if err != nil || decision.State.LastOutboundAt.Hour() != 11 || decision.State.LastBodyAt.Hour() != 10 {
		t.Fatalf("卡片只能滑动普通锚点: decision=%+v err=%v", decision, err)
	}

	unknownClock := body
	unknownClock.Key = "message:4"
	unknownClock.MessageSeq = 4
	unknownClock.OccurredAt = nil
	decision, err = ApplyV4BusinessEvent(decision.State, unknownClock)
	if err != nil || !decision.State.ClockUncertain || decision.State.LastOutboundAt.Hour() != 11 ||
		decision.State.LastBodyAt.Hour() != 10 {
		t.Fatalf("缺时刻只能禁用调度，不能伪造锚点: decision=%+v err=%v", decision, err)
	}
}

func TestV4AutomaticOutboundWithoutActionSemanticMakesOnlyBodyClockUncertain(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	event, err := NormalizeLedgerMessage(LedgerMessageFact{
		Seq: 2, Direction: "out", Kind: "text", Text: textPointer("无法分类的自动文本"),
		Origin: "self", TsApproxMs: func() *int64 { value := v4Time(9).UnixMilli(); return &value }(),
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := ApplyV4BusinessEvent(state, event)
	if err != nil || decision.State.ClockUncertain || !decision.State.BodyClockUncertain ||
		decision.State.LastOutboundAt.Hour() != 9 || decision.State.LastBodyAt.Hour() != 8 {
		t.Fatalf("缺脑侧动作语义时不得把普通文本外形猜成正文: state=%+v err=%v", decision.State, err)
	}
}

func TestV4BlacklistAndUnknownEventsNeverEscapeTheirBoundaries(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	blacklisted, err := ApplyV4BusinessEvent(state, BusinessEvent{
		Key: "status:blacklisted:1", Kind: EventCandidateBlacklisted, Source: EventSourcePlatformStatus,
	})
	if err != nil || blacklisted.State.MainStatus != V4StatusEnded || blacklisted.State.EndReason != V4EndBlacklisted ||
		blacklisted.State.ColdPromptRemaining != 0 || blacklisted.State.ColdWechatRemaining != 0 {
		t.Fatalf("推进态拉黑未归档并清预算: decision=%+v err=%v", blacklisted, err)
	}

	unknown, err := ApplyV4BusinessEvent(state, BusinessEvent{
		Key: "message:99", Kind: EventUnknownPlatform, Source: EventSourceMessage, MessageSeq: 99,
	})
	if err != nil || unknown.ManualReason != V4ManualUnknownPlatformEvent || unknown.Dialogue != V4DialogueNone ||
		len(unknown.Actions) != 0 || !reflect.DeepEqual(unknown.State, state) {
		t.Fatalf("未知平台事件不应获得自动权限: decision=%+v err=%v", unknown, err)
	}
}

func TestV4StateRejectsBrokenAggregateAndEventFacts(t *testing.T) {
	broken := NewV4GreetedState(v4Time(8))
	broken.WechatState = V4WechatStatus("future")
	if _, err := ApplyV4BusinessEvent(broken, v4MessageEvent("message:1", 1, EventSystemNotice)); !errors.Is(err, ErrInvalidV4StateTransition) {
		t.Fatalf("非法聚合没有响亮失败: %v", err)
	}

	state := NewV4GreetedState(v4Time(8))
	invalidEvents := []BusinessEvent{
		{Kind: EventSystemNotice, Source: EventSourceMessage, MessageSeq: 1},
		{Key: "message:0", Kind: EventCandidateExpressionReceived, Source: EventSourceMessage},
		{Key: "future:1", Kind: BusinessEventKind("future"), Source: EventSourceMessage},
	}
	for index, event := range invalidEvents {
		if _, err := ApplyV4BusinessEvent(state, event); !errors.Is(err, ErrInvalidV4StateTransition) {
			t.Fatalf("非法事件[%d]没有响亮失败: %v", index, err)
		}
	}
}

// 点拒绝换微信卡(规格事件表,2026-08-11 甲方裁决):仅推进态且线为已邀请时
// 降「已拒」,其余无操作;不产动作、不滑锚。已拒后交换成功仍可推进已换号。
func TestV4WechatRejectedEventOnlyDowngradesInvitedProgressState(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusCommunicating
	state.WechatState = V4WechatInvited
	decision, err := ApplyV4BusinessEvent(state, v4MessageEvent("message:9", 9, EventWechatRejected))
	if err != nil || decision.State.WechatState != V4WechatRejected || len(decision.Actions) != 0 ||
		decision.Dialogue != V4DialogueNone || decision.State.LastOutboundMessageSeq != 0 {
		t.Fatalf("已邀请+推进态应降已拒且零动作零滑锚: decision=%+v err=%v", decision, err)
	}

	notInvited := NewV4GreetedState(v4Time(8))
	notInvited.MainStatus = V4StatusCommunicating
	decision, err = ApplyV4BusinessEvent(notInvited, v4MessageEvent("message:9", 9, EventWechatRejected))
	if err != nil || decision.State.WechatState != V4WechatNotInvited {
		t.Fatalf("未邀请不得凭拒收事件推进微信线: decision=%+v err=%v", decision, err)
	}

	interviewed := NewV4GreetedState(v4Time(8))
	interviewed.MainStatus = V4StatusInterviewed
	interviewed.WechatState = V4WechatInvited
	decision, err = ApplyV4BusinessEvent(interviewed, v4MessageEvent("message:9", 9, EventWechatRejected))
	if err != nil || decision.State.WechatState != V4WechatInvited {
		t.Fatalf("服务态按 §七 无操作: decision=%+v err=%v", decision, err)
	}

	rejected := NewV4GreetedState(v4Time(8))
	rejected.MainStatus = V4StatusCommunicating
	rejected.WechatState = V4WechatRejected
	decision, err = ApplyV4BusinessEvent(rejected, v4MessageEvent("message:10", 10, EventWechatExchanged))
	if err != nil || decision.State.WechatState != V4WechatExchanged {
		t.Fatalf("已拒后对方自发起交换成功仍应推进已换号: decision=%+v err=%v", decision, err)
	}
}

func TestV4SilenceEndReasonWechatRejected(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusCommunicating
	state.WechatState = V4WechatRejected
	if got := v4SilenceEndReason(state); got != V4EndSilentWechatRejected {
		t.Fatalf("已拒线的沉默归档原因应为 silentWechatRejected: %s", got)
	}
	archived := state
	archiveV4State(&archived, V4EndSilentWechatRejected)
	if err := validateV4State(archived); err != nil {
		t.Fatalf("沉默-邀微已拒归档态必须通过校验: %v", err)
	}
}
