package productworkflow

import (
	"errors"
	"sort"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/testfixture"
	"recruithelper/client/service/internal/workflow"
)

type fixtureClock struct{ now time.Time }

func (c *fixtureClock) Now() time.Time { return c.now }

type fixtureActor struct {
	store        *store.Store
	clock        *fixtureClock
	startTargets []int
	enableCalls  int
	pauseCalls   int
	gate         func() error
	startErr     error
	enableErr    error
}

func (a *fixtureActor) StartSourcing(key store.AccountKey, revision string, target int) error {
	a.startTargets = append(a.startTargets, target)
	if a.startErr != nil {
		return a.startErr
	}
	_, err := a.store.StartSourcingBatch(store.StartSourcingBatchRequest{
		Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revision, TargetCount: target, StartedAt: a.clock.Now(),
	})
	return err
}

func (a *fixtureActor) EnableToday(key store.AccountKey) error {
	a.enableCalls++
	if a.enableErr != nil {
		return a.enableErr
	}
	now := a.clock.Now()
	return a.store.MutateAccount(key, func(account *store.Account) error {
		account.EnabledDate = now.Format("2006-01-02")
		account.EnabledAt = &now
		account.StoppedAt = nil
		account.PausedReason = ""
		return nil
	})
}

func (a *fixtureActor) PauseNow(store.AccountKey) error {
	a.pauseCalls++
	return nil
}

func (a *fixtureActor) SetWorkflowMemberGate(gate func() error) { a.gate = gate }

func TestFullWorkflowPersistsPauseResumeAndExplicitDailyWindowRecovery(t *testing.T) {
	db, key, revision := productWorkflowFixture(t)
	location := time.FixedZone("CST", 8*60*60)
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	actor := &fixtureActor{store: db, clock: clock}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	if actor.gate == nil {
		t.Fatal("manager did not install the shared member gate")
	}

	run, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil || run.Mode != workflow.ModeFull || run.Status != workflow.StatusRunning ||
		run.SourcingBatchID == nil || len(actor.startTargets) != 1 ||
		actor.startTargets[0] != NewFullWorkflowTargetCount {
		t.Fatalf("StartFull() = %+v, targets=%v, err=%v", run, actor.startTargets, err)
	}
	replayed, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil || replayed.RunID != run.RunID || len(actor.startTargets) != 1 {
		t.Fatalf("replayed StartFull() = %+v, targets=%v, err=%v", replayed, actor.startTargets, err)
	}

	paused, err := manager.Pause()
	if err != nil || paused.Status != workflow.StatusPaused || actor.pauseCalls != 1 {
		t.Fatalf("Pause() = %+v, pauseCalls=%d, err=%v", paused, actor.pauseCalls, err)
	}
	if err := actor.gate(); !errors.Is(err, ErrMemberStartBlocked) {
		t.Fatalf("paused member gate error = %v", err)
	}
	resumed, err := manager.Resume()
	if err != nil || resumed.Status != workflow.StatusRunning || actor.enableCalls != 1 {
		t.Fatalf("Resume() = %+v, enableCalls=%d, err=%v", resumed, actor.enableCalls, err)
	}
	if err := actor.gate(); err != nil {
		t.Fatalf("open resumed member gate: %v", err)
	}

	clock.now = time.Date(2026, 7, 26, 0, 0, 0, 0, location)
	if err := actor.gate(); !errors.Is(err, ErrMemberStartBlocked) {
		t.Fatalf("midnight member gate error = %v", err)
	}
	waiting, err := db.ActiveProductWorkflowRun()
	if err != nil || waiting == nil ||
		waiting.Status != workflow.StatusWaitingDailyWindow ||
		waiting.ResumeStatus != workflow.StatusRunning {
		t.Fatalf("midnight state = %+v, %v", waiting, err)
	}
	clock.now = time.Date(2026, 7, 26, 8, 0, 0, 0, location)
	if err := actor.gate(); !errors.Is(err, ErrMemberStartBlocked) {
		t.Fatalf("08:00 must not auto-resume: %v", err)
	}
	resumed, err = manager.Resume()
	if err != nil || resumed.Status != workflow.StatusRunning || actor.enableCalls != 2 {
		t.Fatalf("explicit next-day Resume() = %+v calls=%d err=%v", resumed, actor.enableCalls, err)
	}
}

