package store

import "time"

// 骨架期临时表。宪法约定:表结构当前全部视为临时,骨架期用 AutoMigrate 快跑;
// 首个对外发布前(与表结构正式设计同期)切换为 append-only migration。

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
	CreatedAt     time.Time
	UpdatedAt     time.Time
	TerminalAt    *time.Time // 进入终局的时刻
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
