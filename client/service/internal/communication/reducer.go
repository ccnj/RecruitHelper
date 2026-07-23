// Package communication contains the pure deterministic M5 communication
// reducer. It neither persists facts nor calls a provider or browser primitive.
package communication

import (
	"errors"
	"strings"

	"recruithelper/client/service/internal/m5ai"
)

var ErrInvalidReducerInput = errors.New("M5 沟通 reducer 输入无效")

type TurnStatus string

const (
	TurnCollected      TurnStatus = "collected"
	TurnClassified     TurnStatus = "classified"
	TurnAdviceReady    TurnStatus = "adviceReady"
	TurnManualRequired TurnStatus = "manualRequired"
)

type IntentSource string

const (
	IntentSourceCodeShortCircuit   IntentSource = "codeShortCircuit"
	IntentSourceLLM                IntentSource = "llm"
	IntentSourceLLMFailureFallback IntentSource = "llmFailureFallback"
)

type AdviceState string

const (
	AdviceAbsent AdviceState = "absent"
	AdviceOK     AdviceState = "ok"
	AdviceFailed AdviceState = "failed"
)

type FrozenMessageKind string

const (
	FrozenMessageText   FrozenMessageKind = "text"
	FrozenMessageImage  FrozenMessageKind = "image"
	FrozenMessageVoice  FrozenMessageKind = "voice"
	FrozenMessageFile   FrozenMessageKind = "file"
	FrozenMessageCard   FrozenMessageKind = "card"
	FrozenMessageSystem FrozenMessageKind = "system"
)

type ManualReason string

const (
	ManualUnsupportedMedia    ManualReason = "unsupportedMedia"
	ManualUnsupportedSemantic ManualReason = "unsupportedSemantic"
	ManualIntentRejected      ManualReason = "intentRejected"
	ManualReplyFailed         ManualReason = "replyFailed"
	ManualReplyInvalid        ManualReason = "replyInvalid"
)

type CommunicationActionKind string

const CommunicationActionReplyText CommunicationActionKind = "replyText"

type CommunicationActionStatus string

const CommunicationActionPlanned CommunicationActionStatus = "planned"

type FrozenInboundMessage struct {
	Seq  int64
	Kind FrozenMessageKind
	Text string
}

// FrozenTurnFacts contains only the already-frozen, platform-independent turn
// facts needed by the reducer. Snapshot/context/rendering validity is proved by
// the freezing transaction and provider adapter before advice reaches here.
type FrozenTurnFacts struct {
	TurnID           string
	Messages         []FrozenInboundMessage
	RecommendedSlots []string
}

type IntentAdvice struct {
	State      AdviceState
	Suggestion m5ai.IntentSuggestion
}

type ReplyAdvice struct {
	State      AdviceState
	Suggestion m5ai.ReplySuggestion
}

type ReduceInput struct {
	Turn   FrozenTurnFacts
	Intent IntentAdvice
	Reply  ReplyAdvice
}

// CommunicationActionPlan is the only business action this reducer can
// produce. ActionID and contentHash are deterministic/persistent adapter
// concerns; the reducer cannot create an EffectIntent or command.
type CommunicationActionPlan struct {
	TurnID string
	Kind   CommunicationActionKind
	Text   string
	Status CommunicationActionStatus
}

type Decision struct {
	TurnID       string
	TurnStatus   TurnStatus
	IntentLabel  m5ai.IntentLabel
	IntentSource IntentSource
	NextAdvice   m5ai.CompletionPurpose
	ManualReason ManualReason
	Action       *CommunicationActionPlan
}

