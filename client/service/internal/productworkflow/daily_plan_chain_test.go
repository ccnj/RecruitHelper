package productworkflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
)

func dailyPlanChainFixture(t *testing.T) (
	*store.Store, store.AccountKey, *Manager, *fixturePipelineActor, *fixtureClock,
	m5ai.ContextRevision, m5ai.ContextRevision,
) {
	t.Helper()
	db, key, _ := productWorkflowFixture(t)
	location := time.FixedZone("CST", 8*60*60)
	clock := &fixtureClock{now: time.Date(2026, 9, 1, 9, 0, 0, 0, location)}
	at := clock.now.Add(-time.Hour)
	revA := dailyPlanRevisionFixture("1", "计划职位甲", 84, 84, at)
	revB := dailyPlanRevisionFixture("2", "计划职位乙", 80, 90, at)
	if _, err := db.SaveEffectiveLegacyJobAIContexts([]m5ai.ContextRevision{revA, revB}, at); err != nil {
		t.Fatal(err)
	}
	actor := &fixturePipelineActor{fixtureActor: &fixtureActor{store: db, clock: clock}}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	return db, key, manager, actor, clock, revA, revB
}

func finalizeActivePlanBothOnline(t *testing.T, db *store.Store, key store.AccountKey, at time.Time) store.DailyJobPlan {
	t.Helper()
	plan, _, err := db.ActiveDailyJobPlan(key)
	if err != nil || plan == nil {
		t.Fatalf("活跃计划缺失: %v", err)
	}
	finalized, err := db.FinalizeDailyJobPlan(plan.PlanID, []store.DailyJobPlanGateObservation{
		{Seq: 1, Online: true, StatusLabel: "在线中"},
		{Seq: 2, Online: true, StatusLabel: "在线中"},
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	return finalized.Plan
}

func terminalizeRunBatch(t *testing.T, db *store.Store, run *store.ProductWorkflowRun, reason string, at time.Time) {
	t.Helper()
	if run.SourcingBatchID == nil {
		t.Fatal("run 无批次")
	}
	if _, err := db.StopSourcingBatch(store.StopSourcingBatchRequest{
		BatchID: *run.SourcingBatchID, Reason: reason, StoppedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}

func walkRunToStage(t *testing.T, db *store.Store, run *store.ProductWorkflowRun, target string, at time.Time) *store.ProductWorkflowRun {
	t.Helper()
	order := []string{
		store.ProductWorkflowStageSourcing,
		store.ProductWorkflowStageScoring,
		store.ProductWorkflowStageSelection,
		store.ProductWorkflowStageGreetingGeneration,
		store.ProductWorkflowStageGreetingSending,
		store.ProductWorkflowStageCommunication,
	}
	started := false
	for _, stage := range order {
		if stage == run.Stage {
			started = true
			continue
		}
		if !started {
			continue
		}
		next, err := db.AdvanceProductWorkflowStage(store.AdvanceProductWorkflowStageRequest{
			RunID: run.RunID, ExpectedStage: run.Stage,
			ExpectedStatus: workflow.StatusRunning, NextStage: stage, At: at,
		})
		if err != nil {
			t.Fatalf("walk %s→%s: %v", run.Stage, stage, err)
		}
		run = next
		if stage == target {
			return run
		}
	}
	if run.Stage != target {
		t.Fatalf("未能走到 %s,现在 %s", target, run.Stage)
	}
	return run
}

// 全链贯通:开始建计划→条目一沟通→自动接续条目二→末条目开启巡检→跨日
// 收口计划完成。
func TestDailyPlanChainsThroughEntriesAndCompletes(t *testing.T) {
	db, key, manager, actor, clock, _, revB := dailyPlanChainFixture(t)

	runA, err := manager.StartFullDailyPlan(key)
	if err != nil || runA.SourcingBatchID == nil {
		t.Fatalf("StartFullDailyPlan: %+v err=%v", runA, err)
	}
	if actor.startTargets[0] != 63 || actor.startCaptureLimits[0] != 126 {
		t.Fatalf("首条目规模: %v %v", actor.startTargets, actor.startCaptureLimits)
	}
	plan := finalizeActivePlanBothOnline(t, db, key, clock.now)
	terminalizeRunBatch(t, db, runA, "testFunnelDone", clock.now)
	runA = walkRunToStage(t, db, runA, store.ProductWorkflowStageCommunication, clock.now)

	// tick 1:登记接续,按住巡检不开。
	held, err := manager.AdvanceOnce(context.Background())
	if err != nil || held.PendingAction != store.ProductWorkflowPendingActionSourcing ||
		actor.enableCalls != 0 {
		t.Fatalf("接续未登记或巡检被打开: %+v enable=%d err=%v", held, actor.enableCalls, err)
	}
	// tick 2:边界消费——条目一 done,旧 run 完结(additionalBatch),条目二开批。
	runB, err := manager.AdvanceOnce(context.Background())
	if err != nil || runB == nil || runB.RunID == runA.RunID ||
		runB.Stage != store.ProductWorkflowStageSourcing {
		t.Fatalf("接续未开新 run: %+v err=%v", runB, err)
	}
	if actor.startTargets[1] != 63 || actor.startCaptureLimits[1] != 126 {
		t.Fatalf("条目二规模: %v %v", actor.startTargets, actor.startCaptureLimits)
	}
	_, entries, err := db.ActiveDailyJobPlan(key)
	if err != nil || entries[0].Status != store.DailyJobPlanEntryDone {
		t.Fatalf("条目一未落 done: %+v err=%v", entries, err)
	}
	prior, err := db.ProductWorkflowRunByID(runA.RunID)
	if err != nil || prior.Status != workflow.StatusCompleted ||
		prior.EndReason != productWorkflowEndReasonAdditionalBatch {
		t.Fatalf("旧 run 未按 additionalBatch 完结: %+v err=%v", prior, err)
	}
	batchB, err := db.SourcingBatchByID(*runB.SourcingBatchID)
	if err != nil || batchB.ContextRevisionHash != revB.RevisionHash {
		t.Fatalf("条目二批次 revision 错误: %+v err=%v", batchB, err)
	}

	// 条目二走完:末条目 done、巡检开启、计划仍活跃。
	terminalizeRunBatch(t, db, runB, "testFunnelDone", clock.now)
	runB = walkRunToStage(t, db, runB, store.ProductWorkflowStageCommunication, clock.now)
	final, err := manager.AdvanceOnce(context.Background())
	if err != nil || final.PendingAction != "" || actor.enableCalls == 0 {
		t.Fatalf("末条目应开启巡检: %+v enable=%d err=%v", final, actor.enableCalls, err)
	}
	_, entries, _ = db.ActiveDailyJobPlan(key)
	if entries[1].Status != store.DailyJobPlanEntryDone {
		t.Fatalf("末条目未落 done: %+v", entries)
	}

	// 跨日:run 以 dailyWindowClosed 终局 → 收口扫描把计划记完成。
	clock.now = clock.now.Add(16 * time.Hour)
	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatalf("跨日终局: %v", err)
	}
	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatalf("收口扫描: %v", err)
	}
	var closed store.DailyJobPlan
	if err := dbPlanByID(db, plan.PlanID, &closed); err != nil {
		t.Fatal(err)
	}
	if closed.Status != store.DailyJobPlanCompleted {
		t.Fatalf("计划未完成收口: %+v", closed)
	}
	// 跨日关窗收口不自动开沟通:窗口本来就关了。
	if active, err := db.ActiveProductWorkflowRun(); err != nil || active != nil {
		t.Fatalf("跨日收口不得自动开启沟通: %+v err=%v", active, err)
	}
}

// 末条目被跳过类失败拦下后,计划记完成并自动开启沟通巡检(只处理消息模式),
// 已发候选人有人盯回复;跳过原因带批次留痕的判定现场(2026-09-02 甲方裁决)。
func TestDailyPlanSkippedLastEntryAutoStartsCommunication(t *testing.T) {
	db, key, manager, actor, clock, _, _ := dailyPlanChainFixture(t)
	runA, err := manager.StartFullDailyPlan(key)
	if err != nil {
		t.Fatal(err)
	}
	plan := finalizeActivePlanBothOnline(t, db, key, clock.now)
	terminalizeRunBatch(t, db, runA, "testFunnelDone", clock.now)
	walkRunToStage(t, db, runA, store.ProductWorkflowStageCommunication, clock.now)
	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatalf("登记接续: %v", err)
	}
	runB, err := manager.AdvanceOnce(context.Background())
	if err != nil || runB == nil || runB.RunID == runA.RunID || runB.SourcingBatchID == nil {
		t.Fatalf("接续未开新 run: %+v err=%v", runB, err)
	}
	enableBefore := actor.enableCalls

	// 条目二开批时推荐页两次未就绪:批次 blocked(recommendPageNotReady)+留痕,
	// run 失败——这正是 failStoppedPipeline 的落账形态。
	if _, err := db.BlockSourcingBatch(store.BlockSourcingBatchRequest{
		BatchID: *runB.SourcingBatchID, Reason: store.SourcingBatchGateReasonRecommendPageNotReady,
		Detail: "CTX_NOT_READY/pageBroken: 智联推荐页在期限内未就绪", BlockedAt: clock.now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: runB.RunID,
		From:  workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning},
		To:    workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusFailed},
		At:    clock.now, Stage: store.ProductWorkflowStageFailed,
		Failure: "产品工作流批次推进状态无效: recommendPageNotReady",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatalf("收口扫描: %v", err)
	}
	bundle, err := db.DailyJobPlanByID(plan.PlanID)
	if err != nil || bundle == nil || bundle.Plan.Status != store.DailyJobPlanCompleted {
		t.Fatalf("计划未记完成: %+v err=%v", bundle, err)
	}
	if bundle.Entries[1].Status != store.DailyJobPlanEntrySkipped ||
		bundle.Entries[1].SkipReason != "batch:recommendPageNotReady|CTX_NOT_READY/pageBroken: 智联推荐页在期限内未就绪" {
		t.Fatalf("末条目未按跳过类带现场留痕: %+v", bundle.Entries)
	}
	if active, err := db.ActiveSourcingBatch(key); err != nil || active != nil {
		t.Fatalf("跳过后不得残留未终局批次: %+v err=%v", active, err)
	}
	active, err := db.ActiveProductWorkflowRun()
	if err != nil || active == nil || active.Mode != workflow.ModeReplyOnly ||
		active.Status != workflow.StatusRunning || active.Stage != store.ProductWorkflowStageCommunication {
		t.Fatalf("收口后应自动开启只处理消息运行: %+v err=%v", active, err)
	}
	if actor.enableCalls != enableBefore+1 {
		t.Fatalf("账号巡检应被重新启用一次: before=%d after=%d", enableBefore, actor.enableCalls)
	}
}

