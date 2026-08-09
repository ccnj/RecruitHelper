// Package productworkflow owns the durable control state behind the ordinary
// product UI. Candidate progress remains in the existing M5/M6 facts; this
// package only decides whether a new unit of work may begin.
package productworkflow

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
)

// NewFullWorkflowTargetCount 是新建完整流程批次第一轮的采集人数。
// NewFullWorkflowCaptureLimit 是整批累计可采上限,NewFullWorkflowCaptureStep
// 是第二轮起每轮的增量:150 / 50 / 50 / 50,最多四轮。首轮开大是因为过线率
// 事先不可知,先用一轮取到足够样本;后续小步是为了少采一点算一点——每轮都
// 要采满才评分,超采只能压到一轮 50 人以内,压不到零。
const (
	NewFullWorkflowTargetCount  = 150
	NewFullWorkflowCaptureStep  = 50
	NewFullWorkflowCaptureLimit = 300
)

var (
	ErrNilStore              = errors.New("产品工作流 store 不能为空")
	ErrNilActor              = errors.New("产品工作流 actor 不能为空")
	ErrInvalidConfig         = errors.New("产品工作流配置无效")
	ErrWorkflowNotActive     = errors.New("当前没有未终局产品工作流")
	ErrWorkflowScopeConflict = errors.New("产品工作流账号范围冲突")
	ErrSourcingBatchActive   = errors.New("存在未终局采集批次，不能启动仅多轮回复")
	ErrMemberStartBlocked    = errors.New("当前不允许开始下一位候选人")
)

type Clock interface {
	Now() time.Time
}

type Actor interface {
	StartSourcing(key store.AccountKey, revisionHash string, targetCount, captureLimit int) error
	EnableToday(store.AccountKey) error
	HoldAfterSourcing(store.AccountKey) error
	PauseNow(store.AccountKey) error
}

type memberGateInstaller interface {
	SetWorkflowMemberGate(func() error)
}

type conversationGateInstaller interface {
	SetWorkflowConversationGate(func() (bool, error))
}

type patrolBoundaryActor interface {
	RunAtPatrolBoundary(func() error) error
}

type Config struct {
	Clock       Clock
	Location    *time.Location
	DailyWindow workflow.DailyWindowPolicy
}

type Manager struct {
	store       *store.Store
	actor       Actor
	clock       Clock
	location    *time.Location
	dailyWindow workflow.DailyWindowPolicy
	// advanceMu prevents two background ticks from running the same pipeline
	// phase concurrently. It is deliberately separate from mu: Pause and the
	// shared member gate must remain able to close while one candidate's AI
	// call or hand command is naturally finishing.
	advanceMu sync.Mutex
	// confirmationProjection is the sole source for the exact selectable set
	// accepted by ConfirmAll. Production always uses Store.AppConfirmation;
	// keeping it as a function also makes the control law testable without
	// manufacturing candidate PII fixtures.
	confirmationProjection func(string) (*store.AppConfirmationProjection, error)

	// Control transitions and the per-member gate share this exact mutex. A
	// resume cannot expose StatusRunning to a member loop until actor enabling
	// has either succeeded or the durable state has been rolled back.
	mu sync.Mutex
}

func NewManager(db *store.Store, actor Actor, config Config) (*Manager, error) {
	if db == nil {
		return nil, ErrNilStore
	}
	if actor == nil {
		return nil, ErrNilActor
	}
	if config.Clock == nil || config.Location == nil {
		return nil, ErrInvalidConfig
	}
	manager := &Manager{
		store: db, actor: actor, clock: config.Clock, location: config.Location,
		dailyWindow:            config.DailyWindow,
		confirmationProjection: db.AppConfirmation,
	}
	if installer, ok := actor.(memberGateInstaller); ok {
		installer.SetWorkflowMemberGate(manager.MayStartNextWorkflowMember)
	}
	if installer, ok := actor.(conversationGateInstaller); ok {
		installer.SetWorkflowConversationGate(manager.MayStartNextConversation)
	}
	return manager, nil
}

