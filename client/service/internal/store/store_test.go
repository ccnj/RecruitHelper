package store

import (
	"sync"
	"testing"
	"time"
)

// 并发 MutateCmd 不丢更新、不撞 SQLITE_BUSY(红队 F1 回归:SetMaxOpenConns(1) 串行化)。
func TestConcurrentMutateNoBusyNoLost(t *testing.T) {
	s := openTest(t)
	if err := s.CreateCmd(&CmdRecord{MsgID: "M1", Name: "debug.ping", Class: "readonly", HandID: "h", Status: CmdQueued}); err != nil {
		t.Fatalf("CreateCmd: %v", err)
	}
	const N = 100
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.MutateCmd("M1", func(r *CmdRecord) error { r.Attempt++; return nil })
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("并发 MutateCmd[%d] 失败(疑似 SQLITE_BUSY): %v", i, e)
		}
	}
	rec, _ := s.CmdByMsgID("M1")
	if rec.Attempt != N {
		t.Fatalf("100 次并发自增应无丢失得 %d,实得 %d(丢更新)", N, rec.Attempt)
	}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenMigrateAndWAL(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("首次 Open: %v", err)
	}
	mode, err := s.JournalMode()
	if err != nil {
		t.Fatalf("查 journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, 期望 wal", mode)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 二次打开(AutoMigrate 幂等)
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("二次 Open: %v", err)
	}
	_ = s2.Close()
}

func TestHands(t *testing.T) {
	s := openTest(t)
	h := &PairedHand{
		HandID:    "hand-01",
		TokenHash: HashToken("tok-abc"),
		Origin:    "chrome-extension://abcdefg",
		Label:     "测试手",
		CreatedAt: time.Now(),
	}
	if err := s.UpsertHand(h); err != nil {
		t.Fatalf("UpsertHand: %v", err)
	}
	got, err := s.HandByID("hand-01")
	if err != nil || got == nil {
		t.Fatalf("HandByID: %v, got=%v", err, got)
	}
	if got.TokenHash != HashToken("tok-abc") {
		t.Fatalf("TokenHash 不匹配")
	}
	if got.TokenHash == "tok-abc" {
		t.Fatalf("token 明文不许落库")
	}
	// 未知手 → (nil, nil)
	none, err := s.HandByID("hand-99")
	if err != nil || none != nil {
		t.Fatalf("未知手应返回 (nil,nil),得到 %v, %v", none, err)
	}
	if err := s.TouchHand("hand-01", time.Now()); err != nil {
		t.Fatalf("TouchHand: %v", err)
	}
	hs, err := s.Hands()
	if err != nil || len(hs) != 1 {
		t.Fatalf("Hands: %v, n=%d", err, len(hs))
	}
}

func TestCmdLedgerLifecycle(t *testing.T) {
	s := openTest(t)
	c := &CmdRecord{
		MsgID: "M1", Name: "debug.ping", Class: "readonly",
		HandID: "hand-01", Session: "s-1", BootIDAtDispatch: "b-1",
		Status: CmdQueued, Attempt: 0, DeadlineMs: time.Now().UnixMilli() + 30000,
	}
	if err := s.CreateCmd(c); err != nil {
		t.Fatalf("CreateCmd: %v", err)
	}
	// queued → sent → accepted → ok
	for _, st := range []CmdStatus{CmdSent, CmdAccepted, CmdOk} {
		if err := s.MutateCmd("M1", func(r *CmdRecord) error {
			r.Status = st
			if st == CmdSent {
				r.Attempt++
			}
			return nil
		}); err != nil {
			t.Fatalf("MutateCmd → %s: %v", st, err)
		}
	}
	got, _ := s.CmdByMsgID("M1")
	if got.Status != CmdOk || got.Attempt != 1 {
		t.Fatalf("终局状态错误: %+v", got)
	}
	if got.TerminalAt == nil {
		t.Fatalf("进入终局必须盖 TerminalAt")
	}
	// 按状态检索
	in, err := s.CmdsInStatus(CmdOk, CmdSuspect)
	if err != nil || len(in) != 1 {
		t.Fatalf("CmdsInStatus: %v, n=%d", err, len(in))
	}
	// 终局判定表
	if CmdQueued.Terminal() || CmdSent.Terminal() || CmdAccepted.Terminal() {
		t.Fatalf("非终局状态被判为终局")
	}
	for _, st := range []CmdStatus{CmdOk, CmdFailed, CmdCanceled, CmdExpired, CmdRejected, CmdVoid, CmdSuspect, CmdResolvedOk, CmdResolvedFailed} {
		if !st.Terminal() {
			t.Fatalf("%s 应为终局", st)
		}
	}
}

func TestProcessedDedup(t *testing.T) {
	s := openTest(t)
	already, err := s.MarkProcessed("R1", "result", "hand-01")
	if err != nil || already {
		t.Fatalf("首见应 already=false: %v, %v", already, err)
	}
	already, err = s.MarkProcessed("R1", "result", "hand-01")
	if err != nil || !already {
		t.Fatalf("重复应 already=true: %v, %v", already, err)
	}
}

func TestAudit(t *testing.T) {
	s := openTest(t)
	s.Audit("superseded", "hand-01", "", "旧连接被新 hello 顶替")
	s.Audit("late_frame", "hand-01", "M9", "终局后收到迟到 result")
	es, err := s.AuditEntries(10)
	if err != nil || len(es) != 2 {
		t.Fatalf("AuditEntries: %v, n=%d", err, len(es))
	}
	if es[0].Category != "late_frame" {
		t.Fatalf("应按倒序返回,得到 %q", es[0].Category)
	}
}
