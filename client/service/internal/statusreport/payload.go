// Package statusreport 每 5 分钟把本机运行快照报给旧后台(AGENTS.md
// 「全局约定·工作状态上报」,2026-08-06 甲方裁决)。
//
// 本包最重要的一条设计:**载荷是独立结构体,字段逐个显式赋值**。
// 不能图省事把 store.AppOverviewProjection 直接序列化出去 —— 那些类型里带着
// 候选人 DisplayName,直接发等于把姓名传上公网。这里的每个字段都是计数、枚举
// 或时刻,没有一个字段能装下候选人身份或业务正文。
//
// 另一条:上报的是**当前累计快照**,不是增量事件。丢一次由下一次自愈,所以本包
// 没有重试、没有发件箱、没有游标,也不补报客户端没运行的那段时间。
package statusreport

import "time"

// SchemaVersion 是载荷版本。服务端按它宽容解析。
//
// 加字段时注意顺序:服务端 Pydantic 是 extra="forbid",新字段必须**先发后台、
// 再发客户端**,否则新客户端会被老后台整份 422 拒掉。
const SchemaVersion = 1

// Payload 的 json tag 必须与旧后台 app/api/client_status.py 的 alias 逐字对齐。
// 对不上的字段会被 forbid 挡成 422,而不是被忽略。
type Payload struct {
	MachineID     string `json:"machineId"`
	LicenseToken  string `json:"licenseToken"`
	AppVersion    string `json:"appVersion,omitempty"`
	SchemaVersion int    `json:"schemaVersion"`

	// ReportedAt 带本机时区偏移,LocalDate 是本机自己算的日期。服务端不按自己
	// 的时区重新切天 —— 客户机可能在别的时区,"今天"只有它自己算得对。
	ReportedAt time.Time `json:"reportedAt"`
	LocalDate  string    `json:"localDate"`

	Account  Account  `json:"account"`
	Job      Job      `json:"job"`
	Workflow Workflow `json:"workflow"`
	Batch    Batch    `json:"batch"`
	Today    Today    `json:"today"`
	Total    Total    `json:"total"`
	Health   Health   `json:"health"`
}

// Account 只说"有没有账号在跑"。accountRef 原文不报 —— 报了对运营没用。
type Account struct {
	Bound    bool   `json:"bound"`
	Platform string `json:"platform,omitempty"`
}

type Job struct {
	BackendJobID string     `json:"backendJobId,omitempty"`
	Name         string     `json:"name,omitempty"`
	SyncStatus   string     `json:"syncStatus,omitempty"`
	LastSyncedAt *time.Time `json:"lastSyncedAt,omitempty"`
}

type Workflow struct {
	Status string `json:"status,omitempty"`
	Stage  string `json:"stage,omitempty"`
	Mode   string `json:"mode,omitempty"`
	// EndReason/FailureReason 原样上报(裁决):类型上是自由文本,实践上写进去
	// 的是 noNewCandidates / startInterruptedBeforeBatch / effectResolvedFailed
	// 这类驼峰码。不做枚举映射。
	EndReason     string     `json:"endReason,omitempty"`
	FailureReason string     `json:"failureReason,omitempty"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	PausedAt      *time.Time `json:"pausedAt,omitempty"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
}

type Batch struct {
	BatchID       string `json:"batchId,omitempty"`
	Stage         string `json:"stage,omitempty"`
	Reason        string `json:"reason,omitempty"`
	TargetCount   int64  `json:"targetCount"`
	CapturedCount int64  `json:"capturedCount"`
	ScoredCount   int64  `json:"scoredCount"`
	SelectedCount int64  `json:"selectedCount"`
	SentCount     int64  `json:"sentCount"`
	// 生成失败与发送失败分两个字段。合成一个的话,1 次生成失败 + 2 次发送失败
	// 会在两个阶段各显示 3,看起来像 6 处失败 —— 产品投影层踩过这个坑。
	GenerationFailedCount int64 `json:"generationFailedCount"`
	SendFailedCount       int64 `json:"sendFailedCount"`
	SuspectCount          int64 `json:"suspectCount"`
}

type TodayJob struct {
	BackendJobID string `json:"backendJobId,omitempty"`
	Name         string `json:"name,omitempty"`
	Captured     int64  `json:"captured"`
}

// Today 一律是**当日**口径。失败数与 suspect 数不在这里 —— 它们只有"当前批次"
// 口径(见 Batch),摆进 Today 会让同一个数字挂上一个它对不上的标签。
type Today struct {
	Captured          int64 `json:"captured"`
	Rated             int64 `json:"rated"`
	Confirmed         int64 `json:"confirmed"`
	Greeted           int64 `json:"greeted"`
	Replies           int64 `json:"replies"`
	Wechat            int64 `json:"wechat"`
	InterviewInvites  int64 `json:"interviewInvites"`
	Appointments      int64 `json:"appointments"`
	ElapsedInterviews int64 `json:"elapsedInterviews"`
	// ByJob 回答「今天采集了什么职位」。职位是客户自己的,不是候选人信息。
	ByJob []TodayJob `json:"byJob"`
}

type Total struct {
	Greeted     int64 `json:"greeted"`
	Wechat      int64 `json:"wechat"`
	Interviewed int64 `json:"interviewed"`
}

type Health struct {
	HandOnline         bool   `json:"handOnline"`
	HandContractMatch  bool   `json:"handContractMatch"`
	ExtensionVersion   string `json:"extensionVersion,omitempty"`
	LastHeartbeatAgoMs int64  `json:"lastHeartbeatAgoMs"`
	// 证词 journal 打满曾经直接把发送打瘫,而那次是事后才知道的。
	WitnessJournalOpen     int64 `json:"witnessJournalOpen"`
	WitnessOutboxPending   int64 `json:"witnessOutboxPending"`
	PendingManualVerdicts  int64 `json:"pendingManualVerdicts"`
	ManualRequiredProfiles int64 `json:"manualRequiredProfiles"`
	LLMProviderConfigured  bool  `json:"llmProviderConfigured"`
	BrainUptimeSec         int64 `json:"brainUptimeSec"`
}
