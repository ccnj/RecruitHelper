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

// NextSourcingRunWithoutScore 返回指定账号/revision 尚无任何评分 invocation
// 的最早采集事实。失败、完成或 inFlight invocation 都会占住 RunID，绝不
// 因调用失败而从这里获得第二次 provider 授权。
func (s *Store) NextSourcingRunWithoutScore(key AccountKey, revisionHash string) (*SourcingCandidateRun, error) {
	if key.Platform == "" || key.AccountRef == "" || strings.TrimSpace(revisionHash) == "" {
		return nil, ErrAIInvocationInvalid
	}
	var run SourcingCandidateRun
	err := s.db.Table("sourcing_candidate_runs AS run").
		Select("run.*").
		Joins("LEFT JOIN sourcing_score_invocations AS invocation ON invocation.run_id = run.run_id").
		Where("run.platform = ? AND run.account_ref = ? AND run.context_revision_hash = ? AND invocation.invocation_id IS NULL",
			key.Platform, key.AccountRef, revisionHash).
		Order("run.captured_at ASC, run.run_id ASC").
		First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

type ReserveSourcingScoreRequest struct {
	InvocationID        string
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
	if strings.TrimSpace(req.InvocationID) == "" || strings.TrimSpace(req.RunID) == "" ||
		strings.TrimSpace(req.ContextRevisionHash) == "" || strings.TrimSpace(req.RunContentHash) == "" ||
		strings.TrimSpace(req.Provider) == "" || strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.InputHash) == "" {
		return nil, ErrAIInvocationInvalid
	}
	if req.StartedAt.IsZero() {
		req.StartedAt = time.Now()
	}
	out := &ReserveSourcingScoreResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var run SourcingCandidateRun
		if err := tx.First(&run, "run_id = ?", req.RunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourcingBinding
			}
			return err
		}
		if run.ContextRevisionHash != req.ContextRevisionHash || run.ContentHash != req.RunContentHash {
			return ErrSourcingBinding
		}
		wanted := SourcingScoreInvocation{
			InvocationID: req.InvocationID, RunID: req.RunID,
			ContextRevisionHash: req.ContextRevisionHash, RunContentHash: req.RunContentHash,
			Provider: req.Provider, Model: req.Model, InputHash: req.InputHash,
			Status: AIInvocationTransportFailed, StartedAt: req.StartedAt,
		}
		var existing SourcingScoreInvocation
		err := tx.First(&existing, "invocation_id = ?", req.InvocationID).Error
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
