package communication

import (
	"errors"
	"reflect"
	"testing"
)

func textPointer(value string) *string { return &value }

func TestNormalizeLedgerMessagePromotesOnlyStableBusinessSemantics(t *testing.T) {
	cases := []struct {
		name string
		fact LedgerMessageFact
		want BusinessEvent
	}{
		{
			name: "candidate text",
			fact: LedgerMessageFact{Seq: 1, Direction: "in", Kind: "text", Text: textPointer("候选人文本"), Origin: "external"},
			want: BusinessEvent{Key: "message:1", Kind: EventCandidateExpressionReceived, Source: EventSourceMessage, MessageSeq: 1, ExpressionKind: ExpressionText, Text: "候选人文本"},
		},
		{
			name: "candidate media",
			fact: LedgerMessageFact{Seq: 2, Direction: "in", Kind: "voice", Origin: "external"},
			want: BusinessEvent{Key: "message:2", Kind: EventCandidateExpressionReceived, Source: EventSourceMessage, MessageSeq: 2, ExpressionKind: ExpressionVoice},
		},
		{
			name: "resume semantic card",
			fact: LedgerMessageFact{Seq: 3, Direction: "in", Kind: "card", CardType: "resumeAttachment", CardState: "unknown", Origin: "external"},
			want: BusinessEvent{Key: "message:3", Kind: EventResumeSubmitted, Source: EventSourceMessage, MessageSeq: 3},
		},
		{
			name: "candidate asks for wechat",
			fact: LedgerMessageFact{Seq: 4, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "pending", Origin: "external"},
			want: BusinessEvent{Key: "message:4", Kind: EventWechatRequested, Source: EventSourceMessage, MessageSeq: 4},
		},
		{
			name: "human outbound",
			fact: LedgerMessageFact{Seq: 5, Direction: "out", Kind: "text", Text: textPointer("人工文本"), Origin: "external"},
			want: BusinessEvent{Key: "message:5", Kind: EventHumanOutboundObserved, Source: EventSourceMessage, MessageSeq: 5},
		},
		{
			name: "automatic outbound",
			fact: LedgerMessageFact{Seq: 6, Direction: "out", Kind: "text", Text: textPointer("自动文本"), Origin: "self"},
			want: BusinessEvent{Key: "message:6", Kind: EventAutomaticOutboundObserved, Source: EventSourceMessage, MessageSeq: 6},
		},
		{
			name: "outbound interview card",
			fact: LedgerMessageFact{Seq: 7, Direction: "out", Kind: "card", CardType: "interviewInvite", CardState: "pending", Origin: "external"},
			want: BusinessEvent{Key: "message:7", Kind: EventInterviewInvited, Source: EventSourceMessage, MessageSeq: 7},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeLedgerMessage(tc.fact)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("事件转换错误: got=%+v want=%+v err=%v", got, tc.want, err)
			}
		})
	}
}

func TestNormalizeLedgerMessageKeepsUnsupportedShapesConservative(t *testing.T) {
	cases := []struct {
		name string
		fact LedgerMessageFact
		code string
	}{
		{name: "system", fact: LedgerMessageFact{Seq: 1, Direction: "in", Kind: "system", Origin: "external"}},
		{name: "other card", fact: LedgerMessageFact{Seq: 2, Direction: "in", Kind: "card", CardType: "other", CardState: "unknown", Origin: "external"}, code: "unsupportedInboundCard"},
		{name: "accepted inbound card is not a request", fact: LedgerMessageFact{Seq: 3, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "accepted", Origin: "external"}, code: "inboundWechatCardState"},
		{name: "unknown inbound card is not a request", fact: LedgerMessageFact{Seq: 5, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "unknown", Origin: "external"}, code: "inboundWechatCardState"},
		{name: "empty text", fact: LedgerMessageFact{Seq: 4, Direction: "in", Kind: "text", Text: textPointer("  "), Origin: "external"}, code: "emptyInboundText"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeLedgerMessage(tc.fact)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "system" {
				if got.Kind != EventSystemNotice {
					t.Fatalf("system 应是无副作用通知: %+v", got)
				}
				return
			}
			if got.Kind != EventUnknownPlatform || got.ConservativeCode != tc.code {
				t.Fatalf("未支持形态不应获得自动权限: %+v", got)
			}
		})
	}
}

