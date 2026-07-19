package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

var allM2Features = []string{
	string(protocol.FeatureLease1),
	string(protocol.FeatureProgress1),
	string(protocol.FeatureCancel1),
}

func businessNav(handID, accountRef string) DispatchRequest {
	return DispatchRequest{
		HandID: handID,
		Name:   protocol.PrimNavEnsureSurface,
		Args:   json.RawMessage(`{"surface":"im"}`),
		Context: &protocol.CmdContext{
			Platform:                     "zhilian",
			AccountRef:                   accountRef,
			ExpectedPrincipalFingerprint: "opaque-fp-1",
		},
	}
}

func prepareNavHand(m *mockSender, handID, boot string) {
	m.up(handID, boot)
	m.negotiate(handID, []string{protocol.PrimNavEnsureSurface + "@1"}, allM2Features)
}

type staleAtSendSender struct{ *mockSender }

func (s *staleAtSendSender) SendEnvelope(string, protocol.Envelope) error {
	return ErrStaleSession
}

func TestGenerationBoundDispatchRejectsMismatchAndVoidsDefinitiveStaleSend(t *testing.T) {
	d, st, m := newDisp(t)
	prepareNavHand(m, "hand-generation", "boot-generation")
	m.setSession("hand-generation", "session-current")

	mismatch := businessNav("hand-generation", "acct-generation")
	mismatch.ExpectedSession = "session-old"
	mismatch.ExpectedBootID = "boot-generation"
	if _, err := d.DispatchStructured(mismatch); !errors.Is(err, ErrStaleSession) {
		t.Fatalf("落账前 generation mismatch 应拒绝: %v", err)
	}
	if rows, _ := st.RecentCmds(10); len(rows) != 0 {
		t.Fatalf("落账前拒绝不得留下命令: %+v", rows)
	}

	current := businessNav("hand-generation", "acct-generation")
	current.ExpectedSession = "session-current"
	current.ExpectedBootID = "boot-generation"
	d.sender = &staleAtSendSender{mockSender: m}
	msgID, err := d.DispatchStructured(current)
	if !errors.Is(err, ErrStaleSession) || msgID == "" {
		t.Fatalf("Hub 写门禁 stale 应返回有身份的失败命令: msg=%q err=%v", msgID, err)
	}
	record, lookupErr := st.CmdByMsgID(msgID)
	if lookupErr != nil || record == nil || record.Status != store.CmdVoid || record.ErrorCode != string(protocol.ErrCodeStaleSession) {
		t.Fatalf("可证明未写 socket 的 generation-bound 命令必须 void: record=%+v err=%v", record, lookupErr)
	}
	if rows, _ := st.NonTerminalCmds(); len(rows) != 0 {
		t.Fatalf("void 命令不得被 queued 故障轨道重投新代: %+v", rows)
	}
}

