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
	caps              map[string][]string
	features          map[string][]string
	contractMatch     map[string]bool
	session           map[string]string
	sent              []protocol.Envelope
	closed            []string
	offlineMs         int64
	keepOnlineOnClose bool // true=CloseHand 不即时置离线(模拟真实 hub 异步关连接)
	onSend            func(string, protocol.Envelope)
	beforeClose       func(handID, expectedSession string)
	witness           map[string]HandWitness
}

func newMock() *mockSender {
	return &mockSender{
		online: map[string]bool{}, boot: map[string]string{}, session: map[string]string{},
		caps: map[string][]string{}, features: map[string][]string{}, contractMatch: map[string]bool{},
		witness: map[string]HandWitness{},
	}
}

func (m *mockSender) HandWitness(handID string) (HandWitness, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	witness, ok := m.witness[handID]
	return witness, ok && m.online[handID]
}

func (m *mockSender) negotiate(handID string, caps, features []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.caps[handID] = append([]string(nil), caps...)
	m.features[handID] = append([]string(nil), features...)
}

func (m *mockSender) up(handID, boot string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.online[handID] = true
	m.boot[handID] = boot
	m.contractMatch[handID] = true
	if m.session[handID] == "" {
		m.session[handID] = "s-test"
	}
}

func (m *mockSender) HandContractMatch(handID string) (bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.online[handID] {
		return false, false
	}
	return m.contractMatch[handID], true
}

func (m *mockSender) setContractMatch(handID string, matched bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.contractMatch[handID] = matched
}

func (m *mockSender) setSession(handID, session string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session[handID] = session
}

func (m *mockSender) SendEnvelope(handID string, env protocol.Envelope) error {
	m.mu.Lock()
	if !m.online[handID] {
		m.mu.Unlock()
		return ErrHandOffline
	}
	m.sent = append(m.sent, env)
	hook := m.onSend
	m.mu.Unlock()
	if hook != nil {
		hook(handID, env)
	}
	return nil
}

func (m *mockSender) HandSession(handID string) (string, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.online[handID] {
		return "", "", false
	}
	return m.session[handID], m.boot[handID], true
}

func (m *mockSender) HandNegotiation(handID string) ([]string, []string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.online[handID] {
		return nil, nil, false
	}
	return append([]string(nil), m.caps[handID]...), append([]string(nil), m.features[handID]...), true
}

func (m *mockSender) CloseHand(handID, expectedSession, _ string) bool {
	m.mu.Lock()
	hook := m.beforeClose
	m.mu.Unlock()
	if hook != nil {
		hook(handID, expectedSession)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.online[handID] || m.session[handID] != expectedSession {
		return false
	}
	m.closed = append(m.closed, handID)
	if !m.keepOnlineOnClose {
		m.online[handID] = false // 关连接 = 离线
	}
	return true
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
	return countAudit(t, st, category, msgID) > 0
}

func countAudit(t *testing.T, st *store.Store, category, msgID string) int {
	t.Helper()
	es, _ := st.AuditEntries(500)
	n := 0
	for _, e := range es {
		if e.Category == category && (msgID == "" || e.RefMsgID == msgID) {
			n++
		}
	}
	return n
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

// readonly 超时 → void + 持久化退避 replacement(未超 cap)。
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
	if m.sentCount() != before {
		t.Fatalf("超时后不得跳过 5s 退避立即发送")
	}
	// 新命令 RedispatchN=1，退避门槛必须落库。
	recs, _ := st.RecentCmds(10)
	var replacement *store.CmdRecord
	for _, r := range recs {
		if r.Status == store.CmdQueued && r.RedispatchN == 1 && r.Name == protocol.PrimDebugPing {
			copy := r
			replacement = &copy
		}
	}
	if replacement == nil || replacement.NotBeforeAt == nil {
		t.Fatalf("应存在带 NotBeforeAt 的 RedispatchN=1 queued 命令: %+v", recs)
	}
	d.sweepFaults(replacement.NotBeforeAt.Add(time.Millisecond))
	if m.sentCount() != before+1 {
		t.Fatal("退避到期后应发送 replacement")
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
	d.OnResult("hand-01", "res-1", protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusOk, Data: json.RawMessage(`{"echoedAfterMs":0}`),
	})
	rec, _ := st.CmdByMsgID(msgID)
	if rec.Status != store.CmdOk {
		t.Fatalf("迟到 result 应自动核销为 ok,得到 %s", rec.Status)
	}
	if !hasAudit(t, st, "suspect_cleared", msgID) {
		t.Fatalf("应有 suspect_cleared 审计")
	}
}

// suspect 只隔离原 idemKey/业务动作，不再占用账号串行域。
func TestSuspectReleasesDomain(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	msgID, _ := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	d.sweepFaults(future())
	if rec, _ := st.CmdByMsgID(msgID); rec.Status != store.CmdSuspect {
		t.Fatalf("前置:应 suspect")
	}
	if _, err := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"ok"}`)); err != nil {
		t.Fatalf("其他 effectful 应继续派发,得到 %v", err)
	}
	if _, err := d.Dispatch("hand-01", protocol.PrimDebugSwitchWindow, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("其他 intrusive 应继续派发,得到 %v", err)
	}
	if _, err := d.Dispatch("hand-01", protocol.PrimDebugPing, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("readonly 应继续派发,得到 %v", err)
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

// ackTimeout sweep 已确认旧 session 在线后若恰好发生 takeover，CloseHand 的
// expectedSession 栅栏必须使旧证词 no-op，不能关掉刚上线的新 session。
func TestAckTimeoutTakeoverDoesNotCloseNewSession(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-same")
	m.setSession("hand-01", "session-old")
	msgID, err := d.Dispatch("hand-01", protocol.PrimDebugSlowEcho,
		json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	if err != nil {
		t.Fatal(err)
	}
	m.beforeClose = func(handID, expectedSession string) {
		if handID != "hand-01" || expectedSession != "session-old" {
			t.Errorf("sweep 携带的关闭证词错误: hand=%s session=%s", handID, expectedSession)
		}
		m.setSession(handID, "session-new")
	}
	d.sweepFaults(time.Now().Add(4 * time.Second))

	m.mu.Lock()
	closed := append([]string(nil), m.closed...)
	current := m.session["hand-01"]
	online := m.online["hand-01"]
	m.mu.Unlock()
	if len(closed) != 0 || !online || current != "session-new" {
		t.Fatalf("旧 ackTimeout 证词误关新会话: closed=%v online=%v session=%s", closed, online, current)
	}
	if hasAudit(t, st, "ack_timeout", msgID) {
		t.Fatal("未实际关链不应写 ack_timeout 成功审计")
	}
	d.wmu.Lock()
	wedge := d.wedged["hand-01"]
	d.wmu.Unlock()
	if wedge != 0 {
		t.Fatalf("no-op 关闭不应累加 HAND_WEDGED: %d", wedge)
	}
}
