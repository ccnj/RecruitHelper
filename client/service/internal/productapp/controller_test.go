package productapp

import (
	"context"
	"encoding/json"
	"errors"
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
}

func (f *fakeSource) FetchCurrent(context.Context) ([]byte, error) {
	f.calls++
	if f.callOrder != nil {
		*f.callOrder = append(*f.callOrder, "fetch")
	}
	return f.raw, f.err
}

func TestFullStartSynchronizesExactlyOneBackendJobBeforeWorkflow(t *testing.T) {
	db, key := controllerFixture(t)
	flow := &fakeWorkflow{}
	source := &fakeSource{raw: syntheticCurrentJob(t, 42, "产品经理")}
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
	controller, err := New(db, flow, source, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background(), "full", "42"); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || flow.fullKey != key || flow.fullRevision == "" {
		t.Fatalf("source=%d key=%+v revision=%q", source.calls, flow.fullKey, flow.fullRevision)
	}
	revision, err := db.CurrentLegacyJobAIContextByBackendJobID("42")
	if err != nil || revision == nil || revision.RevisionHash != flow.fullRevision {
		t.Fatalf("persisted revision=%+v err=%v", revision, err)
	}
}

func TestReplyOnlyAndControlsNeverFetchJobConfig(t *testing.T) {
	db, key := controllerFixture(t)
	flow := &fakeWorkflow{}
	source := &fakeSource{raw: []byte("must not read")}
	controller, err := New(
		db,
		flow,
		source,
		func() time.Time {
			return time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background(), "replyOnly", ""); err != nil {
		t.Fatal(err)
	}
	if flow.replyKey != key || source.calls != 0 {
		t.Fatalf("reply key=%+v sourceCalls=%d", flow.replyKey, source.calls)
	}
	if err := controller.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.End(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.ConfirmAll(
		context.Background(), "batch-one", []string{"profile-one"},
	); err != nil {
		t.Fatal(err)
	}
	if flow.pauseCalls != 1 || flow.resumeCalls != 1 || flow.endCalls != 1 ||
		flow.confirmBatch != "batch-one" || len(flow.confirmIDs) != 1 {
		t.Fatalf("unexpected controls: %+v", flow)
	}
}

func TestClosedWindowClickCannotBecomeAutomaticEightOClockStart(t *testing.T) {
	db, _ := controllerFixture(t)
	flow := &fakeWorkflow{}
	source := &fakeSource{raw: syntheticCurrentJob(t, 42, "产品经理")}
	now := time.Date(2026, 7, 25, 7, 59, 59, 0, time.Local)
	controller, err := New(db, flow, source, func() time.Time {
		captured := now
		now = time.Date(2026, 7, 25, 8, 0, 1, 0, time.Local)
		return captured
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := controller.Start(context.Background(), "full", "42"); !errors.Is(
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

func TestFullStartRejectsChangedBackendJobAfterSavingLatestHead(t *testing.T) {
	db, _ := controllerFixture(t)
	flow := &fakeWorkflow{}
	source := &fakeSource{raw: syntheticCurrentJob(t, 99, "新职位")}
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
	controller, err := New(db, flow, source, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background(), "full", "42"); !errors.Is(
		err,
		ErrJobSelectionChanged,
	) {
		t.Fatalf("Start() error = %v", err)
	}
	if flow.fullRevision != "" {
		t.Fatalf("职位变化不得创建工作流: %+v", flow)
	}
	current, err := db.CurrentLegacyJobAIContextByBackendJobID("99")
	if err != nil || current == nil || current.DisplayName != "新职位" {
		t.Fatalf("最新职位仍应保存供页面下一轮刷新: current=%+v err=%v", current, err)
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
	source := &fakeSource{raw: []byte("must not fetch")}
	controller, err := New(db, flow, source, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background(), "full", "42"); err != nil {
		t.Fatal(err)
	}
	if source.calls != 0 || flow.fullRevision != started.Batch.ContextRevisionHash {
		t.Fatalf(
			"recovery fetched=%d revision=%q batch=%+v",
			source.calls,
			flow.fullRevision,
			started.Batch,
		)
	}
	if err := controller.Start(context.Background(), "full", "99"); !errors.Is(
		err,
		ErrJobSelectionChanged,
	) {
		t.Fatalf("错误职位不得接管既有批次: %v", err)
	}
}

func TestAdditionalBatchRefreshesCurrentBackendJobConfig(t *testing.T) {
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
	source := &fakeSource{raw: syntheticCurrentJob(t, 42, "产品经理")}
	controller, err := New(db, flow, source, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	if err := controller.Start(context.Background(), "full", "42"); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || flow.fullKey != key || flow.fullRevision == "" {
		t.Fatalf(
			"additional source=%d key=%+v revision=%q",
			source.calls,
			flow.fullKey,
			flow.fullRevision,
		)
	}
}

func TestAdditionalBatchFromPausedCommunicationQueuesWithoutResuming(t *testing.T) {
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
	controller, err := New(db, flow, source, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	if err := controller.Start(context.Background(), "full", "42"); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || flow.resumeCalls != 0 ||
		len(flow.callOrder) != 2 ||
		flow.callOrder[0] != "fetch" ||
		flow.callOrder[1] != "full" {
		t.Fatalf("paused additional source=%d flow=%+v", source.calls, flow)
	}
}

func TestAdditionalBatchSyncFailureKeepsCommunicationPaused(t *testing.T) {
	db, key := controllerFixture(t)
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
	if _, err := db.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
		RunID:      "wf-paused-sync-failure",
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
	sourceErr := errors.New("fixture backend unavailable")
	controller, err := New(
		db,
		flow,
		&fakeSource{err: sourceErr},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := controller.Start(context.Background(), "full", "42"); !errors.Is(err, sourceErr) {
		t.Fatalf("Start() error=%v", err)
	}
	if flow.resumeCalls != 0 || flow.fullRevision != "" || len(flow.callOrder) != 0 {
		t.Fatalf("sync failure advanced workflow: %+v", flow)
	}
	active, err := db.ActiveProductWorkflowRun()
	if err != nil || active == nil || active.Status != workflow.StatusPaused ||
		active.Stage != store.ProductWorkflowStageCommunication {
		t.Fatalf("paused run changed after sync failure: run=%+v err=%v", active, err)
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
	controller, err := New(db, &fakeWorkflow{}, &fakeSource{}, time.Now)
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
	controller, err := New(db, &fakeWorkflow{}, &fakeSource{}, func() time.Time { return now })
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
	controller, err := New(db, &fakeWorkflow{}, &fakeSource{}, func() time.Time { return now })
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

func TestRuntimeStateFailsClosedWhenNoUniqueAccountExists(t *testing.T) {
	db, _ := controllerFixture(t)
	fingerprint := "principal-controller-second"
	if err := db.CreateAccount(&store.Account{
		Platform: "zhilian", AccountRef: "account-controller-second",
		BoundHandID: "hand-controller-second", PrincipalFingerprint: &fingerprint,
		IdentityState: store.IdentityVerified,
	}); err != nil {
		t.Fatal(err)
	}
	controller, err := New(db, &fakeWorkflow{}, &fakeSource{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	state, err := controller.RuntimeState()
	if err != nil || state.Platform != "" || state.AccountRef != "" ||
		state.CurrentBatchID != "" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
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
