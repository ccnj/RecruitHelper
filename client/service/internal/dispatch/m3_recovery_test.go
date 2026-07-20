package dispatch

import (
	"encoding/json"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func reconnectEffect(t *testing.T, d *Dispatcher, m *mockSender, newSession, newBoot, storeID string) {
	t.Helper()
	m.setSession("hand-send", newSession)
	m.up("hand-send", newBoot)
	m.mu.Lock()
	m.witness["hand-send"] = HandWitness{StoreID: storeID}
	m.mu.Unlock()
	d.OnReconnectWitness("hand-send", newBoot, storeID, 0, 0)
}

func queryCount(m *mockSender) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, envelope := range m.sent {
		if envelope.Kind == protocol.KindQuery {
			n++
		}
	}
	return n
}

func cmdCountFor(m *mockSender, ref string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, envelope := range m.sent {
		if envelope.Kind == protocol.KindCmd && envelope.MsgID == ref {
			n++
		}
	}
	return n
}

func TestEffectRecoveryUnknownSameWitnessResendsOriginalMsgIDOnce(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-recover-unknown", "conv-recover-unknown")
	receipt, err := d.SendMessage(sendRequest("intent-recover-unknown", key, "你好"))
	if err != nil {
		t.Fatal(err)
	}
	reconnectEffect(t, d, m, "session-recover", "boot-recover", "witness-store-1")
	if queryCount(m) != 1 {
		t.Fatalf("重连必须先 query: %d", queryCount(m))
	}
	cmd, _ := st.CmdByMsgID(receipt.MsgID)
	d.OnReport("hand-send", "report-unknown", "session-recover", "boot-recover", protocol.ReportBody{
		Ref: receipt.MsgID, State: protocol.ReportStateUnknown, WitnessStoreId: "witness-store-1",
	})
	after, _ := st.CmdByMsgID(receipt.MsgID)
	if cmdCountFor(m, receipt.MsgID) != 2 || after.MsgID != receipt.MsgID || after.RecoveryRedispatchN != 1 ||
		after.Status != store.CmdSent || after.IdemKey != cmd.IdemKey || after.Args != cmd.Args || after.Guards != cmd.Guards {
		t.Fatalf("unknown 同库只能原 msgId/原材料恢复一次: before=%+v after=%+v count=%d",
			cmd, after, cmdCountFor(m, receipt.MsgID))
	}
	lineage, _ := st.CmdLineage(receipt.LogicalDispatchID)
	if len(lineage) != 1 {
		t.Fatalf("恢复不得铸 replacement: %+v", lineage)
	}
}

func TestEffectRecoveryQueuedWaitsOriginalAndChangedWitnessVerifies(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-recover-queued", "conv-recover-queued")
		receipt, _ := d.SendMessage(sendRequest("intent-recover-queued", key, "你好"))
		reconnectEffect(t, d, m, "session-queued", "boot-queued", "witness-store-1")
		d.OnReport("hand-send", "report-queued", "session-queued", "boot-queued", protocol.ReportBody{
			Ref: receipt.MsgID, State: protocol.ReportStateQueued, WitnessStoreId: "witness-store-1",
		})
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		if cmd.Status != store.CmdPendingReconcile || cmd.ReconcileNextAt == nil || cmdCountFor(m, receipt.MsgID) != 1 {
			t.Fatalf("report=queued 必须保留对账并有界复询，不得重投: %+v count=%d", cmd, cmdCountFor(m, receipt.MsgID))
		}
		d.sweepEffectRecovery(cmd.ReconcileNextAt.Add(time.Millisecond))
		if queryCount(m) != 2 || cmdCountFor(m, receipt.MsgID) != 1 {
			t.Fatalf("queued 后只能复询 query，不得重投 SX: queries=%d cmds=%d", queryCount(m), cmdCountFor(m, receipt.MsgID))
		}
	})

	t.Run("changed witness unknown", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-recover-store", "conv-recover-store")
		receipt, _ := d.SendMessage(sendRequest("intent-recover-store", key, "你好"))
		release := make(chan struct{})
		d.SetEffectVerifier(blockingVerifier{release: release})
		defer close(release)
		reconnectEffect(t, d, m, "session-store", "boot-store", "witness-store-2")
		d.OnReport("hand-send", "report-store", "session-store", "boot-store", protocol.ReportBody{
			Ref: receipt.MsgID, State: protocol.ReportStateUnknown, WitnessStoreId: "witness-store-2",
		})
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		if cmd.Status != store.CmdVerifying || cmd.RecoveryAuthorized || cmdCountFor(m, receipt.MsgID) != 1 {
			t.Fatalf("换 witness store 的 unknown 不是零副作用证明: %+v", cmd)
		}
	})
}

