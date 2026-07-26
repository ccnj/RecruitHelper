package dispatch

import (
	"encoding/json"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// 重连 bootId 未变 → 同 msgId 重发(attempt 递增)。
func TestReconnectSameBootResend(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	before := m.sentCount()

	// 手重连,同 bootId
	d.OnReconnect("hand-01", "b-1")

	if m.sentCount() != before+1 {
		t.Fatalf("同 bootId 应重发一次")
	}
	rec, _ := st.CmdByMsgID(msgID)
	if rec.Status.Terminal() {
		t.Fatalf("重发不应终局化,得到 %s", rec.Status)
	}
	if rec.Attempt < 2 {
		t.Fatalf("重发应递增 attempt,得到 %d", rec.Attempt)
	}
	if !hasAudit(t, st, "resend", msgID) {
		t.Fatalf("应有 resend 审计")
	}
}

// 重连 bootId 变了 → effectful 转 suspect,readonly void。
func TestReconnectBootChangedCollects(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	effID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	roID, _ := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))

	// 手换代重连(SW 死后新 bootId)
	m.up("hand-01", "b-2")
	d.OnReconnect("hand-01", "b-2")

	if rec, _ := st.CmdByMsgID(effID); rec.Status != store.CmdSuspect {
		t.Fatalf("effectful bootId 换代应 suspect,得到 %s", rec.Status)
	}
	if rec, _ := st.CmdByMsgID(roID); rec.Status != store.CmdVoid {
		t.Fatalf("readonly bootId 换代应 void,得到 %s", rec.Status)
	}
}

func TestReloadPrimitiveNeverEntersAutomaticRedispatch(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-reload", "boot-old")
	msgID, err := d.Dispatch("hand-reload", protocol.PrimDebugReload, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Dispatch reload: %v", err)
	}
	before := m.sentCount()

	// 同 boot 重连通常会重发原 msgId；reload 必须直接收束，不能再次杀 SW。
	d.OnReconnect("hand-reload", "boot-old")
	if m.sentCount() != before {
		t.Fatalf("reload 同 boot 重连不得自动重发: before=%d after=%d", before, m.sentCount())
	}
	if record, _ := st.CmdByMsgID(msgID); record.Status != store.CmdVoid {
		t.Fatalf("reload 未终局时同 boot 重连应 void，得到 %s", record.Status)
	}

	// queued 故障轨也不能在存活连接上偷偷补投。
	queuedID, err := d.Dispatch("hand-reload", protocol.PrimDebugReload, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Dispatch second reload: %v", err)
	}
	if err := st.MutateCmd(queuedID, func(record *store.CmdRecord) error {
		record.Status = store.CmdQueued
		record.SentAt = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before = m.sentCount()
	d.sweepFaults(time.Now())
	if m.sentCount() != before {
		t.Fatal("reload queued 故障轨不得自动补投")
	}
	if record, _ := st.CmdByMsgID(queuedID); record.Status != store.CmdVoid {
		t.Fatalf("reload queued 应转 void 等人工重试，得到 %s", record.Status)
	}
}

// 脑重启扫描:在途 readonly/intrusive 作废,effectful suspect。
func TestRecoverScan(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	effID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	roID, _ := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))
	inID, _ := d.Dispatch("hand-01", protocol.PrimDebugSwitchWindow, json.RawMessage(`{}`))

	// 模拟脑重启:用新 Dispatcher(共享同一 store)调 Recover
	d2 := New(st, m)
	d2.Recover()

	if rec, _ := st.CmdByMsgID(effID); rec.Status != store.CmdSuspect {
		t.Fatalf("重启后 effectful 应 suspect,得到 %s", rec.Status)
	}
	if rec, _ := st.CmdByMsgID(roID); rec.Status != store.CmdVoid {
		t.Fatalf("重启后 readonly 应 void,得到 %s", rec.Status)
	}
	if rec, _ := st.CmdByMsgID(inID); rec.Status != store.CmdVoid {
		t.Fatalf("重启后 intrusive 应 void,得到 %s", rec.Status)
	}
}

// 人工裁决前置(法条5):手在线同代拒裁;离线足时长放行。
func TestVerdictGating(t *testing.T) {
	d, st, m := newDisp(t)
	d.SetManualDelayMs(1000) // 门槛 1s(测试短值)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.sweepFaults(future())
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdSuspect {
		t.Fatalf("前置:应 suspect")
	}

	// 手在线且同代 → 拒裁(result 可能在途)
	if err := d.Verdict(msgID, store.CmdResolvedFailed); err != ErrVerdictNotReady {
		t.Fatalf("在线同代应拒裁,得到 %v", err)
	}

	// 手离线但不足门槛 → 拒裁
	m.mu.Lock()
	m.online["hand-01"] = false
	m.offlineMs = 500
	m.mu.Unlock()
	if err := d.Verdict(msgID, store.CmdResolvedFailed); err != ErrVerdictNotReady {
		t.Fatalf("离线不足门槛应拒裁,得到 %v", err)
	}

	// 手离线足门槛 → 放行
	m.mu.Lock()
	m.offlineMs = 2000
	m.mu.Unlock()
	if err := d.Verdict(msgID, store.CmdResolvedFailed); err != nil {
		t.Fatalf("离线足门槛应放行,得到 %v", err)
	}
	rec, _ := st.CmdByMsgID(msgID)
	if rec.Status != store.CmdResolvedFailed {
		t.Fatalf("裁决后应 resolvedFailed,得到 %s", rec.Status)
	}
}

// suspect 落下时账号域已经释放，人裁不再承担解冻账号的职责。
func TestSuspectReleasesDomainBeforeVerdict(t *testing.T) {
	d, st, m := newDisp(t)
	d.SetManualDelayMs(0)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.sweepFaults(future())
	if _, err := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"ok"}`)); err != nil {
		t.Fatalf("suspect 后其他动作应可派发: %v", err)
	}
	m.mu.Lock()
	m.online["hand-01"] = false
	m.mu.Unlock()
	if err := d.Verdict(msgID, store.CmdResolvedFailed); err != nil {
		t.Fatalf("裁决: %v", err)
	}
	_ = st
}

// HAND_WEDGED:连续 3 次 ackTimeout → 告警;正常 ack(resetWedged)清零。
func TestHandWedged(t *testing.T) {
	d, st, _ := newDisp(t)
	d.noteAckTimeout("hand-01")
	d.noteAckTimeout("hand-01")
	if hasAudit(t, st, "hand_wedged", "") {
		t.Fatalf("2 次不应告警")
	}
	d.noteAckTimeout("hand-01")
	if !hasAudit(t, st, "hand_wedged", "") {
		t.Fatalf("连续 3 次应告警 HAND_WEDGED")
	}
	// 正常 ack 清零
	d.resetWedged("hand-01")
	d.wmu.Lock()
	n := d.wedged["hand-01"]
	d.wmu.Unlock()
	if n != 0 {
		t.Fatalf("resetWedged 应清零,得到 %d", n)
	}
}
