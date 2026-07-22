package communication

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

func scheduleAt(state V4State, after time.Duration) time.Time {
	return state.LastOutboundAt.Add(after)
}

func v4ScheduleInput(state V4State, now time.Time) V4ScheduleInput {
	return V4ScheduleInput{
		ProfileKey: "profile-fixture", State: state, Now: now,
		Reply: adviceReplyAbsent(), FixedPhrases: availableV4FixedPhrases(),
		InterviewFollowupTexts: map[uint8]string{1: "跟催一", 2: "跟催二", 3: "跟催三"},
	}
}

func adviceReplyAbsent() ReplyAdvice {
	return ReplyAdvice{State: AdviceAbsent}
}

func TestV4ScheduleDoesNothingBeforeFirstThreshold(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	input := v4ScheduleInput(state, scheduleAt(state, 23*time.Hour+59*time.Minute))
	decision, err := EvaluateV4Schedule(input)
	if err != nil || decision.Status != V4ScheduleNoAction || decision.NextAdvice != V4AdviceNone ||
		len(decision.Actions) != 0 || !reflect.DeepEqual(decision.State, state) {
		t.Fatalf("未到下界不应计划动作或修改预算: decision=%+v err=%v", decision, err)
	}
}

func TestV4ScheduleSevenDayFallbackWinsEvenWithPendingDialogueAndRecentCard(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusCommunicating
	cardAt := state.LastBodyAt.Add(6 * 24 * time.Hour)
	cardEvent := BusinessEvent{
		Key: "message:2", Kind: EventWechatInvited, Source: EventSourceMessage,
		MessageSeq: 2, OccurredAt: &cardAt,
	}
	withCard, err := ApplyV4BusinessEvent(state, cardEvent)
	if err != nil {
		t.Fatal(err)
	}
	input := v4ScheduleInput(withCard.State, state.LastBodyAt.Add(7*24*time.Hour))
	input.HasPendingDialogue = true
	decision, err := EvaluateV4Schedule(input)
	if err != nil || decision.Status != V4ScheduleActionsPlanned || len(decision.Actions) != 1 ||
		decision.Actions[0].Kind != V4ActionArchive || decision.Actions[0].EndReason != V4EndFallback {
		t.Fatalf("7 天正文兜底没有先于未回应消息与最近卡片: decision=%+v err=%v", decision, err)
	}
	if decision.State.MainStatus != V4StatusCommunicating {
		t.Fatalf("计划归档时不应提前推进状态: %+v", decision.State)
	}
}

func TestV4SchedulePendingDialogueSuppressesAllNonFallbackTiers(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	input := v4ScheduleInput(state, scheduleAt(state, 3*24*time.Hour))
	input.HasPendingDialogue = true
	decision, err := EvaluateV4Schedule(input)
	if err != nil || decision.Status != V4ScheduleNoAction || len(decision.Actions) != 0 {
		t.Fatalf("未回应真实表达存在时不得跑普通时刻表: decision=%+v err=%v", decision, err)
	}
}

func TestV4ScheduleInterviewFollowupsArePerCardSequentialAndLowestFirst(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusInvited
	state.InterviewGroups = []V4InterviewFollowupGroup{
		{MessageSeq: 20, NextStage: 2, Active: true},
		{MessageSeq: 30, NextStage: 1, Active: true},
	}
	input := v4ScheduleInput(state, scheduleAt(state, 10*time.Minute))
	decision, err := EvaluateV4Schedule(input)
	if err != nil || decision.Status != V4ScheduleActionsPlanned || len(decision.Actions) != 1 ||
		decision.Actions[0].Kind != V4ActionInterviewFollowup || decision.Actions[0].CardMessageSeq != 30 ||
		decision.Actions[0].Stage != 1 || decision.Actions[0].Text != "跟催一" {
		t.Fatalf("没有选当前最低档并精确绑定所属卡: decision=%+v err=%v", decision, err)
	}
	if decision.State.InterviewGroups[1].NextStage != 1 {
		t.Fatal("计划阶段不应提前花掉跟催赠额")
	}

	confirmed, err := ApplyV4ConfirmedAction(decision.State, V4ConfirmedAction{
		ActionKey: decision.Actions[0].ActionKey, Kind: V4ActionInterviewFollowup,
		MessageSeq: 40, CardMessageSeq: 30, Stage: 1, SentAt: &input.Now,
	})
	if err != nil || confirmed.InterviewGroups[1].NextStage != 2 || confirmed.LastOutboundMessageSeq != 40 {
		t.Fatalf("跟催正证没有推进所属组: state=%+v err=%v", confirmed, err)
	}

	replayed, err := ApplyV4ConfirmedAction(confirmed, V4ConfirmedAction{
		ActionKey: decision.Actions[0].ActionKey, Kind: V4ActionInterviewFollowup,
		MessageSeq: 40, CardMessageSeq: 30, Stage: 1, SentAt: &input.Now,
	})
	if err != nil || !reflect.DeepEqual(replayed, confirmed) {
		t.Fatalf("跟催正证重放不幂等: first=%+v replayed=%+v err=%v", confirmed, replayed, err)
	}
}

