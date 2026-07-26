package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/workflow"
)

func TestProductWorkflowRunPersistsSingleActiveControlAndHistory(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	initial := workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning}

	run, err := s.CreateProductWorkflowRun(CreateProductWorkflowRunRequest{
		RunID: "wf-one", Platform: "zhilian", AccountRef: "account-one",
		State: initial, Stage: ProductWorkflowStageSourcing, StartedAt: now,
	})
	if err != nil || run.ActiveSlot == nil || run.Status != workflow.StatusRunning {
		t.Fatalf("CreateProductWorkflowRun() = %+v, %v", run, err)
	}
	if _, err := s.CreateProductWorkflowRun(CreateProductWorkflowRunRequest{
		RunID: "wf-two", Platform: "zhilian", AccountRef: "account-one",
		State: initial, Stage: ProductWorkflowStageSourcing, StartedAt: now,
	}); !errors.Is(err, ErrProductWorkflowConflict) {
		t.Fatalf("second active run error = %v, want conflict", err)
	}

	pausedState := workflow.State{
		Mode: workflow.ModeFull, Status: workflow.StatusPaused,
		ResumeStatus: workflow.StatusRunning,
	}
	paused, err := s.TransitionProductWorkflowRun(TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: initial, To: pausedState, At: now.Add(time.Minute),
	})
	if err != nil || paused.Status != workflow.StatusPaused || paused.PausedAt == nil {
		t.Fatalf("pause transition = %+v, %v", paused, err)
	}
	replayed, err := s.TransitionProductWorkflowRun(TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: initial, To: pausedState, At: now.Add(2 * time.Minute),
	})
	if err != nil || replayed.UpdatedAt != paused.UpdatedAt {
		t.Fatalf("pause replay changed fact = %+v, %v", replayed, err)
	}

	resumedState := workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning}
	resumed, err := s.TransitionProductWorkflowRun(TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: pausedState, To: resumedState, At: now.Add(3 * time.Minute),
	})
	if err != nil || resumed.Status != workflow.StatusRunning || resumed.ResumedAt == nil {
		t.Fatalf("resume transition = %+v, %v", resumed, err)
	}
	failedState := workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusFailed}
	failed, err := s.TransitionProductWorkflowRun(TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: resumedState, To: failedState,
		At: now.Add(4 * time.Minute), Stage: ProductWorkflowStageFailed, Failure: "fixture",
	})
	if err != nil || failed.ActiveSlot != nil || failed.EndedAt == nil ||
		failed.FailureReason != "fixture" {
		t.Fatalf("terminal transition = %+v, %v", failed, err)
	}

	next, err := s.CreateProductWorkflowRun(CreateProductWorkflowRunRequest{
		RunID: "wf-two", Platform: "zhilian", AccountRef: "account-one",
		State: initial, Stage: ProductWorkflowStageSourcing, StartedAt: now.Add(5 * time.Minute),
	})
	if err != nil || next.ActiveSlot == nil {
		t.Fatalf("new active after terminal = %+v, %v", next, err)
	}
}

