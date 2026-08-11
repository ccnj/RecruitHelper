package communication

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var ErrInvalidV4StateTransition = errors.New("v4 沟通状态或跃迁无效")

type V4MainStatus string

const (
	V4StatusGreeted       V4MainStatus = "greeted"
	V4StatusCommunicating V4MainStatus = "communicating"
	V4StatusInvited       V4MainStatus = "invited"
	V4StatusInterviewed   V4MainStatus = "interviewed"
	V4StatusEnded         V4MainStatus = "ended"
	V4StatusEliminated    V4MainStatus = "eliminated"
)

type V4EndReason string

const (
	V4EndRejected              V4EndReason = "rejected"
	V4EndBlacklisted           V4EndReason = "blacklisted"
	V4EndFallback              V4EndReason = "fallbackArchive"
	V4EndSilentInterview       V4EndReason = "silentInterviewPending"
	V4EndSilentWechatInvited   V4EndReason = "silentWechatInvited"
	V4EndSilentWechatExchanged V4EndReason = "silentWechatExchanged"
	V4EndSilent                V4EndReason = "silent"
)

type V4WechatStatus string

const (
	V4WechatNotInvited V4WechatStatus = "notInvited"
	V4WechatInvited    V4WechatStatus = "invited"
	V4WechatExchanged  V4WechatStatus = "exchanged"
)

type V4DialogueRequirement string

const (
	V4DialogueNone                      V4DialogueRequirement = "none"
	V4DialogueClassifyAndReply          V4DialogueRequirement = "classifyAndReply"
	V4DialogueReplyKnownInterested      V4DialogueRequirement = "replyKnownInterested"
	V4DialogueWechatContinuation        V4DialogueRequirement = "wechatContinuation"
	V4DialogueServiceReply              V4DialogueRequirement = "serviceReply"
	V4DialogueInterviewRejectionReceipt V4DialogueRequirement = "interviewRejectionReceipt"
)

type V4ActionKind string

const (
	V4ActionAcceptWechat             V4ActionKind = "acceptWechat"
	V4ActionNotifyWechat             V4ActionKind = "notifyWechat"
	V4ActionWechatReceipt            V4ActionKind = "wechatReceipt"
	V4ActionInterviewAcceptedReceipt V4ActionKind = "interviewAcceptedReceipt"
	V4ActionNotifyInterviewAccepted  V4ActionKind = "notifyInterviewAccepted"
	V4ActionInviteWechat             V4ActionKind = "inviteWechat"
	V4ActionReplyText                V4ActionKind = "replyText"
	V4ActionServiceReply             V4ActionKind = "serviceReply"
	V4ActionRejectionRetention       V4ActionKind = "rejectionRetention"
	V4ActionRejectionClosing         V4ActionKind = "rejectionClosing"
	V4ActionInterviewRejectionReply  V4ActionKind = "interviewRejectionReply"
	V4ActionColdPrompt               V4ActionKind = "coldPrompt"
	V4ActionColdWechatText           V4ActionKind = "coldWechatText"
	V4ActionColdWechatInvite         V4ActionKind = "coldWechatInvite"
	V4ActionInterviewFollowup        V4ActionKind = "interviewFollowup"
	V4ActionInterviewInvite          V4ActionKind = "interviewInvite"
	V4ActionArchive                  V4ActionKind = "archive"
)

type V4ManualReason string

const (
	V4ManualUnknownPlatformEvent V4ManualReason = "unknownPlatformEvent"
	V4ManualInvalidTransition    V4ManualReason = "invalidTransition"
	V4ManualInterviewCardMissing V4ManualReason = "interviewCardMissing"
)

type V4RejectionStage string

const (
	V4RejectionStageRetention V4RejectionStage = "retention"
	V4RejectionStageClosing   V4RejectionStage = "closing"
	V4RejectionStageArchive   V4RejectionStage = "archive"
)

type V4InterviewFollowupGroup struct {
	MessageSeq           int64 `json:"messageSeq"`
	NextStage            uint8 `json:"nextStage"`
	Active               bool  `json:"active"`
	Rejected             bool  `json:"rejected"`
	RejectionReceiptSent bool  `json:"rejectionReceiptSent"`
}