func TestRecoveryReportDoneRequiresOuterJournalAndResultCorrelation(t *testing.T) {
	for _, mismatch := range []string{"outer", "journal", "result"} {
		t.Run(mismatch, func(t *testing.T) {
			d, st, m := newDisp(t)
			key := seedSendTarget(t, st, m, "acct-done-"+mismatch, "conv-done-"+mismatch)
			receipt, _ := d.SendMessage(sendRequest("intent-done-"+mismatch, key, "你好"))
			release := make(chan struct{})
			d.SetEffectVerifier(blockingVerifier{release: release})
			defer close(release)
			reconnectEffect(t, d, m, "session-done", "boot-done", "witness-store-1")
			cmd, _ := st.CmdByMsgID(receipt.MsgID)
			outerRef, journalRef, resultRef := receipt.MsgID, receipt.MsgID, receipt.MsgID
			switch mismatch {
			case "outer":
				outerRef = "other-ref"
			case "journal":
				journalRef = "other-ref"
			case "result":
				resultRef = "other-ref"
			}
			result := validSendResult(resultRef, key.ConversationRef, "你好")
			d.OnReport("hand-send", "report-done-"+mismatch, "session-done", "boot-done", protocol.ReportBody{
				Ref: outerRef, State: protocol.ReportStateDone, WitnessStoreId: "witness-store-1",
				Journal: &protocol.JournalSnapshot{
					Ref: journalRef, IdemKey: cmd.IdemKey, State: protocol.JournalStateCommitted, StartedAt: 1, CommittedAt: 2,
				},
				Result: &result,
			})
			intent, _ := st.EffectIntentByID(receipt.IntentID)
			messages, _ := st.MessagesForConversation(key)
			if intent.Status == store.EffectIntentOk || len(messages) != 1 {
				t.Fatalf("%s ref 错配不得终局或追加 self: intent=%+v messages=%+v", mismatch, intent, messages)
			}
		})
	}
}

func TestRecoveryQueryTimeoutRetriesReadonlyQueryThenVerifies(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-query-timeout", "conv-query-timeout")
	receipt, _ := d.SendMessage(sendRequest("intent-query-timeout", key, "你好"))
	reconnectEffect(t, d, m, "session-query", "boot-query", "witness-store-1")
	if queryCount(m) != 1 {
		t.Fatal("预置 query 数错误")
	}
	for i := 0; i < recoveryQueryMax; i++ {
		d.sweepEffectRecovery(time.Now().Add(time.Duration(i+1) * time.Hour))
	}
	cmd, _ := st.CmdByMsgID(receipt.MsgID)
	if queryCount(m) != recoveryQueryMax || cmd.Status != store.CmdSuspect || cmd.ReviewReady {
		t.Fatalf("report 丢失应有限重问 query 后 fail-closed 验证/suspect: queries=%d cmd=%+v", queryCount(m), cmd)
	}
}

