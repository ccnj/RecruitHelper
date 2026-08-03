package communication

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"recruithelper/client/service/internal/m5ai"
)

func availableV4FixedPhrases() V4FixedPhraseView {
	return V4FixedPhraseView{Phrases: map[V4FixedPhraseKind]V4FixedPhrase{
		V4PhraseRejectionRetention: {
			Kind: V4PhraseRejectionRetention, SourceScene: "rejectWechat",
			State: V4PhraseAvailable,
			Messages: []string{
				"方便的话也可以先加个微信了解一下。",
			},
			Text: "方便的话也可以先加个微信了解一下。",
		},
		V4PhraseWechatReceipt: {
			Kind: V4PhraseWechatReceipt, SourceScene: "wechatAccepted",
			State: V4PhraseAvailable,
			Messages: []string{
				"好的，晚点加你。",
			},
			Text: "好的，晚点加你。",
		},
		V4PhraseInterviewAccepted: {
			Kind: V4PhraseInterviewAccepted, SourceScene: "meetingAccepted",
			State: V4PhraseAvailable,
			Messages: []string{
				"好的，面试安排已确认。",
			},
			Text: "好的，面试安排已确认。",
		},
		V4PhraseColdWechat: {
			Kind: V4PhraseColdWechat, SourceScene: "silence48Wechat",
			State: V4PhraseAvailable,
			Messages: []string{
				"如果方便，也可以先加微信了解。",
			},
			Text: "如果方便，也可以先加微信了解。",
		},
	}}
}

func activeV4DialogueState() V4State {
	state := NewV4GreetedState(v4Time(8))
	state.MainStatus = V4StatusCommunicating
	state.RealMessageRound = 2
	state.LastRealMessageSeq = 3
	return state
}

func TestV4DialogueOrdinaryTurnAuthorizesIntentThenReplyInOrder(t *testing.T) {
	input := V4DialogueInput{
		State: activeV4DialogueState(), Requirement: V4DialogueClassifyAndReply,
		Turn: ordinaryTurn(), Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	}
	waitingIntent, err := ReduceV4Dialogue(input)
	if err != nil || waitingIntent.Status != V4DialogueWaitingAdvice || waitingIntent.NextAdvice != V4AdviceIntent ||
		len(waitingIntent.Actions) != 0 || waitingIntent.IntentLabel != "" {
		t.Fatalf("普通轮没有先停在唯一 intent 调用: decision=%+v err=%v", waitingIntent, err)
	}

	input.Intent = IntentAdvice{State: AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: m5ai.IntentInterested}}
	waitingReply, err := ReduceV4Dialogue(input)
	if err != nil || waitingReply.Status != V4DialogueWaitingAdvice || waitingReply.NextAdvice != V4AdviceReply ||
		waitingReply.IntentLabel != m5ai.IntentInterested || waitingReply.IntentSource != IntentSourceLLM {
		t.Fatalf("意向完成后没有停在独立 reply 调用: decision=%+v err=%v", waitingReply, err)
	}

	input.Reply = ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "可以的，我们聊聊具体情况。"}}
	planned, err := ReduceV4Dialogue(input)
	if err != nil || planned.Status != V4DialogueActionsPlanned || planned.NextAdvice != V4AdviceNone ||
		len(planned.Actions) != 1 || planned.Actions[0].Kind != V4ActionReplyText ||
		planned.Actions[0].Text != input.Reply.Suggestion.Text || planned.State.LastOutboundMessageSeq != input.State.LastOutboundMessageSeq {
		t.Fatalf("回复建议没有变成唯一候选动作，或计划阶段提前记账: decision=%+v err=%v", planned, err)
	}

	replayed, err := ReduceV4Dialogue(input)
	if err != nil || !reflect.DeepEqual(planned, replayed) {
		t.Fatalf("同一冻结轮重复归约必须确定: first=%+v replayed=%+v err=%v", planned, replayed, err)
	}
}