// 用户点结束的收口不自动开沟通:结束就是结束。
func TestDailyPlanUserEndedDoesNotAutoStartCommunication(t *testing.T) {
	db, key, manager, actor, clock, _, _ := dailyPlanChainFixture(t)
	runA, err := manager.StartFullDailyPlan(key)
	if err != nil {
		t.Fatal(err)
	}
	plan := finalizeActivePlanBothOnline(t, db, key, clock.now)
	terminalizeRunBatch(t, db, runA, "testFunnelDone", clock.now)
	walkRunToStage(t, db, runA, store.ProductWorkflowStageCommunication, clock.now)
	if _, err := manager.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	enableBefore := actor.enableCalls
	for i := 0; i < 3; i++ {
		if _, err := manager.AdvanceOnce(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	var closed store.DailyJobPlan
	if err := dbPlanByID(db, plan.PlanID, &closed); err != nil {
		t.Fatal(err)
	}
	if closed.Status != store.DailyJobPlanAborted || closed.EndReason != planEndReasonUserEnded {
		t.Fatalf("计划未按 userEnded 终止: %+v", closed)
	}
	if active, err := db.ActiveProductWorkflowRun(); err != nil || active != nil {
		t.Fatalf("用户结束后不得自动开启沟通: %+v err=%v", active, err)
	}
	if actor.enableCalls != enableBefore {
		t.Fatalf("用户结束后账号不得被重新启用: before=%d after=%d", enableBefore, actor.enableCalls)
	}
}

// dbPlanByID 直读终局计划(测试辅助)。
func dbPlanByID(db *store.Store, planID string, out *store.DailyJobPlan) error {
	bundle, err := db.DailyJobPlanByID(planID)
	if err != nil {
		return err
	}
	if bundle == nil {
		return errors.New("plan not found: " + planID)
	}
	*out = bundle.Plan
	return nil
}

// 跳过类失败(职位不在线族)只跳过该条目并接续下一条目(2026-09-01 裁决)。
func TestDailyPlanSkipsGateStoppedEntryAndChains(t *testing.T) {
	db, key, manager, actor, clock, _, revB := dailyPlanChainFixture(t)
	runA, err := manager.StartFullDailyPlan(key)
	if err != nil {
		t.Fatal(err)
	}
	finalizeActivePlanBothOnline(t, db, key, clock.now)
	// 模拟批前闸拦下:批次 stopped(jobNotOnline)、run 失败——这正是
	// failStoppedPipeline 的落账形态。
	terminalizeRunBatch(t, db, runA, "jobNotOnline", clock.now)
	if _, err := db.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: runA.RunID,
		From:  workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning},
		To:    workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusFailed},
		At:    clock.now, Stage: store.ProductWorkflowStageFailed, Failure: "sourcingBatchStopped",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatalf("收口扫描: %v", err)
	}
	_, entries, err := db.ActiveDailyJobPlan(key)
	if err != nil || entries[0].Status != store.DailyJobPlanEntrySkipped ||
		entries[0].SkipReason != "batch:jobNotOnline" {
		t.Fatalf("条目一未按跳过类留痕: %+v err=%v", entries, err)
	}
	active, err := db.ActiveProductWorkflowRun()
	if err != nil || active == nil || active.SourcingBatchID == nil {
		t.Fatalf("未接续条目二: %+v err=%v", active, err)
	}
	batch, err := db.SourcingBatchByID(*active.SourcingBatchID)
	if err != nil || batch.ContextRevisionHash != revB.RevisionHash {
		t.Fatalf("接续批次错误: %+v err=%v", batch, err)
	}
	_ = actor
}

