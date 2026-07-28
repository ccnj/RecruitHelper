package communication

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

func v4InboundText(seq int64, text string) LedgerMessageFact {
	return LedgerMessageFact{
		Seq: seq, Direction: "in", Kind: "text", Text: textPointer(text), Origin: "external",
	}
}

func TestV4InboundTurnClosesLedgerToDeterministicAdviceAndAction(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	input := V4InboundTurnInput{
		State: state, TurnID: "turn-ledger-ordinary",
		Messages: []LedgerMessageFact{
			v4InboundText(2, "您好，我想了解一下"),
			v4InboundText(3, "工作地点在哪里？"),
		},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	}
	waiting, err := ReduceV4InboundTurn(input)
	if err != nil || waiting.State.MainStatus != V4StatusCommunicating || waiting.State.RealMessageRound != 2 ||
		waiting.State.LastRealMessageSeq != 3 || waiting.Dialogue.Status != V4DialogueWaitingAdvice ||
		waiting.Dialogue.NextAdvice != V4AdviceIntent ||
		waiting.Requirement != V4DialogueClassifyAndReply || len(waiting.EventActions) != 0 {
		t.Fatalf("合法多消息轮没有归一化为单一计数轮与 intent 权限: decision=%+v err=%v", waiting, err)
	}

	replayInput := input
	replayInput.State = waiting.State
	replayed, err := ReduceV4InboundTurn(replayInput)
	if err != nil || replayed.State.RealMessageRound != 2 || replayed.State.LastRealMessageSeq != 3 ||
		replayed.Dialogue.NextAdvice != V4AdviceIntent {
		t.Fatalf("同一账本轮重放不应再开计数轮: decision=%+v err=%v", replayed, err)
	}

	input.Intent = IntentAdvice{State: AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: m5ai.IntentNeutral}}
	input.Reply = ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "工作地点在上海，方便继续聊聊吗？"}}
	planned, err := ReduceV4InboundTurn(input)
	if err != nil || planned.Dialogue.Status != V4DialogueActionsPlanned || len(planned.Dialogue.Actions) != 1 ||
		planned.Dialogue.Actions[0].Kind != V4ActionReplyText || planned.Dialogue.Actions[0].ActionKey == "" ||
		planned.State.LastOutboundMessageSeq != 0 {
		t.Fatalf("完整建议没有得到唯一稳定动作，或计划阶段提前记账: decision=%+v err=%v", planned, err)
	}
	repeatedPlan, err := ReduceV4InboundTurn(input)
	if err != nil || !reflect.DeepEqual(planned, repeatedPlan) {
		t.Fatalf("同一账本与建议重复归约不确定: first=%+v repeated=%+v err=%v", planned, repeatedPlan, err)
	}
}

func TestV4InboundTurnResumeCardSkipsIntentAI(t *testing.T) {
	decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
		State: NewV4GreetedState(v4Time(8)), TurnID: "turn-resume-card",
		Messages: []LedgerMessageFact{{
			Seq: 2, Direction: "in", Kind: "card", CardType: "resumeAttachment",
			CardState: "unknown", Origin: "external",
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	})
	if err != nil || decision.State.MainStatus != V4StatusCommunicating ||
		decision.Dialogue.Status != V4DialogueWaitingAdvice || decision.Dialogue.NextAdvice != V4AdviceReply ||
		decision.Requirement != V4DialogueReplyKnownInterested ||
		decision.Dialogue.IntentLabel != m5ai.IntentInterested || decision.Dialogue.IntentSource != IntentSourceBusinessEvent {
		t.Fatalf("简历卡没有按确定性强意向跳过 intent AI: decision=%+v err=%v", decision, err)
	}
}

