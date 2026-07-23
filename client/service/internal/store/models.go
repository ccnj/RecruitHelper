package store

import (
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
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
	CreatedAt                time.Time
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
	TargetCount         int    `gorm:"not null;check:ck_sourcing_batch_target_count,target_count > 0"`
	PositionRef         *string
	PositionTitle       *string
	PositionBoundAt     *time.Time

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
	MainStatus      CandidateProfileStatus `gorm:"not null;index"`
	EndReason       *CandidateProfileEndReason

	SuccessfulGreetingIntentID     *string
	ConversationRef                *string `gorm:"uniqueIndex:ux_candidate_profile_conversation,priority:3"`
	GreetedAt                      *time.Time
	CommunicatingAt                *time.Time
	FirstRealMessageSeq            *int64
	ResumeCaptureState             ResumeCaptureState `gorm:"not null;default:unattempted;index"`
	ResumeCaptureLogicalDispatchID *string            `gorm:"uniqueIndex"`
	ActiveResumeSnapshotID         *string            `gorm:"uniqueIndex"`
	ResumeCaptureAttemptedAt       *time.Time
	ResumeCaptureFailureReason     string
	CreatedAt                      time.Time
	UpdatedAt                      time.Time

	GreetingHead             *CandidateGreetingHead    `gorm:"foreignKey:ProfileID;references:ProfileID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	CommunicationV4Aggregate *CommunicationV4Aggregate `gorm:"foreignKey:ProfileID;references:ProfileID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
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
	CommunicationV4InputConfirmedAction CommunicationV4InputKind = "confirmedAction"
	CommunicationV4InputArchiveAction   CommunicationV4InputKind = "archiveAction"
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

// DialogueTurn 是一次不可变输入边界及其确定性处理状态。正文仍来自消息账本、
// 简历快照和职位 revision；此表只冻结稳定引用、边界和分类结果。
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
	FailureReason       string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type CommunicationActionKind string

const CommunicationActionReplyText CommunicationActionKind = "replyText"

type CommunicationActionStatus string

const (
	CommunicationActionPlanned        CommunicationActionStatus = "planned"
	CommunicationActionEffectPending  CommunicationActionStatus = "effectPending"
	CommunicationActionSent           CommunicationActionStatus = "sent"
	CommunicationActionManualRequired CommunicationActionStatus = "manualRequired"
	CommunicationActionSuperseded     CommunicationActionStatus = "superseded"
)

// CommunicationAction 是 AI 建议经确定性代码批准后的唯一业务动作事实。
// 本表本身不派发；后续批次只能从 ActionID 稳定派生一个 effect intent。
type CommunicationAction struct {
	ActionID        string                    `gorm:"primaryKey"`
	TurnID          string                    `gorm:"not null;index;uniqueIndex:ux_communication_action_turn_kind,priority:1"`
	Kind            CommunicationActionKind   `gorm:"not null;uniqueIndex:ux_communication_action_turn_kind,priority:2"`
	Text            string                    `gorm:"not null"`
	ContentHash     string                    `gorm:"not null"`
	Status          CommunicationActionStatus `gorm:"not null;index"`
	EffectIntentID  *string                   `gorm:"uniqueIndex"`
	FailureReason   string
	PlannedAt       time.Time `gorm:"not null"`
	EffectStartedAt *time.Time
	SentAt          *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
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

// SourcingScoreInvocation 是采集 run 的一次且仅一次评分调用事实。它与
// DialogueTurn/AIInvocation 分表，避免把 RunID 偷塞进 TurnID；调用前预留仍
// 沿用 transportFailed+FinishedAt=NULL 表示 inFlight，且不授权启动恢复重调。
type SourcingScoreInvocation struct {
	InvocationID        string `gorm:"primaryKey"`
	RunID               string `gorm:"not null;uniqueIndex"`
	ContextRevisionHash string `gorm:"not null;index"`
	RunContentHash      string `gorm:"not null"`
	Provider            string `gorm:"not null"`
	Model               string `gorm:"not null"`
	InputHash           string `gorm:"not null"`

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

// SourcingGreetingInvocation 是正式筛选批次中一位 selected 成员的一次且
// 仅一次招呼语生成调用事实。BatchID/RunID/ProfileID 三重绑定防止跨批
// 复用；调用前预留沿用 transportFailed+FinishedAt=NULL 表示 inFlight，
// 且只有 ok 终局允许保存 GreetingText/ContentHash 业务正文事实。
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
	LastMessageSeq     int64
	LastSyncedRoundID  string
	LastSyncedAt       *time.Time

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

	Direction        string `gorm:"not null"`
	Kind             string `gorm:"not null"`
	ContentHash      string `gorm:"not null;index"`
	Text             *string
	BlobRef          string
	CardType         string
	CardState        string
	TsApproxMs       *int64
	Origin           string
	FirstSeenRoundID string `gorm:"index"`
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