func TestFullWorkflowAdoptsUnfinishedBatchTargetInsteadOfCreatingThirty(t *testing.T) {
	db, key, revision := productWorkflowFixture(t)
	location := time.UTC
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	if _, err := db.StartSourcingBatch(store.StartSourcingBatchRequest{
		BatchID: "existing-seven", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revision.RevisionHash, TargetCount: 7, StartedAt: clock.now,
	}); err != nil {
		t.Fatal(err)
	}
	actor := &fixtureActor{store: db, clock: clock}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	run, err := manager.StartFull(key, "caller-must-not-replace-existing-revision")
	if err != nil || run.SourcingBatchID == nil || *run.SourcingBatchID != "existing-seven" ||
		len(actor.startTargets) != 1 || actor.startTargets[0] != 7 {
		t.Fatalf("adopt existing batch = %+v targets=%v err=%v", run, actor.startTargets, err)
	}
	batch, err := db.ActiveSourcingBatch(key)
	if err != nil || batch == nil || batch.TargetCount != 7 ||
		batch.ContextRevisionHash != revision.RevisionHash {
		t.Fatalf("existing batch mutated = %+v, %v", batch, err)
	}
}

func TestReplyOnlyAndClosedWindowNeverCreateSourcingBatchOrReservation(t *testing.T) {
	db, key, _ := productWorkflowFixture(t)
	location := time.UTC
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 7, 59, 59, 0, location)}
	actor := &fixtureActor{store: db, clock: clock}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	if run, err := manager.StartReplyOnly(key); run != nil ||
		!errors.Is(err, workflow.ErrDailyWindowClosed) {
		t.Fatalf("closed StartReplyOnly() = %+v, %v", run, err)
	}
	if active, err := db.ActiveProductWorkflowRun(); err != nil || active != nil {
		t.Fatalf("closed click persisted reservation: %+v, %v", active, err)
	}

	clock.now = time.Date(2026, 7, 25, 8, 0, 0, 0, location)
	run, err := manager.StartReplyOnly(key)
	if err != nil || run.Mode != workflow.ModeReplyOnly ||
		run.Stage != store.ProductWorkflowStageCommunication || actor.enableCalls != 1 {
		t.Fatalf("StartReplyOnly() = %+v enable=%d err=%v", run, actor.enableCalls, err)
	}
	if batch, err := db.ActiveSourcingBatch(key); err != nil || batch != nil {
		t.Fatalf("reply-only created sourcing batch: %+v, %v", batch, err)
	}
}

func TestFullStartAfterReplyOnlyCommunicationCreatesOneNewBatch(t *testing.T) {
	db, key, revision := productWorkflowFixture(t)
	location := time.UTC
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	actor := &fixtureActor{store: db, clock: clock}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	communication, err := manager.StartReplyOnly(key)
	if err != nil {
		t.Fatal(err)
	}

	additional, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil ||
		additional == nil ||
		additional.RunID == communication.RunID ||
		additional.Stage != store.ProductWorkflowStageSourcing ||
		additional.SourcingBatchID == nil ||
		len(actor.startTargets) != 1 {
		t.Fatalf(
			"additional=%+v startTargets=%v err=%v",
			additional,
			actor.startTargets,
			err,
		)
	}
	historical, err := db.ProductWorkflowRunByID(communication.RunID)
	if err != nil ||
		historical == nil ||
		historical.Status != workflow.StatusCompleted ||
		historical.Stage != store.ProductWorkflowStageCompleted ||
		historical.ActiveSlot != nil {
		t.Fatalf("historical communication=%+v err=%v", historical, err)
	}

	replayed, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil ||
		replayed == nil ||
		replayed.RunID != additional.RunID ||
		len(actor.startTargets) != 1 {
		t.Fatalf(
			"replayed additional=%+v startTargets=%v err=%v",
			replayed,
			actor.startTargets,
			err,
		)
	}
}

