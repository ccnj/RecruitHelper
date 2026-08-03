package dispatch

import (
	"context"
	"errors"
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// verifyErrStub 让验证读以指定错误失败，用于分辨"通道失效"与其他失败。
type verifyErrStub struct {
	err   error
	calls int
}

func (v *verifyErrStub) Verify(
	_ context.Context,
	_ VerificationRequest,
) (VerificationObservation, error) {
	v.calls++
	return VerificationObservation{}, v.err
}

func seedSendMessageVerifying(
	t *testing.T,
	d *Dispatcher,
	st *store.Store,
	hand *mockSender,
	accountRef, conversationRef, fingerprint string,
) *store.CmdRecord {
	t.Helper()
	key := seedSendTarget(t, st, hand, accountRef, conversationRef)
	_, command := seedCardEffectIntent(
		t, st, key, protocol.PrimChatSendMessage,
		protocol.ChatSendMessageArgs{ConversationRef: conversationRef, Text: "你好"},
		fingerprint, 1,
	)
	// sideEffect=possible 的失败进入验证轨，与生产同路。
	outcome, _, err := d.applyResultMessage(
		"hand-send", "result-"+accountRef,
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
	return command
}

// 2026-08-03 甲方裁决：页面两级取数通道整体失效时，chat.sendMessage 的验证读
// 判成功并收编，不重发、不转人工。
func TestSendMessageVerificationOptimisticOkWhenPageSourceUnavailable(t *testing.T) {
	d, st, hand := newDisp(t)
	command := seedSendMessageVerifying(t, d, st, hand, "acct-page-src", "conv-page-src", "fp-page-src")

	// 手侧错误名经 result.Error.Message 与 RunError 包装后的真实形态。
	verifier := &verifyErrStub{err: errors.New(
		"ELEMENT_UNRESOLVED: read_thread_main_failed:read_history_dom_empty_settle:" +
			"thread_page_source_unavailable",
	)}
	d.SetEffectVerifier(verifier)
	d.verifyEffect(context.Background(), command.MsgID)

	current, err := st.CmdByMsgID(command.MsgID)
	if err != nil || current == nil {
		t.Fatalf("读取命令失败: %v", err)
	}
	if current.Status != store.CmdOk {
		t.Fatalf("通道整体失效必须乐观判成功并收编，实际状态 %s", current.Status)
	}
	if verifier.calls != 1 {
		t.Fatalf("乐观判定不得重复验证读: calls=%d", verifier.calls)
	}
}

// 边界：能读到数据、只是结论为阴性的失败，绝不适用上面的例外。
// 这条与上面那条一起，锁住"通道好不好"与"结论对不对"的分界线。
func TestSendMessageVerificationOtherFailureStillMisses(t *testing.T) {
	d, st, hand := newDisp(t)
	command := seedSendMessageVerifying(t, d, st, hand, "acct-other-fail", "conv-other-fail", "fp-other-fail")

	verifier := &verifyErrStub{err: errors.New("ELEMENT_UNRESOLVED: dom_thread_unstable")}
	d.SetEffectVerifier(verifier)
	d.verifyEffect(context.Background(), command.MsgID)

	current, err := st.CmdByMsgID(command.MsgID)
	if err != nil || current == nil {
		t.Fatalf("读取命令失败: %v", err)
	}
	if current.Status == store.CmdOk {
		t.Fatal("非通道失效的验证失败不得判成功——该例外只覆盖数据源不可用")
	}
	if current.VerificationN != 1 {
		t.Fatalf("普通验证失败应计入轮次: n=%d", current.VerificationN)
	}
}

// 边界：该例外只授权给 chat.sendMessage，不得扩及其他 effectful 原语。
func TestPageSourceUnavailableDoesNotApplyToOtherPrimitives(t *testing.T) {
	d, st, hand := newDisp(t)
	key := seedSendTarget(t, st, hand, "acct-accept-src", "conv-accept-src")
	_, command := seedCardEffectIntent(
		t, st, key, protocol.PrimChatAcceptWechat,
		protocol.ChatAcceptWechatArgs{
			ConversationRef: "conv-accept-src",
			RequestSourceKey: "0123456789abcdef0123456789abcdef" +
				"0123456789abcdef0123456789abcdef",
		},
		"fp-accept-src", 1,
	)
	outcome, _, err := d.applyResultMessage(
		"hand-send", "result-accept-src",
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

	d.SetEffectVerifier(&verifyErrStub{err: errors.New(
		"ELEMENT_UNRESOLVED: read_thread_main_failed:x:thread_page_source_unavailable",
	)})
	d.verifyEffect(context.Background(), command.MsgID)

	current, err := st.CmdByMsgID(command.MsgID)
	if err != nil || current == nil {
		t.Fatalf("读取命令失败: %v", err)
	}
	if current.Status == store.CmdOk {
		t.Fatal("acceptWechat 不在例外范围内，通道失效仍必须走 miss")
	}
}
