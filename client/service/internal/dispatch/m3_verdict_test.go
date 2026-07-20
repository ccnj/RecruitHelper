package dispatch

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func makeEffectSuspectReviewable(t *testing.T, d *Dispatcher, st *store.Store, ref string, reviewAfter time.Time) {
	t.Helper()
	if err := st.MoveEffectToVerification(ref, "test ambiguity", time.Now()); err != nil {
		t.Fatal(err)
	}
	cmd, _ := st.CmdByMsgID(ref)
	for i := 0; i < protocol.DefaultVerificationMaxRounds; i++ {
		now := time.Now()
		if _, err := st.RecordVerificationMiss(ref, "test miss", now, reviewAfter, now,
			protocol.DefaultVerificationMaxRounds); err != nil {
			t.Fatal(err)
		}
	}
	cmd, _ = st.CmdByMsgID(ref)
	if cmd.Status != store.CmdSuspect || !cmd.ReviewReady {
		t.Fatalf("预置 reviewable suspect 失败: %+v", cmd)
	}
	_ = d
}

func TestRealEffectVerdictResolvedOKIsAtomicAndLateFailedNoneCorrectsLedger(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-verdict-ok", "conv-verdict-ok")
	text := "你好"
	receipt, _ := d.SendMessage(sendRequest("intent-verdict-ok", key, text))
	makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))

	if err := d.Verdict(receipt.MsgID, store.CmdResolvedOk); err != nil {
		t.Fatalf("在线但对账/验证/安全窗已收束应可人裁: %v", err)
	}
	cmd, _ := st.CmdByMsgID(receipt.MsgID)
	intent, _ := st.EffectIntentByID(receipt.IntentID)
	messages, _ := st.MessagesForConversation(key)
	if cmd.Status != store.CmdResolvedOk || intent.Status != store.EffectIntentResolvedOk ||
		len(messages) != 2 || messages[1].OutboundIntentID == nil || *messages[1].OutboundIntentID != receipt.IntentID {
		t.Fatalf("resolvedOk 必须原子推进 cmd/intent/self message: cmd=%+v intent=%+v messages=%+v", cmd, intent, messages)
	}

	// durable result 是更强的平台事实：明确 none 必须纠正人误判并撤回
	// 该 intent 产生的虚假 self 消息。
	d.OnResult("hand-send", "late-none-after-verdict", protocol.ResultBody{
		Ref: receipt.MsgID, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodeInternalHand, Message: "not sent",
			Retryable: protocol.RetryableNo, SideEffect: protocol.SideEffectNone,
		},
	})
	cmd, _ = st.CmdByMsgID(receipt.MsgID)
	intent, _ = st.EffectIntentByID(receipt.IntentID)
	messages, _ = st.MessagesForConversation(key)
	if cmd.Status != store.CmdFailed || intent.Status != store.EffectIntentFailed || len(messages) != 1 {
		t.Fatalf("迟到 failed+none 未原子纠正人裁: cmd=%+v intent=%+v messages=%+v", cmd, intent, messages)
	}
}