func TestV4ScheduleMissingThreeStageCopyStopsOnlyDueFollowup(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusInvited
	state.InterviewGroups = []V4InterviewFollowupGroup{{MessageSeq: 20, NextStage: 1, Active: true}}
	input := v4ScheduleInput(state, scheduleAt(state, 10*time.Minute))
	input.InterviewFollowupTexts = nil
	decision, err := EvaluateV4Schedule(input)
	if err != nil || decision.Status != V4ScheduleManualRequired ||
		decision.ManualReason != V4ManualFollowupPhraseUnavailable || len(decision.Actions) != 0 {
		t.Fatalf("没有三档运营文案时不能复用一条旧话术三次: decision=%+v err=%v", decision, err)
	}
}

func TestV4ScheduleColdLadderUsesAIThenFixedTextAndInvite(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	input := v4ScheduleInput(state, scheduleAt(state, 24*time.Hour))
	waiting, err := EvaluateV4Schedule(input)
	if err != nil || waiting.Status != V4ScheduleWaitingAdvice || waiting.NextAdvice != V4AdviceSilenceFollowup ||
		len(waiting.Actions) != 0 {
		t.Fatalf("催1 到期没有只申请沉默追问 AI: decision=%+v err=%v", waiting, err)
	}

	input.Reply = ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "最近方便看看这个机会吗？"}}
	coldOne, err := EvaluateV4Schedule(input)
	if err != nil || coldOne.Status != V4ScheduleActionsPlanned || len(coldOne.Actions) != 1 ||
		coldOne.Actions[0].Kind != V4ActionColdPrompt || coldOne.Actions[0].Round != 1 || coldOne.Actions[0].Stage != 1 ||
		coldOne.State.ColdPromptRemaining != 2 {
		t.Fatalf("催1 建议没有形成单一动作，或计划提前扣预算: decision=%+v err=%v", coldOne, err)
	}
	confirmedOne, err := ApplyV4ConfirmedAction(coldOne.State, V4ConfirmedAction{
		ActionKey: coldOne.Actions[0].ActionKey, Kind: V4ActionColdPrompt,
		MessageSeq: 2, SentAt: &input.Now, Round: 1, Stage: 1,
	})
	if err != nil || confirmedOne.ColdPromptRemaining != 1 || confirmedOne.ColdPromptSentCount != 1 ||
		confirmedOne.LastColdPromptRound != 1 {
		t.Fatalf("催1 正证没有扣终身预算并标本轮: state=%+v err=%v", confirmedOne, err)
	}

	secondDue := confirmedOne.LastOutboundAt.Add(24 * time.Hour)
	secondInput := v4ScheduleInput(confirmedOne, secondDue)
	coldTwo, err := EvaluateV4Schedule(secondInput)
	if err != nil || coldTwo.Status != V4ScheduleActionsPlanned || coldTwo.NextAdvice != V4AdviceNone ||
		len(coldTwo.Actions) != 2 || coldTwo.Actions[0].Kind != V4ActionColdWechatText ||
		coldTwo.Actions[1].Kind != V4ActionColdWechatInvite {
		t.Fatalf("同一轮催1用过后没有进入固定催2+邀请: decision=%+v err=%v", coldTwo, err)
	}

	textState, err := ApplyV4ConfirmedAction(coldTwo.State, V4ConfirmedAction{
		ActionKey: coldTwo.Actions[0].ActionKey, Kind: V4ActionColdWechatText,
		MessageSeq: 3, SentAt: &secondDue,
	})
	if err != nil || !textState.ColdWechatTextSent || textState.ColdWechatRemaining != 1 {
		t.Fatalf("催2正文正证不应提前消费邀请预算: state=%+v err=%v", textState, err)
	}
	inviteAt := secondDue.Add(time.Second)
	invited, err := ApplyV4ConfirmedAction(textState, V4ConfirmedAction{
		ActionKey: coldTwo.Actions[1].ActionKey, Kind: V4ActionColdWechatInvite,
		MessageSeq: 4, SentAt: &inviteAt,
	})
	if err != nil || invited.ColdWechatRemaining != 0 || invited.WechatState != V4WechatInvited {
		t.Fatalf("催2邀请正证没有消费预算并推进微信线: state=%+v err=%v", invited, err)
	}

	archiveInput := v4ScheduleInput(invited, invited.LastOutboundAt.Add(36*time.Hour))
	archive, err := EvaluateV4Schedule(archiveInput)
	if err != nil || len(archive.Actions) != 1 || archive.Actions[0].Kind != V4ActionArchive ||
		archive.Actions[0].EndReason != V4EndSilentWechatInvited {
		t.Fatalf("无可用催后没有按微信线归档: decision=%+v err=%v", archive, err)
	}
}

