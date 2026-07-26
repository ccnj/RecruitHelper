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

// CompleteSourcingBatchCandidateRunRequest 是正式批次的成员收编入口。平台、
// 账号、配置 revision 与职位都从 BatchID 指向的不可变批次事实重新核对，
// 不能由调用方另行指定。
type CompleteSourcingBatchCandidateRunRequest struct {
	BatchID           string
	RunID             string
	LogicalDispatchID string
	Data              protocol.CandidateReadSourcingResumeData
}

type CompleteSourcingBatchCandidateRunResult struct {
	Run            SourcingCandidateRun
	Created        bool
	CapturedCount  int64
	BatchCompleted bool
}

type SourcingCandidateRunSummary struct {
	RunID                   string
	SourceLogicalDispatchID string
	ObservedAt              int64
	CapturedAt              time.Time
	SchemaVersion           int
	ContentHash             string
	ResumeBytes             int
	Score                   *SourcingScoreSummary
	Selection               *SourcingSelectionSummary
}

type SourcingScoreSummary struct {
	InvocationID        string
	Status              AIInvocationStatus
	Score               *int
	Provider            string
	Model               string
	InputTokens         int
	CachedInputTokens   int
	OutputTokens        int
	ErrorClass          string
	EstimatedCostMicros int64
	StartedAt           time.Time
	FinishedAt          *time.Time
}