// V4State is a platform-independent aggregate. It contains only monotone
// business facts needed to derive the next communication decision. Planning an
// effect must not mutate this value; only an observed platform fact or a
// positive action confirmation may do so.
type V4State struct {
	MainStatus  V4MainStatus   `json:"mainStatus"`
	EndReason   V4EndReason    `json:"endReason,omitempty"`
	WechatState V4WechatStatus `json:"wechatState"`

	ColdPromptRemaining    uint8  `json:"coldPromptRemaining"`
	ColdWechatRemaining    uint8  `json:"coldWechatRemaining"`
	ColdPromptSentCount    uint8  `json:"coldPromptSentCount"`
	RealMessageRound       uint64 `json:"realMessageRound"`
	LastColdPromptRound    uint64 `json:"lastColdPromptRound"`
	LastRealMessageSeq     int64  `json:"lastRealMessageSeq"`
	LastOutboundMessageSeq int64  `json:"lastOutboundMessageSeq"`

	RetentionSent                bool             `json:"retentionSent"`
	ClosingSent                  bool             `json:"closingSent"`
	ColdWechatTextSent           bool             `json:"coldWechatTextSent"`
	WechatReceiptSent            bool             `json:"wechatReceiptSent"`
	InterviewAcceptedReceiptSent bool             `json:"interviewAcceptedReceiptSent"`
	RejectionTurnMessageSeq      int64            `json:"rejectionTurnMessageSeq"`
	RejectionTurnID              string           `json:"rejectionTurnId,omitempty"`
	RejectionStage               V4RejectionStage `json:"rejectionStage,omitempty"`

	LastOutboundAt     *time.Time `json:"lastOutboundAt,omitempty"`
	LastBodyAt         *time.Time `json:"lastBodyAt,omitempty"`
	ClockUncertain     bool       `json:"clockUncertain"`
	BodyClockUncertain bool       `json:"bodyClockUncertain"`

	InterviewGroups []V4InterviewFollowupGroup `json:"interviewGroups"`

	// InterviewMethod 是本次面试的类型,取我方邀面卡进账本时那张卡的 method。
	// 平台一个候选人终身只有一张邀面卡,改面试尚未开发,所以它一旦写下就不再
	// 变动。空值表示"这张卡没有类型投影"——线下能力上线前发出的卡与历史未
	// 映射数据都是空,按线上处理(《沟通逻辑规格-v4》事件表,2026-08-04 裁决)。
	InterviewMethod string `json:"interviewMethod,omitempty"`
}

type V4EventAction struct {
	ActionKey      string       `json:"actionKey"`
	Kind           V4ActionKind `json:"kind"`
	CardMessageSeq int64        `json:"cardMessageSeq,omitempty"`
}

type V4EventDecision struct {
	State                V4State
	Dialogue             V4DialogueRequirement
	DialogueAfterActions bool
	Actions              []V4EventAction
	ManualReason         V4ManualReason
}

type V4ConfirmedAction struct {
	ActionKey      string       `json:"actionKey"`
	Kind           V4ActionKind `json:"kind"`
	MessageSeq     int64        `json:"messageSeq,omitempty"`
	CardMessageSeq int64        `json:"cardMessageSeq,omitempty"`
	SentAt         *time.Time   `json:"sentAt,omitempty"`
	Round          uint64       `json:"round,omitempty"`
	Stage          uint8        `json:"stage,omitempty"`
}

type v4ExpressionDisposition uint8

const (
	v4ExpressionNew v4ExpressionDisposition = iota + 1
	v4ExpressionReplay
	v4ExpressionStale
)

func NewV4GreetedState(greetedAt *time.Time) V4State {
	state := V4State{
		MainStatus: V4StatusGreeted, WechatState: V4WechatNotInvited,
		ColdPromptRemaining: 2, ColdWechatRemaining: 1, RealMessageRound: 1,
	}
	if greetedAt == nil {
		state.ClockUncertain = true
		state.BodyClockUncertain = true
		return state
	}
	state.LastOutboundAt = copyTime(greetedAt)
	state.LastBodyAt = copyTime(greetedAt)
	return state
}

