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
	manager, actor, db, run, _ := orchestratorFixtureAtGreetingGeneration(t)
	actor.greetingProgress = &store.SourcingBatchGreetingProgress{Completed: true}
	actor.sendProgress = &store.SourcingBatchGreetingSendProgress{Completed: true}

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

	completed, err := manager.AdvanceOnce(context.Background())
	if err != nil || completed.Status != workflow.StatusCompleted ||
		completed.Stage != store.ProductWorkflowStageCompleted || actor.enableCalls != 1 {
		t.Fatalf("communication AdvanceOnce() = %+v enable=%d err=%v",
			completed, actor.enableCalls, err)
	}
	if replayed, err = manager.ConfirmAll(batchID, []string{"anything"}); err != nil || replayed.Status != workflow.StatusCompleted {
		t.Fatalf("terminal ConfirmAll() = %+v, %v", replayed, err)
	}
	if active, err := manager.AdvanceOnce(context.Background()); err != nil || active != nil {
		t.Fatalf("terminal AdvanceOnce() = %+v, %v", active, err)
	}
	if actor.sendCalls != 1 {
		t.Fatalf("replay repeated sender: %d calls", actor.sendCalls)
	}
}

func TestCommunicationResumeAfterSourcingRechecksDurableWorkflow(t *testing.T) {
	t.Run("running sourcing run enables existing communication", func(t *testing.T) {
		db, key, revision := productWorkflowFixture(t)
		location := time.FixedZone("CST", 8*60*60)
		clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
		actor := &fixtureActor{store: db, clock: clock}
		manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
		if err != nil {
			t.Fatal(err)
		}
		run, err := manager.StartFull(key, revision.RevisionHash)
		if err != nil {
			t.Fatal(err)
		}

		if err := manager.enableCommunicationAfterSourcing(run); err != nil {
			t.Fatal(err)
		}
		if actor.enableCalls != 1 {
			t.Fatalf("EnableToday calls = %d, want 1", actor.enableCalls)
		}
	})

	t.Run("pause wins before communication resume", func(t *testing.T) {
		db, key, revision := productWorkflowFixture(t)
		location := time.FixedZone("CST", 8*60*60)
		clock := &fixtureClock{now: time.Date(2026, 7, 25, 9, 0, 0, 0, location)}
		actor := &fixtureActor{store: db, clock: clock}
		manager, err := NewManager(db, actor, Config{Clock: clock, Location: location})
		if err != nil {
			t.Fatal(err)
		}
		run, err := manager.StartFull(key, revision.RevisionHash)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Pause(); err != nil {
			t.Fatal(err)
		}

		if err := manager.enableCommunicationAfterSourcing(run); !errors.Is(
			err,
			store.ErrProductWorkflowConflict,
		) {
			t.Fatalf("enable after pause error = %v", err)
		}
		if actor.enableCalls != 0 {
			t.Fatalf("EnableToday calls after pause = %d, want 0", actor.enableCalls)
		}
	})
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
