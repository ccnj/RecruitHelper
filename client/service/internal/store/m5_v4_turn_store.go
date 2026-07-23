package store

import (
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"

	"gorm.io/gorm"
)

const communicationV4DialogueTurnSemanticKind = "inboundTurn"
const communicationV4DialogueAdviceKeySeparator = "|advice|"

type FreezeCommunicationV4TurnResult struct {
	Turn        DialogueTurn
	Aggregate   CommunicationV4Aggregate
	Application CommunicationV4ProjectionApplication
	Created     bool
}

// CommunicationV4OwnsTurn distinguishes the V4 aggregate continuation from a
// historical M5-A trial turn. The production patrol must never advance an
// unfinished legacy turn through the V4 owner.
func (s *Store) CommunicationV4OwnsTurn(turnID string) (bool, error) {
	if strings.TrimSpace(turnID) == "" {
		return false, ErrDialogueTurnInvalid
	}
	var owned bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var turn DialogueTurn
		if err := tx.First(&turn, "turn_id = ?", turnID).Error; err != nil {
			return err
		}
		_, found, err := communicationV4TurnApplicationTx(tx, turn)
		if err != nil {
			return err
		}
		owned = found
		return nil
	})
	return owned, err
}

// CommunicationV4NextAdvice returns the authoritative continuation encoded by
// the latest immutable V4 application receipt. Patrol uses it to distinguish
// ordinary intent/reply, service reply and card-rejection reply without
// inferring a branch from nullable legacy DialogueTurn fields.
func (s *Store) CommunicationV4NextAdvice(
	turnID string,
) (communication.V4AdvicePurpose, bool, error) {
	if strings.TrimSpace(turnID) == "" {
		return "", false, ErrDialogueTurnInvalid
	}
	var next communication.V4AdvicePurpose
	owned := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var turn DialogueTurn
		if err := tx.First(&turn, "turn_id = ?", turnID).Error; err != nil {
			return err
		}
		head, found, err := communicationV4TurnHeadApplicationTx(tx, turn)
		if err != nil || !found {
			return err
		}
		next = head.Outcome.NextAdvice
		owned = true
		return nil
	})
	return next, owned, err
}

