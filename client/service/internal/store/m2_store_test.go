package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAccountIdentityUniqueAndRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	accounts := []*Account{
		{Platform: "zhilian", AccountRef: "acc-01"},
		{Platform: "zhilian", AccountRef: "acc-02"},
		{Platform: "another", AccountRef: "acc-01"},
	}
	for _, a := range accounts {
		if err := s.CreateAccount(a); err != nil {
			t.Fatalf("CreateAccount(%s/%s): %v", a.Platform, a.AccountRef, err)
		}
	}
	verifiedAt := time.Date(2026, 7, 17, 9, 0, 0, 0, time.Local)
	key := AccountKey{Platform: "zhilian", AccountRef: "acc-01"}
	if err := s.BindAccountPrincipal(key, "hand-01", "opaque-principal-a", "sess-1", "boot-1", verifiedAt); err != nil {
		t.Fatalf("BindAccountPrincipal: %v", err)
	}
	// 同一平台同一 principal 只能绑定一个脑账号。
	if err := s.BindAccountPrincipal(AccountKey{Platform: "zhilian", AccountRef: "acc-02"},
		"hand-02", "opaque-principal-a", "sess-2", "boot-2", verifiedAt); err == nil {
		t.Fatal("同平台重复 principal 应被唯一键拒绝")
	}
	acc2, _ := s.AccountByKey(AccountKey{Platform: "zhilian", AccountRef: "acc-02"})
	if acc2.IdentityState != IdentityUnbound || acc2.PrincipalFingerprint != nil {
		t.Fatalf("唯一键失败必须回滚 acc-02 绑定,得到 %+v", acc2)
	}
	// 平台维度进入唯一键:另一平台可存在字节相同的不透明指纹。
	if err := s.BindAccountPrincipal(AccountKey{Platform: "another", AccountRef: "acc-01"},
		"hand-03", "opaque-principal-a", "sess-3", "boot-3", verifiedAt); err != nil {
		t.Fatalf("跨平台相同 opaque 值不应冲突: %v", err)
	}

	enabledAt := verifiedAt.Add(time.Hour)
	if err := s.MutateAccount(key, func(a *Account) error {
		a.EnabledDate = "2026-07-17"
		a.EnabledAt = &enabledAt
		a.DirtyHint = true
		return nil
	}); err != nil {
		t.Fatalf("MutateAccount: %v", err)
	}
	if err := s.SetAccountIdentityState(key, IdentityUnobservable, "pageAbsent"); err != nil {
		t.Fatalf("SetAccountIdentityState: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重启 Open: %v", err)
	}
	defer s2.Close()
	got, err := s2.AccountByKey(key)
	if err != nil || got == nil {
		t.Fatalf("AccountByKey after restart: %v, %+v", err, got)
	}
	if got.IdentityState != IdentityUnobservable || got.PrincipalFingerprint == nil || *got.PrincipalFingerprint != "opaque-principal-a" {
		t.Fatalf("暂不可观测不能抹掉绑定指纹: %+v", got)
	}
	if got.EnabledDate != "2026-07-17" || got.EnabledAt == nil || !got.DirtyHint {
		t.Fatalf("账号 actor 状态未跨重启恢复: %+v", got)
	}
}

func TestBindAccountObservationCreateReuseAndConflict(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 17, 9, 30, 0, 0, time.UTC)
	first, created, err := s.BindAccountObservation(
		AccountKey{Platform: "zhilian", AccountRef: "account-generated-1"},
		"hand-1", "opaque-principal", "session-1", "boot-1", at, true,
	)
	if err != nil || !created || first.AccountRef != "account-generated-1" || first.IdentityState != IdentityVerified {
		t.Fatalf("首次绑定错误: account=%+v created=%v err=%v", first, created, err)
	}
	// UI 再次点“绑定当前账号”会生成新 accountRef，但必须复用旧根，
	// 并用新的会话/代际观测原子刷新身份健康。
	reused, created, err := s.BindAccountObservation(
		AccountKey{Platform: "zhilian", AccountRef: "account-generated-2"},
		"hand-2", "opaque-principal", "session-2", "boot-2", at.Add(time.Minute), true,
	)
	if err != nil || created || reused.AccountRef != first.AccountRef || reused.BoundHandID != "hand-2" ||
		reused.IdentitySession != "session-2" || reused.IdentityBootID != "boot-2" {
		t.Fatalf("主体复用错误: account=%+v created=%v err=%v", reused, created, err)
	}
	if rows, _ := s.Accounts(); len(rows) != 1 {
		t.Fatalf("复用主体时不得留下空壳账号: %+v", rows)
	}
	_, _, err = s.BindAccountObservation(
		AccountKey{Platform: "zhilian", AccountRef: "explicit-other"},
		"hand-3", "opaque-principal", "session-3", "boot-3", at.Add(2*time.Minute), false,
	)
	if !errors.Is(err, ErrPrincipalAlreadyBound) {
		t.Fatalf("显式 accountRef 不得偷偷吞并到另一账号: %v", err)
	}

	enabledAt := at.Add(3 * time.Minute)
	if err := s.MutateAccount(AccountKey{Platform: first.Platform, AccountRef: first.AccountRef}, func(account *Account) error {
		account.EnabledDate = "2026-07-17"
		account.EnabledAt = &enabledAt
		account.DirtyHint = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.BindAccountObservation(
		AccountKey{Platform: first.Platform, AccountRef: first.AccountRef},
		"hand-other", "opaque-other-principal", "session-other", "boot-other", at.Add(4*time.Minute), false,
	)
	if !errors.Is(err, ErrAccountPrincipalMismatch) {
		t.Fatalf("既有账号根不得覆盖为另一主体: %v", err)
	}
	afterMismatch, _ := s.AccountByKey(AccountKey{Platform: first.Platform, AccountRef: first.AccountRef})
	if afterMismatch.PrincipalFingerprint == nil || *afterMismatch.PrincipalFingerprint != "opaque-principal" ||
		afterMismatch.BoundHandID != "hand-2" || afterMismatch.IdentitySession != "session-2" ||
		afterMismatch.EnabledDate != "2026-07-17" || afterMismatch.EnabledAt == nil || !afterMismatch.DirtyHint {
		t.Fatalf("跨主体改绑失败必须完整保留旧根: %+v", afterMismatch)
	}
}

func TestApplyResultMessageRollsBackProcessedWitnessWithCommand(t *testing.T) {
	s := openTest(t)
	if err := s.CreateCmd(&CmdRecord{
		MsgID: "cmd-atomic-result", Name: "debug.ping", Class: "readonly", HandID: "hand-1",
		Status: CmdSent, LogicalDispatchID: "cmd-atomic-result",
	}); err != nil {
		t.Fatal(err)
	}
	forced := errors.New("forced command save failure")
	callbackName := "test:fail_atomic_result_command_save"
	if err := s.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CmdRecord" {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.ApplyResultMessage("cmd-atomic-result", "result-atomic", "result", "hand-1",
		func(command *CmdRecord) (ResultCommandMutation, error) {
			command.Status = CmdOk
			return ResultCommandMutation{Save: true}, nil
		})
	_ = s.db.Callback().Update().Remove(callbackName)
	if !errors.Is(err, forced) {
		t.Fatalf("应返回命令落库失败: %v", err)
	}
	command, _ := s.CmdByMsgID("cmd-atomic-result")
	var processed int64
	if countErr := s.db.Model(&ProcessedMsg{}).Where("msg_id = ?", "result-atomic").Count(&processed).Error; countErr != nil {
		t.Fatal(countErr)
	}
	if command.Status != CmdSent || processed != 0 {
		t.Fatalf("终局与 processed 证词必须同回滚: command=%+v processed=%d", command, processed)
	}
}

