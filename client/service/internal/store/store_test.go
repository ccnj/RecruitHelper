package store

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestStoreLoggerDoesNotInterpolateOpaqueIdentity(t *testing.T) {
	var output bytes.Buffer
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: newStoreLogger(&output),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&Candidate{}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	const sentinel = "RAW-USER-REF-MUST-NOT-LEAK-3f6bd4"
	var candidate Candidate
	queryErr := db.First(&candidate, "platform = ? AND platform_user_ref = ?", "zhilian", sentinel).Error
	if !errors.Is(queryErr, gorm.ErrRecordNotFound) {
		t.Fatalf("应制造一条可记录的查询失败: %v", queryErr)
	}
	logged := output.String()
	if logged == "" {
		t.Fatal("测试必须实际捕获一条 GORM warn 日志")
	}
	if strings.Contains(logged, sentinel) {
		t.Fatalf("参数化查询日志泄漏不透明平台身份: %s", logged)
	}
}

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
	registeredAt := time.Now()
	h, created, previousOrigin, err := s.RegisterHand("hand-01", "chrome-extension://abcdefg", registeredAt)
	if err != nil || !created || previousOrigin != "" {
		t.Fatalf("RegisterHand 首次登记: h=%+v created=%v previous=%q err=%v", h, created, previousOrigin, err)
	}
	got, err := s.HandByID("hand-01")
	if err != nil || got == nil {
		t.Fatalf("HandByID: %v, got=%v", err, got)
	}
	if got.Origin != "chrome-extension://abcdefg" || !got.CreatedAt.Equal(registeredAt) {
		t.Fatalf("手字段不匹配: %+v", got)
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

func TestRegisterHandConcurrentFirstHelloIsIdempotent(t *testing.T) {
	s := openTest(t)
	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	created := make([]bool, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, created[i], _, errs[i] = s.RegisterHand(
				"hand-concurrent", "chrome-extension://same", time.Now(),
			)
		}(i)
	}
	close(start)
	wg.Wait()
	createdN := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("并发登记[%d]: %v", i, errs[i])
		}
		if created[i] {
			createdN++
		}
	}
	if createdN != 1 {
		t.Fatalf("并发首次 hello 必须只创建一次，实际 %d", createdN)
	}
	hands, err := s.Hands()
	if err != nil || len(hands) != 1 || hands[0].HandID != "hand-concurrent" {
		t.Fatalf("并发登记后 hands 不幂等: %+v err=%v", hands, err)
	}
}

func TestHandSchemaHasNoPairingCredentials(t *testing.T) {
	s := openTest(t)
	if !s.db.Migrator().HasTable("hands") {
		t.Fatal("新模型必须使用 hands 表")
	}
	if s.db.Migrator().HasTable("paired_hands") {
		t.Fatal("新安装不得创建 paired_hands 旧表")
	}
	columns, err := s.db.Migrator().ColumnTypes(&Hand{})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range columns {
		if column.Name() == "token_hash" {
			t.Fatal("hands 不得保留 token_hash")
		}
	}
}

func TestOpenDropsRetiredPairedHandsTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		withRow bool
	}{
		{name: "空旧表"},
		{name: "带测试行旧表", withRow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			legacyDB, err := gorm.Open(sqlite.Open("file:"+filepath.Join(dir, "brain.db")), &gorm.Config{})
			if err != nil {
				t.Fatalf("打开旧库: %v", err)
			}
			if err := legacyDB.Exec(`CREATE TABLE paired_hands (
				hand_id text PRIMARY KEY,
				token_hash text NOT NULL,
				origin text NOT NULL,
				label text,
				created_at datetime,
				last_seen_at datetime
			)`).Error; err != nil {
				t.Fatalf("创建旧配对表: %v", err)
			}
			// 相似前缀表是破坏范围哨兵：迁移只能命中精确旧表名。
			if err := legacyDB.Exec(`CREATE TABLE paired_hands_archive (
				hand_id text PRIMARY KEY
			)`).Error; err != nil {
				t.Fatalf("创建相似名哨兵表: %v", err)
			}
			if err := legacyDB.Exec(`INSERT INTO paired_hands_archive (hand_id) VALUES ('must-survive')`).Error; err != nil {
				t.Fatalf("写入相似名哨兵表: %v", err)
			}
			if tc.withRow {
				if err := legacyDB.Exec(`INSERT INTO paired_hands
					(hand_id, token_hash, origin, label)
					VALUES ('legacy-hand', 'legacy-token-hash', 'chrome-extension://legacy', '测试旧手')`).Error; err != nil {
					t.Fatalf("写入旧配对测试行: %v", err)
				}
			}
			legacySQL, err := legacyDB.DB()
			if err != nil {
				t.Fatal(err)
			}
			if err := legacySQL.Close(); err != nil {
				t.Fatalf("关闭旧库: %v", err)
			}

			s, err := Open(dir)
			if err != nil {
				t.Fatalf("升级打开旧库: %v", err)
			}
			defer s.Close()
			if s.db.Migrator().HasTable(retiredPairedHandsTable) {
				t.Fatal("升级后仍存在 paired_hands 旧凭据表")
			}
			var sentinelRows int64
			if !s.db.Migrator().HasTable("paired_hands_archive") ||
				s.db.Table("paired_hands_archive").Count(&sentinelRows).Error != nil || sentinelRows != 1 {
				t.Fatalf("精确删表迁移误伤相似名表: rows=%d", sentinelRows)
			}
			if !s.db.Migrator().HasTable(&Hand{}) {
				t.Fatal("升级后缺少现行 hands 表")
			}
		})
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
