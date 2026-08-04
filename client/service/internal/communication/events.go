package communication

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidBusinessEventInput = errors.New("沟通业务事件输入无效")

// BusinessEventKind is the small platform-neutral vocabulary consumed by the
// communication state machine. Platform enums and DOM/NIM shapes must be
// resolved before this boundary. Unknown means no automatic side effect.
type BusinessEventKind string

const (
	EventCandidateExpressionReceived BusinessEventKind = "candidateExpressionReceived"
	EventResumeSubmitted             BusinessEventKind = "resumeSubmitted"
	EventWechatRequested             BusinessEventKind = "wechatRequested"
	EventWechatInvited               BusinessEventKind = "wechatInvited"
	EventWechatExchanged             BusinessEventKind = "wechatExchanged"
	EventInterviewInvited            BusinessEventKind = "interviewInvited"
	EventInterviewAccepted           BusinessEventKind = "interviewAccepted"
	EventInterviewRejected           BusinessEventKind = "interviewRejected"
	EventHumanOutboundObserved       BusinessEventKind = "humanOutboundObserved"
	EventAutomaticOutboundObserved   BusinessEventKind = "automaticOutboundObserved"
	EventCandidateBlacklisted        BusinessEventKind = "candidateBlacklisted"
	EventSystemNotice                BusinessEventKind = "systemNotice"
	EventUnknownPlatform             BusinessEventKind = "unknownPlatformEvent"
)

type BusinessEventSource string

const (
	EventSourceMessage        BusinessEventSource = "message"
	EventSourceCardTransition BusinessEventSource = "cardTransition"
	EventSourcePlatformStatus BusinessEventSource = "platformStatus"
)

type ExpressionKind string

const (
	ExpressionText  ExpressionKind = "text"
	ExpressionImage ExpressionKind = "image"
	ExpressionVoice ExpressionKind = "voice"
	ExpressionFile  ExpressionKind = "file"
)

type BusinessEvent struct {
	Key              string              `json:"key"`
	Kind             BusinessEventKind   `json:"kind"`
	Source           BusinessEventSource `json:"source"`
	MessageSeq       int64               `json:"messageSeq"`
	OccurredAt       *time.Time          `json:"occurredAt,omitempty"`
	ExpressionKind   ExpressionKind      `json:"expressionKind,omitempty"`
	Text             string              `json:"text,omitempty"`
	IsBody           bool                `json:"isBody,omitempty"`
	BodyKindKnown    bool                `json:"bodyKindKnown,omitempty"`
	ConservativeCode string              `json:"conservativeCode,omitempty"`
	// InterviewMethod 只在我方邀面卡事件上有值,取账本里那张卡的平台无关
	// method 投影。它是"本次面试是线上还是线下"的唯一事实来源。
	InterviewMethod string `json:"interviewMethod,omitempty"`
}

// LedgerMessageFact deliberately mirrors only the platform-neutral message
// ledger. Text is local business data; callers must not put it in logs or
// reports. CardType is the public contract enum, never a platform contentType.
type LedgerMessageFact struct {
	Seq       int64
	Direction string
	Kind      string
	Text      *string
	CardType  string
	CardState string
	// InterviewMethod 直接取消息行的同名列(邀面卡的平台无关身份投影之一),
	// 非邀面卡恒为 nil。
	InterviewMethod *string
	Origin          string
	ActionKind      V4ActionKind
	TsApproxMs      *int64
}

type LedgerCardTransitionFact struct {
	MessageSeq int64
	CardType   string
	FromState  string
	ToState    string
	OccurredAt *time.Time
}

// NormalizeLedgerMessage performs strict promotion from a persisted neutral
// message to one business event. A valid but unsupported shape becomes
// unknownPlatformEvent instead of an error, so it remains observable without
// gaining authority to drive an automatic action.
func NormalizeLedgerMessage(fact LedgerMessageFact) (BusinessEvent, error) {
	if err := validateLedgerMessageFact(fact); err != nil {
		return BusinessEvent{}, err
	}
	event := BusinessEvent{
		Key: fmt.Sprintf("message:%d", fact.Seq), Source: EventSourceMessage,
		MessageSeq: fact.Seq, OccurredAt: timeFromUnixMilli(fact.TsApproxMs),
	}

	switch fact.Direction {
	case "in":
		return normalizeInboundMessage(event, fact), nil
	case "out":
		return normalizeOutboundMessage(event, fact), nil
	case "system":
		event.Kind = EventSystemNotice
		return event, nil
	default:
		return BusinessEvent{}, ErrInvalidBusinessEventInput
	}
}

func normalizeInboundMessage(event BusinessEvent, fact LedgerMessageFact) BusinessEvent {
	switch fact.Kind {
	case "text":
		if fact.Text == nil || strings.TrimSpace(*fact.Text) == "" {
			return unknownEvent(event, "emptyInboundText")
		}
		event.Kind = EventCandidateExpressionReceived
		event.ExpressionKind = ExpressionText
		event.Text = *fact.Text
		return event
	case "image":
		event.Kind = EventCandidateExpressionReceived
		event.ExpressionKind = ExpressionImage
		return event
	case "voice":
		event.Kind = EventCandidateExpressionReceived
		event.ExpressionKind = ExpressionVoice
		return event
	case "file":
		event.Kind = EventCandidateExpressionReceived
		event.ExpressionKind = ExpressionFile
		return event
	case "card":
		switch fact.CardType {
		case "resumeAttachment":
			event.Kind = EventResumeSubmitted
			return event
		case "wechatExchange":
			// The platform adapter may emit pending only after proving the
			// request shape and accepted only after proving the exchange-result
			// shape. Other states deliberately have no authority.
			switch fact.CardState {
			case "pending":
				event.Kind = EventWechatRequested
				return event
			case "accepted":
				event.Kind = EventWechatExchanged
				return event
			}
			return unknownEvent(event, "inboundWechatCardState")
		case "interviewInvite":
			if fact.CardState == "accepted" {
				event.Kind = EventInterviewAccepted
				return event
			}
			return unknownEvent(event, "inboundInterviewCardState")
		default:
			return unknownEvent(event, "unsupportedInboundCard")
		}
	case "system":
		event.Kind = EventSystemNotice
		return event
	default:
		return unknownEvent(event, "unsupportedInboundKind")
	}
}

