package statusreport

import (
	"time"

	"recruithelper/client/service/internal/secposture"
	"recruithelper/client/service/internal/store"
)

// Store 是本包用到的只读投影,窄接口声明在使用方 —— 测试给假实现,不必搬 *store.Store。
type Store interface {
	AppCurrentJob() (store.AppJobProjection, error)
	AppOverview(store.AppOverviewRequest) (*store.AppOverviewProjection, error)
	StatusReportCounts(platform, accountRef string, start, end time.Time) (store.StatusReportCounts, error)
	SuspectCmds() ([]store.CmdRecord, error)
	LatestProductWorkflowRun() (*store.ProductWorkflowRun, error)
}

// Runtime 是产品运行态里本包关心的部分。由装配方从 productController 取。
type Runtime struct {
	Platform       string
	AccountRef     string
	CurrentBatchID string
}

// HandHealth 是插件侧健康快照。由装配方从 session 注册表转换 —— 本包不认识 hub。
type HandHealth struct {
	Online             bool
	ContractMatch      bool
	ExtensionVersion   string
	LastHeartbeatAgoMs int64
	JournalOpen        int64
	OutboxPending      int64
}

type Deps struct {
	Store              Store
	Runtime            func() (Runtime, error)
	Hand               func() HandHealth
	ProviderConfigured func() bool
	// Security 给出最近一次安全姿态采集(可能为 nil:非 Windows、或后台采集
	// 还没跑完第一轮)。缓存读取,零阻塞;nil 时载荷里没有 security 块。
	Security   func() *secposture.Posture
	AppVersion string
	// StartedAt 是脑的启动时刻,用来算 uptime。频繁重启本身是一种故障信号。
	StartedAt time.Time
	Now       func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Collect 组装一份当前快照。
//
// 任何一步读失败都整体失败,不降级成"报零"。因为零在运营眼里等于"今天没干活",
// 而实际是"读不出来" —— 这个方向的误判会让人以为机器闲着,恰恰不去查。读不到就
// 不报,管理前台显示离线,日志里有原因。
func Collect(deps Deps) (*Payload, error) {
	now := deps.now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 0, 1)

	payload := &Payload{
		SchemaVersion: SchemaVersion,
		AppVersion:    deps.AppVersion,
		ReportedAt:    now,
		LocalDate:     start.Format("2006-01-02"),
		Today:         Today{ByJob: []TodayJob{}},
	}
	if !deps.StartedAt.IsZero() && now.After(deps.StartedAt) {
		payload.Health.BrainUptimeSec = int64(now.Sub(deps.StartedAt).Seconds())
	}
	if deps.ProviderConfigured != nil {
		payload.Health.LLMProviderConfigured = deps.ProviderConfigured()
	}
	if deps.Security != nil {
		payload.Security = deps.Security()
	}
	if deps.Hand != nil {
		hand := deps.Hand()
		payload.Health.HandOnline = hand.Online
		payload.Health.HandContractMatch = hand.ContractMatch
		payload.Health.ExtensionVersion = hand.ExtensionVersion
		payload.Health.LastHeartbeatAgoMs = hand.LastHeartbeatAgoMs
		payload.Health.WitnessJournalOpen = hand.JournalOpen
		payload.Health.WitnessOutboxPending = hand.OutboxPending
	}

	var runtime Runtime
	if deps.Runtime != nil {
		got, err := deps.Runtime()
		if err != nil {
			return nil, err
		}
		runtime = got
	}
	payload.Account = Account{
		Bound:    runtime.Platform != "" && runtime.AccountRef != "",
		Platform: runtime.Platform,
	}

	if deps.Store == nil {
		return payload, nil
	}

	job, err := deps.Store.AppCurrentJob()
	if err != nil {
		return nil, err
	}
	payload.Job = Job{
		BackendJobID: job.BackendJobID,
		Name:         job.Name,
		SyncStatus:   job.SyncStatus,
		LastSyncedAt: job.LastSyncedAt,
	}

	run, err := deps.Store.LatestProductWorkflowRun()
	if err != nil {
		return nil, err
	}
	if run != nil {
		payload.Workflow = Workflow{
			Status:        string(run.Status),
			Stage:         run.Stage,
			Mode:          string(run.Mode),
			EndReason:     run.EndReason,
			FailureReason: run.FailureReason,
			StartedAt:     &run.StartedAt,
			PausedAt:      run.PausedAt,
			EndedAt:       run.EndedAt,
		}
	}

	suspects, err := deps.Store.SuspectCmds()
	if err != nil {
		return nil, err
	}
	payload.Health.PendingManualVerdicts = int64(len(suspects))

	// 零账号(全新安装、还没登录)照常上报:职位与健康都有值,业务计数全零。
	// 不能因为没账号就不报 —— 那正是最需要被看见的状态。
	if !payload.Account.Bound {
		return payload, nil
	}

	counts, err := deps.Store.StatusReportCounts(runtime.Platform, runtime.AccountRef, start, end)
	if err != nil {
		return nil, err
	}
	payload.Today.Captured = counts.TodayCaptured
	payload.Today.Rejected = counts.TodayRejected
	payload.Today.Blacklisted = counts.TodayBlacklisted
	payload.Total.Rejected = counts.CurrentRejected
	payload.Total.Blacklisted = counts.TotalBlacklisted
	payload.Health.ManualRequiredProfiles = counts.ManualRequiredProfiles
	for _, row := range counts.TodayCapturedByJob {
		payload.Today.ByJob = append(payload.Today.ByJob, TodayJob{
			BackendJobID: row.BackendJobID,
			Name:         row.Name,
			Captured:     row.Captured,
		})
	}

	overview, err := deps.Store.AppOverview(store.AppOverviewRequest{
		Now:            now,
		CurrentBatchID: runtime.CurrentBatchID,
		Platform:       runtime.Platform,
		AccountRef:     runtime.AccountRef,
	})
	if err != nil {
		return nil, err
	}
	if overview == nil {
		return payload, nil
	}
	applyOverview(payload, overview)
	return payload, nil
}