func TestProductWorkflowPendingActionUsesCASAndPreservesTerminalHistory(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	running := workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning}
	run, err := s.CreateProductWorkflowRun(CreateProductWorkflowRunRequest{
		RunID: "wf-pending", Platform: "zhilian", AccountRef: "account-one",
		State: running, Stage: ProductWorkflowStageCommunication, StartedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	requestedAt := now.Add(time.Minute)
	sourcing, err := s.RequestProductWorkflowPendingAction(
		RequestProductWorkflowPendingActionRequest{
			RunID: run.RunID, Action: ProductWorkflowPendingActionSourcing,
			ContextRevisionHash: "revision-next", RequestedAt: requestedAt,
		},
	)
	if err != nil || sourcing.PendingAction != ProductWorkflowPendingActionSourcing ||
		sourcing.PendingContextRevisionHash != "revision-next" ||
		sourcing.PendingRequestedAt == nil ||
		!sourcing.PendingRequestedAt.Equal(requestedAt) {
		t.Fatalf("request sourcing = %+v, %v", sourcing, err)
	}

	replayed, err := s.RequestProductWorkflowPendingAction(
		RequestProductWorkflowPendingActionRequest{
			RunID: run.RunID, Action: ProductWorkflowPendingActionSourcing,
			ContextRevisionHash: "revision-next", RequestedAt: requestedAt.Add(time.Minute),
		},
	)
	if err != nil || replayed.PendingRequestedAt == nil ||
		!replayed.PendingRequestedAt.Equal(requestedAt) ||
		!replayed.UpdatedAt.Equal(sourcing.UpdatedAt) {
		t.Fatalf("request replay changed original fact = %+v, %v", replayed, err)
	}
	if _, err := s.RequestProductWorkflowPendingAction(
		RequestProductWorkflowPendingActionRequest{
			RunID: run.RunID, Action: ProductWorkflowPendingActionSourcing,
			ContextRevisionHash: "revision-other", RequestedAt: requestedAt,
		},
	); !errors.Is(err, ErrProductWorkflowConflict) {
		t.Fatalf("competing sourcing revision error = %v", err)
	}
	if _, err := s.RequestProductWorkflowPendingAction(
		RequestProductWorkflowPendingActionRequest{
			RunID: run.RunID, Action: ProductWorkflowPendingActionEnd,
			RequestedAt: requestedAt,
		},
	); !errors.Is(err, ErrProductWorkflowConflict) {
		t.Fatalf("competing end action error = %v", err)
	}
	if _, err := s.ClearProductWorkflowPendingAction(
		ClearProductWorkflowPendingActionRequest{
			RunID: run.RunID, ExpectedAction: ProductWorkflowPendingActionEnd,
		},
	); !errors.Is(err, ErrProductWorkflowConflict) {
		t.Fatalf("wrong expected clear error = %v", err)
	}

	cleared, err := s.ClearProductWorkflowPendingAction(
		ClearProductWorkflowPendingActionRequest{
			RunID: run.RunID, ExpectedAction: ProductWorkflowPendingActionSourcing,
		},
	)
	if err != nil || cleared.PendingAction != "" ||
		cleared.PendingContextRevisionHash != "" ||
		cleared.PendingRequestedAt != nil {
		t.Fatalf("clear sourcing = %+v, %v", cleared, err)
	}
	clearReplay, err := s.ClearProductWorkflowPendingAction(
		ClearProductWorkflowPendingActionRequest{
			RunID: run.RunID, ExpectedAction: ProductWorkflowPendingActionSourcing,
		},
	)
	if err != nil || !clearReplay.UpdatedAt.Equal(cleared.UpdatedAt) {
		t.Fatalf("clear replay changed fact = %+v, %v", clearReplay, err)
	}

	endRequestedAt := requestedAt.Add(2 * time.Minute)
	pendingEnd, err := s.RequestProductWorkflowPendingAction(
		RequestProductWorkflowPendingActionRequest{
			RunID: run.RunID, Action: ProductWorkflowPendingActionEnd,
			RequestedAt: endRequestedAt,
		},
	)
	if err != nil || pendingEnd.PendingAction != ProductWorkflowPendingActionEnd ||
		pendingEnd.PendingContextRevisionHash != "" ||
		pendingEnd.PendingRequestedAt == nil ||
		!pendingEnd.PendingRequestedAt.Equal(endRequestedAt) {
		t.Fatalf("request end = %+v, %v", pendingEnd, err)
	}

	completed := workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusCompleted}
	terminal, err := s.TransitionProductWorkflowRun(TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: running, To: completed,
		At: endRequestedAt.Add(time.Minute), Stage: ProductWorkflowStageCompleted,
		EndReason: "userEnded",
	})
	if err != nil || terminal.ActiveSlot != nil || terminal.EndReason != "userEnded" ||
		terminal.PendingAction != ProductWorkflowPendingActionEnd ||
		terminal.PendingRequestedAt == nil ||
		!terminal.PendingRequestedAt.Equal(endRequestedAt) {
		t.Fatalf("terminal transition did not preserve pending history = %+v, %v", terminal, err)
	}
	terminalReplay, err := s.TransitionProductWorkflowRun(TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: running, To: completed,
		At: endRequestedAt.Add(2 * time.Minute), Stage: ProductWorkflowStageCompleted,
		EndReason: "userEnded",
	})
	if err != nil || !terminalReplay.UpdatedAt.Equal(terminal.UpdatedAt) {
		t.Fatalf("terminal replay changed fact = %+v, %v", terminalReplay, err)
	}
	if _, err := s.TransitionProductWorkflowRun(TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: running, To: completed,
		At: endRequestedAt.Add(2 * time.Minute), Stage: ProductWorkflowStageCompleted,
		EndReason: "differentReason",
	}); !errors.Is(err, ErrProductWorkflowConflict) {
		t.Fatalf("terminal replay with different reason error = %v", err)
	}
	if _, err := s.ClearProductWorkflowPendingAction(
		ClearProductWorkflowPendingActionRequest{
			RunID: run.RunID, ExpectedAction: ProductWorkflowPendingActionEnd,
		},
	); !errors.Is(err, ErrProductWorkflowConflict) {
		t.Fatalf("terminal history clear error = %v", err)
	}
}