// StartFull starts a new full workflow at 150 candidates, except when the
// account already has an unfinished sourcing batch. In that case it adopts
// the batch's original revision and target unchanged.
func (m *Manager) StartFull(
	key store.AccountKey,
	contextRevisionHash string,
) (*store.ProductWorkflowRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key.Platform = strings.TrimSpace(key.Platform)
	key.AccountRef = strings.TrimSpace(key.AccountRef)
	contextRevisionHash = strings.TrimSpace(contextRevisionHash)
	if key.Platform == "" || key.AccountRef == "" {
		return nil, store.ErrProductWorkflowInvalid
	}
	now := m.clock.Now()
	if current, err := m.prepareFullStart(
		key,
		contextRevisionHash,
		now,
	); current != nil || err != nil {
		return current, err
	}
	return m.startFullLocked(key, contextRevisionHash, now)
}

func (m *Manager) startFullLocked(
	key store.AccountKey,
	contextRevisionHash string,
	now time.Time,
) (*store.ProductWorkflowRun, error) {
	activeBatch, err := m.store.ActiveSourcingBatch(key)
	if err != nil {
		return nil, err
	}
	revisionHash := contextRevisionHash
	targetCount := NewFullWorkflowTargetCount
	captureLimit := NewFullWorkflowCaptureLimit
	if activeBatch != nil {
		// 复用未终局批次时一律沿用它自己的额度,包括分轮前建立、CaptureLimit
		// 为 0 的存量批次:它们继续按单轮语义走完,不被新版改成分轮。
		revisionHash = activeBatch.ContextRevisionHash
		targetCount = activeBatch.TargetCount
		captureLimit = activeBatch.CaptureLimit
	}
	if revisionHash == "" {
		return nil, store.ErrJobAIContextRevisionInvalid
	}

	decision, err := workflow.Start(
		nil, workflow.ModeFull, now, m.location, m.dailyWindow,
	)
	if err != nil {
		return nil, err
	}
	run, err := m.store.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
		Platform: key.Platform, AccountRef: key.AccountRef,
		State: decision.State, Stage: store.ProductWorkflowStageSourcing, StartedAt: now,
	})
	if err != nil {
		return nil, err
	}

	if activeBatch != nil && activeBatch.Status == store.SourcingBatchBlocked {
		if _, err := m.store.ResumeSourcingBatch(store.ResumeSourcingBatchRequest{
			BatchID: activeBatch.BatchID,
		}); err != nil {
			return nil, m.failStart(run, key, err)
		}
	}
	if err := m.actor.StartSourcing(key, revisionHash, targetCount, captureLimit); err != nil {
		return nil, m.failStart(run, key, err)
	}
	activeBatch, err = m.store.ActiveSourcingBatch(key)
	if err != nil {
		return nil, m.failStart(run, key, err)
	}
	if activeBatch == nil || activeBatch.TargetCount != targetCount ||
		activeBatch.CaptureLimit != captureLimit ||
		activeBatch.ContextRevisionHash != revisionHash {
		return nil, m.failStart(run, key, store.ErrSourcingBatchConflict)
	}
	attached, err := m.store.AttachProductWorkflowSourcingBatch(run.RunID, activeBatch.BatchID)
	if err != nil {
		return nil, m.failStart(run, key, err)
	}
	return attached, nil
}

// prepareFullStart keeps repeated clicks idempotent. Once the prior funnel has
// reached communication, “再采一批” is durably queued instead of terminating
// the communication run immediately; the orchestrator consumes it only at the
// patrol candidate boundary.
func (m *Manager) prepareFullStart(
	key store.AccountKey,
	contextRevisionHash string,
	now time.Time,
) (*store.ProductWorkflowRun, error) {
	current, err := m.store.ActiveProductWorkflowRun()
	if err != nil || current == nil {
		return current, err
	}
	if current.Platform != key.Platform || current.AccountRef != key.AccountRef {
		return nil, ErrWorkflowScopeConflict
	}
	if current.Stage != store.ProductWorkflowStageCommunication {
		return m.activeForStart(key, workflow.ModeFull, now)
	}
	return m.store.RequestProductWorkflowPendingAction(
		store.RequestProductWorkflowPendingActionRequest{
			RunID:               current.RunID,
			Action:              store.ProductWorkflowPendingActionSourcing,
			ContextRevisionHash: contextRevisionHash,
			RequestedAt:         now,
		},
	)
}

