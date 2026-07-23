package store

import (
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"

	"gorm.io/gorm"
)

const (
	communicationV4ManualMultiVisibleAction = "multiVisibleActionPolicyConflict"
	communicationV4ManualUnsupportedAction  = "unsupportedV4ActionKind"
)

type communicationV4AdviceDigestInput struct {
	TurnID       string                 `json:"turnId"`
	InvocationID string                 `json:"invocationId"`
	Purpose      m5ai.CompletionPurpose `json:"purpose"`
	Status       AIInvocationStatus     `json:"status"`
	OutputHash   string                 `json:"outputHash,omitempty"`
	IntentLabel  m5ai.IntentLabel       `json:"intentLabel,omitempty"`
	IntentSource DialogueIntentSource   `json:"intentSource,omitempty"`
	TextHash     string                 `json:"textHash,omitempty"`
	ManualReason string                 `json:"manualReason,omitempty"`
}

func communicationV4AdviceDigest(
	turn DialogueTurn,
	invocation AIInvocation,
	label m5ai.IntentLabel,
	source DialogueIntentSource,
	text string,
	manualReason string,
) (string, error) {
	return communicationV4InputDigest(communicationV4AdviceDigestInput{
		TurnID: turn.TurnID, InvocationID: invocation.InvocationID,
		Purpose: invocation.Purpose, Status: invocation.Status, OutputHash: invocation.OutputHash,
		IntentLabel: label, IntentSource: source, TextHash: textcanon.Hash(text),
		ManualReason: manualReason,
	})
}

func communicationV4InitialRequirementTx(
	tx *gorm.DB,
	turn DialogueTurn,
) (communication.V4DialogueRequirement, error) {
	initial, found, err := communicationV4TurnApplicationTx(tx, turn)
	if err != nil {
		return "", err
	}
	if !found || initial.Outcome.Dialogue == "" {
		return "", ErrCommunicationV4Corrupt
	}
	return initial.Outcome.Dialogue, nil
}

func communicationV4TurnReducerInputTx(
	tx *gorm.DB,
	turn DialogueTurn,
	aggregate CommunicationV4Aggregate,
	intent communication.IntentAdvice,
	reply communication.ReplyAdvice,
) (communication.V4InboundTurnDecision, error) {
	requirement, err := communicationV4InitialRequirementTx(tx, turn)
	if err != nil {
		return communication.V4InboundTurnDecision{}, err
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", turn.ProfileID).Error; err != nil {
		return communication.V4InboundTurnDecision{}, err
	}
	_, _, facts, _, err := loadCommunicationV4TurnBoundaryTx(
		tx,
		profile,
		FreezeDialogueTurnRequest{
			TurnID: turn.TurnID, ProfileID: turn.ProfileID, ConversationRef: turn.ConversationRef,
			InputDigest: turn.InputDigest, HistoryThroughSeq: turn.HistoryThroughSeq,
			InboundFromSeq: turn.InboundFromSeq, InboundThroughSeq: turn.InboundThroughSeq,
			ContextRevisionHash: turn.ContextRevisionHash, ResumeSnapshotID: turn.ResumeSnapshotID,
			RecommendedTimeText: turn.RecommendedTimeText, RenderFormatVersion: turn.RenderFormatVersion,
			FrozenAt: turn.CreatedAt,
		},
	)
	if err != nil {
		return communication.V4InboundTurnDecision{}, err
	}
	var revision JobAIContextRevision
	if err := tx.First(&revision, "revision_hash = ?", turn.ContextRevisionHash).Error; err != nil {
		return communication.V4InboundTurnDecision{}, ErrDialogueTurnBinding
	}
	fixedPhrases, err := communication.BuildV4FixedPhraseView(revision.SourcePackage)
	if err != nil {
		return communication.V4InboundTurnDecision{}, ErrDialogueTurnBinding
	}
	decision, err := communication.ReduceV4InboundTurn(communication.V4InboundTurnInput{
		State: aggregate.State, TurnID: turn.TurnID, Messages: facts,
		Intent: intent, Reply: reply, FixedPhrases: fixedPhrases,
	})
	if err != nil {
		return communication.V4InboundTurnDecision{}, err
	}
	if decision.Requirement != requirement {
		return communication.V4InboundTurnDecision{}, ErrCommunicationV4Conflict
	}
	return decision, nil
}