// NewV4InboundConversationState creates the honest pre-projection state for a
// candidate-initiated conversation. There is no fabricated greeting or
// outbound clock: the first stable inbound message is still pending at the
// aggregate's sequence-zero projection boundary and will become real-message
// round one through the ordinary reducer.
func NewV4InboundConversationState() V4State {
	return V4State{
		MainStatus:             V4StatusCommunicating,
		WechatState:            V4WechatNotInvited,
		ColdPromptRemaining:    2,
		ColdWechatRemaining:    1,
		ClockUncertain:         true,
		BodyClockUncertain:     true,
		RealMessageRound:       0,
		LastRealMessageSeq:     0,
		LastOutboundMessageSeq: 0,
	}
}

// ApplyV4BusinessEvent consumes one already-normalized fact. The caller may
// replay the same fact: message events are deduplicated by their stable ledger
// sequence, card-derived plans carry stable ActionKey values, and no budget or
// sent marker advances here.
func ApplyV4BusinessEvent(input V4State, event BusinessEvent) (V4EventDecision, error) {
	if err := validateV4State(input); err != nil || strings.TrimSpace(event.Key) == "" {
		return V4EventDecision{}, ErrInvalidV4StateTransition
	}
	state := cloneV4State(input)
	decision := V4EventDecision{State: state, Dialogue: V4DialogueNone}

	switch event.Kind {
	case EventCandidateExpressionReceived:
		return applyV4RealExpression(decision, event, V4DialogueClassifyAndReply)
	case EventResumeSubmitted:
		return applyV4RealExpression(decision, event, V4DialogueReplyKnownInterested)
	case EventWechatRequested:
		decision, disposition, err := applyV4RealExpressionDecision(decision, event)
		if err != nil || disposition == v4ExpressionStale || decision.State.MainStatus == V4StatusEliminated {
			return decision, err
		}
		if decision.State.MainStatus == V4StatusInterviewed {
			// 服务态只保留确定性的接受与收号通知，不安排 AI 对话跟随：没有
			// 约面可推，候选人可见回执由随后的 wechatExchanged 事件按固定
			// 确认语给出（规格 §七"收号+回执"）。
			decision.Actions = append(decision.Actions,
				eventAction(event, V4ActionAcceptWechat),
				eventAction(event, V4ActionNotifyWechat),
			)
			return decision, nil
		}
		decision.Dialogue = dialogueForState(decision.State, V4DialogueWechatContinuation)
		if decision.Dialogue != V4DialogueNone {
			decision.DialogueAfterActions = true
			decision.Actions = append(decision.Actions,
				eventAction(event, V4ActionAcceptWechat),
				eventAction(event, V4ActionNotifyWechat),
			)
		}
		return decision, nil
	case EventWechatInvited:
		advanceV4Wechat(&decision.State, V4WechatInvited)
		advanceV4OutboundClock(&decision.State, event.MessageSeq, event.OccurredAt, false)
		return decision, nil
	case EventWechatExchanged:
		advanceV4Wechat(&decision.State, V4WechatExchanged)
		decision.Actions = append(decision.Actions, eventAction(event, V4ActionNotifyWechat))
		if !decision.State.WechatReceiptSent {
			decision.Actions = append(decision.Actions, eventAction(event, V4ActionWechatReceipt))
		}
		return decision, nil
	case EventInterviewInvited:
		if decision.State.MainStatus != V4StatusCommunicating && decision.State.MainStatus != V4StatusInvited {
			return manualV4Decision(input, V4ManualInvalidTransition), nil
		}
		if event.MessageSeq <= 0 {
			return V4EventDecision{}, ErrInvalidV4StateTransition
		}
		decision.State.MainStatus = V4StatusInvited
		// 面试类型锚在"卡已进账本"这个事实上,不锚 AI 建议:建议到卡真正躺进
		// 会话之间还隔着时段命中、业务前置与发后正证。
		decision.State.InterviewMethod = event.InterviewMethod
		addV4InterviewGroup(&decision.State, event.MessageSeq)
		advanceV4OutboundClock(&decision.State, event.MessageSeq, event.OccurredAt, false)
		return decision, nil
	case EventInterviewAccepted:
		return applyV4InterviewAccepted(input, decision, event)
	case EventInterviewRejected:
		return applyV4InterviewRejected(input, decision, event)
	case EventHumanOutboundObserved, EventAutomaticOutboundObserved:
		advanceV4OutboundClock(&decision.State, event.MessageSeq, event.OccurredAt, event.IsBody)
		if !event.BodyKindKnown {
			decision.State.BodyClockUncertain = true
		}
		return decision, nil
	case EventCandidateBlacklisted:
		if isV4ProgressStatus(decision.State.MainStatus) {
			archiveV4State(&decision.State, V4EndBlacklisted)
		}
		return decision, nil
	case EventSystemNotice:
		return decision, nil
	case EventUnknownPlatform:
		return manualV4Decision(input, V4ManualUnknownPlatformEvent), nil
	default:
		return V4EventDecision{}, ErrInvalidV4StateTransition
	}
}