func TestLateSafeTerminalsAtomicallyRetractResolvedOKMessage(t *testing.T) {
	tests := []struct {
		name       string
		result     func(string) protocol.ResultBody
		wantStatus store.CmdStatus
	}{
		{
			name: "failed-none", wantStatus: store.CmdFailed,
			result: func(ref string) protocol.ResultBody {
				return protocol.ResultBody{
					Ref: ref, Status: protocol.ResultStatusFailed,
					Error: &protocol.ErrorBody{
						Code: protocol.ErrCodeInternalHand, Message: "definitely not sent",
						Retryable: protocol.RetryableNo, SideEffect: protocol.SideEffectNone,
					},
				}
			},
		},
		{
			name: "canceled", wantStatus: store.CmdCanceled,
			result: func(ref string) protocol.ResultBody {
				return protocol.ResultBody{Ref: ref, Status: protocol.ResultStatusCanceled}
			},
		},
		{
			name: "expired", wantStatus: store.CmdExpired,
			result: func(ref string) protocol.ResultBody {
				return protocol.ResultBody{Ref: ref, Status: protocol.ResultStatusExpired}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, st, m := newDisp(t)
			key := seedSendTarget(t, st, m, "acct-safe-"+test.name, "conv-safe-"+test.name)
			receipt, _ := d.SendMessage(sendRequest("intent-safe-"+test.name, key, "你好"))
			makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))
			if err := d.Verdict(receipt.MsgID, store.CmdResolvedOk); err != nil {
				t.Fatal(err)
			}
			before, _ := st.EffectIntentByID(receipt.IntentID)
			if before.ResultMessageSeq == nil {
				t.Fatal("resolvedOk 应先铸造 self 消息")
			}

			d.OnResult("hand-send", "late-safe-"+test.name, test.result(receipt.MsgID))
			cmd, _ := st.CmdByMsgID(receipt.MsgID)
			intent, _ := st.EffectIntentByID(receipt.IntentID)
			messages, _ := st.MessagesForConversation(key)
			if cmd.Status != test.wantStatus || intent.Status != store.EffectIntentFailed ||
				intent.ResultMessageSeq != nil || len(messages) != 1 {
				t.Fatalf("迟到安全终局必须原子撤回人工 self: cmd=%+v intent=%+v messages=%+v", cmd, intent, messages)
			}
			if !hasAudit(t, st, "suspect_cleared", receipt.MsgID) {
				t.Fatal("权威安全终局纠正人裁必须留 suspect_cleared 审计")
			}
		})
	}
}

func TestLateConfirmedResultWinsResolvedFailedAndResultFirstBlocksVerdict(t *testing.T) {
	t.Run("verdict first", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-late-ok", "conv-late-ok")
		receipt, _ := d.SendMessage(sendRequest("intent-late-ok", key, "你好"))
		makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))
		if err := d.Verdict(receipt.MsgID, store.CmdResolvedFailed); err != nil {
			t.Fatal(err)
		}
		d.OnResult("hand-send", "late-ok-after-failed", validSendResult(receipt.MsgID, key.ConversationRef, "你好"))
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		intent, _ := st.EffectIntentByID(receipt.IntentID)
		messages, _ := st.MessagesForConversation(key)
		if cmd.Status != store.CmdOk || intent.Status != store.EffectIntentOk || len(messages) != 2 {
			t.Fatalf("迟到 ok 必须赢过 resolvedFailed: cmd=%+v intent=%+v messages=%+v", cmd, intent, messages)
		}
	})

	t.Run("result first", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-result-first", "conv-result-first")
		receipt, _ := d.SendMessage(sendRequest("intent-result-first", key, "你好"))
		makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))
		d.OnResult("hand-send", "ok-before-verdict", validSendResult(receipt.MsgID, key.ConversationRef, "你好"))
		if err := d.Verdict(receipt.MsgID, store.CmdResolvedFailed); !errors.Is(err, ErrNotSuspect) {
			t.Fatalf("平台 result 先入账后人裁必须输: %v", err)
		}
		intent, _ := st.EffectIntentByID(receipt.IntentID)
		if intent.Status != store.EffectIntentOk {
			t.Fatalf("人裁不得覆盖已入账 result: %+v", intent)
		}
	})
}

