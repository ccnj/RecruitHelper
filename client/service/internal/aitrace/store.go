// Package aitrace persists the complete local request/response corpus for AI
// calls. It intentionally owns a separate SQLite database from brain.db.
package aitrace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	// SchemaVersion is written into every immutable trace row so later readers
	// can distinguish storage shapes without guessing from nullable columns.
	SchemaVersion = 1

	databaseFilename = "ai-traces.db"

	TraceStateRequestCaptured = "requestCaptured"
	TraceStateCompleted       = "completed"
)

var (
	// ErrConflict means an invocation ID was reused with different immutable
	// request data or a finished trace was completed with different data.
	ErrConflict = errors.New("AI trace 内容冲突")
	// ErrNotFound means Finish/Get referenced an invocation that was never
	// begun in this trace database.
	ErrNotFound = errors.New("AI trace 不存在")
)

// TransportCode is a bounded, non-sensitive failure classification. Raw
// network errors must never be placed in this field.
type TransportCode string

const (
	TransportNone           TransportCode = ""
	TransportRequestInvalid TransportCode = "requestInvalid"
	TransportCanceled       TransportCode = "canceled"
	TransportTimeout        TransportCode = "timeout"
	TransportNetwork        TransportCode = "network"
	TransportResponseRead   TransportCode = "responseRead"
	TransportUnknown        TransportCode = "transport"
)

// BeginRecord contains only provider request material and non-secret metadata.
// Endpoint, base URL, headers, API keys and authorization values deliberately
// have no representation in this API.
//
// Purpose is free text owned by the caller: this package records whatever the
// caller calls its own AI use, and only requires it to be non-empty. There used
// to be an enumeration whitelist here, and it silently rejected every trace of
// the three purposes added after it was written (serviceReply, jobClass,
// jobKeywords) — a second copy of a list that lives in m5ai has no way to stay
// in sync. Do not reintroduce one (2026-08-01 甲方裁决).
type BeginRecord struct {
	InvocationID        string
	Purpose             string
	Provider            string
	Model               string
	ConfigHash          string
	ContextRevisionHash string
	PromptRevision      string
	RequestJSON         []byte
	StartedAt           time.Time
}

// FinishRecord completes an existing trace after the transport attempt.
// RawResponse is the exact response bytes observed by the caller. A nil slice
// denotes that no response bytes were observed; a non-nil empty slice denotes
// an observed empty response.
type FinishRecord struct {
	InvocationID  string
	HTTPStatus    *int
	RawResponse   []byte
	TransportCode TransportCode
	FinishedAt    time.Time
}

// Trace is the read model for one persisted request/response pair.
type Trace struct {
	SchemaVersion       int
	InvocationID        string
	Purpose             string
	Provider            string
	Model               string
	ConfigHash          string
	ContextRevisionHash string
	PromptRevision      string
	RequestJSON         []byte
	RequestBytes        int64
	RequestHash         string
	StartedAt           time.Time
	TraceState          string
	HTTPStatus          *int
	ResponsePresent     bool
	RawResponse         []byte
	ResponseBytes       int64
	ResponseHash        string
	TransportCode       TransportCode
	FinishedAt          *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type traceRow struct {
	SchemaVersion       int       `gorm:"not null"`
	InvocationID        string    `gorm:"primaryKey;size:128"`
	Purpose             string    `gorm:"not null;size:64"`
	Provider            string    `gorm:"not null;size:128"`
	Model               string    `gorm:"not null;size:256"`
	ConfigHash          string    `gorm:"not null;size:128"`
	ContextRevisionHash string    `gorm:"not null;size:128"`
	PromptRevision      *string   `gorm:"size:128"`
	RequestJSON         []byte    `gorm:"not null"`
	RequestBytes        int64     `gorm:"not null"`
	RequestHash         string    `gorm:"not null;size:64"`
	StartedAt           time.Time `gorm:"not null"`
	TraceState          string    `gorm:"not null;size:32"`
	HTTPStatus          *int
	ResponseBody        []byte
	ResponseBytes       int64   `gorm:"not null"`
	ResponseHash        *string `gorm:"size:64"`
	TransportCode       string  `gorm:"size:32"`
	FinishedAt          *time.Time
	CreatedAt           time.Time `gorm:"not null"`
	UpdatedAt           time.Time `gorm:"not null"`
}

func (traceRow) TableName() string { return "ai_traces" }

// Store owns the standalone ai-traces.db connection.
type Store struct {
	db *gorm.DB
}

// Open creates or opens dataDir/ai-traces.db and serializes SQLite access
// through one connection, matching the brain database's single-writer rule.
func Open(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("AI trace 数据目录为空")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 AI trace 数据目录: %w", err)
	}
	databasePath := filepath.Join(dataDir, databaseFilename)
	if err := preparePrivateDatabaseFile(databasePath); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
		databasePath,
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		// A trace contains prompt and response bodies. Never let the ORM echo
		// bound values into stderr when a query fails.
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("打开 AI trace SQLite: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("取得 AI trace 底层 DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&traceRow{}); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("迁移 AI trace 表: %w", err)
	}
	if err := secureSQLiteFiles(databasePath); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func preparePrivateDatabaseFile(databasePath string) error {
	file, err := os.OpenFile(databasePath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("创建 AI trace SQLite: %w", err)
	}
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("关闭 AI trace SQLite 预创建句柄: %w", closeErr)
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		return fmt.Errorf("收紧 AI trace SQLite 权限: %w", err)
	}
	return nil
}