// ApplyV4ConfirmedAction advances sent flags, budgets and clocks only after the
// caller has a unique positive postcondition. Replaying a confirmation is
// idempotent; planning or clicking alone must never call this function.
func ApplyV4ConfirmedAction(input V4State, action V4ConfirmedAction) (V4State, error) {
	if err := validateV4State(input); err != nil || strings.TrimSpace(action.ActionKey) == "" {
		return V4State{}, ErrInvalidV4StateTransition
	}
	state := cloneV4State(input)

	switch action.Kind {
	case V4ActionReplyText, V4ActionServiceReply:
		if action.MessageSeq <= 0 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, isV4BodyAction(action.Kind))
	case V4ActionRejectionRetention:
		if action.MessageSeq <= 0 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		state.RetentionSent = true
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, isV4BodyAction(action.Kind))
	case V4ActionRejectionClosing:
		if action.MessageSeq <= 0 || !(state.RetentionSent || state.ColdWechatTextSent) {
			return V4State{}, ErrInvalidV4StateTransition
		}
		state.ClosingSent = true
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, isV4BodyAction(action.Kind))
		archiveV4State(&state, V4EndRejected)
	case V4ActionWechatReceipt:
		if action.MessageSeq <= 0 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		state.WechatReceiptSent = true
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, isV4BodyAction(action.Kind))
	case V4ActionInterviewAcceptedReceipt:
		if action.MessageSeq <= 0 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		state.InterviewAcceptedReceiptSent = true
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, isV4BodyAction(action.Kind))
	case V4ActionInterviewRejectionReply:
		if action.MessageSeq <= 0 || action.CardMessageSeq <= 0 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		index := exactV4InterviewGroup(state.InterviewGroups, action.CardMessageSeq)
		if index < 0 || !state.InterviewGroups[index].Rejected {
			return V4State{}, ErrInvalidV4StateTransition
		}
		state.InterviewGroups[index].RejectionReceiptSent = true
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, isV4BodyAction(action.Kind))
	case V4ActionInviteWechat:
		if action.MessageSeq <= 0 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		advanceV4Wechat(&state, V4WechatInvited)
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, false)
	case V4ActionAcceptWechat:
		advanceV4Wechat(&state, V4WechatExchanged)
	case V4ActionInterviewInvite:
		if action.MessageSeq <= 0 || (state.MainStatus != V4StatusCommunicating && state.MainStatus != V4StatusInvited) {
			return V4State{}, ErrInvalidV4StateTransition
		}
		state.MainStatus = V4StatusInvited
		addV4InterviewGroup(&state, action.MessageSeq)
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, false)
	case V4ActionColdPrompt:
		if action.MessageSeq <= 0 || action.Round == 0 || action.Round > state.RealMessageRound || action.Stage < 1 || action.Stage > 2 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		if action.Stage <= state.ColdPromptSentCount {
			return state, nil
		}
		if action.Stage != state.ColdPromptSentCount+1 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		state.ColdPromptSentCount++
		if state.ColdPromptRemaining > 0 {
			state.ColdPromptRemaining--
		}
		if action.Round == state.RealMessageRound {
			state.LastColdPromptRound = action.Round
		}
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, isV4BodyAction(action.Kind))
	case V4ActionColdWechatText:
		if action.MessageSeq <= 0 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		state.ColdWechatTextSent = true
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, isV4BodyAction(action.Kind))
	case V4ActionColdWechatInvite:
		if action.MessageSeq <= 0 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		if state.ColdWechatRemaining > 0 {
			state.ColdWechatRemaining--
		}
		advanceV4Wechat(&state, V4WechatInvited)
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, false)
	case V4ActionInterviewFollowup:
		if action.MessageSeq <= 0 || action.CardMessageSeq <= 0 || action.Stage < 1 || action.Stage > 3 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		index := exactV4InterviewGroup(state.InterviewGroups, action.CardMessageSeq)
		if index < 0 {
			return V4State{}, ErrInvalidV4StateTransition
		}
		group := &state.InterviewGroups[index]
		if group.NextStage > action.Stage {
			return state, nil
		}
		if group.NextStage != action.Stage {
			return V4State{}, ErrInvalidV4StateTransition
		}
		group.NextStage++
		if group.NextStage > 3 {
			group.Active = false
		}
		advanceV4OutboundClock(&state, action.MessageSeq, action.SentAt, isV4BodyAction(action.Kind))
	case V4ActionNotifyWechat, V4ActionNotifyInterviewAccepted:
		// Notifications are externally deduplicated by ActionKey and do not
		// change the profile communication aggregate.
	default:
		return V4State{}, ErrInvalidV4StateTransition
	}
	if err := validateV4State(state); err != nil {
		return V4State{}, err
	}
	return state, nil
}