func TestV4DialoguePreservesPhraseOrderAndPutsSuggestedCardLast(t *testing.T) {
	input := V4DialogueInput{
		State:       activeV4DialogueState(),
		Requirement: V4DialogueClassifyAndReply,
		Turn: FrozenTurnFacts{
			TurnID:   "turn-multi-bubble",
			Messages: ordinaryTurn().Messages,
		},
		Intent: IntentAdvice{
			State:      AdviceOK,
			Suggestion: m5ai.IntentSuggestion{Label: m5ai.IntentInterested},
		},
		Reply: ReplyAdvice{
			State: AdviceOK,
			Suggestion: m5ai.ReplySuggestion{
				Phrases: []string{"第一句", "第二句", "第三句"},
				Text:    "第一句\n第二句\n第三句",
				Action:  m5ai.ReplyActionInviteWechat,
			},
		},
	}

	planned, err := ReduceV4Dialogue(input)
	if err != nil || planned.Status != V4DialogueActionsPlanned || len(planned.Actions) != 4 {
		t.Fatalf("多气泡+卡片没有形成完整有序计划: decision=%+v err=%v", planned, err)
	}
	wantKinds := []V4ActionKind{
		V4ActionReplyText,
		V4ActionReplyText,
		V4ActionReplyText,
		V4ActionInviteWechat,
	}
	wantTexts := []string{"第一句", "第二句", "第三句", ""}
	wantKeys := []string{
		"turn-multi-bubble|replyText",
		"turn-multi-bubble|replyText|bubble:2",
		"turn-multi-bubble|replyText|bubble:3",
		"turn-multi-bubble|inviteWechat",
	}
	seenKeys := make(map[string]struct{}, len(planned.Actions))
	for index, action := range planned.Actions {
		if action.Kind != wantKinds[index] || action.Text != wantTexts[index] ||
			action.ActionKey != wantKeys[index] {
			t.Fatalf("动作[%d]的顺序、正文或稳定键错误: action=%+v", index, action)
		}
		if _, exists := seenKeys[action.ActionKey]; exists {
			t.Fatalf("动作键重复: %q", action.ActionKey)
		}
		seenKeys[action.ActionKey] = struct{}{}
	}

	replayed, err := ReduceV4Dialogue(input)
	if err != nil || !reflect.DeepEqual(planned, replayed) {
		t.Fatalf("同一多气泡冻结轮重复归约不确定: first=%+v replayed=%+v err=%v", planned, replayed, err)
	}
}

func TestV4DialogueServiceReplyAbandonsMultiBubbleSuggestion(t *testing.T) {
	// 规格 v4 §七(2026-07-31):服务补句只允许一句;多气泡是服务解析器
	// 合同外形状,放弃补句、正常终局,不发送也不转人工。
	state := activeV4DialogueState()
	state.MainStatus = V4StatusInterviewed
	input := V4DialogueInput{
		State:       state,
		Requirement: V4DialogueServiceReply,
		Turn:        FrozenTurnFacts{TurnID: "turn-service-multi"},
		Intent:      IntentAdvice{State: AdviceAbsent},
		Reply: ReplyAdvice{
			State: AdviceOK,
			Suggestion: m5ai.ReplySuggestion{
				Phrases: []string{"第一句", "第二句"},
				Text:    "第一句\n第二句",
			},
		},
	}

	planned, err := ReduceV4Dialogue(input)
	if err != nil || planned.Status != V4DialogueNoAction || len(planned.Actions) != 0 ||
		planned.ManualReason != "" || planned.ServiceVerdict != V4ServiceReplySkipped {
		t.Fatalf("服务态多气泡建议应放弃补句而非发送或转人工: decision=%+v err=%v", planned, err)
	}
}

