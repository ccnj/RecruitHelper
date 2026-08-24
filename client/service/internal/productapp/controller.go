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
	ErrAccountUnavailable   = errors.New("没有可运行的平台账号")
	ErrJobConfigUnavailable = errors.New("当前职位配置不可用")
	ErrJobSelectionChanged  = errors.New("当前职位已变化，请刷新后重试")
	ErrHandUnavailable      = errors.New("浏览器插件不在线")
	ErrHandAmbiguous        = errors.New("多个浏览器插件在线")
	ErrLoginRequired        = errors.New("平台登录不可用")
	ErrWechatNotConfigured  = errors.New("平台个人中心尚未配置微信号")
	ErrWechatCheckFailed    = errors.New("微信号配置检查未完成")
)

// AccountResolver 在"开始"时探测当前 Chrome 登录的平台主体,按指纹找回既有
// 账本根或当场建档(2026-07-30 甲方裁决"账号跟随登录")。它是 effectful 入口的
// 精确解析;只读投影不探测,用 currentAccount 的最近验证启发式。
type AccountResolver interface {
	ResolveCurrent(ctx context.Context) (store.AccountKey, error)
}

// WechatSettingReader 在"开始"时经手读取平台个人中心的微信号配置是否已填
// (2026-08-18 甲方裁决,微信配置开工闸)。换微信链路把该配置发给同意交换的
// 候选人;配置空着时邀请发出后无号可给,所以开始前先查、没配就不放行。
type WechatSettingReader interface {
	ReadWechatConfigured(ctx context.Context, key store.AccountKey) (bool, error)
}

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
	// resolver 可以为 nil(既有测试只验证职位配置逻辑):此时 Start 退回
	// currentAccount 的库内扫描。生产装配始终注入,见 main.go。
	resolver AccountResolver
	// providerApplied 在模型配置落盘成功后被调用(可为 nil),由 main 装配为
	// "重建引擎并换代",落盘即生效(2026-08-12 甲方裁决)。
	providerApplied func()
	// smartProviderConfig/smartProviderApplied 是发布专用「聪明ai」凭据的对应
	// 一对(AGENTS.md「LLM provider 直连」2026-08-24 增补),随同一次 job-config
	// 拉取从响应顶层 smartAi 块刷新;可为 nil,刷新函数对 nil 安全。
	smartProviderConfig  *m5ai.ProviderConfigStore
	smartProviderApplied func()
	// wechatReader 可以为 nil(既有测试不注入,行为同闸引入前)。生产装配始终
	// 注入,见 main.go。
	wechatReader WechatSettingReader
}

// SetAccountResolver 注入"开始"时的账号解析器(装配期一次,非并发安全)。
func (c *Controller) SetAccountResolver(resolver AccountResolver) *Controller {
	c.resolver = resolver
	return c
}

// SetWechatSettingReader 注入微信配置开工闸的读取器(装配期一次,非并发安全)。
func (c *Controller) SetWechatSettingReader(reader WechatSettingReader) *Controller {
	c.wechatReader = reader
	return c
}

// SetProviderApplied 注入模型配置落盘后的引擎换代回调(装配期一次,非并发安全)。
func (c *Controller) SetProviderApplied(fn func()) *Controller {
	c.providerApplied = fn
	return c
}

