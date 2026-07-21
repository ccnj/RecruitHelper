package store

import (
	"time"

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

// EffectIntent 实现 §7.5 的脑账本闸。IntentID 由管理客户端在一次
// 真人确认里生成并在 HTTP 重试中复用；IdemKey 由脑依据该 ID 确定性派生。
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

	CreatedAt time.Time
	UpdatedAt time.Time
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

// CandidateProfileStatus 是人×职位档案的主线状态。M4 只生产
// selected→greeted，或明确 GREETING_REJECTED 时 selected→ended；
// eliminated 只为人级建档闸的既定语义保留。
type CandidateProfileStatus string

const (
	CandidateProfileSelected   CandidateProfileStatus = "selected"
	CandidateProfileGreeted    CandidateProfileStatus = "greeted"
	CandidateProfileEnded      CandidateProfileStatus = "ended"
	CandidateProfileEliminated CandidateProfileStatus = "eliminated"
)

type CandidateProfileEndReason string

const CandidateProfileEndGreetingFailed CandidateProfileEndReason = "greetingFailed"

type ResumeCaptureState string

const (
	ResumeCaptureUnattempted    ResumeCaptureState = "unattempted"
	ResumeCaptureInFlight       ResumeCaptureState = "inFlight"
	ResumeCaptureCaptured       ResumeCaptureState = "captured"
	ResumeCaptureManualRequired ResumeCaptureState = "manualRequired"
)

// CandidateProfile 是沟通状态主体。人级建档闸的部分唯一索引刻意不含
// AccountRef，并把 ended 也视为非 eliminated，防止换账号/职位重复追求。
// ConversationRef 必须为 NULL 而不是空串，否则未建联档案会互相撞唯一键。
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
	ResumeCaptureState             ResumeCaptureState `gorm:"not null;default:unattempted;index"`
	ResumeCaptureLogicalDispatchID *string            `gorm:"uniqueIndex"`
	ActiveResumeSnapshotID         *string            `gorm:"uniqueIndex"`
	ResumeCaptureAttemptedAt       *time.Time
	ResumeCaptureFailureReason     string
	CreatedAt                      time.Time
	UpdatedAt                      time.Time

	GreetingHead *CandidateGreetingHead `gorm:"foreignKey:ProfileID;references:ProfileID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
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
	Platform        string `gorm:"primaryKey;index:idx_messages_conversation,priority:1"`
	AccountRef      string `gorm:"primaryKey;index:idx_messages_conversation,priority:2"`
	ConversationRef string `gorm:"primaryKey;index:idx_messages_conversation,priority:3"`
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
