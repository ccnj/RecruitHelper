// Package productapp connects the ordinary local UI to the durable product
// workflow. It owns configuration-plane synchronization and account selection,
// but delegates all business sequencing and effects to productworkflow.
package productapp

import (
	"context"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
)

var (
	ErrControllerInvalid    = errors.New("产品工作流控制器配置无效")
	ErrAccountUnavailable   = errors.New("没有唯一可运行的平台账号")
	ErrJobConfigUnavailable = errors.New("当前职位配置不可用")
	ErrJobSelectionChanged  = errors.New("当前职位已变化，请刷新后重试")
)

type Workflow interface {
	StartFull(store.AccountKey, string) (*store.ProductWorkflowRun, error)
	StartReplyOnly(store.AccountKey) (*store.ProductWorkflowRun, error)
	Pause() (*store.ProductWorkflowRun, error)
	Resume() (*store.ProductWorkflowRun, error)
	ConfirmAll(string, []string) (*store.ProductWorkflowRun, error)
}

type JobConfigSource interface {
	FetchCurrent(context.Context) ([]byte, error)
}

type Controller struct {
	store    *store.Store
	workflow Workflow
	source   JobConfigSource
	now      func() time.Time
}

type RuntimeState struct {
	Platform           string
	AccountRef         string
	CurrentBatchID     string
	WorkflowMode       string
	WorkflowStatus     string
	CommunicationState string
}

func New(
	db *store.Store,
	productWorkflow Workflow,
	source JobConfigSource,
	now func() time.Time,
) (*Controller, error) {
	if db == nil || productWorkflow == nil || source == nil {
		return nil, ErrControllerInvalid
	}
	if now == nil {
		now = time.Now
	}
	return &Controller{store: db, workflow: productWorkflow, source: source, now: now}, nil
}