func applyV4RealExpression(decision V4EventDecision, event BusinessEvent, normal V4DialogueRequirement) (V4EventDecision, error) {
	decision, disposition, err := applyV4RealExpressionDecision(decision, event)
	if err != nil || disposition == v4ExpressionStale || decision.State.MainStatus == V4StatusEliminated {
		return decision, err
	}
	decision.Dialogue = dialogueForState(decision.State, normal)
	return decision, nil
}

func applyV4RealExpressionDecision(decision V4EventDecision, event BusinessEvent) (V4EventDecision, v4ExpressionDisposition, error) {
	if event.MessageSeq <= 0 {
		return V4EventDecision{}, 0, ErrInvalidV4StateTransition
	}
	if event.MessageSeq < decision.State.LastRealMessageSeq {
		return decision, v4ExpressionStale, nil
	}
	if event.MessageSeq == decision.State.LastRealMessageSeq {
		return decision, v4ExpressionReplay, nil
	}
	decision.State.LastRealMessageSeq = event.MessageSeq
	if decision.State.MainStatus == V4StatusEliminated {
		return decision, v4ExpressionNew, nil
	}
	decision.State.RealMessageRound++
	deactivateV4InterviewGroups(&decision.State)
	switch decision.State.MainStatus {
	case V4StatusGreeted, V4StatusEnded:
		decision.State.MainStatus = V4StatusCommunicating
		decision.State.EndReason = ""
	case V4StatusCommunicating, V4StatusInvited, V4StatusInterviewed:
	default:
		return V4EventDecision{}, 0, ErrInvalidV4StateTransition
	}
	return decision, v4ExpressionNew, nil
}

func dialogueForState(state V4State, normal V4DialogueRequirement) V4DialogueRequirement {
	switch state.MainStatus {
	case V4StatusInterviewed:
		return V4DialogueServiceReply
	case V4StatusCommunicating, V4StatusInvited:
		return normal
	default:
		return V4DialogueNone
	}
}

func applyV4InterviewAccepted(input V4State, decision V4EventDecision, event BusinessEvent) (V4EventDecision, error) {
	switch decision.State.MainStatus {
	case V4StatusCommunicating, V4StatusInvited, V4StatusEnded:
		decision.State.MainStatus = V4StatusInterviewed
		decision.State.EndReason = ""
		deactivateV4InterviewGroups(&decision.State)
	case V4StatusInterviewed:
		// The state transition may already be durable while one of the
		// event-derived actions is not. Re-emit the same stable action keys;
		// the action store, not this pure reducer, owns exactly-once creation.
	case V4StatusEliminated:
		return decision, nil
	default:
		return manualV4Decision(input, V4ManualInvalidTransition), nil
	}
	if !decision.State.InterviewAcceptedReceiptSent {
		decision.Actions = append(decision.Actions, eventAction(event, V4ActionInterviewAcceptedReceipt))
	}
	decision.Actions = append(decision.Actions, eventAction(event, V4ActionNotifyInterviewAccepted))
	if decision.State.WechatState == V4WechatNotInvited {
		decision.Actions = append(decision.Actions, eventAction(event, V4ActionInviteWechat))
	}
	return decision, nil
}

