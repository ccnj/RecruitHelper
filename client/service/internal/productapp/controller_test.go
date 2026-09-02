package productapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/testfixture"
	"recruithelper/client/service/internal/workflow"
)

type fakeWorkflow struct {
	fullKey      store.AccountKey
	fullRevision string
	replyKey     store.AccountKey
	pauseCalls   int
	resumeCalls  int
	endCalls     int
	callOrder    []string
	confirmBatch string
	confirmIDs   []string
}

func (f *fakeWorkflow) StartFull(
	key store.AccountKey,
	revision string,
) (*store.ProductWorkflowRun, error) {
	f.callOrder = append(f.callOrder, "full")
	f.fullKey, f.fullRevision = key, revision
	return &store.ProductWorkflowRun{
		RunID: "wf-fake", Platform: key.Platform, AccountRef: key.AccountRef,
		Mode: workflow.ModeFull, Status: workflow.StatusRunning,
	}, nil
}

func (f *fakeWorkflow) StartFullDailyPlan(
	key store.AccountKey,
) (*store.ProductWorkflowRun, error) {
	f.callOrder = append(f.callOrder, "dailyPlan")
	f.fullKey = key
	return &store.ProductWorkflowRun{
		RunID: "wf-fake", Platform: key.Platform, AccountRef: key.AccountRef,
		Mode: workflow.ModeFull, Status: workflow.StatusRunning,
	}, nil
}

func (f *fakeWorkflow) StartReplyOnly(
	key store.AccountKey,
) (*store.ProductWorkflowRun, error) {
	f.replyKey = key
	return &store.ProductWorkflowRun{
		RunID: "wf-fake", Platform: key.Platform, AccountRef: key.AccountRef,
		Mode: workflow.ModeReplyOnly, Status: workflow.StatusRunning,
	}, nil
}

func (f *fakeWorkflow) Pause() (*store.ProductWorkflowRun, error) {
	f.pauseCalls++
	return &store.ProductWorkflowRun{}, nil
}

func (f *fakeWorkflow) Resume() (*store.ProductWorkflowRun, error) {
	f.resumeCalls++
	f.callOrder = append(f.callOrder, "resume")
	return &store.ProductWorkflowRun{}, nil
}

func (f *fakeWorkflow) End() (*store.ProductWorkflowRun, error) {
	f.endCalls++
	return &store.ProductWorkflowRun{}, nil
}

func (f *fakeWorkflow) ConfirmAll(
	batchID string,
	profileIDs []string,
) (*store.ProductWorkflowRun, error) {
	f.confirmBatch = batchID
	f.confirmIDs = append([]string(nil), profileIDs...)
	return &store.ProductWorkflowRun{}, nil
}

type fakeSource struct {
	raw       []byte
	err       error
	calls     int
	callOrder *[]string

	allRaw   []byte
	allErr   error
	allCalls int
}

func (f *fakeSource) FetchCurrent(context.Context) ([]byte, error) {
	f.calls++
	if f.callOrder != nil {
		*f.callOrder = append(*f.callOrder, "fetch")
	}
	return f.raw, f.err
}

// FetchAll 默认失败:绝大多数既有用例并不关心有效职位集,让它们顺带证明
// 复数同步故障不会阻断开始。需要真实有效集的用例显式给 allRaw。
func (f *fakeSource) FetchAll(context.Context) ([]byte, error) {
	f.allCalls++
	if f.callOrder != nil {
		*f.callOrder = append(*f.callOrder, "fetchAll")
	}
	if f.allErr != nil {
		return nil, f.allErr
	}
	if f.allRaw == nil {
		return nil, errors.New("用例未提供多职位响应")
	}
	return f.allRaw, nil
}

// 全新完整开始(2026-09-01 当日职位计划):先做硬前提的复数同步(计划名单),
// 再尽力而为地刷新当前职位 head 与 provider 凭据,最后交给 StartFullDailyPlan。
func TestFullStartSyncsConfigPlaneThenStartsDailyPlan(t *testing.T) {
	db, key := controllerFixture(t)
	flow := &fakeWorkflow{}
	source := &fakeSource{
		raw: syntheticCurrentJob(t, 42, "产品经理"),
		allRaw: syntheticAllJobs(t, 42, map[int]string{
			42: "产品经理", 43: "客户经理",
		}),
		callOrder: nil,
	}
	source.callOrder = &flow.callOrder
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
	controller, err := New(
		db, flow, source, func() time.Time { return now }, workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background(), "full", "", ""); err != nil {
		t.Fatal(err)
	}
	// 顺序:先回填当前职位 head(尽力而为),后复数同步(名单最终裁决)——
	// 颠倒会让「只加不减」的回填把复数同步剔除的职位重新塞进名单。
	if source.allCalls != 1 || source.calls != 1 || flow.fullKey != key ||
		len(flow.callOrder) != 3 ||
		flow.callOrder[0] != "fetch" ||
		flow.callOrder[1] != "fetchAll" ||
		flow.callOrder[2] != "dailyPlan" {
		t.Fatalf("source=%d/%d key=%+v order=%v",
			source.allCalls, source.calls, flow.fullKey, flow.callOrder)
	}
	revision, err := db.CurrentLegacyJobAIContextByBackendJobID("42")
	if err != nil || revision == nil {
		t.Fatalf("当前职位 head 应照旧落库(主动来聊建档用): %+v err=%v", revision, err)
	}
}