func TestCreateCmdDomainClaimAtomic(t *testing.T) {
	s := openTest(t)
	cmds := []*CmdRecord{
		{MsgID: "M-domain-1", Name: "chat.readList", Class: "intrusive", Domain: "acc-01", HandID: "h1", Status: CmdQueued},
		{MsgID: "M-domain-2", Name: "chat.readThread", Class: "intrusive", Domain: "acc-01", HandID: "h2", Status: CmdQueued},
	}
	var wg sync.WaitGroup
	errs := make([]error, len(cmds))
	for i := range cmds {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.CreateCmdIfDomainAvailable(cmds[i])
		}(i)
	}
	wg.Wait()
	var created, busy int
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrDomainBusy):
			busy++
		default:
			t.Fatalf("意外错误: %v", err)
		}
	}
	if created != 1 || busy != 1 {
		t.Fatalf("同账号跨手只能原子占用一条命令,created=%d busy=%d", created, busy)
	}
	rows, err := s.NonTerminalCmds()
	if err != nil || len(rows) != 1 {
		t.Fatalf("域占用后应恰有一条在途: err=%v n=%d", err, len(rows))
	}
}

func TestLogicalDispatchReplacementLeafAndRollback(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := &CmdRecord{
		MsgID: "M-root", Name: "chat.readThread", Class: "intrusive", Domain: "acc-01",
		Platform: "zhilian", AccountRef: "acc-01", ExpectedPrincipalFingerprint: "opaque-a",
		ContextJSON: `{"platform":"zhilian","accountRef":"acc-01","expectedPrincipalFingerprint":"opaque-a"}`,
		Args:        `{"conversationRef":"c-1"}`, HandID: "hand-01", Status: CmdAccepted, ExecBudgetMs: 30000,
	}
	if err := s.CreateCmd(root); err != nil {
		t.Fatalf("CreateCmd root: %v", err)
	}
	if root.LogicalDispatchID != root.MsgID {
		t.Fatalf("根命令 logical id 应默认等于 msgId: %+v", root)
	}
	if err := s.CreateCmd(&CmdRecord{MsgID: "M-other-root", LogicalDispatchID: root.LogicalDispatchID, Name: "debug.ping", Class: "readonly", HandID: "h", Status: CmdQueued}); !errors.Is(err, ErrLineageConflict) {
		t.Fatalf("同一 logicalDispatchID 不得创建第二个根,得到 %v", err)
	}
	child := &CmdRecord{MsgID: "M-child", Session: "s-2", BootIDAtDispatch: "b-2", DeadlineMs: time.Now().Add(time.Minute).UnixMilli()}
	if err := s.ReplaceCmd(root.MsgID, CmdVoid, "deadline", child); err != nil {
		t.Fatalf("ReplaceCmd: %v", err)
	}
	state, err := s.LogicalDispatch(root.MsgID)
	if err != nil {
		t.Fatalf("LogicalDispatch: %v", err)
	}
	if state.Settled || state.Length != 2 || state.Leaf.MsgID != child.MsgID || state.Leaf.Status != CmdQueued {
		t.Fatalf("中间 void 不得结束逻辑链: %+v", state)
	}
	if state.Leaf.Platform != "zhilian" || state.Leaf.AccountRef != "acc-01" ||
		state.Leaf.ContextJSON != root.ContextJSON || state.Leaf.ExpectedPrincipalFingerprint != "opaque-a" {
		t.Fatalf("替代命令必须完整继承 context: %+v", state.Leaf)
	}
	if state.Leaf.ParentMsgID == nil || *state.Leaf.ParentMsgID != root.MsgID || state.Leaf.LineageDepth != 1 {
		t.Fatalf("父链/深度错误: %+v", state.Leaf)
	}
	if err := s.MutateCmd(child.MsgID, func(c *CmdRecord) error { c.Status = CmdOk; return nil }); err != nil {
		t.Fatalf("child terminalize: %v", err)
	}
	state, _ = s.LogicalDispatch(root.MsgID)
	if !state.Settled || state.Leaf.MsgID != child.MsgID {
		t.Fatalf("只允许最终叶终局完成逻辑链: %+v", state)
	}

	// 替代命令创建失败时,父命令终局与 replacement 指针必须一起回滚。
	root2 := &CmdRecord{MsgID: "M-root-2", Name: "chat.readList", Class: "intrusive", HandID: "h", Status: CmdAccepted}
	if err := s.CreateCmd(root2); err != nil {
		t.Fatal(err)
	}
	conflicting := &CmdRecord{MsgID: "M-conflicting-child", Name: root2.Name, ContextJSON: `{"platform":"wrong"}`}
	if err := s.ReplaceCmd(root2.MsgID, CmdVoid, "must rollback", conflicting); !errors.Is(err, ErrLineageConflict) {
		t.Fatalf("替代链不得改写业务 context,得到 %v", err)
	}
	parentAfter, _ := s.CmdByMsgID(root2.MsgID)
	if parentAfter.Status != CmdAccepted || parentAfter.ReplacementMsgID != nil || parentAfter.TerminalAt != nil {
		t.Fatalf("context 冲突后父命令必须原样回滚: %+v", parentAfter)
	}
	duplicate := &CmdRecord{MsgID: child.MsgID}
	if err := s.ReplaceCmd(root2.MsgID, CmdVoid, "forced failure", duplicate); err == nil {
		t.Fatal("重复 child msgId 应使 ReplaceCmd 失败")
	}
	parentAfter, _ = s.CmdByMsgID(root2.MsgID)
	if parentAfter.Status != CmdAccepted || parentAfter.ReplacementMsgID != nil || parentAfter.TerminalAt != nil {
		t.Fatalf("ReplaceCmd 失败后父命令必须原样回滚: %+v", parentAfter)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	recovered, err := s2.LogicalDispatch(root.MsgID)
	if err != nil || !recovered.Settled || recovered.Leaf.MsgID != child.MsgID {
		t.Fatalf("逻辑链未跨重启恢复: state=%+v err=%v", recovered, err)
	}
}

func TestMigrateBackfillsLegacyLogicalDispatchID(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &CmdRecord{MsgID: "M-legacy", Name: "debug.ping", Class: "readonly", HandID: "h", Status: CmdOk}
	// 绕过 CreateCmd 模拟 M1 升级前没有 logical_dispatch_id 的旧行。
	if err := s.db.Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.CmdByMsgID("M-legacy")
	if err != nil || got.LogicalDispatchID != "M-legacy" {
		t.Fatalf("旧命令 logical id 回填失败: %+v err=%v", got, err)
	}
}

func TestMigrateActualM1CommandTable(t *testing.T) {
	dir := t.TempDir()
	legacyDB, err := gorm.Open(sqlite.Open("file:"+filepath.Join(dir, "brain.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开旧库: %v", err)
	}
	// 直接建 M1 形状的表:它根本没有 M2 的 context/lineage 列,不是在新表里写空值伪装升级。
	if err := legacyDB.Exec(`CREATE TABLE cmd_records (
		msg_id text PRIMARY KEY, name text NOT NULL, class text NOT NULL, idem_key text,
		domain text, args text, hand_id text NOT NULL, session text, boot_id_at_dispatch text,
		status text NOT NULL, attempt integer, redispatch_n integer, sent_at datetime,
		deadline_ms integer, exec_budget_ms integer, error_code text, side_effect text,
		result_body text, suspect_reason text, created_at datetime, updated_at datetime, terminal_at datetime
	)`).Error; err != nil {
		t.Fatalf("建 M1 cmd_records: %v", err)
	}
	if err := legacyDB.Exec(`INSERT INTO cmd_records
		(msg_id,name,class,hand_id,status,created_at,updated_at)
		VALUES ('M-old-1','debug.ping','readonly','h','ok',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
		       ('M-old-2','debug.ping','readonly','h','failed',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatalf("写 M1 行: %v", err)
	}
	legacySQL, err := legacyDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := legacySQL.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("M1 实表升级失败: %v", err)
	}
	defer s.Close()
	for _, msgID := range []string{"M-old-1", "M-old-2"} {
		got, err := s.CmdByMsgID(msgID)
		if err != nil || got == nil || got.LogicalDispatchID != msgID || got.LineageDepth != 0 || got.ParentMsgID != nil {
			t.Fatalf("旧行 %s 没有迁移成独立逻辑根: %+v err=%v", msgID, got, err)
		}
		logical, err := s.LogicalDispatch(msgID)
		if err != nil || !logical.Settled || logical.Length != 1 || logical.Leaf.MsgID != msgID {
			t.Fatalf("旧行 %s 升级后逻辑查询不可恢复: %+v err=%v", msgID, logical, err)
		}
	}
}

func TestConversationListAtomicIdentityAndPlatformKeys(t *testing.T) {
	s := openTest(t)
	createAccountAndRound(t, s, "zhilian", "acc-01", "round-list")
	createAccountAndRound(t, s, "other", "acc-01", "round-list")
	activity := int64(1234)
	req := SaveConversationListRequest{
		Platform: "zhilian", AccountRef: "acc-01", RoundID: "round-list", ObservedAt: time.Now(), Complete: true,
		Entries: []ListIndexEntry{{
			ConversationRef: "conv-1", PlatformUserRef: "user-1", PeerDisplayName: "候选人甲",
			UnreadCount: 2, LastMessageDirection: "in", LastMessageKind: "text",
			LastMessagePreview: "你好", LastActivityMs: &activity,
		}},
	}
	if err := s.SaveConversationList(req); err != nil {
		t.Fatalf("SaveConversationList: %v", err)
	}
	// 同一身份重复列表只更新,不新增重复索引。
	req.Entries[0].UnreadCount = 0
	if err := s.SaveConversationList(req); err != nil {
		t.Fatalf("重复 SaveConversationList: %v", err)
	}
	rows, _ := s.ConversationsForAccount(AccountKey{Platform: "zhilian", AccountRef: "acc-01"})
	if len(rows) != 1 || rows[0].UnreadCount != 0 || rows[0].PlatformUserRef != "user-1" {
		t.Fatalf("列表索引幂等更新失败: %+v", rows)
	}
	round, _ := s.PatrolRoundByKey("zhilian", "acc-01", "round-list")
	if round.ListComplete == nil || !*round.ListComplete {
		t.Fatalf("列表 complete 未原子落到 round: %+v", round)
	}

	// 同一 conversationRef 在不同平台账号下是不同正式键。
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform: "other", AccountRef: "acc-01", RoundID: "round-list", Complete: true,
		Entries: []ListIndexEntry{{ConversationRef: "conv-1", PlatformUserRef: "user-1"}},
	}); err != nil {
		t.Fatalf("跨平台相同 conversationRef 应允许: %v", err)
	}

	// 批量中后项身份冲突时,前面新建的 conv-2 也必须回滚。
	err := s.SaveConversationList(SaveConversationListRequest{
		Platform: "zhilian", AccountRef: "acc-01", RoundID: "round-list", Complete: false,
		Entries: []ListIndexEntry{
			{ConversationRef: "conv-2", PlatformUserRef: "user-2"},
			{ConversationRef: "conv-1", PlatformUserRef: "DIFFERENT"},
		},
	})
	if !errors.Is(err, ErrPeerIdentityConflict) {
		t.Fatalf("期望 peer identity 冲突,得到 %v", err)
	}
	conv2, _ := s.ConversationByKey(ConversationKey{Platform: "zhilian", AccountRef: "acc-01", ConversationRef: "conv-2"})
	if conv2 != nil {
		t.Fatalf("批量失败必须回滚前项,却留下 %+v", conv2)
	}
}

func TestTrackedAdoptionAndSnapshotApplyAtomic(t *testing.T) {
	s := openTest(t)
	createAccountAndRound(t, s, "zhilian", "acc-01", "round-1")
	key := ConversationKey{Platform: "zhilian", AccountRef: "acc-01", ConversationRef: "conv-1"}
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-1", Complete: true,
		Entries: []ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "user-1", PeerDisplayName: "候选人甲"}},
	}); err != nil {
		t.Fatal(err)
	}
	intent, err := s.TrackConversation(key, "user", time.Now())
	if err != nil || intent.Status != TrackingPending {
		t.Fatalf("TrackConversation: %+v %v", intent, err)
	}
	intent2, err := s.TrackConversation(key, "user", time.Now().Add(time.Hour))
	if err != nil || !intent2.RequestedAt.Equal(intent.RequestedAt) {
		t.Fatalf("重复 track 应幂等且不重置意图: %+v err=%v", intent2, err)
	}

	t1, t2, t3 := "历史文本", "邀面卡", "新增回复"
	adopted, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, RoundID: "round-1", ExpectedTailSeq: 0, PlatformUserRef: "user-1", Adopt: true,
		NewMessages: []MessageDraft{
			{Direction: "in", Kind: "text", ContentHash: "h1", Text: &t1, Origin: "external"},
			{Direction: "out", Kind: "card", ContentHash: "card-id-1", Text: &t2, CardType: "interviewInvite", CardState: "pending", Origin: "external"},
		},
	})
	if err != nil {
		t.Fatalf("首次收编: %v", err)
	}
	if adopted.TailSeq != 2 || adopted.AdoptedBoundarySeq != 2 || len(adopted.Inserted) != 2 {
		t.Fatalf("首次收编结果错误: %+v", adopted)
	}
	conv, _ := s.ConversationByKey(key)
	tracked, _ := s.TrackedIntentByConversation(key)
	if conv.TrackingState != TrackingAdopted || conv.AdoptedBoundarySeq != 2 || tracked.Status != TrackingAdopted || tracked.AdoptedAt == nil {
		t.Fatalf("tracked/adoptedBoundary 未原子推进: conv=%+v intent=%+v", conv, tracked)
	}
	audits, _ := s.AuditEntries(20)
	if len(audits) == 0 || audits[0].Category != "conversation_adopted" || audits[0].Platform != "zhilian" {
		t.Fatalf("收编审计缺失: %+v", audits)
	}
	messages, _ := s.MessagesForConversation(key)
	if len(messages) != 2 || messages[0].FirstSeenRoundID != "round-1" || messages[1].FirstSeenRoundID != "round-1" {
		t.Fatalf("firstSeenRound 未与首次观测轮绑定: %+v", messages)
	}
	round, _ := s.PatrolRoundByKey("zhilian", "acc-01", "round-1")
	if round.NewMessageCount != 0 {
		t.Fatalf("首次收编的边界前历史不得计入本轮新增,得到 %d", round.NewMessageCount)
	}
	if _, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{Key: key, ExpectedTailSeq: 2, Adopt: true}); !errors.Is(err, ErrConversationAlreadyAdopted) {
		t.Fatalf("重复收编应拒绝,得到 %v", err)
	}

	next, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, RoundID: "round-1", ExpectedTailSeq: 2,
		NewMessages: []MessageDraft{{Direction: "in", Kind: "text", ContentHash: "h3", Text: &t3, Origin: "external"}},
		CardChanges: []CardStateChange{{Seq: 2, ContentHash: "card-id-1", FromState: "pending", CardState: "accepted"}},
	})
	if err != nil || next.TailSeq != 3 || next.AdoptedBoundarySeq != 2 {
		t.Fatalf("新增投影/卡片跃迁: %+v err=%v", next, err)
	}
	card, _ := s.MessageBySeq(key, 2)
	if card.CardState != "accepted" {
		t.Fatalf("卡片状态未更新: %+v", card)
	}

	// 先插新消息、后遇到错误卡片 hash:整个事务必须回滚,不能留下 seq=4。
	_, err = s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, RoundID: "round-1", ExpectedTailSeq: 3,
		NewMessages: []MessageDraft{{Direction: "in", Kind: "text", ContentHash: "h4", Text: &t3, Origin: "external"}},
		CardChanges: []CardStateChange{{Seq: 2, ContentHash: "wrong-card-id", FromState: "accepted", CardState: "rejected"}},
	})
	if !errors.Is(err, ErrConversationVersionConflict) {
		t.Fatalf("期望卡片身份冲突,得到 %v", err)
	}
	messages, _ = s.MessagesForConversation(key)
	conv, _ = s.ConversationByKey(key)
	if len(messages) != 3 || conv.LastMessageSeq != 3 {
		t.Fatalf("快照事务失败后发生部分写: messages=%d conv=%+v", len(messages), conv)
	}
	if card, _ = s.MessageBySeq(key, 2); card.CardState != "accepted" {
		t.Fatalf("失败事务覆盖了既有 cardState: %+v", card)
	}
	round, _ = s.PatrolRoundByKey("zhilian", "acc-01", "round-1")
	if round.NewMessageCount != 1 {
		t.Fatalf("round 新消息计数应仅含已提交消息,得到 %d", round.NewMessageCount)
	}
	if messages[2].FirstSeenRoundID != "round-1" {
		t.Fatalf("本轮新增消息缺 firstSeenRound: %+v", messages[2])
	}
	recent, err := s.RecentMessagesForConversation(key, 2)
	if err != nil || len(recent) != 2 || recent[0].Seq != 2 || recent[1].Seq != 3 {
		t.Fatalf("最近消息应截取尾部且仍按 seq 正序: %+v err=%v", recent, err)
	}
	if _, err := s.RecentMessagesForConversation(key, 0); err == nil {
		t.Fatal("非法 limit 必须响亮失败")
	}
}

func TestCardTransitionFactSurvivesRestartAndAcknowledgesOnce(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := ConversationKey{Platform: "zhilian", AccountRef: "acc-card-outbox", ConversationRef: "conv-card-outbox"}
	const roundID = "round-card-outbox"
	seedAdoptedPendingCard(t, s, key, roundID, "card-outbox-hash")

	transitionAt := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	apply := ApplyConversationChangesRequest{
		Key: key, RoundID: roundID, ExpectedTailSeq: 1, SyncedAt: transitionAt,
		CardChanges: []CardStateChange{{
			Seq: 1, ContentHash: "card-outbox-hash", FromState: "pending", CardState: "accepted",
		}},
	}
	if _, err := s.ApplyConversationChanges(apply); err != nil {
		t.Fatalf("提交卡片跃迁: %v", err)
	}
	// 相同目标状态的计划重放是幂等成功，不追加第二条事实。
	if _, err := s.ApplyConversationChanges(apply); err != nil {
		t.Fatalf("重放已提交跃迁: %v", err)
	}
	stale := apply
	stale.CardChanges = []CardStateChange{{
		Seq: 1, ContentHash: "card-outbox-hash", FromState: "pending", CardState: "rejected",
	}}
	if _, err := s.ApplyConversationChanges(stale); !errors.Is(err, ErrConversationVersionConflict) {
		t.Fatalf("过时计划不得把已 accepted 卡改为 rejected: %v", err)
	}
	pending, err := s.PendingCardTransitions(10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("应只有一条 pending 事实: %+v err=%v", pending, err)
	}
	fact := pending[0]
	if fact.Platform != key.Platform || fact.AccountRef != key.AccountRef || fact.ConversationRef != key.ConversationRef ||
		fact.MessageSeq != 1 || fact.RoundID != roundID || fact.ContentHash != "card-outbox-hash" ||
		fact.CardType != "interviewInvite" || fact.FromState != "pending" || fact.ToState != "accepted" ||
		!fact.CreatedAt.Equal(transitionAt) || fact.AcknowledgedAt != nil {
		t.Fatalf("跃迁事实不完整: %+v", fact)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatalf("重启 Open: %v", err)
	}
	defer s.Close()
	for read := 0; read < 2; read++ {
		pending, err = s.PendingCardTransitions(10)
		if err != nil || len(pending) != 1 || pending[0].Key() != fact.Key() {
			t.Fatalf("重启后读取 %d 不得隐式消费: %+v err=%v", read+1, pending, err)
		}
	}
	ackAt := transitionAt.Add(time.Minute)
	acknowledged, err := s.AcknowledgeCardTransition(fact.Key(), ackAt)
	if err != nil || !acknowledged {
		t.Fatalf("首次显式确认: acknowledged=%t err=%v", acknowledged, err)
	}
	acknowledged, err = s.AcknowledgeCardTransition(fact.Key(), ackAt.Add(time.Minute))
	if err != nil || acknowledged {
		t.Fatalf("重复确认必须幂等: acknowledged=%t err=%v", acknowledged, err)
	}
	pending, err = s.PendingCardTransitions(10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("确认后不得继续列为 pending: %+v err=%v", pending, err)
	}
	retained, err := s.CardTransitionByKey(fact.Key())
	if err != nil || retained == nil || retained.AcknowledgedAt == nil || !retained.AcknowledgedAt.Equal(ackAt) {
		t.Fatalf("确认必须保留追加事实与首次确认时间: %+v err=%v", retained, err)
	}
}

func TestCardTransitionFactAutoMigrateSchema(t *testing.T) {
	s := openTest(t)
	type tableColumn struct {
		Name string
		PK   int `gorm:"column:pk"`
	}
	var columns []tableColumn
	if err := s.db.Raw("PRAGMA table_info('card_transition_facts')").Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	wantColumns := map[string]bool{
		"platform": false, "account_ref": false, "conversation_ref": false, "message_seq": false,
		"from_state": false, "to_state": false, "round_id": false, "content_hash": false,
		"card_type": false, "created_at": false, "acknowledged_at": false,
	}
	primaryKeyColumns := map[string]bool{
		"platform": false, "account_ref": false, "conversation_ref": false,
		"message_seq": false, "from_state": false, "to_state": false,
	}
	for _, column := range columns {
		if _, ok := wantColumns[column.Name]; ok {
			wantColumns[column.Name] = true
		}
		if _, ok := primaryKeyColumns[column.Name]; ok && column.PK > 0 {
			primaryKeyColumns[column.Name] = true
		}
	}
	for name, present := range wantColumns {
		if !present {
			t.Fatalf("AutoMigrate 缺少 card transition 列 %s: %+v", name, columns)
		}
	}
	for name, inPrimaryKey := range primaryKeyColumns {
		if !inPrimaryKey {
			t.Fatalf("复合主键缺少 %s: %+v", name, columns)
		}
	}
	type tableIndex struct {
		Name string
	}
	var indexes []tableIndex
	if err := s.db.Raw("PRAGMA index_list('card_transition_facts')").Scan(&indexes).Error; err != nil {
		t.Fatal(err)
	}
	foundPendingIndex := false
	for _, index := range indexes {
		if index.Name == "idx_card_transition_pending" {
			foundPendingIndex = true
			break
		}
	}
	if !foundPendingIndex {
		t.Fatalf("AutoMigrate 缺少 pending 扫描索引: %+v", indexes)
	}
}

func TestCardTransitionFactWriteFailureRollsBackWholeApply(t *testing.T) {
	s := openTest(t)
	key := ConversationKey{Platform: "zhilian", AccountRef: "acc-card-rollback", ConversationRef: "conv-card-rollback"}
	const roundID = "round-card-rollback"
	seedAdoptedPendingCard(t, s, key, roundID, "card-rollback-hash")

	forced := errors.New("forced card transition fact failure")
	callbackName := "test:fail_card_transition_fact"
	if err := s.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CardTransitionFact" {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Create().Remove(callbackName)

	newText := "这条也必须回滚"
	_, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, RoundID: roundID, ExpectedTailSeq: 1,
		NewMessages: []MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: "new-rollback-hash", Text: &newText, Origin: "external",
		}},
		CardChanges: []CardStateChange{{
			Seq: 1, ContentHash: "card-rollback-hash", FromState: "pending", CardState: "accepted",
		}},
	})
	if !errors.Is(err, forced) {
		t.Fatalf("应向上返回事实写入失败: %v", err)
	}
	messages, _ := s.MessagesForConversation(key)
	conversation, _ := s.ConversationByKey(key)
	round, _ := s.PatrolRoundByKey(key.Platform, key.AccountRef, roundID)
	pending, pendingErr := s.PendingCardTransitions(10)
	if pendingErr != nil {
		t.Fatal(pendingErr)
	}
	if len(messages) != 1 || messages[0].CardState != "pending" || conversation.LastMessageSeq != 1 ||
		round.NewMessageCount != 0 || len(pending) != 0 {
		t.Fatalf("事实写入失败必须回滚卡状态/新消息/尾/计数: messages=%+v conv=%+v round=%+v pending=%+v",
			messages, conversation, round, pending)
	}
}

func TestAdoptionRequiresPeerAnchorAndRollsBack(t *testing.T) {
	s := openTest(t)
	createAccountAndRound(t, s, "zhilian", "acc-01", "round-peer")
	key := ConversationKey{Platform: "zhilian", AccountRef: "acc-01", ConversationRef: "conv-no-peer"}
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-peer",
		Entries: []ListIndexEntry{{ConversationRef: key.ConversationRef, PeerDisplayName: "待确认候选人"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrackConversation(key, "user", time.Now()); err != nil {
		t.Fatal(err)
	}
	text := "历史消息"
	_, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, RoundID: "round-peer", ExpectedTailSeq: 0, Adopt: true,
		NewMessages: []MessageDraft{{Direction: "in", Kind: "text", ContentHash: "h-no-peer", Text: &text, Origin: "external"}},
	})
	if !errors.Is(err, ErrPeerIdentityRequired) {
		t.Fatalf("没有 platformUserRef 不得收编,得到 %v", err)
	}
	messages, _ := s.MessagesForConversation(key)
	conv, _ := s.ConversationByKey(key)
	intent, _ := s.TrackedIntentByConversation(key)
	if len(messages) != 0 || conv.LastMessageSeq != 0 || conv.TrackingState != TrackingPending || intent.Status != TrackingPending {
		t.Fatalf("候选人锚缺失必须整体回滚: messages=%+v conv=%+v intent=%+v", messages, conv, intent)
	}
}

func TestRebuildConversationBaselineDoesNotCountOrReadopt(t *testing.T) {
	s := openTest(t)
	key := ConversationKey{Platform: "zhilian", AccountRef: "acc-01", ConversationRef: "conv-baseline"}
	createAccountAndRound(t, s, key.Platform, key.AccountRef, "round-baseline")
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-baseline",
		Entries: []ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "user-baseline"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrackConversation(key, "user", time.Now()); err != nil {
		t.Fatal(err)
	}
	premature := "must-not-write"
	if _, err := s.RebuildConversationBaseline(RebuildConversationBaselineRequest{
		Key: key, RoundID: "round-baseline", ExpectedTailSeq: 0,
		Historical: []MessageDraft{{Direction: "in", Kind: "text", ContentHash: "premature", Text: &premature, Origin: "external"}},
	}); !errors.Is(err, ErrConversationNotTracked) {
		t.Fatalf("pending 会话不得把基线重建当成首次 adopt,得到 %v", err)
	}
	pendingConv, _ := s.ConversationByKey(key)
	pendingIntent, _ := s.TrackedIntentByConversation(key)
	if pendingConv.TrackingState != TrackingPending || pendingIntent.Status != TrackingPending || pendingConv.LastMessageSeq != 0 {
		t.Fatalf("被拒的基线重建改写了 pending 状态: conv=%+v intent=%+v", pendingConv, pendingIntent)
	}
	history := "old-context"
	if _, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, RoundID: "round-baseline", ExpectedTailSeq: 0, Adopt: true,
		NewMessages: []MessageDraft{{Direction: "in", Kind: "text", ContentHash: "old-hash", Text: &history, Origin: "external"}},
	}); err != nil {
		t.Fatal(err)
	}

	x, y := "deep-x", "deep-y"
	result, err := s.RebuildConversationBaseline(RebuildConversationBaselineRequest{
		Key: key, RoundID: "round-baseline", ExpectedTailSeq: 1,
		Historical: []MessageDraft{
			{Direction: "in", Kind: "text", ContentHash: "deep-x-hash", Text: &x, Origin: "external"},
			{Direction: "out", Kind: "text", ContentHash: "deep-y-hash", Text: &y, Origin: "external"},
		},
		AuditDetail: "reachedTop=true anchorMatched=false",
	})
	if err != nil {
		t.Fatalf("RebuildConversationBaseline: %v", err)
	}
	if result.TailSeq != 3 || result.HistoricalFromSeq != 2 || result.HistoricalThroughSeq != 3 || result.AdoptedBoundarySeq != 1 {
		t.Fatalf("历史基线结果错误: %+v", result)
	}
	messages, _ := s.MessagesForConversation(key)
	conv, _ := s.ConversationByKey(key)
	intent, _ := s.TrackedIntentByConversation(key)
	round, _ := s.PatrolRoundByKey(key.Platform, key.AccountRef, "round-baseline")
	if len(messages) != 3 || messages[1].FirstSeenRoundID != "round-baseline" || messages[2].FirstSeenRoundID != "round-baseline" {
		t.Fatalf("历史基线消息未完整落库: %+v", messages)
	}
	if conv.TrackingState != TrackingAdopted || intent.Status != TrackingAdopted || conv.AdoptedBoundarySeq != 1 || conv.LastMessageSeq != 3 {
		t.Fatalf("历史基线不得重用首次 adopt 状态机: conv=%+v intent=%+v", conv, intent)
	}
	if round.NewMessageCount != 0 {
		t.Fatalf("历史基线不得计入本轮新增,得到 %d", round.NewMessageCount)
	}
	audits, _ := s.AuditEntries(20)
	if len(audits) == 0 || audits[0].Category != "conversation_zero_overlap_rebaseline" || audits[0].RoundID != "round-baseline" {
		t.Fatalf("历史基线强制审计缺失: %+v", audits)
	}
}

func TestRebuildConversationBaselineLateAuditFailureRollsBackAll(t *testing.T) {
	s := openTest(t)
	key := ConversationKey{Platform: "zhilian", AccountRef: "acc-01", ConversationRef: "conv-baseline-rollback"}
	createAccountAndRound(t, s, key.Platform, key.AccountRef, "round-baseline-rollback")
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-baseline-rollback",
		Entries: []ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "user-baseline"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrackConversation(key, "user", time.Now()); err != nil {
		t.Fatal(err)
	}
	history := "old"
	if _, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, RoundID: "round-baseline-rollback", ExpectedTailSeq: 0, Adopt: true,
		NewMessages: []MessageDraft{{Direction: "in", Kind: "text", ContentHash: "old", Text: &history, Origin: "external"}},
	}); err != nil {
		t.Fatal(err)
	}

	forced := errors.New("forced rebaseline audit failure")
	callbackName := "test:fail_rebaseline_audit"
	if err := s.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "AuditEntry" {
			return
		}
		entry, ok := tx.Statement.Dest.(*AuditEntry)
		if ok && entry.Category == "conversation_zero_overlap_rebaseline" {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Create().Remove(callbackName)

	beforeMessages, _ := s.MessagesForConversation(key)
	beforeConv, _ := s.ConversationByKey(key)
	beforeRound, _ := s.PatrolRoundByKey(key.Platform, key.AccountRef, "round-baseline-rollback")
	beforeAudits, _ := s.AuditEntries(50)
	deep := "deep"
	_, err := s.RebuildConversationBaseline(RebuildConversationBaselineRequest{
		Key: key, RoundID: "round-baseline-rollback", ExpectedTailSeq: 1,
		Historical: []MessageDraft{{Direction: "in", Kind: "text", ContentHash: "deep", Text: &deep, Origin: "external"}},
	})
	if !errors.Is(err, forced) {
		t.Fatalf("应把审计失败返回调用方,得到 %v", err)
	}
	afterMessages, _ := s.MessagesForConversation(key)
	afterConv, _ := s.ConversationByKey(key)
	afterRound, _ := s.PatrolRoundByKey(key.Platform, key.AccountRef, "round-baseline-rollback")
	afterAudits, _ := s.AuditEntries(50)
	if len(afterMessages) != len(beforeMessages) || afterConv.LastMessageSeq != beforeConv.LastMessageSeq ||
		afterConv.LastSyncedRoundID != beforeConv.LastSyncedRoundID ||
		afterRound.NewMessageCount != beforeRound.NewMessageCount || len(afterAudits) != len(beforeAudits) {
		t.Fatalf("审计末步失败后事务未全回滚: messages %d->%d conv=%+v round=%+v audits %d->%d",
			len(beforeMessages), len(afterMessages), afterConv, afterRound, len(beforeAudits), len(afterAudits))
	}
}

func TestTrackedIntentIsFormalSource(t *testing.T) {
	s := openTest(t)
	createAccountAndRound(t, s, "zhilian", "acc-01", "round-track")
	key := ConversationKey{Platform: "zhilian", AccountRef: "acc-01", ConversationRef: "conv-track"}
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-track",
		Entries: []ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "user-track"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrackConversation(key, "user", time.Now()); err != nil {
		t.Fatal(err)
	}
	tracked, err := s.TrackedConversations(AccountKey{Platform: key.Platform, AccountRef: key.AccountRef})
	if err != nil || len(tracked) != 1 || tracked[0].ConversationRef != key.ConversationRef {
		t.Fatalf("正式 tracked 查询错误: %+v err=%v", tracked, err)
	}
	// 模拟非事务手改/旧版损坏:Conversation 投影与正式 intent 不一致。
	if err := s.db.Model(&Conversation{}).Where(conversationWhere(key), conversationArgs(key)...).
		Update("tracking_state", TrackingUntracked).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrackConversation(key, "user", time.Now()); !errors.Is(err, ErrTrackingStateCorrupt) {
		t.Fatalf("损坏状态不得被幂等调用静默修复,得到 %v", err)
	}
	tracked, err = s.TrackedConversations(AccountKey{Platform: key.Platform, AccountRef: key.AccountRef})
	if err != nil || len(tracked) != 0 {
		t.Fatalf("投影与 intent 不一致时不得误当 tracked: %+v err=%v", tracked, err)
	}
}

func TestBeginPatrolRoundAtomicallyConsumesDirtyHint(t *testing.T) {
	s := openTest(t)
	key := AccountKey{Platform: "zhilian", AccountRef: "acc-begin"}
	if err := s.CreateAccount(&Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	originalNext := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	if err := s.MutateAccount(key, func(account *Account) error {
		account.DirtyHint = true
		account.NextPatrolAt = &originalNext
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePatrolRound(&PatrolRound{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "duplicate",
	}); err != nil {
		t.Fatal(err)
	}
	failedNext := originalNext.Add(time.Hour)
	if err := s.BeginPatrolRound(&PatrolRound{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "duplicate",
	}, failedNext); err == nil {
		t.Fatal("重复 round 创建失败时事务应回滚")
	}
	afterFailure, _ := s.AccountByKey(key)
	if !afterFailure.DirtyHint || afterFailure.NextPatrolAt == nil || !afterFailure.NextPatrolAt.Equal(originalNext) {
		t.Fatalf("创建失败丢了 dirty/next: %+v", afterFailure)
	}

	successNext := originalNext.Add(2 * time.Hour)
	if err := s.BeginPatrolRound(&PatrolRound{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "new-round",
	}, successNext); err != nil {
		t.Fatal(err)
	}
	afterSuccess, _ := s.AccountByKey(key)
	if afterSuccess.DirtyHint || afterSuccess.NextPatrolAt == nil || !afterSuccess.NextPatrolAt.Equal(successNext) {
		t.Fatalf("开轮未原子消费 dirty/设置 next: %+v", afterSuccess)
	}
}

func TestPatrolRoundRunningRecovery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAccount(&Account{Platform: "zhilian", AccountRef: "acc-01"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePatrolRound(&PatrolRound{Platform: "zhilian", AccountRef: "acc-01", RoundID: "round-running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	recoveredAt := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	count, err := s2.RecoverRunningPatrolRounds(recoveredAt)
	if err != nil || count != 1 {
		t.Fatalf("脑重启收束 running round 失败: count=%d err=%v", count, err)
	}
	recovered, _ := s2.PatrolRoundByKey("zhilian", "acc-01", "round-running")
	account, _ := s2.AccountByKey(AccountKey{Platform: "zhilian", AccountRef: "acc-01"})
	if recovered.Status != "failed" || recovered.Stage != "interrupted" || recovered.ErrorCode != "BRAIN_RESTART" ||
		recovered.FinishedAt == nil || !recovered.FinishedAt.Equal(recoveredAt) || !account.DirtyHint ||
		account.NextPatrolAt == nil || !account.NextPatrolAt.Equal(recoveredAt) {
		t.Fatalf("重启恢复未原子收束轮次并拉起下轮: round=%+v account=%+v", recovered, account)
	}
	running, _ := s2.RunningPatrolRounds()
	if len(running) != 0 {
		t.Fatalf("已完成 round 不应出现在恢复扫描: %+v", running)
	}
}

func createAccountAndRound(t *testing.T, s *Store, platform, accountRef, roundID string) {
	t.Helper()
	if err := s.CreateAccount(&Account{Platform: platform, AccountRef: accountRef}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := s.CreatePatrolRound(&PatrolRound{Platform: platform, AccountRef: accountRef, RoundID: roundID}); err != nil {
		t.Fatalf("CreatePatrolRound: %v", err)
	}
}

func seedAdoptedPendingCard(t *testing.T, s *Store, key ConversationKey, roundID, contentHash string) {
	t.Helper()
	createAccountAndRound(t, s, key.Platform, key.AccountRef, roundID)
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: roundID,
		Entries: []ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "user-card"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrackConversation(key, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	cardText := "邀面卡"
	if _, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, RoundID: roundID, ExpectedTailSeq: 0, Adopt: true,
		NewMessages: []MessageDraft{{
			Direction: "out", Kind: "card", ContentHash: contentHash, Text: &cardText,
			CardType: "interviewInvite", CardState: "pending", Origin: "external",
		}},
	}); err != nil {
		t.Fatalf("收编 pending 卡片: %v", err)
	}
}
