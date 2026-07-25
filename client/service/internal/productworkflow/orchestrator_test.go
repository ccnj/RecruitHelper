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

		if err := manager.ensureCommunicationDuringFunnel(run); err != nil {
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

		if err := manager.ensureCommunicationDuringFunnel(run); !errors.Is(
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

func TestCommunicationFailureDoesNotBlockGreetingGeneration(t *testing.T) {
	manager, actor, _, _, _ := orchestratorFixtureAtGreetingGeneration(t)
	actor.enableErr = errors.New("fixture communication unavailable")
	actor.greetingProgress = &store.SourcingBatchGreetingProgress{Completed: true}

	awaiting, err := manager.AdvanceOnce(context.Background())
	if !errors.Is(err, ErrCommunicationResumeFailed) ||
		awaiting == nil ||
		awaiting.Status != workflow.StatusAwaitingConfirmation ||
		awaiting.Stage != store.ProductWorkflowStageAwaitingConfirmation ||
		actor.greetingCalls != 1 {
		t.Fatalf(
			"communication failure blocked generation: run=%+v greeting=%d err=%v",
			awaiting,
			actor.greetingCalls,
			err,
		)
	}

	actor.enableErr = nil
	stillAwaiting, err := manager.AdvanceOnce(context.Background())
	if err != nil ||
		stillAwaiting == nil ||
		stillAwaiting.Status != workflow.StatusAwaitingConfirmation ||
		actor.enableCalls != 2 {
		t.Fatalf(
			"communication retry = %+v enable=%d err=%v",
			stillAwaiting,
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
