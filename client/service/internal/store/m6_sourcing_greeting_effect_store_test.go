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
	// 开思考模式(2026-08-16 甲方裁决)后正 reasoning token 是 OK 记录的常态形态。
	// fixture 特意用正值:2026-08-17 客户机整批发送曾被两处漏拆的内联"必须为 0"
	// 闸卡死,这里钉住扫描计划/进度/Prepare/意图创建/上下文绑定整个发送面不复发。
	reasoningTokens := 6233
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
				InputTokens: 10, OutputTokens: 5, ReasoningTokens: &reasoningTokens,
				UsageShape: AIInvocationUsageComplete, ReasoningContentEmpty: false,
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

func invalidateSourcingGreetingFixture(
	t *testing.T,
	fixture sourcingGreetingEffectFixture,
	at time.Time,
) *InvalidateSourcingFeedResult {
	t.Helper()
	result, err := fixture.Store.InvalidateSourcingFeed(InvalidateSourcingFeedRequest{
		Platform: fixture.AccountKey.Platform, AccountRef: fixture.AccountKey.AccountRef,
		Trigger: "testGreetingFeedChanged", At: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSourcingGreetingSendScanPlanReturnsEveryUnresolvedReadyTarget(t *testing.T) {
	fixture := seedSourcingGreetingEffectFixture(t, "scan-plan", 3)
	s := fixture.Store

	plan, err := s.SourcingGreetingSendScanPlan(fixture.BatchID)
	if err != nil || plan == nil {
		t.Fatalf("读取批量招呼续扫投影失败: plan=%+v err=%v", plan, err)
	}
	if plan.BatchID != fixture.BatchID ||
		plan.Platform != fixture.AccountKey.Platform ||
		plan.AccountRef != fixture.AccountKey.AccountRef ||
		plan.PositionRef != "position-"+fixture.BatchID ||
		plan.CapturedCount != 3 ||
		plan.BatchTailAnchor != "user-run-effect-scan-plan-c" ||
		len(plan.Targets) != 3 {
		t.Fatalf("续扫投影范围或尾锚错误: %+v", plan)
	}
	for i := range plan.Targets {
		target := plan.Targets[i]
		if target.InvocationID != fixture.Invocations[i].InvocationID ||
			target.PlatformUserRef != "user-run-effect-scan-plan-"+string(rune('a'+i)) ||
			target.PositionRef != plan.PositionRef || target.EffectIntentID != nil {
			t.Fatalf("第 %d 个未绑定目标投影错误: %+v", i, target)
		}
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	terminalReq := sourcingGreetingEffectRequest(
		t, fixture, fixture.Invocations[1], now,
	)
	terminal, err := s.CreateGreetingEffectIntentAndCmd(terminalReq)
	if err != nil || terminal == nil || !terminal.Created {
		t.Fatalf("建立终局 WAL 失败: result=%+v err=%v", terminal, err)
	}
	terminalAt := now.Add(2 * time.Minute)
	if err := s.db.Model(&CmdRecord{}).Where("msg_id = ?", terminal.Command.MsgID).
		Updates(map[string]any{"status": CmdOk, "terminal_at": terminalAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&EffectIntent{}).Where("intent_id = ?", terminal.Intent.IntentID).
		Update("status", EffectIntentOk).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", terminal.Intent.TargetRef).
		Updates(map[string]any{
			"main_status":                   CandidateProfileGreeted,
			"successful_greeting_intent_id": terminal.Intent.IntentID,
			"greeted_at":                    terminalAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	linkedReq := sourcingGreetingEffectRequest(
		t, fixture, fixture.Invocations[0], now.Add(3*time.Minute),
	)
	linked, err := s.CreateGreetingEffectIntentAndCmd(linkedReq)
	if err != nil || linked == nil || !linked.Created {
		t.Fatalf("建立 linked WAL 失败: result=%+v err=%v", linked, err)
	}
	if err := s.db.Model(&SourcingGreetingInvocation{}).
		Where("invocation_id = ?", fixture.Invocations[2].InvocationID).
		Updates(map[string]any{
			"status": AIInvocationTransportFailed, "greeting_text": "", "content_hash": "",
		}).Error; err != nil {
		t.Fatal(err)
	}

	plan, err = s.SourcingGreetingSendScanPlan(fixture.BatchID)
	if err != nil || plan == nil || len(plan.Targets) != 1 ||
		plan.Targets[0].InvocationID != fixture.Invocations[0].InvocationID ||
		plan.Targets[0].EffectIntentID == nil ||
		*plan.Targets[0].EffectIntentID != linked.Intent.IntentID {
		t.Fatalf("linked/terminal/生成失败分类错误: plan=%+v err=%v", plan, err)
	}

	invalidateSourcingGreetingFixture(t, fixture, now.Add(3*time.Minute))
	plan, err = s.SourcingGreetingSendScanPlan(fixture.BatchID)
	if err != nil || plan == nil || len(plan.Targets) != 1 ||
		plan.Targets[0].EffectIntentID == nil ||
		*plan.Targets[0].EffectIntentID != linked.Intent.IntentID {
		t.Fatalf("推荐流换代错误丢弃 linked WAL: plan=%+v err=%v", plan, err)
	}

	if err := s.db.Model(&CmdRecord{}).Where("msg_id = ?", linked.Command.MsgID).
		Update("args", `{}`).Error; err != nil {
		t.Fatal(err)
	}
	if plan, err := s.SourcingGreetingSendScanPlan(fixture.BatchID); plan != nil ||
		!errors.Is(err, ErrSourcingGreetingEffectConflict) {
		t.Fatalf("损坏 linked WAL 未被续扫投影复用校验拒绝: plan=%+v err=%v", plan, err)
	}
}

func TestSourcingGreetingSendScanPlanTailComesFromWholeCapturedBatch(t *testing.T) {
	base := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	revisionHash := "revision-effect-scan-tail"
	batchID := "batch-effect-scan-tail"
	s, key := prepareSourcingSelectionStore(t, revisionHash, 5, 2, 2, 100, base)
	fixtures := []selectionRunFixture{
		{RunID: "run-effect-scan-tail-a", Score: intPointer(9), CapturedAt: base},
		{RunID: "run-effect-scan-tail-b", Score: intPointer(8), CapturedAt: base.Add(time.Minute)},
		{RunID: "run-effect-scan-tail-c", Score: intPointer(4), CapturedAt: base.Add(2 * time.Minute)},
	}
	runs := insertCompletedSelectionBatch(t, s, key, batchID, revisionHash, base, fixtures)
	positionRef := "position-" + batchID
	boundAt := base.Add(-time.Second)
	if err := s.db.Model(&SourcingBatch{}).Where("batch_id = ?", batchID).Updates(map[string]any{
		"position_ref": positionRef, "position_bound_at": boundAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	selection, err := s.SelectCompletedSourcingBatch(batchID, base.Add(2*time.Hour))
	if err != nil || selection == nil || selection.SelectedCount != 2 {
		t.Fatalf("构造含非 selected 尾成员的批次失败: selection=%+v err=%v", selection, err)
	}
	decisions := sourcingSelectionOutcomes(t, s, batchID)
	zero := 0
	for i := 0; i < 2; i++ {
		reservation := greetingReservation(
			batchID,
			runs[i],
			decisions[runs[i].RunID],
			"invocation-effect-scan-tail-"+string(rune('a'+i)),
			base.Add(3*time.Hour+time.Duration(i)*time.Minute),
		)
		if _, err := s.ReserveSourcingGreeting(reservation); err != nil {
			t.Fatal(err)
		}
		text := "全批尾锚测试招呼 " + string(rune('甲'+i))
		if _, err := s.CompleteSourcingGreeting(CompleteSourcingGreetingRequest{
			Completion: AIInvocationCompletion{
				InvocationID: reservation.InvocationID, Status: AIInvocationOK,
				OutputHash:  "output-effect-scan-tail-" + string(rune('a'+i)),
				InputTokens: 10, OutputTokens: 5, ReasoningTokens: &zero,
				UsageShape: AIInvocationUsageComplete, ReasoningContentEmpty: true,
				FinishedAt: base.Add(4*time.Hour + time.Duration(i)*time.Minute),
			},
			GreetingText: text, ContentHash: sourcingGreetingContentHash(text),
		}); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := s.SourcingGreetingSendScanPlan(batchID)
	if err != nil || plan == nil || plan.CapturedCount != 3 ||
		plan.BatchTailAnchor != runs[2].PlatformUserRef || len(plan.Targets) != 2 {
		t.Fatalf("尾锚未取全批最后采集成员: plan=%+v err=%v", plan, err)
	}
	for _, target := range plan.Targets {
		if target.PlatformUserRef == plan.BatchTailAnchor {
			t.Fatalf("非 selected 尾成员错误进入发送目标: plan=%+v", plan)
		}
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
	generation, err := s.SourcingBatchGreetingProgress(fixture.BatchID)
	if err != nil || generation == nil || generation.SelectedCount != 1 ||
		generation.OKCount != 1 || !generation.Completed {
		t.Fatalf("profile 已 greeted 后历史生成聚合失效: progress=%+v err=%v", generation, err)
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

func TestSourcingGreetingFeedInvalidationAbandonsAllUnboundCompletedBatchMembers(t *testing.T) {
	fixture := seedSourcingGreetingEffectFixture(t, "feed-all-unbound", 3)
	batch, err := fixture.Store.SourcingBatchByID(fixture.BatchID)
	if err != nil || batch == nil {
		t.Fatalf("读取批次失败: batch=%+v err=%v", batch, err)
	}
	invalidated := invalidateSourcingGreetingFixture(t, fixture, batch.StartedAt.Add(time.Minute))
	if invalidated == nil || !invalidated.MarkerAdvanced || invalidated.BatchStopped {
		t.Fatalf("completed 批次换代只应推进 marker: result=%+v", invalidated)
	}

	next, err := fixture.Store.NextSourcingGreetingSendTarget(fixture.BatchID)
	if err != nil || next != nil {
		t.Fatalf("换代后仍返回未绑定目标: next=%+v err=%v", next, err)
	}
	progress, err := fixture.Store.SourcingBatchGreetingSendProgress(fixture.BatchID)
	if err != nil || progress == nil || progress.ReadyCount != 3 || progress.PendingCount != 0 ||
		progress.InFlightCount != 0 || progress.AbandonedCount != 3 || !progress.Completed {
		t.Fatalf("换代后的 abandoned 聚合错误: progress=%+v err=%v", progress, err)
	}
}

func TestSourcingGreetingFeedInvalidationKeepsBoundInflightAndAbandonsOnlyUnbound(t *testing.T) {
	fixture := seedSourcingGreetingEffectFixture(t, "feed-inflight", 2)
	now := time.Now().UTC().Truncate(time.Millisecond)
	req := sourcingGreetingEffectRequest(t, fixture, fixture.Invocations[0], now)
	created, err := fixture.Store.CreateGreetingEffectIntentAndCmd(req)
	if err != nil || created == nil || !created.Created {
		t.Fatalf("建立在途 WAL 失败: result=%+v err=%v", created, err)
	}
	invalidateSourcingGreetingFixture(t, fixture, now.Add(time.Second))

	next, err := fixture.Store.NextSourcingGreetingSendTarget(fixture.BatchID)
	if err != nil || next == nil || next.InvocationID != fixture.Invocations[0].InvocationID ||
		next.EffectIntentID == nil || *next.EffectIntentID != created.Intent.IntentID {
		t.Fatalf("换代错误丢弃已绑定在途目标: next=%+v err=%v", next, err)
	}
	progress, err := fixture.Store.SourcingBatchGreetingSendProgress(fixture.BatchID)
	if err != nil || progress == nil || progress.PendingCount != 0 || progress.InFlightCount != 1 ||
		progress.AbandonedCount != 1 || progress.Completed {
		t.Fatalf("在途与 abandoned 未正确分桶: progress=%+v err=%v", progress, err)
	}
}

func TestSourcingGreetingFeedInvalidationBetweenPrepareAndWALCreationRejectsAtomically(t *testing.T) {
	fixture := seedSourcingGreetingEffectFixture(t, "feed-create-race", 1)
	now := time.Now().UTC().Truncate(time.Millisecond)
	req := sourcingGreetingEffectRequest(t, fixture, fixture.Invocations[0], now)
	invalidateSourcingGreetingFixture(t, fixture, now.Add(time.Second))

	result, err := fixture.Store.CreateGreetingEffectIntentAndCmd(req)
	if result != nil || !errors.Is(err, ErrSourcingGreetingFeedChanged) {
		t.Fatalf("Prepare 后换代未阻断 WAL 创建: result=%+v err=%v", result, err)
	}
	invocation, err := fixture.Store.SourcingGreetingByProfileID(req.Intent.TargetRef)
	if err != nil || invocation == nil || invocation.EffectIntentID != nil || invocation.EffectStartedAt != nil {
		t.Fatalf("拒绝后 invocation 被绑定: invocation=%+v err=%v", invocation, err)
	}
	intent, err := fixture.Store.EffectIntentByID(req.Intent.IntentID)
	if err != nil || intent != nil {
		t.Fatalf("拒绝后创建了 intent: intent=%+v err=%v", intent, err)
	}
	cmd, err := fixture.Store.CmdByMsgID(req.Command.MsgID)
	if err != nil || cmd != nil {
		t.Fatalf("拒绝后创建了 command: command=%+v err=%v", cmd, err)
	}
	var heads int64
	if err := fixture.Store.db.Model(&CandidateGreetingHead{}).Count(&heads).Error; err != nil || heads != 0 {
		t.Fatalf("拒绝后创建了 greeting head: count=%d err=%v", heads, err)
	}
}

func TestSourcingGreetingFeedMarkerBeforeBatchStartDoesNotAbandonBatch(t *testing.T) {
	fixture := seedSourcingGreetingEffectFixture(t, "feed-older-marker", 2)
	batch, err := fixture.Store.SourcingBatchByID(fixture.BatchID)
	if err != nil || batch == nil {
		t.Fatalf("读取批次失败: batch=%+v err=%v", batch, err)
	}
	invalidateSourcingGreetingFixture(t, fixture, batch.StartedAt.Add(-time.Second))

	next, err := fixture.Store.NextSourcingGreetingSendTarget(fixture.BatchID)
	if err != nil || next == nil || next.InvocationID != fixture.Invocations[0].InvocationID ||
		next.EffectIntentID != nil {
		t.Fatalf("早于批次的 marker 错误废弃新批次: next=%+v err=%v", next, err)
	}
	progress, err := fixture.Store.SourcingBatchGreetingSendProgress(fixture.BatchID)
	if err != nil || progress == nil || progress.PendingCount != 2 || progress.AbandonedCount != 0 ||
		progress.Completed {
		t.Fatalf("早期 marker 污染新批次聚合: progress=%+v err=%v", progress, err)
	}
}
