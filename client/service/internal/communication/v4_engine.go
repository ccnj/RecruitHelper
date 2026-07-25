package communication

import (
	"strings"

	"recruithelper/client/service/internal/m5ai"
)

// V4InboundTurnInput is the narrow replay boundary for one contiguous inbound
// candidate turn. The ledger facts remain platform-neutral; provider advice is
// optional and may only be consumed if the deterministic branch asks for it.
type V4InboundTurnInput struct {
	State                  V4State
	TurnID                 string
	Messages               []LedgerMessageFact
	RecommendedSlots       []string
	Intent                 IntentAdvice
	Reply                  ReplyAdvice
	FixedPhrases           V4FixedPhraseView
	PrerequisitesConfirmed bool
}

type V4InboundTurnDecision struct {
	State                V4State
	Requirement          V4DialogueRequirement
	DialogueAfterActions bool

	EventActions []V4EventAction
	Dialogue     V4DialogueDecision
	ManualReason V4ManualReason
}

// ReduceV4InboundTurn closes the pure vertical slice:
// neutral ledger -> normalized events -> v4 state -> optional AI/action plan.
// Multiple ordinary messages in the contiguous segment open exactly one cold
// counting round. Mixed special events are kept conservative until a richer
// turn-envelope fact is introduced.
func ReduceV4InboundTurn(input V4InboundTurnInput) (V4InboundTurnDecision, error) {
	if err := validateV4State(input.State); err != nil || strings.TrimSpace(input.TurnID) == "" || len(input.Messages) == 0 ||
		!validAdviceState(input.Intent.State) || !validAdviceState(input.Reply.State) {
		return V4InboundTurnDecision{}, ErrInvalidV4StateTransition
	}

	frozen := FrozenTurnFacts{
		TurnID:           input.TurnID,
		RecommendedSlots: append([]string(nil), input.RecommendedSlots...),
	}
	var ordinaryEvents []BusinessEvent
	var specialEvents []BusinessEvent
	hasUnknown := false
	previousSeq := int64(0)
	for _, fact := range input.Messages {
		if fact.Seq <= previousSeq || (fact.Direction != "in" && fact.Direction != "system") {
			return V4InboundTurnDecision{}, ErrInvalidV4StateTransition
		}
		previousSeq = fact.Seq
		event, err := NormalizeLedgerMessage(fact)
		if err != nil {
			return V4InboundTurnDecision{}, err
		}
		switch event.Kind {
		case EventCandidateExpressionReceived:
			ordinaryEvents = append(ordinaryEvents, event)
			frozen.Messages = append(frozen.Messages, frozenInboundFromEvent(event))
		case EventResumeSubmitted, EventWechatRequested, EventWechatExchanged,
			EventInterviewAccepted:
			specialEvents = append(specialEvents, event)
			frozen.Messages = append(frozen.Messages, FrozenInboundMessage{Seq: event.MessageSeq, Kind: FrozenMessageCard})
		case EventSystemNotice:
			// System rows remain in the ledger but do not enter a candidate turn.
		case EventUnknownPlatform:
			hasUnknown = true
		default:
			return V4InboundTurnDecision{}, ErrInvalidV4StateTransition
		}
	}

	if len(ordinaryEvents) == 0 && len(specialEvents) == 0 {
		if hasUnknown {
			decision := manualV4InboundTurn(input.State, V4ManualUnknownPlatformEvent)
			decision.Requirement = V4DialogueNone
			return decision, nil
		}
		dialogue, err := ReduceV4Dialogue(V4DialogueInput{
			State: input.State, Requirement: V4DialogueNone,
			Intent: input.Intent, Reply: input.Reply,
		})
		if err != nil {
			return V4InboundTurnDecision{}, err
		}
		return V4InboundTurnDecision{
			State: dialogue.State, Requirement: V4DialogueNone, Dialogue: dialogue,
		}, nil
	}

	if len(specialEvents) > 1 || (len(specialEvents) == 1 && len(ordinaryEvents) > 0) {
		state, err := applyV4AggregateOrdinaryTurn(input.State, input.TurnID, frozen.Messages[len(frozen.Messages)-1].Seq)
		if err != nil {
			return V4InboundTurnDecision{}, err
		}
		decision := manualV4InboundTurn(state, V4ManualUnsupportedSemantic)
		decision.Requirement = V4DialogueNone
		return decision, nil
	}

	var event BusinessEvent
	if len(specialEvents) == 1 {
		event = specialEvents[0]
	} else {
		event = BusinessEvent{
			Key: "turn:" + input.TurnID, Kind: EventCandidateExpressionReceived,
			Source: EventSourceMessage, MessageSeq: ordinaryEvents[len(ordinaryEvents)-1].MessageSeq,
		}
	}
	eventDecision, err := ApplyV4BusinessEvent(input.State, event)
	if err != nil {
		return V4InboundTurnDecision{}, err
	}
	if eventDecision.ManualReason != "" {
		decision := manualV4InboundTurn(eventDecision.State, eventDecision.ManualReason)
		decision.Requirement = eventDecision.Dialogue
		return decision, nil
	}
	if hasUnknown {
		return V4InboundTurnDecision{
			State:        eventDecision.State,
			Requirement:  eventDecision.Dialogue,
			Dialogue:     manualV4Dialogue(eventDecision.State, V4ManualUnknownPlatformEvent, "", ""),
			ManualReason: V4ManualUnknownPlatformEvent,
		}, nil
	}
	if receipt, handled := v4ReceiptDialogue(
		eventDecision.State,
		event.Kind,
		eventDecision.Actions,
		input.FixedPhrases,
	); handled {
		return V4InboundTurnDecision{
			State: eventDecision.State, Requirement: eventDecision.Dialogue,
			DialogueAfterActions: eventDecision.DialogueAfterActions,
			EventActions:         append([]V4EventAction(nil), eventDecision.Actions...),
			Dialogue:             receipt, ManualReason: receipt.ManualReason,
		}, nil
	}

	dialogue, err := ReduceV4Dialogue(V4DialogueInput{
		State: eventDecision.State, Requirement: eventDecision.Dialogue, Turn: frozen,
		Intent: input.Intent, Reply: input.Reply, FixedPhrases: input.FixedPhrases,
		CardMessageSeq: event.MessageSeq, PrerequisitesConfirmed: input.PrerequisitesConfirmed,
	})
	if err != nil {
		return V4InboundTurnDecision{}, err
	}
	return V4InboundTurnDecision{
		State: dialogue.State, Requirement: eventDecision.Dialogue,
		DialogueAfterActions: eventDecision.DialogueAfterActions,
		EventActions:         append([]V4EventAction(nil), eventDecision.Actions...),
		Dialogue:             dialogue, ManualReason: dialogue.ManualReason,
	}, nil
}

