package store

import (
	"encoding/json"
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
	PhraseHashes []string               `json:"phraseHashes,omitempty"`
	ReplyAction  m5ai.ReplyAction       `json:"replyAction,omitempty"`
	MeetingTime  string                 `json:"meetingTime,omitempty"`
	ManualReason string                 `json:"manualReason,omitempty"`
}

func communicationV4AdviceDigest(
	turn DialogueTurn,
	invocation AIInvocation,
	label m5ai.IntentLabel,
	source DialogueIntentSource,
	reply m5ai.ReplySuggestion,
	manualReason string,
) (string, error) {
	var phraseHashes []string
	if len(reply.Phrases) > 0 {
		phraseHashes = make([]string, len(reply.Phrases))
		for index, phrase := range reply.Phrases {
			phraseHashes[index] = textcanon.Hash(phrase)
		}
	}
	return communicationV4InputDigest(communicationV4AdviceDigestInput{
		TurnID: turn.TurnID, InvocationID: invocation.InvocationID,
		Purpose: invocation.Purpose, Status: invocation.Status, OutputHash: invocation.OutputHash,
		IntentLabel: label, IntentSource: source, TextHash: textcanon.Hash(reply.Text),
		PhraseHashes: phraseHashes, ReplyAction: reply.Action,
		MeetingTime: reply.MeetingTime, ManualReason: manualReason,
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
	lastOutbound, inbound, facts, _, err := reconstructCommunicationV4TurnBoundaryTx(
		tx, profile, turn.ConversationRef, turn.InboundFromSeq, turn.InboundThroughSeq,
	)
	if err != nil {
		return communication.V4InboundTurnDecision{}, err
	}
	digest, turnID, err := communicationV4TurnIdentity(
		aggregate, turn.ProfileID, lastOutbound, inbound,
	)
	if err != nil || digest != turn.InputDigest || turnID != turn.TurnID {
		return communication.V4InboundTurnDecision{}, ErrDialogueTurnBinding
	}
	recommendedSlots, _ := m5ai.FrozenRecommendedSlots(turn.RecommendedTimeText)
	var revision JobAIContextRevision
	if err := tx.First(&revision, "revision_hash = ?", turn.ContextRevisionHash).Error; err != nil {
		return communication.V4InboundTurnDecision{}, ErrDialogueTurnBinding
	}
	fixedPhrases, err := communication.BuildV4FixedPhraseView(revision.SourcePackage)
	if err != nil {
		return communication.V4InboundTurnDecision{}, ErrDialogueTurnBinding
	}
	prerequisitesConfirmed := false
	if requirement == communication.V4DialogueWechatContinuation {
		head, found, err := communicationV4TurnHeadApplicationTx(tx, turn)
		if err != nil {
			return communication.V4InboundTurnDecision{}, err
		}
		prerequisitesConfirmed = found &&
			head.InputKind == CommunicationV4InputConfirmedAction &&
			head.Outcome.Dialogue == communication.V4DialogueWechatContinuation &&
			head.Outcome.DialogueStatus == communication.V4DialogueWaitingAdvice &&
			head.Outcome.NextAdvice == communication.V4AdviceReply
	}
	decision, err := communication.ReduceV4InboundTurn(communication.V4InboundTurnInput{
		State: aggregate.State, TurnID: turn.TurnID, Messages: facts,
		RecommendedSlots: recommendedSlots,
		Intent:           intent, Reply: reply, FixedPhrases: fixedPhrases,
		PrerequisitesConfirmed: prerequisitesConfirmed,
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
	return supportedCommunicationV4TextKind(plan.Kind) &&
		plan.InterviewStartsAtMs == nil &&
		plan.InterviewEndsAtMs == nil &&
		plan.InterviewMethod == nil
}

func supportedCommunicationV4TextKind(kind communication.V4ActionKind) bool {
	switch kind {
	case communication.V4ActionReplyText,
		communication.V4ActionServiceReply,
		communication.V4ActionRejectionRetention,
		communication.V4ActionRejectionClosing,
		communication.V4ActionWechatReceipt,
		communication.V4ActionInterviewAcceptedReceipt,
		communication.V4ActionInterviewRejectionReply:
		return true
	default:
		return false
	}
}

func communicationV4AdvicePolicy(
	decision communication.V4InboundTurnDecision,
) (communication.V4InboundTurnDecision, []communication.V4PlannedAction, string) {
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
	if len(decision.Dialogue.Actions) == 0 {
		return decision, nil, communicationV4ManualUnsupportedAction
	}
	plans := append([]communication.V4PlannedAction(nil), decision.Dialogue.Actions...)
	actionKeys := make(map[string]struct{}, len(plans))
	texts := make([]string, 0, len(plans))
	var textKind communication.V4ActionKind
	for index, plan := range plans {
		if _, duplicate := actionKeys[plan.ActionKey]; strings.TrimSpace(plan.ActionKey) == "" || duplicate {
			return decision, nil, communicationV4ManualMultiVisibleAction
		}
		actionKeys[plan.ActionKey] = struct{}{}
		if supportedCommunicationV4TextPlan(plan) {
			if len(texts) >= m5ai.ReplyPhraseMaxItems ||
				(len(texts) > 0 && plan.Kind != textKind) {
				return decision, nil, communicationV4ManualMultiVisibleAction
			}
			if err := m5ai.ValidateSendText(plan.Text); err != nil {
				return decision, nil, communicationV4ManualUnsupportedAction
			}
			if len(texts) == 0 {
				textKind = plan.Kind
			}
			texts = append(texts, plan.Text)
			continue
		}
		if index != len(plans)-1 || len(texts) == 0 ||
			!supportedCommunicationV4CardPlan(plan) ||
			!approvedCommunicationV4VisibleCombination(textKind, plan.Kind) {
			return decision, nil, communicationV4ManualMultiVisibleAction
		}
	}
	if len(texts) == 0 {
		return decision, nil, communicationV4ManualUnsupportedAction
	}
	if err := m5ai.ValidateSendText(strings.Join(texts, "\n")); err != nil {
		return decision, nil, communicationV4ManualUnsupportedAction
	}
	return decision, plans, ""
}

func communicationV4ReplyPhrases(
	plans []communication.V4PlannedAction,
) []string {
	phrases := make([]string, 0, len(plans))
	for _, plan := range plans {
		if !supportedCommunicationV4TextKind(plan.Kind) {
			break
		}
		phrases = append(phrases, plan.Text)
	}
	return phrases
}

func supportedCommunicationV4CardPlan(plan communication.V4PlannedAction) bool {
	if strings.TrimSpace(plan.ActionKey) == "" || plan.Text != "" ||
		plan.CardMessageSeq != 0 || plan.Round != 0 || plan.Stage != 0 || plan.EndReason != "" {
		return false
	}
	switch plan.Kind {
	case communication.V4ActionInviteWechat:
		return plan.InterviewStartsAtMs == nil &&
			plan.InterviewEndsAtMs == nil &&
			plan.InterviewMethod == nil
	case communication.V4ActionInterviewInvite:
		return plan.InterviewStartsAtMs != nil &&
			plan.InterviewEndsAtMs != nil &&
			plan.InterviewMethod != nil &&
			*plan.InterviewStartsAtMs > 0 &&
			*plan.InterviewEndsAtMs ==
				*plan.InterviewStartsAtMs+communication.V4InterviewDurationMs &&
			*plan.InterviewMethod == "wechatVideo"
	default:
		return false
	}
}

func approvedCommunicationV4VisibleCombination(
	textKind communication.V4ActionKind,
	cardKind communication.V4ActionKind,
) bool {
	switch cardKind {
	case communication.V4ActionInviteWechat:
		switch textKind {
		case communication.V4ActionReplyText,
			communication.V4ActionRejectionRetention,
			communication.V4ActionColdWechatText,
			communication.V4ActionInterviewAcceptedReceipt:
			return true
		}
	case communication.V4ActionInterviewInvite:
		return textKind == communication.V4ActionReplyText
	}
	return false
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
		err := tx.Where("turn_id = ?", turn.TurnID).
			Order("planned_at, created_at, action_id").
			First(&action).Error
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
	decision, plans, manualReason := communicationV4AdvicePolicy(decision)
	if len(plans) > 0 {
		rendered, ready, err := materializeCommunicationV4FixedTextPlansTx(
			tx,
			turn.ProfileID,
			plans,
		)
		if err != nil {
			return nil, err
		}
		if !ready {
			manualReason = string(communication.V4ManualFixedPhraseUnavailable)
			plans = nil
			decision.ManualReason = communication.V4ManualFixedPhraseUnavailable
			decision.Dialogue.Status = communication.V4DialogueManualRequired
			decision.Dialogue.NextAdvice = communication.V4AdviceNone
			decision.Dialogue.ManualReason = communication.V4ManualFixedPhraseUnavailable
			decision.Dialogue.Actions = nil
		} else {
			plans = rendered
			decision.Dialogue.Actions = append(
				[]communication.V4PlannedAction(nil),
				rendered...,
			)
		}
	}
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
			Dialogue:             decision.Requirement,
			DialogueAfterActions: decision.DialogueAfterActions,
			Actions:              append([]communication.V4EventAction(nil), decision.EventActions...),
			ManualReason:         communication.V4ManualReason(manualReason),
			DialogueStatus:       decision.Dialogue.Status,
			NextAdvice:           decision.Dialogue.NextAdvice,
			IntentLabel:          decision.Dialogue.IntentLabel,
			IntentSource:         decision.Dialogue.IntentSource,
			PlannedActions:       redactedCommunicationV4Plans(decision.Dialogue.Actions),
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
	if phrases := communicationV4ReplyPhrases(plans); len(phrases) > 0 {
		encoded, err := json.Marshal(phrases)
		if err != nil {
			return nil, err
		}
		updates["reply_phrases"] = string(encoded)
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
	if len(plans) == 0 {
		return nil, nil
	}
	plan := plans[0]
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

func materializeCommunicationV4FixedTextPlanTx(
	tx *gorm.DB,
	profileID string,
	plan communication.V4PlannedAction,
) (communication.V4PlannedAction, bool, error) {
	switch plan.Kind {
	case communication.V4ActionRejectionRetention,
		communication.V4ActionRejectionClosing:
	default:
		return plan, true, nil
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		return communication.V4PlannedAction{}, false, err
	}
	salutation, err := communicationV4ProfileSalutationTx(tx, profile)
	if err != nil {
		return communication.V4PlannedAction{}, false, err
	}
	rendered, err := communication.RenderV4FixedPhrase(
		plan.Text,
		communication.V4FixedPhraseRenderInput{Salutation: salutation},
	)
	if err != nil {
		return communication.V4PlannedAction{}, false, nil
	}
	plan.Text = rendered
	return plan, true, nil
}

func materializeCommunicationV4FixedTextPlansTx(
	tx *gorm.DB,
	profileID string,
	plans []communication.V4PlannedAction,
) ([]communication.V4PlannedAction, bool, error) {
	rendered := append([]communication.V4PlannedAction(nil), plans...)
	for index := range rendered {
		plan, ready, err := materializeCommunicationV4FixedTextPlanTx(
			tx,
			profileID,
			rendered[index],
		)
		if err != nil {
			return nil, false, err
		}
		if !ready {
			return nil, false, nil
		}
		rendered[index] = plan
	}
	return rendered, true, nil
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
		*turn, invocation, label, source, m5ai.ReplySuggestion{}, manualReason,
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
	reply m5ai.ReplySuggestion,
	contentHash string,
	manualReason string,
	at time.Time,
) (*CommunicationAction, error) {
	if manualReason == "" && invocation.Status == AIInvocationOK {
		_, text, err := m5ai.CanonicalReplyPhrases(reply)
		if err != nil || contentHash != textcanon.Hash(text) {
			return nil, ErrCommunicationActionInvalid
		}
	}
	digest, err := communicationV4AdviceDigest(
		*turn, invocation, turn.IntentLabel, turn.IntentSource, reply, manualReason,
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
		err := tx.Where("turn_id = ?", turn.TurnID).
			Order("planned_at, created_at, action_id").
			First(&action).Error
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
		var intent communication.IntentAdvice
		switch requirement {
		case communication.V4DialogueServiceReply:
			// This branch is selected by deterministic business state and
			// deliberately skips intent classification.
			intent = communication.IntentAdvice{State: communication.AdviceAbsent}
		default:
			intent, err = communicationV4IntentAdviceFromTurn(*turn)
			if err != nil {
				return nil, err
			}
		}
		replyAdvice := communication.ReplyAdvice{State: communication.AdviceFailed}
		if invocation.Status == AIInvocationOK {
			replyAdvice = communication.ReplyAdvice{
				State:      communication.AdviceOK,
				Suggestion: reply,
			}
		}
		decision, err = communicationV4TurnReducerInputTx(
			tx,
			*turn,
			aggregate,
			intent,
			replyAdvice,
		)
		if err != nil {
			return nil, err
		}
	}
	return persistCommunicationV4AdviceTx(tx, turn, invocation, digest, decision, at)
}
