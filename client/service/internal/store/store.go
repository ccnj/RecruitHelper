// Package store:脑的唯一账本(SQLite,单写者)。
// 驱动用 glebarez/sqlite(纯 Go,基于 modernc),避免 cgo 拖累 Windows 交叉编译。
package store

import (
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

type Store struct {
	db *gorm.DB
}

// Open 打开(必要时创建)数据目录下的 brain.db,开 WAL,建/补表。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("建数据目录: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
		filepath.Join(dataDir, "brain.db"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: newStoreLogger(os.Stderr),
	})
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite: %w", err)
	}
	// SQLite 单写:把连接池锁到 1 条连接,所有读写在同一连接上由 database/sql 串行化。
	// 否则多连接下并发写会撞 SQLITE_BUSY(FOR UPDATE 在 SQLite 是 no-op、busy_timeout 对
	// 快照升级死锁无效),错误被静默吞掉可致丢结果→假 suspect→人工误判→双发(红队 F1)。
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("取底层 DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	// 首个客户安装前已经退役整条配对/token 机制。AutoMigrate 不会删除旧表，
	// 因此升级过 M1 开发库时必须精确移除这张凭据表，不能让死 schema 继续
	// 冒充现行数据模型；其余旧表和业务数据一概不碰。
	if err := dropRetiredPairingSchema(db); err != nil {
		return nil, fmt.Errorf("删除已退役配对表: %w", err)
	}
	// SQLite 不允许给已有数据的表直接追加无默认值的 NOT NULL 列。
	// M1→M2 因而先做两阶段迁移:加可回填列、把每条旧命令设成独立逻辑根,
	// 再交给 AutoMigrate 补其余列/索引。重启发生在两步之间也可再入。
	if err := prepareCmdLineageMigration(db); err != nil {
		return nil, fmt.Errorf("预迁移命令逻辑派发: %w", err)
	}
	// 只有旧库首次引入 head 表时允许从历史意图做一次
	// 确定性迁移。表已存在但某会话 head 丢失属于损坏，
	// 重启不得重算并掩盖，后续读/写必须 fail-closed。
	effectHeadTableExisted := db.Migrator().HasTable(&ConversationEffectHead{})
	if err := db.AutoMigrate(
		&Hand{},
		&Account{},
		&SourcingBatch{},
		&SourcingCandidateRun{},
		&Candidate{},
		&CandidateProfile{},
		&CommunicationV4Aggregate{},
		&CommunicationV4ProjectionApplication{},
		&CommunicationV4EventAction{},
		&CommunicationV4ScheduleOccurrence{},
		&CandidateResumeSnapshot{},
		&M5TrialSelection{},
		&JobAIContextRevision{},
		&ProfileAIContextBinding{},
		&DialogueTurn{},
		&AIInvocation{},
		&SourcingScoreInvocation{},
		&SourcingGreetingInvocation{},
		&SourcingBatchSelection{},
		&SourcingSelectionDecision{},
		&CommunicationAction{},
		&EffectIntent{},
		&CandidateGreetingHead{},
		&ConversationEffectHead{},
		&CmdRecord{},
		&ProcessedMsg{},
		&Conversation{},
		&TrackedIntent{},
		&Message{},
		&CardTransitionFact{},
		&PatrolRound{},
		&AuditEntry{},
	); err != nil {
		return nil, fmt.Errorf("建表: %w", err)
	}
	backendJobBackfill, err := backfillBackendJobIDs(db)
	if err != nil {
		return nil, fmt.Errorf("回填后台职位 ID: %w", err)
	}
	if backendJobBackfill.BatchesUnresolved != 0 ||
		backendJobBackfill.ProfilesUnresolved != 0 ||
		backendJobBackfill.ProfilesAmbiguous != 0 {
		slog.Warn("存在无法唯一回填后台职位 ID 的历史事实",
			"batchUnresolved", backendJobBackfill.BatchesUnresolved,
			"profileUnresolved", backendJobBackfill.ProfilesUnresolved,
			"profileAmbiguous", backendJobBackfill.ProfilesAmbiguous,
		)
	}
	if err := db.Model(&CandidateProfile{}).
		Where("resume_capture_state IS NULL OR resume_capture_state = ?", "").
		UpdateColumn("resume_capture_state", ResumeCaptureUnattempted).Error; err != nil {
		return nil, fmt.Errorf("回填候选人简历补采状态: %w", err)
	}
	// M1 已有命令没有 logical_dispatch_id。M2 迁移把每条旧命令视为一条独立逻辑链的根,
	// 保证升级后重启扫描与 ledger 查询不出现不可达记录。
	if err := db.Model(&CmdRecord{}).
		Where("logical_dispatch_id IS NULL OR logical_dispatch_id = ?", "").
		UpdateColumn("logical_dispatch_id", gorm.Expr("msg_id")).Error; err != nil {
		return nil, fmt.Errorf("回填命令逻辑派发 ID: %w", err)
	}
	if !effectHeadTableExisted {
		if err := backfillConversationEffectHeads(db); err != nil {
			return nil, fmt.Errorf("回填会话副作用 head: %w", err)
		}
	}
	return &Store{db: db}, nil
}

