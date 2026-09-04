package dispatch

import (
	"context"
	"encoding/json"
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

// 卡上不带邀面参数的平台(BOSS,平台事实 §四):验证器命中的是常量投影行,交出的
// interview 为 nil。此前 verification.go 要求 interview 非空,这类卡一进验证读就
// 必然 miss、用尽轮数转 suspect(2026-09-04 场景三出口审查优化级第 2 条)。
type neutralInterviewCardVerifier struct {
	calls int
	hash  string
}

func (v *neutralInterviewCardVerifier) Verify(
	_ context.Context,
	_ VerificationRequest,
) (VerificationObservation, error) {
	v.calls++
	return VerificationObservation{
		Confirmed:   true,
		ContentHash: v.hash,
		SourceKey: "0123456789abcdef0123456789abcdef" +
			"0123456789abcdef0123456789abcdef",
		ObservedAt: 1_722_000_000_000,
		Reason:     "最近窗口命中严格卡片正证(同类取最新)",
	}, nil
}

func seedUnconfirmedInviteCard(
	t *testing.T, interview protocol.InterviewDetails,
) (*Dispatcher, *store.Store, store.ConversationKey, *store.CmdRecord) {
	t.Helper()
	d, st, hand := newDisp(t)
	key := seedSendTarget(t, st, hand, "acct-card-neutral", "conv-card-neutral")
	args := protocol.ChatSendInviteCardArgs{ConversationRef: "conv-card-neutral", Interview: interview}
	_, command := seedCardEffectIntent(
		t, st, key, protocol.PrimChatSendInviteCard, args,
		syncledger.InterviewInviteContentHash(interview.StartsAt, interview.EndsAt, string(interview.Method)), 1,
	)
	outcome, _, err := d.applyResultMessage(
		"hand-send", "result-card-neutral",
		protocol.ResultBody{
			Ref: command.MsgID, Status: protocol.ResultStatusFailed,
			Error: &protocol.ErrorBody{
				Code: protocol.ErrCodePostconditionUnconfirmed, Message: "只点击了一次发送,但未确认出站邀面行",
				Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectPossible,
			},
		},
	)
	if err != nil || outcome != ocEffSuspect {
		t.Fatalf("邀面卡 possible 应进入验证: outcome=%v err=%v", outcome, err)
	}
	return d, st, key, command
}

func TestInviteCardVerificationAcceptsNeutralProjectionWithoutInterview(t *testing.T) {
	neutral := syncledger.InterviewInviteNeutralContentHash()
	tests := []struct {
		name      string
		interview protocol.InterviewDetails
	}{
		{
			name: "wechatVideo", interview: protocol.InterviewDetails{
				StartsAt: 1_722_000_000_000, EndsAt: 1_722_003_600_000, Method: protocol.InterviewMethodWechatVideo,
			},
		},
		{
			name: "onsite", interview: protocol.InterviewDetails{
				StartsAt: 1_722_000_000_000, Method: protocol.InterviewMethodOnsite,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, st, key, command := seedUnconfirmedInviteCard(t, test.interview)
			verifier := &neutralInterviewCardVerifier{hash: neutral}
			d.SetEffectVerifier(verifier)
			d.verifyEffect(context.Background(), command.MsgID)

			current, err := st.CmdByMsgID(command.MsgID)
			if err != nil || current == nil || current.Status != store.CmdOk {
				t.Fatalf("常量投影正证必须自动补记 ok,不得 miss: cmd=%+v err=%v", current, err)
			}
			intent, _ := st.EffectIntentByID(command.IntentID)
			if intent == nil || intent.Status != store.EffectIntentOk {
				t.Fatalf("意图应终局 ok: %+v", intent)
			}
			if verifier.calls != 1 {
				t.Fatalf("首轮即应收编: calls=%d", verifier.calls)
			}
			messages, _ := st.MessagesForConversation(key)
			var card *store.Message
			for i := range messages {
				if messages[i].CardType == "interviewInvite" {
					card = &messages[i]
				}
			}
			if card == nil {
				t.Fatalf("验证成功应入账一条邀面卡事实: %+v", messages)
			}
			if card.Direction != "out" || card.ContentHash != neutral || card.CardState != "unknown" {
				t.Fatalf("账本行应取手侧观察到的常量投影、状态 unknown: %+v", card)
			}
			// 面试字段与派发 ok 收编同口径:取意图参数,onsite 的 endsAt 缺席。
			if card.InterviewStartsAtMs == nil || *card.InterviewStartsAtMs != test.interview.StartsAt ||
				card.InterviewMethod == nil || *card.InterviewMethod != string(test.interview.Method) {
				t.Fatalf("账本行面试字段应取原 args: %+v", card)
			}
			if test.interview.Method == protocol.InterviewMethodOnsite {
				if card.InterviewEndsAtMs != nil {
					t.Fatalf("线下卡 endsAt 必须缺席: %+v", card)
				}
			} else if card.InterviewEndsAtMs == nil || *card.InterviewEndsAtMs != test.interview.EndsAt {
				t.Fatalf("线上卡 endsAt 应取原 args: %+v", card)
			}
			var result protocol.ResultBody
			if err := json.Unmarshal([]byte(current.ResultBody), &result); err != nil {
				t.Fatalf("补记的 result 应可解析: %v", err)
			}
			var data protocol.ChatSendInviteCardData
			if err := json.Unmarshal(result.Data, &data); err != nil {
				t.Fatalf("补记的 data 应可解析: %v", err)
			}
			if data.Interview != test.interview || data.ContentHash != neutral || data.ConversationRef != "conv-card-neutral" {
				t.Fatalf("补记 data 应回显原 args 与常量投影: %+v", data)
			}
		})
	}
}

// 既无邀面参数、hash 又不是常量投影:不是本次的卡,仍按 miss 计轮。
func TestInviteCardVerificationRejectsNilInterviewWithParamHash(t *testing.T) {
	interview := protocol.InterviewDetails{
		StartsAt: 1_722_000_000_000, EndsAt: 1_722_003_600_000, Method: protocol.InterviewMethodWechatVideo,
	}
	d, st, key, command := seedUnconfirmedInviteCard(t, interview)
	verifier := &neutralInterviewCardVerifier{
		hash: syncledger.InterviewInviteContentHash(interview.StartsAt, interview.EndsAt, string(interview.Method)),
	}
	d.SetEffectVerifier(verifier)
	d.verifyEffect(context.Background(), command.MsgID)
	current, _ := st.CmdByMsgID(command.MsgID)
	if current == nil || current.Status != store.CmdVerifying || current.VerificationN != 1 {
		t.Fatalf("参数配方 hash 却无参数:应记 miss 继续 verifying: %+v", current)
	}
	messages, _ := st.MessagesForConversation(key)
	for _, message := range messages {
		if message.CardType == "interviewInvite" {
			t.Fatalf("miss 不得入账卡片事实: %+v", message)
		}
	}
}
