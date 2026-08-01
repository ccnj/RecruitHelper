package dispatch

import (
	"context"
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

type positiveWechatAcceptVerifier struct {
	calls int
}

func (v *positiveWechatAcceptVerifier) Verify(
	_ context.Context,
	_ VerificationRequest,
) (VerificationObservation, error) {
	v.calls++
	return VerificationObservation{
		Confirmed:   true,
		ContentHash: "fingerprint-accept-commit-fail",
		SourceKey: "a1b2c3d4e5f60718293a4b5c6d7e8f90" +
			"a1b2c3d4e5f60718293a4b5c6d7e8f90",
		PeerWechat: "wx-commit-fail",
		ObservedAt: 1_722_000_000_000,
		Reason:     "唯一命中",
	}, nil
}

// 正证已经读到、入账却失败时，验证器必须退避并计入轮次上限，最终转 suspect。
// 旧实现在入账失败分支裸 return：命令留在 verifying、nextAt 停在过去，sweep
// 下一轮立刻判到期再验一次，形成每秒一轮、既不收敛也不转人工的永动机
// （2026-08-01 客户机 195 轮 readThread 事故）。
func TestVerificationCommitFailureCountsRoundsThenSuspects(t *testing.T) {
	d, st, hand := newDisp(t)
	key := seedSendTarget(t, st, hand, "acct-commit-fail", "conv-commit-fail")
	_, command := seedCardEffectIntent(
		t, st, key, protocol.PrimChatAcceptWechat,
		protocol.ChatAcceptWechatArgs{
			ConversationRef: "conv-commit-fail",
			RequestSourceKey: "0123456789abcdef0123456789abcdef" +
				"0123456789abcdef0123456789abcdef",
		},
		"fingerprint-accept-commit-fail", 1,
	)
	outcome, _, err := d.applyResultMessage(
		"hand-send", "result-commit-fail",
		protocol.ResultBody{
			Ref: command.MsgID, Status: protocol.ResultStatusFailed,
			Error: &protocol.ErrorBody{
				Code: protocol.ErrCodeInternalHand, Message: "click outcome unknown",
				Retryable: protocol.RetryableNo, SideEffect: protocol.SideEffectPossible,
			},
		},
	)
	if err != nil || outcome != ocEffSuspect {
		t.Fatalf("possible 应进入验证: outcome=%v err=%v", outcome, err)
	}
	verifier := &positiveWechatAcceptVerifier{}
	d.SetEffectVerifier(verifier)

	for round := 1; round <= 3; round++ {
		d.verifyEffect(context.Background(), command.MsgID)
		current, lookupErr := st.CmdByMsgID(command.MsgID)
		if lookupErr != nil || current == nil {
			t.Fatalf("第 %d 轮读取命令失败: %v", round, lookupErr)
		}
		if current.VerificationN != round {
			t.Fatalf("入账失败必须计入验证轮次: round=%d n=%d", round, current.VerificationN)
		}
		if round < 3 {
			if current.Status != store.CmdVerifying {
				t.Fatalf("未耗尽轮次前应保持 verifying: %+v", current)
			}
			if current.VerificationNextAt == nil {
				t.Fatalf("入账失败必须安排退避，否则 sweep 会立刻重验: %+v", current)
			}
		}
	}
	current, _ := st.CmdByMsgID(command.MsgID)
	if current.Status != store.CmdSuspect {
		t.Fatalf("轮次耗尽必须转 suspect 交人工: %+v", current)
	}
	if !hasAudit(t, st, "effect_verification_commit_failed", command.MsgID) {
		t.Fatal("入账失败必须留审计")
	}
	if verifier.calls != 3 {
		t.Fatalf("入账失败不得无限重验: calls=%d", verifier.calls)
	}
}