func TestStructuredDispatchOnlyAllowsContextlessPreBindProbe(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-bind", "boot-bind")
	m.negotiate("hand-bind", []string{
		protocol.PrimProbePlatform + "@1",
		protocol.PrimChatReadList + "@1",
	}, allM2Features)

	msgID, err := d.DispatchStructured(DispatchRequest{
		HandID: "hand-bind", Name: protocol.PrimProbePlatform, Args: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("绑定前无 context probe.platform 应走正式派发: %v", err)
	}
	rec, err := st.CmdByMsgID(msgID)
	if err != nil || rec == nil {
		t.Fatalf("读取绑定探测命令: rec=%+v err=%v", rec, err)
	}
	if rec.ContextJSON != "" || rec.AccountRef != "" || rec.Platform != "" || rec.Domain != "probe:hand-bind" {
		t.Fatalf("绑定前探测不应伪造账号 context，且须落独立探测域: %+v", rec)
	}
	m.mu.Lock()
	var sentBody protocol.CmdBody
	if len(m.sent) != 0 {
		_ = json.Unmarshal(m.sent[0].Body, &sentBody)
	}
	m.mu.Unlock()
	if sentBody.Name != protocol.PrimProbePlatform || sentBody.Context != nil {
		t.Fatalf("线上绑定探测信封应仅 probe.platform 无 context: %+v", sentBody)
	}

	_, err = d.DispatchStructured(DispatchRequest{
		HandID: "hand-bind", Name: protocol.PrimChatReadList,
		Args: json.RawMessage(`{"filter":"all"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "$.context") {
		t.Fatalf("其他 M2 原语缺 context 必须被 generated validator 拒绝: %v", err)
	}
}

func TestStructuredDispatchAtomicDomainAcrossHands(t *testing.T) {
	d, st, m := newDisp(t)
	prepareNavHand(m, "hand-01", "b-1")
	prepareNavHand(m, "hand-02", "b-2")

	msgID, err := d.DispatchStructured(businessNav("hand-01", "acct-1"))
	if err != nil {
		t.Fatalf("首个派发: %v", err)
	}
	if _, err := d.DispatchStructured(businessNav("hand-02", "acct-1")); !errors.Is(err, store.ErrDomainBusy) {
		t.Fatalf("跨手同账号必须命中原子 domain 闸,得到 %v", err)
	}
	rec, _ := st.CmdByMsgID(msgID)
	if rec.Domain != "zhilian:acct-1" || rec.Platform != "zhilian" || rec.AccountRef != "acct-1" {
		t.Fatalf("业务 domain/context 未持久化: %+v", rec)
	}
	var persisted protocol.CmdContext
	if err := json.Unmarshal([]byte(rec.ContextJSON), &persisted); err != nil || persisted != *businessNav("hand-01", "acct-1").Context {
		t.Fatalf("generated CmdContext 未完整持久化: %+v err=%v", persisted, err)
	}
}

func TestContextSurvivesSameIDResendAndReplacement(t *testing.T) {
	d, st, m := newDisp(t)
	prepareNavHand(m, "hand-01", "b-1")
	root, err := d.DispatchStructured(businessNav("hand-01", "acct-ctx"))
	if err != nil {
		t.Fatal(err)
	}
	d.OnReconnect("hand-01", "b-1")

	m.mu.Lock()
	sent := append([]protocol.Envelope(nil), m.sent...)
	m.mu.Unlock()
	if len(sent) < 2 || sent[0].MsgID != root || sent[1].MsgID != root {
		t.Fatalf("同代重连应同 msgId 重发: %+v", sent)
	}
	for i := 0; i < 2; i++ {
		var body protocol.CmdBody
		_ = json.Unmarshal(sent[i].Body, &body)
		if body.Context == nil || body.Context.AccountRef != "acct-ctx" || body.Context.ExpectedPrincipalFingerprint != "opaque-fp-1" {
			t.Fatalf("第 %d 次发送丢 context: %+v", i+1, body.Context)
		}
	}

	// 未 ack，走 deadline 重派；ReplaceCmd 必须原子继承 context 并保持 logical id。
	d.sweepFaults(future())
	lineage, err := st.CmdLineage(root)
	if err != nil || len(lineage) != 2 {
		t.Fatalf("应形成两节点 replacement chain: len=%d err=%v", len(lineage), err)
	}
	if lineage[1].LogicalDispatchID != root || lineage[1].ContextJSON != lineage[0].ContextJSON || lineage[1].ParentMsgID == nil {
		t.Fatalf("replacement 未继承 logical/context: %+v", lineage)
	}
}

func TestWaitLogicalIgnoresIntermediateVoid(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	root, err := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan *store.LogicalDispatchState, 1)
	go func() {
		state, _ := d.WaitLogical(ctx, root)
		done <- state
	}()

	d.sweepFaults(future())
	select {
	case got := <-done:
		t.Fatalf("中间 void 不得唤醒逻辑等待: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	state, err := st.LogicalDispatch(root)
	if err != nil || state.Length != 2 || state.Settled {
		t.Fatalf("replacement leaf 应仍在途: %+v err=%v", state, err)
	}
	d.OnResult("hand-01", "result-leaf", protocol.ResultBody{
		Ref: state.Leaf.MsgID, Status: protocol.ResultStatusOk,
		Data: json.RawMessage(`{"echo":null,"swStartedAt":1}`),
	})
	select {
	case got := <-done:
		if got == nil || !got.Settled || got.Leaf.MsgID != state.Leaf.MsgID || got.Leaf.Status != store.CmdOk {
			t.Fatalf("最终 leaf 返回错误: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("最终 result 未唤醒 WaitLogical")
	}
}

func TestWaitLogicalSubscriptionsAreReleased(t *testing.T) {
	d, _, m := newDisp(t)
	m.up("hand-01", "b-1")
	root, err := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := d.WaitLogical(ctx, root)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消等待返回错误: %v", err)
	}
	d.waitMu.Lock()
	defer d.waitMu.Unlock()
	if len(d.waits) != 0 {
		t.Fatalf("WaitLogical 订阅泄漏: %+v", d.waits)
	}
}

func TestFailedRetryableResultBuildsAtomicBackoffLineage(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	root, err := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	before := m.sentCount()
	d.OnResult("hand-01", "result-retryable", protocol.ResultBody{
		Ref: root, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodeExecTimeoutHand, Retryable: protocol.RetryableYes, SideEffect: protocol.SideEffectNone,
		},
	})
	lineage, err := st.CmdLineage(root)
	if err != nil || len(lineage) != 2 || lineage[0].Status != store.CmdFailed || lineage[1].Status != store.CmdQueued {
		t.Fatalf("failed 与 replacement 未原子建链: %+v err=%v", lineage, err)
	}
	child := lineage[1]
	// OnResult 会回一条 ack，但退避期内不得出现新 cmd。
	if child.NotBeforeAt == nil || child.RedispatchN != 1 || m.sentCount() != before+1 {
		t.Fatalf("首次安全重派必须落库 5s 退避且不立即发送: %+v", child)
	}
	logical, _ := st.LogicalDispatch(root)
	if logical.Settled {
		t.Fatal("中间 failed 不得提前收束逻辑派发")
	}
	d.sweepFaults(child.NotBeforeAt.Add(time.Millisecond))
	if m.sentCount() != before+2 {
		t.Fatal("退避到期后未发送 replacement")
	}
}

func TestAfterRecoveryResultReturnsToActorWithoutBlindRetry(t *testing.T) {
	d, st, m := newDisp(t)
	prepareNavHand(m, "hand-01", "b-1")
	root, err := d.DispatchStructured(businessNav("hand-01", "acct-recovery"))
	if err != nil {
		t.Fatal(err)
	}
	d.OnResult("hand-01", "result-after-recovery", protocol.ResultBody{
		Ref: root, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodeCtxNotReady, Retryable: protocol.RetryableAfterRecovery, SideEffect: protocol.SideEffectNone,
		},
	})
	lineage, err := st.CmdLineage(root)
	if err != nil || len(lineage) != 1 || lineage[0].Status != store.CmdFailed {
		t.Fatalf("afterRecovery 必须先返回 actor 做 ensure，不得盲重试: %+v err=%v", lineage, err)
	}
	if !hasAudit(t, st, "result_retry_after_recovery", root) {
		t.Fatal("afterRecovery 缺少显式审计")
	}
}

func TestResultTransactionDoesNotReenterSingleSQLiteConnection(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	if err := st.CreateAccount(&store.Account{Platform: "zhilian", AccountRef: "acct-poll"}); err != nil {
		t.Fatal(err)
	}
	msgID, err := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		// 空 data 会触发 primitive_data_invalid 审计；审计必须在命令+去重
		// 事务之外执行，否则 SetMaxOpenConns(1) 下会自锁。
		d.OnResult("hand-01", "result-invalid-concurrent", protocol.ResultBody{
			Ref: msgID, Status: protocol.ResultStatusOk, Data: json.RawMessage(`{}`),
		})
		close(done)
	}()
	pollsDone := make(chan error, 1)
	go func() {
		for range 20 {
			if _, err := st.Accounts(); err != nil {
				pollsDone <- err
				return
			}
		}
		pollsDone <- nil
	}()
	deadline := time.After(3 * time.Second)
	for done != nil || pollsDone != nil {
		select {
		case <-done:
			done = nil
		case err := <-pollsDone:
			if err != nil {
				t.Fatalf("并发 Accounts 读取失败: %v", err)
			}
			pollsDone = nil
		case <-deadline:
			t.Fatal("result 事务反入 Store，阻塞了单连接 SQLite")
		}
	}
	if !hasAudit(t, st, "primitive_data_invalid", msgID) {
		t.Fatal("非法结果事务完成后缺少审计")
	}
}

func TestLeaseProgressCancelAndResultRace(t *testing.T) {
	d, st, m := newDisp(t)
	prepareNavHand(m, "hand-01", "b-1")
	msgID, err := d.DispatchStructured(businessNav("hand-01", "acct-lease"))
	if err != nil {
		t.Fatal(err)
	}
	d.OnAck("hand-01", protocol.AckBody{Ref: msgID, Status: protocol.AckStatusAccepted})

	d.leaseMu.Lock()
	first := *d.leases[msgID]
	d.leaseMu.Unlock()
	if first.expiresAt.After(first.deadline) {
		t.Fatal("初始 lease 越过 absolute deadline")
	}
	d.OnProgress("hand-01", protocol.ProgressBody{Ref: msgID, Stage: "page 1", Pct: 20})
	d.leaseMu.Lock()
	refreshed := *d.leases[msgID]
	d.leaseMu.Unlock()
	if refreshed.expiresAt.Before(first.expiresAt) || refreshed.expiresAt.After(refreshed.deadline) {
		t.Fatalf("progress 刷新错误: first=%v refreshed=%v deadline=%v", first.expiresAt, refreshed.expiresAt, refreshed.deadline)
	}

	// 模拟手在收到 cancel 的同一竞态窗回目标 result；发送返回后脑再做 gap
	// 终局化时必须观察到 result 已赢，不能覆盖成 void/replacement。
	m.mu.Lock()
	m.onSend = func(handID string, env protocol.Envelope) {
		if env.Kind == protocol.KindCancel {
			d.OnResult(handID, "target-result", protocol.ResultBody{Ref: msgID, Status: protocol.ResultStatusCanceled})
		}
	}
	m.mu.Unlock()
	d.sweepFaults(refreshed.expiresAt.Add(time.Millisecond))
	var cancelEnv protocol.Envelope
	m.mu.Lock()
	for _, env := range m.sent {
		if env.Kind == protocol.KindCancel {
			cancelEnv = env
		}
	}
	m.mu.Unlock()
	if cancelEnv.MsgID == "" {
		t.Fatal("lease gap 未发送 cancel")
	}
	d.OnAck("hand-01", protocol.AckBody{Ref: cancelEnv.MsgID, Status: protocol.AckStatusAccepted})
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdCanceled {
		t.Fatalf("目标 result 必须赢 cancel 竞态,得到 %s", rec.Status)
	}
	d.sweepFaults(refreshed.deadline.Add(time.Second))
	m.mu.Lock()
	cancels := 0
	for _, env := range m.sent {
		if env.Kind == protocol.KindCancel {
			cancels++
		}
	}
	m.mu.Unlock()
	if cancels != 1 {
		t.Fatalf("cancel 必须唯一,得到 %d", cancels)
	}
}

func TestExpiredProgressCannotReviveLease(t *testing.T) {
	d, st, m := newDisp(t)
	prepareNavHand(m, "hand-01", "b-1")
	msgID, err := d.DispatchStructured(businessNav("hand-01", "acct-expired-progress"))
	if err != nil {
		t.Fatal(err)
	}
	d.OnAck("hand-01", protocol.AckBody{Ref: msgID, Status: protocol.AckStatusAccepted})
	expiredAt := time.Now().Add(-time.Millisecond)
	d.leaseMu.Lock()
	d.leases[msgID].expiresAt = expiredAt
	d.leaseMu.Unlock()

	if d.OnProgress("hand-01", protocol.ProgressBody{Ref: msgID, Stage: "too-late", Pct: 50}) {
		t.Fatal("已过期 progress 不得续租")
	}
	d.leaseMu.Lock()
	got := d.leases[msgID].expiresAt
	d.leaseMu.Unlock()
	if !got.Equal(expiredAt) {
		t.Fatalf("迟到 progress 改写了租约: got=%v want=%v", got, expiredAt)
	}
	if !hasAudit(t, st, "progress_after_lease", msgID) {
		t.Fatal("已过期 progress 缺少响亮审计")
	}
}

func TestLeaseCancelKeepsBoundedResultWindowBeforeReplacement(t *testing.T) {
	d, st, m := newDisp(t)
	prepareNavHand(m, "hand-01", "b-1")
	msgID, err := d.DispatchStructured(businessNav("hand-01", "acct-window"))
	if err != nil {
		t.Fatal(err)
	}
	d.OnAck("hand-01", protocol.AckBody{Ref: msgID, Status: protocol.AckStatusAccepted})
	d.leaseMu.Lock()
	expires := d.leases[msgID].expiresAt
	d.leaseMu.Unlock()

	d.sweepFaults(expires.Add(time.Millisecond))
	state, err := st.LogicalDispatch(msgID)
	if err != nil || state.Length != 1 || state.Leaf.Status != store.CmdAccepted {
		t.Fatalf("cancel 发出后不应立即 replacement: %+v err=%v", state, err)
	}
	d.sweepFaults(expires.Add(time.Duration(protocol.DefaultCancelSettleMs-1) * time.Millisecond))
	state, _ = st.LogicalDispatch(msgID)
	if state.Length != 1 {
		t.Fatalf("result 竞态窗内出现 replacement: %+v", state)
	}
	d.sweepFaults(expires.Add(time.Duration(protocol.DefaultCancelSettleMs+1) * time.Millisecond))
	state, err = st.LogicalDispatch(msgID)
	if err != nil || state.Length != 2 || state.Settled {
		t.Fatalf("竞态窗后应建立 replacement leaf: %+v err=%v", state, err)
	}
}

func TestAckResultSourceAndPrimitiveDataAreValidated(t *testing.T) {
	d, st, m := newDisp(t)
	prepareNavHand(m, "hand-01", "b-1")
	prepareNavHand(m, "hand-02", "b-2")
	msgID, err := d.DispatchStructured(businessNav("hand-01", "acct-source"))
	if err != nil {
		t.Fatal(err)
	}
	d.OnAck("hand-02", protocol.AckBody{Ref: msgID, Status: protocol.AckStatusAccepted})
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdSent {
		t.Fatalf("异手 ack 推进了命令: %s", rec.Status)
	}
	d.OnAck("hand-01", protocol.AckBody{Ref: msgID, Status: protocol.AckStatusAccepted})
	d.OnResult("hand-02", "wrong-hand-result", protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusOk,
		Data: json.RawMessage(`{"ready":true,"loginState":"in","createdTab":false}`),
	})
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdAccepted {
		t.Fatalf("异手 result 终局化了命令: %s", rec.Status)
	}
	d.OnResult("hand-01", "bad-data-result", protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusOk, Data: json.RawMessage(`{}`),
	})
	rec, _ := st.CmdByMsgID(msgID)
	if rec.Status != store.CmdFailed || rec.ErrorCode != string(protocol.ErrCodeInternalHand) {
		t.Fatalf("非法 primitive data 未落成响亮失败: %+v", rec)
	}
}

func TestResultPersistenceFailureDoesNotAckHand(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, err := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	before := m.sentCount()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	d.OnResult("hand-01", "result-db-down", protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusOk,
		Data: json.RawMessage(`{"echo":null,"swStartedAt":1}`),
	})
	if got := m.sentCount(); got != before {
		t.Fatalf("持久化失败仍发 ack: before=%d after=%d", before, got)
	}
}

func TestM2NegotiationGatesIndependent(t *testing.T) {
	d, _, m := newDisp(t)
	m.up("hand-01", "b-1")
	req := businessNav("hand-01", "acct-gate")
	m.negotiate("hand-01", nil, allM2Features)
	if _, err := d.DispatchStructured(req); !errors.Is(err, ErrCapability) {
		t.Fatalf("缺 primitive cap 应拒绝: %v", err)
	}
	for i, missing := range allM2Features {
		features := append([]string(nil), allM2Features[:i]...)
		features = append(features, allM2Features[i+1:]...)
		m.negotiate("hand-01", []string{protocol.PrimNavEnsureSurface + "@1"}, features)
		if _, err := d.DispatchStructured(req); !errors.Is(err, ErrFeature) {
			t.Fatalf("缺 %s 应独立拒绝: %v", missing, err)
		}
	}
}