// Reduce maps one immutable turn plus already-available advice facts to one
// deterministic state. AdviceAbsent means the orchestrator may make that one
// approved provider call; it is not itself an instruction to perform I/O.
func Reduce(input ReduceInput) (Decision, error) {
	texts, manualReason, err := validateFrozenTurn(input.Turn)
	if err != nil {
		return Decision{}, err
	}
	if manualReason != "" {
		return manualDecision(input.Turn.TurnID, manualReason, "", ""), nil
	}
	if !validAdviceState(input.Intent.State) || !validAdviceState(input.Reply.State) {
		return Decision{}, ErrInvalidReducerInput
	}

	shortCircuit := m5ai.ClassifyIntentShortCircuitV4(texts)
	label := m5ai.IntentLabel("")
	source := IntentSource("")
	if shortCircuit.Matched {
		if input.Intent.State != AdviceAbsent || !validIntentLabel(shortCircuit.Label) {
			return Decision{}, ErrInvalidReducerInput
		}
		label = shortCircuit.Label
		source = IntentSourceCodeShortCircuit
	} else {
		switch input.Intent.State {
		case AdviceAbsent:
			if input.Reply.State != AdviceAbsent {
				return Decision{}, ErrInvalidReducerInput
			}
			return Decision{
				TurnID: input.Turn.TurnID, TurnStatus: TurnCollected,
				NextAdvice: m5ai.PurposeIntent,
			}, nil
		case AdviceFailed:
			label = m5ai.IntentNeutral
			source = IntentSourceLLMFailureFallback
		case AdviceOK:
			if validIntentLabel(input.Intent.Suggestion.Label) {
				label = input.Intent.Suggestion.Label
				source = IntentSourceLLM
			} else {
				// 非法 provider 输出与调用失败共享获批的 neutral fallback，
				// 不把解析错误升级成第二次意向调用。
				label = m5ai.IntentNeutral
				source = IntentSourceLLMFailureFallback
			}
		}
	}

	if label == m5ai.IntentRejected {
		return manualDecision(input.Turn.TurnID, ManualIntentRejected, label, source), nil
	}
	if label != m5ai.IntentInterested && label != m5ai.IntentNeutral {
		return Decision{}, ErrInvalidReducerInput
	}

	switch input.Reply.State {
	case AdviceAbsent:
		return Decision{
			TurnID: input.Turn.TurnID, TurnStatus: TurnClassified,
			IntentLabel: label, IntentSource: source, NextAdvice: m5ai.PurposeReply,
		}, nil
	case AdviceFailed:
		return manualDecision(input.Turn.TurnID, ManualReplyFailed, label, source), nil
	case AdviceOK:
		if err := m5ai.ValidateSendText(input.Reply.Suggestion.Text); err != nil {
			return manualDecision(input.Turn.TurnID, ManualReplyInvalid, label, source), nil
		}
		return Decision{
			TurnID: input.Turn.TurnID, TurnStatus: TurnAdviceReady,
			IntentLabel: label, IntentSource: source,
			Action: &CommunicationActionPlan{
				TurnID: input.Turn.TurnID, Kind: CommunicationActionReplyText,
				Text: input.Reply.Suggestion.Text, Status: CommunicationActionPlanned,
			},
		}, nil
	default:
		return Decision{}, ErrInvalidReducerInput
	}
}

func validateFrozenTurn(turn FrozenTurnFacts) ([]string, ManualReason, error) {
	if strings.TrimSpace(turn.TurnID) == "" || len(turn.Messages) == 0 {
		return nil, "", ErrInvalidReducerInput
	}
	texts := make([]string, 0, len(turn.Messages))
	var previousSeq int64
	for index, message := range turn.Messages {
		if message.Seq <= 0 || (index > 0 && message.Seq <= previousSeq) {
			return nil, "", ErrInvalidReducerInput
		}
		previousSeq = message.Seq
		switch message.Kind {
		case FrozenMessageText:
			if strings.TrimSpace(message.Text) == "" {
				return nil, ManualUnsupportedSemantic, nil
			}
			texts = append(texts, message.Text)
		case FrozenMessageImage, FrozenMessageVoice, FrozenMessageFile:
			return nil, ManualUnsupportedMedia, nil
		case FrozenMessageCard, FrozenMessageSystem:
			return nil, ManualUnsupportedSemantic, nil
		default:
			return nil, ManualUnsupportedSemantic, nil
		}
	}
	return texts, "", nil
}

func validAdviceState(state AdviceState) bool {
	return state == AdviceAbsent || state == AdviceOK || state == AdviceFailed
}

func validIntentLabel(label m5ai.IntentLabel) bool {
	return label == m5ai.IntentInterested || label == m5ai.IntentRejected || label == m5ai.IntentNeutral
}

func manualDecision(turnID string, reason ManualReason, label m5ai.IntentLabel, source IntentSource) Decision {
	return Decision{
		TurnID: turnID, TurnStatus: TurnManualRequired,
		IntentLabel: label, IntentSource: source, ManualReason: reason,
	}
}