func communicationV4IntentAdvice(
	invocation AIInvocation,
	label m5ai.IntentLabel,
	source DialogueIntentSource,
	manualReason string,
) (communication.IntentAdvice, error) {
	if manualReason != "" && manualReason != "intentRejected" {
		return communication.IntentAdvice{State: communication.AdviceAbsent}, nil
	}
	switch invocation.Status {
	case AIInvocationOK:
		if !validIntentLabel(label) || source != DialogueIntentLLM {
			return communication.IntentAdvice{}, ErrAIInvocationInvalid
		}
		return communication.IntentAdvice{
			State: communication.AdviceOK,
			Suggestion: m5ai.IntentSuggestion{
				Label: label,
			},
		}, nil
	default:
		if label != m5ai.IntentNeutral || source != DialogueIntentLLMFailure {
			return communication.IntentAdvice{}, ErrAIInvocationInvalid
		}
		return communication.IntentAdvice{State: communication.AdviceFailed}, nil
	}
}

func communicationV4IntentAdviceFromTurn(turn DialogueTurn) (communication.IntentAdvice, error) {
	switch turn.IntentSource {
	case DialogueIntentLLM:
		if !validIntentLabel(turn.IntentLabel) {
			return communication.IntentAdvice{}, ErrDialogueTurnState
		}
		return communication.IntentAdvice{
			State:      communication.AdviceOK,
			Suggestion: m5ai.IntentSuggestion{Label: turn.IntentLabel},
		}, nil
	case DialogueIntentLLMFailure:
		if turn.IntentLabel != m5ai.IntentNeutral {
			return communication.IntentAdvice{}, ErrDialogueTurnState
		}
		return communication.IntentAdvice{State: communication.AdviceFailed}, nil
	case DialogueIntentCodeShortCircuit, DialogueIntentBusinessEvent:
		return communication.IntentAdvice{State: communication.AdviceAbsent}, nil
	default:
		return communication.IntentAdvice{}, ErrDialogueTurnState
	}
}

func manualCommunicationV4AdviceDecision(
	state communication.V4State,
	requirement communication.V4DialogueRequirement,
	reason string,
	label m5ai.IntentLabel,
	source DialogueIntentSource,
) communication.V4InboundTurnDecision {
	return communication.V4InboundTurnDecision{
		State: state, Requirement: requirement,
		Dialogue: communication.V4DialogueDecision{
			State: state, Status: communication.V4DialogueManualRequired,
			IntentLabel: label, IntentSource: communication.IntentSource(source),
			NextAdvice:   communication.V4AdviceNone,
			ManualReason: communication.V4ManualReason(reason),
		},
		ManualReason: communication.V4ManualReason(reason),
	}
}

func redactedCommunicationV4Plans(
	plans []communication.V4PlannedAction,
) []communication.V4PlannedAction {
	out := make([]communication.V4PlannedAction, len(plans))
	copy(out, plans)
	for index := range out {
		out[index].Text = ""
	}
	return out
}

func supportedCommunicationV4TextPlan(plan communication.V4PlannedAction) bool {
	if strings.TrimSpace(plan.ActionKey) == "" || strings.TrimSpace(plan.Text) == "" {
		return false
	}
	return supportedCommunicationV4TextKind(plan.Kind)
}

func supportedCommunicationV4TextKind(kind communication.V4ActionKind) bool {
	switch kind {
	case communication.V4ActionReplyText,
		communication.V4ActionServiceReply,
		communication.V4ActionRejectionRetention,
		communication.V4ActionRejectionClosing,
		communication.V4ActionInterviewRejectionReply:
		return true
	default:
		return false
	}
}

func communicationV4AdvicePolicy(
	decision communication.V4InboundTurnDecision,
) (communication.V4InboundTurnDecision, *communication.V4PlannedAction, string) {
	if decision.ManualReason != "" || decision.Dialogue.Status == communication.V4DialogueManualRequired {
		reason := string(decision.ManualReason)
		if reason == "" {
			reason = string(decision.Dialogue.ManualReason)
		}
		return decision, nil, reason
	}
	if decision.Dialogue.Status != communication.V4DialogueActionsPlanned {
		return decision, nil, ""
	}
	if len(decision.Dialogue.Actions) > 1 {
		return decision, nil, communicationV4ManualMultiVisibleAction
	}
	if len(decision.Dialogue.Actions) == 0 {
		return decision, nil, communicationV4ManualUnsupportedAction
	}
	plan := decision.Dialogue.Actions[0]
	if !supportedCommunicationV4TextPlan(plan) {
		return decision, nil, communicationV4ManualUnsupportedAction
	}
	return decision, &plan, ""
}

