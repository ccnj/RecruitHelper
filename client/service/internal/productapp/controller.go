// Package productapp connects the ordinary local UI to the durable product
// workflow. It owns configuration-plane synchronization and account selection,
// but delegates all business sequencing and effects to productworkflow.
package productapp

import (
	"context"
	"errors"
	"log/slog"
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
	End() (*store.ProductWorkflowRun, error)
	ConfirmAll(string, []string) (*store.ProductWorkflowRun, error)
}

type JobConfigSource interface {
	FetchCurrent(context.Context) ([]byte, error)
	FetchAll(context.Context) ([]byte, error)
}

type Controller struct {
	store       *store.Store
	workflow    Workflow
	source      JobConfigSource
	now         func() time.Time
	dailyWindow workflow.DailyWindowPolicy
	// providerConfig 可以为 nil(既有测试构造不注入)。凡是本控制器拉过一次
	// job-config,就顺手刷新 provider 凭据,免得后台换了 key 还要进诊断台。
	providerConfig *m5ai.ProviderConfigStore
}

type RuntimeState struct {
	Platform              string
	AccountRef            string
	CurrentBatchID        string
	WorkflowMode          string
	WorkflowStatus        string
	WorkflowStage         string
	WorkflowPendingAction string
	CanAddBatch           bool
	CanEnd                bool
	CommunicationState    string
}

func New(
	db *store.Store,
	productWorkflow Workflow,
	source JobConfigSource,
	now func() time.Time,
	dailyWindow workflow.DailyWindowPolicy,
	providerConfig ...*m5ai.ProviderConfigStore,
) (*Controller, error) {
	if db == nil || productWorkflow == nil || source == nil {
		return nil, ErrControllerInvalid
	}
	if len(providerConfig) > 1 {
		return nil, ErrControllerInvalid
	}
	if now == nil {
		now = time.Now
	}
	controller := &Controller{
		store: db, workflow: productWorkflow, source: source, now: now,
		dailyWindow: dailyWindow,
	}
	if len(providerConfig) == 1 {
		controller.providerConfig = providerConfig[0]
	}
	return controller, nil
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
	open, err := c.dailyWindow.Evaluate(requestedAt, time.Local)
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
	additionalBatch := active != nil &&
		active.Stage == store.ProductWorkflowStageCommunication &&
		(active.Status == workflow.StatusRunning || active.Status == workflow.StatusPaused)
	var batch *store.SourcingBatch
	if active != nil && active.SourcingBatchID != nil && !additionalBatch {
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
	if active != nil && !additionalBatch {
		return ErrJobConfigUnavailable
	}

	raw, err := c.source.FetchCurrent(ctx)
	if err != nil {
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	m5ai.RefreshBackendProviderConfig(c.providerConfig, raw)
	revisions, err := m5ai.ImportLegacyJobConfigFromBackend(raw, c.now())
	if err != nil || len(revisions) != 1 {
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	// 先刷有效集再落当前职位:SaveCurrentLegacyJobAIContext 只加不减,这个顺序
	// 保证当前工作职位一定留在有效集里,不必为"复数响应恰好不含当前职位"另写
	// 一条保护分支。
	c.SyncEffectiveJobs(ctx)
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

// SyncJobs 是产品面"同步职位"的入口:刷新有效职位集,并把旧后台当前职位重新
// 拉取落库,与开始按钮之前那次同步同形。
//
// 它不启动、不恢复任何工作流,也不改写已冻结批次的 revision 绑定与已建档候选人
// 的职位归属——那些是不可变事实。后台当前职位若在运行期间变过,下一次开始仍由
// 既有的 ErrJobSelectionChanged 拦住,本入口不替它做裁决。
//
// 不受统一业务运行窗口约束:同步配置不产生任何候选人可见动作,也不创建新链。
func (c *Controller) SyncJobs(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := c.source.FetchCurrent(ctx)
	if err != nil {
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	m5ai.RefreshBackendProviderConfig(c.providerConfig, raw)
	revisions, err := m5ai.ImportLegacyJobConfigFromBackend(raw, c.now())
	if err != nil || len(revisions) != 1 {
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	// 与 Start 同序:先刷有效集,再落当前职位。
	c.SyncEffectiveJobs(ctx)
	if _, err := c.store.SaveCurrentLegacyJobAIContext(revisions, c.now()); err != nil {
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	return nil
}

// SyncEffectiveJobs 刷新有效职位集,并刻意不向业务主线返回错误。
//
// 有效集只决定"主动来聊的候选人能否被自动建档",一次配置面故障不该阻断用户
// 点下的开始。任何失败路径都保持既有集合原样:用一次网络抖动把全部职位清空,
// 会让所有入站候选人集体 noMatch,方向比"晚一轮才接上"坏得多。失败必须响亮,
// 因此每条不合格职位都单独告警——运营要据此知道去后台补哪个职位的配置。
func (c *Controller) SyncEffectiveJobs(ctx context.Context) {
	raw, err := c.source.FetchAll(ctx)
	if err != nil {
		slog.Warn("有效职位集同步失败，保持既有集合", "error", err)
		return
	}
	revisions, skipped, err := m5ai.ImportLegacyJobConfigsTolerant(raw, c.now())
	for index := range skipped {
		slog.Warn("职位配置不合格，未进入有效职位集",
			"jobIndex", skipped[index].Index,
			"backendJobId", skipped[index].SourceJobRef,
			"reason", skipped[index].Reason,
		)
	}
	if err != nil {
		slog.Warn("有效职位集整包无效，保持既有集合", "error", err)
		return
	}
	stored, err := c.store.SaveEffectiveLegacyJobAIContexts(revisions, c.now())
	if err != nil {
		slog.Warn("有效职位集写入失败，保持既有集合", "error", err)
		return
	}
	slog.Info("有效职位集已刷新",
		"eligible", len(stored), "skipped", len(skipped))
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

func (c *Controller) End(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := c.workflow.End()
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
		state.WorkflowStage = run.Stage
		state.WorkflowPendingAction = string(run.PendingAction)
		state.CanAddBatch = run.Stage == store.ProductWorkflowStageCommunication &&
			(run.Status == workflow.StatusRunning || run.Status == workflow.StatusPaused) &&
			run.PendingAction == ""
		state.CanEnd = run.Stage == store.ProductWorkflowStageCommunication &&
			(run.Status == workflow.StatusRunning ||
				run.Status == workflow.StatusPaused ||
				run.Status == workflow.StatusWaitingDailyWindow) &&
			run.PendingAction == ""
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
