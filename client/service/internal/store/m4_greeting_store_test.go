package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

type greetingLedgerFixture struct {
	ProfileID  string
	Platform   string
	AccountRef string
	HandID     string
	Session    string
	BootID     string
	Principal  string
}

func seedGreetingLedger(t *testing.T, s *Store, profileID string) greetingLedgerFixture {
	t.Helper()
	fixture := greetingLedgerFixture{
		ProfileID: profileID, Platform: "zhilian", AccountRef: "account-" + profileID,
		HandID: "hand-" + profileID, Session: "session-" + profileID,
		BootID: "boot-" + profileID, Principal: "principal-" + profileID,
	}
	createM4Account(t, s, fixture.Platform, fixture.AccountRef)
	if err := s.BindAccountPrincipal(
		AccountKey{Platform: fixture.Platform, AccountRef: fixture.AccountRef},
		fixture.HandID, fixture.Principal, fixture.Session, fixture.BootID, time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectCandidateProfile(candidateSelection(
		profileID, fixture.Platform, fixture.AccountRef, "person-"+profileID, "position-"+profileID,
		"候选人", "职位", time.Now(),
	)); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func greetingIntentRequest(
	fixture greetingLedgerFixture, intentID, previousIntentID string, now time.Time,
) CreateGreetingEffectIntentRequest {
	msgID := "msg-" + intentID
	idemKey := "ik1:" + fixture.Platform + ":" + fixture.AccountRef + ":chat.sendGreeting:" + fixture.ProfileID + ":" + intentID
	deadline := now.Add(time.Hour).UnixMilli()
	return CreateGreetingEffectIntentRequest{
		Intent: EffectIntent{
			IntentID: intentID, IdemKey: idemKey, Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			Primitive: primitiveChatSendGreeting, TargetRef: fixture.ProfileID,
			PayloadHash: "payload-" + intentID, GuardsHash: "guards-" + intentID,
			Status: EffectIntentDispatching, DeadlineMs: deadline, SendFingerprint: "fingerprint-" + intentID,
		},
		Command: CmdRecord{
			MsgID: msgID, Name: primitiveChatSendGreeting, Class: "effectful", IdemKey: idemKey,
			Domain:   fixture.Platform + ":" + fixture.AccountRef,
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ExpectedPrincipalFingerprint: fixture.Principal, IntentID: intentID,
			HandID: fixture.HandID, Session: fixture.Session, BootIDAtDispatch: fixture.BootID,
			Status: CmdQueued, DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		PreviousIntentID: previousIntentID,
		Now:              now,
	}
}

func settleGreetingFailed(t *testing.T, s *Store, intentID string, at time.Time) {
	t.Helper()
	if err := s.db.Model(&CmdRecord{}).Where("intent_id = ?", intentID).
		Updates(map[string]any{"status": CmdFailed, "terminal_at": at}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&EffectIntent{}).Where("intent_id = ?", intentID).
		Updates(map[string]any{"status": EffectIntentFailed, "resolved_at": at}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestCandidateGreetingHeadSchemaHasRestrictedProfileForeignKey(t *testing.T) {
	s := openTest(t)
	type tableColumn struct {
		Name string
		PK   int `gorm:"column:pk"`
	}
	var columns []tableColumn
	if err := s.db.Raw("PRAGMA table_info('candidate_greeting_heads')").Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	profilePrimary := false
	for _, column := range columns {
		seen[column.Name] = true
		if column.Name == "profile_id" && column.PK > 0 {
			profilePrimary = true
		}
	}
	if !profilePrimary || !seen["latest_intent_id"] || !seen["generation"] || seen["deleted_at"] {
		t.Fatalf("招呼 head schema 不完整或引入软删除: %+v", columns)
	}
	var resultConversationColumn int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_table_info('effect_intents') WHERE name = 'result_conversation_ref'`).
		Scan(&resultConversationColumn).Error; err != nil || resultConversationColumn != 1 {
		t.Fatalf("EffectIntent 缺少 nullable 结果会话: count=%d err=%v", resultConversationColumn, err)
	}
	type foreignKeyRow struct {
		Table    string `gorm:"column:table"`
		From     string `gorm:"column:from"`
		To       string `gorm:"column:to"`
		OnUpdate string `gorm:"column:on_update"`
		OnDelete string `gorm:"column:on_delete"`
	}
	var foreignKeys []foreignKeyRow
	if err := s.db.Raw("PRAGMA foreign_key_list('candidate_greeting_heads')").Scan(&foreignKeys).Error; err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range foreignKeys {
		if row.Table == "candidate_profiles" && row.From == "profile_id" && row.To == "profile_id" {
			found = true
			if row.OnDelete != "RESTRICT" || row.OnUpdate != "RESTRICT" {
				t.Fatalf("head 外键不得 cascade/set null: %+v", row)
			}
		}
	}
	if !found {
		t.Fatalf("head 缺少 Profile 外键: %+v", foreignKeys)
	}
}

func TestGreetingIntentExactRetryIsIdempotentAndRejectsMaterialSwap(t *testing.T) {
	s := openTest(t)
	fixture := seedGreetingLedger(t, s, "profile-idempotent")
	now := time.Date(2026, 7, 20, 14, 0, 0, 0, time.UTC)
	req := greetingIntentRequest(fixture, "intent-same", "", now)
	first, err := s.CreateGreetingEffectIntentAndCmd(req)
	if err != nil || !first.Created {
		t.Fatalf("首次招呼意图: result=%+v err=%v", first, err)
	}
	retried, err := s.CreateGreetingEffectIntentAndCmd(req)
	if err != nil || retried.Created || retried.Intent.IntentID != first.Intent.IntentID || retried.Command.MsgID != first.Command.MsgID {
		t.Fatalf("精确重试必须收编同一 intent/cmd: result=%+v err=%v", retried, err)
	}
	mutated := req
	mutated.Intent.PayloadHash = "swapped-payload"
	if _, err := s.CreateGreetingEffectIntentAndCmd(mutated); !errors.Is(err, ErrEffectIntentConflict) {
		t.Fatalf("同 intentId 偷换正文材料必须冲突: %v", err)
	}
	var intents, commands, heads int64
	_ = s.db.Model(&EffectIntent{}).Where("primitive = ?", primitiveChatSendGreeting).Count(&intents).Error
	_ = s.db.Model(&CmdRecord{}).Where("intent_id = ?", req.Intent.IntentID).Count(&commands).Error
	_ = s.db.Model(&CandidateGreetingHead{}).Count(&heads).Error
	if intents != 1 || commands != 1 || heads != 1 {
		t.Fatalf("精确重试发生账本增生: intents=%d commands=%d heads=%d", intents, commands, heads)
	}
	latest, err := s.LatestGreetingEffectIntent(fixture.ProfileID)
	if err != nil || latest == nil || latest.IntentID != req.Intent.IntentID {
		t.Fatalf("latest 未沿 head 返回: latest=%+v err=%v", latest, err)
	}
}

func TestGreetingHeadConcurrentCASHasSingleWinner(t *testing.T) {
	s := openTest(t)
	fixture := seedGreetingLedger(t, s, "profile-cas")
	now := time.Date(2026, 7, 20, 15, 0, 0, 0, time.UTC)
	requests := []CreateGreetingEffectIntentRequest{
		greetingIntentRequest(fixture, "intent-cas-a", "", now),
		greetingIntentRequest(fixture, "intent-cas-b", "", now),
	}
	start := make(chan struct{})
	results := make([]*CreateEffectIntentResult, len(requests))
	errs := make([]error, len(requests))
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = s.CreateGreetingEffectIntentAndCmd(requests[i])
		}(i)
	}
	close(start)
	wg.Wait()
	winners, conflicts := 0, 0
	winnerID := ""
	for i := range requests {
		if errs[i] == nil {
			winners++
			winnerID = results[i].Intent.IntentID
			continue
		}
		var conflict *CandidateGreetingCASConflictError
		if errors.As(errs[i], &conflict) && conflict.Current != nil {
			conflicts++
			continue
		}
		t.Fatalf("并发 CAS 返回意外错误[%d]: %v", i, errs[i])
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("同 predecessor 必须一胜一 CAS 冲突: winners=%d conflicts=%d errs=%+v", winners, conflicts, errs)
	}
	var head CandidateGreetingHead
	if err := s.db.First(&head, "profile_id = ?", fixture.ProfileID).Error; err != nil ||
		head.LatestIntentID != winnerID || head.Generation != 1 {
		t.Fatalf("head 未指向唯一赢家: head=%+v err=%v", head, err)
	}
	var intents, commands int64
	_ = s.db.Model(&EffectIntent{}).Where("primitive = ? AND target_ref = ?", primitiveChatSendGreeting, fixture.ProfileID).Count(&intents).Error
	_ = s.db.Model(&CmdRecord{}).Where("intent_id <> ?", "").Count(&commands).Error
	if intents != 1 || commands != 1 {
		t.Fatalf("CAS 败方必须整体回滚: intents=%d commands=%d", intents, commands)
	}
}

func TestGreetingHeadAdvancesOnlyAfterFailedIntent(t *testing.T) {
	s := openTest(t)
	fixture := seedGreetingLedger(t, s, "profile-generation")
	now := time.Date(2026, 7, 20, 16, 0, 0, 0, time.UTC)
	if _, err := s.CreateGreetingEffectIntentAndCmd(greetingIntentRequest(fixture, "intent-first", "", now)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGreetingEffectIntentAndCmd(greetingIntentRequest(fixture, "intent-too-early", "intent-first", now)); !errors.Is(err, ErrCandidateGreetingFrozen) {
		t.Fatalf("在途招呼必须冻结新意图: %v", err)
	}
	settleGreetingFailed(t, s, "intent-first", now.Add(time.Minute))
	second, err := s.CreateGreetingEffectIntentAndCmd(greetingIntentRequest(fixture, "intent-second", "intent-first", now.Add(2*time.Minute)))
	if err != nil || !second.Created {
		t.Fatalf("失败终局后真人可沿正确 predecessor 新铸意图: result=%+v err=%v", second, err)
	}
	if _, err := s.CreateGreetingEffectIntentAndCmd(greetingIntentRequest(fixture, "intent-stale", "intent-first", now.Add(2*time.Minute))); !errors.Is(err, ErrCandidateGreetingCASConflict) {
		t.Fatalf("旧 predecessor 必须被单调 head 拒绝: %v", err)
	}
	var head CandidateGreetingHead
	_ = s.db.First(&head, "profile_id = ?", fixture.ProfileID).Error
	if head.Generation != 2 || head.LatestIntentID != "intent-second" {
		t.Fatalf("head generation 未单调推进: %+v", head)
	}
	for _, rejectedID := range []string{"intent-too-early", "intent-stale"} {
		intent, _ := s.EffectIntentByID(rejectedID)
		cmd, _ := s.CmdByMsgID("msg-" + rejectedID)
		if intent != nil || cmd != nil {
			t.Fatalf("拒绝的新意图不得泄漏: id=%s intent=%+v cmd=%+v", rejectedID, intent, cmd)
		}
	}
}

func TestGreetingIntentRequiresUnboundSelectedProfile(t *testing.T) {
	s := openTest(t)
	fixture := seedGreetingLedger(t, s, "profile-ended")
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", fixture.ProfileID).
		Update("main_status", CandidateProfileEnded).Error; err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateGreetingEffectIntentAndCmd(greetingIntentRequest(fixture, "intent-ended", "", time.Now()))
	if !errors.Is(err, ErrCandidateProfileNotSelected) {
		t.Fatalf("非 selected 档案不得铸招呼意图: %v", err)
	}
	if intent, _ := s.EffectIntentByID("intent-ended"); intent != nil {
		t.Fatalf("状态拒绝不得留下 intent: %+v", intent)
	}
	var heads int64
	_ = s.db.Model(&CandidateGreetingHead{}).Count(&heads).Error
	if heads != 0 {
		t.Fatalf("状态拒绝不得留下 head: %d", heads)
	}
}

func TestGreetingIntentCommandFailureRollsBackIntentAndHead(t *testing.T) {
	s := openTest(t)
	fixture := seedGreetingLedger(t, s, "profile-rollback-head")
	forced := errors.New("forced greeting command create failure")
	callbackName := "test:fail_greeting_command_create"
	if err := s.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CmdRecord" {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Create().Remove(callbackName)

	_, err := s.CreateGreetingEffectIntentAndCmd(greetingIntentRequest(fixture, "intent-rollback", "", time.Now()))
	if !errors.Is(err, forced) {
		t.Fatalf("应返回注入的 command 写失败: %v", err)
	}
	intent, _ := s.EffectIntentByID("intent-rollback")
	cmd, _ := s.CmdByMsgID("msg-intent-rollback")
	var heads int64
	_ = s.db.Model(&CandidateGreetingHead{}).Count(&heads).Error
	if intent != nil || cmd != nil || heads != 0 {
		t.Fatalf("command 写失败必须回滚 intent/head: intent=%+v cmd=%+v heads=%d", intent, cmd, heads)
	}
}

func TestGreetingHeadCorruptionAndGenerationOverflowFailClosed(t *testing.T) {
	t.Run("head points to missing intent", func(t *testing.T) {
		s := openTest(t)
		fixture := seedGreetingLedger(t, s, "profile-corrupt-head")
		now := time.Now()
		if _, err := s.CreateGreetingEffectIntentAndCmd(greetingIntentRequest(fixture, "intent-corrupt-a", "", now)); err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&CandidateGreetingHead{}).Where("profile_id = ?", fixture.ProfileID).
			Update("latest_intent_id", "missing-intent").Error; err != nil {
			t.Fatal(err)
		}
		if latest, err := s.LatestGreetingEffectIntent(fixture.ProfileID); !errors.Is(err, ErrCandidateGreetingHeadCorrupt) || latest != nil {
			t.Fatalf("损坏 head 读取必须 fail-closed: latest=%+v err=%v", latest, err)
		}
		if _, err := s.CreateGreetingEffectIntentAndCmd(greetingIntentRequest(fixture, "intent-corrupt-b", "missing-intent", now)); !errors.Is(err, ErrCandidateGreetingHeadCorrupt) {
			t.Fatalf("损坏 head 不得创建后继: %v", err)
		}
	})

	t.Run("generation exhaustion", func(t *testing.T) {
		s := openTest(t)
		fixture := seedGreetingLedger(t, s, "profile-overflow-head")
		now := time.Now()
		if _, err := s.CreateGreetingEffectIntentAndCmd(greetingIntentRequest(fixture, "intent-overflow-a", "", now)); err != nil {
			t.Fatal(err)
		}
		settleGreetingFailed(t, s, "intent-overflow-a", now)
		if err := s.db.Model(&CandidateGreetingHead{}).Where("profile_id = ?", fixture.ProfileID).
			UpdateColumn("generation", int64(maxSQLiteEffectHeadGeneration)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateGreetingEffectIntentAndCmd(greetingIntentRequest(fixture, "intent-overflow-b", "intent-overflow-a", now)); !errors.Is(err, ErrCandidateGreetingHeadCorrupt) {
			t.Fatalf("generation 耗尽必须 fail-closed: %v", err)
		}
		if leaked, _ := s.EffectIntentByID("intent-overflow-b"); leaked != nil {
			t.Fatalf("generation 失败不得泄漏 intent: %+v", leaked)
		}
	})
}

func TestGreetingTargetDoesNotPolluteConversationEffectHeadNamespace(t *testing.T) {
	s := openTest(t)
	intent := EffectIntent{
		IntentID: "intent-target-collision", IdemKey: "idem-target-collision",
		Platform: "zhilian", AccountRef: "account-target-collision",
		Primitive: primitiveChatSendGreeting, TargetRef: "same-opaque-target",
		PayloadHash: "payload", GuardsHash: "guards", RootMsgID: "msg-target-collision",
		Status: EffectIntentFailed, DeadlineMs: time.Now().Add(time.Hour).UnixMilli(),
	}
	if err := s.db.Create(&intent).Error; err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestEffectIntent(intent.Platform, intent.AccountRef, intent.TargetRef)
	if err != nil || latest != nil {
		t.Fatalf("greeting ProfileID 不得冒充会话 orphan/head: latest=%+v err=%v", latest, err)
	}
}

func TestGreetingSuccessWriteFailureRollsBackWholeResultWAL(t *testing.T) {
	s := openTest(t)
	fixture := seedGreetingLedger(t, s, "profile-result-rollback")
	now := time.Date(2026, 7, 20, 20, 0, 0, 0, time.UTC)
	req := greetingIntentRequest(fixture, "intent-result-rollback", "", now)
	req.Intent.SendFingerprint = "fingerprint-result-rollback"
	created, err := s.CreateGreetingEffectIntentAndCmd(req)
	if err != nil {
		t.Fatal(err)
	}

	forced := errors.New("forced greeting message create failure")
	callbackName := "test:fail_greeting_result_message_create"
	if err := s.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Message" {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Create().Remove(callbackName)

	_, err = s.ApplyResultMessage(
		created.Command.MsgID,
		"result-result-rollback",
		"result",
		fixture.HandID,
		func(command *CmdRecord) (ResultCommandMutation, error) {
			terminalAt := now.Add(time.Minute)
			command.Status = CmdOk
			command.TerminalAt = &terminalAt
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk,
					Text:         "测试招呼",
					ContentHash:  req.Intent.SendFingerprint,
					Greeting: &GreetingResultMutation{
						PlatformUserRef: "person-" + fixture.ProfileID,
						PositionRef:     "position-" + fixture.ProfileID,
						ConversationRef: "conversation-result-rollback",
						Text:            "测试招呼",
						ContentHash:     req.Intent.SendFingerprint,
						ObservedAtMs:    terminalAt.UnixMilli(),
					},
				},
			}, nil
		},
	)
	if !errors.Is(err, forced) {
		t.Fatalf("应返回注入的消息写失败: %v", err)
	}

	cmd, _ := s.CmdByMsgID(created.Command.MsgID)
	intent, _ := s.EffectIntentByID(req.Intent.IntentID)
	profile, _ := s.CandidateProfileByID(fixture.ProfileID)
	if cmd == nil || cmd.Status != CmdQueued || intent == nil || intent.Status != EffectIntentDispatching ||
		intent.ResultConversationRef != nil || intent.ResultMessageSeq != nil ||
		profile == nil || profile.MainStatus != CandidateProfileSelected ||
		profile.SuccessfulGreetingIntentID != nil || profile.ConversationRef != nil || profile.GreetedAt != nil {
		t.Fatalf("结果事务失败后 Cmd/Intent/Profile 未整体回滚: cmd=%+v intent=%+v profile=%+v", cmd, intent, profile)
	}
	var processed, conversations, tracked, messages int64
	_ = s.db.Model(&ProcessedMsg{}).Where("msg_id = ?", "result-result-rollback").Count(&processed).Error
	_ = s.db.Model(&Conversation{}).Where("account_ref = ?", fixture.AccountRef).Count(&conversations).Error
	_ = s.db.Model(&TrackedIntent{}).Where("account_ref = ?", fixture.AccountRef).Count(&tracked).Error
	_ = s.db.Model(&Message{}).Where("account_ref = ?", fixture.AccountRef).Count(&messages).Error
	if processed != 0 || conversations != 0 || tracked != 0 || messages != 0 {
		t.Fatalf("结果事务失败泄漏业务/WAL 行: processed=%d conversations=%d tracked=%d messages=%d",
			processed, conversations, tracked, messages)
	}
}