func dialogueTurnStatusFromCommunicationV4Decision(
	decision communication.V4InboundTurnDecision,
	manualReason string,
) (DialogueTurnStatus, error) {
	if manualReason != "" {
		return DialogueTurnManualRequired, nil
	}
	switch decision.Dialogue.Status {
	case communication.V4DialogueWaitingAdvice:
		switch decision.Dialogue.NextAdvice {
		case communication.V4AdviceIntent:
			return DialogueTurnCollected, nil
		case communication.V4AdviceReply, communication.V4AdviceServiceReply,
			communication.V4AdviceInterviewRejectionReply:
			return DialogueTurnClassified, nil
		}
	case communication.V4DialogueActionsPlanned:
		return DialogueTurnAdviceReady, nil
	case communication.V4DialogueNoAction:
		return DialogueTurnCompleted, nil
	case communication.V4DialogueManualRequired:
		return DialogueTurnManualRequired, nil
	}
	return "", ErrDialogueTurnState
}

func persistCommunicationV4AdviceTx(
	tx *gorm.DB,
	turn *DialogueTurn,
	invocation AIInvocation,
	digest string,
	decision communication.V4InboundTurnDecision,
	at time.Time,
) (*CommunicationAction, error) {
	key := communicationV4DialogueAdviceKey(turn.TurnID, invocation.Purpose)
	existing, found, err := communicationV4ApplicationTx(
		tx,
		turn.ProfileID,
		CommunicationV4InputDialogueAdvice,
		key,
	)
	if err != nil {
		return nil, err
	}
	if found {
		if existing.InputDigest != digest ||
			existing.SemanticKind != string(invocation.Purpose) ||
			existing.MessageSeq != turn.InboundThroughSeq {
			return nil, ErrCommunicationV4Conflict
		}
		var action CommunicationAction
		err := tx.First(&action, "turn_id = ?", turn.TurnID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, tx.First(turn, "turn_id = ?", turn.TurnID).Error
		}
		if err != nil {
			return nil, err
		}
		if err := tx.First(turn, "turn_id = ?", turn.TurnID).Error; err != nil {
			return nil, err
		}
		return &action, nil
	}
	if err := validateDialogueTurnAIAdviceTx(tx, *turn, invocation.Purpose); err != nil {
		return nil, err
	}
	aggregate, err := communicationV4AggregateTx(tx, turn.ProfileID)
	if err != nil {
		return nil, err
	}
	decision, plan, manualReason := communicationV4AdvicePolicy(decision)
	status, err := dialogueTurnStatusFromCommunicationV4Decision(decision, manualReason)
	if err != nil {
		return nil, err
	}

	next := aggregate
	next.State = decision.State
	next.Revision++
	next.UpdatedAt = at
	if manualReason != "" {
		next.AutomationStatus = ProfileCommunicationAutomationManualRequired
		next.ManualReason = manualReason
		next.ManualRequiredAt = &at
	}
	application := CommunicationV4ProjectionApplication{
		ProfileID: turn.ProfileID, InputKind: CommunicationV4InputDialogueAdvice, InputKey: key,
		InputDigest: digest, SemanticKind: string(invocation.Purpose),
		MessageSeq: turn.InboundThroughSeq, FromRevision: aggregate.Revision, ToRevision: next.Revision,
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
		AppliedAt: at,
	}
	if err := persistCommunicationV4TransitionTx(tx, aggregate, next, application); err != nil {
		return nil, err
	}

	label := decision.Dialogue.IntentLabel
	source := dialogueIntentSourceFromV4(decision.Dialogue.IntentSource)
	classifiedAt := turn.ClassifiedAt
	if invocation.Purpose == m5ai.PurposeIntent && classifiedAt == nil {
		classifiedAt = &at
	}
	updates := map[string]any{
		"status": status, "intent_label": label, "intent_source": source,
		"classified_at": classifiedAt, "failure_reason": manualReason, "updated_at": at,
	}
	updated := tx.Model(&DialogueTurn{}).
		Where("turn_id = ? AND status = ?", turn.TurnID, turn.Status).
		Updates(updates)
	if updated.Error != nil {
		return nil, updated.Error
	}
	if updated.RowsAffected != 1 {
		return nil, ErrDialogueTurnConflict
	}
	if err := tx.First(turn, "turn_id = ?", turn.TurnID).Error; err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, nil
	}
	action := CommunicationAction{
		ActionID: plan.ActionKey, TurnID: turn.TurnID, Kind: CommunicationActionReplyText,
		Text: plan.Text, ContentHash: textcanon.Hash(plan.Text), Status: CommunicationActionPlanned,
		PlannedAt: at, CreatedAt: at, UpdatedAt: at,
	}
	if err := tx.Create(&action).Error; err != nil {
		return nil, err
	}
	return &action, nil
}

