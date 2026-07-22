package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/contract/gen/go/protocol"

	"gorm.io/gorm"
)

type sourcingGreetingEffectFixture struct {
	Store       *Store
	BatchID     string
	AccountKey  AccountKey
	HandID      string
	Session     string
	BootID      string
	Principal   string
	Invocations []SourcingGreetingInvocation
}

func seedSourcingGreetingEffectFixture(
	t *testing.T,
	slug string,
	count int,
) sourcingGreetingEffectFixture {
	t.Helper()
	base := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Millisecond)
	fixtures := make([]selectionRunFixture, count)
	for i := range fixtures {
		score := 10 - i
		fixtures[i] = selectionRunFixture{
			RunID: "run-effect-" + slug + "-" + string(rune('a'+i)), Score: &score,
			CapturedAt: base.Add(time.Duration(i) * time.Minute),
		}
	}
	batchID := "batch-effect-" + slug
	revisionHash := "revision-effect-" + slug
	s, runs, decisions := prepareSourcingGreetingBatch(
		t, batchID, revisionHash, count, base, fixtures,
	)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-" + revisionHash}
	fixture := sourcingGreetingEffectFixture{
		Store: s, BatchID: batchID, AccountKey: key,
		HandID: "hand-effect-" + slug, Session: "session-effect-" + slug,
		BootID: "boot-effect-" + slug, Principal: "principal-effect-" + slug,
	}
	if err := s.BindAccountPrincipal(
		key, fixture.HandID, fixture.Principal, fixture.Session, fixture.BootID, base.Add(3*time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	zero := 0
	for i := range runs {
		reservation := greetingReservation(
			batchID, runs[i], decisions[runs[i].RunID],
			"invocation-effect-"+slug+"-"+string(rune('a'+i)), base.Add(2*time.Hour+time.Duration(i)*time.Minute),
		)
		if _, err := s.ReserveSourcingGreeting(reservation); err != nil {
			t.Fatal(err)
		}
		text := "正式批次招呼正文 " + string(rune('甲'+i))
		invocation, err := s.CompleteSourcingGreeting(CompleteSourcingGreetingRequest{
			Completion: AIInvocationCompletion{
				InvocationID: reservation.InvocationID, Status: AIInvocationOK,
				OutputHash:  "output-effect-" + slug + "-" + string(rune('a'+i)),
				InputTokens: 10, OutputTokens: 5, ReasoningTokens: &zero,
				UsageShape: AIInvocationUsageComplete, ReasoningContentEmpty: true,
				FinishedAt: base.Add(2*time.Hour + 30*time.Minute + time.Duration(i)*time.Minute),
			},
			GreetingText: text, ContentHash: sourcingGreetingContentHash(text),
		})
		if err != nil {
			t.Fatal(err)
		}
		fixture.Invocations = append(fixture.Invocations, *invocation)
	}
	return fixture
}

func sourcingGreetingEffectRequest(
	t *testing.T,
	fixture sourcingGreetingEffectFixture,
	invocation SourcingGreetingInvocation,
	now time.Time,
) CreateGreetingEffectIntentRequest {
	t.Helper()
	preparation, err := fixture.Store.PrepareSourcingGreetingSend(fixture.BatchID, invocation.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	args, err := protocol.Encode(protocol.ChatSendGreetingArgs{
		PlatformUserRef: preparation.Profile.PlatformUserRef,
		PositionRef:     preparation.Profile.PositionRef,
		Text:            preparation.GreetingText,
	})
	if err != nil {
		t.Fatal(err)
	}
	guards, err := protocol.Encode(protocol.ChatSendGreetingGuards{ExpectUnestablished: true})
	if err != nil {
		t.Fatal(err)
	}
	contextJSON, err := protocol.Encode(protocol.CmdContext{
		Platform: preparation.Profile.Platform, AccountRef: preparation.Profile.AccountRef,
		ExpectedPrincipalFingerprint: fixture.Principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	intentID := preparation.IntentID
	msgID := "msg-" + intentID
	idemKey := "ik1:" + preparation.Profile.Platform + ":" + preparation.Profile.AccountRef +
		":" + primitiveChatSendGreeting + ":" + preparation.Profile.ProfileID + ":" + intentID
	deadline := now.Add(time.Hour).UnixMilli()
	return CreateGreetingEffectIntentRequest{
		Intent: EffectIntent{
			IntentID: intentID, IdemKey: idemKey,
			Platform: preparation.Profile.Platform, AccountRef: preparation.Profile.AccountRef,
			Primitive: primitiveChatSendGreeting, TargetRef: preparation.Profile.ProfileID,
			PayloadHash: sourcingGreetingContentHash(string(args)),
			GuardsHash:  sourcingGreetingContentHash(string(guards)),
			Status:      EffectIntentDispatching, DeadlineMs: deadline,
			SendFingerprint: sourcingGreetingSendFingerprint(preparation.GreetingText),
		},
		Command: CmdRecord{
			MsgID: msgID, Name: primitiveChatSendGreeting, Class: "effectful", IdemKey: idemKey,
			Domain:   preparation.Profile.Platform + ":" + preparation.Profile.AccountRef,
			Platform: preparation.Profile.Platform, AccountRef: preparation.Profile.AccountRef,
			ExpectedPrincipalFingerprint: fixture.Principal, ContextJSON: string(contextJSON),
			Args: string(args), Guards: string(guards), IntentID: intentID,
			HandID: fixture.HandID, Session: fixture.Session, BootIDAtDispatch: fixture.BootID,
			Status: CmdQueued, DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		SourcingSource: &preparation.Source,
		Now:            now,
	}
}

func TestSourcingGreetingEffectSourceAtomicallyBindsWALAndPreciselyReplays(t *testing.T) {
	fixture := seedSourcingGreetingEffectFixture(t, "atomic", 1)
	s := fixture.Store
	now := time.Now().UTC().Truncate(time.Millisecond)
	req := sourcingGreetingEffectRequest(t, fixture, fixture.Invocations[0], now)

	created, err := s.CreateGreetingEffectIntentAndCmd(req)
	if err != nil || created == nil || !created.Created {
		t.Fatalf("自动招呼来源未原子进入 WAL: result=%+v err=%v", created, err)
	}
	stored, err := s.SourcingGreetingByProfileID(req.Intent.TargetRef)
	if err != nil || stored == nil || stored.EffectIntentID == nil ||
		*stored.EffectIntentID != req.Intent.IntentID || stored.EffectStartedAt == nil {
		t.Fatalf("invocation 未绑定唯一 intent: invocation=%+v err=%v", stored, err)
	}
	replayed, err := s.CreateGreetingEffectIntentAndCmd(req)
	if err != nil || replayed == nil || replayed.Created ||
		replayed.Intent.IntentID != created.Intent.IntentID || replayed.Command.MsgID != created.Command.MsgID {
		t.Fatalf("精确重放未收编原 WAL: result=%+v err=%v", replayed, err)
	}
	wrongSource := req
	wrongSource.SourcingSource = &SourcingGreetingEffectSource{
		BatchID: "another-batch", InvocationID: fixture.Invocations[0].InvocationID,
	}
	if result, err := s.CreateGreetingEffectIntentAndCmd(wrongSource); result != nil ||
		!errors.Is(err, ErrSourcingBatchNotFound) {
		t.Fatalf("跨批来源重放未拒绝: result=%+v err=%v", result, err)
	}

	terminalAt := now.Add(time.Minute)
	if err := s.db.Model(&CmdRecord{}).Where("intent_id = ?", req.Intent.IntentID).
		Updates(map[string]any{"status": CmdOk, "terminal_at": terminalAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&EffectIntent{}).Where("intent_id = ?", req.Intent.IntentID).
		Update("status", EffectIntentOk).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", req.Intent.TargetRef).
		Updates(map[string]any{
			"main_status": CandidateProfileGreeted, "successful_greeting_intent_id": req.Intent.IntentID,
			"greeted_at": terminalAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	prepared, err := s.PrepareSourcingGreetingSend(fixture.BatchID, fixture.Invocations[0].InvocationID)
	if err != nil || prepared == nil || !prepared.EffectLinked || prepared.IntentID != req.Intent.IntentID {
		t.Fatalf("profile 已 greeted 后精确重放失效: preparation=%+v err=%v", prepared, err)
	}

	var intents, commands, heads int64
	_ = s.db.Model(&EffectIntent{}).Count(&intents).Error
	_ = s.db.Model(&CmdRecord{}).Where("intent_id <> ?", "").Count(&commands).Error
	_ = s.db.Model(&CandidateGreetingHead{}).Count(&heads).Error
	if intents != 1 || commands != 1 || heads != 1 {
		t.Fatalf("重放发生账本增生: intents=%d commands=%d heads=%d", intents, commands, heads)
	}
}

func TestSourcingGreetingEffectBindingFailureRollsBackIntentCommandAndHead(t *testing.T) {
	fixture := seedSourcingGreetingEffectFixture(t, "rollback", 1)
	s := fixture.Store
	req := sourcingGreetingEffectRequest(t, fixture, fixture.Invocations[0], time.Now())
	forced := errors.New("forced sourcing invocation bind failure")
	callback := "test:fail_sourcing_greeting_effect_bind"
	if err := s.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "SourcingGreetingInvocation" {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Update().Remove(callback)

	result, err := s.CreateGreetingEffectIntentAndCmd(req)
	if result != nil || !errors.Is(err, forced) {
		t.Fatalf("未返回绑定写失败: result=%+v err=%v", result, err)
	}
	invocation, _ := s.SourcingGreetingByProfileID(req.Intent.TargetRef)
	intent, _ := s.EffectIntentByID(req.Intent.IntentID)
	cmd, _ := s.CmdByMsgID(req.Command.MsgID)
	var heads int64
	_ = s.db.Model(&CandidateGreetingHead{}).Count(&heads).Error
	if invocation == nil || invocation.EffectIntentID != nil || invocation.EffectStartedAt != nil ||
		intent != nil || cmd != nil || heads != 0 {
		t.Fatalf("绑定失败泄漏半态: invocation=%+v intent=%+v cmd=%+v heads=%d",
			invocation, intent, cmd, heads)
	}
}

func TestSourcingGreetingEffectAuthorizationIsTargetScopedAcrossGreetedProfiles(t *testing.T) {
	fixture := seedSourcingGreetingEffectFixture(t, "target-scope", 2)
	s := fixture.Store
	now := time.Now().UTC().Truncate(time.Millisecond)
	firstReq := sourcingGreetingEffectRequest(t, fixture, fixture.Invocations[0], now)
	first, err := s.CreateGreetingEffectIntentAndCmd(firstReq)
	if err != nil {
		t.Fatal(err)
	}
	terminalAt := now.Add(time.Second)
	if err := s.db.Model(&CmdRecord{}).Where("msg_id = ?", first.Command.MsgID).
		Updates(map[string]any{"status": CmdOk, "terminal_at": terminalAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&EffectIntent{}).Where("intent_id = ?", first.Intent.IntentID).
		Update("status", EffectIntentOk).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", first.Intent.TargetRef).
		Updates(map[string]any{
			"main_status":                   CandidateProfileGreeted,
			"successful_greeting_intent_id": first.Intent.IntentID, "greeted_at": terminalAt,
		}).Error; err != nil {
		t.Fatal(err)
	}

	target, err := s.NextSourcingGreetingSendTarget(fixture.BatchID)
	if err != nil || target == nil || target.InvocationID != fixture.Invocations[1].InvocationID {
		t.Fatalf("首人 greeted 后未选到第二人: target=%+v err=%v", target, err)
	}
	secondReq := sourcingGreetingEffectRequest(t, fixture, fixture.Invocations[1], now.Add(time.Minute))
	second, err := s.CreateGreetingEffectIntentAndCmd(secondReq)
	if err != nil || second == nil || !second.Created {
		t.Fatalf("首人已 greeted 错误阻断第二人: result=%+v err=%v", second, err)
	}
	progress, err := s.SourcingBatchGreetingSendProgress(fixture.BatchID)
	if err != nil || progress == nil || progress.ContextRevisionHash == "" ||
		progress.SelectedCount != 2 || progress.ReadyCount != 2 ||
		progress.SentCount != 1 || progress.InFlightCount != 1 || progress.Completed {
		t.Fatalf("发送聚合错误: progress=%+v err=%v", progress, err)
	}
}