// SetSmartProviderStore 注入聪明ai配置落盘与其换代回调(装配期一次,非并发安全)。
func (c *Controller) SetSmartProviderStore(store *m5ai.ProviderConfigStore, onApplied func()) *Controller {
	c.smartProviderConfig = store
	c.smartProviderApplied = onApplied
	return c
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
	// LastRunFailureReason 只在没有活跃运行、且最近一次运行以失败终局时携带
	// 该次的失败原因原文。运行失败即终局,首页否则永远看不到"为什么停了"
	// (2026-08-12 甲方要求,起因是推荐流被刷新后批次静默作废)。
	LastRunFailureReason string
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
	// start, so a 06:59 click cannot become an implicit 07:00 reservation and
	// a 23:59 click cannot cross midnight into a new run.
	requestedAt := c.now()
	open, err := c.dailyWindow.Evaluate(requestedAt, time.Local)
	if err != nil {
		return err
	}
	if !open {
		return workflow.ErrDailyWindowClosed
	}

	key, err := c.startAccount(ctx)
	if err != nil {
		return err
	}
	if err := c.gateWechatConfigured(ctx, key); err != nil {
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
		logCurrentJobSyncFailure("start", "fetch", err, -1)
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	m5ai.RefreshBackendProviderConfig(c.providerConfig, raw, c.providerApplied)
	m5ai.RefreshSmartProviderConfig(c.smartProviderConfig, raw, c.smartProviderApplied)
	revisions, err := m5ai.ImportLegacyJobConfigFromBackend(raw, c.now())
	if err != nil || len(revisions) != 1 {
		logCurrentJobSyncFailure("start", "import", err, len(revisions))
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	// 先刷有效集再落当前职位:SaveCurrentLegacyJobAIContext 只加不减,这个顺序
	// 保证当前工作职位一定留在有效集里,不必为"复数响应恰好不含当前职位"另写
	// 一条保护分支。
	c.SyncEffectiveJobs(ctx)
	stored, err := c.store.SaveCurrentLegacyJobAIContext(revisions, c.now())
	if err != nil || len(stored) != 1 {
		logCurrentJobSyncFailure("start", "persist", err, len(stored))
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	// 全新开始一律跑后台此刻选中的职位,即使它与页面上显示的那个已经不是同一个
	// (2026-08-10 甲方裁决)。此处原先拿页面带上来的职位 ID 与刚拉到的后台职位
	// 比对,不一致就拒绝,要人先点"同步职位"再点一次开始。甲方的心智是"跑后台
	// 选的那个职位",这道拦截对他只是一次多余往返。
	//
	// 页面不会因此显示错的职位:开始成功后前端立即重拉全量数据,首页职位随之
	// 变成实际执行的那个。刚落库的 stored[0] 就是这次要跑的 revision,页面读到
	// 的与脑执行的是同一份事实。
	//
	// 有未终局批次的那条路不适用本裁决,仍在上面按批次锚定的职位拦截:那批人
	// 已经采下来、可能已经建档,换职位会让他们用一个职位的话术挂在另一个职位
	// 名下。要换职位得先把旧批次结束掉。
	_, err = c.workflow.StartFull(key, stored[0].RevisionHash)
	return err
}

// gateWechatConfigured 是微信配置开工闸(2026-08-18 甲方裁决):平台个人中心
// 没填微信号时换微信链路无号可给,开始一律拦下。只在"这次点击会开启新工作"
// 时检查——存在活跃工作流或未终局采集批次说明创建那份工作的点击已过闸,且
// 此时导航去个人中心会打扰运行现场(推荐页运行连续性禁止批次运行期导航走)。
// 读不到与未配置同向处理(不放行);误拦由用户补配置后重试收敛,检查本身
// 不自动重试。
func (c *Controller) gateWechatConfigured(ctx context.Context, key store.AccountKey) error {
	if c.wechatReader == nil {
		return nil
	}
	if run, err := c.store.ActiveProductWorkflowRun(); err != nil {
		return err
	} else if run != nil {
		return nil
	}
	if batch, err := c.store.ActiveSourcingBatch(key); err != nil {
		return err
	} else if batch != nil {
		return nil
	}
	configured, err := c.wechatReader.ReadWechatConfigured(ctx, key)
	if err != nil {
		// 手离线/多手是既有哨兵,保持原文案;其余失败统一归"检查未完成"。
		if errors.Is(err, ErrHandUnavailable) || errors.Is(err, ErrHandAmbiguous) {
			return err
		}
		slog.Warn("微信配置检查失败，拒绝开始",
			"errorCode", "wechatGateReadFailed", "err", err)
		return errors.Join(ErrWechatCheckFailed, err)
	}
	if !configured {
		slog.Info("平台个人中心未配置微信号，拒绝开始",
			"errorCode", "wechatGateNotConfigured")
		return ErrWechatNotConfigured
	}
	return nil
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
		logCurrentJobSyncFailure("syncJobs", "fetch", err, -1)
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	m5ai.RefreshBackendProviderConfig(c.providerConfig, raw, c.providerApplied)
	m5ai.RefreshSmartProviderConfig(c.smartProviderConfig, raw, c.smartProviderApplied)
	revisions, err := m5ai.ImportLegacyJobConfigFromBackend(raw, c.now())
	if err != nil || len(revisions) != 1 {
		logCurrentJobSyncFailure("syncJobs", "import", err, len(revisions))
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	// 与 Start 同序:先刷有效集,再落当前职位。
	c.SyncEffectiveJobs(ctx)
	if _, err := c.store.SaveCurrentLegacyJobAIContext(revisions, c.now()); err != nil {
		logCurrentJobSyncFailure("syncJobs", "persist", err, -1)
		return errors.Join(ErrJobConfigUnavailable, err)
	}
	return nil
}

// logCurrentJobSyncFailure 让当前职位同步失败在脑日志里可定位。产品面响应按
// 数据边界只给固定文案,导入失败的具体原因(缺哪个文档、缺哪个占位符)此前哪里
// 都不记,新客户配置不合格时只能人肉对后台——2026-08-01 真机装机正是这样卡住的。
// 错误文本只含文档类型名与占位符名,不含 prompt 正文、候选人内容或密钥。
func logCurrentJobSyncFailure(entry, stage string, err error, count int) {
	attrs := []any{"entry", entry, "stage", stage}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	if count >= 0 {
		attrs = append(attrs, "revisionCount", count)
	}
	slog.Warn("当前职位同步失败", attrs...)
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
		// 结束在漏斗阶段同样可用(2026-07-31 甲方裁决)。此前只认沟通阶段,
		// 于是漏斗跑着的一两个小时里用户面前一个可点的东西都没有——想停只
		// 能关客户端。结束本身不是硬杀:它写一个 pendingAction,由编排器在
		// 候选人/成员边界收束,已铸的 WAL intent 照常走完。
		state.CanEnd = run.Stage != store.ProductWorkflowStageCompleted &&
			run.Stage != store.ProductWorkflowStageFailed &&
			(run.Status == workflow.StatusRunning ||
				run.Status == workflow.StatusPaused ||
				run.Status == workflow.StatusWaitingDailyWindow ||
				run.Status == workflow.StatusAwaitingConfirmation) &&
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

	if latest, latestErr := c.store.LatestProductWorkflowRun(); latestErr != nil {
		return RuntimeState{}, latestErr
	} else if latest != nil && latest.Status == workflow.StatusFailed {
		state.LastRunFailureReason = latest.FailureReason
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

// startAccount 是"开始"这一 effectful 入口的账号解析:优先探测当前 Chrome
// 登录的主体(账号跟随登录,2026-07-30 裁决)。运行中的工作流仍钉住自己的账号,
// 追加批次不得因用户中途切号而漂移。
func (c *Controller) startAccount(ctx context.Context) (store.AccountKey, error) {
	if run, err := c.store.ActiveProductWorkflowRun(); err != nil {
		return store.AccountKey{}, err
	} else if run != nil {
		return store.AccountKey{Platform: run.Platform, AccountRef: run.AccountRef}, nil
	}
	if c.resolver != nil {
		return c.resolver.ResolveCurrent(ctx)
	}
	return c.currentAccount()
}

// currentAccount 是只读投影的账号启发式:不探测页面,取库内最近一次身份验证
// 通过的账号。巡检每轮成功探测都会刷新 IdentityVerifiedAt,所以多账号来回切换
// 时它自动收敛到真实登录的那一个;两次开始之间的短暂窗口里可能显示上一个号,
// 属可接受的展示偏差——所有 effectful 路径都走 startAccount 的精确探测,
// 不依赖本启发式。旧的"全库恰好一个账号"规则已随账号跟随登录退役。
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
		if selected == nil || accountVerifiedAfter(account, selected) {
			selected = account
		}
	}
	if selected == nil {
		return store.AccountKey{}, ErrAccountUnavailable
	}
	return store.AccountKey{
		Platform: selected.Platform, AccountRef: selected.AccountRef,
	}, nil
}

// accountVerifiedAfter 比较两个账号的最近验证时刻,时刻相同或都缺失时按
// AccountRef 字典序保证确定性。
func accountVerifiedAfter(candidate, incumbent *store.Account) bool {
	candidateAt, incumbentAt := time.Time{}, time.Time{}
	if candidate.IdentityVerifiedAt != nil {
		candidateAt = *candidate.IdentityVerifiedAt
	}
	if incumbent.IdentityVerifiedAt != nil {
		incumbentAt = *incumbent.IdentityVerifiedAt
	}
	if candidateAt.Equal(incumbentAt) {
		return candidate.AccountRef < incumbent.AccountRef
	}
	return candidateAt.After(incumbentAt)
}

var _ JobConfigSource = (*jobconfig.Source)(nil)
