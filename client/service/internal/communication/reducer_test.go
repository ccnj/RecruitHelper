package communication

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"recruithelper/client/service/internal/m5ai"
)

func ordinaryTurn() FrozenTurnFacts {
	return FrozenTurnFacts{
		TurnID: "turn-fixture",
		Messages: []FrozenInboundMessage{
			{Seq: 2, Kind: FrozenMessageText, Text: "您好，我想了解一下"},
			{Seq: 3, Kind: FrozenMessageText, Text: "工作地点在哪里？"},
		},
	}
}

func TestReducerWaitsForEachIndependentAdviceInOrder(t *testing.T) {
	decision, err := Reduce(ReduceInput{
		Turn:   ordinaryTurn(),
		Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent},
	})
	if err != nil || decision.TurnStatus != TurnCollected || decision.NextAdvice != m5ai.PurposeIntent || decision.Action != nil {
		t.Fatalf("未分类轮没有停在独立 intent 用途: decision=%+v err=%v", decision, err)
	}

	decision, err = Reduce(ReduceInput{
		Turn:   ordinaryTurn(),
		Intent: IntentAdvice{State: AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: m5ai.IntentInterested}},
		Reply:  ReplyAdvice{State: AdviceAbsent},
	})
	if err != nil || decision.TurnStatus != TurnClassified || decision.NextAdvice != m5ai.PurposeReply ||
		decision.IntentLabel != m5ai.IntentInterested || decision.IntentSource != IntentSourceLLM || decision.Action != nil {
		t.Fatalf("分类后没有停在独立 reply 用途: decision=%+v err=%v", decision, err)
	}
}

func TestReducerIntentFailureFallsBackOnceThenAllowsReply(t *testing.T) {
	input := ReduceInput{
		Turn:   ordinaryTurn(),
		Intent: IntentAdvice{State: AdviceFailed},
		Reply:  ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "可以的，工作地点在上海。"}},
	}
	decision, err := Reduce(input)
	if err != nil || decision.TurnStatus != TurnAdviceReady || decision.IntentLabel != m5ai.IntentNeutral ||
		decision.IntentSource != IntentSourceLLMFailureFallback || decision.NextAdvice != "" || decision.Action == nil {
		t.Fatalf("intent failure 未按 neutral fallback 收敛: decision=%+v err=%v", decision, err)
	}
	if decision.Action.Kind != CommunicationActionReplyText || decision.Action.Status != CommunicationActionPlanned ||
		decision.Action.TurnID != ordinaryTurn().TurnID || decision.Action.Text != input.Reply.Suggestion.Text {
		t.Fatalf("fallback 后 action 计划错误: %+v", decision.Action)
	}

	repeated, err := Reduce(input)
	if err != nil || !reflect.DeepEqual(decision, repeated) {
		t.Fatalf("同一冻结事实重复 reduce 不确定: first=%+v repeated=%+v err=%v", decision, repeated, err)
	}
}

func TestReducerRejectedAlwaysTurnsManualWithoutAction(t *testing.T) {
	decision, err := Reduce(ReduceInput{
		Turn:   ordinaryTurn(),
		Intent: IntentAdvice{State: AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: m5ai.IntentRejected}},
		// 即使上游错误地附带了 reply，rejected 也绝不把它转成动作。
		Reply: ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "不应发送"}},
	})
	if err != nil || decision.TurnStatus != TurnManualRequired || decision.ManualReason != ManualIntentRejected ||
		decision.IntentLabel != m5ai.IntentRejected || decision.IntentSource != IntentSourceLLM ||
		decision.Action != nil || decision.NextAdvice != "" {
		t.Fatalf("rejected 未保守转人工: decision=%+v err=%v", decision, err)
	}
}

func TestReducerMediaAndUnsupportedSemanticsTurnManualBeforeAdvice(t *testing.T) {
	cases := []struct {
		name   string
		kind   FrozenMessageKind
		reason ManualReason
	}{
		{name: "image", kind: FrozenMessageImage, reason: ManualUnsupportedMedia},
		{name: "voice", kind: FrozenMessageVoice, reason: ManualUnsupportedMedia},
		{name: "file", kind: FrozenMessageFile, reason: ManualUnsupportedMedia},
		{name: "card", kind: FrozenMessageCard, reason: ManualUnsupportedSemantic},
		{name: "unknown", kind: FrozenMessageKind("future-kind"), reason: ManualUnsupportedSemantic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := Reduce(ReduceInput{
				Turn:   FrozenTurnFacts{TurnID: "turn-unsupported", Messages: []FrozenInboundMessage{{Seq: 1, Kind: tc.kind}}},
				Intent: IntentAdvice{State: AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: m5ai.IntentInterested}},
				Reply:  ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "不应发送"}},
			})
			if err != nil || decision.TurnStatus != TurnManualRequired || decision.ManualReason != tc.reason ||
				decision.IntentLabel != "" || decision.Action != nil || decision.NextAdvice != "" {
				t.Fatalf("未激活消息没有在建议前转人工: decision=%+v err=%v", decision, err)
			}
		})
	}
}

