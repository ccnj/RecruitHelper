package store

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

func (s *Store) SourcingScoreByRunID(runID string) (*SourcingScoreInvocation, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, ErrAIInvocationInvalid
	}
	var invocation SourcingScoreInvocation
	err := s.db.First(&invocation, "run_id = ?", runID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &invocation, nil
}

// SourcingBatchScoringProgress 是正式批次评分的脱敏聚合。它只暴露批次
// 随机引用、冻结配置 hash、计数和已经冻结的 provider/model，不包含成员
// 引用、简历正文或逐条 invocation 引用。
type SourcingBatchScoringProgress struct {
	BatchID             string
	ContextRevisionHash string
	TargetCount         int
	OKCount             int64
	FailedCount         int64
	InFlightCount       int64
	PendingCount        int64
	Provider            string
	Model               string
	Completed           bool
}

// NextSourcingBatchRunWithoutScore 只消费一个已经完整结束的正式采集批次。
// 同账号、同 revision 的其他批次以及 BatchID=NULL 的历史行不会进入查询。
func (s *Store) NextSourcingBatchRunWithoutScore(batchID string) (*SourcingCandidateRun, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var run *SourcingCandidateRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := validateCompletedSourcingBatchForScoringTx(tx, batchID)
		if err != nil {
			return err
		}
		var candidate SourcingCandidateRun
		err = tx.Table("sourcing_candidate_runs AS run").
			Select("run.*").
			Joins("LEFT JOIN sourcing_score_invocations AS invocation ON invocation.run_id = run.run_id").
			Where("run.batch_id = ? AND run.context_revision_hash = ? AND invocation.invocation_id IS NULL",
				batch.BatchID, batch.ContextRevisionHash).
			Order("run.captured_at ASC, run.run_id ASC").
			First(&candidate).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		run = &candidate
		return nil
	})
	return run, err
}

type ReserveSourcingScoreRequest struct {
	InvocationID        string
	BatchID             string
	RunID               string
	ContextRevisionHash string
	RunContentHash      string
	Provider            string
	Model               string
	InputHash           string
	StartedAt           time.Time
}

type ReserveSourcingScoreResult struct {
	Invocation SourcingScoreInvocation
	Created    bool
}

