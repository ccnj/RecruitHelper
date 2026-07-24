package productworkflow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
)

var (
	ErrPipelineActorUnavailable      = errors.New("产品工作流批次推进 actor 尚未接线")
	ErrWorkflowPipelineInvalid       = errors.New("产品工作流批次推进状态无效")
	ErrConfirmationNotReady          = errors.New("候选确认批次尚未就绪")
	ErrConfirmationSelectionMismatch = errors.New("候选确认必须精确全选当前可发送候选人")
	ErrGreetingSendingRequiresManual = errors.New("招呼发送存在待人工收敛成员")
)

// PipelineActor deliberately mirrors the already-existing M6 production
// entry points. The product workflow only sequences them; it never creates a
// second AI, selection, WAL or hand-dispatch path.
type PipelineActor interface {
	Actor
	ScoreCompletedSourcingBatch(
		context.Context,
		string,
	) (*store.SourcingBatchScoringProgress, error)
	GenerateSelectedSourcingGreetings(
		context.Context,
		string,
	) (*store.SourcingBatchGreetingProgress, error)
	SendSelectedSourcingGreetings(
		context.Context,
		string,
	) (*store.SourcingBatchGreetingSendProgress, error)
}

// AdvanceOnce advances at most one durable phase. Candidate-level methods may
// finish several members internally, but they all call the exact member gate
// installed by NewManager before beginning the next member.
func (m *Manager) AdvanceOnce(ctx context.Context) (*store.ProductWorkflowRun, error) {
	m.advanceMu.Lock()
	defer m.advanceMu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil || run == nil {
		return run, err
	}
	if run.Mode == workflow.ModeReplyOnly {
		// Reply-only is intentionally isolated from every M6 stage.
		return run, nil
	}
	if run.Mode != workflow.ModeFull || run.SourcingBatchID == nil ||
		strings.TrimSpace(*run.SourcingBatchID) == "" {
		return run, ErrWorkflowPipelineInvalid
	}
	switch run.Status {
	case workflow.StatusPaused, workflow.StatusWaitingDailyWindow,
		workflow.StatusAwaitingConfirmation:
		return run, nil
	case workflow.StatusRunning:
	default:
		return run, ErrWorkflowPipelineInvalid
	}

	batchID := *run.SourcingBatchID
	switch run.Stage {
	case store.ProductWorkflowStageSourcing:
		batch, loadErr := m.store.SourcingBatchByID(batchID)
		if loadErr != nil {
			return run, loadErr
		}
		if batch == nil {
			return run, store.ErrSourcingBatchNotFound
		}
		switch batch.Status {
		case store.SourcingBatchPreparing, store.SourcingBatchCollecting,
			store.SourcingBatchBlocked:
			return run, nil
		case store.SourcingBatchCompleted:
			// 正式采集达到目标时，采集 actor 会先暂停账号。推荐页工作已经
			// 结束，后续评分/生成不再占用手，因此在同一持久工作流仍为
			// running 的前提下恢复既有会话巡检；等待人工确认期间多轮回复
			// 也不会被漏斗无故冻住。
			if err := m.enableCommunicationAfterSourcing(run); err != nil {
				return m.currentRunOr(run), err
			}
			return m.advanceStage(run, store.ProductWorkflowStageScoring)
		case store.SourcingBatchStopped:
			return m.failStoppedPipeline(run, batch.Reason)
		default:
			return run, ErrWorkflowPipelineInvalid
		}

	case store.ProductWorkflowStageScoring:
		if blocked := m.requireOpenMemberBoundary(run); blocked != nil {
			return blocked.run, blocked.err
		}
		actor, ok := m.actor.(PipelineActor)
		if !ok {
			return run, ErrPipelineActorUnavailable
		}
		progress, advanceErr := actor.ScoreCompletedSourcingBatch(ctx, batchID)
		if advanceErr != nil {
			return m.currentRunOr(run), advanceErr
		}
		if progress == nil || !progress.Completed {
			return run, nil
		}
		return m.advanceStage(run, store.ProductWorkflowStageSelection)

	case store.ProductWorkflowStageSelection:
		if blocked := m.requireOpenMemberBoundary(run); blocked != nil {
			return blocked.run, blocked.err
		}
		if _, selectErr := m.store.SelectCompletedSourcingBatch(batchID, m.clock.Now()); selectErr != nil {
			return run, selectErr
		}
		return m.advanceStage(run, store.ProductWorkflowStageGreetingGeneration)

	case store.ProductWorkflowStageGreetingGeneration:
		if blocked := m.requireOpenMemberBoundary(run); blocked != nil {
			return blocked.run, blocked.err
		}
		actor, ok := m.actor.(PipelineActor)
		if !ok {
			return run, ErrPipelineActorUnavailable
		}
		progress, advanceErr := actor.GenerateSelectedSourcingGreetings(ctx, batchID)
		if advanceErr != nil {
			return m.currentRunOr(run), advanceErr
		}
		if progress == nil || !progress.Completed {
			return run, nil
		}
		return m.enterAwaitingConfirmation(run)

	case store.ProductWorkflowStageAwaitingConfirmation:
		// This branch is intentionally inert. Only ConfirmAll may move it to
		// greetingSending, regardless of timers, restarts or repeated ticks.
		return run, nil

	case store.ProductWorkflowStageGreetingSending:
		if blocked := m.requireOpenMemberBoundary(run); blocked != nil {
			return blocked.run, blocked.err
		}
		actor, ok := m.actor.(PipelineActor)
		if !ok {
			return run, ErrPipelineActorUnavailable
		}
		progress, advanceErr := actor.SendSelectedSourcingGreetings(ctx, batchID)
		if advanceErr != nil {
			return m.currentRunOr(run), advanceErr
		}
		if progress == nil {
			return run, nil
		}
		if progress.SuspectCount > 0 && !progress.Completed {
			return run, ErrGreetingSendingRequiresManual
		}
		if !progress.Completed {
			return run, nil
		}
		// Persist the send-complete boundary before enabling communication.
		// If enabling fails, the next tick retries only EnableToday and never
		// calls the batch sender again.
		return m.advanceStage(run, store.ProductWorkflowStageCommunication)

	case store.ProductWorkflowStageCommunication:
		return m.completeWithCommunication(run)

	default:
		return run, ErrWorkflowPipelineInvalid
	}
}