// newStoreLogger 保留错误/慢查询可见性，但永不把绑定参数插回 SQL。
// Candidate 的不透明平台 userId 属于权威本机数据，不得因数据库错误进入日志。
func newStoreLogger(writer io.Writer) gormlogger.Interface {
	return gormlogger.New(log.New(writer, "\r\n", log.LstdFlags), gormlogger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  gormlogger.Warn,
		IgnoreRecordNotFoundError: false,
		ParameterizedQueries:      true,
		Colorful:                  false,
	})
}

// backfillConversationEffectHeads 只为尚无 head 的旧数据建立一次持久
// 锚点。旧库无法恢复真实插入顺序，故用 created_at+intent_id 作确定性
// 迁移裁决；一旦 head 存在，后续 Open/VACUUM 永不重新排序或覆盖。
func backfillConversationEffectHeads(db *gorm.DB) error {
	type key struct {
		platform, accountRef, conversationRef string
	}
	type candidate struct {
		latest     EffectIntent
		generation uint64
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var intents []EffectIntent
		if err := tx.Where("primitive = ?", primitiveChatSendMessage).
			Order("platform, account_ref, target_ref, created_at, intent_id").Find(&intents).Error; err != nil {
			return err
		}
		candidates := make(map[key]candidate)
		for i := range intents {
			intent := intents[i]
			k := key{intent.Platform, intent.AccountRef, intent.TargetRef}
			current := candidates[k]
			if current.generation >= maxSQLiteEffectHeadGeneration {
				return fmt.Errorf("旧库会话副作用 generation 溢出: %s/%s/%s", k.platform, k.accountRef, k.conversationRef)
			}
			current.latest = intent
			current.generation++
			candidates[k] = current
		}
		for k, current := range candidates {
			head := ConversationEffectHead{
				Platform: k.platform, AccountRef: k.accountRef, ConversationRef: k.conversationRef,
				LatestIntentID: current.latest.IntentID, Generation: current.generation,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&head).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

const retiredPairedHandsTable = "paired_hands"

func dropRetiredPairingSchema(db *gorm.DB) error {
	if !db.Migrator().HasTable(retiredPairedHandsTable) {
		return nil
	}
	return db.Migrator().DropTable(retiredPairedHandsTable)
}

func prepareCmdLineageMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&CmdRecord{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&CmdRecord{}, "LogicalDispatchID") {
		if err := db.Exec("ALTER TABLE cmd_records ADD COLUMN logical_dispatch_id text").Error; err != nil {
			return err
		}
	}
	return db.Exec("UPDATE cmd_records SET logical_dispatch_id = msg_id WHERE logical_dispatch_id IS NULL OR logical_dispatch_id = ''").Error
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// JournalMode 返回当前 journal_mode(状态页与测试用)。
func (s *Store) JournalMode() (string, error) {
	var mode string
	err := s.db.Raw("PRAGMA journal_mode").Scan(&mode).Error
	return mode, err
}

// ---------- 手 ----------

// RegisterHand 按 handId 幂等登记/复用本地手。SQLite 单连接使并发首次
// hello 的查询与写入串行化；既有手保留 CreatedAt/Label，只刷新 Origin 与
// LastSeenAt。previousOrigin 供会话层做软审计；首次登记时为空。
func (s *Store) RegisterHand(handID, origin string, now time.Time) (hand Hand, created bool, previousOrigin string, err error) {
	err = s.db.Transaction(func(tx *gorm.DB) error {
		lookupErr := tx.First(&hand, "hand_id = ?", handID).Error
		switch {
		case errors.Is(lookupErr, gorm.ErrRecordNotFound):
			hand = Hand{HandID: handID, Origin: origin, CreatedAt: now, LastSeenAt: now}
			if createErr := tx.Create(&hand).Error; createErr != nil {
				return createErr
			}
			created = true
			return nil
		case lookupErr != nil:
			return lookupErr
		default:
			previousOrigin = hand.Origin
			hand.Origin = origin
			hand.LastSeenAt = now
			return tx.Model(&Hand{}).Where("hand_id = ?", handID).Updates(map[string]any{
				"origin": origin, "last_seen_at": now,
			}).Error
		}
	})
	return hand, created, previousOrigin, err
}

// HandByID:未找到返回 (nil, nil)。
func (s *Store) HandByID(id string) (*Hand, error) {
	var h Hand
	err := s.db.First(&h, "hand_id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (s *Store) TouchHand(id string, t time.Time) error {
	return s.db.Model(&Hand{}).Where("hand_id = ?", id).Update("last_seen_at", t).Error
}

func (s *Store) Hands() ([]Hand, error) {
	var hs []Hand
	err := s.db.Order("hand_id").Find(&hs).Error
	return hs, err
}

// ---------- 命令账本(write-ahead) ----------

func (s *Store) CreateCmd(c *CmdRecord) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		return createRootCmd(tx, c)
	})
}

// CmdByMsgID:未找到返回 (nil, nil)。
func (s *Store) CmdByMsgID(msgID string) (*CmdRecord, error) {
	var c CmdRecord
	err := s.db.First(&c, "msg_id = ?", msgID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// MutateCmd:事务内读改写一条账本记录;mutate 里改状态时负责维护 TerminalAt。
// 串行化靠 SetMaxOpenConns(1)——单连接上事务天然互斥(SQLite 无行锁,FOR UPDATE 是 no-op,故不用)。
// 推进到终局状态时自动盖 TerminalAt(若 mutate 未盖)。
func (s *Store) MutateCmd(msgID string, mutate func(*CmdRecord) error) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var c CmdRecord
		if err := tx.First(&c, "msg_id = ?", msgID).Error; err != nil {
			return err
		}
		if err := mutate(&c); err != nil {
			return err
		}
		if c.Status.Terminal() && c.TerminalAt == nil {
			now := time.Now()
			c.TerminalAt = &now
		}
		return tx.Save(&c).Error
	})
}