func TestV4InboundTurnWechatRequestPlansDeterministicActionsBeforeAI(t *testing.T) {
	input := V4InboundTurnInput{
		State: NewV4GreetedState(v4Time(8)), TurnID: "turn-wechat-card",
		Messages: []LedgerMessageFact{{
			Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
			CardState: "pending", Origin: "external",
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	}
	decision, err := ReduceV4InboundTurn(input)
	if err != nil || len(decision.EventActions) != 2 || decision.EventActions[0].Kind != V4ActionAcceptWechat ||
		decision.EventActions[1].Kind != V4ActionNotifyWechat || decision.Dialogue.Status != V4DialogueWaitingPrerequisite ||
		decision.Dialogue.NextAdvice != V4AdviceNone ||
		decision.Requirement != V4DialogueWechatContinuation ||
		!decision.DialogueAfterActions {
		t.Fatalf("主动换微信没有先给确定性动作: decision=%+v err=%v", decision, err)
	}
	input.PrerequisitesConfirmed = true
	afterAccept, err := ReduceV4InboundTurn(input)
	if err != nil || afterAccept.Dialogue.Status != V4DialogueWaitingAdvice ||
		afterAccept.Dialogue.NextAdvice != V4AdviceReply ||
		!afterAccept.DialogueAfterActions {
		t.Fatalf("同意动作确认后没有开放唯一 reply AI: decision=%+v err=%v", afterAccept, err)
	}
}

func TestV4InboundTurnInterviewedWechatRequestAcceptsWithoutDialogue(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusInterviewed
	input := V4InboundTurnInput{
		State: state, TurnID: "turn-wechat-card-interviewed",
		Messages: []LedgerMessageFact{{
			Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
			CardState: "pending", Origin: "external",
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	}
	decision, err := ReduceV4InboundTurn(input)
	if err != nil || decision.State.MainStatus != V4StatusInterviewed ||
		decision.Requirement != V4DialogueNone || decision.DialogueAfterActions ||
		decision.Dialogue.Status != V4DialogueNoAction ||
		decision.Dialogue.NextAdvice != V4AdviceNone ||
		decision.ManualReason != "" ||
		len(decision.EventActions) != 2 ||
		decision.EventActions[0].Kind != V4ActionAcceptWechat ||
		decision.EventActions[1].Kind != V4ActionNotifyWechat {
		t.Fatalf("服务态主动换微信应只产出确定性动作且无 AI 需求: decision=%+v err=%v", decision, err)
	}
}

func TestV4InboundTurnAcceptedCardsAdvanceStateWithoutAI(t *testing.T) {
	t.Run("wechat exchanged", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: NewV4GreetedState(v4Time(8)), TurnID: "turn-wechat-accepted",
			Messages: []LedgerMessageFact{{
				Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
				CardState: "accepted", Origin: "external",
			}},
			Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
			FixedPhrases: availableV4FixedPhrases(),
		})
		if err != nil || decision.State.WechatState != V4WechatExchanged ||
			decision.Dialogue.Status != V4DialogueNoAction || decision.Dialogue.NextAdvice != V4AdviceNone ||
			len(decision.Dialogue.Actions) != 0 ||
			len(decision.EventActions) != 2 ||
			decision.EventActions[0].Kind != V4ActionNotifyWechat ||
			decision.EventActions[1].Kind != V4ActionWechatReceipt {
			t.Fatalf("换微信成功事实没有确定性推进且保持零 AI: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("interview accepted", func(t *testing.T) {
		state := NewV4GreetedState(v4Time(8))
		expression, err := ApplyV4BusinessEvent(state, BusinessEvent{
			Key: "message:2", Kind: EventCandidateExpressionReceived,
			Source: EventSourceMessage, MessageSeq: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		invited, err := ApplyV4BusinessEvent(expression.State, BusinessEvent{
			Key: "message:3", Kind: EventInterviewInvited,
			Source: EventSourceMessage, MessageSeq: 3, OccurredAt: v4Time(9),
		})
		if err != nil || invited.State.MainStatus != V4StatusInvited {
			t.Fatalf("邀面前置状态失败: decision=%+v err=%v", invited, err)
		}

		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: invited.State, TurnID: "turn-interview-accepted",
			Messages: []LedgerMessageFact{{
				Seq: 4, Direction: "in", Kind: "card", CardType: "interviewInvite",
				CardState: "accepted", Origin: "external",
			}},
			Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
			FixedPhrases: availableV4FixedPhrases(),
		})
		if err != nil || decision.State.MainStatus != V4StatusInterviewed ||
			decision.Dialogue.Status != V4DialogueNoAction || decision.Dialogue.NextAdvice != V4AdviceNone ||
			len(decision.Dialogue.Actions) != 0 ||
			len(decision.EventActions) != 3 ||
			decision.EventActions[0].Kind != V4ActionInterviewAcceptedReceipt ||
			decision.EventActions[1].Kind != V4ActionNotifyInterviewAccepted ||
			decision.EventActions[2].Kind != V4ActionInviteWechat {
			t.Fatalf("面试接受事实没有确定性推进且保持零 AI: decision=%+v err=%v", decision, err)
		}
	})
}

func TestV4InboundTurnAcceptedCardWithoutFixedReceiptStopsAfterStateFact(t *testing.T) {
	decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
		State: NewV4GreetedState(v4Time(8)), TurnID: "turn-wechat-receipt-missing",
		Messages: []LedgerMessageFact{{
			Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
			CardState: "accepted", Origin: "external",
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	})
	if err != nil || decision.State.WechatState != V4WechatExchanged ||
		decision.Dialogue.Status != V4DialogueManualRequired ||
		decision.ManualReason != V4ManualFixedPhraseUnavailable ||
		len(decision.Dialogue.Actions) != 0 {
		t.Fatalf("缺固定回执时应保留换号事实并禁止发送: decision=%+v err=%v", decision, err)
	}
}

func TestV4InboundTurnRejectedShortCircuitNeverRequestsReplyAI(t *testing.T) {
	decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
		State: NewV4GreetedState(v4Time(8)), TurnID: "turn-ledger-rejected",
		Messages: []LedgerMessageFact{v4InboundText(2, "暂时不考虑，谢谢")},
		Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		FixedPhrases: availableV4FixedPhrases(),
	})
	if err != nil || decision.Dialogue.Status != V4DialogueActionsPlanned ||
		decision.Dialogue.NextAdvice != V4AdviceNone || decision.Dialogue.IntentLabel != m5ai.IntentRejected ||
		len(decision.Dialogue.Actions) != 2 || decision.State.ColdPromptRemaining != 0 || decision.State.ColdWechatRemaining != 0 {
		t.Fatalf("拒绝短路没有直接进入固定分支: decision=%+v err=%v", decision, err)
	}
}

func TestV4InboundTurnRejectedReplayCannotAdvanceTheSameTurnToClosing(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	phrases := availableV4FixedPhrases()
	phrases.Phrases[V4PhraseRejectionClosing] = V4FixedPhrase{
		Kind: V4PhraseRejectionClosing, State: V4PhraseAvailable,
		Messages: []string{"好的，后续有机会再联系。"},
		Text:     "好的，后续有机会再联系。",
	}
	firstInput := V4InboundTurnInput{
		State: state, TurnID: "turn-rejected-replay", Messages: []LedgerMessageFact{v4InboundText(2, "暂时不考虑，谢谢")},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent}, FixedPhrases: phrases,
	}
	first, err := ReduceV4InboundTurn(firstInput)
	if err != nil || len(first.Dialogue.Actions) == 0 || first.Dialogue.Actions[0].Kind != V4ActionRejectionRetention {
		t.Fatalf("首次拒绝没有冻结为挽留阶段: decision=%+v err=%v", first, err)
	}
	retentionKey := first.Dialogue.Actions[0].ActionKey
	confirmedAt := *state.LastOutboundAt
	confirmedAt = confirmedAt.Add(time.Hour)
	confirmed, err := ApplyV4ConfirmedAction(first.State, V4ConfirmedAction{
		ActionKey: retentionKey, Kind: V4ActionRejectionRetention, MessageSeq: 3, SentAt: &confirmedAt,
	})
	if err != nil || !confirmed.RetentionSent {
		t.Fatalf("挽留正证没有落入冻结状态: state=%+v err=%v", confirmed, err)
	}

	firstInput.State = confirmed
	replayed, err := ReduceV4InboundTurn(firstInput)
	if err != nil || replayed.State.RejectionStage != V4RejectionStageRetention || len(replayed.Dialogue.Actions) == 0 ||
		replayed.Dialogue.Actions[0].Kind != V4ActionRejectionRetention || replayed.Dialogue.Actions[0].ActionKey != retentionKey {
		t.Fatalf("同一拒绝轮重放只能恢复原挽留键: decision=%+v err=%v", replayed, err)
	}
	for _, action := range replayed.Dialogue.Actions {
		if action.Kind == V4ActionRejectionClosing {
			t.Fatalf("同一拒绝轮在挽留正证后错误升级为收场: %+v", replayed.Dialogue.Actions)
		}
	}

	secondInput := firstInput
	secondInput.State = confirmed
	secondInput.TurnID = "turn-rejected-new"
	secondInput.Messages = []LedgerMessageFact{v4InboundText(4, "还是不考虑")}
	second, err := ReduceV4InboundTurn(secondInput)
	if err != nil || second.State.RejectionTurnMessageSeq != 4 || second.State.RejectionStage != V4RejectionStageClosing ||
		len(second.Dialogue.Actions) != 1 || second.Dialogue.Actions[0].Kind != V4ActionRejectionClosing {
		t.Fatalf("只有更高消息序号的新拒绝轮才能升级收场: decision=%+v err=%v", second, err)
	}
}

