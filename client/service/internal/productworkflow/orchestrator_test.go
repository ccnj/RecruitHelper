package productworkflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
)

type fixturePipelineActor struct {
	*fixtureActor
	scoreProgress    *store.SourcingBatchScoringProgress
	greetingProgress *store.SourcingBatchGreetingProgress
	sendProgress     *store.SourcingBatchGreetingSendProgress
	scoreErr         error
	greetingErr      error
	sendErr          error
	scoreCalls       int
	greetingCalls    int
	sendCalls        int
}

func (a *fixturePipelineActor) ScoreCompletedSourcingBatch(
	context.Context,
	string,
) (*store.SourcingBatchScoringProgress, error) {
	a.scoreCalls++
	return a.scoreProgress, a.scoreErr
}

func (a *fixturePipelineActor) GenerateSelectedSourcingGreetings(
	context.Context,
	string,
) (*store.SourcingBatchGreetingProgress, error) {
	a.greetingCalls++
	return a.greetingProgress, a.greetingErr
}

func (a *fixturePipelineActor) SendSelectedSourcingGreetings(
	context.Context,
	string,
) (*store.SourcingBatchGreetingSendProgress, error) {
	a.sendCalls++
	return a.sendProgress, a.sendErr
}

func TestGreetingGenerationStopsAtHumanConfirmationWithoutSending(t *testing.T) {
	manager, actor, db, run, batchID := orchestratorFixtureAtGreetingGeneration(t)
	actor.greetingProgress = &store.SourcingBatchGreetingProgress{Completed: true}
	actor.sendProgress = &store.SourcingBatchGreetingSendProgress{Completed: true}
	manager.confirmationProjection = fixtureConfirmationProjection(batchID)

	awaiting, err := manager.AdvanceOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if awaiting.RunID != run.RunID ||
		awaiting.Status != workflow.StatusAwaitingConfirmation ||
		awaiting.Stage != store.ProductWorkflowStageAwaitingConfirmation {
		t.Fatalf("AdvanceOnce() = %+v", awaiting)
	}
	if actor.greetingCalls != 1 || actor.sendCalls != 0 {
		t.Fatalf("generation=%d send=%d; generation must never authorize send",
			actor.greetingCalls, actor.sendCalls)
	}
	persisted, err := db.ActiveProductWorkflowRun()
	if err != nil || persisted == nil ||
		persisted.Status != workflow.StatusAwaitingConfirmation {
		t.Fatalf("persisted awaiting confirmation = %+v, %v", persisted, err)
	}
}

func TestGreetingGenerationWithNoSendableCandidateSkipsEmptyConfirmation(t *testing.T) {
	manager, actor, _, run, batchID := orchestratorFixtureAtGreetingGeneration(t)
	actor.greetingProgress = &store.SourcingBatchGreetingProgress{Completed: true}
	manager.confirmationProjection = func(requested string) (*store.AppConfirmationProjection, error) {
		if requested != batchID {
			return nil, store.ErrAppProjectionInvalid
		}
		return &store.AppConfirmationProjection{
			Available: true,
			Ready:     true,
			BatchID:   batchID,
			Candidates: []store.AppConfirmationCandidate{
				{ProfileID: "profile-failed", Status: "generationFailed"},
			},
			GenerationFailed: 1,
		}, nil
	}

	communication, err := manager.AdvanceOnce(context.Background())
	if err != nil ||
		communication.RunID != run.RunID ||
		communication.Status != workflow.StatusRunning ||
		communication.Stage != store.ProductWorkflowStageCommunication ||
		actor.greetingCalls != 1 ||
		actor.sendCalls != 0 {
		t.Fatalf(
			"zero-sendable generation = %+v greeting=%d send=%d err=%v",
			communication,
			actor.greetingCalls,
			actor.sendCalls,
			err,
		)
	}
}