// 非跳过类失败终止整个计划,并终局化残留 blocked 批次——防止次日被当成存量
// 批次收养后按配置全额跑掉(超发方向,必须堵死)。
func TestDailyPlanAbortsOnNonSkipFailureAndTerminalizesBatch(t *testing.T) {
	db, key, manager, _, clock, _, _ := dailyPlanChainFixture(t)
	runA, err := manager.StartFullDailyPlan(key)
	if err != nil {
		t.Fatal(err)
	}
	plan := finalizeActivePlanBothOnline(t, db, key, clock.now)
	// blocked(非终局)+ 非跳过类原因。
	if _, err := db.BlockSourcingBatch(store.BlockSourcingBatchRequest{
		BatchID: *runA.SourcingBatchID, Reason: "windowNoProgress", BlockedAt: clock.now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: runA.RunID,
		From:  workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning},
		To:    workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusFailed},
		At:    clock.now, Stage: store.ProductWorkflowStageFailed, Failure: "sourcingBatchStopped",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatalf("收口扫描: %v", err)
	}
	var closed store.DailyJobPlan
	if err := dbPlanByID(db, plan.PlanID, &closed); err != nil {
		t.Fatal(err)
	}
	if closed.Status != store.DailyJobPlanAborted || closed.EndReason != planEndReasonRunFailed {
		t.Fatalf("计划未按 runFailed 终止: %+v", closed)
	}
	if active, err := db.ActiveSourcingBatch(key); err != nil || active != nil {
		t.Fatalf("blocked 批次未被终局化: %+v err=%v", active, err)
	}
	// 运行失败终止后自动开启只处理消息运行(2026-09-02 甲方裁决):已发的人有人盯回复。
	auto, err := db.ActiveProductWorkflowRun()
	if err != nil || auto == nil || auto.Mode != workflow.ModeReplyOnly ||
		auto.Stage != store.ProductWorkflowStageCommunication {
		t.Fatalf("runFailed 终止后应自动开启只处理消息运行: %+v err=%v", auto, err)
	}
	// 次日重来是全新计划,不收养旧批次:昨日自动开启的沟通运行先按跨日终局。
	clock.now = clock.now.Add(24 * time.Hour)
	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatalf("跨日终局: %v", err)
	}
	if stale, err := db.ActiveProductWorkflowRun(); err != nil || stale != nil {
		t.Fatalf("昨日沟通运行未按跨日终局: %+v err=%v", stale, err)
	}
	fresh, err := manager.StartFullDailyPlan(key)
	if err != nil || fresh == nil {
		t.Fatalf("次日重来失败: %+v err=%v", fresh, err)
	}
	freshPlan, _, err := db.ActiveDailyJobPlan(key)
	if err != nil || freshPlan == nil || freshPlan.PlanID == plan.PlanID {
		t.Fatalf("未建立全新计划: %+v err=%v", freshPlan, err)
	}
}