func (s *Store) CmdsInStatus(statuses ...CmdStatus) ([]CmdRecord, error) {
	var cs []CmdRecord
	err := s.db.Where("status IN ?", statuses).Order("created_at").Find(&cs).Error
	return cs, err
}

// RecentCmds:最近 limit 条命令账本,按创建倒序(ledger 视图/测试)。
func (s *Store) RecentCmds(limit int) ([]CmdRecord, error) {
	var cs []CmdRecord
	err := s.db.Order("created_at DESC").Limit(limit).Find(&cs).Error
	return cs, err
}

// 非终局状态集(超时引擎与重连收编、脑重启扫描的扫描面)。
var nonTerminalStatuses = []CmdStatus{
	CmdQueued, CmdSent, CmdAccepted, CmdPendingReconcile, CmdVerifying,
}

// NonTerminalCmds:全部非终局命令(超时引擎扫描 + 脑重启扫描)。
func (s *Store) NonTerminalCmds() ([]CmdRecord, error) {
	var cs []CmdRecord
	err := s.db.Where("status IN ?", nonTerminalStatuses).Order("created_at").Find(&cs).Error
	return cs, err
}

// NonTerminalCmdsForHand:某手的非终局命令(重连收编用)。
func (s *Store) NonTerminalCmdsForHand(handID string) ([]CmdRecord, error) {
	var cs []CmdRecord
	err := s.db.Where("hand_id = ? AND status IN ?", handID, nonTerminalStatuses).Order("created_at").Find(&cs).Error
	return cs, err
}

// SuspectCmds:全部 suspect 命令(人工队列展示)。
func (s *Store) SuspectCmds() ([]CmdRecord, error) {
	var cs []CmdRecord
	err := s.db.Where("status = ?", CmdSuspect).Order("created_at").Find(&cs).Error
	return cs, err
}

// HasSuspectInDomain:该串行域是否存在 suspect(法条4 串行域冻结)。
func (s *Store) HasSuspectInDomain(domain string) (bool, error) {
	var n int64
	err := s.db.Model(&CmdRecord{}).Where("domain = ? AND status = ?", domain, CmdSuspect).Count(&n).Error
	return n > 0, err
}

// HasSuspectIdemKey:该幂等键是否被 suspect 冻结(法条3 幂等键冻结)。
func (s *Store) HasSuspectIdemKey(idemKey string) (bool, error) {
	if idemKey == "" {
		return false, nil
	}
	var n int64
	err := s.db.Model(&CmdRecord{}).Where("idem_key = ? AND status = ?", idemKey, CmdSuspect).Count(&n).Error
	return n > 0, err
}

// ---------- 上行去重 ----------

// MarkProcessed:首见返回 already=false 并落库;重复返回 already=true。
func (s *Store) MarkProcessed(msgID, kind, handID string) (already bool, err error) {
	res := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&ProcessedMsg{
		MsgID: msgID, Kind: kind, HandID: handID, ProcessedAt: time.Now(),
	})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 0, nil
}

// ---------- 审计 ----------

// Audit:尽力而为的留痕;写失败只打日志,不让审计失败拖垮主流程。
func (s *Store) Audit(category, handID, refMsgID, detail string) {
	err := s.db.Create(&AuditEntry{
		At: time.Now(), Category: category, HandID: handID, RefMsgID: refMsgID, Detail: detail,
	}).Error
	if err != nil {
		slog.Error("审计写入失败", "category", category, "err", err)
	}
}

func (s *Store) AuditEntries(limit int) ([]AuditEntry, error) {
	var es []AuditEntry
	err := s.db.Order("id DESC").Limit(limit).Find(&es).Error
	return es, err
}