func TestLateDoneAfterQueryTimeoutEnteredVerificationIsStillAccepted(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-late-done", "conv-late-done")
	receipt, _ := d.SendMessage(sendRequest("intent-late-done", key, "你好"))
	release := make(chan struct{})
	d.SetEffectVerifier(blockingVerifier{release: release})
	defer close(release)
	reconnectEffect(t, d, m, "session-late-done", "boot-late-done", "witness-store-1")
	for i := 0; i < recoveryQueryMax; i++ {
		d.sweepEffectRecovery(time.Now().Add(time.Duration(i+1) * time.Hour))
	}
	cmd, _ := st.CmdByMsgID(receipt.MsgID)
	if cmd.Status != store.CmdVerifying || cmd.QueryMsgID == "" {
		t.Fatalf("query 耗尽应在同 generation 验证中且保留 query 栅栏: %+v", cmd)
	}
	result := validSendResult(receipt.MsgID, key.ConversationRef, "你好")
	d.OnReport("hand-send", "report-late-done", "session-late-done", "boot-late-done", protocol.ReportBody{
		Ref: receipt.MsgID, State: protocol.ReportStateDone, WitnessStoreId: "witness-store-1",
		Journal: &protocol.JournalSnapshot{
			Ref: receipt.MsgID, IdemKey: cmd.IdemKey, State: protocol.JournalStateCommitted, StartedAt: 1, CommittedAt: 2,
		},
		Result: &result,
	})
	after, _ := st.CmdByMsgID(receipt.MsgID)
	intent, _ := st.EffectIntentByID(receipt.IntentID)
	messages, _ := st.MessagesForConversation(key)
	if after.Status != store.CmdOk || intent.Status != store.EffectIntentOk || len(messages) != 2 {
		t.Fatalf("当前 generation 迟到 committed done 必须赢过验证中间态: cmd=%+v intent=%+v messages=%+v", after, intent, messages)
	}
}

func TestQueuedExecutingReportsAreBoundedThenVerifyWithoutSXRedispatch(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-queued-bounded", "conv-queued-bounded")
	receipt, _ := d.SendMessage(sendRequest("intent-queued-bounded", key, "你好"))
	release := make(chan struct{})
	d.SetEffectVerifier(blockingVerifier{release: release})
	defer close(release)
	reconnectEffect(t, d, m, "session-queued-bounded", "boot-queued-bounded", "witness-store-1")
	states := []protocol.ReportState{protocol.ReportStateQueued, protocol.ReportStateExecuting, protocol.ReportStateQueued}
	for i, state := range states {
		d.OnReport("hand-send", "report-bounded-"+string(rune('a'+i)), "session-queued-bounded", "boot-queued-bounded", protocol.ReportBody{
			Ref: receipt.MsgID, State: state, WitnessStoreId: "witness-store-1",
		})
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		if i < len(states)-1 {
			if cmd.Status != store.CmdPendingReconcile || cmd.ReconcileNextAt == nil {
				t.Fatalf("第 %d 次 %s 应等待有界复询: %+v", i+1, state, cmd)
			}
			d.sweepEffectRecovery(cmd.ReconcileNextAt.Add(time.Millisecond))
		} else if cmd.Status != store.CmdVerifying {
			t.Fatalf("复询上限应转验证: %+v", cmd)
		}
	}
	if queryCount(m) != recoveryQueryMax || cmdCountFor(m, receipt.MsgID) != 1 {
		t.Fatalf("整个活性轨只能重发 query，原 SX 仍一次: queries=%d cmds=%d",
			queryCount(m), cmdCountFor(m, receipt.MsgID))
	}
}