func applyV4InterviewRejected(input V4State, decision V4EventDecision, event BusinessEvent) (V4EventDecision, error) {
	if decision.State.MainStatus == V4StatusInterviewed || decision.State.MainStatus == V4StatusEliminated || decision.State.MainStatus == V4StatusEnded {
		return decision, nil
	}
	index := exactV4InterviewGroup(decision.State.InterviewGroups, event.MessageSeq)
	if index < 0 {
		return manualV4Decision(input, V4ManualInterviewCardMissing), nil
	}
	group := &decision.State.InterviewGroups[index]
	group.Active = false
	group.Rejected = true
	if !group.RejectionReceiptSent {
		decision.Dialogue = V4DialogueInterviewRejectionReceipt
	}
	return decision, nil
}

func eventAction(event BusinessEvent, kind V4ActionKind) V4EventAction {
	return V4EventAction{
		ActionKey: fmt.Sprintf("%s|%s", event.Key, kind), Kind: kind,
		CardMessageSeq: event.MessageSeq,
	}
}

func manualV4Decision(state V4State, reason V4ManualReason) V4EventDecision {
	return V4EventDecision{State: cloneV4State(state), Dialogue: V4DialogueNone, ManualReason: reason}
}

func advanceV4Wechat(state *V4State, wanted V4WechatStatus) {
	if v4WechatRank(wanted) > v4WechatRank(state.WechatState) {
		state.WechatState = wanted
	}
}

func v4WechatRank(status V4WechatStatus) int {
	switch status {
	case V4WechatNotInvited:
		return 0
	case V4WechatInvited:
		return 1
	case V4WechatExchanged:
		return 2
	default:
		return -1
	}
}

func addV4InterviewGroup(state *V4State, messageSeq int64) {
	if exactV4InterviewGroup(state.InterviewGroups, messageSeq) >= 0 {
		return
	}
	index := sort.Search(len(state.InterviewGroups), func(index int) bool {
		return state.InterviewGroups[index].MessageSeq >= messageSeq
	})
	group := V4InterviewFollowupGroup{MessageSeq: messageSeq, NextStage: 1, Active: true}
	state.InterviewGroups = append(state.InterviewGroups, V4InterviewFollowupGroup{})
	copy(state.InterviewGroups[index+1:], state.InterviewGroups[index:])
	state.InterviewGroups[index] = group
}

func exactV4InterviewGroup(groups []V4InterviewFollowupGroup, messageSeq int64) int {
	index := sort.Search(len(groups), func(index int) bool { return groups[index].MessageSeq >= messageSeq })
	if index == len(groups) || groups[index].MessageSeq != messageSeq {
		return -1
	}
	return index
}

func deactivateV4InterviewGroups(state *V4State) {
	for index := range state.InterviewGroups {
		state.InterviewGroups[index].Active = false
	}
}

func archiveV4State(state *V4State, reason V4EndReason) {
	state.MainStatus = V4StatusEnded
	state.EndReason = reason
	state.ColdPromptRemaining = 0
	state.ColdWechatRemaining = 0
	deactivateV4InterviewGroups(state)
}

func advanceV4OutboundClock(state *V4State, messageSeq int64, occurredAt *time.Time, isBody bool) {
	if messageSeq <= 0 {
		state.ClockUncertain = true
		return
	}
	if messageSeq < state.LastOutboundMessageSeq {
		return
	}
	if messageSeq > state.LastOutboundMessageSeq {
		state.LastOutboundMessageSeq = messageSeq
	}
	if occurredAt == nil {
		state.ClockUncertain = true
		if isBody {
			state.BodyClockUncertain = true
		}
		return
	}
	at := occurredAt.UTC()
	if state.LastOutboundAt != nil && at.Before(*state.LastOutboundAt) {
		state.ClockUncertain = true
		if isBody {
			state.BodyClockUncertain = true
		}
		return
	}
	state.LastOutboundAt = &at
	state.ClockUncertain = false
	if isBody {
		bodyAt := at
		state.LastBodyAt = &bodyAt
		state.BodyClockUncertain = false
	}
}

