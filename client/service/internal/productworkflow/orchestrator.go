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
func (m *Manager) AdvanceOnce(
	ctx context.Context,
) (result *store.ProductWorkflowRun, resultErr error) {
	m.advanceMu.Lock()
	defer m.advanceMu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil || run == nil {
		return run, err
	}
	if run.Mode != workflow.ModeFull && run.Mode != workflow.ModeReplyOnly {
		return run, ErrWorkflowPipelineInvalid
	}
	if run.Stage == store.ProductWorkflowStageCommunication &&
		m.communicationRunExpired(run) {
		return m.completeExpiredCommunicationRun(run)
	}
	if run.Status == workflow.StatusRunning ||
		run.Status == workflow.StatusAwaitingConfirmation {
		synced, closed, syncErr := m.syncDailyWindow(run)
		if syncErr != nil {
			return synced, syncErr
		}
		run = synced
		if closed {
			return run, ErrMemberStartBlocked
		}
	}
	if run.Mode == workflow.ModeReplyOnly {
		// Reply-only is intentionally isolated from every M6 stage.
		synced, _, syncErr := m.syncAccountPause(run)
		return synced, syncErr
	}
	if run.Mode == workflow.ModeFull &&
		run.Status == workflow.StatusRunning &&
		run.Stage == store.ProductWorkflowStageSourcing &&
		(run.SourcingBatchID == nil || strings.TrimSpace(*run.SourcingBatchID) == "") {
		return m.recoverInterruptedFullStart(run)
	}
	if run.Mode != workflow.ModeFull || run.SourcingBatchID == nil ||
		strings.TrimSpace(*run.SourcingBatchID) == "" {
		return run, ErrWorkflowPipelineInvalid
	}
	synced, paused, syncErr := m.syncAccountPause(run)
	if syncErr != nil || paused {
		return synced, syncErr
	}
	run = synced
	switch run.Status {
	case workflow.StatusPaused, workflow.StatusWaitingDailyWindow:
		return run, nil
	case workflow.StatusRunning, workflow.StatusAwaitingConfirmation:
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
		case store.SourcingBatchPreparing, store.SourcingBatchCollecting:
			return run, nil
		case store.SourcingBatchBlocked:
			// blocked 是一次正式采集尝试的明确失败边界，而不是自动等待。
			// 终局化产品 run 后，下一次真人点击 StartFull 才会走既有
			// ResumeSourcingBatch 入口复用原批次、目标和 revision。
			return m.failStoppedPipeline(run, batch.Reason)
		case store.SourcingBatchCompleted:
			// 采集 actor 达标后暂停账号，漏斗继续在推荐页事实之上完成
			// 评分、筛选、招呼语生成、确认与招呼发送。只有全部招呼终局、
			// stage 进入 communication 后，keepCommunicationRunning 才会
			// 开启 IM 巡检；漏斗与沟通不再并行争用智联工作页。
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
		confirmation, projectionErr := m.confirmationProjection(batchID)
		if projectionErr != nil {
			return run, projectionErr
		}
		if confirmationReadyWithoutSendableCandidates(confirmation, batchID) {
			// 零入选或全部生成失败没有任何候选人可见动作可供确认。
			// 直接完成漏斗并保留同一运行的多轮沟通控制，不能留下一个
			// 永远无法提交的空人工闸。
			return m.advanceStage(run, store.ProductWorkflowStageCommunication)
		}
		return m.enterAwaitingConfirmation(run)

	case store.ProductWorkflowStageAwaitingConfirmation:
		confirmation, projectionErr := m.confirmationProjection(batchID)
		if projectionErr != nil {
			return run, projectionErr
		}
		if confirmationReadyWithoutSendableCandidates(confirmation, batchID) {
			// 推荐流换代可能在人工闸建立之后令全部未绑定发送意图的
			// 候选人变为 abandoned。此时已经不存在可由真人确认的
			// 外部动作，继续占住人工闸只会让“再采一批”永久不可用。
			// 业务事实与 abandoned 原因保留不动，仅关闭空人工闸。
			from := stateOf(run)
			to := workflow.State{Mode: run.Mode, Status: workflow.StatusRunning}
			return m.store.TransitionProductWorkflowRun(
				store.TransitionProductWorkflowRunRequest{
					RunID: run.RunID,
					From:  from,
					To:    to,
					At:    m.clock.Now(),
					Stage: store.ProductWorkflowStageCommunication,
				},
			)
		}
		// 仍有可发送候选人时，只有 ConfirmAll 可以授权进入发送阶段。
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
		return m.keepCommunicationRunning(run)

	default:
		return run, ErrWorkflowPipelineInvalid
	}
}

