package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
)

const maxSourcingGreetingTextBytes = 2048

// SourcingGreetingMaterial 是批量招呼语生成唯一允许读取的候选材料。它只
// 从完整筛选批次的 selected decision 投影，调用方无需也不得自行拼接档案。
type SourcingGreetingMaterial struct {
	BatchID             string
	ContextRevisionHash string
	RunID               string
	RunContentHash      string
	ProfileID           string
	ResumeJSON          string
	CapturedAt          time.Time
}

type ReserveSourcingGreetingRequest struct {
	InvocationID        string
	BatchID             string
	RunID               string
	ProfileID           string
	ContextRevisionHash string
	RunContentHash      string
	Provider            string
	Model               string
	InputHash           string
	StartedAt           time.Time
}

type ReserveSourcingGreetingResult struct {
	Invocation SourcingGreetingInvocation
	Created    bool
}

type CompleteSourcingGreetingRequest struct {
	Completion   AIInvocationCompletion
	GreetingText string
	ContentHash  string
}

// SourcingBatchGreetingProgress 是招呼语生成的脱敏批次聚合。正文、成员、
// run/profile/invocation 引用都不会进入这个投影。
type SourcingBatchGreetingProgress struct {
	BatchID             string
	ContextRevisionHash string
	SelectedCount       int
	OKCount             int64
	FailedCount         int64
	InFlightCount       int64
	PendingCount        int64
	Provider            string
	Model               string
	InputTokens         int64
	CachedInputTokens   int64
	OutputTokens        int64
	EstimatedCostMicros int64
	Completed           bool
}

func (s *Store) SourcingGreetingByProfileID(profileID string) (*SourcingGreetingInvocation, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, ErrAIInvocationInvalid
	}
	var invocation SourcingGreetingInvocation
	err := s.db.First(&invocation, "profile_id = ?", profileID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &invocation, nil
}

// NextSelectedSourcingGreetingMaterial 返回批次中尚无任何调用预留的最早
// selected 成员。批次筛选、逐人 decision、profile 与 run 任一绑定不完整
// 都响亮失败，不会退化为“看起来 selected”的宽松查询。
func (s *Store) NextSelectedSourcingGreetingMaterial(batchID string) (*SourcingGreetingMaterial, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var next *SourcingGreetingMaterial
	err := s.db.Transaction(func(tx *gorm.DB) error {
		_, materials, invocations, err := loadSourcingGreetingScopeTx(tx, batchID)
		if err != nil {
			return err
		}
		reservedByRun := make(map[string]struct{}, len(invocations))
		for _, invocation := range invocations {
			reservedByRun[invocation.RunID] = struct{}{}
		}
		for i := range materials {
			if _, exists := reservedByRun[materials[i].RunID]; exists {
				continue
			}
			material := materials[i]
			next = &material
			break
		}
		return nil
	})
	return next, err
}