// 用户结束当日运行 → 收口扫描按 userEnded 终止计划。
func TestDailyPlanAbortsWhenUserEndsRun(t *testing.T) {
	db, key, manager, _, clock, _, _ := dailyPlanChainFixture(t)
	runA, err := manager.StartFullDailyPlan(key)
	if err != nil {
		t.Fatal(err)
	}
	plan := finalizeActivePlanBothOnline(t, db, key, clock.now)
	terminalizeRunBatch(t, db, runA, "userEndedWorkflow", clock.now)
	if _, err := db.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: runA.RunID,
		From:  workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning},
		To:    workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusCompleted},
		At:    clock.now, Stage: store.ProductWorkflowStageCompleted,
		EndReason: productWorkflowEndReasonUserEnded,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatalf("收口扫描: %v", err)
	}
	var closed store.DailyJobPlan
	if err := dbPlanByID(db, plan.PlanID, &closed); err != nil {
		t.Fatal(err)
	}
	if closed.Status != store.DailyJobPlanAborted || closed.EndReason != planEndReasonUserEnded {
		t.Fatalf("计划未按 userEnded 终止: %+v", closed)
	}
}

// 排序硬约束(出口硬性回归):批次发送未全部终局时,下一职位的批次开不出来。
// 两道断言:发送阶段不产生接续登记;即使有人越权登记,存储层也拒绝非沟通
// 阶段的 sourcing pending。
func TestNextJobCannotOpenBeforeSendingFullyTerminal(t *testing.T) {
	db, key, manager, actor, clock, _, revB := dailyPlanChainFixture(t)
	runA, err := manager.StartFullDailyPlan(key)
	if err != nil {
		t.Fatal(err)
	}
	finalizeActivePlanBothOnline(t, db, key, clock.now)
	terminalizeRunBatch(t, db, runA, "testFunnelDone", clock.now)
	runA = walkRunToStage(t, db, runA, store.ProductWorkflowStageGreetingSending, clock.now)

	// 发送未完成:progress.Completed=false,编排器停在发送阶段。
	actor.sendProgress = &store.SourcingBatchGreetingSendProgress{
		SelectedCount: 3, SentCount: 1, PendingCount: 2, Completed: false,
	}
	stuck, err := manager.AdvanceOnce(context.Background())
	if err != nil || stuck.Stage != store.ProductWorkflowStageGreetingSending ||
		stuck.PendingAction != "" {
		t.Fatalf("发送未终局不得离开发送阶段/登记接续: %+v err=%v", stuck, err)
	}
	if len(actor.startTargets) != 1 {
		t.Fatalf("发送未终局却开了新批: %v", actor.startTargets)
	}
	// 越权登记也被存储层拒绝:sourcing pending 只接受沟通阶段。
	if _, err := db.RequestProductWorkflowPendingAction(store.RequestProductWorkflowPendingActionRequest{
		RunID: runA.RunID, Action: store.ProductWorkflowPendingActionSourcing,
		ContextRevisionHash: revB.RevisionHash, RequestedAt: clock.now,
	}); !errors.Is(err, store.ErrProductWorkflowConflict) {
		t.Fatalf("非沟通阶段的接续登记应被拒绝: %v", err)
	}
}