func (c *Controller) Start(
	ctx context.Context,
	mode, expectedBackendJobID string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mode = strings.TrimSpace(mode)
	expectedBackendJobID = strings.TrimSpace(expectedBackendJobID)
	switch mode {
	case string(workflow.ModeReplyOnly):
		if expectedBackendJobID != "" {
			return ErrJobSelectionChanged
		}
	case string(workflow.ModeFull):
		if expectedBackendJobID == "" || len(expectedBackendJobID) > 128 {
			return ErrJobConfigUnavailable
		}
	default:
		return workflow.ErrInvalidMode
	}
	// Capture the user's click-time window before any backend request or
	// durable write. The workflow manager performs the second check at actual
	// start, so a 07:59 click cannot become an implicit 08:00 reservation and
	// a 23:59 click cannot cross midnight into a new run.
	requestedAt := c.now()
	open, err := workflow.EvaluateDailyWindow(requestedAt, time.Local)
	if err != nil {
		return err
	}
	if !open {
		return workflow.ErrDailyWindowClosed
	}

	key, err := c.currentAccount()
	if err != nil {
		return err
	}
	if mode == string(workflow.ModeReplyOnly) {
		_, err = c.workflow.StartReplyOnly(key)
		return err
	}

	// Repeated start and an unfinished batch are recovery paths. They already
	// own an immutable revision, so a transient old-backend outage must not
	// replace or strand that fact.
	active, loadErr := c.store.ActiveProductWorkflowRun()
	if loadErr != nil {
		return loadErr
	}
	var batch *store.SourcingBatch
	if active != nil && active.SourcingBatchID != nil {
		batch, loadErr = c.store.SourcingBatchByID(*active.SourcingBatchID)
	} else {
		batch, loadErr = c.store.ActiveSourcingBatch(key)
	}
	if loadErr != nil {
		return loadErr
	}
	if batch != nil {
		if batch.Platform != key.Platform || batch.AccountRef != key.AccountRef ||
			batch.BackendJobID == nil ||
			strings.TrimSpace(*batch.BackendJobID) != expectedBackendJobID {
			return ErrJobSelectionChanged
		}
		_, err = c.workflow.StartFull(key, batch.ContextRevisionHash)
		return err
	}
	if active != nil {
		return ErrJobConfigUnavailable
	}

	raw, err := c.source.FetchCurrent(ctx)
	if err != nil {
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	revisions, err := m5ai.ImportLegacyJobConfigFromBackend(raw, c.now())
	if err != nil || len(revisions) != 1 {
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	stored, err := c.store.SaveCurrentLegacyJobAIContext(revisions, c.now())
	if err != nil || len(stored) != 1 {
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	if strings.TrimSpace(stored[0].SourceJobRef) != expectedBackendJobID {
		return ErrJobSelectionChanged
	}
	_, err = c.workflow.StartFull(key, stored[0].RevisionHash)
	return err
}

func (c *Controller) Pause(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := c.workflow.Pause()
	return err
}

func (c *Controller) Resume(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := c.workflow.Resume()
	return err
}

func (c *Controller) ConfirmAll(
	ctx context.Context,
	batchID string,
	profileIDs []string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := c.workflow.ConfirmAll(batchID, profileIDs)
	return err
}

func (c *Controller) RuntimeState() (RuntimeState, error) {
	run, err := c.store.ActiveProductWorkflowRun()
	if err != nil {
		return RuntimeState{}, err
	}
	state := RuntimeState{}
	if run != nil {
		state.Platform = run.Platform
		state.AccountRef = run.AccountRef
		state.WorkflowMode = string(run.Mode)
		state.WorkflowStatus = string(run.Status)
		if run.SourcingBatchID != nil {
			state.CurrentBatchID = *run.SourcingBatchID
		}
		switch run.Status {
		case workflow.StatusPaused:
			state.CommunicationState = "paused"
		case workflow.StatusWaitingDailyWindow:
			state.CommunicationState = "waitingDailyWindow"
		default:
			state.CommunicationState, err = c.accountCommunicationState(store.AccountKey{
				Platform: run.Platform, AccountRef: run.AccountRef,
			})
			if err != nil {
				return RuntimeState{}, err
			}
		}
		return state, nil
	}

	key, err := c.currentAccount()
	if err != nil {
		if errors.Is(err, ErrAccountUnavailable) {
			return state, nil
		}
		return RuntimeState{}, err
	}
	state.Platform = key.Platform
	state.AccountRef = key.AccountRef
	if batch, loadErr := c.store.ActiveSourcingBatch(key); loadErr != nil {
		return RuntimeState{}, loadErr
	} else if batch != nil {
		state.CurrentBatchID = batch.BatchID
	}
	state.CommunicationState, err = c.accountCommunicationState(key)
	if err != nil {
		return RuntimeState{}, err
	}
	return state, nil
}

func (c *Controller) accountCommunicationState(key store.AccountKey) (string, error) {
	account, err := c.store.AccountByKey(key)
	if err != nil || account == nil {
		return "", err
	}
	now := c.now()
	localDate := now.In(time.Local).Format("2006-01-02")
	if account.EnabledDate == localDate && account.EnabledAt != nil &&
		account.StoppedAt == nil && account.PausedReason == "" {
		return "running", nil
	}
	if account.PausedReason != "" {
		return "paused", nil
	}
	return "idle", nil
}

func (c *Controller) currentAccount() (store.AccountKey, error) {
	if run, err := c.store.ActiveProductWorkflowRun(); err != nil {
		return store.AccountKey{}, err
	} else if run != nil {
		return store.AccountKey{Platform: run.Platform, AccountRef: run.AccountRef}, nil
	}
	accounts, err := c.store.Accounts()
	if err != nil {
		return store.AccountKey{}, err
	}
	var selected *store.Account
	for index := range accounts {
		account := &accounts[index]
		if strings.TrimSpace(account.BoundHandID) == "" ||
			account.PrincipalFingerprint == nil ||
			strings.TrimSpace(*account.PrincipalFingerprint) == "" {
			continue
		}
		if account.IdentityState != store.IdentityVerified &&
			account.IdentityState != store.IdentityUnobservable {
			continue
		}
		if selected != nil {
			return store.AccountKey{}, ErrAccountUnavailable
		}
		selected = account
	}
	if selected == nil {
		return store.AccountKey{}, ErrAccountUnavailable
	}
	return store.AccountKey{
		Platform: selected.Platform, AccountRef: selected.AccountRef,
	}, nil
}

var _ JobConfigSource = (*jobconfig.Source)(nil)