func (m *Manager) enableCommunicationAfterSourcing(
	fallback *store.ProductWorkflowRun,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return err
	}
	if run == nil || run.RunID != fallback.RunID ||
		run.Status != workflow.StatusRunning ||
		run.Stage != store.ProductWorkflowStageSourcing {
		return store.ErrProductWorkflowConflict
	}
	open, err := workflow.EvaluateDailyWindow(m.clock.Now(), m.location)
	if err != nil {
		return err
	}
	if !open {
		from := stateOf(run)
		waiting := workflow.State{
			Mode: run.Mode, Status: workflow.StatusWaitingDailyWindow,
			ResumeStatus: workflow.StatusRunning,
		}
		if _, err := m.store.TransitionProductWorkflowRun(
			store.TransitionProductWorkflowRunRequest{
				RunID: run.RunID, From: from, To: waiting, At: m.clock.Now(),
			},
		); err != nil {
			return err
		}
		return ErrMemberStartBlocked
	}
	return m.actor.EnableToday(store.AccountKey{
		Platform: run.Platform, AccountRef: run.AccountRef,
	})
}

// Run is a small restart-safe pump. Errors are observations, not permission
// to discard durable progress; the next tick re-enters through AdvanceOnce.
func (m *Manager) Run(
	ctx context.Context,
	interval time.Duration,
	report func(error),
) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_, err := m.AdvanceOnce(ctx)
		if err != nil && !errors.Is(err, ErrMemberStartBlocked) &&
			!errors.Is(err, context.Canceled) && report != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ConfirmAll is the only transition which can authorize first-greeting