func normalizeOutboundMessage(event BusinessEvent, fact LedgerMessageFact) BusinessEvent {
	if fact.Kind == "card" {
		switch fact.CardType {
		case "interviewInvite":
			event.Kind = EventInterviewInvited
			if fact.InterviewMethod != nil {
				event.InterviewMethod = *fact.InterviewMethod
			}
			return event
		case "wechatExchange":
			// 与入站分支同构：pending 只证明"请求已存在"，accepted 只证明
			// "交换结果已成立"。交换结果卡归属点同意的一方，我方接受候选人
			// 主动请求时它就是出站的；只按 cardType 一律读成"我方发起邀请"
			// 会把交换成功误记为又发了一次邀请，微信线永远推不到已换号。
			switch fact.CardState {
			case "pending":
				event.Kind = EventWechatInvited
				return event
			case "accepted":
				event.Kind = EventWechatExchanged
				return event
			}
			return unknownEvent(event, "outboundWechatCardState")
		default:
			return unknownEvent(event, "unsupportedOutboundCard")
		}
	}
	if fact.Origin == "self" {
		event.Kind = EventAutomaticOutboundObserved
		if fact.ActionKind != "" {
			event.IsBody, event.BodyKindKnown = classifyV4OutboundActionBody(fact.ActionKind)
		}
	} else {
		event.Kind = EventHumanOutboundObserved
		event.IsBody = fact.Kind != "card" && fact.Kind != "system"
		event.BodyKindKnown = true
	}
	return event
}

// NormalizeCardTransition promotes only contract-level card state changes.
// Platform-private state codes must never reach this function.
func NormalizeCardTransition(fact LedgerCardTransitionFact) (BusinessEvent, error) {
	if fact.MessageSeq <= 0 || !validNeutralCardType(fact.CardType) ||
		!validNeutralCardState(fact.FromState) || !validNeutralCardState(fact.ToState) ||
		fact.FromState == fact.ToState {
		return BusinessEvent{}, ErrInvalidBusinessEventInput
	}
	event := BusinessEvent{
		Key:    fmt.Sprintf("card:%d:%s:%s", fact.MessageSeq, fact.FromState, fact.ToState),
		Source: EventSourceCardTransition, MessageSeq: fact.MessageSeq,
		OccurredAt: copyTime(fact.OccurredAt),
	}
	switch fact.CardType {
	case "interviewInvite":
		switch fact.ToState {
		case "accepted":
			event.Kind = EventInterviewAccepted
		case "rejected":
			event.Kind = EventInterviewRejected
		default:
			return unknownEvent(event, "interviewCardTransition"), nil
		}
	case "wechatExchange":
		if fact.ToState == "accepted" {
			event.Kind = EventWechatExchanged
		} else {
			return unknownEvent(event, "wechatCardTransition"), nil
		}
	default:
		return unknownEvent(event, "unsupportedCardTransition"), nil
	}
	return event, nil
}

func unknownEvent(event BusinessEvent, code string) BusinessEvent {
	event.Kind = EventUnknownPlatform
	event.ConservativeCode = code
	return event
}

func validateLedgerMessageFact(fact LedgerMessageFact) error {
	if fact.Seq <= 0 || !validNeutralDirection(fact.Direction) ||
		!validNeutralMessageKind(fact.Kind) || (fact.Origin != "external" && fact.Origin != "self") {
		return ErrInvalidBusinessEventInput
	}
	if fact.TsApproxMs != nil && *fact.TsApproxMs <= 0 {
		return ErrInvalidBusinessEventInput
	}
	if fact.Kind == "card" {
		if !validNeutralCardType(fact.CardType) || !validNeutralCardState(fact.CardState) {
			return ErrInvalidBusinessEventInput
		}
	} else if fact.CardType != "" || fact.CardState != "" {
		return ErrInvalidBusinessEventInput
	}
	if fact.ActionKind != "" {
		_, known := classifyV4OutboundActionBody(fact.ActionKind)
		if fact.Direction != "out" || fact.Origin != "self" || !known {
			return ErrInvalidBusinessEventInput
		}
	}
	return nil
}

func timeFromUnixMilli(value *int64) *time.Time {
	if value == nil {
		return nil
	}
	at := time.UnixMilli(*value).UTC()
	return &at
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	at := value.UTC()
	return &at
}

func validNeutralDirection(value string) bool {
	return value == "in" || value == "out" || value == "system"
}

func validNeutralMessageKind(value string) bool {
	switch value {
	case "text", "image", "voice", "file", "card", "system":
		return true
	default:
		return false
	}
}

func validNeutralCardType(value string) bool {
	switch value {
	case "interviewInvite", "wechatExchange", "resumeAttachment", "other":
		return true
	default:
		return false
	}
}

func validNeutralCardState(value string) bool {
	switch value {
	case "pending", "accepted", "rejected", "expired", "unknown":
		return true
	default:
		return false
	}
}
