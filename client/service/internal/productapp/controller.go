// Package productapp connects the ordinary local UI to the durable product
// workflow. It owns configuration-plane synchronization and account selection,
// but delegates all business sequencing and effects to productworkflow.
package productapp

import (
	"context"
	"errors"
	"fmt"
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
	// ErrHandCapabilityMissing:该平台的插件未声明/未实现这条原语(2026-09-02 甲方裁决,
	// hello 按平台声明能力)。开工闸类与尽力而为类调用遇到它一律"跳过并留痕",不算失败、
	// 不重试;由 appbridge 把脑闸 ErrCapability 与手侧 PROTO_UNSUPPORTED_CMD 两种信号翻成它。
	ErrHandCapabilityMissing = errors.New("该平台的插件未实现此能力")
)

// DefaultPlatform 是客户快照缺席平台字段时的默认值(2026-09-02 甲方裁决,模型 1 底稿 1.4):
// 本产品此前只随智联交付,缺席即旧世界。它是部署默认值,不是业务判断;appbridge 的手侧
// 回落与它是同一个事实。
const DefaultPlatform = "zhilian"

// LoginRequiredError 是 ErrLoginRequired 的带平台形态:文案要按平台说"去登录哪个端"。
// errors.Is 仍命中 ErrLoginRequired。
type LoginRequiredError struct {
	Platform string
}

func (e *LoginRequiredError) Error() string {
	return ErrLoginRequired.Error() + ": " + e.Platform
}

func (e *LoginRequiredError) Unwrap() error { return ErrLoginRequired }

// CustomerPlatformSource 读本地客户快照里的平台归属(模型 1):由 jobconfig.Source 实现,
// 随 bind 与每次职位配置同步刷新;返回空串表示快照缺席,由解析器按 DefaultPlatform 处理。
type CustomerPlatformSource interface {
	CustomerPlatform() string
}

// AccountResolver 在"开始"时探测当前 Chrome 登录的平台主体,按指纹找回既有
// 账本根或当场建档(2026-07-30 甲方裁决"账号跟随登录")。它是 effectful 入口的
// 精确解析;只读投影不探测,用 currentAccount 的最近验证启发式。
type AccountResolver interface {
	// platform 来自客户快照(模型 1);为空由解析器按 DefaultPlatform 处理。
	ResolveCurrent(ctx context.Context, platform string) (store.AccountKey, error)
}

// WechatSettingReader 在"开始"时经手读取平台个人中心的微信号配置是否已填
// (2026-08-18 甲方裁决,微信配置开工闸)。换微信链路把该配置发给同意交换的
// 候选人;配置空着时邀请发出后无号可给,所以开始前先查、没配就不放行。
type WechatSettingReader interface {
	ReadWechatConfigured(ctx context.Context, key store.AccountKey) (bool, error)
}

// NoticeCollector 在"开始"时经手读取平台个人中心「通知」页签第一页并上报旧后台
// (2026-09-02 甲方裁决,平台通知上报,第十一项云端出站)。它不是闸:失败只记
// 日志,不影响开始;只在与微信闸相同的前置下调用(这次点击会开启新工作)。
type NoticeCollector interface {
	CollectNotices(ctx context.Context, key store.AccountKey) error
}

type Workflow interface {
	StartFull(store.AccountKey, string) (*store.ProductWorkflowRun, error)
	StartFullDailyPlan(store.AccountKey) (*store.ProductWorkflowRun, error)
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
	// subSmartProviderConfig/subSmartProviderApplied 是回复族专用「次聪明ai」
	// 的对应一对(同条款 2026-08-24 增补),来自响应顶层 subSmartAi 块。
	subSmartProviderConfig  *m5ai.ProviderConfigStore
	subSmartProviderApplied func()
	// wechatReader 可以为 nil(既有测试不注入,行为同闸引入前)。生产装配始终
	// 注入,见 main.go。
	wechatReader WechatSettingReader
	// noticeCollector 可以为 nil(既有测试不注入)。生产装配始终注入,见 main.go。
	noticeCollector NoticeCollector
	// platformSource 读客户快照的平台归属(模型 1);nil 或空串按 DefaultPlatform。
	platformSource CustomerPlatformSource
}

