package syncledger

import (
	"errors"

	"recruithelper/client/service/internal/store"
)

var (
	ErrPlanNotExecutable = errors.New("对齐计划尚不可落库")
	ErrInvalidPlan       = errors.New("对齐计划与决策不一致")
)

// PlanRepository is the narrow persistence seam. The concrete store owns both
// transaction forms; adapters cannot accidentally feed a historical rebaseline
// through normal new-message accounting.
type PlanRepository interface {
	ApplyConversationChanges(store.ApplyConversationChangesRequest) (*store.ApplyConversationChangesResult, error)
	RebuildConversationBaseline(store.RebuildConversationBaselineRequest) (*store.RebuildConversationBaselineResult, error)
	CorrectMessageClassification(store.CorrectMessageClassificationRequest) (*store.CorrectMessageClassificationResult, error)
}

type AppliedPlan struct {
	Decision             Decision
	Inserted             []store.Message
	TailSeq              int64
	AdoptedBoundarySeq   int64
	HistoricalThroughSeq int64
}

// ApplyPlan routes exactly one executable plan to its matching transaction.
func ApplyPlan(repo PlanRepository, plan *Plan) (*AppliedPlan, error) {
	if repo == nil || plan == nil {
		return nil, ErrInvalidPlan
	}
	if plan.Decision == DecisionNeedDeep {
		return nil, ErrPlanNotExecutable
	}
	if plan.Decision == DecisionClassificationCorrection {
		if plan.Apply != nil || plan.Rebaseline != nil || plan.Correction == nil ||
			len(plan.EventProjection) != 0 || len(plan.CardTransitions) != 0 || len(plan.Audits) != 0 {
			return nil, ErrInvalidPlan
		}
		result, err := repo.CorrectMessageClassification(*plan.Correction)
		if err != nil {
			return nil, err
		}
		inserted := []store.Message(nil)
		if !result.AlreadyApplied {
			inserted = []store.Message{result.Corrected}
		}
		return &AppliedPlan{
			Decision: plan.Decision, Inserted: inserted, TailSeq: result.TailSeq,
			AdoptedBoundarySeq: result.AdoptedBoundarySeq,
		}, nil
	}
	if plan.Decision == DecisionAuditedRebaseline {
		if plan.Apply != nil || plan.Rebaseline == nil || plan.Correction != nil || len(plan.EventProjection) != 0 {
			return nil, ErrInvalidPlan
		}
		result, err := repo.RebuildConversationBaseline(*plan.Rebaseline)
		if err != nil {
			return nil, err
		}
		return &AppliedPlan{
			Decision: plan.Decision, Inserted: result.Inserted, TailSeq: result.TailSeq,
			AdoptedBoundarySeq:   result.AdoptedBoundarySeq,
			HistoricalThroughSeq: result.HistoricalThroughSeq,
		}, nil
	}
	if plan.Apply == nil || plan.Rebaseline != nil || plan.Correction != nil {
		return nil, ErrInvalidPlan
	}
	result, err := repo.ApplyConversationChanges(*plan.Apply)
	if err != nil {
		return nil, err
	}
	return &AppliedPlan{
		Decision: plan.Decision, Inserted: result.Inserted, TailSeq: result.TailSeq,
		AdoptedBoundarySeq: result.AdoptedBoundarySeq,
	}, nil
}