type SourcingSelectionSummary struct {
	Outcome   SourcingSelectionOutcome
	Score     *int
	MinScore  int
	ProfileID *string
	DecidedAt time.Time
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
			if err := upsertSourcingCandidateTx(tx, req.Platform, req.Data, existing.CapturedAt); err != nil {
				return err
			}
			out = existing
			return nil
		}
		if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}
		if err := upsertSourcingCandidateTx(tx, req.Platform, req.Data, *leaf.TerminalAt); err != nil {
			return err
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

// CompleteSourcingBatchCandidateRun 从 candidate.readSourcingTargetResume 的
// 持久命令谱系收编一个正式批次成员。成员插入、计数达标、批次 completed
// 以及账号采集暂停在同一 SQLite 事务中完成；同 logical 或同批次同候选人
// 的再次到达只复用首次成员，不重复计数。
func (s *Store) CompleteSourcingBatchCandidateRun(
	req CompleteSourcingBatchCandidateRunRequest,
) (*CompleteSourcingBatchCandidateRunResult, error) {
	if req.BatchID == "" || req.RunID == "" || req.LogicalDispatchID == "" ||
		req.Data.PlatformUserRef == "" || req.Data.PositionRef == "" {
		return nil, ErrSourcingBatchInvalid
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

	out := CompleteSourcingBatchCandidateRunResult{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := sourcingBatchByIDTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		if batch.PositionRef == nil || *batch.PositionRef != req.Data.PositionRef {
			return ErrSourcingBinding
		}

		var account Account
		if err := tx.First(&account,
			"platform = ? AND account_ref = ?", batch.Platform, batch.AccountRef,
		).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		if account.PrincipalFingerprint == nil {
			return ErrSourcingBinding
		}
		var revision JobAIContextRevision
		if err := tx.First(&revision, "revision_hash = ?", batch.ContextRevisionHash).Error; err != nil {
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
		leaf, err := validateFormalSourcingTargetLineage(records, batch, account, req.Data)
		if err != nil {
			return err
		}
		if records[0].CreatedAt.Before(batch.StartedAt) || leaf.TerminalAt == nil || leaf.TerminalAt.Before(batch.StartedAt) {
			return ErrSourcingBinding
		}

		resultRaw := json.RawMessage(leaf.ResultBody)
		meta := protocol.Primitives[protocol.PrimCandidateReadSourcingTargetResume]
		var result protocol.ResultBody
		var persistedData protocol.CandidateReadSourcingResumeData
		if len(resultRaw) == 0 ||
			protocol.ValidatePrimitiveResult(protocol.PrimCandidateReadSourcingTargetResume, meta.Ver, resultRaw) != nil ||
			json.Unmarshal(resultRaw, &result) != nil || result.Ref != leaf.MsgID ||
			result.Status != protocol.ResultStatusOk || json.Unmarshal(result.Data, &persistedData) != nil {
			return ErrSourcingBinding
		}
		persistedRaw, _ := json.Marshal(persistedData)
		requestedRaw, _ := json.Marshal(req.Data)
		if string(persistedRaw) != string(requestedRaw) {
			return ErrSourcingConflict
		}

		var existingLogical SourcingCandidateRun
		existingErr := tx.First(&existingLogical,
			"source_logical_dispatch_id = ?", req.LogicalDispatchID,
		).Error
		if existingErr == nil {
			if !sameFormalSourcingMember(existingLogical, batch, req.Data) ||
				existingLogical.ContentHash != contentHash || existingLogical.ResumeJSON != string(resumeRaw) {
				return ErrSourcingBatchConflict
			}
			return fillCompletedSourcingBatchReplayTx(tx, batch, existingLogical, &out)
		}
		if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}

		var existingMember SourcingCandidateRun
		existingErr = tx.First(&existingMember,
			"batch_id = ? AND platform_user_ref = ?", batch.BatchID, req.Data.PlatformUserRef,
		).Error
		if existingErr == nil {
			if !sameFormalSourcingMember(existingMember, batch, req.Data) {
				return ErrSourcingBatchConflict
			}
			return fillCompletedSourcingBatchReplayTx(tx, batch, existingMember, &out)
		}
		if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}

		if batch.Status != SourcingBatchCollecting || batch.EndedAt != nil {
			return ErrSourcingBatchStateConflict
		}
		captured, err := sourcingBatchCapturedCountTx(tx, batch.BatchID)
		if err != nil {
			return err
		}
		if captured >= int64(batch.TargetCount) {
			return ErrSourcingBatchStateConflict
		}
		if err := upsertSourcingCandidateTx(tx, batch.Platform, req.Data, *leaf.TerminalAt); err != nil {
			return err
		}
		batchID := batch.BatchID
		run := SourcingCandidateRun{
			RunID: req.RunID, BatchID: &batchID,
			Platform: batch.Platform, AccountRef: batch.AccountRef,
			ContextRevisionHash: batch.ContextRevisionHash,
			PlatformUserRef:     req.Data.PlatformUserRef, DisplayName: req.Data.DisplayName,
			PositionRef: req.Data.PositionRef, PositionTitle: req.Data.PositionTitle,
			ContactState: string(req.Data.ContactState), SourceLogicalDispatchID: req.LogicalDispatchID,
			ObservedAt: req.Data.ObservedAt, CapturedAt: *leaf.TerminalAt,
			SchemaVersion: sourcingResumeSchemaV1, ContentHash: contentHash, ResumeJSON: string(resumeRaw),
		}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		captured++
		if captured > int64(batch.TargetCount) {
			return ErrSourcingBatchStateConflict
		}
		completed := captured == int64(batch.TargetCount)
		batchUpdates := map[string]any{"last_attempt_at": leaf.TerminalAt}
		if completed {
			batchUpdates["status"] = SourcingBatchCompleted
			batchUpdates["reason"] = ""
			batchUpdates["ended_at"] = leaf.TerminalAt
		}
		updatedBatch := tx.Model(&SourcingBatch{}).
			Where("batch_id = ? AND status = ? AND ended_at IS NULL", batch.BatchID, SourcingBatchCollecting).
			Updates(batchUpdates)
		if updatedBatch.Error != nil {
			return updatedBatch.Error
		}
		if updatedBatch.RowsAffected != 1 {
			return ErrSourcingBatchStateConflict
		}
		if completed {
			updatedAccount := tx.Model(&Account{}).
				Where("platform = ? AND account_ref = ?", batch.Platform, batch.AccountRef).
				Updates(map[string]any{
					"stopped_at":    leaf.TerminalAt,
					"paused_reason": SourcingTargetReachedPauseReason,
					"dirty_hint":    true,
				})
			if updatedAccount.Error != nil {
				return updatedAccount.Error
			}
			if updatedAccount.RowsAffected != 1 {
				return ErrSourcingBinding
			}
		}

		out.Run = run
		out.Created = true
		out.CapturedCount = captured
		out.BatchCompleted = completed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func validateFormalSourcingTargetLineage(
	records []CmdRecord,
	batch SourcingBatch,
	account Account,
	data protocol.CandidateReadSourcingResumeData,
) (*CmdRecord, error) {
	leaf, err := validateLineage(records)
	if err != nil {
		return nil, err
	}
	if leaf.Status != CmdOk || leaf.Name != protocol.PrimCandidateReadSourcingTargetResume || leaf.TerminalAt == nil {
		return nil, ErrSourcingBinding
	}
	for _, command := range records {
		if command.Name != protocol.PrimCandidateReadSourcingTargetResume ||
			command.Class != string(protocol.ClassIntrusive) || command.Platform != batch.Platform ||
			command.AccountRef != batch.AccountRef || command.Domain != batch.Platform+":"+batch.AccountRef {
			return nil, ErrSourcingBinding
		}
		var args protocol.CandidateReadSourcingTargetResumeArgs
		if json.Unmarshal([]byte(command.Args), &args) != nil ||
			args.PlatformUserRef != data.PlatformUserRef || args.PositionRef != data.PositionRef ||
			batch.PositionRef == nil || args.PositionRef != *batch.PositionRef {
			return nil, ErrSourcingBinding
		}
		if !sourcingBatchCommandMatchesAccount(command, batch, account) {
			return nil, ErrSourcingBinding
		}
	}
	return leaf, nil
}

func sameFormalSourcingMember(
	run SourcingCandidateRun,
	batch SourcingBatch,
	data protocol.CandidateReadSourcingResumeData,
) bool {
	return run.BatchID != nil && *run.BatchID == batch.BatchID &&
		run.Platform == batch.Platform && run.AccountRef == batch.AccountRef &&
		run.ContextRevisionHash == batch.ContextRevisionHash &&
		run.PlatformUserRef == data.PlatformUserRef && run.PositionRef == data.PositionRef
}

func sourcingBatchCapturedCountTx(tx *gorm.DB, batchID string) (int64, error) {
	var captured int64
	err := tx.Model(&SourcingCandidateRun{}).Where("batch_id = ?", batchID).Count(&captured).Error
	return captured, err
}

func fillCompletedSourcingBatchReplayTx(
	tx *gorm.DB,
	batch SourcingBatch,
	run SourcingCandidateRun,
	out *CompleteSourcingBatchCandidateRunResult,
) error {
	captured, err := sourcingBatchCapturedCountTx(tx, batch.BatchID)
	if err != nil {
		return err
	}
	out.Run = run
	out.Created = false
	out.CapturedCount = captured
	out.BatchCompleted = batch.Status == SourcingBatchCompleted && batch.EndedAt != nil
	return nil
}

// upsertSourcingCandidateTx 只建立 platform+platformUserRef 人根并刷新展示
// 快照，不创建人×职位档案，也不代表已经评分或入选。
func upsertSourcingCandidateTx(
	tx *gorm.DB,
	platform string,
	data protocol.CandidateReadSourcingResumeData,
	capturedAt time.Time,
) error {
	observedAt := time.UnixMilli(data.ObservedAt)
	if data.ObservedAt <= 0 {
		observedAt = capturedAt
	}
	_, _, err := upsertCandidateSnapshotTx(tx, SelectCandidateProfileRequest{
		Scope: CandidateProfileScope{
			Platform: platform, PlatformUserRef: data.PlatformUserRef,
		},
		DisplayName: data.DisplayName,
		ObservedAt:  observedAt,
	})
	return err
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
	score, err := s.SourcingScoreByRunID(latest.RunID)
	if err != nil {
		return nil, err
	}
	if score != nil {
		status.Latest.Score = &SourcingScoreSummary{
			InvocationID: score.InvocationID, Status: score.Status, Score: score.Score,
			Provider: score.Provider, Model: score.Model,
			InputTokens: score.InputTokens, CachedInputTokens: score.CachedInputTokens,
			OutputTokens: score.OutputTokens, ErrorClass: score.ErrorClass,
			EstimatedCostMicros: score.EstimatedCostMicros,
			StartedAt:           score.StartedAt, FinishedAt: score.FinishedAt,
		}
	}
	selection, err := s.SourcingSelectionByRunID(latest.RunID)
	if err != nil {
		return nil, err
	}
	if selection != nil {
		status.Latest.Selection = &SourcingSelectionSummary{
			Outcome: selection.Outcome, Score: selection.Score, MinScore: selection.MinScore,
			ProfileID: selection.ProfileID, DecidedAt: selection.DecidedAt,
		}
	}
	return status, nil
}
