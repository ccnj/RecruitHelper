package dispatch

import (
	"encoding/json"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// F7:suspect 收到 failed+possible 的迟到 result → 保持 suspect(歧义只转人工,不抹平)。
func TestSuspectPossibleLateResultKept(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.sweepFaults(future())
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdSuspect {
		t.Fatalf("前置:应 suspect")
	}
	// 迟到 result 是 failed+possible(仍歧义)
	d.OnResult("hand-01", "res-p", protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{Code: protocol.ErrCodeInternalHand, SideEffect: protocol.SideEffectPossible},
	})
	rec, _ := st.CmdByMsgID(msgID)
	if rec.Status != store.CmdSuspect {
		t.Fatalf("possible 迟到 result 应保持 suspect,得到 %s", rec.Status)
	}
	if !hasAudit(t, st, "suspect_kept", msgID) {
		t.Fatalf("应有 suspect_kept 审计")
	}
	// 对照:ok 迟到 result 才核销
	d.OnResult("hand-01", "res-ok", protocol.ResultBody{Ref: msgID, Status: protocol.ResultStatusOk})
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdOk {
		t.Fatalf("ok 迟到 result 应核销为 ok")
	}
}

// F4:intrusive 在冻结域内不重派(留 void)。
func TestIntrusiveNotRedispatchedInFrozenDomain(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	// 先造一个 suspect 冻结 debug:hand-01 域
	sID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	// 再派一个 intrusive(此刻域尚未冻结,能派出)
	iID, _ := d.Dispatch("hand-01", protocol.PrimDebugSwitchWindow, json.RawMessage(`{}`))
	if iID == "" {
		t.Fatalf("intrusive 派发前置失败")
	}
	before := m.sentCount()
	// sweep:slowEcho 先 suspect(冻结域),switchWindow 后 void——重派应被冻结拦下
	d.sweepFaults(future())
	if rec, _ := st.CmdByMsgID(sID); rec.Status != store.CmdSuspect {
		t.Fatalf("slowEcho 应 suspect")
	}
	if rec, _ := st.CmdByMsgID(iID); rec.Status != store.CmdVoid {
		t.Fatalf("switchWindow 应 void,得到 %s", rec.Status)
	}
	if m.sentCount() != before {
		t.Fatalf("冻结域内 intrusive 不应重派(不新增发送)")
	}
	if !hasAudit(t, st, "redispatch_frozen", iID) {
		t.Fatalf("应有 redispatch_frozen 审计")
	}
}

// F5/F8:瞬态拒绝回 queued 的命令,在存活连接上被 sweep 重投,不滞留到 deadline 误 suspect。
func TestTransientRejectRequeueRedriven(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	// 手回瞬态拒绝 QUEUE_FULL
	d.OnAck("hand-01", protocol.AckBody{Ref: msgID, Status: protocol.AckStatusRejected,
		Error: &protocol.ErrorBody{Code: protocol.ErrCodeQueueFull}})
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdQueued {
		t.Fatalf("瞬态拒绝应回 queued,得到 %s", rec.Status)
	}
	before := m.sentCount()
	// 未到 deadline 的 sweep → 重投驱动(不进 suspect)
	d.sweepFaults(time.Now())
	if m.sentCount() != before+1 {
		t.Fatalf("queued 命令应被 sweep 重投")
	}
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdSent {
		t.Fatalf("重投后应回 sent,得到 %s", rec.Status)
	}
}

// F12:OnAck 拒绝分支——协议性拒绝落 rejected 终局。
func TestOnAckProtocolReject(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))
	d.OnAck("hand-01", protocol.AckBody{Ref: msgID, Status: protocol.AckStatusRejected,
		Error: &protocol.ErrorBody{Code: protocol.ErrCodeProtoUnsupportedCmd}})
	rec, _ := st.CmdByMsgID(msgID)
	if rec.Status != store.CmdRejected {
		t.Fatalf("协议性拒绝应 rejected,得到 %s", rec.Status)
	}
	if !hasAudit(t, st, "cmd_rejected", msgID) {
		t.Fatalf("应有 cmd_rejected 审计")
	}
}

// F3:先发送后记 sent 的竞态下,ack 抢在 sent 记账前到达(命令处于 queued)也应收下 → accepted。
func TestOnAckOnQueuedAdvances(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	// 直接造一个 queued 命令(模拟"已发送但 sent 记账未落")
	_ = st.CreateCmd(&store.CmdRecord{MsgID: "Q1", Name: protocol.PrimDebugPing, Class: "readonly",
		HandID: "hand-01", Status: store.CmdQueued})
	d.OnAck("hand-01", protocol.AckBody{Ref: "Q1", Status: protocol.AckStatusAccepted})
	rec, _ := st.CmdByMsgID("Q1")
	if rec.Status != store.CmdAccepted {
		t.Fatalf("queued 收 accepted 应推进 accepted,得到 %s", rec.Status)
	}
}

// F9:迟到 ack 命中终局命令 → late_ack 审计不静默。
func TestLateAckAudited(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))
	// 先终局化
	d.OnResult("hand-01", "r1", protocol.ResultBody{Ref: msgID, Status: protocol.ResultStatusOk})
	// 迟到 ack 到达
	d.OnAck("hand-01", protocol.AckBody{Ref: msgID, Status: protocol.AckStatusAccepted})
	if !hasAudit(t, st, "late_ack", msgID) {
		t.Fatalf("终局后迟到 ack 应有 late_ack 审计")
	}
}

