package store

import (
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/workflow"
)

// 首个客户安装前仍用 AutoMigrate 快跑；Candidate/CandidateProfile 的正式身份语义
// 已在 M4 冻结，首个对外发布前切换为显式 migration。

// Hand:已与脑建立过会话的本地手。handId 由 Chrome profile 内的手
// 自行生成并持久化；脑仅把它作为不透明路由键，不签发凭据。
type Hand struct {
	HandID     string `gorm:"primaryKey"`
	Origin     string `gorm:"not null"` // 最近一次成功 hello 的扩展 Origin；变化只软审计，不拒绝
	Label      string
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// CmdStatus:脑侧账本状态(协议规格 §8.1)。账本内部状态,不是线上协议字段。
type CmdStatus string

const (
	CmdQueued   CmdStatus = "queued"   // 已记账未发送(先记账后发送)
	CmdSent     CmdStatus = "sent"     // 已进 socket,等 ack
	CmdAccepted CmdStatus = "accepted" // 手已入队
	// 首个真实副作用批次的恢复中间态。两者都不是业务终局，
	// 且持续占用原账号串行域：pendingReconcile 等手的 outbox/report，
	// verifying 只允许为消解该 SX 而执行的 chat.readThread 验证读。
	CmdPendingReconcile CmdStatus = "pendingReconcile"
	CmdVerifying        CmdStatus = "verifying"
	// 终局(result 为准)
	CmdOk       CmdStatus = "ok"
	CmdFailed   CmdStatus = "failed"
	CmdCanceled CmdStatus = "canceled"
	CmdExpired  CmdStatus = "expired"
	CmdRejected CmdStatus = "rejected" // 协议性拒绝(PROTO_*);瞬态拒绝不落此态,回 queued
	// 收编终局
	CmdVoid    CmdStatus = "void"    // readonly/intrusive 作废,巡检自然重派
	CmdSuspect CmdStatus = "suspect" // effectful 结果未知,人工队列(六法条)
	// 人工裁决终局
	CmdResolvedOk     CmdStatus = "resolvedOk"
	CmdResolvedFailed CmdStatus = "resolvedFailed"
)

// Terminal 报告该状态是否为终局(不再有自动状态推进;suspect 可被迟到 result 自动核销,是唯一例外)。
func (s CmdStatus) Terminal() bool {
	switch s {
	case CmdOk, CmdFailed, CmdCanceled, CmdExpired, CmdRejected, CmdVoid, CmdSuspect, CmdResolvedOk, CmdResolvedFailed:
		return true
	}
	return false
}

// CmdRecord:在途命令账本(write-ahead:必先落库再进 socket,脑账本永远是手所见的超集)。
type CmdRecord struct {
	MsgID      string `gorm:"primaryKey"`
	Name       string `gorm:"not null;index"` // 原语名
	Class      string `gorm:"not null"`       // readonly / intrusive / effectful
	IdemKey    string `gorm:"index"`          // 仅 effectful
	Domain     string `gorm:"index"`          // 串行域键:业务=accountRef;debug=debug:{handId}
	Platform   string `gorm:"index:idx_cmd_context,priority:1"`
	AccountRef string `gorm:"index:idx_cmd_context,priority:2"`
	// ExpectedPrincipalFingerprint 是脑绑定的招聘方账号不透明指纹。脑只作相等比较,不解析内容。
	ExpectedPrincipalFingerprint string
	// ContextJSON 保存 cmd.context 的完整 JSON,防止未来合法加法字段在重连/重派时丢失。
	ContextJSON      string
	Args             string // cmd.args 原文 JSON(重连收编重发用)
	Guards           string // cmd.guards 原文 JSON；SX 重连/重派必须字节级保持
	IntentID         string `gorm:"index"` // 真实 SX 的脑侧业务意图；debug effectful 留空
	HandID           string `gorm:"not null;index"`
	Session          string // 派发时会话(重投时更新)
	BootIDAtDispatch string // 派发时手的 bootId(重连后同 msgId 重发的前提)
	// LogicalDispatchID 跨物理重派保持不变。ParentMsgID/ReplacementMsgID 构成单链;
	// NULL 表示无父/无替代,允许数据库唯一索引容纳多条根命令。
	LogicalDispatchID string  `gorm:"not null;index"`
	ParentMsgID       *string `gorm:"uniqueIndex"`
	ReplacementMsgID  *string `gorm:"uniqueIndex"`
	LineageDepth      int
	Status            CmdStatus  `gorm:"not null;index"`
	Attempt           int        // 同 msgId 第几次发送
	RedispatchN       int        // 本意图已重派次数(readonly/intrusive void→重派链累计),void 时向新命令 +1 传递
	SentAt            *time.Time // 最近一次进入 sent 的时刻(ackTimeout 判定锚点)
	// NotBeforeAt 是脑侧持久化的重投/重派退避门槛。处于 queued 不代表
	// 可以立即发送；进程重启后仍必须遵守该时刻，避免退避只存在内存里。
	NotBeforeAt   *time.Time `gorm:"index"`
	DeadlineMs    int64      // 绝对毫秒(脑钟);suspect 判定 = deadline+宽限 无终局
	ExecBudgetMs  int64
	ErrorCode     string // 终局为 failed 时
	SideEffect    string // 终局 error 的副作用标注(none/possible/confirmed)
	ResultBody    string // 终局 result 的 body JSON(审计与重放)
	SuspectReason string // 进 suspect 的原因(deadline/bootId 换代/脑重启扫描/sideEffect=possible)

	// SX 四阶段恢复证词。它们只记录“脑看到了什么”，不把手侧
	// journal 升格为权威账本。PreReconcileStatus 保留进入对账前的物理态；
	// QueryMsgID/Report* 用于迟到、重复和乱序 report 的硬栅栏。
	PreReconcileStatus       CmdStatus
	ReconcileSession         string
	ReconcileBootID          string
	ReconcileNextAt          *time.Time `gorm:"index"` // queued/executing report 后的有界复询时钟
	QueryMsgID               string     `gorm:"index"`
	QuerySentAt              *time.Time `gorm:"index"`
	QueryN                   int        // 对账 query 持久尝试数；只重发只读 query，绝不授权 SX
	ReportState              string
	ReportBody               string
	ReportedAt               *time.Time
	WitnessStoreIDAtDispatch string
	RecoveryAuthorized       bool
	RecoveryRedispatchN      int // report=unknown 证明安全后的同 msgId 恢复次数，上限 1
	VerificationN            int
	VerificationReason       string
	VerificationNextAt       *time.Time `gorm:"index"`
	VerificationForMsgID     string     `gorm:"index"` // 仅验证读物理命令填写
	VerificationChildMsgID   string     `gorm:"index"` // 仅 parent SX：当前尚未消费的验证读 logical root
	ReviewReady              bool       // 真实 SX 已完成 outbox/query/验证收束，在线也可人裁
	ReviewAfterMs            int64      // 不可早于原命令 deadline+grace，防页面僵尸迟到 click
	// created_at 带索引:诊断台 RecentCmds 按它倒序取 50 条,客户机 13.9 万行
	// 无索引时每次全表扫描约 900ms,单写连接上其余查询全部排队(2026-08-16
	// 客户机实测;建索引后同查询毫秒级,存量库由 AutoMigrate 启动时一次性补建)。
	CreatedAt                time.Time `gorm:"index"`
	UpdatedAt                time.Time
	TerminalAt               *time.Time // 进入终局的时刻
}

// EffectIntentStatus 是脑侧权威的 SX 业务意图状态。物理命令只能在
// 同 witness store 的 unknown 证明“未尝试”后，以原 msgId/body/idemKey/guards 恢复一次。
type EffectIntentStatus string

const (
	EffectIntentDispatching    EffectIntentStatus = "dispatching"
	EffectIntentReconciling    EffectIntentStatus = "reconciling"
	EffectIntentVerifying      EffectIntentStatus = "verifying"
	EffectIntentOk             EffectIntentStatus = "ok"
	EffectIntentFailed         EffectIntentStatus = "failed"
	EffectIntentSuspect        EffectIntentStatus = "suspect"
	EffectIntentResolvedOk     EffectIntentStatus = "resolvedOk"
	EffectIntentResolvedFailed EffectIntentStatus = "resolvedFailed"
)

// EffectIntent 实现 §7.5 的脑账本闸。IntentID 来自已持久化的业务源：
// M3 真人确认或 M5 CommunicationAction；重试复用原 ID，IdemKey 由脑确定性派生。
// PayloadHash/GuardsHash 防止同一 IntentID 被偷换文本或前置断言后重用。
type EffectIntent struct {
	IntentID    string             `gorm:"primaryKey"`
	IdemKey     string             `gorm:"not null;uniqueIndex"`
	Platform    string             `gorm:"not null;index:idx_effect_intent_account,priority:1;index:idx_effect_intent_conversation,priority:1"`
	AccountRef  string             `gorm:"not null;index:idx_effect_intent_account,priority:2;index:idx_effect_intent_conversation,priority:2"`
	Primitive   string             `gorm:"not null"`
	TargetRef   string             `gorm:"not null;index:idx_effect_intent_conversation,priority:3"`
	PayloadHash string             `gorm:"not null"`
	GuardsHash  string             `gorm:"not null"`
	RootMsgID   string             `gorm:"not null;uniqueIndex"`
	Status      EffectIntentStatus `gorm:"not null;index"`
	DeadlineMs  int64              `gorm:"not null;index"`

	// SendFingerprint 是平台无关的目标消息指纹，只由契约 data
	// 与验证读的结构化字段比较，禁止解析 evidence/DOM 作裁决。
	SendFingerprint string
	// ResultConversationRef 仅供建立新关系的 effectful 原语保存结果会话；
	// chat.sendMessage 的 TargetRef 本身就是既有会话，因此保持 NULL。
	ResultConversationRef *string
	ResultMsgID           string
	ResultMessageSeq      *int64
	SuspectReason         string
	ResolvedAt            *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// ConversationEffectHead 是会话副作用意图的持久单调 head。CAS 只比较
// LatestIntentID，并在创建 EffectIntent+Cmd 的同一事务里递增 Generation；
// 绝不从 created_at 或 SQLite rowid 反推“最新”。
type ConversationEffectHead struct {
	Platform        string `gorm:"primaryKey"`
	AccountRef      string `gorm:"primaryKey"`
	ConversationRef string `gorm:"primaryKey"`
	LatestIntentID  string `gorm:"not null;index"`
	Generation      uint64 `gorm:"not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ProcessedMsg:上行消息(result/event)按 msgId 持久去重,保留 30 天(清理后续步骤做)。
type ProcessedMsg struct {
	MsgID       string `gorm:"primaryKey"`
	Kind        string
	HandID      string
	ProcessedAt time.Time `gorm:"index"`
}

// AuditEntry:审计流水。响亮原则:顶替、非当前连接丢弃、迟到帧、suspect、人工裁决,全部留痕。
type AuditEntry struct {
	ID              uint      `gorm:"primaryKey;autoIncrement"`
	At              time.Time `gorm:"index"`
	Category        string    `gorm:"not null;index"`
	HandID          string
	RefMsgID        string
	Platform        string `gorm:"index:idx_audit_context,priority:1"`
	AccountRef      string `gorm:"index:idx_audit_context,priority:2"`
	ConversationRef string `gorm:"index"`
	RoundID         string `gorm:"index"`
	Detail          string // JSON 或短文本
}

// IdentityState 是脑对招聘方登录身份绑定的持久状态。页面暂不可观测不等于绑定失效。
type IdentityState string

const (
	IdentityUnbound      IdentityState = "unbound"
	IdentityVerified     IdentityState = "verified"
	IdentityUnobservable IdentityState = "unobservable"
	IdentityInvalid      IdentityState = "invalid"
)

// Account 是平台账号 actor 的持久根。正式身份键始终包含 platform 维度。
// PrincipalFingerprint 是平台无关的不透明等值指纹,不得存 Cookie/token 或可解析的账号凭据。
type Account struct {
	Platform   string `gorm:"primaryKey;uniqueIndex:ux_account_principal,priority:1"`
	AccountRef string `gorm:"primaryKey"`

	BoundHandID          string        `gorm:"index"`
	PrincipalFingerprint *string       `gorm:"uniqueIndex:ux_account_principal,priority:2"`
	IdentityState        IdentityState `gorm:"not null;index"`
	IdentityVerifiedAt   *time.Time
	IdentitySession      string
	IdentityBootID       string
	IdentityReason       string

	// 每日真人开启与账号 actor 恢复状态。EnabledDate 使用账号所在本地日 YYYY-MM-DD。
	EnabledDate      string `gorm:"index"`
	EnabledAt        *time.Time
	StoppedAt        *time.Time
	PausedReason     string
	NextPatrolAt     *time.Time `gorm:"index"`
	LastPatrolAt     *time.Time
	ManualQuietUntil *time.Time
	DirtyHint        bool

	// 采集开关绑定到仓库自有的不可变职位配置 revision。关闭每日 actor
	// 不会抹掉该配置；重新 start 可显式切换到另一 revision。
	SourcingEnabled             bool   `gorm:"not null;default:false;index"`
	SourcingContextRevisionHash string `gorm:"index"`
	SourcingStartedAt           *time.Time
	SourcingLastAttemptAt       *time.Time
	SourcingLastErrorCode       string
	// SourcingFeedInvalidatedAt 是推荐流最近一次已知换代边界。早于或等于
	// 该时刻启动的批次不得继续采集，也不得为尚未进入 WAL 的招呼新建意图。
	SourcingFeedInvalidatedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProductWorkflowRun is the durable control fact behind the ordinary user's
// start/pause/resume buttons. It deliberately does not duplicate candidate
// progress: sourcing batches, score/greeting invocations and communication
// aggregates remain their own authorities.
//
// ActiveSlot is NULL for terminal history and "active" for the single current
// workflow. The nullable unique index keeps restart recovery deterministic
// without physically deleting prior runs.
type ProductWorkflowRun struct {
	RunID string `gorm:"primaryKey"`

	ActiveSlot *string `gorm:"uniqueIndex"`
	Platform   string  `gorm:"not null;index"`
	AccountRef string  `gorm:"not null;index"`

	Mode         workflow.Mode   `gorm:"not null"`
	Status       workflow.Status `gorm:"not null;index"`
	ResumeStatus workflow.Status
	Stage        string `gorm:"not null;index"`

	// A later user-authorized run may adopt the same still-open sourcing batch
	// after an earlier controller start failed. ActiveSlot already prevents two
	// live controllers, so history must not make BatchID globally unique.
	SourcingBatchID *string `gorm:"index"`
	FailureReason   string
	EndReason       string `gorm:"not null;default:''"`

	// PendingAction is the durable control handoff requested while a workflow
	// is already in communication. It deliberately stays on terminal history;
	// consumers clear it explicitly only when the requested handoff is
	// withdrawn rather than completed.
	PendingAction              ProductWorkflowPendingAction `gorm:"not null;default:''"`
	PendingContextRevisionHash string                       `gorm:"not null;default:''"`
	PendingRequestedAt         *time.Time

	StartedAt time.Time `gorm:"not null"`
	PausedAt  *time.Time
	ResumedAt *time.Time
	EndedAt   *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type SourcingBatchStatus string

const (
	SourcingBatchPreparing  SourcingBatchStatus = "preparing"
	SourcingBatchCollecting SourcingBatchStatus = "collecting"
	SourcingBatchBlocked    SourcingBatchStatus = "blocked"
	SourcingBatchCompleted  SourcingBatchStatus = "completed"
	SourcingBatchStopped    SourcingBatchStatus = "stopped"
)

// SourcingBatch 是一次正式采集的不可变范围与可恢复状态。PositionRef 在
// preparing 阶段为空，首个窗口正结果绑定后不可再改；EndedAt 非空表示终态。
type SourcingBatch struct {
	BatchID string `gorm:"primaryKey"`

	Platform   string `gorm:"not null;index:ux_sourcing_batch_open,unique,where:ended_at IS NULL,priority:1"`
	AccountRef string `gorm:"not null;index:ux_sourcing_batch_open,unique,where:ended_at IS NULL,priority:2"`

	ContextRevisionHash string `gorm:"not null;index"`
	// BackendJobID 是旧后台 Job.ID。历史孤儿允许为空；所有新批次必须由
	// ContextRevisionHash 对应 revision 的 SourceJobRef 原子派生并写入。
	BackendJobID *string `gorm:"index"`
	// TargetCount 是本轮采集到多少人为止,同时是"批内成员数"的断言:评分与
	// 筛选都要求 run 数精确等于它。分轮采集只在轮次之间抬高它,单轮语义不变。
	TargetCount int `gorm:"not null;check:ck_sourcing_batch_target_count,target_count > 0"`
	// CaptureLimit 是整批累计可采人数的上限。TargetCount 抬档不得越过它;
	// 0 表示不分轮(管理面显式启动的批次与分轮前建立的存量批次)。
	CaptureLimit    int `gorm:"not null;default:0"`
	PositionRef     *string
	PositionTitle   *string
	PositionBoundAt *time.Time

	Status SourcingBatchStatus `gorm:"not null;index;check:ck_sourcing_batch_status,status IN ('preparing','collecting','blocked','completed','stopped')"`
	Reason string

	StartedAt     time.Time `gorm:"not null"`
	LastAttemptAt *time.Time
	EndedAt       *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time

	Members []SourcingCandidateRun `json:"-" gorm:"foreignKey:BatchID;references:BatchID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}

// SourcingCandidateRun 是推荐页一次单候选人简历采集的不可变业务事实。
// 候选人身份与简历正文只允许在业务库内使用；管理 API 只能投影随机引用、
// hash、字节数、状态与时刻。
type SourcingCandidateRun struct {
	RunID               string  `gorm:"primaryKey"`
	BatchID             *string `gorm:"index;uniqueIndex:ux_sourcing_batch_candidate,priority:1"`
	Platform            string  `gorm:"not null;index:idx_sourcing_account_revision,priority:1"`
	AccountRef          string  `gorm:"not null;index:idx_sourcing_account_revision,priority:2"`
	ContextRevisionHash string  `gorm:"not null;index:idx_sourcing_account_revision,priority:3"`

	PlatformUserRef string `gorm:"not null;uniqueIndex:ux_sourcing_batch_candidate,priority:2"`
	DisplayName     *string
	PositionRef     string `gorm:"not null"`
	PositionTitle   *string
	ContactState    string `gorm:"not null"`

	SourceLogicalDispatchID string    `gorm:"not null;uniqueIndex"`
	ObservedAt              int64     `gorm:"not null"`
	CapturedAt              time.Time `gorm:"not null;index"`
	SchemaVersion           int       `gorm:"not null"`
	ContentHash             string    `gorm:"not null"`
	ResumeJSON              string    `gorm:"not null"`
	CreatedAt               time.Time
}

// Candidate 是人的平台身份根。PlatformUserRef 只作不透明等值比较；
// resumeNumber、姓名、职位和招聘账号都不参与身份。
type Candidate struct {
	Platform        string `gorm:"primaryKey"`
	PlatformUserRef string `gorm:"primaryKey"`
	DisplayName     *string
	FirstSeenAt     time.Time `gorm:"not null"`
	LastSeenAt      time.Time `gorm:"not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time

	Profiles []CandidateProfile `gorm:"foreignKey:Platform,PlatformUserRef;references:Platform,PlatformUserRef;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}

// CandidateProfileStatus 是人×职位档案的主线状态。M5 在正式账本首次
// 观察到候选人真实文字时生产 greeted→communicating；AI 成败不回滚该事实。
type CandidateProfileStatus string

const (
	CandidateProfileSelected      CandidateProfileStatus = "selected"
	CandidateProfileGreeted       CandidateProfileStatus = "greeted"
	CandidateProfileCommunicating CandidateProfileStatus = "communicating"
	CandidateProfileInvited       CandidateProfileStatus = "invited"
	CandidateProfileInterviewed   CandidateProfileStatus = "interviewed"
	CandidateProfileEnded         CandidateProfileStatus = "ended"
	CandidateProfileEliminated    CandidateProfileStatus = "eliminated"
)

type CandidateProfileEndReason string

const (
	CandidateProfileEndGreetingFailed         CandidateProfileEndReason = "greetingFailed"
	CandidateProfileEndRejected               CandidateProfileEndReason = "rejected"
	CandidateProfileEndBlacklisted            CandidateProfileEndReason = "blacklisted"
	CandidateProfileEndFallbackArchive        CandidateProfileEndReason = "fallbackArchive"
	CandidateProfileEndSilentInterviewPending CandidateProfileEndReason = "silentInterviewPending"
	CandidateProfileEndSilentWechatInvited    CandidateProfileEndReason = "silentWechatInvited"
	CandidateProfileEndSilentWechatRejected   CandidateProfileEndReason = "silentWechatRejected"
	CandidateProfileEndSilentWechatExchanged  CandidateProfileEndReason = "silentWechatExchanged"
	CandidateProfileEndSilent                 CandidateProfileEndReason = "silent"
)

type ResumeCaptureState string

const (
	ResumeCaptureUnattempted    ResumeCaptureState = "unattempted"
	ResumeCaptureInFlight       ResumeCaptureState = "inFlight"
	ResumeCaptureCaptured       ResumeCaptureState = "captured"
	ResumeCaptureManualRequired ResumeCaptureState = "manualRequired"
)

// CandidateProfile 是沟通状态主体。人级建档闸的部分唯一索引刻意不含
// AccountRef，并把 ended 也视为非 eliminated，防止换账号/职位重复追求。
// ConversationRef 必须为 NULL 而不是空串；关系正证已成立但会话尚待巡检
// 绑定的 greeted 档案同样保持 NULL，禁止伪造平台会话引用。
type CandidateProfile struct {
	ProfileID       string `gorm:"primaryKey"`
	Platform        string `gorm:"not null;uniqueIndex:ux_candidate_profile_identity,priority:1;index:ux_candidate_profile_active,unique,where:main_status <> 'eliminated',priority:1;uniqueIndex:ux_candidate_profile_conversation,priority:1"`
	AccountRef      string `gorm:"not null;uniqueIndex:ux_candidate_profile_identity,priority:2;uniqueIndex:ux_candidate_profile_conversation,priority:2"`
	PlatformUserRef string `gorm:"not null;uniqueIndex:ux_candidate_profile_identity,priority:3;index:ux_candidate_profile_active,unique,priority:2"`
	PositionRef     string `gorm:"not null;uniqueIndex:ux_candidate_profile_identity,priority:4"`
	PositionTitle   *string
	// BackendJobID 是沟通与职位配置路由的直接业务键。无法由既有事实唯一
	// 回填的历史档案保持 NULL，任何需要职位配置的路径必须响亮停止。
	BackendJobID *string                `gorm:"index"`
	MainStatus   CandidateProfileStatus `gorm:"not null;index"`
	EndReason    *CandidateProfileEndReason

	SuccessfulGreetingIntentID *string
	ConversationRef            *string `gorm:"uniqueIndex:ux_candidate_profile_conversation,priority:3"`
	GreetedAt                  *time.Time
	CommunicatingAt            *time.Time
	// InterviewedAt 是首次进入"已约面"的时刻,与 GreetedAt/CommunicatingAt
	// 同语义:只在首次写入,不被后续 ended→interviewed 往返覆盖,否则同一人
	// 会反复计入"今日新约面"。它是"何时被接受"的唯一事实——消息时间戳只能
	// 算出"何时发卡"。
	InterviewedAt                  *time.Time
	FirstRealMessageSeq            *int64
	ResumeCaptureState             ResumeCaptureState `gorm:"not null;default:unattempted;index"`
	ResumeCaptureLogicalDispatchID *string            `gorm:"uniqueIndex"`
	ActiveResumeSnapshotID         *string            `gorm:"uniqueIndex"`
	ResumeCaptureAttemptedAt       *time.Time
	ResumeCaptureFailureReason     string
	// GreetingRejectReason 是平台拒绝本次招呼时给出的原话(2026-08-07 甲方
	// 裁决:错误原因要传到客户端)。EndReason=greetingFailed 太笼统,敏感词、
	// 上限、技术失败在客户端上长得一样。这里刻意存自由文本而不新增枚举:
	// 平台拒绝理由不可枚举,自由文本能原样透传,平台改文案我方零改动。
	// 它是平台文案不是候选人明文,允许经 /app/* 产品 API 投影给客户端。
	GreetingRejectReason string
	CreatedAt            time.Time
	UpdatedAt            time.Time

	GreetingHead             *CandidateGreetingHead    `gorm:"foreignKey:ProfileID;references:ProfileID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	CommunicationV4Aggregate *CommunicationV4Aggregate `gorm:"foreignKey:ProfileID;references:ProfileID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}

// ContactAsset is the local business authority for candidate contact details.
// Value and both source identities are deliberately excluded from JSON so this
// model cannot accidentally become an admin/log projection. Rows are business
// facts and are never physically deleted.
type ContactAsset struct {
	AssetID          string  `gorm:"primaryKey"`
	ProfileID        string  `gorm:"not null;index"`
	Platform         string  `gorm:"not null;uniqueIndex:ux_contact_asset_source,priority:1"`
	AccountRef       string  `gorm:"not null;uniqueIndex:ux_contact_asset_source,priority:2"`
	ConversationRef  string  `gorm:"not null;uniqueIndex:ux_contact_asset_source,priority:3"`
	Kind             string  `gorm:"not null;uniqueIndex:ux_contact_asset_source,priority:4"`
	SourceKey        string  `json:"-" gorm:"not null;uniqueIndex:ux_contact_asset_source,priority:5"`
	RequestSourceKey string  `json:"-" gorm:"not null"`
	Value            string  `json:"-" gorm:"not null"`
	EffectIntentID   *string `json:"-" gorm:"uniqueIndex"`
	ObservedAtMs     int64   `gorm:"not null"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// NotificationOutbox 是运营通知发件箱(AGENTS.md「运营通知 webhook」2026-07-28
// 裁决)。业务事实提交时同事务只入队一行(EventKey 幂等),渲染与发送由 notify
// runner 在发送时刻按最新业务事实执行;行内只有事件元数据,不存候选人姓名、
// 微信号或正文。行是业务事实:终态只标记(sent/failed/skipped/expired),不物理删除。
type NotificationOutbox struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	NotifyType  string `gorm:"not null;index"`
	EventKey    string `gorm:"not null;uniqueIndex"`
	ProfileID   string `gorm:"not null;index"`
	PayloadJSON string `gorm:"not null;default:'{}'"`
	Status      string `gorm:"not null;default:pending;index"`
	Attempts    int    `gorm:"not null;default:0"`
	// SentWithWechat 只对约面通知有意义:该条发出时正文是否已带微信号,
	// 是微信互加通知去重的唯一权威事实(照抄旧项目语义)。
	SentWithWechat bool `gorm:"not null;default:false"`
	// AssetsRequestedAt 非空表示取证截图已为本通知派发过(每通知至多一轮),
	// 截图失败不重拍,由 15 分钟兜底闸门按纯文本发送。
	AssetsRequestedAt *time.Time
	LastError         string
	CreatedAt         time.Time `gorm:"index"`
	SentAt            *time.Time
	UpdatedAt         time.Time
}

// CandidateScreenshot 是候选人取证截图事实行(聊天/简历)。图像字节在 blob
// 内容寻址存储,此处只登记引用与元数据;追加行、消费方取最新,不覆盖更新、
// 不物理删除。截图字节不进普通日志、审计 detail 与管理 API。
type CandidateScreenshot struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	ProfileID    string `gorm:"not null;index:idx_candidate_screenshot_profile_kind,priority:1"`
	Kind         string `gorm:"not null;index:idx_candidate_screenshot_profile_kind,priority:2"`
	BlobRef      string `gorm:"not null"`
	ByteSize     int64  `gorm:"not null"`
	Truncated    bool   `gorm:"not null"`
	CapturedAtMs int64  `gorm:"not null"`
	CreatedAt    time.Time
}

// SuspectSceneShot 是 effectful 命令转 suspect 后的现场截图事实行(2026-08-07
// 甲方裁决)。图像字节在 blob 内容寻址存储,此处只登记引用与元数据;追加行、
// 不覆盖更新、不物理删除,保留语义比照运营取证截图。截图只落同机——诊断包
// 白名单硬排除 blobs/,不出站、不进普通日志、审计 detail 与管理 API 响应正文。
type SuspectSceneShot struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	MsgID        string `gorm:"not null;index"` // 转 suspect 的那条命令
	IntentID     string `gorm:"index"`
	Primitive    string `gorm:"not null"`
	BlobRef      string `gorm:"not null"`
	ByteSize     int64  `gorm:"not null"`
	CapturedAtMs int64  `gorm:"not null"`
	CreatedAt    time.Time
}

// CandidatePhoneObservation 是取证顺访经 chat.readPeerPhone 读到并通过收编
// 判定的候选人电话观察事实行(2026-08-06 甲方裁决)。追加行、消费方取最新、
// 不物理删除。Phone 去处比照微信号收口:只进运营通知 webhook 正文与 /admin
// 诊断面,json:"-" 防止被投影误序列化;不进普通日志、审计 detail、AI 请求。
type CandidatePhoneObservation struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	ProfileID    string `gorm:"not null;index"`
	Phone        string `json:"-" gorm:"not null"`
	ObservedAtMs int64  `gorm:"not null"`
	CreatedAt    time.Time
}

// PhoneRevealAttempt 是「查看电话」揭示的标记先行事实行(2026-08-07 甲方裁决):
// 派发 chat.revealPeerPhone 前先落行,ProfileID 唯一索引保证每候选人终身至多
// 消耗一次平台查看权益;不管命令结局如何不删除、不重派。行内无号码。
type PhoneRevealAttempt struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	ProfileID string `gorm:"not null;uniqueIndex"`
	CreatedAt time.Time
}

type ProfileCommunicationAutomationStatus string

const (
	ProfileCommunicationAutomationActive         ProfileCommunicationAutomationStatus = "active"
	ProfileCommunicationAutomationManualRequired ProfileCommunicationAutomationStatus = "manualRequired"
)

// CommunicationV4Aggregate is the durable V4 aggregate root for one profile.
// CandidateProfile.MainStatus/EndReason are only the compact query projection
// and advance in the same transaction. Revision counts applied immutable
// inputs; root creation starts at zero.
type CommunicationV4Aggregate struct {
	ProfileID            string                               `gorm:"primaryKey"`
	RootGreetingIntentID string                               `gorm:"not null;uniqueIndex"`
	StateSchemaVersion   uint                                 `gorm:"not null"`
	Revision             uint64                               `gorm:"not null"`
	ProjectedThroughSeq  int64                                `gorm:"not null"`
	// VerdictGeneration 是对话轨身份指纹的裁决代次成员(协议 §7.4 bnd-v1,
	// 2026-08-27 停机点第二步):平时恒 0,该档案 suspect 经人工裁决
	// resolvedFailed 的裁决事务内加一,使同一输入边界的重新规划立即可行、
	// 键确定性区别于旧尝试。单调只增,不参与任何闸,不经 revision CAS
	// (SQLite 单写串行化下独立自增安全)。
	VerdictGeneration int64 `gorm:"not null;default:0"`
	State                communication.V4State                `gorm:"serializer:json;not null"`
	AutomationStatus     ProfileCommunicationAutomationStatus `gorm:"not null;index"`
	ManualReason         string
	ManualRequiredAt     *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type CommunicationV4InputKind string

const (
	CommunicationV4InputBusinessEvent   CommunicationV4InputKind = "businessEvent"
	CommunicationV4InputDialogueTurn    CommunicationV4InputKind = "dialogueTurn"
	CommunicationV4InputDialogueAdvice  CommunicationV4InputKind = "dialogueAdvice"
	CommunicationV4InputConfirmedAction CommunicationV4InputKind = "confirmedAction"
	CommunicationV4InputRetractedAction CommunicationV4InputKind = "retractedAction"
	CommunicationV4InputArchiveAction   CommunicationV4InputKind = "archiveAction"
	CommunicationV4InputSchedulePlan    CommunicationV4InputKind = "schedulePlan"
	// CommunicationV4InputManualUnfreeze is appended only by the offline
	// unsupported-semantic unfreeze CLI: it advances a manualRequired turn
	// receipt to the replayed waiting-advice outcome without rewriting the
	// immutable original receipt.
	CommunicationV4InputManualUnfreeze CommunicationV4InputKind = "manualUnfreeze"
)

// CommunicationV4ApplicationOutcome stores only the decision data needed to
// resume orchestration after a crash. Candidate text is deliberately absent.
type CommunicationV4ApplicationOutcome struct {
	Dialogue             communication.V4DialogueRequirement `json:"dialogue"`
	DialogueAfterActions bool                                `json:"dialogueAfterActions"`
	Actions              []communication.V4EventAction       `json:"actions"`
	ManualReason         communication.V4ManualReason        `json:"manualReason,omitempty"`

	DialogueStatus communication.V4DialogueDecisionStatus `json:"dialogueStatus,omitempty"`
	NextAdvice     communication.V4AdvicePurpose          `json:"nextAdvice,omitempty"`
	IntentLabel    m5ai.IntentLabel                       `json:"intentLabel,omitempty"`
	IntentSource   communication.IntentSource             `json:"intentSource,omitempty"`
	PlannedActions []communication.V4PlannedAction        `json:"plannedActions,omitempty"`

	// Confirmed actions retain only their pre-action aggregate snapshot. It has
	// no candidate text and exists solely so a later authoritative safe
	// terminal can append a compensating receipt instead of rewriting history.
	StateBeforeAction         *communication.V4State `json:"stateBeforeAction,omitempty"`
	ProjectedThroughSeqBefore *int64                 `json:"projectedThroughSeqBefore,omitempty"`
}

// CommunicationV4ProjectionApplication is an immutable receipt for one input.
// Persisting the reducer outcome beside the new aggregate prevents a crash
// between state advancement and action planning from losing required work.
type CommunicationV4ProjectionApplication struct {
	ProfileID string                   `gorm:"primaryKey;uniqueIndex:ux_communication_v4_application_revision,priority:1"`
	InputKind CommunicationV4InputKind `gorm:"primaryKey"`
	InputKey  string                   `gorm:"primaryKey"`

	InputDigest  string                            `gorm:"not null"`
	SemanticKind string                            `gorm:"not null"`
	MessageSeq   int64                             `gorm:"not null"`
	FromRevision uint64                            `gorm:"not null"`
	ToRevision   uint64                            `gorm:"not null;uniqueIndex:ux_communication_v4_application_revision,priority:2"`
	Outcome      CommunicationV4ApplicationOutcome `gorm:"serializer:json;not null"`
	AppliedAt    time.Time                         `gorm:"not null"`
}

type CommunicationV4EventActionStatus string

const (
	CommunicationV4EventActionPlanned        CommunicationV4EventActionStatus = "planned"
	CommunicationV4EventActionDeferred       CommunicationV4EventActionStatus = "deferred"
	CommunicationV4EventActionEffectPending  CommunicationV4EventActionStatus = "effectPending"
	CommunicationV4EventActionSent           CommunicationV4EventActionStatus = "sent"
	CommunicationV4EventActionManualRequired CommunicationV4EventActionStatus = "manualRequired"
	// CommunicationV4EventActionRetried 是干净失败自动重试通则(协议规格 §8.4,
	// 2026-08-02 推广)下原失败事件动作的留档终态;重试由语义键追加 |try{n}
	// 后缀的新事件动作行承载,新行自带全新 intentId/idemKey。
	CommunicationV4EventActionRetried CommunicationV4EventActionStatus = "retried"
)

type CommunicationV4EventEffectKind string

const (
	CommunicationV4EventEffectReplyText    CommunicationV4EventEffectKind = "replyText"
	CommunicationV4EventEffectInviteWechat CommunicationV4EventEffectKind = "inviteWechat"
	CommunicationV4EventEffectNotification CommunicationV4EventEffectKind = "notification"
	CommunicationV4EventEffectAcceptWechat CommunicationV4EventEffectKind = "acceptWechat"
)

// CommunicationV4EventAction is the immutable local plan derived from one
// projection receipt. SemanticActionKey remains scoped to a
// profile; ActionID is its deterministic SHA-256 identity. Deferred rows are
// explicit business facts, not placeholders that may be deleted later.
type CommunicationV4EventAction struct {
	ActionID            string                         `gorm:"primaryKey"`
	ProfileID           string                         `gorm:"not null;index;uniqueIndex:ux_communication_v4_event_action_semantic,priority:1;uniqueIndex:ux_communication_v4_event_action_source_ordinal,priority:1"`
	SourceInputKind     CommunicationV4InputKind       `gorm:"not null;index:idx_communication_v4_event_action_source,priority:1;uniqueIndex:ux_communication_v4_event_action_source_ordinal,priority:2"`
	SourceInputKey      string                         `gorm:"not null;index:idx_communication_v4_event_action_source,priority:2;uniqueIndex:ux_communication_v4_event_action_source_ordinal,priority:3"`
	SourceOrdinal       int                            `gorm:"not null;uniqueIndex:ux_communication_v4_event_action_source_ordinal,priority:4"`
	SemanticActionKey   string                         `gorm:"not null;uniqueIndex:ux_communication_v4_event_action_semantic,priority:2"`
	V4Kind              communication.V4ActionKind     `gorm:"not null"`
	CardMessageSeq      int64                          `gorm:"not null"`
	EffectKind          CommunicationV4EventEffectKind `gorm:"not null"`
	Text                string
	ContentHash         string
	ContextRevisionHash string
	DependsOnActionID   *string                          `gorm:"index"`
	Status              CommunicationV4EventActionStatus `gorm:"not null;index"`
	FailureReason       string
	EffectIntentID      *string   `gorm:"uniqueIndex"`
	PlannedAt           time.Time `gorm:"not null"`
	EffectStartedAt     *time.Time
	SentAt              *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type CommunicationV4ScheduleOccurrenceStatus string

const (
	CommunicationV4ScheduleOccurrenceApplied CommunicationV4ScheduleOccurrenceStatus = "applied"
)

// CommunicationV4SchedulePlan freezes one non-archive schedule decision
// against the exact aggregate and active ledger boundary from which it was
// derived. The plan is immutable; execution state belongs to the separately
// persisted CommunicationV4EventAction rows and the existing effect WAL.
type CommunicationV4SchedulePlan struct {
	PlanID  string `gorm:"primaryKey"`
	PlanKey string `gorm:"not null;uniqueIndex:ux_communication_v4_schedule_plan_key,priority:2"`

	ProfileID       string `gorm:"not null;index;uniqueIndex:ux_communication_v4_schedule_plan_key,priority:1"`
	ConversationRef string `gorm:"not null;index"`

	BasisRevision            uint64 `gorm:"not null"`
	BasisProjectedThroughSeq int64  `gorm:"not null"`
	BasisMessageTailSeq      int64  `gorm:"not null"`
	ContextRevisionHash      string `gorm:"not null;index"`

	EvaluatedAt    time.Time                       `gorm:"not null"`
	DueAt          time.Time                       `gorm:"not null"`
	PlannedActions []communication.V4PlannedAction `gorm:"serializer:json;not null"`
	ActionsDigest  string                          `gorm:"not null"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// CommunicationV4ScheduleAIInvocation is the persisted one-shot provider
// authorization for a silence-followup schedule tier. It deliberately does
// not reuse AIInvocation.TurnID: schedule advice has no DialogueTurn and must
// not manufacture one. The suggested body is an immutable local business
// result; raw provider request/response remains exclusive to ai-traces.db.
type CommunicationV4ScheduleAIInvocation struct {
	InvocationID string `gorm:"primaryKey"`
	AdviceKey    string `gorm:"not null;uniqueIndex:ux_communication_v4_schedule_ai_advice,priority:2"`
	ProfileID    string `gorm:"not null;index;uniqueIndex:ux_communication_v4_schedule_ai_advice,priority:1"`

	ConversationRef          string `gorm:"not null;index"`
	BasisRevision            uint64 `gorm:"not null"`
	BasisProjectedThroughSeq int64  `gorm:"not null"`
	ContextRevisionHash      string `gorm:"not null;index"`
	ResumeSnapshotID         string `gorm:"not null"`
	EvaluatedAt              time.Time
	Purpose                  m5ai.CompletionPurpose `gorm:"not null"`
	Attempt                  int                    `gorm:"not null"`
	Provider                 string                 `gorm:"not null"`
	Model                    string                 `gorm:"not null"`
	InputHash                string                 `gorm:"not null"`
	SuggestionText           string
	OutputHash               string
	InputTokens              int
	CachedInputTokens        int
	OutputTokens             int
	ReasoningTokens          *int
	UsageShape               AIInvocationUsageShape
	ReasoningContentEmpty    bool
	LatencyMs                int64
	Status                   AIInvocationStatus `gorm:"not null;index"`
	ErrorClass               string
	FailureStage             string
	ErrorDetailCode          string
	ProviderHTTPStatus       *int
	RequestBytes             int
	ResponseBytes            int
	TraceStatus              m5ai.TraceStatus
	EstimatedCostMicros      int64
	CreatedAt                time.Time `gorm:"not null"`
	FinishedAt               *time.Time
}

// CommunicationV4ScheduleOccurrence freezes one deterministic schedule
// evaluation as an append-only business fact. The first slice persists only
// internal archive occurrences, so applied is deliberately the sole status:
// the occurrence and aggregate transition are committed in one transaction
// and no planned archive half-state may exist.
type CommunicationV4ScheduleOccurrence struct {
	OccurrenceID  string                     `gorm:"primaryKey"`
	OccurrenceKey string                     `gorm:"not null;uniqueIndex:ux_communication_v4_schedule_occurrence_key,priority:2"`
	ProfileID     string                     `gorm:"not null;index;uniqueIndex:ux_communication_v4_schedule_occurrence_key,priority:1"`
	Kind          communication.V4ActionKind `gorm:"not null"`

	BasisRevision            uint64    `gorm:"not null"`
	BasisProjectedThroughSeq int64     `gorm:"not null"`
	ConversationRef          string    `gorm:"not null"`
	AnchorMessageSeq         int64     `gorm:"not null"`
	DueAt                    time.Time `gorm:"not null"`
	EvaluatedAt              time.Time `gorm:"not null"`

	Round          uint64                    `gorm:"not null"`
	Stage          uint8                     `gorm:"not null"`
	CardMessageSeq int64                     `gorm:"not null"`
	EndReason      communication.V4EndReason `gorm:"not null"`

	Status        CommunicationV4ScheduleOccurrenceStatus `gorm:"not null;index"`
	FailureReason string
	AppliedAt     time.Time `gorm:"not null"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CandidateResumeSnapshot 是一次完整简历读取的不可变业务事实。正文只在本机
// 业务库保存；普通管理 API、审计和日志只暴露 hash/大小/覆盖信息。
type CandidateResumeSnapshot struct {
	SnapshotID              string    `gorm:"primaryKey"`
	ProfileID               string    `gorm:"not null;index"`
	SourceKind              string    `gorm:"not null"`
	SourceConversationRef   string    `gorm:"not null"`
	SourceLogicalDispatchID string    `gorm:"not null;uniqueIndex"`
	ObservedAt              int64     `gorm:"not null"`
	CapturedAt              time.Time `gorm:"not null"`
	SchemaVersion           int       `gorm:"not null"`
	ContentHash             string    `gorm:"not null"`
	ResumeJSON              string    `gorm:"not null"`
	CreatedAt               time.Time
}

type M5TrialSelectionStatus string

const (
	M5TrialSelectionActive         M5TrialSelectionStatus = "active"
	M5TrialSelectionManualRequired M5TrialSelectionStatus = "manualRequired"
	M5TrialSelectionCompleted      M5TrialSelectionStatus = "completed"
)

// M5TrialSelection 是真人对单 profile 试运行范围的持久授权。ActiveSlot 只在
// active 时为固定非空值，SQLite 唯一索引从数据库层保证全库最多一项有效授权。
type M5TrialSelection struct {
	SelectionID string                 `gorm:"primaryKey"`
	ProfileID   string                 `gorm:"not null;index"`
	Status      M5TrialSelectionStatus `gorm:"not null;index"`
	ActiveSlot  *string                `gorm:"uniqueIndex"`
	SelectedBy  string                 `gorm:"not null"`
	Reason      string
	SelectedAt  time.Time `gorm:"not null"`
	EndedAt     *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// JobAIContextRevision 是一次不可变的职位 AI 上下文导入。SourcePackage
// 完整保留旧 job_version_docs 原包，Communication 只保存经事实门冻结的
// 三文档可执行视图；两者都属于本地业务配置，不进入普通日志。
type JobAIContextRevision struct {
	RevisionHash string `gorm:"primaryKey;uniqueIndex:ux_job_ai_context_revision,priority:2"`
	ContextID    string `gorm:"not null;index;uniqueIndex:ux_job_ai_context_revision,priority:1"`
	SourceKind   string `gorm:"not null"`
	SourceJobRef string
	DisplayName  string `gorm:"not null"`
	Environment  string

	SourcePackage m5ai.JobConfigDocumentPackage `gorm:"serializer:json;not null"`
	Communication m5ai.CommunicationView        `gorm:"serializer:json;not null"`
	CreatedAt     time.Time                     `gorm:"not null"`
}

// JobAIContextHead 记录某个配置来源职位最近一次成功同步后的当前 revision。
// revision 本身继续不可变；head 只是可推进的当前指针，不能由 revision.CreatedAt
// 反推，因为 A→B→A 会复用最初那条 A revision。
// ActivationCurrent 与 InboundEligible 是两个不同的问题，刻意不合并成一个字段:
// 前者回答"漏斗/采集/批量招呼此刻在为哪个职位工作",必须唯一;后者回答"主动来
// 聊的候选人可以被建到哪些职位下",是最近一次成功复数同步返回且配置合格的职位
// 全集。当前工作职位必然属于有效集,反之不成立。
type JobAIContextHead struct {
	SourceKind        string `gorm:"primaryKey"`
	SourceJobRef      string `gorm:"primaryKey"`
	ContextID         string `gorm:"not null;index"`
	RevisionHash      string `gorm:"not null;index"`
	ActivationCurrent bool   `gorm:"not null;default:false;index"`
	InboundEligible   bool   `gorm:"not null;default:false;index"`
	LastSyncedAt      time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type ProfileAIContextBindingStatus string

const (
	ProfileAIContextBindingActive     ProfileAIContextBindingStatus = "active"
	ProfileAIContextBindingSuperseded ProfileAIContextBindingStatus = "superseded"
)

// ProfileAIContextBinding 是真人把一个试运行 profile 显式绑定到一个职位
// 上下文 revision 的业务事实。部分唯一索引从数据库层保证每个 profile
// 最多一个 active；改绑只推进旧行状态，永不物理删除。
type ProfileAIContextBinding struct {
	BindingID    string                        `gorm:"primaryKey"`
	ProfileID    string                        `gorm:"not null;index;index:ux_profile_ai_context_active,unique,where:status = 'active'"`
	ContextID    string                        `gorm:"not null;index:idx_profile_ai_context_revision,priority:1"`
	RevisionHash string                        `gorm:"not null;index:idx_profile_ai_context_revision,priority:2"`
	Status       ProfileAIContextBindingStatus `gorm:"not null;index"`
	Reason       string
	BoundBy      string    `gorm:"not null"`
	BoundAt      time.Time `gorm:"not null"`
	SupersededAt *time.Time
}

type DialogueTurnStatus string

const (
	DialogueTurnCollected      DialogueTurnStatus = "collected"
	DialogueTurnClassified     DialogueTurnStatus = "classified"
	DialogueTurnAdviceReady    DialogueTurnStatus = "adviceReady"
	DialogueTurnDispatching    DialogueTurnStatus = "dispatching"
	DialogueTurnCompleted      DialogueTurnStatus = "completed"
	DialogueTurnManualRequired DialogueTurnStatus = "manualRequired"
	DialogueTurnSuperseded     DialogueTurnStatus = "superseded"
)

type DialogueIntentSource string

const (
	DialogueIntentCodeShortCircuit DialogueIntentSource = "codeShortCircuit"
	DialogueIntentLLM              DialogueIntentSource = "llm"
	DialogueIntentLLMFailure       DialogueIntentSource = "llmFailureFallback"
	DialogueIntentBusinessEvent    DialogueIntentSource = "businessEvent"
)

// DialogueTurn 是一次不可变输入边界及其确定性处理状态。输入正文仍来自消息账本、
// 简历快照和职位 revision；ReplyPhrases 是经确定性代码批准后、供后续逐气泡物化
// 使用的业务事实，不进入无正文诊断投影。
type DialogueTurn struct {
	TurnID              string             `gorm:"primaryKey"`
	ProfileID           string             `gorm:"not null;index;uniqueIndex:ux_dialogue_turn_input,priority:1"`
	ConversationRef     string             `gorm:"not null;index"`
	InputDigest         string             `gorm:"not null;uniqueIndex:ux_dialogue_turn_input,priority:2"`
	HistoryThroughSeq   int64              `gorm:"not null"`
	InboundFromSeq      int64              `gorm:"not null"`
	InboundThroughSeq   int64              `gorm:"not null"`
	ContextRevisionHash string             `gorm:"not null;index"`
	ResumeSnapshotID    string             `gorm:"not null;index"`
	RecommendedTimeText string             `gorm:"not null"`
	RenderFormatVersion string             `gorm:"not null"`
	Status              DialogueTurnStatus `gorm:"not null;index"`
	IntentLabel         m5ai.IntentLabel
	IntentSource        DialogueIntentSource
	ClassifiedAt        *time.Time
	ReplyPhrases        []string `gorm:"serializer:json"`
	FailureReason       string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type CommunicationActionKind string

const (
	CommunicationActionReplyText       CommunicationActionKind = "replyText"
	CommunicationActionInviteWechat    CommunicationActionKind = "inviteWechat"
	CommunicationActionInterviewInvite CommunicationActionKind = "interviewInvite"
	CommunicationActionAcceptWechat    CommunicationActionKind = "acceptWechat"
)

type CommunicationActionStatus string

const (
	CommunicationActionPlanned        CommunicationActionStatus = "planned"
	CommunicationActionEffectPending  CommunicationActionStatus = "effectPending"
	CommunicationActionSent           CommunicationActionStatus = "sent"
	CommunicationActionManualRequired CommunicationActionStatus = "manualRequired"
	CommunicationActionSuperseded     CommunicationActionStatus = "superseded"
	// CommunicationActionRetried 是邀面卡干净失败自动重试例外(2026-07-29 甲方
	// 裁决)下原失败动作的留档终态;重试由带 |try{n} 后缀的新动作行承载。
	CommunicationActionRetried CommunicationActionStatus = "retried"
)

// CommunicationAction 是 AI 建议经确定性代码批准后的唯一业务动作事实。
// 本表本身不派发；后续批次只能从 ActionID 稳定派生一个 effect intent。
type CommunicationAction struct {
	ActionID            string                  `gorm:"primaryKey"`
	TurnID              string                  `gorm:"not null;index"`
	Kind                CommunicationActionKind `gorm:"not null;index"`
	Text                string                  `gorm:"not null"`
	ContentHash         string                  `gorm:"not null"`
	DependsOnActionID   *string                 `gorm:"index"`
	InterviewStartsAtMs *int64
	InterviewEndsAtMs   *int64
	InterviewMethod     *string
	Status              CommunicationActionStatus `gorm:"not null;index"`
	EffectIntentID      *string                   `gorm:"uniqueIndex"`
	FailureReason       string
	PlannedAt           time.Time `gorm:"not null"`
	EffectStartedAt     *time.Time
	SentAt              *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type AIInvocationStatus string

const (
	AIInvocationOK               AIInvocationStatus = "ok"
	AIInvocationTransportFailed  AIInvocationStatus = "transportFailed"
	AIInvocationProviderRejected AIInvocationStatus = "providerRejected"
	AIInvocationInvalidOutput    AIInvocationStatus = "invalidOutput"
	AIInvocationBudgetBlocked    AIInvocationStatus = "budgetBlocked"
)

type AIInvocationUsageShape string

const (
	AIInvocationUsageComplete        AIInvocationUsageShape = "complete"
	AIInvocationReasoningFieldAbsent AIInvocationUsageShape = "reasoningFieldAbsent"
)

// AIInvocation 只保存计量、内容 hash 与无正文诊断摘要，不保存 prompt、正文、
// 原始响应、key、base URL 或候选人身份。完整原文只允许进入独立 ai-traces.db。
// turn+purpose+attempt 唯一，M5-A attempt 固定为 1。
// 调用前预留沿用 transportFailed 且 FinishedAt=NULL；只有首次插入者可调用，
// 调用后以 FinishedAt IS NULL 作 CAS。重启遗留预留事实不授权自动重调。
type AIInvocation struct {
	InvocationID        string                 `gorm:"primaryKey"`
	TurnID              string                 `gorm:"not null;index;uniqueIndex:ux_ai_invocation_turn_purpose_attempt,priority:1"`
	Purpose             m5ai.CompletionPurpose `gorm:"not null;uniqueIndex:ux_ai_invocation_turn_purpose_attempt,priority:2"`
	Attempt             int                    `gorm:"not null;uniqueIndex:ux_ai_invocation_turn_purpose_attempt,priority:3"`
	Provider            string                 `gorm:"not null"`
	Model               string                 `gorm:"not null"`
	ContextRevisionHash string                 `gorm:"not null;index"`
	InputHash           string                 `gorm:"not null"`
	OutputHash          string
	InputTokens         int
	CachedInputTokens   int
	OutputTokens        int
	ReasoningTokens     *int
	UsageShape          AIInvocationUsageShape
	LatencyMs           int64
	Status              AIInvocationStatus `gorm:"not null;index"`
	ErrorClass          string
	FailureStage        string
	ErrorDetailCode     string
	ProviderHTTPStatus  *int
	RequestBytes        int
	ResponseBytes       int
	TraceStatus         m5ai.TraceStatus
	EstimatedCostMicros int64
	CreatedAt           time.Time `gorm:"not null"`
	FinishedAt          *time.Time
}

// SourcingScoreInvocation 是采集 run 至多一行的评分调用终局事实。它与
// DialogueTurn/AIInvocation 分表，避免把 RunID 偷塞进 TurnID；调用前预留仍
// 沿用 transportFailed+FinishedAt=NULL 表示 inFlight。按 2026-07-28 并行重试
// 裁决，inFlight 行允许被评分编排器续驱动（进程内重试与重启接手），每次
// provider HTTP 尝试发出前登记 AttemptCount；BudgetedAttemptCount 只统计
// 计入非 429 预算的尝试（首次、非 429 失败后的重试、接手后的首次保守计入），
// 预算耗尽写终局失败。终局仍只写回本行一次。
type SourcingScoreInvocation struct {
	InvocationID        string `gorm:"primaryKey"`
	RunID               string `gorm:"not null;uniqueIndex"`
	ContextRevisionHash string `gorm:"not null;index"`
	RunContentHash      string `gorm:"not null"`
	Provider            string `gorm:"not null"`
	Model               string `gorm:"not null"`
	InputHash           string `gorm:"not null"`

	AttemptCount         int `gorm:"not null;default:0"`
	BudgetedAttemptCount int `gorm:"not null;default:0"`

	Status              AIInvocationStatus `gorm:"not null;index"`
	Score               *int
	OutputHash          string
	InputTokens         int
	CachedInputTokens   int
	OutputTokens        int
	ReasoningTokens     *int
	UsageShape          AIInvocationUsageShape
	LatencyMs           int64
	ErrorClass          string
	FailureStage        string
	ErrorDetailCode     string
	ProviderHTTPStatus  *int
	RequestBytes        int
	ResponseBytes       int
	TraceStatus         m5ai.TraceStatus
	EstimatedCostMicros int64
	StartedAt           time.Time `gorm:"not null"`
	FinishedAt          *time.Time
}

// SourcingGreetingInvocation 是正式筛选批次中一位 selected 成员至多一行的
// 招呼语生成调用终局事实。BatchID/RunID/ProfileID 三重绑定防止跨批
// 复用；调用前预留沿用 transportFailed+FinishedAt=NULL 表示 inFlight，
// 且只有 ok 终局允许保存 GreetingText/ContentHash 业务正文事实。按
// 2026-07-28 并行重试裁决，inFlight 行允许被生成编排器续驱动，尝试计数
// 语义与 SourcingScoreInvocation 相同。
type SourcingGreetingInvocation struct {
	InvocationID        string `gorm:"primaryKey"`
	BatchID             string `gorm:"not null;index"`
	RunID               string `gorm:"not null;uniqueIndex"`
	ProfileID           string `gorm:"not null;uniqueIndex"`
	ContextRevisionHash string `gorm:"not null;index"`
	RunContentHash      string `gorm:"not null"`
	Provider            string `gorm:"not null"`
	Model               string `gorm:"not null"`
	InputHash           string `gorm:"not null"`

	AttemptCount         int `gorm:"not null;default:0"`
	BudgetedAttemptCount int `gorm:"not null;default:0"`

	Status              AIInvocationStatus `gorm:"not null;index"`
	GreetingText        string
	ContentHash         string
	OutputHash          string
	InputTokens         int
	CachedInputTokens   int
	OutputTokens        int
	ReasoningTokens     *int
	UsageShape          AIInvocationUsageShape
	LatencyMs           int64
	ErrorClass          string
	FailureStage        string
	ErrorDetailCode     string
	ProviderHTTPStatus  *int
	RequestBytes        int
	ResponseBytes       int
	TraceStatus         m5ai.TraceStatus
	EstimatedCostMicros int64
	StartedAt           time.Time `gorm:"not null"`
	FinishedAt          *time.Time

	// EffectIntentID 把一次成功生成事实单调绑定到唯一招呼意图。NULL 表示尚未
	// 进入副作用轨道；一旦绑定，只能由该 EffectIntent 的既有恢复轨收敛。
	EffectIntentID  *string `gorm:"uniqueIndex"`
	EffectStartedAt *time.Time
}

type SourcingSelectionOutcome string

const (
	SourcingSelectionSelected             SourcingSelectionOutcome = "selected"
	SourcingSelectionScoreBelowThreshold  SourcingSelectionOutcome = "scoreBelowThreshold"
	SourcingSelectionContactStateRejected SourcingSelectionOutcome = "contactStateRejected"
	SourcingSelectionScoringFailed        SourcingSelectionOutcome = "scoringFailed"
	SourcingSelectionExistingProfile      SourcingSelectionOutcome = "existingProfile"
	SourcingSelectionQuotaFull            SourcingSelectionOutcome = "quotaFull"
	SourcingSelectionMaleRatioLimited     SourcingSelectionOutcome = "maleRatioLimited"
)

// SourcingBatchSelection 是一次正式采集批次的完整筛选摘要。BatchID 主键既
// 保证一个批次只能形成一份名单，也让重放只能读取首次原子提交的结果；所有
// 候选人身份与逐人分数仍只存在于 run/decision 业务事实中。
type SourcingBatchSelection struct {
	BatchID             string `gorm:"primaryKey"`
	ContextRevisionHash string `gorm:"not null;index"`
	AlgorithmVersion    string `gorm:"not null"`

	MinScore       int `gorm:"not null"`
	TargetMin      int `gorm:"not null"`
	TargetMax      int `gorm:"not null"`
	TargetCount    int `gorm:"not null"`
	MaleRatioLimit int `gorm:"not null"`
	MaleLimit      int `gorm:"not null"`

	PoolCount          int `gorm:"not null"`
	EligibleCount      int `gorm:"not null"`
	SelectedCount      int `gorm:"not null"`
	MaleSelectedCount  int `gorm:"not null"`
	UnknownGenderCount int `gorm:"not null"`

	CompletedAt time.Time `gorm:"not null"`
	CreatedAt   time.Time
}

// SourcingSelectionDecision 是评分事实到候选人档案之间的一次性确定性裁决。
// 它只保存随机引用、配置 hash 与数值结果，不复制平台身份或简历正文。
// RunID 作为主键保证低分/失败/已有档案也只裁决一次，不会堵住后续采集。
type SourcingSelectionDecision struct {
	RunID               string `gorm:"primaryKey"`
	ContextRevisionHash string `gorm:"not null;index"`
	Score               *int
	MinScore            int                      `gorm:"not null"`
	Outcome             SourcingSelectionOutcome `gorm:"not null;index"`
	ProfileID           *string                  `gorm:"index"`
	DecidedAt           time.Time                `gorm:"not null"`
	CreatedAt           time.Time
}

// CandidateGreetingHead 是招呼前无 conversationRef 时的持久单调 CAS 锚。
// 它不是第二套 intent 账本；LatestIntentID 永远指向既有 EffectIntent。
type CandidateGreetingHead struct {
	ProfileID      string `gorm:"primaryKey"`
	LatestIntentID string `gorm:"not null;uniqueIndex"`
	Generation     uint64 `gorm:"not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TrackingState 表示列表索引是否已被脑正式选入对账范围。
type TrackingState string

const (
	TrackingUntracked TrackingState = "untracked"
	TrackingPending   TrackingState = "pending"
	TrackingAdopted   TrackingState = "adopted"
)

// Conversation 是会话列表索引与消息账本游标。conversationRef 对脑是不透明稳定键。
// 候选人身份只锚 platform+platformUserRef,绝不锚 resumeNumber。
type Conversation struct {
	Platform        string `gorm:"primaryKey;index:idx_conversations_account,priority:1;index:idx_conversation_peer,priority:1"`
	AccountRef      string `gorm:"primaryKey;index:idx_conversations_account,priority:2"`
	ConversationRef string `gorm:"primaryKey"`

	PlatformUserRef      string `gorm:"index:idx_conversation_peer,priority:2"`
	PeerDisplayName      string
	UnreadCount          int
	LastMessageDirection string
	LastMessageKind      string
	LastMessagePreview   string
	LastActivityMs       *int64
	LastListedRoundID    string
	LastListedAt         *time.Time

	TrackingState      TrackingState `gorm:"not null;index"`
	AdoptedBoundarySeq int64
	// RespondedThroughSeq 是「已回应至」水位(2026-08-02 甲方裁决决策 3,
	// 2026-08-27 随停机点第二步固化落地):巡检静默收尾已处理到的消息位置,
	// 只作加速下界,供后续现算裁决压掉静默尾巴的每轮重判。三条纪律(v4
	// 规格 §一同名条目):只做下界、永不做闸——不许相等比对、不许 CAS、
	// 不许作停机条件,数值异常即当不存在、按锚重算;单写单向只增,唯一
	// 写入点是巡检静默收尾;水位实际压掉消息时记日志。
	RespondedThroughSeq int64 `gorm:"not null;default:0"`
	LastMessageSeq      int64
	LastSyncedRoundID  string
	LastSyncedAt       *time.Time

	// 巡检单人隔离标记（2026-07-27 甲方裁决：单个候选人的确定性错误只隔离
	// 该会话，不停整个账号轮）。打标后人工解除前，巡检不再自动对账或推进
	// 该会话；这是运行状态列，不是业务事实删除。
	PatrolQuarantinedAt    *time.Time
	PatrolQuarantineReason string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TrackedIntent 是用户或未来系统招呼成功后创建的正式收编意图,不是测试开关。
type TrackedIntent struct {
	Platform        string        `gorm:"primaryKey"`
	AccountRef      string        `gorm:"primaryKey"`
	ConversationRef string        `gorm:"primaryKey"`
	Status          TrackingState `gorm:"not null;index"` // pending / adopted
	RequestedBy     string
	RequestedAt     time.Time
	AdoptedAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Message 是脑内有序消息账本。Seq 是脑分配的会话内序号,不是平台消息 ID。
type Message struct {
	Platform        string `gorm:"primaryKey;index:idx_messages_conversation,priority:1;uniqueIndex:idx_messages_source_key,priority:1"`
	AccountRef      string `gorm:"primaryKey;index:idx_messages_conversation,priority:2;uniqueIndex:idx_messages_source_key,priority:2"`
	ConversationRef string `gorm:"primaryKey;index:idx_messages_conversation,priority:3;uniqueIndex:idx_messages_source_key,priority:3"`
	Seq             int64  `gorm:"primaryKey;autoIncrement:false"`

	Direction   string `gorm:"not null"`
	Kind        string `gorm:"not null"`
	ContentHash string `gorm:"not null;index"`
	Text        *string
	BlobRef     string
	CardType    string
	CardState   string
	// Interview* is the stable, platform-neutral identity projection for an
	// interviewInvite card. All three columns are NULL for legacy/unmapped
	// cards and for every other message kind.
	InterviewStartsAtMs *int64
	InterviewEndsAtMs   *int64
	InterviewMethod     *string
	TsApproxMs          *int64
	Origin              string
	FirstSeenRoundID    string `gorm:"index"`
	// SourceKey 只是会话内等值键，不是平台消息 ID。指针保证旧消息
	// 与无稳定身份的消息真正落为 SQL NULL；json:"-" 防止它被模型
	// 直接序列化到管理端或 UI。
	SourceKey *string `json:"-" gorm:"uniqueIndex:idx_messages_source_key,priority:4"`
	// OutboundIntentID 只在 SX 成功终局与消息账本事实同一事务
	// 追加时填写。用 nullable 唯一索引避免旧/外部消息的空值互相冲突。
	OutboundIntentID *string `gorm:"uniqueIndex"`
	// RetractedAt 表示这条脑侧消息事实已被更强证据推翻，不是
	// 平台真的撤回了消息。它是普通显式字段，不得换成会隐式过滤的
	// gorm.DeletedAt；活动账本与审计/去重查询必须各自明示选择语义。
	RetractedAt      *time.Time
	RetractionReason string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// CardTransitionFact 是卡片状态跃迁的追加式事实 outbox。事实与 Message.CardState
// 在同一事务提交；消费方只写确认时间，不删除事实。复合主键允许同一张卡按状态机
// 发生多步跃迁，同时让同一 from→to 的重复对账在数据库层也无法重复入账。
type CardTransitionFact struct {
	Platform        string `gorm:"primaryKey"`
	AccountRef      string `gorm:"primaryKey"`
	ConversationRef string `gorm:"primaryKey"`
	MessageSeq      int64  `gorm:"primaryKey;autoIncrement:false"`
	FromState       string `gorm:"primaryKey"`
	ToState         string `gorm:"primaryKey"`

	RoundID     string    `gorm:"not null;index"`
	ContentHash string    `gorm:"not null"`
	CardType    string    `gorm:"not null"`
	CreatedAt   time.Time `gorm:"not null;index:idx_card_transition_pending,priority:2"`

	AcknowledgedAt *time.Time `gorm:"index:idx_card_transition_pending,priority:1"`
}

// PatrolRound 记录账号 actor 一轮的可恢复进度与结果。身份键包含 platform/accountRef。
type PatrolRound struct {
	Platform        string `gorm:"primaryKey;index:idx_patrol_round_status,priority:1"`
	AccountRef      string `gorm:"primaryKey;index:idx_patrol_round_status,priority:2"`
	RoundID         string `gorm:"primaryKey"`
	Trigger         string
	Status          string `gorm:"not null;index:idx_patrol_round_status,priority:3"`
	Stage           string
	ListComplete    *bool
	NewMessageCount int
	ErrorCode       string
	StartedAt       time.Time
	FinishedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