func TestNormalizeCardTransitionPromotesAcceptedAndRejectedFacts(t *testing.T) {
	cases := []struct {
		fact LedgerCardTransitionFact
		kind BusinessEventKind
	}{
		{LedgerCardTransitionFact{MessageSeq: 9, CardType: "interviewInvite", FromState: "pending", ToState: "accepted"}, EventInterviewAccepted},
		{LedgerCardTransitionFact{MessageSeq: 9, CardType: "interviewInvite", FromState: "pending", ToState: "rejected"}, EventInterviewRejected},
		{LedgerCardTransitionFact{MessageSeq: 10, CardType: "wechatExchange", FromState: "pending", ToState: "accepted"}, EventWechatExchanged},
	}
	for _, tc := range cases {
		got, err := NormalizeCardTransition(tc.fact)
		if err != nil || got.Kind != tc.kind || got.Source != EventSourceCardTransition || got.MessageSeq != tc.fact.MessageSeq {
			t.Fatalf("卡片跃迁转换错误: fact=%+v got=%+v err=%v", tc.fact, got, err)
		}
		repeated, err := NormalizeCardTransition(tc.fact)
		if err != nil || !reflect.DeepEqual(got, repeated) {
			t.Fatalf("同一跃迁必须可重放: first=%+v repeated=%+v err=%v", got, repeated, err)
		}
	}

	unknown, err := NormalizeCardTransition(LedgerCardTransitionFact{
		MessageSeq: 11, CardType: "wechatExchange", FromState: "pending", ToState: "rejected",
	})
	if err != nil || unknown.Kind != EventUnknownPlatform || unknown.ConservativeCode != "wechatCardTransition" {
		t.Fatalf("未获业务语义的跃迁必须保守: %+v err=%v", unknown, err)
	}
}

func TestNormalizeBusinessEventRejectsBrokenNeutralFacts(t *testing.T) {
	invalidMessages := []LedgerMessageFact{
		{Seq: 0, Direction: "in", Kind: "text", Text: textPointer("x"), Origin: "external"},
		{Seq: 1, Direction: "future", Kind: "text", Text: textPointer("x"), Origin: "external"},
		{Seq: 1, Direction: "in", Kind: "future", Origin: "external"},
		{Seq: 1, Direction: "in", Kind: "text", Text: textPointer("x"), CardType: "other", Origin: "external"},
		{Seq: 1, Direction: "in", Kind: "card", CardType: "future", CardState: "unknown", Origin: "external"},
		{Seq: 1, Direction: "in", Kind: "card", CardType: "other", CardState: "future", Origin: "external"},
		{Seq: 1, Direction: "in", Kind: "text", Text: textPointer("x"), Origin: "future"},
	}
	for index, fact := range invalidMessages {
		if _, err := NormalizeLedgerMessage(fact); !errors.Is(err, ErrInvalidBusinessEventInput) {
			t.Fatalf("非法消息事实[%d] 未响亮失败: %v", index, err)
		}
	}
	invalidTransitions := []LedgerCardTransitionFact{
		{MessageSeq: 0, CardType: "interviewInvite", FromState: "pending", ToState: "accepted"},
		{MessageSeq: 1, CardType: "future", FromState: "pending", ToState: "accepted"},
		{MessageSeq: 1, CardType: "interviewInvite", FromState: "future", ToState: "accepted"},
		{MessageSeq: 1, CardType: "interviewInvite", FromState: "pending", ToState: "pending"},
	}
	for index, fact := range invalidTransitions {
		if _, err := NormalizeCardTransition(fact); !errors.Is(err, ErrInvalidBusinessEventInput) {
			t.Fatalf("非法卡片事实[%d] 未响亮失败: %v", index, err)
		}
	}
}