func TestAwaitingConfirmationClosesWhenFeedChangeLeavesNoSendableCandidate(t *testing.T) {
	manager, actor, db, run, batchID := orchestratorFixtureAtGreetingGeneration(t)
	actor.greetingProgress = &store.SourcingBatchGreetingProgress{Completed: true}

	awaiting, err := manager.AdvanceOnce(context.Background())
	if err != nil ||
		awaiting.RunID != run.RunID ||
		awaiting.Status != workflow.StatusAwaitingConfirmation ||
		awaiting.Stage != store.ProductWorkflowStageAwaitingConfirmation {
		t.Fatalf("enter awaiting confirmation = %+v, %v", awaiting, err)
	}
	manager.confirmationProjection = func(requested string) (*store.AppConfirmationProjection, error) {
		if requested != batchID {
			return nil, store.ErrAppProjectionInvalid
		}
		return &store.AppConfirmationProjection{
			Available: true,
			Ready:     true,
			BatchID:   batchID,
			Candidates: []store.AppConfirmationCandidate{
				{ProfileID: "profile-a", Status: "abandoned"},
				{ProfileID: "profile-b", Status: "abandoned"},
				{ProfileID: "profile-c", Status: "generationFailed"},
			},
			GenerationFailed: 1,
		}, nil
	}

	communication, err := manager.AdvanceOnce(context.Background())
	if err != nil ||
		communication.RunID != run.RunID ||
		communication.Status != workflow.StatusRunning ||
		communication.Stage != store.ProductWorkflowStageCommunication ||
		actor.sendCalls != 0 {
		t.Fatalf(
			"close empty confirmation = %+v send=%d err=%v",
			communication,
			actor.sendCalls,
			err,
		)
	}
	persisted, err := db.ActiveProductWorkflowRun()
	if err != nil ||
		persisted == nil ||
		persisted.Status != workflow.StatusRunning ||
		persisted.Stage != store.ProductWorkflowStageCommunication {
		t.Fatalf("persisted communication = %+v, %v", persisted, err)
	}
}