func TestV4DialogueServiceReplyTerminals(t *testing.T) {
	base := func() V4DialogueInput {
		state := activeV4DialogueState()
		state.MainStatus = V4StatusInterviewed
		return V4DialogueInput{
			State:       state,
			Requirement: V4DialogueServiceReply,
			Turn:        FrozenTurnFacts{TurnID: "turn-service-terminal"},
			Intent:      IntentAdvice{State: AdviceAbsent},
		}
	}

	t.Run("advice_failed_abandons_without_manual", func(t *testing.T) {
		input := base()
		input.Reply = ReplyAdvice{State: AdviceFailed}
		decision, err := ReduceV4Dialogue(input)
		if err != nil || decision.Status != V4DialogueNoAction || decision.ManualReason != "" ||
			decision.ServiceVerdict != V4ServiceReplySkipped {
			t.Fatalf("补句失败应放弃并正常终局: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("empty_phrases_is_explicit_silence", func(t *testing.T) {
		input := base()
		input.Reply = ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{}}
		decision, err := ReduceV4Dialogue(input)
		if err != nil || decision.Status != V4DialogueNoAction || decision.ManualReason != "" ||
			decision.ServiceVerdict != V4ServiceReplySilent {
			t.Fatalf("空建议应判定为显式静默: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("pending_event_actions_gate_until_confirmed", func(t *testing.T) {
		input := base()
		input.PendingEventActions = true
		input.Reply = ReplyAdvice{State: AdviceAbsent}
		waiting, err := ReduceV4Dialogue(input)
		if err != nil || waiting.Status != V4DialogueWaitingPrerequisite ||
			waiting.NextAdvice != V4AdviceNone {
			t.Fatalf("固定段未收束时不得请求补句建议: decision=%+v err=%v", waiting, err)
		}
		input.PrerequisitesConfirmed = true
		advised, err := ReduceV4Dialogue(input)
		if err != nil || advised.Status != V4DialogueWaitingAdvice ||
			advised.NextAdvice != V4AdviceServiceReply {
			t.Fatalf("前置确认后应请求一次补句建议: decision=%+v err=%v", advised, err)
		}
	})
}

func TestV4DialogueDeterministicallyClosesReplyActionSuggestions(t *testing.T) {
	baseTurn := ordinaryTurn()
	baseTurn.RecommendedSlots = []string{
		"2026-07-14 09:00:00",
		"2026-07-14 14:00:00",
	}
	base := V4DialogueInput{
		State: activeV4DialogueState(), Requirement: V4DialogueClassifyAndReply,
		Turn: baseTurn,
		Intent: IntentAdvice{
			State:      AdviceOK,
			Suggestion: m5ai.IntentSuggestion{Label: m5ai.IntentInterested},
		},
	}

	t.Run("no action remains one text", func(t *testing.T) {
		input := base
		input.Reply = ReplyAdvice{
			State: AdviceOK,
			Suggestion: m5ai.ReplySuggestion{
				Text: "我们继续聊聊岗位。",
			},
		}
		decision, err := ReduceV4Dialogue(input)
		if err != nil || decision.Status != V4DialogueActionsPlanned ||
			len(decision.Actions) != 1 ||
			decision.Actions[0].Kind != V4ActionReplyText {
			t.Fatalf("无动作建议不应改变正文路径: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("wechat action requires unused balance", func(t *testing.T) {
		input := base
		input.Reply = ReplyAdvice{
			State: AdviceOK,
			Suggestion: m5ai.ReplySuggestion{
				Text:   "方便的话，我发个换微信邀请。",
				Action: m5ai.ReplyActionInviteWechat,
			},
		}
		decision, err := ReduceV4Dialogue(input)
		if err != nil || decision.Status != V4DialogueActionsPlanned ||
			len(decision.Actions) != 2 ||
			decision.Actions[0].Kind != V4ActionReplyText ||
			decision.Actions[1].Kind != V4ActionInviteWechat {
			t.Fatalf("合法换微信建议未形成固定两动作: decision=%+v err=%v", decision, err)
		}

		input.State.WechatState = V4WechatInvited
		blocked, err := ReduceV4Dialogue(input)
		if err != nil || blocked.Status != V4DialogueManualRequired ||
			blocked.ManualReason != V4ManualReplyInvalid ||
			len(blocked.Actions) != 0 {
			t.Fatalf("已消耗微信余额仍形成动作: decision=%+v err=%v", blocked, err)
		}

		input.State = base.State
		input.Turn.Messages = []FrozenInboundMessage{{
			Seq:  4,
			Kind: FrozenMessageSystem,
		}}
		withoutCandidateMessage, err := ReduceV4Dialogue(input)
		if err != nil ||
			withoutCandidateMessage.Status != V4DialogueManualRequired ||
			len(withoutCandidateMessage.Actions) != 0 {
			t.Fatalf(
				"没有本轮真实候选消息仍形成动作: decision=%+v err=%v",
				withoutCandidateMessage,
				err,
			)
		}
	})

	t.Run("meeting action uniquely binds frozen slot", func(t *testing.T) {
		input := base
		input.Reply = ReplyAdvice{
			State: AdviceOK,
			Suggestion: m5ai.ReplySuggestion{
				Text:        "那我们约在这个时间视频面试。",
				Action:      m5ai.ReplyActionStartOnlineMeeting,
				MeetingTime: " \n7月14日14:00\t",
			},
		}
		decision, err := ReduceV4Dialogue(input)
		wantStart, _ := m5ai.MatchFrozenRecommendedMeetingTime(
			baseTurn.RecommendedSlots,
			"7月14日14:00",
		)
		if err != nil || decision.Status != V4DialogueActionsPlanned ||
			len(decision.Actions) != 2 ||
			decision.Actions[1].Kind != V4ActionInterviewInvite ||
			decision.Actions[1].InterviewStartsAtMs == nil ||
			*decision.Actions[1].InterviewStartsAtMs != wantStart ||
			decision.Actions[1].InterviewEndsAtMs == nil ||
			*decision.Actions[1].InterviewEndsAtMs != wantStart+V4InterviewDurationMs ||
			decision.Actions[1].InterviewMethod == nil ||
			*decision.Actions[1].InterviewMethod != "wechatVideo" {
			t.Fatalf("合法邀面建议没有派生固定卡片参数: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("meeting mismatch makes the whole turn manual", func(t *testing.T) {
		cases := []struct {
			name        string
			slots       []string
			meetingTime string
		}{
			{name: "zero match", slots: baseTurn.RecommendedSlots, meetingTime: "7月15日14:00"},
			{name: "multiple matches", slots: []string{"2026-07-14 14:00:00", "2026-07-14 14:00:00"}, meetingTime: "7月14日14:00"},
			{name: "legacy turn without slots", slots: nil, meetingTime: "7月14日14:00"},
			{name: "invalid format", slots: baseTurn.RecommendedSlots, meetingTime: "07月14日14:00"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				input := base
				input.Turn.RecommendedSlots = testCase.slots
				input.Reply = ReplyAdvice{
					State: AdviceOK,
					Suggestion: m5ai.ReplySuggestion{
						Text:        "这条正文也不得单独发送。",
						Action:      m5ai.ReplyActionStartOnlineMeeting,
						MeetingTime: testCase.meetingTime,
					},
				}
				decision, err := ReduceV4Dialogue(input)
				if err != nil || decision.Status != V4DialogueManualRequired ||
					decision.ManualReason != V4ManualReplyInvalid ||
					len(decision.Actions) != 0 {
					t.Fatalf("非法邀面建议未整轮转人工: decision=%+v err=%v", decision, err)
				}
			})
		}
	})

	t.Run("non ordinary reply purpose rejects AI action", func(t *testing.T) {
		// 2026-07-31 规格修订:服务轨动作建议仍然一律不执行,但终局从
		// 转人工改为放弃补句、正常收束。
		input := base
		input.State.MainStatus = V4StatusInterviewed
		input.Requirement = V4DialogueServiceReply
		input.Intent = IntentAdvice{State: AdviceAbsent}
		input.Reply = ReplyAdvice{
			State: AdviceOK,
			Suggestion: m5ai.ReplySuggestion{
				Text:   "服务态不能执行模型动作。",
				Action: m5ai.ReplyActionInviteWechat,
			},
		}
		decision, err := ReduceV4Dialogue(input)
		if err != nil || decision.Status != V4DialogueNoAction ||
			decision.ManualReason != "" || len(decision.Actions) != 0 ||
			decision.ServiceVerdict != V4ServiceReplySkipped {
			t.Fatalf("服务轨动作建议应放弃补句而非执行或转人工: decision=%+v err=%v", decision, err)
		}
	})

	t.Run("reducer rejects action shapes even if parser was bypassed", func(t *testing.T) {
		for _, suggestion := range []m5ai.ReplySuggestion{
			{
				Text:   "未知动作不得执行。",
				Action: m5ai.ReplyAction("futureAction"),
			},
			{
				Text:        "换微信动作不得携带会议时间。",
				Action:      m5ai.ReplyActionInviteWechat,
				MeetingTime: "7月14日14:00",
			},
			{
				Text:        "无动作也不得携带空白会议字段。",
				Action:      m5ai.ReplyActionNone,
				MeetingTime: " ",
			},
		} {
			input := base
			input.Reply = ReplyAdvice{State: AdviceOK, Suggestion: suggestion}
			decision, err := ReduceV4Dialogue(input)
			if err != nil || decision.Status != V4DialogueManualRequired ||
				len(decision.Actions) != 0 {
				t.Fatalf(
					"绕过 parser 的非法动作形态未被 reducer 重判: suggestion=%+v decision=%+v err=%v",
					suggestion,
					decision,
					err,
				)
			}
		}
	})
}

func TestV4DialogueShortCircuitCanSkipIntentAI(t *testing.T) {
	input := V4DialogueInput{
		State: activeV4DialogueState(), Requirement: V4DialogueClassifyAndReply,
		Turn: FrozenTurnFacts{TurnID: "turn-resume-marker", Messages: []FrozenInboundMessage{
			{Seq: 4, Kind: FrozenMessageText, Text: "已发送了在线简历"},
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	}
	decision, err := ReduceV4Dialogue(input)
	if err != nil || decision.Status != V4DialogueWaitingAdvice || decision.NextAdvice != V4AdviceReply ||
		decision.IntentLabel != m5ai.IntentInterested || decision.IntentSource != IntentSourceCodeShortCircuit {
		t.Fatalf("已批准简历短路没有跳过 intent AI: decision=%+v err=%v", decision, err)
	}
}

func TestV4DialogueRejectedFirstTurnUsesFixedPhraseWithoutReplyAI(t *testing.T) {
	input := V4DialogueInput{
		State: activeV4DialogueState(), Requirement: V4DialogueClassifyAndReply,
		Turn: FrozenTurnFacts{TurnID: "turn-rejected-1", Messages: []FrozenInboundMessage{
			{Seq: 4, Kind: FrozenMessageText, Text: "暂时不考虑，谢谢"},
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		FixedPhrases: availableV4FixedPhrases(),
	}
	decision, err := ReduceV4Dialogue(input)
	if err != nil || decision.Status != V4DialogueActionsPlanned || decision.NextAdvice != V4AdviceNone ||
		decision.IntentLabel != m5ai.IntentRejected || decision.IntentSource != IntentSourceCodeShortCircuit ||
		decision.State.ColdPromptRemaining != 0 || decision.State.ColdWechatRemaining != 0 ||
		decision.State.RetentionSent || decision.State.WechatState != V4WechatNotInvited || len(decision.Actions) != 2 ||
		decision.Actions[0].Kind != V4ActionRejectionRetention || decision.Actions[1].Kind != V4ActionInviteWechat ||
		decision.State.RejectionTurnMessageSeq != 4 || decision.State.RejectionTurnID != "turn-rejected-1" ||
		decision.State.RejectionStage != V4RejectionStageRetention {
		t.Fatalf("首次拒绝没有走固定话术+微信邀请，或计划阶段提前推进: decision=%+v err=%v", decision, err)
	}
	for _, action := range decision.Actions {
		if action.ActionKey == "" {
			t.Fatalf("拒绝分支动作缺稳定键: %+v", decision.Actions)
		}
	}
}

func TestV4DialogueRejectedPreservesFixedMessageBubblesBeforeWechatCard(t *testing.T) {
	fixed := availableV4FixedPhrases()
	retention := fixed.Phrases[V4PhraseRejectionRetention]
	retention.Messages = []string{
		"第一项。",
		"第二项第一行。\n第二项第二行。",
		"第三项包含两句话。仍然是同一个数组项。",
	}
	retention.Text = strings.Join(retention.Messages, "\n")
	fixed.Phrases[V4PhraseRejectionRetention] = retention
	input := V4DialogueInput{
		State: activeV4DialogueState(), Requirement: V4DialogueClassifyAndReply,
		Turn: FrozenTurnFacts{TurnID: "turn-rejected-fixed-bubbles", Messages: []FrozenInboundMessage{
			{Seq: 4, Kind: FrozenMessageText, Text: "暂时不考虑，谢谢"},
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		FixedPhrases: fixed,
	}

	decision, err := ReduceV4Dialogue(input)
	if err != nil || decision.Status != V4DialogueActionsPlanned ||
		decision.NextAdvice != V4AdviceNone ||
		len(decision.Actions) != len(retention.Messages)+1 {
		t.Fatalf("固定挽留多气泡没有形成完整计划: decision=%+v err=%v", decision, err)
	}
	for index, message := range retention.Messages {
		action := decision.Actions[index]
		if action.Kind != V4ActionRejectionRetention ||
			action.Text != message ||
			action.ActionKey != stableV4TurnPhraseActionKey(
				input.Turn.TurnID,
				V4ActionRejectionRetention,
				0,
				index+1,
			) {
			t.Fatalf("固定挽留气泡[%d]边界或稳定键错误: action=%+v", index, action)
		}
	}
	card := decision.Actions[len(decision.Actions)-1]
	if card.Kind != V4ActionInviteWechat ||
		card.ActionKey != stableV4TurnActionKey(input.Turn.TurnID, V4ActionInviteWechat, 0) {
		t.Fatalf("换微信卡没有排在固定话术最后: %+v", card)
	}
}

func TestV4DialogueRejectedMissingPhraseOnlyDegradesThatBranch(t *testing.T) {
	state := activeV4DialogueState()
	input := V4DialogueInput{
		State: state, Requirement: V4DialogueClassifyAndReply,
		Turn: FrozenTurnFacts{TurnID: "turn-rejected-missing", Messages: []FrozenInboundMessage{
			{Seq: 4, Kind: FrozenMessageText, Text: "不感兴趣"},
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	}
	decision, err := ReduceV4Dialogue(input)
	if err != nil || decision.Status != V4DialogueManualRequired || decision.ManualReason != V4ManualFixedPhraseUnavailable ||
		decision.NextAdvice != V4AdviceNone || len(decision.Actions) != 0 ||
		decision.State.ColdPromptRemaining != 0 || decision.State.ColdWechatRemaining != 0 {
		t.Fatalf("缺固定话术应只让拒绝分支转人工，并保留分类清预算事实: decision=%+v err=%v", decision, err)
	}

	ordinary := V4DialogueInput{
		State: state, Requirement: V4DialogueClassifyAndReply, Turn: ordinaryTurn(),
		Intent: IntentAdvice{State: AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: m5ai.IntentNeutral}},
		Reply:  ReplyAdvice{State: AdviceAbsent},
	}
	next, err := ReduceV4Dialogue(ordinary)
	if err != nil || next.Status != V4DialogueWaitingAdvice || next.NextAdvice != V4AdviceReply {
		t.Fatalf("缺固定话术不应全局阻断非拒绝轮: decision=%+v err=%v", next, err)
	}
}

func TestV4DialogueSecondRejectionStopsWhenClosingViewIsAbsent(t *testing.T) {
	state := activeV4DialogueState()
	state.RetentionSent = true
	input := V4DialogueInput{
		State: state, Requirement: V4DialogueClassifyAndReply,
		Turn: FrozenTurnFacts{TurnID: "turn-rejected-2", Messages: []FrozenInboundMessage{
			{Seq: 5, Kind: FrozenMessageText, Text: "这个不合适"},
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		FixedPhrases: availableV4FixedPhrases(),
	}
	decision, err := ReduceV4Dialogue(input)
	if err != nil || decision.Status != V4DialogueManualRequired || decision.ManualReason != V4ManualFixedPhraseUnavailable ||
		len(decision.Actions) != 0 {
		t.Fatalf("调用方缺少本地收场视图时不得拆 rejectWechat 数组猜造: decision=%+v err=%v", decision, err)
	}
}

func TestV4DialogueSecondRejectionUsesApprovedLocalClosingDefault(t *testing.T) {
	state := activeV4DialogueState()
	state.RetentionSent = true
	phrases, err := BuildV4FixedPhraseView(m5ai.JobConfigDocumentPackage{})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := ReduceV4Dialogue(V4DialogueInput{
		State: state, Requirement: V4DialogueClassifyAndReply,
		Turn: FrozenTurnFacts{TurnID: "turn-rejected-local-closing", Messages: []FrozenInboundMessage{
			{Seq: 5, Kind: FrozenMessageText, Text: "还是不考虑"},
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
		FixedPhrases: phrases,
	})
	if err != nil || decision.Status != V4DialogueActionsPlanned ||
		len(decision.Actions) != 1 ||
		decision.Actions[0].Kind != V4ActionRejectionClosing ||
		decision.Actions[0].Text != v4LocalRejectionClosingText {
		t.Fatalf("第二次拒绝没有使用获批本地收场话术: decision=%+v err=%v", decision, err)
	}
}

func TestV4DialogueAllRejectedPhrasesSentArchivesSilently(t *testing.T) {
	state := activeV4DialogueState()
	state.RetentionSent = true
	state.ClosingSent = true
	decision, err := ReduceV4Dialogue(V4DialogueInput{
		State: state, Requirement: V4DialogueClassifyAndReply,
		Turn: FrozenTurnFacts{TurnID: "turn-rejected-3", Messages: []FrozenInboundMessage{
			{Seq: 6, Kind: FrozenMessageText, Text: "暂时不考虑，谢谢"},
		}},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	})
	if err != nil || decision.Status != V4DialogueNoAction || decision.NextAdvice != V4AdviceNone ||
		len(decision.Actions) != 0 || decision.State.MainStatus != V4StatusEnded || decision.State.EndReason != V4EndRejected {
		t.Fatalf("固定话术均已发后应静默归档: decision=%+v err=%v", decision, err)
	}
}

func TestV4DialogueKnownInterestWechatAndServiceSkipIntentAI(t *testing.T) {
	cases := []struct {
		name        string
		state       V4State
		requirement V4DialogueRequirement
		purpose     V4AdvicePurpose
		action      V4ActionKind
		label       m5ai.IntentLabel
	}{
		{name: "resume", state: activeV4DialogueState(), requirement: V4DialogueReplyKnownInterested, purpose: V4AdviceReply, action: V4ActionReplyText, label: m5ai.IntentInterested},
		{name: "service", state: func() V4State { state := activeV4DialogueState(); state.MainStatus = V4StatusInterviewed; return state }(), requirement: V4DialogueServiceReply, purpose: V4AdviceServiceReply, action: V4ActionServiceReply},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := V4DialogueInput{
				State: tc.state, Requirement: tc.requirement, Turn: FrozenTurnFacts{TurnID: "turn-" + tc.name},
				Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
			}
			waiting, err := ReduceV4Dialogue(input)
			if err != nil || waiting.Status != V4DialogueWaitingAdvice || waiting.NextAdvice != tc.purpose ||
				waiting.IntentLabel != tc.label {
				t.Fatalf("确定性分支不应先调用 intent AI: decision=%+v err=%v", waiting, err)
			}
			input.Reply = ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{
				Phrases: []string{"收到，我们继续沟通。"}, Text: "收到，我们继续沟通。",
			}}
			planned, err := ReduceV4Dialogue(input)
			if err != nil || planned.Status != V4DialogueActionsPlanned || len(planned.Actions) != 1 ||
				planned.Actions[0].Kind != tc.action {
				t.Fatalf("回复建议动作种类错误: decision=%+v err=%v", planned, err)
			}
		})
	}
}

func TestV4DialogueWechatContinuationWaitsForDeterministicAcceptance(t *testing.T) {
	input := V4DialogueInput{
		State: activeV4DialogueState(), Requirement: V4DialogueWechatContinuation,
		Turn:   FrozenTurnFacts{TurnID: "turn-wechat-request"},
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	}
	waiting, err := ReduceV4Dialogue(input)
	if err != nil || waiting.Status != V4DialogueWaitingPrerequisite || waiting.NextAdvice != V4AdviceNone ||
		waiting.IntentLabel != m5ai.IntentInterested || len(waiting.Actions) != 0 {
		t.Fatalf("候选人主动换微信必须先完成确定性同意动作: decision=%+v err=%v", waiting, err)
	}

	input.PrerequisitesConfirmed = true
	waitingAI, err := ReduceV4Dialogue(input)
	if err != nil || waitingAI.Status != V4DialogueWaitingAdvice || waitingAI.NextAdvice != V4AdviceReply {
		t.Fatalf("确定性同意完成后才应开放回复 AI: decision=%+v err=%v", waitingAI, err)
	}

	input.Reply = ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "好的，我们继续聊聊岗位。"}}
	planned, err := ReduceV4Dialogue(input)
	if err != nil || planned.Status != V4DialogueActionsPlanned || len(planned.Actions) != 1 ||
		planned.Actions[0].Kind != V4ActionReplyText {
		t.Fatalf("换微信承接回复计划错误: decision=%+v err=%v", planned, err)
	}
}

func TestV4DialogueInterviewRejectionUsesDedicatedReplyWithoutIntent(t *testing.T) {
	state := activeV4DialogueState()
	state.MainStatus = V4StatusInvited
	state.InterviewGroups = []V4InterviewFollowupGroup{{MessageSeq: 20, NextStage: 1, Rejected: true}}
	input := V4DialogueInput{
		State: state, Requirement: V4DialogueInterviewRejectionReceipt,
		Turn: FrozenTurnFacts{TurnID: "turn-card-rejected"}, CardMessageSeq: 20,
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	}
	waiting, err := ReduceV4Dialogue(input)
	if err != nil || waiting.Status != V4DialogueWaitingAdvice || waiting.NextAdvice != V4AdviceInterviewRejectionReply ||
		waiting.IntentLabel != "" {
		t.Fatalf("拒面卡不应判意向: decision=%+v err=%v", waiting, err)
	}

	input.Reply = ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "了解，方便的话可以说说哪个时间更合适。"}}
	planned, err := ReduceV4Dialogue(input)
	if err != nil || planned.Status != V4DialogueActionsPlanned || len(planned.Actions) != 1 ||
		planned.Actions[0].Kind != V4ActionInterviewRejectionReply || planned.Actions[0].CardMessageSeq != 20 ||
		!strings.Contains(planned.Actions[0].ActionKey, "card:20") {
		t.Fatalf("拒面卡回执没有绑定精确卡片: decision=%+v err=%v", planned, err)
	}

	state.InterviewGroups[0].RejectionReceiptSent = true
	input.State = state
	input.Reply = ReplyAdvice{State: AdviceAbsent}
	done, err := ReduceV4Dialogue(input)
	if err != nil || done.Status != V4DialogueNoAction || done.NextAdvice != V4AdviceNone {
		t.Fatalf("每卡回执已发后不得再调用 AI: decision=%+v err=%v", done, err)
	}
}

func TestV4DialogueReplyFailureAndInvalidOutputTurnManualWithoutAction(t *testing.T) {
	base := V4DialogueInput{
		State: activeV4DialogueState(), Requirement: V4DialogueReplyKnownInterested,
		Turn: FrozenTurnFacts{TurnID: "turn-reply-failure"}, Intent: IntentAdvice{State: AdviceAbsent},
	}
	base.Reply = ReplyAdvice{State: AdviceFailed}
	failed, err := ReduceV4Dialogue(base)
	if err != nil || failed.Status != V4DialogueManualRequired || failed.ManualReason != V4ManualReplyFailed || len(failed.Actions) != 0 {
		t.Fatalf("reply 失败未收敛到人工: decision=%+v err=%v", failed, err)
	}

	base.Reply = ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: strings.Repeat("长", m5ai.SendTextMaxUTF8Bytes)}}
	invalid, err := ReduceV4Dialogue(base)
	if err != nil || invalid.Status != V4DialogueManualRequired || invalid.ManualReason != V4ManualReplyInvalid || len(invalid.Actions) != 0 {
		t.Fatalf("非法 reply 未收敛到人工: decision=%+v err=%v", invalid, err)
	}
}

func TestV4DialogueRejectsAdviceOnNoActionAndBranchMismatches(t *testing.T) {
	state := activeV4DialogueState()
	cases := []V4DialogueInput{
		{State: state, Requirement: V4DialogueNone, Intent: IntentAdvice{State: AdviceOK}},
		{State: state, Requirement: V4DialogueReplyKnownInterested, Turn: FrozenTurnFacts{TurnID: "turn"}, Intent: IntentAdvice{State: AdviceOK}, Reply: ReplyAdvice{State: AdviceAbsent}},
		{State: state, Requirement: V4DialogueServiceReply, Turn: FrozenTurnFacts{TurnID: "turn"}, Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent}},
		{State: state, Requirement: V4DialogueInterviewRejectionReceipt, Turn: FrozenTurnFacts{TurnID: "turn"}, CardMessageSeq: 99, Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent}},
	}
	for index, input := range cases {
		if _, err := ReduceV4Dialogue(input); !errors.Is(err, ErrInvalidV4StateTransition) {
			t.Fatalf("分支错配[%d]没有响亮失败: %v", index, err)
		}
	}
}

func TestRoundUpToInterviewTimeGrid(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"整点不变", 1785391200000, 1785391200000},           // 2026-07-30 14:00:00
		{"格点不变", 1785391500000, 1785391500000},           // 14:05:00
		{"14:03 进到 14:05", 1785391380000, 1785391500000}, // 14:03:00
		{"14:01 进到 14:05", 1785391260000, 1785391500000}, // 14:01:00
		{"零点后一毫秒进整格", 1785391200001, 1785391500000},      // 14:00:00.001
		{"14:59 进到 15:00", 1785394740000, 1785394800000}, // 14:59:00
	}
	for _, tc := range cases {
		if got := roundUpToInterviewTimeGrid(tc.in); got != tc.want {
			t.Fatalf("%s: roundUpToInterviewTimeGrid(%d)=%d, want %d", tc.name, tc.in, got, tc.want)
		}
	}
	if V4InterviewDurationMs%V4InterviewTimeGridMs != 0 {
		t.Fatalf("面试时长必须落在平台时间格上: duration=%d grid=%d", V4InterviewDurationMs, V4InterviewTimeGridMs)
	}
}

// 菜单与事后裁决必须同源(规格 v4 §五「客户端渲染期追加块」)。【本轮可选
// 动作】块照 V4ReplyActionMenu 渲染,而 planV4ReplyActions 是真正说了算的
// 那一道;两边一旦走岔,症状是"模型照块填了、脑还是拒",在生产里极难查。
// 这个对拍遍历状态空间,断言"菜单说能填" 恰好等价于 "裁决接受该动作"。
func TestV4ReplyActionMenuMatchesPlanDecision(t *testing.T) {
	const hitMeetingTime = "7月14日09:00"
	slots := []string{"2026-07-14 09:00:00", "2026-07-14 14:00:00"}

	wechatLines := []V4WechatStatus{
		V4WechatNotInvited, V4WechatInvited, V4WechatExchanged,
	}
	mainStatuses := []V4MainStatus{
		V4StatusGreeted, V4StatusCommunicating, V4StatusInvited,
		V4StatusInterviewed, V4StatusEnded, V4StatusEliminated,
	}

	for _, allowSuggested := range []bool{true, false} {
		for _, wechat := range wechatLines {
			for _, mainStatus := range mainStatuses {
				for _, withSlots := range []bool{true, false} {
					for _, withMessages := range []bool{true, false} {
						state := activeV4DialogueState()
						state.MainStatus = mainStatus
						state.WechatState = wechat

						turn := ordinaryTurn()
						if !withMessages {
							turn.Messages = nil
						}
						if withSlots {
							turn.RecommendedSlots = slots
						}

						menu := V4ReplyActionMenu(state, turn, allowSuggested)

						_, wechatAccepted := planV4ReplyActions(state, turn, m5ai.ReplySuggestion{
							Phrases: []string{"那咱们加个微信细聊。"},
							Text:    "那咱们加个微信细聊。",
							Action:  m5ai.ReplyActionInviteWechat,
						}, allowSuggested)
						if wechatAccepted != menu.AllowInviteWechat {
							t.Fatalf(
								"换微信判据漂移: menu=%t plan=%t (allowSuggested=%t wechat=%s main=%s slots=%t msgs=%t)",
								menu.AllowInviteWechat, wechatAccepted,
								allowSuggested, wechat, mainStatus, withSlots, withMessages,
							)
						}

						_, meetingAccepted := planV4ReplyActions(state, turn, m5ai.ReplySuggestion{
							Phrases:     []string{"那就约这个时间。"},
							Text:        "那就约这个时间。",
							Action:      m5ai.ReplyActionStartOnlineMeeting,
							MeetingTime: hitMeetingTime,
						}, allowSuggested)
						if meetingAccepted != menu.AllowStartMeeting {
							t.Fatalf(
								"线上会议判据漂移: menu=%t plan=%t (allowSuggested=%t wechat=%s main=%s slots=%t msgs=%t)",
								menu.AllowStartMeeting, meetingAccepted,
								allowSuggested, wechat, mainStatus, withSlots, withMessages,
							)
						}
					}
				}
			}
		}
	}
}

// 微信线三态各自有独立措辞,不得合并:"已经发出邀请"和"已经换到号"对模型
// 是两件不同的事实,说错就是拿假事实喂它。
func TestV4ReplyActionMenuReportsWechatLineVerbatim(t *testing.T) {
	for _, testCase := range []struct {
		state V4WechatStatus
		want  m5ai.ReplyMenuWechatLine
	}{
		{V4WechatNotInvited, m5ai.ReplyMenuWechatNotInvited},
		{V4WechatInvited, m5ai.ReplyMenuWechatInvited},
		{V4WechatExchanged, m5ai.ReplyMenuWechatExchanged},
	} {
		state := activeV4DialogueState()
		state.WechatState = testCase.state
		if got := V4ReplyActionMenu(state, ordinaryTurn(), true).WechatLine; got != testCase.want {
			t.Fatalf("微信线措辞映射错误: state=%s got=%s want=%s", testCase.state, got, testCase.want)
		}
	}
}