func TestV4InboundTurnUnknownShapePreservesMainlineFactButGrantsNoAction(t *testing.T) {
	decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
		State: NewV4GreetedState(v4Time(8)), TurnID: "turn-with-unknown",
		Messages: []LedgerMessageFact{
			v4InboundText(2, "一条合法表达"),
			{Seq: 3, Direction: "in", Kind: "card", CardType: "other", CardState: "unknown", Origin: "external"},
		},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	})
	if err != nil || decision.State.MainStatus != V4StatusCommunicating || decision.State.RealMessageRound != 2 ||
		decision.ManualReason != V4ManualUnknownPlatformEvent || decision.Dialogue.Status != V4DialogueManualRequired ||
		len(decision.EventActions) != 0 || len(decision.Dialogue.Actions) != 0 || decision.Dialogue.NextAdvice != V4AdviceNone {
		t.Fatalf("未知形态应保留已知状态事实但禁止所有自动副作用: decision=%+v err=%v", decision, err)
	}
}

func TestV4InboundTurnResumeMixReducesToOneKnownInterestedRound(t *testing.T) {
	resumeCard := func(seq int64) LedgerMessageFact {
		return LedgerMessageFact{Seq: seq, Direction: "in", Kind: "card", CardType: "resumeAttachment", CardState: "unknown", Origin: "external"}
	}
	tests := []struct {
		name     string
		messages []LedgerMessageFact
		lastSeq  int64
	}{
		{name: "text_card_text", messages: []LedgerMessageFact{v4InboundText(2, "请问做几休几"), resumeCard(3), v4InboundText(4, "工作时间是")}, lastSeq: 4},
		{name: "card_then_text", messages: []LedgerMessageFact{resumeCard(2), v4InboundText(3, "另外补充一句")}, lastSeq: 3},
		{name: "text_then_card", messages: []LedgerMessageFact{v4InboundText(2, "简历发您看下"), resumeCard(3)}, lastSeq: 3},
		{name: "double_card", messages: []LedgerMessageFact{resumeCard(2), resumeCard(3)}, lastSeq: 3},
		{name: "rejection_like_text_with_card", messages: []LedgerMessageFact{v4InboundText(2, "不考虑了"), resumeCard(3)}, lastSeq: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
				State: NewV4GreetedState(v4Time(8)), TurnID: "turn-resume-mix",
				Messages: test.messages,
				Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
			})
			if err != nil || decision.ManualReason != "" || decision.State.MainStatus != V4StatusCommunicating ||
				decision.State.RealMessageRound != 2 || decision.State.LastRealMessageSeq != test.lastSeq ||
				decision.Requirement != V4DialogueReplyKnownInterested || len(decision.EventActions) != 0 ||
				decision.Dialogue.Status != V4DialogueWaitingAdvice || decision.Dialogue.NextAdvice != V4AdviceReply ||
				decision.Dialogue.IntentLabel != m5ai.IntentInterested || decision.Dialogue.IntentSource != IntentSourceBusinessEvent ||
				len(decision.Dialogue.Actions) != 0 {
				t.Fatalf("简历混合轮应合成一次已知有意向轮: decision=%+v err=%v", decision, err)
			}
		})
	}
}