// SetAccountResolver 注入"开始"时的账号解析器(装配期一次,非并发安全)。
// SetCustomerPlatformSource 注入客户快照平台读取器(装配期一次,非并发安全)。可以为 nil
// (既有测试不注入):此时按 DefaultPlatform,与后台未下发平台字段的行为相同。
func (c *Controller) SetCustomerPlatformSource(source CustomerPlatformSource) *Controller {
	c.platformSource = source
	return c
}

func (c *Controller) SetAccountResolver(resolver AccountResolver) *Controller {
	c.resolver = resolver
	return c
}

// SetWechatSettingReader 注入微信配置开工闸的读取器(装配期一次,非并发安全)。
func (c *Controller) SetWechatSettingReader(reader WechatSettingReader) *Controller {
	c.wechatReader = reader
	return c
}

// SetNoticeCollector 注入平台通知读取上报器(装配期一次,非并发安全)。
func (c *Controller) SetNoticeCollector(collector NoticeCollector) *Controller {
	c.noticeCollector = collector
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

// SetSubSmartProviderStore 注入次聪明ai配置落盘与其换代回调(装配期一次,非并发安全)。
func (c *Controller) SetSubSmartProviderStore(store *m5ai.ProviderConfigStore, onApplied func()) *Controller {
	c.subSmartProviderConfig = store
	c.subSmartProviderApplied = onApplied
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
		// 当日职位计划(2026-09-01):完整流程不再绑定单一职位,页面带上来的
		// 职位 ID 仅作兼容接收、一律忽略(超长仍拒绝,防误传)。
		if len(expectedBackendJobID) > 128 {
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
	c.collectNoticesBestEffort(ctx, key)
	if mode == string(workflow.ModeReplyOnly) {
		c.syncJobsBestEffort(ctx, "startReplyOnly")
		_, err = c.workflow.StartReplyOnly(key)
		return err
	}

	// 当日职位计划(AGENTS.md 2026-09-01):完整流程一律按计划跑。活跃运行的
	// 幂等返回与未终局批次的收养都在 StartFullDailyPlan 内处理。无活跃运行时
	// 一律先刷新配置面(不看有没有未终局批次——那个读数在锁外,收口扫描可能
	// 正把批次终局化,靠它决定跳过同步会让 StartFullDailyPlan 用陈旧名单建
	// 计划):顺序是先尽力而为地刷当前职位 head 与 provider 凭据,再做复数
	// 同步——复数同步是名单的最终裁决,后台已剔除的"当前职位"不得经回填的
	// 「只加不减」重新获得建档资格。复数同步失败时:没有可收养的批次就拒绝
	// 开始(名单硬前提);有未终局批次则按既有恢复语义继续(收养不需要名单,
	// 后台断网不该把恢复也堵死)。
	active, loadErr := c.store.ActiveProductWorkflowRun()
	if loadErr != nil {
		return loadErr
	}
	if active == nil {
		if raw, fetchErr := c.source.FetchCurrent(ctx); fetchErr != nil {
			logCurrentJobSyncFailure("start", "fetch", fetchErr, -1)
		} else {
			m5ai.RefreshBackendProviderConfig(c.providerConfig, raw, c.providerApplied)
			m5ai.RefreshSmartProviderConfig(c.smartProviderConfig, raw, c.smartProviderApplied)
			m5ai.RefreshSubSmartProviderConfig(c.subSmartProviderConfig, raw, c.subSmartProviderApplied)
			revisions, importErr := m5ai.ImportLegacyJobConfigFromBackend(raw, c.now())
			if importErr != nil || len(revisions) != 1 {
				logCurrentJobSyncFailure("start", "import", importErr, len(revisions))
			} else if _, persistErr := c.store.SaveCurrentLegacyJobAIContext(revisions, c.now()); persistErr != nil {
				logCurrentJobSyncFailure("start", "persist", persistErr, 1)
			}
		}
		if syncErr := c.syncEffectiveJobsStrict(ctx); syncErr != nil {
			batch, loadErr := c.store.ActiveSourcingBatch(key)
			if loadErr != nil {
				return loadErr
			}
			if batch == nil {
				return errors.Join(ErrJobConfigUnavailable, syncErr)
			}
			slog.Warn("有效职位集同步失败,按既有未终局批次恢复继续",
				"batchId", batch.BatchID, "err", syncErr.Error())
		}
	} else {
		// 活跃运行在场时同样尽力全刷(2026-09-01 甲方裁决:所有开始/恢复
		// 入口全刷)。刷新只推进 head 与引擎凭据,让在聊候选人下一轮用上
		// 最新提示词;已冻结批次与当日计划的 revision 绑定是不可变事实,
		// 不受影响。失败不拦——接续/幂等返回不需要名单。
		c.syncJobsBestEffort(ctx, "startFullActive")
	}
	_, err = c.workflow.StartFullDailyPlan(key)
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
	if fresh, err := c.startsNewWork(key); err != nil {
		return err
	} else if !fresh {
		return nil
	}
	configured, err := c.wechatReader.ReadWechatConfigured(ctx, key)
	if err != nil {
		// 该平台的插件没有这条原语(2026-09-02 甲方裁决 2.2):开工闸按"跳过并留痕"放行。
		// 脑闸拒绝发生在记账之前、cmd_records 无痕,留痕只能在这里写。放行的方向由裁决定:
		// BOSS 第一刀既无 readWechatSetting 也无换微信原语,跳过闸是安全的;将来 BOSS 补了
		// 换微信而没补本原语,这道闸会被静默跳过——届时应先补能力再开换微信,不能靠闸。
		if errors.Is(err, ErrHandCapabilityMissing) {
			slog.Info("微信配置闸:该平台无此能力,跳过",
				"errorCode", "wechatGateCapabilitySkipped", "platform", key.Platform, "err", err)
			c.store.Audit("wechat_gate_capability_skipped", "", "",
				fmt.Sprintf("platform=%s primitive=account.readWechatSetting@1 %v", key.Platform, err))
			return nil
		}
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

// startsNewWork 判定这次点击是否会开启新工作:没有活跃工作流、也没有未终局
// 采集批次。微信闸与平台通知读取共用这一个判据——两者都要导航去个人中心,
// 而批次运行期不得离开推荐页(推荐页运行连续性)。
func (c *Controller) startsNewWork(key store.AccountKey) (bool, error) {
	if run, err := c.store.ActiveProductWorkflowRun(); err != nil {
		return false, err
	} else if run != nil {
		return false, nil
	}
	if batch, err := c.store.ActiveSourcingBatch(key); err != nil {
		return false, err
	} else if batch != nil {
		return false, nil
	}
	return true, nil
}

// collectNoticesBestEffort 是平台通知上报的开始入口消费点(2026-09-02 甲方裁决):
// 微信闸通过之后、工作流创建之前同步读一次个人中心「通知」页签第一页。同步是
// 硬要求——它要导航页面,必须在批次读推荐流之前收束。它不是闸:读不到、手离线、
// 上报失败一律只记日志,开始照常;不重试,下次开始自愈。
func (c *Controller) collectNoticesBestEffort(ctx context.Context, key store.AccountKey) {
	if c.noticeCollector == nil {
		return
	}
	fresh, err := c.startsNewWork(key)
	if err != nil {
		slog.Warn("平台通知读取前置判定失败,跳过本次(不影响开始)",
			"errorCode", "noticeCollectSkipped", "err", err)
		return
	}
	if !fresh {
		return
	}
	if err := c.noticeCollector.CollectNotices(ctx, key); err != nil {
		if errors.Is(err, ErrHandCapabilityMissing) {
			// 不是失败,是该平台没这条原语(2026-09-02 甲方裁决 2.2):Info 级并留审计行。
			slog.Info("平台通知读取:该平台无此能力,跳过",
				"errorCode", "noticeCollectCapabilitySkipped", "platform", key.Platform, "err", err)
			c.store.Audit("notice_collect_capability_skipped", "", "",
				fmt.Sprintf("platform=%s primitive=account.readNotices@1 %v", key.Platform, err))
			return
		}
		slog.Warn("平台通知读取失败,跳过本次(不影响开始)",
			"errorCode", "noticeCollectFailed", "err", err)
	}
}

// SyncJobs 是产品面"同步职位"的入口:刷新有效职位集,并把旧后台当前职位重新
// 拉取落库,与开始按钮之前那次同步同形。仅回复开始、全流程接续与恢复也经
// syncJobsBestEffort 复用本函数(2026-09-01 甲方裁决:所有开始/恢复入口全刷)。
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
	m5ai.RefreshSubSmartProviderConfig(c.subSmartProviderConfig, raw, c.subSmartProviderApplied)
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

// syncJobsBestEffort 在仅回复开始、全流程接续与恢复前尽力做一次与「同步职位」
// 完全相同的全量刷新(2026-09-01 甲方裁决),让在聊候选人的下一轮尽快用上最新
// 提示词与引擎配置。失败只响亮记日志、按本地既有配置继续——后台一次故障不得
// 把开始/恢复堵死;全流程首开的名单硬前提(复数同步失败即拒绝)不经本路径。
func (c *Controller) syncJobsBestEffort(ctx context.Context, entry string) {
	if err := c.SyncJobs(ctx); err != nil {
		slog.Warn("配置面尽力刷新失败,按本地既有配置继续",
			"entry", entry, "err", err.Error())
	}
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
	if err := c.syncEffectiveJobsStrict(ctx); err != nil {
		slog.Warn("有效职位集同步失败，保持既有集合", "error", err)
	}
}

// syncEffectiveJobsStrict 是当日职位计划的名单硬前提:失败即整体报错,由
// 调用方决定是否阻断(开始阻断,后台巡检类调用只告警)。
func (c *Controller) syncEffectiveJobsStrict(ctx context.Context) error {
	raw, err := c.source.FetchAll(ctx)
	if err != nil {
		// 留痕条款:对外收窄为固定文案之前,完整失败现场先落日志。
		slog.Warn("有效职位集拉取失败", "stage", "fetchAll", "err", err.Error())
		return fmt.Errorf("有效职位集拉取失败: %w", err)
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
		slog.Warn("有效职位集整包无效", "stage", "import", "err", err.Error())
		return fmt.Errorf("有效职位集整包无效: %w", err)
	}
	stored, err := c.store.SaveEffectiveLegacyJobAIContexts(revisions, c.now())
	if err != nil {
		slog.Warn("有效职位集写入失败", "stage", "persist", "err", err.Error())
		return fmt.Errorf("有效职位集写入失败: %w", err)
	}
	slog.Info("有效职位集已刷新",
		"eligible", len(stored), "skipped", len(skipped))
	return nil
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
	c.syncJobsBestEffort(ctx, "resume")
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
//
// 平台来自客户快照(2026-09-02 甲方裁决,模型 1):人工点击与每日自动开始同一入口、同一
// 平台来源;运行中的工作流仍钉自己的账号,快照此时变了也不影响本次运行。
func (c *Controller) startAccount(ctx context.Context) (store.AccountKey, error) {
	if run, err := c.store.ActiveProductWorkflowRun(); err != nil {
		return store.AccountKey{}, err
	} else if run != nil {
		return store.AccountKey{Platform: run.Platform, AccountRef: run.AccountRef}, nil
	}
	if c.resolver != nil {
		return c.resolver.ResolveCurrent(ctx, c.customerPlatform())
	}
	return c.currentAccount()
}

func (c *Controller) customerPlatform() string {
	if c.platformSource == nil {
		return DefaultPlatform
	}
	platform := strings.TrimSpace(c.platformSource.CustomerPlatform())
	if platform == "" {
		return DefaultPlatform
	}
	return platform
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