func secureSQLiteFiles(databasePath string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := databasePath + suffix
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("收紧 AI trace SQLite 辅助文件权限: %w", err)
		}
	}
	return nil
}

// Close releases this store's standalone SQLite connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("取得 AI trace 底层 DB: %w", err)
	}
	return sqlDB.Close()
}

// Begin durably records the complete request before transport. Repeating the
// exact same record is idempotent; reusing the invocation ID with different
// content is a loud ErrConflict.
func (s *Store) Begin(ctx context.Context, record BeginRecord) error {
	if err := validateBegin(record); err != nil {
		return err
	}
	row := traceRow{
		SchemaVersion:       SchemaVersion,
		InvocationID:        record.InvocationID,
		Purpose:             record.Purpose,
		Provider:            record.Provider,
		Model:               record.Model,
		ConfigHash:          record.ConfigHash,
		ContextRevisionHash: record.ContextRevisionHash,
		PromptRevision:      optionalString(record.PromptRevision),
		RequestJSON:         cloneBytes(record.RequestJSON),
		RequestBytes:        int64(len(record.RequestJSON)),
		RequestHash:         sha256Hex(record.RequestJSON),
		StartedAt:           record.StartedAt,
		TraceState:          TraceStateRequestCaptured,
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing traceRow
		err := tx.Where("invocation_id = ?", record.InvocationID).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("写入 AI trace 请求: %w", err)
			}
			return nil
		case err != nil:
			return fmt.Errorf("读取 AI trace 请求: %w", err)
		case sameBegin(existing, row):
			return nil
		default:
			return fmt.Errorf("%w: invocation=%s begin", ErrConflict, record.InvocationID)
		}
	})
}

// Finish completes an existing request trace. Repeating the exact completion
// is idempotent; a second, different completion is a loud ErrConflict.
func (s *Store) Finish(ctx context.Context, record FinishRecord) error {
	if err := validateFinish(record); err != nil {
		return err
	}
	var responseHash *string
	if record.RawResponse != nil {
		digest := sha256Hex(record.RawResponse)
		responseHash = &digest
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing traceRow
		err := tx.Where("invocation_id = ?", record.InvocationID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: invocation=%s", ErrNotFound, record.InvocationID)
		}
		if err != nil {
			return fmt.Errorf("读取待完成 AI trace: %w", err)
		}
		finishedAt := record.FinishedAt
		completion := traceRow{
			HTTPStatus:    cloneInt(record.HTTPStatus),
			ResponseBody:  cloneBytes(record.RawResponse),
			ResponseBytes: int64(len(record.RawResponse)),
			ResponseHash:  responseHash,
			TransportCode: string(record.TransportCode),
			FinishedAt:    &finishedAt,
		}
		if existing.TraceState == TraceStateCompleted {
			if sameFinish(existing, completion) {
				return nil
			}
			return fmt.Errorf("%w: invocation=%s finish", ErrConflict, record.InvocationID)
		}
		if existing.TraceState != TraceStateRequestCaptured {
			return fmt.Errorf("AI trace 状态无效: invocation=%s state=%s",
				record.InvocationID, existing.TraceState)
		}
		updates := map[string]any{
			"trace_state":    TraceStateCompleted,
			"http_status":    completion.HTTPStatus,
			"response_body":  completion.ResponseBody,
			"response_bytes": completion.ResponseBytes,
			"response_hash":  completion.ResponseHash,
			"transport_code": completion.TransportCode,
			"finished_at":    completion.FinishedAt,
		}
		if err := tx.Model(&traceRow{}).
			Where("invocation_id = ? AND trace_state = ?",
				record.InvocationID, TraceStateRequestCaptured).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("完成 AI trace: %w", err)
		}
		return nil
	})
}

// Get returns a detached copy of one trace. It is intentionally a local
// package API, not an admin/report surface.
func (s *Store) Get(ctx context.Context, invocationID string) (Trace, error) {
	if strings.TrimSpace(invocationID) == "" {
		return Trace{}, errors.New("AI trace invocationId 为空")
	}
	var row traceRow
	err := s.db.WithContext(ctx).Where("invocation_id = ?", invocationID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Trace{}, fmt.Errorf("%w: invocation=%s", ErrNotFound, invocationID)
	}
	if err != nil {
		return Trace{}, fmt.Errorf("读取 AI trace: %w", err)
	}
	return row.toTrace(), nil
}