func TestAuthorizedRecoveryReleasedWhenOtherAccountVerificationSettles(t *testing.T) {
	d, st, m := newDisp(t)
	keyA := seedSendTarget(t, st, m, "acct-barrier-a", "conv-barrier-a")
	keyB := seedSendTarget(t, st, m, "acct-barrier-b", "conv-barrier-b")
	receiptA, _ := d.SendMessage(sendRequest("intent-barrier-a", keyA, "A"))
	receiptB, _ := d.SendMessage(sendRequest("intent-barrier-b", keyB, "B"))
	release := make(chan struct{})
	d.SetEffectVerifier(blockingVerifier{release: release})
	defer close(release)
	reconnectEffect(t, d, m, "session-barrier", "boot-barrier", "witness-store-1")
	cmdA, _ := st.CmdByMsgID(receiptA.MsgID)
	d.OnReport("hand-send", "report-barrier-a", "session-barrier", "boot-barrier", protocol.ReportBody{
		Ref: receiptA.MsgID, State: protocol.ReportStateUnknown, WitnessStoreId: "witness-store-1",
	})
	afterA, _ := st.CmdByMsgID(receiptA.MsgID)
	if !afterA.RecoveryAuthorized || cmdCountFor(m, receiptA.MsgID) != 1 {
		t.Fatalf("A 应已授权但被 B 屏障: %+v", afterA)
	}
	d.OnReport("hand-send", "report-barrier-b", "session-barrier", "boot-barrier", protocol.ReportBody{
		Ref: receiptB.MsgID, State: protocol.ReportStateAttempting, WitnessStoreId: "witness-store-1",
	})
	cmdB, _ := st.CmdByMsgID(receiptB.MsgID)
	for i := 0; i < protocol.DefaultVerificationMaxRounds; i++ {
		d.recordVerificationMiss(*cmdB, "test miss")
		cmdB, _ = st.CmdByMsgID(receiptB.MsgID)
	}
	afterA, _ = st.CmdByMsgID(receiptA.MsgID)
	if cmdB.Status != store.CmdSuspect || !cmdB.ReviewReady || afterA.Status != store.CmdSent ||
		afterA.MsgID != cmdA.MsgID || cmdCountFor(m, receiptA.MsgID) != 2 {
		t.Fatalf("B 收束后必须重跑屏障并恢复 A 原 msgId: A=%+v B=%+v", afterA, cmdB)
	}
}