// 活跃运行期间重复点击开始是幂等空操作:原「沟通期再采一批」入口随当日计划
// 停用(设计文档「存量与入口兼容」,待甲方追认)。
func TestStartFullDailyPlanIsIdempotentDuringActiveRun(t *testing.T) {
	db, key, manager, actor, clock, _, _ := dailyPlanChainFixture(t)
	runA, err := manager.StartFullDailyPlan(key)
	if err != nil {
		t.Fatal(err)
	}
	finalizeActivePlanBothOnline(t, db, key, clock.now)
	terminalizeRunBatch(t, db, runA, "testFunnelDone", clock.now)
	runA = walkRunToStage(t, db, runA, store.ProductWorkflowStageCommunication, clock.now)

	again, err := manager.StartFullDailyPlan(key)
	if err != nil || again.RunID != runA.RunID || again.PendingAction != "" {
		t.Fatalf("沟通期重复开始应幂等返回: %+v err=%v", again, err)
	}
	if len(actor.startTargets) != 1 {
		t.Fatalf("重复开始不得另开批次: %v", actor.startTargets)
	}
	plans, err := db.ActiveDailyJobPlans()
	if err != nil || len(plans) != 1 {
		t.Fatalf("重复开始不得另建计划: %d err=%v", len(plans), err)
	}
}