// ReserveSourcingScore 是评分 provider 调用的唯一持久授权点。既有 RunID
// 无论处于何种状态都只返回 Created=false，不产生第二条预留。
func (s *Store) ReserveSourcingScore(req ReserveSourcingScoreRequest) (*ReserveSourcingScoreResult, error) {
	if strings.TrimSpace(req.InvocationID) == "" || strings.TrimSpace(req.BatchID) == "" || strings.TrimSpace(req.RunID) == "" ||
		strings.TrimSpace(req.ContextRevisionHash) == "" || strings.TrimSpace(req.RunContentHash) == "" ||
		strings.TrimSpace(req.Provider) == "" || strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.InputHash) == "" {
		return nil, ErrAIInvocationInvalid
	}
	if req.StartedAt.IsZero() {
		req.StartedAt = time.Now()
	}
	out := &ReserveSourcingScoreResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := validateCompletedSourcingBatchForScoringTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		var run SourcingCandidateRun
		if err := tx.First(&run, "run_id = ?", req.RunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourcingBinding
			}
			return err
		}
		if run.BatchID == nil || *run.BatchID != batch.BatchID ||
			run.Platform != batch.Platform || run.AccountRef != batch.AccountRef ||
			run.ContextRevisionHash != batch.ContextRevisionHash ||
			req.ContextRevisionHash != batch.ContextRevisionHash || run.ContentHash != req.RunContentHash {
			return ErrSourcingBinding
		}
		type providerModel struct {
			Provider string
			Model    string
		}
		var frozen []providerModel
		err = tx.Table("sourcing_score_invocations AS invocation").
			Select("DISTINCT invocation.provider AS provider, invocation.model AS model").
			Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = invocation.run_id").
			Where("run.batch_id = ?", batch.BatchID).
			Scan(&frozen).Error
		if err != nil {
			return err
		}
		if len(frozen) > 1 || (len(frozen) == 1 &&
			(frozen[0].Provider != req.Provider || frozen[0].Model != req.Model)) {
			return ErrAIInvocationConflict
		}
		wanted := SourcingScoreInvocation{
			InvocationID: req.InvocationID, RunID: req.RunID,
			ContextRevisionHash: req.ContextRevisionHash, RunContentHash: req.RunContentHash,
			Provider: req.Provider, Model: req.Model, InputHash: req.InputHash,
			Status: AIInvocationTransportFailed, StartedAt: req.StartedAt,
		}
		var existing SourcingScoreInvocation
		err = tx.First(&existing, "invocation_id = ?", req.InvocationID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = tx.First(&existing, "run_id = ?", req.RunID).Error
		}
		if err == nil {
			if !sameSourcingScoreReservation(existing, wanted) {
				return ErrAIInvocationConflict
			}
			out.Invocation = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(&wanted).Error; err != nil {
			return err
		}
		out.Invocation = wanted
		out.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SourcingBatchScoringProgress 按目标成员聚合评分状态。仅有预留而没有
// FinishedAt 只能计作 inFlight；成功与失败之和覆盖全部目标成员时才完成。
// 若持久层出现跨 provider/model 混用或不可能的 invocation 形态则响亮冲突。
func (s *Store) SourcingBatchScoringProgress(batchID string) (*SourcingBatchScoringProgress, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var progress SourcingBatchScoringProgress
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := validateCompletedSourcingBatchForScoringTx(tx, batchID)
		if err != nil {
			return err
		}
		progress = SourcingBatchScoringProgress{
			BatchID: batch.BatchID, ContextRevisionHash: batch.ContextRevisionHash,
			TargetCount: batch.TargetCount,
		}
		type scoringRow struct {
			RunContextRevisionHash        string
			RunContentHash                string
			InvocationID                  string
			InvocationContextRevisionHash string
			InvocationRunContentHash      string
			Provider                      string
			Model                         string
			Status                        AIInvocationStatus
			Score                         *int
			FinishedAt                    *time.Time
		}
		var rows []scoringRow
		if err := tx.Table("sourcing_candidate_runs AS run").
			Select(`run.context_revision_hash AS run_context_revision_hash,
				run.content_hash AS run_content_hash,
				invocation.invocation_id,
				invocation.context_revision_hash AS invocation_context_revision_hash,
				invocation.run_content_hash AS invocation_run_content_hash,
				invocation.provider, invocation.model, invocation.status,
				invocation.score, invocation.finished_at`).
			Joins("LEFT JOIN sourcing_score_invocations AS invocation ON invocation.run_id = run.run_id").
			Where("run.batch_id = ?", batch.BatchID).
			Order("run.captured_at ASC, run.run_id ASC").
			Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) != batch.TargetCount {
			return ErrSourcingBatchConflict
		}
		for _, row := range rows {
			if row.InvocationID == "" {
				progress.PendingCount++
				continue
			}
			if row.InvocationContextRevisionHash != batch.ContextRevisionHash ||
				row.InvocationContextRevisionHash != row.RunContextRevisionHash ||
				row.InvocationRunContentHash != row.RunContentHash {
				return ErrAIInvocationConflict
			}
			if progress.Provider == "" {
				progress.Provider, progress.Model = row.Provider, row.Model
			} else if progress.Provider != row.Provider || progress.Model != row.Model {
				return ErrAIInvocationConflict
			}
			if strings.TrimSpace(row.Provider) == "" || strings.TrimSpace(row.Model) == "" {
				return ErrAIInvocationConflict
			}
			if row.FinishedAt == nil {
				if row.Status != AIInvocationTransportFailed || row.Score != nil {
					return ErrAIInvocationConflict
				}
				progress.InFlightCount++
				continue
			}
			if row.Status == AIInvocationOK {
				if row.Score == nil || *row.Score < 1 || *row.Score > 10 {
					return ErrAIInvocationConflict
				}
				progress.OKCount++
				continue
			}
			if row.Score != nil {
				return ErrAIInvocationConflict
			}
			progress.FailedCount++
		}
		total := progress.OKCount + progress.FailedCount + progress.InFlightCount + progress.PendingCount
		if total != int64(progress.TargetCount) {
			return ErrSourcingBatchConflict
		}
		progress.Completed = progress.OKCount+progress.FailedCount == int64(progress.TargetCount)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &progress, nil
}