func TestReplyOnlyRejectsUnfinishedSourcingBatch(t *testing.T) {
	db, key, revision := productWorkflowFixture(t)
	location := time.UTC
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	if _, err := db.StartSourcingBatch(store.StartSourcingBatchRequest{
		BatchID: "batch-block-reply-only", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revision.RevisionHash,
		TargetCount:         NewFullWorkflowTargetCount,
		StartedAt:           clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	actor := &fixtureActor{store: db, clock: clock}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	run, err := manager.StartReplyOnly(key)
	if run != nil || !errors.Is(err, ErrSourcingBatchActive) {
		t.Fatalf("StartReplyOnly() = %+v, %v", run, err)
	}
	if actor.enableCalls != 0 {
		t.Fatalf("未终局采集批次存在时不得开启账号: %d", actor.enableCalls)
	}
	if active, loadErr := db.ActiveProductWorkflowRun(); loadErr != nil || active != nil {
		t.Fatalf("拒绝后不得留下工作流: active=%+v err=%v", active, loadErr)
	}
}

func TestResumeActorFailureRollsBackDurableMemberGate(t *testing.T) {
	db, key, _ := productWorkflowFixture(t)
	location := time.UTC
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	actor := &fixtureActor{store: db, clock: clock}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartReplyOnly(key); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Pause(); err != nil {
		t.Fatal(err)
	}
	actor.enableErr = errors.New("fixture enable failed")
	if run, err := manager.Resume(); run != nil || !errors.Is(err, actor.enableErr) {
		t.Fatalf("failed Resume() = %+v, %v", run, err)
	}
	active, err := db.ActiveProductWorkflowRun()
	if err != nil || active == nil || active.Status != workflow.StatusPaused ||
		active.ResumeStatus != workflow.StatusRunning {
		t.Fatalf("resume failure did not roll back: %+v, %v", active, err)
	}
	if err := actor.gate(); !errors.Is(err, ErrMemberStartBlocked) {
		t.Fatalf("rolled-back member gate error = %v", err)
	}
}

func productWorkflowFixture(
	t *testing.T,
) (*store.Store, store.AccountKey, m5ai.ContextRevision) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key := store.AccountKey{Platform: "zhilian", AccountRef: "account-product-workflow"}
	if err := db.CreateAccount(&store.Account{
		Platform: key.Platform, AccountRef: key.AccountRef,
	}); err != nil {
		t.Fatal(err)
	}
	revision := productWorkflowRevision(time.Date(2026, 7, 25, 7, 0, 0, 0, time.UTC))
	if _, err := db.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{revision}, revision.CreatedAt,
	); err != nil {
		t.Fatal(err)
	}
	return db, key, revision
}

func productWorkflowRevision(at time.Time) m5ai.ContextRevision {
	documents := []m5ai.JobConfigDocument{
		{DocType: "候选人筛选", Content: `{"minScore":5,"targetMin":1,"targetMax":30,"maleRatioLimit":50}`},
		{DocType: "多轮沟通", Content: "reply"},
		{DocType: "客户事实库", Content: "facts"},
		{DocType: "意向判断", Content: "intent"},
		{DocType: "打分", Content: "score {resume_json}"},
		{DocType: "招呼语", Content: `{"prompt":"{career_state} {resume_summary_json}"}`},
		{DocType: "职位筛选", Content: testfixture.SourcingFiltersDocument},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	return m5ai.ContextRevision{
		ContextID: "context-product-workflow", RevisionHash: "revision-product-workflow",
		SourceKind: "legacyJobConfig", SourceJobRef: "88", DisplayName: "合成职位",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: "reply", IntentPrompt: "intent", CustomerFacts: "facts",
			MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: at,
	}
}