func (m *Manager) StartReplyOnly(key store.AccountKey) (*store.ProductWorkflowRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key.Platform = strings.TrimSpace(key.Platform)
	key.AccountRef = strings.TrimSpace(key.AccountRef)
	if key.Platform == "" || key.AccountRef == "" {
		return nil, store.ErrProductWorkflowInvalid
	}
	now := m.clock.Now()
	if current, err := m.activeForStart(key, workflow.ModeReplyOnly, now); current != nil || err != nil {
		return current, err
	}
	activeBatch, err := m.store.ActiveSourcingBatch(key)
	if err != nil {
		return nil, err
	}
	if activeBatch != nil {
		return nil, ErrSourcingBatchActive
	}
	decision, err := workflow.Start(
		nil, workflow.ModeReplyOnly, now, m.location, m.dailyWindow,
	)
	if err != nil {
		return nil, err
	}
	run, err := m.store.CreateProductWorkflowRun(store.CreateProductWorkflowRunRequest{
		Platform: key.Platform, AccountRef: key.AccountRef,
		State: decision.State, Stage: store.ProductWorkflowStageCommunication, StartedAt: now,
	})
	if err != nil {
		return nil, err
	}
	if err := m.actor.EnableToday(key); err != nil {
		return nil, m.failStart(run, key, err)
	}
	return run, nil
}

func (m *Manager) Pause() (*store.ProductWorkflowRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrWorkflowNotActive
	}
	from := stateOf(run)
	decision, err := workflow.Pause(from)
	if err != nil {
		return nil, err
	}
	if !decision.Changed {
		return run, nil
	}
	paused, err := m.store.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: from, To: decision.State, At: m.clock.Now(),
	})
	if err != nil {
		return nil, err
	}
	key := store.AccountKey{Platform: run.Platform, AccountRef: run.AccountRef}
	if err := m.actor.PauseNow(key); err != nil {
		// The durable member gate is already closed. Keeping that safe state is
		// preferable to reopening work because the account projection failed.
		return paused, err
	}
	return paused, nil
}

func (m *Manager) Resume() (*store.ProductWorkflowRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrWorkflowNotActive
	}
	from := stateOf(run)
	now := m.clock.Now()
	decision, err := workflow.Resume(from, now, m.location, m.dailyWindow)
	if err != nil {
		return nil, err
	}
	if !decision.Changed {
		return run, nil
	}
	resumed, err := m.store.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
		RunID: run.RunID, From: from, To: decision.State, At: now,
	})
	if err != nil {
		return nil, err
	}
	key := store.AccountKey{Platform: run.Platform, AccountRef: run.AccountRef}
	var actorErr error
	if usesSourcingTargetHold(resumed) {
		// 恢复本地漏斗不等于恢复 IM actor。评分到招呼发送仍需保持
		// sourcingTargetReached hold，直到 communication 才由
		// keepCommunicationRunning 唯一地重新启用聊天巡检。
		actorErr = m.actor.HoldAfterSourcing(key)
	} else {
		actorErr = m.actor.EnableToday(key)
	}
	if actorErr != nil {
		_, rollbackErr := m.store.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
			RunID: run.RunID, From: decision.State, To: from, At: now,
		})
		return nil, errors.Join(actorErr, rollbackErr)
	}
	return resumed, nil
}

func usesSourcingTargetHold(run *store.ProductWorkflowRun) bool {
	if run == nil ||
		run.Mode != workflow.ModeFull ||
		run.SourcingBatchID == nil ||
		strings.TrimSpace(*run.SourcingBatchID) == "" {
		return false
	}
	switch run.Stage {
	case store.ProductWorkflowStageScoring,
		store.ProductWorkflowStageSelection,
		store.ProductWorkflowStageGreetingGeneration,
		store.ProductWorkflowStageAwaitingConfirmation,
		store.ProductWorkflowStageGreetingSending:
		return true
	default:
		return false
	}
}

