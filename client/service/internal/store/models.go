package store

import "time"

// 骨架期临时表。宪法约定:表结构当前全部视为临时,骨架期用 AutoMigrate 快跑;
// 首个对外发布前(与表结构正式设计同期)切换为 append-only migration。

// PairedHand:配对过的手(工牌)。token 只落哈希,明文不入库。
type PairedHand struct {
	HandID     string `gorm:"primaryKey"` // 脑签发,hand-01 式可读名
	TokenHash  string `gorm:"not null"`   // sha256(token) 十六进制
	Origin     string `gorm:"not null"`   // 配对时学习到的扩展 Origin(变更=软告警,见规格 §2.2)
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
	MsgID            string    `gorm:"primaryKey"`
	Name             string    `gorm:"not null;index"` // 原语名
	Class            string    `gorm:"not null"`       // readonly / intrusive / effectful
	IdemKey          string    `gorm:"index"`          // 仅 effectful
	HandID           string    `gorm:"not null;index"`
	Session          string    // 派发时会话(重投时更新)
	BootIDAtDispatch string    // 派发时手的 bootId(重连后同 msgId 重发的前提)
	Status           CmdStatus `gorm:"not null;index"`
	Attempt          int       // 同 msgId 第几次发送
	DeadlineMs       int64     // 绝对毫秒(脑钟);suspect 判定 = deadline+宽限 无终局
	ExecBudgetMs     int64
	ErrorCode        string // 终局为 failed 时
	SideEffect       string // 终局 error 的副作用标注(none/possible/confirmed)
	ResultBody       string // 终局 result 的 body JSON(审计与重放)
	SuspectReason    string // 进 suspect 的原因(deadline/bootId 换代/脑重启扫描/sideEffect=possible)
	CreatedAt        time.Time
	UpdatedAt        time.Time
	TerminalAt       *time.Time // 进入终局的时刻
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
	ID       uint      `gorm:"primaryKey;autoIncrement"`
	At       time.Time `gorm:"index"`
	Category string    `gorm:"not null;index"`
	HandID   string
	RefMsgID string
	Detail   string // JSON 或短文本
}