func validateBegin(record BeginRecord) error {
	switch {
	case strings.TrimSpace(record.InvocationID) == "":
		return errors.New("AI trace invocationId 为空")
	case strings.TrimSpace(record.Purpose) == "":
		return errors.New("AI trace purpose 为空")
	case strings.TrimSpace(record.Provider) == "":
		return errors.New("AI trace provider 为空")
	case strings.TrimSpace(record.Model) == "":
		return errors.New("AI trace model 为空")
	case strings.TrimSpace(record.ConfigHash) == "":
		return errors.New("AI trace configHash 为空")
	case strings.TrimSpace(record.ContextRevisionHash) == "":
		return errors.New("AI trace contextRevisionHash 为空")
	case len(record.RequestJSON) == 0 || !json.Valid(record.RequestJSON):
		return errors.New("AI trace requestJSON 不是有效 JSON")
	case record.StartedAt.IsZero():
		return errors.New("AI trace startedAt 为空")
	default:
		return nil
	}
}

func validateFinish(record FinishRecord) error {
	if strings.TrimSpace(record.InvocationID) == "" {
		return errors.New("AI trace invocationId 为空")
	}
	if record.FinishedAt.IsZero() {
		return errors.New("AI trace finishedAt 为空")
	}
	if record.HTTPStatus != nil && (*record.HTTPStatus < 100 || *record.HTTPStatus > 599) {
		return errors.New("AI trace HTTP status 越界")
	}
	if !validTransportCode(record.TransportCode) {
		return errors.New("AI trace transportCode 非安全枚举")
	}
	if record.HTTPStatus == nil && record.TransportCode == TransportNone {
		return errors.New("AI trace 无 HTTP 响应时必须提供安全 transportCode")
	}
	return nil
}

func validTransportCode(code TransportCode) bool {
	switch code {
	case TransportNone, TransportRequestInvalid, TransportCanceled, TransportTimeout,
		TransportNetwork, TransportResponseRead, TransportUnknown:
		return true
	default:
		return false
	}
}

func sameBegin(left, right traceRow) bool {
	return left.SchemaVersion == right.SchemaVersion &&
		left.InvocationID == right.InvocationID &&
		left.Purpose == right.Purpose &&
		left.Provider == right.Provider &&
		left.Model == right.Model &&
		left.ConfigHash == right.ConfigHash &&
		left.ContextRevisionHash == right.ContextRevisionHash &&
		equalString(left.PromptRevision, right.PromptRevision) &&
		left.RequestBytes == right.RequestBytes &&
		left.RequestHash == right.RequestHash &&
		bytes.Equal(left.RequestJSON, right.RequestJSON) &&
		left.StartedAt.Equal(right.StartedAt)
}

func sameFinish(existing, completion traceRow) bool {
	return equalInt(existing.HTTPStatus, completion.HTTPStatus) &&
		existing.ResponseBytes == completion.ResponseBytes &&
		equalString(existing.ResponseHash, completion.ResponseHash) &&
		bytes.Equal(existing.ResponseBody, completion.ResponseBody) &&
		existing.TransportCode == completion.TransportCode &&
		equalTime(existing.FinishedAt, completion.FinishedAt)
}

func (row traceRow) toTrace() Trace {
	rawResponse := cloneBytes(row.ResponseBody)
	// SQLite scans both NULL and a zero-length BLOB into a nil []byte. The
	// nullable response hash is the durable presence marker, so reconstruct an
	// observed empty body for callers.
	if row.ResponseHash != nil && rawResponse == nil {
		rawResponse = []byte{}
	}
	return Trace{
		SchemaVersion:       row.SchemaVersion,
		InvocationID:        row.InvocationID,
		Purpose:             row.Purpose,
		Provider:            row.Provider,
		Model:               row.Model,
		ConfigHash:          row.ConfigHash,
		ContextRevisionHash: row.ContextRevisionHash,
		PromptRevision:      dereferenceString(row.PromptRevision),
		RequestJSON:         cloneBytes(row.RequestJSON),
		RequestBytes:        row.RequestBytes,
		RequestHash:         row.RequestHash,
		StartedAt:           row.StartedAt,
		TraceState:          row.TraceState,
		HTTPStatus:          cloneInt(row.HTTPStatus),
		ResponsePresent:     row.ResponseHash != nil,
		RawResponse:         rawResponse,
		ResponseBytes:       row.ResponseBytes,
		ResponseHash:        dereferenceString(row.ResponseHash),
		TransportCode:       TransportCode(row.TransportCode),
		FinishedAt:          cloneTime(row.FinishedAt),
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	cloned := value
	return &cloned
}

func dereferenceString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