func TestSafeRecoveryCapKeepsWholeHandBarrierBeforeAnyRedispatch(t *testing.T) {
	d, st, m := newDisp(t)
	keyA := seedSendTarget(t, st, m, "acct-cap-a", "conv-cap-a")
	keyB := seedSendTarget(t, st, m, "acct-cap-b", "conv-cap-b")
	receiptA, _ := d.SendMessage(sendRequest("intent-cap-a", keyA, "A"))
	receiptB, _ := d.SendMessage(sendRequest("intent-cap-b", keyB, "B"))
	if err := st.MutateCmd(receiptB.MsgID, func(record *store.CmdRecord) error {
		record.RecoveryRedispatchN = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	d.SetEffectVerifier(blockingVerifier{release: release})
	defer close(release)
	reconnectEffect(t, d, m, "session-cap", "boot-cap", "witness-store-1")
	d.OnReport("hand-send", "report-cap-a", "session-cap", "boot-cap", protocol.ReportBody{
		Ref: receiptA.MsgID, State: protocol.ReportStateUnknown, WitnessStoreId: "witness-store-1",
	})
	d.OnReport("hand-send", "report-cap-b", "session-cap", "boot-cap", protocol.ReportBody{
		Ref: receiptB.MsgID, State: protocol.ReportStateUnknown, WitnessStoreId: "witness-store-1",
	})
	afterA, _ := st.CmdByMsgID(receiptA.MsgID)
	afterB, _ := st.CmdByMsgID(receiptB.MsgID)
	if afterA.Status != store.CmdPendingReconcile || !afterA.RecoveryAuthorized ||
		afterB.Status != store.CmdVerifying || cmdCountFor(m, receiptA.MsgID) != 1 {
		t.Fatalf("B 到恢复 cap 时不得按枚举顺序先重投 A: A=%+v B=%+v Acount=%d",
			afterA, afterB, cmdCountFor(m, receiptA.MsgID))
	}
}

func TestAttemptingReportWithWrongIdemStillVerifiesAndNeverRedispatches(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-attempting-idem", "conv-attempting-idem")
	receipt, _ := d.SendMessage(sendRequest("intent-attempting-idem", key, "你好"))
	release := make(chan struct{})
	d.SetEffectVerifier(blockingVerifier{release: release})
	defer close(release)
	reconnectEffect(t, d, m, "session-attempting-idem", "boot-attempting-idem", "witness-store-1")
	d.OnReport("hand-send", "report-attempting-idem", "session-attempting-idem", "boot-attempting-idem", protocol.ReportBody{
		Ref: receipt.MsgID, State: protocol.ReportStateAttempting, WitnessStoreId: "witness-store-1",
		Journal: &protocol.JournalSnapshot{
			Ref: receipt.MsgID, IdemKey: "wrong-idem", State: protocol.JournalStateAttempting, StartedAt: 1,
		},
	})
	cmd, _ := st.CmdByMsgID(receipt.MsgID)
	if cmd.Status != store.CmdVerifying || cmdCountFor(m, receipt.MsgID) != 1 ||
		!hasAudit(t, st, "report_attempting_witness_mismatch", receipt.MsgID) {
		t.Fatalf("attempting 错配只能 fail-closed 验证，不得恢复 SX: cmd=%+v count=%d",
			cmd, cmdCountFor(m, receipt.MsgID))
	}
}

func TestRecoveryQueryAttemptCountSurvivesBrainRestart(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-query-restart", "conv-query-restart")
	receipt, _ := d.SendMessage(sendRequest("intent-query-restart", key, "你好"))
	reconnectEffect(t, d, m, "session-query-1", "boot-query-1", "witness-store-1")
	before, _ := st.CmdByMsgID(receipt.MsgID)
	if before.QueryN != 1 {
		t.Fatalf("首次 query 应持久: %+v", before)
	}
	d2 := New(st, m)
	d2.Recover()
	reconnectEffect(t, d2, m, "session-query-2", "boot-query-2", "witness-store-1")
	after, _ := st.CmdByMsgID(receipt.MsgID)
	if after.QueryN != 2 || queryCount(m) != 2 {
		t.Fatalf("脑重启不得清空 query 上限证词: before=%+v after=%+v", before, after)
	}
}

func TestDoneReportValidPathUsesPrimitiveValidatorAndAtomicLedger(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-done-ok", "conv-done-ok")
	receipt, _ := d.SendMessage(sendRequest("intent-done-ok", key, "你好"))
	reconnectEffect(t, d, m, "session-done-ok", "boot-done-ok", "witness-store-1")
	cmd, _ := st.CmdByMsgID(receipt.MsgID)
	result := validSendResult(receipt.MsgID, key.ConversationRef, "你好")
	d.OnReport("hand-send", "report-done-ok", "session-done-ok", "boot-done-ok", protocol.ReportBody{
		Ref: receipt.MsgID, State: protocol.ReportStateDone, WitnessStoreId: "witness-store-1",
		Journal: &protocol.JournalSnapshot{
			Ref: receipt.MsgID, IdemKey: cmd.IdemKey, State: protocol.JournalStateCommitted, StartedAt: 1, CommittedAt: 2,
		},
		Result: &result,
	})
	after, _ := st.CmdByMsgID(receipt.MsgID)
	intent, _ := st.EffectIntentByID(receipt.IntentID)
	messages, _ := st.MessagesForConversation(key)
	if after.Status != store.CmdOk || intent.Status != store.EffectIntentOk || len(messages) != 2 {
		body, _ := json.Marshal(result)
		t.Fatalf("合法 done 未原子终局: cmd=%+v intent=%+v messages=%+v result=%s", after, intent, messages, body)
	}
}