func TestBlockedSourcingFailsRunAndExplicitRestartAdoptsSameBatch(t *testing.T) {
	db, key, revision := productWorkflowFixture(t)
	location := time.FixedZone("CST", 8*60*60)
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	actor := &fixturePipelineActor{
		fixtureActor: &fixtureActor{store: db, clock: clock},
	}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil || first.SourcingBatchID == nil {
		t.Fatalf("StartFull() = %+v, %v", first, err)
	}
	batchID := *first.SourcingBatchID
	if _, err := db.BlockSourcingBatch(store.BlockSourcingBatchRequest{
		BatchID: batchID, Reason: "windowNoProgress", BlockedAt: clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	failed, err := manager.AdvanceOnce(context.Background())
	if !errors.Is(err, ErrWorkflowPipelineInvalid) ||
		failed == nil ||
		failed.RunID != first.RunID ||
		failed.Status != workflow.StatusFailed ||
		failed.Stage != store.ProductWorkflowStageFailed {
		t.Fatalf("blocked AdvanceOnce() = %+v, %v", failed, err)
	}
	if active, err := db.ActiveProductWorkflowRun(); err != nil || active != nil {
		t.Fatalf("failed run retained active slot: %+v, %v", active, err)
	}

	restarted, err := manager.StartFull(key, "caller-must-not-replace-revision")
	if err != nil ||
		restarted == nil ||
		restarted.RunID == first.RunID ||
		restarted.SourcingBatchID == nil ||
		*restarted.SourcingBatchID != batchID {
		t.Fatalf("restart = %+v, %v", restarted, err)
	}
	batch, err := db.SourcingBatchByID(batchID)
	if err != nil ||
		batch == nil ||
		batch.ContextRevisionHash != revision.RevisionHash ||
		batch.TargetCount != NewFullWorkflowTargetCount ||
		batch.Status != store.SourcingBatchPreparing {
		t.Fatalf("restarted batch = %+v, %v", batch, err)
	}
}

func TestAdvanceOnceProjectsAccountPauseAndResumesSameFullRunAndBatch(t *testing.T) {
	db, key, revision := productWorkflowFixture(t)
	location := time.FixedZone("CST", 8*60*60)
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	actor := &fixturePipelineActor{
		fixtureActor: &fixtureActor{store: db, clock: clock},
	}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil || started.SourcingBatchID == nil {
		t.Fatalf("StartFull() = %+v, %v", started, err)
	}
	batchID := *started.SourcingBatchID
	if err := db.MutateAccount(key, func(account *store.Account) error {
		account.StoppedAt = &clock.now
		account.PausedReason = "handManualReview"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	paused, err := manager.AdvanceOnce(context.Background())
	if err != nil ||
		paused == nil ||
		paused.RunID != started.RunID ||
		paused.Status != workflow.StatusPaused ||
		paused.ResumeStatus != workflow.StatusRunning ||
		paused.Stage != store.ProductWorkflowStageSourcing ||
		paused.SourcingBatchID == nil ||
		*paused.SourcingBatchID != batchID {
		t.Fatalf("project account pause = %+v, %v", paused, err)
	}

	resumed, err := manager.Resume()
	if err != nil ||
		resumed == nil ||
		resumed.RunID != started.RunID ||
		resumed.Status != workflow.StatusRunning ||
		resumed.Stage != store.ProductWorkflowStageSourcing ||
		resumed.SourcingBatchID == nil ||
		*resumed.SourcingBatchID != batchID {
		t.Fatalf("resume same run and batch = %+v, %v", resumed, err)
	}
}

func TestAdvanceOnceKeepsSourcingTargetPauseInsidePostSourcingFunnel(t *testing.T) {
	db, key, revision := productWorkflowFixture(t)
	location := time.FixedZone("CST", 8*60*60)
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	actor := &fixturePipelineActor{
		fixtureActor:  &fixtureActor{store: db, clock: clock},
		scoreProgress: &store.SourcingBatchScoringProgress{},
	}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil || started.SourcingBatchID == nil {
		t.Fatalf("StartFull() = %+v, %v", started, err)
	}
	scoring, err := db.AdvanceProductWorkflowStage(
		store.AdvanceProductWorkflowStageRequest{
			RunID: started.RunID, ExpectedStage: started.Stage,
			ExpectedStatus: workflow.StatusRunning,
			NextStage:      store.ProductWorkflowStageScoring,
			At:             clock.Now(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MutateAccount(key, func(account *store.Account) error {
		account.StoppedAt = &clock.now
		account.PausedReason = store.SourcingTargetReachedPauseReason
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	advanced, err := manager.AdvanceOnce(context.Background())
	if err != nil ||
		advanced == nil ||
		advanced.RunID != scoring.RunID ||
		advanced.Status != workflow.StatusRunning ||
		advanced.Stage != store.ProductWorkflowStageScoring ||
		actor.scoreCalls != 1 {
		t.Fatalf(
			"sourcing target pause leaked into workflow: run=%+v scoreCalls=%d err=%v",
			advanced,
			actor.scoreCalls,
			err,
		)
	}
	account, err := db.AccountByKey(key)
	if err != nil ||
		account == nil ||
		account.StoppedAt == nil ||
		account.PausedReason != store.SourcingTargetReachedPauseReason {
		t.Fatalf("internal actor pause was unexpectedly cleared: %+v, %v", account, err)
	}
}

func TestAdvanceOnceStillProjectsManualPauseDuringScoring(t *testing.T) {
	db, key, revision := productWorkflowFixture(t)
	location := time.FixedZone("CST", 8*60*60)
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	actor := &fixturePipelineActor{
		fixtureActor:  &fixtureActor{store: db, clock: clock},
		scoreProgress: &store.SourcingBatchScoringProgress{},
	}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil || started.SourcingBatchID == nil {
		t.Fatalf("StartFull() = %+v, %v", started, err)
	}
	scoring, err := db.AdvanceProductWorkflowStage(
		store.AdvanceProductWorkflowStageRequest{
			RunID: started.RunID, ExpectedStage: started.Stage,
			ExpectedStatus: workflow.StatusRunning,
			NextStage:      store.ProductWorkflowStageScoring,
			At:             clock.Now(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MutateAccount(key, func(account *store.Account) error {
		account.StoppedAt = &clock.now
		account.PausedReason = "handManualReview"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	paused, err := manager.AdvanceOnce(context.Background())
	if err != nil ||
		paused == nil ||
		paused.RunID != scoring.RunID ||
		paused.Status != workflow.StatusPaused ||
		paused.ResumeStatus != workflow.StatusRunning ||
		paused.Stage != store.ProductWorkflowStageScoring ||
		actor.scoreCalls != 0 {
		t.Fatalf(
			"manual scoring pause not projected: run=%+v scoreCalls=%d err=%v",
			paused,
			actor.scoreCalls,
			err,
		)
	}
}

func TestResumeScoringRestoresSourcingHoldWithoutEnablingCommunication(t *testing.T) {
	db, key, revision := productWorkflowFixture(t)
	location := time.FixedZone("CST", 8*60*60)
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	actor := &fixturePipelineActor{
		fixtureActor:  &fixtureActor{store: db, clock: clock},
		scoreProgress: &store.SourcingBatchScoringProgress{},
	}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil || started.SourcingBatchID == nil {
		t.Fatalf("StartFull() = %+v, %v", started, err)
	}
	scoring, err := db.AdvanceProductWorkflowStage(
		store.AdvanceProductWorkflowStageRequest{
			RunID: started.RunID, ExpectedStage: started.Stage,
			ExpectedStatus: workflow.StatusRunning,
			NextStage:      store.ProductWorkflowStageScoring,
			At:             clock.Now(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := manager.Pause()
	if err != nil ||
		paused.Status != workflow.StatusPaused ||
		paused.Stage != store.ProductWorkflowStageScoring {
		t.Fatalf("Pause() = %+v, %v", paused, err)
	}

	resumed, err := manager.Resume()
	if err != nil ||
		resumed == nil ||
		resumed.RunID != scoring.RunID ||
		resumed.Status != workflow.StatusRunning ||
		resumed.Stage != store.ProductWorkflowStageScoring ||
		actor.holdCalls != 1 ||
		actor.enableCalls != 0 {
		t.Fatalf(
			"Resume() = %+v hold=%d enable=%d err=%v",
			resumed,
			actor.holdCalls,
			actor.enableCalls,
			err,
		)
	}
	account, err := db.AccountByKey(key)
	if err != nil ||
		account == nil ||
		account.StoppedAt == nil ||
		account.PausedReason != store.SourcingTargetReachedPauseReason {
		t.Fatalf("post-sourcing hold = %+v, %v", account, err)
	}
}

func TestResumeScoringHoldFailureRollsBackWorkflowPause(t *testing.T) {
	db, key, revision := productWorkflowFixture(t)
	location := time.FixedZone("CST", 8*60*60)
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	holdErr := errors.New("fixture hold unavailable")
	actor := &fixturePipelineActor{
		fixtureActor: &fixtureActor{
			store: db, clock: clock, holdErr: holdErr,
		},
	}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil || started.SourcingBatchID == nil {
		t.Fatalf("StartFull() = %+v, %v", started, err)
	}
	if _, err := db.AdvanceProductWorkflowStage(
		store.AdvanceProductWorkflowStageRequest{
			RunID: started.RunID, ExpectedStage: started.Stage,
			ExpectedStatus: workflow.StatusRunning,
			NextStage:      store.ProductWorkflowStageScoring,
			At:             clock.Now(),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Pause(); err != nil {
		t.Fatal(err)
	}

	resumed, err := manager.Resume()
	if !errors.Is(err, holdErr) ||
		resumed != nil ||
		actor.holdCalls != 1 ||
		actor.enableCalls != 0 {
		t.Fatalf(
			"Resume() = %+v hold=%d enable=%d err=%v",
			resumed,
			actor.holdCalls,
			actor.enableCalls,
			err,
		)
	}
	active, err := db.ActiveProductWorkflowRun()
	if err != nil ||
		active == nil ||
		active.Status != workflow.StatusPaused ||
		active.ResumeStatus != workflow.StatusRunning ||
		active.Stage != store.ProductWorkflowStageScoring {
		t.Fatalf("rollback = %+v, %v", active, err)
	}
}

func TestAdvanceOnceProjectsReplyOnlyAccountPause(t *testing.T) {
	db, key, _ := productWorkflowFixture(t)
	location := time.FixedZone("CST", 8*60*60)
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	actor := &fixturePipelineActor{
		fixtureActor: &fixtureActor{store: db, clock: clock},
	}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.StartReplyOnly(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MutateAccount(key, func(account *store.Account) error {
		account.StoppedAt = &clock.now
		account.PausedReason = "handManualReview"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	paused, err := manager.AdvanceOnce(context.Background())
	if err != nil ||
		paused == nil ||
		paused.RunID != started.RunID ||
		paused.Mode != workflow.ModeReplyOnly ||
		paused.Status != workflow.StatusPaused ||
		paused.ResumeStatus != workflow.StatusRunning ||
		paused.Stage != store.ProductWorkflowStageCommunication ||
		paused.SourcingBatchID != nil {
		t.Fatalf("project reply-only pause = %+v, %v", paused, err)
	}
}

func TestAdvanceOnceProjectsAwaitingConfirmationPauseAndResumeStatus(t *testing.T) {
	manager, actor, db, started, batchID := orchestratorFixtureAtGreetingGeneration(t)
	actor.greetingProgress = &store.SourcingBatchGreetingProgress{Completed: true}
	awaiting, err := manager.AdvanceOnce(context.Background())
	if err != nil ||
		awaiting == nil ||
		awaiting.Status != workflow.StatusAwaitingConfirmation {
		t.Fatalf("enter awaiting confirmation = %+v, %v", awaiting, err)
	}
	key := store.AccountKey{Platform: awaiting.Platform, AccountRef: awaiting.AccountRef}
	now := manager.clock.Now()
	if err := db.MutateAccount(key, func(account *store.Account) error {
		account.StoppedAt = &now
		account.PausedReason = "handManualReview"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	paused, err := manager.AdvanceOnce(context.Background())
	if err != nil ||
		paused == nil ||
		paused.RunID != started.RunID ||
		paused.Status != workflow.StatusPaused ||
		paused.ResumeStatus != workflow.StatusAwaitingConfirmation ||
		paused.Stage != store.ProductWorkflowStageAwaitingConfirmation ||
		paused.SourcingBatchID == nil ||
		*paused.SourcingBatchID != batchID {
		t.Fatalf("project awaiting pause = %+v, %v", paused, err)
	}

	resumed, err := manager.Resume()
	if err != nil ||
		resumed == nil ||
		resumed.RunID != started.RunID ||
		resumed.Status != workflow.StatusAwaitingConfirmation ||
		resumed.ResumeStatus != "" ||
		resumed.Stage != store.ProductWorkflowStageAwaitingConfirmation ||
		resumed.SourcingBatchID == nil ||
		*resumed.SourcingBatchID != batchID {
		t.Fatalf("resume awaiting confirmation = %+v, %v", resumed, err)
	}
}

func TestAdvanceOnceKeepsStoppedSourcingTerminalizationAheadOfAccountPause(t *testing.T) {
	db, key, revision := productWorkflowFixture(t)
	location := time.FixedZone("CST", 8*60*60)
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	actor := &fixturePipelineActor{
		fixtureActor: &fixtureActor{store: db, clock: clock},
	}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil || started.SourcingBatchID == nil {
		t.Fatalf("StartFull() = %+v, %v", started, err)
	}
	if _, err := db.StopSourcingBatch(store.StopSourcingBatchRequest{
		BatchID:   *started.SourcingBatchID,
		Reason:    "fixtureStopped",
		StoppedAt: clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.MutateAccount(key, func(account *store.Account) error {
		account.StoppedAt = &clock.now
		account.PausedReason = "handManualReview"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	failed, err := manager.AdvanceOnce(context.Background())
	if !errors.Is(err, ErrWorkflowPipelineInvalid) ||
		failed == nil ||
		failed.RunID != started.RunID ||
		failed.Status != workflow.StatusFailed ||
		failed.Stage != store.ProductWorkflowStageFailed {
		t.Fatalf("stopped sourcing terminalization = %+v, %v", failed, err)
	}
}

func TestInterruptedFullStartRecoversWithoutLeavingActiveSlotLocked(t *testing.T) {
	t.Run("attach batch created before interruption", func(t *testing.T) {
		db, key, revision := productWorkflowFixture(t)
		location := time.UTC
		clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
		actor := &fixturePipelineActor{
			fixtureActor: &fixtureActor{store: db, clock: clock},
		}
		manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
		if err != nil {
			t.Fatal(err)
		}
		run, err := db.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
			RunID: "wf-interrupted-with-batch", Platform: key.Platform, AccountRef: key.AccountRef,
			State: workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning},
			Stage: store.ProductWorkflowStageSourcing, StartedAt: clock.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		started, err := db.StartSourcingBatch(store.StartSourcingBatchRequest{
			BatchID:  "batch-created-before-interruption",
			Platform: key.Platform, AccountRef: key.AccountRef,
			ContextRevisionHash: revision.RevisionHash,
			TargetCount:         NewFullWorkflowTargetCount,
			StartedAt:           clock.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}

		recovered, err := manager.AdvanceOnce(context.Background())
		if err != nil ||
			recovered == nil ||
			recovered.RunID != run.RunID ||
			recovered.SourcingBatchID == nil ||
			*recovered.SourcingBatchID != started.Batch.BatchID {
			t.Fatalf("recovered=%+v err=%v", recovered, err)
		}
	})

	t.Run("terminalize run when no batch exists", func(t *testing.T) {
		db, key, _ := productWorkflowFixture(t)
		location := time.UTC
		clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
		actor := &fixturePipelineActor{
			fixtureActor: &fixtureActor{store: db, clock: clock},
		}
		manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
		if err != nil {
			t.Fatal(err)
		}
		run, err := db.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
			RunID: "wf-interrupted-without-batch", Platform: key.Platform, AccountRef: key.AccountRef,
			State: workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning},
			Stage: store.ProductWorkflowStageSourcing, StartedAt: clock.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}

		failed, err := manager.AdvanceOnce(context.Background())
		if !errors.Is(err, ErrWorkflowPipelineInvalid) ||
			failed == nil ||
			failed.RunID != run.RunID ||
			failed.Status != workflow.StatusFailed ||
			failed.Stage != store.ProductWorkflowStageFailed ||
			failed.FailureReason != "startInterruptedBeforeBatch" ||
			actor.pauseCalls != 1 {
			t.Fatalf(
				"failed=%+v pause=%d err=%v",
				failed,
				actor.pauseCalls,
				err,
			)
		}
		if active, loadErr := db.ActiveProductWorkflowRun(); loadErr != nil || active != nil {
			t.Fatalf("interrupted run retained active slot: %+v, %v", active, loadErr)
		}
	})
}

func TestPipelinePumpPersistsMidnightAcrossIdleBoundaries(t *testing.T) {
	t.Run("reply only", func(t *testing.T) {
		db, key, _ := productWorkflowFixture(t)
		location := time.FixedZone("CST", 8*60*60)
		clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
		actor := &fixturePipelineActor{
			fixtureActor: &fixtureActor{store: db, clock: clock},
		}
		manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
		if err != nil {
			t.Fatal(err)
		}
		first, err := manager.StartReplyOnly(key)
		if err != nil {
			t.Fatal(err)
		}
		clock.now = time.Date(2026, 7, 26, 0, 0, 0, 0, location)

		completed, err := manager.AdvanceOnce(context.Background())
		if err != nil ||
			completed == nil ||
			completed.RunID != first.RunID ||
			completed.Status != workflow.StatusCompleted ||
			completed.Stage != store.ProductWorkflowStageCompleted ||
			actor.pauseCalls != 1 {
			t.Fatalf(
				"reply-only midnight = %+v pause=%d err=%v",
				completed,
				actor.pauseCalls,
				err,
			)
		}
		if active, loadErr := db.ActiveProductWorkflowRun(); loadErr != nil || active != nil {
			t.Fatalf("expired communication retained active slot: %+v, %v", active, loadErr)
		}

		clock.now = time.Date(2026, 7, 26, 8, 0, 0, 0, location)
		restarted, err := manager.StartReplyOnly(key)
		if err != nil ||
			restarted == nil ||
			restarted.RunID == first.RunID ||
			restarted.Status != workflow.StatusRunning {
			t.Fatalf("next-day explicit start = %+v, %v", restarted, err)
		}
	})

	t.Run("sourcing", func(t *testing.T) {
		db, key, revision := productWorkflowFixture(t)
		location := time.FixedZone("CST", 8*60*60)
		clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
		actor := &fixturePipelineActor{
			fixtureActor: &fixtureActor{store: db, clock: clock},
		}
		manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.StartFull(key, revision.RevisionHash); err != nil {
			t.Fatal(err)
		}
		clock.now = time.Date(2026, 7, 26, 0, 0, 0, 0, location)

		waiting, err := manager.AdvanceOnce(context.Background())
		if !errors.Is(err, ErrMemberStartBlocked) ||
			waiting == nil ||
			waiting.Status != workflow.StatusWaitingDailyWindow ||
			waiting.ResumeStatus != workflow.StatusRunning ||
			waiting.Stage != store.ProductWorkflowStageSourcing {
			t.Fatalf("sourcing midnight = %+v, %v", waiting, err)
		}
	})

	t.Run("awaiting confirmation", func(t *testing.T) {
		manager, actor, _, _, _ := orchestratorFixtureAtGreetingGeneration(t)
		actor.greetingProgress = &store.SourcingBatchGreetingProgress{Completed: true}
		awaiting, err := manager.AdvanceOnce(context.Background())
		if err != nil || awaiting.Status != workflow.StatusAwaitingConfirmation {
			t.Fatalf("enter awaiting = %+v, %v", awaiting, err)
		}
		manager.clock.(*fixtureClock).now = time.Date(
			2026,
			7,
			26,
			0,
			0,
			0,
			0,
			manager.location,
		)

		waiting, err := manager.AdvanceOnce(context.Background())
		if !errors.Is(err, ErrMemberStartBlocked) ||
			waiting == nil ||
			waiting.Status != workflow.StatusWaitingDailyWindow ||
			waiting.ResumeStatus != workflow.StatusAwaitingConfirmation ||
			waiting.Stage != store.ProductWorkflowStageAwaitingConfirmation {
			t.Fatalf("confirmation midnight = %+v, %v", waiting, err)
		}
	})
}

func TestConfirmAllRequiresExactSelectableSetAndOpenWindow(t *testing.T) {
	manager, actor, db, _, batchID := orchestratorFixtureAtGreetingGeneration(t)
	actor.greetingProgress = &store.SourcingBatchGreetingProgress{Completed: true}
	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.confirmationProjection = fixtureConfirmationProjection(batchID)

	for _, selected := range [][]string{
		{"profile-a"},
		{"profile-a", "profile-b", "profile-c"},
		{"profile-a", "profile-a"},
	} {
		if confirmed, err := manager.ConfirmAll(batchID, selected); confirmed != nil ||
			!errors.Is(err, ErrConfirmationSelectionMismatch) {
			t.Fatalf("ConfirmAll(%v) = %+v, %v", selected, confirmed, err)
		}
	}
	persisted, err := db.ActiveProductWorkflowRun()
	if err != nil || persisted == nil ||
		persisted.Status != workflow.StatusAwaitingConfirmation {
		t.Fatalf("rejected confirmation mutated state = %+v, %v", persisted, err)
	}

	manager.clock.(*fixtureClock).now = time.Date(
		2026, 7, 26, 7, 30, 0, 0, manager.location,
	)
	waiting, err := manager.ConfirmAll(batchID, []string{"profile-b", "profile-a"})
	if !errors.Is(err, workflow.ErrDailyWindowClosed) ||
		waiting == nil || waiting.Status != workflow.StatusWaitingDailyWindow ||
		waiting.ResumeStatus != workflow.StatusAwaitingConfirmation {
		t.Fatalf("closed-window ConfirmAll() = %+v, %v", waiting, err)
	}
	if actor.sendCalls != 0 {
		t.Fatalf("closed-window confirmation sent %d times", actor.sendCalls)
	}
}

func TestRepeatedConfirmationNeverRepeatsBatchSender(t *testing.T) {
	manager, actor, _, _, batchID := orchestratorFixtureAtGreetingGeneration(t)
	actor.greetingProgress = &store.SourcingBatchGreetingProgress{Completed: true}
	actor.sendProgress = &store.SourcingBatchGreetingSendProgress{
		SelectedCount: 2, SentCount: 2, Completed: true,
	}
	if _, err := manager.AdvanceOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.confirmationProjection = fixtureConfirmationProjection(batchID)

	first, err := manager.ConfirmAll(batchID, []string{"profile-b", "profile-a"})
	if err != nil || first.Stage != store.ProductWorkflowStageGreetingSending {
		t.Fatalf("first ConfirmAll() = %+v, %v", first, err)
	}
	replayed, err := manager.ConfirmAll(batchID, []string{"stale-or-different-replay"})
	if err != nil || replayed.RunID != first.RunID ||
		replayed.Stage != store.ProductWorkflowStageGreetingSending {
		t.Fatalf("replayed ConfirmAll() = %+v, %v", replayed, err)
	}
	if actor.sendCalls != 0 {
		t.Fatalf("confirmation endpoint called sender %d times", actor.sendCalls)
	}

	communication, err := manager.AdvanceOnce(context.Background())
	if err != nil || communication.Stage != store.ProductWorkflowStageCommunication {
		t.Fatalf("send AdvanceOnce() = %+v, %v", communication, err)
	}
	if actor.sendCalls != 1 {
		t.Fatalf("sender calls after send-complete boundary = %d", actor.sendCalls)
	}
	if replayed, err = manager.ConfirmAll(batchID, []string{"profile-a", "profile-b"}); err != nil || replayed.Stage != store.ProductWorkflowStageCommunication {
		t.Fatalf("post-send ConfirmAll() = %+v, %v", replayed, err)
	}

	communication, err = manager.AdvanceOnce(context.Background())
	if err != nil || communication.Status != workflow.StatusRunning ||
		communication.Stage != store.ProductWorkflowStageCommunication || actor.enableCalls != 1 {
		t.Fatalf("communication AdvanceOnce() = %+v enable=%d err=%v",
			communication, actor.enableCalls, err)
	}
	if replayed, err = manager.ConfirmAll(batchID, []string{"anything"}); err != nil ||
		replayed.Status != workflow.StatusRunning ||
		replayed.Stage != store.ProductWorkflowStageCommunication {
		t.Fatalf("communication ConfirmAll() = %+v, %v", replayed, err)
	}
	if active, err := manager.AdvanceOnce(context.Background()); err != nil ||
		active == nil ||
		active.RunID != communication.RunID ||
		active.Stage != store.ProductWorkflowStageCommunication {
		t.Fatalf("active communication AdvanceOnce() = %+v, %v", active, err)
	}
	paused, err := manager.Pause()
	if err != nil ||
		paused.Status != workflow.StatusPaused ||
		paused.Stage != store.ProductWorkflowStageCommunication {
		t.Fatalf("pause communication = %+v, %v", paused, err)
	}
	resumed, err := manager.Resume()
	if err != nil ||
		resumed.Status != workflow.StatusRunning ||
		resumed.Stage != store.ProductWorkflowStageCommunication ||
		actor.enableCalls != 2 {
		t.Fatalf("resume communication = %+v enable=%d err=%v", resumed, actor.enableCalls, err)
	}
	if actor.sendCalls != 1 {
		t.Fatalf("replay repeated sender: %d calls", actor.sendCalls)
	}
}

func TestGreetingGenerationDoesNotEnableCommunicationDuringFunnel(t *testing.T) {
	manager, actor, _, _, _ := orchestratorFixtureAtGreetingGeneration(t)
	actor.enableErr = errors.New("fixture communication unavailable")
	actor.greetingProgress = &store.SourcingBatchGreetingProgress{Completed: true}

	awaiting, err := manager.AdvanceOnce(context.Background())
	if err != nil ||
		awaiting == nil ||
		awaiting.Status != workflow.StatusAwaitingConfirmation ||
		awaiting.Stage != store.ProductWorkflowStageAwaitingConfirmation ||
		actor.greetingCalls != 1 ||
		actor.enableCalls != 0 {
		t.Fatalf(
			"funnel unexpectedly enabled communication: run=%+v greeting=%d enable=%d err=%v",
			awaiting,
			actor.greetingCalls,
			actor.enableCalls,
			err,
		)
	}
}

func orchestratorFixtureAtGreetingGeneration(
	t *testing.T,
) (*Manager, *fixturePipelineActor, *store.Store, *store.ProductWorkflowRun, string) {
	t.Helper()
	db, key, revision := productWorkflowFixture(t)
	location := time.FixedZone("CST", 8*60*60)
	clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
	baseActor := &fixtureActor{store: db, clock: clock}
	actor := &fixturePipelineActor{fixtureActor: baseActor}
	manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
	if err != nil {
		t.Fatal(err)
	}
	run, err := manager.StartFull(key, revision.RevisionHash)
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []string{
		store.ProductWorkflowStageScoring,
		store.ProductWorkflowStageSelection,
		store.ProductWorkflowStageGreetingGeneration,
	} {
		run, err = db.AdvanceProductWorkflowStage(store.AdvanceProductWorkflowStageRequest{
			RunID: run.RunID, ExpectedStage: run.Stage,
			ExpectedStatus: workflow.StatusRunning, NextStage: next, At: clock.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	manager.confirmationProjection = fixtureConfirmationProjection(*run.SourcingBatchID)
	return manager, actor, db, run, *run.SourcingBatchID
}

func fixtureConfirmationProjection(
	batchID string,
) func(string) (*store.AppConfirmationProjection, error) {
	return func(requestedBatchID string) (*store.AppConfirmationProjection, error) {
		if requestedBatchID != batchID {
			return nil, store.ErrAppProjectionInvalid
		}
		return &store.AppConfirmationProjection{
			Available: true, Ready: true, BatchID: batchID, SelectableCount: 2,
			Candidates: []store.AppConfirmationCandidate{
				{ProfileID: "profile-a", Status: "ready", Selectable: true},
				{ProfileID: "profile-b", Status: "ready", Selectable: true},
				{ProfileID: "profile-c", Status: "generationFailed"},
			},
		}, nil
	}
}