func validateCompletedSourcingBatchForScoringTx(tx *gorm.DB, batchID string) (SourcingBatch, error) {
	batch, err := sourcingBatchByIDTx(tx, strings.TrimSpace(batchID))
	if err != nil {
		return SourcingBatch{}, err
	}
	if batch.Status != SourcingBatchCompleted || batch.EndedAt == nil {
		return SourcingBatch{}, ErrSourcingBatchStateConflict
	}
	var total int64
	if err := tx.Model(&SourcingCandidateRun{}).Where("batch_id = ?", batch.BatchID).Count(&total).Error; err != nil {
		return SourcingBatch{}, err
	}
	if total != int64(batch.TargetCount) {
		return SourcingBatch{}, ErrSourcingBatchConflict
	}
	var matching int64
	if err := tx.Model(&SourcingCandidateRun{}).
		Where("batch_id = ? AND platform = ? AND account_ref = ? AND context_revision_hash = ?",
			batch.BatchID, batch.Platform, batch.AccountRef, batch.ContextRevisionHash).
		Count(&matching).Error; err != nil {
		return SourcingBatch{}, err
	}
	if matching != total {
		return SourcingBatch{}, ErrSourcingBatchConflict
	}
	var revision JobAIContextRevision
	if err := tx.First(&revision, "revision_hash = ?", batch.ContextRevisionHash).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SourcingBatch{}, ErrJobAIContextRevisionNotFound
		}
		return SourcingBatch{}, err
	}
	return batch, nil
}

func sameSourcingScoreReservation(existing, wanted SourcingScoreInvocation) bool {
	return existing.RunID == wanted.RunID && existing.ContextRevisionHash == wanted.ContextRevisionHash &&
		existing.RunContentHash == wanted.RunContentHash && existing.Provider == wanted.Provider &&
		existing.Model == wanted.Model && existing.InputHash == wanted.InputHash
}

type CompleteSourcingScoreRequest struct {
	Completion AIInvocationCompletion
	Score      *int
}

// CompleteSourcingScore 只消费尚未完成的预留。成功必须同时满足 1..10 分与
// 非思考用量闸；失败不允许携带 score。相同完成可幂等收编，任一差异都冲突。
func (s *Store) CompleteSourcingScore(req CompleteSourcingScoreRequest) (*SourcingScoreInvocation, error) {
	if err := validateInvocationCompletion(req.Completion); err != nil {
		return nil, err
	}
	if req.Completion.Status == AIInvocationOK {
		if req.Score == nil || *req.Score < 1 || *req.Score > 10 || !reasoningCompletionSafe(req.Completion) {
			return nil, ErrAIInvocationInvalid
		}
	} else if req.Score != nil {
		return nil, ErrAIInvocationInvalid
	}
	var out SourcingScoreInvocation
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&out, "invocation_id = ?", req.Completion.InvocationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAIInvocationNotFound
			}
			return err
		}
		if out.FinishedAt != nil {
			if sameSourcingScoreCompletion(out, req) {
				return nil
			}
			return ErrAIInvocationConflict
		}
		if out.Status != AIInvocationTransportFailed {
			return ErrAIInvocationConflict
		}
		updates := map[string]any{
			"status": req.Completion.Status, "score": req.Score,
			"output_hash":           req.Completion.OutputHash,
			"input_tokens":          req.Completion.InputTokens,
			"cached_input_tokens":   req.Completion.CachedInputTokens,
			"output_tokens":         req.Completion.OutputTokens,
			"reasoning_tokens":      req.Completion.ReasoningTokens,
			"usage_shape":           req.Completion.UsageShape,
			"latency_ms":            req.Completion.LatencyMs,
			"error_class":           req.Completion.ErrorClass,
			"estimated_cost_micros": req.Completion.EstimatedCostMicros,
			"finished_at":           req.Completion.FinishedAt,
		}
		updated := tx.Model(&SourcingScoreInvocation{}).
			Where("invocation_id = ? AND finished_at IS NULL AND status = ?",
				req.Completion.InvocationID, AIInvocationTransportFailed).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAIInvocationConflict
		}
		return tx.First(&out, "invocation_id = ?", req.Completion.InvocationID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func sameSourcingScoreCompletion(existing SourcingScoreInvocation, req CompleteSourcingScoreRequest) bool {
	completion := req.Completion
	return existing.Status == completion.Status && sameOptionalInt(existing.Score, req.Score) &&
		existing.OutputHash == completion.OutputHash && existing.InputTokens == completion.InputTokens &&
		existing.CachedInputTokens == completion.CachedInputTokens && existing.OutputTokens == completion.OutputTokens &&
		sameOptionalInt(existing.ReasoningTokens, completion.ReasoningTokens) && existing.UsageShape == completion.UsageShape &&
		existing.LatencyMs == completion.LatencyMs && existing.ErrorClass == completion.ErrorClass &&
		existing.EstimatedCostMicros == completion.EstimatedCostMicros && existing.FinishedAt != nil &&
		existing.FinishedAt.Equal(completion.FinishedAt)
}
