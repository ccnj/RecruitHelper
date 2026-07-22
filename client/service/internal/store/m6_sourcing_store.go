package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"recruithelper/contract/gen/go/protocol"

	"gorm.io/gorm"
)

const sourcingResumeSchemaV1 = 1

var (
	ErrSourcingNotEnabled = errors.New("账号采集尚未启用")
	ErrSourcingBinding    = errors.New("采集结果与当前账号配置不一致")
	ErrSourcingConflict   = errors.New("同一采集逻辑返回冲突内容")
)

type CompleteSourcingCandidateRunRequest struct {
	RunID               string
	Platform            string
	AccountRef          string
	ContextRevisionHash string
	LogicalDispatchID   string
	Data                protocol.CandidateReadSourcingResumeData
}

type SourcingCandidateRunSummary struct {
	RunID                   string
	SourceLogicalDispatchID string
	ObservedAt              int64
	CapturedAt              time.Time
	SchemaVersion           int
	ContentHash             string
	ResumeBytes             int
}

type AccountSourcingStatus struct {
	Platform            string
	AccountRef          string
	Enabled             bool
	ContextRevisionHash string
	StartedAt           *time.Time
	LastAttemptAt       *time.Time
	LastErrorCode       string
	CaptureCount        int64
	Latest              *SourcingCandidateRunSummary
}

// SourcingExcludedPlatformUserRefs 只供脑内构造下一次原语参数。返回值含平台
// 身份，不得穿过管理 API；契约当前把单次排除列表限制为 32 项。
func (s *Store) SourcingExcludedPlatformUserRefs(key AccountKey, revisionHash string, limit int) ([]string, error) {
	if key.Platform == "" || key.AccountRef == "" || revisionHash == "" || limit < 1 || limit > 32 {
		return nil, errors.New("采集排除列表参数无效")
	}
	var refs []string
	err := s.db.Model(&SourcingCandidateRun{}).
		Distinct("platform_user_ref").
		Where("platform = ? AND account_ref = ? AND context_revision_hash = ?", key.Platform, key.AccountRef, revisionHash).
		Order("captured_at DESC, run_id DESC").Limit(limit).Pluck("platform_user_ref", &refs).Error
	return refs, err
}

// MarkSourcingAttempt 只推进账号级、无候选人身份的运行元数据。它不会创建
// 虚假的采集事实；成功事实仍只能由 CompleteSourcingCandidateRun 收编。
func (s *Store) MarkSourcingAttempt(key AccountKey, revisionHash string, at time.Time, errorCode string) error {
	if revisionHash == "" || len(errorCode) > 128 {
		return errors.New("采集尝试元数据无效")
	}
	if at.IsZero() {
		at = time.Now()
	}
	return s.MutateAccount(key, func(account *Account) error {
		if !account.SourcingEnabled || account.SourcingContextRevisionHash != revisionHash {
			return ErrSourcingBinding
		}
		account.SourcingLastAttemptAt = &at
		account.SourcingLastErrorCode = errorCode
		return nil
	})
}

