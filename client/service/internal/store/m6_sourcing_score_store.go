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

// SourcingScoringRevision 返回本批评分阶段实际使用的配置。首条评分预留
// 尚未出现时读取该 BackendJobID 最近成功同步的 legacy head；一旦已有任意
// 预留，整批余下成员都继续使用该预留已经记录的 revision。
func (s *Store) SourcingScoringRevision(batchID string) (*JobAIContextRevision, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var out *JobAIContextRevision
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := validateCompletedSourcingBatchForScoringTx(tx, batchID)
		if err != nil {
			return err
		}
		revision, err := sourcingStageRevisionTx(
			tx, batch, "sourcing_score_invocations", true,
		)
		if err != nil {
			return err
		}
		out = revision
		return nil
	})
	return out, err
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

// SourcingScoreWorkItem 是评分编排器的一个待驱动成员。Invocation 为 nil
// 表示尚无预留；非 nil 时必为未终局（inFlight）行，按 2026-07-28 并行重试
// 裁决允许续驱动。
type SourcingScoreWorkItem struct {
	Run        SourcingCandidateRun
	Invocation *SourcingScoreInvocation
}

// PendingSourcingScoreWork 按采集顺序返回批次内全部仍需驱动的成员：
// 尚无预留的与 inFlight 的。已终局成员不出现。
func (s *Store) PendingSourcingScoreWork(batchID string) ([]SourcingScoreWorkItem, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var items []SourcingScoreWorkItem
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := validateCompletedSourcingBatchForScoringTx(tx, batchID)
		if err != nil {
			return err
		}
		var runs []SourcingCandidateRun
		if err := tx.Where("batch_id = ? AND context_revision_hash = ?",
			batch.BatchID, batch.ContextRevisionHash).
			Order("captured_at ASC, run_id ASC").Find(&runs).Error; err != nil {
			return err
		}
		if len(runs) == 0 {
			return nil
		}
		runIDs := make([]string, 0, len(runs))
		for i := range runs {
			runIDs = append(runIDs, runs[i].RunID)
		}
		var invocations []SourcingScoreInvocation
		if err := tx.Where("run_id IN ?", runIDs).Find(&invocations).Error; err != nil {
			return err
		}
		byRun := make(map[string]SourcingScoreInvocation, len(invocations))
		for i := range invocations {
			byRun[invocations[i].RunID] = invocations[i]
		}
		for i := range runs {
			invocation, exists := byRun[runs[i].RunID]
			if !exists {
				items = append(items, SourcingScoreWorkItem{Run: runs[i]})
				continue
			}
			if invocation.FinishedAt != nil {
				continue
			}
			inFlight := invocation
			items = append(items, SourcingScoreWorkItem{Run: runs[i], Invocation: &inFlight})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// RecordSourcingScoreAttempt 在一次 provider HTTP 尝试发出前登记该尝试。
// 只允许作用于未终局预留；budgeted 表示本次尝试计入非 429 预算。返回
// 更新后的行，供调用方以 AttemptCount 派生 attempt 追踪身份。
func (s *Store) RecordSourcingScoreAttempt(invocationID string, budgeted bool) (*SourcingScoreInvocation, error) {
	invocationID = strings.TrimSpace(invocationID)
	if invocationID == "" {
		return nil, ErrAIInvocationInvalid
	}
	var out SourcingScoreInvocation
	err := s.db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{"attempt_count": gorm.Expr("attempt_count + 1")}
		if budgeted {
			updates["budgeted_attempt_count"] = gorm.Expr("budgeted_attempt_count + 1")
		}
		updated := tx.Model(&SourcingScoreInvocation{}).
			Where("invocation_id = ? AND finished_at IS NULL AND status = ?",
				invocationID, AIInvocationTransportFailed).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAIInvocationConflict
		}
		return tx.First(&out, "invocation_id = ?", invocationID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
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
			run.ContentHash != req.RunContentHash {
			return ErrSourcingBinding
		}
		revision, err := requireLegacyRevisionForSourcingBatchTx(
			tx, batch, req.ContextRevisionHash,
		)
		if err != nil || revision == nil {
			return ErrSourcingBinding
		}
		stageRevision, err := sourcingStageRevisionTx(
			tx, batch, "sourcing_score_invocations", false,
		)
		if err != nil {
			return err
		}
		if stageRevision == nil {
			current, currentErr := currentLegacyRevisionForSourcingBatchTx(tx, batch)
			if currentErr != nil {
				return currentErr
			}
			if current == nil || current.RevisionHash != req.ContextRevisionHash {
				return ErrSourcingBinding
			}
		} else if stageRevision.RevisionHash != req.ContextRevisionHash {
			return ErrAIInvocationConflict
		}
		// 同批不再冻结单一 provider/model:引擎运行期可换代(2026-08-12 甲方
		// 裁决),混模型批次照常预留推进,每行各自记下自己的调用事实。
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
			BatchID: batch.BatchID, TargetCount: batch.TargetCount,
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
			if row.RunContextRevisionHash != batch.ContextRevisionHash ||
				row.InvocationRunContentHash != row.RunContentHash {
				return ErrAIInvocationConflict
			}
			if _, err := requireLegacyRevisionForSourcingBatchTx(
				tx, batch, row.InvocationContextRevisionHash,
			); err != nil {
				return ErrAIInvocationConflict
			}
			if progress.ContextRevisionHash == "" {
				progress.ContextRevisionHash = row.InvocationContextRevisionHash
			} else if progress.ContextRevisionHash != row.InvocationContextRevisionHash {
				return ErrAIInvocationConflict
			}
			// 混模型批次合法(2026-08-12 甲方裁决):进度上的 Provider/Model 取
			// 首行,只作展示参考,不再要求全批一致;行级 provider/model 为空
			// 仍是不可能形态,照旧响亮冲突。
			if progress.Provider == "" {
				progress.Provider, progress.Model = row.Provider, row.Model
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
	backendJobID, err := sourcingBatchBackendJobID(batch)
	if err != nil || strings.TrimSpace(revision.SourceJobRef) != backendJobID {
		return SourcingBatch{}, ErrSourcingBatchConflict
	}
	return batch, nil
}

func sourcingBatchBackendJobID(batch SourcingBatch) (string, error) {
	if batch.BackendJobID == nil {
		return "", ErrSourcingBatchConflict
	}
	backendJobID := strings.TrimSpace(*batch.BackendJobID)
	if backendJobID == "" {
		return "", ErrSourcingBatchConflict
	}
	return backendJobID, nil
}

func requireLegacyRevisionForSourcingBatchTx(
	tx *gorm.DB,
	batch SourcingBatch,
	revisionHash string,
) (*JobAIContextRevision, error) {
	backendJobID, err := sourcingBatchBackendJobID(batch)
	if err != nil || strings.TrimSpace(revisionHash) == "" {
		return nil, ErrJobAIContextHeadInvalid
	}
	var revision JobAIContextRevision
	if err := tx.First(&revision, "revision_hash = ?", strings.TrimSpace(revisionHash)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJobAIContextRevisionNotFound
		}
		return nil, err
	}
	if revision.SourceKind != legacyJobConfigSourceKind ||
		strings.TrimSpace(revision.SourceJobRef) != backendJobID {
		return nil, ErrJobAIContextHeadInvalid
	}
	return &revision, nil
}

func currentLegacyRevisionForSourcingBatchTx(
	tx *gorm.DB,
	batch SourcingBatch,
) (*JobAIContextRevision, error) {
	backendJobID, err := sourcingBatchBackendJobID(batch)
	if err != nil {
		return nil, err
	}
	var head JobAIContextHead
	err = tx.First(
		&head,
		"source_kind = ? AND source_job_ref = ?",
		legacyJobConfigSourceKind,
		backendJobID,
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	revision, err := requireLegacyRevisionForSourcingBatchTx(tx, batch, head.RevisionHash)
	if err != nil {
		return nil, err
	}
	if revision.ContextID != head.ContextID {
		return nil, ErrJobAIContextHeadInvalid
	}
	return revision, nil
}

// sourcingStageRevisionTx 从一个 invocation 表推导已经开始的阶段 revision。
// allowCurrent=true 只供阶段入口在零预留时取 current head；Reserve 必须传
// false 并自行在同一事务内重核 current，避免 head 切换与首条预留竞态。
func sourcingStageRevisionTx(
	tx *gorm.DB,
	batch SourcingBatch,
	table string,
	allowCurrent bool,
) (*JobAIContextRevision, error) {
	if table != "sourcing_score_invocations" && table != "sourcing_greeting_invocations" {
		return nil, ErrJobAIContextHeadInvalid
	}
	var hashes []string
	query := tx.Table(table+" AS invocation").
		Distinct("invocation.context_revision_hash").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = invocation.run_id").
		Where("run.batch_id = ?", batch.BatchID)
	if table == "sourcing_greeting_invocations" {
		query = query.Where("invocation.batch_id = ?", batch.BatchID)
	}
	if err := query.Pluck("invocation.context_revision_hash", &hashes).Error; err != nil {
		return nil, err
	}
	if len(hashes) > 1 {
		return nil, ErrAIInvocationConflict
	}
	if len(hashes) == 1 {
		return requireLegacyRevisionForSourcingBatchTx(tx, batch, hashes[0])
	}
	if allowCurrent {
		revision, err := currentLegacyRevisionForSourcingBatchTx(tx, batch)
		if err != nil {
			return nil, err
		}
		if revision == nil {
			return nil, ErrJobAIContextRevisionNotFound
		}
		return revision, nil
	}
	return nil, nil
}

// provider/model 刻意不参与预留同一性:引擎运行期可换代,旧引擎预留的行由
// 新引擎按原身份接手,行上保留预留时刻的 provider/model 事实。
func sameSourcingScoreReservation(existing, wanted SourcingScoreInvocation) bool {
	return existing.RunID == wanted.RunID && existing.ContextRevisionHash == wanted.ContextRevisionHash &&
		existing.RunContentHash == wanted.RunContentHash && existing.InputHash == wanted.InputHash
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
			"failure_stage":         req.Completion.FailureStage,
			"error_detail_code":     req.Completion.ErrorDetailCode,
			"provider_http_status":  req.Completion.ProviderHTTPStatus,
			"request_bytes":         req.Completion.RequestBytes,
			"response_bytes":        req.Completion.ResponseBytes,
			"trace_status":          req.Completion.TraceStatus,
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
		existing.FailureStage == completion.FailureStage &&
		existing.ErrorDetailCode == completion.ErrorDetailCode &&
		sameOptionalInt(existing.ProviderHTTPStatus, completion.ProviderHTTPStatus) &&
		existing.RequestBytes == completion.RequestBytes && existing.ResponseBytes == completion.ResponseBytes &&
		existing.TraceStatus == completion.TraceStatus &&
		existing.EstimatedCostMicros == completion.EstimatedCostMicros && existing.FinishedAt != nil &&
		existing.FinishedAt.Equal(completion.FinishedAt)
}