// 仅回复开始也做与「同步职位」相同的尽力全刷(2026-09-01 甲方裁决:所有开始/
// 恢复入口全刷),当前职位 head 随之推进,在聊候选人下一轮即用新配置。
func TestReplyOnlyStartRefreshesConfigPlane(t *testing.T) {
	db, key := controllerFixture(t)
	flow := &fakeWorkflow{}
	source := &fakeSource{raw: syntheticCurrentJob(t, 42, "产品经理")}
	controller, err := New(
		db,
		flow,
		source,
		func() time.Time {
			return time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
		},
		workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background(), "replyOnly", "", ""); err != nil {
		t.Fatal(err)
	}
	if flow.replyKey != key || source.calls != 1 || source.allCalls != 1 {
		t.Fatalf("reply key=%+v source=%d/%d",
			flow.replyKey, source.calls, source.allCalls)
	}
	revision, err := db.CurrentLegacyJobAIContextByBackendJobID("42")
	if err != nil || revision == nil {
		t.Fatalf("仅回复开始应推进当前职位 head: %+v err=%v", revision, err)
	}
}

// 尽力而为:后台整体不可达时仅回复照常开始,不拦按钮。
func TestReplyOnlyStartSurvivesConfigPlaneFailure(t *testing.T) {
	db, key := controllerFixture(t)
	flow := &fakeWorkflow{}
	source := &fakeSource{raw: []byte("backend down")}
	controller, err := New(
		db,
		flow,
		source,
		func() time.Time {
			return time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
		},
		workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background(), "replyOnly", "", ""); err != nil {
		t.Fatal(err)
	}
	if flow.replyKey != key || source.calls != 1 {
		t.Fatalf("reply key=%+v sourceCalls=%d", flow.replyKey, source.calls)
	}
}