// ReserveSourcingGreeting 是 provider 调用的唯一持久授权点。RunID 与
// ProfileID 任一已存在预留都只能重放原事实，不授权第二次网络调用。
func (s *Store) ReserveSourcingGreeting(req ReserveSourcingGreetingRequest) (*ReserveSourcingGreetingResult, error) {
	if strings.TrimSpace(req.InvocationID) == "" || strings.TrimSpace(req.BatchID) == "" ||
		strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.ProfileID) == "" ||
		strings.TrimSpace(req.ContextRevisionHash) == "" || strings.TrimSpace(req.RunContentHash) == "" ||
		strings.TrimSpace(req.Provider) == "" || strings.TrimSpace(req.Model) == "" ||
		strings.TrimSpace(req.InputHash) == "" {
		return nil, ErrAIInvocationInvalid
	}
	if req.StartedAt.IsZero() {
		req.StartedAt = time.Now()
	}
	out := &ReserveSourcingGreetingResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		_, materials, invocations, err := loadSourcingGreetingScopeTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		var material *SourcingGreetingMaterial
		for i := range materials {
			if materials[i].RunID == req.RunID && materials[i].ProfileID == req.ProfileID {
				candidate := materials[i]
				material = &candidate
				break
			}
		}
		if material == nil || material.ContextRevisionHash != req.ContextRevisionHash ||
			material.RunContentHash != req.RunContentHash {
			return ErrSourcingBinding
		}
		for _, invocation := range invocations {
			if invocation.Provider != req.Provider || invocation.Model != req.Model {
				return ErrAIInvocationConflict
			}
		}
		wanted := SourcingGreetingInvocation{
			InvocationID: req.InvocationID, BatchID: req.BatchID, RunID: req.RunID,
			ProfileID: req.ProfileID, ContextRevisionHash: req.ContextRevisionHash,
			RunContentHash: req.RunContentHash, Provider: req.Provider, Model: req.Model,
			InputHash: req.InputHash, Status: AIInvocationTransportFailed, StartedAt: req.StartedAt,
		}
		var existing SourcingGreetingInvocation
		err = tx.First(&existing, "invocation_id = ?", req.InvocationID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = tx.First(&existing, "run_id = ? OR profile_id = ?", req.RunID, req.ProfileID).Error
		}
		if err == nil {
			if !sameSourcingGreetingReservation(existing, wanted) {
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

// CompleteSourcingGreeting 只完成既有 inFlight 预留。成功正文必须已经
// 完成 trim+NFC 规范化并满足发送边界；失败终局禁止携带正文或正文 hash。
func (s *Store) CompleteSourcingGreeting(req CompleteSourcingGreetingRequest) (*SourcingGreetingInvocation, error) {
	if err := validateInvocationCompletion(req.Completion); err != nil {
		return nil, err
	}
	if req.Completion.Status == AIInvocationOK {
		if !validPersistedGreetingText(req.GreetingText) || strings.TrimSpace(req.ContentHash) == "" ||
			req.ContentHash != sourcingGreetingContentHash(req.GreetingText) ||
			!reasoningCompletionSafe(req.Completion) {
			return nil, ErrAIInvocationInvalid
		}
	} else if req.GreetingText != "" || req.ContentHash != "" {
		return nil, ErrAIInvocationInvalid
	}
	var out SourcingGreetingInvocation
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&out, "invocation_id = ?", req.Completion.InvocationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAIInvocationNotFound
			}
			return err
		}
		if out.FinishedAt != nil {
			if sameSourcingGreetingCompletion(out, req) {
				return nil
			}
			return ErrAIInvocationConflict
		}
		if out.Status != AIInvocationTransportFailed || out.GreetingText != "" || out.ContentHash != "" {
			return ErrAIInvocationConflict
		}
		updates := map[string]any{
			"status": req.Completion.Status, "greeting_text": req.GreetingText,
			"content_hash": req.ContentHash, "output_hash": req.Completion.OutputHash,
			"input_tokens": req.Completion.InputTokens, "cached_input_tokens": req.Completion.CachedInputTokens,
			"output_tokens": req.Completion.OutputTokens, "reasoning_tokens": req.Completion.ReasoningTokens,
			"usage_shape": req.Completion.UsageShape, "latency_ms": req.Completion.LatencyMs,
			"error_class": req.Completion.ErrorClass, "estimated_cost_micros": req.Completion.EstimatedCostMicros,
			"finished_at": req.Completion.FinishedAt,
		}
		updated := tx.Model(&SourcingGreetingInvocation{}).
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

func (s *Store) SourcingBatchGreetingProgress(batchID string) (*SourcingBatchGreetingProgress, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var progress SourcingBatchGreetingProgress
	err := s.db.Transaction(func(tx *gorm.DB) error {
		selection, materials, invocations, err := loadSourcingGreetingScopeTx(tx, batchID)
		if err != nil {
			return err
		}
		progress = SourcingBatchGreetingProgress{
			BatchID: selection.BatchID, ContextRevisionHash: selection.ContextRevisionHash,
			SelectedCount: selection.SelectedCount,
		}
		byRun := make(map[string]SourcingGreetingInvocation, len(invocations))
		for _, invocation := range invocations {
			byRun[invocation.RunID] = invocation
		}
		for _, material := range materials {
			invocation, exists := byRun[material.RunID]
			if !exists {
				progress.PendingCount++
				continue
			}
			if progress.Provider == "" {
				progress.Provider, progress.Model = invocation.Provider, invocation.Model
			} else if progress.Provider != invocation.Provider || progress.Model != invocation.Model {
				return ErrAIInvocationConflict
			}
			progress.InputTokens += int64(invocation.InputTokens)
			progress.CachedInputTokens += int64(invocation.CachedInputTokens)
			progress.OutputTokens += int64(invocation.OutputTokens)
			progress.EstimatedCostMicros += invocation.EstimatedCostMicros
			if invocation.FinishedAt == nil {
				if invocation.Status != AIInvocationTransportFailed || invocation.GreetingText != "" || invocation.ContentHash != "" {
					return ErrAIInvocationConflict
				}
				progress.InFlightCount++
				continue
			}
			if invocation.Status == AIInvocationOK {
				if !validPersistedGreetingText(invocation.GreetingText) || strings.TrimSpace(invocation.ContentHash) == "" {
					return ErrAIInvocationConflict
				}
				progress.OKCount++
				continue
			}
			if invocation.GreetingText != "" || invocation.ContentHash != "" {
				return ErrAIInvocationConflict
			}
			progress.FailedCount++
		}
		total := progress.OKCount + progress.FailedCount + progress.InFlightCount + progress.PendingCount
		if total != int64(progress.SelectedCount) {
			return ErrSourcingSelectionConflict
		}
		progress.Completed = progress.OKCount+progress.FailedCount == int64(progress.SelectedCount)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &progress, nil
}

// loadSourcingGreetingScopeTx 先重建并验证完整 selected 范围，再读取与该
// 范围相交的 invocation。它是 next/reserve/progress 字面同一份事实门。
func loadSourcingGreetingScopeTx(
	tx *gorm.DB,
	batchID string,
) (SourcingBatchSelection, []SourcingGreetingMaterial, []SourcingGreetingInvocation, error) {
	batch, err := validateCompletedSourcingBatchForScoringTx(tx, strings.TrimSpace(batchID))
	if err != nil {
		return SourcingBatchSelection{}, nil, nil, err
	}
	if batch.PositionRef == nil || strings.TrimSpace(*batch.PositionRef) == "" {
		return SourcingBatchSelection{}, nil, nil, ErrSourcingBinding
	}
	var selection SourcingBatchSelection
	if err := tx.First(&selection, "batch_id = ?", batch.BatchID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionNotReady
		}
		return SourcingBatchSelection{}, nil, nil, err
	}
	if err := validatePersistedSourcingBatchSelectionTx(tx, batch, selection); err != nil {
		return SourcingBatchSelection{}, nil, nil, err
	}
	var runs []SourcingCandidateRun
	if err := tx.Where("batch_id = ?", batch.BatchID).
		Order("captured_at ASC, run_id ASC").Find(&runs).Error; err != nil {
		return SourcingBatchSelection{}, nil, nil, err
	}
	if len(runs) != batch.TargetCount {
		return SourcingBatchSelection{}, nil, nil, ErrSourcingBatchConflict
	}
	materials := make([]SourcingGreetingMaterial, 0, selection.SelectedCount)
	seenProfiles := make(map[string]struct{}, selection.SelectedCount)
	for i := range runs {
		run := runs[i]
		if run.BatchID == nil || *run.BatchID != batch.BatchID || run.Platform != batch.Platform ||
			run.AccountRef != batch.AccountRef || run.ContextRevisionHash != batch.ContextRevisionHash ||
			run.PositionRef != *batch.PositionRef || strings.TrimSpace(run.ContentHash) == "" {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingBinding
		}
		var decision SourcingSelectionDecision
		if err := tx.First(&decision, "run_id = ?", run.RunID).Error; err != nil {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
		}
		if decision.ContextRevisionHash != batch.ContextRevisionHash {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
		}
		if decision.Outcome != SourcingSelectionSelected {
			if decision.ProfileID != nil {
				return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
			}
			continue
		}
		if decision.ProfileID == nil || strings.TrimSpace(*decision.ProfileID) == "" {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
		}
		if _, duplicate := seenProfiles[*decision.ProfileID]; duplicate {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
		}
		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", *decision.ProfileID).Error; err != nil {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
		}
		if profile.Platform != run.Platform || profile.AccountRef != run.AccountRef ||
			profile.PlatformUserRef != run.PlatformUserRef || profile.PositionRef != run.PositionRef ||
			profile.MainStatus != CandidateProfileSelected || profile.EndReason != nil {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingBinding
		}
		seenProfiles[profile.ProfileID] = struct{}{}
		materials = append(materials, SourcingGreetingMaterial{
			BatchID: batch.BatchID, ContextRevisionHash: batch.ContextRevisionHash,
			RunID: run.RunID, RunContentHash: run.ContentHash, ProfileID: profile.ProfileID,
			ResumeJSON: run.ResumeJSON, CapturedAt: run.CapturedAt,
		})
	}
	if len(materials) != selection.SelectedCount {
		return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
	}
	if len(materials) == 0 {
		return selection, materials, nil, nil
	}
	runIDs := make([]string, 0, len(materials))
	profileIDs := make([]string, 0, len(materials))
	materialByRun := make(map[string]SourcingGreetingMaterial, len(materials))
	for _, material := range materials {
		runIDs = append(runIDs, material.RunID)
		profileIDs = append(profileIDs, material.ProfileID)
		materialByRun[material.RunID] = material
	}
	var invocations []SourcingGreetingInvocation
	if err := tx.Where("batch_id = ? OR run_id IN ? OR profile_id IN ?", batch.BatchID, runIDs, profileIDs).
		Order("started_at, invocation_id").Find(&invocations).Error; err != nil {
		return SourcingBatchSelection{}, nil, nil, err
	}
	seenRuns := make(map[string]struct{}, len(invocations))
	seenInvocationProfiles := make(map[string]struct{}, len(invocations))
	provider, model := "", ""
	for _, invocation := range invocations {
		material, exists := materialByRun[invocation.RunID]
		if !exists || invocation.BatchID != batch.BatchID || invocation.ProfileID != material.ProfileID ||
			invocation.ContextRevisionHash != material.ContextRevisionHash ||
			invocation.RunContentHash != material.RunContentHash || strings.TrimSpace(invocation.Provider) == "" ||
			strings.TrimSpace(invocation.Model) == "" || strings.TrimSpace(invocation.InputHash) == "" {
			return SourcingBatchSelection{}, nil, nil, ErrAIInvocationConflict
		}
		if _, duplicate := seenRuns[invocation.RunID]; duplicate {
			return SourcingBatchSelection{}, nil, nil, ErrAIInvocationConflict
		}
		if _, duplicate := seenInvocationProfiles[invocation.ProfileID]; duplicate {
			return SourcingBatchSelection{}, nil, nil, ErrAIInvocationConflict
		}
		seenRuns[invocation.RunID] = struct{}{}
		seenInvocationProfiles[invocation.ProfileID] = struct{}{}
		if provider == "" {
			provider, model = invocation.Provider, invocation.Model
		} else if provider != invocation.Provider || model != invocation.Model {
			return SourcingBatchSelection{}, nil, nil, ErrAIInvocationConflict
		}
	}
	return selection, materials, invocations, nil
}

func sameSourcingGreetingReservation(existing, wanted SourcingGreetingInvocation) bool {
	return existing.BatchID == wanted.BatchID && existing.RunID == wanted.RunID &&
		existing.ProfileID == wanted.ProfileID && existing.ContextRevisionHash == wanted.ContextRevisionHash &&
		existing.RunContentHash == wanted.RunContentHash && existing.Provider == wanted.Provider &&
		existing.Model == wanted.Model && existing.InputHash == wanted.InputHash
}

func sameSourcingGreetingCompletion(existing SourcingGreetingInvocation, req CompleteSourcingGreetingRequest) bool {
	completion := req.Completion
	return existing.Status == completion.Status && existing.GreetingText == req.GreetingText &&
		existing.ContentHash == req.ContentHash && existing.OutputHash == completion.OutputHash &&
		existing.InputTokens == completion.InputTokens && existing.CachedInputTokens == completion.CachedInputTokens &&
		existing.OutputTokens == completion.OutputTokens &&
		sameOptionalInt(existing.ReasoningTokens, completion.ReasoningTokens) &&
		existing.UsageShape == completion.UsageShape && existing.LatencyMs == completion.LatencyMs &&
		existing.ErrorClass == completion.ErrorClass &&
		existing.EstimatedCostMicros == completion.EstimatedCostMicros && existing.FinishedAt != nil &&
		existing.FinishedAt.Equal(completion.FinishedAt)
}

func validPersistedGreetingText(text string) bool {
	return text != "" && text == strings.TrimSpace(text) && utf8.ValidString(text) &&
		len([]byte(text)) <= maxSourcingGreetingTextBytes && norm.NFC.IsNormalString(text)
}

func sourcingGreetingContentHash(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}