// sending. The supplied IDs must be exactly the current selectable projection;
// subsets, supersets, duplicates and stale batches are rejected.
func (m *Manager) ConfirmAll(
	batchID string,
	profileIDs []string,
) (*store.ProductWorkflowRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, store.ErrProductWorkflowInvalid
	}
	if historical, err := m.store.ProductWorkflowRunBySourcingBatchID(batchID); err != nil {
		return nil, err
	} else if historical != nil &&
		historical.Status == workflow.StatusCompleted &&
		historical.Stage == store.ProductWorkflowStageCompleted {
		// HTTP replay after terminal completion cannot recreate a send.
		return historical, nil
	}

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrWorkflowNotActive
	}
	if run.Mode != workflow.ModeFull || run.SourcingBatchID == nil ||
		*run.SourcingBatchID != batchID {
		return nil, ErrWorkflowScopeConflict
	}
	if run.Status == workflow.StatusRunning &&
		(run.Stage == store.ProductWorkflowStageGreetingSending ||
			run.Stage == store.ProductWorkflowStageCommunication) {
		// Confirmation was already durably consumed. The sender's existing
		// source/WAL idempotency remains the only effect authority.
		return run, nil
	}
	if run.Status != workflow.StatusAwaitingConfirmation ||
		run.Stage != store.ProductWorkflowStageAwaitingConfirmation {
		return nil, ErrConfirmationNotReady
	}

	now := m.clock.Now()
	open, err := workflow.EvaluateDailyWindow(now, m.location)
	if err != nil {
		return nil, err
	}
	if !open {
		waitingState := workflow.State{
			Mode: run.Mode, Status: workflow.StatusWaitingDailyWindow,
			ResumeStatus: workflow.StatusAwaitingConfirmation,
		}
		waiting, persistErr := m.store.TransitionProductWorkflowRun(
			store.TransitionProductWorkflowRunRequest{
				RunID: run.RunID, From: stateOf(run), To: waitingState, At: now,
			},
		)
		if persistErr != nil {
			return nil, persistErr
		}
		return waiting, workflow.ErrDailyWindowClosed
	}

	projection, err := m.confirmationProjection(batchID)
	if err != nil {
		return nil, err
	}
	expected, err := exactSelectableProfiles(projection, batchID)
	if err != nil {
		return nil, err
	}
	actual, err := canonicalProfileIDs(profileIDs)
	if err != nil || !sameStrings(expected, actual) {
		return nil, ErrConfirmationSelectionMismatch
	}

	from := stateOf(run)
	to := workflow.State{Mode: run.Mode, Status: workflow.StatusRunning}
	return m.store.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: from, To: to, At: now,
		Stage: store.ProductWorkflowStageGreetingSending,
	})
}

func (m *Manager) advanceStage(
	run *store.ProductWorkflowRun,
	next string,
) (*store.ProductWorkflowRun, error) {
	return m.store.AdvanceProductWorkflowStage(store.AdvanceProductWorkflowStageRequest{
		RunID: run.RunID, ExpectedStage: run.Stage, ExpectedStatus: run.Status,
		NextStage: next, At: m.clock.Now(),
	})
}

func (m *Manager) enterAwaitingConfirmation(
	run *store.ProductWorkflowRun,
) (*store.ProductWorkflowRun, error) {
	from := stateOf(run)
	to := workflow.State{Mode: run.Mode, Status: workflow.StatusAwaitingConfirmation}
	return m.store.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: from, To: to, At: m.clock.Now(),
		Stage: store.ProductWorkflowStageAwaitingConfirmation,
	})
}

func (m *Manager) failStoppedPipeline(
	run *store.ProductWorkflowRun,
	reason string,
) (*store.ProductWorkflowRun, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "sourcingBatchStopped"
	}
	cause := fmt.Errorf("%w: %s", ErrWorkflowPipelineInvalid, reason)
	from := stateOf(run)
	to := workflow.State{Mode: run.Mode, Status: workflow.StatusFailed}
	failed, err := m.store.TransitionProductWorkflowRun(
		store.TransitionProductWorkflowRunRequest{
			RunID: run.RunID, From: from, To: to, At: m.clock.Now(),
			Stage: store.ProductWorkflowStageFailed, Failure: cause.Error(),
		},
	)
	if err != nil {
		return run, errors.Join(cause, err)
	}
	return failed, cause
}

