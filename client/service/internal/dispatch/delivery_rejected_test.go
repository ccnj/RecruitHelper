package dispatch

import (
	"context"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

// fixedObservationVerifier 返回固定 observation,用于注入拒收判定。
type fixedObservationVerifier struct {
	observation VerificationObservation
	calls       int
}

func (v *fixedObservationVerifier) Verify(
	_ context.Context,
	_ VerificationRequest,
) (VerificationObservation, error) {
	v.calls++
	return v.observation, nil
}

// 拒收通知判失败(AGENTS 防护成本预算第 9 条,2026-08-11 甲方裁决):验证读带回
// 新鲜拒收时间戳时确定性失败收场——resolvedFailed、不产生 suspect、不重验。
func TestSendMessageVerificationDeliveryRejectedResolvesFailedWithoutSuspect(t *testing.T) {
	d, st, hand := newDisp(t)
	command := seedSendMessageVerifying(t, d, st, hand, "acct-rejected", "conv-rejected", "fp-rejected")

	noticeTs := time.Now().UnixMilli()
	verifier := &fixedObservationVerifier{observation: VerificationObservation{
		DeliveryRejectedTs: &noticeTs,
		ObservedAt:         noticeTs,
		Reason:             "验证窗口出现不早于派发的平台拒收通知(候选人已拉黑),判确定性失败",
	}}
	d.SetEffectVerifier(verifier)
	d.verifyEffect(context.Background(), command.MsgID)

	current, err := st.CmdByMsgID(command.MsgID)
	if err != nil || current == nil {
		t.Fatalf("读取命令失败: %v", err)
	}
	if current.Status != store.CmdResolvedFailed {
		t.Fatalf("拒收判定必须 resolvedFailed 收场,实际 %s", current.Status)
	}
	intent, err := st.EffectIntentByID(current.IntentID)
	if err != nil || intent == nil {
		t.Fatalf("读取意图失败: %v", err)
	}
	if intent.Status != store.EffectIntentResolvedFailed {
		t.Fatalf("意图必须 resolvedFailed,实际 %s", intent.Status)
	}
	if verifier.calls != 1 {
		t.Fatalf("确定性失败不得重复验证读: calls=%d", verifier.calls)
	}

	// 终局后重复落账必须响亮拒绝——它不是可重入路径。
	if err := st.ResolveEffectDeliveryRejected(command.MsgID, "again", time.Now()); err == nil {
		t.Fatal("对已终局命令重复拒收落账应报状态冲突")
	}
}