// syncAccountPause keeps the ordinary product projection aligned with the
// account actor's durable stop state. A sourcing batch's own terminal state
// remains authoritative: completed/blocked/stopped must first pass through the
// existing sourcing switch so it can advance or terminalize the workflow.
func (m *Manager) syncAccountPause(
	fallback *store.ProductWorkflowRun,
) (*store.ProductWorkflowRun, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return fallback, false, err
	}
	if run == nil || run.RunID != fallback.RunID {
		return currentRunOr(fallback, run, nil), false, store.ErrProductWorkflowConflict
	}
	if run.Status != workflow.StatusRunning &&
		run.Status != workflow.StatusAwaitingConfirmation {
		return run, false, nil
	}
	if run.Mode == workflow.ModeFull &&
		run.Stage == store.ProductWorkflowStageSourcing {
		if run.SourcingBatchID == nil ||
			strings.TrimSpace(*run.SourcingBatchID) == "" {
			return run, false, nil
		}
		batch, loadErr := m.store.SourcingBatchByID(*run.SourcingBatchID)
		if loadErr != nil {
			return run, false, loadErr
		}
		if batch == nil {
			return run, false, store.ErrSourcingBatchNotFound
		}
		if batch.Status != store.SourcingBatchPreparing &&
			batch.Status != store.SourcingBatchCollecting {
			return run, false, nil
		}
	}

	key := store.AccountKey{Platform: run.Platform, AccountRef: run.AccountRef}
	account, err := m.store.AccountByKey(key)
	if err != nil {
		return run, false, err
	}
	if account == nil {
		return run, false, store.ErrAccountNotFound
	}
	if account.StoppedAt == nil && strings.TrimSpace(account.PausedReason) == "" {
		return run, false, nil
	}

	decision, err := workflow.Pause(stateOf(run))
	if err != nil {
		return run, false, err
	}
	if !decision.Changed {
		return run, false, nil
	}
	paused, err := m.store.TransitionProductWorkflowRun(
		store.TransitionProductWorkflowRunRequest{
			RunID: run.RunID,
			From:  stateOf(run),
			To:    decision.State,
			At:    m.clock.Now(),
		},
	)
	if err != nil {
		return run, false, err
	}
	return paused, true, nil
}

func (m *Manager) recoverInterruptedFullStart(
	fallback *store.ProductWorkflowRun,
) (*store.ProductWorkflowRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return fallback, err
	}
	if run == nil || run.RunID != fallback.RunID {
		return currentRunOr(fallback, run, nil), store.ErrProductWorkflowConflict
	}
	if run.SourcingBatchID != nil && strings.TrimSpace(*run.SourcingBatchID) != "" {
		return run, nil
	}
	if run.Mode != workflow.ModeFull ||
		run.Status != workflow.StatusRunning ||
		run.Stage != store.ProductWorkflowStageSourcing {
		return run, ErrWorkflowPipelineInvalid
	}

	key := store.AccountKey{Platform: run.Platform, AccountRef: run.AccountRef}
	batch, err := m.store.ActiveSourcingBatch(key)
	if err != nil {
		return run, err
	}
	if batch != nil {
		return m.store.AttachProductWorkflowSourcingBatch(run.RunID, batch.BatchID)
	}

	failed, err := m.store.TransitionProductWorkflowRun(
		store.TransitionProductWorkflowRunRequest{
			RunID: run.RunID,
			From:  stateOf(run),
			To: workflow.State{
				Mode: run.Mode, Status: workflow.StatusFailed,
			},
			At:      m.clock.Now(),
			Stage:   store.ProductWorkflowStageFailed,
			Failure: "startInterruptedBeforeBatch",
		},
	)
	if err != nil {
		return run, err
	}
	pauseErr := m.actor.PauseNow(key)
	return failed, errors.Join(ErrWorkflowPipelineInvalid, pauseErr)
}