// 恢复入口同样尽力全刷;暂停、结束与确认发送仍不触碰配置面。
func TestResumeRefreshesConfigPlaneOtherControlsDoNot(t *testing.T) {
	db, _ := controllerFixture(t)
	flow := &fakeWorkflow{}
	source := &fakeSource{raw: syntheticCurrentJob(t, 42, "产品经理")}
	controller, err := New(
		db,
		flow,
		source,
		func() time.Time {
			return time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
		},
		workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.calls != 0 {
		t.Fatalf("暂停不应触碰配置面: %d", source.calls)
	}
	if err := controller.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if flow.resumeCalls != 1 || source.calls != 1 || source.allCalls != 1 {
		t.Fatalf("恢复应先尽力全刷: resume=%d source=%d/%d",
			flow.resumeCalls, source.calls, source.allCalls)
	}
	revision, err := db.CurrentLegacyJobAIContextByBackendJobID("42")
	if err != nil || revision == nil {
		t.Fatalf("恢复应推进当前职位 head: %+v err=%v", revision, err)
	}
	if err := controller.End(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.ConfirmAll(
		context.Background(), "batch-one", []string{"profile-one"},
	); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || flow.pauseCalls != 1 || flow.endCalls != 1 ||
		flow.confirmBatch != "batch-one" || len(flow.confirmIDs) != 1 {
		t.Fatalf("结束/确认不应再触碰配置面: source=%d flow=%+v", source.calls, flow)
	}
}

func TestClosedWindowClickCannotBecomeAutomaticEightOClockStart(t *testing.T) {
	db, _ := controllerFixture(t)
	flow := &fakeWorkflow{}
	source := &fakeSource{raw: syntheticCurrentJob(t, 42, "产品经理")}
	now := time.Date(2026, 7, 25, 6, 59, 59, 0, time.Local)
	controller, err := New(db, flow, source, func() time.Time {
		captured := now
		now = time.Date(2026, 7, 25, 7, 0, 1, 0, time.Local)
		return captured
	}, workflow.DailyWindowPolicy{})
	if err != nil {
		t.Fatal(err)
	}

	if err := controller.Start(context.Background(), "full", "42", ""); !errors.Is(
		err,
		workflow.ErrDailyWindowClosed,
	) {
		t.Fatalf("closed-window Start() error = %v", err)
	}
	if source.calls != 0 || flow.fullRevision != "" || flow.fullKey.Platform != "" {
		t.Fatalf(
			"closed click crossed boundary: source=%d key=%+v revision=%q",
			source.calls,
			flow.fullKey,
			flow.fullRevision,
		)
	}
}

func TestDevelopmentWindowOverrideUsesRealTimeAndAllowsExplicitStart(t *testing.T) {
	db, key := controllerFixture(t)
	flow := &fakeWorkflow{}
	source := &fakeSource{
		raw:    syntheticCurrentJob(t, 42, "产品经理"),
		allRaw: syntheticAllJobs(t, 42, map[int]string{42: "产品经理"}),
	}
	now := time.Date(2026, 7, 25, 1, 30, 0, 0, time.Local)
	controller, err := New(
		db,
		flow,
		source,
		func() time.Time { return now },
		workflow.DailyWindowPolicy{AllowOutOfWindow: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background(), "full", "42", ""); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || flow.fullKey != key ||
		len(flow.callOrder) == 0 || flow.callOrder[len(flow.callOrder)-1] != "dailyPlan" {
		t.Fatalf("source=%d key=%+v order=%v", source.calls, flow.fullKey, flow.callOrder)
	}
	revision, err := db.CurrentLegacyJobAIContextByBackendJobID("42")
	if err != nil || revision == nil || !revision.CreatedAt.Equal(now) {
		t.Fatalf("override must keep real timestamp: revision=%+v err=%v", revision, err)
	}
}

// 页面带上来的职位 ID 仅兼容接收、一律忽略(2026-09-01 当日职位计划;其前身
// 2026-08-10「跑后台此刻选中的职位」裁决随单职位模式退役)。带旧职位、带错
// 职位都不再触发 ErrJobSelectionChanged,一律按当日计划开跑。
func TestFullStartIgnoresPageJobSelection(t *testing.T) {
	db, key := controllerFixture(t)
	flow := &fakeWorkflow{}
	source := &fakeSource{
		raw:    syntheticCurrentJob(t, 99, "新职位"),
		allRaw: syntheticAllJobs(t, 99, map[int]string{42: "旧职位", 99: "新职位"}),
	}
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
	controller, err := New(
		db, flow, source, func() time.Time { return now }, workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	// 页面带上来的还是旧职位 42:不比对、不拒绝,按当日计划开跑。
	if err := controller.Start(context.Background(), "full", "42", ""); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if flow.fullKey != key ||
		len(flow.callOrder) == 0 || flow.callOrder[len(flow.callOrder)-1] != "dailyPlan" {
		t.Fatalf("应按当日计划开跑: key=%+v order=%v", flow.fullKey, flow.callOrder)
	}
}

func TestFullStartRecoversBoundBatchWithoutFetchingBackend(t *testing.T) {
	db, key := controllerFixture(t)
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
	revisions, err := m5ai.ImportLegacyJobConfigFromBackend(
		syntheticCurrentJob(t, 42, "产品经理"),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.SaveCurrentLegacyJobAIContext(revisions, now)
	if err != nil {
		t.Fatal(err)
	}
	started, err := db.StartSourcingBatch(store.StartSourcingBatchRequest{
		BatchID: "batch-recover-bound", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: stored[0].RevisionHash, TargetCount: 30, StartedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	flow := &fakeWorkflow{}
	// 后台整体不可达:FetchCurrent 返回不可解析内容、FetchAll 报错。
	source := &fakeSource{raw: []byte("backend down"), callOrder: &flow.callOrder}
	controller, err := New(
		db, flow, source, func() time.Time { return now }, workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	// 有未终局批次时,配置面故障不得把恢复堵死:同步照常尝试(锁外读批次不可
	// 作跳过依据),失败后按既有批次收养继续;批次自带的 revision 是不可替换
	// 的事实,收养语义在 StartFullDailyPlan 内部。
	if err := controller.Start(context.Background(), "full", "42", ""); err != nil {
		t.Fatal(err)
	}
	if len(flow.callOrder) == 0 || flow.callOrder[len(flow.callOrder)-1] != "dailyPlan" {
		t.Fatalf("recovery order=%v batch=%+v", flow.callOrder, started.Batch)
	}
}

// 活跃运行期间点开始:先尽力全刷配置面(2026-09-01 甲方裁决:所有开始/恢复
// 入口全刷,让在聊候选人下一轮用上新提示词),再委托 StartFullDailyPlan(真实
// 实现里对 replyOnly 活跃运行报模式冲突、对 full 幂等返回;「沟通期再采一批」
// 的同步-换批语义随当日计划停用)。已冻结批次的 revision 绑定不受刷新影响。
func TestStartWithActiveRunRefreshesConfigPlaneThenDelegates(t *testing.T) {
	db, key := controllerFixture(t)
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
	if _, err := db.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
		RunID:      "wf-reply-only-running",
		Platform:   key.Platform,
		AccountRef: key.AccountRef,
		State: workflow.State{
			Mode: workflow.ModeReplyOnly, Status: workflow.StatusRunning,
		},
		Stage:     store.ProductWorkflowStageCommunication,
		StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	flow := &fakeWorkflow{}
	source := &fakeSource{
		raw:       syntheticCurrentJob(t, 42, "产品经理"),
		callOrder: &flow.callOrder,
	}
	controller, err := New(
		db, flow, source, func() time.Time { return now }, workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := controller.Start(context.Background(), "full", "42", ""); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || source.allCalls != 1 || flow.fullKey != key ||
		len(flow.callOrder) != 3 ||
		flow.callOrder[0] != "fetch" ||
		flow.callOrder[1] != "fetchAll" ||
		flow.callOrder[2] != "dailyPlan" {
		t.Fatalf(
			"active-run start source=%d/%d key=%+v order=%v",
			source.calls, source.allCalls, flow.fullKey, flow.callOrder,
		)
	}
	revision, err := db.CurrentLegacyJobAIContextByBackendJobID("42")
	if err != nil || revision == nil {
		t.Fatalf("接续开始应推进当前职位 head: %+v err=%v", revision, err)
	}
}

func TestStartFromPausedCommunicationDelegatesWithoutResuming(t *testing.T) {
	db, key := controllerFixture(t)
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
	if _, err := db.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
		RunID:      "wf-communication-paused",
		Platform:   key.Platform,
		AccountRef: key.AccountRef,
		State: workflow.State{
			Mode:         workflow.ModeFull,
			Status:       workflow.StatusPaused,
			ResumeStatus: workflow.StatusRunning,
		},
		Stage:     store.ProductWorkflowStageCommunication,
		StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	flow := &fakeWorkflow{}
	source := &fakeSource{
		raw:       syntheticCurrentJob(t, 42, "产品经理"),
		callOrder: &flow.callOrder,
	}
	controller, err := New(
		db, flow, source, func() time.Time { return now }, workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := controller.Start(context.Background(), "full", "42", ""); err != nil {
		t.Fatal(err)
	}
	// 活跃运行在场:先尽力全刷配置面,但不擅自恢复,仍直接委托。
	if source.calls != 1 || source.allCalls != 1 || flow.resumeCalls != 0 ||
		len(flow.callOrder) != 3 ||
		flow.callOrder[0] != "fetch" ||
		flow.callOrder[1] != "fetchAll" ||
		flow.callOrder[2] != "dailyPlan" {
		t.Fatalf("paused delegation source=%d flow=%+v", source.calls, flow)
	}
}

// 全新开始的复数同步是计划名单硬前提(2026-09-01):失败即拒绝开始,不得
// 回落旧单职位路径,也不得带病建计划。
func TestFreshFullStartBlockedWhenPluralSyncFails(t *testing.T) {
	db, _ := controllerFixture(t)
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
	flow := &fakeWorkflow{}
	sourceErr := errors.New("fixture backend unavailable")
	controller, err := New(
		db,
		flow,
		&fakeSource{raw: syntheticCurrentJob(t, 42, "产品经理"), allErr: sourceErr},
		func() time.Time { return now },
		workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := controller.Start(context.Background(), "full", "", ""); !errors.Is(err, sourceErr) ||
		!errors.Is(err, ErrJobConfigUnavailable) {
		t.Fatalf("Start() error=%v", err)
	}
	for _, call := range flow.callOrder {
		if call == "dailyPlan" || call == "full" {
			t.Fatalf("sync failure advanced workflow: %+v", flow)
		}
	}
	if active, activeErr := db.ActiveProductWorkflowRun(); activeErr != nil || active != nil {
		t.Fatalf("sync failure left state: %+v %v", active, activeErr)
	}
}

func TestRuntimeStateUsesDurableWorkflowBatch(t *testing.T) {
	db, key := controllerFixture(t)
	now := time.Now()
	revisions, err := m5ai.ImportLegacyJobConfigFromBackend(
		syntheticCurrentJob(t, 42, "产品经理"), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.SaveCurrentLegacyJobAIContext(revisions, now)
	if err != nil {
		t.Fatal(err)
	}
	started, err := db.StartSourcingBatch(store.StartSourcingBatchRequest{
		BatchID: "batch-runtime", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: stored[0].RevisionHash, TargetCount: 30, StartedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch := started.Batch
	run, err := db.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
		RunID: "wf-runtime", Platform: key.Platform, AccountRef: key.AccountRef,
		State: workflow.State{
			Mode: workflow.ModeFull, Status: workflow.StatusAwaitingConfirmation,
		},
		Stage: store.ProductWorkflowStageAwaitingConfirmation, StartedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AttachProductWorkflowSourcingBatch(run.RunID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	controller, err := New(
		db, &fakeWorkflow{}, &fakeSource{}, time.Now, workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := controller.RuntimeState()
	if err != nil || state.CurrentBatchID != batch.BatchID ||
		state.Platform != key.Platform || state.AccountRef != key.AccountRef ||
		state.WorkflowMode != "full" ||
		state.WorkflowStatus != "awaitingConfirmation" ||
		state.CommunicationState != "idle" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestRuntimeStateAllowsAdditionalBatchFromRunningOrPausedCommunication(t *testing.T) {
	db, key := controllerFixture(t)
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.Local)
	if _, err := db.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
		RunID:      "wf-can-add-batch",
		Platform:   key.Platform,
		AccountRef: key.AccountRef,
		State: workflow.State{
			Mode: workflow.ModeReplyOnly, Status: workflow.StatusRunning,
		},
		Stage:     store.ProductWorkflowStageCommunication,
		StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	controller, err := New(
		db, &fakeWorkflow{}, &fakeSource{}, func() time.Time { return now },
		workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := controller.RuntimeState()
	if err != nil || !state.CanAddBatch || !state.CanEnd ||
		state.WorkflowStage != store.ProductWorkflowStageCommunication {
		t.Fatalf("running communication state=%+v err=%v", state, err)
	}
	if _, err := db.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: "wf-can-add-batch",
		From:  workflow.State{Mode: workflow.ModeReplyOnly, Status: workflow.StatusRunning},
		To:    workflow.State{Mode: workflow.ModeReplyOnly, Status: workflow.StatusPaused, ResumeStatus: workflow.StatusRunning},
		At:    now,
		Stage: store.ProductWorkflowStageCommunication,
	}); err != nil {
		t.Fatal(err)
	}
	state, err = controller.RuntimeState()
	if err != nil || !state.CanAddBatch || !state.CanEnd {
		t.Fatalf("paused communication state=%+v err=%v", state, err)
	}
}

func TestRuntimeStateKeepsAccountAndUnfinishedBatchWithoutWorkflowRun(t *testing.T) {
	db, key := controllerFixture(t)
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.Local)
	revisions, err := m5ai.ImportLegacyJobConfigFromBackend(
		syntheticCurrentJob(t, 42, "产品经理"),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.SaveCurrentLegacyJobAIContext(revisions, now)
	if err != nil {
		t.Fatal(err)
	}
	started, err := db.StartSourcingBatch(store.StartSourcingBatchRequest{
		BatchID: "batch-without-workflow", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: stored[0].RevisionHash, TargetCount: 30, StartedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := New(
		db, &fakeWorkflow{}, &fakeSource{}, func() time.Time { return now },
		workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := controller.RuntimeState()
	if err != nil || state.Platform != key.Platform || state.AccountRef != key.AccountRef ||
		state.CurrentBatchID != started.Batch.BatchID || state.WorkflowMode != "" ||
		state.WorkflowStatus != "" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

// 运行失败即终局、不再是活跃运行,首页否则永远看不到"为什么停了"。投影在无
// 活跃运行时带出最近一次失败运行的原因(2026-08-12 甲方要求,起因是推荐流被
// 刷新后批次静默作废)。
func TestRuntimeStateCarriesLastFailedRunReason(t *testing.T) {
	db, key := controllerFixture(t)
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.Local)
	running := workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning}
	run, err := db.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
		Platform: key.Platform, AccountRef: key.AccountRef,
		State: running, Stage: store.ProductWorkflowStageSourcing, StartedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: running,
		To: workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusFailed},
		At: now.Add(time.Minute), Stage: store.ProductWorkflowStageFailed,
		Failure: "产品工作流批次推进状态无效: recommendationFeedChanged",
	}); err != nil {
		t.Fatal(err)
	}
	controller, err := New(
		db, &fakeWorkflow{}, &fakeSource{}, func() time.Time { return now },
		workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := controller.RuntimeState()
	if err != nil || state.WorkflowStatus != "" ||
		!strings.Contains(state.LastRunFailureReason, "recommendationFeedChanged") {
		t.Fatalf("失败运行原因未带出: state=%+v err=%v", state, err)
	}
}

// 多账号是受支持的形态(账号跟随登录,2026-07-30 裁决):只读投影不再因为库里
// 有两个账号而失明,而是取最近一次身份验证通过的那个。巡检每轮成功探测都会
// 刷新 IdentityVerifiedAt,启发式因此自动收敛到当前真实登录的账号。
func TestRuntimeStatePicksMostRecentlyVerifiedAccount(t *testing.T) {
	db, elder := controllerFixture(t)
	elderVerifiedAt := time.Date(2026, 7, 25, 8, 0, 0, 0, time.Local)
	if err := db.MutateAccount(elder, func(account *store.Account) error {
		account.IdentityVerifiedAt = &elderVerifiedAt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fingerprint := "principal-controller-second"
	recentVerifiedAt := time.Date(2026, 7, 25, 9, 30, 0, 0, time.Local)
	if err := db.CreateAccount(&store.Account{
		Platform: "zhilian", AccountRef: "account-controller-second",
		BoundHandID: "hand-controller-second", PrincipalFingerprint: &fingerprint,
		IdentityState: store.IdentityVerified, IdentityVerifiedAt: &recentVerifiedAt,
	}); err != nil {
		t.Fatal(err)
	}
	controller, err := New(
		db, &fakeWorkflow{}, &fakeSource{}, time.Now, workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := controller.RuntimeState()
	if err != nil || state.AccountRef != "account-controller-second" {
		t.Fatalf("未选中最近验证的账号: state=%+v err=%v", state, err)
	}
}

type fakeWechatReader struct {
	configured bool
	err        error
	calls      int
}

func (f *fakeWechatReader) ReadWechatConfigured(
	context.Context,
	store.AccountKey,
) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.configured, nil
}

type fakeNoticeCollector struct {
	err   error
	calls int
}

func (f *fakeNoticeCollector) CollectNotices(context.Context, store.AccountKey) error {
	f.calls++
	return f.err
}

// 平台通知上报(2026-09-02 甲方裁决):微信闸通过后同步读一次,失败不拦开始;
// 微信闸拦下时不读;已有活跃工作或未终局批次时不读(不得导航去个人中心)。
func TestStartCollectsNoticesBestEffort(t *testing.T) {
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.Local)

	t.Run("collects once after the gate passes and failure does not block", func(t *testing.T) {
		db, key := controllerFixture(t)
		flow := &fakeWorkflow{}
		collector := &fakeNoticeCollector{err: errors.New("通知列表在期限内未装载完成")}
		controller, err := New(
			db, flow, &fakeSource{}, func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller.SetWechatSettingReader(&fakeWechatReader{configured: true})
		controller.SetNoticeCollector(collector)
		if err := controller.Start(context.Background(), "replyOnly", "", ""); err != nil {
			t.Fatalf("通知读取失败不得拦住开始: %v", err)
		}
		if flow.replyKey != key || collector.calls != 1 {
			t.Fatalf("应读一次且照常开始: key=%+v calls=%d", flow.replyKey, collector.calls)
		}
	})

	t.Run("capability missing is skipped with an audit row, not counted as failure", func(t *testing.T) {
		db, key := controllerFixture(t)
		flow := &fakeWorkflow{}
		collector := &fakeNoticeCollector{err: fmt.Errorf("%w: 手未声明原语能力", ErrHandCapabilityMissing)}
		controller, err := New(
			db, flow, &fakeSource{}, func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller.SetWechatSettingReader(&fakeWechatReader{configured: true})
		controller.SetNoticeCollector(collector)
		if err := controller.Start(context.Background(), "replyOnly", "", ""); err != nil {
			t.Fatalf("能力缺失不得拦住开始: %v", err)
		}
		if flow.replyKey != key || collector.calls != 1 {
			t.Fatalf("应读一次且照常开始: key=%+v calls=%d", flow.replyKey, collector.calls)
		}
		entries, _ := db.AuditEntries(20)
		found := false
		for _, entry := range entries {
			if entry.Category == "notice_collect_capability_skipped" && strings.Contains(entry.Detail, "account.readNotices@1") {
				found = true
			}
		}
		if !found {
			t.Fatalf("跳过必须留审计行: %+v", entries)
		}
	})

	t.Run("wechat gate rejection skips the read", func(t *testing.T) {
		db, _ := controllerFixture(t)
		collector := &fakeNoticeCollector{}
		controller, err := New(
			db, &fakeWorkflow{}, &fakeSource{}, func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller.SetWechatSettingReader(&fakeWechatReader{configured: false})
		controller.SetNoticeCollector(collector)
		if err := controller.Start(context.Background(), "replyOnly", "", ""); !errors.Is(err, ErrWechatNotConfigured) {
			t.Fatalf("微信闸应先拦下: %v", err)
		}
		if collector.calls != 0 {
			t.Fatalf("被闸拦下的开始不得读通知: %d", collector.calls)
		}
	})

	t.Run("active workflow run skips the read", func(t *testing.T) {
		db, key := controllerFixture(t)
		if _, err := db.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
			RunID: "wf-notice-skip", Platform: key.Platform, AccountRef: key.AccountRef,
			State: workflow.State{
				Mode: workflow.ModeReplyOnly, Status: workflow.StatusRunning,
			},
			Stage:     store.ProductWorkflowStageCommunication,
			StartedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		collector := &fakeNoticeCollector{}
		controller, err := New(
			db, &fakeWorkflow{}, &fakeSource{raw: syntheticCurrentJob(t, 42, "产品经理")},
			func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller.SetNoticeCollector(collector)
		if err := controller.Start(context.Background(), "full", "42", ""); err != nil {
			t.Fatal(err)
		}
		if collector.calls != 0 {
			t.Fatalf("活跃工作流在场不得导航去读通知: %d", collector.calls)
		}
	})
}

// 微信配置开工闸(2026-08-18 甲方裁决):未配置或读不到都不放行,已有活跃
// 工作时跳过检查(创建那次点击已过闸,且运行期不得导航去个人中心)。
func TestStartGatedOnWechatConfiguration(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.Local)

	t.Run("not configured blocks both modes before any backend fetch", func(t *testing.T) {
		db, _ := controllerFixture(t)
		flow := &fakeWorkflow{}
		source := &fakeSource{raw: []byte("must not fetch")}
		reader := &fakeWechatReader{configured: false}
		controller, err := New(
			db, flow, source, func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller.SetWechatSettingReader(reader)
		if err := controller.Start(context.Background(), "replyOnly", "", ""); !errors.Is(err, ErrWechatNotConfigured) {
			t.Fatalf("replyOnly 未被微信配置闸拦下: %v", err)
		}
		if err := controller.Start(context.Background(), "full", "42", ""); !errors.Is(err, ErrWechatNotConfigured) {
			t.Fatalf("full 未被微信配置闸拦下: %v", err)
		}
		if source.calls != 0 || flow.replyKey != (store.AccountKey{}) || len(flow.callOrder) != 0 {
			t.Fatalf("被拦下的开始不得触达后台或工作流: source=%d flow=%+v", source.calls, flow)
		}
		if reader.calls != 2 {
			t.Fatalf("每次点击都应现查一次: %d", reader.calls)
		}
	})

	t.Run("read failure blocks with its own sentinel, hand sentinels pass through", func(t *testing.T) {
		db, _ := controllerFixture(t)
		reader := &fakeWechatReader{err: errors.New("个人中心读不到")}
		controller, err := New(
			db, &fakeWorkflow{}, &fakeSource{},
			func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller.SetWechatSettingReader(reader)
		if err := controller.Start(context.Background(), "replyOnly", "", ""); !errors.Is(err, ErrWechatCheckFailed) {
			t.Fatalf("读取失败应按检查未完成拒绝: %v", err)
		}
		reader.err = ErrHandUnavailable
		err = controller.Start(context.Background(), "replyOnly", "", "")
		if !errors.Is(err, ErrHandUnavailable) || errors.Is(err, ErrWechatCheckFailed) {
			t.Fatalf("手侧哨兵应原样透传: %v", err)
		}
	})

	t.Run("capability missing skips the gate with an audit row", func(t *testing.T) {
		// 2026-09-02 甲方裁决 2.2:该平台的插件没有 account.readWechatSetting 时,
		// 开工闸"跳过并留痕"放行——它不是检查失败,BOSS 第一刀正是这条路。
		db, key := controllerFixture(t)
		flow := &fakeWorkflow{}
		reader := &fakeWechatReader{err: fmt.Errorf("%w: 手未声明原语能力", ErrHandCapabilityMissing)}
		collector := &fakeNoticeCollector{}
		controller, err := New(
			db, flow, &fakeSource{},
			func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller.SetWechatSettingReader(reader)
		controller.SetNoticeCollector(collector)
		if err := controller.Start(context.Background(), "replyOnly", "", ""); err != nil {
			t.Fatalf("能力缺失应跳过闸放行: %v", err)
		}
		if flow.replyKey != key || reader.calls != 1 || collector.calls != 1 {
			t.Fatalf("应读一次、放行并继续读通知: key=%+v reads=%d notices=%d", flow.replyKey, reader.calls, collector.calls)
		}
		entries, _ := db.AuditEntries(20)
		found := false
		for _, entry := range entries {
			if entry.Category == "wechat_gate_capability_skipped" && strings.Contains(entry.Detail, "account.readWechatSetting@1") {
				found = true
			}
		}
		if !found {
			t.Fatalf("跳过必须留审计行(脑闸拒绝在记账前、cmd_records 无痕): %+v", entries)
		}
	})

	t.Run("configured lets start proceed", func(t *testing.T) {
		db, key := controllerFixture(t)
		flow := &fakeWorkflow{}
		reader := &fakeWechatReader{configured: true}
		controller, err := New(
			db, flow, &fakeSource{},
			func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller.SetWechatSettingReader(reader)
		if err := controller.Start(context.Background(), "replyOnly", "", ""); err != nil {
			t.Fatal(err)
		}
		if flow.replyKey != key || reader.calls != 1 {
			t.Fatalf("已配置应放行且只查一次: key=%+v calls=%d", flow.replyKey, reader.calls)
		}
	})

	t.Run("unfinished batch skips the check", func(t *testing.T) {
		db, key := controllerFixture(t)
		revisions, err := m5ai.ImportLegacyJobConfigFromBackend(
			syntheticCurrentJob(t, 42, "产品经理"), now,
		)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := db.SaveCurrentLegacyJobAIContext(revisions, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.StartSourcingBatch(store.StartSourcingBatchRequest{
			BatchID: "batch-wechat-skip", Platform: key.Platform, AccountRef: key.AccountRef,
			ContextRevisionHash: stored[0].RevisionHash, TargetCount: 30, StartedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		// 即便现在没配,恢复既有批次也不重查:批次创建那次点击已过闸,运行期
		// 导航去个人中心会打断推荐流。
		reader := &fakeWechatReader{configured: false}
		controller, err := New(
			db, &fakeWorkflow{}, &fakeSource{},
			func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller.SetWechatSettingReader(reader)
		if err := controller.Start(context.Background(), "full", "42", ""); err != nil {
			t.Fatal(err)
		}
		if reader.calls != 0 {
			t.Fatalf("恢复既有批次不得触发微信检查: %d", reader.calls)
		}
	})

	t.Run("active workflow run skips the check", func(t *testing.T) {
		db, key := controllerFixture(t)
		if _, err := db.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
			RunID: "wf-wechat-skip", Platform: key.Platform, AccountRef: key.AccountRef,
			State: workflow.State{
				Mode: workflow.ModeReplyOnly, Status: workflow.StatusRunning,
			},
			Stage:     store.ProductWorkflowStageCommunication,
			StartedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		reader := &fakeWechatReader{configured: false}
		controller, err := New(
			db, &fakeWorkflow{}, &fakeSource{raw: syntheticCurrentJob(t, 42, "产品经理")},
			func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller.SetWechatSettingReader(reader)
		if err := controller.Start(context.Background(), "full", "42", ""); err != nil {
			t.Fatal(err)
		}
		if reader.calls != 0 {
			t.Fatalf("活跃工作流追加批次不得触发微信检查: %d", reader.calls)
		}
	})
}

func controllerFixture(t *testing.T) (*store.Store, store.AccountKey) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fingerprint := "principal-controller"
	key := store.AccountKey{Platform: "zhilian", AccountRef: "account-controller"}
	if err := db.CreateAccount(&store.Account{
		Platform: key.Platform, AccountRef: key.AccountRef,
		BoundHandID: "hand-controller", PrincipalFingerprint: &fingerprint,
		IdentityState: store.IdentityVerified,
	}); err != nil {
		t.Fatal(err)
	}
	return db, key
}

// syntheticAllJobs 拼出复数端点的真实顶层形状 {currentJobId,jobs}。
func syntheticAllJobs(t *testing.T, currentJobID int, jobs map[int]string) []byte {
	t.Helper()
	bundles := make([]any, 0, len(jobs))
	for jobID, name := range jobs {
		var bundle map[string]any
		if err := json.Unmarshal(syntheticCurrentJob(t, jobID, name), &bundle); err != nil {
			t.Fatal(err)
		}
		bundles = append(bundles, bundle)
	}
	raw, err := json.Marshal(map[string]any{
		"currentJobId": currentJobID, "jobs": bundles,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFullStartBuildsEffectiveJobSetAndSurvivesPluralFailure(t *testing.T) {
	now := time.Date(2026, 7, 29, 9, 0, 0, 0, time.Local)

	t.Run("plural sync establishes the effective set", func(t *testing.T) {
		db, _ := controllerFixture(t)
		source := &fakeSource{
			raw: syntheticCurrentJob(t, 42, "产品经理"),
			allRaw: syntheticAllJobs(t, 42, map[int]string{
				42: "产品经理", 43: "客户经理",
			}),
		}
		controller, err := New(
			db, &fakeWorkflow{}, source,
			func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := controller.Start(context.Background(), "full", "42", ""); err != nil {
			t.Fatal(err)
		}
		// 非当前职位也必须进有效集，这正是本轮要交付的能力。
		effective, err := db.EffectiveLegacyJobs()
		if err != nil {
			t.Fatal(err)
		}
		if len(effective) != 2 ||
			effective[0].BackendJobID != "42" || effective[0].DisplayName != "产品经理" ||
			effective[1].BackendJobID != "43" || effective[1].DisplayName != "客户经理" {
			t.Fatalf("有效职位集不正确: %+v", effective)
		}
	})

	t.Run("plural failure blocks a fresh start and preserves the prior set", func(t *testing.T) {
		db, _ := controllerFixture(t)
		okSource := &fakeSource{
			raw: syntheticCurrentJob(t, 42, "产品经理"),
			allRaw: syntheticAllJobs(t, 42, map[int]string{
				42: "产品经理",
			}),
		}
		flow := &fakeWorkflow{}
		controller, err := New(
			db, flow, okSource,
			func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := controller.SyncJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		// 2026-09-01 起复数同步是计划名单硬前提:失败拒绝开始,但不清空既有集。
		broken := &fakeSource{
			raw:    syntheticCurrentJob(t, 42, "产品经理"),
			allErr: errors.New("旧后台不可达"),
		}
		blocked, err := New(
			db, flow, broken,
			func() time.Time { return now }, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := blocked.Start(context.Background(), "full", "", ""); !errors.Is(err, ErrJobConfigUnavailable) {
			t.Fatalf("复数同步失败应阻断全新开始: %v", err)
		}
		effective, err := db.EffectiveLegacyJobs()
		if err != nil {
			t.Fatal(err)
		}
		if len(effective) != 1 || effective[0].BackendJobID != "42" {
			t.Fatalf("同步失败不得清空既有有效集: %+v", effective)
		}
	})
}

func syntheticCurrentJob(t *testing.T, jobID int, name string) []byte {
	t.Helper()
	documents := map[string]string{
		"候选人筛选": `{"minScore":5}`,
		"固定规则":  "",
		"固定话术":  `{"fixture":true}`,
		"多轮沟通":  "简历={简历}\n时段={推荐时段}\n历史={对话历史}\n输出={话术_序列}",
		"客户事实库": "fixture://facts",
		"意向判断":  "招呼={招呼语}\n回复={回复}",
		"打分":    "fixture://score",
		"招呼语":   "fixture://greeting",
		"沉默追问":  "fixture://silence",
		"职位筛选":  testfixture.SourcingFiltersDocument,
	}
	block := func(prompt string) map[string]any {
		return map[string]any{"prompt": prompt}
	}
	raw, err := json.Marshal(map[string]any{
		"job":       map[string]any{"id": jobID, "name": name, "environment": "online"},
		"documents": documents,
		"scoring":   block(documents["打分"]), "greeting": block(documents["招呼语"]),
		"communication": block(documents["多轮沟通"]), "intent": block(documents["意向判断"]),
		"silenceFollowup": block(documents["沉默追问"]),
		"facts":           map[string]any{"content": documents["客户事实库"]},
		"fixedPhrases": map[string]any{
			"content": documents["固定话术"], "scenes": map[string]any{},
		},
		"fixedRules":         map[string]any{"content": documents["固定规则"]},
		"filters":            map[string]any{},
		"candidateSelection": map[string]any{"minScore": 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type fakeResolver struct {
	key      store.AccountKey
	err      error
	calls    int
	platform string
}

func (f *fakeResolver) ResolveCurrent(_ context.Context, platform string) (store.AccountKey, error) {
	f.platform = platform
	f.calls++
	if f.err != nil {
		return store.AccountKey{}, f.err
	}
	return f.key, nil
}

// 账号跟随登录:开始用的是解析器探测出的账号,不是库内扫描选中的账号。
func TestStartFollowsResolvedLoginAccount(t *testing.T) {
	db, _ := controllerFixture(t)
	flow := &fakeWorkflow{}
	resolved := store.AccountKey{Platform: "zhilian", AccountRef: "account-resolved-by-login"}
	resolver := &fakeResolver{key: resolved}
	controller, err := New(
		db, flow, &fakeSource{}, func() time.Time {
			return time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
		}, workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	controller.SetAccountResolver(resolver)
	if err := controller.Start(context.Background(), "replyOnly", "", ""); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || flow.replyKey != resolved {
		t.Fatalf("resolver.calls=%d replyKey=%+v", resolver.calls, flow.replyKey)
	}
}

func TestStartPropagatesResolverSentinels(t *testing.T) {
	db, _ := controllerFixture(t)
	ambiguous := &PlatformAmbiguousError{Platforms: []string{"zhilian", "boss"}}
	for _, sentinel := range []error{ErrHandUnavailable, ErrHandAmbiguous, ErrLoginRequired, ambiguous} {
		flow := &fakeWorkflow{}
		controller, err := New(
			db, flow, &fakeSource{}, func() time.Time {
				return time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
			}, workflow.DailyWindowPolicy{},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller.SetAccountResolver(&fakeResolver{err: sentinel})
		startErr := controller.Start(context.Background(), "replyOnly", "", "")
		if !errors.Is(startErr, sentinel) {
			t.Fatalf("sentinel %v 未透传: %v", sentinel, startErr)
		}
		if sentinel == ambiguous && !errors.Is(startErr, ErrPlatformAmbiguous) {
			t.Fatalf("歧义错误应命中 ErrPlatformAmbiguous 哨兵: %v", startErr)
		}
		if flow.replyKey != (store.AccountKey{}) {
			t.Fatalf("解析失败仍启动了工作流: %+v", flow.replyKey)
		}
	}
}

// 当前职位导入失败必须在脑日志可定位(2026-08-01 真机装机卡在新客户配置不合格,
// 而失败原因哪里都没记)。断言日志包含入口/阶段与导入错误里的文档类型名。
func TestSyncJobsLogsImportFailureReason(t *testing.T) {
	db, _ := controllerFixture(t)
	// 缺"多轮沟通"等必需文档的整包:构造真实的导入失败。
	source := &fakeSource{raw: []byte(`{"job":{"id":9,"name":"职位九","environment":"online"},"documents":{"打分":"p"}}`)}
	controller, err := New(db, &fakeWorkflow{}, source, time.Now, workflow.DailyWindowPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(previous)
	if err := controller.SyncJobs(context.Background()); !errors.Is(err, ErrJobConfigUnavailable) {
		t.Fatalf("导入失败未按不可用返回: %v", err)
	}
	logged := buf.String()
	// 该整包命中的第一条校验是"documents 与结构化区冲突: 打分";断言点在于
	// 具体文档名到达日志,而不是命中哪条校验。
	if !strings.Contains(logged, "当前职位同步失败") ||
		!strings.Contains(logged, "stage=import") ||
		!strings.Contains(logged, "打分") {
		t.Fatalf("失败原因未进日志: %s", logged)
	}
}

// 后台「当前职位」已从职位列表删除时,开始路径的 head 回填不得把它塞回有效
// 集——复数同步是名单的最终裁决(2026-09-01 审查修复:同步顺序回填在前)。
func TestFullStartExcludesCurrentJobDroppedFromPluralSync(t *testing.T) {
	db, _ := controllerFixture(t)
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.Local)
	flow := &fakeWorkflow{}
	source := &fakeSource{
		raw:    syntheticCurrentJob(t, 42, "已删职位"),
		allRaw: syntheticAllJobs(t, 43, map[int]string{43: "客户经理"}),
	}
	controller, err := New(
		db, flow, source, func() time.Time { return now }, workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background(), "full", "", ""); err != nil {
		t.Fatal(err)
	}
	effective, err := db.EffectiveLegacyJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != 1 || effective[0].BackendJobID != "43" {
		t.Fatalf("已删职位不得进有效集/计划名单: %+v", effective)
	}
}

// 开始入口的 platform 参数(2026-09-02 批 D 2.1):透传给解析器;不合法拒绝;运行中忽略。
func TestStartPassesPlatformToResolverAndRejectsInvalid(t *testing.T) {
	db, key := controllerFixture(t)
	flow := &fakeWorkflow{}
	resolver := &fakeResolver{key: key}
	controller, err := New(
		db, flow, &fakeSource{}, func() time.Time {
			return time.Date(2026, 9, 2, 9, 0, 0, 0, time.Local)
		}, workflow.DailyWindowPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	controller.SetAccountResolver(resolver)
	if err := controller.Start(context.Background(), "replyOnly", "", " boss "); err != nil {
		t.Fatalf("合法平台应放行: %v", err)
	}
	if resolver.platform != "boss" {
		t.Fatalf("platform 应去首尾空白后透传给解析器: %q", resolver.platform)
	}
	if err := controller.Start(context.Background(), "replyOnly", "", "bo ss"); !errors.Is(err, ErrPlatformInvalid) {
		t.Fatalf("含空白的平台标识应拒绝: %v", err)
	}
	if err := controller.Start(context.Background(), "replyOnly", "", strings.Repeat("b", 65)); !errors.Is(err, ErrPlatformInvalid) {
		t.Fatalf("超长平台标识应拒绝: %v", err)
	}
}
