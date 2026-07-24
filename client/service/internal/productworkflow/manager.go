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

const NewFullWorkflowTargetCount = 30

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
	StartSourcing(store.AccountKey, string, int) error
	EnableToday(store.AccountKey) error
	PauseNow(store.AccountKey) error
}

type memberGateInstaller interface {
	SetWorkflowMemberGate(func() error)
}

type Config struct {
	Clock    Clock
	Location *time.Location
}

type Manager struct {
	store    *store.Store
	actor    Actor
	clock    Clock
	location *time.Location
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
		confirmationProjection: db.AppConfirmation,
	}
	if installer, ok := actor.(memberGateInstaller); ok {
		installer.SetWorkflowMemberGate(manager.MayStartNextWorkflowMember)
	}
	return manager, nil
}

// StartFull starts a new full workflow at 30 candidates, except when the
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
	if current, err := m.activeForStart(key, workflow.ModeFull, now); current != nil || err != nil {
		return current, err
	}

	activeBatch, err := m.store.ActiveSourcingBatch(key)
	if err != nil {
		return nil, err
	}
	revisionHash := contextRevisionHash
	targetCount := NewFullWorkflowTargetCount
	if activeBatch != nil {
		revisionHash = activeBatch.ContextRevisionHash
		targetCount = activeBatch.TargetCount
	}
	if revisionHash == "" {
		return nil, store.ErrJobAIContextRevisionInvalid
	}

	decision, err := workflow.Start(nil, workflow.ModeFull, now, m.location)
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
	if err := m.actor.StartSourcing(key, revisionHash, targetCount); err != nil {
		return nil, m.failStart(run, key, err)
	}
	activeBatch, err = m.store.ActiveSourcingBatch(key)
	if err != nil {
		return nil, m.failStart(run, key, err)
	}
	if activeBatch == nil || activeBatch.TargetCount != targetCount ||
		activeBatch.ContextRevisionHash != revisionHash {
		return nil, m.failStart(run, key, store.ErrSourcingBatchConflict)
	}
	attached, err := m.store.AttachProductWorkflowSourcingBatch(run.RunID, activeBatch.BatchID)
	if err != nil {
		return nil, m.failStart(run, key, err)
	}
	return attached, nil
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
	decision, err := workflow.Start(nil, workflow.ModeReplyOnly, now, m.location)
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
	decision, err := workflow.Resume(from, now, m.location)
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
	if err := m.actor.EnableToday(key); err != nil {
		_, rollbackErr := m.store.TransitionProductWorkflowRun(store.TransitionProductWorkflowRunRequest{
			RunID: run.RunID, From: decision.State, To: from, At: now,
		})
		return nil, errors.Join(err, rollbackErr)
	}
	return resumed, nil
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
		open, err := workflow.EvaluateDailyWindow(now, m.location)
		if err != nil {
			return err
		}
		if !open {
			return fmt.Errorf("%w: %s", ErrMemberStartBlocked, workflow.StatusWaitingDailyWindow)
		}
		return nil
	}
	from := stateOf(run)
	decision, err := workflow.MayStartNextWorkflowMember(from, now, m.location)
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
	if _, err := workflow.Start(&currentState, mode, now, m.location); err != nil {
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