// classifyV4OutboundActionBody is the single v4 definition of a body message.
// The specification exhaustively limits bodies to replies, retention and
// closing. Cards, cold prompts, follow-ups and receipts still slide the normal
// outbound clock, but never the seven-day fallback clock.
func classifyV4OutboundActionBody(kind V4ActionKind) (isBody bool, known bool) {
	switch kind {
	case V4ActionReplyText, V4ActionServiceReply, V4ActionRejectionRetention, V4ActionRejectionClosing:
		return true, true
	case V4ActionWechatReceipt, V4ActionInterviewAcceptedReceipt, V4ActionInterviewRejectionReply,
		V4ActionInviteWechat, V4ActionAcceptWechat, V4ActionColdPrompt, V4ActionColdWechatText,
		V4ActionColdWechatInvite, V4ActionInterviewFollowup, V4ActionInterviewInvite:
		return false, true
	default:
		return false, false
	}
}

func isV4BodyAction(kind V4ActionKind) bool {
	isBody, _ := classifyV4OutboundActionBody(kind)
	return isBody
}

func cloneV4State(state V4State) V4State {
	state.LastOutboundAt = copyTime(state.LastOutboundAt)
	state.LastBodyAt = copyTime(state.LastBodyAt)
	state.InterviewGroups = append([]V4InterviewFollowupGroup(nil), state.InterviewGroups...)
	return state
}

func isV4ProgressStatus(status V4MainStatus) bool {
	return status == V4StatusGreeted || status == V4StatusCommunicating || status == V4StatusInvited
}

func validateV4State(state V4State) error {
	switch state.MainStatus {
	case V4StatusGreeted, V4StatusCommunicating, V4StatusInvited, V4StatusInterviewed, V4StatusEnded, V4StatusEliminated:
	default:
		return ErrInvalidV4StateTransition
	}
	if v4WechatRank(state.WechatState) < 0 || state.ColdPromptRemaining > 2 || state.ColdWechatRemaining > 1 ||
		state.ColdPromptSentCount > 2 || state.ColdPromptRemaining+state.ColdPromptSentCount > 2 ||
		state.LastColdPromptRound > state.RealMessageRound || state.LastRealMessageSeq < 0 || state.LastOutboundMessageSeq < 0 {
		return ErrInvalidV4StateTransition
	}
	if (state.MainStatus == V4StatusEnded) != (state.EndReason != "") ||
		(state.EndReason != "" && !validV4EndReason(state.EndReason)) ||
		(state.ClosingSent && !state.RetentionSent && !state.ColdWechatTextSent) {
		return ErrInvalidV4StateTransition
	}
	if (state.RejectionTurnMessageSeq == 0) != (state.RejectionStage == "") ||
		(state.RejectionTurnMessageSeq == 0) != (state.RejectionTurnID == "") ||
		state.RejectionTurnMessageSeq < 0 || !validV4RejectionStage(state.RejectionStage) ||
		(state.RejectionStage == V4RejectionStageClosing && !state.RetentionSent && !state.ColdWechatTextSent) ||
		(state.RejectionStage == V4RejectionStageArchive &&
			((!state.RetentionSent && !state.ColdWechatTextSent) || !state.ClosingSent)) {
		return ErrInvalidV4StateTransition
	}
	previousSeq := int64(0)
	for _, group := range state.InterviewGroups {
		if group.MessageSeq <= previousSeq || group.NextStage < 1 || group.NextStage > 4 ||
			(group.Active && (group.Rejected || group.NextStage > 3)) {
			return ErrInvalidV4StateTransition
		}
		previousSeq = group.MessageSeq
	}
	return nil
}

// ValidateV4State exposes the aggregate invariant to persistence and
// orchestration layers without letting either layer duplicate the validation
// rules. All durable V4 states must pass this exact validator before commit.
func ValidateV4State(state V4State) error {
	return validateV4State(state)
}

func validV4RejectionStage(stage V4RejectionStage) bool {
	switch stage {
	case "", V4RejectionStageRetention, V4RejectionStageClosing, V4RejectionStageArchive:
		return true
	default:
		return false
	}
}

func validV4EndReason(reason V4EndReason) bool {
	switch reason {
	case V4EndRejected, V4EndBlacklisted, V4EndFallback, V4EndSilentInterview,
		V4EndSilentWechatInvited, V4EndSilentWechatExchanged, V4EndSilent:
		return true
	default:
		return false
	}
}
