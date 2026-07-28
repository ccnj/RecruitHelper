package store

import "testing"

// 混合输入轮(规格 §五,2026-07-28)批 A 的 canonical 形态判定:文字与简历卡
// 任意混合、任意条数合法;其他卡型、媒体、空文字仍整轮出局。
func TestDialogueTurnInputKindOfMixedShapes(t *testing.T) {
	text := func(seq int64, body string) Message {
		return Message{Seq: seq, Direction: "in", Kind: "text", Text: &body, Origin: "external"}
	}
	resume := func(seq int64) Message {
		return Message{Seq: seq, Direction: "in", Kind: "card", CardType: "resumeAttachment", CardState: "unknown", Origin: "external"}
	}
	empty := ""
	tests := []struct {
		name    string
		inbound []Message
		kind    DialogueTurnInputKind
		ok      bool
	}{
		{name: "all_text", inbound: []Message{text(2, "在的"), text(3, "想了解下")}, kind: DialogueTurnInputText, ok: true},
		{name: "single_resume_card", inbound: []Message{resume(2)}, kind: DialogueTurnInputResumeAttachment, ok: true},
		{name: "text_card_text", inbound: []Message{text(2, "请问做几休几"), resume(3), text(4, "工作时间是")}, kind: DialogueTurnInputResumeAttachment, ok: true},
		{name: "double_resume_cards", inbound: []Message{resume(2), resume(3)}, kind: DialogueTurnInputResumeAttachment, ok: true},
		{name: "cards_then_text", inbound: []Message{resume(2), resume(3), text(4, "请查收")}, kind: DialogueTurnInputResumeAttachment, ok: true},
		{name: "empty_turn", inbound: nil, ok: false},
		{name: "empty_text_rejected", inbound: []Message{text(2, " "), resume(3)}, ok: false},
		{name: "nil_text_rejected", inbound: []Message{{Seq: 2, Direction: "in", Kind: "text", Text: nil, Origin: "external"}}, ok: false},
		{name: "media_with_card_rejected", inbound: []Message{{Seq: 2, Direction: "in", Kind: "image", Origin: "external"}, resume(3)}, ok: false},
		{name: "wechat_pending_mix_batch_b", inbound: []Message{text(2, "加个微信"), {Seq: 3, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "pending", Origin: "external"}}, kind: DialogueTurnInputWechatCard, ok: true},
		{name: "wechat_accepted_mix_batch_b", inbound: []Message{{Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "accepted", Origin: "external"}, text(3, "加好了")}, kind: DialogueTurnInputWechatCard, ok: true},
		{name: "wechat_with_resume_labels_wechat", inbound: []Message{resume(2), {Seq: 3, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "accepted", Origin: "external"}}, kind: DialogueTurnInputWechatCard, ok: true},
		{name: "wechat_self_origin_rejected", inbound: []Message{{Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "pending", Origin: "self"}}, ok: false},
		{name: "wechat_unknown_state_rejected", inbound: []Message{{Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "unknown", Origin: "external"}}, ok: false},
		{name: "interview_accepted_mix_batch_c", inbound: []Message{{Seq: 2, Direction: "in", Kind: "card", CardType: "interviewInvite", CardState: "accepted", Origin: "external"}, text(3, "到时见")}, kind: DialogueTurnInputInterviewAccepted, ok: true},
		{name: "interview_accepted_with_wechat_labels_wechat", inbound: []Message{{Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "accepted", Origin: "external"}, {Seq: 3, Direction: "in", Kind: "card", CardType: "interviewInvite", CardState: "accepted", Origin: "external"}}, kind: DialogueTurnInputWechatCard, ok: true},
		{name: "interview_pending_card_rejected", inbound: []Message{{Seq: 2, Direction: "in", Kind: "card", CardType: "interviewInvite", CardState: "unknown", Origin: "external"}, text(3, "看到了")}, ok: false},
		{name: "self_origin_card_rejected", inbound: []Message{text(2, "你好"), {Seq: 3, Direction: "in", Kind: "card", CardType: "resumeAttachment", CardState: "unknown", Origin: "self"}}, ok: false},
		{name: "non_unknown_card_state_rejected", inbound: []Message{{Seq: 2, Direction: "in", Kind: "card", CardType: "resumeAttachment", CardState: "accepted", Origin: "external"}}, ok: false},
		{name: "out_direction_rejected", inbound: []Message{{Seq: 2, Direction: "out", Kind: "text", Text: &empty, Origin: "self"}}, ok: false},
		{name: "non_increasing_seq_rejected", inbound: []Message{text(3, "先到"), resume(3)}, ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, ok := DialogueTurnInputKindOf(test.inbound)
			if ok != test.ok || (test.ok && kind != test.kind) {
				t.Fatalf("形态判定不符合混合输入轮规格: kind=%q ok=%v", kind, ok)
			}
		})
	}
}