func TestReviewAfterBlocksEarlyVerdictAndNewIntentUntilZombieWindowEnds(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-review-after", "conv-review-after")
	receipt, _ := d.SendMessage(sendRequest("intent-review-after", key, "你好"))
	reviewAfter := time.Now().Add(time.Hour)
	makeEffectSuspectReviewable(t, d, st, receipt.MsgID, reviewAfter)
	cmd, _ := st.CmdByMsgID(receipt.MsgID)
	if cmd.ReviewAfterMs < reviewAfter.Add(-time.Second).UnixMilli() {
		t.Fatalf("reviewAfter 未持久不可逆动作最晚窗: %+v", cmd)
	}
	ready, after := d.SuspectReviewState(*cmd)
	if ready || after == nil || *after != cmd.ReviewAfterMs {
		t.Fatalf("早于 reviewAfter 必须展示不可裁: ready=%v after=%v cmd=%+v", ready, after, cmd)
	}
	if err := d.Verdict(receipt.MsgID, store.CmdResolvedFailed); !errors.Is(err, ErrVerdictNotReady) {
		t.Fatalf("页面僵尸可能迟到 click 时禁止早裁: %v", err)
	}
	next := sendRequest("intent-review-after-new", key, "再发")
	next.PreviousIntentID = receipt.IntentID
	if _, err := d.SendMessage(next); !errors.Is(err, store.ErrDomainBusy) {
		t.Fatalf("reviewAfter 之前 suspect 域必须冻结新 intent: %v", err)
	}
	if err := st.MutateCmd(receipt.MsgID, func(record *store.CmdRecord) error {
		record.ReviewAfterMs = time.Now().Add(-time.Second).UnixMilli()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Verdict(receipt.MsgID, store.CmdResolvedFailed); err != nil {
		t.Fatalf("安全窗已过应可裁: %v", err)
	}
}

func TestOnlineUnsettledRealEffectSuspectCannotBeReviewed(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-review-unsettled", "conv-review-unsettled")
	receipt, _ := d.SendMessage(sendRequest("intent-review-unsettled", key, "你好"))
	if err := st.MoveEffectToVerification(receipt.MsgID, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkEffectSuspect(receipt.MsgID, "verifier unavailable", time.Now()); err != nil {
		t.Fatal(err)
	}
	cmd, _ := st.CmdByMsgID(receipt.MsgID)
	if cmd.ReviewReady {
		t.Fatal("未收束证词不得标 reviewReady")
	}
	if err := d.Verdict(receipt.MsgID, store.CmdResolvedFailed); !errors.Is(err, ErrVerdictNotReady) {
		t.Fatalf("在线未收束必须拒绝人裁: %v", err)
	}
}

func TestPossibleAfterResolvedFailedCannotReopenOldIntentOrCaptureSuccessorMessage(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-verdict-successor", "conv-verdict-successor")
	text := "同文消息"
	oldReceipt, _ := d.SendMessage(sendRequest("intent-old", key, text))
	makeEffectSuspectReviewable(t, d, st, oldReceipt.MsgID, time.Now().Add(-time.Second))
	if err := d.Verdict(oldReceipt.MsgID, store.CmdResolvedFailed); err != nil {
		t.Fatal(err)
	}
	next := sendRequest("intent-new", key, text)
	next.PreviousIntentID = oldReceipt.IntentID
	newReceipt, err := d.SendMessage(next)
	if err != nil {
		t.Fatalf("人裁失败后应允许人产生新 intent: %v", err)
	}
	d.OnResult("hand-send", "late-possible-old", protocol.ResultBody{
		Ref: oldReceipt.MsgID, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodeInternalHand, Message: "ambiguous late result",
			Retryable: protocol.RetryableNo, SideEffect: protocol.SideEffectPossible,
		},
	})
	oldCmd, _ := st.CmdByMsgID(oldReceipt.MsgID)
	newCmd, _ := st.CmdByMsgID(newReceipt.MsgID)
	oldIntent, _ := st.EffectIntentByID(oldReceipt.IntentID)
	newIntent, _ := st.EffectIntentByID(newReceipt.IntentID)
	if oldCmd.Status != store.CmdResolvedFailed || oldIntent.Status != store.EffectIntentResolvedFailed ||
		newCmd.Status != store.CmdSent || newIntent.Status != store.EffectIntentDispatching {
		t.Fatalf("迟到 possible 不得重开旧 intent 并把后继同文认给它: oldCmd=%+v oldIntent=%+v newCmd=%+v newIntent=%+v",
			oldCmd, oldIntent, newCmd, newIntent)
	}
	if !hasAudit(t, st, "late_possible_after_verdict", oldReceipt.MsgID) {
		t.Fatal("保留人裁必须响亮审计")
	}
}