func completeCommunicationV4IntentTx(
	tx *gorm.DB,
	turn *DialogueTurn,
	invocation AIInvocation,
	label m5ai.IntentLabel,
	source DialogueIntentSource,
	manualReason string,
	at time.Time,
) error {
	digest, err := communicationV4AdviceDigest(
		*turn, invocation, label, source, "", manualReason,
	)
	if err != nil {
		return err
	}
	key := communicationV4DialogueAdviceKey(turn.TurnID, m5ai.PurposeIntent)
	if existing, found, err := communicationV4ApplicationTx(
		tx, turn.ProfileID, CommunicationV4InputDialogueAdvice, key,
	); err != nil {
		return err
	} else if found {
		if existing.InputDigest != digest {
			return ErrCommunicationV4Conflict
		}
		return tx.First(turn, "turn_id = ?", turn.TurnID).Error
	}
	if err := validateDialogueTurnAIAdviceTx(tx, *turn, m5ai.PurposeIntent); err != nil {
		return err
	}
	aggregate, err := communicationV4AggregateTx(tx, turn.ProfileID)
	if err != nil {
		return err
	}
	requirement, err := communicationV4InitialRequirementTx(tx, *turn)
	if err != nil {
		return err
	}
	var decision communication.V4InboundTurnDecision
	if manualReason != "" && manualReason != "intentRejected" {
		decision = manualCommunicationV4AdviceDecision(
			aggregate.State, requirement, manualReason, label, source,
		)
	} else {
		advice, err := communicationV4IntentAdvice(invocation, label, source, manualReason)
		if err != nil {
			return err
		}
		decision, err = communicationV4TurnReducerInputTx(
			tx, *turn, aggregate, advice, communication.ReplyAdvice{State: communication.AdviceAbsent},
		)
		if err != nil {
			return err
		}
	}
	_, err = persistCommunicationV4AdviceTx(tx, turn, invocation, digest, decision, at)
	return err
}

func completeCommunicationV4ReplyTx(
	tx *gorm.DB,
	turn *DialogueTurn,
	invocation AIInvocation,
	text string,
	contentHash string,
	manualReason string,
	at time.Time,
) (*CommunicationAction, error) {
	if manualReason == "" && invocation.Status == AIInvocationOK &&
		(strings.TrimSpace(text) == "" || contentHash != textcanon.Hash(text)) {
		return nil, ErrCommunicationActionInvalid
	}
	digest, err := communicationV4AdviceDigest(
		*turn, invocation, turn.IntentLabel, turn.IntentSource, text, manualReason,
	)
	if err != nil {
		return nil, err
	}
	key := communicationV4DialogueAdviceKey(turn.TurnID, m5ai.PurposeReply)
	if existing, found, err := communicationV4ApplicationTx(
		tx, turn.ProfileID, CommunicationV4InputDialogueAdvice, key,
	); err != nil {
		return nil, err
	} else if found {
		if existing.InputDigest != digest {
			return nil, ErrCommunicationV4Conflict
		}
		var action CommunicationAction
		err := tx.First(&action, "turn_id = ?", turn.TurnID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, tx.First(turn, "turn_id = ?", turn.TurnID).Error
		}
		if err != nil {
			return nil, err
		}
		return &action, nil
	}
	if err := validateDialogueTurnAIAdviceTx(tx, *turn, m5ai.PurposeReply); err != nil {
		return nil, err
	}
	aggregate, err := communicationV4AggregateTx(tx, turn.ProfileID)
	if err != nil {
		return nil, err
	}
	requirement, err := communicationV4InitialRequirementTx(tx, *turn)
	if err != nil {
		return nil, err
	}
	var decision communication.V4InboundTurnDecision
	if manualReason != "" {
		decision = manualCommunicationV4AdviceDecision(
			aggregate.State, requirement, manualReason, turn.IntentLabel, turn.IntentSource,
		)
	} else {
		intent, err := communicationV4IntentAdviceFromTurn(*turn)
		if err != nil {
			return nil, err
		}
		reply := communication.ReplyAdvice{State: communication.AdviceFailed}
		if invocation.Status == AIInvocationOK {
			reply = communication.ReplyAdvice{
				State:      communication.AdviceOK,
				Suggestion: m5ai.ReplySuggestion{Text: text},
			}
		}
		decision, err = communicationV4TurnReducerInputTx(tx, *turn, aggregate, intent, reply)
		if err != nil {
			return nil, err
		}
	}
	return persistCommunicationV4AdviceTx(tx, turn, invocation, digest, decision, at)
}