func TestProductWorkflowPendingActionRequiresEligibleCommunicationRun(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	allowed := []workflow.State{
		{Mode: workflow.ModeFull, Status: workflow.StatusRunning},
		{
			Mode: workflow.ModeFull, Status: workflow.StatusPaused,
			ResumeStatus: workflow.StatusRunning,
		},
		{
			Mode: workflow.ModeReplyOnly, Status: workflow.StatusWaitingDailyWindow,
			ResumeStatus: workflow.StatusRunning,
		},
	}
	for index, state := range allowed {
		t.Run(string(state.Status), func(t *testing.T) {
			s := openTest(t)
			run, err := s.CreateProductWorkflowRun(CreateProductWorkflowRunRequest{
				RunID:    "wf-allowed-" + string(rune('a'+index)),
				Platform: "zhilian", AccountRef: "account-one", State: state,
				Stage: ProductWorkflowStageCommunication, StartedAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.RequestProductWorkflowPendingAction(
				RequestProductWorkflowPendingActionRequest{
					RunID: run.RunID, Action: ProductWorkflowPendingActionEnd,
					RequestedAt: now.Add(time.Minute),
				},
			); err != nil {
				t.Fatalf("allowed state request error = %v", err)
			}
		})
	}

	disallowed := []struct {
		name  string
		state workflow.State
		stage string
	}{
		{
			name: "wrong stage",
			state: workflow.State{
				Mode: workflow.ModeFull, Status: workflow.StatusRunning,
			},
			stage: ProductWorkflowStageScoring,
		},
		{
			name: "awaiting confirmation",
			state: workflow.State{
				Mode: workflow.ModeFull, Status: workflow.StatusAwaitingConfirmation,
			},
			stage: ProductWorkflowStageCommunication,
		},
	}
	for index, tc := range disallowed {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			run, err := s.CreateProductWorkflowRun(CreateProductWorkflowRunRequest{
				RunID:    "wf-disallowed-" + string(rune('a'+index)),
				Platform: "zhilian", AccountRef: "account-one", State: tc.state,
				Stage: tc.stage, StartedAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.RequestProductWorkflowPendingAction(
				RequestProductWorkflowPendingActionRequest{
					RunID: run.RunID, Action: ProductWorkflowPendingActionEnd,
					RequestedAt: now.Add(time.Minute),
				},
			); !errors.Is(err, ErrProductWorkflowConflict) {
				t.Fatalf("ineligible request error = %v", err)
			}
		})
	}
}

func TestProductWorkflowPendingActionValidatesActionPayload(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	running := workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning}
	run, err := s.CreateProductWorkflowRun(CreateProductWorkflowRunRequest{
		RunID: "wf-pending-validation", Platform: "zhilian", AccountRef: "account-one",
		State: running, Stage: ProductWorkflowStageCommunication, StartedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	invalid := []RequestProductWorkflowPendingActionRequest{
		{
			RunID: run.RunID, Action: ProductWorkflowPendingActionSourcing,
			RequestedAt: now,
		},
		{
			RunID: run.RunID, Action: ProductWorkflowPendingActionEnd,
			ContextRevisionHash: "unexpected", RequestedAt: now,
		},
		{
			RunID: run.RunID, Action: "unknown", RequestedAt: now,
		},
		{
			RunID: run.RunID, Action: ProductWorkflowPendingActionEnd,
		},
	}
	for index, request := range invalid {
		if _, err := s.RequestProductWorkflowPendingAction(request); !errors.Is(err, ErrProductWorkflowInvalid) {
			t.Fatalf("invalid request %d error = %v", index, err)
		}
	}
	if _, err := s.TransitionProductWorkflowRun(TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: running,
		To: workflow.State{
			Mode: workflow.ModeFull, Status: workflow.StatusPaused,
			ResumeStatus: workflow.StatusRunning,
		},
		At: now.Add(time.Minute), EndReason: "notTerminal",
	}); !errors.Is(err, ErrProductWorkflowInvalid) {
		t.Fatalf("non-terminal end reason error = %v", err)
	}
}

func TestAttachProductWorkflowSourcingBatchRequiresSameScope(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	state := workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning}
	run, err := s.CreateProductWorkflowRun(CreateProductWorkflowRunRequest{
		RunID: "wf-attach", Platform: "zhilian", AccountRef: "account-one",
		State: state, Stage: ProductWorkflowStageSourcing, StartedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch := SourcingBatch{
		BatchID: "batch-one", Platform: "zhilian", AccountRef: "account-one",
		ContextRevisionHash: "revision-one", TargetCount: 30,
		Status: SourcingBatchPreparing, StartedAt: now,
	}
	if err := s.db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	attached, err := s.AttachProductWorkflowSourcingBatch(run.RunID, batch.BatchID)
	if err != nil || attached.SourcingBatchID == nil || *attached.SourcingBatchID != batch.BatchID {
		t.Fatalf("AttachProductWorkflowSourcingBatch() = %+v, %v", attached, err)
	}
	replayed, err := s.AttachProductWorkflowSourcingBatch(run.RunID, batch.BatchID)
	if err != nil || replayed.SourcingBatchID == nil || *replayed.SourcingBatchID != batch.BatchID {
		t.Fatalf("replayed attach = %+v, %v", replayed, err)
	}

	other := SourcingBatch{
		BatchID: "batch-other", Platform: "zhilian", AccountRef: "account-other",
		ContextRevisionHash: "revision-one", TargetCount: 30,
		Status: SourcingBatchPreparing, StartedAt: now,
	}
	if err := s.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.AttachProductWorkflowSourcingBatch(run.RunID, other.BatchID); !errors.Is(err, ErrProductWorkflowConflict) {
		t.Fatalf("scope-conflicting attach error = %v", err)
	}
}

func TestAdvanceProductWorkflowStageUsesStatusAndStageCAS(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	running := workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning}
	run, err := s.CreateProductWorkflowRun(CreateProductWorkflowRunRequest{
		RunID: "wf-stage", Platform: "zhilian", AccountRef: "account-one",
		State: running, Stage: ProductWorkflowStageSourcing, StartedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := AdvanceProductWorkflowStageRequest{
		RunID: run.RunID, ExpectedStage: ProductWorkflowStageSourcing,
		ExpectedStatus: workflow.StatusRunning, NextStage: ProductWorkflowStageScoring,
		At: now.Add(time.Minute),
	}
	advanced, err := s.AdvanceProductWorkflowStage(request)
	if err != nil || advanced.Stage != ProductWorkflowStageScoring ||
		advanced.Status != workflow.StatusRunning {
		t.Fatalf("AdvanceProductWorkflowStage() = %+v, %v", advanced, err)
	}
	replayed, err := s.AdvanceProductWorkflowStage(request)
	if err != nil || replayed.Stage != ProductWorkflowStageScoring ||
		!replayed.UpdatedAt.Equal(advanced.UpdatedAt) {
		t.Fatalf("stage replay changed fact = %+v, %v", replayed, err)
	}

	wrongStage := request
	wrongStage.ExpectedStage = ProductWorkflowStageSelection
	wrongStage.NextStage = ProductWorkflowStageGreetingGeneration
	if _, err := s.AdvanceProductWorkflowStage(wrongStage); !errors.Is(err, ErrProductWorkflowConflict) {
		t.Fatalf("wrong expected stage error = %v", err)
	}
	wrongStatus := request
	wrongStatus.ExpectedStage = ProductWorkflowStageScoring
	wrongStatus.NextStage = ProductWorkflowStageSelection
	wrongStatus.ExpectedStatus = workflow.StatusAwaitingConfirmation
	if _, err := s.AdvanceProductWorkflowStage(wrongStatus); !errors.Is(err, ErrProductWorkflowConflict) {
		t.Fatalf("wrong expected status error = %v", err)
	}

	paused := workflow.State{
		Mode: workflow.ModeFull, Status: workflow.StatusPaused,
		ResumeStatus: workflow.StatusRunning,
	}
	if _, err := s.TransitionProductWorkflowRun(TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: running, To: paused, At: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	pausedAdvance := request
	pausedAdvance.ExpectedStage = ProductWorkflowStageScoring
	pausedAdvance.NextStage = ProductWorkflowStageSelection
	pausedAdvance.ExpectedStatus = workflow.StatusPaused
	if _, err := s.AdvanceProductWorkflowStage(pausedAdvance); !errors.Is(err, ErrProductWorkflowInvalid) {
		t.Fatalf("paused stage advance error = %v", err)
	}
}

func TestProductWorkflowRunBySourcingBatchIDIncludesTerminalHistoryAndPrefersNewest(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	batch := SourcingBatch{
		BatchID: "batch-history", Platform: "zhilian", AccountRef: "account-one",
		ContextRevisionHash: "revision-one", TargetCount: 30,
		Status: SourcingBatchPreparing, StartedAt: now,
	}
	if err := s.db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	running := workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusRunning}
	first, err := s.CreateProductWorkflowRun(CreateProductWorkflowRunRequest{
		RunID: "wf-history-a", Platform: batch.Platform, AccountRef: batch.AccountRef,
		State: running, Stage: ProductWorkflowStageSourcing, StartedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AttachProductWorkflowSourcingBatch(first.RunID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	failed := workflow.State{Mode: workflow.ModeFull, Status: workflow.StatusFailed}
	if _, err := s.TransitionProductWorkflowRun(TransitionProductWorkflowRunRequest{
		RunID: first.RunID, From: running, To: failed, At: now.Add(time.Minute),
		Stage: ProductWorkflowStageFailed, Failure: "fixture",
	}); err != nil {
		t.Fatal(err)
	}
	historical, err := s.ProductWorkflowRunBySourcingBatchID(batch.BatchID)
	if err != nil || historical == nil || historical.RunID != first.RunID ||
		historical.ActiveSlot != nil {
		t.Fatalf("terminal batch workflow = %+v, %v", historical, err)
	}

	second, err := s.CreateProductWorkflowRun(CreateProductWorkflowRunRequest{
		RunID: "wf-history-b", Platform: batch.Platform, AccountRef: batch.AccountRef,
		State: running, Stage: ProductWorkflowStageSourcing, StartedAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AttachProductWorkflowSourcingBatch(second.RunID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	newest, err := s.ProductWorkflowRunBySourcingBatchID(batch.BatchID)
	if err != nil || newest == nil || newest.RunID != second.RunID ||
		newest.ActiveSlot == nil {
		t.Fatalf("newest batch workflow = %+v, %v", newest, err)
	}
	if missing, err := s.ProductWorkflowRunBySourcingBatchID("batch-missing"); err != nil || missing != nil {
		t.Fatalf("missing batch workflow = %+v, %v", missing, err)
	}
}