// End requests a communication-only terminal handoff. It never interrupts the
// current candidate's advice/WAL/action chain; AdvanceOnce consumes the request
// under the patrol boundary and releases the active product slot afterwards.
func (m *Manager) End() (*store.ProductWorkflowRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrWorkflowNotActive
	}
	return m.store.RequestProductWorkflowPendingAction(
		store.RequestProductWorkflowPendingActionRequest{
			RunID:       run.RunID,
			Action:      store.ProductWorkflowPendingActionEnd,
			RequestedAt: m.clock.Now(),
		},
	)
}

// MayStartNextConversation is deliberately coarser than the shared member
// gate. A pending page handoff closes only the next candidate boundary; the
// current candidate may still finish every already-authorized advice/action
// stage and effect WAL.
func (m *Manager) MayStartNextConversation() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return false, err
	}
	if run == nil {
		return true, nil
	}
	return run.PendingAction == "", nil
}

// MayStartNextWorkflowMember is the one literal persistent boundary used by
// scoring, greeting generation and greeting sending. It never interrupts an
// already-created WAL intent.
func (m *Manager) MayStartNextWorkflowMember() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.clock.Now()
	run, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return err
	}
	if run == nil {
		open, err := m.dailyWindow.Evaluate(now, m.location)
		if err != nil {
			return err
		}
		if !open {
			return fmt.Errorf("%w: %s", ErrMemberStartBlocked, workflow.StatusWaitingDailyWindow)
		}
		return nil
	}
	// 用户已经请求结束时,下一个成员不再开工(2026-07-31 甲方裁决)。这是
	// "点了结束真的会停"的落点:编排器对 pendingAction 的收束发生在 Advance,
	// 而评分、生成招呼语、发招呼三个阶段各自在成员边界自转,这里不看的话,
	// 结束要等整批(可能一两个小时)跑完才生效。已铸的 WAL intent 不受影响,
	// 它们在自己的轨道上收束。
	if run.PendingAction == store.ProductWorkflowPendingActionEnd {
		return fmt.Errorf("%w: %s", ErrMemberStartBlocked, run.PendingAction)
	}
	from := stateOf(run)
	decision, err := workflow.MayStartNextWorkflowMember(
		from, now, m.location, m.dailyWindow,
	)
	if err != nil {
		return err
	}
	if decision.Changed {
		if _, err := m.store.TransitionProductWorkflowRun(
			store.TransitionProductWorkflowRunRequest{
				RunID: run.RunID, From: from, To: decision.State, At: now,
			},
		); err != nil {
			return err
		}
	}
	if !decision.Allowed {
		return fmt.Errorf("%w: %s", ErrMemberStartBlocked, decision.State.Status)
	}
	return nil
}

func (m *Manager) activeForStart(
	key store.AccountKey,
	mode workflow.Mode,
	now time.Time,
) (*store.ProductWorkflowRun, error) {
	current, err := m.store.ActiveProductWorkflowRun()
	if err != nil || current == nil {
		return current, err
	}
	if current.Platform != key.Platform || current.AccountRef != key.AccountRef {
		return nil, ErrWorkflowScopeConflict
	}
	currentState := stateOf(current)
	if _, err := workflow.Start(
		&currentState, mode, now, m.location, m.dailyWindow,
	); err != nil {
		return nil, err
	}
	return current, nil
}

func (m *Manager) failStart(
	run *store.ProductWorkflowRun,
	key store.AccountKey,
	cause error,
) error {
	pauseErr := m.actor.PauseNow(key)
	if run == nil {
		return errors.Join(cause, pauseErr)
	}
	from := stateOf(run)
	failed := workflow.State{Mode: run.Mode, Status: workflow.StatusFailed}
	_, persistErr := m.store.TransitionProductWorkflowRun(
		store.TransitionProductWorkflowRunRequest{
			RunID: run.RunID, From: from, To: failed, At: m.clock.Now(),
			Stage: store.ProductWorkflowStageFailed, Failure: cause.Error(),
		},
	)
	return errors.Join(cause, pauseErr, persistErr)
}

func stateOf(run *store.ProductWorkflowRun) workflow.State {
	return workflow.State{
		Mode: run.Mode, Status: run.Status, ResumeStatus: run.ResumeStatus,
	}
}