func (m *Manager) completeWithCommunication(
	fallback *store.ProductWorkflowRun,
) (*store.ProductWorkflowRun, error) {
	// Linearize the final enable against Pause/Resume. Otherwise Pause could
	// close the durable gate and account actor between EnableToday and the
	// terminal workflow transition, only to have a stale tick reopen it.
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return fallback, err
	}
	if run == nil {
		return fallback, store.ErrProductWorkflowConflict
	}
	if run.RunID != fallback.RunID || run.Status != workflow.StatusRunning ||
		run.Stage != store.ProductWorkflowStageCommunication {
		return run, store.ErrProductWorkflowConflict
	}
	now := m.clock.Now()
	open, err := workflow.EvaluateDailyWindow(now, m.location)
	if err != nil {
		return run, err
	}
	if !open {
		waiting := workflow.State{
			Mode: run.Mode, Status: workflow.StatusWaitingDailyWindow,
			ResumeStatus: workflow.StatusRunning,
		}
		return m.store.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
			RunID: run.RunID, From: stateOf(run), To: waiting, At: now,
		})
	}
	key := store.AccountKey{Platform: run.Platform, AccountRef: run.AccountRef}
	if err := m.actor.EnableToday(key); err != nil {
		return run, err
	}
	to := workflow.State{Mode: run.Mode, Status: workflow.StatusCompleted}
	return m.store.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: stateOf(run), To: to, At: now,
		Stage: store.ProductWorkflowStageCompleted,
	})
}

type memberBoundaryResult struct {
	run *store.ProductWorkflowRun
	err error
}

func (m *Manager) requireOpenMemberBoundary(
	fallback *store.ProductWorkflowRun,
) *memberBoundaryResult {
	if err := m.MayStartNextWorkflowMember(); err != nil {
		current, loadErr := m.store.ActiveProductWorkflowRun()
		if loadErr != nil {
			return &memberBoundaryResult{run: fallback, err: errors.Join(err, loadErr)}
		}
		return &memberBoundaryResult{run: currentRunOr(fallback, current, nil), err: err}
	}
	return nil
}

func exactSelectableProfiles(
	projection *store.AppConfirmationProjection,
	batchID string,
) ([]string, error) {
	if projection == nil || !projection.Available || !projection.Ready ||
		projection.BatchID != batchID {
		return nil, ErrConfirmationNotReady
	}
	ids := make([]string, 0, len(projection.Candidates))
	for i := range projection.Candidates {
		if projection.Candidates[i].Selectable {
			ids = append(ids, projection.Candidates[i].ProfileID)
		}
	}
	canonical, err := canonicalProfileIDs(ids)
	if err != nil || len(canonical) == 0 ||
		int64(len(canonical)) != projection.SelectableCount {
		return nil, ErrConfirmationNotReady
	}
	return canonical, nil
}

func canonicalProfileIDs(values []string) ([]string, error) {
	out := make([]string, len(values))
	for i := range values {
		out[i] = strings.TrimSpace(values[i])
		if out[i] == "" {
			return nil, ErrConfirmationSelectionMismatch
		}
	}
	sort.Strings(out)
	for i := 1; i < len(out); i++ {
		if out[i] == out[i-1] {
			return nil, ErrConfirmationSelectionMismatch
		}
	}
	return out, nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func currentRunOr(
	fallback *store.ProductWorkflowRun,
	current *store.ProductWorkflowRun,
	err error,
) *store.ProductWorkflowRun {
	if err == nil && current != nil {
		return current
	}
	return fallback
}

func (m *Manager) currentRunOr(
	fallback *store.ProductWorkflowRun,
) *store.ProductWorkflowRun {
	current, err := m.store.ActiveProductWorkflowRun()
	return currentRunOr(fallback, current, err)
}