func v4ReceiptDialogue(
	state V4State,
	eventKind BusinessEventKind,
	actions []V4EventAction,
	phrases V4FixedPhraseView,
) (V4DialogueDecision, bool) {
	var actionKind V4ActionKind
	var phraseKind V4FixedPhraseKind
	switch eventKind {
	case EventWechatExchanged:
		actionKind = V4ActionWechatReceipt
		phraseKind = V4PhraseWechatReceipt
	case EventInterviewAccepted:
		actionKind = V4ActionInterviewAcceptedReceipt
		phraseKind = V4PhraseInterviewAccepted
	default:
		return V4DialogueDecision{}, false
	}
	var selected *V4EventAction
	for index := range actions {
		if actions[index].Kind != actionKind {
			continue
		}
		copy := actions[index]
		selected = &copy
		break
	}
	if selected == nil {
		return V4DialogueDecision{}, false
	}
	phrase := phrases.Phrase(phraseKind)
	if phrase.State != V4PhraseAvailable || m5ai.ValidateSendText(phrase.Text) != nil {
		return manualV4Dialogue(state, V4ManualFixedPhraseUnavailable, "", ""), true
	}
	return V4DialogueDecision{
		State: state, Status: V4DialogueNoAction, NextAdvice: V4AdviceNone,
	}, true
}

func applyV4AggregateOrdinaryTurn(state V4State, turnID string, lastMessageSeq int64) (V4State, error) {
	decision, err := ApplyV4BusinessEvent(state, BusinessEvent{
		Key: "turn:" + turnID, Kind: EventCandidateExpressionReceived,
		Source: EventSourceMessage, MessageSeq: lastMessageSeq,
	})
	if err != nil {
		return V4State{}, err
	}
	return decision.State, nil
}

func frozenInboundFromEvent(event BusinessEvent) FrozenInboundMessage {
	kind := FrozenMessageKind("")
	switch event.ExpressionKind {
	case ExpressionText:
		kind = FrozenMessageText
	case ExpressionImage:
		kind = FrozenMessageImage
	case ExpressionVoice:
		kind = FrozenMessageVoice
	case ExpressionFile:
		kind = FrozenMessageFile
	default:
		kind = FrozenMessageSystem
	}
	return FrozenInboundMessage{Seq: event.MessageSeq, Kind: kind, Text: event.Text}
}

func manualV4InboundTurn(state V4State, reason V4ManualReason) V4InboundTurnDecision {
	dialogue := manualV4Dialogue(state, reason, "", "")
	return V4InboundTurnDecision{
		State: dialogue.State, Dialogue: dialogue, ManualReason: reason,
	}
}