// applyOverview 把产品首页那份投影摊平成上报字段。
//
// **逐字段显式赋值**,不是整体序列化:投影里带着候选人 DisplayName
// (AppOverviewProjection.TodayInterviews),整体发出去就是把姓名传上公网。
func applyOverview(payload *Payload, overview *store.AppOverviewProjection) {
	funnel := overview.Funnel
	payload.Batch = Batch{
		BatchID:               funnel.BatchID,
		Stage:                 funnel.Stage,
		Reason:                funnel.LastFailureReason,
		TargetCount:           int64(funnel.TargetCount),
		CapturedCount:         funnel.CapturedCount,
		ScoredCount:           funnel.ScoredCount,
		SelectedCount:         funnel.SelectedCount,
		SentCount:             funnel.SentCount,
		GenerationFailedCount: funnel.GenerationFailedCount,
		SendFailedCount:       funnel.SendFailedCount,
		SuspectCount:          funnel.SuspectCount,
	}

	statistics := overview.Statistics
	payload.Today.Rated = metricValue(statistics.TodayRated)
	payload.Today.Confirmed = metricValue(statistics.TodayConfirmation)
	payload.Today.Greeted = metricValue(statistics.TodayGreeted)
	payload.Today.Replies = metricValue(statistics.TodayNewReplies)
	payload.Today.Wechat = metricValue(statistics.TodayWechat)
	payload.Today.InterviewInvites = metricValue(statistics.TodayInvited)
	payload.Today.Appointments = metricValue(statistics.TodayNewAppointments)
	payload.Today.ElapsedInterviews = metricValue(statistics.TodayElapsedInterviews)

	// 逐字段赋值,不整体覆盖:Total.Rejected/Blacklisted 在进本函数前已由
	// StatusReportCounts 填好,结构体字面量会把它们清零。
	payload.Total.Greeted = metricValue(statistics.TotalGreeted)
	payload.Total.Wechat = metricValue(statistics.TotalWechat)
	payload.Total.Interviewed = metricValue(statistics.TotalInterviewed)
}

// metricValue 把"可能读不出来"的指标压成一个数。不可用与 0 在这里合并 —— 载荷
// 只承载趋势,真要区分二者去查本机诊断台。
func metricValue(metric store.AppMetric) int64 {
	if metric.Value == nil {
		return 0
	}
	return *metric.Value
}