func TestV4ScheduleNewRealRoundUnlocksOnlySecondColdPrompt(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	firstDue := state.LastOutboundAt.Add(24 * time.Hour)
	first, err := ApplyV4ConfirmedAction(state, V4ConfirmedAction{
		ActionKey: "profile|cold|1", Kind: V4ActionColdPrompt, MessageSeq: 2,
		SentAt: &firstDue, Round: 1, Stage: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	real, err := ApplyV4BusinessEvent(first, v4MessageEvent("message:3", 3, EventCandidateExpressionReceived))
	if err != nil || real.State.RealMessageRound != 2 {
		t.Fatalf("真实表达没有开新计数轮: decision=%+v err=%v", real, err)
	}
	replyAt := firstDue.Add(time.Minute)
	withReply, err := ApplyV4ConfirmedAction(real.State, V4ConfirmedAction{
		ActionKey: "turn:2|reply", Kind: V4ActionReplyText, MessageSeq: 4, SentAt: &replyAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := v4ScheduleInput(withReply, withReply.LastOutboundAt.Add(24*time.Hour))
	input.Reply = ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "如果方便，我们可以再聊聊。"}}
	second, err := EvaluateV4Schedule(input)
	if err != nil || len(second.Actions) != 1 || second.Actions[0].Kind != V4ActionColdPrompt ||
		second.Actions[0].Round != 2 || second.Actions[0].Stage != 2 {
		t.Fatalf("新轮没有只解锁终身第二次催1: decision=%+v err=%v", second, err)
	}
}

func TestV4ScheduleArchiveReasonPriorityFollowsV4(t *testing.T) {
	cases := []struct {
		name   string
		status V4MainStatus
		wechat V4WechatStatus
		want   V4EndReason
	}{
		{name: "interview first", status: V4StatusInvited, wechat: V4WechatExchanged, want: V4EndSilentInterview},
		{name: "wechat invited", status: V4StatusCommunicating, wechat: V4WechatInvited, want: V4EndSilentWechatInvited},
		{name: "wechat exchanged", status: V4StatusCommunicating, wechat: V4WechatExchanged, want: V4EndSilentWechatExchanged},
		{name: "plain", status: V4StatusCommunicating, wechat: V4WechatNotInvited, want: V4EndSilent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := NewV4GreetedState(v4Time(8))
			state.MainStatus = tc.status
			state.WechatState = tc.wechat
			state.ColdPromptRemaining = 0
			state.ColdWechatRemaining = 0
			decision, err := EvaluateV4Schedule(v4ScheduleInput(state, state.LastOutboundAt.Add(36*time.Hour)))
			if err != nil || len(decision.Actions) != 1 || decision.Actions[0].EndReason != tc.want {
				t.Fatalf("归档原因优先级错误: decision=%+v err=%v", decision, err)
			}
		})
	}
}

func TestV4ScheduleUnknownClockNeverBecomesFailureOrAutomaticAction(t *testing.T) {
	state := NewV4GreetedState(nil)
	decision, err := EvaluateV4Schedule(v4ScheduleInput(state, time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)))
	if err != nil || decision.Status != V4ScheduleManualRequired || decision.ManualReason != V4ManualScheduleClockUnknown ||
		len(decision.Actions) != 0 {
		t.Fatalf("缺时钟只能未确认转人工: decision=%+v err=%v", decision, err)
	}

	known := NewV4GreetedState(v4Time(8))
	missingCard := v4MessageEvent("message:2", 2, EventWechatInvited)
	withMissing, err := ApplyV4BusinessEvent(known, missingCard)
	if err != nil || !withMissing.State.ClockUncertain || withMissing.State.BodyClockUncertain {
		t.Fatalf("缺卡片时刻只应污染普通锚: state=%+v err=%v", withMissing.State, err)
	}
	knownCardAt := known.LastOutboundAt.Add(time.Hour)
	missingCard.OccurredAt = &knownCardAt
	recovered, err := ApplyV4BusinessEvent(withMissing.State, missingCard)
	if err != nil || recovered.State.ClockUncertain || recovered.State.BodyClockUncertain {
		t.Fatalf("同一最新卡片取得已知时刻后应恢复普通锚: state=%+v err=%v", recovered.State, err)
	}
}

