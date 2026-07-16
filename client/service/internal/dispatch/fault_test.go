package dispatch

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// mockSender:不经 WS 的假手,用于超时引擎单元测试(快、确定)。
type mockSender struct {
	mu                sync.Mutex
	online            map[string]bool
	boot              map[string]string
	sent              []protocol.Envelope
	closed            []string
	offlineMs         int64
	keepOnlineOnClose bool // true=CloseHand 不即时置离线(模拟真实 hub 异步关连接)
}

func newMock() *mockSender {
	return &mockSender{online: map[string]bool{}, boot: map[string]string{}}
}

func (m *mockSender) up(handID, boot string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.online[handID] = true
	m.boot[handID] = boot
}

func (m *mockSender) SendEnvelope(handID string, env protocol.Envelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.online[handID] {
		return ErrHandOffline
	}
	m.sent = append(m.sent, env)
	return nil
}

func (m *mockSender) HandSession(handID string) (string, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.online[handID] {
		return "", "", false
	}
	return "s-test", m.boot[handID], true
}

func (m *mockSender) CloseHand(handID, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = append(m.closed, handID)
	if !m.keepOnlineOnClose {
		m.online[handID] = false // 关连接 = 离线
	}
}

func (m *mockSender) HandOfflineMs(handID string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.online[handID] {
		return 0
	}
	return m.offlineMs // 测试可设
}

func (m *mockSender) sentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func newDisp(t *testing.T) (*Dispatcher, *store.Store, *mockSender) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := newMock()
	return New(st, m), st, m
}

func hasAudit(t *testing.T, st *store.Store, category, msgID string) bool {
	t.Helper()
	es, _ := st.AuditEntries(200)
	for _, e := range es {
		if e.Category == category && (msgID == "" || e.RefMsgID == msgID) {
			return true
		}
	}
	return false
}

var future = func() time.Time { return time.Now().Add(1 * time.Hour) }

// effectful 超 deadline+宽限无终局 → suspect(法条1)。
func TestEffectfulDeadlineSuspect(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, err := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	d.sweepFaults(future())
	rec, _ := st.CmdByMsgID(msgID)
	if rec.Status != store.CmdSuspect {
		t.Fatalf("effectful 超时应 suspect,得到 %s", rec.Status)
	}
	if !hasAudit(t, st, "suspect", msgID) {
		t.Fatalf("应有 suspect 审计")
	}
}

// readonly 超时 → void + 重派(未超 cap)。
func TestReadonlyVoidRedispatch(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))
	before := m.sentCount()
	d.sweepFaults(future())
	rec, _ := st.CmdByMsgID(msgID)
	if rec.Status != store.CmdVoid {
		t.Fatalf("readonly 超时应 void,得到 %s", rec.Status)
	}
	if m.sentCount() != before+1 {
		t.Fatalf("应重派一条新命令")
	}
	// 新命令 RedispatchN=1
	recs, _ := st.RecentCmds(10)
	found := false
	for _, r := range recs {
		if r.Status == store.CmdSent && r.RedispatchN == 1 && r.Name == protocol.PrimDebugPing {
			found = true
		}
	}
	if !found {
		t.Fatalf("应存在 RedispatchN=1 的新 sent 命令")
	}
}

// readonly 重派耗尽(cap=2)→ 停止重派 + 审计。
func TestReadonlyRedispatchExhausted(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`))
	// 三次 sweep:void+redispatch(n1)、void+redispatch(n2)、void+耗尽(n2>=cap2)
	d.sweepFaults(future())
	d.sweepFaults(future())
	d.sweepFaults(future())
	if !hasAudit(t, st, "redispatch_exhausted", "") {
		t.Fatalf("应有 redispatch_exhausted 审计")
	}
	// 全部命令应为 void(无残留 sent)
	sentLeft := 0
	recs, _ := st.RecentCmds(20)
	for _, r := range recs {
		if r.Status == store.CmdSent {
			sentLeft++
		}
	}
	if sentLeft != 0 {
		t.Fatalf("耗尽后不应有残留 sent 命令,剩 %d", sentLeft)
	}
}

// suspect 收到迟到 result → 自动核销(法条6)。
func TestSuspectLateResultCleared(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.sweepFaults(future())
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdSuspect {
		t.Fatalf("应先 suspect")
	}
	// 迟到 result ok 到达
	d.OnResult("hand-01", "res-1", protocol.ResultBody{Ref: msgID, Status: protocol.ResultStatusOk})
	rec, _ := st.CmdByMsgID(msgID)
	if rec.Status != store.CmdOk {
		t.Fatalf("迟到 result 应自动核销为 ok,得到 %s", rec.Status)
	}
	if !hasAudit(t, st, "suspect_cleared", msgID) {
		t.Fatalf("应有 suspect_cleared 审计")
	}
}

// 串行域冻结(法条4):域内有 suspect → 拒新 effectful/intrusive;readonly 放行。
func TestDomainFreeze(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.sweepFaults(future())
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdSuspect {
		t.Fatalf("前置:应 suspect")
	}
	// 新 effectful 被冻结
	if _, err := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"ok"}`)); err != ErrDomainFrozen {
		t.Fatalf("effectful 应被域冻结,得到 %v", err)
	}
	// 新 intrusive 被冻结
	if _, err := d.Dispatch("hand-01", protocol.PrimDebugSwitchWindow, json.RawMessage(`{}`)); err != ErrDomainFrozen {
		t.Fatalf("intrusive 应被域冻结,得到 %v", err)
	}
	// readonly 放行(不进串行域)
	if _, err := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("readonly 不应被冻结,得到 %v", err)
	}
}

// effectful result 标 sideEffect=possible → suspect(而非 failed 终局)。
func TestEffectfulResultPossibleSuspect(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"failed"}`))
	d.OnResult("hand-01", "res-2", protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{Code: protocol.ErrCodeInternalHand, SideEffect: protocol.SideEffectPossible},
	})
	rec, _ := st.CmdByMsgID(msgID)
	if rec.Status != store.CmdSuspect {
		t.Fatalf("sideEffect=possible 应转 suspect,得到 %s", rec.Status)
	}
}

// ackTimeout:sent 超 3s 无应答且手在线 → 关连接。
func TestAckTimeoutClosesHand(t *testing.T) {
	d, _, m := newDisp(t)
	m.up("hand-01", "b-1")
	// slowEcho deadline 300s,确保 +4s 时未触发 deadline 分支
	d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.sweepFaults(time.Now().Add(4 * time.Second)) // 过 ackTimeout(3s),未过 deadline
	m.mu.Lock()
	n := len(m.closed)
	m.mu.Unlock()
	if n != 1 || m.closed[0] != "hand-01" {
		t.Fatalf("应关闭 hand-01 一次,得到 %v", m.closed)
	}
}
