package communication

import (
	"fmt"
	"strings"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

type V4DialogueDecisionStatus string

const (
	V4DialogueWaitingAdvice       V4DialogueDecisionStatus = "waitingAdvice"
	V4DialogueWaitingPrerequisite V4DialogueDecisionStatus = "waitingPrerequisite"
	V4DialogueActionsPlanned      V4DialogueDecisionStatus = "actionsPlanned"
	V4DialogueNoAction            V4DialogueDecisionStatus = "noAction"
	V4DialogueManualRequired      V4DialogueDecisionStatus = "manualRequired"
)

type V4AdvicePurpose string

const (
	V4AdviceNone                    V4AdvicePurpose = "none"
	V4AdviceIntent                  V4AdvicePurpose = "intent"
	V4AdviceReply                   V4AdvicePurpose = "reply"
	V4AdviceServiceReply            V4AdvicePurpose = "serviceReply"
	V4AdviceInterviewRejectionReply V4AdvicePurpose = "interviewRejectionReply"
)

const IntentSourceBusinessEvent IntentSource = "businessEvent"

const V4InterviewDurationMs int64 = 30 * 60 * 1000

const (
	V4ManualUnsupportedMedia       V4ManualReason = "unsupportedMedia"
	V4ManualUnsupportedSemantic    V4ManualReason = "unsupportedSemantic"
	V4ManualReplyFailed            V4ManualReason = "replyFailed"
	V4ManualReplyInvalid           V4ManualReason = "replyInvalid"
	V4ManualFixedPhraseUnavailable V4ManualReason = "fixedPhraseUnavailable"
	V4ManualWechatContinuation     V4ManualReason = "wechatContinuationManual"
)

type V4PlannedAction struct {
	ActionKey           string       `json:"actionKey"`
	Kind                V4ActionKind `json:"kind"`
	Text                string       `json:"text,omitempty"`
	CardMessageSeq      int64        `json:"cardMessageSeq,omitempty"`
	AnchorMessageSeq    int64        `json:"anchorMessageSeq,omitempty"`
	InterviewStartsAtMs *int64       `json:"interviewStartsAtMs,omitempty"`
	InterviewEndsAtMs   *int64       `json:"interviewEndsAtMs,omitempty"`
	InterviewMethod     *string      `json:"interviewMethod,omitempty"`
	Round               uint64       `json:"round,omitempty"`
	Stage               uint8        `json:"stage,omitempty"`
	EndReason           V4EndReason  `json:"endReason,omitempty"`
	DueAt               *time.Time   `json:"dueAt,omitempty"`
}

type V4DialogueInput struct {
	State                  V4State
	Requirement            V4DialogueRequirement
	Turn                   FrozenTurnFacts
	Intent                 IntentAdvice
	Reply                  ReplyAdvice
	FixedPhrases           V4FixedPhraseView
	CardMessageSeq         int64
	PrerequisitesConfirmed bool
}

type V4DialogueDecision struct {
	State        V4State
	Status       V4DialogueDecisionStatus
	IntentLabel  m5ai.IntentLabel
	IntentSource IntentSource
	NextAdvice   V4AdvicePurpose
	Actions      []V4PlannedAction
	ManualReason V4ManualReason
}

// ReduceV4Dialogue is the AI permission gate. The deterministic requirement
// selects the branch first; only waitingAdvice decisions authorize one model
// invocation. Fixed phrases, terminal/no-op branches and manual fallbacks never
// call AI merely to make the pipeline uniform.
func ReduceV4Dialogue(input V4DialogueInput) (V4DialogueDecision, error) {
	if err := validateV4State(input.State); err != nil || !validAdviceState(input.Intent.State) || !validAdviceState(input.Reply.State) {
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
	state := cloneV4State(input.State)

	switch input.Requirement {
	case V4DialogueNone:
		if input.Intent.State != AdviceAbsent || input.Reply.State != AdviceAbsent {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		return V4DialogueDecision{State: state, Status: V4DialogueNoAction, NextAdvice: V4AdviceNone}, nil
	case V4DialogueClassifyAndReply:
		if state.MainStatus != V4StatusCommunicating && state.MainStatus != V4StatusInvited {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		return reduceV4ClassifiedDialogue(input, state)
	case V4DialogueReplyKnownInterested, V4DialogueWechatContinuation:
		if state.MainStatus != V4StatusCommunicating && state.MainStatus != V4StatusInvited {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		if input.Intent.State != AdviceAbsent {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		if input.Requirement == V4DialogueWechatContinuation && !input.PrerequisitesConfirmed {
			if input.Reply.State != AdviceAbsent {
				return V4DialogueDecision{}, ErrInvalidV4StateTransition
			}
			return V4DialogueDecision{
				State: state, Status: V4DialogueWaitingPrerequisite, IntentLabel: m5ai.IntentInterested,
				IntentSource: IntentSourceBusinessEvent, NextAdvice: V4AdviceNone,
			}, nil
		}
		return reduceV4ReplyOnly(input, state, V4ActionReplyText, V4AdviceReply, m5ai.IntentInterested, IntentSourceBusinessEvent)
	case V4DialogueServiceReply:
		if state.MainStatus != V4StatusInterviewed || input.Intent.State != AdviceAbsent {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		return reduceV4ReplyOnly(input, state, V4ActionServiceReply, V4AdviceServiceReply, "", "")
	case V4DialogueInterviewRejectionReceipt:
		if input.Intent.State != AdviceAbsent || input.CardMessageSeq <= 0 ||
			(state.MainStatus != V4StatusCommunicating && state.MainStatus != V4StatusInvited) {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		index := exactV4InterviewGroup(state.InterviewGroups, input.CardMessageSeq)
		if index < 0 || !state.InterviewGroups[index].Rejected {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		if state.InterviewGroups[index].RejectionReceiptSent {
			return V4DialogueDecision{State: state, Status: V4DialogueNoAction, NextAdvice: V4AdviceNone}, nil
		}
		return reduceV4ReplyOnly(input, state, V4ActionInterviewRejectionReply, V4AdviceInterviewRejectionReply, "", "")
	default:
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
}

func reduceV4ClassifiedDialogue(input V4DialogueInput, state V4State) (V4DialogueDecision, error) {
	base, err := Reduce(ReduceInput{Turn: input.Turn, Intent: input.Intent, Reply: input.Reply})
	if err != nil {
		return V4DialogueDecision{}, err
	}
	switch base.TurnStatus {
	case TurnCollected:
		return V4DialogueDecision{State: state, Status: V4DialogueWaitingAdvice, NextAdvice: V4AdviceIntent}, nil
	case TurnClassified:
		return V4DialogueDecision{
			State: state, Status: V4DialogueWaitingAdvice, IntentLabel: base.IntentLabel,
			IntentSource: base.IntentSource, NextAdvice: V4AdviceReply,
		}, nil
	case TurnAdviceReady:
		actions, valid := planV4ReplyActions(
			state,
			input.Turn,
			base.Action.Text,
			input.Reply.Suggestion,
			true,
		)
		if !valid {
			return manualV4Dialogue(
				state,
				V4ManualReplyInvalid,
				base.IntentLabel,
				base.IntentSource,
			), nil
		}
		return V4DialogueDecision{
			State: state, Status: V4DialogueActionsPlanned, IntentLabel: base.IntentLabel,
			IntentSource: base.IntentSource, NextAdvice: V4AdviceNone,
			Actions: actions,
		}, nil
	case TurnManualRequired:
		if base.ManualReason == ManualIntentRejected {
			return reduceV4Rejected(input, state, base.IntentSource)
		}
		return manualV4Dialogue(state, mapV4ManualReason(base.ManualReason), base.IntentLabel, base.IntentSource), nil
	default:
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
}

func reduceV4Rejected(input V4DialogueInput, state V4State, source IntentSource) (V4DialogueDecision, error) {
	state.ColdPromptRemaining = 0
	state.ColdWechatRemaining = 0
	turnSeq := input.Turn.Messages[len(input.Turn.Messages)-1].Seq
	if state.RejectionTurnMessageSeq > turnSeq ||
		(state.RejectionTurnMessageSeq == turnSeq && state.RejectionTurnID != "" && state.RejectionTurnID != input.Turn.TurnID) {
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
	if state.RejectionTurnMessageSeq != turnSeq {
		state.RejectionTurnMessageSeq = turnSeq
		state.RejectionTurnID = input.Turn.TurnID
		state.RejectionStage = chooseV4RejectionStage(state)
	}

	switch state.RejectionStage {
	case V4RejectionStageRetention:
		phrase := input.FixedPhrases.Phrase(V4PhraseRejectionRetention)
		if phrase.State != V4PhraseAvailable {
			return manualV4Dialogue(state, V4ManualFixedPhraseUnavailable, m5ai.IntentRejected, source), nil
		}
		actions := []V4PlannedAction{{
			ActionKey: stableV4TurnActionKey(state.RejectionTurnID, V4ActionRejectionRetention, 0),
			Kind:      V4ActionRejectionRetention, Text: phrase.Text,
		}}
		if state.WechatState == V4WechatNotInvited {
			actions = append(actions, V4PlannedAction{
				ActionKey: stableV4TurnActionKey(state.RejectionTurnID, V4ActionInviteWechat, 0),
				Kind:      V4ActionInviteWechat,
			})
		}
		return V4DialogueDecision{
			State: state, Status: V4DialogueActionsPlanned, IntentLabel: m5ai.IntentRejected,
			IntentSource: source, NextAdvice: V4AdviceNone, Actions: actions,
		}, nil
	case V4RejectionStageClosing:
		phrase := input.FixedPhrases.Phrase(V4PhraseRejectionClosing)
		if phrase.State != V4PhraseAvailable {
			return manualV4Dialogue(state, V4ManualFixedPhraseUnavailable, m5ai.IntentRejected, source), nil
		}
		return V4DialogueDecision{
			State: state, Status: V4DialogueActionsPlanned, IntentLabel: m5ai.IntentRejected,
			IntentSource: source, NextAdvice: V4AdviceNone,
			Actions: []V4PlannedAction{{
				ActionKey: stableV4TurnActionKey(state.RejectionTurnID, V4ActionRejectionClosing, 0),
				Kind:      V4ActionRejectionClosing, Text: phrase.Text,
			}},
		}, nil
	case V4RejectionStageArchive:
		archiveV4State(&state, V4EndRejected)
		return V4DialogueDecision{
			State: state, Status: V4DialogueNoAction, IntentLabel: m5ai.IntentRejected,
			IntentSource: source, NextAdvice: V4AdviceNone,
		}, nil
	default:
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
}

func chooseV4RejectionStage(state V4State) V4RejectionStage {
	if !state.RetentionSent {
		return V4RejectionStageRetention
	}
	if !state.ClosingSent {
		return V4RejectionStageClosing
	}
	return V4RejectionStageArchive
}

func reduceV4ReplyOnly(
	input V4DialogueInput,
	state V4State,
	actionKind V4ActionKind,
	purpose V4AdvicePurpose,
	label m5ai.IntentLabel,
	source IntentSource,
) (V4DialogueDecision, error) {
	if strings.TrimSpace(input.Turn.TurnID) == "" {
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
	switch input.Reply.State {
	case AdviceAbsent:
		return V4DialogueDecision{
			State: state, Status: V4DialogueWaitingAdvice, IntentLabel: label,
			IntentSource: source, NextAdvice: purpose,
		}, nil
	case AdviceFailed:
		return manualV4Dialogue(state, V4ManualReplyFailed, label, source), nil
	case AdviceOK:
		if err := m5ai.ValidateSendText(input.Reply.Suggestion.Text); err != nil {
			return manualV4Dialogue(state, V4ManualReplyInvalid, label, source), nil
		}
		actions, valid := planV4ReplyActions(
			state,
			input.Turn,
			input.Reply.Suggestion.Text,
			input.Reply.Suggestion,
			purpose == V4AdviceReply,
		)
		if !valid {
			return manualV4Dialogue(state, V4ManualReplyInvalid, label, source), nil
		}
		if len(actions) == 0 || actions[0].Kind != V4ActionReplyText {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		actions[0].Kind = actionKind
		actions[0].ActionKey = stableV4TurnActionKey(
			input.Turn.TurnID,
			actionKind,
			input.CardMessageSeq,
		)
		actions[0].CardMessageSeq = input.CardMessageSeq
		return V4DialogueDecision{
			State: state, Status: V4DialogueActionsPlanned, IntentLabel: label,
			IntentSource: source, NextAdvice: V4AdviceNone,
			Actions: actions,
		}, nil
	default:
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
}

func planV4ReplyActions(
	state V4State,
	turn FrozenTurnFacts,
	text string,
	suggestion m5ai.ReplySuggestion,
	allowSuggestedAction bool,
) ([]V4PlannedAction, bool) {
	textPlan := V4PlannedAction{
		ActionKey: stableV4TurnActionKey(turn.TurnID, V4ActionReplyText, 0),
		Kind:      V4ActionReplyText,
		Text:      text,
	}
	switch suggestion.Action {
	case m5ai.ReplyActionNone:
		if suggestion.MeetingTime != "" {
			return nil, false
		}
		return []V4PlannedAction{textPlan}, true
	case m5ai.ReplyActionInviteWechat:
		if !allowSuggestedAction || suggestion.MeetingTime != "" ||
			!v4ReplyActionEligible(state, turn) ||
			state.WechatState != V4WechatNotInvited {
			return nil, false
		}
		return []V4PlannedAction{
			textPlan,
			{
				ActionKey: stableV4TurnActionKey(turn.TurnID, V4ActionInviteWechat, 0),
				Kind:      V4ActionInviteWechat,
			},
		}, true
	case m5ai.ReplyActionStartOnlineMeeting:
		if !allowSuggestedAction || !v4ReplyActionEligible(state, turn) {
			return nil, false
		}
		startsAt, matched := m5ai.MatchFrozenRecommendedMeetingTime(
			turn.RecommendedSlots,
			suggestion.MeetingTime,
		)
		if !matched {
			return nil, false
		}
		endsAt := startsAt + V4InterviewDurationMs
		method := "wechatVideo"
		return []V4PlannedAction{
			textPlan,
			{
				ActionKey:           stableV4TurnActionKey(turn.TurnID, V4ActionInterviewInvite, 0),
				Kind:                V4ActionInterviewInvite,
				InterviewStartsAtMs: &startsAt,
				InterviewEndsAtMs:   &endsAt,
				InterviewMethod:     &method,
			},
		}, true
	default:
		return nil, false
	}
}

func v4ReplyActionEligible(state V4State, turn FrozenTurnFacts) bool {
	if state.MainStatus != V4StatusCommunicating &&
		state.MainStatus != V4StatusInvited {
		return false
	}
	for _, message := range turn.Messages {
		switch message.Kind {
		case FrozenMessageText, FrozenMessageImage, FrozenMessageVoice,
			FrozenMessageFile, FrozenMessageCard:
			return true
		}
	}
	return false
}

func stableV4TurnActionKey(turnID string, kind V4ActionKind, cardMessageSeq int64) string {
	if cardMessageSeq > 0 {
		return fmt.Sprintf("%s|%s|card:%d", turnID, kind, cardMessageSeq)
	}
	return fmt.Sprintf("%s|%s", turnID, kind)
}

func manualV4Dialogue(state V4State, reason V4ManualReason, label m5ai.IntentLabel, source IntentSource) V4DialogueDecision {
	return V4DialogueDecision{
		State: cloneV4State(state), Status: V4DialogueManualRequired,
		IntentLabel: label, IntentSource: source, NextAdvice: V4AdviceNone, ManualReason: reason,
	}
}

func mapV4ManualReason(reason ManualReason) V4ManualReason {
	switch reason {
	case ManualUnsupportedMedia:
		return V4ManualUnsupportedMedia
	case ManualUnsupportedSemantic:
		return V4ManualUnsupportedSemantic
	case ManualReplyFailed:
		return V4ManualReplyFailed
	case ManualReplyInvalid:
		return V4ManualReplyInvalid
	default:
		return V4ManualInvalidTransition
	}
}
