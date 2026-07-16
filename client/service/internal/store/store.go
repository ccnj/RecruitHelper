// Package store:脑的唯一账本(SQLite,单写者)。
// 驱动用 glebarez/sqlite(纯 Go,基于 modernc),避免 cgo 拖累 Windows 交叉编译。
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite: %w", err)
	}
	if err := db.AutoMigrate(&PairedHand{}, &CmdRecord{}, &ProcessedMsg{}, &AuditEntry{}); err != nil {
		return nil, fmt.Errorf("建表: %w", err)
	}
	return &Store{db: db}, nil
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

// HashToken:token 明文 → 落库哈希。
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ---------- 手(工牌) ----------

func (s *Store) UpsertHand(h *PairedHand) error {
	return s.db.Save(h).Error
}

// HandByID:未找到返回 (nil, nil)。
func (s *Store) HandByID(id string) (*PairedHand, error) {
	var h PairedHand
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
	return s.db.Model(&PairedHand{}).Where("hand_id = ?", id).Update("last_seen_at", t).Error
}

func (s *Store) Hands() ([]PairedHand, error) {
	var hs []PairedHand
	err := s.db.Order("hand_id").Find(&hs).Error
	return hs, err
}

// ---------- 命令账本(write-ahead) ----------

func (s *Store) CreateCmd(c *CmdRecord) error {
	return s.db.Create(c).Error
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
// 推进到终局状态时自动盖 TerminalAt(若 mutate 未盖)。
func (s *Store) MutateCmd(msgID string, mutate func(*CmdRecord) error) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var c CmdRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, "msg_id = ?", msgID).Error; err != nil {
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