// F14:迟到 result 命中 void → 只审计 late_result,不改账不 suspect_cleared;未知 ref → orphan。
func TestLateResultOnVoidAndOrphan(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	// 造 void readonly(deadline 触发 void)
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))
	d.sweepFaults(future())
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdVoid {
		t.Fatalf("前置:应 void")
	}
	d.OnResult("hand-01", "rv", protocol.ResultBody{Ref: msgID, Status: protocol.ResultStatusOk})
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdVoid {
		t.Fatalf("void 收迟到 result 应仍 void,得到 %s", rec.Status)
	}
	if !hasAudit(t, st, "late_result", msgID) {
		t.Fatalf("应有 late_result 审计")
	}
	if hasAudit(t, st, "suspect_cleared", msgID) {
		t.Fatalf("void 不应产生 suspect_cleared")
	}
	// 未知 ref → orphan
	d.OnResult("hand-01", "ro", protocol.ResultBody{Ref: "nonexistent", Status: protocol.ResultStatusOk})
	if !hasAudit(t, st, "orphan_result", "nonexistent") {
		t.Fatalf("未知 ref 应 orphan_result 审计")
	}
}

// F13:Verdict 与迟到 result 赛跑——result 先核销 suspect,后续人裁得 ErrNotSuspect;online 异代立即放行 resolvedOk。
func TestVerdictRaces(t *testing.T) {
	d, st, m := newDisp(t)
	d.SetManualDelayMs(0)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.sweepFaults(future())
	// 迟到 result 先核销
	d.OnResult("hand-01", "rc", protocol.ResultBody{Ref: msgID, Status: protocol.ResultStatusOk})
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdOk {
		t.Fatalf("应已核销为 ok")
	}
	// 人裁应作罢
	if err := d.Verdict(msgID, store.CmdResolvedFailed); err != ErrNotSuspect {
		t.Fatalf("已核销后人裁应 ErrNotSuspect,得到 %v", err)
	}

	// online 异代立即放行 resolvedOk
	msg2, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.sweepFaults(future())
	m.up("hand-01", "b-2") // 换代仍在线
	if err := d.Verdict(msg2, store.CmdResolvedOk); err != nil {
		t.Fatalf("online 异代应放行,得到 %v", err)
	}
	if rec, _ := st.CmdByMsgID(msg2); rec.Status != store.CmdResolvedOk {
		t.Fatalf("应 resolvedOk")
	}
}

// F4 残漏:OnReconnect 两阶段——intrusive 创建早于同域 effectful,换代收编时 intrusive
// 不应被派进即将冻结的域(不依赖 created_at 枚举顺序)。
func TestReconnectTwoPhaseFreeze(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	iID, _ := d.Dispatch("hand-01", protocol.PrimDebugSwitchWindow, json.RawMessage(`{}`)) // intrusive 先建
	eID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	sentBefore := m.sentCount()

	m.up("hand-01", "b-2") // 换代重连
	d.OnReconnect("hand-01", "b-2")

	if rec, _ := st.CmdByMsgID(eID); rec.Status != store.CmdSuspect {
		t.Fatalf("换代 effectful 应 suspect,得到 %s", rec.Status)
	}
	if rec, _ := st.CmdByMsgID(iID); rec.Status != store.CmdVoid {
		t.Fatalf("换代 intrusive 应 void,得到 %s", rec.Status)
	}
	if m.sentCount() != sentBefore {
		t.Fatalf("冻结域内 intrusive 不应重派(不新增发送)")
	}
	if !hasAudit(t, st, "redispatch_frozen", iID) {
		t.Fatalf("应有 redispatch_frozen 审计(两阶段生效)")
	}
}

// F10 残漏:重传的迟到 result(possible / orphan)不重复刷审计。
func TestRetransmitNoAuditSpam(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.sweepFaults(future()) // → suspect
	// 同一 possible result 重传 3 次(同 resultMsgID)
	for range 3 {
		d.OnResult("hand-01", "res-dup", protocol.ResultBody{
			Ref: msgID, Status: protocol.ResultStatusFailed,
			Error: &protocol.ErrorBody{Code: protocol.ErrCodeInternalHand, SideEffect: protocol.SideEffectPossible},
		})
	}
	if n := countAudit(t, st, "suspect_kept", msgID); n != 1 {
		t.Fatalf("possible result 重传 3 次应只 1 条 suspect_kept,得到 %d", n)
	}
	// orphan 同理
	for range 3 {
		d.OnResult("hand-01", "res-orph", protocol.ResultBody{Ref: "nope", Status: protocol.ResultStatusOk})
	}
	if n := countAudit(t, st, "orphan_result", "nope"); n != 1 {
		t.Fatalf("orphan 重传 3 次应只 1 条 orphan_result,得到 %d", n)
	}
}

// F11:单次 sweep 内同手多条 sent,即使 CloseHand 异步(手仍在线)也只关一次、wedged 仅 +1。
func TestSweepSingleClosePerHand(t *testing.T) {
	d, st, m := newDisp(t)
	m.keepOnlineOnClose = true // 模拟真实 hub:关连接不即时离线
	m.up("hand-01", "b-1")
	d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.sweepFaults(time.Now().Add(4 * time.Second)) // 过 ackTimeout,未过 deadline
	m.mu.Lock()
	nClose := len(m.closed)
	m.mu.Unlock()
	if nClose != 1 {
		t.Fatalf("单次 sweep 应只关一次连接,得到 %d", nClose)
	}
	d.wmu.Lock()
	w := d.wedged["hand-01"]
	d.wmu.Unlock()
	if w != 1 {
		t.Fatalf("wedged 应只 +1,得到 %d", w)
	}
	_ = st
}