// CompleteSourcingCandidateRun 从持久命令谱系重新核对 generated result，
// 再把简历正文收编为不可变业务事实；调用方传入的 data 不能单独充当证据。
func (s *Store) CompleteSourcingCandidateRun(req CompleteSourcingCandidateRunRequest) (*SourcingCandidateRun, error) {
	if req.RunID == "" || req.Platform == "" || req.AccountRef == "" ||
		req.ContextRevisionHash == "" || req.LogicalDispatchID == "" {
		return nil, errors.New("采集事实缺少 run/account/context/logical 标识")
	}
	resumeRaw, err := json.Marshal(canonicalResumeV1{
		Basic: req.Data.Basic, Expectations: req.Data.Expectations,
		SelfEvaluation: req.Data.SelfEvaluation, Education: req.Data.Education,
		WorkExperiences: req.Data.WorkExperiences,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(resumeRaw)
	contentHash := hex.EncodeToString(digest[:])
	var out SourcingCandidateRun
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var account Account
		if err := tx.First(&account, "platform = ? AND account_ref = ?", req.Platform, req.AccountRef).Error; err != nil {
			return err
		}
		if !account.SourcingEnabled {
			return ErrSourcingNotEnabled
		}
		if account.SourcingContextRevisionHash != req.ContextRevisionHash || account.PrincipalFingerprint == nil {
			return ErrSourcingBinding
		}
		var revision JobAIContextRevision
		if err := tx.First(&revision, "revision_hash = ?", req.ContextRevisionHash).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrJobAIContextRevisionNotFound
			}
			return err
		}

		var records []CmdRecord
		if err := tx.Where("logical_dispatch_id = ?", req.LogicalDispatchID).
			Order("lineage_depth, created_at, msg_id").Find(&records).Error; err != nil {
			return err
		}
		leaf, err := validateLineage(records)
		if err != nil {
			return err
		}
		if leaf.Status != CmdOk || leaf.Name != protocol.PrimCandidateReadSourcingResume || leaf.TerminalAt == nil {
			return ErrSourcingBinding
		}
		if err := validateSourcingRoot(records[0], account, req.Data); err != nil {
			return err
		}
		resultRaw := json.RawMessage(leaf.ResultBody)
		meta := protocol.Primitives[protocol.PrimCandidateReadSourcingResume]
		var result protocol.ResultBody
		var persistedData protocol.CandidateReadSourcingResumeData
		if len(resultRaw) == 0 || protocol.ValidatePrimitiveResult(protocol.PrimCandidateReadSourcingResume, meta.Ver, resultRaw) != nil ||
			json.Unmarshal(resultRaw, &result) != nil || result.Ref != leaf.MsgID || result.Status != protocol.ResultStatusOk ||
			json.Unmarshal(result.Data, &persistedData) != nil {
			return ErrSourcingBinding
		}
		persistedRaw, _ := json.Marshal(persistedData)
		requestedRaw, _ := json.Marshal(req.Data)
		if string(persistedRaw) != string(requestedRaw) {
			return ErrSourcingConflict
		}

		var existing SourcingCandidateRun
		existingErr := tx.First(&existing, "source_logical_dispatch_id = ?", req.LogicalDispatchID).Error
		if existingErr == nil {
			if existing.Platform != req.Platform || existing.AccountRef != req.AccountRef ||
				existing.ContextRevisionHash != req.ContextRevisionHash || existing.ContentHash != contentHash ||
				existing.ResumeJSON != string(resumeRaw) || existing.PlatformUserRef != req.Data.PlatformUserRef ||
				existing.PositionRef != req.Data.PositionRef {
				return ErrSourcingConflict
			}
			out = existing
			return nil
		}
		if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}
		out = SourcingCandidateRun{
			RunID: req.RunID, Platform: req.Platform, AccountRef: req.AccountRef,
			ContextRevisionHash: req.ContextRevisionHash,
			PlatformUserRef:     req.Data.PlatformUserRef, DisplayName: req.Data.DisplayName,
			PositionRef: req.Data.PositionRef, PositionTitle: req.Data.PositionTitle,
			ContactState: string(req.Data.ContactState), SourceLogicalDispatchID: req.LogicalDispatchID,
			ObservedAt: req.Data.ObservedAt, CapturedAt: *leaf.TerminalAt,
			SchemaVersion: sourcingResumeSchemaV1, ContentHash: contentHash, ResumeJSON: string(resumeRaw),
		}
		if err := tx.Create(&out).Error; err != nil {
			return err
		}
		return tx.Model(&Account{}).
			Where("platform = ? AND account_ref = ? AND sourcing_enabled = ? AND sourcing_context_revision_hash = ?",
				req.Platform, req.AccountRef, true, req.ContextRevisionHash).
			Updates(map[string]any{
				"sourcing_last_attempt_at": leaf.TerminalAt,
				"sourcing_last_error_code": "",
			}).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func validateSourcingRoot(command CmdRecord, account Account, data protocol.CandidateReadSourcingResumeData) error {
	if command.Name != protocol.PrimCandidateReadSourcingResume || command.Class != string(protocol.ClassIntrusive) ||
		command.Platform != account.Platform || command.AccountRef != account.AccountRef ||
		command.Domain != account.Platform+":"+account.AccountRef {
		return ErrSourcingBinding
	}
	var args protocol.CandidateReadSourcingResumeArgs
	if json.Unmarshal([]byte(command.Args), &args) != nil {
		return ErrSourcingBinding
	}
	for _, excluded := range args.ExcludePlatformUserRefs {
		if data.PlatformUserRef == excluded {
			return ErrSourcingBinding
		}
	}
	var context protocol.CmdContext
	if json.Unmarshal([]byte(command.ContextJSON), &context) != nil || context.Platform != account.Platform ||
		context.AccountRef != account.AccountRef || context.ExpectedPrincipalFingerprint == "" ||
		context.ExpectedPrincipalFingerprint != *account.PrincipalFingerprint ||
		command.ExpectedPrincipalFingerprint != context.ExpectedPrincipalFingerprint {
		return ErrSourcingBinding
	}
	return nil
}

func (s *Store) AccountSourcingStatus(key AccountKey) (*AccountSourcingStatus, error) {
	account, err := s.AccountByKey(key)
	if err != nil || account == nil {
		return nil, err
	}
	status := &AccountSourcingStatus{
		Platform: account.Platform, AccountRef: account.AccountRef,
		Enabled: account.SourcingEnabled, ContextRevisionHash: account.SourcingContextRevisionHash,
		StartedAt: account.SourcingStartedAt, LastAttemptAt: account.SourcingLastAttemptAt,
		LastErrorCode: account.SourcingLastErrorCode,
	}
	if account.SourcingContextRevisionHash == "" {
		return status, nil
	}
	where := "platform = ? AND account_ref = ? AND context_revision_hash = ?"
	args := []any{key.Platform, key.AccountRef, account.SourcingContextRevisionHash}
	if err := s.db.Model(&SourcingCandidateRun{}).Where(where, args...).Count(&status.CaptureCount).Error; err != nil {
		return nil, err
	}
	var latest SourcingCandidateRun
	err = s.db.Where(where, args...).Order("captured_at DESC, run_id DESC").First(&latest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return status, nil
	}
	if err != nil {
		return nil, err
	}
	status.Latest = &SourcingCandidateRunSummary{
		RunID: latest.RunID, SourceLogicalDispatchID: latest.SourceLogicalDispatchID,
		ObservedAt: latest.ObservedAt, CapturedAt: latest.CapturedAt,
		SchemaVersion: latest.SchemaVersion, ContentHash: latest.ContentHash, ResumeBytes: len(latest.ResumeJSON),
	}
	return status, nil
}
