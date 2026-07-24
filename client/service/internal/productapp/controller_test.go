package productapp

import (
	"context"
	"encoding/json"
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
	confirmBatch string
	confirmIDs   []string
}

func (f *fakeWorkflow) StartFull(
	key store.AccountKey,
	revision string,
) (*store.ProductWorkflowRun, error) {
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
	raw   []byte
	calls int
}

func (f *fakeSource) FetchCurrent(context.Context) ([]byte, error) {
	f.calls++
	return f.raw, nil
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
	if err := controller.Start(context.Background(), "full"); err != nil {
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
	controller, err := New(db, flow, source, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background(), "replyOnly"); err != nil {
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
	if err := controller.ConfirmAll(
		context.Background(), "batch-one", []string{"profile-one"},
	); err != nil {
		t.Fatal(err)
	}
	if flow.pauseCalls != 1 || flow.resumeCalls != 1 ||
		flow.confirmBatch != "batch-one" || len(flow.confirmIDs) != 1 {
		t.Fatalf("unexpected controls: %+v", flow)
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
		state.WorkflowMode != "full" ||
		state.WorkflowStatus != "awaitingConfirmation" ||
		state.CommunicationState != "idle" {
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
