package dispatch

import (
	"context"
	"sync"
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type recordingCardVerifier struct {
	mu       sync.Mutex
	requests []VerificationRequest
}

func (v *recordingCardVerifier) Verify(
	_ context.Context,
	req VerificationRequest,
) (VerificationObservation, error) {
	v.mu.Lock()
	v.requests = append(v.requests, req)
	v.mu.Unlock()
	return VerificationObservation{Reason: "本轮未取得唯一严格卡片正证"}, nil
}

func (v *recordingCardVerifier) snapshot() []VerificationRequest {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]VerificationRequest(nil), v.requests...)
}

func TestCardVerificationMissesThreeRoundsThenSuspectWithoutNewAction(t *testing.T) {
	tests := []struct {
		name      string
		primitive string
		args      any
		hash      string
		assertReq func(*testing.T, VerificationRequest)
	}{
		{
			name: "wechat invite", primitive: protocol.PrimChatSendWechatInvite,
			args: protocol.ChatSendWechatInviteArgs{ConversationRef: "conv-card-verify-miss"},
			hash: syncledger.WechatExchangeContentHash(),
			assertReq: func(t *testing.T, request VerificationRequest) {
				t.Helper()
				if request.WechatInviteArgs == nil ||
					request.WechatInviteArgs.ConversationRef != "conv-card-verify-miss" ||
					request.InviteCardArgs != nil {
					t.Fatalf("换微信验证请求未保留原 args: %+v", request)
				}
			},
		},
		{
			name: "interview invite", primitive: protocol.PrimChatSendInviteCard,
			args: protocol.ChatSendInviteCardArgs{
				ConversationRef: "conv-card-verify-miss",
				Interview: protocol.InterviewDetails{
					StartsAt: 1_722_000_000_000, EndsAt: 1_722_001_800_000,
					Method: protocol.InterviewMethodWechatVideo,
				},
			},
			hash: syncledger.InterviewInviteContentHash(
				1_722_000_000_000, 1_722_001_800_000, "wechatVideo",
			),
			assertReq: func(t *testing.T, request VerificationRequest) {
				t.Helper()
				if request.InviteCardArgs == nil ||
					request.InviteCardArgs.ConversationRef != "conv-card-verify-miss" ||
					request.InviteCardArgs.Interview.EndsAt != 1_722_001_800_000 ||
					request.WechatInviteArgs != nil {
					t.Fatalf("邀面验证请求未保留原 args: %+v", request)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, st, hand := newDisp(t)
			key := seedSendTarget(t, st, hand, "acct-card-verify-miss", "conv-card-verify-miss")
			_, command := seedCardEffectIntent(
				t, st, key, test.primitive, test.args, test.hash, 1,
			)
			outcome, _, err := d.applyResultMessage(
				"hand-send", "result-card-verify-miss",
				protocol.ResultBody{
					Ref: command.MsgID, Status: protocol.ResultStatusFailed,
					Error: &protocol.ErrorBody{
						Code: protocol.ErrCodeInternalHand, Message: "click outcome unknown",
						Retryable: protocol.RetryableNo, SideEffect: protocol.SideEffectPossible,
					},
				},
			)
			if err != nil || outcome != ocEffSuspect {
				t.Fatalf("卡片 possible 应进入验证: outcome=%v err=%v", outcome, err)
			}
			verifier := &recordingCardVerifier{}
			d.SetEffectVerifier(verifier)
			for round := 1; round <= 3; round++ {
				d.verifyEffect(context.Background(), command.MsgID)
				current, lookupErr := st.CmdByMsgID(command.MsgID)
				if lookupErr != nil || current == nil || current.VerificationN != round {
					t.Fatalf("第 %d 轮验证计数错误: cmd=%+v err=%v", round, current, lookupErr)
				}
				if round < 3 && current.Status != store.CmdVerifying {
					t.Fatalf("前三轮前必须保持 verifying: %+v", current)
				}
			}
			current, _ := st.CmdByMsgID(command.MsgID)
			intent, _ := st.EffectIntentByID(command.IntentID)
			if current.Status != store.CmdSuspect || intent == nil ||
				intent.Status != store.EffectIntentSuspect {
				t.Fatalf("第三轮阴性必须转 suspect: cmd=%+v intent=%+v", current, intent)
			}
			messages, _ := st.MessagesForConversation(key)
			if len(messages) != 1 {
				t.Fatalf("阴性验证不得追加消息或创建新卡片动作: %+v", messages)
			}
			requests := verifier.snapshot()
			if len(requests) != 3 {
				t.Fatalf("必须严格最多三轮: %d", len(requests))
			}
			for _, request := range requests {
				if len(request.Guards.ExpectedTail) != 1 ||
					request.Guards.ExpectedTail[0].ContentHash != "anchor" {
					t.Fatalf("验证必须复用原 expectedTail: %+v", request.Guards)
				}
				test.assertReq(t, request)
			}
		})
	}
}