func TestV4ScheduleServiceAndArchivedStatesNeverRunClocks(t *testing.T) {
	for _, status := range []V4MainStatus{V4StatusInterviewed, V4StatusEliminated, V4StatusEnded} {
		state := NewV4GreetedState(nil)
		state.MainStatus = status
		state.ColdPromptRemaining = 0
		state.ColdWechatRemaining = 0
		if status == V4StatusEnded {
			state.EndReason = V4EndSilent
		}
		decision, err := EvaluateV4Schedule(v4ScheduleInput(state, time.Now().UTC()))
		if err != nil || decision.Status != V4ScheduleNoAction || len(decision.Actions) != 0 {
			t.Fatalf("非推进态不应因缺时钟启动调度 status=%s decision=%+v err=%v", status, decision, err)
		}
	}
}

func TestV4ArchiveActionIsInternalIdempotentAndRejectsReasonConflict(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.ColdPromptRemaining = 0
	state.ColdWechatRemaining = 0
	decision, err := EvaluateV4Schedule(v4ScheduleInput(state, state.LastOutboundAt.Add(36*time.Hour)))
	if err != nil || len(decision.Actions) != 1 {
		t.Fatalf("没有得到归档动作: decision=%+v err=%v", decision, err)
	}
	archived, err := ApplyV4ArchiveAction(state, decision.Actions[0])
	if err != nil || archived.MainStatus != V4StatusEnded || archived.EndReason != V4EndSilent {
		t.Fatalf("内部归档动作没有收束状态: state=%+v err=%v", archived, err)
	}
	replayed, err := ApplyV4ArchiveAction(archived, decision.Actions[0])
	if err != nil || !reflect.DeepEqual(replayed, archived) {
		t.Fatalf("归档动作重放不幂等: first=%+v replayed=%+v err=%v", archived, replayed, err)
	}
	conflict := decision.Actions[0]
	conflict.EndReason = V4EndBlacklisted
	if _, err := ApplyV4ArchiveAction(archived, conflict); !errors.Is(err, ErrInvalidV4StateTransition) {
		t.Fatalf("归档原因冲突没有响亮失败: %v", err)
	}
}

func TestV4ScheduleRejectsAdviceWhenCurrentTierDoesNotAuthorizeAI(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	input := v4ScheduleInput(state, state.LastOutboundAt.Add(time.Hour))
	input.Reply = ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "不应消费"}}
	if _, err := EvaluateV4Schedule(input); !errors.Is(err, ErrInvalidV4StateTransition) {
		t.Fatalf("未到 AI 档位却携带建议没有响亮失败: %v", err)
	}
}