func TestV4InboundTurnInterviewAcceptedMixActivatedByBatchC(t *testing.T) {
	acceptedCard := func(seq int64) LedgerMessageFact {
		return LedgerMessageFact{Seq: seq, Direction: "in", Kind: "card", CardType: "interviewInvite", CardState: "accepted", Origin: "external"}
	}
	communicating := func() V4State {
		state := NewV4GreetedState(v4Time(8))
		state.MainStatus = V4StatusCommunicating
		state.RealMessageRound = 1
		state.LastRealMessageSeq = 1
		return state
	}

	t.Run("accepted_with_text_service_reply_replaces_receipt_and_invite", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: communicating(), TurnID: "turn-c-accepted-text",
			Messages: []LedgerMessageFact{acceptedCard(2), v4InboundText(3, "请问要准备什么")},
			Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != "" ||
			decision.Requirement != V4DialogueServiceReply ||
			decision.State.MainStatus != V4StatusInterviewed ||
			!decision.State.InterviewAcceptedReceiptSent ||
			len(decision.EventActions) != 1 ||
			decision.EventActions[0].Kind != V4ActionNotifyInterviewAccepted ||
			decision.Dialogue.Status != V4DialogueWaitingAdvice ||
			decision.Dialogue.NextAdvice != V4AdviceServiceReply {
			t.Fatalf("邀面接受+文字应由一次服务应答替代回执并撤下追邀卡: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("accepted_alone_keeps_receipt_and_invite_chain", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: communicating(), TurnID: "turn-c-accepted-alone",
			Messages: []LedgerMessageFact{acceptedCard(2)},
			Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.Requirement != V4DialogueNone ||
			len(decision.EventActions) != 3 ||
			decision.EventActions[0].Kind != V4ActionInterviewAcceptedReceipt ||
			decision.EventActions[1].Kind != V4ActionNotifyInterviewAccepted ||
			decision.EventActions[2].Kind != V4ActionInviteWechat ||
			decision.State.InterviewAcceptedReceiptSent {
			t.Fatalf("无文字单卡轮必须保持既有回执+追邀链: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("accepted_with_resume_service_reply", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: communicating(), TurnID: "turn-c-accepted-resume",
			Messages: []LedgerMessageFact{
				{Seq: 2, Direction: "in", Kind: "card", CardType: "resumeAttachment", CardState: "unknown", Origin: "external"},
				acceptedCard(3),
			},
			Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != "" ||
			decision.Requirement != V4DialogueServiceReply ||
			!decision.State.InterviewAcceptedReceiptSent ||
			len(decision.EventActions) != 1 ||
			decision.EventActions[0].Kind != V4ActionNotifyInterviewAccepted {
			t.Fatalf("简历+邀面接受轮应走服务应答承接: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("accepted_with_wechat_accepted_text_single_dialogue", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: communicating(), TurnID: "turn-c-accepted-wechat",
			Messages: []LedgerMessageFact{
				{Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "accepted", Origin: "external"},
				acceptedCard(3),
				v4InboundText(4, "都弄好了"),
			},
			Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != "" ||
			decision.Requirement != V4DialogueServiceReply ||
			decision.State.WechatState != V4WechatExchanged ||
			!decision.State.WechatReceiptSent ||
			!decision.State.InterviewAcceptedReceiptSent ||
			len(decision.EventActions) != 2 ||
			decision.EventActions[0].Kind != V4ActionNotifyWechat ||
			decision.EventActions[1].Kind != V4ActionNotifyInterviewAccepted {
			t.Fatalf("多类服务卡+文字应动作并集且对话仅一次: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("greeted_accepted_alone_stays_manual_invalid_transition", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: NewV4GreetedState(v4Time(8)), TurnID: "turn-c-greeted-alone",
			Messages: []LedgerMessageFact{acceptedCard(2)},
			Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != V4ManualInvalidTransition {
			t.Fatalf("未进入沟通态的纯接受卡保持既有保守语义: decision=%+v err=%v", decision, err)
		}
	})
}

func TestV4InboundTurnWechatMixActivatedByBatchB(t *testing.T) {
	pendingCard := func(seq int64) LedgerMessageFact {
		return LedgerMessageFact{Seq: seq, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "pending", Origin: "external"}
	}
	acceptedCard := func(seq int64) LedgerMessageFact {
		return LedgerMessageFact{Seq: seq, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "accepted", Origin: "external"}
	}
	resumeCard := func(seq int64) LedgerMessageFact {
		return LedgerMessageFact{Seq: seq, Direction: "in", Kind: "card", CardType: "resumeAttachment", CardState: "unknown", Origin: "external"}
	}

	t.Run("pending_with_text_waits_accept_chain", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: NewV4GreetedState(v4Time(8)), TurnID: "turn-b-pending-text",
			Messages: []LedgerMessageFact{pendingCard(2), v4InboundText(3, "加个微信细聊")},
			Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != "" ||
			decision.Requirement != V4DialogueWechatContinuation || !decision.DialogueAfterActions ||
			decision.State.MainStatus != V4StatusCommunicating || decision.State.RealMessageRound != 2 ||
			decision.State.LastRealMessageSeq != 3 ||
			len(decision.EventActions) != 2 ||
			decision.EventActions[0].Kind != V4ActionAcceptWechat ||
			decision.EventActions[1].Kind != V4ActionNotifyWechat ||
			decision.Dialogue.Status != V4DialogueWaitingPrerequisite ||
			decision.Dialogue.IntentLabel != m5ai.IntentInterested ||
			decision.Dialogue.IntentSource != IntentSourceBusinessEvent ||
			decision.Dialogue.NextAdvice != V4AdviceNone {
			t.Fatalf("请求卡+文字应挂起等待接受链且动作照旧: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("pending_with_text_prerequisites_confirmed_continues_reply", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: NewV4GreetedState(v4Time(8)), TurnID: "turn-b-pending-confirmed",
			Messages:               []LedgerMessageFact{pendingCard(2), v4InboundText(3, "加个微信细聊")},
			Intent:                 IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
			PrerequisitesConfirmed: true,
		})
		if err != nil || decision.ManualReason != "" ||
			decision.Requirement != V4DialogueWechatContinuation ||
			decision.Dialogue.Status != V4DialogueWaitingAdvice ||
			decision.Dialogue.NextAdvice != V4AdviceReply {
			t.Fatalf("接受链完成后应进入一次承接回复: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("accepted_with_text_replaces_receipt_with_continuation", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: NewV4GreetedState(v4Time(8)), TurnID: "turn-b-accepted-text",
			Messages: []LedgerMessageFact{acceptedCard(2), v4InboundText(3, "加好了,微信聊")},
			Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != "" ||
			decision.Requirement != V4DialogueReplyKnownInterested || decision.DialogueAfterActions ||
			decision.State.WechatState != V4WechatExchanged ||
			!decision.State.WechatReceiptSent ||
			len(decision.EventActions) != 1 ||
			decision.EventActions[0].Kind != V4ActionNotifyWechat ||
			decision.Dialogue.Status != V4DialogueWaitingAdvice ||
			decision.Dialogue.NextAdvice != V4AdviceReply {
			t.Fatalf("交换成功卡+文字应由一次承接替代固定回执并置位: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("resume_with_wechat_accepted_no_text_single_visible_track", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: NewV4GreetedState(v4Time(8)), TurnID: "turn-b-resume-accepted",
			Messages: []LedgerMessageFact{resumeCard(2), acceptedCard(3)},
			Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != "" ||
			decision.Requirement != V4DialogueReplyKnownInterested ||
			!decision.State.WechatReceiptSent ||
			len(decision.EventActions) != 1 ||
			decision.EventActions[0].Kind != V4ActionNotifyWechat ||
			decision.Dialogue.Status != V4DialogueWaitingAdvice ||
			decision.Dialogue.NextAdvice != V4AdviceReply {
			t.Fatalf("简历+交换成功轮的回复承接必须替代固定回执(单可见轨): decision=%+v err=%v", decision, err)
		}
	})

	t.Run("pending_and_accepted_with_text_keeps_single_accept_anchor", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: NewV4GreetedState(v4Time(8)), TurnID: "turn-b-pending-accepted",
			Messages: []LedgerMessageFact{acceptedCard(2), pendingCard(3), v4InboundText(4, "再加一个号")},
			Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != "" ||
			decision.Requirement != V4DialogueWechatContinuation || !decision.DialogueAfterActions ||
			decision.State.WechatState != V4WechatExchanged || !decision.State.WechatReceiptSent ||
			len(decision.EventActions) != 3 ||
			decision.EventActions[0].Kind != V4ActionNotifyWechat ||
			decision.EventActions[1].Kind != V4ActionAcceptWechat ||
			decision.EventActions[2].Kind != V4ActionNotifyWechat ||
			decision.Dialogue.Status != V4DialogueWaitingPrerequisite {
			t.Fatalf("交换成功+请求卡混合应保持恰好一个接受锚且回执被承接替代: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("double_pending_stays_manual", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: NewV4GreetedState(v4Time(8)), TurnID: "turn-b-double-pending",
			Messages: []LedgerMessageFact{pendingCard(2), pendingCard(3), v4InboundText(4, "两张卡")},
			Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != V4ManualUnsupportedSemantic ||
			len(decision.EventActions) != 0 || decision.Dialogue.NextAdvice != V4AdviceNone {
			t.Fatalf("同轮多张请求卡没有确定接受目标,必须保守转人工: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("interviewed_pending_with_text_stays_manual", func(t *testing.T) {
		state := NewV4GreetedState(v4Time(8))
		state.MainStatus = V4StatusInterviewed
		state.RealMessageRound = 3
		state.LastRealMessageSeq = 5
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: state, TurnID: "turn-b-interviewed-pending",
			Messages: []LedgerMessageFact{pendingCard(6), v4InboundText(7, "加微信呀")},
			Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != V4ManualUnsupportedSemantic ||
			len(decision.EventActions) != 0 {
			t.Fatalf("已约面服务态的请求卡+文字接续形状未立案,应保守转人工: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("interviewed_accepted_with_text_service_reply", func(t *testing.T) {
		state := NewV4GreetedState(v4Time(8))
		state.MainStatus = V4StatusInterviewed
		state.RealMessageRound = 3
		state.LastRealMessageSeq = 5
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: state, TurnID: "turn-b-interviewed-accepted",
			Messages: []LedgerMessageFact{acceptedCard(6), v4InboundText(7, "加好了")},
			Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != "" ||
			decision.Requirement != V4DialogueServiceReply ||
			!decision.State.WechatReceiptSent ||
			len(decision.EventActions) != 1 ||
			decision.EventActions[0].Kind != V4ActionNotifyWechat ||
			decision.Dialogue.Status != V4DialogueWaitingAdvice ||
			decision.Dialogue.NextAdvice != V4AdviceServiceReply {
			t.Fatalf("服务态交换成功+文字应由一次服务应答承接替代回执: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("unknown_event_short_circuits_before_receipt_suppression", func(t *testing.T) {
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: NewV4GreetedState(v4Time(8)), TurnID: "turn-b-unknown-mix",
			Messages: []LedgerMessageFact{
				acceptedCard(2),
				v4InboundText(3, "看下这个"),
				{Seq: 4, Direction: "in", Kind: "card", CardType: "interviewInvite", CardState: "pending", Origin: "external"},
			},
			Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != V4ManualUnknownPlatformEvent ||
			decision.State.WechatReceiptSent ||
			len(decision.EventActions) != 0 {
			t.Fatalf("未知事件轮必须在义务置位前整轮转人工: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("eliminated_pending_with_text_no_action", func(t *testing.T) {
		state := NewV4GreetedState(v4Time(8))
		state.MainStatus = V4StatusEliminated
		decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
			State: state, TurnID: "turn-b-eliminated",
			Messages: []LedgerMessageFact{pendingCard(2), v4InboundText(3, "在吗")},
			Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		})
		if err != nil || decision.ManualReason != "" ||
			decision.Requirement != V4DialogueNone ||
			len(decision.EventActions) != 0 ||
			decision.State.LastRealMessageSeq != 3 ||
			decision.Dialogue.Status != V4DialogueNoAction {
			t.Fatalf("已淘汰档案的请求卡混合轮只滑锚不产生动作: decision=%+v err=%v", decision, err)
		}
	})
}

func TestV4InboundTurnSystemOnlyProducesExplicitNoAction(t *testing.T) {
	decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
		State: NewV4GreetedState(v4Time(8)), TurnID: "turn-system-only",
		Messages: []LedgerMessageFact{{
			Seq: 2, Direction: "system", Kind: "system", Origin: "external",
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	})
	if err != nil || decision.Dialogue.Status != V4DialogueNoAction || decision.Dialogue.NextAdvice != V4AdviceNone ||
		decision.State.MainStatus != V4StatusGreeted || len(decision.EventActions) != 0 {
		t.Fatalf("纯系统行不应为统一流水线强行调用 AI: decision=%+v err=%v", decision, err)
	}
}

func TestV4InboundTurnMediaReachesKnownManualBoundaryWithoutAI(t *testing.T) {
	decision, err := ReduceV4InboundTurn(V4InboundTurnInput{
		State: NewV4GreetedState(v4Time(8)), TurnID: "turn-media",
		Messages: []LedgerMessageFact{{Seq: 2, Direction: "in", Kind: "voice", Origin: "external"}},
		Intent:   IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	})
	if err != nil || decision.State.MainStatus != V4StatusCommunicating ||
		decision.Dialogue.Status != V4DialogueManualRequired || decision.ManualReason != V4ManualUnsupportedMedia ||
		decision.Dialogue.NextAdvice != V4AdviceNone {
		t.Fatalf("当前 provider 无媒体输入时应推进主线后转人工: decision=%+v err=%v", decision, err)
	}
}

func TestV4InboundTurnRejectsBrokenLedgerBoundary(t *testing.T) {
	state := NewV4GreetedState(v4Time(8))
	cases := []V4InboundTurnInput{
		{State: state, TurnID: "", Messages: []LedgerMessageFact{v4InboundText(1, "x")}, Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent}},
		{State: state, TurnID: "turn", Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent}},
		{State: state, TurnID: "turn", Messages: []LedgerMessageFact{v4InboundText(2, "x"), v4InboundText(2, "y")}, Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent}},
		{State: state, TurnID: "turn", Messages: []LedgerMessageFact{{Seq: 2, Direction: "out", Kind: "text", Text: textPointer("x"), Origin: "self"}}, Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent}},
	}
	for index, input := range cases {
		if _, err := ReduceV4InboundTurn(input); !errors.Is(err, ErrInvalidV4StateTransition) {
			t.Fatalf("非法账本边界[%d]没有响亮失败: %v", index, err)
		}
	}
}