func TestReducerReplyFailureOrInvalidTextTurnsManual(t *testing.T) {
	base := ReduceInput{
		Turn:   ordinaryTurn(),
		Intent: IntentAdvice{State: AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: m5ai.IntentNeutral}},
	}

	failed := base
	failed.Reply = ReplyAdvice{State: AdviceFailed}
	decision, err := Reduce(failed)
	if err != nil || decision.TurnStatus != TurnManualRequired || decision.ManualReason != ManualReplyFailed ||
		decision.IntentLabel != m5ai.IntentNeutral || decision.Action != nil || decision.NextAdvice != "" {
		t.Fatalf("reply failure 未转人工: decision=%+v err=%v", decision, err)
	}

	invalid := base
	invalid.Reply = ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: strings.Repeat("长", m5ai.SendTextMaxUTF8Bytes)}}
	decision, err = Reduce(invalid)
	if err != nil || decision.TurnStatus != TurnManualRequired || decision.ManualReason != ManualReplyInvalid ||
		decision.Action != nil {
		t.Fatalf("非法 reply 文本未转人工: decision=%+v err=%v", decision, err)
	}
}

func TestReducerCreatesExactlyOneReplyTextPlanForInterestedOrNeutral(t *testing.T) {
	for _, label := range []m5ai.IntentLabel{m5ai.IntentInterested, m5ai.IntentNeutral} {
		t.Run(string(label), func(t *testing.T) {
			decision, err := Reduce(ReduceInput{
				Turn:   ordinaryTurn(),
				Intent: IntentAdvice{State: AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: label}},
				Reply:  ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "您好\n方便聊聊具体情况吗？"}},
			})
			if err != nil || decision.TurnStatus != TurnAdviceReady || decision.IntentLabel != label ||
				decision.IntentSource != IntentSourceLLM || decision.Action == nil ||
				decision.Action.Kind != CommunicationActionReplyText || decision.Action.Status != CommunicationActionPlanned {
				t.Fatalf("普通非拒绝轮 action 计划错误: decision=%+v err=%v", decision, err)
			}
		})
	}
}

func TestReducerKeepsJoinedCompatibilitySummaryForOrderedPhrases(t *testing.T) {
	decision, err := Reduce(ReduceInput{
		Turn:   ordinaryTurn(),
		Intent: IntentAdvice{State: AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: m5ai.IntentInterested}},
		Reply: ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{
			Phrases: []string{"第一句", "第二句", "第三句"},
			Text:    "第一句\n第二句\n第三句",
		}},
	})
	if err != nil || decision.TurnStatus != TurnAdviceReady || decision.Action == nil ||
		decision.Action.Text != "第一句\n第二句\n第三句" {
		t.Fatalf("归约器没有保留多气泡的兼容摘要: decision=%+v err=%v", decision, err)
	}
}

func TestReducerInvalidIntentOutputUsesApprovedFallback(t *testing.T) {
	decision, err := Reduce(ReduceInput{
		Turn:   ordinaryTurn(),
		Intent: IntentAdvice{State: AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: m5ai.IntentLabel("unexpected")}},
		Reply:  ReplyAdvice{State: AdviceAbsent},
	})
	if err != nil || decision.TurnStatus != TurnClassified || decision.IntentLabel != m5ai.IntentNeutral ||
		decision.IntentSource != IntentSourceLLMFailureFallback || decision.NextAdvice != m5ai.PurposeReply {
		t.Fatalf("非法 intent 输出未按失败口径 neutral fallback: decision=%+v err=%v", decision, err)
	}
}

func TestReducerRejectsBrokenFrozenBoundariesAndAdviceOrder(t *testing.T) {
	cases := []ReduceInput{
		{Turn: FrozenTurnFacts{TurnID: "", Messages: ordinaryTurn().Messages}, Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent}},
		{Turn: FrozenTurnFacts{TurnID: "turn-empty"}, Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent}},
		{Turn: FrozenTurnFacts{TurnID: "turn-seq", Messages: []FrozenInboundMessage{{Seq: 2, Kind: FrozenMessageText, Text: "一"}, {Seq: 2, Kind: FrozenMessageText, Text: "二"}}}, Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceAbsent}},
		{Turn: ordinaryTurn(), Intent: IntentAdvice{State: AdviceAbsent}, Reply: ReplyAdvice{State: AdviceOK, Suggestion: m5ai.ReplySuggestion{Text: "越序"}}},
		{Turn: ordinaryTurn(), Intent: IntentAdvice{State: AdviceState("future")}, Reply: ReplyAdvice{State: AdviceAbsent}},
	}
	for index, input := range cases {
		if _, err := Reduce(input); !errors.Is(err, ErrInvalidReducerInput) {
			t.Fatalf("broken input[%d] 未响亮失败: %v", index, err)
		}
	}
}