// FreezeCommunicationV4Turn is the V4 production admission transaction for
// one contiguous inbound turn. It revalidates the complete target and message
// boundary, runs the deterministic reducer with no invented advice, then
// atomically persists the aggregate transition, immutable application receipt
// and dialogue turn. M5TrialSelection is deliberately outside this path.
func (s *Store) FreezeCommunicationV4Turn(
	req FreezeDialogueTurnRequest,
) (*FreezeCommunicationV4TurnResult, error) {
	if err := validateFreezeDialogueTurnRequest(req); err != nil {
		return nil, err
	}
	if req.FrozenAt.IsZero() {
		req.FrozenAt = time.Now()
	}
	req.FrozenAt = req.FrozenAt.UTC()
	out := &FreezeCommunicationV4TurnResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		aggregate, err := communicationV4AggregateTx(tx, req.ProfileID)
		if err != nil {
			return err
		}
		existing, found, err := dialogueTurnByIdentityTx(
			tx, req.TurnID, req.ProfileID, req.InputDigest,
		)
		if err != nil {
			return err
		}
		application, applied, err := communicationV4ApplicationTx(
			tx, req.ProfileID, CommunicationV4InputDialogueTurn, req.TurnID,
		)
		if err != nil {
			return err
		}
		if found || applied {
			if !found || !applied ||
				!sameFrozenDialogueTurn(existing, dialogueTurnFromFreezeRequest(req)) ||
				application.InputDigest != req.InputDigest ||
				application.SemanticKind != communicationV4DialogueTurnSemanticKind ||
				application.MessageSeq != req.InboundThroughSeq {
				return ErrCommunicationV4Conflict
			}
			out.Turn = existing
			out.Aggregate = aggregate
			out.Application = application
			return nil
		}
		if aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
			aggregate.ProjectedThroughSeq != req.HistoryThroughSeq {
			return ErrDialogueTurnBinding
		}
		target, ready, err := communicationTargetTx(tx, req.ProfileID)
		if err != nil {
			return err
		}
		if !ready || target.Conversation.ConversationRef != req.ConversationRef {
			return ErrDialogueTurnBinding
		}
		material, materialReady, err := communicationAIMaterialTx(tx, target)
		if err != nil {
			return err
		}
		if !materialReady ||
			material.ContextBinding.RevisionHash != req.ContextRevisionHash ||
			material.ResumeSnapshot.SnapshotID != req.ResumeSnapshotID {
			return ErrDialogueTurnBinding
		}
		var unfinished int64
		if err := tx.Model(&DialogueTurn{}).
			Where("profile_id = ? AND status IN ?", req.ProfileID, []DialogueTurnStatus{
				DialogueTurnCollected,
				DialogueTurnClassified,
				DialogueTurnAdviceReady,
				DialogueTurnDispatching,
				DialogueTurnManualRequired,
			}).
			Count(&unfinished).Error; err != nil {
			return err
		}
		if unfinished != 0 {
			return ErrDialogueTurnState
		}

		lastOutbound, inbound, facts, firstReal, err := loadCommunicationV4TurnBoundaryTx(
			tx, target.Profile, req,
		)
		if err != nil {
			return err
		}
		digest, turnID, err := DialogueTurnIdentity(req.ProfileID, lastOutbound, inbound)
		if err != nil || digest != req.InputDigest || turnID != req.TurnID {
			return ErrDialogueTurnBinding
		}
		fixedPhrases, err := communication.BuildV4FixedPhraseView(
			material.ContextRevision.SourcePackage,
		)
		if err != nil {
			return ErrDialogueTurnBinding
		}
		decision, err := communication.ReduceV4InboundTurn(communication.V4InboundTurnInput{
			State: aggregate.State, TurnID: req.TurnID, Messages: facts,
			Intent:       communication.IntentAdvice{State: communication.AdviceAbsent},
			Reply:        communication.ReplyAdvice{State: communication.AdviceAbsent},
			FixedPhrases: fixedPhrases,
		})
		if err != nil {
			return err
		}
		decision, plans, policyManualReason := communicationV4AdvicePolicy(decision)
		turn, err := dialogueTurnFromV4Decision(req, decision)
		if err != nil {
			return err
		}
		manualReason := string(decision.ManualReason)
		if policyManualReason != "" {
			manualReason = policyManualReason
			turn.Status = DialogueTurnManualRequired
			turn.FailureReason = policyManualReason
		}
		monthStart, nextMonth := localMonthBounds(req.FrozenAt)
		var monthlyTurns int64
		if err := tx.Model(&DialogueTurn{}).
			Where("created_at >= ? AND created_at < ?", monthStart, nextMonth).
			Count(&monthlyTurns).Error; err != nil {
			return err
		}
		if monthlyTurns >= m5MonthlyTurnLimit {
			return ErrDialogueTurnBudget
		}

		next := aggregate
		next.State = decision.State
		next.Revision++
		next.ProjectedThroughSeq = req.InboundThroughSeq
		next.UpdatedAt = req.FrozenAt
		if manualReason != "" {
			manualAt := req.FrozenAt
			next.AutomationStatus = ProfileCommunicationAutomationManualRequired
			next.ManualReason = manualReason
			next.ManualRequiredAt = &manualAt
		}
		application = CommunicationV4ProjectionApplication{
			ProfileID:   req.ProfileID,
			InputKind:   CommunicationV4InputDialogueTurn,
			InputKey:    req.TurnID,
			InputDigest: req.InputDigest, SemanticKind: communicationV4DialogueTurnSemanticKind,
			MessageSeq: req.InboundThroughSeq, FromRevision: aggregate.Revision, ToRevision: next.Revision,
			Outcome: CommunicationV4ApplicationOutcome{
				Dialogue:       decision.Requirement,
				Actions:        append([]communication.V4EventAction(nil), decision.EventActions...),
				ManualReason:   communication.V4ManualReason(manualReason),
				DialogueStatus: decision.Dialogue.Status,
				NextAdvice:     decision.Dialogue.NextAdvice,
				IntentLabel:    decision.Dialogue.IntentLabel,
				IntentSource:   decision.Dialogue.IntentSource,
				PlannedActions: redactedCommunicationV4Plans(decision.Dialogue.Actions),
			},
			AppliedAt: req.FrozenAt,
		}
		if err := persistCommunicationV4TransitionTx(tx, aggregate, next, application); err != nil {
			return err
		}
		if firstReal != nil && target.Profile.FirstRealMessageSeq == nil {
			communicatingAt := firstReal.CreatedAt
			if communicatingAt.IsZero() {
				communicatingAt = req.FrozenAt
			}
			updated := tx.Model(&CandidateProfile{}).
				Where(
					"profile_id = ? AND first_real_message_seq = ?",
					req.ProfileID,
					decision.State.LastRealMessageSeq,
				).
				Updates(map[string]any{
					"first_real_message_seq": firstReal.Seq,
					"communicating_at":       communicatingAt,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrCommunicationV4Conflict
			}
		}
		if err := tx.Create(&turn).Error; err != nil {
			return err
		}
		if len(plans) != 0 {
			plan := plans[0]
			action := CommunicationAction{
				ActionID: plan.ActionKey, TurnID: turn.TurnID, Kind: CommunicationActionReplyText,
				Text: plan.Text, ContentHash: textcanon.Hash(plan.Text),
				Status:    CommunicationActionPlanned,
				PlannedAt: req.FrozenAt, CreatedAt: req.FrozenAt, UpdatedAt: req.FrozenAt,
			}
			if err := tx.Create(&action).Error; err != nil {
				return err
			}
		}
		out.Turn = turn
		out.Aggregate = next
		out.Application = application
		out.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validateFreezeDialogueTurnRequest(req FreezeDialogueTurnRequest) error {
	if strings.TrimSpace(req.TurnID) == "" || strings.TrimSpace(req.ProfileID) == "" ||
		strings.TrimSpace(req.ConversationRef) == "" || !validCommunicationV4Digest(req.InputDigest) ||
		strings.TrimSpace(req.ContextRevisionHash) == "" || strings.TrimSpace(req.ResumeSnapshotID) == "" ||
		strings.TrimSpace(req.RecommendedTimeText) == "" || strings.TrimSpace(req.RenderFormatVersion) == "" ||
		req.HistoryThroughSeq <= 0 || req.InboundFromSeq <= req.HistoryThroughSeq ||
		req.InboundThroughSeq < req.InboundFromSeq {
		return ErrDialogueTurnInvalid
	}
	return nil
}

func dialogueTurnFromFreezeRequest(req FreezeDialogueTurnRequest) DialogueTurn {
	return DialogueTurn{
		TurnID: req.TurnID, ProfileID: req.ProfileID, ConversationRef: req.ConversationRef,
		InputDigest: req.InputDigest, HistoryThroughSeq: req.HistoryThroughSeq,
		InboundFromSeq: req.InboundFromSeq, InboundThroughSeq: req.InboundThroughSeq,
		ContextRevisionHash: req.ContextRevisionHash, ResumeSnapshotID: req.ResumeSnapshotID,
		RecommendedTimeText: req.RecommendedTimeText, RenderFormatVersion: req.RenderFormatVersion,
		Status: DialogueTurnCollected, CreatedAt: req.FrozenAt, UpdatedAt: req.FrozenAt,
	}
}

func dialogueTurnFromV4Decision(
	req FreezeDialogueTurnRequest,
	decision communication.V4InboundTurnDecision,
) (DialogueTurn, error) {
	turn := dialogueTurnFromFreezeRequest(req)
	turn.IntentLabel = decision.Dialogue.IntentLabel
	turn.IntentSource = dialogueIntentSourceFromV4(decision.Dialogue.IntentSource)
	switch decision.Dialogue.Status {
	case communication.V4DialogueWaitingAdvice:
		switch decision.Dialogue.NextAdvice {
		case communication.V4AdviceIntent:
			turn.Status = DialogueTurnCollected
		case communication.V4AdviceReply, communication.V4AdviceServiceReply,
			communication.V4AdviceInterviewRejectionReply:
			turn.Status = DialogueTurnClassified
			classifiedAt := req.FrozenAt
			turn.ClassifiedAt = &classifiedAt
		default:
			return DialogueTurn{}, ErrDialogueTurnState
		}
	case communication.V4DialogueWaitingPrerequisite:
		turn.Status = DialogueTurnCollected
	case communication.V4DialogueActionsPlanned:
		turn.Status = DialogueTurnAdviceReady
		classifiedAt := req.FrozenAt
		turn.ClassifiedAt = &classifiedAt
	case communication.V4DialogueNoAction:
		turn.Status = DialogueTurnCompleted
	case communication.V4DialogueManualRequired:
		turn.Status = DialogueTurnManualRequired
		turn.FailureReason = string(decision.ManualReason)
	default:
		return DialogueTurn{}, ErrDialogueTurnState
	}
	return turn, nil
}

func dialogueIntentSourceFromV4(source communication.IntentSource) DialogueIntentSource {
	switch source {
	case communication.IntentSourceCodeShortCircuit:
		return DialogueIntentCodeShortCircuit
	case communication.IntentSourceLLM:
		return DialogueIntentLLM
	case communication.IntentSourceLLMFailureFallback:
		return DialogueIntentLLMFailure
	case communication.IntentSourceBusinessEvent:
		return DialogueIntentBusinessEvent
	default:
		return ""
	}
}

func communicationV4TurnApplicationTx(
	tx *gorm.DB,
	turn DialogueTurn,
) (CommunicationV4ProjectionApplication, bool, error) {
	application, found, err := communicationV4ApplicationTx(
		tx,
		turn.ProfileID,
		CommunicationV4InputDialogueTurn,
		turn.TurnID,
	)
	if err != nil || !found {
		return application, found, err
	}
	if application.InputDigest != turn.InputDigest ||
		application.SemanticKind != communicationV4DialogueTurnSemanticKind ||
		application.MessageSeq != turn.InboundThroughSeq {
		return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
	}
	return application, true, nil
}

func communicationV4DialogueAdviceKey(turnID string, purpose m5ai.CompletionPurpose) string {
	return turnID + communicationV4DialogueAdviceKeySeparator + string(purpose)
}

func communicationV4OutcomeAuthorizesAdvice(
	outcome CommunicationV4ApplicationOutcome,
	purpose m5ai.CompletionPurpose,
) bool {
	if outcome.DialogueStatus != communication.V4DialogueWaitingAdvice {
		return false
	}
	switch purpose {
	case m5ai.PurposeIntent:
		return outcome.NextAdvice == communication.V4AdviceIntent
	case m5ai.PurposeReply:
		switch outcome.NextAdvice {
		case communication.V4AdviceReply, communication.V4AdviceServiceReply,
			communication.V4AdviceInterviewRejectionReply:
			return true
		}
	}
	return false
}

func communicationV4TurnHeadApplicationTx(
	tx *gorm.DB,
	turn DialogueTurn,
) (CommunicationV4ProjectionApplication, bool, error) {
	head, found, err := communicationV4TurnApplicationTx(tx, turn)
	if err != nil || !found {
		return head, found, err
	}
	for _, purpose := range []m5ai.CompletionPurpose{m5ai.PurposeIntent, m5ai.PurposeReply} {
		key := communicationV4DialogueAdviceKey(turn.TurnID, purpose)
		continuation, exists, err := communicationV4ApplicationTx(
			tx,
			turn.ProfileID,
			CommunicationV4InputDialogueAdvice,
			key,
		)
		if err != nil {
			return CommunicationV4ProjectionApplication{}, false, err
		}
		if !exists {
			continue
		}
		if !communicationV4OutcomeAuthorizesAdvice(head.Outcome, purpose) ||
			continuation.SemanticKind != string(purpose) ||
			continuation.MessageSeq != turn.InboundThroughSeq ||
			continuation.FromRevision != head.ToRevision ||
			continuation.Outcome.Dialogue != head.Outcome.Dialogue {
			return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
		}
		head = continuation
	}
	return head, true, nil
}

func markCommunicationV4AutomationManualTx(
	tx *gorm.DB,
	profileID string,
	reason string,
	at time.Time,
) error {
	aggregate, err := communicationV4AggregateTx(tx, profileID)
	if err != nil {
		return err
	}
	switch aggregate.AutomationStatus {
	case ProfileCommunicationAutomationManualRequired:
		if aggregate.ManualReason != reason || aggregate.ManualRequiredAt == nil {
			return ErrCommunicationV4Conflict
		}
		return nil
	case ProfileCommunicationAutomationActive:
	default:
		return ErrCommunicationV4Conflict
	}
	updated := tx.Model(&CommunicationV4Aggregate{}).
		Where(
			"profile_id = ? AND automation_status = ?",
			profileID,
			ProfileCommunicationAutomationActive,
		).
		Updates(map[string]any{
			"automation_status":  ProfileCommunicationAutomationManualRequired,
			"manual_reason":      reason,
			"manual_required_at": at.UTC(),
			"updated_at":         at.UTC(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrCommunicationV4Conflict
	}
	return nil
}

func loadCommunicationV4TurnBoundaryTx(
	tx *gorm.DB,
	profile CandidateProfile,
	req FreezeDialogueTurnRequest,
) (Message, []Message, []communication.LedgerMessageFact, *Message, error) {
	var tail int64
	if err := tx.Model(&Message{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL",
			profile.Platform,
			profile.AccountRef,
			req.ConversationRef,
		).
		Select("COALESCE(MAX(seq), 0)").
		Scan(&tail).Error; err != nil || tail != req.InboundThroughSeq {
		return Message{}, nil, nil, nil, ErrDialogueTurnBinding
	}
	var outboundTail int64
	if err := tx.Model(&Message{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL AND direction = ?",
			profile.Platform,
			profile.AccountRef,
			req.ConversationRef,
			"out",
		).
		Select("COALESCE(MAX(seq), 0)").
		Scan(&outboundTail).Error; err != nil || outboundTail != req.HistoryThroughSeq {
		return Message{}, nil, nil, nil, ErrDialogueTurnBinding
	}
	var lastOutbound Message
	if err := tx.First(
		&lastOutbound,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND retracted_at IS NULL",
		profile.Platform,
		profile.AccountRef,
		req.ConversationRef,
		req.HistoryThroughSeq,
	).Error; err != nil || lastOutbound.Direction != "out" {
		return Message{}, nil, nil, nil, ErrDialogueTurnBinding
	}
	var boundary []Message
	if err := tx.Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq > ? AND seq <= ? "+
			"AND retracted_at IS NULL",
		profile.Platform,
		profile.AccountRef,
		req.ConversationRef,
		req.HistoryThroughSeq,
		req.InboundThroughSeq,
	).Order("seq").Find(&boundary).Error; err != nil {
		return Message{}, nil, nil, nil, err
	}
	inbound, validBoundary := DialogueTurnCandidateMessages(boundary)
	if !validBoundary || len(boundary) == 0 ||
		boundary[len(boundary)-1].Seq != req.InboundThroughSeq ||
		inbound[0].Seq != req.InboundFromSeq {
		return Message{}, nil, nil, nil, ErrDialogueTurnBinding
	}
	facts := make([]communication.LedgerMessageFact, 0, len(boundary))
	var firstReal *Message
	for index := range boundary {
		message := boundary[index]
		facts = append(facts, communication.LedgerMessageFact{
			Seq: message.Seq, Direction: message.Direction, Kind: message.Kind,
			Text: message.Text, CardType: message.CardType, CardState: message.CardState,
			Origin: message.Origin, TsApproxMs: message.TsApproxMs,
		})
		if firstReal == nil && message.Direction == "in" && message.Kind != "system" &&
			IsM5RealCandidateMessage(message) {
			copy := message
			firstReal = &copy
		}
	}
	return lastOutbound, inbound, facts, firstReal, nil
}