// communicationRunExpired separates the user's daily run switch from the
// one-shot funnel. A funnel which crossed midnight may still resume and finish
// on the next day; ResumedAt therefore becomes that run's effective task day.
// Once the run is already in communication, the next civil day requires a new
// explicit click instead of silently re-enabling yesterday's task.
func (m *Manager) communicationRunExpired(run *store.ProductWorkflowRun) bool {
	if run == nil || run.StartedAt.IsZero() {
		return false
	}
	activatedAt := run.StartedAt
	if run.ResumedAt != nil && run.ResumedAt.After(activatedAt) {
		activatedAt = *run.ResumedAt
	}
	now := m.clock.Now().In(m.location)
	activated := activatedAt.In(m.location)
	return now.Year() != activated.Year() || now.YearDay() != activated.YearDay()
}

func (m *Manager) completeExpiredCommunicationRun(
	fallback *store.ProductWorkflowRun,
) (*store.ProductWorkflowRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return fallback, err
	}
	if run == nil || run.RunID != fallback.RunID ||
		run.Stage != store.ProductWorkflowStageCommunication {
		return currentRunOr(fallback, run, nil), store.ErrProductWorkflowConflict
	}
	if !m.communicationRunExpired(run) {
		return run, nil
	}
	completed, err := m.store.TransitionProductWorkflowRun(
		store.TransitionProductWorkflowRunRequest{
			RunID: run.RunID,
			From:  stateOf(run),
			To: workflow.State{
				Mode: run.Mode, Status: workflow.StatusCompleted,
			},
			At:    m.clock.Now(),
			Stage: store.ProductWorkflowStageCompleted,
		},
	)
	if err != nil {
		return run, err
	}
	key := store.AccountKey{Platform: run.Platform, AccountRef: run.AccountRef}
	if err := m.actor.PauseNow(key); err != nil {
		return completed, err
	}
	return completed, nil
}

// syncDailyWindow persists the midnight boundary even when a workflow is
// merely waiting for a batch or for human confirmation and therefore has no
// candidate member about to start. It uses the same pure evaluator as all
// member loops, but does not mistake an open-window awaiting-confirmation
// state (Allowed=false by design) for a failure.
func (m *Manager) syncDailyWindow(
	fallback *store.ProductWorkflowRun,
) (*store.ProductWorkflowRun, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return fallback, false, err
	}
	if run == nil || run.RunID != fallback.RunID {
		return currentRunOr(fallback, run, nil), false, store.ErrProductWorkflowConflict
	}
	decision, err := workflow.MayStartNextWorkflowMember(
		stateOf(run),
		m.clock.Now(),
		m.location,
	)
	if err != nil {
		return run, false, err
	}
	if !decision.Changed {
		return run, false, nil
	}
	waiting, err := m.store.TransitionProductWorkflowRun(
		store.TransitionProductWorkflowRunRequest{
			RunID: run.RunID, From: stateOf(run), To: decision.State, At: m.clock.Now(),
		},
	)
	if err != nil {
		return run, false, err
	}
	return waiting, true, nil
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

func (m *Manager) keepCommunicationRunning(
	fallback *store.ProductWorkflowRun,
) (*store.ProductWorkflowRun, error) {
	// Linearize communication enabling against Pause/Resume. Otherwise Pause could
	// close the durable gate and account actor between EnableToday and the
	// returned state, only to have a stale tick reopen it.
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
	account, err := m.store.AccountByKey(key)
	if err != nil {
		return run, err
	}
	if account == nil {
		return run, store.ErrAccountNotFound
	}
	localDate := now.In(m.location).Format("2006-01-02")
	if account.EnabledDate == localDate &&
		account.EnabledAt != nil &&
		account.StoppedAt == nil &&
		account.PausedReason == "" {
		return run, nil
	}
	if err := m.actor.EnableToday(key); err != nil {
		return run, err
	}
	// 完整流程的漏斗已经终局，但“循环多轮回复”仍是同一用户运行。
	// 保留 active run，首页才始终拥有真实的暂停/恢复入口；漏斗完成度
	// 继续由已经终局的 sourcing batch 独立投影。
	return run, nil
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

func confirmationReadyWithoutSendableCandidates(
	projection *store.AppConfirmationProjection,
	batchID string,
) bool {
	if projection == nil ||
		!projection.Available ||
		!projection.Ready ||
		projection.BatchID != batchID ||
		projection.SelectableCount != 0 ||
		projection.GenerationPending != 0 {
		return false
	}
	for i := range projection.Candidates {
		switch projection.Candidates[i].Status {
		case "generationFailed", "abandoned", "unavailable":
		default:
			return false
		}
	}
	return true
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
