package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"

	"gorm.io/gorm"
)

const communicationV4ScheduleAIInvocationDomain = "communication-v4-schedule-ai-v1|"

type ReserveCommunicationV4ScheduleAIInvocationRequest struct {
	ProfileID                   string
	ConversationRef             string
	ExpectedRevision            uint64
	ExpectedProjectedThroughSeq int64
	ContextRevisionHash         string
	ResumeSnapshotID            string
	EvaluatedAt                 time.Time
	Provider                    string
	Model                       string
	InputHash                   string
	CreatedAt                   time.Time
}

type ReserveCommunicationV4ScheduleAIInvocationResult struct {
	Invocation CommunicationV4ScheduleAIInvocation
	Created    bool
}

// ReserveCommunicationV4ScheduleAIInvocation is the sole authorization for a
// silence-followup provider call. Existing unfinished rows are crash evidence,
// not permission to call the provider again.
func (s *Store) ReserveCommunicationV4ScheduleAIInvocation(
	req ReserveCommunicationV4ScheduleAIInvocationRequest,
) (*ReserveCommunicationV4ScheduleAIInvocationResult, error) {
	req.ProfileID = strings.TrimSpace(req.ProfileID)
	req.ConversationRef = strings.TrimSpace(req.ConversationRef)
	req.ContextRevisionHash = strings.TrimSpace(req.ContextRevisionHash)
	req.ResumeSnapshotID = strings.TrimSpace(req.ResumeSnapshotID)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	req.InputHash = strings.TrimSpace(req.InputHash)
	if req.ProfileID == "" ||
		req.ConversationRef == "" ||
		req.ContextRevisionHash == "" ||
		req.ResumeSnapshotID == "" ||
		req.Provider == "" ||
		req.Model == "" ||
		req.InputHash == "" ||
		req.ExpectedProjectedThroughSeq < 0 ||
		req.EvaluatedAt.IsZero() {
		return nil, ErrAIInvocationInvalid
	}
	req.EvaluatedAt = req.EvaluatedAt.UTC()
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	req.CreatedAt = req.CreatedAt.UTC()

	out := &ReserveCommunicationV4ScheduleAIInvocationResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		decision, err := validateCommunicationV4ScheduleAdviceBoundaryTx(
			tx,
			req,
		)
		if err != nil {
			return err
		}
		invocationID := communicationV4ScheduleAIInvocationID(
			req.ProfileID,
			decision.AdviceKey,
		)
		wanted := CommunicationV4ScheduleAIInvocation{
			InvocationID:             invocationID,
			AdviceKey:                decision.AdviceKey,
			ProfileID:                req.ProfileID,
			ConversationRef:          req.ConversationRef,
			BasisRevision:            req.ExpectedRevision,
			BasisProjectedThroughSeq: req.ExpectedProjectedThroughSeq,
			ContextRevisionHash:      req.ContextRevisionHash,
			ResumeSnapshotID:         req.ResumeSnapshotID,
			EvaluatedAt:              req.EvaluatedAt,
			Purpose:                  m5ai.PurposeSilenceFollowup,
			Attempt:                  1,
			Provider:                 req.Provider,
			Model:                    req.Model,
			InputHash:                req.InputHash,
			Status:                   AIInvocationTransportFailed,
			CreatedAt:                req.CreatedAt,
		}
		var existing CommunicationV4ScheduleAIInvocation
		err = tx.First(&existing, "invocation_id = ?", invocationID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = tx.First(
				&existing,
				"profile_id = ? AND advice_key = ?",
				req.ProfileID,
				decision.AdviceKey,
			).Error
		}
		if err == nil {
			if !sameCommunicationV4ScheduleAIReservation(existing, wanted) {
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

type CompleteCommunicationV4ScheduleAIInvocationRequest struct {
	Completion     AIInvocationCompletion
	SuggestionText string
}

func (s *Store) CompleteCommunicationV4ScheduleAIInvocation(
	req CompleteCommunicationV4ScheduleAIInvocationRequest,
) (*CommunicationV4ScheduleAIInvocation, error) {
	req.SuggestionText = strings.TrimSpace(req.SuggestionText)
	if err := validateInvocationCompletion(req.Completion); err != nil {
		return nil, err
	}
	if req.Completion.Status == AIInvocationOK {
		if m5ai.ValidateSendText(req.SuggestionText) != nil {
			return nil, ErrAIInvocationInvalid
		}
	} else if req.SuggestionText != "" {
		return nil, ErrAIInvocationInvalid
	}

	var out CommunicationV4ScheduleAIInvocation
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var invocation CommunicationV4ScheduleAIInvocation
		if err := tx.First(
			&invocation,
			"invocation_id = ?",
			req.Completion.InvocationID,
		).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAIInvocationNotFound
			}
			return err
		}
		if invocation.Purpose != m5ai.PurposeSilenceFollowup ||
			invocation.Attempt != 1 ||
			!validCommunicationV4ScheduleAIInvocation(invocation) {
			return ErrAIInvocationConflict
		}
		if invocation.FinishedAt != nil {
			if !sameCommunicationV4ScheduleAICompletion(
				invocation,
				req.Completion,
				req.SuggestionText,
			) {
				return ErrAIInvocationConflict
			}
			out = invocation
			return nil
		}
		updates := map[string]any{
			"suggestion_text":         req.SuggestionText,
			"output_hash":             req.Completion.OutputHash,
			"input_tokens":            req.Completion.InputTokens,
			"cached_input_tokens":     req.Completion.CachedInputTokens,
			"output_tokens":           req.Completion.OutputTokens,
			"reasoning_tokens":        req.Completion.ReasoningTokens,
			"usage_shape":             req.Completion.UsageShape,
			"reasoning_content_empty": req.Completion.ReasoningContentEmpty,
			"latency_ms":              req.Completion.LatencyMs,
			"status":                  req.Completion.Status,
			"error_class":             req.Completion.ErrorClass,
			"failure_stage":           req.Completion.FailureStage,
			"error_detail_code":       req.Completion.ErrorDetailCode,
			"provider_http_status":    req.Completion.ProviderHTTPStatus,
			"request_bytes":           req.Completion.RequestBytes,
			"response_bytes":          req.Completion.ResponseBytes,
			"trace_status":            req.Completion.TraceStatus,
			"estimated_cost_micros":   req.Completion.EstimatedCostMicros,
			"finished_at":             req.Completion.FinishedAt.UTC(),
		}
		updated := tx.Model(&CommunicationV4ScheduleAIInvocation{}).
			Where("invocation_id = ? AND finished_at IS NULL", invocation.InvocationID).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAIInvocationConflict
		}
		if err := tx.First(&out, "invocation_id = ?", invocation.InvocationID).Error; err != nil {
			return err
		}
		if !sameCommunicationV4ScheduleAICompletion(
			out,
			req.Completion,
			req.SuggestionText,
		) {
			return ErrAIInvocationConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func validateCommunicationV4ScheduleAdviceBoundaryTx(
	tx *gorm.DB,
	req ReserveCommunicationV4ScheduleAIInvocationRequest,
) (communication.V4ScheduleDecision, error) {
	target, ready, err := communicationTargetTx(tx, req.ProfileID)
	if err != nil {
		return communication.V4ScheduleDecision{}, err
	}
	if !ready ||
		target.Conversation.ConversationRef != req.ConversationRef ||
		target.Aggregate.Revision != req.ExpectedRevision ||
		target.Aggregate.ProjectedThroughSeq != req.ExpectedProjectedThroughSeq {
		return communication.V4ScheduleDecision{}, ErrCommunicationV4Conflict
	}
	_, conversation, activeTail, err := communicationV4ArchiveBoundaryTx(
		tx,
		req.ProfileID,
		req.ConversationRef,
	)
	if err != nil {
		return communication.V4ScheduleDecision{}, err
	}
	if conversation.LastMessageSeq != activeTail ||
		activeTail != req.ExpectedProjectedThroughSeq {
		return communication.V4ScheduleDecision{}, ErrCommunicationV4Conflict
	}
	material, materialReady, err := communicationAIMaterialTx(tx, target)
	if err != nil {
		return communication.V4ScheduleDecision{}, err
	}
	if !materialReady ||
		material.ContextRevision.RevisionHash != req.ContextRevisionHash ||
		material.ResumeSnapshot.SnapshotID != req.ResumeSnapshotID {
		return communication.V4ScheduleDecision{}, ErrCommunicationV4Conflict
	}
	decision, err := communication.EvaluateV4Schedule(
		communication.V4ScheduleInput{
			ProfileKey:          target.Profile.ProfileID,
			State:               target.Aggregate.State,
			ProjectedThroughSeq: target.Aggregate.ProjectedThroughSeq,
			Now:                 req.EvaluatedAt,
			HasPendingDialogue:  false,
			Reply:               communication.ReplyAdvice{State: communication.AdviceAbsent},
		},
	)
	if err != nil {
		return communication.V4ScheduleDecision{}, err
	}
	if decision.Status != communication.V4ScheduleWaitingAdvice ||
		decision.NextAdvice != communication.V4AdviceSilenceFollowup ||
		strings.TrimSpace(decision.AdviceKey) == "" ||
		len(decision.Actions) != 0 ||
		decision.ManualReason != "" {
		return communication.V4ScheduleDecision{}, ErrCommunicationV4Conflict
	}
	return decision, nil
}

func communicationV4ScheduleAIInvocationID(
	profileID string,
	adviceKey string,
) string {
	digest := sha256.Sum256([]byte(
		communicationV4ScheduleAIInvocationDomain +
			profileID + "\x00" + adviceKey,
	))
	return hex.EncodeToString(digest[:])
}

// sameCommunicationV4ScheduleAIReservation 只比较构成预留身份的三项。
//
// AdviceKey 已经完整定义了"哪个候选人、什么用途、第几轮、第几阶段"的沉默
// 追问；ProfileID 与 Purpose 是它的结构性冗余，用来兜住 ID 派生出错或串档。
// 这三项不等才是真正的账本错乱，值得响亮拒绝。
//
// 其余字段——ConversationRef、BasisRevision、BasisProjectedThroughSeq、
// ContextRevisionHash、ResumeSnapshotID、Provider、Model、InputHash——是这次
// 调用当时的上下文快照。它们照常写入、照常保留审计价值，但不参与身份判定：
// 职位配置更新、模型切换、候选人新消息、简历重采都会改动它们，而这些全是
// 正常业务事件，不该让一条尚未收束的预留变成永久毒药。InvocationID 与
// Attempt 则是恒等量（前者就是查询键本身，后者在建预留时写死 1），比较它们
// 是死代码。
//
// 复用语义（2026-08-01 甲方裁决）：命中已有预留即直接复用其话术，不再调用
// AI。因此配置或模型换代后，存量候选人继续沿用旧话术，新的 AdviceKey 才用
// 新配置/新模型生成——切换是渐进的，不会卡住任何人。若确需让存量重新生成，
// 显式清理在途预留，不要依赖冲突报错倒逼。
//
// 事故记录：2026-08-01 11:04 职位配置换代后，79 个尚未发出沉默追问的候选人
// 在 12:03 的一分钟内被连续判为 ErrAIInvocationConflict，进而隔离会话并冻结
// 档案。根因即此处把会变的上下文当成了身份。
func sameCommunicationV4ScheduleAIReservation(
	existing CommunicationV4ScheduleAIInvocation,
	wanted CommunicationV4ScheduleAIInvocation,
) bool {
	return existing.AdviceKey == wanted.AdviceKey &&
		existing.ProfileID == wanted.ProfileID &&
		existing.Purpose == wanted.Purpose &&
		validCommunicationV4ScheduleAIInvocation(existing)
}

func validCommunicationV4ScheduleAIInvocation(
	invocation CommunicationV4ScheduleAIInvocation,
) bool {
	if strings.TrimSpace(invocation.InvocationID) == "" ||
		strings.TrimSpace(invocation.AdviceKey) == "" ||
		strings.TrimSpace(invocation.ProfileID) == "" ||
		strings.TrimSpace(invocation.ConversationRef) == "" ||
		strings.TrimSpace(invocation.ContextRevisionHash) == "" ||
		strings.TrimSpace(invocation.ResumeSnapshotID) == "" ||
		strings.TrimSpace(invocation.Provider) == "" ||
		strings.TrimSpace(invocation.Model) == "" ||
		strings.TrimSpace(invocation.InputHash) == "" ||
		invocation.BasisProjectedThroughSeq < 0 ||
		invocation.EvaluatedAt.IsZero() ||
		invocation.Purpose != m5ai.PurposeSilenceFollowup ||
		invocation.Attempt != 1 ||
		invocation.CreatedAt.IsZero() ||
		communicationV4ScheduleAIInvocationID(
			invocation.ProfileID,
			invocation.AdviceKey,
		) != invocation.InvocationID {
		return false
	}
	if invocation.FinishedAt == nil {
		return invocation.Status == AIInvocationTransportFailed &&
			invocation.SuggestionText == "" &&
			invocation.OutputHash == "" &&
			invocation.InputTokens == 0 &&
			invocation.CachedInputTokens == 0 &&
			invocation.OutputTokens == 0 &&
			invocation.ReasoningTokens == nil &&
			invocation.UsageShape == "" &&
			invocation.LatencyMs == 0 &&
			invocation.ErrorClass == ""
	}
	completion := communicationV4ScheduleAICompletion(invocation)
	return validateInvocationCompletion(completion) == nil &&
		((invocation.Status == AIInvocationOK &&
			m5ai.ValidateSendText(invocation.SuggestionText) == nil) ||
			(invocation.Status != AIInvocationOK &&
				invocation.SuggestionText == ""))
}

func communicationV4ScheduleAICompletion(
	invocation CommunicationV4ScheduleAIInvocation,
) AIInvocationCompletion {
	return AIInvocationCompletion{
		InvocationID:          invocation.InvocationID,
		Status:                invocation.Status,
		OutputHash:            invocation.OutputHash,
		InputTokens:           invocation.InputTokens,
		CachedInputTokens:     invocation.CachedInputTokens,
		OutputTokens:          invocation.OutputTokens,
		ReasoningTokens:       invocation.ReasoningTokens,
		UsageShape:            invocation.UsageShape,
		ReasoningContentEmpty: invocation.ReasoningContentEmpty,
		LatencyMs:             invocation.LatencyMs,
		ErrorClass:            invocation.ErrorClass,
		FailureStage:          invocation.FailureStage,
		ErrorDetailCode:       invocation.ErrorDetailCode,
		ProviderHTTPStatus:    invocation.ProviderHTTPStatus,
		RequestBytes:          invocation.RequestBytes,
		ResponseBytes:         invocation.ResponseBytes,
		TraceStatus:           invocation.TraceStatus,
		EstimatedCostMicros:   invocation.EstimatedCostMicros,
		FinishedAt:            *invocation.FinishedAt,
	}
}

func sameCommunicationV4ScheduleAICompletion(
	invocation CommunicationV4ScheduleAIInvocation,
	completion AIInvocationCompletion,
	suggestionText string,
) bool {
	if invocation.FinishedAt == nil ||
		invocation.SuggestionText != suggestionText {
		return false
	}
	existing := communicationV4ScheduleAICompletion(invocation)
	return existing.InvocationID == completion.InvocationID &&
		existing.Status == completion.Status &&
		existing.OutputHash == completion.OutputHash &&
		existing.InputTokens == completion.InputTokens &&
		existing.CachedInputTokens == completion.CachedInputTokens &&
		existing.OutputTokens == completion.OutputTokens &&
		sameOptionalInt(existing.ReasoningTokens, completion.ReasoningTokens) &&
		existing.UsageShape == completion.UsageShape &&
		existing.ReasoningContentEmpty == completion.ReasoningContentEmpty &&
		existing.LatencyMs == completion.LatencyMs &&
		existing.ErrorClass == completion.ErrorClass &&
		existing.FailureStage == completion.FailureStage &&
		existing.ErrorDetailCode == completion.ErrorDetailCode &&
		sameOptionalInt(existing.ProviderHTTPStatus, completion.ProviderHTTPStatus) &&
		existing.RequestBytes == completion.RequestBytes &&
		existing.ResponseBytes == completion.ResponseBytes &&
		existing.TraceStatus == completion.TraceStatus &&
		existing.EstimatedCostMicros == completion.EstimatedCostMicros &&
		existing.FinishedAt.Equal(completion.FinishedAt)
}