// 生产真实时序回归(审查阻断项):计划序第一个职位离线时,定稿钩子先把条目 1
// 标 skipped、随后同一次闸读取把批次拦停(jobNotOnline)、run 失败——收口扫描
// 必须照样接续条目 2,而不是把整份计划按 runFailed 终止。
func TestDailyPlanChainsWhenFirstEntryAlreadySkippedByFinalize(t *testing.T) {
	db, key, manager, _, clock, _, revB := dailyPlanChainFixture(t)
	runA, err := manager.StartFullDailyPlan(key)
	if err != nil {
		t.Fatal(err)
	}
	// 定稿:条目 1 离线(被定稿直接标 skipped)、条目 2 在线。
	plan, _, err := db.ActiveDailyJobPlan(key)
	if err != nil || plan == nil {
		t.Fatalf("计划缺失: %v", err)
	}
	if _, err := db.FinalizeDailyJobPlan(plan.PlanID, []store.DailyJobPlanGateObservation{
		{Seq: 1, Online: false, StatusLabel: "未上线"},
		{Seq: 2, Online: true, StatusLabel: "在线中"},
	}, clock.now); err != nil {
		t.Fatal(err)
	}
	// 批次随后被同一次闸读取拦停,run 失败(failStoppedPipeline 的落账形态)。
	terminalizeRunBatch(t, db, runA, "jobNotOnline", clock.now)
	if _, err := db.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: runA.RunID,
		From:  workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning},
		To:    workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusFailed},
		At:    clock.now, Stage: store.ProductWorkflowStageFailed, Failure: "sourcingBatchStopped",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatalf("收口扫描: %v", err)
	}
	var closed store.DailyJobPlan
	if err := dbPlanByID(db, plan.PlanID, &closed); err != nil {
		t.Fatal(err)
	}
	if closed.Status == store.DailyJobPlanAborted {
		t.Fatalf("首条目离线不得终止整份计划: %+v", closed)
	}
	active, err := db.ActiveProductWorkflowRun()
	if err != nil || active == nil || active.SourcingBatchID == nil {
		t.Fatalf("未接续条目二: %+v err=%v", active, err)
	}
	batch, err := db.SourcingBatchByID(*active.SourcingBatchID)
	if err != nil || batch.ContextRevisionHash != revB.RevisionHash {
		t.Fatalf("接续批次错误: %+v err=%v", batch, err)
	}
}

// replyOnly 插曲不得决定计划生死:条目一被跳过类原因拦停后,用户点过「只处理
// 消息」又结束——收口分类只看本账号最近一次 full 运行,照样跳过接续。
func TestDailyPlanIgnoresReplyOnlyInterludeWhenClassifying(t *testing.T) {
	db, key, manager, _, clock, _, revB := dailyPlanChainFixture(t)
	runA, err := manager.StartFullDailyPlan(key)
	if err != nil {
		t.Fatal(err)
	}
	finalizeActivePlanBothOnline(t, db, key, clock.now)
	terminalizeRunBatch(t, db, runA, "jobNotOnline", clock.now)
	if _, err := db.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: runA.RunID,
		From:  workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning},
		To:    workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusFailed},
		At:    clock.now, Stage: store.ProductWorkflowStageFailed, Failure: "sourcingBatchStopped",
	}); err != nil {
		t.Fatal(err)
	}
	// replyOnly 插曲:开始又结束,成为全局最近一条 run。
	clock.now = clock.now.Add(time.Minute)
	replyRun, err := manager.StartReplyOnly(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: replyRun.RunID,
		From:  workflow.State{Mode: workflow.ModeReplyOnly, Status: workflow.StatusRunning},
		To:    workflow.State{Mode: workflow.ModeReplyOnly, Status: workflow.StatusCompleted},
		At:    clock.now, Stage: store.ProductWorkflowStageCompleted,
		EndReason: productWorkflowEndReasonUserEnded,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatalf("收口扫描: %v", err)
	}
	_, entries, err := db.ActiveDailyJobPlan(key)
	if err != nil || entries == nil || entries[0].Status != store.DailyJobPlanEntrySkipped {
		t.Fatalf("计划被 replyOnly 插曲错杀: entries=%+v err=%v", entries, err)
	}
	active, err := db.ActiveProductWorkflowRun()
	if err != nil || active == nil || active.Mode != workflow.ModeFull {
		t.Fatalf("未接续条目二: %+v err=%v", active, err)
	}
	batch, err := db.SourcingBatchByID(*active.SourcingBatchID)
	if err != nil || batch.ContextRevisionHash != revB.RevisionHash {
		t.Fatalf("接续批次错误: %+v err=%v", batch, err)
	}
}
